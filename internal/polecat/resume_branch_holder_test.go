package polecat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// si-d6kw. "gt sling <bead> <rig> --branch <held-branch>" spawned a SECOND
// polecat onto a branch another polecat's worktree still had checked out. The
// two auto-checkpointed over each other; a fleet sweep then found three more
// worktrees armed with 7767 / 6824 / 434 staged deletions, and one of those
// deletions was committed at 08:01 during an explicit stop-write with both
// agents complying — because the auto-save needs no agent to act.
//
// The guard lives in internal/git; these tests exist because a guard the sling
// resume path does not CALL protects nothing. Both spawn entry points are
// covered: AddWithOptions (named polecat) and addWithOptionsLocked, reached via
// AllocateAndAdd (pool allocation), which is the one --branch actually used.

// seedLiveWorktreeOnBranch creates a real branch in the repo and a real, live
// worktree holding it — the "dag still has it checked out" precondition.
func seedLiveWorktreeOnBranch(t *testing.T, repo, branch string) string {
	t.Helper()
	cmd := exec.Command("git", "branch", branch, "HEAD")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create branch %s: %v\n%s", branch, err, out)
	}
	holder := filepath.Join(t.TempDir(), "holder")
	cmd = exec.Command("git", "worktree", "add", holder, branch)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed live worktree on %s: %v\n%s", branch, err, out)
	}
	return holder
}

func assertRefusalIsActionable(t *testing.T, err error, branch, holder string) {
	t.Helper()
	if err == nil {
		t.Fatal("resume onto a live-held branch SUCCEEDED — this is si-d6kw: two worktrees " +
			"on one ref, which the auto-save turns into committed data loss")
	}
	msg := err.Error()
	// Naming the holder is the difference between a refusal an operator can act
	// on and one they route around. si-d6kw's post-mortem traced the original
	// incident to a safe path that failed with a bare "exit status 1".
	if !strings.Contains(msg, holder) {
		resolved, evalErr := filepath.EvalSymlinks(holder)
		if evalErr != nil || !strings.Contains(msg, resolved) {
			t.Fatalf("refusal does not name the holding worktree %s.\nGot: %s", holder, msg)
		}
	}
	if !strings.Contains(msg, branch) {
		t.Fatalf("refusal does not name the branch %s.\nGot: %s", branch, msg)
	}
	// It must tell the operator what to do instead, or it manufactures pressure
	// toward the dangerous option exactly as the original defect did.
	if !strings.Contains(msg, "Instead:") {
		t.Fatalf("refusal offers no alternative; an operator who is blocked will find a "+
			"worse route.\nGot: %s", msg)
	}
}

func TestAddWithOptionsRefusesResumeBranchHeldByLiveWorktree(t *testing.T) {
	mgr, mayorRig := setupCanonicalBranchManagerTest(t)
	const branch = "polecat/dag/si-aka.9+ms43eli5"
	holder := seedLiveWorktreeOnBranch(t, mayorRig, branch)

	_, err := mgr.AddWithOptions("toast", AddOptions{ResumeBranch: branch})
	assertRefusalIsActionable(t, err, branch, holder)

	// The refusal must leave nothing behind. A half-built polecat directory is
	// its own failure mode — reconcile treats it as an allocation.
	polecatDir := filepath.Join(mgr.rig.Path, "polecats", "toast")
	if _, statErr := os.Stat(polecatDir); !os.IsNotExist(statErr) {
		t.Fatalf("polecat dir %s survived the refusal; rollback did not run", polecatDir)
	}

	// And the original holder must be untouched: still the only worktree on the
	// branch, still on it. Refusing loudly while having already forced would be
	// the worst of both.
	holders := worktreesOnBranch(t, mayorRig, branch)
	if len(holders) != 1 {
		t.Fatalf("worktrees on %s = %d (%v), want exactly 1 — the original holder", branch, len(holders), holders)
	}
}

func TestAllocateAndAddRefusesResumeBranchHeldByLiveWorktree(t *testing.T) {
	mgr, mayorRig := setupCanonicalBranchManagerTest(t)
	const branch = "polecat/keeper/si-aka.37+ms43gm2z"
	holder := seedLiveWorktreeOnBranch(t, mayorRig, branch)

	_, _, err := mgr.AllocateAndAdd(AddOptions{ResumeBranch: branch})
	assertRefusalIsActionable(t, err, branch, holder)

	holders := worktreesOnBranch(t, mayorRig, branch)
	if len(holders) != 1 {
		t.Fatalf("worktrees on %s = %d (%v), want exactly 1", branch, len(holders), holders)
	}
}

// The recovery workflow --branch exists for, which the guard must NOT break.
// Four of Alice's five recovery slings were exactly this case.
func TestAddWithOptionsResumesBranchWhoseHolderWasReaped(t *testing.T) {
	mgr, mayorRig := setupCanonicalBranchManagerTest(t)
	const branch = "polecat/cheedo/si-aka.10+ms43f8l0"
	holder := seedLiveWorktreeOnBranch(t, mayorRig, branch)

	// Reap the original worker the way the fleet does.
	if err := os.RemoveAll(holder); err != nil {
		t.Fatalf("reap holder: %v", err)
	}
	// Denominator: the stale record must survive the reap, or this test is just
	// the unheld-branch case wearing a different name. Note this deliberately
	// counts records INCLUDING prunable ones — worktreesOnBranch excludes them,
	// which is the whole point of the reap.
	if !anyWorktreeRecordFor(t, mayorRig, branch) {
		t.Fatal("no worktree record for the branch after reaping — setup no longer " +
			"reproduces the stale-holder case")
	}

	polecat, err := mgr.AddWithOptions("toast", AddOptions{ResumeBranch: branch})
	if err != nil {
		t.Fatalf("resume after reap was refused: %v\n"+
			"This is the workflow --force was added for. A guard that blocks it removes the "+
			"only working recovery route, which is what si-d6kw's fix order warned about", err)
	}
	if polecat.Branch != branch {
		t.Fatalf("resumed polecat is on %q, want %q", polecat.Branch, branch)
	}
	head, err := git.NewGit(polecat.ClonePath).CurrentBranch()
	if err != nil {
		t.Fatalf("read resumed worktree branch: %v", err)
	}
	if head != branch {
		t.Fatalf("resumed worktree HEAD is on %q, want %q", head, branch)
	}
}

// anyWorktreeRecordFor reports whether git still has ANY record for the branch,
// live or prunable. Used for setup denominators, where the point is that a dead
// record survives.
func anyWorktreeRecordFor(t *testing.T, repo, branch string) bool {
	t.Helper()
	list, err := git.NewGit(repo).WorktreeList()
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	for _, wt := range list {
		if wt.Branch == branch {
			return true
		}
	}
	return false
}

// worktreesOnBranch returns the LIVE worktrees holding branch.
func worktreesOnBranch(t *testing.T, repo, branch string) []string {
	t.Helper()
	list, err := git.NewGit(repo).WorktreeList()
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("WorktreeList returned nothing — an empty list satisfies any count assertion")
	}
	var paths []string
	for _, wt := range list {
		if wt.Branch == branch && !wt.Prunable {
			paths = append(paths, wt.Path)
		}
	}
	return paths
}
