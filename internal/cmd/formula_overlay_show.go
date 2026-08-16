package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/formula"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var formulaOverlayShowCmd = &cobra.Command{
	Use:   "show <formula>",
	Short: "Show active overlay for a formula",
	Long: `Display the resolved overlay content for a formula with source annotation.

Shows which file provides the overlay (town-level or rig-level) and its contents.

Examples:
  gt formula overlay show mol-polecat-work
  gt formula overlay show mol-polecat-work --rig gastown`,
	Args: cobra.ExactArgs(1),
	RunE: runFormulaOverlayShow,
}

var formulaOverlayShowRig string

func init() {
	formulaOverlayCmd.AddCommand(formulaOverlayShowCmd)
	formulaOverlayShowCmd.Flags().StringVar(&formulaOverlayShowRig, "rig", "", "Rig name (default: auto-detect from cwd)")
}

func runFormulaOverlayShow(cmd *cobra.Command, args []string) error {
	formulaName := args[0]

	townRoot, rigName, err := resolveOverlayContext(formulaOverlayShowRig)
	if err != nil {
		return err
	}

	townPath := filepath.Join(townRoot, "formula-overlays", formulaName+".toml")
	rigPath := ""
	if rigName != "" {
		rigPath = filepath.Join(townRoot, rigName, "formula-overlays", formulaName+".toml")
	}

	// Load and validate overlay
	overlay, err := formula.LoadFormulaOverlay(formulaName, townRoot, rigName)
	if err != nil {
		return fmt.Errorf("loading overlay: %w", err)
	}

	if overlay == nil {
		fmt.Printf("No overlay found for formula %q\n", formulaName)
		fmt.Printf("  Checked: %s\n", townPath)
		if rigPath != "" {
			fmt.Printf("  Checked: %s\n", rigPath)
		}
		fmt.Println("\nUse 'gt formula overlay edit' to create one.")
		return nil
	}

	// Determine which file was used (rig takes precedence)
	source := townPath
	sourceLabel := "town"
	if rigPath != "" {
		if _, err := os.Stat(rigPath); err == nil {
			source = rigPath
			sourceLabel = "rig:" + rigName
		}
	}

	fmt.Printf("# Overlay: %s (%s)\n", formulaName, sourceLabel)
	fmt.Printf("# Source: %s\n", source)
	fmt.Printf("# Step overrides: %d\n", len(overlay.StepOverrides))
	fmt.Println()

	// Print the raw TOML file content
	data, err := os.ReadFile(source) //nolint:gosec // G304: path from trusted overlay directory
	if err != nil {
		return fmt.Errorf("reading overlay file: %w", err)
	}
	fmt.Print(string(data))
	if !strings.HasSuffix(string(data), "\n") {
		fmt.Println()
	}

	return nil
}

// resolveOverlayContext finds the town root and rig name for overlay commands.
func resolveOverlayContext(explicitRig string) (townRoot, rigName string, err error) {
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
