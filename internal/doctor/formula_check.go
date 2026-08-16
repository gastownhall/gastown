package doctor

import (
	"fmt"
	"strings"

	"github.com/jonbaldie/gastown/internal/formula"
)

// FormulaCheck verifies that embedded formulas are up-to-date.
// It detects outdated formulas (binary updated), missing formulas (user deleted),
// and modified formulas (user customized). Can auto-fix outdated and missing.
type FormulaCheck struct {
	FixableCheck
}

// NewFormulaCheck creates a new formula check.
func NewFormulaCheck() *FormulaCheck {
	return &FormulaCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "formulas",
				CheckDescription: "Check embedded formulas are up-to-date",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// Run checks if formulas need updating.
func (c *FormulaCheck) Run(ctx *CheckContext) *CheckResult {
	report, err := formula.CheckFormulaHealth(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("Could not check formulas: %v", err),
		}
	}

	// All good
	if report.Outdated == 0 && report.Missing == 0 && report.Modified == 0 && report.New == 0 && report.Untracked == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("%d formulas up-to-date", report.OK),
		}
	}

	// Build details
	var details []string
	var needsFix bool

	for _, f := range report.Formulas {
		switch f.Status {
		case "outdated":
			details = append(details, fmt.Sprintf("  %s: update available", f.Name))
			needsFix = true
		case "missing":
			details = append(details, fmt.Sprintf("  %s: missing (will reinstall)", f.Name))
			needsFix = true
		case "modified":
			details = append(details, fmt.Sprintf("  %s: locally modified (skipping)", f.Name))
		case "new":
			details = append(details, fmt.Sprintf("  %s: new formula available", f.Name))
			needsFix = true
		case "untracked":
			details = append(details, fmt.Sprintf("  %s: untracked (will update)", f.Name))
			needsFix = true
		}
	}

	// Determine status
	status := StatusOK
	if needsFix {
		status = StatusWarning
	}

	// Build message
	var parts []string
	if report.Outdated > 0 {
		parts = append(parts, fmt.Sprintf("%d outdated", report.Outdated))
	}
	if report.Missing > 0 {
		parts = append(parts, fmt.Sprintf("%d missing", report.Missing))
	}
	if report.New > 0 {
		parts = append(parts, fmt.Sprintf("%d new", report.New))
	}
	if report.Untracked > 0 {
		parts = append(parts, fmt.Sprintf("%d untracked", report.Untracked))
	}
	if report.Modified > 0 {
		parts = append(parts, fmt.Sprintf("%d modified", report.Modified))
	}

	message := fmt.Sprintf("Formulas: %s", strings.Join(parts, ", "))

	result := &CheckResult{
		Name:    c.Name(),
		Status:  status,
		Message: message,
		Details: details,
	}

	if needsFix {
		result.FixHint = "Run 'gt doctor --fix' to update formulas"
	}

	return result
}

// Fix updates outdated and missing formulas.
func (c *FormulaCheck) Fix(ctx *CheckContext) error {
	updated, skipped, reinstalled, err := formula.UpdateFormulas(ctx.TownRoot)
	if err != nil {
		return err
	}

	// Log what was done (caller will re-run check to show new status)
	if updated > 0 || reinstalled > 0 || skipped > 0 {
		// The doctor framework will re-run the check after fix
		// so we don't need to log here
		_ = updated
		_ = reinstalled
		_ = skipped
	}

	return nil
}
