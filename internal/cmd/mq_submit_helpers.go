package cmd

import (
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/style"
)

// commitSHAResolver is the minimal git surface needed by resolveCommitSHA.
// Defined as an interface so the helper can be table-tested with a fake.
type commitSHAResolver interface {
	CurrentBranch() (string, error)
	FetchBranch(remote, branch string) error
	Rev(ref string) (string, error)
}

// resolveCommitSHA returns the commit SHA to record on the MR bead.
//
// When --branch is explicit and points at a different branch than the cwd's
// current branch (e.g., mayor's worktree on holds-gates submitting a polecat
// branch), the polecat branch tip is the correct commit SHA — not cwd HEAD.
//
// Behavior:
//   - explicitBranch == "" or matches current branch: return Rev("HEAD") (cwd is on the right branch already)
//   - explicitBranch differs from current branch: fetch origin/<explicitBranch>, then Rev("origin/<explicitBranch>")
//
// The fetch is always performed in the cross-branch path. It is idempotent
// (no harm if already current), single-ref (fast), and protects against the
// failure mode where cwd's local origin/<branch> ref is stale relative to
// what was just pushed (the recurring 9-occurrence bug ps-7v7 / co-toog).
//
// A fetch failure is non-fatal: emit a warning and proceed with Rev. If the
// remote ref is missing entirely Rev will fail and the caller surfaces that.
func resolveCommitSHA(g commitSHAResolver, explicitBranch string) (string, error) {
	if explicitBranch == "" {
		return g.Rev("HEAD")
	}

	currentBranch, err := g.CurrentBranch()
	if err != nil {
		// If we can't determine the current branch, fall back to HEAD with
		// a warning: the explicit branch may or may not match HEAD, but
		// HEAD is the only ref we can resolve.
		style.PrintWarning("could not determine current branch: %v (falling back to HEAD)", err)
		return g.Rev("HEAD")
	}

	if explicitBranch == currentBranch {
		return g.Rev("HEAD")
	}

	if fetchErr := g.FetchBranch("origin", explicitBranch); fetchErr != nil {
		// Non-fatal: warn and try Rev anyway. If origin/<branch> doesn't
		// exist locally, Rev will fail and the caller surfaces it.
		style.PrintWarning("could not fetch origin/%s: %v (commit_sha may be stale)", explicitBranch, fetchErr)
	}

	sha, err := g.Rev("origin/" + explicitBranch)
	if err != nil {
		return "", fmt.Errorf("resolving origin/%s: %w", explicitBranch, err)
	}
	return sha, nil
}

// shouldDeleteSupersededBranch reports whether the old MR's remote branch
// should be deleted as part of supersede cleanup.
//
// Two conditions must hold:
//   - The old branch must differ from the new MR's branch. Same-branch
//     supersede (mayor's pattern: rebase → push --force-with-lease →
//     resubmit) means the new MR points at the same branch — deleting it
//     would leave the new MR pointing at a phantom origin branch (ps-7v7).
//   - The old branch must be a polecat/* branch. Non-polecat branches may
//     belong to contributor forks; deleting them closes upstream PRs (GH#2669).
func shouldDeleteSupersededBranch(oldBranch, newBranch string) bool {
	if oldBranch == newBranch {
		return false
	}
	return strings.HasPrefix(oldBranch, "polecat/")
}
