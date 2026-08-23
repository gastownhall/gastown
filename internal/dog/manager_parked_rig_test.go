package dog

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
)

// Dogs create a worktree in every configured rig, so they are the one
// multi-rig path that walks into a parked or docked rig. Every other
// dispatch path already gates on cmd.IsRigParkedOrDocked; these tests pin
// the equivalent gate for the kennel. The production incident: `invest` was
// parked with an unreachable submodule, and dog spawning entered it anyway.

// addParkedRig registers a rig that is reachable on disk but reported blocked
// by the injected predicate, so the test distinguishes "skipped because
// parked" from "skipped because broken".
func addParkedRig(t *testing.T, m *Manager, townRoot, rigName string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(townRoot, rigName), 0o755); err != nil {
		t.Fatalf("Failed to create parked rig dir: %v", err)
	}
	m.rigsConfig.Rigs[rigName] = config.RigEntry{GitURL: "local://" + rigName}
}

// TestManager_Add_SkipsParkedRig proves Add does not create a worktree in a
// parked rig and records it as skipped rather than failing the spawn.
func TestManager_Add_SkipsParkedRig(t *testing.T) {
	m, townRoot := testTownWithGitRigs(t)
	addParkedRig(t, m, townRoot, "parkedrig")

	var asked []string
	m.WithRigBlockedCheck(func(rig string) (bool, string) {
		asked = append(asked, rig)
		if rig == "parkedrig" {
			return true, "rig is parked"
		}
		return false, ""
	})

	d, err := m.Add("parkaware")
	if err != nil {
		t.Fatalf("Add() error = %v, want success skipping only the parked rig", err)
	}

	if _, ok := d.Worktrees["parkedrig"]; ok {
		t.Errorf("Worktrees contains parkedrig = %v, want it skipped", d.Worktrees)
	}
	if _, ok := d.Worktrees["testrig"]; !ok {
		t.Errorf("Worktrees = %v, want an entry for the operational testrig", d.Worktrees)
	}

	if _, err := m.Get("parkaware"); err != nil {
		t.Fatalf("Get() error = %v, want the dog to be readable", err)
	}
	state, err := m.loadState("parkaware")
	if err != nil {
		t.Fatalf("loadState() error = %v, want persisted state", err)
	}
	if !containsRig(state.SkippedRigs, "parkedrig") {
		t.Errorf("SkippedRigs = %v, want it to record parkedrig", state.SkippedRigs)
	}

	if !containsRig(asked, "parkedrig") {
		t.Errorf("predicate was asked about %v, want it consulted for parkedrig", asked)
	}

	// The decisive assertion: no worktree directory was materialised for the
	// parked rig. This is what corrupted the kennel in production.
	if _, err := os.Stat(filepath.Join(townRoot, "deacon", "dogs", "parkaware", "parkedrig")); !os.IsNotExist(err) {
		t.Errorf("parked rig worktree exists on disk, want it never created")
	}
}

// TestManager_Add_NilPredicateTouchesEveryRig proves the gate is opt-in, so
// callers that do not inject a predicate keep the previous behaviour.
func TestManager_Add_NilPredicateTouchesEveryRig(t *testing.T) {
	m, _ := testTownWithGitRigs(t)

	d, err := m.Add("nopredicate")
	if err != nil {
		t.Fatalf("Add() error = %v, want success", err)
	}
	if _, ok := d.Worktrees["testrig"]; !ok {
		t.Errorf("Worktrees = %v, want testrig when no predicate is injected", d.Worktrees)
	}
}

// TestManager_Add_AllRigsParkedFailsLoudly proves a kennel entry is never
// created empty: if every rig is blocked, Add fails instead of producing a
// dog with no worktrees that later looks idle to the dispatcher.
func TestManager_Add_AllRigsParkedFailsLoudly(t *testing.T) {
	m, townRoot := testTownWithGitRigs(t)
	m.WithRigBlockedCheck(func(string) (bool, string) { return true, "rig is parked" })

	if _, err := m.Add("allparked"); err == nil {
		t.Fatal("Add() error = nil, want failure when every rig is blocked")
	}

	if _, err := os.Stat(filepath.Join(townRoot, "deacon", "dogs", "allparked")); !os.IsNotExist(err) {
		t.Errorf("kennel entry survived a fully blocked spawn, want no debris")
	}
}

func containsRig(rigs []string, want string) bool {
	for _, rig := range rigs {
		if rig == want {
			return true
		}
	}
	return false
}
