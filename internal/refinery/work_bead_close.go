package refinery

import (
	"fmt"
	"io"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

type mergedWorkBeadCloseRequest struct {
	MRID        string
	Branch      string
	Target      string
	SourceIssue string
	AgentBead   string
	MergeCommit string
}

type mergedWorkBeadCloseResult struct {
	WorkBeadID string
	Closed     bool
	NotFound   bool
}

type workBeadCloser interface {
	Show(id string) (*beads.Issue, error)
	ForceCloseWithReason(reason string, ids ...string) error
}

type issueReader interface {
	Show(id string) (*beads.Issue, error)
}

func closeMergedWorkBead(work workBeadCloser, agent issueReader, out io.Writer, req mergedWorkBeadCloseRequest) mergedWorkBeadCloseResult {
	logf := func(format string, args ...interface{}) {
		if out != nil {
			_, _ = fmt.Fprintf(out, format, args...)
		}
	}

	workBeadID := resolveMergedWorkBead(agent, req)
	result := mergedWorkBeadCloseResult{WorkBeadID: workBeadID}
	if workBeadID == "" {
		logf("[Refinery] Note: merged MR %s has no resolvable work bead to close\n", req.MRID)
		result.NotFound = true
		return result
	}
	if work == nil {
		logf("[Refinery] Warning: no beads client available to close work bead %s\n", workBeadID)
		result.NotFound = true
		return result
	}

	issue, err := work.Show(workBeadID)
	if err != nil || issue == nil {
		logf("[Refinery] Warning: failed to fetch work bead %s: %v\n", workBeadID, err)
		result.NotFound = true
		return result
	}
	if reason := beads.ConcreteWorkIssueRejectReason(issue); reason != "" {
		logf("[Refinery] Warning: refusing to close non-concrete work bead %s (%s)\n", workBeadID, reason)
		result.NotFound = true
		return result
	}
	closeReasonPrefix := fmt.Sprintf("Merged in %s", req.MRID)
	closeReason := closeReasonPrefix
	if req.MergeCommit != "" {
		closeReason = fmt.Sprintf("%s\ntarget_branch: %s\ncommit_sha: %s", closeReason, req.Target, req.MergeCommit)
	}

	// `gt done` closes the source bead before the refinery finishes merging, so
	// the bead is usually already terminal by the time we get here. Returning
	// early left close_reason as whatever `gt done` wrote, which breaks
	// merge-blocks dependents: they only treat a blocker as satisfied when the
	// reason starts with "Merged in ". Stamp the merge reason instead.
	if beads.IssueStatus(strings.TrimSpace(issue.Status)).IsTerminal() {
		result.Closed = true
		if reason := refineryMergedWorkBeadCloseBlockReason(issue); reason != "" {
			logf("[Refinery] Work bead already closed: %s\n", workBeadID)
			return result
		}
		if strings.HasPrefix(strings.TrimSpace(issue.CloseReason), closeReasonPrefix) {
			logf("[Refinery] Work bead already closed with merge reason: %s\n", workBeadID)
			return result
		}
		logf("[Refinery] Work bead already closed, stamping merge reason: %s\n", workBeadID)
		if err := work.ForceCloseWithReason(closeReason, workBeadID); err != nil {
			logf("[Refinery] Warning: failed to stamp merge reason on %s: %v\n", workBeadID, err)
		}
		return result
	}
	if reason := refineryMergedWorkBeadCloseBlockReason(issue); reason != "" {
		logf("[Refinery] Warning: refusing to close non-mergeable work bead %s (%s)\n", workBeadID, reason)
		result.NotFound = true
		return result
	}

	if err := work.ForceCloseWithReason(closeReason, workBeadID); err != nil {
		if issue, showErr := work.Show(workBeadID); showErr == nil && issue != nil &&
			beads.ConcreteWorkIssueRejectReason(issue) == "" &&
			beads.IssueStatus(strings.TrimSpace(issue.Status)).IsTerminal() {
			logf("[Refinery] Work bead already closed: %s\n", workBeadID)
			result.Closed = true
			return result
		}
		logf("[Refinery] Warning: failed to close work bead %s: %v\n", workBeadID, err)
		result.NotFound = true
		return result
	}

	logf("[Refinery] Closed work bead: %s\n", workBeadID)
	result.Closed = true
	return result
}

func resolveMergedWorkBead(agent issueReader, req mergedWorkBeadCloseRequest) string {
	if sourceIssue := cleanWorkBeadID(req.SourceIssue); sourceIssue != "" {
		return sourceIssue
	}
	if agent == nil || cleanWorkBeadID(req.AgentBead) == "" || cleanWorkBeadID(req.MRID) == "" {
		return ""
	}

	agentIssue, err := agent.Show(req.AgentBead)
	if err != nil || !beads.IsAgentBead(agentIssue) {
		return ""
	}
	fields := beads.ParseAgentFields(agentIssue.Description)
	if fields == nil {
		return ""
	}
	if fields.ActiveMR != req.MRID && fields.MRID != req.MRID {
		return ""
	}
	agentBranch := strings.TrimSpace(fields.Branch)
	requestBranch := strings.TrimSpace(req.Branch)
	if agentBranch == "" || strings.EqualFold(agentBranch, "null") {
		return ""
	}
	if requestBranch == "" || strings.EqualFold(requestBranch, "null") || agentBranch != requestBranch {
		return ""
	}
	return cleanWorkBeadID(fields.LastSourceIssue)
}

func cleanWorkBeadID(id string) string {
	id = strings.TrimSpace(id)
	if strings.EqualFold(id, "null") {
		return ""
	}
	return id
}

func refineryMergedWorkBeadCloseBlockReason(issue *beads.Issue) string {
	if fields := beads.ParseAttachmentFields(issue); fields != nil {
		switch {
		case fields.NoMerge:
			return "no_merge"
		case fields.ReviewOnly:
			return "review_only"
		case strings.EqualFold(strings.TrimSpace(fields.MergeStrategy), "local"):
			return "merge_strategy:local"
		}
	}
	return ""
}
