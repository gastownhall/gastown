package cmd

import (
	"fmt"
	"os"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/spf13/cobra"
)

var releaseReason string

var releaseCmd = &cobra.Command{
	Use:     "release <issue-id>...",
	GroupID: GroupWork,
	Short:   "Release stuck in_progress issues back to pending",
	Long: `Release one or more in_progress issues back to open/pending status.

This is used to recover stuck steps when a worker dies mid-task.
The issue is moved to "open" status and the assignee is cleared,
allowing another worker to claim and complete it.

Examples:
  gt release gt-abc           # Release single issue
  gt release gt-abc gt-def    # Release multiple issues
  gt release gt-abc -r "worker died"  # Release with reason

This implements nondeterministic idempotence - work can be safely
retried by releasing and reclaiming stuck steps.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runRelease,
}

func init() {
	releaseCmd.Flags().StringVarP(&releaseReason, "reason", "r", "", "Reason for releasing (added as note)")
	rootCmd.AddCommand(releaseCmd)
}

func runRelease(cmd *cobra.Command, args []string) error {
	// Get working directory for beads
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	bd := beads.New(cwd)

	// Release each issue
	var released, failed int
	for _, id := range args {
		var err error
		if releaseReason != "" {
			err = bd.ReleaseWithReason(id, releaseReason)
		} else {
			err = bd.Release(id)
		}

		if err != nil {
			fmt.Printf("%s Failed to release %s: %v\n", style.Dim.Render("✗"), id, err)
			failed++
		} else {
			fmt.Printf("%s Released %s → open\n", style.Bold.Render("✓"), id)
			released++
		}
	}

	// Summary if multiple
	if len(args) > 1 {
		fmt.Printf("\nReleased: %d, Failed: %d\n", released, failed)
	}

	if failed > 0 {
		return fmt.Errorf("%d issue(s) failed to release", failed)
	}

	return nil
}
