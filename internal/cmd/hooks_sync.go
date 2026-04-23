package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/hooks"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var hooksSyncDryRun bool

var hooksSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Regenerate all agent hook/settings files",
	Long: `Regenerate hook and settings files for all agents across the workspace.

For Claude agents (settings.json merge):
1. Load base config
2. Apply role override (if exists)
3. Apply rig+role override (if exists)
4. Merge hooks section into existing settings.json (preserving all fields)
5. Write updated settings.json

For template-based agents (OpenCode, Gemini, Copilot, etc.):
1. Resolve the agent configured for each role
2. Compare deployed hook file against current template
3. Overwrite if content differs

Examples:
  gt hooks sync             # Regenerate all hook/settings files
  gt hooks sync --dry-run   # Show what would change without writing`,
	RunE: runHooksSync,
}

func init() {
	hooksCmd.AddCommand(hooksSyncCmd)
	hooksSyncCmd.Flags().BoolVar(&hooksSyncDryRun, "dry-run", false, "Show what would change without writing")
}

func runHooksSync(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	targets, err := hooks.DiscoverTargets(townRoot)
	if err != nil {
		return fmt.Errorf("discovering targets: %w", err)
	}

	if hooksSyncDryRun {
		fmt.Println("Dry run - showing what would change...")
		fmt.Println()
	} else {
		fmt.Println("Syncing hooks...")
	}

	updated := 0
	unchanged := 0
	created := 0
	errors := 0
	integrityErrors := 0
	var failedTargets []string

	for _, target := range targets {
		result, err := syncTarget(target, hooksSyncDryRun)
		if err != nil {
			label := "sync error"
			if hooks.IsSettingsIntegrityError(err) {
				label = "integrity violation"
				integrityErrors++
			}
			fmt.Printf(
				"  %s %s (%s): %v\n",
				style.Error.Render("✖"),
				target.DisplayKey(),
				label,
				err,
			)
			errors++
			failedTargets = append(failedTargets, target.DisplayKey())
			continue
		}

		relPath, pathErr := filepath.Rel(townRoot, target.Path)
		if pathErr != nil {
			relPath = target.Path
		}

		switch result {
		case syncCreated:
			if hooksSyncDryRun {
				fmt.Printf("  %s %s %s\n", style.Warning.Render("~"), relPath, style.Dim.Render("(would create)"))
			} else {
				fmt.Printf("  %s %s %s\n", style.Success.Render("✓"), relPath, style.Dim.Render("(created)"))
			}
			created++
		case syncUpdated:
			if hooksSyncDryRun {
				fmt.Printf("  %s %s %s\n", style.Warning.Render("~"), relPath, style.Dim.Render("(would update)"))
			} else {
				fmt.Printf("  %s %s %s\n", style.Success.Render("✓"), relPath, style.Dim.Render("(updated)"))
			}
			updated++
		case syncUnchanged:
			fmt.Printf("  %s %s %s\n", style.Dim.Render("·"), relPath, style.Dim.Render("(unchanged)"))
			unchanged++
		}
	}

	// Sync template-based (non-Claude) agents at each role location.
	// These agents use SyncForRole (content-aware comparison) instead of the
	// JSON merge path used for Claude targets above.
	locations, locErr := hooks.DiscoverRoleLocations(townRoot)
	if locErr != nil {
		fmt.Printf("  %s discovering role locations: %v\n", style.Error.Render("✖"), locErr)
		errors++
	} else {
		for _, loc := range locations {
			rigPath := ""
			if loc.Rig != "" {
				rigPath = filepath.Join(townRoot, loc.Rig)
			}

			// Use ResolveRoleAgentName (not ResolveRoleAgentConfig) so that hooks are
			// installed based on the *configured* agent, not the *resolved* one.
			// ResolveRoleAgentConfig falls back to claude when the agent binary is not
			// found in PATH (e.g., in CI or on a fresh machine), which would silently
			// skip creating opencode/gemini/etc. plugin files.
			agentName, _ := config.ResolveRoleAgentName(loc.Role, townRoot, rigPath)
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

			// Claude targets are already handled by DiscoverTargets + syncTarget above.
			if hooksProvider == "claude" {
				continue
			}

			useSettingsDir := preset.HooksUseSettingsDir

			// Determine sync targets.
			// - Town-level roles (mayor, deacon): the role dir IS the working directory.
			// - Rig roles with useSettingsDir: one shared file in the role parent.
			// - Rig roles without useSettingsDir (OpenCode, etc.): need files in each
			//   individual worktree subdirectory.
			var syncDirs []string
			if loc.Rig == "" || useSettingsDir {
				syncDirs = []string{loc.Dir}
			} else {
				syncDirs = hooks.DiscoverWorktrees(loc.Dir)
			}

			for _, dir := range syncDirs {
				targetPath := filepath.Join(dir, preset.HooksDir, preset.HooksSettingsFile)
				relPath, pathErr := filepath.Rel(townRoot, targetPath)
				if pathErr != nil {
					relPath = targetPath
				}

				if hooksSyncDryRun {
					if _, statErr := os.Stat(targetPath); statErr == nil {
						fmt.Printf("  %s %s %s\n", style.Warning.Render("~"), relPath, style.Dim.Render("(would check "+hooksProvider+")"))
					} else {
						fmt.Printf("  %s %s %s\n", style.Warning.Render("~"), relPath, style.Dim.Render("(would create "+hooksProvider+")"))
						created++
					}
					continue
				}

				result, syncErr := hooks.SyncForRole(hooksProvider, dir, dir, loc.Role,
					preset.HooksDir, preset.HooksSettingsFile, useSettingsDir)
				if syncErr != nil {
					fmt.Printf("  %s %s (%s): %v\n", style.Error.Render("✖"), relPath, hooksProvider, syncErr)
					errors++
					failedTargets = append(failedTargets, relPath)
					continue
				}

				switch result {
				case hooks.SyncCreated:
					fmt.Printf("  %s %s %s\n", style.Success.Render("✓"), relPath, style.Dim.Render("(created "+hooksProvider+")"))
					created++
				case hooks.SyncUpdated:
					fmt.Printf("  %s %s %s\n", style.Success.Render("✓"), relPath, style.Dim.Render("(updated "+hooksProvider+")"))
					updated++
				case hooks.SyncUnchanged:
					fmt.Printf("  %s %s %s\n", style.Dim.Render("·"), relPath, style.Dim.Render("(unchanged "+hooksProvider+")"))
					unchanged++
				}
			}
		}
	}

	// Summary
	fmt.Println()
	total := updated + unchanged + created + errors
	if hooksSyncDryRun {
		fmt.Printf("Would sync %d targets (%d to create, %d to update, %d unchanged",
			total, created, updated, unchanged)
	} else {
		fmt.Printf("Synced %d targets (%d created, %d updated, %d unchanged",
			total, created, updated, unchanged)
	}
	if errors > 0 {
		fmt.Printf(", %s", style.Error.Render(fmt.Sprintf("%d errors", errors)))
	}
	fmt.Println(")")

	if errors > 0 {
		if integrityErrors > 0 {
			return fmt.Errorf(
				"hooks sync failed closed: %d integrity violation(s) across %s",
				integrityErrors,
				strings.Join(failedTargets, ", "),
			)
		}
		return fmt.Errorf(
			"hooks sync failed: %d target(s) failed (%s)",
			errors,
			strings.Join(failedTargets, ", "),
		)
	}

	return nil
}

type syncResult int

const (
	syncUnchanged syncResult = iota
	syncUpdated
	syncCreated
)

// syncTarget syncs a single target's .claude/settings.json.
// Uses MarshalSettings/UnmarshalSettings to preserve unknown fields.
func syncTarget(target hooks.Target, dryRun bool) (syncResult, error) {
	// Compute expected hooks for this target
	expected, err := hooks.ComputeExpected(target.Key)
	if err != nil {
		return 0, fmt.Errorf("computing expected config: %w", err)
	}

	// Load existing settings (returns zero-value if file doesn't exist)
	current, err := hooks.LoadSettings(target.Path)
	if err != nil {
		return 0, fmt.Errorf("loading current settings: %w", err)
	}

	// Check if the file exists
	_, statErr := os.Stat(target.Path)
	fileExists := statErr == nil

	// Compare hooks sections and the Claude startup defaults. Existing settings
	// from older versions may have current hooks but still miss prompt defaults.
	if fileExists && hooks.HooksEqual(expected, &current.Hooks) && hooks.HasClaudePromptDefaults(current) {
		return syncUnchanged, nil
	}

	if dryRun {
		if fileExists {
			return syncUpdated, nil
		}
		return syncCreated, nil
	}

	// Update hooks section, preserving all other fields (including unknown ones)
	current.Hooks = *expected

	// Ensure enabledPlugins map exists with beads disabled (Gas Town standard)
	if current.EnabledPlugins == nil {
		current.EnabledPlugins = make(map[string]bool)
	}
	current.EnabledPlugins["beads@beads-marketplace"] = false

	// Inject default MCP servers (mempalace) if not already configured
	if defaultMCP := hooks.DefaultMCPServers(); defaultMCP != nil {
		if current.MCPServers == nil {
			current.MCPServers = make(map[string]hooks.MCPServer)
		}
		for name, server := range defaultMCP {
			if _, exists := current.MCPServers[name]; !exists {
				current.MCPServers[name] = server
			}
		}
	}

	// Create .claude directory if needed
	claudeDir := filepath.Dir(target.Path)
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return 0, fmt.Errorf("creating .claude directory: %w", err)
	}

	// Write settings.json using MarshalSettings to preserve unknown fields
	data, err := hooks.MarshalSettings(current)
	if err != nil {
		return 0, fmt.Errorf("marshaling settings: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(target.Path, data, 0644); err != nil {
		return 0, fmt.Errorf("writing settings: %w", err)
	}

	if fileExists {
		return syncUpdated, nil
	}
	return syncCreated, nil
}
