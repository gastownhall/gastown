package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// RigManagedRoles are roles whose agent configuration may be overridden by a rig.
var RigManagedRoles = []string{"witness", "refinery", "polecat", "crew"}

// RigRoleAssignment is the effective persisted agent and effort for one rig role.
type RigRoleAssignment struct {
	Role         string
	Agent        string
	Effort       string
	RoleSpecific bool
}

// SetTownRole assigns an agent profile and optional effort to a managed role.
// An empty effort preserves the role's existing effort setting.
func SetTownRole(settingsPath, role, agent, effort string) error {
	if err := validateManagedRole(role); err != nil {
		return err
	}

	settings, err := LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("loading town settings: %w", err)
	}
	runtimeConfig := lookupAgentConfigIfExists(agent, settings, nil)
	if runtimeConfig == nil {
		return fmt.Errorf("agent %q not found (use 'gt config agent list' to see available agents)", agent)
	}
	effectiveEffort := requestedOrConfiguredEffort(role, effort, settings, nil)
	if err := validateRuntimeEffort(runtimeConfig, effectiveEffort); err != nil {
		return err
	}

	if settings.RoleAgents == nil {
		settings.RoleAgents = make(map[string]string)
	}
	settings.RoleAgents[role] = agent
	if effort != "" {
		if settings.RoleEffort == nil {
			settings.RoleEffort = make(map[string]string)
		}
		settings.RoleEffort[role] = effort
	}
	settings.CostTier = ""

	if err := SaveTownSettings(settingsPath, settings); err != nil {
		return fmt.Errorf("saving town settings: %w", err)
	}
	return nil
}

// SetRigRole assigns an agent profile and optional effort to a rig-managed role.
// An empty effort preserves the role's existing effort setting.
func SetRigRole(townRoot, rigPath, role, agent, effort string) error {
	if err := validateRigManagedRole(role); err != nil {
		return err
	}
	townSettings, rigSettings, err := loadRoleSettings(townRoot, rigPath)
	if err != nil {
		return err
	}
	runtimeConfig := lookupAgentConfigIfExists(agent, townSettings, rigSettings)
	if runtimeConfig == nil {
		return fmt.Errorf("agent %q not found (use 'gt config agent list' to see available agents)", agent)
	}
	effectiveEffort := requestedOrConfiguredEffort(role, effort, townSettings, rigSettings)
	if err := validateRuntimeEffort(runtimeConfig, effectiveEffort); err != nil {
		return err
	}

	if rigSettings.RoleAgents == nil {
		rigSettings.RoleAgents = make(map[string]string)
	}
	rigSettings.RoleAgents[role] = agent
	if effort != "" {
		if rigSettings.RoleEffort == nil {
			rigSettings.RoleEffort = make(map[string]string)
		}
		rigSettings.RoleEffort[role] = effort
	}
	return saveRigRoleSettings(rigPath, rigSettings)
}

// UnsetRigRole clears the agent and effort assigned to a rig-managed role.
func UnsetRigRole(rigPath, role string) error {
	if err := validateRigManagedRole(role); err != nil {
		return err
	}
	rigSettings, err := LoadRigSettings(RigSettingsPath(rigPath))
	if err != nil {
		return fmt.Errorf("loading rig settings: %w", err)
	}
	delete(rigSettings.RoleAgents, role)
	delete(rigSettings.RoleEffort, role)
	return saveRigRoleSettings(rigPath, rigSettings)
}

// ValidateRigRoleAgent validates a role_agents dot-path assignment without saving it.
func ValidateRigRoleAgent(townRoot, rigPath, role, agent string) error {
	if err := validateRigManagedRole(role); err != nil {
		return err
	}
	townSettings, rigSettings, err := loadRoleSettings(townRoot, rigPath)
	if err != nil {
		return err
	}
	runtimeConfig := lookupAgentConfigIfExists(agent, townSettings, rigSettings)
	if runtimeConfig == nil {
		return fmt.Errorf("agent %q not found (use 'gt config agent list' to see available agents)", agent)
	}
	return validateRuntimeEffort(runtimeConfig, configuredRoleEffort(role, townSettings, rigSettings))
}

// ValidateRigRoleEffort validates a role_effort dot-path assignment against
// the runtime currently selected for that role.
func ValidateRigRoleEffort(townRoot, rigPath, role, effort string) error {
	if err := validateRigManagedRole(role); err != nil {
		return err
	}
	townSettings, rigSettings, err := loadRoleSettings(townRoot, rigPath)
	if err != nil {
		return err
	}
	runtimeConfig := configuredRoleRuntime(role, townSettings, rigSettings)
	if runtimeConfig == nil {
		agent, _ := configuredRoleAgent(role, townSettings, rigSettings)
		return fmt.Errorf("configured agent %q for role %q not found", agent, role)
	}
	return validateRuntimeEffort(runtimeConfig, effort)
}

// ResolveRigRoleAssignments loads and resolves every rig-managed role. Unlike
// the runtime fallback APIs, it returns malformed settings and registry errors
// so diagnostic commands never print defaults for unreadable configuration.
func ResolveRigRoleAssignments(townRoot, rigPath string) ([]RigRoleAssignment, error) {
	townSettings, rigSettings, err := loadRoleSettings(townRoot, rigPath)
	if err != nil {
		return nil, err
	}

	assignments := make([]RigRoleAssignment, 0, len(RigManagedRoles))
	for _, role := range RigManagedRoles {
		agent, roleSpecific := configuredRoleAgent(role, townSettings, rigSettings)
		effort := configuredRoleEffort(role, townSettings, rigSettings)
		if tierName := os.Getenv("GT_COST_TIER"); tierName != "" && IsValidTier(tierName) {
			if roleEffort := CostTierRoleEffort(CostTier(tierName)); roleEffort != nil {
				effort = roleEffort[role]
			}
		}
		assignments = append(assignments, RigRoleAssignment{
			Role:         role,
			Agent:        agent,
			Effort:       effort,
			RoleSpecific: roleSpecific,
		})
	}
	return assignments, nil
}

// UnsetTownRole clears the agent and effort assigned to a managed role.
func UnsetTownRole(settingsPath, role string) error {
	if err := validateManagedRole(role); err != nil {
		return err
	}

	settings, err := LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("loading town settings: %w", err)
	}
	delete(settings.RoleAgents, role)
	delete(settings.RoleEffort, role)
	settings.CostTier = ""

	if err := SaveTownSettings(settingsPath, settings); err != nil {
		return fmt.Errorf("saving town settings: %w", err)
	}
	return nil
}

func validateManagedRole(role string) error {
	return validateRoleFrom(role, TierManagedRoles)
}

func validateRigManagedRole(role string) error {
	return validateRoleFrom(role, RigManagedRoles)
}

func validateRoleFrom(role string, validRoles []string) error {
	for _, candidate := range validRoles {
		if role == candidate {
			return nil
		}
	}
	return fmt.Errorf("unknown role %q (valid: %s)", role, strings.Join(validRoles, ", "))
}

func validateRuntimeEffort(runtimeConfig *RuntimeConfig, effort string) error {
	if effort == "" {
		return nil
	}
	if isValidEffortLevelForRuntime(runtimeConfig, effort) {
		return nil
	}
	return fmt.Errorf("invalid effort %q (valid: %s)", effort, strings.Join(validEffortLevelsForRuntime(runtimeConfig), ", "))
}

func loadRoleSettings(townRoot, rigPath string) (*TownSettings, *RigSettings, error) {
	townSettingsPath := TownSettingsPath(townRoot)
	townSettings, err := LoadOrCreateTownSettings(townSettingsPath)
	if err != nil {
		return nil, nil, fmt.Errorf("loading town settings %s: %w", townSettingsPath, err)
	}
	rigSettingsPath := RigSettingsPath(rigPath)
	rigSettings, err := LoadRigSettings(rigSettingsPath)
	if errors.Is(err, ErrNotFound) {
		rigSettings = NewRigSettings()
	} else if err != nil {
		return nil, nil, fmt.Errorf("loading rig settings %s: %w", rigSettingsPath, err)
	}
	townRegistryPath := DefaultAgentRegistryPath(townRoot)
	if err := LoadAgentRegistry(townRegistryPath); err != nil {
		return nil, nil, fmt.Errorf("loading town agent registry %s: %w", townRegistryPath, err)
	}
	rigRegistryPath := RigAgentRegistryPath(rigPath)
	if err := LoadRigAgentRegistry(rigRegistryPath); err != nil {
		return nil, nil, fmt.Errorf("loading rig agent registry %s: %w", rigRegistryPath, err)
	}
	return townSettings, rigSettings, nil
}

func configuredRoleRuntime(role string, townSettings *TownSettings, rigSettings *RigSettings) *RuntimeConfig {
	agent, _ := configuredRoleAgent(role, townSettings, rigSettings)
	return lookupAgentConfigIfExists(agent, townSettings, rigSettings)
}

func configuredRoleAgent(role string, townSettings *TownSettings, rigSettings *RigSettings) (string, bool) {
	if rigSettings != nil && rigSettings.RoleAgents[role] != "" {
		return rigSettings.RoleAgents[role], true
	}
	if townSettings != nil && townSettings.RoleAgents[role] != "" {
		return townSettings.RoleAgents[role], true
	}
	if rigSettings != nil && rigSettings.Agent != "" {
		return rigSettings.Agent, false
	}
	if townSettings != nil && townSettings.DefaultAgent != "" {
		return townSettings.DefaultAgent, false
	}
	return string(AgentClaude), false
}

func configuredRoleEffort(role string, townSettings *TownSettings, rigSettings *RigSettings) string {
	if rigSettings != nil && rigSettings.RoleEffort[role] != "" {
		return rigSettings.RoleEffort[role]
	}
	if townSettings != nil {
		return townSettings.RoleEffort[role]
	}
	return ""
}

func requestedOrConfiguredEffort(role, requested string, townSettings *TownSettings, rigSettings *RigSettings) string {
	if requested != "" {
		return requested
	}
	return configuredRoleEffort(role, townSettings, rigSettings)
}

func saveRigRoleSettings(rigPath string, settings *RigSettings) error {
	if err := SaveRigSettings(RigSettingsPath(rigPath), settings); err != nil {
		return fmt.Errorf("saving rig settings: %w", err)
	}
	return nil
}
