package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/testutil"
)

func TestConfigMixAppliesRolesAndCrew(t *testing.T) {
	townRoot := setupTestTownForConfig(t)
	settingsPath := config.TownSettingsPath(townRoot)
	if err := config.SaveTownSettings(settingsPath, config.NewTownSettings()); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	gtBinary := buildGT(t)
	env := testutil.CleanGTEnv("HOME=" + t.TempDir())

	output := runGTCmdOutput(t, gtBinary, townRoot, env,
		"config", "mix", "default=codex", "mayor=pi:high", "crew=codex", "crew:alice=pi")
	if !strings.Contains(output, "Mixed town") {
		t.Fatalf("apply output missing mixed banner:\n%s", output)
	}
	if !strings.Contains(output, "pi") || !strings.Contains(output, "codex") {
		t.Fatalf("apply output missing providers:\n%s", output)
	}
	if !strings.Contains(output, "mayor") || !strings.Contains(output, "alice") {
		t.Fatalf("apply output missing role or crew rows:\n%s", output)
	}

	got, err := config.LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		t.Fatalf("LoadOrCreateTownSettings: %v", err)
	}
	if got.DefaultAgent != "codex" {
		t.Fatalf("DefaultAgent = %q, want codex", got.DefaultAgent)
	}
	if got.RoleAgents["mayor"] != "pi" || got.RoleEffort["mayor"] != "high" {
		t.Fatalf("mayor assignment = %q / %q", got.RoleAgents["mayor"], got.RoleEffort["mayor"])
	}
	if got.RoleAgents["crew"] != "codex" {
		t.Fatalf("RoleAgents[crew] = %q, want codex", got.RoleAgents["crew"])
	}
	if got.CrewAgents["alice"] != "pi" {
		t.Fatalf("CrewAgents[alice] = %q, want pi", got.CrewAgents["alice"])
	}

	listOutput := runGTCmdOutput(t, gtBinary, townRoot, env, "config", "mix")
	if !strings.Contains(listOutput, "MIXED") && !strings.Contains(listOutput, "Mixed") {
		t.Fatalf("list output missing mixed marker:\n%s", listOutput)
	}
	if !strings.Contains(listOutput, "alice") || !strings.Contains(listOutput, "high") {
		t.Fatalf("list output missing crew or effort:\n%s", listOutput)
	}
}

func TestConfigMixJSONListsEffectiveAssignments(t *testing.T) {
	townRoot := setupTestTownForConfig(t)
	settings := config.NewTownSettings()
	settings.DefaultAgent = "codex"
	settings.RoleAgents = map[string]string{"mayor": "pi"}
	if err := config.SaveTownSettings(config.TownSettingsPath(townRoot), settings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	gtBinary := buildGT(t)
	env := testutil.CleanGTEnv("HOME=" + t.TempDir())
	output := runGTCmdOutput(t, gtBinary, townRoot, env, "config", "mix", "--json")

	var mix struct {
		config.TownMix
		Binaries []config.MixBinary `json:"binaries"`
	}
	if err := json.Unmarshal([]byte(output), &mix); err != nil {
		t.Fatalf("unmarshal mix JSON: %v\n%s", err, output)
	}
	if !mix.Mixed || mix.DefaultAgent != "codex" {
		t.Fatalf("JSON mix = %+v, want mixed codex default", mix)
	}
	if len(mix.Binaries) == 0 {
		t.Fatalf("JSON mix missing binaries:\n%s", output)
	}
}

func TestConfigMixRejectsInvalidSpecWithoutPersisting(t *testing.T) {
	townRoot := setupTestTownForConfig(t)
	settingsPath := config.TownSettingsPath(townRoot)
	settings := config.NewTownSettings()
	settings.RoleAgents = map[string]string{"mayor": "claude"}
	if err := config.SaveTownSettings(settingsPath, settings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	gtBinary := buildGT(t)
	env := testutil.CleanGTEnv("HOME=" + t.TempDir())
	output, err := runGTCmdMayFail(t, gtBinary, townRoot, env,
		"config", "mix", "mayor=pi", "inventor=codex")
	if err == nil {
		t.Fatalf("invalid mix succeeded; output:\n%s", output)
	}
	if !strings.Contains(output, `unknown role "inventor"`) {
		t.Fatalf("invalid mix output = %q, want unknown role", output)
	}

	got, loadErr := config.LoadOrCreateTownSettings(settingsPath)
	if loadErr != nil {
		t.Fatalf("LoadOrCreateTownSettings: %v", loadErr)
	}
	if got.RoleAgents["mayor"] != "claude" {
		t.Fatalf("RoleAgents[mayor] = %q after rejected mix, want claude", got.RoleAgents["mayor"])
	}
}
