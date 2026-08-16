package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jonbaldie/gastown/internal/formula"
)

func TestNewFormulaCheck(t *testing.T) {
	check := NewFormulaCheck()
	if check.Name() != "formulas" {
		t.Errorf("Name() = %q, want %q", check.Name(), "formulas")
	}
	if !check.CanFix() {
		t.Error("FormulaCheck should be fixable")
	}
}

func TestFormulaCheck_Run_AllOK(t *testing.T) {
	tmpDir := t.TempDir()

	// Provision formulas fresh
	_, err := formula.ProvisionFormulas(tmpDir)
	if err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}

	check := NewFormulaCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("Status = %v, want %v", result.Status, StatusOK)
	}
}

func TestFormulaCheck_Run_Missing(t *testing.T) {
	tmpDir := t.TempDir()

	// Provision formulas
	_, err := formula.ProvisionFormulas(tmpDir)
	if err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}

	// Delete a formula
	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	formulaPath := filepath.Join(formulasDir, "mol-deacon-patrol.formula.toml")
	if err := os.Remove(formulaPath); err != nil {
		t.Fatal(err)
	}

	check := NewFormulaCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("Status = %v, want %v", result.Status, StatusWarning)
	}
	if result.FixHint == "" {
		t.Error("should have FixHint")
	}
}

func TestFormulaCheck_Fix(t *testing.T) {
	tmpDir := t.TempDir()

	// Provision formulas
	_, err := formula.ProvisionFormulas(tmpDir)
	if err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}

	// Delete a formula
	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	formulaPath := filepath.Join(formulasDir, "mol-deacon-patrol.formula.toml")
	if err := os.Remove(formulaPath); err != nil {
		t.Fatal(err)
	}

	check := NewFormulaCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Run fix
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() error: %v", err)
	}

	// Verify formula was restored
	if _, err := os.Stat(formulaPath); os.IsNotExist(err) {
		t.Error("formula should have been restored")
	}

	// Re-run check - should be OK now
	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("after fix, Status = %v, want %v", result.Status, StatusOK)
	}
}
