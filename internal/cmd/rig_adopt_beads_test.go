package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRigAdoptBeadsCandidateDetection verifies the .beads/ candidate detection
// logic used by runRigAdopt to decide whether to initialize a fresh database.
func TestRigAdoptBeadsCandidateDetection(t *testing.T) {
	tests := []struct {
		name           string
		setupDirs      []string // directories to create under rigPath
		wantFoundBeads bool     // whether any candidate should be found
	}{
		{
			name:           "no beads directory exists",
			setupDirs:      nil,
			wantFoundBeads: false,
		},
		{
			name:           "rig-level .beads exists",
			setupDirs:      []string{".beads"},
			wantFoundBeads: true,
		},
		{
			name:           "mayor/rig/.beads exists (tracked beads)",
			setupDirs:      []string{"mayor/rig/.beads"},
			wantFoundBeads: true,
		},
		{
			name:           "both candidates exist",
			setupDirs:      []string{".beads", "mayor/rig/.beads"},
			wantFoundBeads: true,
		},
		{
			name:           "unrelated directories dont count",
			setupDirs:      []string{"src", "docs", "mayor"},
			wantFoundBeads: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rigPath := t.TempDir()

			// Set up test directories
			for _, dir := range tt.setupDirs {
				if err := os.MkdirAll(filepath.Join(rigPath, dir), 0755); err != nil {
					t.Fatalf("creating dir %q: %v", dir, err)
				}
			}

			found := hasRigBeadsCandidate(rigPath)

			if found != tt.wantFoundBeads {
				t.Errorf("beads candidate found = %v, want %v", found, tt.wantFoundBeads)
			}
		})
	}
}

// TestRigAdoptFallbackInitNeeded verifies that when no .beads/ candidate exists
// and a prefix is available, the fallback init path is triggered.
func TestRigAdoptFallbackInitNeeded(t *testing.T) {
	tests := []struct {
		name         string
		hasDotBeads  bool
		hasPrefix    bool
		wantFallback bool
	}{
		{
			name:         "no beads + has prefix → needs fallback",
			hasDotBeads:  false,
			hasPrefix:    true,
			wantFallback: true,
		},
		{
			name:         "no beads + no prefix → skip fallback",
			hasDotBeads:  false,
			hasPrefix:    false,
			wantFallback: false,
		},
		{
			name:         "has beads + has prefix → no fallback needed",
			hasDotBeads:  true,
			hasPrefix:    true,
			wantFallback: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beadsPrefix := ""
			if tt.hasPrefix {
				beadsPrefix = "test"
			}

			needsFallback := shouldInitializeAdoptedRigBeads(tt.hasDotBeads, beadsPrefix)

			if needsFallback != tt.wantFallback {
				t.Errorf("needsFallback = %v, want %v (foundBeads=%v, prefix=%q)",
					needsFallback, tt.wantFallback, tt.hasDotBeads, beadsPrefix)
			}
		})
	}
}
