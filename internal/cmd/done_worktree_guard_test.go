package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestResolveDonePolecatWorktreeAcceptsOwnWorktree(t *testing.T) {
	for _, tt := range []struct {
		name     string
		layout   string
		subdir   string
		gtRole   string
		wantRoot func(townRoot string) string
	}{
		{
			name:   "nested root",
			layout: "nested",
			gtRole: "gastown/polecats/shiny",
			wantRoot: func(townRoot string) string {
				return filepath.Join(townRoot, "gastown", "polecats", "shiny", "gastown")
			},
		},
		{
			name:   "nested subdir",
			layout: "nested",
			subdir: filepath.Join("internal", "cmd"),
			gtRole: "gastown/polecats/shiny",
			wantRoot: func(townRoot string) string {
				return filepath.Join(townRoot, "gastown", "polecats", "shiny", "gastown")
			},
		},
		{
			name:   "legacy root",
			layout: "legacy",
			gtRole: "polecat",
			wantRoot: func(townRoot string) string {
				return filepath.Join(townRoot, "gastown", "polecats", "shiny")
			},
		},
		{
			name:   "legacy subdir",
			layout: "legacy",
			subdir: filepath.Join("internal", "cmd"),
			gtRole: "gastown/shiny",
			wantRoot: func(townRoot string) string {
				return filepath.Join(townRoot, "gastown", "polecats", "shiny")
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			townRoot, repoRoot := setupDoneGuardWorktree(t, tt.layout, "shiny")
			cwd := repoRoot
			if tt.subdir != "" {
				cwd = filepath.Join(repoRoot, tt.subdir)
				if err := os.MkdirAll(cwd, 0755); err != nil {
					t.Fatalf("mkdir subdir: %v", err)
				}
			}
			setDoneGuardEnv(t, "gastown", "shiny", tt.gtRole)

			got, err := resolveDonePolecatWorktreeAt(cwd)
			if err != nil {
				t.Fatalf("resolveDonePolecatWorktreeAt: %v", err)
			}
			if got.townRoot != townRoot {
				t.Fatalf("townRoot = %q, want %q", got.townRoot, townRoot)
			}
			if got.cwd != doneCanonicalPath(tt.wantRoot(townRoot)) {
				t.Fatalf("cwd = %q, want %q", got.cwd, doneCanonicalPath(tt.wantRoot(townRoot)))
			}
			if got.rigName != "gastown" || got.polecatName != "shiny" || got.actor != "gastown/polecats/shiny" {
				t.Fatalf("identity = %#v, want gastown/polecats/shiny", got)
			}
		})
	}
}

func TestResolveDonePolecatWorktreeRejectsUnsafePaths(t *testing.T) {
	for _, tt := range []struct {
		name   string
		cwd    func(townRoot, ownRepo string) string
		gitDir bool
	}{
		{
			name: "town root",
			cwd:  func(townRoot, ownRepo string) string { return townRoot },
		},
		{
			name: "mayor rig",
			cwd: func(townRoot, ownRepo string) string {
				return filepath.Join(townRoot, "mayor", "rig")
			},
			gitDir: true,
		},
		{
			name: "rig root",
			cwd: func(townRoot, ownRepo string) string {
				return filepath.Join(townRoot, "gastown")
			},
		},
		{
			name: "other polecat",
			cwd: func(townRoot, ownRepo string) string {
				return filepath.Join(townRoot, "gastown", "polecats", "other", "gastown")
			},
			gitDir: true,
		},
		{
			name: "nested parent without git root",
			cwd: func(townRoot, ownRepo string) string {
				return filepath.Join(townRoot, "gastown", "polecats", "shiny")
			},
		},
		{
			name: "nested parent with git root",
			cwd: func(townRoot, ownRepo string) string {
				return filepath.Join(townRoot, "gastown", "polecats", "shiny")
			},
			gitDir: true,
		},
		{
			name: "missing worktree",
			cwd: func(townRoot, ownRepo string) string {
				return filepath.Join(townRoot, "gastown", "polecats", "shiny", "missing")
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			townRoot, ownRepo := setupDoneGuardWorktree(t, "nested", "shiny")
			cwd := tt.cwd(townRoot, ownRepo)
			if tt.gitDir {
				initDoneGuardGitRepo(t, cwd)
			} else if tt.name != "missing worktree" {
				if err := os.MkdirAll(cwd, 0755); err != nil {
					t.Fatalf("mkdir cwd: %v", err)
				}
			}
			setDoneGuardEnv(t, "gastown", "shiny", "gastown/polecats/shiny")
			t.Setenv("GT_POLECAT_PATH", ownRepo)

			if _, err := resolveDonePolecatWorktreeAt(cwd); err == nil {
				t.Fatalf("resolveDonePolecatWorktreeAt(%q) succeeded, want rejection", cwd)
			}
		})
	}
}

func TestResolveDonePolecatWorktreeRejectsIdentityMismatch(t *testing.T) {
	for _, tt := range []struct {
		name    string
		actor   string
		gtRole  string
		gtRig   string
		polecat string
	}{
		{name: "non polecat actor", actor: "gastown/crew/shiny", gtRole: "gastown/polecats/shiny", gtRig: "gastown", polecat: "shiny"},
		{name: "malformed actor", actor: "gastown/polecats", gtRole: "gastown/polecats/shiny", gtRig: "gastown", polecat: "shiny"},
		{name: "actor mismatch", actor: "gastown/polecats/other", gtRole: "gastown/polecats/shiny", gtRig: "gastown", polecat: "shiny"},
		{name: "role mismatch", actor: "gastown/polecats/shiny", gtRole: "gastown/polecats/other", gtRig: "gastown", polecat: "shiny"},
		{name: "rig mismatch", actor: "gastown/polecats/shiny", gtRole: "gastown/polecats/shiny", gtRig: "other", polecat: "shiny"},
		{name: "polecat mismatch", actor: "gastown/polecats/shiny", gtRole: "gastown/polecats/shiny", gtRig: "gastown", polecat: "other"},
		{name: "non polecat role", actor: "gastown/polecats/shiny", gtRole: "gastown/crew/shiny", gtRig: "gastown", polecat: "shiny"},
		{name: "missing role", actor: "gastown/polecats/shiny", gtRig: "gastown", polecat: "shiny"},
		{name: "actor traversal rig", actor: "../polecats/shiny", gtRole: "gastown/polecats/shiny", gtRig: "gastown", polecat: "shiny"},
		{name: "actor traversal polecat", actor: "gastown/polecats/..", gtRole: "gastown/polecats/shiny", gtRig: "gastown", polecat: "shiny"},
		{name: "actor backslash polecat", actor: "gastown/polecats/shiny\\other", gtRole: "gastown/polecats/shiny", gtRig: "gastown", polecat: "shiny"},
		{name: "env traversal rig", actor: "gastown/polecats/shiny", gtRole: "gastown/polecats/shiny", gtRig: "..", polecat: "shiny"},
		{name: "env traversal polecat", actor: "gastown/polecats/shiny", gtRole: "gastown/polecats/shiny", gtRig: "gastown", polecat: ".."},
		{name: "role extra components", actor: "gastown/polecats/shiny", gtRole: "gastown/polecats/shiny/extra", gtRig: "gastown", polecat: "shiny"},
		{name: "role traversal rig", actor: "gastown/polecats/shiny", gtRole: "../polecats/shiny", gtRig: "gastown", polecat: "shiny"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, repoRoot := setupDoneGuardWorktree(t, "nested", "shiny")
			t.Setenv("BD_ACTOR", tt.actor)
			t.Setenv("GT_ROLE", tt.gtRole)
			t.Setenv("GT_RIG", tt.gtRig)
			t.Setenv("GT_POLECAT", tt.polecat)

			if _, err := resolveDonePolecatWorktreeAt(repoRoot); err == nil {
				t.Fatal("resolveDonePolecatWorktreeAt succeeded, want identity rejection")
			}
		})
	}
}

func TestResolveDonePolecatWorktreeRejectsTownRootMismatch(t *testing.T) {
	for _, envName := range []string{"GT_TOWN_ROOT", "GT_ROOT"} {
		t.Run(envName, func(t *testing.T) {
			_, repoRoot := setupDoneGuardWorktree(t, "nested", "shiny")
			setDoneGuardEnv(t, "gastown", "shiny", "gastown/polecats/shiny")
			t.Setenv(envName, filepath.Join(t.TempDir(), "other-town"))

			if _, err := resolveDonePolecatWorktreeAt(repoRoot); err == nil || !strings.Contains(err.Error(), "town root mismatch") {
				t.Fatalf("resolveDonePolecatWorktreeAt error = %v, want town root mismatch", err)
			}
		})
	}
}

func TestResolveDonePolecatWorktreeRejectsGitWorkTreeSpoof(t *testing.T) {
	for _, tt := range []struct {
		envName string
		value   func(repoRoot string) string
	}{
		{envName: "GIT_DIR", value: func(repoRoot string) string { return filepath.Join(repoRoot, ".git") }},
		{envName: "GIT_WORK_TREE", value: func(repoRoot string) string { return repoRoot }},
	} {
		t.Run(tt.envName, func(t *testing.T) {
			townRoot, repoRoot := setupDoneGuardWorktree(t, "nested", "shiny")
			setDoneGuardEnv(t, "gastown", "shiny", "gastown/polecats/shiny")
			t.Setenv(tt.envName, tt.value(repoRoot))

			if _, err := resolveDonePolecatWorktreeAt(townRoot); err == nil || !strings.Contains(err.Error(), "unset "+tt.envName) {
				t.Fatalf("resolveDonePolecatWorktreeAt error = %v, want git env override rejection", err)
			}
		})
	}
}

func TestRunDoneRejectsMayorRigBeforeAutosave(t *testing.T) {
	for _, tt := range []struct {
		name string
		cwd  func(townRoot string) string
	}{
		{
			name: "town root",
			cwd:  func(townRoot string) string { return townRoot },
		},
		{
			name: "town mayor rig",
			cwd:  func(townRoot string) string { return filepath.Join(townRoot, "mayor", "rig") },
		},
		{
			name: "rig mayor rig",
			cwd:  func(townRoot string) string { return filepath.Join(townRoot, "gastown", "mayor", "rig") },
		},
		{
			name: "other polecat",
			cwd: func(townRoot string) string {
				return filepath.Join(townRoot, "gastown", "polecats", "other", "gastown")
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			townRoot, _ := setupDoneGuardWorktree(t, "nested", "shiny")
			dirtyRepo := tt.cwd(townRoot)
			initDoneGuardGitRepo(t, dirtyRepo)
			dirtyFile := filepath.Join(dirtyRepo, "unrelated.txt")
			if err := os.WriteFile(dirtyFile, []byte("do not commit\n"), 0644); err != nil {
				t.Fatalf("write dirty file: %v", err)
			}

			origDoneStatus, origCleanupStatus := doneStatus, doneCleanupStatus
			doneStatus = ExitDeferred
			doneCleanupStatus = "uncommitted"
			t.Cleanup(func() {
				doneStatus = origDoneStatus
				doneCleanupStatus = origCleanupStatus
			})
			setDoneGuardEnv(t, "gastown", "shiny", "gastown/polecats/shiny")

			origDir, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(dirtyRepo); err != nil {
				t.Fatalf("chdir dirty repo: %v", err)
			}
			t.Cleanup(func() { _ = os.Chdir(origDir) })

			err = runDone(nil, nil)
			if err == nil || !strings.Contains(err.Error(), "assigned polecat worktree") {
				t.Fatalf("runDone error = %v, want assigned worktree rejection", err)
			}
			status := doneGuardGitOutput(t, dirtyRepo, "status", "--short")
			if !strings.Contains(status, "?? unrelated.txt") {
				t.Fatalf("dirty repo status = %q, want dirty file left uncommitted", status)
			}
		})
	}
}

func TestIsDoneCommand(t *testing.T) {
	done := &cobra.Command{Use: "done"}
	root := &cobra.Command{Use: "gt"}
	root.AddCommand(done)
	if !isDoneCommand(done) {
		t.Fatal("done command should be detected")
	}
	if isDoneCommand(root) {
		t.Fatal("root command should not be detected as done")
	}
}

func TestIsDoneCommandIgnoresNestedDoneSubcommands(t *testing.T) {
	root := &cobra.Command{Use: "gt"}
	done := &cobra.Command{Use: "done"}
	root.AddCommand(done)

	dog := &cobra.Command{Use: "dog"}
	dogDone := &cobra.Command{Use: "done"}
	dog.AddCommand(dogDone)
	root.AddCommand(dog)

	if !isDoneCommand(done) {
		t.Fatal("top-level done command should be detected")
	}
	if isDoneCommand(dogDone) {
		t.Fatal("gt dog done must not trip the polecat-only gt done guard")
	}
	if isDoneCommand(dog) {
		t.Fatal("gt dog should not be detected as done")
	}
}

func TestPersistentPreRunDoneRejectsBeforeRegistryFallback(t *testing.T) {
	townRoot, _ := setupDoneGuardWorktree(t, "nested", "shiny")
	initDoneGuardGitRepo(t, townRoot)
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "rigs.json"), []byte(`{"rigs":{"gastown":{"beads":{"prefix":"gt"}}}}`), 0644); err != nil {
		t.Fatalf("write mayor rigs.json: %v", err)
	}
	setDoneGuardEnv(t, "gastown", "shiny", "gastown/polecats/shiny")

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir town root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	done := &cobra.Command{Use: "done"}
	err = persistentPreRun(done, nil)
	if err == nil || !strings.Contains(err.Error(), "assigned polecat worktree") {
		t.Fatalf("persistentPreRun error = %v, want assigned worktree rejection", err)
	}
	if _, err := os.Stat(filepath.Join(townRoot, "rigs.json")); !os.IsNotExist(err) {
		t.Fatalf("town root rigs.json exists after rejected done pre-run; err=%v", err)
	}
}

func setupDoneGuardWorktree(t *testing.T, layout, polecatName string) (string, string) {
	t.Helper()
	townRoot := t.TempDir()
	t.Setenv("GT_TOWN_ROOT", townRoot)
	t.Setenv("GT_ROOT", townRoot)
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}

	polecatRoot := filepath.Join(townRoot, "gastown", "polecats", polecatName)
	repoRoot := polecatRoot
	if layout == "nested" {
		repoRoot = filepath.Join(polecatRoot, "gastown")
	}
	initDoneGuardGitRepo(t, repoRoot)
	return townRoot, repoRoot
}

func initDoneGuardGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir git repo: %v", err)
	}
	cmd := exec.Command("git", "init", dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", dir, err, output)
	}
}

func setDoneGuardEnv(t *testing.T, rig, polecatName, gtRole string) {
	t.Helper()
	t.Setenv("BD_ACTOR", rig+"/polecats/"+polecatName)
	t.Setenv("GT_ROLE", gtRole)
	t.Setenv("GT_RIG", rig)
	t.Setenv("GT_POLECAT", polecatName)
	t.Setenv("GT_SESSION", "gt-"+polecatName)
}

func doneGuardGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", cmdArgs, err, output)
	}
	return string(output)
}
