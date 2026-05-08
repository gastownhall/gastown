package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/hooks"
)

func TestParseHooksFile(t *testing.T) {
	// Create a temp directory with a test settings file
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatalf("failed to create .claude dir: %v", err)
	}

	settings := hooks.SettingsJSON{
		Hooks: hooks.HooksConfig{
			SessionStart: []hooks.HookEntry{
				{
					Matcher: "",
					Hooks: []hooks.Hook{
						{Type: "command", Command: "gt prime"},
					},
				},
			},
			UserPromptSubmit: []hooks.HookEntry{
				{
					Matcher: "*.go",
					Hooks: []hooks.Hook{
						{Type: "command", Command: "go fmt"},
						{Type: "command", Command: "go vet"},
					},
				},
			},
		},
	}

	data, err := hooks.MarshalSettings(&settings)
	if err != nil {
		t.Fatalf("failed to marshal settings: %v", err)
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(settingsPath, data, 0644); err != nil {
		t.Fatalf("failed to write settings: %v", err)
	}

	// Parse the file
	hookInfos, err := parseHooksFile(settingsPath, "test/agent")
	if err != nil {
		t.Fatalf("parseHooksFile failed: %v", err)
	}

	// Verify results
	if len(hookInfos) != 2 {
		t.Errorf("expected 2 hooks, got %d", len(hookInfos))
	}

	// Find the SessionStart hook
	var sessionStart, userPrompt *HookInfo
	for i := range hookInfos {
		switch hookInfos[i].Type {
		case "SessionStart":
			sessionStart = &hookInfos[i]
		case "UserPromptSubmit":
			userPrompt = &hookInfos[i]
		}
	}

	if sessionStart == nil {
		t.Fatal("expected SessionStart hook")
	}
	if sessionStart.Agent != "test/agent" {
		t.Errorf("expected agent 'test/agent', got %q", sessionStart.Agent)
	}
	if len(sessionStart.Commands) != 1 || sessionStart.Commands[0] != "gt prime" {
		t.Errorf("unexpected SessionStart commands: %v", sessionStart.Commands)
	}

	if userPrompt == nil {
		t.Fatal("expected UserPromptSubmit hook")
	}
	if userPrompt.Matcher != "*.go" {
		t.Errorf("expected matcher '*.go', got %q", userPrompt.Matcher)
	}
	if len(userPrompt.Commands) != 2 {
		t.Errorf("expected 2 commands, got %d", len(userPrompt.Commands))
	}
}

func TestParseHooksFileMissing(t *testing.T) {
	// parseHooksFile now returns empty results for missing files (via LoadSettings),
	// not an error. This matches the updated semantics.
	infos, err := parseHooksFile("/nonexistent/settings.json", "test")
	if err != nil {
		t.Errorf("unexpected error for missing file: %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("expected 0 hooks for missing file, got %d", len(infos))
	}
}

func TestParseHooksFileInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	settingsPath := filepath.Join(tmpDir, "settings.json")

	if err := os.WriteFile(settingsPath, []byte("not json"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	_, err := parseHooksFile(settingsPath, "test")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestInstallHookToSerializesCorrectly(t *testing.T) {
	// Regression test: installHookTo must use hooks.MarshalSettings, not
	// json.MarshalIndent. SettingsJSON fields use json:"-" tags, so
	// encoding/json produces {} and silently clobbers hooks/plugins.
	tmpDir := t.TempDir()

	hookDef := HookDefinition{
		Event:    "SessionStart",
		Command:  "echo hello",
		Matchers: []string{""},
		Roles:    []string{"crew"},
		Enabled:  true,
	}

	err := installHookTo(tmpDir, hookDef, false)
	if err != nil {
		t.Fatalf("installHookTo failed: %v", err)
	}

	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("failed to read settings: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "hooks") {
		t.Error("installed settings.json missing 'hooks' key — likely serialized with json.MarshalIndent instead of hooks.MarshalSettings")
	}
	if !strings.Contains(content, "enabledPlugins") {
		t.Error("installed settings.json missing 'enabledPlugins' key")
	}
	if !strings.Contains(content, "echo hello") {
		t.Error("installed settings.json missing hook command")
	}
}

func TestParseHooksFileEmptyHooks(t *testing.T) {
	tmpDir := t.TempDir()
	settingsPath := filepath.Join(tmpDir, "settings.json")

	settings := hooks.SettingsJSON{}

	data, _ := json.Marshal(settings)
	if err := os.WriteFile(settingsPath, data, 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	hookInfos, err := parseHooksFile(settingsPath, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(hookInfos) != 0 {
		t.Errorf("expected 0 hooks, got %d", len(hookInfos))
	}
}

func TestDiscoverHooksCrewLevel(t *testing.T) {
	// Create a temp directory structure simulating a Gas Town workspace
	tmpDir := t.TempDir()

	// Create rig structure with shared crew and polecats settings at the parent level.
	// DiscoverTargets targets the shared parent directories (crew/.claude/settings.json),
	// not individual crew member or polecat worktree directories.
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	// Create shared crew settings (crew/.claude/settings.json)
	crewClaudeDir := filepath.Join(rigDir, "crew", ".claude")
	if err := os.MkdirAll(crewClaudeDir, 0755); err != nil {
		t.Fatalf("failed to create crew/.claude dir: %v", err)
	}

	crewSettings := hooks.SettingsJSON{
		Hooks: hooks.HooksConfig{
			SessionStart: []hooks.HookEntry{
				{
					Matcher: "",
					Hooks: []hooks.Hook{
						{Type: "command", Command: "crew-level-hook"},
					},
				},
			},
		},
	}
	crewData, _ := hooks.MarshalSettings(&crewSettings)
	if err := os.WriteFile(filepath.Join(crewClaudeDir, "settings.json"), crewData, 0644); err != nil {
		t.Fatalf("failed to write crew settings: %v", err)
	}

	// Create shared polecats settings (polecats/.claude/settings.json)
	polecatsClaudeDir := filepath.Join(rigDir, "polecats", ".claude")
	if err := os.MkdirAll(polecatsClaudeDir, 0755); err != nil {
		t.Fatalf("failed to create polecats/.claude dir: %v", err)
	}

	polecatsSettings := hooks.SettingsJSON{
		Hooks: hooks.HooksConfig{
			PreToolUse: []hooks.HookEntry{
				{
					Matcher: "",
					Hooks: []hooks.Hook{
						{Type: "command", Command: "polecats-level-hook"},
					},
				},
			},
		},
	}
	polecatsData, _ := hooks.MarshalSettings(&polecatsSettings)
	if err := os.WriteFile(filepath.Join(polecatsClaudeDir, "settings.json"), polecatsData, 0644); err != nil {
		t.Fatalf("failed to write polecats settings: %v", err)
	}

	// Discover hooks
	hookInfos, err := discoverHooks(tmpDir)
	if err != nil {
		t.Fatalf("discoverHooks failed: %v", err)
	}

	// Verify shared crew and polecats hooks were discovered
	var foundCrewLevel, foundPolecatsLevel bool
	for _, h := range hookInfos {
		if h.Agent == "testrig/crew" && len(h.Commands) > 0 && h.Commands[0] == "crew-level-hook" {
			foundCrewLevel = true
		}
		if h.Agent == "testrig/polecats" && len(h.Commands) > 0 && h.Commands[0] == "polecats-level-hook" {
			foundPolecatsLevel = true
		}
	}

	if !foundCrewLevel {
		t.Error("expected crew hook to be discovered (testrig/crew)")
	}
	if !foundPolecatsLevel {
		t.Error("expected polecats hook to be discovered (testrig/polecats)")
	}
}

func TestResolveSettingsTarget(t *testing.T) {
	townRoot := "/home/user/gt"

	tests := []struct {
		name     string
		cwd      string
		expected string
	}{
		{
			name:     "crew member worktree resolves to crew parent",
			cwd:      "/home/user/gt/myrig/crew/alice",
			expected: "/home/user/gt/myrig/crew",
		},
		{
			name:     "deeply nested crew path resolves to crew parent",
			cwd:      "/home/user/gt/myrig/crew/alice/src/pkg",
			expected: "/home/user/gt/myrig/crew",
		},
		{
			name:     "polecat worktree resolves to polecats parent",
			cwd:      "/home/user/gt/myrig/polecats/toast/myrig",
			expected: "/home/user/gt/myrig/polecats",
		},
		{
			name:     "witness subdir resolves to witness parent",
			cwd:      "/home/user/gt/myrig/witness/rig",
			expected: "/home/user/gt/myrig/witness",
		},
		{
			name:     "refinery subdir resolves to refinery parent",
			cwd:      "/home/user/gt/myrig/refinery/rig",
			expected: "/home/user/gt/myrig/refinery",
		},
		{
			name:     "mayor stays at cwd",
			cwd:      "/home/user/gt/mayor",
			expected: "/home/user/gt/mayor",
		},
		{
			name:     "deacon stays at cwd",
			cwd:      "/home/user/gt/deacon",
			expected: "/home/user/gt/deacon",
		},
		{
			name:     "town root stays at cwd",
			cwd:      "/home/user/gt",
			expected: "/home/user/gt",
		},
		{
			name:     "rig root stays at cwd",
			cwd:      "/home/user/gt/myrig",
			expected: "/home/user/gt/myrig",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.FromSlash(townRoot)
			cwd := filepath.FromSlash(tt.cwd)
			want := filepath.FromSlash(tt.expected)
			got := resolveSettingsTarget(root, cwd)
			if got != want {
				t.Errorf("resolveSettingsTarget(%q, %q) = %q, want %q", root, cwd, got, want)
			}
		})
	}
}

func TestHooksCmdHasVerboseAndJSONFlags(t *testing.T) {
	verboseFlag := hooksCmd.Flags().Lookup("verbose")
	if verboseFlag == nil {
		t.Fatal("hooksCmd missing --verbose flag")
	}
	if verboseFlag.Shorthand != "v" {
		t.Errorf("expected --verbose shorthand -v, got %q", verboseFlag.Shorthand)
	}

	jsonFlag := hooksCmd.Flags().Lookup("json")
	if jsonFlag == nil {
		t.Fatal("hooksCmd missing --json flag")
	}
}

func TestOutputHooksHumanVerbose(t *testing.T) {
	hookInfos := []HookInfo{
		{
			Type:     "SessionStart",
			Location: "/test/.claude/settings.json",
			Agent:    "gastown/polecats",
			Matcher:  "",
			Commands: []string{"gt prime"},
			Status:   "active",
		},
	}

	// Verify the function accepts and uses the verbose parameter without error.
	if err := outputHooksHuman("/test", hookInfos, false); err != nil {
		t.Errorf("outputHooksHuman(verbose=false) returned error: %v", err)
	}
	if err := outputHooksHuman("/test", hookInfos, true); err != nil {
		t.Errorf("outputHooksHuman(verbose=true) returned error: %v", err)
	}
}

func TestOutputHooksHumanEmpty(t *testing.T) {
	if err := outputHooksHuman("/test", nil, false); err != nil {
		t.Errorf("outputHooksHuman(empty) returned error: %v", err)
	}
}
