// Package lock provides agent identity locking to prevent multiple agents
// from claiming the same worker identity.
//
// Lock files are stored at <worker>/.runtime/agent.lock and contain:
// - PID of the owning process
// - Timestamp when lock was acquired
// - Session ID (tmux session name)
//
// Stale locks (where the PID is dead) are automatically cleaned up.
package lock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jonbaldie/gastown/internal/tmux"
)

// Common errors
var (
	ErrLocked      = errors.New("worker is locked by another agent")
	ErrNotLocked   = errors.New("worker is not locked")
	ErrInvalidLock = errors.New("invalid lock file")
)

// LockInfo contains information about who holds a lock.
type LockInfo struct {
	PID        int       `json:"pid"`
	AcquiredAt time.Time `json:"acquired_at"`
	SessionID  string    `json:"session_id,omitempty"`
	Hostname   string    `json:"hostname,omitempty"`
}

// IsStale checks if the lock is stale (owning process is dead).
func (l *LockInfo) IsStale() bool {
	return !processExists(l.PID)
}

// Lock represents an agent identity lock for a worker directory.
type Lock struct {
	workerDir string
	lockPath  string
}

// New creates a Lock for the given worker directory.
func New(workerDir string) *Lock {
	return &Lock{
		workerDir: workerDir,
		lockPath:  filepath.Join(workerDir, ".runtime", "agent.lock"),
	}
}

// Acquire attempts to acquire the lock for this worker.
// Returns ErrLocked if another live process holds the lock.
// Automatically cleans up stale locks.
//
// Uses OS-level advisory locking (flock) to prevent TOCTOU races
// where two processes could both see no lock and both write one.
func (l *Lock) Acquire(sessionID string) error {
	// Ensure .runtime directory exists before flock
	dir := filepath.Dir(l.lockPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating lock directory: %w", err)
	}

	// Acquire advisory flock to serialize concurrent Acquire() calls.
	// The flock file is separate from the lock file itself.
	unlock, err := flockAcquire(l.lockPath + ".flock")
	if err != nil {
		return fmt.Errorf("acquiring coordination lock: %w", err)
	}
	defer unlock()

	// Check for existing lock (now safe from races)
	info, err := l.Read()
	if err == nil {
		// Lock exists - check if stale
		if info.IsStale() {
			// Stale lock - remove it
			if err := l.Release(); err != nil {
				return fmt.Errorf("removing stale lock: %w", err)
			}
		} else {
			// Active lock - check if it's us
			if info.PID == os.Getpid() {
				// We already hold it - refresh
				return l.write(sessionID)
			}
			// Another process holds it
			return fmt.Errorf("%w: PID %d (session: %s, acquired: %s)",
				ErrLocked, info.PID, info.SessionID, info.AcquiredAt.Format(time.RFC3339))
		}
	}

	// No lock or stale lock removed - acquire it
	return l.write(sessionID)
}

// Release releases the lock if we hold it.
func (l *Lock) Release() error {
	if err := os.Remove(l.lockPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing lock file: %w", err)
	}
	return nil
}

// Read reads the current lock info without modifying it.
func (l *Lock) Read() (*LockInfo, error) {
	data, err := os.ReadFile(l.lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotLocked
		}
		return nil, fmt.Errorf("reading lock file: %w", err)
	}

	var info LockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidLock, err)
	}

	return &info, nil
}

// Check checks if the worker is locked by another agent.
// Returns nil if unlocked or locked by us.
// Returns ErrLocked if locked by another live process.
// Automatically cleans up stale locks.
func (l *Lock) Check() error {
	info, err := l.Read()
	if err != nil {
		if errors.Is(err, ErrNotLocked) {
			return nil // Not locked
		}
		return err
	}

	// Check if stale
	if info.IsStale() {
		// Clean up stale lock (best-effort cleanup)
		_ = l.Release()
		return nil
	}

	// Check if it's us
	if info.PID == os.Getpid() {
		return nil
	}

	// Locked by another process
	return fmt.Errorf("%w: PID %d (session: %s)", ErrLocked, info.PID, info.SessionID)
}

// Status returns a human-readable status of the lock.
func (l *Lock) Status() string {
	info, err := l.Read()
	if err != nil {
		if errors.Is(err, ErrNotLocked) {
			return "unlocked"
		}
		return fmt.Sprintf("error: %v", err)
	}

	if info.IsStale() {
		return fmt.Sprintf("stale (dead PID %d)", info.PID)
	}

	if info.PID == os.Getpid() {
		return "locked (by us)"
	}

	return fmt.Sprintf("locked by PID %d (session: %s)", info.PID, info.SessionID)
}

// ForceRelease removes the lock regardless of who holds it.
// Use with caution - only for doctor --fix scenarios.
func (l *Lock) ForceRelease() error {
	return l.Release()
}

// write creates or updates the lock file.
func (l *Lock) write(sessionID string) error {
	// Ensure .runtime directory exists
	dir := filepath.Dir(l.lockPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating lock directory: %w", err)
	}

	hostname, _ := os.Hostname()
	info := LockInfo{
		PID:        os.Getpid(),
		AcquiredAt: time.Now(),
		SessionID:  sessionID,
		Hostname:   hostname,
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling lock info: %w", err)
	}

	// Write to temp file then rename atomically to prevent partial lock files
	// on crash. os.Rename is atomic on POSIX when src and dst are on the same
	// filesystem (guaranteed here — same directory).
	tmpPath := l.lockPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil { //nolint:gosec // G306: lock files are non-sensitive operational data
		return fmt.Errorf("writing temp lock file: %w", err)
	}
	if err := os.Rename(tmpPath, l.lockPath); err != nil {
		// Clean up temp file on rename failure
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming lock file: %w", err)
	}

	return nil
}

// FindAllLocks scans for agent.lock files, skipping large internal
// directories (.dolt-data, .git) that are extremely slow to walk on
// Docker bind mounts (macOS VirtioFS).
// Returns a map of worker directory -> LockInfo.
func FindAllLocks(root string) (map[string]*LockInfo, error) {
	locks := make(map[string]*LockInfo)

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		// Skip large internal directories that never contain agent locks.
		if info.IsDir() {
			name := info.Name()
			if name == ".dolt-data" || name == ".dolt-backup" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}

		if filepath.Base(path) == "agent.lock" && filepath.Base(filepath.Dir(path)) == ".runtime" {
			workerDir := filepath.Dir(filepath.Dir(path))
			lock := New(workerDir)
			lockInfo, err := lock.Read()
			if err == nil {
				locks[workerDir] = lockInfo
			}
		}

		return nil
	})

	return locks, err
}

// CleanStaleLocks removes all stale locks in a directory tree.
// Returns the number of stale locks cleaned.
// A lock is only truly stale if BOTH the PID is dead AND the tmux session
// doesn't exist. This prevents killing active workers whose spawning process
// has exited (which is normal - Claude runs as a child in tmux).
func CleanStaleLocks(root string) (int, error) {
	locks, err := FindAllLocks(root)
	if err != nil {
		return 0, err
	}

	// Get active tmux sessions to verify locks
	activeSessions := getActiveTmuxSessions()
	sessionSet := make(map[string]bool)
	for _, s := range activeSessions {
		sessionSet[s] = true
	}

	cleaned := 0
	for workerDir, info := range locks {
		if info.IsStale() {
			// PID is dead, but check if session still exists
			if info.SessionID != "" && sessionSet[info.SessionID] {
				// Session exists - worker is alive, don't clean
				continue
			}
			// Both PID dead AND no session = truly stale
			lock := New(workerDir)
			if err := lock.Release(); err == nil {
				cleaned++
			}
		}
	}

	return cleaned, nil
}

// getActiveTmuxSessions returns a list of active tmux session identifiers.
// Returns both session names (gt-foo-bar) and session IDs in various formats
// (%N, $N) to handle different lock file formats.
func getActiveTmuxSessions() []string {
	// Get both session name and ID to handle different lock formats
	// Format: "session_name:session_id" e.g., "gt-beads-crew-dave:$55"
	// Use the town's tmux socket so we query the correct server.
	// Without -L, bare "tmux" queries the "default" socket, which misses
	// all sessions on the per-town socket (e.g., "gt-a1b2c3") and causes
	// CleanStaleLocks to incorrectly remove locks for active sessions.
	args := []string{}
	if sock := tmux.GetDefaultSocket(); sock != "" {
		args = append(args, "-L", sock)
	}
	args = append(args, "list-sessions", "-F", "#{session_name}:#{session_id}")
	cmd := execCommand("tmux", args...)
	output, err := cmd.Output()
	if err != nil {
		return nil // tmux not running or not installed
	}

	var sessions []string
	for _, line := range splitLines(string(output)) {
		if line == "" {
			continue
		}
		// Parse "name:$id" format
		parts := splitOnColon(line)
		if len(parts) >= 1 {
			sessions = append(sessions, parts[0]) // session name
		}
		if len(parts) >= 2 {
			id := parts[1]
			sessions = append(sessions, id) // $N format
			// Also add %N format (old tmux style) for compatibility
			if len(id) > 0 && id[0] == '$' {
				sessions = append(sessions, "%"+id[1:])
			}
		}
	}
	return sessions
}

// splitOnColon splits on the first colon only (session names shouldn't have colons)
func splitOnColon(s string) []string {
	idx := -1
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			idx = i
			break
		}
	}
	if idx < 0 {
		return []string{s}
	}
	return []string{s[:idx], s[idx+1:]}
}

// execCommand is a wrapper for exec.Command to allow testing
var execCommand = func(name string, args ...string) interface{ Output() ([]byte, error) } {
	return realExecCommand(name, args...)
}

func realExecCommand(name string, args ...string) interface{ Output() ([]byte, error) } {
	return &execCmdWrapper{name: name, args: args}
}

type execCmdWrapper struct {
	name string
	args []string
}

func (c *execCmdWrapper) Output() ([]byte, error) {
	cmd := exec.Command(c.name, c.args...) //nolint:gosec // G204: command args are controlled internally
	setProcessGroup(cmd)
	return cmd.Output()
}

// splitLines splits a string into lines, handling both \n and \r\n
func splitLines(s string) []string {
	var lines []string
	var current []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, string(current))
			current = nil
		} else if s[i] == '\r' {
			// Skip \r
		} else {
			current = append(current, s[i])
		}
	}
	if len(current) > 0 {
		lines = append(lines, string(current))
	}
	return lines
}

// DetectCollisions finds workers with multiple agents claiming the same identity.
// This detects the case where multiple processes think they own the same worker
// by comparing tmux sessions with lock files.
// Returns a list of collision descriptions.
func DetectCollisions(root string, activeSessions []string) []string {
	var collisions []string

	locks, err := FindAllLocks(root)
	if err != nil {
		return collisions
	}

	// Build set of active sessions
	activeSet := make(map[string]bool)
	for _, s := range activeSessions {
		activeSet[s] = true
	}

	for workerDir, info := range locks {
		if info.IsStale() {
			collisions = append(collisions,
				fmt.Sprintf("stale lock in %s (dead PID %d, session: %s)",
					workerDir, info.PID, info.SessionID))
			continue
		}

		// Check if the session in the lock matches an active session
		if info.SessionID != "" && !activeSet[info.SessionID] {
			collisions = append(collisions,
				fmt.Sprintf("orphaned lock in %s (session %s not found, PID %d still alive)",
					workerDir, info.SessionID, info.PID))
		}
	}

	return collisions
}
