package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// si-d6kw: gt put two polecat worktrees on one branch. A commit in either then
// appeared in the other as a delta to re-commit, and the auto-save committed
// that delta with no agent acting — 7767 / 6824 / 434 staged deletions found
// armed across the fleet.
//
// The subtlety these tests exist to pin down: git refuses BOTH a live holder
// and a stale record with the same "already used by worktree at ..." message,
// which is why callers reached for --force in the first place. Dropping --force
// would fix the collision and break the reap-and-resume workflow it was added
// for. So the guard is not "refuse when git refuses" — it is "refuse a LIVE
// holder, clear a dead one" — and the tests have to show both halves, plus that
// the refusal is the guard's and not git's.

func worktreeHolderRepo(t *testing.T) (*Git, string) {
	t.Helper()
	dir := initTestRepo(t)
	return NewGit(dir), dir
}

// addWorktreeOnNewBranch creates a real worktree holding a real branch.
func addWorktreeOnNewBranch(t *testing.T, repo string, path, branch string) {
	t.Helper()
	cmd := exec.Command("git", "worktree", "add", "-b", branch, path)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed worktree %s on %s: %v\n%s", path, branch, err, out)
	}
}

func TestWorktreeAddExistingSafeRefusesBranchHeldByLiveWorktree(t *testing.T) {
	g, repo := worktreeHolderRepo(t)
	holder := filepath.Join(t.TempDir(), "holder")
	addWorktreeOnNewBranch(t, repo, holder, "polecat/dag/si-aka.9")

	second := filepath.Join(t.TempDir(), "second")
	err := g.WorktreeAddExistingSafe(second, "polecat/dag/si-aka.9")
	if err == nil {
		t.Fatal("WorktreeAddExistingSafe attached a second worktree to a live-held branch; " +
			"that is the si-d6kw defect")
	}

	var held *BranchHeldError
	if !errors.As(err, &held) {
		t.Fatalf("error = %v (%T), want *BranchHeldError — callers switch on this type to "+
			"produce the actionable refusal", err, err)
	}
	if !sameResolvedPath(t, held.Path, holder) {
		t.Fatalf("BranchHeldError.Path = %q, want %q — the message must NAME the holder, "+
			"or the operator cannot act on it", held.Path, holder)
	}

	// The refusal must be total. A partially-created worktree is the hazard.
	if _, statErr := os.Stat(second); !os.IsNotExist(statErr) {
		t.Fatalf("refused, but %s exists anyway — nothing may be created on the refusal path", second)
	}
	for _, wt := range mustList(t, g) {
		if wt.Path == second {
			t.Fatalf("refused, but git registered a worktree at %s", second)
		}
	}
}

// NEGATIVE CONTROL. Without this, the test above passes even if the guard does
// nothing, because plain git would refuse too — and the whole point is that the
// old code got PAST git's refusal. This asserts the setup really is one --force
// away from the two-worktrees-on-one-ref state, so the refusal above is
// attributable to the guard rather than to git.
func TestWorktreeAddExistingForceStillReachesTheStateTheGuardRefuses(t *testing.T) {
	g, repo := worktreeHolderRepo(t)
	holder := filepath.Join(t.TempDir(), "holder")
	addWorktreeOnNewBranch(t, repo, holder, "polecat/dag/si-aka.9")

	second := filepath.Join(t.TempDir(), "second")
	if err := g.WorktreeAddExistingForce(second, "polecat/dag/si-aka.9"); err != nil {
		t.Fatalf("WorktreeAddExistingForce: %v — if this now fails, the negative control is "+
			"stale and the refusal test above may be measuring git rather than the guard", err)
	}

	var holders int
	for _, wt := range mustList(t, g) {
		if wt.Branch == "polecat/dag/si-aka.9" {
			holders++
		}
	}
	if holders != 2 {
		t.Fatalf("worktrees on polecat/dag/si-aka.9 = %d, want 2 — the reproduction of si-d6kw "+
			"did not reproduce, so the guard test proves nothing", holders)
	}
}

// The workflow that must NOT break. Alice's recovery slings were 4-of-5 clean
// precisely because the original worktree had been reaped; a guard that refuses
// this case removes the only working recovery route and pushes operators to a
// worse one.
func TestWorktreeAddExistingSafeResumesBranchAfterHolderWasReaped(t *testing.T) {
	g, repo := worktreeHolderRepo(t)
	holder := filepath.Join(t.TempDir(), "holder")
	addWorktreeOnNewBranch(t, repo, holder, "polecat/keeper/si-aka.37")

	// Reap it the way the fleet does: the directory goes, the record remains.
	if err := os.RemoveAll(holder); err != nil {
		t.Fatalf("reap holder: %v", err)
	}

	// Denominator: the stale record must actually still be there, otherwise this
	// test proves only that attaching to an unheld branch works.
	if !recordExistsFor(t, g, "polecat/keeper/si-aka.37") {
		t.Fatal("no worktree record for the branch after reaping — test setup no longer " +
			"reproduces the stale-holder case it is named for")
	}

	resumed := filepath.Join(t.TempDir(), "resumed")
	if err := g.WorktreeAddExistingSafe(resumed, "polecat/keeper/si-aka.37"); err != nil {
		t.Fatalf("WorktreeAddExistingSafe refused a reaped holder: %v\n"+
			"This is the resume workflow --force existed for; refusing it is the regression "+
			"si-d6kw's own fix order warned about", err)
	}
	assertOnBranch(t, resumed, "polecat/keeper/si-aka.37")
}

// The case a directory-existence check alone gets WRONG, and the reason
// LiveWorktreeForBranch reads git's prunable flag rather than just stat()ing
// the path. A worktree whose .git file is broken still has its DIRECTORY on
// disk, and git still reports the branch as taken — but it is not a live
// worker, it is the broken worktree that "gt polecat repair" exists for.
// Treating it as a live holder makes such a polecat permanently unresumable.
//
// This test was added because a mutation that deleted the Prunable check went
// GREEN: the stat() fallback covered the reaped-directory case and hid it.
func TestWorktreeAddExistingSafeResumesWorktreeWhoseGitFileIsBroken(t *testing.T) {
	g, repo := worktreeHolderRepo(t)
	broken := filepath.Join(t.TempDir(), "broken")
	addWorktreeOnNewBranch(t, repo, broken, "polecat/coma/si-aka.14.2")

	// Break the link without removing the directory.
	if err := os.Remove(filepath.Join(broken, ".git")); err != nil {
		t.Fatalf("break worktree .git: %v", err)
	}

	// Denominator: the state under test must actually hold — directory present,
	// git reporting prunable. If either drifts, this test silently becomes a
	// duplicate of the reaped-holder one.
	if _, err := os.Stat(broken); err != nil {
		t.Fatalf("broken worktree directory should still exist: %v", err)
	}
	var sawBrokenAsPrunable bool
	for _, wt := range mustList(t, g) {
		if wt.Branch == "polecat/coma/si-aka.14.2" && wt.Prunable {
			sawBrokenAsPrunable = true
		}
	}
	if !sawBrokenAsPrunable {
		t.Fatal("git no longer reports the broken worktree as prunable — this test is not " +
			"exercising the directory-exists-but-dead case it is named for")
	}

	resumed := filepath.Join(t.TempDir(), "resumed")
	if err := g.WorktreeAddExistingSafe(resumed, "polecat/coma/si-aka.14.2"); err != nil {
		t.Fatalf("WorktreeAddExistingSafe refused a broken (not live) worktree: %v\n"+
			"A directory-existence check alone gives this wrong answer; the prunable flag "+
			"is what distinguishes 'someone is working here' from 'this worktree is dead'", err)
	}
	assertOnBranch(t, resumed, "polecat/coma/si-aka.14.2")
}

func TestWorktreeAddExistingSafeAttachesWhenNoWorktreeHoldsBranch(t *testing.T) {
	g, repo := worktreeHolderRepo(t)
	cmd := exec.Command("git", "branch", "polecat/toast/si-aka.9+rework")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create branch: %v\n%s", err, out)
	}

	path := filepath.Join(t.TempDir(), "fresh")
	if err := g.WorktreeAddExistingSafe(path, "polecat/toast/si-aka.9+rework"); err != nil {
		t.Fatalf("WorktreeAddExistingSafe on an unheld branch: %v", err)
	}
	assertOnBranch(t, path, "polecat/toast/si-aka.9+rework")
}

// LiveWorktreeForBranch is the predicate the whole guard rests on. Test it
// directly so a failure points at the discrimination rather than at the caller.
func TestLiveWorktreeForBranchIgnoresPrunableRecords(t *testing.T) {
	g, repo := worktreeHolderRepo(t)
	live := filepath.Join(t.TempDir(), "live")
	stale := filepath.Join(t.TempDir(), "stale")
	addWorktreeOnNewBranch(t, repo, live, "branch/live")
	addWorktreeOnNewBranch(t, repo, stale, "branch/stale")
	if err := os.RemoveAll(stale); err != nil {
		t.Fatalf("reap: %v", err)
	}

	// Denominator on the parse itself: if WorktreeList stopped reporting
	// prunable at all, every record would look live and the "stale" assertion
	// below would still pass by accident on a differently-broken parser.
	list := mustList(t, g)
	var sawPrunable bool
	for _, wt := range list {
		if wt.Branch == "branch/stale" {
			if !wt.Prunable {
				t.Fatalf("record for branch/stale parsed with Prunable=false; "+
					"reason=%q — git reports it as prunable", wt.PrunableReason)
			}
			sawPrunable = true
		}
	}
	if !sawPrunable {
		t.Fatal("no record found for branch/stale — cannot distinguish 'correctly ignored' " +
			"from 'never examined'")
	}

	got, err := g.LiveWorktreeForBranch("branch/stale")
	if err != nil {
		t.Fatalf("LiveWorktreeForBranch(stale): %v", err)
	}
	if got != nil {
		t.Fatalf("LiveWorktreeForBranch(branch/stale) = %s, want nil — a reaped worktree is "+
			"not a live holder", got.Path)
	}

	got, err = g.LiveWorktreeForBranch("branch/live")
	if err != nil {
		t.Fatalf("LiveWorktreeForBranch(live): %v", err)
	}
	if got == nil || !sameResolvedPath(t, got.Path, live) {
		t.Fatalf("LiveWorktreeForBranch(branch/live) = %v, want %s", got, live)
	}

	// refs/heads/ prefix must resolve the same way; callers pass both forms.
	got, err = g.LiveWorktreeForBranch("refs/heads/branch/live")
	if err != nil {
		t.Fatalf("LiveWorktreeForBranch(refs/heads/...): %v", err)
	}
	if got == nil || !sameResolvedPath(t, got.Path, live) {
		t.Fatalf("LiveWorktreeForBranch with refs/heads/ prefix = %v, want %s", got, live)
	}
}

// samePath compares paths after resolving symlinks. On macOS t.TempDir() hands
// back /var/... while git reports the resolved /private/var/..., so a raw
// string compare fails on a correct result.
func sameResolvedPath(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		ra = a
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		rb = b
	}
	return ra == rb
}

// PRODUCTION ARCHITECTURE. Rigs use a bare .repo.git as the repo base and hang
// every polecat worktree off it (si-d6kw measured dag's git-dir as
// .repo.git/worktrees/silicon7). The manager-level tests run on the LEGACY
// mayor/rig base because that is what the scaffolding builds, so without this
// test nothing exercises the guard on the shape it actually ships against.
//
// Two properties matter, and the first is the one that would bite: a bare repo
// appears in "worktree list --porcelain" with a "bare" line and NO "branch"
// line. If that parsed as a worktree holding something, the base itself could
// read as a live holder and refuse every resume.
func TestWorktreeGuardOnBareRepoBase(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	runGit(t, src, "init", "-b", "main", ".")
	runGit(t, src, "config", "user.email", "test@test.com")
	runGit(t, src, "config", "user.name", "Test User")
	runGit(t, src, "commit", "--allow-empty", "-m", "init")

	bare := filepath.Join(root, "repo.git")
	runGit(t, root, "clone", "--bare", src, bare)
	g := NewGitWithDir(bare, "")

	// The bare base holds no branch.
	for _, wt := range mustList(t, g) {
		if wt.Branch != "" && wt.Path == bare {
			t.Fatalf("bare repo base parsed as holding branch %q; it has no working tree "+
				"and must never read as a live holder", wt.Branch)
		}
	}
	held, err := g.LiveWorktreeForBranch("main")
	if err != nil {
		t.Fatalf("LiveWorktreeForBranch(main) on bare base: %v", err)
	}
	if held != nil {
		t.Fatalf("bare base reported as live holder of main at %s — every resume would refuse", held.Path)
	}

	// A polecat worktree off the bare base IS a live holder.
	runGit(t, bare, "branch", "polecat/dag/si-aka.9", "main")
	holder := filepath.Join(root, "dag")
	runGit(t, bare, "worktree", "add", holder, "polecat/dag/si-aka.9")

	second := filepath.Join(root, "toast")
	err = g.WorktreeAddExistingSafe(second, "polecat/dag/si-aka.9")
	var branchHeld *BranchHeldError
	if !errors.As(err, &branchHeld) {
		t.Fatalf("WorktreeAddExistingSafe on bare base = %v, want *BranchHeldError — "+
			"this is the exact dag/toast collision, on the exact architecture it happened on", err)
	}

	// And the reaped case still resumes on a bare base.
	if err := os.RemoveAll(holder); err != nil {
		t.Fatalf("reap: %v", err)
	}
	if err := g.WorktreeAddExistingSafe(second, "polecat/dag/si-aka.9"); err != nil {
		t.Fatalf("resume after reap on bare base: %v", err)
	}
	assertOnBranch(t, second, "polecat/dag/si-aka.9")
}

func mustList(t *testing.T, g *Git) []Worktree {
	t.Helper()
	list, err := g.WorktreeList()
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("WorktreeList returned nothing — an empty list satisfies every loop below")
	}
	return list
}

func recordExistsFor(t *testing.T, g *Git, branch string) bool {
	t.Helper()
	for _, wt := range mustList(t, g) {
		if wt.Branch == branch {
			return true
		}
	}
	return false
}

func assertOnBranch(t *testing.T, path, branch string) {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = path
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse in %s: %v", path, err)
	}
	if got := strings.TrimSpace(string(out)); got != branch {
		t.Fatalf("worktree %s is on %q, want %q", path, got, branch)
	}
}
