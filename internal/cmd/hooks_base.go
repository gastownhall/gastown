package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/jonbaldie/gastown/internal/hooks"
	"github.com/spf13/cobra"
)

var hooksBaseCmd = &cobra.Command{
	Use:   "base",
	Short: "Edit the shared base hook config",
	Long: `Edit the shared base hook configuration.

The base config defines hooks that apply to all agents. It is stored
at ~/.gt/hooks-base.json. If the file doesn't exist, it will be
created with sensible defaults (PATH setup, gt prime, etc.).

After editing, run 'gt hooks sync' to propagate changes.

Examples:
  gt hooks base           # Open base config in $EDITOR
  gt hooks base --show    # Print current base config to stdout`,
	RunE: runHooksBase,
}

var hooksBaseShow bool

func init() {
	hooksCmd.AddCommand(hooksBaseCmd)
	hooksBaseCmd.Flags().BoolVar(&hooksBaseShow, "show", false, "Print current base config to stdout")
}

func runHooksBase(cmd *cobra.Command, args []string) error {
	cfg, err := hooks.LoadBase()
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("loading base config: %w", err)
		}
		// File doesn't exist yet - create with defaults
		cfg = hooks.DefaultBase()
		if err := hooks.SaveBase(cfg); err != nil {
			return fmt.Errorf("creating default base config: %w", err)
		}
		fmt.Println("Created default base config")
	}

	if hooksBaseShow {
		data, err := hooks.MarshalConfig(cfg)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	// Open in editor
	path := hooks.BasePath()
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
	if _, err := hooks.LoadBase(); err != nil {
		return fmt.Errorf("warning: base config has errors after editing: %w", err)
	}

	fmt.Println("Base config updated. Run 'gt hooks sync' to propagate changes.")
	return nil
}
