//go:build integration

// Package cmd contains integration tests for beads db initialization after clone.
//
// Run with: go test -tags=integration ./internal/cmd -run TestBeadsDbInitAfterClone -v
//
// Bug: GitHub Issue #72
// When a repo with tracked .beads/ is added as a rig, the database doesn't exist
// (DB files are gitignored) and bd operations fail because no one runs `bd init`.
package cmd

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// getFreePort returns a free TCP port by binding to 127.0.0.1:0.
// Used so each subtest's gt install gets a unique --dolt-port and they
// don't collide on a shared default.
func getFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("getFreePort: failed to bind: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// extractJSON finds the first JSON object in output that may contain non-JSON warnings.
// bd --json -q can still emit warnings to stdout before the JSON payload.
func extractJSON(output []byte) []byte {
	idx := strings.Index(string(output), "{")
	if idx < 0 {
		return output
	}
	return output[idx:]
}

// createTrackedBeadsRepoWithIssues creates a git repo with .beads/ tracked that contains existing issues.
// This simulates a clone of a repo that has tracked beads with issues exported to issues.jsonl.
// The database files are NOT included (gitignored), so prefix must be detected from config.yaml.
func createTrackedBeadsRepoWithIssues(t *testing.T, path, prefix string, numIssues int) {
	t.Helper()

	// Create directory
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	// Initialize git repo with explicit main branch
	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = path
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Create initial file and commit (so we have something before beads)
	readmePath := filepath.Join(path, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Test Repo\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}

	commitCmds := [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", "Initial commit"},
	}
	for _, args := range commitCmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = path
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Initialize beads
	beadsDir := filepath.Join(path, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// Run bd init (pass --server for bd v1.0.0+ which defaults to embedded mode)
	bdInitArgs := []string{"init", "--prefix", prefix}
	if p := os.Getenv("GT_DOLT_PORT"); p != "" {
		bdInitArgs = append(bdInitArgs, "--server", "--server-port", p)
	}
	cmd := exec.Command("bd", bdInitArgs...)
	cmd.Dir = path
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init failed: %v\nOutput: %s", err, output)
	}

	// Create issues
	for i := 1; i <= numIssues; i++ {
		cmd = exec.Command("bd", "-q", "create",
			"--type", "task", "--title", fmt.Sprintf("Test issue %d", i))
		cmd.Dir = path
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bd create issue %d failed: %v\nOutput: %s", i, err, output)
		}
	}

	// Ensure .beads is committed (bd init may auto-commit in newer versions).
	cmd = exec.Command("git", "add", ".beads")
	cmd.Dir = path
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add .beads: %v\n%s", err, out)
	}

	// Only commit if there are staged changes (bd v0.56.1+ auto-commits).
	cmd = exec.Command("git", "diff", "--cached", "--quiet")
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		cmd = exec.Command("git", "commit", "-m", "Add beads with issues")
		cmd.Dir = path
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit beads: %v\n%s", err, out)
		}
	}

	// Remove database files to simulate what a clone would look like
	// (database files are gitignored, so cloned repos don't have them)
	removeDBFiles(t, beadsDir)
}

// TestBeadsDbInitAfterClone tests that when a tracked beads repo is added as a rig,
// the beads database is properly initialized even though database files don't exist.
func TestBeadsDbInitAfterClone(t *testing.T) {
	// Skip if bd is not available
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping test")
	}
	// Dolt server required: bd init auto-detects server on 3307,
	// and gt rig add --adopt uses --server mode for re-initialization.
	requireDoltServer(t)

	tmpDir := t.TempDir()
	configureTestGitIdentity(t, tmpDir)
	gtBinary := buildGT(t)

	t.Run("TrackedRepoWithExistingPrefix", func(t *testing.T) {
		// GitHub Issue #72: gt rig add --adopt should detect existing prefix and init database.
		// When a tracked beads repo has config.yaml with a prefix, adopt should detect it.

		townRoot := filepath.Join(tmpDir, "town-prefix-test")

		// Install town
		port := getFreePort(t)
		cmd := exec.Command(gtBinary, "install", townRoot, "--name", "prefix-test", "--dolt-port", strconv.Itoa(port))
		cmd.Env = append(os.Environ(), "HOME="+tmpDir)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("gt install failed: %v\nOutput: %s", err, output)
		}

		// Bridge test Dolt server PID so subsequent AddRig/IsRunning checks pass.
		bridgeDoltPidToTown(t, townRoot)

		// Create a repo with existing beads prefix "existing-prefix" AND issues
		// directly at the expected rig location
		rigDir := filepath.Join(townRoot, "myrig")
		createTrackedBeadsRepoWithIssues(t, rigDir, "existing-prefix", 3)

		// Add rig with --adopt --force (local repo has no git remote)
		// Pass --prefix to match the existing prefix
		cmd = exec.Command(gtBinary, "rig", "add", "myrig", "--adopt", "--force", "--prefix", "existing-prefix")
		cmd.Dir = townRoot
		cmd.Env = append(os.Environ(), "HOME="+tmpDir)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("gt rig add failed: %v\nOutput: %s", err, output)
		}

		// Verify routes.jsonl has the prefix
		routesContent, err := os.ReadFile(filepath.Join(townRoot, ".beads", "routes.jsonl"))
		if err != nil {
			t.Fatalf("read routes.jsonl: %v", err)
		}

		if !strings.Contains(string(routesContent), `"prefix":"existing-prefix-"`) {
			t.Errorf("routes.jsonl should contain existing-prefix-, got:\n%s", routesContent)
		}

		// NOW TRY TO USE bd - this is the key test for the bug
		// Without the fix, the database doesn't exist and bd operations fail
		cmd = exec.Command("bd", "--json", "-q", "create",
			"--type", "task", "--title", "test-from-rig")
		cmd.Dir = rigDir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd create failed (bug!): %v\nOutput: %s\n\nThis is the bug: database doesn't exist after clone because bd init was never run", err, output)
		}

		var result struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(extractJSON(output), &result); err != nil {
			t.Fatalf("parse output: %v", err)
		}

		if !strings.HasPrefix(result.ID, "existing-prefix-") {
			t.Errorf("expected existing-prefix- prefix, got %s", result.ID)
		}
	})

	t.Run("TrackedRepoWithNoIssuesRequiresPrefix", func(t *testing.T) {
		// Regression test: When a tracked beads repo has NO issues (fresh init),
		// gt rig add must use the --prefix flag since there's nothing to detect from.

		townRoot := filepath.Join(tmpDir, "town-no-issues")

		// Install town
		port := getFreePort(t)
		cmd := exec.Command(gtBinary, "install", townRoot, "--name", "no-issues-test", "--dolt-port", strconv.Itoa(port))
		cmd.Env = append(os.Environ(), "HOME="+tmpDir)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("gt install failed: %v\nOutput: %s", err, output)
		}

		// Bridge test Dolt server PID so subsequent AddRig/IsRunning checks pass.
		bridgeDoltPidToTown(t, townRoot)

		// Create a tracked beads repo with NO issues at the expected rig location
		rigDir := filepath.Join(townRoot, "emptyrig")
		createTrackedBeadsRepoWithNoIssues(t, rigDir, "empty-prefix")

		// Add rig WITH --prefix and --force (local repo has no git remote)
		cmd = exec.Command(gtBinary, "rig", "add", "emptyrig", "--adopt", "--force", "--prefix", "empty-prefix")
		cmd.Dir = townRoot
		cmd.Env = append(os.Environ(), "HOME="+tmpDir)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("gt rig add with --prefix failed: %v\nOutput: %s", err, output)
		}

		// Verify routes.jsonl has the prefix
		routesContent, err := os.ReadFile(filepath.Join(townRoot, ".beads", "routes.jsonl"))
		if err != nil {
			t.Fatalf("read routes.jsonl: %v", err)
		}

		if !strings.Contains(string(routesContent), `"prefix":"empty-prefix-"`) {
			t.Errorf("routes.jsonl should contain empty-prefix-, got:\n%s", routesContent)
		}

		// Verify bd operations work with the configured prefix
		cmd = exec.Command("bd", "--json", "-q", "create",
			"--type", "task", "--title", "test-from-empty-repo")
		cmd.Dir = rigDir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd create failed: %v\nOutput: %s", err, output)
		}

		var result struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(extractJSON(output), &result); err != nil {
			t.Fatalf("parse output: %v", err)
		}

		if !strings.HasPrefix(result.ID, "empty-prefix-") {
			t.Errorf("expected empty-prefix- prefix, got %s", result.ID)
		}
	})

	t.Run("TrackedRepoWithPrefixMismatchErrors", func(t *testing.T) {
		// Test that when --prefix is explicitly provided but doesn't match
		// the prefix detected from the database, gt rig add fails with an error.
		// Prefix detection uses config.yaml (not metadata.json), which survives
		// clones since it is tracked by git.

		townRoot := filepath.Join(tmpDir, "town-mismatch")

		// Install town
		port := getFreePort(t)
		cmd := exec.Command(gtBinary, "install", townRoot, "--name", "mismatch-test", "--dolt-port", strconv.Itoa(port))
		cmd.Env = append(os.Environ(), "HOME="+tmpDir)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("gt install failed: %v\nOutput: %s", err, output)
		}

		// Bridge test Dolt server PID so gt rig add can verify the prefix.
		bridgeDoltPidToTown(t, townRoot)

		// Create a repo with existing beads prefix "real-prefix" with issues
		rigDir := filepath.Join(townRoot, "mismatchrig")
		createTrackedBeadsRepoWithIssues(t, rigDir, "real-prefix", 2)

		// Add rig with WRONG --prefix - should fail
		cmd = exec.Command(gtBinary, "rig", "add", "mismatchrig", "--adopt", "--force", "--prefix", "wrong-prefix")
		cmd.Dir = townRoot
		cmd.Env = append(os.Environ(), "HOME="+tmpDir)
		output, err := cmd.CombinedOutput()

		// Should fail
		if err == nil {
			t.Fatalf("gt rig add should have failed with prefix mismatch, but succeeded.\nOutput: %s", output)
		}

		// Verify error message mentions the mismatch
		outputStr := string(output)
		if !strings.Contains(outputStr, "prefix mismatch") {
			t.Errorf("expected 'prefix mismatch' in error, got:\n%s", outputStr)
		}
		if !strings.Contains(outputStr, "real-prefix") {
			t.Errorf("expected 'real-prefix' (detected) in error, got:\n%s", outputStr)
		}
		if !strings.Contains(outputStr, "wrong-prefix") {
			t.Errorf("expected 'wrong-prefix' (provided) in error, got:\n%s", outputStr)
		}
	})

	t.Run("TrackedRepoWithNoIssuesFallsBackToDerivedPrefix", func(t *testing.T) {
		// Test the fallback behavior: when a tracked beads repo has NO issues
		// and NO --prefix is provided, gt rig add should derive prefix from rig name.

		townRoot := filepath.Join(tmpDir, "town-derived")

		// Install town
		port := getFreePort(t)
		cmd := exec.Command(gtBinary, "install", townRoot, "--name", "derived-test", "--dolt-port", strconv.Itoa(port))
		cmd.Env = append(os.Environ(), "HOME="+tmpDir)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("gt install failed: %v\nOutput: %s", err, output)
		}

		// Bridge test Dolt server PID so subsequent AddRig/IsRunning checks pass.
		bridgeDoltPidToTown(t, townRoot)

		// Create a tracked beads repo with NO issues at the expected rig location
		rigDir := filepath.Join(townRoot, "testrig")
		createTrackedBeadsRepoWithNoIssues(t, rigDir, "original-prefix")

		// Add rig WITHOUT --prefix - should derive from rig name "testrig"
		cmd = exec.Command(gtBinary, "rig", "add", "testrig", "--adopt", "--force")
		cmd.Dir = townRoot
		cmd.Env = append(os.Environ(), "HOME="+tmpDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("gt rig add (no --prefix) failed: %v\nOutput: %s", err, output)
		}

		// Verify bd operations work - the key test is that the database was initialized
		cmd = exec.Command("bd", "--json", "-q", "create",
			"--type", "task", "--title", "test-derived-prefix")
		cmd.Dir = rigDir
		output, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd create failed (database not initialized?): %v\nOutput: %s", err, output)
		}

		var result struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(extractJSON(output), &result); err != nil {
			t.Fatalf("parse output: %v", err)
		}

		// The ID should have SOME prefix (derived from "testrig")
		// We don't care exactly what it is, just that bd works
		if result.ID == "" {
			t.Error("expected non-empty issue ID")
		}
		t.Logf("Created issue with derived prefix: %s", result.ID)
	})

	t.Run("MissingMetadataTriggersReInit", func(t *testing.T) {
		// Exercises the rig.go:691 code path where metadata.json is missing
		// and gt rig add --adopt must re-initialize the database.
		// This simulates an edge case (e.g., legacy repo, manual deletion)
		// where dolt/ and metadata.json are absent despite .beads/ existing.

		townRoot := filepath.Join(tmpDir, "town-reinit")

		// Install town
		port := getFreePort(t)
		cmd := exec.Command(gtBinary, "install", townRoot, "--name", "reinit-test", "--dolt-port", strconv.Itoa(port))
		cmd.Env = append(os.Environ(), "HOME="+tmpDir)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("gt install failed: %v\nOutput: %s", err, output)
		}

		// Bridge test Dolt server PID so subsequent AddRig/IsRunning checks pass.
		bridgeDoltPidToTown(t, townRoot)

		// Create a tracked beads repo with issues
		rigDir := filepath.Join(townRoot, "reinitrig")
		createTrackedBeadsRepoWithIssues(t, rigDir, "reinit-prefix", 2)

		// Forcibly remove metadata.json and dolt/ to simulate missing DB state.
		// This forces the rig.go initialization branch (metadata.json check).
		beadsDir := filepath.Join(rigDir, ".beads")
		os.Remove(filepath.Join(beadsDir, "metadata.json"))
		os.RemoveAll(filepath.Join(beadsDir, "dolt"))

		// Verify metadata.json is actually gone
		if _, err := os.Stat(filepath.Join(beadsDir, "metadata.json")); !os.IsNotExist(err) {
			t.Fatalf("expected metadata.json to be removed, but stat returned: %v", err)
		}

		// Add rig with --adopt --force
		cmd = exec.Command(gtBinary, "rig", "add", "reinitrig", "--adopt", "--force", "--prefix", "reinit-prefix")
		cmd.Dir = townRoot
		cmd.Env = append(os.Environ(), "HOME="+tmpDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("gt rig add failed: %v\nOutput: %s", err, output)
		}

		// Verify the re-init path was triggered: adopt output should confirm init
		if !strings.Contains(string(output), "Initialized beads database") {
			t.Fatalf("expected 'Initialized beads database' in adopt output, got:\n%s", output)
		}

		// Verify the database artifacts were recreated
		metadataPath := filepath.Join(beadsDir, "metadata.json")
		if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
			t.Fatal("metadata.json was not recreated by rig add --adopt")
		}
		doltDir := filepath.Join(beadsDir, "dolt")
		if _, err := os.Stat(doltDir); os.IsNotExist(err) {
			t.Fatal("dolt/ directory was not recreated by rig add --adopt")
		}

		t.Logf("Re-init path verified: metadata.json and dolt/ recreated after adopt")
		// NOTE: We don't test bd create here because rig.go inits with --server mode,
		// which requires a running dolt sql-server for runtime access. The init itself
		// is verified by checking that metadata.json and dolt/ were recreated.
	})
}

// createTrackedBeadsRepoWithNoIssues creates a git repo with .beads/ tracked but NO issues.
// This simulates a fresh bd init that was committed before any issues were created.
func createTrackedBeadsRepoWithNoIssues(t *testing.T, path, prefix string) {
	t.Helper()

	// Create directory
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	// Initialize git repo with explicit main branch
	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = path
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Create initial file and commit
	readmePath := filepath.Join(path, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Test Repo\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}

	commitCmds := [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", "Initial commit"},
	}
	for _, args := range commitCmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = path
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Initialize beads
	beadsDir := filepath.Join(path, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// Run bd init (creates database but no issues; pass --server for bd v1.0.0+)
	bdInitArgs2 := []string{"init", "--prefix", prefix}
	if p := os.Getenv("GT_DOLT_PORT"); p != "" {
		bdInitArgs2 = append(bdInitArgs2, "--server", "--server-port", p)
	}
	cmd := exec.Command("bd", bdInitArgs2...)
	cmd.Dir = path
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init failed: %v\nOutput: %s", err, output)
	}

	// Ensure .beads is committed (bd init may auto-commit in newer versions).
	cmd = exec.Command("git", "add", ".beads")
	cmd.Dir = path
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add .beads: %v\n%s", err, out)
	}

	// Only commit if there are staged changes (bd v0.56.1+ auto-commits).
	cmd = exec.Command("git", "diff", "--cached", "--quiet")
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		cmd = exec.Command("git", "commit", "-m", "Add beads (no issues)")
		cmd.Dir = path
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit beads: %v\n%s", err, out)
		}
	}

	// Remove database files to simulate what a clone would look like
	removeDBFiles(t, beadsDir)
}

// removeDBFiles removes gitignored database files from a beads directory to simulate a clone.
// Only removes files that match patterns in .beads/.gitignore. Files NOT in .gitignore
// (metadata.json, config.yaml, issues.jsonl, etc.) are tracked by git and survive clones.
//
// Anchored to .beads/.gitignore patterns as of bd v0.56.1: *.db, *.db-*, daemon.*, bd.sock,
// sync-state.json, redirect, db.sqlite, bd.db, export-state/, dolt/, dolt-access.lock,
// interactions.jsonl. Note: metadata.json is tracked (not gitignored).
func removeDBFiles(t *testing.T, beadsDir string) {
	t.Helper()

	// Remove gitignored SQLite and legacy database files
	patterns := []string{"*.db", "*.db-wal", "*.db-shm", "*.db-journal", "db.sqlite", "bd.db"}
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(filepath.Join(beadsDir, pattern))
		for _, m := range matches {
			os.Remove(m)
		}
	}
	// Remove gitignored runtime state files (metadata.json is tracked, not removed)
	for _, name := range []string{"sync-state.json", "redirect", ".local_version", "dolt-access.lock", "interactions.jsonl"} {
		os.Remove(filepath.Join(beadsDir, name))
	}
	os.RemoveAll(filepath.Join(beadsDir, "export-state"))
	// Remove Dolt database directory (gitignored since bd v0.50+; managed by Dolt remotes, not git)
	os.RemoveAll(filepath.Join(beadsDir, "dolt"))
}
