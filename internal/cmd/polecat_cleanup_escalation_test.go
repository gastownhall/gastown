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
