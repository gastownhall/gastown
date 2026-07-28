package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetRigLED(t *testing.T) {
	tests := []struct {
		name        string
		hasWitness  bool
		hasRefinery bool
		opState     string
		want        string
	}{
		// Operational state overrides session state (GH#2555)
		{"parked no sessions", false, false, "PARKED", "🅿️"},
		{"parked with sessions", true, true, "PARKED", "🅿️"},
		{"parked partial", true, false, "PARKED", "🅿️"},
		{"docked no sessions", false, false, "DOCKED", "🛑"},
		{"docked with sessions", true, true, "DOCKED", "🛑"},

		// Both running - fully active
		{"both running", true, true, "OPERATIONAL", "🟢"},

		// One running - partially active
		{"witness only", true, false, "OPERATIONAL", "🟡"},
		{"refinery only", false, true, "OPERATIONAL", "🟡"},

		// Nothing running
		{"stopped operational", false, false, "OPERATIONAL", "⚫"},
		{"stopped empty state", false, false, "", "⚫"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetRigLED(tt.hasWitness, tt.hasRefinery, tt.opState)
			if got != tt.want {
				t.Errorf("GetRigLED(%v, %v, %q) = %q, want %q",
					tt.hasWitness, tt.hasRefinery, tt.opState, got, tt.want)
			}
		})
	}
}

func TestRigRepoPath(t *testing.T) {
	// rigRepoPath resolves the mayor clone for a rig, which is what
	// `gt rig list --json` exposes as repo_path. It must return "" rather
	// than a bogus path when there is no usable clone, because consumers
	// (e.g. the git-hygiene plugin) treat a non-empty value as a git repo.
	tests := []struct {
		name  string
		setup func(t *testing.T, rigPath string)
		want  bool // true = expect the mayor/rig path, false = expect ""
	}{
		{
			name:  "no mayor dir at all",
			setup: func(t *testing.T, rigPath string) {},
			want:  false,
		},
		{
			name: "mayor/rig exists but is not a git repo",
			setup: func(t *testing.T, rigPath string) {
				if err := os.MkdirAll(filepath.Join(rigPath, "mayor", "rig"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: false,
		},
		{
			name: "mayor/rig is a normal clone (.git dir)",
			setup: func(t *testing.T, rigPath string) {
				if err := os.MkdirAll(filepath.Join(rigPath, "mayor", "rig", ".git"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: true,
		},
		{
			name: "mayor/rig is a worktree (.git file)",
			setup: func(t *testing.T, rigPath string) {
				dir := filepath.Join(rigPath, "mayor", "rig")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rigPath := t.TempDir()
			tt.setup(t, rigPath)

			got := rigRepoPath(rigPath)

			want := ""
			if tt.want {
				want = filepath.Join(rigPath, "mayor", "rig")
			}
			if got != want {
				t.Errorf("rigRepoPath(%q) = %q, want %q", rigPath, got, want)
			}
		})
	}
}
