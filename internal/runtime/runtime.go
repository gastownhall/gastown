// Package runtime provides helpers for runtime-specific integration.
package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/cli"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/hooks"
	"github.com/steveyegge/gastown/internal/skills"
	"github.com/steveyegge/gastown/internal/templates/commands"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// EnsureSettingsForRole provisions all agent-specific configuration for a role.
// settingsDir is where provider settings (e.g., .claude/settings.json) are installed.
// workDir is the agent's working directory where slash commands and skills are provisioned.
// For roles like crew/witness/refinery/polecat, settingsDir is a gastown-managed
// parent directory (passed via --settings flag), while workDir is the customer repo.
// For mayor/deacon, settingsDir and workDir are the same.
func EnsureSettingsForRole(settingsDir, workDir, role string, rc *config.RuntimeConfig) error {
	if rc == nil {
		rc = config.DefaultRuntimeConfig()
	}

	provider := ""
	if rc.Hooks != nil {
		provider = rc.Hooks.Provider
	}
	if err := provisionRoleSkills(workDir, provider); err != nil {
		return err
	}

	if rc.Hooks == nil {
		return nil
	}
	if provider == "" || provider == "none" {
		return nil
	}

	// 1. Provider-specific settings via generic installer.
	// Reads template metadata from the preset and installs the appropriate template.
	useSettingsDir := false
	if preset := config.GetAgentPresetByName(provider); preset != nil {
		useSettingsDir = preset.HooksUseSettingsDir
	}
	if err := hooks.InstallForRole(provider, settingsDir, workDir, role, rc.Hooks.Dir, rc.Hooks.SettingsFile, useSettingsDir); err != nil {
		return err
	}
	if provider == "gemini" {
		if err := ensureGeminiContextFile(workDir); err != nil {
			return err
		}
	}

	// 2. Slash commands (agent-agnostic, uses shared body with provider-specific frontmatter)
	// Only provision for known agents to maintain backwards compatibility.
	// Skip provisioning when workDir is nested inside a town root: Claude Code's
	// path-hierarchy traversal already delivers the town root's .claude/commands/
	// to the agent, so a duplicate copy in workDir causes each command to appear twice.
	if commands.IsKnownAgent(provider) && !commandsInherited(workDir) {
		if err := commands.ProvisionFor(workDir, provider); err != nil {
			return err
		}
	}

	return nil
}

func provisionRoleSkills(workDir, provider string) error {
	if workDir == "" {
		return nil
	}

	townProvider := provider
	if townProvider == "" || townProvider == "none" {
		townProvider = "claude"
	}
	if townRoot, err := workspace.Find(workDir); err == nil && townRoot != "" {
		if err := skills.ProvisionFor(townRoot, townProvider); err != nil {
			return err
		}
		if samePath(townRoot, workDir) {
			return nil
		}
	}
	return skills.ProvisionFor(workDir, provider)
}

func ensureGeminiContextFile(workDir string) error {
	if workDir == "" {
		return nil
	}

	agentsPath := filepath.Join(workDir, "AGENTS.md")
	geminiPath := filepath.Join(workDir, "GEMINI.md")
	info, err := os.Lstat(geminiPath)
	if err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return nil
		}

		target, err := os.Readlink(geminiPath)
		if err != nil {
			return fmt.Errorf("reading GEMINI.md symlink: %w", err)
		}
		if target == "AGENTS.md" {
			return nil
		}
		if !pointsToAgentsMD(target) {
			return nil
		}
		if _, err := os.Stat(agentsPath); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("checking AGENTS.md: %w", err)
		}
		if err := os.Remove(geminiPath); err != nil {
			return fmt.Errorf("removing non-canonical GEMINI.md symlink: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking GEMINI.md: %w", err)
	}

	if _, err := os.Stat(agentsPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("checking AGENTS.md: %w", err)
	}

	if err := os.Symlink("AGENTS.md", geminiPath); err != nil {
		return fmt.Errorf("creating GEMINI.md symlink: %w", err)
	}
	return nil
}

func pointsToAgentsMD(target string) bool {
	return filepath.Base(filepath.Clean(target)) == "AGENTS.md"
}

// commandsInherited reports whether workDir will receive slash commands via
// Claude Code's path-hierarchy traversal without explicit provisioning.
// Commands are inherited when workDir is inside a Gas Town workspace root and
// not separated from it by a nested git repo. Crew and polecat workdirs are
// nested repos, so they still get their own command provisioning.
func commandsInherited(workDir string) bool {
	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" || samePath(townRoot, workDir) {
		return false
	}

	gitRoot := gitRootOf(workDir)
	if gitRoot != "" && !samePath(gitRoot, townRoot) {
		return false
	}
	return true
}

func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA == nil && errB == nil {
		return filepath.Clean(absA) == filepath.Clean(absB)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// gitRootOf walks up from dir to find the nearest ancestor directory containing
// a .git entry (file or directory). Returns empty string if none found.
func gitRootOf(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
	}
}

type startupPromptSession interface {
	NudgeSession(sessionID, message string) error
	WaitForRuntimeReady(sessionID string, rc *config.RuntimeConfig, timeout time.Duration) error
}

// SessionIDFromEnv returns the runtime session ID, if present.
// It checks GT_SESSION_ID_ENV first, then resolves from the current agent's preset,
// and falls back to CLAUDE_SESSION_ID for backwards compatibility.
func SessionIDFromEnv() string {
	if envName := os.Getenv("GT_SESSION_ID_ENV"); envName != "" {
		if sessionID := os.Getenv(envName); sessionID != "" {
			return sessionID
		}
	}
	// Use the current agent's session ID env var from its preset
	if agentName := os.Getenv("GT_AGENT"); agentName != "" {
		if preset := config.GetAgentPresetByName(agentName); preset != nil && preset.SessionIDEnv != "" {
			if sessionID := os.Getenv(preset.SessionIDEnv); sessionID != "" {
				return sessionID
			}
		}
	}
	// Backwards-compatible fallback for sessions without GT_AGENT
	return os.Getenv("CLAUDE_SESSION_ID")
}

// StartupFallbackCommands returns commands that approximate Claude hooks when hooks are unavailable.
func StartupFallbackCommands(role string, rc *config.RuntimeConfig) []string {
	if rc == nil {
		rc = config.DefaultRuntimeConfig()
	}
	if rc.Hooks != nil && rc.Hooks.Provider != "" && rc.Hooks.Provider != "none" && !rc.Hooks.Informational {
		return nil
	}

	role = strings.ToLower(role)
	command := "gt prime"
	// NOTE: session-started nudge to deacon removed — it interrupted
	// the deacon's await-signal backoff (exponential sleep). The deacon
	// already wakes on beads activity via bd activity --follow.

	return []string{command}
}

// RunStartupFallback sends the startup fallback commands via tmux.
func RunStartupFallback(t *tmux.Tmux, sessionID, role string, rc *config.RuntimeConfig) error {
	commands := StartupFallbackCommands(role, rc)
	for _, cmd := range commands {
		if err := t.NudgeSession(sessionID, cmd); err != nil {
			return err
		}
	}
	return nil
}

// DefaultPrimeWaitMs is the default wait time in milliseconds for non-hook agents
// to run gt prime before sending work instructions.
const DefaultPrimeWaitMs = 2000

// StartupFallbackInfo describes what fallback actions are needed for agent startup
// based on the agent's hook and prompt capabilities.
//
// Fallback matrix based on agent capabilities:
//
//	| Hooks | Prompt | Beacon Content           | Context Source      | Work Instructions   |
//	|-------|--------|--------------------------|---------------------|---------------------|
//	| ✓     | ✓      | Standard                 | Hook runs gt prime  | In beacon           |
//	| ✓     | ✗      | Standard (via nudge)     | Hook runs gt prime  | Same nudge          |
//	| ✗     | ✓      | "Run gt prime" (prompt)  | Agent runs manually | Delayed nudge       |
//	| ✗     | ✗      | "Run gt prime" (nudge)   | Agent runs manually | Delayed nudge       |
type StartupFallbackInfo struct {
	// IncludePrimeInBeacon indicates the beacon should include "Run gt prime" instruction.
	// True for non-hook agents where gt prime doesn't run automatically.
	IncludePrimeInBeacon bool

	// SendBeaconNudge indicates the beacon must be sent via nudge (agent has no prompt support).
	// True for agents with PromptMode "none".
	SendBeaconNudge bool

	// SendStartupNudge indicates work instructions need to be sent via nudge.
	// True when beacon doesn't include work instructions (non-hook agents, or hook agents without prompt).
	SendStartupNudge bool

	// StartupNudgeDelayMs is milliseconds to wait before sending work instructions nudge.
	// Allows gt prime to complete for non-hook agents (where it's not automatic).
	StartupNudgeDelayMs int
}

// StartupPromptFallback describes whether a role's startup prompt must be
// delivered via nudge after startup fallback commands have run.
type StartupPromptFallback struct {
	// Send indicates the startup prompt must be nudged because the runtime
	// cannot receive it as a CLI prompt argument.
	Send bool

	// DelayMs is the minimum wait before sending the startup prompt so
	// non-hook agents have time to finish `gt prime`.
	DelayMs int
}

// GetStartupFallbackInfo returns the fallback actions needed based on agent capabilities.
func GetStartupFallbackInfo(rc *config.RuntimeConfig) *StartupFallbackInfo {
	if rc == nil {
		rc = config.DefaultRuntimeConfig()
	}

	hasHooks := rc.Hooks != nil && rc.Hooks.Provider != "" && rc.Hooks.Provider != "none" && !rc.Hooks.Informational
	hasPrompt := rc.PromptMode != "none"

	info := &StartupFallbackInfo{}

	if !hasHooks {
		// Non-hook agents need to be told to run gt prime
		info.IncludePrimeInBeacon = true
		info.SendStartupNudge = true
		info.StartupNudgeDelayMs = DefaultPrimeWaitMs

		if !hasPrompt {
			// No prompt support - beacon must be sent via nudge
			info.SendBeaconNudge = true
		}
	} else if !hasPrompt {
		// Has hooks but no prompt - need to nudge beacon + work instructions together
		// Hook runs gt prime synchronously, so no wait needed
		info.SendBeaconNudge = true
		info.SendStartupNudge = true
		info.StartupNudgeDelayMs = 0
	}
	// else: hooks + prompt - nothing needed, all in CLI prompt + hook

	return info
}

// GetStartupPromptFallback returns the post-startup prompt delivery behavior
// for runtimes that cannot accept the startup prompt as a CLI argument.
func GetStartupPromptFallback(rc *config.RuntimeConfig) StartupPromptFallback {
	info := GetStartupFallbackInfo(rc)
	return StartupPromptFallback{
		Send:    info.SendBeaconNudge,
		DelayMs: info.StartupNudgeDelayMs,
	}
}

// DeliverStartupPromptFallback sends the startup prompt via nudge for runtimes
// that cannot accept the prompt as a CLI argument.
func DeliverStartupPromptFallback(
	t startupPromptSession,
	sessionID, prompt string,
	rc *config.RuntimeConfig,
	timeout time.Duration,
) error {
	fallback := GetStartupPromptFallback(rc)
	if !fallback.Send {
		return nil
	}

	if fallback.DelayMs > 0 {
		if err := t.WaitForRuntimeReady(sessionID, RuntimeConfigWithMinDelay(rc, fallback.DelayMs), timeout); err != nil {
			return fmt.Errorf("waiting for startup prompt fallback: %w", err)
		}
	}

	if err := t.NudgeSession(sessionID, prompt); err != nil {
		return fmt.Errorf("nudging startup prompt fallback: %w", err)
	}
	return nil
}

// StartupNudgeContent returns the work instructions to send as a startup nudge.
func StartupNudgeContent() string {
	return "Check your hook with `" + cli.Name() + " hook`. If work is present, begin immediately."
}

// BeaconPrimeInstruction returns the instruction to add to beacon for non-hook agents.
func BeaconPrimeInstruction() string {
	return "\n\nRun `" + cli.Name() + " prime` to initialize your context."
}

// RuntimeConfigWithMinDelay returns a shallow copy of rc with ReadyDelayMs set to
// at least minMs, and ReadyPromptPrefix cleared. This forces WaitForRuntimeReady
// to use the delay-based fallback path, ensuring the minimum wall-clock wait is
// always enforced. Used for the gt prime wait where we need a guaranteed delay for
// the agent to process the beacon and run gt prime — prompt detection would
// short-circuit immediately (seeing the still-present prompt from the initial
// readiness check) and bypass the intended delay floor.
func RuntimeConfigWithMinDelay(rc *config.RuntimeConfig, minMs int) *config.RuntimeConfig {
	if rc == nil {
		return &config.RuntimeConfig{Tmux: &config.RuntimeTmuxConfig{ReadyDelayMs: minMs}}
	}
	cp := *rc
	if cp.Tmux == nil {
		cp.Tmux = &config.RuntimeTmuxConfig{ReadyDelayMs: minMs}
	} else {
		tmuxCp := *cp.Tmux
		if tmuxCp.ReadyDelayMs < minMs {
			tmuxCp.ReadyDelayMs = minMs
		}
		// Clear prompt prefix to force the delay-based path in WaitForRuntimeReady.
		// The prime wait needs a guaranteed wall-clock delay, not prompt detection.
		tmuxCp.ReadyPromptPrefix = ""
		cp.Tmux = &tmuxCp
	}
	return &cp
}
