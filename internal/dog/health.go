package dog

import (
	"fmt"
	"time"

	"github.com/steveyegge/gastown/internal/tmux"
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
	HookedWork     string        `json:"hooked_work,omitempty"` // bead still on the dog's hook
	CheckError     string        `json:"check_error,omitempty"` // a sub-check failed; result is partial
}

// HealthChecker performs health checks on dogs in the kennel.
type HealthChecker struct {
	mgr     *Manager
	checker sessionChecker
	hooked  HookedWorkFinder
}

// NewHealthChecker creates a HealthChecker.
func NewHealthChecker(mgr *Manager, checker sessionChecker) *HealthChecker {
	return &HealthChecker{mgr: mgr, checker: checker}
}

// WithHookedWorkFinder attaches a hooked-work source, enabling stranded-dog
// detection. Without one, the stranded check is skipped.
func (hc *HealthChecker) WithHookedWorkFinder(f HookedWorkFinder) *HealthChecker {
	hc.hooked = f
	return hc
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
			if autoClear {
				if err := hc.mgr.ClearWork(d.Name); err == nil {
					result.AutoCleared = true
					result.Recommendation = "zombie auto-cleared (session dead)"
				}
			}

		case tmux.AgentDead:
			// Zombie: session exists but agent process died.
			result.NeedsAttention = true
			result.Recommendation = "zombie: agent dead in session"
			if autoClear {
				_ = hc.checker.KillSession(session)
				if err := hc.mgr.ClearWork(d.Name); err == nil {
					result.AutoCleared = true
					result.Recommendation = "zombie auto-cleared (agent dead, session killed)"
				}
			}

		case tmux.AgentHung:
			// Hung: process alive but no tmux activity for maxInactivity.
			// If autoClear is on, kill and reclaim — the dog almost certainly
			// finished its work but failed to call `gt dog done`.
			result.NeedsAttention = true
			if autoClear {
				_ = hc.checker.KillSession(session)
				if err := hc.mgr.ClearWork(d.Name); err == nil {
					result.AutoCleared = true
					result.Recommendation = "hung dog auto-cleared (idle prompt, session killed)"
				} else {
					result.Recommendation = "hung: auto-clear failed: " + err.Error()
				}
			} else {
				result.Recommendation = "hung: agent alive but no tmux activity"
			}

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
			hc.checkStranded(d, &result)
		}
	}

	return result
}

// checkStranded reports a dog that is idle with no session while still holding
// hooked work. It is the mirror of the orphan case: orphan is a session with no
// work, stranded is work with no session. A town halt between hooking a wisp
// and spawning the dog session drops the spawn, so the work never runs while
// the dog reports idle and healthy (hq-xgq).
//
// Never auto-cleared: unhooking would discard real work, so this is reported
// and the Deacon decides (same treatment as hung).
func (hc *HealthChecker) checkStranded(d *Dog, result *DogHealthResult) {
	if hc.hooked == nil {
		return
	}

	work, err := hc.hooked.HookedWork(d.Name)
	if err != nil {
		// Graceful degradation: a beads outage must not fail the whole check,
		// but the caller should know this dog was not fully checked.
		result.CheckError = "stranded check failed: " + err.Error()
		return
	}
	if work == nil {
		return
	}

	result.SessionStatus = "stranded"
	result.HookedWork = work.ID
	result.NeedsAttention = true
	result.Recommendation = fmt.Sprintf(
		"stranded: dog idle with no session but %s is still hooked (run: gt dog call %s)",
		work.ID, d.Name)
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
