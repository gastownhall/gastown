package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/util"
)

// BeadsMessage represents a message from gt mail inbox --json.
type BeadsMessage struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	Timestamp string `json:"timestamp"`
	Read      bool   `json:"read"`
	Priority  string `json:"priority"`
	Type      string `json:"type"`
}

// MaxLifecycleMessageAge is the default max age of a lifecycle message before it's ignored.
// Configurable via operational.daemon.max_lifecycle_message_age.
const MaxLifecycleMessageAge = 6 * time.Hour

// ProcessLifecycleRequests checks for and processes lifecycle requests from the deacon inbox.
func (d *Daemon) ProcessLifecycleRequests() {
	// Get mail for deacon identity (using gt mail, not bd mail)
	cmd := exec.Command(d.gtPath, "mail", "inbox", "--identity", "deacon/", "--json")
	cmd.Dir = d.config.TownRoot
	cmd.Env = bdReadOnlyEnv()
	util.SetDetachedProcessGroup(cmd)

	output, err := cmd.Output()
	if err != nil {
		d.logger.Printf("Warning: failed to fetch deacon inbox: %v", err)
		return
	}

	if len(output) == 0 || string(output) == "[]" || string(output) == "[]\n" {
		return
	}

	var messages []BeadsMessage
	if err := json.Unmarshal(output, &messages); err != nil {
		d.logger.Printf("Error parsing mail: %v", err)
		return
	}

	for _, msg := range messages {
		if msg.Read {
			continue // Already processed
		}

		request := d.parseLifecycleRequest(&msg)
		if request == nil {
			continue // Not a lifecycle request
		}

		// Check message age - ignore stale lifecycle requests
		maxAge := d.loadOperationalConfig().GetDaemonConfig().MaxLifecycleMessageAgeD()
		if msgTime, err := time.Parse(time.RFC3339, msg.Timestamp); err == nil {
			age := time.Since(msgTime)
			if age > maxAge {
				d.logger.Printf("Ignoring stale lifecycle request from %s (age: %v, max: %v) - deleting",
					request.From, age.Round(time.Minute), maxAge)
				if err := d.closeMessage(msg.ID); err != nil {
					d.logger.Printf("Warning: failed to delete stale message %s: %v", msg.ID, err)
				}
				continue
			}
		}

		d.logger.Printf("Processing lifecycle request from %s: %s", request.From, request.Action)

		// CRITICAL: Delete message FIRST, before executing action.
		// This prevents stale messages from being reprocessed on every heartbeat.
		// "Claim then execute" pattern: claim by deleting, then execute.
		// Even if action fails, the message is gone - sender must re-request.
		if err := d.closeMessage(msg.ID); err != nil {
			d.logger.Printf("Warning: failed to delete message %s before execution: %v", msg.ID, err)
			// Continue anyway - better to attempt action than leave stale message
		}

		if err := d.executeLifecycleAction(request); err != nil {
			d.logger.Printf("Error executing lifecycle action: %v", err)
			continue
		}
	}
}

// LifecycleBody is the structured body format for lifecycle requests.
// Claude should send mail with JSON body: {"action": "cycle"} or {"action": "shutdown"}
type LifecycleBody struct {
	Action string `json:"action"`
}

// parseLifecycleRequest extracts a lifecycle request from a message.
// Uses structured body parsing instead of keyword matching on subject.
func (d *Daemon) parseLifecycleRequest(msg *BeadsMessage) *LifecycleRequest {
	// Gate: subject must start with "LIFECYCLE:"
	subject := strings.ToLower(msg.Subject)
	if !strings.HasPrefix(subject, "lifecycle:") {
		return nil
	}

	// Parse structured body for action
	var body LifecycleBody
	if err := json.Unmarshal([]byte(msg.Body), &body); err != nil {
		// Fallback: check for simple action strings in body
		bodyLower := strings.ToLower(strings.TrimSpace(msg.Body))
		switch {
		case bodyLower == "restart" || bodyLower == "action: restart":
			body.Action = "restart"
		case bodyLower == "shutdown" || bodyLower == "action: shutdown" || bodyLower == "stop":
			body.Action = "shutdown"
		case bodyLower == "cycle" || bodyLower == "action: cycle":
			body.Action = "cycle"
		default:
			d.logger.Printf("Lifecycle request with unparseable body: %q", msg.Body)
			return nil
		}
	}

	// Map action string to enum
	var action LifecycleAction
	switch strings.ToLower(body.Action) {
	case "restart":
		action = ActionRestart
	case "shutdown", "stop":
		action = ActionShutdown
	case "cycle":
		action = ActionCycle
	default:
		d.logger.Printf("Unknown lifecycle action: %q", body.Action)
		return nil
	}

	return &LifecycleRequest{
		From:      msg.From,
		Action:    action,
		Timestamp: time.Now(),
	}
}

// executeLifecycleAction performs the requested lifecycle action.
func (d *Daemon) executeLifecycleAction(request *LifecycleRequest) error {
	// Determine session name from sender identity
	sessionName := d.identityToSession(request.From)
	if sessionName == "" {
		return fmt.Errorf("unknown agent identity: %s", request.From)
	}

	d.logger.Printf("Executing %s for session %s", request.Action, sessionName)

	// Check agent bead state (ZFC: trust what agent reports) - gt-39ttg
	agentBeadID := d.identityToAgentBeadID(request.From)
	if agentBeadID != "" {
		if beadState, err := d.getAgentBeadState(agentBeadID); err == nil {
			d.logger.Printf("Agent bead %s reports state: %s", agentBeadID, beadState)
		}
	}

	// Check if session exists (tmux detection still needed for lifecycle actions)
	running, err := d.tmux.HasSession(sessionName)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}

	switch request.Action {
	case ActionShutdown:
		if running {
			// Use KillSessionWithProcesses to ensure all descendant processes are killed.
			// This prevents orphan bash processes from Claude's Bash tool surviving session termination.
			if err := d.tmux.KillSessionWithProcesses(sessionName); err != nil {
				return fmt.Errorf("killing session: %w", err)
			}
			d.logger.Printf("Killed session %s", sessionName)
		}
		return nil

	case ActionCycle, ActionRestart:
		if running {
			// Kill the session first - use KillSessionWithProcesses to prevent orphan processes.
			if err := d.tmux.KillSessionWithProcesses(sessionName); err != nil {
				return fmt.Errorf("killing session: %w", err)
			}
			d.logger.Printf("Killed session %s for restart", sessionName)

			// Wait a moment
			time.Sleep(constants.ShutdownNotifyDelay)
		}

		// Restart the session
		if err := d.restartSession(sessionName, request.From); err != nil {
			return fmt.Errorf("restarting session: %w", err)
		}
		d.logger.Printf("Restarted session %s", sessionName)
		return nil

	default:
		return fmt.Errorf("unknown action: %s", request.Action)
	}
}

// ParsedIdentity holds the components extracted from an agent identity string.
// This is used to look up the appropriate role config for lifecycle management.
type ParsedIdentity struct {
	RoleType  string // mayor, deacon, witness, refinery, crew, polecat
	RigName   string // Empty for town-level agents (mayor, deacon)
	AgentName string // Empty for singletons (mayor, deacon, witness, refinery)
}

// parseIdentity extracts role type, rig name, and agent name from an identity string.
// This is the ONLY place where identity string patterns are parsed.
// All other functions should use the extracted components to look up role config.
func parseIdentity(identity string) (*ParsedIdentity, error) {
	switch identity {
	case constants.RoleMayor:
		return &ParsedIdentity{RoleType: constants.RoleMayor}, nil
	case constants.RoleDeacon:
		return &ParsedIdentity{RoleType: constants.RoleDeacon}, nil
	}

	// Pattern: <rig>-witness → witness role
	if strings.HasSuffix(identity, "-witness") {
		rigName := strings.TrimSuffix(identity, "-witness")
		return &ParsedIdentity{RoleType: constants.RoleWitness, RigName: rigName}, nil
	}

	// Pattern: <rig>-refinery → refinery role
	if strings.HasSuffix(identity, "-refinery") {
		rigName := strings.TrimSuffix(identity, "-refinery")
		return &ParsedIdentity{RoleType: constants.RoleRefinery, RigName: rigName}, nil
	}

	// Pattern: <rig>-crew-<name> → crew role
	if strings.Contains(identity, "-crew-") {
		parts := strings.SplitN(identity, "-crew-", 2)
		if len(parts) == 2 {
			return &ParsedIdentity{RoleType: constants.RoleCrew, RigName: parts[0], AgentName: parts[1]}, nil
		}
	}

	// Pattern: <rig>-polecat-<name> → polecat role
	if strings.Contains(identity, "-polecat-") {
		parts := strings.SplitN(identity, "-polecat-", 2)
		if len(parts) == 2 {
			return &ParsedIdentity{RoleType: constants.RolePolecat, RigName: parts[0], AgentName: parts[1]}, nil
		}
	}

	// Pattern: <rig>/polecats/<name> → polecat role (slash format)
	if strings.Contains(identity, "/polecats/") {
		parts := strings.Split(identity, "/polecats/")
		if len(parts) == 2 {
			return &ParsedIdentity{RoleType: constants.RolePolecat, RigName: parts[0], AgentName: parts[1]}, nil
		}
	}

	return nil, fmt.Errorf("unknown identity format: %s", identity)
}

// getRoleConfigForIdentity loads role configuration from the config-based role system.
// Uses config.LoadRoleDefinition() with layered override resolution (builtin → town → rig).
// Returns config in beads.RoleConfig format for backward compatibility.
func (d *Daemon) getRoleConfigForIdentity(identity string) (*beads.RoleConfig, *ParsedIdentity, error) {
	parsed, err := parseIdentity(identity)
	if err != nil {
		return nil, nil, err
	}

	// Determine rig path for rig-scoped roles
	rigPath := ""
	if parsed.RigName != "" {
		rigPath = filepath.Join(d.config.TownRoot, parsed.RigName)
	}

	// Load role definition from config system (Phase 2: config-based roles)
	roleDef, err := config.LoadRoleDefinition(d.config.TownRoot, rigPath, parsed.RoleType)
	if err != nil {
		d.logger.Printf("Warning: failed to load role definition for %s: %v", parsed.RoleType, err)
		// Return parsed identity even if config fails (caller can use defaults)
		return nil, parsed, nil
	}

	// Convert to beads.RoleConfig for backward compatibility
	roleConfig := &beads.RoleConfig{
		SessionPattern: roleDef.Session.Pattern,
		WorkDirPattern: roleDef.Session.WorkDir,
		NeedsPreSync:   roleDef.Session.NeedsPreSync,
		StartCommand:   roleDef.Session.StartCommand,
		EnvVars:        roleDef.Env,
	}

	return roleConfig, parsed, nil
}

// identityToSession converts a beads identity to a tmux session name.
// Always uses session.*SessionName() functions for consistency with gt up and daemon heartbeat.
func (d *Daemon) identityToSession(identity string) string {
	parsed, err := parseIdentity(identity)
	if err != nil {
		return ""
	}

	switch parsed.RoleType {
	case constants.RoleMayor:
		return session.MayorSessionName()
	case constants.RoleDeacon:
		return session.DeaconSessionName()
	case constants.RoleWitness:
		return session.WitnessSessionName(session.PrefixFor(parsed.RigName))
	case constants.RoleRefinery:
		return session.RefinerySessionName(session.PrefixFor(parsed.RigName))
	case constants.RoleCrew:
		return session.CrewSessionName(session.PrefixFor(parsed.RigName), parsed.AgentName)
	case constants.RolePolecat:
		return session.PolecatSessionName(session.PrefixFor(parsed.RigName), parsed.AgentName)
	default:
		return ""
	}
}

// restartSession starts a new session for the given agent.
// Uses role config if available, falls back to hardcoded defaults.
func (d *Daemon) restartSession(sessionName, identity string) error {
	// Get role config for this identity
	config, parsed, err := d.getRoleConfigForIdentity(identity)
	if err != nil {
		return fmt.Errorf("parsing identity: %w", err)
	}

	// Check rig operational state for rig-level agents (witness, refinery, crew, polecat)
	// Town-level agents (mayor, deacon) are not affected by rig state
	if parsed.RigName != "" {
		if operational, reason := d.isRigOperational(parsed.RigName); !operational {
			d.logger.Printf("Skipping session restart for %s: %s", identity, reason)
			return fmt.Errorf("cannot restart session: %s", reason)
		}
	}

	// Determine working directory
	workDir := d.getWorkDir(config, parsed)
	if workDir == "" {
		return fmt.Errorf("cannot determine working directory for %s", identity)
	}

	// Determine if pre-sync is needed
	needsPreSync := d.getNeedsPreSync(config, parsed)

	// Pre-sync workspace for agents with git worktrees
	if needsPreSync {
		d.logger.Printf("Pre-syncing workspace for %s at %s", identity, workDir)
		d.syncWorkspace(workDir)
	}

	// Build startup command BEFORE creating the session so we can use
	// NewSessionWithCommand (command as initial pane process). This eliminates
	// the race condition in the old EnsureSessionFresh + SendKeys pattern where
	// the shell might not be ready to receive keystrokes, producing empty windows.
	startCmd := d.getStartCommand(config, parsed)

	// Create session with command as initial process (replaces EnsureSessionFresh + SendKeys).
	// EnsureSessionFreshWithCommand kills zombie sessions and creates a new one atomically.
	if err := d.tmux.EnsureSessionFreshWithCommand(sessionName, workDir, startCmd); err != nil {
		if errors.Is(err, tmux.ErrSessionRunning) {
			d.logger.Printf("Session %s already running with healthy agent, skipping restart", sessionName)
			return nil
		}
		return fmt.Errorf("creating session: %w", err)
	}

	// Set environment variables in tmux session table (for debugging/monitoring tools).
	d.setSessionEnvironment(sessionName, config, parsed)

	// Apply theme (non-fatal: theming failure doesn't affect operation)
	d.applySessionTheme(sessionName, parsed)

	// Wait for Claude to start, then accept startup dialogs if they appear.
	if err := d.tmux.WaitForCommand(sessionName, constants.SupportedShells, constants.ClaudeStartTimeout); err != nil {
		// Non-fatal - Claude might still start
	}
	_ = d.tmux.AcceptStartupDialogs(sessionName)
	time.Sleep(constants.ShutdownNotifyDelay)

	return nil
}

// getWorkDir determines the working directory for an agent.
// Uses role config if available, falls back to hardcoded defaults.
func (d *Daemon) getWorkDir(config *beads.RoleConfig, parsed *ParsedIdentity) string {
	// If role config has work_dir_pattern, use it
	if config != nil && config.WorkDirPattern != "" {
		return beads.ExpandRolePattern(config.WorkDirPattern, d.config.TownRoot, parsed.RigName, parsed.AgentName, parsed.RoleType, session.PrefixFor(parsed.RigName))
	}

	// Fallback: use default patterns based on role type
	switch parsed.RoleType {
	case constants.RoleMayor:
		return d.config.TownRoot
	case constants.RoleDeacon:
		return d.config.TownRoot
	case constants.RoleWitness:
		return filepath.Join(d.config.TownRoot, parsed.RigName)
	case constants.RoleRefinery:
		return filepath.Join(d.config.TownRoot, parsed.RigName, "refinery", "rig")
	case constants.RoleCrew:
		return filepath.Join(d.config.TownRoot, parsed.RigName, "crew", parsed.AgentName)
	case constants.RolePolecat:
		// New structure: polecats/<name>/<rigname>/ (for LLM ergonomics)
		// Old structure: polecats/<name>/ (for backward compat)
		newPath := filepath.Join(d.config.TownRoot, parsed.RigName, "polecats", parsed.AgentName, parsed.RigName)
		if _, err := os.Stat(newPath); err == nil {
			return newPath
		}
		return filepath.Join(d.config.TownRoot, parsed.RigName, "polecats", parsed.AgentName)
	default:
		return ""
	}
}

// getNeedsPreSync determines if a workspace needs git sync before starting.
// Uses role config if available, falls back to hardcoded defaults.
func (d *Daemon) getNeedsPreSync(config *beads.RoleConfig, parsed *ParsedIdentity) bool {
	// If role config is available, use it
	if config != nil {
		return config.NeedsPreSync
	}

	// Fallback: roles with persistent git clones need pre-sync
	switch parsed.RoleType {
	case constants.RoleRefinery, constants.RoleCrew:
		return true
	default:
		return false
	}
}

// isBuiltinClaudeStartCommand returns true if the start_command is the
// built-in default from role TOMLs ("exec claude --dangerously-skip-permissions").
// Custom start_commands (e.g., "exec run --town {town}") return false.
func isBuiltinClaudeStartCommand(cmd string) bool {
	trimmed := strings.TrimPrefix(cmd, "exec ")
	return trimmed == "claude --dangerously-skip-permissions"
}

// getStartCommand determines the startup command for an agent.
// Uses role config if available, then role-based agent selection, then hardcoded defaults.
// Includes beacon + role-specific instructions in the CLI prompt.
func (d *Daemon) getStartCommand(roleConfig *beads.RoleConfig, parsed *ParsedIdentity) string {
	// Role config start_command: only use when the resolved agent is Claude.
	// Built-in role TOMLs hardcode "exec claude ..." which bypasses the
	// declarative agent resolution system. Fall through to agent resolution
	// so non-Claude agents (copilot, codex, etc.) get the correct command.
	if roleConfig != nil && roleConfig.StartCommand != "" {
		rigPath := ""
		if parsed != nil && parsed.RigName != "" {
			rigPath = filepath.Join(d.config.TownRoot, parsed.RigName)
		}
		rc := config.ResolveRoleAgentConfig(parsed.RoleType, d.config.TownRoot, rigPath)
		if !config.IsResolvedAgentClaude(rc) {
			// Non-Claude agent: skip TOML start_command entirely (GH#2417).
			// Built-in role TOMLs hardcode "exec claude ..." which is wrong
			// for non-Claude agents. Fall through to BuildStartupCommandFromConfig
			// which uses the resolved agent's command and args.
		} else if !isBuiltinClaudeStartCommand(roleConfig.StartCommand) {
			// Custom (non-builtin) start_command with Claude agent: use TOML
			// pattern with template expansion.
			cmd := beads.ExpandRolePattern(roleConfig.StartCommand, d.config.TownRoot, parsed.RigName, parsed.AgentName, parsed.RoleType, session.PrefixFor(parsed.RigName))
			if strings.HasPrefix(cmd, "exec ") {
				cmd = "exec env -u CLAUDECODE NODE_OPTIONS='' " + cmd[len("exec "):]
			} else {
				cmd = "env -u CLAUDECODE NODE_OPTIONS='' " + cmd
			}
			return cmd
		}
		// Claude agent with built-in start_command: fall through to
		// BuildStartupCommandFromConfig for proper model flag resolution.
	}

	rigPath := ""
	if parsed != nil && parsed.RigName != "" {
		rigPath = filepath.Join(d.config.TownRoot, parsed.RigName)
	}

	// Use role-based agent resolution for per-role model selection
	runtimeConfig := config.ResolveRoleAgentConfig(parsed.RoleType, d.config.TownRoot, rigPath)

	// Build recipient for beacon using non-path format to prevent LLMs
	// from misinterpreting the recipient as a filesystem path.
	recipient := session.BeaconRecipient(parsed.RoleType, parsed.AgentName, parsed.RigName)
	prompt := session.BuildStartupPrompt(session.BeaconConfig{
		Recipient: recipient,
		Sender:    "daemon",
		Topic:     "lifecycle-restart",
	}, "Run `gt prime --hook` and begin work.")

	// Inline AgentEnv into the command for ALL roles, not just polecat/crew.
	// Without this, daemon-restarted witness/refinery/mayor/deacon sessions
	// don't get BEADS_DOLT_PORT, GT_ROLE, GT_RIG, etc. in Claude's env. Their
	// bd subprocess then falls back to embedded-Dolt auto-discovery instead of
	// connecting to the central server (gt-neycp).
	//
	// PrependEnv produces "export K=V ... && exec cmd" which is safe for
	// WaitForCommand/pane_current_command detection: exec replaces the shell,
	// so tmux sees the agent process, not a shell running exports.
	var sessionIDEnv string
	if runtimeConfig.Session != nil {
		sessionIDEnv = runtimeConfig.Session.SessionIDEnv
	}
	envVars := config.AgentEnv(config.AgentEnvConfig{
		Role:         parsed.RoleType,
		Rig:          parsed.RigName,
		AgentName:    parsed.AgentName,
		TownRoot:     d.config.TownRoot,
		SessionIDEnv: sessionIDEnv,
	})
	config.SanitizeAgentEnv(envVars, map[string]string{})
	return config.PrependEnv("exec "+runtimeConfig.BuildCommandWithPrompt(prompt), envVars)
}

// setSessionEnvironment sets environment variables for the tmux session.
// Uses centralized AgentEnv for consistency, plus custom env vars from role config if available.
func (d *Daemon) setSessionEnvironment(sessionName string, roleConfig *beads.RoleConfig, parsed *ParsedIdentity) {
	// Resolve CLAUDE_CONFIG_DIR from accounts.json so daemon-restarted sessions
	// use the correct account. Mirrors the crew startup path (start.go).
	accountsPath := constants.MayorAccountsPath(d.config.TownRoot)
	runtimeConfigDir, _, _ := config.ResolveAccountConfigDir(accountsPath, "")
	if runtimeConfigDir == "" {
		runtimeConfigDir = os.Getenv("CLAUDE_CONFIG_DIR")
	}

	// Use centralized AgentEnv for base environment variables
	envVars := config.AgentEnv(config.AgentEnvConfig{
		Role:             parsed.RoleType,
		Rig:              parsed.RigName,
		AgentName:        parsed.AgentName,
		TownRoot:         d.config.TownRoot,
		RuntimeConfigDir: runtimeConfigDir,
		SessionName:      sessionName,
	})
	for k, v := range envVars {
		_ = d.tmux.SetEnvironment(sessionName, k, v)
	}

	// Record agent's pane_id for ZFC-compliant liveness checks (gt-qmsx).
	if paneID, err := d.tmux.GetPaneID(sessionName); err == nil {
		_ = d.tmux.SetEnvironment(sessionName, "GT_PANE_ID", paneID)
	}

	// Set any custom env vars from role config.
	// Skip keys already set by AgentEnv to prevent TOML [env] from clobbering
	// canonical qualified values (e.g., GT_ROLE). See: https://github.com/steveyegge/gastown/issues/2492
	if roleConfig != nil {
		for k, v := range roleConfig.EnvVars {
			if _, alreadySet := envVars[k]; alreadySet {
				continue
			}
			expanded := beads.ExpandRolePattern(v, d.config.TownRoot, parsed.RigName, parsed.AgentName, parsed.RoleType, session.PrefixFor(parsed.RigName))
			_ = d.tmux.SetEnvironment(sessionName, k, expanded)
		}
	}
}

// applySessionTheme applies tmux theming to the session.
func (d *Daemon) applySessionTheme(sessionName string, parsed *ParsedIdentity) {
	rigName := parsed.RigName
	role := parsed.RoleType
	worker := parsed.RoleType
	if role == constants.RoleMayor {
		rigName = ""
		worker = "Mayor"
	}
	theme := tmux.ResolveSessionTheme(d.config.TownRoot, rigName, role, parsed.AgentName)
	_ = d.tmux.ConfigureGasTownSession(sessionName, theme, rigName, worker, role)
}

// syncFailureEscalationThreshold is the default number of consecutive pull failures
// before logging escalates from WARN to ERROR.
// Configurable via operational.daemon.sync_failure_escalation_threshold.
const syncFailureEscalationThreshold = 3

// syncWorkspace syncs a git workspace before starting a new session.
// This ensures agents with persistent clones (like refinery) start with current code.
// Handles dirty working trees by auto-stashing before pull and restoring after.
func (d *Daemon) syncWorkspace(workDir string) {
	// Determine default branch from rig config
	// workDir is like <townRoot>/<rigName>/<role>/rig or <townRoot>/<rigName>/crew/<name>
	defaultBranch := "main" // fallback
	rel, err := filepath.Rel(d.config.TownRoot, workDir)
	if err == nil {
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) > 0 {
			rigPath := filepath.Join(d.config.TownRoot, parts[0])
			if rigCfg, err := rig.LoadRigConfig(rigPath); err == nil && rigCfg.DefaultBranch != "" {
				defaultBranch = rigCfg.DefaultBranch
			}
		}
	}

	// Capture stderr for debuggability
	var stderr bytes.Buffer

	// Fetch latest from origin
	fetchCmd := exec.Command("git", "fetch", "origin")
	fetchCmd.Dir = workDir
	fetchCmd.Stderr = &stderr
	fetchCmd.Env = os.Environ() // Inherit PATH to find git executable
	util.SetDetachedProcessGroup(fetchCmd)
	if err := fetchCmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		d.logger.Printf("Error: git fetch failed in %s: %s", workDir, errMsg)
		return // Fail fast - don't start agent with stale code
	}

	// Reset stderr buffer
	stderr.Reset()

	// Check if working tree is dirty before attempting pull.
	// "git pull --rebase" fails with "cannot pull with rebase: You have unstaged changes"
	// on dirty trees, so we auto-stash first and restore after.
	stashed := false
	if d.isWorkingTreeDirty(workDir) {
		d.logger.Printf("Warning: dirty working tree in %s, auto-stashing before pull", workDir)
		stashCmd := exec.Command("git", "stash", "push", "-u", "-m", "daemon-auto-stash: pre-sync")
		stashCmd.Dir = workDir
		stashCmd.Stderr = &stderr
		stashCmd.Env = os.Environ()
		util.SetDetachedProcessGroup(stashCmd)
		if err := stashCmd.Run(); err != nil {
			errMsg := strings.TrimSpace(stderr.String())
			if errMsg == "" {
				errMsg = err.Error()
			}
			d.logger.Printf("Warning: git stash failed in %s: %s, skipping pull", workDir, errMsg)
			d.recordSyncFailure(workDir)
			return
		}
		stashed = true
		stderr.Reset()
	}

	// Pull with rebase to incorporate changes
	pullCmd := exec.Command("git", "pull", "--rebase", "origin", defaultBranch)
	pullCmd.Dir = workDir
	pullCmd.Stderr = &stderr
	pullCmd.Env = os.Environ() // Inherit PATH to find git executable
	util.SetDetachedProcessGroup(pullCmd)
	if err := pullCmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		d.recordSyncFailure(workDir)
		failures := d.getSyncFailures(workDir)
		escalationThreshold := d.loadOperationalConfig().GetDaemonConfig().SyncFailureEscalationThresholdV()
		if failures >= escalationThreshold {
			d.logger.Printf("Error: git pull repeatedly failing in %s (%d consecutive failures): %s", workDir, failures, errMsg)
		} else {
			d.logger.Printf("Warning: git pull failed in %s (%d consecutive failure(s)): %s", workDir, failures, errMsg)
		}
	} else {
		// Pull succeeded - reset failure counter
		d.resetSyncFailures(workDir)
	}

	// Restore stashed changes if we stashed them
	if stashed {
		stderr.Reset()
		popCmd := exec.Command("git", "stash", "pop")
		popCmd.Dir = workDir
		popCmd.Stderr = &stderr
		popCmd.Env = os.Environ()
		util.SetDetachedProcessGroup(popCmd)
		if err := popCmd.Run(); err != nil {
			errMsg := strings.TrimSpace(stderr.String())
			if errMsg == "" {
				errMsg = err.Error()
			}
			d.logger.Printf("Warning: git stash pop failed in %s: %s (stashed changes preserved in stash list)", workDir, errMsg)
		}
	}

	// Note: With Dolt backend, beads changes are persisted immediately - no sync needed
}

// isWorkingTreeDirty checks if a git working tree has uncommitted changes.
func (d *Daemon) isWorkingTreeDirty(workDir string) bool {
	// "git status --porcelain" outputs nothing if clean
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	util.SetDetachedProcessGroup(cmd)
	output, err := cmd.Output()
	if err != nil {
		// If we can't check, assume dirty to be safe
		return true
	}
	return len(strings.TrimSpace(string(output))) > 0
}

// recordSyncFailure increments the consecutive failure counter for a workdir.
func (d *Daemon) recordSyncFailure(workDir string) {
	if d.syncFailures == nil {
		d.syncFailures = make(map[string]int)
	}
	d.syncFailures[workDir]++
}

// getSyncFailures returns the consecutive failure count for a workdir.
func (d *Daemon) getSyncFailures(workDir string) int {
	if d.syncFailures == nil {
		return 0
	}
	return d.syncFailures[workDir]
}

// resetSyncFailures clears the failure counter for a workdir after a successful sync.
func (d *Daemon) resetSyncFailures(workDir string) {
	if d.syncFailures == nil {
		return
	}
	delete(d.syncFailures, workDir)
}

// closeMessage removes a lifecycle mail message after processing.
// We use delete instead of read because gt mail read intentionally
// doesn't mark messages as read (to preserve handoff messages).
func (d *Daemon) closeMessage(id string) error {
	// Use gt mail delete to actually remove the message
	cmd := exec.Command(d.gtPath, "mail", "delete", id)
	cmd.Dir = d.config.TownRoot
	cmd.Env = os.Environ() // Inherit PATH to find gt executable
	util.SetDetachedProcessGroup(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gt mail delete %s: %v (output: %s)", id, err, string(output))
	}
	d.logger.Printf("Deleted lifecycle message: %s", id)
	return nil
}

// AgentBeadInfo represents the parsed fields from an agent bead.
type AgentBeadInfo struct {
	ID         string `json:"id"`
	Type       string `json:"issue_type"`
	State      string // From description agent_state, fallback to legacy DB column
	HookBead   string // From DB column (hook_bead)
	RoleType   string // Parsed from description: role_type
	Rig        string // Parsed from description: rig
	LastUpdate string `json:"updated_at"`
	// Note: RoleBead field removed - role definitions are now config-based
}

// getAgentBeadState reads non-observable agent state from an agent bead.
// Per gt-zecmc: Observable states (running, dead, idle) are derived from tmux.
// Only non-observable states (stuck, awaiting-gate, muted, paused) are stored in beads.
// Returns the agent_state field value or empty string if not found.
func (d *Daemon) getAgentBeadState(agentBeadID string) (string, error) {
	info, err := d.getAgentBeadInfo(agentBeadID)
	if err != nil {
		return "", err
	}
	return info.State, nil
}

// getAgentBeadInfo fetches and parses an agent bead by ID.
func (d *Daemon) getAgentBeadInfo(agentBeadID string) (*AgentBeadInfo, error) {
	cmd := exec.Command(d.bdPath, "show", agentBeadID, "--json")
	cmd.Dir = d.config.TownRoot
	cmd.Env = bdReadOnlyEnv()
	util.SetDetachedProcessGroup(cmd)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bd show %s: %w", agentBeadID, err)
	}

	// bd show --json returns an array with one element
	var issues []struct {
		ID          string   `json:"id"`
		Type        string   `json:"issue_type"`
		Labels      []string `json:"labels"`
		Description string   `json:"description"`
		UpdatedAt   string   `json:"updated_at"`
		HookBead    string   `json:"hook_bead"`   // Read from database column
		AgentState  string   `json:"agent_state"` // Read from database column
	}

	if err := json.Unmarshal(output, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd show output: %w", err)
	}

	if len(issues) == 0 {
		return nil, fmt.Errorf("agent bead not found: %s", agentBeadID)
	}

	issue := issues[0]
	if !beads.IsAgentBead(&beads.Issue{Type: issue.Type, Labels: issue.Labels}) {
		return nil, fmt.Errorf("bead %s is not an agent bead (type=%s)", agentBeadID, issue.Type)
	}

	// Parse agent fields from description for role/state info
	fields := beads.ParseAgentFields(issue.Description)

	info := &AgentBeadInfo{
		ID:         issue.ID,
		Type:       issue.Type,
		LastUpdate: issue.UpdatedAt,
	}

	if fields != nil {
		info.RoleType = fields.RoleType
		info.Rig = fields.Rig
	}

	info.State = beads.ResolveAgentState(issue.Description, issue.AgentState)

	// Use HookBead from database column directly (not from description)
	// The description may contain stale data - the slot is the source of truth.
	info.HookBead = issue.HookBead

	return info, nil
}

// getAgentHookBead re-reads the hook_bead for an agent bead from the database.
// Used for TOCTOU re-verification before taking destructive action on agents.
// Returns empty string on error or if no hook_bead is set.
func (d *Daemon) getAgentHookBead(agentBeadID string) string {
	cmd := exec.Command(d.bdPath, "show", agentBeadID, "--json")
	cmd.Dir = d.config.TownRoot
	cmd.Env = bdReadOnlyEnv()
	util.SetDetachedProcessGroup(cmd)

	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	var issues []struct {
		HookBead string `json:"hook_bead"`
	}
	if err := json.Unmarshal(output, &issues); err != nil || len(issues) == 0 {
		return ""
	}
	return issues[0].HookBead
}

// identityToAgentBeadID maps a daemon identity to an agent bead ID.
// Uses parseIdentity to extract components, then uses beads package helpers.
func (d *Daemon) identityToAgentBeadID(identity string) string {
	parsed, err := parseIdentity(identity)
	if err != nil {
		return ""
	}

	switch parsed.RoleType {
	case constants.RoleDeacon:
		return beads.DeaconBeadIDTown()
	case constants.RoleMayor:
		return beads.MayorBeadIDTown()
	case constants.RoleWitness:
		prefix := config.GetRigPrefix(d.config.TownRoot, parsed.RigName)
		return beads.WitnessBeadIDWithPrefix(prefix, parsed.RigName)
	case constants.RoleRefinery:
		prefix := config.GetRigPrefix(d.config.TownRoot, parsed.RigName)
		return beads.RefineryBeadIDWithPrefix(prefix, parsed.RigName)
	case constants.RoleCrew:
		prefix := config.GetRigPrefix(d.config.TownRoot, parsed.RigName)
		return beads.CrewBeadIDWithPrefix(prefix, parsed.RigName, parsed.AgentName)
	case constants.RolePolecat:
		prefix := config.GetRigPrefix(d.config.TownRoot, parsed.RigName)
		return beads.PolecatBeadIDWithPrefix(prefix, parsed.RigName, parsed.AgentName)
	default:
		return ""
	}
}

// NOTE: checkStaleAgents() and markAgentDead() were removed in gt-zecmc.
// Agent liveness is now discovered from tmux, not recorded in beads.
// "Discover, don't track" principle: observable state should not be recorded.

// identityToBDActor converts a daemon identity to BD_ACTOR format (with slashes).
// Uses parseIdentity to extract components, then builds the slash format.
func identityToBDActor(identity string) string {
	// Handle already-slash-formatted identities
	if strings.Contains(identity, "/polecats/") || strings.Contains(identity, "/crew/") ||
		strings.Contains(identity, "/witness") || strings.Contains(identity, "/refinery") {
		return identity
	}

	parsed, err := parseIdentity(identity)
	if err != nil {
		return identity // Unknown format - return as-is
	}

	switch parsed.RoleType {
	case constants.RoleMayor, constants.RoleDeacon:
		return parsed.RoleType
	case constants.RoleWitness:
		return parsed.RigName + "/witness"
	case constants.RoleRefinery:
		return parsed.RigName + "/refinery"
	case constants.RoleCrew:
		return parsed.RigName + "/crew/" + parsed.AgentName
	case constants.RolePolecat:
		return parsed.RigName + "/polecats/" + parsed.AgentName
	default:
		return identity
	}
}

// GUPPViolationTimeout is the canonical GUPP violation threshold.
// Defined in constants package — this alias avoids updating all call sites.
const GUPPViolationTimeout = constants.GUPPViolationTimeout

// listAgentBeadsJSON queries both the issues and wisps tables for agent beads
// and unmarshals the combined results into the provided slice pointer.
// The wisps query is best-effort (gracefully ignored if table doesn't exist).
func (d *Daemon) listAgentBeadsJSON(dest interface{}) error {
	// Query issues table (backward compat during migration)
	cmd := exec.Command(d.bdPath, "list", "--label=gt:agent", "--json", "--flat") //nolint:gosec // G204: bd is a trusted internal tool
	cmd.Dir = d.config.TownRoot
	cmd.Env = bdReadOnlyEnv()
	util.SetDetachedProcessGroup(cmd)

	issuesOutput, issuesErr := cmd.Output()

	// Query wisps table (primary source after agent bead migration)
	wispCmd := exec.Command(d.bdPath, "mol", "wisp", "list", "--json") //nolint:gosec // G204: bd is a trusted internal tool
	wispCmd.Dir = d.config.TownRoot
	wispCmd.Env = bdReadOnlyEnv()
	util.SetDetachedProcessGroup(wispCmd)

	wispOutput, _ := wispCmd.Output() // Best-effort: wisps table may not exist

	// Merge results: parse wisps first, then issues (wisps take precedence)
	combined := mergeAgentBeadJSON(wispOutput, issuesOutput)
	if combined == nil {
		if issuesErr != nil {
			return fmt.Errorf("bd list failed: %w", issuesErr)
		}
		return fmt.Errorf("no agent beads found")
	}

	return json.Unmarshal(combined, dest)
}

// mergeAgentBeadJSON merges JSON arrays from wisps and issues queries.
// Returns a combined JSON array with wisps taking precedence for duplicate IDs.
// Filters wisps to only include agent beads (type=agent or label gt:agent).
func mergeAgentBeadJSON(wispJSON, issuesJSON []byte) []byte {
	type agentEntry struct {
		ID     string   `json:"id"`
		Type   string   `json:"issue_type"`
		Labels []string `json:"labels"`
	}

	// Track IDs from wisps to avoid duplicates
	seenIDs := make(map[string]bool)
	var result []json.RawMessage

	// Parse wisps first (primary source)
	if len(wispJSON) > 0 {
		var wispEntries []json.RawMessage
		if json.Unmarshal(wispJSON, &wispEntries) == nil {
			for _, raw := range wispEntries {
				var entry agentEntry
				if json.Unmarshal(raw, &entry) == nil && beads.IsAgentBead(&beads.Issue{Type: entry.Type, Labels: entry.Labels}) {
					seenIDs[entry.ID] = true
					result = append(result, raw)
				}
			}
		}
	}

	// Then issues (backward compat, skip duplicates)
	if len(issuesJSON) > 0 {
		var issueEntries []json.RawMessage
		if json.Unmarshal(issuesJSON, &issueEntries) == nil {
			for _, raw := range issueEntries {
				var entry agentEntry
				if json.Unmarshal(raw, &entry) == nil && !seenIDs[entry.ID] {
					result = append(result, raw)
				}
			}
		}
	}

	if len(result) == 0 {
		return nil
	}

	combined, _ := json.Marshal(result)
	return combined
}

// checkGUPPViolations looks for agents that have work-on-hook but aren't
// progressing. This is a GUPP violation: agents with hooked work must execute.
// The daemon detects these and notifies the relevant Witness for remediation.
func (d *Daemon) checkGUPPViolations() {
	// Check if any rigs are operational before querying agent beads
	rigs := d.getKnownRigs()
	hasOperationalRig := false
	for _, rigName := range rigs {
		if operational, _ := d.isRigOperational(rigName); operational {
			hasOperationalRig = true
			break
		}
	}

	// Skip entirely if no rigs are operational (all docked/parked)
	if !hasOperationalRig {
		return
	}

	for _, rigName := range rigs {
		d.checkRigGUPPViolations(rigName)
	}
}

// checkRigGUPPViolations checks polecats in a specific rig for GUPP violations.
func (d *Daemon) checkRigGUPPViolations(rigName string) {
	// List polecat agent beads for this rig (issues + wisps tables)
	// Pattern: <prefix>-<rig>-polecat-<name> (e.g., gt-gastown-polecat-Toast)
	var agents []struct {
		ID          string   `json:"id"`
		Description string   `json:"description"`
		UpdatedAt   string   `json:"updated_at"`
		HookBead    string   `json:"hook_bead"` // Read from database column, not description
		AgentState  string   `json:"agent_state"`
		Labels      []string `json:"labels"`
		Type        string   `json:"issue_type"`
	}

	if err := d.listAgentBeadsJSON(&agents); err != nil {
		// Suppress warning when there are simply no agent beads (expected when all rigs are docked)
		d.logger.Printf("Warning: listing agent beads failed for GUPP check: %v", err)
		return
	}

	// Use the rig's configured prefix (e.g., "gt" for gastown, "bd" for beads)
	rigPrefix := config.GetRigPrefix(d.config.TownRoot, rigName)
	// Pattern: <prefix>-<rig>-polecat-<name>
	prefix := rigPrefix + "-" + rigName + "-polecat-"
	for _, agent := range agents {
		// Only check polecats for this rig
		if !strings.HasPrefix(agent.ID, prefix) {
			continue
		}

		// Check if agent has work on hook
		// Use HookBead from database column directly (not parsed from description)
		if agent.HookBead == "" {
			continue // No hooked work - no GUPP violation possible
		}

		agentState := beads.ResolveAgentState(agent.Description, agent.AgentState)

		// Skip nuked agents — they're intentionally terminated and should not
		// trigger alerts even if stale hook_bead data remains in the database.
		if beads.AgentState(agentState) == beads.AgentStateNuked {
			continue
		}

		// Per gt-zecmc: derive running state from tmux, not agent_state
		// Extract polecat name from agent ID (<prefix>-<rig>-polecat-<name> -> <name>)
		polecatName := strings.TrimPrefix(agent.ID, prefix)
		sessionName := session.PolecatSessionName(session.PrefixFor(rigName), polecatName)

		// Check if tmux session exists and agent is running
		if d.tmux.IsAgentAlive(sessionName) {
			// Session is alive - check if it's been stuck too long
			updatedAt, err := time.Parse(time.RFC3339, agent.UpdatedAt)
			if err != nil {
				continue
			}

			age := time.Since(updatedAt)
			if age > GUPPViolationTimeout {
				d.logger.Printf("GUPP violation: agent %s has hook_bead=%s but hasn't updated in %v (timeout: %v)",
					agent.ID, agent.HookBead, age.Round(time.Minute), GUPPViolationTimeout)

				// Notify the witness for this rig
				d.notifyWitnessOfGUPP(rigName, agent.ID, agent.HookBead, age)
			}
		}
	}
}

// notifyWitnessOfGUPP sends a mail to the rig's witness about a GUPP violation.
func (d *Daemon) notifyWitnessOfGUPP(rigName, agentID, hookBead string, stuckDuration time.Duration) {
	witnessAddr := rigName + "/witness"
	subject := fmt.Sprintf("GUPP_VIOLATION: %s stuck for %v", agentID, stuckDuration.Round(time.Minute))
	body := fmt.Sprintf(`Agent %s has work on hook but isn't progressing.

hook_bead: %s
stuck_duration: %v

Action needed: Check if agent is alive and responsive. Consider restarting if stuck.`,
		agentID, hookBead, stuckDuration.Round(time.Minute))

	cmd := exec.Command(d.gtPath, "mail", "send", witnessAddr, "-s", subject, "-m", body)
	cmd.Dir = d.config.TownRoot
	cmd.Env = os.Environ() // Inherit PATH to find gt executable
	util.SetDetachedProcessGroup(cmd)

	if err := cmd.Run(); err != nil {
		d.logger.Printf("Warning: failed to notify witness of GUPP violation: %v", err)
	} else {
		d.logger.Printf("Notified %s of GUPP violation for %s", witnessAddr, agentID)
	}
}

// checkOrphanedWork looks for work assigned to dead agents.
// Orphaned work needs to be reassigned or the agent needs to be restarted.
// Per gt-zecmc: derive agent liveness from tmux, not agent_state.
func (d *Daemon) checkOrphanedWork() {
	// Check if any rigs are operational before querying agent beads
	rigs := d.getKnownRigs()
	hasOperationalRig := false
	for _, rigName := range rigs {
		if operational, _ := d.isRigOperational(rigName); operational {
			hasOperationalRig = true
			break
		}
	}

	// Skip entirely if no rigs are operational (all docked/parked)
	if !hasOperationalRig {
		return
	}

	for _, rigName := range rigs {
		d.checkRigOrphanedWork(rigName)
	}
}

// checkRigOrphanedWork checks polecats in a specific rig for orphaned work.
func (d *Daemon) checkRigOrphanedWork(rigName string) {
	// List polecat agent beads (issues + wisps tables)
	var agents []struct {
		ID          string   `json:"id"`
		HookBead    string   `json:"hook_bead"`
		AgentState  string   `json:"agent_state"`
		Description string   `json:"description"`
		Labels      []string `json:"labels"`
		Type        string   `json:"issue_type"`
	}

	if err := d.listAgentBeadsJSON(&agents); err != nil {
		d.logger.Printf("Warning: listing agent beads failed for orphaned work check: %v", err)
		return
	}

	// Use the rig's configured prefix (e.g., "gt" for gastown, "bd" for beads)
	rigPrefix := config.GetRigPrefix(d.config.TownRoot, rigName)
	// Pattern: <prefix>-<rig>-polecat-<name>
	prefix := rigPrefix + "-" + rigName + "-polecat-"
	for _, agent := range agents {
		// Only check polecats for this rig
		if !strings.HasPrefix(agent.ID, prefix) {
			continue
		}

		// No hooked work = nothing to orphan
		if agent.HookBead == "" {
			continue
		}

		agentState := beads.ResolveAgentState(agent.Description, agent.AgentState)

		// Skip nuked agents — they're intentionally terminated and should not
		// trigger alerts even if stale hook_bead data remains in the database.
		if beads.AgentState(agentState) == beads.AgentStateNuked {
			continue
		}

		// Check if tmux session is alive (derive state from tmux, not bead)
		polecatName := strings.TrimPrefix(agent.ID, prefix)
		sessionName := session.PolecatSessionName(session.PrefixFor(rigName), polecatName)

		// Session running = not orphaned (work is being processed)
		if d.tmux.IsAgentAlive(sessionName) {
			continue
		}

		// TOCTOU guard: re-verify agent state before taking action.
		// Between the bd list above and now, the agent may have been
		// restarted or its hook_bead cleared. Re-check both conditions.
		if d.tmux.IsAgentAlive(sessionName) {
			continue
		}
		currentHookBead := d.getAgentHookBead(agent.ID)
		if currentHookBead == "" {
			continue
		}

		// Session dead but has hooked work = orphaned!
		d.logger.Printf("Orphaned work detected: agent %s session is dead but has hook_bead=%s",
			agent.ID, currentHookBead)

		d.notifyWitnessOfOrphanedWork(rigName, agent.ID, currentHookBead)
	}
}

// extractRigFromAgentID extracts the rig name from a polecat agent ID.
// Example: gt-gastown-polecat-max → gastown
func (d *Daemon) extractRigFromAgentID(agentID string) string {
	// Use the beads package helper to correctly parse agent bead IDs.
	// Pattern: <prefix>-<rig>-polecat-<name> (e.g., gt-gastown-polecat-Toast)
	rig, role, _, ok := beads.ParseAgentBeadID(agentID)
	if !ok || role != constants.RolePolecat {
		return ""
	}
	return rig
}

// notifyWitnessOfOrphanedWork sends a mail to the rig's witness about orphaned work.
func (d *Daemon) notifyWitnessOfOrphanedWork(rigName, agentID, hookBead string) {
	witnessAddr := rigName + "/witness"
	subject := fmt.Sprintf("ORPHANED_WORK: %s has hooked work but is dead", agentID)
	body := fmt.Sprintf(`Agent %s is dead but has work on its hook.

hook_bead: %s

Action needed: Either restart the agent or reassign the work.`,
		agentID, hookBead)

	cmd := exec.Command(d.gtPath, "mail", "send", witnessAddr, "-s", subject, "-m", body)
	cmd.Dir = d.config.TownRoot
	cmd.Env = os.Environ() // Inherit PATH to find gt executable
	util.SetDetachedProcessGroup(cmd)

	if err := cmd.Run(); err != nil {
		d.logger.Printf("Warning: failed to notify witness of orphaned work: %v", err)
	} else {
		d.logger.Printf("Notified %s of orphaned work for %s", witnessAddr, agentID)
	}
}
