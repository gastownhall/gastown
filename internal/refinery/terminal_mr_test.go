package refinery

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestShouldClearAgentActiveMRRequiresAuthoritativeRejection(t *testing.T) {
	tests := []struct {
		name                string
		requested           string
		authoritativeReason string
		want                bool
	}{
		{"verified rejection", "rejected: policy", "rejected", true},
		{"merged request", "merged", "merged", false},
		{"stale rejection against merged MR", "rejected: policy", "merged", false},
		{"blank terminal reason", "rejected: policy", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldClearAgentActiveMR(tt.requested, tt.authoritativeReason); got != tt.want {
				t.Fatalf("shouldClearAgentActiveMR(%q, %q) = %v, want %v", tt.requested, tt.authoritativeReason, got, tt.want)
			}
		})
	}
}

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

func TestValidateTerminalMRCloseSnapshotRejectsAgentBeadDrift(t *testing.T) {
	expected := &MergeRequest{
		ID:           "gt-mr-proof",
		Branch:       "polecat/test/proof",
		IssueID:      "gt-proof",
		TargetBranch: "main",
		CommitSHA:    "abc123",
		AgentBead:    "hq-agent-original",
	}
	fields := &beads.MRFields{
		Branch:      expected.Branch,
		SourceIssue: expected.IssueID,
		Target:      expected.TargetBranch,
		CommitSHA:   expected.CommitSHA,
		AgentBead:   "hq-agent-replacement",
	}

	err := validateTerminalMRCloseSnapshot(expected.ID, fields, expected)
	if err == nil || !strings.Contains(err.Error(), "agent_bead") {
		t.Fatalf("validateTerminalMRCloseSnapshot error = %v, want agent_bead drift failure", err)
	}
}

func TestValidateTerminalMRCloseSnapshotRejectsAgentInjectionWhenExpectedEmpty(t *testing.T) {
	expected := &MergeRequest{
		ID:           "gt-mr-proof",
		Branch:       "polecat/test/proof",
		IssueID:      "gt-proof",
		TargetBranch: "main",
		CommitSHA:    "abc123",
	}
	fields := &beads.MRFields{
		Branch:      expected.Branch,
		SourceIssue: expected.IssueID,
		Target:      expected.TargetBranch,
		CommitSHA:   expected.CommitSHA,
		AgentBead:   "hq-agent-injected",
	}

	err := validateTerminalMRCloseSnapshot(expected.ID, fields, expected)
	if err == nil || !strings.Contains(err.Error(), "agent_bead") {
		t.Fatalf("validateTerminalMRCloseSnapshot error = %v, want empty-to-nonempty agent_bead drift failure", err)
	}
}
