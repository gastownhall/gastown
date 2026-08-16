package cmd

import (
	"fmt"

	"github.com/jonbaldie/gastown/internal/hooks"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var hooksInitDryRun bool

var hooksInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Bootstrap base config from existing settings.json files",
	Long: `Bootstrap the hooks base config by analyzing existing settings.json files.

This scans all managed .claude/settings.json files in the workspace,
finds hooks that are common across all targets (becomes the base config),
and identifies per-target differences (becomes overrides).

After init, run 'gt hooks diff' to verify no changes would be made.

Examples:
  gt hooks init             # Bootstrap base and overrides
  gt hooks init --dry-run   # Show what would be written without writing`,
	RunE: runHooksInit,
}

func init() {
	hooksCmd.AddCommand(hooksInitCmd)
	hooksInitCmd.Flags().BoolVar(&hooksInitDryRun, "dry-run", false, "Show what would be written without writing")
}

func runHooksInit(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Check if base config already exists
	if _, err := hooks.LoadBase(); err == nil {
		return fmt.Errorf("base config already exists at %s\nUse 'gt hooks base' to edit it", hooks.BasePath())
	}

	// Discover all targets and load their current settings
	targets, err := hooks.DiscoverTargets(townRoot)
	if err != nil {
		return fmt.Errorf("discovering targets: %w", err)
	}

	// Collect hooks from all existing settings files
	var found []targetHooks

	for _, target := range targets {
		settings, err := hooks.LoadSettings(target.Path)
		if err != nil {
			continue
		}
		// Skip targets with empty hooks
		hasHooks := false
		for _, et := range hooks.EventTypes {
			if len(settings.Hooks.GetEntries(et)) > 0 {
				hasHooks = true
				break
			}
		}
		if !hasHooks {
			continue
		}
		found = append(found, targetHooks{target: target, config: &settings.Hooks})
	}

	if len(found) == 0 {
		fmt.Println("No existing hooks found in workspace settings files.")
		fmt.Println("Creating default base config...")
		base := hooks.DefaultBase()
		if hooksInitDryRun {
			data, _ := hooks.MarshalConfig(base)
			fmt.Printf("\nWould write to %s:\n%s\n", hooks.BasePath(), string(data))
			return nil
		}
		if err := hooks.SaveBase(base); err != nil {
			return fmt.Errorf("saving base config: %w", err)
		}
		fmt.Printf("%s Created base config at %s\n", style.Success.Render("✓"), hooks.BasePath())
		return nil
	}

	fmt.Printf("Found hooks in %d settings file(s)\n\n", len(found))

	// Find common hooks across all targets (intersection = base)
	base := findCommonHooks(found)

	// Find per-target differences (overrides)
	type overrideEntry struct {
		key    string
		config *hooks.HooksConfig
	}
	var overrides []overrideEntry
	seen := make(map[string]bool)

	for _, th := range found {
		diff := computeDiff(base, th.config)
		if diff == nil {
			continue
		}
		// Use the target key for the override, deduplicating
		if seen[th.target.Key] {
			continue
		}
		seen[th.target.Key] = true
		overrides = append(overrides, overrideEntry{key: th.target.Key, config: diff})
	}

	// Display or write results
	if hooksInitDryRun {
		data, _ := hooks.MarshalConfig(base)
		fmt.Printf("Would write base config to %s:\n%s\n\n", hooks.BasePath(), string(data))

		for _, o := range overrides {
			data, _ := hooks.MarshalConfig(o.config)
			fmt.Printf("Would write override %s to %s:\n%s\n\n",
				o.key, hooks.OverridePath(o.key), string(data))
		}

		fmt.Printf("%s %d base + %d override(s) would be created\n",
			style.Dim.Render("(dry-run)"), 1, len(overrides))
		return nil
	}

	// Write base config
	if err := hooks.SaveBase(base); err != nil {
		return fmt.Errorf("saving base config: %w", err)
	}
	fmt.Printf("%s Created base config at %s\n", style.Success.Render("✓"), hooks.BasePath())

	// Write overrides
	for _, o := range overrides {
		if err := hooks.SaveOverride(o.key, o.config); err != nil {
			fmt.Printf("  %s Failed to write override %s: %v\n", style.Warning.Render("!"), o.key, err)
			continue
		}
		fmt.Printf("%s Created override %s\n", style.Success.Render("✓"), o.key)
	}

	fmt.Printf("\nVerify with: %s\n", style.Dim.Render("gt hooks diff"))
	return nil
}

// findCommonHooks finds hook entries common across all targets.
// An entry is "common" if every target has the same matcher+hooks for that event type.
// Collects candidate entries from ALL targets to compute a proper intersection.
func findCommonHooks(targets []targetHooks) *hooks.HooksConfig {
	if len(targets) == 0 {
		return hooks.DefaultBase()
	}

	result := &hooks.HooksConfig{}

	for _, et := range hooks.EventTypes {
		// Collect unique candidate entries from ALL targets (not just the first).
		// Using matcher+hooks as the dedup key ensures we consider every distinct
		// entry regardless of which target introduced it.
		type entryKey struct {
			matcher string
			hooks   string // serialized hooks for comparison
		}
		seen := make(map[entryKey]hooks.HookEntry)
		for _, th := range targets {
			for _, entry := range th.config.GetEntries(et) {
				key := entryKey{matcher: entry.Matcher, hooks: hooksFingerprint(entry.Hooks)}
				if _, ok := seen[key]; !ok {
					seen[key] = entry
				}
			}
		}

		// Check each candidate: is it present in ALL targets?
		var common []hooks.HookEntry
		for _, entry := range seen {
			isCommon := true
			for _, th := range targets {
				otherEntries := th.config.GetEntries(et)
				found := false
				for _, oe := range otherEntries {
					if oe.Matcher == entry.Matcher && hooksListEqual(oe.Hooks, entry.Hooks) {
						found = true
						break
					}
				}
				if !found {
					isCommon = false
					break
				}
			}
			if isCommon {
				common = append(common, entry)
			}
		}
		if len(common) > 0 {
			result.SetEntries(et, common)
		}
	}

	return result
}

// hooksFingerprint returns a string key for a slice of hooks, used for deduplication.
func hooksFingerprint(hks []hooks.Hook) string {
	var s string
	for _, h := range hks {
		s += h.Type + ":" + h.Command + ";"
	}
	return s
}

// computeDiff returns hooks in target that are not in base, or nil if identical.
func computeDiff(base, target *hooks.HooksConfig) *hooks.HooksConfig {
	if hooks.HooksEqual(base, target) {
		return nil
	}

	diff := &hooks.HooksConfig{}
	hasDiff := false

	for _, et := range hooks.EventTypes {
		targetEntries := target.GetEntries(et)
		baseEntries := base.GetEntries(et)

		var diffEntries []hooks.HookEntry
		for _, te := range targetEntries {
			inBase := false
			for _, be := range baseEntries {
				if be.Matcher == te.Matcher && hooksListEqual(be.Hooks, te.Hooks) {
					inBase = true
					break
				}
			}
			if !inBase {
				diffEntries = append(diffEntries, te)
			}
		}
		if len(diffEntries) > 0 {
			diff.SetEntries(et, diffEntries)
			hasDiff = true
		}
	}

	if !hasDiff {
		return nil
	}
	return diff
}

// hooksListEqual checks if two hook lists are identical.
func hooksListEqual(a, b []hooks.Hook) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].Command != b[i].Command {
			return false
		}
	}
	return true
}

// targetHooks pairs a target with its parsed hooks config.
type targetHooks struct {
	target hooks.Target
	config *hooks.HooksConfig
}
