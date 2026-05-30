package cmd

import (
	"errors"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
)

type fakeReuseMRShower struct {
	issue *beads.Issue
	err   error
}

func (f fakeReuseMRShower) Show(issueID string) (*beads.Issue, error) {
	return f.issue, f.err
}

type fakeReuseMapShower struct {
	issues map[string]*beads.Issue
	errs   map[string]error
}

func (f fakeReuseMapShower) Show(issueID string) (*beads.Issue, error) {
	if err := f.errs[issueID]; err != nil {
		return nil, err
	}
	issue, ok := f.issues[issueID]
	if !ok {
		return nil, beads.ErrNotFound
	}
	return issue, nil
}

func TestEffectivePolecatState(t *testing.T) {
	tests := []struct {
		name string
		item PolecatListItem
		want polecat.State
	}{
		{
			name: "session-running-done-with-issue-becomes-working",
			item: PolecatListItem{
				State:          polecat.StateDone,
				Issue:          "gt-abc",
				SessionRunning: true,
			},
			want: polecat.StateWorking,
		},
		{
			name: "session-running-done-without-issue-stays-done",
			item: PolecatListItem{
				State:          polecat.StateDone,
				SessionRunning: true,
			},
			want: polecat.StateDone,
		},
		{
			name: "session-dead-working-becomes-stalled",
			item: PolecatListItem{
				State:          polecat.StateWorking,
				SessionRunning: false,
			},
			want: polecat.StateStalled,
		},
		{
			name: "zombie-is-never-rewritten",
			item: PolecatListItem{
				State:          polecat.StateZombie,
				SessionRunning: false,
				Zombie:         true,
			},
			want: polecat.StateZombie,
		},
		{
			name: "idle-session-dead-stays-idle",
			item: PolecatListItem{
				State:          polecat.StateIdle,
				SessionRunning: false,
			},
			want: polecat.StateIdle,
		},
		{
			name: "idle-session-running-without-issue-stays-idle",
			item: PolecatListItem{
				State:          polecat.StateIdle,
				SessionRunning: true,
			},
			want: polecat.StateIdle,
		},
		{
			name: "idle-session-running-with-issue-becomes-working",
			item: PolecatListItem{
				State:          polecat.StateIdle,
				Issue:          "gt-abc",
				SessionRunning: true,
			},
			want: polecat.StateWorking,
		},
		{
			name: "stalled-stays-stalled-when-session-dead",
			item: PolecatListItem{
				State:          polecat.StateStalled,
				SessionRunning: false,
			},
			want: polecat.StateStalled,
		},
		{
			name: "stalled-becomes-working-when-session-alive",
			item: PolecatListItem{
				State:          polecat.StateStalled,
				SessionRunning: true,
			},
			want: polecat.StateStalled, // stalled is a detected state, session running doesn't override
		},
		{
			name: "review-needed-stays-review-needed-when-session-alive",
			item: PolecatListItem{
				State:          polecat.StateReviewNeeded,
				SessionRunning: true,
			},
			want: polecat.StateReviewNeeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectivePolecatState(tt.item)
			if got != tt.want {
				t.Fatalf("effectivePolecatState() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestActiveMRBlocksReuse(t *testing.T) {
	tests := []struct {
		name       string
		mrID       string
		sourceHint string
		gitSafe    bool
		bd         reuseMRShower
		want       bool
	}{
		{name: "empty active MR does not block"},
		{
			name: "open MR blocks reuse",
			mrID: "mr-1",
			bd:   fakeReuseMRShower{issue: &beads.Issue{ID: "mr-1", Status: "open"}},
			want: true,
		},
		{
			name:       "closed MR with terminal source does not block reuse",
			mrID:       "mr-1",
			sourceHint: "gt-closed",
			gitSafe:    true,
			bd:         fakeReuseMapShower{issues: map[string]*beads.Issue{"mr-1": &beads.Issue{ID: "mr-1", Status: "closed"}, "gt-closed": &beads.Issue{ID: "gt-closed", Status: "closed"}}},
			want:       false,
		},
		{
			name:       "closed MR with terminal source blocks when git unsafe",
			mrID:       "mr-1",
			sourceHint: "gt-closed",
			bd:         fakeReuseMapShower{issues: map[string]*beads.Issue{"mr-1": &beads.Issue{ID: "mr-1", Status: "closed"}, "gt-closed": &beads.Issue{ID: "gt-closed", Status: "closed"}}},
			want:       true,
		},
		{
			name: "closed MR without source blocks conservatively",
			mrID: "mr-1",
			bd:   fakeReuseMapShower{issues: map[string]*beads.Issue{"mr-1": &beads.Issue{ID: "mr-1", Status: "closed"}}},
			want: true,
		},
		{
			name: "lookup error blocks conservatively",
			mrID: "mr-1",
			bd:   fakeReuseMRShower{err: errors.New("bd exploded")},
			want: true,
		},
		{
			name: "missing MR blocks conservatively without source",
			mrID: "mr-1",
			bd:   fakeReuseMRShower{},
			want: true,
		},
		{
			name:       "missing MR with terminal source does not block reuse",
			mrID:       "mr-1",
			sourceHint: "gt-closed",
			gitSafe:    true,
			bd:         fakeReuseMapShower{issues: map[string]*beads.Issue{"gt-closed": &beads.Issue{ID: "gt-closed", Status: "closed"}}},
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := activeMRBlocksReuse(tt.bd, tt.mrID, tt.sourceHint, true, tt.gitSafe); got != tt.want {
				t.Fatalf("activeMRBlocksReuse() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWorkstateDispositionProjectionAgreement(t *testing.T) {
	tests := []struct {
		name         string
		in           polecat.WorkstateInput
		wantReusable bool
		wantRecovery bool
		wantMQSubmit bool
		wantSafe     bool
		wantCapacity polecatCapacitySnapshot
	}{
		{
			name:         "reusable idle",
			in:           polecat.WorkstateInput{State: polecat.StateIdle, CleanupStatus: polecat.CleanupClean},
			wantReusable: true,
			wantSafe:     true,
			wantCapacity: polecatCapacitySnapshot{ReusableIdle: 1},
		},
		{
			name:         "recovery blocked idle",
			in:           polecat.WorkstateInput{State: polecat.StateIdle, CleanupStatus: polecat.CleanupUnpushed},
			wantRecovery: true,
			wantCapacity: polecatCapacitySnapshot{RecoveryBlocked: 1},
		},
		{
			name:         "needs mq submit",
			in:           polecat.WorkstateInput{State: polecat.StateIdle, CleanupStatus: polecat.CleanupClean, Branch: "polecat/test", MQCheckRequired: true, HasSubmittableWork: true},
			wantRecovery: true,
			wantMQSubmit: true,
			wantCapacity: polecatCapacitySnapshot{RecoveryBlocked: 1},
		},
		{
			name:         "working",
			in:           polecat.WorkstateInput{State: polecat.StateWorking, CleanupStatus: polecat.CleanupClean},
			wantCapacity: polecatCapacitySnapshot{Working: 1},
		},
		{
			name:         "pending active mr",
			in:           polecat.WorkstateInput{State: polecat.StateIdle, CleanupStatus: polecat.CleanupClean, ActiveMR: "gt-mr-open", ActiveMRBlocker: "active_mr=gt-mr-open status=open"},
			wantCapacity: polecatCapacitySnapshot{PendingMR: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disposition := polecat.DecideWorkstate(tt.in)
			list := PolecatListItem{
				Verdict:              disposition.Verdict,
				Reason:               disposition.Reason,
				Reusable:             disposition.Reusable,
				SafeToNuke:           disposition.SafeToNuke,
				NeedsRecovery:        disposition.NeedsRecovery,
				NeedsMQSubmit:        disposition.NeedsMQSubmit,
				MQStatus:             disposition.MQStatus,
				CountsTowardCapacity: disposition.CountsTowardCapacity,
				ReuseStatus:          disposition.ReuseStatus,
			}
			recovery := RecoveryStatus{}
			applyWorkstateDispositionToRecoveryStatus(&recovery, disposition)
			if list.Reusable != recovery.Reusable || list.SafeToNuke != recovery.SafeToNuke || list.NeedsRecovery != recovery.NeedsRecovery || list.NeedsMQSubmit != recovery.NeedsMQSubmit || list.MQStatus != recovery.MQStatus || list.CountsTowardCapacity != recovery.CountsTowardCapacity || list.ReuseStatus != recovery.ReuseStatus {
				t.Fatalf("list projection %+v disagrees with recovery %+v", list, recovery)
			}
			if recovery.Reusable != tt.wantReusable || recovery.SafeToNuke != tt.wantSafe || recovery.NeedsRecovery != tt.wantRecovery || recovery.NeedsMQSubmit != tt.wantMQSubmit {
				t.Fatalf("recovery projection = %+v", recovery)
			}
			snapshot := polecatCapacitySnapshot{}
			applyWorkstateDispositionToCapacitySnapshot(&snapshot, tt.in.State, disposition)
			if snapshot.Working != tt.wantCapacity.Working || snapshot.RecoveryBlocked != tt.wantCapacity.RecoveryBlocked || snapshot.ReusableIdle != tt.wantCapacity.ReusableIdle || snapshot.PendingMR != tt.wantCapacity.PendingMR {
				t.Fatalf("capacity projection = %+v, want %+v", snapshot, tt.wantCapacity)
			}
		})
	}
}

func TestPolecatReuseStatus(t *testing.T) {
	tests := []struct {
		name             string
		state            polecat.State
		cleanupStatus    string
		activeMR         string
		branch           string
		activeMRBlocks   bool
		staleCleanupSafe bool
		want             string
	}{
		{
			name:  "working has no reuse status",
			state: polecat.StateWorking,
			want:  "",
		},
		{
			name:          "idle missing cleanup is recovery needed",
			state:         polecat.StateIdle,
			cleanupStatus: "",
			want:          "idle-recovery-needed",
		},
		{
			name:          "idle dirty cleanup is recovery needed",
			state:         polecat.StateIdle,
			cleanupStatus: string(polecat.CleanupUnpushed),
			want:          "idle-recovery-needed",
		},
		{
			name:             "idle stale dirty cleanup can be clean",
			state:            polecat.StateIdle,
			cleanupStatus:    string(polecat.CleanupUnpushed),
			staleCleanupSafe: true,
			want:             "idle-clean",
		},
		{
			name:           "idle open MR is pr open",
			state:          polecat.StateIdle,
			cleanupStatus:  string(polecat.CleanupClean),
			activeMR:       "mr-1",
			activeMRBlocks: true,
			want:           "idle-pr-open",
		},
		{
			name:          "idle clean old branch is preserved",
			state:         polecat.StateIdle,
			cleanupStatus: string(polecat.CleanupClean),
			branch:        "polecat/chrome/old-work",
			want:          "idle-preserved",
		},
		{
			name:          "idle clean main is clean",
			state:         polecat.StateIdle,
			cleanupStatus: string(polecat.CleanupClean),
			branch:        "main",
			want:          "idle-clean",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := polecatReuseStatus(tt.state, tt.cleanupStatus, tt.activeMR, tt.branch, tt.activeMRBlocks, tt.staleCleanupSafe)
			if got != tt.want {
				t.Fatalf("polecatReuseStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}
