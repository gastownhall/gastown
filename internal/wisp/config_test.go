package wisp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConfig_EnsureCreatesCanonicalEmptyConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := NewConfig(tmpDir, "testrig")

	if err := cfg.Ensure(); err != nil {
		t.Fatalf("Ensure() error: %v", err)
	}

	data, err := os.ReadFile(cfg.ConfigPath())
	if err != nil {
		t.Fatalf("read ensured config: %v", err)
	}
	var got ConfigFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal ensured config: %v", err)
	}
	if got.Rig != "testrig" {
		t.Fatalf("Rig = %q, want testrig", got.Rig)
	}
	if got.Values == nil || len(got.Values) != 0 {
		t.Fatalf("Values = %#v, want initialized empty map", got.Values)
	}
	if got.Blocked == nil || len(got.Blocked) != 0 {
		t.Fatalf("Blocked = %#v, want initialized empty slice", got.Blocked)
	}
}

func TestConfig_EnsurePreservesExistingConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := NewConfig(tmpDir, "testrig")
	if err := cfg.Set("status", "parked"); err != nil {
		t.Fatalf("Set() error: %v", err)
	}
	before, err := os.ReadFile(cfg.ConfigPath())
	if err != nil {
		t.Fatalf("read config before Ensure: %v", err)
	}

	if err := cfg.Ensure(); err != nil {
		t.Fatalf("Ensure() error: %v", err)
	}
	after, err := os.ReadFile(cfg.ConfigPath())
	if err != nil {
		t.Fatalf("read config after Ensure: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("Ensure() changed existing config:\nbefore: %s\nafter: %s", before, after)
	}
}

func TestConfig_BasicOperations(t *testing.T) {
	// Create temp directory for test
	tmpDir := t.TempDir()
	rigName := "testrig"

	cfg := NewConfig(tmpDir, rigName)

	// Test initial state - Get returns nil
	if got := cfg.Get("key1"); got != nil {
		t.Errorf("Get(key1) = %v, want nil", got)
	}

	// Test Set and Get
	if err := cfg.Set("key1", "value1"); err != nil {
		t.Fatalf("Set(key1) error: %v", err)
	}

	if got := cfg.Get("key1"); got != "value1" {
		t.Errorf("Get(key1) = %v, want value1", got)
	}

	// Test GetString
	if got := cfg.GetString("key1"); got != "value1" {
		t.Errorf("GetString(key1) = %v, want value1", got)
	}

	// Test file was created in correct location
	expectedPath := filepath.Join(tmpDir, WispConfigDir, ConfigSubdir, rigName+".json")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Errorf("Config file not created at expected path: %s", expectedPath)
	}
}

func TestConfig_Block(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := NewConfig(tmpDir, "testrig")

	// Set a value first
	if err := cfg.Set("key1", "value1"); err != nil {
		t.Fatalf("Set(key1) error: %v", err)
	}

	// Verify it's not blocked
	if cfg.IsBlocked("key1") {
		t.Error("key1 should not be blocked initially")
	}

	// Block the key
	if err := cfg.Block("key1"); err != nil {
		t.Fatalf("Block(key1) error: %v", err)
	}

	// Verify it's blocked
	if !cfg.IsBlocked("key1") {
		t.Error("key1 should be blocked after Block()")
	}

	// Get should return nil for blocked key
	if got := cfg.Get("key1"); got != nil {
		t.Errorf("Get(key1) = %v, want nil for blocked key", got)
	}

	// Set should be ignored for blocked key
	if err := cfg.Set("key1", "newvalue"); err != nil {
		t.Fatalf("Set on blocked key error: %v", err)
	}
	if got := cfg.Get("key1"); got != nil {
		t.Errorf("Get(key1) after Set = %v, want nil (blocked key)", got)
	}
}

func TestConfig_Unset(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := NewConfig(tmpDir, "testrig")

	// Set and then unset
	if err := cfg.Set("key1", "value1"); err != nil {
		t.Fatalf("Set(key1) error: %v", err)
	}
	if err := cfg.Unset("key1"); err != nil {
		t.Fatalf("Unset(key1) error: %v", err)
	}
	if got := cfg.Get("key1"); got != nil {
		t.Errorf("Get(key1) after Unset = %v, want nil", got)
	}

	// Block and then unset
	if err := cfg.Block("key2"); err != nil {
		t.Fatalf("Block(key2) error: %v", err)
	}
	if !cfg.IsBlocked("key2") {
		t.Error("key2 should be blocked")
	}
	if err := cfg.Unset("key2"); err != nil {
		t.Fatalf("Unset(key2) error: %v", err)
	}
	if cfg.IsBlocked("key2") {
		t.Error("key2 should not be blocked after Unset")
	}
}

func TestConfig_TypedGetters(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := NewConfig(tmpDir, "testrig")

	// Test GetBool
	if err := cfg.Set("enabled", true); err != nil {
		t.Fatalf("Set(enabled) error: %v", err)
	}
	if got := cfg.GetBool("enabled"); !got {
		t.Error("GetBool(enabled) = false, want true")
	}
	if got := cfg.GetBool("nonexistent"); got {
		t.Error("GetBool(nonexistent) = true, want false")
	}

	// GetString on non-string returns empty
	if got := cfg.GetString("enabled"); got != "" {
		t.Errorf("GetString(enabled) = %q, want empty for bool value", got)
	}
}

func TestConfig_AllAndKeys(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := NewConfig(tmpDir, "testrig")

	// Set some values
	_ = cfg.Set("key1", "value1")
	_ = cfg.Set("key2", 42)
	_ = cfg.Block("key3")

	// Test All (should not include blocked)
	all := cfg.All()
	if len(all) != 2 {
		t.Errorf("All() returned %d items, want 2", len(all))
	}
	if all["key1"] != "value1" {
		t.Errorf("All()[key1] = %v, want value1", all["key1"])
	}

	// Test BlockedKeys
	blocked := cfg.BlockedKeys()
	if len(blocked) != 1 || blocked[0] != "key3" {
		t.Errorf("BlockedKeys() = %v, want [key3]", blocked)
	}

	// Test Keys (includes both)
	keys := cfg.Keys()
	if len(keys) != 3 {
		t.Errorf("Keys() returned %d items, want 3", len(keys))
	}
}

func TestConfig_Clear(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := NewConfig(tmpDir, "testrig")

	// Set some values and blocks
	_ = cfg.Set("key1", "value1")
	_ = cfg.Block("key2")

	// Clear
	if err := cfg.Clear(); err != nil {
		t.Fatalf("Clear() error: %v", err)
	}

	// Verify everything is gone
	if got := cfg.Get("key1"); got != nil {
		t.Errorf("Get(key1) after Clear = %v, want nil", got)
	}
	if cfg.IsBlocked("key2") {
		t.Error("key2 should not be blocked after Clear")
	}
}

func TestConfig_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"

	// Create first config instance and set value
	cfg1 := NewConfig(tmpDir, rigName)
	if err := cfg1.Set("persistent", "value"); err != nil {
		t.Fatalf("Set error: %v", err)
	}
	if err := cfg1.Block("blocked"); err != nil {
		t.Fatalf("Block error: %v", err)
	}

	// Create second config instance and verify persistence
	cfg2 := NewConfig(tmpDir, rigName)
	if got := cfg2.Get("persistent"); got != "value" {
		t.Errorf("Persistence: Get(persistent) = %v, want value", got)
	}
	if !cfg2.IsBlocked("blocked") {
		t.Error("Persistence: blocked key should persist")
	}
}

func TestConfig_MultipleRigs(t *testing.T) {
	tmpDir := t.TempDir()

	cfg1 := NewConfig(tmpDir, "rig1")
	cfg2 := NewConfig(tmpDir, "rig2")

	// Set different values
	_ = cfg1.Set("key", "value1")
	_ = cfg2.Set("key", "value2")

	// Verify isolation
	if got := cfg1.Get("key"); got != "value1" {
		t.Errorf("rig1 Get(key) = %v, want value1", got)
	}
	if got := cfg2.Get("key"); got != "value2" {
		t.Errorf("rig2 Get(key) = %v, want value2", got)
	}
}
