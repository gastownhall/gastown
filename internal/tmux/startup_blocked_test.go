package tmux

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/constants"
)

// TestWrapErrorClassifiesMissingPane pins the classification that hq-bcowvq
// turned on: tmux names a missing target after the command's target type, so a
// session-scoped command says "can't find session" while capture-pane and
// send-keys say "can't find pane". Both mean the session is gone and both must
// map to ErrSessionNotFound.
func TestWrapErrorClassifiesMissingPane(t *testing.T) {
	tm := NewTmux()
	cases := []struct {
		name   string
		stderr string
		want   error
	}{
		{"missing session", "can't find session: hq-dog-alpha", ErrSessionNotFound},
		{"missing pane", "can't find pane: hq-dog-alpha", ErrSessionNotFound},
		{"missing window", "can't find window: hq-dog-alpha", ErrSessionNotFound},
		{"no server", "no server running on /tmp/tmux-501/default", ErrNoServer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tm.wrapError(errors.New("exit status 1"), tc.stderr, []string{"capture-pane"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("wrapError(%q) = %v, want errors.Is(..., %v)", tc.stderr, err, tc.want)
			}
			if !IsSessionGone(err) {
				t.Fatalf("IsSessionGone(%v) = false, want true", err)
			}
		})
	}
}

// TestWrapErrorKeepsGenuineFailuresUnclassified is the negative half: a real
// tmux failure must NOT be mistaken for an absent session, or callers would
// silently swallow it as "nothing to see here".
func TestWrapErrorKeepsGenuineFailuresUnclassified(t *testing.T) {
	tm := NewTmux()
	err := tm.wrapError(errors.New("exit status 1"),
		"respawn pane failed: fork failed: Device not configured", []string{"respawn-pane"})
	if IsSessionGone(err) {
		t.Fatalf("IsSessionGone(%v) = true, want false", err)
	}
}

// TestCheckStartupBlockedSessionGone proves the cold-start half of hq-bcowvq:
// when the session does not exist, CheckStartupBlocked must report that the
// session is gone — wrapping ErrSessionNotFound so the caller can say "the agent
// died" — rather than the bare error that callers were labelling
// "startup blocked".
func TestCheckStartupBlockedSessionGone(t *testing.T) {
	tm := newTestTmux(t)

	// Keep a server alive so the failure is specifically "this session is
	// missing", not "no server running".
	keepAlive := "gt-test-keepalive-startupgone"
	_ = tm.KillSession(keepAlive)
	if err := tm.NewSession(keepAlive, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(keepAlive) }()

	err := tm.CheckStartupBlocked("gt-test-absent-session-xyz")
	if err == nil {
		t.Fatal("CheckStartupBlocked on an absent session returned nil")
	}
	if !IsSessionGone(err) {
		t.Fatalf("CheckStartupBlocked = %v, want an error satisfying IsSessionGone", err)
	}
	if !strings.Contains(err.Error(), "vanished during startup") {
		t.Errorf("error %q does not name the vanished session", err)
	}
}

// TestCheckStartupBlockedStillBlocksOnRealDialog is the negative test the fix
// has to survive: when the pane exists and shows a genuine blocking modal,
// CheckStartupBlocked must still block, and must NOT look like a gone session.
func TestCheckStartupBlockedStillBlocksOnRealDialog(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-startup-blocked-real"

	_ = tm.KillSession(sessionName)
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Render a blocking modal with no prompt after it. The trailing sleep keeps
	// the shell busy so the prompt does not return below the banner —
	// promptAppearsAfterStartupBlocker treats a prompt printed after a blocker
	// as proof the modal was already dismissed.
	if err := tm.SendKeys(sessionName, "clear; printf 'Bypass Permissions mode\\n'; sleep 30"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	err := tm.CheckStartupBlocked(sessionName)
	if err == nil {
		t.Fatal("CheckStartupBlocked returned nil with a blocking dialog on screen")
	}
	if IsSessionGone(err) {
		t.Fatalf("a live blocked session was classified as gone: %v", err)
	}
	if !strings.Contains(err.Error(), "interactive startup dialog still visible") {
		t.Errorf("error %q does not name the blocking dialog", err)
	}
}

// TestAcceptStartupDialogsFailsFastWhenSessionGone pins the 16 seconds the
// incident burned on every retry: both dialog acceptors polled a session that
// no longer existed for the full DialogPollTimeout each before anything
// reported a failure.
func TestAcceptStartupDialogsFailsFastWhenSessionGone(t *testing.T) {
	tm := newTestTmux(t)

	keepAlive := "gt-test-keepalive-dialogfast"
	_ = tm.KillSession(keepAlive)
	if err := tm.NewSession(keepAlive, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(keepAlive) }()

	start := time.Now()
	err := tm.AcceptStartupDialogs("gt-test-absent-session-xyz")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("AcceptStartupDialogs on an absent session returned nil")
	}
	if !IsSessionGone(err) {
		t.Fatalf("AcceptStartupDialogs = %v, want an error satisfying IsSessionGone", err)
	}
	if elapsed >= constants.DialogPollTimeout {
		t.Errorf("took %v, expected to fail fast (well under one %v poll timeout)",
			elapsed, constants.DialogPollTimeout)
	}
}
