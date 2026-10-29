package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newReachableTown builds a town root with routes.jsonl describing one rig.
// mayor/town.json is what beads.FindTownRoot keys on, so the check resolves the
// town database through the same path the runtime uses.
func newReachableTown(t *testing.T, routes string) string {
	t.Helper()
	townRoot := t.TempDir()
	townBeads := filepath.Join(townRoot, ".beads")
	mayorDir := filepath.Join(townRoot, "mayor")
	for _, dir := range []string{townBeads, mayorDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routes), 0o644); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte(`{"name":"test"}`), 0o644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	return townRoot
}

// mkdirs creates every directory or fails the test.
func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
}

func TestAgentBeadReachableCheck_Metadata(t *testing.T) {
	check := NewAgentBeadReachableCheck()
	if check.Name() != "agent-bead-reachable" {
		t.Errorf("Name() = %q, want agent-bead-reachable", check.Name())
	}
	if check.Category() != CategoryRig {
		t.Errorf("Category() = %v, want %v", check.Category(), CategoryRig)
	}
}

func TestAgentBeadReachableCheck_NoAgentDirs(t *testing.T) {
	// No rig agent directories exist, and the rig scope excludes mayor/deacon,
	// so the check has nothing to assert and must not open any database.
	townRoot := newReachableTown(t, `{"prefix":"al-","path":"alphaprime2/mayor/rig"}`+"\n")

	check := NewAgentBeadReachableCheck()
	check.beadIDs = func(string) map[string]bool {
		t.Fatal("beadIDs should not be consulted when no agent directories exist")
		return nil
	}

	result := check.Run(&CheckContext{TownRoot: townRoot, RigName: "alphaprime2"})
	if result.Status != StatusOK {
		t.Fatalf("Status = %v, want OK (message: %s)", result.Status, result.Message)
	}
}

// TestAgentBeadReachableCheck_TownFallbackIsReachable pins the fix for hq-5z6:
// a rig agent whose cwd resolves to the rig database still counts as reachable
// because agent-bead operations fall back to the town database.
func TestAgentBeadReachableCheck_TownFallbackIsReachable(t *testing.T) {
	townRoot := newReachableTown(t, `{"prefix":"al-","path":"alphaprime2/mayor/rig"}`+"\n")

	rigBeads := filepath.Join(townRoot, "alphaprime2", ".beads")
	witnessDir := filepath.Join(townRoot, "alphaprime2", "witness")
	for _, dir := range []string{rigBeads, witnessDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	townBeads := filepath.Join(townRoot, ".beads")
	check := NewAgentBeadReachableCheck()
	check.beadIDs = func(beadsDir string) map[string]bool {
		if beadsDir == townBeads {
			return map[string]bool{"al-alphaprime2-witness": true}
		}
		return nil // The rig database holds no agent beads — the hq-5z6 shape.
	}

	result := check.Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusOK {
		t.Fatalf("Status = %v, want OK (message: %s, details: %v)", result.Status, result.Message, result.Details)
	}
	if !strings.Contains(result.Message, "All 1 agent bead(s)") {
		t.Errorf("Message = %q, want it to report 1 checked agent bead", result.Message)
	}
}

// TestAgentBeadReachableCheck_ReportsUnreachable covers the failure this check
// exists for: an agent bead that exists, but in a database the owning agent's
// directory never consults — here the refinery's bead landed in the *other*
// rig's database, so neither the alphaprime2 rig database nor town holds it.
func TestAgentBeadReachableCheck_ReportsUnreachable(t *testing.T) {
	routes := strings.Join([]string{
		`{"prefix":"al-","path":"alphaprime2/mayor/rig"}`,
		`{"prefix":"gs-","path":"gastown/mayor/rig"}`,
	}, "\n") + "\n"
	townRoot := newReachableTown(t, routes)

	rigBeads := filepath.Join(townRoot, "alphaprime2", ".beads")
	otherRigBeads := filepath.Join(townRoot, "gastown", ".beads")
	mkdirs(t,
		rigBeads,
		otherRigBeads,
		filepath.Join(townRoot, "alphaprime2", "witness"),
		filepath.Join(townRoot, "alphaprime2", "refinery"),
		filepath.Join(townRoot, "gastown", "witness"),
	)

	townBeads := filepath.Join(townRoot, ".beads")
	check := NewAgentBeadReachableCheck()
	check.beadIDs = func(beadsDir string) map[string]bool {
		switch beadsDir {
		case townBeads:
			return map[string]bool{"al-alphaprime2-witness": true}
		case otherRigBeads:
			// Misrouted: an alphaprime2 agent bead created in the gastown db.
			return map[string]bool{"al-alphaprime2-refinery": true}
		}
		return nil
	}

	result := check.Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusError {
		t.Fatalf("Status = %v, want Error (message: %s)", result.Status, result.Message)
	}
	if len(result.Details) != 1 {
		t.Fatalf("Details = %v, want exactly the refinery", result.Details)
	}
	if !strings.Contains(result.Details[0], "al-alphaprime2-refinery") {
		t.Errorf("Details[0] = %q, want the refinery agent bead", result.Details[0])
	}
	if !strings.Contains(result.Details[0], rigBeads) {
		t.Errorf("Details[0] = %q, want the resolved database %q", result.Details[0], rigBeads)
	}
}

// TestAgentBeadReachableCheck_IgnoresMissingBeads keeps this check from
// double-reporting what agent-beads-exist already owns: a bead that exists in
// no database at all is missing, not misrouted.
func TestAgentBeadReachableCheck_IgnoresMissingBeads(t *testing.T) {
	townRoot := newReachableTown(t, `{"prefix":"al-","path":"alphaprime2/mayor/rig"}`+"\n")
	mkdirs(t,
		filepath.Join(townRoot, "alphaprime2", ".beads"),
		filepath.Join(townRoot, "alphaprime2", "witness"),
		filepath.Join(townRoot, "alphaprime2", "refinery"),
	)

	check := NewAgentBeadReachableCheck()
	check.beadIDs = func(string) map[string]bool { return nil }

	result := check.Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusOK {
		t.Fatalf("Status = %v, want OK (message: %s, details: %v)", result.Status, result.Message, result.Details)
	}
	if !strings.Contains(result.Message, "All 0 agent bead(s)") {
		t.Errorf("Message = %q, want it to report 0 checked agent beads", result.Message)
	}
}

// TestAgentBeadReachableCheck_RespectsRigScope keeps a --rig run from asserting
// on other rigs' agents.
func TestAgentBeadReachableCheck_RespectsRigScope(t *testing.T) {
	routes := strings.Join([]string{
		`{"prefix":"al-","path":"alphaprime2/mayor/rig"}`,
		`{"prefix":"gs-","path":"gastown/mayor/rig"}`,
	}, "\n") + "\n"
	townRoot := newReachableTown(t, routes)

	gastownBeads := filepath.Join(townRoot, "gastown", ".beads")
	mkdirs(t,
		filepath.Join(townRoot, "alphaprime2", ".beads"),
		gastownBeads,
		filepath.Join(townRoot, "alphaprime2", "witness"),
		filepath.Join(townRoot, "gastown", "witness"),
		filepath.Join(townRoot, "deacon"),
	)

	check := NewAgentBeadReachableCheck()
	check.beadIDs = func(beadsDir string) map[string]bool {
		// Both rigs' witness beads are misrouted into the gastown database, so a
		// town-wide run would flag both. The rig-scoped run must flag only its own.
		if beadsDir == gastownBeads {
			return map[string]bool{
				"al-alphaprime2-witness": true,
				"gs-gastown-witness":     true,
			}
		}
		return nil
	}

	result := check.Run(&CheckContext{TownRoot: townRoot, RigName: "alphaprime2"})
	if result.Status != StatusError {
		t.Fatalf("Status = %v, want Error (message: %s)", result.Status, result.Message)
	}
	if len(result.Details) != 1 {
		t.Fatalf("Details = %v, want only the alphaprime2 witness", result.Details)
	}
	for _, detail := range result.Details {
		if strings.Contains(detail, "gs-gastown") || strings.Contains(detail, "hq-mayor") || strings.Contains(detail, "hq-deacon") {
			t.Errorf("rig-scoped run reported out-of-scope agent: %q", detail)
		}
	}
}

func TestResolveBeadsDirFrom(t *testing.T) {
	townRoot := t.TempDir()
	rigBeads := filepath.Join(townRoot, "alphaprime2", ".beads")
	redirectDir := filepath.Join(townRoot, "alphaprime2", "witness")
	redirectBeads := filepath.Join(redirectDir, ".beads")
	for _, dir := range []string{rigBeads, redirectBeads} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	// Redirect targets are relative to the directory that holds .beads.
	if err := os.WriteFile(filepath.Join(redirectBeads, "redirect"), []byte("../.beads"), 0o644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	if got := resolveBeadsDirFrom(redirectDir); got != rigBeads {
		t.Errorf("resolveBeadsDirFrom(redirect dir) = %q, want %q", got, rigBeads)
	}

	// Walking up finds the nearest ancestor .beads.
	nested := filepath.Join(townRoot, "alphaprime2", "refinery", "rig")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if got := resolveBeadsDirFrom(nested); got != rigBeads {
		t.Errorf("resolveBeadsDirFrom(nested) = %q, want %q", got, rigBeads)
	}

	// Outside any beads workspace.
	if got := resolveBeadsDirFrom(t.TempDir()); got != "" {
		t.Errorf("resolveBeadsDirFrom(outside) = %q, want \"\"", got)
	}
}
