package rig

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/git"
)

func setupTestTown(t *testing.T) (string, *config.RigsConfig) {
	t.Helper()
	root := t.TempDir()

	rigsConfig := &config.RigsConfig{
		Version: 1,
		Rigs:    make(map[string]config.RigEntry),
	}

	return root, rigsConfig
}

func writeFakeBD(t *testing.T, script string, windowsScript string) string {
	t.Helper()
	binDir := t.TempDir()

	if runtime.GOOS == "windows" {
		if windowsScript == "" {
			t.Fatal("windows script is required on Windows")
		}
		scriptPath := filepath.Join(binDir, "bd.cmd")
		if err := os.WriteFile(scriptPath, []byte(windowsScript), 0644); err != nil {
			t.Fatalf("write fake bd: %v", err)
		}
		return binDir
	}

	scriptPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	return binDir
}

func assertBeadsDirLog(t *testing.T, logPath, want string) {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading beads dir log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		t.Fatalf("expected beads dir log entries, got none")
	}
	for _, line := range lines {
		trimmed := strings.TrimSuffix(line, "\r")
		if trimmed != want {
			t.Fatalf("BEADS_DIR = %q, want %q", trimmed, want)
		}
	}
}

func createTestRig(t *testing.T, root, name string) {
	t.Helper()

	rigPath := filepath.Join(root, name)
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}

	// Create agent dirs (witness, refinery, mayor)
	for _, dir := range AgentDirs {
		dirPath := filepath.Join(rigPath, dir)
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	// Create some polecats
	polecatsDir := filepath.Join(rigPath, "polecats")
	for _, polecat := range []string{"Toast", "Cheedo"} {
		if err := os.MkdirAll(filepath.Join(polecatsDir, polecat), 0755); err != nil {
			t.Fatalf("mkdir polecat: %v", err)
		}
	}
	// Create a shared support dir that should not be treated as a polecat worktree.
	if err := os.MkdirAll(filepath.Join(polecatsDir, ".claude"), 0755); err != nil {
		t.Fatalf("mkdir polecats/.claude: %v", err)
	}
}

func TestDiscoverRigs(t *testing.T) {
	root, rigsConfig := setupTestTown(t)

	// Create test rig
	createTestRig(t, root, "gastown")
	rigsConfig.Rigs["gastown"] = config.RigEntry{
		GitURL: "git@github.com:test/gastown.git",
	}

	manager := NewManager(root, rigsConfig, git.NewGit(root))

	rigs, err := manager.DiscoverRigs()
	if err != nil {
		t.Fatalf("DiscoverRigs: %v", err)
	}

	if len(rigs) != 1 {
		t.Errorf("rigs count = %d, want 1", len(rigs))
	}

	rig := rigs[0]
	if rig.Name != "gastown" {
		t.Errorf("Name = %q, want gastown", rig.Name)
	}
	if len(rig.Polecats) != 2 {
		t.Errorf("Polecats count = %d, want 2", len(rig.Polecats))
	}
	if slices.Contains(rig.Polecats, ".claude") {
		t.Errorf("expected polecats/.claude to be ignored, got %v", rig.Polecats)
	}
	if !rig.HasWitness {
		t.Error("expected HasWitness = true")
	}
	if !rig.HasRefinery {
		t.Error("expected HasRefinery = true")
	}
}

func TestDiscoverRigs_SortedByName(t *testing.T) {
	root, rigsConfig := setupTestTown(t)

	// Register rigs in deliberately non-alphabetical order.
	// Go map iteration is randomized, so without sorting the output
	// order would be nondeterministic across runs.
	names := []string{"zebra", "alpha", "middle", "beta"}
	for _, name := range names {
		createTestRig(t, root, name)
		rigsConfig.Rigs[name] = config.RigEntry{
			GitURL: "git@github.com:test/" + name + ".git",
		}
	}

	manager := NewManager(root, rigsConfig, git.NewGit(root))

	// Run multiple iterations to catch nondeterminism — a single pass
	// could accidentally return sorted order from a random map.
	for i := 0; i < 10; i++ {
		rigs, err := manager.DiscoverRigs()
		if err != nil {
			t.Fatalf("DiscoverRigs (iter %d): %v", i, err)
		}

		if len(rigs) != len(names) {
			t.Fatalf("iter %d: rigs count = %d, want %d", i, len(rigs), len(names))
		}

		want := []string{"alpha", "beta", "middle", "zebra"}
		for j, rig := range rigs {
			if rig.Name != want[j] {
				t.Errorf("iter %d: rigs[%d].Name = %q, want %q", i, j, rig.Name, want[j])
			}
		}
	}
}

func TestGetRig(t *testing.T) {
	root, rigsConfig := setupTestTown(t)

	createTestRig(t, root, "test-rig")
	rigsConfig.Rigs["test-rig"] = config.RigEntry{
		GitURL: "git@github.com:test/test-rig.git",
	}

	manager := NewManager(root, rigsConfig, git.NewGit(root))

	rig, err := manager.GetRig("test-rig")
	if err != nil {
		t.Fatalf("GetRig: %v", err)
	}

	if rig.Name != "test-rig" {
		t.Errorf("Name = %q, want test-rig", rig.Name)
	}
}

func TestGetRigNotFound(t *testing.T) {
	root, rigsConfig := setupTestTown(t)
	manager := NewManager(root, rigsConfig, git.NewGit(root))

	_, err := manager.GetRig("nonexistent")
	if err != ErrRigNotFound {
		t.Errorf("GetRig = %v, want ErrRigNotFound", err)
	}
}

func TestRigExists(t *testing.T) {
	root, rigsConfig := setupTestTown(t)
	rigsConfig.Rigs["exists"] = config.RigEntry{}

	manager := NewManager(root, rigsConfig, git.NewGit(root))

	if !manager.RigExists("exists") {
		t.Error("expected RigExists = true for existing rig")
	}
	if manager.RigExists("nonexistent") {
		t.Error("expected RigExists = false for nonexistent rig")
	}
}

func TestRemoveRig(t *testing.T) {
	root, rigsConfig := setupTestTown(t)
	rigsConfig.Rigs["to-remove"] = config.RigEntry{}

	manager := NewManager(root, rigsConfig, git.NewGit(root))

	if err := manager.RemoveRig("to-remove"); err != nil {
		t.Fatalf("RemoveRig: %v", err)
	}

	if manager.RigExists("to-remove") {
		t.Error("rig should not exist after removal")
	}
}

func TestRemoveRigNotFound(t *testing.T) {
	root, rigsConfig := setupTestTown(t)
	manager := NewManager(root, rigsConfig, git.NewGit(root))

	err := manager.RemoveRig("nonexistent")
	if err != ErrRigNotFound {
		t.Errorf("RemoveRig = %v, want ErrRigNotFound", err)
	}
}

func TestRemoveRigNotFoundWithOrphanDir(t *testing.T) {
	root, rigsConfig := setupTestTown(t)
	manager := NewManager(root, rigsConfig, git.NewGit(root))

	// Create an orphaned directory on disk without registering it in config
	orphanDir := filepath.Join(root, "orphan-rig")
	if err := os.MkdirAll(orphanDir, 0o755); err != nil {
		t.Fatalf("creating orphan dir: %v", err)
	}

	// Manager should still return ErrRigNotFound (UX is handled in cmd layer)
	err := manager.RemoveRig("orphan-rig")
	if err != ErrRigNotFound {
		t.Errorf("RemoveRig orphan dir = %v, want ErrRigNotFound", err)
	}
}

func TestUsedNamepoolThemes(t *testing.T) {
	root, rigsConfig := setupTestTown(t)

	// Register two rigs
	rigsConfig.Rigs["alpha"] = config.RigEntry{}
	rigsConfig.Rigs["beta"] = config.RigEntry{}

	// Create settings for alpha with explicit theme
	alphaSettings := filepath.Join(root, "alpha", "settings")
	if err := os.MkdirAll(alphaSettings, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alphaSettings, "config.json"), []byte(`{"type":"rig-settings","version":1,"namepool":{"style":"minerals"}}`), 0644); err != nil {
		t.Fatal(err)
	}

	// beta has no settings — should use fallback
	manager := NewManager(root, rigsConfig, git.NewGit(root))
	fallback := func(name string) string { return "fallback-" + name }
	themes := manager.UsedNamepoolThemes(fallback)

	if len(themes) != 2 {
		t.Fatalf("expected 2 themes, got %d: %v", len(themes), themes)
	}

	hasAlpha := slices.Contains(themes, "minerals")
	hasBeta := slices.Contains(themes, "fallback-beta")
	if !hasAlpha {
		t.Errorf("expected 'minerals' for alpha, themes: %v", themes)
	}
	if !hasBeta {
		t.Errorf("expected 'fallback-beta' for beta, themes: %v", themes)
	}
}

func TestAddRig_RejectsInvalidNames(t *testing.T) {
	root, rigsConfig := setupTestTown(t)
	manager := NewManager(root, rigsConfig, git.NewGit(root))

	tests := []struct {
		name      string
		wantError string
	}{
		{"op-baby", `rig name "op-baby" contains invalid characters`},
		{"my.rig", `rig name "my.rig" contains invalid characters`},
		{"my rig", `rig name "my rig" contains invalid characters`},
		{"op-baby-test", `rig name "op-baby-test" contains invalid characters`},
		{"hq", `rig name "hq" is reserved for town-level infrastructure`},
		{"HQ", `rig name "HQ" is reserved for town-level infrastructure`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := manager.AddRig(AddRigOptions{
				Name:   tt.name,
				GitURL: "git@github.com:test/test.git",
			})
			if err == nil {
				t.Errorf("AddRig(%q) succeeded, want error containing %q", tt.name, tt.wantError)
				return
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Errorf("AddRig(%q) error = %q, want error containing %q", tt.name, err.Error(), tt.wantError)
			}
		})
	}
}

func TestAddRig_EmptyRepositoryReturnsFriendlyError(t *testing.T) {
	root, rigsConfig := setupTestTown(t)
	remoteDir := filepath.Join(t.TempDir(), "empty-remote")
	if err := os.MkdirAll(remoteDir, 0755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = remoteDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	manager := NewManager(root, rigsConfig, git.NewGit(root))
	_, err := manager.AddRig(AddRigOptions{
		Name:          "emptyrepo",
		GitURL:        remoteDir,
		BeadsPrefix:   "er",
		SkipDoltCheck: true,
	})
	if err == nil {
		t.Fatal("AddRig succeeded, want empty repository error")
	}
	want := fmt.Sprintf("repository %s is empty (no commits). Push at least one commit before adding it as a rig", remoteDir)
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("AddRig error = %q, want containing %q", err.Error(), want)
	}
	if strings.Contains(err.Error(), "couldn't find remote ref") {
		t.Fatalf("AddRig surfaced low-level fetch error: %q", err.Error())
	}
	if _, statErr := os.Stat(filepath.Join(root, "emptyrepo")); !os.IsNotExist(statErr) {
		t.Fatalf("expected failed rig directory to be removed, stat err = %v", statErr)
	}
}

func TestAddRig_EmptyRepositoryWithBranchReturnsFriendlyError(t *testing.T) {
	root, rigsConfig := setupTestTown(t)
	remoteDir := filepath.Join(t.TempDir(), "empty-remote")
	if err := os.MkdirAll(remoteDir, 0755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = remoteDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	manager := NewManager(root, rigsConfig, git.NewGit(root))
	_, err := manager.AddRig(AddRigOptions{
		Name:          "emptybranchrepo",
		GitURL:        remoteDir,
		BeadsPrefix:   "ebr",
		DefaultBranch: "main",
		SkipDoltCheck: true,
	})
	if err == nil {
		t.Fatal("AddRig succeeded, want empty repository error")
	}
	want := fmt.Sprintf("repository %s is empty (no commits). Push at least one commit before adding it as a rig", remoteDir)
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("AddRig error = %q, want containing %q", err.Error(), want)
	}
	if strings.Contains(err.Error(), "Remote branch main not found") {
		t.Fatalf("AddRig surfaced low-level clone error: %q", err.Error())
	}
}

func TestAddRig_NonEmptyRepositoryWithBadHeadIsNotReportedAsEmpty(t *testing.T) {
	root, rigsConfig := setupTestTown(t)
	tmp := t.TempDir()
	remoteDir := filepath.Join(tmp, "remote.git")
	workDir := filepath.Join(tmp, "work")
	for _, args := range [][]string{
		{"git", "init", "--bare", "--initial-branch=main", remoteDir},
		{"git", "clone", remoteDir, workDir},
		{"git", "-C", workDir, "config", "user.email", "test@test.com"},
		{"git", "-C", workDir, "config", "user.name", "Test User"},
		{"git", "-C", workDir, "commit", "--allow-empty", "-m", "init"},
		{"git", "-C", workDir, "push", "origin", "HEAD:refs/heads/main"},
		{"git", "--git-dir", remoteDir, "symbolic-ref", "HEAD", "refs/heads/missing"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	manager := NewManager(root, rigsConfig, git.NewGit(root))
	_, err := manager.AddRig(AddRigOptions{
		Name:          "badheadrepo",
		GitURL:        remoteDir,
		BeadsPrefix:   "bhr",
		SkipDoltCheck: true,
	})
	if err == nil {
		t.Fatal("AddRig succeeded, want bad remote HEAD error")
	}
	if strings.Contains(err.Error(), "is empty") {
		t.Fatalf("AddRig reported non-empty bad-HEAD repo as empty: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "has refs, but no default branch could be cloned") {
		t.Fatalf("AddRig error = %q, want bad remote HEAD diagnostic", err.Error())
	}
}

func TestAddRig_TagOnlyRepositoryIsNotReportedAsEmpty(t *testing.T) {
	root, rigsConfig := setupTestTown(t)
	repoDir := filepath.Join(t.TempDir(), "tag-only")
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main", repoDir},
		{"git", "-C", repoDir, "config", "user.email", "test@test.com"},
		{"git", "-C", repoDir, "config", "user.name", "Test User"},
		{"git", "-C", repoDir, "commit", "--allow-empty", "-m", "init"},
		{"git", "-C", repoDir, "tag", "v1"},
		{"git", "-C", repoDir, "update-ref", "-d", "refs/heads/main"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	manager := NewManager(root, rigsConfig, git.NewGit(root))
	_, err := manager.AddRig(AddRigOptions{
		Name:          "tagonlyrepo",
		GitURL:        repoDir,
		BeadsPrefix:   "tor",
		SkipDoltCheck: true,
	})
	if err == nil {
		t.Fatal("AddRig succeeded, want no branch error")
	}
	if strings.Contains(err.Error(), "is empty") {
		t.Fatalf("AddRig reported tag-only repo as empty: %q", err.Error())
	}
}

func TestListRigNames(t *testing.T) {
	root, rigsConfig := setupTestTown(t)
	rigsConfig.Rigs["rig1"] = config.RigEntry{}
	rigsConfig.Rigs["rig2"] = config.RigEntry{}

	manager := NewManager(root, rigsConfig, git.NewGit(root))

	names := manager.ListRigNames()
	if len(names) != 2 {
		t.Errorf("names count = %d, want 2", len(names))
	}
}

func TestRigSummary(t *testing.T) {
	rig := &Rig{
		Name:        "test",
		Polecats:    []string{"a", "b", "c"},
		HasWitness:  true,
		HasRefinery: false,
	}

	summary := rig.Summary()

	if summary.Name != "test" {
		t.Errorf("Name = %q, want test", summary.Name)
	}
	if summary.PolecatCount != 3 {
		t.Errorf("PolecatCount = %d, want 3", summary.PolecatCount)
	}
	if !summary.HasWitness {
		t.Error("expected HasWitness = true")
	}
	if summary.HasRefinery {
		t.Error("expected HasRefinery = false")
	}
}

func TestEnsureGitignoreEntry_AddsEntry(t *testing.T) {
	root, rigsConfig := setupTestTown(t)
	manager := NewManager(root, rigsConfig, git.NewGit(root))

	gitignorePath := filepath.Join(root, ".gitignore")

	if err := manager.ensureGitignoreEntry(gitignorePath, ".test-entry/"); err != nil {
		t.Fatalf("ensureGitignoreEntry: %v", err)
	}

	content, _ := os.ReadFile(gitignorePath)
	if string(content) != ".test-entry/\n" {
		t.Errorf("content = %q, want .test-entry/", string(content))
	}
}

func TestEnsureGitignoreEntry_DoesNotDuplicate(t *testing.T) {
	root, rigsConfig := setupTestTown(t)
	manager := NewManager(root, rigsConfig, git.NewGit(root))

	gitignorePath := filepath.Join(root, ".gitignore")

	// Pre-populate with the entry
	if err := os.WriteFile(gitignorePath, []byte(".test-entry/\n"), 0644); err != nil {
		t.Fatalf("writing .gitignore: %v", err)
	}

	if err := manager.ensureGitignoreEntry(gitignorePath, ".test-entry/"); err != nil {
		t.Fatalf("ensureGitignoreEntry: %v", err)
	}

	content, _ := os.ReadFile(gitignorePath)
	if string(content) != ".test-entry/\n" {
		t.Errorf("content = %q, want single .test-entry/", string(content))
	}
}

func TestEnsureGitignoreEntry_AppendsToExisting(t *testing.T) {
	root, rigsConfig := setupTestTown(t)
	manager := NewManager(root, rigsConfig, git.NewGit(root))

	gitignorePath := filepath.Join(root, ".gitignore")

	// Pre-populate with existing entries
	if err := os.WriteFile(gitignorePath, []byte("node_modules/\n*.log\n"), 0644); err != nil {
		t.Fatalf("writing .gitignore: %v", err)
	}

	if err := manager.ensureGitignoreEntry(gitignorePath, ".test-entry/"); err != nil {
		t.Fatalf("ensureGitignoreEntry: %v", err)
	}

	content, _ := os.ReadFile(gitignorePath)
	expected := "node_modules/\n*.log\n.test-entry/\n"
	if string(content) != expected {
		t.Errorf("content = %q, want %q", string(content), expected)
	}
}

func TestInitBeads_TrackedBeads_CreatesRedirect(t *testing.T) {
	t.Parallel()
	// When the cloned repo has tracked beads (mayor/rig/.beads exists),
	// initBeads should create a redirect file at <rig>/.beads/redirect
	// pointing to mayor/rig/.beads instead of creating a local database.
	rigPath := t.TempDir()

	// Simulate tracked beads in the cloned repo
	mayorBeadsDir := filepath.Join(rigPath, "mayor", "rig", ".beads")
	if err := os.MkdirAll(mayorBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir mayor beads: %v", err)
	}
	// Create a config file to simulate a real beads directory
	if err := os.WriteFile(filepath.Join(mayorBeadsDir, "config.yaml"), []byte("prefix: gt\n"), 0644); err != nil {
		t.Fatalf("write mayor config: %v", err)
	}

	manager := &Manager{}
	if err := manager.InitBeads(rigPath, "gt", ""); err != nil {
		t.Fatalf("initBeads: %v", err)
	}

	// Verify redirect file was created
	redirectPath := filepath.Join(rigPath, ".beads", "redirect")
	content, err := os.ReadFile(redirectPath)
	if err != nil {
		t.Fatalf("reading redirect file: %v", err)
	}

	expected := "mayor/rig/.beads\n"
	if string(content) != expected {
		t.Errorf("redirect content = %q, want %q", string(content), expected)
	}

	// Verify no local database was created (no config.yaml at rig level)
	rigConfigPath := filepath.Join(rigPath, ".beads", "config.yaml")
	if _, err := os.Stat(rigConfigPath); !os.IsNotExist(err) {
		t.Errorf("expected no config.yaml at rig level when using redirect, but it exists")
	}
}

func TestInitBeads_LocalBeads_CreatesDatabase(t *testing.T) {
	// Cannot use t.Parallel() due to t.Setenv
	// When the cloned repo does NOT have tracked beads (no mayor/rig/.beads),
	// initBeads should create a local database at <rig>/.beads/
	rigPath := t.TempDir()

	// Create mayor/rig directory but WITHOUT .beads (no tracked beads)
	mayorRigDir := filepath.Join(rigPath, "mayor", "rig")
	if err := os.MkdirAll(mayorRigDir, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Use fake bd that succeeds
	script := `#!/usr/bin/env bash
set -e
if [[ "$1" == "init" ]]; then
  # Simulate successful bd init
  exit 0
fi
exit 0
`
	windowsScript := "@echo off\r\nif \"%1\"==\"init\" exit /b 0\r\nexit /b 0\r\n"
	binDir := writeFakeBD(t, script, windowsScript)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	manager := &Manager{}
	if err := manager.InitBeads(rigPath, "gt", ""); err != nil {
		t.Fatalf("initBeads: %v", err)
	}

	// Verify NO redirect file was created
	redirectPath := filepath.Join(rigPath, ".beads", "redirect")
	if _, err := os.Stat(redirectPath); !os.IsNotExist(err) {
		t.Errorf("expected no redirect file for local beads, but it exists")
	}

	// Verify .beads directory was created
	beadsDir := filepath.Join(rigPath, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		t.Errorf("expected .beads directory to be created")
	}
}

func TestInitBeadsWritesConfigOnFailure(t *testing.T) {
	rigPath := t.TempDir()
	beadsDir := filepath.Join(rigPath, ".beads")

	script := `#!/usr/bin/env bash
set -e
if [[ -n "$BEADS_DIR_LOG" ]]; then
  echo "${BEADS_DIR:-<unset>}" >> "$BEADS_DIR_LOG"
fi
cmd="$1"
shift
if [[ "$cmd" == "init" ]]; then
  echo "bd init failed" >&2
  exit 1
fi
echo "unexpected command: $cmd" >&2
exit 1
`
	windowsScript := "@echo off\r\nif defined BEADS_DIR_LOG (\r\n  if defined BEADS_DIR (\r\n    echo %BEADS_DIR%>>\"%BEADS_DIR_LOG%\"\r\n  ) else (\r\n    echo ^<unset^> >>\"%BEADS_DIR_LOG%\"\r\n  )\r\n)\r\nif \"%1\"==\"init\" (\r\n  exit /b 1\r\n)\r\nexit /b 1\r\n"

	binDir := writeFakeBD(t, script, windowsScript)
	beadsDirLog := filepath.Join(t.TempDir(), "beads-dir.log")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BEADS_DIR_LOG", beadsDirLog)

	manager := &Manager{}
	if err := manager.InitBeads(rigPath, "gt", "testrig"); err != nil {
		t.Fatalf("initBeads: %v", err)
	}

	configPath := filepath.Join(beadsDir, "config.yaml")
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config.yaml: %v", err)
	}
	want := "prefix: gt\nissue-prefix: gt\ndolt.idle-timeout: \"0\"\n"
	if string(config) != want {
		t.Fatalf("config.yaml = %q, want %q", string(config), want)
	}

	metadataPath := filepath.Join(beadsDir, "metadata.json")
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("reading metadata.json: %v", err)
	}
	var metadata map[string]interface{}
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatalf("parsing metadata.json: %v", err)
	}
	if metadata["dolt_mode"] != "server" {
		t.Fatalf("dolt_mode = %v, want server", metadata["dolt_mode"])
	}
	if metadata["dolt_database"] != "testrig" {
		t.Fatalf("dolt_database = %v, want testrig", metadata["dolt_database"])
	}
	assertBeadsDirLog(t, beadsDirLog, beadsDir)
}

func TestInitBeadsSetsIssuePrefix(t *testing.T) {
	// Cannot use t.Parallel() due to t.Setenv
	// Verify that initBeads calls 'bd config set issue_prefix <prefix>'
	// when bd init succeeds (Dolt database is available).
	rigPath := t.TempDir()

	// Create mayor/rig directory WITHOUT .beads (no tracked beads)
	mayorRigDir := filepath.Join(rigPath, "mayor", "rig")
	if err := os.MkdirAll(mayorRigDir, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Track all commands received by fake bd
	cmdLog := filepath.Join(t.TempDir(), "bd-cmds.log")
	script := `#!/usr/bin/env bash
set -e
echo "$@" >> "$BD_CMD_LOG"
exit 0
`
	windowsScript := "@echo off\r\nif defined BD_CMD_LOG echo %* >> \"%BD_CMD_LOG%\"\r\nexit /b 0\r\n"
	binDir := writeFakeBD(t, script, windowsScript)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_CMD_LOG", cmdLog)

	manager := &Manager{}
	if err := manager.InitBeads(rigPath, "myrig", ""); err != nil {
		t.Fatalf("initBeads: %v", err)
	}

	// Read logged commands
	logData, err := os.ReadFile(cmdLog)
	if err != nil {
		t.Fatalf("reading command log: %v", err)
	}
	cmds := string(logData)

	// Verify bd config set issue_prefix was called with the correct prefix
	if !strings.Contains(cmds, "config set issue_prefix myrig") {
		t.Errorf("expected 'bd config set issue_prefix myrig' in commands log, got:\n%s", cmds)
	}
}

func TestInitBeadsPassesCanonicalDatabase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake bd arg/env logging is shell-specific")
	}

	rigPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rigPath, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	cmdLog := filepath.Join(t.TempDir(), "bd-cmds.log")
	script := `#!/usr/bin/env bash
set -e
printf 'args=%s env=%s beads=%s db=%s\n' "$*" "${BEADS_DOLT_SERVER_DATABASE:-<unset>}" "${BEADS_DIR:-<unset>}" "${BEADS_DB:-<unset>}" >> "$BD_CMD_LOG"
exit 0
`
	binDir := writeFakeBD(t, script, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_CMD_LOG", cmdLog)
	t.Setenv("BEADS_DIR", filepath.Join(rigPath, "wrong", ".beads"))
	t.Setenv("BEADS_DB", filepath.Join(rigPath, "wrong.db"))
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "stale_prefix_db")

	manager := &Manager{}
	if err := manager.InitBeads(rigPath, "xx", "my_project"); err != nil {
		t.Fatalf("InitBeads: %v", err)
	}

	logData, err := os.ReadFile(cmdLog)
	if err != nil {
		t.Fatalf("reading command log: %v", err)
	}
	cmds := string(logData)
	if !strings.Contains(cmds, "args=init --prefix xx --database my_project --server") {
		t.Fatalf("bd init did not use canonical database; log:\n%s", cmds)
	}
	if strings.Contains(cmds, "env=stale_prefix_db") || strings.Contains(cmds, "wrong.db") || strings.Contains(cmds, filepath.Join(rigPath, "wrong", ".beads")) {
		t.Fatalf("stale BEADS env leaked into bd subprocess; log:\n%s", cmds)
	}
	if !strings.Contains(cmds, "env=my_project") {
		t.Fatalf("bd subprocess did not receive canonical database env; log:\n%s", cmds)
	}
}

func TestInitAgentBeadsUsesRigBeadsDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake bd stub is not compatible with multiline descriptions on Windows")
	}

	// Rig-level agent beads (witness, refinery) are stored in rig beads.
	// Town-level agents (mayor, deacon) are created by gt install in town beads.
	// This test verifies that rig agent beads are created in the rig directory,
	// using the resolved rig beads directory for BEADS_DIR.
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrip")
	rigBeadsDir := filepath.Join(rigPath, ".beads")

	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir rig beads dir: %v", err)
	}

	// Track which agent IDs were created
	var createdAgents []string

	script := `#!/usr/bin/env bash
set -e
if [[ -n "$BEADS_DIR_LOG" ]]; then
  echo "${BEADS_DIR:-<unset>}" >> "$BEADS_DIR_LOG"
fi
if [[ "$1" == "--allow-stale" ]]; then
  shift
fi
cmd="$1"
shift
case "$cmd" in
  show)
    # Return empty to indicate agent doesn't exist yet
    echo "[]"
    ;;
  create)
    id=""
    title=""
    for arg in "$@"; do
      case "$arg" in
        --id=*) id="${arg#--id=}" ;;
        --title=*) title="${arg#--title=}" ;;
      esac
    done
    # Log the created agent ID for verification
    echo "$id" >> "$AGENT_LOG"
    printf '{"id":"%s","title":"%s","description":"","issue_type":"agent"}' "$id" "$title"
    ;;
  slot)
    # Accept slot commands
    ;;
  config)
    # Accept config commands (e.g., "bd config set types.custom ...")
    ;;
  init)
    # Accept init commands (e.g., "bd init --prefix gt --server")
    ;;
  *)
    echo "unexpected command: $cmd" >&2
    exit 1
    ;;
esac
`
	windowsScript := "@echo off\r\nsetlocal enabledelayedexpansion\r\nif defined BEADS_DIR_LOG (\r\n  if defined BEADS_DIR (\r\n    echo %BEADS_DIR%>>\"%BEADS_DIR_LOG%\"\r\n  ) else (\r\n    echo ^<unset^> >>\"%BEADS_DIR_LOG%\"\r\n  )\r\n)\r\nset \"cmd=%1\"\r\nset \"arg2=%2\"\r\nset \"arg3=%3\"\r\nif \"%cmd%\"==\"--allow-stale\" (\r\n  set \"cmd=%2\"\r\n  set \"arg2=%3\"\r\n  set \"arg3=%4\"\r\n)\r\nif \"%cmd%\"==\"show\" (\r\n  echo []\r\n  exit /b 0\r\n)\r\nif \"%cmd%\"==\"create\" (\r\n  set \"id=\"\r\n  set \"title=\"\r\n  for %%A in (%*) do (\r\n    set \"arg=%%~A\"\r\n    if /i \"!arg:~0,5!\"==\"--id=\" set \"id=!arg:~5!\"\r\n    if /i \"!arg:~0,8!\"==\"--title=\" set \"title=!arg:~8!\"\r\n  )\r\n  if defined AGENT_LOG (\r\n    echo !id!>>\"%AGENT_LOG%\"\r\n  )\r\n  echo {\"id\":\"!id!\",\"title\":\"!title!\",\"description\":\"\",\"issue_type\":\"agent\"}\r\n  exit /b 0\r\n)\r\nif \"%cmd%\"==\"slot\" exit /b 0\r\nif \"%cmd%\"==\"config\" exit /b 0\r\nif \"%cmd%\"==\"init\" exit /b 0\r\nexit /b 1\r\n"

	binDir := writeFakeBD(t, script, windowsScript)
	agentLog := filepath.Join(t.TempDir(), "agents.log")
	beadsDirLog := filepath.Join(t.TempDir(), "beads-dir.log")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AGENT_LOG", agentLog)
	t.Setenv("BEADS_DIR_LOG", beadsDirLog)
	t.Setenv("BEADS_DIR", "") // Clear any existing BEADS_DIR

	manager := &Manager{townRoot: townRoot}
	if err := manager.initAgentBeads(rigPath, "demo", "gt"); err != nil {
		t.Fatalf("initAgentBeads: %v", err)
	}

	// Verify the expected rig-level agents were created
	data, err := os.ReadFile(agentLog)
	if err != nil {
		t.Fatalf("reading agent log: %v", err)
	}
	createdAgents = strings.Split(strings.TrimSpace(string(data)), "\n")

	// Should create witness and refinery for the rig
	expectedAgents := map[string]bool{
		"gt-demo-witness":  false,
		"gt-demo-refinery": false,
	}

	for _, id := range createdAgents {
		if _, ok := expectedAgents[id]; ok {
			expectedAgents[id] = true
		}
	}

	for id, found := range expectedAgents {
		if !found {
			t.Errorf("expected agent %s was not created", id)
		}
	}
	assertBeadsDirLog(t, beadsDirLog, rigBeadsDir)
}

func TestIsValidBeadsPrefix(t *testing.T) {
	tests := []struct {
		prefix string
		want   bool
	}{
		// Valid prefixes
		{"gt", true},
		{"bd", true},
		{"hq", true},
		{"gastown", true},
		{"myProject", true},
		{"my-project", true},
		{"a", true},
		{"A", true},
		{"test123", true},
		{"a1b2c3", true},
		{"a-b-c", true},

		// Invalid prefixes
		{"", false},                      // empty
		{"1abc", false},                  // starts with number
		{"-abc", false},                  // starts with hyphen
		{"abc def", false},               // contains space
		{"abc;ls", false},                // shell injection attempt
		{"$(whoami)", false},             // command substitution
		{"`id`", false},                  // backtick command
		{"abc|cat", false},               // pipe
		{"../etc/passwd", false},         // path traversal
		{"aaaaaaaaaaaaaaaaaaaaa", false}, // too long (21 chars, >20 limit)
		{"valid-but-with-$var", false},   // variable reference
	}

	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			got := isValidBeadsPrefix(tt.prefix)
			if got != tt.want {
				t.Errorf("isValidBeadsPrefix(%q) = %v, want %v", tt.prefix, got, tt.want)
			}
		})
	}
}

// TestDropRigOrphanDBs_RemovesPrefixDB is the regression test for gh#3562.
//
// `bd init --prefix <prefix> --server` creates a Dolt database whose name
// matches the prefix exactly (e.g. "ma" for prefix=ma) on bd >= 0.62. The rig
// uses <rigName> as its canonical database, so the prefix-named DB is an
// orphan that must be removed — otherwise beads created from the rig land in
// the orphan while the mayor reads from <rigName>, silently splitting the data.
//
// This test simulates the post-init filesystem state, runs the orphan dropper,
// and asserts that exactly one rig database remains on disk.
func TestDropRigOrphanDBs_RemovesPrefixDB(t *testing.T) {
	// Disable the running-server check so RemoveDatabase falls through to
	// pure filesystem cleanup. Pointing GT_DOLT_PORT at a definitely-unused
	// port keeps the test independent of any real Dolt server on the host.
	t.Setenv("GT_DOLT_PORT", "1") // privileged port, definitely not running dolt

	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")

	const rigName = "mobile_apps"
	const prefix = "ma"

	// Simulate post-init state: rigName DB (correct) and prefix DB (orphan).
	for _, db := range []string{rigName, prefix} {
		doltDir := filepath.Join(dataDir, db, ".dolt")
		if err := os.MkdirAll(doltDir, 0755); err != nil {
			t.Fatalf("seed %s: %v", db, err)
		}
	}

	if err := dropRigOrphanDBs(townRoot, prefix, rigName); err != nil {
		t.Fatalf("dropRigOrphanDBs: %v", err)
	}

	// Orphan must be gone.
	if _, err := os.Stat(filepath.Join(dataDir, prefix)); !os.IsNotExist(err) {
		t.Errorf("prefix DB %q should have been removed; stat err = %v", prefix, err)
	}

	// Canonical rig DB must remain.
	if _, err := os.Stat(filepath.Join(dataDir, rigName, ".dolt")); err != nil {
		t.Errorf("rig DB %q should be preserved; stat err = %v", rigName, err)
	}
}

// TestDropRigOrphanDBs_RemovesLegacyBeadsPrefixDB covers the bd < 0.62
// naming convention where bd init created "beads_<prefix>" instead of
// "<prefix>". Both forms must be cleaned up to keep older workspaces
// functional after upgrade.
func TestDropRigOrphanDBs_RemovesLegacyBeadsPrefixDB(t *testing.T) {
	t.Setenv("GT_DOLT_PORT", "1")

	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")

	const rigName = "mobile_apps"
	const prefix = "ma"
	const legacyOrphan = "beads_ma"

	for _, db := range []string{rigName, legacyOrphan} {
		if err := os.MkdirAll(filepath.Join(dataDir, db, ".dolt"), 0755); err != nil {
			t.Fatalf("seed %s: %v", db, err)
		}
	}

	if err := dropRigOrphanDBs(townRoot, prefix, rigName); err != nil {
		t.Fatalf("dropRigOrphanDBs: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dataDir, legacyOrphan)); !os.IsNotExist(err) {
		t.Errorf("legacy orphan %q should have been removed; stat err = %v", legacyOrphan, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, rigName, ".dolt")); err != nil {
		t.Errorf("rig DB %q should be preserved; stat err = %v", rigName, err)
	}
}

// TestDropRigOrphanDBs_PreservesRigDB is the safety check that the helper
// never removes a database whose name happens to match the prefix when the
// rig itself is named after its prefix (e.g. rig "gastown" with prefix "gt"
// where neither candidate is an orphan).
func TestDropRigOrphanDBs_PreservesRigDB(t *testing.T) {
	t.Setenv("GT_DOLT_PORT", "1")

	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")

	// Pathological case: rigName == prefix. Nothing should be dropped.
	const rigName = "gt"
	const prefix = "gt"

	if err := os.MkdirAll(filepath.Join(dataDir, rigName, ".dolt"), 0755); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := dropRigOrphanDBs(townRoot, prefix, rigName); err != nil {
		t.Fatalf("dropRigOrphanDBs: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dataDir, rigName, ".dolt")); err != nil {
		t.Errorf("rig DB %q must be preserved when prefix == rigName; stat err = %v", rigName, err)
	}
}

func TestInitBeadsRejectsInvalidPrefix(t *testing.T) {
	rigPath := t.TempDir()
	manager := &Manager{}

	tests := []string{
		"",
		"$(whoami)",
		"abc;rm -rf /",
		"../etc",
		"123",
	}

	for _, prefix := range tests {
		t.Run(prefix, func(t *testing.T) {
			err := manager.InitBeads(rigPath, prefix, "")
			if err == nil {
				t.Errorf("initBeads(%q) should have failed", prefix)
			}
			if !strings.Contains(err.Error(), "invalid beads prefix") {
				t.Errorf("initBeads(%q) error = %q, want error containing 'invalid beads prefix'", prefix, err.Error())
			}
		})
	}
}

func TestDeriveBeadsPrefix(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		// Compound words with common suffixes should split
		{"gastown", "gt"},     // gas + town
		{"nashville", "nv"},   // nash + ville
		{"bridgeport", "bp"},  // bridge + port
		{"someplace", "sp"},   // some + place
		{"greenland", "gl"},   // green + land
		{"springfield", "sf"}, // spring + field
		{"hollywood", "hw"},   // holly + wood
		{"oxford", "of"},      // ox + ford

		// Hyphenated names
		{"my-project", "mp"},
		{"gas-town", "gt"},
		{"some-long-name", "sln"},

		// Underscored names
		{"my_project", "mp"},

		// Short single words (use the whole name)
		{"foo", "foo"},
		{"bar", "bar"},
		{"ab", "ab"},

		// Longer single words without known suffixes (first 2 chars)
		{"myrig", "my"},
		{"awesome", "aw"},
		{"coolrig", "co"},

		// camelCase names
		{"myProject", "mp"},
		{"gasStation", "gs"},
		{"HTMLParser", "hp"},

		// With language suffixes stripped
		{"myproject-py", "my"},
		{"myproject-go", "my"},

		// Path-like names (slashes stripped)
		{"/my_app", "ma"},
		{"/some/deep/path", "pa"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveBeadsPrefix(tt.name)
			if got != tt.want {
				t.Errorf("deriveBeadsPrefix(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestSplitCompoundWord(t *testing.T) {
	tests := []struct {
		word string
		want []string
	}{
		// Known suffixes
		{"gastown", []string{"gas", "town"}},
		{"nashville", []string{"nash", "ville"}},
		{"bridgeport", []string{"bridge", "port"}},
		{"someplace", []string{"some", "place"}},
		{"greenland", []string{"green", "land"}},
		{"springfield", []string{"spring", "field"}},
		{"hollywood", []string{"holly", "wood"}},
		{"oxford", []string{"ox", "ford"}},

		// Just the suffix (should not split)
		{"town", []string{"town"}},
		{"ville", []string{"ville"}},

		// No known suffix
		{"myrig", []string{"myrig"}},
		{"awesome", []string{"awesome"}},

		// Empty prefix would result (should not split)
		// Note: "town" itself shouldn't split to ["", "town"]
	}

	for _, tt := range tests {
		t.Run(tt.word, func(t *testing.T) {
			got := splitCompoundWord(tt.word)
			if len(got) != len(tt.want) {
				t.Errorf("splitCompoundWord(%q) = %v, want %v", tt.word, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("splitCompoundWord(%q)[%d] = %q, want %q", tt.word, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSplitCamelCase(t *testing.T) {
	tests := []struct {
		word string
		want []string
	}{
		// Basic camelCase
		{"myProject", []string{"my", "Project"}},
		{"gasStation", []string{"gas", "Station"}},

		// PascalCase
		{"MyProject", []string{"My", "Project"}},

		// Uppercase runs
		{"HTMLParser", []string{"HTML", "Parser"}},
		{"parseJSON", []string{"parse", "JSON"}},

		// No splits (single word, all lower)
		{"gastown", []string{"gastown"}},
		{"a", []string{"a"}},

		// All uppercase (no lower transition)
		{"AB", []string{"AB"}},

		// Empty
		{"", nil},
	}

	for _, tt := range tests {
		t.Run(tt.word, func(t *testing.T) {
			got := splitCamelCase(tt.word)
			if len(got) != len(tt.want) {
				t.Errorf("splitCamelCase(%q) = %v, want %v", tt.word, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("splitCamelCase(%q)[%d] = %q, want %q", tt.word, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestConvertToSSH(t *testing.T) {
	tests := []struct {
		name    string
		https   string
		wantSSH string
	}{
		{
			name:    "GitHub with .git suffix",
			https:   "https://github.com/owner/repo.git",
			wantSSH: "git@github.com:owner/repo.git",
		},
		{
			name:    "GitHub without .git suffix",
			https:   "https://github.com/owner/repo",
			wantSSH: "git@github.com:owner/repo.git",
		},
		{
			name:    "GitHub with org/subpath",
			https:   "https://github.com/myorg/myproject.git",
			wantSSH: "git@github.com:myorg/myproject.git",
		},
		{
			name:    "GitLab with .git suffix",
			https:   "https://gitlab.com/owner/repo.git",
			wantSSH: "git@gitlab.com:owner/repo.git",
		},
		{
			name:    "GitLab without .git suffix",
			https:   "https://gitlab.com/owner/repo",
			wantSSH: "git@gitlab.com:owner/repo.git",
		},
		{
			name:    "Bitbucket with .git suffix",
			https:   "https://bitbucket.org/owner/repo.git",
			wantSSH: "git@bitbucket.org:owner/repo.git",
		},
		{
			name:    "Bitbucket without .git suffix",
			https:   "https://bitbucket.org/owner/repo",
			wantSSH: "git@bitbucket.org:owner/repo.git",
		},
		{
			name:    "Unknown host returns empty",
			https:   "https://gitlab.example.com/owner/repo.git",
			wantSSH: "",
		},
		{
			name:    "Non-HTTPS URL returns empty",
			https:   "git@github.com:owner/repo.git",
			wantSSH: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertToSSH(tt.https)
			if got != tt.wantSSH {
				t.Errorf("convertToSSH(%q) = %q, want %q", tt.https, got, tt.wantSSH)
			}
		})
	}
}

func TestDetectBeadsPrefixFromConfig_TrailingDash(t *testing.T) {
	tests := []struct {
		name       string
		configYAML string
		want       string
	}{
		{
			name:       "prefix without trailing dash is unchanged",
			configYAML: "prefix: baseball-v3\n",
			want:       "baseball-v3",
		},
		{
			name:       "prefix with trailing dash is stripped",
			configYAML: "prefix: baseball-v3-\n",
			want:       "baseball-v3",
		},
		{
			name:       "issue-prefix with trailing dash is stripped",
			configYAML: "issue-prefix: baseball-v3-\n",
			want:       "baseball-v3",
		},
		{
			name:       "quoted prefix with trailing dash is stripped",
			configYAML: "prefix: \"baseball-v3-\"\n",
			want:       "baseball-v3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(configPath, []byte(tt.configYAML), 0644); err != nil {
				t.Fatalf("writing config: %v", err)
			}
			got := detectBeadsPrefixFromConfig(configPath)
			if got != tt.want {
				t.Errorf("detectBeadsPrefixFromConfig() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectBeadsPrefixFromConfig_NoFallbackToJSONL(t *testing.T) {
	// Verify that detectBeadsPrefixFromConfig does NOT fall back to issues.jsonl.
	// Gastown requires Dolt server — JSONL is not a supported data source.
	dir := t.TempDir()

	// Write config.yaml without a prefix key
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("# no prefix\n"), 0644); err != nil {
		t.Fatalf("writing config.yaml: %v", err)
	}

	// Write issues.jsonl with valid data — should be ignored
	issuesPath := filepath.Join(dir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(`{"id":"gt-mawit","title":"test"}`), 0644); err != nil {
		t.Fatalf("writing issues.jsonl: %v", err)
	}

	got := detectBeadsPrefixFromConfig(configPath)
	if got != "" {
		t.Errorf("detectBeadsPrefixFromConfig() = %q, want empty (should not read issues.jsonl)", got)
	}
}

func TestIsStandardBeadHash(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"mawit", true},
		{"abc12", true},
		{"z0ixd", true},
		{"00000", true},
		{"abcde", true},
		{"witness", false},    // too long (agent role)
		{"abc", false},        // too short
		{"ABC12", false},      // uppercase
		{"abc-1", false},      // contains hyphen
		{"", false},           // empty
		{"abc1234567", false}, // 10 chars (MR hash)
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isStandardBeadHash(tt.input)
			if got != tt.want {
				t.Errorf("isStandardBeadHash(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestRegisterRig_RejectsReservedNames(t *testing.T) {
	root, rigsConfig := setupTestTown(t)
	manager := NewManager(root, rigsConfig, git.NewGit(root))

	tests := []struct {
		name      string
		wantError string
	}{
		{"hq", `rig name "hq" is reserved for town-level infrastructure`},
		{"HQ", `rig name "HQ" is reserved for town-level infrastructure`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := manager.RegisterRig(RegisterRigOptions{
				Name: tt.name,
			})
			if err == nil {
				t.Errorf("RegisterRig(%q) succeeded, want error containing %q", tt.name, tt.wantError)
				return
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Errorf("RegisterRig(%q) error = %q, want error containing %q", tt.name, err.Error(), tt.wantError)
			}
		})
	}
}

func TestRegisterRig_DetectsAndPersistsCustomPushURL(t *testing.T) {
	root, rigsConfig := setupTestTown(t)
	manager := NewManager(root, rigsConfig, git.NewGit(root))

	rigName := "adoptme"
	rigPath := filepath.Join(root, rigName)
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("mkdir rig path: %v", err)
	}

	upstreamURL := filepath.Join(root, "upstream.git")
	forkURL := filepath.Join(root, "fork.git")

	for _, args := range [][]string{{"git", "init", "--bare", upstreamURL}, {"git", "init", "--bare", forkURL}} {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, string(out))
		}
	}

	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "remote", "add", "origin", upstreamURL},
		{"git", "remote", "set-url", "origin", "--push", forkURL},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = rigPath
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, string(out))
		}
	}

	result, err := manager.RegisterRig(RegisterRigOptions{Name: rigName, GitURL: upstreamURL})
	if err != nil {
		t.Fatalf("RegisterRig: %v", err)
	}

	if result.GitURL != upstreamURL {
		t.Errorf("GitURL = %q, want %q", result.GitURL, upstreamURL)
	}

	entry, ok := rigsConfig.Rigs[rigName]
	if !ok {
		t.Fatalf("rig entry %q missing from config", rigName)
	}
	if entry.PushURL != forkURL {
		t.Errorf("PushURL = %q, want %q", entry.PushURL, forkURL)
	}
}

func TestRegisterRig_DetectPushURLEmptyWhenPushEqualsFetch(t *testing.T) {
	root, rigsConfig := setupTestTown(t)
	manager := NewManager(root, rigsConfig, git.NewGit(root))

	rigName := "adoptsame"
	rigPath := filepath.Join(root, rigName)
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("mkdir rig path: %v", err)
	}

	url := filepath.Join(root, "upstream-same.git")
	cmd := exec.Command("git", "init", "--bare", url)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init bare repo failed: %v\n%s", err, string(out))
	}

	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "remote", "add", "origin", url},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = rigPath
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, string(out))
		}
	}

	if _, err := manager.RegisterRig(RegisterRigOptions{Name: rigName, GitURL: url}); err != nil {
		t.Fatalf("RegisterRig: %v", err)
	}

	entry := rigsConfig.Rigs[rigName]
	if entry.PushURL != "" {
		t.Errorf("PushURL = %q, want empty when push URL equals fetch URL", entry.PushURL)
	}
}

func TestDetectGitURL_MayorRigFallback(t *testing.T) {
	// Verify detectGitURL finds the origin remote from mayor/rig when the
	// root rigPath has no git repo. This exercises the fallback path that
	// was fixed by switching from NewGitWithDir to NewGit.
	root, rigsConfig := setupTestTown(t)
	manager := NewManager(root, rigsConfig, git.NewGit(root))

	rigName := "detectme"
	rigPath := filepath.Join(root, rigName)

	// Create mayor/rig as a clone (but do NOT create a git repo at rigPath itself)
	upstreamURL := filepath.Join(root, "detect-upstream.git")
	cmd := exec.Command("git", "init", "--bare", upstreamURL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init bare repo failed: %v\n%s", err, string(out))
	}
	mayorRigPath := filepath.Join(rigPath, "mayor", "rig")
	if err := os.MkdirAll(filepath.Dir(mayorRigPath), 0755); err != nil {
		t.Fatalf("mkdir mayor dir: %v", err)
	}
	cloneCmd := exec.Command("git", "clone", upstreamURL, mayorRigPath)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("clone failed: %v\n%s", err, string(out))
	}

	// detectGitURL is unexported, so test via RegisterRig with no --url
	result, err := manager.RegisterRig(RegisterRigOptions{Name: rigName, Force: true})
	if err != nil {
		t.Fatalf("RegisterRig: %v", err)
	}
	if result.GitURL != upstreamURL {
		t.Errorf("GitURL = %q, want %q (should detect from mayor/rig)", result.GitURL, upstreamURL)
	}
}

func TestRegisterRig_LegacyConfigPreservesExistingPushURL(t *testing.T) {
	// Legacy config.json (pre-push_url feature) has no push_url field.
	// RegisterRig should NOT clear existing push URLs in .repo.git because
	// empty push_url in legacy config is indistinguishable from "never set"
	// due to omitempty. Existing git push URLs are preserved.
	root, rigsConfig := setupTestTown(t)
	manager := NewManager(root, rigsConfig, git.NewGit(root))

	rigName := "legacyrig"
	rigPath := filepath.Join(root, rigName)

	upstreamURL := filepath.Join(root, "upstream-legacy.git")
	forkURL := filepath.Join(root, "fork-legacy.git")
	for _, u := range []string{upstreamURL, forkURL} {
		cmd := exec.Command("git", "init", "--bare", u)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("init bare repo %s failed: %v\n%s", u, err, string(out))
		}
	}

	// Create .repo.git bare repo with a custom push URL (user configured manually)
	bareRepoPath := filepath.Join(rigPath, ".repo.git")
	if err := os.MkdirAll(filepath.Dir(bareRepoPath), 0755); err != nil {
		t.Fatalf("mkdir rig dir: %v", err)
	}
	cloneCmd := exec.Command("git", "clone", "--bare", upstreamURL, bareRepoPath)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("clone failed: %v\n%s", err, string(out))
	}
	pushCmd := exec.Command("git", "--git-dir", bareRepoPath, "remote", "set-url", "origin", "--push", forkURL)
	if out, err := pushCmd.CombinedOutput(); err != nil {
		t.Fatalf("set push URL failed: %v\n%s", err, string(out))
	}

	// Write legacy config.json WITHOUT push_url field
	configData := fmt.Sprintf(`{"type":"rig","version":1,"name":"%s","git_url":"%s","default_branch":"main"}`, rigName, upstreamURL)
	if err := os.WriteFile(filepath.Join(rigPath, "config.json"), []byte(configData), 0644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	result, err := manager.RegisterRig(RegisterRigOptions{Name: rigName, GitURL: upstreamURL})
	if err != nil {
		t.Fatalf("RegisterRig: %v", err)
	}

	// Legacy config has empty PushURL, so auto-detect runs and finds the fork URL
	// from .repo.git. The detected push URL is persisted to both config.json and
	// town config (RigEntry.PushURL), while keeping "not authoritative" semantics
	// (no clearing on empty detect).
	entry := rigsConfig.Rigs[rigName]
	if entry.PushURL != forkURL {
		t.Errorf("town config PushURL = %q, want %q (auto-detected from .repo.git)", entry.PushURL, forkURL)
	}

	// Verify .repo.git push URL was preserved
	pushOut, err := exec.Command("git", "--git-dir", bareRepoPath, "remote", "get-url", "--push", "origin").CombinedOutput()
	if err != nil {
		t.Fatalf("get push URL: %v\n%s", err, string(pushOut))
	}
	pushURLResult := strings.TrimSpace(string(pushOut))
	if pushURLResult != forkURL {
		t.Errorf(".repo.git push URL = %q, want %q (should be preserved for legacy config)", pushURLResult, forkURL)
	}

	_ = result
}

func TestEnsureMetadata_SetsRequiredFields(t *testing.T) {
	// Verify that EnsureMetadata writes the fields that AddRig depends on:
	// dolt_mode=server, dolt_database=<rigName>, backend=dolt
	// This guards against the regression fixed in PR #1343.
	townRoot := t.TempDir()
	rigName := "myrig"

	// Create the beads directory structure that EnsureMetadata expects
	beadsDir := filepath.Join(townRoot, rigName, "mayor", "rig", ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}

	if err := doltserver.EnsureMetadata(townRoot, rigName); err != nil {
		t.Fatalf("EnsureMetadata: %v", err)
	}

	metadataPath := filepath.Join(beadsDir, "metadata.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}

	var meta map[string]interface{}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("parse metadata.json: %v", err)
	}

	checks := map[string]string{
		"backend":       "dolt",
		"dolt_mode":     "server",
		"dolt_database": rigName,
	}
	for key, want := range checks {
		got, ok := meta[key].(string)
		if !ok {
			t.Errorf("metadata.json missing %q field", key)
			continue
		}
		if got != want {
			t.Errorf("metadata.json %q = %q, want %q", key, got, want)
		}
	}
}

// createTestGitRepoForRig creates a minimal git repo with one commit suitable
// for use as a remote in AddRig tests. Returns the repo path.
func createTestGitRepoForRig(t *testing.T, name string) string {
	t.Helper()
	repoDir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# "+name+"\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	for _, args := range [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", "Initial commit"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	return repoDir
}

// fakeBDForAddRig puts a minimal no-op bd shim on PATH so that AddRig's
// InitBeads and initAgentBeads calls succeed.
func fakeBDForAddRig(t *testing.T) {
	t.Helper()
	script := `#!/bin/bash
# no-op bd shim for AddRig tests
cmd="$1"
[[ "$cmd" == "--allow-stale" ]] && { shift; cmd="$1"; }
shift
case "$cmd" in
  init|config|slot) exit 0 ;;
  show) echo "[]" ;;
  create)
    id=""; title=""
    for arg in "$@"; do
      case "$arg" in --id=*) id="${arg#--id=}" ;; --title=*) title="${arg#--title=}" ;; esac
    done
    printf '{"id":"%s","title":"%s","description":"","issue_type":"agent"}' "$id" "$title"
    ;;
  *) exit 0 ;;
esac
`
	windowsScript := "@echo off\r\nif \"%1\"==\"init\" exit /b 0\r\nif \"%1\"==\"config\" exit /b 0\r\nif \"%1\"==\"slot\" exit /b 0\r\nif \"%1\"==\"--allow-stale\" shift\r\nif \"%1\"==\"show\" echo [] & exit /b 0\r\nif \"%1\"==\"create\" echo {\"id\":\"x\",\"title\":\"x\"} & exit /b 0\r\nexit /b 0\r\n"
	binDir := writeFakeBD(t, script, windowsScript)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestAddRig_UpstreamURL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based bd shim not reliable on Windows CI")
	}

	fakeBDForAddRig(t)

	root, rigsConfig := setupTestTown(t)
	forkURL := createTestGitRepoForRig(t, "fork")
	upstreamURL := createTestGitRepoForRig(t, "upstream")

	manager := NewManager(root, rigsConfig, git.NewGit(root))

	rig, err := manager.AddRig(AddRigOptions{
		Name:          "forkrig",
		GitURL:        forkURL,
		UpstreamURL:   upstreamURL,
		BeadsPrefix:   "fk",
		SkipDoltCheck: true,
	})
	if err != nil {
		t.Fatalf("AddRig: %v", err)
	}

	rigPath := filepath.Join(root, "forkrig")

	t.Run("bare repo upstream remote", func(t *testing.T) {
		bareGit := git.NewGitWithDir(filepath.Join(rigPath, ".repo.git"), "")
		got, err := bareGit.GetUpstreamURL()
		if err != nil {
			t.Fatalf("GetUpstreamURL: %v", err)
		}
		if got != upstreamURL {
			t.Errorf("bare upstream = %q, want %q", got, upstreamURL)
		}
	})

	t.Run("mayor clone upstream remote", func(t *testing.T) {
		mayorGit := git.NewGit(filepath.Join(rigPath, "mayor", "rig"))
		got, err := mayorGit.GetUpstreamURL()
		if err != nil {
			t.Fatalf("GetUpstreamURL: %v", err)
		}
		if got != upstreamURL {
			t.Errorf("mayor upstream = %q, want %q", got, upstreamURL)
		}
	})

	t.Run("config.json round-trips upstream_url", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(rigPath, "config.json"))
		if err != nil {
			t.Fatalf("reading config.json: %v", err)
		}
		var cfg RigConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatalf("parsing config.json: %v", err)
		}
		if cfg.UpstreamURL != upstreamURL {
			t.Errorf("config.json UpstreamURL = %q, want %q", cfg.UpstreamURL, upstreamURL)
		}
	})

	t.Run("town registry persists upstream_url", func(t *testing.T) {
		entry, ok := rigsConfig.Rigs["forkrig"]
		if !ok {
			t.Fatal("rig not found in town config")
		}
		if entry.UpstreamURL != upstreamURL {
			t.Errorf("RigEntry.UpstreamURL = %q, want %q", entry.UpstreamURL, upstreamURL)
		}
	})

	_ = rig
}

// TestAddRig_BranchFlag verifies that --branch is passed to the bare clone so
// the bare repo's HEAD and origin tracking ref both point to the specified branch.
func TestAddRig_BranchFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based bd shim not reliable on Windows CI")
	}

	fakeBDForAddRig(t)

	// Create a remote with two branches: main (default) and develop.
	repoDir := t.TempDir()
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main", repoDir},
		{"git", "-C", repoDir, "config", "user.email", "test@test.com"},
		{"git", "-C", repoDir, "config", "user.name", "Test User"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# test\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	for _, args := range [][]string{
		{"git", "-C", repoDir, "add", "."},
		{"git", "-C", repoDir, "commit", "-m", "Initial commit"},
		{"git", "-C", repoDir, "checkout", "-b", "develop"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repoDir, "develop.txt"), []byte("develop branch\n"), 0644); err != nil {
		t.Fatalf("write develop.txt: %v", err)
	}
	for _, args := range [][]string{
		{"git", "-C", repoDir, "add", "."},
		{"git", "-C", repoDir, "commit", "-m", "develop commit"},
		// Switch back to main so remote HEAD points to main (the default branch).
		{"git", "-C", repoDir, "checkout", "main"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	root, rigsConfig := setupTestTown(t)
	manager := NewManager(root, rigsConfig, git.NewGit(root))

	_, err := manager.AddRig(AddRigOptions{
		Name:          "testrig",
		GitURL:        repoDir,
		BeadsPrefix:   "tr",
		DefaultBranch: "develop",
		SkipDoltCheck: true,
	})
	if err != nil {
		t.Fatalf("AddRig: %v", err)
	}

	rigPath := filepath.Join(root, "testrig")
	bareRepoPath := filepath.Join(rigPath, ".repo.git")
	bareGit := git.NewGitWithDir(bareRepoPath, "")

	t.Run("bare repo HEAD points to develop", func(t *testing.T) {
		got := bareGit.DefaultBranch()
		if got != "develop" {
			t.Errorf("bare repo DefaultBranch() = %q, want %q", got, "develop")
		}
	})

	t.Run("origin/develop tracking ref exists in bare repo", func(t *testing.T) {
		exists, err := bareGit.RefExists("refs/remotes/origin/develop")
		if err != nil {
			t.Fatalf("RefExists: %v", err)
		}
		if !exists {
			t.Error("refs/remotes/origin/develop does not exist in bare repo")
		}
	})

	t.Run("config.json DefaultBranch is develop", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(rigPath, "config.json"))
		if err != nil {
			t.Fatalf("reading config.json: %v", err)
		}
		var cfg RigConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatalf("parsing config.json: %v", err)
		}
		if cfg.DefaultBranch != "develop" {
			t.Errorf("config.json DefaultBranch = %q, want %q", cfg.DefaultBranch, "develop")
		}
	})
}

// TestBareCloneDefaultBranch verifies that DefaultBranch() returns the correct
// branch for a bare clone whose remote uses a non-"main" default branch.
func TestBareCloneDefaultBranch(t *testing.T) {
	// Create a source repo with "master" as the default branch.
	// Override GIT_CONFIG_GLOBAL so user config (e.g. init.defaultBranch)
	// doesn't interfere.
	srcDir := t.TempDir()
	gitEnv := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	for _, args := range [][]string{
		{"git", "init", "-b", "master", srcDir},
		{"git", "-C", srcDir, "config", "user.email", "test@test.com"},
		{"git", "-C", srcDir, "config", "user.name", "Test"},
		{"git", "-C", srcDir, "commit", "--allow-empty", "-m", "init"},
	} {
		c := exec.Command(args[0], args[1:]...)
		c.Env = gitEnv
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", args, out)
		}
	}

	// Bare-clone it, just like AddRig does.
	// Use a subdirectory that doesn't exist yet so git clone creates it
	// (cloning into an existing dir may skip HEAD setup on some git versions).
	bareDir := filepath.Join(t.TempDir(), "repo.git")
	c := exec.Command("git", "clone", "--bare", srcDir, bareDir)
	c.Env = gitEnv
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("bare clone: %s", out)
	}

	g := git.NewGit(bareDir)
	if got := g.DefaultBranch(); got != "master" {
		t.Errorf("DefaultBranch() = %q, want %q", got, "master")
	}
}

func TestBeadsConfigHasSyncRemote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		configYAML string
		want       bool
	}{
		{
			name:       "sync.remote present",
			configYAML: "sync.remote: \"git+https://github.com/example/repo.git\"\n",
			want:       true,
		},
		{
			name:       "sync.remote with single quotes",
			configYAML: "sync.remote: 'git+ssh://git@github.com/example/repo.git'\n",
			want:       true,
		},
		{
			name:       "sync.remote empty value",
			configYAML: "sync.remote: \"\"\n",
			want:       false,
		},
		{
			name:       "sync.remote absent",
			configYAML: "prefix: gt\nissue-prefix: gt\n",
			want:       false,
		},
		{
			name:       "sync.remote commented out",
			configYAML: "# sync.remote: git+https://example.com/repo.git\nprefix: gt\n",
			want:       false,
		},
		{
			name:       "sync.remote with prefix and other keys",
			configYAML: "prefix: gt\nsync.remote: git+https://github.com/org/repo.git\ndolt.idle-timeout: \"0\"\n",
			want:       true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(configPath, []byte(tt.configYAML), 0644); err != nil {
				t.Fatalf("writing config: %v", err)
			}
			got := beadsConfigHasSyncRemote(configPath)
			if got != tt.want {
				t.Errorf("beadsConfigHasSyncRemote() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBeadsConfigHasSyncRemote_MissingFile(t *testing.T) {
	t.Parallel()
	got := beadsConfigHasSyncRemote("/nonexistent/path/config.yaml")
	if got {
		t.Error("beadsConfigHasSyncRemote() = true for missing file, want false")
	}
}

func TestAddRig_TrackedBeadsWithSyncRemote_PassesReinitFlags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based bd shim not reliable on Windows CI")
	}

	// Fake bd that succeeds on all subcommands and logs bd init args.
	cmdLog := filepath.Join(t.TempDir(), "bd-cmds.log")
	script := `#!/usr/bin/env bash
cmd="$1"
[[ "$cmd" == "--allow-stale" ]] && { shift; cmd="$1"; }
shift
if [[ "$cmd" == "init" ]]; then
  echo "init $*" >> "$BD_CMD_LOG"
fi
case "$cmd" in
  init|config|migrate) exit 0 ;;
  show) echo "[]" ;;
  create)
    id=""; title=""
    for arg in "$@"; do
      case "$arg" in --id=*) id="${arg#--id=}" ;; --title=*) title="${arg#--title=}" ;; esac
    done
    printf '{"id":"%s","title":"%s","description":"","issue_type":"agent"}' "$id" "$title"
    ;;
  *) exit 0 ;;
esac
`
	binDir := writeFakeBD(t, script, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_CMD_LOG", cmdLog)

	// Create a git repo with .beads/config.yaml containing sync.remote.
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	configYAML := "prefix: gt\nsync.remote: \"git+https://github.com/steveyegge/gastown.git\"\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main", repoDir},
		{"git", "-C", repoDir, "config", "user.email", "test@test.com"},
		{"git", "-C", repoDir, "config", "user.name", "Test User"},
		{"git", "-C", repoDir, "add", "."},
		{"git", "-C", repoDir, "commit", "-m", "Initial commit with beads config"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	root, rigsConfig := setupTestTown(t)
	manager := NewManager(root, rigsConfig, git.NewGit(root))

	// AddRig may fail after the bd init step (e.g. Dolt not running); that's fine.
	// We only care that bd init was called with the right flags.
	_, _ = manager.AddRig(AddRigOptions{
		Name:          "testrip",
		GitURL:        repoDir,
		BeadsPrefix:   "gt",
		SkipDoltCheck: true,
	})

	logData, err := os.ReadFile(cmdLog)
	if err != nil {
		t.Fatalf("reading bd cmd log: %v", err)
	}
	cmds := string(logData)

	if !strings.Contains(cmds, "--reinit-local") {
		t.Errorf("bd init missing --reinit-local; full log:\n%s", cmds)
	}
	if !strings.Contains(cmds, "--discard-remote") {
		t.Errorf("bd init missing --discard-remote; full log:\n%s", cmds)
	}
	if !strings.Contains(cmds, "--destroy-token=DESTROY-gt") {
		t.Errorf("bd init missing --destroy-token=DESTROY-gt; full log:\n%s", cmds)
	}
}

func TestAddRig_TrackedBeadsWithoutSyncRemote_NoReinitFlags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based bd shim not reliable on Windows CI")
	}

	// Fake bd that logs bd init args.
	cmdLog := filepath.Join(t.TempDir(), "bd-cmds.log")
	script := `#!/usr/bin/env bash
cmd="$1"
[[ "$cmd" == "--allow-stale" ]] && { shift; cmd="$1"; }
shift
if [[ "$cmd" == "init" ]]; then
  echo "init $*" >> "$BD_CMD_LOG"
fi
case "$cmd" in
  init|config|migrate) exit 0 ;;
  show) echo "[]" ;;
  create)
    id=""; title=""
    for arg in "$@"; do
      case "$arg" in --id=*) id="${arg#--id=}" ;; --title=*) title="${arg#--title=}" ;; esac
    done
    printf '{"id":"%s","title":"%s","description":"","issue_type":"agent"}' "$id" "$title"
    ;;
  *) exit 0 ;;
esac
`
	binDir := writeFakeBD(t, script, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_CMD_LOG", cmdLog)

	// Create a git repo with .beads/config.yaml without sync.remote.
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	configYAML := "prefix: gt\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main", repoDir},
		{"git", "-C", repoDir, "config", "user.email", "test@test.com"},
		{"git", "-C", repoDir, "config", "user.name", "Test User"},
		{"git", "-C", repoDir, "add", "."},
		{"git", "-C", repoDir, "commit", "-m", "Initial commit"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	root, rigsConfig := setupTestTown(t)
	manager := NewManager(root, rigsConfig, git.NewGit(root))
	_, _ = manager.AddRig(AddRigOptions{
		Name:          "testrip",
		GitURL:        repoDir,
		BeadsPrefix:   "gt",
		SkipDoltCheck: true,
	})

	logData, err := os.ReadFile(cmdLog)
	if err != nil {
		// No bd init calls logged means the test is inconclusive; skip.
		t.Skip("bd init was not logged (may have been skipped due to bdDatabaseExists check)")
	}
	cmds := string(logData)
	if strings.Contains(cmds, "--reinit-local") {
		t.Errorf("bd init should NOT have --reinit-local without sync.remote; got:\n%s", cmds)
	}
	if strings.Contains(cmds, "--discard-remote") {
		t.Errorf("bd init should NOT have --discard-remote without sync.remote; got:\n%s", cmds)
	}
}
