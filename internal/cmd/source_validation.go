package cmd

import (
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

type submitSourceIssue struct {
	ID              string
	Issue           *beads.Issue
	BD              *beads.Beads
	CurrentBeadsDir string
	RoutedBeadsDir  string
}

func routedIssueBeads(cwd, issueID string) (*beads.Beads, string, string) {
	currentBeadsDir := beads.ResolveBeadsDir(cwd)
	routedBeadsDir := beads.ResolveBeadsDirForID(currentBeadsDir, issueID)
	return beads.NewWithBeadsDir(cwd, routedBeadsDir), currentBeadsDir, routedBeadsDir
}

func sourceRouteContext(currentBeadsDir, routedBeadsDir string) string {
	return fmt.Sprintf("current_db=%s routed_db=%s", currentBeadsDir, routedBeadsDir)
}

func resolveSubmitSourceIssue(cwd, issueID string) (*submitSourceIssue, error) {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return nil, fmt.Errorf("source_issue is required")
	}

	sourceBD, currentBeadsDir, routedBeadsDir := routedIssueBeads(cwd, issueID)
	issue, err := sourceBD.Show(issueID)
	if err != nil {
		return nil, fmt.Errorf("source_issue %s could not be resolved (%s): %w", issueID, sourceRouteContext(currentBeadsDir, routedBeadsDir), err)
	}
	if err := validateConcreteSourceIssue(issueID, issue); err != nil {
		return nil, err
	}
	return &submitSourceIssue{
		ID:              issueID,
		Issue:           issue,
		BD:              sourceBD,
		CurrentBeadsDir: currentBeadsDir,
		RoutedBeadsDir:  routedBeadsDir,
	}, nil
}

func validateConcreteSourceIssue(issueID string, issue *beads.Issue) error {
	if reason := beads.ConcreteWorkIssueRejectReason(issue); reason != "" {
		return fmt.Errorf("source_issue %s is not concrete (%s)", issueID, reason)
	}
	return nil
}

func validateMergeRequestSource(mr *beads.Issue, expectedIssueID string, expectedIssue *beads.Issue) error {
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
	if expectedIssue == nil {
		return fmt.Errorf("source_issue %s was not pre-resolved for merge request validation", sourceIssueID)
	}
	if resolvedID := strings.TrimSpace(expectedIssue.ID); resolvedID != "" && resolvedID != sourceIssueID {
		return fmt.Errorf("pre-resolved source_issue %s does not match merge request source_issue %s", resolvedID, sourceIssueID)
	}
	return validateConcreteSourceIssue(sourceIssueID, expectedIssue)
}
