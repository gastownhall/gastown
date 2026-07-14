package cmd

import (
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

func validateConcreteSourceIssue(issueID string, issue *beads.Issue) error {
	if reason := beads.ConcreteWorkIssueRejectReason(issue); reason != "" {
		return fmt.Errorf("source_issue %s is not concrete (%s)", issueID, reason)
	}
	return nil
}

func validateMergeRequestSource(bd *beads.Beads, mr *beads.Issue, expectedIssueID string) error {
	if mr == nil {
		return fmt.Errorf("merge request is missing")
	}
	fields := beads.ParseMRFields(mr)
	if fields == nil || strings.TrimSpace(fields.SourceIssue) == "" {
		return fmt.Errorf("merge request %s has missing source_issue", mr.ID)
	}
	sourceIssueID := strings.TrimSpace(fields.SourceIssue)
	if sourceIssueID != strings.TrimSpace(expectedIssueID) {
		return fmt.Errorf("merge request %s source_issue %s does not match expected %s", mr.ID, sourceIssueID, expectedIssueID)
	}
	sourceIssue, err := bd.Show(sourceIssueID)
	if err != nil {
		return fmt.Errorf("source_issue %s could not be resolved: %w", sourceIssueID, err)
	}
	return validateConcreteSourceIssue(sourceIssueID, sourceIssue)
}
