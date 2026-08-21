package rig

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/git"
)

func TestRegisterRigBootstrapsCanonicalTopologyWithoutTouchingExistingWIP(t *testing.T) {
	root, rigsConfig := setupTestTown(t)
	manager := NewManager(root, rigsConfig, git.NewGit(root))
	remote := createTestGitRepoForRig(t, "adopt-source")

	rigName := "adopted"
	rigPath := filepath.Join(root, rigName)
	mayorPath := filepath.Join(rigPath, "mayor", "rig")
	polecatPath := filepath.Join(rigPath, "polecats", "worker", rigName)
	if err := os.MkdirAll(filepath.Dir(mayorPath), 0755); err != nil {
		t.Fatalf("create Mayor parent: %v", err)
	}
	runGitForCanonicalRepoTest(t, "clone", remote, mayorPath)
	if err := os.MkdirAll(filepath.Dir(polecatPath), 0755); err != nil {
		t.Fatalf("create polecat parent: %v", err)
	}
	runGitForCanonicalRepoTest(t, "-C", mayorPath, "worktree", "add", "-b", "polecat/worker", polecatPath)

	if err := os.WriteFile(filepath.Join(mayorPath, "README.md"), []byte("Mayor WIP\n"), 0644); err != nil {
		t.Fatalf("write Mayor WIP: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorPath, "mayor-untracked.txt"), []byte("keep Mayor bytes\n"), 0644); err != nil {
		t.Fatalf("write Mayor untracked WIP: %v", err)
	}
	if err := os.WriteFile(filepath.Join(polecatPath, "README.md"), []byte("Polecat WIP\n"), 0644); err != nil {
		t.Fatalf("write polecat WIP: %v", err)
	}
	if err := os.WriteFile(filepath.Join(polecatPath, "polecat-untracked.txt"), []byte("keep polecat bytes\n"), 0644); err != nil {
		t.Fatalf("write polecat untracked WIP: %v", err)
	}

	if err := manager.saveRigConfig(rigPath, &RigConfig{
		Type:          "rig",
		Version:       CurrentRigConfigVersion,
		Name:          rigName,
		GitURL:        remote,
		LocalRepo:     mayorPath,
		DefaultBranch: "main",
		CreatedAt:     time.Now(),
		Beads:         &BeadsConfig{Prefix: "adp"},
	}); err != nil {
		t.Fatalf("write adopted config: %v", err)
	}

	before := snapshotCanonicalRepoTestTrees(t, map[string]string{
		"mayor":   mayorPath,
		"polecat": polecatPath,
	})

	result, err := manager.RegisterRig(RegisterRigOptions{Name: rigName})
	if err != nil {
		t.Fatalf("RegisterRig: %v", err)
	}
	if !result.FromConfig {
		t.Fatal("RegisterRig did not use the existing adopted-rig config")
	}
	if result.DefaultBranch != "main" {
		t.Fatalf("default branch = %q, want main", result.DefaultBranch)
	}

	assertCanonicalRepoTopology(t, rigPath, remote, "main")
	after := snapshotCanonicalRepoTestTrees(t, map[string]string{
		"mayor":   mayorPath,
		"polecat": polecatPath,
	})
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("canonical bootstrap changed Mayor or polecat WIP\nbefore: %#v\nafter:  %#v", before, after)
	}

	second, err := EnsureCanonicalRepoTopology(rigPath)
	if err != nil {
		t.Fatalf("second EnsureCanonicalRepoTopology: %v", err)
	}
	if second.BareRepoCreated || second.RefineryWorktreeCreated {
		t.Fatalf("idempotent ensure reported new topology: %+v", second)
	}
	if got := snapshotCanonicalRepoTestTrees(t, map[string]string{
		"mayor":   mayorPath,
		"polecat": polecatPath,
	}); !reflect.DeepEqual(before, got) {
		t.Fatalf("idempotent ensure changed Mayor or polecat WIP\nbefore: %#v\nafter:  %#v", before, got)
	}
}

func TestEnsureCanonicalRepoTopologyRestoresMissingRefineryWorktree(t *testing.T) {
	remote := createTestGitRepoForRig(t, "refinery-source")
	rigPath := filepath.Join(t.TempDir(), "repair")
	manager := NewManager(filepath.Dir(rigPath), nil, git.NewGit(filepath.Dir(rigPath)))
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("create rig: %v", err)
	}
	if err := manager.saveRigConfig(rigPath, &RigConfig{
		Type:          "rig",
		Version:       CurrentRigConfigVersion,
		Name:          "repair",
		GitURL:        remote,
		DefaultBranch: "main",
		CreatedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("write rig config: %v", err)
	}

	barePath := filepath.Join(rigPath, ".repo.git")
	if err := git.NewGit(rigPath).CloneBareWithBranch(remote, barePath, "main"); err != nil {
		t.Fatalf("create existing bare repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(remote, "after-bare-clone.txt"), []byte("advance integration branch\n"), 0644); err != nil {
		t.Fatalf("write integration advance: %v", err)
	}
	runGitForCanonicalRepoTest(t, "-C", remote, "add", "after-bare-clone.txt")
	runGitForCanonicalRepoTest(t, "-C", remote, "commit", "-m", "advance integration branch")
	advancedCommit := gitOutputForCanonicalRepoTest(t, "-C", remote, "rev-parse", "HEAD")

	result, err := EnsureCanonicalRepoTopology(rigPath)
	if err != nil {
		t.Fatalf("EnsureCanonicalRepoTopology: %v", err)
	}
	if result.BareRepoCreated {
		t.Fatal("existing bare repository was reported as newly created")
	}
	if !result.RefineryWorktreeCreated {
		t.Fatal("missing refinery worktree was not reported as restored")
	}
	assertCanonicalRepoTopology(t, rigPath, remote, "main")
	if got := gitOutputForCanonicalRepoTest(t, "-C", filepath.Join(rigPath, "refinery", "rig"), "rev-parse", "HEAD"); got != advancedCommit {
		t.Fatalf("restored refinery HEAD = %q, want advanced integration commit %q", got, advancedCommit)
	}
}

func TestEnsureCanonicalRepoTopologyRetriesAfterForwardSubmoduleRepair(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "always")

	remote, parentWorktree, submoduleWorktree, pinnedCommit, publishedCommit := createCanonicalRepoWithUnpublishedSubmodule(t)
	rigPath := filepath.Join(t.TempDir(), "submodule-retry")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("create rig: %v", err)
	}
	if err := saveRigConfigFile(rigPath, &RigConfig{
		Type:          "rig",
		Version:       CurrentRigConfigVersion,
		Name:          "submodule_retry",
		GitURL:        remote,
		DefaultBranch: "main",
		CreatedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("write rig config: %v", err)
	}

	if _, err := EnsureCanonicalRepoTopology(rigPath); err == nil {
		t.Fatal("first ensure succeeded with an unpublished pinned submodule commit")
	} else if !strings.Contains(err.Error(), "initializing submodules") {
		t.Fatalf("first ensure error = %q, want submodule initialization failure", err)
	}
	if err := git.NewGitWithDir(filepath.Join(rigPath, ".repo.git"), "").ValidateBareRepository(); err != nil {
		t.Fatalf("first ensure did not leave a valid retryable bare repo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rigPath, "refinery", "rig", ".git")); err != nil {
		t.Fatalf("first ensure did not leave a retryable refinery worktree: %v", err)
	}

	if _, err := EnsureCanonicalRepoTopology(rigPath); err == nil {
		t.Fatal("retry accepted a refinery worktree with incomplete submodules")
	} else if !strings.Contains(err.Error(), "initializing canonical refinery submodules") {
		t.Fatalf("retry error = %q, want canonical submodule failure", err)
	}

	runGitForCanonicalRepoTest(t, "-C", submoduleWorktree, "checkout", "--detach", publishedCommit)
	runGitForCanonicalRepoTest(t, "-C", parentWorktree, "add", "libs/sub")
	runGitForCanonicalRepoTest(t, "-C", parentWorktree, "commit", "-m", "repair dangling submodule pin")
	runGitForCanonicalRepoTest(t, "-C", parentWorktree, "push", "origin", "main")
	repairedParentCommit := gitOutputForCanonicalRepoTest(t, "-C", parentWorktree, "rev-parse", "HEAD")

	result, err := EnsureCanonicalRepoTopology(rigPath)
	if err != nil {
		t.Fatalf("ensure after forward parent repair: %v", err)
	}
	if result.BareRepoCreated || result.RefineryWorktreeCreated {
		t.Fatalf("retry recreated existing canonical topology: %+v", result)
	}
	gotParentCommit := gitOutputForCanonicalRepoTest(t, "-C", filepath.Join(rigPath, "refinery", "rig"), "rev-parse", "HEAD")
	if gotParentCommit != repairedParentCommit {
		t.Fatalf("refinery HEAD = %q, want repaired parent commit %q", gotParentCommit, repairedParentCommit)
	}
	gotCommit := gitOutputForCanonicalRepoTest(t, "-C", filepath.Join(rigPath, "refinery", "rig", "libs", "sub"), "rev-parse", "HEAD")
	if gotCommit != publishedCommit {
		t.Fatalf("submodule HEAD = %q, want repaired published commit %q (dangling pin was %q)", gotCommit, publishedCommit, pinnedCommit)
	}
}

func assertCanonicalRepoTopology(t *testing.T, rigPath, remote, branch string) {
	t.Helper()
	barePath := filepath.Join(rigPath, ".repo.git")
	bareGit := git.NewGitWithDir(barePath, "")
	if err := bareGit.ValidateBareRepository(); err != nil {
		t.Fatalf("bare repo invalid: %v", err)
	}
	if got, err := bareGit.RemoteURL("origin"); err != nil || got != remote {
		t.Fatalf("origin = %q, %v; want %q", got, err, remote)
	}
	if got, err := bareGit.ConfigGet("remote.origin.fetch"); err != nil || got != CanonicalBareFetchRefspec {
		t.Fatalf("origin refspec = %q, %v; want %q", got, err, CanonicalBareFetchRefspec)
	}
	if exists, err := bareGit.RemoteTrackingBranchExists("origin", branch); err != nil || !exists {
		t.Fatalf("origin/%s exists = %v, %v", branch, exists, err)
	}

	refineryPath := filepath.Join(rigPath, "refinery", "rig")
	gitInfo, err := os.Stat(filepath.Join(refineryPath, ".git"))
	if err != nil {
		t.Fatalf("refinery worktree .git: %v", err)
	}
	if !gitInfo.Mode().IsRegular() {
		t.Fatal("refinery/rig .git is not a worktree link")
	}
	if got, err := git.NewGit(refineryPath).CurrentBranch(); err != nil || got != branch {
		t.Fatalf("refinery branch = %q, %v; want %q", got, err, branch)
	}
}

func snapshotCanonicalRepoTestTrees(t *testing.T, roots map[string]string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	for label, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			key := filepath.Join(label, rel)
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if entry.IsDir() {
				snapshot[key] = "dir:" + info.Mode().String()
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				snapshot[key] = "symlink:" + target
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			hash := sha256.Sum256(data)
			snapshot[key] = fmt.Sprintf("%s:%x", info.Mode(), hash)
			return nil
		})
		if err != nil {
			t.Fatalf("snapshot %s: %v", label, err)
		}
	}
	return snapshot
}

func runGitForCanonicalRepoTest(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitOutputForCanonicalRepoTest(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func createCanonicalRepoWithUnpublishedSubmodule(t *testing.T) (string, string, string, string, string) {
	t.Helper()
	root := t.TempDir()
	submoduleRemote := filepath.Join(root, "submodule.git")
	submoduleWorktree := filepath.Join(root, "submodule-work")
	parentRemote := filepath.Join(root, "parent.git")
	parentWorktree := filepath.Join(root, "parent-work")

	runGitForCanonicalRepoTest(t, "init", "--bare", "--initial-branch=main", submoduleRemote)
	runGitForCanonicalRepoTest(t, "init", "--initial-branch=main", submoduleWorktree)
	runGitForCanonicalRepoTest(t, "-C", submoduleWorktree, "config", "user.email", "canonical@example.com")
	runGitForCanonicalRepoTest(t, "-C", submoduleWorktree, "config", "user.name", "Canonical Test")
	if err := os.WriteFile(filepath.Join(submoduleWorktree, "lib.txt"), []byte("published\n"), 0644); err != nil {
		t.Fatalf("write published submodule file: %v", err)
	}
	runGitForCanonicalRepoTest(t, "-C", submoduleWorktree, "add", "lib.txt")
	runGitForCanonicalRepoTest(t, "-C", submoduleWorktree, "commit", "-m", "published")
	publishedCommit := gitOutputForCanonicalRepoTest(t, "-C", submoduleWorktree, "rev-parse", "HEAD")
	runGitForCanonicalRepoTest(t, "-C", submoduleWorktree, "remote", "add", "origin", submoduleRemote)
	runGitForCanonicalRepoTest(t, "-C", submoduleWorktree, "push", "-u", "origin", "main")

	runGitForCanonicalRepoTest(t, "init", "--bare", "--initial-branch=main", parentRemote)
	runGitForCanonicalRepoTest(t, "init", "--initial-branch=main", parentWorktree)
	runGitForCanonicalRepoTest(t, "-C", parentWorktree, "config", "user.email", "canonical@example.com")
	runGitForCanonicalRepoTest(t, "-C", parentWorktree, "config", "user.name", "Canonical Test")
	if err := os.WriteFile(filepath.Join(parentWorktree, "README.md"), []byte("parent\n"), 0644); err != nil {
		t.Fatalf("write parent file: %v", err)
	}
	runGitForCanonicalRepoTest(t, "-C", parentWorktree, "add", "README.md")
	runGitForCanonicalRepoTest(t, "-C", parentWorktree, "commit", "-m", "parent")
	runGitForCanonicalRepoTest(t, "-c", "protocol.file.allow=always", "-C", parentWorktree, "submodule", "add", submoduleRemote, "libs/sub")
	runGitForCanonicalRepoTest(t, "-C", parentWorktree, "commit", "-am", "add submodule")
	runGitForCanonicalRepoTest(t, "-C", parentWorktree, "remote", "add", "origin", parentRemote)
	runGitForCanonicalRepoTest(t, "-C", parentWorktree, "push", "-u", "origin", "main")

	submoduleCheckout := filepath.Join(parentWorktree, "libs", "sub")
	runGitForCanonicalRepoTest(t, "-C", submoduleCheckout, "config", "user.email", "canonical@example.com")
	runGitForCanonicalRepoTest(t, "-C", submoduleCheckout, "config", "user.name", "Canonical Test")
	if err := os.WriteFile(filepath.Join(submoduleCheckout, "unpublished.txt"), []byte("unpublished\n"), 0644); err != nil {
		t.Fatalf("write unpublished submodule file: %v", err)
	}
	runGitForCanonicalRepoTest(t, "-C", submoduleCheckout, "add", "unpublished.txt")
	runGitForCanonicalRepoTest(t, "-C", submoduleCheckout, "commit", "-m", "unpublished")
	pinnedCommit := gitOutputForCanonicalRepoTest(t, "-C", submoduleCheckout, "rev-parse", "HEAD")
	runGitForCanonicalRepoTest(t, "-C", parentWorktree, "add", "libs/sub")
	runGitForCanonicalRepoTest(t, "-C", parentWorktree, "commit", "-m", "pin unpublished submodule")
	runGitForCanonicalRepoTest(t, "-C", parentWorktree, "push", "origin", "main")

	return parentRemote, parentWorktree, submoduleCheckout, pinnedCommit, publishedCommit
}
