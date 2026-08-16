package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/jonbaldie/gastown/internal/channelevents"
	"github.com/spf13/cobra"
)

var (
	emitEventChannel string
	emitEventType    string
	emitEventPayload []string
)

var moleculeEmitEventCmd = &cobra.Command{
	Use:   "emit-event",
	Short: "Emit a file-based event on a named channel",
	Long: `Emit an event file to ~/gt/events/<channel>/ for subscribers to pick up.

This is the Go counterpart to emit-event.sh. Events are JSON files consumed
by await-event subscribers (e.g., the refinery watching for MERGE_READY events).

EVENT FORMAT:
Creates a JSON file at ~/gt/events/<channel>/<timestamp>.event:
  {"type": "...", "channel": "...", "timestamp": "...", "payload": {...}}

EXAMPLES:
  # Emit a MERGE_READY event for the refinery
  gt mol step emit-event --channel refinery --type MERGE_READY \
    --payload polecat=nux --payload branch=polecat/nux/gt-iw7m

  # Emit a PATROL_WAKE event
  gt mol step emit-event --channel refinery --type PATROL_WAKE \
    --payload source=witness --payload queue_depth=3

  # Emit an MQ_SUBMIT event
  gt mol step emit-event --channel refinery --type MQ_SUBMIT \
    --payload branch=feat/new-feature --payload mr_id=bd-42`,
	RunE: runMoleculeEmitEvent,
}

// EmitEventResult is returned when an event is emitted.
type EmitEventResult struct {
	Path    string `json:"path"`
	Channel string `json:"channel"`
	Type    string `json:"type"`
}

func init() {
	moleculeEmitEventCmd.Flags().StringVar(&emitEventChannel, "channel", "",
		"Event channel name (required, e.g., 'refinery')")
	moleculeEmitEventCmd.Flags().StringVar(&emitEventType, "type", "",
		"Event type (required, e.g., 'MERGE_READY')")
	moleculeEmitEventCmd.Flags().StringArrayVar(&emitEventPayload, "payload", nil,
		"Payload key=value pairs (repeatable)")
	moleculeEmitEventCmd.Flags().BoolVar(&moleculeJSON, "json", false,
		"Output as JSON")
	_ = moleculeEmitEventCmd.MarkFlagRequired("channel")
	_ = moleculeEmitEventCmd.MarkFlagRequired("type")

	moleculeStepCmd.AddCommand(moleculeEmitEventCmd)
}

func runMoleculeEmitEvent(cmd *cobra.Command, args []string) error {
	path, err := channelevents.Emit(emitEventChannel, emitEventType, emitEventPayload)
	if err != nil {
		return err
	}

	if moleculeJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(EmitEventResult{
			Path:    path,
			Channel: emitEventChannel,
			Type:    emitEventType,
		})
	}

	fmt.Println(path)
	return nil
}
