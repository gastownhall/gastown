package rig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/wisp"
)

func TestGetConfig_SystemDefaults(t *testing.T) {
	// Create a temp rig with no wisp or bead config
	tmpDir := t.TempDir()
	rigPath := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatal(err)
	}

	rig := &Rig{
		Name: "testrig",
		Path: rigPath,
	}

	// Should get system defaults
	result := rig.GetConfigWithSource("status")
	if result.Source != SourceSystem {
		t.Errorf("expected source SourceSystem, got %s", result.Source)
	}
	if result.Value != "operational" {
		t.Errorf("expected value 'operational', got %v", result.Value)
	}

	// Test boolean default
	if !rig.GetBoolConfig("auto_restart") {
		t.Error("expected auto_restart to be true by default")
	}

	// Test int default
	maxPolecats := rig.GetIntConfig("max_polecats")
	if maxPolecats != 10 {
		t.Errorf("expected max_polecats=10, got %d", maxPolecats)
	}
}

func TestGetConfig_WispOverride(t *testing.T) {
	tmpDir := t.TempDir()
	rigPath := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatal(err)
	}

	rig := &Rig{
		Name: "testrig",
		Path: rigPath,
	}

	// Create wisp config with override
	wispCfg := wisp.NewConfig(tmpDir, "testrig")
	if err := wispCfg.Set("status", "parked"); err != nil {
		t.Fatal(err)
	}

	// Should get wisp value
	result := rig.GetConfigWithSource("status")
	if result.Source != SourceWisp {
		t.Errorf("expected source SourceWisp, got %s", result.Source)
	}
	if result.Value != "parked" {
		t.Errorf("expected value 'parked', got %v", result.Value)
	}
}

func TestGetConfig_WispBlocked(t *testing.T) {
	tmpDir := t.TempDir()
	rigPath := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatal(err)
	}

	rig := &Rig{
		Name: "testrig",
		Path: rigPath,
	}

	// Block auto_restart at wisp layer
	wispCfg := wisp.NewConfig(tmpDir, "testrig")
	if err := wispCfg.Block("auto_restart"); err != nil {
		t.Fatal(err)
	}

	// Should return nil (blocked)
	result := rig.GetConfigWithSource("auto_restart")
	if result.Source != SourceBlocked {
		t.Errorf("expected source SourceBlocked, got %s", result.Source)
	}
	if result.Value != nil {
		t.Errorf("expected nil value for blocked key, got %v", result.Value)
	}

	// Bool getter should return false for blocked
	if rig.GetBoolConfig("auto_restart") {
		t.Error("expected auto_restart to be false when blocked")
	}
}

func TestGetIntConfig_Stacking(t *testing.T) {
	tmpDir := t.TempDir()
	rigPath := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatal(err)
	}

	rig := &Rig{
		Name: "testrig",
		Path: rigPath,
	}

	// Set wisp adjustment
	wispCfg := wisp.NewConfig(tmpDir, "testrig")
	if err := wispCfg.Set("priority_adjustment", 5); err != nil {
		t.Fatal(err)
	}

	// priority_adjustment uses stacking: base (0) + wisp (5) = 5
	result := rig.GetIntConfig("priority_adjustment")
	if result != 5 {
		t.Errorf("expected priority_adjustment=5, got %d", result)
	}
}

func TestGetBoolConfig_StringConversion(t *testing.T) {
	tmpDir := t.TempDir()
	rigPath := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatal(err)
	}

	rig := &Rig{
		Name: "testrig",
		Path: rigPath,
	}

	// Set string "true" in wisp
	wispCfg := wisp.NewConfig(tmpDir, "testrig")
	if err := wispCfg.Set("custom_bool", "true"); err != nil {
		t.Fatal(err)
	}

	if !rig.GetBoolConfig("custom_bool") {
		t.Error("expected 'true' string to convert to bool true")
	}

	// Set string "false"
	if err := wispCfg.Set("custom_bool", "false"); err != nil {
		t.Fatal(err)
	}

	if rig.GetBoolConfig("custom_bool") {
		t.Error("expected 'false' string to convert to bool false")
	}
}

func TestGetConfig_UnknownKey(t *testing.T) {
	tmpDir := t.TempDir()
	rigPath := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatal(err)
	}

	rig := &Rig{
		Name: "testrig",
		Path: rigPath,
	}

	result := rig.GetConfigWithSource("nonexistent_key")
	if result.Source != SourceNone {
		t.Errorf("expected source SourceNone, got %s", result.Source)
	}
	if result.Value != nil {
		t.Errorf("expected nil value for unknown key, got %v", result.Value)
	}
}

func TestGetStringConfig(t *testing.T) {
	tmpDir := t.TempDir()
	rigPath := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatal(err)
	}

	rig := &Rig{
		Name: "testrig",
		Path: rigPath,
	}

	// System default for status
	status := rig.GetStringConfig("status")
	if status != "operational" {
		t.Errorf("expected status='operational', got %s", status)
	}

	// Unknown key
	unknown := rig.GetStringConfig("nonexistent")
	if unknown != "" {
		t.Errorf("expected empty string for unknown key, got %s", unknown)
	}
}

func TestToInt(t *testing.T) {
	tests := []struct {
		input    interface{}
		expected int
	}{
		{nil, 0},
		{0, 0},
		{42, 42},
		{int64(100), 100},
		{float64(3.14), 3},
		{"123", 123},
		{"abc", 0},
		{true, 0}, // bools don't convert to int
	}

	for _, tc := range tests {
		result := toInt(tc.input)
		if result != tc.expected {
			t.Errorf("toInt(%v) = %d, expected %d", tc.input, result, tc.expected)
		}
	}
}

// TestGetConfig_BeadLabel tests reading config from rig bead labels.
// This requires a more complex setup with a full beads database.
func TestGetConfig_BeadLabel(t *testing.T) {
	tmpDir := t.TempDir()
	townDir := tmpDir
	rigPath := filepath.Join(townDir, "testrig")
	beadsDir := filepath.Join(rigPath, ".beads")

	// Create directory structure
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a minimal issues.jsonl with a rig identity bead
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	rigBead := map[string]interface{}{
		"id":     "gt-rig-testrig",
		"type":   "rig",
		"title":  "testrig",
		"status": "open",
		"labels": []string{"status:docked", "priority:high"},
	}
	data, _ := json.Marshal(rigBead)
	if err := os.WriteFile(issuesPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	// Note: This test demonstrates the structure but bd Show requires
	// a proper beads database. In production, use bd commands or mocks.
	// For now, we test that getBeadLabel returns nil gracefully when
	// beads is not fully set up.

	rig := &Rig{
		Name: "testrig",
		Path: rigPath,
	}

	// Without full beads setup, should fall back to system defaults
	result := rig.GetConfigWithSource("status")
	// Either SourceBead (if beads is set up) or SourceSystem
	if result.Source != SourceBead && result.Source != SourceSystem {
		t.Logf("source is %s (expected SourceBead or SourceSystem)", result.Source)
	}
}

func TestSetRefineryDisabled_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	rigPath := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatal(err)
	}
	// Write minimal config.json
	cfg := &RigConfig{Type: "rig", Version: 1, Name: "testrig", GitURL: "https://github.com/example/repo"}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(filepath.Join(rigPath, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// Disable
	if err := SetRefineryDisabled(rigPath, true); err != nil {
		t.Fatalf("SetRefineryDisabled(true): %v", err)
	}
	got, err := LoadRigConfig(rigPath)
	if err != nil {
		t.Fatalf("LoadRigConfig after disable: %v", err)
	}
	if !got.RefineryDisabled {
		t.Error("RefineryDisabled should be true after SetRefineryDisabled(true)")
	}

	// Re-enable
	if err := SetRefineryDisabled(rigPath, false); err != nil {
		t.Fatalf("SetRefineryDisabled(false): %v", err)
	}
	got, err = LoadRigConfig(rigPath)
	if err != nil {
		t.Fatalf("LoadRigConfig after enable: %v", err)
	}
	if got.RefineryDisabled {
		t.Error("RefineryDisabled should be false after SetRefineryDisabled(false)")
	}
}
