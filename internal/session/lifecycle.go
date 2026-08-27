// Package session provides polecat session lifecycle management.
package session

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/runtime"
	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/tmux"
)

// SessionConfig describes how to create and start a tmux session.
// This unifies the common startup pattern that was previously duplicated
// across polecat, mayor, boot, deacon, witness, refinery, crew, and dog
// session managers. Each of those managers previously had to coordinate
// 4+ packages (config, runtime, session, tmux) manually.
//
// Usage pattern:
//
//	result, err := session.StartSession(t, session.SessionConfig{
//	    SessionID: "gt-myrig-toast",
//	    WorkDir:   "/path/to/worktree",
//	    Role:      "polecat",
//	    TownRoot:  "/path/to/town",
//	    Beacon:    session.BeaconConfig{...},
//	})
type SessionConfig struct {
	// SessionID is the tmux session name (e.g., "gt-wyvern-Toast", "hq-mayor").
	SessionID string

	// WorkDir is the working directory for the session.
	WorkDir string

	// Role is the agent role (e.g., "polecat", "mayor", "boot", "deacon").
	Role string

	// TownRoot is the root of the Gas Town workspace (e.g., ~/gt).
	TownRoot string

	// RigPath is the rig directory path for config resolution.
	// Empty for town-level agents (mayor, deacon, boot).
	RigPath string

	// RigName is the rig name for environment variables and theming.
	// Empty for town-level agents.
	RigName string

	// AgentName is the specific agent name within a rig.
	// Used for polecats, crew, and dogs. Empty for singletons.
	AgentName string

	// Command is a pre-built startup command. If non-empty, skips command building.
	// If empty, the command is built from Beacon + config.BuildAgentStartupCommand.
	Command string

	// Beacon configures the startup beacon message for session identification.
	// Ignored if Command is non-empty.
	Beacon BeaconConfig

	// Instructions are appended after the beacon in the startup prompt.
	// Used by roles like Boot and Deacon that need explicit instructions.
	// Ignored if Command is non-empty.
	Instructions string

	// AgentOverride optionally specifies a different agent alias (e.g., "opencode").
	AgentOverride string

	// RuntimeConfigDir overrides the config directory for the runtime.
	RuntimeConfigDir string

	// ExtraEnv adds additional environment variables beyond the standard AgentEnv set.
	// These are set in the tmux session environment after the standard vars.
	ExtraEnv map[string]string

	// Theme is the tmux theme to apply. Nil means no theme is applied.
	Theme *tmux.Theme

	// Post-start behavior options.

	// WaitForAgent waits for the agent command to appear in the pane.
	WaitForAgent bool

	// WaitFatal makes WaitForAgent failure fatal — kills the session and returns error.
	// If false, WaitForAgent failure is silently ignored.
	WaitFatal bool

	// AcceptBypass accepts the bypass permissions warning dialog if it appears.
	AcceptBypass bool

	// ReadyDelay sleeps for the runtime's configured readiness delay.
	ReadyDelay bool

	// AutoRespawn sets the auto-respawn hook so the session survives crashes.
	AutoRespawn bool

	// RemainOnExit sets remain-on-exit immediately after session creation.
	RemainOnExit bool

	// TrackPID tracks the pane PID for defense-in-depth orphan cleanup.
	TrackPID bool

	// VerifySurvived checks that the session is still alive after startup.
	VerifySurvived bool
}

// StartResult contains the results of session startup.
type StartResult struct {
	// RuntimeConfig is the resolved runtime config for the role.
	// Callers may need this for role-specific post-startup steps
	// (e.g., handling fallback nudges, legacy fallback).
	RuntimeConfig *config.RuntimeConfig

	// RunID is the GASTA run identifier (GT_RUN) generated for this session.
	// All telemetry events emitted within the session carry this ID, enabling
	// waterfall correlation across prompts, BD calls, mail operations, and
	// agent conversation events.
	RunID string
}

// StartSession creates a tmux session following the standard Gas Town lifecycle.
//
// The lifecycle handles:
//  1. Resolve runtime config for the role
//  2. Ensure settings/plugins exist for the agent
//  3. Build startup command (if not provided)
//  4. Create tmux session with command
//  5. Set environment variables (standard + extra)
//  6. Apply theme (if configured)
//  7. Optional post-start: wait for agent, accept bypass, ready delay,
//     auto-respawn, PID tracking, verify survived
//
// Role-specific concerns (issue validation, fallback nudges, pane-died hooks,
// crew cycle bindings, etc.) should be handled by the caller before/after
// calling StartSession.
func StartSession(t *tmux.Tmux, cfg SessionConfig) (_ *StartResult, retErr error) {
	// Generate the GASTA run ID — the root identifier for all telemetry emitted
	// by this agent session and its subprocesses (bd, mail, …).
	runID := uuid.New().String()
	ctx := telemetry.WithRunID(context.Background(), runID)

	defer func() { telemetry.RecordSessionStart(ctx, cfg.SessionID, cfg.Role, retErr) }()
	if cfg.SessionID == "" {
		return nil, fmt.Errorf("SessionID is required")
	}
	if cfg.WorkDir == "" {
		return nil, fmt.Errorf("WorkDir is required")
	}
	if cfg.Role == "" {
		return nil, fmt.Errorf("Role is required")
	}

	// 1. Resolve runtime config.
	runtimeConfig := config.ResolveRoleAgentConfig(cfg.Role, cfg.TownRoot, cfg.RigPath)
	if cfg.AgentOverride != "" {
		rc, _, err := config.ResolveAgentConfigWithOverride(cfg.TownRoot, cfg.RigPath, cfg.AgentOverride)
		if err != nil {
			return nil, fmt.Errorf("resolving agent config for %s: %w", cfg.AgentOverride, err)
		}
		runtimeConfig = rc
	}

	// Credential preflight. A dead OAuth token does not make Claude Code
	// exit — it drops into the interactive onboarding flow and parks there.
	// The tmux session exists and the process runs, so every liveness check
	// passes while the agent never reaches a prompt and never heartbeats
	// (hq-ac0). Refuse to spawn instead of creating a silent zombie.
	if warning, err := config.PreflightAgentAuth(runtimeConfig, cfg.RuntimeConfigDir); err != nil {
		return nil, fmt.Errorf("credential preflight for %s: %w", cfg.Role, err)
	} else if warning != "" {
		fmt.Printf("warning: %s\n", warning)
	}

	// 2. Ensure settings/plugins exist for the agent.
	settingsDir := config.RoleSettingsDir(cfg.Role, cfg.RigPath)
	if settingsDir == "" {
		settingsDir = cfg.WorkDir
	}
	if err := runtime.EnsureSettingsForRole(settingsDir, cfg.WorkDir, cfg.Role, runtimeConfig); err != nil {
		return nil, fmt.Errorf("ensuring runtime settings: %w", err)
	}

	// 3. Build startup command if not provided.
	command := cfg.Command
	if command == "" {
		prompt := buildPrompt(cfg)
		var err error
		command, err = buildCommand(cfg, prompt)
		if err != nil {
			return nil, fmt.Errorf("building startup command: %w", err)
		}
	}

	// Prepend runtime config dir env if needed.
	if runtimeConfig.Session != nil && runtimeConfig.Session.ConfigDirEnv != "" && cfg.RuntimeConfigDir != "" {
		command = config.PrependEnv(command, map[string]string{
			runtimeConfig.Session.ConfigDirEnv: cfg.RuntimeConfigDir,
		})
	}

	// 4. Compute environment variables BEFORE creating the session so they
	// can be passed via tmux -e flags. Setting env via SetEnvironment after
	// session creation only affects newly spawned panes — the running pane
	// (and any subprocess the agent spawns, e.g. bd) keeps its original
	// environment (gt-neycp).
	envVars := config.AgentEnv(config.AgentEnvConfig{
		Role:             cfg.Role,
		Rig:              cfg.RigName,
		AgentName:        cfg.AgentName,
		TownRoot:         cfg.TownRoot,
		RuntimeConfigDir: cfg.RuntimeConfigDir,
		Agent:            cfg.AgentOverride,
		SessionName:      cfg.SessionID,
	})
	envVars = MergeRuntimeLivenessEnv(envVars, runtimeConfig)
	envVars["GT_RUN"] = runID
	for k, v := range cfg.ExtraEnv {
		envVars[k] = v
	}

	// 5. Create tmux session with command and env vars via -e flags so the
	// initial shell — and the agent's subprocesses — inherit them from the start.
	if err := t.NewSessionWithCommandAndEnv(cfg.SessionID, cfg.WorkDir, command, envVars); err != nil {
		return nil, fmt.Errorf("creating session: %w", err)
	}

	// 6. Set remain-on-exit immediately if requested (before anything else can fail).
	if cfg.RemainOnExit {
		_ = t.SetRemainOnExit(cfg.SessionID, true)
	}

	// 7. Apply theme.
	if cfg.Theme != nil {
		_ = t.ConfigureGasTownSession(cfg.SessionID, cfg.Theme, cfg.RigName, cfg.AgentName, cfg.Role)
	}

	// 8. Wait for agent to start.
	if cfg.WaitForAgent {
		if err := t.WaitForCommand(cfg.SessionID, constants.SupportedShells, constants.ClaudeStartTimeout); err != nil {
			if cfg.WaitFatal {
				_ = t.KillSessionWithProcesses(cfg.SessionID)
				return nil, fmt.Errorf("waiting for %s to start: %w", cfg.Role, err)
			}
		}
	}

	// 9. Auto-respawn hook.
	if cfg.AutoRespawn {
		if err := t.SetAutoRespawnHook(cfg.SessionID); err != nil {
			fmt.Printf("warning: failed to set auto-respawn hook for %s: %v\n", cfg.Role, err)
		}
	}

	// 10. Accept startup dialogs (workspace trust + bypass permissions).
	if cfg.AcceptBypass {
		_ = t.AcceptStartupDialogs(cfg.SessionID)
		if err := t.CheckStartupBlocked(cfg.SessionID); err != nil {
			_ = t.KillSessionWithProcesses(cfg.SessionID)
			return nil, fmt.Errorf("startup blocked: %w", err)
		}
	}

	// 11. Ready delay: wait for agent to be fully ready at the prompt.
	// Uses prompt-based polling for agents with ReadyPromptPrefix,
	// falling back to ReadyDelayMs sleep for agents without prompt detection.
	if cfg.ReadyDelay {
		if err := t.WaitForRuntimeReady(cfg.SessionID, runtimeConfig, constants.ClaudeStartTimeout); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: agent readiness detection timed out for %s: %v\n", cfg.SessionID, err)
		}
	}

	// 12. Verify session survived startup.
	if cfg.VerifySurvived {
		running, err := t.HasSession(cfg.SessionID)
		if err != nil {
			// Clean up session on verification error to prevent orphan
			_ = t.KillSessionWithProcesses(cfg.SessionID)
			return nil, fmt.Errorf("verifying session: %w", err)
		}
		if !running {
			return nil, fmt.Errorf("session %s died during startup (agent command may have failed)", cfg.SessionID)
		}
		if err := t.CheckStartupBlocked(cfg.SessionID); err != nil {
			_ = t.KillSessionWithProcesses(cfg.SessionID)
			return nil, fmt.Errorf("startup blocked: %w", err)
		}
		if status := t.CheckSessionHealth(cfg.SessionID, 0); status != tmux.SessionHealthy {
			_ = t.KillSessionWithProcesses(cfg.SessionID)
			return nil, fmt.Errorf("session %s unhealthy during startup: %s", cfg.SessionID, status)
		}
	}

	// 13. Record agent's pane_id for ZFC-compliant liveness checks (gt-qmsx).
	// Declared pane identity replaces process-tree inference in IsRuntimeRunning
	// and FindAgentPane. Legacy sessions without GT_PANE_ID fall back to scanning.
	if paneID, err := t.GetPaneID(cfg.SessionID); err == nil {
		_ = t.SetEnvironment(cfg.SessionID, "GT_PANE_ID", paneID)
	}

	// 14. Track PID for defense-in-depth orphan cleanup.
	if cfg.TrackPID && cfg.TownRoot != "" {
		_ = TrackSessionPID(cfg.TownRoot, cfg.SessionID, t)
	}

	// 14. Stream agent conversation events to VictoriaLogs (opt-in).
	// Reads ~/.claude/projects/<hash>/<session>.jsonl and emits agent.event logs.
	// Non-fatal: observability failures must never block agent startup.
	if os.Getenv("GT_LOG_AGENT_OUTPUT") == "true" && os.Getenv("GT_OTEL_LOGS_URL") != "" {
		if err := ActivateAgentLogging(cfg.SessionID, cfg.WorkDir, runID); err != nil {
			fmt.Fprintf(os.Stderr, "warning: agent log watcher setup failed for %s: %v\n", cfg.SessionID, err)
		}
	}

	// Record the agent instantiation event (GASTA root span).
	// Done after session creation so we only emit on success.
	RecordAgentInstantiateFromDir(ctx, runID, runtimeConfig.ResolvedAgent,
		cfg.Role, cfg.AgentName, cfg.SessionID, cfg.RigName, cfg.TownRoot, "", cfg.WorkDir)

	return &StartResult{RuntimeConfig: runtimeConfig, RunID: runID}, nil
}

// RecordAgentInstantiateFromDir resolves the git branch/commit from workDir and
// emits the agent.instantiate root telemetry event. resolvedAgent defaults to
// "claudecode" when empty. Use this instead of calling telemetry.RecordAgentInstantiate
// directly to avoid duplicating the agentType/git-lookup boilerplate.
func RecordAgentInstantiateFromDir(ctx context.Context, runID, resolvedAgent, role, agentName, sessionID, rigName, townRoot, issueID, workDir string) {
	agentType := resolvedAgent
	if agentType == "" {
		agentType = "claudecode"
	}
	branch, commit := "", ""
	if g := git.NewGit(workDir); g != nil {
		if b, err := g.CurrentBranch(); err == nil {
			branch = b
		}
		if c, err := g.Rev("HEAD"); err == nil {
			commit = c
		}
	}
	telemetry.RecordAgentInstantiate(ctx, telemetry.AgentInstantiateInfo{
		RunID:     runID,
		AgentType: agentType,
		Role:      role,
		AgentName: agentName,
		SessionID: sessionID,
		RigName:   rigName,
		TownRoot:  townRoot,
		IssueID:   issueID,
		GitBranch: branch,
		GitCommit: commit,
	})
}

// StopSession stops a tmux session with optional graceful shutdown.
//
// If graceful is true, sends Ctrl-C first and waits for the session to exit
// before force-killing. This allows the agent to clean up.
func StopSession(t *tmux.Tmux, sessionID string, graceful bool) error {
	running, err := t.HasSession(sessionID)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	if graceful {
		_ = t.SendKeysRaw(sessionID, "C-c")
		WaitForSessionExit(t, sessionID, constants.GracefulShutdownTimeout)
	}

	// Kill any detached agent-log watcher for this session before tearing down
	// the tmux session, to avoid orphan processes accumulating over time.
	DeactivateAgentLogging(sessionID)

	if err := t.KillSessionWithProcesses(sessionID); err != nil {
		return fmt.Errorf("killing session: %w", err)
	}

	return nil
}

func mapKeysSorted(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// MergeRuntimeLivenessEnv ensures liveness-critical env vars are present in the
// tmux session environment table, even when agent resolution came from
// workspace/default settings rather than an explicit --agent override.
//
// Call this after config.AgentEnv() to add GT_AGENT and GT_PROCESS_NAMES
// before writing env vars to the tmux session via SetEnvironment.
func MergeRuntimeLivenessEnv(envVars map[string]string, runtimeConfig *config.RuntimeConfig) map[string]string {
	if envVars == nil {
		envVars = make(map[string]string)
	}
	if runtimeConfig == nil {
		return envVars
	}

	if _, hasGTAgent := envVars["GT_AGENT"]; !hasGTAgent && runtimeConfig.ResolvedAgent != "" {
		envVars["GT_AGENT"] = runtimeConfig.ResolvedAgent
	}

	if _, hasProcessNames := envVars["GT_PROCESS_NAMES"]; !hasProcessNames {
		agentForLookup := runtimeConfig.ResolvedAgent
		commandForLookup := runtimeConfig.Command
		argsForLookup := runtimeConfig.Args
		if existing, ok := envVars["GT_AGENT"]; ok && existing != "" {
			agentForLookup = existing
			// When GT_AGENT was set by AgentOverride (differs from the
			// workspace-resolved agent), the runtimeConfig.Command/Args
			// belong to the workspace agent, not the override. Pass empty
			// command so ResolveProcessNames uses the preset's own command.
			if existing != runtimeConfig.ResolvedAgent {
				commandForLookup = ""
				argsForLookup = nil
			}
		}
		processNames := config.ResolveProcessNames(agentForLookup, commandForLookup, argsForLookup...)
		if len(processNames) > 0 {
			envVars["GT_PROCESS_NAMES"] = strings.Join(processNames, ",")
		}
	}

	return envVars
}

// KillExistingSession kills an existing session if one is found.
// Returns true if a session was killed.
//
// If checkAlive is true, only kills zombie sessions (tmux alive but agent dead).
// If the session exists and the agent is alive, returns ErrAlreadyRunning.
// If checkAlive is false, kills any existing session unconditionally.
func KillExistingSession(t *tmux.Tmux, sessionID string, checkAlive bool) (bool, error) {
	running, err := t.HasSession(sessionID)
	if err != nil {
		return false, fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return false, nil
	}

	if checkAlive && t.IsAgentAlive(sessionID) {
		return false, fmt.Errorf("session already running: %s", sessionID)
	}

	if err := t.KillSessionWithProcesses(sessionID); err != nil {
		return false, fmt.Errorf("killing session %s: %w", sessionID, err)
	}

	return true, nil
}

// buildPrompt creates the startup prompt from beacon + instructions.
func buildPrompt(cfg SessionConfig) string {
	if cfg.Instructions != "" {
		return BuildStartupPrompt(cfg.Beacon, cfg.Instructions)
	}
	return FormatStartupBeacon(cfg.Beacon)
}

// buildCommand creates the startup command using the config package.
func buildCommand(cfg SessionConfig, prompt string) (string, error) {
	if cfg.AgentOverride != "" {
		return config.BuildAgentStartupCommandWithAgentOverride(
			cfg.Role, cfg.RigName, cfg.TownRoot, cfg.RigPath, prompt, cfg.AgentOverride)
	}
	return config.BuildAgentStartupCommand(
		cfg.Role, cfg.RigName, cfg.TownRoot, cfg.RigPath, prompt), nil
}

// ShutdownDelay is the standard delay after session creation.
// Some roles use this instead of the runtime's ready delay.
func ShutdownDelay() time.Duration {
	return constants.ShutdownNotifyDelay
}
