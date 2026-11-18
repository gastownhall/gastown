package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/autosave"
)

// si-9wu1: the refusal has to be discoverable by someone who does not know to
// look for it. gt doctor is where an operator already goes when something feels
// wrong, which is the only property that matters for a detector.

func TestAutosaveRefusalCheckReportsAMarker(t *testing.T) {
	town := t.TempDir()
	worktree := filepath.Join(town, "silicon", "polecats", "keeper", "silicon")
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := autosave.WriteRefusal(worktree, "polecat/keeper/si-aka.37", 7767); err != nil {
		t.Fatalf("WriteRefusal: %v", err)
	}

	res := NewAutosaveRefusalCheck().Run(&CheckContext{TownRoot: town})
	if res.Status != StatusError {
		t.Fatalf("status = %v, want StatusError; message=%q", res.Status, res.Message)
	}
	joined := strings.Join(res.Details, "\n")
	if !strings.Contains(joined, "7767") || !strings.Contains(joined, "polecat/keeper/si-aka.37") {
		t.Fatalf("details %v do not name the line count and the branch — an operator cannot act "+
			"on a report that does not say where or how much", res.Details)
	}
	if !strings.Contains(res.FixHint, "not modified") {
		t.Errorf("FixHint should lead with the fact that nothing was destroyed: %q", res.FixHint)
	}
}

func TestAutosaveRefusalCheckPassesOnACleanTown(t *testing.T) {
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "silicon", "polecats", "keeper", "silicon"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	res := NewAutosaveRefusalCheck().Run(&CheckContext{TownRoot: town})
	if res.Status != StatusOK {
		t.Fatalf("status = %v on a clean town, want StatusOK; message=%q details=%v",
			res.Status, res.Message, res.Details)
	}
}

// Denominator: no town root is not a clean bill of health.
func TestAutosaveRefusalCheckDoesNotReportOKWithNoTownRoot(t *testing.T) {
	res := NewAutosaveRefusalCheck().Run(&CheckContext{})
	if res.Status == StatusOK {
		t.Fatalf("status = StatusOK having scanned nothing (message=%q)", res.Message)
	}
}

// The refusal threshold and the detector threshold must not drift apart. If the
// detector fired lower than the refusal, gt doctor would report a hazard the
// safety net commits anyway — a warning contradicted by the tool that issued it.
func TestRefusalThresholdMatchesDoctorThreshold(t *testing.T) {
	if autosave.Threshold != armedStagingThreshold {
		t.Fatalf("autosave.Threshold = %d but doctor armedStagingThreshold = %d; a gap between "+
			"them is a window where doctor reports armed and the auto-save commits it anyway",
			autosave.Threshold, armedStagingThreshold)
	}
}
