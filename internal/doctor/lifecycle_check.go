package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var lifecycleExecCommand = exec.Command

func lifecycleDeaconEnv(environ []string) []string {
	env := make([]string, 0, len(environ)+2)
	for _, entry := range environ {
		if strings.HasPrefix(entry, "GT_ROLE=") || strings.HasPrefix(entry, "BD_ACTOR=") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "GT_ROLE=deacon", "BD_ACTOR=deacon/")
}

// LifecycleHygieneCheck detects and cleans up stale lifecycle state.
// This can happen when lifecycle messages weren't properly deleted after processing.
type LifecycleHygieneCheck struct {
	FixableCheck
	staleMessages []staleMessage
}

type staleMessage struct {
	ID      string
	Subject string
	From    string
}

// NewLifecycleHygieneCheck creates a new lifecycle hygiene check.
func NewLifecycleHygieneCheck() *LifecycleHygieneCheck {
	return &LifecycleHygieneCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "lifecycle-hygiene",
				CheckDescription: "Check for stale lifecycle messages",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// Run checks for stale lifecycle state.
func (c *LifecycleHygieneCheck) Run(ctx *CheckContext) *CheckResult {
	c.staleMessages = nil

	// Check for stale lifecycle messages in deacon inbox
	staleCount := c.checkDeaconInbox(ctx)
	if staleCount == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No stale lifecycle messages found",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("Found %d stale lifecycle message(s) in deacon inbox", staleCount),
		FixHint: "Run 'gt doctor --fix' to clean up",
	}
}

// checkDeaconInbox looks for stale lifecycle messages.
func (c *LifecycleHygieneCheck) checkDeaconInbox(ctx *CheckContext) int {
	// Get deacon inbox via gt mail
	cmd := lifecycleExecCommand("gt", "mail", "inbox", "--identity", "deacon/", "--json")
	cmd.Dir = ctx.TownRoot

	output, err := cmd.Output()
	if err != nil {
		return 0 // Can't check, assume OK
	}

	if len(output) == 0 || string(output) == "[]" || string(output) == "[]\n" {
		return 0
	}

	var messages []struct {
		ID      string `json:"id"`
		From    string `json:"from"`
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(output, &messages); err != nil {
		return 0
	}

	// Look for lifecycle messages
	for _, msg := range messages {
		if strings.HasPrefix(strings.ToLower(msg.Subject), "lifecycle:") {
			c.staleMessages = append(c.staleMessages, staleMessage{
				ID:      msg.ID,
				Subject: msg.Subject,
				From:    msg.From,
			})
		}
	}

	return len(c.staleMessages)
}

// Fix cleans up stale lifecycle messages.
func (c *LifecycleHygieneCheck) Fix(ctx *CheckContext) error {
	var errors []string

	// Delete stale lifecycle messages
	for _, msg := range c.staleMessages {
		cmd := lifecycleExecCommand("gt", "mail", "delete", msg.ID) //nolint:gosec // G204: msg.ID is from internal state, not user input
		cmd.Dir = ctx.TownRoot
		cmd.Env = lifecycleDeaconEnv(os.Environ())
		output, err := cmd.CombinedOutput()
		if err != nil {
			detail := strings.TrimSpace(string(output))
			if detail != "" {
				errors = append(errors, fmt.Sprintf("failed to delete message %s: %v: %s", msg.ID, err, detail))
			} else {
				errors = append(errors, fmt.Sprintf("failed to delete message %s: %v", msg.ID, err))
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("%s", strings.Join(errors, "; "))
	}
	return nil
}
