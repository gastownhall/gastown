package refinery

import (
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

type postMergeCoordinator struct {
	loadMR        func(string) (*MergeRequest, error)
	verifyProof   func(*MergeRequest) error
	closeMR       func(*MergeRequest, string) error
	loadSource    func(string) (*beads.Issue, error)
	closeSource   func(string, string) error
	loadAgent     func(string) (*beads.Issue, error)
	finalizeAgent func(string, string) error
	releaseSlot   func() error
}

type postMergeProofGit interface {
	VerifyPushedCommitReachableFromPushTarget(remote, target, commit string) error
}

func verifyPostMergeProof(g postMergeProofGit, mr *MergeRequest) error {
	if mr == nil {
		return fmt.Errorf("merge request is missing")
	}
	if g == nil {
		return fmt.Errorf("git client is missing")
	}
	target := strings.TrimSpace(mr.TargetBranch)
	if target == "" {
		return fmt.Errorf("missing target branch")
	}
	if source := strings.TrimSpace(mr.Branch); source != "" && source == target {
		return fmt.Errorf("source branch %s matches target branch", source)
	}
	commit := strings.TrimSpace(mr.CommitSHA)
	if commit == "" {
		return fmt.Errorf("missing submitted commit_sha")
	}
	if err := g.VerifyPushedCommitReachableFromPushTarget("origin", target, commit); err != nil {
		return fmt.Errorf("target %s does not contain submitted head %s: %w", target, commit, err)
	}
	return nil
}

func newPostMergeCoordinator(
	b *beads.Beads,
	defaultBranch string,
	verifyProof func(*MergeRequest) error,
	releaseSlot func() error,
) *postMergeCoordinator {
	agentBeads := b.ForAgentBead()
	if releaseSlot == nil {
		releaseSlot = func() error { return nil }
	}
	return &postMergeCoordinator{
		loadMR: func(id string) (*MergeRequest, error) {
			issue, err := b.Show(id)
			if err != nil {
				return nil, err
			}
			if issue == nil || !beads.HasLabel(issue, "gt:merge-request") {
				return nil, ErrMRNotFound
			}
			return mergeRequestFromIssue(issue, defaultBranch), nil
		},
		verifyProof: verifyProof,
		closeMR: func(mr *MergeRequest, mergeCommit string) error {
			_, err := closeTerminalMR(b, mr.ID, terminalMRCloseOptions{
				Reason:        string(CloseReasonMerged),
				MergeCommit:   mergeCommit,
				AgentBeadHint: mr.AgentBead,
				ExpectedMR:    mr,
			})
			return err
		},
		loadSource: b.Show,
		closeSource: func(id, reason string) error {
			return b.ForceCloseWithReason(reason, id)
		},
		loadAgent: agentBeads.Show,
		finalizeAgent: func(id, mrID string) error {
			return agentBeads.FinalizeAgentAfterMergedMR(id, mrID)
		},
		releaseSlot: releaseSlot,
	}
}

func (c *postMergeCoordinator) run(expected *MergeRequest, mergeCommit string) (*PostMergeResult, error) {
	if expected == nil {
		return nil, ErrMRNotFound
	}
	mr, err := c.loadMR(expected.ID)
	if err != nil {
		return nil, fmt.Errorf("load authoritative MR: %w", err)
	}
	result := &PostMergeResult{MR: mr}
	if mr == nil {
		return result, fmt.Errorf("load authoritative MR %s: missing", expected.ID)
	}
	if err := requireSameMergeRequestSnapshot(expected, mr); err != nil {
		return result, err
	}
	proofSnapshot := *mr
	if err := c.verifyProof(mr); err != nil {
		return result, fmt.Errorf("post-merge proof: %w", err)
	}
	mr, err = c.loadMR(expected.ID)
	if err != nil {
		return result, fmt.Errorf("reload MR after post-merge proof: %w", err)
	}
	if mr == nil {
		return result, fmt.Errorf("reload MR %s after post-merge proof: missing", expected.ID)
	}
	if err := validateMergeRequestIdentity(&proofSnapshot, mr, "after merge proof"); err != nil {
		return result, err
	}
	result.MR = mr

	if err := c.closeMR(mr, mergeCommit); err != nil {
		return result, fmt.Errorf("close MR: %w", err)
	}
	mr, err = c.loadMR(expected.ID)
	if err != nil {
		return result, fmt.Errorf("reread closed MR: %w", err)
	}
	if mr == nil || mr.Status != MRClosed || mr.CloseReason != CloseReasonMerged {
		status, reason := MRStatus(""), CloseReason("")
		if mr != nil {
			status, reason = mr.Status, mr.CloseReason
		}
		return result, fmt.Errorf("authoritative MR %s is not merged terminal: status=%q close_reason=%q", expected.ID, status, reason)
	}
	result.MR = mr
	result.MRClosed = true

	sourceID := strings.TrimSpace(mr.IssueID)
	if sourceID == "" {
		agentID := strings.TrimSpace(mr.AgentBead)
		if agentID != "" {
			agent, loadErr := c.loadAgent(agentID)
			if loadErr != nil {
				return result, fmt.Errorf("resolve source identity via agent %s: %w", agentID, loadErr)
			}
			if agent == nil || !beads.IsAgentBead(agent) {
				return result, fmt.Errorf("resolve source identity via agent %s: missing or invalid agent", agentID)
			}
			fields := beads.ParseAgentFields(agent.Description)
			if (strings.TrimSpace(fields.ActiveMR) == mr.ID || strings.TrimSpace(fields.MRID) == mr.ID) &&
				strings.TrimSpace(fields.Branch) == strings.TrimSpace(mr.Branch) {
				sourceID = cleanWorkBeadID(fields.LastSourceIssue)
			}
		}
		if sourceID == "" {
			return result, fmt.Errorf("resolve source identity for MR %s: unresolved", mr.ID)
		}
	}
	result.SourceIssueID = sourceID
	source, err := c.loadSource(sourceID)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) {
			result.SourceIssueNotFound = true
		} else {
			return result, fmt.Errorf("load source %s: %w", sourceID, err)
		}
	} else {
		if source == nil {
			return result, fmt.Errorf("load source %s: missing without ErrNotFound", sourceID)
		}
		if reason := beads.ConcreteWorkIssueRejectReason(source); reason != "" {
			return result, fmt.Errorf("source %s is not concrete work: %s", sourceID, reason)
		}
		if reason := refineryMergedWorkBeadCloseBlockReason(source); reason != "" {
			return result, fmt.Errorf("source %s is not merge-closeable: %s", sourceID, reason)
		}
		if beads.IssueStatus(strings.TrimSpace(source.Status)).IsTerminal() {
			result.SourceIssueClosed = true
		} else {
			reason := fmt.Sprintf("Merged in %s\ntarget_branch: %s\ncommit_sha: %s", mr.ID, mr.TargetBranch, mergeCommit)
			if err := c.closeSource(sourceID, reason); err != nil {
				return result, fmt.Errorf("close source %s: %w", sourceID, err)
			}
			source, err = c.loadSource(sourceID)
			if err != nil {
				if errors.Is(err, beads.ErrNotFound) {
					result.SourceIssueNotFound = true
				} else {
					return result, fmt.Errorf("reread source %s after close: %w", sourceID, err)
				}
			} else if source == nil || !beads.IssueStatus(strings.TrimSpace(source.Status)).IsTerminal() {
				return result, fmt.Errorf("source %s is not authoritatively terminal after close", sourceID)
			} else {
				result.SourceIssueClosed = true
			}
		}
	}

	agentID := strings.TrimSpace(mr.AgentBead)
	if agentID == "" {
		return result, fmt.Errorf("resolve agent identity for MR %s: unresolved", mr.ID)
	}
	agent, err := c.loadAgent(agentID)
	if err != nil {
		return result, fmt.Errorf("load agent %s: %w", agentID, err)
	}
	if agent == nil {
		return result, fmt.Errorf("load agent %s: missing", agentID)
	}
	if !beads.IsAgentBead(agent) {
		return result, fmt.Errorf("%s is not an agent bead", agentID)
	}
	fields := beads.ParseAgentFields(agent.Description)
	if agentAlreadyFinalized(fields) {
		return result, nil
	}
	if err := requireFinalizableAgent(fields, mr.ID); err != nil {
		return result, fmt.Errorf("agent %s not finalizable: %w", agentID, err)
	}
	if err := c.finalizeAgent(agentID, mr.ID); err != nil {
		return result, fmt.Errorf("finalize agent %s: %w", agentID, err)
	}
	if err := c.releaseSlot(); err != nil {
		return result, fmt.Errorf("release merge slot: %w", err)
	}
	return result, nil
}

func requireSameMergeRequestSnapshot(expected, actual *MergeRequest) error {
	return validateMergeRequestIdentity(expected, actual, "before post-merge finalization")
}

func validateMergeRequestIdentity(expected, actual *MergeRequest, boundary string) error {
	if expected == nil || actual == nil {
		return fmt.Errorf("cannot compare missing merge request identity")
	}
	checks := []struct {
		name string
		want string
		got  string
	}{
		{"id", expected.ID, actual.ID},
		{"branch", expected.Branch, actual.Branch},
		{"source_issue", expected.IssueID, actual.IssueID},
		{"target", expected.TargetBranch, actual.TargetBranch},
		{"commit_sha", expected.CommitSHA, actual.CommitSHA},
		{"agent_bead", expected.AgentBead, actual.AgentBead},
	}
	for _, check := range checks {
		if strings.TrimSpace(check.want) != strings.TrimSpace(check.got) {
			return fmt.Errorf("MR %s changed %s: %s=%q, verified %q",
				expected.ID, boundary, check.name, strings.TrimSpace(check.got), strings.TrimSpace(check.want))
		}
	}
	return nil
}

func requireFinalizableAgent(fields *beads.AgentFields, mrID string) error {
	if fields == nil {
		return fmt.Errorf("missing agent fields")
	}
	if strings.TrimSpace(fields.AgentState) != string(beads.AgentStateDone) {
		return fmt.Errorf("agent_state=%q, want done", fields.AgentState)
	}
	if strings.TrimSpace(fields.ActiveMR) != strings.TrimSpace(mrID) {
		return fmt.Errorf("active_mr=%q, want %q", fields.ActiveMR, mrID)
	}
	if strings.TrimSpace(fields.CleanupStatus) != "clean" {
		return fmt.Errorf("cleanup_status=%q, want clean", fields.CleanupStatus)
	}
	if valuePresent(fields.HookBead) {
		return fmt.Errorf("hook_bead=%q, want empty", fields.HookBead)
	}
	if fields.MRFailed {
		return fmt.Errorf("mr_failed=true")
	}
	if fields.PushFailed {
		return fmt.Errorf("push_failed=true")
	}
	return nil
}

func agentAlreadyFinalized(fields *beads.AgentFields) bool {
	return fields != nil &&
		strings.TrimSpace(fields.AgentState) == string(beads.AgentStateIdle) &&
		!valuePresent(fields.ActiveMR) &&
		strings.TrimSpace(fields.CleanupStatus) == "clean" &&
		!valuePresent(fields.HookBead) &&
		!fields.MRFailed &&
		!fields.PushFailed
}

func valuePresent(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.EqualFold(value, "null")
}

func mergeRequestFromIssue(issue *beads.Issue, defaultBranch string) *MergeRequest {
	if issue == nil {
		return nil
	}
	fields := beads.ParseMRFields(issue)
	if fields == nil {
		return &MergeRequest{
			ID:           issue.ID,
			IssueID:      issue.ID,
			TargetBranch: strings.TrimSpace(defaultBranch),
			Status:       mrStatusFromIssue(issue),
			CreatedAt:    parseTime(issue.CreatedAt),
		}
	}
	target := strings.TrimSpace(fields.Target)
	if target == "" {
		target = strings.TrimSpace(defaultBranch)
	}
	return &MergeRequest{
		ID:           issue.ID,
		Branch:       fields.Branch,
		Worker:       fields.Worker,
		AgentBead:    fields.AgentBead,
		IssueID:      fields.SourceIssue,
		TargetBranch: target,
		CommitSHA:    fields.CommitSHA,
		PRURL:        fields.PRURL,
		PRNumber:     fields.PRNumber,
		MergeCommit:  fields.MergeCommit,
		Status:       mrStatusFromIssue(issue),
		CloseReason:  CloseReason(fields.CloseReason),
		CreatedAt:    parseTime(issue.CreatedAt),
	}
}
