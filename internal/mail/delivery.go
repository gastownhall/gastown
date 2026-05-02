package mail

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

const (
	// DeliveryStatePending indicates a message has been durably written but not
	// yet acknowledged by a worker/recipient.
	DeliveryStatePending = "pending"
	// DeliveryStateAcked indicates receipt has been acknowledged.
	DeliveryStateAcked = "acked"

	// Label keys used for two-phase delivery tracking.
	DeliveryLabelPending       = "delivery:pending"
	DeliveryLabelAcked         = "delivery:acked"
	DeliveryLabelAckedByPrefix = "delivery-acked-by:"
	DeliveryLabelAckedAtPrefix = "delivery-acked-at:"
)

// DeliverySendLabels returns labels written during phase-1 (send).
func DeliverySendLabels() []string {
	return []string{DeliveryLabelPending}
}

// DeliveryAckLabelSequence returns labels for phase-2 (ack). The ordering is
// intentional for crash safety: state remains pending until the final ack label
// write succeeds.
func DeliveryAckLabelSequence(recipientIdentity string, at time.Time) []string {
	ackedAt := at.UTC().Format(time.RFC3339)
	return []string{
		DeliveryLabelAckedByPrefix + recipientIdentity,
		DeliveryLabelAckedAtPrefix + ackedAt,
		DeliveryLabelAcked,
	}
}

// DeliveryAckLabelSequenceIdempotent returns ack labels, reusing an existing
// timestamp from existingLabels if one is present AND the recipient identity
// matches. This ensures retries produce the exact same label set instead of
// appending duplicate timestamps. If the recipient differs (e.g., after a
// claim-release-reclaim cycle), a fresh timestamp is generated.
//
// The scan is order-independent because bd show --json returns labels in
// lexicographic order, not insertion order. We collect all acked-by and
// acked-at values and only reuse a timestamp when this recipient is the
// sole acker (no mixed state from crash recovery).
func DeliveryAckLabelSequenceIdempotent(recipientIdentity string, at time.Time, existingLabels []string) []string {
	ts := at.UTC().Format(time.RFC3339)
	var recipients []string
	var timestamps []string
	for _, label := range existingLabels {
		if strings.HasPrefix(label, DeliveryLabelAckedByPrefix) {
			recipients = append(recipients, strings.TrimPrefix(label, DeliveryLabelAckedByPrefix))
		}
		if strings.HasPrefix(label, DeliveryLabelAckedAtPrefix) {
			timestamps = append(timestamps, strings.TrimPrefix(label, DeliveryLabelAckedAtPrefix))
		}
	}
	// Reuse existing timestamp only when this recipient is the sole acker
	// and exactly one timestamp exists. Multiple acked-by labels indicate
	// mixed state from crash recovery — use fresh timestamp to avoid
	// cross-recipient leakage.
	if len(recipients) == 1 && recipients[0] == recipientIdentity && len(timestamps) == 1 {
		ts = timestamps[0]
	}
	return []string{
		DeliveryLabelAckedByPrefix + recipientIdentity,
		DeliveryLabelAckedAtPrefix + ts,
		DeliveryLabelAcked,
	}
}

// AcknowledgeDeliveryBead writes phase-2 delivery ack labels for a bead.
// It reads existing labels for idempotent retry (reusing prior timestamps),
// then writes the ack label sequence. Uses runBdCommand with timeouts.
//
// If beadID routes to a different rig than beadsDir, BEADS_DIR is stripped
// ("") so bd performs its own prefix-based routing via routes.jsonl. Pinning
// BEADS_DIR at a rig's .beads bypasses routing and fails the lookup for
// beads whose prefix maps to a different database on the shared Dolt
// server (au-ofe, au-b9d).
func AcknowledgeDeliveryBead(workDir, beadsDir, beadID, recipientIdentity string) error {
	beadsDir = routedBeadsDirForID(beadsDir, beadID)
	existingLabels, readErr := readBeadLabelsShared(workDir, beadsDir, beadID)
	if readErr != nil {
		// Log but proceed with empty labels — fresh timestamp is acceptable
		// degradation vs blocking the ack entirely.
		fmt.Fprintf(os.Stderr, "delivery ack: could not read labels for %s: %v (proceeding with fresh timestamp)\n", beadID, readErr)
	}

	for _, label := range deliveryAckLabelsToWrite(recipientIdentity, timeNow().UTC(), existingLabels) {
		args := []string{"label", "add", beadID, label}
		ctx, cancel := bdWriteCtx()
		_, err := runBdCommand(ctx, args, workDir, beadsDir)
		cancel()
		if err == nil {
			continue // bd label add silently succeeds on duplicate labels.
		}
		if bdErr, ok := err.(*bdError); ok && (bdErr.ContainsError("not found") || bdErr.ContainsError("no issue found")) {
			return ErrMessageNotFound
		}
		return err
	}
	return nil
}

// deliveryAckLabelsToWrite returns the idempotent ack labels that are not
// already present. This avoids duplicate bd label-add calls, which otherwise
// attempt a Dolt commit and produce "nothing to commit" warnings.
func deliveryAckLabelsToWrite(recipientIdentity string, at time.Time, existingLabels []string) []string {
	sequence := DeliveryAckLabelSequenceIdempotent(recipientIdentity, at, existingLabels)
	if len(existingLabels) == 0 {
		return sequence
	}

	existing := make(map[string]struct{}, len(existingLabels))
	for _, label := range existingLabels {
		existing[label] = struct{}{}
	}

	missing := make([]string, 0, len(sequence))
	for _, label := range sequence {
		if _, ok := existing[label]; ok {
			continue
		}
		missing = append(missing, label)
	}
	return missing
}

// routedBeadsDirForID returns the BEADS_DIR value to pass into runBdCommand
// so that beadID resolves correctly. Same-rig ids keep currentBeadsDir;
// cross-rig ids return "" so bd's own prefix routing (via routes.jsonl in
// the town's .beads) dispatches to the correct database. See au-ofe, au-b9d.
func routedBeadsDirForID(currentBeadsDir, beadID string) string {
	target := beads.ResolveBeadsDirForID(currentBeadsDir, beadID)
	if target == "" || target == currentBeadsDir {
		return currentBeadsDir
	}
	return ""
}

// readBeadLabelsShared reads the labels for a bead, returning an error on failure
// instead of silently swallowing it.
func readBeadLabelsShared(workDir, beadsDir, id string) ([]string, error) {
	args := []string{"show", id, "--json"}
	ctx, cancel := bdReadCtx()
	defer cancel()
	stdout, err := runBdCommand(ctx, args, workDir, beadsDir)
	if err != nil {
		return nil, fmt.Errorf("bd show %s: %w", id, err)
	}
	var bms []BeadsMessage
	if err := json.Unmarshal(stdout, &bms); err != nil {
		return nil, fmt.Errorf("parsing bd show %s: %w", id, err)
	}
	if len(bms) == 0 {
		return nil, nil
	}
	return bms[0].Labels, nil
}

// ParseDeliveryLabels derives delivery state and ack metadata from labels.
// The state is append-only:
// - `delivery:pending` means pending
// - once `delivery:acked` appears, state is acked (even if pending remains)
//
// Note: bd show --json returns labels in lexicographic order, so this parser
// must be order-independent. It uses last-wins for both acked-by and acked-at.
// For RFC3339 timestamps, lexicographic last-wins is chronologically correct.
func ParseDeliveryLabels(labels []string) (state, ackedBy string, ackedAt *time.Time) {
	hasPending := false
	hasAcked := false

	for _, label := range labels {
		switch {
		case label == DeliveryLabelPending:
			hasPending = true
		case label == DeliveryLabelAcked:
			hasAcked = true
		case strings.HasPrefix(label, DeliveryLabelAckedByPrefix):
			ackedBy = strings.TrimPrefix(label, DeliveryLabelAckedByPrefix)
		case strings.HasPrefix(label, DeliveryLabelAckedAtPrefix):
			ts := strings.TrimPrefix(label, DeliveryLabelAckedAtPrefix)
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				ackedAt = &t
			}
		}
	}

	if hasAcked {
		return DeliveryStateAcked, ackedBy, ackedAt
	}
	if hasPending {
		return DeliveryStatePending, "", nil
	}
	return "", "", nil
}
