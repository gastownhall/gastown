//go:build !windows

package cmd

import (
	"fmt"
	"syscall"
	"time"

	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/util"
)

// cleanupOrphanedClaude finds and kills orphaned agent processes with a grace period.
// This is a simpler synchronous implementation that:
// 1. Finds orphaned processes (TTY-less, older than 60s, not in Gas Town sessions)
// 2. Sends SIGTERM to all of them
// 3. Waits for the grace period
// 4. Sends SIGKILL to any that are still alive
func cleanupOrphanedClaude(graceSecs int) {
	// Find orphaned processes
	orphans, err := util.FindOrphanedClaudeProcesses()
	if err != nil {
		fmt.Printf("  %s Warning: %v\n", style.Bold.Render("⚠"), err)
		return
	}

	if len(orphans) == 0 {
		fmt.Printf("  %s No orphaned processes found\n", style.Dim.Render("○"))
		return
	}

	// Send SIGTERM to all orphans
	var termPIDs []int
	for _, orphan := range orphans {
		if err := syscall.Kill(orphan.PID, syscall.SIGTERM); err != nil {
			if err != syscall.ESRCH {
				fmt.Printf("  %s PID %d: failed to send SIGTERM: %v\n",
					style.Bold.Render("⚠"), orphan.PID, err)
			}
			continue
		}
		termPIDs = append(termPIDs, orphan.PID)
		fmt.Printf("  %s PID %d: sent SIGTERM (waiting %ds before SIGKILL)\n",
			style.Bold.Render("→"), orphan.PID, graceSecs)
	}

	if len(termPIDs) == 0 {
		return
	}

	// Wait for grace period
	fmt.Printf("  %s Waiting %d seconds for processes to terminate gracefully...\n",
		style.Dim.Render("⏳"), graceSecs)
	time.Sleep(time.Duration(graceSecs) * time.Second)

	// Check which processes are still alive and send SIGKILL
	var killedCount, alreadyDeadCount int
	for _, pid := range termPIDs {
		// Check if process still exists
		if err := syscall.Kill(pid, 0); err != nil {
			// Process is gone (either died from SIGTERM or doesn't exist)
			alreadyDeadCount++
			continue
		}

		// Process still alive - send SIGKILL
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			if err != syscall.ESRCH {
				fmt.Printf("  %s PID %d: failed to send SIGKILL: %v\n",
					style.Bold.Render("⚠"), pid, err)
			}
			continue
		}
		killedCount++
		fmt.Printf("  %s PID %d: sent SIGKILL (did not respond to SIGTERM)\n",
			style.Bold.Render("✓"), pid)
	}

	if alreadyDeadCount > 0 {
		fmt.Printf("  %s %d process(es) terminated gracefully from SIGTERM\n",
			style.Bold.Render("✓"), alreadyDeadCount)
	}
	if killedCount == 0 && alreadyDeadCount > 0 {
		fmt.Printf("  %s All processes cleaned up successfully\n",
			style.Bold.Render("✓"))
	}
}

// verifyNoOrphans checks that no agent processes survived shutdown.
// If any are found, it reports them and attempts a final SIGKILL.
func verifyNoOrphans() {
	orphans, err := util.FindOrphanedClaudeProcesses()
	if err != nil {
		fmt.Printf("  %s Could not verify: %v\n", style.Bold.Render("⚠"), err)
		return
	}

	// Also check for zombie processes (have TTY but no tmux session)
	zombies, zErr := util.FindZombieClaudeProcesses()
	if zErr != nil {
		// Non-fatal, FindOrphanedClaudeProcesses already covered TTY-less ones
		zombies = nil
	}

	totalSurvivors := len(orphans) + len(zombies)
	if totalSurvivors == 0 {
		fmt.Printf("  %s No orphaned agent processes detected\n", style.Bold.Render("✓"))
		return
	}

	fmt.Printf("  %s %d agent process(es) survived shutdown:\n",
		style.Bold.Render("⚠"), totalSurvivors)

	// Kill orphans (TTY-less)
	for _, o := range orphans {
		fmt.Printf("    PID %d (%s, age %ds) - sending SIGKILL\n", o.PID, o.Cmd, o.Age)
		_ = syscall.Kill(o.PID, syscall.SIGKILL)
	}

	// Kill zombies (have TTY but no tmux session)
	for _, z := range zombies {
		fmt.Printf("    PID %d (%s, age %ds, tty %s) - sending SIGKILL\n", z.PID, z.Cmd, z.Age, z.TTY)
		_ = syscall.Kill(z.PID, syscall.SIGKILL)
	}
}
