package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDonePreflightBaseRef(t *testing.T) {
	tests := []struct {
		name            string
		resolvedTarget  string
		explicitBaseRef string
		want            string
		wantExplicit    bool
	}{
		{
			name:           "preserves resolved target without override",
			resolvedTarget: "origin/main",
			want:           "origin/main",
		},
		{
			name:            "preserves fork-aware target without override",
			resolvedTarget:  "upstream/main",
			explicitBaseRef: "  ",
			want:            "upstream/main",
		},
		{
			name:            "uses explicit local ref",
			resolvedTarget:  "origin/integration/epic",
			explicitBaseRef: "refs/heads/integration/epic",
			want:            "refs/heads/integration/epic",
			wantExplicit:    true,
		},
		{
			name:            "trims explicit remote ref",
			resolvedTarget:  "origin/develop",
			explicitBaseRef: " upstream/develop ",
			want:            "upstream/develop",
			wantExplicit:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, explicit := donePreflightBaseRef(tt.resolvedTarget, tt.explicitBaseRef)
			if got != tt.want {
				t.Fatalf("donePreflightBaseRef(%q, %q) = %q, want %q", tt.resolvedTarget, tt.explicitBaseRef, got, tt.want)
			}
			if explicit != tt.wantExplicit {
				t.Fatalf("donePreflightBaseRef(%q, %q) explicit = %v, want %v", tt.resolvedTarget, tt.explicitBaseRef, explicit, tt.wantExplicit)
			}
		})
	}
}

func TestDonePreVerifiedBaseUsesExplicitBaseRef(t *testing.T) {
	target := "feature/integration"
	explicitBaseRef := "refs/heads/feature/integration-stable"
	baseRef, explicit := donePreflightBaseRef("origin/"+target, explicitBaseRef)
	if !explicit || baseRef != explicitBaseRef {
		t.Fatalf("donePreflightBaseRef returned %q, %v; want %q, true", baseRef, explicit, explicitBaseRef)
	}

	source, err := os.ReadFile("done.go")
	if err != nil {
		t.Fatalf("read done.go: %v", err)
	}
	normalized := strings.Join(strings.Fields(string(source)), " ")
	if strings.Contains(normalized, `verifiedBaseRef := g.CleanBaseRef("origin", defaultBranch, target)`) {
		t.Fatalf("pre-verified base for symbolic target %q is recomputed instead of using explicit base ref %q", target, explicitBaseRef)
	}
	if !strings.Contains(normalized, "verifiedBaseRef := baseRef") {
		t.Fatal("pre-verified base does not use the resolved baseRef")
	}
}

func TestDoneCommandAcceptsBaseRef(t *testing.T) {
	if flag := doneCmd.Flags().Lookup("base-ref"); flag == nil {
		t.Fatal("gt done does not accept --base-ref")
	}
}

func TestDonePreflightUsesFormulaTargetBeforeCommitChecks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}

	remote := filepath.Join(townRoot, "remote.git")
	repo := filepath.Join(townRoot, "gastown", "polecats", "nux", "gastown")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	testRunGit(t, townRoot, "init", "--bare", remote)
	testRunGit(t, repo, "init", "--initial-branch", "main")
	testRunGit(t, repo, "config", "user.email", "test@test.com")
	testRunGit(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	testRunGit(t, repo, "add", "README.md")
	testRunGit(t, repo, "commit", "-m", "base")
	testRunGit(t, repo, "remote", "add", "origin", remote)
	testRunGit(t, repo, "branch", "polecat/nux/gt-task")
	testRunGit(t, repo, "checkout", "-b", "integration/legacy")
	for i := 0; i < 200; i++ {
		testRunGit(t, repo, "commit", "--allow-empty", "-m", fmt.Sprintf("integration %d", i))
	}
	testRunGit(t, repo, "push", "origin", "integration/legacy")
	testRunGit(t, repo, "checkout", "polecat/nux/gt-task")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0644); err != nil {
		t.Fatalf("write feature: %v", err)
	}
	testRunGit(t, repo, "add", "feature.txt")
	testRunGit(t, repo, "commit", "-m", "feature")
	testRunGit(t, repo, "push", "origin", "HEAD:main")

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	bdScript := `#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
if [ "$1" = "show" ] && [ "$2" = "gt-task" ]; then
  echo '[{"id":"gt-task","title":"Task","status":"open","description":"formula_vars: [\"base_branch=integration/legacy\"]"}]'
  exit 0
fi
echo '[]'
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	oldIssue, oldPriority, oldStatus := doneIssue, donePriority, doneStatus
	oldCleanup, oldResume := doneCleanupStatus, doneResume
	oldPreVerified, oldTarget, oldBaseRef, oldSkipVerify := donePreVerified, doneTarget, doneBaseRef, doneSkipVerify
	t.Cleanup(func() {
		doneIssue, donePriority, doneStatus = oldIssue, oldPriority, oldStatus
		doneCleanupStatus, doneResume = oldCleanup, oldResume
		donePreVerified, doneTarget, doneBaseRef, doneSkipVerify = oldPreVerified, oldTarget, oldBaseRef, oldSkipVerify
	})
	doneIssue = "gt-task"
	donePriority = -1
	doneStatus = ExitCompleted
	doneCleanupStatus = "unpushed"
	doneResume = false
	donePreVerified = false
	doneTarget = ""
	doneBaseRef = ""
	doneSkipVerify = false

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_ACTOR", "gastown/polecats/nux")
	t.Setenv("GT_ROLE", "gastown/polecats/nux")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "nux")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_SESSION", "")
	t.Setenv("GT_TOWN_ROOT", townRoot)
	t.Setenv("GT_ROOT", townRoot)

	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}

	err = runDone(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "branch contamination: 200 commits behind origin/integration/legacy") {
		t.Fatalf("runDone error = %v, want integration target contamination preflight", err)
	}
}

func TestDoneNoMRUsesResolvedFormulaTargetForPushVerification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}

	remote := filepath.Join(townRoot, "remote.git")
	repo := filepath.Join(townRoot, "gastown", "polecats", "nux", "gastown")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	testRunGit(t, townRoot, "init", "--bare", remote)
	testRunGit(t, repo, "init", "--initial-branch", "main")
	testRunGit(t, repo, "config", "user.email", "test@test.com")
	testRunGit(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	testRunGit(t, repo, "add", "README.md")
	testRunGit(t, repo, "commit", "-m", "base")
	testRunGit(t, repo, "remote", "add", "origin", remote)
	testRunGit(t, repo, "push", "origin", "main")
	testRunGit(t, repo, "checkout", "-b", "integration/legacy")
	testRunGit(t, repo, "commit", "--allow-empty", "-m", "integration")
	testRunGit(t, repo, "push", "origin", "integration/legacy")
	testRunGit(t, repo, "checkout", "-b", "polecat/nux/gt-task")

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	bdScript := `#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
if [ "$1" = "show" ] && [ "$2" = "gt-task" ]; then
  echo '[{"id":"gt-task","title":"Task","status":"open","description":"formula_vars: [\"base_branch=integration/legacy\"]"}]'
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	oldIssue, oldPriority, oldStatus := doneIssue, donePriority, doneStatus
	oldCleanup, oldResume := doneCleanupStatus, doneResume
	oldPreVerified, oldTarget, oldBaseRef, oldSkipVerify := donePreVerified, doneTarget, doneBaseRef, doneSkipVerify
	t.Cleanup(func() {
		doneIssue, donePriority, doneStatus = oldIssue, oldPriority, oldStatus
		doneCleanupStatus, doneResume = oldCleanup, oldResume
		donePreVerified, doneTarget, doneBaseRef, doneSkipVerify = oldPreVerified, oldTarget, oldBaseRef, oldSkipVerify
	})
	doneIssue = "gt-task"
	donePriority = -1
	doneStatus = ExitCompleted
	doneCleanupStatus = "clean"
	doneResume = false
	donePreVerified = false
	doneTarget = ""
	doneBaseRef = ""
	doneSkipVerify = false

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_ACTOR", "gastown/polecats/nux")
	t.Setenv("GT_ROLE", "gastown/polecats/nux")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "nux")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_SESSION", "")
	t.Setenv("GT_TOWN_ROOT", townRoot)
	t.Setenv("GT_ROOT", townRoot)

	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}

	if err := runDone(nil, nil); err != nil {
		t.Fatalf("runDone: %v", err)
	}
}
