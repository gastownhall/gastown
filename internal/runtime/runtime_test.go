package runtime

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
)

type fakeStartupPromptSession struct {
	nudges    []string
	waitCalls int
	waitRC    *config.RuntimeConfig
	waitErr   error
	nudgeErr  error
}

func (f *fakeStartupPromptSession) NudgeSession(_ string, message string) error {
	if f.nudgeErr != nil {
		return f.nudgeErr
	}
	f.nudges = append(f.nudges, message)
	return nil
}

func (f *fakeStartupPromptSession) WaitForRuntimeReady(_ string, rc *config.RuntimeConfig, _ time.Duration) error {
	f.waitCalls++
	f.waitRC = rc
	return f.waitErr
}

func TestSessionIDFromEnv_Default(t *testing.T) {
	// Clear all environment variables
	oldGSEnv := os.Getenv("GT_SESSION_ID_ENV")
	oldClaudeID := os.Getenv("CLAUDE_SESSION_ID")
	defer func() {
		if oldGSEnv != "" {
			os.Setenv("GT_SESSION_ID_ENV", oldGSEnv)
		} else {
			os.Unsetenv("GT_SESSION_ID_ENV")
		}
		if oldClaudeID != "" {
			os.Setenv("CLAUDE_SESSION_ID", oldClaudeID)
		} else {
			os.Unsetenv("CLAUDE_SESSION_ID")
		}
	}()
	os.Unsetenv("GT_SESSION_ID_ENV")
	os.Unsetenv("CLAUDE_SESSION_ID")

	result := SessionIDFromEnv()
	if result != "" {
		t.Errorf("SessionIDFromEnv() with no env vars should return empty, got %q", result)
	}
}

func TestSessionIDFromEnv_ClaudeSessionID(t *testing.T) {
	oldGSEnv := os.Getenv("GT_SESSION_ID_ENV")
	oldClaudeID := os.Getenv("CLAUDE_SESSION_ID")
	defer func() {
		if oldGSEnv != "" {
			os.Setenv("GT_SESSION_ID_ENV", oldGSEnv)
		} else {
			os.Unsetenv("GT_SESSION_ID_ENV")
		}
		if oldClaudeID != "" {
			os.Setenv("CLAUDE_SESSION_ID", oldClaudeID)
		} else {
			os.Unsetenv("CLAUDE_SESSION_ID")
		}
	}()

	os.Unsetenv("GT_SESSION_ID_ENV")
	os.Setenv("CLAUDE_SESSION_ID", "test-session-123")

	result := SessionIDFromEnv()
	if result != "test-session-123" {
		t.Errorf("SessionIDFromEnv() = %q, want %q", result, "test-session-123")
	}
}

func TestSessionIDFromEnv_CustomEnvVar(t *testing.T) {
	oldGSEnv := os.Getenv("GT_SESSION_ID_ENV")
	oldCustomID := os.Getenv("CUSTOM_SESSION_ID")
	oldClaudeID := os.Getenv("CLAUDE_SESSION_ID")
	defer func() {
		if oldGSEnv != "" {
			os.Setenv("GT_SESSION_ID_ENV", oldGSEnv)
		} else {
			os.Unsetenv("GT_SESSION_ID_ENV")
		}
		if oldCustomID != "" {
			os.Setenv("CUSTOM_SESSION_ID", oldCustomID)
		} else {
			os.Unsetenv("CUSTOM_SESSION_ID")
		}
		if oldClaudeID != "" {
			os.Setenv("CLAUDE_SESSION_ID", oldClaudeID)
		} else {
			os.Unsetenv("CLAUDE_SESSION_ID")
		}
	}()

	os.Setenv("GT_SESSION_ID_ENV", "CUSTOM_SESSION_ID")
	os.Setenv("CUSTOM_SESSION_ID", "custom-session-456")
	os.Setenv("CLAUDE_SESSION_ID", "claude-session-789")

	result := SessionIDFromEnv()
	if result != "custom-session-456" {
		t.Errorf("SessionIDFromEnv() with custom env = %q, want %q", result, "custom-session-456")
	}
}

func TestStartupFallbackCommands_NoHooks(t *testing.T) {
	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider: "none",
		},
	}

	commands := StartupFallbackCommands("polecat", rc)
	if commands == nil {
		t.Error("StartupFallbackCommands() with no hooks should return commands")
	}
	if len(commands) == 0 {
		t.Error("StartupFallbackCommands() should return at least one command")
	}
}

func TestStartupFallbackCommands_WithHooks(t *testing.T) {
	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider: "claude",
		},
	}

	commands := StartupFallbackCommands("polecat", rc)
	if commands != nil {
		t.Error("StartupFallbackCommands() with hooks provider should return nil")
	}
}

func TestStartupFallbackCommands_NilConfig(t *testing.T) {
	// Nil config defaults to claude provider, which has hooks
	// So it returns nil (no fallback commands needed)
	commands := StartupFallbackCommands("polecat", nil)
	if commands != nil {
		t.Error("StartupFallbackCommands() with nil config should return nil (defaults to claude with hooks)")
	}
}

func TestStartupFallbackCommands_AutonomousRole(t *testing.T) {
	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider: "none",
		},
	}

	autonomousRoles := []string{"polecat", "witness", "refinery", "deacon"}
	for _, role := range autonomousRoles {
		t.Run(role, func(t *testing.T) {
			commands := StartupFallbackCommands(role, rc)
			if commands == nil || len(commands) == 0 {
				t.Error("StartupFallbackCommands() should return commands for autonomous role")
			}
			// Should contain mail check
			found := false
			for _, cmd := range commands {
				if contains(cmd, "mail check --inject") {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Commands for %s should contain mail check --inject", role)
			}
		})
	}
}

func TestStartupFallbackCommands_NonAutonomousRole(t *testing.T) {
	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider: "none",
		},
	}

	nonAutonomousRoles := []string{"mayor", "crew", "keeper"}
	for _, role := range nonAutonomousRoles {
		t.Run(role, func(t *testing.T) {
			commands := StartupFallbackCommands(role, rc)
			if commands == nil || len(commands) == 0 {
				t.Error("StartupFallbackCommands() should return commands for non-autonomous role")
			}
			// Should NOT contain mail check
			for _, cmd := range commands {
				if contains(cmd, "mail check --inject") {
					t.Errorf("Commands for %s should NOT contain mail check --inject", role)
				}
			}
		})
	}
}

func TestStartupFallbackCommands_RoleCasing(t *testing.T) {
	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider: "none",
		},
	}

	// Role should be lowercased internally
	commands := StartupFallbackCommands("POLECAT", rc)
	if commands == nil {
		t.Error("StartupFallbackCommands() should handle uppercase role")
	}
}

func TestEnsureSettingsForRole_NilConfig(t *testing.T) {
	// Should not panic with nil config
	err := EnsureSettingsForRole("/tmp/test", "/tmp/test", "polecat", nil)
	if err != nil {
		t.Errorf("EnsureSettingsForRole() with nil config should not error, got %v", err)
	}
}

func TestEnsureSettingsForRole_NilHooks(t *testing.T) {
	rc := &config.RuntimeConfig{
		Hooks: nil,
	}

	err := EnsureSettingsForRole("/tmp/test", "/tmp/test", "polecat", rc)
	if err != nil {
		t.Errorf("EnsureSettingsForRole() with nil hooks should not error, got %v", err)
	}
}

func TestEnsureSettingsForRole_UnknownProvider(t *testing.T) {
	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider: "unknown",
		},
	}

	err := EnsureSettingsForRole("/tmp/test", "/tmp/test", "polecat", rc)
	if err != nil {
		t.Errorf("EnsureSettingsForRole() with unknown provider should not error, got %v", err)
	}
}

func TestEnsureSettingsForRole_OpenCodeUsesWorkDir(t *testing.T) {
	// OpenCode plugins must be installed in workDir (not settingsDir) because
	// OpenCode has no --settings equivalent for path redirection.
	settingsDir := t.TempDir()
	workDir := t.TempDir()

	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider:     "opencode",
			Dir:          "plugins",
			SettingsFile: "gastown.js",
		},
	}

	err := EnsureSettingsForRole(settingsDir, workDir, "crew", rc)
	if err != nil {
		t.Fatalf("EnsureSettingsForRole() error = %v", err)
	}

	// Plugin should be in workDir, not settingsDir
	if _, err := os.Stat(settingsDir + "/plugins/gastown.js"); err == nil {
		t.Error("OpenCode plugin should NOT be in settingsDir")
	}
	if _, err := os.Stat(workDir + "/plugins/gastown.js"); err != nil {
		t.Error("OpenCode plugin should be in workDir")
	}
}

func TestEnsureSettingsForRole_ClaudeUsesSettingsDir(t *testing.T) {
	// Claude settings must be installed in settingsDir (passed via --settings flag).
	settingsDir := t.TempDir()
	workDir := t.TempDir()

	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider:     "claude",
			Dir:          ".claude",
			SettingsFile: "settings.json",
		},
	}

	err := EnsureSettingsForRole(settingsDir, workDir, "crew", rc)
	if err != nil {
		t.Fatalf("EnsureSettingsForRole() error = %v", err)
	}

	// Settings should be in settingsDir, not workDir
	if _, err := os.Stat(settingsDir + "/.claude/settings.json"); err != nil {
		t.Error("Claude settings should be in settingsDir")
	}
	if _, err := os.Stat(workDir + "/.claude/settings.json"); err == nil {
		t.Error("Claude settings should NOT be in workDir when dirs differ")
	}
}

func TestEnsureSettingsForRole_SkipsCommandsWhenAncestorHasThem(t *testing.T) {
	// When workDir is nested under a directory that already has commands provisioned
	// (e.g. mayor/ inside town root), EnsureSettingsForRole must NOT write a duplicate
	// copy. Claude Code inherits from all ancestor .claude/commands/ dirs.
	townRoot := t.TempDir()
	workDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Simulate town root having commands already (as ProvisionCommands would do).
	townCommandsDir := filepath.Join(townRoot, ".claude", "commands")
	if err := os.MkdirAll(townCommandsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townCommandsDir, "done.md"), []byte("---\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}

	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider:     "claude",
			Dir:          ".claude",
			SettingsFile: "settings.json",
		},
	}

	if err := EnsureSettingsForRole(workDir, workDir, "mayor", rc); err != nil {
		t.Fatalf("EnsureSettingsForRole() error = %v", err)
	}

	// Commands must NOT be provisioned into mayor/.claude/commands/ because the
	// town root's .claude/commands/ is inherited via directory traversal.
	mayorCommandsDir := filepath.Join(workDir, ".claude", "commands")
	if _, err := os.Stat(mayorCommandsDir); err == nil {
		t.Error("EnsureSettingsForRole should not provision commands when ancestor already has them")
	}
}

func TestGetStartupFallbackInfo_HooksWithPrompt(t *testing.T) {
	// Claude: hooks enabled, prompt mode "arg"
	rc := &config.RuntimeConfig{
		PromptMode: "arg",
		Hooks: &config.RuntimeHooksConfig{
			Provider: "claude",
		},
	}

	info := GetStartupFallbackInfo(rc)
	if info.IncludePrimeInBeacon {
		t.Error("Hooks+Prompt should NOT include prime instruction in beacon")
	}
	if info.SendStartupNudge {
		t.Error("Hooks+Prompt should NOT need startup nudge (beacon has it)")
	}
}

func TestGetStartupFallbackInfo_HooksNoPrompt(t *testing.T) {
	// Hypothetical agent: hooks enabled but no prompt support
	rc := &config.RuntimeConfig{
		PromptMode: "none",
		Hooks: &config.RuntimeHooksConfig{
			Provider: "claude",
		},
	}

	info := GetStartupFallbackInfo(rc)
	if info.IncludePrimeInBeacon {
		t.Error("Hooks+NoPrompt should NOT include prime instruction (hooks run it)")
	}
	if !info.SendStartupNudge {
		t.Error("Hooks+NoPrompt should need startup nudge (no prompt to include it)")
	}
	if info.StartupNudgeDelayMs != 0 {
		t.Error("Hooks+NoPrompt should NOT wait (hooks already ran gt prime)")
	}
}

func TestGetStartupFallbackInfo_NoHooksWithPrompt(t *testing.T) {
	// Codex: no hooks, but has prompt support
	rc := &config.RuntimeConfig{
		PromptMode: "arg",
		Hooks: &config.RuntimeHooksConfig{
			Provider: "none",
		},
	}

	info := GetStartupFallbackInfo(rc)
	if !info.IncludePrimeInBeacon {
		t.Error("NoHooks+Prompt should include prime instruction in beacon")
	}
	if !info.SendStartupNudge {
		t.Error("NoHooks+Prompt should need startup nudge")
	}
	if info.StartupNudgeDelayMs <= 0 {
		t.Error("NoHooks+Prompt should wait for gt prime to complete")
	}
}

func TestGetStartupFallbackInfo_NoHooksNoPrompt(t *testing.T) {
	// Auggie/AMP: no hooks, no prompt support
	rc := &config.RuntimeConfig{
		PromptMode: "none",
		Hooks: &config.RuntimeHooksConfig{
			Provider: "none",
		},
	}

	info := GetStartupFallbackInfo(rc)
	if !info.IncludePrimeInBeacon {
		t.Error("NoHooks+NoPrompt should include prime instruction")
	}
	if !info.SendStartupNudge {
		t.Error("NoHooks+NoPrompt should need startup nudge")
	}
	if info.StartupNudgeDelayMs <= 0 {
		t.Error("NoHooks+NoPrompt should wait for gt prime to complete")
	}
	if !info.SendBeaconNudge {
		t.Error("NoHooks+NoPrompt should send beacon via nudge (no prompt)")
	}
}

func TestGetStartupFallbackInfo_NilConfig(t *testing.T) {
	// Nil config defaults to Claude (hooks enabled, prompt "arg")
	info := GetStartupFallbackInfo(nil)
	if info.IncludePrimeInBeacon {
		t.Error("Nil config (defaults to Claude) should NOT include prime instruction")
	}
	if info.SendStartupNudge {
		t.Error("Nil config (defaults to Claude) should NOT need startup nudge")
	}
}

func TestStartupNudgeContent(t *testing.T) {
	content := StartupNudgeContent()
	if content == "" {
		t.Error("StartupNudgeContent should return non-empty string")
	}
	if !contains(content, "gt hook") {
		t.Error("StartupNudgeContent should mention gt hook")
	}
}

func TestGetStartupPromptFallback_NoHooksNoPrompt(t *testing.T) {
	rc := &config.RuntimeConfig{
		PromptMode: "none",
		Hooks: &config.RuntimeHooksConfig{
			Provider: "none",
		},
	}

	fallback := GetStartupPromptFallback(rc)
	if !fallback.Send {
		t.Error("NoHooks+NoPrompt should nudge the startup prompt")
	}
	if fallback.DelayMs <= 0 {
		t.Error("NoHooks+NoPrompt should wait for gt prime before nudging the startup prompt")
	}
}

func TestGetStartupPromptFallback_WithPrompt(t *testing.T) {
	rc := &config.RuntimeConfig{
		PromptMode: "arg",
		Hooks: &config.RuntimeHooksConfig{
			Provider: "none",
		},
	}

	fallback := GetStartupPromptFallback(rc)
	if fallback.Send {
		t.Error("Prompt-capable runtimes should not need a startup prompt nudge")
	}
	if fallback.DelayMs != DefaultPrimeWaitMs {
		t.Errorf("DelayMs = %d, want %d", fallback.DelayMs, DefaultPrimeWaitMs)
	}
}

func TestDeliverStartupPromptFallback_NoPromptWaitsAndNudges(t *testing.T) {
	rc := &config.RuntimeConfig{
		PromptMode: "none",
		Hooks: &config.RuntimeHooksConfig{
			Provider: "none",
		},
		Tmux: &config.RuntimeTmuxConfig{
			ReadyPromptPrefix: "should-be-cleared",
			ReadyDelayMs:      100,
		},
	}
	tm := &fakeStartupPromptSession{}

	err := DeliverStartupPromptFallback(tm, "sess-1", "begin patrol", rc, 30*time.Second)
	if err != nil {
		t.Fatalf("DeliverStartupPromptFallback() error = %v", err)
	}
	if tm.waitCalls != 1 {
		t.Fatalf("waitCalls = %d, want 1", tm.waitCalls)
	}
	if tm.waitRC == nil || tm.waitRC.Tmux == nil {
		t.Fatalf("waitRC missing tmux config: %#v", tm.waitRC)
	}
	if tm.waitRC.Tmux.ReadyPromptPrefix != "" {
		t.Fatalf("ReadyPromptPrefix = %q, want empty", tm.waitRC.Tmux.ReadyPromptPrefix)
	}
	if tm.waitRC.Tmux.ReadyDelayMs < DefaultPrimeWaitMs {
		t.Fatalf("ReadyDelayMs = %d, want >= %d", tm.waitRC.Tmux.ReadyDelayMs, DefaultPrimeWaitMs)
	}
	if len(tm.nudges) != 1 || tm.nudges[0] != "begin patrol" {
		t.Fatalf("nudges = %#v, want [\"begin patrol\"]", tm.nudges)
	}
}

func TestDeliverStartupPromptFallback_WithPromptNoOp(t *testing.T) {
	rc := &config.RuntimeConfig{
		PromptMode: "arg",
		Hooks: &config.RuntimeHooksConfig{
			Provider: "none",
		},
	}
	tm := &fakeStartupPromptSession{}

	err := DeliverStartupPromptFallback(tm, "sess-1", "begin patrol", rc, 30*time.Second)
	if err != nil {
		t.Fatalf("DeliverStartupPromptFallback() error = %v", err)
	}
	if tm.waitCalls != 0 {
		t.Fatalf("waitCalls = %d, want 0", tm.waitCalls)
	}
	if len(tm.nudges) != 0 {
		t.Fatalf("nudges = %#v, want none", tm.nudges)
	}
}

func TestDeliverStartupPromptFallback_WaitError(t *testing.T) {
	rc := &config.RuntimeConfig{
		PromptMode: "none",
		Hooks: &config.RuntimeHooksConfig{
			Provider: "none",
		},
	}
	tm := &fakeStartupPromptSession{waitErr: os.ErrDeadlineExceeded}

	err := DeliverStartupPromptFallback(tm, "sess-1", "begin patrol", rc, 30*time.Second)
	if err == nil {
		t.Fatal("DeliverStartupPromptFallback() error = nil, want non-nil")
	}
	if len(tm.nudges) != 0 {
		t.Fatalf("nudges = %#v, want none after wait failure", tm.nudges)
	}
}

func TestEnsureSettingsForRole_CopilotUsesWorkDir(t *testing.T) {
	// Copilot instructions must be installed in workDir (not settingsDir) because
	// Copilot has no --settings equivalent for path redirection.
	settingsDir := t.TempDir()
	workDir := t.TempDir()

	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider:     "copilot",
			Dir:          ".copilot",
			SettingsFile: "copilot-instructions.md",
		},
	}

	err := EnsureSettingsForRole(settingsDir, workDir, "crew", rc)
	if err != nil {
		t.Fatalf("EnsureSettingsForRole() error = %v", err)
	}

	// Instructions should be in workDir, not settingsDir
	if _, err := os.Stat(settingsDir + "/.copilot/copilot-instructions.md"); err == nil {
		t.Error("Copilot instructions should NOT be in settingsDir")
	}
	if _, err := os.Stat(workDir + "/.copilot/copilot-instructions.md"); err != nil {
		t.Error("Copilot instructions should be in workDir")
	}
}

func TestEnsureSettingsForRole_CursorUsesWorkDir(t *testing.T) {
	// Cursor hooks.json is installed under workDir (HooksUseSettingsDir false for cursor preset).
	settingsDir := t.TempDir()
	workDir := t.TempDir()

	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider:     "cursor",
			Dir:          ".cursor",
			SettingsFile: "hooks.json",
		},
	}

	err := EnsureSettingsForRole(settingsDir, workDir, "crew", rc)
	if err != nil {
		t.Fatalf("EnsureSettingsForRole() error = %v", err)
	}

	if _, err := os.Stat(settingsDir + "/.cursor/hooks.json"); err == nil {
		t.Error("Cursor hooks should NOT be in settingsDir")
	}
	if _, err := os.Stat(workDir + "/.cursor/hooks.json"); err != nil {
		t.Error("Cursor hooks should be in workDir")
	}
}

func TestGetStartupFallbackInfo_InformationalHooks(t *testing.T) {
	// Copilot: hooks provider set but informational (instructions file, not executable).
	// Should be treated as having NO hooks for startup fallback purposes.
	rc := &config.RuntimeConfig{
		PromptMode: "arg",
		Hooks: &config.RuntimeHooksConfig{
			Provider:      "copilot",
			Informational: true,
		},
	}

	info := GetStartupFallbackInfo(rc)
	if !info.IncludePrimeInBeacon {
		t.Error("Informational hooks should include prime instruction in beacon")
	}
	if !info.SendStartupNudge {
		t.Error("Informational hooks should need startup nudge")
	}
	if info.SendBeaconNudge {
		t.Error("Informational hooks with prompt should NOT need beacon nudge")
	}
}

func TestStartupFallbackCommands_InformationalHooks(t *testing.T) {
	// Copilot has hooks provider set but informational — should still get fallback commands.
	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider:      "copilot",
			Informational: true,
		},
	}

	commands := StartupFallbackCommands("polecat", rc)
	if commands == nil {
		t.Error("StartupFallbackCommands() with informational hooks should return commands")
	}
}

func TestEnsureSettingsForRole_GeminiUsesWorkDir(t *testing.T) {
	// Gemini CLI has no --settings flag; settings must go to workDir (like OpenCode).
	settingsDir := t.TempDir()
	workDir := t.TempDir()

	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider:     "gemini",
			Dir:          ".gemini",
			SettingsFile: "settings.json",
		},
	}

	err := EnsureSettingsForRole(settingsDir, workDir, "crew", rc)
	if err != nil {
		t.Fatalf("EnsureSettingsForRole() error = %v", err)
	}

	// Settings should be in workDir, not settingsDir
	if _, err := os.Stat(settingsDir + "/.gemini/settings.json"); err == nil {
		t.Error("Gemini settings should NOT be in settingsDir")
	}
	if _, err := os.Stat(workDir + "/.gemini/settings.json"); err != nil {
		t.Error("Gemini settings should be in workDir")
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && findSubstring(s, substr)
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			if s[i+j] != substr[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestRuntimeConfigWithMinDelay_NilConfig(t *testing.T) {
	result := RuntimeConfigWithMinDelay(nil, 3000)
	if result == nil {
		t.Fatal("RuntimeConfigWithMinDelay(nil) should return non-nil config")
	}
	if result.Tmux == nil {
		t.Fatal("RuntimeConfigWithMinDelay(nil) should have Tmux config")
	}
	if result.Tmux.ReadyDelayMs != 3000 {
		t.Errorf("ReadyDelayMs = %d, want 3000", result.Tmux.ReadyDelayMs)
	}
}

func TestRuntimeConfigWithMinDelay_NilTmux(t *testing.T) {
	rc := &config.RuntimeConfig{PromptMode: "arg"}
	result := RuntimeConfigWithMinDelay(rc, 2000)
	if result.Tmux == nil {
		t.Fatal("should have Tmux config")
	}
	if result.Tmux.ReadyDelayMs != 2000 {
		t.Errorf("ReadyDelayMs = %d, want 2000", result.Tmux.ReadyDelayMs)
	}
	// Original should be unmodified
	if rc.Tmux != nil {
		t.Error("original config should not be modified")
	}
}

func TestRuntimeConfigWithMinDelay_BelowMin(t *testing.T) {
	rc := &config.RuntimeConfig{
		Tmux: &config.RuntimeTmuxConfig{
			ReadyDelayMs:      500,
			ReadyPromptPrefix: "❯ ",
		},
	}
	result := RuntimeConfigWithMinDelay(rc, 2000)
	if result.Tmux.ReadyDelayMs != 2000 {
		t.Errorf("ReadyDelayMs = %d, want 2000 (should be raised to min)", result.Tmux.ReadyDelayMs)
	}
	// ReadyPromptPrefix should be cleared to force delay-based path
	if result.Tmux.ReadyPromptPrefix != "" {
		t.Errorf("ReadyPromptPrefix = %q, want empty (should be cleared to force delay path)", result.Tmux.ReadyPromptPrefix)
	}
	// Original should be unmodified
	if rc.Tmux.ReadyDelayMs != 500 {
		t.Errorf("original ReadyDelayMs = %d, want 500 (should not be modified)", rc.Tmux.ReadyDelayMs)
	}
	if rc.Tmux.ReadyPromptPrefix != "❯ " {
		t.Error("original ReadyPromptPrefix should not be modified")
	}
}

func TestRuntimeConfigWithMinDelay_AboveMin(t *testing.T) {
	rc := &config.RuntimeConfig{
		Tmux: &config.RuntimeTmuxConfig{
			ReadyDelayMs: 5000,
		},
	}
	result := RuntimeConfigWithMinDelay(rc, 2000)
	if result.Tmux.ReadyDelayMs != 5000 {
		t.Errorf("ReadyDelayMs = %d, want 5000 (should not be lowered)", result.Tmux.ReadyDelayMs)
	}
}

func TestRuntimeConfigWithMinDelay_ZeroMin(t *testing.T) {
	rc := &config.RuntimeConfig{
		Tmux: &config.RuntimeTmuxConfig{
			ReadyDelayMs: 0,
		},
	}
	result := RuntimeConfigWithMinDelay(rc, 0)
	if result.Tmux.ReadyDelayMs != 0 {
		t.Errorf("ReadyDelayMs = %d, want 0", result.Tmux.ReadyDelayMs)
	}
}
