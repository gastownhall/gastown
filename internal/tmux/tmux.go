// Package tmux provides a wrapper for tmux session operations via subprocess.
package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/telemetry"
)

// sessionNudgeLocks serializes nudges to the same session.
// This prevents interleaving when multiple nudges arrive concurrently,
// which can cause garbled input and missed Enter keys.
// Uses channel-based semaphores instead of sync.Mutex to support
// timed lock acquisition — preventing permanent lockout if a nudge hangs.
var sessionNudgeLocks sync.Map // map[string]chan struct{}

// nudgeLockTimeout is how long to wait to acquire the per-session nudge lock.
// If a previous nudge is still holding the lock after this duration, we give up
// rather than blocking forever. This prevents a hung tmux from permanently
// blocking all future nudges to that session.
const nudgeLockTimeout = 30 * time.Second

// validSessionNameRe validates session names to prevent shell injection
var validSessionNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Common errors
var (
	ErrNoServer           = errors.New("no tmux server running")
	ErrSessionExists      = errors.New("session already exists")
	ErrSessionNotFound    = errors.New("session not found")
	ErrSessionRunning     = errors.New("session already running with healthy agent")
	ErrInvalidSessionName = errors.New("invalid session name")
	ErrIdleTimeout        = errors.New("agent not idle before timeout")
)

// validateSessionName checks that a session name contains only safe characters.
// Returns ErrInvalidSessionName if the name contains dots, colons, or other
// characters that cause tmux to silently fail or produce cryptic errors.
func validateSessionName(name string) error {
	if name == "" || !validSessionNameRe.MatchString(name) {
		return fmt.Errorf("%w %q: must match %s", ErrInvalidSessionName, name, validSessionNameRe.String())
	}
	return nil
}

// validateCommandBinary extracts the binary path from a tmux session command
// and verifies it exists on disk. Handles common patterns:
//   - "exec env VAR=val /path/to/binary --args"
//   - "/path/to/binary --args"
//   - "sh -c '...'" (skipped — shell will handle resolution)
//
// Only checks absolute paths to avoid false positives on shell builtins.
func validateCommandBinary(command string) error {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil
	}

	// Skip past "exec" and "env" prefixes, KEY=VAL assignments,
	// and PowerShell $env: assignments and call operator (&).
	i := 0
	for i < len(fields) {
		f := fields[i]
		if f == "exec" || f == "env" || f == "&" {
			i++
			continue
		}
		// POSIX: KEY=VAL
		if strings.Contains(f, "=") && !strings.HasPrefix(f, "/") && !strings.HasPrefix(f, "-") {
			i++
			continue
		}
		// PowerShell: $env:KEY='val'; (may span multiple fields if value has spaces)
		if strings.HasPrefix(f, "$env:") {
			i++
			// Skip continuation fields until we see a semicolon-terminated one
			for i < len(fields) && !strings.HasSuffix(fields[i-1], ";") {
				i++
			}
			continue
		}
		break
	}

	if i >= len(fields) {
		return nil
	}

	binary := fields[i]
	// Only validate absolute paths — relative or bare names are resolved by shell.
	if !strings.HasPrefix(binary, "/") {
		return nil
	}
	if _, err := os.Stat(binary); err != nil {
		return fmt.Errorf("command binary not found: %s", binary)
	}
	return nil
}

// defaultSocket is the tmux socket name (-L flag) for multi-instance isolation.
// When set, all tmux commands use this socket instead of the default server.
// Access is protected by defaultSocketMu for concurrent test safety.
var (
	defaultSocket   string
	defaultSocketMu sync.RWMutex
)

// SetDefaultSocket sets the package-level default tmux socket name.
// Called during init to scope tmux to the current town.
func SetDefaultSocket(name string) {
	defaultSocketMu.Lock()
	defaultSocket = name
	defaultSocketMu.Unlock()
}

// GetDefaultSocket returns the current default tmux socket name.
func GetDefaultSocket() string {
	defaultSocketMu.RLock()
	defer defaultSocketMu.RUnlock()
	return defaultSocket
}

// SocketDir returns the directory where tmux stores its socket files.
// On macOS, tmux uses /tmp (not $TMPDIR which points to /var/folders/...),
// so we must use /tmp directly rather than os.TempDir().
func SocketDir() string {
	return filepath.Join("/tmp", fmt.Sprintf("tmux-%d", os.Getuid()))
}

// IsInSameSocket checks if the current process is inside a tmux session on the
// same socket as the default town socket. Used to decide between switch-client
// (same socket) and attach-session (different socket or outside tmux).
func IsInSameSocket() bool {
	tmuxEnv := os.Getenv("TMUX")
	if tmuxEnv == "" {
		return false
	}
	// TMUX format: /tmp/tmux-UID/socketname,pid,index
	parts := strings.SplitN(tmuxEnv, ",", 2)
	currentSocket := filepath.Base(parts[0])

	targetSocket := GetDefaultSocket()
	if targetSocket == "" {
		targetSocket = "default"
	}
	return currentSocket == targetSocket
}

// BuildCommand creates an exec.Cmd for tmux with the default socket applied.
// Use this instead of exec.Command("tmux", ...) for code outside the Tmux struct.
func BuildCommand(args ...string) *exec.Cmd {
	return BuildCommandContext(context.Background(), args...)
}

// BuildCommandContext is like BuildCommand but honors a context for cancellation.
func BuildCommandContext(ctx context.Context, args ...string) *exec.Cmd {
	allArgs := []string{"-u"}
	if sock := GetDefaultSocket(); sock != "" {
		allArgs = append(allArgs, "-L", sock)
	}
	allArgs = append(allArgs, args...)
	cmd := exec.CommandContext(ctx, "tmux", allArgs...)
	hideConsoleWindow(cmd)
	return cmd
}

// Tmux wraps tmux operations.
type Tmux struct {
	socketName string // tmux socket name (-L flag), empty = default socket
}

// noTownSocket is a sentinel socket name used when no town socket is configured.
// Using a non-existent socket causes tmux operations to fail with a clear
// "no server running" error instead of silently connecting to the wrong server.
const noTownSocket = "gt-no-town-socket"

// EnvAgentReady is the tmux session environment variable set by the agent's
// SessionStart hook (gt prime --hook) to signal that the agent has started.
// Used by WaitForCommand as a ZFC-compliant fallback for detecting wrapped
// agents (where pane_current_command remains a shell). See gt-sk5u.
const EnvAgentReady = "GT_AGENT_READY"

// NewTmux creates a new Tmux wrapper using the initialized town socket.
// Falls back to GT_TOWN_SOCKET env var (set by cross-socket tmux bindings).
// Empty socket means use the default tmux server.
func NewTmux() *Tmux {
	sock := GetDefaultSocket()
	if sock == "" {
		// GT_TOWN_SOCKET is embedded in tmux bindings created by EnsureBindingsOnSocket
		// so that "gt agents menu" / "gt feed" invoked from a personal terminal still
		// target the correct town server even when InitRegistry was not called.
		sock = os.Getenv("GT_TOWN_SOCKET")
	}
	return &Tmux{socketName: sock}
}

// NewTmuxWithSocket creates a Tmux wrapper that targets a named socket.
// This creates/connects to an isolated tmux server, separate from the user's
// default server. Primarily used in tests to prevent session name collisions
// and keystroke leaks (e.g. Escape from NudgeSession hitting the user's prefix table).
func NewTmuxWithSocket(socket string) *Tmux {
	return &Tmux{socketName: socket}
}

// run executes a tmux command and returns stdout.
// All commands include -u flag for UTF-8 support regardless of locale settings.
// See: https://github.com/steveyegge/gastown/issues/1219
func (t *Tmux) run(args ...string) (string, error) {
	return t.runContext(context.Background(), args...)
}

func (t *Tmux) commandContext(ctx context.Context, args ...string) *exec.Cmd {
	allArgs := []string{"-u"}
	if t.socketName != "" {
		allArgs = append(allArgs, "-L", t.socketName)
	}
	allArgs = append(allArgs, args...)
	cmd := exec.CommandContext(ctx, "tmux", allArgs...)
	hideConsoleWindow(cmd)
	return cmd
}

func (t *Tmux) runContext(ctx context.Context, args ...string) (string, error) {
	cmd := t.commandContext(ctx, args...)
	if _, ok := ctx.Deadline(); ok {
		cmd.WaitDelay = 100 * time.Millisecond
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", t.wrapError(err, stderr.String(), args)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// wrapError wraps tmux errors with context.
func (t *Tmux) wrapError(err error, stderr string, args []string) error {
	stderr = strings.TrimSpace(stderr)

	// Detect specific error types
	if strings.Contains(stderr, "no server running") ||
		strings.Contains(stderr, "error connecting to") ||
		strings.Contains(stderr, "no current target") ||
		strings.Contains(stderr, "server exited unexpectedly") {
		return ErrNoServer
	}
	if strings.Contains(stderr, "duplicate session") {
		return ErrSessionExists
	}
	if strings.Contains(stderr, "session not found") ||
		strings.Contains(stderr, "can't find session") {
		return ErrSessionNotFound
	}

	if stderr != "" {
		return fmt.Errorf("tmux %s: %s", args[0], stderr)
	}
	return fmt.Errorf("tmux %s: %w", args[0], err)
}

func (t *Tmux) createNewSession(name, workDir string, env map[string]string) error {
	if err := t.ensureNewSessionSocketSafe(); err != nil {
		return err
	}

	args := []string{"new-session", "-d", "-s", name}
	if workDir != "" {
		args = append(args, "-c", workDir)
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, env[k]))
	}
	if _, err := t.run(args...); err != nil {
		return err
	}
	// tmux 3.3+ sets window-size=manual on detached sessions (no client present),
	// which locks the window at 80x24 even after a client attaches. Override to
	// "latest" so the window auto-resizes to the attaching client's terminal size.
	_, _ = t.run("set-option", "-wt", name, "window-size", "latest")
	return nil
}

// NewSession creates a new detached tmux session.
func (t *Tmux) NewSession(name, workDir string) error {
	if err := validateSessionName(name); err != nil {
		return err
	}
	return t.createNewSession(name, workDir, nil)
}

// NewSessionWithCommand creates a new detached tmux session that immediately runs a command.
// Unlike NewSession + SendKeys, this avoids race conditions where the shell isn't ready
// or the command arrives before the shell prompt. The command runs directly as the
// initial process of the pane.
//
// Validates workDir (if non-empty) exists and is a directory. After creation, performs
// a brief health check to catch immediate command failures (binary not found, syntax
// errors, etc.) so callers get an error instead of a silently dead session.
// See: https://github.com/anthropics/gastown/issues/280
func (t *Tmux) NewSessionWithCommand(name, workDir, command string) error {
	if err := validateSessionName(name); err != nil {
		return err
	}
	if workDir != "" {
		info, err := os.Stat(workDir)
		if err != nil {
			return fmt.Errorf("invalid work directory %q: %w", workDir, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("work directory %q is not a directory", workDir)
		}
	}
	if err := validateCommandBinary(command); err != nil {
		return err
	}
	if err := t.ensureNewSessionSocketSafe(); err != nil {
		return err
	}

	// Defense-in-depth: remove CLAUDECODE and agent-identity vars from the
	// tmux server's global environment so new sessions don't inherit them.
	// CLAUDECODE: Claude Code sets this on startup; causes nested-session
	// detection failures if inherited (GH#1666).
	// IdentityEnvVars (GT_ROLE, BD_ACTOR, GIT_AUTHOR_NAME, etc.): the daemon
	// process sets BD_ACTOR=daemon; if the tmux server was started from the
	// daemon's context the global env inherits that value. Polecat sessions
	// then inherit BD_ACTOR=daemon and gt done rejects them with "you are daemon"
	// (gt-xyr). Clearing these here ensures every session gets identity vars
	// only from its own startup command / -e flags, not from a stale global.
	_, _ = t.run("set-environment", "-g", "-u", "CLAUDECODE")
	for _, k := range config.IdentityEnvVars {
		_, _ = t.run("set-environment", "-g", "-u", k)
	}

	// Two-step creation: create session with default shell first, configure
	// remain-on-exit, then replace the shell with the actual command. This
	// eliminates the race between command exit and health check setup.
	if err := t.createNewSession(name, workDir, nil); err != nil {
		return err
	}

	// Enable remain-on-exit BEFORE command runs so we can inspect exit status
	_, _ = t.run("set-option", "-t", name, "remain-on-exit", "on")

	// Replace the initial shell with the actual command.
	// On Windows (psmux), respawn-pane doesn't support passing a command
	// argument, so we use send-keys to type the command into the shell.
	if runtime.GOOS == "windows" {
		if _, err := t.run("send-keys", "-t", name, command, "Enter"); err != nil {
			_ = t.KillSession(name)
			return fmt.Errorf("failed to send command in session %q: %w", name, err)
		}
	} else {
		respawnArgs := []string{"respawn-pane", "-k", "-t", name}
		if workDir != "" {
			respawnArgs = append(respawnArgs, "-c", workDir)
		}
		respawnArgs = append(respawnArgs, command)
		if _, err := t.run(respawnArgs...); err != nil {
			_ = t.KillSession(name)
			return fmt.Errorf("failed to start command in session %q: %w", name, err)
		}
	}

	return t.checkSessionAfterCreate(name, command)
}

// NewSessionWithCommandAndEnv creates a new detached tmux session with environment
// variables set via -e flags. This ensures the initial shell process inherits the
// correct environment from the session, rather than inheriting from the tmux server
// or parent process. The -e flags set session-level environment before the shell
// starts, preventing stale env vars (e.g., GT_ROLE from a parent mayor session)
// from leaking into crew/polecat shells.
//
// The command should still use 'exec env' for WaitForCommand detection compatibility,
// but -e provides defense-in-depth for the initial shell environment.
// Requires tmux >= 3.2.
func (t *Tmux) NewSessionWithCommandAndEnv(name, workDir, command string, env map[string]string) error {
	if err := validateSessionName(name); err != nil {
		return err
	}

	// Kill stale same-named sessions on other sockets to prevent split-brain.
	// This is best-effort: failures are silently ignored.
	t.killSplitBrainSession(name)

	if workDir != "" {
		info, err := os.Stat(workDir)
		if err != nil {
			return fmt.Errorf("invalid work directory %q: %w", workDir, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("work directory %q is not a directory", workDir)
		}
	}
	if err := validateCommandBinary(command); err != nil {
		return err
	}

	// Two-step creation: create session with env vars and default shell, then
	// replace the shell with the actual command after configuring remain-on-exit.
	if err := t.createNewSession(name, workDir, env); err != nil {
		return err
	}

	// Enable remain-on-exit BEFORE command runs so we can inspect exit status
	_, _ = t.run("set-option", "-t", name, "remain-on-exit", "on")

	// Replace the initial shell with the actual command.
	if runtime.GOOS == "windows" {
		if _, err := t.run("send-keys", "-t", name, command, "Enter"); err != nil {
			_ = t.KillSession(name)
			return fmt.Errorf("failed to send command in session %q: %w", name, err)
		}
	} else {
		respawnArgs := []string{"respawn-pane", "-k", "-t", name}
		if workDir != "" {
			respawnArgs = append(respawnArgs, "-c", workDir)
		}
		respawnArgs = append(respawnArgs, command)
		if _, err := t.run(respawnArgs...); err != nil {
			_ = t.KillSession(name)
			return fmt.Errorf("failed to start command in session %q: %w", name, err)
		}
	}

	return t.checkSessionAfterCreate(name, command)
}

// checkSessionAfterCreate verifies that a newly created session's command didn't
// fail immediately (binary not found, syntax error, etc.). Expects remain-on-exit
// to already be enabled on the session. Checks the exit status after a brief delay.
// Only returns an error for non-zero exits (command failures), not clean exits (status 0).
func (t *Tmux) checkSessionAfterCreate(name, command string) error {
	checkPaneDead := func() (bool, error) {
		paneDead, _ := t.run("display-message", "-p", "-t", name, "#{pane_dead}")
		if strings.TrimSpace(paneDead) != "1" {
			return false, nil
		}
		exitStatus, _ := t.run("display-message", "-p", "-t", name, "#{pane_dead_status}")
		status := strings.TrimSpace(exitStatus)
		if status != "" && status != "0" {
			_ = t.KillSession(name)
			return true, fmt.Errorf("session %q: command exited with status %s: %s", name, status, command)
		}
		_ = t.KillSession(name)
		return true, nil
	}

	// First check at 50ms: catches fast failures on lightly-loaded runners.
	time.Sleep(50 * time.Millisecond)
	if dead, err := checkPaneDead(); dead {
		return err
	}

	// Second check at 250ms: catches exec failures on loaded CI runners where
	// process startup takes longer than 50ms. This is the fix for CI getting
	// false negatives on TestNewSessionWithCommand_ExecEnvBadBinary. Normal
	// long-lived sessions (Claude, shell) will still be alive here and return nil.
	time.Sleep(200 * time.Millisecond)
	if dead, err := checkPaneDead(); dead {
		return err
	}

	// Pane is alive — restore default (no need to keep dead sessions around)
	_, _ = t.run("set-option", "-t", name, "remain-on-exit", "off")
	return nil
}

// EnsureSessionFresh ensures a session is available and healthy.
// If the session exists but is a zombie (Claude not running), it kills the session first.
// This prevents "session already exists" errors when trying to restart dead agents.
//
// A session is considered a zombie if:
// - The tmux session exists
// - But Claude (node process) is not running in it
//
// Uses create-first approach to avoid TOCTOU race conditions in multi-agent
// environments where another agent could create the same session between a
// check and create call.
//
// Returns nil if session was created successfully or already exists with a running agent.
func (t *Tmux) EnsureSessionFresh(name, workDir string) error {
	if err := validateSessionName(name); err != nil {
		return err
	}

	// Try to create the session first (atomic — avoids check-then-create race)
	err := t.NewSession(name, workDir)
	if err == nil {
		return nil // Created successfully
	}
	if err != ErrSessionExists {
		return fmt.Errorf("creating session: %w", err)
	}

	// Session already exists — check if it's a zombie
	if t.IsAgentRunning(name) {
		// Session is healthy (agent running) — nothing to do
		return nil
	}

	// Zombie session: tmux alive but agent dead
	// Kill it so we can create a fresh one
	// Use KillSessionWithProcesses to ensure all descendant processes are killed
	if err := t.KillSessionWithProcesses(name); err != nil {
		return fmt.Errorf("killing zombie session: %w", err)
	}

	// Create fresh session (handle race: another agent may have created it
	// between our kill and this create — that's fine, treat as success)
	err = t.NewSession(name, workDir)
	if err == ErrSessionExists {
		return nil
	}
	return err
}

// EnsureSessionFreshWithCommand is like EnsureSessionFresh but creates the
// session with a command as the pane's initial process via NewSessionWithCommand.
// This eliminates the race condition in the EnsureSessionFresh + SendKeys pattern
// where the shell may not be ready to receive keystrokes, resulting in empty
// windows. The command runs as the pane's initial process — no shell involved.
//
// If an existing session has a healthy agent, returns ErrSessionRunning.
func (t *Tmux) EnsureSessionFreshWithCommand(name, workDir, command string) error {
	if err := validateSessionName(name); err != nil {
		return err
	}

	// Check if session exists
	running, err := t.HasSession(name)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if running {
		if t.IsAgentRunning(name) {
			// Session is healthy — don't replace it
			return ErrSessionRunning
		}
		// Zombie session: tmux alive but agent dead — kill it
		if err := t.KillSessionWithProcesses(name); err != nil {
			return fmt.Errorf("killing zombie session: %w", err)
		}
	}

	// Create session with command as the initial process
	return t.NewSessionWithCommand(name, workDir, command)
}

// EnsureSessionFreshWithCommandAndEnv is like EnsureSessionFreshWithCommand but
// also seeds the session's environment via tmux -e flags. The -e flags set
// session-level env BEFORE the shell starts, so the initial pane (and any
// subprocesses the agent spawns, e.g. bd) inherit it. SetEnvironment after
// creation only affects newly spawned panes — not the running pane's
// subprocess tree (gt-neycp).
//
// If an existing session has a healthy agent, returns ErrSessionRunning.
func (t *Tmux) EnsureSessionFreshWithCommandAndEnv(name, workDir, command string, env map[string]string) error {
	if err := validateSessionName(name); err != nil {
		return err
	}

	running, err := t.HasSession(name)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if running {
		if t.IsAgentRunning(name) {
			return ErrSessionRunning
		}
		if err := t.KillSessionWithProcesses(name); err != nil {
			return fmt.Errorf("killing zombie session: %w", err)
		}
	}

	return t.NewSessionWithCommandAndEnv(name, workDir, command, env)
}

// KillSession terminates a tmux session. Idempotent: returns nil if the
// session is already gone or there is no tmux server.
func (t *Tmux) KillSession(name string) (retErr error) {
	defer func() { telemetry.RecordSessionStop(context.Background(), name, retErr) }()
	_, retErr = t.run("kill-session", "-t", name)
	if retErr == ErrSessionNotFound || retErr == ErrNoServer {
		retErr = nil
	}
	return retErr
}

// processKillGracePeriod is how long to wait after SIGTERM before sending SIGKILL.
// 2 seconds gives processes time to clean up gracefully. The previous 100ms was too short
// and caused Claude processes to become orphans when they couldn't shut down in time.
const processKillGracePeriod = 2 * time.Second

// KillSessionWithProcesses explicitly kills all processes in a session before terminating it.
// This prevents orphan processes that survive tmux kill-session due to SIGHUP being ignored.
//
// Process:
// 1. Get the pane's main process PID and its process group ID (PGID)
// 2. Kill the entire process group (catches reparented processes that stayed in the group)
// 3. Find all descendant processes recursively (catches any stragglers)
// 4. Send SIGTERM/SIGKILL to descendants
// 5. Kill the pane process itself
// 6. Kill the tmux session
//
// The process group kill is critical because:
// - pgrep -P only finds direct children (PPID matching)
// - Processes that reparent to init (PID 1) are missed by pgrep
// - But they typically stay in the same process group unless they call setsid()
//
// This ensures Claude processes and all their children are properly terminated.
func (t *Tmux) KillSessionWithProcesses(name string) error {
	// Disarm auto-respawn BEFORE killing anything. The pane-died hook would
	// otherwise respawn the process 3 seconds after we kill it, creating a
	// zombie that fights every kill attempt.
	_ = t.SetRemainOnExit(name, false)
	_, _ = t.run("set-hook", "-t", name, "-u", "pane-died")

	// Get the pane PID
	pid, err := t.GetPanePID(name)
	if err != nil {
		// Session might not exist or server may have already gone away.
		killErr := t.KillSession(name)
		if killErr == nil || killErr == ErrSessionNotFound || killErr == ErrNoServer {
			return nil
		}
		return killErr
	}

	if pid != "" {
		// Walk the process tree for all descendants (catches processes that
		// called setsid() and created their own process groups)
		descendants := getAllDescendants(pid)

		// Build known PID set for group membership verification
		knownPIDs := make(map[string]bool, len(descendants)+1)
		knownPIDs[pid] = true
		for _, d := range descendants {
			knownPIDs[d] = true
		}

		// Find reparented processes from our process group. Instead of killing
		// the entire group blindly with syscall.Kill(-pgid, ...) — which could
		// hit unrelated processes sharing the same PGID — we enumerate group
		// members and only include those reparented to init (PPID == 1), which
		// indicates they were likely children in our tree that outlived their parent.
		pgid := getProcessGroupID(pid)
		if pgid != "" && pgid != "0" && pgid != "1" {
			reparented := collectReparentedGroupMembers(pgid, knownPIDs)
			descendants = append(descendants, reparented...)
		}

		// Send SIGTERM to all descendants (deepest first to avoid orphaning)
		for _, dpid := range descendants {
			_ = exec.Command("kill", "-TERM", dpid).Run()
		}

		// Wait for graceful shutdown (2s gives processes time to clean up)
		time.Sleep(processKillGracePeriod)

		// Send SIGKILL to any remaining descendants
		for _, dpid := range descendants {
			_ = exec.Command("kill", "-KILL", dpid).Run()
		}

		// Kill the pane process itself (may have called setsid() and detached)
		_ = exec.Command("kill", "-TERM", pid).Run()
		time.Sleep(processKillGracePeriod)
		_ = exec.Command("kill", "-KILL", pid).Run()
	}

	// Kill the tmux session
	// Ignore missing/dead-server errors - killing the pane process may have
	// already caused tmux to destroy the session automatically.
	err = t.KillSession(name)
	if err == ErrSessionNotFound || err == ErrNoServer {
		return nil
	}
	return err
}

// KillSessionWithProcessesExcluding is like KillSessionWithProcesses but excludes
// specified PIDs from being killed. This is essential for self-kill scenarios where
// the calling process (e.g., gt done) is running inside the session it's terminating.
// Without exclusion, the caller would be killed before completing the cleanup.
func (t *Tmux) KillSessionWithProcessesExcluding(name string, excludePIDs []string) error {
	// Disarm auto-respawn BEFORE killing anything (same as KillSessionWithProcesses).
	_ = t.SetRemainOnExit(name, false)
	_, _ = t.run("set-hook", "-t", name, "-u", "pane-died")

	// Build exclusion set for O(1) lookup
	exclude := make(map[string]bool)
	for _, pid := range excludePIDs {
		exclude[pid] = true
	}

	// Get the pane PID
	pid, err := t.GetPanePID(name)
	if err != nil {
		// Session might not exist or server may have already gone away.
		killErr := t.KillSession(name)
		if killErr == nil || killErr == ErrSessionNotFound || killErr == ErrNoServer {
			return nil
		}
		return killErr
	}

	if pid != "" {
		// Get the process group ID
		pgid := getProcessGroupID(pid)

		// Collect all PIDs to kill (from multiple sources)
		toKill := make(map[string]bool)

		// 1. Get all descendant PIDs recursively (catches processes that called setsid())
		descendants := getAllDescendants(pid)

		// Build known PID set for group membership verification
		knownPIDs := make(map[string]bool, len(descendants)+1)
		knownPIDs[pid] = true
		for _, dpid := range descendants {
			if !exclude[dpid] {
				toKill[dpid] = true
			}
			knownPIDs[dpid] = true
		}

		// 2. Get verified process group members (only reparented-to-init processes).
		// Instead of adding ALL group members — which could include unrelated
		// processes sharing the same PGID — we only add those that were reparented
		// to init (PPID == 1), indicating they were likely children in our tree.
		if pgid != "" && pgid != "0" && pgid != "1" {
			for _, member := range collectReparentedGroupMembers(pgid, knownPIDs) {
				if !exclude[member] {
					toKill[member] = true
				}
			}
		}

		// Convert to slice for iteration
		var killList []string
		for p := range toKill {
			killList = append(killList, p)
		}

		// Send SIGTERM to all non-excluded processes
		for _, dpid := range killList {
			_ = exec.Command("kill", "-TERM", dpid).Run()
		}

		// Wait for graceful shutdown (2s gives processes time to clean up)
		time.Sleep(processKillGracePeriod)

		// Send SIGKILL to any remaining non-excluded processes
		for _, dpid := range killList {
			_ = exec.Command("kill", "-KILL", dpid).Run()
		}

		// Kill the pane process itself (may have called setsid() and detached)
		// Only if not excluded
		if !exclude[pid] {
			_ = exec.Command("kill", "-TERM", pid).Run()
			time.Sleep(processKillGracePeriod)
			_ = exec.Command("kill", "-KILL", pid).Run()
		}
	}

	// Kill the tmux session - this will terminate the excluded process too.
	// Ignore missing/dead-server errors - if we killed all non-excluded
	// processes, tmux may have already destroyed the session automatically.
	err = t.KillSession(name)
	if err == ErrSessionNotFound || err == ErrNoServer {
		return nil
	}
	return err
}

// killSplitBrainSession kills a same-named session on the "default" tmux socket
// if this Tmux instance targets a different socket. This prevents split-brain
// where stale sessions on the wrong socket shadow the real ones, causing nudge
// and other session-discovery commands to fail.
//
// Best-effort: all errors are silently ignored. The stale session may not exist,
// the default server may not be running, etc. — none of these should block
// session creation on the correct socket.
func (t *Tmux) killSplitBrainSession(name string) {
	if t.socketName == "" || t.socketName == "default" || t.socketName == noTownSocket {
		return // Already on default or no town context — nothing to clean up
	}
	other := NewTmuxWithSocket("default")
	if running, _ := other.HasSession(name); running {
		_ = other.KillSessionWithProcesses(name)
	}
}

// collectReparentedGroupMembers returns process group members that have been
// reparented to init (PPID == 1) but are not in the known descendant set.
// These are processes that were likely children in our tree but outlived their
// parent and got reparented to init while keeping the original PGID.
//
// This is safer than killing the entire process group blindly with
// syscall.Kill(-pgid, ...), which could hit unrelated processes if the PGID
// is shared or has been reused after the group leader exited.
func collectReparentedGroupMembers(pgid string, knownPIDs map[string]bool) []string {
	members := getProcessGroupMembers(pgid)
	var reparented []string
	for _, member := range members {
		if knownPIDs[member] {
			continue // Already in descendant list, will be handled there
		}
		// Check if reparented to init — probably was our child
		ppid := getParentPID(member)
		if ppid == "1" {
			reparented = append(reparented, member)
		}
		// Otherwise skip — this process is not in our tree and not reparented,
		// so it's likely unrelated and should not be killed
	}
	return reparented
}

// getAllDescendants recursively finds all descendant PIDs of a process.
// Returns PIDs in deepest-first order so killing them doesn't orphan grandchildren.
func getAllDescendants(pid string) []string {
	var result []string

	// Get direct children using pgrep
	out, err := exec.Command("pgrep", "-P", pid).Output()
	if err != nil {
		return result
	}

	children := strings.Fields(strings.TrimSpace(string(out)))
	for _, child := range children {
		// First add grandchildren (recursively) - deepest first
		result = append(result, getAllDescendants(child)...)
		// Then add this child
		result = append(result, child)
	}

	return result
}

// KillPaneProcesses explicitly kills all processes associated with a tmux pane.
// This prevents orphan processes that survive pane respawn due to SIGHUP being ignored.
//
// Process:
// 1. Get the pane's main process PID and its process group ID (PGID)
// 2. Kill the entire process group (catches reparented processes)
// 3. Find all descendant processes recursively (catches any stragglers)
// 4. Send SIGTERM/SIGKILL to descendants
// 5. Kill the pane process itself
//
// This ensures Claude processes and all their children are properly terminated
// before respawning the pane.
func (t *Tmux) KillPaneProcesses(pane string) error {
	// Get the pane PID
	pid, err := t.GetPanePID(pane)
	if err != nil {
		return fmt.Errorf("getting pane PID: %w", err)
	}

	if pid == "" {
		return fmt.Errorf("pane PID is empty")
	}

	// Walk the process tree for all descendants (catches processes that
	// called setsid() and created their own process groups)
	descendants := getAllDescendants(pid)

	// Build known PID set for group membership verification
	knownPIDs := make(map[string]bool, len(descendants)+1)
	knownPIDs[pid] = true
	for _, d := range descendants {
		knownPIDs[d] = true
	}

	// Find reparented processes from our process group. Instead of killing
	// the entire group blindly with syscall.Kill(-pgid, ...) — which could
	// hit unrelated processes sharing the same PGID — we enumerate group
	// members and only include those reparented to init (PPID == 1).
	pgid := getProcessGroupID(pid)
	if pgid != "" && pgid != "0" && pgid != "1" {
		reparented := collectReparentedGroupMembers(pgid, knownPIDs)
		descendants = append(descendants, reparented...)
	}

	// Send SIGTERM to all descendants (deepest first to avoid orphaning)
	for _, dpid := range descendants {
		_ = exec.Command("kill", "-TERM", dpid).Run()
	}

	// Wait for graceful shutdown (2s gives processes time to clean up)
	time.Sleep(processKillGracePeriod)

	// Send SIGKILL to any remaining descendants
	for _, dpid := range descendants {
		_ = exec.Command("kill", "-KILL", dpid).Run()
	}

	// Kill the pane process itself (may have called setsid() and detached,
	// or may have no children like Claude Code)
	_ = exec.Command("kill", "-TERM", pid).Run()
	time.Sleep(processKillGracePeriod)
	_ = exec.Command("kill", "-KILL", pid).Run()

	return nil
}

// KillPaneProcessesExcluding is like KillPaneProcesses but excludes specified PIDs
// from being killed. This is essential for self-handoff scenarios where the calling
// process (e.g., gt handoff running inside Claude Code) needs to survive long enough
// to call RespawnPane. Without exclusion, the caller would be killed before completing.
//
// The excluded PIDs should include the calling process and any ancestors that must
// survive. After this function returns, RespawnPane's -k flag will send SIGHUP to
// clean up the remaining processes.
func (t *Tmux) KillPaneProcessesExcluding(pane string, excludePIDs []string) error {
	// Build exclusion set for O(1) lookup
	exclude := make(map[string]bool)
	for _, pid := range excludePIDs {
		exclude[pid] = true
	}

	// Get the pane PID
	pid, err := t.GetPanePID(pane)
	if err != nil {
		return fmt.Errorf("getting pane PID: %w", err)
	}

	if pid == "" {
		return fmt.Errorf("pane PID is empty")
	}

	// Get all descendant PIDs recursively (returns deepest-first order)
	descendants := getAllDescendants(pid)

	// Filter out excluded PIDs
	var filtered []string
	for _, dpid := range descendants {
		if !exclude[dpid] {
			filtered = append(filtered, dpid)
		}
	}

	// Send SIGTERM to all non-excluded descendants (deepest first to avoid orphaning)
	for _, dpid := range filtered {
		_ = exec.Command("kill", "-TERM", dpid).Run()
	}

	// Wait for graceful shutdown
	time.Sleep(100 * time.Millisecond)

	// Send SIGKILL to any remaining non-excluded descendants
	for _, dpid := range filtered {
		_ = exec.Command("kill", "-KILL", dpid).Run()
	}

	// Kill the pane process itself only if not excluded
	if !exclude[pid] {
		_ = exec.Command("kill", "-TERM", pid).Run()
		time.Sleep(100 * time.Millisecond)
		_ = exec.Command("kill", "-KILL", pid).Run()
	}

	return nil
}

// ServerPID returns the PID of the tmux server process.
// Returns 0 if the server is not running or the PID cannot be determined.
func (t *Tmux) ServerPID() int {
	out, err := t.run("display-message", "-p", "#{pid}")
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(out))
	return pid
}

// KillServer terminates the entire tmux server and all sessions.
func (t *Tmux) KillServer() error {
	_, err := t.run("kill-server")
	if errors.Is(err, ErrNoServer) {
		return nil // Already dead
	}
	return err
}

// SetExitEmpty controls the tmux exit-empty server option.
// When on (default), the server exits when there are no sessions.
// When off, the server stays running even with no sessions.
// This is useful during shutdown to prevent the server from exiting
// when all Gas Town sessions are killed but the user has no other sessions.
func (t *Tmux) SetExitEmpty(on bool) error {
	value := "on"
	if !on {
		value = "off"
	}
	_, err := t.run("set-option", "-g", "exit-empty", value)
	if errors.Is(err, ErrNoServer) {
		return nil // No server to configure
	}
	return err
}

// IsAvailable checks if tmux is installed and can be invoked.
func (t *Tmux) IsAvailable() bool {
	cmd := exec.Command("tmux", "-V")
	hideConsoleWindow(cmd)
	return cmd.Run() == nil
}

// HasSession checks if a session exists (exact match).
// Uses "=" prefix for exact matching, preventing prefix matches
// (e.g., "gt-deacon-boot" won't match when checking for "gt-deacon").
func (t *Tmux) HasSession(name string) (bool, error) {
	// psmux (Windows tmux alternative) doesn't support the "=" exact-match
	// prefix for session targets. Use the bare name on Windows.
	target := "=" + name
	if runtime.GOOS == "windows" {
		target = name
	}
	_, err := t.run("has-session", "-t", target)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) || errors.Is(err, ErrNoServer) {
			return false, nil
		}
		// psmux (Windows) returns exit code 1 with empty stderr, bypassing
		// wrapError's string matching. Fall back to treating any error as
		// "not found" on Windows only.
		if runtime.GOOS == "windows" {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ListSessions returns all session names.
func (t *Tmux) ListSessions() ([]string, error) {
	out, err := t.run("list-sessions", "-F", "#{session_name}")
	if err != nil {
		if errors.Is(err, ErrNoServer) {
			return nil, nil // No server = no sessions
		}
		return nil, err
	}

	if out == "" {
		return nil, nil
	}

	var sessions []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// psmux ignores -F format and returns "name: N windows (created ...)"
		// Extract just the session name before the colon.
		if idx := strings.Index(line, ": "); idx > 0 {
			line = line[:idx]
		}
		sessions = append(sessions, line)
	}
	return sessions, nil
}

// SessionSet provides O(1) session existence checks by caching session names.
// Use this when you need to check multiple sessions to avoid N+1 subprocess calls.
type SessionSet struct {
	sessions map[string]struct{}
}

// NewSessionSet creates a SessionSet from a list of session names.
// This is useful for testing or when session names are known from another source.
func NewSessionSet(names []string) *SessionSet {
	set := &SessionSet{
		sessions: make(map[string]struct{}, len(names)),
	}
	for _, name := range names {
		set.sessions[name] = struct{}{}
	}
	return set
}

// GetSessionSet returns a SessionSet containing all current sessions.
// Call this once at the start of an operation, then use Has() for O(1) checks.
// This replaces multiple HasSession() calls with a single ListSessions() call.
//
// Builds the map directly from tmux output to avoid intermediate slice allocation.
func (t *Tmux) GetSessionSet() (*SessionSet, error) {
	out, err := t.run("list-sessions", "-F", "#{session_name}")
	if err != nil {
		if errors.Is(err, ErrNoServer) {
			return &SessionSet{sessions: make(map[string]struct{})}, nil
		}
		return nil, err
	}

	// Count newlines to pre-size map (avoids rehashing during insertion)
	count := strings.Count(out, "\n") + 1
	set := &SessionSet{
		sessions: make(map[string]struct{}, count),
	}

	// Parse directly without intermediate slice allocation
	for len(out) > 0 {
		idx := strings.IndexByte(out, '\n')
		var line string
		if idx >= 0 {
			line = out[:idx]
			out = out[idx+1:]
		} else {
			line = out
			out = ""
		}
		if line != "" {
			set.sessions[line] = struct{}{}
		}
	}
	return set, nil
}

// Has returns true if the session exists in the set.
// This is an O(1) lookup - no subprocess is spawned.
func (s *SessionSet) Has(name string) bool {
	if s == nil {
		return false
	}
	_, ok := s.sessions[name]
	return ok
}

// Names returns all session names in the set.
func (s *SessionSet) Names() []string {
	if s == nil || len(s.sessions) == 0 {
		return nil
	}
	names := make([]string, 0, len(s.sessions))
	for name := range s.sessions {
		names = append(names, name)
	}
	return names
}

// ListSessionIDs returns a map of session name to session ID.
// Session IDs are in the format "$N" where N is a number.
func (t *Tmux) ListSessionIDs() (map[string]string, error) {
	out, err := t.run("list-sessions", "-F", "#{session_name}:#{session_id}")
	if err != nil {
		if errors.Is(err, ErrNoServer) {
			return nil, nil // No server = no sessions
		}
		return nil, err
	}

	if out == "" {
		return nil, nil
	}

	result := make(map[string]string)
	skipped := 0
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		// Parse "name:$id" format
		idx := strings.Index(line, ":")
		if idx > 0 && idx < len(line)-1 {
			name := line[:idx]
			id := line[idx+1:]
			result[name] = id
		} else {
			skipped++
		}
	}
	// Note: skipped lines are silently ignored for backward compatibility
	_ = skipped
	return result, nil
}

// SendKeys sends keystrokes to a session and presses Enter.
// Always sends Enter as a separate command for reliability.
// Uses a debounce delay between paste and Enter to ensure paste completes.
func (t *Tmux) SendKeys(session, keys string) error {
	return t.SendKeysDebounced(session, keys, constants.DefaultDebounceMs) // 100ms default debounce
}

// SendKeysDebounced sends keystrokes with a configurable delay before Enter.
// The debounceMs parameter controls how long to wait after paste before sending Enter.
// This prevents race conditions where Enter arrives before paste is processed.
func (t *Tmux) SendKeysDebounced(session, keys string, debounceMs int) (retErr error) {
	defer func() { telemetry.RecordPromptSend(context.Background(), session, keys, debounceMs, retErr) }()
	// Send text using literal mode (-l) to handle special chars
	if _, err := t.run("send-keys", "-t", session, "-l", keys); err != nil {
		return err
	}
	// Wait for paste to be processed
	if debounceMs > 0 {
		time.Sleep(time.Duration(debounceMs) * time.Millisecond)
	}
	// Send Enter separately - more reliable than appending to send-keys
	_, retErr = t.run("send-keys", "-t", session, "Enter")
	return retErr
}

// SendKeysRaw sends keystrokes without adding Enter.
func (t *Tmux) SendKeysRaw(session, keys string) error {
	_, err := t.run("send-keys", "-t", session, keys)
	return err
}

// SendKeysReplace sends keystrokes, clearing any pending input first.
// This is useful for "replaceable" notifications where only the latest matters.
// Uses Ctrl-U to clear the input line before sending the new message.
// The delay parameter controls how long to wait after clearing before sending (ms).
func (t *Tmux) SendKeysReplace(session, keys string, clearDelayMs int) error {
	// Send Ctrl-U to clear any pending input on the line
	if _, err := t.run("send-keys", "-t", session, "C-u"); err != nil {
		return err
	}

	// Small delay to let the clear take effect
	if clearDelayMs > 0 {
		time.Sleep(time.Duration(clearDelayMs) * time.Millisecond)
	}

	// Now send the actual message
	return t.SendKeys(session, keys)
}

// SendKeysDelayed sends keystrokes after a delay (in milliseconds).
// Useful for waiting for a process to be ready before sending input.
func (t *Tmux) SendKeysDelayed(session, keys string, delayMs int) error {
	time.Sleep(time.Duration(delayMs) * time.Millisecond)
	return t.SendKeys(session, keys)
}

// SendKeysDelayedDebounced sends keystrokes after a pre-delay, with a custom debounce before Enter.
// Use this when sending input to a process that needs time to initialize AND the message
// needs extra time between paste and Enter (e.g., Claude prompt injection).
// preDelayMs: time to wait before sending text (for process readiness)
// debounceMs: time to wait between text paste and Enter key (for paste completion)
func (t *Tmux) SendKeysDelayedDebounced(session, keys string, preDelayMs, debounceMs int) error {
	if preDelayMs > 0 {
		time.Sleep(time.Duration(preDelayMs) * time.Millisecond)
	}
	return t.SendKeysDebounced(session, keys, debounceMs)
}

// getSessionNudgeSem returns the channel semaphore for serializing nudges to a session.
// Creates a new semaphore if one doesn't exist for this session.
// The semaphore is a buffered channel of size 1 — send to acquire, receive to release.
func getSessionNudgeSem(session string) chan struct{} {
	sem := make(chan struct{}, 1)
	actual, _ := sessionNudgeLocks.LoadOrStore(session, sem)
	return actual.(chan struct{})
}

// acquireNudgeLock attempts to acquire the per-session nudge lock with a timeout.
// Returns true if the lock was acquired, false if the timeout expired.
func acquireNudgeLock(session string, timeout time.Duration) bool {
	sem := getSessionNudgeSem(session)
	select {
	case sem <- struct{}{}:
		return true
	case <-time.After(timeout):
		return false
	}
}

// releaseNudgeLock releases the per-session nudge lock.
func releaseNudgeLock(session string) {
	sem := getSessionNudgeSem(session)
	select {
	case <-sem:
	default:
		// Lock wasn't held — shouldn't happen, but don't block
	}
}

// nudgeFlockPath returns the filesystem lock path for cross-process nudge serialization.
// Lock files live alongside the nudge queue directory for self-documentation and cleanup.
func nudgeFlockPath(townRoot, session string) string {
	safe := strings.ReplaceAll(session, "/", "_")
	return filepath.Join(townRoot, constants.DirRuntime, "nudge_queue", safe, ".lock")
}

// IsSessionAttached returns true if the session has any clients attached.
func (t *Tmux) IsSessionAttached(target string) bool {
	attached, err := t.run("display-message", "-t", target, "-p", "#{session_attached}")
	return err == nil && attached == "1"
}

// WakePane triggers a SIGWINCH in a pane by resizing it slightly then restoring.
// This wakes up Claude Code's event loop by simulating a terminal resize.
//
// When Claude runs in a detached tmux session, its TUI library may not process
// stdin until a terminal event occurs. Attaching triggers SIGWINCH which wakes
// the event loop. This function simulates that by doing a resize dance.
//
// Note: This always performs the resize. Use WakePaneIfDetached to skip
// attached sessions where the wake is unnecessary.
func (t *Tmux) WakePane(target string) {
	// Use resize-window to trigger SIGWINCH. resize-pane doesn't work on
	// single-pane sessions because the pane already fills the window.
	// resize-window changes the window dimensions, which sends SIGWINCH to
	// all processes in all panes of that window.
	//
	// Get current width, bump +1, then restore. This avoids permanent size
	// changes even if the second resize fails.
	widthStr, err := t.run("display-message", "-p", "-t", target, "#{window_width}")
	if err != nil {
		return // session may be dead
	}
	width := strings.TrimSpace(widthStr)
	if width == "" {
		return
	}
	// Parse width to compute +1
	var w int
	if _, err := fmt.Sscanf(width, "%d", &w); err != nil || w < 1 {
		return
	}
	_, _ = t.run("resize-window", "-t", target, "-x", fmt.Sprintf("%d", w+1))
	time.Sleep(50 * time.Millisecond)
	_, _ = t.run("resize-window", "-t", target, "-x", width)

	// Reset window-size to "latest" after the resize dance. tmux automatically
	// sets window-size to "manual" whenever resize-window is called, which
	// permanently locks the window at the current dimensions. This prevents
	// the window from auto-sizing to a client when a human later attaches,
	// causing dots around the edges as if another smaller client is viewing.
	_, _ = t.run("set-option", "-w", "-t", target, "window-size", "latest")
}

// WakePaneIfDetached triggers a SIGWINCH only if the session is detached.
// This avoids unnecessary latency on attached sessions where Claude is
// already processing terminal events.
func (t *Tmux) WakePaneIfDetached(target string) {
	if t.IsSessionAttached(target) {
		return
	}
	t.WakePane(target)
}

// isTransientSendKeysError returns true if the error from tmux send-keys is
// transient and safe to retry. "not in a mode" occurs when the target pane's
// TUI hasn't initialized its input handling yet (common during cold startup).
func isTransientSendKeysError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not in a mode")
}

// sanitizeNudgeMessage removes control characters that corrupt tmux send-keys
// delivery. ESC (0x1b) triggers terminal escape sequences, CR (0x0d) acts as
// premature Enter, BS (0x08) deletes characters. TAB is replaced with a space
// to avoid triggering shell completion. Printable characters (including quotes,
// backticks, and Unicode) are preserved.
func sanitizeNudgeMessage(msg string) string {
	var b strings.Builder
	b.Grow(len(msg))
	for _, r := range msg {
		switch {
		case r == '\t': // TAB → space (avoid triggering completion)
			b.WriteRune(' ')
		case r == '\n': // preserve newlines (send-keys -l treats as Enter, known limitation)
			b.WriteRune(r)
		case r < 0x20: // strip all other control chars (ESC, CR, BS, etc.)
			continue
		case r == 0x7f: // DEL
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isInRewindMode checks if a tmux target is displaying Claude Code's Rewind
// conversation history browser. When Rewind is active, the session ignores
// typed text and only responds to Enter (accept rewind) or Escape (cancel).
// This can happen when a stray or deliberate Escape keystroke combines with
// a previous Escape to form the double-Escape sequence that activates Rewind.
//
// Detection is based on pane content analysis. Returns false on any error
// (defensive — don't block nudge delivery on detection failure).
func (t *Tmux) isInRewindMode(target string) bool {
	content, err := t.CapturePane(target, 15)
	if err != nil {
		return false
	}
	return containsRewindIndicators(content)
}

// containsRewindIndicators checks pane content for Claude Code Rewind menu
// patterns. The Rewind UI takes over the terminal and shows distinctive
// action prompts (Enter to act, Esc to cancel/exit). We require multiple
// co-occurring indicators to avoid false positives from conversation text.
func containsRewindIndicators(content string) bool {
	lower := strings.ToLower(content)

	// Primary: "rewind" appears alongside both Enter and Esc action prompts.
	if strings.Contains(lower, "rewind") {
		if strings.Contains(lower, "enter") && strings.Contains(lower, "esc") {
			return true
		}
	}

	// Secondary: specific action prompt pairs characteristic of the Rewind UI.
	rewindActionPairs := [][2]string{
		{"enter to continue", "esc to exit"},
		{"enter to accept", "esc to cancel"},
		{"enter to select", "esc to go back"},
		{"enter to select", "esc to cancel"},
	}
	for _, pair := range rewindActionPairs {
		if strings.Contains(lower, pair[0]) && strings.Contains(lower, pair[1]) {
			return true
		}
	}

	return false
}

// dismissRewindMode sends Escape to cancel Claude Code's Rewind menu,
// then waits briefly for the UI to return to normal.
func (t *Tmux) dismissRewindMode(target string) {
	_, _ = t.run("send-keys", "-t", target, "Escape")
	time.Sleep(300 * time.Millisecond)
}

// sendEnterVerified sends Enter to a tmux target and verifies it was processed
// by checking that the pane content changes. Under load, tmux may buffer
// keystrokes, causing Enter to race with text delivery — Enter arrives while
// tmux is still processing text/Escape and gets treated as part of the text
// stream rather than a separate submit action.
//
// After sending Enter, polls the pane content with exponential backoff. If the
// content hasn't changed (Enter wasn't processed), retries the Enter keystroke.
// Max 3 retries before returning an error.
//
// Falls back to best-effort (no verification) if pane capture fails.
func (t *Tmux) sendEnterVerified(target string) error {
	const (
		maxRetries     = 3
		initialBackoff = 500 * time.Millisecond
		verifyLines    = 5 // capture last N lines for comparison
	)

	// Snapshot pane content before Enter so we can detect processing.
	preSnapshot, preErr := t.CapturePane(target, verifyLines)

	// Send Enter
	if _, err := t.run("send-keys", "-t", target, "Enter"); err != nil {
		return fmt.Errorf("send Enter: %w", err)
	}

	// If we can't snapshot, fall back to unverified delivery (old behavior).
	if preErr != nil {
		return nil
	}

	backoff := initialBackoff
	for retry := 0; retry < maxRetries; retry++ {
		time.Sleep(backoff)

		postSnapshot, err := t.CapturePane(target, verifyLines)
		if err != nil {
			// Can't verify — assume success.
			return nil
		}

		if postSnapshot != preSnapshot {
			// Content changed — Enter was processed.
			return nil
		}

		// Content unchanged — Enter may not have been processed. Retry.
		if _, err := t.run("send-keys", "-t", target, "Enter"); err != nil {
			return fmt.Errorf("send Enter (retry %d): %w", retry+1, err)
		}

		// Exponential backoff: 500ms → 1000ms → 2000ms
		backoff *= 2
	}

	// Final verification after last retry.
	time.Sleep(500 * time.Millisecond)
	postSnapshot, err := t.CapturePane(target, verifyLines)
	if err != nil || postSnapshot != preSnapshot {
		return nil // Can't verify or content changed — consider success.
	}

	return fmt.Errorf("nudge Enter not processed after %d retries: pane content unchanged", maxRetries)
}

// adaptiveTextDelay returns the post-text-delivery delay for a message.
// Base 500ms + 25ms per chunk beyond the first, capped at 2s.
// Longer messages need more time for tmux to process all chunks under load.
func adaptiveTextDelay(messageLen int) time.Duration {
	numChunks := (messageLen + sendKeysChunkSize - 1) / sendKeysChunkSize
	delay := 500*time.Millisecond + time.Duration(max(0, numChunks-1))*25*time.Millisecond
	if delay > 2*time.Second {
		delay = 2 * time.Second
	}
	return delay
}

// sendMessageToTarget sends a sanitized message to a tmux target. For small
// messages (< sendKeysChunkSize), uses send-keys -l. For larger messages,
// sends in chunks with delays to avoid overwhelming the TTY input buffer.
//
// NOTE: The Linux TTY canonical mode buffer is 4096 bytes. Messages longer
// than ~4000 bytes may be truncated by the kernel's line discipline when
// delivered to programs using line-buffered input (readline, read, etc.).
// This is a fundamental kernel limit, not a tmux limitation. Programs reading
// raw stdin (like Claude Code's TUI) are not affected.
const sendKeysChunkSize = 512

func (t *Tmux) sendMessageToTarget(target, text string) error {
	if len(text) <= sendKeysChunkSize {
		return t.sendKeysLiteralWithRetry(target, text, constants.NudgeReadyTimeout)
	}
	// Send in chunks to avoid tmux send-keys argument length limits.
	// Each chunk is sent with a small delay to let the terminal process it.
	for i := 0; i < len(text); i += sendKeysChunkSize {
		end := i + sendKeysChunkSize
		if end > len(text) {
			end = len(text)
		}
		chunk := text[i:end]
		if i == 0 {
			// First chunk uses retry logic for startup race
			if err := t.sendKeysLiteralWithRetry(target, chunk, constants.NudgeReadyTimeout); err != nil {
				return err
			}
		} else {
			if _, err := t.run("send-keys", "-t", target, "-l", chunk); err != nil {
				return err
			}
		}
		// Small delay between chunks to let the terminal process
		if end < len(text) {
			time.Sleep(10 * time.Millisecond)
		}
	}
	return nil
}

// sendKeysLiteralWithRetry sends literal text to a tmux target, retrying on
// transient errors (e.g., "not in a mode" during agent TUI startup).
// This is the core retry loop used by both NudgeSession and NudgePane.
//
// Returns nil on success, or the last error after all retries are exhausted.
// Non-transient errors (session not found, no server) fail immediately.
//
// Related upstream issues:
//   - #1216: Nudge delivery reliability (input collision — NOT addressed here)
//   - #1275: Graceful nudge delivery (work interruption — NOT addressed here)
//
// This function ONLY addresses the startup race where the agent TUI hasn't
// initialized yet, causing tmux send-keys to fail with "not in a mode".
func (t *Tmux) sendKeysLiteralWithRetry(target, text string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	interval := constants.NudgeRetryInterval
	var lastErr error

	for time.Now().Before(deadline) {
		_, err := t.run("send-keys", "-t", target, "-l", text)
		if err == nil {
			return nil
		}
		if !isTransientSendKeysError(err) {
			return err // non-transient (session gone, no server) — fail fast
		}
		lastErr = err
		// Clamp sleep to remaining time so we don't overshoot the deadline.
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		sleep := interval
		if sleep > remaining {
			sleep = remaining
		}
		time.Sleep(sleep)
		// Grow interval by 1.5x, capped at 2s to stay responsive.
		// 500ms → 750ms → 1125ms → 1687ms → 2s (capped)
		interval = interval * 3 / 2
		if interval > 2*time.Second {
			interval = 2 * time.Second
		}
	}
	return fmt.Errorf("agent not ready for input after %s: %w", timeout, lastErr)
}

// NudgeSession sends a message to a Claude Code session reliably.
// This is the canonical way to send messages to Claude sessions.
// Uses: literal mode + 500ms debounce + ESC (for vim mode) + separate Enter.
// After sending, triggers SIGWINCH to wake Claude in detached sessions.
// Verification is the Witness's job (AI), not this function.
//
// If the agent TUI hasn't initialized yet (cold startup), retries with backoff
// up to NudgeReadyTimeout before giving up. See sendKeysLiteralWithRetry.
//
// IMPORTANT: Nudges to the same session are serialized to prevent interleaving.
// If multiple goroutines try to nudge the same session concurrently, they will
// queue up and execute one at a time. This prevents garbled input when
// SessionStart hooks and nudges arrive simultaneously.
func (t *Tmux) NudgeSession(session, message string) error {
	return t.NudgeSessionWithOpts(session, message, NudgeOpts{})
}

// NudgeOpts controls optional behavior for nudge delivery.
type NudgeOpts struct {
	// SkipEscape omits the Escape keystroke (step 5) and the 600ms readline
	// timeout (step 6) from the delivery protocol. Set this for agents where
	// Escape cancels in-flight generation (e.g., Gemini CLI) rather than
	// harmlessly exiting vim INSERT mode.
	SkipEscape bool

	// TownRoot, if set, enables flock-based cross-process serialization of
	// nudge delivery. Each `gt nudge` CLI invocation is a separate OS process,
	// so the in-process channel semaphore alone cannot prevent interleaving.
	// When TownRoot is provided, a filesystem lock is acquired at
	// <townRoot>/.runtime/nudge_queue/<session>/.lock before delivery.
	// When empty, only in-process locking is used (backward-compatible).
	TownRoot string
}

// canonicalPaneTarget converts a pane identifier like "%23" into an explicit
// tmux session:window.pane target. If the pane is stale or resolves outside the
// expected session, fall back to the session's first pane instead of the active
// pane that a bare session target would select.
func (t *Tmux) canonicalPaneTarget(session, pane string) string {
	fallback := session + ":0.0"
	if pane == "" {
		return fallback
	}

	expectedSession := session
	if out, err := t.run("display-message", "-t", session, "-p", "#{session_name}"); err == nil {
		if resolved := strings.TrimSpace(out); resolved != "" {
			expectedSession = resolved
		}
	}

	out, err := t.run("display-message", "-t", pane, "-p", "#{session_name}\t#{window_index}\t#{pane_index}")
	if err != nil {
		return fallback
	}
	if target, ok := canonicalPaneTargetFromDisplay(expectedSession, out); ok {
		return target
	}
	return fallback
}

func canonicalPaneTargetFromDisplay(expectedSession, out string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(out), "\t")
	if len(parts) != 3 {
		return "", false
	}

	session := strings.TrimSpace(parts[0])
	window := strings.TrimSpace(parts[1])
	pane := strings.TrimSpace(parts[2])
	if session != expectedSession || !isTmuxIndex(window) || !isTmuxIndex(pane) {
		return "", false
	}
	return fmt.Sprintf("%s:%s.%s", session, window, pane), true
}

func isTmuxIndex(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(value)
	return err == nil && n >= 0
}

// NudgeSessionWithOpts is like NudgeSession but accepts delivery options.
// See NudgeOpts for available options.
func (t *Tmux) NudgeSessionWithOpts(session, message string, opts NudgeOpts) error {
	// Cross-process lock: serialize nudges across OS processes via flock(2).
	// Each `gt nudge` CLI invocation is a separate process, so the in-process
	// channel semaphore below provides no cross-process protection. Without
	// this, concurrent nudges interleave send-keys/Enter and produce garbled
	// or empty input. (GH#gt-ukl8)
	if opts.TownRoot != "" {
		lockPath := nudgeFlockPath(opts.TownRoot, session)
		unlock, err := acquireFlockLock(lockPath, nudgeLockTimeout)
		if err != nil {
			return fmt.Errorf("cross-process nudge lock for session %q: %w", session, err)
		}
		defer unlock()
	}

	// In-process lock: serialize nudges within a single process (goroutine fast path).
	if !acquireNudgeLock(session, nudgeLockTimeout) {
		return fmt.Errorf("nudge lock timeout for session %q: previous nudge may be hung", session)
	}
	defer releaseNudgeLock(session)

	// Resolve the correct target: in multi-pane sessions, find the pane
	// running the agent rather than sending to the focused pane.
	target := session + ":0.0"
	if agentPane, err := t.FindAgentPane(session); err == nil && agentPane != "" {
		target = t.canonicalPaneTarget(session, agentPane)
	}

	// 0. Pre-delivery: dismiss Rewind menu if the session is stuck in it.
	// A previous nudge or user action may have triggered Claude Code's
	// double-Escape Rewind UI, which captures all input. Dismiss it first
	// so the nudge can be delivered normally. (GH#gt-8el)
	if t.isInRewindMode(target) {
		t.dismissRewindMode(target)
	}

	// 1. Exit copy/scroll mode if active — copy mode intercepts input,
	//    preventing delivery to the underlying process.
	if inMode, _ := t.run("display-message", "-p", "-t", target, "#{pane_in_mode}"); strings.TrimSpace(inMode) == "1" {
		_, _ = t.run("send-keys", "-t", target, "-X", "cancel")
		time.Sleep(50 * time.Millisecond)
	}

	// 2. Sanitize control characters that corrupt delivery
	sanitized := sanitizeNudgeMessage(message)

	if !opts.SkipEscape {
		// Auto-skip Escape for Copilot CLI sessions. Escape cancels in-flight
		// generation in Copilot CLI (like Gemini), leaving the nudge text
		// stranded in the input field without Enter being processed. (hq-isz)
		agentType, _ := t.GetEnvironment(session, "GT_AGENT")
		if agentType == "copilot" {
			opts.SkipEscape = true
		}
	}
	// Snapshot before typing the nudge so the message text itself cannot look
	// like the agent's busy indicator.
	sendEscape := !opts.SkipEscape && t.shouldSendEscape(target)

	// 3. Send text via send-keys -l. Messages > 512 bytes are chunked
	//    with 10ms inter-chunk delays to avoid argument length limits.
	if err := t.sendMessageToTarget(target, sanitized); err != nil {
		return err
	}

	// 4. Adaptive post-text delay: scales with message length to give tmux
	// enough time to process all chunks under load. (GH#gt-0b5)
	time.Sleep(adaptiveTextDelay(len(sanitized)))

	if sendEscape {
		// 5. Send Escape to exit vim INSERT mode if enabled (harmless in normal mode)
		// See: https://github.com/anthropics/gastown/issues/307
		_, _ = t.run("send-keys", "-t", target, "Escape")

		// 6. Wait 600ms — must exceed bash readline's keyseq-timeout (500ms default)
		// so ESC is processed alone, not as a meta prefix for the subsequent Enter.
		// Without this, ESC+Enter within 500ms becomes M-Enter (meta-return) which
		// does NOT submit the line.
		time.Sleep(600 * time.Millisecond)

		// 6.5. Post-Escape: check if our Escape triggered Rewind mode.
		// This happens when a previous Escape was still in the input buffer,
		// combining with ours to form the double-Escape that activates Rewind.
		// If triggered, dismiss Rewind and re-send the message (Rewind
		// consumed the original input). Skip the second Escape to avoid
		// re-triggering. (GH#gt-8el)
		if t.isInRewindMode(target) {
			t.dismissRewindMode(target)
			// Re-send message text — Rewind consumed the original input.
			_ = t.sendMessageToTarget(target, sanitized)
			time.Sleep(adaptiveTextDelay(len(sanitized)))
		}
	}

	// 7. Send Enter with verification — polls pane content to confirm Enter
	// was processed, retrying with exponential backoff under load. (GH#gt-0b5)
	if err := t.sendEnterVerified(target); err != nil {
		return fmt.Errorf("nudge to session %q: %w", session, err)
	}

	// 8. Wake the pane to trigger SIGWINCH for detached sessions.
	// Use the resolved target (session:window.pane) rather than the bare
	// session name. resize-window on a bare session name resizes the
	// session's *active* window — in a multi-window session (e.g. one with a
	// `gt feed -w` window open and focused) that is not the agent's window,
	// so the agent's pane never receives SIGWINCH and stays idle despite the
	// delivered nudge. Targeting the resolved pane wakes the correct window.
	t.WakePaneIfDetached(target)
	return nil
}

// NudgePane sends a message to a specific pane reliably.
// Same pattern as NudgeSession but targets a pane ID (e.g., "%9") instead of session name.
// After sending, triggers SIGWINCH to wake Claude in detached sessions.
// Nudges to the same pane are serialized to prevent interleaving.
func (t *Tmux) NudgePane(pane, message string) error {
	// Serialize nudges to this pane to prevent interleaving.
	// Use a timed lock to avoid permanent blocking if a previous nudge hung.
	if !acquireNudgeLock(pane, nudgeLockTimeout) {
		return fmt.Errorf("nudge lock timeout for pane %q: previous nudge may be hung", pane)
	}
	defer releaseNudgeLock(pane)

	// 0. Pre-delivery: dismiss Rewind menu if active. (GH#gt-8el)
	if t.isInRewindMode(pane) {
		t.dismissRewindMode(pane)
	}

	// 1. Exit copy/scroll mode if active — copy mode intercepts input,
	//    preventing delivery to the underlying process.
	if inMode, _ := t.run("display-message", "-p", "-t", pane, "#{pane_in_mode}"); strings.TrimSpace(inMode) == "1" {
		_, _ = t.run("send-keys", "-t", pane, "-X", "cancel")
		time.Sleep(50 * time.Millisecond)
	}

	// 2. Sanitize control characters that corrupt delivery
	sanitized := sanitizeNudgeMessage(message)
	// Snapshot before typing the nudge so the message text itself cannot look
	// like the agent's busy indicator.
	sendEscape := t.shouldSendEscape(pane)

	// 3. Send text via send-keys -l. Messages > 512 bytes are chunked
	//    with 10ms inter-chunk delays to avoid argument length limits.
	if err := t.sendMessageToTarget(pane, sanitized); err != nil {
		return err
	}

	// 4. Adaptive post-text delay: scales with message length. (GH#gt-0b5)
	time.Sleep(adaptiveTextDelay(len(sanitized)))

	if sendEscape {
		// 5. Send Escape to exit vim INSERT mode if enabled (harmless in normal mode)
		// See: https://github.com/anthropics/gastown/issues/307
		_, _ = t.run("send-keys", "-t", pane, "Escape")

		// 6. Wait 600ms — must exceed bash readline's keyseq-timeout (500ms default)
		time.Sleep(600 * time.Millisecond)

		// 6.5. Post-Escape: check if our Escape triggered Rewind mode. (GH#gt-8el)
		if t.isInRewindMode(pane) {
			t.dismissRewindMode(pane)
			_ = t.sendMessageToTarget(pane, sanitized)
			time.Sleep(adaptiveTextDelay(len(sanitized)))
		}
	}

	// 7. Send Enter with verification — polls pane content to confirm Enter
	// was processed, retrying with exponential backoff under load. (GH#gt-0b5)
	if err := t.sendEnterVerified(pane); err != nil {
		return fmt.Errorf("nudge to pane %q: %w", pane, err)
	}

	// 8. Wake the pane to trigger SIGWINCH for detached sessions
	t.WakePaneIfDetached(pane)
	return nil
}

// AcceptStartupDialogs dismisses startup dialogs that can block automated
// sessions. Currently handles (in order):
//  1. Workspace trust dialog (Claude "Quick safety check", Codex "Do you trust the contents of this directory?")
//  2. Bypass permissions warning ("Bypass Permissions mode") — requires Down+Enter
//
// Call this after starting the agent and waiting for it to initialize (WaitForCommand),
// but before sending any prompts. Idempotent: safe to call on sessions without dialogs.
func (t *Tmux) AcceptStartupDialogs(session string) error {
	if err := t.AcceptWorkspaceTrustDialog(session); err != nil {
		return fmt.Errorf("workspace trust dialog: %w", err)
	}
	if err := t.AcceptBypassPermissionsWarning(session); err != nil {
		return fmt.Errorf("bypass permissions warning: %w", err)
	}
	return nil
}

// CheckStartupBlocked fails fast when a known interactive startup modal is
// still visible after dialog acceptance. These modals block automated sessions
// from receiving or acting on the bootstrap prompt.
func (t *Tmux) CheckStartupBlocked(session string) error {
	deadline := time.Now().Add(constants.DialogPollTimeout)
	var blocker string
	for {
		content, err := t.CapturePane(session, 80)
		if err != nil {
			return err
		}
		current, ok := containsBlockingStartupDialog(content)
		if !ok {
			return nil
		}
		blocker = current
		if time.Now().After(deadline) {
			return fmt.Errorf("interactive startup dialog still visible in %s: %s", session, blocker)
		}
		time.Sleep(constants.DialogPollInterval)
	}
}

// AcceptWorkspaceTrustDialog dismisses workspace trust dialogs for supported
// agents. Claude shows "Quick safety check"; Codex shows
// "Do you trust the contents of this directory?". In both cases the safe
// continue option is pre-selected, so Enter accepts the dialog.
//
// Uses a polling loop instead of a single check to handle the race condition where
// the agent hasn't rendered the dialog yet when we first check. Exits early if the
// agent prompt appears (indicating no dialog will be shown).
func (t *Tmux) AcceptWorkspaceTrustDialog(session string) error {
	deadline := time.Now().Add(constants.DialogPollTimeout)
	for time.Now().Before(deadline) {
		content, err := t.CapturePane(session, 30)
		if err != nil {
			time.Sleep(constants.DialogPollInterval)
			continue
		}

		// Look for characteristic trust dialog text before prompt detection.
		// Codex trust screens include a leading ">" banner line, so prompt
		// detection alone would exit too early.
		if containsWorkspaceTrustDialog(content) {
			// Dialog found — accept it (option 1 is pre-selected, just press Enter)
			if _, err := t.run("send-keys", "-t", session, "Enter"); err != nil {
				return err
			}
			// Wait for dialog to dismiss before proceeding
			time.Sleep(500 * time.Millisecond)
			return nil
		}

		// Early exit: if agent prompt or shell prompt is visible, no trust dialog will appear.
		// Claude prompt is ">", shell prompts are "$", "%", "#".
		// Also exit if bypass permissions dialog is next (handled by AcceptBypassPermissionsWarning).
		if containsPromptIndicator(content) || strings.Contains(content, "Bypass Permissions mode") {
			return nil
		}

		time.Sleep(constants.DialogPollInterval)
	}

	// Timeout — no dialog detected, safe to proceed
	return nil
}

func containsWorkspaceTrustDialog(content string) bool {
	return strings.Contains(content, "trust this folder") ||
		strings.Contains(content, "Quick safety check") ||
		strings.Contains(content, "Do you trust the contents of this directory?")
}

func containsBlockingStartupDialog(content string) (string, bool) {
	if promptAppearsAfterStartupBlocker(content) {
		return "", false
	}
	if containsCodexUpdateDialog(content) {
		return "codex update prompt", true
	}
	if containsWorkspaceTrustDialog(content) {
		return "workspace trust prompt", true
	}
	if strings.Contains(content, "Bypass Permissions mode") {
		return "bypass permissions prompt", true
	}
	return "", false
}

func promptAppearsAfterStartupBlocker(content string) bool {
	promptLine := lastPromptIndicatorLine(content)
	if promptLine < 0 {
		return false
	}
	blockerLine := lastStartupBlockerLine(content)
	return blockerLine >= 0 && promptLine > blockerLine
}

func lastStartupBlockerLine(content string) int {
	markers := []string{
		"Update available!",
		"Update now",
		"Skip until next version",
		"trust this folder",
		"Quick safety check",
		"Do you trust the contents of this directory?",
		"Bypass Permissions mode",
	}
	last := -1
	for i, line := range strings.Split(content, "\n") {
		for _, marker := range markers {
			if strings.Contains(line, marker) {
				last = i
				break
			}
		}
	}
	return last
}

func containsCodexUpdateDialog(content string) bool {
	return strings.Contains(content, "Update available!") &&
		strings.Contains(content, "Update now") &&
		strings.Contains(content, "Skip until next version")
}

// promptSuffixes are strings that indicate a shell or agent prompt is visible.
// Claude prompt ends with ">", Codex uses "›", and shells often end with
// "$", "%", "#", or "❯".
var promptSuffixes = []string{">", "›", "$", "%", "#", "❯"}

// containsPromptIndicator checks if pane content contains a prompt indicator
// that signals a shell or agent is ready (no dialog blocking it).
func containsPromptIndicator(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		for _, suffix := range promptSuffixes {
			if strings.HasSuffix(trimmed, suffix) {
				return true
			}
		}
	}
	return false
}

func lastPromptIndicatorLine(content string) int {
	last := -1
	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		for _, suffix := range promptSuffixes {
			if strings.HasSuffix(trimmed, suffix) {
				last = i
				break
			}
		}
	}
	return last
}

// AcceptBypassPermissionsWarning dismisses the Claude Code bypass permissions warning dialog.
// When Claude starts with --dangerously-skip-permissions, it shows a warning dialog that
// requires pressing Down arrow to select "Yes, I accept" and then Enter to confirm.
// This function checks if the warning is present before sending keys to avoid interfering
// with sessions that don't show the warning (e.g., already accepted or different config).
//
// Uses a polling loop instead of a single check to handle the race condition where
// Claude hasn't rendered the dialog yet when we first check. Exits early if the
// agent prompt appears (indicating no dialog will be shown).
//
// Call this after starting Claude and waiting for it to initialize (WaitForCommand),
// but before sending any prompts.
func (t *Tmux) AcceptBypassPermissionsWarning(session string) error {
	deadline := time.Now().Add(constants.DialogPollTimeout)
	for time.Now().Before(deadline) {
		content, err := t.CapturePane(session, 30)
		if err != nil {
			time.Sleep(constants.DialogPollInterval)
			continue
		}

		// Look for the characteristic warning text
		if strings.Contains(content, "Bypass Permissions mode") {
			// Dialog found — press Down to select "Yes, I accept" then Enter
			if _, err := t.run("send-keys", "-t", session, "Down"); err != nil {
				return err
			}
			time.Sleep(200 * time.Millisecond)
			if _, err := t.run("send-keys", "-t", session, "Enter"); err != nil {
				return err
			}
			return nil
		}

		// Early exit: if agent prompt or shell prompt is visible, no dialog will appear
		if containsPromptIndicator(content) {
			return nil
		}

		time.Sleep(constants.DialogPollInterval)
	}

	// Timeout — no dialog detected, safe to proceed
	return nil
}

// DismissStartupDialogsBlind sends the key sequences needed to dismiss all
// known Claude Code startup dialogs without screen-scraping pane content.
// This avoids coupling to third-party TUI strings that can change with any update.
//
// The sequence handles (in order):
//  1. Workspace trust dialog — Enter (option 1 "Yes, I trust this folder" is pre-selected)
//  2. Bypass permissions warning — Down+Enter (select "Yes, I accept" then confirm)
//
// Safe to call on sessions where no dialog is showing: Enter sends a blank input
// to an idle Claude prompt (harmless for a stalled session), and Down+Enter either
// does nothing or sends another blank input.
//
// This is intended for remediation of stalled sessions detected via structured
// signals (session age + activity). For startup-time dialog handling where
// precision matters, use AcceptStartupDialogs instead.
func (t *Tmux) DismissStartupDialogsBlind(session string) error {
	// Step 1: Send Enter to dismiss trust dialog (if present)
	if _, err := t.run("send-keys", "-t", session, "Enter"); err != nil {
		return fmt.Errorf("sending Enter for trust dialog: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Step 2: Send Down+Enter to dismiss bypass permissions dialog (if present)
	if _, err := t.run("send-keys", "-t", session, "Down"); err != nil {
		return fmt.Errorf("sending Down for bypass dialog: %w", err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := t.run("send-keys", "-t", session, "Enter"); err != nil {
		return fmt.Errorf("sending Enter for bypass dialog: %w", err)
	}

	return nil
}

// GetPaneCommand returns the current command running in a pane.
// Returns "bash", "zsh", "claude", "node", etc.
func (t *Tmux) GetPaneCommand(session string) (string, error) {
	// Use display-message targeting the first window explicitly (:^) to avoid
	// returning the active pane's command when a non-agent window is focused.
	// Agent processes always run in the first window; without explicit targeting,
	// a user-created window or split pane (running a shell) could cause health
	// checks to falsely report the agent as dead.
	out, err := t.run("display-message", "-t", session+":^", "-p", "#{pane_current_command}")
	if err != nil {
		return "", err
	}
	result := strings.TrimSpace(out)
	if result == "" {
		return "", fmt.Errorf("empty command for session %s (session may not exist)", session)
	}
	return result, nil
}

// FindAgentPane finds the pane running an agent process within a session.
// In multi-window/multi-pane sessions, send-keys -t <session> targets the
// active/focused pane, which may not be the agent pane. This method returns
// the pane ID (e.g., "%5") of the one running the agent.
//
// ZFC (gt-qmsx): Reads declared GT_PANE_ID from session environment first.
// Falls back to scanning all panes for legacy sessions without GT_PANE_ID.
//
// Returns ("", nil) if the session has only one pane (no disambiguation needed),
// or if no agent pane can be identified (caller should fall back to session targeting).
func (t *Tmux) FindAgentPane(session string) (string, error) {
	// ZFC: read declared pane identity set at session startup (gt-qmsx).
	// This replaces process-tree inference for sessions that record GT_PANE_ID.
	if declaredPane, err := t.GetEnvironment(session, "GT_PANE_ID"); err == nil && declaredPane != "" {
		targetSession := session
		if sessionOut, sessionErr := t.run("display-message", "-t", session, "-p", "#{session_name}"); sessionErr == nil {
			if resolved := strings.TrimSpace(sessionOut); resolved != "" {
				targetSession = resolved
			}
		}

		// Verify the pane still exists in the target session. Pane IDs are tmux-global,
		// so a stale GT_PANE_ID may still resolve in a different restarted session.
		if paneSession, verifyErr := t.run("display-message", "-t", declaredPane, "-p", "#{session_name}"); verifyErr == nil && strings.TrimSpace(paneSession) == targetSession {
			return declaredPane, nil
		}
		// Declared pane is gone or belongs to another session — fall through to scan.
	}

	// Fallback: scan all panes for legacy sessions without GT_PANE_ID.
	return t.findAgentPaneByScan(session)
}

// findAgentPaneByScan enumerates all panes across all windows (-s) and returns
// the pane ID of the one running the agent. This is the legacy path for sessions
// that predate GT_PANE_ID (gt-qmsx).
func (t *Tmux) findAgentPaneByScan(session string) (string, error) {
	out, err := t.run("list-panes", "-s", "-t", session, "-F", "#{pane_id}\t#{pane_current_command}\t#{pane_pid}")
	if err != nil {
		return "", err
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) <= 1 {
		// Single pane - no disambiguation needed
		return "", nil
	}

	// Get agent process names from session environment
	processNames := t.resolveSessionProcessNames(session)

	// Check each pane for agent process
	for _, line := range lines {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		paneID := parts[0]
		paneCmd := parts[1]
		panePID := parts[2]

		if t.matchesPaneRuntime(session, paneCmd, panePID, processNames) {
			return paneID, nil
		}
	}

	// No agent pane found
	return "", nil
}

// GetPaneID returns the pane identifier for a session's first pane.
// Returns a pane ID like "%0" that can be used with RespawnPane.
// Targets pane 0 explicitly to be consistent with GetPaneCommand,
// GetPanePID, and GetPaneWorkDir.
func (t *Tmux) GetPaneID(session string) (string, error) {
	out, err := t.run("display-message", "-t", session+":0.0", "-p", "#{pane_id}")
	if err != nil {
		return "", err
	}
	result := strings.TrimSpace(out)
	if result == "" {
		return "", fmt.Errorf("no panes found in session %s", session)
	}
	return result, nil
}

// GetPaneWorkDir returns the current working directory of a pane.
// Targets pane 0 explicitly to avoid returning the active pane's
// working directory in multi-pane sessions.
func (t *Tmux) GetPaneWorkDir(session string) (string, error) {
	out, err := t.run("display-message", "-t", session+":0.0", "-p", "#{pane_current_path}")
	if err != nil {
		return "", err
	}
	result := strings.TrimSpace(out)
	if result == "" {
		return "", fmt.Errorf("empty working directory for session %s (session may not exist)", session)
	}
	return result, nil
}

// GetPanePID returns the PID of the pane's main process.
// When target is a session name, explicitly targets the first window (:^) to avoid
// returning the active pane's PID when a non-agent window is focused. When target is
// a pane ID (e.g., "%5"), uses it directly.
func (t *Tmux) GetPanePID(target string) (string, error) {
	tmuxTarget := target
	if !strings.HasPrefix(target, "%") {
		tmuxTarget = target + ":^"
	}
	out, err := t.run("display-message", "-t", tmuxTarget, "-p", "#{pane_pid}")
	if err != nil {
		return "", err
	}
	result := strings.TrimSpace(out)
	if result == "" {
		return "", fmt.Errorf("empty PID for target %s (session may not exist)", target)
	}
	return result, nil
}

// GetSessionActivity returns the last activity time for a session.
// This is updated whenever there's any activity in the session (input/output).
func (t *Tmux) GetSessionActivity(session string) (time.Time, error) {
	out, err := t.run("display-message", "-t", session, "-p", "#{session_activity}")
	if err != nil {
		return time.Time{}, err
	}

	timestamp, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing session activity: %w", err)
	}
	return time.Unix(timestamp, 0), nil
}

// ZombieStatus describes the liveness state of a tmux agent session.
type ZombieStatus int

const (
	// SessionHealthy means the session exists and the agent process is alive.
	SessionHealthy ZombieStatus = iota
	// SessionDead means the tmux session does not exist.
	SessionDead
	// AgentDead means the tmux session exists but the agent process has died.
	AgentDead
	// AgentHung means the tmux session and agent process exist but there has
	// been no tmux activity for longer than the specified threshold.
	AgentHung
)

// String returns a human-readable label for the zombie status.
func (z ZombieStatus) String() string {
	switch z {
	case SessionHealthy:
		return "healthy"
	case SessionDead:
		return "session-dead"
	case AgentDead:
		return "agent-dead"
	case AgentHung:
		return "agent-hung"
	default:
		return "unknown"
	}
}

// IsZombie returns true if the status represents a zombie (any non-healthy state
// where the session exists but the agent is dead or hung).
func (z ZombieStatus) IsZombie() bool {
	return z == AgentDead || z == AgentHung
}

// CheckSessionHealth determines the health status of an agent session.
// It performs three levels of checking:
//  1. Session existence (tmux has-session)
//  2. Agent process liveness (IsAgentAlive — checks process tree)
//  3. Activity staleness (GetSessionActivity — checks tmux output timestamp)
//
// The maxInactivity parameter controls how long a session can be idle before
// being considered hung. Pass 0 to skip activity checking (only check process
// liveness). A reasonable default for production is 10-15 minutes.
//
// This is the preferred unified method for zombie detection across all agent types.
func (t *Tmux) CheckSessionHealth(session string, maxInactivity time.Duration) ZombieStatus {
	// Level 1: Does the tmux session exist?
	alive, err := t.HasSession(session)
	if err != nil || !alive {
		return SessionDead
	}

	// Level 2: Is the agent process running inside the session?
	if !t.IsAgentAlive(session) {
		return AgentDead
	}

	// Level 3: Has there been recent activity? (optional)
	if maxInactivity > 0 {
		lastActivity, err := t.GetSessionActivity(session)
		if err == nil && !lastActivity.IsZero() {
			if time.Since(lastActivity) > maxInactivity {
				return AgentHung
			}
		}
		// On error or zero time, skip activity check — don't false-positive
	}

	return SessionHealthy
}

// processMatchesNames checks if a process's binary name matches any of the given names.
// Uses ps to get the actual command name from the process's executable path.
// This handles cases where argv[0] is modified (e.g., Claude showing version "2.1.30").
func processMatchesNames(pid string, names []string) bool {
	if len(names) == 0 {
		return false
	}
	// Use ps to get the command name (COMM column gives the executable name)
	cmd := exec.Command("ps", "-p", pid, "-o", "comm=")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	// Get just the base name (in case it's a full path like /Users/.../claude)
	commPath := strings.TrimSpace(string(out))
	comm := filepath.Base(commPath)

	// Check if any name matches
	for _, name := range names {
		if comm == name {
			return true
		}
	}
	return false
}

// hasDescendantWithNames checks if a process has any descendant (child, grandchild, etc.)
// matching any of the given names. Recursively traverses the process tree up to maxDepth.
// Used when the pane command is a shell (bash, zsh, pwsh) that launched an agent.
func hasDescendantWithNames(pid string, names []string, depth int) bool {
	const maxDepth = 10 // Prevent infinite loops in case of circular references
	if len(names) == 0 || depth > maxDepth {
		return false
	}
	if runtime.GOOS == "windows" {
		return hasDescendantWithNamesWindows(pid, names, depth)
	}
	return hasDescendantWithNamesPosix(pid, names, depth)
}

// hasDescendantWithNamesPosix uses pgrep to find child processes on Unix systems.
func hasDescendantWithNamesPosix(pid string, names []string, depth int) bool {
	const maxDepth = 10
	if depth > maxDepth {
		return false
	}
	// Use pgrep to find child processes
	cmd := exec.Command("pgrep", "-P", pid, "-l")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	// Build a set of names for fast lookup
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}
	// Check if any child matches, or recursively check grandchildren
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "PID name" e.g., "29677 node"
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			childPid := parts[0]
			childName := parts[1]
			// Direct match
			if nameSet[childName] {
				return true
			}
			// Recursive check of descendants
			if hasDescendantWithNames(childPid, names, depth+1) {
				return true
			}
		}
	}
	return false
}

// FindSessionByWorkDir finds tmux sessions where the pane's current working directory
// matches or is under the target directory. Returns session names that match.
// If processNames is provided, only returns sessions that match those processes.
// If processNames is nil or empty, returns all sessions matching the directory.
func (t *Tmux) FindSessionByWorkDir(targetDir string, processNames []string) ([]string, error) {
	sessions, err := t.ListSessions()
	if err != nil {
		return nil, err
	}

	var matches []string
	for _, session := range sessions {
		if session == "" {
			continue
		}

		workDir, err := t.GetPaneWorkDir(session)
		if err != nil {
			continue // Skip sessions we can't query
		}

		// Check if workdir matches target (exact match or subdir)
		if workDir == targetDir || strings.HasPrefix(workDir, targetDir+"/") {
			if len(processNames) > 0 {
				if t.IsRuntimeRunning(session, processNames) {
					matches = append(matches, session)
				}
				continue
			}
			matches = append(matches, session)
		}
	}

	return matches, nil
}

// CapturePane captures the visible content of a pane.
func (t *Tmux) CapturePane(session string, lines int) (string, error) {
	return t.run("capture-pane", "-p", "-t", session, "-S", fmt.Sprintf("-%d", lines))
}

// CapturePaneAll captures all scrollback history.
func (t *Tmux) CapturePaneAll(session string) (string, error) {
	return t.run("capture-pane", "-p", "-t", session, "-S", "-")
}

// CapturePaneLines captures the last N lines of a pane as a slice.
func (t *Tmux) CapturePaneLines(session string, lines int) ([]string, error) {
	out, err := t.CapturePane(session, lines)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// AttachSession attaches to an existing session.
// Note: This replaces the current process with tmux attach.
func (t *Tmux) AttachSession(session string) error {
	_, err := t.run("attach-session", "-t", session)
	return err
}

// SelectWindow selects a window by index.
func (t *Tmux) SelectWindow(session string, index int) error {
	_, err := t.run("select-window", "-t", fmt.Sprintf("%s:%d", session, index))
	return err
}

// ResolveCurrentSession returns the session name for the tmux pane that is an
// ancestor of the calling process. Works even when $TMUX and $TMUX_PANE are
// not in the process environment (e.g., Claude Code hook subprocesses).
//
// Walks up the process parent chain and matches against tmux pane PIDs on
// the configured socket.
func (t *Tmux) ResolveCurrentSession() (string, error) {
	out, err := t.run("list-panes", "-a", "-F", "#{pane_pid} #{session_name}")
	if err != nil {
		return "", fmt.Errorf("listing panes: %w", err)
	}

	paneSessions := make(map[int]string)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		pid, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		paneSessions[pid] = parts[1]
	}

	// Walk up from our PID to PID 1, checking each against pane PIDs
	pid := os.Getpid()
	for pid > 1 {
		if name, ok := paneSessions[pid]; ok {
			return name, nil
		}
		ppid, err := parentPID(pid)
		if err != nil || ppid == pid {
			break
		}
		pid = ppid
	}

	return "", fmt.Errorf("no tmux pane ancestor found for pid %d", os.Getpid())
}

// parentPID returns the parent PID of the given process.
func parentPID(pid int) (int, error) {
	data, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

// SetEnvironment sets an environment variable in the session.
func (t *Tmux) SetEnvironment(session, key, value string) error {
	_, err := t.run("set-environment", "-t", session, key, value)
	return err
}

// GetEnvironment gets an environment variable from the session.
func (t *Tmux) GetEnvironment(session, key string) (string, error) {
	out, err := t.run("show-environment", "-t", session, key)
	if err != nil {
		return "", err
	}
	// psmux may return all environment variables instead of just the requested key.
	// Parse line-by-line and find the matching KEY=value line.
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 && parts[0] == key {
			return parts[1], nil
		}
	}
	// Fallback: if only one line, use it directly (standard tmux behavior)
	parts := strings.SplitN(strings.TrimSpace(out), "=", 2)
	if len(parts) == 2 && parts[0] == key {
		return parts[1], nil
	}
	return "", fmt.Errorf("environment variable %s not found in session %s", key, session)
}

// SetGlobalEnvironment sets an environment variable in the tmux global environment.
// Unlike SetEnvironment, this is not scoped to a session — it applies server-wide.
func (t *Tmux) SetGlobalEnvironment(key, value string) error {
	_, err := t.run("set-environment", "-g", key, value)
	return err
}

// UnsetGlobalEnvironment removes an environment variable from the tmux global environment.
func (t *Tmux) UnsetGlobalEnvironment(key string) error {
	_, err := t.run("set-environment", "-g", "-u", key)
	return err
}

// GetGlobalEnvironment gets an environment variable from the tmux global environment.
func (t *Tmux) GetGlobalEnvironment(key string) (string, error) {
	out, err := t.run("show-environment", "-g", key)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 && parts[0] == key {
			return parts[1], nil
		}
	}
	return "", fmt.Errorf("global environment variable %s not found", key)
}

// GetAllEnvironment returns all environment variables for a session.
func (t *Tmux) GetAllEnvironment(session string) (map[string]string, error) {
	out, err := t.run("show-environment", "-t", session)
	if err != nil {
		return nil, err
	}

	env := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-") {
			// Skip empty lines and unset markers (lines starting with -)
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			env[parts[0]] = parts[1]
		}
	}
	return env, nil
}

// RenameSession renames a session.
func (t *Tmux) RenameSession(oldName, newName string) error {
	if err := validateSessionName(newName); err != nil {
		return err
	}
	_, err := t.run("rename-session", "-t", oldName, newName)
	return err
}

// SessionInfo contains information about a tmux session.
type SessionInfo struct {
	Name         string
	Windows      int
	Created      string
	Attached     bool
	Activity     string // Last activity time
	LastAttached string // Last time the session was attached
}

// DisplayMessage shows a message in the tmux status line.
// This is non-disruptive - it doesn't interrupt the session's input.
// Duration is specified in milliseconds.
func (t *Tmux) DisplayMessage(session, message string, durationMs int) error {
	// Set display time temporarily, show message, then restore
	// Use -d flag for duration in tmux 2.9+
	_, err := t.run("display-message", "-t", session, "-d", fmt.Sprintf("%d", durationMs), message)
	return err
}

// DisplayMessageDefault shows a message with default duration (5 seconds).
func (t *Tmux) DisplayMessageDefault(session, message string) error {
	return t.DisplayMessage(session, message, constants.DefaultDisplayMs)
}

// SendNotificationBanner sends a visible notification banner to a tmux session.
// This interrupts the terminal to ensure the notification is seen.
// Uses echo to print a boxed banner with the notification details.
func (t *Tmux) SendNotificationBanner(session, from, subject string) error {
	// Sanitize inputs to prevent output manipulation
	from = strings.ReplaceAll(from, "\n", " ")
	from = strings.ReplaceAll(from, "\r", " ")
	subject = strings.ReplaceAll(subject, "\n", " ")
	subject = strings.ReplaceAll(subject, "\r", " ")

	// Build the banner text
	banner := fmt.Sprintf(`echo '
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
📬 NEW MAIL from %s
Subject: %s
Run: gt mail inbox
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
'`, from, subject)

	return t.SendKeys(session, banner)
}

// IsAgentRunning checks if an agent appears to be running in the session.
//
// If expectedPaneCommands is non-empty, the pane's current command must match one of them.
// If expectedPaneCommands is empty, any non-shell command counts as "agent running".
func (t *Tmux) IsAgentRunning(session string, expectedPaneCommands ...string) bool {
	cmd, err := t.GetPaneCommand(session)
	if err != nil {
		return false
	}

	if len(expectedPaneCommands) > 0 {
		for _, expected := range expectedPaneCommands {
			if expected != "" && cmd == expected {
				return true
			}
		}
		return false
	}

	// Fallback: any non-shell command counts as running.
	for _, shell := range constants.SupportedShells {
		if cmd == shell {
			return false
		}
	}
	return cmd != ""
}

// IsRuntimeRunning checks if a runtime appears to be running in the session.
//
// ZFC (gt-qmsx): Reads declared GT_PANE_ID from session environment first,
// then checks only that pane. Falls back to scanning all panes for legacy
// sessions without GT_PANE_ID.
func (t *Tmux) IsRuntimeRunning(session string, processNames []string) bool {
	if len(processNames) == 0 {
		return false
	}

	// ZFC: check declared pane identity set at session startup (gt-qmsx).
	if declaredPane, err := t.GetEnvironment(session, "GT_PANE_ID"); err == nil && declaredPane != "" {
		if t.checkTargetPaneForRuntime(session, declaredPane, processNames) {
			return true
		}
		// On Windows (psmux), pane IDs like %1 may not be supported by
		// display-message. Fall through to legacy path instead of returning false.
		if runtime.GOOS != "windows" {
			return false
		}
	}

	// Legacy fallback: check first window, then scan all panes.
	if t.checkPaneForRuntime(session, processNames) {
		return true
	}
	out, err := t.run("list-panes", "-s", "-t", session, "-F", "#{pane_current_command}\t#{pane_pid}")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) < 2 {
			continue
		}
		cmd, pid := parts[0], parts[1]
		if t.matchesPaneRuntime(session, cmd, pid, processNames) {
			return true
		}
	}
	return false
}

// checkTargetPaneForRuntime checks if a specific pane (by ID, e.g., "%5") is
// running a matching process. Used by the ZFC path when GT_PANE_ID is declared.
func (t *Tmux) checkTargetPaneForRuntime(session, paneID string, processNames []string) bool {
	cmd, err := t.run("display-message", "-t", paneID, "-p", "#{pane_current_command}")
	if err != nil {
		return false // pane doesn't exist
	}
	pid, _ := t.run("display-message", "-t", paneID, "-p", "#{pane_pid}")
	return t.matchesPaneRuntime(session, strings.TrimSpace(cmd), strings.TrimSpace(pid), processNames)
}

// checkPaneForRuntime checks if the first window's pane is running a matching process.
func (t *Tmux) checkPaneForRuntime(session string, processNames []string) bool {
	cmd, err := t.GetPaneCommand(session)
	if err != nil {
		return false
	}
	pid, _ := t.GetPanePID(session)
	return t.matchesPaneRuntime(session, cmd, pid, processNames)
}

// cursorAgentSessionDeclaresCursor reports whether tmux session env identifies the Cursor
// runtime (preset "cursor"). Used to disambiguate the generic process name "agent" (Cursor's
// install script symlinks `agent` to the same binary as cursor-agent) from unrelated binaries
// named `agent`. The most reliable signal is GT_AGENT=cursor together with GT_PROCESS_NAMES
// set at session startup (see internal/session/lifecycle.go).
func cursorAgentSessionDeclaresCursor(t *Tmux, session string) bool {
	if t == nil || session == "" {
		return false
	}
	agent, err := t.GetEnvironment(session, "GT_AGENT")
	if err == nil && agent == string(config.AgentCursor) {
		return true
	}
	if names, err := t.GetEnvironment(session, "GT_PROCESS_NAMES"); err == nil && names != "" {
		for _, n := range strings.Split(names, ",") {
			if strings.TrimSpace(n) == "cursor-agent" {
				return true
			}
		}
	}
	return false
}

func withoutProcessName(names []string, drop string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != drop {
			out = append(out, n)
		}
	}
	return out
}

// processNamesForSession returns process names for pane matching, dropping the ambiguous
// name "agent" unless the session declares the Cursor runtime.
func processNamesForSession(t *Tmux, session string, processNames []string) []string {
	if len(processNames) == 0 {
		return processNames
	}
	if cursorAgentSessionDeclaresCursor(t, session) {
		return processNames
	}
	return withoutProcessName(processNames, "agent")
}

// matchesPaneRuntime checks if a pane with the given command and PID is running a matching process.
func (t *Tmux) matchesPaneRuntime(session, cmd, pid string, processNames []string) bool {
	names := processNamesForSession(t, session, processNames)
	if len(names) == 0 {
		return false
	}
	// Direct command match
	for _, name := range names {
		if cmd == name {
			return true
		}
	}
	if pid == "" {
		return false
	}
	// If pane command is a shell, check descendants
	for _, shell := range constants.SupportedShells {
		if cmd == shell {
			return hasDescendantWithNames(pid, names, 0)
		}
	}
	// Unrecognized command: check if process itself matches (version-as-argv[0])
	if processMatchesNames(pid, names) {
		return true
	}
	// Finally check descendants as fallback
	return hasDescendantWithNames(pid, names, 0)
}

// IsAgentAlive checks if an agent is running in the session using agent-agnostic detection.
// It reads GT_PROCESS_NAMES from the session environment for accurate process detection,
// falling back to GT_AGENT-based lookup for legacy sessions.
// This is the preferred method for zombie detection across all agent types.
func (t *Tmux) IsAgentAlive(session string) bool {
	return t.IsRuntimeRunning(session, t.resolveSessionProcessNames(session))
}

// resolveSessionProcessNames returns the process names to check for a session.
// Prefers GT_PROCESS_NAMES (set at startup, handles custom agents that shadow
// built-in presets). Falls back to GT_AGENT-based lookup for legacy sessions.
func (t *Tmux) resolveSessionProcessNames(session string) []string {
	// Prefer explicit process names set at startup (handles custom agents correctly)
	if names, err := t.GetEnvironment(session, "GT_PROCESS_NAMES"); err == nil && names != "" {
		return strings.Split(names, ",")
	}
	// Fallback: resolve from agent name (built-in presets only)
	agentName, _ := t.GetEnvironment(session, "GT_AGENT")
	return config.GetProcessNames(agentName) // Returns Claude defaults if empty
}

// WaitForCommand polls until the pane is NOT running one of the excluded commands.
// Useful for waiting until a shell has started a new process (e.g., claude).
// Returns nil when a non-excluded command is detected, or error on timeout.
//
// ZFC fallback: when the pane command IS a shell (e.g., bash), checks for the
// GT_AGENT_READY env var set by the agent's SessionStart hook (gt prime --hook).
// This handles agents wrapped in shell scripts (e.g., c2claude wrapping
// claude-original) where exec env does not replace the shell as the pane
// foreground process. Replaces process-tree probing (IsAgentAlive) per gt-sk5u.
func (t *Tmux) WaitForCommand(session string, excludeCommands []string, timeout time.Duration) error {
	// ZFC: Clear agent-ready sentinel to prevent stale values from previous
	// agent runs. The agent's SessionStart hook (gt prime --hook) sets this
	// to "1" once the agent is running. Unsetting here ensures we only detect
	// the NEW agent, not a leftover from a previous run.
	_, _ = t.run("set-environment", "-u", "-t", session, EnvAgentReady)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd, err := t.GetPaneCommand(session)
		if err != nil {
			time.Sleep(constants.PollInterval)
			continue
		}
		// Check if current command is NOT in the exclude list
		excluded := false
		for _, exc := range excludeCommands {
			if cmd == exc {
				excluded = true
				break
			}
		}
		if !excluded {
			return nil
		}
		// ZFC fallback: check if the agent signaled readiness via its startup
		// hook. This replaces process-tree descendant probing (IsAgentAlive)
		// for wrapped agents where pane_current_command remains a shell.
		if ready, err := t.GetEnvironment(session, EnvAgentReady); err == nil && ready == "1" {
			return nil
		}
		time.Sleep(constants.PollInterval)
	}
	return fmt.Errorf("timeout waiting for command (still running excluded command)")
}

// WaitForShellReady polls until the pane is running a shell command.
// Useful for waiting until a process has exited and returned to shell.
func (t *Tmux) WaitForShellReady(session string, timeout time.Duration) error {
	shells := constants.SupportedShells
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd, err := t.GetPaneCommand(session)
		if err != nil {
			time.Sleep(constants.PollInterval)
			continue
		}
		for _, shell := range shells {
			if cmd == shell {
				return nil
			}
		}
		time.Sleep(constants.PollInterval)
	}
	return fmt.Errorf("timeout waiting for shell")
}

// WaitForRuntimeReady polls until the runtime's prompt indicator appears in the pane.
// Runtime is ready when we see the configured prompt prefix at the start of a line.
//
// IMPORTANT: Bootstrap vs Steady-State Observation
//
// This function uses regex to detect runtime prompts - a ZFC violation.
// ZFC (Zero False Commands) principle: AI should observe AI, not regex.
//
// Bootstrap (acceptable):
//
//	During cold startup when no AI agent is running, the daemon uses this
//	function to get the Deacon online. Regex is acceptable here.
//
// Steady-State (use AI observation instead):
//
//	Once any AI agent is running, observation should be AI-to-AI:
//	- Deacon monitoring polecats → use patrol formula + AI analysis
//	- Deacon restarting → Mayor watches via 'gt peek'
//	- Mayor restarting → Deacon watches via 'gt peek'

// matchesPromptPrefix reports whether a captured pane line matches the
// configured ready-prompt prefix. It normalizes non-breaking spaces
// (U+00A0) to regular spaces before matching, because Claude Code uses
// NBSP after its ❯ prompt character while the default ReadyPromptPrefix
// uses a regular space. See https://github.com/steveyegge/gastown/issues/1387.
func matchesPromptPrefix(line, readyPromptPrefix string) bool {
	if readyPromptPrefix == "" {
		return false
	}
	trimmed := strings.TrimSpace(line)
	// Normalize NBSP (U+00A0) → regular space so that prompt matching
	// works regardless of which whitespace character the agent uses.
	trimmed = strings.ReplaceAll(trimmed, "\u00a0", " ")
	normalizedPrefix := strings.ReplaceAll(readyPromptPrefix, "\u00a0", " ")
	prefix := strings.TrimSpace(normalizedPrefix)
	return strings.HasPrefix(trimmed, normalizedPrefix) || (prefix != "" && trimmed == prefix)
}

func hasBusyIndicator(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	return strings.Contains(trimmed, "esc to interrupt")
}

// shouldSendEscapeForLines reports whether the vim-mode Escape keystroke
// (nudge delivery step 5) is safe to send, given a snapshot of pane lines.
//
// The Escape exists to exit a vim-mode composer's INSERT mode so the following
// Enter submits the line (GH#307). But in Claude Code — and Codex/Gemini —
// Escape also cancels in-flight generation; the status bar literally reads
// "esc to interrupt" while the agent is working. Sending Escape in that state
// would interrupt the agent's current turn (e.g. the Mayor). Returns false when
// any line shows the busy indicator so the caller suppresses the Escape.
//
// FRAGILITY: this depends on the agent TUI rendering the literal substring
// "esc to interrupt" while generating (via hasBusyIndicator — the same
// assumption IsIdle/WaitForIdle already make). If that upstream status text
// changes, the gate fails open and silently: the Escape is sent again and
// nudges can resume interrupting the agent. Tracked in gastownhall/gastown#4240.
func shouldSendEscapeForLines(lines []string) bool {
	for _, line := range lines {
		if hasBusyIndicator(line) {
			return false
		}
	}
	return true
}

// shouldSendEscape captures the target's pane and reports whether the vim-mode
// Escape is safe to send right now (see shouldSendEscapeForLines). Callers
// snapshot before writing nudge text so the message itself cannot masquerade as
// the busy indicator. On capture failure it returns false: when we cannot
// confirm the agent is idle, skipping the Escape is the safe default — it avoids
// interrupting an active agent and is harmless for the common (non-vim) case
// where Enter alone submits.
func (t *Tmux) shouldSendEscape(target string) bool {
	lines, err := t.CapturePaneLines(target, 5)
	if err != nil {
		return false
	}
	return shouldSendEscapeForLines(lines)
}

func readyPromptPrefixForSession(t *Tmux, session string) string {
	promptPrefix := DefaultReadyPromptPrefix
	agentName, err := t.GetEnvironment(session, "GT_AGENT")
	if err != nil || agentName == "" {
		return promptPrefix
	}
	preset := config.GetAgentPresetByName(agentName)
	if preset == nil || preset.ReadyPromptPrefix == "" {
		return promptPrefix
	}
	return preset.ReadyPromptPrefix
}

func (t *Tmux) WaitForRuntimeReady(session string, rc *config.RuntimeConfig, timeout time.Duration) error {
	if rc == nil || rc.Tmux == nil {
		return nil
	}

	if rc.Tmux.ReadyPromptPrefix == "" {
		if rc.Tmux.ReadyDelayMs <= 0 {
			return nil
		}
		// Fallback to fixed delay when prompt detection is unavailable.
		delay := time.Duration(rc.Tmux.ReadyDelayMs) * time.Millisecond
		if delay > timeout {
			delay = timeout
		}
		time.Sleep(delay)
		return nil
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Capture last few lines of the pane
		lines, err := t.CapturePaneLines(session, 10)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		// Look for runtime prompt indicator at start of line
		for _, line := range lines {
			if matchesPromptPrefix(line, rc.Tmux.ReadyPromptPrefix) {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for runtime prompt")
}

// DefaultReadyPromptPrefix is the Claude Code prompt prefix used for idle detection.
// Claude Code uses ❯ (U+276F) as the prompt character.
const DefaultReadyPromptPrefix = "❯ "

// WaitForIdle polls until the agent appears to be at an idle prompt.
// Unlike WaitForRuntimeReady (which is for bootstrap), this is for steady-state
// idle detection — used to avoid interrupting agents mid-work.
//
// Returns nil if the agent becomes idle within the timeout.
// Returns an error if the timeout expires while the agent is still busy.
func (t *Tmux) WaitForIdle(session string, timeout time.Duration) error {
	promptPrefix := readyPromptPrefixForSession(t, session)
	prefix := strings.TrimSpace(promptPrefix)

	// Require 2 consecutive idle polls to filter out transient states.
	// During inter-tool-call gaps (~500ms), the prompt may briefly appear
	// in the pane buffer while Claude Code is still actively working.
	// Two polls 200ms apart (400ms window) confirms genuine idle state.
	consecutiveIdle := 0
	const requiredConsecutive = 2

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		lines, err := t.CapturePaneLines(session, 5)
		if err != nil {
			// Distinguish terminal errors from transient ones.
			// Session not found or no server means the session is gone —
			// no point in polling further.
			if errors.Is(err, ErrSessionNotFound) || errors.Is(err, ErrNoServer) {
				return err
			}
			consecutiveIdle = 0
			time.Sleep(200 * time.Millisecond)
			continue
		}

		// Busy indicator check: if "esc to interrupt" is visible anywhere in
		// the recent pane output, the agent is actively working — NOT idle,
		// regardless of whether the prompt prefix is also visible.
		statusBarBusy := false
		for _, line := range lines {
			if hasBusyIndicator(line) {
				statusBarBusy = true
				break
			}
		}
		if statusBarBusy {
			consecutiveIdle = 0
			time.Sleep(200 * time.Millisecond)
			continue
		}

		// Scan all captured lines for the prompt prefix.
		// Claude Code renders a status bar below the prompt line,
		// so the prompt may not be the last non-empty line.
		promptFound := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if matchesPromptPrefix(trimmed, promptPrefix) || (prefix != "" && trimmed == prefix) {
				promptFound = true
				break
			}
		}

		if promptFound {
			consecutiveIdle++
			if consecutiveIdle >= requiredConsecutive {
				return nil
			}
		} else {
			consecutiveIdle = 0
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ErrIdleTimeout
}

// IsAtPrompt checks if the agent is currently at an idle prompt (non-blocking).
// Returns true if the pane shows the ReadyPromptPrefix, indicating the agent is
// idle and ready for input. Used by startup nudge verification to detect whether
// a nudge was lost (agent returned to prompt without processing it).
func (t *Tmux) IsAtPrompt(session string, rc *config.RuntimeConfig) bool {
	promptPrefix := DefaultReadyPromptPrefix
	if rc != nil && rc.Tmux != nil && rc.Tmux.ReadyPromptPrefix != "" {
		promptPrefix = rc.Tmux.ReadyPromptPrefix
	}

	lines, err := t.CapturePaneLines(session, 10)
	if err != nil {
		return false
	}

	for _, line := range lines {
		if matchesPromptPrefix(line, promptPrefix) {
			return true
		}
	}
	return false
}

// IsIdle checks whether a session is currently at the idle input prompt (❯)
// with no active work in progress.
// Returns true if idle, false if the agent is busy or the check fails.
// This is a point-in-time snapshot, not a poll.
//
// Detection strategy: check the Claude Code status bar (bottom line of the
// pane starting with ⏵⏵). When the agent is actively working, the status
// bar contains "esc to interrupt". When idle, it does not.
func (t *Tmux) IsIdle(session string) bool {
	lines, err := t.CapturePaneLines(session, 5)
	if err != nil {
		return false
	}

	for _, line := range lines {
		if hasBusyIndicator(line) {
			return false
		}
	}

	promptPrefix := readyPromptPrefixForSession(t, session)
	for _, line := range lines {
		if matchesPromptPrefix(line, promptPrefix) {
			return true
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "⏵⏵") || strings.Contains(trimmed, "\u23F5\u23F5") {
			return true
		}
	}
	return false
}

// GetSessionInfo returns detailed information about a session.
func (t *Tmux) GetSessionInfo(name string) (*SessionInfo, error) {
	format := "#{session_name}|#{session_windows}|#{session_created}|#{session_attached}|#{session_activity}|#{session_last_attached}"
	out, err := t.run("list-sessions", "-F", format, "-f", fmt.Sprintf("#{==:#{session_name},%s}", name))
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, ErrSessionNotFound
	}

	parts := strings.Split(out, "|")
	if len(parts) < 4 {
		return nil, fmt.Errorf("unexpected session info format: %s", out)
	}

	windows := 0
	_, _ = fmt.Sscanf(parts[1], "%d", &windows) // non-fatal: defaults to 0 on parse error

	// Convert unix timestamp to formatted string for consumers.
	created := parts[2]
	var createdUnix int64
	if _, err := fmt.Sscanf(created, "%d", &createdUnix); err == nil && createdUnix > 0 {
		created = time.Unix(createdUnix, 0).Format("2006-01-02 15:04:05")
	}

	info := &SessionInfo{
		Name:     parts[0],
		Windows:  windows,
		Created:  created,
		Attached: parts[3] == "1",
	}

	// Activity and last attached are optional (may not be present in older tmux)
	if len(parts) > 4 {
		info.Activity = parts[4]
	}
	if len(parts) > 5 {
		info.LastAttached = parts[5]
	}

	return info, nil
}

// GetSessionCreatedTime returns the creation time of a tmux session.
// Uses #{session_created} (Unix timestamp) from tmux list-sessions.
func (t *Tmux) GetSessionCreatedTime(name string) (time.Time, error) {
	out, err := t.run("list-sessions", "-F", "#{session_created}", "-f", fmt.Sprintf("#{==:#{session_name},%s}", name))
	if err != nil {
		return time.Time{}, err
	}
	if out == "" {
		return time.Time{}, ErrSessionNotFound
	}
	var unix int64
	if _, err := fmt.Sscanf(strings.TrimSpace(out), "%d", &unix); err != nil {
		return time.Time{}, fmt.Errorf("parsing session created time %q: %w", out, err)
	}
	return time.Unix(unix, 0), nil
}

// ApplyTheme sets the status bar style for a session.
func (t *Tmux) ApplyTheme(session string, theme Theme) error {
	_, err := t.run("set-option", "-t", session, "status-style", theme.Style())
	return err
}

// ClearTheme removes Gas Town tmux styling from a session.
func (t *Tmux) ClearTheme(session string) error {
	if _, err := t.run("set-option", "-t", session, "-u", "status-style"); err != nil {
		return err
	}
	_, err := t.run("set-window-option", "-t", session, "-u", "window-style")
	return err
}

// ApplyWindowStyle sets or resets the window background (window-style).
// If ws is nil, resets to terminal defaults. If non-nil, applies the colors.
func (t *Tmux) ApplyWindowStyle(session string, ws *WindowStyle) error {
	style := "bg=default,fg=default"
	if ws != nil {
		style = ws.Style()
	}
	_, err := t.run("set-option", "-t", session, "window-style", style)
	return err
}

// roleIcons maps role names to display icons for the status bar.
// Uses centralized emojis from constants package.
// Includes legacy keys ("coordinator", "health-check") for backwards compatibility.
var roleIcons = map[string]string{
	// Standard role names (from constants)
	constants.RoleMayor:    constants.EmojiMayor,
	constants.RoleDeacon:   constants.EmojiDeacon,
	constants.RoleWitness:  constants.EmojiWitness,
	constants.RoleRefinery: constants.EmojiRefinery,
	constants.RoleCrew:     constants.EmojiCrew,
	constants.RolePolecat:  constants.EmojiPolecat,
	// Legacy names (for backwards compatibility)
	"coordinator":  constants.EmojiMayor,
	"health-check": constants.EmojiDeacon,
}

// SetStatusFormat configures the left side of the status bar.
// Shows compact identity: icon + minimal context
func (t *Tmux) SetStatusFormat(session, rig, worker, role string) error {
	// Get icon for role (empty string if not found)
	icon := roleIcons[role]

	// Compact format - icon already identifies role
	// Mayor: 🎩 Mayor
	// Crew:  👷 gastown/crew/max (full path)
	// Polecat: 😺 gastown/Toast
	var left string
	if rig == "" {
		// Town-level agent (Mayor, Deacon) - keep as-is
		left = fmt.Sprintf("%s %s ", icon, worker)
	} else {
		// Rig agents - use session name (already in prefix format: gt-crew-gus)
		left = fmt.Sprintf("%s %s ", icon, session)
	}

	if _, err := t.run("set-option", "-t", session, "status-left-length", "25"); err != nil {
		return err
	}
	_, err := t.run("set-option", "-t", session, "status-left", left)
	return err
}

// SetDynamicStatus configures the right side with dynamic content.
// Uses a shell command that tmux calls periodically to get current status.
func (t *Tmux) SetDynamicStatus(session string) error {
	if err := validateSessionName(session); err != nil {
		return err
	}

	// tmux calls this command every status-interval seconds
	// gt status-line reads env vars and mail to build the status
	//
	// On Windows, tmux #() spawns a visible cmd.exe + conhost.exe window on
	// every invocation, causing rapid screen flashing. Fall back to a static
	// status until psmux supports CREATE_NO_WINDOW for #() commands.
	var right string
	if runtime.GOOS == "windows" {
		right = `%H:%M`
	} else {
		right = fmt.Sprintf(`#(gt status-line --session=%s 2>/dev/null) %%H:%%M`, session)
	}

	if _, err := t.run("set-option", "-t", session, "status-right-length", "80"); err != nil {
		return err
	}
	// Keep refresh modest: status-line may inspect hooks/mail, which are Dolt-backed.
	if _, err := t.run("set-option", "-t", session, "status-interval", "60"); err != nil {
		return err
	}
	_, err := t.run("set-option", "-t", session, "status-right", right)
	return err
}

// ConfigureGasTownSession applies Gas Town status configuration to a session.
// A nil theme disables tmux styling while still applying status/bindings.
//
// Window background is controlled by theme.Window:
//   - non-nil: apply Window's colors as the window background
//   - nil: reset window background to terminal defaults (disabled)
func (t *Tmux) ConfigureGasTownSession(session string, theme *Theme, rig, worker, role string) error {
	if theme != nil {
		if err := t.ApplyTheme(session, *theme); err != nil {
			return fmt.Errorf("applying theme: %w", err)
		}
		if err := t.ApplyWindowStyle(session, theme.Window); err != nil {
			return fmt.Errorf("applying window style: %w", err)
		}
	} else {
		if err := t.ClearTheme(session); err != nil {
			return fmt.Errorf("clearing theme: %w", err)
		}
	}
	if err := t.SetStatusFormat(session, rig, worker, role); err != nil {
		return fmt.Errorf("setting status format: %w", err)
	}
	if err := t.SetDynamicStatus(session); err != nil {
		return fmt.Errorf("setting dynamic status: %w", err)
	}
	if err := t.SetMailClickBinding(session); err != nil {
		return fmt.Errorf("setting mail click binding: %w", err)
	}
	if err := t.SetFeedBinding(session); err != nil {
		return fmt.Errorf("setting feed binding: %w", err)
	}
	if err := t.SetAgentsBinding(session); err != nil {
		return fmt.Errorf("setting agents binding: %w", err)
	}
	if err := t.SetRigMenuBinding(session); err != nil {
		return fmt.Errorf("setting rig menu binding: %w", err)
	}
	if err := t.SetCycleBindings(session); err != nil {
		return fmt.Errorf("setting cycle bindings: %w", err)
	}
	if err := t.EnableMouseMode(session); err != nil {
		return fmt.Errorf("enabling mouse mode: %w", err)
	}
	return nil
}

// EnableMouseMode enables mouse support and clipboard integration for a tmux session.
// This allows clicking to select panes/windows, scrolling with mouse wheel,
// and dragging to resize panes. Hold Shift for native terminal text selection.
// Also enables clipboard integration so copied text goes to system clipboard.
//
// Respects the user's global mouse preference: if the global setting is "off",
// mouse is not forced on for the session, so prefix+m toggles work correctly.
func (t *Tmux) EnableMouseMode(session string) error {
	// Check global mouse setting — respect user toggle (prefix+m)
	out, err := t.run("show-options", "-gv", "mouse")
	if err == nil && strings.TrimSpace(out) == "off" {
		// User has globally disabled mouse; don't override per-session
		return nil
	}
	if _, err := t.run("set-option", "-t", session, "mouse", "on"); err != nil {
		return err
	}
	// Enable clipboard integration with terminal (OSC 52)
	// This allows copying text to system clipboard when selecting with mouse
	_, err = t.run("set-option", "-t", session, "set-clipboard", "on")
	return err
}

// IsInsideTmux checks if the current process is running inside a tmux session.
// This is detected by the presence of the TMUX environment variable.
func IsInsideTmux() bool {
	return os.Getenv("TMUX") != ""
}

// SetMailClickBinding configures left-click on status-right to show mail preview.
// This creates a popup showing the first unread message when clicking the mail icon area.
//
// The binding is conditional: it only activates in Gas Town sessions (those matching
// a registered rig prefix or "hq-"). In non-GT sessions, the user's original
// MouseDown1StatusRight binding (if any) is preserved.
// See: https://github.com/steveyegge/gastown/issues/1548
func (t *Tmux) SetMailClickBinding(session string) error {
	// Skip if already configured — preserves user's original fallback from first call
	if t.isGTBinding("root", "MouseDown1StatusRight") {
		return nil
	}
	ifShell := fmt.Sprintf("echo '#{session_name}' | grep -Eq '%s'", sessionPrefixPattern())
	fallback := t.getKeyBinding("root", "MouseDown1StatusRight")
	if fallback == "" {
		// No prior binding — do nothing in non-GT sessions
		fallback = ":"
	}
	_, err := t.run("bind-key", "-T", "root", "MouseDown1StatusRight",
		"if-shell", ifShell,
		"display-popup -E -w 60 -h 15 'gt mail peek || echo No unread mail'",
		fallback)
	return err
}

// RespawnPane kills all processes in a pane and starts a new command.
// This is used for "hot reload" of agent sessions - instantly restart in place.
// The pane parameter should be a pane ID (e.g., "%0") or session:window.pane format.
func (t *Tmux) RespawnPane(pane, command string) error {
	if runtime.GOOS == "windows" {
		// psmux: respawn-pane -k kills the process, then send-keys types the command.
		if _, err := t.run("respawn-pane", "-k", "-t", pane); err != nil {
			return err
		}
		_, err := t.run("send-keys", "-t", pane, command, "Enter")
		return err
	}
	_, err := t.run("respawn-pane", "-k", "-t", pane, command)
	return err
}

// RespawnPaneWithWorkDir kills all processes in a pane and starts a new command
// in the specified working directory. Use this when the pane's current working
// directory may have been deleted.
func (t *Tmux) RespawnPaneWithWorkDir(pane, workDir, command string) error {
	if runtime.GOOS == "windows" {
		if _, err := t.run("respawn-pane", "-k", "-t", pane); err != nil {
			return err
		}
		// Change directory first if needed, then run command
		if workDir != "" {
			cdCmd := fmt.Sprintf("Set-Location %s; %s", psQuoteValue(workDir), command)
			_, err := t.run("send-keys", "-t", pane, cdCmd, "Enter")
			return err
		}
		_, err := t.run("send-keys", "-t", pane, command, "Enter")
		return err
	}
	args := []string{"respawn-pane", "-k", "-t", pane}
	if workDir != "" {
		args = append(args, "-c", workDir)
	}
	args = append(args, command)
	_, err := t.run(args...)
	return err
}

// psQuoteValue quotes a value for PowerShell single-quoted strings.
func psQuoteValue(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ClearHistory clears the scrollback history buffer for a pane.
// This resets copy-mode display from [0/N] to [0/0].
// The pane parameter should be a pane ID (e.g., "%0") or session:window.pane format.
func (t *Tmux) ClearHistory(pane string) error {
	_, err := t.run("clear-history", "-t", pane)
	return err
}

// SetRemainOnExit controls whether a pane stays around after its process exits.
// When on, the pane remains with "[Exited]" status, allowing respawn-pane to restart it.
// When off (default), the pane is destroyed when its process exits.
// This is essential for handoff: set on before killing processes, so respawn-pane works.
func (t *Tmux) SetRemainOnExit(pane string, on bool) error {
	value := "on"
	if !on {
		value = "off"
	}
	_, err := t.run("set-option", "-t", pane, "remain-on-exit", value)
	return err
}

// SwitchClient switches the current tmux client to a different session.
// Used after remote recycle to move the user's view to the recycled session.
func (t *Tmux) SwitchClient(targetSession string) error {
	_, err := t.run("switch-client", "-t", targetSession)
	return err
}

// SetCrewCycleBindings sets up C-b n/p to cycle through sessions.
// This is now an alias for SetCycleBindings - the unified command detects
// session type automatically.
//
// IMPORTANT: We pass #{session_name} to the command because run-shell doesn't
// reliably preserve the session context. tmux expands #{session_name} at binding
// resolution time (when the key is pressed), giving us the correct session.
func (t *Tmux) SetCrewCycleBindings(session string) error {
	return t.SetCycleBindings(session)
}

// SetTownCycleBindings sets up C-b n/p to cycle through sessions.
// This is now an alias for SetCycleBindings - the unified command detects
// session type automatically.
func (t *Tmux) SetTownCycleBindings(session string) error {
	return t.SetCycleBindings(session)
}

// isGTBinding checks if the given key already has a Gas Town binding.
// Used to skip redundant re-binding on repeated ConfigureGasTownSession /
// EnsureBindingsOnSocket calls, preserving the user's original fallback.
//
// Two forms are recognized:
//  1. Guarded form (set by SetAgentsBinding/SetFeedBinding): uses if-shell
//     with a "gt " command — detects both old and new guarded bindings.
//  2. Unguarded form (set by EnsureBindingsOnSocket): direct run-shell
//     invoking "gt agents menu" or "gt feed --window".
func (t *Tmux) isGTBinding(table, key string) bool {
	output, err := t.run("list-keys", "-T", table, key)
	if err != nil || output == "" {
		return false
	}
	// Guarded form: if-shell + "gt ".
	if strings.Contains(output, "if-shell") && strings.Contains(output, "gt ") {
		return true
	}
	// Unguarded form: direct GT commands set by EnsureBindingsOnSocket.
	return strings.Contains(output, "gt agents menu") ||
		strings.Contains(output, "gt feed --window") ||
		strings.Contains(output, "gt rig menu")
}

// isGTBindingWithClient checks if the given key has a GT binding that includes
// --client for multi-client support. Older GT bindings without --client cause
// switch-client to target the wrong client when multiple clients are attached.
func (t *Tmux) isGTBindingWithClient(table, key string) bool {
	output, err := t.run("list-keys", "-T", table, key)
	if err != nil || output == "" {
		return false
	}
	return strings.Contains(output, "if-shell") && strings.Contains(output, "gt ") &&
		strings.Contains(output, "--client")
}

// isGTBindingCurrent checks whether the existing GT cycle binding has the
// current prefix pattern. Returns false if the binding is stale (e.g., after
// gt rig add introduces a new prefix not yet in the grep pattern).
func (t *Tmux) isGTBindingCurrent(table, key, currentPattern string) bool {
	output, err := t.run("list-keys", "-T", table, key)
	if err != nil || output == "" {
		return false
	}
	return strings.Contains(output, currentPattern)
}

// getKeyBinding returns the current tmux command bound to the given key in the
// specified key table. Returns empty string if no binding exists or if querying
// fails. This is used to capture user bindings before overwriting them, so the
// original binding can be preserved in the else branch of an if-shell guard.
//
// The returned string is a tmux command (e.g., "next-window", "run-shell 'lazygit'")
// suitable for use as a command argument to bind-key or if-shell.
//
// If the existing binding is already a Gas Town if-shell binding (detected by
// the presence of both "if-shell" and "gt " in the output), it is treated as
// no prior binding to avoid recursive wrapping on repeated calls.
func (t *Tmux) getKeyBinding(table, key string) string {
	// tmux list-keys -T <table> <key> outputs a line like:
	//   bind-key -T prefix g if-shell "..." "run-shell 'gt agents menu'" ":"
	// We need to extract just the command portion.
	//
	// Assumed format (tested with tmux 3.3+):
	//   bind-key [-r] -T <table> <key> <command...>
	// If tmux changes this format, parsing fails safely (returns ""),
	// which causes the caller to use its default fallback.
	output, err := t.run("list-keys", "-T", table, key)
	if err != nil || output == "" {
		return ""
	}

	// Don't capture existing GT bindings as "user bindings to preserve" —
	// that would wrap our own command in another layer.
	// Check both guarded (if-shell) and unguarded (direct run-shell) forms.
	if strings.Contains(output, "if-shell") && strings.Contains(output, "gt ") {
		return ""
	}
	if strings.Contains(output, "gt agents menu") ||
		strings.Contains(output, "gt feed --window") {
		return ""
	}

	// Parse the binding command from list-keys output.
	// Format: "bind-key [-r] -T <table> <key> <command...>"
	// We need everything after the key name.
	// Find the key in the output and take everything after it.
	fields := strings.Fields(output)
	keyIdx := -1
	for i, f := range fields {
		if f == "-T" && i+2 < len(fields) {
			// Skip table name, the next field is the key
			keyIdx = i + 2
			break
		}
	}
	if keyIdx < 0 || keyIdx >= len(fields)-1 {
		return ""
	}

	// Everything after the key is the command
	// Rejoin from keyIdx+1 onward, but we need to preserve the original spacing.
	// Find the key token in the original string and take everything after it.
	idx := strings.Index(output, " "+fields[keyIdx]+" ")
	if idx < 0 {
		return ""
	}
	cmd := strings.TrimSpace(output[idx+len(" "+fields[keyIdx]+" "):])
	if cmd == "" {
		return ""
	}

	return cmd
}

// safePrefixRe matches the character set guaranteed by beadsPrefixRegexp in
// internal/rig/manager.go.  Used as defense-in-depth: if rigs.json is
// hand-edited with regex metacharacters or shell-special chars, we skip the
// entry rather than injecting it into a grep -Eq / tmux if-shell fragment.
var safePrefixRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9-]{0,19}$`)

// sessionPrefixPattern returns a grep -Eq pattern that matches any registered
// Gas Town session name.  The pattern is built dynamically from rigs.json
// (via config.AllRigPrefixes) so that rigs beyond gastown/hq are recognized.
// "hq" is always included because it lives outside the rig registry
// (town-level services).
//
// Example output: "^(bd|db|fa|gl|gt|hq|la|lc)-"
func sessionPrefixPattern() string {
	seen := map[string]bool{"hq": true, "gt": true} // always include HQ + gastown fallback
	townRoot := os.Getenv("GT_ROOT")
	if townRoot == "" {
		townRoot = os.Getenv("GT_TOWN_ROOT")
	}
	if townRoot != "" {
		for _, p := range config.AllRigPrefixes(townRoot) {
			if safePrefixRe.MatchString(p) {
				seen[p] = true
			}
		}
	}
	sorted := make([]string, 0, len(seen))
	for p := range seen {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	return "^(" + strings.Join(sorted, "|") + ")-"
}

// SetCycleBindings sets up C-b n/p to cycle through related sessions.
// The gt cycle command automatically detects the session type and cycles
// within the appropriate group:
// - Town sessions: Mayor ↔ Deacon
// - Crew sessions: All crew members in the same rig
// - Rig ops sessions: Witness + Refinery + Polecats in the same rig
//
// IMPORTANT: These bindings are conditional - they only run gt cycle for
// Gas Town sessions (those matching a registered rig prefix or "hq-").
// For non-GT sessions, the user's original binding is preserved. If no
// prior binding existed, the tmux defaults (next-window/previous-window)
// are used.
// See: https://github.com/steveyegge/gastown/issues/13
// See: https://github.com/steveyegge/gastown/issues/1548
//
// IMPORTANT: We pass #{session_name} to the command because run-shell doesn't
// reliably preserve the session context. tmux expands #{session_name} at binding
// resolution time (when the key is pressed), giving us the correct session.
func (t *Tmux) SetCycleBindings(session string) error {
	// Skip if already correctly configured:
	// 1. Has --client for multi-client support
	// 2. Has the current prefix pattern (not stale from before a gt rig add)
	// We must re-bind if an older GT binding exists without --client, or if the
	// prefix pattern is stale (missing newly added rig prefixes).
	// See: https://github.com/steveyegge/gastown/issues/2299
	pattern := sessionPrefixPattern()
	if t.isGTBindingWithClient("prefix", "n") && t.isGTBindingCurrent("prefix", "n", pattern) {
		return nil
	}
	ifShell := fmt.Sprintf("echo '#{session_name}' | grep -Eq '%s'", pattern)

	// Capture existing bindings before overwriting, falling back to tmux defaults
	nextFallback := t.getKeyBinding("prefix", "n")
	if nextFallback == "" {
		nextFallback = "next-window"
	}
	prevFallback := t.getKeyBinding("prefix", "p")
	if prevFallback == "" {
		prevFallback = "previous-window"
	}

	// C-b n → gt cycle next for Gas Town sessions, original binding otherwise
	// Pass --client #{client_tty} so switch-client targets the correct client
	// when multiple tmux clients are attached (e.g., gastown + beads rigs).
	if _, err := t.run("bind-key", "-T", "prefix", "n",
		"if-shell", ifShell,
		"run-shell 'gt cycle next --session #{session_name} --client #{client_tty}'",
		nextFallback); err != nil {
		return err
	}
	// C-b p → gt cycle prev for Gas Town sessions, original binding otherwise
	if _, err := t.run("bind-key", "-T", "prefix", "p",
		"if-shell", ifShell,
		"run-shell 'gt cycle prev --session #{session_name} --client #{client_tty}'",
		prevFallback); err != nil {
		return err
	}
	return nil
}

// SetFeedBinding configures C-b a to jump to the activity feed window.
// This creates the feed window if it doesn't exist, or switches to it if it does.
// Uses `gt feed --window` which handles both creation and switching.
//
// IMPORTANT: This binding is conditional - it only runs for Gas Town sessions
// (those matching a registered rig prefix or "hq-"). For non-GT sessions, the
// user's original binding is preserved. If no prior binding existed, the key
// press is silently ignored.
// See: https://github.com/steveyegge/gastown/issues/13
// See: https://github.com/steveyegge/gastown/issues/1548
func (t *Tmux) SetFeedBinding(session string) error {
	pattern := sessionPrefixPattern()
	// Skip if already configured with the current rig prefix pattern.
	// Must re-bind if the pattern is stale (e.g., after gt rig add adds a new prefix).
	if t.isGTBinding("prefix", "a") && t.isGTBindingCurrent("prefix", "a", pattern) {
		return nil
	}
	ifShell := fmt.Sprintf("echo '#{session_name}' | grep -Eq '%s'", pattern)
	fallback := t.getKeyBinding("prefix", "a")
	if fallback == "" {
		// No prior binding — do nothing in non-GT sessions
		fallback = ":"
	}
	_, err := t.run("bind-key", "-T", "prefix", "a",
		"if-shell", ifShell,
		"run-shell 'gt feed --window'",
		fallback)
	return err
}

// SetAgentsBinding configures C-b g to open the agent switcher popup menu.
// This runs `gt agents menu` which displays a tmux popup with all Gas Town agents.
//
// IMPORTANT: This binding is conditional - it only runs for Gas Town sessions
// (those matching a registered rig prefix or "hq-"). For non-GT sessions, the
// user's original binding is preserved. If no prior binding existed, the key
// press is silently ignored.
// See: https://github.com/steveyegge/gastown/issues/1548
func (t *Tmux) SetAgentsBinding(session string) error {
	pattern := sessionPrefixPattern()
	// Skip if already configured with the current rig prefix pattern.
	// Must re-bind if the pattern is stale (e.g., after gt rig add adds a new prefix).
	if t.isGTBinding("prefix", "g") && t.isGTBindingCurrent("prefix", "g", pattern) {
		return nil
	}
	ifShell := fmt.Sprintf("echo '#{session_name}' | grep -Eq '%s'", pattern)
	fallback := t.getKeyBinding("prefix", "g")
	if fallback == "" {
		// No prior binding — do nothing in non-GT sessions
		fallback = ":"
	}
	_, err := t.run("bind-key", "-T", "prefix", "g",
		"if-shell", ifShell,
		"run-shell 'gt agents menu'",
		fallback)
	return err
}

// SetRigMenuBinding configures C-b r to open the rig menu popup.
// This runs `gt rig menu` which displays a tmux display-menu with all rigs
// and per-rig actions (start, stop, park, etc.).
func (t *Tmux) SetRigMenuBinding(session string) error {
	if t.isGTBinding("prefix", "r") {
		return nil
	}
	ifShell := fmt.Sprintf("echo '#{session_name}' | grep -Eq '%s'", sessionPrefixPattern())
	fallback := t.getKeyBinding("prefix", "r")
	if fallback == "" {
		fallback = ":"
	}
	_, err := t.run("bind-key", "-T", "prefix", "r",
		"if-shell", ifShell,
		"run-shell 'gt rig menu'",
		fallback)
	return err
}

// EnsureBindingsOnSocket sets the gt agents menu and feed keybindings on a
// specific tmux socket. This is used during gt up to ensure the bindings work
// even when the user is on a different socket than the town socket.
//
// townSocket is the socket name where GT agents live (e.g. "gt-a1b2c3"). When
// non-empty it is embedded in the binding command as GT_TOWN_SOCKET=<name>
// so that gt agents menu can locate agent sessions even when invoked from a
// directory outside the town root (e.g. a personal tmux session where
// workspace.FindFromCwd fails and InitRegistry is never called).
// Pass "" for test-socket use where InitRegistry is already called.
//
// Unlike SetAgentsBinding/SetFeedBinding (called during gt prime), this method:
//   - Targets a specific socket regardless of the Tmux instance's default
//   - Skips the session-name guard when there is no pre-existing user binding,
//     since the user may be in a personal session (not matching GT prefixes)
//     and still wants the agent menu for cross-socket navigation
//
// Safe to call multiple times; skips if bindings already exist.
func EnsureBindingsOnSocket(socket, townSocket string) error {
	t := NewTmuxWithSocket(socket)

	// Build the command strings, optionally prefixed with GT_TOWN_SOCKET so
	// gt agents menu / gt feed can find the right tmux server even when called
	// from a non-town directory.
	agentsCmd := "gt agents menu"
	feedCmd := "gt feed --window"
	if townSocket != "" {
		agentsCmd = fmt.Sprintf("GT_TOWN_SOCKET=%s gt agents menu", townSocket)
		feedCmd = fmt.Sprintf("GT_TOWN_SOCKET=%s gt feed --window", townSocket)
	}

	// Agents binding (prefix + g)
	if !t.isGTBinding("prefix", "g") {
		ifShell := fmt.Sprintf("echo '#{session_name}' | grep -Eq '%s'", sessionPrefixPattern())
		fallback := t.getKeyBinding("prefix", "g")
		if fallback == "" || fallback == ":" {
			// No user binding to preserve — always show the GT agent menu.
			// This is critical for cross-socket use: on the default socket,
			// no session names match GT prefixes, so an if-shell guard would
			// prevent the menu from ever appearing.
			_, _ = t.run("bind-key", "-T", "prefix", "g",
				"run-shell", agentsCmd)
		} else {
			// User has a custom binding — guard with GT pattern, preserve theirs.
			_, _ = t.run("bind-key", "-T", "prefix", "g",
				"if-shell", ifShell,
				"run-shell '"+agentsCmd+"'",
				fallback)
		}
	}

	// Feed binding (prefix + a)
	if !t.isGTBinding("prefix", "a") {
		ifShell := fmt.Sprintf("echo '#{session_name}' | grep -Eq '%s'", sessionPrefixPattern())
		fallback := t.getKeyBinding("prefix", "a")
		if fallback == "" || fallback == ":" {
			_, _ = t.run("bind-key", "-T", "prefix", "a",
				"run-shell", feedCmd)
		} else {
			_, _ = t.run("bind-key", "-T", "prefix", "a",
				"if-shell", ifShell,
				"run-shell '"+feedCmd+"'",
				fallback)
		}
	}

	// Rig menu binding (prefix + r)
	rigMenuCmd := "gt rig menu"
	if townSocket != "" {
		rigMenuCmd = fmt.Sprintf("GT_TOWN_SOCKET=%s gt rig menu", townSocket)
	}
	if !t.isGTBinding("prefix", "r") {
		ifShell := fmt.Sprintf("echo '#{session_name}' | grep -Eq '%s'", sessionPrefixPattern())
		fallback := t.getKeyBinding("prefix", "r")
		if fallback == "" || fallback == ":" {
			_, _ = t.run("bind-key", "-T", "prefix", "r",
				"run-shell", rigMenuCmd)
		} else {
			_, _ = t.run("bind-key", "-T", "prefix", "r",
				"if-shell", ifShell,
				"run-shell '"+rigMenuCmd+"'",
				fallback)
		}
	}

	return nil
}

// GetSessionCreatedUnix returns the Unix timestamp when a session was created.
// Returns 0 if the session doesn't exist or can't be queried.
func (t *Tmux) GetSessionCreatedUnix(session string) (int64, error) {
	out, err := t.run("display-message", "-t", session, "-p", "#{session_created}")
	if err != nil {
		return 0, err
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing session_created %q: %w", out, err)
	}
	return ts, nil
}

// SocketFromEnv extracts the tmux socket name from the TMUX environment variable.
// TMUX format: /path/to/socket,server_pid,session_index
// Returns the basename of the socket path (e.g., "default", "gt"), or empty if
// not in tmux or the env variable is not set.
func SocketFromEnv() string {
	tmuxEnv := os.Getenv("TMUX")
	if tmuxEnv == "" {
		return ""
	}
	// Extract socket path (everything before first comma)
	parts := strings.SplitN(tmuxEnv, ",", 2)
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}
	return filepath.Base(parts[0])
}

// CurrentSessionName returns the tmux session name for the current process.
// Uses TMUX_PANE to target the caller's actual pane, avoiding tmux picking
// a random session when multiple sessions exist. Returns empty string if not in tmux.
func CurrentSessionName() string {
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return ""
	}
	out, err := BuildCommand("display-message", "-t", pane, "-p", "#{session_name}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// CleanupOrphanedSessions scans for zombie Gas Town sessions and kills them.
// A zombie session is one where tmux is alive but the Claude process has died.
// This runs at `gt start` time to prevent session name conflicts and resource accumulation.
//
// The isGTSession predicate identifies Gas Town sessions (e.g. session.IsKnownSession).
// It is passed as a parameter to avoid a circular import from tmux → session.
//
// Returns:
//   - cleaned: number of zombie sessions that were killed
//   - err: error if session listing failed (individual kill errors are logged but not returned)
func (t *Tmux) CleanupOrphanedSessions(isGTSession func(string) bool) (cleaned int, err error) {
	sessions, err := t.ListSessions()
	if err != nil {
		return 0, fmt.Errorf("listing sessions: %w", err)
	}

	for _, sess := range sessions {
		// Only process Gas Town sessions
		if !isGTSession(sess) {
			continue
		}

		// Check if the session is a zombie (tmux alive, agent dead)
		if !t.IsAgentAlive(sess) {
			// Kill the zombie session
			if killErr := t.KillSessionWithProcesses(sess); killErr != nil {
				// Log but continue - other sessions may still need cleanup
				fmt.Printf("  warning: failed to kill orphaned session %s: %v\n", sess, killErr)
				continue
			}
			cleaned++
		}
	}

	return cleaned, nil
}

// SetPaneDiedHook sets a pane-died hook on a session to detect crashes.
// When the pane exits, tmux runs the hook command with exit status info.
// The agentID is used to identify the agent in crash logs (e.g., "gastown/Toast").
func (t *Tmux) SetPaneDiedHook(session, agentID string) error {
	if err := validateSessionName(session); err != nil {
		return err
	}
	// Sanitize agentID to prevent shell injection (session already validated by regex)
	agentID = strings.ReplaceAll(agentID, "'", "'\\''")
	session = strings.ReplaceAll(session, "'", "'\\''") // safe after validation, but keep for consistency

	// Hook command logs the crash with exit status
	// #{pane_dead_status} is the exit code of the process that died
	// We run gt log crash which records to the town log
	hookCmd := fmt.Sprintf(`run-shell "gt log crash --agent '%s' --session '%s' --exit-code #{pane_dead_status}"`,
		agentID, session)

	// Set the hook on this specific session
	_, err := t.run("set-hook", "-t", session, "pane-died", hookCmd)
	return err
}

// SetAutoRespawnHook configures a session to automatically respawn when the pane dies.
// This is used for persistent agents like Deacon that should never exit.
// PATCH-010: Fixes Deacon crash loop by respawning at tmux level.
//
// The hook:
// 1. Waits 3 seconds (debounce rapid crashes)
// 2. Checks if pane is still dead (daemon may have already restarted it)
// 3. Respawns the pane with its original command
// 4. Re-enables remain-on-exit (respawn-pane resets it to off!)
//
// The hook uses run-shell -b (background) to prevent output from leaking to
// the user's active tmux pane, and includes || true to suppress error display.
//
// Requires remain-on-exit to be set first (called automatically by this function).
func (t *Tmux) SetAutoRespawnHook(session string) error {
	if err := validateSessionName(session); err != nil {
		return err
	}
	// First, enable remain-on-exit so the pane stays after process exit
	if err := t.SetRemainOnExit(session, true); err != nil {
		return fmt.Errorf("setting remain-on-exit: %w", err)
	}

	// Sanitize session name for shell safety
	safeSession := strings.ReplaceAll(session, "'", "'\\''")

	// Build the tmux command prefix, including socket flag when configured.
	// When a socket is configured, the embedded tmux commands MUST include
	// the -L flag. run-shell spawns a subprocess that runs bare `tmux` which
	// would otherwise connect to the default server instead of the town socket.
	tmuxCmd := "tmux"
	if t.socketName != "" {
		tmuxCmd = fmt.Sprintf("tmux -L %s", t.socketName)
	}

	hookCmd := buildAutoRespawnHookCmd(tmuxCmd, safeSession)

	// Set the hook on this specific session.
	// Note: this OVERWRITES any existing pane-died hook (e.g., SetPaneDiedHook).
	// tmux only allows one hook per event per session.
	_, err := t.run("set-hook", "-t", session, "pane-died", hookCmd)
	if err != nil {
		return fmt.Errorf("setting pane-died hook: %w", err)
	}

	return nil
}

// buildAutoRespawnHookCmd builds the pane-died hook command string for auto-respawn.
// The tmuxCmd parameter is the tmux binary invocation (e.g., "tmux -L gt" or "tmux").
// The session parameter is the already-sanitized session name.
//
// The command has three safety measures:
//
//  1. run-shell -b: Runs in background so output/errors never leak to the
//     user's active tmux pane. Without -b, run-shell displays failures
//     (like "'...' returned 1") on the attached client's current pane,
//     which can take over an unrelated session the user is viewing.
//
//  2. Dead-pane guard: Checks #{pane_dead} before respawning. The daemon's
//     heartbeat may have already restarted the session during the 3-second
//     sleep window. Without this guard, the hook blindly runs respawn-pane -k
//     which kills the daemon's freshly-started agent.
//
//  3. || true: Ensures the overall command always exits 0, suppressing any
//     error display from tmux even if the session was killed entirely.
func buildAutoRespawnHookCmd(tmuxCmd, session string) string {
	// The shell pipeline:
	//   sleep 3                              -- debounce rapid crashes
	//   list-panes ... #{pane_dead} | grep   -- guard: only proceed if pane is still dead
	//   respawn-pane -k                      -- restart with original command
	//   set-option remain-on-exit on         -- re-enable (respawn-pane resets it to off!)
	//   || true                              -- suppress errors unconditionally
	//
	// IMPORTANT: run-shell expands format variables (#{...}) at hook fire time,
	// not at shell execution time. We need the pane_dead check to run 3 seconds
	// AFTER the pane dies (to detect if the daemon already restarted it).
	// Using ##{pane_dead} escapes the first expansion (## -> #), so the shell
	// receives #{pane_dead} and passes it to the nested `tmux list-panes` call
	// which evaluates it at query time -- giving us the CURRENT pane state.
	return fmt.Sprintf(
		`run-shell -b "sleep 3 && %s list-panes -t '%s' -F '##{pane_dead}' 2>/dev/null | grep -q 1 && %s respawn-pane -k -t '%s' && %s set-option -t '%s' remain-on-exit on || true"`,
		tmuxCmd, session, tmuxCmd, session, tmuxCmd, session)
}
