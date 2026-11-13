package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/formula"
)

// TestFormulaSyncForTown_ReinstallsMissing verifies the sync glue drives
// formula.UpdateFormulas against the town root: a deleted (missing) formula
// comes back from the embedded copies.
func TestFormulaSyncForTown_ReinstallsMissing(t *testing.T) {
	townRoot := t.TempDir()
	if _, err := formula.ProvisionFormulas(townRoot); err != nil {
		t.Fatalf("ProvisionFormulas: %v", err)
	}

	formulasDir := filepath.Join(townRoot, ".beads", "formulas")
	entries, err := os.ReadDir(formulasDir)
	if err != nil {
		t.Fatalf("read formulas dir: %v", err)
	}
	var victim string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".toml" {
			victim = filepath.Join(formulasDir, e.Name())
			break
		}
	}
	if victim == "" {
		t.Skip("no provisioned formulas to test with")
	}
	if err := os.Remove(victim); err != nil {
		t.Fatalf("remove formula: %v", err)
	}

	if err := formulaSyncForTown(townRoot); err != nil {
		t.Fatalf("formulaSyncForTown: %v", err)
	}

	if _, err := os.Stat(victim); err != nil {
		t.Errorf("missing formula was not reinstalled: %v", err)
	}
	report, err := formula.CheckFormulaHealth(townRoot)
	if err != nil {
		t.Fatalf("CheckFormulaHealth: %v", err)
	}
	if report.Missing != 0 || report.Outdated != 0 {
		t.Errorf("after sync: missing=%d outdated=%d, want 0/0", report.Missing, report.Outdated)
	}
}
