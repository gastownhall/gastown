package polecat

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/testutil"
	"github.com/steveyegge/gastown/internal/tmux"
)

// installMockBd places a fake bd binary in PATH that handles the commands
// needed by AddWithOptions (init, create, show, config, update, slot, etc.).
// This allows polecat tests to run without a real bd installation.
//
// On Windows, uses a .cmd→PowerShell wrapper (batch echo mangles JSON quotes).
// Pattern borrowed from internal/cmd/rig_integration_test.go:mockBdCommand.
func installMockBd(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()

	if runtime.GOOS == "windows" {
		psPath := filepath.Join(binDir, "bd.ps1")
		psScript := `# Mock bd for polecat tests (PowerShell)
$cmd = ''
foreach ($arg in $args) {
  if ($arg -like '--*') { continue }
  $cmd = $arg
  break
}
switch ($cmd) {
  'init'   { exit 0 }
  'config' { exit 0 }
  'create' {
    $beadId = 'mock-1'
    foreach ($arg in $args) {
      if ($arg -like '--id=*') { $beadId = $arg.Substring(5) }
    }
    Write-Output ("{""id"":""" + $beadId + """,""status"":""open"",""created_at"":""2025-01-01T00:00:00Z""}")
    exit 0
  }
  'show' {
    Write-Error '{"error":"not found"}'
    exit 1
  }
  default { exit 0 }
}
`
		cmdScript := "@echo off\r\npwsh -NoProfile -NoLogo -File \"" + psPath + "\" %*\r\n"
		if err := os.WriteFile(psPath, []byte(psScript), 0644); err != nil {
			t.Fatalf("write mock bd.ps1: %v", err)
		}
		if err := os.WriteFile(filepath.Join(binDir, "bd.cmd"), []byte(cmdScript), 0644); err != nil {
			t.Fatalf("write mock bd.cmd: %v", err)
		}
	} else {
		script := `#!/bin/sh
# Mock bd for polecat tests.
# Find the actual command (skip global flags like --allow-stale).
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;; # skip flags
    *) cmd="$arg"; break ;;
  esac
done
case "$cmd" in
  init|config|update|slot|reopen|migrate)
    exit 0
    ;;
  create)
    bead_id="mock-1"
    for arg in "$@"; do
      case "$arg" in
        --id=*) bead_id="${arg#--id=}" ;;
      esac
    done
    echo "{\"id\":\"$bead_id\",\"status\":\"open\",\"created_at\":\"2025-01-01T00:00:00Z\"}"
    exit 0
    ;;
  show)
    echo '{"error":"not found"}' >&2
    exit 1
    ;;
  *)
    exit 0
    ;;
esac
`
		if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
			t.Fatalf("write mock bd: %v", err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func setupCanonicalBranchManagerTest(t *testing.T) (*Manager, string) {
	t.Helper()
	installMockBd(t)

	root := t.TempDir()
	mayorRig := filepath.Join(root, "mayor", "rig")
	if err := os.MkdirAll(mayorRig, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	rigBeads := filepath.Join(root, ".beads")
	if err := os.MkdirAll(rigBeads, 0755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}
	mayorBeads := filepath.Join(mayorRig, ".beads")
	if err := os.MkdirAll(mayorBeads, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig/.beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigBeads, "redirect"), []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}

	cmd := exec.Command("git", "init", "-b", "main")
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	readmePath := filepath.Join(mayorRig, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	mayorGit := git.NewGit(mayorRig)
	if err := mayorGit.Add("README.md"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := mayorGit.Commit("Initial commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	cmd = exec.Command("git", "remote", "add", "origin", mayorRig)
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "update-ref", "refs/remotes/origin/main", "HEAD")
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git update-ref: %v\n%s", err, out)
	}

	r := &rig.Rig{Name: "rig", Path: root}
	return NewManager(r, git.NewGit(root), nil), mayorRig
}

func createStalePolecatCommit(t *testing.T, repoPath, startPoint, branchName string) string {
	t.Helper()

	repoGit := git.NewGit(repoPath)
	if err := repoGit.CheckoutNewBranch(branchName, startPoint); err != nil {
		t.Fatalf("checkout stale branch %s from %s: %v", branchName, startPoint, err)
	}

	fileName := strings.NewReplacer("/", "-", "@", "-").Replace(branchName) + ".txt"
	if err := os.WriteFile(filepath.Join(repoPath, fileName), []byte(branchName+"\n"), 0644); err != nil {
		t.Fatalf("write stale branch marker: %v", err)
	}
	if err := repoGit.Add(fileName); err != nil {
		t.Fatalf("git add stale branch marker: %v", err)
	}
	if err := repoGit.Commit("Create stale polecat branch"); err != nil {
		t.Fatalf("git commit stale branch marker: %v", err)
	}

	sha, err := repoGit.Rev("HEAD")
	if err != nil {
		t.Fatalf("resolve stale branch commit: %v", err)
	}
	return sha
}

func TestStateIsWorking(t *testing.T) {
	tests := []struct {
		state   State
		working bool
	}{
		{StateWorking, true},
		{StateDone, false},
		{StateStuck, false},
	}

	for _, tt := range tests {
		if got := tt.state.IsWorking(); got != tt.working {
			t.Errorf("%s.IsWorking() = %v, want %v", tt.state, got, tt.working)
		}
	}
}

func TestPolecatSummary(t *testing.T) {
	p := &Polecat{
		Name:  "Toast",
		State: StateWorking,
		Issue: "gt-abc",
	}

	summary := p.Summary()
	if summary.Name != "Toast" {
		t.Errorf("Name = %q, want Toast", summary.Name)
	}
	if summary.State != StateWorking {
		t.Errorf("State = %v, want StateWorking", summary.State)
	}
	if summary.Issue != "gt-abc" {
		t.Errorf("Issue = %q, want gt-abc", summary.Issue)
	}
}

func TestListEmpty(t *testing.T) {
	root := t.TempDir()
	r := &rig.Rig{
		Name: "test-rig",
		Path: root,
	}
	m := NewManager(r, git.NewGit(root), nil)

	polecats, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(polecats) != 0 {
		t.Errorf("polecats count = %d, want 0", len(polecats))
	}
}

func TestGetNotFound(t *testing.T) {
	root := t.TempDir()
	r := &rig.Rig{
		Name: "test-rig",
		Path: root,
	}
	m := NewManager(r, git.NewGit(root), nil)

	_, err := m.Get("nonexistent")
	if err != ErrPolecatNotFound {
		t.Errorf("Get = %v, want ErrPolecatNotFound", err)
	}
}

func TestRemoveNotFound(t *testing.T) {
	root := t.TempDir()
	r := &rig.Rig{
		Name: "test-rig",
		Path: root,
	}
	m := NewManager(r, git.NewGit(root), nil)

	err := m.Remove("nonexistent", false)
	if err != ErrPolecatNotFound {
		t.Errorf("Remove = %v, want ErrPolecatNotFound", err)
	}
}

func TestPolecatDir(t *testing.T) {
	r := &rig.Rig{
		Name: "test-rig",
		Path: "/home/user/ai/test-rig",
	}
	m := NewManager(r, git.NewGit(r.Path), nil)

	dir := m.polecatDir("Toast")
	expected := "/home/user/ai/test-rig/polecats/Toast"
	if filepath.ToSlash(dir) != expected {
		t.Errorf("polecatDir = %q, want %q", dir, expected)
	}
}

func TestAssigneeID(t *testing.T) {
	r := &rig.Rig{
		Name: "test-rig",
		Path: "/home/user/ai/test-rig",
	}
	m := NewManager(r, git.NewGit(r.Path), nil)

	id := m.assigneeID("Toast")
	expected := "test-rig/polecats/Toast"
	if id != expected {
		t.Errorf("assigneeID = %q, want %q", id, expected)
	}
}

// TestAgentBeadID_Deterministic verifies that agentBeadID returns the same string
// on repeated calls regardless of process working directory. Regression test for
// gt-lph: the old implementation called workspace.Find on each invocation, which
// could resolve differently depending on cwd, causing non-deterministic IDs across
// Manager instances for the same rig path.
func TestAgentBeadID_Deterministic(t *testing.T) {
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "myrig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}

	r := &rig.Rig{
		Name: "myrig",
		Path: rigPath,
	}

	// Construct two Managers from the same rig path — they must produce
	// identical agentBeadIDs regardless of construction context.
	m1 := NewManager(r, git.NewGit(rigPath), nil)
	m2 := NewManager(r, git.NewGit(rigPath), nil)

	id1a := m1.agentBeadID("Toast")
	id1b := m1.agentBeadID("Toast")
	id2 := m2.agentBeadID("Toast")

	// Same Manager, repeated calls — must be identical.
	if id1a != id1b {
		t.Errorf("agentBeadID not stable across calls: %q vs %q", id1a, id1b)
	}

	// Different Manager, same rig — must be identical.
	if id1a != id2 {
		t.Errorf("agentBeadID differs across Managers for same rig: %q vs %q", id1a, id2)
	}

	// Verify the ID is non-empty and contains expected components.
	if id1a == "" {
		t.Fatal("agentBeadID returned empty string")
	}

	// Change process working directory and construct a third Manager —
	// the ID must still match (the old bug: workspace.Find resolved
	// differently from different cwds).
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("Chdir to townRoot: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	m3 := NewManager(r, git.NewGit(rigPath), nil)
	id3 := m3.agentBeadID("Toast")
	if id1a != id3 {
		t.Errorf("agentBeadID differs after cwd change: %q (original) vs %q (after chdir)", id1a, id3)
	}
}

func TestNewManager_NamepoolFromRigConfig(t *testing.T) {
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "myrig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatal(err)
	}

	// Write rig config.json with polecat_names (no settings/config.json)
	rigConfig := `{"polecat_names": ["alpha", "beta", "gamma"]}`
	if err := os.WriteFile(filepath.Join(rigPath, "config.json"), []byte(rigConfig), 0644); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{Name: "myrig", Path: rigPath}
	m := NewManager(r, git.NewGit(rigPath), nil)
	pool := m.GetNamePool()

	name, err := pool.Allocate()
	if err != nil {
		t.Fatalf("Allocate error: %v", err)
	}
	if name != "alpha" {
		t.Errorf("expected first name from rig config (alpha), got %q", name)
	}
}

// Note: State persistence tests removed - state is now derived from beads assignee field.
// Integration tests should verify beads-based state management.

func TestGetReturnsWorkingWithoutBeads(t *testing.T) {
	// When beads is not available, Get should return StateWorking
	// (assume the polecat is doing something if it exists)
	//
	// Skip if bd is installed - the test assumes bd is unavailable, but when bd
	// is present it queries beads and returns actual state instead of defaulting.
	if _, err := exec.LookPath("bd"); err == nil {
		t.Skip("skipping: bd is installed, test requires bd to be unavailable")
	}

	root := t.TempDir()
	polecatDir := filepath.Join(root, "polecats", "Test")
	if err := os.MkdirAll(polecatDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create mayor/rig directory for beads (but no actual beads)
	mayorRigDir := filepath.Join(root, "mayor", "rig")
	if err := os.MkdirAll(mayorRigDir, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	r := &rig.Rig{
		Name: "test-rig",
		Path: root,
	}
	m := NewManager(r, git.NewGit(root), nil)

	// Get should return polecat with StateWorking (assume active if beads unavailable)
	polecat, err := m.Get("Test")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if polecat.Name != "Test" {
		t.Errorf("Name = %q, want Test", polecat.Name)
	}
	if polecat.State != StateWorking {
		t.Errorf("State = %v, want StateWorking (beads not available)", polecat.State)
	}
}

func TestListWithPolecats(t *testing.T) {
	root := t.TempDir()

	// Create some polecat directories (state is now derived from beads, not state files)
	for _, name := range []string{"Toast", "Cheedo"} {
		polecatDir := filepath.Join(root, "polecats", name)
		if err := os.MkdirAll(polecatDir, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "polecats", ".claude"), 0755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	// Create mayor/rig for beads path
	mayorRig := filepath.Join(root, "mayor", "rig")
	if err := os.MkdirAll(mayorRig, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	r := &rig.Rig{
		Name: "test-rig",
		Path: root,
	}
	m := NewManager(r, git.NewGit(root), nil)

	polecats, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(polecats) != 2 {
		t.Errorf("polecats count = %d, want 2", len(polecats))
	}
}

// Note: TestSetState, TestAssignIssue, and TestClearIssue were removed.
// These operations now require a running beads instance and are tested
// via integration tests. The unit tests here focus on testing the basic
// polecat lifecycle operations that don't require beads.

func TestSetStateWithoutBeads(t *testing.T) {
	// SetState should not error when beads is not available
	root := t.TempDir()
	polecatDir := filepath.Join(root, "polecats", "Test")
	if err := os.MkdirAll(polecatDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Create mayor/rig for beads path
	mayorRig := filepath.Join(root, "mayor", "rig")
	if err := os.MkdirAll(mayorRig, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	r := &rig.Rig{
		Name: "test-rig",
		Path: root,
	}
	m := NewManager(r, git.NewGit(root), nil)

	// SetState should succeed (no-op when no issue assigned)
	err := m.SetState("Test", StateWorking)
	if err != nil {
		t.Errorf("SetState: %v (expected no error when no beads/issue)", err)
	}
}

func TestClearIssueWithoutAssignment(t *testing.T) {
	// ClearIssue should not error when no issue is assigned
	root := t.TempDir()
	polecatDir := filepath.Join(root, "polecats", "Test")
	if err := os.MkdirAll(polecatDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Create mayor/rig for beads path
	mayorRig := filepath.Join(root, "mayor", "rig")
	if err := os.MkdirAll(mayorRig, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	r := &rig.Rig{
		Name: "test-rig",
		Path: root,
	}
	m := NewManager(r, git.NewGit(root), nil)

	// ClearIssue should succeed even when no issue assigned
	err := m.ClearIssue("Test")
	if err != nil {
		t.Errorf("ClearIssue: %v (expected no error when no assignment)", err)
	}
}

// NOTE: TestInstallCLAUDETemplate tests were removed.
// We no longer write CLAUDE.md to worktrees - Gas Town context is injected
// ephemerally via SessionStart hook (gt prime) to prevent leaking internal
// architecture into project repos.

func TestAddWithOptions_HasAgentsMD(t *testing.T) {
	// This test verifies that AGENTS.md exists in polecat worktrees after creation.
	// AGENTS.md is critical for polecats to "land the plane" properly.
	installMockBd(t)

	root := t.TempDir()

	// Create mayor/rig directory structure (this acts as repo base when no .repo.git)
	mayorRig := filepath.Join(root, "mayor", "rig")
	if err := os.MkdirAll(mayorRig, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Initialize git repo in mayor/rig
	cmd := exec.Command("git", "init")
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	// Create AGENTS.md with test content
	agentsMDContent := []byte("# AGENTS.md\n\nTest content for polecats.\n")
	agentsMDPath := filepath.Join(mayorRig, "AGENTS.md")
	if err := os.WriteFile(agentsMDPath, agentsMDContent, 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	// Commit AGENTS.md so it's part of the repo
	mayorGit := git.NewGit(mayorRig)
	if err := mayorGit.Add("AGENTS.md"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := mayorGit.Commit("Add AGENTS.md"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// AddWithOptions needs origin/main to exist. Add self as origin and create tracking ref.
	cmd = exec.Command("git", "remote", "add", "origin", mayorRig)
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	// When using a local directory as remote, fetch doesn't create tracking branches.
	// Create origin/main manually since AddWithOptions expects origin/main by default.
	cmd = exec.Command("git", "update-ref", "refs/remotes/origin/main", "HEAD")
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git update-ref: %v\n%s", err, out)
	}

	// Create rig-level .beads directory with redirect to mayor/rig/.beads
	rigBeads := filepath.Join(root, ".beads")
	if err := os.MkdirAll(rigBeads, 0755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}
	mayorBeads := filepath.Join(mayorRig, ".beads")
	if err := os.MkdirAll(mayorBeads, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig/.beads: %v", err)
	}
	rigRedirect := filepath.Join(rigBeads, "redirect")
	if err := os.WriteFile(rigRedirect, []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}

	// Create rig pointing to root
	r := &rig.Rig{
		Name: "rig",
		Path: root,
	}
	m := NewManager(r, git.NewGit(root), nil)

	// Create polecat via AddWithOptions
	polecat, err := m.AddWithOptions("TestAgent", AddOptions{})
	if err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	// Verify AGENTS.md exists in the worktree
	worktreeAgentsMD := filepath.Join(polecat.ClonePath, "AGENTS.md")
	if _, err := os.Stat(worktreeAgentsMD); os.IsNotExist(err) {
		t.Errorf("AGENTS.md does not exist in worktree at %s", worktreeAgentsMD)
	}

	// Verify content matches
	content, err := os.ReadFile(worktreeAgentsMD)
	if err != nil {
		t.Fatalf("read worktree AGENTS.md: %v", err)
	}
	gotContent := strings.ReplaceAll(string(content), "\r\n", "\n")
	wantContent := strings.ReplaceAll(string(agentsMDContent), "\r\n", "\n")
	if gotContent != wantContent {
		t.Errorf("AGENTS.md content = %q, want %q", gotContent, wantContent)
	}
}

// TestReconcilePoolWith tests all permutations of directory and session existence.
// This is the core allocation policy logic.
//
// Truth table:
//
//	HasDir | HasSession | Result
//	-------|------------|------------------
//	false  | false      | available (not in-use)
//	true   | false      | in-use (normal finished polecat)
//	false  | true       | orphan → kill session, available
//	true   | true       | in-use (normal working polecat)
func TestReconcilePoolWith(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		namesWithDirs     []string
		namesWithSessions []string
		wantInUse         []string // names that should be marked in-use
		wantOrphans       []string // sessions that should be killed
	}{
		{
			name:              "no dirs, no sessions - all available",
			namesWithDirs:     []string{},
			namesWithSessions: []string{},
			wantInUse:         []string{},
			wantOrphans:       []string{},
		},
		{
			name:              "has dir, no session - in use",
			namesWithDirs:     []string{"toast"},
			namesWithSessions: []string{},
			wantInUse:         []string{"toast"},
			wantOrphans:       []string{},
		},
		{
			name:              "no dir, has session - orphan killed",
			namesWithDirs:     []string{},
			namesWithSessions: []string{"nux"},
			wantInUse:         []string{},
			wantOrphans:       []string{"nux"},
		},
		{
			name:              "has dir, has session - in use",
			namesWithDirs:     []string{"capable"},
			namesWithSessions: []string{"capable"},
			wantInUse:         []string{"capable"},
			wantOrphans:       []string{},
		},
		{
			name:              "mixed: one with dir, one orphan session",
			namesWithDirs:     []string{"toast"},
			namesWithSessions: []string{"toast", "nux"},
			wantInUse:         []string{"toast"},
			wantOrphans:       []string{"nux"},
		},
		{
			name:              "multiple dirs, no sessions",
			namesWithDirs:     []string{"toast", "nux", "capable"},
			namesWithSessions: []string{},
			wantInUse:         []string{"capable", "nux", "toast"},
			wantOrphans:       []string{},
		},
		{
			name:              "multiple orphan sessions",
			namesWithDirs:     []string{},
			namesWithSessions: []string{"slit", "rictus"},
			wantInUse:         []string{},
			wantOrphans:       []string{"rictus", "slit"},
		},
		{
			name:              "complex: dirs, valid sessions, orphan sessions",
			namesWithDirs:     []string{"toast", "capable"},
			namesWithSessions: []string{"toast", "nux", "slit"},
			wantInUse:         []string{"capable", "toast"},
			wantOrphans:       []string{"nux", "slit"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary directory for pool state
			tmpDir, err := os.MkdirTemp("", "reconcile-test-*")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.RemoveAll(tmpDir) }()

			// Create rig and manager (nil tmux for unit test)
			// Use "myrig" which hashes to mad-max theme
			r := &rig.Rig{
				Name: "myrig",
				Path: tmpDir,
			}
			m := NewManager(r, nil, nil)

			// Call ReconcilePoolWith
			m.ReconcilePoolWith(tt.namesWithDirs, tt.namesWithSessions)

			// Verify in-use names
			gotInUse := m.namePool.ActiveNames()
			sort.Strings(gotInUse)
			sort.Strings(tt.wantInUse)

			if len(gotInUse) != len(tt.wantInUse) {
				t.Errorf("in-use count: got %d, want %d", len(gotInUse), len(tt.wantInUse))
			}
			for i := range tt.wantInUse {
				if i >= len(gotInUse) || gotInUse[i] != tt.wantInUse[i] {
					t.Errorf("in-use names: got %v, want %v", gotInUse, tt.wantInUse)
					break
				}
			}

			// Verify orphans would be identified correctly
			// (actual killing requires tmux, tested separately)
			dirSet := make(map[string]bool)
			for _, name := range tt.namesWithDirs {
				dirSet[name] = true
			}
			var gotOrphans []string
			for _, name := range tt.namesWithSessions {
				if !dirSet[name] {
					gotOrphans = append(gotOrphans, name)
				}
			}
			sort.Strings(gotOrphans)
			sort.Strings(tt.wantOrphans)

			if len(gotOrphans) != len(tt.wantOrphans) {
				t.Errorf("orphan count: got %d, want %d", len(gotOrphans), len(tt.wantOrphans))
			}
			for i := range tt.wantOrphans {
				if i >= len(gotOrphans) || gotOrphans[i] != tt.wantOrphans[i] {
					t.Errorf("orphans: got %v, want %v", gotOrphans, tt.wantOrphans)
					break
				}
			}
		})
	}
}

// TestReconcilePoolWith_Allocation verifies that allocation respects reconciled state.
func TestReconcilePoolWith_Allocation(t *testing.T) {
	t.Parallel()

	tmpDir, err := os.MkdirTemp("", "reconcile-alloc-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Use "myrig" which hashes to mad-max theme
	r := &rig.Rig{
		Name: "myrig",
		Path: tmpDir,
	}
	m := NewManager(r, nil, nil)

	// Mark first few pool names as in-use via directories
	// (furiosa, nux, slit are first 3 in mad-max theme)
	m.ReconcilePoolWith([]string{"furiosa", "nux", "slit"}, []string{})

	// First allocation should skip in-use names
	name, err := m.namePool.Allocate()
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	// Should get "rictus" (4th in mad-max theme), not furiosa/nux/slit
	if name == "furiosa" || name == "nux" || name == "slit" {
		t.Errorf("allocated in-use name %q, should have skipped", name)
	}
	if name != "rictus" {
		t.Errorf("expected rictus (4th name), got %q", name)
	}
}

// TestReconcilePoolWith_OrphanDoesNotBlockAllocation verifies orphan sessions
// don't prevent name allocation (they're killed, freeing the name).
func TestReconcilePoolWith_OrphanDoesNotBlockAllocation(t *testing.T) {
	t.Parallel()

	tmpDir, err := os.MkdirTemp("", "reconcile-orphan-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Use "myrig" which hashes to mad-max theme
	r := &rig.Rig{
		Name: "myrig",
		Path: tmpDir,
	}
	m := NewManager(r, nil, nil)

	// furiosa has orphan session (no dir) - should NOT block allocation
	m.ReconcilePoolWith([]string{}, []string{"furiosa"})

	// furiosa should be available (orphan session killed, name freed)
	name, err := m.namePool.Allocate()
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	if name != "furiosa" {
		t.Errorf("expected furiosa (orphan freed), got %q", name)
	}
}

func TestIsDoltConfigError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"transient optimistic lock", fmt.Errorf("optimistic lock failed"), false},
		{"transient serialization", fmt.Errorf("serialization failure"), false},
		{"not initialized", fmt.Errorf("database not initialized"), true},
		{"no such table", fmt.Errorf("no such table: issues"), true},
		{"table not found", fmt.Errorf("table not found: issues"), true},
		{"issue_prefix missing", fmt.Errorf("issue_prefix not configured"), true},
		{"no database", fmt.Errorf("no database found at path"), true},
		{"database not found", fmt.Errorf("database not found"), true},
		{"connection refused", fmt.Errorf("dial tcp: connection refused"), true},
		{"configure custom types", fmt.Errorf("configure custom types in /path: exit 1"), true},
		{"identity mismatch", fmt.Errorf("identity mismatch: local project_id != database project_id"), true},
		{"Unknown database", fmt.Errorf("Unknown database 'gastown'"), true},
		{"generic error", fmt.Errorf("something else failed"), false},
		{"wrapped not initialized", fmt.Errorf("bd create failed: %w", fmt.Errorf("database not initialized")), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDoltConfigError(tt.err); got != tt.want {
				t.Errorf("isDoltConfigError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsDoltOptimisticLockError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"optimistic lock", fmt.Errorf("optimistic lock failed"), true},
		{"serialization failure", fmt.Errorf("serialization failure"), true},
		{"lock wait timeout", fmt.Errorf("lock wait timeout exceeded"), true},
		{"try restarting transaction", fmt.Errorf("try restarting transaction"), true},
		{"database is read only", fmt.Errorf("database is read only"), true},
		{"cannot update manifest", fmt.Errorf("cannot update manifest"), true},
		{"config error", fmt.Errorf("not initialized"), false},
		{"generic error", fmt.Errorf("something else"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDoltOptimisticLockError(tt.err); got != tt.want {
				t.Errorf("isDoltOptimisticLockError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsCurrentHookedIssueForAssignee(t *testing.T) {
	assignee := "testrig/polecats/toast"

	tests := []struct {
		name  string
		issue *beads.Issue
		want  bool
	}{
		{
			name: "nil issue",
			want: false,
		},
		{
			name: "hooked and matching assignee",
			issue: &beads.Issue{
				Status:   beads.StatusHooked,
				Assignee: assignee,
			},
			want: true,
		},
		{
			name: "hooked but different assignee",
			issue: &beads.Issue{
				Status:   beads.StatusHooked,
				Assignee: "testrig/polecats/nux",
			},
			want: false,
		},
		{
			name: "matching assignee but open status",
			issue: &beads.Issue{
				Status:   "open",
				Assignee: assignee,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCurrentHookedIssueForAssignee(tt.issue, assignee); got != tt.want {
				t.Fatalf("isCurrentHookedIssueForAssignee() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildBranchName(t *testing.T) {
	tmpDir := t.TempDir()

	// Initialize a git repo for config access
	gitCmd := exec.Command("git", "init")
	gitCmd.Dir = tmpDir
	if err := gitCmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	// Set git user.name for testing
	configCmd := exec.Command("git", "config", "user.name", "testuser")
	configCmd.Dir = tmpDir
	if err := configCmd.Run(); err != nil {
		t.Fatalf("git config: %v", err)
	}

	tests := []struct {
		name     string
		template string
		issue    string
		want     string
	}{
		{
			name:     "default_with_issue",
			template: "", // Empty template = default behavior
			issue:    "gt-123",
			want:     "polecat/alpha/gt-123@", // timestamp suffix varies
		},
		{
			name:     "default_without_issue",
			template: "",
			issue:    "",
			want:     "polecat/alpha-", // timestamp suffix varies
		},
		{
			name:     "custom_template_user_year_month",
			template: "{user}/{year}/{month}/fix",
			issue:    "",
			want:     "testuser/", // year/month will vary
		},
		{
			name:     "custom_template_with_name",
			template: "feature/{name}",
			issue:    "",
			want:     "feature/alpha",
		},
		{
			name:     "custom_template_with_issue",
			template: "work/{issue}",
			issue:    "gt-456",
			want:     "work/456",
		},
		{
			name:     "custom_template_with_timestamp",
			template: "feature/{name}-{timestamp}",
			issue:    "",
			want:     "feature/alpha-", // timestamp suffix varies
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create rig with test template
			r := &rig.Rig{
				Name: "test-rig",
				Path: tmpDir,
			}

			// Override system defaults for this test if template is set
			if tt.template != "" {
				origDefault := rig.SystemDefaults["polecat_branch_template"]
				rig.SystemDefaults["polecat_branch_template"] = tt.template
				defer func() {
					rig.SystemDefaults["polecat_branch_template"] = origDefault
				}()
			}

			g := git.NewGit(tmpDir)
			m := NewManager(r, g, nil)

			got := m.buildBranchName("alpha", tt.issue)

			// For default templates, just check prefix since timestamp varies
			if tt.template == "" {
				if !strings.HasPrefix(got, tt.want) {
					t.Errorf("buildBranchName() = %q, want prefix %q", got, tt.want)
				}
			} else {
				// For custom templates with time-varying fields, check prefix
				if strings.Contains(tt.template, "{year}") || strings.Contains(tt.template, "{month}") || strings.Contains(tt.template, "{timestamp}") {
					if !strings.HasPrefix(got, tt.want) {
						t.Errorf("buildBranchName() = %q, want prefix %q", got, tt.want)
					}
				} else {
					if got != tt.want {
						t.Errorf("buildBranchName() = %q, want %q", got, tt.want)
					}
				}
			}
		})
	}
}

func TestAddWithOptions_NoPrimeMDCreatedLocally(t *testing.T) {
	// This test verifies that ProvisionPrimeMDForWorktree does NOT create
	// a local .beads/PRIME.md in the worktree when there's no tracked one.
	//
	// Bug: If redirect setup fails or ProvisionPrimeMDForWorktree doesn't
	// follow redirects correctly, it may create PRIME.md locally instead
	// of at the rig-level beads location.

	root := t.TempDir()

	// Create mayor/rig directory structure
	mayorRig := filepath.Join(root, "mayor", "rig")
	if err := os.MkdirAll(mayorRig, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Create rig-level .beads directory
	rigBeads := filepath.Join(root, ".beads")
	if err := os.MkdirAll(rigBeads, 0755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}

	// Create redirect at rig level pointing to mayor/rig/.beads
	mayorBeads := filepath.Join(mayorRig, ".beads")
	if err := os.MkdirAll(mayorBeads, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig/.beads: %v", err)
	}
	rigRedirect := filepath.Join(rigBeads, "redirect")
	if err := os.WriteFile(rigRedirect, []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}

	// Initialize beads database so agent bead creation works.
	// Use real bd if available; fall back to a mock for environments (like
	// Windows CI) where bd is not installed.
	if _, err := exec.LookPath("bd"); err == nil {
		testutil.RequireDoltContainer(t)
		port, _ := strconv.Atoi(testutil.DoltContainerPort())
		bd := beads.NewIsolatedWithPort(mayorRig, port)
		if err := bd.Init("gt"); err != nil {
			t.Fatalf("bd init: %v", err)
		}
	} else {
		installMockBd(t)
		// Write the custom-types sentinel so EnsureCustomTypes is a no-op.
		_ = os.WriteFile(filepath.Join(mayorBeads, ".gt-types-configured"), []byte("v1\n"), 0644)
	}

	// Initialize git repo in mayor/rig WITHOUT any .beads/PRIME.md
	cmd := exec.Command("git", "init")
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	// Create a dummy file and commit (NO .beads/PRIME.md)
	dummyPath := filepath.Join(mayorRig, "README.md")
	if err := os.WriteFile(dummyPath, []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	mayorGit := git.NewGit(mayorRig)
	if err := mayorGit.Add("README.md"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := mayorGit.Commit("Initial commit without PRIME.md"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// AddWithOptions needs origin/main to exist. Add self as origin and create tracking ref.
	cmd = exec.Command("git", "remote", "add", "origin", mayorRig)
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	// When using a local directory as remote, fetch doesn't create tracking branches.
	// Create origin/main manually since AddWithOptions expects origin/main by default.
	cmd = exec.Command("git", "update-ref", "refs/remotes/origin/main", "HEAD")
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git update-ref: %v\n%s", err, out)
	}

	// Create rig pointing to root
	r := &rig.Rig{
		Name: "rig",
		Path: root,
	}
	m := NewManager(r, git.NewGit(root), nil)

	// Create polecat
	polecat, err := m.AddWithOptions("TestNoLocal", AddOptions{})
	if err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	// BUG CHECK: The worktree should NOT have a local .beads/PRIME.md
	// ProvisionPrimeMDForWorktree should follow redirect to mayor/rig/.beads
	worktreePrimeMD := filepath.Join(polecat.ClonePath, ".beads", "PRIME.md")
	if _, err := os.Stat(worktreePrimeMD); err == nil {
		t.Errorf("PRIME.md should NOT exist in worktree .beads/ (should be at rig level via redirect): %s", worktreePrimeMD)
	}

	// Verify the redirect file exists
	worktreeRedirect := filepath.Join(polecat.ClonePath, ".beads", "redirect")
	if _, err := os.Stat(worktreeRedirect); os.IsNotExist(err) {
		t.Errorf("redirect file should exist at: %s", worktreeRedirect)
	}

	// Verify PRIME.md was created at mayor/rig/.beads/ (where redirect points)
	mayorPrimeMD := filepath.Join(mayorBeads, "PRIME.md")
	if _, err := os.Stat(mayorPrimeMD); os.IsNotExist(err) {
		t.Errorf("PRIME.md should exist at mayor/rig/.beads/: %s", mayorPrimeMD)
	}
}

func TestAddWithOptions_UsesCanonicalOriginDefaultBranch(t *testing.T) {
	mgr, mayorRig := setupCanonicalBranchManagerTest(t)

	mayorGit := git.NewGit(mayorRig)
	baseSHA, err := mayorGit.Rev("origin/main")
	if err != nil {
		t.Fatalf("resolve origin/main: %v", err)
	}
	staleSHA := createStalePolecatCommit(t, mayorRig, "main", "polecat/stale-source")

	polecat, err := mgr.AddWithOptions("toast", AddOptions{})
	if err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	worktreeGit := git.NewGit(polecat.ClonePath)
	staleAncestor, err := worktreeGit.IsAncestor(staleSHA, polecat.Branch)
	if err != nil {
		t.Fatalf("check stale ancestry: %v", err)
	}
	if staleAncestor {
		t.Fatalf("new polecat branch %q unexpectedly includes stale local commit %s", polecat.Branch, staleSHA)
	}

	baseAncestor, err := worktreeGit.IsAncestor(baseSHA, polecat.Branch)
	if err != nil {
		t.Fatalf("check canonical ancestry: %v", err)
	}
	if !baseAncestor {
		t.Fatalf("new polecat branch %q should descend from origin/main commit %s", polecat.Branch, baseSHA)
	}
}

func TestReuseIdlePolecat_UsesCanonicalOriginDefaultBranch(t *testing.T) {
	mgr, mayorRig := setupCanonicalBranchManagerTest(t)

	mayorGit := git.NewGit(mayorRig)
	baseSHA, err := mayorGit.Rev("origin/main")
	if err != nil {
		t.Fatalf("resolve origin/main: %v", err)
	}

	polecat, err := mgr.AddWithOptions("toast", AddOptions{})
	if err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	staleSHA := createStalePolecatCommit(t, polecat.ClonePath, "HEAD", "polecat/toast-stale")

	reused, err := mgr.ReuseIdlePolecat("toast", AddOptions{HookBead: "gt-next"})
	if err != nil {
		t.Fatalf("ReuseIdlePolecat: %v", err)
	}

	worktreeGit := git.NewGit(reused.ClonePath)
	staleAncestor, err := worktreeGit.IsAncestor(staleSHA, reused.Branch)
	if err != nil {
		t.Fatalf("check stale ancestry: %v", err)
	}
	if staleAncestor {
		t.Fatalf("reused polecat branch %q unexpectedly includes stale local commit %s", reused.Branch, staleSHA)
	}

	baseAncestor, err := worktreeGit.IsAncestor(baseSHA, reused.Branch)
	if err != nil {
		t.Fatalf("check canonical ancestry: %v", err)
	}
	if !baseAncestor {
		t.Fatalf("reused polecat branch %q should descend from origin/main commit %s", reused.Branch, baseSHA)
	}
}

// TestAddWithOptions_ResumeBranch verifies gh#3602: when ResumeBranch is set,
// AddWithOptions checks out the named existing branch instead of creating a
// fresh polecat/<name>/<bead>@<ts> branch. This lets `gt sling --branch/--pr`
// resume work on an existing PR branch without creating duplicates.
func TestAddWithOptions_ResumeBranch(t *testing.T) {
	mgr, mayorRig := setupCanonicalBranchManagerTest(t)

	// Create a "PR branch" on origin with a marker commit, mimicking an existing
	// open PR that a polecat needs to resume.
	prBranch := "polecat/example/gh-1234@abcdef"
	prCommit := createStalePolecatCommit(t, mayorRig, "main", prBranch)

	// Push the branch to the remote-tracking ref so the bare repo can resolve it.
	cmd := exec.Command("git", "update-ref", "refs/remotes/origin/"+prBranch, "HEAD")
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("update-ref origin/%s: %v\n%s", prBranch, err, out)
	}

	polecat, err := mgr.AddWithOptions("toast", AddOptions{ResumeBranch: prBranch})
	if err != nil {
		t.Fatalf("AddWithOptions with ResumeBranch: %v", err)
	}

	if polecat.Branch != prBranch {
		t.Fatalf("polecat.Branch = %q, want %q (ResumeBranch should override fresh-branch naming)", polecat.Branch, prBranch)
	}

	worktreeGit := git.NewGit(polecat.ClonePath)
	current, err := worktreeGit.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if current != prBranch {
		t.Fatalf("worktree HEAD on branch %q, want %q", current, prBranch)
	}

	// The PR commit must be reachable from HEAD — proves we attached to the
	// existing branch rather than starting fresh from main.
	reachable, err := worktreeGit.IsAncestor(prCommit, "HEAD")
	if err != nil {
		t.Fatalf("IsAncestor: %v", err)
	}
	if !reachable {
		t.Fatalf("PR commit %s should be reachable from HEAD on resumed branch", prCommit)
	}
}

func TestAddWithOptions_NoFilesAddedToRepo(t *testing.T) {
	// This test verifies the invariant that polecat creation does NOT add any
	// TRACKED files to the repo's directory structure. The user's code should stay pure.
	//
	// After polecat install, `git status` in the worktree should show no
	// untracked files and no modifications. Settings are installed at the shared
	// polecats/.claude/settings.json directory (outside worktrees), so they
	// never appear in any worktree's git status.

	root := t.TempDir()

	// Create mayor/rig directory structure
	mayorRig := filepath.Join(root, "mayor", "rig")
	if err := os.MkdirAll(mayorRig, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Create rig-level .beads directory with redirect
	rigBeads := filepath.Join(root, ".beads")
	if err := os.MkdirAll(rigBeads, 0755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}
	mayorBeads := filepath.Join(mayorRig, ".beads")
	if err := os.MkdirAll(mayorBeads, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig/.beads: %v", err)
	}
	rigRedirect := filepath.Join(rigBeads, "redirect")
	if err := os.WriteFile(rigRedirect, []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}

	// Initialize beads database so agent bead creation works.
	// Use real bd if available; fall back to a mock for environments (like
	// Windows CI) where bd is not installed.
	if _, err := exec.LookPath("bd"); err == nil {
		testutil.RequireDoltContainer(t)
		port, _ := strconv.Atoi(testutil.DoltContainerPort())
		bd := beads.NewIsolatedWithPort(mayorRig, port)
		if err := bd.Init("gt"); err != nil {
			t.Fatalf("bd init: %v", err)
		}
	} else {
		installMockBd(t)
		// Write the custom-types sentinel so EnsureCustomTypes is a no-op.
		_ = os.WriteFile(filepath.Join(mayorBeads, ".gt-types-configured"), []byte("v1\n"), 0644)
	}

	// Initialize a CLEAN git repo with known files only
	cmd := exec.Command("git", "init")
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	// Create .gitignore with .claude/ and .beads/ (standard practice)
	// .claude/ - Claude Code local state
	// .beads/ - Gas Town local state (redirect file)
	gitignorePath := filepath.Join(mayorRig, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(".claude/\n.beads/\n"), 0644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	// Create minimal repo content (NO .beads, NO .claude, NO CLAUDE.md)
	readmePath := filepath.Join(mayorRig, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Clean Repo\n"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	srcDir := filepath.Join(mayorRig, "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	mainPath := filepath.Join(srcDir, "main.go")
	if err := os.WriteFile(mainPath, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	// Commit everything
	mayorGit := git.NewGit(mayorRig)
	if err := mayorGit.Add("."); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := mayorGit.Commit("Initial commit - clean repo"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// AddWithOptions needs origin/main to exist. Add self as origin and create tracking ref.
	cmd = exec.Command("git", "remote", "add", "origin", mayorRig)
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	// When using a local directory as remote, fetch doesn't create tracking branches.
	// Create origin/main manually since AddWithOptions expects origin/main by default.
	cmd = exec.Command("git", "update-ref", "refs/remotes/origin/main", "HEAD")
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git update-ref: %v\n%s", err, out)
	}

	// Create AGENTS.md in mayor/rig AFTER git commit (NOT tracked in git)
	// This triggers the fallback copy during polecat install
	agentsMDPath := filepath.Join(mayorRig, "AGENTS.md")
	if err := os.WriteFile(agentsMDPath, []byte("# AGENTS\n\nFallback content.\n"), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	// Create rig and polecat manager
	r := &rig.Rig{
		Name: "rig",
		Path: root,
	}
	m := NewManager(r, git.NewGit(root), nil)

	// Create polecat
	polecat, err := m.AddWithOptions("TestClean", AddOptions{})
	if err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	// Run git status in worktree - should show nothing except .beads/ (infrastructure)
	// Settings are at polecats/.claude/settings.json (outside worktree) so won't appear
	cmd = exec.Command("git", "status", "--porcelain")
	cmd.Dir = polecat.ClonePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, out)
	}

	// Filter out expected infrastructure files
	var unexpected []string
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		// .beads/ is expected - it contains the redirect file for shared beads
		if strings.Contains(line, ".beads") {
			continue
		}
		// CLAUDE.md is expected - provisioned by CreatePolecatCLAUDEmd for gt done instructions
		if strings.Contains(line, "CLAUDE.md") {
			continue
		}
		unexpected = append(unexpected, line)
	}
	if len(unexpected) > 0 {
		t.Errorf("polecat worktree should be clean after install (no files added to repo), but git status shows:\n%s", strings.Join(unexpected, "\n"))
	}
}

func TestAddWithOptions_SettingsInstalledInPolecatsDir(t *testing.T) {
	// This test verifies that polecat creation installs .claude/settings.json
	// in the SHARED polecats/ parent directory (not inside individual worktrees).
	// Claude Code with --settings supports parent directory settings, and placing
	// them at the polecats/ level avoids polluting individual worktree repos.

	root := t.TempDir()

	// Create mayor/rig directory structure
	mayorRig := filepath.Join(root, "mayor", "rig")
	if err := os.MkdirAll(mayorRig, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Create rig-level .beads directory with redirect
	rigBeads := filepath.Join(root, ".beads")
	if err := os.MkdirAll(rigBeads, 0755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}
	mayorBeads := filepath.Join(mayorRig, ".beads")
	if err := os.MkdirAll(mayorBeads, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig/.beads: %v", err)
	}
	rigRedirect := filepath.Join(rigBeads, "redirect")
	if err := os.WriteFile(rigRedirect, []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}

	// Initialize beads database so agent bead creation works.
	// Use real bd if available; fall back to a mock for environments (like
	// Windows CI) where bd is not installed.
	if _, err := exec.LookPath("bd"); err == nil {
		testutil.RequireDoltContainer(t)
		port, _ := strconv.Atoi(testutil.DoltContainerPort())
		bd := beads.NewIsolatedWithPort(mayorRig, port)
		if err := bd.Init("gt"); err != nil {
			t.Fatalf("bd init: %v", err)
		}
	} else {
		installMockBd(t)
		// Write the custom-types sentinel so EnsureCustomTypes is a no-op.
		_ = os.WriteFile(filepath.Join(mayorBeads, ".gt-types-configured"), []byte("v1\n"), 0644)
	}

	// Initialize a git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	readmePath := filepath.Join(mayorRig, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Test Repo\n"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}

	mayorGit := git.NewGit(mayorRig)
	if err := mayorGit.Add("."); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := mayorGit.Commit("Initial commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// AddWithOptions needs origin/main to exist. Add self as origin and create tracking ref.
	cmd = exec.Command("git", "remote", "add", "origin", mayorRig)
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	// When using a local directory as remote, fetch doesn't create tracking branches.
	// Create origin/main manually since AddWithOptions expects origin/main by default.
	cmd = exec.Command("git", "update-ref", "refs/remotes/origin/main", "HEAD")
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git update-ref: %v\n%s", err, out)
	}

	// Create rig and polecat manager
	r := &rig.Rig{
		Name: "rig",
		Path: root,
	}
	m := NewManager(r, git.NewGit(root), nil)

	// Create polecat
	polecat, err := m.AddWithOptions("TestSettings", AddOptions{})
	if err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	// Verify settings.json exists in the SHARED polecats/ parent directory
	// polecats dir is the parent of polecat.ClonePath's parent (ClonePath = polecats/<name>/<rig>)
	polecatsDir := filepath.Dir(filepath.Dir(polecat.ClonePath))
	settingsPath := filepath.Join(polecatsDir, ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		t.Errorf("settings.json should exist at %s (shared polecats dir) for Claude Code to find hooks", settingsPath)
	}

	// Verify settings.json does NOT exist inside the worktree (no longer installed there)
	worktreeSettingsPath := filepath.Join(polecat.ClonePath, ".claude", "settings.json")
	if _, err := os.Stat(worktreeSettingsPath); err == nil {
		t.Errorf("settings.json should NOT exist inside worktree at %s (settings are now in shared polecats dir)", worktreeSettingsPath)
	}
}

// TestOverflowNameSessionFormat verifies that overflow names don't create double-prefix.
// Regression test for the double-prefix bug (tr-testrig-N instead of tr-N).
func TestOverflowNameSessionFormat(t *testing.T) {
	// Register prefix for testrig so PrefixFor("testrig") returns "tr"
	reg := session.NewPrefixRegistry()
	reg.Register("tr", "testrig")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })

	tmpDir := t.TempDir()

	// Create minimal rig
	rigPath := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{
		Name: "testrig",
		Path: rigPath,
	}

	// Create name pool with small size to trigger overflow quickly
	pool := NewNamePoolWithConfig(rigPath, "testrig", "mad-max", nil, 2)
	mgr := &Manager{
		rig:      r,
		namePool: pool,
	}

	// Allocate all themed names
	_, _ = mgr.namePool.Allocate() // furiosa
	_, _ = mgr.namePool.Allocate() // nux

	// Next allocation should be overflow (just a number)
	overflowName, err := mgr.namePool.Allocate()
	if err != nil {
		t.Fatalf("overflow allocation failed: %v", err)
	}

	// Overflow name should be just "3", not "testrig-3"
	if overflowName != "3" {
		t.Errorf("expected overflow name '3', got %s", overflowName)
	}

	// Create session manager
	sessMgr := NewSessionManager(nil, r)
	sessionName := sessMgr.SessionName(overflowName)

	// Verify session name is tr-3, NOT tr-testrig-3
	expected := "tr-3"
	if sessionName != expected {
		t.Errorf("expected session name %s, got %s (double-prefix bug!)", expected, sessionName)
	}

	// Verify no double-prefix
	if strings.Contains(sessionName, "testrig-testrig") {
		t.Errorf("double-prefix detected in session name: %s", sessionName)
	}
}

// TestPendingMarkerBlocksReallocation verifies that a .pending reservation file
// written by AllocateName prevents a concurrent reconcile from treating the name
// as available (the TOCTOU fix: hq-ypvza / gt-601kx).
func TestPendingMarkerBlocksReallocation(t *testing.T) {
	t.Parallel()

	tmpDir, err := os.MkdirTemp("", "pending-marker-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Use "myrig" which hashes to mad-max theme (furiosa is first name)
	r := &rig.Rig{
		Name: "myrig",
		Path: tmpDir,
	}
	m := NewManager(r, nil, nil)

	// Simulate AllocateName: create polecats/ dir and write a .pending marker
	// for "furiosa" (as if AllocateName ran but AddWithOptions hasn't yet).
	polecatsDir := filepath.Join(tmpDir, "polecats")
	if err := os.MkdirAll(polecatsDir, 0755); err != nil {
		t.Fatal(err)
	}
	pendingPath := m.pendingPath("furiosa")
	if err := os.WriteFile(pendingPath, []byte("999"), 0644); err != nil {
		t.Fatal(err)
	}

	// Simulate a concurrent reconcile (no directories exist, only the marker).
	// reconcilePoolInternal should treat "furiosa" as in-use via the marker.
	m.reconcilePoolInternal()

	// Now allocate — should NOT get furiosa (it's reserved by .pending).
	name, err := m.namePool.Allocate()
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if name == "furiosa" {
		t.Errorf("allocated furiosa despite active .pending marker — TOCTOU race not fixed")
	}
}

// TestStalePendingMarkerIsCleanedUp verifies that cleanupOrphanPolecatState
// removes .pending files older than pendingMaxAge.
func TestStalePendingMarkerIsCleanedUp(t *testing.T) {
	t.Parallel()

	tmpDir, err := os.MkdirTemp("", "stale-pending-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	r := &rig.Rig{
		Name: "myrig",
		Path: tmpDir,
	}
	m := NewManager(r, nil, nil)

	polecatsDir := filepath.Join(tmpDir, "polecats")
	if err := os.MkdirAll(polecatsDir, 0755); err != nil {
		t.Fatal(err)
	}

	pendingPath := m.pendingPath("furiosa")
	if err := os.WriteFile(pendingPath, []byte("999"), 0644); err != nil {
		t.Fatal(err)
	}

	// Backdate the file to simulate a stale marker (older than pendingMaxAge).
	staleTime := time.Now().Add(-(pendingMaxAge + time.Minute))
	if err := os.Chtimes(pendingPath, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}

	// cleanupOrphanPolecatState should remove stale markers.
	m.cleanupOrphanPolecatState()

	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Errorf("stale .pending file was not cleaned up by cleanupOrphanPolecatState")
	}
}

// TestAddWithOptions_RollbackReleasesName verifies that when AddWithOptions fails,
// the allocated name is released back to the pool and the polecat directory is cleaned up.
// Regression test for gt-2vs22: cleanupOnError previously only removed the directory,
// leaking pool names on spawn failure.
func TestAddWithOptions_RollbackReleasesName(t *testing.T) {
	root := t.TempDir()

	// Create mayor/rig directory structure (acts as repo base)
	mayorRig := filepath.Join(root, "mayor", "rig")
	if err := os.MkdirAll(mayorRig, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Initialize git repo in mayor/rig
	cmd := exec.Command("git", "init")
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	// Create and commit a file
	if err := os.WriteFile(filepath.Join(mayorRig, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mayorGit := git.NewGit(mayorRig)
	if err := mayorGit.Add("."); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := mayorGit.Commit("Initial commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// Add origin remote pointing to a nonexistent path so that fetch fails
	// and origin/main is never created. This causes AddWithOptions to fail at
	// ref validation, testing rollback.
	cmd = exec.Command("git", "remote", "add", "origin", "/nonexistent/repo")
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}

	r := &rig.Rig{
		Name: "rig",
		Path: root,
	}
	m := NewManager(r, git.NewGit(root), nil)

	// Allocate a name (simulates what gt sling does before AddWithOptions)
	name, err := m.AllocateName()
	if err != nil {
		t.Fatalf("AllocateName: %v", err)
	}

	// Verify name is active in pool after allocation
	activeBeforeAdd := m.namePool.ActiveCount()
	if activeBeforeAdd == 0 {
		t.Fatal("expected at least 1 active name after AllocateName")
	}

	// Try to create polecat — should fail because origin/main doesn't exist
	_, err = m.AddWithOptions(name, AddOptions{})
	if err == nil {
		t.Fatal("AddWithOptions should have failed without origin/main ref")
	}

	// Verify name was released back to pool (gt-2vs22 fix)
	activeNames := m.namePool.ActiveNames()
	for _, n := range activeNames {
		if n == name {
			t.Errorf("name %q still active in pool after failed AddWithOptions — rollback didn't release it", name)
		}
	}

	// Verify polecat directory was cleaned up
	polecatDir := m.polecatDir(name)
	if _, err := os.Stat(polecatDir); !os.IsNotExist(err) {
		t.Errorf("polecat directory %s still exists after failed AddWithOptions", polecatDir)
	}

	// Verify pending marker was cleaned up
	pendingPath := m.pendingPath(name)
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Errorf("pending marker %s still exists after failed AddWithOptions", pendingPath)
	}
}

// TestAddWithOptions_RollbackCleansWorktree verifies that when AddWithOptions fails
// AFTER the worktree is created (e.g., agent bead creation fails), the worktree
// registration is cleaned up along with the directory and pool name.
// Regression test for gt-2vs22.
func TestAddWithOptions_RollbackCleansWorktree(t *testing.T) {
	root := t.TempDir()

	// Create mayor/rig directory structure
	mayorRig := filepath.Join(root, "mayor", "rig")
	if err := os.MkdirAll(mayorRig, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Initialize git repo with a commit
	cmd := exec.Command("git", "init")
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(mayorRig, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mayorGit := git.NewGit(mayorRig)
	if err := mayorGit.Add("."); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := mayorGit.Commit("Initial commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// Set up origin/main ref (so worktree creation succeeds)
	cmd = exec.Command("git", "remote", "add", "origin", mayorRig)
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "update-ref", "refs/remotes/origin/main", "HEAD")
	cmd.Dir = mayorRig
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git update-ref: %v\n%s", err, out)
	}

	// Install a mock bd that FAILS on create (simulates agent bead creation failure)
	binDir := t.TempDir()
	if runtime.GOOS == "windows" {
		psPath := filepath.Join(binDir, "bd.ps1")
		psScript := `$cmd = ''
foreach ($arg in $args) {
  if ($arg -like '--*') { continue }
  $cmd = $arg
  break
}
switch ($cmd) {
  'create' {
    Write-Error 'error: database not initialized'
    exit 1
  }
  default { exit 0 }
}
`
		cmdScript := "@echo off\r\npwsh -NoProfile -NoLogo -File \"" + psPath + "\" %*\r\n"
		if err := os.WriteFile(psPath, []byte(psScript), 0644); err != nil {
			t.Fatalf("write mock bd.ps1: %v", err)
		}
		if err := os.WriteFile(filepath.Join(binDir, "bd.cmd"), []byte(cmdScript), 0644); err != nil {
			t.Fatalf("write mock bd.cmd: %v", err)
		}
	} else {
		script := `#!/bin/sh
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done
case "$cmd" in
  create)
    echo "error: database not initialized" >&2
    exit 1
    ;;
  *)
    exit 0
    ;;
esac
`
		if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
			t.Fatalf("write mock bd: %v", err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Create rig-level .beads directory
	rigBeads := filepath.Join(root, ".beads")
	if err := os.MkdirAll(rigBeads, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	mayorBeads := filepath.Join(mayorRig, ".beads")
	if err := os.MkdirAll(mayorBeads, 0755); err != nil {
		t.Fatalf("mkdir mayor .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigBeads, "redirect"), []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}
	// Write custom-types sentinel so EnsureCustomTypes is a no-op
	_ = os.WriteFile(filepath.Join(mayorBeads, ".gt-types-configured"), []byte("v1\n"), 0644)

	r := &rig.Rig{
		Name: "rig",
		Path: root,
	}
	m := NewManager(r, git.NewGit(root), nil)

	// Allocate a name
	name, err := m.AllocateName()
	if err != nil {
		t.Fatalf("AllocateName: %v", err)
	}

	// AddWithOptions should fail at agent bead creation (mock bd fails on create)
	_, err = m.AddWithOptions(name, AddOptions{})
	if err == nil {
		t.Fatal("AddWithOptions should have failed with mock bd failing on create")
	}

	// Verify name was released back to pool
	activeNames := m.namePool.ActiveNames()
	for _, n := range activeNames {
		if n == name {
			t.Errorf("name %q still active in pool after failed AddWithOptions — rollback didn't release it", name)
		}
	}

	// Verify polecat directory was cleaned up
	polecatDir := m.polecatDir(name)
	if _, err := os.Stat(polecatDir); !os.IsNotExist(err) {
		t.Errorf("polecat directory %s still exists after rollback", polecatDir)
	}

	// Verify worktree registration was cleaned up from git.
	// The branch ref may remain (cleaned later by CleanupStaleBranches),
	// but the worktree entry should be removed so git doesn't track a stale path.
	clonePath := filepath.Join(polecatDir, r.Name)
	cmd = exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = mayorRig
	out, cmdErr := cmd.CombinedOutput()
	if cmdErr == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, clonePath) {
				t.Errorf("stale worktree entry for %s still registered in git after rollback", clonePath)
			}
		}
	}
}

// TestAllocateAndAdd_NoDuplicateNames verifies that concurrent AllocateAndAdd
// calls never produce duplicate polecat names (GH#2215). Each goroutine will
// fail at worktree creation (no origin/main), but the allocated names must
// all be unique — the race condition would show as duplicate names.
func TestAllocateAndAdd_NoDuplicateNames(t *testing.T) {
	const concurrency = 20
	root := t.TempDir()

	// Create mayor/rig directory structure
	mayorRig := filepath.Join(root, "mayor", "rig")
	if err := os.MkdirAll(mayorRig, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Initialize git repo with origin remote (will fail fetch, that's expected)
	cmdInit := exec.Command("git", "init")
	cmdInit.Dir = mayorRig
	if out, err := cmdInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(mayorRig, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mayorGit := git.NewGit(mayorRig)
	if err := mayorGit.Add("."); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := mayorGit.Commit("Initial commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	cmdRemote := exec.Command("git", "remote", "add", "origin", "/nonexistent/repo")
	cmdRemote.Dir = mayorRig
	if out, err := cmdRemote.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}

	r := &rig.Rig{
		Name: "rig",
		Path: root,
	}
	m := NewManager(r, git.NewGit(root), nil)

	// Launch concurrent AllocateAndAdd calls. They will fail at worktree
	// creation (no origin/main), but the names they attempt must be unique.
	type result struct {
		name string
		err  error
	}
	results := make(chan result, concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			name, _, err := m.AllocateAndAdd(AddOptions{})
			results <- result{name: name, err: err}
		}()
	}

	// Collect results
	seen := make(map[string]int)
	for i := 0; i < concurrency; i++ {
		r := <-results
		if r.name != "" {
			seen[r.name]++
		}
	}

	// Verify no duplicate names
	for name, count := range seen {
		if count > 1 {
			t.Errorf("name %q allocated %d times — race condition (GH#2215)", name, count)
		}
	}
}

// TestReuseIdlePolecat_KillsLiveSession verifies that ReuseIdlePolecat kills
// an existing live (non-stale) tmux session instead of returning ErrSessionRunning.
// This is the regression test for the sling-reuse-stale-session bug: idle polecats
// with a live Claude session at a dead ❯ prompt must have their session killed so
// StartSession can create a fresh session with a proper gt prime --hook cycle.
func TestReuseIdlePolecat_KillsLiveSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux not supported on Windows")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	townRoot := t.TempDir()
	rigName := "testreuse"
	rigPath := filepath.Join(townRoot, rigName)
	polecatName := "toast"

	// Create minimal polecat directory structure
	polecatDir := filepath.Join(rigPath, "polecats", polecatName)
	if err := os.MkdirAll(polecatDir, 0755); err != nil {
		t.Fatalf("mkdir polecat dir: %v", err)
	}

	// Register a unique prefix for session naming
	reg := session.NewPrefixRegistry()
	reg.Register("gt", rigName)
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })

	tm := tmux.NewTmux()
	r := &rig.Rig{Name: rigName, Path: rigPath}
	mgr := NewManager(r, git.NewGit(rigPath), tm)

	// Create a live tmux session (simulates Claude sitting at ❯ after gt done)
	sessMgr := NewSessionManager(tm, r)
	sessionName := sessMgr.SessionName(polecatName)
	if err := tm.NewSessionWithCommand(sessionName, townRoot, "sleep 300"); err != nil {
		t.Fatalf("create tmux session: %v", err)
	}
	t.Cleanup(func() { _ = tm.KillSessionWithProcesses(sessionName) })

	// Write a fresh heartbeat (simulating a session that just finished gt done
	// but hasn't gone stale yet — this is the exact scenario that previously
	// caused ReuseIdlePolecat to return ErrSessionRunning)
	TouchSessionHeartbeat(townRoot, sessionName)

	// Verify session is alive and heartbeat exists
	running, err := tm.HasSession(sessionName)
	if err != nil || !running {
		t.Fatalf("precondition: session %s should be running", sessionName)
	}
	if hb := ReadSessionHeartbeat(townRoot, sessionName); hb == nil {
		t.Fatal("precondition: heartbeat should exist")
	}

	// Call ReuseIdlePolecat — it will kill the session, then fail on worktree
	// operations (no real git repo). The important thing is it does NOT return
	// ErrSessionRunning.
	_, reuseErr := mgr.ReuseIdlePolecat(polecatName, AddOptions{})

	// Verify it did NOT return ErrSessionRunning (the old buggy behavior)
	if errors.Is(reuseErr, ErrSessionRunning) {
		t.Fatalf("ReuseIdlePolecat returned ErrSessionRunning for live session — "+
			"this is the sling-reuse-stale-session bug: idle polecats with live "+
			"sessions must have their session killed, not rejected")
	}

	// We expect an error from later steps (worktree not found), but not from session handling
	if reuseErr == nil {
		t.Fatal("expected error from worktree operations (test has no real git repo)")
	}
	if !strings.Contains(reuseErr.Error(), "worktree") {
		t.Logf("ReuseIdlePolecat error (expected worktree-related): %v", reuseErr)
	}

	// Verify the session was killed
	running, _ = tm.HasSession(sessionName)
	if running {
		t.Error("session should have been killed by ReuseIdlePolecat")
	}

	// Verify heartbeat was cleaned up
	if hb := ReadSessionHeartbeat(townRoot, sessionName); hb != nil {
		t.Error("heartbeat should have been removed after session kill")
	}
}

// TestReuseIdlePolecat_KillsStaleSession verifies that ReuseIdlePolecat also
// handles the stale-session case correctly (regression: the original code path
// that worked before the fix should still work after).
func TestReuseIdlePolecat_KillsStaleSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux not supported on Windows")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	townRoot := t.TempDir()
	rigName := "teststale"
	rigPath := filepath.Join(townRoot, rigName)
	polecatName := "marmalade"

	polecatDir := filepath.Join(rigPath, "polecats", polecatName)
	if err := os.MkdirAll(polecatDir, 0755); err != nil {
		t.Fatalf("mkdir polecat dir: %v", err)
	}

	reg := session.NewPrefixRegistry()
	reg.Register("gt", rigName)
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })

	tm := tmux.NewTmux()
	r := &rig.Rig{Name: rigName, Path: rigPath}
	mgr := NewManager(r, git.NewGit(rigPath), tm)

	sessMgr := NewSessionManager(tm, r)
	sessionName := sessMgr.SessionName(polecatName)
	if err := tm.NewSessionWithCommand(sessionName, townRoot, "sleep 300"); err != nil {
		t.Fatalf("create tmux session: %v", err)
	}
	t.Cleanup(func() { _ = tm.KillSessionWithProcesses(sessionName) })

	// Write a STALE heartbeat (old timestamp)
	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-10 * time.Minute).UTC()
	data := []byte(`{"timestamp":"` + oldTime.Format(time.RFC3339Nano) + `","state":"exiting"}`)
	if err := os.WriteFile(filepath.Join(dir, sessionName+".json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	_, reuseErr := mgr.ReuseIdlePolecat(polecatName, AddOptions{})

	// Should not return ErrSessionRunning
	if errors.Is(reuseErr, ErrSessionRunning) {
		t.Fatal("ReuseIdlePolecat should not return ErrSessionRunning for stale session")
	}

	// Session should be killed
	running, _ := tm.HasSession(sessionName)
	if running {
		t.Error("stale session should have been killed")
	}

	// Heartbeat should be cleaned up
	if hb := ReadSessionHeartbeat(townRoot, sessionName); hb != nil {
		t.Error("heartbeat should have been removed after stale session kill")
	}
}

// TestReuseIdlePolecat_NoSessionNoop verifies that ReuseIdlePolecat proceeds
// normally when there's no existing session (the most common reuse case: session
// was already killed by the Witness or expired).
func TestReuseIdlePolecat_NoSessionNoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux not supported on Windows")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	townRoot := t.TempDir()
	rigName := "testnoop"
	rigPath := filepath.Join(townRoot, rigName)
	polecatName := "jam"

	polecatDir := filepath.Join(rigPath, "polecats", polecatName)
	if err := os.MkdirAll(polecatDir, 0755); err != nil {
		t.Fatalf("mkdir polecat dir: %v", err)
	}

	reg := session.NewPrefixRegistry()
	reg.Register("gt", rigName)
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })

	tm := tmux.NewTmux()
	r := &rig.Rig{Name: rigName, Path: rigPath}
	mgr := NewManager(r, git.NewGit(rigPath), tm)

	// No tmux session, no heartbeat — the common idle case
	_, reuseErr := mgr.ReuseIdlePolecat(polecatName, AddOptions{})

	// Should not return ErrSessionRunning
	if errors.Is(reuseErr, ErrSessionRunning) {
		t.Fatal("ReuseIdlePolecat should not return ErrSessionRunning when no session exists")
	}

	// Error should be from later steps (worktree ops), not session handling
	if reuseErr == nil {
		t.Fatal("expected error from worktree operations")
	}
}
