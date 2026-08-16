// Package style provides consistent terminal styling using Lipgloss.
// Uses the Ayu theme colors from internal/ui for semantic consistency.
package style

import (
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/jonbaldie/gastown/internal/ui"
)

var (
	// Success style for positive outcomes (green)
	Success = lipgloss.NewStyle().
		Foreground(ui.ColorPass).
		Bold(true)

	// Warning style for cautionary messages (yellow)
	Warning = lipgloss.NewStyle().
		Foreground(ui.ColorWarn).
		Bold(true)

	// Error style for failures (red)
	Error = lipgloss.NewStyle().
		Foreground(ui.ColorFail).
		Bold(true)

	// Info style for informational messages (blue)
	Info = lipgloss.NewStyle().
		Foreground(ui.ColorAccent)

	// Dim style for secondary information (gray)
	Dim = lipgloss.NewStyle().
		Foreground(ui.ColorMuted)

	// Bold style for emphasis
	Bold = lipgloss.NewStyle().
		Bold(true)

	// SuccessPrefix is the checkmark prefix for success messages
	SuccessPrefix = Success.Render(ui.IconPass)

	// WarningPrefix is the warning prefix
	WarningPrefix = Warning.Render(ui.IconWarn)

	// ErrorPrefix is the error prefix
	ErrorPrefix = Error.Render(ui.IconFail)

	// ArrowPrefix for action indicators
	ArrowPrefix = Info.Render("→")
)

// PrintWarning prints a warning message to stderr with consistent formatting.
// The format and args work like fmt.Printf.
// Writes to stderr so warnings never contaminate structured (JSON) output on stdout.
func PrintWarning(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "%s %s\n", Warning.Render(ui.IconWarn+" Warning:"), msg)
}
