package cmd

import "github.com/steveyegge/gastown/internal/beads"

func isMergeRequestReadyForSelection(issue *beads.Issue) bool {
	return issue != nil && issue.Status == "open" && !beads.HasUnresolvedBlockers(issue)
}
