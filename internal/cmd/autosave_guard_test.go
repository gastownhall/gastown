package cmd

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/autosave"
	"github.com/steveyegge/gastown/internal/git"
)

// si-9wu1 / ruling hq-vpt06. The auto-save's existing guard unstages FILE
// deletions and lets a mass line-removal through, because a rebase stages the
// delta as modifications to files that still exist. That is commit 504a5ed:
// 5 files, +26, -434, landed during an explicit stop-write.
//
// The ruling chose REFUSE specifically because it is the only option that never
// writes. That property is what these tests pin — not just "an error came back".

func guardRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// armedRepo returns a repo whose index is staged to remove `lines` lines from a
// file that still exists — the rebase signature, not a file deletion.
func armedRepo(t *testing.T, lines int) (string, string) {
	t.Helper()
	parent := t.TempDir()
	repo := filepath.Join(parent, "silicon")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	guardRun(t, repo, "init")
	guardRun(t, repo, "config", "user.email", "t@t.com")
	guardRun(t, repo, "config", "user.name", "T")

	body := strings.Repeat("a line of merged work\n", lines+1)
	if err := os.WriteFile(filepath.Join(repo, "delta.txt"), []byte(body), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	guardRun(t, repo, "add", "delta.txt")
	guardRun(t, repo, "commit", "-m", "merged work")

	if err := os.WriteFile(filepath.Join(repo, "delta.txt"), []byte("a line of merged work\n"), 0644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	guardRun(t, repo, "add", "delta.txt")

	// Control: the state must be invisible to the file-status query the OLD
	// guard used, or this fixture is not the defect.
	if out := strings.TrimSpace(guardRun(t, repo, "diff", "--cached", "--name-status", "--diff-filter=D")); out != "" {
		t.Fatalf("fixture stages FILE deletions (%q) — not the rebase-armed state", out)
	}
	return parent, repo
}

func TestRefuseArmedAutosaveRefusesTheRebaseArmedState(t *testing.T) {
	_, repo := armedRepo(t, 500)

	err := refuseArmedAutosave(git.NewGit(repo), repo, "polecat/keeper/si-aka.37")
	if err == nil {
		t.Fatal("auto-save was allowed to proceed against an index staged to delete 500 lines; " +
			"that is 504a5ed")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error does not report the line count: %v", err)
	}
}

// THE PROPERTY THE RULING TURNS ON. Refusing must leave the repository exactly
// as it found it — index and working tree. If refusal writes, it is no longer
// distinguishable from the options it was chosen over.
func TestRefuseArmedAutosaveWritesNothingToTheRepository(t *testing.T) {
	_, repo := armedRepo(t, 500)

	beforeStatus := guardRun(t, repo, "status", "--porcelain")
	beforeStaged := guardRun(t, repo, "diff", "--cached", "--shortstat")
	beforeHead := guardRun(t, repo, "rev-parse", "HEAD")

	// stageForAutosave, not refuseArmedAutosave: the point is that the staging
	// did NOT happen, and only the function that owns the staging can show it.
	err := stageForAutosave(git.NewGit(repo), repo, "polecat/keeper/si-aka.37")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !errors.Is(err, errAutosaveRefused) {
		t.Fatalf("error = %v, want it to wrap errAutosaveRefused — the caller aborts on that "+
			"sentinel and merely warns on anything else", err)
	}

	if got := guardRun(t, repo, "status", "--porcelain"); got != beforeStatus {
		t.Errorf("working tree/index changed across the refusal:\nbefore %q\nafter  %q", beforeStatus, got)
	}
	if got := guardRun(t, repo, "diff", "--cached", "--shortstat"); got != beforeStaged {
		t.Errorf("the INDEX changed across the refusal:\nbefore %q\nafter  %q", beforeStaged, got)
	}
	if got := guardRun(t, repo, "rev-parse", "HEAD"); got != beforeHead {
		t.Errorf("HEAD moved across the refusal: %q -> %q", beforeHead, got)
	}
}

// And the refusal must be findable afterwards, beside the worktree.
func TestRefuseArmedAutosaveLeavesADurableMarker(t *testing.T) {
	parent, repo := armedRepo(t, 500)

	if err := refuseArmedAutosave(git.NewGit(repo), repo, "polecat/keeper/si-aka.37"); err == nil {
		t.Fatal("expected a refusal")
	}

	marker := filepath.Join(parent, autosave.MarkerName)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("no refusal marker at %s: %v — a refusal that only prints is unobservable, "+
			"because the auto-save fires at session end when nobody reads stdout", marker, err)
	}
}

// Denominator: ordinary work must still be auto-saved. A guard that refuses
// everything protects nothing and gets removed.
func TestRefuseArmedAutosaveAllowsOrdinaryWork(t *testing.T) {
	_, repo := armedRepo(t, 10)

	if err := refuseArmedAutosave(git.NewGit(repo), repo, "polecat/keeper/si-aka.37"); err != nil {
		t.Fatalf("auto-save refused for a 10-line removal (threshold %d): %v", autosave.Threshold, err)
	}
}

// Denominator: a clean index must not be refused either.
func TestRefuseArmedAutosaveAllowsACleanIndex(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "silicon")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	guardRun(t, repo, "init")
	guardRun(t, repo, "config", "user.email", "t@t.com")
	guardRun(t, repo, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("hi\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	guardRun(t, repo, "add", "a.txt")
	guardRun(t, repo, "commit", "-m", "init")

	if err := refuseArmedAutosave(git.NewGit(repo), repo, "polecat/keeper/si-aka.37"); err != nil {
		t.Fatalf("auto-save refused on a clean index: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(parent, autosave.MarkerName)); statErr == nil {
		t.Fatal("a refusal marker was written for a clean index")
	}
}

// The staging must not run when the index is armed. This is the ordering the
// ruling depends on, and folding the staging into stageForAutosave is what
// makes it testable at all: a guard sitting one line above a call to
// "git add -A" in a 900-line function is enforced by nothing but proximity.
func TestStageForAutosaveDoesNotStageWhenItRefuses(t *testing.T) {
	_, repo := armedRepo(t, 500)

	// An untracked file that "git add -A" would stage. If staging ran, this
	// shows up in the index.
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("new\n"), 0644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}

	if err := stageForAutosave(git.NewGit(repo), repo, "polecat/keeper/si-aka.37"); err == nil {
		t.Fatal("expected a refusal")
	}

	staged := guardRun(t, repo, "diff", "--cached", "--name-only")
	if strings.Contains(staged, "untracked.txt") {
		t.Fatalf("untracked.txt was staged despite the refusal; staging ran before the guard, "+
			"and the refusal is then no longer the option that never writes.\nstaged: %q", staged)
	}
}

// Denominator for the above: when NOT refusing, stageForAutosave must actually
// stage — otherwise the test above passes because staging never works at all.
func TestStageForAutosaveStagesWhenItAllows(t *testing.T) {
	_, repo := armedRepo(t, 10)

	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("new\n"), 0644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}

	if err := stageForAutosave(git.NewGit(repo), repo, "polecat/keeper/si-aka.37"); err != nil {
		t.Fatalf("stageForAutosave: %v", err)
	}

	staged := guardRun(t, repo, "diff", "--cached", "--name-only")
	if !strings.Contains(staged, "untracked.txt") {
		t.Fatalf("stageForAutosave did not stage the working tree; the refusal test above would "+
			"then pass for the wrong reason.\nstaged: %q", staged)
	}
}
