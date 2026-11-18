// Package beads provides handoff bead operations for agent workflow management.
package beads

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/lock"
)

// Issue status constants kept as untyped strings for backward compatibility.
// The typed versions (IssueStatus) are in status.go.
const (
	// StatusPinned is the status for pinned beads that never get closed.
	StatusPinned = "pinned"

	// StatusHooked is the status for beads on an agent's hook (work assignment).
	StatusHooked = "hooked"
)

// HandoffBeadTitle returns the well-known title for a role's handoff bead.
func HandoffBeadTitle(role string) string {
	return role + " Handoff"
}

// FindHandoffBead finds the pinned handoff bead for a role by title.
// Returns nil if not found (not an error).
func (b *Beads) FindHandoffBead(role string) (*Issue, error) {
	issues, err := b.List(ListOptions{Status: StatusPinned, Priority: -1})
	if err != nil {
		return nil, fmt.Errorf("listing pinned issues: %w", err)
	}

	targetTitle := HandoffBeadTitle(role)
	for _, issue := range issues {
		if issue.Title == targetTitle {
			return issue, nil
		}
	}

	return nil, nil
}

// FindAllHandoffBeads fetches all pinned beads once and returns a map from
// role name to handoff bead. This avoids the N+1 subprocess problem where
// FindHandoffBead is called once per agent, each spawning a bd subprocess.
func (b *Beads) FindAllHandoffBeads() (map[string]*Issue, error) {
	issues, err := b.List(ListOptions{Status: StatusPinned, Priority: -1})
	if err != nil {
		return nil, fmt.Errorf("listing pinned issues: %w", err)
	}

	result := make(map[string]*Issue)
	for _, issue := range issues {
		// Handoff bead titles follow the pattern "<role> Handoff"
		if strings.HasSuffix(issue.Title, " Handoff") {
			role := strings.TrimSuffix(issue.Title, " Handoff")
			result[role] = issue
		}
	}

	return result, nil
}

// GetOrCreateHandoffBead returns the handoff bead for a role, creating it if needed.
func (b *Beads) GetOrCreateHandoffBead(role string) (*Issue, error) {
	// Check if it exists
	existing, err := b.FindHandoffBead(role)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	issue, err := b.Create(CreateOptions{
		Title:       HandoffBeadTitle(role),
		Labels:      []string{"gt:task"},
		Priority:    2,
		Description: "", // Empty until first handoff
		Actor:       role,
	})
	if err != nil {
		return nil, fmt.Errorf("creating handoff bead: %w", err)
	}

	// Update to pinned status. If this fails, clean up the orphaned bead
	// to prevent duplicates on retry (FindHandoffBead only searches pinned beads).
	status := StatusPinned
	if err := b.Update(issue.ID, UpdateOptions{Status: &status}); err != nil {
		// Best-effort cleanup — ignore delete error since pin failure is the real problem
		_ = b.CloseWithReason("orphaned: failed to pin", issue.ID)
		return nil, fmt.Errorf("setting handoff bead to pinned: %w", err)
	}

	// Re-fetch to get updated status. If this fails, the bead is already
	// created and pinned — a retry of GetOrCreateHandoffBead will find it.
	return b.Show(issue.ID)
}

// UpdateHandoffContent updates the handoff bead's description with new content.
func (b *Beads) UpdateHandoffContent(role, content string) error {
	issue, err := b.GetOrCreateHandoffBead(role)
	if err != nil {
		return err
	}

	return b.Update(issue.ID, UpdateOptions{Description: &content})
}

// ClearHandoffContent clears the handoff bead's description.
func (b *Beads) ClearHandoffContent(role string) error {
	issue, err := b.FindHandoffBead(role)
	if err != nil {
		return err
	}
	if issue == nil {
		return nil // Nothing to clear
	}

	empty := ""
	return b.Update(issue.ID, UpdateOptions{Description: &empty})
}

// ClearMailResult contains statistics from a ClearMail operation.
type ClearMailResult struct {
	Closed  int // Number of messages closed
	Cleared int // Number of pinned messages cleared (content removed)
}

// ClearMail closes or clears all open messages.
// Non-pinned messages are closed with the given reason.
// Pinned messages have their description cleared but remain open.
func (b *Beads) ClearMail(reason string) (*ClearMailResult, error) {
	// List all open messages
	issues, err := b.List(ListOptions{
		Status:   "open",
		Label:    "gt:message",
		Priority: -1,
	})
	if err != nil {
		return nil, fmt.Errorf("listing messages: %w", err)
	}

	result := &ClearMailResult{}

	// Separate pinned from non-pinned
	var toClose []string
	var toClear []*Issue

	for _, issue := range issues {
		if issue.Status == StatusPinned {
			toClear = append(toClear, issue)
		} else {
			toClose = append(toClose, issue.ID)
		}
	}

	// Close non-pinned messages in batch
	if len(toClose) > 0 {
		if err := b.CloseWithReason(reason, toClose...); err != nil {
			return nil, fmt.Errorf("closing messages: %w", err)
		}
		result.Closed = len(toClose)
	}

	// Clear pinned messages — continue on error so partial progress isn't lost
	empty := ""
	var clearErrs []error
	for _, issue := range toClear {
		if err := b.Update(issue.ID, UpdateOptions{Description: &empty}); err != nil {
			clearErrs = append(clearErrs, fmt.Errorf("clearing pinned message %s: %w", issue.ID, err))
			continue
		}
		result.Cleared++
	}

	if len(clearErrs) > 0 {
		return result, fmt.Errorf("partial failure clearing %d/%d pinned messages: %w",
			len(clearErrs), len(toClear), errors.Join(clearErrs...))
	}

	return result, nil
}

// CloseStaleHookedMailBeads closes any gt:message beads in status=hooked assigned
// to agentID. Called before creating a new handoff mail to prevent accumulation of
// stale beads across sessions. Returns the number of beads closed. (GH#3859)
func (b *Beads) CloseStaleHookedMailBeads(agentID string) (int, error) {
	hooked, err := b.List(ListOptions{
		Status:   StatusHooked,
		Label:    "gt:message",
		Assignee: agentID,
		Priority: -1,
	})
	if err != nil {
		return 0, fmt.Errorf("listing hooked mail beads: %w", err)
	}
	if len(hooked) == 0 {
		return 0, nil
	}
	ids := make([]string, len(hooked))
	for i, h := range hooked {
		ids[i] = h.ID
	}
	if err := b.ForceCloseWithReason("handoff: superseded by new session", ids...); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// lockBead acquires a cross-process advisory lock for a bead operation.
// Returns a cleanup function that releases the lock.
// Lock files are stored in <beadsDir>/locks/<beadID>.flock.
func (b *Beads) lockBead(beadID string) (func(), error) {
	locksDir := filepath.Join(b.getResolvedBeadsDir(), "locks")
	if err := os.MkdirAll(locksDir, 0755); err != nil {
		return nil, fmt.Errorf("creating locks directory: %w", err)
	}
	lockPath := filepath.Join(locksDir, beadID+".flock")
	return lock.FlockAcquire(lockPath)
}

// AttachMolecule attaches a molecule to a pinned bead by updating its description.
// The moleculeID is the root issue ID of the molecule to attach.
// Uses advisory file locking to prevent concurrent read-modify-write races.
// Returns the updated issue.
func (b *Beads) AttachMolecule(pinnedBeadID, moleculeID string) (*Issue, error) {
	// Acquire per-bead lock to serialize concurrent attach/detach operations
	unlock, err := b.lockBead(pinnedBeadID)
	if err != nil {
		return nil, fmt.Errorf("acquiring bead lock: %w", err)
	}
	defer unlock()

	// Fetch the pinned bead
	issue, err := b.Show(pinnedBeadID)
	if err != nil {
		return nil, fmt.Errorf("fetching pinned bead: %w", err)
	}

	// Only allow pinned beads (permanent records like role definitions)
	if issue.Status != StatusPinned {
		return nil, fmt.Errorf("issue %s is not pinned (status: %s)", pinnedBeadID, issue.Status)
	}

	// Build attachment fields with current timestamp
	fields := &AttachmentFields{
		AttachedMolecule: moleculeID,
		AttachedAt:       currentTimestamp(),
	}

	// Update description with attachment fields
	newDesc := SetAttachmentFields(issue, fields)

	// Update the issue
	if err := b.Update(pinnedBeadID, UpdateOptions{Description: &newDesc}); err != nil {
		return nil, fmt.Errorf("updating pinned bead: %w", err)
	}

	// Re-fetch to return updated state
	return b.Show(pinnedBeadID)
}

// DetachMolecule removes molecule attachment from a pinned bead.
// Uses advisory file locking to prevent concurrent read-modify-write races.
// Returns the updated issue.
func (b *Beads) DetachMolecule(pinnedBeadID string) (*Issue, error) {
	// Acquire per-bead lock to serialize concurrent attach/detach operations
	unlock, err := b.lockBead(pinnedBeadID)
	if err != nil {
		return nil, fmt.Errorf("acquiring bead lock: %w", err)
	}
	defer unlock()

	// Fetch the pinned bead
	issue, err := b.Show(pinnedBeadID)
	if err != nil {
		return nil, fmt.Errorf("fetching pinned bead: %w", err)
	}

	// Check if there's anything to detach
	if ParseAttachmentFields(issue) == nil {
		return issue, nil // Nothing to detach
	}

	// Clear attachment fields by passing nil
	newDesc := SetAttachmentFields(issue, nil)

	// Update the issue
	if err := b.Update(pinnedBeadID, UpdateOptions{Description: &newDesc}); err != nil {
		return nil, fmt.Errorf("updating pinned bead: %w", err)
	}

	// Re-fetch to return updated state
	return b.Show(pinnedBeadID)
}

// RepairMissingMolecule removes workflow state for an attachment whose wisp was
// already deleted, while preserving completion routing such as no_merge,
// review_only, convoy ownership, and merge strategy. This prevents recovery
// from accidentally rerunning or enqueueing external-PR work.
func (b *Beads) RepairMissingMolecule(beadID string) (*Issue, error) {
	unlock, err := b.lockBead(beadID)
	if err != nil {
		return nil, fmt.Errorf("acquiring bead lock: %w", err)
	}
	defer unlock()

	issue, err := b.Show(beadID)
	if err != nil {
		return nil, fmt.Errorf("fetching bead: %w", err)
	}
	fields := ParseAttachmentFields(issue)
	if fields == nil || fields.AttachedMolecule == "" {
		return issue, nil
	}

	clearMissingMoleculeFields(fields)
	newDesc := SetAttachmentFields(issue, fields)
	if err := b.Update(beadID, UpdateOptions{Description: &newDesc}); err != nil {
		return nil, fmt.Errorf("repairing missing molecule metadata: %w", err)
	}
	return b.Show(beadID)
}

func clearMissingMoleculeFields(fields *AttachmentFields) {
	fields.AttachedMolecule = ""
	fields.AttachedFormula = ""
	fields.AttachedAt = ""
	fields.AttachedArgs = ""
	fields.AttachedVars = nil
	fields.FormulaVars = ""
}

// GetAttachment returns the attachment fields from a pinned bead.
// Returns nil if no molecule is attached.
func (b *Beads) GetAttachment(pinnedBeadID string) (*AttachmentFields, error) {
	issue, err := b.Show(pinnedBeadID)
	if err != nil {
		return nil, err
	}

	return ParseAttachmentFields(issue), nil
}

// currentTimestamp returns the current time in ISO 8601 format.
func currentTimestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}
