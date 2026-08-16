package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/jonbaldie/gastown/internal/hooks"
	"github.com/spf13/cobra"
)

var hooksOverrideCmd = &cobra.Command{
	Use:   "override <target>",
	Short: "Edit overrides for a role or rig",
	Long: `Edit hook overrides for a specific role or rig+role combination.

Valid targets:
  Role-level:  crew, witness, refinery, polecats, mayor, deacon
  Rig+role:    gastown/crew, beads/witness, sky/polecats, etc.

Overrides are merged on top of the base config during sync.
Hooks with the same matcher replace the base hook entirely.

Override files are stored in ~/.gt/hooks-overrides/<target>.json.

Examples:
  gt hooks override crew              # Edit crew role overrides
  gt hooks override gastown/crew      # Edit gastown rig crew overrides
  gt hooks override mayor             # Edit mayor overrides
  gt hooks override crew --show       # Print current override config`,
	Args: cobra.ExactArgs(1),
	RunE: runHooksOverride,
}

var hooksOverrideShow bool

func init() {
	hooksCmd.AddCommand(hooksOverrideCmd)
	hooksOverrideCmd.Flags().BoolVar(&hooksOverrideShow, "show", false, "Print current override config to stdout")
}

func runHooksOverride(cmd *cobra.Command, args []string) error {
	normalized, ok := hooks.NormalizeTarget(args[0])
	if !ok {
		return fmt.Errorf("invalid target %q; valid targets are roles (crew, witness, refinery, polecats, mayor, deacon) or rig/role (gastown/crew, etc.)", args[0])
	}
	target := normalized

	cfg, err := hooks.LoadOverride(target)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("loading override config for %q: %w", target, err)
		}
		// File doesn't exist yet - create empty
		cfg = &hooks.HooksConfig{}
		if err := hooks.SaveOverride(target, cfg); err != nil {
			return fmt.Errorf("creating override config: %w", err)
		}
		fmt.Printf("Created empty override config for %s\n", target)
	}

	if hooksOverrideShow {
		data, err := hooks.MarshalConfig(cfg)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	// Open in editor
	path := hooks.OverridePath(target)
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
	if _, err := hooks.LoadOverride(target); err != nil {
		return fmt.Errorf("warning: override config has errors after editing: %w", err)
	}

	fmt.Printf("Override config for %s updated. Run 'gt hooks sync' to propagate changes.\n", target)
	return nil
}
