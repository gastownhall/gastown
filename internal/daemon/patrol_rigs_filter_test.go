package daemon

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/wisp"
)

func TestEnsureRigWispConfigs_CreatesMissingAndPreservesExisting(t *testing.T) {
	townRoot := t.TempDir()
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0o755); err != nil {
		t.Fatalf("mkdir mayor dir: %v", err)
	}
	rigsJSON := `{"rigs":{"alpha":{},"beta":{}}}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0o644); err != nil {
		t.Fatalf("write rigs.json: %v", err)
	}
	if err := wisp.NewConfig(townRoot, "beta").Set("status", "parked"); err != nil {
		t.Fatalf("set beta parked: %v", err)
	}

	var logs bytes.Buffer
	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(&logs, "", 0),
	}
	d.ensureRigWispConfigs()

	if _, err := os.Stat(wisp.NewConfig(townRoot, "alpha").ConfigPath()); err != nil {
		t.Fatalf("alpha config was not created: %v", err)
	}
	if got := wisp.NewConfig(townRoot, "beta").GetString("status"); got != "parked" {
		t.Fatalf("beta status = %q, want parked", got)
	}
	if strings.Contains(logs.String(), "no wisp config") {
		t.Fatalf("unexpected repeated missing-config warning: %s", logs.String())
	}
}

// Regression test for gt-arz:
// getPatrolRigs should filter parked/docked rigs at list-building time.
func TestGetPatrolRigs_FiltersNonOperationalRigs(t *testing.T) {
	townRoot := t.TempDir()

	// Seed known rigs.
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

	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(os.Stderr, "[test] ", 0),
	}

	got := d.getPatrolRigs("witness")
	slices.Sort(got)
	// When Dolt is unavailable, isRigOperational() fails safe and returns false
	// for all rigs (can't verify docked status). This prevents witnesses from
	// starting for potentially docked rigs during Dolt outages.
	want := []string{}
	if !slices.Equal(got, want) {
		t.Fatalf("getPatrolRigs() = %v, want %v (all rigs excluded when Dolt unavailable - fail-safe)", got, want)
	}
}
