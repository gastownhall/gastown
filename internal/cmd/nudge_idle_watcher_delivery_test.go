package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/nudge"
	"github.com/steveyegge/gastown/internal/tmux"
)

// TestDeliverNudge_WaitIdle_IdleWatcherSendKeysFailure_ReturnsError replays
// GH#4666: busy-target wait-idle falls through to the idle-watcher, send-keys
// then fails with the tmux 3.7b "unknown flag -V" error, and gt still reports
// success. The loop is red while deliverNudge returns nil after that failure.
func TestDeliverNudge_WaitIdle_IdleWatcherSendKeysFailure_ReturnsError(t *testing.T) {
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}

	origMode := nudgeModeFlag
	origPriority := nudgePriorityFlag
	origWait := waitIdleTimeout
	origWatch := idleWatcherTimeout
	origInterval := idleWatcherPollInterval
	t.Cleanup(func() {
		nudgeModeFlag = origMode
		nudgePriorityFlag = origPriority
		waitIdleTimeout = origWait
		idleWatcherTimeout = origWatch
		idleWatcherPollInterval = origInterval
	})

	t.Setenv("GT_TEST_NUDGE_LOG", "")
	t.Setenv("GT_REAL_TMUX", realTmux)
	t.Setenv("GT_TMUX_FAIL_SEND_KEYS", "1")

	townRoot := setupTestTownForConfig(t)
	t.Chdir(townRoot)

	socket := isolatedTmuxSocketName(t)
	sessionName := "hq-mayor"
	startIsolatedTmuxSession(t, realTmux, socket, sessionName, "printf '❯ \\n'; exec cat")

	stub := installTmuxStub(t)
	tm := tmux.NewTmuxWithSocketAndBinary(socket, stub)

	nudgeModeFlag = NudgeModeWaitIdle
	nudgePriorityFlag = nudge.PriorityNormal
	// Force the idle-watcher path: the first WaitForIdle cannot complete two
	// consecutive polls inside 1ms, so wait-idle queues and watchAndDeliver
	// is the delivery attempt that hits the send-keys failure.
	waitIdleTimeout = time.Millisecond
	idleWatcherTimeout = 5 * time.Second
	idleWatcherPollInterval = 800 * time.Millisecond

	var deliverErr error
	stderrText := captureStderr(t, func() {
		deliverErr = deliverNudge(tm, sessionName, "hello from 4666", "test")
	})

	if !strings.Contains(stderrText, "idle-watcher: delivery for "+sessionName+" failed") {
		t.Fatalf("expected idle-watcher delivery failure log, stderr=%q err=%v", stderrText, deliverErr)
	}
	if !strings.Contains(stderrText, "unknown flag -V") {
		t.Fatalf("expected tmux 3.7b unknown-flag symptom, stderr=%q", stderrText)
	}

	// User-facing symptom: gt prints ✓ Nudged and exits 0 because this error
	// is swallowed. The contract is that a hard send-keys failure is not success.
	if deliverErr == nil {
		t.Fatalf("deliverNudge returned nil after idle-watcher send-keys failure; nudge was reported successful. stderr=%q queueLen=%d",
			stderrText, nudge.QueueLen(townRoot, sessionName))
	}

	if got := nudge.QueueLen(townRoot, sessionName); got == 0 {
		t.Fatalf("hard delivery error dropped the queued nudge (queue empty, only lock may remain)")
	}
}

func isolatedTmuxSocketName(t *testing.T) string {
	t.Helper()
	return "gt4666-" + strings.ReplaceAll(t.Name(), "/", "-")
}

func startIsolatedTmuxSession(t *testing.T, realTmux, socket, sessionName, shellCmd string) {
	t.Helper()
	cmd := exec.Command(realTmux, "-u", "-L", socket, "new-session", "-d", "-s", sessionName, "sh", "-c", shellCmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command(realTmux, "-L", socket, "kill-server").Run()
	})
}

func installTmuxStub(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src := filepath.Join(filepath.Dir(file), "..", "tmux", "testdata", "tmuxstub.sh")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read tmux stub: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatalf("write tmux stub: %v", err)
	}
	return dst
}
