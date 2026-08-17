package now

import (
	"fmt"
	"strings"

	"github.com/jonbaldie/gastown/internal/config"
)

// AliasConfig builds the now-mayor / now-workers runtime alias.
func AliasConfig(profile Profile) *config.RuntimeConfig {
	rc := config.RuntimeConfigFromPreset(config.AgentPreset(profile.Runtime))
	if rc == nil {
		rc = &config.RuntimeConfig{}
	}
	rc.Provider = profile.Runtime
	if profile.Model != "" {
		rc.Args = withModel(rc.Args, profile.Model)
	}
	return rc
}

// MixAssignments returns mix specs for a mayor or worker profile.
func MixAssignments(alias string, roles []string, effort string) []config.MixAssignment {
	assignments := make([]config.MixAssignment, 0, len(roles))
	for _, role := range roles {
		assignments = append(assignments, config.MixAssignment{
			Kind:   config.MixKindRole,
			Name:   role,
			Agent:  alias,
			Effort: effort,
		})
	}
	return assignments
}

func withModel(args []string, model string) []string {
	out := make([]string, 0, len(args)+2)
	skipNext := false
	for _, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if arg == "--model" {
			skipNext = true
			continue
		}
		if strings.HasPrefix(arg, "--model=") {
			continue
		}
		out = append(out, arg)
	}
	return append(out, "--model", model)
}

// ApplyMix writes now-mayor/now-workers aliases and role assignments.
// MayorSpec/WorkersSpec empty means "keep existing aliases unless missing".
func ApplyMix(townRoot, mayorSpec, workersSpec string, mayorProfile, workersProfile Profile) (mayorChanged bool, err error) {
	settingsPath := config.TownSettingsPath(townRoot)
	settings, err := config.LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		return false, fmt.Errorf("loading town settings: %w", err)
	}

	needMayor := mayorSpec != "" || settings.Agents == nil || settings.Agents[MayorAlias] == nil
	needWorkers := workersSpec != "" || settings.Agents == nil || settings.Agents[WorkersAlias] == nil
	if !needMayor && !needWorkers {
		return false, nil
	}

	before := mayorMixFingerprint(settings)

	if settings.Agents == nil {
		settings.Agents = make(map[string]*config.RuntimeConfig)
	}
	if needMayor {
		settings.Agents[MayorAlias] = AliasConfig(mayorProfile)
	}
	if needWorkers {
		settings.Agents[WorkersAlias] = AliasConfig(workersProfile)
	}
	if err := config.SaveTownSettings(settingsPath, settings); err != nil {
		return false, fmt.Errorf("saving agent aliases: %w", err)
	}

	var assignments []config.MixAssignment
	if needMayor {
		assignments = append(assignments, MixAssignments(MayorAlias, MayorRoles, mayorProfile.Effort)...)
	}
	if needWorkers {
		assignments = append(assignments, MixAssignments(WorkersAlias, WorkerRoles, workersProfile.Effort)...)
	}
	if err := config.ApplyTownMix(settingsPath, assignments); err != nil {
		return false, err
	}

	after, err := config.LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		return false, err
	}
	return mayorMixFingerprint(after) != before, nil
}

func mayorMixFingerprint(settings *config.TownSettings) string {
	if settings == nil {
		return ""
	}
	mayorAgent := settings.RoleAgents["mayor"]
	mayorEffort := settings.RoleEffort["mayor"]
	mayorArgs := ""
	if settings.Agents != nil {
		if rc := settings.Agents[MayorAlias]; rc != nil {
			mayorArgs = strings.Join(rc.Args, " ")
		}
	}
	return strings.Join([]string{mayorAgent, mayorEffort, mayorArgs}, "|")
}
