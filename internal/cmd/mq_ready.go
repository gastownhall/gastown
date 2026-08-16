package cmd

import "github.com/jonbaldie/gastown/internal/beads"

func isMergeRequestReadyForSelection(issue *beads.Issue) bool {
	return issue != nil && issue.Status == "open" && !beads.HasUnresolvedBlockers(issue)
}
