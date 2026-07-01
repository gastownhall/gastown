package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/workspace"
)

// mountainForce controls whether to launch a mountain with warnings.
var mountainForce bool

// mountainJSON controls whether output is machine-readable JSON.
var mountainJSON bool

var mountainCmd = &cobra.Command{
	Use:         "mountain <epic-id>",
	GroupID:     GroupWork,
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Activate Mountain-Eater: stage, label, and launch an epic",
	Long: `Activate the Mountain-Eater on an epic for autonomous grinding.

A mountain is a convoy with the 'mountain' label. This command:
  1. Stages the convoy (validate DAG, compute waves)
  2. Adds the 'mountain' label (enables Deacon audit + Witness failure tracking)
  3. Launches the convoy (dispatches Wave 1)

Regular convoys (no mountain label) continue working as normal.
The mountain label opts a convoy into enhanced stall detection,
skip-after-N-failures, and active progress monitoring.

Use subcommands to manage active mountains:
  gt mountain status [id]    Show mountain progress
  gt mountain pause <id>     Pause a mountain (stop dispatching)
  gt mountain resume <id>    Resume a paused mountain
  gt mountain cancel <id>    Cancel (remove mountain label)

Examples:
  gt mountain gt-epic-auth       Activate mountain on an epic
  gt mountain --force gt-epic-x  Launch even with staging warnings`,
	Args: cobra.ExactArgs(1),
	RunE: runMountain,
}

var mountainStatusCmd = &cobra.Command{
	Use:   "status [epic-id|convoy-id]",
	Short: "Show mountain progress",
	Long: `Show progress for active mountains.

Without arguments, lists all active mountains with progress bars.
With an ID, shows detailed status including active/blocked/skipped tasks.

Accepts either an epic ID or convoy ID.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runMountainStatus,
}

var mountainPauseCmd = &cobra.Command{
	Use:   "pause <epic-id|convoy-id>",
	Short: "Pause a mountain (stop dispatching new waves)",
	Long: `Pause an active mountain. Keeps the mountain label but stops
new wave dispatch. Active polecats continue their current work.

Resume with 'gt mountain resume'.`,
	Args: cobra.ExactArgs(1),
	RunE: runMountainPause,
}

var mountainResumeCmd = &cobra.Command{
	Use:   "resume <epic-id|convoy-id>",
	Short: "Resume a paused mountain",
	Long: `Resume a previously paused mountain. Re-enables wave dispatch
and continues grinding from where it left off.`,
	Args: cobra.ExactArgs(1),
	RunE: runMountainResume,
}

var mountainCancelCmd = &cobra.Command{
	Use:   "cancel <epic-id|convoy-id>",
	Short: "Cancel a mountain (remove label, keep convoy)",
	Long: `Cancel the Mountain-Eater on a convoy. Removes the mountain label
but leaves the convoy for manual management. Active polecats continue
their current work but no new waves will be dispatched with enhanced
monitoring.`,
	Args: cobra.ExactArgs(1),
	RunE: runMountainCancel,
}

func init() {
	mountainCmd.Flags().BoolVarP(&mountainForce, "force", "f", false, "Launch even with staging warnings")
	mountainCmd.Flags().BoolVar(&mountainJSON, "json", false, "Output machine-readable JSON")

	mountainStatusCmd.Flags().BoolVar(&mountainJSON, "json", false, "Output as JSON")

	mountainCmd.AddCommand(mountainStatusCmd)
	mountainCmd.AddCommand(mountainPauseCmd)
	mountainCmd.AddCommand(mountainResumeCmd)
	mountainCmd.AddCommand(mountainCancelCmd)

	rootCmd.AddCommand(mountainCmd)
}

// runMountain implements `gt mountain <epic-id>`.
// Stages a convoy from the epic, adds the mountain label, and launches Wave 1.
func runMountain(cmd *cobra.Command, args []string) error {
	epicID := args[0]

	// Step 1: Validate the input is an epic.
	result, err := bdShow(epicID)
	if err != nil {
		return fmt.Errorf("cannot resolve %s: %w", epicID, err)
	}
	if result.IssueType != "epic" {
		return fmt.Errorf("%s is a %s, not an epic — mountains require an epic", epicID, result.IssueType)
	}

	fmt.Printf("Validating epic structure...\n")
	fmt.Printf("  Epic: %s %q\n", epicID, result.Title)

	// Step 2: Stage — collect beads, build DAG, compute waves.
	input := &StageInput{Kind: StageInputEpic, IDs: []string{epicID}, RawArgs: []string{epicID}}
	beadList, deps, err := collectBeads(input)
	if err != nil {
		return fmt.Errorf("collect beads: %w", err)
	}

	dag := buildConvoyDAG(beadList, deps)
	errFindings := detectErrors(dag)
	warnFindings := detectWarnings(dag, input)
	allFindings := append(errFindings, warnFindings...)
	errs, warns := categorizeFindings(allFindings)

	if len(errs) > 0 {
		fmt.Fprint(os.Stderr, renderErrors(errs))
		return fmt.Errorf("mountain staging failed: %d error(s) found", len(errs))
	}

	waves, gated, err := computeWaves(dag)
	if err != nil {
		return fmt.Errorf("compute waves: %w", err)
	}

	// Add gated task warnings.
	for _, g := range gated {
		warns = append(warns, StagingFinding{
			Severity:     "warning",
			Category:     "gated",
			BeadIDs:      []string{g.TaskID},
			Message:      fmt.Sprintf("task %s is gated by non-slingable blocker(s): %s", g.TaskID, strings.Join(g.GatedBy, ", ")),
			SuggestedFix: fmt.Sprintf("close or tombstone %s to include %s in waves", strings.Join(g.GatedBy, ", "), g.TaskID),
		})
	}

	status := chooseStatus(errs, warns)

	// Count slingable tasks and epics.
	slingable := 0
	epicCount := 0
	for _, node := range dag.Nodes {
		if isSlingableType(node.Type) {
			slingable++
		}
		if node.Type == "epic" {
			epicCount++
		}
	}

	fmt.Printf("  Tasks: %d (%d slingable, %d epics)\n", len(dag.Nodes), slingable, epicCount)
	fmt.Printf("  Waves: %d (computed from blocking deps)\n", len(waves))

	// Max parallelism = largest wave.
	maxParallel := 0
	for _, w := range waves {
		if len(w.Tasks) > maxParallel {
			maxParallel = len(w.Tasks)
		}
	}
	fmt.Printf("  Max parallelism: %d\n", maxParallel)

	// Show warnings.
	if len(warns) > 0 {
		fmt.Printf("\n  Warnings:\n")
		for _, w := range warns {
			fmt.Printf("    %s\n", w.Message)
		}
	}

	if len(errs) == 0 {
		fmt.Printf("\n  Errors: none\n")
	}

	// Check status with warnings — refuse unless --force.
	if status == convoyStatusStagedWarnings && !mountainForce {
		return fmt.Errorf("staging has warnings, use --force to proceed")
	}

	// Step 3: Create the staged convoy.
	title := "Mountain: " + result.Title
	convoyID, err := createStagedConvoy(dag, waves, status, title)
	if err != nil {
		return fmt.Errorf("create convoy: %w", err)
	}

	fmt.Printf("\nCreating convoy...\n")
	fmt.Printf("  Convoy: %s %q\n", convoyID, title)
	fmt.Printf("  Label: mountain\n")

	// Step 4: Add the mountain label.
	if err := bdAddLabelTown(convoyID, "mountain"); err != nil {
		return fmt.Errorf("add mountain label: %w", err)
	}

	// Step 5: Launch — transition to open + dispatch Wave 1.
	if err := transitionConvoyToOpen(convoyID, true); err != nil {
		return fmt.Errorf("launch convoy: %w", err)
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("resolve town root: %w", err)
	}

	if err := checkBlockedRigsForLaunch(dag, townRoot, mountainForce); err != nil {
		return err
	}

	results, err := dispatchWave1(convoyID, dag, waves, townRoot)
	if err != nil {
		return fmt.Errorf("dispatch wave 1: %w", err)
	}

	// Render launch output.
	fmt.Printf("\nLaunching Wave 1 (%d tasks)...\n", len(results))

	// Sort for deterministic output.
	sort.Slice(results, func(i, j int) bool {
		return results[i].BeadID < results[j].BeadID
	})

	for _, r := range results {
		nodeTitle := ""
		if node := dag.Nodes[r.BeadID]; node != nil {
			nodeTitle = node.Title
		}
		rig := r.Rig
		if rig == "" {
			rig = "auto"
		}
		if r.Success {
			fmt.Printf("  Slung %s → %s", r.BeadID, rig)
			if nodeTitle != "" {
				fmt.Printf("  (%s)", nodeTitle)
			}
			fmt.Println()
		} else {
			fmt.Printf("  Failed %s → %s: %v\n", r.BeadID, rig, r.Error)
		}
	}

	fmt.Printf("\nMountain active. ConvoyManager will feed subsequent waves.\n")
	fmt.Printf("Deacon will audit progress every ~10 minutes.\n")
	fmt.Printf("Check status: gt mountain status %s\n", convoyID)

	return nil
}

// bdAddLabelTown adds a label to a bead in the town beads database.
func bdAddLabelTown(beadID, label string) error {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}
	cmd := issueTrackerCommand("update", beadID, "--add-label="+label)
	cmd.Dir = townBeads
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bd update %s --add-label=%s: %w\noutput: %s", beadID, label, err, out)
	}
	return nil
}

// bdRemoveLabelTown removes a label from a bead in the town beads database.
func bdRemoveLabelTown(beadID, label string) error {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}
	cmd := issueTrackerCommand("update", beadID, "--remove-label="+label)
	cmd.Dir = townBeads
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bd update %s --remove-label=%s: %w\noutput: %s", beadID, label, err, out)
	}
	return nil
}

// mountainConvoyInfo holds convoy data for mountain status display.
type mountainConvoyInfo struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Status string   `json:"status"`
	Labels []string `json:"labels"`
}

// runMountainStatus shows status for active mountains.
func runMountainStatus(cmd *cobra.Command, args []string) error {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	if len(args) == 0 {
		return showAllMountainStatus(townBeads)
	}

	return showMountainDetail(townBeads, args[0])
}

// showAllMountainStatus lists all active mountains with progress summary.
func showAllMountainStatus(townBeads string) error {
	convoys, err := findMountainConvoys(townBeads)
	if err != nil {
		return err
	}

	if len(convoys) == 0 {
		fmt.Println("No active mountains.")
		fmt.Println("Activate with: gt mountain <epic-id>")
		return nil
	}

	if mountainJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(convoys)
	}

	fmt.Println("Active Mountains:")
	for _, c := range convoys {
		// Get tracked beads for progress.
		trackedBeads, _, err := collectConvoyBeads(c.ID)
		if err != nil {
			fmt.Printf("  %s %q (error reading beads: %v)\n", c.ID, c.Title, err)
			continue
		}

		total := 0
		closed := 0
		for _, b := range trackedBeads {
			if isSlingableType(b.Type) {
				total++
				if b.Status == "closed" {
					closed++
				}
			}
		}

		pct := 0
		if total > 0 {
			pct = (closed * 100) / total
		}

		bar := renderProgressBar(pct, 20)
		fmt.Printf("  %s %q\n", c.ID, c.Title)
		fmt.Printf("    Progress: %s %d/%d (%d%%)\n", bar, closed, total, pct)
	}

	return nil
}

// showMountainDetail shows detailed status for a single mountain.
func showMountainDetail(townBeads, inputID string) error {
	// Resolve: inputID could be an epic or convoy.
	convoyID, err := resolveMountainID(townBeads, inputID)
	if err != nil {
		return err
	}

	// Get convoy info.
	showOut, err := runBdJSON(townBeads, "show", convoyID, "--json")
	if err != nil {
		return fmt.Errorf("convoy %q not found", convoyID)
	}

	var convoys []struct {
		ID     string   `json:"id"`
		Title  string   `json:"title"`
		Status string   `json:"status"`
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(showOut, &convoys); err != nil {
		return fmt.Errorf("parsing convoy data: %w", err)
	}
	if len(convoys) == 0 {
		return fmt.Errorf("convoy %q not found", convoyID)
	}

	cv := convoys[0]
	if !hasLabel(cv.Labels, "mountain") {
		return fmt.Errorf("%s is not a mountain (no mountain label)", convoyID)
	}

	// Collect tracked beads.
	trackedBeads, deps, err := collectConvoyBeads(convoyID)
	if err != nil {
		return fmt.Errorf("reading tracked beads: %w", err)
	}

	dag := buildConvoyDAG(trackedBeads, deps)
	waves, _, err := computeWaves(dag)
	if err != nil {
		return fmt.Errorf("computing waves: %w", err)
	}

	// Categorize beads.
	var completed []string
	var active []string
	var ready []string
	var skipped []string
	var blocked []string

	for _, b := range trackedBeads {
		if !isSlingableType(b.Type) {
			continue
		}
		switch {
		case b.Status == "closed":
			completed = append(completed, b.ID)
		case b.Status == "in_progress" || b.Status == "hooked":
			active = append(active, b.ID)
		case hasBeadLabel(townBeads, b.ID, "mountain:skipped"):
			skipped = append(skipped, b.ID)
		default:
			// Check if blocked by deps.
			node := dag.Nodes[b.ID]
			if node != nil && len(node.BlockedBy) > 0 {
				hasOpenBlocker := false
				for _, dep := range node.BlockedBy {
					depNode := dag.Nodes[dep]
					if depNode != nil && depNode.Status != "closed" {
						hasOpenBlocker = true
						break
					}
				}
				if hasOpenBlocker {
					blocked = append(blocked, b.ID)
				} else {
					ready = append(ready, b.ID)
				}
			} else {
				ready = append(ready, b.ID)
			}
		}
	}

	total := len(completed) + len(active) + len(ready) + len(skipped) + len(blocked)

	if mountainJSON {
		jsonOut := map[string]interface{}{
			"convoy_id": convoyID,
			"title":     cv.Title,
			"status":    cv.Status,
			"total":     total,
			"completed": len(completed),
			"active":    len(active),
			"ready":     len(ready),
			"skipped":   len(skipped),
			"blocked":   len(blocked),
			"waves":     len(waves),
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(jsonOut)
	}

	// Render output matching design spec.
	pct := 0
	if total > 0 {
		pct = (len(completed) * 100) / total
	}

	fmt.Printf("Mountain: %s %q\n", convoyID, cv.Title)
	fmt.Printf("\nProgress: %d/%d closed (%d%%)\n", len(completed), total, pct)
	fmt.Printf("Wave: %d total\n", len(waves))

	if len(completed) > 0 {
		sort.Strings(completed)
		fmt.Printf("\nCompleted (%d):\n", len(completed))
		for _, id := range completed {
			title := ""
			if node := dag.Nodes[id]; node != nil {
				title = node.Title
			}
			fmt.Printf("  ✓ %s  %s\n", id, title)
		}
	}

	if len(active) > 0 {
		sort.Strings(active)
		fmt.Printf("\nActive (%d):\n", len(active))
		for _, id := range active {
			title := ""
			if node := dag.Nodes[id]; node != nil {
				title = node.Title
			}
			fmt.Printf("  ⟳ %s  %s\n", id, title)
		}
	}

	if len(ready) > 0 {
		sort.Strings(ready)
		fmt.Printf("\nReady (%d):\n", len(ready))
		for _, id := range ready {
			title := ""
			if node := dag.Nodes[id]; node != nil {
				title = node.Title
			}
			fmt.Printf("  ○ %s  %s\n", id, title)
		}
	}

	if len(skipped) > 0 {
		sort.Strings(skipped)
		fmt.Printf("\nSkipped (%d):\n", len(skipped))
		for _, id := range skipped {
			title := ""
			if node := dag.Nodes[id]; node != nil {
				title = node.Title
			}
			fmt.Printf("  ⊘ %s  %s\n", id, title)
		}
	}

	if len(blocked) > 0 {
		sort.Strings(blocked)
		fmt.Printf("\nBlocked (%d):\n", len(blocked))
		for _, id := range blocked {
			node := dag.Nodes[id]
			title := ""
			blockers := ""
			if node != nil {
				title = node.Title
				var openBlockers []string
				for _, dep := range node.BlockedBy {
					depNode := dag.Nodes[dep]
					if depNode != nil && depNode.Status != "closed" {
						openBlockers = append(openBlockers, dep)
					}
				}
				if len(openBlockers) > 0 {
					blockers = " (needs: " + strings.Join(openBlockers, ", ") + ")"
				}
			}
			fmt.Printf("  ◌ %s  %s%s\n", id, title, blockers)
		}
	}

	return nil
}

// findMountainConvoys lists all open convoys with the mountain label.
func findMountainConvoys(townBeads string) ([]mountainConvoyInfo, error) {
	issues, err := listConvoyIssues(townBeads, "open", false, "mountain")
	if err != nil {
		return nil, fmt.Errorf("listing mountain convoys: %w", err)
	}

	convoys := make([]mountainConvoyInfo, 0, len(issues))
	for _, issue := range issues {
		convoys = append(convoys, mountainConvoyInfo{
			ID:     issue.ID,
			Title:  issue.Title,
			Labels: issue.Labels,
		})
	}

	return convoys, nil
}

// resolveMountainID resolves an epic-id or convoy-id to the mountain convoy ID.
// If the input is already a convoy with the mountain label, returns it directly.
// If the input is an epic, searches for a mountain convoy tracking it.
func resolveMountainID(townBeads, inputID string) (string, error) {
	result, err := bdShow(inputID)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %s: %w", inputID, err)
	}

	if isConvoyIssue(result.IssueType, result.Labels) {
		return inputID, nil
	}

	// Input is an epic — find a mountain convoy that tracks it.
	convoys, err := findMountainConvoys(townBeads)
	if err != nil {
		return "", err
	}

	for _, cv := range convoys {
		// Check if the convoy title starts with "Mountain: " + epic title.
		if strings.Contains(cv.Title, result.Title) {
			return cv.ID, nil
		}
	}

	return "", fmt.Errorf("no active mountain found for epic %s", inputID)
}

// hasBeadLabel checks if a bead has a specific label by querying bd show.
func hasBeadLabel(townBeads, beadID, label string) bool {
	out, err := runBdJSON(townBeads, "show", beadID, "--json")
	if err != nil {
		return false
	}

	var results []struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(out, &results); err != nil || len(results) == 0 {
		return false
	}

	return hasLabel(results[0].Labels, label)
}

// renderProgressBar renders a simple Unicode progress bar.
func renderProgressBar(pct, width int) string {
	filled := (pct * width) / 100
	if filled > width {
		filled = width
	}
	var b strings.Builder
	for i := 0; i < width; i++ {
		if i < filled {
			b.WriteRune('█')
		} else {
			b.WriteRune('░')
		}
	}
	return b.String()
}

// runMountainPause pauses an active mountain by setting the convoy to paused status.
func runMountainPause(cmd *cobra.Command, args []string) error {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	convoyID, err := resolveMountainID(townBeads, args[0])
	if err != nil {
		return err
	}

	// Add paused label — the ConvoyManager checks this to skip dispatch.
	if err := bdAddLabelTown(convoyID, "mountain:paused"); err != nil {
		return fmt.Errorf("pause mountain: %w", err)
	}

	fmt.Printf("Mountain %s paused.\n", convoyID)
	fmt.Printf("Active polecats will finish their current work.\n")
	fmt.Printf("Resume with: gt mountain resume %s\n", convoyID)
	return nil
}

// runMountainResume resumes a paused mountain.
func runMountainResume(cmd *cobra.Command, args []string) error {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	convoyID, err := resolveMountainID(townBeads, args[0])
	if err != nil {
		return err
	}

	if err := bdRemoveLabelTown(convoyID, "mountain:paused"); err != nil {
		return fmt.Errorf("resume mountain: %w", err)
	}

	fmt.Printf("Mountain %s resumed.\n", convoyID)
	fmt.Printf("ConvoyManager will continue dispatching waves.\n")
	return nil
}

// runMountainCancel cancels a mountain by removing the mountain label.
// Leaves the convoy intact for manual management.
func runMountainCancel(cmd *cobra.Command, args []string) error {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	convoyID, err := resolveMountainID(townBeads, args[0])
	if err != nil {
		return err
	}

	// Remove mountain label (and paused if present).
	if err := bdRemoveLabelTown(convoyID, "mountain"); err != nil {
		return fmt.Errorf("cancel mountain: %w", err)
	}
	// Best-effort remove paused label too.
	_ = bdRemoveLabelTown(convoyID, "mountain:paused")

	fmt.Printf("Mountain canceled on %s.\n", convoyID)
	fmt.Printf("Convoy remains open for manual management.\n")
	fmt.Printf("Check convoy status: gt convoy status %s\n", convoyID)
	return nil
}
