package doctor

import (
	"os"
	"os/exec"
	"time"

	"github.com/jonbaldie/gastown/internal/daemon"
)

// DaemonCheck verifies the daemon is running.
type DaemonCheck struct {
	FixableCheck
}

// NewDaemonCheck creates a new daemon check.
func NewDaemonCheck() *DaemonCheck {
	return &DaemonCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "daemon",
				CheckDescription: "Check if Gas Town daemon is running",
				CheckCategory:    CategoryInfrastructure,
			},
		},
	}
}

// Run checks if the daemon is running.
func (c *DaemonCheck) Run(ctx *CheckContext) *CheckResult {
	running, pid, err := daemon.IsRunning(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "Failed to check daemon status",
			Details: []string{err.Error()},
		}
	}

	if running {
		// Get more info about daemon state
		state, err := daemon.LoadState(ctx.TownRoot)
		details := []string{}
		if err == nil && !state.StartedAt.IsZero() {
			uptime := time.Since(state.StartedAt).Round(time.Second)
			details = append(details, "Uptime: "+uptime.String())
			if state.HeartbeatCount > 0 {
				details = append(details, "Heartbeats: "+string(rune(state.HeartbeatCount)))
			}
		}

		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Daemon is running (PID " + itoa(pid) + ")",
			Details: details,
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: "Daemon is not running",
		FixHint: "Run 'gt daemon start' or 'gt doctor --fix'",
	}
}

// Fix starts the daemon.
func (c *DaemonCheck) Fix(ctx *CheckContext) error {
	if ctx.NoStart {
		return ErrSkippedNoStart
	}

	// Find gt executable
	gtPath, err := os.Executable()
	if err != nil {
		return err
	}

	// Start daemon in background (detach from parent I/O - daemon uses its own logging)
	cmd := exec.Command(gtPath, "daemon", "run")
	cmd.Dir = ctx.TownRoot
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return err
	}

	// Wait a moment for daemon to initialize
	time.Sleep(300 * time.Millisecond)

	return nil
}

// itoa is a simple int to string helper
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}
