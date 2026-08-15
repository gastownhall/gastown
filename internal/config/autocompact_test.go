package config

import (
	"strconv"
	"strings"
	"testing"
)

func TestResolveAutoCompactWindow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		cap          int
		modelDefault int
		want         int
	}{
		{
			name:         "uses the 150k cap when the model window is larger",
			cap:          150_000,
			modelDefault: 200_000,
			want:         150_000,
		},
		{
			name:         "uses the model window when it is smaller than the cap",
			cap:          150_000,
			modelDefault: 128_000,
			want:         128_000,
		},
		{
			name:         "uses the cap when the model window is unknown",
			cap:          150_000,
			modelDefault: 0,
			want:         150_000,
		},
		{
			name:         "falls back to the default cap when the configured cap is missing",
			cap:          0,
			modelDefault: 200_000,
			want:         150_000,
		},
		{
			name:         "falls back to the default cap when the configured cap is negative",
			cap:          -1,
			modelDefault: 200_000,
			want:         150_000,
		},
		{
			name:         "uses a custom cap when it is below the model window",
			cap:          80_000,
			modelDefault: 200_000,
			want:         80_000,
		},
		{
			name:         "equal cap and model window returns that value",
			cap:          128_000,
			modelDefault: 128_000,
			want:         128_000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ResolveAutoCompactWindow(tt.cap, tt.modelDefault)
			if got != tt.want {
				t.Fatalf("ResolveAutoCompactWindow(%d, %d) = %d, want %d", tt.cap, tt.modelDefault, got, tt.want)
			}
		})
	}
}

func TestParseTokenCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		want    int
		wantOK  bool
	}{
		{in: "150000", want: 150_000, wantOK: true},
		{in: "150k", want: 150_000, wantOK: true},
		{in: "150K", want: 150_000, wantOK: true},
		{in: "128k", want: 128_000, wantOK: true},
		{in: " 80k ", want: 80_000, wantOK: true},
		{in: "0", wantOK: false},
		{in: "-1", wantOK: false},
		{in: "tokens", wantOK: false},
		{in: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, ok := ParseTokenCount(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("ParseTokenCount(%q) ok = %v, want %v", tt.in, ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Fatalf("ParseTokenCount(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestResolveAutoCompactCap(t *testing.T) {
	t.Parallel()

	t.Run("defaults to 150k", func(t *testing.T) {
		t.Parallel()
		if got := ResolveAutoCompactCap("", ""); got != 150_000 {
			t.Fatalf("ResolveAutoCompactCap() = %d, want 150000", got)
		}
	})

	t.Run("reads town settings", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		settings := NewTownSettings()
		settings.AutoCompactWindow = 80_000
		if err := SaveTownSettings(TownSettingsPath(townRoot), settings); err != nil {
			t.Fatalf("SaveTownSettings: %v", err)
		}
		if got := ResolveAutoCompactCap(townRoot, ""); got != 80_000 {
			t.Fatalf("ResolveAutoCompactCap() = %d, want 80000", got)
		}
	})

	t.Run("environment overrides town settings", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		settings := NewTownSettings()
		settings.AutoCompactWindow = 80_000
		if err := SaveTownSettings(TownSettingsPath(townRoot), settings); err != nil {
			t.Fatalf("SaveTownSettings: %v", err)
		}
		if got := ResolveAutoCompactCap(townRoot, "200k"); got != 200_000 {
			t.Fatalf("ResolveAutoCompactCap() = %d, want 200000", got)
		}
	})
}

func TestModelDefaultContextWindow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model string
		want  int
	}{
		{model: "sonnet", want: 200_000},
		{model: "sonnet[1m]", want: 1_000_000},
		{model: "opus", want: 200_000},
		{model: "opus[1m]", want: 1_000_000},
		{model: "haiku", want: 200_000},
		{model: "claude-sonnet-4-6", want: 200_000},
		{model: "gpt-4o", want: 128_000},
		{model: "gpt-4.1", want: 0},
		{model: "unknown-local-model", want: 0},
		{model: "", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			t.Parallel()
			if got := ModelDefaultContextWindow(tt.model); got != tt.want {
				t.Fatalf("ModelDefaultContextWindow(%q) = %d, want %d", tt.model, got, tt.want)
			}
		})
	}
}

func TestApplyAutoCompactWindowToAllAgentTypes(t *testing.T) {
	t.Parallel()

	if len(builtinPresets) == 0 {
		t.Fatal("builtinPresets is empty")
	}

	for name, preset := range builtinPresets {
		t.Run(string(name), func(t *testing.T) {
			t.Parallel()
			rc := runtimeConfigFromAgentInfo(name, preset)
			ApplyAutoCompactWindow(rc, 150_000)

			if rc.Env == nil {
				t.Fatal("Env is nil after applying auto-compact window")
			}
			if got := rc.Env[AutoCompactWindowEnv]; got != "150000" {
				t.Fatalf("GT_AUTO_COMPACT_WINDOW = %q, want 150000", got)
			}
			if preset.AutoCompactEnv != "" {
				if got := rc.Env[preset.AutoCompactEnv]; got != "150000" {
					t.Fatalf("%s = %q, want 150000", preset.AutoCompactEnv, got)
				}
			}
			if preset.AutoCompactFlag != "" {
				if !hasFlagValue(rc.Args, preset.AutoCompactFlag, "150000") {
					t.Fatalf("args %v missing %s 150000", rc.Args, preset.AutoCompactFlag)
				}
			}
		})
	}
}

func TestBuildStartupCommandAppliesAutoCompactWindow(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	if err := SaveTownSettings(TownSettingsPath(townRoot), NewTownSettings()); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	for _, name := range []string{"claude", "gemini", "codex", "cursor", "pi", "opencode", "copilot"} {
		t.Run(name, func(t *testing.T) {
			cmd, err := BuildStartupCommandWithAgentOverride(map[string]string{
				"GT_ROLE": "mayor",
				"GT_ROOT": townRoot,
			}, "", "", name)
			if err != nil {
				t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
			}
			if !strings.Contains(cmd, AutoCompactWindowEnv+"=150000") {
				t.Fatalf("startup command for %s missing %s=150000: %s", name, AutoCompactWindowEnv, cmd)
			}
			if name == "claude" && !strings.Contains(cmd, "--autocompact 150000") {
				t.Fatalf("claude startup command missing --autocompact 150000: %s", cmd)
			}
		})
	}
}

func TestBuildStartupCommandClampsToModelWindow(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	settings := NewTownSettings()
	settings.Agents = map[string]*RuntimeConfig{
		"small-claude": {
			Provider: "claude",
			Command:  "claude",
			Args:     []string{"--dangerously-skip-permissions", "--model", "gpt-4o"},
		},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), settings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	cmd, err := BuildStartupCommandWithAgentOverride(map[string]string{
		"GT_ROLE": "mayor",
		"GT_ROOT": townRoot,
	}, "", "", "small-claude")
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}
	if !strings.Contains(cmd, AutoCompactWindowEnv+"=128000") {
		t.Fatalf("expected clamp to 128000, got: %s", cmd)
	}
	if strings.Contains(cmd, AutoCompactWindowEnv+"=150000") {
		t.Fatalf("150k cap should have been clamped for a 128k model: %s", cmd)
	}
}

func TestBuildStartupCommandHonorsTownAutoCompactWindow(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	settings := NewTownSettings()
	settings.AutoCompactWindow = 80_000
	if err := SaveTownSettings(TownSettingsPath(townRoot), settings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	cmd := BuildStartupCommand(map[string]string{
		"GT_ROLE": "mayor",
		"GT_ROOT": townRoot,
	}, "", "")
	if !strings.Contains(cmd, AutoCompactWindowEnv+"="+strconv.Itoa(80_000)) {
		t.Fatalf("startup command missing configured 80000 window: %s", cmd)
	}
}

func hasFlagValue(args []string, flag, value string) bool {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) && args[i+1] == value {
			return true
		}
		if arg == flag+"="+value {
			return true
		}
	}
	return false
}
