package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/nudge"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/jonbaldie/gastown/internal/tmux/tmuxtest"
)

// TestDeliverNudge_WaitIdle_IdleWatcherSendKeysFailure_ReturnsError replays
// GH#4666: busy-target wait-idle falls through to the idle-watcher, send-keys
// then fails with the tmux 3.7b "unknown flag -V" error. The contract is that
// deliverNudge returns that failure with operation and session context, and
// the drained nudge is requeued.
func TestDeliverNudge_WaitIdle_IdleWatcherSendKeysFailure_ReturnsError(t *testing.T) {
	townRoot, sessionName, tm := setupWaitIdleSendKeysFailure(t, time.Millisecond)

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

	if deliverErr == nil {
		t.Fatalf("deliverNudge returned nil after idle-watcher send-keys failure; nudge was reported successful. stderr=%q queueLen=%d",
			stderrText, nudge.QueueLen(townRoot, sessionName))
	}
	if !strings.Contains(deliverErr.Error(), "idle-watcher") {
		t.Fatalf("delivery error missing operation context: %v", deliverErr)
	}
	if !strings.Contains(deliverErr.Error(), sessionName) {
		t.Fatalf("delivery error missing session resource context: %v", deliverErr)
	}

	if got := nudge.QueueLen(townRoot, sessionName); got == 0 {
		t.Fatalf("hard delivery error dropped the queued nudge (queue empty, only lock may remain)")
	}
}

// TestDeliverNudge_WaitIdle_DirectSendKeysFailure_QueuesAndReturnsError covers
// the idle-direct path: WaitForIdle succeeds, send-keys fails, and the hard
// error must queue then return so the message is not lost and gt does not
// print success.
func TestDeliverNudge_WaitIdle_DirectSendKeysFailure_QueuesAndReturnsError(t *testing.T) {
	townRoot, sessionName, tm := setupWaitIdleSendKeysFailure(t, 5*time.Second)

	var deliverErr error
	stderrText := captureStderr(t, func() {
		deliverErr = deliverNudge(tm, sessionName, "hello from 4666", "test")
	})

	if !strings.Contains(stderrText, "wait-idle: delivery for "+sessionName+" failed") {
		t.Fatalf("expected wait-idle delivery failure log, stderr=%q err=%v", stderrText, deliverErr)
	}
	if deliverErr == nil {
		t.Fatalf("deliverNudge returned nil after idle-direct send-keys failure. stderr=%q queueLen=%d",
			stderrText, nudge.QueueLen(townRoot, sessionName))
	}
	if !strings.Contains(deliverErr.Error(), "wait-idle") {
		t.Fatalf("delivery error missing operation context: %v", deliverErr)
	}
	if !strings.Contains(deliverErr.Error(), sessionName) {
		t.Fatalf("delivery error missing session resource context: %v", deliverErr)
	}
	if got := nudge.QueueLen(townRoot, sessionName); got == 0 {
		t.Fatalf("hard delivery error dropped the nudge (queue empty)")
	}
}

// TestRecoverFailedWatcherDelivery_RequeueFailure_ImmediateFallback covers
// the issue's last-resort fallback: when watcher delivery fails and requeue
// also fails, attempt immediate delivery so the drained nudge is not lost.
func TestRecoverFailedWatcherDelivery_RequeueFailure_ImmediateFallback(t *testing.T) {
	townRoot, sessionName, tm := setupWaitIdleSendKeysFailure(t, time.Millisecond)
	drained := []nudge.QueuedNudge{{
		Sender:   "test",
		Message:  "hello from 4666",
		Priority: nudge.PriorityNormal,
	}}
	queueDir := filepath.Join(townRoot, constants.DirRuntime, "nudge_queue", sessionName)
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatalf("mkdir queue dir: %v", err)
	}
	if err := os.RemoveAll(queueDir); err != nil {
		t.Fatalf("remove queue dir: %v", err)
	}
	if err := os.WriteFile(queueDir, []byte("not-a-directory"), 0o644); err != nil {
		t.Fatalf("block requeue: %v", err)
	}

	var recoverErr error
	stderrText := captureStderr(t, func() {
		recoverErr = recoverFailedWatcherDelivery(tm, townRoot, sessionName, drained,
			fmt.Errorf("tmux send-keys: command send-keys: unknown flag -V"))
	})

	if !strings.Contains(stderrText, "delivering immediately") {
		t.Fatalf("expected immediate-delivery fallback after requeue failure, stderr=%q err=%v", stderrText, recoverErr)
	}
	if recoverErr == nil {
		t.Fatalf("send-keys still fails on immediate fallback; must not report success. stderr=%q", stderrText)
	}
	if !strings.Contains(recoverErr.Error(), "idle-watcher") {
		t.Fatalf("delivery error missing operation context: %v", recoverErr)
	}
	if !strings.Contains(recoverErr.Error(), sessionName) {
		t.Fatalf("delivery error missing session resource context: %v", recoverErr)
	}
	if !strings.Contains(recoverErr.Error(), "requeue failed") {
		t.Fatalf("delivery error missing requeue failure: %v", recoverErr)
	}
}

func setupWaitIdleSendKeysFailure(t *testing.T, waitIdle time.Duration) (townRoot, sessionName string, tm *tmux.Tmux) {
	t.Helper()
	realTmux := tmuxtest.RealTmuxOrSkip(t)

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

	townRoot = setupTestTownForConfig(t)
	t.Chdir(townRoot)

	socket := tmuxtest.SocketName(t, "gt4666-")
	sessionName = "hq-mayor"
	tmuxtest.StartSession(t, realTmux, socket, sessionName, "printf '❯ \\n'; exec cat")

	stub := tmuxtest.InstallStub(t)
	tm = tmux.NewTmuxWithSocketAndBinary(socket, stub)

	nudgeModeFlag = NudgeModeWaitIdle
	nudgePriorityFlag = nudge.PriorityNormal
	waitIdleTimeout = waitIdle
	idleWatcherTimeout = 5 * time.Second
	idleWatcherPollInterval = 800 * time.Millisecond
	return townRoot, sessionName, tm
}
