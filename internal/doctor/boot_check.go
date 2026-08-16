package doctor

import (
	"fmt"
	"os"
	"time"

	"github.com/jonbaldie/gastown/internal/boot"
	"github.com/jonbaldie/gastown/internal/session"
)

// BootHealthCheck verifies Boot watchdog health.
// "The vet checks on the dog."
type BootHealthCheck struct {
	FixableCheck
	missingDir bool // track if directory is missing for Fix()
}

// NewBootHealthCheck creates a new Boot health check.
func NewBootHealthCheck() *BootHealthCheck {
	return &BootHealthCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "boot-health",
				CheckDescription: "Check Boot watchdog health (the vet checks on the dog)",
				CheckCategory:    CategoryInfrastructure,
			},
		},
	}
}

// CanFix returns true only if the directory is missing (we can create it).
func (c *BootHealthCheck) CanFix() bool {
	return c.missingDir
}

// Fix creates the missing boot directory.
func (c *BootHealthCheck) Fix(ctx *CheckContext) error {
	if !c.missingDir {
		return nil
	}
	b := boot.New(ctx.TownRoot)
	return b.EnsureDir()
}

// Run checks Boot health: directory, session, status, and marker freshness.
func (c *BootHealthCheck) Run(ctx *CheckContext) *CheckResult {
	b := boot.New(ctx.TownRoot)
	details := []string{}

	// Check 1: Boot directory exists
	bootDir := b.Dir()
	if _, err := os.Stat(bootDir); os.IsNotExist(err) {
		c.missingDir = true
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Boot directory not present",
			Details: []string{fmt.Sprintf("Expected: %s", bootDir)},
			FixHint: "Run 'gt doctor --fix' to create it",
		}
	}

	// Check 2: Session alive
	sessionAlive := b.IsSessionAlive()
	if sessionAlive {
		details = append(details, fmt.Sprintf("Session: %s (alive)", session.BootSessionName()))
	} else {
		details = append(details, fmt.Sprintf("Session: %s (not running)", session.BootSessionName()))
	}

	// Check 3: Last execution status
	status, err := b.LoadStatus()
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "Failed to load Boot status",
			Details: []string{err.Error()},
		}
	}

	if !status.CompletedAt.IsZero() {
		age := time.Since(status.CompletedAt).Round(time.Second)
		details = append(details, fmt.Sprintf("Last run: %s ago", age))
		if status.LastAction != "" {
			details = append(details, fmt.Sprintf("Last action: %s", status.LastAction))
		}
		if status.Target != "" {
			details = append(details, fmt.Sprintf("Target: %s", status.Target))
		}
		if status.Error != "" {
			details = append(details, fmt.Sprintf("Last error: %s", status.Error))
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusWarning,
				Message: "Boot last run had an error",
				Details: details,
				FixHint: "Check daemon logs for details",
			}
		}
	} else if status.StartedAt.IsZero() {
		details = append(details, "No previous run recorded")
	}

	// Check 4: Currently running (uses tmux session state per ZFC principle)
	if sessionAlive {
		details = append(details, "Currently running (tmux session active)")
	}

	// All checks passed
	message := "Boot watchdog healthy"
	if b.IsDegraded() {
		message = "Boot watchdog healthy (degraded mode)"
		details = append(details, "Running in degraded mode (no tmux)")
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: message,
		Details: details,
	}
}
