package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetTownRole(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	settings := NewTownSettings()
	settings.CostTier = string(TierEconomy)
	settings.Agents = map[string]*RuntimeConfig{
		"pi-luna": {
			Provider: "pi",
			Command:  "pi",
			Args:     []string{"--model", "openai-codex/gpt-5.6-luna"},
		},
	}
	settings.RoleEffort = map[string]string{"witness": "medium"}
	if err := SaveTownSettings(settingsPath, settings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	if err := SetTownRole(settingsPath, "witness", "pi-luna", "low"); err != nil {
		t.Fatalf("SetTownRole: %v", err)
	}

	got, err := LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		t.Fatalf("LoadOrCreateTownSettings: %v", err)
	}
	if got.RoleAgents["witness"] != "pi-luna" {
		t.Fatalf("RoleAgents[witness] = %q, want pi-luna", got.RoleAgents["witness"])
	}
	if got.RoleEffort["witness"] != "low" {
		t.Fatalf("RoleEffort[witness] = %q, want low", got.RoleEffort["witness"])
	}
	if got.CostTier != "" {
		t.Fatalf("CostTier = %q, want empty after explicit role configuration", got.CostTier)
	}
}

func TestSetTownRoleWithoutEffortPreservesEffort(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	settings := NewTownSettings()
	settings.RoleEffort = map[string]string{"mayor": "high"}
	if err := SaveTownSettings(settingsPath, settings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	if err := SetTownRole(settingsPath, "mayor", "pi", ""); err != nil {
		t.Fatalf("SetTownRole: %v", err)
	}

	got, err := LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		t.Fatalf("LoadOrCreateTownSettings: %v", err)
	}
	if got.RoleEffort["mayor"] != "high" {
		t.Fatalf("RoleEffort[mayor] = %q, want high", got.RoleEffort["mayor"])
	}
}

func TestSetTownRoleRejectsPreservedEffortUnsupportedByNewAgent(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	settings := NewTownSettings()
	settings.Agents = map[string]*RuntimeConfig{
		"pi-luna": {Provider: "pi", Command: "pi"},
	}
	settings.RoleAgents = map[string]string{"mayor": "pi-luna"}
	settings.RoleEffort = map[string]string{"mayor": "xhigh"}
	if err := SaveTownSettings(settingsPath, settings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	if err := SetTownRole(settingsPath, "mayor", "claude", ""); err == nil {
		t.Fatal("SetTownRole accepted preserved Pi-only effort for Claude")
	}

	got, err := LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		t.Fatalf("LoadOrCreateTownSettings: %v", err)
	}
	if got.RoleAgents["mayor"] != "pi-luna" || got.RoleEffort["mayor"] != "xhigh" {
		t.Fatalf("rejected update mutated role settings: agent=%q effort=%q", got.RoleAgents["mayor"], got.RoleEffort["mayor"])
	}
}

func TestSetTownRoleAcceptsPiSpecificEffort(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	settings := NewTownSettings()
	settings.Agents = map[string]*RuntimeConfig{
		"pi-luna": {Provider: "pi", Command: "pi"},
	}
	if err := SaveTownSettings(settingsPath, settings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	if err := SetTownRole(settingsPath, "mayor", "pi-luna", "xhigh"); err != nil {
		t.Fatalf("SetTownRole with Pi xhigh effort: %v", err)
	}
}

func TestSetTownRoleRejectsInvalidConfigurationWithoutPersisting(t *testing.T) {
	tests := []struct {
		name   string
		role   string
		agent  string
		effort string
	}{
		{name: "role", role: "inventor", agent: "pi", effort: "low"},
		{name: "agent", role: "witness", agent: "missing-agent", effort: "low"},
		{name: "effort", role: "witness", agent: "pi", effort: "extreme"},
		{name: "non-Pi effort", role: "witness", agent: "claude", effort: "xhigh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settingsPath := filepath.Join(t.TempDir(), "settings.json")
			settings := NewTownSettings()
			settings.RoleAgents = map[string]string{"witness": "claude"}
			if err := SaveTownSettings(settingsPath, settings); err != nil {
				t.Fatalf("SaveTownSettings: %v", err)
			}

			if err := SetTownRole(settingsPath, tt.role, tt.agent, tt.effort); err == nil {
				t.Fatalf("SetTownRole(%q, %q, %q) succeeded, want error", tt.role, tt.agent, tt.effort)
			}

			got, err := LoadOrCreateTownSettings(settingsPath)
			if err != nil {
				t.Fatalf("LoadOrCreateTownSettings: %v", err)
			}
			if got.RoleAgents["witness"] != "claude" {
				t.Fatalf("RoleAgents[witness] = %q after rejected update, want claude", got.RoleAgents["witness"])
			}
		})
	}
}

func TestSetAndUnsetRigRole(t *testing.T) {
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "sample")
	townSettings := NewTownSettings()
	townSettings.Agents = map[string]*RuntimeConfig{
		"pi-luna": {Provider: "pi", Command: "pi", Args: []string{"--model", "openai-codex/gpt-5.6-luna"}},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	if err := SetRigRole(townRoot, rigPath, "polecat", "pi-luna", "xhigh"); err != nil {
		t.Fatalf("SetRigRole: %v", err)
	}
	rigSettings, err := LoadRigSettings(RigSettingsPath(rigPath))
	if err != nil {
		t.Fatalf("LoadRigSettings after set: %v", err)
	}
	if rigSettings.RoleAgents["polecat"] != "pi-luna" {
		t.Fatalf("RoleAgents[polecat] = %q, want pi-luna", rigSettings.RoleAgents["polecat"])
	}
	if rigSettings.RoleEffort["polecat"] != "xhigh" {
		t.Fatalf("RoleEffort[polecat] = %q, want xhigh", rigSettings.RoleEffort["polecat"])
	}

	if err := UnsetRigRole(rigPath, "polecat"); err != nil {
		t.Fatalf("UnsetRigRole: %v", err)
	}
	rigSettings, err = LoadRigSettings(RigSettingsPath(rigPath))
	if err != nil {
		t.Fatalf("LoadRigSettings after unset: %v", err)
	}
	if _, ok := rigSettings.RoleAgents["polecat"]; ok {
		t.Fatal("RoleAgents[polecat] remains after unset")
	}
	if _, ok := rigSettings.RoleEffort["polecat"]; ok {
		t.Fatal("RoleEffort[polecat] remains after unset")
	}
}

func TestRigRoleAgentChangeRejectsPreservedUnsupportedEffort(t *testing.T) {
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "sample")
	townSettings := NewTownSettings()
	townSettings.Agents = map[string]*RuntimeConfig{
		"pi-luna": {Provider: "pi", Command: "pi"},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	rigSettings := NewRigSettings()
	rigSettings.RoleAgents = map[string]string{"polecat": "pi-luna"}
	rigSettings.RoleEffort = map[string]string{"polecat": "xhigh"}
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	if err := SetRigRole(townRoot, rigPath, "polecat", "claude", ""); err == nil {
		t.Fatal("SetRigRole accepted preserved Pi-only effort for Claude")
	}
	if err := ValidateRigRoleAgent(townRoot, rigPath, "polecat", "claude"); err == nil {
		t.Fatal("ValidateRigRoleAgent accepted preserved Pi-only effort for Claude")
	}

	got, err := LoadRigSettings(RigSettingsPath(rigPath))
	if err != nil {
		t.Fatalf("LoadRigSettings: %v", err)
	}
	if got.RoleAgents["polecat"] != "pi-luna" || got.RoleEffort["polecat"] != "xhigh" {
		t.Fatalf("rejected update mutated role settings: agent=%q effort=%q", got.RoleAgents["polecat"], got.RoleEffort["polecat"])
	}
}

func TestRigRoleAgentChangeValidatesInheritedTownEffort(t *testing.T) {
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "sample")
	townSettings := NewTownSettings()
	townSettings.Agents = map[string]*RuntimeConfig{
		"pi-luna": {Provider: "pi", Command: "pi"},
	}
	townSettings.RoleEffort = map[string]string{"witness": "xhigh"}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	if err := SetRigRole(townRoot, rigPath, "witness", "claude", ""); err == nil {
		t.Fatal("SetRigRole accepted inherited Pi-only effort for Claude")
	}
}

func TestSetRigRoleRejectsUnknownRoleAndAgent(t *testing.T) {
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "sample")
	if err := SaveTownSettings(TownSettingsPath(townRoot), NewTownSettings()); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	if err := SetRigRole(townRoot, rigPath, "polecats", "pi", "low"); err == nil {
		t.Fatal("SetRigRole accepted unknown role")
	}
	if err := SetRigRole(townRoot, rigPath, "polecat", "pi-lcoal", "low"); err == nil {
		t.Fatal("SetRigRole accepted unknown agent")
	}
}

func TestValidateRigRoleEffortRejectsDanglingConfiguredAgent(t *testing.T) {
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "sample")
	if err := SaveTownSettings(TownSettingsPath(townRoot), NewTownSettings()); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	rigSettings := NewRigSettings()
	rigSettings.RoleAgents = map[string]string{"witness": "missing-agent"}
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	err := ValidateRigRoleEffort(townRoot, rigPath, "witness", "low")
	if err == nil {
		t.Fatal("ValidateRigRoleEffort accepted dangling configured agent")
	}
	if !strings.Contains(err.Error(), `configured agent "missing-agent" for role "witness" not found`) {
		t.Fatalf("ValidateRigRoleEffort error = %q, want contextual dangling-agent error", err)
	}
}

func TestSetRigRolePropagatesAgentRegistryErrors(t *testing.T) {
	tests := []struct {
		name         string
		registryPath func(string, string) string
		wantContext  string
	}{
		{
			name: "town",
			registryPath: func(townRoot, _ string) string {
				return DefaultAgentRegistryPath(townRoot)
			},
			wantContext: "loading town agent registry",
		},
		{
			name: "rig",
			registryPath: func(_, rigPath string) string {
				return RigAgentRegistryPath(rigPath)
			},
			wantContext: "loading rig agent registry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			townRoot := t.TempDir()
			rigPath := filepath.Join(townRoot, "sample")
			if err := SaveTownSettings(TownSettingsPath(townRoot), NewTownSettings()); err != nil {
				t.Fatalf("SaveTownSettings: %v", err)
			}
			if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
				t.Fatalf("SaveRigSettings: %v", err)
			}
			registryPath := tt.registryPath(townRoot, rigPath)
			if err := os.MkdirAll(filepath.Dir(registryPath), 0o755); err != nil {
				t.Fatalf("MkdirAll registry directory: %v", err)
			}
			if err := os.WriteFile(registryPath, []byte("not json"), 0o600); err != nil {
				t.Fatalf("WriteFile malformed registry: %v", err)
			}

			err := SetRigRole(townRoot, rigPath, "witness", "pi", "low")
			if err == nil {
				t.Fatal("SetRigRole succeeded with malformed agent registry")
			}
			if !strings.Contains(err.Error(), tt.wantContext) || !strings.Contains(err.Error(), registryPath) {
				t.Fatalf("SetRigRole error = %q, want context %q and path %q", err, tt.wantContext, registryPath)
			}
		})
	}
}

func TestUnsetTownRole(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	settings := NewTownSettings()
	settings.CostTier = string(TierBudget)
	settings.RoleAgents = map[string]string{"witness": "pi"}
	settings.RoleEffort = map[string]string{"witness": "low"}
	if err := SaveTownSettings(settingsPath, settings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	if err := UnsetTownRole(settingsPath, "witness"); err != nil {
		t.Fatalf("UnsetTownRole: %v", err)
	}

	got, err := LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		t.Fatalf("LoadOrCreateTownSettings: %v", err)
	}
	if _, ok := got.RoleAgents["witness"]; ok {
		t.Fatal("RoleAgents[witness] remains after unset")
	}
	if _, ok := got.RoleEffort["witness"]; ok {
		t.Fatal("RoleEffort[witness] remains after unset")
	}
	if got.CostTier != "" {
		t.Fatalf("CostTier = %q, want empty after explicit role configuration", got.CostTier)
	}
}

func TestUnsetTownRoleRejectsUnknownRole(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	if err := SaveTownSettings(settingsPath, NewTownSettings()); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	if err := UnsetTownRole(settingsPath, "inventor"); err == nil {
		t.Fatal("UnsetTownRole succeeded, want error")
	}
}
