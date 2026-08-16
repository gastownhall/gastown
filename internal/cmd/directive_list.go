package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var directiveListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all directive files",
	Long: `List all directive files across town and rig levels.

Shows each directive file with its scope (town or rig) and role.

Examples:
  gt directive list`,
	RunE: runDirectiveList,
}

func init() {
	directiveCmd.AddCommand(directiveListCmd)
}

func runDirectiveList(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	type entry struct {
		scope string // "town" or rig name
		role  string
		path  string
	}

	var entries []entry

	// Town-level directives
	townDir := filepath.Join(townRoot, "directives")
	if files, err := os.ReadDir(townDir); err == nil {
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
				continue
			}
			role := strings.TrimSuffix(f.Name(), ".md")
			path := filepath.Join(townDir, f.Name())
			if fileHasContent(path) {
				entries = append(entries, entry{scope: "town", role: role, path: path})
			}
		}
	}

	// Rig-level directives — scan each rig directory
	rigDirs, err := os.ReadDir(townRoot)
	if err == nil {
		for _, d := range rigDirs {
			if !d.IsDir() {
				continue
			}
			rigName := d.Name()
			// Skip non-rig directories
			rigConfigPath := filepath.Join(townRoot, rigName, "config.json")
			if _, err := os.Stat(rigConfigPath); err != nil {
				continue
			}

			rigDir := filepath.Join(townRoot, rigName, "directives")
			files, err := os.ReadDir(rigDir)
			if err != nil {
				continue
			}
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
					continue
				}
				role := strings.TrimSuffix(f.Name(), ".md")
				path := filepath.Join(rigDir, f.Name())
				if fileHasContent(path) {
					entries = append(entries, entry{scope: rigName, role: role, path: path})
				}
			}
		}
	}

	if len(entries) == 0 {
		fmt.Println("No directive files found.")
		fmt.Println("\nUse 'gt directive edit <role>' to create one.")
		return nil
	}

	fmt.Printf("Directive files (%d):\n\n", len(entries))
	fmt.Printf("  %-10s %-12s %s\n", "SCOPE", "ROLE", "PATH")
	fmt.Printf("  %-10s %-12s %s\n", "─────", "────", "────")
	for _, e := range entries {
		fmt.Printf("  %-10s %-12s %s\n", e.scope, e.role, e.path)
	}

	return nil
}
