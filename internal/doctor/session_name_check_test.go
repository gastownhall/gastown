package doctor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/session"
)

// testRegistryForNameCheck returns a PrefixRegistry with a few known rigs
// suitable for session-name-format tests.
func testRegistryForNameCheck() *session.PrefixRegistry {
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("nif", "niflheim")
	reg.Register("wa", "whatsapp_automation")
	return reg
}

func TestNewMalformedSessionNameCheck(t *testing.T) {
	check := NewMalformedSessionNameCheck()

	if check.Name() != "session-name-format" {
		t.Errorf("expected name 'session-name-format', got %q", check.Name())
	}

	if check.Description() != "Detect sessions with outdated Gas Town naming format" {
		t.Errorf("unexpected description: %q", check.Description())
	}

	if !check.CanFix() {
		t.Error("expected CanFix to return true")
	}

	if check.Category() != CategoryCleanup {
		t.Errorf("expected category %q, got %q", CategoryCleanup, check.Category())
	}
}

func TestMalformedSessionNameCheck_Run_NoSessions(t *testing.T) {
	check := NewMalformedSessionNameCheck()
	check.sessionListerForTest = &mockSessionLister{sessions: []string{}}
	check.registryForTest = testRegistryForNameCheck()

	ctx := &CheckContext{TownRoot: t.TempDir()}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected OK with no sessions, got %v: %s", result.Status, result.Message)
	}
}

// TestMalformedSessionNameCheck_Run_AllCorrect verifies that sessions already
// in canonical format produce a clean result. This test uses a populated
// registry so sessions actually parse — it does not pass vacuously.
func TestMalformedSessionNameCheck_Run_AllCorrect(t *testing.T) {
	reg := testRegistryForNameCheck()
	check := NewMalformedSessionNameCheck()
	check.registryForTest = reg
	check.sessionListerForTest = &mockSessionLister{sessions: []string{
		"hq-mayor",
		"hq-deacon",
		"gt-witness",
		"nif-refinery",
		"wa-crew-batista",
	}}

	ctx := &CheckContext{TownRoot: t.TempDir()}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected OK for correctly-named sessions, got %v: %s\nDetails: %v",
			result.Status, result.Message, result.Details)
	}
}

func TestMalformedSessionNameCheck_Run_NonGasTownSessions(t *testing.T) {
	check := NewMalformedSessionNameCheck()
	check.registryForTest = testRegistryForNameCheck()
	check.sessionListerForTest = &mockSessionLister{sessions: []string{
		"my-personal-session",
		"vim",
		"jupyter",
	}}

	ctx := &CheckContext{TownRoot: t.TempDir()}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected OK for non-Gas Town sessions, got %v", result.Status)
	}
}

// TestMalformedSessionNameCheck_Run_NonGasTownWithRigSubstring verifies that
// non-Gastown sessions whose names happen to contain a rig name are NOT
// falsely flagged. The ownership guard requires a known Gastown prefix.
func TestMalformedSessionNameCheck_Run_NonGasTownWithRigSubstring(t *testing.T) {
	check := NewMalformedSessionNameCheck()
	check.registryForTest = testRegistryForNameCheck()
	check.sessionListerForTest = &mockSessionLister{sessions: []string{
		"my-niflheim-witness",              // "my" is not a known Gastown prefix
		"foo-gastown-refinery",             // "foo" is not a known Gastown prefix
		"test-whatsapp_automation-witness", // "test" is not a known Gastown prefix
	}}

	ctx := &CheckContext{TownRoot: t.TempDir()}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected OK for non-Gastown sessions with rig substrings, got %v: %s\nDetails: %v",
			result.Status, result.Message, result.Details)
	}
}

// TestMalformedSessionNameCheck_Run_PolecatWithRigSubstring verifies that
// polecat sessions whose names embed a rig name are NOT falsely flagged.
// E.g., "gt-fix-gastown-witness" is a polecat named "fix-gastown-witness",
// not a legacy gastown witness session.
func TestMalformedSessionNameCheck_Run_PolecatWithRigSubstring(t *testing.T) {
	check := NewMalformedSessionNameCheck()
	check.registryForTest = testRegistryForNameCheck()
	check.sessionListerForTest = &mockSessionLister{sessions: []string{
		"gt-fix-gastown-witness",      // polecat "fix-gastown-witness", prefix "gt-fix" is not known
		"nif-debug-niflheim-refinery", // prefix "nif-debug" is not a known prefix
	}}

	ctx := &CheckContext{TownRoot: t.TempDir()}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected OK for polecat sessions with rig substrings, got %v: %s\nDetails: %v",
			result.Status, result.Message, result.Details)
	}
}

// TestMalformedSessionNameCheck_Run_DetectsMismatch is the core test.
// It verifies that a genuine legacy name (gt-niflheim-witness) is detected
// and the canonical name (nif-witness) is reported.
func TestMalformedSessionNameCheck_Run_DetectsMismatch(t *testing.T) {
	check := NewMalformedSessionNameCheck()
	check.registryForTest = testRegistryForNameCheck()
	check.sessionListerForTest = &mockSessionLister{sessions: []string{
		"hq-mayor",
		"gt-niflheim-witness",  // legacy: should be nif-witness
		"gt-niflheim-refinery", // legacy: should be nif-refinery
		"nif-refinery",         // already canonical — should not be flagged
	}}

	ctx := &CheckContext{TownRoot: t.TempDir()}
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected Warning for legacy sessions, got %v: %s", result.Status, result.Message)
	}

	// Should find exactly the 2 legacy sessions.
	if len(result.Details) != 2 {
		t.Errorf("expected 2 details, got %d: %v", len(result.Details), result.Details)
	}

	// Verify the canonical renames are present in the details.
	for _, want := range []string{"gt-niflheim-witness", "nif-witness", "gt-niflheim-refinery", "nif-refinery"} {
		found := false
		for _, d := range result.Details {
			if strings.Contains(d, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected details to contain %q, got: %v", want, result.Details)
		}
	}
}

// TestMalformedSessionNameCheck_Run_LegacyWAWitness verifies the stated use
// case: gt-whatsapp_automation-witness → wa-witness.
func TestMalformedSessionNameCheck_Run_LegacyWAWitness(t *testing.T) {
	check := NewMalformedSessionNameCheck()
	check.registryForTest = testRegistryForNameCheck()
	check.sessionListerForTest = &mockSessionLister{sessions: []string{
		"gt-whatsapp_automation-witness",
	}}

	ctx := &CheckContext{TownRoot: t.TempDir()}
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Fatalf("expected Warning for legacy wa-witness session, got %v", result.Status)
	}
	if len(result.Details) != 1 {
		t.Fatalf("expected 1 detail, got %d: %v", len(result.Details), result.Details)
	}
	d := result.Details[0]
	if !strings.Contains(d, "gt-whatsapp_automation-witness") || !strings.Contains(d, "wa-witness") {
		t.Errorf("expected detail to map legacy → canonical, got: %q", d)
	}
}

// TestMalformedSessionNameCheck_Run_CrewSession verifies that legacy crew
// sessions are detected and flagged with a "manual rename required" note,
// since Fix() cannot safely rename attached crew sessions.
func TestMalformedSessionNameCheck_Run_CrewSession(t *testing.T) {
	check := NewMalformedSessionNameCheck()
	check.registryForTest = testRegistryForNameCheck()
	check.sessionListerForTest = &mockSessionLister{sessions: []string{
		"gt-niflheim-crew-wolf",
	}}

	ctx := &CheckContext{TownRoot: t.TempDir()}
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Fatalf("expected Warning for legacy crew session, got %v", result.Status)
	}
	// Detail must mention "manual rename" so the user knows --fix won't fix it.
	for _, d := range result.Details {
		if strings.Contains(d, "gt-niflheim-crew-wolf") {
			if !strings.Contains(d, "manual") {
				t.Errorf("crew session detail should mention manual rename, got: %q", d)
			}
			return
		}
	}
	t.Errorf("crew session not found in details: %v", result.Details)
}

// TestMalformedSessionNameCheck_Fix_Rename verifies the happy path: Fix()
// renames a legacy session to its canonical name.
func TestMalformedSessionNameCheck_Fix_Rename(t *testing.T) {
	check := NewMalformedSessionNameCheck()
	check.registryForTest = testRegistryForNameCheck()

	// Pre-populate the cached malformed list (as if Run was called).
	check.malformed = []sessionRename{
		{oldName: "gt-niflheim-witness", newName: "nif-witness", isCrew: false},
	}

	mt := &mockTmux{
		sessions:    map[string]bool{"gt-niflheim-witness": true},
		renamedFrom: []string{},
		renamedTo:   []string{},
	}
	check.tmuxForTest = mt

	ctx := &CheckContext{TownRoot: t.TempDir()}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() returned error: %v", err)
	}

	if len(mt.renamedFrom) != 1 || mt.renamedFrom[0] != "gt-niflheim-witness" {
		t.Errorf("expected rename from gt-niflheim-witness, got: %v", mt.renamedFrom)
	}
	if mt.renamedTo[0] != "nif-witness" {
		t.Errorf("expected rename to nif-witness, got: %q", mt.renamedTo[0])
	}
}

// TestMalformedSessionNameCheck_Fix_SkipsCrew verifies that crew sessions are
// NOT renamed by Fix() (they need manual intervention).
func TestMalformedSessionNameCheck_Fix_SkipsCrew(t *testing.T) {
	check := NewMalformedSessionNameCheck()
	check.malformed = []sessionRename{
		{oldName: "gt-niflheim-crew-wolf", newName: "nif-crew-wolf", isCrew: true},
	}

	mt := &mockTmux{sessions: map[string]bool{"gt-niflheim-crew-wolf": true}}
	check.tmuxForTest = mt

	ctx := &CheckContext{TownRoot: t.TempDir()}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() returned error: %v", err)
	}

	if len(mt.renamedFrom) != 0 {
		t.Errorf("Fix() should not rename crew sessions, but renamed: %v", mt.renamedFrom)
	}
}

// TestMalformedSessionNameCheck_Fix_SkipsCollision verifies that if the target
// name is already in use, Fix() skips the rename to avoid clobbering.
func TestMalformedSessionNameCheck_Fix_SkipsCollision(t *testing.T) {
	check := NewMalformedSessionNameCheck()
	check.malformed = []sessionRename{
		{oldName: "gt-niflheim-witness", newName: "nif-witness", isCrew: false},
	}

	mt := &mockTmux{
		sessions: map[string]bool{
			"gt-niflheim-witness": true,
			"nif-witness":         true, // target already exists
		},
	}
	check.tmuxForTest = mt

	ctx := &CheckContext{TownRoot: t.TempDir()}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() returned error: %v", err)
	}

	if len(mt.renamedFrom) != 0 {
		t.Errorf("Fix() should skip when target exists, but renamed: %v", mt.renamedFrom)
	}
}

// TestMalformedSessionNameCheck_Fix_TOCTOUGuard verifies that Fix() skips a
// rename when the source session no longer exists (killed between Run and Fix).
func TestMalformedSessionNameCheck_Fix_TOCTOUGuard(t *testing.T) {
	check := NewMalformedSessionNameCheck()
	check.malformed = []sessionRename{
		{oldName: "gt-niflheim-witness", newName: "nif-witness", isCrew: false},
	}

	mt := &mockTmux{
		sessions: map[string]bool{
			// source is gone — simulates zombie check killing it between Run and Fix
		},
	}
	check.tmuxForTest = mt

	ctx := &CheckContext{TownRoot: t.TempDir()}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() returned error: %v", err)
	}

	if len(mt.renamedFrom) != 0 {
		t.Errorf("Fix() should skip when source is gone, but renamed: %v", mt.renamedFrom)
	}
}

// TestMalformedSessionNameCheck_Fix_HasSessionError verifies that Fix()
// surfaces HasSession errors instead of silently ignoring them.
func TestMalformedSessionNameCheck_Fix_HasSessionError(t *testing.T) {
	check := NewMalformedSessionNameCheck()
	check.malformed = []sessionRename{
		{oldName: "gt-niflheim-witness", newName: "nif-witness", isCrew: false},
	}

	mt := &mockTmux{
		sessions:      map[string]bool{},
		hasSessionErr: fmt.Errorf("tmux socket unavailable"),
	}
	check.tmuxForTest = mt

	ctx := &CheckContext{TownRoot: t.TempDir()}
	err := check.Fix(ctx)
	if err == nil {
		t.Fatal("Fix() should return error when HasSession fails, got nil")
	}
	if !strings.Contains(err.Error(), "tmux socket unavailable") {
		t.Errorf("Fix() error should contain HasSession error, got: %v", err)
	}

	if len(mt.renamedFrom) != 0 {
		t.Errorf("Fix() should not rename when HasSession errors, but renamed: %v", mt.renamedFrom)
	}
}

// mockTmux is a tmux.Tmux stub for testing Fix() without real tmux.
type mockTmux struct {
	sessions      map[string]bool
	renamedFrom   []string
	renamedTo     []string
	hasSessionErr error // If non-nil, HasSession returns this error
}

func (m *mockTmux) HasSession(name string) (bool, error) {
	if m.hasSessionErr != nil {
		return false, m.hasSessionErr
	}
	return m.sessions[name], nil
}

func (m *mockTmux) RenameSession(from, to string) error {
	m.renamedFrom = append(m.renamedFrom, from)
	m.renamedTo = append(m.renamedTo, to)
	// Update sessions map to reflect the rename.
	delete(m.sessions, from)
	m.sessions[to] = true
	return nil
}
