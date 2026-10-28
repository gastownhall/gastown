package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTownWithEventChannels builds a town with the given real rigs plus the
// native event channel directories (events/witness, events/refinery), which
// look like a rig to a naive directory scan.
func newTownWithEventChannels(t *testing.T, rigNames ...string) string {
	t.Helper()
	townRoot := t.TempDir()
	t.Setenv("HOME", townRoot)

	for _, name := range rigNames {
		for _, sub := range []string{"crew", "witness", "refinery", "polecats"} {
			if err := os.MkdirAll(filepath.Join(townRoot, name, sub), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, channel := range []string{"witness", "refinery"} {
		dir := filepath.Join(townRoot, "events", channel)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "1700000000000000000-1.event"), []byte(`{"type":"MERGE_READY"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return townRoot
}

func mentionsEventsDir(s string) bool {
	s = filepath.ToSlash(s)
	return s == "events" || strings.HasPrefix(s, "events/") || strings.Contains(s, "/events/")
}

func TestFindAllRigs_SkipsTownEventsDir(t *testing.T) {
	tests := []struct {
		name string
		rigs []string
	}{
		{"single rig", []string{"myrig"}},
		{"rig with confusable name", []string{"myrig", "events2", "my-events", "eventsrig"}},
		{"no real rigs", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			townRoot := newTownWithEventChannels(t, tt.rigs...)

			got := map[string]bool{}
			for _, rig := range findAllRigs(townRoot) {
				got[filepath.Base(rig)] = true
			}
			if got["events"] {
				t.Errorf("findAllRigs returned the events channel directory as a rig: %v", got)
			}
			for _, want := range tt.rigs {
				if !got[want] {
					t.Errorf("findAllRigs missing real rig %q: %v", want, got)
				}
			}
			if len(got) != len(tt.rigs) {
				t.Errorf("expected %d rigs, got %v", len(tt.rigs), got)
			}
		})
	}
}

func TestClaudeSettingsCheck_IgnoresTownEventsDir(t *testing.T) {
	tests := []struct {
		name string
		rigs []string
	}{
		{"single rig", []string{"myrig"}},
		{"rig with confusable name", []string{"myrig", "events2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			townRoot := newTownWithEventChannels(t, tt.rigs...)

			check := NewClaudeSettingsCheck()
			files := check.findSettingsFiles(townRoot)

			rigsSeen := map[string]bool{}
			for _, f := range files {
				if f.rigName == "events" || mentionsEventsDir(relTo(townRoot, f.path)) {
					t.Errorf("claude-settings considered events dir: %s", f.path)
				}
				if f.rigName != "" {
					rigsSeen[f.rigName] = true
				}
			}
			for _, want := range tt.rigs {
				if !rigsSeen[want] {
					t.Errorf("claude-settings did not check real rig %q", want)
				}
			}

			result := check.Run(&CheckContext{TownRoot: townRoot})
			for _, d := range result.Details {
				if strings.Contains(d, "events/") {
					t.Errorf("unexpected detail mentioning events dir: %s", d)
				}
			}
		})
	}
}

func TestSettingsCheck_IgnoresTownEventsDir(t *testing.T) {
	townRoot := newTownWithEventChannels(t, "myrig", "events2")

	result := NewSettingsCheck().Run(&CheckContext{TownRoot: townRoot})

	for _, d := range result.Details {
		if mentionsEventsDir(d) {
			t.Errorf("rig-settings reported events dir: %s", d)
		}
	}
	// Both real rigs lack settings/, the events dir must not add a third.
	if result.Status != StatusWarning || len(result.Details) != 2 {
		t.Errorf("expected warning for exactly the 2 real rigs, got %v %q %v", result.Status, result.Message, result.Details)
	}
}

func TestLandWorktreeGitignoreCheck_IgnoresTownEventsDir(t *testing.T) {
	townRoot := newTownWithEventChannels(t)
	createRigWithGitignore(t, townRoot, "myrig", ".land-worktree/\n")

	result := NewLandWorktreeGitignoreCheck().Run(&CheckContext{TownRoot: townRoot})

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK, got %v: %s %v", result.Status, result.Message, result.Details)
	}

	// A real rig missing the entry is still reported, and only that rig.
	createRigWithGitignore(t, townRoot, "events2", "plugins/\n")
	result = NewLandWorktreeGitignoreCheck().Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusWarning || len(result.Details) != 1 || mentionsEventsDir(result.Details[0]) {
		t.Errorf("expected exactly one warning for events2, got %v %v", result.Status, result.Details)
	}
}

func TestHooksSyncCheck_IgnoresTownEventsDir(t *testing.T) {
	townRoot := newTownWithEventChannels(t, "myrig")

	result := NewHooksSyncCheck().Run(&CheckContext{TownRoot: townRoot})

	for _, d := range result.Details {
		if mentionsEventsDir(d) {
			t.Errorf("hooks-sync reported events dir: %s", d)
		}
	}
	if strings.Contains(result.Message, "events") {
		t.Errorf("hooks-sync message mentions events: %s", result.Message)
	}
}

func relTo(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}
