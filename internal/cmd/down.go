package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/crew"
	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

const (
	shutdownLockFile    = "daemon/shutdown.lock"
	shutdownLockTimeout = 5 * time.Second

	// ShutdownSentinel is a file written during gt down to prevent agents from
	// restarting the daemon mid-shutdown. Checked by ensureDaemon.
	ShutdownSentinel = "daemon/shutting-down"

	// defaultDownOrphanGraceSecs is the grace period for orphan cleanup during gt down.
	// Short because gt down is meant to be quick - processes already had SIGTERM via
	// KillSessionWithProcesses.
	defaultDownOrphanGraceSecs = 5
)

var downCmd = &cobra.Command{
	Use:     "down",
	GroupID: GroupServices,
	Short:   "Stop all Gas Town services",
	Long: `Stop Gas Town services (reversible pause).

Shutdown levels (progressively more aggressive):
  gt down                    Stop infrastructure (default)
  gt down --polecats         Also stop all polecat sessions
  gt down --all              Full shutdown with orphan cleanup
  gt down --nuke             Also kill the shared tmux server

Infrastructure agents stopped:
  • Crew       - Per-rig crew member sessions
  • Refineries - Per-rig work processors
  • Witnesses  - Per-rig polecat managers
  • Mayor      - Global work coordinator
  • Boot       - Deacon's watchdog
  • Deacon     - Health orchestrator
  • Daemon     - Go background process
  • Dolt       - Shared SQL database server

This is a "pause" operation - use 'gt start' to bring everything back up.
For permanent cleanup (removing worktrees), use 'gt shutdown' instead.

Use cases:
  • Taking a break (stop token consumption)
  • Clean shutdown before system maintenance
  • Resetting the town to a clean state`,
	RunE: runDown,
}

var (
	downQuiet    bool
	downForce    bool
	downAll      bool
	downNuke     bool
	downDryRun   bool
	downPolecats bool
)

func init() {
	downCmd.Flags().BoolVarP(&downQuiet, "quiet", "q", false, "Only show errors")
	downCmd.Flags().BoolVarP(&downForce, "force", "f", false, "Force kill without graceful shutdown")
	downCmd.Flags().BoolVarP(&downPolecats, "polecats", "p", false, "Also stop all polecat sessions")
	downCmd.Flags().BoolVarP(&downAll, "all", "a", false, "Full shutdown with orphan cleanup and verification")
	downCmd.Flags().BoolVar(&downNuke, "nuke", false, "Kill the shared tmux server (default socket) and all its sessions")
	downCmd.Flags().BoolVar(&downDryRun, "dry-run", false, "Preview what would be stopped without taking action")
	rootCmd.AddCommand(downCmd)
}

func runDown(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	t := tmux.NewTmux()
	if !t.IsAvailable() {
		return fmt.Errorf("tmux not available (is tmux installed and on PATH?)")
	}

	// Phase 0: Acquire shutdown lock (skip for dry-run)
	if !downDryRun {
		lock, err := acquireShutdownLock(townRoot)
		if err != nil {
			return fmt.Errorf("cannot proceed: %w", err)
		}
		defer func() {
			_ = lock.Unlock()
			// Do NOT remove the lock file. Flock works on file descriptors,
			// not paths. Removing the file while another process is waiting
			// on the flock causes it to acquire a lock on the deleted inode,
			// providing no mutual exclusion against a process that creates a
			// new file at the same path.
		}()

		// GH#2656: Write shutdown sentinel to prevent agents from restarting the
		// daemon while we're tearing down. ensureDaemon checks for this file.
		sentinelPath := filepath.Join(townRoot, ShutdownSentinel)
		_ = os.WriteFile(sentinelPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0644)
		defer os.Remove(sentinelPath)

		// Prevent tmux server from exiting when all sessions are killed.
		// By default, tmux exits when there are no sessions (exit-empty on).
		// This ensures the server stays running for subsequent `gt up`.
		// Ignore errors - if there's no server, nothing to configure.
		_ = t.SetExitEmpty(false)
	}
	allOK := true

	if downDryRun {
		fmt.Println("═══ DRY RUN: Preview of shutdown actions ═══")
		fmt.Println()
	}

	rigs := discoverRigs(townRoot)

	// Phase 0.5: Stop polecats if --polecats
	if downPolecats {
		if downDryRun {
			fmt.Println("Would stop polecats...")
		} else {
			fmt.Println("Stopping polecats...")
		}
		polecatsStopped := stopAllPolecats(t, townRoot, rigs, downForce, downDryRun)
		if downDryRun {
			if polecatsStopped > 0 {
				printDownStatus("Polecats", true, fmt.Sprintf("%d would stop", polecatsStopped))
			} else {
				printDownStatus("Polecats", true, "none running")
			}
		} else {
			if polecatsStopped > 0 {
				printDownStatus("Polecats", true, fmt.Sprintf("%d stopped", polecatsStopped))
			} else {
				printDownStatus("Polecats", true, "none running")
			}
		}
		fmt.Println()
	}

	// Phase 0.6: Stop crew member sessions.
	// Crew sessions consume tokens and must be stopped during any shutdown.
	crewStopped := stopAllCrew(t, townRoot, rigs, downDryRun)
	if downDryRun {
		if crewStopped > 0 {
			printDownStatus("Crew", true, fmt.Sprintf("%d would stop", crewStopped))
		}
	} else {
		if crewStopped > 0 {
			printDownStatus("Crew", true, fmt.Sprintf("%d stopped", crewStopped))
		}
	}

	// Phase 1: Stop refineries
	for _, rigName := range rigs {
		sessionName := session.RefinerySessionName(session.PrefixFor(rigName))
		if downDryRun {
			if running, _ := t.HasSession(sessionName); running {
				printDownStatus(fmt.Sprintf("Refinery (%s)", rigName), true, "would stop")
			}
			continue
		}
		wasRunning, err := stopSession(t, sessionName)
		if err != nil {
			printDownStatus(fmt.Sprintf("Refinery (%s)", rigName), false, err.Error())
			allOK = false
		} else if wasRunning {
			printDownStatus(fmt.Sprintf("Refinery (%s)", rigName), true, "stopped")
		} else {
			printDownStatus(fmt.Sprintf("Refinery (%s)", rigName), true, "not running")
		}
	}

	// Phase 2: Stop witnesses
	for _, rigName := range rigs {
		sessionName := session.WitnessSessionName(session.PrefixFor(rigName))
		if downDryRun {
			if running, _ := t.HasSession(sessionName); running {
				printDownStatus(fmt.Sprintf("Witness (%s)", rigName), true, "would stop")
			}
			continue
		}
		wasRunning, err := stopSession(t, sessionName)
		if err != nil {
			printDownStatus(fmt.Sprintf("Witness (%s)", rigName), false, err.Error())
			allOK = false
		} else if wasRunning {
			printDownStatus(fmt.Sprintf("Witness (%s)", rigName), true, "stopped")
		} else {
			printDownStatus(fmt.Sprintf("Witness (%s)", rigName), true, "not running")
		}
	}

	// Phase 3: Stop town-level sessions (Mayor, Boot, Deacon)
	for _, ts := range session.TownSessions() {
		if downDryRun {
			if running, _ := t.HasSession(ts.SessionID); running {
				printDownStatus(ts.Name, true, "would stop")
			}
			continue
		}
		stopped, err := session.StopTownSession(t, ts, downForce)
		if err != nil {
			printDownStatus(ts.Name, false, err.Error())
			allOK = false
		} else if stopped {
			printDownStatus(ts.Name, true, "stopped")
		} else {
			printDownStatus(ts.Name, true, "not running")
		}
	}

	// Phase 4: Stop Daemon
	running, pid, daemonErr := daemon.IsRunning(townRoot)
	if daemonErr != nil {
		printDownStatus("Daemon", false, fmt.Sprintf("status check failed: %v", daemonErr))
		allOK = false
	} else if downDryRun {
		if running {
			printDownStatus("Daemon", true, fmt.Sprintf("would stop (PID %d)", pid))
		}
	} else {
		if running {
			if err := daemon.StopDaemon(townRoot); err != nil {
				printDownStatus("Daemon", false, err.Error())
				allOK = false
			} else if pid > 0 {
				printDownStatus("Daemon", true, fmt.Sprintf("stopped (was PID %d)", pid))
			} else {
				printDownStatus("Daemon", true, "stopped (stale lock cleaned)")
			}
		} else {
			printDownStatus("Daemon", true, "not running")
		}
	}

	// Phase 4b-i: Stop bd dolt idle-monitor processes.
	// These background processes respawn per-agent Dolt servers after they're
	// terminated, creating a race condition where rogues grab the port before
	// the canonical server can restart. Must be stopped BEFORE Dolt shutdown.
	idleMonitors := doltserver.FindIdleMonitorProcesses(townRoot)
	if len(idleMonitors) > 0 {
		if downDryRun {
			printDownStatus("Dolt idle-monitors", true, fmt.Sprintf("%d would stop", len(idleMonitors)))
		} else {
			stopped := stopIdleMonitors(idleMonitors)
			if stopped > 0 {
				printDownStatus("Dolt idle-monitors", true, fmt.Sprintf("stopped %d", stopped))
			}
		}
	}

	// Phase 4b-ii: Stop Dolt server
	doltCfg := doltserver.DefaultConfig(townRoot)
	if _, statErr := os.Stat(doltCfg.DataDir); statErr == nil {
		doltRunning, doltPid, doltErr := doltserver.IsRunning(townRoot)
		if doltErr != nil {
			printDownStatus("Dolt", false, fmt.Sprintf("status check failed: %v", doltErr))
			allOK = false
		} else if downDryRun {
			if doltRunning {
				printDownStatus("Dolt", true, fmt.Sprintf("would stop (PID %d)", doltPid))
			}
		} else {
			if doltRunning {
				if err := doltserver.Stop(townRoot); err != nil {
					printDownStatus("Dolt", false, err.Error())
					allOK = false
				} else {
					printDownStatus("Dolt", true, fmt.Sprintf("stopped (was PID %d)", doltPid))
				}
			} else {
				printDownStatus("Dolt", true, "not running")
			}
		}
	}

	// Phase 4b-iii: Stop imposter Dolt servers.
	// After stopping the canonical server, rogue Dolt servers spawned by bd
	// from .beads/dolt/ directories may still be running. KillImposters only
	// catches servers on our port, so also scan for any dolt sql-server
	// processes rooted in this town's directory tree.
	if !downDryRun {
		if err := doltserver.KillImposters(townRoot); err != nil {
			printDownStatus("Dolt imposters", false, err.Error())
			allOK = false
		}
		orphanDolts := findOrphanDoltServers(townRoot)
		if len(orphanDolts) > 0 {
			stopped := stopOrphanDoltServers(orphanDolts)
			if stopped > 0 {
				printDownStatus("Dolt orphans", true, fmt.Sprintf("stopped %d rogue server(s)", stopped))
			}
		}
	} else {
		conflictPID, _ := doltserver.CheckPortConflict(townRoot)
		if conflictPID > 0 {
			printDownStatus("Dolt imposters", true, fmt.Sprintf("would stop imposter (PID %d)", conflictPID))
		}
		orphanDolts := findOrphanDoltServers(townRoot)
		if len(orphanDolts) > 0 {
			printDownStatus("Dolt orphans", true, fmt.Sprintf("%d rogue server(s) would stop", len(orphanDolts)))
		}
	}

	// Phase 4b-iv: Remove .beads/dolt directories.
	// These legacy per-agent data directories trigger bd to auto-spawn local
	// Dolt servers. Removing them prevents rogue respawn on next gt up.
	// Data has already been migrated to .dolt-data/ by gt dolt migrate.
	beadsDoltDirs := findBeadsDoltDirs(townRoot)
	if len(beadsDoltDirs) > 0 {
		if downDryRun {
			printDownStatus("Beads dolt dirs", true, fmt.Sprintf("%d would remove", len(beadsDoltDirs)))
		} else {
			removed := removeBeadsDoltDirs(beadsDoltDirs)
			if removed > 0 {
				printDownStatus("Beads dolt dirs", true, fmt.Sprintf("removed %d", removed))
			}
		}
	}

	// Phase 4c: Clean up legacy socket sessions.
	// Old binaries created sessions on the "default" tmux socket or on the
	// basename-only socket (e.g., "gt" instead of "gt-a1b2c3"). After
	// transitioning to path-hashed sockets, ghost sessions on old sockets
	// persist and cause split-brain.
	if !downDryRun {
		cleaned := cleanupLegacyDefaultSocket()
		if cleaned > 0 {
			printDownStatus("Legacy sessions", true, fmt.Sprintf("cleaned %d from 'default' socket", cleaned))
		}
		cleaned = cleanupLegacyBaseSocket(townRoot)
		if cleaned > 0 {
			printDownStatus("Legacy sessions", true, fmt.Sprintf("cleaned %d from old basename socket", cleaned))
		}
	} else {
		count := countLegacyDefaultSocketSessions()
		if count > 0 {
			printDownStatus("Legacy sessions", true, fmt.Sprintf("%d would be cleaned from 'default' socket", count))
		}
		count = countLegacyBaseSocketSessions(townRoot)
		if count > 0 {
			printDownStatus("Legacy sessions", true, fmt.Sprintf("%d would be cleaned from old basename socket", count))
		}
	}

	// Phase 5: Orphan cleanup and verification (--all or --force)
	if (downAll || downForce) && !downDryRun {
		fmt.Println()

		// Kill any processes tracked via PID files (defense-in-depth for
		// processes that survived normal session teardown).
		killed, pidErrs := session.KillTrackedPIDs(townRoot)
		if killed > 0 {
			fmt.Printf("  Killed %d tracked orphan process(es) via PID files\n", killed)
		}
		for _, e := range pidErrs {
			fmt.Printf("  PID cleanup warning: %s\n", e)
		}

		fmt.Println("Cleaning up orphaned Claude processes...")
		cleanupOrphanedClaude(defaultDownOrphanGraceSecs)

		time.Sleep(500 * time.Millisecond)
		respawned := verifyShutdown(t, townRoot)
		if len(respawned) > 0 {
			fmt.Println()
			fmt.Printf("%s Warning: Some processes may have respawned:\n", style.Bold.Render("⚠"))
			for _, r := range respawned {
				fmt.Printf("  • %s\n", r)
			}
			fmt.Println()
			fmt.Printf("This may indicate a process manager is respawning agents.\n")
			fmt.Printf("Check with:\n")
			fmt.Printf("  %s\n", style.Dim.Render("ps aux | grep claude  # Find respawned processes"))
			fmt.Printf("  %s\n", style.Dim.Render("gt status             # Verify town state"))
			allOK = false
		}
	}

	// Phase 6: Nuke tmux server (--nuke only)
	// Each town uses a per-town tmux socket derived from a hash of the town's
	// canonical path (see registry.go townSocketName), so --nuke only affects
	// this town's server. Users may also have opened custom windows/panes, so
	// we require confirmation.
	if downNuke {
		socket := tmux.GetDefaultSocket()
		socketLabel := "default"
		if socket != "" {
			socketLabel = socket
		}
		if downDryRun {
			printDownStatus("Tmux server", true, fmt.Sprintf("would kill (socket: %s)", socketLabel))
		} else if os.Getenv("GT_NUKE_ACKNOWLEDGED") == "" {
			fmt.Println()
			fmt.Printf("%s The --nuke flag kills this town's tmux server (socket: %s).\n",
				style.Bold.Render("⚠ BLOCKED:"), socketLabel)
			fmt.Printf("This will destroy all tmux sessions on this socket, including any custom windows you opened.\n")
			fmt.Println()
			fmt.Printf("To proceed, run with: %s\n", style.Bold.Render("GT_NUKE_ACKNOWLEDGED=1 gt down --nuke"))
			allOK = false
		} else {
			if err := t.KillServer(); err != nil {
				printDownStatus("Tmux server", false, err.Error())
				allOK = false
			} else {
				printDownStatus("Tmux server", true, fmt.Sprintf("killed (socket: %s)", socketLabel))
			}
		}
	}

	// Summary
	fmt.Println()
	if downDryRun {
		fmt.Println("═══ DRY RUN COMPLETE (no changes made) ═══")
		return nil
	}

	if allOK {
		fmt.Printf("%s All services stopped\n", style.Bold.Render("✓"))
		stoppedServices := []string{"dolt", "daemon", "deacon", "boot", "mayor"}
		for _, rigName := range rigs {
			stoppedServices = append(stoppedServices, fmt.Sprintf("%s/refinery", rigName))
			stoppedServices = append(stoppedServices, fmt.Sprintf("%s/witness", rigName))
		}
		if crewStopped > 0 {
			stoppedServices = append(stoppedServices, "crew")
		}
		if downPolecats {
			stoppedServices = append(stoppedServices, "polecats")
		}
		if downAll {
			stoppedServices = append(stoppedServices, "bd-processes")
		}
		if downNuke {
			stoppedServices = append(stoppedServices, "tmux-server")
		}
		_ = events.LogFeed(events.TypeHalt, "gt", events.HaltPayload(stoppedServices))
	} else {
		fmt.Printf("%s Some services failed to stop\n", style.Bold.Render("✗"))
		return fmt.Errorf("not all services stopped")
	}

	return nil
}

// stopAllPolecats stops all polecat sessions across all rigs.
// Stops are performed in parallel for faster teardown.
// Returns the number of polecats stopped (or would be stopped in dry-run).
func stopAllPolecats(t *tmux.Tmux, townRoot string, rigNames []string, force bool, dryRun bool) int {
	stopped := 0

	// Load rigs config
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)

	if dryRun {
		for _, rigName := range rigNames {
			r, err := rigMgr.GetRig(rigName)
			if err != nil {
				continue
			}
			polecatMgr := polecat.NewSessionManager(t, r)
			infos, err := polecatMgr.ListPolecats()
			if err != nil {
				continue
			}
			for _, info := range infos {
				stopped++
				fmt.Printf("  %s [%s] %s would stop\n", style.Dim.Render("○"), rigName, info.Polecat)
			}
		}
		return stopped
	}

	// Collect targets and stop all in parallel.
	type polecatResult struct {
		rigName string
		name    string
		err     error
	}

	var results []polecatResult
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, rigName := range rigNames {
		r, err := rigMgr.GetRig(rigName)
		if err != nil {
			continue
		}

		polecatMgr := polecat.NewSessionManager(t, r)
		infos, err := polecatMgr.ListPolecats()
		if err != nil {
			continue
		}

		for _, info := range infos {
			wg.Add(1)
			go func(rn, name string, mgr *polecat.SessionManager) {
				defer wg.Done()
				err := mgr.Stop(name, force)
				mu.Lock()
				results = append(results, polecatResult{rigName: rn, name: name, err: err})
				mu.Unlock()
			}(rigName, info.Polecat, polecatMgr)
		}
	}
	wg.Wait()

	for _, res := range results {
		if res.err == nil {
			stopped++
			fmt.Printf("  %s [%s] %s stopped\n", style.SuccessPrefix, res.rigName, res.name)
		} else {
			fmt.Printf("  %s [%s] %s: %s\n", style.ErrorPrefix, res.rigName, res.name, res.err.Error())
		}
	}

	return stopped
}

// stopAllCrew stops all crew member sessions across all rigs.
// Stops are performed in parallel for faster teardown.
// Returns the number of crew sessions stopped (or would be stopped in dry-run).
func stopAllCrew(t *tmux.Tmux, townRoot string, rigNames []string, dryRun bool) int {
	stopped := 0

	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)

	// Collect all running crew sessions to stop.
	type crewTarget struct {
		rigName   string
		name      string
		sessionID string
	}
	var targets []crewTarget

	for _, rigName := range rigNames {
		r, err := rigMgr.GetRig(rigName)
		if err != nil {
			continue
		}

		crewMgr := crew.NewManager(r, g)
		workers, err := crewMgr.List()
		if err != nil {
			continue
		}

		for _, worker := range workers {
			sessionID := crewMgr.SessionName(worker.Name)
			running, err := t.HasSession(sessionID)
			if err != nil || !running {
				continue
			}

			if dryRun {
				stopped++
				fmt.Printf("  %s [%s] crew/%s would stop\n", style.Dim.Render("○"), rigName, worker.Name)
				continue
			}
			targets = append(targets, crewTarget{rigName: rigName, name: worker.Name, sessionID: sessionID})
		}
	}

	if len(targets) == 0 {
		return stopped
	}

	// Stop all crew sessions in parallel.
	type crewResult struct {
		rigName string
		name    string
		err     error
	}
	results := make([]crewResult, len(targets))
	var wg sync.WaitGroup

	for i, tgt := range targets {
		wg.Add(1)
		go func(i int, tgt crewTarget) {
			defer wg.Done()
			_, err := stopSession(t, tgt.sessionID)
			results[i] = crewResult{rigName: tgt.rigName, name: tgt.name, err: err}
		}(i, tgt)
	}
	wg.Wait()

	for _, res := range results {
		if res.err == nil {
			stopped++
			fmt.Printf("  %s [%s] crew/%s stopped\n", style.SuccessPrefix, res.rigName, res.name)
		} else {
			fmt.Printf("  %s [%s] crew/%s: %s\n", style.ErrorPrefix, res.rigName, res.name, res.err.Error())
		}
	}

	return stopped
}

func printDownStatus(name string, ok bool, detail string) {
	if downQuiet && ok {
		return
	}
	if ok {
		fmt.Printf("%s %s: %s\n", style.SuccessPrefix, name, style.Dim.Render(detail))
	} else {
		fmt.Printf("%s %s: %s\n", style.ErrorPrefix, name, detail)
	}
}

// stopSession gracefully stops a tmux session.
// Returns (wasRunning, error) - wasRunning is true if session existed and was stopped.
func stopSession(t *tmux.Tmux, sessionName string) (bool, error) {
	running, err := t.HasSession(sessionName)
	if err != nil {
		return false, err
	}
	if !running {
		return false, nil // Already stopped
	}

	// Try graceful shutdown first (Ctrl-C, best-effort interrupt)
	if !downForce {
		_ = t.SendKeysRaw(sessionName, "C-c")
		if session.WaitForSessionExit(t, sessionName, constants.GracefulShutdownTimeout) {
			return true, nil // Process exited gracefully
		}
	}

	// Kill the session (with explicit process termination to prevent orphans)
	return true, t.KillSessionWithProcesses(sessionName)
}

// acquireShutdownLock prevents concurrent shutdowns.
// Returns the lock (caller must defer Unlock()) or error if lock held.
func acquireShutdownLock(townRoot string) (*flock.Flock, error) {
	lockPath := filepath.Join(townRoot, shutdownLockFile)

	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		return nil, fmt.Errorf("creating lock directory: %w", err)
	}

	lock := flock.New(lockPath)

	ctx, cancel := context.WithTimeout(context.Background(), shutdownLockTimeout)
	defer cancel()

	locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("lock acquisition failed: %w", err)
	}

	if !locked {
		return nil, fmt.Errorf("another shutdown is in progress (lock held: %s)", lockPath)
	}

	return lock, nil
}

// verifyShutdown checks for respawned processes after shutdown.
// Returns list of things that are still running or respawned.
func verifyShutdown(t *tmux.Tmux, townRoot string) []string {
	var respawned []string

	sessions, err := t.ListSessions()
	if err == nil {
		for _, sess := range sessions {
			if session.IsKnownSession(sess) {
				respawned = append(respawned, fmt.Sprintf("tmux session %s", sess))
			}
		}
	}

	pidFile := filepath.Join(townRoot, "daemon", "daemon.pid")
	if pidData, err := os.ReadFile(pidFile); err == nil {
		var pid int
		if _, err := fmt.Sscanf(string(pidData), "%d", &pid); err == nil {
			if isProcessRunning(pid) {
				respawned = append(respawned, fmt.Sprintf("gt daemon (PID %d)", pid))
			}
		}
	}

	// Check for orphaned Claude/node processes
	// These can be left behind if tmux sessions were killed but child processes didn't terminate
	if pids := findOrphanedClaudeProcesses(townRoot); len(pids) > 0 {
		respawned = append(respawned, fmt.Sprintf("orphaned Claude processes (PIDs: %v)", pids))
	}

	// Check for respawned idle-monitors
	if pids := doltserver.FindIdleMonitorProcesses(townRoot); len(pids) > 0 {
		respawned = append(respawned, fmt.Sprintf("bd dolt idle-monitor processes (PIDs: %v)", pids))
	}

	// Check for orphan Dolt servers from .beads/dolt directories
	if pids := findOrphanDoltServers(townRoot); len(pids) > 0 {
		respawned = append(respawned, fmt.Sprintf("orphan Dolt servers (PIDs: %v)", pids))
	}

	return respawned
}

// findOrphanedClaudeProcesses finds Gas Town agent processes (claude/codex/opencode/cursor-agent/copilot/node)
// that are running in the town directory but aren't associated with any active tmux session.
// This can happen when tmux sessions are killed but child processes don't terminate.
//
// Only matches processes whose full command line references the town root path,
// which avoids false positives on unrelated Node.js applications (VS Code
// extensions, web servers, etc.).
func findOrphanedClaudeProcesses(townRoot string) []int {
	// Use ps to get PID, process name, and full command line in a single pass.
	// Previous implementation used "pgrep -l node" which matched ALL node
	// processes on the system regardless of whether they belonged to Gas Town.
	out, err := exec.Command("ps", "-eo", "pid,comm,args").Output()
	if err != nil {
		return nil
	}

	var orphaned []int
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		var pid int
		if _, err := fmt.Sscanf(fields[0], "%d", &pid); err != nil {
			continue
		}

		// Only consider known Gas Town process names
		comm := strings.ToLower(fields[1])
		switch comm {
		case "claude", "claude-code", "codex", "opencode", "cursor-agent", "agent", "copilot", "node":
			// Potential Gas Town process
		default:
			continue
		}

		// Verify the process's command line references the town root.
		// This filters out unrelated node processes (VS Code, web servers, etc.)
		// whose command lines won't contain the Gas Town directory path.
		args := strings.Join(fields[2:], " ")
		if strings.Contains(args, townRoot) {
			orphaned = append(orphaned, pid)
		}
	}

	return orphaned
}

// cleanupLegacyDefaultSocket removes Gas Town sessions left on the "default"
// tmux socket by old binaries. Returns the number of sessions cleaned.
func cleanupLegacyDefaultSocket() int {
	return session.CleanupLegacyDefaultSocket()
}

// countLegacyDefaultSocketSessions counts Gas Town sessions on the "default"
// tmux socket (for dry-run output).
func countLegacyDefaultSocketSessions() int {
	return session.CountLegacyDefaultSocketSessions()
}

// cleanupLegacyBaseSocket removes Gas Town sessions left on the old basename-only
// tmux socket (e.g., "gt") by binaries from before path-hashed socket names were
// introduced (e.g., "gt-a1b2c3"). Returns the number of sessions cleaned.
func cleanupLegacyBaseSocket(townRoot string) int {
	return session.CleanupLegacyBaseSocket(townRoot)
}

// countLegacyBaseSocketSessions counts Gas Town sessions on the old basename-only
// tmux socket (for dry-run output).
func countLegacyBaseSocketSessions(townRoot string) int {
	return session.CountLegacyBaseSocketSessions(townRoot)
}

// stopIdleMonitors terminates idle-monitor processes.
// Returns the number of processes successfully stopped.
func stopIdleMonitors(pids []int) int {
	var stopped int
	for _, pid := range pids {
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		if err := proc.Signal(os.Interrupt); err != nil {
			// Process may have already exited
			continue
		}
		// Brief wait for graceful exit
		time.Sleep(200 * time.Millisecond)
		if !isProcessRunning(pid) {
			stopped++
			continue
		}
		_ = proc.Kill()
		stopped++
	}
	return stopped
}

// findOrphanDoltServers finds dolt sql-server processes whose working
// directory is within the town root but NOT the canonical .dolt-data/ dir.
// These are rogues spawned by bd from .beads/dolt/ directories.
func findOrphanDoltServers(townRoot string) []int {
	out, err := exec.Command("ps", "-eo", "pid,args").Output()
	if err != nil {
		return nil
	}

	canonicalDir, _ := filepath.Abs(filepath.Join(townRoot, ".dolt-data"))
	townAbs, _ := filepath.Abs(townRoot)

	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "dolt") || !strings.Contains(line, "sql-server") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}

		// Check the process's working directory via lsof
		cwdOut, err := exec.Command("lsof", "-p", strconv.Itoa(pid), "-Fn", "-d", "cwd").Output()
		if err != nil {
			continue
		}
		cwd := ""
		for _, cwdLine := range strings.Split(string(cwdOut), "\n") {
			if strings.HasPrefix(cwdLine, "n") {
				cwd = cwdLine[1:]
				break
			}
		}
		if cwd == "" {
			continue
		}

		cwdAbs, _ := filepath.Abs(cwd)
		// Only target processes rooted in our town but NOT in canonical data dir.
		// Use path-boundary check to avoid false matches on sibling paths.
		inTown := cwdAbs == townAbs || strings.HasPrefix(cwdAbs, townAbs+string(filepath.Separator))
		notCanonical := !strings.HasPrefix(cwdAbs, canonicalDir)
		if inTown && notCanonical {
			pids = append(pids, pid)
		}
	}
	return pids
}

// stopOrphanDoltServers terminates orphan Dolt servers.
// Returns the number of processes stopped.
func stopOrphanDoltServers(pids []int) int {
	var stopped int
	for _, pid := range pids {
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		if err := proc.Signal(os.Interrupt); err != nil {
			continue
		}
		// Wait up to 3s for Dolt to flush and exit
		for i := 0; i < 6; i++ {
			time.Sleep(500 * time.Millisecond)
			if !isProcessRunning(pid) {
				break
			}
		}
		if isProcessRunning(pid) {
			_ = proc.Kill()
		}
		stopped++
	}
	return stopped
}

// findBeadsDoltDirs finds .beads/dolt directories that trigger bd auto-spawning.
// These are legacy per-agent data directories that should have been migrated
// to .dolt-data/ by gt dolt migrate.
func findBeadsDoltDirs(townRoot string) []string {
	var dirs []string
	townAbs, _ := filepath.Abs(townRoot)

	_ = filepath.WalkDir(townAbs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			return nil
		}

		// Skip .dolt-data (canonical data), .git, node_modules, etc.
		name := d.Name()
		if name == ".dolt-data" || name == ".git" || name == "node_modules" || name == ".repo.git" {
			return filepath.SkipDir
		}

		// Limit depth to avoid deep traversal
		rel, _ := filepath.Rel(townAbs, path)
		if strings.Count(rel, string(filepath.Separator)) > 5 {
			return filepath.SkipDir
		}

		// Match .beads/dolt directories
		if name == "dolt" && strings.HasSuffix(filepath.Dir(path), ".beads") {
			dirs = append(dirs, path)
			return filepath.SkipDir
		}

		return nil
	})
	return dirs
}

// removeBeadsDoltDirs removes legacy .beads/dolt directories that are safe to
// delete. A directory is safe if it is empty or contains only Dolt metadata
// (no .dolt subdirectory with actual database content). Directories with
// unmigrated database data are skipped to avoid data loss.
// Returns count removed.
func removeBeadsDoltDirs(dirs []string) int {
	var removed int
	for _, dir := range dirs {
		if !isSafeToRemoveBeadsDolt(dir) {
			fmt.Fprintf(os.Stderr, "Warning: skipping %s — may contain unmigrated data\n", dir)
			continue
		}
		if err := os.RemoveAll(dir); err == nil {
			removed++
		}
	}
	return removed
}

// isSafeToRemoveBeadsDolt checks if a .beads/dolt directory can be safely
// removed. Safe means: empty, or contains no actual database content
// (no .dolt subdirectory with working data). Unmigrated databases have
// a .dolt/ directory inside with noms/manifest files.
func isSafeToRemoveBeadsDolt(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false // can't read it, don't remove it
	}
	if len(entries) == 0 {
		return true // empty dir is safe
	}

	// Check if any subdirectory contains a .dolt directory (unmigrated DB)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dotDolt := filepath.Join(dir, entry.Name(), ".dolt")
		if _, err := os.Stat(dotDolt); err == nil {
			return false // has unmigrated database data
		}
	}

	// Also check if .dolt exists directly in this dir
	if _, err := os.Stat(filepath.Join(dir, ".dolt")); err == nil {
		return false
	}

	return true
}
