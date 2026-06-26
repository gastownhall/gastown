package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/doltserver"
)

func TestDoltServerPortUsesResolvedTownPortWhenManagerAbsent(t *testing.T) {
	townRoot := t.TempDir()
	statePath := filepath.Join(townRoot, "daemon", "dolt-state.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte(`{"running":true,"port":5517}`), 0644); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{config: &Config{TownRoot: townRoot}}
	if got := d.doltServerPort(); got != 5517 {
		t.Fatalf("doltServerPort() = %d, want 5517", got)
	}
}

func TestDoltServerPortPrefersEnabledManagerPort(t *testing.T) {
	townRoot := t.TempDir()
	statePath := filepath.Join(townRoot, "daemon", "dolt-state.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte(`{"running":true,"port":5517}`), 0644); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{
		config:     &Config{TownRoot: townRoot},
		doltServer: &DoltServerManager{config: &DoltServerConfig{Port: 6617}},
	}
	if got := d.doltServerPort(); got != 6617 {
		t.Fatalf("doltServerPort() = %d, want manager port 6617", got)
	}
}

func TestDoltServerPortFallsBackToDefaultPort(t *testing.T) {
	t.Setenv("GT_DOLT_PORT", "")
	d := &Daemon{config: &Config{TownRoot: t.TempDir()}}
	if got := d.doltServerPort(); got != doltserver.DefaultPort {
		t.Fatalf("doltServerPort() = %d, want default %d", got, doltserver.DefaultPort)
	}
}
