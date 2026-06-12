package witness

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/nudge"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/runtime"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Common errors
var (
	ErrNotRunning     = errors.New("witness not running")
	ErrAlreadyRunning = errors.New("witness already running")
)

// Manager handles witness lifecycle and monitoring operations.
// ZFC-compliant: tmux session is the source of truth for running state.
type Manager struct {
	rig *rig.Rig
}

// NewManager creates a new witness manager for a rig.
func NewManager(r *rig.Rig) *Manager {
	return &Manager{
		rig: r,
	}
}

// IsRunning checks if the witness session is active and healthy.
// Checks both tmux session existence AND agent process liveness to avoid
// reporting zombie sessions (tmux alive but Claude dead) as "running".
// ZFC: tmux session existence is the source of truth for session state,
// but agent liveness determines if the session is actually functional.
func (m *Manager) IsRunning() (bool, error) {
	t := tmux.NewTmux()
	status := t.CheckSessionHealth(m.SessionName(), 0)
	return status == tmux.SessionHealthy, nil
}

// IsHealthy checks if the witness is running and has been active recently.
// Unlike IsRunning which only checks process liveness, this also detects hung
// sessions where Claude is alive but hasn't produced output in maxInactivity.
// Returns the detailed ZombieStatus for callers that need to distinguish
// between different failure modes.
func (m *Manager) IsHealthy(maxInactivity time.Duration) tmux.ZombieStatus {
	t := tmux.NewTmux()
	return t.CheckSessionHealth(m.SessionName(), maxInactivity)
}

// SessionName returns the tmux session name for this witness.
func (m *Manager) SessionName() string {
	return session.WitnessSessionName(session.PrefixFor(m.rig.Name))
}

// Status returns information about the witness session.
// ZFC-compliant: tmux session is the source of truth.
func (m *Manager) Status() (*tmux.SessionInfo, error) {
	t := tmux.NewTmux()
	sessionID := m.SessionName()

	running, err := t.HasSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return nil, ErrNotRunning
	}

	return t.GetSessionInfo(sessionID)
}

// witnessDir returns the working directory for the witness.
// Prefers witness/rig/ for existing legacy clones, otherwise uses witness/.
func (m *Manager) witnessDir() string {
	witnessRigDir := filepath.Join(m.rig.Path, "witness", "rig")
	if _, err := os.Stat(witnessRigDir); err == nil {
		return witnessRigDir
	}

	return filepath.Join(m.rig.Path, "witness")
}

func (m *Manager) prepareWitnessDir(townRoot string) (string, error) {
	witnessDir := m.witnessDir()
	if err := os.MkdirAll(witnessDir, 0755); err != nil {
		return "", fmt.Errorf("creating witness dir: %w", err)
	}
	if err := beads.SetupRedirect(townRoot, witnessDir); err != nil {
		return "", fmt.Errorf("ensuring witness beads redirect: %w", err)
	}
	return witnessDir, nil
}

// Start starts the witness.
// If foreground is true, returns an error (foreground mode deprecated).
// Otherwise, spawns a Claude agent in a tmux session.
// agentOverride optionally specifies a different agent alias to use.
// envOverrides are KEY=VALUE pairs that override all other env var sources.
// ZFC-compliant: no state file, tmux session is source of truth.
func (m *Manager) Start(foreground bool, agentOverride string, envOverrides []string) error {
	t := tmux.NewTmux()
	sessionID := m.SessionName()

	if foreground {
		// Foreground mode is deprecated - patrol logic moved to mol-witness-patrol
		return fmt.Errorf("foreground mode is deprecated; use background mode (remove --foreground flag)")
	}

	// Check if session already exists
	running, _ := t.HasSession(sessionID)
	if running {
		// Session exists - check if Claude is actually running (healthy vs zombie)
		if t.IsAgentAlive(sessionID) {
			// Healthy - Claude is running
			return ErrAlreadyRunning
		}
		// Zombie detected — tmux alive but agent dead.
		// Mitigate TOCTOU gap: the agent may be slow to start, appearing
		// dead during initialization. Record session creation time, wait
		// briefly, then re-verify before killing to avoid destroying a
		// session that just became healthy.
		createdAt, _ := t.GetSessionCreatedUnix(sessionID)
		time.Sleep(constants.ZombieKillGracePeriod)

		// Re-check: abort kill if agent started or session was replaced
		if t.IsAgentAlive(sessionID) {
			return ErrAlreadyRunning
		}
		if createdNow, _ := t.GetSessionCreatedUnix(sessionID); createdAt > 0 && createdNow != createdAt {
			// Session was replaced between checks — another process already
			// handled the zombie. Treat as already running; caller can retry.
			return ErrAlreadyRunning
		}

		if err := t.KillSession(sessionID); err != nil {
			return fmt.Errorf("killing zombie session: %w", err)
		}
	}

	// Note: No PID check per ZFC - tmux session is the source of truth

	// Ensure runtime settings exist in the shared witness parent directory.
	// Settings are passed to Claude Code via --settings flag.
	// ResolveRoleAgentConfig is internally serialized (resolveConfigMu in
	// package config) to prevent concurrent rig starts from corrupting the
	// global agent registry.
	// Working directory
	townRoot := m.townRoot()
	witnessDir, err := m.prepareWitnessDir(townRoot)
	if err != nil {
		return err
	}

	// Resolve CLAUDE_CONFIG_DIR from accounts.json so witness sessions
	// use the correct account. Mirrors the daemon restart path (lifecycle.go).
	accountsPath := constants.MayorAccountsPath(townRoot)
	runtimeConfigDir, _, _ := config.ResolveAccountConfigDir(accountsPath, "")
	if runtimeConfigDir == "" {
		runtimeConfigDir = os.Getenv("CLAUDE_CONFIG_DIR")
	}

	runtimeConfig := config.ResolveRoleAgentConfig("witness", townRoot, m.rig.Path)
	witnessSettingsDir := config.RoleSettingsDir("witness", m.rig.Path)
	if err := runtime.EnsureSettingsForRole(witnessSettingsDir, witnessDir, "witness", runtimeConfig); err != nil {
		return fmt.Errorf("ensuring runtime settings: %w", err)
	}

	// Ensure .gitignore has required Gas Town patterns
	if err := rig.EnsureGitignorePatterns(witnessDir); err != nil {
		style.PrintWarning("could not update witness .gitignore: %v", err)
	}

	roleConfig, err := m.roleConfig()
	if err != nil {
		// Non-fatal: role config is optional. Log and continue with defaults.
		log.Printf("warning: could not load witness role config for %s: %v", m.rig.Name, err)
		roleConfig = nil
	}

	// Compute environment BEFORE creating the session so it can be passed to
	// tmux via -e flags. This ensures the initial shell — and any subprocesses
	// Claude spawns (notably bd) — inherit BEADS_DOLT_PORT and friends.
	// Setting env after session creation via SetEnvironment only affects newly
	// spawned panes, not the subprocess tree of the already-running pane (gt-neycp).
	envVars := config.AgentEnv(config.AgentEnvConfig{
		Role:             "witness",
		Rig:              m.rig.Name,
		TownRoot:         townRoot,
		RuntimeConfigDir: runtimeConfigDir,
		Agent:            agentOverride,
		SessionName:      sessionID,
	})
	envVars = session.MergeRuntimeLivenessEnv(envVars, runtimeConfig)

	// Generate the GASTA run ID for this witness session.
	runID := uuid.New().String()
	envVars["GT_RUN"] = runID

	// Apply role config env vars (non-fatal). Skip keys already set by AgentEnv
	// to prevent TOML env overriding the canonical qualified GT_ROLE.
	// See: https://github.com/steveyegge/gastown/issues/2492
	roleEnv := roleConfigEnvVars(roleConfig, townRoot, m.rig.Name)
	for key, value := range roleEnv {
		if _, alreadySet := envVars[key]; alreadySet {
			continue
		}
		envVars[key] = value
	}

	// Apply CLI env overrides last (highest priority).
	for _, override := range envOverrides {
		if key, value, ok := strings.Cut(override, "="); ok {
			envVars[key] = value
		}
	}

	// Build startup command. The command also embeds env vars via 'exec env'
	// for WaitForCommand detection — belt-and-suspenders alongside -e flags.
	// NOTE: No gt prime injection needed - SessionStart hook handles it automatically.
	// Pass m.rig.Path so rig agent settings are honored (not town-level defaults)
	command, err := buildWitnessStartCommand(m.rig.Path, m.rig.Name, townRoot, sessionID, agentOverride, roleConfig, runtimeConfigDir)
	if err != nil {
		return err
	}

	// Create session with command and env vars via -e flags so the initial
	// shell (and Claude's subprocesses) inherit them from the start.
	// See: https://github.com/anthropics/gastown/issues/280 (race condition fix)
	if err := t.NewSessionWithCommandAndEnv(sessionID, witnessDir, command, envVars); err != nil {
		return fmt.Errorf("creating tmux session: %w", err)
	}

	// Apply Gas Town theming (non-fatal: theming failure doesn't affect operation)
	theme := tmux.ResolveSessionTheme(townRoot, m.rig.Name, "witness", "")
	_ = t.ConfigureGasTownSession(sessionID, theme, m.rig.Name, "witness", "witness")

	// Wait for Claude to start - fatal if Claude fails to launch
	if err := t.WaitForCommand(sessionID, constants.SupportedShells, constants.ClaudeStartTimeout); err != nil {
		// Kill the zombie session before returning error
		_ = t.KillSessionWithProcesses(sessionID)
		return fmt.Errorf("waiting for witness to start: %w", err)
	}

	// Accept startup dialogs (workspace trust + bypass permissions) if they appear.
	if err := t.AcceptStartupDialogs(sessionID); err != nil {
		log.Printf("warning: accepting startup dialogs for %s: %v", sessionID, err)
	}

	// Track PID for defense-in-depth orphan cleanup (non-fatal)
	if err := session.TrackSessionPID(townRoot, sessionID, t); err != nil {
		log.Printf("warning: tracking session PID for %s: %v", sessionID, err)
	}

	// Start nudge-queue poller (gt-dgf). Claude's UserPromptSubmit hook only
	// drains when the agent submits a prompt. Idle agents never submit, so
	// queued nudges deadlock. The poller breaks the cycle by polling every 10s.
	if _, pollerErr := nudge.StartPoller(townRoot, sessionID); pollerErr != nil {
		log.Printf("warning: could not start nudge poller for %s: %v", sessionID, pollerErr)
	}

	_ = runtime.RunStartupFallback(t, sessionID, "witness", runtimeConfig)
	initialPrompt := session.BuildStartupPrompt(session.BeaconConfig{
		Recipient: session.BeaconRecipient("witness", "", m.rig.Name),
		Sender:    "deacon",
		Topic:     "patrol",
	}, "Run `gt prime --hook` and begin patrol.")
	_ = runtime.DeliverStartupPromptFallback(t, sessionID, initialPrompt, runtimeConfig, constants.ClaudeStartTimeout)

	// Stream witness's Claude Code JSONL conversation log to VictoriaLogs (opt-in).
	if os.Getenv("GT_LOG_AGENT_OUTPUT") == "true" && os.Getenv("GT_OTEL_LOGS_URL") != "" {
		if err := session.ActivateAgentLogging(sessionID, witnessDir, runID); err != nil {
			log.Printf("warning: agent log watcher setup failed for %s: %v", sessionID, err)
		}
	}

	// Record the agent instantiation event (GASTA root span).
	session.RecordAgentInstantiateFromDir(context.Background(), runID, runtimeConfig.ResolvedAgent,
		"witness", "witness", sessionID, m.rig.Name, townRoot, "", witnessDir)

	time.Sleep(constants.ShutdownNotifyDelay)

	return nil
}

func (m *Manager) roleConfig() (*beads.RoleConfig, error) {
	townRoot := m.townRoot()
	roleDef, err := config.LoadRoleDefinition(townRoot, m.rig.Path, "witness")
	if err != nil {
		return nil, fmt.Errorf("loading witness role config: %w", err)
	}
	return &beads.RoleConfig{
		SessionPattern: roleDef.Session.Pattern,
		WorkDirPattern: roleDef.Session.WorkDir,
		NeedsPreSync:   roleDef.Session.NeedsPreSync,
		StartCommand:   roleDef.Session.StartCommand,
		EnvVars:        roleDef.Env,
	}, nil
}

func (m *Manager) townRoot() string {
	townRoot, err := workspace.Find(m.rig.Path)
	if err != nil || townRoot == "" {
		return m.rig.Path
	}
	return townRoot
}

func roleConfigEnvVars(roleConfig *beads.RoleConfig, townRoot, rigName string) map[string]string {
	if roleConfig == nil || len(roleConfig.EnvVars) == 0 {
		return nil
	}
	expanded := make(map[string]string, len(roleConfig.EnvVars))
	for key, value := range roleConfig.EnvVars {
		expanded[key] = beads.ExpandRolePattern(value, townRoot, rigName, "", "witness", session.PrefixFor(rigName))
	}
	return expanded
}

func buildWitnessStartCommand(rigPath, rigName, townRoot, sessionName, agentOverride string, roleConfig *beads.RoleConfig, runtimeConfigDir string) (string, error) {
	if agentOverride != "" {
		roleConfig = nil
	}
	if roleConfig != nil && roleConfig.StartCommand != "" {
		rc := config.ResolveRoleAgentConfig("witness", townRoot, rigPath)
		if !config.IsResolvedAgentClaude(rc) {
			// Non-Claude agent: skip TOML start_command entirely.
			// Built-in role TOMLs hardcode "exec claude ..." which is wrong
			// for non-Claude agents. Fall through to BuildStartupCommandFromConfig
			// which uses the resolved agent's command and args.
		} else if !isBuiltinClaudeStartCommand(roleConfig.StartCommand) && !config.HasExplicitRoleAgent("witness", townRoot, rigPath) {
			// Custom (non-builtin) start_command with Claude agent and no explicit
			// role_agents mapping: use TOML pattern with template expansion.
			cmd := beads.ExpandRolePattern(roleConfig.StartCommand, townRoot, rigName, "", "witness", session.PrefixFor(rigName))
			if strings.HasPrefix(cmd, "exec ") {
				cmd = "exec env -u CLAUDECODE NODE_OPTIONS='' " + strings.TrimPrefix(cmd, "exec ")
			} else {
				cmd = "env -u CLAUDECODE NODE_OPTIONS='' " + cmd
			}
			return cmd, nil
		}
		// Non-Claude agent OR Claude with built-in start_command: fall
		// through to BuildStartupCommandFromConfig for proper agent and
		// model flag resolution.
	}
	initialPrompt := session.BuildStartupPrompt(session.BeaconConfig{
		Recipient: session.BeaconRecipient("witness", "", rigName),
		Sender:    "deacon",
		Topic:     "patrol",
	}, "Run `gt prime --hook` and begin patrol.")
	command, err := config.BuildStartupCommandFromConfig(config.AgentEnvConfig{
		Role:             "witness",
		Rig:              rigName,
		TownRoot:         townRoot,
		RuntimeConfigDir: runtimeConfigDir,
		Prompt:           initialPrompt,
		Topic:            "patrol",
		SessionName:      sessionName,
	}, rigPath, initialPrompt, agentOverride)
	if err != nil {
		return "", fmt.Errorf("building startup command: %w", err)
	}
	return command, nil
}

// isBuiltinClaudeStartCommand returns true if the start_command is the
// built-in default from role TOMLs ("exec claude --dangerously-skip-permissions").
// Custom start_commands (e.g., "exec run --town {town}") return false.
func isBuiltinClaudeStartCommand(cmd string) bool {
	trimmed := strings.TrimPrefix(cmd, "exec ")
	return trimmed == "claude --dangerously-skip-permissions"
}

// Stop stops the witness.
// ZFC-compliant: tmux session is the source of truth.
func (m *Manager) Stop() error {
	t := tmux.NewTmux()
	sessionID := m.SessionName()

	// Check if tmux session exists
	running, _ := t.HasSession(sessionID)
	if !running {
		return ErrNotRunning
	}

	// Kill the tmux session
	return t.KillSession(sessionID)
}
