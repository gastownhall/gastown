package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

// writeFakeBDAllFail creates a "bd" script where EVERY invocation fails,
// simulating a full Dolt outage (both agent-bead show and work-bead list).
func writeFakeBDAllFail(t *testing.T, dir string) string {
	t.Helper()
	script := "#!/bin/sh\nexit 1\n"
	path := filepath.Join(dir, "bd")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("writing fake bd: %v", err)
	}
	return path
}

func writeStaleWorkingHeartbeat(t *testing.T, townRoot, sessionName string, age time.Duration) {
	t.Helper()
	hbPath := filepath.Join(townRoot, ".runtime", "heartbeats", sessionName+".json")
	if err := os.MkdirAll(filepath.Dir(hbPath), 0755); err != nil {
		t.Fatal(err)
	}
	hb := polecat.SessionHeartbeat{
		Timestamp: time.Now().UTC().Add(-age),
		State:     polecat.HeartbeatWorking,
	}
	data, err := json.Marshal(hb)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hbPath, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// TestReapIdlePolecat_SkipsWhenAllBeadLookupsFail verifies the fail-safe rule
// from the 2026-07-08 incidents: when BOTH the agent-bead lookup and the
// work-bead verification fail (full Dolt outage), the polecat's work-state is
// UNKNOWN and the reaper must NOT kill — no matter how stale the heartbeat is,
// and even when the agent process is not detectable.
func TestReapIdlePolecat_SkipsWhenAllBeadLookupsFail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for tmux and bd")
	}
	old := session.DefaultRegistry()
	reg := session.NewPrefixRegistry()
	reg.Register("myr", "myr")
	session.SetDefaultRegistry(reg)
	defer session.SetDefaultRegistry(old)

	binDir := t.TempDir()
	writeFakeTmuxIdleSession(t, binDir) // session alive, agent NOT detectable
	bdPath := writeFakeBDAllFail(t, binDir)

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	townRoot := t.TempDir()
	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(&logBuf, "", 0),
		tmux:   tmux.NewTmuxWithSocket(""),
		bdPath: bdPath,
	}

	// 10x the timeout — extreme staleness must still not override the fail-safe.
	writeStaleWorkingHeartbeat(t, townRoot, "myr-mycat", 150*time.Minute)

	d.reapIdlePolecat("myr", "mycat", 15*time.Minute)

	got := logBuf.String()
	if strings.Contains(got, "Reaping idle polecat") {
		t.Errorf("must NOT reap when work-state is unknown (all bead lookups failed), got: %q", got)
	}
	if !strings.Contains(got, "failing safe") {
		t.Errorf("expected a fail-safe skip log line, got: %q", got)
	}
}

// TestReapIdlePolecat_SkipsAliveAgentWhenBeadLookupFails verifies that a
// session with a live agent process is never reaped in the bead-lookup-failed
// path, even when the work-bead check verifies no assigned work and the
// heartbeat is past every staleness multiple. (Before this fix, the 3x-threshold
// arm killed regardless of agent liveness.)
func TestReapIdlePolecat_SkipsAliveAgentWhenBeadLookupFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for tmux and bd")
	}
	old := session.DefaultRegistry()
	reg := session.NewPrefixRegistry()
	reg.Register("myr", "myr")
	session.SetDefaultRegistry(reg)
	defer session.SetDefaultRegistry(old)

	binDir := t.TempDir()
	writeFakeTmuxWithAgent(t, binDir, "claude") // agent process IS running
	bdPath := writeFakeBDLookupFail(t, binDir, false /* verified: no work */)

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	townRoot := t.TempDir()
	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(&logBuf, "", 0),
		tmux:   tmux.NewTmuxWithSocket(""),
		bdPath: bdPath,
	}

	writeStaleWorkingHeartbeat(t, townRoot, "myr-mycat", 60*time.Minute) // 4x threshold

	d.reapIdlePolecat("myr", "mycat", 15*time.Minute)

	if strings.Contains(logBuf.String(), "Reaping idle polecat") {
		t.Errorf("must NOT reap a session with a live agent process on bead-lookup failure, got: %q", logBuf.String())
	}
}

// TestIsRigBeadNotFound verifies the classification that keeps isRigOperational
// from conflating a definitively-missing rig bead with a Dolt outage (2026-07-08:
// missing cap-rig-capital excluded the rig from witness patrol indefinitely).
func TestIsRigBeadNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"sentinel", beads.ErrNotFound, true},
		{"wrapped sentinel", fmt.Errorf("show cap-rig-capital: %w", beads.ErrNotFound), true},
		{"cli not found text", errors.New(`no issue found matching "cap-rig-capital"`), true},
		{"cli plural text", errors.New("no issues found matching the provided IDs"), true},
		{"transport timeout", errors.New("read tcp 127.0.0.1:37204->127.0.0.1:3307: i/o timeout"), false},
		{"connection refused", errors.New("dial tcp 127.0.0.1:3307: connect: connection refused"), false},
		{"eof", io.EOF, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRigBeadNotFound(tc.err); got != tc.want {
				t.Errorf("isRigBeadNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
