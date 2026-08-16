package cmd

import (
	"fmt"
	"os"

	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var tapGuardBdInitCmd = &cobra.Command{
	Use:   "bd-init",
	Short: "Block bd init in wrong directories",
	Long: `Block beads initialization outside the Gas Town HQ root.

Running 'bd init' in rig worktrees or arbitrary directories creates
orphan beads databases that conflict with the centralized HQ database.

This guard blocks:
  - bd init (in any directory other than the HQ root)

Exit codes:
  0 - Operation allowed (in HQ root or not in Gas Town context)
  2 - Operation BLOCKED (in a rig worktree or other non-HQ directory)`,
	RunE: runTapGuardBdInit,
}

func init() {
	tapGuardCmd.AddCommand(tapGuardBdInitCmd)
}

func runTapGuardBdInit(cmd *cobra.Command, args []string) error {
	if !isGasTownAgentContext() {
		return nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil // can't determine — allow
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return nil // not in a town — allow
	}

	if cwd == townRoot {
		return nil
	}

	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "╔══════════════════════════════════════════════════════════════════╗")
	fmt.Fprintln(os.Stderr, "║  ❌ BD INIT BLOCKED                                              ║")
	fmt.Fprintln(os.Stderr, "╠══════════════════════════════════════════════════════════════════╣")
	fmt.Fprintln(os.Stderr, "║  Running 'bd init' here would create an orphan beads database.  ║")
	fmt.Fprintln(os.Stderr, "║  Gas Town uses a centralized DB at the HQ root.                 ║")
	fmt.Fprintf(os.Stderr, "║  HQ root: %-53s║\n", townRoot)
	fmt.Fprintf(os.Stderr, "║  CWD:     %-53s║\n", cwd)
	fmt.Fprintln(os.Stderr, "║                                                                  ║")
	fmt.Fprintln(os.Stderr, "║  The beads database already exists at the HQ root.              ║")
	fmt.Fprintln(os.Stderr, "║  Use 'bd' commands directly — they auto-discover the DB.        ║")
	fmt.Fprintln(os.Stderr, "╚══════════════════════════════════════════════════════════════════╝")
	fmt.Fprintln(os.Stderr, "")
	return NewSilentExit(2)
}
