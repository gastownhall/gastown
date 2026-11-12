package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Crew command flags
var (
	crewRig           string
	crewBranch        bool
	crewJSON          bool
	crewForce         bool
	crewPurge         bool
	crewNoTmux        bool
	crewDetached      bool
	crewMessage       string
	crewAccount       string
	crewAgentOverride string
	crewAll           bool
	crewListAll       bool
	crewDryRun        bool
	crewDebug         bool
	crewReset         bool
	crewResume        string
)

var crewCmd = &cobra.Command{
	Use:     "crew",
	GroupID: GroupWorkspace,
	Short:   "Manage crew workers (persistent workspaces for humans)",
	RunE:    requireSubcommand,
	Long: `Manage crew workers - persistent workspaces for human developers.

CREW VS POLECATS:
  Polecats: Ephemeral sessions. Witness-managed. Auto-nuked after work.
  Crew:     Persistent. User-managed. Stays until you remove it.

Crew workers are full git clones (not worktrees) for human developers
who want persistent context and control over their workspace lifecycle.
Use crew workers for exploratory work, long-running tasks, or when you
want to keep uncommitted changes around.

Features:
  - Gas Town integrated: Mail, nudge, handoff all work
  - Recognizable names: dave, emma, fred (not ephemeral pool names)
  - Tmux optional: Can work in terminal directly without tmux session

Commands:
  gt crew start <name>     Start session (creates workspace if needed)
  gt crew stop <name>      Stop session(s)
  gt crew add <name>       Create workspace without starting
  gt crew list             List workspaces with status
  gt crew at <name>        Attach to session
  gt crew remove <name>    Remove workspace
  gt crew refresh <name>   Context cycle with handoff mail
  gt crew restart <name>   Kill and restart session fresh`,
}

var crewAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Create a new crew workspace",
	Long: `Create new crew workspace(s) with a clone of the rig repository.

Each workspace is created at <rig>/crew/<name>/ with:
- A full git clone of the project repository
- Mail directory for message delivery
- CLAUDE.md with crew worker prompting
- Optional feature branch (crew/<name>)

Examples:
  gt crew add dave                       # Create single workspace
  gt crew add murgen croaker goblin      # Create multiple at once
  gt crew add emma --rig greenplace      # Create in specific rig
  gt crew add fred --branch              # Create with feature branch`,
	Args: cobra.MinimumNArgs(1),
	RunE: runCrewAdd,
}

var crewListCmd = &cobra.Command{
	Use:   "list [rig]",
	Short: "List crew workspaces with status",
	Args:  cobra.MaximumNArgs(1),
	Long: `List all crew workspaces in a rig with their status.

Shows git branch, session state, and git status for each workspace.

Examples:
  gt crew list                    # List in current rig
  gt crew list greenplace         # List in specific rig (positional)
  gt crew list --rig greenplace   # List in specific rig (flag)
  gt crew list --all              # List in all rigs
  gt crew list --json             # JSON output`,
	RunE: runCrewList,
}

var crewAtCmd = &cobra.Command{
	Use:     "at [name]",
	Aliases: []string{"attach"},
	Short:   "Attach to crew workspace session",
	Long: `Start or attach to a tmux session for a crew workspace.

Creates a new tmux session if none exists, or attaches to existing.
Use --no-tmux to just print the directory path instead.

When run from inside tmux, the session is started but you stay in your
current pane. Use C-b s to switch to the new session.

When run from outside tmux, you are attached to the session (unless
--detached is specified).

Branch Handling:
  By default, the workspace stays on its current branch (a warning is
  shown if not on the default branch). Use --reset to switch to the
  default branch and pull latest changes.

Rig Discovery:
  The rig is resolved in order: --rig flag, rig/name format, current
  directory, or by scanning all rigs for the crew member name. This
  means "gt crew at dave" works from anywhere in the town if dave
  exists in exactly one rig.

  If no name is provided, attempts to detect the crew workspace from the
  current directory. If you're in <rig>/crew/<name>/, it will attach to
  that workspace automatically.

Examples:
  gt crew at dave                 # Attach to dave's session (rig auto-detected)
  gt crew at                      # Auto-detect from cwd
  gt crew at gastown/dave         # Explicit rig/name format
  gt crew at dave --reset         # Reset to default branch first
  gt crew at dave --detached      # Start session without attaching
  gt crew at dave --no-tmux       # Just print path`,
	Args: cobra.MaximumNArgs(1),
	RunE: runCrewAt,
}

var crewRemoveCmd = &cobra.Command{
	Use:   "remove <name...>",
	Short: "Remove crew workspace(s)",
	Long: `Remove one or more crew workspaces from the rig.

Checks for uncommitted changes and running sessions before removing.
Use --force to skip checks and remove anyway.

The agent bead is CLOSED by default (preserves CV history). Use --purge
to DELETE the agent bead entirely (for accidental/test crew that should
leave no trace in the ledger).

--purge also:
  - Deletes the agent bead (not just closes it)
  - Unassigns any beads assigned to this crew member
  - Clears mail in the agent's inbox
  - Properly handles git worktrees (not just regular clones)

Examples:
  gt crew remove dave                       # Remove with safety checks
  gt crew remove dave emma fred             # Remove multiple
  gt crew remove beads/grip beads/fang      # Remove from specific rig
  gt crew remove dave --force               # Force remove (closes bead)
  gt crew remove test-crew --purge          # Obliterate (deletes bead)`,
	Args: cobra.MinimumNArgs(1),
	RunE: runCrewRemove,
}

var crewRefreshCmd = &cobra.Command{
	Use:   "refresh <name>",
	Short: "Context cycling with mail-to-self handoff",
	Long: `Cycle a crew workspace session with handoff.

Sends a handoff mail to the workspace's own inbox, then restarts the session.
The new session reads the handoff mail and resumes work.

Examples:
  gt crew refresh dave                           # Refresh with auto-generated handoff
  gt crew refresh dave -m "Working on gt-123"    # Add custom message`,
	Args: cobra.ExactArgs(1),
	RunE: runCrewRefresh,
}

var crewStatusCmd = &cobra.Command{
	Use:   "status [<name>]",
	Short: "Show detailed workspace status",
	Long: `Show detailed status for crew workspace(s).

Displays session state, git status, branch info, and mail inbox status.
If no name given, shows status for all crew workers across all rigs.

Examples:
  gt crew status                  # Status of all crew workers across all rigs
  gt crew status beads            # Status of all crew workers in beads
  gt crew status --rig beads      # Status of all crew workers in beads
  gt crew status dave             # Status of specific worker
  gt crew status beads/dave       # Status of specific worker in beads
  gt crew status --json           # JSON output`,
	RunE: runCrewStatus,
}

var crewRestartCmd = &cobra.Command{
	Use:     "restart [name...]",
	Aliases: []string{"rs"},
	Short:   "Kill and restart crew workspace session(s)",
	Long: `Kill the tmux session and restart fresh with Claude.

Useful when a crew member gets confused or needs a clean slate.
Unlike 'refresh', this does NOT send handoff mail - it's a clean start.

The command will:
1. Kill existing tmux session if running
2. Start fresh session with Claude
3. Run gt prime to reinitialize context

Use --all to restart all running crew sessions across all rigs.

Examples:
  gt crew restart dave                  # Restart dave's session
  gt crew restart dave emma fred        # Restart multiple
  gt crew restart beads/grip beads/fang # Restart from specific rig
  gt crew rs emma                       # Same, using alias
  gt crew restart --all                 # Restart all running crew sessions
  gt crew restart --all --rig beads     # Restart all crew in beads rig
  gt crew restart --all --dry-run       # Preview what would be restarted`,
	Args: func(cmd *cobra.Command, args []string) error {
		if crewAll {
			if len(args) > 0 {
				return fmt.Errorf("cannot specify both --all and a name")
			}
			return nil
		}
		if len(args) < 1 {
			return fmt.Errorf("requires at least 1 argument (or --all)")
		}
		return nil
	},
	RunE: runCrewRestart,
}

var crewRenameCmd = &cobra.Command{
	Use:   "rename <old-name> <new-name>",
	Short: "Rename a crew workspace",
	Long: `Rename a crew workspace.

Kills any running session, renames the directory, and updates state.
The new session will use the new name (gt-<rig>-crew-<new-name>).

Examples:
  gt crew rename dave david       # Rename dave to david
  gt crew rename madmax max       # Rename madmax to max`,
	Args: cobra.ExactArgs(2),
	RunE: runCrewRename,
}

var crewPristineCmd = &cobra.Command{
	Use:   "pristine [<name>]",
	Short: "Sync crew workspaces with remote",
	Long: `Ensure crew workspace(s) are up-to-date.

Runs git pull for the specified crew, or all crew workers.
Reports any uncommitted changes that may need attention.

Examples:
  gt crew pristine                # Pristine all crew workers
  gt crew pristine dave           # Pristine specific worker
  gt crew pristine --json         # JSON output`,
	RunE: runCrewPristine,
}

var crewNextCmd = &cobra.Command{
	Use:   "next",
	Short: "Switch to next crew session in same rig",
	Long: `Switch to the next crew tmux session within the same rig.

Used internally by tmux keybindings for quick session cycling.
Pass --session to specify the current tmux session name.`,
	Hidden: true, // Internal command for tmux keybindings
	RunE:   runCrewNext,
}

var crewPrevCmd = &cobra.Command{
	Use:   "prev",
	Short: "Switch to previous crew session in same rig",
	Long: `Switch to the previous crew tmux session within the same rig.

Used internally by tmux keybindings for quick session cycling.
Pass --session to specify the current tmux session name.`,
	Hidden: true, // Internal command for tmux keybindings
	RunE:   runCrewPrev,
}

var crewStartCmd = &cobra.Command{
	Use:     "start [rig] [name...]",
	Aliases: []string{"spawn"},
	Short:   "Start crew worker(s) in a rig",
	Long: `Start crew workers in a rig, creating workspaces if they don't exist.

The rig name can be provided as the first argument, or inferred from the
current directory. If no crew names are specified, starts all crew in the rig.

The crew session starts in the background with Claude running and ready.

Use --resume to resume a previous session instead of starting fresh. This
passes the agent's resume flag (e.g., Claude's --resume) so the session
picks up where it left off, with proper Gas Town metadata set so GC doesn't
kill the session.

Examples:
  gt crew start beads             # Start all crew in beads rig
  gt crew start                   # Start all crew (rig inferred from cwd)
  gt crew start beads grip fang   # Start specific crew in beads rig
  gt crew start gastown joe       # Start joe in gastown rig
  gt crew start beads ace --resume          # Resume ace's most recent session
  gt crew start beads ace --resume abc123   # Resume specific session ID`,
	Args: func(cmd *cobra.Command, args []string) error {
		// With --all, we can have 0 args (infer rig) or 1+ args (rig specified)
		if crewAll {
			return nil
		}
		// Allow: 0 args (infer rig, default to --all)
		//        1 arg  (rig specified, default to --all)
		//        2+ args (rig + specific crew names)
		return nil
	},
	RunE: runCrewStart,
}

var crewStopCmd = &cobra.Command{
	Use:   "stop [name...]",
	Short: "Stop crew workspace session(s)",
	Long: `Stop one or more running crew workspace sessions.

If a rig name is given alone, stops all crew in that rig. Otherwise stops
the specified crew member(s).

The name can include the rig in slash format (e.g., beads/emma).
If not specified, the rig is inferred from the current directory.

Output is captured before stopping for debugging purposes (use --force
to skip capture for faster shutdown).

Examples:
  gt crew stop beads                        # Stop all crew in beads rig
  gt crew stop                              # Stop all crew (rig inferred from cwd)
  gt crew stop beads/emma                   # Stop specific crew member
  gt crew stop dave                         # Stop dave in current rig
  gt crew stop --all                        # Stop all running crew sessions
  gt crew stop dave --force                 # Stop without capturing output`,
	Args: func(cmd *cobra.Command, args []string) error {
		if crewAll {
			if len(args) > 0 {
				return fmt.Errorf("cannot specify both --all and a name")
			}
			return nil
		}
		// Allow: 0 args (infer rig, default to --all)
		//        1 arg  (rig name → all in that rig, or crew name → specific crew)
		//        1+ args (specific crew names)
		return nil
	},
	RunE: runCrewStop,
}

func init() {
	// Add flags
	crewAddCmd.Flags().StringVar(&crewRig, "rig", "", "Rig to create crew workspace in")
	crewAddCmd.Flags().BoolVar(&crewBranch, "branch", false, "Create a feature branch (crew/<name>)")

	crewListCmd.Flags().StringVar(&crewRig, "rig", "", "Filter by rig name")
	crewListCmd.Flags().BoolVar(&crewListAll, "all", false, "List crew workspaces in all rigs")
	crewListCmd.Flags().BoolVar(&crewJSON, "json", false, "Output as JSON")

	crewAtCmd.Flags().StringVar(&crewRig, "rig", "", "Rig to use")
	crewAtCmd.Flags().BoolVar(&crewNoTmux, "no-tmux", false, "Just print directory path")
	crewAtCmd.Flags().BoolVarP(&crewDetached, "detached", "d", false, "Start session without attaching")
	crewAtCmd.Flags().StringVar(&crewAccount, "account", "", "Claude Code account handle to use (overrides default)")
	crewAtCmd.Flags().StringVar(&crewAgentOverride, "agent", "", "Agent alias to run crew worker with (overrides rig/town default)")
	crewAtCmd.Flags().BoolVar(&crewDebug, "debug", false, "Show debug output for troubleshooting")
	crewAtCmd.Flags().BoolVar(&crewReset, "reset", false, "Reset workspace to default branch (checkout and pull)")

	crewRemoveCmd.Flags().StringVar(&crewRig, "rig", "", "Rig to use")
	crewRemoveCmd.Flags().BoolVar(&crewForce, "force", false, "Force remove (skip safety checks)")
	crewRemoveCmd.Flags().BoolVar(&crewPurge, "purge", false, "Obliterate: delete agent bead, unassign work, clear mail")

	crewRefreshCmd.Flags().StringVar(&crewRig, "rig", "", "Rig to use")
	crewRefreshCmd.Flags().StringVarP(&crewMessage, "message", "m", "", "Custom handoff message")

	crewStatusCmd.Flags().StringVar(&crewRig, "rig", "", "Filter by rig name")
	crewStatusCmd.Flags().BoolVar(&crewJSON, "json", false, "Output as JSON")

	crewRenameCmd.Flags().StringVar(&crewRig, "rig", "", "Rig to use")

	crewPristineCmd.Flags().StringVar(&crewRig, "rig", "", "Filter by rig name")
	crewPristineCmd.Flags().BoolVar(&crewJSON, "json", false, "Output as JSON")

	crewRestartCmd.Flags().StringVar(&crewRig, "rig", "", "Rig to use (filter when using --all)")
	crewRestartCmd.Flags().BoolVar(&crewAll, "all", false, "Restart all running crew sessions")
	crewRestartCmd.Flags().BoolVar(&crewDryRun, "dry-run", false, "Show what would be restarted without restarting")
	crewRestartCmd.Flags().StringVar(&crewAgentOverride, "agent", "", "Agent alias to run restarted crew with (overrides rig/town default)")

	crewStartCmd.Flags().StringVar(&crewRig, "rig", "", "Rig to start crew in (alternative to positional rig arg)")
	crewStartCmd.Flags().BoolVar(&crewAll, "all", false, "Start all crew members in the rig")
	crewStartCmd.Flags().StringVar(&crewAccount, "account", "", "Claude Code account handle to use")
	crewStartCmd.Flags().StringVar(&crewAgentOverride, "agent", "", "Agent alias to run crew worker with (overrides rig/town default)")
	crewStartCmd.Flags().StringVar(&crewResume, "resume", "", "Resume a previous session (optionally specify session ID)")
	crewStartCmd.Flags().Lookup("resume").NoOptDefVal = "last"

	crewStopCmd.Flags().StringVar(&crewRig, "rig", "", "Rig to use (filter when using --all)")
	crewStopCmd.Flags().BoolVar(&crewAll, "all", false, "Stop all running crew sessions")
	crewStopCmd.Flags().BoolVar(&crewDryRun, "dry-run", false, "Show what would be stopped without stopping")
	crewStopCmd.Flags().BoolVar(&crewForce, "force", false, "Skip output capture for faster shutdown")

	// Add subcommands
	crewCmd.AddCommand(crewAddCmd)
	crewCmd.AddCommand(crewListCmd)
	crewCmd.AddCommand(crewAtCmd)
	crewCmd.AddCommand(crewRemoveCmd)
	crewCmd.AddCommand(crewRefreshCmd)
	crewCmd.AddCommand(crewStatusCmd)
	crewCmd.AddCommand(crewRenameCmd)
	crewCmd.AddCommand(crewPristineCmd)
	crewCmd.AddCommand(crewRestartCmd)

	// Add --session flag to next/prev commands for tmux key binding support
	// When run via run-shell, tmux session context may be wrong, so we pass it explicitly
	crewNextCmd.Flags().StringVarP(&crewCycleSession, "session", "s", "", "tmux session name (for key bindings)")
	crewPrevCmd.Flags().StringVarP(&crewCycleSession, "session", "s", "", "tmux session name (for key bindings)")
	crewCmd.AddCommand(crewNextCmd)
	crewCmd.AddCommand(crewPrevCmd)
	crewCmd.AddCommand(crewStartCmd)
	crewCmd.AddCommand(crewStopCmd)

	rootCmd.AddCommand(crewCmd)
}
