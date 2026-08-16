package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/config"
)

func TestRoutedIssueBeadsUsesTownRoutesForCustomPrefix(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)

	_, gotCurrent, gotRouted := routedIssueBeads(workDir, "bd-source")
	if gotCurrent != currentBeadsDir {
		t.Fatalf("current beads dir = %q, want %q", gotCurrent, currentBeadsDir)
	}
	if gotRouted != ownerBeadsDir {
		t.Fatalf("routed beads dir = %q, want %q", gotRouted, ownerBeadsDir)
	}
}

func TestSourceRouteContextNamesCurrentAndRoutedDB(t *testing.T) {
	context := sourceRouteContext("/town/gastown/.beads", "/town/beads/.beads")
	for _, want := range []string{"current_db=/town/gastown/.beads", "routed_db=/town/beads/.beads"} {
		if !strings.Contains(context, want) {
			t.Fatalf("source route context %q missing %q", context, want)
		}
	}
}

func TestResolveSubmitSourceIssueIgnoresCurrentRigMirror(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	installSubmitSourceBDStub(t, currentBeadsDir, ownerBeadsDir, false)

	source, err := resolveSubmitSourceIssue(workDir, "bd-source")
	if err != nil {
		t.Fatalf("resolveSubmitSourceIssue: %v", err)
	}
	if source.Issue.Title != "owner source" {
		t.Fatalf("source title = %q, want routed owner source (current-rig mirror must be ignored)", source.Issue.Title)
	}
	if source.CurrentBeadsDir != currentBeadsDir || source.RoutedBeadsDir != ownerBeadsDir {
		t.Fatalf("route = current %q routed %q, want current %q routed %q", source.CurrentBeadsDir, source.RoutedBeadsDir, currentBeadsDir, ownerBeadsDir)
	}
}

func TestResolveSubmitSourceIssueFailureNamesRoutingContext(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	installSubmitSourceBDStub(t, currentBeadsDir, ownerBeadsDir, true)

	_, err := resolveSubmitSourceIssue(workDir, "bd-source")
	if err == nil {
		t.Fatal("resolveSubmitSourceIssue succeeded, want routed owner lookup failure")
	}
	errText := err.Error()
	for _, want := range []string{"source_issue bd-source could not be resolved", "current_db=" + currentBeadsDir, "routed_db=" + ownerBeadsDir} {
		if !strings.Contains(errText, want) {
			t.Fatalf("error %q missing %q", errText, want)
		}
	}
}

func TestValidateMergeRequestSourceUsesPreResolvedSource(t *testing.T) {
	mr := &beads.Issue{ID: "gt-mr", Description: "source_issue: bd-source\n"}
	if err := validateMergeRequestSource(mr, "bd-source", nil); err == nil || !strings.Contains(err.Error(), "pre-resolved") {
		t.Fatalf("validateMergeRequestSource without source = %v, want pre-resolved error", err)
	}
	if err := validateMergeRequestSource(mr, "bd-source", &beads.Issue{ID: "bd-source", Type: "task"}); err != nil {
		t.Fatalf("validateMergeRequestSource with routed source: %v", err)
	}
}

func TestMqSubmitPathUsesRoutedSourceAndCurrentRigQueueBeads(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	logPath := installSubmitSourceBDRecorder(t, currentBeadsDir, ownerBeadsDir)

	currentBD := beads.New(workDir)
	source, err := resolveSubmitSourceIssue(workDir, "bd-source")
	if err != nil {
		t.Fatalf("resolveSubmitSourceIssue: %v", err)
	}

	if _, err := currentBD.Create(beads.CreateOptions{
		Title:       "Merge: bd-source",
		Labels:      []string{"gt:merge-request"},
		Priority:    source.Issue.Priority,
		Description: "branch: polecat/refuge/bd-source\ntarget: main\nsource_issue: bd-source\nrig: gastown",
		Ephemeral:   true,
		Rig:         "gastown",
	}); err != nil {
		t.Fatalf("current-rig MR create: %v", err)
	}
	if err := source.BD.AddComment("bd-source", "MR created: gt-mr"); err != nil {
		t.Fatalf("source back-link comment: %v", err)
	}

	log := readSubmitSourceBDLog(t, logPath)
	assertBDLogContains(t, log, ownerBeadsDir, "show bd-source --json")
	assertBDLogContains(t, log, currentBeadsDir, "create --json")
	assertBDLogContains(t, log, ownerBeadsDir, "comments add bd-source")
	assertBDLogNotContains(t, log, currentBeadsDir, "show bd-source --json")
}

func TestDoneNoMRClosePathUsesRoutedSourceBeads(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	logPath := installSubmitSourceBDRecorder(t, currentBeadsDir, ownerBeadsDir)

	source, err := resolveSubmitSourceIssue(workDir, "bd-source")
	if err != nil {
		t.Fatalf("resolveSubmitSourceIssue: %v", err)
	}
	if skipReason, fatal := doneSourceCloseSkipReason(source.BD, "bd-source", source.Issue); skipReason != "" || fatal {
		t.Fatalf("doneSourceCloseSkipReason = %q, %v; want close allowed", skipReason, fatal)
	}
	if err := source.BD.ForceCloseWithReason("done", "bd-source"); err != nil {
		t.Fatalf("routed source close: %v", err)
	}

	log := readSubmitSourceBDLog(t, logPath)
	assertBDLogContains(t, log, ownerBeadsDir, "show bd-source --json")
	assertBDLogContains(t, log, ownerBeadsDir, "close bd-source")
	assertBDLogNotContains(t, log, currentBeadsDir, "close bd-source")
}

func TestRunMqSubmitWithRoutedIssueIgnoresCurrentRigMirror(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)
	branch := setupRoutedSubmitGitRepo(t, workDir, true)
	logPath := installSubmitSourceBDRecorder(t, currentBeadsDir, ownerBeadsDir)
	resetMqSubmitFlagsForTest(t)
	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))
	t.Setenv("GT_RIG", "")
	t.Chdir(workDir)

	mqSubmitBranch = branch
	mqSubmitIssue = "bd-source"
	mqSubmitNoCleanup = true
	if err := runMqSubmit(nil, nil); err != nil {
		t.Fatalf("runMqSubmit: %v", err)
	}

	log := readSubmitSourceBDLog(t, logPath)
	assertBDLogContains(t, log, ownerBeadsDir, "show bd-source --json")
	assertBDLogContains(t, log, currentBeadsDir, "create --json")
	assertBDLogContains(t, log, ownerBeadsDir, "comments add bd-source")
	assertBDLogNotContains(t, log, currentBeadsDir, "show bd-source --json")
}

func TestRunDoneWithRoutedIssueIgnoresCurrentRigMirror(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)
	setupRoutedSubmitGitRepo(t, workDir, false)
	logPath := installSubmitSourceBDRecorder(t, currentBeadsDir, ownerBeadsDir)
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
	if err := runDone(nil, nil); err != nil {
		t.Fatalf("runDone: %v", err)
	}

	log := readSubmitSourceBDLog(t, logPath)
	assertBDLogContains(t, log, ownerBeadsDir, "show bd-source --json")
	assertBDLogContains(t, log, currentBeadsDir, "create --json")
	assertBDLogContains(t, log, ownerBeadsDir, "comments add bd-source")
	assertBDLogContains(t, log, currentBeadsDir, "show gt-mr --json")
	assertBDLogNotContains(t, log, currentBeadsDir, "show bd-source --json")
}

func setupRoutedSourceTestTown(t *testing.T) (workDir, currentBeadsDir, ownerBeadsDir string) {
	t.Helper()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0o755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write town sentinel: %v", err)
	}

	workDir = filepath.Join(townRoot, "gastown", "polecats", "refuge", "gastown")
	currentBeadsDir = filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")
	ownerBeadsDir = filepath.Join(townRoot, "beads", "mayor", "rig", ".beads")
	townBeadsDir := filepath.Join(townRoot, ".beads")
	for _, dir := range []string{filepath.Join(workDir, ".beads"), currentBeadsDir, ownerBeadsDir, townBeadsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(workDir, ".beads", "redirect"), []byte("../../../mayor/rig/.beads\n"), 0o644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}
	if err := beads.WriteRoutes(townBeadsDir, []beads.Route{
		{Prefix: "gt-", Path: "gastown/mayor/rig"},
		{Prefix: "bd-", Path: "beads/mayor/rig"},
	}); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	return workDir, currentBeadsDir, ownerBeadsDir
}

func routedSourceTestTownRoot(workDir string) string {
	return filepath.Clean(filepath.Join(workDir, "..", "..", "..", ".."))
}

func setupRoutedSubmitCommandTown(t *testing.T, workDir string) {
	t.Helper()
	townRoot := routedSourceTestTownRoot(workDir)
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	if err := config.SaveRigsConfig(rigsPath, &config.RigsConfig{
		Version: config.CurrentRigsVersion,
		Rigs: map[string]config.RigEntry{
			"gastown": {GitURL: "file://test-gastown"},
		},
	}); err != nil {
		t.Fatalf("save rigs config: %v", err)
	}
}

func setupRoutedSubmitGitRepo(t *testing.T, workDir string, pushBranch bool) string {
	t.Helper()
	remote := t.TempDir()
	runGitForMQSubmitTest(t, remote, "init", "--bare")
	runGitForMQSubmitTest(t, workDir, "init")
	runGitForMQSubmitTest(t, workDir, "config", "user.email", "test@example.com")
	runGitForMQSubmitTest(t, workDir, "config", "user.name", "Test User")
	runGitForMQSubmitTest(t, workDir, "remote", "add", "origin", remote)
	writeMQSubmitTestFile(t, workDir, ".gitignore", ".beads/\n.runtime/\n")
	writeMQSubmitTestFile(t, workDir, "file.txt", "main\n")
	runGitForMQSubmitTest(t, workDir, "add", ".gitignore", "file.txt")
	runGitForMQSubmitTest(t, workDir, "commit", "-m", "main")
	runGitForMQSubmitTest(t, workDir, "branch", "-M", "main")
	runGitForMQSubmitTest(t, workDir, "push", "-u", "origin", "main")
	branch := "feature/routed-submit"
	runGitForMQSubmitTest(t, workDir, "checkout", "-b", branch)
	writeMQSubmitTestFile(t, workDir, "file.txt", "feature\n")
	runGitForMQSubmitTest(t, workDir, "commit", "-am", "feature")
	if pushBranch {
		runGitForMQSubmitTest(t, workDir, "push", "origin", branch)
	}
	return branch
}

func installSubmitSourceBDStub(t *testing.T, currentBeadsDir, ownerBeadsDir string, ownerMissing bool) {
	t.Helper()
	binDir := t.TempDir()
	ownerCase := fmt.Sprintf(`
if [ "$BEADS_DIR" = %q ]; then
  echo '[{"id":"bd-source","title":"owner source","status":"open","priority":1,"issue_type":"task"}]'
  exit 0
fi`, ownerBeadsDir)
	if ownerMissing {
		ownerCase = fmt.Sprintf(`
if [ "$BEADS_DIR" = %q ]; then
  echo "Issue not found in owner" >&2
  exit 1
fi`, ownerBeadsDir)
	}
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--allow-stale" ]; then
  shift
fi
if [ "$1" = "version" ]; then
  echo "bd stub"
  exit 0
fi
if [ "$1" = "show" ] && [ "$2" = "bd-source" ]; then
  if [ "$BEADS_DIR" = %q ]; then
    echo '[{"id":"bd-source","title":"current mirror","status":"open","priority":1,"issue_type":"task"}]'
    exit 0
  fi
%s
  echo "Issue not found in $BEADS_DIR" >&2
  exit 1
fi
echo "unexpected bd command: $*" >&2
exit 1
`, currentBeadsDir, ownerCase)
	path := filepath.Join(binDir, "bd")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	beads.ResetBdAllowStaleCacheForTest()
}

func installSubmitSourceBDRecorder(t *testing.T, currentBeadsDir, ownerBeadsDir string) string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "bd.log")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--allow-stale" ]; then
  shift
fi
if [ "$1" = "version" ]; then
  echo "bd stub"
  exit 0
fi
printf '%%s\t%%s\n' "$BEADS_DIR" "$*" >> %q
if [ "$1" = "show" ] && [ "$2" = "bd-source" ]; then
  if [ "$BEADS_DIR" = %q ]; then
    echo '[{"id":"bd-source","title":"current mirror","status":"open","priority":1,"issue_type":"task","description":"convoy_id: hq-cv-test\\nmerge_strategy: mr"}]'
    exit 0
  fi
  if [ "$BEADS_DIR" = %q ]; then
    echo '[{"id":"bd-source","title":"owner source","status":"open","priority":1,"issue_type":"task","description":"convoy_id: hq-cv-test\\nmerge_strategy: mr"}]'
    exit 0
  fi
  echo "Issue not found in $BEADS_DIR" >&2
  exit 1
fi
if [ "$1" = "show" ] && [ "$2" = "gt-mr" ]; then
  echo '[{"id":"gt-mr","title":"Merge: bd-source","status":"open","priority":1,"issue_type":"task","labels":["gt:merge-request"],"description":"branch: feature/routed-submit\\ntarget: main\\nsource_issue: bd-source\\nrig: gastown"}]'
  exit 0
fi
if [ "$1" = "list" ]; then
  echo '[]'
  exit 0
fi
if [ "$1" = "sql" ]; then
  echo '[]'
  exit 0
fi
if [ "$1" = "create" ]; then
  echo '{"id":"gt-mr","title":"Merge: bd-source","status":"open","priority":1,"issue_type":"task","labels":["gt:merge-request"]}'
  exit 0
fi
if [ "$1" = "comments" ] && [ "$2" = "add" ]; then
  exit 0
fi
if [ "$1" = "close" ]; then
  exit 0
fi
echo "unexpected bd command: $*" >&2
exit 1
`, logPath, currentBeadsDir, ownerBeadsDir)
	path := filepath.Join(binDir, "bd")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write bd recorder: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)
	return logPath
}

func readSubmitSourceBDLog(t *testing.T, logPath string) string {
	t.Helper()
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd recorder log: %v", err)
	}
	return string(log)
}

func assertBDLogContains(t *testing.T, log, beadsDir, args string) {
	t.Helper()
	needle := beadsDir + "\t" + args
	if !strings.Contains(log, needle) {
		t.Fatalf("bd log missing %q:\n%s", needle, log)
	}
}

func assertBDLogNotContains(t *testing.T, log, beadsDir, args string) {
	t.Helper()
	needle := beadsDir + "\t" + args
	if strings.Contains(log, needle) {
		t.Fatalf("bd log unexpectedly contains %q:\n%s", needle, log)
	}
}

func resetMqSubmitFlagsForTest(t *testing.T) {
	t.Helper()
	oldBranch, oldIssue, oldEpic := mqSubmitBranch, mqSubmitIssue, mqSubmitEpic
	oldPriority := mqSubmitPriority
	oldNoCleanup, oldSkipDeps, oldResubmit := mqSubmitNoCleanup, mqSubmitSkipDeps, mqSubmitResubmit
	mqSubmitBranch, mqSubmitIssue, mqSubmitEpic = "", "", ""
	mqSubmitPriority = -1
	mqSubmitNoCleanup, mqSubmitSkipDeps, mqSubmitResubmit = false, false, false
	t.Cleanup(func() {
		mqSubmitBranch, mqSubmitIssue, mqSubmitEpic = oldBranch, oldIssue, oldEpic
		mqSubmitPriority = oldPriority
		mqSubmitNoCleanup, mqSubmitSkipDeps, mqSubmitResubmit = oldNoCleanup, oldSkipDeps, oldResubmit
	})
}

func resetDoneFlagsForTest(t *testing.T) {
	t.Helper()
	oldIssue, oldStatus, oldCleanupStatus, oldTarget := doneIssue, doneStatus, doneCleanupStatus, doneTarget
	oldPriority := donePriority
	oldResume, oldPreVerified, oldSkipVerify := doneResume, donePreVerified, doneSkipVerify
	oldUpdateAgentStateOnDoneFn := updateAgentStateOnDoneFn
	doneIssue = ""
	donePriority = -1
	doneStatus = ExitCompleted
	doneCleanupStatus = ""
	doneResume = false
	donePreVerified = false
	doneTarget = ""
	doneSkipVerify = false
	t.Cleanup(func() {
		doneIssue, doneStatus, doneCleanupStatus, doneTarget = oldIssue, oldStatus, oldCleanupStatus, oldTarget
		donePriority = oldPriority
		doneResume, donePreVerified, doneSkipVerify = oldResume, oldPreVerified, oldSkipVerify
		updateAgentStateOnDoneFn = oldUpdateAgentStateOnDoneFn
	})
}
