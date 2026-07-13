// Package refinery provides the merge queue processing agent.
//
// ZFC-compliant: Running state is derived from tmux sessions, not stored in files.
// Merge queue is derived from beads merge-request issues.
package refinery

import (
	"errors"
	"fmt"
	"time"
)

// MergeRequest represents a branch waiting to be merged.
type MergeRequest struct {
	// ID is a unique identifier for this merge request.
	ID string `json:"id"`

	// Branch is the source branch name (e.g., "polecat/Toast/gt-abc").
	Branch string `json:"branch"`

	// Worker is the polecat that created this branch.
	Worker string `json:"worker"`

	// AgentBead is the town-level agent bead that owns active_mr for this MR.
	AgentBead string `json:"agent_bead,omitempty"`

	// IssueID is the beads issue being worked on.
	IssueID string `json:"issue_id"`

	// SwarmID is the swarm this work belongs to (if any).
	SwarmID string `json:"swarm_id,omitempty"`

	// TargetBranch is where this should merge (usually integration or main).
	TargetBranch string `json:"target_branch"`

	// CommitSHA is the source branch tip submitted to the queue.
	CommitSHA string `json:"commit_sha,omitempty"`

	// PRURL/PRNumber record the authoritative PR identity when available.
	PRURL    string `json:"pr_url,omitempty"`
	PRNumber int    `json:"pr_number,omitempty"`

	// MergeCommit is the SHA that was pushed to the target branch after merge.
	MergeCommit string `json:"merge_commit,omitempty"`

	// CreatedAt is when the MR was queued.
	CreatedAt time.Time `json:"created_at"`

	// Status is the current status of the merge request.
	Status MRStatus `json:"status"`

	// CloseReason indicates why the MR was closed (only set when Status=closed).
	CloseReason CloseReason `json:"close_reason,omitempty"`

	// Error contains error details if the MR failed.
	Error string `json:"error,omitempty"`
}

// MRStatus represents the status of a merge request.
// Uses beads-style statuses for consistency with the issue tracking system.
type MRStatus string

const (
	// MROpen means the MR is waiting to be processed or needs rework.
	MROpen MRStatus = "open"

	// MRInProgress means the MR is currently being merged by the Engineer.
	MRInProgress MRStatus = "in_progress"

	// MRClosed means the MR processing is complete (merged, rejected, etc).
	MRClosed MRStatus = "closed"
)

// Extended MR statuses for the v2 state machine.
// These are stored in the MR bead's checkpoint/claim fields, not in the
// beads status field (which remains open/in_progress/closed for compatibility).
type MRPhase string

const (
	// MRPhaseReady means the MR is queued and available for claiming.
	MRPhaseReady MRPhase = "ready"

	// MRPhaseClaimed means a refinery instance has claimed the MR for processing.
	MRPhaseClaimed MRPhase = "claimed"

	// MRPhasePreparing means quality gates are running (rebase, setup, tests).
	MRPhasePreparing MRPhase = "preparing"

	// MRPhasePrepared means quality gates completed (may have failed — needs diagnosis).
	MRPhasePrepared MRPhase = "prepared"

	// MRPhaseMerging means the merge is in progress (ff-merge + push).
	MRPhaseMerging MRPhase = "merging"

	// MRPhaseMerged means the MR was successfully merged to target.
	MRPhaseMerged MRPhase = "merged"

	// MRPhaseRejected means the MR was rejected after diagnosis.
	MRPhaseRejected MRPhase = "rejected"

	// MRPhaseFailed means a transient error occurred (eligible for retry).
	MRPhaseFailed MRPhase = "failed"
)

// ValidPhaseTransitions defines the allowed state transitions for MR phases.
var ValidPhaseTransitions = map[MRPhase][]MRPhase{
	MRPhaseReady:     {MRPhaseClaimed},
	MRPhaseClaimed:   {MRPhasePreparing, MRPhaseReady},
	MRPhasePreparing: {MRPhasePrepared, MRPhaseFailed},
	MRPhasePrepared:  {MRPhaseMerging, MRPhaseRejected, MRPhaseReady},
	MRPhaseMerging:   {MRPhaseMerged, MRPhaseFailed},
	MRPhaseFailed:    {MRPhaseReady},
	// Terminal states: MRPhaseMerged, MRPhaseRejected (no transitions out)
}

// ValidatePhaseTransition checks if a phase transition is allowed.
func ValidatePhaseTransition(from, to MRPhase) error {
	if from == to {
		return nil
	}
	allowed, ok := ValidPhaseTransitions[from]
	if !ok {
		return fmt.Errorf("%w: %s is a terminal state", ErrInvalidTransition, from)
	}
	for _, target := range allowed {
		if target == to {
			return nil
		}
	}
	return fmt.Errorf("%w: %s → %s is not allowed", ErrInvalidTransition, from, to)
}

// DefaultClaimTTLMinutes is the default TTL for MR claims.
const DefaultClaimTTLMinutes = 30

// CloseReason indicates why a merge request was closed.
type CloseReason string

const (
	// CloseReasonMerged means the MR was successfully merged.
	CloseReasonMerged CloseReason = "merged"

	// CloseReasonRejected means the MR was manually rejected.
	CloseReasonRejected CloseReason = "rejected"

	// CloseReasonConflict means the MR had unresolvable conflicts.
	CloseReasonConflict CloseReason = "conflict"

	// CloseReasonSuperseded means the MR was replaced by another.
	CloseReasonSuperseded CloseReason = "superseded"
)

// QueueItem represents an item in the merge queue for display.
type QueueItem struct {
	Position int           `json:"position"`
	MR       *MergeRequest `json:"mr"`
	Age      string        `json:"age"`
}

// State transition errors.
var (
	// ErrInvalidTransition is returned when a state transition is not allowed.
	ErrInvalidTransition = errors.New("invalid state transition")

	// ErrClosedImmutable is returned when attempting to change a closed MR.
	ErrClosedImmutable = errors.New("closed merge requests are immutable")
)

// ValidateTransition checks if a state transition from -> to is valid.
//
// Valid transitions:
//   - open → in_progress (Engineer claims MR)
//   - in_progress → closed (merge success or rejection)
//   - in_progress → open (failure, reassign to worker)
//   - open → closed (manual rejection)
//
// Invalid:
//   - closed → anything (immutable once closed)
func ValidateTransition(from, to MRStatus) error {
	// Same state is always valid (no-op)
	if from == to {
		return nil
	}

	// Closed is immutable - cannot transition to anything else
	if from == MRClosed {
		return fmt.Errorf("%w: cannot change status from closed", ErrClosedImmutable)
	}

	// Check valid transitions
	switch from {
	case MROpen:
		// open → in_progress: Engineer claims MR
		// open → closed: manual rejection
		if to == MRInProgress || to == MRClosed {
			return nil
		}
	case MRInProgress:
		// in_progress → closed: merge success or rejection
		// in_progress → open: failure, reassign to worker
		if to == MRClosed || to == MROpen {
			return nil
		}
	}

	return fmt.Errorf("%w: %s → %s is not allowed", ErrInvalidTransition, from, to)
}

// SetStatus updates the MR status after validating the transition.
// Returns an error if the transition is not allowed.
func (mr *MergeRequest) SetStatus(newStatus MRStatus) error {
	if err := ValidateTransition(mr.Status, newStatus); err != nil {
		return err
	}
	mr.Status = newStatus
	return nil
}

// Close closes the MR with the given reason after validating the transition.
// Returns an error if the MR cannot be closed from its current state.
// Once closed, an MR cannot be closed again (even with a different reason).
func (mr *MergeRequest) Close(reason CloseReason) error {
	// Closed MRs are immutable - cannot be closed again
	if mr.Status == MRClosed {
		return fmt.Errorf("%w: MR is already closed", ErrClosedImmutable)
	}
	if err := ValidateTransition(mr.Status, MRClosed); err != nil {
		return err
	}
	mr.Status = MRClosed
	mr.CloseReason = reason
	return nil
}

// Reopen reopens a failed MR (transitions from in_progress back to open).
// Returns an error if the transition is not allowed.
func (mr *MergeRequest) Reopen() error {
	if mr.Status != MRInProgress {
		return fmt.Errorf("%w: can only reopen from in_progress, current status is %s",
			ErrInvalidTransition, mr.Status)
	}
	mr.Status = MROpen
	mr.CloseReason = "" // Clear any previous close reason
	return nil
}

// Claim transitions the MR from open to in_progress (Engineer claims it).
// Returns an error if the transition is not allowed.
func (mr *MergeRequest) Claim() error {
	if mr.Status != MROpen {
		return fmt.Errorf("%w: can only claim from open, current status is %s",
			ErrInvalidTransition, mr.Status)
	}
	mr.Status = MRInProgress
	return nil
}

// IsClosed returns true if the MR is in a closed state.
func (mr *MergeRequest) IsClosed() bool {
	return mr.Status == MRClosed
}

// FailureType categorizes merge failures for appropriate handling.
type FailureType string

const (
	// FailureNone indicates no failure (success).
	FailureNone FailureType = ""

	// FailureConflict indicates merge conflicts with target branch.
	FailureConflict FailureType = "conflict"

	// FailureTestsFail indicates tests failed after merge.
	FailureTestsFail FailureType = "tests_fail"

	// FailureBuildFail indicates build failed after merge.
	FailureBuildFail FailureType = "build_fail"

	// FailureFlakyTest indicates a potentially flaky test failure (may retry).
	FailureFlakyTest FailureType = "flaky_test"

	// FailurePushFail indicates push to remote failed.
	FailurePushFail FailureType = "push_fail"

	// FailureFetch indicates fetch of source branch failed.
	FailureFetch FailureType = "fetch_fail"

	// FailureCheckout indicates checkout of target branch failed.
	FailureCheckout FailureType = "checkout_fail"
)

// FailureLabel returns the beads label for this failure type.
func (f FailureType) FailureLabel() string {
	switch f {
	case FailureConflict:
		return "needs-rebase"
	case FailureTestsFail, FailureBuildFail, FailureFlakyTest:
		return "needs-fix"
	case FailurePushFail:
		return "needs-retry"
	default:
		return ""
	}
}

// ShouldAssignToWorker returns true if this failure should be assigned back to the worker.
func (f FailureType) ShouldAssignToWorker() bool {
	switch f {
	case FailureConflict, FailureTestsFail, FailureBuildFail, FailureFlakyTest:
		return true
	default:
		return false
	}
}

// IsOpen returns true if the MR is in an open state (waiting for processing).
func (mr *MergeRequest) IsOpen() bool {
	return mr.Status == MROpen
}

// IsInProgress returns true if the MR is currently being processed.
func (mr *MergeRequest) IsInProgress() bool {
	return mr.Status == MRInProgress
}
