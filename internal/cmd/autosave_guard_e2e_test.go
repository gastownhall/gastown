package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/autosave"
)

// si-9wu1, end to end. THE GAP THESE CLOSE, STATED PRECISELY.
//
// autosave_guard_test.go proves stageForAutosave refuses an armed index and
// writes nothing. It calls that function directly. Nothing in it proves gt done
// ever REACHES the guard — and the path to it is not a straight line:
//
//	runDone -> auto-detect doneCleanupStatus (g.CheckUncommittedWork + g.Status)
//	        -> "uncommitted"?  -> HasUncommittedChanges && !CleanExcludingRuntime()
//	        -> stageForAutosave
//
// The armed state is a STAGED modification over a CLEAN working tree. If any
// link in that chain treated "worktree matches index" as clean — a plausible
// reading of every function named in it — the guard would never run, the
// auto-save would never run either, and all nine unit tests would still be
// green. The refusal would be dead code that tests beautifully.
//
// Both the bead and the Mayor's ruling recorded this in the same words: "NOT
// PROVEN: no end-to-end gt done run", with the ruling adding "whoever
// implements should close that by running it". This file runs it.
//
// TestRunDoneAutosavesOrdinaryUncommittedWork is the denominator and is not
// optional. Without it, the refusal test passes if runDone aborts ANYWHERE
// before the auto-save for any unrelated reason — "no commit landed" is exactly
// what a fixture that never got that far also looks like.

// armDoneWorktree stages a `lines`-line removal from within a file that still
// exists — the rebase signature — into an already-initialised repo, and asserts
// the fixture really is that state rather than a file deletion.
func armDoneWorktree(t *testing.T, workDir string, lines int) {
	t.Helper()

	body := strings.Repeat("a line of merged work\n", lines+1)
	writeMQSubmitTestFile(t, workDir, "delta.txt", body)
	runGitForMQSubmitTest(t, workDir, "add", "delta.txt")
	runGitForMQSubmitTest(t, workDir, "commit", "-m", "merged work")

	writeMQSubmitTestFile(t, workDir, "delta.txt", "a line of merged work\n")
	runGitForMQSubmitTest(t, workDir, "add", "delta.txt")

	// Control: invisible to the file-status query the OLD guard used, or this
	// fixture is not reproducing the defect.
	if out := strings.TrimSpace(gitOut(t, workDir, "diff", "--cached", "--name-status", "--diff-filter=D")); out != "" {
		t.Fatalf("fixture stages FILE deletions (%q) — that is not the rebase-armed state", out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return runGitForMQSubmitTest(t, dir, args...)
}

// setupAutosaveE2EDone builds the town + polecat worktree + bd stub that runDone
// needs, and leaves every done flag at its default so the CLEANUP STATUS IS
// AUTO-DETECTED. Passing doneCleanupStatus="uncommitted" by hand would skip the
// exact link under test.
func setupAutosaveE2EDone(t *testing.T) string {
	t.Helper()
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)
	setupRoutedSubmitGitRepo(t, workDir, false)
	installSubmitSourceBDRecorder(t, currentBeadsDir, ownerBeadsDir)
	resetDoneFlagsForTest(t)

	townRoot := routedSourceTestTownRoot(workDir)
	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))
	t.Setenv("GT_TOWN_ROOT", townRoot)
	t.Setenv("GT_ROOT", townRoot)
	t.Setenv("GT_ROLE", "gastown/polecats/refuge")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "refuge")
	t.Setenv("BD_ACTOR", "gastown/polecats/refuge")
	t.Chdir(workDir)

	doneIssue = "bd-source"
	doneSkipVerify = true
	updateAgentStateOnDoneFn = func(cwd, townRoot, exitType, issueID string) error { return nil }
	return workDir
}

// gt done against an armed worktree must refuse, and must leave the repository
// byte-for-byte as it found it. This is 504a5ed's scenario driven through the
// real entry point.
func TestRunDoneRefusesAnArmedWorktree(t *testing.T) {
	workDir := setupAutosaveE2EDone(t)
	armDoneWorktree(t, workDir, 500)

	beforeHead := gitOut(t, workDir, "rev-parse", "HEAD")
	beforeStaged := gitOut(t, workDir, "diff", "--cached", "--shortstat")
	beforeStatus := gitOut(t, workDir, "status", "--porcelain")

	err := runDone(nil, nil)

	// The substantive property FIRST, before anything about the error. Deleting
	// the guard makes runDone continue past the auto-save and die further down
	// for an unrelated reason; asserting the error shape first would report that
	// downstream symptom instead of the defect that caused it.
	if got := gitOut(t, workDir, "rev-parse", "HEAD"); got != beforeHead {
		t.Fatalf("THE AUTO-SAVE COMMITTED A 500-LINE REMOVAL. HEAD %q -> %q.\n"+
			"That is 504a5ed reproduced: 5 files, +26, -434, landed during an explicit "+
			"stop-write. Commit:\n%s", beforeHead, got,
			gitOut(t, workDir, "show", "--stat", "--format=%s", "HEAD"))
	}
	if got := gitOut(t, workDir, "diff", "--cached", "--shortstat"); got != beforeStaged {
		t.Errorf("the INDEX changed across a refusal:\nbefore %q\nafter  %q", beforeStaged, got)
	}
	if got := gitOut(t, workDir, "status", "--porcelain"); got != beforeStatus {
		t.Errorf("the working tree changed across a refusal:\nbefore %q\nafter  %q", beforeStatus, got)
	}

	// Nothing was committed — but that is also true of a run that aborted before
	// ever reaching the auto-save. The refusal has to be the REASON.
	if err == nil {
		t.Fatal("gt done completed against an armed worktree without refusing")
	}
	if !errors.Is(err, errAutosaveRefused) {
		t.Fatalf("nothing was committed, but NOT because the auto-save refused: %v\n"+
			"Some earlier guard stopped this run, so it does not exercise the path under test.", err)
	}

	// And the record has to outlive the session: gt done runs at session end,
	// when nobody is reading stdout.
	marker := filepath.Join(filepath.Dir(workDir), autosave.MarkerName)
	data, readErr := os.ReadFile(marker)
	if readErr != nil {
		t.Fatalf("no refusal marker at %s: %v", marker, readErr)
	}
	if !strings.Contains(string(data), "WORKING TREE WAS NOT MODIFIED") {
		t.Errorf("the marker does not lead with the one fact that stops a reader panicking; "+
			"a refusal notice that reads like a failure notice gets 'fixed' by disabling the "+
			"guard.\nmarker: %s", data)
	}
}

// DENOMINATOR. Ordinary uncommitted work must still be auto-saved by the same
// call — proving runDone reaches the auto-save with this fixture at all. Without
// this, the test above cannot distinguish "refused" from "never got there".
func TestRunDoneAutosavesOrdinaryUncommittedWork(t *testing.T) {
	workDir := setupAutosaveE2EDone(t)

	// A small removal plus new work: under the threshold, so the guard allows it.
	writeMQSubmitTestFile(t, workDir, "file.txt", "ordinary edit\n")
	writeMQSubmitTestFile(t, workDir, "new_work.txt", "implementation\n")

	beforeHead := strings.TrimSpace(gitOut(t, workDir, "rev-parse", "HEAD"))

	err := runDone(nil, nil)

	afterHead := strings.TrimSpace(gitOut(t, workDir, "rev-parse", "HEAD"))
	if afterHead == beforeHead {
		t.Fatalf("gt done did not auto-save ordinary uncommitted work (HEAD still %s, err=%v).\n"+
			"Then TestRunDoneRefusesAnArmedWorktree proves nothing: it asserts no commit landed, "+
			"and no commit lands on this fixture either way.", beforeHead, err)
	}
	if errors.Is(err, errAutosaveRefused) {
		t.Fatalf("the guard refused ordinary work below the %d-line threshold: %v",
			autosave.Threshold, err)
	}

	subject := gitOut(t, workDir, "log", "-1", "--format=%s")
	if !strings.Contains(subject, "auto-save") {
		t.Errorf("HEAD moved but not via the auto-save: %q", strings.TrimSpace(subject))
	}
	if staged := gitOut(t, workDir, "show", "--name-only", "--format=", "HEAD"); !strings.Contains(staged, "new_work.txt") {
		t.Errorf("the auto-save commit does not contain the new work: %q", staged)
	}

	// No marker: nothing was refused.
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(workDir), autosave.MarkerName)); statErr == nil {
		t.Error("a refusal marker was written for an allowed auto-save")
	}
}
