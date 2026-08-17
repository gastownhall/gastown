package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/config"
)

func TestProvisionForSettings_CustomCodexAliasDoesNotWarn(t *testing.T) {
	town := config.NewTownSettings()
	town.Agents["codex-cheap"] = &config.RuntimeConfig{
		Provider: "codex",
		Command:  "codex",
		Args:     []string{"-m", "gpt-5.3-codex-spark"},
	}
	if err := ProvisionForSettings(t.TempDir(), "codex-cheap", town); err != nil {
		t.Fatalf("ProvisionForSettings(codex-cheap) = %v", err)
	}
}

func TestProvisionForSettings_CustomClaudeAliasWritesCommands(t *testing.T) {
	town := config.NewTownSettings()
	town.Agents["claude-cheap"] = &config.RuntimeConfig{
		Provider: "claude",
		Command:  "claude",
	}
	dir := t.TempDir()
	if err := ProvisionForSettings(dir, "claude-cheap", town); err != nil {
		t.Fatalf("ProvisionForSettings(claude-cheap) = %v", err)
	}
	cmdPath := filepath.Join(dir, ".claude", "commands", "handoff.md")
	body, err := os.ReadFile(cmdPath)
	if err != nil {
		t.Fatalf("expected Claude command scaffold at %s: %v", cmdPath, err)
	}
	if !strings.Contains(string(body), "allowed-tools: Bash(gt handoff:*)") {
		t.Fatalf("Claude alias did not inherit Claude command templates:\n%s", body)
	}
}

func TestProvisionForSettings_UnknownBinaryStillFails(t *testing.T) {
	town := config.NewTownSettings()
	town.Agents["mystery-bot"] = &config.RuntimeConfig{Command: "mystery-bot"}
	err := ProvisionForSettings(t.TempDir(), "mystery-bot", town)
	if err == nil {
		t.Fatal("expected unknown truly-custom binary to fail")
	}
	if !strings.Contains(err.Error(), "unknown agent or no config dir: mystery-bot") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProvisionFor_BuiltinCodexIsSilent(t *testing.T) {
	if err := ProvisionFor(t.TempDir(), "codex"); err != nil {
		t.Fatalf("ProvisionFor(codex) = %v", err)
	}
}
