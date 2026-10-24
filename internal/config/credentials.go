package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ClaudeConfigDirEnv is the environment variable Claude Code reads to locate
// its config directory — and therefore its OAuth credential store.
const ClaudeConfigDirEnv = "CLAUDE_CONFIG_DIR"

// CredentialsFileName is the OAuth credential store Claude Code writes inside
// CLAUDE_CONFIG_DIR on Linux/WSL. macOS keeps credentials in the login
// Keychain instead; see internal/quota/keychain.go for that path.
const CredentialsFileName = ".credentials.json"

// SkipAuthPreflightEnv disables the spawn-path credential gate when set to a
// truthy value ("1", "true", "yes"). Escape hatch for operators who know the
// credential store is fine and want the session launched anyway.
const SkipAuthPreflightEnv = "GT_SKIP_AUTH_PREFLIGHT"

// CredentialExpiryWindow is how far ahead of expiry an access token is already
// treated as expiring. A token that dies during startup strands the agent the
// same way an already-dead one does.
const CredentialExpiryWindow = 5 * time.Minute

// CredentialStatus is the verdict of a credential check.
type CredentialStatus int

const (
	// CredentialsUnknown means the credential state could not be determined:
	// no store on disk, unreadable, or a shape this code does not recognize.
	// Never blocks a spawn — an absent store is normal on macOS (Keychain) and
	// for API-key auth.
	CredentialsUnknown CredentialStatus = iota

	// CredentialsValid means the access token is good for at least
	// CredentialExpiryWindow.
	CredentialsValid

	// CredentialsExpiring means the access token expires within
	// CredentialExpiryWindow but has not expired yet.
	CredentialsExpiring

	// CredentialsExpired means the access token has expired. A refresh token
	// is present and still valid, so re-authentication is cheap.
	CredentialsExpired

	// CredentialsUnusable means the access token has expired and there is no
	// usable refresh token behind it. Full re-login required.
	CredentialsUnusable
)

func (s CredentialStatus) String() string {
	switch s {
	case CredentialsValid:
		return "valid"
	case CredentialsExpiring:
		return "expiring"
	case CredentialsExpired:
		return "expired"
	case CredentialsUnusable:
		return "unusable"
	default:
		return "unknown"
	}
}

// CredentialCheck is the result of inspecting a Claude Code credential store.
type CredentialCheck struct {
	// Status is the verdict.
	Status CredentialStatus

	// ConfigDir is the config directory that was inspected.
	ConfigDir string

	// Path is the credential file that was inspected. It may not exist.
	Path string

	// ExpiresAt is when the access token expires. Zero if not determined.
	ExpiresAt time.Time

	// RefreshExpiresAt is when the refresh token expires. Zero if the store
	// does not carry a refresh expiry.
	RefreshExpiresAt time.Time

	// Err explains a CredentialsUnknown verdict. Nil otherwise.
	Err error
}

// OK reports whether a session may be launched against this credential store.
// Unknown states are permissive: this check is a guard against a known hang,
// not an authentication authority.
func (c CredentialCheck) OK() bool {
	return c.Status != CredentialsExpired && c.Status != CredentialsUnusable
}

// claudeCredentialsFile mirrors the subset of .credentials.json Gas Town reads.
// Timestamps are Unix milliseconds.
type claudeCredentialsFile struct {
	OAuth *struct {
		AccessToken           string `json:"accessToken"`
		RefreshToken          string `json:"refreshToken"`
		ExpiresAt             int64  `json:"expiresAt"`
		RefreshTokenExpiresAt int64  `json:"refreshTokenExpiresAt"`
	} `json:"claudeAiOauth"`
}

// ResolveClaudeConfigDir returns the config directory to inspect. An explicit
// dir wins; otherwise CLAUDE_CONFIG_DIR, then the ~/.claude default.
func ResolveClaudeConfigDir(configDir string) string {
	if configDir != "" {
		return expandPath(configDir)
	}
	if env := os.Getenv(ClaudeConfigDirEnv); env != "" {
		return expandPath(env)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// CheckClaudeCredentials inspects the OAuth credential store for a config dir
// and reports whether an agent launched against it will be able to
// authenticate.
//
// This exists because Claude Code does not fail fast on a stale token: it falls
// back to the interactive onboarding flow and parks there. The tmux session
// exists, the process runs, nothing hits stderr — so every liveness check
// passes while the agent never reaches a prompt (hq-ac0, hq-jri).
//
// A missing or unrecognized store yields CredentialsUnknown, not an error
// verdict. macOS stores credentials in the Keychain, and API-key auth has no
// OAuth store at all; neither should block a spawn.
func CheckClaudeCredentials(configDir string) CredentialCheck {
	return checkClaudeCredentialsAt(configDir, time.Now())
}

func checkClaudeCredentialsAt(configDir string, now time.Time) CredentialCheck {
	resolved := ResolveClaudeConfigDir(configDir)
	check := CredentialCheck{ConfigDir: resolved}
	if resolved == "" {
		check.Err = errors.New("cannot resolve Claude config dir")
		return check
	}
	check.Path = filepath.Join(resolved, CredentialsFileName)

	data, err := os.ReadFile(check.Path)
	if err != nil {
		check.Err = err
		return check
	}

	var creds claudeCredentialsFile
	if err := json.Unmarshal(data, &creds); err != nil {
		check.Err = fmt.Errorf("parsing %s: %w", check.Path, err)
		return check
	}
	if creds.OAuth == nil || creds.OAuth.ExpiresAt <= 0 {
		check.Err = fmt.Errorf("%s has no claudeAiOauth expiry", check.Path)
		return check
	}

	check.ExpiresAt = time.UnixMilli(creds.OAuth.ExpiresAt)
	if creds.OAuth.RefreshTokenExpiresAt > 0 {
		check.RefreshExpiresAt = time.UnixMilli(creds.OAuth.RefreshTokenExpiresAt)
	}

	if now.Before(check.ExpiresAt) {
		if now.Add(CredentialExpiryWindow).Before(check.ExpiresAt) {
			check.Status = CredentialsValid
		} else {
			check.Status = CredentialsExpiring
		}
		return check
	}

	// Access token is dead. Whether that is recoverable depends on the refresh
	// token behind it.
	refreshUsable := creds.OAuth.RefreshToken != "" &&
		(check.RefreshExpiresAt.IsZero() || now.Before(check.RefreshExpiresAt))
	if refreshUsable {
		check.Status = CredentialsExpired
	} else {
		check.Status = CredentialsUnusable
	}
	return check
}

// Describe renders a one-line, operator-facing summary of the check.
func (c CredentialCheck) Describe() string {
	switch c.Status {
	case CredentialsValid:
		return fmt.Sprintf("OAuth token valid until %s", c.ExpiresAt.Format(time.RFC3339))
	case CredentialsExpiring:
		return fmt.Sprintf("OAuth token expires at %s (within %s)", c.ExpiresAt.Format(time.RFC3339), CredentialExpiryWindow)
	case CredentialsExpired:
		return fmt.Sprintf("OAuth token expired at %s", c.ExpiresAt.Format(time.RFC3339))
	case CredentialsUnusable:
		return fmt.Sprintf("OAuth token expired at %s and the refresh token is gone or expired", c.ExpiresAt.Format(time.RFC3339))
	default:
		if c.Err != nil {
			return fmt.Sprintf("OAuth token state unknown: %v", c.Err)
		}
		return "OAuth token state unknown"
	}
}

// remedy returns the operator instruction for a blocking verdict.
func (c CredentialCheck) remedy() string {
	return fmt.Sprintf("re-authenticate with: %s=%s claude   (then retry)\n"+
		"to launch anyway, set %s=1", ClaudeConfigDirEnv, c.ConfigDir, SkipAuthPreflightEnv)
}

// usesOAuth reports whether a runtime authenticates via Claude Code's OAuth
// credential store. Runtimes that pin ANTHROPIC_API_KEY or redirect
// ANTHROPIC_BASE_URL (e.g. the groq-compound preset, which drives the claude
// binary against Groq) do not, even though they set CLAUDE_CONFIG_DIR.
func usesOAuth(rc *RuntimeConfig) bool {
	if rc == nil {
		return false
	}
	if rc.Session == nil || rc.Session.ConfigDirEnv != ClaudeConfigDirEnv {
		return false
	}
	for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"} {
		if v, ok := rc.Env[k]; ok && v != "" {
			return false
		}
	}
	return true
}

// PreflightAgentAuth is the spawn-path credential gate.
//
// It returns a non-nil error when the session must not be launched — the token
// is dead and the agent would silently park on Claude Code's onboarding flow
// instead of reaching a prompt. It returns a non-empty warning when launch may
// proceed but the operator should know the token is close to the edge.
//
// Both return values are empty for non-OAuth runtimes, for credential stores
// this code cannot read, and when SkipAuthPreflightEnv is set.
func PreflightAgentAuth(rc *RuntimeConfig, configDir string) (warning string, err error) {
	if !usesOAuth(rc) {
		return "", nil
	}
	if isTruthyEnv(os.Getenv(SkipAuthPreflightEnv)) {
		return "", nil
	}

	check := CheckClaudeCredentials(configDir)
	switch check.Status {
	case CredentialsExpired, CredentialsUnusable:
		return "", fmt.Errorf("%s\n%s", check.Describe(), check.remedy())
	case CredentialsExpiring:
		return check.Describe(), nil
	default:
		return "", nil
	}
}

func isTruthyEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
