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
	ID        string   `json:"id"`
	Available bool     `json:"available"`
	Holder    string   `json:"holder,omitempty"`
	Waiters   []string `json:"waiters,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// mergeSlotData is the JSON structure stored in the merge slot bead's Description.
type mergeSlotData struct {
	Holder  string   `json:"holder"`
	Waiters []string `json:"waiters,omitempty"`
}

// parseMergeSlotData decodes the merge slot state from a bead's Description field.
func parseMergeSlotData(issue *Issue) mergeSlotData {
	if issue.Description == "" {
		return mergeSlotData{}
	}
	var data mergeSlotData
	_ = json.Unmarshal([]byte(issue.Description), &data)
	return data
}

// mergeSlotStatusFromIssue builds a MergeSlotStatus from a bead issue.
func mergeSlotStatusFromIssue(issue *Issue) *MergeSlotStatus {
	data := parseMergeSlotData(issue)
	return &MergeSlotStatus{
		ID:        issue.ID,
		Available: data.Holder == "",
		Holder:    data.Holder,
		Waiters:   data.Waiters,
	}
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
	return mergeSlotStatusFromIssue(issue), nil
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

	data := parseMergeSlotData(issue)

	if data.Holder != "" && data.Holder != holder {
		// Slot is held by someone else.
		if addWaiter {
			// Add to waiters list if not already present.
			alreadyWaiting := false
			for _, w := range data.Waiters {
				if w == holder {
					alreadyWaiting = true
					break
				}
			}
			if !alreadyWaiting {
				data.Waiters = append(data.Waiters, holder)
				newDesc, _ := json.Marshal(data)
				desc := string(newDesc)
				_ = b.Update(issue.ID, UpdateOptions{Description: &desc})
			}
		}
		return &MergeSlotStatus{
			ID:      issue.ID,
			Holder:  data.Holder,
			Waiters: data.Waiters,
		}, nil
	}

	// Slot is available or we already hold it — acquire.
	data.Holder = holder
	// Remove from waiters if present.
	filtered := data.Waiters[:0]
	for _, w := range data.Waiters {
		if w != holder {
			filtered = append(filtered, w)
		}
	}
	data.Waiters = filtered

	newDesc, _ := json.Marshal(data)
	desc := string(newDesc)
	if err := b.Update(issue.ID, UpdateOptions{Description: &desc}); err != nil {
		return nil, fmt.Errorf("acquiring merge slot: %w", err)
	}

	return &MergeSlotStatus{
		ID:        issue.ID,
		Available: false,
		Holder:    holder,
		Waiters:   data.Waiters,
	}, nil
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

	data := parseMergeSlotData(issue)

	if data.Holder == "" {
		return nil // Already available
	}
	if holder != "" && data.Holder != holder {
		return fmt.Errorf("slot release failed: held by %q, not %q", data.Holder, holder)
	}

	// Clear holder; promote first waiter if any.
	var newHolder string
	var remainingWaiters []string
	if len(data.Waiters) > 0 {
		newHolder = data.Waiters[0]
		remainingWaiters = data.Waiters[1:]
	}

	newData := mergeSlotData{Holder: newHolder, Waiters: remainingWaiters}
	newDesc, _ := json.Marshal(newData)
	desc := string(newDesc)

	if err := b.Update(issue.ID, UpdateOptions{Description: &desc}); err != nil {
		return fmt.Errorf("releasing merge slot: %w", err)
	}

	return nil
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
