package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	return newTestTmuxWithShell(t, "/bin/sh")
}

func newTestTmuxWithShell(t *testing.T, shell string) *Tmux {
	t.Helper()
	if !hasTmux() {
		t.Skip("tmux not installed")
	}

	socket := fmt.Sprintf("gt-test-%d-%d", os.Getpid(), testTmuxSocketSequence.Add(1))
	cmd := exec.Command(
		"tmux", "-u", "-f", os.DevNull, "-L", socket,
		"new-session", "-d", "-s", testTmuxSentinelSession,
	)
	cmd.Env = isolatedTmuxServerEnvironment(shell)
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

func isolatedTmuxServerEnvironment(shell string) []string {
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
	return append(env, "SHELL="+shell, "PS1="+testTmuxPrompt)
}

func newReadyTestSession(t *testing.T, tm *Tmux, session string) {
	t.Helper()
	const shell = `/bin/sh -i`
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

func waitForPaneCommand(t *testing.T, tm *Tmux, target, command string) {
	t.Helper()
	deadline := time.Now().Add(testTmuxReadyTimeout)
	var (
		lastCommand string
		lastErr     error
	)
	for time.Now().Before(deadline) {
		lastCommand, lastErr = tm.GetPaneCommand(target)
		if lastErr == nil && lastCommand == command {
			return
		}
		time.Sleep(testTmuxReadyInterval)
	}
	t.Fatalf("pane %q command did not become %q within %s: command=%q err=%v", target, command, testTmuxReadyTimeout, lastCommand, lastErr)
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

func TestNewSessionWithCommandBypassesHoldingShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("controlled POSIX shell is not supported by psmux")
	}

	shellPath := filepath.Join(t.TempDir(), "holding-shell")
	shell := `#!/bin/sh
if [ "$1" = "-c" ]; then
	/bin/sh -c "$2" &
	child=$!
	trap 'kill "$child" 2>/dev/null' EXIT HUP INT TERM
	wait "$child"
	exit $?
fi
while :; do sleep 1; done
`
	if err := os.WriteFile(shellPath, []byte(shell), 0o700); err != nil {
		t.Fatalf("write controlled shell: %v", err)
	}

	tm := newTestTmuxWithShell(t, shellPath)
	session := "gt-test-direct-command"
	if err := tm.NewSessionWithCommand(session, "", "sleep 30"); err != nil {
		t.Fatalf("NewSessionWithCommand: %v", err)
	}
	waitForPaneCommand(t, tm, session, "sleep")
}
