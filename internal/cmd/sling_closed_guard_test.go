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

// TestExecuteSling_ClosedBead verifies that executeSling rejects closed beads.
func TestExecuteSling_ClosedBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	// Create bd stub that returns status:"closed"
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"Done task","status":"closed","assignee":"","description":""}]'
    ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	params := sling.Intent{
		BeadID:   "test-closed1",
		RigName:  "testrig",
		TownRoot: townRoot,
	}

	result, err := executeSling(context.Background(), params)
	if err == nil {
		t.Fatal("expected error when slinging closed bead, got nil")
	}

	if result.ErrMsg != "already closed" {
		t.Errorf("expected ErrMsg='already closed', got %q", result.ErrMsg)
	}

	if !strings.Contains(err.Error(), "closed") || !strings.Contains(err.Error(), "work already completed") {
		t.Errorf("error should mention closed status: %v", err)
	}
}

// TestExecuteSling_TombstoneBead verifies that executeSling rejects tombstone beads.
func TestExecuteSling_TombstoneBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	// Create bd stub that returns status:"tombstone"
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"Tombstoned task","status":"tombstone","assignee":"","description":""}]'
    ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	params := sling.Intent{
		BeadID:   "test-tomb1",
		RigName:  "testrig",
		TownRoot: townRoot,
	}

	result, err := executeSling(context.Background(), params)
	if err == nil {
		t.Fatal("expected error when slinging tombstone bead, got nil")
	}

	if result.ErrMsg != "already tombstone" {
		t.Errorf("expected ErrMsg='already tombstone', got %q", result.ErrMsg)
	}

	if !strings.Contains(err.Error(), "tombstone") || !strings.Contains(err.Error(), "work already completed") {
		t.Errorf("error should mention tombstone status: %v", err)
	}
}

// TestExecuteSling_ClosedBead_ForceDoesNotBypass verifies that --force does NOT
// bypass the closed bead guard. To re-dispatch, the bead must be reopened first.
func TestExecuteSling_ClosedBead_ForceDoesNotBypass(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	// Create bd stub that returns status:"closed"
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"Done task","status":"closed","assignee":"","description":""}]'
    ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	params := sling.Intent{
		BeadID:   "test-closed2",
		RigName:  "testrig",
		TownRoot: townRoot,
		Force:    true, // --force should NOT bypass closed guard
	}

	_, err := executeSling(context.Background(), params)
	if err == nil {
		t.Fatal("expected error when slinging closed bead with --force, got nil")
	}

	if !strings.Contains(err.Error(), "closed") || !strings.Contains(err.Error(), "work already completed") {
		t.Errorf("--force should not bypass closed guard: %v", err)
	}
}
