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

	"github.com/jonbaldie/gastown/internal/boot"
	"github.com/jonbaldie/gastown/internal/tmux"
)

func writeFakeTmux(t *testing.T, dir string) {
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

# Keep session checks simple for this regression repro: no existing boot session.
if [[ "$cmd" == "has-session" ]]; then
  exit 1
fi

exit 0
`
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
}

// Regression test for gt-1z0:
// daemon should not spawn a fresh Boot session every heartbeat when triage was just run.
func TestEnsureBootRunning_DoesNotSpawnEveryTick(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}
	townRoot := t.TempDir()
	fakeBinDir := t.TempDir()
	tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
	if err := os.WriteFile(tmuxLog, []byte{}, 0o644); err != nil {
		t.Fatalf("create tmux log: %v", err)
	}

	writeFakeTmux(t, fakeBinDir)
	t.Setenv("PATH", fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_LOG", tmuxLog)
	t.Setenv("GT_DEGRADED", "false")

	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(io.Discard, "", 0),
		tmux:   tmux.NewTmux(),
	}

	// Simulate two adjacent heartbeats.
	d.ensureBootRunning()
	d.ensureBootRunning()

	data, err := os.ReadFile(tmuxLog)
	if err != nil {
		t.Fatalf("read tmux log: %v", err)
	}

	// Desired behavior (cooldown): single spawn in this short interval.
	// Current behavior: two spawns (fails here).
	spawns := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, "new-session ") {
			spawns++
		}
	}
	if spawns != 1 {
		t.Fatalf("boot spawn count = %d, want 1 (avoid spawning every daemon tick)", spawns)
	}
}

// Regression test for gt-qu883c:
// daemon should suppress Boot spawns when Boot's last action was "nothing" (deacon healthy).
func TestEnsureBootRunning_SuppressesWhenDeaconHealthy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}
	townRoot := t.TempDir()
	fakeBinDir := t.TempDir()
	tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
	if err := os.WriteFile(tmuxLog, []byte{}, 0o644); err != nil {
		t.Fatalf("create tmux log: %v", err)
	}

	writeFakeTmux(t, fakeBinDir)
	t.Setenv("PATH", fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_LOG", tmuxLog)
	t.Setenv("GT_DEGRADED", "false")

	// Write a boot-status.json indicating deacon was healthy ("nothing") recently.
	b := boot.New(townRoot)
	if err := b.SaveStatus(&boot.Status{
		StartedAt:   time.Now().Add(-30 * time.Second),
		CompletedAt: time.Now().Add(-20 * time.Second),
		LastAction:  "nothing",
	}); err != nil {
		t.Fatalf("save boot status: %v", err)
	}

	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(io.Discard, "", 0),
		tmux:   tmux.NewTmux(),
	}

	// Even though cooldown has expired (bootLastSpawned is zero),
	// idle suppression should prevent spawning.
	d.ensureBootRunning()

	data, err := os.ReadFile(tmuxLog)
	if err != nil {
		t.Fatalf("read tmux log: %v", err)
	}

	spawns := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, "new-session ") {
			spawns++
		}
	}
	if spawns != 0 {
		t.Fatalf("boot spawn count = %d, want 0 (should suppress when deacon healthy)", spawns)
	}
}

// Test that idle suppression does NOT prevent spawning when Boot's last action was not "nothing".
func TestEnsureBootRunning_SpawnsWhenDeaconUnhealthy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}
	townRoot := t.TempDir()
	fakeBinDir := t.TempDir()
	tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
	if err := os.WriteFile(tmuxLog, []byte{}, 0o644); err != nil {
		t.Fatalf("create tmux log: %v", err)
	}

	writeFakeTmux(t, fakeBinDir)
	t.Setenv("PATH", fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_LOG", tmuxLog)
	t.Setenv("GT_DEGRADED", "false")

	// Write a boot-status.json indicating Boot had to wake deacon recently.
	b := boot.New(townRoot)
	if err := b.SaveStatus(&boot.Status{
		StartedAt:   time.Now().Add(-30 * time.Second),
		CompletedAt: time.Now().Add(-20 * time.Second),
		LastAction:  "wake",
		Target:      "deacon",
	}); err != nil {
		t.Fatalf("save boot status: %v", err)
	}

	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(io.Discard, "", 0),
		tmux:   tmux.NewTmux(),
	}

	// When last action was "wake" (not "nothing"), Boot should still spawn.
	d.ensureBootRunning()

	data, err := os.ReadFile(tmuxLog)
	if err != nil {
		t.Fatalf("read tmux log: %v", err)
	}

	spawns := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, "new-session ") {
			spawns++
		}
	}
	if spawns != 1 {
		t.Fatalf("boot spawn count = %d, want 1 (should spawn when deacon was unhealthy)", spawns)
	}
}
