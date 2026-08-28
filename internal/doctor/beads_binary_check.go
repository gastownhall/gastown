package doctor

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/deps"
)

// BeadsBinaryCheck verifies that the beads (bd) binary is installed and meets
// the minimum version requirement. This is an informational check with no
// auto-fix — the user must install or upgrade bd manually.
type BeadsBinaryCheck struct {
	BaseCheck
}

// NewBeadsBinaryCheck creates a new beads binary version check.
func NewBeadsBinaryCheck() *BeadsBinaryCheck {
	return &BeadsBinaryCheck{
		BaseCheck: BaseCheck{
			CheckName:        "beads-binary",
			CheckDescription: "Check that beads (bd) is installed and meets minimum version",
			CheckCategory:    CategoryInfrastructure,
		},
	}
}

// Run checks if bd is available in PATH and reports its version status.
func (c *BeadsBinaryCheck) Run(ctx *CheckContext) *CheckResult {
	status, version := deps.CheckBeads()

	switch status {
	case deps.BeadsOK:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("bd %s", version),
		}

	case deps.BeadsNotFound:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "beads (bd) not found in PATH",
			Details: []string{
				"The bd CLI is required for beads operations",
			},
			FixHint: "Install: " + deps.BeadsInstallHint,
		}

	case deps.BeadsTooOld:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("bd %s is too old (minimum: %s)", version, deps.MinBeadsVersion),
			Details: []string{
				fmt.Sprintf("Installed version %s does not meet the minimum requirement of %s", version, deps.MinBeadsVersion),
			},
			FixHint: "Upgrade: " + deps.BeadsInstallHint,
		}

	case deps.BeadsWrongDistribution:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("bd %s is not the governed downstream distribution", version),
			FixHint: "Replace it: " + deps.BeadsInstallHint,
		}

	case deps.BeadsUnknown:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "bd found but version could not be determined",
			FixHint: "Try reinstalling: " + deps.BeadsInstallHint,
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "bd available",
	}
}
