package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGetGitStateDistinguishesSharedStashes(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	worktree := filepath.Join(dir, "other")

	runGitCmd(t, "", "init", repo)
	runGitCmd(t, repo, "config", "user.email", "test@example.com")
	runGitCmd(t, repo, "config", "user.name", "Test User")
	writeTestFile(t, filepath.Join(repo, "file.txt"), "base\n")
	runGitCmd(t, repo, "add", "file.txt")
	runGitCmd(t, repo, "commit", "-m", "base")
	runGitCmd(t, repo, "branch", "-M", "main")
	runGitCmd(t, repo, "checkout", "-b", "other")
	runGitCmd(t, repo, "checkout", "main")
	runGitCmd(t, repo, "worktree", "add", worktree, "other")

	writeTestFile(t, filepath.Join(repo, "file.txt"), "base\nmain change\n")
	runGitCmd(t, repo, "stash", "push", "-m", "main-only")

	state, err := getGitState(worktree)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}
	if state.StashCount != 0 {
		t.Fatalf("branch stash count = %d, want 0 for sibling branch stash", state.StashCount)
	}
	if state.SharedStashCount != 1 {
		t.Fatalf("shared stash count = %d, want 1", state.SharedStashCount)
	}
	if !state.Clean {
		t.Fatal("sibling branch stash must not make this worktree dirty")
	}

	writeTestFile(t, filepath.Join(worktree, "file.txt"), "base\nworktree change\n")
	runGitCmd(t, worktree, "stash", "push", "-m", "worktree-only")

	state, err = getGitState(worktree)
	if err != nil {
		t.Fatalf("getGitState after worktree stash: %v", err)
	}
	if state.StashCount != 1 {
		t.Fatalf("branch stash count = %d, want 1 for current branch stash", state.StashCount)
	}
	if state.SharedStashCount != 1 {
		t.Fatalf("shared stash count = %d, want 1 sibling stash", state.SharedStashCount)
	}
	if state.Clean {
		t.Fatal("current branch stash must still mark this worktree dirty")
	}
}

func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
