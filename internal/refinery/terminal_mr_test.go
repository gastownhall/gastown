package refinery

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestValidateTerminalMRCloseSnapshotRejectsDrift(t *testing.T) {
	expected := &MergeRequest{
		ID:           "gt-mr-proof",
		Branch:       "polecat/test/proof",
		IssueID:      "gt-proof",
		TargetBranch: "main",
		CommitSHA:    "abc123",
	}
	fields := &beads.MRFields{
		Branch:      "polecat/test/proof",
		SourceIssue: "gt-proof",
		Target:      "main",
		CommitSHA:   "def456",
	}

	err := validateTerminalMRCloseSnapshot(expected.ID, fields, expected)
	if err == nil || !strings.Contains(err.Error(), "changed after merge proof") {
		t.Fatalf("validateTerminalMRCloseSnapshot error = %v, want drift failure", err)
	}
}

func TestValidateTerminalMRCloseSnapshotAllowsMatchingSnapshot(t *testing.T) {
	expected := &MergeRequest{
		ID:           "gt-mr-proof",
		Branch:       "polecat/test/proof",
		IssueID:      "gt-proof",
		TargetBranch: "main",
		CommitSHA:    "abc123",
	}
	fields := &beads.MRFields{
		Branch:      "polecat/test/proof",
		SourceIssue: "gt-proof",
		Target:      "main",
		CommitSHA:   "abc123",
	}

	if err := validateTerminalMRCloseSnapshot(expected.ID, fields, expected); err != nil {
		t.Fatalf("validateTerminalMRCloseSnapshot: %v", err)
	}
}
