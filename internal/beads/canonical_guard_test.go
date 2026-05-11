package beads

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalRuntimeGuard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	canonical := filepath.Join(home, ".beads-runtime", ".beads")
	if err := os.MkdirAll(canonical, 0755); err != nil {
		t.Fatal(err)
	}

	if !IsCanonicalRuntimeDir(canonical) {
		t.Fatalf("expected %s to be canonical runtime", canonical)
	}
	if err := GuardNonCanonicalRuntime(canonical, "test operation"); err == nil {
		t.Fatal("expected canonical runtime guard to reject mutation")
	}

	localRig := filepath.Join(home, "gt", "rig", ".beads")
	if err := os.MkdirAll(localRig, 0755); err != nil {
		t.Fatal(err)
	}
	if IsCanonicalRuntimeDir(localRig) {
		t.Fatalf("did not expect %s to be canonical runtime", localRig)
	}
	if err := GuardNonCanonicalRuntime(localRig, "test operation"); err != nil {
		t.Fatalf("expected local rig runtime to pass guard: %v", err)
	}
}

func TestEnvWithBeadsDirStripsInheritedRouting(t *testing.T) {
	got := EnvWithBeadsDir([]string{
		"BEADS_DIR=/bad",
		"BEADS_DB=/bad/db",
		"BEADS_DOLT_SERVER_DATABASE=bad",
		"KEEP=1",
	}, "/good/.beads")

	seenGood := false
	for _, entry := range got {
		switch entry {
		case "BEADS_DIR=/bad", "BEADS_DB=/bad/db", "BEADS_DOLT_SERVER_DATABASE=bad":
			t.Fatalf("inherited Beads routing env was not stripped: %v", got)
		case "BEADS_DIR=/good/.beads":
			seenGood = true
		}
	}
	if !seenGood {
		t.Fatalf("explicit BEADS_DIR missing from env: %v", got)
	}
}
