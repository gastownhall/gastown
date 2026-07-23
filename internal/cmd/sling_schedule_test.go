package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
	"github.com/steveyegge/gastown/internal/wisp"
)

func TestScheduledLocalBaseRoundTripToSpawn(t *testing.T) {
	townRoot, rigPath, _ := setupMutableBDRawSlingTest(t, "Keep this body.")
	contextPath := filepath.Join(townRoot, "sling-context.json")
	t.Setenv("BD_CONTEXT_FILE", contextPath)

	err := scheduleBead("gt-rawrollback", "gastown", ScheduleOptions{
		Merge:       "local",
		BaseBranch:  "feature/local-integration",
		NoConvoy:    true,
		HookRawBead: true,
		Force:       true,
	})
	if err != nil {
		t.Fatalf("scheduleBead: %v", err)
	}

	description, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatalf("read scheduled context: %v", err)
	}
	fields := beads.ParseSlingContextFields(string(description))
	if fields == nil {
		t.Fatalf("parse scheduled context: %s", description)
	}
	if fields.Merge != "local" || fields.BaseBranch != "feature/local-integration" {
		t.Fatalf("stored context merge/base = %q/%q, want local/feature/local-integration", fields.Merge, fields.BaseBranch)
	}

	oldSpawn := spawnPolecatForSling
	t.Cleanup(func() { spawnPolecatForSling = oldSpawn })
	var gotOpts SlingSpawnOptions
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		gotOpts = opts
		return &SpawnedPolecatInfo{
			RigName:     rigName,
			PolecatName: "localbase",
			ClonePath:   rigPath,
			Pane:        "test-pane",
		}, nil
	}

	_, err = dispatchSingleBead(capacity.PendingBead{
		ID:         "gt-context",
		WorkBeadID: "gt-rawrollback",
		TargetRig:  "gastown",
		Context:    fields,
	}, townRoot, "test")
	if err != nil {
		t.Fatalf("dispatchSingleBead: %v", err)
	}
	if !gotOpts.LocalBase {
		t.Fatal("scheduled local merge did not set SlingSpawnOptions.LocalBase")
	}
	if gotOpts.BaseBranch != "feature/local-integration" {
		t.Fatalf("scheduled spawn base branch = %q, want feature/local-integration", gotOpts.BaseBranch)
	}
}

// TestAreScheduledFailClosed verifies that areScheduled fails closed when
// running outside a town root — all requested IDs should be treated as scheduled.
// This prevents false stranded detection and duplicate scheduling on transient errors.
func TestAreScheduledFailClosed(t *testing.T) {
	// Run areScheduled from a temp dir that is NOT a town root.
	// workspace.FindFromCwd will fail, triggering the fail-closed path.
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir to temp dir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	requestedIDs := []string{"bead-1", "bead-2", "bead-3"}
	result := areScheduled(requestedIDs)

	// All IDs should appear as scheduled (fail closed)
	for _, id := range requestedIDs {
		if !result[id] {
			t.Errorf("areScheduled fail-closed: expected %q to be marked as scheduled, but it was not", id)
		}
	}
}

// TestAreScheduledEmptyInput verifies areScheduled returns empty map for no input.
func TestAreScheduledEmptyInput(t *testing.T) {
	result := areScheduled(nil)
	if len(result) != 0 {
		t.Errorf("areScheduled(nil) should return empty map, got %d entries", len(result))
	}
	result = areScheduled([]string{})
	if len(result) != 0 {
		t.Errorf("areScheduled([]) should return empty map, got %d entries", len(result))
	}
}

// TestResolveFormula verifies formula resolution precedence:
// explicit flag > wisp layer > bead layer > system default > settings file > hardcoded fallback.
func TestResolveFormula(t *testing.T) {
	t.Parallel()

	t.Run("explicit flag wins", func(t *testing.T) {
		t.Parallel()
		got := resolveFormula("mol-evolve", false, "/tmp/nonexistent", "myrig")
		if got != "mol-evolve" {
			t.Errorf("got %q, want %q", got, "mol-evolve")
		}
	})

	t.Run("hookRawBead returns empty", func(t *testing.T) {
		t.Parallel()
		got := resolveFormula("mol-evolve", true, "/tmp/nonexistent", "myrig")
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("system default mol-polecat-work", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		rigName := "testrig"
		_ = os.MkdirAll(filepath.Join(tmpDir, rigName), 0o755)
		got := resolveFormula("", false, tmpDir, rigName)
		if got != "mol-polecat-work" {
			t.Errorf("got %q, want %q", got, "mol-polecat-work")
		}
	})

	t.Run("wisp layer overrides system default", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		rigName := "testrig"
		_ = os.MkdirAll(filepath.Join(tmpDir, rigName), 0o755)

		wispCfg := wisp.NewConfig(tmpDir, rigName)
		if err := wispCfg.Set("default_formula", "mol-evolve"); err != nil {
			t.Fatalf("wisp set: %v", err)
		}

		got := resolveFormula("", false, tmpDir, rigName)
		if got != "mol-evolve" {
			t.Errorf("got %q, want %q", got, "mol-evolve")
		}
	})

	t.Run("explicit flag overrides wisp layer", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		rigName := "testrig"
		_ = os.MkdirAll(filepath.Join(tmpDir, rigName), 0o755)

		wispCfg := wisp.NewConfig(tmpDir, rigName)
		if err := wispCfg.Set("default_formula", "mol-evolve"); err != nil {
			t.Fatalf("wisp set: %v", err)
		}

		got := resolveFormula("mol-custom", false, tmpDir, rigName)
		if got != "mol-custom" {
			t.Errorf("got %q, want %q", got, "mol-custom")
		}
	})

	t.Run("empty rigName falls back to hardcoded default", func(t *testing.T) {
		t.Parallel()
		got := resolveFormula("", false, "/tmp/nonexistent", "")
		if got != "mol-polecat-work" {
			t.Errorf("got %q, want %q", got, "mol-polecat-work")
		}
	})
}
