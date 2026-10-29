package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/steveyegge/gastown/internal/config"
)

// Default budgets applied by LoadConfig when the rig leaves them unset (zero).
const (
	DefaultMaxFixFiles  = 5
	DefaultMaxFixRounds = 3
	DefaultMaxPlanLoops = 3
)

// LoadConfig reads the gated-workflow block from the rig's settings/config.json.
// A missing settings file or a missing workflow.gated block yields a disabled
// config with default budgets and a nil error; a malformed file is an error.
// The returned config is always a fresh copy that callers may mutate.
func LoadConfig(rigPath string) (*config.GatedWorkflow, error) {
	out := &config.GatedWorkflow{}
	settings, err := config.LoadRigSettings(config.RigSettingsPath(rigPath))
	switch {
	case errors.Is(err, config.ErrNotFound):
		// No settings file: gated workflow not configured.
	case err != nil:
		return nil, err
	case settings.Workflow != nil && settings.Workflow.Gated != nil:
		*out = *settings.Workflow.Gated
	}
	if out.MaxFixFiles == 0 {
		out.MaxFixFiles = DefaultMaxFixFiles
	}
	if out.MaxFixRounds == 0 {
		out.MaxFixRounds = DefaultMaxFixRounds
	}
	if out.MaxPlanLoops == 0 {
		out.MaxPlanLoops = DefaultMaxPlanLoops
	}
	return out, nil
}

// CheckCommandsSHA returns the lowercase hex sha256 of cmds joined with "\n".
func CheckCommandsSHA(cmds []string) string {
	sum := sha256.Sum256([]byte(strings.Join(cmds, "\n")))
	return hex.EncodeToString(sum[:])
}

// ChecksApproved reports whether the rig's check commands match the approved
// hash, so any edit to CheckCommands after approval voids the approval.
func ChecksApproved(c *config.GatedWorkflow) bool {
	return c.CheckCommandsApprovedSHA == CheckCommandsSHA(c.CheckCommands)
}
