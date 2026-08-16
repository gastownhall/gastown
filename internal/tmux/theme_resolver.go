package tmux

import (
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/constants"
)

// ResolveSessionTheme returns the configured tmux theme for a session.
// A nil theme means tmux theming is explicitly disabled.
// crewMember is the crew member name (e.g. "krieger"); pass "" for non-crew roles.
// When non-empty, crew_themes config is checked before role-level fallback.
func ResolveSessionTheme(townRoot, rigName, role, crewMember string) *Theme {
	role = normalizeThemeRole(role)

	if rigTheme := resolveRigSessionTheme(townRoot, rigName, role, crewMember); rigTheme != unresolvedTheme {
		return rigTheme
	}

	if townTheme := resolveTownSessionTheme(townRoot, role, crewMember); townTheme != unresolvedTheme {
		return townTheme
	}

	if themeName, ok := config.BuiltinRoleThemes()[role]; ok {
		if theme := GetThemeByName(themeName); theme != nil {
			return theme
		}
	}

	switch role {
	case constants.RoleMayor:
		theme := MayorTheme()
		return &theme
	case constants.RoleDeacon:
		theme := DeaconTheme()
		return &theme
	case "dog":
		theme := DogTheme()
		return &theme
	default:
		if rigName == "" {
			return nil
		}
		theme := AssignTheme(rigName)
		return &theme
	}
}

var unresolvedTheme = &Theme{Name: "__unresolved__"}

func resolveRigSessionTheme(townRoot, rigName, role, crewMember string) *Theme {
	if townRoot == "" || rigName == "" {
		return unresolvedTheme
	}

	settingsPath := config.RigSettingsPath(filepath.Join(townRoot, rigName))
	settings, err := config.LoadRigSettings(settingsPath)
	if err != nil || settings.Theme == nil {
		return unresolvedTheme
	}

	// Per-member theme takes priority over role-level theme.
	if crewMember != "" && settings.Theme.CrewThemes != nil {
		if resolved, ok := resolveRoleThemeName(settings.Theme.CrewThemes[crewMember]); ok {
			return resolved
		}
	}

	if settings.Theme.RoleThemes != nil {
		if resolved, ok := resolveRoleThemeName(settings.Theme.RoleThemes[role]); ok {
			return resolved
		}
	}

	return resolveThemeConfig(settings.Theme)
}

func resolveTownSessionTheme(townRoot, role, crewMember string) *Theme {
	if townRoot == "" {
		return unresolvedTheme
	}

	mayorCfg, err := config.LoadMayorConfig(filepath.Join(townRoot, "mayor", "config.json"))
	if err != nil || mayorCfg.Theme == nil {
		return unresolvedTheme
	}

	// Per-member theme takes priority over role defaults at town level too.
	if crewMember != "" && mayorCfg.Theme.CrewThemes != nil {
		if resolved, ok := resolveRoleThemeName(mayorCfg.Theme.CrewThemes[crewMember]); ok {
			return resolved
		}
	}

	if mayorCfg.Theme.RoleDefaults != nil {
		if resolved, ok := resolveRoleThemeName(mayorCfg.Theme.RoleDefaults[role]); ok {
			return resolved
		}
	}

	if mayorCfg.Theme.Disabled {
		return nil
	}
	if mayorCfg.Theme.Custom != nil {
		return customTheme("custom", mayorCfg.Theme.Custom)
	}
	if mayorCfg.Theme.Name != "" {
		if theme := GetThemeByName(mayorCfg.Theme.Name); theme != nil {
			return theme
		}
	}

	return unresolvedTheme
}

func resolveThemeConfig(cfg *config.ThemeConfig) *Theme {
	if cfg == nil {
		return unresolvedTheme
	}
	if cfg.Disabled {
		return nil
	}
	if cfg.Custom != nil {
		return customTheme("custom", cfg.Custom)
	}
	if cfg.Name != "" {
		if theme := GetThemeByName(cfg.Name); theme != nil {
			return theme
		}
	}
	return unresolvedTheme
}

func resolveRoleThemeName(name string) (*Theme, bool) {
	if name == "" {
		return nil, false
	}
	if strings.EqualFold(name, "none") {
		return nil, true
	}
	if theme := GetThemeByName(name); theme != nil {
		return theme, true
	}
	return nil, false
}

func customTheme(name string, custom *config.CustomTheme) *Theme {
	if custom == nil {
		return nil
	}
	themeName := name
	if themeName == "" {
		themeName = "custom"
	}
	return &Theme{
		Name: themeName,
		BG:   custom.BG,
		FG:   custom.FG,
	}
}

func normalizeThemeRole(role string) string {
	switch role {
	case "coordinator":
		return constants.RoleMayor
	case "health-check":
		return constants.RoleDeacon
	default:
		return role
	}
}
