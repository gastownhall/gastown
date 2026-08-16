package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jonbaldie/gastown/internal/formula"
	"github.com/spf13/cobra"
)

var formulaOverlayEditCmd = &cobra.Command{
	Use:   "edit <formula>",
	Short: "Edit overlay for a formula",
	Long: `Open the overlay file for a formula in $EDITOR.

Creates the directory and file if they do not exist. By default, edits the
rig-level overlay (if a rig is detected) or the town-level overlay.

Use --town to explicitly edit the town-level overlay.

Examples:
  gt formula overlay edit mol-polecat-work
  gt formula overlay edit mol-polecat-work --rig gastown
  gt formula overlay edit mol-polecat-work --town`,
	Args: cobra.ExactArgs(1),
	RunE: runFormulaOverlayEdit,
}

var (
	formulaOverlayEditRig  string
	formulaOverlayEditTown bool
)

func init() {
	formulaOverlayCmd.AddCommand(formulaOverlayEditCmd)
	formulaOverlayEditCmd.Flags().StringVar(&formulaOverlayEditRig, "rig", "", "Rig name (default: auto-detect from cwd)")
	formulaOverlayEditCmd.Flags().BoolVar(&formulaOverlayEditTown, "town", false, "Edit town-level overlay instead of rig-level")
}

func runFormulaOverlayEdit(cmd *cobra.Command, args []string) error {
	formulaName := args[0]

	townRoot, rigName, err := resolveOverlayContext(formulaOverlayEditRig)
	if err != nil {
		return err
	}

	// Determine which file to edit
	var path string
	if formulaOverlayEditTown || rigName == "" {
		path = filepath.Join(townRoot, "formula-overlays", formulaName+".toml")
	} else {
		path = filepath.Join(townRoot, rigName, "formula-overlays", formulaName+".toml")
	}

	// Create directory and file if needed
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		initial := `# Formula overlay for ` + formulaName + `
# Modes: replace, append, skip
#
# [[step-overrides]]
# step_id = "step-name"
# mode = "append"
# description = """
# Additional instructions for this step.
# """
`
		if err := os.WriteFile(path, []byte(initial), 0644); err != nil {
			return fmt.Errorf("creating overlay file: %w", err)
		}
		fmt.Printf("Created new overlay: %s\n", path)
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

	// Validate after editing
	if _, err := formula.LoadFormulaOverlay(formulaName, townRoot, rigName); err != nil {
		return fmt.Errorf("warning: overlay has errors after editing: %w", err)
	}

	fmt.Printf("Overlay updated: %s\n", path)
	fmt.Println("Changes take effect at next 'gt prime'.")
	return nil
}
