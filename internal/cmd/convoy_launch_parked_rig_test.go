package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonbaldie/gastown/internal/wisp"
)

// TestCheckBlockedRigsForLaunch_NoParkedRigs verifies that checkBlockedRigsForLaunch
// returns nil when no rigs are parked.
func TestCheckBlockedRigsForLaunch_NoParkedRigs(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Type: "task", Rig: "gastown"},
		"gt-b": {ID: "gt-b", Type: "task", Rig: "beads"},
	}}

	err := checkBlockedRigsForLaunch(dag, townRoot, false)
	if err != nil {
		t.Fatalf("expected no error when no rigs are parked, got: %v", err)
	}
}

// TestCheckBlockedRigsForLaunch_ParkedRig_BlocksWithoutForce verifies that
// checkBlockedRigsForLaunch returns an error when a rig is parked and force is false.
func TestCheckBlockedRigsForLaunch_ParkedRig_BlocksWithoutForce(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	// Set up wisp config with parked status
	rigName := "parkedrig"
	configDir := filepath.Join(townRoot, wisp.WispConfigDir, wisp.ConfigSubdir)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("failed to create wisp config dir: %v", err)
	}
	configFile := filepath.Join(configDir, rigName+".json")
	data, _ := json.Marshal(wisp.ConfigFile{
		Rig:    rigName,
		Values: map[string]interface{}{"status": "parked"},
	})
	if err := os.WriteFile(configFile, data, 0o644); err != nil {
		t.Fatalf("failed to write wisp config: %v", err)
	}

	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Type: "task", Rig: rigName},
		"gt-b": {ID: "gt-b", Type: "task", Rig: "gastown"},
	}}

	err := checkBlockedRigsForLaunch(dag, townRoot, false)
	if err == nil {
		t.Fatal("expected error when rig is parked without force, got nil")
	}

	// Verify error message contains helpful info
	errMsg := err.Error()
	if !parkedRigContainsAll(errMsg, "parked", rigName, "unpark", "--force") {
		t.Errorf("error message should mention parked rig, unpark command, and --force, got: %s", errMsg)
	}
}

// TestCheckBlockedRigsForLaunch_ParkedRig_AllowedWithForce verifies that
// checkBlockedRigsForLaunch allows proceeding when a rig is parked but force is true.
func TestCheckBlockedRigsForLaunch_ParkedRig_AllowedWithForce(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	// Set up wisp config with parked status
	rigName := "parkedrig"
	configDir := filepath.Join(townRoot, wisp.WispConfigDir, wisp.ConfigSubdir)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("failed to create wisp config dir: %v", err)
	}
	configFile := filepath.Join(configDir, rigName+".json")
	data, _ := json.Marshal(wisp.ConfigFile{
		Rig:    rigName,
		Values: map[string]interface{}{"status": "parked"},
	})
	if err := os.WriteFile(configFile, data, 0o644); err != nil {
		t.Fatalf("failed to write wisp config: %v", err)
	}

	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a": {ID: "gt-a", Type: "task", Rig: rigName},
		"gt-b": {ID: "gt-b", Type: "task", Rig: "gastown"},
	}}

	err := checkBlockedRigsForLaunch(dag, townRoot, true)
	if err != nil {
		t.Fatalf("expected no error when force=true, got: %v", err)
	}
}

// TestCollectBlockedRigsInDAG verifies that collectBlockedRigsInDAG correctly
// identifies beads targeting parked rigs.
func TestCollectBlockedRigsInDAG(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	// Set up wisp config with parked status for one rig
	parkedRig := "parkedrig"
	configDir := filepath.Join(townRoot, wisp.WispConfigDir, wisp.ConfigSubdir)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("failed to create wisp config dir: %v", err)
	}
	configFile := filepath.Join(configDir, parkedRig+".json")
	data, _ := json.Marshal(wisp.ConfigFile{
		Rig:    parkedRig,
		Values: map[string]interface{}{"status": "parked"},
	})
	if err := os.WriteFile(configFile, data, 0o644); err != nil {
		t.Fatalf("failed to write wisp config: %v", err)
	}

	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"gt-a":   {ID: "gt-a", Type: "task", Rig: parkedRig},
		"gt-b":   {ID: "gt-b", Type: "task", Rig: parkedRig},
		"gt-c":   {ID: "gt-c", Type: "task", Rig: "activerig"},
		"epic-1": {ID: "epic-1", Type: "epic", Rig: parkedRig}, // epics should be ignored
	}}

	result := collectBlockedRigsInDAG(dag, townRoot)

	if len(result) != 1 {
		t.Fatalf("expected 1 parked rig, got %d: %v", len(result), result)
	}

	beads, ok := result[parkedRig]
	if !ok {
		t.Fatalf("expected parked rig %q in results, got: %v", parkedRig, result)
	}

	if len(beads) != 2 {
		t.Errorf("expected 2 beads for parked rig, got %d: %v", len(beads), beads)
	}
}
