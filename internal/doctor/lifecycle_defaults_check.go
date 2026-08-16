package doctor

import (
	"fmt"
	"strings"

	"github.com/jonbaldie/gastown/internal/daemon"
)

// LifecycleDefaultsCheck detects missing lifecycle patrol entries in daemon.json
// and auto-populates them with sensible defaults.
//
// Existing towns that upgraded may be missing entries for newer Dogs
// (Compactor, JSONL backup, scheduled maintenance, etc.). This check
// invokes the same EnsureLifecycleDefaults logic used by gt init/gt up.
type LifecycleDefaultsCheck struct {
	FixableCheck
	missing []string
}

// NewLifecycleDefaultsCheck creates a new lifecycle defaults check.
func NewLifecycleDefaultsCheck() *LifecycleDefaultsCheck {
	return &LifecycleDefaultsCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "lifecycle-defaults",
				CheckDescription: "Check daemon.json has all lifecycle patrol entries",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// Run checks for missing lifecycle patrol entries in daemon.json.
func (c *LifecycleDefaultsCheck) Run(ctx *CheckContext) *CheckResult {
	c.missing = nil

	config := daemon.LoadPatrolConfig(ctx.TownRoot)
	if config == nil {
		// No daemon.json at all — EnsureLifecycleConfigFile handles creation.
		// Report as warning so --fix can create it.
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "daemon.json not found",
			FixHint: "Run 'gt doctor --fix' to create with defaults",
		}
	}

	if config.Patrols == nil {
		c.missing = []string{"patrols (entire section)"}
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "daemon.json missing patrols section",
			FixHint: "Run 'gt doctor --fix' to populate defaults",
		}
	}

	// Check each patrol entry individually
	p := config.Patrols
	if p.WispReaper == nil {
		c.missing = append(c.missing, "wisp_reaper")
	}
	if p.CompactorDog == nil {
		c.missing = append(c.missing, "compactor_dog")
	}
	if p.DoctorDog == nil {
		c.missing = append(c.missing, "doctor_dog")
	}
	if p.JsonlGitBackup == nil {
		c.missing = append(c.missing, "jsonl_git_backup")
	}
	if p.DoltBackup == nil {
		c.missing = append(c.missing, "dolt_backup")
	}
	if p.ScheduledMaintenance == nil {
		c.missing = append(c.missing, "scheduled_maintenance")
	}

	if len(c.missing) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "All lifecycle patrols configured",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("Missing %d lifecycle patrol(s): %s", len(c.missing), strings.Join(c.missing, ", ")),
		FixHint: "Run 'gt doctor --fix' to populate defaults",
	}
}

// Fix populates missing lifecycle patrol entries with defaults.
func (c *LifecycleDefaultsCheck) Fix(ctx *CheckContext) error {
	return daemon.EnsureLifecycleConfigFile(ctx.TownRoot)
}
