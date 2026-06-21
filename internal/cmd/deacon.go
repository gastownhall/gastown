package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/runtime"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/util"
	"github.com/steveyegge/gastown/internal/workspace"
)

// getDeaconSessionName returns the Deacon session name.
func getDeaconSessionName() string {
	return session.DeaconSessionName()
}

var deaconCmd = &cobra.Command{
	Use:     "deacon",
	Aliases: []string{"dea"},
	GroupID: GroupAgents,
	Short:   "Manage the Deacon (town-level watchdog)",
	RunE:    requireSubcommand,
	Long: `Manage the Deacon - the town-level watchdog for Gas Town.

The Deacon ("daemon beacon") is the only agent that receives mechanical
heartbeats from the daemon. It monitors system health across all rigs:
  - Watches all Witnesses (are they alive? stuck? responsive?)
  - Manages Dogs for cross-rig infrastructure work
  - Handles lifecycle requests (respawns, restarts)
  - Receives heartbeat pokes and decides what needs attention

The Deacon patrols the town; Witnesses patrol their rigs; Polecats work.

Role shortcuts: "deacon" in mail/nudge addresses resolves to this agent.`,
}

var deaconStartCmd = &cobra.Command{
	Use:     "start",
	Aliases: []string{"spawn"},
	Short:   "Start the Deacon session",
	Long: `Start the Deacon tmux session.

Creates a new detached tmux session for the Deacon and launches Claude.
The session runs in the workspace root directory.`,
	RunE: runDeaconStart,
}

var deaconStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the Deacon session",
	Long: `Stop the Deacon tmux session.

Attempts graceful shutdown first (Ctrl-C), then kills the tmux session.`,
	RunE: runDeaconStop,
}

var deaconAttachCmd = &cobra.Command{
	Use:     "attach",
	Aliases: []string{"at"},
	Short:   "Attach to the Deacon session",
	Long: `Attach to the running Deacon tmux session.

Attaches the current terminal to the Deacon's tmux session.
Detach with Ctrl-B D.`,
	RunE: runDeaconAttach,
}

var deaconStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check Deacon session status",
	Long: `Check if the Deacon tmux session is currently running.

Shows whether the Deacon has an active tmux session and reports
its session name. The Deacon is the town-level watchdog that
receives heartbeats from the daemon.

Examples:
  gt deacon status`,
	RunE: runDeaconStatus,
}

var deaconRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the Deacon session",
	Long: `Restart the Deacon tmux session.

Stops the current session (if running) and starts a fresh one.`,
	RunE: runDeaconRestart,
}

var deaconAgentOverride string

var deaconHeartbeatCmd = &cobra.Command{
	Use:   "heartbeat [action]",
	Short: "Update the Deacon heartbeat",
	Long: `Update the Deacon heartbeat file.

The heartbeat signals to the daemon that the Deacon is alive and working.
Call this at the start of each wake cycle to prevent daemon pokes.

Examples:
  gt deacon heartbeat                    # Touch heartbeat with timestamp
  gt deacon heartbeat "checking mayor"   # Touch with action description`,
	RunE: runDeaconHeartbeat,
}

var deaconHealthCheckCmd = &cobra.Command{
	Use:   "health-check <agent>",
	Short: "Send a health check ping to an agent and track response",
	Long: `Send a HEALTH_CHECK nudge to an agent and wait for response.

This command is used by the Deacon during health rounds to detect stuck sessions.
It tracks consecutive failures and determines when force-kill is warranted.

The detection protocol:
1. Send HEALTH_CHECK nudge to the agent
2. Wait for agent to update their bead (configurable timeout, default 30s)
3. If no activity update, increment failure counter
4. After N consecutive failures (default 3), recommend force-kill

Exit codes:
  0 - Agent responded or is in cooldown (no action needed)
  1 - Error occurred
  2 - Agent should be force-killed (consecutive failures exceeded)

Examples:
  gt deacon health-check gastown/polecats/max
  gt deacon health-check gastown/witness --timeout=60s
  gt deacon health-check deacon --failures=5`,
	Args: cobra.ExactArgs(1),
	RunE: runDeaconHealthCheck,
}

var deaconForceKillCmd = &cobra.Command{
	Use:   "force-kill <agent>",
	Short: "Force-kill an unresponsive agent session",
	Long: `Force-kill an agent session that has been detected as stuck.

This command is used by the Deacon when an agent fails consecutive health checks.
It performs the force-kill protocol:

1. Log the intervention (send mail to agent)
2. Kill the tmux session
3. Update agent bead state to "killed"
4. Notify mayor (optional, for visibility)

After force-kill, the agent is 'asleep'. Normal wake mechanisms apply:
- gt rig boot restarts it
- Or stays asleep until next activity trigger

This respects the cooldown period - won't kill if recently killed.

Examples:
  gt deacon force-kill gastown/polecats/max
  gt deacon force-kill gastown/witness --reason="unresponsive for 90s"`,
	Args: cobra.ExactArgs(1),
	RunE: runDeaconForceKill,
}

var deaconHealthStateCmd = &cobra.Command{
	Use:   "health-state",
	Short: "Show health check state for all monitored agents",
	Long: `Display the current health check state including:
- Consecutive failure counts
- Last ping and response times
- Force-kill history and cooldowns

This helps the Deacon understand which agents may need attention.`,
	RunE: runDeaconHealthState,
}

var deaconStaleHooksCmd = &cobra.Command{
	Use:   "stale-hooks",
	Short: "Find and unhook stale hooked beads",
	Long: `Find beads stuck in 'hooked' status and unhook them if the agent is gone.

Beads can get stuck in 'hooked' status when agents die or abandon work.
This command finds hooked beads older than the threshold (default: 1 hour),
checks if the assignee agent is still alive, and unhooks them if not.

Examples:
  gt deacon stale-hooks                 # Find and unhook stale beads
  gt deacon stale-hooks --dry-run       # Preview what would be unhooked
  gt deacon stale-hooks --max-age=30m   # Use 30 minute threshold`,
	RunE: runDeaconStaleHooks,
}

var deaconPauseCmd = &cobra.Command{
	Use:   "pause",
	Short: "Pause the Deacon to prevent patrol actions",
	Long: `Pause the Deacon to prevent it from performing any patrol actions.

When paused, the Deacon:
- Will not create patrol molecules
- Will not run health checks
- Will not take any autonomous actions
- Will display a PAUSED message on startup

The pause state persists across session restarts. Use 'gt deacon resume'
to allow the Deacon to work again.

Examples:
  gt deacon pause                           # Pause with no reason
  gt deacon pause --reason="testing"        # Pause with a reason`,
	RunE: runDeaconPause,
}

var deaconResumeCmd = &cobra.Command{
	Use:   "resume",
	Short: "Resume the Deacon to allow patrol actions",
	Long: `Resume the Deacon so it can perform patrol actions again.

This removes the pause file and allows the Deacon to work normally.`,
	RunE: runDeaconResume,
}

var deaconCleanupOrphansCmd = &cobra.Command{
	Use:   "cleanup-orphans",
	Short: "Clean up orphaned claude subagent processes",
	Long: `Clean up orphaned claude subagent processes.

Claude Code's Task tool spawns subagent processes that sometimes don't clean up
properly after completion. These accumulate and consume significant memory.

Detection is based on TTY column: processes with TTY "?" have no controlling
terminal. Legitimate claude instances in terminals have a TTY like "pts/0".

This is safe because:
- Processes in terminals (your personal sessions) have a TTY - won't be touched
- Only kills processes that have no controlling terminal
- These orphans are children of the tmux server with no TTY

Example:
  gt deacon cleanup-orphans`,
	RunE: runDeaconCleanupOrphans,
}

var deaconZombieScanCmd = &cobra.Command{
	Use:        "zombie-scan",
	SuggestFor: []string{"orphan-scan", "orphan_scan", "orphan"},
	Short:      "Find and clean zombie Claude processes not in active tmux sessions",
	Long: `Find and clean zombie Claude processes not in active tmux sessions.

Unlike cleanup-orphans (which uses TTY detection), zombie-scan uses tmux
verification: it checks if each Claude process is in an active tmux session
by comparing against actual pane PIDs.

A process is a zombie if:
- It's a Claude/codex process
- It's NOT the pane PID of any active tmux session
- It's NOT a child of any pane PID
- It's older than 60 seconds

This catches "ghost" processes that have a TTY (from a dead tmux session)
but are no longer part of any active Gas Town session.

Examples:
  gt deacon zombie-scan           # Find and kill zombies
  gt deacon zombie-scan --dry-run # Just list zombies, don't kill`,
	RunE: runDeaconZombieScan,
}

var deaconRedispatchCmd = &cobra.Command{
	Use:   "redispatch <bead-id>",
	Short: "Re-dispatch a recovered bead to an available polecat",
	Long: `Re-dispatch a recovered bead from a dead polecat to an available polecat.

When the Witness detects a dead polecat with abandoned work, it resets the bead
to open status and sends a RECOVERED_BEAD mail to the Deacon. This command
handles the re-dispatch:

1. Checks re-dispatch state (how many times this bead has been re-dispatched)
2. Rate-limits to prevent thrashing (cooldown between re-dispatches)
3. If under the limit: runs 'gt sling <bead> <rig>' to re-dispatch
4. If over the limit: escalates to Mayor instead of re-slinging

Exit codes:
  0 - Bead successfully re-dispatched or escalated
  1 - Error occurred
  2 - Bead in cooldown (try again later)
  3 - Bead skipped (already claimed or non-open status)

Examples:
  gt deacon redispatch gt-abc123                    # Auto-detect rig from prefix
  gt deacon redispatch gt-abc123 --rig gastown      # Explicit target rig
  gt deacon redispatch gt-abc123 --max-attempts 5   # Allow 5 attempts before escalation
  gt deacon redispatch gt-abc123 --cooldown 10m     # 10 minute cooldown between attempts`,
	Args: cobra.ExactArgs(1),
	RunE: runDeaconRedispatch,
}

var deaconRedispatchStateCmd = &cobra.Command{
	Use:   "redispatch-state",
	Short: "Show re-dispatch state for recovered beads",
	Long: `Display the current re-dispatch tracking state including:
- Attempt counts per bead
- Cooldown status
- Escalation history

This helps the Deacon understand which recovered beads need attention.`,
	RunE: runDeaconRedispatchState,
}

var deaconFeedStrandedCmd = &cobra.Command{
	Use:   "feed-stranded",
	Short: "Detect and feed stranded convoys automatically",
	Long: `Detect stranded convoys and take mechanical actions where safe.

A convoy is "stranded" when it is open AND either:
- Has ready issues (open, unblocked, no assignee) but no workers
- Has 0 tracked issues (empty — needs auto-close)
- Has tracked issues but none are ready (needs agent review)

This command:
1. Runs 'gt convoy stranded --json' to find stranded convoys
2. For feedable convoys (ready_count > 0): dispatches a dog via gt sling
3. For empty convoys (tracked_count == 0): auto-closes via gt convoy check
4. For tracked-but-not-ready convoys: surfaces raw data for deacon review
5. Rate limits to avoid spawning too many dogs at once

Rate limiting:
- Per-cycle limit (default 3): max convoys fed per invocation
- Per-convoy cooldown (default 10m): prevents re-feeding before dog finishes

This is called by the Deacon during patrol. Run manually for debugging.

Examples:
  gt deacon feed-stranded                  # Feed stranded convoys
  gt deacon feed-stranded --max-feeds 5    # Allow up to 5 feeds per cycle
  gt deacon feed-stranded --cooldown 5m    # 5 minute per-convoy cooldown
  gt deacon feed-stranded --json           # Machine-readable output`,
	RunE: runDeaconFeedStranded,
}

var deaconFeedStrandedStateCmd = &cobra.Command{
	Use:   "feed-stranded-state",
	Short: "Show feed-stranded state for tracked convoys",
	Long: `Display the current feed-stranded tracking state including:
- Feed counts per convoy
- Cooldown status
- Last feed times

This helps the Deacon understand which convoys have been recently fed.`,
	RunE: runDeaconFeedStrandedState,
}

var (
	// Status flags
	deaconStatusJSON bool

	// Health check flags
	healthCheckTimeout  time.Duration
	healthCheckFailures int
	healthCheckCooldown time.Duration

	// Force kill flags
	forceKillReason     string
	forceKillSkipNotify bool

	// Stale hooks flags
	staleHooksMaxAge time.Duration
	staleHooksDryRun bool

	// Pause flags
	pauseReason string

	// Zombie scan flags
	zombieScanDryRun bool

	// Redispatch flags
	redispatchRig         string
	redispatchMaxAttempts int
	redispatchCooldown    time.Duration

	// Feed-stranded flags
	feedStrandedMaxFeeds int
	feedStrandedCooldown time.Duration
	feedStrandedJSON     bool
)

func init() {
	deaconCmd.AddCommand(deaconStartCmd)
	deaconCmd.AddCommand(deaconStopCmd)
	deaconCmd.AddCommand(deaconAttachCmd)
	deaconCmd.AddCommand(deaconStatusCmd)
	deaconCmd.AddCommand(deaconRestartCmd)
	deaconCmd.AddCommand(deaconHeartbeatCmd)
	deaconCmd.AddCommand(deaconHealthCheckCmd)
	deaconCmd.AddCommand(deaconForceKillCmd)
	deaconCmd.AddCommand(deaconHealthStateCmd)
	deaconCmd.AddCommand(deaconStaleHooksCmd)
	deaconCmd.AddCommand(deaconPauseCmd)
	deaconCmd.AddCommand(deaconResumeCmd)
	deaconCmd.AddCommand(deaconCleanupOrphansCmd)
	deaconCmd.AddCommand(deaconZombieScanCmd)
	deaconCmd.AddCommand(deaconRedispatchCmd)
	deaconCmd.AddCommand(deaconRedispatchStateCmd)
	deaconCmd.AddCommand(deaconFeedStrandedCmd)
	deaconCmd.AddCommand(deaconFeedStrandedStateCmd)

	// Flags for status
	deaconStatusCmd.Flags().BoolVar(&deaconStatusJSON, "json", false, "Output as JSON")

	// Flags for health-check
	deaconHealthCheckCmd.Flags().DurationVar(&healthCheckTimeout, "timeout", 30*time.Second,
		"How long to wait for agent response")
	deaconHealthCheckCmd.Flags().IntVar(&healthCheckFailures, "failures", 3,
		"Number of consecutive failures before recommending force-kill")
	deaconHealthCheckCmd.Flags().DurationVar(&healthCheckCooldown, "cooldown", 5*time.Minute,
		"Minimum time between force-kills of same agent")

	// Flags for force-kill
	deaconForceKillCmd.Flags().StringVar(&forceKillReason, "reason", "",
		"Reason for force-kill (included in notifications)")
	deaconForceKillCmd.Flags().BoolVar(&forceKillSkipNotify, "skip-notify", false,
		"Skip sending notification mail to mayor")

	// Flags for stale-hooks
	deaconStaleHooksCmd.Flags().DurationVar(&staleHooksMaxAge, "max-age", 1*time.Hour,
		"Maximum age before a hooked bead is considered stale")
	deaconStaleHooksCmd.Flags().BoolVar(&staleHooksDryRun, "dry-run", false,
		"Preview what would be unhooked without making changes")

	// Flags for pause
	deaconPauseCmd.Flags().StringVar(&pauseReason, "reason", "",
		"Reason for pausing the Deacon")

	// Flags for zombie-scan
	deaconZombieScanCmd.Flags().BoolVar(&zombieScanDryRun, "dry-run", false,
		"List zombies without killing them")

	// Flags for redispatch
	deaconRedispatchCmd.Flags().StringVar(&redispatchRig, "rig", "",
		"Target rig to re-dispatch to (auto-detected from bead prefix if omitted)")
	deaconRedispatchCmd.Flags().IntVar(&redispatchMaxAttempts, "max-attempts", 0,
		"Max re-dispatch attempts before escalating to Mayor (default: 3)")
	deaconRedispatchCmd.Flags().DurationVar(&redispatchCooldown, "cooldown", 0,
		"Minimum time between re-dispatches of same bead (default: 5m)")

	// Flags for feed-stranded
	deaconFeedStrandedCmd.Flags().IntVar(&feedStrandedMaxFeeds, "max-feeds", 0,
		"Max convoys to feed per invocation (default: 3)")
	deaconFeedStrandedCmd.Flags().DurationVar(&feedStrandedCooldown, "cooldown", 0,
		"Minimum time between feeds of same convoy (default: 10m)")
	deaconFeedStrandedCmd.Flags().BoolVar(&feedStrandedJSON, "json", false,
		"Output results as JSON")

	deaconStartCmd.Flags().StringVar(&deaconAgentOverride, "agent", "", "Agent alias to run the Deacon with (overrides town default)")
	deaconAttachCmd.Flags().StringVar(&deaconAgentOverride, "agent", "", "Agent alias to run the Deacon with (overrides town default)")
	deaconRestartCmd.Flags().StringVar(&deaconAgentOverride, "agent", "", "Agent alias to run the Deacon with (overrides town default)")

	rootCmd.AddCommand(deaconCmd)
}

func runDeaconStart(cmd *cobra.Command, args []string) error {
	t := tmux.NewTmux()

	sessionName := getDeaconSessionName()

	// Check if session already exists
	running, err := t.HasSession(sessionName)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if running {
		return fmt.Errorf("Deacon session already running. Attach with: gt deacon attach")
	}

	if err := startDeaconSession(t, sessionName, deaconAgentOverride); err != nil {
		return err
	}

	fmt.Printf("%s Deacon session started. Attach with: %s\n",
		style.Bold.Render("✓"),
		style.Dim.Render("gt deacon attach"))

	return nil
}

// startDeaconSession creates and initializes the Deacon tmux session.
func startDeaconSession(t *tmux.Tmux, sessionName, agentOverride string) error {
	// Find workspace root
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Deacon runs from its own directory (for correct role detection by gt prime)
	deaconDir := filepath.Join(townRoot, "deacon")

	// Ensure deacon directory exists
	if err := os.MkdirAll(deaconDir, 0755); err != nil {
		return fmt.Errorf("creating deacon directory: %w", err)
	}

	// Resolve CLAUDE_CONFIG_DIR from accounts.json so deacon sessions
	// use the correct account. Mirrors the daemon restart path (lifecycle.go).
	accountsPath := constants.MayorAccountsPath(townRoot)
	runtimeConfigDir, _, _ := config.ResolveAccountConfigDir(accountsPath, "")
	if runtimeConfigDir == "" {
		runtimeConfigDir = os.Getenv("CLAUDE_CONFIG_DIR")
	}

	// Ensure runtime settings exist (autonomous role needs mail in SessionStart)
	runtimeConfig := config.ResolveRoleAgentConfig("deacon", townRoot, deaconDir)
	if err := runtime.EnsureSettingsForRole(deaconDir, deaconDir, "deacon", runtimeConfig); err != nil {
		return fmt.Errorf("ensuring runtime settings: %w", err)
	}

	initialPrompt := session.BuildStartupPrompt(session.BeaconConfig{
		Recipient: "deacon",
		Sender:    "daemon",
		Topic:     "patrol",
	}, "I am Deacon. First run `gt deacon heartbeat`. Then check gt hook, and if it is empty run `gt sling mol-deacon-patrol deacon`, then execute the hook it creates. After patrol completes, return to idle - the daemon's event loop manages waiting and respawning.")
	startupCmd, err := config.BuildStartupCommandFromConfig(config.AgentEnvConfig{
		Role:             "deacon",
		TownRoot:         townRoot,
		RuntimeConfigDir: runtimeConfigDir,
		Prompt:           initialPrompt,
		Topic:            "patrol",
		SessionName:      sessionName,
	}, "", initialPrompt, agentOverride)
	if err != nil {
		return fmt.Errorf("building startup command: %w", err)
	}

	// Create session with command directly to avoid send-keys race condition.
	// See: https://github.com/anthropics/gastown/issues/280
	fmt.Println("Starting Deacon session...")
	if err := t.NewSessionWithCommand(sessionName, deaconDir, startupCmd); err != nil {
		return fmt.Errorf("creating session: %w", err)
	}

	// Set environment (non-fatal: session works without these)
	// Use centralized AgentEnv for consistency across all role startup paths
	envVars := config.AgentEnv(config.AgentEnvConfig{
		Role:             "deacon",
		TownRoot:         townRoot,
		RuntimeConfigDir: runtimeConfigDir,
		Agent:            agentOverride,
	})
	for k, v := range envVars {
		_ = t.SetEnvironment(sessionName, k, v)
	}

	// Record agent's pane_id for ZFC-compliant liveness checks (gt-qmsx).
	if paneID, err := t.GetPaneID(sessionName); err == nil {
		_ = t.SetEnvironment(sessionName, "GT_PANE_ID", paneID)
	}

	// Apply Deacon theme (non-fatal: theming failure doesn't affect operation)
	// Note: ConfigureGasTownSession includes cycle bindings
	theme := tmux.ResolveSessionTheme(townRoot, "", "deacon", "")
	_ = t.ConfigureGasTownSession(sessionName, theme, "", "Deacon", "health-check")

	// Wait for Claude to start
	if err := t.WaitForCommand(sessionName, constants.SupportedShells, constants.ClaudeStartTimeout); err != nil {
		return fmt.Errorf("waiting for deacon to start: %w", err)
	}

	// Accept startup dialogs (workspace trust + bypass permissions) if they appear.
	_ = t.AcceptStartupDialogs(sessionName)

	time.Sleep(constants.ShutdownNotifyDelay)

	deaconTownRoot, _ := workspace.FindFromCwdOrError()
	runtimeCfg := config.ResolveRoleAgentConfig("deacon", deaconTownRoot, "")
	_ = runtime.RunStartupFallback(t, sessionName, "deacon", runtimeCfg)

	return nil
}

func runDeaconStop(cmd *cobra.Command, args []string) error {
	t := tmux.NewTmux()

	sessionName := getDeaconSessionName()

	// Check if session exists
	running, err := t.HasSession(sessionName)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return errors.New("Deacon session is not running")
	}

	fmt.Println("Stopping Deacon session...")

	// Try graceful shutdown first (best-effort interrupt)
	_ = t.SendKeysRaw(sessionName, "C-c")
	time.Sleep(100 * time.Millisecond)

	// Kill the session.
	// Use KillSessionWithProcesses to ensure all descendant processes are killed.
	if err := t.KillSessionWithProcesses(sessionName); err != nil {
		return fmt.Errorf("killing session: %w", err)
	}

	fmt.Printf("%s Deacon session stopped.\n", style.Bold.Render("✓"))
	return nil
}

func runDeaconAttach(cmd *cobra.Command, args []string) error {
	t := tmux.NewTmux()

	sessionName := getDeaconSessionName()

	// Check if session exists
	running, err := t.HasSession(sessionName)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if !running {
		// Auto-start if not running
		fmt.Println("Deacon session not running, starting...")
		if err := startDeaconSession(t, sessionName, deaconAgentOverride); err != nil {
			return err
		}
	}
	// Session uses a respawn loop, so Claude restarts automatically if it exits

	// Use shared attach helper (smart: links if inside tmux, attaches if outside)
	return attachToTmuxSession(sessionName)
}

// DeaconStatusOutput is the JSON-serializable status of the Deacon.
type DeaconStatusOutput struct {
	Running   bool             `json:"running"`
	Paused    bool             `json:"paused"`
	Session   string           `json:"session"`
	Heartbeat *HeartbeatStatus `json:"heartbeat,omitempty"`
}

// HeartbeatStatus is the JSON-serializable heartbeat info.
type HeartbeatStatus struct {
	Timestamp  time.Time `json:"timestamp"`
	AgeSec     float64   `json:"age_seconds"`
	Cycle      int64     `json:"cycle"`
	LastAction string    `json:"last_action,omitempty"`
	Fresh      bool      `json:"fresh"`
	Stale      bool      `json:"stale"`
	VeryStale  bool      `json:"very_stale"`
}

func runDeaconStatus(cmd *cobra.Command, args []string) error {
	t := tmux.NewTmux()

	sessionName := getDeaconSessionName()
	townRoot, _ := workspace.FindFromCwdOrError()

	// Gather state
	paused := false
	var pauseState *deacon.PauseState
	if townRoot != "" {
		var err error
		paused, pauseState, err = deacon.IsPaused(townRoot)
		if err != nil {
			paused = false
		}
	}

	running, err := t.HasSession(sessionName)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}

	// Read heartbeat
	var hbStatus *HeartbeatStatus
	if townRoot != "" {
		if hb := deacon.ReadHeartbeat(townRoot); hb != nil {
			hbStatus = &HeartbeatStatus{
				Timestamp:  hb.Timestamp,
				AgeSec:     hb.Age().Seconds(),
				Cycle:      hb.Cycle,
				LastAction: hb.LastAction,
				Fresh:      hb.IsFresh(),
				Stale:      hb.IsStale(),
				VeryStale:  hb.IsVeryStale(),
			}
		}
	}

	// JSON output
	if deaconStatusJSON {
		out := DeaconStatusOutput{
			Running:   running,
			Paused:    paused,
			Session:   sessionName,
			Heartbeat: hbStatus,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	// Human-readable output
	if paused && pauseState != nil {
		fmt.Printf("%s DEACON PAUSED\n", style.Bold.Render("⏸️"))
		if pauseState.Reason != "" {
			fmt.Printf("  Reason: %s\n", pauseState.Reason)
		}
		fmt.Printf("  Paused at: %s\n", pauseState.PausedAt.Format(time.RFC3339))
		fmt.Printf("  Paused by: %s\n", pauseState.PausedBy)
		fmt.Println()
		fmt.Printf("Resume with: %s\n", style.Dim.Render("gt deacon resume"))
		fmt.Println()
	}

	if running {
		// Get session info for more details
		info, err := t.GetSessionInfo(sessionName)
		if err == nil {
			status := "detached"
			if info.Attached {
				status = "attached"
			}
			fmt.Printf("%s Deacon session is %s\n",
				style.Bold.Render("●"),
				style.Bold.Render("running"))
			fmt.Printf("  Status: %s\n", status)
			fmt.Printf("  Created: %s\n", info.Created)
		} else {
			fmt.Printf("%s Deacon session is %s\n",
				style.Bold.Render("●"),
				style.Bold.Render("running"))
		}
	} else {
		fmt.Printf("%s Deacon session is %s\n",
			style.Dim.Render("○"),
			"not running")
		fmt.Printf("\nStart with: %s\n", style.Dim.Render("gt deacon start"))
	}

	// Heartbeat info (shown after session status)
	if hbStatus != nil {
		fmt.Println()
		ageDur := time.Duration(hbStatus.AgeSec * float64(time.Second))
		fmt.Printf("  Heartbeat: %s ago (cycle %d)\n",
			ageDur.Round(time.Second), hbStatus.Cycle)
		if hbStatus.LastAction != "" {
			fmt.Printf("  Last action: %s\n", hbStatus.LastAction)
		}
		health := "fresh"
		if hbStatus.VeryStale {
			health = "very stale"
		} else if hbStatus.Stale {
			health = "stale"
		}
		fmt.Printf("  Health: %s\n", health)
	} else if townRoot != "" {
		fmt.Println()
		fmt.Printf("  Heartbeat: %s\n", style.Dim.Render("no heartbeat file"))
	}

	if running {
		fmt.Printf("\nAttach with: %s\n", style.Dim.Render("gt deacon attach"))
	}

	return nil
}

func runDeaconRestart(cmd *cobra.Command, args []string) error {
	t := tmux.NewTmux()

	sessionName := getDeaconSessionName()

	running, err := t.HasSession(sessionName)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}

	fmt.Println("Restarting Deacon...")

	if running {
		// Kill existing session.
		// Use KillSessionWithProcesses to ensure all descendant processes are killed.
		if err := t.KillSessionWithProcesses(sessionName); err != nil {
			style.PrintWarning("failed to kill session: %v", err)
		}
	}

	// Start fresh
	if err := runDeaconStart(cmd, args); err != nil {
		return err
	}

	fmt.Printf("%s Deacon restarted\n", style.Bold.Render("✓"))
	fmt.Printf("  %s\n", style.Dim.Render("Use 'gt deacon attach' to connect"))
	return nil
}

func runDeaconHeartbeat(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Check if Deacon is paused - if so, refuse to update heartbeat
	paused, state, err := deacon.IsPaused(townRoot)
	if err != nil {
		return fmt.Errorf("checking pause state: %w", err)
	}
	if paused {
		fmt.Printf("%s Deacon is paused. Use 'gt deacon resume' to unpause.\n", style.Bold.Render("⏸️"))
		if state.Reason != "" {
			fmt.Printf("  Reason: %s\n", state.Reason)
		}
		return errors.New("Deacon is paused")
	}

	action := ""
	if len(args) > 0 {
		action = strings.Join(args, " ")
	}

	if action != "" {
		if err := deacon.TouchWithAction(townRoot, action, 0, 0); err != nil {
			return fmt.Errorf("updating heartbeat: %w", err)
		}
		fmt.Printf("%s Heartbeat updated: %s\n", style.Bold.Render("✓"), action)
	} else {
		if err := deacon.Touch(townRoot); err != nil {
			return fmt.Errorf("updating heartbeat: %w", err)
		}
		fmt.Printf("%s Heartbeat updated\n", style.Bold.Render("✓"))
	}

	return nil
}

// runDeaconHealthCheck implements the health-check command.
// It sends a HEALTH_CHECK nudge to an agent, waits for response, and tracks state.
func runDeaconHealthCheck(cmd *cobra.Command, args []string) error {
	agent := args[0]

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load health check state
	state, err := deacon.LoadHealthCheckState(townRoot)
	if err != nil {
		return fmt.Errorf("loading health check state: %w", err)
	}
	agentState := state.GetAgentState(agent)

	// Check if agent is in cooldown
	if agentState.IsInCooldown(healthCheckCooldown) {
		remaining := agentState.CooldownRemaining(healthCheckCooldown)
		fmt.Printf("%s Agent %s is in cooldown (remaining: %s)\n",
			style.Dim.Render("○"), agent, remaining.Round(time.Second))
		return nil
	}

	// Get agent bead info before ping (for baseline)
	beadID, sessionName, err := agentAddressToIDs(agent)
	if err != nil {
		return fmt.Errorf("invalid agent address: %w", err)
	}

	t := tmux.NewTmux()

	// Check if session exists
	exists, err := t.HasSession(sessionName)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if !exists {
		fmt.Printf("%s Agent %s session not running\n", style.Dim.Render("○"), agent)
		return nil
	}

	// Record ping
	agentState.RecordPing()

	// Send health check nudge via immediate delivery (not queued).
	// Health checks MUST interrupt to test liveness — queued delivery would
	// defer until the next turn boundary, causing the 30s timeout to expire
	// and producing false negatives that kill healthy agents.
	healthMsg := "HEALTH_CHECK: respond with any action to confirm responsiveness"
	if err := t.NudgeSession(sessionName, healthMsg); err != nil {
		return fmt.Errorf("sending health check nudge: %w", err)
	}

	// Get baseline times AFTER sending nudge to avoid false positives.
	// By sampling after the nudge, we only detect activity caused by our check.
	baselineTime, err := getAgentBeadUpdateTime(townRoot, beadID)
	if err != nil {
		// Bead might not exist yet - use current time as baseline
		// This way only updates AFTER this point count as responses
		baselineTime = time.Now()
	}

	// Also capture baseline tmux session activity time.
	// This is the secondary response signal: if the session shows new output
	// after our nudge, the agent is alive and processing — even if it hasn't
	// updated its bead (e.g., witness agents that respond in prose rather than
	// via a structured bead-update channel).
	baselineActivity, activityErr := t.GetSessionActivity(sessionName)

	fmt.Printf("%s Sent HEALTH_CHECK to %s, waiting %s...\n",
		style.Bold.Render("→"), agent, healthCheckTimeout)

	// Wait for response using context and ticker for reliability
	// This prevents loop hangs if system clock changes
	ctx, cancel := context.WithTimeout(context.Background(), healthCheckTimeout)
	defer cancel()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	responded := false

	for {
		select {
		case <-ctx.Done():
			goto Done
		case <-ticker.C:
			// Primary signal: bead update (structured response channel)
			newTime, err := getAgentBeadUpdateTime(townRoot, beadID)
			if err == nil && newTime.After(baselineTime) {
				responded = true
				goto Done
			}

			// Secondary signal: tmux session activity (prose/command response)
			// Agents like the Witness respond to HEALTH_CHECK by running commands
			// in their session, producing output, but may not update their bead.
			// Session activity is a reliable liveness signal for these agents.
			if activityErr == nil {
				newActivity, err := t.GetSessionActivity(sessionName)
				if err == nil && newActivity.After(baselineActivity) {
					responded = true
					goto Done
				}
			}
		}
	}

Done:
	// Record result
	if responded {
		agentState.RecordResponse()
		if err := deacon.SaveHealthCheckState(townRoot, state); err != nil {
			style.PrintWarning("failed to save health check state: %v", err)
		}
		fmt.Printf("%s Agent %s responded (failures reset to 0)\n",
			style.Bold.Render("✓"), agent)
		return nil
	}

	// No response - record failure
	agentState.RecordFailure()
	if err := deacon.SaveHealthCheckState(townRoot, state); err != nil {
		style.PrintWarning("failed to save health check state: %v", err)
	}

	fmt.Printf("%s Agent %s did not respond (consecutive failures: %d/%d)\n",
		style.Dim.Render("⚠"), agent, agentState.ConsecutiveFailures, healthCheckFailures)

	// Check if force-kill threshold reached
	if agentState.ShouldForceKill(healthCheckFailures) {
		fmt.Printf("%s Agent %s should be force-killed\n", style.Bold.Render("✗"), agent)
		return NewSilentExit(2) // Exit code 2 = should force-kill
	}

	return nil
}

// runDeaconForceKill implements the force-kill command.
// It kills a stuck agent session and updates its bead state.
func runDeaconForceKill(cmd *cobra.Command, args []string) error {
	agent := args[0]

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load health check state
	state, err := deacon.LoadHealthCheckState(townRoot)
	if err != nil {
		return fmt.Errorf("loading health check state: %w", err)
	}
	agentState := state.GetAgentState(agent)

	// Check cooldown (unless bypassed)
	if agentState.IsInCooldown(healthCheckCooldown) {
		remaining := agentState.CooldownRemaining(healthCheckCooldown)
		return fmt.Errorf("agent %s is in cooldown (remaining: %s) - cannot force-kill yet",
			agent, remaining.Round(time.Second))
	}

	// Get session name
	_, sessionName, err := agentAddressToIDs(agent)
	if err != nil {
		return fmt.Errorf("invalid agent address: %w", err)
	}

	t := tmux.NewTmux()

	// Check if session exists
	exists, err := t.HasSession(sessionName)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if !exists {
		fmt.Printf("%s Agent %s session not running\n", style.Dim.Render("○"), agent)
		return nil
	}

	// Build reason
	reason := forceKillReason
	if reason == "" {
		reason = fmt.Sprintf("unresponsive after %d consecutive health check failures",
			agentState.ConsecutiveFailures)
	}

	// Step 1: Log the intervention (send mail to agent)
	fmt.Printf("%s Sending force-kill notification to %s...\n", style.Dim.Render("1."), agent)
	mailBody := fmt.Sprintf("Deacon detected %s as unresponsive.\nReason: %s\nAction: force-killing session", agent, reason)
	sendMail(townRoot, agent, "FORCE_KILL: unresponsive", mailBody)

	// Step 2: Kill the tmux session.
	// Use KillSessionWithProcesses to ensure all descendant processes are killed.
	fmt.Printf("%s Killing tmux session %s...\n", style.Dim.Render("2."), sessionName)
	if err := t.KillSessionWithProcesses(sessionName); err != nil {
		return fmt.Errorf("killing session: %w", err)
	}

	// Step 3: Update agent bead state (optional - best effort)
	fmt.Printf("%s Updating agent bead state to 'killed'...\n", style.Dim.Render("3."))
	updateAgentBeadState(townRoot, agent, "killed", reason)

	// Step 4: Notify mayor (optional)
	if !forceKillSkipNotify {
		fmt.Printf("%s Notifying mayor...\n", style.Dim.Render("4."))
		notifyBody := fmt.Sprintf("Agent %s was force-killed by Deacon.\nReason: %s", agent, reason)
		sendMail(townRoot, "mayor/", "Agent killed: "+agent, notifyBody)
	}

	// Record force-kill in state
	agentState.RecordForceKill()
	if err := deacon.SaveHealthCheckState(townRoot, state); err != nil {
		style.PrintWarning("failed to save health check state: %v", err)
	}

	fmt.Printf("%s Force-killed agent %s (total kills: %d)\n",
		style.Bold.Render("✓"), agent, agentState.ForceKillCount)
	fmt.Printf("  %s\n", style.Dim.Render("Agent is now 'asleep'. Use 'gt rig boot' to restart."))

	return nil
}

// runDeaconHealthState shows the current health check state.
func runDeaconHealthState(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	state, err := deacon.LoadHealthCheckState(townRoot)
	if err != nil {
		return fmt.Errorf("loading health check state: %w", err)
	}

	if len(state.Agents) == 0 {
		fmt.Printf("%s No health check state recorded yet\n", style.Dim.Render("○"))
		return nil
	}

	fmt.Printf("%s Health Check State (updated %s)\n\n",
		style.Bold.Render("●"),
		state.LastUpdated.Format(time.RFC3339))

	for agentID, agentState := range state.Agents {
		fmt.Printf("Agent: %s\n", style.Bold.Render(agentID))

		if !agentState.LastPingTime.IsZero() {
			fmt.Printf("  Last ping: %s ago\n", time.Since(agentState.LastPingTime).Round(time.Second))
		}
		if !agentState.LastResponseTime.IsZero() {
			fmt.Printf("  Last response: %s ago\n", time.Since(agentState.LastResponseTime).Round(time.Second))
		}

		fmt.Printf("  Consecutive failures: %d\n", agentState.ConsecutiveFailures)
		fmt.Printf("  Total force-kills: %d\n", agentState.ForceKillCount)

		if !agentState.LastForceKillTime.IsZero() {
			fmt.Printf("  Last force-kill: %s ago\n", time.Since(agentState.LastForceKillTime).Round(time.Second))
			if agentState.IsInCooldown(healthCheckCooldown) {
				remaining := agentState.CooldownRemaining(healthCheckCooldown)
				fmt.Printf("  Cooldown: %s remaining\n", remaining.Round(time.Second))
			}
		}
		fmt.Println()
	}

	return nil
}

// agentAddressToIDs converts an agent address to bead ID and session name.
// Supports formats: "gastown/polecats/max", "gastown/witness", "deacon", "mayor"
// Note: Town-level agents (Mayor, Deacon) use hq- prefix bead IDs stored in town beads.
func agentAddressToIDs(address string) (beadID, sessionName string, err error) {
	switch address {
	case constants.RoleDeacon:
		return beads.DeaconBeadIDTown(), session.DeaconSessionName(), nil
	case constants.RoleMayor:
		return beads.MayorBeadIDTown(), session.MayorSessionName(), nil
	}

	parts := strings.Split(address, "/")
	switch len(parts) {
	case 2:
		// rig/role: "gastown/witness", "gastown/refinery"
		rig, role := parts[0], parts[1]
		switch role {
		case constants.RoleWitness:
			return session.WitnessSessionName(session.PrefixFor(rig)), session.WitnessSessionName(session.PrefixFor(rig)), nil
		case constants.RoleRefinery:
			return session.RefinerySessionName(session.PrefixFor(rig)), session.RefinerySessionName(session.PrefixFor(rig)), nil
		default:
			return "", "", fmt.Errorf("unknown role: %s", role)
		}
	case 3:
		// rig/type/name: "gastown/polecats/max", "gastown/crew/alpha"
		rig, agentType, name := parts[0], parts[1], parts[2]
		switch agentType {
		case "polecats":
			return session.PolecatSessionName(session.PrefixFor(rig), name), session.PolecatSessionName(session.PrefixFor(rig), name), nil
		case constants.RoleCrew:
			return session.CrewSessionName(session.PrefixFor(rig), name), session.CrewSessionName(session.PrefixFor(rig), name), nil
		default:
			return "", "", fmt.Errorf("unknown agent type: %s", agentType)
		}
	default:
		return "", "", fmt.Errorf("invalid agent address format: %s (expected rig/type/name or rig/role)", address)
	}
}

// getAgentBeadUpdateTime gets the update time from an agent bead.
func getAgentBeadUpdateTime(townRoot, beadID string) (time.Time, error) {
	cmd := exec.Command("bd", "show", beadID, "--json")
	cmd.Dir = townRoot

	output, err := cmd.Output()
	if err != nil {
		return time.Time{}, err
	}

	var issues []struct {
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal(output, &issues); err != nil {
		return time.Time{}, err
	}

	if len(issues) == 0 {
		return time.Time{}, fmt.Errorf("bead not found: %s", beadID)
	}

	return time.Parse(time.RFC3339, issues[0].UpdatedAt)
}

// sendMail sends a mail message using gt mail send.
func sendMail(townRoot, to, subject, body string) {
	cmd := exec.Command("gt", "mail", "send", to, "-s", subject, "-m", body)
	cmd.Dir = townRoot
	_ = cmd.Run() // Best effort
}

// updateAgentBeadState updates an agent bead's state.
func updateAgentBeadState(townRoot, agent, state, _ string) { // reason unused but kept for API consistency
	beadID, _, err := agentAddressToIDs(agent)
	if err != nil {
		return
	}

	_ = beads.New(townRoot).UpdateAgentState(beadID, state) // Best effort
}

// runDeaconStaleHooks finds and unhooks stale hooked beads.
func runDeaconStaleHooks(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	cfg := &deacon.StaleHookConfig{
		MaxAge: staleHooksMaxAge,
		DryRun: staleHooksDryRun,
	}

	result, err := deacon.ScanStaleHooks(townRoot, cfg)
	if err != nil {
		return fmt.Errorf("scanning stale hooks: %w", err)
	}

	// Print summary
	if result.TotalHooked == 0 {
		fmt.Printf("%s No hooked beads found\n", style.Dim.Render("○"))
		return nil
	}

	fmt.Printf("%s Found %d hooked bead(s), %d stale (older than %s)\n",
		style.Bold.Render("●"), result.TotalHooked, result.StaleCount, staleHooksMaxAge)

	if result.StaleCount == 0 {
		fmt.Printf("%s No stale hooked beads\n", style.Dim.Render("○"))
		return nil
	}

	// Print details for each stale bead
	for _, r := range result.Results {
		status := style.Dim.Render("○")
		action := "skipped (agent alive)"

		if !r.AgentAlive {
			if staleHooksDryRun {
				status = style.Bold.Render("?")
				action = "would unhook (agent dead)"
			} else if r.Unhooked {
				status = style.Bold.Render("✓")
				action = "unhooked (agent dead)"
			} else if r.Error != "" {
				status = style.Dim.Render("✗")
				action = fmt.Sprintf("error: %s", r.Error)
			}
		}

		fmt.Printf("  %s %s: %s (age: %s, assignee: %s)\n",
			status, r.BeadID, action, r.Age, r.Assignee)

		// Surface partial work warnings
		if r.PartialWork {
			var details []string
			if r.WorktreeDirty {
				details = append(details, "uncommitted changes")
			}
			if r.UnpushedCount > 0 {
				details = append(details, fmt.Sprintf("%d unpushed commit(s)", r.UnpushedCount))
			}
			fmt.Printf("    %s partial work detected: %s\n",
				style.Bold.Render("⚠"), strings.Join(details, ", "))
		}
		if r.WorktreeError != "" {
			fmt.Printf("    %s worktree check failed: %s\n",
				style.Dim.Render("⚠"), r.WorktreeError)
		}
	}

	// Count beads with partial work
	partialWorkCount := 0
	for _, r := range result.Results {
		if r.PartialWork {
			partialWorkCount++
		}
	}

	// Summary
	if staleHooksDryRun {
		fmt.Printf("\n%s Dry run - no changes made. Run without --dry-run to unhook.\n",
			style.Dim.Render("ℹ"))
	} else if result.Unhooked > 0 {
		fmt.Printf("\n%s Unhooked %d stale bead(s)\n",
			style.Bold.Render("✓"), result.Unhooked)
	}
	if partialWorkCount > 0 {
		fmt.Printf("%s %d bead(s) had partial work in worktree\n",
			style.Bold.Render("⚠"), partialWorkCount)
	}

	return nil
}

// runDeaconPause pauses the Deacon to prevent patrol actions.
func runDeaconPause(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Check if already paused
	paused, state, err := deacon.IsPaused(townRoot)
	if err != nil {
		return fmt.Errorf("checking pause state: %w", err)
	}
	if paused {
		fmt.Printf("%s Deacon is already paused\n", style.Dim.Render("○"))
		fmt.Printf("  Reason: %s\n", state.Reason)
		fmt.Printf("  Paused at: %s\n", state.PausedAt.Format(time.RFC3339))
		fmt.Printf("  Paused by: %s\n", state.PausedBy)
		return nil
	}

	// Pause the Deacon
	if err := deacon.Pause(townRoot, pauseReason, "human"); err != nil {
		return fmt.Errorf("pausing Deacon: %w", err)
	}

	fmt.Printf("%s Deacon paused\n", style.Bold.Render("⏸️"))
	if pauseReason != "" {
		fmt.Printf("  Reason: %s\n", pauseReason)
	}
	fmt.Printf("  Pause file: %s\n", deacon.GetPauseFile(townRoot))
	fmt.Println()
	fmt.Printf("The Deacon will not perform any patrol actions until resumed.\n")
	fmt.Printf("Resume with: %s\n", style.Dim.Render("gt deacon resume"))

	return nil
}

// runDeaconResume resumes the Deacon to allow patrol actions.
func runDeaconResume(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Check if paused
	paused, _, err := deacon.IsPaused(townRoot)
	if err != nil {
		return fmt.Errorf("checking pause state: %w", err)
	}
	if !paused {
		fmt.Printf("%s Deacon is not paused\n", style.Dim.Render("○"))
		return nil
	}

	// Resume the Deacon
	if err := deacon.Resume(townRoot); err != nil {
		return fmt.Errorf("resuming Deacon: %w", err)
	}

	fmt.Printf("%s Deacon resumed\n", style.Bold.Render("▶️"))
	fmt.Println("The Deacon can now perform patrol actions.")

	return nil
}

// runDeaconCleanupOrphans cleans up orphaned claude subagent processes.
func runDeaconCleanupOrphans(cmd *cobra.Command, args []string) error {
	// First, find orphans
	orphans, err := util.FindOrphanedClaudeProcesses()
	if err != nil {
		return fmt.Errorf("finding orphaned processes: %w", err)
	}

	if len(orphans) == 0 {
		fmt.Printf("%s No orphaned claude processes found\n", style.Dim.Render("○"))
		return nil
	}

	fmt.Printf("%s Found %d orphaned claude process(es)\n", style.Bold.Render("●"), len(orphans))

	// Process them with signal escalation
	results, err := util.CleanupOrphanedClaudeProcesses()
	if err != nil {
		style.PrintWarning("cleanup had errors: %v", err)
	}

	// Report results
	var terminated, escalated, unkillable int
	for _, r := range results {
		town := r.Process.TownRoot
		if town == "" {
			town = "unknown"
		}
		switch r.Signal {
		case "SIGTERM":
			fmt.Printf("  %s Sent SIGTERM to PID %d (%s) town=%s\n", style.Bold.Render("→"), r.Process.PID, r.Process.Cmd, town)
			terminated++
		case "SIGKILL":
			fmt.Printf("  %s Escalated to SIGKILL for PID %d (%s) town=%s\n", style.Bold.Render("!"), r.Process.PID, r.Process.Cmd, town)
			escalated++
		case "UNKILLABLE":
			fmt.Printf("  %s WARNING: PID %d (%s) survived SIGKILL town=%s\n", style.Bold.Render("⚠"), r.Process.PID, r.Process.Cmd, town)
			unkillable++
		}
	}

	if len(results) > 0 {
		summary := fmt.Sprintf("Processed %d orphan(s)", len(results))
		if escalated > 0 {
			summary += fmt.Sprintf(" (%d escalated to SIGKILL)", escalated)
		}
		if unkillable > 0 {
			summary += fmt.Sprintf(" (%d unkillable)", unkillable)
		}
		fmt.Printf("%s %s\n", style.Bold.Render("✓"), summary)
	}

	return nil
}

// runDeaconZombieScan finds and cleans zombie Claude processes not in active tmux sessions.
func runDeaconZombieScan(cmd *cobra.Command, args []string) error {
	// Find zombies using tmux verification
	zombies, err := util.FindZombieClaudeProcesses()
	if err != nil {
		return fmt.Errorf("finding zombie processes: %w", err)
	}

	if len(zombies) == 0 {
		fmt.Printf("%s No zombie claude processes found\n", style.Dim.Render("○"))
		return nil
	}

	fmt.Printf("%s Found %d zombie claude process(es)\n", style.Bold.Render("●"), len(zombies))

	// In dry-run mode, just list them
	if zombieScanDryRun {
		for _, z := range zombies {
			ageStr := fmt.Sprintf("%dm", z.Age/60)
			town := z.TownRoot
			if town == "" {
				town = "unknown"
			}
			fmt.Printf("  %s PID %d (%s) TTY=%s age=%s town=%s\n",
				style.Dim.Render("→"), z.PID, z.Cmd, z.TTY, ageStr, town)
		}
		fmt.Printf("%s Dry run - no processes killed\n", style.Dim.Render("○"))
		return nil
	}

	// Process them with signal escalation
	results, err := util.CleanupZombieClaudeProcesses()
	if err != nil {
		style.PrintWarning("cleanup had errors: %v", err)
	}

	// Report results
	var terminated, escalated, unkillable int
	for _, r := range results {
		town := r.Process.TownRoot
		if town == "" {
			town = "unknown"
		}
		switch r.Signal {
		case "SIGTERM":
			fmt.Printf("  %s Sent SIGTERM to PID %d (%s) TTY=%s town=%s\n",
				style.Bold.Render("→"), r.Process.PID, r.Process.Cmd, r.Process.TTY, town)
			terminated++
		case "SIGKILL":
			fmt.Printf("  %s Escalated to SIGKILL for PID %d (%s) town=%s\n",
				style.Bold.Render("!"), r.Process.PID, r.Process.Cmd, town)
			escalated++
		case "UNKILLABLE":
			fmt.Printf("  %s WARNING: PID %d (%s) survived SIGKILL town=%s\n",
				style.Bold.Render("⚠"), r.Process.PID, r.Process.Cmd, town)
			unkillable++
		}
	}

	if len(results) > 0 {
		summary := fmt.Sprintf("Processed %d zombie(s)", len(results))
		if escalated > 0 {
			summary += fmt.Sprintf(" (%d escalated to SIGKILL)", escalated)
		}
		if unkillable > 0 {
			summary += fmt.Sprintf(" (%d unkillable)", unkillable)
		}
		fmt.Printf("%s %s\n", style.Bold.Render("✓"), summary)
	}

	return nil
}

// runDeaconRedispatch handles re-dispatching a recovered bead.
func runDeaconRedispatch(cmd *cobra.Command, args []string) error {
	beadID := args[0]

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	result := deacon.Redispatch(townRoot, beadID, redispatchRig, redispatchMaxAttempts, redispatchCooldown)

	switch result.Action {
	case "redispatched":
		fmt.Printf("%s %s\n", style.Bold.Render("✓"), result.Message)
		return nil

	case "escalated":
		fmt.Printf("%s %s\n", style.Bold.Render("⚠"), result.Message)
		if result.Error != nil {
			return result.Error
		}
		return nil

	case "already-escalated":
		fmt.Printf("%s %s\n", style.Dim.Render("○"), result.Message)
		return nil

	case "cooldown":
		fmt.Printf("%s %s\n", style.Dim.Render("○"), result.Message)
		return NewSilentExit(2)

	case "skipped":
		fmt.Printf("%s %s\n", style.Dim.Render("○"), result.Message)
		return NewSilentExit(3)

	case "error":
		if result.Error != nil {
			return result.Error
		}
		return fmt.Errorf("redispatch failed: %s", result.Message)

	default:
		return fmt.Errorf("unexpected redispatch result: %s", result.Action)
	}
}

// runDeaconRedispatchState shows the current re-dispatch state.
func runDeaconRedispatchState(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	state, err := deacon.LoadRedispatchState(townRoot)
	if err != nil {
		return fmt.Errorf("loading redispatch state: %w", err)
	}

	if len(state.Beads) == 0 {
		fmt.Printf("%s No re-dispatch state recorded\n", style.Dim.Render("○"))
		return nil
	}

	fmt.Printf("%s Re-dispatch State (updated %s)\n\n",
		style.Bold.Render("●"),
		state.LastUpdated.Format(time.RFC3339))

	for beadID, beadState := range state.Beads {
		fmt.Printf("Bead: %s\n", style.Bold.Render(beadID))
		fmt.Printf("  Attempts: %d\n", beadState.AttemptCount)

		if !beadState.LastAttemptTime.IsZero() {
			fmt.Printf("  Last attempt: %s ago\n", time.Since(beadState.LastAttemptTime).Round(time.Second))
		}
		if beadState.LastRig != "" {
			fmt.Printf("  Last rig: %s\n", beadState.LastRig)
		}
		if beadState.Escalated {
			fmt.Printf("  Escalated: YES (at %s)\n", beadState.EscalatedAt.Format(time.RFC3339))
		}

		cooldown := deacon.DefaultRedispatchCooldown
		if beadState.IsInCooldown(cooldown) {
			remaining := beadState.CooldownRemaining(cooldown)
			fmt.Printf("  Cooldown: %s remaining\n", remaining.Round(time.Second))
		}
		fmt.Println()
	}

	return nil
}

// runDeaconFeedStranded detects stranded convoys and feeds them.
func runDeaconFeedStranded(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	result := deacon.FeedStranded(townRoot, feedStrandedMaxFeeds, feedStrandedCooldown)

	// JSON output
	if feedStrandedJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	// Human-readable output
	if len(result.Details) == 0 {
		fmt.Printf("%s No stranded convoys found\n", style.Dim.Render("○"))
		return nil
	}

	for _, d := range result.Details {
		switch d.Action {
		case "fed":
			fmt.Printf("  %s %s: %s\n", style.Bold.Render("✓"), d.ConvoyID, d.Message)
		case "closed":
			fmt.Printf("  %s %s: %s\n", style.Bold.Render("✓"), d.ConvoyID, d.Message)
		case "needs_attention":
			fmt.Printf("  %s %s: %s\n", style.Warning.Render("?"), d.ConvoyID, d.Message)
		case "cooldown":
			fmt.Printf("  %s %s: %s\n", style.Dim.Render("○"), d.ConvoyID, d.Message)
		case "limit":
			fmt.Printf("  %s %s: %s\n", style.Dim.Render("○"), d.ConvoyID, d.Message)
		case "error":
			id := d.ConvoyID
			if id == "" {
				id = "(general)"
			}
			fmt.Printf("  %s %s: %s\n", style.Dim.Render("✗"), id, d.Message)
		}
	}

	// Summary
	fmt.Printf("\n%s Fed: %d, Closed: %d, Needs attention: %d, Skipped: %d, Errors: %d\n",
		style.Bold.Render("●"), result.Fed, result.Closed, result.NeedsAttention, result.Skipped, result.Errors)

	return nil
}

// runDeaconFeedStrandedState shows the current feed-stranded state.
func runDeaconFeedStrandedState(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	state, err := deacon.LoadFeedStrandedState(townRoot)
	if err != nil {
		return fmt.Errorf("loading feed-stranded state: %w", err)
	}

	if len(state.Convoys) == 0 {
		fmt.Printf("%s No feed-stranded state recorded yet\n", style.Dim.Render("○"))
		return nil
	}

	fmt.Printf("%s Feed-Stranded State (updated %s)\n\n",
		style.Bold.Render("●"),
		state.LastUpdated.Format(time.RFC3339))

	for convoyID, convoyState := range state.Convoys {
		fmt.Printf("Convoy: %s\n", style.Bold.Render(convoyID))
		fmt.Printf("  Feed count: %d\n", convoyState.FeedCount)

		if !convoyState.LastFeedTime.IsZero() {
			fmt.Printf("  Last feed: %s ago\n", time.Since(convoyState.LastFeedTime).Round(time.Second))
		}

		cooldown := deacon.DefaultFeedCooldown
		if convoyState.IsInCooldown(cooldown) {
			remaining := convoyState.CooldownRemaining(cooldown)
			fmt.Printf("  Cooldown: %s remaining\n", remaining.Round(time.Second))
		}
		fmt.Println()
	}

	return nil
}
