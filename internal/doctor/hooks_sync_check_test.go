package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/hooks"
)

// scaffoldWorkspace creates a minimal town workspace in a temp directory with
// the given role agents configured. Returns the town root path.
func scaffoldWorkspace(t *testing.T, roleAgents map[string]string) string {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Put dummy binaries for non-Claude role agents on PATH so agent
	// resolution doesn't fall back to claude when the binary is missing.
	if len(roleAgents) > 0 {
		binDir := filepath.Join(tmpDir, "bin")
		if err := os.MkdirAll(binDir, 0755); err != nil {
			t.Fatal(err)
		}
		for _, agent := range roleAgents {
			if agent != "" && agent != "claude" {
				if err := os.WriteFile(filepath.Join(binDir, agent), []byte("#!/bin/sh\n"), 0755); err != nil {
					t.Fatal(err)
				}
			}
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}

	townRoot := filepath.Join(tmpDir, "town")

	// Required workspace structure
	for _, dir := range []string{"mayor", "deacon"} {
		if err := os.MkdirAll(filepath.Join(townRoot, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// Workspace marker
	if err := os.WriteFile(
		filepath.Join(townRoot, "mayor", "town.json"),
		[]byte(`{"type":"town","version":1,"name":"test"}`),
		0644,
	); err != nil {
		t.Fatal(err)
	}

	// Town settings with role agents
	townSettings := config.NewTownSettings()
	townSettings.RoleAgents = roleAgents
	if err := os.MkdirAll(filepath.Join(townRoot, "settings"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveTownSettings(config.TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatal(err)
	}

	// Base hooks config (required for Claude targets)
	base := &hooks.HooksConfig{
		SessionStart: []hooks.HookEntry{
			{Matcher: "", Hooks: []hooks.Hook{{Type: "command", Command: "echo test"}}},
		},
	}
	if err := hooks.SaveBase(base); err != nil {
		t.Fatalf("SaveBase: %v", err)
	}

	return townRoot
}

// syncAllClaudeTargets creates in-sync .claude/settings.json for every
// Claude target that DiscoverTargets would find. This prevents false
// positives from unrelated Claude targets in template agent tests.
func syncAllClaudeTargets(t *testing.T, townRoot string) {
	t.Helper()
	targets, err := hooks.DiscoverTargets(townRoot)
	if err != nil {
		t.Fatalf("DiscoverTargets: %v", err)
	}
	for _, target := range targets {
		if target.Provider != "" && target.Provider != "claude" {
			continue
		}
		expected, err := hooks.ComputeExpected(target.Key)
		if err != nil {
			t.Fatalf("ComputeExpected(%s): %v", target.Key, err)
		}
		settings := &hooks.SettingsJSON{Hooks: *expected}
		data, err := hooks.MarshalSettings(settings)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(target.Path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target.Path, append(data, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHooksSyncCheck_ClaudeTargetInSync(t *testing.T) {
	townRoot := scaffoldWorkspace(t, nil)

	// Create a rig with a crew worktree
	worktree := filepath.Join(townRoot, "myrig", "crew", "alice")
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatal(err)
	}

	// Sync ALL Claude targets (mayor, deacon, crew worktree)
	syncAllClaudeTargets(t, townRoot)

	check := NewHooksSyncCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK for in-sync Claude targets, got %v: %s", result.Status, result.Message)
		for _, d := range result.Details {
			t.Logf("  detail: %s", d)
		}
	}
}

func TestHooksSyncCheck_ClaudeTargetMissingPromptDefaults(t *testing.T) {
	townRoot := scaffoldWorkspace(t, nil)
	syncAllClaudeTargets(t, townRoot)

	targetPath := filepath.Join(townRoot, "mayor", ".claude", "settings.json")
	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	delete(raw, "skipDangerousModePermissionPrompt")
	delete(raw, "hasCompletedOnboarding")
	delete(raw, "theme")
	delete(raw, "permissions")
	data, err = json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	check := NewHooksSyncCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning for missing prompt defaults, got %v: %s", result.Status, result.Message)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}
	check = NewHooksSyncCheck()
	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Fatalf("expected StatusOK after fix, got %v: %s", result.Status, result.Message)
	}
}

func TestHooksSyncCheck_TemplateAgent_InSync(t *testing.T) {
	townRoot := scaffoldWorkspace(t, map[string]string{"crew": "opencode"})

	// Create a crew worktree
	worktree := filepath.Join(townRoot, "myrig", "crew", "alice")
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatal(err)
	}

	// Sync Claude targets first
	syncAllClaudeTargets(t, townRoot)

	// Install the correct OpenCode template file
	expectedContent, err := hooks.ComputeExpectedTemplate("opencode", "gastown.js", "crew")
	if err != nil {
		t.Fatalf("ComputeExpectedTemplate: %v", err)
	}
	pluginDir := filepath.Join(worktree, ".opencode", "plugins")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "gastown.js"), expectedContent, 0644); err != nil {
		t.Fatal(err)
	}

	check := NewHooksSyncCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK for in-sync template agent, got %v: %s", result.Status, result.Message)
		for _, d := range result.Details {
			t.Logf("  detail: %s", d)
		}
	}
}

func TestHooksSyncCheck_TemplateAgent_OutOfSync(t *testing.T) {
	townRoot := scaffoldWorkspace(t, map[string]string{"crew": "opencode"})

	// Create a crew worktree with stale content
	worktree := filepath.Join(townRoot, "myrig", "crew", "alice")
	pluginDir := filepath.Join(worktree, ".opencode", "plugins")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "gastown.js"), []byte("// old stale content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Sync Claude targets so any Warning comes from the template agent
	syncAllClaudeTargets(t, townRoot)

	check := NewHooksSyncCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning for out-of-sync template agent, got %v: %s", result.Status, result.Message)
	}
}

func TestHooksSyncCheck_TemplateAgent_Missing(t *testing.T) {
	townRoot := scaffoldWorkspace(t, map[string]string{"crew": "opencode"})

	// Create a crew worktree but DON'T install the plugin
	worktree := filepath.Join(townRoot, "myrig", "crew", "alice")
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatal(err)
	}

	// Sync Claude targets
	syncAllClaudeTargets(t, townRoot)

	check := NewHooksSyncCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning for missing template agent file, got %v: %s", result.Status, result.Message)
	}
}

func TestHooksSyncCheck_Fix_TemplateAgent(t *testing.T) {
	townRoot := scaffoldWorkspace(t, map[string]string{"crew": "opencode"})

	// Create a crew worktree with stale content
	worktree := filepath.Join(townRoot, "myrig", "crew", "alice")
	pluginDir := filepath.Join(worktree, ".opencode", "plugins")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "gastown.js"), []byte("// stale"), 0644); err != nil {
		t.Fatal(err)
	}

	// Sync Claude targets
	syncAllClaudeTargets(t, townRoot)

	check := NewHooksSyncCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	// Run to detect out-of-sync
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning before fix, got %v", result.Status)
	}

	// Fix should write correct content
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix: %v", err)
	}

	// Verify file now matches expected template
	pluginPath := filepath.Join(pluginDir, "gastown.js")
	actual, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("reading fixed file: %v", err)
	}
	expected, err := hooks.ComputeExpectedTemplate("opencode", "gastown.js", "crew")
	if err != nil {
		t.Fatalf("ComputeExpectedTemplate: %v", err)
	}
	if string(actual) != string(expected) {
		t.Error("fixed file does not match expected template")
	}
}

func TestHooksSyncCheck_PolecatNestedWorktree_InSync(t *testing.T) {
	townRoot := scaffoldWorkspace(t, map[string]string{"polecat": "opencode"})

	worktree := filepath.Join(townRoot, "myrig", "polecats", "fury", "gastown")
	if err := os.MkdirAll(filepath.Join(worktree, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	syncAllClaudeTargets(t, townRoot)

	expectedContent, err := hooks.ComputeExpectedTemplate("opencode", "gastown.js", "polecat")
	if err != nil {
		t.Fatalf("ComputeExpectedTemplate: %v", err)
	}
	pluginDir := filepath.Join(worktree, ".opencode", "plugins")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "gastown.js"), expectedContent, 0644); err != nil {
		t.Fatal(err)
	}

	check := NewHooksSyncCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK for in-sync nested polecat worktree, got %v: %s", result.Status, result.Message)
		for _, d := range result.Details {
			t.Logf("  detail: %s", d)
		}
	}
}

func TestHooksSyncCheck_Fix_PreservesClaudePath(t *testing.T) {
	townRoot := scaffoldWorkspace(t, nil)

	// Sync all Claude targets first (creates in-sync settings for mayor, deacon)
	syncAllClaudeTargets(t, townRoot)

	// THEN overwrite mayor's settings with stale hooks but a custom editorMode
	mayorClaudeDir := filepath.Join(townRoot, "mayor", ".claude")
	stale := &hooks.SettingsJSON{
		EditorMode: "vim",
		Hooks: hooks.HooksConfig{
			SessionStart: []hooks.HookEntry{
				{Matcher: "", Hooks: []hooks.Hook{{Type: "command", Command: "old-cmd"}}},
			},
		},
	}
	data, err := hooks.MarshalSettings(stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mayorClaudeDir, "settings.json"), append(data, '\n'), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewHooksSyncCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	// Run to detect out-of-sync
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning before fix, got %v: %s", result.Status, result.Message)
	}

	// Fix
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix: %v", err)
	}

	// Verify editorMode was preserved (merge path, not overwrite)
	settings, err := hooks.LoadSettings(filepath.Join(mayorClaudeDir, "settings.json"))
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if settings.EditorMode != "vim" {
		t.Errorf("editorMode not preserved: got %q, want %q", settings.EditorMode, "vim")
	}
}
