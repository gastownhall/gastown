package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/tmux"
)

func TestBootSpawnAgentFlag(t *testing.T) {
	flag := bootSpawnCmd.Flags().Lookup("agent")
	if flag == nil {
		t.Fatal("expected boot spawn to define --agent flag")
	}
	if flag.DefValue != "" {
		t.Errorf("expected default agent override to be empty, got %q", flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "overrides town default") {
		t.Errorf("expected --agent usage to mention overrides town default, got %q", flag.Usage)
	}
}

// =============================================================================
// executeWarrants Tests
// =============================================================================

// writeTestWarrant creates a warrant file in dir for testing.
func writeTestWarrant(t *testing.T, dir string, w Warrant) {
	t.Helper()
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		t.Fatalf("marshaling warrant: %v", err)
	}
	if err := os.WriteFile(warrantFilePath(dir, w.Target), data, 0644); err != nil {
		t.Fatalf("writing warrant: %v", err)
	}
}

// readTestWarrant reads and parses a warrant file from dir.
func readTestWarrant(t *testing.T, dir string, target string) Warrant {
	t.Helper()
	data, err := os.ReadFile(warrantFilePath(dir, target))
	if err != nil {
		t.Fatalf("reading warrant file: %v", err)
	}
	var w Warrant
	if err := json.Unmarshal(data, &w); err != nil {
		t.Fatalf("parsing warrant file: %v", err)
	}
	return w
}

// TestExecuteWarrants_MarksPendingAsExecuted verifies that executeWarrants
// marks pending warrants as executed even when the target session is already dead.
// This covers the normal case during degraded triage: sessions have been killed
// by other means, or the warrant fires in the same cycle that restarts everything.
func TestExecuteWarrants_MarksPendingAsExecuted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping warrant execution test on Windows (no tmux)")
	}
	// Register prefixes so targetToSessionName can resolve "gastown" → "gt"
	setupWarrantTestRegistry(t)

	warrantDir := t.TempDir()

	pending := Warrant{
		ID:       "warrant-test-pending",
		Target:   "gastown/polecats/test-nonexistent-x7q",
		Reason:   "Zombie: no session, idle >10m",
		FiledBy:  "test",
		FiledAt:  time.Now().Add(-5 * time.Minute),
		Executed: false,
	}
	writeTestWarrant(t, warrantDir, pending)

	tm := tmux.NewTmux()
	executeWarrants(warrantDir, tm)

	result := readTestWarrant(t, warrantDir, pending.Target)
	if !result.Executed {
		t.Error("Executed = false, want true after executeWarrants")
	}
	if result.ExecutedAt == nil {
		t.Error("ExecutedAt = nil, want non-nil after executeWarrants")
	}
}

// TestExecuteWarrants_SkipsAlreadyExecuted verifies that executeWarrants does
// not re-execute or modify warrants that are already marked as executed.
func TestExecuteWarrants_SkipsAlreadyExecuted(t *testing.T) {
	setupWarrantTestRegistry(t)

	warrantDir := t.TempDir()

	executedAt := time.Now().Add(-time.Hour)
	done := Warrant{
		ID:         "warrant-already-done",
		Target:     "gastown/polecats/already-done-x7q",
		Reason:     "Already executed",
		FiledBy:    "test",
		FiledAt:    time.Now().Add(-2 * time.Hour),
		Executed:   true,
		ExecutedAt: &executedAt,
	}
	writeTestWarrant(t, warrantDir, done)

	tm := tmux.NewTmux()
	executeWarrants(warrantDir, tm)

	result := readTestWarrant(t, warrantDir, done.Target)
	if !result.Executed {
		t.Error("Executed = false, want true (unchanged)")
	}
	// ExecutedAt should be the original time, not updated
	if result.ExecutedAt == nil || !result.ExecutedAt.Equal(executedAt) {
		t.Errorf("ExecutedAt changed: got %v, want %v", result.ExecutedAt, executedAt)
	}
}

// TestExecuteWarrants_MissingDir verifies that executeWarrants handles a
// missing warrants directory gracefully (no panic, no error).
func TestExecuteWarrants_MissingDir(t *testing.T) {
	tm := tmux.NewTmux()
	missingDir := filepath.Join(t.TempDir(), "does-not-exist")
	executeWarrants(missingDir, tm) // should not panic
}

// TestExecuteWarrants_EmptyDir verifies that executeWarrants handles an
// empty warrants directory gracefully.
func TestExecuteWarrants_EmptyDir(t *testing.T) {
	warrantDir := t.TempDir()
	tm := tmux.NewTmux()
	executeWarrants(warrantDir, tm) // should not panic
}

// TestExecuteWarrants_IgnoresNonWarrantFiles verifies that non-.warrant.json
// files in the directory are ignored.
func TestExecuteWarrants_IgnoresNonWarrantFiles(t *testing.T) {
	warrantDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(warrantDir, "readme.txt"), []byte("ignore me"), 0644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	tm := tmux.NewTmux()
	executeWarrants(warrantDir, tm) // should not panic or error
}
