package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListExistingRigBeadsDirs(t *testing.T) {
	tests := []struct {
		name      string
		setupDirs []string
		wantRel   []string
	}{
		{
			name:      "no beads directory exists",
			setupDirs: nil,
		},
		{
			name:      "rig-level .beads exists",
			setupDirs: []string{".beads"},
			wantRel:   []string{".beads"},
		},
		{
			name:      "mayor/rig/.beads exists (tracked beads)",
			setupDirs: []string{"mayor/rig/.beads"},
			wantRel:   []string{filepath.Join("mayor", "rig", ".beads")},
		},
		{
			name:      "both candidates exist prefers rig-level first",
			setupDirs: []string{".beads", "mayor/rig/.beads"},
			wantRel:   []string{".beads", filepath.Join("mayor", "rig", ".beads")},
		},
		{
			name:      "unrelated directories dont count",
			setupDirs: []string{"src", "docs", "mayor"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rigPath := t.TempDir()
			for _, dir := range tt.setupDirs {
				if err := os.MkdirAll(filepath.Join(rigPath, dir), 0755); err != nil {
					t.Fatalf("creating dir %q: %v", dir, err)
				}
			}

			got := listExistingRigBeadsDirs(rigPath)
			if len(got) != len(tt.wantRel) {
				t.Fatalf("listExistingRigBeadsDirs = %v, want %d entries", got, len(tt.wantRel))
			}
			for i, rel := range tt.wantRel {
				want := filepath.Join(rigPath, rel)
				if got[i] != want {
					t.Errorf("existing[%d] = %q, want %q", i, got[i], want)
				}
			}
		})
	}
}

func TestAdoptedRigBeadsPlanUsesSingleSnapshot(t *testing.T) {
	tests := []struct {
		name          string
		existing      []string
		prefix        string
		wantHandle    string
		wantInitFresh bool
	}{
		{
			name:          "empty snapshot with prefix initializes once",
			existing:      nil,
			prefix:        "gt",
			wantHandle:    "",
			wantInitFresh: true,
		},
		{
			name:          "empty snapshot without prefix skips init",
			existing:      nil,
			prefix:        "",
			wantHandle:    "",
			wantInitFresh: false,
		},
		{
			name:          "existing candidate is handled and not initialized again",
			existing:      []string{"/rig/.beads"},
			prefix:        "gt",
			wantHandle:    "/rig/.beads",
			wantInitFresh: false,
		},
		{
			name:          "first snapshot entry is the only handled dir",
			existing:      []string{"/rig/.beads", "/rig/mayor/rig/.beads"},
			prefix:        "gt",
			wantHandle:    "/rig/.beads",
			wantInitFresh: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotHandle, gotInit := adoptedRigBeadsPlan(tt.existing, tt.prefix)
			if gotHandle != tt.wantHandle || gotInit != tt.wantInitFresh {
				t.Fatalf("adoptedRigBeadsPlan(%v, %q) = (%q, %v), want (%q, %v)",
					tt.existing, tt.prefix, gotHandle, gotInit, tt.wantHandle, tt.wantInitFresh)
			}
			if gotHandle != "" && gotInit {
				t.Fatal("a single snapshot must not both handle a candidate and initialize a fresh database")
			}
		})
	}
}
