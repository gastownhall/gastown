package runtime

import (
	"os"
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

	autonomousRoles := []string{"polecat"}
	for _, role := range autonomousRoles {
		t.Run(role, func(t *testing.T) {
			commands := StartupFallbackCommands(role, rc)
			if commands == nil || len(commands) == 0 {
				t.Error("StartupFallbackCommands() should return commands for autonomous role")
			}
			for _, cmd := range commands {
				if cmd != "gt prime" {
					t.Fatalf("Commands for %s = %q, want gt prime", role, cmd)
				}
			}
		})
	}
}

func TestStartupFallbackCommands_PatrolRolesSkipMailInject(t *testing.T) {
	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider: "none",
		},
	}

	for _, role := range []string{"witness", "refinery", "deacon", "boot", "deacon/boot"} {
		t.Run(role, func(t *testing.T) {
			commands := StartupFallbackCommands(role, rc)
			if commands == nil || len(commands) == 0 {
				t.Fatal("StartupFallbackCommands() should return commands for patrol role")
			}
			for _, cmd := range commands {
				if contains(cmd, "mail check --inject") {
					t.Fatalf("patrol role %s should not contain startup mail check: %q", role, cmd)
				}
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
	dir := t.TempDir()
	err := EnsureSettingsForRole(dir, dir, "polecat", nil)
	if err != nil {
		t.Errorf("EnsureSettingsForRole() with nil config should not error, got %v", err)
	}
}

func TestEnsureSettingsForRole_NilHooks(t *testing.T) {
	dir := t.TempDir()
	rc := &config.RuntimeConfig{
		Hooks: nil,
	}

	err := EnsureSettingsForRole(dir, dir, "polecat", rc)
	if err != nil {
		t.Errorf("EnsureSettingsForRole() with nil hooks should not error, got %v", err)
	}
}

func TestEnsureSettingsForRole_UnknownProvider(t *testing.T) {
	dir := t.TempDir()
	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider: "unknown",
		},
	}

	err := EnsureSettingsForRole(dir, dir, "polecat", rc)
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
	if err := os.WriteFile(workDir+"/AGENTS.md", []byte("# Agents\n"), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

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
	target, err := os.Readlink(workDir + "/GEMINI.md")
	if err != nil {
		t.Fatalf("Gemini context symlink should exist in workDir: %v", err)
	}
	if target != "AGENTS.md" {
		t.Errorf("GEMINI.md target = %q, want AGENTS.md", target)
	}
}

func TestEnsureSettingsForRole_GeminiRepairsBrokenContextSymlink(t *testing.T) {
	settingsDir := t.TempDir()
	workDir := t.TempDir()
	if err := os.WriteFile(workDir+"/AGENTS.md", []byte("# Agents\n"), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	if err := os.Symlink("./rig/worktree/AGENTS.md", workDir+"/GEMINI.md"); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider:     "gemini",
			Dir:          ".gemini",
			SettingsFile: "settings.json",
		},
	}

	if err := EnsureSettingsForRole(settingsDir, workDir, "witness", rc); err != nil {
		t.Fatalf("EnsureSettingsForRole() error = %v", err)
	}

	target, err := os.Readlink(workDir + "/GEMINI.md")
	if err != nil {
		t.Fatalf("read repaired GEMINI.md symlink: %v", err)
	}
	if target != "AGENTS.md" {
		t.Errorf("GEMINI.md target = %q, want AGENTS.md", target)
	}
}

func TestEnsureSettingsForRole_GeminiRepairsResolvableAgentsSymlink(t *testing.T) {
	settingsDir := t.TempDir()
	workDir := t.TempDir()
	if err := os.WriteFile(workDir+"/AGENTS.md", []byte("# Agents\n"), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	otherDir := t.TempDir()
	otherAgents := otherDir + "/AGENTS.md"
	if err := os.WriteFile(otherAgents, []byte("# Other Agents\n"), 0644); err != nil {
		t.Fatalf("write other AGENTS.md: %v", err)
	}
	if err := os.Symlink(otherAgents, workDir+"/GEMINI.md"); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider:     "gemini",
			Dir:          ".gemini",
			SettingsFile: "settings.json",
		},
	}

	if err := EnsureSettingsForRole(settingsDir, workDir, "witness", rc); err != nil {
		t.Fatalf("EnsureSettingsForRole() error = %v", err)
	}

	target, err := os.Readlink(workDir + "/GEMINI.md")
	if err != nil {
		t.Fatalf("read repaired GEMINI.md symlink: %v", err)
	}
	if target != "AGENTS.md" {
		t.Errorf("GEMINI.md target = %q, want AGENTS.md", target)
	}
}

func TestEnsureSettingsForRole_GeminiPreservesGeminiOverlay(t *testing.T) {
	settingsDir := t.TempDir()
	workDir := t.TempDir()
	geminiContent := []byte("# Gemini overlay\n")
	if err := os.WriteFile(workDir+"/AGENTS.md", []byte("# Agents\n"), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	if err := os.WriteFile(workDir+"/GEMINI.md", geminiContent, 0644); err != nil {
		t.Fatalf("write GEMINI.md: %v", err)
	}

	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider:     "gemini",
			Dir:          ".gemini",
			SettingsFile: "settings.json",
		},
	}

	if err := EnsureSettingsForRole(settingsDir, workDir, "crew", rc); err != nil {
		t.Fatalf("EnsureSettingsForRole() error = %v", err)
	}

	got, err := os.ReadFile(workDir + "/GEMINI.md")
	if err != nil {
		t.Fatalf("read GEMINI.md: %v", err)
	}
	if string(got) != string(geminiContent) {
		t.Errorf("GEMINI.md content = %q, want %q", got, geminiContent)
	}
}

func TestEnsureSettingsForRole_GeminiNoAgentsMDNoops(t *testing.T) {
	settingsDir := t.TempDir()
	workDir := t.TempDir()

	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider:     "gemini",
			Dir:          ".gemini",
			SettingsFile: "settings.json",
		},
	}

	if err := EnsureSettingsForRole(settingsDir, workDir, "crew", rc); err != nil {
		t.Fatalf("EnsureSettingsForRole() error = %v", err)
	}
	if _, err := os.Lstat(workDir + "/GEMINI.md"); !os.IsNotExist(err) {
		t.Fatalf("GEMINI.md should not be created without AGENTS.md, err = %v", err)
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

func makeTownRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(root+"/mayor", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"/mayor/town.json", []byte(`{"type":"town"}`), 0644); err != nil {
		t.Fatal(err)
	}
	return root
}

func makeTownRootWithGit(t *testing.T) string {
	t.Helper()
	root := makeTownRoot(t)
	if err := os.MkdirAll(root+"/.git", 0755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestCommandsInherited_WorkDirIsNestedInTownRoot(t *testing.T) {
	// workDir is a subdirectory of the town root (same git repo) → inherited
	root := makeTownRootWithGit(t)
	mayorDir := root + "/mayor"

	if !commandsInherited(mayorDir) {
		t.Error("commandsInherited() = false, want true for workDir nested inside town root")
	}
}

func TestCommandsInherited_WorkDirIsTownRoot(t *testing.T) {
	// workDir == git root → not inherited (we're provisioning at the root itself)
	root := makeTownRootWithGit(t)

	if commandsInherited(root) {
		t.Error("commandsInherited() = true, want false when workDir equals the git root")
	}
}

func TestCommandsInherited_WorkDirNestedInTownRootBeforeGitInit(t *testing.T) {
	// gt install creates mayor/deacon settings before it initializes town .git.
	// Those role dirs still inherit town-level commands once install provisions them.
	root := makeTownRoot(t)
	mayorDir := root + "/mayor"

	if !commandsInherited(mayorDir) {
		t.Error("commandsInherited() = false, want true for town role dir before .git exists")
	}
}

func TestCommandsInherited_NestedGitRepoInsideTownRoot(t *testing.T) {
	// Crew/polecat workdirs live in nested git repos under the town root. Claude
	// Code stops at that repo boundary, so they need explicit command provisioning.
	root := makeTownRootWithGit(t)
	workDir := root + "/rig/polecats/chrome/repo"
	if err := os.MkdirAll(workDir+"/.git", 0755); err != nil {
		t.Fatal(err)
	}

	if commandsInherited(workDir) {
		t.Error("commandsInherited() = true, want false for nested git repo inside town root")
	}
}

func TestCommandsInherited_WorkDirIsOutsideTownRoot(t *testing.T) {
	// workDir in a standalone git repo that is NOT a Gas Town workspace → not inherited
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/.git", 0755); err != nil {
		t.Fatal(err)
	}
	subDir := dir + "/src"
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	if commandsInherited(subDir) {
		t.Error("commandsInherited() = true, want false for workDir in non-workspace git repo")
	}
}

func TestCommandsInherited_NoGitRoot(t *testing.T) {
	// workDir has no .git ancestor → not inherited
	dir := t.TempDir()
	// Don't create .git

	if commandsInherited(dir) {
		t.Error("commandsInherited() = true, want false when no .git ancestor found")
	}
}

func TestEnsureSettingsForRole_SkipsCommandsWhenInheritedFromTownRoot(t *testing.T) {
	// Mayor/deacon run inside the town root git repo. Commands provisioned at the
	// town root are inherited by Claude Code's path-hierarchy traversal, so
	// EnsureSettingsForRole must NOT provision a duplicate copy in the role dir.
	root := makeTownRootWithGit(t)
	mayorDir := root + "/mayor"
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider:     "claude",
			Dir:          ".claude",
			SettingsFile: "settings.json",
		},
	}

	if err := EnsureSettingsForRole(mayorDir, mayorDir, "mayor", rc); err != nil {
		t.Fatalf("EnsureSettingsForRole() error = %v", err)
	}

	// Commands must NOT be provisioned inside the role dir
	for _, cmd := range []string{"done", "handoff", "review"} {
		path := mayorDir + "/.claude/commands/" + cmd + ".md"
		if _, err := os.Stat(path); err == nil {
			t.Errorf("command %s.md was provisioned in mayor dir, want skipped (would duplicate town-root copy)", cmd)
		}
	}
}

func TestEnsureSettingsForRole_ProvisionCommandsOutsideTownRoot(t *testing.T) {
	// Crew/polecat workDirs are outside the town root git repo.
	// EnsureSettingsForRole must provision commands normally.
	workDir := t.TempDir()
	// workDir has no .git ancestor, so commandsInherited returns false.

	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider:     "claude",
			Dir:          ".claude",
			SettingsFile: "settings.json",
		},
	}

	if err := EnsureSettingsForRole(workDir, workDir, "crew", rc); err != nil {
		t.Fatalf("EnsureSettingsForRole() error = %v", err)
	}

	// At least one command should be provisioned
	provisioned := 0
	for _, cmd := range []string{"done", "handoff", "review"} {
		if _, err := os.Stat(workDir + "/.claude/commands/" + cmd + ".md"); err == nil {
			provisioned++
		}
	}
	if provisioned == 0 {
		t.Error("no commands provisioned in workDir outside town root, want at least one")
	}
}

func TestEnsureSettingsForRole_ProvisionsMattPocockSkills(t *testing.T) {
	workDir := t.TempDir()
	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider:     "claude",
			Dir:          ".claude",
			SettingsFile: "settings.json",
		},
	}

	if err := EnsureSettingsForRole(workDir, workDir, "polecat", rc); err != nil {
		t.Fatalf("EnsureSettingsForRole() error = %v", err)
	}

	for _, name := range []string{"implement", "diagnosing-bugs", "to-spec", "to-tickets", "resolving-merge-conflicts"} {
		path := workDir + "/.agents/skills/" + name + "/SKILL.md"
		if _, err := os.Stat(path); err != nil {
			t.Errorf("missing skill %s: %v", name, err)
		}
		claudePath := workDir + "/.claude/skills/" + name + "/SKILL.md"
		if _, err := os.Stat(claudePath); err != nil {
			t.Errorf("missing claude skill %s: %v", name, err)
		}
	}
}

func TestEnsureSettingsForRole_ProvisionsSkillsWithoutHooks(t *testing.T) {
	workDir := t.TempDir()
	if err := EnsureSettingsForRole(workDir, workDir, "crew", &config.RuntimeConfig{}); err != nil {
		t.Fatalf("EnsureSettingsForRole() error = %v", err)
	}

	path := workDir + "/.agents/skills/implement/SKILL.md"
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("skills must be provisioned even without hooks: %v", err)
	}
}

func TestEnsureSettingsForRole_ProvisionsInheritedRoleWorkDir(t *testing.T) {
	root := makeTownRootWithGit(t)
	mayorDir := root + "/mayor"
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	rc := &config.RuntimeConfig{
		Hooks: &config.RuntimeHooksConfig{
			Provider:     "claude",
			Dir:          ".claude",
			SettingsFile: "settings.json",
		},
	}
	if err := EnsureSettingsForRole(mayorDir, mayorDir, "mayor", rc); err != nil {
		t.Fatalf("EnsureSettingsForRole() error = %v", err)
	}

	if _, err := os.Stat(mayorDir + "/.agents/skills/implement/SKILL.md"); err != nil {
		t.Fatalf("mayor workDir must have local skills: %v", err)
	}
	if _, err := os.Stat(root + "/.agents/skills/implement/SKILL.md"); err != nil {
		t.Fatalf("town root should also receive skills: %v", err)
	}
}
