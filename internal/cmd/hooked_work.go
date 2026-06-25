package cmd

import (
	"sort"

	"github.com/steveyegge/gastown/internal/beads"
)

const statusInProgress = "in_progress"

// listBeadsAcrossTables lists matching durable issues and ephemeral wisps.
// Hook state is stored on the work bead itself, and patrol work lives in the
// wisps table, so readers must not use issue-only bd list results as the source
// of truth.
func listBeadsAcrossTables(b *beads.Beads, opts beads.ListOptions) ([]*beads.Issue, error) {
	limit := opts.Limit

	issueOpts := opts
	issueOpts.Ephemeral = false
	issueOpts.Limit = 0
	issues, err := b.ListIssues(issueOpts)
	if err != nil {
		return nil, err
	}

	wispOpts := opts
	wispOpts.Ephemeral = true
	wispOpts.Limit = 0
	wisps, err := b.List(wispOpts)
	if err != nil {
		return nil, err
	}

	merged := mergeBeadLists(issues, wisps)
	if limit > 0 && len(merged) > limit {
		return merged[:limit], nil
	}
	return merged, nil
}

func listAssignedActiveWork(b *beads.Beads, assignee string) ([]*beads.Issue, error) {
	for _, status := range []string{beads.StatusHooked, statusInProgress} {
		beadsForStatus, err := listBeadsAcrossTables(b, beads.ListOptions{
			Status:   status,
			Assignee: assignee,
			Priority: -1,
		})
		if err != nil {
			return nil, err
		}
		if len(beadsForStatus) > 0 {
			return beadsForStatus, nil
		}
	}
	return nil, nil
}

func listChildrenAcrossTables(b *beads.Beads, parentID string) ([]*beads.Issue, error) {
	return listBeadsAcrossTables(b, beads.ListOptions{
		Parent:   parentID,
		Status:   "all",
		Priority: -1,
	})
}

func mergeBeadLists(primary, secondary []*beads.Issue) []*beads.Issue {
	merged := make([]*beads.Issue, 0, len(primary)+len(secondary))
	seen := make(map[string]struct{}, len(primary)+len(secondary))
	for _, issue := range append(primary, secondary...) {
		if issue == nil || issue.ID == "" {
			continue
		}
		if _, ok := seen[issue.ID]; ok {
			continue
		}
		seen[issue.ID] = struct{}{}
		merged = append(merged, issue)
	}

	sort.SliceStable(merged, func(i, j int) bool {
		return beadRecencyKey(merged[i]) > beadRecencyKey(merged[j])
	})
	return merged
}

func beadRecencyKey(issue *beads.Issue) string {
	if issue == nil {
		return ""
	}
	if issue.UpdatedAt != "" {
		return issue.UpdatedAt
	}
	if issue.CreatedAt != "" {
		return issue.CreatedAt
	}
	return issue.ID
}
