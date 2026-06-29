package cmd

import (
	"fmt"
	"sort"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
)

// MQ next command flags
var (
	mqNextStrategy string // "priority" (default) or "fifo"
	mqNextJSON     bool
	mqNextQuiet    bool
)

var mqNextCmd = &cobra.Command{
	Use:   "next <rig>",
	Short: "Show the highest-priority merge request",
	Long: `Show the next merge request to process based on priority score.

The priority scoring function considers:
  - Convoy age: Older convoys get higher priority (starvation prevention)
  - Issue priority: P0 > P1 > P2 > P3 > P4
  - Retry count: MRs that fail repeatedly get deprioritized
  - MR age: FIFO tiebreaker for same priority/convoy

Use --strategy=fifo for first-in-first-out ordering instead.

Examples:
  gt mq next gastown                    # Show highest-priority MR
  gt mq next gastown --strategy=fifo    # Show oldest MR instead
  gt mq next gastown --quiet            # Just print the MR ID
  gt mq next gastown --json             # Output as JSON`,
	Args: cobra.ExactArgs(1),
	RunE: runMQNext,
}

func init() {
	mqNextCmd.Flags().StringVar(&mqNextStrategy, "strategy", "priority", "Ordering strategy: 'priority' or 'fifo'")
	mqNextCmd.Flags().BoolVar(&mqNextJSON, "json", false, "Output as JSON")
	mqNextCmd.Flags().BoolVarP(&mqNextQuiet, "quiet", "q", false, "Just print the MR ID")

	mqCmd.AddCommand(mqNextCmd)
}

func runMQNext(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	_, r, _, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	// Create beads wrapper for the rig
	b := beads.New(r.BeadsPath())

	// Query for open merge-requests (ready to process).
	// Use ListMergeRequests to query both issues and wisps tables,
	// since MRs are created as ephemeral (wisps) by gt mq submit (GH#2446).
	opts := beads.ListOptions{
		Label:    "gt:merge-request",
		Status:   "open",
		Priority: -1, // No priority filter
		Rig:      rigName,
	}

	issues, err := b.ListMergeRequests(opts)
	if err != nil {
		return fmt.Errorf("querying merge queue: %w", err)
	}

	// Filter to only ready MRs (no blockers)
	var ready []*beads.Issue
	for _, issue := range issues {
		if isMergeRequestReadyForSelection(issue) {
			ready = append(ready, issue)
		}
	}

	if len(ready) == 0 {
		if mqNextQuiet {
			return nil // Silent exit
		}
		fmt.Printf("%s No ready merge requests in queue\n", style.Dim.Render("ℹ"))
		return nil
	}

	now := time.Now()

	// Sort based on strategy
	if mqNextStrategy == "fifo" {
		// FIFO: oldest first by creation time
		sort.Slice(ready, func(i, j int) bool {
			ti, _ := time.Parse(time.RFC3339, ready[i].CreatedAt)
			tj, _ := time.Parse(time.RFC3339, ready[j].CreatedAt)
			return ti.Before(tj)
		})
	} else {
		// Priority: highest score first
		type scoredIssue struct {
			issue *beads.Issue
			score float64
		}
		scored := make([]scoredIssue, len(ready))
		for i, issue := range ready {
			fields := beads.ParseMRFields(issue)
			score := calculateMRScore(issue, fields, now)
			scored[i] = scoredIssue{issue: issue, score: score}
		}

		sort.Slice(scored, func(i, j int) bool {
			return scored[i].score > scored[j].score
		})

		// Rebuild ready slice in sorted order
		for i, s := range scored {
			ready[i] = s.issue
		}
	}

	// Get the top MR
	next := ready[0]
	fields := beads.ParseMRFields(next)

	// Output based on format flags
	if mqNextQuiet {
		fmt.Println(next.ID)
		return nil
	}

	if mqNextJSON {
		return outputJSON(next)
	}

	// Human-readable output
	fmt.Printf("%s Next MR to process:\n\n", style.Bold.Render("🎯"))

	score := calculateMRScore(next, fields, now)

	fmt.Printf("  ID:       %s\n", next.ID)
	fmt.Printf("  Score:    %.1f\n", score)
	fmt.Printf("  Priority: P%d\n", next.Priority)

	if fields != nil {
		if fields.Branch != "" {
			fmt.Printf("  Branch:   %s\n", fields.Branch)
		}
		if fields.Worker != "" {
			fmt.Printf("  Worker:   %s\n", fields.Worker)
		}
		if fields.ConvoyID != "" {
			fmt.Printf("  Convoy:   %s\n", fields.ConvoyID)
		}
		if fields.RetryCount > 0 {
			fmt.Printf("  Retries:  %d\n", fields.RetryCount)
		}
	}

	fmt.Printf("  Age:      %s\n", formatMRAge(next.CreatedAt))

	if len(ready) > 1 {
		fmt.Printf("\n  %s\n", style.Dim.Render(fmt.Sprintf("(%d more in queue)", len(ready)-1)))
	}

	return nil
}
