package refinery

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	gitpkg "github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/testutil"
)

func TestNewEngineer_SlingConflictTaskInitialized(t *testing.T) {
	r := &rig.Rig{Name: "test-rig", Path: t.TempDir()}
	e := NewEngineer(r)
	if e.slingConflictTask == nil {
		t.Error("expected slingConflictTask to be initialized by NewEngineer")
	}
}

func setupConflictTaskTestEngineer(t *testing.T) *Engineer {
	t.Helper()
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())

	tmpDir := t.TempDir()
	rigPath := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("mkdir rigPath: %v", err)
	}

	b := beads.NewIsolatedWithPort(rigPath, port)
	if err := b.Init("gt"); err != nil {
		t.Skipf("bd init unavailable in test environment: %v", err)
	}

	// Use a bare directory for git — Rev() fails gracefully with "unknown-sha".
	gitDir := filepath.Join(tmpDir, "gitrepo")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatalf("mkdir gitDir: %v", err)
	}

	return &Engineer{
		rig:    &rig.Rig{Name: "testrig", Path: rigPath},
		beads:  b,
		git:    gitpkg.NewGit(gitDir),
		output: &bytes.Buffer{},
		mergeSlotEnsureExists: func() (string, error) {
			return "test-slot", nil
		},
		mergeSlotAcquire: func(holder string, _ bool) (*beads.MergeSlotStatus, error) {
			return &beads.MergeSlotStatus{Available: true, Holder: holder}, nil
		},
		mergeSlotRelease: func(_ string) error { return nil },
	}
}

// TestCreateConflictResolutionTaskForMR_AutoSlingsTask verifies that the
// refinery calls slingConflictTask immediately after creating the conflict
// resolution task, dispatching it without manual intervention.
func TestCreateConflictResolutionTaskForMR_AutoSlingsTask(t *testing.T) {
	e := setupConflictTaskTestEngineer(t)

	var slingedTaskID, slingedRig string
	e.slingConflictTask = func(taskID, rigName string) error {
		slingedTaskID = taskID
		slingedRig = rigName
		return nil
	}

	mr := &MRInfo{
		ID:          "gt-testmr",
		Branch:      "polecat/test/gt-abc",
		Target:      "main",
		SourceIssue: "gt-abc",
		Priority:    2,
	}

	taskID, err := e.createConflictResolutionTaskForMR(mr, ProcessResult{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if taskID == "" {
		t.Fatal("expected non-empty task ID")
	}
	if slingedTaskID == "" {
		t.Fatal("slingConflictTask was not called after task creation")
	}
	if slingedTaskID != taskID {
		t.Errorf("slingConflictTask called with task %q, want %q", slingedTaskID, taskID)
	}
	if slingedRig != "testrig" {
		t.Errorf("slingConflictTask called with rig %q, want %q", slingedRig, "testrig")
	}

	out := e.output.(*bytes.Buffer).String()
	if !strings.Contains(out, "Auto-slung") {
		t.Errorf("expected output to contain 'Auto-slung', got: %s", out)
	}
}

// TestCreateConflictResolutionTaskForMR_SlingFailureNonFatal verifies that a
// sling failure is logged as a warning but does not propagate as an error,
// preserving the task ID return so the MR can still be blocked on the task.
func TestCreateConflictResolutionTaskForMR_SlingFailureNonFatal(t *testing.T) {
	e := setupConflictTaskTestEngineer(t)

	e.slingConflictTask = func(_, _ string) error {
		return io.ErrUnexpectedEOF // simulated sling failure
	}

	mr := &MRInfo{
		ID:     "gt-testmr2",
		Branch: "polecat/test/gt-def",
		Target: "main",
	}

	taskID, err := e.createConflictResolutionTaskForMR(mr, ProcessResult{})
	if err != nil {
		t.Fatalf("sling failure must be non-fatal; got error: %v", err)
	}
	if taskID == "" {
		t.Fatal("expected task ID even when sling fails")
	}

	out := e.output.(*bytes.Buffer).String()
	if !strings.Contains(out, "Warning: failed to auto-sling") {
		t.Errorf("expected warning in output, got: %s", out)
	}
}

// TestCreateConflictResolutionTaskForMR_NilSlingFnSkipped verifies that when
// slingConflictTask is nil (e.g., in tests that construct Engineer directly),
// createConflictResolutionTaskForMR completes without panicking.
func TestCreateConflictResolutionTaskForMR_NilSlingFnSkipped(t *testing.T) {
	e := setupConflictTaskTestEngineer(t)
	e.slingConflictTask = nil

	mr := &MRInfo{
		ID:     "gt-testmr3",
		Branch: "polecat/test/gt-ghi",
		Target: "main",
	}

	taskID, err := e.createConflictResolutionTaskForMR(mr, ProcessResult{})
	if err != nil {
		t.Fatalf("nil slingConflictTask must not cause error; got: %v", err)
	}
	if taskID == "" {
		t.Fatal("expected task ID when slingConflictTask is nil")
	}
}
