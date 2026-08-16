package protocol

import (
	"fmt"
	"io"
	"os"

	"github.com/jonbaldie/gastown/internal/mail"
)

// DefaultRefineryHandler provides the default implementation for Refinery protocol handlers.
// It receives MERGE_READY messages from the Witness and acknowledges verified work.
// Note: The Refinery now queries beads directly for merge requests (via ReadyWithType).
type DefaultRefineryHandler struct {
	// Rig is the name of the rig this refinery processes.
	Rig string

	// WorkDir is the working directory for operations.
	WorkDir string

	// Router is used to send mail messages.
	Router *mail.Router

	// Output is where to write status messages.
	Output io.Writer
}

// NewRefineryHandler creates a new DefaultRefineryHandler.
func NewRefineryHandler(rig, workDir string) *DefaultRefineryHandler {
	return &DefaultRefineryHandler{
		Rig:     rig,
		WorkDir: workDir,
		Router:  mail.NewRouter(workDir),
		Output:  os.Stdout,
	}
}

// SetOutput sets the output writer for status messages.
func (h *DefaultRefineryHandler) SetOutput(w io.Writer) {
	h.Output = w
}

// HandleMergeReady handles a MERGE_READY message from Witness.
// When a polecat's work is verified and ready, the Refinery acknowledges receipt.
//
// Belt-and-suspenders: if the message indicates this is from an owned+direct
// convoy, the Refinery skips processing. These MRs shouldn't exist (gt done
// skips MR creation for owned+direct), but this guards against edge cases.
//
// NOTE: The merge-request bead is created by `gt done`, so we no longer need
// to add to the mrqueue here. The Refinery queries beads directly for ready MRs.
func (h *DefaultRefineryHandler) HandleMergeReady(payload *MergeReadyPayload) error {
	_, _ = fmt.Fprintf(h.Output, "[Refinery] MERGE_READY received for polecat %s\n", payload.Polecat)
	_, _ = fmt.Fprintf(h.Output, "  Branch: %s\n", payload.Branch)
	_, _ = fmt.Fprintf(h.Output, "  Issue: %s\n", payload.Issue)
	_, _ = fmt.Fprintf(h.Output, "  Verified: %s\n", payload.Verified)

	// Belt-and-suspenders: check if this is from an owned+direct convoy.
	// The Verified field may contain "owned+direct" marker from witness.
	if payload.Verified == "owned+direct: skip merge" {
		_, _ = fmt.Fprintf(h.Output, "[Refinery] ⚠ Owned+direct convoy — skipping merge (belt-and-suspenders)\n")
		return nil
	}

	// The merge-request bead is created by `gt done` with gt:merge-request label.
	// The Refinery queries beads directly via ReadyWithType("merge-request").
	// No need to add to mrqueue - that was a duplicate tracking file.
	_, _ = fmt.Fprintf(h.Output, "[Refinery] ✓ Work verified - Refinery will pick up MR via beads query\n")

	return nil
}

// SendMerged sends a MERGED message to the Witness.
// Called by the Refinery after successfully merging a branch.
func (h *DefaultRefineryHandler) SendMerged(polecat, branch, issue, targetBranch, mergeCommit string) error {
	msg := NewMergedMessage(h.Rig, polecat, branch, issue, targetBranch, mergeCommit)
	return h.Router.Send(msg)
}

// SendMergeFailed sends a MERGE_FAILED message to the Witness.
// LEGACY: Prefer SendFixNeeded which sends directly to the polecat.
// Called by the Refinery when a merge fails.
func (h *DefaultRefineryHandler) SendMergeFailed(polecat, branch, issue, targetBranch, failureType, errorMsg string) error {
	msg := NewMergeFailedMessage(h.Rig, polecat, branch, issue, targetBranch, failureType, errorMsg)
	return h.Router.Send(msg)
}

// SendFixNeeded sends a FIX_NEEDED message directly to the Polecat.
// Called by the Refinery when a merge fails due to quality checks/tests.
// The polecat fixes the code in-place and resubmits without losing context.
func (h *DefaultRefineryHandler) SendFixNeeded(polecat, branch, issue, targetBranch, failureType, errorMsg, mrBeadID string, attemptNumber int) error {
	msg := NewFixNeededMessage(h.Rig, polecat, branch, issue, targetBranch, failureType, errorMsg, mrBeadID, attemptNumber)
	return h.Router.Send(msg)
}

// SendReworkRequest sends a REWORK_REQUEST message to the Witness.
// Called by the Refinery when a branch has conflicts.
func (h *DefaultRefineryHandler) SendReworkRequest(polecat, branch, issue, targetBranch string, conflictFiles []string) error {
	msg := NewReworkRequestMessage(h.Rig, polecat, branch, issue, targetBranch, conflictFiles)
	return h.Router.Send(msg)
}

// NotifyMergeOutcome is a convenience method that sends the appropriate message
// based on the merge result.
type MergeOutcome struct {
	// Success indicates whether the merge was successful.
	Success bool

	// Conflict indicates the failure was due to conflicts (needs rebase).
	Conflict bool

	// FailureType categorizes the failure (e.g., "tests", "build").
	FailureType string

	// Error is the error message if the merge failed.
	Error string

	// MergeCommit is the SHA of the merge commit on success.
	MergeCommit string

	// ConflictFiles lists files with conflicts (if Conflict is true).
	ConflictFiles []string

	// MRBeadID is the merge-request bead ID (for FIX_NEEDED tracking).
	MRBeadID string

	// AttemptNumber tracks how many fix attempts have been made.
	AttemptNumber int
}

// NotifyMergeOutcome sends the appropriate protocol message based on the outcome.
// For failures (non-conflict), sends FIX_NEEDED directly to the polecat
// so it can fix the code in-place without context loss.
func (h *DefaultRefineryHandler) NotifyMergeOutcome(polecat, branch, issue, targetBranch string, outcome MergeOutcome) error {
	if outcome.Success {
		return h.SendMerged(polecat, branch, issue, targetBranch, outcome.MergeCommit)
	}

	if outcome.Conflict {
		return h.SendReworkRequest(polecat, branch, issue, targetBranch, outcome.ConflictFiles)
	}

	// Send FIX_NEEDED directly to polecat (event-driven lifecycle).
	// The polecat stays alive and fixes in-place.
	return h.SendFixNeeded(polecat, branch, issue, targetBranch, outcome.FailureType, outcome.Error, outcome.MRBeadID, outcome.AttemptNumber)
}

// Ensure DefaultRefineryHandler implements RefineryHandler.
var _ RefineryHandler = (*DefaultRefineryHandler)(nil)
