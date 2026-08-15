package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildStartupCommandAppliesPiRoleEffort(t *testing.T) {
	tests := []struct {
		name      string
		baseArgs  []string
		effort    string
		want      string
		doNotWant string
	}{
		{
			name:     "adds the role effort as Pi thinking",
			baseArgs: []string{"--model", "openai-codex/gpt-5.6-luna"},
			effort:   "low",
			want:     "--model openai-codex/gpt-5.6-luna --thinking low",
		},
		{
			name:      "role effort overrides the profile default",
			baseArgs:  []string{"--model", "openai-codex/gpt-5.6-luna", "--thinking", "max"},
			effort:    "medium",
			want:      "--model openai-codex/gpt-5.6-luna --thinking medium",
			doNotWant: "--thinking max",
		},
		{
			name:     "does not duplicate an explicitly configured extension",
			baseArgs: []string{"-e", ".pi/extensions/gastown-hooks.js", "--model", "openai-codex/gpt-5.6-luna"},
			effort:   "high",
			want:     "--model openai-codex/gpt-5.6-luna --thinking high",
		},
		{
			name:     "replaces a trailing thinking flag with Pi-specific effort",
			baseArgs: []string{"--model", "openai-codex/gpt-5.6-luna", "--thinking"},
			effort:   "xhigh",
			want:     "--model openai-codex/gpt-5.6-luna --thinking xhigh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			townRoot := t.TempDir()
			fakeBin := t.TempDir()
			piPath := filepath.Join(fakeBin, "pi")
			if err := os.WriteFile(piPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatalf("write fake pi: %v", err)
			}
			t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

			settings := NewTownSettings()
			settings.Agents = map[string]*RuntimeConfig{
				"pi-luna": {Provider: "pi", Command: "pi", Args: tt.baseArgs},
			}
			settings.RoleAgents = map[string]string{"witness": "pi-luna"}
			settings.RoleEffort = map[string]string{"witness": tt.effort}
			if err := SaveTownSettings(TownSettingsPath(townRoot), settings); err != nil {
				t.Fatalf("save town settings: %v", err)
			}

			got := BuildStartupCommand(map[string]string{
				"GT_ROLE": "sample/witness",
				"GT_ROOT": townRoot,
			}, "", "")
			if !strings.Contains(got, tt.want) {
				t.Fatalf("startup command %q does not contain %q", got, tt.want)
			}
			if tt.doNotWant != "" && strings.Contains(got, tt.doNotWant) {
				t.Fatalf("startup command %q contains stale profile effort %q", got, tt.doNotWant)
			}
			if !strings.Contains(got, "-e .pi/extensions/gastown-hooks.js") {
				t.Fatalf("startup command %q does not load the Gas Town Pi extension", got)
			}
			if count := strings.Count(got, "-e .pi/extensions/gastown-hooks.js"); count != 1 {
				t.Fatalf("startup command %q loads the Gas Town Pi extension %d times, want once", got, count)
			}
			if !strings.Contains(got, "--approve") {
				t.Fatalf("startup command %q does not approve Gas Town's project-local Pi extension", got)
			}
		})
	}
}
