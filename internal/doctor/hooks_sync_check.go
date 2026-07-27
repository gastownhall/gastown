package doctor

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/hooks"
)

// templateTarget tracks a non-Claude template-based agent file that is out of sync.
type templateTarget struct {
	path           string
	dir            string
	provider       string
	role           string
	hooksDir       string
	settingsFile   string
	useSettingsDir bool
}

// HooksSyncCheck verifies all hook/settings files match what gt hooks sync would generate.
type HooksSyncCheck struct {
	FixableCheck
	outOfSync         []hooks.Target   // Claude targets
	templateOutOfSync []templateTarget // Non-Claude template-based targets
}

// NewHooksSyncCheck creates a new hooks sync validation check.
func NewHooksSyncCheck() *HooksSyncCheck {
	return &HooksSyncCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "hooks-sync",
				CheckDescription: "Verify hooks settings.json files are in sync",
				CheckCategory:    CategoryHooks,
			},
		},
	}
}

// Run checks all managed hook/settings files for sync status.
func (c *HooksSyncCheck) Run(ctx *CheckContext) *CheckResult {
	c.outOfSync = nil
	c.templateOutOfSync = nil

	var details []string
	totalTargets := 0

	// Loop 1: Claude targets — use base+override merge system via DiscoverTargets.
	targets, err := hooks.DiscoverTargets(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusWarning,
			Message:  fmt.Sprintf("Failed to discover targets: %v", err),
			Category: c.Category(),
		}
	}

	for _, target := range targets {
		totalTargets++

		expected, err := hooks.ComputeExpected(target.Key)
		if err != nil {
			details = append(details, fmt.Sprintf("%s: error computing expected: %v", target.DisplayKey(), err))
			continue
		}

		current, err := hooks.LoadSettings(target.Path)
		if err != nil {
			details = append(details, fmt.Sprintf("%s: error loading: %v", target.DisplayKey(), err))
			continue
		}

		_, statErr := os.Stat(target.Path)
		fileExists := statErr == nil

		if !fileExists || !hooks.HooksEqual(expected, &current.Hooks) || !hooks.HasClaudePromptDefaults(current) {
			c.outOfSync = append(c.outOfSync, target)
			if !fileExists {
				details = append(details, fmt.Sprintf("%s: missing", target.DisplayKey()))
			} else if !hooks.HasClaudePromptDefaults(current) {
				details = append(details, fmt.Sprintf("%s: missing Claude prompt defaults", target.DisplayKey()))
			} else {
				details = append(details, fmt.Sprintf("%s: out of sync", target.DisplayKey()))
			}
		}
	}

	// Loop 2: Non-Claude template-based agents — use DiscoverRoleLocations + SyncForRole comparison.
	locations, locErr := hooks.DiscoverRoleLocations(ctx.TownRoot)
	if locErr != nil {
		details = append(details, fmt.Sprintf("discovering role locations: %v", locErr))
	} else {
		for _, loc := range locations {
			rigPath := ""
			if loc.Rig != "" {
				rigPath = filepath.Join(ctx.TownRoot, loc.Rig)
			}
			// Use ResolveRoleAgentName (not ResolveRoleAgentConfig) so that checks are
			// based on the *configured* agent, not the *resolved* one.
			// ResolveRoleAgentConfig falls back to claude when the agent binary is not
			// found in PATH (e.g., in CI), which would silently skip non-Claude targets.
			agentName, _ := config.ResolveRoleAgentName(loc.Role, ctx.TownRoot, rigPath)
			if agentName == "" {
				continue
			}
			preset := config.GetAgentPresetByName(agentName)
			if preset == nil || preset.HooksDir == "" || preset.HooksSettingsFile == "" {
				continue
			}
			hooksProvider := preset.HooksProvider
			if hooksProvider == "" {
				hooksProvider = agentName
			}
			// Claude targets are handled by Loop 1.
			if hooksProvider == "claude" {
				continue
			}

			useSettingsDir := preset.HooksUseSettingsDir

			var checkDirs []string
			if loc.Rig == "" || useSettingsDir {
				checkDirs = []string{loc.Dir}
			} else {
				checkDirs = hooks.DiscoverWorktreesForRole(loc.Dir, loc.Role)
			}

			for _, dir := range checkDirs {
				totalTargets++
				targetPath := filepath.Join(dir, preset.HooksDir, preset.HooksSettingsFile)

				expected, err := hooks.ComputeExpectedTemplate(hooksProvider, preset.HooksSettingsFile, loc.Role)
				if err != nil {
					details = append(details, fmt.Sprintf("%s (%s): error computing template: %v", targetPath, hooksProvider, err))
					continue
				}

				actual, readErr := os.ReadFile(targetPath)
				if readErr != nil {
					// File missing
					c.templateOutOfSync = append(c.templateOutOfSync, templateTarget{
						path: targetPath, dir: dir, provider: hooksProvider,
						role: loc.Role, hooksDir: preset.HooksDir,
						settingsFile: preset.HooksSettingsFile, useSettingsDir: useSettingsDir,
					})
					details = append(details, fmt.Sprintf("%s (%s): missing", targetPath, hooksProvider))
					continue
				}

				// Compare: structural for JSON, byte-exact for other files.
				inSync := false
				if filepath.Ext(preset.HooksSettingsFile) == ".json" {
					inSync = hooks.TemplateContentEqual(expected, actual)
				} else {
					inSync = bytes.Equal(expected, actual)
				}

				if !inSync {
					c.templateOutOfSync = append(c.templateOutOfSync, templateTarget{
						path: targetPath, dir: dir, provider: hooksProvider,
						role: loc.Role, hooksDir: preset.HooksDir,
						settingsFile: preset.HooksSettingsFile, useSettingsDir: useSettingsDir,
					})
					details = append(details, fmt.Sprintf("%s (%s): out of sync", targetPath, hooksProvider))
				}
			}
		}
	}

	outOfSyncCount := len(c.outOfSync) + len(c.templateOutOfSync)
	if outOfSyncCount == 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  fmt.Sprintf("All %d hook targets in sync", totalTargets),
			Category: c.Category(),
		}
	}

	return &CheckResult{
		Name:     c.Name(),
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d target(s) out of sync", outOfSyncCount),
		Details:  details,
		FixHint:  "Run 'gt doctor --fix hooks-sync' to regenerate settings files",
		Category: c.Category(),
	}
}

// Fix brings all out-of-sync targets back into sync.
func (c *HooksSyncCheck) Fix(ctx *CheckContext) error {
	if len(c.outOfSync) == 0 && len(c.templateOutOfSync) == 0 {
		return nil
	}

	var errs []string

	// Fix Claude targets via merge system.
	for _, target := range c.outOfSync {
		expected, err := hooks.ComputeExpected(target.Key)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", target.DisplayKey(), err))
			continue
		}

		current, err := hooks.LoadSettings(target.Path)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", target.DisplayKey(), err))
			continue
		}

		current.Hooks = *expected

		if current.EnabledPlugins == nil {
			current.EnabledPlugins = make(map[string]bool)
		}
		current.EnabledPlugins["beads@beads-marketplace"] = false

		claudeDir := filepath.Dir(target.Path)
		if err := os.MkdirAll(claudeDir, 0755); err != nil {
			errs = append(errs, fmt.Sprintf("%s: creating dir: %v", target.DisplayKey(), err))
			continue
		}

		data, err := hooks.MarshalSettings(current)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: marshal: %v", target.DisplayKey(), err))
			continue
		}
		data = append(data, '\n')

		if err := os.WriteFile(target.Path, data, 0644); err != nil {
			errs = append(errs, fmt.Sprintf("%s: write: %v", target.DisplayKey(), err))
			continue
		}
	}

	// Fix template-based targets via SyncForRole.
	for _, tt := range c.templateOutOfSync {
		_, err := hooks.SyncForRole(tt.provider, tt.dir, tt.dir, tt.role,
			tt.hooksDir, tt.settingsFile, tt.useSettingsDir)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", tt.path, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}
