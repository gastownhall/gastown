package doctor

import (
	"fmt"
	"time"

	"github.com/steveyegge/gastown/internal/daemon"
)

// ModelCrashRecoveryCheck surfaces durable local-session model recovery
// incidents without mutating supervisor state.
type ModelCrashRecoveryCheck struct {
	BaseCheck
}

func NewModelCrashRecoveryCheck() *ModelCrashRecoveryCheck {
	return &ModelCrashRecoveryCheck{
		BaseCheck: BaseCheck{
			CheckName:        "model-crash-recovery",
			CheckDescription: "Check local model-crash supervisor recovery state",
			CheckCategory:    CategoryInfrastructure,
		},
	}
}

func (c *ModelCrashRecoveryCheck) Run(ctx *CheckContext) *CheckResult {
	if !daemon.IsModelCrashRecoveryProvisioned(ctx.TownRoot) {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "Local model-crash recovery is not provisioned",
			Category: CategoryInfrastructure,
		}
	}
	if err := daemon.ValidateModelCrashWatchdog(ctx.TownRoot, time.Now()); err != nil {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusError,
			Message:  "Model-crash recovery is fail-closed because the LM watchdog is unavailable",
			Details:  []string{err.Error()},
			FixHint:  "Restore a fresh healthy deacon/lmstudio-watchdog.json before relying on automatic local-session recovery",
			Category: CategoryInfrastructure,
		}
	}

	incidents, err := daemon.LoadModelCrashRecoveryIncidents(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusError,
			Message:  "Failed to read model-crash supervisor state",
			Details:  []string{err.Error()},
			Category: CategoryInfrastructure,
		}
	}

	details := make([]string, 0, len(incidents))
	active := 0
	exhausted := 0
	for _, incident := range incidents {
		if !incident.Confirmed && !incident.RecoveryExhausted {
			continue
		}
		active++
		if incident.RecoveryExhausted {
			exhausted++
		}
		details = append(details, fmt.Sprintf(
			"%s: incident=%s action=%s exhausted=%t session=%s",
			incident.Identity, incident.IncidentID, incident.RecoveryAction,
			incident.RecoveryExhausted, incident.SessionName))
	}
	if active == 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "No confirmed model-crash recovery incidents",
			Category: CategoryInfrastructure,
		}
	}
	status := StatusWarning
	message := fmt.Sprintf("%d model-crash recovery incident(s) are being observed", active)
	if exhausted > 0 {
		status = StatusError
		message = fmt.Sprintf("%d exhausted model-crash recovery incident(s) require attention", exhausted)
	}
	return &CheckResult{
		Name:     c.Name(),
		Status:   status,
		Message:  message,
		Details:  details,
		FixHint:  "Inspect with 'gt session health <tmux-session> --json'; recovery actions are supervisor-owned",
		Category: CategoryInfrastructure,
	}
}
