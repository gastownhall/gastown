package polecat

import "testing"

// A polecat that exited DEFERRED used to be written as agent_state=stuck, and
// StateStuck is neither idle nor done, so DecideWorkstate returned NeedsRecovery
// with CountsTowardCapacity true. It then held a capacity slot indefinitely
// however clean it was. Observed live: exit DEFERRED, cleanup_status=clean,
// git-state clean / 0 unpushed / SAFE_TO_NUKE, slot held, nothing at risk, and
// --reconcile-cleanup unable to reach it because cleanup_status was already
// correct.
//
// These pin the disposition side of that: stuck costs a slot, done does not, so
// the exit type that writes stuck is the whole decision.
func TestStuckHoldsACapacitySlotEvenWhenClean(t *testing.T) {
	d := DecideWorkstate(WorkstateInput{State: StateStuck, CleanupStatus: CleanupClean})
	if !d.CountsTowardCapacity {
		t.Error("stuck no longer holds a slot; this test is the reason ESCALATED still writes it")
	}
	if d.Reusable {
		t.Error("a stuck polecat must not be reused -- an escalation may need its sandbox inspected")
	}
}

// The other half: once a deferred exit writes done instead of stuck, the same
// clean polecat is reusable and costs nothing.
func TestDoneAndCleanCostsNoSlot(t *testing.T) {
	d := DecideWorkstate(WorkstateInput{State: StateDone, CleanupStatus: CleanupClean})
	if d.CountsTowardCapacity {
		t.Error("a done-and-clean polecat is holding a capacity slot")
	}
	if !d.Reusable {
		t.Errorf("done+clean must be reusable; verdict=%v reason=%q blockers=%v", d.Verdict, d.Reason, d.Blockers)
	}
}

// Changing the exit mapping must not make a DIRTY deferred polecat reusable:
// the protection has to come from the worktree facts, not from the state name.
func TestDoneButDirtyStillHoldsItsSandbox(t *testing.T) {
	for name, in := range map[string]WorkstateInput{
		"uncommitted": {State: StateDone, CleanupStatus: CleanupClean, GitDirty: true},
		"unpushed":    {State: StateDone, CleanupStatus: CleanupClean, UnpushedCommits: 1},
		"stash":       {State: StateDone, CleanupStatus: CleanupClean, StashCount: 1},
		"hook held":   {State: StateDone, CleanupStatus: CleanupClean, HookBead: "ap-9999"},
	} {
		if d := DecideWorkstate(in); d.Reusable {
			t.Errorf("%s: a deferred-but-unclean polecat was declared reusable; its work would be handed away", name)
		}
	}
}
