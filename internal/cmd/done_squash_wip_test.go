package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// wipTestRepo creates a git repo with a main branch, a feature branch, and the
// given sequence of commit subjects on the feature branch. Returns the repo dir.
func wipTestRepo(t *testing.T, subjects ...string) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	run("git", "init", "-b", "main")
	run("git", "config", "user.email", "test@test.com")
	run("git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "-A")
	run("git", "commit", "-m", "initial commit")
	run("git", "checkout", "-b", "feature")

	for i, subj := range subjects {
		name := "file" + strings.Repeat("x", i) + ".txt"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(subj+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		run("git", "add", name)
		run("git", "commit", "-m", subj)
	}
	return dir
}

func featureSubjects(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command("git", "log", "--format=%s", "main..HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

// TestSquashWIPCheckpointCommits_StripsWIPBeforePush verifies the gt done
// pre-push hook squashes checkpoint_dog's "WIP: checkpoint (auto)" commits so
// they never reach the remote (gastownhall/gastown#4440).
func TestSquashWIPCheckpointCommits_StripsWIPBeforePush(t *testing.T) {
	dir := wipTestRepo(t,
		"feat: real work",
		"WIP: checkpoint (auto)",
		"fix: more real work",
		"WIP: checkpoint (auto)",
	)

	squashWIPCheckpointCommits(dir, "main")

	subjects := featureSubjects(t, dir)
	if len(subjects) != 1 {
		t.Fatalf("expected 1 squashed commit, got %d: %v", len(subjects), subjects)
	}
	for _, s := range subjects {
		if strings.HasPrefix(s, "WIP: checkpoint (auto)") {
			t.Errorf("WIP checkpoint commit survived squash: %q", s)
		}
	}
	// The most recent non-WIP subject is preserved as the squash-commit title
	// (git log order: newest first).
	if subjects[0] != "fix: more real work" {
		t.Errorf("squash title = %q, want %q", subjects[0], "fix: more real work")
	}
}

// TestSquashWIPCheckpointCommits_NoWIPIsNoOp verifies branches without WIP
// checkpoint commits keep their history untouched.
func TestSquashWIPCheckpointCommits_NoWIPIsNoOp(t *testing.T) {
	dir := wipTestRepo(t, "feat: one", "fix: two")

	squashWIPCheckpointCommits(dir, "main")

	subjects := featureSubjects(t, dir)
	if len(subjects) != 2 {
		t.Fatalf("expected history untouched (2 commits), got %d: %v", len(subjects), subjects)
	}
}
