package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestIsReadyIssue_BlockingAndStatus(t *testing.T) {
	tests := []struct {
		name string
		in   trackedIssueInfo
		want bool
	}{
		{
			name: "closed issue never ready",
			in: trackedIssueInfo{
				Status:  "closed",
				Blocked: false,
			},
			want: false,
		},
		{
			name: "unknown issue never ready",
			in: trackedIssueInfo{
				Status:  trackedStatusUnknown,
				Blocked: false,
			},
			want: false,
		},
		{
			name: "blank status never ready",
			in: trackedIssueInfo{
				Status:  " ",
				Blocked: false,
			},
			want: false,
		},
		{
			name: "blocked open issue not ready",
			in: trackedIssueInfo{
				Status:  "open",
				Blocked: true,
			},
			want: false,
		},
		{
			name: "open unassigned issue ready",
			in: trackedIssueInfo{
				Status:  "open",
				Blocked: false,
			},
			want: true,
		},
		{
			name: "non-open unassigned issue treated ready for recovery",
			in: trackedIssueInfo{
				Status:  "in_progress",
				Blocked: false,
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isReadyIssue(tc.in, nil)
			if got != tc.want {
				t.Fatalf("isReadyIssue() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestApplyFreshIssueDetails_SetsBlockedFlag(t *testing.T) {
	dep := trackedDependency{
		ID:     "gt-123",
		Status: "open",
	}
	details := &issueDetails{
		ID:             "gt-123",
		Status:         "open",
		BlockedByCount: 1,
	}

	applyFreshIssueDetails(&dep, details)

	if !dep.Blocked {
		t.Fatalf("applyFreshIssueDetails() should set Blocked=true when details are blocked")
	}
}

func TestApplyFreshIssueDetails_BlankStatusBecomesUnknown(t *testing.T) {
	dep := trackedDependency{ID: "gt-123"}
	details := &issueDetails{ID: "gt-123", Status: "  "}

	applyFreshIssueDetails(&dep, details)

	if dep.Status != trackedStatusUnknown {
		t.Fatalf("dep.Status = %q, want %q", dep.Status, trackedStatusUnknown)
	}
}

func TestIssueDetailsIsBlocked(t *testing.T) {
	tests := []struct {
		name string
		in   issueDetails
		want bool
	}{
		{
			name: "blocked_by_count marks blocked",
			in: issueDetails{
				BlockedByCount: 2,
			},
			want: true,
		},
		{
			name: "blocked_by list marks blocked",
			in: issueDetails{
				BlockedBy: []string{"gt-1"},
			},
			want: true,
		},
		{
			name: "open blocks dependency marks blocked",
			in: issueDetails{
				Dependencies: []issueDependency{
					{DependencyType: "blocks", Status: "open"},
				},
			},
			want: true,
		},
		{
			name: "closed blocks dependency does not mark blocked",
			in: issueDetails{
				Dependencies: []issueDependency{
					{DependencyType: "blocks", Status: "closed"},
				},
			},
			want: false,
		},
		{
			name: "non-blocking dependency does not mark blocked",
			in: issueDetails{
				Dependencies: []issueDependency{
					{DependencyType: "parent-child", Status: "open"},
				},
			},
			want: false,
		},
		// #1893: merge-blocks stays blocked until merge is confirmed, not merely closed.
		{
			name: "open merge-blocks dependency marks blocked",
			in: issueDetails{
				Dependencies: []issueDependency{
					{DependencyType: "merge-blocks", Status: "open"},
				},
			},
			want: true,
		},
		{
			name: "closed merge-blocks without merge confirmation still blocked",
			in: issueDetails{
				Dependencies: []issueDependency{
					{DependencyType: "merge-blocks", Status: "closed"},
				},
			},
			want: true,
		},
		{
			name: "closed merge-blocks with gt done close reason still blocked",
			in: issueDetails{
				Dependencies: []issueDependency{
					{DependencyType: "merge-blocks", Status: "closed", CloseReason: "done"},
				},
			},
			want: true,
		},
		{
			name: "rejected merge-blocks stays blocked",
			in: issueDetails{
				Dependencies: []issueDependency{
					{DependencyType: "merge-blocks", Status: "closed", CloseReason: "MR rejected"},
				},
			},
			want: true,
		},
		{
			name: "merged merge-blocks does not mark blocked",
			in: issueDetails{
				Dependencies: []issueDependency{
					{DependencyType: "merge-blocks", Status: "closed", CloseReason: "Merged in mr-xyz"},
				},
			},
			want: false,
		},
		{
			name: "direct-merge merge-blocks does not mark blocked",
			in: issueDetails{
				Dependencies: []issueDependency{
					{DependencyType: "merge-blocks", Status: "closed", CloseReason: "Direct merge to main (convoy strategy)"},
				},
			},
			want: false,
		},
		{
			name: "no-code-changes merge-blocks does not mark blocked",
			in: issueDetails{
				Dependencies: []issueDependency{
					{DependencyType: "merge-blocks", Status: "closed", CloseReason: "Completed with no code changes (already fixed or already merged)"},
				},
			},
			want: false,
		},
		{
			name: "tombstone merge-blocks does not mark blocked",
			in: issueDetails{
				Dependencies: []issueDependency{
					{DependencyType: "merge-blocks", Status: "tombstone"},
				},
			},
			want: false,
		},
		{
			name: "closed blocks dependency still unblocks (existing semantics)",
			in: issueDetails{
				Dependencies: []issueDependency{
					{DependencyType: "blocks", Status: "closed"},
				},
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.IsBlocked()
			if got != tc.want {
				t.Fatalf("IsBlocked() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIssueToDetails_PreservesMergeBlocksCloseReason(t *testing.T) {
	issue := &beads.Issue{
		ID:     "gt-downstream",
		Title:  "Downstream",
		Status: "open",
		Type:   "task",
		Dependencies: []beads.IssueDep{
			{
				ID:             "gt-upstream",
				Status:         "closed",
				DependencyType: "merge-blocks",
				CloseReason:    "Merged in mr-xyz",
			},
		},
	}

	details := issueToDetails(issue)
	if details == nil {
		t.Fatal("issueToDetails returned nil")
	}
	if len(details.Dependencies) != 1 {
		t.Fatalf("Dependencies len = %d, want 1", len(details.Dependencies))
	}
	dep := details.Dependencies[0]
	if dep.DependencyType != "merge-blocks" {
		t.Fatalf("DependencyType = %q, want merge-blocks", dep.DependencyType)
	}
	if dep.CloseReason != "Merged in mr-xyz" {
		t.Fatalf("CloseReason = %q, want %q", dep.CloseReason, "Merged in mr-xyz")
	}
	if details.IsBlocked() {
		t.Fatal("issueToDetails should preserve merge confirmation so IsBlocked is false")
	}
}

func TestIsSlingableBead(t *testing.T) {
	// Set up a fake town root with routes.jsonl
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	routesContent := `{"prefix": "gt-", "path": "gastown/mayor/rig"}
{"prefix": "bd-", "path": "beads/mayor/rig"}
{"prefix": "hq-", "path": "."}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		beadID string
		want   bool
	}{
		{"rig bead is slingable", "gt-wisp-abc", true},
		{"another rig bead is slingable", "bd-wisp-xyz", true},
		{"town-level bead not slingable", "hq-wisp-abc", false},
		{"town-level convoy not slingable", "hq-cv-kl6ns", false},
		{"unknown prefix not slingable", "zz-wisp-abc", false},
		{"no prefix assumes slingable", "nohyphen", true},
		{"empty ID assumes slingable", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isSlingableBead(townRoot, tc.beadID)
			if got != tc.want {
				t.Fatalf("isSlingableBead(%q) = %v, want %v", tc.beadID, got, tc.want)
			}
		})
	}
}
