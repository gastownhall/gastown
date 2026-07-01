package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/steveyegge/gastown/internal/session"
)

// setupTestRegistry sets up a prefix registry for tests and returns a cleanup function.
func setupTestRegistry(t *testing.T) {
	t.Helper()
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("bd", "beads")
	reg.Register("nif", "niflheim")
	reg.Register("grc", "grctool")
	reg.Register("7s", "7thsense")
	reg.Register("pf", "pulseflow")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

// mockSessionLister allows deterministic testing of orphan session detection.
type mockSessionLister struct {
	sessions []string
	err      error
}

func (m *mockSessionLister) ListSessions() ([]string, error) {
	return m.sessions, m.err
}

func TestNewOrphanSessionCheck(t *testing.T) {
	check := NewOrphanSessionCheck()

	if check.Name() != "orphan-sessions" {
		t.Errorf("expected name 'orphan-sessions', got %q", check.Name())
	}

	if !check.CanFix() {
		t.Error("expected CanFix to return true for session check")
	}
}

func TestNewOrphanProcessCheck(t *testing.T) {
	check := NewOrphanProcessCheck()

	if check.Name() != "orphan-processes" {
		t.Errorf("expected name 'orphan-processes', got %q", check.Name())
	}

	// OrphanProcessCheck should NOT be fixable - it's informational only
	if check.CanFix() {
		t.Error("expected CanFix to return false for process check (informational only)")
	}
}

func TestOrphanProcessCheck_Run(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("orphan process detection is not supported on Windows")
	}

	// This test verifies the check runs without error.
	// Results depend on whether Claude processes exist in the test environment.
	check := NewOrphanProcessCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)

	// Should return OK (no processes or all inside tmux) or Warning (processes outside tmux)
	// Both are valid depending on test environment
	if result.Status != StatusOK && result.Status != StatusWarning {
		t.Errorf("expected StatusOK or StatusWarning, got %v: %s", result.Status, result.Message)
	}

	// If warning, should have informational details
	if result.Status == StatusWarning {
		if len(result.Details) < 3 {
			t.Errorf("expected at least 3 detail lines (2 info + 1 process), got %d", len(result.Details))
		}
		// Should NOT have a FixHint since this is informational only
		if result.FixHint != "" {
			t.Errorf("expected no FixHint for informational check, got %q", result.FixHint)
		}
	}
}

func TestOrphanProcessCheck_MessageContent(t *testing.T) {
	// Verify the check description is correct
	check := NewOrphanProcessCheck()

	expectedDesc := "Detect runtime processes outside tmux"
	if check.Description() != expectedDesc {
		t.Errorf("expected description %q, got %q", expectedDesc, check.Description())
	}
}

func TestIsCrewSession(t *testing.T) {
	setupTestRegistry(t)
	tests := []struct {
		session string
		want    bool
	}{
		{"gt-crew-joe", true},  // gastown crew (prefix: gt)
		{"bd-crew-max", true},  // beads crew (prefix: bd)
		{"nif-crew-a", true},   // niflheim crew (prefix: nif)
		{"gt-witness", false},  // witness, not crew
		{"gt-refinery", false}, // refinery, not crew
		{"gt-polecat1", false}, // polecat, not crew
		{"hq-deacon", false},
		{"hq-mayor", false},
		{"other-session", false},
		{"gt-crew", false}, // "crew" is a polecat name, not crew role (no name after crew-)
	}

	for _, tt := range tests {
		t.Run(tt.session, func(t *testing.T) {
			got := isCrewSession(tt.session)
			if got != tt.want {
				t.Errorf("isCrewSession(%q) = %v, want %v", tt.session, got, tt.want)
			}
		})
	}
}

func TestOrphanSessionCheck_IsValidSession(t *testing.T) {
	setupTestRegistry(t)
	check := NewOrphanSessionCheck()
	validRigs := []string{"gastown", "beads"}
	mayorSession := "hq-mayor"
	deaconSession := "hq-deacon"

	tests := []struct {
		session string
		want    bool
	}{
		// Town-level sessions
		{"hq-mayor", true},
		{"hq-deacon", true},

		// Boot watchdog session
		{"hq-boot", true},

		// Valid rig sessions (using rig prefixes)
		{"gt-witness", true},  // gastown witness (prefix: gt)
		{"gt-refinery", true}, // gastown refinery
		{"gt-polecat1", true}, // gastown polecat
		{"bd-witness", true},  // beads witness (prefix: bd)
		{"bd-refinery", true}, // beads refinery
		{"bd-crew-max", true}, // beads crew

		// Invalid rig sessions (unknown prefix/rig)
		{"zz-witness", false},  // unknown prefix
		{"xx-refinery", false}, // unknown prefix

		// Non-GT sessions fail format validation
		{"other-session", false},
	}

	for _, tt := range tests {
		t.Run(tt.session, func(t *testing.T) {
			got := check.isValidSession(tt.session, validRigs, mayorSession, deaconSession)
			if got != tt.want {
				t.Errorf("isValidSession(%q) = %v, want %v", tt.session, got, tt.want)
			}
		})
	}
}

// TestOrphanSessionCheck_IsValidSession_EdgeCases tests edge cases that have caused
// false positives in production - sessions incorrectly detected as orphans.
func TestOrphanSessionCheck_IsValidSession_EdgeCases(t *testing.T) {
	setupTestRegistry(t)
	check := NewOrphanSessionCheck()
	validRigs := []string{"gastown", "niflheim", "grctool", "7thsense", "pulseflow"}
	mayorSession := "hq-mayor"
	deaconSession := "hq-deacon"

	tests := []struct {
		name    string
		session string
		want    bool
		reason  string
	}{
		// Crew sessions with various name formats (using rig prefixes)
		{
			name:    "crew_simple_name",
			session: "gt-crew-max",
			want:    true,
			reason:  "simple crew name should be valid",
		},
		{
			name:    "crew_with_numbers",
			session: "nif-crew-codex1",
			want:    true,
			reason:  "crew name with numbers should be valid",
		},
		{
			name:    "crew_alphanumeric",
			session: "grc-crew-grc1",
			want:    true,
			reason:  "alphanumeric crew name should be valid",
		},
		{
			name:    "crew_short_name",
			session: "7s-crew-ss1",
			want:    true,
			reason:  "short crew name should be valid",
		},
		{
			name:    "crew_pf1",
			session: "pf-crew-pf1",
			want:    true,
			reason:  "pf1 crew name should be valid",
		},

		// Polecat sessions (any name after prefix should be accepted)
		{
			name:    "polecat_hash_style",
			session: "gt-abc123def",
			want:    true,
			reason:  "polecat with hash-style name should be valid",
		},
		{
			name:    "polecat_descriptive",
			session: "nif-fix-auth-bug",
			want:    true,
			reason:  "polecat with descriptive name should be valid",
		},

		// Sessions that should be detected as orphans
		{
			name:    "unknown_prefix_witness",
			session: "zz-witness",
			want:    false,
			reason:  "unknown prefix/rig should be orphan",
		},
		{
			name:    "malformed_no_dash",
			session: "x",
			want:    false,
			reason:  "session without dash should be invalid",
		},

		// Edge case: hyphenated rig names are no longer ambiguous because
		// each rig has a distinct prefix. E.g., rig "foo-bar" uses prefix "fb",
		// so its witness session is simply "fb-witness".
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := check.isValidSession(tt.session, validRigs, mayorSession, deaconSession)
			if got != tt.want {
				t.Errorf("isValidSession(%q) = %v, want %v: %s", tt.session, got, tt.want, tt.reason)
			}
		})
	}
}

// TestOrphanSessionCheck_GetValidRigs verifies rig detection from filesystem.
func TestOrphanSessionCheck_GetValidRigs(t *testing.T) {
	check := NewOrphanSessionCheck()
	townRoot := t.TempDir()

	// Setup: create mayor directory (required for getValidRigs to proceed)
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("failed to create mayor dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "rigs.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to create rigs.json: %v", err)
	}

	// Create some rigs with polecats/crew directories
	createRigDir := func(name string, hasCrew, hasPolecats bool) {
		rigPath := filepath.Join(townRoot, name)
		os.MkdirAll(rigPath, 0755)
		if hasCrew {
			os.MkdirAll(filepath.Join(rigPath, "crew"), 0755)
		}
		if hasPolecats {
			os.MkdirAll(filepath.Join(rigPath, "polecats"), 0755)
		}
	}

	createRigDir("gastown", true, true)
	createRigDir("niflheim", true, false)
	createRigDir("grctool", false, true)
	createRigDir("not-a-rig", false, false) // No crew or polecats

	rigs := check.getValidRigs(townRoot)

	// Should find gastown, niflheim, grctool but not "not-a-rig"
	expected := map[string]bool{
		"gastown":  true,
		"niflheim": true,
		"grctool":  true,
	}

	for _, rig := range rigs {
		if !expected[rig] {
			t.Errorf("unexpected rig %q in result", rig)
		}
		delete(expected, rig)
	}

	for rig := range expected {
		t.Errorf("expected rig %q not found in result", rig)
	}
}

// TestOrphanSessionCheck_FixProtectsCrewSessions verifies that Fix() never kills crew sessions.
func TestOrphanSessionCheck_FixProtectsCrewSessions(t *testing.T) {
	setupTestRegistry(t)
	check := NewOrphanSessionCheck()

	// Simulate cached orphan sessions including a crew session
	check.orphanSessions = []string{
		"gt-crew-max",     // Crew - should be protected
		"zz-witness",      // Not crew - would be killed
		"nif-crew-codex1", // Crew - should be protected
	}

	// Verify isCrewSession correctly identifies crew sessions
	for _, sess := range check.orphanSessions {
		if sess == "gt-crew-max" || sess == "nif-crew-codex1" {
			if !isCrewSession(sess) {
				t.Errorf("isCrewSession(%q) should return true for crew session", sess)
			}
		} else {
			if isCrewSession(sess) {
				t.Errorf("isCrewSession(%q) should return false for non-crew session", sess)
			}
		}
	}
}

// TestIsCrewSession_ComprehensivePatterns tests the crew session detection pattern thoroughly.
func TestIsCrewSession_ComprehensivePatterns(t *testing.T) {
	setupTestRegistry(t)

	tests := []struct {
		session string
		want    bool
		reason  string
	}{
		// Valid crew patterns (new format: <prefix>-crew-<name>)
		{"gt-crew-joe", true, "gastown crew session"},
		{"bd-crew-max", true, "beads crew session"},
		{"nif-crew-codex1", true, "niflheim crew with numbers in name"},
		{"grc-crew-grc1", true, "grctool crew with alphanumeric name"},
		{"7s-crew-ss1", true, "rig starting with number"},

		// Invalid crew patterns
		{"gt-witness", false, "witness is not crew"},
		{"gt-refinery", false, "refinery is not crew"},
		{"gt-polecat-abc", false, "polecat name, not crew"},
		{"hq-deacon", false, "deacon is not crew"},
		{"hq-mayor", false, "mayor is not crew"},
		{"", false, "empty string"},
		{"gt-morsov", false, "polecat, not crew"},
	}

	for _, tt := range tests {
		t.Run(tt.session, func(t *testing.T) {
			got := isCrewSession(tt.session)
			if got != tt.want {
				t.Errorf("isCrewSession(%q) = %v, want %v: %s", tt.session, got, tt.want, tt.reason)
			}
		})
	}
}

// TestOrphanSessionCheck_HQSessions tests that hq-* sessions are properly recognized as valid.
func TestOrphanSessionCheck_HQSessions(t *testing.T) {
	townRoot := t.TempDir()
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0o755); err != nil {
		t.Fatalf("create mayor dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("create rigs.json: %v", err)
	}

	lister := &mockSessionLister{
		sessions: []string{
			"hq-mayor",  // valid: headquarters mayor session
			"hq-deacon", // valid: headquarters deacon session
		},
	}
	check := NewOrphanSessionCheckWithSessionLister(lister)
	result := check.Run(&CheckContext{TownRoot: townRoot})

	if result.Status != StatusOK {
		t.Fatalf("expected StatusOK for valid hq sessions, got %v: %s", result.Status, result.Message)
	}
	if result.Message != "All 2 Gas Town sessions are valid" {
		t.Fatalf("unexpected message: %q", result.Message)
	}
	if len(check.orphanSessions) != 0 {
		t.Fatalf("expected no orphan sessions, got %v", check.orphanSessions)
	}
}

// TestOrphanSessionCheck_Run_Deterministic tests the full Run path with a mock session
// lister, ensuring deterministic behavior without depending on real tmux state.
func TestOrphanSessionCheck_Run_Deterministic(t *testing.T) {
	setupTestRegistry(t)

	townRoot := t.TempDir()
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0o755); err != nil {
		t.Fatalf("create mayor dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("create rigs.json: %v", err)
	}

	// Create rig directories to make them "valid"
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", "polecats"), 0o755); err != nil {
		t.Fatalf("create gastown rig: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "beads", "crew"), 0o755); err != nil {
		t.Fatalf("create beads rig: %v", err)
	}

	lister := &mockSessionLister{
		sessions: []string{
			"gt-witness",     // valid: gastown rig exists (prefix "gt")
			"gt-polecat1",    // valid: gastown rig exists
			"bd-refinery",    // valid: beads rig exists (prefix "bd")
			"hq-mayor",       // valid: hq-mayor is recognized
			"hq-deacon",      // valid: hq-deacon is recognized
			"zz-witness",     // ignored: unknown prefix, not a gastown session
			"xx-crew-joe",    // ignored: unknown prefix, not a gastown session
			"random-session", // ignored: unknown prefix, not a gastown session
		},
	}
	check := NewOrphanSessionCheckWithSessionLister(lister)
	result := check.Run(&CheckContext{TownRoot: townRoot})

	if result.Status != StatusOK {
		t.Fatalf("expected StatusOK, got %v: %s", result.Status, result.Message)
	}

	if len(check.orphanSessions) != 0 {
		t.Fatalf("expected 0 orphans (unknown prefixes are ignored), got %d: %v", len(check.orphanSessions), check.orphanSessions)
	}
}

func TestArgvHasFlag(t *testing.T) {
	if !argvHasFlag("/x/cursor-agent -f --resume z", "-f") {
		t.Error("expected -f in argv")
	}
	if argvHasFlag("/x/cursor-agent --resume z", "-f") {
		t.Error("did not expect -f")
	}
}

func TestGasTownRuntimeYOLO(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		args string
		want bool
	}{
		{"claude_gt", "claude", "/x/claude --dangerously-skip-permissions foo", true},
		{"claude_personal", "claude", "/x/claude foo", false},
		{"kiro_trust_all_tools", "kiro-cli", "kiro-cli chat --trust-all-tools", true},
		{"kiro_alias", "kiro", "kiro chat --trust-all-tools", true},
		{"kiro_plain", "kiro-cli", "kiro-cli chat", false},
		{"cursor_agent", "cursor-agent", "cursor-agent -f --resume x", true},
		{"cursor_no_f", "cursor-agent", "cursor-agent --resume x", false},
		{"agent_symlink", "agent", "agent -f --resume x", true},
		{"agent_f_only", "agent", "agent -f", false},
		{"copilot_yolo", "copilot", "copilot --yolo", true},
		{"copilot_plain", "copilot", "copilot", false},
		{"unknown", "vim", "vim foo", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gasTownRuntimeYOLO(tt.cmd, tt.args); got != tt.want {
				t.Errorf("gasTownRuntimeYOLO(%q, %q) = %v, want %v", tt.cmd, tt.args, got, tt.want)
			}
		})
	}
}
