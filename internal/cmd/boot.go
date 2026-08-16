package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/boot"
	"github.com/jonbaldie/gastown/internal/daemon"
	"github.com/jonbaldie/gastown/internal/deacon"
	"github.com/jonbaldie/gastown/internal/session"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	bootStatusJSON    bool
	bootDegraded      bool
	bootAgentOverride string
)

var bootCmd = &cobra.Command{
	Use:     "boot",
	GroupID: GroupAgents,
	Short:   "Manage Boot (Deacon watchdog)",
	Long: `Manage Boot - the daemon's watchdog for Deacon triage.

Boot is a special dog that runs fresh on each daemon tick. It observes
the system state and decides whether to start/wake/nudge/interrupt the
Deacon, or do nothing. This centralizes the "when to wake" decision in
an agent that can reason about it.

Boot lifecycle:
  1. Daemon tick spawns Boot (fresh each time)
  2. Boot runs triage: observe, decide, act
  3. Boot cleans inbox (discards stale handoffs)
  4. Boot exits (or handoffs in non-degraded mode)

Location: ~/gt/deacon/dogs/boot/
Session: gt-boot`,
}

var bootStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Boot status",
	Long: `Show Boot's current status and last execution.

Displays:
  - Whether Boot is currently running
  - Last action taken (start/wake/nudge/nothing)
  - Timing information
  - Degraded mode status`,
	RunE: runBootStatus,
}

var bootSpawnCmd = &cobra.Command{
	Use:   "spawn",
	Short: "Spawn Boot for triage",
	Long: `Spawn Boot to run the triage cycle.

This is normally called by the daemon. It spawns Boot in a fresh
tmux session (or subprocess in degraded mode) to observe and decide
what action to take on the Deacon.

Boot runs to completion and exits - it doesn't maintain state
between invocations.`,
	RunE: runBootSpawn,
}

var bootTriageCmd = &cobra.Command{
	Use:   "triage",
	Short: "Run triage directly (degraded mode)",
	Long: `Run Boot's triage logic directly without Claude.

This is for degraded mode operation when tmux is unavailable.
It performs basic observation and takes conservative action:
  - If Deacon is not running: start it
  - If Deacon appears stuck: attempt restart
  - Otherwise: do nothing

Use --degraded flag when running in degraded mode.`,
	RunE: runBootTriage,
}

func init() {
	bootStatusCmd.Flags().BoolVar(&bootStatusJSON, "json", false, "Output as JSON")
	bootTriageCmd.Flags().BoolVar(&bootDegraded, "degraded", false, "Run in degraded mode (no tmux)")
	bootSpawnCmd.Flags().StringVar(&bootAgentOverride, "agent", "", "Agent alias to run Boot with (overrides town default)")

	bootCmd.AddCommand(bootStatusCmd)
	bootCmd.AddCommand(bootSpawnCmd)
	bootCmd.AddCommand(bootTriageCmd)

	rootCmd.AddCommand(bootCmd)
}

func getBootManager() (*boot.Boot, error) {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return nil, fmt.Errorf("finding town root: %w", err)
	}

	return boot.New(townRoot), nil
}

func runBootStatus(cmd *cobra.Command, args []string) error {
	b, err := getBootManager()
	if err != nil {
		return err
	}

	status, err := b.LoadStatus()
	if err != nil {
		return fmt.Errorf("loading status: %w", err)
	}

	isRunning := b.IsRunning()
	sessionAlive := b.IsSessionAlive()

	if bootStatusJSON {
		output := map[string]interface{}{
			"running":       isRunning,
			"session_alive": sessionAlive,
			"degraded":      b.IsDegraded(),
			"boot_dir":      b.Dir(),
			"last_status":   status,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	// Pretty print
	fmt.Println(style.Bold.Render("Boot Status"))
	fmt.Println()

	if isRunning {
		fmt.Printf("  State: %s\n", style.Bold.Render("running"))
	} else {
		fmt.Printf("  State: %s\n", style.Dim.Render("idle"))
	}

	if sessionAlive {
		fmt.Printf("  Session: %s (alive)\n", session.BootSessionName())
	} else {
		fmt.Printf("  Session: %s\n", style.Dim.Render("not running"))
	}

	if b.IsDegraded() {
		fmt.Printf("  Mode: %s\n", style.Bold.Render("DEGRADED"))
	} else {
		fmt.Printf("  Mode: normal\n")
	}

	fmt.Println()
	fmt.Println(style.Dim.Render("Last Execution:"))

	if status.StartedAt.IsZero() {
		fmt.Printf("  %s\n", style.Dim.Render("(no executions recorded)"))
	} else {
		if !status.CompletedAt.IsZero() {
			duration := status.CompletedAt.Sub(status.StartedAt)
			fmt.Printf("  Completed: %s (%s ago)\n",
				status.CompletedAt.Format("15:04:05"),
				formatDurationAgo(time.Since(status.CompletedAt)))
			fmt.Printf("  Duration:  %s\n", duration.Round(time.Millisecond))
		} else {
			fmt.Printf("  Started: %s\n", status.StartedAt.Format("15:04:05"))
		}

		if status.LastAction != "" {
			fmt.Printf("  Action:  %s", status.LastAction)
			if status.Target != "" {
				fmt.Printf(" → %s", status.Target)
			}
			fmt.Println()
		}

		if status.Error != "" {
			fmt.Printf("  Error:   %s\n", style.Bold.Render(status.Error))
		}
	}

	fmt.Println()
	fmt.Printf("  Dir: %s\n", b.Dir())

	return nil
}

func runBootSpawn(cmd *cobra.Command, args []string) error {
	b, err := getBootManager()
	if err != nil {
		return err
	}

	if b.IsRunning() {
		fmt.Println("Boot is already running - skipping spawn")
		return nil
	}

	// Save starting status
	status := &boot.Status{
		StartedAt: time.Now(),
	}
	if err := b.SaveStatus(status); err != nil {
		return fmt.Errorf("saving status: %w", err)
	}

	// Spawn Boot
	if err := b.Spawn(bootAgentOverride); err != nil {
		status.Error = err.Error()
		status.CompletedAt = time.Now()
		_ = b.SaveStatus(status)
		return fmt.Errorf("spawning boot: %w", err)
	}

	if b.IsDegraded() {
		fmt.Println("Boot spawned in degraded mode (subprocess)")
	} else {
		fmt.Printf("Boot spawned in session: %s\n", session.BootSessionName())
	}

	return nil
}

func runBootTriage(cmd *cobra.Command, args []string) error {
	b, err := getBootManager()
	if err != nil {
		return err
	}

	// Acquire lock
	if err := b.AcquireLock(); err != nil {
		return fmt.Errorf("acquiring lock: %w", err)
	}
	defer func() { _ = b.ReleaseLock() }()

	startTime := time.Now()
	status := &boot.Status{
		StartedAt: startTime,
	}

	// In degraded mode, we do basic mechanical triage
	// without full Claude reasoning capability
	action, target, triageErr := runDegradedTriage(b)

	status.LastAction = action
	status.Target = target
	status.CompletedAt = time.Now()

	if triageErr != nil {
		status.Error = triageErr.Error()
	}

	if err := b.SaveStatus(status); err != nil {
		return fmt.Errorf("saving status: %w", err)
	}

	if triageErr != nil {
		return triageErr
	}

	fmt.Printf("Triage complete: %s", action)
	if target != "" {
		fmt.Printf(" → %s", target)
	}
	fmt.Println()

	return nil
}

// runDegradedTriage performs mechanical Deacon health checks without AI reasoning.
//
// ZFC principle: "Agent decides. Go transports." Complex triage decisions
// (heartbeat staleness, idle detection, backoff awareness, molecule progress)
// belong in the Boot agent's mol-boot-triage formula, not in Go code.
// This function handles only mechanical safety checks that must work even
// when no AI agent is available.
func runDegradedTriage(b *boot.Boot) (action, target string, err error) {
	// Abort triage if a shutdown is in progress. Without this check, Boot could
	// detect Deacon as "down" during the graceful shutdown window and restart it,
	// creating a zombie Deacon that survives gt down.
	townRoot, _ := workspace.FindFromCwd()
	if townRoot != "" && daemon.IsShutdownInProgress(townRoot) {
		return "nothing", "shutdown-in-progress", nil
	}

	tm := b.Tmux()

	// Scan and execute pending death warrants. This is a side effect that runs
	// before the normal triage decision — warrant execution is mechanical and
	// does not affect which action runDegradedTriage returns to the caller.
	if townRoot != "" {
		executeWarrants(filepath.Join(townRoot, "warrants"), tm)
	}

	// Check if Deacon session exists — the only mechanical check degraded
	// triage needs. All deeper reasoning (staleness, idle, backoff, progress)
	// is the Boot agent's job via mol-boot-triage.
	deaconSession := getDeaconSessionName()
	hasDeacon, err := tm.HasSession(deaconSession)
	if err != nil {
		return "error", "deacon", fmt.Errorf("checking deacon session: %w", err)
	}

	if !hasDeacon {
		// Deacon not running - start it immediately rather than waiting
		// for the next daemon heartbeat cycle (up to 3 minutes away).
		fmt.Println("Deacon session missing - starting Deacon")
		if townRoot != "" {
			mgr := deacon.NewManager(townRoot)
			if err := mgr.Start(""); err != nil && err != deacon.ErrAlreadyRunning {
				fmt.Printf("Failed to start Deacon: %v\n", err)
				return "error", "deacon-start-failed", fmt.Errorf("starting deacon: %w", err)
			}
			return "start", "deacon-restarted", nil
		}
		return "error", "deacon-missing", fmt.Errorf("cannot find town root to start deacon")
	}

	// Deacon session exists — in degraded mode, that's all we can check
	// mechanically. The Boot agent handles deeper health assessment.
	return "nothing", "", nil
}

// executeWarrants scans the warrants directory and executes any pending warrants.
// It is called as a side effect during degraded triage, before the normal
// Deacon health decision is made. Errors are non-fatal: a failed execution is
// logged and skipped rather than aborting triage.
func executeWarrants(warrantDir string, tm *tmux.Tmux) {
	entries, err := os.ReadDir(warrantDir)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("Warning: reading warrants dir: %v\n", err)
		}
		return
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".warrant.json") {
			continue
		}

		path := filepath.Join(warrantDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Printf("Warning: reading warrant file %s: %v\n", entry.Name(), err)
			continue
		}

		var w Warrant
		if err := json.Unmarshal(data, &w); err != nil {
			fmt.Printf("Warning: parsing warrant file %s: %v\n", entry.Name(), err)
			continue
		}

		if w.Executed {
			continue
		}

		if err := executeOneWarrant(&w, path, tm); err != nil {
			fmt.Printf("Warning: executing warrant for %s: %v\n", w.Target, err)
			continue
		}
	}
}

// formatDurationAgo formats a duration for human display.
func formatDurationAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 min"
		}
		return fmt.Sprintf("%d min", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	}
}
