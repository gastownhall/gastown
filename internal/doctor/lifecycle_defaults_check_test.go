package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonbaldie/gastown/internal/daemon"
)

func TestLifecycleDefaultsCheck_NoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	check := NewLifecycleDefaultsCheck()
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected Warning for missing daemon.json, got %s", result.Status)
	}
	if result.Message != "daemon.json not found" {
		t.Errorf("unexpected message: %s", result.Message)
	}
}

func TestLifecycleDefaultsCheck_FullyConfigured(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	os.MkdirAll(mayorDir, 0755)

	config := daemon.DefaultLifecycleConfig()
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(filepath.Join(mayorDir, "daemon.json"), data, 0644)

	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewLifecycleDefaultsCheck()
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected OK for fully configured, got %s: %s", result.Status, result.Message)
	}
}

func TestLifecycleDefaultsCheck_MissingPatrols(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	os.MkdirAll(mayorDir, 0755)

	// Only wisp_reaper configured — rest missing
	config := &daemon.DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Patrols: &daemon.PatrolsConfig{
			WispReaper: &daemon.WispReaperConfig{Enabled: true, IntervalStr: "30m"},
		},
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(filepath.Join(mayorDir, "daemon.json"), data, 0644)

	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewLifecycleDefaultsCheck()
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected Warning for partial config, got %s", result.Status)
	}
	// Should report 5 missing: compactor_dog, doctor_dog, jsonl_git_backup, dolt_backup, scheduled_maintenance
	if len(check.missing) != 5 {
		t.Errorf("expected 5 missing patrols, got %d: %v", len(check.missing), check.missing)
	}
}

func TestLifecycleDefaultsCheck_Fix(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	os.MkdirAll(mayorDir, 0755)

	// Partial config
	config := &daemon.DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Patrols: &daemon.PatrolsConfig{
			WispReaper: &daemon.WispReaperConfig{Enabled: true, IntervalStr: "1h"},
		},
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(filepath.Join(mayorDir, "daemon.json"), data, 0644)

	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewLifecycleDefaultsCheck()

	// Verify it detects the problem
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected Warning before fix, got %s", result.Status)
	}

	// Fix it
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}

	// Verify fix worked
	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected OK after fix, got %s: %s", result.Status, result.Message)
	}

	// Verify user config was preserved
	loaded := daemon.LoadPatrolConfig(tmpDir)
	if loaded.Patrols.WispReaper.IntervalStr != "1h" {
		t.Errorf("expected preserved user interval 1h, got %s", loaded.Patrols.WispReaper.IntervalStr)
	}
}

func TestLifecycleDefaultsCheck_CanFix(t *testing.T) {
	check := NewLifecycleDefaultsCheck()
	if !check.CanFix() {
		t.Error("expected CanFix() to return true")
	}
}

func TestLifecycleDefaultsCheck_NilPatrolsSection(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	os.MkdirAll(mayorDir, 0755)

	// Config with no patrols section at all
	config := &daemon.DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(filepath.Join(mayorDir, "daemon.json"), data, 0644)

	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewLifecycleDefaultsCheck()
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected Warning for nil patrols, got %s", result.Status)
	}
	if result.Message != "daemon.json missing patrols section" {
		t.Errorf("unexpected message: %s", result.Message)
	}
}
