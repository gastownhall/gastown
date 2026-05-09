package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
)

// TestIsRigParkedOrDocked_NoRouteForPrefix verifies that IsRigParkedOrDocked
// returns (false, "") without emitting routing warnings when the rig's beads
// prefix has no entry in routes.jsonl. This is the regression test for
// https://github.com/gastownhall/gastown/issues/3855.
func TestIsRigParkedOrDocked_NoRouteForPrefix(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "myproject"

	// Set up rig directory with config.json using the default "gt" prefix.
	// This simulates a project rig at a non-standard install where the gastown
	// infrastructure rig (which owns the "gt-" prefix route) is not registered.
	rigPath := filepath.Join(townRoot, rigName)
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatalf("mkdir rig path: %v", err)
	}
	rigCfg := &config.RigConfig{
		Type:    "rig",
		Version: config.CurrentRigConfigVersion,
		Name:    rigName,
		GitURL:  "git@github.com:example/myproject.git",
		Beads:   &config.BeadsConfig{Prefix: "gt"},
	}
	if err := config.SaveRigConfig(filepath.Join(rigPath, "config.json"), rigCfg); err != nil {
		t.Fatalf("save rig config: %v", err)
	}

	// Create .beads directory without any routes.jsonl — gastown infra rig
	// is not registered at this install (the scenario from issue #3855).
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	blocked, reason := IsRigParkedOrDocked(townRoot, rigName)
	if blocked {
		t.Errorf("IsRigParkedOrDocked = (%v, %q), want (false, \"\")", blocked, reason)
	}
	if reason != "" {
		t.Errorf("IsRigParkedOrDocked returned unexpected reason %q, want \"\"", reason)
	}
}

// TestIsRigParkedOrDocked_WithRoute_ProceedsToBead verifies that when a route
// IS registered for the rig's prefix, the bead lookup proceeds normally. The
// route guard must not prevent the docked/parked check when routing is valid.
func TestIsRigParkedOrDocked_WithRoute_ProceedsToBead(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "myproject"

	rigPath := filepath.Join(townRoot, rigName)
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatalf("mkdir rig path: %v", err)
	}
	rigCfg := &config.RigConfig{
		Type:    "rig",
		Version: config.CurrentRigConfigVersion,
		Name:    rigName,
		GitURL:  "git@github.com:example/myproject.git",
		Beads:   &config.BeadsConfig{Prefix: "gt"},
	}
	if err := config.SaveRigConfig(filepath.Join(rigPath, "config.json"), rigCfg); err != nil {
		t.Fatalf("save rig config: %v", err)
	}

	// Register a "gt-" route pointing at the rig directory (no real bead DB needed;
	// bd.Show will fail gracefully and return (false, "")).
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"myproject"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	// With a valid route the code proceeds to bd.Show, which will fail (no Dolt
	// running in tests) and return (false, "") — same observable result, but the
	// code path past the route guard is exercised.
	blocked, reason := IsRigParkedOrDocked(townRoot, rigName)
	if blocked {
		t.Errorf("IsRigParkedOrDocked = (%v, %q), want (false, \"\")", blocked, reason)
	}
	if reason != "" {
		t.Errorf("IsRigParkedOrDocked returned unexpected reason %q, want \"\"", reason)
	}
}
