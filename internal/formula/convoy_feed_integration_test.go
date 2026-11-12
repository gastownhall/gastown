package formula

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/constants"
)

// TestConvoyFeedWorkflow_Integration is a high-level integration test that verifies
// the complete convoy feed workflow can be parsed and validated.
//
// This is a regression test for GitHub issue #1133:
// - mol-convoy-feed formula was committed with undefined template variables
// - This caused "bd mol wisp" to fail with "missing required variables" error
// - The fix adds computed variables to [vars] with default=""
//
// The test ensures:
// 1. The formula can be parsed
// 2. All template variables are defined in [vars]
// 3. The convoy input variable is marked as required
// 4. Computed variables have empty defaults (not required as inputs)
func TestConvoyFeedWorkflow_Integration(t *testing.T) {
	formulaPath := filepath.Join("formulas", constants.MolConvoyFeed+".formula.toml")
	data, err := os.ReadFile(formulaPath)
	if err != nil {
		t.Skipf("Formula file not found: %v", err)
	}

	// Step 1: Parse the formula
	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Failed to parse mol-convoy-feed formula: %v", err)
	}

	// Step 2: Verify it's a workflow formula
	if f.Type != TypeWorkflow {
		t.Errorf("Expected workflow type, got %s", f.Type)
	}

	// Step 3: Verify the convoy variable is required (this is the input)
	convoyVar, ok := f.Vars["convoy"]
	if !ok {
		t.Fatal("Missing required 'convoy' variable definition")
	}
	if !convoyVar.Required {
		t.Error("'convoy' variable should be marked as required")
	}

	// Step 4: Verify template variables are all defined
	if err := f.ValidateTemplateVariables(); err != nil {
		t.Errorf("Template variable validation failed: %v", err)
	}

	// Step 5: Verify computed variables have defaults (not required as inputs)
	computedVars := []string{
		"ready_count", "available_count", "dispatch_count",
		"issue_id", "title", "rig", "polecat",
		"error", "error_count", "report_summary",
	}
	for _, name := range computedVars {
		v, ok := f.Vars[name]
		if !ok {
			t.Errorf("Missing computed variable %q in [vars]", name)
			continue
		}
		// Computed variables should have default="" so they're not required as inputs
		// They get filled in by the dog during execution
		if v.Required {
			t.Errorf("Computed variable %q should not be marked required", name)
		}
	}

	// Step 6: Verify the formula has the expected steps
	expectedSteps := []string{
		"load-convoy",
		"check-capacity",
		"dispatch-work",
		"report-results",
		"return-to-kennel",
	}
	if len(f.Steps) != len(expectedSteps) {
		t.Errorf("Expected %d steps, got %d", len(expectedSteps), len(f.Steps))
	}
	for i, expected := range expectedSteps {
		if i >= len(f.Steps) {
			break
		}
		if f.Steps[i].ID != expected {
			t.Errorf("Step %d: expected %q, got %q", i, expected, f.Steps[i].ID)
		}
	}
}

// TestAllDogFormulas_CanBeWisped verifies that all dog formulas (those used by
// Deacon's dogs) can pass variable validation for wisp creation.
//
// Dog formulas are special because they're invoked via:
//
//	gt sling mol-<name> deacon/dogs/<dog> --var convoy=<id>
//
// The wisp creation validates that all template variables are either:
// - Provided via --var flags, OR
// - Defined in [vars] with a default value
func TestAllDogFormulas_CanBeWisped(t *testing.T) {
	dogFormulas := []string{
		constants.MolConvoyFeed,
		constants.MolConvoyCleanup,
		"mol-dep-propagate",
		"mol-digest-generate",
		"mol-orphan-scan",
		"mol-session-gc",
	}

	formulasDir := "formulas"
	for _, name := range dogFormulas {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(formulasDir, name+".formula.toml")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Skipf("Formula not found: %v", err)
			}

			f, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse failed: %v", err)
			}

			// All template variables must be defined
			if err := f.ValidateTemplateVariables(); err != nil {
				t.Errorf("Would fail wisp creation: %v", err)
			}

			// Must have at least one required input variable
			hasRequired := false
			for _, v := range f.Vars {
				if v.Required {
					hasRequired = true
					break
				}
			}
			if !hasRequired {
				t.Log("Warning: formula has no required input variables")
			}
		})
	}
}

func TestOrphanScanScopeDefaultsToTown(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("formulas", "mol-orphan-scan.formula.toml"))
	if err != nil {
		t.Fatalf("Read mol-orphan-scan formula: %v", err)
	}

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse mol-orphan-scan formula: %v", err)
	}

	scope, ok := f.Vars["scope"]
	if !ok {
		t.Fatal("Missing scope variable")
	}
	if scope.Required {
		t.Error("scope must be optional when it has a default")
	}
	if scope.Default != "town" {
		t.Errorf("scope default = %q, want town", scope.Default)
	}
}

// TestPolecatFormulas_CanBeWisped verifies that polecat formulas pass variable validation.
// These formulas are used when spawning polecats for code review and PR review tasks.
func TestPolecatFormulas_CanBeWisped(t *testing.T) {
	polecatFormulas := []struct {
		name         string
		requiredVars []string
	}{
		{
			name:         "mol-polecat-code-review",
			requiredVars: []string{"scope", "issue", "rig"},
		},
		{
			name:         "mol-polecat-review-pr",
			requiredVars: []string{"pr_url", "issue", "rig"},
		},
	}

	formulasDir := "formulas"
	for _, tc := range polecatFormulas {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(formulasDir, tc.name+".formula.toml")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Skipf("Formula not found: %v", err)
			}

			f, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse failed: %v", err)
			}

			// All template variables must be defined
			if err := f.ValidateTemplateVariables(); err != nil {
				t.Errorf("Would fail wisp creation: %v", err)
			}

			// Verify expected required variables exist
			for _, varName := range tc.requiredVars {
				v, ok := f.Vars[varName]
				if !ok {
					t.Errorf("Missing required variable %q", varName)
					continue
				}
				if !v.Required {
					t.Errorf("Variable %q should be marked required", varName)
				}
			}
		})
	}
}

// TestTownShutdownFormula_CanBeWisped verifies the town shutdown formula passes validation.
// This formula is used by Mayor to orchestrate full Gas Town shutdown/restart.
func TestTownShutdownFormula_CanBeWisped(t *testing.T) {
	formulaPath := filepath.Join("formulas", "mol-town-shutdown.formula.toml")
	data, err := os.ReadFile(formulaPath)
	if err != nil {
		t.Skipf("Formula file not found: %v", err)
	}

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Failed to parse mol-town-shutdown formula: %v", err)
	}

	// Verify it's a workflow formula
	if f.Type != TypeWorkflow {
		t.Errorf("Expected workflow type, got %s", f.Type)
	}

	// All template variables must be defined
	if err := f.ValidateTemplateVariables(); err != nil {
		t.Errorf("Would fail wisp creation: %v", err)
	}

	// shutdown_reason should be defined but not required (has a use case without it)
	if v, ok := f.Vars["shutdown_reason"]; !ok {
		t.Error("Missing 'shutdown_reason' variable")
	} else if v.Required {
		t.Error("'shutdown_reason' should not be required (optional parameter)")
	}
}
