package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// isClaudeCmd checks if a command resolves to the claude binary on any platform.
// Note: Named differently from loader_test.go's isClaudeCommand to avoid redeclaration.
func isClaudeCmd(cmd string) bool {
	base := filepath.Base(cmd)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	return base == "claude"
}

func TestBuiltInAgentPresetSummary(t *testing.T) {
	t.Parallel()
	s := BuiltInAgentPresetSummary()
	if !strings.Contains(s, "cursor") || !strings.Contains(s, "claude") {
		t.Fatalf("BuiltInAgentPresetSummary() = %q, want cursor and claude", s)
	}
	names := strings.Split(s, ", ")
	if !sort.StringsAreSorted(names) {
		t.Errorf("BuiltInAgentPresetSummary not sorted: %q", s)
	}
}

func TestBuiltinPresets(t *testing.T) {
	t.Parallel()
	// Ensure all built-in presets are accessible
	presets := []AgentPreset{AgentClaude, AgentGemini, AgentCodex, AgentKiro, AgentCursor, AgentAuggie, AgentAmp, AgentOpenCode, AgentCopilot, AgentPi, AgentOmp}

	for _, preset := range presets {
		info := GetAgentPreset(preset)
		if info == nil {
			t.Errorf("GetAgentPreset(%s) returned nil", preset)
			continue
		}

		if info.Command == "" {
			t.Errorf("preset %s has empty Command", preset)
		}

		// All presets should have ProcessNames for agent detection
		if len(info.ProcessNames) == 0 {
			t.Errorf("preset %s has empty ProcessNames", preset)
		}
	}
}

func TestGetAgentPresetByName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		want    AgentPreset
		wantNil bool
	}{
		{"claude", AgentClaude, false},
		{"gemini", AgentGemini, false},
		{"codex", AgentCodex, false},
		{"kiro", AgentKiro, false},
		{"cursor", AgentCursor, false},
		{"auggie", AgentAuggie, false},
		{"amp", AgentAmp, false},
		{"aider", "", true},                // Not built-in, can be added via config
		{"opencode", AgentOpenCode, false}, // Built-in multi-model CLI agent
		{"copilot", AgentCopilot, false},   // Built-in GitHub Copilot CLI agent
		{"pi", AgentPi, false},             // Pi Coding Agent
		{"omp", AgentOmp, false},           // Oh My Pi
		{"unknown", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetAgentPresetByName(tt.name)
			if tt.wantNil && got != nil {
				t.Errorf("GetAgentPresetByName(%s) = %v, want nil", tt.name, got)
			}
			if !tt.wantNil && got == nil {
				t.Errorf("GetAgentPresetByName(%s) = nil, want preset", tt.name)
			}
			if !tt.wantNil && got != nil && got.Name != tt.want {
				t.Errorf("GetAgentPresetByName(%s).Name = %v, want %v", tt.name, got.Name, tt.want)
			}
		})
	}
}

func TestRuntimeConfigFromPreset(t *testing.T) {
	t.Parallel()
	tests := []struct {
		preset      AgentPreset
		wantCommand string
	}{
		{AgentClaude, "claude"}, // Note: claude may resolve to full path
		{AgentGemini, "gemini"},
		{AgentCodex, "codex"},
		{AgentKiro, "kiro-cli"},
		{AgentCursor, "cursor-agent"},
		{AgentAuggie, "auggie"},
		{AgentAmp, "amp"},
		{AgentCopilot, "copilot"},
	}

	for _, tt := range tests {
		t.Run(string(tt.preset), func(t *testing.T) {
			rc := RuntimeConfigFromPreset(tt.preset)
			// For claude, command may be full path due to resolveClaudePath
			if tt.preset == AgentClaude {
				if !isClaudeCmd(rc.Command) {
					t.Errorf("RuntimeConfigFromPreset(%s).Command = %v, want claude or path ending in /claude",
						tt.preset, rc.Command)
				}
			} else if rc.Command != tt.wantCommand {
				t.Errorf("RuntimeConfigFromPreset(%s).Command = %v, want %v",
					tt.preset, rc.Command, tt.wantCommand)
			}
		})
	}
}

func TestRuntimeConfigFromPresetReturnsNilEnvForPresetsWithoutEnv(t *testing.T) {
	t.Parallel()
	// Built-in presets like Claude don't have Env set
	// This verifies nil Env handling in RuntimeConfigFromPreset
	rc := RuntimeConfigFromPreset(AgentClaude)
	if rc == nil {
		t.Fatal("RuntimeConfigFromPreset returned nil")
	}

	// Claude preset doesn't have Env, so it should be nil
	if rc.Env != nil && len(rc.Env) > 0 {
		t.Errorf("Expected nil/empty Env for Claude preset, got %v", rc.Env)
	}
}

func TestIsKnownPreset(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want bool
	}{
		{"claude", true},
		{"gemini", true},
		{"codex", true},
		{"cursor", true},
		{"auggie", true},
		{"amp", true},
		{"aider", false},   // Not built-in, can be added via config
		{"opencode", true}, // Built-in multi-model CLI agent
		{"copilot", true},  // Built-in GitHub Copilot CLI agent
		{"pi", true},       // Pi Coding Agent
		{"omp", true},      // Oh My Pi
		{"unknown", false},
		{"chatgpt", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsKnownPreset(tt.name); got != tt.want {
				t.Errorf("IsKnownPreset(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestLoadAgentRegistry(t *testing.T) {
	// Create temp directory for test config
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "agents.json")

	// Write custom agent config
	customRegistry := AgentRegistry{
		Version: CurrentAgentRegistryVersion,
		Agents: map[string]*AgentPresetInfo{
			"my-agent": {
				Name:    "my-agent",
				Command: "my-agent-bin",
				Args:    []string{"--auto"},
			},
		},
	}

	data, err := json.Marshal(customRegistry)
	if err != nil {
		t.Fatalf("failed to marshal test config: %v", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	// Reset global registry for test isolation
	ResetRegistryForTesting()

	// Load should succeed
	if err := LoadAgentRegistry(configPath); err != nil {
		t.Fatalf("LoadAgentRegistry failed: %v", err)
	}

	// Check custom agent is available
	myAgent := GetAgentPresetByName("my-agent")
	if myAgent == nil {
		t.Fatal("custom agent 'my-agent' not found after loading registry")
	}

	if myAgent.Command != "my-agent-bin" {
		t.Errorf("my-agent.Command = %v, want my-agent-bin", myAgent.Command)
	}

	// Check built-ins still accessible
	claude := GetAgentPresetByName("claude")
	if claude == nil {
		t.Fatal("built-in 'claude' not found after loading registry")
	}

	// Reset for other tests
	ResetRegistryForTesting()
}

func TestGetProcessNamesRespectsRegistryOverride(t *testing.T) {
	// Regression test: settings/agents.json overrides must be visible to
	// GetProcessNames so that liveness checks (IsAgentAlive, daemon heartbeat,
	// cleanup) respect user-configured process names.
	// Real-world case: NixOS wraps claude as ".claude-unwrapped".
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	// Before loading any registry, GetProcessNames returns the builtin default.
	builtinNames := GetProcessNames("claude")
	if len(builtinNames) != 2 || builtinNames[0] != "node" || builtinNames[1] != "claude" {
		t.Fatalf("builtin GetProcessNames(claude) = %v, want [node claude]", builtinNames)
	}

	// Write a settings/agents.json that adds ".claude-unwrapped" to process_names.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "agents.json")

	customRegistry := AgentRegistry{
		Version: CurrentAgentRegistryVersion,
		Agents: map[string]*AgentPresetInfo{
			"claude": {
				Name:         "claude",
				Command:      "claude",
				Args:         []string{"--dangerously-skip-permissions"},
				ProcessNames: []string{"node", "claude", ".claude-unwrapped"},
			},
		},
	}

	data, err := json.Marshal(customRegistry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// After loading the registry, GetProcessNames must return the override.
	if err := LoadAgentRegistry(configPath); err != nil {
		t.Fatalf("LoadAgentRegistry: %v", err)
	}

	got := GetProcessNames("claude")
	want := []string{"node", "claude", ".claude-unwrapped"}
	if len(got) != len(want) {
		t.Fatalf("GetProcessNames(claude) after LoadAgentRegistry = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("GetProcessNames(claude)[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolveProcessNames(t *testing.T) {
	t.Parallel()
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	tests := []struct {
		name      string
		agentName string
		command   string
		want      []string
	}{
		{
			name:      "built-in preset with matching command",
			agentName: "claude",
			command:   "claude",
			want:      []string{"node", "claude"},
		},
		{
			name:      "built-in preset with matching command (opencode)",
			agentName: "opencode",
			command:   "opencode",
			want:      []string{"opencode", "node", "bun"},
		},
		{
			name:      "custom agent shadowing built-in with different command",
			agentName: "codex",
			command:   "opencode",
			want:      []string{"opencode", "node", "bun"},
		},
		{
			name:      "custom agent shadowing built-in with same command",
			agentName: "codex",
			command:   "codex",
			want:      []string{"codex"},
		},
		{
			name:      "built-in preset through gt wrapper command",
			agentName: "codex",
			command:   "gt-codex",
			want:      []string{"codex"},
		},
		{
			name:      "unknown agent with known command",
			agentName: "my-custom-agent",
			command:   "claude",
			want:      []string{"node", "claude"},
		},
		{
			name:      "unknown agent with unknown command",
			agentName: "my-custom-agent",
			command:   "my-binary",
			want:      []string{"my-binary"},
		},
		{
			name:      "empty agent name with command",
			agentName: "",
			command:   "opencode",
			want:      []string{"opencode", "node", "bun"},
		},
		{
			name:      "path-resolved command matches built-in preset",
			agentName: "claude",
			command:   "/usr/local/bin/claude",
			want:      []string{"node", "claude"},
		},
		{
			name:      "path-resolved command with unknown agent",
			agentName: "my-custom-agent",
			command:   "/opt/bin/opencode",
			want:      []string{"opencode", "node", "bun"},
		},
		{
			name:      "path-resolved unknown command falls back to basename",
			agentName: "my-agent",
			command:   "/usr/local/bin/my-binary",
			want:      []string{"my-binary"},
		},
		{
			name:      "empty agent and command",
			agentName: "",
			command:   "",
			want:      []string{"node", "claude"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveProcessNames(tt.agentName, tt.command)
			if len(got) != len(tt.want) {
				t.Fatalf("ResolveProcessNames(%q, %q) = %v, want %v", tt.agentName, tt.command, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ResolveProcessNames(%q, %q)[%d] = %q, want %q", tt.agentName, tt.command, i, got[i], tt.want[i])
				}
			}
		})
	}

	// Test registry preset with absolute-path command.
	// Custom agents loaded from agents.json may store full paths in Command.
	// ResolveProcessNames must normalize both sides to match correctly.
	t.Run("registry preset with absolute-path command matches", func(t *testing.T) {
		RegisterAgentForTesting("custom-tool", AgentPresetInfo{
			Name:         "custom-tool",
			Command:      "/opt/bin/custom-tool",
			ProcessNames: []string{"custom-tool", "node"},
		})
		t.Cleanup(func() {
			ResetRegistryForTesting()
		})

		// Query with basename — should match via filepath.Base normalization
		got := ResolveProcessNames("custom-tool", "custom-tool")
		want := []string{"custom-tool", "node"}
		if len(got) != len(want) {
			t.Fatalf("ResolveProcessNames with abs-path registry = %v, want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("registry preset with absolute-path command matches via command lookup", func(t *testing.T) {
		RegisterAgentForTesting("abs-tool", AgentPresetInfo{
			Name:         "abs-tool",
			Command:      "/usr/local/bin/special-binary",
			ProcessNames: []string{"special-binary", "helper"},
		})
		t.Cleanup(func() {
			ResetRegistryForTesting()
		})

		// Query with different agent name but matching command basename
		got := ResolveProcessNames("unknown-agent", "special-binary")
		want := []string{"special-binary", "helper"}
		if len(got) != len(want) {
			t.Fatalf("ResolveProcessNames command-based lookup with abs-path = %v, want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})

	// Regression: custom agents wrapped in `env -u VAR <real-binary>` (or
	// nohup/sudo/etc.) used to fall through to GT_PROCESS_NAMES=<wrapper>,
	// which IsAgentAlive could never match — wrapper has exec'd into the real
	// binary by then. ResolveProcessNames must look past the wrapper.
	wrapperCases := []struct {
		name    string
		agent   AgentPresetInfo
		want    []string
		command string // command passed to ResolveProcessNames
	}{
		{
			name: "env -u VAR claude unwraps to claude preset",
			agent: AgentPresetInfo{
				Name:    "claude",
				Command: "env",
				Args:    []string{"-u", "ANTHROPIC_API_KEY", "claude", "--dangerously-skip-permissions", "--effort", "high"},
			},
			command: "env",
			want:    []string{"node", "claude"},
		},
		{
			name: "env VAR=val claude unwraps past assignments",
			agent: AgentPresetInfo{
				Name:    "claude",
				Command: "env",
				Args:    []string{"FOO=bar", "BAZ=qux", "claude"},
			},
			command: "env",
			want:    []string{"node", "claude"},
		},
		{
			name: "env -- claude (separator) unwraps to claude",
			agent: AgentPresetInfo{
				Name:    "claude",
				Command: "env",
				Args:    []string{"-i", "--", "claude", "--foo"},
			},
			command: "env",
			want:    []string{"node", "claude"},
		},
		{
			name: "nohup opencode unwraps to opencode preset",
			agent: AgentPresetInfo{
				Name:    "opencode",
				Command: "nohup",
				Args:    []string{"opencode", "--quiet"},
			},
			command: "nohup",
			want:    []string{"opencode", "node", "bun"},
		},
		{
			name: "sudo -u runner codex unwraps to codex preset",
			agent: AgentPresetInfo{
				Name:    "codex",
				Command: "sudo",
				Args:    []string{"-u", "runner", "codex", "--dangerously-bypass-approvals-and-sandbox"},
			},
			command: "sudo",
			want:    []string{"codex"},
		},
		{
			name: "env wrapping unknown binary returns binary basename",
			agent: AgentPresetInfo{
				Name:    "my-agent",
				Command: "env",
				Args:    []string{"-u", "FOO", "/opt/my-tool", "--flag"},
			},
			command: "env",
			want:    []string{"my-tool"},
		},
	}
	for _, tc := range wrapperCases {
		t.Run(tc.name, func(t *testing.T) {
			RegisterAgentForTesting(string(tc.agent.Name), tc.agent)
			t.Cleanup(ResetRegistryForTesting)

			got := ResolveProcessNames(string(tc.agent.Name), tc.command, tc.agent.Args...)
			if len(got) != len(tc.want) {
				t.Fatalf("ResolveProcessNames(%q, %q) = %v, want %v", tc.agent.Name, tc.command, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}

	// Real-world scenario: Paul's town settings shadow the canonical claude
	// preset with a wrapper. The registry still holds the built-in claude
	// preset; Args live only on the caller's RuntimeConfig. Caller args must
	// take precedence so wrapper-unwrap finds the real binary.
	t.Run("caller args used when registry holds canonical preset", func(t *testing.T) {
		ResetRegistryForTesting()
		t.Cleanup(ResetRegistryForTesting)
		// No RegisterAgentForTesting — registry has canonical built-in claude
		// (Command="claude", ProcessNames=[node, claude], Args=[--dangerously-...]).
		got := ResolveProcessNames("claude", "env",
			"-u", "ANTHROPIC_API_KEY", "claude", "--dangerously-skip-permissions", "--effort", "high")
		want := []string{"node", "claude"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})
}

func TestAgentPresetApprovalFlags(t *testing.T) {
	t.Parallel()
	// Verify permissive-approval flags are set correctly for each E2E tested agent.
	tests := []struct {
		preset  AgentPreset
		wantArg string // At least this arg should be present
	}{
		{AgentClaude, "--dangerously-skip-permissions"},
		{AgentGemini, "yolo"}, // Part of "--approval-mode yolo"
		{AgentCodex, "--dangerously-bypass-approvals-and-sandbox"},
		{AgentCopilot, "--yolo"},
	}

	for _, tt := range tests {
		t.Run(string(tt.preset), func(t *testing.T) {
			info := GetAgentPreset(tt.preset)
			if info == nil {
				t.Fatalf("preset %s not found", tt.preset)
			}

			found := false
			for _, arg := range info.Args {
				if arg == tt.wantArg || (tt.preset == AgentGemini && arg == "yolo") {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("preset %s args %v missing expected %s", tt.preset, info.Args, tt.wantArg)
			}
		})
	}
}

func TestMergeWithPreset(t *testing.T) {
	t.Parallel()
	// Test that user config overrides preset defaults
	userConfig := &RuntimeConfig{
		Command: "/custom/claude",
		Args:    []string{"--custom-arg"},
	}

	merged := userConfig.MergeWithPreset(AgentClaude)

	if merged.Command != "/custom/claude" {
		t.Errorf("merged command should be user value, got %s", merged.Command)
	}

	if len(merged.Args) != 1 || merged.Args[0] != "--custom-arg" {
		t.Errorf("merged args should be user value, got %v", merged.Args)
	}

	// Test nil config gets preset defaults
	var nilConfig *RuntimeConfig
	merged = nilConfig.MergeWithPreset(AgentClaude)

	if !isClaudeCmd(merged.Command) {
		t.Errorf("nil config merge should get preset command (claude or path), got %s", merged.Command)
	}

	// Test empty config gets preset defaults
	emptyConfig := &RuntimeConfig{}
	merged = emptyConfig.MergeWithPreset(AgentGemini)

	if merged.Command != "gemini" {
		t.Errorf("empty config merge should get preset command, got %s", merged.Command)
	}
}

func TestBuildResumeCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		agentName string
		sessionID string
		wantEmpty bool
		contains  []string // strings that should appear in result
	}{
		{
			name:      "claude with session",
			agentName: "claude",
			sessionID: "session-123",
			wantEmpty: false,
			contains:  []string{"claude", "--dangerously-skip-permissions", "--resume", "session-123"},
		},
		{
			name:      "gemini with session",
			agentName: "gemini",
			sessionID: "gemini-sess-456",
			wantEmpty: false,
			contains:  []string{"gemini", "--approval-mode", "yolo", "--resume", "gemini-sess-456"},
		},
		{
			name:      "codex subcommand style",
			agentName: "codex",
			sessionID: "codex-sess-789",
			wantEmpty: false,
			contains:  []string{"codex", "resume", "codex-sess-789", "--dangerously-bypass-approvals-and-sandbox"},
		},
		{
			name:      "kiro flag style",
			agentName: "kiro",
			sessionID: "f2946a26-3735-4b08-8d05-c928010302d5",
			wantEmpty: false,
			contains:  []string{"kiro-cli", "chat", "--trust-all-tools", "--resume-id", "f2946a26-3735-4b08-8d05-c928010302d5"},
		},
		{
			name:      "empty session ID",
			agentName: "claude",
			sessionID: "",
			wantEmpty: true,
			contains:  []string{"claude"},
		},
		{
			name:      "copilot flag style",
			agentName: "copilot",
			sessionID: "cea0d5f0-662a-4a98-9585-060b9d2a7a19",
			wantEmpty: false,
			contains:  []string{"copilot", "--yolo", "--resume", "cea0d5f0-662a-4a98-9585-060b9d2a7a19"},
		},
		{
			name:      "unknown agent",
			agentName: "unknown-agent",
			sessionID: "session-123",
			wantEmpty: true,
			contains:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildResumeCommand(tt.agentName, tt.sessionID)
			if tt.wantEmpty {
				if result != "" {
					t.Errorf("BuildResumeCommand(%s, %s) = %q, want empty", tt.agentName, tt.sessionID, result)
				}
				return
			}
			for _, s := range tt.contains {
				if !strings.Contains(result, s) {
					t.Errorf("BuildResumeCommand(%s, %s) = %q, missing %q", tt.agentName, tt.sessionID, result, s)
				}
			}
		})
	}
}

func TestSupportsSessionResume(t *testing.T) {
	t.Parallel()
	tests := []struct {
		agentName string
		want      bool
	}{
		{"claude", true},
		{"gemini", true},
		{"codex", true},
		{"kiro", true},
		{"cursor", true},
		{"auggie", true},
		{"amp", true},
		{"copilot", true},
		{"unknown", false},
	}

	for _, tt := range tests {
		t.Run(tt.agentName, func(t *testing.T) {
			if got := SupportsSessionResume(tt.agentName); got != tt.want {
				t.Errorf("SupportsSessionResume(%s) = %v, want %v", tt.agentName, got, tt.want)
			}
		})
	}
}

func TestGetSessionIDEnvVar(t *testing.T) {
	t.Parallel()
	tests := []struct {
		agentName string
		want      string
	}{
		{"claude", "CLAUDE_SESSION_ID"},
		{"gemini", "GEMINI_SESSION_ID"},
		{"codex", ""},   // Codex uses JSONL output instead
		{"kiro", ""},    // Kiro stores sessions per directory and resumes by CLI flag
		{"cursor", ""},  // Cursor uses --resume with chatId directly
		{"auggie", ""},  // Auggie uses --resume directly
		{"amp", ""},     // AMP uses 'threads continue' subcommand
		{"copilot", ""}, // Copilot stores session IDs on disk, not in env
		{"unknown", ""},
	}

	for _, tt := range tests {
		t.Run(tt.agentName, func(t *testing.T) {
			if got := GetSessionIDEnvVar(tt.agentName); got != tt.want {
				t.Errorf("GetSessionIDEnvVar(%s) = %q, want %q", tt.agentName, got, tt.want)
			}
		})
	}
}

func TestGetProcessNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		agentName string
		want      []string
	}{
		{"claude", []string{"node", "claude"}},
		{"gemini", []string{"gemini"}},
		{"codex", []string{"codex"}},
		{"kiro", []string{"kiro-cli"}},
		{"cursor", []string{"cursor-agent", "agent"}},
		{"auggie", []string{"auggie"}},
		{"amp", []string{"amp"}},
		{"opencode", []string{"opencode", "node", "bun"}},
		{"copilot", []string{"copilot"}},
		{"pi", []string{"pi", "node", "bun"}},
		{"unknown", []string{"node", "claude"}}, // Falls back to Claude's process
	}

	for _, tt := range tests {
		t.Run(tt.agentName, func(t *testing.T) {
			got := GetProcessNames(tt.agentName)
			if len(got) != len(tt.want) {
				t.Errorf("GetProcessNames(%s) = %v, want %v", tt.agentName, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("GetProcessNames(%s)[%d] = %q, want %q", tt.agentName, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestListAgentPresetsMatchesConstants(t *testing.T) {
	t.Parallel()
	// Ensure all AgentPreset constants are returned by ListAgentPresets
	allConstants := []AgentPreset{AgentClaude, AgentGemini, AgentCodex, AgentCursor, AgentAuggie, AgentAmp, AgentOpenCode, AgentCopilot, AgentPi, AgentOmp}
	presets := ListAgentPresets()

	// Convert to map for quick lookup
	presetMap := make(map[string]bool)
	for _, p := range presets {
		presetMap[p] = true
	}

	// Verify all constants are in the list
	for _, c := range allConstants {
		if !presetMap[string(c)] {
			t.Errorf("ListAgentPresets() missing constant %q", c)
		}
	}

	// Verify no empty names
	for _, p := range presets {
		if p == "" {
			t.Error("ListAgentPresets() contains empty string")
		}
	}
}

func TestAgentCommandGeneration(t *testing.T) {
	t.Parallel()
	// Test full command line generation for each agent
	tests := []struct {
		preset       AgentPreset
		wantCommand  string
		wantContains []string // Args that should be present
	}{
		{
			preset:       AgentClaude,
			wantCommand:  "claude",
			wantContains: []string{"--dangerously-skip-permissions"},
		},
		{
			preset:       AgentGemini,
			wantCommand:  "gemini",
			wantContains: []string{"--approval-mode", "yolo"},
		},
		{
			preset:       AgentCodex,
			wantCommand:  "codex",
			wantContains: []string{"--dangerously-bypass-approvals-and-sandbox"},
		},
		{
			preset:       AgentCursor,
			wantCommand:  "cursor-agent",
			wantContains: []string{"-f"},
		},
		{
			preset:       AgentAuggie,
			wantCommand:  "auggie",
			wantContains: []string{"--allow-indexing"},
		},
		{
			preset:       AgentAmp,
			wantCommand:  "amp",
			wantContains: []string{"--dangerously-allow-all", "--no-ide"},
		},
		{
			preset:       AgentCopilot,
			wantCommand:  "copilot",
			wantContains: []string{"--yolo"},
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.preset), func(t *testing.T) {
			rc := RuntimeConfigFromPreset(tt.preset)
			if rc == nil {
				t.Fatal("RuntimeConfigFromPreset returned nil")
			}

			// For claude, command may be full path due to resolveClaudePath
			if tt.preset == AgentClaude {
				if !isClaudeCmd(rc.Command) {
					t.Errorf("Command = %q, want claude or path ending in /claude", rc.Command)
				}
			} else if rc.Command != tt.wantCommand {
				t.Errorf("Command = %q, want %q", rc.Command, tt.wantCommand)
			}

			// Check required args are present
			argsStr := strings.Join(rc.Args, " ")
			for _, arg := range tt.wantContains {
				found := false
				for _, a := range rc.Args {
					if a == arg {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Args %q missing expected %q", argsStr, arg)
				}
			}
		})
	}
}

func TestCursorAgentPreset(t *testing.T) {
	t.Parallel()
	// Verify cursor agent preset is correctly configured
	info := GetAgentPreset(AgentCursor)
	if info == nil {
		t.Fatal("cursor preset not found")
	}

	// Check command
	if info.Command != "cursor-agent" {
		t.Errorf("cursor command = %q, want cursor-agent", info.Command)
	}

	// Check YOLO-equivalent flag (-f for force mode; CLI also documents --force / --yolo)
	hasF := false
	for _, arg := range info.Args {
		if arg == "-f" {
			hasF = true
		}
	}
	if !hasF {
		t.Error("cursor args missing -f (force/YOLO mode)")
	}

	// Check ProcessNames for detection (install script provides both "agent" and "cursor-agent" symlinks).
	// Tmux only treats "agent" as Cursor when GT_AGENT=cursor or GT_PROCESS_NAMES includes cursor-agent.
	seen := make(map[string]bool, len(info.ProcessNames))
	for _, n := range info.ProcessNames {
		seen[n] = true
	}
	for _, name := range []string{"agent", "cursor-agent"} {
		if !seen[name] {
			t.Errorf("cursor ProcessNames missing %q (got %v)", name, info.ProcessNames)
		}
	}

	// Check resume support
	if info.ResumeFlag != "--resume" {
		t.Errorf("cursor ResumeFlag = %q, want --resume", info.ResumeFlag)
	}
	if info.ResumeStyle != "flag" {
		t.Errorf("cursor ResumeStyle = %q, want flag", info.ResumeStyle)
	}
	if info.ReadyDelayMs != 5000 {
		t.Errorf("cursor ReadyDelayMs = %d, want 5000 (nudge poller + WaitForRuntimeReady)", info.ReadyDelayMs)
	}
}

func TestKiroAgentPreset(t *testing.T) {
	t.Parallel()

	info := GetAgentPreset(AgentKiro)
	if info == nil {
		t.Fatal("kiro preset not found")
	}

	if info.Command != "kiro-cli" {
		t.Errorf("kiro Command = %q, want kiro-cli", info.Command)
	}
	wantArgs := []string{"chat", "--trust-all-tools"}
	if len(info.Args) != len(wantArgs) {
		t.Fatalf("kiro Args = %v, want %v", info.Args, wantArgs)
	}
	for i, want := range wantArgs {
		if info.Args[i] != want {
			t.Errorf("kiro Args[%d] = %q, want %q", i, info.Args[i], want)
		}
	}

	if len(info.ProcessNames) != 1 || info.ProcessNames[0] != "kiro-cli" {
		t.Errorf("kiro ProcessNames = %v, want [kiro-cli]", info.ProcessNames)
	}
	if info.SessionIDEnv != "" {
		t.Errorf("kiro SessionIDEnv = %q, want empty", info.SessionIDEnv)
	}
	if info.ResumeFlag != "--resume-id" {
		t.Errorf("kiro ResumeFlag = %q, want --resume-id", info.ResumeFlag)
	}
	if info.ContinueFlag != "--resume" {
		t.Errorf("kiro ContinueFlag = %q, want --resume", info.ContinueFlag)
	}
	if info.ResumeStyle != "flag" {
		t.Errorf("kiro ResumeStyle = %q, want flag", info.ResumeStyle)
	}
	if info.SupportsHooks {
		t.Error("kiro SupportsHooks should remain false until Gas Town has a Kiro hook adapter")
	}
	if info.SupportsForkSession {
		t.Error("kiro should not support fork session")
	}
	if info.NonInteractive != nil {
		t.Errorf("kiro NonInteractive = %+v, want nil until --no-interactive positional prompts are modeled", info.NonInteractive)
	}
	if info.ReadyDelayMs != 5000 {
		t.Errorf("kiro ReadyDelayMs = %d, want 5000", info.ReadyDelayMs)
	}
	if info.InstructionsFile != "AGENTS.md" {
		t.Errorf("kiro InstructionsFile = %q, want AGENTS.md", info.InstructionsFile)
	}
}

// TestDefaultRigAgentRegistryPath verifies that the default rig agent registry path is constructed correctly.
func TestDefaultRigAgentRegistryPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		rigPath      string
		expectedPath string
	}{
		{"/Users/alice/gt/myproject", "/Users/alice/gt/myproject/settings/agents.json"},
		{"/tmp/my-rig", "/tmp/my-rig/settings/agents.json"},
		{"relative/path", "relative/path/settings/agents.json"},
	}

	for _, tt := range tests {
		t.Run(tt.rigPath, func(t *testing.T) {
			got := DefaultRigAgentRegistryPath(tt.rigPath)
			want := tt.expectedPath
			if filepath.ToSlash(got) != filepath.ToSlash(want) {
				t.Errorf("DefaultRigAgentRegistryPath(%s) = %s, want %s", tt.rigPath, got, want)
			}
		})
	}
}

// TestLoadRigAgentRegistry verifies that rig-level agent registry is loaded correctly.
func TestLoadRigAgentRegistry(t *testing.T) {
	// Reset registry for test isolation
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	tmpDir := t.TempDir()
	registryPath := filepath.Join(tmpDir, "settings", "agents.json")
	configDir := filepath.Join(tmpDir, "settings")

	// Create settings directory
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("failed to create settings dir: %v", err)
	}

	// Write agent registry
	registryContent := `{
  "version": 1,
  "agents": {
    "opencode": {
      "command": "opencode",
      "args": ["--session"],
      "non_interactive": {
        "subcommand": "run",
        "output_flag": "--format json"
      }
    }
  }
}`

	if err := os.WriteFile(registryPath, []byte(registryContent), 0644); err != nil {
		t.Fatalf("failed to write registry file: %v", err)
	}

	// Test 1: Load should succeed and merge agents
	t.Run("load and merge", func(t *testing.T) {
		if err := LoadRigAgentRegistry(registryPath); err != nil {
			t.Fatalf("LoadRigAgentRegistry(%s) failed: %v", registryPath, err)
		}

		info := GetAgentPresetByName("opencode")
		if info == nil {
			t.Fatal("expected opencode agent to be available after loading rig registry")
		}

		if info.Command != "opencode" {
			t.Errorf("expected opencode agent command to be 'opencode', got %s", info.Command)
		}
		if info.ConfigDir != ".opencode" {
			t.Errorf("expected opencode ConfigDir to inherit '.opencode', got %q", info.ConfigDir)
		}
		if info.HooksDir != ".opencode/plugins" {
			t.Errorf("expected opencode HooksDir to inherit '.opencode/plugins', got %q", info.HooksDir)
		}
		if info.HooksSettingsFile != "gastown.js" {
			t.Errorf("expected opencode HooksSettingsFile to inherit 'gastown.js', got %q", info.HooksSettingsFile)
		}
		if len(info.ProcessNames) == 0 {
			t.Errorf("expected opencode ProcessNames to remain populated after partial override")
		}
		if info.ReadyDelayMs != 8000 {
			t.Errorf("expected opencode ReadyDelayMs to inherit 8000, got %d", info.ReadyDelayMs)
		}
	})

	// Test 2: File not found should return nil (no error)
	t.Run("file not found", func(t *testing.T) {
		nonExistentPath := filepath.Join(tmpDir, "other-rig", "settings", "agents.json")
		if err := LoadRigAgentRegistry(nonExistentPath); err != nil {
			t.Errorf("LoadRigAgentRegistry(%s) should not error for non-existent file: %v", nonExistentPath, err)
		}

		// Verify that previously loaded agent (from test 1) is still available
		info := GetAgentPresetByName("opencode")
		if info == nil {
			t.Errorf("expected opencode agent to still be available after loading non-existent path")
			return
		}
		if info.Command != "opencode" {
			t.Errorf("expected opencode agent command to be 'opencode', got %s", info.Command)
		}
	})

	// Test 3: Invalid JSON should error
	t.Run("invalid JSON", func(t *testing.T) {
		invalidRegistryPath := filepath.Join(tmpDir, "bad-rig", "settings", "agents.json")
		badConfigDir := filepath.Join(tmpDir, "bad-rig", "settings")
		if err := os.MkdirAll(badConfigDir, 0755); err != nil {
			t.Fatalf("failed to create bad-rig settings dir: %v", err)
		}

		invalidContent := `{"version": 1, "agents": {invalid json}}`
		if err := os.WriteFile(invalidRegistryPath, []byte(invalidContent), 0644); err != nil {
			t.Fatalf("failed to write invalid registry file: %v", err)
		}

		if err := LoadRigAgentRegistry(invalidRegistryPath); err == nil {
			t.Errorf("LoadRigAgentRegistry(%s) should error for invalid JSON: got nil", invalidRegistryPath)
		}
	})
}

func TestOpenCodeAgentPreset(t *testing.T) {
	t.Parallel()
	// Verify OpenCode agent preset is correctly configured
	info := GetAgentPreset(AgentOpenCode)
	if info == nil {
		t.Fatal("opencode preset not found")
	}

	// Check command
	if info.Command != "opencode" {
		t.Errorf("opencode command = %q, want opencode", info.Command)
	}

	// Check Args (should be empty - YOLO via Env)
	if len(info.Args) != 0 {
		t.Errorf("opencode args = %v, want empty (uses Env for YOLO)", info.Args)
	}

	// Check Env for OPENCODE_PERMISSION
	if info.Env == nil {
		t.Fatal("opencode Env is nil")
	}
	permission, ok := info.Env["OPENCODE_PERMISSION"]
	if !ok {
		t.Error("opencode Env missing OPENCODE_PERMISSION")
	}
	if permission != `{"*":"allow"}` {
		t.Errorf("OPENCODE_PERMISSION = %q, want {\"*\":\"allow\"}", permission)
	}

	// Check ProcessNames for detection (opencode, node, bun)
	if len(info.ProcessNames) != 3 {
		t.Errorf("opencode ProcessNames length = %d, want 3", len(info.ProcessNames))
	}
	expectedNames := []string{"opencode", "node", "bun"}
	for i, want := range expectedNames {
		if i < len(info.ProcessNames) && info.ProcessNames[i] != want {
			t.Errorf("opencode ProcessNames[%d] = %q, want %q", i, info.ProcessNames[i], want)
		}
	}

	// Check hooks support
	if !info.SupportsHooks {
		t.Error("opencode should support hooks")
	}

	// Check fork session (not supported)
	if info.SupportsForkSession {
		t.Error("opencode should not support fork session")
	}

	// Check NonInteractive config
	if info.NonInteractive == nil {
		t.Fatal("opencode NonInteractive is nil")
	}
	if info.NonInteractive.Subcommand != "run" {
		t.Errorf("opencode NonInteractive.Subcommand = %q, want run", info.NonInteractive.Subcommand)
	}
	if info.NonInteractive.OutputFlag != "--format json" {
		t.Errorf("opencode NonInteractive.OutputFlag = %q, want --format json", info.NonInteractive.OutputFlag)
	}
}

func TestOpenCodeProviderDefaults(t *testing.T) {
	t.Parallel()

	// Test defaultReadyDelayMs for opencode
	delay := defaultReadyDelayMs("opencode")
	if delay != 8000 {
		t.Errorf("defaultReadyDelayMs(opencode) = %d, want 8000", delay)
	}

	// Test defaultProcessNames for opencode (from preset: opencode, node, bun)
	names := defaultProcessNames("opencode", "opencode")
	if len(names) != 3 {
		t.Errorf("defaultProcessNames(opencode) length = %d, want 3", len(names))
	}
	if len(names) >= 3 && (names[0] != "opencode" || names[1] != "node" || names[2] != "bun") {
		t.Errorf("defaultProcessNames(opencode) = %v, want [opencode, node, bun]", names)
	}

	// Test defaultInstructionsFile for opencode
	instFile := defaultInstructionsFile("opencode")
	if instFile != "AGENTS.md" {
		t.Errorf("defaultInstructionsFile(opencode) = %q, want AGENTS.md", instFile)
	}
}

func TestOpenCodeRuntimeConfigFromPreset(t *testing.T) {
	t.Parallel()
	rc := RuntimeConfigFromPreset(AgentOpenCode)
	if rc == nil {
		t.Fatal("RuntimeConfigFromPreset(opencode) returned nil")
	}

	// Check command
	if rc.Command != "opencode" {
		t.Errorf("RuntimeConfig.Command = %q, want opencode", rc.Command)
	}

	// Check Env is copied
	if rc.Env == nil {
		t.Fatal("RuntimeConfig.Env is nil")
	}
	if rc.Env["OPENCODE_PERMISSION"] != `{"*":"allow"}` {
		t.Errorf("RuntimeConfig.Env[OPENCODE_PERMISSION] = %q, want {\"*\":\"allow\"}", rc.Env["OPENCODE_PERMISSION"])
	}

	// Verify Env is a copy (mutation doesn't affect original)
	rc.Env["MUTATED"] = "yes"
	original := GetAgentPreset(AgentOpenCode)
	if _, exists := original.Env["MUTATED"]; exists {
		t.Error("Mutation of RuntimeConfig.Env affected original preset")
	}
}

func TestCopilotAgentPreset(t *testing.T) {
	t.Parallel()
	info := GetAgentPreset(AgentCopilot)
	if info == nil {
		t.Fatal("copilot preset not found")
	}

	if info.Command != "copilot" {
		t.Errorf("copilot command = %q, want copilot", info.Command)
	}

	hasYolo := false
	for _, arg := range info.Args {
		if arg == "--yolo" {
			hasYolo = true
		}
	}
	if !hasYolo {
		t.Error("copilot args missing --yolo")
	}

	if len(info.ProcessNames) != 1 || info.ProcessNames[0] != "copilot" {
		t.Errorf("copilot ProcessNames = %v, want [copilot]", info.ProcessNames)
	}

	if info.SessionIDEnv != "" {
		t.Errorf("copilot SessionIDEnv = %q, want empty", info.SessionIDEnv)
	}

	if info.ResumeFlag != "--resume" {
		t.Errorf("copilot ResumeFlag = %q, want --resume", info.ResumeFlag)
	}
	if info.ContinueFlag != "--continue" {
		t.Errorf("copilot ContinueFlag = %q, want --continue", info.ContinueFlag)
	}
	if info.ResumeStyle != "flag" {
		t.Errorf("copilot ResumeStyle = %q, want flag", info.ResumeStyle)
	}

	if !info.SupportsHooks {
		t.Error("copilot should support hooks (.github/hooks/*.json lifecycle hooks)")
	}

	if info.SupportsForkSession {
		t.Error("copilot should not support fork session")
	}

	// GA: COPILOT_HOME overrides config directory
	if info.ConfigDirEnv != "COPILOT_HOME" {
		t.Errorf("copilot ConfigDirEnv = %q, want COPILOT_HOME", info.ConfigDirEnv)
	}

	// GA: no detectable prompt prefix — uses delay-based readiness
	if info.ReadyPromptPrefix != "" {
		t.Errorf("copilot ReadyPromptPrefix = %q, want empty (GA has no ❯ prompt)", info.ReadyPromptPrefix)
	}

	if info.NonInteractive == nil {
		t.Fatal("copilot NonInteractive is nil")
	}
	if info.NonInteractive.PromptFlag != "-p" {
		t.Errorf("copilot NonInteractive.PromptFlag = %q, want -p", info.NonInteractive.PromptFlag)
	}
}

func TestPiAgentPreset(t *testing.T) {
	t.Parallel()
	info := GetAgentPreset(AgentPi)
	if info == nil {
		t.Fatal("pi preset not found")
	}

	if info.Command != "pi" {
		t.Errorf("pi command = %q, want pi", info.Command)
	}

	// Pi preset includes -e flag to load gastown hooks extension
	if len(info.Args) != 2 || info.Args[0] != "-e" {
		t.Errorf("pi args = %v, want [-e .pi/extensions/gastown-hooks.js]", info.Args)
	}

	if len(info.ProcessNames) != 3 {
		t.Errorf("pi ProcessNames length = %d, want 3", len(info.ProcessNames))
	}
	expectedNames := []string{"pi", "node", "bun"}
	for i, want := range expectedNames {
		if i < len(info.ProcessNames) && info.ProcessNames[i] != want {
			t.Errorf("pi ProcessNames[%d] = %q, want %q", i, info.ProcessNames[i], want)
		}
	}

	if !info.SupportsHooks {
		t.Error("pi should support hooks")
	}

	if info.SupportsForkSession {
		t.Error("pi should not support fork session")
	}

	if info.SessionIDEnv != "PI_SESSION_ID" {
		t.Errorf("pi SessionIDEnv = %q, want PI_SESSION_ID", info.SessionIDEnv)
	}

	if info.NonInteractive == nil {
		t.Fatal("pi NonInteractive is nil")
	}
	if info.NonInteractive.PromptFlag != "-p" {
		t.Errorf("pi NonInteractive.PromptFlag = %q, want -p", info.NonInteractive.PromptFlag)
	}
}

func TestCopilotProviderDefaults(t *testing.T) {
	t.Parallel()

	cmd := defaultRuntimeCommand("copilot")
	if cmd != "copilot" {
		t.Errorf("defaultRuntimeCommand(copilot) = %q, want copilot", cmd)
	}

	args := defaultRuntimeArgs("copilot")
	if len(args) != 1 || args[0] != "--yolo" {
		t.Errorf("defaultRuntimeArgs(copilot) = %v, want [--yolo]", args)
	}

	mode := defaultPromptMode("copilot")
	if mode != "arg" {
		t.Errorf("defaultPromptMode(copilot) = %q, want arg", mode)
	}

	env := defaultSessionIDEnv("copilot")
	if env != "" {
		t.Errorf("defaultSessionIDEnv(copilot) = %q, want empty", env)
	}

	configEnv := defaultConfigDirEnv("copilot")
	if configEnv != "COPILOT_HOME" {
		t.Errorf("defaultConfigDirEnv(copilot) = %q, want COPILOT_HOME", configEnv)
	}

	provider := defaultHooksProvider("copilot")
	if provider != "copilot" {
		t.Errorf("defaultHooksProvider(copilot) = %q, want copilot", provider)
	}

	if defaultHooksInformational("copilot") {
		t.Error("defaultHooksInformational(copilot) should be false (executable hooks)")
	}
	if defaultHooksInformational("claude") {
		t.Error("defaultHooksInformational(claude) should be false")
	}

	dir := defaultHooksDir("copilot")
	if dir != ".github/hooks" {
		t.Errorf("defaultHooksDir(copilot) = %q, want .github/hooks", dir)
	}

	file := defaultHooksFile("copilot")
	if file != "gastown.json" {
		t.Errorf("defaultHooksFile(copilot) = %q, want gastown.json", file)
	}

	names := defaultProcessNames("copilot", "copilot")
	if len(names) != 1 || names[0] != "copilot" {
		t.Errorf("defaultProcessNames(copilot) = %v, want [copilot]", names)
	}

	prefix := defaultReadyPromptPrefix("copilot")
	if prefix != "" {
		t.Errorf("defaultReadyPromptPrefix(copilot) = %q, want empty (GA has no ❯ prompt)", prefix)
	}

	delay := defaultReadyDelayMs("copilot")
	if delay != 5000 {
		t.Errorf("defaultReadyDelayMs(copilot) = %d, want 5000", delay)
	}

	instFile := defaultInstructionsFile("copilot")
	if instFile != "AGENTS.md" {
		t.Errorf("defaultInstructionsFile(copilot) = %q, want AGENTS.md", instFile)
	}
}

func TestCopilotRuntimeConfigFromPreset(t *testing.T) {
	t.Parallel()
	rc := RuntimeConfigFromPreset(AgentCopilot)
	if rc == nil {
		t.Fatal("RuntimeConfigFromPreset(copilot) returned nil")
	}

	if rc.Command != "copilot" {
		t.Errorf("RuntimeConfig.Command = %q, want copilot", rc.Command)
	}

	if len(rc.Args) != 1 || rc.Args[0] != "--yolo" {
		t.Errorf("RuntimeConfig.Args = %v, want [--yolo]", rc.Args)
	}

	if rc.Env != nil && len(rc.Env) > 0 {
		t.Errorf("Expected nil/empty Env for Copilot preset, got %v", rc.Env)
	}
}

func TestCodexRuntimeConfigHasPromptDetection(t *testing.T) {
	t.Parallel()

	rc := RuntimeConfigFromPreset(AgentCodex)
	if rc == nil {
		t.Fatal("RuntimeConfigFromPreset(codex) returned nil")
	}
	if rc.Tmux == nil {
		t.Fatal("RuntimeConfigFromPreset(codex).Tmux returned nil")
	}
	if rc.Tmux.ReadyPromptPrefix != "› " {
		t.Errorf("RuntimeConfigFromPreset(codex).Tmux.ReadyPromptPrefix = %q, want %q", rc.Tmux.ReadyPromptPrefix, "› ")
	}
	if rc.PromptMode != "arg" {
		t.Errorf("RuntimeConfigFromPreset(codex).PromptMode = %q, want arg", rc.PromptMode)
	}
	args := strings.Join(rc.Args, " ")
	if !strings.Contains(args, codexUpdateCheckConfig) {
		t.Errorf("RuntimeConfigFromPreset(codex).Args = %v, want %q", rc.Args, codexUpdateCheckConfig)
	}
	if !strings.Contains(args, "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("RuntimeConfigFromPreset(codex).Args = %v, want bypass flag", rc.Args)
	}
}

func TestPiProviderDefaults(t *testing.T) {
	t.Parallel()

	input := &RuntimeConfig{Command: "pi"}
	result := fillRuntimeDefaults(input)

	if result.Tmux == nil {
		t.Fatal("fillRuntimeDefaults(pi) should auto-fill Tmux")
	}
	if result.Tmux.ReadyDelayMs != 8000 {
		t.Errorf("Tmux.ReadyDelayMs = %d, want 8000", result.Tmux.ReadyDelayMs)
	}
	wantNames := []string{"pi", "node", "bun"}
	if len(result.Tmux.ProcessNames) != len(wantNames) {
		t.Errorf("Tmux.ProcessNames = %v, want %v", result.Tmux.ProcessNames, wantNames)
	}

	if result.PromptMode != "arg" {
		t.Errorf("PromptMode = %q, want arg", result.PromptMode)
	}

	if result.Hooks == nil {
		t.Fatal("fillRuntimeDefaults(pi) should auto-fill Hooks")
	}
	if result.Hooks.Provider != "pi" {
		t.Errorf("Hooks.Provider = %q, want pi", result.Hooks.Provider)
	}
}

func TestPiRuntimeConfigFromPreset(t *testing.T) {
	t.Parallel()
	rc := RuntimeConfigFromPreset(AgentPi)
	if rc == nil {
		t.Fatal("RuntimeConfigFromPreset(pi) returned nil")
	}

	if rc.Command != "pi" {
		t.Errorf("RuntimeConfig.Command = %q, want pi", rc.Command)
	}

	if rc.Env != nil && len(rc.Env) > 0 {
		t.Errorf("Expected nil/empty Env for Pi preset, got %v", rc.Env)
	}
}

// TestAllHookSupportingAgentsHaveHookFields ensures that every built-in preset
// with SupportsHooks=true also declares the three fields required by the hooks
// install path: HooksProvider, HooksDir, and HooksSettingsFile.
//
// If this test fails, add the missing fields to the offending preset in
// builtinPresets (agents.go) before setting SupportsHooks=true.
func TestAllHookSupportingAgentsHaveHookFields(t *testing.T) {
	t.Parallel()
	for name, preset := range builtinPresets {
		if !preset.SupportsHooks {
			continue
		}
		if preset.HooksProvider == "" {
			t.Errorf("agent %q: SupportsHooks=true but HooksProvider is empty", name)
		}
		if preset.HooksDir == "" {
			t.Errorf("agent %q: SupportsHooks=true but HooksDir is empty", name)
		}
		if preset.HooksSettingsFile == "" {
			t.Errorf("agent %q: SupportsHooks=true but HooksSettingsFile is empty", name)
		}
	}
}

func TestResolveACPConfig(t *testing.T) {
	t.Parallel()
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	tests := []struct {
		name      string
		agentName string
		command   string
		wantCmd   string
	}{
		{
			name:      "built-in preset with matching command",
			agentName: "opencode",
			command:   "opencode",
			wantCmd:   "acp",
		},
		{
			name:      "custom agent shadowing built-in with same command",
			agentName: "opencode",
			command:   "opencode",
			wantCmd:   "acp",
		},
		{
			name:      "unknown agent with known command",
			agentName: "my-custom-model",
			command:   "opencode",
			wantCmd:   "acp",
		},
		{
			name:      "unknown agent with unknown command",
			agentName: "my-custom-agent",
			command:   "my-binary",
			wantCmd:   "",
		},
		{
			name:      "path-resolved command matches built-in preset",
			agentName: "opencode",
			command:   "/usr/local/bin/opencode",
			wantCmd:   "acp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveACPConfig(tt.agentName, tt.command)
			if tt.wantCmd == "" {
				if got != nil {
					t.Errorf("ResolveACPConfig(%q, %q) = %v, want nil", tt.agentName, tt.command, got)
				}
			} else {
				if got == nil {
					t.Fatalf("ResolveACPConfig(%q, %q) = nil, want config with command %q", tt.agentName, tt.command, tt.wantCmd)
				}
				if got.Command != tt.wantCmd {
					t.Errorf("ResolveACPConfig(%q, %q).Command = %q, want %q", tt.agentName, tt.command, got.Command, tt.wantCmd)
				}
			}
		})
	}
}

func TestSupportsACPWithCustomAgent(t *testing.T) {
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	// Register a custom agent that uses 'opencode' command but doesn't have ACP config
	RegisterAgentForTesting("custom-model", AgentPresetInfo{
		Name:    "custom-model",
		Command: "opencode",
	})

	if !SupportsACP("custom-model") {
		t.Error("SupportsACP(custom-model) = false, want true (uses opencode command)")
	}

	acpCfg := GetACPConfig("custom-model")
	if acpCfg == nil {
		t.Fatal("GetACPConfig(custom-model) = nil, want config")
	}
	if acpCfg.Command != "acp" {
		t.Errorf("GetACPConfig(custom-model).Command = %q, want acp", acpCfg.Command)
	}
}

func TestGetACPCommand(t *testing.T) {
	t.Parallel()
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	tests := []struct {
		agentName string
		want      string
	}{
		{"opencode", "acp"},
		{"claude", ""},
		{"gemini", ""},
		{"codex", ""},
		{"cursor", ""},
		{"auggie", ""},
		{"amp", ""},
		{"copilot", ""},
		{"pi", ""},
		{"unknown-agent", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.agentName, func(t *testing.T) {
			if got := GetACPCommand(tt.agentName); got != tt.want {
				t.Errorf("GetACPCommand(%q) = %q, want %q", tt.agentName, got, tt.want)
			}
		})
	}
}

func TestACPConfig(t *testing.T) {
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "agents.json")

	customRegistry := AgentRegistry{
		Version: CurrentAgentRegistryVersion,
		Agents: map[string]*AgentPresetInfo{
			"custom-agent": {
				Name:    "custom-agent",
				Command: "custom-agent",
				ACP: &ACPConfig{
					Command: "acp",
				},
			},
			"legacy-agent": {
				Name:    "legacy-agent",
				Command: "legacy-agent",
				ACP:     nil,
			},
		},
	}

	data, err := json.Marshal(customRegistry)
	if err != nil {
		t.Fatalf("failed to marshal test config: %v", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	if err := LoadAgentRegistry(configPath); err != nil {
		t.Fatalf("LoadAgentRegistry failed: %v", err)
	}

	if !SupportsACP("custom-agent") {
		t.Error("SupportsACP(custom-agent) = false, want true (has ACP)")
	}

	if GetACPCommand("custom-agent") != "acp" {
		t.Errorf("GetACPCommand(custom-agent) = %q, want acp", GetACPCommand("custom-agent"))
	}

	if SupportsACP("legacy-agent") {
		t.Error("SupportsACP(legacy-agent) = true, want false (no ACP)")
	}

	if GetACPCommand("legacy-agent") != "" {
		t.Errorf("GetACPCommand(legacy-agent) = %q, want empty", GetACPCommand("legacy-agent"))
	}

	agentInfo := GetAgentPresetByName("custom-agent")
	if agentInfo == nil {
		t.Fatal("custom-agent not found after loading registry")
	}
	if agentInfo.ACP == nil || agentInfo.ACP.Command != "acp" {
		t.Errorf("AgentPresetInfo.ACP.Command = %q, want acp", agentInfo.ACP.Command)
	}
}

// TestACPModes tests the three ACP invocation modes:
// - Native mode: Binary is already an ACP adapter
// - Subcommand mode: Agent has ACP as a subcommand
// - Flag mode: Agent uses flags to enable ACP
func TestACPModes(t *testing.T) {
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)

	tests := []struct {
		name     string
		rc       *RuntimeConfig
		wantACP  bool
		wantMode string
		wantCmd  string
		wantArgs []string
	}{
		{
			name: "native mode - claude-agent-acp",
			rc: &RuntimeConfig{
				Command: "claude-agent-acp",
				ACP: &ACPConfig{
					Mode: ACPModeNative,
				},
			},
			wantACP:  true,
			wantMode: ACPModeNative,
			wantCmd:  "",
			wantArgs: nil,
		},
		{
			name: "subcommand mode - opencode acp",
			rc: &RuntimeConfig{
				Command: "opencode",
				ACP: &ACPConfig{
					Command: "acp",
					Args:    []string{"--debug"},
				},
			},
			wantACP:  true,
			wantMode: "", // Mode is not set, defaults to subcommand behavior
			wantCmd:  "acp",
			wantArgs: []string{"--debug"},
		},
		{
			name: "subcommand mode with explicit mode",
			rc: &RuntimeConfig{
				Command: "opencode",
				ACP: &ACPConfig{
					Mode:    ACPModeSubcommand,
					Command: "acp",
				},
			},
			wantACP:  true,
			wantMode: ACPModeSubcommand,
			wantCmd:  "acp",
			wantArgs: nil,
		},
		{
			name: "flag mode - gemini --acp",
			rc: &RuntimeConfig{
				Command: "gemini",
				ACP: &ACPConfig{
					Mode: ACPModeFlag,
					Args: []string{"--acp"},
				},
			},
			wantACP:  true,
			wantMode: ACPModeFlag,
			wantCmd:  "",
			wantArgs: []string{"--acp"},
		},
		{
			name: "native mode with args",
			rc: &RuntimeConfig{
				Command: "claude-agent-acp",
				ACP: &ACPConfig{
					Mode: ACPModeNative,
					Args: []string{"--debug"},
				},
			},
			wantACP:  true,
			wantMode: ACPModeNative,
			wantCmd:  "",
			wantArgs: []string{"--debug"},
		},
		{
			name: "inherited ACP from preset",
			rc: &RuntimeConfig{
				Command: "opencode",
			},
			wantACP:  true,
			wantMode: "", // Default, treated as subcommand
			wantCmd:  "acp",
			wantArgs: nil,
		},
		{
			name: "no ACP support",
			rc: &RuntimeConfig{
				Command: "claude",
			},
			wantACP:  false,
			wantMode: "",
			wantCmd:  "",
			wantArgs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotACP := RuntimeConfigSupportsACP(tt.rc)
			if gotACP != tt.wantACP {
				t.Errorf("RuntimeConfigSupportsACP() = %v, want %v", gotACP, tt.wantACP)
			}

			if tt.wantACP {
				acpConfig := GetACPConfigFromRuntime(tt.rc)
				if acpConfig == nil {
					t.Fatalf("GetACPConfigFromRuntime() = nil, want config")
				}
				if acpConfig.Mode != tt.wantMode {
					t.Errorf("ACP.Mode = %q, want %q", acpConfig.Mode, tt.wantMode)
				}
				if acpConfig.Command != tt.wantCmd {
					t.Errorf("ACP.Command = %q, want %q", acpConfig.Command, tt.wantCmd)
				}
				if tt.wantArgs != nil {
					if len(acpConfig.Args) != len(tt.wantArgs) {
						t.Errorf("ACP.Args length = %d, want %d", len(acpConfig.Args), len(tt.wantArgs))
					} else {
						for i, arg := range tt.wantArgs {
							if acpConfig.Args[i] != arg {
								t.Errorf("ACP.Args[%d] = %q, want %q", i, acpConfig.Args[i], arg)
							}
						}
					}
				}
			} else {
				acpConfig := GetACPConfigFromRuntime(tt.rc)
				if acpConfig != nil {
					t.Errorf("GetACPConfigFromRuntime() = %+v, want nil", acpConfig)
				}
			}
		})
	}
}

// TestACPModeConstants verifies the ACP mode constants.
func TestACPModeConstants(t *testing.T) {
	if ACPModeNative != "native" {
		t.Errorf("ACPModeNative = %q, want native", ACPModeNative)
	}
	if ACPModeSubcommand != "subcommand" {
		t.Errorf("ACPModeSubcommand = %q, want subcommand", ACPModeSubcommand)
	}
	if ACPModeFlag != "flag" {
		t.Errorf("ACPModeFlag = %q, want flag", ACPModeFlag)
	}
}
