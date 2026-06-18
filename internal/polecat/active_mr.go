package polecat

import (
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/workitem"
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
	ActiveMR        string
	Pending         bool
	Reason          string
	MRStatus        string
	SourceIssue     string
	SourceTerminal  bool
	SourceMalformed bool
	Stale           bool
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
		// Open MRs must carry their own source_issue. Do not let agent hints mask
		// malformed queue state.
		result.SourceIssue = sourceIssueFromMR(mr)
		if sourceStatus := assessActiveMRSource(reader, result.SourceIssue); sourceStatus.malformed {
			result.SourceMalformed = true
			result.Reason = fmt.Sprintf("active_mr=%s status=%s %s reconcile_needed=malformed_source", mrID, mr.Status, sourceStatus.reason)
			return result
		} else if sourceStatus.lookupBlocked {
			result.Reason = fmt.Sprintf("active_mr=%s status=%s %s", mrID, mr.Status, sourceStatus.reason)
			return result
		}
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
	sourceStatus := assessActiveMRSource(reader, sourceIssue)
	if sourceStatus.malformed {
		result.SourceMalformed = true
		result.Reason = fmt.Sprintf("active_mr=%s status=%s %s reconcile_needed=malformed_source", result.ActiveMR, mrStatus, sourceStatus.reason)
		return result
	}
	terminal, reason := sourceStatus.terminal, sourceStatus.reason
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
		if source := sourceIssueFromMR(mr); source != "" {
			return source
		}
	}
	return normalizeSourceIssue(hint)
}

func sourceIssueFromMR(mr *beads.Issue) string {
	if mr == nil {
		return ""
	}
	if fields := beads.ParseMRFields(mr); fields != nil {
		return normalizeSourceIssue(fields.SourceIssue)
	}
	return ""
}

func normalizeSourceIssue(source string) string {
	source = strings.TrimSpace(source)
	if strings.EqualFold(source, "null") {
		return ""
	}
	return source
}

type activeMRSourceStatus struct {
	terminal      bool
	reason        string
	malformed     bool
	lookupBlocked bool
}

func assessActiveMRSource(reader IssueReader, sourceIssue string) activeMRSourceStatus {
	if sourceIssue == "" {
		return activeMRSourceStatus{reason: "source_issue=<missing>", malformed: true}
	}
	if reader == nil {
		return activeMRSourceStatus{reason: fmt.Sprintf("source_issue=%s source_status=unverified", sourceIssue), lookupBlocked: true}
	}
	issue, err := reader.Show(sourceIssue)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) {
			return activeMRSourceStatus{reason: fmt.Sprintf("source_issue=%s source_status=missing", sourceIssue), malformed: true}
		}
		return activeMRSourceStatus{reason: fmt.Sprintf("source_issue=%s source_status=lookup_error: %v", sourceIssue, err), lookupBlocked: true}
	}
	if issue == nil {
		return activeMRSourceStatus{reason: fmt.Sprintf("source_issue=%s source_status=missing", sourceIssue), malformed: true}
	}
	assessment := workitem.AssessConcrete(workitem.Snapshot{
		ID:        issue.ID,
		Title:     issue.Title,
		Type:      issue.Type,
		Labels:    issue.Labels,
		Ephemeral: issue.Ephemeral,
	})
	if !assessment.Concrete {
		return activeMRSourceStatus{reason: fmt.Sprintf("source_issue=%s source_status=non_concrete:%s", sourceIssue, assessment.Reason), malformed: true}
	}
	if beads.IssueStatus(issue.Status).IsTerminal() {
		return activeMRSourceStatus{terminal: true}
	}
	return activeMRSourceStatus{reason: fmt.Sprintf("source_issue=%s source_status=%s", sourceIssue, issue.Status)}
}
