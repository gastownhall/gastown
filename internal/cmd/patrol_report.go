package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/atomicfile"
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
making shortcutting visible in the ledger.

Examples:
  gt patrol report --summary "All clear, no issues" --steps "heartbeat:OK,inbox-check:OK,health-scan:OK"
  gt patrol report --summary "Dolt latency elevated, filed escalation" --steps "heartbeat:OK,inbox-check:OK,health-scan:BLOCKED"`,
	RunE: runPatrolReport,
}

func init() {
	patrolReportCmd.Flags().StringVar(&patrolReportSummary, "summary", "", "Brief summary of patrol observations (required)")
	patrolReportCmd.Flags().StringVar(&patrolReportSteps, "steps", "", "Required complete step audit: comma-separated step:STATUS pairs (OK, SKIP, FAIL, or BLOCKED)")
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

	// Validate the audit before looking up or mutating the active patrol. Role
	// formulas have always required every step to be reported explicitly; fail
	// closed here so an incomplete report cannot close the cycle while claiming
	// success in the ledger.
	if err := validateStepAudit(cfg.PatrolMolName, patrolReportSteps); err != nil {
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

	// Build step audit checklist
	stepAudit := buildStepAudit(cfg.PatrolMolName, patrolReportSteps)

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
	if _, err := writePatrolReceipt(roleInfo, cfg.PatrolMolName, patrolID, patrolReportSteps, time.Now().UTC()); err != nil {
		style.PrintWarning("could not write patrol receipt: %v", err)
	}

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

// PatrolReceipt is the machine-readable evidence emitted only after a patrol
// and all of its descendants have closed successfully.
type PatrolReceipt struct {
	SchemaVersion int               `json:"schema_version"`
	Role          string            `json:"role"`
	Rig           string            `json:"rig,omitempty"`
	PatrolID      string            `json:"patrol_id"`
	Formula       string            `json:"formula"`
	CheckedAt     string            `json:"checked_at"`
	Complete      bool              `json:"complete"`
	Steps         map[string]string `json:"steps"`
}

func patrolReceiptPath(info RoleInfo) (string, error) {
	if info.TownRoot == "" {
		return "", fmt.Errorf("town root is required for patrol receipt")
	}
	name := ""
	switch info.Role {
	case RoleDeacon:
		name = "deacon.json"
	case RoleWitness, RoleRefinery:
		if info.Rig == "" || filepath.Base(info.Rig) != info.Rig || info.Rig == "." || info.Rig == ".." {
			return "", fmt.Errorf("invalid rig %q for patrol receipt", info.Rig)
		}
		name = info.Rig + "-" + string(info.Role) + ".json"
	default:
		return "", fmt.Errorf("unsupported role %q for patrol receipt", info.Role)
	}
	return filepath.Join(info.TownRoot, ".gastown", "patrol-receipts", name), nil
}

func writePatrolReceipt(info RoleInfo, formulaName, patrolID, stepsFlag string, checkedAt time.Time) (string, error) {
	path, err := patrolReceiptPath(info)
	if err != nil {
		return "", err
	}
	role := string(info.Role)
	if info.Rig != "" {
		role = info.Rig + "/" + role
	}
	receipt := PatrolReceipt{
		SchemaVersion: 1,
		Role:          role,
		Rig:           info.Rig,
		PatrolID:      patrolID,
		Formula:       formulaName,
		CheckedAt:     checkedAt.Format(time.RFC3339),
		Complete:      true,
		Steps:         parseStepResults(stepsFlag),
	}
	if err := atomicfile.EnsureDirAndWriteJSON(path, receipt); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}

// validateStepAudit requires an explicit, formula-complete patrol receipt.
// SKIP, FAIL, and BLOCKED remain valid evidence; omission does not, because an
// omitted step is indistinguishable from a patrol shortcut.
func validateStepAudit(formulaName, stepsFlag string) error {
	if strings.TrimSpace(stepsFlag) == "" {
		return fmt.Errorf("patrol step audit is required; report every formula step with --steps")
	}

	content, err := formula.GetEmbeddedFormulaContent(formulaName)
	if err != nil {
		return fmt.Errorf("loading patrol formula %s: %w", formulaName, err)
	}
	f, err := formula.Parse(content)
	if err != nil {
		return fmt.Errorf("parsing patrol formula %s: %w", formulaName, err)
	}

	allStepIDs := f.GetAllIDs()
	known := make(map[string]struct{}, len(allStepIDs))
	for _, stepID := range allStepIDs {
		known[stepID] = struct{}{}
	}

	reported := make(map[string]string, len(allStepIDs))
	validStatuses := map[string]struct{}{
		"OK":      {},
		"SKIP":    {},
		"FAIL":    {},
		"BLOCKED": {},
	}
	for _, raw := range strings.Split(stepsFlag, ",") {
		entry := strings.TrimSpace(raw)
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return fmt.Errorf("invalid patrol step entry %q; expected step:STATUS", entry)
		}
		stepID := strings.TrimSpace(parts[0])
		status := strings.ToUpper(strings.TrimSpace(parts[1]))
		if _, ok := known[stepID]; !ok {
			return fmt.Errorf("unknown patrol step %q for formula %s", stepID, formulaName)
		}
		if _, ok := validStatuses[status]; !ok {
			return fmt.Errorf("invalid patrol step status %q for %s; use OK, SKIP, FAIL, or BLOCKED", status, stepID)
		}
		if _, duplicate := reported[stepID]; duplicate {
			return fmt.Errorf("duplicate patrol step %q", stepID)
		}
		reported[stepID] = status
	}

	missing := make([]string, 0)
	for _, stepID := range allStepIDs {
		if _, ok := reported[stepID]; !ok {
			missing = append(missing, stepID)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing patrol steps: %s", strings.Join(missing, ", "))
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
func buildStepAudit(formulaName string, stepsFlag string) string {
	// Load the formula to get the canonical step list
	content, err := formula.GetEmbeddedFormulaContent(formulaName)
	if err != nil {
		if stepsFlag == "" {
			return "Steps: NOT REPORTED (formula not found)"
		}
		// Can't validate without the formula, but still show what was reported
		return fmt.Sprintf("Steps: %s (unvalidated — formula not found)", stepsFlag)
	}

	f, err := formula.Parse(content)
	if err != nil {
		if stepsFlag == "" {
			return "Steps: NOT REPORTED (formula parse error)"
		}
		return fmt.Sprintf("Steps: %s (unvalidated — formula parse error)", stepsFlag)
	}

	allStepIDs := f.GetAllIDs()
	if len(allStepIDs) == 0 {
		return ""
	}

	if stepsFlag == "" {
		return fmt.Sprintf("Steps: NOT REPORTED (?/%d)", len(allStepIDs))
	}

	// Parse the reported step results
	reported := parseStepResults(stepsFlag)

	// Build the audit line: map each formula step to its reported status
	var parts []string
	okCount := 0
	for _, stepID := range allStepIDs {
		status, ok := reported[stepID]
		if !ok {
			status = "SKIP"
		}
		if status == "OK" {
			okCount++
		}
		parts = append(parts, stepID+" "+status)
	}

	return fmt.Sprintf("Steps: %s (%d/%d)", strings.Join(parts, " | "), okCount, len(allStepIDs))
}

// parseStepResults parses a comma-separated string of step:STATUS pairs.
// Returns a map of step ID to uppercase status.
// Example input: "heartbeat:OK,inbox-check:OK,orphan-cleanup:SKIP"
func parseStepResults(stepsFlag string) map[string]string {
	results := make(map[string]string)
	for _, entry := range strings.Split(stepsFlag, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) == 2 {
			results[strings.TrimSpace(parts[0])] = strings.ToUpper(strings.TrimSpace(parts[1]))
		}
	}
	return results
}
