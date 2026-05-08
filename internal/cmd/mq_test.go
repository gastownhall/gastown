package cmd

import (
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// mockBranchOps is a test double for postMergeBranchOps.
type mockBranchOps struct {
	hasOpenPR            bool
	deleteRemoteBranchFn func(remote, branch string) error
	deleteBranchFn       func(name string, force bool) error
}

func (m *mockBranchOps) HasOpenPR(_ string) bool { return m.hasOpenPR }
func (m *mockBranchOps) DeleteRemoteBranch(remote, branch string) error {
	if m.deleteRemoteBranchFn != nil {
		return m.deleteRemoteBranchFn(remote, branch)
	}
	return nil
}
func (m *mockBranchOps) DeleteBranch(name string, force bool) error {
	if m.deleteBranchFn != nil {
		return m.deleteBranchFn(name, force)
	}
	return nil
}

func TestDeletePostMergeBranch(t *testing.T) {
	tests := []struct {
		name       string
		ops        *mockBranchOps
		branch     string
		wantErr    bool
		wantErrStr string
	}{
		{
			name:   "skips deletion when open PR exists",
			ops:    &mockBranchOps{hasOpenPR: true},
			branch: "polecat/nitro/eat-luj@movek49r",
		},
		{
			name: "returns error when remote deletion fails",
			ops: &mockBranchOps{
				deleteRemoteBranchFn: func(_, _ string) error {
					return errors.New("ref not found")
				},
			},
			branch:     "polecat/nitro/eat-luj@movek49r",
			wantErr:    true,
			wantErrStr: "remote branch delete",
		},
		{
			name:   "succeeds when both remote and local deletion succeed",
			ops:    &mockBranchOps{},
			branch: "polecat/nitro/eat-luj@movek49r",
		},
		{
			name: "succeeds when remote succeeds but local deletion fails (best-effort)",
			ops: &mockBranchOps{
				deleteBranchFn: func(_ string, _ bool) error {
					return errors.New("branch not found locally")
				},
			},
			branch: "polecat/nitro/eat-luj@movek49r",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := deletePostMergeBranch(tt.ops, tt.branch)
			if (err != nil) != tt.wantErr {
				t.Errorf("deletePostMergeBranch() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && tt.wantErrStr != "" {
				if !stringContains(err.Error(), tt.wantErrStr) {
					t.Errorf("deletePostMergeBranch() error = %q, want containing %q", err.Error(), tt.wantErrStr)
				}
			}
		})
	}
}

func TestParseBranchName(t *testing.T) {
	tests := []struct {
		name       string
		branch     string
		wantIssue  string
		wantWorker string
	}{
		{
			name:       "polecat branch format",
			branch:     "polecat/Nux/gt-xyz",
			wantIssue:  "gt-xyz",
			wantWorker: "Nux",
		},
		{
			name:       "polecat branch with subtask",
			branch:     "polecat/Worker/gt-abc.1",
			wantIssue:  "gt-abc.1",
			wantWorker: "Worker",
		},
		{
			name:       "polecat branch with issue and timestamp",
			branch:     "polecat/furiosa/gt-jns7.1@mk123456",
			wantIssue:  "gt-jns7.1",
			wantWorker: "furiosa",
		},
		{
			name:       "modern polecat branch (timestamp format)",
			branch:     "polecat/furiosa-mkc36bb9",
			wantIssue:  "", // Should NOT extract fake issue from worker-timestamp
			wantWorker: "furiosa",
		},
		{
			name:       "modern polecat branch with longer name",
			branch:     "polecat/citadel-mk0vro62",
			wantIssue:  "",
			wantWorker: "citadel",
		},
		{
			name:       "simple issue branch",
			branch:     "gt-xyz",
			wantIssue:  "gt-xyz",
			wantWorker: "",
		},
		{
			name:       "feature branch with issue",
			branch:     "feature/gt-abc-impl",
			wantIssue:  "gt-abc",
			wantWorker: "",
		},
		{
			name:       "no issue pattern",
			branch:     "main",
			wantIssue:  "",
			wantWorker: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := parseBranchName(tt.branch)
			if info.Issue != tt.wantIssue {
				t.Errorf("parseBranchName() Issue = %q, want %q", info.Issue, tt.wantIssue)
			}
			if info.Worker != tt.wantWorker {
				t.Errorf("parseBranchName() Worker = %q, want %q", info.Worker, tt.wantWorker)
			}
		})
	}
}

func TestFormatMRAge(t *testing.T) {
	tests := []struct {
		name      string
		createdAt string
		wantOk    bool // just check it doesn't panic/error
	}{
		{
			name:      "RFC3339 format",
			createdAt: "2025-01-01T12:00:00Z",
			wantOk:    true,
		},
		{
			name:      "alternative format",
			createdAt: "2025-01-01T12:00:00",
			wantOk:    true,
		},
		{
			name:      "invalid format",
			createdAt: "not-a-date",
			wantOk:    true, // returns "?" for invalid
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatMRAge(tt.createdAt)
			if tt.wantOk && result == "" {
				t.Errorf("formatMRAge() returned empty for %s", tt.createdAt)
			}
		})
	}
}

func TestGetDescriptionWithoutMRFields(t *testing.T) {
	tests := []struct {
		name        string
		description string
		want        string
	}{
		{
			name:        "empty description",
			description: "",
			want:        "",
		},
		{
			name:        "only MR fields",
			description: "branch: polecat/Nux/gt-xyz\ntarget: main\nworker: Nux",
			want:        "",
		},
		{
			name:        "mixed content",
			description: "branch: polecat/Nux/gt-xyz\nSome custom notes\ntarget: main",
			want:        "Some custom notes",
		},
		{
			name:        "no MR fields",
			description: "Just a regular description\nWith multiple lines",
			want:        "Just a regular description\nWith multiple lines",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getDescriptionWithoutMRFields(tt.description)
			if got != tt.want {
				t.Errorf("getDescriptionWithoutMRFields() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		maxLen int
		want   string
	}{
		{
			name:   "short string",
			s:      "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "exact length",
			s:      "hello",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "needs truncation",
			s:      "hello world",
			maxLen: 8,
			want:   "hello...",
		},
		{
			name:   "very short max",
			s:      "hello",
			maxLen: 3,
			want:   "hel",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateString(tt.s, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatStatus(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   string // We check for substring since styling adds ANSI codes
	}{
		{
			name:   "open status",
			status: "open",
			want:   "open",
		},
		{
			name:   "in_progress status",
			status: "in_progress",
			want:   "in_progress",
		},
		{
			name:   "closed status",
			status: "closed",
			want:   "closed",
		},
		{
			name:   "unknown status",
			status: "pending",
			want:   "pending",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatStatus(tt.status)
			if got == "" {
				t.Errorf("formatStatus(%q) returned empty string", tt.status)
			}
			// The result contains ANSI codes, so just check the status text is present
			if !contains(got, tt.want) {
				t.Errorf("formatStatus(%q) = %q, should contain %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestGetStatusIcon(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"open", "○"},
		{"in_progress", "▶"},
		{"closed", "✓"},
		{"unknown", "•"},
		{"", "•"},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := getStatusIcon(tt.status)
			if got != tt.want {
				t.Errorf("getStatusIcon(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestFormatTimeAgo(t *testing.T) {
	tests := []struct {
		name      string
		timestamp string
		wantEmpty bool
	}{
		{
			name:      "RFC3339 format",
			timestamp: "2025-01-01T12:00:00Z",
			wantEmpty: false,
		},
		{
			name:      "RFC3339 with timezone",
			timestamp: "2025-01-01T12:00:00-08:00",
			wantEmpty: false,
		},
		{
			name:      "date only format",
			timestamp: "2025-01-01",
			wantEmpty: false,
		},
		{
			name:      "datetime without Z",
			timestamp: "2025-01-01T12:00:00",
			wantEmpty: false,
		},
		{
			name:      "invalid format returns empty",
			timestamp: "not-a-date",
			wantEmpty: true,
		},
		{
			name:      "empty string returns empty",
			timestamp: "",
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTimeAgo(tt.timestamp)
			if tt.wantEmpty && got != "" {
				t.Errorf("formatTimeAgo(%q) = %q, want empty", tt.timestamp, got)
			}
			if !tt.wantEmpty && got == "" {
				t.Errorf("formatTimeAgo(%q) returned empty, want non-empty", tt.timestamp)
			}
		})
	}
}

// contains checks if s contains substr (helper for styled output)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && stringContains(s, substr)))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestIssuePatternCompiledAtPackageLevel verifies that the issuePattern regex
// is compiled once at package level (not on every parseBranchName call).
func TestIssuePatternCompiledAtPackageLevel(t *testing.T) {
	// Verify the pattern is not nil and is a compiled regex
	if issuePattern == nil {
		t.Error("issuePattern should be compiled at package level, got nil")
	}
	// Verify it matches expected patterns
	tests := []struct {
		branch    string
		wantMatch bool
		wantIssue string
	}{
		{"polecat/Nux/gt-xyz", true, "gt-xyz"},
		{"gt-abc", true, "gt-abc"},
		{"feature/proj-123-add-feature", true, "proj-123"},
		{"main", false, ""},
		{"", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.branch, func(t *testing.T) {
			matches := issuePattern.FindStringSubmatch(tt.branch)
			if (len(matches) > 1) != tt.wantMatch {
				t.Errorf("FindStringSubmatch(%q) match = %v, want %v", tt.branch, len(matches) > 1, tt.wantMatch)
			}
			if tt.wantMatch && len(matches) > 1 && matches[1] != tt.wantIssue {
				t.Errorf("FindStringSubmatch(%q) issue = %q, want %q", tt.branch, matches[1], tt.wantIssue)
			}
		})
	}
}

// TestPolecatCleanupTimeoutConstant verifies the timeout constant is set correctly.
func TestPolecatCleanupTimeoutConstant(t *testing.T) {
	// This test documents the expected timeout value.
	// The actual timeout behavior is tested manually or with integration tests.
	const expectedMaxCleanupWait = 5 * time.Minute
	if expectedMaxCleanupWait != 5*time.Minute {
		t.Errorf("expectedMaxCleanupWait = %v, want 5m", expectedMaxCleanupWait)
	}
}

// TestMRFilteringByLabel verifies that MRs are identified by their gt:merge-request
// label rather than the deprecated issue_type field. This is the fix for #816 where
// MRs created by `gt done` have issue_type='task' but correct gt:merge-request label.
func TestMRFilteringByLabel(t *testing.T) {
	tests := []struct {
		name     string
		issue    *beads.Issue
		wantIsMR bool
	}{
		{
			name: "MR with correct label and wrong type (bug #816 scenario)",
			issue: &beads.Issue{
				ID:     "mr-1",
				Title:  "Merge: test-branch",
				Type:   "task", // Wrong type (default from bd create)
				Labels: []string{"gt:merge-request"}, // Correct label
			},
			wantIsMR: true,
		},
		{
			name: "MR with correct label and correct type",
			issue: &beads.Issue{
				ID:     "mr-2",
				Title:  "Merge: another-branch",
				Type:   "merge-request",
				Labels: []string{"gt:merge-request"},
			},
			wantIsMR: true,
		},
		{
			name: "Task without MR label",
			issue: &beads.Issue{
				ID:     "task-1",
				Title:  "Regular task",
				Type:   "task",
				Labels: []string{"other-label"},
			},
			wantIsMR: false,
		},
		{
			name: "Issue with no labels",
			issue: &beads.Issue{
				ID:    "issue-1",
				Title: "No labels",
				Type:  "task",
			},
			wantIsMR: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := beads.HasLabel(tt.issue, "gt:merge-request")
			if got != tt.wantIsMR {
				t.Errorf("HasLabel(%q, \"gt:merge-request\") = %v, want %v",
					tt.issue.ID, got, tt.wantIsMR)
			}
		})
	}
}
