package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

func TestPruneRemotePolecatBranchesDryRunIncludesPatchEquivalentBranch(t *testing.T) {
	localDir, mainBranch := initPolecatPruneTestRepo(t)
	repoGit := git.NewGit(localDir)
	branch := "polecat/prune-patch-equivalent"

	runGit(t, localDir, "checkout", "-b", branch, mainBranch)
	writePolecatPruneTestFile(t, filepath.Join(localDir, "feature.txt"), "feature\n")
	runGit(t, localDir, "add", "feature.txt")
	runGit(t, localDir, "commit", "-m", "feature work")
	branchSHA, err := repoGit.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev branch: %v", err)
	}
	runGit(t, localDir, "push", "origin", branch)

	runGit(t, localDir, "checkout", mainBranch)
	writePolecatPruneTestFile(t, filepath.Join(localDir, "advance.txt"), "target advanced\n")
	runGit(t, localDir, "add", "advance.txt")
	runGit(t, localDir, "commit", "-m", "advance target")
	runGit(t, localDir, "cherry-pick", strings.TrimSpace(branchSHA))
	runGit(t, localDir, "push", "origin", mainBranch)
	if err := repoGit.FetchPrune("origin"); err != nil {
		t.Fatalf("FetchPrune: %v", err)
	}

	out := captureStdout(t, func() {
		pruned, err := pruneRemotePolecatBranches(repoGit, true)
		if err != nil {
			t.Fatalf("pruneRemotePolecatBranches: %v", err)
		}
		if pruned != 1 {
			t.Fatalf("pruned = %d, want 1", pruned)
		}
	})
	if !strings.Contains(out, "Would delete remote") || !strings.Contains(out, branch) {
		t.Fatalf("dry-run output %q, want branch %s", out, branch)
	}
	exists, err := repoGit.RemoteBranchExists("origin", branch)
	if err != nil {
		t.Fatalf("RemoteBranchExists: %v", err)
	}
	if !exists {
		t.Fatal("dry-run should not delete the remote branch")
	}
}

func initPolecatPruneTestRepo(t *testing.T) (string, string) {
	t.Helper()
	tmp := t.TempDir()
	remoteDir := filepath.Join(tmp, "remote.git")
	localDir := filepath.Join(tmp, "local")
	mainBranch := "main"

	runGit(t, tmp, "init", "--bare", "--initial-branch", mainBranch, remoteDir)
	runGit(t, tmp, "clone", remoteDir, localDir)
	runGit(t, localDir, "config", "user.email", "test@test.com")
	runGit(t, localDir, "config", "user.name", "Test User")
	writePolecatPruneTestFile(t, filepath.Join(localDir, "README.md"), "test\n")
	runGit(t, localDir, "add", "README.md")
	runGit(t, localDir, "commit", "-m", "initial")
	runGit(t, localDir, "push", "-u", "origin", mainBranch)

	return localDir, mainBranch
}

func writePolecatPruneTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
