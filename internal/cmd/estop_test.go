package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/estop"
)

func setupEstopCommandTestTown(t *testing.T) string {
	t.Helper()

	townRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("create mayor marker: %v", err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir test town: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})

	return townRoot
}

func TestEstopCmdRejectsUnexpectedArgs(t *testing.T) {
	if estopCmd.Args == nil {
		t.Fatal("estopCmd.Args is nil")
	}
	if err := estopCmd.Args(estopCmd, []string{"junk"}); err == nil {
		t.Fatal("estopCmd should reject unexpected positional args")
	}
	if err := estopCmd.Args(estopCmd, nil); err != nil {
		t.Fatalf("estopCmd should accept no positional args: %v", err)
	}
}

func TestRunEstopStatusDoesNotCreateSentinel(t *testing.T) {
	townRoot := setupEstopCommandTestTown(t)

	var runErr error
	out := captureStdout(t, func() {
		runErr = runEstopStatus(estopStatusCmd, nil)
	})
	if runErr != nil {
		t.Fatalf("runEstopStatus: %v", runErr)
	}
	if !strings.Contains(out, "No E-stop active.") {
		t.Fatalf("status output = %q, want no-active message", out)
	}
	if _, err := os.Stat(estop.FilePath(townRoot)); !os.IsNotExist(err) {
		t.Fatalf("status should not create town-wide ESTOP sentinel, stat err = %v", err)
	}
}

func TestRunEstopStatusReportsPerRigEstop(t *testing.T) {
	townRoot := setupEstopCommandTestTown(t)
	if err := estop.ActivateRig(townRoot, "gastown", estop.TriggerManual, "maintenance"); err != nil {
		t.Fatalf("ActivateRig: %v", err)
	}

	var runErr error
	out := captureStdout(t, func() {
		runErr = runEstopStatus(estopStatusCmd, nil)
	})
	if runErr != nil {
		t.Fatalf("runEstopStatus: %v", runErr)
	}
	for _, want := range []string{"E-STOP: gastown", "maintenance", "Clear with:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output = %q, want %q", out, want)
		}
	}
	if _, err := os.Stat(estop.FilePath(townRoot)); !os.IsNotExist(err) {
		t.Fatalf("status should not create town-wide ESTOP sentinel, stat err = %v", err)
	}
}
