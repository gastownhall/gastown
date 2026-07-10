package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/deacon"
)

// stubBeadSync replaces the agent-bead heartbeat sync (which shells out to bd)
// for the duration of a test and reports whether it was invoked.
func stubBeadSync(t *testing.T) *bool {
	t.Helper()
	called := false
	orig := deaconAgentBeadHeartbeatSync
	deaconAgentBeadHeartbeatSync = func(string) { called = true }
	t.Cleanup(func() { deaconAgentBeadHeartbeatSync = orig })
	return &called
}

func TestStampDeaconHeartbeatOnReport_StampsAllStores(t *testing.T) {
	townRoot := t.TempDir()
	synced := stubBeadSync(t)

	stampDeaconHeartbeatOnReport(townRoot, "all clear")

	hb := deacon.ReadHeartbeat(townRoot)
	if hb == nil {
		t.Fatal("heartbeat.json not written after successful patrol report")
	}
	if !hb.IsFresh() {
		t.Errorf("heartbeat not fresh: timestamp %v", hb.Timestamp)
	}
	if want := "patrol report: all clear"; hb.LastAction != want {
		t.Errorf("LastAction = %q, want %q", hb.LastAction, want)
	}

	legacy := filepath.Join(townRoot, "deacon", ".deacon-heartbeat")
	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("legacy .deacon-heartbeat not touched: %v", err)
	}
	if !*synced {
		t.Error("agent-bead heartbeat sync not invoked")
	}
}

func TestStampDeaconHeartbeatOnReport_SkipsWhenPaused(t *testing.T) {
	townRoot := t.TempDir()
	if err := deacon.Pause(townRoot, "test pause", "test"); err != nil {
		t.Fatalf("pausing deacon: %v", err)
	}
	synced := stubBeadSync(t)

	stampDeaconHeartbeatOnReport(townRoot, "all clear")

	if deacon.ReadHeartbeat(townRoot) != nil {
		t.Error("heartbeat written while Deacon is paused")
	}
	if *synced {
		t.Error("agent-bead heartbeat sync must not run while paused")
	}
}
