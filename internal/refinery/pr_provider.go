package refinery

import (
	"strings"

	"github.com/steveyegge/gastown/internal/git"
)

// PRProvider abstracts VCS-specific PR operations for the merge queue.
// Implementations exist for GitHub (default) and Bitbucket Cloud.
type PRProvider interface {
	// FindPullRequest returns the PR for the given ref, or nil if none exists.
	FindPullRequest(branch, prURL string, prNumber int, headSHA string) (*git.PullRequestInfo, error)

	// IsPRApproved checks whether a PR has at least one approving review.
	IsPRApproved(pr *git.PullRequestInfo) (bool, error)

	// MergePR merges a PR using the specified method (e.g., "squash", "merge", "rebase").
	// Returns the merge commit SHA on success (if available).
	MergePR(pr *git.PullRequestInfo, method string) (string, error)

	// ChecksPassed reports the CI state of the PR's current head. passed is
	// true only when every check succeeded (or was neutral/skipped); pending
	// is true while any check is still running, and when no checks are
	// reported at all (never treated as passed). Definite failures return a
	// *ChecksFailedError naming the failing checks; any other error means the
	// state is unknown and callers must fail closed.
	ChecksPassed(pr *git.PullRequestInfo) (passed, pending bool, err error)
}

// ChecksFailedError is returned by PRProvider.ChecksPassed when CI reported
// definite failures. Names lists the failing checks.
type ChecksFailedError struct {
	Names []string
}

func (e *ChecksFailedError) Error() string {
	return "CI checks failed: " + strings.Join(e.Names, ", ")
}
