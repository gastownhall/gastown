package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestIsMergeRequestReadyForSelection(t *testing.T) {
	tests := []struct {
		name  string
		issue *beads.Issue
		want  bool
	}{
		{
			name:  "open without blockers is ready",
			issue: &beads.Issue{Status: "open"},
			want:  true,
		},
		{
			name: "nil issue is not ready",
		},
		{
			name:  "closed issue is not ready",
			issue: &beads.Issue{Status: "closed"},
		},
		{
			name: "open issue with blocking dependency is not ready",
			issue: &beads.Issue{
				Status:       "open",
				Dependencies: []beads.IssueDep{{ID: "gt-blocker", Status: "open", DependencyType: "blocks"}},
			},
		},
		{
			name:  "open issue with unhydrated dependency count is not ready",
			issue: &beads.Issue{Status: "open", DependencyCount: 1},
		},
		{
			name: "closed dependency overrides stale blocked count",
			issue: &beads.Issue{
				Status:         "open",
				BlockedByCount: 1,
				Dependencies:   []beads.IssueDep{{ID: "gt-closed", Status: "closed", DependencyType: "blocks"}},
			},
			want: true,
		},
		{
			name: "unmerged merge-block remains not ready",
			issue: &beads.Issue{
				Status:       "open",
				Dependencies: []beads.IssueDep{{ID: "gt-closed-only", Status: "closed", DependencyType: "merge-blocks"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMergeRequestReadyForSelection(tt.issue); got != tt.want {
				t.Fatalf("isMergeRequestReadyForSelection() = %v, want %v", got, tt.want)
			}
		})
	}
}
