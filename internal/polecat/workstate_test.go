package polecat

import "testing"

func TestDecideWorkstateCanonicalFields(t *testing.T) {
	tests := []struct {
		name string
		in   WorkstateInput
		want WorkstateDisposition
	}{
		{
			name: "clean idle is reusable and safe",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "main"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, ReuseStatus: "idle-clean"},
		},
		{
			name: "dirty idle needs recovery and capacity",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupUnpushed},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "cleanup-has_unpushed", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed"},
		},
		{
			name: "protected active work fails closed without capacity",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, ActiveWorkBlocker: "assigned_work=gt-blocked status=blocked"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "active-work", NeedsRecovery: true, CountsTowardCapacity: false, ReuseStatus: "idle-recovery-needed"},
		},
		{
			name: "active work blocker consumes capacity when requested",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, ActiveWorkBlocker: "assigned_work=gt-open status=open", ActiveWorkCountsTowardCapacity: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "active-work", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed"},
		},
		{
			name: "unsubmitted branch needs mq submit",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", MQCheckRequired: true, HasSubmittableWork: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsMQSubmit, Reason: "mq-not-submitted", NeedsRecovery: true, NeedsMQSubmit: true, MQStatus: "not_submitted", CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed"},
		},
		{
			name: "mq lookup uncertainty blocks cleanup",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", MQCheckRequired: true, MQLookupFailed: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "mq-lookup-failed", NeedsRecovery: true, MQStatus: "unknown", CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed", Blockers: []string{"mq_status=unknown"}},
		},
		{
			name: "open work with unpushed commits needs recovery",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", UnpushedCommits: 1},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "git-unpushed", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed", Blockers: []string{"git_state=has_unpushed unpushed_commits=1"}},
		},
		{
			name: "mr submission makes mq submitted",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", MQCheckRequired: true, HasSubmittableWork: true, MRSubmitted: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, MQStatus: "submitted", ReuseStatus: "idle-preserved"},
		},
		{
			name: "terminal source alone does not prove mq submitted",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", MQCheckRequired: true, HasSubmittableWork: true, AssignedBeadTerminal: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsMQSubmit, Reason: "mq-not-submitted", NeedsRecovery: true, NeedsMQSubmit: true, MQStatus: "not_submitted", CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed"},
		},
		{
			name: "dirty worktree blocks terminal source",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", GitDirty: true, GitDirtyReason: "git_state=has_uncommitted uncommitted_files=1", MQCheckRequired: true, HasSubmittableWork: true, AssignedBeadTerminal: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "git-dirty", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed", Blockers: []string{"git_state=has_uncommitted uncommitted_files=1"}},
		},
		{
			name: "stash blocks terminal source",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", StashCount: 1, MQCheckRequired: true, HasSubmittableWork: true, AssignedBeadTerminal: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "git-stash", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed", Blockers: []string{"git_state=has_stash stash_count=1"}},
		},
		{
			name: "terminal source does not suppress unpreserved commits",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", UnpushedCommits: 1, MQCheckRequired: true, HasSubmittableWork: true, AssignedBeadTerminal: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "git-unpushed", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed", Blockers: []string{"git_state=has_unpushed unpushed_commits=1"}},
		},
		{
			name: "push failure blocks terminal source",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", PushFailed: true, MQCheckRequired: true, HasSubmittableWork: true, AssignedBeadTerminal: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "push-failed", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed", Blockers: []string{"push_failed=true"}},
		},
		{
			name: "mr failure blocks terminal source",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", MRFailed: true, MQCheckRequired: true, HasSubmittableWork: true, AssignedBeadTerminal: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "mr-failed", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed", Blockers: []string{"mr_failed=true"}},
		},
		{
			name: "open active mr blocks terminal source",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", ActiveMR: "gt-mr-open", ActiveMRBlocker: "active_mr=gt-mr-open status=open", MQCheckRequired: true, HasSubmittableWork: true, AssignedBeadTerminal: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictPendingMR, Reason: "active-mr-open", ReuseStatus: "idle-pr-open", Blockers: []string{"active_mr=gt-mr-open status=open"}},
		},
		{
			name: "terminal active mr does not block when gatherer omits blocker",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, ActiveMR: "gt-mr-closed"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, ReuseStatus: "idle-clean"},
		},
		{
			name: "open active mr is preserved pending mr",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, ActiveMR: "gt-mr-open", ActiveMRBlocker: "active_mr=gt-mr-open status=open"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictPendingMR, Reason: "active-mr-open", ReuseStatus: "idle-pr-open"},
		},
		{
			name: "open active mr does not hide cleanup blocker",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupUnpushed, ActiveMR: "gt-mr-open", ActiveMRBlocker: "active_mr=gt-mr-open status=open"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "cleanup-has_unpushed", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed", Blockers: []string{"cleanup_status=has_unpushed", "active_mr=gt-mr-open status=open"}},
		},
		{
			name: "done active mr remains pending mr",
			in:   WorkstateInput{State: StateDone, CleanupStatus: CleanupClean, ActiveMR: "gt-mr-open", ActiveMRBlocker: "active_mr=gt-mr-open status=open"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictPendingMR, Reason: "active-mr-open", ReuseStatus: "idle-pr-open", Blockers: []string{"active_mr=gt-mr-open status=open"}},
		},
		{
			name: "done without mr blocks reuse until cleanup",
			in:   WorkstateInput{State: StateDone, CleanupStatus: CleanupClean},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "not-idle", NeedsRecovery: true, CountsTowardCapacity: true},
		},
		{
			name: "working counts as working capacity",
			in:   WorkstateInput{State: StateWorking, CleanupStatus: CleanupClean},
			want: WorkstateDisposition{Verdict: WorkstateVerdictWorking, Reason: "not-idle", NeedsRecovery: false, CountsTowardCapacity: true},
		},
		{
			name: "stalled active work preserves blocker",
			in:   WorkstateInput{State: StateStalled, CleanupStatus: CleanupClean, ActiveWorkBlocker: "assigned_work=gt-open status=open", ActiveWorkCountsTowardCapacity: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "not-idle", NeedsRecovery: true, CountsTowardCapacity: true, Blockers: []string{"assigned_work=gt-open status=open"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecideWorkstate(tt.in)
			if got.Verdict != tt.want.Verdict || got.Reason != tt.want.Reason || got.Reusable != tt.want.Reusable || got.SafeToNuke != tt.want.SafeToNuke || got.NeedsRecovery != tt.want.NeedsRecovery || got.NeedsMQSubmit != tt.want.NeedsMQSubmit || got.MQStatus != tt.want.MQStatus || got.CountsTowardCapacity != tt.want.CountsTowardCapacity || got.ReuseStatus != tt.want.ReuseStatus {
				t.Fatalf("DecideWorkstate() = %+v, want fields %+v", got, tt.want)
			}
			if tt.want.Blockers != nil {
				if len(got.Blockers) != len(tt.want.Blockers) {
					t.Fatalf("DecideWorkstate() blockers = %v, want %v", got.Blockers, tt.want.Blockers)
				}
				for i := range tt.want.Blockers {
					if got.Blockers[i] != tt.want.Blockers[i] {
						t.Fatalf("DecideWorkstate() blockers = %v, want %v", got.Blockers, tt.want.Blockers)
					}
				}
			}
		})
	}
}
