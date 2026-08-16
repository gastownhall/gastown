package config

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// Mix assignment kinds accepted by ParseMixSpec and ApplyTownMix.
const (
	MixKindRole    = "role"
	MixKindCrew    = "crew"
	MixKindDefault = "default"
)

// MixAssignment is one town-level agent assignment from a mix spec.
type MixAssignment struct {
	Kind   string
	Name   string
	Agent  string
	Effort string
}

// MixEntry is the effective agent assignment for one role or crew worker.
type MixEntry struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Agent    string `json:"agent"`
	Provider string `json:"provider,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Source   string `json:"source"`
}

// TownMix is the effective town-wide mix of agent types.
type TownMix struct {
	DefaultAgent string     `json:"default_agent"`
	Roles        []MixEntry `json:"roles"`
	Crew         []MixEntry `json:"crew,omitempty"`
	Providers    []string   `json:"providers"`
	Mixed        bool       `json:"mixed"`
}

// ParseMixSpecs parses every mix spec. The first invalid spec fails the batch.
func ParseMixSpecs(specs []string) ([]MixAssignment, error) {
	assignments := make([]MixAssignment, 0, len(specs))
	for _, spec := range specs {
		assignment, err := ParseMixSpec(spec)
		if err != nil {
			return nil, err
		}
		assignments = append(assignments, assignment)
	}
	return assignments, nil
}

// ParseMixSpec parses one assignment of the form:
//
//	role=agent
//	role=agent:effort
//	crew:name=agent
//	default=agent
func ParseMixSpec(spec string) (MixAssignment, error) {
	target, agentSpec, ok := strings.Cut(strings.TrimSpace(spec), "=")
	if !ok {
		return MixAssignment{}, fmt.Errorf("expected target=agent (got %q)", spec)
	}
	target = strings.TrimSpace(target)
	agentSpec = strings.TrimSpace(agentSpec)
	if target == "" {
		return MixAssignment{}, fmt.Errorf("empty target in %q", spec)
	}
	if agentSpec == "" {
		return MixAssignment{}, fmt.Errorf("empty agent in %q", spec)
	}

	agent, effort, err := splitAgentEffort(agentSpec)
	if err != nil {
		return MixAssignment{}, err
	}

	switch {
	case target == MixKindDefault:
		if effort != "" {
			return MixAssignment{}, fmt.Errorf("default agent cannot set effort")
		}
		return MixAssignment{Kind: MixKindDefault, Agent: agent}, nil
	case strings.HasPrefix(target, "crew:"):
		name := strings.TrimSpace(strings.TrimPrefix(target, "crew:"))
		if name == "" {
			return MixAssignment{}, fmt.Errorf("empty crew name in %q", spec)
		}
		if err := validateMixCrewName(name); err != nil {
			return MixAssignment{}, err
		}
		if effort != "" {
			return MixAssignment{}, fmt.Errorf("crew assignment cannot set effort")
		}
		return MixAssignment{Kind: MixKindCrew, Name: name, Agent: agent}, nil
	default:
		return MixAssignment{Kind: MixKindRole, Name: target, Agent: agent, Effort: effort}, nil
	}
}

func splitAgentEffort(agentSpec string) (string, string, error) {
	agent, effort, found := strings.Cut(agentSpec, ":")
	agent = strings.TrimSpace(agent)
	effort = strings.TrimSpace(effort)
	if agent == "" {
		return "", "", fmt.Errorf("empty agent")
	}
	if found && effort == "" {
		return "", "", fmt.Errorf("empty effort")
	}
	return agent, effort, nil
}

func validateMixCrewName(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("empty crew name")
	}
	if strings.ContainsAny(name, "/\\:=") {
		return fmt.Errorf("invalid crew name %q", name)
	}
	return nil
}

// ApplyTownMix writes every assignment in one save. Invalid input leaves
// the existing settings unchanged.
func ApplyTownMix(settingsPath string, assignments []MixAssignment) error {
	if len(assignments) == 0 {
		return fmt.Errorf("no mix assignments")
	}

	settings, err := LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("loading town settings: %w", err)
	}

	seenRoles := make(map[string]struct{})
	seenCrew := make(map[string]struct{})
	seenDefault := false
	for _, assignment := range assignments {
		if err := validateMixAssignment(settings, assignment); err != nil {
			return err
		}
		switch assignment.Kind {
		case MixKindDefault:
			if seenDefault {
				return fmt.Errorf("duplicate assignment for default agent")
			}
			seenDefault = true
		case MixKindRole:
			if _, ok := seenRoles[assignment.Name]; ok {
				return fmt.Errorf("duplicate assignment for role %s", assignment.Name)
			}
			seenRoles[assignment.Name] = struct{}{}
		case MixKindCrew:
			if _, ok := seenCrew[assignment.Name]; ok {
				return fmt.Errorf("duplicate assignment for crew %s", assignment.Name)
			}
			seenCrew[assignment.Name] = struct{}{}
		default:
			return fmt.Errorf("unknown mix kind %q", assignment.Kind)
		}
	}

	for _, assignment := range assignments {
		switch assignment.Kind {
		case MixKindDefault:
			settings.DefaultAgent = assignment.Agent
		case MixKindRole:
			if settings.RoleAgents == nil {
				settings.RoleAgents = make(map[string]string)
			}
			settings.RoleAgents[assignment.Name] = assignment.Agent
			if assignment.Effort != "" {
				if settings.RoleEffort == nil {
					settings.RoleEffort = make(map[string]string)
				}
				settings.RoleEffort[assignment.Name] = assignment.Effort
			} else if runtimeConfig := lookupAgentConfigIfExists(assignment.Agent, settings, nil); runtimeConfig != nil {
				if validateRuntimeEffort(runtimeConfig, settings.RoleEffort[assignment.Name]) != nil {
					delete(settings.RoleEffort, assignment.Name)
				}
			}
		case MixKindCrew:
			if settings.CrewAgents == nil {
				settings.CrewAgents = make(map[string]string)
			}
			settings.CrewAgents[assignment.Name] = assignment.Agent
		}
	}
	settings.CostTier = ""

	if err := SaveTownSettings(settingsPath, settings); err != nil {
		return fmt.Errorf("saving town settings: %w", err)
	}
	return nil
}

func validateMixAssignment(settings *TownSettings, assignment MixAssignment) error {
	switch assignment.Kind {
	case MixKindDefault:
		if assignment.Effort != "" {
			return fmt.Errorf("default agent cannot set effort")
		}
		_, err := requireKnownAgent(assignment.Agent, settings)
		return err
	case MixKindRole:
		if err := validateManagedRole(assignment.Name); err != nil {
			return err
		}
		runtimeConfig, err := requireKnownAgent(assignment.Agent, settings)
		if err != nil {
			return err
		}
		effectiveEffort := assignment.Effort
		if effectiveEffort == "" {
			effectiveEffort = settings.RoleEffort[assignment.Name]
			if effectiveEffort != "" && validateRuntimeEffort(runtimeConfig, effectiveEffort) != nil {
				return nil
			}
		}
		return validateRuntimeEffort(runtimeConfig, effectiveEffort)
	case MixKindCrew:
		if err := validateMixCrewName(assignment.Name); err != nil {
			return err
		}
		if assignment.Effort != "" {
			return fmt.Errorf("crew assignment cannot set effort")
		}
		_, err := requireKnownAgent(assignment.Agent, settings)
		return err
	default:
		return fmt.Errorf("unknown mix kind %q", assignment.Kind)
	}
}

// DescribeTownMix returns the effective agent mix from town settings.
func DescribeTownMix(settings *TownSettings) TownMix {
	if settings == nil {
		settings = NewTownSettings()
	}

	defaultAgent := settings.DefaultAgent
	if defaultAgent == "" {
		defaultAgent = string(AgentClaude)
	}

	mix := TownMix{
		DefaultAgent: defaultAgent,
		Roles:        make([]MixEntry, 0, len(TierManagedRoles)),
	}
	providers := make(map[string]struct{})

	for _, role := range TierManagedRoles {
		agent := settings.RoleAgents[role]
		source := "default"
		if agent != "" {
			source = "role"
		} else {
			agent = defaultAgent
		}
		entry := mixEntry(MixKindRole, role, agent, settings.RoleEffort[role], source, settings)
		mix.Roles = append(mix.Roles, entry)
		if entry.Provider != "" {
			providers[entry.Provider] = struct{}{}
		}
	}

	crewNames := make([]string, 0, len(settings.CrewAgents))
	for name := range settings.CrewAgents {
		crewNames = append(crewNames, name)
	}
	sort.Strings(crewNames)
	for _, name := range crewNames {
		agent := settings.CrewAgents[name]
		entry := mixEntry(MixKindCrew, name, agent, "", "crew", settings)
		mix.Crew = append(mix.Crew, entry)
		if entry.Provider != "" {
			providers[entry.Provider] = struct{}{}
		}
	}

	mix.Providers = make([]string, 0, len(providers))
	for provider := range providers {
		mix.Providers = append(mix.Providers, provider)
	}
	sort.Strings(mix.Providers)
	mix.Mixed = len(mix.Providers) > 1
	return mix
}

func requireKnownAgent(agent string, settings *TownSettings) (*RuntimeConfig, error) {
	runtimeConfig := lookupAgentConfigIfExists(agent, settings, nil)
	if runtimeConfig == nil {
		return nil, fmt.Errorf("agent %q not found (use 'gt config agent list' to see available agents)", agent)
	}
	return runtimeConfig, nil
}

func mixEntry(kind, name, agent, effort, source string, settings *TownSettings) MixEntry {
	entry := MixEntry{
		Kind:   kind,
		Name:   name,
		Agent:  agent,
		Effort: effort,
		Source: source,
	}
	if runtimeConfig := lookupAgentConfigIfExists(agent, settings, nil); runtimeConfig != nil {
		entry.Provider = runtimeConfig.Provider
	}
	return entry
}

// MixBinary is the command used by one agent in the current mix.
type MixBinary struct {
	Agent   string `json:"agent"`
	Command string `json:"command"`
	Present bool   `json:"present"`
}

// DescribeMixBinaries reports whether each distinct mix agent is on PATH.
func DescribeMixBinaries(settings *TownSettings) []MixBinary {
	mix := DescribeTownMix(settings)
	seen := make(map[string]struct{})
	var agents []string
	add := func(agent string) {
		if agent == "" {
			return
		}
		if _, ok := seen[agent]; ok {
			return
		}
		seen[agent] = struct{}{}
		agents = append(agents, agent)
	}
	add(mix.DefaultAgent)
	for _, entry := range mix.Roles {
		add(entry.Agent)
	}
	for _, entry := range mix.Crew {
		add(entry.Agent)
	}
	sort.Strings(agents)

	binaries := make([]MixBinary, 0, len(agents))
	for _, agent := range agents {
		command := agent
		if runtimeConfig := lookupAgentConfigIfExists(agent, settings, nil); runtimeConfig != nil && runtimeConfig.Command != "" {
			command = runtimeConfig.Command
		}
		_, err := exec.LookPath(command)
		binaries = append(binaries, MixBinary{
			Agent:   agent,
			Command: command,
			Present: err == nil,
		})
	}
	return binaries
}
