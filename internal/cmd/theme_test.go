package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
)

// setupTestTownForTheme creates a minimal Gas Town workspace for theme tests.
// Returns the town root directory. Caller should chdir into it and restore afterwards.
func setupTestTownForTheme(t *testing.T) string {
	t.Helper()

	townRoot := t.TempDir()

	// Create mayor/town.json (required workspace marker)
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	townConfig := &config.TownConfig{
		Type:      "town",
		Version:   config.CurrentTownVersion,
		Name:      "test-town",
		CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := config.SaveTownConfig(filepath.Join(mayorDir, "town.json"), townConfig); err != nil {
		t.Fatalf("save town.json: %v", err)
	}

	return townRoot
}

func TestSaveRigTheme_PreservesRoleThemes(t *testing.T) {
	townRoot := setupTestTownForTheme(t)
	rigName := "testrig"

	// Create rig settings directory
	settingsDir := filepath.Join(townRoot, rigName, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}

	// Create initial settings with RoleThemes and Custom already set
	initialSettings := &config.RigSettings{
		Type:    "rig-settings",
		Version: config.CurrentRigSettingsVersion,
		Theme: &config.ThemeConfig{
			Name: "ocean",
			RoleThemes: map[string]string{
				"witness":  "rust",
				"refinery": "plum",
			},
			Custom: &config.CustomTheme{
				BG: "#112233",
				FG: "#eeeeff",
			},
		},
	}
	settingsPath := filepath.Join(settingsDir, "config.json")
	if err := config.SaveRigSettings(settingsPath, initialSettings); err != nil {
		t.Fatalf("save initial settings: %v", err)
	}

	// Chdir into the town root so workspace.FindFromCwd works
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origCwd)

	// Call saveRigTheme to change theme name to "forest"
	if err := saveRigTheme(rigName, "forest"); err != nil {
		t.Fatalf("saveRigTheme: %v", err)
	}

	// Reload and verify
	reloaded, err := config.LoadRigSettings(settingsPath)
	if err != nil {
		t.Fatalf("reload settings: %v", err)
	}

	if reloaded.Theme == nil {
		t.Fatal("Theme is nil after save")
	}

	// Theme name should be updated
	if reloaded.Theme.Name != "forest" {
		t.Errorf("Theme.Name = %q, want %q", reloaded.Theme.Name, "forest")
	}

	// RoleThemes should be preserved
	if reloaded.Theme.RoleThemes == nil {
		t.Fatal("RoleThemes is nil after save")
	}
	if got := reloaded.Theme.RoleThemes["witness"]; got != "rust" {
		t.Errorf("RoleThemes[witness] = %q, want %q", got, "rust")
	}
	if got := reloaded.Theme.RoleThemes["refinery"]; got != "plum" {
		t.Errorf("RoleThemes[refinery] = %q, want %q", got, "plum")
	}

	// Custom theme should be cleared when a named theme is selected
	if reloaded.Theme.Custom != nil {
		t.Fatalf("Custom theme = %+v, want nil after selecting named theme", reloaded.Theme.Custom)
	}
}

func TestSaveRigTheme_CreatesNewSettings(t *testing.T) {
	townRoot := setupTestTownForTheme(t)
	rigName := "newrig"

	// Create rig settings directory (but no config.json)
	settingsDir := filepath.Join(townRoot, rigName, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origCwd)

	// Call saveRigTheme on a rig with no existing settings
	if err := saveRigTheme(rigName, "forest"); err != nil {
		t.Fatalf("saveRigTheme: %v", err)
	}

	// Verify the file was created with correct theme
	settingsPath := filepath.Join(settingsDir, "config.json")
	reloaded, err := config.LoadRigSettings(settingsPath)
	if err != nil {
		t.Fatalf("reload settings: %v", err)
	}

	if reloaded.Theme == nil {
		t.Fatal("Theme is nil after save")
	}
	if reloaded.Theme.Name != "forest" {
		t.Errorf("Theme.Name = %q, want %q", reloaded.Theme.Name, "forest")
	}
}

func TestSaveRigTheme_PreservesNonThemeSettings(t *testing.T) {
	townRoot := setupTestTownForTheme(t)
	rigName := "testrig"

	// Create rig settings with merge queue config
	settingsDir := filepath.Join(townRoot, rigName, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}

	initialSettings := config.NewRigSettings()
	initialSettings.Theme = &config.ThemeConfig{Name: "ocean"}
	initialSettings.MergeQueue.OnConflict = "auto_rebase"

	settingsPath := filepath.Join(settingsDir, "config.json")
	if err := config.SaveRigSettings(settingsPath, initialSettings); err != nil {
		t.Fatalf("save initial settings: %v", err)
	}

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origCwd)

	if err := saveRigTheme(rigName, "forest"); err != nil {
		t.Fatalf("saveRigTheme: %v", err)
	}

	// Verify non-theme settings are preserved
	reloaded, err := config.LoadRigSettings(settingsPath)
	if err != nil {
		t.Fatalf("reload settings: %v", err)
	}

	if reloaded.MergeQueue == nil {
		t.Fatal("MergeQueue is nil after save")
	}
	if reloaded.MergeQueue.OnConflict != "auto_rebase" {
		t.Errorf("MergeQueue.OnConflict = %q, want %q", reloaded.MergeQueue.OnConflict, "auto_rebase")
	}
}

func TestSaveRigTheme_RoundTripsJSON(t *testing.T) {
	// Verify that the JSON serialization of ThemeConfig preserves all fields
	original := &config.ThemeConfig{
		Disabled: true,
		Name:     "ocean",
		RoleThemes: map[string]string{
			"witness":  "rust",
			"refinery": "plum",
			"polecat":  "forest",
		},
		Custom: &config.CustomTheme{
			BG: "#001122",
			FG: "#ffeedd",
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var roundtripped config.ThemeConfig
	if err := json.Unmarshal(data, &roundtripped); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if roundtripped.Name != original.Name {
		t.Errorf("Name = %q, want %q", roundtripped.Name, original.Name)
	}
	if roundtripped.Disabled != original.Disabled {
		t.Errorf("Disabled = %v, want %v", roundtripped.Disabled, original.Disabled)
	}
	if len(roundtripped.RoleThemes) != len(original.RoleThemes) {
		t.Errorf("RoleThemes len = %d, want %d", len(roundtripped.RoleThemes), len(original.RoleThemes))
	}
	for k, v := range original.RoleThemes {
		if roundtripped.RoleThemes[k] != v {
			t.Errorf("RoleThemes[%s] = %q, want %q", k, roundtripped.RoleThemes[k], v)
		}
	}
	if roundtripped.Custom == nil {
		t.Fatal("Custom is nil after roundtrip")
	}
	if roundtripped.Custom.BG != original.Custom.BG || roundtripped.Custom.FG != original.Custom.FG {
		t.Errorf("Custom = %+v, want %+v", roundtripped.Custom, original.Custom)
	}
}

func TestSaveRigTheme_DisablesTheming(t *testing.T) {
	townRoot := setupTestTownForTheme(t)
	rigName := "testrig"

	settingsDir := filepath.Join(townRoot, rigName, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origCwd)

	if err := saveRigTheme(rigName, "none"); err != nil {
		t.Fatalf("saveRigTheme: %v", err)
	}

	reloaded, err := config.LoadRigSettings(filepath.Join(settingsDir, "config.json"))
	if err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	if reloaded.Theme == nil {
		t.Fatal("Theme is nil after save")
	}
	if !reloaded.Theme.Disabled {
		t.Fatal("Theme.Disabled = false, want true")
	}
	if reloaded.Theme.Name != "" {
		t.Errorf("Theme.Name = %q, want empty", reloaded.Theme.Name)
	}
	if reloaded.Theme.Custom != nil {
		t.Errorf("Theme.Custom = %+v, want nil", reloaded.Theme.Custom)
	}
}
