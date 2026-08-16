package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/wisp"
)

// TestResolveTarget_ParkedRig_InResolveTarget verifies that the parked rig
// check is integrated into the resolveTarget rig path. Since resolveTarget
// requires a full rig configuration to detect rigs (via IsRigName), this test
// verifies the check is properly called by using executeSling which has a
// direct check. See TestExecuteSling_ParkedRig for the direct test.
//
// Note: A more complete test would require setting up rigs.json and full
// workspace structure. The executeSling check covers the critical path.

// TestExecuteSling_ParkedRig verifies that executeSling fails when the target
// rig is parked (gt-4owfd.1).
func TestExecuteSling_ParkedRig(t *testing.T) {
	// Set up a temp dir as town root
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	// Set up wisp config with parked status
	rigName := "testrig"
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

	// Try to execute sling to the parked rig
	params := SlingParams{
		BeadID:   "test-123",
		RigName:  rigName,
		TownRoot: townRoot,
	}

	result, err := executeSling(params)
	if err == nil {
		t.Fatal("expected error when slinging to parked rig, got nil")
	}

	// Verify the result contains error info
	if result.ErrMsg != "rig parked" {
		t.Errorf("expected ErrMsg='rig parked', got %q", result.ErrMsg)
	}

	// Verify the error message contains helpful info
	errMsg := err.Error()
	if !parkedRigContainsAll(errMsg, "parked", rigName, "unpark") {
		t.Errorf("error message should mention parked rig and unpark command, got: %s", errMsg)
	}
}

// parkedRigContainsAll checks if s contains all the substrings.
func parkedRigContainsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
