package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

func runMailSend(cmd *cobra.Command, args []string) error {
	// Handle --stdin: read message body from stdin (avoids shell quoting issues)
	if mailStdin {
		if mailBody != "" {
			return fmt.Errorf("cannot use --stdin with --message/-m")
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		mailBody = strings.TrimRight(string(data), "\n")
	}

	var to string

	humanTarget, humanRoute, err := resolveHumanMailTarget(args)
	if err != nil {
		return err
	}
	if humanRoute {
		to = humanTarget
	} else if mailSendSelf {
		// Auto-detect identity from cwd
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting current directory: %w", err)
		}
		townRoot, err := workspace.FindFromCwd()
		if err != nil || townRoot == "" {
			return fmt.Errorf("not in a Gas Town workspace")
		}
		roleInfo, err := GetRoleWithContext(cwd, townRoot)
		if err != nil {
			return fmt.Errorf("detecting role: %w", err)
		}
		ctx := RoleContext{
			Role:     roleInfo.Role,
			Rig:      roleInfo.Rig,
			Polecat:  roleInfo.Polecat,
			TownRoot: townRoot,
			WorkDir:  cwd,
		}
		to = buildAgentIdentity(ctx)
		if to == "" {
			return fmt.Errorf("cannot determine identity (role: %s)", ctx.Role)
		}
	} else if mailTo != "" {
		to = mailTo
	} else if len(args) > 0 {
		to = args[0]
	} else {
		return fmt.Errorf("address required (use positional arg, --to, or --self)")
	}

	// All mail uses town beads (two-level architecture)
	workDir, err := findMailWorkDir()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Determine sender (--from overrides auto-detection, for relay/bridge use)
	from := mailFrom
	if from == "" {
		from = detectSender()
	}

	// If subject looks like a reply ("Re: ...") but the user didn't pass
	// --reply-to, try to infer the original message from the sender's inbox.
	// When exactly one unambiguous match exists, populate mailReplyTo so the
	// existing thread-lookup + ClearReplyReminders flow below works as designed.
	// hq-k382x: without this, every "gt mail send <addr> -s 'Re: ...'" leaves
	// the queued reply-reminder in place.
	if mailReplyTo == "" && hasReplyPrefix(mailSubject) {
		if inferred := inferReplyTo(workDir, from, to, mailSubject); inferred != "" {
			mailReplyTo = inferred
		}
	}

	// Create message with auto-generated ID and thread ID
	msg := mail.NewMessage(from, to, mailSubject, mailBody)

	// Set priority (--urgent overrides --priority)
	if mailUrgent {
		msg.Priority = mail.PriorityUrgent
	} else {
		msg.Priority = mail.PriorityFromInt(mailPriority)
	}
	if mailNotify && msg.Priority == mail.PriorityNormal {
		msg.Priority = mail.PriorityHigh
	}

	// Set message type
	msg.Type = mail.ParseMessageType(mailType)

	// Set pinned flag
	msg.Pinned = mailPinned

	// Set wisp flag (ephemeral message) - default true, --permanent overrides
	msg.Wisp = mailWisp && !mailPermanent

	// Set CC recipients
	msg.CC = mailCC

	// Suppress router-side notification when --no-notify is passed.
	// Otherwise the router handles idle-aware notification per-recipient,
	// which also works correctly for fan-out (groups, lists, channels).
	if mailNoNotify {
		msg.SuppressNotify = true
	}

	// Handle reply-to: auto-set type to reply and look up thread
	if mailReplyTo != "" {
		msg.ReplyTo = mailReplyTo
		if msg.Type == mail.TypeNotification {
			msg.Type = mail.TypeReply
		}

		// Look up original message in current user's mailbox to get thread ID.
		// The message we're replying to lives in our inbox (we received it),
		// so we look it up via our own identity (from), not the recipient (to).
		router := mail.NewRouter(workDir)
		mailbox, err := router.GetMailbox(from)
		if err != nil {
			style.PrintWarning("could not open mailbox for thread lookup: %v", err)
		} else {
			original, err := mailbox.Get(mailReplyTo)
			if err != nil {
				style.PrintWarning("could not find original message %s for threading (new thread will be created)", mailReplyTo)
			} else {
				msg.ThreadID = original.ThreadID
			}
		}
	}

	// Generate thread ID for new threads
	if msg.ThreadID == "" {
		msg.ThreadID = generateThreadID()
	}

	// Use address resolver for new address types
	townRoot, _ := workspace.FindFromCwd()
	b := beads.New(townRoot)
	resolver := mail.NewResolver(b, townRoot)

	recipients, err := resolver.Resolve(to)
	if err != nil {
		// Validation errors are definitive — do not fall back to legacy routing,
		// which would silently deliver to a dead inbox.
		// See: https://github.com/steveyegge/gastown/issues/2038
		if errors.Is(err, mail.ErrUnknownRecipient) {
			return err
		}
		// Fall back to legacy routing for infrastructure errors (beads down, etc.)
		router := mail.NewRouter(workDir)
		defer router.WaitPendingNotifications()
		if err := router.Send(msg); err != nil {
			return fmt.Errorf("sending message: %w", err)
		}
		_ = events.LogFeed(events.TypeMail, from, events.MailPayload(to, mailSubject))
		fmt.Printf("%s Message sent to %s\n", style.Bold.Render("✓"), to)
		fmt.Printf("  Subject: %s\n", mailSubject)
		return nil
	}

	// Route based on recipient type, collecting errors instead of failing early
	router := mail.NewRouter(workDir)
	defer router.WaitPendingNotifications()
	var recipientAddrs []string
	var sendErrs []string

	for _, rec := range recipients {
		switch rec.Type {
		case mail.RecipientQueue:
			// Queue messages: single message, workers claim
			msg.To = rec.Address
			if err := router.Send(msg); err != nil {
				sendErrs = append(sendErrs, fmt.Sprintf("queue %s: %v", rec.Address, err))
				continue
			}
			recipientAddrs = append(recipientAddrs, rec.Address)

		case mail.RecipientChannel:
			// Channel messages: single message, broadcast
			msg.To = rec.Address
			if err := router.Send(msg); err != nil {
				sendErrs = append(sendErrs, fmt.Sprintf("channel %s: %v", rec.Address, err))
				continue
			}
			recipientAddrs = append(recipientAddrs, rec.Address)

		default:
			// Direct/agent messages: fan out to each recipient
			msgCopy := *msg
			msgCopy.To = rec.Address
			msgCopy.ID = "" // Each fan-out copy gets its own unique ID
			if err := router.Send(&msgCopy); err != nil {
				sendErrs = append(sendErrs, fmt.Sprintf("%s: %v", rec.Address, err))
				continue
			}
			recipientAddrs = append(recipientAddrs, rec.Address)
		}
	}

	if len(sendErrs) > 0 {
		if len(recipientAddrs) == 0 {
			return fmt.Errorf("all sends failed: %s", strings.Join(sendErrs, "; "))
		}
		fmt.Fprintf(os.Stderr, "⚠ Some deliveries failed: %s\n", strings.Join(sendErrs, "; "))
	}
	if mailReplyTo != "" {
		if err := router.ClearReplyReminders(from, msg.ThreadID); err != nil {
			style.PrintWarning("could not clear satisfied reply reminders: %v", err)
		}
	}

	// Log mail event to activity feed
	_ = events.LogFeed(events.TypeMail, from, events.MailPayload(to, mailSubject))

	fmt.Printf("%s Message sent to %s\n", style.Bold.Render("✓"), to)
	fmt.Printf("  Subject: %s\n", mailSubject)

	// Show resolved recipients if fan-out occurred
	if len(recipientAddrs) > 1 || (len(recipientAddrs) == 1 && recipientAddrs[0] != to) {
		fmt.Printf("  Recipients: %s\n", strings.Join(recipientAddrs, ", "))
	}

	if len(msg.CC) > 0 {
		fmt.Printf("  CC: %s\n", strings.Join(msg.CC, ", "))
	}
	if msg.Type != mail.TypeNotification {
		fmt.Printf("  Type: %s\n", msg.Type)
	}

	return nil
}

func resolveHumanMailTarget(args []string) (target string, handled bool, err error) {
	if !mailSendHuman {
		return "", false, nil
	}
	if mailSendSelf || mailTo != "" || len(args) > 0 {
		return "", true, fmt.Errorf("--human cannot be combined with an address, --to, or --self")
	}
	// Recovery alerts must survive session restarts and wisp collection.
	mailPermanent = true
	mailUrgent = true
	return "overseer", true, nil
}

// generateThreadID creates a random thread ID for new message threads.
func generateThreadID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b) // crypto/rand.Read only fails on broken system
	return "thread-" + hex.EncodeToString(b)
}

// hasReplyPrefix reports whether subject begins with a "Re:" prefix
// (case-insensitive, tolerating arbitrary whitespace after the colon).
func hasReplyPrefix(subject string) bool {
	s := strings.TrimSpace(subject)
	if len(s) < 3 {
		return false
	}
	return strings.EqualFold(s[:3], "re:")
}

// normalizeReplySubject strips leading "Re: " prefixes (case-insensitive,
// possibly nested) and surrounding whitespace, so that two subjects with
// different reply nesting compare equal.
func normalizeReplySubject(subject string) string {
	s := strings.TrimSpace(subject)
	for hasReplyPrefix(s) {
		s = strings.TrimSpace(s[3:])
	}
	return strings.ToLower(s)
}

// normalizeAddress lowercases an address and trims a trailing slash so that
// "Mayor/" and "mayor" compare equal. Matches identityVariants behavior in
// mail.Mailbox without depending on its internals.
func normalizeAddress(addr string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(addr)), "/")
}

// inferReplyTo searches the sender's mailbox for a single unambiguous message
// FROM `to` whose subject (after stripping "Re:" prefixes) matches `subject`.
// Returns the matching message ID when exactly one match exists; returns "" on
// no-match, ambiguity, or any error. Best-effort — used only as a convenience
// to make `gt mail send <to> -s "Re: ..."` clear queued reply-reminders.
func inferReplyTo(workDir, from, to, subject string) string {
	router := mail.NewRouter(workDir)
	mailbox, err := router.GetMailbox(from)
	if err != nil {
		return ""
	}
	messages, err := mailbox.List()
	if err != nil {
		return ""
	}
	return pickReplyTo(messages, to, subject)
}

// pickReplyTo is the pure matching logic for inferReplyTo: given a list of
// candidate messages, returns the single matching message's ID, or "" if there
// is no match or more than one match. Pure to keep it unit-testable.
func pickReplyTo(messages []*mail.Message, to, subject string) string {
	wantSubject := normalizeReplySubject(subject)
	if wantSubject == "" {
		return ""
	}
	wantFrom := normalizeAddress(to)

	var matchID string
	matches := 0
	for _, m := range messages {
		if normalizeAddress(m.From) != wantFrom {
			continue
		}
		if normalizeReplySubject(m.Subject) != wantSubject {
			continue
		}
		matches++
		matchID = m.ID
		if matches > 1 {
			return ""
		}
	}
	if matches != 1 {
		return ""
	}
	return matchID
}
