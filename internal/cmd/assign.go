package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/events"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var assignCmd = &cobra.Command{
	Use:     "assign <crew-member> <title>",
	GroupID: GroupWork,
	Short:   "Create a bead and hook it to a crew member",
	Long: `Create a new bead and immediately hook it to a crew member.

This is a shortcut for "bd create" + "gt hook". The crew member name
is short-form (just the name), and the rig is resolved in order:
--rig flag, current directory, or by scanning all rigs for the crew
member name. This means "gt assign dave ..." works from anywhere in
the town if dave exists in exactly one rig.

The crew member must exist (the directory <rig>/crew/<name> must be
present) or the command will error.

Examples:
  gt assign monet "Fix the auth token refresh bug"
  gt assign monet "Review error handling" -d "The retry logic looks wrong"
  gt assign monet "Fix auth bug" --type bug --priority 1
  gt assign monet "Fix auth bug" --nudge
  gt assign monet "Fix auth bug" --label important
  gt assign monet "Fix auth bug" --rig beads   # Explicit rig override`,
	Args: cobra.MinimumNArgs(2),
	RunE: runAssign,
}

var (
	assignDescription string
	assignType        string
	assignPriority    string
	assignLabels      []string
	assignNudge       bool
	assignRig         string
	assignDryRun      bool
	assignForce       bool
)

func init() {
	assignCmd.Flags().StringVarP(&assignDescription, "description", "d", "", "Bead description")
	assignCmd.Flags().StringVarP(&assignType, "type", "t", "task", "Bead type")
	assignCmd.Flags().StringVarP(&assignPriority, "priority", "p", "2", "Priority 0-4")
	assignCmd.Flags().StringArrayVarP(&assignLabels, "label", "l", nil, "Labels (repeatable)")
	assignCmd.Flags().BoolVar(&assignNudge, "nudge", false, "Wake the agent after hooking")
	assignCmd.Flags().StringVar(&assignRig, "rig", "", "Override rig inference")
	assignCmd.Flags().BoolVarP(&assignDryRun, "dry-run", "n", false, "Show what would happen")
	assignCmd.Flags().BoolVar(&assignForce, "force", false, "Replace existing hooked work")

	rootCmd.AddCommand(assignCmd)
}

func runAssign(_ *cobra.Command, args []string) error {
	crewName := args[0]
	title := strings.Join(args[1:], " ")

	// Find town root
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	// Determine rig
	rigName := assignRig
	if rigName == "" {
		rigName, err = inferRigFromCwd(townRoot)
		if err != nil {
			// Fallback: scan all rigs for a crew member with this name
			rigName, err = inferRigFromCrewName(townRoot, crewName)
			if err != nil {
				return fmt.Errorf("inferring rig (use --rig to specify): %w", err)
			}
		}
	}

	// Validate crew member exists
	crewDir := filepath.Join(townRoot, rigName, "crew", crewName)
	if _, err := os.Stat(crewDir); os.IsNotExist(err) {
		return fmt.Errorf("crew member %q not found in rig %q (no directory %s)", crewName, rigName, crewDir)
	}

	agentID := rigName + "/crew/" + crewName

	if assignDryRun {
		fmt.Printf("Would create bead: %q (type=%s, priority=%s)\n", title, assignType, assignPriority)
		fmt.Printf("Would hook to: %s\n", agentID)
		if assignDescription != "" {
			fmt.Printf("  description: %s\n", assignDescription)
		}
		for _, l := range assignLabels {
			fmt.Printf("  label: %s\n", l)
		}
		if assignNudge {
			fmt.Printf("Would nudge: %s\n", agentID)
		}
		return nil
	}

	// Step 1: Create the bead
	createArgs := []string{"create", "--title=" + title, "--type=" + assignType, "--priority=" + assignPriority, "--silent"}
	if assignDescription != "" {
		createArgs = append(createArgs, "--description="+assignDescription)
	}
	for _, l := range assignLabels {
		createArgs = append(createArgs, "--label="+l)
	}

	fmt.Printf("%s Creating bead for %s...\n", style.Bold.Render("📋"), agentID)

	out, err := BdCmd(createArgs...).
		Dir(townRoot).
		WithAutoCommit().
		Output()
	if err != nil {
		return fmt.Errorf("creating bead: %w", err)
	}

	beadID := strings.TrimSpace(string(out))
	if beadID == "" {
		return fmt.Errorf("bd create returned empty ID")
	}

	fmt.Printf("  Created: %s\n", beadID)

	// Step 2: Hook the bead to the agent with retry logic
	fmt.Printf("%s Hooking %s to %s...\n", style.Bold.Render("🪝"), beadID, agentID)

	const maxRetries = 5
	const baseBackoff = 500 * time.Millisecond
	const backoffMax = 10 * time.Second
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := BdCmd("update", beadID, "--status=hooked", "--assignee="+agentID).
			Dir(townRoot).
			WithAutoCommit().
			Run(); err != nil {
			lastErr = err
			if attempt < maxRetries {
				backoff := slingBackoff(attempt, baseBackoff, backoffMax)
				fmt.Printf("%s Hook attempt %d failed, retrying in %v...\n", style.Warning.Render("⚠"), attempt, backoff)
				time.Sleep(backoff)
				continue
			}
			return fmt.Errorf("hooking bead after %d attempts: %w", maxRetries, lastErr)
		}
		break
	}

	// Step 3: Update agent hook_bead field so gt hook / gt mol status can find the work.
	townBeadsDir := filepath.Join(townRoot, ".beads")
	rigBeadsDir := filepath.Join(townRoot, rigName, ".beads")
	if err := updateAgentHookBead(agentID, beadID, rigBeadsDir, townBeadsDir); err != nil {
		fmt.Printf("%s Could not update agent hook: %v\n", style.Dim.Render("Warning:"), err)
	}

	// Step 4: Log event
	if err := events.LogFeed(events.TypeHook, agentID, events.HookPayload(beadID)); err != nil {
		fmt.Fprintf(os.Stderr, "%s Warning: failed to log event: %v\n", style.Dim.Render("⚠"), err)
	}

	fmt.Printf("%s Assigned %s to %s — %q\n", style.Bold.Render("✓"), beadID, agentID, title)

	// Step 5: Nudge or warn
	if !assignNudge {
		fmt.Printf("  %s Agent won't be notified (use --nudge to wake them)\n", style.Dim.Render("ℹ"))
	} else {
		nudgeMsg := fmt.Sprintf("New work on your hook: %s", title)
		nudgeCmd := exec.Command("gt", "nudge", agentID, "-m", nudgeMsg)
		nudgeCmd.Stderr = os.Stderr
		if out, err := nudgeCmd.Output(); err != nil {
			fmt.Fprintf(os.Stderr, "%s Warning: nudge failed: %v\n", style.Warning.Render("⚠"), err)
		} else if len(out) > 0 {
			fmt.Print(string(out))
		} else {
			fmt.Printf("  Nudged %s\n", agentID)
		}
	}

	return nil
}
