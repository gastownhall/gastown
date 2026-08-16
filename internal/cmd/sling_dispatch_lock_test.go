package cmd

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/sling"
)

// TestExecuteSling_AcquiresBeadLock verifies that executeSling acquires the
// per-bead flock before reading bead status. This prevents TOCTOU races where
// multiple batch/queue dispatch calls read status=open concurrently.
func TestExecuteSling_AcquiresBeadLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("advisory flock is a no-op on Windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	beadID := "gt-locktest1"

	// Hold the flock from outside executeSling — this simulates a concurrent dispatch.
	release, err := tryAcquireSlingBeadLock(townRoot, beadID)
	if err != nil {
		t.Fatalf("pre-acquire lock: %v", err)
	}
	defer release()

	// Create a bd stub (won't be reached since lock should block first)
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
echo '[{"title":"Test","status":"open","assignee":"","description":""}]'
exit 0
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	params := sling.Intent{
		BeadID:   beadID,
		RigName:  "testrig",
		TownRoot: townRoot,
	}

	_, err = executeSling(context.Background(), params)
	if err == nil {
		t.Fatal("expected executeSling to fail when lock is held, got nil error")
	}
	if !strings.Contains(err.Error(), "already being slung") {
		t.Fatalf("expected lock contention error, got: %v", err)
	}
}

// TestExecuteSling_LockReleasedAfterReturn verifies that the flock is released
// when executeSling returns (even on error), allowing a subsequent call to proceed.
func TestExecuteSling_LockReleasedAfterReturn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("advisory flock is a no-op on Windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	beadID := "gt-lockrel1"

	// Create a bd stub that returns closed status (causes executeSling to error out)
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
echo '[{"title":"Done","status":"closed","assignee":"","description":""}]'
exit 0
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	params := sling.Intent{
		BeadID:   beadID,
		RigName:  "testrig",
		TownRoot: townRoot,
	}

	// First call — acquires lock, fails on closed guard, releases lock
	_, err := executeSling(context.Background(), params)
	if err == nil {
		t.Fatal("expected closed guard error")
	}

	// Second call — should acquire the lock (not contention error)
	_, err = executeSling(context.Background(), params)
	if err == nil {
		t.Fatal("expected closed guard error on second call")
	}
	if strings.Contains(err.Error(), "already being slung") {
		t.Fatal("lock was not released after first executeSling returned")
	}
}
