package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/estop"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/nudge"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

func runMailCheck(cmd *cobra.Command, args []string) error {
	// Determine which inbox (priority: --identity flag, auto-detect)
	address := ""
	if mailCheckIdentity != "" {
		address = mailCheckIdentity
	} else {
		address = detectSender()
	}

	// All mail uses town beads (two-level architecture)
	workDir, err := findMailWorkDir()
	if err != nil {
		if mailCheckInject {
			fmt.Fprintf(os.Stderr, "gt mail check: workspace lookup failed: %v\n", err)
			return nil
		}
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Get mailbox
	router := mail.NewRouter(workDir)
	mailbox, err := router.GetMailbox(address)
	if err != nil {
		if mailCheckInject {
			fmt.Fprintf(os.Stderr, "gt mail check: mailbox error for %s: %v\n", address, err)
			return nil
		}
		return fmt.Errorf("getting mailbox: %w", err)
	}

	// Count unread
	_, unread, err := mailbox.Count()
	if err != nil {
		if mailCheckInject {
			fmt.Fprintf(os.Stderr, "gt mail check: count error for %s: %v\n", address, err)
			return nil
		}
		return fmt.Errorf("counting messages: %w", err)
	}

	// JSON output
	if mailCheckJSON {
		result := map[string]interface{}{
			"address": address,
			"unread":  unread,
			"has_new": unread > 0,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	// Inject mode: notify agent of mail with priority-appropriate framing.
	// Three tiers: urgent interrupts immediately, high-priority is processed
	// at the next task boundary, normal/low is informational but still
	// checked before going idle (prevents mail from sitting unread).
	if mailCheckInject {
		// Agent-side E-stop check (defense-in-depth).
		// If an E-stop is active (town-wide or per-rig), inject a system reminder
		// telling the agent to checkpoint and wait. This catches agents that
		// survived the SIGTSTP freeze.
		if townRoot, twErr := workspace.FindFromCwd(); twErr == nil {
			rigName := os.Getenv("GT_RIG")
			if estop.IsActive(townRoot) || (rigName != "" && estop.IsRigActive(townRoot, rigName)) {
				// Read the ESTOP info to surface the reason
				var info *estop.Info
				if estop.IsActive(townRoot) {
					info = estop.Read(townRoot)
				} else if rigName != "" {
					info = estop.ReadRig(townRoot, rigName)
				}
				fmt.Print("<system-reminder>\n")
				fmt.Print("EMERGENCY STOP ACTIVE. All work is paused.\n")
				if info != nil && info.Reason != "" {
					fmt.Printf("Reason: %s\n", info.Reason)
				}
				fmt.Print("Do NOT start new tasks or tool calls. Checkpoint your current state\n")
				fmt.Print("(save progress notes) and wait for the overseer to run 'gt thaw'.\n")
				fmt.Print("This is a system-level pause — it may be due to infrastructure failure,\n")
				fmt.Print("maintenance, or the operator traveling.\n")
				fmt.Print("</system-reminder>\n")
			}
		}

		sessionName := tmux.CurrentSessionName()

		if unread > 0 {
			messages, listErr := mailbox.ListUnread()
			if listErr != nil {
				fmt.Fprintf(os.Stderr, "gt mail check: could not list unread for %s: %v\n", address, listErr)
				return nil
			}
			// Filter out messages already injected this session to avoid
			// re-notifying the agent about the same unread mail every turn.
			messages = filterAlreadyInjected(workDir, sessionName, messages)
			if len(messages) > 0 {
				fmt.Print(formatInjectOutput(messages))
				// Ack after output so message is delivered before being marked acked.
				if ackErr := mailbox.AcknowledgeDeliveries(address, messages); ackErr != nil {
					fmt.Fprintf(os.Stderr, "gt mail check: delivery ack update failed for %s: %v\n", address, ackErr)
				}
				recordInjected(workDir, sessionName, messages)
			}
		}

		// Also drain queued nudges (from --mode=queue or --mode=wait-idle fallback).
		if sessionName != "" {
			queuedNudges, drainErr := nudge.Drain(workDir, sessionName)
			if drainErr != nil {
				fmt.Fprintf(os.Stderr, "gt mail check: nudge queue drain error: %v\n", drainErr)
			} else if len(queuedNudges) > 0 {
				fmt.Print(nudge.FormatForInjection(queuedNudges))
			}
		}

		return nil
	}

	// Normal mode
	if unread > 0 {
		fmt.Printf("%s %d unread message(s)\n", style.Bold.Render("📬"), unread)
		return NewSilentExit(0)
	}
	fmt.Println("No new mail")
	return NewSilentExit(1)
}

// formatInjectOutput builds the system-reminder text for inject mode.
// It separates messages into three tiers (urgent, high, normal/low) and
// formats them with priority-appropriate framing for the agent.
func formatInjectOutput(messages []*mail.Message) string {
	var urgent, high, normal []*mail.Message
	for _, msg := range messages {
		switch msg.Priority {
		case mail.PriorityUrgent:
			urgent = append(urgent, msg)
		case mail.PriorityHigh:
			high = append(high, msg)
		default:
			normal = append(normal, msg)
		}
	}

	var b strings.Builder

	if len(urgent) > 0 {
		// Urgent mail: interrupt — agent should stop and read.
		b.WriteString("<system-reminder>\n")
		fmt.Fprintf(&b, "URGENT: %d urgent message(s) require immediate attention.\n\n", len(urgent))
		for _, msg := range urgent {
			fmt.Fprintf(&b, "- %s from %s: %s\n", msg.ID, msg.From, msg.Subject)
		}
		// Show high-priority messages separately so their "process before idle"
		// framing is preserved even when urgent messages are present.
		if len(high) > 0 {
			fmt.Fprintf(&b, "\nAlso %d high-priority message(s) — process before going idle:\n", len(high))
			for _, msg := range high {
				fmt.Fprintf(&b, "- %s from %s: %s\n", msg.ID, msg.From, msg.Subject)
			}
		}
		if len(normal) > 0 {
			fmt.Fprintf(&b, "\n(Plus %d additional message(s) — check after current task.)\n", len(normal))
		}
		b.WriteString("\nRun 'gt mail read <id>' to read urgent messages.\n")
		b.WriteString("</system-reminder>\n")
	} else if len(high) > 0 {
		// High-priority mail: don't interrupt, but process promptly at task boundary.
		b.WriteString("<system-reminder>\n")
		fmt.Fprintf(&b, "You have %d high-priority message(s) in your inbox.\n\n", len(high))
		for _, msg := range high {
			fmt.Fprintf(&b, "- %s from %s: %s\n", msg.ID, msg.From, msg.Subject)
		}
		if len(normal) > 0 {
			fmt.Fprintf(&b, "\n(Plus %d additional message(s).)\n", len(normal))
		}
		b.WriteString("\nContinue your current task. When it completes, process these messages\n")
		b.WriteString("before going idle: 'gt mail inbox'\n")
		b.WriteString("</system-reminder>\n")
	} else {
		// Normal/low mail: informational, process at next task boundary.
		b.WriteString("<system-reminder>\n")
		fmt.Fprintf(&b, "You have %d unread message(s) in your inbox.\n\n", len(normal))
		for _, msg := range normal {
			fmt.Fprintf(&b, "- %s from %s: %s\n", msg.ID, msg.From, msg.Subject)
		}
		b.WriteString("\nContinue your current task. When it completes, check these messages\n")
		b.WriteString("before going idle: 'gt mail inbox'\n")
		b.WriteString("</system-reminder>\n")
	}

	return b.String()
}

// injectedFile returns the path to the per-session file tracking which mail IDs
// have already been injected. Stored in .runtime/mail_injected/<session>.
func injectedFile(townRoot, session string) string {
	safe := strings.ReplaceAll(session, "/", "_")
	return filepath.Join(townRoot, constants.DirRuntime, "mail_injected", safe)
}

// filterAlreadyInjected removes messages that were already injected in this
// session, so agents aren't re-notified about the same unread mail every turn.
func filterAlreadyInjected(townRoot, session string, messages []*mail.Message) []*mail.Message {
	if session == "" {
		return messages
	}
	data, err := os.ReadFile(injectedFile(townRoot, session))
	if err != nil {
		return messages
	}
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		if id := strings.TrimSpace(line); id != "" {
			seen[id] = true
		}
	}
	var fresh []*mail.Message
	for _, msg := range messages {
		if !seen[msg.ID] {
			fresh = append(fresh, msg)
		}
	}
	return fresh
}

// recordInjected appends the given message IDs to the session's injected file.
func recordInjected(townRoot, session string, messages []*mail.Message) {
	if session == "" || len(messages) == 0 {
		return
	}
	path := injectedFile(townRoot, session)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	var ids []string
	for _, msg := range messages {
		ids = append(ids, msg.ID)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(strings.Join(ids, "\n") + "\n")
}
