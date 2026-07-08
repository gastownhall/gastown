package witness

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/polecat"
)

// writeTownHeartbeatConfig writes a settings/config.json with the given
// operational.polecat.heartbeat_stale_threshold value.
func writeTownHeartbeatConfig(t *testing.T, townRoot, threshold string) {
	t.Helper()
	settingsDir := filepath.Join(townRoot, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"type":"town-settings","version":1,"operational":{"polecat":{"heartbeat_stale_threshold":"` + threshold + `"}}}`
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestWitnessHeartbeatStale_HonorsConfiguredThreshold verifies that witness
// heartbeat verdicts (zombie + stall detection) honor
// operational.polecat.heartbeat_stale_threshold instead of the compiled 3m
// constant. Regression: the daemon/allocator side was fixed to read the config
// but the witness callsites kept comparing against the bare constant, so the
// two components could disagree about whether the same session was alive.
func TestWitnessHeartbeatStale_HonorsConfiguredThreshold(t *testing.T) {
	townRoot := t.TempDir()
	writeTownHeartbeatConfig(t, townRoot, "10m")

	// 5 minutes old: stale under the 3m compiled default, fresh under the
	// configured 10m threshold.
	hb := &polecat.SessionHeartbeat{
		Timestamp: time.Now().UTC().Add(-5 * time.Minute),
		State:     polecat.HeartbeatWorking,
	}
	if witnessHeartbeatStale(townRoot, hb) {
		t.Error("5m-old heartbeat must be fresh under configured 10m threshold")
	}

	// 15 minutes old: stale even under the configured threshold.
	hb.Timestamp = time.Now().UTC().Add(-15 * time.Minute)
	if !witnessHeartbeatStale(townRoot, hb) {
		t.Error("15m-old heartbeat must be stale under configured 10m threshold")
	}
}

// TestWitnessHeartbeatStale_DefaultsWithoutConfig verifies the compiled 3m
// default still applies when no operational config is present.
func TestWitnessHeartbeatStale_DefaultsWithoutConfig(t *testing.T) {
	townRoot := t.TempDir()

	hb := &polecat.SessionHeartbeat{
		Timestamp: time.Now().UTC().Add(-polecat.SessionHeartbeatStaleThreshold + 30*time.Second),
		State:     polecat.HeartbeatWorking,
	}
	if witnessHeartbeatStale(townRoot, hb) {
		t.Error("heartbeat younger than the compiled default must be fresh without config")
	}

	hb.Timestamp = time.Now().UTC().Add(-polecat.SessionHeartbeatStaleThreshold - 30*time.Second)
	if !witnessHeartbeatStale(townRoot, hb) {
		t.Error("heartbeat older than the compiled default must be stale without config")
	}
}
