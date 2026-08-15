package config

import (
	"os"
	"testing"
)

func TestValidCostTiers(t *testing.T) {
	t.Parallel()
	tiers := ValidCostTiers()
	if len(tiers) != 5 {
		t.Fatalf("ValidCostTiers() returned %d tiers, want 5", len(tiers))
	}
	expected := map[string]bool{"standard": true, "economy": true, "budget": true, "custom-groq-opus": true, "custom-groq-sonnet": true}
	for _, tier := range tiers {
		if !expected[tier] {
			t.Errorf("unexpected tier %q", tier)
		}
	}
}

func TestIsValidTier(t *testing.T) {
	t.Parallel()
	tests := []struct {
		tier string
		want bool
	}{
		{"standard", true},
		{"economy", true},
		{"budget", true},
		{"custom-groq-opus", true},
		{"premium", false},
		{"", false},
		{"Standard", false}, // case-sensitive
	}
	for _, tt := range tests {
		t.Run(tt.tier, func(t *testing.T) {
			t.Parallel()
			if got := IsValidTier(tt.tier); got != tt.want {
				t.Errorf("IsValidTier(%q) = %v, want %v", tt.tier, got, tt.want)
			}
		})
	}
}

func TestCostTierRoleAgents(t *testing.T) {
	t.Parallel()

	t.Run("standard maps roles to defaults, boot/dog to haiku", func(t *testing.T) {
		t.Parallel()
		ra := CostTierRoleAgents(TierStandard)
		if ra == nil {
			t.Fatal("standard tier returned nil")
		}
		if len(ra) != len(TierManagedRoles) {
			t.Errorf("standard tier has %d entries, want %d (all managed roles)", len(ra), len(TierManagedRoles))
		}
		expected := map[string]string{
			"mayor":    "",
			"deacon":   "",
			"witness":  "",
			"refinery": "",
			"polecat":  "",
			"crew":     "",
			"boot":     "claude-haiku",
			"dog":      "claude-haiku",
		}
		for role, want := range expected {
			if val, ok := ra[role]; !ok {
				t.Errorf("standard tier missing role %q", role)
			} else if val != want {
				t.Errorf("standard tier %q = %q, want %q", role, val, want)
			}
		}
	})

	t.Run("economy has correct assignments", func(t *testing.T) {
		t.Parallel()
		ra := CostTierRoleAgents(TierEconomy)
		if ra == nil {
			t.Fatal("economy tier returned nil")
		}
		expected := map[string]string{
			"mayor":    "claude-sonnet",
			"deacon":   "claude-haiku",
			"witness":  "claude-sonnet",
			"refinery": "claude-sonnet",
			"polecat":  "", // use default (opus)
			"crew":     "", // use default (opus)
			"boot":     "claude-haiku",
			"dog":      "claude-haiku",
		}
		for role, want := range expected {
			if got := ra[role]; got != want {
				t.Errorf("economy[%q] = %q, want %q", role, got, want)
			}
		}
	})

	t.Run("budget has correct assignments", func(t *testing.T) {
		t.Parallel()
		ra := CostTierRoleAgents(TierBudget)
		if ra == nil {
			t.Fatal("budget tier returned nil")
		}
		expected := map[string]string{
			"mayor":    "claude-sonnet",
			"deacon":   "claude-haiku",
			"witness":  "claude-haiku",
			"refinery": "claude-haiku",
			"polecat":  "claude-sonnet",
			"crew":     "claude-sonnet",
			"boot":     "claude-haiku",
			"dog":      "claude-haiku",
		}
		for role, want := range expected {
			if got := ra[role]; got != want {
				t.Errorf("budget[%q] = %q, want %q", role, got, want)
			}
		}
	})

	t.Run("custom-groq-opus has correct assignments", func(t *testing.T) {
		t.Parallel()
		ra := CostTierRoleAgents(TierCustomGroqOpus)
		if ra == nil {
			t.Fatal("custom-groq-opus tier returned nil")
		}
		expected := map[string]string{
			"mayor":    "",
			"deacon":   "groq-compound",
			"witness":  "groq-compound",
			"refinery": "groq-compound",
			"polecat":  "groq-compound",
			"crew":     "",
			"boot":     "groq-compound",
			"dog":      "groq-compound",
		}
		for role, want := range expected {
			if got := ra[role]; got != want {
				t.Errorf("custom-groq-opus[%q] = %q, want %q", role, got, want)
			}
		}
	})

	t.Run("invalid tier returns nil", func(t *testing.T) {
		t.Parallel()
		ra := CostTierRoleAgents("invalid")
		if ra != nil {
			t.Error("invalid tier should return nil")
		}
	})
}

func TestCostTierAgents(t *testing.T) {
	t.Parallel()

	t.Run("standard returns empty map", func(t *testing.T) {
		t.Parallel()
		agents := CostTierAgents(TierStandard)
		if agents == nil {
			t.Fatal("standard tier returned nil, want empty map")
		}
		if len(agents) != 0 {
			t.Errorf("standard tier has %d agents, want 0", len(agents))
		}
	})

	t.Run("economy returns sonnet and haiku presets", func(t *testing.T) {
		t.Parallel()
		agents := CostTierAgents(TierEconomy)
		if agents == nil {
			t.Fatal("economy tier returned nil")
		}
		if _, ok := agents["claude-sonnet"]; !ok {
			t.Error("economy tier missing claude-sonnet agent")
		}
		if _, ok := agents["claude-haiku"]; !ok {
			t.Error("economy tier missing claude-haiku agent")
		}
	})

	t.Run("budget returns sonnet and haiku presets", func(t *testing.T) {
		t.Parallel()
		agents := CostTierAgents(TierBudget)
		if agents == nil {
			t.Fatal("budget tier returned nil")
		}
		if _, ok := agents["claude-sonnet"]; !ok {
			t.Error("budget tier missing claude-sonnet agent")
		}
		if _, ok := agents["claude-haiku"]; !ok {
			t.Error("budget tier missing claude-haiku agent")
		}
	})

	t.Run("sonnet preset has correct args", func(t *testing.T) {
		t.Parallel()
		agents := CostTierAgents(TierEconomy)
		sonnet := agents["claude-sonnet"]
		if sonnet.Command != "claude" {
			t.Errorf("sonnet Command = %q, want %q", sonnet.Command, "claude")
		}
		found := false
		for i, arg := range sonnet.Args {
			if arg == "--model" && i+1 < len(sonnet.Args) && sonnet.Args[i+1] == "sonnet[1m]" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("sonnet Args %v missing --model sonnet[1m]", sonnet.Args)
		}
	})

	t.Run("haiku preset has correct args", func(t *testing.T) {
		t.Parallel()
		agents := CostTierAgents(TierEconomy)
		haiku := agents["claude-haiku"]
		if haiku.Command != "claude" {
			t.Errorf("haiku Command = %q, want %q", haiku.Command, "claude")
		}
		found := false
		for i, arg := range haiku.Args {
			if arg == "--model" && i+1 < len(haiku.Args) && haiku.Args[i+1] == "haiku" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("haiku Args %v missing --model haiku", haiku.Args)
		}
	})

	t.Run("custom-groq-opus returns groq-compound preset", func(t *testing.T) {
		t.Parallel()
		agents := CostTierAgents(TierCustomGroqOpus)
		if agents == nil {
			t.Fatal("custom-groq-opus tier returned nil")
		}
		groq, ok := agents["groq-compound"]
		if !ok {
			t.Fatal("custom-groq-opus tier missing groq-compound agent")
		}
		if groq.Command != "claude" {
			t.Errorf("groq-compound Command = %q, want %q", groq.Command, "claude")
		}
		if groq.Env == nil {
			t.Fatal("groq-compound Env is nil, want Groq API env vars")
		}
		if groq.Env["ANTHROPIC_BASE_URL"] != "https://api.groq.com/openai/v1" {
			t.Errorf("groq-compound ANTHROPIC_BASE_URL = %q, want Groq API URL", groq.Env["ANTHROPIC_BASE_URL"])
		}
		if groq.Env["ANTHROPIC_MODEL"] != "compound-beta" {
			t.Errorf("groq-compound ANTHROPIC_MODEL = %q, want compound-beta", groq.Env["ANTHROPIC_MODEL"])
		}
		// Verify the preset reads GROQ_API_KEY from the environment (not a hardcoded value)
		if groq.Env["ANTHROPIC_API_KEY"] != os.Getenv("GROQ_API_KEY") {
			t.Errorf("groq-compound ANTHROPIC_API_KEY = %q, want value of GROQ_API_KEY env var", groq.Env["ANTHROPIC_API_KEY"])
		}
	})
}

func TestApplyCostTier(t *testing.T) {
	t.Parallel()

	t.Run("applies economy tier", func(t *testing.T) {
		t.Parallel()
		settings := NewTownSettings()
		if err := ApplyCostTier(settings, TierEconomy); err != nil {
			t.Fatalf("ApplyCostTier: %v", err)
		}
		if settings.CostTier != "economy" {
			t.Errorf("CostTier = %q, want %q", settings.CostTier, "economy")
		}
		if settings.RoleAgents["mayor"] != "claude-sonnet" {
			t.Errorf("RoleAgents[mayor] = %q, want %q", settings.RoleAgents["mayor"], "claude-sonnet")
		}
		if settings.Agents["claude-sonnet"] == nil {
			t.Error("Agents[claude-sonnet] is nil")
		}
		if settings.Agents["claude-haiku"] == nil {
			t.Error("Agents[claude-haiku] is nil")
		}
	})

	t.Run("standard tier clears tier-managed roles and preset agents", func(t *testing.T) {
		t.Parallel()
		settings := NewTownSettings()
		// First apply economy
		if err := ApplyCostTier(settings, TierEconomy); err != nil {
			t.Fatalf("ApplyCostTier economy: %v", err)
		}
		// Then switch to standard
		if err := ApplyCostTier(settings, TierStandard); err != nil {
			t.Fatalf("ApplyCostTier standard: %v", err)
		}
		if settings.CostTier != "standard" {
			t.Errorf("CostTier = %q, want %q", settings.CostTier, "standard")
		}
		// Tier-managed roles with empty standard value should be removed
		for _, role := range []string{"mayor", "deacon", "witness", "refinery", "polecat", "crew"} {
			if val, ok := settings.RoleAgents[role]; ok {
				t.Errorf("RoleAgents[%q] = %q, want deleted (standard tier)", role, val)
			}
		}
		// boot and dog should be set to claude-haiku even on standard tier
		for _, role := range []string{"boot", "dog"} {
			if val := settings.RoleAgents[role]; val != "claude-haiku" {
				t.Errorf("RoleAgents[%q] = %q, want %q (standard tier)", role, val, "claude-haiku")
			}
		}
		if _, ok := settings.Agents["claude-sonnet"]; ok {
			t.Error("standard tier should remove claude-sonnet agent")
		}
		if _, ok := settings.Agents["claude-haiku"]; ok {
			t.Error("standard tier should remove claude-haiku agent")
		}
	})

	t.Run("standard preserves non-tier agents", func(t *testing.T) {
		t.Parallel()
		settings := NewTownSettings()
		settings.Agents = map[string]*RuntimeConfig{
			"custom-agent":  {Command: "custom"},
			"claude-sonnet": claudeSonnetPreset(),
		}
		if err := ApplyCostTier(settings, TierStandard); err != nil {
			t.Fatalf("ApplyCostTier: %v", err)
		}
		if settings.Agents["custom-agent"] == nil {
			t.Error("standard tier should preserve custom-agent")
		}
		if _, ok := settings.Agents["claude-sonnet"]; ok {
			t.Error("standard tier should remove claude-sonnet")
		}
	})

	t.Run("invalid tier returns error", func(t *testing.T) {
		t.Parallel()
		settings := NewTownSettings()
		err := ApplyCostTier(settings, "invalid")
		if err == nil {
			t.Error("expected error for invalid tier")
		}
	})
}

func TestGetCurrentTier(t *testing.T) {
	t.Parallel()

	t.Run("detects standard tier", func(t *testing.T) {
		t.Parallel()
		settings := NewTownSettings()
		settings.CostTier = "standard"
		settings.RoleAgents = map[string]string{
			"boot": "claude-haiku",
			"dog":  "claude-haiku",
		}
		if got := GetCurrentTier(settings); got != "standard" {
			t.Errorf("GetCurrentTier = %q, want %q", got, "standard")
		}
	})

	t.Run("detects economy tier", func(t *testing.T) {
		t.Parallel()
		settings := NewTownSettings()
		if err := ApplyCostTier(settings, TierEconomy); err != nil {
			t.Fatalf("ApplyCostTier: %v", err)
		}
		if got := GetCurrentTier(settings); got != "economy" {
			t.Errorf("GetCurrentTier = %q, want %q", got, "economy")
		}
	})

	t.Run("detects budget tier", func(t *testing.T) {
		t.Parallel()
		settings := NewTownSettings()
		if err := ApplyCostTier(settings, TierBudget); err != nil {
			t.Fatalf("ApplyCostTier: %v", err)
		}
		if got := GetCurrentTier(settings); got != "budget" {
			t.Errorf("GetCurrentTier = %q, want %q", got, "budget")
		}
	})

	t.Run("returns empty for custom config", func(t *testing.T) {
		t.Parallel()
		settings := NewTownSettings()
		settings.RoleAgents = map[string]string{
			"mayor": "some-custom-agent",
		}
		if got := GetCurrentTier(settings); got != "" {
			t.Errorf("GetCurrentTier = %q, want empty string for custom config", got)
		}
	})

	t.Run("detects stale CostTier field", func(t *testing.T) {
		t.Parallel()
		settings := NewTownSettings()
		settings.CostTier = "economy" // says economy
		settings.RoleAgents = map[string]string{
			"mayor": "some-custom-agent", // but actually custom
		}
		// Should detect mismatch and infer from RoleAgents
		if got := GetCurrentTier(settings); got != "" {
			t.Errorf("GetCurrentTier = %q, want empty string for stale CostTier", got)
		}
	})

	t.Run("infers tier without CostTier field", func(t *testing.T) {
		t.Parallel()
		settings := NewTownSettings()
		// Set RoleAgents matching economy tier but without CostTier field
		settings.RoleAgents = map[string]string{
			"mayor":    "claude-sonnet",
			"deacon":   "claude-haiku",
			"witness":  "claude-sonnet",
			"refinery": "claude-sonnet",
			"boot":     "claude-haiku",
			"dog":      "claude-haiku",
		}
		if got := GetCurrentTier(settings); got != "economy" {
			t.Errorf("GetCurrentTier = %q, want %q (inferred)", got, "economy")
		}
	})
}

func TestTierRolesMatch(t *testing.T) {
	t.Parallel()

	t.Run("empty actual does not match standard tier (boot/dog need haiku)", func(t *testing.T) {
		t.Parallel()
		actual := map[string]string{}
		expected := CostTierRoleAgents(TierStandard)
		if tierRolesMatch(actual, expected) {
			t.Error("empty map should not match standard tier (boot/dog require claude-haiku)")
		}
	})

	t.Run("nil actual does not match standard tier", func(t *testing.T) {
		t.Parallel()
		expected := CostTierRoleAgents(TierStandard)
		if tierRolesMatch(nil, expected) {
			t.Error("nil map should not match standard tier (boot/dog require claude-haiku)")
		}
	})

	t.Run("standard tier actual matches standard tier", func(t *testing.T) {
		t.Parallel()
		actual := map[string]string{
			"boot": "claude-haiku",
			"dog":  "claude-haiku",
		}
		expected := CostTierRoleAgents(TierStandard)
		if !tierRolesMatch(actual, expected) {
			t.Error("standard tier assignments should match")
		}
	})

	t.Run("economy tier matches", func(t *testing.T) {
		t.Parallel()
		actual := map[string]string{
			"mayor":    "claude-sonnet",
			"deacon":   "claude-haiku",
			"witness":  "claude-sonnet",
			"refinery": "claude-sonnet",
			"boot":     "claude-haiku",
			"dog":      "claude-haiku",
		}
		expected := CostTierRoleAgents(TierEconomy)
		if !tierRolesMatch(actual, expected) {
			t.Error("economy tier assignments should match")
		}
	})

	t.Run("non-tier custom entries are ignored", func(t *testing.T) {
		t.Parallel()
		// Actual has standard tier assignments plus a custom non-tier entry
		actual := map[string]string{
			"boot":        "claude-haiku",
			"dog":         "claude-haiku",
			"custom-role": "custom-agent",
		}
		expected := CostTierRoleAgents(TierStandard)
		if !tierRolesMatch(actual, expected) {
			t.Error("non-tier custom entries should be ignored in comparison")
		}
	})

	t.Run("different tier-managed values don't match", func(t *testing.T) {
		t.Parallel()
		actual := map[string]string{"mayor": "claude-haiku"}
		expected := CostTierRoleAgents(TierEconomy) // mayor = claude-sonnet
		if tierRolesMatch(actual, expected) {
			t.Error("different tier-managed values should not match")
		}
	})
}

func TestApplyCostTier_PreservesCustomRoleAgents(t *testing.T) {
	t.Parallel()

	t.Run("standard tier preserves non-tier custom role entries", func(t *testing.T) {
		t.Parallel()
		settings := NewTownSettings()
		// Simulate a user who set a custom non-tier role
		settings.RoleAgents["custom-role"] = "custom-agent"
		// Also set a tier-managed role that economy would have set
		settings.RoleAgents["mayor"] = "claude-sonnet"

		if err := ApplyCostTier(settings, TierStandard); err != nil {
			t.Fatalf("ApplyCostTier: %v", err)
		}

		// Custom entry must survive
		if settings.RoleAgents["custom-role"] != "custom-agent" {
			t.Error("standard tier should preserve non-tier RoleAgents entry 'custom-role'")
		}
		// Tier-managed role should be cleared
		if _, ok := settings.RoleAgents["mayor"]; ok {
			t.Error("standard tier should remove tier-managed role 'mayor'")
		}
	})

	t.Run("economy tier preserves non-tier custom role entries", func(t *testing.T) {
		t.Parallel()
		settings := NewTownSettings()
		settings.RoleAgents["custom-role"] = "custom-agent"

		if err := ApplyCostTier(settings, TierEconomy); err != nil {
			t.Fatalf("ApplyCostTier: %v", err)
		}

		if settings.RoleAgents["custom-role"] != "custom-agent" {
			t.Error("economy tier should preserve non-tier RoleAgents entry 'custom-role'")
		}
		if settings.RoleAgents["mayor"] != "claude-sonnet" {
			t.Errorf("economy tier mayor = %q, want claude-sonnet", settings.RoleAgents["mayor"])
		}
	})
}

func TestTierDescription(t *testing.T) {
	t.Parallel()
	for _, tier := range ValidCostTiers() {
		t.Run(tier, func(t *testing.T) {
			t.Parallel()
			desc := TierDescription(CostTier(tier))
			if desc == "" || desc == "Unknown tier" {
				t.Errorf("TierDescription(%q) = %q, want meaningful description", tier, desc)
			}
		})
	}
}

func TestFormatTierRoleTable(t *testing.T) {
	t.Parallel()

	t.Run("valid tier returns formatted output", func(t *testing.T) {
		t.Parallel()
		output := FormatTierRoleTable(TierEconomy)
		if output == "" {
			t.Error("FormatTierRoleTable returned empty for economy tier")
		}
		// Should contain all roles
		for _, role := range []string{"mayor", "deacon", "witness", "refinery", "polecat", "crew", "boot", "dog"} {
			if !contains(output, role) {
				t.Errorf("output missing role %q", role)
			}
		}
	})

	t.Run("invalid tier returns empty", func(t *testing.T) {
		t.Parallel()
		output := FormatTierRoleTable("invalid")
		if output != "" {
			t.Errorf("FormatTierRoleTable(invalid) = %q, want empty", output)
		}
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestCostTierRoleEffort(t *testing.T) {
	t.Parallel()

	t.Run("standard tier all high", func(t *testing.T) {
		t.Parallel()
		re := CostTierRoleEffort(TierStandard)
		if re == nil {
			t.Fatal("CostTierRoleEffort(standard) returned nil")
		}
		for _, role := range TierManagedRoles {
			if re[role] != "high" {
				t.Errorf("standard tier role_effort[%s] = %q, want %q", role, re[role], "high")
			}
		}
	})

	t.Run("economy tier workers high, patrol low/medium", func(t *testing.T) {
		t.Parallel()
		re := CostTierRoleEffort(TierEconomy)
		if re == nil {
			t.Fatal("CostTierRoleEffort(economy) returned nil")
		}
		// Workers should be high
		for _, role := range []string{"polecat", "crew"} {
			if re[role] != "high" {
				t.Errorf("economy tier role_effort[%s] = %q, want %q", role, re[role], "high")
			}
		}
		// Patrol roles should be low
		for _, role := range []string{"deacon", "witness", "boot", "dog"} {
			if re[role] != "low" {
				t.Errorf("economy tier role_effort[%s] = %q, want %q", role, re[role], "low")
			}
		}
		// Mayor and refinery should be medium
		for _, role := range []string{"mayor", "refinery"} {
			if re[role] != "medium" {
				t.Errorf("economy tier role_effort[%s] = %q, want %q", role, re[role], "medium")
			}
		}
	})

	t.Run("budget tier workers medium, patrol low", func(t *testing.T) {
		t.Parallel()
		re := CostTierRoleEffort(TierBudget)
		if re == nil {
			t.Fatal("CostTierRoleEffort(budget) returned nil")
		}
		for _, role := range []string{"polecat", "crew"} {
			if re[role] != "medium" {
				t.Errorf("budget tier role_effort[%s] = %q, want %q", role, re[role], "medium")
			}
		}
		for _, role := range []string{"mayor", "deacon", "witness", "refinery", "boot", "dog"} {
			if re[role] != "low" {
				t.Errorf("budget tier role_effort[%s] = %q, want %q", role, re[role], "low")
			}
		}
	})

	t.Run("invalid tier returns nil", func(t *testing.T) {
		t.Parallel()
		if re := CostTierRoleEffort("invalid"); re != nil {
			t.Errorf("CostTierRoleEffort(invalid) = %v, want nil", re)
		}
	})
}

func TestIsValidEffortLevel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		level string
		want  bool
	}{
		{"off", true},
		{"minimal", true},
		{"low", true},
		{"medium", true},
		{"high", true},
		{"xhigh", true},
		{"max", true},
		{"", false},
		{"extreme", false},
		{"High", false}, // case-sensitive
	}
	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			t.Parallel()
			if got := IsValidEffortLevel(tt.level); got != tt.want {
				t.Errorf("IsValidEffortLevel(%q) = %v, want %v", tt.level, got, tt.want)
			}
		})
	}
}

func TestValidEffortLevelsForRuntime(t *testing.T) {
	t.Parallel()

	pi := &RuntimeConfig{Provider: "pi", Command: "pi"}
	for _, effort := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
		if !isValidEffortLevelForRuntime(pi, effort) {
			t.Errorf("Pi effort %q rejected", effort)
		}
	}

	claude := &RuntimeConfig{Provider: "claude", Command: "claude"}
	for _, effort := range []string{"low", "medium", "high", "max"} {
		if !isValidEffortLevelForRuntime(claude, effort) {
			t.Errorf("Claude effort %q rejected", effort)
		}
	}
	for _, effort := range []string{"off", "minimal", "xhigh"} {
		if isValidEffortLevelForRuntime(claude, effort) {
			t.Errorf("Claude effort %q accepted", effort)
		}
	}
}

func TestApplyCostTier_SetsRoleEffort(t *testing.T) {
	t.Parallel()

	t.Run("economy tier sets non-high effort levels", func(t *testing.T) {
		t.Parallel()
		settings := NewTownSettings()
		if err := ApplyCostTier(settings, TierEconomy); err != nil {
			t.Fatalf("ApplyCostTier: %v", err)
		}
		// Workers have high effort — should NOT be in the map (high is default)
		for _, role := range []string{"polecat", "crew"} {
			if _, ok := settings.RoleEffort[role]; ok {
				t.Errorf("RoleEffort[%s] should not be set (high is default)", role)
			}
		}
		// Patrol roles should have low
		for _, role := range []string{"deacon", "witness", "boot", "dog"} {
			if settings.RoleEffort[role] != "low" {
				t.Errorf("RoleEffort[%s] = %q, want %q", role, settings.RoleEffort[role], "low")
			}
		}
		// Mayor and refinery should have medium
		for _, role := range []string{"mayor", "refinery"} {
			if settings.RoleEffort[role] != "medium" {
				t.Errorf("RoleEffort[%s] = %q, want %q", role, settings.RoleEffort[role], "medium")
			}
		}
	})

	t.Run("standard tier clears role effort entries", func(t *testing.T) {
		t.Parallel()
		settings := NewTownSettings()
		// Apply economy first
		if err := ApplyCostTier(settings, TierEconomy); err != nil {
			t.Fatalf("ApplyCostTier economy: %v", err)
		}
		// Then switch to standard
		if err := ApplyCostTier(settings, TierStandard); err != nil {
			t.Fatalf("ApplyCostTier standard: %v", err)
		}
		// All roles should be cleared (high is default, not persisted)
		for _, role := range TierManagedRoles {
			if val, ok := settings.RoleEffort[role]; ok {
				t.Errorf("RoleEffort[%s] = %q, want deleted (standard tier)", role, val)
			}
		}
	})
}

func TestFormatTierRoleTable_IncludesEffort(t *testing.T) {
	t.Parallel()
	table := FormatTierRoleTable(TierEconomy)
	if table == "" {
		t.Fatal("FormatTierRoleTable(economy) returned empty string")
	}
	// Workers should show "effort: high"
	if !containsSubstring(table, "effort: high") {
		t.Error("economy tier table should contain 'effort: high' for workers")
	}
	// Should contain effort: low for patrol roles
	if !containsSubstring(table, "effort: low") {
		t.Error("economy tier table should contain 'effort: low' for patrol roles")
	}
	// Should contain effort: medium for mayor/refinery
	if !containsSubstring(table, "effort: medium") {
		t.Error("economy tier table should contain 'effort: medium' for mayor/refinery")
	}
}
