package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/rig"
)

// setupTestRigForSettings creates a test rig for settings testing.
// Returns townRoot, rigName, and a cleanup function.
func setupTestRigForSettings(t *testing.T) (string, string) {
	t.Helper()

	townRoot := t.TempDir()

	// Create mayor directory and town.json (required for workspace detection)
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}

	// Create town.json (primary marker for workspace detection)
	townConfig := &config.TownConfig{
		Type:      "town",
		Version:   config.CurrentTownVersion,
		Name:      "test-town",
		CreatedAt: time.Now().Truncate(time.Second),
	}
	townConfigPath := filepath.Join(mayorDir, "town.json")
	if err := config.SaveTownConfig(townConfigPath, townConfig); err != nil {
		t.Fatalf("save town.json: %v", err)
	}

	// Create rigs.json
	rigsPath := filepath.Join(mayorDir, "rigs.json")
	rigsConfig := &config.RigsConfig{
		Version: 1,
		Rigs: map[string]config.RigEntry{
			"testrig": {
				GitURL:  "git@github.com:test/testrig.git",
				AddedAt: time.Now().Truncate(time.Second),
			},
		},
	}
	if err := config.SaveRigsConfig(rigsPath, rigsConfig); err != nil {
		t.Fatalf("save rigs.json: %v", err)
	}

	// Create rig directory structure
	rigPath := filepath.Join(townRoot, "testrig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}

	// Create rig config.json
	rigConfig := config.NewRigConfig("testrig", "git@github.com:test/testrig.git")
	rigConfigPath := filepath.Join(rigPath, "config.json")
	if err := config.SaveRigConfig(rigConfigPath, rigConfig); err != nil {
		t.Fatalf("save rig config: %v", err)
	}

	// Change to town root so workspace.FindFromCwdOrError works
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get current directory: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir to town root: %v", err)
	}
	t.Cleanup(func() {
		os.Chdir(oldCwd)
	})

	return townRoot, "testrig"
}

func TestRigSettingsShow(t *testing.T) {
	t.Run("shows existing settings file", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// Create settings file with some data
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings := config.NewRigSettings()
		settings.Agent = "claude"
		settings.RoleAgents = map[string]string{
			"witness": "gemini",
		}
		if err := config.SaveRigSettings(settingsPath, settings); err != nil {
			t.Fatalf("save settings: %v", err)
		}

		// Run show command
		cmd := rigSettingsShowCmd
		err := runRigSettingsShow(cmd, []string{rigName})
		if err != nil {
			t.Fatalf("runRigSettingsShow error: %v", err)
		}

		// Verify settings were loaded correctly by checking the file still exists
		loaded, err := config.LoadRigSettings(settingsPath)
		if err != nil {
			t.Fatalf("reload settings: %v", err)
		}
		if loaded.Agent != "claude" {
			t.Errorf("Agent = %q, want %q", loaded.Agent, "claude")
		}
	})

	t.Run("shows helpful message when file doesn't exist", func(t *testing.T) {
		_, rigName := setupTestRigForSettings(t)

		// Run show command (no settings file created)
		cmd := rigSettingsShowCmd
		err := runRigSettingsShow(cmd, []string{rigName})
		if err != nil {
			t.Fatalf("runRigSettingsShow error: %v", err)
		}
		// Should not error, just show message
	})

	t.Run("shows various settings configurations", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// Create settings with various configurations
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings := config.NewRigSettings()
		settings.Agent = "claude"
		settings.RoleAgents = map[string]string{
			"witness":  "gemini",
			"refinery": "claude-sonnet",
			"polecat":  "claude-haiku",
		}
		if settings.MergeQueue == nil {
			settings.MergeQueue = config.DefaultMergeQueueConfig()
		}
		if settings.MergeQueue != nil {
			settings.MergeQueue.MaxConcurrent = 5
		}
		if err := config.SaveRigSettings(settingsPath, settings); err != nil {
			t.Fatalf("save settings: %v", err)
		}

		// Run show command
		cmd := rigSettingsShowCmd
		err := runRigSettingsShow(cmd, []string{rigName})
		if err != nil {
			t.Fatalf("runRigSettingsShow error: %v", err)
		}

		// Verify by reloading
		loaded, err := config.LoadRigSettings(settingsPath)
		if err != nil {
			t.Fatalf("reload settings: %v", err)
		}
		if loaded.Agent != "claude" {
			t.Errorf("Agent = %q, want %q", loaded.Agent, "claude")
		}
		if len(loaded.RoleAgents) != 3 {
			t.Errorf("RoleAgents length = %d, want 3", len(loaded.RoleAgents))
		}
	})
}

func TestRigSettingsSet(t *testing.T) {
	t.Run("sets top-level keys", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// Set agent
		cmd := rigSettingsSetCmd
		err := runRigSettingsSet(cmd, []string{rigName, "agent", "claude"})
		if err != nil {
			t.Fatalf("runRigSettingsSet error: %v", err)
		}

		// Verify
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings, err := config.LoadRigSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}
		if settings.Agent != "claude" {
			t.Errorf("Agent = %q, want %q", settings.Agent, "claude")
		}
	})

	t.Run("sets nested keys with dot notation", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// Set role_agents.witness
		cmd := rigSettingsSetCmd
		err := runRigSettingsSet(cmd, []string{rigName, "role_agents.witness", "gemini"})
		if err != nil {
			t.Fatalf("runRigSettingsSet error: %v", err)
		}

		// Verify
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings, err := config.LoadRigSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}
		if settings.RoleAgents == nil {
			t.Fatal("RoleAgents is nil")
		}
		if settings.RoleAgents["witness"] != "gemini" {
			t.Errorf("RoleAgents[witness] = %q, want %q", settings.RoleAgents["witness"], "gemini")
		}
	})

	t.Run("sets deeply nested keys", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// Set merge_queue.max_concurrent
		cmd := rigSettingsSetCmd
		err := runRigSettingsSet(cmd, []string{rigName, "merge_queue.max_concurrent", "5"})
		if err != nil {
			t.Fatalf("runRigSettingsSet error: %v", err)
		}

		// Verify
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings, err := config.LoadRigSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}
		if settings.MergeQueue == nil {
			t.Fatal("MergeQueue is nil")
		}
		if settings.MergeQueue.MaxConcurrent != 5 {
			t.Errorf("MergeQueue.MaxConcurrent = %d, want 5", settings.MergeQueue.MaxConcurrent)
		}
	})

	t.Run("type inference for bool falls back to string", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// parseValue("true") infers bool, but Agent is a string field.
		// setNestedValue should fall back to storing it as the string "true".
		cmd := rigSettingsSetCmd
		err := runRigSettingsSet(cmd, []string{rigName, "agent", "true"})
		if err != nil {
			t.Fatalf("runRigSettingsSet error: %v", err)
		}

		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings, err := config.LoadRigSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}
		if settings.Agent != "true" {
			t.Errorf("Agent = %q, want %q", settings.Agent, "true")
		}
	})

	t.Run("type inference for number", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// Set merge_queue.max_concurrent as number
		cmd := rigSettingsSetCmd
		err := runRigSettingsSet(cmd, []string{rigName, "merge_queue.max_concurrent", "10"})
		if err != nil {
			t.Fatalf("runRigSettingsSet error: %v", err)
		}

		// Verify it's stored as number
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings, err := config.LoadRigSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}
		if settings.MergeQueue.MaxConcurrent != 10 {
			t.Errorf("MergeQueue.MaxConcurrent = %d, want 10", settings.MergeQueue.MaxConcurrent)
		}
	})

	t.Run("number on string field falls back to string", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// parseValue("42") infers int, but Agent is a string field.
		// setNestedValue should fall back to storing it as the string "42".
		cmd := rigSettingsSetCmd
		err := runRigSettingsSet(cmd, []string{rigName, "agent", "42"})
		if err != nil {
			t.Fatalf("runRigSettingsSet error: %v", err)
		}

		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings, err := config.LoadRigSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}
		if settings.Agent != "42" {
			t.Errorf("Agent = %q, want %q", settings.Agent, "42")
		}
	})

	t.Run("false on string field falls back to string", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// parseValue("false") infers bool, but role_agents values are strings.
		cmd := rigSettingsSetCmd
		err := runRigSettingsSet(cmd, []string{rigName, "role_agents.witness", "false"})
		if err != nil {
			t.Fatalf("runRigSettingsSet error: %v", err)
		}

		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings, err := config.LoadRigSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}
		if settings.RoleAgents["witness"] != "false" {
			t.Errorf("RoleAgents[witness] = %q, want %q", settings.RoleAgents["witness"], "false")
		}
	})

	t.Run("type inference for JSON array falls back to string", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// parseValue parses this as a JSON array, but role_agents values are strings.
		// setNestedValue should fall back to storing the raw string representation.
		cmd := rigSettingsSetCmd
		err := runRigSettingsSet(cmd, []string{rigName, "role_agents.witness", `["gemini", "claude"]`})
		if err != nil {
			t.Fatalf("runRigSettingsSet error: %v", err)
		}

		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings, err := config.LoadRigSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}
		// The fallback uses fmt.Sprintf("%v", value) which for a slice gives "[gemini claude]"
		// This is the expected behavior when a JSON array is set on a string field
		if settings.RoleAgents["witness"] == "" {
			t.Error("RoleAgents[witness] should not be empty after set")
		}
	})

	t.Run("creates file if missing", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// Verify file doesn't exist
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		if _, err := os.Stat(settingsPath); err == nil {
			t.Fatal("settings file should not exist yet")
		}

		// Set a value (should create file)
		cmd := rigSettingsSetCmd
		err := runRigSettingsSet(cmd, []string{rigName, "agent", "claude"})
		if err != nil {
			t.Fatalf("runRigSettingsSet error: %v", err)
		}

		// Verify file was created
		if _, err := os.Stat(settingsPath); err != nil {
			t.Fatalf("settings file should exist: %v", err)
		}

		// Verify it's valid JSON
		settings, err := config.LoadRigSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}
		if settings.Agent != "claude" {
			t.Errorf("Agent = %q, want %q", settings.Agent, "claude")
		}
	})

	t.Run("merge behavior for nested objects", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// Create initial settings with merge_queue
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings := config.NewRigSettings()
		if settings.MergeQueue == nil {
			settings.MergeQueue = config.DefaultMergeQueueConfig()
		}
		if err := config.SaveRigSettings(settingsPath, settings); err != nil {
			t.Fatalf("save initial settings: %v", err)
		}

		// Set a nested value (should merge, not replace)
		cmd := rigSettingsSetCmd
		err := runRigSettingsSet(cmd, []string{rigName, "merge_queue.max_concurrent", "7"})
		if err != nil {
			t.Fatalf("runRigSettingsSet error: %v", err)
		}

		// Verify merge_queue still exists and has the new value
		loaded, err := config.LoadRigSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}
		if loaded.MergeQueue == nil {
			t.Fatal("MergeQueue should still exist")
		}
		if loaded.MergeQueue.MaxConcurrent != 7 {
			t.Errorf("MergeQueue.MaxConcurrent = %d, want 7", loaded.MergeQueue.MaxConcurrent)
		}
	})

	t.Run("error case: invalid rig", func(t *testing.T) {
		_, _ = setupTestRigForSettings(t)

		cmd := rigSettingsSetCmd
		err := runRigSettingsSet(cmd, []string{"nonexistent", "agent", "claude"})
		if err == nil {
			t.Fatal("expected error for invalid rig")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("error message should mention 'not found', got: %v", err)
		}
	})

	t.Run("error case: invalid key path", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// Create settings file
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings := config.NewRigSettings()
		if err := config.SaveRigSettings(settingsPath, settings); err != nil {
			t.Fatalf("save settings: %v", err)
		}

		// Try to set a deeply nested path that doesn't make sense
		// This should fail during unmarshaling back to struct
		cmd := rigSettingsSetCmd
		err := runRigSettingsSet(cmd, []string{rigName, "invalid.deeply.nested.path", "value"})
		// This might succeed (creates the path) but fail validation, or might fail earlier
		// The behavior depends on how setNestedValue handles invalid paths
		if err != nil {
			t.Logf("Setting invalid path returned error (expected): %v", err)
		}
	})

	t.Run("error case: unknown top-level key", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// Create settings file
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings := config.NewRigSettings()
		if err := config.SaveRigSettings(settingsPath, settings); err != nil {
			t.Fatalf("save settings: %v", err)
		}

		// Try to set an unknown top-level key (regression test for false success bug)
		cmd := rigSettingsSetCmd
		err := runRigSettingsSet(cmd, []string{rigName, "something_else", "blah"})
		if err == nil {
			t.Fatal("expected error for unknown top-level key 'something_else'")
		}
		if !strings.Contains(err.Error(), "unknown key") {
			t.Errorf("error message should mention 'unknown key', got: %v", err)
		}

		// Verify the file wasn't modified
		loaded, err := config.LoadRigSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}
		if loaded.Agent != "" {
			t.Errorf("Agent should be empty, got %q", loaded.Agent)
		}
	})
}

func TestRigSettingsUnset(t *testing.T) {
	t.Run("unsets top-level keys", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// Create settings with agent set
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings := config.NewRigSettings()
		settings.Agent = "claude"
		if err := config.SaveRigSettings(settingsPath, settings); err != nil {
			t.Fatalf("save settings: %v", err)
		}

		// Unset the agent key
		cmd := rigSettingsUnsetCmd
		err := runRigSettingsUnset(cmd, []string{rigName, "agent"})
		if err != nil {
			t.Fatalf("runRigSettingsUnset error: %v", err)
		}

		// Reload and verify agent is gone
		loaded, err := config.LoadRigSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}
		if loaded.Agent != "" {
			t.Errorf("Agent should be empty after unset, got %q", loaded.Agent)
		}

		// Verify the key is absent from the raw JSON (not just zero-valued)
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatalf("read settings file: %v", err)
		}
		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("unmarshal raw JSON: %v", err)
		}
		if _, exists := raw["agent"]; exists {
			t.Error("agent key should be absent from JSON after unset")
		}
	})

	t.Run("unsets nested keys", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// Create settings with role_agents
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings := config.NewRigSettings()
		settings.RoleAgents = map[string]string{
			"witness":  "gemini",
			"refinery": "claude-sonnet",
		}
		if err := config.SaveRigSettings(settingsPath, settings); err != nil {
			t.Fatalf("save settings: %v", err)
		}

		// Unset just the witness key
		cmd := rigSettingsUnsetCmd
		err := runRigSettingsUnset(cmd, []string{rigName, "role_agents.witness"})
		if err != nil {
			t.Fatalf("runRigSettingsUnset error: %v", err)
		}

		// Reload and verify witness is gone but refinery remains
		loaded, err := config.LoadRigSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}
		if _, exists := loaded.RoleAgents["witness"]; exists {
			t.Error("witness should be removed from role_agents after unset")
		}
		if loaded.RoleAgents["refinery"] != "claude-sonnet" {
			t.Errorf("refinery should still be %q, got %q", "claude-sonnet", loaded.RoleAgents["refinery"])
		}
	})

	t.Run("error case: key not found", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// Create settings file
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings := config.NewRigSettings()
		if err := config.SaveRigSettings(settingsPath, settings); err != nil {
			t.Fatalf("save settings: %v", err)
		}

		// Try to unset non-existent key
		cmd := rigSettingsUnsetCmd
		err := runRigSettingsUnset(cmd, []string{rigName, "nonexistent"})
		if err == nil {
			t.Fatal("expected error for non-existent key")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("error message should mention 'not found', got: %v", err)
		}
	})

	t.Run("error case: invalid rig", func(t *testing.T) {
		_, _ = setupTestRigForSettings(t)

		cmd := rigSettingsUnsetCmd
		err := runRigSettingsUnset(cmd, []string{"nonexistent", "agent"})
		if err == nil {
			t.Fatal("expected error for invalid rig")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("error message should mention 'not found', got: %v", err)
		}
	})

	t.Run("error case: settings file not found", func(t *testing.T) {
		_, rigName := setupTestRigForSettings(t)

		// Don't create settings file
		cmd := rigSettingsUnsetCmd
		err := runRigSettingsUnset(cmd, []string{rigName, "agent"})
		if err == nil {
			t.Fatal("expected error when settings file doesn't exist")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("error message should mention 'not found', got: %v", err)
		}
	})
}

func TestRigSettingsEdgeCases(t *testing.T) {
	t.Run("empty key path", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// Create settings file
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings := config.NewRigSettings()
		if err := config.SaveRigSettings(settingsPath, settings); err != nil {
			t.Fatalf("save settings: %v", err)
		}

		// Try to set with empty key path
		cmd := rigSettingsSetCmd
		err := runRigSettingsSet(cmd, []string{rigName, "", "value"})
		if err == nil {
			t.Fatal("expected error for empty key path")
		}
		if !strings.Contains(err.Error(), "empty") {
			t.Errorf("error message should mention 'empty', got: %v", err)
		}
	})

	t.Run("deeply nested paths", func(t *testing.T) {
		_, rigName := setupTestRigForSettings(t)

		// Set a deeply nested path (though this might not map to actual struct fields)
		// We'll test that the path creation works
		cmd := rigSettingsSetCmd
		err := runRigSettingsSet(cmd, []string{rigName, "a.b.c.d.e", "deepvalue"})
		// This might succeed in creating the path, but fail validation
		// The important thing is it doesn't crash
		if err != nil {
			t.Logf("Deeply nested path returned error (may be expected): %v", err)
		}
	})

	t.Run("special characters in values", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// Set value with special characters
		cmd := rigSettingsSetCmd
		specialValue := `test"value'with\special/chars`
		err := runRigSettingsSet(cmd, []string{rigName, "agent", specialValue})
		if err != nil {
			t.Fatalf("runRigSettingsSet error: %v", err)
		}

		// Verify it's stored correctly
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings, err := config.LoadRigSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}
		if settings.Agent != specialValue {
			t.Errorf("Agent = %q, want %q", settings.Agent, specialValue)
		}
	})

	t.Run("JSON array values", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// Set a JSON array value
		// Note: This might not map to actual RigSettings fields, but tests JSON parsing
		cmd := rigSettingsSetCmd
		jsonArray := `["item1", "item2", "item3"]`
		err := runRigSettingsSet(cmd, []string{rigName, "test_array", jsonArray})
		// This might succeed but the field won't be in the struct
		// We're testing that JSON parsing works
		if err != nil {
			t.Logf("JSON array set returned error (may be expected): %v", err)
		} else {
			// If it succeeded, verify the file is valid JSON
			settingsPath := filepath.Join(rigPath, "settings", "config.json")
			data, err := os.ReadFile(settingsPath)
			if err != nil {
				t.Fatalf("read settings: %v", err)
			}
			var m map[string]interface{}
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatalf("parse JSON: %v", err)
			}
			// The test_array might be in the raw JSON even if not in the struct
			t.Logf("JSON contains: %v", m)
		}
	})

	t.Run("JSON object values", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// Set a JSON object value
		cmd := rigSettingsSetCmd
		jsonObject := `{"key1": "value1", "key2": 42}`
		err := runRigSettingsSet(cmd, []string{rigName, "test_object", jsonObject})
		// Similar to array test - might succeed but not map to struct
		if err != nil {
			t.Logf("JSON object set returned error (may be expected): %v", err)
		} else {
			// Verify file is valid JSON
			settingsPath := filepath.Join(rigPath, "settings", "config.json")
			data, err := os.ReadFile(settingsPath)
			if err != nil {
				t.Fatalf("read settings: %v", err)
			}
			var m map[string]interface{}
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatalf("parse JSON: %v", err)
			}
			t.Logf("JSON contains: %v", m)
		}
	})

	t.Run("multiple set operations preserve other values", func(t *testing.T) {
		townRoot, rigName := setupTestRigForSettings(t)
		rigPath := filepath.Join(townRoot, rigName)

		// Set multiple values
		cmd := rigSettingsSetCmd
		if err := runRigSettingsSet(cmd, []string{rigName, "agent", "claude"}); err != nil {
			t.Fatalf("set agent: %v", err)
		}
		if err := runRigSettingsSet(cmd, []string{rigName, "role_agents.witness", "gemini"}); err != nil {
			t.Fatalf("set witness: %v", err)
		}
		if err := runRigSettingsSet(cmd, []string{rigName, "role_agents.refinery", "claude-sonnet"}); err != nil {
			t.Fatalf("set refinery: %v", err)
		}

		// Verify all values are preserved
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		settings, err := config.LoadRigSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}
		if settings.Agent != "claude" {
			t.Errorf("Agent = %q, want %q", settings.Agent, "claude")
		}
		if settings.RoleAgents == nil {
			t.Fatal("RoleAgents is nil")
		}
		if settings.RoleAgents["witness"] != "gemini" {
			t.Errorf("RoleAgents[witness] = %q, want %q", settings.RoleAgents["witness"], "gemini")
		}
		if settings.RoleAgents["refinery"] != "claude-sonnet" {
			t.Errorf("RoleAgents[refinery] = %q, want %q", settings.RoleAgents["refinery"], "claude-sonnet")
		}
	})
}

// Test helper functions
func TestParseValue(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantType string
		wantVal  interface{}
	}{
		{"boolean true", "true", "bool", true},
		{"boolean false", "false", "bool", false},
		{"integer", "42", "int", 42},
		{"float", "3.14", "float64", 3.14},
		{"JSON array", `["a", "b"]`, "[]interface {}", []interface{}{"a", "b"}},
		{"JSON object", `{"key": "value"}`, "map[string]interface {}", map[string]interface{}{"key": "value"}},
		{"string", "hello", "string", "hello"},
		{"number as string", "123abc", "string", "123abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseValue(tt.input)

			// Type check
			switch tt.wantType {
			case "bool":
				if _, ok := got.(bool); !ok {
					t.Errorf("parseValue(%q) = %T, want bool", tt.input, got)
				}
			case "int":
				if _, ok := got.(int); !ok {
					t.Errorf("parseValue(%q) = %T, want int", tt.input, got)
				}
			case "float64":
				if _, ok := got.(float64); !ok {
					t.Errorf("parseValue(%q) = %T, want float64", tt.input, got)
				}
			case "string":
				if _, ok := got.(string); !ok {
					t.Errorf("parseValue(%q) = %T, want string", tt.input, got)
				}
			}

			// Value check for simple types
			if tt.wantType == "bool" || tt.wantType == "int" || tt.wantType == "string" {
				if got != tt.wantVal {
					t.Errorf("parseValue(%q) = %v, want %v", tt.input, got, tt.wantVal)
				}
			}
		})
	}
}

// TestRigSettingsWriterReaderAgree is the regression test for hq-am01: it proves
// that values written by `gt rig settings set merge_queue.*` (writer →
// settings/config.json) are the values the refinery resolves via LoadConfig
// (reader). Before the fix the reader read the rig-root config.json and silently
// fell back to DefaultMergeQueueConfig (on_conflict=assign_back, max_concurrent=1).
func TestRigSettingsWriterReaderAgree(t *testing.T) {
	townRoot, rigName := setupTestRigForSettings(t)
	rigPath := filepath.Join(townRoot, rigName)

	// Writer path: drive the real `gt rig settings set` handlers.
	if err := runRigSettingsSet(rigSettingsSetCmd, []string{rigName, "merge_queue.max_concurrent", "3"}); err != nil {
		t.Fatalf("set merge_queue.max_concurrent: %v", err)
	}
	if err := runRigSettingsSet(rigSettingsSetCmd, []string{rigName, "merge_queue.on_conflict", "auto_rebase"}); err != nil {
		t.Fatalf("set merge_queue.on_conflict: %v", err)
	}

	// Sanity: the writer wrote settings/config.json (canonical path).
	settingsPath := filepath.Join(rigPath, "settings", "config.json")
	if _, err := os.Stat(settingsPath); err != nil {
		t.Fatalf("expected writer to create %s: %v", settingsPath, err)
	}

	// Reader path: exactly what the refinery's LoadConfig resolves.
	r := &rig.Rig{Name: rigName, Path: rigPath}
	cfg, srcPath, err := refinery.EffectiveConfig(r)
	if err != nil {
		t.Fatalf("EffectiveConfig: %v", err)
	}

	if srcPath != settingsPath {
		t.Errorf("effective config source = %q, want canonical %q", srcPath, settingsPath)
	}
	if cfg.MaxConcurrent != 3 {
		t.Errorf("MaxConcurrent = %d, want 3 (writer value, not default 1)", cfg.MaxConcurrent)
	}
	if cfg.OnConflict != "auto_rebase" {
		t.Errorf("OnConflict = %q, want %q (writer value, not default assign_back)", cfg.OnConflict, "auto_rebase")
	}
}

// TestRigSettingsShowEffective verifies `gt rig settings show --effective` runs
// against the canonical file and surfaces resolved values (with defaults applied
// for unset fields).
func TestRigSettingsShowEffective(t *testing.T) {
	townRoot, rigName := setupTestRigForSettings(t)
	rigPath := filepath.Join(townRoot, rigName)

	if err := runRigSettingsSet(rigSettingsSetCmd, []string{rigName, "merge_queue.max_concurrent", "4"}); err != nil {
		t.Fatalf("set merge_queue.max_concurrent: %v", err)
	}

	// Drive the --effective path; must not error and must read the set value.
	prev := rigSettingsShowEffective
	rigSettingsShowEffective = true
	t.Cleanup(func() { rigSettingsShowEffective = prev })

	if err := runRigSettingsShow(rigSettingsShowCmd, []string{rigName}); err != nil {
		t.Fatalf("runRigSettingsShow --effective: %v", err)
	}

	// Confirm the underlying resolver returns the set value plus defaults.
	r := &rig.Rig{Name: rigName, Path: rigPath}
	cfg, _, err := refinery.EffectiveConfig(r)
	if err != nil {
		t.Fatalf("EffectiveConfig: %v", err)
	}
	if cfg.MaxConcurrent != 4 {
		t.Errorf("MaxConcurrent = %d, want 4", cfg.MaxConcurrent)
	}
	// Default preserved for an unset field.
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v, want default 30s", cfg.PollInterval)
	}
}

// TestRigSettingsEffectiveLegacyFallback verifies that a rig with only a legacy
// rig-root config.json merge_queue (no settings/config.json) still resolves via
// the fallback, so the mayor's earlier hand-patched stopgaps keep working until
// removed.
func TestRigSettingsEffectiveLegacyFallback(t *testing.T) {
	tmpDir := t.TempDir()
	legacy := map[string]interface{}{
		"type":    "rig",
		"version": 1,
		"merge_queue": map[string]interface{}{
			"max_concurrent": 7,
			"on_conflict":    "auto_rebase",
		},
	}
	data, _ := json.MarshalIndent(legacy, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{Name: "legacy-rig", Path: tmpDir}
	cfg, srcPath, err := refinery.EffectiveConfig(r)
	if err != nil {
		t.Fatalf("EffectiveConfig: %v", err)
	}
	if srcPath != filepath.Join(tmpDir, "config.json") {
		t.Errorf("source = %q, want legacy rig-root config.json", srcPath)
	}
	if cfg.MaxConcurrent != 7 {
		t.Errorf("MaxConcurrent = %d, want 7 (legacy fallback)", cfg.MaxConcurrent)
	}
	if cfg.OnConflict != "auto_rebase" {
		t.Errorf("OnConflict = %q, want auto_rebase (legacy fallback)", cfg.OnConflict)
	}
}
