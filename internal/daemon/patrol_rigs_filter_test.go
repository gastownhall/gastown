package daemon

import (
	"log"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/wisp"
)

// newPatrolFilterTown seeds a town with rigs alpha/beta/gamma, marks beta
// parked and gamma docked via the wisp layer, and returns a daemon for it.
func newPatrolFilterTown(t *testing.T) *Daemon {
	t.Helper()
	townRoot := t.TempDir()

	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0o755); err != nil {
		t.Fatalf("mkdir mayor dir: %v", err)
	}
	rigsJSON := `{"rigs":{"alpha":{},"beta":{},"gamma":{}}}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0o644); err != nil {
		t.Fatalf("write rigs.json: %v", err)
	}

	// Mark beta/gamma as non-operational via wisp status.
	if err := wisp.NewConfig(townRoot, "beta").Set("status", "parked"); err != nil {
		t.Fatalf("set beta parked: %v", err)
	}
	if err := wisp.NewConfig(townRoot, "gamma").Set("status", "docked"); err != nil {
		t.Fatalf("set gamma docked: %v", err)
	}

	return &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(os.Stderr, "[test] ", 0),
	}
}

// Regression test for gt-arz:
// getPatrolRigs should filter parked/docked rigs at list-building time.
func TestGetPatrolRigs_FiltersNonOperationalRigs(t *testing.T) {
	d := newPatrolFilterTown(t)

	withStubbedRigBeadLookup(t, func(townRoot, rigName, prefix string) (*beads.Issue, error) {
		return &beads.Issue{ID: beads.RigBeadIDWithPrefix(prefix, rigName), Labels: []string{"gt:rig"}}, nil
	})

	got := d.getPatrolRigs("witness")
	slices.Sort(got)
	want := []string{"alpha"}
	if !slices.Equal(got, want) {
		t.Fatalf("getPatrolRigs() = %v, want %v", got, want)
	}
}

// Regression test for gt-gf6: a rig identity bead that cannot be found must not
// remove the rig from patrol. The prefix-routing miss made every lookup return
// "not found", which excluded all four rigs from both the witness and refinery
// patrols for hours — the daemon stopped supervising the entire town while
// reporting healthy.
func TestGetPatrolRigs_MissingRigBeadsDoNotEmptyPatrol(t *testing.T) {
	d := newPatrolFilterTown(t)

	withStubbedRigBeadLookup(t, func(townRoot, rigName, prefix string) (*beads.Issue, error) {
		return nil, beads.ErrNotFound
	})

	got := d.getPatrolRigs("witness")
	slices.Sort(got)
	want := []string{"alpha"}
	if !slices.Equal(got, want) {
		t.Fatalf("getPatrolRigs() = %v, want %v (missing rig beads must not disable supervision)", got, want)
	}
}
