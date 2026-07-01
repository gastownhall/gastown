package doctor

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/deps"
)

// BeadsBinaryCheck verifies that the selected issue tracker binary is installed
// and meets the minimum version requirement. This is an informational check
// with no auto-fix — the user must install or upgrade the CLI manually.
type BeadsBinaryCheck struct {
	BaseCheck
}

// NewBeadsBinaryCheck creates a new beads binary version check.
func NewBeadsBinaryCheck() *BeadsBinaryCheck {
	return &BeadsBinaryCheck{
		BaseCheck: BaseCheck{
			CheckName:        "beads-binary",
			CheckDescription: "Check that the issue tracker CLI is installed and meets minimum version",
			CheckCategory:    CategoryInfrastructure,
		},
	}
}

// Run checks if the selected issue tracker CLI is available in PATH and reports
// its version status.
func (c *BeadsBinaryCheck) Run(ctx *CheckContext) *CheckResult {
	backend := deps.EffectiveIssueTrackerBackend(ctx.TownRoot)
	cliName := deps.IssueTrackerCommandName(backend)
	status, version := deps.CheckBeadsForBackend(backend)

	switch status {
	case deps.BeadsOK:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("%s %s", cliName, version),
		}

	case deps.BeadsNotFound:
		if backend == deps.IssueTrackerBackendMinibeads {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusError,
				Message: "minibeads (mb) not found in PATH",
				Details: []string{
					"The mb CLI is required for minibeads operations",
				},
				FixHint: "Build minibeads and ensure mb is on PATH",
			}
		}
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

	case deps.BeadsUnknown:
		if backend == deps.IssueTrackerBackendMinibeads {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusWarning,
				Message: "mb found but version could not be determined",
				FixHint: "Check that mb version prints: mb version X.Y.Z",
			}
		}
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "bd found but version could not be determined",
			FixHint: fmt.Sprintf("Try reinstalling: go install %s", deps.BeadsInstallPath),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("%s available", cliName),
	}
}
