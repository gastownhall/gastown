package polecat

import (
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// ActiveWorkReader is the beads subset needed to decide whether cleanup would
// discard work still assigned to a polecat.
type ActiveWorkReader interface {
	IssueReader
	ListByAssignee(assignee string) ([]*beads.Issue, error)
}

// ActiveWorkEvidence is the shared cleanup/recovery gate for polecat work.
// BlocksCleanup is broader than Active: protected states and lookup failures are
// unsafe for cleanup even when they are not proof that work is actively running.
type ActiveWorkEvidence struct {
	Active          bool
	Protected       bool
	BlocksCleanup   bool
	RequiresRestart bool
	Blocker         string
	HookBead        string
	HookSafe        bool
	HookTerminal    bool
	AssignedIssue   string
}

// AssessActiveWork returns the active/protected/unsafe work evidence for a
// polecat. Direct assignment is authoritative; legacy hook_bead and agent_state
// are compatibility and lifecycle evidence.
func AssessActiveWork(reader ActiveWorkReader, assignee string, agentState beads.AgentState, hookBead string) ActiveWorkEvidence {
	result := ActiveWorkEvidence{HookSafe: true}

	for _, evidence := range []ActiveWorkEvidence{
		assessAssignedWork(reader, assignee),
		AssessHookWork(reader, hookBead),
		AssessAgentStateWork(agentState),
	} {
		result.Merge(evidence)
	}

	return result
}

// Merge folds additional evidence into e while preserving the first blocker in
// caller-supplied priority order. Hook metadata only changes when the incoming
// evidence is actually about a hook, so lifecycle evidence cannot clobber it.
func (e *ActiveWorkEvidence) Merge(next ActiveWorkEvidence) {
	e.Active = e.Active || next.Active
	e.Protected = e.Protected || next.Protected
	e.BlocksCleanup = e.BlocksCleanup || next.BlocksCleanup
	e.RequiresRestart = e.RequiresRestart || next.RequiresRestart
	if e.Blocker == "" && next.Blocker != "" {
		e.Blocker = next.Blocker
	}
	if next.HookBead != "" {
		e.HookBead = next.HookBead
		e.HookSafe = next.HookSafe
		e.HookTerminal = next.HookTerminal
	}
	if e.AssignedIssue == "" && next.AssignedIssue != "" {
		e.AssignedIssue = next.AssignedIssue
	}
}

// AssessHookWork classifies legacy hook_bead evidence. Lookup uncertainty fails
// closed for cleanup; callers with verified reaped status can use
// AssessHookStatus directly.
func AssessHookWork(reader IssueReader, hookBead string) ActiveWorkEvidence {
	hookBead = strings.TrimSpace(hookBead)
	if hookBead == "" {
		return ActiveWorkEvidence{HookSafe: true}
	}
	if reader == nil {
		return hookEvidence(hookBead, false, false, false, fmt.Sprintf("hook_bead=%s status=unverified", hookBead))
	}
	issue, err := reader.Show(hookBead)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) {
			return hookEvidence(hookBead, false, false, false, fmt.Sprintf("hook_bead=%s status=missing", hookBead))
		}
		return hookEvidence(hookBead, false, false, false, fmt.Sprintf("hook_bead=%s status=lookup_error: %v", hookBead, err))
	}
	if issue == nil {
		return hookEvidence(hookBead, false, false, false, fmt.Sprintf("hook_bead=%s status=missing", hookBead))
	}
	return AssessHookStatus(hookBead, issue.Status, true)
}

// AssessHookStatus classifies a hook when the caller already has a status. A
// verified empty status represents a reaped terminal hook; unverified empty
// status remains unsafe.
func AssessHookStatus(hookBead, status string, verified bool) ActiveWorkEvidence {
	hookBead = strings.TrimSpace(hookBead)
	if hookBead == "" {
		return ActiveWorkEvidence{HookSafe: true}
	}
	if !verified {
		return hookEvidence(hookBead, false, false, false, fmt.Sprintf("hook_bead=%s status=unverified", hookBead))
	}
	status = strings.TrimSpace(status)
	if status == "" || beads.IssueStatus(status).IsTerminal() {
		return hookEvidence(hookBead, true, true, false, "")
	}
	return hookEvidence(hookBead, false, false, issueStatusRequiresRestart(beads.IssueStatus(status)), fmt.Sprintf("hook_bead=%s status=%s", hookBead, status))
}

func hookEvidence(hookBead string, safe, terminal, active bool, blocker string) ActiveWorkEvidence {
	return ActiveWorkEvidence{
		Active:          active,
		Protected:       blocker != "" && !active,
		BlocksCleanup:   blocker != "",
		RequiresRestart: active,
		Blocker:         blocker,
		HookBead:        hookBead,
		HookSafe:        safe,
		HookTerminal:    terminal,
	}
}

// AssessAgentStateWork classifies agent_state as lifecycle evidence. Protected
// states block cleanup without implying an automatic restart.
func AssessAgentStateWork(state beads.AgentState) ActiveWorkEvidence {
	if state == "" || state == beads.AgentStateIdle || state == beads.AgentStateDone || state == beads.AgentStateNuked {
		return ActiveWorkEvidence{HookSafe: true}
	}
	if state.IsActive() {
		return ActiveWorkEvidence{Active: true, BlocksCleanup: true, RequiresRestart: true, Blocker: fmt.Sprintf("agent_state=%s", state), HookSafe: true}
	}
	if state.ProtectsFromCleanup() || state == beads.AgentStateEscalated {
		return ActiveWorkEvidence{Protected: true, BlocksCleanup: true, Blocker: fmt.Sprintf("agent_state=%s", state), HookSafe: true}
	}
	return ActiveWorkEvidence{HookSafe: true}
}

func assessAssignedWork(reader ActiveWorkReader, assignee string) ActiveWorkEvidence {
	assignee = strings.TrimSpace(assignee)
	if assignee == "" {
		return ActiveWorkEvidence{HookSafe: true}
	}
	if reader == nil {
		return ActiveWorkEvidence{BlocksCleanup: true, Blocker: fmt.Sprintf("assigned_work assignee=%s status=unverified", assignee), HookSafe: true}
	}
	issues, err := reader.ListByAssignee(assignee)
	if err != nil {
		return ActiveWorkEvidence{BlocksCleanup: true, Blocker: fmt.Sprintf("assigned_work assignee=%s status=lookup_error: %v", assignee, err), HookSafe: true}
	}
	for _, issue := range issues {
		if !assignedIssueBlocksCleanup(issue) {
			continue
		}
		active := assignedIssueRequiresRestart(issue)
		return ActiveWorkEvidence{
			Active:          active,
			Protected:       !active,
			BlocksCleanup:   true,
			RequiresRestart: active,
			Blocker:         fmt.Sprintf("assigned_work=%s status=%s", issue.ID, issue.Status),
			AssignedIssue:   issue.ID,
			HookSafe:        true,
		}
	}
	return ActiveWorkEvidence{HookSafe: true}
}

func assignedIssueBlocksCleanup(issue *beads.Issue) bool {
	if issue == nil || beads.IsAgentBead(issue) || beads.IsProtectedBead(issue) {
		return false
	}
	return !beads.IssueStatus(issue.Status).IsTerminal()
}

func assignedIssueRequiresRestart(issue *beads.Issue) bool {
	if issue == nil {
		return false
	}
	return issueStatusRequiresRestart(beads.IssueStatus(issue.Status))
}

func issueStatusRequiresRestart(status beads.IssueStatus) bool {
	switch status {
	case beads.StatusOpen, beads.StatusInProgress, beads.IssueStatusHooked:
		return true
	default:
		return false
	}
}
