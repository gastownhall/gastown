package witness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
)

func TestEscalationTierAdvancement(t *testing.T) {
	tmpDir := t.TempDir()
	witnessDir := filepath.Join(tmpDir, "witness")
	if err := os.MkdirAll(witnessDir, 0755); err != nil {
		t.Fatal(err)
	}

	beadID := "test-abc123"

	// Initially at tier 0.
	tier := GetEscalationTier(tmpDir, beadID)
	if tier != 0 {
		t.Errorf("initial tier = %d, want 0", tier)
	}

	// Record some respawns at tier 0.
	RecordBeadRespawn(tmpDir, beadID)
	RecordBeadRespawn(tmpDir, beadID)
	RecordBeadRespawn(tmpDir, beadID)

	// Verify count is 3.
	state := loadBeadRespawnState(tmpDir)
	if state.Beads[beadID].Count != 3 {
		t.Errorf("count after 3 respawns = %d, want 3", state.Beads[beadID].Count)
	}

	// Advance to tier 1 — count should reset to 0.
	newTier := AdvanceEscalationTier(tmpDir, beadID)
	if newTier != 1 {
		t.Errorf("after first advance, tier = %d, want 1", newTier)
	}

	state = loadBeadRespawnState(tmpDir)
	if state.Beads[beadID].Count != 0 {
		t.Errorf("count after tier advance = %d, want 0", state.Beads[beadID].Count)
	}
	if state.Beads[beadID].EscalationTier != 1 {
		t.Errorf("escalation_tier after advance = %d, want 1", state.Beads[beadID].EscalationTier)
	}

	// ShouldBlockRespawn should now be false (count is 0).
	// We need config for this — write a minimal settings file.
	settingsDir := filepath.Join(tmpDir, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatal(err)
	}
	maxRespawns := 3
	settings := map[string]interface{}{
		"type":    "town-settings",
		"version": 1,
		"operational": map[string]interface{}{
			"witness": map[string]interface{}{
				"max_bead_respawns": maxRespawns,
			},
		},
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	if ShouldBlockRespawn(tmpDir, beadID) {
		t.Error("ShouldBlockRespawn = true after tier advance (count=0), want false")
	}

	// Record respawns at tier 1 up to threshold.
	for i := 0; i < maxRespawns; i++ {
		RecordBeadRespawn(tmpDir, beadID)
	}

	if !ShouldBlockRespawn(tmpDir, beadID) {
		t.Error("ShouldBlockRespawn = false after exhausting tier 1, want true")
	}

	// Advance to tier 2.
	newTier = AdvanceEscalationTier(tmpDir, beadID)
	if newTier != 2 {
		t.Errorf("after second advance, tier = %d, want 2", newTier)
	}

	if ShouldBlockRespawn(tmpDir, beadID) {
		t.Error("ShouldBlockRespawn = true after second tier advance, want false")
	}
}

func TestSetLastAgent(t *testing.T) {
	tmpDir := t.TempDir()
	witnessDir := filepath.Join(tmpDir, "witness")
	if err := os.MkdirAll(witnessDir, 0755); err != nil {
		t.Fatal(err)
	}

	beadID := "test-xyz789"

	SetLastAgent(tmpDir, beadID, "opus")

	state := loadBeadRespawnState(tmpDir)
	rec := state.Beads[beadID]
	if rec == nil {
		t.Fatal("expected record to exist after SetLastAgent")
	}
	if rec.LastAgent != "opus" {
		t.Errorf("LastAgent = %q, want %q", rec.LastAgent, "opus")
	}
}

func TestResolveUpgradeAgent(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name           string
		rigAgent       string // agent in rig settings
		polecatAgent   string // role_agents.polecat in rig settings
		upgradeAgent   string // policy.UpgradeAgent
		wantAgent      string
	}{
		{
			name:      "no rig agent, default upgrade to opus",
			wantAgent: "opus",
		},
		{
			name:         "rig uses sonnet, upgrade to opus",
			rigAgent:     "sonnet",
			wantAgent:    "opus",
		},
		{
			name:      "rig already on opus, no upgrade",
			rigAgent:  "opus",
			wantAgent: "",
		},
		{
			name:         "polecat role uses sonnet, upgrade to opus",
			polecatAgent: "claude-sonnet",
			wantAgent:    "opus",
		},
		{
			name:         "polecat role already opus, no upgrade",
			polecatAgent: "opus",
			wantAgent:    "",
		},
		{
			name:         "custom upgrade target",
			rigAgent:     "haiku",
			upgradeAgent: "sonnet",
			wantAgent:    "sonnet",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rigName := "testrig"
			rigDir := filepath.Join(tmpDir, rigName, "settings")
			if err := os.MkdirAll(rigDir, 0755); err != nil {
				t.Fatal(err)
			}

			settings := map[string]interface{}{
				"type":    "rig-settings",
				"version": 1,
			}
			if tt.rigAgent != "" {
				settings["agent"] = tt.rigAgent
			}
			if tt.polecatAgent != "" {
				settings["role_agents"] = map[string]string{"polecat": tt.polecatAgent}
			}
			data, _ := json.Marshal(settings)
			if err := os.WriteFile(filepath.Join(rigDir, "config.json"), data, 0600); err != nil {
				t.Fatal(err)
			}

			policy := &config.EscalationPolicy{
				Enabled:      true,
				UpgradeAgent: tt.upgradeAgent,
			}
			got := resolveUpgradeAgent(tmpDir, rigName, policy)
			if got != tt.wantAgent {
				t.Errorf("resolveUpgradeAgent() = %q, want %q", got, tt.wantAgent)
			}
		})
	}
}

func TestRigHasCrew(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"

	// No crew dir.
	if rigHasCrew(tmpDir, rigName) {
		t.Error("rigHasCrew = true with no crew dir, want false")
	}

	// Empty crew dir.
	crewDir := filepath.Join(tmpDir, rigName, "crew")
	if err := os.MkdirAll(crewDir, 0755); err != nil {
		t.Fatal(err)
	}
	if rigHasCrew(tmpDir, rigName) {
		t.Error("rigHasCrew = true with empty crew dir, want false")
	}

	// Hidden dirs only.
	if err := os.MkdirAll(filepath.Join(crewDir, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	if rigHasCrew(tmpDir, rigName) {
		t.Error("rigHasCrew = true with only hidden dirs, want false")
	}

	// Real crew member.
	if err := os.MkdirAll(filepath.Join(crewDir, "denali"), 0755); err != nil {
		t.Fatal(err)
	}
	if !rigHasCrew(tmpDir, rigName) {
		t.Error("rigHasCrew = false with crew member 'denali', want true")
	}
}

func TestEscalationPolicyAccessors(t *testing.T) {
	// Nil policy.
	var wt *config.WitnessThresholds
	if wt.GetEscalationPolicy() != nil {
		t.Error("nil WitnessThresholds.GetEscalationPolicy() should return nil")
	}

	// Empty policy.
	wt = &config.WitnessThresholds{}
	if wt.GetEscalationPolicy() != nil {
		t.Error("empty WitnessThresholds.GetEscalationPolicy() should return nil")
	}

	// With policy.
	policy := &config.EscalationPolicy{Enabled: true}
	wt = &config.WitnessThresholds{Escalation: policy}
	got := wt.GetEscalationPolicy()
	if got == nil || !got.Enabled {
		t.Error("expected non-nil enabled policy")
	}

	// EffectiveTiers defaults.
	tiers := policy.EffectiveTiers()
	if len(tiers) != 2 || tiers[0] != "upgrade-model" || tiers[1] != "route-crew" {
		t.Errorf("EffectiveTiers() = %v, want [upgrade-model, route-crew]", tiers)
	}

	// Custom tiers.
	policy.Tiers = []string{"route-crew"}
	tiers = policy.EffectiveTiers()
	if len(tiers) != 1 || tiers[0] != "route-crew" {
		t.Errorf("EffectiveTiers() with custom = %v, want [route-crew]", tiers)
	}

	// AttemptsPerTierV fallback.
	if v := policy.AttemptsPerTierV(5); v != 5 {
		t.Errorf("AttemptsPerTierV(5) = %d, want 5", v)
	}
	n := 2
	policy.AttemptsPerTier = &n
	if v := policy.AttemptsPerTierV(5); v != 2 {
		t.Errorf("AttemptsPerTierV(5) with configured=2 = %d, want 2", v)
	}
}
