package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectTbbRepo(t *testing.T) {
	dir := t.TempDir()
	if err := rejectTbbRepo(dir); err != nil {
		t.Fatalf("rejectTbbRepo without .tbb/: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".tbb"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := rejectTbbRepo(dir)
	if err == nil {
		t.Fatal("rejectTbbRepo with .tbb/: want error, got nil")
	}
	if !strings.Contains(err.Error(), "uses tbb (.tbb/ found); gtb does not manage tbb repos") {
		t.Fatalf("error = %q, missing refusal message", err)
	}
}

func TestRejectTbbRig_ChecksNestedCheckouts(t *testing.T) {
	for _, sub := range []string{"", "mayor/rig", "refinery/rig"} {
		t.Run("at "+sub, func(t *testing.T) {
			rigPath := t.TempDir()
			if err := rejectTbbRig(rigPath); err != nil {
				t.Fatalf("clean rig: %v", err)
			}
			if err := os.MkdirAll(filepath.Join(rigPath, filepath.FromSlash(sub), ".tbb"), 0o755); err != nil {
				t.Fatal(err)
			}
			err := rejectTbbRig(rigPath)
			if err == nil || !strings.Contains(err.Error(), "uses tbb (.tbb/ found); gtb does not manage tbb repos") {
				t.Fatalf("rejectTbbRig = %v, want tbb refusal", err)
			}
		})
	}
}
