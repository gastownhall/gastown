package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Initialize repo
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	// Configure user for commits
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = dir
	_ = cmd.Run()

	// Create initial commit
	testFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(testFile, []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = dir
	_ = cmd.Run()

	return dir
}

type townRootSafetySnapshot struct {
	Head   string
	Branch string
	Files  map[string]string
}

func initTownRootSafetyRepo(t *testing.T) string {
	t.Helper()

	root := initTestRepo(t)
	g := NewGit(root)
	cmd := exec.Command("git", "branch", "polecat/safety")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create safety branch: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("committed\n"), 0644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	if err := g.Add("tracked.txt"); err != nil {
		t.Fatalf("git add tracked: %v", err)
	}
	if err := g.Commit("add tracked file"); err != nil {
		t.Fatalf("git commit tracked: %v", err)
	}

	writeTownSafetyFile(t, root, "mayor/town.json", `{"name":"test-town"}\n`)
	writeTownSafetyFile(t, root, "mayor/rigs.json", `{"rigs":[]}\n`)
	writeTownSafetyFile(t, root, ".dolt-data/gastown/.dolt/noms/manifest", "manifest sentinel\n")
	writeTownSafetyFile(t, root, ".runtime/sentinel", "runtime sentinel\n")
	writeTownSafetyFile(t, root, ".beads/metadata.json", `{"prefix":"hq"}\n`)
	writeTownSafetyFile(t, root, "daemon/daemon.pid", "12345\n")
	writeTownSafetyFile(t, root, "user-work.txt", "untracked user work\n")
	writeTownSafetyFile(t, root, "tracked.txt", "dirty tracked work\n")

	return root
}

func writeTownSafetyFile(t *testing.T, root, rel, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func snapshotTownRootSafety(t *testing.T, root string) townRootSafetySnapshot {
	t.Helper()
	g := NewGit(root)
	head, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("rev HEAD: %v", err)
	}
	branch, err := g.CurrentBranch()
	if err != nil {
		t.Fatalf("current branch: %v", err)
	}
	s := townRootSafetySnapshot{
		Head:   head,
		Branch: branch,
		Files:  make(map[string]string),
	}
	for _, rel := range townRootSafetyFiles() {
		path := filepath.Join(root, filepath.FromSlash(rel))
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		s.Files[rel] = string(contents)
	}
	return s
}

func townRootSafetyFiles() []string {
	return []string{
		"mayor/town.json",
		"mayor/rigs.json",
		".dolt-data/gastown/.dolt/noms/manifest",
		".runtime/sentinel",
		".beads/metadata.json",
		"daemon/daemon.pid",
		"user-work.txt",
		"tracked.txt",
	}
}

func assertTownRootSafetyPreserved(t *testing.T, root string, before townRootSafetySnapshot) {
	t.Helper()
	after := snapshotTownRootSafety(t, root)
	if after.Head != before.Head {
		t.Fatalf("HEAD changed: got %s, want %s", after.Head, before.Head)
	}
	if after.Branch != before.Branch {
		t.Fatalf("branch changed: got %s, want %s", after.Branch, before.Branch)
	}
	for rel, want := range before.Files {
		if got := after.Files[rel]; got != want {
			t.Fatalf("%s changed: got %q, want %q", rel, got, want)
		}
	}
}

func requireTownRootSafetyError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrUnsafeTownRootGitMutation) {
		t.Fatalf("error = %v, want ErrUnsafeTownRootGitMutation", err)
	}
}

func TestTownRootMutatingGitCommandsAreBlocked(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Git) error
	}{
		{name: "checkout", run: func(g *Git) error { return g.Checkout("polecat/safety") }},
		{name: "checkout new branch", run: func(g *Git) error { return g.CheckoutNewBranch("polecat/new", "polecat/safety") }},
		{name: "checkout reset branch", run: func(g *Git) error { return g.CheckoutResetBranch("polecat/reset", "polecat/safety") }},
		{name: "reset hard", run: func(g *Git) error { return g.ResetHard("polecat/safety") }},
		{name: "clean force", run: func(g *Git) error { return g.CleanForce() }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := initTownRootSafetyRepo(t)
			before := snapshotTownRootSafety(t, root)
			err := tt.run(NewGit(root))
			requireTownRootSafetyError(t, err)
			assertTownRootSafetyPreserved(t, root, before)
		})
	}
}

func TestTownRootReadOnlyStashListIsAllowed(t *testing.T) {
	root := initTownRootSafetyRepo(t)
	count, err := NewGit(root).StashCount()
	if err != nil {
		t.Fatalf("stash count should be allowed: %v", err)
	}
	if count != 0 {
		t.Fatalf("stash count = %d, want 0", count)
	}
}

func TestNestedWorkDirResolvingToTownRootGitIsBlocked(t *testing.T) {
	root := initTownRootSafetyRepo(t)
	rigDir := filepath.Join(root, "gastown")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatalf("mkdir rig dir: %v", err)
	}
	cmd := exec.Command("git", "-C", rigDir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("raw git top-level: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != root {
		t.Fatalf("raw git top-level = %q, want %q", got, root)
	}

	before := snapshotTownRootSafety(t, root)
	for _, tt := range []struct {
		name string
		run  func(*Git) error
	}{
		{name: "checkout", run: func(g *Git) error { return g.Checkout("polecat/safety") }},
		{name: "reset hard", run: func(g *Git) error { return g.ResetHard("polecat/safety") }},
		{name: "clean force", run: func(g *Git) error { return g.CleanForce() }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err = tt.run(NewGit(rigDir))
			requireTownRootSafetyError(t, err)
			assertTownRootSafetyPreserved(t, root, before)
		})
	}
}

func TestWorktreeAddCannotTargetTownRootRuntimePaths(t *testing.T) {
	root := initTownRootSafetyRepo(t)
	before := snapshotTownRootSafety(t, root)

	src := initTestRepo(t)
	bareDir := filepath.Join(t.TempDir(), ".repo.git")
	cmd := exec.Command("git", "clone", "--bare", src, bareDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone bare: %v\n%s", err, out)
	}

	for i, target := range []string{
		root,
		filepath.Join(root, "mayor", "bad-worktree"),
		filepath.Join(root, ".dolt-data", "bad-worktree"),
		filepath.Join(root, ".runtime", "bad-worktree"),
		filepath.Join(root, ".beads", "bad-worktree"),
		filepath.Join(root, "daemon", "bad-worktree"),
	} {
		t.Run(filepath.Base(filepath.Dir(target))+"/"+filepath.Base(target), func(t *testing.T) {
			err := NewGitWithDir(bareDir, "").WorktreeAddFromRef(target, fmt.Sprintf("polecat/town-root-%d", i), "HEAD")
			requireTownRootSafetyError(t, err)
			assertTownRootSafetyPreserved(t, root, before)
		})
	}

	link := filepath.Join(t.TempDir(), "townlink")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	err := NewGitWithDir(bareDir, "").WorktreeAddFromRef(filepath.Join(link, ".runtime", "linked-worktree"), "polecat/town-root-symlink", "HEAD")
	requireTownRootSafetyError(t, err)
	assertTownRootSafetyPreserved(t, root, before)
}

func TestCloneCannotTargetTownRootRuntimePaths(t *testing.T) {
	root := initTownRootSafetyRepo(t)
	before := snapshotTownRootSafety(t, root)
	src := initTestRepo(t)

	err := NewGit(t.TempDir()).Clone(src, root)
	requireTownRootSafetyError(t, err)
	assertTownRootSafetyPreserved(t, root, before)

	link := filepath.Join(t.TempDir(), "townlink")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	err = NewGit(t.TempDir()).Clone(src, filepath.Join(link, ".dolt-data", "clone"))
	requireTownRootSafetyError(t, err)
	assertTownRootSafetyPreserved(t, root, before)

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir town root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	err = NewGit(t.TempDir()).Clone(src, filepath.Join(".runtime", "relative-clone"))
	requireTownRootSafetyError(t, err)
	assertTownRootSafetyPreserved(t, root, before)
}

func TestIsRepo(t *testing.T) {
	dir := t.TempDir()
	g := NewGit(dir)

	if g.IsRepo() {
		t.Fatal("expected IsRepo to be false for empty dir")
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	if !g.IsRepo() {
		t.Fatal("expected IsRepo to be true after git init")
	}
}

func TestCloneWithReferenceCreatesAlternates(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")

	if err := exec.Command("git", "init", src).Run(); err != nil {
		t.Fatalf("init src: %v", err)
	}
	_ = exec.Command("git", "-C", src, "config", "user.email", "test@test.com").Run()
	_ = exec.Command("git", "-C", src, "config", "user.name", "Test User").Run()

	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	_ = exec.Command("git", "-C", src, "add", ".").Run()
	_ = exec.Command("git", "-C", src, "commit", "-m", "initial").Run()

	g := NewGit(tmp)
	if err := g.CloneWithReference(src, dst, src); err != nil {
		t.Fatalf("CloneWithReference: %v", err)
	}

	alternates := filepath.Join(dst, ".git", "objects", "info", "alternates")
	if _, err := os.Stat(alternates); err != nil {
		t.Fatalf("expected alternates file: %v", err)
	}
}

func TestCloneWithReferencePreservesSymlinks(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")

	// Create test repo with symlink
	if err := exec.Command("git", "init", src).Run(); err != nil {
		t.Fatalf("init src: %v", err)
	}
	_ = exec.Command("git", "-C", src, "config", "user.email", "test@test.com").Run()
	_ = exec.Command("git", "-C", src, "config", "user.name", "Test User").Run()

	// Create a directory and a symlink to it
	targetDir := filepath.Join(src, "target")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "file.txt"), []byte("content\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	linkPath := filepath.Join(src, "link")
	if err := os.Symlink("target", linkPath); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	_ = exec.Command("git", "-C", src, "add", ".").Run()
	_ = exec.Command("git", "-C", src, "commit", "-m", "initial").Run()

	// Clone with reference
	g := NewGit(tmp)
	if err := g.CloneWithReference(src, dst, src); err != nil {
		t.Fatalf("CloneWithReference: %v", err)
	}

	// Verify symlink was preserved
	dstLink := filepath.Join(dst, "link")
	info, err := os.Lstat(dstLink)
	if err != nil {
		t.Fatalf("lstat link: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected %s to be a symlink, got mode %v", dstLink, info.Mode())
	}

	// Verify symlink target is correct
	target, err := os.Readlink(dstLink)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != "target" {
		t.Errorf("expected symlink target 'target', got %q", target)
	}
}

func TestCurrentBranch(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	branch, err := g.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}

	// Modern git uses "main", older uses "master"
	if branch != "main" && branch != "master" {
		t.Errorf("branch = %q, want main or master", branch)
	}
}

func TestStatus(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Should be clean initially
	status, err := g.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Clean {
		t.Error("expected clean status")
	}

	// Add an untracked file
	testFile := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(testFile, []byte("new"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	status, err = g.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Clean {
		t.Error("expected dirty status")
	}
	if len(status.Untracked) != 1 {
		t.Errorf("untracked = %d, want 1", len(status.Untracked))
	}
}

func TestAddAndCommit(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Create a new file
	testFile := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(testFile, []byte("new content"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Add and commit
	if err := g.Add("new.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("add new file"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Should be clean
	status, err := g.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Clean {
		t.Error("expected clean after commit")
	}
}

func TestHasUncommittedChanges(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	has, err := g.HasUncommittedChanges()
	if err != nil {
		t.Fatalf("HasUncommittedChanges: %v", err)
	}
	if has {
		t.Error("expected no changes initially")
	}

	// Modify a file
	testFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(testFile, []byte("modified"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	has, err = g.HasUncommittedChanges()
	if err != nil {
		t.Fatalf("HasUncommittedChanges: %v", err)
	}
	if !has {
		t.Error("expected changes after modify")
	}
}

func TestCheckout(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Create a new branch
	if err := g.CreateBranch("feature"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	// Checkout the new branch
	if err := g.Checkout("feature"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	branch, _ := g.CurrentBranch()
	if branch != "feature" {
		t.Errorf("branch = %q, want feature", branch)
	}
}

func TestCheckoutDetachAllowsBranchCheckedOutInAnotherWorktree(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	mainBranch, err := g.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	mainSHA, err := g.Rev(mainBranch)
	if err != nil {
		t.Fatalf("Rev %s: %v", mainBranch, err)
	}

	worktreePath := filepath.Join(t.TempDir(), "worker")
	runGit(t, dir, "worktree", "add", "-b", "polecat/test-detach", worktreePath, "HEAD")
	workerGit := NewGit(worktreePath)

	if err := os.WriteFile(filepath.Join(dir, "after-worker.txt"), []byte("new main commit\n"), 0644); err != nil {
		t.Fatalf("write after-worker file: %v", err)
	}
	runGit(t, dir, "add", "after-worker.txt")
	runGit(t, dir, "commit", "-m", "advance main")
	mainSHA, err = g.Rev(mainBranch)
	if err != nil {
		t.Fatalf("Rev advanced %s: %v", mainBranch, err)
	}

	if err := workerGit.Checkout(mainBranch); err == nil {
		t.Fatalf("Checkout(%s) succeeded, expected branch-in-use failure", mainBranch)
	}
	if err := workerGit.CheckoutDetach(mainBranch); err != nil {
		t.Fatalf("CheckoutDetach(%s): %v", mainBranch, err)
	}

	branch, err := workerGit.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch after detach: %v", err)
	}
	if branch != "HEAD" {
		t.Fatalf("branch after detach = %q, want HEAD", branch)
	}
	headSHA, err := workerGit.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev HEAD after detach: %v", err)
	}
	if headSHA != mainSHA {
		t.Fatalf("detached HEAD = %q, want %q", headSHA, mainSHA)
	}
}

func TestCheckoutNewBranch(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Get current HEAD ref
	head, err := g.run("rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}

	// Create and checkout a new branch from HEAD
	if err := g.CheckoutNewBranch("feature-new", strings.TrimSpace(head)); err != nil {
		t.Fatalf("CheckoutNewBranch: %v", err)
	}

	branch, _ := g.CurrentBranch()
	if branch != "feature-new" {
		t.Errorf("branch = %q, want feature-new", branch)
	}

	// Verify it fails if branch already exists
	if err := g.Checkout("main"); err != nil {
		// Try master for older git
		if err := g.Checkout("master"); err != nil {
			t.Fatalf("Checkout main/master: %v", err)
		}
	}
	err = g.CheckoutNewBranch("feature-new", "HEAD")
	if err == nil {
		t.Error("expected error creating duplicate branch, got nil")
	}
}

func TestNotARepo(t *testing.T) {
	dir := t.TempDir() // Empty dir, not a git repo
	g := NewGit(dir)

	_, err := g.CurrentBranch()
	// ZFC: Check for GitError with raw stderr for agent observation.
	// Agents decide what "not a git repository" means, not Go code.
	gitErr, ok := err.(*GitError)
	if !ok {
		t.Errorf("expected GitError, got %T: %v", err, err)
		return
	}
	// Verify raw stderr is available for agent observation
	if gitErr.Stderr == "" {
		t.Errorf("expected GitError with Stderr, got empty stderr")
	}
}

func TestRev(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	hash, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev: %v", err)
	}

	// Should be a 40-char hex string
	if len(hash) != 40 {
		t.Errorf("hash length = %d, want 40", len(hash))
	}
}

func TestFetchBranch(t *testing.T) {
	// Create a "remote" repo
	remoteDir := t.TempDir()
	cmd := exec.Command("git", "init", "--bare")
	cmd.Dir = remoteDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init --bare: %v", err)
	}

	// Create a local repo and push to remote
	localDir := initTestRepo(t)
	g := NewGit(localDir)

	// Add remote
	cmd = exec.Command("git", "remote", "add", "origin", remoteDir)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git remote add: %v", err)
	}

	// Push main branch
	mainBranch, _ := g.CurrentBranch()
	cmd = exec.Command("git", "push", "-u", "origin", mainBranch)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git push: %v", err)
	}

	// Fetch should succeed
	if err := g.FetchBranch("origin", mainBranch); err != nil {
		t.Errorf("FetchBranch: %v", err)
	}
}

func TestCheckConflicts_NoConflict(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)
	mainBranch, _ := g.CurrentBranch()

	// Create feature branch with non-conflicting change
	if err := g.CreateBranch("feature"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("feature"); err != nil {
		t.Fatalf("Checkout feature: %v", err)
	}

	// Add a new file (won't conflict with main)
	newFile := filepath.Join(dir, "feature.txt")
	if err := os.WriteFile(newFile, []byte("feature content"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := g.Add("feature.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("add feature file"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Go back to main
	if err := g.Checkout(mainBranch); err != nil {
		t.Fatalf("Checkout main: %v", err)
	}

	// Check for conflicts - should be none
	conflicts, err := g.CheckConflicts("feature", mainBranch)
	if err != nil {
		t.Fatalf("CheckConflicts: %v", err)
	}
	if len(conflicts) > 0 {
		t.Errorf("expected no conflicts, got %v", conflicts)
	}

	// Verify we're still on main and clean
	branch, _ := g.CurrentBranch()
	if branch != mainBranch {
		t.Errorf("branch = %q, want %q", branch, mainBranch)
	}
	status, _ := g.Status()
	if !status.Clean {
		t.Error("expected clean working directory after CheckConflicts")
	}
}

func TestCheckConflicts_WithConflict(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)
	mainBranch, _ := g.CurrentBranch()

	// Create feature branch
	if err := g.CreateBranch("feature"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("feature"); err != nil {
		t.Fatalf("Checkout feature: %v", err)
	}

	// Modify README.md on feature branch
	readmeFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readmeFile, []byte("# Feature changes\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := g.Add("README.md"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("modify readme on feature"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Go back to main and make conflicting change
	if err := g.Checkout(mainBranch); err != nil {
		t.Fatalf("Checkout main: %v", err)
	}
	if err := os.WriteFile(readmeFile, []byte("# Main changes\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := g.Add("README.md"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("modify readme on main"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Check for conflicts - should find README.md
	conflicts, err := g.CheckConflicts("feature", mainBranch)
	if err != nil {
		t.Fatalf("CheckConflicts: %v", err)
	}
	if len(conflicts) == 0 {
		t.Error("expected conflicts, got none")
	}

	foundReadme := false
	for _, f := range conflicts {
		if f == "README.md" {
			foundReadme = true
			break
		}
	}
	if !foundReadme {
		t.Errorf("expected README.md in conflicts, got %v", conflicts)
	}

	// Verify we're still on main and clean
	branch, _ := g.CurrentBranch()
	if branch != mainBranch {
		t.Errorf("branch = %q, want %q", branch, mainBranch)
	}
	status, _ := g.Status()
	if !status.Clean {
		t.Error("expected clean working directory after CheckConflicts")
	}
}

// TestCloneBareHasOriginRefs verifies that after CloneBare, origin/* refs
// are available for worktree creation. This was broken before the fix:
// bare clones had refspec configured but no fetch was run, so origin/main
// didn't exist and WorktreeAddFromRef("origin/main") failed.
//
// Related: GitHub issue #286
func TestCloneBareHasOriginRefs(t *testing.T) {
	tmp := t.TempDir()

	// Create a "remote" repo with a commit on main
	remoteDir := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remoteDir, 0755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = remoteDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = remoteDir
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = remoteDir
	_ = cmd.Run()

	// Create initial commit
	readmeFile := filepath.Join(remoteDir, "README.md")
	if err := os.WriteFile(readmeFile, []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = remoteDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = remoteDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// Get the main branch name (main or master depending on git version)
	cmd = exec.Command("git", "branch", "--show-current")
	cmd.Dir = remoteDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git branch --show-current: %v", err)
	}
	mainBranch := strings.TrimSpace(string(out))

	// Clone as bare repo using our CloneBare function
	bareDir := filepath.Join(tmp, "bare.git")
	g := NewGit(tmp)
	if err := g.CloneBare(remoteDir, bareDir); err != nil {
		t.Fatalf("CloneBare: %v", err)
	}

	// Verify origin/main exists (this was the bug - it didn't exist before the fix)
	bareGit := NewGitWithDir(bareDir, "")
	cmd = exec.Command("git", "--git-dir", bareDir, "branch", "-r")
	out, err = cmd.Output()
	if err != nil {
		t.Fatalf("git branch -r: %v", err)
	}

	originMain := "origin/" + mainBranch
	if !stringContains(string(out), originMain) {
		t.Errorf("expected %q in remote branches, got: %s", originMain, out)
	}

	// Verify WorktreeAddFromRef succeeds with origin/main
	// This is what polecat creation does
	worktreePath := filepath.Join(tmp, "worktree")
	if err := bareGit.WorktreeAddFromRef(worktreePath, "test-branch", originMain); err != nil {
		t.Errorf("WorktreeAddFromRef(%q) failed: %v", originMain, err)
	}

	// Verify the worktree was created and has the expected file
	worktreeReadme := filepath.Join(worktreePath, "README.md")
	if _, err := os.Stat(worktreeReadme); err != nil {
		t.Errorf("expected README.md in worktree: %v", err)
	}
}

func TestCloneBareEmptyRepoSkipsMissingHeadFetch(t *testing.T) {
	tmp := t.TempDir()
	remoteDir := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remoteDir, 0755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = remoteDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	bareDir := filepath.Join(tmp, "bare.git")
	g := NewGit(tmp)
	if err := g.CloneBare(remoteDir, bareDir); err != nil {
		t.Fatalf("CloneBare empty repo: %v", err)
	}

	bareGit := NewGitWithDir(bareDir, "")
	empty, err := bareGit.IsEmpty()
	if err != nil {
		t.Fatalf("IsEmpty: %v", err)
	}
	if !empty {
		t.Error("expected bare clone of empty repo to be empty")
	}
}

func TestIsEmpty_EmptyRepo(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	g := NewGit(dir)
	empty, err := g.IsEmpty()
	if err != nil {
		t.Fatalf("IsEmpty: %v", err)
	}
	if !empty {
		t.Error("expected newly-initialized repo to be empty")
	}
}

func TestIsEmpty_RepoWithCommit(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	empty, err := g.IsEmpty()
	if err != nil {
		t.Fatalf("IsEmpty: %v", err)
	}
	if empty {
		t.Error("expected repo with commits to not be empty")
	}
}

func TestRefExists_ValidRef(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	// HEAD should exist
	exists, err := g.RefExists("HEAD")
	if err != nil {
		t.Fatalf("RefExists(HEAD): %v", err)
	}
	if !exists {
		t.Error("expected HEAD to exist")
	}
}

func TestRefExists_InvalidRef(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	// A ref that doesn't exist
	exists, err := g.RefExists("refs/heads/nonexistent-branch")
	if err != nil {
		t.Fatalf("RefExists: %v", err)
	}
	if exists {
		t.Error("expected nonexistent ref to not exist")
	}
}

func TestRefExists_OriginRef(t *testing.T) {
	tmp := t.TempDir()

	// Create a remote repo
	remoteDir := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remoteDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = remoteDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = remoteDir
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = remoteDir
	_ = cmd.Run()
	if err := os.WriteFile(filepath.Join(remoteDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = remoteDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = remoteDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// Get main branch name
	cmd = exec.Command("git", "branch", "--show-current")
	cmd.Dir = remoteDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git branch: %v", err)
	}
	mainBranch := strings.TrimSpace(string(out))

	// Clone bare
	bareDir := filepath.Join(tmp, "bare.git")
	g := NewGit(tmp)
	if err := g.CloneBare(remoteDir, bareDir); err != nil {
		t.Fatalf("CloneBare: %v", err)
	}

	bareGit := NewGitWithDir(bareDir, "")

	// origin/<main> should exist
	exists, err := bareGit.RefExists("origin/" + mainBranch)
	if err != nil {
		t.Fatalf("RefExists(origin/%s): %v", mainBranch, err)
	}
	if !exists {
		t.Errorf("expected origin/%s to exist", mainBranch)
	}

	// origin/nonexistent should not exist
	exists, err = bareGit.RefExists("origin/nonexistent")
	if err != nil {
		t.Fatalf("RefExists(origin/nonexistent): %v", err)
	}
	if exists {
		t.Error("expected origin/nonexistent to not exist")
	}
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// initTestRepoWithRemote sets up a local repo with a bare remote and initial push.
// Returns (localDir, remoteDir, mainBranch).
func initTestRepoWithRemote(t *testing.T) (string, string, string) {
	t.Helper()
	tmp := t.TempDir()

	// Create bare remote
	remoteDir := filepath.Join(tmp, "remote.git")
	if err := exec.Command("git", "init", "--bare", remoteDir).Run(); err != nil {
		t.Fatalf("git init --bare: %v", err)
	}

	// Create local repo
	localDir := filepath.Join(tmp, "local")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = localDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
	}

	// Initial commit
	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, args := range [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", "initial"},
		{"git", "remote", "add", "origin", remoteDir},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = localDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
	}

	// Get main branch name and push
	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = localDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("branch --show-current: %v", err)
	}
	mainBranch := strings.TrimSpace(string(out))

	cmd = exec.Command("git", "push", "-u", "origin", mainBranch)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("push: %v", err)
	}

	return localDir, remoteDir, mainBranch
}

func TestPruneStaleBranches_MergedBranch(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	// Create a polecat branch, commit, and merge it to main
	if err := g.CreateBranch("polecat/test-merged"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/test-merged"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "feature.txt"), []byte("feature"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := g.Add("feature.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("add feature"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Push polecat branch to origin
	cmd := exec.Command("git", "push", "origin", "polecat/test-merged")
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("push polecat branch: %v", err)
	}

	// Merge to main
	if err := g.Checkout(mainBranch); err != nil {
		t.Fatalf("Checkout main: %v", err)
	}
	if err := g.Merge("polecat/test-merged"); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	// Push main
	cmd = exec.Command("git", "push", "origin", mainBranch)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("push main: %v", err)
	}

	// Delete remote polecat branch (simulating refinery cleanup)
	cmd = exec.Command("git", "push", "origin", "--delete", "polecat/test-merged")
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("delete remote branch: %v", err)
	}

	// Fetch --prune to remove remote tracking ref
	if err := g.FetchPrune("origin"); err != nil {
		t.Fatalf("FetchPrune: %v", err)
	}

	// Verify polecat branch still exists locally
	branches, err := g.ListBranches("polecat/*")
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	if len(branches) != 1 {
		t.Fatalf("expected 1 local polecat branch, got %d", len(branches))
	}

	// Prune should remove it
	pruned, err := g.PruneStaleBranches("polecat/*", false)
	if err != nil {
		t.Fatalf("PruneStaleBranches: %v", err)
	}
	if len(pruned) != 1 {
		t.Fatalf("expected 1 pruned branch, got %d", len(pruned))
	}
	if pruned[0].Name != "polecat/test-merged" {
		t.Errorf("pruned name = %q, want polecat/test-merged", pruned[0].Name)
	}
	if pruned[0].Reason != "no-remote-merged" {
		t.Errorf("pruned reason = %q, want no-remote-merged", pruned[0].Reason)
	}

	// Verify branch is gone
	branches, err = g.ListBranches("polecat/*")
	if err != nil {
		t.Fatalf("ListBranches after prune: %v", err)
	}
	if len(branches) != 0 {
		t.Errorf("expected 0 branches after prune, got %d: %v", len(branches), branches)
	}
}

func TestPruneStaleBranches_DryRun(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	// Create and merge a polecat branch (same as above)
	if err := g.CreateBranch("polecat/test-dryrun"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/test-dryrun"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "dry.txt"), []byte("dry"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := g.Add("dry.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("dry run test"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := g.Checkout(mainBranch); err != nil {
		t.Fatalf("Checkout main: %v", err)
	}
	if err := g.Merge("polecat/test-dryrun"); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	// Push main to update origin/main
	cmd := exec.Command("git", "push", "origin", mainBranch)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("push main: %v", err)
	}

	// Dry run should report but not delete
	pruned, err := g.PruneStaleBranches("polecat/*", true)
	if err != nil {
		t.Fatalf("PruneStaleBranches dry-run: %v", err)
	}
	if len(pruned) != 1 {
		t.Fatalf("expected 1 branch in dry-run, got %d", len(pruned))
	}

	// Branch should still exist
	branches, err := g.ListBranches("polecat/*")
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	if len(branches) != 1 {
		t.Errorf("expected branch to still exist after dry-run, got %d branches", len(branches))
	}
}

func TestPruneStaleBranches_SkipsCurrentBranch(t *testing.T) {
	localDir, _, _ := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	// Create and checkout a polecat branch (making it the current branch)
	if err := g.CreateBranch("polecat/current"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/current"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	// Prune should not delete the current branch
	pruned, err := g.PruneStaleBranches("polecat/*", false)
	if err != nil {
		t.Fatalf("PruneStaleBranches: %v", err)
	}
	if len(pruned) != 0 {
		t.Errorf("expected 0 pruned (current branch should be skipped), got %d", len(pruned))
	}
}

func TestPruneStaleBranches_SkipsUnmerged(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	// Create a polecat branch with a commit NOT merged to main
	if err := g.CreateBranch("polecat/unmerged"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/unmerged"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "unmerged.txt"), []byte("unmerged"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := g.Add("unmerged.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("unmerged work"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Push to remote so it has a remote tracking branch
	cmd := exec.Command("git", "push", "origin", "polecat/unmerged")
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("push: %v", err)
	}

	if err := g.Checkout(mainBranch); err != nil {
		t.Fatalf("Checkout main: %v", err)
	}

	// Prune should NOT delete unmerged branch that still has remote
	pruned, err := g.PruneStaleBranches("polecat/*", false)
	if err != nil {
		t.Fatalf("PruneStaleBranches: %v", err)
	}
	if len(pruned) != 0 {
		t.Errorf("expected 0 pruned (unmerged with remote should be kept), got %d", len(pruned))
	}
}

func TestListPushRemoteRefsWithHashesClassifiesRemoteOnlyMergedBranch(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)
	branch := "polecat/remote-merged"

	if err := g.CreateBranch(branch); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout(branch); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "remote.txt"), []byte("remote"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := g.Add("remote.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("remote merged work"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	branchSHA, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev branch: %v", err)
	}
	runGit(t, localDir, "push", "origin", branch)

	if err := g.Checkout(mainBranch); err != nil {
		t.Fatalf("Checkout main: %v", err)
	}
	if err := g.Merge(branch); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	runGit(t, localDir, "push", "origin", mainBranch)
	if err := g.DeleteBranch(branch, false); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	runGit(t, localDir, "update-ref", "-d", "refs/remotes/origin/"+branch)

	refs, err := g.ListPushRemoteRefsWithHashes("origin", "refs/heads/polecat/")
	if err != nil {
		t.Fatalf("ListPushRemoteRefsWithHashes: %v", err)
	}
	var found RemoteRef
	for _, ref := range refs {
		if ref.Name == "refs/heads/"+branch {
			found = ref
			break
		}
	}
	if found.Name == "" {
		t.Fatalf("remote ref %q not found in %#v", branch, refs)
	}
	if found.Hash != branchSHA {
		t.Fatalf("remote ref hash = %q, want %q", found.Hash, branchSHA)
	}

	merged, err := g.IsAncestor(found.Hash, "origin/"+mainBranch)
	if err != nil {
		t.Fatalf("IsAncestor remote hash: %v", err)
	}
	if !merged {
		t.Fatalf("expected remote-only branch hash to be classified as merged")
	}
}

func TestPushWithEnv(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	// Set up a pre-push hook that blocks unless GT_INTEGRATION_LAND=1
	hooksDir := filepath.Join(localDir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hookScript := `#!/bin/bash
if [[ "$GT_INTEGRATION_LAND" != "1" ]]; then
  echo "BLOCKED: GT_INTEGRATION_LAND not set"
  exit 1
fi
exit 0
`
	hookPath := filepath.Join(hooksDir, "pre-push")
	if err := os.WriteFile(hookPath, []byte(hookScript), 0755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	// Make a commit to push
	if err := os.WriteFile(filepath.Join(localDir, "env-test.txt"), []byte("test"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := g.Add("env-test.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("env test"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Regular Push should fail (hook blocks without env var)
	err := g.Push("origin", mainBranch, false)
	if err == nil {
		t.Fatal("expected Push to fail without GT_INTEGRATION_LAND")
	}

	// PushWithEnv with GT_INTEGRATION_LAND=1 should succeed
	err = g.PushWithEnv("origin", mainBranch, false, []string{"GT_INTEGRATION_LAND=1"})
	if err != nil {
		t.Fatalf("PushWithEnv with GT_INTEGRATION_LAND=1 should succeed: %v", err)
	}
}

func TestFetchPrune(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	// Create and push a branch
	if err := g.CreateBranch("polecat/prune-test"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	cmd := exec.Command("git", "push", "origin", "polecat/prune-test")
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := g.Checkout(mainBranch); err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	// Verify remote tracking ref exists
	exists, err := g.RemoteTrackingBranchExists("origin", "polecat/prune-test")
	if err != nil {
		t.Fatalf("RemoteTrackingBranchExists: %v", err)
	}
	if !exists {
		t.Fatal("expected remote tracking branch to exist")
	}

	// Delete remote branch
	cmd = exec.Command("git", "push", "origin", "--delete", "polecat/prune-test")
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("delete remote: %v", err)
	}

	// FetchPrune should remove the stale tracking ref
	if err := g.FetchPrune("origin"); err != nil {
		t.Fatalf("FetchPrune: %v", err)
	}

	exists, err = g.RemoteTrackingBranchExists("origin", "polecat/prune-test")
	if err != nil {
		t.Fatalf("RemoteTrackingBranchExists after prune: %v", err)
	}
	if exists {
		t.Error("expected remote tracking branch to be pruned")
	}
}

// initTestRepoWithSubmodule creates a parent repo with a submodule for testing.
// Returns parentDir, submoduleRemoteDir (bare).
func initTestRepoWithSubmodule(t *testing.T) (string, string) {
	t.Helper()
	tmp := t.TempDir()

	// Create a "remote" bare repo for the submodule
	subRemote := filepath.Join(tmp, "sub-remote.git")
	runGit(t, tmp, "init", "--bare", "--initial-branch", "main", subRemote)

	// Create a working clone of the submodule to add content
	subWork := filepath.Join(tmp, "sub-work")
	runGit(t, tmp, "clone", subRemote, subWork)
	runGit(t, subWork, "config", "user.email", "test@test.com")
	runGit(t, subWork, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(subWork, "lib.go"), []byte("package lib\n"), 0644); err != nil {
		t.Fatalf("write sub file: %v", err)
	}
	runGit(t, subWork, "add", ".")
	runGit(t, subWork, "commit", "-m", "initial sub commit")
	runGit(t, subWork, "push", "origin", "main")

	// Create the parent repo
	parent := filepath.Join(tmp, "parent")
	runGit(t, tmp, "init", "--initial-branch", "main", parent)
	runGit(t, parent, "config", "user.email", "test@test.com")
	runGit(t, parent, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(parent, "README.md"), []byte("# Parent\n"), 0644); err != nil {
		t.Fatalf("write parent file: %v", err)
	}
	runGit(t, parent, "add", ".")
	runGit(t, parent, "commit", "-m", "initial parent commit")

	// Add the submodule
	runGit(t, parent, "submodule", "add", subRemote, "libs/sub")
	runGit(t, parent, "commit", "-m", "add submodule")

	return parent, subRemote
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	// Prepend -c protocol.file.allow=always to allow local file:// transport
	fullArgs := append([]string{"-c", "protocol.file.allow=always"}, args...)
	cmd := exec.Command("git", fullArgs...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func TestInitSubmodules_NoSubmodules(t *testing.T) {
	dir := initTestRepo(t)
	// Should be a no-op, not an error
	if err := InitSubmodules(dir); err != nil {
		t.Fatalf("InitSubmodules on repo without submodules: %v", err)
	}
}

func TestInitSubmodules_SkipsUntrackedGitmodules(t *testing.T) {
	dir := initTestRepo(t)
	gitmodules := filepath.Join(dir, ".gitmodules")
	content := []byte("[submodule \"libs/sub\"]\n\tpath = libs/sub\n\turl = https://example.com/sub.git\n")
	if err := os.WriteFile(gitmodules, content, 0644); err != nil {
		t.Fatalf("write .gitmodules: %v", err)
	}
	if err := InitSubmodules(dir); err != nil {
		t.Fatalf("InitSubmodules should skip untracked .gitmodules: %v", err)
	}
}

func TestHasTrackedGitmodules(t *testing.T) {
	dir := initTestRepo(t)
	if hasTrackedGitmodules(dir) {
		t.Fatal("expected false when .gitmodules doesn't exist")
	}
	gitmodules := filepath.Join(dir, ".gitmodules")
	if err := os.WriteFile(gitmodules, []byte("[submodule \"x\"]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if hasTrackedGitmodules(dir) {
		t.Fatal("expected false when .gitmodules exists but is untracked")
	}
	runGit(t, dir, "add", ".gitmodules")
	runGit(t, dir, "commit", "-m", "add gitmodules")
	if !hasTrackedGitmodules(dir) {
		t.Fatal("expected true when .gitmodules is tracked")
	}
}

func TestInitSubmodules_WithSubmodules(t *testing.T) {
	parent, _ := initTestRepoWithSubmodule(t)

	// The submodule should already be initialized from the test setup
	libFile := filepath.Join(parent, "libs", "sub", "lib.go")
	if _, err := os.Stat(libFile); err != nil {
		t.Fatalf("expected submodule file to exist after setup: %v", err)
	}

	// Now test that InitSubmodules works on a fresh clone
	tmp := t.TempDir()
	cloneDest := filepath.Join(tmp, "clone")
	// Clone without --recurse-submodules to simulate current behavior
	cmd := exec.Command("git", "-c", "protocol.file.allow=always", "clone", parent, cloneDest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}

	// Submodule dir exists but is empty
	subDir := filepath.Join(cloneDest, "libs", "sub")
	entries, _ := os.ReadDir(subDir)
	if len(entries) > 0 {
		t.Fatal("expected empty submodule dir before init")
	}

	// Allow file:// transport for submodule init in test environment
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "always")

	// InitSubmodules should populate it
	if err := InitSubmodules(cloneDest); err != nil {
		t.Fatalf("InitSubmodules: %v", err)
	}

	libFile = filepath.Join(cloneDest, "libs", "sub", "lib.go")
	if _, err := os.Stat(libFile); err != nil {
		t.Fatalf("expected submodule file after InitSubmodules: %v", err)
	}
}

func TestSubmoduleChanges(t *testing.T) {
	parent, subRemote := initTestRepoWithSubmodule(t)

	// Create a branch with a submodule change
	runGit(t, parent, "checkout", "-b", "feature")

	// Make a new commit in the submodule
	subPath := filepath.Join(parent, "libs", "sub")
	if err := os.WriteFile(filepath.Join(subPath, "new.go"), []byte("package lib\n// new\n"), 0644); err != nil {
		t.Fatalf("write new sub file: %v", err)
	}
	runGit(t, subPath, "add", ".")
	runGit(t, subPath, "commit", "-m", "new sub commit")
	runGit(t, subPath, "push", "origin", "HEAD:main")

	// Update the parent's submodule pointer
	runGit(t, parent, "add", "libs/sub")
	runGit(t, parent, "commit", "-m", "update submodule pointer")

	// Now check for submodule changes between main and feature
	g := NewGit(parent)
	changes, err := g.SubmoduleChanges("main", "feature")
	if err != nil {
		t.Fatalf("SubmoduleChanges: %v", err)
	}

	if len(changes) != 1 {
		t.Fatalf("expected 1 submodule change, got %d", len(changes))
	}

	sc := changes[0]
	if sc.Path != "libs/sub" {
		t.Errorf("expected path libs/sub, got %s", sc.Path)
	}
	if sc.OldSHA == "" {
		t.Error("expected non-empty OldSHA")
	}
	if sc.NewSHA == "" {
		t.Error("expected non-empty NewSHA")
	}
	if sc.OldSHA == sc.NewSHA {
		t.Error("expected different SHAs")
	}
	if sc.URL != subRemote {
		t.Errorf("expected URL %s, got %s", subRemote, sc.URL)
	}
}

func TestSubmoduleChanges_NoSubmodules(t *testing.T) {
	dir := initTestRepo(t)

	// Create a branch with a regular file change
	runGit(t, dir, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "add file")

	// Detect the default branch name (may be "main" or "master" depending on git config)
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--verify", "main")
	defaultBranch := "main"
	if cmd.Run() != nil {
		defaultBranch = "master"
	}

	g := NewGit(dir)
	changes, err := g.SubmoduleChanges(defaultBranch, "feature")
	if err != nil {
		t.Fatalf("SubmoduleChanges: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 submodule changes, got %d", len(changes))
	}
}

func TestPushSubmoduleCommit(t *testing.T) {
	parent, subRemote := initTestRepoWithSubmodule(t)

	// Make a new commit in the submodule (but don't push it)
	subPath := filepath.Join(parent, "libs", "sub")
	if err := os.WriteFile(filepath.Join(subPath, "pushed.go"), []byte("package lib\n// pushed\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, subPath, "add", ".")
	runGit(t, subPath, "commit", "-m", "unpushed commit")

	// Get the SHA of the new commit
	cmd := exec.Command("git", "-C", subPath, "rev-parse", "HEAD")
	shaBytes, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	sha := strings.TrimSpace(string(shaBytes))

	// Verify it's not on the remote yet
	lsCmd := exec.Command("git", "ls-remote", subRemote, "refs/heads/main")
	lsOut, _ := lsCmd.Output()
	remoteSHA := strings.Fields(string(lsOut))[0]
	if remoteSHA == sha {
		t.Fatal("commit should not be on remote yet")
	}

	// Push it using PushSubmoduleCommit
	g := NewGit(parent)
	if err := g.PushSubmoduleCommit("libs/sub", sha, "origin"); err != nil {
		t.Fatalf("PushSubmoduleCommit: %v", err)
	}

	// Verify it's now on the remote
	lsCmd = exec.Command("git", "ls-remote", subRemote, "refs/heads/main")
	lsOut, _ = lsCmd.Output()
	remoteSHA = strings.Fields(string(lsOut))[0]
	if remoteSHA != sha {
		t.Errorf("expected remote main to be %s, got %s", sha, remoteSHA)
	}
}

func TestPushSubmoduleCommit_ShortSHA(t *testing.T) {
	// Verify that PushSubmoduleCommit doesn't panic when given a short SHA
	// that triggers an error path. The error message formats sha[:8] which
	// panics if len(sha) < 8. (gt-dg7)
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Use a 7-char SHA (shorter than the [:8] slice). This will fail to push
	// (no such submodule), but must not panic — it should return an error.
	shortSHA := "09bcf16"
	err := g.PushSubmoduleCommit("nonexistent/sub", shortSHA, "origin")
	if err == nil {
		t.Fatal("expected error for nonexistent submodule, got nil")
	}
	// The key assertion: we got here without panicking
}

func TestSubmoduleChanges_SkipsClaudeWorktrees(t *testing.T) {
	// Verify that SubmoduleChanges filters out .claude/ paths.
	// Claude Code creates worktrees under .claude/worktrees/ which have .git
	// files that git may report as gitlinks (mode 160000). These are not
	// real submodules and should be skipped. (gt-dg7)
	tmp := t.TempDir()

	// Create a bare remote for the .claude submodule
	claudeRemote := filepath.Join(tmp, "claude-remote.git")
	runGit(t, tmp, "init", "--bare", "--initial-branch", "main", claudeRemote)

	// Populate the claude submodule remote
	claudeWork := filepath.Join(tmp, "claude-work")
	runGit(t, tmp, "clone", claudeRemote, claudeWork)
	runGit(t, claudeWork, "config", "user.email", "test@test.com")
	runGit(t, claudeWork, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(claudeWork, "init.go"), []byte("package x\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, claudeWork, "add", ".")
	runGit(t, claudeWork, "commit", "-m", "init")
	runGit(t, claudeWork, "push", "origin", "main")

	// Start from the standard parent with libs/sub submodule
	parent, _ := initTestRepoWithSubmodule(t)

	// Add the .claude/worktrees submodule
	runGit(t, parent, "submodule", "add", claudeRemote, ".claude/worktrees/codebase-friction")
	runGit(t, parent, "commit", "-m", "add claude worktree submodule")

	// Create a branch and update both submodules
	runGit(t, parent, "checkout", "-b", "feature")

	// Update the real submodule
	subPath := filepath.Join(parent, "libs", "sub")
	if err := os.WriteFile(filepath.Join(subPath, "change.go"), []byte("package lib\n// change\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, subPath, "add", ".")
	runGit(t, subPath, "commit", "-m", "real sub change")
	runGit(t, subPath, "push", "origin", "HEAD:main")

	// Update the .claude worktree submodule
	claudePath := filepath.Join(parent, ".claude", "worktrees", "codebase-friction")
	if err := os.WriteFile(filepath.Join(claudePath, "change.go"), []byte("package x\n// change\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, claudePath, "add", ".")
	runGit(t, claudePath, "commit", "-m", "claude worktree change")
	runGit(t, claudePath, "push", "origin", "HEAD:main")

	runGit(t, parent, "add", ".")
	runGit(t, parent, "commit", "-m", "update both submodules")

	// SubmoduleChanges should return only the real submodule, not the .claude/ one
	g := NewGit(parent)
	changes, err := g.SubmoduleChanges("main", "feature")
	if err != nil {
		t.Fatalf("SubmoduleChanges: %v", err)
	}

	if len(changes) != 1 {
		t.Fatalf("expected 1 submodule change (filtered .claude/), got %d", len(changes))
	}
	if changes[0].Path != "libs/sub" {
		t.Errorf("expected path libs/sub, got %s", changes[0].Path)
	}
}

func TestConfigurePushURL(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Add a remote
	cmd := exec.Command("git", "remote", "add", "origin", "https://github.com/upstream/repo.git")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("add remote: %v", err)
	}

	// Configure push URL
	pushURL := "https://github.com/fork/repo.git"
	if err := g.ConfigurePushURL("origin", pushURL); err != nil {
		t.Fatalf("ConfigurePushURL: %v", err)
	}

	// Verify via GetPushURL
	got, err := g.GetPushURL("origin")
	if err != nil {
		t.Fatalf("GetPushURL: %v", err)
	}
	if got != pushURL {
		t.Errorf("GetPushURL = %q, want %q", got, pushURL)
	}

	// Verify fetch URL is unchanged
	fetchCmd := exec.Command("git", "remote", "get-url", "origin")
	fetchCmd.Dir = dir
	out, err := fetchCmd.Output()
	if err != nil {
		t.Fatalf("get fetch url: %v", err)
	}
	fetchURL := strings.TrimSpace(string(out))
	if fetchURL != "https://github.com/upstream/repo.git" {
		t.Errorf("fetch URL changed to %q, should be unchanged", fetchURL)
	}
}

func TestGetPushURL_NoPushURL(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Add remote without custom push URL
	cmd := exec.Command("git", "remote", "add", "origin", "https://github.com/upstream/repo.git")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("add remote: %v", err)
	}

	// GetPushURL returns fetch URL when no custom push URL is set
	got, err := g.GetPushURL("origin")
	if err != nil {
		t.Fatalf("GetPushURL: %v", err)
	}
	if got != "https://github.com/upstream/repo.git" {
		t.Errorf("GetPushURL = %q, want fetch URL when no push URL configured", got)
	}
}

// TestStashCount_FiltersByBranch verifies that StashCount only counts stashes
// belonging to the current branch, not stashes from other worktrees/branches.
// Git stashes are repo-wide (stored in .git/refs/stash), so without filtering
// a worktree would see sibling stashes and block Remove(force=true).
func TestStashCount_FiltersByBranch(t *testing.T) {
	t.Parallel()
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Create a stash on the default branch
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "stash", "push", "-m", "main-stash")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git stash: %v", err)
	}

	// Create a worktree on a different branch
	wtDir := t.TempDir()
	cmd = exec.Command("git", "worktree", "add", wtDir, "-b", "polecat-branch")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add: %v", err)
	}

	// StashCount from worktree should be 0 (stash belongs to main, not polecat-branch)
	wtGit := NewGit(wtDir)
	count, err := wtGit.StashCount()
	if err != nil {
		t.Fatalf("StashCount: %v", err)
	}
	if count != 0 {
		t.Errorf("StashCount from worktree = %d, want 0 (stash belongs to different branch)", count)
	}
	totalCount, err := wtGit.StashCountAll()
	if err != nil {
		t.Fatalf("StashCountAll: %v", err)
	}
	if totalCount != 1 {
		t.Errorf("StashCountAll from worktree = %d, want 1 shared repo stash", totalCount)
	}

	// StashCount from main repo should be 1
	mainCount, err := g.StashCount()
	if err != nil {
		t.Fatalf("StashCount: %v", err)
	}
	if mainCount != 1 {
		t.Errorf("StashCount from main = %d, want 1", mainCount)
	}

	// Create a stash on the worktree branch
	if err := os.WriteFile(filepath.Join(wtDir, "wt-dirty.txt"), []byte("wt-dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = wtDir
	_ = cmd.Run()
	cmd = exec.Command("git", "stash", "push", "-m", "wt-stash")
	cmd.Dir = wtDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git stash in worktree: %v", err)
	}

	// Now worktree should see 1 (its own stash)
	count, err = wtGit.StashCount()
	if err != nil {
		t.Fatalf("StashCount: %v", err)
	}
	if count != 1 {
		t.Errorf("StashCount from worktree after own stash = %d, want 1", count)
	}
	totalCount, err = wtGit.StashCountAll()
	if err != nil {
		t.Fatalf("StashCountAll after own stash: %v", err)
	}
	if totalCount != 2 {
		t.Errorf("StashCountAll from worktree after own stash = %d, want 2 repo-wide stashes", totalCount)
	}

	// Main repo should still see 1 (only its own stash)
	mainCount, err = g.StashCount()
	if err != nil {
		t.Fatalf("StashCount: %v", err)
	}
	if mainCount != 1 {
		t.Errorf("StashCount from main after worktree stash = %d, want 1", mainCount)
	}
}

// TestStashCount_DetachedHEAD verifies that StashCount counts all stashes
// when in detached HEAD state (cannot determine branch, falls back to counting all).
func TestStashCount_DetachedHEAD(t *testing.T) {
	t.Parallel()
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Create a stash on main
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "stash", "push", "-m", "some-stash")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git stash: %v", err)
	}

	// Detach HEAD
	cmd = exec.Command("git", "checkout", "--detach")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git checkout --detach: %v", err)
	}

	// In detached HEAD, StashCount should count all stashes (safe fallback)
	count, err := g.StashCount()
	if err != nil {
		t.Fatalf("StashCount: %v", err)
	}
	if count != 1 {
		t.Errorf("StashCount in detached HEAD = %d, want 1 (should count all stashes)", count)
	}
}

// TestStashCount_CustomMessage verifies that StashCount handles both
// "WIP on <branch>:" (auto-stash) and "On <branch>:" (custom message) formats.
func TestStashCount_CustomMessage(t *testing.T) {
	t.Parallel()
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Create a stash with custom message (produces "On <branch>: <message>" format)
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "stash", "push", "-m", "my custom message")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git stash: %v", err)
	}

	// Should count the custom-message stash on current branch
	count, err := g.StashCount()
	if err != nil {
		t.Fatalf("StashCount: %v", err)
	}
	if count != 1 {
		t.Errorf("StashCount with custom message stash = %d, want 1", count)
	}
}

// TestStashCount_NoFalsePositiveFromCommitMessage verifies that a stash
// from branch "develop" with commit message containing "on fix:" does NOT
// match when the current branch is "fix".
func TestStashCount_NoFalsePositiveFromCommitMessage(t *testing.T) {
	t.Parallel()
	dir := initTestRepo(t)

	// Create branch "develop" and make a commit with message containing "on fix:"
	cmd := exec.Command("git", "checkout", "-b", "develop")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git checkout -b develop: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("work on fix: edge case"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "work on fix: edge case")
	cmd.Dir = dir
	_ = cmd.Run()

	// Create a stash on "develop" — its reflog line will contain "on fix:" in the
	// commit message, but the branch prefix is "WIP on develop:"
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "stash")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git stash: %v", err)
	}

	// Switch to branch "fix" — should NOT see the "develop" stash
	cmd = exec.Command("git", "checkout", "-b", "fix")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git checkout -b fix: %v", err)
	}

	fixGit := NewGit(dir)
	count, err := fixGit.StashCount()
	if err != nil {
		t.Fatalf("StashCount: %v", err)
	}
	if count != 0 {
		t.Errorf("StashCount on 'fix' branch = %d, want 0 (stash belongs to 'develop', commit msg has 'on fix:')", count)
	}
}

// TestStashListForBranch verifies StashListForBranch returns entries scoped
// to the current branch with parsed Ref/Message fields.
func TestStashListForBranch(t *testing.T) {
	t.Parallel()
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Empty repo — no stashes
	entries, err := g.StashListForBranch()
	if err != nil {
		t.Fatalf("StashListForBranch (empty): %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("StashListForBranch (empty) = %d entries, want 0", len(entries))
	}

	// Create two stashes on main
	for i, content := range []string{"first", "second"} {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "add", ".")
		cmd.Dir = dir
		_ = cmd.Run()
		cmd = exec.Command("git", "stash", "push", "-m", fmt.Sprintf("stash-%d", i))
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git stash %d: %v", i, err)
		}
	}

	entries, err = g.StashListForBranch()
	if err != nil {
		t.Fatalf("StashListForBranch: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("StashListForBranch = %d entries, want 2", len(entries))
	}
	// Newest first: stash@{0} is "second", stash@{1} is "first"
	if entries[0].Ref != "stash@{0}" || entries[1].Ref != "stash@{1}" {
		t.Errorf("Ref ordering = [%s, %s], want [stash@{0}, stash@{1}]",
			entries[0].Ref, entries[1].Ref)
	}
	if entries[0].Message == "" || entries[1].Message == "" {
		t.Errorf("Empty messages: [%s, %s]", entries[0].Message, entries[1].Message)
	}
}

// TestStashPop verifies StashPop applies and drops a stash, leaving the
// working tree dirty (so the gt-pvx auto-commit path catches it).
func TestStashPop(t *testing.T) {
	t.Parallel()
	dir := initTestRepo(t)
	g := NewGit(dir)

	// Create a stash
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "stash", "push", "-m", "popme")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git stash: %v", err)
	}

	// Confirm one stash exists
	count, _ := g.StashCount()
	if count != 1 {
		t.Fatalf("StashCount before pop = %d, want 1", count)
	}

	// Pop it
	if err := g.StashPop("stash@{0}"); err != nil {
		t.Fatalf("StashPop: %v", err)
	}

	// Stash should be gone
	count, _ = g.StashCount()
	if count != 0 {
		t.Errorf("StashCount after pop = %d, want 0", count)
	}

	// Working tree should now have the file (dirty)
	if _, err := os.Stat(filepath.Join(dir, "dirty.txt")); err != nil {
		t.Errorf("dirty.txt should exist after pop: %v", err)
	}

	// Empty ref should error
	if err := g.StashPop(""); err == nil {
		t.Error("StashPop(\"\") should error")
	}
}

func TestClearPushURL(t *testing.T) {
	dir := initTestRepo(t)
	g := NewGit(dir)

	fetchURL := "https://github.com/upstream/repo.git"
	pushURL := "https://github.com/fork/repo.git"

	cmd := exec.Command("git", "remote", "add", "origin", fetchURL)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("add remote: %v", err)
	}

	// Set a custom push URL
	if err := g.ConfigurePushURL("origin", pushURL); err != nil {
		t.Fatalf("ConfigurePushURL: %v", err)
	}
	got, _ := g.GetPushURL("origin")
	if got != pushURL {
		t.Fatalf("push URL after set = %q, want %q", got, pushURL)
	}

	// Clear the custom push URL
	if err := g.ClearPushURL("origin"); err != nil {
		t.Fatalf("ClearPushURL: %v", err)
	}

	// After clearing, GetPushURL should return the fetch URL
	got, err := g.GetPushURL("origin")
	if err != nil {
		t.Fatalf("GetPushURL after clear: %v", err)
	}
	if got != fetchURL {
		t.Errorf("push URL after clear = %q, want %q (fetch URL)", got, fetchURL)
	}

	// Clearing again should be a no-op (not an error)
	if err := g.ClearPushURL("origin"); err != nil {
		t.Errorf("ClearPushURL (idempotent) should not error, got: %v", err)
	}
}

func TestIsGasTownRuntimePath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{".claude/", true},
		{".claude/settings.json", true},
		{".claude/commands/foo.md", true},
		{".claude", true},
		{".runtime/", true},
		{".runtime/state.json", true},
		{".runtime", true},
		{".opencode/", true},
		{".opencode/plugins/gastown.js", true},
		{".opencode/commands/handoff.md", true},
		{".beads/", true},
		{".beads/db.json", true},
		{".beads\\db.json", true},
		{".beads/.runtime/state.json", true},
		{".logs/agent.log", true},
		{"__pycache__/", true},
		{"__pycache__/foo.cpython-312.pyc", true},
		{"src/__pycache__/bar.pyc", true},
		{"services/cyrus/workflow-cyrus-edge/node_modules/pkg/index.js", true},
		{"services\\cyrus\\workflow-cyrus-edge\\node_modules\\pkg\\index.js", true},
		{"dashboard/public/meridian-dashboard/.vite/vitest/hash/results.json", true},
		{"services/workflows/collateral-internal/execution_log.db", true},
		{"api/.pytest_cache/v/cache/nodeids", true},
		{"api/.mypy_cache/3.12/module.meta.json", true},
		{"api/.ruff_cache/0.8.0/cache", true},
		{"coverage/lcov.info", true},
		{"htmlcov/index.html", true},
		{"src/module.pyc", true},
		{"frontend/.DS_Store", true},
		{"src/main.go", false},
		{"README.md", false},
		{".gitignore", false},
		{"claude-stuff/foo", false},
		{"src/coverage_report.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isGasTownRuntimePath(tt.path)
			if got != tt.want {
				t.Errorf("isGasTownRuntimePath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestCleanExcludingRuntime(t *testing.T) {
	tests := []struct {
		name string
		s    UncommittedWorkStatus
		want bool
	}{
		{
			name: "only runtime artifacts",
			s: UncommittedWorkStatus{
				HasUncommittedChanges: true,
				UntrackedFiles:        []string{".claude/", ".opencode/plugins/gastown.js", ".runtime/state.json"},
			},
			want: true,
		},
		{
			name: "real code changes",
			s: UncommittedWorkStatus{
				HasUncommittedChanges: true,
				ModifiedFiles:         []string{"src/main.go"},
			},
			want: false,
		},
		{
			name: "runtime path conflict blocks",
			s: UncommittedWorkStatus{
				HasUncommittedChanges: true,
				UnmergedFiles:         []string{".opencode/plugins/gastown.js"},
			},
			want: false,
		},
		{
			name: "mix of runtime and real",
			s: UncommittedWorkStatus{
				HasUncommittedChanges: true,
				UntrackedFiles:        []string{".claude/settings.json"},
				ModifiedFiles:         []string{"src/main.go"},
			},
			want: false,
		},
		{
			name: "clean",
			s:    UncommittedWorkStatus{},
			want: true,
		},
		{
			name: "stashes ignored (survive worktree deletion)",
			s: UncommittedWorkStatus{
				StashCount: 1,
			},
			want: true,
		},
		{
			// Unpushed commits alone do not affect CleanExcludingRuntime — this
			// function only evaluates uncommitted file changes. Unpushed commits
			// are handled separately by the CommitsAhead check in gt done (gas-7vg).
			name: "unpushed commits alone do not block",
			s: UncommittedWorkStatus{
				UnpushedCommits: 2,
			},
			want: true,
		},
		{
			// The primary bug scenario (gas-7vg): polecat commits work (1 unpushed
			// commit) then calls gt done with only infrastructure files untracked.
			// CleanExcludingRuntime must return true so gt done is not blocked.
			name: "unpushed commit with only runtime artifacts",
			s: UncommittedWorkStatus{
				HasUncommittedChanges: true,
				UnpushedCommits:       1,
				UntrackedFiles:        []string{".beads/", ".claude/commands/done.md", ".runtime/state.json"},
			},
			want: true,
		},
		{
			name: "pycache untracked",
			s: UncommittedWorkStatus{
				HasUncommittedChanges: true,
				UntrackedFiles:        []string{"__pycache__/foo.pyc", ".beads/db"},
			},
			want: true,
		},
		{
			name: "nested dependency and cache artifacts",
			s: UncommittedWorkStatus{
				HasUncommittedChanges: true,
				UntrackedFiles: []string{
					"services/cyrus/workflow-cyrus-edge/node_modules/pkg/index.js",
					"dashboard/public/meridian-dashboard/.vite/vitest/hash/results.json",
					"services/workflows/collateral-internal/execution_log.db",
					"api/.pytest_cache/v/cache/nodeids",
					"src/__pycache__/module.cpython-312.pyc",
				},
			},
			want: true,
		},
		{
			// CLAUDE.local.md is a Gas Town overlay file (gt-p35) that must not
			// block gt done or be auto-committed.
			name: "CLAUDE.local.md is runtime artifact",
			s: UncommittedWorkStatus{
				HasUncommittedChanges: true,
				UntrackedFiles:        []string{"CLAUDE.local.md"},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.s.CleanExcludingRuntime()
			if got != tt.want {
				t.Errorf("CleanExcludingRuntime() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRuntimeArtifactPaths(t *testing.T) {
	status := UncommittedWorkStatus{
		HasUncommittedChanges: true,
		ModifiedFiles: []string{
			"services/workflows/collateral-internal/execution_log.db",
			"src/handler.go",
		},
		UntrackedFiles: []string{
			".opencode/plugins/gastown.js",
			"services/cyrus/workflow-cyrus-edge/node_modules/pkg/index.js",
			"services/cyrus/workflow-cyrus-edge/node_modules/pkg/package.json",
			"dashboard/public/meridian-dashboard/.vite/vitest/hash/results.json",
			"api/.pytest_cache/v/cache/nodeids",
			"src/__pycache__/module.cpython-312.pyc",
			"cmd/new_feature.go",
		},
	}

	got := status.RuntimeArtifactPaths()
	want := []string{
		"services/workflows/collateral-internal/execution_log.db",
		".opencode/",
		"services/cyrus/workflow-cyrus-edge/node_modules/",
		"dashboard/public/meridian-dashboard/.vite/",
		"api/.pytest_cache/",
		"src/__pycache__/",
	}
	if len(got) != len(want) {
		t.Fatalf("RuntimeArtifactPaths() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("RuntimeArtifactPaths()[%d] = %q, want %q (all: %#v)", i, got[i], want[i], got)
		}
	}
}

func TestParsePorcelainStatusEntryPreservesRenameCopySourceAndConflict(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantCode   string
		wantSource string
		wantPath   string
		wantPaths  []string
		unmerged   bool
	}{
		{
			name:       "rename",
			line:       "R  README.md -> .opencode/plugins/gastown.js",
			wantCode:   "R ",
			wantSource: "README.md",
			wantPath:   ".opencode/plugins/gastown.js",
			wantPaths:  []string{"README.md", ".opencode/plugins/gastown.js"},
		},
		{
			name:       "copy",
			line:       "C  README.md -> .opencode/plugins/gastown.js",
			wantCode:   "C ",
			wantSource: "README.md",
			wantPath:   ".opencode/plugins/gastown.js",
			wantPaths:  []string{"README.md", ".opencode/plugins/gastown.js"},
		},
		{
			name:      "unmerged",
			line:      "UU .opencode/plugins/gastown.js",
			wantCode:  "UU",
			wantPath:  ".opencode/plugins/gastown.js",
			wantPaths: []string{".opencode/plugins/gastown.js"},
			unmerged:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parsePorcelainStatusEntry(tt.line)
			if !ok {
				t.Fatal("parsePorcelainStatusEntry returned ok=false")
			}
			if got.Code != tt.wantCode || got.SourcePath != tt.wantSource || got.Path != tt.wantPath || got.Unmerged != tt.unmerged {
				t.Fatalf("parsePorcelainStatusEntry(%q) = %+v", tt.line, got)
			}
			if paths := got.paths(); strings.Join(paths, "\x00") != strings.Join(tt.wantPaths, "\x00") {
				t.Fatalf("paths = %v, want %v", paths, tt.wantPaths)
			}
		})
	}
}

func TestCheckUncommittedWorkCapturesPorcelainRenameAndUnmergedPaths(t *testing.T) {
	t.Run("rename to real path blocks", func(t *testing.T) {
		dir := initTestRepo(t)
		runGitTestCmd(t, dir, "mv", "README.md", "renamed.md")

		status, err := NewGit(dir).CheckUncommittedWork()
		if err != nil {
			t.Fatalf("CheckUncommittedWork: %v", err)
		}
		if !status.HasUncommittedChanges {
			t.Fatal("rename should mark worktree dirty")
		}
		want := []string{"README.md", "renamed.md"}
		if got := status.NonRuntimePaths(); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("NonRuntimePaths = %v, want %v", got, want)
		}
		if status.CleanExcludingRuntime() {
			t.Fatal("rename to real path must block runtime-excluding clean check")
		}
	})

	t.Run("rename from real path to runtime path blocks", func(t *testing.T) {
		dir := initTestRepo(t)
		if err := os.MkdirAll(filepath.Join(dir, ".opencode", "plugins"), 0755); err != nil {
			t.Fatalf("mkdir opencode plugins: %v", err)
		}
		runGitTestCmd(t, dir, "mv", "README.md", ".opencode/plugins/gastown.js")

		status, err := NewGit(dir).CheckUncommittedWork()
		if err != nil {
			t.Fatalf("CheckUncommittedWork: %v", err)
		}
		if !status.HasUncommittedChanges {
			t.Fatal("runtime rename should still mark raw worktree dirty")
		}
		if got := status.NonRuntimePaths(); len(got) != 1 || got[0] != "README.md" {
			t.Fatalf("NonRuntimePaths = %v, want [README.md]", got)
		}
		if status.CleanExcludingRuntime() {
			t.Fatal("real source renamed to runtime destination must block runtime-excluding clean check")
		}
	})

	t.Run("rename from runtime path to runtime path is ignored by runtime filter", func(t *testing.T) {
		dir := initTestRepo(t)
		if err := os.MkdirAll(filepath.Join(dir, ".opencode", "plugins"), 0755); err != nil {
			t.Fatalf("mkdir opencode plugins: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".opencode", "plugins", "old.js"), []byte("runtime\n"), 0644); err != nil {
			t.Fatalf("write runtime source: %v", err)
		}
		runGitTestCmd(t, dir, "add", ".opencode/plugins/old.js")
		runGitTestCmd(t, dir, "commit", "-m", "add tracked runtime source")
		runGitTestCmd(t, dir, "mv", ".opencode/plugins/old.js", ".opencode/plugins/gastown.js")

		status, err := NewGit(dir).CheckUncommittedWork()
		if err != nil {
			t.Fatalf("CheckUncommittedWork: %v", err)
		}
		if !status.HasUncommittedChanges {
			t.Fatal("runtime rename should still mark raw worktree dirty")
		}
		if got := status.NonRuntimePaths(); len(got) != 0 {
			t.Fatalf("NonRuntimePaths = %v, want none for runtime-only rename", got)
		}
		if !status.CleanExcludingRuntime() {
			t.Fatal("runtime-only rename should be clean excluding runtime")
		}
	})

	t.Run("unmerged runtime conflict blocks", func(t *testing.T) {
		dir := initTestRepo(t)
		runGitTestCmd(t, dir, "branch", "-M", "main")
		if err := os.MkdirAll(filepath.Join(dir, ".opencode", "plugins"), 0755); err != nil {
			t.Fatalf("mkdir opencode plugins: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".opencode", "plugins", "gastown.js"), []byte("base\n"), 0644); err != nil {
			t.Fatalf("write base runtime conflict file: %v", err)
		}
		runGitTestCmd(t, dir, "add", ".opencode/plugins/gastown.js")
		runGitTestCmd(t, dir, "commit", "-m", "add runtime conflict base")
		runGitTestCmd(t, dir, "switch", "-c", "side")
		if err := os.WriteFile(filepath.Join(dir, ".opencode", "plugins", "gastown.js"), []byte("side\n"), 0644); err != nil {
			t.Fatalf("write side runtime conflict file: %v", err)
		}
		runGitTestCmd(t, dir, "commit", "-am", "side runtime change")
		runGitTestCmd(t, dir, "switch", "main")
		if err := os.WriteFile(filepath.Join(dir, ".opencode", "plugins", "gastown.js"), []byte("main\n"), 0644); err != nil {
			t.Fatalf("write main runtime conflict file: %v", err)
		}
		runGitTestCmd(t, dir, "commit", "-am", "main runtime change")
		runGitTestCmdWantFailure(t, dir, "merge", "side")

		status, err := NewGit(dir).CheckUncommittedWork()
		if err != nil {
			t.Fatalf("CheckUncommittedWork: %v", err)
		}
		if got := status.NonRuntimePaths(); len(got) != 1 || got[0] != ".opencode/plugins/gastown.js" {
			t.Fatalf("NonRuntimePaths = %v, want [.opencode/plugins/gastown.js]", got)
		}
		if status.CleanExcludingRuntime() {
			t.Fatal("runtime unmerged conflict must block runtime-excluding clean check")
		}
	})

	t.Run("unmerged conflict blocks", func(t *testing.T) {
		dir := initTestRepo(t)
		runGitTestCmd(t, dir, "branch", "-M", "main")
		if err := os.WriteFile(filepath.Join(dir, "conflict.txt"), []byte("base\n"), 0644); err != nil {
			t.Fatalf("write base conflict file: %v", err)
		}
		runGitTestCmd(t, dir, "add", "conflict.txt")
		runGitTestCmd(t, dir, "commit", "-m", "add conflict base")
		runGitTestCmd(t, dir, "switch", "-c", "side")
		if err := os.WriteFile(filepath.Join(dir, "conflict.txt"), []byte("side\n"), 0644); err != nil {
			t.Fatalf("write side conflict file: %v", err)
		}
		runGitTestCmd(t, dir, "commit", "-am", "side change")
		runGitTestCmd(t, dir, "switch", "main")
		if err := os.WriteFile(filepath.Join(dir, "conflict.txt"), []byte("main\n"), 0644); err != nil {
			t.Fatalf("write main conflict file: %v", err)
		}
		runGitTestCmd(t, dir, "commit", "-am", "main change")
		runGitTestCmdWantFailure(t, dir, "merge", "side")

		status, err := NewGit(dir).CheckUncommittedWork()
		if err != nil {
			t.Fatalf("CheckUncommittedWork: %v", err)
		}
		if got := status.NonRuntimePaths(); len(got) != 1 || got[0] != "conflict.txt" {
			t.Fatalf("NonRuntimePaths = %v, want [conflict.txt]", got)
		}
		if status.CleanExcludingRuntime() {
			t.Fatal("unmerged conflict must block runtime-excluding clean check")
		}
	})
}

func runGitTestCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func runGitTestCmdWantFailure(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("git %v unexpectedly succeeded\n%s", args, out)
	}
}

func TestCheckBranchContamination(t *testing.T) {
	// Create a repo with main and a feature branch that diverges.
	dir := initTestRepo(t) // has initial commit on default branch
	g := NewGit(dir)

	// Create a "main" branch explicitly and add commits to it.
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Rename default branch to main for consistency.
	run("branch", "-M", "main")

	// Create feature branch from current state.
	run("checkout", "-b", "feature")

	// Add a commit on feature.
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature work"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "feature commit")

	// Switch back to main and add several commits (simulating upstream progress).
	run("checkout", "main")
	for i := 0; i < 5; i++ {
		fname := filepath.Join(dir, "main_"+strings.Repeat("x", i+1)+".txt")
		if err := os.WriteFile(fname, []byte("main work"), 0644); err != nil {
			t.Fatal(err)
		}
		run("add", ".")
		run("commit", "-m", "main commit")
	}

	// Check contamination from the feature branch's perspective.
	run("checkout", "feature")
	contam, err := g.CheckBranchContamination("main")
	if err != nil {
		t.Fatalf("CheckBranchContamination: %v", err)
	}

	if contam.Behind != 5 {
		t.Errorf("Behind = %d, want 5", contam.Behind)
	}
	if contam.Ahead != 1 {
		t.Errorf("Ahead = %d, want 1", contam.Ahead)
	}

	// From main's perspective: 0 behind, 5 ahead of feature's merge-base.
	run("checkout", "main")
	contam, err = g.CheckBranchContamination("feature")
	if err != nil {
		t.Fatalf("CheckBranchContamination from main: %v", err)
	}
	if contam.Behind != 1 {
		t.Errorf("Behind (from main) = %d, want 1", contam.Behind)
	}
	if contam.Ahead != 5 {
		t.Errorf("Ahead (from main) = %d, want 5", contam.Ahead)
	}
}

// initTestRepoWithSplitRemote creates a test setup that mirrors the polecat workflow:
// two bare repos (upstream and fork), a local clone whose origin has fetch URL → upstream
// and push URL → fork. Returns (localDir, upstreamBareDir, forkBareDir, mainBranch).
func initTestRepoWithSplitRemote(t *testing.T) (string, string, string, string) {
	t.Helper()
	tmp := t.TempDir()

	upstream := filepath.Join(tmp, "upstream.git")
	fork := filepath.Join(tmp, "fork.git")
	localDir := filepath.Join(tmp, "local")

	for _, bare := range []string{upstream, fork} {
		if err := exec.Command("git", "init", "--bare", bare).Run(); err != nil {
			t.Fatalf("git init --bare %s: %v", bare, err)
		}
	}

	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = localDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
	}

	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, args := range [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", "initial"},
		{"git", "remote", "add", "origin", upstream},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = localDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
	}

	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = localDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("branch --show-current: %v", err)
	}
	mainBranch := strings.TrimSpace(string(out))

	// Push initial commit to both upstream and fork
	cmd = exec.Command("git", "push", "origin", mainBranch)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("push to upstream: %v", err)
	}
	cmd = exec.Command("git", "push", fork, mainBranch)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("push to fork: %v", err)
	}

	// Split the remote: fetch stays at upstream, push goes to fork
	g := NewGit(localDir)
	if err := g.ConfigurePushURL("origin", fork); err != nil {
		t.Fatalf("ConfigurePushURL: %v", err)
	}

	return localDir, upstream, fork, mainBranch
}

func TestForkBackedDefaultPushGuard_SplitPushURL(t *testing.T) {
	localDir, _, _, mainBranch := initTestRepoWithSplitRemote(t)
	g := NewGit(localDir)

	if !g.ForkBackedRemote("origin") {
		t.Fatal("split push URL should be detected as fork-backed")
	}
	if got := g.CleanDefaultBranchBaseRef("origin", mainBranch); got != "origin/"+mainBranch {
		t.Fatalf("CleanDefaultBranchBaseRef split = %q, want origin/%s", got, mainBranch)
	}

	refspecs := []string{
		mainBranch,
		"refs/heads/" + mainBranch,
		"feature:" + mainBranch,
		"feature:refs/heads/" + mainBranch,
		"HEAD:" + mainBranch,
		"+feature:" + mainBranch,
		":" + mainBranch,
	}
	for _, refspec := range refspecs {
		if err := g.RefuseForkBackedDefaultPush("origin", refspec, mainBranch); err == nil {
			t.Fatalf("RefuseForkBackedDefaultPush(%q) returned nil, want refusal", refspec)
		}
	}
	if err := g.PushWithEnv("origin", "HEAD:"+mainBranch, false, []string{"GT_INTEGRATION_LAND=1"}); err == nil {
		t.Fatal("PushWithEnv should not let GT_INTEGRATION_LAND bypass fork default-branch guard")
	}
}

func TestForkBackedDefaultPushGuard_OriginForkWithUpstream(t *testing.T) {
	localDir, upstream, fork, mainBranch := initTestRepoWithSplitRemote(t)
	g := NewGit(localDir)
	if err := g.ClearPushURL("origin"); err != nil {
		t.Fatalf("ClearPushURL: %v", err)
	}
	if _, err := g.SetRemoteURL("origin", fork); err != nil {
		t.Fatalf("SetRemoteURL origin fork: %v", err)
	}
	if err := g.AddUpstreamRemote(upstream); err != nil {
		t.Fatalf("AddUpstreamRemote: %v", err)
	}

	if !g.ForkBackedRemote("origin") {
		t.Fatal("origin fork plus distinct upstream should be detected as fork-backed")
	}
	if got := g.CleanDefaultBranchBaseRef("origin", mainBranch); got != "upstream/"+mainBranch {
		t.Fatalf("CleanDefaultBranchBaseRef fork = %q, want upstream/%s", got, mainBranch)
	}
	if err := g.Push("origin", "feature:"+mainBranch, false); err == nil {
		t.Fatal("Push should block feature-to-default refspec in fork/upstream topology")
	}
}

func TestForkBackedDefaultPushGuard_AllowsNormalDefaultPush(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	if g.ForkBackedRemote("origin") {
		t.Fatal("plain origin remote should not be fork-backed")
	}
	if err := g.RefuseForkBackedDefaultPush("origin", mainBranch, mainBranch); err != nil {
		t.Fatalf("normal default push should be allowed: %v", err)
	}
}

func TestForkBackedDefaultPushGuard_AllowsFeatureBranchPush(t *testing.T) {
	localDir, _, _, _ := initTestRepoWithSplitRemote(t)
	g := NewGit(localDir)

	if err := g.CreateBranch("polecat/fork-policy"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/fork-policy"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "policy.txt"), []byte("policy\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := g.Add("policy.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("fork policy feature"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := g.Push("origin", "polecat/fork-policy:polecat/fork-policy", false); err != nil {
		t.Fatalf("feature branch push should remain allowed: %v", err)
	}
}

// TestPushRemoteBranchExists_SplitURL is the core regression test for GH#3224:
// with a split fetch/push URL, RemoteBranchExists checks the fetch URL (upstream)
// while PushRemoteBranchExists checks the push URL (fork/bare repo).
func TestPushRemoteBranchExists_SplitURL(t *testing.T) {
	localDir, _, _, _ := initTestRepoWithSplitRemote(t)
	g := NewGit(localDir)

	// Create a feature branch and push to origin (goes to fork via push URL)
	if err := g.CreateBranch("polecat/fix-test"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/fix-test"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "fix.go"), []byte("package fix\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = localDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "fix commit")
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if err := g.Push("origin", "polecat/fix-test", false); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// RemoteBranchExists checks the fetch URL (upstream) — branch NOT there
	exists, err := g.RemoteBranchExists("origin", "polecat/fix-test")
	if err != nil {
		t.Fatalf("RemoteBranchExists: %v", err)
	}
	if exists {
		t.Error("RemoteBranchExists should return false — branch was pushed to fork, not upstream")
	}

	// PushRemoteBranchExists checks the push URL (fork) — branch IS there
	exists, err = g.PushRemoteBranchExists("origin", "polecat/fix-test")
	if err != nil {
		t.Fatalf("PushRemoteBranchExists: %v", err)
	}
	if !exists {
		t.Error("PushRemoteBranchExists should return true — branch was pushed to fork")
	}
}

func TestListPushRemoteRefsWithHashesUsesPushURLHash(t *testing.T) {
	localDir, upstream, _, mainBranch := initTestRepoWithSplitRemote(t)
	g := NewGit(localDir)
	branch := "polecat/split-classifier"

	if err := g.CreateBranch(branch); err != nil {
		t.Fatalf("CreateBranch upstream branch: %v", err)
	}
	if err := g.Checkout(branch); err != nil {
		t.Fatalf("Checkout upstream branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "upstream.go"), []byte("package upstream\n"), 0644); err != nil {
		t.Fatalf("write upstream: %v", err)
	}
	if err := g.Add("upstream.go"); err != nil {
		t.Fatalf("Add upstream: %v", err)
	}
	if err := g.Commit("upstream branch work"); err != nil {
		t.Fatalf("Commit upstream: %v", err)
	}
	upstreamSHA, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev upstream: %v", err)
	}
	runGit(t, localDir, "push", upstream, branch)
	if err := g.Checkout(mainBranch); err != nil {
		t.Fatalf("Checkout main: %v", err)
	}
	if err := g.Merge(branch); err != nil {
		t.Fatalf("Merge upstream branch: %v", err)
	}
	mainSHA, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev main: %v", err)
	}
	runGit(t, localDir, "push", upstream, mainBranch)
	runGit(t, localDir, "update-ref", "refs/remotes/origin/"+branch, upstreamSHA)
	runGit(t, localDir, "update-ref", "refs/remotes/origin/"+mainBranch, mainSHA)

	runGit(t, localDir, "checkout", "-B", branch, "origin/"+mainBranch)
	if err := os.WriteFile(filepath.Join(localDir, "fork.go"), []byte("package fork\n"), 0644); err != nil {
		t.Fatalf("write fork: %v", err)
	}
	if err := g.Add("fork.go"); err != nil {
		t.Fatalf("Add fork: %v", err)
	}
	if err := g.Commit("fork branch work"); err != nil {
		t.Fatalf("Commit fork: %v", err)
	}
	forkSHA, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev fork: %v", err)
	}
	if forkSHA == upstreamSHA {
		t.Fatal("expected fork branch commit to differ from upstream branch commit")
	}
	runGit(t, localDir, "push", "origin", branch)
	runGit(t, localDir, "update-ref", "refs/remotes/origin/"+branch, upstreamSHA)

	refs, err := g.ListPushRemoteRefsWithHashes("origin", "refs/heads/polecat/")
	if err != nil {
		t.Fatalf("ListPushRemoteRefsWithHashes: %v", err)
	}
	var found RemoteRef
	for _, ref := range refs {
		if ref.Name == "refs/heads/"+branch {
			found = ref
			break
		}
	}
	if found.Name == "" {
		t.Fatalf("remote ref %q not found in %#v", branch, refs)
	}
	if found.Hash != forkSHA {
		t.Fatalf("push remote ref hash = %q, want fork hash %q", found.Hash, forkSHA)
	}

	trackingMerged, err := g.IsAncestor("origin/"+branch, "origin/"+mainBranch)
	if err != nil {
		t.Fatalf("IsAncestor tracking ref: %v", err)
	}
	if !trackingMerged {
		t.Fatal("expected fetch-remote tracking branch to be merged")
	}
	hashMerged, err := g.IsAncestor(found.Hash, "origin/"+mainBranch)
	if err != nil {
		t.Fatalf("IsAncestor push hash: %v", err)
	}
	if hashMerged {
		t.Fatal("expected push remote hash to remain unmerged despite merged fetch tracking ref")
	}
}

func TestDeleteRemoteBranchIfAtRejectsChangedBranch(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	branch := "polecat/lease-test"
	if err := g.CreateBranch(branch); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout(branch); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "lease.txt"), []byte("old"), 0644); err != nil {
		t.Fatalf("write old: %v", err)
	}
	if err := g.Add("lease.txt"); err != nil {
		t.Fatalf("Add old: %v", err)
	}
	if err := g.Commit("lease old"); err != nil {
		t.Fatalf("Commit old: %v", err)
	}
	oldHash, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev old: %v", err)
	}
	if err := g.Push("origin", branch, false); err != nil {
		t.Fatalf("Push old: %v", err)
	}

	if err := os.WriteFile(filepath.Join(localDir, "lease.txt"), []byte("new"), 0644); err != nil {
		t.Fatalf("write new: %v", err)
	}
	if err := g.Add("lease.txt"); err != nil {
		t.Fatalf("Add new: %v", err)
	}
	if err := g.Commit("lease new"); err != nil {
		t.Fatalf("Commit new: %v", err)
	}
	newHash, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev new: %v", err)
	}
	if err := g.Push("origin", branch, false); err != nil {
		t.Fatalf("Push new: %v", err)
	}

	if err := g.DeleteRemoteBranchIfAt("origin", branch, oldHash); err == nil {
		t.Fatal("DeleteRemoteBranchIfAt should reject a branch that advanced")
	}
	exists, err := g.RemoteBranchExists("origin", branch)
	if err != nil {
		t.Fatalf("RemoteBranchExists after rejected delete: %v", err)
	}
	if !exists {
		t.Fatal("branch should still exist after rejected delete")
	}
	if err := g.DeleteRemoteBranchIfAt("origin", branch, newHash); err != nil {
		t.Fatalf("DeleteRemoteBranchIfAt current hash: %v", err)
	}
	exists, err = g.RemoteBranchExists("origin", branch)
	if err != nil {
		t.Fatalf("RemoteBranchExists after delete: %v", err)
	}
	if exists {
		t.Fatal("branch should be deleted when expected hash matches")
	}
	if err := g.Checkout(mainBranch); err != nil {
		t.Fatalf("Checkout main: %v", err)
	}
}

// TestPushRemoteBranchExists_NoPushURL verifies that PushRemoteBranchExists
// falls back to RemoteBranchExists when no custom push URL is configured.
func TestPushRemoteBranchExists_NoPushURL(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	// No custom push URL — PushRemoteBranchExists should behave like RemoteBranchExists
	exists, err := g.PushRemoteBranchExists("origin", mainBranch)
	if err != nil {
		t.Fatalf("PushRemoteBranchExists: %v", err)
	}
	if !exists {
		t.Errorf("PushRemoteBranchExists should find %s on origin (no split URL)", mainBranch)
	}

	// Nonexistent branch should return false
	exists, err = g.PushRemoteBranchExists("origin", "nonexistent-branch")
	if err != nil {
		t.Fatalf("PushRemoteBranchExists (nonexistent): %v", err)
	}
	if exists {
		t.Error("PushRemoteBranchExists should return false for nonexistent branch")
	}
}

func TestVerifyPushedCommit(t *testing.T) {
	localDir, _, _ := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	if err := g.CreateBranch("polecat/verified-push"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/verified-push"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "verified.txt"), []byte("v1\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := g.Add("verified.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("verified push v1"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	v1, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev v1: %v", err)
	}
	if err := g.Push("origin", "polecat/verified-push", false); err != nil {
		t.Fatalf("Push v1: %v", err)
	}
	if err := g.VerifyPushedCommit("origin", "polecat/verified-push", v1); err != nil {
		t.Fatalf("VerifyPushedCommit v1: %v", err)
	}

	if err := os.WriteFile(filepath.Join(localDir, "verified.txt"), []byte("v2\n"), 0644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	if err := g.Add("verified.txt"); err != nil {
		t.Fatalf("Add v2: %v", err)
	}
	if err := g.Commit("verified push v2"); err != nil {
		t.Fatalf("Commit v2: %v", err)
	}
	v2, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev v2: %v", err)
	}
	if err := g.VerifyPushedCommit("origin", "polecat/verified-push", v2); err == nil {
		t.Fatal("VerifyPushedCommit should fail when remote branch is stale")
	}
	if err := g.VerifyPushedCommit("origin", "polecat/missing", v2); err == nil {
		t.Fatal("VerifyPushedCommit should fail when remote branch is missing")
	}
}

func TestVerifyPushedCommitSplitURL(t *testing.T) {
	localDir, _, _, _ := initTestRepoWithSplitRemote(t)
	g := NewGit(localDir)

	if err := g.CreateBranch("polecat/verified-split"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/verified-split"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "split.txt"), []byte("split\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := g.Add("split.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("verified split push"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	sha, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("Rev: %v", err)
	}
	if err := g.Push("origin", "polecat/verified-split", false); err != nil {
		t.Fatalf("Push: %v", err)
	}

	fetchTip, err := g.RemoteBranchTip("origin", "polecat/verified-split")
	if err != nil {
		t.Fatalf("RemoteBranchTip: %v", err)
	}
	if fetchTip != "" {
		t.Fatalf("fetch remote should not have split push branch, got %s", fetchTip)
	}
	if err := g.VerifyPushedCommit("origin", "polecat/verified-split", sha); err != nil {
		t.Fatalf("VerifyPushedCommit should query push URL: %v", err)
	}
}

// TestBranchPushedToRemote_SplitURL verifies that BranchPushedToRemote correctly
// reports a branch as pushed when it exists on the push target (fork), even though
// it's absent from the fetch URL (upstream). This is the GH#3224 fix.
func TestBranchPushedToRemote_SplitURL(t *testing.T) {
	localDir, _, _, _ := initTestRepoWithSplitRemote(t)
	g := NewGit(localDir)

	// Create and push a feature branch (goes to fork via push URL)
	if err := g.CreateBranch("polecat/status-test"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/status-test"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "status.go"), []byte("package status\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = localDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "status commit")
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if err := g.Push("origin", "polecat/status-test", false); err != nil {
		t.Fatalf("Push: %v", err)
	}

	pushed, unpushed, err := g.BranchPushedToRemote("polecat/status-test", "origin")
	if err != nil {
		t.Fatalf("BranchPushedToRemote: %v", err)
	}
	if !pushed {
		t.Error("BranchPushedToRemote should report pushed=true (branch is on fork)")
	}
	if unpushed != 0 {
		t.Errorf("BranchPushedToRemote unpushed = %d, want 0", unpushed)
	}
}

func TestUnpushedCommitsPrefersExactRemoteBranchOverUpstream(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)
	branch := "polecat/already-pushed"

	if err := g.CreateBranch(branch); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout(branch); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "work.go"), []byte("package work\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := g.Add("work.go"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("polecat work"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := g.Push("origin", branch, false); err != nil {
		t.Fatalf("Push: %v", err)
	}
	runGit(t, localDir, "branch", "--set-upstream-to=origin/"+mainBranch, branch)

	unpushed, err := g.UnpushedCommits()
	if err != nil {
		t.Fatalf("UnpushedCommits: %v", err)
	}
	if unpushed != 0 {
		t.Fatalf("UnpushedCommits = %d, want 0 for pushed branch tracking origin/%s", unpushed, mainBranch)
	}

	status, err := g.CheckUncommittedWork()
	if err != nil {
		t.Fatalf("CheckUncommittedWork: %v", err)
	}
	if !status.Clean() {
		t.Fatalf("CheckUncommittedWork should be clean, got %s", status)
	}
}

// TestBranchPushedToRemote_NoPushURL verifies baseline behavior: when fetch and
// push URLs are the same, BranchPushedToRemote works normally.
func TestBranchPushedToRemote_NoPushURL(t *testing.T) {
	localDir, _, mainBranch := initTestRepoWithRemote(t)
	g := NewGit(localDir)

	// Main branch is pushed — should be reported as pushed
	pushed, unpushed, err := g.BranchPushedToRemote(mainBranch, "origin")
	if err != nil {
		t.Fatalf("BranchPushedToRemote: %v", err)
	}
	if !pushed {
		t.Error("BranchPushedToRemote should report pushed=true for main")
	}
	if unpushed != 0 {
		t.Errorf("BranchPushedToRemote unpushed = %d, want 0", unpushed)
	}

	// Unpushed branch — should report not pushed
	if err := g.CreateBranch("polecat/unpushed"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("polecat/unpushed"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "new.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = localDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "local only")
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	pushed, unpushed, err = g.BranchPushedToRemote("polecat/unpushed", "origin")
	if err != nil {
		t.Fatalf("BranchPushedToRemote: %v", err)
	}
	if pushed {
		t.Error("BranchPushedToRemote should report pushed=false for unpushed branch")
	}
	if unpushed < 1 {
		t.Errorf("BranchPushedToRemote unpushed = %d, want >= 1", unpushed)
	}
}
