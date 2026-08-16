package doctor

import (
	"fmt"

	"github.com/jonbaldie/gastown/internal/deps"
)

// ClaudeBinaryCheck verifies that the Claude Code CLI is installed and meets
// the minimum version requirement. Claude Code is optional (Gas Town supports
// other agents), so missing Claude Code is a warning, not an error.
type ClaudeBinaryCheck struct {
	BaseCheck
}

// NewClaudeBinaryCheck creates a new Claude Code binary version check.
func NewClaudeBinaryCheck() *ClaudeBinaryCheck {
	return &ClaudeBinaryCheck{
		BaseCheck: BaseCheck{
			CheckName:        "claude-binary",
			CheckDescription: "Check that Claude Code meets minimum version for Gas Town",
			CheckCategory:    CategoryInfrastructure,
		},
	}
}

// Run checks if Claude Code is available in PATH and reports its version status.
func (c *ClaudeBinaryCheck) Run(ctx *CheckContext) *CheckResult {
	status, version := deps.CheckClaudeCode()

	switch status {
	case deps.ClaudeCodeOK:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("claude %s", version),
		}

	case deps.ClaudeCodeNotFound:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "claude not found (optional — other agents work without it)",
		}

	case deps.ClaudeCodeTooOld:
		return &CheckResult{
			Name:   c.Name(),
			Status: StatusWarning,
			Message: fmt.Sprintf("claude %s is below minimum (%s) — hooks, skills, and nudge delivery may not work",
				version, deps.MinClaudeCodeVersion),
			Details: []string{
				fmt.Sprintf("Gas Town requires Claude Code >= %s for full functionality", deps.MinClaudeCodeVersion),
				"Features requiring hooks (priming, mail, guards) will not work on older versions",
			},
			FixHint: fmt.Sprintf("Upgrade: npm install -g @anthropic-ai/claude-code@latest (see %s)", deps.ClaudeCodeInstallURL),
		}

	case deps.ClaudeCodeOldButOK:
		return &CheckResult{
			Name:   c.Name(),
			Status: StatusOK,
			Message: fmt.Sprintf("claude %s (upgrade to %s+ recommended for unified skills/commands)",
				version, deps.RecommendedClaudeCodeVersion),
		}

	case deps.ClaudeCodeExecFailed:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "claude found but 'claude --version' failed",
			FixHint: fmt.Sprintf("Reinstall: npm install -g @anthropic-ai/claude-code@latest (see %s)", deps.ClaudeCodeInstallURL),
		}

	case deps.ClaudeCodeUnknown:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "claude found but version could not be parsed",
			FixHint: fmt.Sprintf("Try reinstalling: npm install -g @anthropic-ai/claude-code@latest (see %s)", deps.ClaudeCodeInstallURL),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "claude available",
	}
}
