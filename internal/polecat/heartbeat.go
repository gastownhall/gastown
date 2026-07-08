package polecat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/config"
)

// SessionHeartbeatStaleThreshold is the default age at which a polecat session
// heartbeat is considered stale, indicating the agent process is likely dead.
// Configurable via operational.polecat.heartbeat_stale_threshold in
// settings/config.json — use SessionHeartbeatStaleThresholdFor to honor the
// configured value; this constant is only the compiled-in default.
const SessionHeartbeatStaleThreshold = 3 * time.Minute

// SessionHeartbeatStaleThresholdFor returns the heartbeat stale threshold for a
// town, honoring operational.polecat.heartbeat_stale_threshold in
// settings/config.json and falling back to SessionHeartbeatStaleThreshold.
func SessionHeartbeatStaleThresholdFor(townRoot string) time.Duration {
	if townRoot == "" {
		return SessionHeartbeatStaleThreshold
	}
	return config.LoadOperationalConfig(townRoot).GetPolecatConfig().HeartbeatStaleThresholdD()
}

// HeartbeatState represents the agent-reported state in a heartbeat v2 (gt-3vr5).
// Agents report their own state; the witness makes exactly one inference:
// "is the heartbeat fresh?" Everything else is agent-reported.
type HeartbeatState string

const (
	// HeartbeatWorking means the agent is actively processing.
	HeartbeatWorking HeartbeatState = "working"
	// HeartbeatIdle means the agent is waiting for input.
	HeartbeatIdle HeartbeatState = "idle"
	// HeartbeatExiting means the agent is in the gt done flow.
	HeartbeatExiting HeartbeatState = "exiting"
	// HeartbeatStuck means the agent self-reports being stuck.
	HeartbeatStuck HeartbeatState = "stuck"
)

// SessionHeartbeat represents a polecat session's heartbeat file.
// v1: timestamp only. v2 (gt-3vr5): adds agent-reported state, context, and bead.
type SessionHeartbeat struct {
	Timestamp time.Time      `json:"timestamp"`
	State     HeartbeatState `json:"state,omitempty"`   // v2: agent-reported state
	Context   string         `json:"context,omitempty"` // v2: what the agent is doing
	Bead      string         `json:"bead,omitempty"`    // v2: current hook bead ID
}

// EffectiveState returns the agent-reported state, defaulting to HeartbeatWorking
// for v1 heartbeats without a state field (backwards compatibility). See gt-3vr5.
func (h *SessionHeartbeat) EffectiveState() HeartbeatState {
	if h.State == "" {
		return HeartbeatWorking
	}
	return h.State
}

// IsV2 returns true if this heartbeat carries a state field (heartbeat v2).
// Used by the witness to decide whether to use agent-reported state or fall
// through to legacy timer-based detection.
func (h *SessionHeartbeat) IsV2() bool {
	return h.State != ""
}

// heartbeatsDir returns the directory for polecat session heartbeat files.
// Heartbeats live under <townRoot>/.runtime/heartbeats/, parallel to .runtime/pids/.
func heartbeatsDir(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "heartbeats")
}

// heartbeatFile returns the path to a heartbeat file for a given session.
func heartbeatFile(townRoot, sessionName string) string {
	return filepath.Join(heartbeatsDir(townRoot), sessionName+".json")
}

// TouchSessionHeartbeat writes or updates the heartbeat file for a polecat session.
// Writes state="working" by default (heartbeat v2, gt-3vr5).
// This is best-effort: errors are silently ignored because heartbeat signals
// are non-critical and should not interrupt gt commands.
func TouchSessionHeartbeat(townRoot, sessionName string) {
	TouchSessionHeartbeatWithState(townRoot, sessionName, HeartbeatWorking, "", "")
}

// TouchSessionHeartbeatWithState writes a heartbeat with explicit state information.
// Used by gt done (state="exiting") and gt heartbeat (state="stuck"). See gt-3vr5.
// This is best-effort: errors are silently ignored.
func TouchSessionHeartbeatWithState(townRoot, sessionName string, state HeartbeatState, context, bead string) {
	dir := heartbeatsDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}

	hb := SessionHeartbeat{
		Timestamp: time.Now().UTC(),
		State:     state,
		Context:   context,
		Bead:      bead,
	}

	data, err := json.Marshal(hb)
	if err != nil {
		return
	}

	_ = os.WriteFile(heartbeatFile(townRoot, sessionName), data, 0644)
}

// ReadSessionHeartbeat reads the heartbeat for a polecat session.
// Returns nil if the file doesn't exist or can't be read.
func ReadSessionHeartbeat(townRoot, sessionName string) *SessionHeartbeat {
	data, err := os.ReadFile(heartbeatFile(townRoot, sessionName))
	if err != nil {
		return nil
	}

	var hb SessionHeartbeat
	if err := json.Unmarshal(data, &hb); err != nil {
		return nil
	}

	return &hb
}

// IsSessionHeartbeatStale returns true if the session's heartbeat is older than
// the stale threshold, or if no heartbeat file exists.
//
// When no heartbeat file exists, this returns false to avoid false positives
// during the rollout period where sessions may not yet be touching heartbeats.
// The caller should fall back to other liveness checks in that case.
func IsSessionHeartbeatStale(townRoot, sessionName string) (stale bool, exists bool) {
	hb := ReadSessionHeartbeat(townRoot, sessionName)
	if hb == nil {
		return false, false
	}
	return time.Since(hb.Timestamp) >= SessionHeartbeatStaleThresholdFor(townRoot), true
}

// RemoveSessionHeartbeat removes the heartbeat file for a session.
// Called during session cleanup.
func RemoveSessionHeartbeat(townRoot, sessionName string) {
	_ = os.Remove(heartbeatFile(townRoot, sessionName))
}
