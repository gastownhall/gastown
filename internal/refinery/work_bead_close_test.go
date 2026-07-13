package refinery

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

type fakeWorkBeadStore struct {
	issues          map[string]*beads.Issue
	closeCalls      []string
	lastCloseReason string
	closeErr        error
	closeErrCloses  bool
}

func newFakeWorkBeadStore() *fakeWorkBeadStore {
	return &fakeWorkBeadStore{issues: map[string]*beads.Issue{}}
}

func (f *fakeWorkBeadStore) add(issue *beads.Issue) {
	f.issues[issue.ID] = issue
}

func (f *fakeWorkBeadStore) Show(id string) (*beads.Issue, error) {
	issue, ok := f.issues[id]
	if !ok {
		return nil, beads.ErrNotFound
	}
	return issue, nil
}

func (f *fakeWorkBeadStore) ForceCloseWithReason(reason string, ids ...string) error {
	f.lastCloseReason = reason
	f.closeCalls = append(f.closeCalls, ids...)
	if f.closeErr != nil {
		if f.closeErrCloses {
			for _, id := range ids {
				if issue, ok := f.issues[id]; ok {
					issue.Status = string(beads.StatusClosed)
				}
			}
		}
		return f.closeErr
	}
	for _, id := range ids {
		if issue, ok := f.issues[id]; ok {
			issue.Status = string(beads.StatusClosed)
		}
	}
	return nil
}

func workIssue(id string, status string) *beads.Issue {
	return &beads.Issue{ID: id, Title: id, Type: "bug", Status: status}
}

func agentIssue(id string, desc string) *beads.Issue {
	return &beads.Issue{ID: id, Title: id, Type: "agent", Labels: []string{"gt:agent"}, Status: string(beads.StatusOpen), Description: desc}
}

func TestCloseMergedWorkBead_SourceIssueWinsOverAgentFallback(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(workIssue("gt-source", string(beads.StatusOpen)))
	work.add(workIssue("gt-agent-hint", string(beads.StatusOpen)))
	agent := newFakeWorkBeadStore()
	agent.add(agentIssue("gt-agent", "active_mr: gt-mr\nlast_source_issue: gt-agent-hint\n"))

	result := closeMergedWorkBead(work, agent, nil, mergedWorkBeadCloseRequest{
		MRID:        "gt-mr",
		Branch:      "polecat/atom/gt-source+abc123",
		Target:      "main",
		SourceIssue: "gt-source",
		AgentBead:   "gt-agent",
		MergeCommit: "abc123",
	})

	if !result.Closed || result.WorkBeadID != "gt-source" {
		t.Fatalf("result = %+v, want closed gt-source", result)
	}
	if len(work.closeCalls) != 1 || work.closeCalls[0] != "gt-source" {
		t.Fatalf("close calls = %v, want [gt-source]", work.closeCalls)
	}
	if !strings.Contains(work.lastCloseReason, "Merged in gt-mr") || !strings.Contains(work.lastCloseReason, "commit_sha: abc123") {
		t.Fatalf("close reason missing merge metadata: %q", work.lastCloseReason)
	}
}

func TestCloseMergedWorkBead_FallsBackToVerifiedAgentSource(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(workIssue("gt-source", string(beads.StatusOpen)))
	agent := newFakeWorkBeadStore()
	agent.add(agentIssue("gt-agent", "active_mr: gt-mr\nbranch: polecat/atom/gt-source+abc123\nlast_source_issue: gt-source\n"))

	result := closeMergedWorkBead(work, agent, nil, mergedWorkBeadCloseRequest{
		MRID:      "gt-mr",
		Branch:    "polecat/atom/gt-source+abc123",
		Target:    "main",
		AgentBead: "gt-agent",
	})

	if !result.Closed || result.WorkBeadID != "gt-source" {
		t.Fatalf("result = %+v, want fallback close gt-source", result)
	}
	if len(work.closeCalls) != 1 || work.closeCalls[0] != "gt-source" {
		t.Fatalf("close calls = %v, want [gt-source]", work.closeCalls)
	}
}

func TestCloseMergedWorkBead_FallsBackToCompletionMRID(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(workIssue("gt-source", string(beads.StatusOpen)))
	agent := newFakeWorkBeadStore()
	agent.add(agentIssue("gt-agent", "mr_id: gt-mr\nbranch: polecat/atom/gt-source+abc123\nlast_source_issue: gt-source\n"))

	result := closeMergedWorkBead(work, agent, nil, mergedWorkBeadCloseRequest{
		MRID:      "gt-mr",
		Branch:    "polecat/atom/gt-source+abc123",
		AgentBead: "gt-agent",
	})

	if !result.Closed || result.WorkBeadID != "gt-source" {
		t.Fatalf("result = %+v, want completion-metadata fallback close", result)
	}
}

func TestCloseMergedWorkBead_RejectsUnverifiedAgentFallbacks(t *testing.T) {
	tests := []struct {
		name          string
		agentDesc     string
		agentType     string
		agentLabs     []string
		requestBranch string
	}{
		{name: "wrong active mr", agentDesc: "active_mr: gt-other\nlast_source_issue: gt-source\n", agentType: "agent", agentLabs: []string{"gt:agent"}, requestBranch: "polecat/atom/gt-source+abc123"},
		{name: "wrong completion mr", agentDesc: "mr_id: gt-other\nlast_source_issue: gt-source\n", agentType: "agent", agentLabs: []string{"gt:agent"}, requestBranch: "polecat/atom/gt-source+abc123"},
		{name: "branch mismatch", agentDesc: "active_mr: gt-mr\nbranch: polecat/other/gt-source+abc123\nlast_source_issue: gt-source\n", agentType: "agent", agentLabs: []string{"gt:agent"}, requestBranch: "polecat/atom/gt-source+abc123"},
		{name: "missing agent branch", agentDesc: "active_mr: gt-mr\nlast_source_issue: gt-source\n", agentType: "agent", agentLabs: []string{"gt:agent"}, requestBranch: "polecat/atom/gt-source+abc123"},
		{name: "missing request branch", agentDesc: "active_mr: gt-mr\nbranch: polecat/atom/gt-source+abc123\nlast_source_issue: gt-source\n", agentType: "agent", agentLabs: []string{"gt:agent"}},
		{name: "missing source", agentDesc: "active_mr: gt-mr\nbranch: polecat/atom/gt-source+abc123\n", agentType: "agent", agentLabs: []string{"gt:agent"}, requestBranch: "polecat/atom/gt-source+abc123"},
		{name: "not agent bead", agentDesc: "active_mr: gt-mr\nlast_source_issue: gt-source\n", agentType: "task", agentLabs: nil, requestBranch: "polecat/atom/gt-source+abc123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			work := newFakeWorkBeadStore()
			work.add(workIssue("gt-source", string(beads.StatusOpen)))
			agent := newFakeWorkBeadStore()
			agent.add(&beads.Issue{ID: "gt-agent", Title: "agent", Type: tt.agentType, Labels: tt.agentLabs, Status: string(beads.StatusOpen), Description: tt.agentDesc})

			result := closeMergedWorkBead(work, agent, nil, mergedWorkBeadCloseRequest{
				MRID:      "gt-mr",
				Branch:    tt.requestBranch,
				AgentBead: "gt-agent",
			})

			if result.Closed || !result.NotFound || result.WorkBeadID != "" {
				t.Fatalf("result = %+v, want unresolved fallback", result)
			}
			if len(work.closeCalls) != 0 {
				t.Fatalf("close calls = %v, want none", work.closeCalls)
			}
		})
	}
}

func TestCloseMergedWorkBead_RejectsNonConcreteTarget(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(&beads.Issue{ID: "gt-mr-target", Title: "MR target", Type: "merge-request", Labels: []string{"gt:merge-request"}, Status: string(beads.StatusOpen)})
	agent := newFakeWorkBeadStore()
	agent.add(agentIssue("gt-agent", "active_mr: gt-mr\nbranch: polecat/atom/gt-source+abc123\nlast_source_issue: gt-mr-target\n"))

	result := closeMergedWorkBead(work, agent, nil, mergedWorkBeadCloseRequest{MRID: "gt-mr", Branch: "polecat/atom/gt-source+abc123", AgentBead: "gt-agent"})

	if result.Closed || !result.NotFound || result.WorkBeadID != "gt-mr-target" {
		t.Fatalf("result = %+v, want rejected non-concrete target", result)
	}
	if len(work.closeCalls) != 0 {
		t.Fatalf("close calls = %v, want none", work.closeCalls)
	}
}

func TestCloseMergedWorkBead_RejectsNonMergeableTargets(t *testing.T) {
	tests := []struct {
		name        string
		description string
	}{
		{name: "no_merge", description: "no_merge: true\n"},
		{name: "review_only", description: "review_only: true\n"},
		{name: "local merge strategy", description: "merge_strategy: local\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			work := newFakeWorkBeadStore()
			issue := workIssue("gt-source", string(beads.StatusOpen))
			issue.Description = tt.description
			work.add(issue)

			result := closeMergedWorkBead(work, nil, nil, mergedWorkBeadCloseRequest{MRID: "gt-mr", SourceIssue: "gt-source"})

			if result.Closed || !result.NotFound || result.WorkBeadID != "gt-source" {
				t.Fatalf("result = %+v, want rejected target", result)
			}
			if len(work.closeCalls) != 0 {
				t.Fatalf("close calls = %v, want none", work.closeCalls)
			}
		})
	}
}

func TestCloseMergedWorkBead_AlreadyTerminalConcreteTargetIsNoop(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(workIssue("gt-source", string(beads.StatusClosed)))

	result := closeMergedWorkBead(work, nil, nil, mergedWorkBeadCloseRequest{MRID: "gt-mr", SourceIssue: "gt-source"})

	if !result.Closed || result.WorkBeadID != "gt-source" {
		t.Fatalf("result = %+v, want terminal no-op success", result)
	}
	if len(work.closeCalls) != 0 {
		t.Fatalf("close calls = %v, want none", work.closeCalls)
	}
}

func TestCloseMergedWorkBead_CloseErrorLeavesWorkOpen(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(workIssue("gt-source", string(beads.StatusOpen)))
	work.closeErr = errors.New("dolt unavailable")

	result := closeMergedWorkBead(work, nil, nil, mergedWorkBeadCloseRequest{MRID: "gt-mr", SourceIssue: "gt-source"})

	if result.Closed || !result.NotFound || result.WorkBeadID != "gt-source" {
		t.Fatalf("result = %+v, want failed close", result)
	}
	if got := work.issues["gt-source"].Status; got != string(beads.StatusOpen) {
		t.Fatalf("source status = %q, want open", got)
	}
}

func TestCloseMergedWorkBead_CloseErrorThenTerminalRaceSucceeds(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(workIssue("gt-source", string(beads.StatusOpen)))
	work.closeErr = errors.New("lost close race")
	work.closeErrCloses = true

	result := closeMergedWorkBead(work, nil, nil, mergedWorkBeadCloseRequest{MRID: "gt-mr", SourceIssue: "gt-source"})

	if !result.Closed || result.NotFound || result.WorkBeadID != "gt-source" {
		t.Fatalf("result = %+v, want terminal race success", result)
	}
}

func TestManagerIssueToMRIncludesAgentBead(t *testing.T) {
	mgr, _ := setupTestManager(t)
	issue := &beads.Issue{
		ID:          "gt-mr",
		Title:       "MR",
		Status:      string(beads.StatusOpen),
		Description: "branch: polecat/atom/gt-source+abc123\nsource_issue: gt-source\nagent_bead: gt-agent\ntarget: main",
	}

	mr := mgr.issueToMR(issue)
	if mr.AgentBead != "gt-agent" {
		t.Fatalf("AgentBead = %q, want gt-agent", mr.AgentBead)
	}
}
