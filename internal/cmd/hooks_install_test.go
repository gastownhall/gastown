package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/constants"
)

func TestDetermineTargets_AllRigsSkipsTownEventsDir(t *testing.T) {
	tests := []struct {
		name     string
		rigs     []string
		wantRigs []string
	}{
		{"single rig", []string{"myrig"}, []string{"myrig"}},
		{"rig with confusable name", []string{"myrig", "events2"}, []string{"myrig", "events2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			townRoot := t.TempDir()
			for _, rig := range tt.rigs {
				if err := os.MkdirAll(filepath.Join(townRoot, rig, "crew"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, channel := range []string{"witness", "refinery", "crew"} {
				if err := os.MkdirAll(filepath.Join(townRoot, "events", channel), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			targets, err := determineTargets(townRoot, constants.RoleCrew, true, []string{constants.RoleCrew})
			if err != nil {
				t.Fatalf("determineTargets failed: %v", err)
			}

			if len(targets) != len(tt.wantRigs) {
				t.Errorf("expected %d targets, got %v", len(tt.wantRigs), targets)
			}
			for _, target := range targets {
				rel, _ := filepath.Rel(townRoot, target)
				if strings.HasPrefix(filepath.ToSlash(rel), "events/") {
					t.Errorf("target under events dir: %s", target)
				}
			}
		})
	}
}
