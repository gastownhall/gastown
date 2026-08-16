// Package doctor provides a framework for running health checks on Gas Town workspaces.
package doctor

import (
	"fmt"
	"io"
	"time"

	"github.com/jonbaldie/gastown/internal/ui"
)

// Category constants for grouping checks
const (
	CategoryCore           = "Core"
	CategoryInfrastructure = "Infrastructure"
	CategoryRig            = "Rig"
	CategoryPatrol         = "Patrol"
	CategoryConfig         = "Configuration"
	CategoryCleanup        = "Cleanup"
	CategoryHooks          = "Hooks"
)

// CategoryOrder defines the display order for categories
var CategoryOrder = []string{
	CategoryCore,
	CategoryInfrastructure,
	CategoryRig,
	CategoryPatrol,
	CategoryConfig,
	CategoryCleanup,
	CategoryHooks,
}

// CheckStatus represents the result status of a health check.
type CheckStatus int

const (
	// StatusOK indicates the check passed.
	StatusOK CheckStatus = iota
	// StatusWarning indicates a non-critical issue.
	StatusWarning
	// StatusError indicates a critical problem.
	StatusError
)

// String returns a human-readable status.
func (s CheckStatus) String() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusWarning:
		return "Warning"
	case StatusError:
		return "Error"
	default:
		return "Unknown"
	}
}

// CheckContext provides context for running checks.
type CheckContext struct {
	TownRoot        string // Root directory of the Gas Town workspace
	RigName         string // Rig name (empty for town-level checks)
	Verbose         bool   // Enable verbose output
	RestartSessions bool   // Restart patrol sessions when fixing (requires explicit --restart-sessions flag)
	NoStart         bool   // Suppress starting daemon/agents during --fix
}

// RigPath returns the full path to the rig directory.
// Returns empty string if RigName is not set.
func (ctx *CheckContext) RigPath() string {
	if ctx.RigName == "" {
		return ""
	}
	return ctx.TownRoot + "/" + ctx.RigName
}

// DefaultSlowThreshold is the default duration above which a check is considered slow.
const DefaultSlowThreshold = 1 * time.Second

// CheckResult represents the outcome of a health check.
type CheckResult struct {
	Name     string        // Check name
	Status   CheckStatus   // Result status
	Message  string        // Primary result message
	Details  []string      // Additional information
	FixHint  string        // Suggestion if not auto-fixable
	Category string        // Category for grouping (e.g., CategoryCore)
	Elapsed  time.Duration // How long the check took to run
	Fixed    bool          // True if this check was auto-fixed
}

// Check defines the interface for a health check.
type Check interface {
	// Name returns the check identifier.
	Name() string

	// Description returns a human-readable description.
	Description() string

	// Run executes the check and returns a result.
	Run(ctx *CheckContext) *CheckResult

	// Fix attempts to automatically fix the issue.
	// Should only be called if CanFix() returns true.
	Fix(ctx *CheckContext) error

	// CanFix returns true if this check can automatically fix issues.
	CanFix() bool
}

// ReportSummary summarizes the results of all checks.
type ReportSummary struct {
	Total       int
	OK          int
	Warnings    int
	Errors      int
	Fixed       int           // Checks that were auto-fixed
	Slow        int           // Checks that took longer than threshold (counted during Print)
	SlowestName string        // Name of the slowest check
	SlowestTime time.Duration // Duration of the slowest check
}

// Report contains all check results and a summary.
type Report struct {
	Timestamp time.Time
	Checks    []*CheckResult
	Summary   ReportSummary
}

// NewReport creates an empty report with the current timestamp.
func NewReport() *Report {
	return &Report{
		Timestamp: time.Now(),
		Checks:    make([]*CheckResult, 0),
	}
}

// Add adds a check result to the report and updates the summary.
func (r *Report) Add(result *CheckResult) {
	r.Checks = append(r.Checks, result)
	r.Summary.Total++

	switch result.Status {
	case StatusOK:
		r.Summary.OK++
	case StatusWarning:
		r.Summary.Warnings++
	case StatusError:
		r.Summary.Errors++
	}

	// Track fixed checks
	if result.Fixed {
		r.Summary.Fixed++
	}

	// Track the slowest check
	if result.Elapsed > r.Summary.SlowestTime {
		r.Summary.SlowestName = result.Name
		r.Summary.SlowestTime = result.Elapsed
	}
}

// HasErrors returns true if any check reported an error.
func (r *Report) HasErrors() bool {
	return r.Summary.Errors > 0
}

// HasWarnings returns true if any check reported a warning.
func (r *Report) HasWarnings() bool {
	return r.Summary.Warnings > 0
}

// IsHealthy returns true if all checks passed without errors or warnings.
func (r *Report) IsHealthy() bool {
	return r.Summary.Errors == 0 && r.Summary.Warnings == 0
}

// PrintSummaryOnly outputs just the summary and warnings section.
// Used after streaming output where checks were already printed as they ran.
// Slow checks are already counted during streaming, so slowThreshold is only
// used for the summary display.
func (r *Report) PrintSummaryOnly(w io.Writer, verbose bool, slowThreshold time.Duration) {
	// Collect warnings/errors for summary section
	var warnings []*CheckResult
	for _, check := range r.Checks {
		if check.Status != StatusOK {
			warnings = append(warnings, check)
		}
	}

	// Print separator and summary
	_, _ = fmt.Fprintln(w, ui.RenderSeparator())
	r.printSummary(w, slowThreshold)

	// Print warnings/errors section with fixes
	r.printWarningsSection(w, warnings)

	// Print details for non-OK checks in verbose mode
	if verbose && len(warnings) > 0 {
		for _, check := range warnings {
			if len(check.Details) > 0 {
				for _, detail := range check.Details {
					_, _ = fmt.Fprintf(w, "     %s%s\n", ui.MutedStyle.Render(ui.TreeLast), ui.RenderMuted(detail))
				}
			}
		}
	}
}

// Print outputs the report to the given writer.
// Matches bd doctor UX: grouped by category, semantic icons, warnings section.
// If slowThreshold > 0, displays elapsed time for checks exceeding the threshold.
func (r *Report) Print(w io.Writer, verbose bool, slowThreshold time.Duration) {
	// Print header with version placeholder (caller should set via PrintWithVersion)
	_, _ = fmt.Fprintln(w)

	// Group checks by category
	checksByCategory := make(map[string][]*CheckResult)
	for _, check := range r.Checks {
		cat := check.Category
		if cat == "" {
			cat = "Other"
		}
		checksByCategory[cat] = append(checksByCategory[cat], check)
	}

	// Track warnings/errors for summary section
	var warnings []*CheckResult

	// Print checks by category in defined order
	for _, category := range CategoryOrder {
		checks, exists := checksByCategory[category]
		if !exists || len(checks) == 0 {
			continue
		}

		// Print category header
		_, _ = fmt.Fprintln(w, ui.RenderCategory(category))

		// Print each check in this category
		for _, check := range checks {
			r.printCheck(w, check, verbose, slowThreshold)
			if check.Status != StatusOK {
				warnings = append(warnings, check)
			}
		}
		_, _ = fmt.Fprintln(w)
	}

	// Print any checks without a category
	if otherChecks, exists := checksByCategory["Other"]; exists && len(otherChecks) > 0 {
		_, _ = fmt.Fprintln(w, ui.RenderCategory("Other"))
		for _, check := range otherChecks {
			r.printCheck(w, check, verbose, slowThreshold)
			if check.Status != StatusOK {
				warnings = append(warnings, check)
			}
		}
		_, _ = fmt.Fprintln(w)
	}

	// Print separator and summary
	_, _ = fmt.Fprintln(w, ui.RenderSeparator())
	r.printSummary(w, slowThreshold)

	// Print warnings/errors section with fixes
	r.printWarningsSection(w, warnings)
}

// printCheck outputs a single check result with semantic styling.
func (r *Report) printCheck(w io.Writer, check *CheckResult, verbose bool, slowThreshold time.Duration) {
	var statusIcon string
	switch check.Status {
	case StatusOK:
		statusIcon = ui.RenderPassIcon()
	case StatusWarning:
		statusIcon = ui.RenderWarnIcon()
	case StatusError:
		statusIcon = ui.RenderFailIcon()
	}

	// Add hourglass for slow checks (only when --slow is enabled)
	isSlow := slowThreshold > 0 && check.Elapsed >= slowThreshold
	if isSlow {
		r.Summary.Slow++ // Count slow checks during print
	}

	// Print check line: icon + name + muted message + optional timing
	// For slow checks, hourglass replaces spaces to maintain alignment
	slowIndicator := "  "
	if isSlow {
		slowIndicator = "⏳"
	}
	_, _ = fmt.Fprintf(w, "  %s%s%s", statusIcon, slowIndicator, check.Name)
	if check.Message != "" {
		_, _ = fmt.Fprintf(w, "%s", ui.RenderMuted(" "+check.Message))
	}
	if isSlow {
		_, _ = fmt.Fprintf(w, "%s", ui.RenderMuted(" ("+formatDuration(check.Elapsed)+")"))
	}
	_, _ = fmt.Fprintln(w)

	// Print details in verbose mode or for non-OK results (with tree connector)
	if len(check.Details) > 0 && (verbose || check.Status != StatusOK) {
		for _, detail := range check.Details {
			_, _ = fmt.Fprintf(w, "     %s%s\n", ui.MutedStyle.Render(ui.TreeLast), ui.RenderMuted(detail))
		}
	}
}

// formatDuration formats a duration in a human-readable way.
// Examples: "1.2s", "45s", "1m 30s", "2h 5m"
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

// printSummary outputs the summary line with semantic icons.
func (r *Report) printSummary(w io.Writer, slowThreshold time.Duration) {
	summary := fmt.Sprintf("%s %d passed  %s %d warnings  %s %d failed",
		ui.RenderPassIcon(), r.Summary.OK,
		ui.RenderWarnIcon(), r.Summary.Warnings,
		ui.RenderFailIcon(), r.Summary.Errors,
	)
	if r.Summary.Fixed > 0 {
		summary += fmt.Sprintf("  🔧 %d fixed", r.Summary.Fixed)
	}
	if slowThreshold > 0 && r.Summary.Slow > 0 {
		summary += fmt.Sprintf("  ⏳ %d slow (slowest: %s %s)",
			r.Summary.Slow,
			r.Summary.SlowestName,
			formatDuration(r.Summary.SlowestTime),
		)
	}
	_, _ = fmt.Fprintln(w, summary)
}

// printWarningsSection outputs separate sections for failures, warnings, and fixed items.
func (r *Report) printWarningsSection(w io.Writer, issues []*CheckResult) {
	// Separate into categories
	var failures, warnings, fixed []*CheckResult
	for _, check := range issues {
		if check.Fixed {
			fixed = append(fixed, check)
		} else if check.Status == StatusError {
			failures = append(failures, check)
		} else {
			warnings = append(warnings, check)
		}
	}

	// Also collect fixed items from all checks (not just issues)
	for _, check := range r.Checks {
		if check.Fixed && check.Status == StatusOK {
			fixed = append(fixed, check)
		}
	}

	// If nothing to report, show success message
	if len(failures) == 0 && len(warnings) == 0 && len(fixed) == 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, ui.RenderPass(ui.IconPass+" All checks passed"))
		return
	}

	// Print FAILURES section
	if len(failures) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, ui.RenderFail(ui.IconFail+"  FAILURES"))
		for i, check := range failures {
			line := fmt.Sprintf("%s: %s", check.Name, check.Message)
			_, _ = fmt.Fprintf(w, "  %s  %s %s\n", ui.RenderFailIcon(), ui.RenderFail(fmt.Sprintf("%d.", i+1)), ui.RenderFail(line))
			if check.FixHint != "" {
				_, _ = fmt.Fprintf(w, "        %s%s\n", ui.MutedStyle.Render(ui.TreeLast), check.FixHint)
			}
		}
	}

	// Print WARNINGS section
	if len(warnings) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, ui.RenderWarn(ui.IconWarn+"  WARNINGS"))
		for i, check := range warnings {
			line := fmt.Sprintf("%s: %s", check.Name, check.Message)
			_, _ = fmt.Fprintf(w, "  %s  %s %s\n", ui.RenderWarnIcon(), ui.RenderWarn(fmt.Sprintf("%d.", i+1)), line)
			if check.FixHint != "" {
				_, _ = fmt.Fprintf(w, "        %s%s\n", ui.MutedStyle.Render(ui.TreeLast), check.FixHint)
			}
		}
	}

	// Print FIXED section
	if len(fixed) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, ui.RenderPass("🔧  FIXED"))
		for i, check := range fixed {
			line := fmt.Sprintf("%s: %s", check.Name, check.Message)
			_, _ = fmt.Fprintf(w, "  %s  %s %s\n", ui.RenderPassIcon(), ui.RenderMuted(fmt.Sprintf("%d.", i+1)), ui.RenderMuted(line))
		}
	}

	// If only fixed items, show success message
	if len(failures) == 0 && len(warnings) == 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, ui.RenderPass(ui.IconPass+" All remaining checks passed"))
	}
}
