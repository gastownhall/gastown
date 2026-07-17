package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

func setupPolecatCapacityTestTown(t *testing.T, maxPolecats int) string {
	t.Helper()
	townRoot := t.TempDir()
	configureScheduler(t, townRoot, maxPolecats, 1)
	if err := config.SaveRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"), &config.RigsConfig{Version: config.CurrentRigsVersion}); err != nil {
		t.Fatalf("SaveRigsConfig: %v", err)
	}
	return townRoot
}

func setupPolecatCapacityRig(t *testing.T, maxPolecats int) string {
	t.Helper()
	townRoot := t.TempDir()
	configureScheduler(t, townRoot, maxPolecats, 1)
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", "polecats"), 0755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}
	if err := config.SaveRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"), &config.RigsConfig{
		Version: config.CurrentRigsVersion,
		Rigs: map[string]config.RigEntry{
			"gastown": {GitURL: "https://example.invalid/gastown.git"},
		},
	}); err != nil {
		t.Fatalf("SaveRigsConfig: %v", err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	return townRoot
}

func TestCapacitySnapshotCleansStaleReservations(t *testing.T) {
	townRoot := setupPolecatCapacityTestTown(t, 1)
	dir := polecatAdmissionDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir reservations: %v", err)
	}
	stale := polecatAdmissionReservation{
		ID:        "stale",
		PID:       99999999,
		Rig:       "gastown",
		Bead:      "gt-stale",
		Operation: "test",
		CreatedAt: time.Now().Add(-2 * polecatAdmissionReservationTTL),
	}
	data, err := json.Marshal(stale)
	if err != nil {
		t.Fatalf("marshal stale reservation: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stale.json"), data, 0644); err != nil {
		t.Fatalf("write stale reservation: %v", err)
	}

	snapshot, err := polecatCapacitySnapshotForTown(townRoot)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Reservations != 0 || snapshot.Free != 1 {
		t.Fatalf("snapshot after stale cleanup = %+v, want reservations=0 free=1", snapshot)
	}
	if _, err := os.Stat(filepath.Join(dir, "stale.json")); !os.IsNotExist(err) {
		t.Fatalf("stale reservation still exists: %v", err)
	}
}

func TestCapacitySnapshotRemovesStructurallyInvalidReservations(t *testing.T) {
	townRoot := setupPolecatCapacityTestTown(t, 1)
	dir := polecatAdmissionDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir reservations: %v", err)
	}
	path := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(path, []byte(`{}`), 0644); err != nil {
		t.Fatalf("write invalid reservation: %v", err)
	}

	snapshot, err := polecatCapacitySnapshotForTown(townRoot)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Reservations != 0 || snapshot.Free != 1 {
		t.Fatalf("snapshot after invalid cleanup = %+v, want reservations=0 free=1", snapshot)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid reservation still exists: %v", err)
	}
}

func TestCapacitySnapshotRemovesMismatchedReservationFile(t *testing.T) {
	townRoot := setupPolecatCapacityTestTown(t, 1)
	dir := polecatAdmissionDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir reservations: %v", err)
	}
	reservation := polecatAdmissionReservation{
		ID:        "other",
		PID:       os.Getpid(),
		Rig:       "gastown",
		Bead:      "gt-mismatch",
		Operation: "test",
		CreatedAt: time.Now(),
	}
	data, err := json.Marshal(reservation)
	if err != nil {
		t.Fatalf("marshal reservation: %v", err)
	}
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write mismatched reservation: %v", err)
	}

	snapshot, err := polecatCapacitySnapshotForTown(townRoot)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Reservations != 0 || snapshot.Free != 1 {
		t.Fatalf("snapshot after mismatch cleanup = %+v, want reservations=0 free=1", snapshot)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("mismatched reservation still exists: %v", err)
	}
}

func TestCapacitySnapshotKeepsOldLiveReservation(t *testing.T) {
	townRoot := setupPolecatCapacityTestTown(t, 1)
	dir := polecatAdmissionDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir reservations: %v", err)
	}
	reservation := polecatAdmissionReservation{
		ID:        "live",
		PID:       os.Getpid(),
		Rig:       "gastown",
		Bead:      "gt-live",
		Operation: "test",
		CreatedAt: time.Now().Add(-2 * polecatAdmissionReservationTTL),
	}
	data, err := json.Marshal(reservation)
	if err != nil {
		t.Fatalf("marshal reservation: %v", err)
	}
	path := filepath.Join(dir, "live.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write live reservation: %v", err)
	}

	snapshot, err := polecatCapacitySnapshotForTown(townRoot)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Reservations != 1 || snapshot.Free != 0 {
		t.Fatalf("snapshot with old live reservation = %+v, want reservations=1 free=0", snapshot)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("live reservation should remain: %v", err)
	}
}

func TestAcquirePolecatAdmissionUsesConfiguredCap(t *testing.T) {
	townRoot := setupPolecatCapacityTestTown(t, 1)

	first, snapshot, err := acquirePolecatAdmission(townRoot, "gastown", "gt-one", "test")
	if err != nil {
		t.Fatalf("first admission: %v", err)
	}
	defer first.Release()
	if snapshot.Max != 1 || snapshot.Reservations != 1 || snapshot.Free != 0 {
		t.Fatalf("snapshot after first admission = %+v, want max=1 reservations=1 free=0", snapshot)
	}

	second, deniedSnapshot, err := acquirePolecatAdmission(townRoot, "gastown", "gt-two", "test")
	if second != nil {
		defer second.Release()
	}
	var admissionErr *polecatCapacityAdmissionError
	if !errors.As(err, &admissionErr) {
		t.Fatalf("second admission error = %v, want polecatCapacityAdmissionError", err)
	}
	if deniedSnapshot.Max != 1 || deniedSnapshot.Reservations != 1 || deniedSnapshot.Free != 0 {
		t.Fatalf("denied snapshot = %+v, want max=1 reservations=1 free=0", deniedSnapshot)
	}
	if !strings.Contains(err.Error(), "scheduler.max_polecats") {
		t.Fatalf("denial error %q should mention scheduler.max_polecats", err.Error())
	}

	first.Release()
	third, snapshot, err := acquirePolecatAdmission(townRoot, "gastown", "gt-three", "test")
	if err != nil {
		t.Fatalf("third admission after release: %v", err)
	}
	defer third.Release()
	if snapshot.Max != 1 || snapshot.Reservations != 1 || snapshot.Free != 0 {
		t.Fatalf("snapshot after third admission = %+v, want max=1 reservations=1 free=0", snapshot)
	}
}

func TestAcquirePolecatAdmissionDisabledWhenSchedulerCapNonPositive(t *testing.T) {
	for _, maxPolecats := range []int{-1, 0} {
		t.Run("max", func(t *testing.T) {
			townRoot := t.TempDir()
			configureScheduler(t, townRoot, maxPolecats, 1)

			handle, snapshot, err := acquirePolecatAdmission(townRoot, "gastown", "gt-one", "test")
			if err != nil {
				t.Fatalf("admission with max=%d: %v", maxPolecats, err)
			}
			defer handle.Release()
			if !handle.disabled {
				t.Fatalf("admission handle should be disabled for max=%d", maxPolecats)
			}
			if snapshot.Max != maxPolecats {
				t.Fatalf("snapshot max = %d, want %d", snapshot.Max, maxPolecats)
			}
			if _, err := os.Stat(polecatAdmissionDir(townRoot)); !os.IsNotExist(err) {
				t.Fatalf("reservation dir exists for disabled admission: %v", err)
			}
		})
	}
}

func TestConcurrentPolecatAdmissionReservationsDoNotExceedCap(t *testing.T) {
	townRoot := setupPolecatCapacityTestTown(t, 1)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var handles []*polecatAdmissionHandle
	successes := 0
	denials := 0

	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			handle, _, err := acquirePolecatAdmission(townRoot, "gastown", "gt-race", "test")
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
				handles = append(handles, handle)
				return
			}
			var admissionErr *polecatCapacityAdmissionError
			if errors.As(err, &admissionErr) || strings.Contains(err.Error(), "admission is busy") {
				denials++
				return
			}
			t.Errorf("unexpected admission error: %v", err)
		}()
	}
	close(start)
	wg.Wait()
	for _, handle := range handles {
		handle.Release()
	}

	if successes != 1 {
		t.Fatalf("successful admissions = %d, want 1", successes)
	}
	if denials != 5 {
		t.Fatalf("denied admissions = %d, want 5", denials)
	}
}

func TestApplyAgentFieldsToCapacitySnapshotSeparatesPendingMR(t *testing.T) {
	tests := []struct {
		name       string
		fields     *beads.AgentFields
		activeWork *beads.Issue
		want       polecatCapacitySnapshot
	}{
		{
			name:   "active mr is pending capacity",
			fields: &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: "clean", ActiveMR: "gt-mr-open"},
			want:   polecatCapacitySnapshot{PendingMR: 1},
		},
		{
			name:   "push failed remains recovery blocked",
			fields: &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: "clean", ActiveMR: "gt-mr-open", PushFailed: true},
			want:   polecatCapacitySnapshot{RecoveryBlocked: 1, capacityUsed: 1},
		},
		{
			name:   "clean idle is reusable",
			fields: &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: "clean"},
			want:   polecatCapacitySnapshot{ReusableIdle: 1},
		},
		{
			name:       "active work consumes capacity",
			fields:     &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: "clean"},
			activeWork: &beads.Issue{ID: "gt-work", Status: string(beads.StatusOpen), Assignee: "gastown/polecats/synth"},
			want:       polecatCapacitySnapshot{RecoveryBlocked: 1, capacityUsed: 1},
		},
		{
			name:       "deferred work blocks recovery without capacity",
			fields:     &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: "clean"},
			activeWork: &beads.Issue{ID: "gt-paused", Status: string(beads.StatusDeferred), Assignee: "gastown/polecats/synth"},
			want:       polecatCapacitySnapshot{RecoveryBlocked: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := polecatCapacitySnapshot{}
			applyAgentFieldsToCapacitySnapshot(&snapshot, "gastown", "synth", tt.fields, tt.activeWork, nil)
			if snapshot.Working != tt.want.Working || snapshot.RecoveryBlocked != tt.want.RecoveryBlocked || snapshot.ReusableIdle != tt.want.ReusableIdle || snapshot.PendingMR != tt.want.PendingMR || snapshot.capacityUsed != tt.want.capacityUsed {
				t.Fatalf("snapshot = %+v, want %+v", snapshot, tt.want)
			}
		})
	}
}

func TestCapacitySnapshotRecoveryBlockedDoesNotAlwaysConsumeFreeCapacity(t *testing.T) {
	snapshot := polecatCapacitySnapshot{Max: 3}
	applyWorkstateDispositionToCapacitySnapshot(&snapshot, polecat.StateIdle, polecat.WorkstateDisposition{
		Verdict:              polecat.WorkstateVerdictNeedsRecovery,
		NeedsRecovery:        true,
		CountsTowardCapacity: false,
	})
	applyWorkstateDispositionToCapacitySnapshot(&snapshot, polecat.StateStalled, polecat.WorkstateDisposition{
		Verdict:              polecat.WorkstateVerdictNeedsRecovery,
		NeedsRecovery:        true,
		CountsTowardCapacity: true,
	})
	snapshot.Free = snapshot.Max - snapshot.occupied()

	if snapshot.RecoveryBlocked != 2 || snapshot.capacityUsed != 1 || snapshot.Free != 2 {
		t.Fatalf("snapshot = %+v, want recovery=2 capacityUsed=1 free=2", snapshot)
	}
}

func TestPrintDryRunPlanUsesCapacitySnapshot(t *testing.T) {
	out := captureStdout(t, func() {
		printDryRunPlan(capacity.DispatchPlan{
			ToDispatch: []capacity.PendingBead{{ID: "ctx-1", WorkBeadID: "gt-one", TargetRig: "gastown"}},
			Skipped:    2,
			Reason:     "capacity",
		}, polecatCapacitySnapshot{
			Max:             2,
			Working:         1,
			RecoveryBlocked: 1,
			Reservations:    0,
			ReusableIdle:    3,
			PendingMR:       2,
			Free:            0,
		}, 5)
	})
	for _, want := range []string{"0 free of 2", "working: 1", "recovery_blocked: 1", "reusable_idle: 3", "pending_mr: 2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output %q missing %q", out, want)
		}
	}
}

func TestPrintDryRunPlanValidationReasonNotCapacity(t *testing.T) {
	out := captureStdout(t, func() {
		printDryRunPlan(capacity.DispatchPlan{
			Skipped: 2,
			Reason:  "validation",
		}, polecatCapacitySnapshot{Max: 2, Free: 2}, 5)
	})
	if !strings.Contains(out, "validation failed for 2 candidate") {
		t.Fatalf("dry-run output %q missing validation reason", out)
	}
	if strings.Contains(out, "No capacity") {
		t.Fatalf("dry-run output %q should not report capacity for validation failures", out)
	}
}

func TestPrintDispatchNoOpReportsExplicitReason(t *testing.T) {
	out := captureStdout(t, func() {
		printDispatchNoOp(capacity.DispatchReport{Reason: "none"}, polecatCapacitySnapshot{})
	})
	if !strings.Contains(out, "No ready beads scheduled for dispatch") {
		t.Fatalf("none output = %q", out)
	}

	out = captureStdout(t, func() {
		printDispatchNoOp(capacity.DispatchReport{Reason: "validation", Skipped: 1}, polecatCapacitySnapshot{})
	})
	if !strings.Contains(out, "No dispatchable beads") || !strings.Contains(out, "validation") {
		t.Fatalf("validation output = %q", out)
	}
}

func TestResolveTargetRigPassesHeldAdmissionToSpawn(t *testing.T) {
	townRoot := setupPolecatCapacityRig(t, 1)
	oldSpawn := spawnPolecatForSling
	t.Cleanup(func() { spawnPolecatForSling = oldSpawn })
	called := false
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		called = true
		if rigName != "gastown" {
			t.Fatalf("rigName = %q, want gastown", rigName)
		}
		if !opts.SkipAdmission {
			t.Fatal("spawn should skip admission when caller already holds reservation")
		}
		if opts.TownRoot != townRoot {
			t.Fatalf("TownRoot = %q, want %q", opts.TownRoot, townRoot)
		}
		return &SpawnedPolecatInfo{
			RigName:     "gastown",
			PolecatName: "toast",
			ClonePath:   filepath.Join(townRoot, "gastown", "polecats", "toast", "gastown"),
			SessionName: "gt-gastown-polecat-toast",
		}, nil
	}

	resolved, err := resolveTarget("gastown", ResolveTargetOptions{
		TownRoot:             townRoot,
		SkipPolecatAdmission: true,
		NoBoot:               true,
	})
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if !called {
		t.Fatal("spawnPolecatForSling was not called")
	}
	if resolved.Agent != "gastown/polecats/toast" {
		t.Fatalf("resolved agent = %q, want gastown/polecats/toast", resolved.Agent)
	}
}

func TestStandaloneFormulaRigTargetAcquiresSingleAdmission(t *testing.T) {
	townRoot := setupPolecatCapacityRig(t, 1)
	oldAcquire := acquirePolecatAdmissionFn
	oldSpawn := spawnPolecatForSling
	oldFind := findHookedFormulaSingletonFn
	oldDryRun, oldNoBoot := slingDryRun, slingNoBoot
	t.Cleanup(func() {
		acquirePolecatAdmissionFn = oldAcquire
		spawnPolecatForSling = oldSpawn
		findHookedFormulaSingletonFn = oldFind
		slingDryRun, slingNoBoot = oldDryRun, oldNoBoot
	})
	slingDryRun = false
	slingNoBoot = true
	admissions := 0
	acquirePolecatAdmissionFn = func(townRootArg, rigName, beadID, operation string) (*polecatAdmissionHandle, polecatCapacitySnapshot, error) {
		admissions++
		if townRootArg != townRoot || rigName != "gastown" || beadID != "test-formula" || operation != "formula" {
			t.Fatalf("admission args = (%q,%q,%q,%q)", townRootArg, rigName, beadID, operation)
		}
		return &polecatAdmissionHandle{disabled: true}, polecatCapacitySnapshot{Max: 1, Free: 0}, nil
	}
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		if !opts.SkipAdmission {
			t.Fatal("formula rig spawn should use caller-held admission")
		}
		return &SpawnedPolecatInfo{
			RigName:     "gastown",
			PolecatName: "toast",
			ClonePath:   filepath.Join(townRoot, "gastown", "polecats", "toast", "gastown"),
			SessionName: "gt-gastown-polecat-toast",
		}, nil
	}
	findHookedFormulaSingletonFn = func(workDir, targetAgent, formulaName string) (*beads.Issue, error) {
		return &beads.Issue{ID: "gt-wisp-existing"}, nil
	}

	if err := runSlingFormula(context.Background(), []string{"test-formula", "gastown"}); err != nil {
		t.Fatalf("runSlingFormula: %v", err)
	}
	if admissions != 1 {
		t.Fatalf("admissions = %d, want 1", admissions)
	}
}

func TestStandaloneFormulaExistingPolecatNoopDoesNotRequireCapacity(t *testing.T) {
	townRoot := setupPolecatCapacityRig(t, 1)
	oldAcquire := acquirePolecatAdmissionFn
	oldResolve := resolveTargetAgentFn
	oldFind := findHookedFormulaSingletonFn
	oldDryRun := slingDryRun
	t.Cleanup(func() {
		acquirePolecatAdmissionFn = oldAcquire
		resolveTargetAgentFn = oldResolve
		findHookedFormulaSingletonFn = oldFind
		slingDryRun = oldDryRun
	})
	slingDryRun = false
	acquirePolecatAdmissionFn = func(townRootArg, rigName, beadID, operation string) (*polecatAdmissionHandle, polecatCapacitySnapshot, error) {
		t.Fatalf("no-op existing formula should not acquire capacity, got (%q,%q,%q,%q)", townRootArg, rigName, beadID, operation)
		return nil, polecatCapacitySnapshot{}, nil
	}
	resolveTargetAgentFn = func(target string) (string, string, string, error) {
		if target != "gastown/polecats/toast" {
			t.Fatalf("target = %q, want gastown/polecats/toast", target)
		}
		return "gastown/polecats/toast", "%1", filepath.Join(townRoot, "gastown", "polecats", "toast", "gastown"), nil
	}
	findHookedFormulaSingletonFn = func(workDir, targetAgent, formulaName string) (*beads.Issue, error) {
		return &beads.Issue{ID: "gt-wisp-existing"}, nil
	}

	if err := runSlingFormula(context.Background(), []string{"test-formula", "gastown/polecats/toast"}); err != nil {
		t.Fatalf("runSlingFormula: %v", err)
	}
}
