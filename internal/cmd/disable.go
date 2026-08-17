// ABOUTME: Command to disable Gas Town machine-wide.
// ABOUTME: Sets the host-global state to disabled so tools work vanilla.

package cmd

import (
	"fmt"

	"github.com/jonbaldie/gastown/internal/shell"
	"github.com/jonbaldie/gastown/internal/state"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/spf13/cobra"
)

var disableClean bool

var disableCmd = &cobra.Command{
	Use:     "disable",
	GroupID: GroupConfig,
	Short:   "Disable Gas Town machine-wide",
	Long: `Disable Gas Town machine-wide for all agentic coding tools on this machine.

This updates ~/.local/state/gastown/state.json. It is not town-scoped.
Town workspaces stay on disk. 'gt up' in a town does not require enable.

When disabled:
  - Shell hooks become no-ops
  - Claude Code SessionStart hooks skip 'gt prime'
  - Tools work 100% vanilla (no Gas Town behavior)

The workspace (~/gt) is preserved. Use 'gt enable' to re-enable.

Flags:
  --clean  Also remove shell integration from ~/.zshrc/~/.bashrc

Environment overrides still work:
  GASTOWN_ENABLED=1   - Enable for current session only`,
	RunE: runDisable,
}

func init() {
	disableCmd.Flags().BoolVar(&disableClean, "clean", false,
		"Remove shell integration from RC files")
	rootCmd.AddCommand(disableCmd)
}

func runDisable(cmd *cobra.Command, args []string) error {
	if err := state.Disable(); err != nil {
		return fmt.Errorf("disabling Gas Town: %w", err)
	}

	if disableClean {
		if err := removeShellIntegration(); err != nil {
			fmt.Printf("%s Could not clean shell integration: %v\n",
				style.Warning.Render("!"), err)
		} else {
			fmt.Println("  Removed shell integration from RC files")
		}
	}

	fmt.Printf("%s Gas Town disabled\n", style.Success.Render("✓"))
	fmt.Println()
	fmt.Println("All agentic coding tools now work vanilla.")
	if !disableClean {
		fmt.Printf("Use %s to also remove shell hooks\n",
			style.Dim.Render("gt disable --clean"))
	}
	fmt.Printf("Use %s to re-enable\n", style.Dim.Render("gt enable"))

	return nil
}

func removeShellIntegration() error {
	return shell.Remove()
}
