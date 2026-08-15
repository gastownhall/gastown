package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/testutil"
)

func TestRigRoleCommands(t *testing.T) {
	gtBinary := buildGT(t)
	townRoot, rigName := setupTestRigForSettings(t)
	rigPath := filepath.Join(townRoot, rigName)
	townSettings := config.NewTownSettings()
	townSettings.Agents = map[string]*config.RuntimeConfig{
		"pi-luna": {Provider: "pi", Command: "pi", Args: []string{"--model", "openai-codex/gpt-5.6-luna"}},
	}
	if err := config.SaveTownSettings(config.TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	env := testutil.CleanGTEnv("HOME=" + t.TempDir())
	setOutput := runGTCmdOutput(t, gtBinary, townRoot, env,
		"rig", "role", "set", rigName, "polecat", "pi-luna", "xhigh")
	if !strings.Contains(setOutput, "Set rig testrig role polecat to agent pi-luna with xhigh effort") {
		t.Fatalf("set output = %q, want confirmation", setOutput)
	}

	settings, err := config.LoadRigSettings(config.RigSettingsPath(rigPath))
	if err != nil {
		t.Fatalf("LoadRigSettings after set: %v", err)
	}
	if settings.RoleAgents["polecat"] != "pi-luna" || settings.RoleEffort["polecat"] != "xhigh" {
		t.Fatalf("rig role setting = agent %q effort %q, want pi-luna/xhigh",
			settings.RoleAgents["polecat"], settings.RoleEffort["polecat"])
	}

	listOutput := runGTCmdOutput(t, gtBinary, townRoot, env, "rig", "role", "list", rigName)
	if !strings.Contains(listOutput, "polecat") ||
		!strings.Contains(listOutput, "pi-luna") ||
		!strings.Contains(listOutput, "xhigh") {
		t.Fatalf("list output does not show polecat assignment:\n%s", listOutput)
	}

	invalidOutput, invalidErr := runGTCmdMayFail(t, gtBinary, townRoot, env,
		"rig", "role", "set", rigName, "polecats", "pi-luna", "low")
	if invalidErr == nil {
		t.Fatalf("invalid rig role succeeded; output:\n%s", invalidOutput)
	}
	if !strings.Contains(invalidOutput, `unknown role "polecats"`) {
		t.Fatalf("invalid role output = %q, want validation error", invalidOutput)
	}

	unsetOutput := runGTCmdOutput(t, gtBinary, townRoot, env,
		"rig", "role", "unset", rigName, "polecat")
	if !strings.Contains(unsetOutput, "Cleared rig testrig role configuration for polecat") {
		t.Fatalf("unset output = %q, want confirmation", unsetOutput)
	}
	settings, err = config.LoadRigSettings(config.RigSettingsPath(rigPath))
	if err != nil {
		t.Fatalf("LoadRigSettings after unset: %v", err)
	}
	if _, ok := settings.RoleAgents["polecat"]; ok {
		t.Fatal("RoleAgents[polecat] remains after unset")
	}
	if _, ok := settings.RoleEffort["polecat"]; ok {
		t.Fatal("RoleEffort[polecat] remains after unset")
	}
}

func TestRigRoleListRejectsMalformedSettings(t *testing.T) {
	gtBinary := buildGT(t)
	townRoot, rigName := setupTestRigForSettings(t)
	rigPath := filepath.Join(townRoot, rigName)
	settingsPath := config.RigSettingsPath(rigPath)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatalf("MkdirAll settings directory: %v", err)
	}
	if err := os.WriteFile(settingsPath, []byte("not json"), 0o600); err != nil {
		t.Fatalf("WriteFile malformed rig settings: %v", err)
	}

	env := testutil.CleanGTEnv("HOME=" + t.TempDir())
	output, err := runGTCmdMayFail(t, gtBinary, townRoot, env,
		"rig", "role", "list", rigName)
	if err == nil {
		t.Fatalf("rig role list succeeded with malformed settings; output:\n%s", output)
	}
	if !strings.Contains(output, "loading rig settings") || !strings.Contains(output, settingsPath) {
		t.Fatalf("rig role list output = %q, want context and path %q", output, settingsPath)
	}
}
