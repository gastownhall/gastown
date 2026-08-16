package cmd

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/mail"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/townlog"
	"github.com/jonbaldie/gastown/internal/witness"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

// Callback message subject patterns for routing.
var (
	// POLECAT_DONE <name> - polecat signaled completion
	patternPolecatDone = regexp.MustCompile(`^POLECAT_DONE\s+(\S+)`)

	// Merge Request Rejected: <branch> - refinery rejected MR
	patternMergeRejected = regexp.MustCompile(`^Merge Request Rejected:\s+(.+)`)

	// Merge Request Completed: <branch> - refinery completed MR
	patternMergeCompleted = regexp.MustCompile(`^Merge Request Completed:\s+(.+)`)

	// HELP: <topic> - polecat requesting help
	patternHelp = regexp.MustCompile(`^HELP:\s+(.+)`)

	// ESCALATION: <topic> - witness escalating issue
	patternEscalation = regexp.MustCompile(`^ESCALATION:\s+(.+)`)

	// SLING_REQUEST: <bead-id> - request to sling work
	patternSling = regexp.MustCompile(`^SLING_REQUEST:\s+(\S+)`)

	// NOTE: WITNESS_REPORT and REFINERY_REPORT removed.
	// Witnesses and Refineries handle their duties autonomously.
	// They only escalate genuine problems, not routine status updates.
)

// CallbackType identifies the type of callback message.
type CallbackType string

const (
	CallbackPolecatDone    CallbackType = "polecat_done"
	CallbackMergeRejected  CallbackType = "merge_rejected"
	CallbackMergeCompleted CallbackType = "merge_completed"
	CallbackHelp           CallbackType = "help"
	CallbackEscalation     CallbackType = "escalation"
	CallbackSling          CallbackType = "sling"
	CallbackUnknown        CallbackType = "unknown"
	// NOTE: CallbackWitnessReport and CallbackRefineryReport removed.
	// Routine status reports are no longer sent to Mayor.
)

// CallbackResult tracks the result of processing a callback.
type CallbackResult struct {
	MessageID    string
	CallbackType CallbackType
	From         string
	Subject      string
	Handled      bool
	Action       string
	Error        error
}

var callbacksCmd = &cobra.Command{
	Use:     "callbacks",
	GroupID: GroupAgents,
	Short:   "Handle agent callbacks",
	Long: `Handle callbacks from agents during Deacon patrol.

Callbacks are messages sent to the Mayor from:
- Witnesses reporting polecat status
- Refineries reporting merge results
- Polecats requesting help or escalation
- External triggers (webhooks, timers)

This command processes the Mayor's inbox and handles each message
appropriately, routing to other agents or updating state as needed.`,
}

var callbacksProcessCmd = &cobra.Command{
	Use:   "process",
	Short: "Process pending callbacks",
	Long: `Process all pending callbacks in the Mayor's inbox.

Reads unread messages from the Mayor's inbox and handles each based on
its type:

  POLECAT_DONE       - Log completion, update stats
  MERGE_COMPLETED    - Notify worker, close source issue
  MERGE_REJECTED     - Notify worker of rejection reason
  HELP:              - Route to human or handle if possible
  ESCALATION:        - Log and route to human
  SLING_REQUEST:     - Spawn polecat for the work

Note: Witnesses and Refineries handle routine operations autonomously.
They only send escalations for genuine problems, not status reports.

Unknown message types are logged but left unprocessed.`,
	RunE: runCallbacksProcess,
}

var (
	callbacksDryRun  bool
	callbacksVerbose bool
)

func init() {
	callbacksProcessCmd.Flags().BoolVar(&callbacksDryRun, "dry-run", false, "Show what would be processed without taking action")
	callbacksProcessCmd.Flags().BoolVarP(&callbacksVerbose, "verbose", "v", false, "Show detailed processing info")

	callbacksCmd.AddCommand(callbacksProcessCmd)
	rootCmd.AddCommand(callbacksCmd)
}

func runCallbacksProcess(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Get Mayor's mailbox
	router := mail.NewRouter(townRoot)
	mailbox, err := router.GetMailbox("mayor/")
	if err != nil {
		return fmt.Errorf("getting mayor mailbox: %w", err)
	}

	// Get unread messages
	messages, err := mailbox.ListUnread()
	if err != nil {
		return fmt.Errorf("listing unread messages: %w", err)
	}

	if len(messages) == 0 {
		fmt.Printf("%s No pending callbacks\n", style.Dim.Render("○"))
		return nil
	}

	fmt.Printf("%s Processing %d callback(s)\n", style.Bold.Render("●"), len(messages))

	var results []CallbackResult
	for _, msg := range messages {
		result := processCallback(townRoot, msg, callbacksDryRun)
		results = append(results, result)

		// Print result
		if result.Error != nil {
			fmt.Printf("  %s %s: %v\n",
				style.Error.Render("✗"),
				msg.Subject,
				result.Error)
		} else if result.Handled {
			fmt.Printf("  %s [%s] %s\n",
				style.Bold.Render("✓"),
				result.CallbackType,
				result.Action)
		} else {
			fmt.Printf("  %s [%s] %s\n",
				style.Dim.Render("○"),
				result.CallbackType,
				result.Action)
		}

		if callbacksVerbose {
			fmt.Printf("      From: %s\n", msg.From)
			fmt.Printf("      Subject: %s\n", msg.Subject)
		}
	}

	// Summary
	handled := 0
	errors := 0
	for _, r := range results {
		if r.Handled {
			handled++
		}
		if r.Error != nil {
			errors++
		}
	}

	fmt.Println()
	if callbacksDryRun {
		fmt.Printf("%s Dry run: would process %d/%d callbacks\n",
			style.Dim.Render("○"), handled, len(results))
	} else {
		fmt.Printf("%s Processed %d/%d callbacks",
			style.Bold.Render("✓"), handled, len(results))
		if errors > 0 {
			fmt.Printf(" (%d errors)", errors)
		}
		fmt.Println()
	}

	return nil
}

// processCallback handles a single callback message and returns the result.
func processCallback(townRoot string, msg *mail.Message, dryRun bool) CallbackResult {
	result := CallbackResult{
		MessageID: msg.ID,
		From:      msg.From,
		Subject:   msg.Subject,
	}

	// Classify the callback
	result.CallbackType = classifyCallback(msg.Subject)

	// Handle based on type
	switch result.CallbackType {
	case CallbackPolecatDone:
		result.Action, result.Error = handlePolecatDone(townRoot, msg, dryRun)
		result.Handled = result.Error == nil

	case CallbackMergeCompleted:
		result.Action, result.Error = handleMergeCompleted(townRoot, msg, dryRun)
		result.Handled = result.Error == nil

	case CallbackMergeRejected:
		result.Action, result.Error = handleMergeRejected(townRoot, msg, dryRun)
		result.Handled = result.Error == nil

	case CallbackHelp:
		result.Action, result.Error = handleHelp(townRoot, msg, dryRun)
		result.Handled = result.Error == nil

	case CallbackEscalation:
		result.Action, result.Error = handleEscalation(townRoot, msg, dryRun)
		result.Handled = result.Error == nil

	case CallbackSling:
		result.Action, result.Error = handleSling(townRoot, msg, dryRun)
		result.Handled = result.Error == nil

	default:
		result.Action = "unknown message type, skipped"
		result.Handled = false
	}

	// Archive handled messages (unless dry-run)
	if result.Handled && !dryRun {
		router := mail.NewRouter(townRoot)
		if mailbox, err := router.GetMailbox("mayor/"); err == nil {
			_ = mailbox.Delete(msg.ID)
		}
	}

	return result
}

// classifyCallback determines the type of callback from the subject line.
func classifyCallback(subject string) CallbackType {
	switch {
	case patternPolecatDone.MatchString(subject):
		return CallbackPolecatDone
	case patternMergeRejected.MatchString(subject):
		return CallbackMergeRejected
	case patternMergeCompleted.MatchString(subject):
		return CallbackMergeCompleted
	case patternHelp.MatchString(subject):
		return CallbackHelp
	case patternEscalation.MatchString(subject):
		return CallbackEscalation
	case patternSling.MatchString(subject):
		return CallbackSling
	default:
		return CallbackUnknown
	}
}

// handlePolecatDone processes a POLECAT_DONE callback.
// These come from Witnesses forwarding polecat completion notices.
func handlePolecatDone(townRoot string, msg *mail.Message, dryRun bool) (string, error) {
	matches := patternPolecatDone.FindStringSubmatch(msg.Subject)
	if len(matches) < 2 {
		return "", fmt.Errorf("could not parse polecat name from subject: %q", msg.Subject)
	}
	polecatName := matches[1]

	// Extract info from body
	var exitType, issueID string
	for _, line := range strings.Split(msg.Body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Exit:") {
			exitType = strings.TrimSpace(strings.TrimPrefix(line, "Exit:"))
		}
		if strings.HasPrefix(line, "Issue:") {
			issueID = strings.TrimSpace(strings.TrimPrefix(line, "Issue:"))
		}
	}

	if dryRun {
		return fmt.Sprintf("would log completion for %s (exit=%s, issue=%s)",
			polecatName, exitType, issueID), nil
	}

	// Log the completion
	logCallback(townRoot, fmt.Sprintf("polecat_done: %s completed with %s (issue: %s)",
		msg.From, exitType, issueID))

	return fmt.Sprintf("logged completion for %s", polecatName), nil
}

// handleMergeCompleted processes a merge completion callback from Refinery.
func handleMergeCompleted(townRoot string, msg *mail.Message, dryRun bool) (string, error) {
	matches := patternMergeCompleted.FindStringSubmatch(msg.Subject)
	if len(matches) < 2 {
		return "", fmt.Errorf("could not parse branch from subject: %q", msg.Subject)
	}
	branch := matches[1]

	// Extract MR ID and source issue from body
	var mrID, sourceIssue, mergeCommit string
	for _, line := range strings.Split(msg.Body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "MR:") {
			mrID = strings.TrimSpace(strings.TrimPrefix(line, "MR:"))
		}
		if strings.HasPrefix(line, "Source:") {
			sourceIssue = strings.TrimSpace(strings.TrimPrefix(line, "Source:"))
		}
		if strings.HasPrefix(line, "Commit:") {
			mergeCommit = strings.TrimSpace(strings.TrimPrefix(line, "Commit:"))
		}
	}

	if dryRun {
		return fmt.Sprintf("would close source issue %s (mr=%s, commit=%s)",
			sourceIssue, mrID, mergeCommit), nil
	}

	// Log the merge
	logCallback(townRoot, fmt.Sprintf("merge_completed: branch %s merged (mr=%s, source=%s, commit=%s)",
		branch, mrID, sourceIssue, mergeCommit))

	// Close the source issue if we have it
	if sourceIssue != "" {
		cwd, _ := os.Getwd()
		bd := beads.New(cwd)
		reason := fmt.Sprintf("Merged in %s", mergeCommit)
		if err := bd.Close(sourceIssue, reason); err != nil {
			// Non-fatal: issue might already be closed or not exist
			return fmt.Sprintf("logged merge for %s (could not close %s: %v)",
				branch, sourceIssue, err), nil
		}
	}

	return fmt.Sprintf("logged merge for %s, closed %s", branch, sourceIssue), nil
}

// handleMergeRejected processes a merge rejection callback from Refinery.
func handleMergeRejected(townRoot string, msg *mail.Message, dryRun bool) (string, error) {
	matches := patternMergeRejected.FindStringSubmatch(msg.Subject)
	if len(matches) < 2 {
		return "", fmt.Errorf("could not parse branch from subject: %q", msg.Subject)
	}
	branch := matches[1]

	// Extract reason from body
	var reason string
	if strings.Contains(msg.Body, "Reason:") {
		parts := strings.SplitN(msg.Body, "Reason:", 2)
		if len(parts) > 1 {
			reason = strings.TrimSpace(parts[1])
			// Take just the first line of the reason
			if idx := strings.Index(reason, "\n"); idx > 0 {
				reason = reason[:idx]
			}
		}
	}

	if dryRun {
		return fmt.Sprintf("would log rejection for %s (reason: %s)", branch, reason), nil
	}

	// Log the rejection
	logCallback(townRoot, fmt.Sprintf("merge_rejected: branch %s rejected: %s", branch, reason))

	return fmt.Sprintf("logged rejection for %s", branch), nil
}

// handleHelp processes a HELP: request from a polecat.
// Assesses category and severity to determine priority and routing.
func handleHelp(townRoot string, msg *mail.Message, dryRun bool) (string, error) {
	// Parse the help payload for structured assessment
	payload, err := witness.ParseHelp(msg.Subject, msg.Body)
	if err != nil {
		return "", fmt.Errorf("could not parse help request: %w", err)
	}

	// Assess category and severity from content
	assessment := witness.AssessHelp(payload)

	if dryRun {
		return fmt.Sprintf("would forward help request to overseer: %s [%s/%s]",
			payload.Topic, assessment.Category, assessment.Severity), nil
	}

	// Map assessed severity to mail priority
	var priority mail.Priority
	switch assessment.Severity {
	case witness.HelpSeverityCritical:
		priority = mail.PriorityUrgent
	case witness.HelpSeverityHigh:
		priority = mail.PriorityHigh
	default:
		priority = mail.PriorityNormal
	}

	// Forward to overseer (human) with assessed priority
	router := mail.NewRouter(townRoot)
	defer router.WaitPendingNotifications()
	fwd := &mail.Message{
		From:    "mayor/",
		To:      "overseer",
		Subject: fmt.Sprintf("[FWD][%s] HELP: %s", strings.ToUpper(string(assessment.Severity)), payload.Topic),
		Body: fmt.Sprintf("Forwarded from: %s\nAssessment: category=%s severity=%s (suggest → %s)\nRationale: %s\n\n%s",
			msg.From, assessment.Category, assessment.Severity, assessment.SuggestTo, assessment.Rationale, msg.Body),
		Priority: priority,
	}
	if err := router.Send(fwd); err != nil {
		return "", fmt.Errorf("forwarding to overseer: %w", err)
	}

	// Log the help request with assessment
	logCallback(townRoot, fmt.Sprintf("help_request: from %s: %s [%s/%s]",
		msg.From, payload.Topic, assessment.Category, assessment.Severity))

	return fmt.Sprintf("forwarded help request to overseer: %s [%s/%s]",
		payload.Topic, assessment.Category, assessment.Severity), nil
}

// handleEscalation processes an ESCALATION: from a Witness.
func handleEscalation(townRoot string, msg *mail.Message, dryRun bool) (string, error) {
	matches := patternEscalation.FindStringSubmatch(msg.Subject)
	if len(matches) < 2 {
		return "", fmt.Errorf("could not parse topic from subject: %q", msg.Subject)
	}
	topic := matches[1]

	if dryRun {
		return fmt.Sprintf("would forward escalation to overseer: %s", topic), nil
	}

	// Forward to overseer with urgent priority
	router := mail.NewRouter(townRoot)
	defer router.WaitPendingNotifications()
	fwd := &mail.Message{
		From:     "mayor/",
		To:       "overseer",
		Subject:  fmt.Sprintf("[ESCALATION] %s", topic),
		Body:     fmt.Sprintf("Escalated by: %s\n\n%s", msg.From, msg.Body),
		Priority: mail.PriorityUrgent,
	}
	if err := router.Send(fwd); err != nil {
		return "", fmt.Errorf("forwarding escalation: %w", err)
	}

	// Log the escalation
	logCallback(townRoot, fmt.Sprintf("escalation: from %s: %s", msg.From, topic))

	return fmt.Sprintf("forwarded escalation to overseer: %s", topic), nil
}

// handleSling processes a SLING_REQUEST to spawn work on a polecat.
func handleSling(townRoot string, msg *mail.Message, dryRun bool) (string, error) {
	matches := patternSling.FindStringSubmatch(msg.Subject)
	if len(matches) < 2 {
		return "", fmt.Errorf("could not parse bead ID from subject: %q", msg.Subject)
	}
	beadID := matches[1]

	// Extract rig from body
	var targetRig string
	for _, line := range strings.Split(msg.Body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Rig:") {
			targetRig = strings.TrimSpace(strings.TrimPrefix(line, "Rig:"))
		}
	}

	if targetRig == "" {
		return "", fmt.Errorf("no target rig specified in sling request")
	}

	if dryRun {
		return fmt.Sprintf("would sling %s to %s", beadID, targetRig), nil
	}

	// Log the sling (actual spawn happens via gt sling command)
	logCallback(townRoot, fmt.Sprintf("sling_request: bead %s to rig %s", beadID, targetRig))

	// Note: We don't actually spawn here - that would be done by the Deacon
	// executing the sling command based on this request.
	return fmt.Sprintf("logged sling request: %s to %s (execute with: gt sling %s %s)",
		beadID, targetRig, beadID, targetRig), nil
}

// logCallback logs a callback processing event to the town log.
func logCallback(townRoot, context string) {
	logger := townlog.NewLogger(townRoot)
	_ = logger.Log(townlog.EventCallback, "mayor/", context)
}
