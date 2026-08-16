package doctor

import (
	"fmt"
	"strings"

	"github.com/jonbaldie/gastown/internal/skills"
)

// SkillsCheck validates that town-level mattpocock skills are provisioned.
type SkillsCheck struct {
	FixableCheck
	missingSkills []string
}

// NewSkillsCheck creates a new skills check.
func NewSkillsCheck() *SkillsCheck {
	return &SkillsCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "skills-provisioned",
				CheckDescription: "Check mattpocock skills are provisioned at town level",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// Run checks if town-level skills are provisioned.
func (c *SkillsCheck) Run(ctx *CheckContext) *CheckResult {
	c.missingSkills = skills.MissingFor(ctx.TownRoot, "claude")

	if len(c.missingSkills) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("Town-level mattpocock skills provisioned (%d)", len(skills.Names())),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("Missing town-level skills: %s", strings.Join(c.missingSkills, ", ")),
		Details: []string{
			fmt.Sprintf("Expected at: %s/.agents/skills/", ctx.TownRoot),
			"Role sessions need these skills in every workspace",
		},
		FixHint: "Run 'gt doctor --fix' to provision missing skills",
	}
}

// Fix provisions missing skills at town level.
func (c *SkillsCheck) Fix(ctx *CheckContext) error {
	if len(c.missingSkills) == 0 {
		return nil
	}
	return skills.ProvisionFor(ctx.TownRoot, "claude")
}
