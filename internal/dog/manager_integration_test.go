package dog

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
)

// =============================================================================
// Integration Test Helpers
// =============================================================================

// skipIfNoGit skips the test if git is not available.
func skipIfNoGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available, skipping integration test")
	}
}

// testTownWithGitRigs creates a complete test town with git repositories.
// Sets up bare repos and mayor worktrees to simulate real Gas Town structure.
func testTownWithGitRigs(t *testing.T) (*Manager, string) {
	t.Helper()
	skipIfNoGit(t)

	tmpDir := t.TempDir()

	// Create rigs config
	rigsConfig := &config.RigsConfig{
		Version: 1,
		Rigs: map[string]config.RigEntry{
			"testrig": {GitURL: "local://testrig"},
		},
	}

	// Set up rig structure with bare repo
	rigPath := filepath.Join(tmpDir, "testrig")
	bareRepoPath := filepath.Join(rigPath, ".repo.git")

	// Initialize bare repo
	if err := os.MkdirAll(bareRepoPath, 0755); err != nil {
		t.Fatalf("Failed to create bare repo dir: %v", err)
	}

	// git init --bare
	cmd := exec.Command("git", "init", "--bare", bareRepoPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to init bare repo: %v\n%s", err, out)
	}

	// Create mayor/rig worktree with initial commit
	mayorPath := filepath.Join(rigPath, "mayor", "rig")
	if err := os.MkdirAll(mayorPath, 0755); err != nil {
		t.Fatalf("Failed to create mayor dir: %v", err)
	}

	// Initialize mayor/rig as a regular repo
	cmd = exec.Command("git", "init", mayorPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to init mayor repo: %v\n%s", err, out)
	}

	// Configure git user for commits
	cmd = exec.Command("git", "-C", mayorPath, "config", "user.email", "test@test.com")
	cmd.Run()
	cmd = exec.Command("git", "-C", mayorPath, "config", "user.name", "Test")
	cmd.Run()

	// Create initial commit in mayor/rig
	readmePath := filepath.Join(mayorPath, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Test Rig\n"), 0644); err != nil {
		t.Fatalf("Failed to write README: %v", err)
	}
	cmd = exec.Command("git", "-C", mayorPath, "add", ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "-C", mayorPath, "commit", "-m", "Initial commit")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to git commit: %v\n%s", err, out)
	}

	// Set up bare repo with proper remote configuration
	// Add mayor/rig as a remote to bare repo and fetch
	cmd = gitBareCommand(bareRepoPath, "remote", "add", "origin", mayorPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to add remote: %v\n%s", err, out)
	}
	cmd = gitBareCommand(bareRepoPath, "fetch", "origin")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to fetch: %v\n%s", err, out)
	}
	// Create refs/remotes/origin/main so worktrees can use origin/main
	cmd = gitBareCommand(bareRepoPath, "branch", "main", "FETCH_HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to create main branch: %v\n%s", err, out)
	}
	cmd = gitBareCommand(bareRepoPath, "update-ref", "refs/remotes/origin/main", "FETCH_HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to update origin/main ref: %v\n%s", err, out)
	}

	m := NewManager(tmpDir, rigsConfig)
	return m, tmpDir
}

func gitBareCommand(bareRepoPath string, args ...string) *exec.Cmd {
	cmdArgs := append([]string{"-C", bareRepoPath, "-c", "safe.bareRepository=all"}, args...)
	return exec.Command("git", cmdArgs...)
}

// =============================================================================
// Add (Spawn) Integration Tests
// =============================================================================

func TestManager_Add_Integration_CreatesWorktrees(t *testing.T) {
	m, tmpDir := testTownWithGitRigs(t)

	dog, err := m.Add("alpha")
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Verify dog was created
	if dog.Name != "alpha" {
		t.Errorf("Dog.Name = %q, want 'alpha'", dog.Name)
	}
	if dog.State != StateIdle {
		t.Errorf("Dog.State = %q, want StateIdle", dog.State)
	}

	// Verify dog directory exists
	dogPath := filepath.Join(tmpDir, "deacon", "dogs", "alpha")
	if _, err := os.Stat(dogPath); os.IsNotExist(err) {
		t.Error("Dog directory was not created")
	}

	// Verify state file exists
	statePath := filepath.Join(dogPath, ".dog.json")
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		t.Error("State file was not created")
	}

	// Verify worktree was created for testrig
	if worktreePath, ok := dog.Worktrees["testrig"]; ok {
		if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
			t.Errorf("Worktree path %s does not exist", worktreePath)
		}

		// Verify it's a valid git worktree
		cmd := exec.Command("git", "-C", worktreePath, "status")
		if err := cmd.Run(); err != nil {
			t.Errorf("Worktree is not a valid git repo: %v", err)
		}
	} else {
		t.Error("Worktrees map missing 'testrig' entry")
	}

	// Verify timestamps are set
	if dog.CreatedAt.IsZero() {
		t.Error("CreatedAt was not set")
	}
	if dog.LastActive.IsZero() {
		t.Error("LastActive was not set")
	}
}

func TestManager_Add_Integration_SetsUpBranch(t *testing.T) {
	m, _ := testTownWithGitRigs(t)

	dog, err := m.Add("bravo")
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Verify branch was created with correct naming pattern
	worktreePath := dog.Worktrees["testrig"]
	cmd := exec.Command("git", "-C", worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get branch name: %v", err)
	}

	branch := string(out)
	// Branch should be dog/<name>-<rig>-<timestamp>
	if len(branch) < 10 || branch[:4] != "dog/" {
		t.Errorf("Branch name %q doesn't match expected pattern dog/<name>-<rig>-<timestamp>", branch)
	}
}

func TestManager_Add_Integration_CanAddMultipleDogs(t *testing.T) {
	m, _ := testTownWithGitRigs(t)

	names := []string{"alpha", "beta", "gamma"}
	dogs := make([]*Dog, 0, len(names))

	for _, name := range names {
		dog, err := m.Add(name)
		if err != nil {
			t.Fatalf("Add(%q) error = %v", name, err)
		}
		dogs = append(dogs, dog)
	}

	// Verify all dogs exist
	listed, err := m.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(listed) != len(names) {
		t.Errorf("List() returned %d dogs, want %d", len(listed), len(names))
	}

	// Verify each dog has unique worktree paths
	paths := make(map[string]string)
	for _, dog := range dogs {
		for rig, path := range dog.Worktrees {
			key := rig + ":" + path
			if existing, ok := paths[key]; ok {
				t.Errorf("Duplicate worktree path: %s used by both %s and %s", path, existing, dog.Name)
			}
			paths[key] = dog.Name
		}
	}
}

// =============================================================================
// Remove (Kill) Integration Tests
// =============================================================================

func TestManager_Remove_Integration_CleansUpWorktrees(t *testing.T) {
	m, tmpDir := testTownWithGitRigs(t)

	// First add a dog
	dog, err := m.Add("doomed")
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	dogPath := filepath.Join(tmpDir, "deacon", "dogs", "doomed")
	worktreePath := dog.Worktrees["testrig"]

	// Verify dog and worktree exist
	if _, err := os.Stat(dogPath); os.IsNotExist(err) {
		t.Fatal("Dog directory should exist before Remove")
	}
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Fatal("Worktree should exist before Remove")
	}

	// Remove the dog
	if err := m.Remove("doomed"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	// Verify dog directory was cleaned up
	if _, err := os.Stat(dogPath); !os.IsNotExist(err) {
		t.Error("Dog directory should not exist after Remove")
	}

	// Verify worktree was removed (directory gone or not a git repo)
	if _, err := os.Stat(worktreePath); err == nil {
		// Directory still exists - check if it's still a git repo
		cmd := exec.Command("git", "-C", worktreePath, "status")
		if err := cmd.Run(); err == nil {
			t.Error("Worktree should be removed or invalid after Remove")
		}
	}
}

func TestManager_Remove_Integration_DoesNotAffectOtherDogs(t *testing.T) {
	m, _ := testTownWithGitRigs(t)

	// Add two dogs
	dog1, err := m.Add("survivor")
	if err != nil {
		t.Fatalf("Add(survivor) error = %v", err)
	}
	_, err = m.Add("victim")
	if err != nil {
		t.Fatalf("Add(victim) error = %v", err)
	}

	// Remove victim
	if err := m.Remove("victim"); err != nil {
		t.Fatalf("Remove(victim) error = %v", err)
	}

	// Verify survivor still works
	survivor, err := m.Get("survivor")
	if err != nil {
		t.Fatalf("Get(survivor) error = %v after removing victim", err)
	}
	if survivor.Name != "survivor" {
		t.Errorf("Survivor name changed to %q", survivor.Name)
	}

	// Verify survivor's worktree still works
	worktreePath := dog1.Worktrees["testrig"]
	cmd := exec.Command("git", "-C", worktreePath, "status")
	if err := cmd.Run(); err != nil {
		t.Errorf("Survivor's worktree is broken after removing another dog: %v", err)
	}
}

// =============================================================================
// Full Lifecycle Integration Tests
// =============================================================================

func TestManager_Integration_FullLifecycle(t *testing.T) {
	m, _ := testTownWithGitRigs(t)

	// 1. Add (spawn)
	dog, err := m.Add("lifecycle")
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if dog.State != StateIdle {
		t.Errorf("Initial state = %q, want StateIdle", dog.State)
	}

	// 2. Assign work (working state)
	if err := m.AssignWork("lifecycle", "task-123"); err != nil {
		t.Fatalf("AssignWork() error = %v", err)
	}
	dog, _ = m.Get("lifecycle")
	if dog.State != StateWorking {
		t.Errorf("After AssignWork: state = %q, want StateWorking", dog.State)
	}
	if dog.Work != "task-123" {
		t.Errorf("After AssignWork: work = %q, want 'task-123'", dog.Work)
	}

	// 3. Clear work (back to idle)
	if err := m.ClearWork("lifecycle"); err != nil {
		t.Fatalf("ClearWork() error = %v", err)
	}
	dog, _ = m.Get("lifecycle")
	if dog.State != StateIdle {
		t.Errorf("After ClearWork: state = %q, want StateIdle", dog.State)
	}

	// 4. Remove (kill)
	if err := m.Remove("lifecycle"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	_, err = m.Get("lifecycle")
	if err != ErrDogNotFound {
		t.Errorf("After Remove: Get() error = %v, want ErrDogNotFound", err)
	}
}

func TestManager_Integration_ConcurrentStateChanges(t *testing.T) {
	m, _ := testTownWithGitRigs(t)

	_, err := m.Add("concurrent")
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Rapid state changes should not corrupt state
	for i := 0; i < 10; i++ {
		if err := m.AssignWork("concurrent", "task"); err != nil {
			t.Fatalf("AssignWork iteration %d error = %v", i, err)
		}
		if err := m.ClearWork("concurrent"); err != nil {
			t.Fatalf("ClearWork iteration %d error = %v", i, err)
		}
	}

	// Final state should be consistent
	dog, err := m.Get("concurrent")
	if err != nil {
		t.Fatalf("Final Get() error = %v", err)
	}
	if dog.State != StateIdle {
		t.Errorf("Final state = %q, want StateIdle", dog.State)
	}
	if dog.Work != "" {
		t.Errorf("Final work = %q, want empty", dog.Work)
	}
}

// =============================================================================
// Refresh Integration Tests
// =============================================================================

func TestManager_Refresh_Integration_RecreatesWorktrees(t *testing.T) {
	m, _ := testTownWithGitRigs(t)

	dog, err := m.Add("refresh-test")
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	oldWorktreePath := dog.Worktrees["testrig"]
	oldBranch := ""

	// Get old branch name
	cmd := exec.Command("git", "-C", oldWorktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	if out, err := cmd.Output(); err == nil {
		oldBranch = string(out)
	}

	// Small delay to ensure timestamp-based branch names differ
	time.Sleep(10 * time.Millisecond)

	// Refresh
	if err := m.Refresh("refresh-test"); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	// Get updated dog
	dog, err = m.Get("refresh-test")
	if err != nil {
		t.Fatalf("Get() after Refresh error = %v", err)
	}

	newWorktreePath := dog.Worktrees["testrig"]

	// Verify new worktree exists
	if _, err := os.Stat(newWorktreePath); os.IsNotExist(err) {
		t.Error("New worktree should exist after Refresh")
	}

	// Verify it's a valid git worktree
	cmd = exec.Command("git", "-C", newWorktreePath, "status")
	if err := cmd.Run(); err != nil {
		t.Errorf("New worktree is not a valid git repo: %v", err)
	}

	// Branch should be different (new timestamp)
	cmd = exec.Command("git", "-C", newWorktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	if out, err := cmd.Output(); err == nil {
		newBranch := string(out)
		if newBranch == oldBranch {
			t.Log("Note: Branch names are the same (timestamps may have collided)")
		}
	}
}

// =============================================================================
// RefreshRig Integration Tests
// =============================================================================

func TestManager_RefreshRig_Integration_RecreatesSingleWorktree(t *testing.T) {
	m, _ := testTownWithGitRigs(t)

	dog, err := m.Add("refreshrig-test")
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	oldWorktreePath := dog.Worktrees["testrig"]

	// Small delay to ensure timestamp-based branch names differ
	time.Sleep(10 * time.Millisecond)

	// RefreshRig just for testrig
	if err := m.RefreshRig("refreshrig-test", "testrig"); err != nil {
		t.Fatalf("RefreshRig() error = %v", err)
	}

	// Get updated dog
	dog, err = m.Get("refreshrig-test")
	if err != nil {
		t.Fatalf("Get() after RefreshRig error = %v", err)
	}

	newWorktreePath := dog.Worktrees["testrig"]

	// Verify new worktree exists and is valid
	if _, err := os.Stat(newWorktreePath); os.IsNotExist(err) {
		t.Error("New worktree should exist after RefreshRig")
	}

	cmd := exec.Command("git", "-C", newWorktreePath, "status")
	if err := cmd.Run(); err != nil {
		t.Errorf("New worktree is not a valid git repo: %v", err)
	}

	// Verify old worktree path is either gone or no longer valid
	if oldWorktreePath != newWorktreePath {
		// Paths differ - old should be cleaned up
		if _, err := os.Stat(oldWorktreePath); err == nil {
			cmd = exec.Command("git", "-C", oldWorktreePath, "status")
			if cmd.Run() == nil {
				t.Log("Note: Old worktree still exists (may be same path with new branch)")
			}
		}
	}
}

// =============================================================================
// CleanupStaleBranches Integration Tests
// =============================================================================

func TestManager_CleanupStaleBranches_Integration_NoStaleBranches(t *testing.T) {
	m, _ := testTownWithGitRigs(t)

	// Add a dog (creates branches)
	_, err := m.Add("active-dog")
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Cleanup should find no stale branches (dog is still active)
	deleted, err := m.CleanupStaleBranches()
	if err != nil {
		t.Fatalf("CleanupStaleBranches() error = %v", err)
	}

	if deleted != 0 {
		t.Errorf("CleanupStaleBranches() deleted %d branches, want 0 (no stale)", deleted)
	}
}

func TestManager_CleanupStaleBranches_Integration_EmptyKennel(t *testing.T) {
	m, _ := testTownWithGitRigs(t)

	// No dogs added - should be a no-op
	deleted, err := m.CleanupStaleBranches()
	if err != nil {
		t.Fatalf("CleanupStaleBranches() error = %v", err)
	}

	if deleted != 0 {
		t.Errorf("CleanupStaleBranches() deleted %d branches with empty kennel, want 0", deleted)
	}
}

// =============================================================================
// Error Recovery Integration Tests
// =============================================================================

func TestManager_Integration_RecoveryFromPartialState(t *testing.T) {
	m, tmpDir := testTownWithGitRigs(t)

	// Add a dog normally
	_, err := m.Add("partial")
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Manually corrupt the worktree (simulate crash during creation)
	dogPath := filepath.Join(tmpDir, "deacon", "dogs", "partial")
	worktreePath := filepath.Join(dogPath, "testrig")

	// Delete the worktree directory but keep state file
	if err := os.RemoveAll(worktreePath); err != nil {
		t.Fatalf("Failed to remove worktree: %v", err)
	}

	// Dog should still be retrievable
	_, err = m.Get("partial")
	if err != nil {
		t.Fatalf("Get() error after corruption = %v", err)
	}

	// State management should still work
	if err := m.SetState("partial", StateWorking); err != nil {
		t.Errorf("SetState() error after corruption = %v", err)
	}

	// Remove should succeed and clean up remaining state
	if err := m.Remove("partial"); err != nil {
		t.Fatalf("Remove() error after corruption = %v", err)
	}

	// Verify complete cleanup
	if _, err := os.Stat(dogPath); !os.IsNotExist(err) {
		t.Error("Dog directory should be fully removed")
	}
}
