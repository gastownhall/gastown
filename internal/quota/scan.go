package quota

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/session"
	"github.com/jonbaldie/gastown/internal/util"
)

// ScanResult holds the result of scanning a single tmux session.
type ScanResult struct {
	Session       string `json:"session"`                  // tmux session name
	AccountHandle string `json:"account_handle,omitempty"` // resolved account handle
	ConfigDir     string `json:"config_dir,omitempty"`     // CLAUDE_CONFIG_DIR (even if account unknown)
	RateLimited   bool   `json:"rate_limited"`             // whether hard rate-limit was detected
	NearLimit     bool   `json:"near_limit"`               // whether approaching-limit signal was detected
	MatchedLine   string `json:"matched_line,omitempty"`   // the line that matched (hard or warning)
	ResetsAt      string `json:"resets_at,omitempty"`      // parsed reset time if available
}

// TmuxClient is the interface for tmux operations needed by the scanner.
// This allows testing without a real tmux server.
type TmuxClient interface {
	ListSessions() ([]string, error)
	CapturePane(session string, lines int) (string, error)
	GetEnvironment(session, key string) (string, error)
}

// Scanner detects rate-limited and near-limit sessions by examining tmux pane content.
type Scanner struct {
	tmux            TmuxClient
	patterns        []*regexp.Regexp // hard rate-limit patterns
	warningPatterns []*regexp.Regexp // near-limit warning patterns
	accounts        *config.AccountsConfig
}

// NewScanner creates a scanner with the given tmux client and rate-limit patterns.
// If patterns is nil, DefaultRateLimitPatterns are used.
func NewScanner(tmux TmuxClient, patterns []string, accounts *config.AccountsConfig) (*Scanner, error) {
	if len(patterns) == 0 {
		patterns = constants.DefaultRateLimitPatterns
	}

	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile("(?i)" + p)
		if err != nil {
			return nil, fmt.Errorf("compiling pattern %q: %w", p, err)
		}
		compiled = append(compiled, re)
	}

	return &Scanner{
		tmux:     tmux,
		patterns: compiled,
		accounts: accounts,
	}, nil
}

// WithWarningPatterns enables near-limit detection via pane content patterns.
// If patterns is nil, DefaultNearLimitPatterns are used.
func (s *Scanner) WithWarningPatterns(patterns []string) error {
	if patterns == nil {
		patterns = constants.DefaultNearLimitPatterns
	}

	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile("(?i)" + p)
		if err != nil {
			return fmt.Errorf("compiling warning pattern %q: %w", p, err)
		}
		compiled = append(compiled, re)
	}
	s.warningPatterns = compiled
	return nil
}

// scanLines is the number of pane lines to capture for rate-limit detection.
// We capture a generous window but only check the bottom checkLines for
// rate-limit patterns — if the limit was resolved, subsequent output pushes
// the message above the check window, avoiding false positives.
const scanLines = 30

// checkLines is the number of bottom lines to actually check for rate-limit
// patterns. When Claude Code hits a rate limit, the prompt sits at the bottom.
// Once resolved (e.g., /login, waiting), new output pushes it up.
// 20 balances detection reliability (10 was too small — messages scrolled
// out when agents kept working) against false-positive risk from stale
// rate-limit messages lingering higher in the scroll buffer.
const checkLines = 20

// ScanAll scans all Gas Town tmux sessions for rate-limit and near-limit indicators.
// Returns results for all Gas Town sessions.
func (s *Scanner) ScanAll() ([]ScanResult, error) {
	sessions, err := s.tmux.ListSessions()
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}

	var results []ScanResult
	for _, sess := range sessions {
		if !isGasTownSession(sess) {
			continue
		}

		result := s.scanSession(sess)
		results = append(results, result)
	}

	return results, nil
}

// scanSession examines a single tmux session for rate-limit and near-limit indicators.
func (s *Scanner) scanSession(session string) ScanResult {
	result := ScanResult{Session: session}

	// Always capture CLAUDE_CONFIG_DIR for rotation planning, even if
	// the account handle can't be resolved (unknown account sessions).
	// Falls back to ~/.claude (Claude Code's default) when the env var isn't set.
	if configDir, err := s.tmux.GetEnvironment(session, "CLAUDE_CONFIG_DIR"); err == nil {
		result.ConfigDir = strings.TrimSpace(configDir)
	} else {
		home, _ := os.UserHomeDir()
		if home != "" {
			result.ConfigDir = home + "/.claude"
		}
	}

	// Derive account from CLAUDE_CONFIG_DIR
	result.AccountHandle = s.resolveAccountHandle(session)

	// Capture pane content
	content, err := s.tmux.CapturePane(session, scanLines)
	if err != nil {
		// Can't capture — session might be dead. Not rate-limited.
		return result
	}

	// Only check the bottom checkLines for rate-limit patterns.
	// If the rate limit was resolved (e.g., /login), subsequent output
	// pushes the message above this window, avoiding false positives.
	allLines := strings.Split(content, "\n")
	start := len(allLines) - checkLines
	if start < 0 {
		start = 0
	}
	bottomLines := allLines[start:]

	// Check hard rate-limit patterns first
	for _, line := range bottomLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, re := range s.patterns {
			if re.MatchString(line) {
				result.RateLimited = true
				result.MatchedLine = line
				result.ResetsAt = parseResetTime(line)
				return result
			}
		}
	}

	// No hard limit detected — check near-limit warning patterns
	if len(s.warningPatterns) > 0 {
		for _, line := range bottomLines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			for _, re := range s.warningPatterns {
				if re.MatchString(line) {
					result.NearLimit = true
					result.MatchedLine = line
					return result
				}
			}
		}
	}

	return result
}

// resolveAccountHandle maps a session's active account back to a handle.
// Checks GT_QUOTA_ACCOUNT first (set by keychain swap rotation), then
// falls back to matching CLAUDE_CONFIG_DIR against registered accounts.
func (s *Scanner) resolveAccountHandle(session string) string {
	if s.accounts == nil {
		return ""
	}

	// After keychain swap, the config dir still maps to the old account.
	// GT_QUOTA_ACCOUNT records which account's token is actually active.
	if override, err := s.tmux.GetEnvironment(session, "GT_QUOTA_ACCOUNT"); err == nil {
		override = strings.TrimSpace(override)
		if override != "" {
			if _, ok := s.accounts.Accounts[override]; ok {
				return override
			}
		}
	}

	configDir, err := s.tmux.GetEnvironment(session, "CLAUDE_CONFIG_DIR")
	if err != nil {
		return "" // No CLAUDE_CONFIG_DIR = using default config
	}

	configDir = strings.TrimSpace(configDir)
	for handle, acct := range s.accounts.Accounts {
		// Compare normalized paths (accounts may use ~/... while tmux has expanded)
		if acct.ConfigDir == configDir || util.ExpandHome(acct.ConfigDir) == configDir {
			return handle
		}
	}

	return "" // CLAUDE_CONFIG_DIR doesn't match any registered account
}

// isGasTownSession returns true if the session name belongs to Gas Town.
// Uses the prefix registry to check for known rig prefixes (gt-, bd-, etc.)
// and the hq- prefix for town-level services.
func isGasTownSession(sess string) bool {
	return session.IsKnownSession(sess)
}

// parseResetTime attempts to extract the reset time from a rate-limit message.
// Examples:
//
//	"You've hit your limit · resets 7pm (America/Los_Angeles)" → "7pm (America/Los_Angeles)"
//	"resets 3:00 AM PST" → "3:00 AM PST"
var resetTimePattern = regexp.MustCompile(`(?i)\bresets\s+(.+)`)

func parseResetTime(line string) string {
	m := resetTimePattern.FindStringSubmatch(line)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}
