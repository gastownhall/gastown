package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
)

// getMailbox returns the mailbox for the given address.
func getMailbox(address string) (*mail.Mailbox, error) {
	// All mail uses town beads (two-level architecture)
	workDir, err := findMailWorkDir()
	if err != nil {
		return nil, fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Get mailbox
	router := mail.NewRouter(workDir)
	mailbox, err := router.GetMailbox(address)
	if err != nil {
		return nil, fmt.Errorf("getting mailbox: %w", err)
	}
	return mailbox, nil
}

func runMailInbox(cmd *cobra.Command, args []string) error {
	// Check for mutually exclusive flags
	if mailInboxAll && mailInboxUnread {
		return errors.New("--all and --unread are mutually exclusive")
	}

	// Determine which inbox to check (priority: --identity flag, positional arg, auto-detect)
	address := ""
	if mailInboxIdentity != "" {
		address = mailInboxIdentity
	} else if len(args) > 0 {
		address = args[0]
	} else {
		address = detectSender()
	}

	mailbox, err := getMailbox(address)
	if err != nil {
		return err
	}

	// Load the inbox once. Count() and ListUnread() both call List(), so using
	// them here doubles the bd/Dolt reads on the hot patrol path.
	messages, total, unread, err := loadInboxSnapshot(mailbox, mailInboxUnread)
	if err != nil {
		return fmt.Errorf("listing messages: %w", err)
	}

	// JSON output
	if mailInboxJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(messages); err != nil {
			return err
		}
		return nil
	}

	// Human-readable output
	fmt.Printf("%s Inbox: %s (%d messages, %d unread)\n\n",
		style.Bold.Render("📬"), address, total, unread)

	if len(messages) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(no messages)"))
		return nil
	}

	for i, msg := range messages {
		readMarker := "●"
		if msg.Read {
			readMarker = "○"
		}
		typeMarker := ""
		if msg.Type != "" && msg.Type != mail.TypeNotification {
			typeMarker = fmt.Sprintf(" [%s]", msg.Type)
		}
		priorityMarker := ""
		if msg.Priority == mail.PriorityHigh || msg.Priority == mail.PriorityUrgent {
			priorityMarker = " " + style.Bold.Render("!")
		}
		wispMarker := ""
		if msg.Wisp {
			wispMarker = " " + style.Dim.Render("(wisp)")
		}

		// Show 1-based index for easy reference with 'gt mail read <n>'
		indexStr := style.Dim.Render(fmt.Sprintf("%d.", i+1))
		fmt.Printf("  %s %s %s%s%s%s\n", indexStr, readMarker, msg.Subject, typeMarker, priorityMarker, wispMarker)
		fmt.Printf("      %s from %s\n",
			style.Dim.Render(msg.ID),
			msg.From)
		fmt.Printf("      %s\n",
			style.Dim.Render(msg.Timestamp.Local().Format("2006-01-02 15:04")))
	}

	return nil
}

type inboxLister interface {
	List() ([]*mail.Message, error)
}

func loadInboxSnapshot(mailbox inboxLister, unreadOnly bool) ([]*mail.Message, int, int, error) {
	allMessages, err := mailbox.List()
	if err != nil {
		return nil, 0, 0, err
	}
	if allMessages == nil {
		allMessages = make([]*mail.Message, 0)
	}

	total, unread := countInboxMessages(allMessages)
	if unreadOnly {
		return filterUnreadMessages(allMessages), total, unread, nil
	}
	return allMessages, total, unread, nil
}

func countInboxMessages(messages []*mail.Message) (total, unread int) {
	total = len(messages)
	for _, msg := range messages {
		if msg != nil && !msg.Read {
			unread++
		}
	}
	return total, unread
}

func filterUnreadMessages(messages []*mail.Message) []*mail.Message {
	unreadMessages := make([]*mail.Message, 0)
	for _, msg := range messages {
		if msg != nil && !msg.Read {
			unreadMessages = append(unreadMessages, msg)
		}
	}
	return unreadMessages
}

func runMailRead(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("message ID or index required\n\nRun 'gt mail inbox' to list messages and their IDs")
	}
	msgRef := args[0]

	// Determine which inbox
	address := detectSender()

	mailbox, err := getMailbox(address)
	if err != nil {
		return err
	}

	// Check if the argument is a numeric index (1-based)
	var msgID string
	if idx, err := strconv.Atoi(msgRef); err == nil && idx > 0 {
		// Numeric index: resolve to message ID by listing inbox
		messages, err := mailbox.List()
		if err != nil {
			return fmt.Errorf("listing messages: %w", err)
		}
		if idx > len(messages) {
			return fmt.Errorf("index %d out of range (inbox has %d messages)", idx, len(messages))
		}
		msgID = messages[idx-1].ID
	} else {
		msgID = msgRef
	}

	msg, err := mailbox.Get(msgID)
	if err != nil {
		return fmt.Errorf("getting message: %w", err)
	}

	// Mark as read when viewed (adds "read" label, does not close/archive).
	// Handoff messages are preserved via the hook mechanism, so marking
	// read here is safe — hooked mail is found via gt hook, not the inbox.
	if err := mailbox.MarkReadOnly(msgID); err != nil {
		// Non-fatal: message was retrieved, just couldn't mark
		style.PrintWarning("could not mark message as read: %v", err)
	}

	// JSON output
	if mailReadJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(msg); err != nil {
			return err
		}
		// Ack after output so JSON reflects accurate read-time state.
		if ackErr := mailbox.AcknowledgeDeliveries(address, []*mail.Message{msg}); ackErr != nil {
			fmt.Fprintf(os.Stderr, "gt mail read: delivery ack failed: %v\n", ackErr)
		}
		return nil
	}

	// Human-readable output
	priorityStr := ""
	if msg.Priority == mail.PriorityUrgent {
		priorityStr = " " + style.Bold.Render("[URGENT]")
	} else if msg.Priority == mail.PriorityHigh {
		priorityStr = " " + style.Bold.Render("[HIGH PRIORITY]")
	}

	typeStr := ""
	if msg.Type != "" && msg.Type != mail.TypeNotification {
		typeStr = fmt.Sprintf(" [%s]", msg.Type)
	}

	fmt.Printf("%s %s%s%s\n\n", style.Bold.Render("Subject:"), msg.Subject, typeStr, priorityStr)
	fmt.Printf("From: %s\n", msg.From)
	fmt.Printf("To: %s\n", msg.To)
	fmt.Printf("Date: %s\n", msg.Timestamp.Local().Format("2006-01-02 15:04:05"))
	fmt.Printf("ID: %s\n", style.Dim.Render(msg.ID))

	if msg.ThreadID != "" {
		fmt.Printf("Thread: %s\n", style.Dim.Render(msg.ThreadID))
	}
	if msg.ReplyTo != "" {
		fmt.Printf("Reply-To: %s\n", style.Dim.Render(msg.ReplyTo))
	}

	if msg.Body != "" {
		fmt.Printf("\n%s\n", msg.Body)
	}

	// Ack after output (non-fatal).
	if ackErr := mailbox.AcknowledgeDeliveries(address, []*mail.Message{msg}); ackErr != nil {
		fmt.Fprintf(os.Stderr, "gt mail read: delivery ack failed: %v\n", ackErr)
	}

	return nil
}

func runMailPeek(cmd *cobra.Command, args []string) error {
	// Determine which inbox
	address := detectSender()

	mailbox, err := getMailbox(address)
	if err != nil {
		return NewSilentExit(1) // Silent exit - can't access mailbox
	}

	// Get unread messages
	messages, err := mailbox.ListUnread()
	if err != nil || len(messages) == 0 {
		return NewSilentExit(1) // Silent exit - no unread
	}

	// Show first unread message
	msg := messages[0]

	// Header with priority indicator
	priorityStr := ""
	if msg.Priority == mail.PriorityUrgent {
		priorityStr = " [URGENT]"
	} else if msg.Priority == mail.PriorityHigh {
		priorityStr = " [!]"
	}

	fmt.Printf("📬 %s%s\n", msg.Subject, priorityStr)
	fmt.Printf("From: %s\n", msg.From)
	fmt.Printf("ID: %s\n\n", msg.ID)

	// Body preview (truncate long bodies)
	if msg.Body != "" {
		body := msg.Body
		// Truncate to ~500 chars for popup display
		if len(body) > 500 {
			body = body[:500] + "\n..."
		}
		fmt.Print(body)
		if !strings.HasSuffix(body, "\n") {
			fmt.Println()
		}
	}

	// Show count if more messages
	if len(messages) > 1 {
		fmt.Printf("\n%s\n", style.Dim.Render(fmt.Sprintf("(+%d more unread)", len(messages)-1)))
	}

	return nil
}

func runMailDelete(cmd *cobra.Command, args []string) error {
	// Determine which inbox
	address := detectSender()

	mailbox, err := getMailbox(address)
	if err != nil {
		return err
	}

	// Delete all specified messages
	deleted := 0
	var errors []string
	for _, msgID := range args {
		if err := mailbox.Delete(msgID); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", msgID, err))
		} else {
			deleted++
		}
	}

	// Report results
	if len(errors) > 0 {
		fmt.Printf("%s Deleted %d/%d messages\n",
			style.Bold.Render("⚠"), deleted, len(args))
		for _, e := range errors {
			fmt.Printf("  Error: %s\n", e)
		}
		return fmt.Errorf("failed to delete %d messages", len(errors))
	}

	if len(args) == 1 {
		fmt.Printf("%s Message deleted\n", style.Bold.Render("✓"))
	} else {
		fmt.Printf("%s Deleted %d messages\n", style.Bold.Render("✓"), deleted)
	}
	return nil
}

func runMailArchive(cmd *cobra.Command, args []string) error {
	// Determine which inbox
	address := detectSender()

	mailbox, err := getMailbox(address)
	if err != nil {
		return err
	}

	if mailArchiveStale {
		if len(args) > 0 {
			return errors.New("--stale cannot be combined with message IDs")
		}
		return runMailArchiveStale(mailbox, address)
	}
	if len(args) == 0 {
		return errors.New("message ID required unless using --stale")
	}
	if mailArchiveDryRun {
		fmt.Printf("%s Would archive %d message(s)\n", style.Dim.Render("(dry-run)"), len(args))
		for _, msgID := range args {
			fmt.Printf("  %s\n", style.Dim.Render(msgID))
		}
		return nil
	}

	// Archive all specified messages.
	//
	// Archive is a mail cleanup operation, not a bead operation. If the
	// underlying bead has been garbage collected (by `bd mol wisp gc` or
	// `bd compact`), the mail entry is effectively already gone — we
	// treat ErrMessageNotFound as success so orphaned inbox references
	// can be cleared without manual surgery. See aa-6hv.
	archived := 0
	gcd := 0
	var errMsgs []string
	for _, msgID := range args {
		err := mailbox.Delete(msgID)
		switch {
		case err == nil:
			archived++
		case errors.Is(err, mail.ErrMessageNotFound):
			gcd++
			fmt.Printf("  %s %s: underlying bead already gone (GC'd), entry cleared\n",
				style.Dim.Render("note"), msgID)
		default:
			errMsgs = append(errMsgs, fmt.Sprintf("%s: %v", msgID, err))
		}
	}

	// Report results
	if len(errMsgs) > 0 {
		fmt.Printf("%s Archived %d/%d messages\n",
			style.Bold.Render("⚠"), archived+gcd, len(args))
		for _, e := range errMsgs {
			fmt.Printf("  Error: %s\n", e)
		}
		return fmt.Errorf("failed to archive %d messages", len(errMsgs))
	}

	total := archived + gcd
	if total == 1 {
		fmt.Printf("%s Message archived\n", style.Bold.Render("✓"))
	} else {
		fmt.Printf("%s Archived %d messages\n", style.Bold.Render("✓"), total)
	}
	return nil
}

type staleMessage struct {
	Message *mail.Message
	Reason  string
}

func runMailArchiveStale(mailbox *mail.Mailbox, address string) error {
	identity, err := session.ParseAddress(address)
	if err != nil {
		return fmt.Errorf("determining session for %s: %w", address, err)
	}

	sessionName := identity.SessionName()
	if sessionName == "" {
		return fmt.Errorf("could not determine session name for %s", address)
	}

	sessionStart, err := session.SessionCreatedAt(sessionName)
	if err != nil {
		return fmt.Errorf("getting session start time for %s: %w", sessionName, err)
	}

	messages, err := mailbox.List()
	if err != nil {
		return fmt.Errorf("listing messages: %w", err)
	}

	staleMessages := staleMessagesForSession(messages, sessionStart)
	if mailArchiveDryRun {
		if len(staleMessages) == 0 {
			fmt.Printf("%s No stale messages found\n", style.Success.Render("✓"))
			return nil
		}
		fmt.Printf("%s Would archive %d stale message(s):\n", style.Dim.Render("(dry-run)"), len(staleMessages))
		for _, stale := range staleMessages {
			fmt.Printf("  %s %s\n", style.Dim.Render(stale.Message.ID), stale.Message.Subject)
		}
		return nil
	}

	if len(staleMessages) == 0 {
		fmt.Printf("%s No stale messages to archive\n", style.Success.Render("✓"))
		return nil
	}

	// GC'd beads (see aa-6hv): if the underlying bead was removed by
	// `bd mol wisp gc` or `bd compact`, the close call returns
	// ErrMessageNotFound. That's a success for archive: the mail entry
	// is already effectively gone.
	archived := 0
	gcd := 0
	var errMsgs []string
	for _, stale := range staleMessages {
		err := mailbox.Delete(stale.Message.ID)
		switch {
		case err == nil:
			archived++
		case errors.Is(err, mail.ErrMessageNotFound):
			gcd++
			fmt.Printf("  %s %s: underlying bead already gone (GC'd), entry cleared\n",
				style.Dim.Render("note"), stale.Message.ID)
		default:
			errMsgs = append(errMsgs, fmt.Sprintf("%s: %v", stale.Message.ID, err))
		}
	}

	if len(errMsgs) > 0 {
		fmt.Printf("%s Archived %d/%d stale messages\n", style.Bold.Render("⚠"), archived+gcd, len(staleMessages))
		for _, e := range errMsgs {
			fmt.Printf("  Error: %s\n", e)
		}
		return fmt.Errorf("failed to archive %d stale messages", len(errMsgs))
	}

	total := archived + gcd
	if total == 1 {
		fmt.Printf("%s Stale message archived\n", style.Bold.Render("✓"))
	} else {
		fmt.Printf("%s Archived %d stale messages\n", style.Bold.Render("✓"), total)
	}
	return nil
}

func staleMessagesForSession(messages []*mail.Message, sessionStart time.Time) []staleMessage {
	var staleMessages []staleMessage
	for _, msg := range messages {
		stale, reason := session.StaleReasonForTimes(msg.Timestamp, sessionStart)
		if stale {
			staleMessages = append(staleMessages, staleMessage{Message: msg, Reason: reason})
		}
	}
	return staleMessages
}

func runMailMarkRead(cmd *cobra.Command, args []string) error {
	// Determine which inbox
	address := detectSender()

	mailbox, err := getMailbox(address)
	if err != nil {
		return err
	}

	// --all: mark all unread messages as read
	if mailMarkReadAll {
		if len(args) > 0 {
			return fmt.Errorf("--all cannot be combined with explicit message IDs")
		}
		messages, err := mailbox.ListUnread()
		if err != nil {
			return fmt.Errorf("listing unread messages: %w", err)
		}
		if len(messages) == 0 {
			fmt.Printf("%s No unread messages\n", style.Bold.Render("✓"))
			return nil
		}
		marked := 0
		for _, msg := range messages {
			if err := mailbox.MarkReadOnly(msg.ID); err != nil {
				style.PrintWarning("could not mark %s as read: %v", msg.ID, err)
			} else {
				marked++
			}
		}
		fmt.Printf("%s Marked %d messages as read\n", style.Bold.Render("✓"), marked)
		return nil
	}

	if len(args) == 0 {
		return fmt.Errorf("message ID required (or use --all to mark all as read)")
	}

	// Mark all specified messages as read
	marked := 0
	var errors []string
	for _, msgID := range args {
		if err := mailbox.MarkReadOnly(msgID); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", msgID, err))
		} else {
			marked++
		}
	}

	// Report results
	if len(errors) > 0 {
		fmt.Printf("%s Marked %d/%d messages as read\n",
			style.Bold.Render("⚠"), marked, len(args))
		for _, e := range errors {
			fmt.Printf("  Error: %s\n", e)
		}
		return fmt.Errorf("failed to mark %d messages", len(errors))
	}

	if len(args) == 1 {
		fmt.Printf("%s Message marked as read\n", style.Bold.Render("✓"))
	} else {
		fmt.Printf("%s Marked %d messages as read\n", style.Bold.Render("✓"), marked)
	}
	return nil
}

func runMailMarkUnread(cmd *cobra.Command, args []string) error {
	// Determine which inbox
	address := detectSender()

	mailbox, err := getMailbox(address)
	if err != nil {
		return err
	}

	// Mark all specified messages as unread
	marked := 0
	var errors []string
	for _, msgID := range args {
		if err := mailbox.MarkUnreadOnly(msgID); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", msgID, err))
		} else {
			marked++
		}
	}

	// Report results
	if len(errors) > 0 {
		fmt.Printf("%s Marked %d/%d messages as unread\n",
			style.Bold.Render("⚠"), marked, len(args))
		for _, e := range errors {
			fmt.Printf("  Error: %s\n", e)
		}
		return fmt.Errorf("failed to mark %d messages", len(errors))
	}

	if len(args) == 1 {
		fmt.Printf("%s Message marked as unread\n", style.Bold.Render("✓"))
	} else {
		fmt.Printf("%s Marked %d messages as unread\n", style.Bold.Render("✓"), marked)
	}
	return nil
}

func runMailClear(cmd *cobra.Command, args []string) error {
	// Determine which inbox to clear (target arg or auto-detect)
	address := ""
	if len(args) > 0 {
		address = args[0]
	} else {
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
		fmt.Printf("%s Inbox %s is already empty\n", style.Dim.Render("○"), address)
		return nil
	}

	// Delete each message
	deleted := 0
	var errors []string
	for _, msg := range messages {
		if err := mailbox.Delete(msg.ID); err != nil {
			// If file is already gone (race condition), ignore it and count as success
			if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
				continue
			}
			errors = append(errors, fmt.Sprintf("%s: %v", msg.ID, err))
		} else {
			deleted++
		}
	}

	// Report results
	if len(errors) > 0 {
		fmt.Printf("%s Cleared %d/%d messages from %s\n",
			style.Bold.Render("⚠"), deleted, len(messages), address)
		for _, e := range errors {
			fmt.Printf("  Error: %s\n", e)
		}
		return fmt.Errorf("failed to clear %d messages", len(errors))
	}

	fmt.Printf("%s Cleared %d messages from %s\n",
		style.Bold.Render("✓"), deleted, address)
	return nil
}
