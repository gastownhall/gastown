package cmd

import "testing"

func TestIsBeadID(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		// Valid bead IDs - all known prefixes
		{"gt-abc123", true},
		{"bd-xyz", true},
		{"hq-foo", true},
		{"mol-bar", true},
		{"wisp-baz", true},
		{"dolt-qux", true},
		{"sky-abc", true},
		{"wy-def", true},
		// IDs may contain hyphens after the routing prefix.
		{"hq-cv-foo", true},
		// Rig-derived prefixes may contain underscores.
		{"japanese_reader-id3a", true},
		{"mcp_fit-92f", true},
		// Invalid inputs
		{"", false},
		{"-abc", false},
		{"abc", false},
		{"ABC-123", false},
		{"my_Rig-abc", false},
		{"123-abc", false},
		{"_rig-abc", false},
		{"gt-", false},
		{"-", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isBeadID(tt.input)
			if got != tt.want {
				t.Errorf("isBeadID(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
