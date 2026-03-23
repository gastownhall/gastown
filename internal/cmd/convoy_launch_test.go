package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

// IT-14: Staged:ready convoy transitions to open.
func TestTransitionConvoyToOpen_StagedReady(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Convoy("hq-cv-test", "Test").WithStatus("staged_ready")

	_, logPath := dag.Setup(t)

	err := transitionConvoyToOpen("hq-cv-test", false)
	if err != nil {
		t.Fatalf("transitionConvoyToOpen: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd.log: %v", err)
	}
	logContent := string(logBytes)

	// Should have called bd show to get status.
	if !strings.Contains(logContent, "CMD:show hq-cv-test --json") {
		t.Errorf("bd.log should contain 'CMD:show hq-cv-test --json', got:\n%s", logContent)
	}

	// Should have called bd update to transition to open.
	if !strings.Contains(logContent, "CMD:update hq-cv-test --status=open") {
		t.Errorf("bd.log should contain 'CMD:update hq-cv-test --status=open', got:\n%s", logContent)
	}
}

// IT-15: Staged:warnings without force returns error mentioning --force.
func TestTransitionConvoyToOpen_StagedWarningsNoForce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Convoy("hq-cv-warn", "Warnings Convoy").WithStatus("staged_warnings")

	dag.Setup(t)

	err := transitionConvoyToOpen("hq-cv-warn", false)
	if err == nil {
		t.Fatal("expected error for staged_warnings without force, got nil")
	}

	if !strings.Contains(err.Error(), "force") {
		t.Errorf("error should mention --force, got: %v", err)
	}
	if !strings.Contains(err.Error(), "warnings") {
		t.Errorf("error should mention warnings, got: %v", err)
	}
}

// IT-16: Staged:warnings with force=true transitions to open.
func TestTransitionConvoyToOpen_StagedWarningsWithForce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Convoy("hq-cv-forcew", "Force Warnings").WithStatus("staged_warnings")

	_, logPath := dag.Setup(t)

	err := transitionConvoyToOpen("hq-cv-forcew", true)
	if err != nil {
		t.Fatalf("transitionConvoyToOpen with force: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd.log: %v", err)
	}
	logContent := string(logBytes)

	// Should have called bd update to transition to open.
	if !strings.Contains(logContent, "CMD:update hq-cv-forcew --status=open") {
		t.Errorf("bd.log should contain 'CMD:update hq-cv-forcew --status=open', got:\n%s", logContent)
	}
}

// IT-18: Already-open convoy returns "already launched" error.
func TestTransitionConvoyToOpen_AlreadyOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Convoy("hq-cv-open", "Open Convoy").WithStatus("open")

	dag.Setup(t)

	err := transitionConvoyToOpen("hq-cv-open", false)
	if err == nil {
		t.Fatal("expected error for already-open convoy, got nil")
	}

	if !strings.Contains(err.Error(), "already launched") {
		t.Errorf("error should say 'already launched', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Subcommand registration tests (gt-csl.6.4)
// ---------------------------------------------------------------------------

// TestConvoySubcommandRegistration verifies that convoyStageCmd and
// convoyLaunchCmd are registered as subcommands of convoyCmd.
func TestConvoySubcommandRegistration(t *testing.T) {
	// Verify convoyStageCmd exists and has the expected Use string.
	if convoyStageCmd == nil {
		t.Fatal("convoyStageCmd is nil")
	}
	if !strings.HasPrefix(convoyStageCmd.Use, "stage") {
		t.Errorf("convoyStageCmd.Use = %q, want prefix 'stage'", convoyStageCmd.Use)
	}

	// Verify convoyLaunchCmd exists and has the expected Use string.
	if convoyLaunchCmd == nil {
		t.Fatal("convoyLaunchCmd is nil")
	}
	if !strings.HasPrefix(convoyLaunchCmd.Use, "launch") {
		t.Errorf("convoyLaunchCmd.Use = %q, want prefix 'launch'", convoyLaunchCmd.Use)
	}

	// Verify both are registered as subcommands of convoyCmd.
	subCmds := convoyCmd.Commands()
	foundStage := false
	foundLaunch := false
	for _, cmd := range subCmds {
		switch cmd.Name() {
		case "stage":
			foundStage = true
		case "launch":
			foundLaunch = true
		}
	}
	if !foundStage {
		t.Errorf("convoyStageCmd not registered as subcommand of convoyCmd")
	}
	if !foundLaunch {
		t.Errorf("convoyLaunchCmd not registered as subcommand of convoyCmd")
	}
}

// TestConvoyStageLaunchFlag verifies that the --launch flag exists on convoyStageCmd.
func TestConvoyStageLaunchFlag(t *testing.T) {
	flag := convoyStageCmd.Flags().Lookup("launch")
	if flag == nil {
		t.Fatal("convoyStageCmd should have --launch flag")
	}
	if flag.DefValue != "false" {
		t.Errorf("--launch default = %q, want %q", flag.DefValue, "false")
	}
}

// ---------------------------------------------------------------------------
// Launch-as-alias tests (gt-csl.6.4)
// ---------------------------------------------------------------------------

// IT-19: gt convoy launch <epic-id> delegates to stage+launch (no "not yet
// implemented" error). Verifies the delegation path is wired up.
//
// Note: rigFromBeadID() is a stub returning "", so the staging pipeline will
// hit no-rig errors and stop before creating a convoy. The test verifies
// delegation happened (bd show/dep list ran) and the old "not yet implemented"
// error is gone.
func TestLaunchAsAlias_EpicInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	// Build a DAG with an epic and two child tasks.
	td := newTestDAG(t).
		Epic("gt-epic-1", "Test Epic").
		Task("gt-task-1", "First task", withRig("gastown")).ParentOf("gt-epic-1").
		Task("gt-task-2", "Second task", withRig("gastown")).ParentOf("gt-epic-1")

	_, logPath := td.Setup(t)

	// Clean up shared state.
	defer func() {
		convoyStageLaunch = false
		convoyLaunchForce = false
	}()

	err := runConvoyLaunch(convoyLaunchCmd, []string{"gt-epic-1"})

	// The error should NOT be "stage-and-launch not yet implemented".
	// It will fail with staging errors (no-rig) because rigFromBeadID() is a
	// stub, but the delegation to runConvoyStage happened.
	if err != nil && strings.Contains(err.Error(), "stage-and-launch not yet implemented") {
		t.Errorf("should not get 'not yet implemented' error; delegation failed: %v", err)
	}

	// Verify bd.log shows staging activity (bd show for the epic + children).
	logBytes, err2 := os.ReadFile(logPath)
	if err2 != nil {
		t.Fatalf("read bd.log: %v", err2)
	}
	logContent := string(logBytes)

	// Should have called bd show for the epic (proving delegation to runConvoyStage).
	if !strings.Contains(logContent, "CMD:show gt-epic-1 --json") {
		t.Errorf("bd.log should contain 'CMD:show gt-epic-1 --json' (staging delegation), got:\n%s", logContent)
	}

	// Should have called bd dep list for child tasks (proving staging pipeline ran).
	if !strings.Contains(logContent, "CMD:dep list gt-task-1 --json") {
		t.Errorf("bd.log should contain 'CMD:dep list gt-task-1 --json' (staging ran), got:\n%s", logContent)
	}

	// Verify convoyStageLaunch was reset by defer in runConvoyLaunch.
	if convoyStageLaunch {
		t.Error("convoyStageLaunch should be reset to false after runConvoyLaunch")
	}
}

// IT-20: gt convoy launch <task-id1> <task-id2> delegates to stage+launch for
// task list input.
//
// Note: rigFromBeadID() is a stub returning "", so the staging pipeline will
// hit no-rig errors. The test verifies delegation happened.
func TestLaunchAsAlias_TaskListInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	// Build a DAG with two independent tasks.
	td := newTestDAG(t).
		Task("gt-t1", "Task One", withRig("gastown")).
		Task("gt-t2", "Task Two", withRig("gastown"))

	_, logPath := td.Setup(t)

	// Clean up shared state.
	defer func() {
		convoyStageLaunch = false
		convoyLaunchForce = false
	}()

	err := runConvoyLaunch(convoyLaunchCmd, []string{"gt-t1", "gt-t2"})

	// Should NOT get "not yet implemented".
	if err != nil && strings.Contains(err.Error(), "stage-and-launch not yet implemented") {
		t.Errorf("should not get 'not yet implemented' error; delegation failed: %v", err)
	}

	// Verify bd.log shows staging activity.
	logBytes, err2 := os.ReadFile(logPath)
	if err2 != nil {
		t.Fatalf("read bd.log: %v", err2)
	}
	logContent := string(logBytes)

	// Should have called bd show for both tasks (proving delegation to runConvoyStage).
	if !strings.Contains(logContent, "CMD:show gt-t1 --json") {
		t.Errorf("bd.log should contain 'CMD:show gt-t1 --json', got:\n%s", logContent)
	}
	if !strings.Contains(logContent, "CMD:show gt-t2 --json") {
		t.Errorf("bd.log should contain 'CMD:show gt-t2 --json', got:\n%s", logContent)
	}

	// Should have called bd dep list (proving staging pipeline ran).
	if !strings.Contains(logContent, "CMD:dep list gt-t1 --json") {
		t.Errorf("bd.log should contain 'CMD:dep list gt-t1 --json' (staging ran), got:\n%s", logContent)
	}
}

// IT-33: Staged:ready convoy-id: verify bd.log does NOT contain dep list or
// list --parent (no re-analysis). Only bd show and bd update.
func TestTransitionConvoyToOpen_SkipsReanalysis(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Convoy("hq-cv-noanal", "No Reanalysis").WithStatus("staged_ready")

	_, logPath := dag.Setup(t)

	err := transitionConvoyToOpen("hq-cv-noanal", false)
	if err != nil {
		t.Fatalf("transitionConvoyToOpen: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd.log: %v", err)
	}
	logContent := string(logBytes)

	// Should only contain show and update — no dep list or list --parent.
	if strings.Contains(logContent, "dep list") {
		t.Errorf("bd.log should NOT contain 'dep list' (no re-analysis), got:\n%s", logContent)
	}
	if strings.Contains(logContent, "list --parent") {
		t.Errorf("bd.log should NOT contain 'list --parent' (no re-analysis), got:\n%s", logContent)
	}

	// Verify it DID call show and update.
	if !strings.Contains(logContent, "CMD:show hq-cv-noanal --json") {
		t.Errorf("bd.log should contain 'CMD:show hq-cv-noanal --json', got:\n%s", logContent)
	}
	if !strings.Contains(logContent, "CMD:update hq-cv-noanal --status=open") {
		t.Errorf("bd.log should contain 'CMD:update hq-cv-noanal --status=open', got:\n%s", logContent)
	}
}

// ---------------------------------------------------------------------------
// Wave 1 dispatch tests
// ---------------------------------------------------------------------------

// IT-14 (extended): dispatchWave1 dispatches exactly Wave 1 tasks.
// DAG: A blocks B, C independent → Wave 1 = [A, C], Wave 2 = [B].
// Verifies A and C are dispatched, B is NOT.
func TestDispatchWave1_AllDispatched(t *testing.T) {
	// Build a DAG directly (no bd stub needed — dispatchWave1 is pure logic).
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"task-a": {ID: "task-a", Type: "task", Rig: "gastown", Blocks: []string{"task-b"}},
		"task-b": {ID: "task-b", Type: "task", Rig: "gastown", BlockedBy: []string{"task-a"}},
		"task-c": {ID: "task-c", Type: "task", Rig: "beads"},
	}}

	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	// Stub dispatchTaskDirect to log calls.
	var mu sync.Mutex
	var dispatched []string
	orig := dispatchTaskDirect
	dispatchTaskDirect = func(townRoot, beadID, rig string) error {
		mu.Lock()
		dispatched = append(dispatched, beadID)
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { dispatchTaskDirect = orig })

	results, err := dispatchWave1("test-convoy", dag, waves, "")
	if err != nil {
		t.Fatalf("dispatchWave1: %v", err)
	}

	// Wave 1 should contain task-a and task-c (sorted).
	sort.Strings(dispatched)
	if len(dispatched) != 2 {
		t.Fatalf("expected 2 dispatched tasks, got %d: %v", len(dispatched), dispatched)
	}
	if dispatched[0] != "task-a" || dispatched[1] != "task-c" {
		t.Errorf("expected [task-a, task-c], got %v", dispatched)
	}

	// Verify results match.
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		if !r.Success {
			t.Errorf("task %s should have succeeded", r.BeadID)
		}
	}

	// Verify task-b was NOT dispatched (it's in Wave 2).
	for _, id := range dispatched {
		if id == "task-b" {
			t.Errorf("task-b should NOT be dispatched in Wave 1")
		}
	}
}

// IT-17: dispatchWave1 continues on individual task failure.
// Stub fails for task-a but succeeds for task-c. Both must be attempted.
func TestDispatchWave1_ContinuesOnFailure(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"task-a": {ID: "task-a", Type: "task", Rig: "gastown"},
		"task-c": {ID: "task-c", Type: "task", Rig: "beads"},
	}}

	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	// Stub: task-a fails, task-c succeeds.
	var mu sync.Mutex
	var attempted []string
	orig := dispatchTaskDirect
	dispatchTaskDirect = func(townRoot, beadID, rig string) error {
		mu.Lock()
		attempted = append(attempted, beadID)
		mu.Unlock()
		if beadID == "task-a" {
			return fmt.Errorf("simulated dispatch failure for %s", beadID)
		}
		return nil
	}
	t.Cleanup(func() { dispatchTaskDirect = orig })

	results, err := dispatchWave1("test-convoy", dag, waves, "")
	if err != nil {
		t.Fatalf("dispatchWave1: %v", err)
	}

	// Both tasks must have been attempted.
	sort.Strings(attempted)
	if len(attempted) != 2 {
		t.Fatalf("expected 2 attempted tasks, got %d: %v", len(attempted), attempted)
	}
	if attempted[0] != "task-a" || attempted[1] != "task-c" {
		t.Errorf("expected [task-a, task-c] attempted, got %v", attempted)
	}

	// Verify results: task-a failed, task-c succeeded.
	resultMap := make(map[string]DispatchResult)
	for _, r := range results {
		resultMap[r.BeadID] = r
	}

	if resultMap["task-a"].Success {
		t.Errorf("task-a should have failed")
	}
	if resultMap["task-a"].Error == nil {
		t.Errorf("task-a should have a non-nil error")
	}
	if !resultMap["task-c"].Success {
		t.Errorf("task-c should have succeeded")
	}
	if resultMap["task-c"].Error != nil {
		t.Errorf("task-c should have nil error, got: %v", resultMap["task-c"].Error)
	}
}

// ---------------------------------------------------------------------------
// renderLaunchOutput tests (gt-csl.6.3)
// ---------------------------------------------------------------------------

// IT-29: Output contains convoy ID and gt convoy status <id> command.
func TestRenderLaunchOutput_ConvoyIDAndMonitor(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-task-1": {ID: "gt-task-1", Title: "Task One", Type: "task", Rig: "gastown"},
	}}

	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	results := []DispatchResult{
		{BeadID: "gt-task-1", Rig: "gastown", Success: true},
	}

	output := renderLaunchOutput("hq-cv-abc12", waves, results, dag)

	if !strings.Contains(output, "hq-cv-abc12") {
		t.Errorf("output should contain convoy ID 'hq-cv-abc12', got:\n%s", output)
	}
	if !strings.Contains(output, "gt convoy status hq-cv-abc12") {
		t.Errorf("output should contain 'gt convoy status hq-cv-abc12', got:\n%s", output)
	}
}

// IT-30: Each dispatched task shows bead ID, title, and rig.
func TestRenderLaunchOutput_DispatchedTasksWithRig(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-task-1": {ID: "gt-task-1", Title: "Task One", Type: "task", Rig: "gastown"},
		"gt-task-2": {ID: "gt-task-2", Title: "Task Two", Type: "task", Rig: "beads"},
	}}

	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	results := []DispatchResult{
		{BeadID: "gt-task-1", Rig: "gastown", Success: true},
		{BeadID: "gt-task-2", Rig: "beads", Success: true},
	}

	output := renderLaunchOutput("hq-cv-test", waves, results, dag)

	// Each task's bead ID, title, and rig must appear.
	for _, r := range results {
		if !strings.Contains(output, r.BeadID) {
			t.Errorf("output should contain bead ID %q, got:\n%s", r.BeadID, output)
		}
		node := dag.Nodes[r.BeadID]
		if !strings.Contains(output, node.Title) {
			t.Errorf("output should contain title %q, got:\n%s", node.Title, output)
		}
		if !strings.Contains(output, r.Rig) {
			t.Errorf("output should contain rig %q, got:\n%s", r.Rig, output)
		}
	}
}

// IT-31: Output contains gt convoy -i TUI hint.
func TestRenderLaunchOutput_TUIHint(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-task-1": {ID: "gt-task-1", Title: "Task One", Type: "task", Rig: "gastown"},
	}}

	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	results := []DispatchResult{
		{BeadID: "gt-task-1", Rig: "gastown", Success: true},
	}

	output := renderLaunchOutput("hq-cv-test", waves, results, dag)

	if !strings.Contains(output, "gt convoy -i") {
		t.Errorf("output should contain 'gt convoy -i' TUI hint, got:\n%s", output)
	}
}

// IT-32: Output contains daemon explanation about automatic subsequent wave dispatch.
func TestRenderLaunchOutput_DaemonExplanation(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-task-1": {ID: "gt-task-1", Title: "Task One", Type: "task", Rig: "gastown"},
		"gt-task-2": {ID: "gt-task-2", Title: "Task Two", Type: "task", Rig: "gastown", BlockedBy: []string{"gt-task-1"}},
	}}
	dag.Nodes["gt-task-1"].Blocks = []string{"gt-task-2"}

	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	results := []DispatchResult{
		{BeadID: "gt-task-1", Rig: "gastown", Success: true},
	}

	output := renderLaunchOutput("hq-cv-test", waves, results, dag)

	if !strings.Contains(output, "daemon") {
		t.Errorf("output should mention 'daemon', got:\n%s", output)
	}
	if !strings.Contains(output, "automatically") {
		t.Errorf("output should mention 'automatically', got:\n%s", output)
	}
}

// SN-03: Full output snapshot test with 2 waves, 3 tasks, some failed dispatches.
func TestRenderLaunchOutput_Snapshot(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-task-1": {ID: "gt-task-1", Title: "Task One", Type: "task", Rig: "gastown", Blocks: []string{"gt-task-3"}},
		"gt-task-2": {ID: "gt-task-2", Title: "Task Two", Type: "task", Rig: "gastown"},
		"gt-task-3": {ID: "gt-task-3", Title: "Task Three", Type: "task", Rig: "beads", BlockedBy: []string{"gt-task-1"}},
	}}

	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	// Wave 1 = [gt-task-1, gt-task-2], Wave 2 = [gt-task-3]
	if len(waves) != 2 {
		t.Fatalf("expected 2 waves, got %d", len(waves))
	}

	results := []DispatchResult{
		{BeadID: "gt-task-1", Rig: "gastown", Success: true},
		{BeadID: "gt-task-2", Rig: "gastown", Success: false, Error: fmt.Errorf("connection failed")},
	}

	output := renderLaunchOutput("hq-cv-abc12", waves, results, dag)

	// Section 1: Convoy ID and status
	if !strings.Contains(output, "Convoy launched: hq-cv-abc12 (status: open)") {
		t.Errorf("missing convoy ID line, got:\n%s", output)
	}

	// Section 2: Monitor command
	if !strings.Contains(output, "gt convoy status hq-cv-abc12") {
		t.Errorf("missing monitor command, got:\n%s", output)
	}

	// Section 3: Wave summary
	if !strings.Contains(output, "2 waves") {
		t.Errorf("missing wave count, got:\n%s", output)
	}
	if !strings.Contains(output, "3 tasks total") {
		t.Errorf("missing total task count, got:\n%s", output)
	}
	if !strings.Contains(output, "Wave 1:") && !strings.Contains(output, "dispatched") {
		t.Errorf("missing Wave 1 summary, got:\n%s", output)
	}
	if !strings.Contains(output, "Wave 2:") && !strings.Contains(output, "pending") {
		t.Errorf("missing Wave 2 summary, got:\n%s", output)
	}

	// Section 4: Dispatched tasks with status markers
	if !strings.Contains(output, "gt-task-1") {
		t.Errorf("missing dispatched task gt-task-1, got:\n%s", output)
	}
	if !strings.Contains(output, "Task One") {
		t.Errorf("missing title 'Task One', got:\n%s", output)
	}
	// Success marker
	if !strings.Contains(output, "✓") {
		t.Errorf("missing success marker ✓, got:\n%s", output)
	}
	// Failure marker and error
	if !strings.Contains(output, "✗") {
		t.Errorf("missing failure marker ✗, got:\n%s", output)
	}
	if !strings.Contains(output, "connection failed") {
		t.Errorf("missing error message 'connection failed', got:\n%s", output)
	}

	// Section 5: TUI hint
	if !strings.Contains(output, "gt convoy -i") {
		t.Errorf("missing TUI hint, got:\n%s", output)
	}

	// Section 6: Daemon explanation
	if !strings.Contains(output, "daemon") {
		t.Errorf("missing daemon mention, got:\n%s", output)
	}
	if !strings.Contains(output, "automatically") {
		t.Errorf("missing 'automatically' mention, got:\n%s", output)
	}
}

// IT-23: End-to-end test — staged_ready convoy with tracked tasks → transition
// → rebuild DAG → dispatch Wave 1. Uses bd stub + dispatchTaskDirect stub.
func TestDispatchWave1_EndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	// Build a convoy tracking 3 tasks: A blocks B, C independent.
	// Wave 1 = [A, C], Wave 2 = [B].
	td := newTestDAG(t).
		Convoy("hq-cv-e2e", "E2E Launch").WithStatus("staged_ready").
		Task("hq-task-a", "Task A", withRig("gastown")).TrackedBy("hq-cv-e2e").
		Task("hq-task-b", "Task B", withRig("gastown")).TrackedBy("hq-cv-e2e").BlockedBy("hq-task-a").
		Task("hq-task-c", "Task C", withRig("gastown")).TrackedBy("hq-cv-e2e")

	td.Setup(t)

	// Stub dispatchTaskDirect to log dispatches.
	var mu sync.Mutex
	var dispatched []string
	orig := dispatchTaskDirect
	dispatchTaskDirect = func(townRoot, beadID, rig string) error {
		mu.Lock()
		dispatched = append(dispatched, beadID)
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { dispatchTaskDirect = orig })

	// Run the full flow: collectConvoyBeads → buildConvoyDAG → computeWaves → dispatchWave1.
	beads, deps, err := collectConvoyBeads("hq-cv-e2e")
	if err != nil {
		t.Fatalf("collectConvoyBeads: %v", err)
	}

	dag := buildConvoyDAG(beads, deps)
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	results, err := dispatchWave1("hq-cv-e2e", dag, waves, "")
	if err != nil {
		t.Fatalf("dispatchWave1: %v", err)
	}

	// Verify Wave 1 dispatched A and C, NOT B.
	sort.Strings(dispatched)
	if len(dispatched) != 2 {
		t.Fatalf("expected 2 dispatched tasks, got %d: %v", len(dispatched), dispatched)
	}
	if dispatched[0] != "hq-task-a" || dispatched[1] != "hq-task-c" {
		t.Errorf("expected [hq-task-a, hq-task-c], got %v", dispatched)
	}

	// All dispatched tasks should have succeeded.
	for _, r := range results {
		if !r.Success {
			t.Errorf("task %s should have succeeded, got error: %v", r.BeadID, r.Error)
		}
	}

	// Verify task-b was not dispatched.
	for _, id := range dispatched {
		if id == "hq-task-b" {
			t.Errorf("hq-task-b should NOT be dispatched in Wave 1")
		}
	}
}

// GH-2373 regression:
// collectConvoyBeads must handle tracked deps returned as id-only external refs
// (external:<rig>:<id>) and resolve them into raw bead IDs for lookup.
func TestCollectConvoyBeads_ExternalTrackedIDs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	townRoot := t.TempDir()
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}

	binDir := t.TempDir()
	bdPath := filepath.Join(binDir, "bd")
	bdScript := `#!/bin/sh
case "$*" in
  "show hq-cv-ext --json")
    echo '[{"id":"hq-cv-ext","title":"Ext convoy","status":"staged_ready","issue_type":"convoy","labels":["gt:convoy"]}]'
    ;;
  sql\ *"issue_id = 'hq-cv-ext'"*)
    # bdDepListRawIDs down: return tracked bead IDs
    echo '[{"depends_on_id":"external:ghostty:ghostty-1i4.3"},{"depends_on_id":"external:ghostty:ghostty-1i4.4"}]'
    ;;
  "dep list hq-cv-ext --direction=down --type=tracks --allow-stale --json")
    echo '[{"id":"external:ghostty:ghostty-1i4.3"},{"id":"external:ghostty:ghostty-1i4.4"}]'
    ;;
  "dep list hq-cv-ext --json")
    echo '[{"id":"external:ghostty:ghostty-1i4.3"},{"id":"external:ghostty:ghostty-1i4.4"}]'
    ;;
  "show ghostty-1i4.3 --json")
    echo '[{"id":"ghostty-1i4.3","title":"Task 1","status":"open","issue_type":"task"}]'
    ;;
  "show ghostty-1i4.4 --json")
    echo '[{"id":"ghostty-1i4.4","title":"Task 2","status":"open","issue_type":"task"}]'
    ;;
  show\ *--json)
    # Batch show fallback - return details for any known IDs
    echo '[{"id":"ghostty-1i4.3","title":"Task 1","status":"open","issue_type":"task"},{"id":"ghostty-1i4.4","title":"Task 2","status":"open","issue_type":"task"}]'
    ;;
  "dep list ghostty-1i4.3 --json"|"dep list ghostty-1i4.4 --json")
    echo '[]'
    ;;
  *)
    echo "unexpected bd args: $*" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+origPath)
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir townRoot: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	beads, deps, err := collectConvoyBeads("hq-cv-ext")
	if err != nil {
		t.Fatalf("collectConvoyBeads: %v", err)
	}
	if len(beads) != 2 {
		t.Fatalf("expected 2 tracked beads, got %d", len(beads))
	}
	ids := []string{beads[0].ID, beads[1].ID}
	sort.Strings(ids)
	if ids[0] != "ghostty-1i4.3" || ids[1] != "ghostty-1i4.4" {
		t.Fatalf("unexpected bead IDs: %v", ids)
	}
	if len(deps) != 0 {
		t.Fatalf("expected no deps for tracked beads, got %d", len(deps))
	}
}
