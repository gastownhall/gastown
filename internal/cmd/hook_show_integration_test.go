//go:build integration

package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
)

type hookShowJSON struct {
	Agent  string `json:"agent"`
	BeadID string `json:"bead_id"`
	Status string `json:"status"`
}

// TestHookShowShorthandResolvesToCanonical verifies that hook show accepts
// shorthand polecat targets (rig/name) and resolves them to canonical
// assignee IDs (rig/polecats/name) before querying hooked work.
func TestHookShowShorthandResolvesToCanonical(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping integration test")
	}

	townRoot, polecatDir, rigPrefix := setupHookTestTown(t)

	rigDir := filepath.Join(polecatDir, "..", "..", "mayor", "rig")
	initBeadsDBWithPrefix(t, rigDir, rigPrefix)
	rigRootBeadsDir := filepath.Join(townRoot, "gastown", ".beads")
	if err := os.MkdirAll(rigRootBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir stale rig-root beads dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigRootBeadsDir, "metadata.json"), []byte(`{"dolt_database":"hq"}`), 0644); err != nil {
		t.Fatalf("write stale rig-root metadata: %v", err)
	}
	t.Setenv("BEADS_DIR", filepath.Join(townRoot, ".beads"))
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "hq")
	t.Setenv("BEADS_DB", filepath.Join(townRoot, "wrong.db"))
	t.Setenv("BD_DB", filepath.Join(townRoot, "wrong.bd"))
	t.Setenv("BEADS_DOLT_DATA_DIR", filepath.Join(townRoot, "wrong-data"))

	b := beads.New(rigDir)
	issue, err := b.Create(beads.CreateOptions{
		Title:    "Hook show target normalization test",
		Type:     "task",
		Priority: 2,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	hooked := beads.StatusHooked
	assignee := "gastown/polecats/toast"
	if err := b.Update(issue.ID, beads.UpdateOptions{
		Status:   &hooked,
		Assignee: &assignee,
	}); err != nil {
		t.Fatalf("hook issue: %v", err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(polecatDir); err != nil {
		t.Fatalf("chdir to polecat dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})

	prevJSON := moleculeJSON
	moleculeJSON = true
	t.Cleanup(func() {
		moleculeJSON = prevJSON
	})

	runShow := func(target string) hookShowJSON {
		out := captureStdout(t, func() {
			if err := runHookShow(nil, []string{target}); err != nil {
				t.Fatalf("runHookShow(%q): %v", target, err)
			}
		})
		var parsed hookShowJSON
		if err := json.Unmarshal([]byte(out), &parsed); err != nil {
			t.Fatalf("parse runHookShow(%q) output %q: %v", target, out, err)
		}
		return parsed
	}

	canonical := runShow("gastown/polecats/toast")
	if canonical.BeadID != issue.ID || canonical.Status != beads.StatusHooked {
		t.Fatalf("canonical target mismatch: got bead=%q status=%q, want bead=%q status=%q",
			canonical.BeadID, canonical.Status, issue.ID, beads.StatusHooked)
	}

	shorthand := runShow("gastown/toast")
	if shorthand.BeadID != issue.ID || shorthand.Status != beads.StatusHooked {
		t.Fatalf("shorthand target mismatch: got bead=%q status=%q, want bead=%q status=%q",
			shorthand.BeadID, shorthand.Status, issue.ID, beads.StatusHooked)
	}
	if shorthand.Agent != "gastown/polecats/toast" {
		t.Fatalf("shorthand target did not normalize: got agent=%q, want %q",
			shorthand.Agent, "gastown/polecats/toast")
	}

	inProgress := "in_progress"
	if err := b.Update(issue.ID, beads.UpdateOptions{Status: &inProgress}); err != nil {
		t.Fatalf("mark issue in progress: %v", err)
	}
	active := runShow("gastown/toast")
	if active.BeadID != issue.ID || active.Status != "in_progress" {
		t.Fatalf("in-progress target mismatch: got bead=%q status=%q, want bead=%q status=in_progress",
			active.BeadID, active.Status, issue.ID)
	}
}

func TestHookShowAndStatusFindEphemeralPatrolWisp(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping integration test")
	}

	_, polecatDir, rigPrefix := setupHookTestTown(t)
	rigDir := filepath.Join(polecatDir, "..", "..", "mayor", "rig")
	initBeadsDBWithPrefix(t, rigDir, rigPrefix)

	b := beads.New(rigDir)
	patrol, err := b.Create(beads.CreateOptions{
		Title:     constants.MolRefineryPatrol + " (wisp)",
		Type:      "molecule",
		Priority:  -1,
		Ephemeral: true,
	})
	if err != nil {
		t.Fatalf("create patrol wisp: %v", err)
	}

	hooked := beads.StatusHooked
	assignee := "gastown/refinery"
	if err := b.Update(patrol.ID, beads.UpdateOptions{
		Status:   &hooked,
		Assignee: &assignee,
	}); err != nil {
		t.Fatalf("hook patrol wisp: %v", err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(polecatDir); err != nil {
		t.Fatalf("chdir to polecat dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})

	prevJSON := moleculeJSON
	moleculeJSON = true
	t.Cleanup(func() {
		moleculeJSON = prevJSON
	})

	showOut := captureStdout(t, func() {
		if err := runHookShow(nil, []string{assignee}); err != nil {
			t.Fatalf("runHookShow(%q): %v", assignee, err)
		}
	})
	var show hookShowJSON
	if err := json.Unmarshal([]byte(showOut), &show); err != nil {
		t.Fatalf("parse hook show output %q: %v", showOut, err)
	}
	if show.BeadID != patrol.ID || show.Status != beads.StatusHooked {
		t.Fatalf("hook show mismatch: got bead=%q status=%q, want bead=%q status=%q",
			show.BeadID, show.Status, patrol.ID, beads.StatusHooked)
	}

	statusOut := captureStdout(t, func() {
		if err := runMoleculeStatus(nil, []string{assignee}); err != nil {
			t.Fatalf("runMoleculeStatus(%q): %v", assignee, err)
		}
	})
	var status MoleculeStatusInfo
	if err := json.Unmarshal([]byte(statusOut), &status); err != nil {
		t.Fatalf("parse hook status output %q: %v", statusOut, err)
	}
	if !status.HasWork || status.PinnedBead == nil || status.PinnedBead.ID != patrol.ID {
		t.Fatalf("hook status mismatch: has_work=%v pinned=%+v, want patrol %s",
			status.HasWork, status.PinnedBead, patrol.ID)
	}
}

func TestPrimeStateFindsEphemeralPatrolWisp(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping integration test")
	}

	townRoot, polecatDir, rigPrefix := setupHookTestTown(t)
	rigDir := filepath.Join(polecatDir, "..", "..", "mayor", "rig")
	initBeadsDBWithPrefix(t, rigDir, rigPrefix)

	b := beads.New(rigDir)
	patrol, err := b.Create(beads.CreateOptions{
		Title:     constants.MolRefineryPatrol + " (wisp)",
		Type:      "molecule",
		Priority:  -1,
		Ephemeral: true,
	})
	if err != nil {
		t.Fatalf("create patrol wisp: %v", err)
	}

	hooked := beads.StatusHooked
	assignee := "gastown/refinery"
	if err := b.Update(patrol.ID, beads.UpdateOptions{
		Status:   &hooked,
		Assignee: &assignee,
	}); err != nil {
		t.Fatalf("hook patrol wisp: %v", err)
	}

	state := detectSessionState(RoleContext{
		Role:     RoleRefinery,
		Rig:      "gastown",
		WorkDir:  polecatDir,
		TownRoot: townRoot,
	})
	if state.State != "autonomous" || state.HookedBead != patrol.ID {
		t.Fatalf("prime state = %+v, want autonomous with hooked bead %s", state, patrol.ID)
	}
}
