package autosave

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// si-9wu1 / ruling hq-vpt06. The refusal is only worth anything if it OUTLIVES
// the session: the auto-save fires on gt done, at session end, when nobody is
// reading stdout. These tests pin the durable half.

func TestWriteRefusalLandsBesideTheWorktreeNotInsideIt(t *testing.T) {
	parent := t.TempDir()
	worktree := filepath.Join(parent, "silicon")
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	path, err := WriteRefusal(worktree, "polecat/keeper/si-aka.37", 7767)
	if err != nil {
		t.Fatalf("WriteRefusal: %v", err)
	}

	if got := filepath.Dir(path); got != parent {
		t.Fatalf("marker written to %q, want the worktree's PARENT %q — inside the worktree it "+
			"would be erased by the 'git reset --hard' / 'git clean -f' these sandboxes get "+
			"routinely, which is the one thing the record must survive", got, parent)
	}
	if _, err := os.Stat(filepath.Join(worktree, MarkerName)); err == nil {
		t.Fatal("a marker was also written INSIDE the worktree")
	}
}

func TestWriteRefusalRecordsWhatAnOperatorNeeds(t *testing.T) {
	parent := t.TempDir()
	worktree := filepath.Join(parent, "silicon")
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	path, err := WriteRefusal(worktree, "polecat/keeper/si-aka.37", 7767)
	if err != nil {
		t.Fatalf("WriteRefusal: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}

	var rec Refusal
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("marker is not valid JSON: %v\n%s", err, data)
	}
	if rec.StagedDeletions != 7767 {
		t.Errorf("StagedDeletions = %d, want 7767", rec.StagedDeletions)
	}
	if rec.Branch != "polecat/keeper/si-aka.37" {
		t.Errorf("Branch = %q, want polecat/keeper/si-aka.37", rec.Branch)
	}
	if rec.WorktreePath != worktree {
		t.Errorf("WorktreePath = %q, want %q", rec.WorktreePath, worktree)
	}
	if rec.RefusedAt.IsZero() {
		t.Error("RefusedAt is zero — a record with no time cannot be aged or ordered")
	}
	// The reader arrives without context. The single fact that stops them
	// panicking is that nothing was destroyed.
	if !strings.Contains(rec.Reason, "NOT MODIFIED") {
		t.Errorf("Reason does not tell the reader the working tree was untouched: %q", rec.Reason)
	}
}

func TestFindRefusalsLocatesAMarkerAtPolecatDepth(t *testing.T) {
	town := t.TempDir()
	worktree := filepath.Join(town, "silicon", "polecats", "keeper", "silicon")
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := WriteRefusal(worktree, "polecat/keeper/si-aka.37", 7767); err != nil {
		t.Fatalf("WriteRefusal: %v", err)
	}

	found := FindRefusals(town, 5)
	if len(found) != 1 {
		t.Fatalf("FindRefusals found %d markers, want 1 — a marker the scan cannot reach is the "+
			"unobservable non-save the ruling forbade: %v", len(found), found)
	}
}

// Denominator: the scan must not report markers that are not there.
func TestFindRefusalsReturnsNothingOnACleanTown(t *testing.T) {
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "silicon", "polecats", "keeper", "silicon"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if found := FindRefusals(town, 5); len(found) != 0 {
		t.Fatalf("FindRefusals found %v on a clean town", found)
	}
}

// The walk must not descend into repository internals; a town root contains
// whole repos and an unbounded walk is both slow and liable to false positives.
func TestFindRefusalsSkipsRepositoryInternals(t *testing.T) {
	town := t.TempDir()
	inGit := filepath.Join(town, "silicon", ".repo.git", "worktrees", "keeper")
	if err := os.MkdirAll(inGit, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inGit, MarkerName), []byte("{}\n"), 0644); err != nil {
		t.Fatalf("write decoy: %v", err)
	}

	if found := FindRefusals(town, 5); len(found) != 0 {
		t.Fatalf("FindRefusals descended into .repo.git: %v", found)
	}
}

// A corrupt marker still means a refusal happened. Dropping it silently would
// be the same unobservable non-report the marker exists to prevent.
func TestDescribeStillReportsAnUnparseableMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, MarkerName)
	if err := os.WriteFile(path, []byte("not json"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := Describe(path)
	if !strings.Contains(got, path) {
		t.Fatalf("Describe(%q) = %q — a corrupt marker must still surface its path", path, got)
	}
}
