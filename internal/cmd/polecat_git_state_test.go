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

func TestGetGitStateUsesUpstreamInsteadOfOriginMain(t *testing.T) {
	repo := setupGitStateRemoteRepo(t)

	runGitCmd(t, repo, "switch", "integration/test")
	writeTestFile(t, filepath.Join(repo, "integration.txt"), "integration\n")
	runGitCmd(t, repo, "add", "integration.txt")
	runGitCmd(t, repo, "commit", "-m", "integration")
	runGitCmd(t, repo, "push", "origin", "integration/test")
	runGitCmd(t, repo, "switch", "-c", "polecat/test")
	runGitCmd(t, repo, "branch", "--set-upstream-to=origin/integration/test")

	state, err := getGitState(repo)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}
	if !state.Clean {
		t.Fatalf("branch matching its integration upstream should be clean: %+v", state)
	}
	if state.UnpushedCommits != 0 {
		t.Fatalf("UnpushedCommits = %d, want 0", state.UnpushedCommits)
	}
}

func TestGetGitStateCountsAheadOfUpstream(t *testing.T) {
	repo := setupGitStateRemoteRepo(t)
	runGitCmd(t, repo, "switch", "-c", "polecat/test")
	runGitCmd(t, repo, "branch", "--set-upstream-to=origin/integration/test")
	writeTestFile(t, filepath.Join(repo, "feature.txt"), "feature\n")
	runGitCmd(t, repo, "add", "feature.txt")
	runGitCmd(t, repo, "commit", "-m", "feature")

	state, err := getGitState(repo)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}
	if state.Clean {
		t.Fatal("local commit ahead of upstream should make git state dirty")
	}
	if state.UnpushedCommits != 1 {
		t.Fatalf("UnpushedCommits = %d, want 1", state.UnpushedCommits)
	}
}

func TestGetGitStateTreatsPushedSourceBranchAsClean(t *testing.T) {
	repo := setupGitStateRemoteRepo(t)
	runGitCmd(t, repo, "switch", "-c", "polecat/pushed")
	writeTestFile(t, filepath.Join(repo, "pushed.txt"), "pushed\n")
	runGitCmd(t, repo, "add", "pushed.txt")
	runGitCmd(t, repo, "commit", "-m", "pushed")
	runGitCmd(t, repo, "push", "-u", "origin", "polecat/pushed")

	state, err := getGitState(repo)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}
	if !state.Clean {
		t.Fatalf("fully pushed source branch should not be classified as unpushed: %+v", state)
	}
	if state.UnpushedCommits != 0 {
		t.Fatalf("UnpushedCommits = %d, want 0", state.UnpushedCommits)
	}
}

func TestGetGitStateIgnoresOpenCodeRuntimeArtifacts(t *testing.T) {
	repo := setupGitStateRemoteRepo(t)
	runGitCmd(t, repo, "switch", "-c", "polecat/opencode-runtime")

	if err := os.MkdirAll(filepath.Join(repo, ".opencode", "plugins"), 0755); err != nil {
		t.Fatalf("mkdir opencode plugins: %v", err)
	}
	writeTestFile(t, filepath.Join(repo, ".opencode", "plugins", "gastown.js"), "// generated\n")
	state, err := getGitState(repo)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}
	if !state.Clean {
		t.Fatalf("OpenCode runtime artifact should not dirty recovery state: %+v", state)
	}
	if len(state.UncommittedFiles) != 0 {
		t.Fatalf("UncommittedFiles = %v, want none for runtime-only artifact", state.UncommittedFiles)
	}

	writeTestFile(t, filepath.Join(repo, "real.go"), "package real\n")
	state, err = getGitState(repo)
	if err != nil {
		t.Fatalf("getGitState with real file: %v", err)
	}
	if state.Clean {
		t.Fatal("real source dirt should still block recovery")
	}
	if len(state.UncommittedFiles) != 1 || state.UncommittedFiles[0] != "real.go" {
		t.Fatalf("UncommittedFiles = %v, want only real.go", state.UncommittedFiles)
	}
}

func setupGitStateRemoteRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	repo := filepath.Join(dir, "repo")
	runGitCmd(t, "", "init", "--bare", remote)
	runGitCmd(t, "", "init", repo)
	runGitCmd(t, repo, "config", "user.email", "test@example.com")
	runGitCmd(t, repo, "config", "user.name", "Test User")
	writeTestFile(t, filepath.Join(repo, "README.md"), "base\n")
	runGitCmd(t, repo, "add", "README.md")
	runGitCmd(t, repo, "commit", "-m", "base")
	runGitCmd(t, repo, "branch", "-M", "main")
	runGitCmd(t, repo, "remote", "add", "origin", remote)
	runGitCmd(t, repo, "push", "-u", "origin", "main")
	runGitCmd(t, repo, "switch", "-c", "integration/test")
	runGitCmd(t, repo, "push", "-u", "origin", "integration/test")
	return repo
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
