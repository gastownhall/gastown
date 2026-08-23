package dog

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
)

// These tests pin the defect that emptied the kennel: a single unreachable rig
// aborted the whole spawn, the rollback could not remove registered git
// worktrees, and the surviving directory made the name permanently unusable.

// addBrokenRig registers a rig that has neither .repo.git nor mayor/rig, so
// findRepoBase fails for it exactly as an unreachable rig does in production.
func addBrokenRig(t *testing.T, m *Manager, townRoot, rigName string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(townRoot, rigName), 0755); err != nil {
		t.Fatalf("Failed to create broken rig dir: %v", err)
	}
	m.rigsConfig.Rigs[rigName] = config.RigEntry{GitURL: "local://" + rigName}
}

// TestManager_Add_Integration_TolerantOfUnreachableRig proves one bad rig no
// longer aborts the kennel entry: the dog is created from the rigs that work
// and the skipped rig is recorded in state instead of being lost.
func TestManager_Add_Integration_TolerantOfUnreachableRig(t *testing.T) {
	m, townRoot := testTownWithGitRigs(t)
	addBrokenRig(t, m, townRoot, "brokenrig")

	d, err := m.Add("tolerant")
	if err != nil {
		t.Fatalf("Add() error = %v, want success with the usable rig", err)
	}

	if _, ok := d.Worktrees["testrig"]; !ok {
		t.Errorf("Worktrees = %v, want an entry for testrig", d.Worktrees)
	}
	if _, ok := d.Worktrees["brokenrig"]; ok {
		t.Errorf("Worktrees = %v, want no entry for the unreachable rig", d.Worktrees)
	}

	state, err := m.loadState("tolerant")
	if err != nil {
		t.Fatalf("loadState() error = %v", err)
	}
	if len(state.SkippedRigs) != 1 || state.SkippedRigs[0] != "brokenrig" {
		t.Errorf("SkippedRigs = %v, want [brokenrig]", state.SkippedRigs)
	}

	// The dog must be a first-class kennel member, not a half-entry.
	if _, err := m.Get("tolerant"); err != nil {
		t.Errorf("Get() error = %v, want the partially-spawned dog to be usable", err)
	}
}

// TestManager_Add_Integration_NoResidueWhenEveryRigFails proves a total spawn
// failure leaves nothing behind: no directory, and therefore no burned name.
func TestManager_Add_Integration_NoResidueWhenEveryRigFails(t *testing.T) {
	_, townRoot := testTownWithGitRigs(t)

	broken := NewManager(townRoot, &config.RigsConfig{
		Version: 1,
		Rigs:    map[string]config.RigEntry{"brokenrig": {GitURL: "local://brokenrig"}},
	})
	if err := os.MkdirAll(filepath.Join(townRoot, "brokenrig"), 0755); err != nil {
		t.Fatalf("Failed to create broken rig dir: %v", err)
	}

	if _, err := broken.Add("doomed"); err == nil {
		t.Fatal("Add() error = nil, want failure when no rig is usable")
	}

	dogPath := filepath.Join(townRoot, "deacon", "dogs", "doomed")
	if _, err := os.Stat(dogPath); !os.IsNotExist(err) {
		t.Fatalf("failed spawn left residue at %s (stat err = %v)", dogPath, err)
	}

	debris, err := broken.ListDebris()
	if err != nil {
		t.Fatalf("ListDebris() error = %v", err)
	}
	if len(debris) != 0 {
		t.Errorf("ListDebris() = %v, want empty after a fully rolled-back spawn", debris)
	}
}

// TestManager_Add_Integration_ReclaimsNameBurnedByRegisteredWorktree
// reproduces the production state: a directory holding a live registered git
// worktree and no .dog.json. os.RemoveAll cannot undo that, so the name stayed
// burned. Add must tear the worktree down through the canonical path and
// rebuild the dog.
func TestManager_Add_Integration_ReclaimsNameBurnedByRegisteredWorktree(t *testing.T) {
	m, _ := testTownWithGitRigs(t)

	if _, err := m.Add("rex"); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Recreate the burned state: registered worktree present, state file gone.
	if err := os.Remove(m.stateFilePath("rex")); err != nil {
		t.Fatalf("Failed to remove state file: %v", err)
	}

	if m.exists("rex") {
		t.Fatal("exists() = true for a directory without .dog.json; the name is burned")
	}

	debris, err := m.ListDebris()
	if err != nil {
		t.Fatalf("ListDebris() error = %v", err)
	}
	if len(debris) != 1 || debris[0] != "rex" {
		t.Errorf("ListDebris() = %v, want [rex]; a corrupt kennel must not look empty", debris)
	}

	d, err := m.Add("rex")
	if err != nil {
		t.Fatalf("Add() error = %v, want the burned name to be reclaimable", err)
	}
	worktreePath, ok := d.Worktrees["testrig"]
	if !ok {
		t.Fatalf("Worktrees = %v, want an entry for testrig", d.Worktrees)
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Errorf("rebuilt worktree %s missing: %v", worktreePath, err)
	}

	if _, err := m.Get("rex"); err != nil {
		t.Errorf("Get() error = %v after reclaiming the name", err)
	}

	debris, err = m.ListDebris()
	if err != nil {
		t.Fatalf("ListDebris() error = %v", err)
	}
	if len(debris) != 0 {
		t.Errorf("ListDebris() = %v, want empty after the name was reclaimed", debris)
	}
}

// TestManager_Remove_Integration_ProtectsBootWatchdog pins that the Boot
// watchdog, which lives in the kennel without .dog.json, is never classified as
// debris and never destroyed by kennel cleanup.
func TestManager_Remove_Integration_ProtectsBootWatchdog(t *testing.T) {
	m, townRoot := testTownWithGitRigs(t)

	bootPath := filepath.Join(townRoot, "deacon", "dogs", "boot")
	if err := os.MkdirAll(bootPath, 0755); err != nil {
		t.Fatalf("Failed to create boot dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bootPath, ".boot-status.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("Failed to write boot status: %v", err)
	}

	debris, err := m.ListDebris()
	if err != nil {
		t.Fatalf("ListDebris() error = %v", err)
	}
	for _, name := range debris {
		if name == "boot" {
			t.Fatal("ListDebris() reported the Boot watchdog as debris")
		}
	}

	if err := m.Remove("boot"); err == nil {
		t.Fatal("Remove(boot) error = nil, want refusal to destroy a protected occupant")
	}
	if _, err := os.Stat(bootPath); err != nil {
		t.Errorf("Boot directory was destroyed: %v", err)
	}
}
