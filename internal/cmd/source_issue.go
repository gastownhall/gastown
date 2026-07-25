package cmd

import (
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
)

// resolveSourceIssue determines which bead an MR should be attributed to.
//
// Precedence: explicit --issue > HOOKED bead assigned to this agent > branch name.
//
// The hooked bead outranks the branch name because branch names go stale
// (hq-szhze). Persistent polecats reuse one worktree across slings, and when
// post-done cleanup cannot switch off the feature branch, the next assignment's
// commits land on a branch still named after the PREVIOUS bead. Deriving
// source_issue from that name mis-attributes the MR: the refinery closes the
// wrong bead on merge and the real one is left open with no MR.
func resolveSourceIssue(bd *beads.Beads, branch, explicitIssue, agentID string) (string, error) {
	if explicitIssue != "" {
		return explicitIssue, nil
	}
	return chooseSourceIssue(branch, agentID, findHookedBeadsForAgent(bd, agentID))
}

// chooseSourceIssue is the pure decision half of resolveSourceIssue: given the
// branch, the agent, and the agent's hooked beads, pick the issue to attribute.
//
// Returns an error only when the answer is genuinely ambiguous — several hooked
// beads with no way to pick — so the agent retries with --issue instead of
// silently attributing work to the wrong bead. An empty issue with no error
// means neither the hook nor the branch named one; callers decide whether that
// is fatal.
func chooseSourceIssue(branch, agentID string, hooked []string) (string, error) {
	branchIssue := parseBranchName(branch).Issue

	switch len(hooked) {
	case 0:
		// No hook to consult — the branch name is all we have.
		return branchIssue, nil
	case 1:
		if branchIssue != "" && branchIssue != hooked[0] {
			style.PrintWarning(
				"branch %q names issue %s but %s has %s HOOKED — attributing the MR to the hooked bead\n"+
					"  (stale reused branch, hq-szhze; pass --issue to override)",
				branch, branchIssue, agentID, hooked[0])
		}
		return hooked[0], nil
	}

	// Several hooked beads. The branch name is a legitimate tie-breaker when it
	// names one of them; otherwise refuse to guess.
	for _, id := range hooked {
		if id == branchIssue {
			return id, nil
		}
	}
	return "", fmt.Errorf("cannot determine source issue: %s has %d hooked beads (%s) and branch %q names none of them; pass --issue to disambiguate",
		agentID, len(hooked), strings.Join(hooked, ", "), branch)
}

// findHookedBeadsForAgent returns the IDs of all beads with status=hooked
// assigned to this agent. The work bead itself tracks status and assignee, so
// this is the authoritative record of what the agent is working on (hq-l6mm5).
func findHookedBeadsForAgent(bd *beads.Beads, agentID string) []string {
	if agentID == "" {
		return nil
	}
	hookedBeads, err := bd.List(beads.ListOptions{
		Status:   beads.StatusHooked,
		Assignee: agentID,
		Priority: -1,
	})
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(hookedBeads))
	for _, b := range hookedBeads {
		ids = append(ids, b.ID)
	}
	return ids
}
