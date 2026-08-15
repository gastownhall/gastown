package config

import (
	"fmt"
	"os"
	"strings"
)

// CostTier represents a predefined cost optimization tier for model selection.
type CostTier string

const (
	// TierStandard uses opus for all roles (default, highest quality).
	TierStandard CostTier = "standard"
	// TierEconomy uses sonnet/haiku for patrol roles, keeps opus for workers.
	TierEconomy CostTier = "economy"
	// TierBudget uses haiku/sonnet for patrols, sonnet for workers.
	TierBudget CostTier = "budget"
	// TierCustomGroqOpus routes patrol/utility roles to Groq Compound (fast +
	// cheap) while keeping Opus for mayor and crew (quality-critical work).
	// The groq-compound preset uses the claude CLI as an SDK proxy —
	// see AgentGroqCompound in agents.go for the full wiring.
	TierCustomGroqOpus CostTier = "custom-groq-opus"
	// TierCustomGroqSonnet routes patrol/utility roles to Groq Compound (fast +
	// cheap) while using Sonnet for mayor (quality-critical work).
	// The groq-compound preset uses the claude CLI as an SDK proxy —
	// see AgentGroqCompound in agents.go for the full wiring.
	TierCustomGroqSonnet CostTier = "custom-groq-sonnet"
)

// ValidCostTiers returns all valid tier names.
func ValidCostTiers() []string {
	return []string{
		string(TierStandard),
		string(TierEconomy),
		string(TierBudget),
		string(TierCustomGroqOpus),
		string(TierCustomGroqSonnet),
	}
}

// IsValidTier checks if a string is a valid cost tier name.
func IsValidTier(tier string) bool {
	switch CostTier(tier) {
	case TierStandard, TierEconomy, TierBudget, TierCustomGroqOpus, TierCustomGroqSonnet:
		return true
	default:
		return false
	}
}

// TierManagedRoles is the set of roles whose model selection is managed by cost tiers.
// These are the only roles that ApplyCostTier modifies — any other custom RoleAgents
// entries (e.g., user-defined roles or non-Claude agents for non-tier roles) are preserved.
//
// "boot" and "dog" are utility roles that should always use the cheapest model.
var TierManagedRoles = []string{"mayor", "deacon", "witness", "refinery", "polecat", "crew", "boot", "dog"}

// CostTierRoleAgents returns the role_agents mapping for a given tier.
// All tiers explicitly map every tier-managed role. Standard tier maps roles
// to empty string when they should use the default/opus model.
func CostTierRoleAgents(tier CostTier) map[string]string {
	switch tier {
	case TierStandard:
		return map[string]string{
			"mayor":    "",
			"deacon":   "",
			"witness":  "",
			"refinery": "",
			"polecat":  "",
			"crew":     "",
			"boot":     "claude-haiku",
			"dog":      "claude-haiku",
		}

	case TierEconomy:
		return map[string]string{
			"mayor":    "claude-sonnet",
			"deacon":   "claude-haiku",
			"witness":  "claude-sonnet",
			"refinery": "claude-sonnet",
			"polecat":  "",
			"crew":     "",
			"boot":     "claude-haiku",
			"dog":      "claude-haiku",
		}

	case TierBudget:
		return map[string]string{
			"mayor":    "claude-sonnet",
			"deacon":   "claude-haiku",
			"witness":  "claude-haiku",
			"refinery": "claude-haiku",
			"polecat":  "claude-sonnet",
			"crew":     "claude-sonnet",
			"boot":     "claude-haiku",
			"dog":      "claude-haiku",
		}

	case TierCustomGroqOpus:
		// Mayor and crew keep the default (opus) for highest-quality work.
		// All patrol and utility roles (deacon, witness, refinery, polecat, boot, dog) use
		// Groq Compound for fast, low-cost background orchestration.
		return map[string]string{
			"mayor":    "", // use default (opus)
			"deacon":   "groq-compound",
			"witness":  "groq-compound",
			"refinery": "groq-compound",
			"polecat":  "groq-compound",
			"crew":     "", // use default (opus)
			"boot":     "groq-compound",
			"dog":      "groq-compound",
		}

	case TierCustomGroqSonnet:
		// Mayor uses Sonnet for quality-critical work.
		// All other roles (crew, deacon, witness, refinery, polecat, boot, dog) use
		// Groq Compound for fast, low-cost background orchestration.
		return map[string]string{
			"mayor":    "claude-sonnet",
			"deacon":   "groq-compound",
			"witness":  "groq-compound",
			"refinery": "groq-compound",
			"polecat":  "groq-compound",
			"crew":     "groq-compound",
			"boot":     "groq-compound",
			"dog":      "groq-compound",
		}

	default:
		return nil
	}
}

// CostTierRoleEffort returns the role_effort mapping for a given tier.
// Workers get the highest effort for the tier; patrol roles drop effort since
// they do simpler, more repetitive work. Returns nil if the tier is invalid.
func CostTierRoleEffort(tier CostTier) map[string]string {
	switch tier {
	case TierStandard:
		return map[string]string{
			"mayor":    "high",
			"deacon":   "high",
			"witness":  "high",
			"refinery": "high",
			"polecat":  "high",
			"crew":     "high",
			"boot":     "high",
			"dog":      "high",
		}
	case TierEconomy:
		return map[string]string{
			"mayor":    "medium",
			"deacon":   "low",
			"witness":  "low",
			"refinery": "medium",
			"polecat":  "high",
			"crew":     "high",
			"boot":     "low",
			"dog":      "low",
		}
	case TierBudget:
		return map[string]string{
			"mayor":    "low",
			"deacon":   "low",
			"witness":  "low",
			"refinery": "low",
			"polecat":  "medium",
			"crew":     "medium",
			"boot":     "low",
			"dog":      "low",
		}
	default:
		return nil
	}
}

// ValidEffortLevels returns every effort level supported by a managed runtime.
func ValidEffortLevels() []string {
	return []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}
}

// IsValidEffortLevel checks if a string is a valid effort level.
func IsValidEffortLevel(level string) bool {
	switch level {
	case "off", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func validEffortLevelsForRuntime(rc *RuntimeConfig) []string {
	if isPiAgent(rc) {
		return ValidEffortLevels()
	}
	return []string{"low", "medium", "high", "max"}
}

func isValidEffortLevelForRuntime(rc *RuntimeConfig, level string) bool {
	for _, candidate := range validEffortLevelsForRuntime(rc) {
		if level == candidate {
			return true
		}
	}
	return false
}

// CostTierAgents returns the custom agent definitions needed for a given tier.
// These define the claude-sonnet, claude-haiku, and groq-compound agent presets
// and are written into TownSettings.Agents so Gas Town can resolve them by name.
// Standard tier returns an empty map (no custom agents needed).
func CostTierAgents(tier CostTier) map[string]*RuntimeConfig {
	switch tier {
	case TierStandard:
		return map[string]*RuntimeConfig{}
	case TierEconomy, TierBudget:
		return map[string]*RuntimeConfig{
			"claude-sonnet": claudeSonnetPreset(),
			"claude-haiku":  claudeHaikuPreset(),
		}
	case TierCustomGroqOpus:
		return map[string]*RuntimeConfig{
			// groq-compound is a first-class builtin (AgentGroqCompound) so we
			// derive the RuntimeConfig directly from the registry. This ensures
			// the correct ANTHROPIC_BASE_URL / ANTHROPIC_API_KEY env vars, the
			// right model flag, and all Claude-SDK plumbing are always in sync
			// with the AgentPresetInfo definition in agents.go.
			"groq-compound": groqCompoundPreset(),
		}
	case TierCustomGroqSonnet:
		return map[string]*RuntimeConfig{
			"claude-sonnet": claudeSonnetPreset(),
			"groq-compound": groqCompoundPreset(),
		}
	default:
		return nil
	}
}

// claudeSonnetPreset returns a RuntimeConfig for Claude Sonnet.
// Uses "sonnet[1m]" to enable 1M context window on Max/Team plans.
// Without the [1m] suffix, --model sonnet resolves to 200K context
// because the explicit --model flag bypasses Claude Code's built-in
// plan-based auto-detection that would otherwise enable 1M.
func claudeSonnetPreset() *RuntimeConfig {
	return &RuntimeConfig{
		Provider: string(AgentClaude),
		Command:  "claude",
		Args:     []string{"--dangerously-skip-permissions", "--model", "sonnet[1m]"},
	}
}

// claudeHaikuPreset returns a RuntimeConfig for Claude Haiku.
func claudeHaikuPreset() *RuntimeConfig {
	return &RuntimeConfig{
		Provider: string(AgentClaude),
		Command:  "claude",
		Args:     []string{"--dangerously-skip-permissions", "--model", "haiku"},
	}
}

// groqCompoundPreset returns a RuntimeConfig for Groq's compound-beta model.
//
// The claude CLI is used as the SDK transport — it is redirected to Groq's
// OpenAI-compatible endpoint by overriding two Anthropic SDK env vars:
//
//	ANTHROPIC_BASE_URL  = https://api.groq.com/openai/v1
//	ANTHROPIC_API_KEY   =   (resolved at spawn time from the shell env)
//
// This gives you:
//   - Groq compound-beta reasoning on patrol/utility roles including polecat (low cost, fast)
//   - Full Claude SDK hooks / session tracking / tmux detection inherited
//   - Claude Opus on mayor and crew via the default claude preset
//
// Prerequisite: export GROQ_API_KEY=gsk_... in your shell before starting gt.
func groqCompoundPreset() *RuntimeConfig {
	// Derive from the canonical AgentGroqCompound builtin so Command, Args,
	// Env, and all normalisation logic stay in one place (agents.go).
	rc := RuntimeConfigFromPreset(AgentGroqCompound)
	// Resolve $GROQ_API_KEY at preset creation time so the settings file
	// records the live key value rather than a shell-expansion sentinel.
	if rc != nil && rc.Env != nil {
		if v, ok := rc.Env["ANTHROPIC_API_KEY"]; ok && v == "$GROQ_API_KEY" {
			rc.Env["ANTHROPIC_API_KEY"] = os.Getenv("GROQ_API_KEY")
		}
	}
	return rc
}

// ApplyCostTier writes the tier's agent and role_agents configuration to town settings.
// Only tier-managed roles are modified — custom RoleAgents entries for non-tier roles
// (or intentional non-Claude overrides) are preserved.
func ApplyCostTier(settings *TownSettings, tier CostTier) error {
	roleAgents := CostTierRoleAgents(tier)
	if roleAgents == nil {
		return fmt.Errorf("invalid cost tier: %q (valid: %s)", tier, strings.Join(ValidCostTiers(), ", "))
	}

	agents := CostTierAgents(tier)

	if settings.RoleAgents == nil {
		settings.RoleAgents = make(map[string]string)
	}

	for _, role := range TierManagedRoles {
		agentName := roleAgents[role]
		if agentName == "" {
			delete(settings.RoleAgents, role)
		} else {
			settings.RoleAgents[role] = agentName
		}
	}

	if settings.Agents == nil {
		settings.Agents = make(map[string]*RuntimeConfig)
	}

	// For standard tier, remove all tier-specific agent presets if they exist
	if tier == TierStandard {
		delete(settings.Agents, "claude-sonnet")
		delete(settings.Agents, "claude-haiku")
		delete(settings.Agents, "groq-compound")
	} else {
		for name, rc := range agents {
			settings.Agents[name] = rc
		}
	}

	// Apply effort level defaults for the tier
	roleEffort := CostTierRoleEffort(tier)
	if settings.RoleEffort == nil {
		settings.RoleEffort = make(map[string]string)
	}
	for _, role := range TierManagedRoles {
		effort := roleEffort[role]
		if effort == "" || effort == "high" {
			// "high" is the default — don't persist it
			delete(settings.RoleEffort, role)
		} else {
			settings.RoleEffort[role] = effort
		}
	}

	// Track the tier for display purposes
	settings.CostTier = string(tier)
	return nil
}

// GetCurrentTier infers the current cost tier from the settings' RoleAgents.
// Returns the tier name if it matches a known tier exactly, or empty string for custom configs.
func GetCurrentTier(settings *TownSettings) string {
	if settings.CostTier != "" && IsValidTier(settings.CostTier) {
		expected := CostTierRoleAgents(CostTier(settings.CostTier))
		if tierRolesMatch(settings.RoleAgents, expected) {
			return settings.CostTier
		}
	}

	for _, tierName := range ValidCostTiers() {
		tier := CostTier(tierName)
		expected := CostTierRoleAgents(tier)
		if tierRolesMatch(settings.RoleAgents, expected) {
			return tierName
		}
	}

	return ""
}

// tierRolesMatch checks if the actual RoleAgents map matches a tier's expected
// assignments for tier-managed roles only.
func tierRolesMatch(actual, expected map[string]string) bool {
	for _, role := range TierManagedRoles {
		actualVal := actual[role]     // "" if not present
		expectedVal := expected[role] // "" means "use default"
		if actualVal != expectedVal {
			return false
		}
	}
	return true
}

// TierDescription returns a human-readable description of the tier's model assignments.
func TierDescription(tier CostTier) string {
	switch tier {
	case TierStandard:
		return "All roles use Opus (highest quality)"
	case TierEconomy:
		return "Patrol roles use Sonnet/Haiku, workers use Opus"
	case TierBudget:
		return "Patrol roles use Haiku, workers use Sonnet"
	case TierCustomGroqOpus:
		return "Mayor/Crew → Claude Opus; Deacon/Witness/Refinery/Polecat/Boot/Dog → Groq compound-beta"
	case TierCustomGroqSonnet:
		return "Mayor → Claude Sonnet; All other roles → Groq compound-beta"
	default:
		return "Unknown tier"
	}
}

// FormatTierRoleTable returns a formatted string showing role→model and effort assignments for a tier.
func FormatTierRoleTable(tier CostTier) string {
	roleAgents := CostTierRoleAgents(tier)
	if roleAgents == nil {
		return ""
	}
	roleEffort := CostTierRoleEffort(tier)

	roles := []string{"mayor", "deacon", "witness", "refinery", "polecat", "crew", "boot", "dog"}
	var lines []string
	for _, role := range roles {
		agent := roleAgents[role]
		if agent == "" {
			agent = "(default/opus)"
		}
		effort := roleEffort[role]
		if effort == "" {
			effort = "high"
		}
		lines = append(lines, fmt.Sprintf("  %-10s %-16s effort: %s", role+":", agent, effort))
	}

	return strings.Join(lines, "\n")
}
