package doctor

import (
	"fmt"

	"github.com/jonbaldie/gastown/internal/deps"
)

// DoltBinaryCheck verifies that the dolt binary is installed, accessible in PATH,
// and meets the minimum version requirement. Dolt is required for the beads
// storage backend (dolt sql-server).
type DoltBinaryCheck struct {
	BaseCheck
}

// NewDoltBinaryCheck creates a new dolt binary availability check.
func NewDoltBinaryCheck() *DoltBinaryCheck {
	return &DoltBinaryCheck{
		BaseCheck: BaseCheck{
			CheckName:        "dolt-binary",
			CheckDescription: "Check that dolt is installed and meets minimum version",
			CheckCategory:    CategoryInfrastructure,
		},
	}
}

// Run checks if dolt is available in PATH and reports its version status.
func (c *DoltBinaryCheck) Run(ctx *CheckContext) *CheckResult {
	status, version, detail := deps.CheckDolt()

	switch status {
	case deps.DoltOK:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("dolt %s", version),
		}

	case deps.DoltNotFound:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "dolt not found in PATH",
			Details: []string{
				"Dolt is required for the beads storage backend",
			},
			FixHint: fmt.Sprintf("Install dolt: %s", deps.DoltInstallURL),
		}

	case deps.DoltTooOld:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("dolt %s is too old (minimum: %s)", version, deps.MinDoltVersion),
			Details: []string{
				fmt.Sprintf("Installed version %s does not meet the minimum requirement of %s", version, deps.MinDoltVersion),
			},
			FixHint: fmt.Sprintf("Upgrade dolt: %s", deps.DoltInstallURL),
		}

	case deps.DoltExecFailed:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("dolt found but 'dolt version' failed: %s", detail),
			Details: []string{
				"The dolt binary exists but could not report its version",
			},
			FixHint: fmt.Sprintf("Reinstall dolt: %s", deps.DoltInstallURL),
		}

	case deps.DoltUnknown:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("dolt found but version could not be parsed: %s", detail),
			FixHint: fmt.Sprintf("Reinstall dolt: %s", deps.DoltInstallURL),
		}
	}

	// Unreachable with current DoltStatus values. Return warning to surface
	// unexpected states if a new enum value is added without updating this switch.
	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: "unexpected dolt check status",
	}
}
