package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jonbaldie/gastown/internal/config"
)

func TestNewGroqCompoundCheck(t *testing.T) {
	c := NewGroqCompoundCheck()
	if c.Name() != "groq-compound-json" {
		t.Errorf("Name() = %q, want %q", c.Name(), "groq-compound-json")
	}
	if c.Description() == "" {
		t.Error("Description() is empty")
	}
	if c.Category() != CategoryInfrastructure {
		t.Errorf("Category() = %v, want %v", c.Category(), CategoryInfrastructure)
	}
	if c.CanFix() {
		t.Error("CanFix() = true, want false")
	}
}

func TestGroqCompoundConfigured_EmptyTownRoot(t *testing.T) {
	c := NewGroqCompoundCheck()
	if c.groqCompoundConfigured("") {
		t.Error("groqCompoundConfigured(\"\") = true, want false")
	}
}

func TestGroqCompoundConfigured_NoSettingsFile(t *testing.T) {
	tmp := t.TempDir()
	c := NewGroqCompoundCheck()
	if c.groqCompoundConfigured(tmp) {
		t.Error("groqCompoundConfigured with no settings file = true, want false (default agent is claude)")
	}
}

func TestGroqCompoundConfigured_DefaultAgent(t *testing.T) {
	tmp := t.TempDir()
	settings := config.NewTownSettings()
	settings.DefaultAgent = string(config.AgentGroqCompound)
	writeTownSettings(t, tmp, settings)

	c := NewGroqCompoundCheck()
	if !c.groqCompoundConfigured(tmp) {
		t.Error("groqCompoundConfigured = false, want true when DefaultAgent is groq-compound")
	}
}

func TestGroqCompoundConfigured_RoleAgent(t *testing.T) {
	tmp := t.TempDir()
	settings := config.NewTownSettings()
	settings.RoleAgents["refinery"] = string(config.AgentGroqCompound)
	writeTownSettings(t, tmp, settings)

	c := NewGroqCompoundCheck()
	if !c.groqCompoundConfigured(tmp) {
		t.Error("groqCompoundConfigured = false, want true when a role uses groq-compound")
	}
}

func TestGroqCompoundConfigured_NoMatch(t *testing.T) {
	tmp := t.TempDir()
	settings := config.NewTownSettings()
	settings.RoleAgents["refinery"] = "claude"
	writeTownSettings(t, tmp, settings)

	c := NewGroqCompoundCheck()
	if c.groqCompoundConfigured(tmp) {
		t.Error("groqCompoundConfigured = true, want false when no role uses groq-compound")
	}
}

func TestGroqCompoundCheck_Run_SkipWhenNotConfigured(t *testing.T) {
	tmp := t.TempDir()
	c := NewGroqCompoundCheck()
	res := c.Run(&CheckContext{TownRoot: tmp})
	if res.Status != StatusOK {
		t.Errorf("Run status = %v, want OK (skip path)", res.Status)
	}
}

func TestGroqCompoundCheck_Run_WarnWhenNoAPIKey(t *testing.T) {
	tmp := t.TempDir()
	settings := config.NewTownSettings()
	settings.DefaultAgent = string(config.AgentGroqCompound)
	writeTownSettings(t, tmp, settings)

	t.Setenv("GROQ_API_KEY", "")
	c := NewGroqCompoundCheck()
	res := c.Run(&CheckContext{TownRoot: tmp})
	if res.Status != StatusWarning {
		t.Errorf("Run status = %v, want Warning when GROQ_API_KEY missing", res.Status)
	}
}

func writeTownSettings(t *testing.T, townRoot string, settings *config.TownSettings) {
	t.Helper()
	dir := filepath.Join(townRoot, "settings")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}
	if err := config.SaveTownSettings(config.TownSettingsPath(townRoot), settings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
}
