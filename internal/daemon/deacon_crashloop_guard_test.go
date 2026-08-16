package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/deacon"
	"github.com/jonbaldie/gastown/internal/tmux"
)

func writeFakeTmuxCrashLoop(t *testing.T, dir string) {
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

// Regression test for gt-d61:
// even when Deacon is in crash-loop state, stale-heartbeat fallback still kills session.
func TestCheckDeaconHeartbeat_RespectsCrashLoopGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}
	townRoot := t.TempDir()
	fakeBinDir := t.TempDir()
	tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
	if err := os.WriteFile(tmuxLog, []byte{}, 0o644); err != nil {
		t.Fatalf("create tmux log: %v", err)
	}

	writeFakeTmuxCrashLoop(t, fakeBinDir)
	t.Setenv("PATH", fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_LOG", tmuxLog)

	// Stale heartbeat triggers restart path.
	if err := deacon.WriteHeartbeat(townRoot, &deacon.Heartbeat{
		Timestamp: time.Now().Add(-20 * time.Minute),
		Cycle:     1,
	}); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}

	rt := NewRestartTracker(townRoot, RestartTrackerConfig{})
	rt.state.Agents["deacon"] = &AgentRestartInfo{
		CrashLoopSince: time.Now().Add(-5 * time.Minute),
	}

	d := &Daemon{
		config:         &Config{TownRoot: townRoot},
		logger:         log.New(io.Discard, "", 0),
		tmux:           tmux.NewTmux(),
		restartTracker: rt,
	}

	d.checkDeaconHeartbeat()

	data, err := os.ReadFile(tmuxLog)
	if err != nil {
		t.Fatalf("read tmux log: %v", err)
	}

	kills := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, "kill-session ") {
			kills++
		}
	}
	if kills != 0 {
		t.Fatalf("kill-session count = %d, want 0 while crash-loop guard is active", kills)
	}
}
