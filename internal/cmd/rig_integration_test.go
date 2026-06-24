//go:build integration

// Package cmd contains integration tests for the rig command.
//
// Run with: go test -tags=integration ./internal/cmd -run TestRigAdd -v
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
)

// =============================================================================
// Worktree Cleanliness Test Allowlist
// =============================================================================
//
// These files are allowed to appear as untracked in agent worktrees.
// Each entry documents which component creates the file and why it's acceptable.
//
// TODO items indicate files that should eventually be removed or relocated.

// agentAllowlist maps agent type to allowed untracked files in their worktrees.
// All agents also implicitly allow .beads/ or .beads/redirect (added in checkWorktreeClean).
//
// IMPORTANT: Be very conservative about adding files here. Each entry represents
// a file that Gas Town creates inside the user's repo, which could be accidentally
// committed and pushed upstream. Prefer ephemeral context injection (gt prime) over
// on-disk files.
var agentAllowlist = map[string][]string{
	// Mayor is a clone (not worktree) - it's the canonical copy of the user's repo.
	// For tracked beads repos, bd init creates files here (runs in mayor/rig).
	"mayor": {
		"?? AGENTS.md",  // bd init: creates multi-provider instructions (tracked beads repos only)
		"?? .claude/",   // bd init: creates .claude/settings.json with onboard prompt
		"?? .gitignore", // EnsureGitignorePatterns: adds .claude/, .runtime/, .logs/, __pycache__/ patterns
	},

	// Refinery is a worktree for the merge queue processor.
	"refinery": {},

	// Crew workers are user-managed worktrees for human developers.
	"crew": {
		"?? state.json", // crew/manager.go: Gas Town metadata (TODO: migrate to beads like polecats)
		"?? .gitignore", // EnsureGitignorePatterns: adds .claude/, .runtime/, .logs/, __pycache__/ patterns
	},

	// Polecats are ephemeral worktrees for autonomous agents.
	"polecat": {
		"?? .claude/",   // bd init: creates .claude/commands/ with handoff/review slash commands
		"?? .gitignore", // EnsureGitignorePatterns: adds .claude/, .runtime/, .logs/, __pycache__/ patterns
		"?? CLAUDE.md",  // CreatePolecatCLAUDEmd: gt done instructions and lifecycle context
	},
}

// createTestGitRepo creates a minimal git repository for testing.
// Returns the path to the bare repo URL (suitable for cloning).
func createTestGitRepo(t *testing.T, name string) string {
	t.Helper()

	// Create a regular repo with initial commit
	repoDir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	// Initialize git repo with explicit main branch
	// (system default may vary, causing checkout failures)
	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Create initial file and commit
	readmePath := filepath.Join(repoDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Test Repo\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}

	commitCmds := [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", "Initial commit"},
	}
	for _, args := range commitCmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Return the path as a file:// URL
	return repoDir
}

// createTestGitRepoAt creates a git repo at the specified path (for --adopt tests).
func createTestGitRepoAt(t *testing.T, repoDir string) {
	t.Helper()

	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	readmePath := filepath.Join(repoDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Test Repo\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}

	commitCmds := [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", "Initial commit"},
	}
	for _, args := range commitCmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// setupTestTown creates a minimal Gas Town workspace for testing.
// Returns townRoot and a cleanup function.
func setupTestTown(t *testing.T) string {
	t.Helper()

	townRoot := t.TempDir()

	// Create mayor directory (required for rigs.json)
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}

	// Create empty rigs.json
	rigsPath := filepath.Join(mayorDir, "rigs.json")
	rigsConfig := &config.RigsConfig{
		Version: 1,
		Rigs:    make(map[string]config.RigEntry),
	}
	if err := config.SaveRigsConfig(rigsPath, rigsConfig); err != nil {
		t.Fatalf("save rigs.json: %v", err)
	}

	// Create .beads directory for routes
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// Create .dolt-data directory so doltserver.InitRig doesn't consider
	// a running server "orphaned" (data dir missing) and kill it.
	doltDataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatalf("mkdir .dolt-data: %v", err)
	}

	return townRoot
}

// mockBdCommand creates a fake bd binary that simulates bd behavior.
// This avoids needing bd installed for tests.
func mockBdCommand(t *testing.T) string {
	t.Helper()

	binDir := t.TempDir()
	bdPath := filepath.Join(binDir, "bd")
	logPath := filepath.Join(binDir, "bd.log")

	if runtime.GOOS == "windows" {
		bdPath = filepath.Join(binDir, "bd.cmd")
		psPath := filepath.Join(binDir, "bd.ps1")

		psScript := `# Mock bd for testing (PowerShell)
$logFile = '` + logPath + `'
$cmd = ''
foreach ($arg in $args) {
  if ($arg -like '--*') { continue }
  $cmd = $arg
  break
}

switch ($cmd) {
  'init' {
    $prefix = 'gt'
    for ($i = 0; $i -lt $args.Length; $i++) {
      $arg = $args[$i]
      if ($arg -like '--prefix=*') {
        $prefix = $arg.Substring(9)
      } elseif ($arg -eq '--prefix' -and $i + 1 -lt $args.Length) {
        $prefix = $args[$i + 1]
      }
    }
    New-Item -ItemType Directory -Force -Path .beads | Out-Null
    Set-Content -Path (Join-Path .beads 'config.yaml') -Value ("prefix: " + $prefix)
    exit 0
  }
  'migrate' { exit 0 }
  'show' {
    [Console]::Error.WriteLine('{"error":"not found"}')
    exit 1
  }
  'create' {
    Add-Content -Path $logFile -Value ($args -join ' ')
    $beadId = ''
    foreach ($arg in $args) {
      if ($arg -like '--id=*') {
        $beadId = $arg.Substring(5)
      }
    }
    Write-Output ("{""id"":""" + $beadId + """,""status"":""open"",""created_at"":""2025-01-01T00:00:00Z""}")
    exit 0
  }
  'mol' { exit 0 }
  'list' { exit 0 }
  default { exit 0 }
}
`
		cmdScript := `@echo off
pwsh -NoProfile -NoLogo -File "` + psPath + `" %*
`
		if err := os.WriteFile(psPath, []byte(psScript), 0644); err != nil {
			t.Fatalf("write mock bd ps1: %v", err)
		}
		if err := os.WriteFile(bdPath, []byte(cmdScript), 0644); err != nil {
			t.Fatalf("write mock bd cmd: %v", err)
		}
	} else {
		// Create a script that simulates bd init and other commands
		// Also logs all create commands for verification.
		// Note: beads.run() prepends --allow-stale to all commands,
		// so we need to find the actual command in the argument list.
		script := `#!/bin/sh
# Mock bd for testing
LOG_FILE="` + logPath + `"

# Find the actual command (skip global flags like --allow-stale)
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;; # skip flags
    *) cmd="$arg"; break ;;
  esac
done

case "$cmd" in
  init)
    # Create .beads directory and config.yaml
    mkdir -p .beads
    prefix="gt"
    # Handle both --prefix=value and --prefix value forms
    next_is_prefix=false
    for arg in "$@"; do
      if [ "$next_is_prefix" = true ]; then
        prefix="$arg"
        next_is_prefix=false
      else
        case "$arg" in
          --prefix=*) prefix="${arg#--prefix=}" ;;
          --prefix) next_is_prefix=true ;;
        esac
      fi
    done
    echo "prefix: $prefix" > .beads/config.yaml
    exit 0
    ;;
  migrate)
    exit 0
    ;;
  show)
    echo '{"error":"not found"}' >&2
    exit 1
    ;;
  create)
    # Log all create commands for verification
    echo "$@" >> "$LOG_FILE"
    # Extract the ID from --id=xxx argument
    bead_id=""
    for arg in "$@"; do
      case "$arg" in
        --id=*) bead_id="${arg#--id=}" ;;
      esac
    done
    # Return valid JSON for bead creation
    echo "{\"id\":\"$bead_id\",\"status\":\"open\",\"created_at\":\"2025-01-01T00:00:00Z\"}"
    exit 0
    ;;
  mol|list)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
		if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
			t.Fatalf("write mock bd: %v", err)
		}
	}

	// Prepend to PATH
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return logPath
}

// TestRigAddCreatesCorrectStructure verifies that gt rig add creates
// the expected directory structure.
func TestRigAddCreatesCorrectStructure(t *testing.T) {
	requireDoltServer(t)
	_ = mockBdCommand(t)
	townRoot := setupTestTown(t)
	bridgeDoltPidToTown(t, townRoot)
	gitURL := createTestGitRepo(t, "testproject")

	// Load rigs config
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		t.Fatalf("load rigs.json: %v", err)
	}

	// Create rig manager and add rig
	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)

	_, err = mgr.AddRig(rig.AddRigOptions{
		Name:        "testrig",
		GitURL:      gitURL,
		BeadsPrefix: "tr",
	})
	if err != nil {
		t.Fatalf("AddRig: %v", err)
	}

	rigPath := filepath.Join(townRoot, "testrig")

	// Verify directory structure
	expectedDirs := []string{
		"",             // rig root
		"mayor",        // mayor container
		"mayor/rig",    // mayor clone
		"refinery",     // refinery container
		"refinery/rig", // refinery worktree
		"witness",      // witness dir
		"polecats",     // polecats dir
		"crew",         // crew dir
		".beads",       // beads dir
		"plugins",      // plugins dir
	}

	for _, dir := range expectedDirs {
		path := filepath.Join(rigPath, dir)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("expected directory %s to exist: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("expected %s to be a directory", dir)
		}
	}

	// Verify config.json exists
	configPath := filepath.Join(rigPath, "config.json")
	if _, err := os.Stat(configPath); err != nil {
		t.Errorf("config.json not found: %v", err)
	}

	// Verify .repo.git (bare repo) exists
	bareRepoPath := filepath.Join(rigPath, ".repo.git")
	if _, err := os.Stat(bareRepoPath); err != nil {
		t.Errorf(".repo.git not found: %v", err)
	}

	// Verify mayor/rig is a git repo
	mayorRigPath := filepath.Join(rigPath, "mayor", "rig")
	gitDirPath := filepath.Join(mayorRigPath, ".git")
	if _, err := os.Stat(gitDirPath); err != nil {
		t.Errorf("mayor/rig/.git not found: %v", err)
	}

	// Verify refinery/rig is a git worktree (has .git file pointing to bare repo)
	refineryRigPath := filepath.Join(rigPath, "refinery", "rig")
	refineryGitPath := filepath.Join(refineryRigPath, ".git")
	info, err := os.Stat(refineryGitPath)
	if err != nil {
		t.Errorf("refinery/rig/.git not found: %v", err)
	} else if info.IsDir() {
		t.Errorf("refinery/rig/.git should be a file (worktree), not a directory")
	}

	// NOTE: Most agent settings are installed at startup time, not by gt rig add.
	// Exception: polecats/.claude/ is scaffolded by gt rig add so polecat sessions
	// don't fail on startup due to missing hooks (gt-ke4mj).
	parentSettingsThatShouldNotExist := []struct {
		path string
		desc string
	}{
		{filepath.Join(rigPath, "witness", ".claude", "settings.json"), "witness/.claude/settings.json"},
		{filepath.Join(rigPath, "refinery", ".claude", "settings.json"), "refinery/.claude/settings.json"},
		{filepath.Join(rigPath, "crew", ".claude", "settings.json"), "crew/.claude/settings.json"},
	}

	for _, s := range parentSettingsThatShouldNotExist {
		if _, err := os.Stat(s.path); err == nil {
			t.Errorf("%s should NOT exist after gt rig add (agents install settings at startup)", s.desc)
		}
	}

	// Polecats settings should be scaffolded by gt rig add (gt-ke4mj).
	polecatSettings := filepath.Join(rigPath, "polecats", ".claude", "settings.json")
	if _, err := os.Stat(polecatSettings); os.IsNotExist(err) {
		t.Errorf("polecats/.claude/settings.json should exist after gt rig add (scaffolded for polecat startup)")
	}
	polecatHandoff := filepath.Join(rigPath, "polecats", ".claude", "commands", "handoff.md")
	if _, err := os.Stat(polecatHandoff); os.IsNotExist(err) {
		t.Errorf("polecats/.claude/commands/handoff.md should exist after gt rig add (scaffolded for polecat startup)")
	}

	// NOTE: No per-directory CLAUDE.md/AGENTS.md is created at agent level.
	// Only ~/gt/CLAUDE.md (town-root identity anchor) exists on disk.
	// Full context is injected ephemerally by `gt prime` at session start.

	// NOTE: Settings are now installed at parent directories (e.g., witness/.claude/settings.json)
	// and passed to Claude via --settings flag. Settings no longer exist inside working directories.
	// The old settings.local.json filename should never exist (replaced by settings.json at parent dirs).
	staleSettingsThatShouldNotExist := []struct {
		path string
		desc string
	}{
		{filepath.Join(rigPath, "witness", "rig", ".claude", "settings.local.json"), "witness/rig/.claude/settings.local.json (stale filename)"},
		{filepath.Join(rigPath, "refinery", "rig", ".claude", "settings.local.json"), "refinery/rig/.claude/settings.local.json (stale filename)"},
		{filepath.Join(rigPath, "witness", "rig", ".claude", "settings.json"), "witness/rig/.claude/settings.json (settings belong at parent dir)"},
		{filepath.Join(rigPath, "refinery", "rig", ".claude", "settings.json"), "refinery/rig/.claude/settings.json (settings belong at parent dir)"},
	}

	for _, w := range staleSettingsThatShouldNotExist {
		if _, err := os.Stat(w.path); err == nil {
			t.Errorf("%s should NOT exist (settings are at parent dirs via --settings flag)", w.desc)
		}
	}

	// Verify CLAUDE.md/AGENTS.md is NOT created at any agent directory or inside source repos
	wrongClaudeMd := []struct {
		path string
		desc string
	}{
		{filepath.Join(rigPath, "mayor", "CLAUDE.md"), "mayor/CLAUDE.md (per-rig mayor is just a clone)"},
		{filepath.Join(rigPath, "mayor", "AGENTS.md"), "mayor/AGENTS.md (per-rig mayor is just a clone)"},
		{filepath.Join(rigPath, "mayor", "rig", "CLAUDE.md"), "mayor/rig/CLAUDE.md (inside source repo)"},
		{filepath.Join(rigPath, "refinery", "rig", "CLAUDE.md"), "refinery/rig/CLAUDE.md (inside source repo)"},
		{filepath.Join(rigPath, "refinery", "CLAUDE.md"), "refinery/CLAUDE.md (no per-directory bootstrap)"},
		{filepath.Join(rigPath, "refinery", "AGENTS.md"), "refinery/AGENTS.md (no per-directory bootstrap)"},
		{filepath.Join(rigPath, "witness", "CLAUDE.md"), "witness/CLAUDE.md (no per-directory bootstrap)"},
		{filepath.Join(rigPath, "witness", "AGENTS.md"), "witness/AGENTS.md (no per-directory bootstrap)"},
		{filepath.Join(rigPath, "crew", "CLAUDE.md"), "crew/CLAUDE.md (no per-directory bootstrap)"},
		{filepath.Join(rigPath, "crew", "AGENTS.md"), "crew/AGENTS.md (no per-directory bootstrap)"},
		{filepath.Join(rigPath, "polecats", "CLAUDE.md"), "polecats/CLAUDE.md (no per-directory bootstrap)"},
		{filepath.Join(rigPath, "polecats", "AGENTS.md"), "polecats/AGENTS.md (no per-directory bootstrap)"},
	}

	for _, w := range wrongClaudeMd {
		if _, err := os.Stat(w.path); err == nil {
			t.Errorf("%s should NOT exist (would pollute source repo)", w.desc)
		}
	}
}

// TestRigAddRespectsDefaultAgent verifies that gt rig add scaffolds the polecat
// config directory matching the town's default_agent setting (gt-vdx).
func TestRigAddRespectsDefaultAgent(t *testing.T) {
	requireDoltServer(t)
	_ = mockBdCommand(t)
	townRoot := setupTestTown(t)
	bridgeDoltPidToTown(t, townRoot)
	gitURL := createTestGitRepo(t, "agenttest")

	// Write a town settings file with default_agent=opencode.
	settingsDir := filepath.Join(townRoot, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}
	townSettings := config.NewTownSettings()
	townSettings.DefaultAgent = "opencode"
	if err := config.SaveTownSettings(config.TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("save town settings: %v", err)
	}

	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		t.Fatalf("load rigs.json: %v", err)
	}

	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)

	_, err = mgr.AddRig(rig.AddRigOptions{
		Name:        "agentrig",
		GitURL:      gitURL,
		BeadsPrefix: "ar",
	})
	if err != nil {
		t.Fatalf("AddRig: %v", err)
	}

	rigPath := filepath.Join(townRoot, "agentrig")

	// With default_agent=opencode, polecats/ should use .opencode/, not .claude/.
	opencodePath := filepath.Join(rigPath, "polecats", ".opencode")
	if _, err := os.Stat(opencodePath); os.IsNotExist(err) {
		t.Errorf("polecats/.opencode/ should exist when default_agent=opencode")
	}

	// .claude/ must NOT be created when default_agent=opencode.
	claudePath := filepath.Join(rigPath, "polecats", ".claude")
	if _, err := os.Stat(claudePath); err == nil {
		t.Errorf("polecats/.claude/ should NOT exist when default_agent=opencode")
	}
}

// TestRigAddInitializesBeads verifies that beads is initialized with
// the correct prefix.
func TestRigAddInitializesBeads(t *testing.T) {
	requireDoltServer(t)
	_ = mockBdCommand(t)
	townRoot := setupTestTown(t)
	bridgeDoltPidToTown(t, townRoot)
	gitURL := createTestGitRepo(t, "beadstest")

	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		t.Fatalf("load rigs.json: %v", err)
	}

	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)

	newRig, err := mgr.AddRig(rig.AddRigOptions{
		Name:        "beadstest",
		GitURL:      gitURL,
		BeadsPrefix: "bt",
	})
	if err != nil {
		t.Fatalf("AddRig: %v", err)
	}

	// Verify rig config has correct prefix
	if newRig.Config == nil {
		t.Fatal("rig.Config is nil")
	}
	if newRig.Config.Prefix != "bt" {
		t.Errorf("rig.Config.Prefix = %q, want %q", newRig.Config.Prefix, "bt")
	}

	// Verify .beads directory was created
	beadsDir := filepath.Join(townRoot, "beadstest", ".beads")
	if _, err := os.Stat(beadsDir); err != nil {
		t.Errorf(".beads directory not found: %v", err)
	}

	// Verify config.yaml exists with correct prefix
	configPath := filepath.Join(beadsDir, "config.yaml")
	if _, err := os.Stat(configPath); err != nil {
		t.Errorf(".beads/config.yaml not found: %v", err)
	} else {
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Errorf("reading config.yaml: %v", err)
		} else if !strings.Contains(string(content), "prefix: bt") && !strings.Contains(string(content), "prefix:bt") {
			t.Errorf("config.yaml doesn't contain expected prefix, got: %s", string(content))
		}
	}

	// Verify routes.jsonl does NOT exist in rig .beads — routing belongs at town level.
	// bd walks up the directory tree to find routes.jsonl; a rig-level copy would
	// shadow the town-level routes and break cross-rig routing.
	rigRoutesPath := filepath.Join(beadsDir, "routes.jsonl")
	if _, err := os.Stat(rigRoutesPath); err == nil {
		t.Errorf("routes.jsonl should NOT exist in rig .beads directory (breaks bd walk-up routing)")
	}

	// Verify issues.jsonl does NOT exist — Dolt is the only backend.
	rigIssuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if _, err := os.Stat(rigIssuesPath); err == nil {
		t.Errorf("issues.jsonl should NOT exist in rig .beads directory (Dolt-only mode)")
	}
}

// TestRigAddUpdatesRoutes verifies that routes.jsonl is updated
// with the new rig's route.
func TestRigAddUpdatesRoutes(t *testing.T) {
	requireDoltServer(t)
	_ = mockBdCommand(t)
	townRoot := setupTestTown(t)
	bridgeDoltPidToTown(t, townRoot)
	gitURL := createTestGitRepo(t, "routetest")

	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		t.Fatalf("load rigs.json: %v", err)
	}

	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)

	newRig, err := mgr.AddRig(rig.AddRigOptions{
		Name:        "routetest",
		GitURL:      gitURL,
		BeadsPrefix: "rt",
	})
	if err != nil {
		t.Fatalf("AddRig: %v", err)
	}

	// Append route to routes.jsonl (this is done by the CLI command, not AddRig)
	// The CLI command in runRigAdd calls beads.AppendRoute after AddRig succeeds
	if newRig.Config != nil && newRig.Config.Prefix != "" {
		route := beads.Route{
			Prefix: newRig.Config.Prefix + "-",
			Path:   "routetest",
		}
		if err := beads.AppendRoute(townRoot, route); err != nil {
			t.Fatalf("AppendRoute: %v", err)
		}
	}

	// Save rigs config (normally done by the command)
	if err := config.SaveRigsConfig(rigsPath, rigsConfig); err != nil {
		t.Fatalf("save rigs.json: %v", err)
	}

	// Load routes and verify the new route exists
	townBeadsDir := filepath.Join(townRoot, ".beads")
	routes, err := beads.LoadRoutes(townBeadsDir)
	if err != nil {
		t.Fatalf("LoadRoutes: %v", err)
	}

	// Find route for our rig
	var foundRoute *beads.Route
	for _, r := range routes {
		if r.Prefix == "rt-" {
			foundRoute = &r
			break
		}
	}

	if foundRoute == nil {
		t.Error("route with prefix 'rt-' not found in routes.jsonl")
		t.Logf("routes: %+v", routes)
	} else {
		// The path should point to the rig (or mayor/rig if .beads is tracked in source)
		if !strings.HasPrefix(foundRoute.Path, "routetest") {
			t.Errorf("route path = %q, want prefix 'routetest'", foundRoute.Path)
		}
	}
}

// TestRigAddUpdatesRigsJson verifies that rigs.json is updated
// with the new rig entry.
func TestRigAddUpdatesRigsJson(t *testing.T) {
	requireDoltServer(t)
	_ = mockBdCommand(t)
	townRoot := setupTestTown(t)
	bridgeDoltPidToTown(t, townRoot)
	gitURL := createTestGitRepo(t, "jsontest")

	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		t.Fatalf("load rigs.json: %v", err)
	}

	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)

	_, err = mgr.AddRig(rig.AddRigOptions{
		Name:        "jsontest",
		GitURL:      gitURL,
		BeadsPrefix: "jt",
	})
	if err != nil {
		t.Fatalf("AddRig: %v", err)
	}

	// AddRig saves rigs.json atomically — no separate save needed.

	// Reload and verify
	rigsConfig2, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		t.Fatalf("reload rigs.json: %v", err)
	}

	entry, ok := rigsConfig2.Rigs["jsontest"]
	if !ok {
		t.Error("rig 'jsontest' not found in rigs.json")
		t.Logf("rigs: %+v", rigsConfig2.Rigs)
	} else {
		if entry.GitURL != gitURL {
			t.Errorf("GitURL = %q, want %q", entry.GitURL, gitURL)
		}
		if entry.BeadsConfig == nil {
			t.Error("BeadsConfig is nil")
		} else if entry.BeadsConfig.Prefix != "jt" {
			t.Errorf("BeadsConfig.Prefix = %q, want %q", entry.BeadsConfig.Prefix, "jt")
		}
	}
}

// TestRigAddDerivesPrefix verifies that when no prefix is specified,
// one is derived from the rig name.
func TestRigAddDerivesPrefix(t *testing.T) {
	requireDoltServer(t)
	_ = mockBdCommand(t)
	townRoot := setupTestTown(t)
	bridgeDoltPidToTown(t, townRoot)
	gitURL := createTestGitRepo(t, "myproject")

	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		t.Fatalf("load rigs.json: %v", err)
	}

	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)

	newRig, err := mgr.AddRig(rig.AddRigOptions{
		Name:   "myproject",
		GitURL: gitURL,
		// No BeadsPrefix - should be derived
	})
	if err != nil {
		t.Fatalf("AddRig: %v", err)
	}

	// For a single-word name like "myproject", the prefix should be first 2 chars
	if newRig.Config.Prefix != "my" {
		t.Errorf("derived prefix = %q, want %q", newRig.Config.Prefix, "my")
	}
}

// TestRigAddCreatesRigConfig verifies that config.json contains
// the correct rig configuration.
func TestRigAddCreatesRigConfig(t *testing.T) {
	requireDoltServer(t)
	_ = mockBdCommand(t)
	townRoot := setupTestTown(t)
	bridgeDoltPidToTown(t, townRoot)
	gitURL := createTestGitRepo(t, "configtest")

	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		t.Fatalf("load rigs.json: %v", err)
	}

	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)

	_, err = mgr.AddRig(rig.AddRigOptions{
		Name:        "configtest",
		GitURL:      gitURL,
		BeadsPrefix: "ct",
	})
	if err != nil {
		t.Fatalf("AddRig: %v", err)
	}

	// Read and verify config.json
	configPath := filepath.Join(townRoot, "configtest", "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config.json: %v", err)
	}

	var rigCfg rig.RigConfig
	if err := json.Unmarshal(data, &rigCfg); err != nil {
		t.Fatalf("parsing config.json: %v", err)
	}

	if rigCfg.Type != "rig" {
		t.Errorf("Type = %q, want 'rig'", rigCfg.Type)
	}
	if rigCfg.Name != "configtest" {
		t.Errorf("Name = %q, want 'configtest'", rigCfg.Name)
	}
	if rigCfg.GitURL != gitURL {
		t.Errorf("GitURL = %q, want %q", rigCfg.GitURL, gitURL)
	}
	if rigCfg.Beads == nil {
		t.Error("Beads config is nil")
	} else if rigCfg.Beads.Prefix != "ct" {
		t.Errorf("Beads.Prefix = %q, want 'ct'", rigCfg.Beads.Prefix)
	}
	if rigCfg.DefaultBranch == "" {
		t.Error("DefaultBranch is empty")
	}
}

// TestRigAddWithUpstreamURL verifies that gt rig add --upstream-url
// configures the upstream remote on both the bare repo and mayor clone,
// and persists the URL to config.json and rigs.json.
func TestRigAddWithUpstreamURL(t *testing.T) {
	_ = mockBdCommand(t)
	townRoot := setupTestTown(t)

	// Create two repos: one acts as the fork (origin), one as the upstream.
	forkURL := createTestGitRepo(t, "myfork")
	upstreamURL := createTestGitRepo(t, "upstream_origin")

	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		t.Fatalf("load rigs.json: %v", err)
	}

	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)

	_, err = mgr.AddRig(rig.AddRigOptions{
		Name:        "forkrig",
		GitURL:      forkURL,
		UpstreamURL: upstreamURL,
		BeadsPrefix: "fr",
	})
	if err != nil {
		t.Fatalf("AddRig: %v", err)
	}

	rigPath := filepath.Join(townRoot, "forkrig")

	t.Run("bare repo has upstream remote", func(t *testing.T) {
		bareGit := git.NewGitWithDir(filepath.Join(rigPath, ".repo.git"), "")
		got, err := bareGit.GetUpstreamURL()
		if err != nil {
			t.Fatalf("GetUpstreamURL on bare repo: %v", err)
		}
		if got != upstreamURL {
			t.Errorf("bare repo upstream = %q, want %q", got, upstreamURL)
		}
	})

	t.Run("mayor clone has upstream remote", func(t *testing.T) {
		mayorGit := git.NewGit(filepath.Join(rigPath, "mayor", "rig"))
		got, err := mayorGit.GetUpstreamURL()
		if err != nil {
			t.Fatalf("GetUpstreamURL on mayor: %v", err)
		}
		if got != upstreamURL {
			t.Errorf("mayor upstream = %q, want %q", got, upstreamURL)
		}
	})

	t.Run("config.json persists upstream_url", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(rigPath, "config.json"))
		if err != nil {
			t.Fatalf("reading config.json: %v", err)
		}
		var rigCfg rig.RigConfig
		if err := json.Unmarshal(data, &rigCfg); err != nil {
			t.Fatalf("parsing config.json: %v", err)
		}
		if rigCfg.UpstreamURL != upstreamURL {
			t.Errorf("config.json UpstreamURL = %q, want %q", rigCfg.UpstreamURL, upstreamURL)
		}
	})

	t.Run("rigs.json persists upstream_url", func(t *testing.T) {
		if err := config.SaveRigsConfig(rigsPath, rigsConfig); err != nil {
			t.Fatalf("save rigs.json: %v", err)
		}
		reloaded, err := config.LoadRigsConfig(rigsPath)
		if err != nil {
			t.Fatalf("reload rigs.json: %v", err)
		}
		entry, ok := reloaded.Rigs["forkrig"]
		if !ok {
			t.Fatal("rig 'forkrig' not found in rigs.json")
		}
		if entry.UpstreamURL != upstreamURL {
			t.Errorf("rigs.json UpstreamURL = %q, want %q", entry.UpstreamURL, upstreamURL)
		}
	})
}

// TestRigAddCreatesAgentDirs verifies that agent state files are created.
func TestRigAddCreatesAgentDirs(t *testing.T) {
	requireDoltServer(t)
	_ = mockBdCommand(t)
	townRoot := setupTestTown(t)
	bridgeDoltPidToTown(t, townRoot)
	gitURL := createTestGitRepo(t, "agenttest")

	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		t.Fatalf("load rigs.json: %v", err)
	}

	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)

	_, err = mgr.AddRig(rig.AddRigOptions{
		Name:        "agenttest",
		GitURL:      gitURL,
		BeadsPrefix: "at",
	})
	if err != nil {
		t.Fatalf("AddRig: %v", err)
	}

	rigPath := filepath.Join(townRoot, "agenttest")

	// Verify agent directories exist (state.json files are no longer created)
	expectedDirs := []string{
		"witness",
		"refinery",
		"mayor",
	}

	for _, dir := range expectedDirs {
		path := filepath.Join(rigPath, dir)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("expected directory %s to exist: %v", dir, err)
		} else if !info.IsDir() {
			t.Errorf("expected %s to be a directory", dir)
		}
	}
}

// TestRigAddRejectsInvalidNames verifies that rig names with invalid
// characters are rejected.
func TestRigAddRejectsInvalidNames(t *testing.T) {
	_ = mockBdCommand(t)
	townRoot := setupTestTown(t)
	gitURL := createTestGitRepo(t, "validname")

	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		t.Fatalf("load rigs.json: %v", err)
	}

	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)

	// Characters that break agent ID parsing (hyphens, dots, spaces)
	// Note: underscores are allowed
	invalidNames := []string{
		"my-rig",       // hyphens break agent ID parsing
		"my.rig",       // dots break parsing
		"my rig",       // spaces are invalid
		"my-multi-rig", // multiple hyphens
	}

	for _, name := range invalidNames {
		t.Run(name, func(t *testing.T) {
			_, err := mgr.AddRig(rig.AddRigOptions{
				Name:   name,
				GitURL: gitURL,
			})
			if err == nil {
				t.Errorf("AddRig(%q) should have failed", name)
			} else if !strings.Contains(err.Error(), "invalid characters") {
				t.Errorf("AddRig(%q) error = %v, want 'invalid characters'", name, err)
			}
		})
	}
}

// TestRigAddCreatesAgentBeads verifies that gt rig add creates
// witness and refinery agent beads via the manager's initAgentBeads.
func TestRigAddCreatesAgentBeads(t *testing.T) {
	requireDoltServer(t)
	townRoot := setupTestTown(t)
	bridgeDoltPidToTown(t, townRoot)
	gitURL := createTestGitRepo(t, "agentbeadtest")

	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		t.Fatalf("load rigs.json: %v", err)
	}

	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)

	// AddRig internally calls initAgentBeads which creates witness and refinery beads
	newRig, err := mgr.AddRig(rig.AddRigOptions{
		Name:        "agentbeadtest",
		GitURL:      gitURL,
		BeadsPrefix: "ab",
	})
	if err != nil {
		t.Fatalf("AddRig: %v", err)
	}

	// Expected bead IDs that initAgentBeads should create
	witnessID := beads.WitnessBeadIDWithPrefix(newRig.Config.Prefix, "agentbeadtest")
	refineryID := beads.RefineryBeadIDWithPrefix(newRig.Config.Prefix, "agentbeadtest")

	expectedIDs := []struct {
		id   string
		desc string
	}{
		{witnessID, "witness agent bead"},
		{refineryID, "refinery agent bead"},
	}

	rigBeads := beads.NewWithBeadsDir(newRig.Path, beads.ResolveBeadsDir(newRig.Path))
	for _, expected := range expectedIDs {
		if _, err := rigBeads.Show(expected.id); err != nil {
			t.Errorf("expected %s (%s) to exist: %v", expected.id, expected.desc, err)
		}
	}
}

// TestAgentBeadIDs verifies the agent bead ID generation functions.
func TestAgentBeadIDs(t *testing.T) {
	tests := []struct {
		name     string
		fn       func() string
		expected string
	}{
		{
			"WitnessBeadIDWithPrefix",
			func() string { return beads.WitnessBeadIDWithPrefix("ab", "myrig") },
			"ab-myrig-witness",
		},
		{
			"RefineryBeadIDWithPrefix",
			func() string { return beads.RefineryBeadIDWithPrefix("ab", "myrig") },
			"ab-myrig-refinery",
		},
		{
			"RigBeadIDWithPrefix",
			func() string { return beads.RigBeadIDWithPrefix("ab", "myrig") },
			"ab-rig-myrig",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.fn()
			if result != tc.expected {
				t.Errorf("got %q, want %q", result, tc.expected)
			}
		})
	}
}

// TestAgentWorktreesStayClean verifies that after gt install, gt rig add, and
// agent creation, all agent worktrees have no unexpected Gas Town files.
//
// This is a critical invariant: user repos should stay clean. The only allowed
// Gas Town file is .beads/redirect which points to the shared rig-level beads.
//
// Agents tested:
// - Mayor: mayor/rig/ (clone, created by gt rig add)
// - Refinery: refinery/rig/ (worktree, created by gt rig add)
// - Crew: crew/<name>/ (worktree, created by gt crew add)
// - Polecat: polecats/<name>/<rigname>/ (worktree, created by gt polecat identity add)
//
// Known issues this test catches:
// - Extra files in .beads/ beyond redirect (e.g., PRIME.md, databases)
// - AGENTS.md being copied/created in worktrees
// - CLAUDE.md being created in non-polecat worktrees (polecats need it for gt done)
// - Any other Gas Town artifacts polluting the repo
//
// Tests two scenarios:
// - Repo WITHOUT tracked .beads/ (clean repo)
// - Repo WITH tracked .beads/ (simulates beads project)
func TestAgentWorktreesStayClean(t *testing.T) {
	// Skip if bd is not available (required for beads initialization)
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping integration test")
	}
	requireDoltServer(t)

	testCases := []struct {
		name            string
		hasTrackedBeads bool
	}{
		{"RepoWithoutBeads", false},
		{"RepoWithTrackedBeads", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			runAgentCleanTest(t, tc.hasTrackedBeads)
		})
	}
}

// agentWorktree describes an agent's worktree to check for cleanliness.
type agentWorktree struct {
	name      string   // Human-readable name (e.g., "mayor", "polecat")
	path      string   // Path to the worktree
	allowlist []string // Additional allowlisted files beyond .beads/redirect
	isClone   bool     // True if this is a clone (not worktree) - has different expectations
}

// runAgentCleanTest runs the agent worktree cleanliness test for all agent types.
// If hasTrackedBeads is true, the source repo will have a tracked .beads/ directory.
func runAgentCleanTest(t *testing.T, hasTrackedBeads bool) {
	t.Helper()

	tmpDir := t.TempDir()
	configureTestGitIdentity(t, tmpDir)
	hqPath := filepath.Join(tmpDir, "test-hq")

	// Build gt binary for testing
	gtBinary := buildGT(t)

	// Step 1: Create test git repo with some committed files
	repoDir := filepath.Join(tmpDir, "user-project")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	// Initialize git repo
	gitCmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	}
	for _, args := range gitCmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Create some project files
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# User Project\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	// Optionally create tracked .beads/ directory (simulates beads-enabled project)
	if hasTrackedBeads {
		beadsDir := filepath.Join(repoDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("mkdir .beads: %v", err)
		}
		// Create minimal beads files that would be tracked
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("prefix: test\n"), 0644); err != nil {
			t.Fatalf("write config.yaml: %v", err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, ".gitignore"), []byte("*.db\n*.db-*\n"), 0644); err != nil {
			t.Fatalf("write .gitignore: %v", err)
		}
	}

	// Commit all files
	commitCmds := [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", "Initial commit"},
	}
	for _, args := range commitCmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Step 2: Run gt install
	cmd := exec.Command(gtBinary, "install", hqPath, "--name", "test-town")
	cmd.Env = append(os.Environ(), "HOME="+tmpDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt install failed: %v\nOutput: %s", err, output)
	}
	t.Logf("gt install output:\n%s", output)

	// Bridge the test Dolt server PID so AddRig's IsRunning check passes.
	bridgeDoltPidToTown(t, hqPath)

	// Step 3: Add rig using Manager API (CLI rejects local paths since URL validation was added)
	// Use different prefix based on whether source has tracked beads
	prefix := "tr"
	if hasTrackedBeads {
		prefix = "test" // Must match the prefix in source repo's .beads/config.yaml
	}
	rigsPath := filepath.Join(hqPath, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		t.Fatalf("load rigs.json: %v", err)
	}
	g := git.NewGit(hqPath)
	mgr := rig.NewManager(hqPath, rigsConfig, g)
	_, err = mgr.AddRig(rig.AddRigOptions{
		Name:        "testrig",
		GitURL:      repoDir,
		BeadsPrefix: prefix,
	})
	if err != nil {
		t.Fatalf("AddRig failed: %v", err)
	}
	if err := config.SaveRigsConfig(rigsPath, rigsConfig); err != nil {
		t.Fatalf("save rigs.json: %v", err)
	}

	// Append route to routes.jsonl (the CLI does this after AddRig, but we're using the Go API)
	route := beads.Route{
		Prefix: prefix + "-",
		Path:   "testrig",
	}
	if err := beads.AppendRoute(hqPath, route); err != nil {
		t.Fatalf("AppendRoute: %v", err)
	}

	// Step 4: Create a crew member
	cmd = exec.Command(gtBinary, "crew", "add", "testcrew", "--rig", "testrig")
	cmd.Dir = hqPath
	cmd.Env = append(os.Environ(), "HOME="+tmpDir, "GT_ROOT="+hqPath)
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt crew add failed: %v\nOutput: %s", err, output)
	}
	t.Logf("gt crew add output:\n%s", output)

	// Step 5: Create a polecat (non-fatal: beads infrastructure may not support
	// agent bead creation in environments without a running Dolt server).
	// Use a context with timeout to avoid the 10-retry exponential backoff
	// consuming the entire test timeout.
	polecatCreated := false
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd = exec.CommandContext(ctx, gtBinary, "polecat", "add", "testrig", "TestCat")
	cmd.Dir = hqPath
	cmd.Env = append(os.Environ(), "HOME="+tmpDir, "GT_ROOT="+hqPath)
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Logf("gt polecat identity add failed (non-fatal, beads may not be available): %v", err)
	} else {
		polecatCreated = true
		t.Logf("gt polecat identity add output:\n%s", output)
	}

	// Step 6: Define all agent worktrees to check
	rigPath := filepath.Join(hqPath, "testrig")

	agents := []agentWorktree{
		{
			name:      "mayor",
			path:      filepath.Join(rigPath, "mayor", "rig"),
			isClone:   true,
			allowlist: agentAllowlist["mayor"],
		},
		{
			name:      "refinery",
			path:      filepath.Join(rigPath, "refinery", "rig"),
			isClone:   false,
			allowlist: agentAllowlist["refinery"],
		},
		{
			name:      "crew",
			path:      filepath.Join(rigPath, "crew", "testcrew"),
			isClone:   false,
			allowlist: agentAllowlist["crew"],
		},
	}

	if polecatCreated {
		// Find polecat worktree path (handles both old and new structure)
		polecatPath := filepath.Join(rigPath, "polecats", "TestCat", "testrig")
		if _, err := os.Stat(polecatPath); os.IsNotExist(err) {
			polecatPath = filepath.Join(rigPath, "polecats", "TestCat")
		}
		agents = append(agents, agentWorktree{
			name:      "polecat",
			path:      polecatPath,
			isClone:   false,
			allowlist: agentAllowlist["polecat"],
		})
	}

	// Step 7: Check each agent worktree
	var allFailures []string
	for _, agent := range agents {
		t.Run(agent.name, func(t *testing.T) {
			failures := checkWorktreeClean(t, agent, hasTrackedBeads)
			if len(failures) > 0 {
				allFailures = append(allFailures, fmt.Sprintf("%s: %v", agent.name, failures))
			}
		})
	}

	if len(allFailures) > 0 {
		t.Logf("Summary of failures across all agents:\n%s", strings.Join(allFailures, "\n"))
	}
}

// checkWorktreeClean checks a single agent worktree for unexpected files.
// Returns a list of unexpected files found.
func checkWorktreeClean(t *testing.T, agent agentWorktree, hasTrackedBeads bool) []string {
	t.Helper()

	// Verify the worktree exists
	if _, err := os.Stat(agent.path); os.IsNotExist(err) {
		t.Fatalf("%s worktree not found at %s", agent.name, agent.path)
	}

	// Run git status --porcelain
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = agent.path
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status failed for %s: %v\nOutput: %s", agent.name, err, output)
	}

	// Build allowlist for this agent
	// Base allowlist: .beads/ directory or .beads/redirect file
	allowlist := map[string]bool{
		"?? .beads/":         true, // Directory shown when .beads/ not in source
		"?? .beads/redirect": true, // File shown when .beads/ is tracked in source
	}
	// When source repo has tracked .beads/, bd init generates additional files
	if hasTrackedBeads {
		// These files are generated by bd init and are expected
		// Note: TrimSpace removes leading space from porcelain format, so " M" becomes "M"
		allowlist["M .beads/.gitignore"] = true            // Modified .gitignore
		allowlist["?? .beads/PRIME.md"] = true             // Priming context file
		allowlist["?? .beads/README.md"] = true            // Beads README
		allowlist["?? .beads/interactions.jsonl"] = true   // Interactions log
		allowlist["?? .beads/issues.jsonl"] = true         // Issues log
		allowlist["?? .beads/metadata.json"] = true        // Beads metadata
		allowlist["M .beads/metadata.json"] = true         // Tracked metadata is rewritten to the active Dolt server in tracked-beads mode
		allowlist["?? .beads/.gt-types-configured"] = true // Custom types sentinel
		allowlist["?? .beads/.locks/"] = true              // Beads lock files directory
		allowlist["?? .beads/dolt-access.lock"] = true     // Dolt access lock
		allowlist["?? .beads/dolt/"] = true                // Dolt database directory
		allowlist["?? .beads/hooks/"] = true               // Beads hooks directory
		allowlist["?? .gitattributes"] = true              // Git attributes for beads
		allowlist["?? AGENTS.md"] = true                   // Multi-provider bootstrap
	}
	// Add agent-specific allowlist
	for _, pattern := range agent.allowlist {
		allowlist[pattern] = true
	}

	// Parse git status output
	statusOutput := strings.TrimSpace(string(output))
	var unexpectedFiles []string

	if statusOutput != "" {
		for _, line := range strings.Split(statusOutput, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if !allowlist[line] {
				unexpectedFiles = append(unexpectedFiles, line)
			}
		}
	}

	// On failure, output detailed debug info
	if len(unexpectedFiles) > 0 {
		t.Logf("=== FAILURE: %s worktree is not clean ===", agent.name)
		t.Logf("Path: %s", agent.path)
		t.Logf("Is clone: %v", agent.isClone)
		t.Logf("Has tracked .beads in source: %v", hasTrackedBeads)

		// List all files
		t.Logf("\n=== All files in %s worktree ===", agent.name)
		findCmd := exec.Command("find", ".", "-type", "f", "-not", "-path", "./.git/*")
		findCmd.Dir = agent.path
		findOutput, _ := findCmd.CombinedOutput()
		t.Logf("%s", findOutput)

		// Show .beads contents if it exists
		beadsPath := filepath.Join(agent.path, ".beads")
		if _, err := os.Stat(beadsPath); err == nil {
			t.Logf("\n=== Contents of .beads/ ===")
			lsCmd := exec.Command("ls", "-la")
			lsCmd.Dir = beadsPath
			lsOutput, _ := lsCmd.CombinedOutput()
			t.Logf("%s", lsOutput)
		}

		// Show full git status
		t.Logf("\n=== Full git status ===")
		statusCmd := exec.Command("git", "status")
		statusCmd.Dir = agent.path
		statusFullOutput, _ := statusCmd.CombinedOutput()
		t.Logf("%s", statusFullOutput)

		// Report each unexpected file
		for _, file := range unexpectedFiles {
			t.Errorf("UNEXPECTED FILE in %s: %s", agent.name, file)
		}
	} else {
		t.Logf("SUCCESS: %s worktree is clean", agent.name)
	}

	return unexpectedFiles
}
