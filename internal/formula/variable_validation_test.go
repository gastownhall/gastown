package formula

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/constants"
)

// TestExtractTemplateVariables verifies we can find all {{variable}} patterns.
func TestExtractTemplateVariables(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected []string
	}{
		{
			name:     "single variable",
			text:     "Hello {{name}}!",
			expected: []string{"name"},
		},
		{
			name:     "multiple variables",
			text:     "{{greeting}} {{name}}, you have {{count}} messages",
			expected: []string{"count", "greeting", "name"}, // sorted alphabetically
		},
		{
			name:     "no variables",
			text:     "Hello world!",
			expected: []string{},
		},
		{
			name:     "duplicate variables",
			text:     "{{name}} and {{name}} again",
			expected: []string{"name"}, // should dedupe
		},
		{
			name:     "handlebars helpers ignored",
			text:     "{{#if condition}}{{value}}{{/if}}",
			expected: []string{"value"}, // #if, /if are helpers, not variables
		},
		{
			name:     "each helper ignored",
			text:     "{{#each items}}{{item}}{{/each}}",
			expected: []string{"item"}, // #each, /each are helpers
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractTemplateVariables(tc.text)
			if len(got) != len(tc.expected) {
				t.Errorf("ExtractTemplateVariables(%q) = %v, want %v", tc.text, got, tc.expected)
				return
			}
			for i, v := range tc.expected {
				if got[i] != v {
					t.Errorf("ExtractTemplateVariables(%q)[%d] = %q, want %q", tc.text, i, got[i], v)
				}
			}
		})
	}
}

// TestValidateTemplateVariables verifies that undefined variables are caught.
func TestValidateTemplateVariables(t *testing.T) {
	tests := []struct {
		name      string
		formula   string
		wantError bool
		errorMsg  string
	}{
		{
			name: "all variables defined",
			formula: `
formula = "test"
type = "workflow"
version = 1

[[steps]]
id = "step1"
title = "Work on {{issue}}"
description = "Process {{issue}}"

[vars.issue]
description = "The issue ID"
required = true
`,
			wantError: false,
		},
		{
			name: "undefined variable",
			formula: `
formula = "test"
type = "workflow"
version = 1

[[steps]]
id = "step1"
title = "Count: {{ready_count}}"
description = "Process {{issue}}"

[vars.issue]
description = "The issue ID"
required = true
`,
			wantError: true,
			errorMsg:  "ready_count",
		},
		{
			name: "variable with default is ok",
			formula: `
formula = "test"
type = "workflow"
version = 1

[[steps]]
id = "step1"
title = "Count: {{ready_count}}"

[vars.ready_count]
description = "Computed count"
default = ""
`,
			wantError: false,
		},
		{
			name: "multiple undefined variables",
			formula: `
formula = "test"
type = "workflow"
version = 1

[[steps]]
id = "step1"
title = "{{a}} {{b}} {{c}}"

[vars.a]
description = "Defined"
required = true
`,
			wantError: true,
			errorMsg:  "b", // Should mention at least one undefined
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := Parse([]byte(tc.formula))
			if err != nil {
				t.Fatalf("Parse failed: %v", err)
			}

			err = f.ValidateTemplateVariables()
			if tc.wantError {
				if err == nil {
					t.Errorf("ValidateTemplateVariables() = nil, want error containing %q", tc.errorMsg)
				} else if !strings.Contains(err.Error(), tc.errorMsg) {
					t.Errorf("ValidateTemplateVariables() = %v, want error containing %q", err, tc.errorMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateTemplateVariables() = %v, want nil", err)
				}
			}
		})
	}
}

// TestMolConvoyFeedFormula_VariableValidation is a regression test for issue #1133.
// The mol-convoy-feed formula uses template variables like {{ready_count}} that
// aren't defined in [vars], causing wisp creation to fail.
func TestMolConvoyFeedFormula_VariableValidation(t *testing.T) {
	// Find the formula file
	formulaPath := filepath.Join("formulas", constants.MolConvoyFeed+".formula.toml")
	data, err := os.ReadFile(formulaPath)
	if err != nil {
		t.Skipf("Formula file not found: %v", err)
	}

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Failed to parse mol-convoy-feed formula: %v", err)
	}

	// This test will FAIL until the formula is fixed
	err = f.ValidateTemplateVariables()
	if err != nil {
		t.Errorf("mol-convoy-feed formula has undefined template variables: %v", err)
		t.Log("Fix: Add all computed variables to [vars] with default = \"\"")
	}
}

func TestMolDogReaperFormula_DoesNotForceDoltPort(t *testing.T) {
	formulaPath := filepath.Join("formulas", constants.MolDogReaper+".formula.toml")
	data, err := os.ReadFile(formulaPath)
	if err != nil {
		t.Skipf("Formula file not found: %v", err)
	}
	text := string(data)
	for _, forbidden := range []string{
		"--port={{dolt_port}}",
		"default = \"3307\"",
		"default 3307",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("mol-dog-reaper formula should inherit GT_DOLT_PORT, found %q", forbidden)
		}
	}
}

// TestAllEmbeddedFormulas_VariableValidation ensures no embedded formula
// has undefined template variables. This prevents future regressions.
func TestAllEmbeddedFormulas_VariableValidation(t *testing.T) {
	formulasDir := "formulas"
	entries, err := os.ReadDir(formulasDir)
	if err != nil {
		t.Skipf("Formulas directory not found: %v", err)
	}

	var failures []string
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".formula.toml") {
			continue
		}

		path := filepath.Join(formulasDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("Failed to read %s: %v", entry.Name(), err)
			continue
		}

		f, err := Parse(data)
		if err != nil {
			// Skip formulas that don't parse (may have other issues)
			continue
		}

		if err := f.ValidateTemplateVariables(); err != nil {
			failures = append(failures, entry.Name()+": "+err.Error())
		}
	}

	if len(failures) > 0 {
		t.Errorf("Formulas with undefined template variables:\n%s", strings.Join(failures, "\n"))
	}
}
