package cmd

import (
	"bytes"
	"fmt"
	"github.com/jonbaldie/gastown/internal/beads"
	"os/exec"
	"sort"
	"strings"

	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

// convoyLaunchForce controls whether to launch a convoy with warnings.
var convoyLaunchForce bool

// DispatchResult records the outcome of dispatching a single task.
type DispatchResult struct {
	BeadID  string
	Rig     string
	Success bool
	Error   error
}

// dispatchTaskDirect dispatches a single task to its rig.
// In production, this delegates to gt sling. Tests override this variable
// with a stub to avoid spawning real processes.
var dispatchTaskDirect = func(townRoot, beadID, rig string) error {
	cmd := exec.Command("gt", "sling", beadID, rig)
	cmd.Dir = townRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gt sling %s %s: %w\nstderr: %s", beadID, rig, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

var convoyLaunchCmd = &cobra.Command{
	Use:   "launch <convoy-id | epic-id | task-id...>",
	Short: "Launch a staged convoy: transition to open and dispatch Wave 1",
	Long: `Launch a staged convoy by transitioning its status from staged to open
and dispatching Wave 1 tasks.

For staged convoy-id input: transitions directly and dispatches.
For epic/task input: runs stage + launch in one step.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runConvoyLaunch,
}

func init() {
	convoyLaunchCmd.Flags().BoolVar(&convoyLaunchForce, "force", false, "Launch even with warnings")
}

// transitionConvoyToOpen transitions a staged convoy to open status.
// If the convoy is staged_ready, it transitions unconditionally.
// If the convoy is staged_warnings and force is true, it transitions.
// If the convoy is staged_warnings and force is false, it returns an error.
// If the convoy is already open or closed, it returns an error.
func transitionConvoyToOpen(convoyID string, force bool) error {
	result, err := bdShow(convoyID)
	if err != nil {
		return fmt.Errorf("cannot resolve convoy %s: %w", convoyID, err)
	}

	status := normalizeConvoyStatus(result.Status)

	switch status {
	case convoyStatusStagedReady:
		// Transition directly to open.
		return bdUpdateStatus(convoyID, convoyStatusOpen)

	case convoyStatusStagedWarnings:
		if !force {
			return fmt.Errorf("convoy %s has warnings, use --force to launch", convoyID)
		}
		return bdUpdateStatus(convoyID, convoyStatusOpen)

	case convoyStatusOpen:
		return fmt.Errorf("convoy %s is already launched", convoyID)

	case convoyStatusClosed:
		return fmt.Errorf("convoy %s is closed", convoyID)

	default:
		return fmt.Errorf("convoy %s has unexpected status %q", convoyID, result.Status)
	}
}

// bdUpdateStatus runs `bd update <id> --status=<status>` against the town beads
// database, since convoys live at the HQ level.
func bdUpdateStatus(beadID, status string) error {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}
	cmd := beads.Spawn("update", beadID, "--status="+status)
	cmd.Dir = townBeads
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bd update %s --status=%s: %w\noutput: %s", beadID, status, err, out)
	}
	return nil
}

// collectBlockedRigsInDAG returns a map of parked/docked rig names to the
// bead IDs that target them. Only considers slingable nodes. (gt-4owfd.1)
func collectBlockedRigsInDAG(dag *ConvoyDAG, townRoot string) map[string][]string {
	blockedRigBeads := make(map[string][]string)
	for _, node := range dag.Nodes {
		if !isSlingableType(node.Type) {
			continue
		}
		if node.Rig == "" {
			continue
		}
		if blocked, _ := IsRigParkedOrDocked(townRoot, node.Rig); blocked {
			blockedRigBeads[node.Rig] = append(blockedRigBeads[node.Rig], node.ID)
		}
	}
	return blockedRigBeads
}

// checkBlockedRigsForLaunch checks if any target rigs are parked or docked.
// Returns an error listing all blocked rigs if any are found and force is false.
// (gt-4owfd.1)
func checkBlockedRigsForLaunch(dag *ConvoyDAG, townRoot string, force bool) error {
	blockedRigBeads := collectBlockedRigsInDAG(dag, townRoot)
	if len(blockedRigBeads) == 0 {
		return nil
	}

	// Build sorted list of blocked rigs for deterministic output
	var rigs []string
	for rig := range blockedRigBeads {
		rigs = append(rigs, rig)
	}
	sort.Strings(rigs)

	if force {
		// Warn but proceed
		fmt.Printf("Warning: %d non-operational rig(s) in convoy: %s\n", len(rigs), strings.Join(rigs, ", "))
		fmt.Printf("  Proceeding with --force (tasks may fail)\n")
		return nil
	}

	// Build detailed error message
	var details []string
	for _, rig := range rigs {
		beadIDs := blockedRigBeads[rig]
		sort.Strings(beadIDs)
		details = append(details, fmt.Sprintf("  %s: %s", rig, strings.Join(beadIDs, ", ")))
	}

	return fmt.Errorf("cannot launch: %d target rig(s) are parked or docked:\n%s\n\nUse 'gt rig unpark' or 'gt rig undock' to restore, or --force to proceed anyway",
		len(rigs), strings.Join(details, "\n"))
}

// dispatchWave1 dispatches all tasks in Wave 1 of the computed waves.
// Individual task failures do not abort remaining dispatches (I-14).
// Returns a result for every Wave 1 task and a non-nil error only if waves
// are empty or contain no Wave 1.
func dispatchWave1(convoyID string, dag *ConvoyDAG, waves []Wave, townRoot string) ([]DispatchResult, error) {
	if len(waves) == 0 {
		return nil, fmt.Errorf("convoy %s: no waves to dispatch", convoyID)
	}

	wave1 := waves[0]
	if wave1.Number != 1 {
		return nil, fmt.Errorf("convoy %s: first wave has unexpected number %d", convoyID, wave1.Number)
	}

	var results []DispatchResult
	for _, taskID := range wave1.Tasks {
		node := dag.Nodes[taskID]
		rig := ""
		if node != nil {
			rig = node.Rig
		}

		err := dispatchTaskDirect(townRoot, taskID, rig)
		results = append(results, DispatchResult{
			BeadID:  taskID,
			Rig:     rig,
			Success: err == nil,
			Error:   err,
		})
	}

	return results, nil
}

// renderLaunchOutput formats the post-launch console output showing convoy ID,
// wave summary, dispatched tasks with status, and helpful hints.
func renderLaunchOutput(convoyID string, waves []Wave, results []DispatchResult, dag *ConvoyDAG) string {
	var b strings.Builder

	// Section 1: Convoy ID with status.
	fmt.Fprintf(&b, "Convoy launched: %s (status: open)\n", convoyID)
	b.WriteString("\n")

	// Section 2: Monitor command hint.
	fmt.Fprintf(&b, "  Monitor: gt convoy status %s\n", convoyID)
	b.WriteString("\n")

	// Section 3: Wave summary.
	totalTasks := 0
	for _, w := range waves {
		totalTasks += len(w.Tasks)
	}
	b.WriteString("Wave summary:\n")
	fmt.Fprintf(&b, "  %d waves, %d tasks total\n", len(waves), totalTasks)
	for _, w := range waves {
		status := "pending"
		if w.Number == 1 {
			status = "dispatched"
		}
		fmt.Fprintf(&b, "  Wave %d: %d tasks (%s)\n", w.Number, len(w.Tasks), status)
	}
	b.WriteString("\n")

	// Section 4: Dispatched tasks (Wave 1).
	b.WriteString("Dispatched (Wave 1):\n")

	// Sort results by BeadID for deterministic output.
	sorted := make([]DispatchResult, len(results))
	copy(sorted, results)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].BeadID < sorted[j].BeadID
	})

	for _, r := range sorted {
		marker := "✓"
		if !r.Success {
			marker = "✗"
		}

		title := ""
		if node := dag.Nodes[r.BeadID]; node != nil {
			title = node.Title
		}

		rigInfo := ""
		if r.Rig != "" {
			rigInfo = fmt.Sprintf("  (rig: %s)", r.Rig)
		}

		errInfo := ""
		if r.Error != nil {
			errInfo = fmt.Sprintf("    error: %v", r.Error)
		}

		fmt.Fprintf(&b, "  %s %s  %s%s%s\n", marker, r.BeadID, title, rigInfo, errInfo)
	}
	b.WriteString("\n")

	// Section 5: TUI hint.
	b.WriteString("  Hint: gt convoy -i for interactive monitoring\n")
	b.WriteString("\n")

	// Section 6: Daemon explanation.
	b.WriteString("Subsequent waves will be dispatched automatically by the daemon as tasks complete.\n")

	return b.String()
}

// runConvoyLaunch is the handler for `gt convoy launch`.
func runConvoyLaunch(cmd *cobra.Command, args []string) error {
	// Step 1: Validate args.
	if err := validateStageArgs(args); err != nil {
		return err
	}

	// Step 2: Resolve bead types via bd show for each arg.
	beadTypes := make(map[string]*bdShowResult)
	for _, arg := range args {
		result, err := bdShow(arg)
		if err != nil {
			return fmt.Errorf("cannot resolve bead %s: %w", arg, err)
		}
		beadTypes[arg] = result
	}

	// Step 3: If single arg is a convoy with staged status, transition to open
	// and dispatch Wave 1.
	if len(args) == 1 {
		result := beadTypes[args[0]]
		if isConvoyIssue(result.IssueType, result.Labels) && isStagedStatus(normalizeConvoyStatus(result.Status)) {
			convoyID := args[0]

			if err := transitionConvoyToOpen(convoyID, convoyLaunchForce); err != nil {
				return err
			}

			// Rebuild DAG from tracked beads and dispatch Wave 1.
			beads, deps, err := collectConvoyBeads(convoyID)
			if err != nil {
				return fmt.Errorf("collect beads for dispatch: %w", err)
			}

			dag := buildConvoyDAG(beads, deps)
			waves, _, err := computeWaves(dag)
			if err != nil {
				return fmt.Errorf("compute waves for dispatch: %w", err)
			}

			townRoot, err := workspace.FindFromCwdOrError()
			if err != nil {
				return fmt.Errorf("resolve town root for dispatch: %w", err)
			}

			// Check for parked/docked rigs before dispatch (gt-4owfd.1, #2120)
			if err := checkBlockedRigsForLaunch(dag, townRoot, convoyLaunchForce); err != nil {
				return err
			}

			results, err := dispatchWave1(convoyID, dag, waves, townRoot)
			if err != nil {
				return fmt.Errorf("dispatch wave 1: %w", err)
			}

			// Report results.
			fmt.Print(renderLaunchOutput(convoyID, waves, results, dag))
			return nil
		}
	}

	// Step 4: For non-convoy or non-staged input, delegate to stage+launch flow.
	// Set the --launch flag on convoyStageCmd and delegate to runConvoyStage.
	convoyStageLaunch = true
	defer func() { convoyStageLaunch = false }()
	return runConvoyStage(cmd, args)
}
