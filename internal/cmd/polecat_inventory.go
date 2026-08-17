package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/polecat"
)

const polecatSessionKeySep = "\x00"

type polecatSessionSet map[string]string

type polecatInventoryItem struct {
	Rig            string
	Name           string
	State          polecat.State
	Issue          string
	CleanupStatus  string
	ActiveMR       string
	Branch         string
	SessionRunning bool
	SessionName    string
	Disposition    polecat.WorkstateDisposition
}

type polecatActiveWorkEvidence struct {
	BlocksCleanup        bool
	RequiresRestart      bool
	CountsTowardCapacity bool
	Blocker              string
	AssignedIssue        string
}

func newPolecatSessionSet(sessionNames []string) polecatSessionSet {
	sessions := make(polecatSessionSet, len(sessionNames))
	for _, sessionName := range sessionNames {
		rigName, polecatName, ok := parsePolecatSessionName(sessionName)
		if !ok {
			continue
		}
		sessions[polecatSessionKey(rigName, polecatName)] = sessionName
	}
	return sessions
}

func (s polecatSessionSet) lookup(rigName, polecatName string) (string, bool) {
	if s == nil {
		return "", false
	}
	sessionName, ok := s[polecatSessionKey(rigName, polecatName)]
	return sessionName, ok
}

func (s polecatSessionSet) namesForRig(rigName string) []string {
	if len(s) == 0 {
		return nil
	}
	var names []string
	for _, sessionName := range s {
		sessionRig, _, ok := parsePolecatSessionName(sessionName)
		if ok && sessionRig == rigName {
			names = append(names, sessionName)
		}
	}
	sort.Strings(names)
	return names
}

func polecatSessionKey(rigName, polecatName string) string {
	return rigName + polecatSessionKeySep + polecatName
}

func buildPolecatInventoryItem(rigName, polecatName, clonePath string, bd *beads.Beads, fields *beads.AgentFields, activeWork *beads.Issue, sessions polecatSessionSet) polecatInventoryItem {
	sessionName, running := sessions.lookup(rigName, polecatName)
	item := polecatInventoryItem{
		Rig:            rigName,
		Name:           polecatName,
		State:          polecat.StateIdle,
		SessionRunning: running,
		SessionName:    sessionName,
	}
	if fields != nil {
		item.CleanupStatus = strings.TrimSpace(fields.CleanupStatus)
		item.ActiveMR = strings.TrimSpace(fields.ActiveMR)
		item.Branch = strings.TrimSpace(fields.Branch)
		if beads.AgentState(strings.TrimSpace(fields.AgentState)) == beads.AgentStateDone {
			item.State = polecat.StateDone
		}
	}
	issue := ""
	if activeWork != nil {
		issue = activeWork.ID
		evidence := assessPolecatAssignedIssueWork(activeWork)
		if evidence.RequiresRestart || evidence.CountsTowardCapacity {
			if running {
				item.State = polecat.StateWorking
			} else {
				item.State = polecat.StateStalled
			}
		}
	}
	item.Issue = issue
	item.Disposition = polecat.InspectWorkstate(polecatName, bd, clonePath, item.State, issue)
	return item
}

func failClosedAssignedWorkLookup(d polecat.WorkstateDisposition, err error) polecat.WorkstateDisposition {
	if err == nil {
		return d
	}
	d.Reusable = false
	d.SafeToNuke = false
	d.NeedsRecovery = true
	d.Verdict = polecat.WorkstateVerdictNeedsRecovery
	d.Reason = "active-work"
	d.ReuseStatus = "idle-recovery-needed"
	d.Blockers = append([]string{fmt.Sprintf("assigned_work status=lookup_error: %v", err)}, d.Blockers...)
	return d
}

var polecatSummaryWorkStatuses = []beads.IssueStatus{
	beads.IssueStatusHooked,
	beads.StatusInProgress,
	beads.StatusOpen,
	beads.StatusBlocked,
	beads.StatusDeferred,
}

var polecatSummaryWorkStatusRank = func() map[string]int {
	ranks := make(map[string]int, len(polecatSummaryWorkStatuses))
	for i, status := range polecatSummaryWorkStatuses {
		ranks[string(status)] = i
	}
	return ranks
}()

func listActivePolecatWorkByName(bd *beads.Beads, rigName string) (map[string]*beads.Issue, error) {
	byName := make(map[string]*beads.Issue)
	issues, err := bd.ListIssueStatuses(polecatSummaryWorkStatuses...)
	if err != nil {
		return nil, err
	}
	for _, issue := range issues {
		evidence := assessPolecatAssignedIssueWork(issue)
		if !evidence.BlocksCleanup {
			continue
		}
		name, ok := polecatNameFromAssignee(rigName, issue.Assignee)
		if !ok {
			continue
		}
		if current := byName[name]; current == nil || polecatSummaryIssueRank(issue) < polecatSummaryIssueRank(current) {
			byName[name] = issue
		}
	}
	return byName, nil
}

func polecatSummaryIssueRank(issue *beads.Issue) int {
	if issue == nil {
		return len(polecatSummaryWorkStatuses)
	}
	if rank, ok := polecatSummaryWorkStatusRank[issue.Status]; ok {
		return rank
	}
	return len(polecatSummaryWorkStatuses)
}

func polecatNameFromAssignee(rigName, assignee string) (string, bool) {
	prefix := rigName + "/polecats/"
	if !strings.HasPrefix(assignee, prefix) {
		return "", false
	}
	name := strings.TrimPrefix(assignee, prefix)
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}

func assessPolecatAssignedIssueWork(issue *beads.Issue) polecatActiveWorkEvidence {
	if issue == nil || beads.IsAgentBead(issue) || beads.IsProtectedBead(issue) || beads.IssueStatus(issue.Status).IsTerminal() {
		return polecatActiveWorkEvidence{}
	}
	requiresRestart := polecatSummaryIssueRequiresRestart(beads.IssueStatus(issue.Status))
	return polecatActiveWorkEvidence{
		BlocksCleanup:        true,
		RequiresRestart:      requiresRestart,
		CountsTowardCapacity: requiresRestart,
		Blocker:              fmt.Sprintf("assigned_work=%s status=%s", issue.ID, issue.Status),
		AssignedIssue:        issue.ID,
	}
}

func polecatSummaryIssueRequiresRestart(status beads.IssueStatus) bool {
	switch status {
	case beads.IssueStatusHooked, beads.StatusInProgress, beads.StatusOpen:
		return true
	default:
		return false
	}
}

func parsePolecatAgentFields(issue *beads.Issue) *beads.AgentFields {
	if issue == nil {
		return nil
	}
	fields := beads.ParseAgentFields(issue.Description)
	fields.AgentState = beads.ResolveAgentState(issue.Description, issue.AgentState)
	return fields
}
