package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/formula"
	"github.com/steveyegge/gastown/internal/style"
)

var (
	patrolReportSummary string
	patrolReportSteps   string
)

var patrolReportCmd = &cobra.Command{
	Use:   "report",
	Short: "Close patrol cycle with summary and start next cycle",
	Long: `Close the current patrol cycle, recording a summary of observations,
then automatically start a new patrol cycle.

This replaces the old squash+new pattern with a single command that:
  1. Closes the current patrol root wisp with the summary
  2. Creates a new patrol wisp for the next cycle

The summary is stored on the patrol root wisp for audit purposes.
The --steps flag records which patrol steps were executed vs skipped,
making shortcutting visible in the ledger. Step ids must match the patrol
formula's ids exactly; an unrecognized id is rejected rather than recorded,
because the ledger entry it produces cannot be corrected afterwards.

Examples:
  gt patrol report --summary "All clear, no issues" --steps "heartbeat:OK,inbox-check:OK,health-scan:OK"
  gt patrol report --summary "Dolt latency elevated, filed escalation"`,
	RunE: runPatrolReport,
}

func init() {
	patrolReportCmd.Flags().StringVar(&patrolReportSummary, "summary", "", "Brief summary of patrol observations (required)")
	patrolReportCmd.Flags().StringVar(&patrolReportSteps, "steps", "", "Step audit: comma-separated step:STATUS pairs using the formula's step ids verbatim (e.g., heartbeat:OK,inbox-check:OK)")
	_ = patrolReportCmd.MarkFlagRequired("summary")
}

func runPatrolReport(cmd *cobra.Command, args []string) error {
	// Resolve role
	roleInfo, err := GetRole()
	if err != nil {
		return fmt.Errorf("detecting role: %w", err)
	}

	roleName := string(roleInfo.Role)

	// Build config based on role
	var cfg PatrolConfig
	switch roleInfo.Role {
	case RoleDeacon:
		cfg = PatrolConfig{
			RoleName:      "deacon",
			PatrolMolName: constants.MolDeaconPatrol,
			BeadsDir:      roleInfo.TownRoot,
			Assignee:      "deacon",
		}
	case RoleWitness:
		cfg = PatrolConfig{
			RoleName:      "witness",
			PatrolMolName: constants.MolWitnessPatrol,
			BeadsDir:      roleInfo.TownRoot,
			Assignee:      roleInfo.Rig + "/witness",
		}
	case RoleRefinery:
		cfg = PatrolConfig{
			RoleName:      "refinery",
			PatrolMolName: constants.MolRefineryPatrol,
			BeadsDir:      roleInfo.TownRoot,
			Assignee:      roleInfo.Rig + "/refinery",
			ExtraVars:     buildRefineryPatrolVars(roleInfo),
		}
	default:
		return fmt.Errorf("unsupported role for patrol report: %q", roleName)
	}

	// Build the step audit checklist before touching any state: an unknown step
	// id is a typo in the audit, and a patrol report is a permanent ledger entry
	// that cannot be corrected after the fact (hq-6jz).
	stepAudit, err := buildStepAudit(cfg.PatrolMolName, patrolReportSteps)
	if err != nil {
		return err
	}

	// Find the active patrol
	patrolID, _, hasPatrol, findErr := findActivePatrol(cfg)
	if findErr != nil {
		return fmt.Errorf("finding active patrol: %w", findErr)
	}
	if !hasPatrol {
		return fmt.Errorf("no active patrol found for %s", cfg.RoleName)
	}

	// Close the current patrol root with the summary
	b := cfg.Beads
	if b == nil {
		b = beads.New(cfg.BeadsDir)
	}

	// Update the description with the patrol summary and step audit
	desc := fmt.Sprintf("Patrol report: %s\n\n%s", patrolReportSummary, stepAudit)
	if err := b.Update(patrolID, beads.UpdateOptions{
		Description: &desc,
	}); err != nil {
		style.PrintWarning("could not update patrol summary: %v", err)
	}

	// Print the step audit for visibility
	fmt.Println(stepAudit)

	// Close all descendant wisps first (recursive), then the patrol root.
	// Without this, every patrol cycle leaks ~10 orphan wisps into the DB.
	// If descendants can't be closed, abort so patrol retries next cycle (gt-7lx3).
	closed, closeDescErr := forceCloseDescendants(b, patrolID)
	if closeDescErr != nil {
		return fmt.Errorf("closing descendants of patrol %s (closed %d): %w", patrolID, closed, closeDescErr)
	}

	// Close the patrol root
	if err := b.ForceCloseWithReason("patrol cycle complete: "+patrolReportSummary, patrolID); err != nil {
		return fmt.Errorf("closing patrol %s: %w", patrolID, err)
	}

	fmt.Printf("%s Closed patrol %s\n", style.Success.Render("✓"), patrolID)

	// Start next cycle
	newPatrolID, err := autoSpawnPatrol(cfg)
	if err != nil {
		if newPatrolID != "" {
			fmt.Fprintf(os.Stderr, "warning: %s\n", err.Error())
			fmt.Printf("New patrol: %s\n", newPatrolID)
			return nil
		}
		return fmt.Errorf("starting next patrol cycle: %w", err)
	}

	fmt.Printf("%s Started new patrol: %s\n", style.Success.Render("✓"), newPatrolID)
	if cfg.RoleName == "deacon" {
		stampDeaconHeartbeatOnReport(cfg.BeadsDir, patrolReportSummary)
	}
	return nil
}

func stampDeaconHeartbeatOnReport(townRoot, summary string) {
	paused, _, err := deacon.IsPaused(townRoot)
	if err != nil {
		style.PrintWarning("not stamping deacon heartbeat: pause state unreadable: %v", err)
		return
	}
	if paused {
		return
	}

	action := "patrol report"
	if summary = strings.TrimSpace(summary); summary != "" {
		action += ": " + summary
	}
	if err := syncDeaconHeartbeatStores(townRoot, action); err != nil {
		style.PrintWarning("could not stamp deacon heartbeat: %v", err)
	}
}

// buildStepAudit builds a step checklist from the formula's steps and the
// reported step results. Format:
//
//	Steps: heartbeat OK | inbox-check OK | orphan-cleanup SKIP | ... (14/25)
//
// If stepsFlag is empty, returns a line indicating the audit was not reported.
//
// Reported step ids that don't appear in the formula are an error: silently
// dropping them would record every step they were meant to cover as SKIP,
// writing a false low-compliance patrol to the permanent ledger (hq-6jz).
func buildStepAudit(formulaName string, stepsFlag string) (string, error) {
	// Load the formula to get the canonical step list
	content, err := formula.GetEmbeddedFormulaContent(formulaName)
	if err != nil {
		if stepsFlag == "" {
			return "Steps: NOT REPORTED (formula not found)", nil
		}
		// Can't validate without the formula, but still show what was reported
		return fmt.Sprintf("Steps: %s (unvalidated — formula not found)", stepsFlag), nil
	}

	f, err := formula.Parse(content)
	if err != nil {
		if stepsFlag == "" {
			return "Steps: NOT REPORTED (formula parse error)", nil
		}
		return fmt.Sprintf("Steps: %s (unvalidated — formula parse error)", stepsFlag), nil
	}

	allStepIDs := f.GetAllIDs()
	if len(allStepIDs) == 0 {
		return "", nil
	}

	if stepsFlag == "" {
		return fmt.Sprintf("Steps: NOT REPORTED (?/%d)", len(allStepIDs)), nil
	}

	// Parse the reported step results
	reported := parseStepResults(stepsFlag)

	known := make(map[string]bool, len(allStepIDs))
	for _, stepID := range allStepIDs {
		known[stepID] = true
	}
	var rejected []string
	for _, stepID := range reported.order {
		if !known[stepID] {
			rejected = append(rejected, stepID)
		}
	}
	rejected = append(rejected, reported.malformed...)
	if len(rejected) > 0 {
		return "", fmt.Errorf("unrecognized entries in --steps: %s\n\nvalid step ids for %s:\n  %s\n\nUse these ids verbatim, as id:STATUS pairs. An entry that doesn't match is not recorded, "+
			"and its step lands in the permanent patrol ledger as SKIP",
			strings.Join(rejected, ", "), formulaName, strings.Join(allStepIDs, "\n  "))
	}

	// Build the audit line: map each formula step to its reported status
	var parts []string
	okCount := 0
	for _, stepID := range allStepIDs {
		status, ok := reported.status[stepID]
		if !ok {
			status = "SKIP"
		}
		if status == "OK" {
			okCount++
		}
		parts = append(parts, stepID+" "+status)
	}

	return fmt.Sprintf("Steps: %s (%d/%d)", strings.Join(parts, " | "), okCount, len(allStepIDs)), nil
}

// stepReport is the parsed form of the --steps flag.
type stepReport struct {
	// status maps step ID to uppercase status.
	status map[string]string
	// order lists the reported step IDs in input order, deduplicated, so
	// diagnostics echo them back the way the caller wrote them.
	order []string
	// malformed holds entries with no "id:STATUS" separator. They carry no
	// status, so they can only be reported, never recorded.
	malformed []string
}

// parseStepResults parses a comma-separated string of step:STATUS pairs.
// Example input: "heartbeat:OK,inbox-check:OK,orphan-cleanup:SKIP"
func parseStepResults(stepsFlag string) stepReport {
	report := stepReport{status: make(map[string]string)}
	for _, entry := range strings.Split(stepsFlag, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) != 2 {
			report.malformed = append(report.malformed, entry)
			continue
		}
		id := strings.TrimSpace(parts[0])
		if _, seen := report.status[id]; !seen {
			report.order = append(report.order, id)
		}
		report.status[id] = strings.ToUpper(strings.TrimSpace(parts[1]))
	}
	return report
}
