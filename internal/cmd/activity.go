package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/jonbaldie/gastown/internal/events"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

// Activity emit command flags
var (
	activityEventType string
	activityActor     string
	activityRig       string
	activityPolecat   string
	activityTarget    string
	activityReason    string
	activityMessage   string
	activityStatus    string
	activityIssue     string
	activityTo        string
	activityCount     int
)

var activityCmd = &cobra.Command{
	Use:     "activity",
	GroupID: GroupDiag,
	Short:   "Emit and view activity events",
	Long: `Emit and view activity events for the Gas Town activity feed.

Events are written to ~/gt/.events.jsonl and can be viewed with 'gt feed'.

Subcommands:
  emit    Emit an activity event`,
}

var activityEmitCmd = &cobra.Command{
	Use:   "emit <event-type>",
	Short: "Emit an activity event",
	Long: `Emit an activity event to the Gas Town activity feed.

Supported event types for witness patrol:
  patrol_started   - When witness begins patrol cycle
  polecat_checked  - When witness checks a polecat
  polecat_nudged   - When witness nudges a stuck polecat
  escalation_sent  - When witness escalates to Mayor/Deacon
  patrol_complete  - When patrol cycle finishes

Supported event types for refinery:
  merge_started    - When refinery starts a merge
  merge_complete   - When merge succeeds
  merge_failed     - When merge fails
  queue_processed  - When refinery finishes processing queue

Common options:
  --actor    Who is emitting the event (e.g., greenplace/witness)
  --rig      Which rig the event is about
  --message  Human-readable message

Examples:
  gt activity emit patrol_started --rig greenplace --count 3
  gt activity emit polecat_checked --rig greenplace --polecat Toast --status working --issue gp-xyz
  gt activity emit polecat_nudged --rig greenplace --polecat Toast --reason "idle for 10 minutes"
  gt activity emit escalation_sent --rig greenplace --target Toast --to mayor --reason "unresponsive"
  gt activity emit patrol_complete --rig greenplace --count 3 --message "All polecats healthy"`,
	Args: cobra.ExactArgs(1),
	RunE: runActivityEmit,
}

func init() {
	// Emit command flags
	activityEmitCmd.Flags().StringVar(&activityActor, "actor", "", "Actor emitting the event (auto-detected if not set)")
	activityEmitCmd.Flags().StringVar(&activityRig, "rig", "", "Rig the event is about")
	activityEmitCmd.Flags().StringVar(&activityPolecat, "polecat", "", "Polecat involved (for polecat_checked, polecat_nudged)")
	activityEmitCmd.Flags().StringVar(&activityTarget, "target", "", "Target of the action (for escalation)")
	activityEmitCmd.Flags().StringVar(&activityReason, "reason", "", "Reason for the action")
	activityEmitCmd.Flags().StringVar(&activityMessage, "message", "", "Human-readable message")
	activityEmitCmd.Flags().StringVar(&activityStatus, "status", "", "Status (for polecat_checked: working, idle, stuck)")
	activityEmitCmd.Flags().StringVar(&activityIssue, "issue", "", "Issue ID (for polecat_checked)")
	activityEmitCmd.Flags().StringVar(&activityTo, "to", "", "Escalation target (for escalation_sent: mayor, deacon)")
	activityEmitCmd.Flags().IntVar(&activityCount, "count", 0, "Polecat count (for patrol events)")

	activityCmd.AddCommand(activityEmitCmd)
	rootCmd.AddCommand(activityCmd)
}

func runActivityEmit(cmd *cobra.Command, args []string) error {
	eventType := args[0]

	// Validate we're in a Gas Town workspace
	_, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Auto-detect actor if not provided
	actor := activityActor
	if actor == "" {
		actor = detectActor()
	}

	// Build payload based on event type
	var payload map[string]interface{}

	switch eventType {
	case events.TypePatrolStarted, events.TypePatrolComplete:
		if activityRig == "" {
			return fmt.Errorf("--rig is required for %s events", eventType)
		}
		payload = events.PatrolPayload(activityRig, activityCount, activityMessage)

	case events.TypePolecatChecked:
		if activityRig == "" || activityPolecat == "" {
			return fmt.Errorf("--rig and --polecat are required for polecat_checked events")
		}
		if activityStatus == "" {
			activityStatus = "checked"
		}
		payload = events.PolecatCheckPayload(activityRig, activityPolecat, activityStatus, activityIssue)

	case events.TypePolecatNudged:
		if activityRig == "" || activityPolecat == "" {
			return fmt.Errorf("--rig and --polecat are required for polecat_nudged events")
		}
		payload = events.NudgePayload(activityRig, activityPolecat, activityReason)

	case events.TypeEscalationSent:
		if activityRig == "" || activityTarget == "" || activityTo == "" {
			return fmt.Errorf("--rig, --target, and --to are required for escalation_sent events")
		}
		payload = events.EscalationPayload(activityRig, activityTarget, activityTo, activityReason)

	case events.TypeMergeStarted, events.TypeMerged, events.TypeMergeFailed, events.TypeMergeSkipped:
		// Refinery events - flexible payload
		payload = make(map[string]interface{})
		if activityRig != "" {
			payload["rig"] = activityRig
		}
		if activityMessage != "" {
			payload["message"] = activityMessage
		}
		if activityTarget != "" {
			payload["branch"] = activityTarget
		}
		if activityReason != "" {
			payload["reason"] = activityReason
		}

	default:
		// Generic event - use whatever flags are provided
		payload = make(map[string]interface{})
		if activityRig != "" {
			payload["rig"] = activityRig
		}
		if activityPolecat != "" {
			payload["polecat"] = activityPolecat
		}
		if activityTarget != "" {
			payload["target"] = activityTarget
		}
		if activityReason != "" {
			payload["reason"] = activityReason
		}
		if activityMessage != "" {
			payload["message"] = activityMessage
		}
		if activityStatus != "" {
			payload["status"] = activityStatus
		}
		if activityIssue != "" {
			payload["issue"] = activityIssue
		}
		if activityTo != "" {
			payload["to"] = activityTo
		}
		if activityCount > 0 {
			payload["count"] = activityCount
		}
	}

	// Emit the event
	if err := events.LogFeed(eventType, actor, payload); err != nil {
		return fmt.Errorf("emitting event: %w", err)
	}

	// Print confirmation
	payloadJSON, _ := json.Marshal(payload)
	fmt.Printf("%s Emitted %s event\n", style.Success.Render("✓"), style.Bold.Render(eventType))
	fmt.Printf("  Actor:   %s\n", actor)
	fmt.Printf("  Payload: %s\n", string(payloadJSON))

	return nil
}

// Note: detectActor is defined in sling.go and reused here
