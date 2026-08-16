package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonbaldie/gastown/internal/wisp"
)

func TestIsRigParked_WhenParked(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"

	// Set up wisp config with parked status
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

	if !IsRigParked(townRoot, rigName) {
		t.Error("expected IsRigParked to return true for parked rig")
	}
}

func TestIsRigParked_WhenNotParked(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"

	// No wisp config at all — should not be parked
	if IsRigParked(townRoot, rigName) {
		t.Error("expected IsRigParked to return false when no wisp config exists")
	}
}

func TestIsRigParked_WhenUnparked(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"

	// Set up wisp config with empty status (unparked)
	configDir := filepath.Join(townRoot, wisp.WispConfigDir, wisp.ConfigSubdir)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("failed to create wisp config dir: %v", err)
	}

	configFile := filepath.Join(configDir, rigName+".json")
	data, _ := json.Marshal(wisp.ConfigFile{
		Rig:    rigName,
		Values: map[string]interface{}{},
	})
	if err := os.WriteFile(configFile, data, 0o644); err != nil {
		t.Fatalf("failed to write wisp config: %v", err)
	}

	if IsRigParked(townRoot, rigName) {
		t.Error("expected IsRigParked to return false for unparked rig")
	}
}

func TestIsRigParked_WhenDocked(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"

	// Wisp config with docked status — IsRigParked should return false
	// (docked is a separate check via IsRigDocked)
	configDir := filepath.Join(townRoot, wisp.WispConfigDir, wisp.ConfigSubdir)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("failed to create wisp config dir: %v", err)
	}

	configFile := filepath.Join(configDir, rigName+".json")
	data, _ := json.Marshal(wisp.ConfigFile{
		Rig:    rigName,
		Values: map[string]interface{}{"status": "docked"},
	})
	if err := os.WriteFile(configFile, data, 0o644); err != nil {
		t.Fatalf("failed to write wisp config: %v", err)
	}

	if IsRigParked(townRoot, rigName) {
		t.Error("expected IsRigParked to return false for docked rig (not parked)")
	}
}

func TestRigStatusConstants(t *testing.T) {
	if RigStatusKey != "status" {
		t.Errorf("expected RigStatusKey to be 'status', got %q", RigStatusKey)
	}
	if RigStatusParked != "parked" {
		t.Errorf("expected RigStatusParked to be 'parked', got %q", RigStatusParked)
	}
	if RigDockedLabel != "status:docked" {
		t.Errorf("expected RigDockedLabel to be 'status:docked', got %q", RigDockedLabel)
	}
}
