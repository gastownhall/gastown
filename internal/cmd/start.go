package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/crew"
	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/mayor"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/workspace"
)

// defaultOrphanGraceSecs is the grace period (in seconds) between SIGTERM and SIGKILL
// when automatically cleaning up orphaned Claude processes during shutdown.
// This is shorter than the --cleanup-orphans-grace-secs default (60s) because
// automatic cleanup runs after sessions are already killed, so processes have
// already had time to shut down.
const defaultOrphanGraceSecs = 5

var (
	startAll                    bool
	startAgentOverride          string
	startCrewRig                string
	startCrewAccount            string
	startCrewAgentOverride      string
	startCostTier               string
	shutdownGraceful            bool
	shutdownWait                int
	shutdownAll                 bool
	shutdownForce               bool
	shutdownYes                 bool
	shutdownPolecatsOnly        bool
	shutdownNuclear             bool
	shutdownCleanupOrphans      bool
	shutdownCleanupOrphansGrace int
)

var startCmd = &cobra.Command{
	Use:     "start [path]",
	GroupID: GroupServices,
	Short:   "Start Gas Town or a crew workspace",
	Long: `Start Gas Town by launching the Deacon and Mayor.

The Deacon is the health-check orchestrator that monitors Mayor and Witnesses.
The Mayor is the global coordinator that dispatches work.

By default, other agents (Witnesses, Refineries) are started lazily as needed.
Use --all to start Witnesses and Refineries for all registered rigs immediately.

Crew shortcut:
  If a path like "rig/crew/name" is provided, starts that crew workspace.
  This is equivalent to 'gt start crew rig/name'.

To stop Gas Town, use 'gt shutdown'.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runStart,
}

var shutdownCmd = &cobra.Command{
	Use:     "shutdown",
	GroupID: GroupServices,
	Short:   "Shutdown Gas Town with cleanup",
	Long: `Shutdown Gas Town by stopping agents and cleaning up polecats.

This is the "done for the day" command - it stops everything AND removes
polecat worktrees/branches. For a reversible pause, use 'gt down' instead.

Comparison:
  gt down      - Pause (stop processes, keep worktrees) - reversible
  gt shutdown  - Done (stop + cleanup worktrees) - permanent cleanup

After killing sessions, polecats are cleaned up:
  - Worktrees are removed
  - Polecat branches are deleted
  - Polecats with uncommitted work are SKIPPED (protected)

Shutdown levels (progressively more aggressive):
  (default)       - Stop infrastructure + polecats + cleanup
  --all           - Also stop crew sessions
  --polecats-only - Only stop polecats (leaves infrastructure running)

Use --force or --yes to skip confirmation prompt.
Use --graceful to allow agents time to save state before killing.
Use --nuclear to force cleanup even if polecats have uncommitted work (DANGER).
Use --cleanup-orphans to use a longer grace period for orphan cleanup (default 60s).
Use --cleanup-orphans-grace-secs to set that grace period.

Orphaned Claude processes are always cleaned up after session termination.
By default, a 5-second grace period is used. The --cleanup-orphans flag
extends this to --cleanup-orphans-grace-secs (default 60s) for stubborn processes.`,
	RunE: runShutdown,
}

var startCrewCmd = &cobra.Command{
	Use:   "crew <name>",
	Short: "Start a crew workspace (creates if needed)",
	Long: `Start a crew workspace, creating it if it doesn't exist.

This is a convenience command that combines 'gt crew add' and 'gt crew at --detached'.
The crew session starts in the background with Claude running and ready.

The name can include the rig in slash format (e.g., greenplace/joe).
If not specified, the rig is inferred from the current directory.

Examples:
  gt start crew joe                    # Start joe in current rig
  gt start crew greenplace/joe            # Start joe in gastown rig
  gt start crew joe --rig beads        # Start joe in beads rig`,
	Args: cobra.ExactArgs(1),
	RunE: runStartCrew,
}

func init() {
	startCmd.Flags().BoolVarP(&startAll, "all", "a", false,
		"Also start Witnesses and Refineries for all rigs")
	startCmd.Flags().StringVar(&startAgentOverride, "agent", "", "Agent alias to run Mayor/Deacon with (overrides town default)")
	startCmd.Flags().StringVar(&startCostTier, "cost-tier", "", "Ephemeral cost tier for this session (standard/economy/budget)")

	startCrewCmd.Flags().StringVar(&startCrewRig, "rig", "", "Rig to use")
	startCrewCmd.Flags().StringVar(&startCrewAccount, "account", "", "Claude Code account handle to use")
	startCrewCmd.Flags().StringVar(&startCrewAgentOverride, "agent", "", "Agent alias to run crew worker with (overrides rig/town default)")
	startCmd.AddCommand(startCrewCmd)

	shutdownCmd.Flags().BoolVarP(&shutdownGraceful, "graceful", "g", false,
		"Send ESC to agents and wait for them to handoff before killing")
	shutdownCmd.Flags().IntVarP(&shutdownWait, "wait", "w", 30,
		"Seconds to wait for graceful shutdown (default 30)")
	shutdownCmd.Flags().BoolVarP(&shutdownAll, "all", "a", false,
		"Also stop crew sessions (by default, crew is preserved)")
	shutdownCmd.Flags().BoolVarP(&shutdownForce, "force", "f", false,
		"Skip confirmation prompt (alias for --yes)")
	shutdownCmd.Flags().BoolVarP(&shutdownYes, "yes", "y", false,
		"Skip confirmation prompt")
	shutdownCmd.Flags().BoolVar(&shutdownPolecatsOnly, "polecats-only", false,
		"Only stop polecats (minimal shutdown)")
	shutdownCmd.Flags().BoolVar(&shutdownNuclear, "nuclear", false,
		"Force cleanup even if polecats have uncommitted work (DANGER: may lose work)")
	shutdownCmd.Flags().BoolVar(&shutdownCleanupOrphans, "cleanup-orphans", false,
		"Use longer grace period (--cleanup-orphans-grace-secs) for orphan cleanup instead of default 5s")
	shutdownCmd.Flags().IntVar(&shutdownCleanupOrphansGrace, "cleanup-orphans-grace-secs", 60,
		"Grace period in seconds between SIGTERM and SIGKILL when cleaning orphans (default 60)")

	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(shutdownCmd)
}

func runStart(cmd *cobra.Command, args []string) error {
	// Check if arg looks like a crew path (rig/crew/name)
	if len(args) == 1 && strings.Contains(args[0], "/crew/") {
		// Parse rig/crew/name format
		parts := strings.SplitN(args[0], "/crew/", 2)
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			// Route to crew start with rig/name format
			crewArg := parts[0] + "/" + parts[1]
			return runStartCrew(cmd, []string{crewArg})
		}
	}

	// Verify we're in a Gas Town workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Apply ephemeral cost tier if specified
	if startCostTier != "" {
		if !config.IsValidTier(startCostTier) {
			return fmt.Errorf("invalid cost tier %q (valid: %s)", startCostTier, strings.Join(config.ValidCostTiers(), ", "))
		}
		os.Setenv("GT_COST_TIER", startCostTier)
		fmt.Printf("Using ephemeral cost tier: %s\n", style.Bold.Render(startCostTier))
	}

	if err := config.EnsureDaemonPatrolConfig(townRoot); err != nil {
		fmt.Printf("  %s Could not ensure daemon config: %v\n", style.Dim.Render("○"), err)
	}

	t := tmux.NewTmux()

	// Clean up orphaned tmux sessions before starting new agents.
	// This prevents session name conflicts and resource accumulation from
	// zombie sessions (tmux alive but Claude dead).
	if cleaned, err := t.CleanupOrphanedSessions(session.IsKnownSession); err != nil {
		fmt.Printf("  %s Could not clean orphaned sessions: %v\n", style.Dim.Render("○"), err)
	} else if cleaned > 0 {
		fmt.Printf("  %s Cleaned up %d orphaned session(s)\n", style.Bold.Render("✓"), cleaned)
	}

	fmt.Printf("Starting Gas Town from %s\n\n", style.Dim.Render(townRoot))
	fmt.Println("Starting all agents in parallel...")
	fmt.Println()

	// Discover rigs once upfront to avoid redundant calls from parallel goroutines
	rigs, rigsErr := discoverAllRigs(townRoot)
	if rigsErr != nil {
		fmt.Printf("  %s Could not discover rigs: %v\n", style.Dim.Render("○"), rigsErr)
		// Continue anyway - core agents don't need rigs
	}

	// Phase 1: Start Dolt server BEFORE agents.
	// Agents run bd commands on startup (via gt prime → patrol_helpers) that
	// connect to the Dolt SQL server. Without this sequencing, they race the
	// server and bd auto-spawns orphan embedded servers. (gt-t2zf)
	var doltOK bool
	cfg := doltserver.DefaultConfig(townRoot)
	if _, err := os.Stat(cfg.DataDir); os.IsNotExist(err) {
		// No Dolt data dir — nothing to start
		fmt.Printf("  %s Dolt server skipped (no data dir)\n", style.Dim.Render("○"))
	} else {
		running, _, _ := doltserver.IsRunning(townRoot)
		if running {
			doltOK = true
			fmt.Printf("  %s Dolt server already running\n", style.Dim.Render("○"))
		} else if err := doltserver.Start(townRoot); err != nil {
			fmt.Printf("  %s Dolt server failed: %v\n", style.Dim.Render("○"), err)
		} else {
			doltOK = true
			fmt.Printf("  %s Dolt server started (port %d)\n", style.Bold.Render("✓"), doltserver.DefaultPort)
		}
	}

	// Ensure beads metadata is correct BEFORE agents start.
	// This prevents bd from seeing stale config and spawning orphan servers.
	if doltOK {
		_, _ = doltserver.EnsureAllMetadata(townRoot)
	}

	// Phase 2: Start all agents in parallel (Dolt is now ready)
	var wg sync.WaitGroup
	var mu sync.Mutex // Protects stdout
	var coreErr error

	// Start core agents (Mayor and Deacon) in background
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := startCoreAgents(townRoot, startAgentOverride, &mu); err != nil {
			mu.Lock()
			coreErr = err
			mu.Unlock()
		}
	}()

	// Start rig agents (witnesses, refineries) if --all
	if startAll && rigs != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			startRigAgents(rigs, &mu)
		}()
	}

	// Start configured crew
	if rigs != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			startConfiguredCrew(t, rigs, townRoot, &mu)
		}()
	}

	wg.Wait()

	if coreErr != nil {
		return coreErr
	}

	fmt.Println()
	fmt.Printf("%s Gas Town is running\n", style.Bold.Render("✓"))
	fmt.Println()
	fmt.Printf("  Attach to Mayor:  %s\n", style.Dim.Render("gt mayor attach"))
	fmt.Printf("  Attach to Deacon: %s\n", style.Dim.Render("gt deacon attach"))
	fmt.Printf("  Check status:     %s\n", style.Dim.Render("gt status"))

	return nil
}

// startCoreAgents starts Mayor and Deacon sessions in parallel using the Manager pattern.
// The mutex is used to synchronize output with other parallel startup operations.
func startCoreAgents(townRoot string, agentOverride string, mu *sync.Mutex) error {
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex

	// Start Mayor in goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		mayorMgr := mayor.NewManager(townRoot)
		if err := mayorMgr.Start(agentOverride); err != nil {
			if errors.Is(err, mayor.ErrAlreadyRunning) {
				mu.Lock()
				fmt.Printf("  %s Mayor already running\n", style.Dim.Render("○"))
				mu.Unlock()
			} else if errors.Is(err, mayor.ErrACPActive) {
				mu.Lock()
				fmt.Printf("  %s Mayor already running (ACP mode)\n", style.Dim.Render("○"))
				mu.Unlock()
			} else {
				errMu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("starting Mayor: %w", err)
				}
				errMu.Unlock()
				mu.Lock()
				fmt.Printf("  %s Mayor failed: %v\n", style.Dim.Render("○"), err)
				mu.Unlock()
			}
		} else {
			mu.Lock()
			fmt.Printf("  %s Mayor started\n", style.Bold.Render("✓"))
			mu.Unlock()
		}
	}()

	// Start Deacon in goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		deaconMgr := deacon.NewManager(townRoot)
		if err := deaconMgr.Start(agentOverride); err != nil {
			if errors.Is(err, deacon.ErrAlreadyRunning) {
				mu.Lock()
				fmt.Printf("  %s Deacon already running\n", style.Dim.Render("○"))
				mu.Unlock()
			} else {
				errMu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("starting Deacon: %w", err)
				}
				errMu.Unlock()
				mu.Lock()
				fmt.Printf("  %s Deacon failed: %v\n", style.Dim.Render("○"), err)
				mu.Unlock()
			}
		} else {
			mu.Lock()
			fmt.Printf("  %s Deacon started\n", style.Bold.Render("✓"))
			mu.Unlock()
		}
	}()

	wg.Wait()
	return firstErr
}

// startRigAgents starts witness and refinery for all rigs in parallel.
// Called when --all flag is passed to gt start.
func startRigAgents(rigs []*rig.Rig, mu *sync.Mutex) {
	var wg sync.WaitGroup

	for _, r := range rigs {
		wg.Add(2) // Witness + Refinery

		// Start Witness in goroutine
		go func(r *rig.Rig) {
			defer wg.Done()
			msg := startWitnessForRig(r)
			mu.Lock()
			fmt.Print(msg)
			mu.Unlock()
		}(r)

		// Start Refinery in goroutine
		go func(r *rig.Rig) {
			defer wg.Done()
			msg := startRefineryForRig(r)
			mu.Lock()
			fmt.Print(msg)
			mu.Unlock()
		}(r)
	}

	wg.Wait()
}

// startWitnessForRig starts the witness for a single rig and returns a status message.
func startWitnessForRig(r *rig.Rig) string {
	witMgr := witness.NewManager(r)
	if err := witMgr.Start(false, "", nil); err != nil {
		if errors.Is(err, witness.ErrAlreadyRunning) {
			return fmt.Sprintf("  %s %s witness already running\n", style.Dim.Render("○"), r.Name)
		}
		return fmt.Sprintf("  %s %s witness failed: %v\n", style.Dim.Render("○"), r.Name, err)
	}
	return fmt.Sprintf("  %s %s witness started\n", style.Bold.Render("✓"), r.Name)
}

// startRefineryForRig starts the refinery for a single rig and returns a status message.
func startRefineryForRig(r *rig.Rig) string {
	refineryMgr := refinery.NewManager(r)
	if err := refineryMgr.Start(false, ""); err != nil {
		if errors.Is(err, refinery.ErrAlreadyRunning) {
			return fmt.Sprintf("  %s %s refinery already running\n", style.Dim.Render("○"), r.Name)
		}
		if errors.Is(err, refinery.ErrForkRig) {
			return fmt.Sprintf("  %s %s refinery skipped (fork-backed rig; use PR workflow)\n", style.Dim.Render("○"), r.Name)
		}
		return fmt.Sprintf("  %s %s refinery failed: %v\n", style.Dim.Render("○"), r.Name, err)
	}
	return fmt.Sprintf("  %s %s refinery started\n", style.Bold.Render("✓"), r.Name)
}

// startConfiguredCrew starts crew members configured in rig settings in parallel.
func startConfiguredCrew(t *tmux.Tmux, rigs []*rig.Rig, townRoot string, mu *sync.Mutex) {
	var wg sync.WaitGroup
	var startedAny int32 // Use atomic for thread-safe flag

	for _, r := range rigs {
		crewToStart := getCrewToStart(r)
		for _, crewName := range crewToStart {
			wg.Add(1)
			go func(r *rig.Rig, crewName string) {
				defer wg.Done()
				msg, started := startOrRestartCrewMember(t, r, crewName, townRoot)
				mu.Lock()
				fmt.Print(msg)
				mu.Unlock()
				if started {
					atomic.StoreInt32(&startedAny, 1)
				}
			}(r, crewName)
		}
	}

	wg.Wait()

	if atomic.LoadInt32(&startedAny) == 0 {
		mu.Lock()
		fmt.Printf("  %s No crew configured or all already running\n", style.Dim.Render("○"))
		mu.Unlock()
	}
}

// startOrRestartCrewMember starts or restarts a single crew member and returns a status message.
// Uses IsAgentAlive for robust zombie detection (checks pane command + descendant processes),
// and delegates zombie cleanup to crewMgr.Start() which kills the zombie session and recreates
// it with fresh env vars and runtime settings.
func startOrRestartCrewMember(t *tmux.Tmux, r *rig.Rig, crewName, townRoot string) (msg string, started bool) {
	sessionID := crewSessionName(r.Name, crewName)
	if running, _ := t.HasSession(sessionID); running {
		// Session exists - check if agent is still alive
		// Uses descendant process check instead of pane command check,
		// since crew members launch via bash -c wrappers (see #1315, #1330).
		if !t.IsAgentAlive(sessionID) {
			// Agent has exited, restart it
			// Build startup beacon for predecessor discovery via /resume
			address := session.BeaconRecipient("crew", crewName, r.Name)
			beacon := session.FormatStartupBeacon(session.BeaconConfig{
				Recipient: address,
				Sender:    "human",
				Topic:     "restart",
			})
			agentCmd := config.BuildCrewStartupCommand(r.Name, crewName, r.Path, beacon)
			if err := t.SendKeys(sessionID, agentCmd); err != nil {
				return fmt.Sprintf("  %s %s/%s restart failed: %v\n", style.Dim.Render("○"), r.Name, crewName, err), false
			}
			return fmt.Sprintf("  %s %s/%s agent restarted\n", style.Bold.Render("✓"), r.Name, crewName), true
		}
		// Agent is alive — nothing to do
		return fmt.Sprintf("  %s %s/%s already running\n", style.Dim.Render("○"), r.Name, crewName), false
	}

	if err := startCrewMember(r.Name, crewName, townRoot); err != nil {
		return fmt.Sprintf("  %s %s/%s failed: %v\n", style.Dim.Render("○"), r.Name, crewName, err), false
	}
	return fmt.Sprintf("  %s %s/%s started\n", style.Bold.Render("✓"), r.Name, crewName), true
}

// discoverAllRigs finds all rigs in the workspace.
func discoverAllRigs(townRoot string) ([]*rig.Rig, error) {
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return nil, fmt.Errorf("loading rigs config: %w", err)
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)

	return rigMgr.DiscoverRigs()
}

func runShutdown(cmd *cobra.Command, args []string) error {
	t := tmux.NewTmux()

	// Find workspace root for polecat cleanup
	townRoot, _ := workspace.FindFromCwd()

	// Collect sessions to show what will be stopped
	sessions, err := t.ListSessions()
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}

	toStop, preserved := categorizeSessions(sessions)

	if len(toStop) == 0 {
		fmt.Printf("%s Gas Town was not running\n", style.Dim.Render("○"))

		// Still check for orphaned daemons even if no sessions are running
		if townRoot != "" {
			fmt.Println()
			fmt.Println("Checking for orphaned daemon...")
			stopDaemonIfRunning(townRoot)
		}

		return nil
	}

	// Show what will happen
	fmt.Println("Sessions to stop:")
	for _, sess := range toStop {
		fmt.Printf("  %s %s\n", style.Bold.Render("→"), sess)
	}
	if len(preserved) > 0 && !shutdownAll {
		fmt.Println()
		fmt.Println("Sessions preserved (crew):")
		for _, sess := range preserved {
			fmt.Printf("  %s %s\n", style.Dim.Render("○"), sess)
		}
	}
	fmt.Println()

	// Confirmation prompt
	if !shutdownYes && !shutdownForce {
		fmt.Printf("Proceed with shutdown? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		response, _ := reader.ReadString('\n')
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "y" && response != "yes" {
			fmt.Println("Shutdown canceled.")
			return nil
		}
	}

	if shutdownGraceful {
		return runGracefulShutdown(t, toStop, townRoot)
	}
	return runImmediateShutdown(t, toStop, townRoot)
}

// categorizeSessions splits sessions into those to stop and those to preserve.
func categorizeSessions(sessions []string) (toStop, preserved []string) {
	for _, sess := range sessions {
		// Gas Town sessions use rig-specific prefixes or hq- (town-level)
		if !session.IsKnownSession(sess) {
			continue // Not a Gas Town session
		}

		// Parse session to determine role
		isPolecat := false
		isCrew := false
		if identity, err := session.ParseSessionName(sess); err == nil {
			switch identity.Role {
			case session.RolePolecat:
				isPolecat = true
			case session.RoleCrew:
				isCrew = true
			}
		}

		// Decide based on flags
		if shutdownPolecatsOnly {
			// Only stop polecats
			if isPolecat {
				toStop = append(toStop, sess)
			} else {
				preserved = append(preserved, sess)
			}
		} else if shutdownAll {
			// Stop everything including crew
			toStop = append(toStop, sess)
		} else {
			// Default: preserve crew
			if isCrew {
				preserved = append(preserved, sess)
			} else {
				toStop = append(toStop, sess)
			}
		}
	}
	return
}

func runGracefulShutdown(t *tmux.Tmux, gtSessions []string, townRoot string) error {
	fmt.Printf("Graceful shutdown of Gas Town (waiting up to %ds)...\n\n", shutdownWait)

	// Phase 1: Send ESC to all agents to interrupt them
	fmt.Printf("Phase 1: Sending ESC to %d agent(s)...\n", len(gtSessions))
	for _, sess := range gtSessions {
		fmt.Printf("  %s Interrupting %s\n", style.Bold.Render("→"), sess)
		_ = t.SendKeysRaw(sess, "Escape") // best-effort interrupt
	}

	// Phase 2: Send shutdown message asking agents to handoff
	fmt.Printf("\nPhase 2: Requesting handoff from agents...\n")
	shutdownMsg := "[SHUTDOWN] Gas Town is shutting down. Please save your state and update your handoff bead, then type /exit or wait to be terminated."
	for _, sess := range gtSessions {
		// Small delay then send the message
		time.Sleep(constants.ShutdownNotifyDelay)
		_ = t.SendKeys(sess, shutdownMsg) // best-effort notification
	}

	// Phase 3: Wait for agents to complete handoff
	fmt.Printf("\nPhase 3: Waiting %ds for agents to complete handoff...\n", shutdownWait)
	fmt.Printf("  %s\n", style.Dim.Render("(Press Ctrl-C to force immediate shutdown)"))

	// Wait with countdown
	for remaining := shutdownWait; remaining > 0; remaining -= 5 {
		if remaining < shutdownWait {
			fmt.Printf("  %s %ds remaining...\n", style.Dim.Render("⏳"), remaining)
		}
		sleepTime := 5
		if remaining < 5 {
			sleepTime = remaining
		}
		time.Sleep(time.Duration(sleepTime) * time.Second)
	}

	// Phase 4: Kill sessions in correct order
	fmt.Printf("\nPhase 4: Terminating sessions...\n")
	mayorSession := getMayorSessionName()
	deaconSession := getDeaconSessionName()
	stopped := killSessionsInOrder(t, gtSessions, mayorSession, deaconSession)

	// Phase 5: Always clean up orphaned Claude processes after killing sessions.
	// Processes can survive session kills if they caught/ignored SIGHUP or called setsid().
	// Use the user-specified grace period if --cleanup-orphans was explicitly set,
	// otherwise use a short default (5s) for the automatic sweep.
	graceSecs := defaultOrphanGraceSecs
	if shutdownCleanupOrphans {
		graceSecs = shutdownCleanupOrphansGrace
	}
	fmt.Printf("\nPhase 5: Cleaning up orphaned Claude processes...\n")
	cleanupOrphanedClaude(graceSecs)

	// Phase 6: Cleanup polecat worktrees and branches
	fmt.Printf("\nPhase 6: Cleaning up polecats...\n")
	if townRoot != "" {
		cleanupPolecats(townRoot)
	}

	// Phase 7: Stop the daemon
	fmt.Printf("\nPhase 7: Stopping daemon...\n")
	if townRoot != "" {
		stopDaemonIfRunning(townRoot)
	}

	// Phase 8: Verify no Claude processes survived
	fmt.Printf("\nPhase 8: Verifying shutdown...\n")
	verifyNoOrphans()

	fmt.Println()
	fmt.Printf("%s Graceful shutdown complete (%d sessions stopped)\n", style.Bold.Render("✓"), stopped)
	return nil
}

func runImmediateShutdown(t *tmux.Tmux, gtSessions []string, townRoot string) error {
	fmt.Println("Shutting down Gas Town...")

	mayorSession := getMayorSessionName()
	deaconSession := getDeaconSessionName()
	stopped := killSessionsInOrder(t, gtSessions, mayorSession, deaconSession)

	// Always clean up orphaned Claude processes after killing sessions.
	// Processes can survive session kills if they caught/ignored SIGHUP or called setsid().
	// Use the user-specified grace period if --cleanup-orphans was explicitly set,
	// otherwise use a short default (5s) for the automatic sweep.
	graceSecs := defaultOrphanGraceSecs
	if shutdownCleanupOrphans {
		graceSecs = shutdownCleanupOrphansGrace
	}
	fmt.Println()
	fmt.Println("Cleaning up orphaned Claude processes...")
	cleanupOrphanedClaude(graceSecs)

	// Cleanup polecat worktrees and branches
	if townRoot != "" {
		fmt.Println()
		fmt.Println("Cleaning up polecats...")
		cleanupPolecats(townRoot)
	}

	// Stop the daemon
	if townRoot != "" {
		fmt.Println()
		fmt.Println("Stopping daemon...")
		stopDaemonIfRunning(townRoot)
	}

	// Verify no Claude processes survived
	fmt.Println()
	fmt.Println("Verifying shutdown...")
	verifyNoOrphans()

	fmt.Println()
	fmt.Printf("%s Gas Town shutdown complete (%d sessions stopped)\n", style.Bold.Render("✓"), stopped)

	return nil
}

// killSessionsInOrder stops sessions in the correct shutdown order, matching gt down:
//  1. Polecats and crew (workers - stop before monitors can restart them)
//  2. Refineries (work processors)
//  3. Witnesses (monitors - stop before deacon so they can't restart workers)
//  4. Town sessions: Mayor, Boot, Deacon
//     Boot monitors Deacon, so must be stopped before Deacon.
//
// mayorSession and deaconSession are the dynamic session names for the current town.
//
// Returns the count of sessions that were successfully stopped (verified by checking
// if the session no longer exists after the kill attempt).
func killSessionsInOrder(t *tmux.Tmux, sessions []string, mayorSession, deaconSession string) int {
	stopped := 0
	bootSession := session.BootSessionName()

	// Build a set for O(1) lookup of town-level sessions
	sessionSet := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		sessionSet[s] = true
	}

	// Categorize sessions by type for ordered shutdown.
	var polecats, refineries, witnesses []string
	for _, sess := range sessions {
		// Skip town-level sessions (handled explicitly below)
		if sess == mayorSession || sess == deaconSession || sess == bootSession {
			continue
		}

		// Categorize by role using proper session name parser
		if identity, err := session.ParseSessionName(sess); err == nil {
			switch identity.Role {
			case session.RoleWitness:
				witnesses = append(witnesses, sess)
			case session.RoleRefinery:
				refineries = append(refineries, sess)
			default:
				// Polecats, crew, and any other rig-level sessions
				polecats = append(polecats, sess)
			}
		} else {
			// Unknown pattern, treat as worker (stop early)
			polecats = append(polecats, sess)
		}
	}

	// Helper to kill a session and verify it was stopped
	killAndVerify := func(sess string) bool {
		// Check if session exists before attempting to kill
		exists, _ := t.HasSession(sess)
		if !exists {
			return false // Session already gone
		}

		// Attempt to kill the session and its processes
		_ = t.KillSessionWithProcesses(sess)

		// Verify the session is actually gone (ignore error, check existence)
		// KillSessionWithProcesses might return an error even if it successfully
		// killed the processes and the session auto-closed
		stillExists, _ := t.HasSession(sess)
		if !stillExists {
			fmt.Printf("  %s %s stopped\n", style.Bold.Render("✓"), sess)
			return true
		}
		return false
	}

	// 1. Stop polecats and crew first (workers)
	for _, sess := range polecats {
		if killAndVerify(sess) {
			stopped++
		}
	}

	// 2. Stop refineries (work processors)
	for _, sess := range refineries {
		if killAndVerify(sess) {
			stopped++
		}
	}

	// 3. Stop witnesses (monitors)
	for _, sess := range witnesses {
		if killAndVerify(sess) {
			stopped++
		}
	}

	// 4. Stop town sessions: Mayor, Boot, Deacon (matching TownSessions() order)
	if sessionSet[mayorSession] {
		if killAndVerify(mayorSession) {
			stopped++
		}
	}
	if sessionSet[bootSession] {
		if killAndVerify(bootSession) {
			stopped++
		}
	}
	if sessionSet[deaconSession] {
		if killAndVerify(deaconSession) {
			stopped++
		}
	}

	return stopped
}

// cleanupPolecats removes polecat worktrees and branches for all rigs.
// It refuses to clean up polecats with uncommitted work unless --nuclear is set.
func cleanupPolecats(townRoot string) {
	// Load rigs config
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		fmt.Printf("  %s Could not load rigs config: %v\n", style.Dim.Render("○"), err)
		return
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)

	// Discover all rigs
	rigs, err := rigMgr.DiscoverRigs()
	if err != nil {
		fmt.Printf("  %s Could not discover rigs: %v\n", style.Dim.Render("○"), err)
		return
	}

	totalCleaned := 0
	totalSkipped := 0
	var uncommittedPolecats []string

	for _, r := range rigs {
		polecatGit := git.NewGit(r.Path)
		polecatMgr := polecat.NewManager(r, polecatGit, nil) // nil tmux: just listing, not allocating

		polecats, err := polecatMgr.List()
		if err != nil {
			continue
		}

		for _, p := range polecats {
			// Check for uncommitted work
			pGit := git.NewGit(p.ClonePath)
			status, err := pGit.CheckUncommittedWork()
			if err != nil {
				// Can't check, be safe and skip unless nuclear
				if !shutdownNuclear {
					fmt.Printf("  %s %s/%s: could not check status, skipping\n",
						style.Dim.Render("○"), r.Name, p.Name)
					totalSkipped++
					continue
				}
			} else if !status.Clean() {
				// Has uncommitted work
				if !shutdownNuclear {
					uncommittedPolecats = append(uncommittedPolecats,
						fmt.Sprintf("%s/%s (%s)", r.Name, p.Name, status.String()))
					totalSkipped++
					continue
				}
				// Nuclear mode: warn but proceed
				fmt.Printf("  %s %s/%s: NUCLEAR - removing despite %s\n",
					style.Bold.Render("⚠"), r.Name, p.Name, status.String())
			}

			// Clean: remove worktree and branch
			// selfNuke=false because this is gt start --shutdown cleanup, not polecat self-deleting
			if err := polecatMgr.RemoveWithOptions(p.Name, true, shutdownNuclear, false); err != nil {
				fmt.Printf("  %s %s/%s: cleanup failed: %v\n",
					style.Dim.Render("○"), r.Name, p.Name, err)
				totalSkipped++
				continue
			}

			// Delete the polecat branch from mayor's clone
			branchName := fmt.Sprintf("polecat/%s", p.Name)
			mayorPath := filepath.Join(r.Path, "mayor", "rig")
			mayorGit := git.NewGit(mayorPath)
			_ = mayorGit.DeleteBranch(branchName, true) // Ignore errors

			fmt.Printf("  %s %s/%s: cleaned up\n", style.Bold.Render("✓"), r.Name, p.Name)
			totalCleaned++
		}
	}

	// Summary
	if len(uncommittedPolecats) > 0 {
		fmt.Println()
		fmt.Printf("  %s Polecats with uncommitted work (use --nuclear to force):\n",
			style.Bold.Render("⚠"))
		for _, pc := range uncommittedPolecats {
			fmt.Printf("    • %s\n", pc)
		}
	}

	if totalCleaned > 0 || totalSkipped > 0 {
		fmt.Printf("  Cleaned: %d, Skipped: %d\n", totalCleaned, totalSkipped)
	} else {
		fmt.Printf("  %s No polecats to clean up\n", style.Dim.Render("○"))
	}
}

// stopDaemonIfRunning stops the daemon if it is running.
// This prevents the daemon from restarting agents after shutdown.
// Uses robust detection with fallback to process search.
func stopDaemonIfRunning(townRoot string) {
	// Primary detection: PID file
	running, pid, err := daemon.IsRunning(townRoot)

	if err != nil {
		// Detection error - report it but continue with fallback
		fmt.Printf("  %s Daemon detection warning: %s\n", style.Bold.Render("⚠"), err.Error())
	}

	if running {
		// PID file points to live daemon - stop it
		if err := daemon.StopDaemon(townRoot); err != nil {
			fmt.Printf("  %s Failed to stop daemon (PID %d): %s\n",
				style.Bold.Render("✗"), pid, err.Error())
		} else {
			fmt.Printf("  %s Daemon stopped (was PID %d)\n", style.Bold.Render("✓"), pid)
		}
	} else {
		fmt.Printf("  %s Daemon not tracked by PID file\n", style.Dim.Render("○"))
	}

	// Fallback: Search for orphaned daemon processes
	orphaned, err := daemon.FindOrphanedDaemons(townRoot)
	if err != nil {
		fmt.Printf("  %s Warning: failed to search for orphaned daemons: %v\n",
			style.Dim.Render("○"), err)
		return
	}

	if len(orphaned) > 0 {
		fmt.Printf("  %s Found %d orphaned daemon process(es): %v\n",
			style.Bold.Render("⚠"), len(orphaned), orphaned)

		killed, err := daemon.KillOrphanedDaemons(townRoot)
		if err != nil {
			fmt.Printf("  %s Failed to kill orphaned daemons: %v\n",
				style.Bold.Render("✗"), err)
		} else if killed > 0 {
			fmt.Printf("  %s Killed %d orphaned daemon(s)\n",
				style.Bold.Render("✓"), killed)
		}
	}
}

// runStartCrew starts a crew workspace, creating it if it doesn't exist.
// This combines the functionality of 'gt crew add' and 'gt crew at --detached'.
func runStartCrew(cmd *cobra.Command, args []string) error {
	name := args[0]

	// Parse rig/name format (e.g., "greenplace/joe" -> rig=gastown, name=joe)
	rigName := startCrewRig
	if parsedRig, crewName, ok := parseRigSlashName(name); ok {
		if rigName == "" {
			rigName = parsedRig
		}
		name = crewName
	}

	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// If rig still not specified, try to infer from cwd, then by crew name
	if rigName == "" {
		rigName, err = inferRigFromCwd(townRoot)
		if err != nil {
			rigName, err = inferRigFromCrewName(townRoot, name)
			if err != nil {
				return fmt.Errorf("could not determine rig (use --rig flag or rig/name format): %w", err)
			}
		}
	}

	// Load rigs config
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	// Get rig
	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(rigName)
	if err != nil {
		return fmt.Errorf("rig '%s' not found", rigName)
	}

	// Create crew manager
	crewGit := git.NewGit(r.Path)
	crewMgr := crew.NewManager(r, crewGit)

	// Resolve account for Claude config
	accountsPath := constants.MayorAccountsPath(townRoot)
	claudeConfigDir, accountHandle, err := config.ResolveAccountConfigDir(accountsPath, startCrewAccount)
	if err != nil {
		return fmt.Errorf("resolving account: %w", err)
	}
	if accountHandle != "" {
		fmt.Printf("Using account: %s\n", accountHandle)
	}

	// Use manager's Start() method - handles workspace creation, settings, and session
	err = crewMgr.Start(name, crew.StartOptions{
		Account:         startCrewAccount,
		ClaudeConfigDir: claudeConfigDir,
		AgentOverride:   startCrewAgentOverride,
	})
	if err != nil {
		if errors.Is(err, crew.ErrSessionRunning) {
			fmt.Printf("%s Session already running: %s\n", style.Dim.Render("○"), crewMgr.SessionName(name))
		} else {
			return err
		}
	} else {
		fmt.Printf("%s Started crew workspace: %s/%s\n",
			style.Bold.Render("✓"), rigName, name)
	}

	fmt.Printf("Attach with: %s\n", style.Dim.Render(fmt.Sprintf("gt crew at %s", name)))
	return nil
}

// getCrewToStart reads rig settings and parses the crew.startup field.
// Returns a list of crew names to start.
func getCrewToStart(r *rig.Rig) []string {
	// Load rig settings
	settingsPath := filepath.Join(r.Path, "settings", "config.json")
	settings, err := config.LoadRigSettings(settingsPath)
	if err != nil {
		return nil
	}

	if settings.Crew == nil || settings.Crew.Startup == "" || settings.Crew.Startup == "none" {
		return nil
	}

	startup := settings.Crew.Startup

	// Handle "all" - list all existing crew
	if startup == "all" {
		crewGit := git.NewGit(r.Path)
		crewMgr := crew.NewManager(r, crewGit)
		workers, err := crewMgr.List()
		if err != nil {
			return nil
		}
		var names []string
		for _, w := range workers {
			names = append(names, w.Name)
		}
		return names
	}

	// Parse names: "max", "max and joe", "max, joe", "max, joe, emma"
	// Replace "and" with comma for uniform parsing
	startup = strings.ReplaceAll(startup, " and ", ", ")
	parts := strings.Split(startup, ",")

	var names []string
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name != "" {
			names = append(names, name)
		}
	}

	return names
}

// startCrewMember starts a single crew member, creating if needed.
// This is a simplified version of runStartCrew that doesn't print output.
func startCrewMember(rigName, crewName, townRoot string) error {
	// Load rigs config
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	// Get rig
	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(rigName)
	if err != nil {
		return fmt.Errorf("rig '%s' not found", rigName)
	}

	// Create crew manager and use Start() method
	crewGit := git.NewGit(r.Path)
	crewMgr := crew.NewManager(r, crewGit)

	// Start handles workspace creation, settings, and session all in one
	err = crewMgr.Start(crewName, crew.StartOptions{})
	if err != nil && !errors.Is(err, crew.ErrSessionRunning) {
		return err
	}

	return nil
}
