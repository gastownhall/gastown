package cmd

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

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

func listAssignedActiveWorkAcrossStatuses(b *beads.Beads, assignee string) ([]*beads.Issue, error) {
	var assigned []*beads.Issue
	for _, status := range []string{beads.StatusHooked, statusInProgress} {
		beadsForStatus, err := listBeadsAcrossTables(b, beads.ListOptions{
			Status:   status,
			Assignee: assignee,
			Priority: -1,
		})
		if err != nil {
			return nil, err
		}
		assigned = append(assigned, beadsForStatus...)
	}
	return mergeBeadLists(assigned, nil), nil
}

func listChildrenAcrossTables(b *beads.Beads, parentID string) ([]*beads.Issue, error) {
	return listBeadsAcrossTables(b, beads.ListOptions{
		Parent:   parentID,
		Status:   "all",
		Priority: -1,
	})
}

func resolveHookLookupWorkDir(workDir, target, townRoot string) string {
	if townRoot == "" || isTownLevelRole(target) {
		return workDir
	}

	rigName := strings.Split(target, "/")[0]
	if rigName == "" || rigName == "mayor" || rigName == "deacon" {
		return workDir
	}
	if rigDir := beads.GetRigDirForName(townRoot, rigName); rigDir != "" {
		return rigDir
	}
	return filepath.Join(townRoot, rigName)
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
		left := beadRecencyTime(merged[i])
		right := beadRecencyTime(merged[j])
		if !left.Equal(right) {
			return left.After(right)
		}
		return beadSortID(merged[i]) > beadSortID(merged[j])
	})
	return merged
}

func beadRecencyTime(issue *beads.Issue) time.Time {
	if issue == nil {
		return time.Time{}
	}
	if ts := parseBeadTime(issue.UpdatedAt); !ts.IsZero() {
		return ts
	}
	return parseBeadTime(issue.CreatedAt)
}

func parseBeadTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return ts
}

func beadSortID(issue *beads.Issue) string {
	if issue == nil {
		return ""
	}
	return issue.ID
}
