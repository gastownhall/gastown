// Package deacon provides the Deacon agent infrastructure.
// The Deacon is a Claude agent that monitors Mayor and Witnesses,
// handles lifecycle requests, and keeps Gas Town running.
package deacon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/lock"
)

// Heartbeat age thresholds — these are compiled-in defaults.
// Configurable via operational.deacon.heartbeat_stale_threshold and
// operational.deacon.heartbeat_very_stale_threshold in settings/config.json.
const (
	// HeartbeatStaleThreshold is the age at which a heartbeat is considered stale.
	HeartbeatStaleThreshold = 5 * time.Minute

	// HeartbeatVeryStaleThreshold is the age at which a heartbeat is considered
	// very stale, meaning the Deacon should be poked or restarted.
	// Must be greater than patrol backoff-max (15m) to avoid false positives
	// during legitimate await-signal sleep.
	HeartbeatVeryStaleThreshold = 20 * time.Minute

	// HeartbeatCreditsPerPatrol matches the three heartbeat calls in
	// mol-deacon-patrol: cycle start, mid-cycle, and pre-await checkpoint.
	HeartbeatCreditsPerPatrol = 3
)

// HeartbeatCreditState gates deacon heartbeat writes on patrol lifecycle
// progress. It prevents background loops from inflating heartbeat/cycle counts
// without creating or reporting patrol wisps.
type HeartbeatCreditState struct {
	Credits          int       `json:"credits"`
	PatrolID         string    `json:"patrol_id,omitempty"`
	BootstrapGranted bool      `json:"bootstrap_granted"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Heartbeat represents the Deacon's heartbeat file contents.
// Written by the Deacon on each wake cycle.
// Read by the Go daemon to decide whether to poke.
type Heartbeat struct {
	// Timestamp is when the heartbeat was written.
	Timestamp time.Time `json:"timestamp"`

	// Cycle is the current wake cycle number.
	Cycle int64 `json:"cycle"`

	// LastAction describes what the Deacon did in this cycle.
	LastAction string `json:"last_action,omitempty"`

	// HealthyAgents is the count of healthy agents observed.
	HealthyAgents int `json:"healthy_agents"`

	// UnhealthyAgents is the count of unhealthy agents observed.
	UnhealthyAgents int `json:"unhealthy_agents"`
}

// HeartbeatFile returns the path to the Deacon heartbeat file.
func HeartbeatFile(townRoot string) string {
	return filepath.Join(townRoot, "deacon", "heartbeat.json")
}

// HeartbeatCreditFile returns the path to the persisted heartbeat credit state.
func HeartbeatCreditFile(townRoot string) string {
	return filepath.Join(townRoot, "deacon", "heartbeat_credits.json")
}

func readHeartbeatCredits(townRoot string) (*HeartbeatCreditState, bool, error) {
	data, err := os.ReadFile(HeartbeatCreditFile(townRoot)) //nolint:gosec // G304: path is constructed from trusted townRoot
	if os.IsNotExist(err) {
		return &HeartbeatCreditState{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	var state HeartbeatCreditState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, true, err
	}
	return &state, true, nil
}

func writeHeartbeatCredits(townRoot string, state *HeartbeatCreditState) error {
	path := HeartbeatCreditFile(townRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	state.UpdatedAt = time.Now().UTC()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".heartbeat_credits-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) //nolint:errcheck // best-effort cleanup after rename/failure

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmpPath, path)
}

func withHeartbeatCreditsLock(townRoot string, fn func(*HeartbeatCreditState, bool) error) error {
	path := HeartbeatCreditFile(townRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	unlock, err := lock.FlockAcquire(path + ".flock")
	if err != nil {
		return err
	}
	defer unlock()

	state, exists, err := readHeartbeatCredits(townRoot)
	if err != nil {
		return err
	}
	if err := fn(state, exists); err != nil {
		return err
	}
	return writeHeartbeatCredits(townRoot, state)
}

// GrantHeartbeatCredits records verified patrol lifecycle progress. Credits are
// capped at one patrol cycle so repeated lifecycle calls cannot accumulate an
// unbounded heartbeat budget.
func GrantHeartbeatCredits(townRoot string, patrolID string, credits int) error {
	if patrolID == "" {
		return fmt.Errorf("patrol ID is required to grant deacon heartbeat credits")
	}
	if credits <= 0 {
		return nil
	}
	return withHeartbeatCreditsLock(townRoot, func(state *HeartbeatCreditState, _ bool) error {
		if state.PatrolID == patrolID {
			return nil
		}
		state.PatrolID = patrolID
		state.BootstrapGranted = true
		state.Credits = credits
		if state.Credits > HeartbeatCreditsPerPatrol {
			state.Credits = HeartbeatCreditsPerPatrol
		}
		return nil
	})
}

// ConsumeHeartbeatCredit spends one heartbeat credit. A missing state file gets
// exactly one persisted bootstrap credit for startup before the first patrol
// wisp is created; restarts do not recreate it after it has been consumed.
func ConsumeHeartbeatCredit(townRoot string) error {
	return withHeartbeatCreditsLock(townRoot, func(state *HeartbeatCreditState, exists bool) error {
		if !exists && !state.BootstrapGranted {
			state.BootstrapGranted = true
			state.Credits = 1
		}

		if state.Credits <= 0 {
			return fmt.Errorf("deacon heartbeat refused: no patrol heartbeat credits remain; run gt patrol new or complete the current patrol with gt patrol report")
		}
		state.Credits--
		return nil
	})
}

// WriteHeartbeat writes a new heartbeat to disk.
// Called by the Deacon at the start of each wake cycle.
func WriteHeartbeat(townRoot string, hb *Heartbeat) error {
	hbFile := HeartbeatFile(townRoot)

	// Ensure deacon directory exists
	if err := os.MkdirAll(filepath.Dir(hbFile), 0755); err != nil {
		return err
	}

	// Set timestamp if not already set
	if hb.Timestamp.IsZero() {
		hb.Timestamp = time.Now().UTC()
	}

	data, err := json.MarshalIndent(hb, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(hbFile, data, 0600); err != nil {
		return err
	}

	// Also touch .deacon-heartbeat for backward compatibility with shell scripts
	// that check this file's mtime for liveness detection (stuck-agent-dog).
	// These scripts predate heartbeat.json and check mtime, not file contents.
	legacyFile := filepath.Join(filepath.Dir(hbFile), ".deacon-heartbeat")
	_ = os.WriteFile(legacyFile, []byte(""), 0644) //nolint:gosec // G306: world-readable liveness file is intentional

	return nil
}

// ReadHeartbeat reads the Deacon heartbeat from disk.
// Returns nil if the file doesn't exist or can't be read.
func ReadHeartbeat(townRoot string) *Heartbeat {
	hbFile := HeartbeatFile(townRoot)

	data, err := os.ReadFile(hbFile) //nolint:gosec // G304: path is constructed from trusted townRoot
	if err != nil {
		return nil
	}

	var hb Heartbeat
	if err := json.Unmarshal(data, &hb); err != nil {
		return nil
	}

	return &hb
}

// Age returns how old the heartbeat is.
// Returns a very large duration if the heartbeat is nil.
func (hb *Heartbeat) Age() time.Duration {
	if hb == nil {
		return 24 * time.Hour * 365 // Very stale
	}
	return time.Since(hb.Timestamp)
}

// IsFresh returns true if the heartbeat is less than 5 minutes old.
// A fresh heartbeat means the Deacon is actively working or recently finished.
func (hb *Heartbeat) IsFresh() bool {
	return hb != nil && hb.Age() < HeartbeatStaleThreshold
}

// IsStale returns true if the heartbeat is 5-20 minutes old.
// A stale heartbeat may indicate the Deacon is doing a long operation.
func (hb *Heartbeat) IsStale() bool {
	if hb == nil {
		return false
	}
	age := hb.Age()
	return age >= HeartbeatStaleThreshold && age < HeartbeatVeryStaleThreshold
}

// IsVeryStale returns true if the heartbeat is more than 20 minutes old.
// A very stale heartbeat means the Deacon should be poked.
func (hb *Heartbeat) IsVeryStale() bool {
	return hb == nil || hb.Age() >= HeartbeatVeryStaleThreshold
}

// Touch writes a minimal heartbeat with just the timestamp.
// This is a convenience function for simple heartbeat updates.
func Touch(townRoot string) error {
	// Read existing heartbeat to increment cycle
	existing := ReadHeartbeat(townRoot)
	cycle := int64(1)
	if existing != nil {
		cycle = existing.Cycle + 1
	}

	return WriteHeartbeat(townRoot, &Heartbeat{
		Timestamp: time.Now().UTC(),
		Cycle:     cycle,
	})
}

// TouchWithAction writes a heartbeat with an action description.
func TouchWithAction(townRoot, action string, healthy, unhealthy int) error {
	existing := ReadHeartbeat(townRoot)
	cycle := int64(1)
	if existing != nil {
		cycle = existing.Cycle + 1
	}

	return WriteHeartbeat(townRoot, &Heartbeat{
		Timestamp:       time.Now().UTC(),
		Cycle:           cycle,
		LastAction:      action,
		HealthyAgents:   healthy,
		UnhealthyAgents: unhealthy,
	})
}
