package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/formula"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var formulaSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Update installed formulas from the embedded copies",
	Long: `Update the town's installed formulas (.beads/formulas/) from the
copies embedded in this gt binary.

Outdated and missing formulas are rewritten; formulas you have modified by
hand are left alone (tracked via checksums in .beads/formulas/.installed.json).

This is the surgical alternative to 'gt upgrade' or 'gt doctor --fix' when
only the formulas need refreshing after installing a new gt binary — stale
installed copies otherwise keep referencing commands and steps that drifted
from the shipped versions (hq-uy5).

Examples:
  gt formula sync`,
	RunE: runFormulaSync,
}

func init() {
	formulaCmd.AddCommand(formulaSyncCmd)
}

func runFormulaSync(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	return formulaSyncForTown(townRoot)
}

// formulaSyncForTown updates the town's installed formulas from the embedded
// copies and reports what changed.
func formulaSyncForTown(townRoot string) error {
	updated, skipped, reinstalled, err := formula.UpdateFormulas(townRoot)
	if err != nil {
		return fmt.Errorf("updating formulas: %w", err)
	}

	if updated+skipped+reinstalled == 0 {
		fmt.Printf("%s Formulas up-to-date\n", style.SuccessPrefix)
		return nil
	}
	fmt.Printf("%s Formulas synced: %d updated, %d reinstalled", style.SuccessPrefix, updated, reinstalled)
	if skipped > 0 {
		fmt.Printf(", %d skipped (user-modified)", skipped)
	}
	fmt.Println()
	return nil
}
