package polecat

import (
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// IssueReader is the subset of beads lookup needed to classify active_mr.
type IssueReader interface {
	Show(issueID string) (*beads.Issue, error)
}

// ActiveMRInput describes the active merge-request context for a polecat.
type ActiveMRInput struct {
	ActiveMR        string
	SourceIssueHint string
	RequireGitSafe  bool
	GitSafe         bool
}

// ActiveMRAssessment is the shared active_mr classification used by recovery,
// reuse, and witness paths. Pending is fail-closed: lookup/source uncertainty
// remains blocking unless the stale MR and terminal source are both proven.
type ActiveMRAssessment struct {
	ActiveMR       string
	Pending        bool
	Reason         string
	MRStatus       string
	SourceIssue    string
	SourceTerminal bool
	Stale          bool
}

// AssessActiveMR returns whether active_mr still represents work pending in the
// merge queue. Missing/terminal MRs are stale only when the source issue is
// known terminal and, if requested, direct git state is safe.
func AssessActiveMR(reader IssueReader, in ActiveMRInput) ActiveMRAssessment {
	mrID := strings.TrimSpace(in.ActiveMR)
	if mrID == "" {
		return ActiveMRAssessment{}
	}
	result := ActiveMRAssessment{ActiveMR: mrID, Pending: true}
	if reader == nil {
		result.Reason = fmt.Sprintf("active_mr=%s status=unverified", mrID)
		return result
	}

	mr, err := reader.Show(mrID)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) {
			return assessStaleActiveMR(reader, in, result, "missing", nil)
		}
		result.Reason = fmt.Sprintf("active_mr=%s status=lookup_error: %v", mrID, err)
		return result
	}
	if mr == nil {
		return assessStaleActiveMR(reader, in, result, "missing", nil)
	}

	result.MRStatus = mr.Status
	if !beads.IssueStatus(mr.Status).IsTerminal() {
		result.Reason = fmt.Sprintf("active_mr=%s status=%s", mrID, mr.Status)
		return result
	}
	return assessStaleActiveMR(reader, in, result, mr.Status, mr)
}

func assessStaleActiveMR(reader IssueReader, in ActiveMRInput, result ActiveMRAssessment, mrStatus string, mr *beads.Issue) ActiveMRAssessment {
	result.MRStatus = mrStatus
	result.Stale = true
	sourceIssue := sourceIssueForActiveMR(in.SourceIssueHint, mr)
	result.SourceIssue = sourceIssue
	terminal, reason := terminalSourceIssue(reader, sourceIssue)
	result.SourceTerminal = terminal
	if !terminal {
		result.Reason = fmt.Sprintf("active_mr=%s status=%s %s", result.ActiveMR, mrStatus, reason)
		return result
	}
	if in.RequireGitSafe && !in.GitSafe {
		result.Reason = fmt.Sprintf("active_mr=%s status=%s source_issue=%s git_state=unsafe", result.ActiveMR, mrStatus, sourceIssue)
		return result
	}
	result.Pending = false
	result.Reason = ""
	return result
}

func sourceIssueForActiveMR(hint string, mr *beads.Issue) string {
	if mr != nil {
		if fields := beads.ParseMRFields(mr); fields != nil {
			if source := normalizeSourceIssue(fields.SourceIssue); source != "" {
				return source
			}
		}
	}
	return normalizeSourceIssue(hint)
}

func normalizeSourceIssue(source string) string {
	source = strings.TrimSpace(source)
	if strings.EqualFold(source, "null") {
		return ""
	}
	return source
}

func terminalSourceIssue(reader IssueReader, sourceIssue string) (bool, string) {
	if sourceIssue == "" {
		return false, "source_issue=<missing>"
	}
	if reader == nil {
		return false, fmt.Sprintf("source_issue=%s source_status=unverified", sourceIssue)
	}
	issue, err := reader.Show(sourceIssue)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) {
			return false, fmt.Sprintf("source_issue=%s source_status=missing", sourceIssue)
		}
		return false, fmt.Sprintf("source_issue=%s source_status=lookup_error: %v", sourceIssue, err)
	}
	if issue == nil {
		return false, fmt.Sprintf("source_issue=%s source_status=missing", sourceIssue)
	}
	if beads.IssueStatus(issue.Status).IsTerminal() {
		return true, ""
	}
	return false, fmt.Sprintf("source_issue=%s source_status=%s", sourceIssue, issue.Status)
}
