package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofrs/flock"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

func installFakeBD(t *testing.T, script string) {
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
}

func setupSchedulerScanFailureTown(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()
	for _, dir := range []string{
		filepath.Join(townRoot, "mayor"),
		filepath.Join(townRoot, ".beads"),
		filepath.Join(townRoot, "rig", ".beads"),
	} {
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
	return townRoot
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

func TestValidateDryRunDispatchPlanMarksAllInvalidAsValidation(t *testing.T) {
	townRoot := t.TempDir()
	writeJSONFile(t, filepath.Join(townRoot, "mayor", "rigs.json"), &config.RigsConfig{
		Version: config.CurrentRigsVersion,
		Rigs: map[string]config.RigEntry{
			"testrig": {BeadsConfig: &config.BeadsConfig{Prefix: "gt"}},
		},
	})

	plan := validateDryRunDispatchPlan(townRoot, capacity.DispatchPlan{
		ToDispatch: []capacity.PendingBead{{ID: "ctx-1", WorkBeadID: "hq-one", TargetRig: "testrig"}},
		Reason:     "ready",
	})

	if len(plan.ToDispatch) != 0 || plan.Skipped != 1 || plan.Reason != "validation" {
		t.Fatalf("validated plan = %+v, want no dispatch, skipped=1, reason=validation", plan)
	}
}

func TestListAllSlingContextRecordsFailsOnPartialScanFailure(t *testing.T) {
	townRoot := setupSchedulerScanFailureTown(t)

	_, err := listAllSlingContextRecords(townRoot)
	if err == nil {
		t.Fatal("partial sling-context scan failure should fail closed")
	}
	if !strings.Contains(err.Error(), "listing sling contexts") || !strings.Contains(err.Error(), filepath.Join("rig", ".beads")) {
		t.Fatalf("error = %q, want explicit context scan failure", err.Error())
	}
}

func TestAreScheduledFailsClosedOnContextScanFailure(t *testing.T) {
	townRoot := setupSchedulerScanFailureTown(t)
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

func TestRunSchedulerClearFailsOnContextScanFailure(t *testing.T) {
	townRoot := setupSchedulerScanFailureTown(t)
	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	oldClearBead := schedulerClearBead
	schedulerClearBead = ""
	t.Cleanup(func() { schedulerClearBead = oldClearBead })

	err = runSchedulerClear(nil, nil)
	if err == nil {
		t.Fatal("scheduler clear succeeded with incomplete context scan")
	}
	if !strings.Contains(err.Error(), "listing sling contexts") {
		t.Fatalf("error = %q, want sling context scan failure", err.Error())
	}
}
