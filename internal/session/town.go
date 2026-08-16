// Package session provides polecat session lifecycle management.
package session

import (
	"fmt"
	"time"

	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/events"
	"github.com/jonbaldie/gastown/internal/tmux"
)

// TownSession represents a town-level tmux session.
type TownSession struct {
	Name      string // Display name (e.g., "Mayor")
	SessionID string // Tmux session ID (e.g., "hq-mayor")
}

// TownSessions returns the list of town-level sessions in shutdown order.
// Order matters: Boot (Deacon's watchdog) must be stopped before Deacon,
// otherwise Boot will try to restart Deacon.
func TownSessions() []TownSession {
	return []TownSession{
		{"Mayor", MayorSessionName()},
		{"Boot", BootSessionName()},
		{"Deacon", DeaconSessionName()},
	}
}

// StopTownSession stops a single town-level tmux session.
// If force is true, skips graceful shutdown (Ctrl-C) and kills immediately.
// Returns true if the session was running and stopped, false if not running.
func StopTownSession(t *tmux.Tmux, ts TownSession, force bool) (bool, error) {
	running, err := t.HasSession(ts.SessionID)
	if err != nil {
		return false, err
	}
	if !running {
		return false, nil
	}

	return stopTownSessionInternal(t, ts, force)
}

// StopTownSessionWithCache is like StopTownSession but uses a pre-fetched
// SessionSet for O(1) existence check instead of spawning a subprocess.
func StopTownSessionWithCache(t *tmux.Tmux, ts TownSession, force bool, cache *tmux.SessionSet) (bool, error) {
	if !cache.Has(ts.SessionID) {
		return false, nil
	}

	return stopTownSessionInternal(t, ts, force)
}

// stopTownSessionInternal performs the actual session stop.
func stopTownSessionInternal(t *tmux.Tmux, ts TownSession, force bool) (bool, error) {
	// Try graceful shutdown first (unless forced)
	if !force {
		_ = t.SendKeysRaw(ts.SessionID, "C-c")
		WaitForSessionExit(t, ts.SessionID, constants.GracefulShutdownTimeout)
	}

	// Log pre-death event for crash investigation (before killing)
	reason := "user shutdown"
	if force {
		reason = "forced shutdown"
	}
	_ = events.LogFeed(events.TypeSessionDeath, ts.SessionID,
		events.SessionDeathPayload(ts.SessionID, ts.Name, reason, "gt down"))

	// Kill the session.
	// Use KillSessionWithProcesses to ensure all descendant processes are killed.
	if err := t.KillSessionWithProcesses(ts.SessionID); err != nil {
		return false, fmt.Errorf("killing %s session: %w", ts.Name, err)
	}

	return true, nil
}

// WaitForSessionExit polls for a session's process to exit within the given timeout.
// Returns true if the process exited on its own, false if the timeout was reached.
// This allows graceful shutdown (e.g., after Ctrl-C) to actually complete before
// falling through to forceful termination.
func WaitForSessionExit(t TmuxOps, sessionID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		running, err := t.HasSession(sessionID)
		if err != nil || !running {
			return true
		}
		time.Sleep(constants.PollInterval)
	}
	return false
}
