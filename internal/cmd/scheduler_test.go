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

func installFakeBD(t *testing.T, script string) string {
	t.Helper()
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir fake bd bin: %v", err)
	}
	fakeBD := filepath.Join(binDir, "bd")
	if err := os.WriteFile(fakeBD, []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return fakeBD
}

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
	installFakeBD(t, `#!/bin/sh
case "$1" in
  query) printf '[]\n'; exit 0 ;;
esac
printf '[]\n'
`)
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

func TestBuildSchedulerDispatchPlanRejectsInvalidBatchSize(t *testing.T) {
	townRoot := t.TempDir()
	configureScheduler(t, townRoot, 1, 0)
	if _, err := buildSchedulerDispatchPlan(townRoot, 0, false); err == nil || !strings.Contains(err.Error(), "invalid scheduler batch_size 0") {
		t.Fatalf("buildSchedulerDispatchPlan batch_size=0 error = %v, want invalid batch_size", err)
	}

	configureScheduler(t, townRoot, 1, 1)
	if _, err := buildSchedulerDispatchPlan(townRoot, -1, false); err == nil || !strings.Contains(err.Error(), "invalid scheduler batch override -1") {
		t.Fatalf("buildSchedulerDispatchPlan batch override -1 error = %v, want invalid batch override", err)
	}
}

func TestListAllSlingContextRecordsFailsClosedOnPartialScanFailure(t *testing.T) {
	townRoot := t.TempDir()
	for _, dir := range []string{filepath.Join(townRoot, ".beads"), filepath.Join(townRoot, "rig", ".beads")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	installFakeBD(t, `#!/bin/sh
case "$BEADS_DIR" in
  */rig/.beads) echo "scan failed" >&2; exit 7 ;;
  *) printf '[]\n'; exit 0 ;;
esac
`)

	_, err := listAllSlingContextRecords(townRoot)
	if err == nil {
		t.Fatal("partial sling-context scan failure should fail closed")
	}
	if !strings.Contains(err.Error(), "listing sling contexts") || !strings.Contains(err.Error(), filepath.Join("rig", ".beads")) {
		t.Fatalf("error = %q, want explicit context scan failure", err.Error())
	}
}

func TestAreScheduledFailsClosedOnContextScanFailure(t *testing.T) {
	townRoot := t.TempDir()
	for _, dir := range []string{filepath.Join(townRoot, "mayor"), filepath.Join(townRoot, ".beads")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	installFakeBD(t, `#!/bin/sh
echo "scan failed" >&2
exit 7
`)

	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })

	got := areScheduled([]string{"gt-one", "gt-two"})
	if !got["gt-one"] || !got["gt-two"] {
		t.Fatalf("areScheduled on scan failure = %+v, want all requested IDs marked scheduled", got)
	}
}

func TestBatchFetchBeadInfoByIDsFailsClosedOnShowFailure(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	installFakeBD(t, `#!/bin/sh
case " $* " in
  *" show "*) echo "show failed" >&2; exit 7 ;;
esac
printf '[]\n'
`)

	_, err := batchFetchBeadInfoByIDs(townRoot, []string{"gt-one"})
	if err == nil {
		t.Fatal("bd show failure should fail closed")
	}
	if !strings.Contains(err.Error(), "bd show failed") {
		t.Fatalf("error = %q, want explicit bd show failure", err.Error())
	}
}

func TestBatchFetchBeadInfoByIDsFailsClosedOnMalformedJSON(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	installFakeBD(t, `#!/bin/sh
case " $* " in
  *" show "*) printf 'not-json\n'; exit 0 ;;
esac
printf '[]\n'
`)

	_, err := batchFetchBeadInfoByIDs(townRoot, []string{"gt-one"})
	if err == nil {
		t.Fatal("malformed bd show JSON should fail closed")
	}
	if !strings.Contains(err.Error(), "parsing bd show output") {
		t.Fatalf("error = %q, want explicit bd show parse failure", err.Error())
	}
}

func TestBatchFetchBeadInfoByIDsMissingIDIsNotReady(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	installFakeBD(t, `#!/bin/sh
case " $* " in
  *" show "*) printf '[]\n'; exit 0 ;;
esac
printf '[]\n'
`)

	info, err := batchFetchBeadInfoByIDs(townRoot, []string{"gt-missing"})
	if err != nil {
		t.Fatalf("batchFetchBeadInfoByIDs missing ID error = %v, want nil", err)
	}
	if len(info) != 0 {
		t.Fatalf("batchFetchBeadInfoByIDs missing ID = %+v, want empty map", info)
	}
	fields := &capacity.SlingContextFields{WorkBeadID: "gt-missing", TargetRig: "gastown"}
	bead, ok := scheduledBeadInfoFromWork("context", fields, beadStatusInfo{}, false, nil)
	if !ok || !bead.Blocked {
		t.Fatalf("scheduledBeadInfoFromWork missing ID = %+v ok=%v, want displayed as not ready", bead, ok)
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

func TestListBlockedWorkBeadIDsFailsClosedOnMalformedRows(t *testing.T) {
	townRoot := t.TempDir()
	townBeadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir town beads dir: %v", err)
	}
	if err := beads.WriteRoutes(townBeadsDir, []beads.Route{{Prefix: "hq-", Path: "."}}); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	installFakeBD(t, `#!/bin/sh
printf '[{"issue_id":"hq-blocked"}]\n'
exit 0
`)

	_, err := listBlockedWorkBeadIDsWithError(townRoot, []string{"hq-one"})
	if err == nil {
		t.Fatal("malformed bd blocked row should fail closed")
	}
	if !strings.Contains(err.Error(), "refusing to mark scheduled work ready") || !strings.Contains(err.Error(), "missing id") {
		t.Fatalf("error = %q, want fail-closed malformed-row reason", err.Error())
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
