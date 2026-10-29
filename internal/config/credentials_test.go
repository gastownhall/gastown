package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeCredentials writes a .credentials.json into dir and returns dir.
func writeCredentials(t *testing.T, dir string, oauth map[string]any) string {
	t.Helper()
	payload := map[string]any{}
	if oauth != nil {
		payload["claudeAiOauth"] = oauth
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal credentials: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, CredentialsFileName), data, 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	return dir
}

func claudeRuntime() *RuntimeConfig {
	return &RuntimeConfig{
		Session: &RuntimeSessionConfig{ConfigDirEnv: ClaudeConfigDirEnv},
	}
}

func TestCheckClaudeCredentialsStatuses(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	ms := func(d time.Duration) int64 { return now.Add(d).UnixMilli() }

	tests := []struct {
		name  string
		oauth map[string]any
		want  CredentialStatus
	}{
		{
			name:  "valid well clear of the window",
			oauth: map[string]any{"accessToken": "a", "refreshToken": "r", "expiresAt": ms(time.Hour)},
			want:  CredentialsValid,
		},
		{
			name:  "expiring inside the window",
			oauth: map[string]any{"accessToken": "a", "refreshToken": "r", "expiresAt": ms(CredentialExpiryWindow - time.Minute)},
			want:  CredentialsExpiring,
		},
		{
			name:  "expired with usable refresh token",
			oauth: map[string]any{"accessToken": "a", "refreshToken": "r", "expiresAt": ms(-time.Hour)},
			want:  CredentialsExpired,
		},
		{
			name:  "expired with refresh token that also expired",
			oauth: map[string]any{"accessToken": "a", "refreshToken": "r", "expiresAt": ms(-time.Hour), "refreshTokenExpiresAt": ms(-time.Minute)},
			want:  CredentialsUnusable,
		},
		{
			name:  "expired with no refresh token at all",
			oauth: map[string]any{"accessToken": "a", "expiresAt": ms(-time.Hour)},
			want:  CredentialsUnusable,
		},
		{
			name:  "expired with refresh token still valid in future",
			oauth: map[string]any{"accessToken": "a", "refreshToken": "r", "expiresAt": ms(-time.Hour), "refreshTokenExpiresAt": ms(24 * time.Hour)},
			want:  CredentialsExpired,
		},
		{
			name:  "no claudeAiOauth block",
			oauth: nil,
			want:  CredentialsUnknown,
		},
		{
			name:  "oauth block without an expiry",
			oauth: map[string]any{"accessToken": "a"},
			want:  CredentialsUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeCredentials(t, t.TempDir(), tt.oauth)
			got := checkClaudeCredentialsAt(dir, now)
			if got.Status != tt.want {
				t.Errorf("status = %v, want %v (%s)", got.Status, tt.want, got.Describe())
			}
			if got.ConfigDir != dir {
				t.Errorf("ConfigDir = %q, want %q", got.ConfigDir, dir)
			}
		})
	}
}

func TestCheckClaudeCredentialsUnreadableStoreIsUnknown(t *testing.T) {
	// Missing store: normal on macOS (Keychain) and for API-key auth.
	missing := checkClaudeCredentialsAt(t.TempDir(), time.Now())
	if missing.Status != CredentialsUnknown {
		t.Errorf("missing store: status = %v, want unknown", missing.Status)
	}
	if missing.Err == nil {
		t.Error("missing store: want Err explaining the unknown verdict")
	}
	if !missing.OK() {
		t.Error("missing store: must not block a spawn")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, CredentialsFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	malformed := checkClaudeCredentialsAt(dir, time.Now())
	if malformed.Status != CredentialsUnknown {
		t.Errorf("malformed store: status = %v, want unknown", malformed.Status)
	}
	if !malformed.OK() {
		t.Error("malformed store: must not block a spawn")
	}
}

func TestCredentialCheckOK(t *testing.T) {
	tests := []struct {
		status CredentialStatus
		want   bool
	}{
		{CredentialsUnknown, true},
		{CredentialsValid, true},
		{CredentialsExpiring, true},
		{CredentialsExpired, false},
		{CredentialsUnusable, false},
	}
	for _, tt := range tests {
		if got := (CredentialCheck{Status: tt.status}).OK(); got != tt.want {
			t.Errorf("%v.OK() = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestResolveClaudeConfigDir(t *testing.T) {
	t.Setenv(ClaudeConfigDirEnv, "/env/claude")

	if got := ResolveClaudeConfigDir("/explicit/claude"); got != "/explicit/claude" {
		t.Errorf("explicit dir = %q, want /explicit/claude", got)
	}
	if got := ResolveClaudeConfigDir(""); got != "/env/claude" {
		t.Errorf("env dir = %q, want /env/claude", got)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}

	// Accounts are conventionally configured as ~/.claude-accounts/<name>.
	t.Setenv(ClaudeConfigDirEnv, "~/.claude-accounts/work")
	if want := filepath.Join(home, ".claude-accounts", "work"); ResolveClaudeConfigDir("") != want {
		t.Errorf("tilde env dir = %q, want %q", ResolveClaudeConfigDir(""), want)
	}
	if want := filepath.Join(home, ".claude-accounts", "alt"); ResolveClaudeConfigDir("~/.claude-accounts/alt") != want {
		t.Errorf("tilde explicit dir = %q, want %q", ResolveClaudeConfigDir("~/.claude-accounts/alt"), want)
	}

	t.Setenv(ClaudeConfigDirEnv, "")
	if want := filepath.Join(home, ".claude"); ResolveClaudeConfigDir("") != want {
		t.Errorf("default dir = %q, want %q", ResolveClaudeConfigDir(""), want)
	}
}

func TestPreflightAgentAuthBlocksExpiredToken(t *testing.T) {
	t.Setenv(SkipAuthPreflightEnv, "")
	dir := writeCredentials(t, t.TempDir(), map[string]any{
		"accessToken":  "a",
		"refreshToken": "r",
		"expiresAt":    time.Now().Add(-time.Hour).UnixMilli(),
	})

	warning, err := PreflightAgentAuth(claudeRuntime(), dir)
	if err == nil {
		t.Fatal("expired token must block the spawn")
	}
	if warning != "" {
		t.Errorf("warning = %q, want empty on a blocking verdict", warning)
	}
	// The operator needs to know how to get unstuck.
	if !strings.Contains(err.Error(), SkipAuthPreflightEnv) {
		t.Errorf("error missing the escape hatch: %v", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error missing the config dir: %v", err)
	}
}

func TestPreflightAgentAuthWarnsOnExpiringToken(t *testing.T) {
	t.Setenv(SkipAuthPreflightEnv, "")
	dir := writeCredentials(t, t.TempDir(), map[string]any{
		"accessToken":  "a",
		"refreshToken": "r",
		"expiresAt":    time.Now().Add(CredentialExpiryWindow / 2).UnixMilli(),
	})

	warning, err := PreflightAgentAuth(claudeRuntime(), dir)
	if err != nil {
		t.Fatalf("expiring token must not block the spawn: %v", err)
	}
	if warning == "" {
		t.Error("expiring token should warn the operator")
	}
}

func TestPreflightAgentAuthAllowsValidToken(t *testing.T) {
	t.Setenv(SkipAuthPreflightEnv, "")
	dir := writeCredentials(t, t.TempDir(), map[string]any{
		"accessToken":  "a",
		"refreshToken": "r",
		"expiresAt":    time.Now().Add(time.Hour).UnixMilli(),
	})

	warning, err := PreflightAgentAuth(claudeRuntime(), dir)
	if err != nil || warning != "" {
		t.Errorf("valid token: got (%q, %v), want (\"\", nil)", warning, err)
	}
}

func TestPreflightAgentAuthSkipsNonOAuthRuntimes(t *testing.T) {
	t.Setenv(SkipAuthPreflightEnv, "")
	// A dead store that would otherwise block.
	dir := writeCredentials(t, t.TempDir(), map[string]any{
		"accessToken": "a",
		"expiresAt":   time.Now().Add(-time.Hour).UnixMilli(),
	})

	tests := []struct {
		name string
		rc   *RuntimeConfig
	}{
		{"nil runtime config", nil},
		{"no session config", &RuntimeConfig{}},
		{"non-claude config dir env", &RuntimeConfig{Session: &RuntimeSessionConfig{ConfigDirEnv: "GEMINI_CONFIG_DIR"}}},
		{"empty config dir env", &RuntimeConfig{Session: &RuntimeSessionConfig{ConfigDirEnv: ""}}},
		{"pinned API key", &RuntimeConfig{
			Session: &RuntimeSessionConfig{ConfigDirEnv: ClaudeConfigDirEnv},
			Env:     map[string]string{"ANTHROPIC_API_KEY": "sk-test"},
		}},
		{"pinned auth token", &RuntimeConfig{
			Session: &RuntimeSessionConfig{ConfigDirEnv: ClaudeConfigDirEnv},
			Env:     map[string]string{"ANTHROPIC_AUTH_TOKEN": "tok"},
		}},
		{"redirected base URL", &RuntimeConfig{
			Session: &RuntimeSessionConfig{ConfigDirEnv: ClaudeConfigDirEnv},
			Env:     map[string]string{"ANTHROPIC_BASE_URL": "https://api.groq.com/anthropic"},
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warning, err := PreflightAgentAuth(tt.rc, dir)
			if err != nil || warning != "" {
				t.Errorf("got (%q, %v), want (\"\", nil)", warning, err)
			}
		})
	}
}

func TestPreflightAgentAuthEmptyEnvValueStillGatesOAuth(t *testing.T) {
	t.Setenv(SkipAuthPreflightEnv, "")
	dir := writeCredentials(t, t.TempDir(), map[string]any{
		"accessToken": "a",
		"expiresAt":   time.Now().Add(-time.Hour).UnixMilli(),
	})
	rc := claudeRuntime()
	rc.Env = map[string]string{"ANTHROPIC_API_KEY": ""}

	if _, err := PreflightAgentAuth(rc, dir); err == nil {
		t.Error("an empty ANTHROPIC_API_KEY is not API-key auth; the gate must still apply")
	}
}

func TestPreflightAgentAuthEscapeHatch(t *testing.T) {
	dir := writeCredentials(t, t.TempDir(), map[string]any{
		"accessToken": "a",
		"expiresAt":   time.Now().Add(-time.Hour).UnixMilli(),
	})

	for _, v := range []string{"1", "true", "TRUE", "yes", "on", " 1 "} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(SkipAuthPreflightEnv, v)
			warning, err := PreflightAgentAuth(claudeRuntime(), dir)
			if err != nil || warning != "" {
				t.Errorf("%s=%q: got (%q, %v), want (\"\", nil)", SkipAuthPreflightEnv, v, warning, err)
			}
		})
	}

	for _, v := range []string{"", "0", "false", "no", "maybe"} {
		t.Run("not-truthy-"+v, func(t *testing.T) {
			t.Setenv(SkipAuthPreflightEnv, v)
			if _, err := PreflightAgentAuth(claudeRuntime(), dir); err == nil {
				t.Errorf("%s=%q should not disable the gate", SkipAuthPreflightEnv, v)
			}
		})
	}
}

func TestPreflightAgentAuthUnreadableStoreDoesNotBlock(t *testing.T) {
	t.Setenv(SkipAuthPreflightEnv, "")
	// macOS Keychain and API-key auth leave no OAuth store on disk.
	warning, err := PreflightAgentAuth(claudeRuntime(), t.TempDir())
	if err != nil || warning != "" {
		t.Errorf("got (%q, %v), want (\"\", nil)", warning, err)
	}
}

func TestCredentialStatusString(t *testing.T) {
	want := map[CredentialStatus]string{
		CredentialsUnknown:  "unknown",
		CredentialsValid:    "valid",
		CredentialsExpiring: "expiring",
		CredentialsExpired:  "expired",
		CredentialsUnusable: "unusable",
	}
	for status, name := range want {
		if got := status.String(); got != name {
			t.Errorf("String() = %q, want %q", got, name)
		}
	}
}
