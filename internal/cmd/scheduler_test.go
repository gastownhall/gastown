package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofrs/flock"
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
