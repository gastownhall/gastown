package cmd

import (
	"strings"
	"testing"
)

func TestValidateTarget(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		wantErr bool
		errMsg  string // substring that must appear in error
	}{
		// Valid targets
		{name: "empty target", target: "", wantErr: false},
		{name: "self target", target: ".", wantErr: false},
		{name: "bare rig name", target: "gastown", wantErr: false},
		{name: "role shortcut mayor", target: "mayor", wantErr: false},
		{name: "role shortcut deacon", target: "deacon", wantErr: false},
		{name: "rig/polecats/name", target: "gastown/polecats/nux", wantErr: false},
		{name: "rig/crew/name", target: "gastown/crew/burke", wantErr: false},
		{name: "rig/witness", target: "gastown/witness", wantErr: false},
		{name: "rig/refinery", target: "gastown/refinery", wantErr: false},
		{name: "deacon/dogs", target: "deacon/dogs", wantErr: false},
		{name: "deacon/dogs/name", target: "deacon/dogs/rex", wantErr: false},
		{name: "polecat shorthand", target: "gastown/nux", wantErr: false},
		{name: "crew shorthand", target: "gastown/max", wantErr: false},

		// Invalid targets — empty segments
		{name: "trailing slash", target: "gastown/", wantErr: true, errMsg: "empty path segment"},
		{name: "double slash", target: "gastown//polecats", wantErr: true, errMsg: "empty path segment"},
		{name: "leading slash", target: "/polecats", wantErr: true, errMsg: "empty path segment"},

		// Invalid targets — unknown role (only rejected with 3+ segments)
		{name: "unknown role 3-seg", target: "gastown/badrole/name", wantErr: true, errMsg: "unknown role"},
		{name: "typo in role 3-seg", target: "gastown/polecat/name", wantErr: true, errMsg: "unknown role"},

		// Invalid targets — missing name
		{name: "crew no name", target: "gastown/crew", wantErr: true, errMsg: "requires a worker name"},
		{name: "polecats no name", target: "gastown/polecats", wantErr: true, errMsg: "requires a polecat name"},

		// Invalid targets — witness/refinery with sub-agents
		{name: "witness with name", target: "gastown/witness/extra", wantErr: true, errMsg: "does not have named sub-agents"},
		{name: "refinery with name", target: "gastown/refinery/extra", wantErr: true, errMsg: "does not have named sub-agents"},

		// Invalid targets — too many segments
		{name: "too many segments", target: "gastown/crew/burke/extra", wantErr: true, errMsg: "too many path segments"},

		// Invalid targets — mayor sub-paths
		{name: "mayor sub-agent", target: "mayor/something", wantErr: true, errMsg: "does not have sub-agents"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTarget(tc.target)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateTarget(%q) = nil, want error containing %q", tc.target, tc.errMsg)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateTarget(%q) = %v, want nil", tc.target, err)
			}
			if tc.wantErr && err != nil && tc.errMsg != "" {
				if !strings.Contains(err.Error(), tc.errMsg) {
					t.Fatalf("ValidateTarget(%q) error = %q, want it to contain %q", tc.target, err.Error(), tc.errMsg)
				}
			}
		})
	}
}

func TestValidateFormulaFamily(t *testing.T) {
	tests := []struct {
		name        string
		formulaName string
		targetAgent string
		wantErr     bool
		errMsg      string // substring that must appear in error
	}{
		// No formula to validate
		{name: "empty formula", formulaName: "", targetAgent: "gastown/polecats/Toast", wantErr: false},
		{name: "empty formula with dog target", formulaName: "", targetAgent: "deacon/dogs/alpha", wantErr: false},

		// Non-polecat-specific formulas are always valid
		{name: "mol-dog-backup to dog", formulaName: "mol-dog-backup", targetAgent: "deacon/dogs/alpha", wantErr: false},
		{name: "mol-dog-backup to polecat", formulaName: "mol-dog-backup", targetAgent: "gastown/polecats/Toast", wantErr: false},
		{name: "mol-dog-doctor to mayor", formulaName: "mol-dog-doctor", targetAgent: "mayor", wantErr: false},
		{name: "custom formula to polecat", formulaName: "my-custom-formula", targetAgent: "gastown/polecats/Toast", wantErr: false},
		{name: "custom formula to dog", formulaName: "my-custom-formula", targetAgent: "deacon/dogs/alpha", wantErr: false},
		{name: "custom formula to crew", formulaName: "my-custom-formula", targetAgent: "gastown/crew/burke", wantErr: false},

		// Valid mol-polecat-* formulas (only not allowed on dogs)
		{name: "mol-polecat-work to polecat", formulaName: "mol-polecat-work", targetAgent: "gastown/polecats/Toast", wantErr: false},
		{name: "mol-polecat-work to crew", formulaName: "mol-polecat-work", targetAgent: "gastown/crew/burke", wantErr: false},
		{name: "mol-polecat-work to mayor", formulaName: "mol-polecat-work", targetAgent: "mayor", wantErr: false},
		{name: "mol-polecat-review to polecat greenplace", formulaName: "mol-polecat-review", targetAgent: "greenplace/polecats/Nux", wantErr: false},

		// Invalid: mol-polecat-* to dog (the specific constraint)
		{name: "mol-polecat-work to dog", formulaName: "mol-polecat-work", targetAgent: "deacon/dogs/alpha", wantErr: true, errMsg: "designed for polecats"},
		{name: "mol-polecat-review to dog", formulaName: "mol-polecat-review", targetAgent: "deacon/dogs/beta", wantErr: true, errMsg: "designed for polecats"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFormulaFamily(tc.formulaName, tc.targetAgent)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateFormulaFamily(%q, %q) = nil, want error containing %q", tc.formulaName, tc.targetAgent, tc.errMsg)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateFormulaFamily(%q, %q) = %v, want nil", tc.formulaName, tc.targetAgent, err)
			}
			if tc.wantErr && err != nil && tc.errMsg != "" {
				if !strings.Contains(err.Error(), tc.errMsg) {
					t.Fatalf("ValidateFormulaFamily(%q, %q) error = %q, want it to contain %q", tc.formulaName, tc.targetAgent, err.Error(), tc.errMsg)
				}
			}
		})
	}
}
