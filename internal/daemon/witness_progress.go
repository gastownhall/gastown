package daemon

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/agenthealth"
	"github.com/steveyegge/gastown/internal/beads"
)

// witnessStallCooldown is how long the daemon waits before nudging the same
// stalled witness again. Without it a stalled witness would be nudged on every
// heartbeat tick, which is both noisy and self-defeating: repeated nudges
// interrupt the agent it is trying to restart.
const witnessStallCooldown = 15 * time.Minute

// witnessStallNotifyCooldown is how long the daemon waits before raising
// another out-of-band alert for the same stalled witness.
const witnessStallNotifyCooldown = time.Hour

// stallTracker records when each agent was last nudged or alerted about, so a
// persistent stall produces a steady signal rather than a flood.
//
// Guarded by a mutex because witness checks run concurrently across rigs via
// rigPool.runPerRig, unlike the single-goroutine heartbeat state.
type stallTracker struct {
	mu       sync.Mutex
	nudged   map[string]time.Time
	notified map[string]time.Time
}

func newStallTracker() *stallTracker {
	return &stallTracker{
		nudged:   make(map[string]time.Time),
		notified: make(map[string]time.Time),
	}
}

// shouldAct reports whether cooldown has elapsed since the last action of this
// kind for this key, recording the action when it returns true.
// Callers must hold s.mu.
func (s *stallTracker) shouldAct(m map[string]time.Time, key string, cooldown time.Duration) bool {
	now := time.Now()
	if last, ok := m[key]; ok && now.Sub(last) < cooldown {
		return false
	}
	m[key] = now
	return true
}

func (s *stallTracker) shouldNudge(key string) bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shouldAct(s.nudged, key, witnessStallCooldown)
}

func (s *stallTracker) shouldNotify(key string) bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shouldAct(s.notified, key, witnessStallNotifyCooldown)
}

// clear forgets a key so the next stall starts from a clean cooldown.
func (s *stallTracker) clear(key string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.nudged, key)
	delete(s.notified, key)
}

// assessWitnessProgress judges whether a rig's witness is doing work, given
// that a session already exists for it.
//
// This is the check the spawn guard was missing. ErrAlreadyRunning proves only
// that a process exists — the predicate a frozen witness satisfies just as well
// as a working one, which is why the guard passed gastown/witness 1,887
// consecutive times through a three-day freeze (hq-40k).
func (d *Daemon) assessWitnessProgress(rigName string) agenthealth.Assessment {
	townRoot := d.config.TownRoot

	rigDir := beads.GetRigDirForName(townRoot, rigName)
	if rigDir == "" {
		rigDir = filepath.Join(townRoot, rigName)
	}

	work, workErr := agenthealth.LookupHookedWork(beads.New(rigDir), rigName+"/witness")

	return agenthealth.Assess(agenthealth.Input{
		SessionAlive: true, // caller has already established the session exists
		HookedWork:   work,
		WorkErr:      workErr,
		Threshold:    agenthealth.HookIdleThreshold(townRoot),
	})
}

// witnessAction is what the daemon does about a witness whose session exists.
type witnessAction int

const (
	// witnessActionSkipSpawn stands down: the witness is working.
	witnessActionSkipSpawn witnessAction = iota
	// witnessActionRecover treats the witness as failed even though its session
	// is alive, and acts to get it moving again.
	witnessActionRecover
	// witnessActionReportUnknown reports that the check could not decide.
	witnessActionReportUnknown
)

// witnessActionFor maps a work-progress verdict to the daemon's response.
//
// Only StateHealthy stands the daemon down. StateDegraded — alive but not
// working — gets recovery, the same direction as a crashed witness, because
// treating it as a reason to skip is the defect in hq-40k. StateUnknown is
// reported rather than assumed either way.
func witnessActionFor(state agenthealth.State) witnessAction {
	switch state {
	case agenthealth.StateHealthy:
		return witnessActionSkipSpawn
	case agenthealth.StateDegraded, agenthealth.StateStopped:
		return witnessActionRecover
	default:
		return witnessActionReportUnknown
	}
}

// handleRunningWitness decides what to log and do about a witness whose session
// already exists.
//
// The old guard logged "already running, skipping spawn" and returned, which is
// how three days of silence produced 1,887 reassuring log lines and no alarm.
// Now the session-exists case is split three ways:
//
//   - healthy: skip the spawn, as before.
//   - degraded: the witness is alive and not working. Nudge it and raise an
//     alert. A degraded witness is never treated as a reason to stand down.
//   - unknown: the check could not decide, so it says so rather than logging
//     the reassuring answer it cannot support.
func (d *Daemon) handleRunningWitness(rigName, sessionName string) {
	health := d.assessWitnessProgress(rigName)

	switch witnessActionFor(health.State) {
	case witnessActionSkipSpawn:
		d.witnessStalls.clear(rigName)
		d.logger.Printf("Witness for %s already running (%s), skipping spawn", rigName, health.Reason)

	case witnessActionRecover:
		d.logger.Printf("STALLED WITNESS: %s session %s is alive but %s",
			rigName, sessionName, health.Reason)

		if d.tmux != nil && d.witnessStalls.shouldNudge(rigName) {
			msg := fmt.Sprintf("HEALTH_CHECK: hooked work %s has not advanced in %s — run it or report why you cannot",
				health.HookBeadID, agenthealth.FormatIdle(health.HookIdle))
			if err := d.tmux.NudgeSession(sessionName, msg); err != nil {
				d.logger.Printf("Error nudging stalled witness for %s: %v", rigName, err)
			} else {
				d.logger.Printf("Nudged stalled witness for %s", rigName)
			}
		}

		if d.witnessStalls.shouldNotify(rigName) {
			d.notifySlack("admin", "high", fmt.Sprintf(
				"Witness for %s is alive but stalled — %s. Session %s exists, so nothing will respawn it; it has been nudged.",
				rigName, health.Reason, sessionName))
		}

	default: // witnessActionReportUnknown
		d.logger.Printf("Witness for %s: session exists but work progress is UNKNOWN (%s) — not treating as healthy",
			rigName, health.Reason)
	}
}
