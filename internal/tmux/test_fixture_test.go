package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testTmuxSentinelSession = "gt-test-sentinel"
	testTmuxPrompt          = "gt-test$ "
	testTmuxReadyTimeout    = 5 * time.Second
	testTmuxReadyInterval   = 10 * time.Millisecond
)

var testTmuxSocketSequence atomic.Uint64

func hasTmux() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// newTestTmux starts a tmux server owned by the calling test. The server uses
// no user config and receives only the environment needed to run test commands,
// so active Gas Town sessions and shell initialization cannot affect it.
func newTestTmux(t *testing.T) *Tmux {
	t.Helper()
	if !hasTmux() {
		t.Skip("tmux not installed")
	}

	socket := fmt.Sprintf("gt-test-%d-%d", os.Getpid(), testTmuxSocketSequence.Add(1))
	cmd := exec.Command(
		"tmux", "-u", "-f", os.DevNull, "-L", socket,
		"new-session", "-d", "-s", testTmuxSentinelSession,
	)
	cmd.Env = isolatedTmuxServerEnvironment()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start isolated tmux server %q: %v: %s", socket, err, strings.TrimSpace(string(output)))
	}

	tm := NewTmuxWithSocket(socket)
	t.Cleanup(func() {
		if err := tm.KillServer(); err != nil {
			t.Errorf("kill isolated tmux server %q: %v", socket, err)
		}
	})
	return tm
}

func isolatedTmuxServerEnvironment() []string {
	// Keep command discovery and ordinary process identity portable while
	// excluding tmux, agent, and shell-hook state from the invoking session.
	keys := []string{
		"HOME", "LANG", "LC_ALL", "LOGNAME", "PATH", "TEMP", "TERM",
		"TMP", "TMPDIR", "USER",
	}
	env := make([]string, 0, len(keys)+2)
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return append(env, "SHELL=/bin/sh", "PS1="+testTmuxPrompt)
}

func newReadyTestSession(t *testing.T, tm *Tmux, session string) {
	t.Helper()
	const shell = `env PS1='gt-test$ ' ENV= BASH_ENV= /bin/sh -i`
	if err := tm.NewSessionWithCommand(session, "", shell); err != nil {
		t.Fatalf("start controlled test shell: %v", err)
	}
	waitForPaneText(t, tm, session, strings.TrimSpace(testTmuxPrompt))
}

func waitForPaneText(t *testing.T, tm *Tmux, target, text string) {
	t.Helper()
	deadline := time.Now().Add(testTmuxReadyTimeout)
	var (
		lastOutput string
		lastErr    error
	)
	for time.Now().Before(deadline) {
		lastOutput, lastErr = tm.CapturePane(target, 30)
		if lastErr == nil && strings.Contains(lastOutput, text) {
			return
		}
		time.Sleep(testTmuxReadyInterval)
	}
	t.Fatalf("pane %q did not contain %q within %s: output=%q err=%v", target, text, testTmuxReadyTimeout, lastOutput, lastErr)
}

func TestNewTestTmuxIsolatesStateAndEnvironment(t *testing.T) {
	t.Setenv("BASH_ENV", "/operator/bash-env")
	t.Setenv("TMUX", "/operator/socket,123,0")
	t.Setenv("TMUX_PANE", "%operator")
	t.Setenv("GT_TOWN_SOCKET", "operator")

	first := newTestTmux(t)
	second := newTestTmux(t)
	if first.socketName == second.socketName {
		t.Fatalf("test tmux sockets must be unique, both were %q", first.socketName)
	}

	if _, err := first.run("bind-key", "-T", "prefix", "F12", "display-message", "first-server"); err != nil {
		t.Fatalf("bind key on first server: %v", err)
	}
	if output, err := second.keyBindingOutput("prefix", "F12"); err != nil {
		t.Fatalf("read binding from second server: %v", err)
	} else if output != "" {
		t.Fatalf("binding leaked between test servers: %q", output)
	}

	env, err := first.GetAllEnvironment(testTmuxSentinelSession)
	if err != nil {
		t.Fatalf("read isolated server environment: %v", err)
	}
	for _, key := range []string{"BASH_ENV", "TMUX", "TMUX_PANE", "GT_TOWN_SOCKET"} {
		if value, ok := env[key]; ok {
			t.Errorf("isolated server inherited %s=%q", key, value)
		}
	}
}
