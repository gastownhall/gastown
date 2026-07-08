package polecat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// writeBackdatedHeartbeat writes a heartbeat file whose timestamp is `age` in the past.
func writeBackdatedHeartbeat(t *testing.T, townRoot, sessionName string, age time.Duration) {
	t.Helper()
	dir := filepath.Join(townRoot, ".runtime", "heartbeats")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	hb := SessionHeartbeat{Timestamp: time.Now().UTC().Add(-age), State: HeartbeatWorking}
	data, err := json.Marshal(hb)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionName+".json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

// TestIsSessionHeartbeatStale_HonorsConfiguredThreshold verifies that
// operational.polecat.heartbeat_stale_threshold in settings/config.json is
// actually read (regression fixed 2026-07-08: the accessor existed but was
// never called, so the compiled-in 3m constant always won).
func TestIsSessionHeartbeatStale_HonorsConfiguredThreshold(t *testing.T) {
	townRoot := t.TempDir()
	writeTownHeartbeatConfig(t, townRoot, "10m")
	sessionName := "gt-test-hb-config"

	if got := SessionHeartbeatStaleThresholdFor(townRoot); got != 10*time.Minute {
		t.Fatalf("SessionHeartbeatStaleThresholdFor = %v, want 10m", got)
	}

	// 5 minutes old: stale under the 3m compiled default, fresh under the
	// configured 10m threshold.
	writeBackdatedHeartbeat(t, townRoot, sessionName, 5*time.Minute)
	stale, exists := IsSessionHeartbeatStale(townRoot, sessionName)
	if !exists {
		t.Fatal("expected heartbeat to exist")
	}
	if stale {
		t.Error("5m-old heartbeat must be fresh under configured 10m threshold")
	}

	// 15 minutes old: stale even under the configured threshold.
	writeBackdatedHeartbeat(t, townRoot, sessionName, 15*time.Minute)
	stale, exists = IsSessionHeartbeatStale(townRoot, sessionName)
	if !exists {
		t.Fatal("expected heartbeat to exist")
	}
	if !stale {
		t.Error("15m-old heartbeat must be stale under configured 10m threshold")
	}
}

// TestSessionHeartbeatStaleThresholdFor_Defaults verifies fallback behavior
// when no config is present or townRoot is unknown.
func TestSessionHeartbeatStaleThresholdFor_Defaults(t *testing.T) {
	if got := SessionHeartbeatStaleThresholdFor(""); got != SessionHeartbeatStaleThreshold {
		t.Errorf("empty townRoot: got %v, want compiled default %v", got, SessionHeartbeatStaleThreshold)
	}
	if got := SessionHeartbeatStaleThresholdFor(t.TempDir()); got != SessionHeartbeatStaleThreshold {
		t.Errorf("no config file: got %v, want compiled default %v", got, SessionHeartbeatStaleThreshold)
	}
}
