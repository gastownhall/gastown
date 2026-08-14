package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// requireTestSocket returns a per-test socket name and skips the test if
// tmux is not installed. Each test gets its own socket to prevent interference.
// The socket server is cleaned up when the test finishes.
func requireTestSocket(t *testing.T) string {
	t.Helper()
	if !hasTmux() {
		t.Skip("tmux not installed")
	}
	socket := fmt.Sprintf("gt-test-hook-%d", os.Getpid())
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	})
	return socket
}

// testSession creates a session on the given socket running a simple command.
func testSession(t *testing.T, socket, session, command string) {
	t.Helper()
	args := []string{"-L", socket, "new-session", "-d", "-s", session, command}
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("failed to create test session %q on socket %q: %v\n%s", session, socket, err, out)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if exec.Command("tmux", "-L", socket, "has-session", "-t", session).Run() == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %q never appeared on socket %q", session, socket)
}

func isPaneDead(socket, session string) bool {
	out, err := exec.Command("tmux", "-L", socket, "list-panes", "-t", session, "-F", "#{pane_dead}").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "1"
}

func getPanePID(t *testing.T, socket, session string) string {
	t.Helper()
	out, err := exec.Command("tmux", "-L", socket, "display-message", "-t", session, "-p", "#{pane_pid}").Output()
	if err != nil {
		t.Fatalf("failed to get pane PID for %q: %v", session, err)
	}
	return strings.TrimSpace(string(out))
}

// TestAutoRespawnHookCmd_Format is a fast unit test verifying the hook command
// string contains all required safety measures.
func TestAutoRespawnHookCmd_Format(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		tmuxCmd  string
		session  string
		wantFlag string
	}{
		{"background_flag", "tmux -L gt", "hq-deacon", "run-shell -b"},
		{"dead_pane_guard", "tmux -L gt", "hq-deacon", "pane_dead"},
		{"error_suppression", "tmux -L gt", "hq-deacon", "|| true"},
		{"socket_in_respawn", "tmux -L gt", "hq-deacon", "-L gt"},
		{"bare_tmux_no_socket", "tmux", "hq-deacon", "tmux respawn-pane"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := buildAutoRespawnHookCmd(tt.tmuxCmd, tt.session)
			if !strings.Contains(cmd, tt.wantFlag) {
				t.Errorf("hook command missing %q:\n  %s", tt.wantFlag, cmd)
			}
		})
	}
}

// TestAutoRespawnHook_RespawnWorks is the primary regression test: pane dies,
// hook fires on the correct socket, pane comes back alive.
func TestAutoRespawnHook_RespawnWorks(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux respawn waits on pane death")
	}
	socket := requireTestSocket(t)
	session := "test-respawn"

	t0 := time.Now()
	logT := func(msg string, args ...any) {
		t.Logf("[+%6.2fs] %s", time.Since(t0).Seconds(), fmt.Sprintf(msg, args...))
	}

	testSession(t, socket, session, "sleep 2")
	defer func() { _ = exec.Command("tmux", "-L", socket, "kill-session", "-t", session).Run() }()
	logT("session created with 'sleep 2'")

	// Log the initial pane state
	logT("initial pane_dead=%v, pane_pid=%s", isPaneDead(socket, session), getPanePIDSafe(socket, session))

	tmx := NewTmuxWithSocket(socket)
	if err := tmx.SetAutoRespawnHook(session); err != nil {
		t.Fatalf("SetAutoRespawnHook: %v", err)
	}
	logT("hook installed")

	// Log the hook configuration (try both session and global hooks)
	if hookOut, err := exec.Command("tmux", "-L", socket, "show-hooks", "-t", session).CombinedOutput(); err == nil {
		logT("session hooks: %s", strings.TrimSpace(string(hookOut)))
	}
	if hookOut, err := exec.Command("tmux", "-L", socket, "show-hooks", "-g").CombinedOutput(); err == nil {
		logT("global hooks: %s", strings.TrimSpace(string(hookOut)))
	}
	// Log the actual remain-on-exit setting
	if optOut, err := exec.Command("tmux", "-L", socket, "show-options", "-t", session, "remain-on-exit").CombinedOutput(); err == nil {
		logT("remain-on-exit: %s", strings.TrimSpace(string(optOut)))
	}
	// Log tmux version (hook behavior varies)
	if verOut, err := exec.Command("tmux", "-V").CombinedOutput(); err == nil {
		logT("tmux version: %s", strings.TrimSpace(string(verOut)))
	}

	// Wait for sleep 2 to exit
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if isPaneDead(socket, session) {
			logT("pane died (sleep 2 exited)")
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !isPaneDead(socket, session) {
		logT("WARNING: pane never died within 5s deadline")
	}

	// Wait for hook to respawn (3s sleep + startup)
	alive := false
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !isPaneDead(socket, session) {
			logT("pane respawned (alive again)")
			alive = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !alive {
		// Dump diagnostics on failure
		logT("FAILURE: pane was NOT respawned within 8s of death")
		logT("pane_dead=%v, pane_pid=%s", isPaneDead(socket, session), getPanePIDSafe(socket, session))
		if paneInfo, err := exec.Command("tmux", "-L", socket, "list-panes", "-t", session,
			"-F", "dead=#{pane_dead} pid=#{pane_pid} cmd=#{pane_current_command} start=#{pane_start_command}").CombinedOutput(); err == nil {
			logT("pane info: %s", strings.TrimSpace(string(paneInfo)))
		} else {
			logT("list-panes failed: %v", err)
		}
		if hookOut, err := exec.Command("tmux", "-L", socket, "show-hooks", "-t", session).CombinedOutput(); err == nil {
			logT("hooks at failure: %s", strings.TrimSpace(string(hookOut)))
		}
		// Check if remain-on-exit is set (needed for hook to fire)
		if optOut, err := exec.Command("tmux", "-L", socket, "show-options", "-t", session, "remain-on-exit").CombinedOutput(); err == nil {
			logT("remain-on-exit: %s", strings.TrimSpace(string(optOut)))
		}
		t.Error("pane was NOT respawned — hook failed (likely missing -L socket flag)")
	}
}

// getPanePIDSafe returns the pane PID or "?" if it can't be read (no t.Fatal).
func getPanePIDSafe(socket, session string) string {
	out, err := exec.Command("tmux", "-L", socket, "display-message", "-t", session, "-p", "#{pane_pid}").Output()
	if err != nil {
		return "?(err)"
	}
	return strings.TrimSpace(string(out))
}

// TestAutoRespawnHook_SkipsAlreadyAlive verifies the dead-pane guard: if the
// daemon restarts the pane during the hook's 3s sleep, the hook must NOT kill
// the fresh process.
func TestAutoRespawnHook_SkipsAlreadyAlive(t *testing.T) {
	socket := requireTestSocket(t)
	session := "test-skip-alive"

	testSession(t, socket, session, "sleep 300")
	defer func() { _ = exec.Command("tmux", "-L", socket, "kill-session", "-t", session).Run() }()

	tmx := NewTmuxWithSocket(socket)
	if err := tmx.SetAutoRespawnHook(session); err != nil {
		t.Fatalf("SetAutoRespawnHook: %v", err)
	}

	// Kill process → pane dies → hook starts 3s sleep
	exec.Command("tmux", "-L", socket, "respawn-pane", "-k", "-t", session, "true").Run()
	time.Sleep(500 * time.Millisecond)

	// Simulate daemon: immediately respawn before hook wakes
	exec.Command("tmux", "-L", socket, "respawn-pane", "-k", "-t", session, "sleep 300").Run()
	time.Sleep(300 * time.Millisecond)

	pid1 := getPanePID(t, socket, session)

	// Wait for hook to fire
	time.Sleep(5 * time.Second)

	pid2 := getPanePID(t, socket, session)
	if pid1 != pid2 {
		t.Errorf("hook killed daemon-respawned process: PID %s → %s (race condition)", pid1, pid2)
	}
}
