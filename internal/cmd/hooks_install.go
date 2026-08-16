package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/hooks"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	installRole    string
	installAllRigs bool
	installDryRun  bool
	hooksInstForce bool
)

var hooksInstallCmd = &cobra.Command{
	Use:   "install <hook-name>",
	Short: "Install a hook from the registry",
	Long: `Install a hook from the registry to worktrees.

By default, installs to the current worktree. Use --role to install
to all worktrees of a specific role in the current rig.

Examples:
  gt hooks install pr-workflow-guard              # Install to current worktree
  gt hooks install pr-workflow-guard --role crew  # Install to all crew in current rig
  gt hooks install session-prime --role crew --all-rigs  # Install to all crew everywhere
  gt hooks install pr-workflow-guard --dry-run    # Preview what would be installed`,
	Args: cobra.ExactArgs(1),
	RunE: runHooksInstall,
}

func init() {
	hooksCmd.AddCommand(hooksInstallCmd)
	hooksInstallCmd.Flags().StringVar(&installRole, "role", "", "Install to all worktrees of this role (crew, polecat, witness, refinery)")
	hooksInstallCmd.Flags().BoolVar(&installAllRigs, "all-rigs", false, "Install across all rigs (requires --role)")
	hooksInstallCmd.Flags().BoolVar(&installDryRun, "dry-run", false, "Preview changes without writing files")
	hooksInstallCmd.Flags().BoolVar(&hooksInstForce, "force", false, "Install even if hook is disabled in registry")
}

func runHooksInstall(cmd *cobra.Command, args []string) error {
	hookName := args[0]

	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load registry
	registry, err := LoadRegistry(townRoot)
	if err != nil {
		return err
	}

	// Find the hook
	hookDef, ok := registry.Hooks[hookName]
	if !ok {
		return fmt.Errorf("hook %q not found in registry", hookName)
	}

	if !hookDef.Enabled {
		if !hooksInstForce {
			return fmt.Errorf("hook %q is disabled in registry; use --force to install anyway", hookName)
		}
		fmt.Printf("%s Hook %q is disabled in registry, installing with --force.\n",
			style.Warning.Render("Warning:"), hookName)
	}

	// Determine target worktrees
	targets, err := determineTargets(townRoot, installRole, installAllRigs, hookDef.Roles)
	if err != nil {
		return err
	}

	if len(targets) == 0 {
		if installRole != "" {
			return fmt.Errorf("no targets found for role %q in workspace", installRole)
		}
		// No role specified — resolve CWD to the correct settings directory.
		// For shared-parent roles (crew, polecats, witness, refinery), the
		// settings live in the role parent dir, not the individual worktree.
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		targets = []string{resolveSettingsTarget(townRoot, cwd)}
	}

	// Install to each target
	installed := 0
	errors := 0
	integrityErrors := 0
	var failedTargets []string
	for _, target := range targets {
		if err := installHookTo(target, hookDef, installDryRun); err != nil {
			label := "install error"
			if hooks.IsSettingsIntegrityError(err) {
				label = "integrity violation"
				integrityErrors++
			}
			fmt.Printf("%s Failed to install to %s (%s): %v\n", style.Error.Render("Error:"), target, label, err)
			errors++
			failedTargets = append(failedTargets, target)
			continue
		}
		installed++
	}

	if installDryRun {
		fmt.Printf("\n%s Would install %q to %d worktree(s)\n", style.Dim.Render("Dry run:"), hookName, installed)
	} else {
		fmt.Printf("\n%s Installed %q to %d worktree(s)\n", style.Success.Render("Done:"), hookName, installed)
	}

	if errors > 0 {
		if integrityErrors > 0 {
			return fmt.Errorf(
				"hook install failed closed: %d integrity violation(s) (%s)",
				integrityErrors,
				strings.Join(failedTargets, ", "),
			)
		}
		return fmt.Errorf(
			"hook install failed: %d target(s) failed (%s)",
			errors,
			strings.Join(failedTargets, ", "),
		)
	}

	return nil
}

// determineTargets finds all worktree paths matching the role criteria.
func determineTargets(townRoot, role string, allRigs bool, allowedRoles []string) ([]string, error) {
	if role == "" {
		return nil, nil // Will use current directory
	}

	// Check if role is allowed for this hook
	roleAllowed := false
	for _, r := range allowedRoles {
		if r == role {
			roleAllowed = true
			break
		}
	}
	if !roleAllowed {
		return nil, fmt.Errorf("hook is not applicable to role %q (allowed: %s)", role, strings.Join(allowedRoles, ", "))
	}

	var targets []string

	// Find rigs to scan
	var rigs []string
	if allRigs {
		entries, err := os.ReadDir(townRoot)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && e.Name() != "mayor" && e.Name() != "deacon" && e.Name() != "hooks" {
				rigs = append(rigs, e.Name())
			}
		}
	} else {
		// Find current rig from cwd
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		relPath, err := filepath.Rel(townRoot, cwd)
		if err != nil {
			return nil, err
		}
		parts := strings.Split(relPath, string(filepath.Separator))
		if len(parts) > 0 {
			rigs = []string{parts[0]}
		}
	}

	// Find settings directories for the role in each rig.
	// Settings are installed in shared parent directories (not per-worktree),
	// matching the model used by DiscoverTargets and EnsureSettingsForRole.
	for _, rig := range rigs {
		rigPath := filepath.Join(townRoot, rig)

		switch role {
		case constants.RoleCrew:
			crewDir := filepath.Join(rigPath, "crew")
			if info, err := os.Stat(crewDir); err == nil && info.IsDir() {
				targets = append(targets, crewDir)
			}
		case constants.RolePolecat:
			polecatsDir := filepath.Join(rigPath, "polecats")
			if info, err := os.Stat(polecatsDir); err == nil && info.IsDir() {
				targets = append(targets, polecatsDir)
			}
		case constants.RoleWitness:
			witnessDir := filepath.Join(rigPath, "witness")
			if info, err := os.Stat(witnessDir); err == nil && info.IsDir() {
				targets = append(targets, witnessDir)
			}
		case constants.RoleRefinery:
			refineryDir := filepath.Join(rigPath, "refinery")
			if info, err := os.Stat(refineryDir); err == nil && info.IsDir() {
				targets = append(targets, refineryDir)
			}
		}
	}

	return targets, nil
}

// resolveSettingsTarget resolves a working directory to the appropriate settings
// target directory. For shared-parent roles (crew, polecats, witness, refinery),
// this returns the role parent directory rather than the individual worktree,
// matching the shared settings model used by DiscoverTargets and EnsureSettingsForRole.
func resolveSettingsTarget(townRoot, cwd string) string {
	relPath, err := filepath.Rel(townRoot, cwd)
	if err != nil {
		return cwd
	}
	parts := strings.Split(relPath, string(filepath.Separator))
	if len(parts) < 2 {
		return cwd // At town root or top-level dir (mayor/deacon)
	}
	// parts[0] = rig name (or mayor/deacon), parts[1] = role dir
	roleDir := parts[1]
	switch roleDir {
	case "crew", "polecats", "witness", "refinery":
		return filepath.Join(townRoot, parts[0], roleDir)
	default:
		return cwd
	}
}

// installHookTo installs a hook to a specific worktree.
func installHookTo(worktreePath string, hookDef HookDefinition, dryRun bool) error {
	settingsPath := filepath.Join(worktreePath, ".claude", "settings.json")

	// Load existing settings or create new
	settings, err := hooks.LoadSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("loading existing settings: %w", err)
	}

	// Initialize enabledPlugins if needed
	if settings.EnabledPlugins == nil {
		settings.EnabledPlugins = make(map[string]bool)
	}

	// Build and add hook entries for each matcher
	for _, matcher := range hookDef.Matchers {
		entry := hooks.HookEntry{
			Matcher: matcher,
			Hooks: []hooks.Hook{
				{Type: "command", Command: hookDef.Command},
			},
		}
		settings.Hooks.AddEntry(hookDef.Event, entry)
	}

	// Ensure beads plugin is disabled (standard for Gas Town)
	settings.EnabledPlugins["beads@beads-marketplace"] = false

	// Pretty print relative path
	relPath := worktreePath
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, worktreePath); err == nil && !strings.HasPrefix(rel, "..") {
			relPath = "~/" + rel
		}
	}

	if dryRun {
		fmt.Printf("  %s %s\n", style.Dim.Render("Would install to:"), relPath)
		return nil
	}

	// Create directory if needed
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		return fmt.Errorf("creating .claude directory: %w", err)
	}

	// Write settings using MarshalSettings to preserve custom field handling
	// (SettingsJSON uses json:"-" tags, so encoding/json would produce {})
	data, err := hooks.MarshalSettings(settings)
	if err != nil {
		return fmt.Errorf("marshaling settings: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(settingsPath, data, 0644); err != nil {
		return fmt.Errorf("writing settings: %w", err)
	}

	fmt.Printf("  %s %s\n", style.Success.Render("Installed to:"), relPath)
	return nil
}
