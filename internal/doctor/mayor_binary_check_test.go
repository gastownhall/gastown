package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/config"
)

func TestMayorBinaryCheck_Metadata(t *testing.T) {
	check := NewMayorBinaryCheck()
	if check.Name() != "mayor-binary" {
		t.Errorf("Name() = %q, want mayor-binary", check.Name())
	}
	if check.Category() != CategoryInfrastructure {
		t.Errorf("Category() = %q, want %q", check.Category(), CategoryInfrastructure)
	}
	if check.CanFix() {
		t.Error("CanFix() should be false")
	}
}

func TestMayorBinaryCheck_NoExplicitMix(t *testing.T) {
	check := NewMayorBinaryCheck()
	result := check.Run(&CheckContext{TownRoot: t.TempDir()})
	if result.Status != StatusOK {
		t.Fatalf("status = %v (%s), want OK when mix is unset", result.Status, result.Message)
	}
}

func TestMayorBinaryCheck_CorruptSettingsIsError(t *testing.T) {
	town := t.TempDir()
	settingsPath := config.TownSettingsPath(town)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}
	if err := os.WriteFile(settingsPath, []byte("{not-json"), 0644); err != nil {
		t.Fatalf("write corrupt settings: %v", err)
	}

	check := NewMayorBinaryCheck()
	result := check.Run(&CheckContext{TownRoot: town})
	if result.Status != StatusError {
		t.Fatalf("status = %v (%s), want Error", result.Status, result.Message)
	}
	if !strings.Contains(strings.ToLower(result.Message), "settings") {
		t.Fatalf("error should mention settings:\n%s", result.Message)
	}
}

func TestMayorBinaryCheck_MissingBinaryIsError(t *testing.T) {
	town := t.TempDir()
	settingsPath := config.TownSettingsPath(town)
	settings := config.NewTownSettings()
	settings.RoleAgents = map[string]string{"mayor": "now-mayor"}
	settings.Agents = map[string]*config.RuntimeConfig{
		"now-mayor": {Command: "gt-now-missing-mayor-bin"},
	}
	if err := config.SaveTownSettings(settingsPath, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	check := NewMayorBinaryCheck()
	result := check.Run(&CheckContext{TownRoot: town})
	if result.Status != StatusError {
		t.Fatalf("status = %v (%s), want Error", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "gt-now-missing-mayor-bin") {
		t.Fatalf("error should name the missing binary:\n%s", result.Message)
	}
}

func TestMayorBinaryCheck_PresentBinaryIsOK(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "gt-now-present-mayor-bin")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	t.Setenv("PATH", bin)

	town := t.TempDir()
	settings := config.NewTownSettings()
	settings.RoleAgents = map[string]string{"mayor": "now-mayor"}
	settings.Agents = map[string]*config.RuntimeConfig{
		"now-mayor": {Command: "gt-now-present-mayor-bin"},
	}
	if err := config.SaveTownSettings(config.TownSettingsPath(town), settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	check := NewMayorBinaryCheck()
	result := check.Run(&CheckContext{TownRoot: town})
	if result.Status != StatusOK {
		t.Fatalf("status = %v (%s), want OK", result.Status, result.Message)
	}
}
