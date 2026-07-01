package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/util"
)

var (
	cleanupDryRun bool
	cleanupForce  bool
)

var cleanupCmd = &cobra.Command{
	Use:     "cleanup",
	GroupID: GroupWork,
	Short:   "Clean up orphaned agent processes",
	Long: `Clean up orphaned agent processes that survived session termination.

This command finds and kills agent processes that are not associated with
any active Gas Town tmux session. These orphans can accumulate when:
- Polecat sessions are killed without proper cleanup
- Agent runtimes spawn subprocesses that outlive their parent
- Network or system issues interrupt normal shutdown

Uses aggressive tmux session verification to detect ALL orphaned processes,
not just those with PPID=1.

Examples:
  gt cleanup              # Clean up orphans with confirmation
  gt cleanup --dry-run    # Show what would be killed
  gt cleanup --force      # Kill without confirmation`,
	RunE: runCleanup,
}

func init() {
	cleanupCmd.Flags().BoolVar(&cleanupDryRun, "dry-run", false, "Show what would be killed without killing")
	cleanupCmd.Flags().BoolVarP(&cleanupForce, "force", "f", false, "Kill without confirmation")

	rootCmd.AddCommand(cleanupCmd)
}

func runCleanup(cmd *cobra.Command, args []string) error {
	// Find orphaned processes using aggressive zombie detection
	zombies, err := util.FindZombieClaudeProcesses()
	if err != nil {
		return fmt.Errorf("finding orphaned processes: %w", err)
	}

	if len(zombies) == 0 {
		fmt.Printf("%s No orphaned agent processes found\n", style.Bold.Render("✓"))
		return nil
	}

	// Show what we found
	fmt.Printf("%s Found %d orphaned agent process(es):\n\n", style.Warning.Render("⚠"), len(zombies))
	for _, z := range zombies {
		ageStr := formatProcessAgeCleanup(z.Age)
		fmt.Printf("  %s %s (age: %s, tty: %s)\n",
			style.Bold.Render(fmt.Sprintf("PID %d", z.PID)),
			z.Cmd,
			style.Dim.Render(ageStr),
			z.TTY)
	}
	fmt.Println()

	if cleanupDryRun {
		fmt.Printf("%s Dry run - no processes killed\n", style.Dim.Render("ℹ"))
		return nil
	}

	// Confirm unless --force
	if !cleanupForce {
		fmt.Printf("Kill these %d process(es)? [y/N] ", len(zombies))
		var response string
		_, _ = fmt.Scanln(&response)
		if response != "y" && response != "Y" && response != "yes" && response != "Yes" {
			fmt.Println("Aborted")
			return nil
		}
	}

	// Kill the processes using the standard cleanup function
	results, err := util.CleanupZombieClaudeProcesses()
	if err != nil {
		return fmt.Errorf("cleaning up processes: %w", err)
	}

	// Report results
	var killed, escalated int
	for _, r := range results {
		switch r.Signal {
		case "SIGTERM":
			fmt.Printf("  %s PID %d sent SIGTERM\n", style.Success.Render("✓"), r.Process.PID)
			killed++
		case "SIGKILL":
			fmt.Printf("  %s PID %d sent SIGKILL (didn't respond to SIGTERM)\n", style.Warning.Render("⚠"), r.Process.PID)
			killed++
		case "UNKILLABLE":
			fmt.Printf("  %s PID %d survived SIGKILL\n", style.Error.Render("✗"), r.Process.PID)
			escalated++
		}
	}

	fmt.Printf("\n%s Cleaned up %d process(es)", style.Bold.Render("✓"), killed)
	if escalated > 0 {
		fmt.Printf(", %d unkillable", escalated)
	}
	fmt.Println()

	return nil
}

// formatProcessAgeCleanup formats seconds into a human-readable age string
func formatProcessAgeCleanup(seconds int) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm%ds", seconds/60, seconds%60)
	}
	hours := seconds / 3600
	mins := (seconds % 3600) / 60
	return fmt.Sprintf("%dh%dm", hours, mins)
}
