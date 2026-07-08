package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseCIGateBlockedLabel(t *testing.T) {
	ts, ok := parseCIGateBlockedLabel("ci-gate-blocked:1720000000")
	if !ok {
		t.Fatal("want ok for valid label")
	}
	if want := time.Unix(1720000000, 0); !ts.Equal(want) {
		t.Errorf("ts = %v, want %v", ts, want)
	}
	for _, bad := range []string{"ci-gate-blocked:", "ci-gate-blocked:abc", "needs-ci-green:5:1", "other"} {
		if _, ok := parseCIGateBlockedLabel(bad); ok {
			t.Errorf("parseCIGateBlockedLabel(%q) = ok, want !ok", bad)
		}
	}
}

func TestCIGateDirForPolecat(t *testing.T) {
	rigPath := t.TempDir()
	mayorClone := filepath.Join(rigPath, "mayor", "rig")

	t.Run("missing clone falls back to mayor clone", func(t *testing.T) {
		if got := ciGateDirForPolecat(filepath.Join(rigPath, "gone"), rigPath); got != mayorClone {
			t.Errorf("dir = %q, want %q", got, mayorClone)
		}
	})
	t.Run("empty clone falls back to mayor clone", func(t *testing.T) {
		if got := ciGateDirForPolecat("", rigPath); got != mayorClone {
			t.Errorf("dir = %q, want %q", got, mayorClone)
		}
	})
	t.Run("existing clone preferred", func(t *testing.T) {
		clone := filepath.Join(rigPath, "polecats", "x", "rig")
		if err := os.MkdirAll(clone, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := ciGateDirForPolecat(clone, rigPath); got != clone {
			t.Errorf("dir = %q, want %q", got, clone)
		}
	})
}
