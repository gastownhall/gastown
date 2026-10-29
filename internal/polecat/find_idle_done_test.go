package polecat

import "testing"

// A polecat that finished cleanly sits in StateDone until its cleanup transition
// lands, and that transition does not always arrive. Nine such polecats on
// alphaprime read reusable=true while FindIdlePolecat selected none, so every
// dispatch allocated a fresh worktree and the pool grew 11 -> 17 in an hour
// against a 30-directory ceiling.
//
// DecideWorkstate already treats a done-and-clean polecat as reusable
// (workstate.go:69). These assert the two layers agree, since the selection
// layer having its own narrower idea of "available" is the whole defect.
func TestDoneAndCleanIsReusable(t *testing.T) {
	d := DecideWorkstate(WorkstateInput{State: StateDone, CleanupStatus: CleanupClean})
	if !d.Reusable {
		t.Fatalf("a done-and-clean polecat must be reusable; verdict=%v reason=%q blockers=%v",
			d.Verdict, d.Reason, d.Blockers)
	}
}

func TestIdleAndCleanIsStillReusable(t *testing.T) {
	d := DecideWorkstate(WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean})
	if !d.Reusable {
		t.Fatal("idle+clean must remain reusable")
	}
}

// The safety of widening the state filter rests entirely on the Reusable
// verdict, never on the state name. A genuine zombie -- done but dirty, or still
// holding work -- must fail the verdict and so never be selected.
func TestDoneButUnsafeIsNotReusable(t *testing.T) {
	cases := map[string]WorkstateInput{
		"uncommitted":  {State: StateDone, CleanupStatus: CleanupClean, GitDirty: true},
		"unpushed":     {State: StateDone, CleanupStatus: CleanupClean, UnpushedCommits: 2},
		"stash":        {State: StateDone, CleanupStatus: CleanupClean, StashCount: 1},
		"dirty report": {State: StateDone, CleanupStatus: CleanupUncommitted},
		"holds a hook": {State: StateDone, CleanupStatus: CleanupClean, HookBead: "ap-1234"},
		"push failed":  {State: StateDone, CleanupStatus: CleanupClean, PushFailed: true},
		"mr failed":    {State: StateDone, CleanupStatus: CleanupClean, MRFailed: true},
	}
	for name, in := range cases {
		if d := DecideWorkstate(in); d.Reusable {
			t.Errorf("%s: a done polecat in this state was declared reusable; its work would be handed to a new bead", name)
		}
	}
}

// Working polecats are never reusable regardless of anything else.
func TestWorkingIsNeverReusable(t *testing.T) {
	if d := DecideWorkstate(WorkstateInput{State: StateWorking, CleanupStatus: CleanupClean}); d.Reusable {
		t.Error("a working polecat was declared reusable")
	}
}
