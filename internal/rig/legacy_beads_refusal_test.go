package rig

import (
	"strings"
	"testing"
)

// bdLegacyRefusalOutput is the verbatim message bd prints when its
// legacy-upgrade guard classifies the workspace as belonging to an older
// storage era (beads cmd/bd/legacy_upgrade_guard.go). Rig init must treat this
// as fatal, not as a skippable warning.
const bdLegacyRefusalOutput = "Error: legacy Dolt server workspace detected; " +
	"explicit migration is required before this bd version can open or modify " +
	"the workspace. Preserve .beads unchanged and follow " +
	"docs/getting-started/upgrading.md#cross-era-upgrades"

func TestIsLegacyBeadsRefusal(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "legacy dolt server workspace",
			output: bdLegacyRefusalOutput,
			want:   true,
		},
		{
			name:   "historical sqlite workspace",
			output: "Error: historical SQLite workspace detected; explicit migration is required before this bd version can open or modify the workspace.",
			want:   true,
		},
		{
			name:   "versioned legacy workspace",
			output: "Error: legacy Dolt server workspace from bd 0.58.0 detected; explicit migration is required before this bd version can open or modify the workspace.",
			want:   true,
		},
		{
			name:   "bd not installed",
			output: `exec: "bd": executable file not found in $PATH`,
			want:   false,
		},
		{
			name:   "already initialized",
			output: "Error: beads workspace already initialized (use --force to reinitialize)",
			want:   false,
		},
		{
			name:   "empty output",
			output: "",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLegacyBeadsRefusal(tt.output); got != tt.want {
				t.Errorf("isLegacyBeadsRefusal(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

func TestLegacyBeadsRefusalErrorIsActionable(t *testing.T) {
	err := legacyBeadsRefusalError("customer_code_miner", bdLegacyRefusalOutput)
	if err == nil {
		t.Fatal("legacyBeadsRefusalError returned nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"customer_code_miner",
		"explicit migration is required",
		"go install github.com/steveyegge/gastown/cmd/gt@latest",
		"gt rig remove customer_code_miner --force",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q:\n%s", want, msg)
		}
	}
}
