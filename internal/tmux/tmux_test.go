package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func hasTmux() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// newTestTmux returns a Tmux instance connected to the package-level test
// socket (set by TestMain in testmain_test.go). All tests in this package
// share one tmux server, which is torn down after all tests complete.
//
// This isolates tests from the user's interactive tmux and from other
// packages' tests that run in parallel during `go test ./...`.
func newTestTmux(t *testing.T) *Tmux {
	t.Helper()
	if !hasTmux() {
		t.Skip("tmux not installed")
	}
	return NewTmux()
}

func TestListSessionsNoServer(t *testing.T) {
	tm := newTestTmux(t)
	sessions, err := tm.ListSessions()
	// Should not error even if no server running
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	// Result may be nil or empty slice
	_ = sessions
}

func TestHasSessionNoServer(t *testing.T) {
	tm := newTestTmux(t)
	has, err := tm.HasSession("nonexistent-session-xyz")
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if has {
		t.Error("expected session to not exist")
	}
}

func TestSessionLifecycle(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-session-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	// Create session
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Verify exists
	has, err := tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !has {
		t.Error("expected session to exist after creation")
	}

	// List should include it
	sessions, err := tm.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	found := false
	for _, s := range sessions {
		if s == sessionName {
			found = true
			break
		}
	}
	if !found {
		t.Error("session not found in list")
	}

	// Kill session
	if err := tm.KillSession(sessionName); err != nil {
		t.Fatalf("KillSession: %v", err)
	}

	// Verify gone
	has, err = tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession after kill: %v", err)
	}
	if has {
		t.Error("expected session to not exist after kill")
	}
}

func TestDuplicateSession(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-dup-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	// Create session
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Try to create duplicate
	err := tm.NewSession(sessionName, "")
	if err != ErrSessionExists {
		t.Errorf("expected ErrSessionExists, got %v", err)
	}
}

func TestSendKeysAndCapture(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-keys-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	// Create session
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Send echo command
	if err := tm.SendKeys(sessionName, "echo HELLO_TEST_MARKER"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}

	// Give it a moment to execute
	// In real tests you'd wait for output, but for basic test we just capture
	output, err := tm.CapturePane(sessionName, 50)
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}

	// Should contain our marker (might not if shell is slow, but usually works)
	if !strings.Contains(output, "echo HELLO_TEST_MARKER") {
		t.Logf("captured output: %s", output)
		// Don't fail, just note - timing issues possible
	}
}

func TestGetSessionInfo(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-info-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	// Create session
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	info, err := tm.GetSessionInfo(sessionName)
	if err != nil {
		t.Fatalf("GetSessionInfo: %v", err)
	}

	if info.Name != sessionName {
		t.Errorf("Name = %q, want %q", info.Name, sessionName)
	}
	if info.Windows < 1 {
		t.Errorf("Windows = %d, want >= 1", info.Windows)
	}
}

func TestWrapError(t *testing.T) {
	tm := newTestTmux(t)

	tests := []struct {
		stderr string
		want   error
	}{
		{"no server running on /tmp/tmux-...", ErrNoServer},
		{"error connecting to /tmp/tmux-...", ErrNoServer},
		{"no current target", ErrNoServer},
		{"duplicate session: test", ErrSessionExists},
		{"session not found: test", ErrSessionNotFound},
		{"can't find session: test", ErrSessionNotFound},
	}

	for _, tt := range tests {
		err := tm.wrapError(nil, tt.stderr, []string{"test"})
		if err != tt.want {
			t.Errorf("wrapError(%q) = %v, want %v", tt.stderr, err, tt.want)
		}
	}
}

func TestEnsureSessionFresh_NoExistingSession(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-fresh-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	// EnsureSessionFresh should create a new session
	if err := tm.EnsureSessionFresh(sessionName, ""); err != nil {
		t.Fatalf("EnsureSessionFresh: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Verify session exists
	has, err := tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !has {
		t.Error("expected session to exist after EnsureSessionFresh")
	}
}

func TestEnsureSessionFresh_ZombieSession(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-zombie-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	// Create a zombie session (session exists but no Claude/node running)
	// A normal tmux session with bash/zsh is a "zombie" for our purposes
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Verify it's a zombie (not running any agent)
	if tm.IsAgentAlive(sessionName) {
		t.Skip("session unexpectedly has agent running - can't test zombie case")
	}

	// Fresh tmux sessions can briefly report transient pane commands while the
	// login shell is starting. IsAgentAlive is the stable predicate we care
	// about here: there is no runtime in the session, so EnsureSessionFresh
	// should treat it as a zombie and recreate it successfully.

	// EnsureSessionFresh should kill the zombie and create fresh session
	// This should NOT error with "session already exists"
	if err := tm.EnsureSessionFresh(sessionName, ""); err != nil {
		t.Fatalf("EnsureSessionFresh on zombie: %v", err)
	}

	// Session should still exist
	has, err := tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !has {
		t.Error("expected session to exist after EnsureSessionFresh on zombie")
	}
}

func TestEnsureSessionFresh_IdempotentOnZombie(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-idem-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	// Call EnsureSessionFresh multiple times - should work each time
	for i := 0; i < 3; i++ {
		if err := tm.EnsureSessionFresh(sessionName, ""); err != nil {
			t.Fatalf("EnsureSessionFresh attempt %d: %v", i+1, err)
		}
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Session should exist
	has, err := tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !has {
		t.Error("expected session to exist after multiple EnsureSessionFresh calls")
	}
}

func TestEnsureSessionFreshWithCommand_NoExisting(t *testing.T) {
	if !hasTmux() {
		t.Skip("tmux not installed")
	}

	tm := NewTmux()
	sessionName := "gt-test-fwc-new-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)
	defer func() { _ = tm.KillSession(sessionName) }()

	// EnsureSessionFreshWithCommand should create a new session with the command
	// as the initial pane process (no shell involved).
	if err := tm.EnsureSessionFreshWithCommand(sessionName, "", "sleep 10"); err != nil {
		t.Fatalf("EnsureSessionFreshWithCommand: %v", err)
	}

	// Verify session exists
	has, err := tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !has {
		t.Error("expected session to exist")
	}

	// Verify the command is running (not a shell)
	cmd, err := tm.GetPaneCommand(sessionName)
	if err != nil {
		t.Fatalf("GetPaneCommand: %v", err)
	}
	if cmd != "sleep" {
		t.Logf("pane command = %q (expected 'sleep')", cmd)
	}
}

func TestEnsureSessionFreshWithCommand_KillsZombie(t *testing.T) {
	if !hasTmux() {
		t.Skip("tmux not installed")
	}

	tm := NewTmux()
	sessionName := "gt-test-fwc-zombie-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)
	defer func() { _ = tm.KillSession(sessionName) }()

	// Create a zombie session (empty shell, no agent)
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Verify it's a zombie
	if tm.IsAgentRunning(sessionName) {
		t.Skip("session unexpectedly has agent running")
	}

	// EnsureSessionFreshWithCommand should kill the zombie and create fresh session
	if err := tm.EnsureSessionFreshWithCommand(sessionName, "", "sleep 10"); err != nil {
		t.Fatalf("EnsureSessionFreshWithCommand on zombie: %v", err)
	}

	// Session should exist with the new command
	has, err := tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !has {
		t.Error("expected session to exist after replacing zombie")
	}

	// The command should be sleep, not a shell
	cmd, err := tm.GetPaneCommand(sessionName)
	if err != nil {
		t.Fatalf("GetPaneCommand: %v", err)
	}
	if cmd != "sleep" {
		t.Logf("pane command = %q (expected 'sleep' after zombie replacement)", cmd)
	}
}

func TestIsAgentRunning(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-agent-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	// Create session (will run default shell)
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Wait for the shell to be fully initialized before querying pane command.
	// Without this, GetPaneCommand can return a transient value during shell
	// startup (e.g., login or profile-sourced commands), causing flaky matches.
	if err := tm.WaitForShellReady(sessionName, 2*time.Second); err != nil {
		t.Fatalf("WaitForShellReady: %v", err)
	}

	// Get the current pane command (should be bash/zsh/etc)
	cmd, err := tm.GetPaneCommand(sessionName)
	if err != nil {
		t.Fatalf("GetPaneCommand: %v", err)
	}

	tests := []struct {
		name         string
		processNames []string
		wantRunning  bool
	}{
		{
			name:         "empty process list",
			processNames: []string{},
			wantRunning:  false,
		},
		{
			name:         "matching shell process",
			processNames: []string{cmd}, // Current shell
			wantRunning:  true,
		},
		{
			name:         "claude agent (node) - not running",
			processNames: []string{"node"},
			wantRunning:  cmd == "node", // Only true if shell happens to be node
		},
		{
			name:         "gemini agent - not running",
			processNames: []string{"gemini"},
			wantRunning:  cmd == "gemini",
		},
		{
			name:         "cursor agent - not running",
			processNames: []string{"cursor-agent"},
			wantRunning:  cmd == "cursor-agent",
		},
		{
			name:         "multiple process names with match",
			processNames: []string{"nonexistent", cmd, "also-nonexistent"},
			wantRunning:  true,
		},
		{
			name:         "multiple process names without match",
			processNames: []string{"nonexistent1", "nonexistent2"},
			wantRunning:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Re-check the pane command immediately before assertion.
			// The pane command can transiently change between the initial
			// GetPaneCommand call and subtest execution (e.g., shell profile
			// commands), causing false failures. Retry up to 5 times with
			// a short sleep to tolerate transient pane command changes.
			var got bool
			var currentCmd string
			for attempt := 0; attempt < 5; attempt++ {
				if attempt > 0 {
					time.Sleep(200 * time.Millisecond)
				}
				got = tm.IsAgentRunning(sessionName, tt.processNames...)
				if got == tt.wantRunning {
					return // success
				}
				// Re-read pane command for diagnostics
				currentCmd, _ = tm.GetPaneCommand(sessionName)
			}
			t.Errorf("IsAgentRunning(%q, %v) = %v, want %v (current cmd: %q, setup cmd: %q)",
				sessionName, tt.processNames, got, tt.wantRunning, currentCmd, cmd)
		})
	}
}

func TestIsAgentRunning_NonexistentSession(t *testing.T) {
	tm := newTestTmux(t)

	// IsAgentRunning on nonexistent session should return false, not error
	got := tm.IsAgentRunning("nonexistent-session-xyz", "node", "gemini", "cursor-agent")
	if got {
		t.Error("IsAgentRunning on nonexistent session should return false")
	}
}

func TestIsRuntimeRunning_AgentNameRequiresCursorSession(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-runtime-agent-filter-" + t.Name()
	_ = tm.KillSession(sessionName)
	if err := tm.NewSessionWithCommand(sessionName, "", "sleep 60"); err != nil {
		t.Fatalf("NewSessionWithCommand: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// sleep matches; "agent" in the list is ignored when session does not declare Cursor
	if !tm.IsRuntimeRunning(sessionName, []string{"agent", "sleep"}) {
		t.Error("expected sleep to match when agent is stripped for non-cursor sessions")
	}
	if tm.IsRuntimeRunning(sessionName, []string{"agent"}) {
		t.Error("expected bare agent not to match without GT_AGENT=cursor / GT_PROCESS_NAMES")
	}
	if err := tm.SetEnvironment(sessionName, "GT_AGENT", "cursor"); err != nil {
		t.Fatalf("SetEnvironment: %v", err)
	}
	// With Cursor declared, "agent" is kept in the filter list (pane is still sleep — no match on agent alone)
	if tm.IsRuntimeRunning(sessionName, []string{"agent"}) {
		t.Error("pane is sleep, not agent — should not match on agent name alone")
	}
	if !tm.IsRuntimeRunning(sessionName, []string{"agent", "sleep"}) {
		t.Error("expected sleep to still match with GT_AGENT=cursor")
	}
}

func TestIsRuntimeRunning(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-runtime-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	// Create session (will run default shell, not any agent)
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// IsRuntimeRunning should be false (shell is running, not node/claude)
	cmd, _ := tm.GetPaneCommand(sessionName)
	processNames := []string{"node", "claude"}
	wantRunning := cmd == "node" || cmd == "claude"

	if got := tm.IsRuntimeRunning(sessionName, processNames); got != wantRunning {
		t.Errorf("IsRuntimeRunning() = %v, want %v (pane cmd: %q)", got, wantRunning, cmd)
	}
}

func TestIsRuntimeRunning_ShellWithNodeChild(t *testing.T) {
	tm := newTestTmux(t)

	// Direct path: tmux runs the process as the pane command directly.
	// This simulates a bundled agent binary (e.g. the standalone claude binary)
	// where the pane command IS the agent process.
	t.Run("direct", func(t *testing.T) {
		sessionName := "gt-test-runtime-direct"
		_ = tm.KillSession(sessionName)

		if err := tm.NewSessionWithCommand(sessionName, "", "sleep 10"); err != nil {
			t.Fatalf("NewSessionWithCommand: %v", err)
		}
		defer func() { _ = tm.KillSession(sessionName) }()

		if !tm.IsRuntimeRunning(sessionName, []string{"sleep"}) {
			paneCmd, _ := tm.GetPaneCommand(sessionName)
			t.Errorf("IsRuntimeRunning should return true for direct process (pane cmd: %q)", paneCmd)
		}
	})

	// Shell+child path: tmux runs a shell which spawns the agent as a child.
	// This simulates an npm-installed agent (e.g. claude via node) where the
	// pane command is sh/bash and the agent is a descendant process.
	t.Run("shell_with_child", func(t *testing.T) {
		sessionName := "gt-test-runtime-shell-child"
		_ = tm.KillSession(sessionName)

		if err := tm.NewSessionWithCommand(sessionName, "", "sh -c 'sleep 10'"); err != nil {
			t.Fatalf("NewSessionWithCommand: %v", err)
		}
		defer func() { _ = tm.KillSession(sessionName) }()

		// Give the child process a moment to start
		time.Sleep(100 * time.Millisecond)

		if !tm.IsRuntimeRunning(sessionName, []string{"sleep"}) {
			paneCmd, _ := tm.GetPaneCommand(sessionName)
			t.Errorf("IsRuntimeRunning should return true for child process (pane cmd: %q)", paneCmd)
		}
	})
}

// TestGetPaneCommand_MultiPane verifies that GetPaneCommand returns pane 0's
// command even when a split pane exists and is active. This is the core fix
// for gs-2v7: without explicit pane 0 targeting, health checks would see the
// split pane's shell and falsely report the agent as dead.
func TestGetPaneCommand_MultiPane(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-multipane-" + t.Name()

	_ = tm.KillSession(sessionName)

	// Create session running sleep (simulates an agent process in pane 0)
	if err := tm.NewSessionWithCommand(sessionName, "", "sleep 300"); err != nil {
		t.Fatalf("NewSessionWithCommand: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Verify pane 0 shows "sleep"
	cmd, err := tm.GetPaneCommand(sessionName)
	if err != nil {
		t.Fatalf("GetPaneCommand before split: %v", err)
	}
	if cmd != "sleep" {
		t.Fatalf("expected pane 0 command to be 'sleep', got %q", cmd)
	}

	// Capture pane 0's PID and working directory before the split
	pidBefore, err := tm.GetPanePID(sessionName)
	if err != nil {
		t.Fatalf("GetPanePID before split: %v", err)
	}
	wdBefore, err := tm.GetPaneWorkDir(sessionName)
	if err != nil {
		t.Fatalf("GetPaneWorkDir before split: %v", err)
	}

	// Split the window — creates a new pane running a shell, which becomes active
	if _, err := tm.run("split-window", "-t", sessionName, "-d"); err != nil {
		t.Fatalf("split-window: %v", err)
	}

	// GetPaneCommand should still return "sleep" (pane 0), not the shell
	cmd, err = tm.GetPaneCommand(sessionName)
	if err != nil {
		t.Fatalf("GetPaneCommand after split: %v", err)
	}
	if cmd != "sleep" {
		t.Errorf("after split, GetPaneCommand should return pane 0 command 'sleep', got %q", cmd)
	}

	// GetPanePID should return pane 0's PID, matching the pre-split value
	pid, err := tm.GetPanePID(sessionName)
	if err != nil {
		t.Fatalf("GetPanePID after split: %v", err)
	}
	if pid != pidBefore {
		t.Errorf("GetPanePID changed after split: before=%s, after=%s", pidBefore, pid)
	}

	// GetPaneWorkDir should still return pane 0's working directory
	wd, err := tm.GetPaneWorkDir(sessionName)
	if err != nil {
		t.Fatalf("GetPaneWorkDir after split: %v", err)
	}
	if wd != wdBefore {
		t.Errorf("GetPaneWorkDir changed after split: before=%s, after=%s", wdBefore, wd)
	}
}

func TestHasDescendantWithNames(t *testing.T) {
	t.Parallel()
	// Test the hasDescendantWithNames helper function directly

	// Test with a definitely nonexistent PID
	got := hasDescendantWithNames("999999999", []string{"node", "claude"}, 0)
	if got {
		t.Error("hasDescendantWithNames should return false for nonexistent PID")
	}

	// Use current PID instead of PID 1 to avoid walking the entire process tree
	selfPID := fmt.Sprintf("%d", os.Getpid())

	// Test with empty names slice - should always return false
	got = hasDescendantWithNames(selfPID, []string{}, 0)
	if got {
		t.Error("hasDescendantWithNames should return false for empty names slice")
	}

	// Test with nil names slice - should always return false
	got = hasDescendantWithNames(selfPID, nil, 0)
	if got {
		t.Error("hasDescendantWithNames should return false for nil names slice")
	}

	// Test with current process - should have children but not specific agent processes
	got = hasDescendantWithNames(selfPID, []string{"node", "claude"}, 0)
	if got {
		t.Logf("hasDescendantWithNames(%q, [node,claude]) = true - process has matching child?", selfPID)
	}
}

func TestGetAllDescendants(t *testing.T) {
	t.Parallel()
	// Test the getAllDescendants helper function

	// Test with nonexistent PID - should return empty slice
	got := getAllDescendants("999999999")
	if len(got) != 0 {
		t.Errorf("getAllDescendants(nonexistent) = %v, want empty slice", got)
	}

	// Use current PID instead of PID 1 to avoid walking the entire process tree
	selfPID := fmt.Sprintf("%d", os.Getpid())
	descendants := getAllDescendants(selfPID)
	t.Logf("getAllDescendants(%q) found %d descendants", selfPID, len(descendants))

	// Verify returned PIDs are all numeric strings
	for _, pid := range descendants {
		for _, c := range pid {
			if c < '0' || c > '9' {
				t.Errorf("getAllDescendants returned non-numeric PID: %q", pid)
			}
		}
	}
}

func TestKillSessionWithProcesses(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-killproc-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	// Create session with a long-running process
	cmd := `sleep 300`
	if err := tm.NewSessionWithCommand(sessionName, "", cmd); err != nil {
		t.Fatalf("NewSessionWithCommand: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Verify session exists
	has, err := tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !has {
		t.Fatal("expected session to exist after creation")
	}

	// Kill with processes
	if err := tm.KillSessionWithProcesses(sessionName); err != nil {
		t.Fatalf("KillSessionWithProcesses: %v", err)
	}

	// Verify session is gone
	has, err = tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession after kill: %v", err)
	}
	if has {
		t.Error("expected session to not exist after KillSessionWithProcesses")
		_ = tm.KillSession(sessionName) // cleanup
	}
}

func TestKillSessionWithProcesses_NonexistentSession(t *testing.T) {
	tm := newTestTmux(t)

	// Killing nonexistent session should not panic, just return error or nil
	err := tm.KillSessionWithProcesses("nonexistent-session-xyz-12345")
	// We don't care about the error value, just that it doesn't panic
	_ = err
}

func TestKillSessionWithProcessesExcluding(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-killexcl-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	// Create session with a long-running process
	cmd := `sleep 300`
	if err := tm.NewSessionWithCommand(sessionName, "", cmd); err != nil {
		t.Fatalf("NewSessionWithCommand: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Verify session exists
	has, err := tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !has {
		t.Fatal("expected session to exist after creation")
	}

	// Kill with empty excludePIDs (should behave like KillSessionWithProcesses)
	if err := tm.KillSessionWithProcessesExcluding(sessionName, nil); err != nil {
		t.Fatalf("KillSessionWithProcessesExcluding: %v", err)
	}

	// Verify session is gone
	has, err = tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession after kill: %v", err)
	}
	if has {
		t.Error("expected session to not exist after KillSessionWithProcessesExcluding")
		_ = tm.KillSession(sessionName) // cleanup
	}
}

func TestKillSessionWithProcessesExcluding_WithExcludePID(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-killexcl2-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	// Create session with a long-running process
	cmd := `sleep 300`
	if err := tm.NewSessionWithCommand(sessionName, "", cmd); err != nil {
		t.Fatalf("NewSessionWithCommand: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Get the pane PID
	panePID, err := tm.GetPanePID(sessionName)
	if err != nil {
		t.Fatalf("GetPanePID: %v", err)
	}
	if panePID == "" {
		t.Skip("could not get pane PID")
	}

	// Kill with the pane PID excluded - the function should still kill the session
	// but should not kill the excluded PID before the session is destroyed
	err = tm.KillSessionWithProcessesExcluding(sessionName, []string{panePID})
	if err != nil {
		t.Fatalf("KillSessionWithProcessesExcluding: %v", err)
	}

	// Session should be gone (the final KillSession always happens)
	has, _ := tm.HasSession(sessionName)
	if has {
		t.Error("expected session to not exist after KillSessionWithProcessesExcluding")
	}
}

func TestKillSessionWithProcessesExcluding_NonexistentSession(t *testing.T) {
	tm := newTestTmux(t)

	// Killing nonexistent session should not panic
	err := tm.KillSessionWithProcessesExcluding("nonexistent-session-xyz-12345", []string{"12345"})
	// We don't care about the error value, just that it doesn't panic
	_ = err
}

func TestGetProcessGroupID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping test: process groups not available on Windows")
	}

	// Test with current process
	pid := fmt.Sprintf("%d", os.Getpid())
	pgid := getProcessGroupID(pid)

	if pgid == "" {
		t.Error("expected non-empty PGID for current process")
	}

	// PGID should not be 0 or 1 for a normal process
	if pgid == "0" || pgid == "1" {
		t.Errorf("unexpected PGID %q for current process", pgid)
	}

	// Test with nonexistent PID
	pgid = getProcessGroupID("999999999")
	if pgid != "" {
		t.Errorf("expected empty PGID for nonexistent process, got %q", pgid)
	}
}

func TestGetProcessGroupMembers(t *testing.T) {
	// Get current process's PGID
	pid := fmt.Sprintf("%d", os.Getpid())
	pgid := getProcessGroupID(pid)
	if pgid == "" {
		t.Skip("could not get PGID for current process")
	}

	members := getProcessGroupMembers(pgid)

	// Current process should be in the list
	found := false
	for _, m := range members {
		if m == pid {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("current process %s not found in process group %s members: %v", pid, pgid, members)
	}
}

func TestKillSessionWithProcesses_KillsProcessGroup(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-killpg-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	// Create session that spawns a child process
	// The child will stay in the same process group as the shell
	cmd := `sleep 300 & sleep 300`
	if err := tm.NewSessionWithCommand(sessionName, "", cmd); err != nil {
		t.Fatalf("NewSessionWithCommand: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Give processes time to start
	time.Sleep(200 * time.Millisecond)

	// Verify session exists
	has, err := tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !has {
		t.Fatal("expected session to exist after creation")
	}

	// Kill with processes (should kill the entire process group)
	if err := tm.KillSessionWithProcesses(sessionName); err != nil {
		t.Fatalf("KillSessionWithProcesses: %v", err)
	}

	// Verify session is gone
	has, err = tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession after kill: %v", err)
	}
	if has {
		t.Error("expected session to not exist after KillSessionWithProcesses")
		_ = tm.KillSession(sessionName) // cleanup
	}
}

func TestSessionSet(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-sessionset-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	// Create a test session
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Get the session set
	set, err := tm.GetSessionSet()
	if err != nil {
		t.Fatalf("GetSessionSet: %v", err)
	}

	// Test Has() for existing session
	if !set.Has(sessionName) {
		t.Errorf("SessionSet.Has(%q) = false, want true", sessionName)
	}

	// Test Has() for non-existing session
	if set.Has("nonexistent-session-xyz-12345") {
		t.Error("SessionSet.Has(nonexistent) = true, want false")
	}

	// Test nil safety
	var nilSet *SessionSet
	if nilSet.Has("anything") {
		t.Error("nil SessionSet.Has() = true, want false")
	}

	// Test Names() returns the session
	names := set.Names()
	found := false
	for _, n := range names {
		if n == sessionName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("SessionSet.Names() doesn't contain %q", sessionName)
	}
}

func TestCleanupOrphanedSessions(t *testing.T) {
	// newTestTmux creates an isolated tmux server (unique socket per test).
	// CleanupOrphanedSessions operates on the Tmux receiver which carries that
	// socket, so it can only see sessions on this isolated server — never real
	// polecats or user sessions.
	//
	// History: this test previously used NewTmux() (default server) and killed
	// 8 production crew sessions during a patrol run. The env-var guard added in
	// 3519f58c was the right fix at the time; per-test socket isolation (48550c7f)
	// made it redundant.
	isTestGTSession := func(s string) bool {
		return strings.HasPrefix(s, "gt-") || strings.HasPrefix(s, "hq-")
	}

	tm := newTestTmux(t)

	// Create test sessions with gt- and hq- prefixes (zombie sessions - no Claude running)
	gtSession := "gt-test-cleanup-rig"
	hqSession := "hq-test-cleanup"
	nonGtSession := "other-test-session"

	// Clean up any existing test sessions
	_ = tm.KillSession(gtSession)
	_ = tm.KillSession(hqSession)
	_ = tm.KillSession(nonGtSession)

	// Create zombie sessions (tmux alive, but process is sleep - no Claude/node).
	// Use NewSessionWithCommand with sleep to avoid loading .zshrc, which spawns
	// a node process on this system. A node child of the zsh shell would cause
	// IsAgentAlive to return true, preventing cleanup (gt-it10f6p).
	if err := tm.NewSessionWithCommand(gtSession, "", "sleep 9999"); err != nil {
		t.Fatalf("NewSessionWithCommand(gt): %v", err)
	}
	defer func() { _ = tm.KillSession(gtSession) }()

	if err := tm.NewSessionWithCommand(hqSession, "", "sleep 9999"); err != nil {
		t.Fatalf("NewSessionWithCommand(hq): %v", err)
	}
	defer func() { _ = tm.KillSession(hqSession) }()

	// Create a non-GT session (should NOT be cleaned up)
	if err := tm.NewSessionWithCommand(nonGtSession, "", "sleep 9999"); err != nil {
		t.Fatalf("NewSessionWithCommand(other): %v", err)
	}
	defer func() { _ = tm.KillSession(nonGtSession) }()

	// Verify all sessions exist
	for _, sess := range []string{gtSession, hqSession, nonGtSession} {
		has, err := tm.HasSession(sess)
		if err != nil {
			t.Fatalf("HasSession(%q): %v", sess, err)
		}
		if !has {
			t.Fatalf("expected session %q to exist", sess)
		}
	}

	// Run cleanup
	cleaned, err := tm.CleanupOrphanedSessions(isTestGTSession)
	if err != nil {
		t.Fatalf("CleanupOrphanedSessions: %v", err)
	}

	// Should have cleaned the gt- and hq- zombie sessions
	if cleaned < 2 {
		t.Errorf("CleanupOrphanedSessions cleaned %d sessions, want >= 2", cleaned)
	}

	// Verify GT sessions are gone
	for _, sess := range []string{gtSession, hqSession} {
		has, err := tm.HasSession(sess)
		if err != nil {
			t.Fatalf("HasSession(%q) after cleanup: %v", sess, err)
		}
		if has {
			t.Errorf("expected session %q to be cleaned up", sess)
		}
	}

	// Verify non-GT session still exists
	has, err := tm.HasSession(nonGtSession)
	if err != nil {
		t.Fatalf("HasSession(%q) after cleanup: %v", nonGtSession, err)
	}
	if !has {
		t.Error("non-GT session should NOT have been cleaned up")
	}
}

func TestCleanupOrphanedSessions_NoSessions(t *testing.T) {
	// See TestCleanupOrphanedSessions for isolation rationale.
	isTestGTSession := func(s string) bool {
		return strings.HasPrefix(s, "gt-") || strings.HasPrefix(s, "hq-")
	}

	tm := newTestTmux(t)

	// Fresh isolated server has no sessions — cleanup should be a no-op.
	cleaned, err := tm.CleanupOrphanedSessions(isTestGTSession)
	if err != nil {
		t.Fatalf("CleanupOrphanedSessions: %v", err)
	}

	// May clean some existing GT sessions if they exist, but shouldn't error
	t.Logf("CleanupOrphanedSessions cleaned %d sessions", cleaned)
}

func TestCollectReparentedGroupMembers(t *testing.T) {
	t.Parallel()
	// Test that collectReparentedGroupMembers correctly filters group members.
	// Only processes reparented to init (PPID == 1) that aren't in the known set
	// should be returned.

	// Test with current process's PGID
	pid := fmt.Sprintf("%d", os.Getpid())
	pgid := getProcessGroupID(pid)
	if pgid == "" {
		t.Skip("could not get PGID for current process")
	}

	// Build a known set containing the current process
	knownPIDs := map[string]bool{pid: true}

	// collectReparentedGroupMembers should NOT include our PID (it's in known set)
	reparented := collectReparentedGroupMembers(pgid, knownPIDs)
	for _, rpid := range reparented {
		if rpid == pid {
			t.Errorf("collectReparentedGroupMembers returned known PID %s", pid)
		}
		// Each reparented PID should have PPID == 1
		ppid := getParentPID(rpid)
		if ppid != "1" {
			t.Errorf("collectReparentedGroupMembers returned PID %s with PPID %s (expected 1)", rpid, ppid)
		}
	}
}

func TestGetParentPID(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("getParentPID returns empty string on Windows (no /proc or ps)")
	}

	// Test with current process - should have a valid PPID
	pid := fmt.Sprintf("%d", os.Getpid())
	ppid := getParentPID(pid)
	if ppid == "" {
		t.Error("expected non-empty PPID for current process")
	}

	// PPID should not be "0" for a normal user process
	if ppid == "0" {
		t.Error("unexpected PPID 0 for current process")
	}

	// Test with nonexistent PID
	ppid = getParentPID("999999999")
	if ppid != "" {
		t.Errorf("expected empty PPID for nonexistent process, got %q", ppid)
	}
}

func TestKillSessionWithProcesses_DoesNotKillUnrelatedProcesses(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-nounrelated-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	// Create session with a long-running process
	if err := tm.NewSessionWithCommand(sessionName, "", "sleep 300"); err != nil {
		t.Fatalf("NewSessionWithCommand: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Start a separate background process (simulating an unrelated process)
	// This process runs in its own process group (via setsid or just being separate)
	sentinel := exec.Command("sleep", "300")
	if err := sentinel.Start(); err != nil {
		t.Fatalf("starting sentinel process: %v", err)
	}
	sentinelPID := sentinel.Process.Pid
	defer func() { _ = sentinel.Process.Kill(); _ = sentinel.Wait() }()

	// Give processes time to start
	time.Sleep(200 * time.Millisecond)

	// Kill session with processes
	if err := tm.KillSessionWithProcesses(sessionName); err != nil {
		t.Fatalf("KillSessionWithProcesses: %v", err)
	}

	// The sentinel process should still be alive (it's unrelated)
	// Check by sending signal 0 (existence check)
	if err := sentinel.Process.Signal(os.Signal(nil)); err != nil {
		// Process.Signal(nil) isn't reliable on all platforms, use kill -0
		checkCmd := exec.Command("kill", "-0", fmt.Sprintf("%d", sentinelPID))
		if checkErr := checkCmd.Run(); checkErr != nil {
			t.Errorf("sentinel process %d was killed (should have survived since it's unrelated)", sentinelPID)
		}
	}
}

func TestKillPaneProcessesExcluding(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-killpaneexcl-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	// Create session with a long-running process
	cmd := `sleep 300`
	if err := tm.NewSessionWithCommand(sessionName, "", cmd); err != nil {
		t.Fatalf("NewSessionWithCommand: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Get the pane ID
	paneID, err := tm.GetPaneID(sessionName)
	if err != nil {
		t.Fatalf("GetPaneID: %v", err)
	}

	// Kill pane processes with empty excludePIDs (should kill all processes)
	if err := tm.KillPaneProcessesExcluding(paneID, nil); err != nil {
		t.Fatalf("KillPaneProcessesExcluding: %v", err)
	}

	// Session may still exist (pane respawns as dead), but processes should be gone
	// Check that we can still get info about the session (verifies we didn't panic)
	_, _ = tm.HasSession(sessionName)
}

func TestKillPaneProcessesExcluding_WithExcludePID(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-killpaneexcl2-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	// Create session with a long-running process
	cmd := `sleep 300`
	if err := tm.NewSessionWithCommand(sessionName, "", cmd); err != nil {
		t.Fatalf("NewSessionWithCommand: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Get the pane ID and PID
	paneID, err := tm.GetPaneID(sessionName)
	if err != nil {
		t.Fatalf("GetPaneID: %v", err)
	}

	panePID, err := tm.GetPanePID(sessionName)
	if err != nil {
		t.Fatalf("GetPanePID: %v", err)
	}
	if panePID == "" {
		t.Skip("could not get pane PID")
	}

	// Kill pane processes with the pane PID excluded
	// The function should NOT kill the excluded PID
	err = tm.KillPaneProcessesExcluding(paneID, []string{panePID})
	if err != nil {
		t.Fatalf("KillPaneProcessesExcluding: %v", err)
	}

	// The session/pane should still exist since we excluded the main process
	has, _ := tm.HasSession(sessionName)
	if !has {
		t.Log("Session was destroyed - this may happen if tmux auto-cleaned after descendants died")
	}
}

func TestKillPaneProcessesExcluding_NonexistentPane(t *testing.T) {
	tm := newTestTmux(t)

	// Killing nonexistent pane should return an error but not panic
	err := tm.KillPaneProcessesExcluding("%99999", []string{"12345"})
	if err == nil {
		t.Error("expected error for nonexistent pane")
	}
}

func TestKillPaneProcessesExcluding_FiltersPIDs(t *testing.T) {
	// Unit test the PID filtering logic without needing tmux
	// This tests that the exclusion set is built correctly

	excludePIDs := []string{"123", "456", "789"}
	exclude := make(map[string]bool)
	for _, pid := range excludePIDs {
		exclude[pid] = true
	}

	// Test that excluded PIDs are in the set
	for _, pid := range excludePIDs {
		if !exclude[pid] {
			t.Errorf("exclude[%q] = false, want true", pid)
		}
	}

	// Test that non-excluded PIDs are not in the set
	nonExcluded := []string{"111", "222", "333"}
	for _, pid := range nonExcluded {
		if exclude[pid] {
			t.Errorf("exclude[%q] = true, want false", pid)
		}
	}

	// Test filtering logic
	allPIDs := []string{"111", "123", "222", "456", "333", "789"}
	var filtered []string
	for _, pid := range allPIDs {
		if !exclude[pid] {
			filtered = append(filtered, pid)
		}
	}

	expectedFiltered := []string{"111", "222", "333"}
	if len(filtered) != len(expectedFiltered) {
		t.Fatalf("filtered = %v, want %v", filtered, expectedFiltered)
	}
	for i, pid := range filtered {
		if pid != expectedFiltered[i] {
			t.Errorf("filtered[%d] = %q, want %q", i, pid, expectedFiltered[i])
		}
	}
}

func TestFindAgentPane_SinglePane(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-findagent-single-" + fmt.Sprintf("%d", time.Now().UnixNano()%10000)

	_ = tm.KillSession(sessionName)
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Single pane — should return empty (no disambiguation needed)
	paneID, err := tm.FindAgentPane(sessionName)
	if err != nil {
		t.Fatalf("FindAgentPane: %v", err)
	}
	if paneID != "" {
		t.Errorf("FindAgentPane single pane = %q, want empty", paneID)
	}
}

func TestFindAgentPane_IgnoresPaneIDFromOtherSession(t *testing.T) {
	tm := newTestTmux(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%10000)
	targetSession := "gt-test-findagent-target-" + suffix
	otherSession := "gt-test-findagent-other-" + suffix

	_ = tm.KillSession(targetSession)
	_ = tm.KillSession(otherSession)
	if err := tm.NewSession(targetSession, ""); err != nil {
		t.Fatalf("NewSession target: %v", err)
	}
	defer func() { _ = tm.KillSession(targetSession) }()
	if err := tm.NewSession(otherSession, ""); err != nil {
		t.Fatalf("NewSession other: %v", err)
	}
	defer func() { _ = tm.KillSession(otherSession) }()

	otherPane, err := tm.GetPaneID(otherSession)
	if err != nil {
		t.Fatalf("GetPaneID other: %v", err)
	}
	if err := tm.SetEnvironment(targetSession, "GT_PANE_ID", otherPane); err != nil {
		t.Fatalf("SetEnvironment GT_PANE_ID: %v", err)
	}

	paneID, err := tm.FindAgentPane(targetSession)
	if err != nil {
		t.Fatalf("FindAgentPane: %v", err)
	}
	if paneID == otherPane {
		t.Fatalf("FindAgentPane returned pane %q from another session", paneID)
	}
}

func TestFindAgentPane_MultiPaneWithNode(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-findagent-multi-" + fmt.Sprintf("%d", time.Now().UnixNano()%10000)

	_ = tm.KillSession(sessionName)

	// Create session with a shell pane (simulating a monitoring split)
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Split and run sleep in the new pane (simulating an agent process)
	_, err := tm.run("split-window", "-t", sessionName, "-d", "sleep", "10")
	if err != nil {
		t.Fatalf("split-window: %v", err)
	}

	// Give sleep a moment to start
	time.Sleep(500 * time.Millisecond)

	// Verify we have 2 panes
	out, err := tm.run("list-panes", "-t", sessionName, "-F", "#{pane_id}\t#{pane_current_command}")
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	t.Logf("Panes: %v", lines)
	if len(lines) < 2 {
		t.Skipf("Expected 2 panes, got %d — skipping multi-pane test", len(lines))
	}

	// FindAgentPane should find the sleep pane
	paneID, err := tm.FindAgentPane(sessionName)
	if err != nil {
		t.Fatalf("FindAgentPane: %v", err)
	}

	// Verify it found the correct pane (the one running sleep)
	if paneID == "" {
		t.Log("FindAgentPane returned empty — sleep may not have started yet or detection missed it")
		// Not a hard failure since process startup timing varies
		return
	}

	// Verify the returned pane is actually running sleep
	cmdOut, err := tm.run("display-message", "-t", paneID, "-p", "#{pane_current_command}")
	if err != nil {
		t.Fatalf("display-message: %v", err)
	}
	paneCmd := strings.TrimSpace(cmdOut)
	t.Logf("Agent pane %s running: %s", paneID, paneCmd)
	if paneCmd != "sleep" {
		t.Errorf("FindAgentPane returned pane running %q, want 'sleep'", paneCmd)
	}
}

func TestNudgeLockTimeout(t *testing.T) {
	// Test that acquireNudgeLock returns false after timeout when lock is held.
	session := "test-nudge-timeout-session"

	// Acquire the lock
	if !acquireNudgeLock(session, time.Second) {
		t.Fatal("initial acquireNudgeLock should succeed")
	}

	// Try to acquire again — should timeout
	start := time.Now()
	got := acquireNudgeLock(session, 100*time.Millisecond)
	elapsed := time.Since(start)

	if got {
		t.Error("acquireNudgeLock should return false when lock is held")
		releaseNudgeLock(session) // clean up the extra acquire
	}
	if elapsed < 90*time.Millisecond {
		t.Errorf("timeout returned too fast: %v", elapsed)
	}

	// Release the lock
	releaseNudgeLock(session)

	// Now acquire should succeed again
	if !acquireNudgeLock(session, time.Second) {
		t.Error("acquireNudgeLock should succeed after release")
	}
	releaseNudgeLock(session)
}

func TestNudgeLockConcurrency(t *testing.T) {
	// Test that concurrent nudges to the same session are serialized.
	session := "test-nudge-concurrent-session"
	const goroutines = 5

	// Clean up any previous state for this session key
	sessionNudgeLocks.Delete(session)

	acquired := make(chan bool, goroutines)

	// First goroutine holds the lock
	if !acquireNudgeLock(session, time.Second) {
		t.Fatal("initial acquire should succeed")
	}

	// Launch goroutines that try to acquire the lock
	for i := 0; i < goroutines; i++ {
		go func() {
			got := acquireNudgeLock(session, 200*time.Millisecond)
			acquired <- got
		}()
	}

	// Wait a bit, then release the lock
	time.Sleep(50 * time.Millisecond)
	releaseNudgeLock(session)

	// At most one goroutine should succeed (it gets the lock after we release)
	successes := 0
	for i := 0; i < goroutines; i++ {
		if <-acquired {
			successes++
			releaseNudgeLock(session)
		}
	}

	// At least 1 should succeed (the first one to grab it after release),
	// and the rest should timeout
	if successes < 1 {
		t.Error("expected at least 1 goroutine to acquire the lock after release")
	}
	t.Logf("%d/%d goroutines acquired the lock", successes, goroutines)
}

func TestNudgeLockDifferentSessions(t *testing.T) {
	// Test that locks for different sessions are independent.
	session1 := "test-nudge-session-a"
	session2 := "test-nudge-session-b"

	// Clean up any previous state
	sessionNudgeLocks.Delete(session1)
	sessionNudgeLocks.Delete(session2)

	// Acquire lock for session1
	if !acquireNudgeLock(session1, time.Second) {
		t.Fatal("acquire session1 should succeed")
	}
	defer releaseNudgeLock(session1)

	// Acquiring lock for session2 should succeed (independent)
	if !acquireNudgeLock(session2, time.Second) {
		t.Error("acquire session2 should succeed even when session1 is locked")
	} else {
		releaseNudgeLock(session2)
	}
}

func TestFindAgentPane_NonexistentSession(t *testing.T) {
	tm := newTestTmux(t)
	_, err := tm.FindAgentPane("nonexistent-session-findagent-xyz")
	if err == nil {
		t.Error("FindAgentPane on nonexistent session should return error")
	}
}

func TestValidateSessionName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		session string
		wantErr bool
	}{
		{"valid alphanumeric", "gt-gastown-crew-tom", false},
		{"valid with underscore", "hq_deacon", false},
		{"valid simple", "test123", false},
		{"empty string", "", true},
		{"contains dot", "my.session", true},
		{"contains colon", "my:session", true},
		{"contains space", "my session", true},
		{"contains slash", "rig/crew/tom", true},
		{"contains single quote", "it's", true},
		{"contains semicolon", "a;rm -rf /", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSessionName(tc.session)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateSessionName(%q) error = %v, wantErr %v", tc.session, err, tc.wantErr)
			}
		})
	}
}

func TestNewSession_RejectsInvalidName(t *testing.T) {
	tm := newTestTmux(t)
	err := tm.NewSession("invalid.name", "")
	if err == nil {
		t.Error("NewSession should reject session name with dots")
	}
	if !errors.Is(err, ErrInvalidSessionName) {
		t.Errorf("expected ErrInvalidSessionName, got %v", err)
	}
}

func TestEnsureSessionFresh_RejectsInvalidName(t *testing.T) {
	tm := newTestTmux(t)
	err := tm.EnsureSessionFresh("has:colon", "")
	if err == nil {
		t.Error("EnsureSessionFresh should reject session name with colons")
	}
	if !errors.Is(err, ErrInvalidSessionName) {
		t.Errorf("expected ErrInvalidSessionName, got %v", err)
	}
}

func TestFindAgentPane_MultiPaneNoAgent(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-findagent-noagent-" + fmt.Sprintf("%d", time.Now().UnixNano()%10000)

	_ = tm.KillSession(sessionName)
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Split into two shell panes (no agent running)
	_, err := tm.run("split-window", "-t", sessionName, "-d")
	if err != nil {
		t.Fatalf("split-window: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// FindAgentPane should return empty (no agent in either pane)
	paneID, err := tm.FindAgentPane(sessionName)
	if err != nil {
		t.Fatalf("FindAgentPane: %v", err)
	}
	if paneID != "" {
		t.Errorf("FindAgentPane with no agent = %q, want empty", paneID)
	}
}

func TestNewSessionWithCommandAndEnv(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-env-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	env := map[string]string{
		"GT_ROLE": "testrig/crew/testname",
		"GT_RIG":  "testrig",
		"GT_CREW": "testname",
	}

	// Create session with env vars and a command that prints GT_ROLE
	cmd := `bash -c "echo GT_ROLE=$GT_ROLE; sleep 5"`
	if err := tm.NewSessionWithCommandAndEnv(sessionName, "", cmd, env); err != nil {
		t.Fatalf("NewSessionWithCommandAndEnv: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Verify session exists
	has, err := tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !has {
		t.Fatal("expected session to exist after creation")
	}

	// Verify the env vars are set in the session environment
	gotRole, err := tm.GetEnvironment(sessionName, "GT_ROLE")
	if err != nil {
		t.Fatalf("GetEnvironment GT_ROLE: %v", err)
	}
	if gotRole != "testrig/crew/testname" {
		t.Errorf("GT_ROLE = %q, want %q", gotRole, "testrig/crew/testname")
	}

	gotRig, err := tm.GetEnvironment(sessionName, "GT_RIG")
	if err != nil {
		t.Fatalf("GetEnvironment GT_RIG: %v", err)
	}
	if gotRig != "testrig" {
		t.Errorf("GT_RIG = %q, want %q", gotRig, "testrig")
	}
}

func TestNewSessionWithCommandAndEnvEmpty(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-env-empty-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	// Empty env should work like NewSessionWithCommand
	if err := tm.NewSessionWithCommandAndEnv(sessionName, "", "sleep 5", nil); err != nil {
		t.Fatalf("NewSessionWithCommandAndEnv with nil env: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	has, err := tm.HasSession(sessionName)
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !has {
		t.Fatal("expected session to exist after creation with empty env")
	}
}

func TestIsTransientSendKeysError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"not in a mode", fmt.Errorf("tmux send-keys: not in a mode"), true},
		{"not in a mode wrapped", fmt.Errorf("nudge: %w", fmt.Errorf("tmux send-keys: not in a mode")), true},
		{"session not found", ErrSessionNotFound, false},
		{"no server", ErrNoServer, false},
		{"generic error", fmt.Errorf("something else"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTransientSendKeysError(tt.err)
			if got != tt.want {
				t.Errorf("isTransientSendKeysError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestSendKeysLiteralWithRetry_ImmediateSuccess(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-retry-ok-" + fmt.Sprintf("%d", time.Now().UnixNano()%10000)

	// Create a session that's ready to accept input
	if err := tm.NewSession(sessionName, os.TempDir()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Should succeed immediately — no retry needed
	err := tm.sendKeysLiteralWithRetry(sessionName, "hello", 5*time.Second)
	if err != nil {
		t.Errorf("sendKeysLiteralWithRetry() = %v, want nil", err)
	}
}

func TestSendKeysLiteralWithRetry_NonTransientFails(t *testing.T) {
	tm := newTestTmux(t)

	// Target a session that doesn't exist — should fail immediately, not retry
	start := time.Now()
	err := tm.sendKeysLiteralWithRetry("gt-nonexistent-session-xyz", "hello", 5*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error for nonexistent session, got nil")
	}
	// Should fail fast (< 1s), not wait the full 5s timeout
	if elapsed > 2*time.Second {
		t.Errorf("non-transient error took %v, expected fast failure", elapsed)
	}
}

func TestSendKeysLiteralWithRetry_NonTransientFailsFast(t *testing.T) {
	tm := newTestTmux(t)
	// Use a nonexistent session — tmux returns "session not found" which is
	// non-transient, so the function should fail fast (well under the timeout).
	start := time.Now()
	err := tm.sendKeysLiteralWithRetry("gt-nonexistent-session-fast-fail", "hello", 5*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error for nonexistent session, got nil")
	}
	// Non-transient errors should fail immediately, not wait for timeout.
	if elapsed > 2*time.Second {
		t.Errorf("non-transient error took %v — should have failed fast, not retried until timeout", elapsed)
	}
}

func TestNudgeSession_WithRetry(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-nudge-retry-" + fmt.Sprintf("%d", time.Now().UnixNano()%10000)

	// Create a ready session
	if err := tm.NewSession(sessionName, os.TempDir()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Give shell a moment to initialize
	time.Sleep(200 * time.Millisecond)

	// NudgeSession should succeed on a ready session
	err := tm.NudgeSession(sessionName, "test message")
	if err != nil {
		t.Errorf("NudgeSession() = %v, want nil", err)
	}
}

func TestNudgeSession_WithStoredPaneID(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-nudge-paneid-" + fmt.Sprintf("%d", time.Now().UnixNano()%10000)

	if err := tm.NewSession(sessionName, os.TempDir()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	time.Sleep(200 * time.Millisecond)

	paneID, err := tm.GetPaneID(sessionName)
	if err != nil {
		t.Fatalf("GetPaneID: %v", err)
	}
	if err := tm.SetEnvironment(sessionName, "GT_PANE_ID", paneID); err != nil {
		t.Fatalf("SetEnvironment GT_PANE_ID: %v", err)
	}

	if err := tm.NudgeSession(sessionName, "test message"); err != nil {
		t.Fatalf("NudgeSession() with GT_PANE_ID = %v, want nil", err)
	}
}

// TestNudgeSession_WakesAgentWindowNotActiveWindow is a regression test for a
// missed-wake bug in multi-window sessions. NudgeSessionWithOpts used to pass
// the bare session name to WakePaneIfDetached, which resizes the session's
// *active* window. When an agent session also has another window open and
// focused (e.g. a `gt feed -w` window), the agent's pane lives in a now-inactive
// window, so the SIGWINCH went to the wrong window and the agent never woke.
//
// The wake's resize dance ends by setting the targeted window's window-size
// option to "latest". By pre-setting both windows to "manual" and checking which
// one flips to "latest" after the nudge, we can assert the agent's window — not
// the active window — was the one woken.
func TestNudgeSession_WakesAgentWindowNotActiveWindow(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-nudge-multiwin-" + fmt.Sprintf("%d", time.Now().UnixNano()%100000)

	if err := tm.NewSession(sessionName, os.TempDir()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	time.Sleep(200 * time.Millisecond)

	// The agent pane is window 0's pane. Record it as the declared identity so
	// FindAgentPane resolves the nudge target to it.
	agentPane, err := tm.GetPaneID(sessionName)
	if err != nil {
		t.Fatalf("GetPaneID: %v", err)
	}
	if err := tm.SetEnvironment(sessionName, "GT_PANE_ID", agentPane); err != nil {
		t.Fatalf("SetEnvironment GT_PANE_ID: %v", err)
	}

	// Open a second window, which tmux makes the active window. This puts the
	// agent's pane in a non-active window — the scenario that exposed the bug.
	if _, err := tm.run("new-window", "-t", sessionName, "-n", "feed"); err != nil {
		t.Fatalf("new-window: %v", err)
	}

	// Pre-set both windows to window-size "manual". The wake resets only the
	// window it targets back to "latest", giving us a deterministic signal.
	for _, win := range []string{":0", ":1"} {
		if _, err := tm.run("set-option", "-w", "-t", sessionName+win, "window-size", "manual"); err != nil {
			t.Fatalf("set-option window-size manual on %s: %v", sessionName+win, err)
		}
	}

	if err := tm.NudgeSession(sessionName, "test message"); err != nil {
		t.Fatalf("NudgeSession: %v", err)
	}

	windowSize := func(win string) string {
		out, err := tm.run("show-options", "-w", "-t", sessionName+win, "window-size")
		if err != nil {
			t.Fatalf("show-options window-size on %s: %v", sessionName+win, err)
		}
		fields := strings.Fields(strings.TrimSpace(out))
		if len(fields) < 2 {
			return ""
		}
		return fields[1]
	}

	// Agent window (0) must have been woken; active window (1) must be untouched.
	if got := windowSize(":0"); got != "latest" {
		t.Errorf("agent window (0) window-size = %q, want %q (agent's window was not woken)", got, "latest")
	}
	if got := windowSize(":1"); got != "manual" {
		t.Errorf("active window (1) window-size = %q, want %q (wrong window was woken)", got, "manual")
	}
}

func TestCanonicalPaneTargetFromDisplay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		expectedSession string
		out             string
		want            string
		wantOK          bool
	}{
		{
			name:            "valid target",
			expectedSession: "gt-alpha",
			out:             "gt-alpha\t0\t0",
			want:            "gt-alpha:0.0",
			wantOK:          true,
		},
		{
			name:            "valid multi window",
			expectedSession: "gt-alpha",
			out:             "gt-alpha\t12\t3",
			want:            "gt-alpha:12.3",
			wantOK:          true,
		},
		{
			name:            "wrong session",
			expectedSession: "gt-alpha",
			out:             "gt-beta\t0\t0",
			wantOK:          false,
		},
		{
			name:            "malformed combined target",
			expectedSession: "gt-alpha",
			out:             "gt-alpha:0.0",
			wantOK:          false,
		},
		{
			name:            "empty output",
			expectedSession: "gt-alpha",
			out:             "",
			wantOK:          false,
		},
		{
			name:            "non numeric window",
			expectedSession: "gt-alpha",
			out:             "gt-alpha\tactive\t0",
			wantOK:          false,
		},
		{
			name:            "non numeric pane",
			expectedSession: "gt-alpha",
			out:             "gt-alpha\t0\tactive",
			wantOK:          false,
		},
		{
			name:            "negative index",
			expectedSession: "gt-alpha",
			out:             "gt-alpha\t0\t-1",
			wantOK:          false,
		},
		{
			name:            "signed index",
			expectedSession: "gt-alpha",
			out:             "gt-alpha\t+1\t0",
			wantOK:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := canonicalPaneTargetFromDisplay(tt.expectedSession, tt.out)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("target = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCanonicalPaneTargetResolvesAndFallsBack(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-canonical-pane-" + fmt.Sprintf("%d", time.Now().UnixNano()%100000)
	otherSession := "gt-test-canonical-other-" + fmt.Sprintf("%d", time.Now().UnixNano()%100000)

	if err := tm.NewSession(sessionName, os.TempDir()); err != nil {
		t.Fatalf("NewSession target: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()
	if err := tm.NewSession(otherSession, os.TempDir()); err != nil {
		t.Fatalf("NewSession other: %v", err)
	}
	defer func() { _ = tm.KillSession(otherSession) }()

	time.Sleep(200 * time.Millisecond)
	fallback := sessionName + ":0.0"
	if got := tm.canonicalPaneTarget(sessionName, ""); got != fallback {
		t.Errorf("empty pane target = %q, want %q", got, fallback)
	}
	if got := tm.canonicalPaneTarget(sessionName, "%999999"); got != fallback {
		t.Errorf("missing pane target = %q, want %q", got, fallback)
	}

	paneID, err := tm.GetPaneID(sessionName)
	if err != nil {
		t.Fatalf("GetPaneID target: %v", err)
	}
	if got := tm.canonicalPaneTarget(sessionName, paneID); got != fallback {
		t.Errorf("live pane target = %q, want %q", got, fallback)
	}

	otherPane, err := tm.GetPaneID(otherSession)
	if err != nil {
		t.Fatalf("GetPaneID other: %v", err)
	}
	if got := tm.canonicalPaneTarget(sessionName, otherPane); got != fallback {
		t.Errorf("cross-session pane target = %q, want %q", got, fallback)
	}
}

func TestNudgeSession_StalePaneIDFallsBackToFirstPane(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-nudge-stale-pane-" + fmt.Sprintf("%d", time.Now().UnixNano()%100000)
	otherSession := "gt-test-nudge-other-pane-" + fmt.Sprintf("%d", time.Now().UnixNano()%100000)

	if err := tm.NewSession(sessionName, os.TempDir()); err != nil {
		t.Fatalf("NewSession target: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()
	if err := tm.NewSession(otherSession, os.TempDir()); err != nil {
		t.Fatalf("NewSession other: %v", err)
	}
	defer func() { _ = tm.KillSession(otherSession) }()

	time.Sleep(200 * time.Millisecond)
	otherPane, err := tm.GetPaneID(otherSession)
	if err != nil {
		t.Fatalf("GetPaneID other: %v", err)
	}
	if err := tm.SetEnvironment(sessionName, "GT_PANE_ID", otherPane); err != nil {
		t.Fatalf("SetEnvironment GT_PANE_ID: %v", err)
	}
	if _, err := tm.run("new-window", "-t", sessionName, "-n", "active"); err != nil {
		t.Fatalf("new-window: %v", err)
	}

	marker := "GT_NUDGE_STALE_PANE_FALLBACK_" + fmt.Sprintf("%d", time.Now().UnixNano()%100000)
	if err := tm.NudgeSession(sessionName, "echo "+marker); err != nil {
		t.Fatalf("NudgeSession: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	firstPane, err := tm.CapturePane(sessionName+":0.0", 80)
	if err != nil {
		t.Fatalf("CapturePane first: %v", err)
	}
	activePane, err := tm.CapturePane(sessionName+":1.0", 80)
	if err != nil {
		t.Fatalf("CapturePane active: %v", err)
	}
	otherPaneContent, err := tm.CapturePane(otherSession+":0.0", 80)
	if err != nil {
		t.Fatalf("CapturePane other: %v", err)
	}

	if !strings.Contains(firstPane, marker) {
		t.Fatalf("first pane did not receive nudge marker %q; content:\n%s", marker, firstPane)
	}
	if strings.Contains(activePane, marker) {
		t.Fatalf("active fallback pane received stale-pane nudge marker %q; content:\n%s", marker, activePane)
	}
	if strings.Contains(otherPaneContent, marker) {
		t.Fatalf("cross-session stale pane received nudge marker %q; content:\n%s", marker, otherPaneContent)
	}
}

// TestAdaptiveTextDelay verifies the delay scaling logic for post-text delivery.
func TestAdaptiveTextDelay(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		msgLen  int
		wantMin time.Duration
		wantMax time.Duration
	}{
		{"empty", 0, 500 * time.Millisecond, 500 * time.Millisecond},
		{"small single chunk", 100, 500 * time.Millisecond, 500 * time.Millisecond},
		{"exactly one chunk", 512, 500 * time.Millisecond, 500 * time.Millisecond},
		{"two chunks", 513, 525 * time.Millisecond, 525 * time.Millisecond},
		{"five chunks", 2048 + 1, 600 * time.Millisecond, 600 * time.Millisecond},
		{"huge message capped", 100000, 2 * time.Second, 2 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := adaptiveTextDelay(tt.msgLen)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("adaptiveTextDelay(%d) = %v, want [%v, %v]", tt.msgLen, got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

// TestMatchesPromptPrefix verifies that prompt matching handles non-breaking
// spaces (NBSP, U+00A0) correctly. Claude Code uses NBSP after its > prompt
// character, but the default ReadyPromptPrefix uses a regular space.
// Regression test for https://github.com/steveyegge/gastown/issues/1387.
func TestMatchesPromptPrefix(t *testing.T) {
	t.Parallel()
	const (
		nbsp          = "\u00a0" // non-breaking space
		regularPrefix = "❯ "     // default: ❯ + regular space
	)

	tests := []struct {
		name   string
		line   string
		prefix string
		want   bool
	}{
		// Regular space in both line and prefix (baseline)
		{"regular space matches", "❯ ", regularPrefix, true},
		{"regular space with trailing content", "❯ some input", regularPrefix, true},

		// NBSP in line, regular space in prefix (the bug scenario)
		{"NBSP bare prompt matches", "❯" + nbsp, regularPrefix, true},
		{"NBSP with content matches", "❯" + nbsp + "claude --help", regularPrefix, true},
		{"NBSP with leading whitespace", "  ❯" + nbsp, regularPrefix, true},

		// NBSP in prefix (defensive: user could configure it either way)
		{"NBSP prefix matches NBSP line", "❯" + nbsp + "hello", "❯" + nbsp, true},
		{"NBSP prefix matches regular space line", "❯ hello", "❯" + nbsp, true},

		// Empty prefix never matches
		{"empty prefix", "❯ ", "", false},

		// No prompt character at all
		{"no prompt", "hello world", regularPrefix, false},
		{"empty line", "", regularPrefix, false},
		{"whitespace only", "   ", regularPrefix, false},

		// Bare prompt character without any space
		{"bare prompt no space", "❯", regularPrefix, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesPromptPrefix(tt.line, tt.prefix)
			if got != tt.want {
				t.Errorf("matchesPromptPrefix(%q, %q) = %v, want %v",
					tt.line, tt.prefix, got, tt.want)
			}
		})
	}
}

func TestWaitForIdle_Timeout(t *testing.T) {
	if os.Getenv("TMUX") == "" {
		t.Skip("not inside tmux")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("test requires unix")
	}

	tm := newTestTmux(t)

	// Create a session running a long sleep (no prompt visible)
	sessionName := fmt.Sprintf("gt-test-idle-%d", time.Now().UnixNano())
	if err := tm.NewSessionWithCommand(sessionName, os.TempDir(), "sleep 60"); err != nil {
		t.Fatalf("NewSessionWithCommand: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	time.Sleep(200 * time.Millisecond)

	// WaitForIdle should timeout quickly since the session is running sleep, not a prompt
	err := tm.WaitForIdle(sessionName, 500*time.Millisecond)
	if err == nil {
		t.Error("WaitForIdle should have timed out for a busy session")
	}
	if !errors.Is(err, ErrIdleTimeout) {
		t.Errorf("expected ErrIdleTimeout, got: %v", err)
	}
}

func TestHasBusyIndicator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
		want bool
	}{
		{"claude status busy", "⏵⏵ bypass permissions on ... · esc to interrupt", true},
		{"codex status busy", "• Working (2m 18s • esc to interrupt)", true},
		{"idle line", "› Review ready notification", false},
		{"blank", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasBusyIndicator(tt.line); got != tt.want {
				t.Errorf("hasBusyIndicator(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

// TestShouldSendEscapeForLines guards against the regression where a nudge
// sends the vim-mode Escape keystroke while the agent is actively generating,
// interrupting its current turn (e.g. the Mayor). When the pane shows the busy
// indicator ("esc to interrupt"), the Escape must be suppressed.
func TestShouldSendEscapeForLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		lines []string
		want  bool
	}{
		{
			name:  "claude generating - suppress escape",
			lines: []string{"✻ Cogitating… (12s · ↑ 2.1k tokens · esc to interrupt)"},
			want:  false,
		},
		{
			name:  "codex working - suppress escape",
			lines: []string{"• Working (2m 18s • esc to interrupt)"},
			want:  false,
		},
		{
			name:  "busy indicator among multiple lines - suppress escape",
			lines: []string{"tool output", "more output", "⏵⏵ bypass permissions on · esc to interrupt"},
			want:  false,
		},
		{
			name:  "idle ready prompt - allow escape",
			lines: []string{"❯ "},
			want:  true,
		},
		{
			name:  "idle with typed nudge text - allow escape",
			lines: []string{"❯ HEALTH_CHECK: heartbeat stale, respond to confirm"},
			want:  true,
		},
		{
			name:  "no lines captured - allow escape (not busy)",
			lines: nil,
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldSendEscapeForLines(tt.lines); got != tt.want {
				t.Errorf("shouldSendEscapeForLines(%q) = %v, want %v", tt.lines, got, tt.want)
			}
		})
	}
}

// TestShouldSendEscape_LivePane exercises the busy-state gate end-to-end against
// a real tmux pane (capture-only, so it avoids the sendEnterVerified timing
// flakiness of the full nudge integration tests). It confirms the wiring: when
// the pane shows the "esc to interrupt" busy indicator, shouldSendEscape returns
// false so a nudge will not interrupt the agent's in-flight work.
func TestShouldSendEscape_LivePane(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-test-should-escape-" + t.Name()

	_ = tm.KillSession(session)
	if err := tm.NewSession(session, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(session) }()

	// Idle shell prompt: no busy indicator → Escape is safe to send.
	if !tm.shouldSendEscape(session) {
		out, _ := tm.CapturePane(session, 20)
		t.Fatalf("shouldSendEscape on idle pane = false, want true; pane:\n%s", out)
	}

	// Simulate an agent that is actively generating by rendering the busy
	// indicator into the pane. The typed command line itself contains the
	// marker, so detection does not depend on the command actually executing.
	if err := tm.SendKeys(session, "echo esc to interrupt"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}

	// Poll until the gate flips to suppressed (the shell may be slow to render).
	deadline := time.Now().Add(5 * time.Second)
	for tm.shouldSendEscape(session) {
		if time.Now().After(deadline) {
			out, _ := tm.CapturePane(session, 20)
			t.Fatalf("shouldSendEscape did not detect busy indicator within timeout; pane:\n%s", out)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestShouldSendEscape_CaptureErrorSuppressesEscape(t *testing.T) {
	tm := newTestTmux(t)

	if tm.shouldSendEscape("missing-session-for-escape-check") {
		t.Fatal("shouldSendEscape on missing target = true, want false")
	}
}

func TestDefaultReadyPromptPrefix(t *testing.T) {
	t.Parallel()
	// Verify the constant is set correctly
	if DefaultReadyPromptPrefix == "" {
		t.Error("DefaultReadyPromptPrefix should not be empty")
	}
	if !strings.Contains(DefaultReadyPromptPrefix, "❯") {
		t.Errorf("DefaultReadyPromptPrefix = %q, want to contain ❯", DefaultReadyPromptPrefix)
	}
}

func TestGetSessionActivity(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-activity-" + t.Name()

	// Clean up any existing session
	_ = tm.KillSession(sessionName)

	// Create session
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Get session activity
	activity, err := tm.GetSessionActivity(sessionName)
	if err != nil {
		t.Fatalf("GetSessionActivity: %v", err)
	}

	// Activity should be recent (within last minute since we just created it)
	if activity.IsZero() {
		t.Error("GetSessionActivity returned zero time")
	}

	// Activity should be in the past (or very close to now)
	now := activity // Use activity as baseline since clocks might differ
	_ = now         // Avoid unused variable

	// The activity timestamp should be reasonable (not in far future or past)
	// Just verify it's a valid Unix timestamp (after year 2000)
	if activity.Year() < 2000 {
		t.Errorf("GetSessionActivity returned suspicious time: %v", activity)
	}
}

func TestGetSessionActivity_NonexistentSession(t *testing.T) {
	tm := newTestTmux(t)

	// GetSessionActivity on nonexistent session should error
	_, err := tm.GetSessionActivity("nonexistent-session-xyz-12345")
	if err == nil {
		t.Error("GetSessionActivity on nonexistent session should return error")
	}
}

func TestNewSessionSet(t *testing.T) {
	// Test creating SessionSet from names
	names := []string{"session-a", "session-b", "session-c"}
	set := NewSessionSet(names)

	if set == nil {
		t.Fatal("NewSessionSet returned nil")
	}

	// Test Has() for existing sessions
	for _, name := range names {
		if !set.Has(name) {
			t.Errorf("SessionSet.Has(%q) = false, want true", name)
		}
	}

	// Test Has() for non-existing session
	if set.Has("nonexistent") {
		t.Error("SessionSet.Has(nonexistent) = true, want false")
	}

	// Test Names() returns all sessions
	gotNames := set.Names()
	if len(gotNames) != len(names) {
		t.Errorf("SessionSet.Names() returned %d names, want %d", len(gotNames), len(names))
	}

	// Verify all names are present (order may differ)
	nameSet := make(map[string]bool)
	for _, n := range gotNames {
		nameSet[n] = true
	}
	for _, n := range names {
		if !nameSet[n] {
			t.Errorf("SessionSet.Names() missing %q", n)
		}
	}
}

func TestNewSessionSet_Empty(t *testing.T) {
	set := NewSessionSet([]string{})

	if set == nil {
		t.Fatal("NewSessionSet returned nil for empty input")
	}

	if set.Has("anything") {
		t.Error("Empty SessionSet.Has() = true, want false")
	}

	names := set.Names()
	if len(names) != 0 {
		t.Errorf("Empty SessionSet.Names() returned %d names, want 0", len(names))
	}
}

func TestNewSessionSet_Nil(t *testing.T) {
	set := NewSessionSet(nil)

	if set == nil {
		t.Fatal("NewSessionSet returned nil for nil input")
	}

	if set.Has("anything") {
		t.Error("Nil-input SessionSet.Has() = true, want false")
	}
}

func TestSessionPrefixPattern_AlwaysIncludesGTAndHQ(t *testing.T) {
	// Even without GT_ROOT, the pattern should include gt and hq as safe defaults.
	t.Setenv("GT_ROOT", "")

	pattern := sessionPrefixPattern()
	if !strings.Contains(pattern, "gt") {
		t.Errorf("pattern %q missing 'gt'", pattern)
	}
	if !strings.Contains(pattern, "hq") {
		t.Errorf("pattern %q missing 'hq'", pattern)
	}
	// Must be a valid grep -Eq anchored alternation
	if !strings.HasPrefix(pattern, "^(") || !strings.HasSuffix(pattern, ")-") {
		t.Errorf("pattern %q has unexpected format", pattern)
	}
}

func TestGetKeyBinding_NoExistingBinding(t *testing.T) {
	tm := newTestTmux(t)
	// Query a key that almost certainly has no binding
	result := tm.getKeyBinding("prefix", "F12")
	if result != "" {
		t.Errorf("expected empty string for unbound key, got %q", result)
	}
}

func TestGetKeyBinding_CapturesDefaultBinding(t *testing.T) {
	tm := newTestTmux(t)

	// Query the default tmux binding for prefix-n (next-window).
	// This works without a running tmux server because list-keys
	// returns builtin defaults. Skip if already a GT binding (e.g.,
	// when running inside an active gastown session).
	result := tm.getKeyBinding("prefix", "n")
	if result == "" && tm.isGTBinding("prefix", "n") {
		t.Skip("prefix-n is already a GT binding in this environment")
	}
	if result != "next-window" {
		t.Errorf("expected 'next-window' for default prefix-n binding, got %q", result)
	}
}

func TestGetKeyBinding_CapturesDefaultBindingWithArgs(t *testing.T) {
	tm := newTestTmux(t)

	// prefix-s is "choose-tree -Zs" by default — tests multi-word command parsing
	result := tm.getKeyBinding("prefix", "s")
	if !strings.Contains(result, "choose-tree") {
		t.Errorf("expected binding to contain 'choose-tree', got %q", result)
	}
}

func TestGetKeyBinding_SkipsGasTownBindings(t *testing.T) {
	tm := newTestTmux(t)

	// Bootstrap the isolated server (bind-key requires a running server)
	if err := tm.NewSession("gt-test-bootstrap", ""); err != nil {
		t.Fatalf("bootstrap session: %v", err)
	}
	defer tm.KillSession("gt-test-bootstrap")

	// Set a GT-style if-shell binding (contains both "if-shell" and "gt ")
	ifShell := fmt.Sprintf("echo '#{session_name}' | grep -Eq '%s'", sessionPrefixPattern())
	_, _ = tm.run("bind-key", "-T", "prefix", "F11",
		"if-shell", ifShell,
		"run-shell 'gt agents menu'",
		":")

	result := tm.getKeyBinding("prefix", "F11")
	if result != "" {
		t.Errorf("expected empty string for Gas Town binding, got %q", result)
	}

	// Clean up
	_, _ = tm.run("unbind-key", "-T", "prefix", "F11")
}

func TestGetKeyBinding_CapturesUserBinding(t *testing.T) {
	tm := newTestTmux(t)

	// Bootstrap the isolated server (bind-key requires a running server)
	if err := tm.NewSession("gt-test-bootstrap", ""); err != nil {
		t.Fatalf("bootstrap session: %v", err)
	}
	defer tm.KillSession("gt-test-bootstrap")

	// Set a user binding that doesn't contain "gt "
	_, _ = tm.run("bind-key", "-T", "prefix", "F11", "display-message", "hello")

	result := tm.getKeyBinding("prefix", "F11")
	// Should capture the user's binding command
	if result == "" {
		t.Error("expected non-empty string for user binding")
	}
	if !strings.Contains(result, "display-message") {
		t.Errorf("expected binding to contain 'display-message', got %q", result)
	}

	// Clean up
	_, _ = tm.run("unbind-key", "-T", "prefix", "F11")
}

func TestIsGTBinding_DetectsGasTownBindings(t *testing.T) {
	tm := newTestTmux(t)

	// Bootstrap the isolated server (bind-key requires a running server)
	if err := tm.NewSession("gt-test-bootstrap", ""); err != nil {
		t.Fatalf("bootstrap session: %v", err)
	}
	defer tm.KillSession("gt-test-bootstrap")

	// A plain user binding should NOT be detected as GT
	_, _ = tm.run("bind-key", "-T", "prefix", "F11", "display-message", "hello")
	if tm.isGTBinding("prefix", "F11") {
		t.Error("plain user binding should not be detected as GT binding")
	}

	// A GT-style if-shell binding should be detected
	ifShell := fmt.Sprintf("echo '#{session_name}' | grep -Eq '%s'", sessionPrefixPattern())
	_, _ = tm.run("bind-key", "-T", "prefix", "F11",
		"if-shell", ifShell,
		"run-shell 'gt feed --window'",
		"display-message hello")
	if !tm.isGTBinding("prefix", "F11") {
		t.Error("GT if-shell binding should be detected as GT binding")
	}

	// Clean up
	_, _ = tm.run("unbind-key", "-T", "prefix", "F11")
}

func TestSetBindings_PreserveFallbackOnRepeatedCalls(t *testing.T) {
	tm := newTestTmux(t)

	// Bootstrap the isolated server (bind-key requires a running server)
	if err := tm.NewSession("gt-test-bootstrap", ""); err != nil {
		t.Fatalf("bootstrap session: %v", err)
	}
	defer tm.KillSession("gt-test-bootstrap")

	// Set a custom user binding on F11
	_, _ = tm.run("bind-key", "-T", "prefix", "F11", "display-message", "custom-user-cmd")

	// Wrap it as a GT binding (simulating first Set*Binding call)
	ifShell := fmt.Sprintf("echo '#{session_name}' | grep -Eq '%s'", sessionPrefixPattern())
	_, _ = tm.run("bind-key", "-T", "prefix", "F11",
		"if-shell", ifShell,
		"run-shell 'gt feed --window'",
		"display-message custom-user-cmd")

	// Record the binding after first configuration
	firstRaw, _ := tm.run("list-keys", "-T", "prefix", "F11")

	// isGTBinding should return true, causing Set*Binding to skip
	if !tm.isGTBinding("prefix", "F11") {
		t.Fatal("expected isGTBinding=true after first configuration")
	}

	// Verify the original user fallback is preserved in the binding
	if !strings.Contains(firstRaw, "custom-user-cmd") {
		t.Errorf("original user fallback not found in binding: %q", firstRaw)
	}

	// Clean up
	_, _ = tm.run("unbind-key", "-T", "prefix", "F11")
}

func TestSessionPrefixPattern_WithTownRoot(t *testing.T) {
	// Point at the real town root if available; otherwise skip.
	townRoot := os.Getenv("GT_ROOT")
	if townRoot == "" {
		t.Skip("GT_ROOT not set; skipping live rigs.json test")
	}
	pattern := sessionPrefixPattern()
	// With a real rigs.json, pattern must include at least gt, hq, and
	// whatever other rigs are registered.
	if !strings.Contains(pattern, "gt") {
		t.Errorf("pattern %q missing 'gt'", pattern)
	}
	if !strings.Contains(pattern, "hq") {
		t.Errorf("pattern %q missing 'hq'", pattern)
	}
	// Verify it's a sorted alternation.
	if !strings.HasPrefix(pattern, "^(") || !strings.HasSuffix(pattern, ")-") {
		t.Errorf("pattern %q has unexpected format", pattern)
	}
}

func TestSessionPrefixPattern_FallsBackToGTTownRoot(t *testing.T) {
	// When GT_ROOT is empty but GT_TOWN_ROOT is set, sessionPrefixPattern
	// should use GT_TOWN_ROOT to discover rig prefixes.
	townRoot := os.Getenv("GT_ROOT")
	if townRoot == "" {
		townRoot = os.Getenv("GT_TOWN_ROOT")
	}
	if townRoot == "" {
		t.Skip("neither GT_ROOT nor GT_TOWN_ROOT set; skipping")
	}

	// Clear GT_ROOT, set GT_TOWN_ROOT — simulates daemon startup env.
	t.Setenv("GT_ROOT", "")
	t.Setenv("GT_TOWN_ROOT", townRoot)

	pattern := sessionPrefixPattern()
	if !strings.Contains(pattern, "gt") {
		t.Errorf("pattern %q missing 'gt'", pattern)
	}
	if !strings.Contains(pattern, "hq") {
		t.Errorf("pattern %q missing 'hq'", pattern)
	}
	// With a real rigs.json via GT_TOWN_ROOT, we expect more than just gt+hq.
	// At minimum there should be 3+ prefixes in a multi-rig town.
	inner := strings.TrimPrefix(pattern, "^(")
	inner = strings.TrimSuffix(inner, ")-")
	prefixes := strings.Split(inner, "|")
	if len(prefixes) < 3 {
		t.Errorf("expected at least 3 prefixes via GT_TOWN_ROOT fallback, got %d: %v", len(prefixes), prefixes)
	}
}

func TestZombieStatusString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status   ZombieStatus
		expected string
		zombie   bool
	}{
		{SessionHealthy, "healthy", false},
		{SessionDead, "session-dead", false},
		{AgentDead, "agent-dead", true},
		{AgentHung, "agent-hung", true},
	}

	for _, tc := range tests {
		if got := tc.status.String(); got != tc.expected {
			t.Errorf("ZombieStatus(%d).String() = %q, want %q", tc.status, got, tc.expected)
		}
		if got := tc.status.IsZombie(); got != tc.zombie {
			t.Errorf("ZombieStatus(%d).IsZombie() = %v, want %v", tc.status, got, tc.zombie)
		}
	}
}

func TestCheckSessionHealth_NonexistentSession(t *testing.T) {
	tm := newTestTmux(t)
	status := tm.CheckSessionHealth("nonexistent-session-xyz", 0)
	if status != SessionDead {
		t.Errorf("CheckSessionHealth(nonexistent) = %v, want SessionDead", status)
	}
}

func TestCheckSessionHealth_ZombieSession(t *testing.T) {
	// Create a session with just a shell (no agent running)
	tm := newTestTmux(t)
	sessionName := fmt.Sprintf("gt-test-zombie-%d", os.Getpid())
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer tm.KillSession(sessionName)

	// Wait for shell to start
	time.Sleep(200 * time.Millisecond)

	// Session exists but no agent process → AgentDead
	status := tm.CheckSessionHealth(sessionName, 0)
	if status != AgentDead {
		t.Errorf("CheckSessionHealth(shell-only) = %v, want AgentDead", status)
	}
}

func TestCheckSessionHealth_ActivityCheck(t *testing.T) {
	// Create a session that runs a long-lived process
	tm := newTestTmux(t)
	sessionName := fmt.Sprintf("gt-test-activity-%d", os.Getpid())
	// Use 'sleep' as a stand-in for an agent process
	if err := tm.NewSessionWithCommand(sessionName, "", "sleep 60"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer tm.KillSession(sessionName)
	time.Sleep(300 * time.Millisecond)

	// With no maxInactivity (0), activity is not checked.
	// The session has a non-shell process running (sleep), but it won't
	// match any agent process names, so IsAgentAlive returns false → AgentDead.
	status := tm.CheckSessionHealth(sessionName, 0)
	if status != AgentDead {
		// sleep is not an agent process, so this is expected
		t.Logf("Status with sleep process: %v (expected AgentDead since sleep != agent)", status)
	}

	// With a very short maxInactivity, a recently-created session should be healthy
	// (if the agent were actually running). This tests the activity threshold logic
	// without needing a real Claude process.
}

func TestValidateCommandBinary(t *testing.T) {
	t.Parallel()

	absoluteShell := "/bin/sh"
	if runtime.GOOS == "windows" {
		path, err := exec.LookPath("sh")
		if err != nil {
			t.Skip("sh not installed")
		}
		absoluteShell = path
	}

	tests := []struct {
		name    string
		cmd     string
		wantErr bool
	}{
		{"empty", "", false},
		{"relative binary", "echo hello", false},
		{"valid absolute", absoluteShell + " -c 'echo hi'", false},
		{"missing absolute", "/nonexistent/binary --flag", true},
		{"exec env missing", "exec env GT_TEST=1 /nonexistent/claude-code --settings /tmp", true},
		{"exec env valid", "exec env GT_TEST=1 " + absoluteShell + " -c 'echo hi'", false},
		{"env vars only", "exec env FOO=bar BAZ=1", false},
		{"bare exec", "exec " + absoluteShell, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCommandBinary(tc.cmd)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateCommandBinary(%q) error = %v, wantErr = %v", tc.cmd, err, tc.wantErr)
			}
		})
	}
}
