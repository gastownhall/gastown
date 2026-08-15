package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncPreservesNonHooksFields(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	// Create a town with one crew member
	crewDir := filepath.Join(tmpDir, "town", "rig1", "crew", "alice")
	os.MkdirAll(crewDir, 0755)

	// Write an existing settings.json with known AND unknown fields
	claudeDir := filepath.Join(crewDir, ".claude")
	os.MkdirAll(claudeDir, 0755)

	existingJSON := `{
  "editorMode": "vim",
  "enabledPlugins": {
    "beads@beads-marketplace": false,
    "custom-plugin": true
  },
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [{"type": "command", "command": "old-prime"}]
      }
    ]
  },
  "futureField": "should-be-preserved",
  "anotherUnknown": {"nested": true}
}
`
	settingsPath := filepath.Join(claudeDir, "settings.json")
	os.WriteFile(settingsPath, []byte(existingJSON), 0600)

	// Save a base config
	base := &HooksConfig{
		SessionStart: []HookEntry{
			{Matcher: "", Hooks: []Hook{{Type: "command", Command: "new-prime"}}},
		},
	}
	if err := SaveBase(base); err != nil {
		t.Fatalf("SaveBase: %v", err)
	}

	// Simulate sync using new API
	overrides, _ := LoadAllOverrides()
	merged := MergeHooks(base, overrides, "rig1/crew")

	existingData, _ := os.ReadFile(settingsPath)
	settings, err := UnmarshalSettings(existingData)
	if err != nil {
		t.Fatalf("UnmarshalSettings: %v", err)
	}
	settings.Hooks = *merged

	newData, err := MarshalSettings(settings)
	if err != nil {
		t.Fatalf("MarshalSettings: %v", err)
	}
	newData = append(newData, '\n')
	os.WriteFile(settingsPath, newData, 0600)

	// Read back and verify
	resultData, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading result: %v", err)
	}

	result, err := UnmarshalSettings(resultData)
	if err != nil {
		t.Fatalf("parsing result: %v", err)
	}

	// Known fields should be preserved
	if result.EditorMode != "vim" {
		t.Errorf("editorMode not preserved: got %q", result.EditorMode)
	}
	if !result.EnabledPlugins["custom-plugin"] {
		t.Error("custom-plugin not preserved")
	}

	// Unknown fields should be preserved
	if _, ok := result.Extra["futureField"]; !ok {
		t.Error("futureField (unknown field) not preserved")
	}
	if _, ok := result.Extra["anotherUnknown"]; !ok {
		t.Error("anotherUnknown (unknown field) not preserved")
	}

	// Hooks should be updated
	if len(result.Hooks.SessionStart) != 1 {
		t.Fatalf("expected 1 SessionStart, got %d", len(result.Hooks.SessionStart))
	}
	if result.Hooks.SessionStart[0].Hooks[0].Command != "new-prime" {
		t.Errorf("hooks not updated: got %q", result.Hooks.SessionStart[0].Hooks[0].Command)
	}
}

func TestSyncCreatesNewSettings(t *testing.T) {
	tmpDir := t.TempDir()
	setTestHome(t, tmpDir)

	// Save base config
	base := &HooksConfig{
		SessionStart: []HookEntry{
			{Matcher: "", Hooks: []Hook{{Type: "command", Command: "gt prime"}}},
		},
	}
	SaveBase(base)

	// Simulate sync: create new file using MarshalSettings
	overrides, _ := LoadAllOverrides()
	merged := MergeHooks(base, overrides, "rig1/crew")

	settings := &SettingsJSON{
		EnabledPlugins: map[string]bool{"beads@beads-marketplace": false},
		Hooks:          *merged,
	}

	settingsPath := filepath.Join(tmpDir, "test-worktree", ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(settingsPath), 0755)
	data, _ := MarshalSettings(settings)
	data = append(data, '\n')
	os.WriteFile(settingsPath, data, 0600)

	// Verify file was created and has correct content
	if _, err := os.Stat(settingsPath); err != nil {
		t.Fatalf("settings.json should exist now: %v", err)
	}

	resultData, _ := os.ReadFile(settingsPath)
	result, _ := UnmarshalSettings(resultData)

	if len(result.Hooks.SessionStart) != 1 {
		t.Error("hooks not written correctly")
	}
}

func TestUnmarshalMarshalRoundtrip(t *testing.T) {
	input := `{
  "editorMode": "vim",
  "enabledPlugins": {"beads@beads-marketplace": false},
  "hooks": {
    "SessionStart": [{"matcher": "", "hooks": [{"type": "command", "command": "gt prime"}]}]
  },
  "customSetting": "hello",
  "nested": {"a": 1, "b": "two"}
}`
	s, err := UnmarshalSettings([]byte(input))
	if err != nil {
		t.Fatalf("UnmarshalSettings: %v", err)
	}

	if s.EditorMode != "vim" {
		t.Errorf("editorMode: got %q", s.EditorMode)
	}
	if len(s.Hooks.SessionStart) != 1 {
		t.Error("hooks not parsed")
	}

	// Roundtrip
	output, err := MarshalSettings(s)
	if err != nil {
		t.Fatalf("MarshalSettings: %v", err)
	}

	// Parse again to verify
	s2, err := UnmarshalSettings(output)
	if err != nil {
		t.Fatalf("re-UnmarshalSettings: %v", err)
	}

	if s2.EditorMode != "vim" {
		t.Error("editorMode lost in roundtrip")
	}
	if _, ok := s2.Extra["customSetting"]; !ok {
		t.Error("customSetting lost in roundtrip")
	}
	if _, ok := s2.Extra["nested"]; !ok {
		t.Error("nested lost in roundtrip")
	}
}

func TestLoadSettingsMissingReturnsZeroValue(t *testing.T) {
	s, err := LoadSettings("/nonexistent/path/settings.json")
	if err != nil {
		t.Fatalf("LoadSettings should not error for missing file, got: %v", err)
	}
	if s.EditorMode != "" || len(s.Hooks.SessionStart) != 0 {
		t.Error("expected zero-value SettingsJSON for missing file")
	}
}

func TestMarshalSettingsEmpty(t *testing.T) {
	s := &SettingsJSON{}
	data, err := MarshalSettings(s)
	if err != nil {
		t.Fatalf("MarshalSettings empty: %v", err)
	}

	// Should produce valid JSON
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}

	// Should have hooks key
	if _, ok := m["hooks"]; !ok {
		t.Error("empty settings should still have hooks key")
	}
}

func TestMarshalSettingsInjectsAutoCompactWindow(t *testing.T) {
	data, err := MarshalSettings(&SettingsJSON{})
	if err != nil {
		t.Fatalf("MarshalSettings: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}
	got, ok := m["autoCompactWindow"].(float64)
	if !ok || int(got) != 150000 {
		t.Fatalf("autoCompactWindow = %v, want 150000", m["autoCompactWindow"])
	}
}

func TestMarshalSettingsDoesNotMutateInput(t *testing.T) {
	input := `{
  "editorMode": "vim",
  "hooks": {},
  "customField": "value"
}`
	s, err := UnmarshalSettings([]byte(input))
	if err != nil {
		t.Fatalf("UnmarshalSettings: %v", err)
	}

	// Snapshot Extra before marshal
	origLen := len(s.Extra)

	_, err = MarshalSettings(s)
	if err != nil {
		t.Fatalf("MarshalSettings: %v", err)
	}

	// Extra should not have been modified
	if len(s.Extra) != origLen {
		t.Errorf("Extra was mutated: had %d keys, now has %d", origLen, len(s.Extra))
	}
}

func TestMarshalSettingsDeletesZeroEditorMode(t *testing.T) {
	input := `{
  "editorMode": "vim",
  "hooks": {}
}`
	s, err := UnmarshalSettings([]byte(input))
	if err != nil {
		t.Fatalf("UnmarshalSettings: %v", err)
	}

	// Clear editor mode
	s.EditorMode = ""

	output, err := MarshalSettings(s)
	if err != nil {
		t.Fatalf("MarshalSettings: %v", err)
	}

	// editorMode should NOT appear in output
	if strings.Contains(string(output), "editorMode") {
		t.Errorf("cleared editorMode still in output: %s", output)
	}
}

func TestMarshalSettingsPreservesFieldOrder(t *testing.T) {
	input := `{
  "editorMode": "vim",
  "hooks": {},
  "customField": "value"
}`
	s, err := UnmarshalSettings([]byte(input))
	if err != nil {
		t.Fatalf("UnmarshalSettings: %v", err)
	}

	output, err := MarshalSettings(s)
	if err != nil {
		t.Fatalf("MarshalSettings: %v", err)
	}

	// Verify all expected fields are present
	outputStr := string(output)
	if !strings.Contains(outputStr, "editorMode") {
		t.Error("output missing editorMode")
	}
	if !strings.Contains(outputStr, "hooks") {
		t.Error("output missing hooks")
	}
	if !strings.Contains(outputStr, "customField") {
		t.Error("output missing customField")
	}
}
