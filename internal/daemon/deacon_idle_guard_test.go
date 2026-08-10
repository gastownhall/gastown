package daemon

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	beadsdk "github.com/steveyegge/beads"
)

// writeFakeTmuxWithSession creates a fake tmux binary that reports the Deacon
// session as existing (has-session returns 0). Used for deacon idle guard tests
// where the session must be present so checkDeaconHeartbeat reaches the nudge path.
func writeFakeTmuxWithSession(t *testing.T, dir string) {
	t.Helper()
	script := `#!/usr/bin/env bash
set -euo pipefail

cmd=""
skip_next=0
for arg in "$@"; do
  if [[ "$skip_next" -eq 1 ]]; then
    skip_next=0
    continue
  fi
  if [[ "$arg" == "-u" ]]; then
    continue
  fi
  if [[ "$arg" == "-L" ]]; then
    skip_next=1
    continue
  fi
  cmd="$arg"
  break
done

if [[ -n "${TMUX_LOG:-}" ]]; then
  printf "%s %s\n" "$cmd" "$*" >> "$TMUX_LOG"
fi

if [[ "${1:-}" == "-V" ]]; then
  echo "tmux 3.3a"
  exit 0
fi

# Session exists: has-session returns 0 so the nudge path is reachable.
if [[ "$cmd" == "has-session" ]]; then
  exit 0
fi

exit 0
`
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
}

// TestCheckDeaconHeartbeat_IdleGuard verifies that the nudge is suppressed when
// the Deacon heartbeat is stale but no active work is in flight (idle guard).
func TestCheckDeaconHeartbeat_IdleGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}

	tests := []struct {
		name             string
		heartbeatAge     time.Duration
		stores           map[string]beadsdk.Storage
		wantNudgeLog     bool
		wantIdleGuardLog bool
		wantRestartLog   bool
		desc             string
	}{
		{
			name:         "idle: stale heartbeat, no work — nudge suppressed",
			heartbeatAge: 10 * time.Minute,
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{results: map[string][]*beadsdk.Issue{}},
			},
			wantNudgeLog:     false,
			wantIdleGuardLog: true,
			desc:             "Idle guard must suppress nudge when no work is in flight",
		},
		{
			name:         "active work: stale heartbeat, in_progress bead — nudge sent",
			heartbeatAge: 10 * time.Minute,
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{results: map[string][]*beadsdk.Issue{
					"in_progress": {{ID: "sc-abc"}},
				}},
			},
			wantNudgeLog:     true,
			wantIdleGuardLog: false,
			desc:             "Nudge must fire when in_progress work exists",
		},
		{
			name:         "hooked only: stale heartbeat, patrol wisp — nudge suppressed",
			heartbeatAge: 10 * time.Minute,
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{results: map[string][]*beadsdk.Issue{
					"hooked": {{ID: "hq-wisp-34zi"}},
				}},
			},
			wantNudgeLog:     false,
			wantIdleGuardLog: true,
			desc:             "Patrol wisps in hooked state do not count as active work; nudge must be suppressed",
		},
		{
			name:         "store error: stale heartbeat, store fails — nudge sent conservatively",
			heartbeatAge: 10 * time.Minute,
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{err: fmt.Errorf("db offline")},
			},
			wantNudgeLog:     true,
			wantIdleGuardLog: false,
			desc:             "Nudge must fire conservatively when work state is unknown",
		},
		{
			name:         "very stale + idle: heartbeat >= 20 min, no work — must NOT be killed",
			heartbeatAge: 21 * time.Minute,
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{results: map[string][]*beadsdk.Issue{}},
			},
			wantNudgeLog:     false,
			wantIdleGuardLog: true,
			wantRestartLog:   false,
			desc:             "Idle guard must cover the kill tier too: an idle Deacon crosses 20min by having nothing to do, and restarting it destroys a healthy session for no gain",
		},
		{
			name:         "very stale + active work: heartbeat >= 20 min, in_progress — genuinely stuck, restart",
			heartbeatAge: 21 * time.Minute,
			stores: map[string]beadsdk.Storage{
				"hq": &searchStorage{results: map[string][]*beadsdk.Issue{
					"in_progress": {{ID: "sc-stuck"}},
				}},
			},
			wantNudgeLog:     false,
			wantIdleGuardLog: false,
			wantRestartLog:   true,
			desc:             "A Deacon holding in-flight work past the kill threshold is genuinely stuck and must still be restarted",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			townRoot := t.TempDir()
			fakeBinDir := t.TempDir()
			tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
			if err := os.WriteFile(tmuxLog, []byte{}, 0o644); err != nil {
				t.Fatalf("create tmux log: %v", err)
			}

			writeFakeTmuxWithSession(t, fakeBinDir)
			t.Setenv("PATH", fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TMUX_LOG", tmuxLog)

			writeDeaconHeartbeat(t, townRoot, tc.heartbeatAge)

			d := newTestDaemonWithStores(t, townRoot, tc.stores)

			logBuf := &strings.Builder{}
			d.logger = log.New(logBuf, "", 0)

			d.checkDeaconHeartbeat()

			logOutput := logBuf.String()

			hasIdleGuardLog := strings.Contains(logOutput, "idle, not stuck")
			if hasIdleGuardLog != tc.wantIdleGuardLog {
				t.Errorf("%s\nidle guard log present=%v, want=%v\nlog:\n%s",
					tc.desc, hasIdleGuardLog, tc.wantIdleGuardLog, logOutput)
			}

			hasNudgeLog := strings.Contains(logOutput, "nudging session")
			if hasNudgeLog != tc.wantNudgeLog {
				t.Errorf("%s\nnudge log present=%v, want=%v\nlog:\n%s",
					tc.desc, hasNudgeLog, tc.wantNudgeLog, logOutput)
			}

			hasRestartLog := strings.Contains(logOutput, "STUCK DEACON: heartbeat stale")
			if hasRestartLog != tc.wantRestartLog {
				t.Errorf("%s\nrestart log present=%v, want=%v\nlog:\n%s",
					tc.desc, hasRestartLog, tc.wantRestartLog, logOutput)
			}
		})
	}
}
