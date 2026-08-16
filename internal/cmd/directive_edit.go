package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/spf13/cobra"
)

var directiveEditCmd = &cobra.Command{
	Use:   "edit <role>",
	Short: "Edit directive for a role",
	Long: `Open the directive file for a role in $EDITOR.

Creates the directory and file if they do not exist. By default, edits the
rig-level directive (if a rig is detected) or the town-level directive.

Use --town to explicitly edit the town-level directive.

Examples:
  gt directive edit polecat             # Edit rig-level polecat directive
  gt directive edit crew --rig sky      # Edit sky rig crew directive
  gt directive edit witness --town      # Edit town-level witness directive`,
	Args: cobra.ExactArgs(1),
	RunE: runDirectiveEdit,
}

var (
	directiveEditRig  string
	directiveEditTown bool
)

func init() {
	directiveCmd.AddCommand(directiveEditCmd)
	directiveEditCmd.Flags().StringVar(&directiveEditRig, "rig", "", "Rig name (default: auto-detect from cwd)")
	directiveEditCmd.Flags().BoolVar(&directiveEditTown, "town", false, "Edit town-level directive instead of rig-level")
}

func runDirectiveEdit(cmd *cobra.Command, args []string) error {
	role := args[0]

	if !isValidRole(role) {
		return fmt.Errorf("unknown role %q — valid roles: %s", role, strings.Join(config.AllRoles(), ", "))
	}

	townRoot, rigName, err := resolveDirectiveContext(directiveEditRig)
	if err != nil {
		return err
	}

	// Determine which file to edit
	var path string
	if directiveEditTown || rigName == "" {
		path = filepath.Join(townRoot, "directives", role+".md")
	} else {
		path = filepath.Join(townRoot, rigName, "directives", role+".md")
	}

	// Create directory and file if needed
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Create with a helpful header comment
		initial := fmt.Sprintf("<!-- Directive for role: %s -->\n<!-- This content is injected at prime time. -->\n\n", role)
		if err := os.WriteFile(path, []byte(initial), 0644); err != nil {
			return fmt.Errorf("creating directive file: %w", err)
		}
		fmt.Printf("Created new directive: %s\n", path)
	}

	// Open in editor
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	editorCmd := exec.Command(editor, path)
	editorCmd.Stdin = os.Stdin
	editorCmd.Stdout = os.Stdout
	editorCmd.Stderr = os.Stderr
	if err := editorCmd.Run(); err != nil {
		return fmt.Errorf("running editor: %w", err)
	}

	fmt.Printf("Directive updated: %s\n", path)
	fmt.Println("Changes take effect at next 'gt prime'.")
	return nil
}
