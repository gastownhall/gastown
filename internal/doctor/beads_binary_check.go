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
			FixHint: fmt.Sprintf("Install: go install %s", deps.BeadsInstallPath),
		}

	case deps.BeadsTooOld:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("bd %s is too old (minimum: %s)", version, deps.MinBeadsVersion),
			Details: []string{
				fmt.Sprintf("Installed version %s does not meet the minimum requirement of %s", version, deps.MinBeadsVersion),
			},
			FixHint: fmt.Sprintf("Upgrade: go install %s", deps.BeadsInstallPath),
		}

	case deps.BeadsModuleMismatch:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("bd module %s does not match required %s", version, deps.RequiredBeadsModuleVersion),
			Details: []string{
				fmt.Sprintf("Installed bd was built from Beads module %s; Gas Town requires %s", version, deps.RequiredBeadsModuleVersion),
			},
			FixHint: fmt.Sprintf("Upgrade: go install %s", deps.BeadsInstallPath),
		}

	case deps.BeadsUnknown:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "bd found but version or module provenance could not be verified",
			FixHint: fmt.Sprintf("Try reinstalling: go install %s", deps.BeadsInstallPath),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "bd available",
	}
}
