package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/hooks"
)

// StaleTaskDispatchCheck detects settings.json files that still reference
// the removed "gt tap guard task-dispatch" command. After the task-dispatch
// guard was removed, existing Mayor settings.json files may retain stale
// hook entries that invoke the deleted subcommand.
type StaleTaskDispatchCheck struct {
	FixableCheck
	staleTargets []hooks.Target
}

// NewStaleTaskDispatchCheck creates a check for stale task-dispatch hook references.
func NewStaleTaskDispatchCheck() *StaleTaskDispatchCheck {
	return &StaleTaskDispatchCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "stale-task-dispatch",
				CheckDescription: "Detect stale task-dispatch guard in settings.json",
				CheckCategory:    CategoryHooks,
			},
		},
	}
}

// containsTaskDispatch checks if a HooksConfig has any hook commands
// referencing the removed task-dispatch guard.
func containsTaskDispatch(cfg *hooks.HooksConfig) bool {
	for _, entry := range cfg.PreToolUse {
		for _, h := range entry.Hooks {
			if strings.Contains(h.Command, "task-dispatch") {
				return true
			}
		}
	}
	return false
}

// Run checks all managed settings.json files for stale task-dispatch references.
func (c *StaleTaskDispatchCheck) Run(ctx *CheckContext) *CheckResult {
	c.staleTargets = nil

	targets, err := hooks.DiscoverTargets(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusWarning,
			Message:  fmt.Sprintf("Failed to discover targets: %v", err),
			Category: c.Category(),
		}
	}

	var details []string
	for _, target := range targets {
		// Only check targets that have an existing settings.json
		if _, statErr := os.Stat(target.Path); statErr != nil {
			continue
		}

		current, err := hooks.LoadSettings(target.Path)
		if err != nil {
			details = append(details, fmt.Sprintf("%s: error loading: %v", target.DisplayKey(), err))
			continue
		}

		if containsTaskDispatch(&current.Hooks) {
			c.staleTargets = append(c.staleTargets, target)
			details = append(details, fmt.Sprintf("%s: contains stale task-dispatch guard", target.DisplayKey()))
		}
	}

	if len(c.staleTargets) == 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "No stale task-dispatch hooks found",
			Category: c.Category(),
		}
	}

	return &CheckResult{
		Name:     c.Name(),
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d target(s) have stale task-dispatch guard", len(c.staleTargets)),
		Details:  details,
		FixHint:  "Run 'gt doctor --fix' or 'gt hooks sync' to regenerate settings.json files",
		Category: c.Category(),
	}
}

// stripTaskDispatch removes any PreToolUse entries whose hooks reference
// the removed task-dispatch guard. This handles the case where an on-disk
// hooks-override still contains the stale command — ComputeExpected would
// re-inject it, so we strip it from the computed result before writing.
func stripTaskDispatch(cfg *hooks.HooksConfig) *hooks.HooksConfig {
	var cleaned []hooks.HookEntry
	for _, entry := range cfg.PreToolUse {
		var cleanedHooks []hooks.Hook
		for _, h := range entry.Hooks {
			if !strings.Contains(h.Command, "task-dispatch") {
				cleanedHooks = append(cleanedHooks, h)
			}
		}
		if len(cleanedHooks) > 0 {
			entry.Hooks = cleanedHooks
			cleaned = append(cleaned, entry)
		}
	}
	cfg.PreToolUse = cleaned
	return cfg
}

// Fix regenerates settings.json files that contain the stale task-dispatch guard.
// After computing expected hooks, it strips any task-dispatch references that may
// originate from on-disk hooks-overrides to ensure the fix converges.
func (c *StaleTaskDispatchCheck) Fix(ctx *CheckContext) error {
	if len(c.staleTargets) == 0 {
		return nil
	}

	var errs []string
	for _, target := range c.staleTargets {
		expected, err := hooks.ComputeExpected(target.Key)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", target.DisplayKey(), err))
			continue
		}

		// Strip task-dispatch from expected in case on-disk overrides re-inject it.
		expected = stripTaskDispatch(expected)

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

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}
