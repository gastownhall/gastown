package beads

import (
	"strings"
	"testing"
)

// TestEnvForSubprocessMode_SuppressesAutoImport guarantees that every bd
// subprocess mode used by ConfigureCommand sets BEADS_NO_AUTO_IMPORT=1 — and
// strips any inherited value that would re-enable it. The daemon dog patrols
// (dog_molecule.runBd) and other migrated raw bd call sites route through this
// helper, so this is the regression guard for the self-feeding "create N
// issue(s)" loop where watchdog/compaction dogs auto-imported a stale
// .beads/issues.jsonl over the live Dolt server.
func TestEnvForSubprocessMode_SuppressesAutoImport(t *testing.T) {
	t.Parallel()
	// Hostile base: explicitly turns auto-import ON, to prove we override it.
	base := []string{"BEADS_NO_AUTO_IMPORT=0", "PATH=/usr/bin"}
	for name, mode := range map[string]SubprocessEnvMode{
		"ReadOnlyRouting": ReadOnlyRouting,
		"MutationRouting": MutationRouting,
		"ReadOnlyPinned":  ReadOnlyPinned,
		"MutationPinned":  MutationPinned,
	} {
		env := EnvForSubprocessMode(base, "/town/.beads", mode)
		assertNoAutoImport(t, name, env)
	}
}

// TestSubprocessModeForArgs_SuppressesAutoImport ensures the arg-derived mode
// (read vs mutation) used by dog_molecule.runBd, prime, and witness yields
// BEADS_NO_AUTO_IMPORT=1 for both read and write bd commands.
func TestSubprocessModeForArgs_SuppressesAutoImport(t *testing.T) {
	t.Parallel()
	base := []string{"BEADS_NO_AUTO_IMPORT=0"}
	for _, args := range [][]string{
		{"list", "--json"},
		{"show", "hq-1", "--json"},
		{"close", "hq-1", "--reason=x"},
		{"update", "hq-1", "--status=open"},
	} {
		env := EnvForSubprocessMode(base, "/town/.beads", SubprocessModeForArgs(args))
		assertNoAutoImport(t, strings.Join(args, " "), env)
	}
}

func assertNoAutoImport(t *testing.T, label string, env []string) {
	t.Helper()
	found := false
	for _, kv := range env {
		switch kv {
		case "BEADS_NO_AUTO_IMPORT=1":
			found = true
		case "BEADS_NO_AUTO_IMPORT=0":
			t.Fatalf("%s: inherited BEADS_NO_AUTO_IMPORT=0 was not stripped; got %v", label, env)
		}
	}
	if !found {
		t.Fatalf("%s: env missing BEADS_NO_AUTO_IMPORT=1; got %v", label, env)
	}
}
