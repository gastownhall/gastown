package dog

import (
	"fmt"
	"time"

	"github.com/jonbaldie/gastown/internal/tmux"
)

// sessionChecker abstracts the tmux health-check methods needed by the
// health checker.  Satisfied by *tmux.Tmux; mockable in tests.
type sessionChecker interface {
	CheckSessionHealth(session string, maxInactivity time.Duration) tmux.ZombieStatus
	HasSession(name string) (bool, error)
	KillSession(name string) error
}

// DogHealthResult describes the health of a single dog.
type DogHealthResult struct {
	Name           string        `json:"name"`
	State          State         `json:"state"`
	SessionStatus  string        `json:"session_status"`          // from ZombieStatus.String()
	WorkDuration   time.Duration `json:"work_duration,omitempty"` // how long current work has been running
	NeedsAttention bool          `json:"needs_attention"`
	AutoCleared    bool          `json:"auto_cleared,omitempty"`
	Recommendation string        `json:"recommendation,omitempty"`
}

// HealthChecker performs health checks on dogs in the kennel.
type HealthChecker struct {
	mgr     *Manager
	checker sessionChecker
}

// NewHealthChecker creates a HealthChecker.
func NewHealthChecker(mgr *Manager, checker sessionChecker) *HealthChecker {
	return &HealthChecker{mgr: mgr, checker: checker}
}

// dogSessionName returns the tmux session name for a dog.
func dogSessionName(name string) string {
	return fmt.Sprintf("hq-dog-%s", name)
}

// Check performs a health check on a single dog.
func (hc *HealthChecker) Check(d *Dog, maxInactivity time.Duration, autoClear bool) DogHealthResult {
	result := DogHealthResult{
		Name:  d.Name,
		State: d.State,
	}

	// Compute work duration if working and WorkStartedAt is set.
	if d.State == StateWorking && !d.WorkStartedAt.IsZero() {
		result.WorkDuration = time.Since(d.WorkStartedAt)
	}

	session := dogSessionName(d.Name)

	switch d.State {
	case StateWorking:
		status := hc.checker.CheckSessionHealth(session, maxInactivity)
		result.SessionStatus = status.String()

		switch status {
		case tmux.SessionDead:
			// Zombie: state says working but session is gone.
			result.NeedsAttention = true
			result.Recommendation = "zombie: session dead but state=working"
			hc.maybeClearStateOnly(d, &result, autoClear, false, session,
				"zombie: source-backed work requires explicit recovery",
				"zombie auto-cleared (session dead)")

		case tmux.AgentDead:
			// Zombie: session exists but agent process died.
			result.NeedsAttention = true
			result.Recommendation = "zombie: agent dead in session"
			hc.maybeClearStateOnly(d, &result, autoClear, true, session,
				"zombie: session killed; source-backed work requires explicit recovery",
				"zombie auto-cleared (agent dead, session killed)")

		case tmux.AgentHung:
			// Hung: process alive but no tmux activity for maxInactivity.
			result.NeedsAttention = true
			if !autoClear {
				result.Recommendation = "hung: agent alive but no tmux activity"
				break
			}
			hc.maybeClearStateOnly(d, &result, true, true, session,
				"hung: session killed; source-backed work requires explicit recovery",
				"hung dog auto-cleared (idle prompt, session killed)")

		default: // SessionHealthy — status.String() already set above
		}

	case StateIdle:
		// Check for orphan session.
		has, _ := hc.checker.HasSession(session)
		if has {
			result.SessionStatus = "orphan"
			result.NeedsAttention = true
			if autoClear {
				_ = hc.checker.KillSession(session)
				result.AutoCleared = true
				result.Recommendation = "orphan auto-cleared (session killed)"
			} else {
				result.Recommendation = "orphan: dog idle but tmux session exists"
			}
		} else {
			result.SessionStatus = "none"
		}
	}

	return result
}

func (hc *HealthChecker) maybeClearStateOnly(d *Dog, result *DogHealthResult, autoClear, killSession bool, session, blockedRec, clearedRec string) {
	if !autoClear {
		return
	}
	if killSession {
		_ = hc.checker.KillSession(session)
	}
	if !CanClearStateOnly(d.Work, d.WorkKind) {
		result.Recommendation = blockedRec
		return
	}
	cleared, err := hc.mgr.ClearWorkIfMatches(d.Name, d.Work, d.WorkStartedAt)
	if err != nil {
		result.Recommendation = "auto-clear failed: " + err.Error()
		return
	}
	if cleared {
		result.AutoCleared = true
		result.Recommendation = clearedRec
	}
}

// CheckAll performs health checks on all dogs.
func (hc *HealthChecker) CheckAll(maxInactivity time.Duration, autoClear bool) ([]DogHealthResult, error) {
	dogs, err := hc.mgr.List()
	if err != nil {
		return nil, fmt.Errorf("listing dogs: %w", err)
	}

	results := make([]DogHealthResult, 0, len(dogs))
	for _, d := range dogs {
		results = append(results, hc.Check(d, maxInactivity, autoClear))
	}
	return results, nil
}

// NeedsAttentionCount returns how many results need attention.
func NeedsAttentionCount(results []DogHealthResult) int {
	n := 0
	for _, r := range results {
		if r.NeedsAttention {
			n++
		}
	}
	return n
}
