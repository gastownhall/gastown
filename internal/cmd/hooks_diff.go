package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/charmbracelet/lipgloss"
	"github.com/jonbaldie/gastown/internal/hooks"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var hooksDiffCmd = &cobra.Command{
	Use:   "diff [target]",
	Short: "Show what sync would change",
	Long: `Show what 'gt hooks sync' would change without applying.

Compares the current .claude/settings.json files against what would
be generated from base + overrides. Uses color to highlight additions
and removals.

Exit codes:
  0 - No changes pending
  1 - Changes would be applied

Examples:
  gt hooks diff                    # Show all pending changes
  gt hooks diff gastown/crew       # Show changes for specific target`,
	RunE: runHooksDiff,
}

func init() {
	hooksCmd.AddCommand(hooksDiffCmd)
}

// diffStyles for colored diff output.
var (
	diffAdd    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#86b300", Dark: "#c2d94c"})
	diffRemove = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#f07171", Dark: "#f07178"})
)

func runHooksDiff(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	targets, err := hooks.DiscoverTargets(townRoot)
	if err != nil {
		return fmt.Errorf("discovering targets: %w", err)
	}

	// Filter to specific target if provided
	if len(args) > 0 {
		filter := args[0]
		var filtered []hooks.Target
		for _, t := range targets {
			if t.Key == filter || t.DisplayKey() == filter {
				filtered = append(filtered, t)
			}
		}
		if len(filtered) == 0 {
			return fmt.Errorf("no targets match %q", filter)
		}
		targets = filtered
	}

	hasChanges := false

	for _, target := range targets {
		expected, err := hooks.ComputeExpected(target.Key)
		if err != nil {
			return fmt.Errorf("computing expected config for %s: %w", target.DisplayKey(), err)
		}

		current, err := hooks.LoadSettings(target.Path)
		if err != nil {
			return fmt.Errorf("loading current settings for %s: %w", target.DisplayKey(), err)
		}

		if hooks.HooksEqual(expected, &current.Hooks) {
			continue
		}

		// Compute relative path from town root for display
		relPath, err := filepath.Rel(townRoot, target.Path)
		if err != nil {
			relPath = target.Path
		}

		changes := diffHooksConfigs(&current.Hooks, expected)
		if len(changes) == 0 {
			continue
		}

		hasChanges = true
		fmt.Printf("%s:\n", style.Bold.Render(relPath))
		for _, change := range changes {
			fmt.Print(change)
		}
		fmt.Println()
	}

	if !hasChanges {
		fmt.Println(style.Dim.Render("No changes pending - all targets in sync"))
		return nil
	}

	// Exit with code 1 to indicate changes pending (for scripting)
	return NewSilentExit(1)
}

// diffHooksConfigs compares current and expected configs, returning formatted diff lines.
func diffHooksConfigs(current, expected *hooks.HooksConfig) []string {
	var lines []string

	hookTypes := []struct {
		name     string
		current  []hooks.HookEntry
		expected []hooks.HookEntry
	}{
		{"PreToolUse", current.PreToolUse, expected.PreToolUse},
		{"PostToolUse", current.PostToolUse, expected.PostToolUse},
		{"SessionStart", current.SessionStart, expected.SessionStart},
		{"Stop", current.Stop, expected.Stop},
		{"PreCompact", current.PreCompact, expected.PreCompact},
		{"UserPromptSubmit", current.UserPromptSubmit, expected.UserPromptSubmit},
		{"WorktreeCreate", current.WorktreeCreate, expected.WorktreeCreate},
		{"WorktreeRemove", current.WorktreeRemove, expected.WorktreeRemove},
	}

	for _, ht := range hookTypes {
		typeDiff := diffHookEntries(ht.name, ht.current, ht.expected)
		lines = append(lines, typeDiff...)
	}

	return lines
}

// diffHookEntries compares entries for a single hook type.
func diffHookEntries(hookType string, current, expected []hooks.HookEntry) []string {
	var lines []string

	// Build matcher-indexed map for expected entries
	expectedByMatcher := indexByMatcher(expected)

	// Track processed matchers
	processed := make(map[string]bool)

	// Check for modifications and removals
	for _, entry := range current {
		key := entry.Matcher
		processed[key] = true

		expectedEntry, exists := expectedByMatcher[key]
		if !exists {
			// Entry removed
			matcherLabel := matcherDisplay(key)
			lines = append(lines, fmt.Sprintf("  %s: %s\n",
				hookType,
				diffRemove.Render(fmt.Sprintf("-1 hook (matcher %s)", matcherLabel))))
			for _, h := range entry.Hooks {
				lines = append(lines, fmt.Sprintf("    %s\n", diffRemove.Render("- "+h.Command)))
			}
			continue
		}

		// Compare commands within the entry
		cmdDiff := diffCommands(hookType, key, entry, expectedEntry)
		lines = append(lines, cmdDiff...)
	}

	// Check for additions
	for _, entry := range expected {
		key := entry.Matcher
		if processed[key] {
			continue
		}

		matcherLabel := matcherDisplay(key)
		lines = append(lines, fmt.Sprintf("  %s: %s\n",
			hookType,
			diffAdd.Render(fmt.Sprintf("+1 hook (new matcher %s)", matcherLabel))))
		for _, h := range entry.Hooks {
			lines = append(lines, fmt.Sprintf("    %s\n", diffAdd.Render("+ "+h.Command)))
		}
	}

	return lines
}

// diffCommands compares commands within matched entries.
func diffCommands(hookType, matcher string, current, expected hooks.HookEntry) []string {
	var lines []string

	// Compare hooks by index
	maxLen := len(current.Hooks)
	if len(expected.Hooks) > maxLen {
		maxLen = len(expected.Hooks)
	}

	matcherSuffix := ""
	if matcher != "" {
		matcherSuffix = fmt.Sprintf("[%s]", matcher)
	}

	for i := 0; i < maxLen; i++ {
		if i >= len(current.Hooks) {
			// New hook added
			lines = append(lines, fmt.Sprintf("  %s%s.hooks[%d].command:\n", hookType, matcherSuffix, i))
			lines = append(lines, fmt.Sprintf("    %s\n", diffAdd.Render("+ "+expected.Hooks[i].Command)))
			continue
		}
		if i >= len(expected.Hooks) {
			// Hook removed
			lines = append(lines, fmt.Sprintf("  %s%s.hooks[%d].command:\n", hookType, matcherSuffix, i))
			lines = append(lines, fmt.Sprintf("    %s\n", diffRemove.Render("- "+current.Hooks[i].Command)))
			continue
		}

		// Both exist - compare
		if current.Hooks[i].Command != expected.Hooks[i].Command {
			lines = append(lines, fmt.Sprintf("  %s%s.hooks[%d].command:\n", hookType, matcherSuffix, i))
			lines = append(lines, fmt.Sprintf("    %s\n", diffRemove.Render("- "+truncateCommand(current.Hooks[i].Command))))
			lines = append(lines, fmt.Sprintf("    %s\n", diffAdd.Render("+ "+truncateCommand(expected.Hooks[i].Command))))
		}
	}

	return lines
}

// indexByMatcher builds a map from matcher string to HookEntry.
func indexByMatcher(entries []hooks.HookEntry) map[string]hooks.HookEntry {
	m := make(map[string]hooks.HookEntry)
	for _, e := range entries {
		m[e.Matcher] = e
	}
	return m
}

// matcherDisplay returns a display label for a matcher.
func matcherDisplay(matcher string) string {
	if matcher == "" {
		return `"" (all)`
	}
	return fmt.Sprintf("%q", matcher)
}

// truncateCommand truncates long commands for display, keeping the start and end visible.
func truncateCommand(cmd string) string {
	if len(cmd) <= 80 {
		return cmd
	}
	return cmd[:37] + "..." + cmd[len(cmd)-37:]
}
