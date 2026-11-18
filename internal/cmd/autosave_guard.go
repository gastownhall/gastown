package cmd

import (
	"errors"
	"fmt"

	"github.com/steveyegge/gastown/internal/autosave"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/style"
)

// errAutosaveRefused marks the refusal so the caller can distinguish "declined
// on purpose" (abort) from "git add failed" (warn and carry on).
var errAutosaveRefused = errors.New("auto-save refused")

// stageForAutosave refuses an armed index, or stages the working tree for the
// auto-save commit.
//
// The refusal and the staging are ONE function on purpose. The guard is only
// correct if it runs before "git add -A" — the ruling that chose refusal over
// the alternatives turns on refusal being "the only option that never writes",
// and a guard placed one line after the staging has already written. Keeping
// both here means there is no call-site ordering left to get wrong, and it puts
// the property under test: TestRefuseArmedAutosaveWritesNothingToTheRepository
// asserts the index is untouched across a refusal, which can only hold if the
// add did not run.
func stageForAutosave(g *git.Git, worktreePath, branch string) error {
	if err := refuseArmedAutosave(g, worktreePath, branch); err != nil {
		return err
	}
	return g.Add("-A")
}

// refuseArmedAutosave declines the gt-pvx auto-save when the index is staged to
// remove more lines than a safety net should commit unreviewed. It returns nil
// when the auto-save may proceed.
//
// si-9wu1, Mayor's ruling hq-vpt06 (2026-07-30).
//
// TWO THINGS ABOUT THIS FUNCTION ARE LOAD-BEARING AND BOTH LOOK INCIDENTAL.
//
// It reads StagedLineDeletions, NEVER StagedDeletions. The latter is
// --diff-filter=D, a file-status query, and the state being caught is staged as
// MODIFICATIONS to files that still exist — a rebase under a shared ref removes
// content from within files rather than removing files. --diff-filter=D returns
// empty for it. That blindness is the entire defect: it is why the auto-save's
// existing "never destroy work" guard passed 504a5ed's 434-line deletion
// straight through, and re-using that query here would faithfully reproduce it.
//
// It runs before any staging — see stageForAutosave, which is why that ordering
// is structural rather than remembered. The armed state is already in the index,
// so it is measurable without writing anything, and "the only option that never
// writes" is the whole reason refusal beat the alternatives: unstaging the
// removal commits a broken half-refactor, and committing with a recorded HEAD
// still lands the commit — and on a shared ref, landing IS the harm.
func refuseArmedAutosave(g *git.Git, worktreePath, branch string) error {
	armed, err := g.StagedLineDeletions()
	if err != nil {
		// Measuring failed. Do not refuse on an unknown — the auto-save's whole
		// purpose is preventing loss, and blocking it on a git hiccup would
		// strand work for a reason unrelated to the hazard.
		style.PrintWarning("could not measure staged deletions before auto-save: %v", err)
		return nil
	}
	if armed < autosave.Threshold {
		return nil
	}

	markerPath, markerErr := autosave.WriteRefusal(worktreePath, branch, armed)
	fmt.Printf("\n%s Auto-save REFUSED — the index is staged to delete %d lines\n",
		style.Bold.Render("⛔"), armed)
	fmt.Printf("  The working tree was NOT modified. Your work is still on disk.\n")
	if markerErr == nil {
		fmt.Printf("  Recorded at: %s\n", markerPath)
	} else {
		style.PrintWarning("could not write the refusal marker: %v", markerErr)
	}
	fmt.Printf("  Inspect: git -C %s diff --cached --shortstat\n\n", worktreePath)

	return fmt.Errorf("%w: %d lines staged for deletion (threshold %d).\n"+
		"A safety net must not commit a mass removal unreviewed — this is the signature a\n"+
		"rebase leaves in a worktree sharing its branch with another worktree (si-d6kw),\n"+
		"and it is also what a genuine large refactor looks like.\n"+
		"Commit it deliberately, or 'git -C %s reset --hard HEAD' to drop the staging\n"+
		"without moving the branch, then re-run gt done.",
		errAutosaveRefused, armed, autosave.Threshold, worktreePath)
}
