package cmd

import (
	"github.com/spf13/cobra"
)

// Molecule command flags
var (
	moleculeJSON      bool
	moleculeJitter    string // jitter duration for squash (e.g. "10s")
	moleculeSummary   string // optional summary for squash digest
	moleculeNoDigest  bool   // skip digest bead creation on squash
)

var moleculeCmd = &cobra.Command{
	Use:         "mol",
	Aliases:     []string{"molecule"},
	GroupID:     GroupWork,
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Agent molecule workflow commands",
	RunE:        requireSubcommand,
	Long: `Agent-specific molecule workflow operations.

These commands operate on YOUR hook and YOUR attached molecules.
Use 'gt hook' to see what's on your hook (alias for 'gt mol status').

VIEWING YOUR WORK:
  gt hook              Show what's on your hook
  gt mol current       Show what you should be working on
  gt mol progress      Show execution progress

WORKING ON STEPS:
  gt mol step done     Complete current step (auto-continues)

LIFECYCLE:
  gt mol attach        Attach molecule to your hook
  gt mol detach        Detach molecule from your hook
  gt mol burn          Discard attached molecule (no record)
  gt mol squash        Compress to digest (permanent record)

TO DISPATCH WORK (with molecules):
  gt sling mol-xxx target   # Pour formula + sling to agent
  gt formulas               # List available formulas`,
}


var moleculeProgressCmd = &cobra.Command{
	Use:   "progress <root-issue-id>",
	Short: "Show progress through a molecule's steps",
	Long: `Show the execution progress of an instantiated molecule.

Given a root issue (the parent of molecule steps), displays:
- Total steps and completion status
- Which steps are done, in-progress, ready, or blocked
- Overall progress percentage

This is useful for the Witness to monitor molecule execution.

Example:
  gt molecule progress gt-abc`,
	Args: cobra.ExactArgs(1),
	RunE: runMoleculeProgress,
}

var moleculeAttachCmd = &cobra.Command{
	Use:   "attach [pinned-bead-id] <molecule-id>",
	Short: "Attach a molecule to a pinned bead",
	Long: `Attach a molecule to a pinned/handoff bead.

This records which molecule an agent is currently working on. The attachment
is stored in the pinned bead's description and visible via 'bd show'.

When called with a single argument from an agent working directory, the
pinned bead ID is auto-detected from the current agent's hook.

Examples:
  gt molecule attach gt-abc mol-xyz  # Explicit pinned bead
  gt molecule attach mol-xyz         # Auto-detect from cwd`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runMoleculeAttach,
}

var moleculeDetachCmd = &cobra.Command{
	Use:   "detach <pinned-bead-id>",
	Short: "Detach molecule from a pinned bead",
	Long: `Remove molecule attachment from a pinned/handoff bead.

This clears the attached_molecule and attached_at fields from the bead.

Example:
  gt molecule detach gt-abc`,
	Args: cobra.ExactArgs(1),
	RunE: runMoleculeDetach,
}

var moleculeAttachmentCmd = &cobra.Command{
	Use:   "attachment <pinned-bead-id>",
	Short: "Show attachment status of a pinned bead",
	Long: `Show which molecule is attached to a pinned bead.

Example:
  gt molecule attachment gt-abc`,
	Args: cobra.ExactArgs(1),
	RunE: runMoleculeAttachment,
}

var moleculeAttachFromMailCmd = &cobra.Command{
	Use:   "attach-from-mail <mail-id>",
	Short: "Attach a molecule from a mail message",
	Long: `Attach a molecule to the current agent's hook from a mail message.

This command reads a mail message, extracts the molecule ID from the body,
and attaches it to the agent's pinned bead (hook).

The mail body should contain an "attached_molecule:" field with the molecule ID.

Usage: gt mol attach-from-mail <mail-id>

Behavior:
1. Read mail body for attached_molecule field
2. Attach molecule to agent's hook
3. Mark mail as read
4. Return control for execution

Example:
  gt mol attach-from-mail msg-abc123`,
	Args: cobra.ExactArgs(1),
	RunE: runMoleculeAttachFromMail,
}

var moleculeStatusCmd = &cobra.Command{
	Use:   "status [target]",
	Short: "Show what's on an agent's hook",
	Long: `Show what's slung on an agent's hook.

If no target is specified, shows the current agent's status based on
the working directory (polecat, crew member, witness, etc.).

Output includes:
- What's slung (molecule name, associated issue)
- Current phase and progress
- Whether it's a wisp
- Next action hint

Examples:
  gt mol status                       # Show current agent's hook
  gt mol status greenplace/nux        # Show specific polecat's hook
  gt mol status greenplace/witness    # Show witness's hook`,
	Args: cobra.MaximumNArgs(1),
	RunE: runMoleculeStatus,
}

var moleculeCurrentCmd = &cobra.Command{
	Use:   "current [identity]",
	Short: "Show what agent should be working on",
	Long: `Query what an agent is supposed to be working on via breadcrumb trail.

Looks up the agent's handoff bead, checks for attached molecules, and
identifies the current/next step in the workflow.

If no identity is specified, uses the current agent based on working directory.

Output includes:
- Identity and handoff bead info
- Attached molecule (if any)
- Progress through steps
- Current step that should be worked on next

Examples:
  gt molecule current                 # Current agent's work
  gt molecule current greenplace/furiosa
  gt molecule current deacon
  gt mol current greenplace/witness`,
	Args: cobra.MaximumNArgs(1),
	RunE: runMoleculeCurrent,
}


var moleculeBurnCmd = &cobra.Command{
	Use:   "burn [target]",
	Short: "Burn current molecule without creating a digest",
	Long: `Burn (destroy) the current molecule attachment.

This discards the molecule without creating a permanent record. Use this
when abandoning work or when a molecule doesn't need an audit trail.

If no target is specified, burns the current agent's attached molecule.

For wisps, burning is the default completion action. For regular molecules,
consider using 'squash' instead to preserve an audit trail.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runMoleculeBurn,
}

var moleculeSquashCmd = &cobra.Command{
	Use:   "squash [target]",
	Short: "Compress molecule into a digest",
	Long: `Squash the current molecule into a permanent digest.

This condenses a completed molecule's execution into a compact record.
The digest preserves:
- What molecule was executed
- When it ran
- Summary of results

Use this for patrol cycles and other operational work that should have
a permanent (but compact) record.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runMoleculeSquash,
}

var moleculeStepCmd = &cobra.Command{
	Use:   "step",
	Short: "Molecule step operations",
	RunE:  requireSubcommand,
	Long: `Commands for working with molecule steps.

A molecule is a DAG of steps. Each step is a beads issue with the molecule root
as its parent. Steps can have dependencies on other steps.

When a polecat is working on a molecule, it processes one step at a time:
1. Work on the current step
2. When done: gt mol step done <step-id>
3. System auto-continues to next ready step

IMPORTANT: Always use 'gt mol step done' to complete steps. Do not manually
close steps with 'bd close' - that skips the auto-continuation logic.`,
}


func init() {
	// Progress flags
	moleculeProgressCmd.Flags().BoolVar(&moleculeJSON, "json", false, "Output as JSON")

	// Attachment flags
	moleculeAttachmentCmd.Flags().BoolVar(&moleculeJSON, "json", false, "Output as JSON")

	// Status flags
	moleculeStatusCmd.Flags().BoolVar(&moleculeJSON, "json", false, "Output as JSON")

	// Current flags
	moleculeCurrentCmd.Flags().BoolVar(&moleculeJSON, "json", false, "Output as JSON")

	// Burn flags
	moleculeBurnCmd.Flags().BoolVar(&moleculeJSON, "json", false, "Output as JSON")
	registerOrphanBurnFlags()

	// Squash flags
	moleculeSquashCmd.Flags().BoolVar(&moleculeJSON, "json", false, "Output as JSON")
	moleculeSquashCmd.Flags().StringVar(&moleculeJitter, "jitter", "", "Sleep a random duration from 0 to this value before squashing (e.g. '10s') to reduce concurrent Dolt lock contention")
	moleculeSquashCmd.Flags().StringVar(&moleculeSummary, "summary", "", "Optional summary for the squash digest (e.g. patrol observations)")
	moleculeSquashCmd.Flags().BoolVar(&moleculeNoDigest, "no-digest", false, "Skip digest bead creation (for patrol molecules that run frequently)")

	// Add step subcommand with its children
	moleculeStepCmd.AddCommand(moleculeStepDoneCmd)
	moleculeCmd.AddCommand(moleculeStepCmd)

	// Add subcommands (agent-specific operations only)
	moleculeCmd.AddCommand(moleculeStatusCmd)
	moleculeCmd.AddCommand(moleculeCurrentCmd)
	moleculeCmd.AddCommand(moleculeBurnCmd)
	moleculeCmd.AddCommand(moleculeBurnOrphansCmd)
	moleculeCmd.AddCommand(moleculeSquashCmd)
	moleculeCmd.AddCommand(moleculeProgressCmd)
	moleculeCmd.AddCommand(moleculeDagCmd)
	moleculeCmd.AddCommand(moleculeAttachCmd)
	moleculeCmd.AddCommand(moleculeDetachCmd)
	moleculeCmd.AddCommand(moleculeAttachmentCmd)
	moleculeCmd.AddCommand(moleculeAttachFromMailCmd)

	rootCmd.AddCommand(moleculeCmd)
}
