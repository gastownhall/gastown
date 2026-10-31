package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// si-mefr: si-d6kw's DETECTION half. Prevention closed the sling route, but
// WorktreeAddExistingForce still has legitimate callers and a hand-run
// "git worktree add --force" bypasses everything — so sharing can still arise,
// and until now nothing would report it.
//
// The fixtures below drive REAL git worktrees rather than canned structs,
// because both properties under test are properties of git's own output:
// whether a stale record is distinguishable from a live holder, and whether a
// rebase-armed index is visible to a line count. A struct literal would assert
// my belief about git's output rather than git's output.

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// townWithRig builds townRoot/<rig>/.repo.git as a real repository, matching
// the layout gt actually creates, and returns (townRoot, repoPath).
func townWithRig(t *testing.T, rigName string) (string, string) {
	t.Helper()
	townRoot := t.TempDir()

	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	gitRun(t, src, "init")
	gitRun(t, src, "config", "user.email", "test@test.com")
	gitRun(t, src, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("# test\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	gitRun(t, src, "add", ".")
	gitRun(t, src, "commit", "-m", "initial")

	rigPath := filepath.Join(townRoot, rigName)
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}
	// config.json makes isRigDir recognise it.
	if err := os.WriteFile(filepath.Join(rigPath, "config.json"), []byte("{}\n"), 0644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	repoPath := filepath.Join(rigPath, ".repo.git")
	gitRun(t, townRoot, "clone", "--bare", src, repoPath)

	return townRoot, repoPath
}

func addWorktree(t *testing.T, repoPath, wtPath, branch string, force bool) {
	t.Helper()
	args := []string{"worktree", "add"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, wtPath, branch)
	gitRun(t, repoPath, args...)
}

// A: two LIVE worktrees on one ref must be reported, naming both paths.
func TestSharedRefsReportsTwoLiveWorktreesOnOneBranch(t *testing.T) {
	_, repoPath := townWithRig(t, "silicon")
	gitRun(t, repoPath, "branch", "polecat/dag/si-aka.9", "HEAD")

	first := filepath.Join(t.TempDir(), "dag")
	second := filepath.Join(t.TempDir(), "toast")
	addWorktree(t, repoPath, first, "polecat/dag/si-aka.9", false)
	// --force is how the real collision happened; plain add refuses here.
	addWorktree(t, repoPath, second, "polecat/dag/si-aka.9", true)

	wts, err := git.NewGit(repoPath).WorktreeList()
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}

	shared := sharedRefs(wts)
	if len(shared) != 1 {
		t.Fatalf("sharedRefs found %d shared branches, want 1: %+v\n(worktrees: %+v)", len(shared), shared, wts)
	}
	if shared[0].Branch != "polecat/dag/si-aka.9" {
		t.Fatalf("shared branch = %q, want polecat/dag/si-aka.9", shared[0].Branch)
	}
	if len(shared[0].Paths) != 2 {
		t.Fatalf("shared paths = %v, want both holders — a report that does not NAME them "+
			"cannot be acted on", shared[0].Paths)
	}
}

// B: one live holder plus a PRUNABLE record on the same ref is the normal state
// after a reap. A check that reds here gets deleted, and then there is no check.
func TestSharedRefsIgnoresAPrunableRecord(t *testing.T) {
	_, repoPath := townWithRig(t, "silicon")
	gitRun(t, repoPath, "branch", "polecat/keeper/si-aka.37", "HEAD")

	live := filepath.Join(t.TempDir(), "keeper-live")
	reaped := filepath.Join(t.TempDir(), "keeper-reaped")
	addWorktree(t, repoPath, live, "polecat/keeper/si-aka.37", false)
	addWorktree(t, repoPath, reaped, "polecat/keeper/si-aka.37", true)

	// Reap it: the directory goes, git's administrative record stays.
	if err := os.RemoveAll(reaped); err != nil {
		t.Fatalf("reap worktree: %v", err)
	}

	wts, err := git.NewGit(repoPath).WorktreeList()
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}

	// Control: the reaped record must still be PRESENT in git's output, or this
	// test proves nothing about filtering — it would just be counting one entry.
	var sawPrunable bool
	for _, wt := range wts {
		if wt.Prunable {
			sawPrunable = true
		}
	}
	if !sawPrunable {
		t.Fatalf("no prunable record in %+v — git dropped the reaped worktree from its list, so "+
			"this fixture does not exercise the prunable filter at all", wts)
	}

	if shared := sharedRefs(wts); len(shared) != 0 {
		t.Fatalf("sharedRefs reported %+v for one live worktree plus a reaped record; this is the "+
			"normal post-reap state and flagging it is how the check gets deleted", shared)
	}
}

// Two live worktrees on DIFFERENT branches is the ordinary healthy fleet.
func TestSharedRefsIgnoresDistinctBranches(t *testing.T) {
	_, repoPath := townWithRig(t, "silicon")
	gitRun(t, repoPath, "branch", "polecat/dag/si-aka.9", "HEAD")
	gitRun(t, repoPath, "branch", "polecat/toast/si-aka.9+rework", "HEAD")

	addWorktree(t, repoPath, filepath.Join(t.TempDir(), "dag"), "polecat/dag/si-aka.9", false)
	addWorktree(t, repoPath, filepath.Join(t.TempDir(), "toast"), "polecat/toast/si-aka.9+rework", false)

	wts, err := git.NewGit(repoPath).WorktreeList()
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	if shared := sharedRefs(wts); len(shared) != 0 {
		t.Fatalf("sharedRefs reported %+v for two worktrees on distinct branches", shared)
	}
}

// A detached HEAD has no branch to share; grouping on an empty string would
// collapse every detached worktree into one bogus "shared ref".
func TestSharedRefsIgnoresDetachedWorktrees(t *testing.T) {
	_, repoPath := townWithRig(t, "silicon")

	first := filepath.Join(t.TempDir(), "d1")
	second := filepath.Join(t.TempDir(), "d2")
	gitRun(t, repoPath, "worktree", "add", "--detach", first, "HEAD")
	gitRun(t, repoPath, "worktree", "add", "--detach", second, "HEAD")

	wts, err := git.NewGit(repoPath).WorktreeList()
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	if shared := sharedRefs(wts); len(shared) != 0 {
		t.Fatalf("sharedRefs reported %+v for two DETACHED worktrees — they hold no branch", shared)
	}
}

// End to end through the doctor check, on the real layout.
func TestWorktreeSharedRefCheckFailsOnASharedRef(t *testing.T) {
	townRoot, repoPath := townWithRig(t, "silicon")
	gitRun(t, repoPath, "branch", "polecat/dag/si-aka.9", "HEAD")
	addWorktree(t, repoPath, filepath.Join(t.TempDir(), "dag"), "polecat/dag/si-aka.9", false)
	addWorktree(t, repoPath, filepath.Join(t.TempDir(), "toast"), "polecat/dag/si-aka.9", true)

	res := NewWorktreeSharedRefCheck().Run(&CheckContext{TownRoot: townRoot})
	if res.Status != StatusError {
		t.Fatalf("status = %v, want StatusError; message=%q details=%v", res.Status, res.Message, res.Details)
	}
	if !strings.Contains(strings.Join(res.Details, "\n"), "polecat/dag/si-aka.9") {
		t.Fatalf("details %v do not name the shared ref", res.Details)
	}
}

func TestWorktreeSharedRefCheckPassesOnAHealthyRig(t *testing.T) {
	townRoot, repoPath := townWithRig(t, "silicon")
	gitRun(t, repoPath, "branch", "polecat/dag/si-aka.9", "HEAD")
	addWorktree(t, repoPath, filepath.Join(t.TempDir(), "dag"), "polecat/dag/si-aka.9", false)

	res := NewWorktreeSharedRefCheck().Run(&CheckContext{TownRoot: townRoot})
	if res.Status != StatusOK {
		t.Fatalf("status = %v on a healthy rig, want StatusOK; message=%q details=%v",
			res.Status, res.Message, res.Details)
	}
}

// D, the denominator: examining nothing must NOT read as a clean bill of health.
// This is the failure mode that makes a check worthless — it reports OK forever
// once its enumeration breaks, and nobody notices because OK is what they expect.
func TestWorktreeSharedRefCheckFailsWhenItExaminesNothing(t *testing.T) {
	res := NewWorktreeSharedRefCheck().Run(&CheckContext{TownRoot: t.TempDir()})
	if res.Status == StatusOK {
		t.Fatalf("status = StatusOK having examined zero worktrees (message=%q); an empty "+
			"enumeration must not report healthy", res.Message)
	}
}

// A repo that cannot be READ must not reduce to a clean bill of health. This is
// the same failure as the empty enumeration one layer down: git failing and git
// finding nothing look identical in a bare status, and the reassuring one is
// the wrong default.
func TestWorktreeSharedRefCheckDoesNotReportOKWhenARepoCannotBeRead(t *testing.T) {
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "silicon")
	if err := os.MkdirAll(filepath.Join(rigPath, ".repo.git"), 0755); err != nil {
		t.Fatalf("mkdir bogus repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigPath, "config.json"), []byte("{}\n"), 0644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	res := NewWorktreeSharedRefCheck().Run(&CheckContext{TownRoot: townRoot})
	if res.Status == StatusOK {
		t.Fatalf("status = StatusOK for a .repo.git that is not a git repository (message=%q); "+
			"an unreadable repo is not a healthy one", res.Message)
	}
}

// C, through the doctor check: an armed worktree is reported. The line-based
// vs file-status proof lives in internal/git's staged_line_stat_test.go, which
// carries the negative control asserting --diff-filter=D is blind to it.
func TestWorktreeArmedStagingCheckFailsOnAnArmedWorktree(t *testing.T) {
	townRoot, repoPath := townWithRig(t, "silicon")
	wt := filepath.Join(t.TempDir(), "keeper")
	gitRun(t, repoPath, "branch", "polecat/keeper/si-aka.37", "HEAD")
	addWorktree(t, repoPath, wt, "polecat/keeper/si-aka.37", false)

	// Arm it the way a rebase does: contents removed, file still present.
	body := strings.Repeat("a line of merged work\n", 500)
	if err := os.WriteFile(filepath.Join(wt, "delta.txt"), []byte(body), 0644); err != nil {
		t.Fatalf("write delta: %v", err)
	}
	gitRun(t, wt, "config", "user.email", "test@test.com")
	gitRun(t, wt, "config", "user.name", "Test User")
	gitRun(t, wt, "add", "delta.txt")
	gitRun(t, wt, "commit", "-m", "merged work")
	if err := os.WriteFile(filepath.Join(wt, "delta.txt"), []byte("a line of merged work\n"), 0644); err != nil {
		t.Fatalf("rewrite delta: %v", err)
	}
	gitRun(t, wt, "add", "delta.txt")

	// Control: file-status is blind to this, which is why the check counts lines.
	if out := strings.TrimSpace(gitRun(t, wt, "diff", "--cached", "--name-status", "--diff-filter=D")); out != "" {
		t.Fatalf("fixture stages FILE deletions (%q); it is not the rebase-armed state", out)
	}

	res := NewWorktreeArmedStagingCheck().Run(&CheckContext{TownRoot: townRoot})
	if res.Status != StatusError {
		t.Fatalf("status = %v, want StatusError; message=%q details=%v", res.Status, res.Message, res.Details)
	}
	if !strings.Contains(strings.Join(res.Details, "\n"), "499") {
		t.Fatalf("details %v do not report the staged line count", res.Details)
	}
}

func TestWorktreeArmedStagingCheckPassesOnACleanWorktree(t *testing.T) {
	townRoot, repoPath := townWithRig(t, "silicon")
	wt := filepath.Join(t.TempDir(), "keeper")
	gitRun(t, repoPath, "branch", "polecat/keeper/si-aka.37", "HEAD")
	addWorktree(t, repoPath, wt, "polecat/keeper/si-aka.37", false)

	res := NewWorktreeArmedStagingCheck().Run(&CheckContext{TownRoot: townRoot})
	if res.Status != StatusOK {
		t.Fatalf("status = %v on a clean worktree, want StatusOK; message=%q details=%v",
			res.Status, res.Message, res.Details)
	}
}

// A small staged deletion is ordinary editing, not an armed rebase artifact.
// Without this the check fires on normal work and gets turned off.
func TestWorktreeArmedStagingCheckIgnoresSmallStagedDeletions(t *testing.T) {
	townRoot, repoPath := townWithRig(t, "silicon")
	wt := filepath.Join(t.TempDir(), "keeper")
	gitRun(t, repoPath, "branch", "polecat/keeper/si-aka.37", "HEAD")
	addWorktree(t, repoPath, wt, "polecat/keeper/si-aka.37", false)

	gitRun(t, wt, "config", "user.email", "test@test.com")
	gitRun(t, wt, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(wt, "small.txt"), []byte(strings.Repeat("x\n", 10)), 0644); err != nil {
		t.Fatalf("write small: %v", err)
	}
	gitRun(t, wt, "add", "small.txt")
	gitRun(t, wt, "commit", "-m", "small file")
	if err := os.WriteFile(filepath.Join(wt, "small.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatalf("rewrite small: %v", err)
	}
	gitRun(t, wt, "add", "small.txt")

	res := NewWorktreeArmedStagingCheck().Run(&CheckContext{TownRoot: townRoot})
	if res.Status != StatusOK {
		t.Fatalf("status = %v for 9 staged line-deletions (threshold %d), want StatusOK; details=%v",
			res.Status, armedStagingThreshold, res.Details)
	}
}

// D for the armed check too.
func TestWorktreeArmedStagingCheckFailsWhenItExaminesNothing(t *testing.T) {
	res := NewWorktreeArmedStagingCheck().Run(&CheckContext{TownRoot: t.TempDir()})
	if res.Status == StatusOK {
		t.Fatalf("status = StatusOK having examined zero worktrees (message=%q)", res.Message)
	}
}
