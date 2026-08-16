package cmd

import (
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/testutil"
)

func TestConfigRoleCommands(t *testing.T) {
	townRoot := setupTestTownForConfig(t)
	settingsPath := config.TownSettingsPath(townRoot)
	settings := config.NewTownSettings()
	settings.Agents = map[string]*config.RuntimeConfig{
		"pi-luna": {
			Provider: "pi",
			Command:  "pi",
			Args:     []string{"--model", "openai-codex/gpt-5.6-luna"},
		},
	}
	if err := config.SaveTownSettings(settingsPath, settings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	gtBinary := buildGT(t)
	env := testutil.CleanGTEnv("HOME=" + t.TempDir())

	setOutput := runGTCmdOutput(t, gtBinary, townRoot, env,
		"config", "role", "set", "witness", "pi-luna", "low")
	if !strings.Contains(setOutput, "Set role witness to agent pi-luna with low effort") {
		t.Fatalf("set output = %q, want confirmation", setOutput)
	}

	got, err := config.LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		t.Fatalf("LoadOrCreateTownSettings after set: %v", err)
	}
	if got.RoleAgents["witness"] != "pi-luna" {
		t.Fatalf("RoleAgents[witness] = %q, want pi-luna", got.RoleAgents["witness"])
	}
	if got.RoleEffort["witness"] != "low" {
		t.Fatalf("RoleEffort[witness] = %q, want low", got.RoleEffort["witness"])
	}

	listOutput := runGTCmdOutput(t, gtBinary, townRoot, env, "config", "role", "list")
	if !strings.Contains(listOutput, "ROLE") ||
		!strings.Contains(listOutput, "witness") ||
		!strings.Contains(listOutput, "pi-luna") ||
		!strings.Contains(listOutput, "low") {
		t.Fatalf("list output does not show witness assignment:\n%s", listOutput)
	}

	invalidOutput, invalidErr := runGTCmdMayFail(t, gtBinary, townRoot, env,
		"config", "role", "set", "witness", "pi-luna", "extreme")
	if invalidErr == nil {
		t.Fatalf("invalid effort succeeded; output:\n%s", invalidOutput)
	}
	if !strings.Contains(invalidOutput, `invalid effort "extreme"`) {
		t.Fatalf("invalid effort output = %q, want validation error", invalidOutput)
	}
	got, err = config.LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		t.Fatalf("LoadOrCreateTownSettings after rejected set: %v", err)
	}
	if got.RoleEffort["witness"] != "low" {
		t.Fatalf("RoleEffort[witness] = %q after rejected set, want low", got.RoleEffort["witness"])
	}

	unsetOutput := runGTCmdOutput(t, gtBinary, townRoot, env,
		"config", "role", "unset", "witness")
	if !strings.Contains(unsetOutput, "Cleared role configuration for witness") {
		t.Fatalf("unset output = %q, want confirmation", unsetOutput)
	}
	got, err = config.LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		t.Fatalf("LoadOrCreateTownSettings after unset: %v", err)
	}
	if _, ok := got.RoleAgents["witness"]; ok {
		t.Fatal("RoleAgents[witness] remains after unset")
	}
	if _, ok := got.RoleEffort["witness"]; ok {
		t.Fatal("RoleEffort[witness] remains after unset")
	}
}

func TestConfigRoleCommandRejectsInvalidArguments(t *testing.T) {
	townRoot := setupTestTownForConfig(t)
	gtBinary := buildGT(t)
	env := testutil.CleanGTEnv("HOME=" + t.TempDir())

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing agent argument", args: []string{"config", "role", "set", "witness"}, want: "accepts between 2 and 3 arg(s)"},
		{name: "unknown role", args: []string{"config", "role", "set", "inventor", "pi", "low"}, want: `unknown role "inventor"`},
		{name: "unknown agent", args: []string{"config", "role", "set", "witness", "missing-agent", "low"}, want: `agent "missing-agent" not found`},
		{name: "unknown effort", args: []string{"config", "role", "set", "witness", "pi", "extreme"}, want: `invalid effort "extreme"`},
		{name: "unset missing role", args: []string{"config", "role", "unset"}, want: "accepts 1 arg(s)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := runGTCmdMayFail(t, gtBinary, townRoot, env, tt.args...)
			if err == nil {
				t.Fatalf("gt %v succeeded; output:\n%s", tt.args, output)
			}
			if !strings.Contains(output, tt.want) {
				t.Fatalf("gt %v output = %q, want substring %q", tt.args, output, tt.want)
			}
		})
	}
}
