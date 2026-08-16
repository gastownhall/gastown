package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/polecat"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	namepoolListFlag     bool
	namepoolThemeFlag    string
	namepoolFromFileFlag string
)

var namepoolCmd = &cobra.Command{
	Use:     "namepool",
	GroupID: GroupWorkspace,
	Short:   "Manage polecat name pools",
	Long: `Manage themed name pools for polecats in Gas Town.

By default, polecats get themed names from the Mad Max universe
(furiosa, nux, slit, etc.). You can change the theme or add custom names.

Examples:
  gt namepool              # Show current pool status
  gt namepool --list       # List available themes
  gt namepool themes       # Show theme names
  gt namepool set minerals # Set theme to 'minerals'
  gt namepool add ember    # Add custom name to pool
  gt namepool reset        # Reset pool state`,
	RunE: runNamepool,
}

var namepoolThemesCmd = &cobra.Command{
	Use:   "themes [theme]",
	Short: "List available themes and their names",
	Long: `List available namepool themes or show names in a specific theme.

Without arguments, lists all themes with a preview of their names.
With a theme name argument, shows all names in that theme.`,
	RunE: runNamepoolThemes,
}

var namepoolSetCmd = &cobra.Command{
	Use:   "set <theme>",
	Short: "Set the namepool theme for this rig",
	Long: `Set the namepool theme used for naming new polecats in this rig.

Changes the theme and saves it to the rig settings. Existing polecat
names are not affected. Use 'gt namepool themes' to see available themes.`,
	Args: cobra.ExactArgs(1),
	RunE: runNamepoolSet,
}

var namepoolAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add a custom name to the pool",
	Long: `Add a custom name to the rig's polecat name pool.

The name is appended to the pool and saved in the rig settings.
Duplicate names are silently ignored.`,
	Args: cobra.ExactArgs(1),
	RunE: runNamepoolAdd,
}

var namepoolResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Reset the pool state (release all names)",
	Long: `Reset the polecat name pool, releasing all claimed names.

All names become available for reuse. This does not change the theme
or remove custom names from the configuration.`,
	RunE: runNamepoolReset,
}

var namepoolCreateCmd = &cobra.Command{
	Use:   "create <name> [names...]",
	Short: "Create a custom theme",
	Long: `Create a custom namepool theme stored as a text file.

The theme is saved to <town>/settings/themes/<name>.txt and can be
used with 'gt namepool set <name>'. Names can be provided as arguments
or read from a file with --from-file.

Examples:
  gt namepool create tolkien aragorn legolas gimli gandalf frodo samwise
  gt namepool create tolkien --from-file ~/tolkien-names.txt`,
	Args: cobra.MinimumNArgs(1),
	RunE: runNamepoolCreate,
}

var namepoolDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a custom theme",
	Long: `Delete a custom namepool theme file.

Built-in themes cannot be deleted. If a rig is currently using the
theme, a warning is shown but deletion proceeds.`,
	Args: cobra.ExactArgs(1),
	RunE: runNamepoolDelete,
}

func init() {
	rootCmd.AddCommand(namepoolCmd)
	namepoolCmd.AddCommand(namepoolThemesCmd)
	namepoolCmd.AddCommand(namepoolSetCmd)
	namepoolCmd.AddCommand(namepoolAddCmd)
	namepoolCmd.AddCommand(namepoolResetCmd)
	namepoolCmd.AddCommand(namepoolCreateCmd)
	namepoolCmd.AddCommand(namepoolDeleteCmd)
	namepoolCmd.Flags().BoolVarP(&namepoolListFlag, "list", "l", false, "List available themes")
	namepoolCreateCmd.Flags().StringVar(&namepoolFromFileFlag, "from-file", "", "Read names from file instead of arguments")
}

func runNamepool(cmd *cobra.Command, args []string) error {
	// List themes mode
	if namepoolListFlag {
		return runNamepoolThemes(cmd, nil)
	}

	// Show current pool status
	rigName, rigPath := detectCurrentRigWithPath()
	if rigName == "" {
		return fmt.Errorf("not in a rig directory")
	}

	// Load settings for namepool config
	settingsPath := filepath.Join(rigPath, "settings", "config.json")
	var pool *polecat.NamePool

	settings, err := config.LoadRigSettings(settingsPath)
	if err == nil && settings.Namepool != nil {
		// Use configured namepool settings
		pool = polecat.NewNamePoolWithConfig(
			rigPath,
			rigName,
			settings.Namepool.Style,
			settings.Namepool.Names,
			settings.Namepool.MaxBeforeNumbering,
		)
	} else {
		// Use defaults
		pool = polecat.NewNamePool(rigPath, rigName)
	}

	if err := pool.Load(); err != nil {
		// Pool doesn't exist yet, show defaults
		fmt.Printf("Rig: %s\n", rigName)
		fmt.Printf("Theme: %s (default)\n", polecat.DefaultTheme)
		fmt.Printf("Polecats: 0\n")
		fmt.Printf("Max pool size: %d\n", polecat.DefaultPoolSize)
		return nil
	}

	// Show pool status
	fmt.Printf("Rig: %s\n", rigName)
	theme := pool.GetTheme()
	if polecat.IsBuiltinTheme(theme) {
		fmt.Printf("Theme: %s (built-in)\n", theme)
	} else {
		fmt.Printf("Theme: %s (custom)\n", theme)
	}
	fmt.Printf("Polecats: %d\n", pool.ActiveCount())

	activeNames := pool.ActiveNames()
	if len(activeNames) > 0 {
		fmt.Printf("In use: %s\n", strings.Join(activeNames, ", "))
	}

	// Check if configured (already loaded above)
	if settings.Namepool != nil {
		fmt.Printf("(configured in settings/config.json)\n")
	}

	return nil
}

func runNamepoolThemes(cmd *cobra.Command, args []string) error {
	// Find town root for custom theme discovery
	townRoot, _ := workspace.FindFromCwd()

	if len(args) == 0 {
		// List all themes (built-in + custom)
		themes := polecat.ListAllThemes(townRoot)
		fmt.Println("Available themes:")
		for _, t := range themes {
			label := ""
			if t.IsCustom {
				label = "custom, "
			}
			fmt.Printf("\n  %s (%s%d names):\n", t.Name, label, t.Count)
			// Show name preview
			var names []string
			if t.IsCustom && townRoot != "" {
				names, _ = polecat.ResolveThemeNames(townRoot, t.Name)
			} else {
				names, _ = polecat.GetThemeNames(t.Name)
			}
			if len(names) > 0 {
				preview := names
				if len(preview) > 10 {
					preview = preview[:10]
				}
				fmt.Printf("    %s...\n", strings.Join(preview, ", "))
			}
		}
		return nil
	}

	// Show specific theme names
	theme := args[0]
	var names []string
	var err error
	if townRoot != "" {
		names, err = polecat.ResolveThemeNames(townRoot, theme)
	} else {
		names, err = polecat.GetThemeNames(theme)
	}
	if err != nil {
		return fmt.Errorf("unknown theme: %s (use 'gt namepool themes' to list available themes)", theme)
	}

	label := ""
	if !polecat.IsBuiltinTheme(theme) {
		label = " (custom)"
	}
	fmt.Printf("Theme: %s%s (%d names)\n\n", theme, label, len(names))
	for i, name := range names {
		if i > 0 && i%5 == 0 {
			fmt.Println()
		}
		fmt.Printf("  %-12s", name)
	}
	fmt.Println()

	return nil
}

func runNamepoolSet(cmd *cobra.Command, args []string) error {
	theme := args[0]

	// Get rig
	rigName, rigPath := detectCurrentRigWithPath()
	if rigName == "" {
		return fmt.Errorf("not in a rig directory")
	}

	// Find town root for custom theme resolution
	townRoot, _ := workspace.FindFromCwd()

	// Validate theme: check built-in first, then custom
	if _, err := polecat.ResolveThemeNames(townRoot, theme); err != nil {
		return fmt.Errorf("unknown theme: %s (use 'gt namepool themes' to list available themes)", theme)
	}

	// Update pool
	pool := polecat.NewNamePool(rigPath, rigName)
	if townRoot != "" {
		pool.SetTownRoot(townRoot)
	}
	if err := pool.Load(); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("loading pool: %w", err)
	}

	if err := pool.SetTheme(theme); err != nil {
		return err
	}

	if err := pool.Save(); err != nil {
		return fmt.Errorf("saving pool: %w", err)
	}

	// Load existing settings to preserve custom names when changing theme
	settingsPath := filepath.Join(rigPath, "settings", "config.json")
	var existingNames []string
	if existingSettings, err := config.LoadRigSettings(settingsPath); err == nil {
		if existingSettings.Namepool != nil {
			existingNames = existingSettings.Namepool.Names
		}
	}

	// Also save to rig config, preserving existing custom names
	if err := saveRigNamepoolConfig(rigPath, theme, existingNames); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	fmt.Printf("Theme '%s' set for rig '%s'\n", theme, rigName)
	fmt.Printf("New polecats will use names from this theme.\n")

	return nil
}

func runNamepoolAdd(cmd *cobra.Command, args []string) error {
	name := strings.ToLower(args[0])

	// Validate name
	if err := polecat.ValidatePoolName(name); err != nil {
		return err
	}

	rigName, rigPath := detectCurrentRigWithPath()
	if rigName == "" {
		return fmt.Errorf("not in a rig directory")
	}

	// Load existing rig settings to get current theme and custom names
	settingsPath := filepath.Join(rigPath, "settings", "config.json")
	settings, err := config.LoadRigSettings(settingsPath)
	if err != nil {
		if os.IsNotExist(err) || strings.Contains(err.Error(), "not found") {
			settings = config.NewRigSettings()
		} else {
			return fmt.Errorf("loading settings: %w", err)
		}
	}

	// Initialize namepool config if needed
	if settings.Namepool == nil {
		settings.Namepool = config.DefaultNamepoolConfig()
	}

	// If the rig uses a custom theme (not built-in) and has no per-rig name
	// overrides, append to the theme file instead of the rig config.
	// This prevents a single `add` from shadowing the entire custom theme.
	style := settings.Namepool.Style
	if style != "" && !polecat.IsBuiltinTheme(style) && len(settings.Namepool.Names) == 0 {
		townRoot, _ := workspace.FindFromCwd()
		if townRoot != "" {
			alreadyExists, err := polecat.AppendToCustomTheme(townRoot, style, name)
			if err != nil {
				return fmt.Errorf("appending to custom theme %q: %w", style, err)
			}
			if alreadyExists {
				fmt.Printf("Name '%s' already in theme '%s'\n", name, style)
			} else {
				fmt.Printf("Added '%s' to custom theme '%s'\n", name, style)
			}
			return nil
		}
	}

	// Built-in theme or per-rig override: add to rig config as before
	for _, n := range settings.Namepool.Names {
		if n == name {
			fmt.Printf("Name '%s' already in pool\n", name)
			return nil
		}
	}

	// Append new name to existing custom names
	settings.Namepool.Names = append(settings.Namepool.Names, name)

	// Save to settings/config.json (the source of truth for config)
	if err := config.SaveRigSettings(settingsPath, settings); err != nil {
		return fmt.Errorf("saving settings: %w", err)
	}

	fmt.Printf("Added '%s' to the name pool\n", name)
	return nil
}

func runNamepoolReset(cmd *cobra.Command, args []string) error {
	rigName, rigPath := detectCurrentRigWithPath()
	if rigName == "" {
		return fmt.Errorf("not in a rig directory")
	}

	// Load pool
	pool := polecat.NewNamePool(rigPath, rigName)
	if err := pool.Load(); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("loading pool: %w", err)
	}

	pool.Reset()

	if err := pool.Save(); err != nil {
		return fmt.Errorf("saving pool: %w", err)
	}

	fmt.Printf("Pool reset for rig '%s'\n", rigName)
	fmt.Printf("All names released and available for reuse.\n")
	return nil
}

// detectCurrentRigWithPath determines the rig name and path from cwd.
func detectCurrentRigWithPath() (string, string) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", ""
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return "", ""
	}

	// Get path relative to town root
	rel, err := filepath.Rel(townRoot, cwd)
	if err != nil {
		return "", ""
	}

	// Extract first path component (rig name)
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) > 0 && parts[0] != "." && parts[0] != constants.RoleMayor && parts[0] != constants.RoleDeacon {
		return parts[0], filepath.Join(townRoot, parts[0])
	}

	return "", ""
}

func runNamepoolCreate(cmd *cobra.Command, args []string) error {
	themeName := args[0]

	// Validate theme name
	if polecat.IsBuiltinTheme(themeName) {
		return fmt.Errorf("cannot create custom theme %q: conflicts with built-in theme", themeName)
	}
	if err := polecat.ValidatePoolName(themeName); err != nil {
		return fmt.Errorf("invalid theme name: %w", err)
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	var names []string
	if namepoolFromFileFlag != "" {
		// Read from file
		names, err = polecat.ParseThemeFile(namepoolFromFileFlag)
		if err != nil {
			return fmt.Errorf("reading names from file: %w", err)
		}
	} else {
		// Read from arguments
		if len(args) < 2 {
			return fmt.Errorf("provide names as arguments or use --from-file")
		}
		for _, name := range args[1:] {
			name = strings.ToLower(name)
			if err := polecat.ValidatePoolName(name); err != nil {
				return err
			}
			names = append(names, name)
		}
	}

	if err := polecat.SaveCustomTheme(townRoot, themeName, names); err != nil {
		return err
	}

	fmt.Printf("Created custom theme '%s' with %d names\n", themeName, len(names))
	fmt.Printf("Use 'gt namepool set %s' to activate it for a rig.\n", themeName)
	return nil
}

func runNamepoolDelete(cmd *cobra.Command, args []string) error {
	themeName := args[0]

	// Validate theme name to prevent path traversal (e.g., "../../etc/foo")
	if err := polecat.ValidatePoolName(themeName); err != nil {
		return fmt.Errorf("invalid theme name: %w", err)
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	// Check if any rigs are using this theme and warn
	if using := polecat.FindRigsUsingTheme(townRoot, themeName); len(using) > 0 {
		fmt.Fprintf(os.Stderr, "warning: theme '%s' is currently used by: %s\n", themeName, strings.Join(using, ", "))
		fmt.Fprintf(os.Stderr, "  Those rigs will fall back to the default theme (%s).\n", polecat.DefaultTheme)
	}

	if err := polecat.DeleteCustomTheme(townRoot, themeName); err != nil {
		return err
	}

	fmt.Printf("Deleted custom theme '%s'\n", themeName)
	return nil
}

// saveRigNamepoolConfig saves the namepool config to rig settings.
func saveRigNamepoolConfig(rigPath, theme string, customNames []string) error {
	settingsPath := filepath.Join(rigPath, "settings", "config.json")

	// Load existing settings or create new
	var settings *config.RigSettings
	settings, err := config.LoadRigSettings(settingsPath)
	if err != nil {
		// Create new settings if not found
		if os.IsNotExist(err) || strings.Contains(err.Error(), "not found") {
			settings = config.NewRigSettings()
		} else {
			return fmt.Errorf("loading settings: %w", err)
		}
	}

	// Set namepool
	settings.Namepool = &config.NamepoolConfig{
		Style: theme,
		Names: customNames,
	}

	// Save (creates directory if needed)
	if err := config.SaveRigSettings(settingsPath, settings); err != nil {
		return fmt.Errorf("saving settings: %w", err)
	}

	return nil
}
