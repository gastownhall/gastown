package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/deacon"
)

func TestStampDeaconHeartbeatOnReport_StampsAllStores(t *testing.T) {
	townRoot := t.TempDir()
	syncs := 0
	oldSync := deaconAgentBeadHeartbeatSync
	deaconAgentBeadHeartbeatSync = func(string) { syncs++ }
	t.Cleanup(func() { deaconAgentBeadHeartbeatSync = oldSync })

	stampDeaconHeartbeatOnReport(townRoot, "all clear")

	hb := deacon.ReadHeartbeat(townRoot)
	if hb == nil {
		t.Fatal("expected heartbeat file")
	}
	if hb.LastAction != "patrol report: all clear" {
		t.Fatalf("LastAction = %q, want patrol report summary", hb.LastAction)
	}
	if _, err := os.Stat(filepath.Join(townRoot, "deacon", ".deacon-heartbeat")); err != nil {
		t.Fatalf("expected legacy heartbeat file: %v", err)
	}
	if syncs != 1 {
		t.Fatalf("agent bead syncs = %d, want 1", syncs)
	}
}

func TestStampDeaconHeartbeatOnReport_SkipsWhenPaused(t *testing.T) {
	townRoot := t.TempDir()
	syncs := 0
	oldSync := deaconAgentBeadHeartbeatSync
	deaconAgentBeadHeartbeatSync = func(string) { syncs++ }
	t.Cleanup(func() { deaconAgentBeadHeartbeatSync = oldSync })
	if err := deacon.Pause(townRoot, "maintenance", "test"); err != nil {
		t.Fatal(err)
	}

	stampDeaconHeartbeatOnReport(townRoot, "paused")

	if hb := deacon.ReadHeartbeat(townRoot); hb != nil {
		t.Fatalf("expected no heartbeat when paused, got %+v", hb)
	}
	if syncs != 0 {
		t.Fatalf("agent bead syncs = %d, want 0", syncs)
	}
}

func TestStampDeaconHeartbeatOnReport_SkipsOnCorruptPauseFile(t *testing.T) {
	townRoot := t.TempDir()
	syncs := 0
	oldSync := deaconAgentBeadHeartbeatSync
	deaconAgentBeadHeartbeatSync = func(string) { syncs++ }
	t.Cleanup(func() { deaconAgentBeadHeartbeatSync = oldSync })
	pauseFile := deacon.GetPauseFile(townRoot)
	if err := os.MkdirAll(filepath.Dir(pauseFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pauseFile, []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}

	stampDeaconHeartbeatOnReport(townRoot, "corrupt")

	if hb := deacon.ReadHeartbeat(townRoot); hb != nil {
		t.Fatalf("expected no heartbeat on corrupt pause state, got %+v", hb)
	}
	if syncs != 0 {
		t.Fatalf("agent bead syncs = %d, want 0", syncs)
	}
}
