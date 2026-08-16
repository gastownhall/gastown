package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseMixSpec_RoleAgent(t *testing.T) {
	got, err := ParseMixSpec("mayor=pi")
	if err != nil {
		t.Fatalf("ParseMixSpec: %v", err)
	}
	want := MixAssignment{Kind: MixKindRole, Name: "mayor", Agent: "pi"}
	if got != want {
		t.Fatalf("ParseMixSpec = %+v, want %+v", got, want)
	}
}

func TestParseMixSpec_RoleAgentEffort(t *testing.T) {
	got, err := ParseMixSpec("mayor=pi:high")
	if err != nil {
		t.Fatalf("ParseMixSpec: %v", err)
	}
	want := MixAssignment{Kind: MixKindRole, Name: "mayor", Agent: "pi", Effort: "high"}
	if got != want {
		t.Fatalf("ParseMixSpec = %+v, want %+v", got, want)
	}
}

func TestParseMixSpec_CrewAgent(t *testing.T) {
	got, err := ParseMixSpec("crew:alice=codex")
	if err != nil {
		t.Fatalf("ParseMixSpec: %v", err)
	}
	want := MixAssignment{Kind: MixKindCrew, Name: "alice", Agent: "codex"}
	if got != want {
		t.Fatalf("ParseMixSpec = %+v, want %+v", got, want)
	}
}

func TestParseMixSpec_DefaultAgent(t *testing.T) {
	got, err := ParseMixSpec("default=codex")
	if err != nil {
		t.Fatalf("ParseMixSpec: %v", err)
	}
	want := MixAssignment{Kind: MixKindDefault, Agent: "codex"}
	if got != want {
		t.Fatalf("ParseMixSpec = %+v, want %+v", got, want)
	}
}

func TestParseMixSpec_RejectsMalformed(t *testing.T) {
	tests := []struct {
		spec string
		want string
	}{
		{spec: "mayor", want: "expected target=agent"},
		{spec: "=pi", want: "empty target"},
		{spec: "mayor=", want: "empty agent"},
		{spec: "crew:=pi", want: "empty crew name"},
		{spec: "crew:alice=", want: "empty agent"},
		{spec: "default=codex:high", want: "default agent cannot set effort"},
		{spec: "crew:alice=pi:high", want: "crew assignment cannot set effort"},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			_, err := ParseMixSpec(tt.spec)
			if err == nil {
				t.Fatal("ParseMixSpec succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParseMixSpec(%q) error = %q, want substring %q", tt.spec, err, tt.want)
			}
		})
	}
}

func TestParseMixSpecs(t *testing.T) {
	got, err := ParseMixSpecs([]string{"default=codex", "mayor=pi:high", "crew:alice=pi"})
	if err != nil {
		t.Fatalf("ParseMixSpecs: %v", err)
	}
	want := []MixAssignment{
		{Kind: MixKindDefault, Agent: "codex"},
		{Kind: MixKindRole, Name: "mayor", Agent: "pi", Effort: "high"},
		{Kind: MixKindCrew, Name: "alice", Agent: "pi"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseMixSpecs = %#v, want %#v", got, want)
	}
}

func TestApplyTownMix_AssignsRolesAndCrewAtomically(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	settings := NewTownSettings()
	settings.CostTier = string(TierEconomy)
	settings.RoleAgents = map[string]string{"witness": "claude"}
	if err := SaveTownSettings(settingsPath, settings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	assignments := []MixAssignment{
		{Kind: MixKindDefault, Agent: "codex"},
		{Kind: MixKindRole, Name: "mayor", Agent: "pi", Effort: "high"},
		{Kind: MixKindRole, Name: "crew", Agent: "codex"},
		{Kind: MixKindCrew, Name: "alice", Agent: "pi"},
	}
	if err := ApplyTownMix(settingsPath, assignments); err != nil {
		t.Fatalf("ApplyTownMix: %v", err)
	}

	got, err := LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		t.Fatalf("LoadOrCreateTownSettings: %v", err)
	}
	if got.DefaultAgent != "codex" {
		t.Fatalf("DefaultAgent = %q, want codex", got.DefaultAgent)
	}
	if got.RoleAgents["mayor"] != "pi" {
		t.Fatalf("RoleAgents[mayor] = %q, want pi", got.RoleAgents["mayor"])
	}
	if got.RoleEffort["mayor"] != "high" {
		t.Fatalf("RoleEffort[mayor] = %q, want high", got.RoleEffort["mayor"])
	}
	if got.RoleAgents["crew"] != "codex" {
		t.Fatalf("RoleAgents[crew] = %q, want codex", got.RoleAgents["crew"])
	}
	if got.CrewAgents["alice"] != "pi" {
		t.Fatalf("CrewAgents[alice] = %q, want pi", got.CrewAgents["alice"])
	}
	if got.RoleAgents["witness"] != "claude" {
		t.Fatalf("RoleAgents[witness] = %q, want preserved claude", got.RoleAgents["witness"])
	}
	if got.CostTier != "" {
		t.Fatalf("CostTier = %q, want empty after explicit mix", got.CostTier)
	}
}

func TestApplyTownMix_RejectsInvalidWithoutPersisting(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	settings := NewTownSettings()
	settings.RoleAgents = map[string]string{"mayor": "claude"}
	if err := SaveTownSettings(settingsPath, settings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	err := ApplyTownMix(settingsPath, []MixAssignment{
		{Kind: MixKindRole, Name: "mayor", Agent: "pi"},
		{Kind: MixKindRole, Name: "inventor", Agent: "codex"},
	})
	if err == nil {
		t.Fatal("ApplyTownMix succeeded, want error")
	}
	if !strings.Contains(err.Error(), `unknown role "inventor"`) {
		t.Fatalf("ApplyTownMix error = %q, want unknown role", err)
	}

	got, err := LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		t.Fatalf("LoadOrCreateTownSettings: %v", err)
	}
	if got.RoleAgents["mayor"] != "claude" {
		t.Fatalf("RoleAgents[mayor] = %q after rejected mix, want claude", got.RoleAgents["mayor"])
	}
}

func TestApplyTownMix_RejectsUnknownAgentAndDuplicateTargets(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	if err := SaveTownSettings(settingsPath, NewTownSettings()); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	err := ApplyTownMix(settingsPath, []MixAssignment{
		{Kind: MixKindRole, Name: "mayor", Agent: "missing-agent"},
	})
	if err == nil || !strings.Contains(err.Error(), `agent "missing-agent" not found`) {
		t.Fatalf("unknown agent error = %v", err)
	}

	err = ApplyTownMix(settingsPath, []MixAssignment{
		{Kind: MixKindRole, Name: "mayor", Agent: "pi"},
		{Kind: MixKindRole, Name: "mayor", Agent: "codex"},
	})
	if err == nil || !strings.Contains(err.Error(), `duplicate assignment for role mayor`) {
		t.Fatalf("duplicate role error = %v", err)
	}

	err = ApplyTownMix(settingsPath, []MixAssignment{
		{Kind: MixKindCrew, Name: "alice", Agent: "pi"},
		{Kind: MixKindCrew, Name: "alice", Agent: "codex"},
	})
	if err == nil || !strings.Contains(err.Error(), `duplicate assignment for crew alice`) {
		t.Fatalf("duplicate crew error = %v", err)
	}

	err = ApplyTownMix(settingsPath, []MixAssignment{
		{Kind: MixKindDefault, Agent: "pi"},
		{Kind: MixKindDefault, Agent: "codex"},
	})
	if err == nil || !strings.Contains(err.Error(), `duplicate assignment for default agent`) {
		t.Fatalf("duplicate default error = %v", err)
	}

	err = ApplyTownMix(settingsPath, nil)
	if err == nil || !strings.Contains(err.Error(), "no mix assignments") {
		t.Fatalf("empty mix error = %v", err)
	}
}

func TestApplyTownMix_DropsIncompatiblePreservedEffort(t *testing.T) {
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

	if err := ApplyTownMix(settingsPath, []MixAssignment{
		{Kind: MixKindRole, Name: "mayor", Agent: "codex"},
	}); err != nil {
		t.Fatalf("ApplyTownMix mayor=codex: %v", err)
	}

	got, err := LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		t.Fatalf("LoadOrCreateTownSettings: %v", err)
	}
	if got.RoleAgents["mayor"] != "codex" {
		t.Fatalf("RoleAgents[mayor] = %q, want codex", got.RoleAgents["mayor"])
	}
	if _, ok := got.RoleEffort["mayor"]; ok {
		t.Fatalf("RoleEffort[mayor] = %q, want cleared Pi-only effort", got.RoleEffort["mayor"])
	}
}

func TestDescribeTownMix_ReportsMixedProviders(t *testing.T) {
	settings := NewTownSettings()
	settings.DefaultAgent = "codex"
	settings.RoleAgents = map[string]string{
		"mayor": "pi",
		"crew":  "codex",
	}
	settings.RoleEffort = map[string]string{"mayor": "high"}
	settings.CrewAgents = map[string]string{"alice": "pi"}

	got := DescribeTownMix(settings)
	if !got.Mixed {
		t.Fatal("DescribeTownMix.Mixed = false, want true for pi+codex")
	}
	if got.DefaultAgent != "codex" {
		t.Fatalf("DefaultAgent = %q, want codex", got.DefaultAgent)
	}
	if !reflect.DeepEqual(got.Providers, []string{"codex", "pi"}) {
		t.Fatalf("Providers = %v, want [codex pi]", got.Providers)
	}

	mayor := mixEntryByName(got.Roles, "mayor")
	if mayor.Agent != "pi" || mayor.Provider != "pi" || mayor.Effort != "high" || mayor.Source != "role" {
		t.Fatalf("mayor entry = %+v", mayor)
	}
	crew := mixEntryByName(got.Roles, "crew")
	if crew.Agent != "codex" || crew.Provider != "codex" || crew.Source != "role" {
		t.Fatalf("crew entry = %+v", crew)
	}
	deacon := mixEntryByName(got.Roles, "deacon")
	if deacon.Agent != "codex" || deacon.Source != "default" {
		t.Fatalf("deacon entry = %+v, want default codex", deacon)
	}
	if len(got.Crew) != 1 || got.Crew[0].Name != "alice" || got.Crew[0].Agent != "pi" || got.Crew[0].Provider != "pi" {
		t.Fatalf("crew overrides = %+v", got.Crew)
	}
}

func TestDescribeTownMix_SingleProviderIsNotMixed(t *testing.T) {
	settings := NewTownSettings()
	settings.DefaultAgent = "pi"
	got := DescribeTownMix(settings)
	if got.Mixed {
		t.Fatal("DescribeTownMix.Mixed = true, want false for a single provider")
	}
	if !reflect.DeepEqual(got.Providers, []string{"pi"}) {
		t.Fatalf("Providers = %v, want [pi]", got.Providers)
	}
}

func TestDescribeMixBinaries_ReportsPresentCommands(t *testing.T) {
	settings := NewTownSettings()
	settings.DefaultAgent = "present-agent"
	settings.Agents = map[string]*RuntimeConfig{
		"present-agent": {Provider: "pi", Command: "true"},
		"missing-agent": {Provider: "codex", Command: "gt-mix-missing-binary"},
	}
	settings.RoleAgents = map[string]string{"mayor": "missing-agent"}

	got := DescribeMixBinaries(settings)
	byAgent := map[string]MixBinary{}
	for _, binary := range got {
		byAgent[binary.Agent] = binary
	}
	if !byAgent["present-agent"].Present || byAgent["present-agent"].Command != "true" {
		t.Fatalf("present-agent = %+v, want present true", byAgent["present-agent"])
	}
	if byAgent["missing-agent"].Present || byAgent["missing-agent"].Command != "gt-mix-missing-binary" {
		t.Fatalf("missing-agent = %+v, want missing gt-mix-missing-binary", byAgent["missing-agent"])
	}
}

func mixEntryByName(entries []MixEntry, name string) MixEntry {
	for _, entry := range entries {
		if entry.Name == name {
			return entry
		}
	}
	return MixEntry{}
}
