package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

func TestDispatchScheduledWorkReportsHeldLock(t *testing.T) {
	townRoot := t.TempDir()
	runtimeDir := filepath.Join(townRoot, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatalf("mkdir runtime: %v", err)
	}
	lockFile := filepath.Join(runtimeDir, "scheduler-dispatch.lock")
	lock := flock.New(lockFile)
	locked, err := lock.TryLock()
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if !locked {
		t.Fatal("test could not acquire scheduler dispatch lock")
	}
	t.Cleanup(func() { _ = lock.Unlock() })

	_, err = dispatchScheduledWork(townRoot, "test", 1, false)
	if err == nil {
		t.Fatal("dispatchScheduledWork succeeded with held scheduler lock")
	}
	if !strings.Contains(err.Error(), "scheduler dispatch already in progress") || !strings.Contains(err.Error(), lockFile) {
		t.Fatalf("error = %q, want explicit held lock reason with path", err.Error())
	}
}

func TestPrintDryRunPlanValidationReasonNotCapacity(t *testing.T) {
	out := captureStdout(t, func() {
		printDryRunPlan(capacity.DispatchPlan{
			Skipped: 1,
			Reason:  "validation",
		}, polecatCapacitySnapshot{Max: 2, Free: 2}, 1)
	})
	if !strings.Contains(out, "validation failed") {
		t.Fatalf("dry-run output %q missing validation reason", out)
	}
	if strings.Contains(out, "No capacity") {
		t.Fatalf("dry-run output %q should not report validation as capacity", out)
	}
}

func TestBuildSchedulerDispatchPlanNoCleanupDoesNotDeleteReservations(t *testing.T) {
	townRoot := t.TempDir()
	configureScheduler(t, townRoot, 1, 1)
	writeJSONFile(t, filepath.Join(townRoot, "mayor", "rigs.json"), &config.RigsConfig{
		Version: config.CurrentRigsVersion,
		Rigs:    map[string]config.RigEntry{},
	})

	reservation := polecatAdmissionReservation{
		ID:        "dead-reservation",
		PID:       99999999,
		Operation: "test",
		CreatedAt: time.Now().Add(-polecatAdmissionReservationTTL - time.Hour),
	}
	reservationDir := polecatAdmissionDir(townRoot)
	if err := os.MkdirAll(reservationDir, 0755); err != nil {
		t.Fatalf("mkdir reservation dir: %v", err)
	}
	reservationPath := filepath.Join(reservationDir, reservation.ID+".json")
	data, err := json.Marshal(reservation)
	if err != nil {
		t.Fatalf("marshal reservation: %v", err)
	}
	if err := os.WriteFile(reservationPath, data, 0644); err != nil {
		t.Fatalf("write reservation: %v", err)
	}

	if _, err := buildSchedulerDispatchPlan(townRoot, 0, false); err != nil {
		t.Fatalf("buildSchedulerDispatchPlan cleanup=false: %v", err)
	}
	if _, err := os.Stat(reservationPath); err != nil {
		t.Fatalf("cleanup=false should not delete stale reservation: %v", err)
	}
}

func TestListBlockedWorkBeadIDsFailsClosedOnPartialQueryFailure(t *testing.T) {
	townRoot := t.TempDir()
	townBeadsDir := filepath.Join(townRoot, ".beads")
	rigDir := filepath.Join(townRoot, "rig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	for _, dir := range []string{townBeadsDir, rigBeadsDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := beads.WriteRoutes(townBeadsDir, []beads.Route{
		{Prefix: "hq-", Path: "."},
		{Prefix: "rg-", Path: "rig"},
	}); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir fake bd bin: %v", err)
	}
	fakeBD := filepath.Join(binDir, "bd")
	if err := os.WriteFile(fakeBD, []byte(`#!/bin/sh
case "$BEADS_DIR" in
  */rig/.beads) echo "blocked query failed" >&2; exit 7 ;;
  *) printf '[]\n'; exit 0 ;;
esac
`), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := listBlockedWorkBeadIDsWithError(townRoot, []string{"hq-one", "rg-two"})
	if err == nil {
		t.Fatal("partial bd blocked failure should fail closed")
	}
	if !strings.Contains(err.Error(), "refusing to mark scheduled work ready") {
		t.Fatalf("error = %q, want fail-closed blocked-query reason", err.Error())
	}
}

func TestListBlockedWorkBeadIDsFailsClosedOnMalformedJSON(t *testing.T) {
	townRoot := t.TempDir()
	townBeadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir town beads dir: %v", err)
	}
	if err := beads.WriteRoutes(townBeadsDir, []beads.Route{{Prefix: "hq-", Path: "."}}); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir fake bd bin: %v", err)
	}
	fakeBD := filepath.Join(binDir, "bd")
	if err := os.WriteFile(fakeBD, []byte(`#!/bin/sh
printf 'not-json\n'
exit 0
`), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := listBlockedWorkBeadIDsWithError(townRoot, []string{"hq-one"})
	if err == nil {
		t.Fatal("malformed bd blocked JSON should fail closed")
	}
	if !strings.Contains(err.Error(), "refusing to mark scheduled work ready") {
		t.Fatalf("error = %q, want fail-closed blocked-query reason", err.Error())
	}
}

func TestScheduledBeadInfoFromWorkSkipsNonConcreteWork(t *testing.T) {
	fields := &capacity.SlingContextFields{WorkBeadID: "gt-wisp-abc", TargetRig: "gastown"}
	info := beadStatusInfo{Status: "open", IssueType: "task", Labels: []string{"gt:sling-context"}}
	if _, ok := scheduledBeadInfoFromWork("context", fields, info, true, nil); ok {
		t.Fatalf("scheduledBeadInfoFromWork accepted non-concrete work")
	}

	fields.WorkBeadID = "gt-task"
	info = beadStatusInfo{Status: "open", Title: "Concrete task", IssueType: "task"}
	got, ok := scheduledBeadInfoFromWork("context", fields, info, true, nil)
	if !ok {
		t.Fatalf("scheduledBeadInfoFromWork rejected concrete work")
	}
	if got.ID != "gt-task" || got.Title != "Concrete task" || got.TargetRig != "gastown" || got.Blocked {
		t.Fatalf("scheduledBeadInfoFromWork() = %+v, want concrete unblocked scheduled bead", got)
	}
}
