package cmd

import (
	"testing"

	"github.com/jonbaldie/gastown/internal/session"
)

func TestLooksLikeIssueID(t *testing.T) {
	originalRegistry := session.DefaultRegistry()
	t.Cleanup(func() { session.SetDefaultRegistry(originalRegistry) })

	testRegistry := session.NewPrefixRegistry()
	testRegistry.Register("nx", "nexus")
	testRegistry.Register("rpk", "nrpk")
	testRegistry.Register("longpfx", "longprefix")
	session.SetDefaultRegistry(testRegistry)

	tests := []struct {
		input string
		want  bool
	}{
		{"gt-abc123", true},
		{"bd-xyz789", true},
		{"hq-mayor", true},
		{"nx-def456", true},
		{"rpk-ghi012", true},
		{"longpfx-jkl345", true},
		{"nv-short", true},
		{"ab-min", true},
		{"abc-max3", true},     // 3-char prefix matches heuristic
		{"abcd-four", false},   // 4-char unregistered prefix: not matched by heuristic
		{"abcde-five", false},  // 5-char prefix exceeds heuristic limit
		{"abcdef-max6", false}, // 6-char prefix exceeds heuristic limit
		{"test-plan", false},   // 4-char common word: not a false-positive
		{"gthq-deacon", true},  // legacy gthq prefix via HasKnownPrefix
		{"notvalid", false},
		{"no-hyphen-after", true}, // "no" is a 2-char lowercase prefix
		{"alpha-release", false},  // 5-char word: not a false-positive
		{"deploy-backend", false}, // 6-char word: not a false-positive
		{"A-uppercase", false},
		{"1-number", false},
		{"", false},
		{"-noprefix", false},
		{"a-tooshort", false},
		{"abcdefg-toolong", false},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := looksLikeIssueID(tc.input)
			if got != tc.want {
				t.Errorf("looksLikeIssueID(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}
