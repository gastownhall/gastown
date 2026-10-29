// Package beads provides merge request and gate utilities.
package beads

import (
	"errors"
	"fmt"
	"strings"
)

// FindMRForBranch searches for an open merge-request bead for the given branch.
// Returns the MR bead if found, nil if not found.
// This enables idempotent `gt done` - if an MR already exists, we skip creation.
func (b *Beads) FindMRForBranch(branch string) (*Issue, error) {
	return b.findMRForBranch(branch, true)
}

// FindMRForBranchAny searches for a merge-request bead for the given branch
// across all statuses (open and closed). Used by recovery checks to determine
// if work was ever submitted to the merge queue. See #1035.
func (b *Beads) FindMRForBranchAny(branch string) (*Issue, error) {
	return b.findMRForBranch(branch, false)
}

// FindMRForBranchAndSHA searches for an open merge-request bead matching both
// the branch name AND the commit SHA. This is the correct dedup key: two MRs
// from the same branch but with different commit SHAs are distinct submissions
// (e.g., polecat fixed a gate failure and re-pushed). See GH#3032.
//
// Returns nil if no MR matches both branch and SHA. Callers should create a
// new MR in that case and supersede old MRs for the same source issue.
func (b *Beads) FindMRForBranchAndSHA(branch, commitSHA string) (*Issue, error) {
	issues, err := b.ListMergeRequests(ListOptions{
		Status: "all",
		Label:  "gt:merge-request",
	})
	if err != nil {
		return nil, err
	}

	branchPrefix := "branch: " + branch + "\n"
	for _, issue := range issues {
		if issue.Status == "closed" {
			continue
		}
		if !strings.HasPrefix(issue.Description, branchPrefix) {
			continue
		}
		// Branch matches — check commit SHA.
		// If the MR has no commit_sha field (legacy), fall back to branch-only
		// match for backward compatibility.
		fields := ParseMRFields(issue)
		if fields != nil && fields.CommitSHA != "" && commitSHA != "" {
			if fields.CommitSHA != commitSHA {
				// Same branch but different SHA — this is a stale MR.
				// Don't return it; caller will create a new MR and supersede.
				continue
			}
		}
		return issue, nil
	}

	return nil, nil
}

// findMRForBranch searches the wisps table (Dolt) for a merge-request
// bead matching the given branch.
// Uses status=all which includes all issue statuses with full descriptions.
// Ephemeral=true routes to the wisps table where MR beads live (GH#2446).
// When skipClosed is true, closed beads are excluded (for open-MR checks).
func (b *Beads) findMRForBranch(branch string, skipClosed bool) (*Issue, error) {
	branchPrefix := "branch: " + branch + "\n"

	issues, err := b.ListMergeRequests(ListOptions{
		Status: "all",
		Label:  "gt:merge-request",
	})
	if err != nil {
		return nil, err
	}
	for _, issue := range issues {
		if skipClosed && issue.Status == "closed" {
			continue
		}
		if strings.HasPrefix(issue.Description, branchPrefix) {
			return issue, nil
		}
	}

	return nil, nil
}

// FindOpenMRsForIssue returns all open merge-request beads whose source_issue
// matches the given issue ID. Used to find prior attempts when re-dispatching
// an issue and to supersede old MRs when a new one is created.
func (b *Beads) FindOpenMRsForIssue(issueID string) ([]*Issue, error) {
	issues, err := b.ListMergeRequests(ListOptions{
		Status: "open",
		Label:  "gt:merge-request",
	})
	if err != nil {
		return nil, err
	}

	var matches []*Issue
	for _, issue := range issues {
		if MatchesMRSourceIssue(issue.Description, issueID) {
			matches = append(matches, issue)
		}
	}
	return matches, nil
}

// MatchesMRSourceIssue returns true if the MR description contains a
// source_issue field matching the given issue ID exactly. The trailing
// newline in the needle prevents partial ID matches (e.g., "gt-abc"
// must not match "gt-abcdef").
func MatchesMRSourceIssue(description, issueID string) bool {
	needle := "source_issue: " + issueID + "\n"
	return strings.Contains(description, needle)
}

// ErrMRNotInRigQueue reports that a merge-request bead was created and is
// readable, but is not present in the rig's own merge-queue view.
var ErrMRNotInRigQueue = errors.New("merge request is not visible in the rig's merge queue")

// VerifyMRInRigQueue confirms that mrID is visible in the rig's own merge-queue
// view — the same query the Refinery runs to pick up work (see runMQList).
//
// This exists because neither of the older guards can detect a misfiled MR:
//
//   - ValidateRigPrefix compares only the bead's ID prefix. A town whose rig
//     store and contributor store share a prefix (gastown: both "gt-") passes
//     that check no matter which store the bead landed in.
//   - A plain Show read-back succeeds too, because bd resolves reads across
//     repos.additional and happily finds the bead in the contributor store.
//
// So an MR can be created, be readable, pass both guards, and still be invisible
// to the Refinery — which on a merge-queue rig is silent work loss: the branch is
// pushed, the polecat reports COMPLETED, and nothing is ever queued to merge it
// (gt-6sg). Presence in the queue view is the only check that reflects what the
// Refinery will actually see, so it is the one worth gating completion on.
func (b *Beads) VerifyMRInRigQueue(rigName, mrID string) error {
	if mrID == "" {
		return fmt.Errorf("%w: empty merge request ID", ErrMRNotInRigQueue)
	}

	issues, err := b.ListMergeRequests(ListOptions{
		Status:   "all",
		Label:    "gt:merge-request",
		Priority: -1,
		Rig:      rigName,
	})
	if err != nil {
		// An unreadable queue is not proof the MR is missing. Report the failure
		// so callers can surface it rather than silently treating it as absent.
		return fmt.Errorf("querying rig %q merge queue: %w", rigName, err)
	}

	for _, issue := range issues {
		if issue != nil && issue.ID == mrID {
			return nil
		}
	}

	return fmt.Errorf("%w: %s was created and is readable, but rig %q's queue does not contain it — "+
		"the bead most likely landed in a different store than the one the Refinery reads",
		ErrMRNotInRigQueue, mrID, rigName)
}
