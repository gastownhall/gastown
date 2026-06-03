package refinery

import (
	"context"
	"fmt"
	"strings"
	"time"

	gitpkg "github.com/steveyegge/gastown/internal/git"
)

// CodeRabbitConfig configures the CodeRabbit pre-merge gate.
type CodeRabbitConfig struct {
	// Enabled controls whether the CR gate runs. Default false — rigs must opt in.
	Enabled bool `json:"enabled"`

	// MinAgeForSkip is how old a PR must be before the refinery proceeds without
	// a CR review. PRs newer than this threshold wait for CR to post.
	// Default 10m. Zero uses the default.
	MinAgeForSkip time.Duration `json:"min_age_for_skip"`

	// ParkLabel is the beads label applied to MR beads when parked for CR findings.
	// Default "cr-findings-open".
	ParkLabel string `json:"park_label"`
}

// crDefaultMinAge is the default wait time before skipping a missing CR review.
const crDefaultMinAge = 10 * time.Minute

// crDefaultParkLabel is the default beads label for parked MRs with CR findings.
const crDefaultParkLabel = "cr-findings-open"

// CRCheckResult holds the CodeRabbit gate decision-tree outcome.
type CRCheckResult struct {
	// ShouldPark is true when CR has actionable findings and the PR must not merge.
	// The refinery parks the MR (adds a label) and notifies mayor.
	ShouldPark bool

	// ShouldWait is true when CR hasn't reviewed yet but the PR is new enough to
	// wait. The refinery defers to the next poll cycle without labelling.
	ShouldWait bool

	// Reason is a human-readable explanation of the decision.
	Reason string
}

// crActionableMarkers are body-text patterns that indicate actionable CR findings.
// Used when CR's review state is "COMMENTED" to decide whether to park.
var crActionableMarkers = []string{
	"🛠️",
	"⚠️",
	"🧹",
	"Nitpick",
	"nitpick",
	"actionable comment",
}

// isActionableCRBody reports whether a CR review body contains actionable markers.
func isActionableCRBody(body string) bool {
	for _, m := range crActionableMarkers {
		if strings.Contains(body, m) {
			return true
		}
	}
	return false
}

// crParkLabel returns the effective park label, falling back to the default.
func (e *Engineer) crParkLabel() string {
	if e.config.CodeRabbit != nil && e.config.CodeRabbit.ParkLabel != "" {
		return e.config.CodeRabbit.ParkLabel
	}
	return crDefaultParkLabel
}

// crMinAge returns the effective min-age threshold, falling back to the default.
func (e *Engineer) crMinAge() time.Duration {
	if e.config.CodeRabbit != nil && e.config.CodeRabbit.MinAgeForSkip > 0 {
		return e.config.CodeRabbit.MinAgeForSkip
	}
	return crDefaultMinAge
}

// checkCodeRabbitGate runs the CR pre-merge decision tree for the given branch.
//
// Returns nil when the gate passes (proceed with merge), or a non-nil
// CRCheckResult when the merge should be deferred (ShouldWait) or blocked
// (ShouldPark).
//
// The gate is a no-op when:
//   - CR gate is not configured or not enabled
//   - The branch has no open GitHub PR
//   - The GitHub API is unavailable (fail-open to avoid blocking merges)
func (e *Engineer) checkCodeRabbitGate(_ context.Context, branch string) *CRCheckResult {
	cfg := e.config.CodeRabbit
	if cfg == nil || !cfg.Enabled {
		return nil
	}

	// Find the PR for this branch (may not exist in direct-merge workflows)
	prNumber, err := e.crFindPR(branch)
	if err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] CodeRabbit gate: FindPR(%s) error: %v (skipping gate)\n", branch, err)
		return nil // Fail-open on lookup errors
	}
	if prNumber == 0 {
		return nil // No PR — gate not applicable
	}
	_, _ = fmt.Fprintf(e.output, "[Engineer] CodeRabbit gate: checking PR #%d...\n", prNumber)

	review, prCreatedAt, err := e.crGetStatus(prNumber)
	if err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] CodeRabbit gate: status check error: %v (skipping gate)\n", err)
		return nil // Fail-open on API errors
	}

	minAge := e.crMinAge()

	if review == nil {
		// CR hasn't reviewed yet — decide based on PR age
		prAge := time.Since(prCreatedAt)
		if prAge < minAge {
			return &CRCheckResult{
				ShouldWait: true,
				Reason: fmt.Sprintf("CodeRabbit has not reviewed PR #%d yet (age %v < %v threshold)",
					prNumber, prAge.Truncate(time.Second), minAge),
			}
		}
		// PR old enough — CR likely intentionally skipped (docs-only, etc.)
		_, _ = fmt.Fprintf(e.output,
			"[Engineer] CodeRabbit gate: no review after %v — assuming CR skipped, proceeding\n",
			prAge.Truncate(time.Second))
		return nil
	}

	_, _ = fmt.Fprintf(e.output, "[Engineer] CodeRabbit gate: PR #%d state=%s\n", prNumber, review.State)

	switch review.State {
	case "APPROVED":
		return nil // CR is satisfied — proceed
	case "CHANGES_REQUESTED":
		return &CRCheckResult{
			ShouldPark: true,
			Reason:     fmt.Sprintf("CodeRabbit requested changes on PR #%d", prNumber),
		}
	case "COMMENTED":
		if isActionableCRBody(review.Body) {
			return &CRCheckResult{
				ShouldPark: true,
				Reason:     fmt.Sprintf("CodeRabbit posted actionable findings on PR #%d", prNumber),
			}
		}
		// COMMENTED but no actionable markers — proceed
		return nil
	default:
		return nil // Unknown review state — proceed
	}
}

// crGetStatusFromGit is the production implementation of crGetStatus.
// Wraps git.Git.GetPRCodeRabbitStatus to match the injectable function signature.
func crGetStatusFromGit(g *gitpkg.Git) func(int) (*gitpkg.CRReview, time.Time, error) {
	return func(prNumber int) (*gitpkg.CRReview, time.Time, error) {
		return g.GetPRCodeRabbitStatus(prNumber)
	}
}
