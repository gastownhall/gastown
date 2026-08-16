package doctor

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jonbaldie/gastown/internal/ui"
)

// Doctor manages and executes health checks.
type Doctor struct {
	checks []Check
}

// NewDoctor creates a new Doctor with no registered checks.
func NewDoctor() *Doctor {
	return &Doctor{
		checks: make([]Check, 0),
	}
}

// Register adds a check to the doctor's check list.
func (d *Doctor) Register(check Check) {
	d.checks = append(d.checks, check)
}

// RegisterAll adds multiple checks to the doctor's check list.
func (d *Doctor) RegisterAll(checks ...Check) {
	d.checks = append(d.checks, checks...)
}

// Checks returns the list of registered checks.
func (d *Doctor) Checks() []Check {
	return d.checks
}

// categoryGetter interface for checks that provide a category
type categoryGetter interface {
	Category() string
}

// Run executes all registered checks and returns a report.
func (d *Doctor) Run(ctx *CheckContext) *Report {
	return d.RunStreaming(ctx, nil, 0)
}

// RunStreaming executes all registered checks with optional real-time output.
// If w is non-nil, prints each check name as it starts and result when done.
// If slowThreshold > 0, shows hourglass icon for slow checks.
func (d *Doctor) RunStreaming(ctx *CheckContext, w io.Writer, slowThreshold time.Duration) *Report {
	report := NewReport()

	for _, check := range d.checks {
		// Stream: print check name before running
		if w != nil {
			fmt.Fprintf(w, "  %s  %s...", ui.RenderMuted("○"), check.Name())
		}

		start := time.Now()
		result := check.Run(ctx)
		result.Elapsed = time.Since(start)

		// Ensure check name is populated
		if result.Name == "" {
			result.Name = check.Name()
		}
		// Set category from check if available
		if cg, ok := check.(categoryGetter); ok && result.Category == "" {
			result.Category = cg.Category()
		}

		// Stream: overwrite line with result
		if w != nil {
			var statusIcon string
			switch result.Status {
			case StatusOK:
				statusIcon = ui.RenderPassIcon()
			case StatusWarning:
				statusIcon = ui.RenderWarnIcon()
			case StatusError:
				statusIcon = ui.RenderFailIcon()
			}
			// Check if slow (hourglass replaces spaces to maintain alignment)
			isSlow := slowThreshold > 0 && result.Elapsed >= slowThreshold
			slowIndicator := "  "
			if isSlow {
				report.Summary.Slow++
				slowIndicator = "⏳"
			}
			fmt.Fprintf(w, "\r  %s%s%s", statusIcon, slowIndicator, result.Name)
			if result.Message != "" {
				fmt.Fprintf(w, "%s", ui.RenderMuted(" "+result.Message))
			}
			if isSlow {
				fmt.Fprintf(w, "%s", ui.RenderMuted(" ("+formatDuration(result.Elapsed)+")"))
			}
			fmt.Fprintln(w)
		}

		report.Add(result)
	}

	return report
}

// Fix runs all checks with auto-fix enabled where possible.
// It first runs the check, then if it fails and can be fixed, attempts the fix.
func (d *Doctor) Fix(ctx *CheckContext) *Report {
	return d.FixStreaming(ctx, nil, 0)
}

// safeFixCheck calls check.Fix() with panic recovery. If the Fix method panics
// (e.g., due to a Dolt nil pointer dereference propagating in-process — GH#1769),
// the panic is caught and returned as an error instead of crashing gt doctor.
func safeFixCheck(check Check, ctx *CheckContext) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("fix panicked: %v", r)
		}
	}()
	return check.Fix(ctx)
}

// FixStreaming runs all checks with auto-fix and optional real-time output.
// If w is non-nil, prints each check name as it starts and result when done.
// If slowThreshold > 0, shows hourglass icon for slow checks.
func (d *Doctor) FixStreaming(ctx *CheckContext, w io.Writer, slowThreshold time.Duration) *Report {
	report := NewReport()

	for _, check := range d.checks {
		// Stream: print check name before running
		if w != nil {
			fmt.Fprintf(w, "  %s  %s...", ui.RenderMuted("○"), check.Name())
		}

		start := time.Now()
		result := check.Run(ctx)
		if result.Name == "" {
			result.Name = check.Name()
		}
		// Set category from check if available
		if cg, ok := check.(categoryGetter); ok && result.Category == "" {
			result.Category = cg.Category()
		}

		// Attempt fix if check failed and is fixable
		if result.Status != StatusOK && check.CanFix() {
			// Stream: show the problem with fixing indicator (all on same line)
			if w != nil {
				var problemIcon string
				if result.Status == StatusError {
					problemIcon = ui.RenderFailIcon()
				} else {
					problemIcon = ui.RenderWarnIcon()
				}
				// Overwrite the "checking" line with problem status + fixing indicator
				fmt.Fprintf(w, "\r  %s  %s", problemIcon, check.Name())
				if result.Message != "" {
					fmt.Fprintf(w, "%s", ui.RenderMuted(" "+result.Message))
				}
				fmt.Fprintf(w, "%s", ui.RenderMuted(" (fixing)..."))
			}

			err := safeFixCheck(check, ctx)
			if err == nil {
				// Re-run check to verify fix worked
				result = check.Run(ctx)
				if result.Name == "" {
					result.Name = check.Name()
				}
				// Set category again after re-run
				if cg, ok := check.(categoryGetter); ok && result.Category == "" {
					result.Category = cg.Category()
				}
				// Update message to indicate fix was applied
				if result.Status == StatusOK {
					result.Message = result.Message + " (fixed)"
					result.Fixed = true
				}
			} else if errors.Is(err, ErrSkippedNoStart) {
				// Fix skipped due to --no-start flag
				result.Details = append(result.Details, "Skipped: --no-start suppresses startup")
			} else {
				// Fix failed, add error to details
				result.Details = append(result.Details, "Fix failed: "+err.Error())
			}
		}

		// Record total elapsed time including any fix attempts
		result.Elapsed = time.Since(start)

		// Stream: overwrite line with final result
		if w != nil {
			var statusIcon string
			if result.Fixed {
				statusIcon = ui.RenderFixIcon()
			} else {
				switch result.Status {
				case StatusOK:
					statusIcon = ui.RenderPassIcon()
				case StatusWarning:
					statusIcon = ui.RenderWarnIcon()
				case StatusError:
					statusIcon = ui.RenderFailIcon()
				}
			}
			// Check if slow (hourglass replaces spaces to maintain alignment)
			// Fix icon (🔧) is double-width, so use one less padding space
			isSlow := slowThreshold > 0 && result.Elapsed >= slowThreshold
			slowIndicator := "  "
			if result.Fixed {
				slowIndicator = " "
			}
			if isSlow {
				report.Summary.Slow++
				slowIndicator = "⏳"
			}
			fmt.Fprintf(w, "\r  %s%s%s", statusIcon, slowIndicator, result.Name)
			if result.Message != "" {
				fmt.Fprintf(w, "%s", ui.RenderMuted(" "+result.Message))
			}
			if isSlow {
				fmt.Fprintf(w, "%s", ui.RenderMuted(" ("+formatDuration(result.Elapsed)+")"))
			}
			fmt.Fprintln(w)
		}

		report.Add(result)
	}

	return report
}

// BaseCheck provides a base implementation for checks that don't support auto-fix.
// Embed this in custom checks to get default CanFix() and Fix() implementations.
type BaseCheck struct {
	CheckName        string
	CheckDescription string
	CheckCategory    string // Category for grouping (e.g., CategoryCore)
}

// Category returns the check's category for grouping in output.
func (b *BaseCheck) Category() string {
	return b.CheckCategory
}

// Name returns the check name.
func (b *BaseCheck) Name() string {
	return b.CheckName
}

// Description returns the check description.
func (b *BaseCheck) Description() string {
	return b.CheckDescription
}

// CanFix returns false by default.
func (b *BaseCheck) CanFix() bool {
	return false
}

// Fix returns an error indicating this check cannot be auto-fixed.
func (b *BaseCheck) Fix(ctx *CheckContext) error {
	return ErrCannotFix
}

// FixableCheck provides a base implementation for checks that support auto-fix.
// Embed this and override CanFix() to return true, and implement Fix().
type FixableCheck struct {
	BaseCheck
}

// CanFix returns true for fixable checks.
func (f *FixableCheck) CanFix() bool {
	return true
}
