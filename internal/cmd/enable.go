// ABOUTME: Command to enable Gas Town machine-wide.
// ABOUTME: Sets the host-global state to enabled for all agentic coding tools.

package cmd

import (
	"fmt"

	"github.com/jonbaldie/gastown/internal/state"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/spf13/cobra"
)

var enableCmd = &cobra.Command{
	Use:     "enable",
	GroupID: GroupConfig,
	Short:   "Enable Gas Town machine-wide",
	Long: `Enable Gas Town machine-wide for all agentic coding tools on this machine.

This writes ~/.local/state/gastown/state.json. It is not town-scoped.
A second or test town does not need this command.

When enabled:
  - Shell hooks set GT_TOWN_ROOT and GT_RIG environment variables
  - Claude Code SessionStart hooks run 'gt prime' for context
  - Git repos are auto-registered as rigs (configurable)

Town services do not require this. Use 'gt install' then 'gt up' to boot
a town. 'gt up' does not require a prior 'gt enable'.

Use 'gt disable' to turn off. Use 'gt status' to check state.

Environment overrides:
  GASTOWN_DISABLED=1  - Disable for current session only
  GASTOWN_ENABLED=1   - Enable for current session only`,
	RunE: runEnable,
}

func init() {
	rootCmd.AddCommand(enableCmd)
}

func runEnable(cmd *cobra.Command, args []string) error {
	if err := state.Enable(Version); err != nil {
		return fmt.Errorf("enabling Gas Town: %w", err)
	}

	fmt.Printf("%s Gas Town enabled\n", style.Success.Render("✓"))
	fmt.Println()
	fmt.Println("Gas Town will now:")
	fmt.Println("  • Inject context into Claude Code sessions")
	fmt.Println("  • Set GT_TOWN_ROOT and GT_RIG environment variables")
	fmt.Println("  • Auto-register git repos as rigs (if configured)")
	fmt.Println()
	fmt.Printf("Use %s to disable, %s to check status\n",
		style.Dim.Render("gt disable"),
		style.Dim.Render("gt status"))

	return nil
}
