package protocol

import (
	"fmt"
	"io"
	"os"

	"github.com/jonbaldie/gastown/internal/mail"
	"github.com/jonbaldie/gastown/internal/witness"
)

// DefaultWitnessHandler provides the default implementation for Witness protocol handlers.
// It receives messages from the Refinery about merge outcomes and takes appropriate action.
type DefaultWitnessHandler struct {
	// Rig is the name of the rig this witness manages.
	Rig string

	// WorkDir is the working directory for operations.
	WorkDir string

	// Router is used to send mail messages.
	Router *mail.Router

	// Output is where to write status messages.
	Output io.Writer
}

// NewWitnessHandler creates a new DefaultWitnessHandler.
func NewWitnessHandler(rig, workDir string) *DefaultWitnessHandler {
	return &DefaultWitnessHandler{
		Rig:     rig,
		WorkDir: workDir,
		Router:  mail.NewRouter(workDir),
		Output:  os.Stdout,
	}
}

// SetOutput sets the output writer for status messages.
func (h *DefaultWitnessHandler) SetOutput(w io.Writer) {
	h.Output = w
}

// HandleMerged handles a MERGED message from Refinery.
// When a branch is successfully merged, the Witness:
// 1. Logs the success
// 2. Notifies the polecat of successful merge
// 3. Initiates polecat cleanup (nuke worktree)
func (h *DefaultWitnessHandler) HandleMerged(payload *MergedPayload) error {
	_, _ = fmt.Fprintf(h.Output, "[Witness] MERGED received for polecat %s\n", payload.Polecat)
	_, _ = fmt.Fprintf(h.Output, "  Branch: %s\n", payload.Branch)
	_, _ = fmt.Fprintf(h.Output, "  Issue: %s\n", payload.Issue)
	_, _ = fmt.Fprintf(h.Output, "  Merged to: %s\n", payload.TargetBranch)
	if payload.MergeCommit != "" {
		_, _ = fmt.Fprintf(h.Output, "  Commit: %s\n", payload.MergeCommit)
	}

	// Notify the polecat about successful merge
	if err := h.notifyPolecatMerged(payload); err != nil {
		fmt.Fprintf(h.Output, "[Witness] Warning: failed to notify polecat: %v\n", err)
		// Continue - notification is best-effort
	}

	// Initiate polecat cleanup using AutoNukeIfClean
	// This verifies cleanup_status before nuking to prevent work loss.
	nukeResult := witness.AutoNukeIfClean(h.WorkDir, h.Rig, payload.Polecat)
	if nukeResult.Nuked {
		fmt.Fprintf(h.Output, "[Witness] ✓ Auto-nuked polecat %s: %s\n", payload.Polecat, nukeResult.Reason)
	} else if nukeResult.Skipped {
		fmt.Fprintf(h.Output, "[Witness] ⚠ Cleanup skipped for %s: %s\n", payload.Polecat, nukeResult.Reason)
	} else if nukeResult.Error != nil {
		fmt.Fprintf(h.Output, "[Witness] ✗ Cleanup failed for %s: %v\n", payload.Polecat, nukeResult.Error)
		return fmt.Errorf("cleanup failed for polecat %s: %w", payload.Polecat, nukeResult.Error)
	} else {
		fmt.Fprintf(h.Output, "[Witness] ✓ Polecat %s work merged, cleanup can proceed\n", payload.Polecat)
	}

	return nil
}

// HandleMergeFailed handles a MERGE_FAILED message from Refinery.
// When a merge fails (tests, build, etc.), the Witness:
// 1. Logs the failure
// 2. Notifies the polecat about the failure and required fixes
// 3. Updates the polecat's state to indicate rework needed
func (h *DefaultWitnessHandler) HandleMergeFailed(payload *MergeFailedPayload) error {
	fmt.Fprintf(h.Output, "[Witness] MERGE_FAILED received for polecat %s\n", payload.Polecat)
	fmt.Fprintf(h.Output, "  Branch: %s\n", payload.Branch)
	fmt.Fprintf(h.Output, "  Issue: %s\n", payload.Issue)
	fmt.Fprintf(h.Output, "  Failure type: %s\n", payload.FailureType)
	fmt.Fprintf(h.Output, "  Error: %s\n", payload.Error)

	// Notify the polecat about the failure
	if err := h.notifyPolecatFailed(payload); err != nil {
		fmt.Fprintf(h.Output, "[Witness] Warning: failed to notify polecat: %v\n", err)
		// Continue - notification is best-effort, no cleanup to fail
	}

	fmt.Fprintf(h.Output, "[Witness] ✗ Polecat %s merge failed, rework needed\n", payload.Polecat)

	return nil
}

// HandlePolecatDone handles a POLECAT_DONE notification.
// When a polecat signals completion, the Witness decides whether to register
// the work for merge processing or skip it (for owned+direct convoys).
//
// For standard convoys: the merge pipeline proceeds normally (MR bead exists,
// refinery will process it).
//
// For owned+direct convoys: the polecat already pushed to main and closed its
// issue. The witness skips merge flow registration — only cleanup remains.
func (h *DefaultWitnessHandler) HandlePolecatDone(payload *PolecatDonePayload) error {
	_, _ = fmt.Fprintf(h.Output, "[Witness] POLECAT_DONE received for polecat %s\n", payload.Polecat)
	_, _ = fmt.Fprintf(h.Output, "  Exit: %s\n", payload.ExitType)
	if payload.Issue != "" {
		_, _ = fmt.Fprintf(h.Output, "  Issue: %s\n", payload.Issue)
	}
	_, _ = fmt.Fprintf(h.Output, "  Branch: %s\n", payload.Branch)

	if payload.SkipMergeFlow() {
		_, _ = fmt.Fprintf(h.Output, "[Witness] ✓ Owned+direct convoy %s — merge flow skipped\n", payload.ConvoyID)
		_, _ = fmt.Fprintf(h.Output, "  Polecat already pushed to main. Proceeding with cleanup only.\n")

		// Initiate polecat cleanup (same as HandleMerged)
		nukeResult := witness.AutoNukeIfClean(h.WorkDir, h.Rig, payload.Polecat)
		if nukeResult.Nuked {
			fmt.Fprintf(h.Output, "[Witness] ✓ Auto-nuked polecat %s: %s\n", payload.Polecat, nukeResult.Reason)
		} else if nukeResult.Skipped {
			fmt.Fprintf(h.Output, "[Witness] ⚠ Cleanup skipped for %s: %s\n", payload.Polecat, nukeResult.Reason)
		} else if nukeResult.Error != nil {
			fmt.Fprintf(h.Output, "[Witness] ✗ Cleanup failed for %s: %v\n", payload.Polecat, nukeResult.Error)
		}

		return nil
	}

	// Standard flow: log receipt, merge pipeline will handle the rest
	if payload.MR != "" {
		_, _ = fmt.Fprintf(h.Output, "  MR: %s\n", payload.MR)
	}
	_, _ = fmt.Fprintf(h.Output, "[Witness] ✓ Standard flow — Refinery will process MR\n")

	return nil
}

// HandleReworkRequest handles a REWORK_REQUEST message from Refinery.
// When a branch has conflicts requiring rebase, the Witness:
// 1. Logs the conflict
// 2. Notifies the polecat with rebase instructions
// 3. Updates the polecat's state to indicate rebase needed
func (h *DefaultWitnessHandler) HandleReworkRequest(payload *ReworkRequestPayload) error {
	fmt.Fprintf(h.Output, "[Witness] REWORK_REQUEST received for polecat %s\n", payload.Polecat)
	fmt.Fprintf(h.Output, "  Branch: %s\n", payload.Branch)
	fmt.Fprintf(h.Output, "  Issue: %s\n", payload.Issue)
	fmt.Fprintf(h.Output, "  Target: %s\n", payload.TargetBranch)
	if len(payload.ConflictFiles) > 0 {
		fmt.Fprintf(h.Output, "  Conflicts in: %v\n", payload.ConflictFiles)
	}

	// Notify the polecat about the rebase requirement
	if err := h.notifyPolecatRebase(payload); err != nil {
		fmt.Fprintf(h.Output, "[Witness] Warning: failed to notify polecat: %v\n", err)
		// Continue - notification is best-effort, no cleanup to fail
	}

	fmt.Fprintf(h.Output, "[Witness] ⚠ Polecat %s needs to rebase onto %s\n", payload.Polecat, payload.TargetBranch)

	return nil
}

// notifyPolecatMerged sends a merge success notification to a polecat.
func (h *DefaultWitnessHandler) notifyPolecatMerged(payload *MergedPayload) error {
	msg := mail.NewMessage(
		fmt.Sprintf("%s/witness", h.Rig),
		fmt.Sprintf("%s/%s", h.Rig, payload.Polecat),
		"Work merged successfully",
		fmt.Sprintf(`Your work has been merged to %s.

Branch: %s
Issue: %s
Commit: %s

Thank you for your contribution! Your worktree will be cleaned up shortly.`,
			payload.TargetBranch,
			payload.Branch,
			payload.Issue,
			payload.MergeCommit,
		),
	)
	msg.Priority = mail.PriorityNormal

	return h.Router.Send(msg)
}

// notifyPolecatFailed sends a merge failure notification to a polecat.
func (h *DefaultWitnessHandler) notifyPolecatFailed(payload *MergeFailedPayload) error {
	msg := mail.NewMessage(
		fmt.Sprintf("%s/witness", h.Rig),
		fmt.Sprintf("%s/%s", h.Rig, payload.Polecat),
		fmt.Sprintf("Merge failed: %s", payload.FailureType),
		fmt.Sprintf(`Your merge request failed.

Branch: %s
Issue: %s
Failure: %s
Error: %s

Please fix the issue and resubmit your work with 'gt done'.`,
			payload.Branch,
			payload.Issue,
			payload.FailureType,
			payload.Error,
		),
	)
	msg.Priority = mail.PriorityHigh
	msg.Type = mail.TypeTask

	return h.Router.Send(msg)
}

// notifyPolecatRebase sends a rebase request notification to a polecat.
func (h *DefaultWitnessHandler) notifyPolecatRebase(payload *ReworkRequestPayload) error {
	conflictInfo := ""
	if len(payload.ConflictFiles) > 0 {
		conflictInfo = fmt.Sprintf("\nConflicting files:\n")
		for _, f := range payload.ConflictFiles {
			conflictInfo += fmt.Sprintf("  - %s\n", f)
		}
	}

	msg := mail.NewMessage(
		fmt.Sprintf("%s/witness", h.Rig),
		fmt.Sprintf("%s/%s", h.Rig, payload.Polecat),
		"Rebase required - merge conflict",
		fmt.Sprintf(`Your branch has conflicts with %s.

Branch: %s
Issue: %s
%s
Please rebase your changes:

  git fetch origin
  git rebase origin/%s
  # Resolve any conflicts
  git push -f

Then run 'gt done' to resubmit for merge.`,
			payload.TargetBranch,
			payload.Branch,
			payload.Issue,
			conflictInfo,
			payload.TargetBranch,
		),
	)
	msg.Priority = mail.PriorityHigh
	msg.Type = mail.TypeTask

	return h.Router.Send(msg)
}

// Ensure DefaultWitnessHandler implements WitnessHandler.
var _ WitnessHandler = (*DefaultWitnessHandler)(nil)
