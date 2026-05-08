package cmd

import (
	"github.com/spf13/cobra"
)

var (
	hooksCmdVerbose bool
	hooksCmdJSON    bool
)

var hooksCmd = &cobra.Command{
	Use:     "hooks",
	GroupID: GroupConfig,
	Short:   "List Claude Code hooks in workspace (or manage hooks via subcommands)",
	Long: `List all Claude Code hooks across the Gas Town workspace, grouped by type.

Scans .claude/settings.json files in town root, rigs, polecats, crew,
witness, and refinery. Shows each hook with its owning agent.

Flags:
  --verbose   Show actual hook commands
  --json      Machine-readable JSON output

Subcommands (for hook management):
  base       Edit the shared base hook config
  override   Edit overrides for a role or rig
  sync       Regenerate all .claude/settings.json files
  diff       Show what sync would change
  list       Show all managed settings.json locations
  scan       Scan workspace for existing hooks
  registry   List hooks from the registry
  install    Install a hook from the registry

Config structure:
  Base:      ~/.gt/hooks-base.json
  Overrides: ~/.gt/hooks-overrides/<target>.json

Merge strategy: base → role → rig+role (more specific wins)

Examples:
  gt hooks                # List all hooks with agent ownership
  gt hooks --verbose      # Show actual hook commands
  gt hooks --json         # Machine-readable output
  gt hooks sync           # Regenerate all settings.json files
  gt hooks diff           # Preview what sync would change
  gt hooks base           # Edit the shared base config
  gt hooks override crew  # Edit overrides for all crew workers
  gt hooks list           # Show managed locations and sync status`,
	RunE: runHooksDefault,
}

func runHooksDefault(cmd *cobra.Command, args []string) error {
	return doHooksScan(hooksCmdVerbose, hooksCmdJSON)
}

func init() {
	rootCmd.AddCommand(hooksCmd)
	hooksCmd.Flags().BoolVarP(&hooksCmdVerbose, "verbose", "v", false, "Show hook commands")
	hooksCmd.Flags().BoolVar(&hooksCmdJSON, "json", false, "Machine-readable JSON output")
}
