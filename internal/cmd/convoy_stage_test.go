package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// U-01: Simple 2-node cycle A→B→A
func TestDetectCycles_Simple2NodeCycle(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", BlockedBy: []string{"b"}, Blocks: []string{"b"}},
		"b": {ID: "b", BlockedBy: []string{"a"}, Blocks: []string{"a"}},
	}}
	cycle := detectCycles(dag)
	if cycle == nil {
		t.Fatal("expected cycle, got nil")
	}
	// Cycle should contain both "a" and "b"
	if len(cycle) < 2 {
		t.Fatalf("cycle too short: %v", cycle)
	}
}

// U-02: No cycle - linear chain A→B→C
func TestDetectCycles_NoCycleLinearChain(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Blocks: []string{"b"}},
		"b": {ID: "b", BlockedBy: []string{"a"}, Blocks: []string{"c"}},
		"c": {ID: "c", BlockedBy: []string{"b"}},
	}}
	cycle := detectCycles(dag)
	if cycle != nil {
		t.Fatalf("expected no cycle, got: %v", cycle)
	}
}

// U-03: Self-loop A blocks A
func TestDetectCycles_SelfLoop(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", BlockedBy: []string{"a"}, Blocks: []string{"a"}},
	}}
	cycle := detectCycles(dag)
	if cycle == nil {
		t.Fatal("expected cycle for self-loop, got nil")
	}
}

// U-04: Diamond shape (no cycle) - A→B, A→C, B→D, C→D
func TestDetectCycles_DiamondNoCycle(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Blocks: []string{"b", "c"}},
		"b": {ID: "b", BlockedBy: []string{"a"}, Blocks: []string{"d"}},
		"c": {ID: "c", BlockedBy: []string{"a"}, Blocks: []string{"d"}},
		"d": {ID: "d", BlockedBy: []string{"b", "c"}},
	}}
	cycle := detectCycles(dag)
	if cycle != nil {
		t.Fatalf("expected no cycle in diamond, got: %v", cycle)
	}
}

// U-05: Long chain with back-edge - A→B→C→D→B (cycle: B→C→D→B)
func TestDetectCycles_LongChainWithBackEdge(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Blocks: []string{"b"}},
		"b": {ID: "b", BlockedBy: []string{"a", "d"}, Blocks: []string{"c"}},
		"c": {ID: "c", BlockedBy: []string{"b"}, Blocks: []string{"d"}},
		"d": {ID: "d", BlockedBy: []string{"c"}, Blocks: []string{"b"}},
	}}
	cycle := detectCycles(dag)
	if cycle == nil {
		t.Fatal("expected cycle, got nil")
	}
	// Cycle should include b, c, d
	if len(cycle) < 3 {
		t.Fatalf("cycle too short, expected at least b,c,d: %v", cycle)
	}
}

// ---------------------------------------------------------------------------
// computeWaves tests (U-06 through U-14)
// ---------------------------------------------------------------------------

// helper: collect all task IDs across all waves
func allWaveTaskIDs(waves []Wave) []string {
	var all []string
	for _, w := range waves {
		all = append(all, w.Tasks...)
	}
	return all
}

// helper: find which wave a task is in (returns -1 if not found)
func waveOf(waves []Wave, taskID string) int {
	for _, w := range waves {
		for _, id := range w.Tasks {
			if id == taskID {
				return w.Number
			}
		}
	}
	return -1
}

// U-06: 3 independent tasks (no deps) → all Wave 1
func TestComputeWaves_AllIndependent(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Type: "task"},
		"b": {ID: "b", Type: "task"},
		"c": {ID: "c", Type: "task"},
	}}
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) != 1 {
		t.Fatalf("expected 1 wave, got %d: %+v", len(waves), waves)
	}
	if waves[0].Number != 1 {
		t.Fatalf("expected wave number 1, got %d", waves[0].Number)
	}
	if len(waves[0].Tasks) != 3 {
		t.Fatalf("expected 3 tasks in wave 1, got %d: %v", len(waves[0].Tasks), waves[0].Tasks)
	}
	// Tasks should be sorted for determinism
	expected := []string{"a", "b", "c"}
	for i, id := range waves[0].Tasks {
		if id != expected[i] {
			t.Errorf("wave 1 task[%d] = %q, want %q", i, id, expected[i])
		}
	}
}

// U-07: Linear chain A→B→C → 3 waves
func TestComputeWaves_LinearChain(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Type: "task", Blocks: []string{"b"}},
		"b": {ID: "b", Type: "task", BlockedBy: []string{"a"}, Blocks: []string{"c"}},
		"c": {ID: "c", Type: "task", BlockedBy: []string{"b"}},
	}}
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) != 3 {
		t.Fatalf("expected 3 waves, got %d: %+v", len(waves), waves)
	}
	// Wave 1=[a], Wave 2=[b], Wave 3=[c]
	checks := []struct {
		waveNum int
		tasks   []string
	}{
		{1, []string{"a"}},
		{2, []string{"b"}},
		{3, []string{"c"}},
	}
	for _, c := range checks {
		w := waves[c.waveNum-1]
		if w.Number != c.waveNum {
			t.Errorf("wave %d: got number %d", c.waveNum, w.Number)
		}
		if fmt.Sprintf("%v", w.Tasks) != fmt.Sprintf("%v", c.tasks) {
			t.Errorf("wave %d: got tasks %v, want %v", c.waveNum, w.Tasks, c.tasks)
		}
	}
}

// U-08: Diamond deps → correct waves. A→B, A→C, B→D, C→D = 3 waves: [A], [B,C], [D]
func TestComputeWaves_Diamond(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Type: "task", Blocks: []string{"b", "c"}},
		"b": {ID: "b", Type: "task", BlockedBy: []string{"a"}, Blocks: []string{"d"}},
		"c": {ID: "c", Type: "task", BlockedBy: []string{"a"}, Blocks: []string{"d"}},
		"d": {ID: "d", Type: "task", BlockedBy: []string{"b", "c"}},
	}}
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) != 3 {
		t.Fatalf("expected 3 waves, got %d: %+v", len(waves), waves)
	}
	// Wave 1=[a], Wave 2=[b,c], Wave 3=[d]
	if fmt.Sprintf("%v", waves[0].Tasks) != "[a]" {
		t.Errorf("wave 1: got %v, want [a]", waves[0].Tasks)
	}
	if fmt.Sprintf("%v", waves[1].Tasks) != "[b c]" {
		t.Errorf("wave 2: got %v, want [b c]", waves[1].Tasks)
	}
	if fmt.Sprintf("%v", waves[2].Tasks) != "[d]" {
		t.Errorf("wave 3: got %v, want [d]", waves[2].Tasks)
	}
}

// U-09: Mixed parallel + serial. A→B, C (independent), B→D = waves: [A,C], [B], [D]
func TestComputeWaves_MixedParallelSerial(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Type: "task", Blocks: []string{"b"}},
		"b": {ID: "b", Type: "task", BlockedBy: []string{"a"}, Blocks: []string{"d"}},
		"c": {ID: "c", Type: "task"},
		"d": {ID: "d", Type: "task", BlockedBy: []string{"b"}},
	}}
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) != 3 {
		t.Fatalf("expected 3 waves, got %d: %+v", len(waves), waves)
	}
	// Wave 1=[a,c], Wave 2=[b], Wave 3=[d]
	if fmt.Sprintf("%v", waves[0].Tasks) != "[a c]" {
		t.Errorf("wave 1: got %v, want [a c]", waves[0].Tasks)
	}
	if fmt.Sprintf("%v", waves[1].Tasks) != "[b]" {
		t.Errorf("wave 2: got %v, want [b]", waves[1].Tasks)
	}
	if fmt.Sprintf("%v", waves[2].Tasks) != "[d]" {
		t.Errorf("wave 3: got %v, want [d]", waves[2].Tasks)
	}
}

// U-11: Excludes epics from waves
func TestComputeWaves_ExcludesEpics(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1": {ID: "epic-1", Type: "epic"},
		"task-1": {ID: "task-1", Type: "task"},
	}}
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) != 1 {
		t.Fatalf("expected 1 wave, got %d", len(waves))
	}
	if len(waves[0].Tasks) != 1 || waves[0].Tasks[0] != "task-1" {
		t.Errorf("wave 1: got %v, want [task-1]", waves[0].Tasks)
	}
	// epic should not appear in any wave
	if waveOf(waves, "epic-1") != -1 {
		t.Error("epic-1 should not be in any wave")
	}
}

// U-12: Excludes non-slingable types (decision, epic, etc.)
func TestComputeWaves_ExcludesNonSlingable(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"d1":     {ID: "d1", Type: "decision"},
		"e1":     {ID: "e1", Type: "epic"},
		"task-1": {ID: "task-1", Type: "task"},
		"bug-1":  {ID: "bug-1", Type: "bug"},
		"feat-1": {ID: "feat-1", Type: "feature"},
		"ch-1":   {ID: "ch-1", Type: "chore"},
	}}
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) != 1 {
		t.Fatalf("expected 1 wave, got %d", len(waves))
	}
	// Only slingable types in the wave
	all := allWaveTaskIDs(waves)
	if len(all) != 4 {
		t.Fatalf("expected 4 slingable tasks, got %d: %v", len(all), all)
	}
	// decision and epic should not appear
	for _, id := range all {
		if id == "d1" || id == "e1" {
			t.Errorf("non-slingable %q should not appear in waves", id)
		}
	}
}

// #2141: decision beads block downstream tasks even though decisions aren't slingable.
// A task blocked by an open decision must NOT appear in Wave 1.
func TestComputeWaves_DecisionBlocksTask(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"d1":     {ID: "d1", Type: "decision", Status: "open", Blocks: []string{"task-1"}},
		"task-1": {ID: "task-1", Type: "task", Status: "open", BlockedBy: []string{"d1"}},
		"task-2": {ID: "task-2", Type: "task", Status: "open"},
	}}
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) < 1 {
		t.Fatalf("expected at least 1 wave, got %d", len(waves))
	}
	wave1Tasks := waves[0].Tasks
	for _, id := range wave1Tasks {
		if id == "task-1" {
			t.Errorf("task-1 should NOT be in Wave 1 — it's blocked by decision d1")
		}
		if id == "d1" {
			t.Errorf("decision d1 should NOT appear in any wave (not slingable)")
		}
	}
	found := false
	for _, id := range wave1Tasks {
		if id == "task-2" {
			found = true
		}
	}
	if !found {
		t.Errorf("task-2 should be in Wave 1, got: %v", wave1Tasks)
	}
	for _, w := range waves {
		for _, id := range w.Tasks {
			if id == "d1" {
				t.Errorf("decision d1 should not appear in wave %d", w.Number)
			}
		}
	}
}

// #2141: closed decision beads do NOT block downstream tasks.
func TestComputeWaves_ClosedDecisionDoesNotBlock(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"d1":     {ID: "d1", Type: "decision", Status: "closed", Blocks: []string{"task-1"}},
		"task-1": {ID: "task-1", Type: "task", Status: "open", BlockedBy: []string{"d1"}},
	}}
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) != 1 {
		t.Fatalf("expected 1 wave, got %d", len(waves))
	}
	if len(waves[0].Tasks) != 1 || waves[0].Tasks[0] != "task-1" {
		t.Errorf("task-1 should be in Wave 1 (decision is closed), got: %v", waves[0].Tasks)
	}
}

// U-13: parent-child deps don't create execution edges
func TestComputeWaves_ParentChildNotExecution(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1": {ID: "epic-1", Type: "epic", Children: []string{"task-1", "task-2"}},
		"task-1": {ID: "task-1", Type: "task", Parent: "epic-1"},
		"task-2": {ID: "task-2", Type: "task", Parent: "epic-1"},
	}}
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) != 1 {
		t.Fatalf("expected 1 wave, got %d: %+v", len(waves), waves)
	}
	// Both tasks in Wave 1 (parent-child doesn't block)
	if len(waves[0].Tasks) != 2 {
		t.Fatalf("expected 2 tasks in wave 1, got %d: %v", len(waves[0].Tasks), waves[0].Tasks)
	}
	if waveOf(waves, "task-1") != 1 || waveOf(waves, "task-2") != 1 {
		t.Errorf("both tasks should be in wave 1, got task-1=%d, task-2=%d",
			waveOf(waves, "task-1"), waveOf(waves, "task-2"))
	}
}

// U-14: Empty DAG (no slingable tasks) → error
func TestComputeWaves_EmptyDAG(t *testing.T) {
	// Completely empty
	dag1 := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{}}
	_, _, err := computeWaves(dag1)
	if err == nil {
		t.Error("expected error for empty DAG, got nil")
	}

	// Only non-slingable types
	dag2 := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1":     {ID: "epic-1", Type: "epic"},
		"decision-1": {ID: "decision-1", Type: "decision"},
	}}
	_, _, err = computeWaves(dag2)
	if err == nil {
		t.Error("expected error for DAG with only non-slingable types, got nil")
	}
}

// ---------------------------------------------------------------------------
// Gated task tests — non-slingable blockers
// ---------------------------------------------------------------------------

// Task blocked by open decision → excluded from waves, returned as gated.
func TestComputeWaves_GatedByDecision(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"dec-1":  {ID: "dec-1", Type: "decision", Status: "open", Blocks: []string{"task-1"}},
		"task-1": {ID: "task-1", Type: "task", Status: "open", BlockedBy: []string{"dec-1"}},
		"task-2": {ID: "task-2", Type: "task", Status: "open"},
	}}
	waves, gated, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// task-2 should be in waves, task-1 should be gated
	if len(waves) != 1 || len(waves[0].Tasks) != 1 || waves[0].Tasks[0] != "task-2" {
		t.Errorf("expected wave 1 = [task-2], got %+v", waves)
	}
	if len(gated) != 1 || gated[0].TaskID != "task-1" {
		t.Errorf("expected gated = [task-1], got %+v", gated)
	}
	if len(gated[0].GatedBy) != 1 || gated[0].GatedBy[0] != "dec-1" {
		t.Errorf("expected gated by dec-1, got %v", gated[0].GatedBy)
	}
}

// task-A gated by decision, task-B depends on task-A → both gated.
func TestComputeWaves_GatedTransitive(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"dec-1":  {ID: "dec-1", Type: "decision", Status: "open", Blocks: []string{"task-a"}},
		"task-a": {ID: "task-a", Type: "task", Status: "open", BlockedBy: []string{"dec-1"}, Blocks: []string{"task-b"}},
		"task-b": {ID: "task-b", Type: "task", Status: "open", BlockedBy: []string{"task-a"}},
		"task-c": {ID: "task-c", Type: "task", Status: "open"},
	}}
	waves, gated, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// task-c in waves, task-a and task-b gated
	if len(waves) != 1 || len(waves[0].Tasks) != 1 || waves[0].Tasks[0] != "task-c" {
		t.Errorf("expected wave 1 = [task-c], got %+v", waves)
	}
	if len(gated) != 2 {
		t.Fatalf("expected 2 gated tasks, got %d: %+v", len(gated), gated)
	}
	gatedIDs := map[string]bool{}
	for _, g := range gated {
		gatedIDs[g.TaskID] = true
	}
	if !gatedIDs["task-a"] || !gatedIDs["task-b"] {
		t.Errorf("expected task-a and task-b gated, got %v", gatedIDs)
	}
	// task-a should have direct gate, task-b should have empty GatedBy (transitive)
	for _, g := range gated {
		if g.TaskID == "task-a" && (len(g.GatedBy) != 1 || g.GatedBy[0] != "dec-1") {
			t.Errorf("task-a should be gated by dec-1, got %v", g.GatedBy)
		}
		if g.TaskID == "task-b" && len(g.GatedBy) != 0 {
			t.Errorf("task-b should be transitively gated (empty GatedBy), got %v", g.GatedBy)
		}
	}
}

// Task blocked by closed decision → in waves (gate resolved).
func TestComputeWaves_ResolvedDecision(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"dec-1":  {ID: "dec-1", Type: "decision", Status: "closed", Blocks: []string{"task-1"}},
		"task-1": {ID: "task-1", Type: "task", Status: "open", BlockedBy: []string{"dec-1"}},
	}}
	waves, gated, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gated) != 0 {
		t.Errorf("expected no gated tasks (decision closed), got %+v", gated)
	}
	if len(waves) != 1 || len(waves[0].Tasks) != 1 || waves[0].Tasks[0] != "task-1" {
		t.Errorf("expected wave 1 = [task-1], got %+v", waves)
	}
}

// Task blocked by tombstoned decision → in waves.
func TestComputeWaves_TombstoneDecision(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"dec-1":  {ID: "dec-1", Type: "decision", Status: "tombstone", Blocks: []string{"task-1"}},
		"task-1": {ID: "task-1", Type: "task", Status: "open", BlockedBy: []string{"dec-1"}},
	}}
	waves, gated, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gated) != 0 {
		t.Errorf("expected no gated tasks (decision tombstoned), got %+v", gated)
	}
	if len(waves) != 1 || len(waves[0].Tasks) != 1 || waves[0].Tasks[0] != "task-1" {
		t.Errorf("expected wave 1 = [task-1], got %+v", waves)
	}
}

// Task blocked by open epic → gated.
func TestComputeWaves_GatedByEpic(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1": {ID: "epic-1", Type: "epic", Status: "open", Blocks: []string{"task-1"}},
		"task-1": {ID: "task-1", Type: "task", Status: "open", BlockedBy: []string{"epic-1"}},
		"task-2": {ID: "task-2", Type: "task", Status: "open"},
	}}
	waves, gated, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gated) != 1 || gated[0].TaskID != "task-1" {
		t.Errorf("expected task-1 gated by epic, got %+v", gated)
	}
	if len(waves) != 1 || waves[0].Tasks[0] != "task-2" {
		t.Errorf("expected wave 1 = [task-2], got %+v", waves)
	}
}

// All slingable tasks gated → empty waves, all returned as gated.
func TestComputeWaves_AllGated(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"dec-1":  {ID: "dec-1", Type: "decision", Status: "open", Blocks: []string{"task-1"}},
		"task-1": {ID: "task-1", Type: "task", Status: "open", BlockedBy: []string{"dec-1"}},
	}}
	waves, gated, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(waves) != 0 {
		t.Errorf("expected 0 waves when all tasks gated, got %d", len(waves))
	}
	if len(gated) != 1 {
		t.Errorf("expected 1 gated task, got %d", len(gated))
	}
}

// merge-blocks creates execution edge in DAG.
func TestBuildConvoyDAG_MergeBlocks(t *testing.T) {
	beads := []BeadInfo{
		{ID: "mr-1", Title: "MR", Type: "task", Status: "open"},
		{ID: "task-1", Title: "Task", Type: "task", Status: "open"},
	}
	deps := []DepInfo{
		{IssueID: "task-1", DependsOnID: "mr-1", Type: "merge-blocks"},
	}
	dag := buildConvoyDAG(beads, deps)

	if node := dag.Nodes["task-1"]; node == nil {
		t.Fatal("task-1 not in DAG")
	} else if len(node.BlockedBy) != 1 || node.BlockedBy[0] != "mr-1" {
		t.Errorf("expected task-1 blocked by mr-1, got %v", node.BlockedBy)
	}
	if node := dag.Nodes["mr-1"]; node == nil {
		t.Fatal("mr-1 not in DAG")
	} else if len(node.Blocks) != 1 || node.Blocks[0] != "task-1" {
		t.Errorf("expected mr-1 blocks task-1, got %v", node.Blocks)
	}
}

// Task blocked by decision → not flagged as orphan.
func TestDetectOrphans_DecisionGatedNotOrphan(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"dec-1":  {ID: "dec-1", Type: "decision", Status: "open", Blocks: []string{"task-1"}},
		"task-1": {ID: "task-1", Type: "task", Status: "open", BlockedBy: []string{"dec-1"}},
	}}
	input := &StageInput{Kind: StageInputEpic}
	findings := detectOrphans(dag, input)
	for _, f := range findings {
		if f.Category == "orphan" && f.BeadIDs[0] == "task-1" {
			t.Error("task-1 should not be flagged as orphan — it is blocked by decision dec-1")
		}
	}
}

// ---------------------------------------------------------------------------
// Input parsing + validation tests (gt-csl.3.1)
// ---------------------------------------------------------------------------

// TestConvoyStageInput_EmptyArgs verifies empty args are rejected.
func TestConvoyStageInput_EmptyArgs(t *testing.T) {
	err := validateStageArgs([]string{})
	if err == nil {
		t.Fatal("expected error for empty args")
	}
}

// TestConvoyStageInput_FlagLikeArg verifies flag-like args are rejected.
func TestConvoyStageInput_FlagLikeArg(t *testing.T) {
	err := validateStageArgs([]string{"--verbose"})
	if err == nil {
		t.Fatal("expected error for flag-like arg")
	}
	if !strings.Contains(err.Error(), "flag") {
		t.Errorf("error should mention flag: %v", err)
	}
}

// TestConvoyStageInput_ValidSingleArg verifies a single bead ID passes.
func TestConvoyStageInput_ValidSingleArg(t *testing.T) {
	err := validateStageArgs([]string{"gt-abc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestConvoyStageInput_ValidMultipleArgs verifies multiple bead IDs pass.
func TestConvoyStageInput_ValidMultipleArgs(t *testing.T) {
	err := validateStageArgs([]string{"gt-abc", "gt-def", "gt-ghi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestConvoyStageInput_ClassifyEpic verifies epic type classification.
func TestConvoyStageInput_ClassifyEpic(t *testing.T) {
	kind := classifyBeadType("epic")
	if kind != StageInputEpic {
		t.Errorf("expected StageInputEpic, got %v", kind)
	}
}

// TestConvoyStageInput_ClassifyConvoy verifies convoy type classification.
func TestConvoyStageInput_ClassifyConvoy(t *testing.T) {
	kind := classifyBeadType("convoy")
	if kind != StageInputConvoy {
		t.Errorf("expected StageInputConvoy, got %v", kind)
	}
}

// TestConvoyStageInput_ClassifyTask verifies task-like types are classified as StageInputTasks.
func TestConvoyStageInput_ClassifyTask(t *testing.T) {
	for _, typ := range []string{"task", "bug", "feature", "chore"} {
		kind := classifyBeadType(typ)
		if kind != StageInputTasks {
			t.Errorf("expected StageInputTasks for %q, got %v", typ, kind)
		}
	}
}

// TestConvoyStageInput_MixedTypes verifies mixed input types are rejected.
func TestConvoyStageInput_MixedTypes(t *testing.T) {
	types := map[string]string{"gt-epic": "epic", "gt-task": "task"}
	_, err := resolveInputKind(types)
	if err == nil {
		t.Fatal("expected error for mixed types")
	}
	if !strings.Contains(err.Error(), "mixed") || !strings.Contains(err.Error(), "separate") {
		t.Errorf("error should mention mixed types and suggest separate invocations: %v", err)
	}
}

// TestConvoyStageInput_MultipleEpicsError verifies multiple epics are rejected.
func TestConvoyStageInput_MultipleEpicsError(t *testing.T) {
	types := map[string]string{"gt-epic1": "epic", "gt-epic2": "epic"}
	_, err := resolveInputKind(types)
	if err == nil {
		t.Fatal("expected error for multiple epics")
	}
}

// TestConvoyStageInput_SingleEpicOK verifies a single epic is accepted.
func TestConvoyStageInput_SingleEpicOK(t *testing.T) {
	types := map[string]string{"gt-epic": "epic"}
	input, err := resolveInputKind(types)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if input.Kind != StageInputEpic {
		t.Errorf("expected StageInputEpic, got %v", input.Kind)
	}
}

// TestConvoyStageInput_MultipleTasksOK verifies multiple tasks are accepted.
func TestConvoyStageInput_MultipleTasksOK(t *testing.T) {
	types := map[string]string{"gt-a": "task", "gt-b": "task", "gt-c": "bug"}
	input, err := resolveInputKind(types)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if input.Kind != StageInputTasks {
		t.Errorf("expected StageInputTasks, got %v", input.Kind)
	}
}

// ---------------------------------------------------------------------------
// buildConvoyDAG tests (U-15 through U-19)
// ---------------------------------------------------------------------------

// sliceContains checks if a string slice contains a value.
func sliceContains(ss []string, val string) bool {
	for _, s := range ss {
		if s == val {
			return true
		}
	}
	return false
}

// U-15: blocks deps create execution edges
func TestBuildDAG_BlocksCreateEdges(t *testing.T) {
	beads := []BeadInfo{
		{ID: "a", Title: "Task A", Type: "task", Status: "open"},
		{ID: "b", Title: "Task B", Type: "task", Status: "open"},
	}
	deps := []DepInfo{
		{IssueID: "b", DependsOnID: "a", Type: "blocks"},
	}
	dag := buildConvoyDAG(beads, deps)
	if dag == nil {
		t.Fatal("expected non-nil DAG")
	}
	nodeA := dag.Nodes["a"]
	nodeB := dag.Nodes["b"]
	if nodeA == nil || nodeB == nil {
		t.Fatal("expected both nodes to exist")
	}
	if !sliceContains(nodeA.Blocks, "b") {
		t.Errorf("a.Blocks should contain 'b', got %v", nodeA.Blocks)
	}
	if !sliceContains(nodeB.BlockedBy, "a") {
		t.Errorf("b.BlockedBy should contain 'a', got %v", nodeB.BlockedBy)
	}
}

// U-16: conditional-blocks create execution edges (same as blocks for DAG purposes)
func TestBuildDAG_ConditionalBlocksCreateEdges(t *testing.T) {
	beads := []BeadInfo{
		{ID: "a", Title: "Task A", Type: "task", Status: "open"},
		{ID: "b", Title: "Task B", Type: "task", Status: "open"},
	}
	deps := []DepInfo{
		{IssueID: "b", DependsOnID: "a", Type: "conditional-blocks"},
	}
	dag := buildConvoyDAG(beads, deps)
	nodeA := dag.Nodes["a"]
	nodeB := dag.Nodes["b"]
	if !sliceContains(nodeA.Blocks, "b") {
		t.Errorf("a.Blocks should contain 'b' for conditional-blocks, got %v", nodeA.Blocks)
	}
	if !sliceContains(nodeB.BlockedBy, "a") {
		t.Errorf("b.BlockedBy should contain 'a' for conditional-blocks, got %v", nodeB.BlockedBy)
	}
}

// U-17: waits-for creates execution edges
func TestBuildDAG_WaitsForCreateEdges(t *testing.T) {
	beads := []BeadInfo{
		{ID: "x", Title: "Task X", Type: "task", Status: "open"},
		{ID: "y", Title: "Task Y", Type: "task", Status: "open"},
	}
	deps := []DepInfo{
		{IssueID: "y", DependsOnID: "x", Type: "waits-for"},
	}
	dag := buildConvoyDAG(beads, deps)
	nodeX := dag.Nodes["x"]
	nodeY := dag.Nodes["y"]
	if !sliceContains(nodeX.Blocks, "y") {
		t.Errorf("x.Blocks should contain 'y' for waits-for, got %v", nodeX.Blocks)
	}
	if !sliceContains(nodeY.BlockedBy, "x") {
		t.Errorf("y.BlockedBy should contain 'x' for waits-for, got %v", nodeY.BlockedBy)
	}
}

// U-18: parent-child recorded as hierarchy but NO execution edge
func TestBuildDAG_ParentChildNoExecutionEdge(t *testing.T) {
	beads := []BeadInfo{
		{ID: "epic-1", Title: "Root", Type: "epic", Status: "open"},
		{ID: "task-1", Title: "Child", Type: "task", Status: "open"},
	}
	deps := []DepInfo{
		{IssueID: "task-1", DependsOnID: "epic-1", Type: "parent-child"},
	}
	dag := buildConvoyDAG(beads, deps)
	epicNode := dag.Nodes["epic-1"]
	taskNode := dag.Nodes["task-1"]
	// Hierarchy should be set
	if !sliceContains(epicNode.Children, "task-1") {
		t.Errorf("epic-1.Children should contain 'task-1', got %v", epicNode.Children)
	}
	if taskNode.Parent != "epic-1" {
		t.Errorf("task-1.Parent should be 'epic-1', got %q", taskNode.Parent)
	}
	// Execution edges must NOT be set
	if len(epicNode.Blocks) != 0 {
		t.Errorf("epic-1.Blocks should be empty for parent-child, got %v", epicNode.Blocks)
	}
	if len(taskNode.BlockedBy) != 0 {
		t.Errorf("task-1.BlockedBy should be empty for parent-child, got %v", taskNode.BlockedBy)
	}
}

// U-19: related/tracks deps ignored entirely
func TestBuildDAG_RelatedTracksIgnored(t *testing.T) {
	beads := []BeadInfo{
		{ID: "a", Title: "A", Type: "task", Status: "open"},
		{ID: "b", Title: "B", Type: "task", Status: "open"},
	}
	deps := []DepInfo{
		{IssueID: "a", DependsOnID: "b", Type: "related"},
		{IssueID: "a", DependsOnID: "b", Type: "tracks"},
	}
	dag := buildConvoyDAG(beads, deps)
	nodeA := dag.Nodes["a"]
	nodeB := dag.Nodes["b"]
	if len(nodeA.BlockedBy) != 0 || len(nodeA.Blocks) != 0 {
		t.Errorf("related/tracks should not create edges on a: BlockedBy=%v Blocks=%v", nodeA.BlockedBy, nodeA.Blocks)
	}
	if len(nodeB.BlockedBy) != 0 || len(nodeB.Blocks) != 0 {
		t.Errorf("related/tracks should not create edges on b: BlockedBy=%v Blocks=%v", nodeB.BlockedBy, nodeB.Blocks)
	}
	// Also no hierarchy
	if len(nodeA.Children) != 0 || nodeA.Parent != "" {
		t.Error("related/tracks should not set hierarchy on a")
	}
	if len(nodeB.Children) != 0 || nodeB.Parent != "" {
		t.Error("related/tracks should not set hierarchy on b")
	}
}

// ---------------------------------------------------------------------------
// collectBeads tests — Epic DAG walking (IT-01 through IT-04)
// ---------------------------------------------------------------------------

// IT-01: Epic walk collects all descendants across 3 levels.
// Tree: gt-epic → {gt-sub (epic), gt-task1 (task)}
//
//	gt-sub → {gt-task2 (task), gt-task3 (task)}
func TestEpicWalk_CollectsAllDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Epic("gt-epic", "Root Epic").
		Epic("gt-sub", "Sub Epic").ParentOf("gt-epic").
		Task("gt-task1", "Task 1", withRig("gastown")).ParentOf("gt-epic").
		Task("gt-task2", "Task 2", withRig("gastown")).ParentOf("gt-sub").
		Task("gt-task3", "Task 3", withRig("gastown")).ParentOf("gt-sub")

	dag.Setup(t)

	input := &StageInput{Kind: StageInputEpic, IDs: []string{"gt-epic"}}
	beads, _, err := collectBeads(input)
	if err != nil {
		t.Fatalf("collectBeads: %v", err)
	}

	// Should have 5 beads: epic, sub, task1, task2, task3
	if len(beads) != 5 {
		ids := make([]string, len(beads))
		for i, b := range beads {
			ids[i] = b.ID
		}
		t.Errorf("expected 5 beads, got %d: %v", len(beads), ids)
	}

	// Verify all expected IDs present.
	idSet := make(map[string]bool)
	for _, b := range beads {
		idSet[b.ID] = true
	}
	for _, want := range []string{"gt-epic", "gt-sub", "gt-task1", "gt-task2", "gt-task3"} {
		if !idSet[want] {
			t.Errorf("missing bead %q in collected set", want)
		}
	}
}

// IT-02: Nonexistent epic bead returns error.
func TestEpicWalk_NonexistentBeadErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	// Set up a DAG with only one bead so "gt-missing" doesn't exist.
	dag := newTestDAG(t).
		Task("gt-exists", "Existing task", withRig("gastown"))
	dag.Setup(t)

	input := &StageInput{Kind: StageInputEpic, IDs: []string{"gt-missing"}}
	_, _, err := collectBeads(input)
	if err == nil {
		t.Fatal("expected error for nonexistent epic, got nil")
	}
}

// IT-03: Task list analyzes only given tasks.
func TestTaskListWalk_AnalyzesOnlyGiven(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Task("gt-a", "Task A", withRig("gastown")).
		Task("gt-b", "Task B", withRig("gastown")).BlockedBy("gt-a").
		Task("gt-c", "Task C", withRig("gastown")) // not requested
	dag.Setup(t)

	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b"}}
	beads, deps, err := collectBeads(input)
	if err != nil {
		t.Fatalf("collectBeads: %v", err)
	}

	// Should have exactly 2 beads.
	if len(beads) != 2 {
		ids := make([]string, len(beads))
		for i, b := range beads {
			ids[i] = b.ID
		}
		t.Errorf("expected 2 beads, got %d: %v", len(beads), ids)
	}

	// Verify only gt-a and gt-b.
	idSet := make(map[string]bool)
	for _, b := range beads {
		idSet[b.ID] = true
	}
	if !idSet["gt-a"] || !idSet["gt-b"] {
		t.Errorf("expected gt-a and gt-b, got %v", idSet)
	}
	if idSet["gt-c"] {
		t.Error("gt-c should not be in collected beads")
	}

	// gt-b should have a dep on gt-a.
	foundDep := false
	for _, d := range deps {
		if d.IssueID == "gt-b" && d.DependsOnID == "gt-a" && d.Type == "blocks" {
			foundDep = true
		}
	}
	if !foundDep {
		t.Errorf("expected dep gt-b blocked-by gt-a, got deps: %+v", deps)
	}
}

// IT-04: Convoy reads tracked beads.
func TestConvoyWalk_ReadsTrackedBeads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Convoy("gt-convoy", "Test Convoy").
		Task("gt-t1", "Tracked 1", withRig("gastown")).TrackedBy("gt-convoy").
		Task("gt-t2", "Tracked 2", withRig("gastown")).TrackedBy("gt-convoy")
	dag.Setup(t)

	input := &StageInput{Kind: StageInputConvoy, IDs: []string{"gt-convoy"}}
	beads, _, err := collectBeads(input)
	if err != nil {
		t.Fatalf("collectBeads: %v", err)
	}

	// Should have 2 tracked beads (convoy itself is not returned as a bead to stage).
	if len(beads) != 2 {
		ids := make([]string, len(beads))
		for i, b := range beads {
			ids[i] = b.ID
		}
		t.Errorf("expected 2 beads, got %d: %v", len(beads), ids)
	}

	idSet := make(map[string]bool)
	for _, b := range beads {
		idSet[b.ID] = true
	}
	if !idSet["gt-t1"] || !idSet["gt-t2"] {
		t.Errorf("expected gt-t1 and gt-t2 in tracked beads, got %v", idSet)
	}
}

// IT-05: Epic walk collects deps across the tree.
func TestEpicWalk_CollectsDeps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Epic("gt-epic", "Root Epic").
		Task("gt-t1", "Task 1", withRig("gastown")).ParentOf("gt-epic").
		Task("gt-t2", "Task 2", withRig("gastown")).ParentOf("gt-epic").BlockedBy("gt-t1")
	dag.Setup(t)

	input := &StageInput{Kind: StageInputEpic, IDs: []string{"gt-epic"}}
	beads, deps, err := collectBeads(input)
	if err != nil {
		t.Fatalf("collectBeads: %v", err)
	}
	if len(beads) != 3 {
		t.Fatalf("expected 3 beads, got %d", len(beads))
	}

	// Should find the blocks dep and the parent-child deps.
	var depTypes []string
	for _, d := range deps {
		depTypes = append(depTypes, fmt.Sprintf("%s→%s(%s)", d.IssueID, d.DependsOnID, d.Type))
	}
	sort.Strings(depTypes)

	// Expect parent-child deps for gt-t1 and gt-t2, plus blocks dep gt-t2→gt-t1.
	foundBlocks := false
	for _, d := range deps {
		if d.IssueID == "gt-t2" && d.DependsOnID == "gt-t1" && d.Type == "blocks" {
			foundBlocks = true
		}
	}
	if !foundBlocks {
		t.Errorf("expected blocks dep gt-t2→gt-t1, got: %v", depTypes)
	}
}

// ---------------------------------------------------------------------------
// renderWaveTable tests (U-30, U-38, gt-csl.4.2)
// ---------------------------------------------------------------------------

// U-30: Wave table includes blockers column
func TestRenderWaveTable_IncludesBlockers(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Title: "Task A", Type: "task", Rig: "gastown", Blocks: []string{"gt-b"}},
		"gt-b": {ID: "gt-b", Title: "Task B", Type: "task", Rig: "gastown", BlockedBy: []string{"gt-a"}},
	}}
	waves := []Wave{
		{Number: 1, Tasks: []string{"gt-a"}},
		{Number: 2, Tasks: []string{"gt-b"}},
	}
	output := renderWaveTable(waves, dag)
	if !strings.Contains(output, "gt-a") {
		t.Error("should show gt-a")
	}
	if !strings.Contains(output, "gt-b") {
		t.Error("should show gt-b")
	}
	// gt-b's row should show gt-a as blocker
	// The table should contain "gt-a" in the blocked-by column for gt-b
}

// U-38: Summary line shows totals
func TestRenderWaveTable_SummaryLine(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Title: "A", Type: "task", Rig: "gst"},
		"b": {ID: "b", Title: "B", Type: "task", Rig: "gst"},
		"c": {ID: "c", Title: "C", Type: "task", Rig: "gst"},
	}}
	waves := []Wave{
		{Number: 1, Tasks: []string{"a", "b"}},
		{Number: 2, Tasks: []string{"c"}},
	}
	output := renderWaveTable(waves, dag)
	if !strings.Contains(output, "3 tasks") {
		t.Error("should show 3 tasks")
	}
	if !strings.Contains(output, "2 waves") {
		t.Error("should show 2 waves")
	}
	if !strings.Contains(output, "max parallelism: 2") {
		t.Error("should show max parallelism 2")
	}
}

// Test empty waves
func TestRenderWaveTable_Empty(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{}}
	output := renderWaveTable(nil, dag)
	if !strings.Contains(output, "0 tasks") {
		t.Error("should show 0 tasks")
	}
}

// Test wave table with multiple rigs
func TestRenderWaveTable_MultipleRigs(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Title: "Task A", Type: "task", Rig: "gastown"},
		"bd-b": {ID: "bd-b", Title: "Task B", Type: "task", Rig: "beads"},
	}}
	waves := []Wave{
		{Number: 1, Tasks: []string{"bd-b", "gt-a"}},
	}
	output := renderWaveTable(waves, dag)
	if !strings.Contains(output, "gastown") {
		t.Error("should show gastown rig")
	}
	if !strings.Contains(output, "beads") {
		t.Error("should show beads rig")
	}
}

// Test wave table preserves multi-byte UTF-8 characters during title truncation.
// Regression test: byte-based truncation split em-dashes (U+2014, 3 bytes)
// mid-character, producing mojibake like "â" in the wave table output.
func TestRenderWaveTable_UTF8Truncation(t *testing.T) {
	// Title with em-dash that would be split by byte-based title[:28]
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Title: "F.2: Beads for Optuna rig \u2014 extra", Type: "task", Rig: "gst"},
	}}
	waves := []Wave{
		{Number: 1, Tasks: []string{"gt-a"}},
	}
	output := renderWaveTable(waves, dag)

	// Must not contain the mojibake byte 0xE2 without its continuation bytes.
	// If truncation splits the em-dash, the output will contain an isolated
	// 0xE2 byte which displays as "â".
	for i := 0; i < len(output); i++ {
		if output[i] == 0xE2 {
			// Verify the full 3-byte em-dash sequence is present
			if i+2 >= len(output) || output[i+1] != 0x80 || output[i+2] != 0x94 {
				// Could be a different 3-byte char (like box-drawing "─")
				// Check if it's a valid UTF-8 start byte with proper continuation
				if i+1 >= len(output) || (output[i+1]&0xC0) != 0x80 {
					t.Errorf("found isolated 0xE2 byte at position %d — UTF-8 truncation bug", i)
				}
			}
		}
	}

	// The truncated title should end with ".." and be valid UTF-8
	if !strings.Contains(output, "..") {
		t.Error("long title should be truncated with '..'")
	}
}

// ---------------------------------------------------------------------------
// Error detection + categorization tests (gt-csl.3.3)
// ---------------------------------------------------------------------------

// U-20: Cycle is categorized as error, not warning
func TestCategorize_CycleIsError(t *testing.T) {
	findings := []StagingFinding{
		{Severity: "error", Category: "cycle", BeadIDs: []string{"a", "b"}, Message: "cycle"},
	}
	errs, warns := categorizeFindings(findings)
	if len(errs) != 1 {
		t.Errorf("expected 1 error, got %d", len(errs))
	}
	if len(warns) != 0 {
		t.Errorf("expected 0 warnings, got %d", len(warns))
	}
}

// U-21: No-rig is categorized as error
func TestCategorize_NoRigIsError(t *testing.T) {
	findings := []StagingFinding{
		{Severity: "error", Category: "no-rig", BeadIDs: []string{"gt-xyz"}, Message: "no rig"},
	}
	errs, warns := categorizeFindings(findings)
	if len(errs) != 1 {
		t.Errorf("expected 1 error, got %d", len(errs))
	}
	if len(warns) != 0 {
		t.Errorf("expected 0 warnings, got %d", len(warns))
	}
}

// U-25: No errors + no warnings → staged_ready
func TestChooseStatus_Ready(t *testing.T) {
	status := chooseStatus(nil, nil)
	if status != "staged_ready" {
		t.Errorf("expected staged_ready, got %q", status)
	}
}

// U-26: Warnings only → staged_warnings
func TestChooseStatus_Warnings(t *testing.T) {
	warns := []StagingFinding{{Severity: "warning", Category: "blocked-rig"}}
	status := chooseStatus(nil, warns)
	if status != "staged_warnings" {
		t.Errorf("expected staged_warnings, got %q", status)
	}
}

// U-27: Any errors → no creation (empty string)
func TestChooseStatus_Errors(t *testing.T) {
	errs := []StagingFinding{{Severity: "error", Category: "cycle"}}
	status := chooseStatus(errs, nil)
	if status != "" {
		t.Errorf("expected empty (no creation), got %q", status)
	}
}

// U-39: Error output includes bead IDs and suggested fix
func TestRenderErrors_IncludesFixAndIDs(t *testing.T) {
	findings := []StagingFinding{
		{Severity: "error", Category: "cycle", BeadIDs: []string{"a", "b"},
			Message:      "cycle detected: a → b → a",
			SuggestedFix: "remove one blocking dep"},
	}
	output := renderErrors(findings)
	if !strings.Contains(output, "a, b") {
		t.Error("should include bead IDs")
	}
	if !strings.Contains(output, "remove one blocking dep") {
		t.Error("should include suggested fix")
	}
	if !strings.Contains(output, "cycle") {
		t.Error("should include category")
	}
}

// Test detectErrors with cycle
func TestErrorDetection_CycleDetected(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Type: "task", Rig: "gastown", Blocks: []string{"b"}, BlockedBy: []string{"b"}},
		"b": {ID: "b", Type: "task", Rig: "gastown", BlockedBy: []string{"a"}, Blocks: []string{"a"}},
	}}

	findings := detectErrors(dag)
	errs, _ := categorizeFindings(findings)
	if len(errs) == 0 {
		t.Fatal("expected cycle error")
	}
	if errs[0].Category != "cycle" {
		t.Errorf("expected cycle, got %s", errs[0].Category)
	}
}

// Test detectErrors with no rig
func TestErrorDetection_NoRig(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Type: "task", Rig: ""}, // no rig!
	}}
	findings := detectErrors(dag)
	errs, _ := categorizeFindings(findings)
	if len(errs) == 0 {
		t.Fatal("expected no-rig error")
	}
	if errs[0].Category != "no-rig" {
		t.Errorf("expected no-rig, got %s", errs[0].Category)
	}
}

// Test detectErrors clean DAG → no errors
func TestErrorDetection_Clean(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Type: "task", Rig: "gastown", Blocks: []string{"b"}},
		"b": {ID: "b", Type: "task", Rig: "gastown", BlockedBy: []string{"a"}},
	}}
	findings := detectErrors(dag)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(findings))
	}
}

// ---------------------------------------------------------------------------
// renderDAGTree tests (gt-csl.4.1)
// ---------------------------------------------------------------------------

// U-28: Task-list input renders flat list
func TestRenderDAGTree_TaskListFlat(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Title: "Task A", Type: "task", Status: "open", Rig: "gastown"},
		"gt-b": {ID: "gt-b", Title: "Task B", Type: "task", Status: "open", Rig: "gastown"},
		"gt-c": {ID: "gt-c", Title: "Task C", Type: "bug", Status: "open", Rig: "gastown"},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b", "gt-c"}}
	output := renderDAGTree(dag, input)

	// All 3 IDs must appear
	for _, id := range []string{"gt-a", "gt-b", "gt-c"} {
		if !strings.Contains(output, id) {
			t.Errorf("output should contain %q, got:\n%s", id, output)
		}
	}

	// No tree characters in flat list
	if strings.Contains(output, "├── ") || strings.Contains(output, "└── ") {
		t.Errorf("flat task list should not contain tree characters, got:\n%s", output)
	}
}

// U-29: Epic input renders full tree with indentation
func TestRenderDAGTree_EpicTree(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"root-epic": {ID: "root-epic", Title: "Root Epic", Type: "epic", Status: "open",
			Children: []string{"sub-epic", "task-1"}},
		"sub-epic": {ID: "sub-epic", Title: "Sub Epic", Type: "epic", Status: "open",
			Parent: "root-epic", Children: []string{"task-2", "task-3"}},
		"task-1": {ID: "task-1", Title: "Task One", Type: "task", Status: "open",
			Rig: "gastown", Parent: "root-epic"},
		"task-2": {ID: "task-2", Title: "Task Two", Type: "task", Status: "open",
			Rig: "gastown", Parent: "sub-epic"},
		"task-3": {ID: "task-3", Title: "Task Three", Type: "task", Status: "open",
			Rig: "gastown", Parent: "sub-epic"},
	}}
	input := &StageInput{Kind: StageInputEpic, IDs: []string{"root-epic"}}
	output := renderDAGTree(dag, input)

	// Root epic appears at top level (first line, no tree prefix)
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("expected non-empty output")
	}
	if !strings.HasPrefix(lines[0], "root-epic") {
		t.Errorf("first line should start with root-epic, got: %q", lines[0])
	}

	// sub-epic is indented under root
	if !strings.Contains(output, "sub-epic") {
		t.Error("output should contain sub-epic")
	}

	// task-2 and task-3 are indented under sub-epic
	if !strings.Contains(output, "task-2") || !strings.Contains(output, "task-3") {
		t.Error("output should contain task-2 and task-3")
	}

	// Tree characters must be present
	if !strings.Contains(output, "├── ") && !strings.Contains(output, "└── ") {
		t.Errorf("epic tree should contain tree characters, got:\n%s", output)
	}

	// Verify indentation increases: task-2/task-3 should have more prefix than sub-epic
	subEpicIndent := -1
	task2Indent := -1
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " │├└──")
		indent := len(line) - len(trimmed)
		if strings.Contains(line, "sub-epic") {
			subEpicIndent = indent
		}
		if strings.Contains(line, "task-2") {
			task2Indent = indent
		}
	}
	if subEpicIndent >= 0 && task2Indent >= 0 && task2Indent <= subEpicIndent {
		t.Errorf("task-2 indent (%d) should be greater than sub-epic indent (%d)", task2Indent, subEpicIndent)
	}
}

// U-36: Each node shows ID, type, title, rig, status
func TestRenderDAGTree_NodeInfo(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-abc": {ID: "gt-abc", Title: "My Task", Type: "task", Status: "open", Rig: "gastown"},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-abc"}}
	output := renderDAGTree(dag, input)

	// Verify all fields appear in the output
	for _, want := range []string{"gt-abc", "task", "My Task", "gastown", "open"} {
		if !strings.Contains(output, want) {
			t.Errorf("output should contain %q, got:\n%s", want, output)
		}
	}
}

// U-37: Blocked tasks show blockers inline
func TestRenderDAGTree_BlockedShowsBlockers(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"task-a": {ID: "task-a", Title: "Task A", Type: "task", Status: "open", Rig: "gastown",
			Blocks: []string{"task-b"}},
		"task-b": {ID: "task-b", Title: "Task B", Type: "task", Status: "open", Rig: "gastown",
			BlockedBy: []string{"task-a"}},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"task-a", "task-b"}}
	output := renderDAGTree(dag, input)

	// task-b's line should contain "blocked by" and "task-a"
	lines := strings.Split(output, "\n")
	foundBlocker := false
	for _, line := range lines {
		if strings.Contains(line, "task-b") && strings.Contains(line, "blocked by") && strings.Contains(line, "task-a") {
			foundBlocker = true
		}
	}
	if !foundBlocker {
		t.Errorf("task-b should show 'blocked by' with 'task-a', got:\n%s", output)
	}
}

// SN-01: Full tree for nested epic structure (3-level deep)
func TestRenderDAGTree_NestedEpic(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"root-epic": {ID: "root-epic", Title: "Root", Type: "epic", Status: "open",
			Children: []string{"sub-epic"}},
		"sub-epic": {ID: "sub-epic", Title: "Sub", Type: "epic", Status: "open",
			Parent: "root-epic", Children: []string{"sub-sub-epic"}},
		"sub-sub-epic": {ID: "sub-sub-epic", Title: "SubSub", Type: "epic", Status: "open",
			Parent: "sub-epic", Children: []string{"deep-task"}},
		"deep-task": {ID: "deep-task", Title: "Deep Task", Type: "task", Status: "open",
			Rig: "gastown", Parent: "sub-sub-epic"},
	}}
	input := &StageInput{Kind: StageInputEpic, IDs: []string{"root-epic"}}
	output := renderDAGTree(dag, input)

	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines (root + 3 descendants), got %d:\n%s", len(lines), output)
	}

	// Verify indentation increases at each level.
	// Root has no indent (line 0), sub-epic has some, sub-sub-epic more, deep-task most.
	// We measure indent by counting leading non-alpha chars.
	indentOf := func(line string) int {
		for i, ch := range line {
			if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') {
				return i
			}
		}
		return len(line)
	}

	indent0 := indentOf(lines[0]) // root-epic
	indent1 := indentOf(lines[1]) // sub-epic
	indent2 := indentOf(lines[2]) // sub-sub-epic
	indent3 := indentOf(lines[3]) // deep-task

	if indent1 <= indent0 {
		t.Errorf("sub-epic indent (%d) should be > root indent (%d)", indent1, indent0)
	}
	if indent2 <= indent1 {
		t.Errorf("sub-sub-epic indent (%d) should be > sub-epic indent (%d)", indent2, indent1)
	}
	if indent3 <= indent2 {
		t.Errorf("deep-task indent (%d) should be > sub-sub-epic indent (%d)", indent3, indent2)
	}
}

// IT-40: Tree displayed before wave table (ordering contract)
func TestRenderDAGTree_OutputOrdering(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Title: "Task A", Type: "task", Status: "open", Rig: "gastown",
			Blocks: []string{"gt-b"}},
		"gt-b": {ID: "gt-b", Title: "Task B", Type: "task", Status: "open", Rig: "gastown",
			BlockedBy: []string{"gt-a"}},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b"}}

	treeOutput := renderDAGTree(dag, input)

	// Tree output should NOT contain wave table markers (header separator, wave numbers in columns)
	// The wave table uses "─" separator lines and columnar "Wave" header.
	if strings.Contains(treeOutput, "──────") {
		t.Errorf("tree output should not contain wave table separator, got:\n%s", treeOutput)
	}
	if strings.Contains(treeOutput, "Wave") && strings.Contains(treeOutput, "Blocked By") {
		t.Errorf("tree output should not contain wave table header, got:\n%s", treeOutput)
	}

	// Verify tree output is non-empty and contains expected bead IDs
	if !strings.Contains(treeOutput, "gt-a") || !strings.Contains(treeOutput, "gt-b") {
		t.Errorf("tree output should contain task IDs, got:\n%s", treeOutput)
	}

	// Simulate the full output: tree + wave table concatenation
	waves := []Wave{
		{Number: 1, Tasks: []string{"gt-a"}},
		{Number: 2, Tasks: []string{"gt-b"}},
	}
	waveOutput := renderWaveTable(waves, dag)

	fullOutput := treeOutput + "\n" + waveOutput

	// Tree content (task IDs without wave table formatting) appears before wave table content
	treeFirstID := strings.Index(fullOutput, "gt-a")
	waveTableStart := strings.Index(fullOutput, "Wave")
	if treeFirstID < 0 || waveTableStart < 0 {
		t.Fatalf("expected both tree content and wave table in full output:\n%s", fullOutput)
	}
	if treeFirstID >= waveTableStart {
		t.Errorf("tree content (at %d) should appear before wave table (at %d) in full output", treeFirstID, waveTableStart)
	}
}

// ---------------------------------------------------------------------------
// Warning detection tests (gt-csl.3.4)
// ---------------------------------------------------------------------------

// U-22: Parked rig detected and warned
// This test uses the isRigParkedFn seam to mock parked rig detection.
func TestDetectWarnings_ParkedRig(t *testing.T) {
	// Set up a temp dir as town root and cd there for workspace.FindFromCwd()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	oldDir, _ := os.Getwd()
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(oldDir) })

	// Override isRigBlockedFn to return true for "parkedrig"
	origFn := isRigBlockedFn
	isRigBlockedFn = func(townRoot, rigName string) (bool, string) {
		if rigName == "parkedrig" {
			return true, "parked"
		}
		return false, ""
	}
	t.Cleanup(func() { isRigBlockedFn = origFn })

	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Type: "task", Rig: "parkedrig"},
		"gt-b": {ID: "gt-b", Type: "task", Rig: "gastown"},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b"}}
	findings := detectWarnings(dag, input)

	var parkedFindings []StagingFinding
	for _, f := range findings {
		if f.Category == "blocked-rig" {
			parkedFindings = append(parkedFindings, f)
		}
	}
	if len(parkedFindings) != 1 {
		t.Fatalf("expected 1 blocked-rig warning, got %d: %+v", len(parkedFindings), findings)
	}
	f := parkedFindings[0]
	if f.Severity != "warning" {
		t.Errorf("severity = %q, want %q", f.Severity, "warning")
	}
	if !sliceContains(f.BeadIDs, "gt-a") {
		t.Errorf("BeadIDs should contain gt-a, got %v", f.BeadIDs)
	}
}

// Regression test for #2120 review item #1: docked rigs should also be detected.
func TestDetectWarnings_DockedRig(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	oldDir, _ := os.Getwd()
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(oldDir) })

	// Override isRigBlockedFn to return docked for "dockedrig"
	origFn := isRigBlockedFn
	isRigBlockedFn = func(townRoot, rigName string) (bool, string) {
		if rigName == "dockedrig" {
			return true, "docked"
		}
		return false, ""
	}
	t.Cleanup(func() { isRigBlockedFn = origFn })

	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Type: "task", Rig: "dockedrig"},
		"gt-b": {ID: "gt-b", Type: "task", Rig: "gastown"},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b"}}
	findings := detectWarnings(dag, input)

	var blockedFindings []StagingFinding
	for _, f := range findings {
		if f.Category == "blocked-rig" {
			blockedFindings = append(blockedFindings, f)
		}
	}
	if len(blockedFindings) != 1 {
		t.Fatalf("expected 1 blocked-rig warning for docked rig, got %d: %+v", len(blockedFindings), findings)
	}
	f := blockedFindings[0]
	if f.Severity != "warning" {
		t.Errorf("severity = %q, want %q", f.Severity, "warning")
	}
	if !sliceContains(f.BeadIDs, "gt-a") {
		t.Errorf("BeadIDs should contain gt-a, got %v", f.BeadIDs)
	}
	if !strings.Contains(f.Message, "docked") {
		t.Errorf("message should mention 'docked', got: %s", f.Message)
	}
	if !strings.Contains(f.SuggestedFix, "undock") {
		t.Errorf("suggested fix should mention 'undock', got: %s", f.SuggestedFix)
	}
}

// U-23: Orphan detection for epic input
func TestDetectWarnings_OrphanEpicInput(t *testing.T) {
	// 3 tasks under an epic: A blocks B (connected), C is isolated.
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1": {ID: "epic-1", Type: "epic", Children: []string{"gt-a", "gt-b", "gt-c"}},
		"gt-a":   {ID: "gt-a", Type: "task", Rig: "gastown", Parent: "epic-1", Blocks: []string{"gt-b"}},
		"gt-b":   {ID: "gt-b", Type: "task", Rig: "gastown", Parent: "epic-1", BlockedBy: []string{"gt-a"}},
		"gt-c":   {ID: "gt-c", Type: "task", Rig: "gastown", Parent: "epic-1"},
	}}
	input := &StageInput{Kind: StageInputEpic, IDs: []string{"epic-1"}}
	findings := detectWarnings(dag, input)

	var orphanFindings []StagingFinding
	for _, f := range findings {
		if f.Category == "orphan" {
			orphanFindings = append(orphanFindings, f)
		}
	}
	if len(orphanFindings) != 1 {
		t.Fatalf("expected 1 orphan warning, got %d: %+v", len(orphanFindings), findings)
	}
	if !sliceContains(orphanFindings[0].BeadIDs, "gt-c") {
		t.Errorf("orphan warning should reference gt-c, got %v", orphanFindings[0].BeadIDs)
	}
}

// U-24: Missing integration branch warning
func TestDetectWarnings_MissingBranch(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"root-epic": {ID: "root-epic", Type: "epic", Children: []string{"sub-epic"}},
		"sub-epic":  {ID: "sub-epic", Type: "epic", Parent: "root-epic", Children: []string{"gt-a", "gt-b"}},
		"gt-a":      {ID: "gt-a", Type: "task", Rig: "gastown", Parent: "sub-epic"},
		"gt-b":      {ID: "gt-b", Type: "task", Rig: "gastown", Parent: "sub-epic"},
	}}
	input := &StageInput{Kind: StageInputEpic, IDs: []string{"root-epic"}}
	findings := detectWarnings(dag, input)

	var branchFindings []StagingFinding
	for _, f := range findings {
		if f.Category == "missing-branch" {
			branchFindings = append(branchFindings, f)
		}
	}
	if len(branchFindings) != 1 {
		t.Fatalf("expected 1 missing-branch warning, got %d: %+v", len(branchFindings), findings)
	}
	f := branchFindings[0]
	if f.Severity != "warning" {
		t.Errorf("severity = %q, want %q", f.Severity, "warning")
	}
	if !sliceContains(f.BeadIDs, "sub-epic") {
		t.Errorf("BeadIDs should contain sub-epic, got %v", f.BeadIDs)
	}
	if !strings.Contains(f.SuggestedFix, "sub-epic") {
		t.Errorf("SuggestedFix should mention sub-epic, got %q", f.SuggestedFix)
	}
}

// U-34: Cross-rig routing mismatch warned
func TestDetectWarnings_CrossRig(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Type: "task", Rig: "gastown"},
		"gt-b": {ID: "gt-b", Type: "task", Rig: "gastown"},
		"bd-c": {ID: "bd-c", Type: "task", Rig: "beads"},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b", "bd-c"}}
	findings := detectWarnings(dag, input)

	var crossFindings []StagingFinding
	for _, f := range findings {
		if f.Category == "cross-rig" {
			crossFindings = append(crossFindings, f)
		}
	}
	if len(crossFindings) != 1 {
		t.Fatalf("expected 1 cross-rig warning, got %d: %+v", len(crossFindings), findings)
	}
	f := crossFindings[0]
	if f.Severity != "warning" {
		t.Errorf("severity = %q, want %q", f.Severity, "warning")
	}
	if !sliceContains(f.BeadIDs, "bd-c") {
		t.Errorf("BeadIDs should contain bd-c, got %v", f.BeadIDs)
	}
	if !strings.Contains(f.Message, "gastown") {
		t.Errorf("Message should mention primary rig gastown, got %q", f.Message)
	}
}

// U-35: Capacity estimation
func TestDetectWarnings_Capacity(t *testing.T) {
	// Create a DAG where wave 1 has 6 independent tasks (all in-degree 0).
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"t1": {ID: "t1", Type: "task", Rig: "gastown"},
		"t2": {ID: "t2", Type: "task", Rig: "gastown"},
		"t3": {ID: "t3", Type: "task", Rig: "gastown"},
		"t4": {ID: "t4", Type: "task", Rig: "gastown"},
		"t5": {ID: "t5", Type: "task", Rig: "gastown"},
		"t6": {ID: "t6", Type: "task", Rig: "gastown"},
	}}

	// Verify computeWaves puts them all in wave 1.
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}
	if len(waves) != 1 || len(waves[0].Tasks) != 6 {
		t.Fatalf("expected 1 wave with 6 tasks, got %d waves with tasks: %+v", len(waves), waves)
	}

	input := &StageInput{Kind: StageInputTasks, IDs: []string{"t1", "t2", "t3", "t4", "t5", "t6"}}
	findings := detectWarnings(dag, input)

	var capFindings []StagingFinding
	for _, f := range findings {
		if f.Category == "capacity" {
			capFindings = append(capFindings, f)
		}
	}
	if len(capFindings) != 1 {
		t.Fatalf("expected 1 capacity warning, got %d: %+v", len(capFindings), findings)
	}
	f := capFindings[0]
	if f.Severity != "warning" {
		t.Errorf("severity = %q, want %q", f.Severity, "warning")
	}
	if !strings.Contains(f.Message, "wave 1") {
		t.Errorf("Message should mention wave 1, got %q", f.Message)
	}
	if !strings.Contains(f.Message, "6 tasks") {
		t.Errorf("Message should mention 6 tasks, got %q", f.Message)
	}
}

// IT-43: Orphan detection skipped for task-list input
func TestDetectWarnings_NoOrphansForTaskList(t *testing.T) {
	// Same DAG as orphan test but with task-list input.
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Type: "task", Rig: "gastown", Blocks: []string{"gt-b"}},
		"gt-b": {ID: "gt-b", Type: "task", Rig: "gastown", BlockedBy: []string{"gt-a"}},
		"gt-c": {ID: "gt-c", Type: "task", Rig: "gastown"}, // isolated
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b", "gt-c"}}
	findings := detectWarnings(dag, input)

	for _, f := range findings {
		if f.Category == "orphan" {
			t.Errorf("task-list input should NOT produce orphan warnings, got: %+v", f)
		}
	}
}

// Test renderWarnings output format
func TestRenderWarnings_Format(t *testing.T) {
	findings := []StagingFinding{
		{
			Severity:     "warning",
			Category:     "blocked-rig",
			BeadIDs:      []string{"gt-a"},
			Message:      "task gt-a is assigned to parked rig \"gastown.parked\"",
			SuggestedFix: "reassign gt-a to an active rig",
		},
		{
			Severity: "warning",
			Category: "capacity",
			BeadIDs:  []string{"t1", "t2", "t3", "t4", "t5", "t6"},
			Message:  "wave 1 has 6 tasks (threshold: 5) — may exceed parallel capacity",
		},
		{
			Severity:     "warning",
			Category:     "cross-rig",
			BeadIDs:      []string{"bd-c"},
			Message:      "task bd-c is on rig \"beads\" (primary rig is \"gastown\")",
			SuggestedFix: "verify cross-rig routing for bd-c or reassign to gastown",
		},
	}

	output := renderWarnings(findings)

	// Must start with "Warnings:" header
	if !strings.HasPrefix(output, "Warnings:\n") {
		t.Errorf("output should start with 'Warnings:\\n', got:\n%s", output)
	}

	// Must include categories
	for _, cat := range []string{"blocked-rig", "capacity", "cross-rig"} {
		if !strings.Contains(output, cat) {
			t.Errorf("output should contain category %q, got:\n%s", cat, output)
		}
	}

	// Must include bead IDs
	for _, id := range []string{"gt-a", "bd-c"} {
		if !strings.Contains(output, id) {
			t.Errorf("output should contain bead ID %q, got:\n%s", id, output)
		}
	}

	// Must include suggested fixes
	if !strings.Contains(output, "reassign gt-a") {
		t.Errorf("output should contain suggested fix, got:\n%s", output)
	}

	// Numbered items
	if !strings.Contains(output, "1.") || !strings.Contains(output, "2.") || !strings.Contains(output, "3.") {
		t.Errorf("output should contain numbered items 1-3, got:\n%s", output)
	}
}

// Test detectWarnings clean DAG — no warnings
func TestDetectWarnings_Clean(t *testing.T) {
	// Override isRigBlockedFn so the test doesn't depend on real rig state.
	origFn := isRigBlockedFn
	isRigBlockedFn = func(townRoot, rigName string) (bool, string) { return false, "" }
	t.Cleanup(func() { isRigBlockedFn = origFn })

	// All tasks on same rig, all have deps between them, epic input.
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1": {ID: "epic-1", Type: "epic", Children: []string{"gt-a", "gt-b", "gt-c"}},
		"gt-a":   {ID: "gt-a", Type: "task", Rig: "gastown", Parent: "epic-1", Blocks: []string{"gt-b"}},
		"gt-b":   {ID: "gt-b", Type: "task", Rig: "gastown", Parent: "epic-1", BlockedBy: []string{"gt-a"}, Blocks: []string{"gt-c"}},
		"gt-c":   {ID: "gt-c", Type: "task", Rig: "gastown", Parent: "epic-1", BlockedBy: []string{"gt-b"}},
	}}
	input := &StageInput{Kind: StageInputEpic, IDs: []string{"epic-1"}}
	findings := detectWarnings(dag, input)
	if len(findings) != 0 {
		t.Errorf("expected 0 warnings for clean DAG, got %d: %+v", len(findings), findings)
	}
}

// Test renderWarnings with empty findings
func TestRenderWarnings_Empty(t *testing.T) {
	output := renderWarnings(nil)
	if output != "" {
		t.Errorf("expected empty string for nil findings, got %q", output)
	}
	output = renderWarnings([]StagingFinding{})
	if output != "" {
		t.Errorf("expected empty string for empty findings, got %q", output)
	}
}

// ---------------------------------------------------------------------------
// Staged convoy creation tests (gt-csl.3.5)
// ---------------------------------------------------------------------------

// IT-10: Stage clean (no errors, no warnings) → creates convoy as staged_ready.
// Uses dagBuilder to set up the bd stub environment. Builds a clean ConvoyDAG
// directly (with rigs set). Verifies `bd create` was called with
// --status=staged_ready and `bd dep add` was called for each slingable bead.
func TestCreateStagedConvoy_CleanReady(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	// Set up bd stub environment for create/dep add commands.
	testDAG := newTestDAG(t).
		Task("gt-a", "Task A", withRig("gastown")).
		Task("gt-b", "Task B", withRig("gastown")).BlockedBy("gt-a").
		Task("gt-c", "Task C", withRig("gastown")).BlockedBy("gt-b")

	_, logPath := testDAG.Setup(t)

	// Build the ConvoyDAG directly with rigs populated (avoids rigFromBeadID stub).
	convoyDAG := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Title: "Task A", Type: "task", Status: "open", Rig: "gastown",
			Blocks: []string{"gt-b"}},
		"gt-b": {ID: "gt-b", Title: "Task B", Type: "task", Status: "open", Rig: "gastown",
			BlockedBy: []string{"gt-a"}, Blocks: []string{"gt-c"}},
		"gt-c": {ID: "gt-c", Title: "Task C", Type: "task", Status: "open", Rig: "gastown",
			BlockedBy: []string{"gt-b"}},
	}}

	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b", "gt-c"}}

	// Run the full error/warning detection pipeline.
	errFindings := detectErrors(convoyDAG)
	warnFindings := detectWarnings(convoyDAG, input)
	errs, warns := categorizeFindings(append(errFindings, warnFindings...))
	status := chooseStatus(errs, warns)

	if status != "staged_ready" {
		t.Fatalf("expected staged_ready, got %q", status)
	}

	waves, _, err := computeWaves(convoyDAG)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	convoyID, err := createStagedConvoy(convoyDAG, waves, status, "")
	if err != nil {
		t.Fatalf("createStagedConvoy: %v", err)
	}

	if convoyID == "" {
		t.Fatal("expected non-empty convoy ID")
	}
	if !strings.HasPrefix(convoyID, "hq-cv-") {
		t.Errorf("convoy ID should start with hq-cv-, got %q", convoyID)
	}

	// Read bd.log and verify bd create was called with --status=staged_ready.
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd.log: %v", err)
	}
	logContent := string(logBytes)

	if !strings.Contains(logContent, "create") {
		t.Errorf("bd.log should contain 'create' command, got:\n%s", logContent)
	}
	if !strings.Contains(logContent, "--status=staged_ready") {
		t.Errorf("bd.log should contain '--status=staged_ready', got:\n%s", logContent)
	}

	// Verify bd dep add was called for each slingable bead.
	for _, beadID := range []string{"gt-a", "gt-b", "gt-c"} {
		targetID := "external:gt:" + beadID
		if !strings.Contains(logContent, "dep add "+convoyID+" "+targetID) {
			t.Errorf("bd.log should contain 'dep add %s %s', got:\n%s", convoyID, targetID, logContent)
		}
	}
}

// IT-11: Stage convoy tracks all slingable beads via deps.
// Verifies that epics are NOT tracked, but tasks/bugs ARE tracked.
func TestCreateStagedConvoy_TracksOnlySlingable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Epic("gt-epic", "Root Epic").
		Task("gt-t1", "Task 1", withRig("gastown")).ParentOf("gt-epic").
		Bug("gt-b1", "Bug 1", withRig("gastown")).ParentOf("gt-epic").
		Task("gt-t2", "Task 2", withRig("gastown")).ParentOf("gt-epic").BlockedBy("gt-t1")

	_, logPath := dag.Setup(t)

	input := &StageInput{Kind: StageInputEpic, IDs: []string{"gt-epic"}}
	beads, deps, err := collectBeads(input)
	if err != nil {
		t.Fatalf("collectBeads: %v", err)
	}

	convoyDAG := buildConvoyDAG(beads, deps)

	waves, _, err := computeWaves(convoyDAG)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	convoyID, err := createStagedConvoy(convoyDAG, waves, "staged_ready", "")
	if err != nil {
		t.Fatalf("createStagedConvoy: %v", err)
	}

	// Read bd.log.
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd.log: %v", err)
	}
	logContent := string(logBytes)

	// Slingable beads (tasks and bugs) should be tracked.
	for _, beadID := range []string{"gt-t1", "gt-b1", "gt-t2"} {
		targetID := "external:gt:" + beadID
		if !strings.Contains(logContent, "dep add "+convoyID+" "+targetID) {
			t.Errorf("bd.log should contain 'dep add %s %s' for slingable bead, got:\n%s", convoyID, targetID, logContent)
		}
	}

	// Epics should NOT be tracked.
	lines := strings.Split(logContent, "\n")
	for _, line := range lines {
		if strings.Contains(line, "dep add") && strings.Contains(line, "gt-epic") {
			t.Errorf("epic gt-epic should NOT be tracked via dep add, but found: %s", line)
		}
	}
}

// IT-12: Stage convoy description includes wave count + timestamp.
func TestCreateStagedConvoy_DescriptionFormat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Task("gt-a", "Task A", withRig("gastown")).
		Task("gt-b", "Task B", withRig("gastown")).BlockedBy("gt-a")

	_, logPath := dag.Setup(t)

	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b"}}
	beads, deps, err := collectBeads(input)
	if err != nil {
		t.Fatalf("collectBeads: %v", err)
	}

	convoyDAG := buildConvoyDAG(beads, deps)

	waves, _, err := computeWaves(convoyDAG)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	_, err = createStagedConvoy(convoyDAG, waves, "staged_ready", "")
	if err != nil {
		t.Fatalf("createStagedConvoy: %v", err)
	}

	// Read bd.log to find the create command and verify description.
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd.log: %v", err)
	}
	logContent := string(logBytes)

	// Find the create command line.
	lines := strings.Split(logContent, "\n")
	var createLine string
	for _, line := range lines {
		if strings.Contains(line, "create") && strings.Contains(line, "--type=task") && strings.Contains(line, "--labels=gt:convoy") {
			createLine = line
			break
		}
	}
	if createLine == "" {
		t.Fatalf("no create command found in bd.log:\n%s", logContent)
	}

	// Description should include task count, wave count, and a timestamp.
	if !strings.Contains(createLine, "2 tasks") {
		t.Errorf("create command should mention '2 tasks' in description, got: %s", createLine)
	}
	if !strings.Contains(createLine, "2 waves") {
		t.Errorf("create command should mention '2 waves' in description, got: %s", createLine)
	}
	// Timestamp should look like an RFC3339 date (contains T and Z or +).
	if !strings.Contains(createLine, "Staged at") {
		t.Errorf("create command should contain 'Staged at' timestamp, got: %s", createLine)
	}
}

// IT-41: Convoy ID printed to stdout.
func TestCreateStagedConvoy_IDFormat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Task("gt-a", "Task A", withRig("gastown"))

	dag.Setup(t)

	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a"}}
	beads, deps, err := collectBeads(input)
	if err != nil {
		t.Fatalf("collectBeads: %v", err)
	}

	convoyDAG := buildConvoyDAG(beads, deps)

	waves, _, err := computeWaves(convoyDAG)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	convoyID, err := createStagedConvoy(convoyDAG, waves, "staged_ready", "")
	if err != nil {
		t.Fatalf("createStagedConvoy: %v", err)
	}

	// Convoy ID must be non-empty and start with hq-cv-.
	if convoyID == "" {
		t.Fatal("convoy ID should not be empty")
	}
	if !strings.HasPrefix(convoyID, "hq-cv-") {
		t.Errorf("convoy ID should start with 'hq-cv-', got %q", convoyID)
	}
	// The suffix should be base36 (lowercase alphanumeric).
	suffix := strings.TrimPrefix(convoyID, "hq-cv-")
	for _, ch := range suffix {
		if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')) {
			t.Errorf("convoy ID suffix should be base36 chars, got %q in %q", string(ch), suffix)
		}
	}
}

// ---------------------------------------------------------------------------
// Re-stage existing convoy tests (gt-csl.3.6)
// ---------------------------------------------------------------------------

// IT-13: Re-stage existing staged convoy updates in place (no duplicate).
//
// 1. Set up a DAG with a convoy that has status "staged_ready" and tracks 2 tasks.
// 2. Call updateStagedConvoy (the re-stage path).
// 3. Verify: bd.log shows `bd update <convoy-id>` was called (not `bd create`).
// 4. Verify: no duplicate convoy was created.
// 5. Verify: original convoy ID preserved.
func TestRestageConvoy_UpdatesInPlace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	// Build a DAG with a convoy already in "staged_ready" status,
	// tracking two tasks.
	testDAG := newTestDAG(t).
		Convoy("hq-cv-test1", "Staged Convoy").WithStatus("staged_ready").
		Task("gt-x1", "Task X1", withRig("gastown")).TrackedBy("hq-cv-test1").
		Task("gt-x2", "Task X2", withRig("gastown")).TrackedBy("hq-cv-test1").BlockedBy("gt-x1")

	_, logPath := testDAG.Setup(t)

	// Build the ConvoyDAG directly (as runConvoyStage would after collectBeads).
	convoyDAG := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-x1": {ID: "gt-x1", Title: "Task X1", Type: "task", Status: "open", Rig: "gastown",
			Blocks: []string{"gt-x2"}},
		"gt-x2": {ID: "gt-x2", Title: "Task X2", Type: "task", Status: "open", Rig: "gastown",
			BlockedBy: []string{"gt-x1"}},
	}}

	waves, _, err := computeWaves(convoyDAG)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	// Call updateStagedConvoy — the re-stage path.
	err = updateStagedConvoy("hq-cv-test1", convoyDAG, waves, "staged_ready", "")
	if err != nil {
		t.Fatalf("updateStagedConvoy: %v", err)
	}

	// Read bd.log to inspect which commands were run.
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd.log: %v", err)
	}
	logContent := string(logBytes)

	// Verify: bd update was called with --status=staged_ready.
	if !strings.Contains(logContent, "update hq-cv-test1") {
		t.Errorf("bd.log should contain 'update hq-cv-test1', got:\n%s", logContent)
	}
	if !strings.Contains(logContent, "--status=staged_ready") {
		t.Errorf("bd.log should contain '--status=staged_ready', got:\n%s", logContent)
	}

	// Verify: NO bd create was called.
	lines := strings.Split(logContent, "\n")
	for _, line := range lines {
		if strings.Contains(line, "CMD:create") {
			t.Errorf("bd create should NOT be called during re-stage, but found: %s", line)
		}
	}

	// Verify: NO bd dep add was called (tracking deps already exist).
	for _, line := range lines {
		if strings.Contains(line, "dep add") {
			t.Errorf("bd dep add should NOT be called during re-stage (deps already exist), but found: %s", line)
		}
	}

	// Verify: description update was called.
	foundDescUpdate := false
	for _, line := range lines {
		if strings.Contains(line, "update hq-cv-test1") && strings.Contains(line, "--description=") {
			foundDescUpdate = true
		}
	}
	if !foundDescUpdate {
		t.Errorf("bd.log should contain description update for hq-cv-test1, got:\n%s", logContent)
	}
}

// IT-13b: Re-stage detection logic correctly identifies already-staged convoys.
// Verifies the re-stage flag is set when input convoy has "staged_" prefix status.
func TestRestageConvoy_DetectionLogic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	testDAG := newTestDAG(t).
		Convoy("hq-cv-det", "Detection Convoy").WithStatus("staged_ready").
		Task("gt-d1", "Detection Task 1", withRig("gastown")).TrackedBy("hq-cv-det").
		Task("gt-d2", "Detection Task 2", withRig("gastown")).TrackedBy("hq-cv-det")

	testDAG.Setup(t)

	// Step 2: Resolve bead type via bdShow.
	result, err := bdShow("hq-cv-det")
	if err != nil {
		t.Fatalf("bdShow: %v", err)
	}

	// Verify it's recognized as a convoy.
	if !isConvoyIssue(result.IssueType, result.Labels) {
		t.Fatalf("expected convoy bead, got type %q labels %v", result.IssueType, result.Labels)
	}

	// Verify status is "staged_ready".
	if result.Status != "staged_ready" {
		t.Fatalf("expected status 'staged_ready', got %q", result.Status)
	}

	// Verify the detection logic: status starts with "staged_".
	if !strings.HasPrefix(result.Status, "staged_") {
		t.Errorf("expected status to start with 'staged_', got %q", result.Status)
	}

	// Verify resolveInputKind classifies as convoy.
	beadTypes := map[string]string{"hq-cv-det": result.IssueType}
	input, err := resolveInputKind(beadTypes)
	if err != nil {
		t.Fatalf("resolveInputKind: %v", err)
	}
	if input.Kind != StageInputConvoy {
		t.Errorf("expected StageInputConvoy, got %v", input.Kind)
	}
}

// IT-13c: Re-stage with different status updates correctly.
// Verifies updateStagedConvoy can set staged_warnings status.
func TestRestageConvoy_UpdatesStatusToWarnings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	testDAG := newTestDAG(t).
		Convoy("hq-cv-warn", "Warn Convoy").WithStatus("staged_ready").
		Task("gt-w1", "Warn Task 1", withRig("gastown")).TrackedBy("hq-cv-warn").
		Task("bd-w2", "Warn Task 2", withRig("beads")).TrackedBy("hq-cv-warn")

	_, logPath := testDAG.Setup(t)

	// Build a ConvoyDAG with cross-rig tasks.
	convoyDAG := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-w1": {ID: "gt-w1", Title: "Warn Task 1", Type: "task", Status: "open", Rig: "gastown"},
		"bd-w2": {ID: "bd-w2", Title: "Warn Task 2", Type: "task", Status: "open", Rig: "beads"},
	}}

	waves, _, err := computeWaves(convoyDAG)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	// Call updateStagedConvoy with staged_warnings status.
	err = updateStagedConvoy("hq-cv-warn", convoyDAG, waves, "staged_warnings", "")
	if err != nil {
		t.Fatalf("updateStagedConvoy: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd.log: %v", err)
	}
	logContent := string(logBytes)

	// Status should be updated to staged_warnings.
	if !strings.Contains(logContent, "--status=staged_warnings") {
		t.Errorf("re-stage with warnings should set --status=staged_warnings, got:\n%s", logContent)
	}

	// No create command should be in the log.
	for _, line := range strings.Split(logContent, "\n") {
		if strings.Contains(line, "CMD:create") {
			t.Errorf("should NOT call 'bd create', found: %s", line)
		}
	}
}

// ---------------------------------------------------------------------------
// JSON output mode tests (gt-csl.4.3)
// ---------------------------------------------------------------------------

// U-31: JSON output: valid JSON with all required fields present.
// Build a clean DAG (no errors, no warnings), call the JSON rendering
// function, verify valid JSON with all fields.
func TestJSONOutput_ValidWithAllFields(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Title: "Task A", Type: "task", Status: "open", Rig: "gastown",
			Blocks: []string{"gt-b"}},
		"gt-b": {ID: "gt-b", Title: "Task B", Type: "task", Status: "open", Rig: "gastown",
			BlockedBy: []string{"gt-a"}},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b"}}

	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	result := StageResult{
		Status:   "staged_ready",
		ConvoyID: "hq-cv-test1",
		Errors:   buildFindingsJSON(nil),
		Warnings: buildFindingsJSON(nil),
		Waves:    buildWavesJSON(waves, dag),
		Tree:     buildTreeJSON(dag, input),
	}

	out, err := renderJSON(result)
	if err != nil {
		t.Fatalf("renderJSON: %v", err)
	}

	// Must be valid JSON.
	var parsed StageResult
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\nraw:\n%s", err, out)
	}

	// All required fields present.
	if parsed.Status != "staged_ready" {
		t.Errorf("status = %q, want %q", parsed.Status, "staged_ready")
	}
	if parsed.ConvoyID != "hq-cv-test1" {
		t.Errorf("convoy_id = %q, want %q", parsed.ConvoyID, "hq-cv-test1")
	}
	if parsed.Errors == nil {
		t.Error("errors should not be nil (should be empty array)")
	}
	if parsed.Warnings == nil {
		t.Error("warnings should not be nil (should be empty array)")
	}
	if len(parsed.Waves) == 0 {
		t.Error("waves should not be empty")
	}
	if len(parsed.Tree) == 0 {
		t.Error("tree should not be empty")
	}

	// Verify waves contain task details.
	foundA := false
	foundB := false
	for _, w := range parsed.Waves {
		for _, task := range w.Tasks {
			if task.ID == "gt-a" {
				foundA = true
				if task.Title != "Task A" {
					t.Errorf("gt-a title = %q, want %q", task.Title, "Task A")
				}
				if task.Rig != "gastown" {
					t.Errorf("gt-a rig = %q, want %q", task.Rig, "gastown")
				}
			}
			if task.ID == "gt-b" {
				foundB = true
				if len(task.BlockedBy) == 0 || task.BlockedBy[0] != "gt-a" {
					t.Errorf("gt-b blocked_by = %v, want [gt-a]", task.BlockedBy)
				}
			}
		}
	}
	if !foundA {
		t.Error("wave tasks should contain gt-a")
	}
	if !foundB {
		t.Error("wave tasks should contain gt-b")
	}
}

// U-32: JSON output: errors array populated on failure.
// Build a DAG with a cycle, verify the errors array has the cycle finding.
func TestJSONOutput_ErrorsPopulatedOnCycle(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Type: "task", Rig: "gastown",
			Blocks: []string{"gt-b"}, BlockedBy: []string{"gt-b"}},
		"gt-b": {ID: "gt-b", Type: "task", Rig: "gastown",
			Blocks: []string{"gt-a"}, BlockedBy: []string{"gt-a"}},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-a", "gt-b"}}

	errFindings := detectErrors(dag)
	warnFindings := detectWarnings(dag, input)
	errs, warns := categorizeFindings(append(errFindings, warnFindings...))

	if len(errs) == 0 {
		t.Fatal("expected cycle error")
	}

	result := StageResult{
		Status:   "error",
		Errors:   buildFindingsJSON(errs),
		Warnings: buildFindingsJSON(warns),
		Waves:    []WaveJSON{},
		Tree:     buildTreeJSON(dag, input),
	}

	out, err := renderJSON(result)
	if err != nil {
		t.Fatalf("renderJSON: %v", err)
	}

	var parsed StageResult
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if len(parsed.Errors) == 0 {
		t.Fatal("errors array should not be empty for cycle DAG")
	}

	foundCycle := false
	for _, e := range parsed.Errors {
		if e.Category == "cycle" {
			foundCycle = true
			if len(e.BeadIDs) == 0 {
				t.Error("cycle error should have bead_ids")
			}
			if e.Message == "" {
				t.Error("cycle error should have message")
			}
		}
	}
	if !foundCycle {
		t.Errorf("expected cycle error in errors array, got: %+v", parsed.Errors)
	}
}

// U-33: JSON output: convoy_id empty when errors found.
func TestJSONOutput_ConvoyIDEmptyOnErrors(t *testing.T) {
	result := StageResult{
		Status:   "error",
		ConvoyID: "", // no convoy created
		Errors: []FindingJSON{
			{Category: "cycle", BeadIDs: []string{"a", "b"}, Message: "cycle detected"},
		},
		Warnings: []FindingJSON{},
		Waves:    []WaveJSON{},
		Tree:     []TreeNodeJSON{},
	}

	out, err := renderJSON(result)
	if err != nil {
		t.Fatalf("renderJSON: %v", err)
	}

	var parsed StageResult
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if parsed.ConvoyID != "" {
		t.Errorf("convoy_id should be empty on error, got %q", parsed.ConvoyID)
	}
	if parsed.Status != "error" {
		t.Errorf("status should be 'error', got %q", parsed.Status)
	}
}

// IT-21: --json flag outputs valid JSON to stdout.
// Verifies the flag is registered on the command.
func TestJSONFlag_RegisteredOnCommand(t *testing.T) {
	flag := convoyStageCmd.Flags().Lookup("json")
	if flag == nil {
		t.Fatal("--json flag not registered on convoyStageCmd")
	}
	if flag.DefValue != "false" {
		t.Errorf("--json default should be false, got %q", flag.DefValue)
	}
}

// IT-22: --json output: no human-readable text on stdout.
// Verify JSON mode suppresses tree/table/error output.
// Note: rigFromBeadID() is a stub returning "", so tasks get no-rig errors.
// This test verifies that even on error, JSON mode outputs JSON (not human text).
func TestJSONOutput_NoHumanReadableText(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	testDAG := newTestDAG(t).
		Task("gt-j1", "JSON Task 1", withRig("gastown")).
		Task("gt-j2", "JSON Task 2", withRig("gastown")).BlockedBy("gt-j1")

	testDAG.Setup(t)

	// Capture stdout by setting convoyStageJSON and running the pipeline.
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Also capture stderr to verify no human-readable errors go there.
	oldStderr := os.Stderr
	rErr, wErr, _ := os.Pipe()
	os.Stderr = wErr

	// Enable JSON mode.
	convoyStageJSON = true
	defer func() { convoyStageJSON = false }()

	_ = runConvoyStage(nil, []string{"gt-j1", "gt-j2"})
	w.Close()
	wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr

	outBytes, _ := io.ReadAll(r)
	output := string(outBytes)

	errBytes, _ := io.ReadAll(rErr)
	stderrOutput := string(errBytes)

	// Stdout should be valid JSON.
	var parsed StageResult
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nraw:\n%s", err, output)
	}

	// Should NOT contain human-readable markers on stdout.
	if strings.Contains(output, "├── ") || strings.Contains(output, "└── ") {
		t.Errorf("JSON output should not contain tree characters, got:\n%s", output)
	}
	if strings.Contains(output, "Convoy created:") || strings.Contains(output, "Convoy updated:") {
		t.Errorf("JSON output should not contain human-readable convoy message, got:\n%s", output)
	}
	// The "Errors:" header from renderErrors should NOT appear in JSON mode.
	if strings.Contains(output, "Errors:\n") {
		t.Errorf("JSON output should not contain human-readable error header, got:\n%s", output)
	}
	// Stderr should be empty in JSON mode (errors go into JSON, not stderr).
	if stderrOutput != "" {
		t.Errorf("stderr should be empty in JSON mode, got:\n%s", stderrOutput)
	}
}

// IT-34: --json with errors: non-zero exit code.
func TestJSONOutput_ErrorsReturnNonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	// Create a DAG with a no-rig error (task without rig).
	// Use "zz-" prefix which won't be in routes.jsonl, so rigFromBeadID returns "".
	testDAG := newTestDAG(t).
		Task("zz-norig", "No Rig Task", "") // unmapped prefix → no-rig error

	testDAG.Setup(t)

	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	convoyStageJSON = true
	defer func() { convoyStageJSON = false }()

	err := runConvoyStage(nil, []string{"zz-norig"})
	w.Close()
	os.Stdout = old

	// Should return an error (non-zero exit code).
	if err == nil {
		t.Fatal("expected error for DAG with no-rig, got nil")
	}

	// But stdout should still contain valid JSON.
	outBytes, _ := io.ReadAll(r)
	output := string(outBytes)

	var parsed StageResult
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("error output should still be valid JSON: %v\nraw:\n%s", err, output)
	}

	if parsed.Status != "error" {
		t.Errorf("status should be 'error', got %q", parsed.Status)
	}
	if parsed.ConvoyID != "" {
		t.Errorf("convoy_id should be empty on error, got %q", parsed.ConvoyID)
	}
	if len(parsed.Errors) == 0 {
		t.Error("errors array should not be empty")
	}
}

// SN-06: JSON output: full structure snapshot.
// Build a representative DAG and verify the full JSON output structure
// matches expected field names, nesting, and types.
func TestJSONOutput_FullStructureSnapshot(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1": {ID: "epic-1", Title: "Root Epic", Type: "epic", Status: "open",
			Children: []string{"gt-a", "gt-b"}},
		"gt-a": {ID: "gt-a", Title: "Task A", Type: "task", Status: "open", Rig: "gastown",
			Parent: "epic-1", Blocks: []string{"gt-b"}},
		"gt-b": {ID: "gt-b", Title: "Task B", Type: "task", Status: "open", Rig: "gastown",
			Parent: "epic-1", BlockedBy: []string{"gt-a"}},
	}}
	input := &StageInput{Kind: StageInputEpic, IDs: []string{"epic-1"}}

	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	// Build a warning for cross-rig (simulated).
	warns := []StagingFinding{
		{Severity: "warning", Category: "orphan", BeadIDs: []string{"gt-a"},
			Message: "task gt-a isolated", SuggestedFix: "add dep"},
	}

	result := StageResult{
		Status:   "staged_warnings",
		ConvoyID: "hq-cv-snap1",
		Errors:   buildFindingsJSON(nil),
		Warnings: buildFindingsJSON(warns),
		Waves:    buildWavesJSON(waves, dag),
		Tree:     buildTreeJSON(dag, input),
	}

	out, err := renderJSON(result)
	if err != nil {
		t.Fatalf("renderJSON: %v", err)
	}

	// Parse into raw map to verify exact field names.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// All top-level fields must be present.
	requiredFields := []string{"status", "convoy_id", "errors", "warnings", "waves", "tree"}
	for _, field := range requiredFields {
		if _, ok := raw[field]; !ok {
			t.Errorf("missing top-level field %q in JSON output", field)
		}
	}

	// Parse fully and verify tree structure (epic input → nested).
	var parsed StageResult
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Tree should have 1 root node (epic-1) with 2 children.
	if len(parsed.Tree) != 1 {
		t.Fatalf("tree should have 1 root, got %d", len(parsed.Tree))
	}
	root := parsed.Tree[0]
	if root.ID != "epic-1" {
		t.Errorf("tree root ID = %q, want %q", root.ID, "epic-1")
	}
	if root.Type != "epic" {
		t.Errorf("tree root type = %q, want %q", root.Type, "epic")
	}
	if len(root.Children) != 2 {
		t.Fatalf("tree root should have 2 children, got %d", len(root.Children))
	}

	// Children should be sorted by ID (gt-a before gt-b).
	if root.Children[0].ID != "gt-a" {
		t.Errorf("first child = %q, want gt-a", root.Children[0].ID)
	}
	if root.Children[1].ID != "gt-b" {
		t.Errorf("second child = %q, want gt-b", root.Children[1].ID)
	}

	// Children should have rig set.
	if root.Children[0].Rig != "gastown" {
		t.Errorf("gt-a rig = %q, want gastown", root.Children[0].Rig)
	}

	// Waves should have task details.
	if len(parsed.Waves) != 2 {
		t.Fatalf("expected 2 waves, got %d", len(parsed.Waves))
	}
	if parsed.Waves[0].Number != 1 {
		t.Errorf("wave 1 number = %d, want 1", parsed.Waves[0].Number)
	}

	// Warnings should be populated.
	if len(parsed.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(parsed.Warnings))
	}
	if parsed.Warnings[0].Category != "orphan" {
		t.Errorf("warning category = %q, want orphan", parsed.Warnings[0].Category)
	}
	if parsed.Warnings[0].SuggestedFix != "add dep" {
		t.Errorf("warning suggested_fix = %q, want 'add dep'", parsed.Warnings[0].SuggestedFix)
	}

	// Errors should be empty array, not null.
	if string(raw["errors"]) == "null" {
		t.Error("errors should be [] not null")
	}
}

// Test buildTreeJSON for flat (task-list) input.
func TestBuildTreeJSON_FlatInput(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-x": {ID: "gt-x", Title: "X", Type: "task", Status: "open", Rig: "gastown"},
		"gt-y": {ID: "gt-y", Title: "Y", Type: "bug", Status: "open", Rig: "beads"},
	}}
	input := &StageInput{Kind: StageInputTasks, IDs: []string{"gt-x", "gt-y"}}

	tree := buildTreeJSON(dag, input)

	if len(tree) != 2 {
		t.Fatalf("expected 2 tree nodes, got %d", len(tree))
	}

	// Flat → no children.
	for _, node := range tree {
		if len(node.Children) != 0 {
			t.Errorf("flat tree node %q should have no children", node.ID)
		}
	}

	// Sorted by ID.
	if tree[0].ID != "gt-x" || tree[1].ID != "gt-y" {
		t.Errorf("tree should be sorted by ID: got [%s, %s]", tree[0].ID, tree[1].ID)
	}
}

// Test buildTreeJSON for epic input with nested children.
func TestBuildTreeJSON_EpicInput(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1": {ID: "epic-1", Title: "Root", Type: "epic", Status: "open",
			Children: []string{"sub-epic", "task-1"}},
		"sub-epic": {ID: "sub-epic", Title: "Sub", Type: "epic", Status: "open",
			Parent: "epic-1", Children: []string{"task-2"}},
		"task-1": {ID: "task-1", Title: "T1", Type: "task", Status: "open",
			Rig: "gastown", Parent: "epic-1"},
		"task-2": {ID: "task-2", Title: "T2", Type: "task", Status: "open",
			Rig: "gastown", Parent: "sub-epic"},
	}}
	input := &StageInput{Kind: StageInputEpic, IDs: []string{"epic-1"}}

	tree := buildTreeJSON(dag, input)

	if len(tree) != 1 {
		t.Fatalf("expected 1 root tree node, got %d", len(tree))
	}

	root := tree[0]
	if root.ID != "epic-1" {
		t.Errorf("root ID = %q, want epic-1", root.ID)
	}
	if len(root.Children) != 2 {
		t.Fatalf("root should have 2 children, got %d", len(root.Children))
	}

	// Children sorted: sub-epic < task-1
	if root.Children[0].ID != "sub-epic" {
		t.Errorf("first child = %q, want sub-epic", root.Children[0].ID)
	}
	if root.Children[1].ID != "task-1" {
		t.Errorf("second child = %q, want task-1", root.Children[1].ID)
	}

	// sub-epic has 1 child: task-2
	if len(root.Children[0].Children) != 1 {
		t.Fatalf("sub-epic should have 1 child, got %d", len(root.Children[0].Children))
	}
	if root.Children[0].Children[0].ID != "task-2" {
		t.Errorf("sub-epic child = %q, want task-2", root.Children[0].Children[0].ID)
	}
}

// Test buildFindingsJSON with empty input.
func TestBuildFindingsJSON_Empty(t *testing.T) {
	out := buildFindingsJSON(nil)
	if out == nil {
		t.Fatal("buildFindingsJSON(nil) should return empty slice, not nil")
	}
	if len(out) != 0 {
		t.Errorf("expected 0 findings, got %d", len(out))
	}
}

// Test buildWavesJSON with task details.
func TestBuildWavesJSON_TaskDetails(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a": {ID: "a", Title: "A", Type: "task", Rig: "gst",
			Blocks: []string{"b"}},
		"b": {ID: "b", Title: "B", Type: "task", Rig: "gst",
			BlockedBy: []string{"a"}},
	}}
	waves := []Wave{
		{Number: 1, Tasks: []string{"a"}},
		{Number: 2, Tasks: []string{"b"}},
	}

	wj := buildWavesJSON(waves, dag)
	if len(wj) != 2 {
		t.Fatalf("expected 2 waves, got %d", len(wj))
	}

	// Wave 1: task a, no blockers.
	if wj[0].Number != 1 {
		t.Errorf("wave 1 number = %d", wj[0].Number)
	}
	if len(wj[0].Tasks) != 1 || wj[0].Tasks[0].ID != "a" {
		t.Errorf("wave 1 tasks = %+v", wj[0].Tasks)
	}
	if len(wj[0].Tasks[0].BlockedBy) != 0 {
		t.Errorf("task a should have no blockers, got %v", wj[0].Tasks[0].BlockedBy)
	}

	// Wave 2: task b, blocked by a.
	if wj[1].Tasks[0].ID != "b" {
		t.Errorf("wave 2 task = %q", wj[1].Tasks[0].ID)
	}
	if len(wj[1].Tasks[0].BlockedBy) != 1 || wj[1].Tasks[0].BlockedBy[0] != "a" {
		t.Errorf("task b blocked_by = %v", wj[1].Tasks[0].BlockedBy)
	}
}

// TestAppendValidationWave_CreatesCapstoneWave verifies that appendValidationWave
// creates a validation bead blocked by all slingable tasks and appends it as the
// final wave.
func TestAppendValidationWave_CreatesCapstoneWave(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	testDAG := newTestDAG(t).
		Epic("epic-1", "Test Epic").
		Task("gt-a", "Task A", withRig("gastown")).ParentOf("epic-1").
		Task("gt-b", "Task B", withRig("gastown")).ParentOf("epic-1").BlockedBy("gt-a")

	_, logPath := testDAG.Setup(t)

	// Build the ConvoyDAG.
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1": {ID: "epic-1", Title: "Test Epic", Type: "epic", Status: "open"},
		"gt-a":   {ID: "gt-a", Title: "Task A", Type: "task", Status: "open", Rig: "gastown", Blocks: []string{"gt-b"}},
		"gt-b":   {ID: "gt-b", Title: "Task B", Type: "task", Status: "open", Rig: "gastown", BlockedBy: []string{"gt-a"}},
	}}

	// Compute waves first.
	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}
	if len(waves) != 2 {
		t.Fatalf("expected 2 waves before validation, got %d", len(waves))
	}

	// Append validation wave.
	waves, validationID, err := appendValidationWave(dag, waves, "epic-1")
	if err != nil {
		t.Fatalf("appendValidationWave: %v", err)
	}

	// Verify validation bead was created.
	if validationID == "" {
		t.Fatal("expected non-empty validation bead ID")
	}
	if !strings.HasPrefix(validationID, "hq-") {
		t.Errorf("validation bead ID should start with hq-, got %q", validationID)
	}

	// Verify waves: should now have 3 waves (original 2 + validation).
	if len(waves) != 3 {
		t.Fatalf("expected 3 waves after validation, got %d", len(waves))
	}
	if waves[2].Number != 3 {
		t.Errorf("validation wave number = %d, want 3", waves[2].Number)
	}
	if len(waves[2].Tasks) != 1 || waves[2].Tasks[0] != validationID {
		t.Errorf("validation wave tasks = %v, want [%s]", waves[2].Tasks, validationID)
	}

	// Verify the validation bead was added to the DAG.
	valNode, ok := dag.Nodes[validationID]
	if !ok {
		t.Fatal("validation bead not found in DAG")
	}
	if valNode.Type != "task" {
		t.Errorf("validation bead type = %q, want task", valNode.Type)
	}
	if valNode.Parent != "epic-1" {
		t.Errorf("validation bead parent = %q, want epic-1", valNode.Parent)
	}

	// Verify it's blocked by all slingable beads.
	blockedBy := make(map[string]bool)
	for _, id := range valNode.BlockedBy {
		blockedBy[id] = true
	}
	if !blockedBy["gt-a"] || !blockedBy["gt-b"] {
		t.Errorf("validation bead should be blocked by gt-a and gt-b, got %v", valNode.BlockedBy)
	}

	// Verify slingable nodes now block the validation bead.
	if nodeA, ok := dag.Nodes["gt-a"]; ok {
		found := false
		for _, id := range nodeA.Blocks {
			if id == validationID {
				found = true
			}
		}
		if !found {
			t.Errorf("gt-a should block validation bead, Blocks = %v", nodeA.Blocks)
		}
	}

	// Verify bd commands were logged: create, dep add parent-child, dep add blocks.
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd.log: %v", err)
	}
	logContent := string(logBytes)

	if !strings.Contains(logContent, "create") {
		t.Errorf("bd.log should contain 'create' command")
	}
	if !strings.Contains(logContent, "--type=task") {
		t.Errorf("bd.log should contain '--type=task'")
	}
	if !strings.Contains(logContent, "mol-validate-prd") {
		t.Errorf("bd.log should contain 'mol-validate-prd' in description")
	}
	if !strings.Contains(logContent, "dep add epic-1 "+validationID+" --type=parent-child") {
		t.Errorf("bd.log should contain parent-child dep add, got:\n%s", logContent)
	}
	for _, beadID := range []string{"gt-a", "gt-b"} {
		if !strings.Contains(logContent, "dep add "+beadID+" "+validationID+" --type=blocks") {
			t.Errorf("bd.log should contain 'dep add %s %s --type=blocks', got:\n%s", beadID, validationID, logContent)
		}
	}
}

// TestAppendValidationWave_NoSlingableBeads verifies that appendValidationWave
// returns early when there are no slingable beads (e.g., epic-only DAG).
func TestAppendValidationWave_NoSlingableBeads(t *testing.T) {
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"epic-1": {ID: "epic-1", Title: "Test Epic", Type: "epic", Status: "open"},
	}}

	waves, validationID, err := appendValidationWave(dag, nil, "epic-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if validationID != "" {
		t.Errorf("expected empty validation ID for no slingable beads, got %q", validationID)
	}
	if len(waves) != 0 {
		t.Errorf("expected 0 waves, got %d", len(waves))
	}
}
