package doctor

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/hooks"
)

// HooksBaseCheck warns when ~/.gt/hooks-base.json is missing.
// Without this file, gt hooks diff has no reference point and cannot detect
// drift when gt's default hook configuration changes after initial setup.
type HooksBaseCheck struct {
	FixableCheck
}

// NewHooksBaseCheck creates a new hooks base config check.
func NewHooksBaseCheck() *HooksBaseCheck {
	return &HooksBaseCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "hooks-base-missing",
				CheckDescription: "Check that ~/.gt/hooks-base.json exists for drift detection",
				CheckCategory:    CategoryHooks,
			},
		},
	}
}

// Run checks whether hooks-base.json exists.
func (c *HooksBaseCheck) Run(ctx *CheckContext) *CheckResult {
	if _, err := hooks.LoadBase(); err == nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("hooks-base.json present at %s", hooks.BasePath()),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: "hooks-base.json is missing — gt hooks diff cannot detect drift",
		Details: []string{
			fmt.Sprintf("Expected at: %s", hooks.BasePath()),
			"Without this file, hooks sync works but drift detection is unavailable.",
		},
		FixHint: "Run 'gt doctor --fix hooks-base-missing' or 'gt hooks base --show' to create it",
	}
}

// Fix creates hooks-base.json from current defaults.
func (c *HooksBaseCheck) Fix(ctx *CheckContext) error {
	if _, err := hooks.LoadBase(); err == nil {
		return nil // already exists
	}
	base := hooks.DefaultBase()
	if err := hooks.SaveBase(base); err != nil {
		return fmt.Errorf("creating hooks-base.json: %w", err)
	}
	fmt.Printf("  Created hooks-base.json at %s\n", hooks.BasePath())
	return nil
}

// CanFix returns true — we can auto-create the file.
func (c *HooksBaseCheck) CanFix() bool {
	return true
}
