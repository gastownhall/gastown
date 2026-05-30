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
			name: "unsubmitted branch needs mq submit",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", MQCheckRequired: true, HasSubmittableWork: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsMQSubmit, Reason: "mq-not-submitted", NeedsRecovery: true, NeedsMQSubmit: true, MQStatus: "not_submitted", CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed"},
		},
		{
			name: "terminal source makes mq submitted",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", MQCheckRequired: true, HasSubmittableWork: true, AssignedBeadTerminal: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, MQStatus: "submitted", ReuseStatus: "idle-preserved"},
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
			name: "working counts as working capacity",
			in:   WorkstateInput{State: StateWorking, CleanupStatus: CleanupClean},
			want: WorkstateDisposition{Verdict: WorkstateVerdictWorking, Reason: "not-idle", NeedsRecovery: false, CountsTowardCapacity: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecideWorkstate(tt.in)
			if got.Verdict != tt.want.Verdict || got.Reason != tt.want.Reason || got.Reusable != tt.want.Reusable || got.SafeToNuke != tt.want.SafeToNuke || got.NeedsRecovery != tt.want.NeedsRecovery || got.NeedsMQSubmit != tt.want.NeedsMQSubmit || got.MQStatus != tt.want.MQStatus || got.CountsTowardCapacity != tt.want.CountsTowardCapacity || got.ReuseStatus != tt.want.ReuseStatus {
				t.Fatalf("DecideWorkstate() = %+v, want fields %+v", got, tt.want)
			}
		})
	}
}
