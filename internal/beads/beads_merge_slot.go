// Package beads provides merge slot management for serialized conflict resolution.
//
// The merge slot is a single bead identified by the label "gt:merge-slot".
// Its holder is stored in the bead's Description field as a JSON blob:
//
//	{"holder": "<actor>", "waiters": ["<actor1>", ...]}
//
// When holder is empty the slot is available. The bd merge-slot command was
// removed in v0.62; this implementation uses standard bead CRUD operations
// (Create/List/Show/Update) that remain available in v0.62+.
package beads

import (
	"encoding/json"
	"errors"
	"fmt"
)

// MergeSlotStatus represents the result of checking a merge slot.
type MergeSlotStatus struct {
	ID        string      `json:"id"`
	Available bool        `json:"available"`
	Holder    string      `json:"holder,omitempty"`
	Waiters   []string    `json:"waiters,omitempty"`
	Lease     *MergeLease `json:"lease,omitempty"`
	Error     string      `json:"error,omitempty"`
}

type MergeLeasePhase string

const (
	MergeLeasePhaseAcquired          MergeLeasePhase = "acquired"
	MergeLeasePhaseMutationPossible  MergeLeasePhase = "mutation-possible"
	MergeLeasePhaseProofEstablished  MergeLeasePhase = "proof-established"
	MergeLeasePhaseLifecycleComplete MergeLeasePhase = "lifecycle-complete"
)

type MergeLease struct {
	OwnerToken    string          `json:"owner_token"`
	Phase         MergeLeasePhase `json:"phase"`
	Target        string          `json:"target"`
	PrePushSHA    string          `json:"pre_push_sha"`
	IntendedTip   string          `json:"intended_tip"`
	MRIDs         []string        `json:"mr_ids"`
	SubmittedSHAs []string        `json:"submitted_shas"`
	BatchID       string          `json:"batch_id,omitempty"`
}

// mergeSlotData is the JSON structure stored in the merge slot bead's Description.
type mergeSlotData struct {
	Holder  string      `json:"holder"`
	Waiters []string    `json:"waiters,omitempty"`
	Lease   *MergeLease `json:"lease,omitempty"`
}

// parseMergeSlotData decodes the merge slot state from a bead's Description field.
func parseMergeSlotData(issue *Issue) (mergeSlotData, error) {
	if issue.Description == "" {
		return mergeSlotData{}, nil
	}
	var data mergeSlotData
	if err := json.Unmarshal([]byte(issue.Description), &data); err != nil {
		return mergeSlotData{}, fmt.Errorf("decode merge slot %s: %w", issue.ID, err)
	}
	return data, nil
}

// mergeSlotStatusFromIssue builds a MergeSlotStatus from a bead issue.
func mergeSlotStatusFromIssue(issue *Issue) (*MergeSlotStatus, error) {
	data, err := parseMergeSlotData(issue)
	if err != nil {
		return nil, err
	}
	return &MergeSlotStatus{
		ID:        issue.ID,
		Available: data.Holder == "",
		Holder:    data.Holder,
		Waiters:   data.Waiters,
		Lease:     data.Lease,
	}, nil
}

// getMergeSlotBead finds the merge slot bead (label=gt:merge-slot).
// Returns ErrNotFound if no slot bead exists.
func (b *Beads) getMergeSlotBead() (*Issue, error) {
	issues, err := b.List(ListOptions{Label: "gt:merge-slot"})
	if err != nil {
		return nil, fmt.Errorf("listing merge slot beads: %w", err)
	}
	if len(issues) == 0 {
		return nil, ErrNotFound
	}
	// Show the bead to get its full Description (list output may be truncated).
	return b.Show(issues[0].ID)
}

// MergeSlotCreate creates the merge slot bead for the current rig.
// The slot is used for serialized conflict resolution in the merge queue.
// Returns the slot ID if successful.
func (b *Beads) MergeSlotCreate() (string, error) {
	initial, _ := json.Marshal(mergeSlotData{})
	issue, err := b.Create(CreateOptions{
		Title:       "merge-slot",
		Labels:      []string{"gt:merge-slot"},
		Description: string(initial),
	})
	if err != nil {
		return "", fmt.Errorf("creating merge slot: %w", err)
	}
	return issue.ID, nil
}

// MergeSlotCheck checks the availability of the merge slot.
// Returns the current status including holder and waiters if held.
func (b *Beads) MergeSlotCheck() (*MergeSlotStatus, error) {
	issue, err := b.getMergeSlotBead()
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return &MergeSlotStatus{Error: "not found"}, nil
		}
		return nil, fmt.Errorf("checking merge slot: %w", err)
	}
	return mergeSlotStatusFromIssue(issue)
}

// MergeSlotAcquire attempts to acquire the merge slot for exclusive access.
// If holder is empty, defaults to the configured actor.
// If addWaiter is true and the slot is held, the requester is added to the
// waiters queue (informational; callers use retries for contention handling).
// Returns the acquisition result.
func (b *Beads) MergeSlotAcquire(holder string, addWaiter bool) (*MergeSlotStatus, error) {
	if holder == "" {
		holder = b.getActor()
	}

	issue, err := b.getMergeSlotBead()
	if err != nil {
		return nil, fmt.Errorf("acquiring merge slot: %w", err)
	}

	data, err := parseMergeSlotData(issue)
	if err != nil {
		return nil, err
	}

	if data.Holder != "" && data.Holder != holder {
		if addWaiter {
			alreadyWaiting := false
			for _, w := range data.Waiters {
				if w == holder {
					alreadyWaiting = true
					break
				}
			}
			if !alreadyWaiting {
				data.Waiters = append(data.Waiters, holder)
				if err := b.writeMergeSlotData(issue.ID, data); err != nil {
					return nil, fmt.Errorf("recording merge slot waiter: %w", err)
				}
			}
		}
		return &MergeSlotStatus{
			ID:      issue.ID,
			Holder:  data.Holder,
			Waiters: data.Waiters,
		}, nil
	}

	data.Holder = holder
	data.Waiters = removeMergeSlotWaiter(data.Waiters, holder)
	if err := b.writeMergeSlotData(issue.ID, data); err != nil {
		return nil, fmt.Errorf("acquiring merge slot: %w", err)
	}

	return &MergeSlotStatus{
		ID:        issue.ID,
		Available: false,
		Holder:    holder,
		Waiters:   data.Waiters,
	}, nil
}

// MergeSlotAcquireLease atomically records the owner token and pre-push
// evidence when acquiring the merge slot for a target-branch push.
func (b *Beads) MergeSlotAcquireLease(holder string, addWaiter bool, lease MergeLease) (*MergeSlotStatus, error) {
	if holder == "" || lease.OwnerToken != holder || lease.Phase != MergeLeasePhaseAcquired {
		return nil, fmt.Errorf("acquiring merge lease: invalid owner token or initial phase")
	}
	issue, err := b.getMergeSlotBead()
	if err != nil {
		return nil, fmt.Errorf("acquiring merge lease: %w", err)
	}
	data, err := parseMergeSlotData(issue)
	if err != nil {
		return nil, err
	}
	if data.Holder != "" && data.Holder != holder {
		if addWaiter {
			found := false
			for _, waiter := range data.Waiters {
				found = found || waiter == holder
			}
			if !found {
				data.Waiters = append(data.Waiters, holder)
				if err := b.writeMergeSlotData(issue.ID, data); err != nil {
					return nil, err
				}
			}
		}
		return &MergeSlotStatus{ID: issue.ID, Holder: data.Holder, Waiters: data.Waiters, Lease: data.Lease}, nil
	}
	if data.Holder == holder {
		if data.Lease == nil || data.Lease.OwnerToken != holder {
			return nil, fmt.Errorf("acquiring merge lease: holder %q has no matching persisted lease", holder)
		}
		return mergeSlotStatusFromIssue(issue)
	}
	data.Holder = holder
	leaseCopy := lease
	leaseCopy.MRIDs = append([]string(nil), lease.MRIDs...)
	leaseCopy.SubmittedSHAs = append([]string(nil), lease.SubmittedSHAs...)
	data.Lease = &leaseCopy
	data.Waiters = removeMergeSlotWaiter(data.Waiters, holder)
	if err := b.writeMergeSlotData(issue.ID, data); err != nil {
		return nil, fmt.Errorf("acquiring merge lease: %w", err)
	}
	return &MergeSlotStatus{ID: issue.ID, Holder: holder, Waiters: data.Waiters, Lease: data.Lease}, nil
}

// MergeSlotTransition advances a persisted lease after comparing the exact
// owner token. Phase regressions and skipped phases fail closed.
func (b *Beads) MergeSlotTransition(holder string, phase MergeLeasePhase) error {
	issue, err := b.getMergeSlotBead()
	if err != nil {
		return fmt.Errorf("transitioning merge lease: %w", err)
	}
	data, err := parseMergeSlotData(issue)
	if err != nil {
		return err
	}
	if err := transitionMergeSlotData(&data, holder, phase); err != nil {
		return err
	}
	if err := b.writeMergeSlotData(issue.ID, data); err != nil {
		return fmt.Errorf("transitioning merge lease: %w", err)
	}
	return nil
}

// MergeSlotRelease releases the merge slot after conflict resolution completes.
// If holder is provided, it verifies the slot is held by that holder before releasing.
func (b *Beads) MergeSlotRelease(holder string) error {
	issue, err := b.getMergeSlotBead()
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil // Nothing to release
		}
		return fmt.Errorf("releasing merge slot: %w", err)
	}

	data, err := parseMergeSlotData(issue)
	if err != nil {
		return err
	}

	if data.Holder == "" {
		return nil // Already available
	}
	// Clear holder; promote first waiter if any.
	var newHolder string
	var remainingWaiters []string
	if len(data.Waiters) > 0 {
		newHolder = data.Waiters[0]
		remainingWaiters = data.Waiters[1:]
	}

	if err := releaseMergeSlotData(&data, holder); err != nil {
		return err
	}
	data.Holder = newHolder
	data.Waiters = remainingWaiters
	if err := b.writeMergeSlotData(issue.ID, data); err != nil {
		return fmt.Errorf("releasing merge slot: %w", err)
	}

	return nil
}

func transitionMergeSlotData(data *mergeSlotData, holder string, phase MergeLeasePhase) error {
	if data == nil || data.Lease == nil {
		return fmt.Errorf("merge lease transition failed: no persisted lease")
	}
	if holder == "" || data.Holder != holder || data.Lease.OwnerToken != holder {
		return fmt.Errorf("merge lease transition failed: held by %q with owner %q, not %q",
			data.Holder, data.Lease.OwnerToken, holder)
	}
	order := map[MergeLeasePhase]int{
		MergeLeasePhaseAcquired:          0,
		MergeLeasePhaseMutationPossible:  1,
		MergeLeasePhaseProofEstablished:  2,
		MergeLeasePhaseLifecycleComplete: 3,
	}
	current, currentOK := order[data.Lease.Phase]
	next, nextOK := order[phase]
	if !currentOK || !nextOK || next < current || next > current+1 {
		return fmt.Errorf("merge lease transition failed: invalid phase transition %q -> %q", data.Lease.Phase, phase)
	}
	data.Lease.Phase = phase
	return nil
}

func releaseMergeSlotData(data *mergeSlotData, holder string) error {
	if data == nil || data.Holder == "" {
		return nil
	}
	if holder == "" || data.Holder != holder {
		return fmt.Errorf("slot release failed: held by %q, not %q", data.Holder, holder)
	}
	if data.Lease != nil && data.Lease.OwnerToken != holder {
		return fmt.Errorf("slot release failed: lease owned by %q, not %q", data.Lease.OwnerToken, holder)
	}
	data.Holder = ""
	data.Lease = nil
	return nil
}

func removeMergeSlotWaiter(waiters []string, holder string) []string {
	filtered := waiters[:0]
	for _, waiter := range waiters {
		if waiter != holder {
			filtered = append(filtered, waiter)
		}
	}
	return filtered
}

func (b *Beads) writeMergeSlotData(id string, data mergeSlotData) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode merge slot %s: %w", id, err)
	}
	desc := string(raw)
	return b.Update(id, UpdateOptions{Description: &desc})
}

// MergeSlotEnsureExists creates the merge slot if it doesn't exist.
// This is idempotent - safe to call multiple times.
func (b *Beads) MergeSlotEnsureExists() (string, error) {
	// Check if slot exists first
	status, err := b.MergeSlotCheck()
	if err != nil {
		return "", err
	}

	if status.Error == "not found" {
		// Create it
		return b.MergeSlotCreate()
	}

	return status.ID, nil
}
