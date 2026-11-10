package daemon

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// TestDaemon_DoltRemotesSkippedByDefault pins that the DoltHub push patrol
// is opt-in (spec §9, ruling R30): with nil or default config the push is
// never attempted. pushDoltRemotes logs a "dolt_remotes:" line as soon as it
// gets past the patrol gate, so an empty log means the push was skipped.
func TestDaemon_DoltRemotesSkippedByDefault(t *testing.T) {
	cases := []struct {
		name   string
		config *DaemonPatrolConfig
	}{
		{"nil config", nil},
		{"default lifecycle config", DefaultLifecycleConfig()},
		{"empty patrols", &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			d := &Daemon{logger: log.New(&buf, "", 0), patrolConfig: tc.config}
			d.pushDoltRemotes()
			if strings.Contains(buf.String(), "dolt_remotes:") {
				t.Fatalf("push attempted with default config; log: %q", buf.String())
			}
		})
	}

	// Control: explicitly enabled gets past the gate (and then stops because
	// no dolt server is configured), proving the log probe is meaningful.
	t.Run("explicitly enabled", func(t *testing.T) {
		var buf bytes.Buffer
		d := &Daemon{
			logger: log.New(&buf, "", 0),
			patrolConfig: &DaemonPatrolConfig{Patrols: &PatrolsConfig{
				DoltRemotes: &DoltRemotesConfig{Enabled: true},
			}},
		}
		d.pushDoltRemotes()
		if !strings.Contains(buf.String(), "dolt_remotes:") {
			t.Fatalf("expected enabled patrol to pass the gate; log: %q", buf.String())
		}
	})
}
