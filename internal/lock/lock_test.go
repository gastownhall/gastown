package lock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/tmux"
)

func TestNew(t *testing.T) {
	workerDir := "/tmp/test-worker"
	l := New(workerDir)

	if l.workerDir != workerDir {
		t.Errorf("workerDir = %q, want %q", l.workerDir, workerDir)
	}

	expectedPath := filepath.Join(workerDir, ".runtime", "agent.lock")
	if l.lockPath != expectedPath {
		t.Errorf("lockPath = %q, want %q", l.lockPath, expectedPath)
	}
}

func TestLockInfo_IsStale(t *testing.T) {
	tests := []struct {
		name      string
		pid       int
		wantStale bool
	}{
		{"current process", os.Getpid(), false},
		{"invalid pid zero", 0, true},
		{"invalid pid negative", -1, true},
		{"non-existent pid", 999999999, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &LockInfo{PID: tt.pid}
			if got := info.IsStale(); got != tt.wantStale {
				t.Errorf("IsStale() = %v, want %v", got, tt.wantStale)
			}
		})
	}
}

func TestLock_AcquireAndRelease(t *testing.T) {
	tmpDir := t.TempDir()
	workerDir := filepath.Join(tmpDir, "worker")
	if err := os.MkdirAll(workerDir, 0755); err != nil {
		t.Fatal(err)
	}

	l := New(workerDir)

	// Acquire lock
	err := l.Acquire("test-session")
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	// Verify lock file exists
	info, err := l.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if info.PID != os.Getpid() {
		t.Errorf("PID = %d, want %d", info.PID, os.Getpid())
	}
	if info.SessionID != "test-session" {
		t.Errorf("SessionID = %q, want %q", info.SessionID, "test-session")
	}

	// Release lock
	err = l.Release()
	if err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	// Verify lock file is gone
	_, err = l.Read()
	if err != ErrNotLocked {
		t.Errorf("Read() after release: error = %v, want ErrNotLocked", err)
	}
}

func TestLock_AcquireAlreadyHeld(t *testing.T) {
	tmpDir := t.TempDir()
	workerDir := filepath.Join(tmpDir, "worker")
	if err := os.MkdirAll(workerDir, 0755); err != nil {
		t.Fatal(err)
	}

	l := New(workerDir)

	// Acquire lock first time
	if err := l.Acquire("session-1"); err != nil {
		t.Fatalf("First Acquire() error = %v", err)
	}

	// Re-acquire with different session should refresh
	if err := l.Acquire("session-2"); err != nil {
		t.Fatalf("Second Acquire() error = %v", err)
	}

	// Verify session was updated
	info, err := l.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if info.SessionID != "session-2" {
		t.Errorf("SessionID = %q, want %q", info.SessionID, "session-2")
	}

	l.Release()
}

func TestLock_AcquireStaleLock(t *testing.T) {
	tmpDir := t.TempDir()
	workerDir := filepath.Join(tmpDir, "worker")
	runtimeDir := filepath.Join(workerDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a stale lock file with non-existent PID
	staleLock := LockInfo{
		PID:        999999999, // Non-existent PID
		AcquiredAt: time.Now().Add(-time.Hour),
		SessionID:  "dead-session",
	}
	data, _ := json.Marshal(staleLock)
	lockPath := filepath.Join(runtimeDir, "agent.lock")
	if err := os.WriteFile(lockPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	l := New(workerDir)

	// Should acquire by cleaning up stale lock
	if err := l.Acquire("new-session"); err != nil {
		t.Fatalf("Acquire() with stale lock error = %v", err)
	}

	// Verify we now own it
	info, err := l.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if info.PID != os.Getpid() {
		t.Errorf("PID = %d, want %d", info.PID, os.Getpid())
	}
	if info.SessionID != "new-session" {
		t.Errorf("SessionID = %q, want %q", info.SessionID, "new-session")
	}

	l.Release()
}

func TestLock_Read(t *testing.T) {
	tmpDir := t.TempDir()
	workerDir := filepath.Join(tmpDir, "worker")
	runtimeDir := filepath.Join(workerDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatal(err)
	}

	l := New(workerDir)

	// Test reading non-existent lock
	_, err := l.Read()
	if err != ErrNotLocked {
		t.Errorf("Read() non-existent: error = %v, want ErrNotLocked", err)
	}

	// Test reading invalid JSON
	lockPath := filepath.Join(runtimeDir, "agent.lock")
	if err := os.WriteFile(lockPath, []byte("invalid json"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err = l.Read()
	if err == nil {
		t.Error("Read() invalid JSON: expected error, got nil")
	}

	// Test reading valid lock
	validLock := LockInfo{
		PID:        12345,
		AcquiredAt: time.Now(),
		SessionID:  "test",
		Hostname:   "testhost",
	}
	data, _ := json.Marshal(validLock)
	if err := os.WriteFile(lockPath, data, 0644); err != nil {
		t.Fatal(err)
	}
	info, err := l.Read()
	if err != nil {
		t.Fatalf("Read() valid lock: error = %v", err)
	}
	if info.PID != 12345 {
		t.Errorf("PID = %d, want 12345", info.PID)
	}
	if info.SessionID != "test" {
		t.Errorf("SessionID = %q, want %q", info.SessionID, "test")
	}
}

func TestLock_Check(t *testing.T) {
	tmpDir := t.TempDir()
	workerDir := filepath.Join(tmpDir, "worker")
	runtimeDir := filepath.Join(workerDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatal(err)
	}

	l := New(workerDir)

	// Check when unlocked
	if err := l.Check(); err != nil {
		t.Errorf("Check() unlocked: error = %v, want nil", err)
	}

	// Acquire and check (should pass - we hold it)
	if err := l.Acquire("test"); err != nil {
		t.Fatal(err)
	}
	if err := l.Check(); err != nil {
		t.Errorf("Check() owned by us: error = %v, want nil", err)
	}
	l.Release()

	// Create lock owned by another process - we'll simulate this by using a
	// fake "live" process via the stale lock detection mechanism.
	// Since we can't reliably find another live PID we can signal on all platforms,
	// we test that Check() correctly identifies our own PID vs a different PID.
	// The stale lock cleanup path is tested elsewhere.

	// Test that a non-existent PID lock gets cleaned up and returns nil
	staleLock := LockInfo{
		PID:        999999999, // Non-existent PID
		AcquiredAt: time.Now(),
		SessionID:  "other-session",
	}
	data, _ := json.Marshal(staleLock)
	lockPath := filepath.Join(runtimeDir, "agent.lock")
	if err := os.WriteFile(lockPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	// Check should clean up the stale lock and return nil
	err := l.Check()
	if err != nil {
		t.Errorf("Check() with stale lock: error = %v, want nil (should clean up)", err)
	}

	// Verify lock was cleaned up
	if _, statErr := os.Stat(lockPath); !os.IsNotExist(statErr) {
		t.Error("Check() should have removed stale lock file")
	}
}

func TestLock_Status(t *testing.T) {
	tmpDir := t.TempDir()
	workerDir := filepath.Join(tmpDir, "worker")
	runtimeDir := filepath.Join(workerDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatal(err)
	}

	l := New(workerDir)

	// Unlocked status
	status := l.Status()
	if status != "unlocked" {
		t.Errorf("Status() unlocked = %q, want %q", status, "unlocked")
	}

	// Owned by us
	if err := l.Acquire("test"); err != nil {
		t.Fatal(err)
	}
	status = l.Status()
	if status != "locked (by us)" {
		t.Errorf("Status() owned = %q, want %q", status, "locked (by us)")
	}
	l.Release()

	// Stale lock
	staleLock := LockInfo{
		PID:        999999999,
		AcquiredAt: time.Now(),
		SessionID:  "dead",
	}
	data, _ := json.Marshal(staleLock)
	lockPath := filepath.Join(runtimeDir, "agent.lock")
	if err := os.WriteFile(lockPath, data, 0644); err != nil {
		t.Fatal(err)
	}
	status = l.Status()
	expected := "stale (dead PID 999999999)"
	if status != expected {
		t.Errorf("Status() stale = %q, want %q", status, expected)
	}

	os.Remove(lockPath)
}

func TestLock_ForceRelease(t *testing.T) {
	tmpDir := t.TempDir()
	workerDir := filepath.Join(tmpDir, "worker")
	if err := os.MkdirAll(workerDir, 0755); err != nil {
		t.Fatal(err)
	}

	l := New(workerDir)
	if err := l.Acquire("test"); err != nil {
		t.Fatal(err)
	}

	if err := l.ForceRelease(); err != nil {
		t.Errorf("ForceRelease() error = %v", err)
	}

	_, err := l.Read()
	if err != ErrNotLocked {
		t.Errorf("Read() after ForceRelease: error = %v, want ErrNotLocked", err)
	}
}

func TestProcessExists(t *testing.T) {
	// Current process exists
	if !processExists(os.Getpid()) {
		t.Error("processExists(current PID) = false, want true")
	}

	// Note: PID 1 (init/launchd) cannot be signaled without permission on macOS,
	// so we only test our own process and invalid PIDs.

	// Invalid PIDs
	if processExists(0) {
		t.Error("processExists(0) = true, want false")
	}
	if processExists(-1) {
		t.Error("processExists(-1) = true, want false")
	}
	if processExists(999999999) {
		t.Error("processExists(999999999) = true, want false")
	}
}

func TestFindAllLocks(t *testing.T) {
	tmpDir := t.TempDir()

	// Create multiple worker directories with locks
	workers := []string{"worker1", "worker2", "worker3"}
	for i, w := range workers {
		runtimeDir := filepath.Join(tmpDir, w, ".runtime")
		if err := os.MkdirAll(runtimeDir, 0755); err != nil {
			t.Fatal(err)
		}
		info := LockInfo{
			PID:        i + 100,
			AcquiredAt: time.Now(),
			SessionID:  "session-" + w,
		}
		data, _ := json.Marshal(info)
		lockPath := filepath.Join(runtimeDir, "agent.lock")
		if err := os.WriteFile(lockPath, data, 0644); err != nil {
			t.Fatal(err)
		}
	}

	locks, err := FindAllLocks(tmpDir)
	if err != nil {
		t.Fatalf("FindAllLocks() error = %v", err)
	}

	if len(locks) != 3 {
		t.Errorf("FindAllLocks() found %d locks, want 3", len(locks))
	}

	for _, w := range workers {
		workerDir := filepath.Join(tmpDir, w)
		if _, ok := locks[workerDir]; !ok {
			t.Errorf("FindAllLocks() missing lock for %s", w)
		}
	}
}

func TestCleanStaleLocks(t *testing.T) {
	// Save and restore execCommand
	origExecCommand := execCommand
	defer func() { execCommand = origExecCommand }()

	// Mock tmux to return no active sessions
	execCommand = func(name string, args ...string) interface{ Output() ([]byte, error) } {
		return &mockCmd{output: []byte("")}
	}

	tmpDir := t.TempDir()

	// Create a stale lock
	runtimeDir := filepath.Join(tmpDir, "stale-worker", ".runtime")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatal(err)
	}
	staleLock := LockInfo{
		PID:        999999999,
		AcquiredAt: time.Now(),
		SessionID:  "dead-session",
	}
	data, _ := json.Marshal(staleLock)
	if err := os.WriteFile(filepath.Join(runtimeDir, "agent.lock"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// Create a live lock (current process)
	liveDir := filepath.Join(tmpDir, "live-worker", ".runtime")
	if err := os.MkdirAll(liveDir, 0755); err != nil {
		t.Fatal(err)
	}
	liveLock := LockInfo{
		PID:        os.Getpid(),
		AcquiredAt: time.Now(),
		SessionID:  "live-session",
	}
	data, _ = json.Marshal(liveLock)
	if err := os.WriteFile(filepath.Join(liveDir, "agent.lock"), data, 0644); err != nil {
		t.Fatal(err)
	}

	cleaned, err := CleanStaleLocks(tmpDir)
	if err != nil {
		t.Fatalf("CleanStaleLocks() error = %v", err)
	}

	if cleaned != 1 {
		t.Errorf("CleanStaleLocks() cleaned %d, want 1", cleaned)
	}

	// Verify stale lock is gone
	staleLockPath := filepath.Join(runtimeDir, "agent.lock")
	if _, err := os.Stat(staleLockPath); !os.IsNotExist(err) {
		t.Error("Stale lock file should be removed")
	}

	// Verify live lock still exists
	liveLockPath := filepath.Join(liveDir, "agent.lock")
	if _, err := os.Stat(liveLockPath); err != nil {
		t.Error("Live lock file should still exist")
	}
}

type mockCmd struct {
	output []byte
	err    error
}

func (m *mockCmd) Output() ([]byte, error) {
	return m.output, m.err
}

func TestGetActiveTmuxSessions(t *testing.T) {
	// Save and restore execCommand
	origExecCommand := execCommand
	defer func() { execCommand = origExecCommand }()

	// Mock tmux output
	execCommand = func(name string, args ...string) interface{ Output() ([]byte, error) } {
		return &mockCmd{output: []byte("session1:$1\nsession2:$2\n")}
	}

	sessions := getActiveTmuxSessions()

	// Should contain session names and IDs
	expected := map[string]bool{
		"session1": true,
		"session2": true,
		"$1":       true,
		"$2":       true,
		"%1":       true,
		"%2":       true,
	}

	for _, s := range sessions {
		if !expected[s] {
			t.Errorf("Unexpected session: %s", s)
		}
	}
}

func TestGetActiveTmuxSessionsUsesSocket(t *testing.T) {
	// Save and restore execCommand and tmux socket
	origExecCommand := execCommand
	defer func() { execCommand = origExecCommand }()

	origSocket := tmux.GetDefaultSocket()
	defer tmux.SetDefaultSocket(origSocket)

	// Set a custom socket name
	tmux.SetDefaultSocket("test-town")

	var capturedArgs []string
	execCommand = func(name string, args ...string) interface{ Output() ([]byte, error) } {
		capturedArgs = args
		return &mockCmd{output: []byte("")}
	}

	getActiveTmuxSessions()

	// Verify -L flag was included with the correct socket
	foundSocket := false
	for i, arg := range capturedArgs {
		if arg == "-L" && i+1 < len(capturedArgs) && capturedArgs[i+1] == "test-town" {
			foundSocket = true
			break
		}
	}
	if !foundSocket {
		t.Errorf("getActiveTmuxSessions() args = %v, want -L test-town", capturedArgs)
	}
}

func TestSplitOnColon(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"a:b", []string{"a", "b"}},
		{"abc", []string{"abc"}},
		{"a:b:c", []string{"a", "b:c"}},
		{":b", []string{"", "b"}},
		{"a:", []string{"a", ""}},
	}

	for _, tt := range tests {
		result := splitOnColon(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("splitOnColon(%q) = %v, want %v", tt.input, result, tt.expected)
			continue
		}
		for i := range result {
			if result[i] != tt.expected[i] {
				t.Errorf("splitOnColon(%q)[%d] = %q, want %q", tt.input, i, result[i], tt.expected[i])
			}
		}
	}
}

func TestSplitLines(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"a\nb\nc", []string{"a", "b", "c"}},
		{"a\r\nb\r\nc", []string{"a", "b", "c"}},
		{"single", []string{"single"}},
		{"", []string{}},
		{"a\n", []string{"a"}},
		{"a\nb", []string{"a", "b"}},
	}

	for _, tt := range tests {
		result := splitLines(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("splitLines(%q) = %v, want %v", tt.input, result, tt.expected)
			continue
		}
		for i := range result {
			if result[i] != tt.expected[i] {
				t.Errorf("splitLines(%q)[%d] = %q, want %q", tt.input, i, result[i], tt.expected[i])
			}
		}
	}
}

func TestDetectCollisions(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a stale lock
	runtimeDir := filepath.Join(tmpDir, "stale-worker", ".runtime")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatal(err)
	}
	staleLock := LockInfo{
		PID:        999999999,
		AcquiredAt: time.Now(),
		SessionID:  "dead-session",
	}
	data, _ := json.Marshal(staleLock)
	if err := os.WriteFile(filepath.Join(runtimeDir, "agent.lock"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// Create an orphaned lock (live PID but session not in active list)
	orphanDir := filepath.Join(tmpDir, "orphan-worker", ".runtime")
	if err := os.MkdirAll(orphanDir, 0755); err != nil {
		t.Fatal(err)
	}
	orphanLock := LockInfo{
		PID:        os.Getpid(), // Live PID
		AcquiredAt: time.Now(),
		SessionID:  "orphan-session", // Not in active list
	}
	data, _ = json.Marshal(orphanLock)
	if err := os.WriteFile(filepath.Join(orphanDir, "agent.lock"), data, 0644); err != nil {
		t.Fatal(err)
	}

	activeSessions := []string{"active-session-1", "active-session-2"}
	collisions := DetectCollisions(tmpDir, activeSessions)

	if len(collisions) != 2 {
		t.Errorf("DetectCollisions() found %d collisions, want 2: %v", len(collisions), collisions)
	}

	// Verify we found both issues
	foundStale := false
	foundOrphan := false
	for _, c := range collisions {
		if contains(c, "stale lock") {
			foundStale = true
		}
		if contains(c, "orphaned lock") {
			foundOrphan = true
		}
	}

	if !foundStale {
		t.Error("DetectCollisions() did not find stale lock")
	}
	if !foundOrphan {
		t.Error("DetectCollisions() did not find orphaned lock")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestLock_ReleaseNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	workerDir := filepath.Join(tmpDir, "worker")
	if err := os.MkdirAll(workerDir, 0755); err != nil {
		t.Fatal(err)
	}

	l := New(workerDir)

	// Releasing a non-existent lock should not error
	if err := l.Release(); err != nil {
		t.Errorf("Release() non-existent: error = %v, want nil", err)
	}
}

func TestLock_CheckCleansUpStaleLock(t *testing.T) {
	tmpDir := t.TempDir()
	workerDir := filepath.Join(tmpDir, "worker")
	runtimeDir := filepath.Join(workerDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a stale lock
	staleLock := LockInfo{
		PID:        999999999,
		AcquiredAt: time.Now(),
		SessionID:  "dead",
	}
	data, _ := json.Marshal(staleLock)
	lockPath := filepath.Join(runtimeDir, "agent.lock")
	if err := os.WriteFile(lockPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	l := New(workerDir)

	// Check should clean up stale lock and return nil
	if err := l.Check(); err != nil {
		t.Errorf("Check() with stale lock: error = %v, want nil", err)
	}

	// Lock file should be removed
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("Check() should have removed stale lock file")
	}
}
