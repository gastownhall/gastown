//go:build integration

package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
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
	_ = townRoot

	rigDir := filepath.Join(polecatDir, "..", "..", "mayor", "rig")
	initBeadsDBWithPrefix(t, rigDir, rigPrefix)

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

func TestHookShowRoutedRigRootBeads(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping integration test")
	}

	townRoot, polecatDir, rigPrefix := setupHookTestTown(t)

	townBeadsDir := filepath.Join(townRoot, ".beads")
	if err := beads.WriteRoutes(townBeadsDir, []beads.Route{
		{Prefix: "hq-", Path: "."},
		{Prefix: rigPrefix + "-", Path: "gastown"},
	}); err != nil {
		t.Fatalf("rewrite routes: %v", err)
	}

	rigRoot := filepath.Join(townRoot, "gastown")
	initBeadsDBWithPrefix(t, rigRoot, rigPrefix)

	b := beads.New(rigRoot)
	issue, err := b.Create(beads.CreateOptions{
		Title:    "Hook show routed rig root test",
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
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir to town root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})

	prevJSON := moleculeJSON
	moleculeJSON = true
	t.Cleanup(func() {
		moleculeJSON = prevJSON
	})

	out := captureStdout(t, func() {
		if err := runHookShow(nil, []string{"gastown/polecats/toast"}); err != nil {
			t.Fatalf("runHookShow: %v", err)
		}
	})
	var parsed hookShowJSON
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse runHookShow output %q: %v", out, err)
	}
	if parsed.BeadID != issue.ID || parsed.Status != beads.StatusHooked {
		t.Fatalf("routed rig-root target mismatch: got bead=%q status=%q, want bead=%q status=%q",
			parsed.BeadID, parsed.Status, issue.ID, beads.StatusHooked)
	}

	_ = polecatDir
}
