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

	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/tmux"
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
		config:        &Config{TownRoot: townRoot},
		logger:        log.New(io.Discard, "", 0),
		tmux:          tmux.NewTmux(),
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

// Regression test for op-r9fx: a fresh heartbeat while in crash-loop state
// means the Deacon recovered (e.g. it was revived manually). Once the
// stability period has passed since the last restart attempt, the flag must
// clear and be persisted — before this fix the guard blocked the only code
// path that could ever clear it, so the flag was permanent (6 weeks live).
func TestCheckDeaconHeartbeat_ClearsCrashLoopOnFreshHeartbeat(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "daemon"), 0o755); err != nil {
		t.Fatalf("mkdir daemon dir: %v", err)
	}

	// Fresh heartbeat: the Deacon is alive and patrolling.
	if err := deacon.WriteHeartbeat(townRoot, &deacon.Heartbeat{
		Timestamp: time.Now(),
		Cycle:     42,
	}); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}

	// Crash loop entered 45m ago (still within the 1h expiry), last restart
	// attempt 45m ago (past the 30m stability period).
	rt := NewRestartTracker(townRoot, RestartTrackerConfig{})
	rt.state.Agents["deacon"] = &AgentRestartInfo{
		LastRestart:    time.Now().Add(-45 * time.Minute),
		RestartCount:   5,
		CrashLoopSince: time.Now().Add(-45 * time.Minute),
	}

	d := &Daemon{
		config:         &Config{TownRoot: townRoot},
		logger:         log.New(io.Discard, "", 0),
		tmux:           tmux.NewTmux(),
		restartTracker: rt,
	}

	d.checkDeaconHeartbeat()

	if rt.IsInCrashLoop("deacon") {
		t.Fatal("crash-loop state not cleared despite fresh heartbeat past stability period")
	}

	// The clear must be persisted so a daemon restart doesn't resurrect it.
	reloaded := NewRestartTracker(townRoot, RestartTrackerConfig{})
	if err := reloaded.Load(); err != nil {
		t.Fatalf("reload restart state: %v", err)
	}
	if reloaded.IsInCrashLoop("deacon") {
		t.Fatal("cleared crash-loop state was not persisted to disk")
	}
}

// Regression test for op-r9fx: the crash-loop guard must log state
// transitions, not one line per heartbeat cycle (16k+ spam lines over six
// weeks of "Deacon is in crash-loop state, skipping heartbeat kill check").
func TestCheckDeaconHeartbeat_LogsCrashLoopOncePerTransition(t *testing.T) {
	townRoot := t.TempDir()

	// Stale heartbeat: the Deacon is down, so the guard stays active.
	if err := deacon.WriteHeartbeat(townRoot, &deacon.Heartbeat{
		Timestamp: time.Now().Add(-20 * time.Minute),
		Cycle:     1,
	}); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}

	rt := NewRestartTracker(townRoot, RestartTrackerConfig{})
	rt.state.Agents["deacon"] = &AgentRestartInfo{
		LastRestart:    time.Now().Add(-5 * time.Minute),
		RestartCount:   5,
		CrashLoopSince: time.Now().Add(-5 * time.Minute),
	}

	var buf strings.Builder
	d := &Daemon{
		config:         &Config{TownRoot: townRoot},
		logger:         log.New(&buf, "", 0),
		tmux:           tmux.NewTmux(),
		restartTracker: rt,
	}

	for i := 0; i < 3; i++ {
		d.checkDeaconHeartbeat()
	}

	logged := strings.Count(buf.String(), "entered crash-loop state")
	if logged != 1 {
		t.Fatalf("crash-loop guard logged %d times over 3 cycles, want exactly 1 (transition only):\n%s", logged, buf.String())
	}

	// Clear the flag (fresh heartbeat keeps the post-guard path away from
	// tmux); the next cycle must log the cleared transition exactly once.
	rt.ClearCrashLoop("deacon")
	if err := deacon.WriteHeartbeat(townRoot, &deacon.Heartbeat{
		Timestamp: time.Now(),
		Cycle:     2,
	}); err != nil {
		t.Fatalf("write fresh heartbeat: %v", err)
	}
	d.checkDeaconHeartbeat()
	d.checkDeaconHeartbeat()

	cleared := strings.Count(buf.String(), "crash-loop state cleared")
	if cleared != 1 {
		t.Fatalf("crash-loop clear logged %d times, want exactly 1 (transition only):\n%s", cleared, buf.String())
	}
}
