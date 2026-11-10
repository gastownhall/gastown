package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/workflow"
)

// setupGatedDoneTown builds a polecat worktree for gastown/refuge with the
// gated workflow enabled for gastown, a fake bd that records every call,
// and the in-memory workflow env injected through newWorkflowEnv.
func setupGatedDoneTown(t *testing.T) (env workflow.GateEnv, bdLog string) {
	t.Helper()
	workDir, _, _ := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)
	setupRoutedSubmitGitRepo(t, workDir, false)
	bdLog = installGatedDoneBD(t)
	resetDoneFlagsForTest(t)
	townRoot := routedSourceTestTownRoot(workDir)

	settings := config.NewRigSettings()
	settings.Workflow = &config.WorkflowConfig{Gated: &config.GatedWorkflow{Enabled: true}}
	if err := config.SaveRigSettings(config.RigSettingsPath(filepath.Join(townRoot, "gastown")), settings); err != nil {
		t.Fatal(err)
	}
	env = workflow.GateEnv{
		TownRoot: townRoot,
		Rig:      "gastown",
		Store:    workflow.NewMemStore(),
		Items:    workflow.NewMemItems(),
		Config:   &config.GatedWorkflow{Enabled: true},
		Now:      func() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) },
	}
	oldEnv := newWorkflowEnv
	newWorkflowEnv = func(_ *cobra.Command, rig string) (workflow.GateEnv, error) {
		if rig != "gastown" {
			t.Errorf("workflow env for rig %q, want gastown", rig)
		}
		return env, nil
	}
	t.Cleanup(func() { newWorkflowEnv = oldEnv })

	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))
	t.Setenv("GT_TOWN_ROOT", townRoot)
	t.Setenv("GT_ROOT", townRoot)
	t.Setenv("GT_ROLE", "gastown/polecats/refuge")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "refuge")
	t.Setenv("BD_ACTOR", "gastown/polecats/refuge")
	t.Chdir(workDir)
	doneCleanupStatus = "unpushed"
	doneSkipVerify = true
	return env, bdLog
}

// installGatedDoneBD puts a bd on PATH that logs every call ("<BEADS_DIR>\t
// <args>") and succeeds. show returns the requested id as a hooked bead
// assigned to the polecat with attached_molecule gt-wisp-1.
func installGatedDoneBD(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "bd.log")
	script := fmt.Sprintf(`#!/bin/sh
[ "$1" = "--allow-stale" ] && shift
[ "$1" = "version" ] && { echo "bd stub"; exit 0; }
printf '%%s\t%%s\n' "$BEADS_DIR" "$*" >> %q
case "$1" in
  show) printf '[{"id":"%%s","title":"T","status":"hooked","assignee":"gastown/polecats/refuge","priority":1,"issue_type":"task","description":"attached_molecule: gt-wisp-1"}]\n' "$2" ;;
  list|sql) echo '[]' ;;
  create) echo '{"id":"gt-new","title":"x","status":"open","priority":1,"issue_type":"task"}' ;;
esac
exit 0
`, logPath)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)
	return logPath
}

func writeDraftSpec(t *testing.T, env workflow.GateEnv, id string) string {
	t.Helper()
	spec := workflow.ArtifactPath(env.TownRoot, env.Rig, id, "spec")
	if err := os.MkdirAll(filepath.Dir(spec), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec, []byte("---\nstatus: draft\n---\n# spec\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return spec
}

// fakeRetireKiller records session retirements.
func fakeRetireKiller(t *testing.T) *fakeDoneSessionKiller {
	t.Helper()
	fake := &fakeDoneSessionKiller{}
	old := newDoneSessionKiller
	newDoneSessionKiller = func() doneSessionKiller { return fake }
	t.Cleanup(func() { newDoneSessionKiller = old })
	return fake
}

// TestDone_GatedNonMergeStepNoMR: gt done on a gated item before merge
// creates no MR bead, records the gate's human wait, and still hands off.
func TestDone_GatedNonMergeStepNoMR(t *testing.T) {
	env, bdLog := setupGatedDoneTown(t)
	it, err := env.Items.Create("gastown", workflow.KindTask, "x", "")
	if err != nil {
		t.Fatal(err)
	}
	writeDraftSpec(t, env, it.ID)
	var handedOff []string
	updateAgentStateOnDoneFn = func(_, _, exitType, issueID string) error {
		handedOff = append(handedOff, exitType+" "+issueID)
		return nil
	}
	killer := fakeRetireKiller(t)
	doneIssue = it.ID
	doneStatus = ExitCompleted // the exit that would otherwise submit an MR

	if err := runDone(nil, nil); err != nil {
		t.Fatalf("runDone: %v", err)
	}
	log := readSubmitSourceBDLog(t, bdLog)
	if strings.Contains(log, "gt:merge-request") || strings.Contains(log, "Merge: "+it.ID) {
		t.Fatalf("gated non-merge step created an MR bead:\n%s", log)
	}
	got, _ := env.Items.Get(it.ID)
	if got.WaitingForHuman != "approve spec" {
		t.Errorf("waiting = %q, want %q", got.WaitingForHuman, "approve spec")
	}
	if len(handedOff) != 1 || handedOff[0] != "COMPLETED "+it.ID {
		t.Errorf("agent state handoff = %v", handedOff)
	}
	if killer.calls != 1 {
		t.Errorf("session retirements = %d, want 1 (R22)", killer.calls)
	}
}

// TestDone_GatedHoldReleasesItem (R22, R21): at a human wait the polecat
// runs plain gt done; the item ends open and unassigned with its waiting
// reason set, and the session retires.
func TestDone_GatedHoldReleasesItem(t *testing.T) {
	env, bdLog := setupGatedDoneTown(t)
	it, err := env.Items.Create("gastown", workflow.KindTask, "x", "")
	if err != nil {
		t.Fatal(err)
	}
	writeDraftSpec(t, env, it.ID)
	killer := fakeRetireKiller(t)
	doneIssue = it.ID
	updateAgentStateOnDoneFn = updateAgentStateOnDone // the real one

	if err := runDone(nil, nil); err != nil {
		t.Fatalf("runDone: %v", err)
	}
	log := readSubmitSourceBDLog(t, bdLog)
	if !strings.Contains(log, "update "+it.ID+" --status=open --assignee=") {
		t.Errorf("item not released (open, unassigned):\n%s", log)
	}
	if strings.Contains(log, "close "+it.ID) || strings.Contains(log, "gt:merge-request") {
		t.Errorf("item closed or MR created:\n%s", log)
	}
	if got, _ := env.Items.Get(it.ID); got.WaitingForHuman != "approve spec" {
		t.Errorf("waiting = %q, want approve spec", got.WaitingForHuman)
	}
	wantSession := session.PolecatSessionName(session.PrefixFor("gastown"), "refuge")
	if killer.calls != 1 || killer.name != wantSession {
		t.Errorf("session retirements = %d (%q), want 1 (%q)", killer.calls, killer.name, wantSession)
	}
}

// assertItemKept checks the bd log shows the gated item neither released nor
// closed: it stays hooked and assigned to the preserved session (R26).
func assertItemKept(t *testing.T, log, id string) {
	t.Helper()
	if strings.Contains(log, "update "+id+" --status=open --assignee=") {
		t.Errorf("item released although the session is kept:\n%s", log)
	}
	if strings.Contains(log, "\tclose "+id) || strings.Contains(log, "gt:merge-request") {
		t.Errorf("item closed or MR created:\n%s", log)
	}
}

// TestDone_GatedHoldEscalatedKeepsItem (R26): an ESCALATED gated hold keeps
// the session and leaves the item hooked and assigned to it.
func TestDone_GatedHoldEscalatedKeepsItem(t *testing.T) {
	env, bdLog := setupGatedDoneTown(t)
	it, err := env.Items.Create("gastown", workflow.KindTask, "x", "")
	if err != nil {
		t.Fatal(err)
	}
	writeDraftSpec(t, env, it.ID)
	killer := fakeRetireKiller(t)
	doneIssue = it.ID
	doneStatus = ExitEscalated
	updateAgentStateOnDoneFn = updateAgentStateOnDone

	if err := runDone(nil, nil); err != nil {
		t.Fatalf("runDone: %v", err)
	}
	assertItemKept(t, readSubmitSourceBDLog(t, bdLog), it.ID)
	if killer.calls != 0 {
		t.Errorf("session retired on ESCALATED (calls=%d)", killer.calls)
	}
}

// TestDone_GatedHoldPushFailedKeepsItem (R26): when the gated push fails the
// session is preserved and the item stays hooked and assigned to it.
func TestDone_GatedHoldPushFailedKeepsItem(t *testing.T) {
	env, bdLog := setupGatedDoneTown(t)
	it, err := env.Items.Create("gastown", workflow.KindTask, "x", "")
	if err != nil {
		t.Fatal(err)
	}
	writeDraftSpec(t, env, it.ID)
	cwd, _ := os.Getwd()
	runGitForMQSubmitTest(t, cwd, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	killer := fakeRetireKiller(t)
	doneIssue = it.ID
	updateAgentStateOnDoneFn = updateAgentStateOnDone

	if err := runDone(nil, nil); err != nil {
		t.Fatalf("runDone: %v", err)
	}
	assertItemKept(t, readSubmitSourceBDLog(t, bdLog), it.ID)
	if killer.calls != 0 {
		t.Errorf("session retired after a failed push (calls=%d)", killer.calls)
	}
}

// TestUpdateAgentStateOnDone_GatedHoldNoRelease (R26): when the hold does
// not retire the session, the agent-state update leaves the item alone.
func TestUpdateAgentStateOnDone_GatedHoldNoRelease(t *testing.T) {
	env, bdLog := setupGatedDoneTown(t)
	cwd, _ := os.Getwd()
	doneHoldGatedItem, doneReleaseGatedItem = true, false
	t.Cleanup(func() { doneHoldGatedItem, doneReleaseGatedItem = false, false })

	if err := updateAgentStateOnDone(cwd, env.TownRoot, ExitEscalated, "gt-item"); err != nil {
		t.Fatalf("updateAgentStateOnDone: %v", err)
	}
	log := readSubmitSourceBDLog(t, bdLog)
	assertItemKept(t, log, "gt-item")
	if strings.Contains(log, "close gt-wisp-1") {
		t.Errorf("step molecule closed although the session is kept:\n%s", log)
	}
}

// TestDone_GatedHoldClearsWaiting (R21): once the step's gate no longer
// waits on a human, gt done clears the item's waiting reason.
func TestDone_GatedHoldClearsWaiting(t *testing.T) {
	env, _ := setupGatedDoneTown(t)
	it, err := env.Items.Create("gastown", workflow.KindTask, "x", "")
	if err != nil {
		t.Fatal(err)
	}
	spec := writeDraftSpec(t, env, it.ID)
	fakeRetireKiller(t)
	updateAgentStateOnDoneFn = func(_, _, _, _ string) error { return nil }
	doneIssue = it.ID
	if err := runDone(nil, nil); err != nil {
		t.Fatalf("runDone: %v", err)
	}
	if got, _ := env.Items.Get(it.ID); got.WaitingForHuman != "approve spec" {
		t.Fatalf("first done: waiting = %q", got.WaitingForHuman)
	}

	_, sha, _, err := workflow.ReadArtifact(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Store.AddApproval(workflow.Approval{ItemID: it.ID, Artifact: "spec", SHA: sha, Verdict: "approve",
		Role: "human", Source: "human", Channel: "terminal", Actor: "h"}); err != nil {
		t.Fatal(err)
	}
	if err := runDone(nil, nil); err != nil {
		t.Fatalf("second runDone: %v", err)
	}
	if got, _ := env.Items.Get(it.ID); got.WaitingForHuman != "" {
		t.Fatalf("waiting after the gate passed = %q, want empty", got.WaitingForHuman)
	}
}

type failingItems struct{ workflow.Items }

var errItemsDown = errors.New("dolt: connection refused")

func (failingItems) Get(string) (*workflow.Item, error) { return nil, errItemsDown }

// TestDone_GatedLookupErrorFailsClosed (R24): an Items.Get error other than
// not-found is returned and the MR path is not taken.
func TestDone_GatedLookupErrorFailsClosed(t *testing.T) {
	env, bdLog := setupGatedDoneTown(t)
	_ = env
	oldEnv := newWorkflowEnv
	newWorkflowEnv = func(c *cobra.Command, rig string) (workflow.GateEnv, error) {
		e, err := oldEnv(c, rig)
		e.Items = failingItems{e.Items}
		return e, err
	}
	t.Cleanup(func() { newWorkflowEnv = oldEnv })
	updateAgentStateOnDoneFn = func(_, _, _, _ string) error { return nil }
	doneIssue = "gt-item"

	err := runDone(nil, nil)
	if err == nil || !errors.Is(err, errItemsDown) {
		t.Fatalf("runDone = %v, want the Items.Get error", err)
	}
	if log := readSubmitSourceBDLog(t, bdLog); strings.Contains(log, "gt:merge-request") || strings.Contains(log, "Merge: gt-item") {
		t.Fatalf("MR path taken:\n%s", log)
	}
}

// TestUpdateAgentStateOnDone_GatedHold (R22, review 5): the real agent-state
// update on a gated hold closes only the step molecule, never the item bead,
// and releases the item (open, unassigned).
func TestUpdateAgentStateOnDone_GatedHold(t *testing.T) {
	env, bdLog := setupGatedDoneTown(t)
	cwd, _ := os.Getwd()
	doneHoldGatedItem, doneReleaseGatedItem = true, true
	t.Cleanup(func() { doneHoldGatedItem, doneReleaseGatedItem = false, false })

	if err := updateAgentStateOnDone(cwd, env.TownRoot, ExitCompleted, "gt-item"); err != nil {
		t.Fatalf("updateAgentStateOnDone: %v", err)
	}
	log := readSubmitSourceBDLog(t, bdLog)
	var closes []string
	for _, line := range strings.Split(log, "\n") {
		if i := strings.Index(line, "\tclose "); i >= 0 {
			closes = append(closes, line[i+1:])
		}
	}
	if len(closes) == 0 || !strings.Contains(strings.Join(closes, "|"), "gt-wisp-1") {
		t.Errorf("molecule gt-wisp-1 not closed; closes = %v", closes)
	}
	for _, c := range closes {
		if strings.Contains(c, "gt-item") {
			t.Errorf("item bead closed: %q", c)
		}
	}
	if !strings.Contains(log, "update gt-item --status=open --assignee=") {
		t.Errorf("item not released:\n%s", log)
	}
}

// TestDone_GatedMergeStepUsesMRPath: at merge, gt done takes the existing MR
// submit path and creates the MR bead.
func TestDone_GatedMergeStepUsesMRPath(t *testing.T) {
	env, bdLog := setupGatedDoneTown(t)
	it, err := env.Items.Create("gastown", workflow.KindTask, "x", "")
	if err != nil {
		t.Fatal(err)
	}
	steps := workflow.Steps(workflow.KindTask)
	for i := 0; i+1 < len(steps); i++ {
		if err := env.Store.Transition(it.ID, steps[i], steps[i+1], "t"); err != nil {
			t.Fatal(err)
		}
	}
	updateAgentStateOnDoneFn = func(_, _, _, _ string) error { return nil }
	fakeRetireKiller(t)
	doneIssue = it.ID
	if err := runDone(nil, nil); err != nil {
		t.Fatalf("runDone at merge: %v", err)
	}
	log := readSubmitSourceBDLog(t, bdLog)
	if !strings.Contains(log, "gt:merge-request") || !strings.Contains(log, "Merge: "+it.ID) {
		t.Fatalf("merge step did not create the MR bead:\n%s", log)
	}
	if strings.Contains(log, "update "+it.ID+" --status=open --assignee=") {
		t.Errorf("merge step released the item like a hold:\n%s", log)
	}
	if got, _ := env.Items.Get(it.ID); got.WaitingForHuman != "" {
		t.Errorf("merge step recorded a wait: %q", got.WaitingForHuman)
	}
}
