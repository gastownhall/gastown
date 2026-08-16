package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var formulaOverlayListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all overlay files",
	Long: `List all formula overlay files across town and rig levels.

Shows each overlay file with its scope (town or rig) and formula name.

Examples:
  gt formula overlay list`,
	RunE: runFormulaOverlayList,
}

func init() {
	formulaOverlayCmd.AddCommand(formulaOverlayListCmd)
}

func runFormulaOverlayList(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	type entry struct {
		scope   string // "town" or rig name
		formula string
		path    string
	}

	var entries []entry

	// Town-level overlays
	townDir := filepath.Join(townRoot, "formula-overlays")
	if files, err := os.ReadDir(townDir); err == nil {
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".toml") {
				continue
			}
			name := strings.TrimSuffix(f.Name(), ".toml")
			path := filepath.Join(townDir, f.Name())
			entries = append(entries, entry{scope: "town", formula: name, path: path})
		}
	}

	// Rig-level overlays — scan each rig directory
	rigDirs, err := os.ReadDir(townRoot)
	if err == nil {
		for _, d := range rigDirs {
			if !d.IsDir() {
				continue
			}
			rigName := d.Name()
			rigConfigPath := filepath.Join(townRoot, rigName, "config.json")
			if _, err := os.Stat(rigConfigPath); err != nil {
				continue
			}

			rigDir := filepath.Join(townRoot, rigName, "formula-overlays")
			files, err := os.ReadDir(rigDir)
			if err != nil {
				continue
			}
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".toml") {
					continue
				}
				name := strings.TrimSuffix(f.Name(), ".toml")
				path := filepath.Join(rigDir, f.Name())
				entries = append(entries, entry{scope: rigName, formula: name, path: path})
			}
		}
	}

	if len(entries) == 0 {
		fmt.Println("No overlay files found.")
		fmt.Println("\nUse 'gt formula overlay edit <formula>' to create one.")
		return nil
	}

	fmt.Printf("Formula overlay files (%d):\n\n", len(entries))
	fmt.Printf("  %-10s %-30s %s\n", "SCOPE", "FORMULA", "PATH")
	fmt.Printf("  %-10s %-30s %s\n", "─────", "───────", "────")
	for _, e := range entries {
		fmt.Printf("  %-10s %-30s %s\n", e.scope, e.formula, e.path)
	}

	return nil
}
