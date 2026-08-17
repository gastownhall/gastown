package doctor

import (
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/jonbaldie/gastown/internal/config"
)

// MayorBinaryCheck verifies that the configured Mayor agent binary is on PATH.
type MayorBinaryCheck struct {
	BaseCheck
}

// NewMayorBinaryCheck creates a Mayor agent binary check.
func NewMayorBinaryCheck() *MayorBinaryCheck {
	return &MayorBinaryCheck{
		BaseCheck: BaseCheck{
			CheckName:        "mayor-binary",
			CheckDescription: "Check that the configured Mayor agent binary is on PATH",
			CheckCategory:    CategoryInfrastructure,
		},
	}
}

// Run fails when RoleAgents["mayor"] is set and that agent's command is missing.
func (c *MayorBinaryCheck) Run(ctx *CheckContext) *CheckResult {
	if ctx == nil || ctx.TownRoot == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "no town root",
		}
	}

	settings, err := config.LoadOrCreateTownSettings(config.TownSettingsPath(ctx.TownRoot))
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("could not load town settings: %v", err),
			FixHint: "Fix settings/config.json, then rerun gt doctor",
		}
	}
	if settings == nil || settings.RoleAgents["mayor"] == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Mayor role has no explicit mix agent",
		}
	}

	mayorDir := filepath.Join(ctx.TownRoot, "mayor")
	rc := config.ResolveRoleAgentConfig("mayor", ctx.TownRoot, mayorDir)
	if rc == nil || rc.Command == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Mayor agent %q has no command", settings.RoleAgents["mayor"]),
			FixHint: "Install the Mayor runtime CLI, or run gt now with a runtime that is on PATH",
		}
	}

	if _, err := exec.LookPath(rc.Command); err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Mayor binary %q not found on PATH", rc.Command),
			Details: []string{
				fmt.Sprintf("Mayor mix agent is %s", settings.RoleAgents["mayor"]),
			},
			FixHint: fmt.Sprintf("Install %s and put it on PATH", rc.Command),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("Mayor binary %s", rc.Command),
	}
}
