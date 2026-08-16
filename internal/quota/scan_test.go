package quota

import (
	"fmt"
	"testing"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/session"
	"github.com/jonbaldie/gastown/internal/util"
)

// setupTestRegistry populates the default session prefix registry for tests.
// Returns a cleanup function to restore the original registry.
func setupTestRegistry(t *testing.T) {
	t.Helper()
	r := session.NewPrefixRegistry()
	r.Register("gt", "gastown")
	r.Register("bd", "beads")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(r)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

// mockTmux implements TmuxClient for testing.
type mockTmux struct {
	sessions    []string
	sessionsErr error                        // injected ListSessions error
	paneContent map[string]string            // session -> captured content
	envVars     map[string]map[string]string // session -> key -> value
}

func (m *mockTmux) ListSessions() ([]string, error) {
	if m.sessionsErr != nil {
		return nil, m.sessionsErr
	}
	return m.sessions, nil
}

func (m *mockTmux) CapturePane(session string, lines int) (string, error) {
	content, ok := m.paneContent[session]
	if !ok {
		return "", fmt.Errorf("session %s not found", session)
	}
	return content, nil
}

func (m *mockTmux) GetEnvironment(session, key string) (string, error) {
	envs, ok := m.envVars[session]
	if !ok {
		return "", fmt.Errorf("no environment for session %s", session)
	}
	val, ok := envs[key]
	if !ok {
		return "", fmt.Errorf("env %s not set in session %s", key, session)
	}
	return val, nil
}

func TestScanAll_NoSessions(t *testing.T) {
	setupTestRegistry(t)

	tmux := &mockTmux{}
	scanner, err := NewScanner(tmux, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	results, err := scanner.ScanAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestScanAll_DetectsRateLimited(t *testing.T) {
	setupTestRegistry(t)

	tmux := &mockTmux{
		sessions: []string{"hq-mayor", "gt-crew-bear", "gt-witness", "some-other"},
		paneContent: map[string]string{
			"hq-mayor": `❯ /rate-limit-options
  ⎿  You've hit your limit · resets 7pm (America/Los_Angeles)

❯ 📬 You have new mail from laser/witness.`,
			"gt-crew-bear": `⏺ Working on implementing quota scan...
  Bash: go test ./internal/quota/...
  All tests passed.`,
			"gt-witness": `You've hit your limit · resets 9pm (America/Los_Angeles)`,
			"some-other": `This is not a gas town session content`,
		},
		envVars: map[string]map[string]string{
			"hq-mayor":     {"CLAUDE_CONFIG_DIR": "/home/user/.claude-accounts/work"},
			"gt-crew-bear": {"CLAUDE_CONFIG_DIR": "/home/user/.claude-accounts/personal"},
			"gt-witness":   {"CLAUDE_CONFIG_DIR": "/home/user/.claude-accounts/work"},
		},
	}

	accounts := &config.AccountsConfig{
		Accounts: map[string]config.Account{
			"work":     {ConfigDir: "/home/user/.claude-accounts/work"},
			"personal": {ConfigDir: "/home/user/.claude-accounts/personal"},
		},
	}

	scanner, err := NewScanner(tmux, nil, accounts)
	if err != nil {
		t.Fatal(err)
	}

	results, err := scanner.ScanAll()
	if err != nil {
		t.Fatal(err)
	}

	// Should scan: hq-mayor, gt-crew-bear, gt-witness (known prefixes)
	// "some-other" is skipped — not a registered prefix
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Find specific results
	resultMap := make(map[string]ScanResult)
	for _, r := range results {
		resultMap[r.Session] = r
	}

	// hq-mayor should be rate-limited
	mayor := resultMap["hq-mayor"]
	if !mayor.RateLimited {
		t.Error("expected hq-mayor to be rate-limited")
	}
	if mayor.AccountHandle != "work" {
		t.Errorf("expected hq-mayor account 'work', got %q", mayor.AccountHandle)
	}
	if mayor.ResetsAt != "7pm (America/Los_Angeles)" {
		t.Errorf("expected resets at '7pm (America/Los_Angeles)', got %q", mayor.ResetsAt)
	}

	// gt-crew-bear should NOT be rate-limited
	crew := resultMap["gt-crew-bear"]
	if crew.RateLimited {
		t.Error("expected gt-crew-bear to NOT be rate-limited")
	}
	if crew.AccountHandle != "personal" {
		t.Errorf("expected gt-crew-bear account 'personal', got %q", crew.AccountHandle)
	}

	// gt-witness should be rate-limited
	witness := resultMap["gt-witness"]
	if !witness.RateLimited {
		t.Error("expected gt-witness to be rate-limited")
	}
	if witness.ResetsAt != "9pm (America/Los_Angeles)" {
		t.Errorf("expected resets at '9pm (America/Los_Angeles)', got %q", witness.ResetsAt)
	}
}

func TestScanAll_SkipsNonGasTownSessions(t *testing.T) {
	setupTestRegistry(t)

	tmux := &mockTmux{
		sessions: []string{"myapp", "devserver"},
		paneContent: map[string]string{
			"myapp":     "You've hit your limit",
			"devserver": "running on port 3000",
		},
	}

	scanner, err := NewScanner(tmux, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	results, err := scanner.ScanAll()
	if err != nil {
		t.Fatal(err)
	}
	// "myapp" and "devserver" have no dashes and no hq- prefix → skipped
	if len(results) != 0 {
		t.Errorf("expected 0 results for non-GT sessions, got %d", len(results))
	}
}

// TestScanAll_DetectsRateLimitTUIPrompt verifies detection when the original
// "You've hit your limit" message has scrolled off, leaving only the
// interactive /rate-limit-options TUI prompt visible in the capture window.
func TestScanAll_DetectsRateLimitTUIPrompt(t *testing.T) {
	setupTestRegistry(t)

	tuiPromptContent := `⏺ Working on implementing quota scan...
  Bash: go test ./internal/quota/...
  All tests passed.

❯ /rate-limit-options

What do you want to do?

> 1. Stop and wait for limit to reset
  2. Add funds to continue with extra usage

Enter to confirm · Esc to cancel`

	tmux := &mockTmux{
		sessions: []string{"gt-crew-bear"},
		paneContent: map[string]string{
			"gt-crew-bear": tuiPromptContent,
		},
		envVars: map[string]map[string]string{
			"gt-crew-bear": {"CLAUDE_CONFIG_DIR": "/home/user/.claude-accounts/work"},
		},
	}

	accounts := &config.AccountsConfig{
		Accounts: map[string]config.Account{
			"work": {ConfigDir: "/home/user/.claude-accounts/work"},
		},
	}

	scanner, err := NewScanner(tmux, nil, accounts)
	if err != nil {
		t.Fatal(err)
	}

	results, err := scanner.ScanAll()
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].RateLimited {
		t.Error("expected rate-limited when TUI prompt is visible (original message scrolled off)")
	}
	if results[0].AccountHandle != "work" {
		t.Errorf("expected account 'work', got %q", results[0].AccountHandle)
	}
}

// TestScanAll_DetectsAPIError429 verifies detection of mid-stream API 429
// errors that appear as "API Error: Rate limit reached" — distinct from the
// interactive TUI prompt which only shows on prompt-submission rate limits.
func TestScanAll_DetectsAPIError429(t *testing.T) {
	setupTestRegistry(t)

	apiErrorContent := `  ◆ Update(src/fallback/redis_tracker.py)
  └ Added 4 lines, removed 1 line
  └ API Error: Rate limit reached

  ✻ Cogitated for 4m 51s

❯ `

	tmux := &mockTmux{
		sessions: []string{"gt-crew-bear"},
		paneContent: map[string]string{
			"gt-crew-bear": apiErrorContent,
		},
		envVars: map[string]map[string]string{
			"gt-crew-bear": {"CLAUDE_CONFIG_DIR": "/home/user/.claude-accounts/work"},
		},
	}

	accounts := &config.AccountsConfig{
		Accounts: map[string]config.Account{
			"work": {ConfigDir: "/home/user/.claude-accounts/work"},
		},
	}

	scanner, err := NewScanner(tmux, nil, accounts)
	if err != nil {
		t.Fatal(err)
	}

	results, err := scanner.ScanAll()
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].RateLimited {
		t.Error("expected rate-limited when API Error: Rate limit reached is visible")
	}
}

func TestScanAll_CustomPatterns(t *testing.T) {
	setupTestRegistry(t)

	tmux := &mockTmux{
		sessions: []string{"gt-crew-test"},
		paneContent: map[string]string{
			"gt-crew-test": "CUSTOM_RATE_LIMIT_DETECTED",
		},
	}

	scanner, err := NewScanner(tmux, []string{"CUSTOM_RATE_LIMIT"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	results, err := scanner.ScanAll()
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].RateLimited {
		t.Error("expected rate-limited with custom pattern")
	}
}

func TestScanAll_CaptureError(t *testing.T) {
	setupTestRegistry(t)

	tmux := &mockTmux{
		sessions:    []string{"gt-crew-dead"},
		paneContent: map[string]string{}, // no content = error
	}

	scanner, err := NewScanner(tmux, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	results, err := scanner.ScanAll()
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].RateLimited {
		t.Error("expected NOT rate-limited when capture fails")
	}
}

func TestParseResetTime(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "You've hit your limit · resets 7pm (America/Los_Angeles)",
			expected: "7pm (America/Los_Angeles)",
		},
		{
			input:    "resets 3:00 AM PST",
			expected: "3:00 AM PST",
		},
		{
			input:    "rate limit reached, reset at midnight",
			expected: "",
		},
		{
			input:    "no reset info here",
			expected: "",
		},
		{
			input:    "Resets 11:30pm (America/New_York)",
			expected: "11:30pm (America/New_York)",
		},
	}

	for _, tt := range tests {
		got := parseResetTime(tt.input)
		if got != tt.expected {
			t.Errorf("parseResetTime(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestIsGasTownSession(t *testing.T) {
	setupTestRegistry(t)

	tests := []struct {
		session  string
		expected bool
	}{
		{"hq-mayor", true},
		{"hq-deacon", true},
		{"hq-overseer", true},
		{"gt-crew-bear", true},
		{"gt-witness", true},
		{"bd-refinery", true},
		{"my-app", false},     // has dash but not a known prefix
		{"dev-server", false}, // has dash but not a known prefix
		{"myapp", false},      // no dash, no known prefix
		{"devserver", false},  // no dash, no known prefix
	}

	for _, tt := range tests {
		got := isGasTownSession(tt.session)
		if got != tt.expected {
			t.Errorf("isGasTownSession(%q) = %v, want %v", tt.session, got, tt.expected)
		}
	}
}

func TestNewScanner_InvalidPattern(t *testing.T) {
	_, err := NewScanner(&mockTmux{}, []string{"[invalid"}, nil)
	if err == nil {
		t.Error("expected error for invalid regex pattern")
	}
}

func TestResolveAccountHandle_TildeExpansion(t *testing.T) {
	setupTestRegistry(t)

	tmux := &mockTmux{
		sessions: []string{"gt-crew-test"},
		paneContent: map[string]string{
			"gt-crew-test": "working...",
		},
		envVars: map[string]map[string]string{
			"gt-crew-test": {"CLAUDE_CONFIG_DIR": util.ExpandHome("~/.claude-accounts/work")},
		},
	}

	accounts := &config.AccountsConfig{
		Accounts: map[string]config.Account{
			"work": {ConfigDir: "~/.claude-accounts/work"},
		},
	}

	scanner, err := NewScanner(tmux, nil, accounts)
	if err != nil {
		t.Fatal(err)
	}

	results, err := scanner.ScanAll()
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].AccountHandle != "work" {
		t.Errorf("expected account 'work', got %q", results[0].AccountHandle)
	}
}

func TestScanAll_ListSessionsError(t *testing.T) {
	setupTestRegistry(t)

	tmux := &mockTmux{
		sessionsErr: fmt.Errorf("tmux server not running"),
	}

	scanner, err := NewScanner(tmux, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = scanner.ScanAll()
	if err == nil {
		t.Error("expected error when ListSessions fails")
	}
}

// --- Near-limit detection tests ---

func TestScanAll_DetectsNearLimit_WarningPatterns(t *testing.T) {
	setupTestRegistry(t)

	tmux := &mockTmux{
		sessions: []string{"gt-crew-bear", "gt-crew-wolf"},
		paneContent: map[string]string{
			"gt-crew-bear": "Working normally...\n85% of your daily usage consumed",
			"gt-crew-wolf": "Working normally...",
		},
		envVars: map[string]map[string]string{
			"gt-crew-bear": {"CLAUDE_CONFIG_DIR": "/home/user/.claude-accounts/work"},
			"gt-crew-wolf": {"CLAUDE_CONFIG_DIR": "/home/user/.claude-accounts/personal"},
		},
	}

	accounts := &config.AccountsConfig{
		Accounts: map[string]config.Account{
			"work":     {ConfigDir: "/home/user/.claude-accounts/work"},
			"personal": {ConfigDir: "/home/user/.claude-accounts/personal"},
		},
	}

	scanner, err := NewScanner(tmux, nil, accounts)
	if err != nil {
		t.Fatal(err)
	}
	if err := scanner.WithWarningPatterns(nil); err != nil {
		t.Fatal(err)
	}

	results, err := scanner.ScanAll()
	if err != nil {
		t.Fatal(err)
	}

	resultMap := make(map[string]ScanResult)
	for _, r := range results {
		resultMap[r.Session] = r
	}

	// bear should be near-limit (not hard-limited)
	bear := resultMap["gt-crew-bear"]
	if bear.RateLimited {
		t.Error("expected gt-crew-bear to NOT be hard rate-limited")
	}
	if !bear.NearLimit {
		t.Error("expected gt-crew-bear to be near-limit")
	}
	if bear.MatchedLine == "" {
		t.Error("expected matched line for near-limit detection")
	}

	// wolf should be fine
	wolf := resultMap["gt-crew-wolf"]
	if wolf.RateLimited || wolf.NearLimit {
		t.Error("expected gt-crew-wolf to have no limit signals")
	}
}

func TestScanAll_HardLimitTakesPrecedence(t *testing.T) {
	setupTestRegistry(t)

	// Session has both hard-limit and near-limit patterns.
	// Hard limit should take precedence (NearLimit stays false).
	tmux := &mockTmux{
		sessions: []string{"gt-crew-bear"},
		paneContent: map[string]string{
			"gt-crew-bear": "85% of your daily usage consumed\nYou've hit your limit · resets 7pm (America/Los_Angeles)",
		},
	}

	scanner, err := NewScanner(tmux, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := scanner.WithWarningPatterns(nil); err != nil {
		t.Fatal(err)
	}

	results, err := scanner.ScanAll()
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].RateLimited {
		t.Error("expected hard rate-limited")
	}
	if results[0].NearLimit {
		t.Error("NearLimit should be false when hard rate-limited")
	}
}

func TestScanAll_NearLimitVariousPatterns(t *testing.T) {
	setupTestRegistry(t)

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"usage percentage", "90% of your usage limit", true},
		{"approaching limit", "approaching your rate limit", true},
		{"nearing limit", "nearing your limit", true},
		{"close to limit", "close to your rate limit", true},
		{"almost reached", "almost reached your rate limit", true},
		{"messages remaining", "5 messages remaining", true},
		{"requests left", "10 requests left", true},
		{"usage at percentage", "usage is at 95%", true},
		{"no warning", "Working on implementing feature X...", false},
		{"single digit percentage", "5% of usage", false}, // only 2+ digit percentages
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmux := &mockTmux{
				sessions: []string{"gt-crew-test"},
				paneContent: map[string]string{
					"gt-crew-test": tt.content,
				},
			}

			scanner, err := NewScanner(tmux, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := scanner.WithWarningPatterns(nil); err != nil {
				t.Fatal(err)
			}

			results, err := scanner.ScanAll()
			if err != nil {
				t.Fatal(err)
			}

			if len(results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(results))
			}
			if results[0].NearLimit != tt.want {
				t.Errorf("NearLimit = %v, want %v for content %q", results[0].NearLimit, tt.want, tt.content)
			}
		})
	}
}

func TestWithWarningPatterns_InvalidPattern(t *testing.T) {
	scanner, err := NewScanner(&mockTmux{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	err = scanner.WithWarningPatterns([]string{"[invalid"})
	if err == nil {
		t.Error("expected error for invalid warning pattern")
	}
}
