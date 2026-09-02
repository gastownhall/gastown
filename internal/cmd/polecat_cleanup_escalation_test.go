package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/polecat"
)

// gt-3up: `gt polecat list` and `gt polecat check-recovery` returned opposite
// verdicts for the same polecat, seconds apart. list reads agent beads only and
// never touches git, so a polecat whose bead lost its cleanup_status was pinned
// at NEEDS_RECOVERY / safe_to_nuke=false / counts_toward_capacity=true — on a
// blocker that check-recovery, which does inspect the worktree, proved did not
// exist. The pool stayed at capacity indefinitely.
//
// The list path now escalates to the authoritative disposition for exactly that
// case. These pin the predicate that decides when to escalate: it must fire on
// the missing-cleanup case and must NOT fire when other evidence is in play,
// because then the cheap verdict is already correct and the git I/O is waste.
func TestDispositionBlockedOnlyByMissingCleanup(t *testing.T) {
	tests := []struct {
		name string
		in   polecat.WorkstateDisposition
		want bool
	}{
		{
			name: "the gt-3up case: cleanup is the whole story",
			in: polecat.WorkstateDisposition{
				Verdict:  polecat.WorkstateVerdictNeedsRecovery,
				Reason:   "cleanup-unknown",
				Blockers: []string{"cleanup_status=<missing>"},
			},
			want: true,
		},
		{
			name: "same, with the enum spelling rather than <missing>",
			in: polecat.WorkstateDisposition{
				Verdict:  polecat.WorkstateVerdictNeedsRecovery,
				Reason:   "cleanup-unknown",
				Blockers: []string{"cleanup_status=unknown"},
			},
			want: true,
		},
		{
			name: "a hook bead is also blocking — cheap verdict already correct",
			in: polecat.WorkstateDisposition{
				Verdict:  polecat.WorkstateVerdictNeedsRecovery,
				Reason:   "cleanup-unknown",
				Blockers: []string{"cleanup_status=<missing>", "has work on hook (gt-abc)"},
			},
			want: false,
		},
		{
			name: "real dirty worktree must never be escalated away",
			in: polecat.WorkstateDisposition{
				Verdict:  polecat.WorkstateVerdictNeedsRecovery,
				Reason:   "cleanup-has_uncommitted",
				Blockers: []string{"cleanup_status=has_uncommitted"},
			},
			want: false,
		},
		{
			name: "a stash is work at risk, not missing metadata",
			in: polecat.WorkstateDisposition{
				Verdict:  polecat.WorkstateVerdictNeedsRecovery,
				Reason:   "cleanup-has_stash",
				Blockers: []string{"cleanup_status=has_stash"},
			},
			want: false,
		},
		{
			name: "already safe to nuke — nothing to reconcile",
			in: polecat.WorkstateDisposition{
				Verdict: polecat.WorkstateVerdictSafeToNuke,
			},
			want: false,
		},
		{
			name: "no blockers recorded at all",
			in: polecat.WorkstateDisposition{
				Verdict: polecat.WorkstateVerdictNeedsRecovery,
				Reason:  "cleanup-unknown",
			},
			want: false,
		},
		{
			name: "a differently-named single blocker is not ours",
			in: polecat.WorkstateDisposition{
				Verdict:  polecat.WorkstateVerdictNeedsRecovery,
				Reason:   "cleanup-unknown",
				Blockers: []string{"push_failed=true"},
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := dispositionBlockedOnlyByMissingCleanup(tc.in); got != tc.want {
				t.Fatalf("dispositionBlockedOnlyByMissingCleanup() = %v, want %v", got, tc.want)
			}
		})
	}
}

// gt-b3a2: #4798 fixed the missing-cleanup escalation in `gt polecat list` and
// left the scheduler's capacity path reading the raw, unreconciled disposition.
// Both build their disposition from the SAME buildPolecatInventoryItem, so for
// four hours the two answered the same question differently in the same second:
// list reported counts_toward_capacity=false for every polecat in the pool while
// the scheduler counted all of them recovery-blocked and dispatched nothing.
//
// The invariant that was violated is not "capacity computes X". It is that every
// caller which GATES on a disposition reconciles it first. This pins that: stub
// the reconciler, and prove the capacity path consults it rather than reading
// item.Disposition directly. It fails if anyone reverts that call site.
func TestCapacitySnapshotUsesReconciledDisposition(t *testing.T) {
	orig := reconcilePolecatDispositionFn
	t.Cleanup(func() { reconcilePolecatDispositionFn = orig })

	var sawRig, sawName string
	calls := 0
	// The reconciler stands in for the authoritative check: the bead-only view
	// said "recovery, counts toward capacity"; the worktree says it is reusable.
	reconcilePolecatDispositionFn = func(rigName, polecatName string, item polecatInventoryItem) polecat.WorkstateDisposition {
		calls++
		sawRig, sawName = rigName, polecatName
		return polecat.WorkstateDisposition{
			Verdict:              polecat.WorkstateVerdictSafeToNuke,
			Reason:               "reusable",
			Reusable:             true,
			SafeToNuke:           true,
			CountsTowardCapacity: false,
			ReuseStatus:          "idle-preserved",
		}
	}

	snapshot := polecatCapacitySnapshot{Max: 4}
	applyAgentFieldsToCapacitySnapshot(&snapshot, "liveop", "chrome", nil, nil, nil)

	if calls != 1 {
		t.Fatalf("capacity path called the reconciler %d times, want exactly 1 — it is reading item.Disposition directly (gt-b3a2)", calls)
	}
	if sawRig != "liveop" || sawName != "chrome" {
		t.Fatalf("reconciler got (%q, %q), want (\"liveop\", \"chrome\")", sawRig, sawName)
	}
	if snapshot.RecoveryBlocked != 0 {
		t.Fatalf("snapshot = %+v, want recovery_blocked=0: a reusable polecat must not be counted as recovery-blocked", snapshot)
	}
	if snapshot.ReusableIdle != 1 {
		t.Fatalf("snapshot = %+v, want reusable_idle=1", snapshot)
	}
	if snapshot.occupied() != 0 {
		t.Fatalf("snapshot occupied=%d, want 0: a reusable polecat must not consume capacity", snapshot.occupied())
	}
}

// The reconciler must be a no-op for every disposition that is NOT the narrow
// missing-cleanup case. If it were not, it would pay for git I/O on every
// polecat in the town on every capacity check — and, worse, could override a
// verdict that was already correct.
func TestReconcilePolecatDispositionPassesThroughWhenNotTheGt3upCase(t *testing.T) {
	for _, d := range []polecat.WorkstateDisposition{
		{
			Verdict:              polecat.WorkstateVerdictNeedsRecovery,
			Reason:               "cleanup-has_uncommitted",
			Blockers:             []string{"cleanup_status=has_uncommitted"},
			NeedsRecovery:        true,
			CountsTowardCapacity: true,
		},
		{
			Verdict:              polecat.WorkstateVerdictNeedsRecovery,
			Reason:               "cleanup-unknown",
			Blockers:             []string{"cleanup_status=<missing>", "has work on hook (gt-abc)"},
			NeedsRecovery:        true,
			CountsTowardCapacity: true,
		},
		{
			Verdict:    polecat.WorkstateVerdictSafeToNuke,
			Reason:     "reusable",
			Reusable:   true,
			SafeToNuke: true,
		},
	} {
		got := reconcilePolecatDisposition("liveop", "chrome", polecatInventoryItem{Disposition: d})
		if got.Verdict != d.Verdict || got.Reason != d.Reason || got.CountsTowardCapacity != d.CountsTowardCapacity {
			t.Fatalf("reconcile(%+v) = %+v, want it returned unchanged", d, got)
		}
	}
}
