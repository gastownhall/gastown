package doctor

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/version"
)

// StaleBinaryCheck verifies the installed gt binary is up to date with the repo.
type StaleBinaryCheck struct {
	FixableCheck
}

// NewStaleBinaryCheck creates a new stale binary check.
func NewStaleBinaryCheck() *StaleBinaryCheck {
	return &StaleBinaryCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "stale-binary",
				CheckDescription: "Check if gt binary is up to date with repo",
				CheckCategory:    CategoryInfrastructure,
			},
		},
	}
}

// Run checks if the binary is stale.
func (c *StaleBinaryCheck) Run(ctx *CheckContext) *CheckResult {
	repoRoot, err := version.GetRepoRoot()
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Cannot locate gt source repo (not a development environment)",
			Details: []string{err.Error()},
		}
	}

	return staleResult(c.Name(), version.CheckStaleBinary(repoRoot))
}

// staleResult maps a completed staleness check to a doctor CheckResult.
// It is pure (no git/env access) so it can be unit-tested directly; Run only
// handles repo resolution before delegating here.
func staleResult(name string, info *version.StaleBinaryInfo) *CheckResult {
	if info.Error != nil {
		return &CheckResult{
			Name:    name,
			Status:  StatusOK,
			Message: "Cannot determine binary version (dev build?)",
			Details: []string{info.Error.Error()},
		}
	}

	if info.Skipped {
		return &CheckResult{
			Name:    name,
			Status:  StatusOK,
			Message: "Binary staleness check skipped",
			Details: []string{info.SkipReason},
		}
	}

	if info.IsStale {
		return &CheckResult{
			Name:    name,
			Status:  StatusWarning,
			Message: info.Describe("Binary"),
			FixHint: "Run 'gt install' to rebuild and install",
		}
	}

	return &CheckResult{
		Name:    name,
		Status:  StatusOK,
		Message: fmt.Sprintf("Binary is up to date (%s)", version.ShortCommit(info.BinaryCommit)),
	}
}

// Fix rebuilds and installs gt.
func (c *StaleBinaryCheck) Fix(ctx *CheckContext) error {
	// Note: We don't auto-fix this because:
	// 1. It requires building and installing, which takes time
	// 2. It modifies system files outside the workspace
	// 3. User should explicitly run 'gt install'
	return fmt.Errorf("run 'gt install' manually to rebuild")
}

// CanFix returns false - stale binary should be fixed manually.
func (c *StaleBinaryCheck) CanFix() bool {
	return false
}
