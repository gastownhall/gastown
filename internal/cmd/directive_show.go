package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var directiveShowCmd = &cobra.Command{
	Use:   "show <role>",
	Short: "Show active directive for a role",
	Long: `Display the resolved directive content for a role with source annotation.

Shows which files contribute to the directive (town-level, rig-level, or both).

Examples:
  gt directive show polecat             # Show polecat directive
  gt directive show witness --rig sky   # Show witness directive for sky rig`,
	Args: cobra.ExactArgs(1),
	RunE: runDirectiveShow,
}

var directiveShowRig string

func init() {
	directiveCmd.AddCommand(directiveShowCmd)
	directiveShowCmd.Flags().StringVar(&directiveShowRig, "rig", "", "Rig name (default: auto-detect from cwd)")
}

func runDirectiveShow(cmd *cobra.Command, args []string) error {
	role := args[0]

	// Validate role
	if !isValidRole(role) {
		return fmt.Errorf("unknown role %q — valid roles: %s", role, strings.Join(config.AllRoles(), ", "))
	}

	townRoot, rigName, err := resolveDirectiveContext(directiveShowRig)
	if err != nil {
		return err
	}

	townPath := filepath.Join(townRoot, "directives", role+".md")
	rigPath := ""
	if rigName != "" {
		rigPath = filepath.Join(townRoot, rigName, "directives", role+".md")
	}

	content := config.LoadRoleDirective(role, townRoot, rigName)
	if content == "" {
		fmt.Printf("No directive found for role %q\n", role)
		fmt.Printf("  Checked: %s\n", townPath)
		if rigPath != "" {
			fmt.Printf("  Checked: %s\n", rigPath)
		}
		fmt.Println("\nUse 'gt directive edit' to create one.")
		return nil
	}

	// Determine sources
	hasTown := fileHasContent(townPath)
	hasRig := rigPath != "" && fileHasContent(rigPath)

	// Print source annotation
	switch {
	case hasTown && hasRig:
		fmt.Printf("# Directive: %s (town + rig:%s)\n", role, rigName)
		fmt.Printf("# Town: %s\n", townPath)
		fmt.Printf("# Rig:  %s\n", rigPath)
	case hasRig:
		fmt.Printf("# Directive: %s (rig:%s)\n", role, rigName)
		fmt.Printf("# Source: %s\n", rigPath)
	default:
		fmt.Printf("# Directive: %s (town)\n", role)
		fmt.Printf("# Source: %s\n", townPath)
	}
	fmt.Println()
	fmt.Println(content)

	return nil
}

// resolveDirectiveContext finds the town root and rig name for directive commands.
func resolveDirectiveContext(explicitRig string) (townRoot, rigName string, err error) {
	townRoot, err = workspace.FindFromCwdOrError()
	if err != nil {
		return "", "", fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	rigName = explicitRig
	if rigName == "" {
		cwd, err := os.Getwd()
		if err == nil {
			rigName = detectRigFromPath(townRoot, cwd)
		}
	}

	return townRoot, rigName, nil
}

// isValidRole checks if a role name is in the known roles list.
func isValidRole(role string) bool {
	for _, r := range config.AllRoles() {
		if r == role {
			return true
		}
	}
	return false
}

// fileHasContent returns true if the file exists and has non-whitespace content.
func fileHasContent(path string) bool {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path from trusted config
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) != ""
}
