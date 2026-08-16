package session

import (
	"path/filepath"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/jonbaldie/gastown/internal/workspace"
)

// ResolveWindowTint resolves the window tint style for a session.
// Resolution order mirrors status bar theming:
//  1. Per-rig role tint (rig/settings/config.json → theme.window_tint.role_tints)
//  2. Global role tint (mayor/config.json → theme.window_tint.role_tints)
//  3. Per-rig window tint (rig/settings/config.json → theme.window_tint.name/custom)
//  4. Global window tint (mayor/config.json → theme.window_tint.name/custom)
//  5. Fallback: disabled (nil) — no window tinting by default
//
// Returns nil if window tinting is disabled or not configured.
// Returns a WindowStyle if window tinting is enabled.
func ResolveWindowTint(rig, role string) *tmux.WindowStyle {
	townRoot, _ := workspace.FindFromCwd()

	var rigWindowTint, globalWindowTint *config.WindowTint

	// Load per-rig window tint config.
	if townRoot != "" && rig != "" {
		settingsPath := filepath.Join(townRoot, rig, "settings", "config.json")
		if settings, err := config.LoadRigSettings(settingsPath); err == nil {
			if settings.Theme != nil {
				rigWindowTint = settings.Theme.WindowTint
			}
		}
	}

	// Load global window tint config.
	if townRoot != "" {
		mayorConfigPath := filepath.Join(townRoot, "mayor", "config.json")
		if mayorCfg, err := config.LoadMayorConfig(mayorConfigPath); err == nil {
			if mayorCfg.Theme != nil {
				globalWindowTint = mayorCfg.Theme.WindowTint
			}
		}
	}

	// If the rig has its own window_tint config, it's the final word.
	// The rig either specifies exact colors or returns nil (inherit from status bar).
	// This prevents global role_tints from overriding rig-level intent — e.g.,
	// a rig with crew_themes wants window tint to match per-member status bar colors,
	// not a global role-level default.
	if rigWindowTint != nil {
		if rigWindowTint.Enabled != nil && !*rigWindowTint.Enabled {
			return nil
		}
		if rigWindowTint.RoleTints != nil {
			if themeName, ok := rigWindowTint.RoleTints[role]; ok {
				if theme := tmux.GetThemeByName(themeName); theme != nil {
					return &tmux.WindowStyle{BG: theme.BG, FG: theme.FG}
				}
			}
		}
		if rigWindowTint.Custom != nil {
			return &tmux.WindowStyle{BG: rigWindowTint.Custom.BG, FG: rigWindowTint.Custom.FG}
		}
		if rigWindowTint.Name != "" {
			if theme := tmux.GetThemeByName(rigWindowTint.Name); theme != nil {
				return &tmux.WindowStyle{BG: theme.BG, FG: theme.FG}
			}
		}
		// Rig opted in but provided no specific colors → return nil so the
		// caller inherits from the status bar theme (which includes crew_themes).
		return nil
	}

	// No rig-level window_tint — fall through to global config.
	if globalWindowTint != nil {
		if globalWindowTint.Enabled != nil && !*globalWindowTint.Enabled {
			return nil
		}
		if globalWindowTint.RoleTints != nil {
			if themeName, ok := globalWindowTint.RoleTints[role]; ok {
				if theme := tmux.GetThemeByName(themeName); theme != nil {
					return &tmux.WindowStyle{BG: theme.BG, FG: theme.FG}
				}
			}
		}
		if globalWindowTint.Custom != nil {
			return &tmux.WindowStyle{BG: globalWindowTint.Custom.BG, FG: globalWindowTint.Custom.FG}
		}
		if globalWindowTint.Name != "" {
			if theme := tmux.GetThemeByName(globalWindowTint.Name); theme != nil {
				return &tmux.WindowStyle{BG: theme.BG, FG: theme.FG}
			}
		}
	}

	// No window tint configured — disabled by default.
	return nil
}

// DefaultTintFactor is the default darkening factor for window backgrounds
// when inheriting from the status bar theme.
const DefaultTintFactor = 0.4

// ResolveTintFactor returns the tint_factor from the most specific config level.
// Falls back to DefaultTintFactor if not configured.
func ResolveTintFactor(rig string) float64 {
	townRoot, _ := workspace.FindFromCwd()

	// Check per-rig config.
	if townRoot != "" && rig != "" {
		settingsPath := filepath.Join(townRoot, rig, "settings", "config.json")
		if settings, err := config.LoadRigSettings(settingsPath); err == nil {
			if settings.Theme != nil && settings.Theme.WindowTint != nil && settings.Theme.WindowTint.TintFactor != nil {
				return *settings.Theme.WindowTint.TintFactor
			}
		}
	}

	// Check global config.
	if townRoot != "" {
		mayorConfigPath := filepath.Join(townRoot, "mayor", "config.json")
		if mayorCfg, err := config.LoadMayorConfig(mayorConfigPath); err == nil {
			if mayorCfg.Theme != nil && mayorCfg.Theme.WindowTint != nil && mayorCfg.Theme.WindowTint.TintFactor != nil {
				return *mayorCfg.Theme.WindowTint.TintFactor
			}
		}
	}

	return DefaultTintFactor
}

// IsWindowTintEnabled checks if window tinting is enabled at any config level.
// Returns true if enabled explicitly; false if disabled or not configured.
func IsWindowTintEnabled(rig string) bool {
	townRoot, _ := workspace.FindFromCwd()

	// Check per-rig config.
	if townRoot != "" && rig != "" {
		settingsPath := filepath.Join(townRoot, rig, "settings", "config.json")
		if settings, err := config.LoadRigSettings(settingsPath); err == nil {
			if settings.Theme != nil && settings.Theme.WindowTint != nil && settings.Theme.WindowTint.Enabled != nil {
				return *settings.Theme.WindowTint.Enabled
			}
		}
	}

	// Check global config.
	if townRoot != "" {
		mayorConfigPath := filepath.Join(townRoot, "mayor", "config.json")
		if mayorCfg, err := config.LoadMayorConfig(mayorConfigPath); err == nil {
			if mayorCfg.Theme != nil && mayorCfg.Theme.WindowTint != nil && mayorCfg.Theme.WindowTint.Enabled != nil {
				return *mayorCfg.Theme.WindowTint.Enabled
			}
		}
	}

	return false
}
