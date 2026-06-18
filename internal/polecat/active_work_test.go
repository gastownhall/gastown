package polecat

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

type fakeActiveWorkReader struct {
	issues   map[string]*beads.Issue
	err      error
	assigned []*beads.Issue
}

func (f fakeActiveWorkReader) Show(issueID string) (*beads.Issue, error) {
	if f.err != nil {
		return nil, f.err
	}
	issue, ok := f.issues[issueID]
	if !ok {
		return nil, beads.ErrNotFound
	}
	return issue, nil
}

func (f fakeActiveWorkReader) ListByAssignee(string) ([]*beads.Issue, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.assigned, nil
}

func TestAssessHookWork(t *testing.T) {
	tests := []struct {
		name          string
		hookStatus    string
		err           error
		wantBlocker   string
		wantRestart   bool
		wantProtected bool
		wantSafe      bool
		wantTerminal  bool
	}{
		{name: "closed hook is terminal", hookStatus: "closed", wantSafe: true, wantTerminal: true},
		{name: "tombstone hook is terminal", hookStatus: "tombstone", wantSafe: true, wantTerminal: true},
		{name: "hooked hook blocks", hookStatus: beads.StatusHooked, wantBlocker: "hook_bead=gt-work status=hooked", wantRestart: true},
		{name: "in progress hook blocks", hookStatus: "in_progress", wantBlocker: "hook_bead=gt-work status=in_progress", wantRestart: true},
		{name: "open hook blocks", hookStatus: "open", wantBlocker: "hook_bead=gt-work status=open", wantRestart: true},
		{name: "blocked hook protects without restart", hookStatus: "blocked", wantBlocker: "hook_bead=gt-work status=blocked", wantProtected: true},
		{name: "deferred hook protects without restart", hookStatus: "deferred", wantBlocker: "hook_bead=gt-work status=deferred", wantProtected: true},
		{name: "lookup error blocks", err: errors.New("bd exploded"), wantBlocker: "lookup_error", wantProtected: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := fakeActiveWorkReader{issues: map[string]*beads.Issue{"gt-work": &beads.Issue{ID: "gt-work", Status: tt.hookStatus}}, err: tt.err}
			got := AssessHookWork(reader, "gt-work")
			if got.HookSafe != tt.wantSafe || got.HookTerminal != tt.wantTerminal {
				t.Fatalf("AssessHookWork() hook = (safe=%v terminal=%v), want (%v %v)", got.HookSafe, got.HookTerminal, tt.wantSafe, tt.wantTerminal)
			}
			if tt.wantBlocker != "" && !strings.Contains(got.Blocker, tt.wantBlocker) {
				t.Fatalf("blocker = %q, want contains %q", got.Blocker, tt.wantBlocker)
			}
			if got.RequiresRestart != tt.wantRestart || got.Protected != tt.wantProtected {
				t.Fatalf("AssessHookWork() = %+v, want restart=%v protected=%v", got, tt.wantRestart, tt.wantProtected)
			}
		})
	}
}

func TestAssessActiveWork(t *testing.T) {
	tests := []struct {
		name           string
		reader         fakeActiveWorkReader
		state          beads.AgentState
		wantBlocker    string
		wantRestart    bool
		wantProtected  bool
		wantAssignedID string
	}{
		{
			name:        "direct assigned work blocks cleanup",
			reader:      fakeActiveWorkReader{assigned: []*beads.Issue{{ID: "gt-work", Status: "open"}}},
			wantBlocker: "assigned_work=gt-work status=open", wantRestart: true, wantAssignedID: "gt-work",
		},
		{
			name:        "deferred assigned work blocks cleanup without restart",
			reader:      fakeActiveWorkReader{assigned: []*beads.Issue{{ID: "gt-deferred", Status: "deferred"}}},
			wantBlocker: "assigned_work=gt-deferred status=deferred", wantProtected: true, wantAssignedID: "gt-deferred",
		},
		{
			name:   "closed assigned work is ignored",
			reader: fakeActiveWorkReader{assigned: []*beads.Issue{{ID: "gt-work", Status: "closed"}}},
		},
		{
			name:   "working state blocks cleanup",
			reader: fakeActiveWorkReader{}, state: beads.AgentStateWorking,
			wantBlocker: "agent_state=working", wantRestart: true,
		},
		{
			name:   "paused state blocks cleanup without restart",
			reader: fakeActiveWorkReader{}, state: beads.AgentStatePaused,
			wantBlocker: "agent_state=paused", wantProtected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssessActiveWork(tt.reader, "gastown/polecats/nitro", tt.state, "")
			if tt.wantBlocker == "" {
				if got.BlocksCleanup || got.Blocker != "" {
					t.Fatalf("AssessActiveWork() = %+v, want no blocker", got)
				}
				return
			}
			if !strings.Contains(got.Blocker, tt.wantBlocker) || !got.BlocksCleanup {
				t.Fatalf("AssessActiveWork() = %+v, want blocker %q", got, tt.wantBlocker)
			}
			if got.RequiresRestart != tt.wantRestart || got.Protected != tt.wantProtected || got.AssignedIssue != tt.wantAssignedID {
				t.Fatalf("AssessActiveWork() = %+v", got)
			}
		})
	}
}

func TestActiveWorkEvidenceMergePreservesPriorityAndHook(t *testing.T) {
	evidence := ActiveWorkEvidence{HookSafe: true}
	evidence.Merge(ActiveWorkEvidence{
		Active:          true,
		BlocksCleanup:   true,
		RequiresRestart: true,
		Blocker:         "assigned_work=gt-work status=hooked",
		AssignedIssue:   "gt-work",
	})
	evidence.Merge(ActiveWorkEvidence{
		Active:          true,
		BlocksCleanup:   true,
		RequiresRestart: true,
		Blocker:         "hook_bead=gt-hook status=hooked",
		HookBead:        "gt-hook",
		HookSafe:        false,
	})
	evidence.Merge(AssessAgentStateWork(beads.AgentStateSpawning))

	if evidence.Blocker != "assigned_work=gt-work status=hooked" {
		t.Fatalf("blocker = %q, want first blocker", evidence.Blocker)
	}
	if evidence.HookBead != "gt-hook" || evidence.HookSafe || evidence.HookTerminal {
		t.Fatalf("hook evidence = (bead=%q safe=%v terminal=%v), want active unsafe hook", evidence.HookBead, evidence.HookSafe, evidence.HookTerminal)
	}
	if !evidence.Active || !evidence.BlocksCleanup || !evidence.RequiresRestart || evidence.AssignedIssue != "gt-work" {
		t.Fatalf("merged evidence = %+v", evidence)
	}
}
