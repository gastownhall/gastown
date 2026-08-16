package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jonbaldie/gastown/internal/config"
)

// GroqCompoundCheck probes the groq-compound agent for JSON output compliance.
// It is skipped when groq-compound is not configured in any role.
type GroqCompoundCheck struct {
	BaseCheck
}

// NewGroqCompoundCheck creates a new groq-compound JSON probe check.
func NewGroqCompoundCheck() *GroqCompoundCheck {
	return &GroqCompoundCheck{
		BaseCheck: BaseCheck{
			CheckName:        "groq-compound-json",
			CheckDescription: "Probe groq-compound agent for JSON output compliance",
			CheckCategory:    CategoryInfrastructure,
		},
	}
}

// Run executes the groq-compound JSON probe check.
func (c *GroqCompoundCheck) Run(ctx *CheckContext) *CheckResult {
	// Skip if groq-compound is not configured for any role.
	if !c.groqCompoundConfigured(ctx.TownRoot) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "groq-compound not configured (skipped)",
		}
	}

	// Skip if GROQ_API_KEY is not set.
	if os.Getenv("GROQ_API_KEY") == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "GROQ_API_KEY not set (skipped)",
		}
	}

	// Skip if claude binary is not available.
	if _, err := exec.LookPath("claude"); err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "claude binary not found (skipped)",
		}
	}

	// Build probe prompt with JSON enforcement appended.
	// NonInteractiveConfig equivalent: {OutputFormat:"json", NoColor:true, MaxTurns:1}
	probe := `Respond with exactly: {"status":"ok"}` + config.GroqJSONEnforcement

	out, err := c.invokeGroqCompound(probe)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("invocation failed: %v", err),
		}
	}

	// Attempt JSON unmarshal; pass if it succeeds, fail with raw output if not.
	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "groq-compound returned non-JSON output",
			Details: []string{strings.TrimSpace(string(out))},
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "groq-compound returns valid JSON",
	}
}

// invokeGroqCompound calls the claude binary with Groq routing and returns stdout.
func (c *GroqCompoundCheck) invokeGroqCompound(prompt string) ([]byte, error) {
	groqAPIKey := os.Getenv("GROQ_API_KEY")
	env := append(os.Environ(),
		"ANTHROPIC_BASE_URL=https://api.groq.com/openai/v1",
		"ANTHROPIC_MODEL=compound-beta",
		"ANTHROPIC_API_KEY="+groqAPIKey,
	)
	cmd := exec.Command("claude",
		"--dangerously-skip-permissions",
		"--output-format", "json",
		"--max-turns", "1",
		"-p", prompt,
	)
	cmd.Env = env
	return cmd.Output()
}

// groqCompoundConfigured returns true if groq-compound is configured for any role
// in the town or any registered rig.
func (c *GroqCompoundCheck) groqCompoundConfigured(townRoot string) bool {
	if townRoot == "" {
		return false
	}
	townSettings, err := config.LoadOrCreateTownSettings(config.TownSettingsPath(townRoot))
	if err != nil {
		return false
	}
	if townSettings.DefaultAgent == string(config.AgentGroqCompound) {
		return true
	}
	for _, agent := range townSettings.RoleAgents {
		if agent == string(config.AgentGroqCompound) {
			return true
		}
	}
	return false
}
