package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/mail"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/spf13/cobra"
)

var (
	mailDrainMaxAge   string
	mailDrainDryRun   bool
	mailDrainIdentity string
	mailDrainAll      bool // Archive all protocol messages regardless of age
)

var mailDrainCmd = &cobra.Command{
	Use:   "drain",
	Short: "Bulk-archive stale protocol messages",
	Long: `Bulk-archive stale protocol and lifecycle messages from an inbox.

Drains messages matching common protocol patterns that accumulate in
agent inboxes (especially witness). These are messages that have been
processed or are no longer actionable.

DRAINABLE MESSAGE TYPES:
  POLECAT_DONE       Polecat completion notifications
  POLECAT_STARTED    Polecat startup notifications
  LIFECYCLE:*        Lifecycle events (shutdown, etc.)
  MERGED             Merge confirmations
  MERGE_READY        Merge ready notifications
  MERGE_FAILED       Merge failure notifications
  SWARM_START        Swarm initiation messages

NON-DRAINABLE (preserved):
  HELP:*             Help requests (need human attention)
  HANDOFF            Session handoff context

By default, only archives protocol messages older than 30 minutes.
Use --max-age to change the threshold, or --all to drain regardless of age.

Examples:
  gt mail drain                              # Drain own inbox (30m default)
  gt mail drain --identity gastown/witness   # Drain witness inbox
  gt mail drain --max-age 1h                 # Only drain messages >1h old
  gt mail drain --all                        # Drain all protocol messages
  gt mail drain --dry-run                    # Preview what would be drained`,
	RunE: runMailDrain,
}

func init() {
	mailDrainCmd.Flags().StringVar(&mailDrainMaxAge, "max-age", "30m", "Only drain messages older than this duration (e.g., 30m, 1h, 2h)")
	mailDrainCmd.Flags().BoolVarP(&mailDrainDryRun, "dry-run", "n", false, "Show what would be drained without archiving")
	mailDrainCmd.Flags().StringVar(&mailDrainIdentity, "identity", "", "Target inbox identity (e.g., gastown/witness)")
	mailDrainCmd.Flags().BoolVar(&mailDrainAll, "all", false, "Drain all protocol messages regardless of age")
}

// drainableSubjects are protocol message subject prefixes that are safe to
// bulk-archive. These are routine notifications that don't require individual
// attention once the information is stale.
var drainableSubjects = []string{
	"CRASHED_POLECAT",
	"POLECAT_DONE",
	"POLECAT_STARTED",
	"LIFECYCLE:",
	"MERGED",
	"MERGE_READY",
	"MERGE_FAILED",
	"SWARM_START",
}

// isDrainableMessage checks if a message subject matches a drainable protocol pattern.
func isDrainableMessage(subject string) bool {
	for _, prefix := range drainableSubjects {
		if strings.HasPrefix(subject, prefix) {
			return true
		}
	}
	return false
}

func runMailDrain(cmd *cobra.Command, args []string) error {
	// Parse max-age duration
	maxAge, err := time.ParseDuration(mailDrainMaxAge)
	if err != nil {
		return fmt.Errorf("invalid --max-age %q: %w", mailDrainMaxAge, err)
	}

	// Determine which inbox
	address := mailDrainIdentity
	if address == "" {
		address = detectSender()
	}

	mailbox, err := getMailbox(address)
	if err != nil {
		return err
	}

	// List all messages
	messages, err := mailbox.List()
	if err != nil {
		return fmt.Errorf("listing messages: %w", err)
	}

	if len(messages) == 0 {
		fmt.Printf("%s Inbox %s is empty, nothing to drain\n", style.Success.Render("✓"), address)
		return nil
	}

	// Find drainable messages
	cutoff := time.Now().Add(-maxAge)
	type drainCandidate struct {
		Message *mail.Message
		Reason  string
	}
	var candidates []drainCandidate

	for _, msg := range messages {
		if !isDrainableMessage(msg.Subject) {
			continue
		}

		// Check age unless --all
		if !mailDrainAll && msg.Timestamp.After(cutoff) {
			continue
		}

		reason := "protocol"
		if msg.Wisp {
			reason = "wisp+protocol"
		}
		candidates = append(candidates, drainCandidate{Message: msg, Reason: reason})
	}

	// Also drain read wisps (non-protocol) if they're old enough
	for _, msg := range messages {
		if isDrainableMessage(msg.Subject) {
			continue // already handled above
		}
		if msg.Wisp && msg.Read && (mailDrainAll || msg.Timestamp.Before(cutoff)) {
			candidates = append(candidates, drainCandidate{Message: msg, Reason: "read-wisp"})
		}
	}

	if len(candidates) == 0 {
		fmt.Printf("%s No drainable messages in %s (%d messages total)\n",
			style.Success.Render("✓"), address, len(messages))
		return nil
	}

	// Dry run mode
	if mailDrainDryRun {
		fmt.Printf("%s Would drain %d/%d messages from %s:\n",
			style.Dim.Render("(dry-run)"), len(candidates), len(messages), address)
		for _, c := range candidates {
			age := time.Since(c.Message.Timestamp).Truncate(time.Minute)
			fmt.Printf("  %s %s [%s] (age: %s)\n",
				style.Dim.Render(c.Message.ID), c.Message.Subject, c.Reason, age)
		}
		return nil
	}

	// Archive drainable messages
	archived := 0
	var archiveErrors []string
	for _, c := range candidates {
		if err := mailbox.Delete(c.Message.ID); err != nil {
			archiveErrors = append(archiveErrors, fmt.Sprintf("%s: %v", c.Message.ID, err))
		} else {
			archived++
		}
	}

	remaining := len(messages) - archived

	if len(archiveErrors) > 0 {
		fmt.Printf("%s Drained %d/%d messages from %s (%d remaining, %d errors)\n",
			style.Bold.Render("⚠"), archived, len(candidates), address, remaining, len(archiveErrors))
		for _, e := range archiveErrors {
			fmt.Printf("  Error: %s\n", e)
		}
		return fmt.Errorf("failed to drain %d messages", len(archiveErrors))
	}

	fmt.Printf("%s Drained %d messages from %s (%d remaining)\n",
		style.Bold.Render("✓"), archived, address, remaining)

	// Summarize what was drained by type
	typeCounts := make(map[string]int)
	for _, c := range candidates {
		typeCounts[c.Reason]++
	}
	for reason, count := range typeCounts {
		fmt.Printf("  %s: %d\n", reason, count)
	}

	return nil
}
