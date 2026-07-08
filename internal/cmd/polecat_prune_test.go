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
	createPatchEquivalentRemoteBranch(t, repoGit, localDir, mainBranch, branch)
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
	assertRemotePruneDryRunKeptBranch(t, repoGit, out, branch)
}

func TestRunPolecatPruneRemoteDryRunIncludesPatchEquivalentBranch(t *testing.T) {
	townRoot, rigName := setupTestRigForSettings(t)
	localDir := filepath.Join(townRoot, rigName, "mayor", "rig")
	mainBranch := initPolecatPruneTestRepoAt(t, localDir)
	repoGit := git.NewGit(localDir)
	branch := "polecat/prune-command-patch-equivalent"
	createPatchEquivalentRemoteBranch(t, repoGit, localDir, mainBranch, branch)

	oldRemote, oldDryRun := polecatPruneRemote, polecatPruneDryRun
	polecatPruneRemote = true
	polecatPruneDryRun = true
	t.Cleanup(func() {
		polecatPruneRemote = oldRemote
		polecatPruneDryRun = oldDryRun
	})

	out := captureStdout(t, func() {
		if err := runPolecatPrune(nil, []string{rigName}); err != nil {
			t.Fatalf("runPolecatPrune: %v", err)
		}
	})
	assertRemotePruneDryRunKeptBranch(t, repoGit, out, branch)
}

func TestPruneRemotePolecatBranchesUsesUpstreamBaseForOriginFork(t *testing.T) {
	localDir, mainBranch := initPolecatPruneOriginForkRepo(t)
	repoGit := git.NewGit(localDir)
	branch := "polecat/fork-only-preserved"

	runGit(t, localDir, "checkout", "-b", branch, "upstream/"+mainBranch)
	writePolecatPruneTestFile(t, filepath.Join(localDir, "fork-only.txt"), "fork only\n")
	runGit(t, localDir, "add", "fork-only.txt")
	runGit(t, localDir, "commit", "-m", "fork-only branch work")
	branchSHA, err := repoGit.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev branch: %v", err)
	}
	runGit(t, localDir, "push", "origin", branch)

	runGit(t, localDir, "checkout", mainBranch)
	runGit(t, localDir, "reset", "--hard", "origin/"+mainBranch)
	writePolecatPruneTestFile(t, filepath.Join(localDir, "fork-advance.txt"), "fork advanced\n")
	runGit(t, localDir, "add", "fork-advance.txt")
	runGit(t, localDir, "commit", "-m", "advance fork target")
	runGit(t, localDir, "cherry-pick", strings.TrimSpace(branchSHA))
	runGit(t, localDir, "push", "origin", mainBranch)
	if err := repoGit.FetchPrune("origin"); err != nil {
		t.Fatalf("FetchPrune origin: %v", err)
	}
	if err := repoGit.FetchPrune("upstream"); err != nil {
		t.Fatalf("FetchPrune upstream: %v", err)
	}
	if got := repoGit.CleanDefaultBranchBaseRef("origin", mainBranch); got != "upstream/"+mainBranch {
		t.Fatalf("CleanDefaultBranchBaseRef = %q, want upstream/%s", got, mainBranch)
	}

	out := captureStdout(t, func() {
		pruned, err := pruneRemotePolecatBranches(repoGit, true)
		if err != nil {
			t.Fatalf("pruneRemotePolecatBranches: %v", err)
		}
		if pruned != 0 {
			t.Fatalf("pruned = %d, want 0 because branch is not preserved on upstream/%s", pruned, mainBranch)
		}
	})
	if strings.Contains(out, branch) {
		t.Fatalf("dry-run output %q should not include fork-only branch %s", out, branch)
	}
	exists, err := repoGit.RemoteBranchExists("origin", branch)
	if err != nil {
		t.Fatalf("RemoteBranchExists: %v", err)
	}
	if !exists {
		t.Fatal("fork-only branch should remain on origin")
	}
}

func createPatchEquivalentRemoteBranch(t *testing.T, repoGit *git.Git, localDir, mainBranch, branch string) {
	t.Helper()
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
}

func assertRemotePruneDryRunKeptBranch(t *testing.T, repoGit *git.Git, out, branch string) {
	t.Helper()
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
	localDir := filepath.Join(tmp, "local")
	return localDir, initPolecatPruneTestRepoAt(t, localDir)
}

func initPolecatPruneTestRepoAt(t *testing.T, localDir string) string {
	t.Helper()
	tmp := t.TempDir()
	remoteDir := filepath.Join(tmp, "remote.git")
	mainBranch := "main"

	if err := os.MkdirAll(filepath.Dir(localDir), 0755); err != nil {
		t.Fatalf("mkdir repo parent: %v", err)
	}
	runGit(t, tmp, "init", "--bare", "--initial-branch", mainBranch, remoteDir)
	runGit(t, tmp, "clone", remoteDir, localDir)
	runGit(t, localDir, "config", "user.email", "test@test.com")
	runGit(t, localDir, "config", "user.name", "Test User")
	writePolecatPruneTestFile(t, filepath.Join(localDir, "README.md"), "test\n")
	runGit(t, localDir, "add", "README.md")
	runGit(t, localDir, "commit", "-m", "initial")
	runGit(t, localDir, "push", "-u", "origin", mainBranch)

	return mainBranch
}

func initPolecatPruneOriginForkRepo(t *testing.T) (string, string) {
	t.Helper()
	tmp := t.TempDir()
	upstreamDir := filepath.Join(tmp, "upstream.git")
	forkDir := filepath.Join(tmp, "fork.git")
	localDir := filepath.Join(tmp, "local")
	mainBranch := "main"

	runGit(t, tmp, "init", "--bare", "--initial-branch", mainBranch, upstreamDir)
	runGit(t, tmp, "init", "--bare", "--initial-branch", mainBranch, forkDir)
	runGit(t, tmp, "clone", upstreamDir, localDir)
	runGit(t, localDir, "config", "user.email", "test@test.com")
	runGit(t, localDir, "config", "user.name", "Test User")
	writePolecatPruneTestFile(t, filepath.Join(localDir, "README.md"), "test\n")
	runGit(t, localDir, "add", "README.md")
	runGit(t, localDir, "commit", "-m", "initial")
	runGit(t, localDir, "push", "-u", "origin", mainBranch)
	runGit(t, localDir, "push", forkDir, mainBranch)
	runGit(t, localDir, "remote", "set-url", "origin", forkDir)
	runGit(t, localDir, "remote", "add", "upstream", upstreamDir)
	runGit(t, localDir, "fetch", "origin")
	runGit(t, localDir, "fetch", "upstream")

	return localDir, mainBranch
}

func writePolecatPruneTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
