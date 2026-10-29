package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// slingVarsWithBaseBranchOverride mirrors what gt sling --base-branch stores on
// the bead: loadRigCommandVars puts the rig default first, then sling appends
// the --base-branch value (hq-7eqm).
const slingVarsWithBaseBranchOverride = "feature=Feature\nissue=bd-source\nbase_branch=main\ntest_command=go test ./...\nbase_branch=integration/epic-x"

func TestExtractFormulaVarLastValueWins(t *testing.T) {
	tests := []struct {
		name string
		vars string
		key  string
		want string
	}{
		{name: "single value", vars: "issue=gt-1\nbase_branch=develop", key: "base_branch", want: "develop"},
		{name: "rig default then sling override", vars: slingVarsWithBaseBranchOverride, key: "base_branch", want: "integration/epic-x"},
		{name: "missing key", vars: "issue=gt-1", key: "base_branch", want: ""},
		{name: "value containing equals", vars: "test_command=FOO=1 go test\nissue=gt-1", key: "test_command", want: "FOO=1 go test"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractFormulaVar(tt.vars, tt.key); got != tt.want {
				t.Errorf("extractFormulaVar(%q, %q) = %q, want %q", tt.vars, tt.key, got, tt.want)
			}
		})
	}
}

func TestResolveDoneTarget(t *testing.T) {
	issueWithVars := func(formulaVars string) *beads.Issue {
		return &beads.Issue{ID: "gt-1", Description: beads.SetAttachmentFields(&beads.Issue{}, &beads.AttachmentFields{
			AttachedFormula: "mol-polecat-work",
			FormulaVars:     formulaVars,
		})}
	}
	tests := []struct {
		name       string
		explicit   string
		issue      *beads.Issue
		detected   string
		wantTarget string
		wantSource string
		wantDetect bool
	}{
		{
			name:       "explicit flag beats everything",
			explicit:   "release/v2",
			issue:      issueWithVars("base_branch=integration/epic-x"),
			detected:   "integration/other",
			wantTarget: "release/v2",
			wantSource: "--target flag",
		},
		{
			name:       "sling base_branch override beats rig default stored before it",
			issue:      issueWithVars(slingVarsWithBaseBranchOverride),
			detected:   "integration/other",
			wantTarget: "integration/epic-x",
			wantSource: "formula_vars",
		},
		{
			name:       "base_branch equal to rig default falls through to auto-detect",
			issue:      issueWithVars("base_branch=main"),
			detected:   "integration/epic-x",
			wantTarget: "integration/epic-x",
			wantSource: "integration branch auto-detect",
			wantDetect: true,
		},
		{
			name:       "no issue falls through to auto-detect",
			detected:   "integration/epic-x",
			wantTarget: "integration/epic-x",
			wantSource: "integration branch auto-detect",
			wantDetect: true,
		},
		{
			name:       "nothing found uses rig default",
			issue:      issueWithVars("issue=gt-1"),
			wantTarget: "main",
			wantDetect: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detectCalled := false
			detect := func() string {
				detectCalled = true
				return tt.detected
			}
			target, source := resolveDoneTarget(tt.explicit, "main", tt.issue, detect)
			if target != tt.wantTarget || source != tt.wantSource {
				t.Errorf("resolveDoneTarget() = (%q, %q), want (%q, %q)", target, source, tt.wantTarget, tt.wantSource)
			}
			if detectCalled != tt.wantDetect {
				t.Errorf("integration auto-detect called = %v, want %v", detectCalled, tt.wantDetect)
			}
		})
	}
}

// TestRunDoneRebasesOntoSlingBaseBranch is the hq-7eqm regression: a plain
// `gt done` (no --target) on a bead slung with --base-branch must rebase onto
// and target the integration branch, never the rig default branch.
func TestRunDoneRebasesOntoSlingBaseBranch(t *testing.T) {
	workDir, _, _ := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)

	const integration = "integration/epic-x"
	remote := t.TempDir()
	runGitForMQSubmitTest(t, remote, "init", "--bare")
	runGitForMQSubmitTest(t, workDir, "init")
	runGitForMQSubmitTest(t, workDir, "config", "user.email", "test@example.com")
	runGitForMQSubmitTest(t, workDir, "config", "user.name", "Test User")
	runGitForMQSubmitTest(t, workDir, "remote", "add", "origin", remote)
	commitFile := func(name string) string {
		writeMQSubmitTestFile(t, workDir, name, name+"\n")
		runGitForMQSubmitTest(t, workDir, "add", name)
		runGitForMQSubmitTest(t, workDir, "commit", "-m", name)
		return runGitForMQSubmitTest(t, workDir, "rev-parse", "HEAD")
	}
	writeMQSubmitTestFile(t, workDir, ".gitignore", ".beads/\n.runtime/\n")
	runGitForMQSubmitTest(t, workDir, "add", ".gitignore")
	commitFile("base.txt")
	runGitForMQSubmitTest(t, workDir, "branch", "-M", "main")
	runGitForMQSubmitTest(t, workDir, "push", "-u", "origin", "main")
	runGitForMQSubmitTest(t, workDir, "checkout", "-b", integration)
	commitFile("integration.txt")
	runGitForMQSubmitTest(t, workDir, "push", "origin", integration)
	branch := "polecat/refuge/bd-source"
	runGitForMQSubmitTest(t, workDir, "checkout", "-b", branch)
	commitFile("feature.txt")
	// Both base candidates move on after the polecat branched, so gt done
	// auto-rebases onto whichever one it resolved.
	runGitForMQSubmitTest(t, workDir, "checkout", integration)
	integrationTip := commitFile("integration-later.txt")
	runGitForMQSubmitTest(t, workDir, "push", "origin", integration)
	runGitForMQSubmitTest(t, workDir, "checkout", "main")
	mainOnly := commitFile("main-only.txt")
	runGitForMQSubmitTest(t, workDir, "push", "origin", "main")
	runGitForMQSubmitTest(t, workDir, "checkout", branch)

	sourceDesc := beads.SetAttachmentFields(&beads.Issue{Description: "convoy_id: hq-cv-test\nmerge_strategy: mr"}, &beads.AttachmentFields{
		AttachedFormula: "mol-polecat-work",
		AttachedVars:    strings.Split(slingVarsWithBaseBranchOverride, "\n"),
		FormulaVars:     slingVarsWithBaseBranchOverride,
	})
	logPath := installDoneTargetBDStub(t, sourceDesc)

	resetDoneFlagsForTest(t)
	townRoot := routedSourceTestTownRoot(workDir)
	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))
	t.Setenv("GT_TOWN_ROOT", townRoot)
	t.Setenv("GT_ROOT", townRoot)
	t.Setenv("GT_ROLE", "gastown/polecats/refuge")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "refuge")
	t.Setenv("BD_ACTOR", "gastown/polecats/refuge")
	t.Chdir(workDir)

	doneIssue = "bd-source"
	doneCleanupStatus = "unpushed"
	doneSkipVerify = true
	updateAgentStateOnDoneFn = func(cwd, townRoot, exitType, issueID string) error { return nil }
	oldKiller := newDoneSessionKiller
	newDoneSessionKiller = func() doneSessionKiller { return &fakeDoneSessionKiller{} }
	t.Cleanup(func() { newDoneSessionKiller = oldKiller })
	if err := runDone(nil, nil); err != nil {
		t.Fatalf("runDone: %v", err)
	}

	isAncestor := func(commit string) bool {
		return exec.Command("git", "-C", workDir, "merge-base", "--is-ancestor", commit, "HEAD").Run() == nil
	}
	if isAncestor(mainOnly) {
		t.Errorf("branch was rebased onto origin/main: main-only commit %s is in HEAD", mainOnly[:8])
	}
	if !isAncestor(integrationTip) {
		t.Errorf("branch was not rebased onto origin/%s: tip %s is not in HEAD", integration, integrationTip[:8])
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	if !strings.Contains(string(log), "target: "+integration+"\n") {
		t.Errorf("MR was not created with target %s; bd log:\n%s", integration, log)
	}
}

// installDoneTargetBDStub installs a bd stub that serves bd-source with the
// given description and records every call (including MR create descriptions).
func installDoneTargetBDStub(t *testing.T, sourceDescription string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "bd.log")
	sourceJSON, err := json.Marshal([]map[string]any{{
		"id": "bd-source", "title": "source", "status": "open", "priority": 1,
		"issue_type": "task", "description": sourceDescription,
	}})
	if err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(dir, "source.json")
	if err := os.WriteFile(sourcePath, sourceJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--allow-stale" ]; then
  shift
fi
if [ "$1" = "version" ]; then
  echo "bd stub"
  exit 0
fi
printf '%%s\t%%s\n' "$BEADS_DIR" "$*" >> %q
case "$1 $2" in
  "show bd-source") cat %q; exit 0 ;;
  "show gt-mr") echo '[{"id":"gt-mr","title":"Merge: bd-source","status":"open","priority":1,"issue_type":"task","labels":["gt:merge-request"]}]'; exit 0 ;;
  "comments add") exit 0 ;;
esac
case "$1" in
  list|sql) echo '[]'; exit 0 ;;
  create) echo '{"id":"gt-mr","title":"Merge: bd-source","status":"open","priority":1,"issue_type":"task","labels":["gt:merge-request"]}'; exit 0 ;;
  close|update|label) exit 0 ;;
esac
echo "unexpected bd command: $*" >&2
exit 1
`, logPath, sourcePath)
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)
	return logPath
}
