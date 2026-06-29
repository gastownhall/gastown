package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/steveyegge/gastown/internal/tmux"
)

func TestDefaultConfig(t *testing.T) {
	townRoot := "/tmp/test-town"
	config := DefaultConfig(townRoot)

	if config.HeartbeatInterval != 5*time.Minute {
		t.Errorf("expected HeartbeatInterval 5m, got %v", config.HeartbeatInterval)
	}
	if config.TownRoot != townRoot {
		t.Errorf("expected TownRoot %q, got %q", townRoot, config.TownRoot)
	}
	if config.LogFile != filepath.Join(townRoot, "daemon", "daemon.log") {
		t.Errorf("expected LogFile in daemon dir, got %q", config.LogFile)
	}
	if config.PidFile != filepath.Join(townRoot, "daemon", "daemon.pid") {
		t.Errorf("expected PidFile in daemon dir, got %q", config.PidFile)
	}
}

func TestDaemonPathCandidatesIncludesLaunchdToolDirs(t *testing.T) {
	home := filepath.Join("Users", "alice")
	exePath := filepath.Join("opt", "homebrew", "bin", "gt")

	got := daemonPathCandidates(home, exePath)
	for _, want := range []string{
		filepath.Dir(exePath),
		filepath.Join(home, ".local/bin"),
		filepath.Join(home, "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
	} {
		if !slices.Contains(got, want) {
			t.Fatalf("daemonPathCandidates(%q, %q) missing %q; got %v", home, exePath, want, got)
		}
	}
}

func TestCleanupLegacySocketSessionsRunsOnce(t *testing.T) {
	oldCleanup := cleanupLegacySocketsForDaemon
	t.Cleanup(func() { cleanupLegacySocketsForDaemon = oldCleanup })

	var calls int
	var gotRoot string
	cleanupLegacySocketsForDaemon = func(townRoot string) (int, int) {
		calls++
		gotRoot = townRoot
		return 1, 2
	}

	townRoot := t.TempDir()
	d := &Daemon{
		config: DefaultConfig(townRoot),
		logger: log.New(io.Discard, "", 0),
	}
	d.cleanupLegacySocketSessions()
	if calls != 1 {
		t.Fatalf("cleanup calls after first invocation = %d, want 1", calls)
	}
	if gotRoot != townRoot {
		t.Fatalf("cleanup townRoot = %q, want %q", gotRoot, townRoot)
	}

	d.cleanupLegacySocketSessions()
	if calls != 1 {
		t.Fatalf("cleanup calls after second invocation = %d, want 1", calls)
	}
}

func TestSyncWorkspaceRefusesTownRootWorkDir(t *testing.T) {
	townRoot := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = townRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for _, args := range [][]string{{"config", "user.email", "test@test.com"}, {"config", "user.name", "Test User"}} {
		cmd = exec.Command("git", args...)
		cmd.Dir = townRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(townRoot, "README.md"), []byte("# Town\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	cmd = exec.Command("git", "add", "README.md")
	cmd.Dir = townRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = townRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	writeDaemonTownFile(t, townRoot, "mayor/town.json", `{"name":"test-town"}\n`)
	writeDaemonTownFile(t, townRoot, "mayor/rigs.json", `{"rigs":[]}\n`)
	writeDaemonTownFile(t, townRoot, ".dolt-data/gastown/.dolt/noms/manifest", "manifest\n")
	writeDaemonTownFile(t, townRoot, ".runtime/sentinel", "runtime\n")
	writeDaemonTownFile(t, townRoot, ".beads/metadata.json", `{"prefix":"hq"}\n`)
	writeDaemonTownFile(t, townRoot, "daemon/daemon.pid", "12345\n")
	writeDaemonTownFile(t, townRoot, "user-work.txt", "user work\n")

	headBefore := daemonGitOutput(t, townRoot, "rev-parse", "HEAD")
	filesBefore := snapshotDaemonTownFiles(t, townRoot)
	var logBuf bytes.Buffer
	d := &Daemon{
		config: DefaultConfig(townRoot),
		logger: log.New(&logBuf, "", 0),
	}
	d.syncWorkspace(townRoot)

	if !strings.Contains(logBuf.String(), "refusing daemon git sync") {
		t.Fatalf("log = %q, want refusal", logBuf.String())
	}
	if got := daemonGitOutput(t, townRoot, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("HEAD changed: got %s, want %s", got, headBefore)
	}
	assertDaemonTownFilesPreserved(t, townRoot, filesBefore)

	nestedRig := filepath.Join(townRoot, "gastown")
	if err := os.MkdirAll(nestedRig, 0755); err != nil {
		t.Fatalf("mkdir nested rig: %v", err)
	}
	logBuf.Reset()
	d.syncWorkspace(nestedRig)
	if !strings.Contains(logBuf.String(), "refusing daemon git sync") {
		t.Fatalf("nested log = %q, want refusal", logBuf.String())
	}
	if got := daemonGitOutput(t, townRoot, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("HEAD changed after nested sync: got %s, want %s", got, headBefore)
	}
	assertDaemonTownFilesPreserved(t, townRoot, filesBefore)
}

func TestEnsureRefineryRunningSafetyStoppedDoesNotSpawn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock bd/tmux scripts use POSIX shell")
	}
	townRoot := t.TempDir()
	writeDaemonTownFile(t, townRoot, "mayor/town.json", `{"name":"test"}`)
	writeDaemonTownFile(t, townRoot, ".beads/metadata.json", `{"prefix":"hq"}`)
	writeDaemonTownFile(t, townRoot, "events/refinery/pending.event", "{}")
	if err := os.MkdirAll(filepath.Join(townRoot, "testrig"), 0o755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "commands.log")
	writeDaemonSafetyStopMockBD(t, binDir, logPath)
	writeDaemonSafetyStopMockTmux(t, binDir, logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	d := &Daemon{
		config: DefaultConfig(townRoot),
		logger: log.New(io.Discard, "", 0),
		tmux:   tmux.NewTmux(),
	}
	d.ensureRefineryRunning("testrig")

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read command log: %v", err)
	}
	if strings.Contains(string(logData), "new-session") {
		t.Fatalf("daemon spawned refinery despite safety stop; log:\n%s", logData)
	}
}

func writeDaemonSafetyStopMockBD(t *testing.T, binDir, logPath string) {
	t.Helper()
	script := `#!/bin/sh
printf 'bd %s\n' "$*" >> "` + logPath + `"
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done
case "$cmd" in
  version)
    echo "bd test"
    ;;
  show)
    printf '%s\n' '[{"id":"gt-testrig-refinery","title":"Refinery","issue_type":"task","labels":["gt:agent","safety_stop:hq-vmrwr"],"status":"open","description":"role_type: refinery\nrig: testrig\nagent_state: idle"}]'
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
}

func writeDaemonSafetyStopMockTmux(t *testing.T, binDir, logPath string) {
	t.Helper()
	script := `#!/bin/sh
printf 'tmux %s\n' "$*" >> "` + logPath + `"
case "$1" in
  has-session)
    exit 1
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
}

func writeDaemonTownFile(t *testing.T, root, rel, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func daemonGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func snapshotDaemonTownFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	for _, rel := range []string{
		"mayor/town.json",
		"mayor/rigs.json",
		".dolt-data/gastown/.dolt/noms/manifest",
		".runtime/sentinel",
		".beads/metadata.json",
		"daemon/daemon.pid",
		"user-work.txt",
	} {
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		files[rel] = string(contents)
	}
	return files
}

func assertDaemonTownFilesPreserved(t *testing.T, root string, before map[string]string) {
	t.Helper()
	for rel, want := range before {
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read preserved %s: %v", rel, err)
		}
		if got := string(contents); got != want {
			t.Fatalf("%s changed: got %q, want %q", rel, got, want)
		}
	}
}

func TestStateFile(t *testing.T) {
	townRoot := "/tmp/test-town"
	expected := filepath.Join(townRoot, "daemon", "state.json")
	result := StateFile(townRoot)

	if result != expected {
		t.Errorf("StateFile(%q) = %q, expected %q", townRoot, result, expected)
	}
}

func TestLoadState_NonExistent(t *testing.T) {
	// Create temp dir that doesn't have a state file
	tmpDir, err := os.MkdirTemp("", "daemon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	state, err := LoadState(tmpDir)
	if err != nil {
		t.Errorf("LoadState should not error for missing file, got %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil state")
	}
	if state.Running {
		t.Error("expected Running=false for empty state")
	}
	if state.PID != 0 {
		t.Errorf("expected PID=0 for empty state, got %d", state.PID)
	}
}

func TestLoadState_ExistingFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "daemon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create daemon directory
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a state file
	startTime := time.Now().Truncate(time.Second)
	testState := &State{
		Running:        true,
		PID:            12345,
		StartedAt:      startTime,
		LastHeartbeat:  startTime,
		HeartbeatCount: 42,
	}

	data, err := json.MarshalIndent(testState, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(daemonDir, "state.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// Load and verify
	loaded, err := LoadState(tmpDir)
	if err != nil {
		t.Fatalf("LoadState error: %v", err)
	}
	if !loaded.Running {
		t.Error("expected Running=true")
	}
	if loaded.PID != 12345 {
		t.Errorf("expected PID=12345, got %d", loaded.PID)
	}
	if loaded.HeartbeatCount != 42 {
		t.Errorf("expected HeartbeatCount=42, got %d", loaded.HeartbeatCount)
	}
}

func TestLoadState_InvalidJSON(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "daemon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create daemon directory with invalid JSON
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(daemonDir, "state.json"), []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadState(tmpDir)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestSaveState(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "daemon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	state := &State{
		Running:        true,
		PID:            9999,
		StartedAt:      time.Now(),
		LastHeartbeat:  time.Now(),
		HeartbeatCount: 100,
	}

	// SaveState should create daemon directory if needed
	if err := SaveState(tmpDir, state); err != nil {
		t.Fatalf("SaveState error: %v", err)
	}

	// Verify file exists
	stateFile := StateFile(tmpDir)
	if _, err := os.Stat(stateFile); err != nil {
		t.Errorf("state file should exist: %v", err)
	}

	// Verify contents
	loaded, err := LoadState(tmpDir)
	if err != nil {
		t.Fatalf("LoadState error: %v", err)
	}
	if loaded.PID != 9999 {
		t.Errorf("expected PID=9999, got %d", loaded.PID)
	}
	if loaded.HeartbeatCount != 100 {
		t.Errorf("expected HeartbeatCount=100, got %d", loaded.HeartbeatCount)
	}
}

func TestSaveLoadState_Roundtrip(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "daemon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	original := &State{
		Running:        true,
		PID:            54321,
		StartedAt:      time.Now().Truncate(time.Second),
		LastHeartbeat:  time.Now().Truncate(time.Second),
		HeartbeatCount: 1000,
	}

	if err := SaveState(tmpDir, original); err != nil {
		t.Fatalf("SaveState error: %v", err)
	}

	loaded, err := LoadState(tmpDir)
	if err != nil {
		t.Fatalf("LoadState error: %v", err)
	}

	if loaded.Running != original.Running {
		t.Errorf("Running mismatch: got %v, want %v", loaded.Running, original.Running)
	}
	if loaded.PID != original.PID {
		t.Errorf("PID mismatch: got %d, want %d", loaded.PID, original.PID)
	}
	if loaded.HeartbeatCount != original.HeartbeatCount {
		t.Errorf("HeartbeatCount mismatch: got %d, want %d", loaded.HeartbeatCount, original.HeartbeatCount)
	}
	// Time comparison with truncation to handle JSON serialization
	if !loaded.StartedAt.Truncate(time.Second).Equal(original.StartedAt) {
		t.Errorf("StartedAt mismatch: got %v, want %v", loaded.StartedAt, original.StartedAt)
	}
}

func TestListPolecatWorktrees_SkipsHiddenDirs(t *testing.T) {
	tmpDir := t.TempDir()
	polecatsDir := filepath.Join(tmpDir, "some-rig", "polecats")

	if err := os.MkdirAll(filepath.Join(polecatsDir, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(polecatsDir, "furiosa"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(polecatsDir, "not-a-dir.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	polecats, err := listPolecatWorktrees(polecatsDir)
	if err != nil {
		t.Fatalf("listPolecatWorktrees returned error: %v", err)
	}

	if slices.Contains(polecats, ".claude") {
		t.Fatalf("expected hidden dir .claude to be ignored, got %v", polecats)
	}
	if !slices.Contains(polecats, "furiosa") {
		t.Fatalf("expected furiosa to be included, got %v", polecats)
	}
}

// NOTE: TestIsWitnessSession removed - isWitnessSession function was deleted
// as part of ZFC cleanup. Witness poking is now Deacon's responsibility.

func TestLifecycleAction_Constants(t *testing.T) {
	// Verify constants have expected string values
	if ActionCycle != "cycle" {
		t.Errorf("expected ActionCycle='cycle', got %q", ActionCycle)
	}
	if ActionRestart != "restart" {
		t.Errorf("expected ActionRestart='restart', got %q", ActionRestart)
	}
	if ActionShutdown != "shutdown" {
		t.Errorf("expected ActionShutdown='shutdown', got %q", ActionShutdown)
	}
}

func TestLifecycleRequest_Serialization(t *testing.T) {
	request := &LifecycleRequest{
		From:      "mayor",
		Action:    ActionCycle,
		Timestamp: time.Now().Truncate(time.Second),
	}

	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var loaded LifecycleRequest
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if loaded.From != request.From {
		t.Errorf("From mismatch: got %q, want %q", loaded.From, request.From)
	}
	if loaded.Action != request.Action {
		t.Errorf("Action mismatch: got %q, want %q", loaded.Action, request.Action)
	}
}

func TestIsShutdownInProgress_NoLockFile(t *testing.T) {
	tmpDir := t.TempDir()

	d := &Daemon{
		config: &Config{TownRoot: tmpDir},
	}

	// No lock file exists - should return false
	if d.isShutdownInProgress() {
		t.Error("expected false when lock file doesn't exist")
	}
}

func TestIsShutdownInProgress_StaleLockFile(t *testing.T) {
	tmpDir := t.TempDir()
	lockDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(lockDir, "shutdown.lock")

	// Create a stale lock file (not actually locked)
	if err := os.WriteFile(lockPath, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{
		config: &Config{TownRoot: tmpDir},
	}

	// File exists but not locked - should return false
	if d.isShutdownInProgress() {
		t.Error("expected false when lock file exists but is not locked")
	}

	// File should still exist - flock files are never removed to prevent
	// a race where concurrent callers lock different inodes
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Error("expected lock file to be preserved (flock files should not be removed)")
	}
}

func TestIsShutdownInProgress_ActiveLock(t *testing.T) {
	tmpDir := t.TempDir()
	lockDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(lockDir, "shutdown.lock")

	// Create and hold the lock (simulating active shutdown)
	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}
	if !locked {
		t.Fatal("expected to acquire lock")
	}
	defer func() { _ = lock.Unlock() }()

	d := &Daemon{
		config: &Config{TownRoot: tmpDir},
	}

	// File exists and is locked - should return true
	if !d.isShutdownInProgress() {
		t.Error("expected true when lock file is actively held")
	}

	// File should still exist (we're still holding the lock)
	if _, err := os.Stat(lockPath); err != nil {
		t.Errorf("lock file should still exist: %v", err)
	}
}

// TestDaemon_StartsManagerAndScanner verifies that the convoy manager (event-driven + stranded scan)
// starts and stops correctly when used as the daemon does.
func TestDaemon_StartsManagerAndScanner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	manager := NewConvoyManager(townRoot, func(string, ...interface{}) {}, "gt", 1*time.Hour, nil, nil, nil)
	if err := manager.Start(); err != nil {
		t.Fatalf("manager Start: %v", err)
	}
	manager.Stop()
}

// TestDaemon_StopsManagerAndScanner verifies that stopping the convoy manager
// completes without blocking (e.g. context cancellation works).
func TestDaemon_StopsManagerAndScanner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	manager := NewConvoyManager(townRoot, func(string, ...interface{}) {}, "gt", 1*time.Hour, nil, nil, nil)
	if err := manager.Start(); err != nil {
		t.Fatalf("manager Start: %v", err)
	}

	done := make(chan struct{})
	go func() {
		manager.Stop()
		close(done)
	}()
	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not complete within 5s")
	}
}

// TestIsRunningFromPID_StalePIDReturnsNoError verifies that isRunningFromPID
// returns (false, 0, nil) — not an error — when it finds and removes a stale
// PID file. This is the fix for GH#2107: `gt daemon start` was treating the
// stale cleanup as an error, showing help text instead of starting the daemon.
func TestIsRunningFromPID_StalePIDReturnsNoError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a PID file pointing to a process that doesn't exist.
	// PID 2^22-1 (4194303) is extremely unlikely to be in use.
	stalePID := 4194303
	pidFile := filepath.Join(daemonDir, "daemon.pid")
	if _, err := writePIDFile(pidFile, stalePID); err != nil {
		t.Fatal(err)
	}

	running, pid, err := isRunningFromPID(tmpDir)
	if err != nil {
		t.Errorf("isRunningFromPID should not return error for stale PID, got: %v", err)
	}
	if running {
		t.Error("expected running=false for stale PID")
	}
	if pid != 0 {
		t.Errorf("expected pid=0, got %d", pid)
	}

	// PID file should have been removed
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Error("expected stale PID file to be removed")
	}
}

// TestIsRunningFromPID_NoPIDFile verifies clean return when no PID file exists.
func TestIsRunningFromPID_NoPIDFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	running, pid, err := isRunningFromPID(tmpDir)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if running {
		t.Error("expected running=false")
	}
	if pid != 0 {
		t.Errorf("expected pid=0, got %d", pid)
	}
}

// TestIsRunningFromPID_LiveProcess verifies detection of a live process.
func TestIsRunningFromPID_LiveProcess(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Use our own PID — guaranteed alive
	pidFile := filepath.Join(daemonDir, "daemon.pid")
	if _, err := writePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatal(err)
	}

	running, pid, err := isRunningFromPID(tmpDir)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !running {
		t.Error("expected running=true for live process")
	}
	if pid != os.Getpid() {
		t.Errorf("expected pid=%d, got %d", os.Getpid(), pid)
	}
}

func TestHasPendingEvents_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	eventDir := filepath.Join(tmpDir, "events", "refinery")
	if err := os.MkdirAll(eventDir, 0755); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{config: &Config{TownRoot: tmpDir}}

	if d.hasPendingEvents("refinery") {
		t.Error("expected false for empty event directory")
	}
}

func TestHasPendingEvents_MissingDir(t *testing.T) {
	tmpDir := t.TempDir()

	d := &Daemon{config: &Config{TownRoot: tmpDir}}

	if d.hasPendingEvents("refinery") {
		t.Error("expected false when event directory doesn't exist")
	}
}

func TestHasPendingEvents_WithEventFiles(t *testing.T) {
	tmpDir := t.TempDir()
	eventDir := filepath.Join(tmpDir, "events", "refinery")
	if err := os.MkdirAll(eventDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create an event file
	eventFile := filepath.Join(eventDir, "1234567890-1-12345.event")
	if err := os.WriteFile(eventFile, []byte(`{"type":"MQ_SUBMIT"}`), 0644); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{config: &Config{TownRoot: tmpDir}}

	if !d.hasPendingEvents("refinery") {
		t.Error("expected true when .event files exist")
	}
}

func TestHasPendingEvents_IgnoresNonEventFiles(t *testing.T) {
	tmpDir := t.TempDir()
	eventDir := filepath.Join(tmpDir, "events", "refinery")
	if err := os.MkdirAll(eventDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a non-event file (e.g., .tmp or .lock)
	if err := os.WriteFile(filepath.Join(eventDir, "temp.lock"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{config: &Config{TownRoot: tmpDir}}

	if d.hasPendingEvents("refinery") {
		t.Error("expected false when only non-.event files exist")
	}
}

// TestIsRigOperational_FailSafeOnDoltUnavailable verifies that when Dolt is
// unavailable and we can't check the rig bead for docked status, we fail-safe
// by assuming the rig is NOT operational. This prevents wasting API credits
// starting witnesses for potentially docked rigs. (Regression test for
// bug where witnesses started for docked rigs during Dolt outage)
func TestIsRigOperational_FailSafeOnDoltUnavailable(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a minimal rig structure without a beads database
	rigName := "testrig"
	rigPath := filepath.Join(tmpDir, rigName)
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatal(err)
	}

	// Create config.json with a prefix
	configPath := filepath.Join(rigPath, "config.json")
	configJSON := `{"beads": {"prefix": "tr"}}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Create mayor/rig/.beads directory but NO Dolt database
	// This simulates Dolt being down or database not accessible
	mayorBeads := filepath.Join(rigPath, "mayor", "rig", ".beads")
	if err := os.MkdirAll(mayorBeads, 0755); err != nil {
		t.Fatal(err)
	}

	// Create town-level .beads with routes.jsonl
	townBeads := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatal(err)
	}
	routesContent := `{"prefix":"tr-","path":"testrig/mayor/rig"}`
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create daemon with no Dolt server running
	d := &Daemon{
		config: &Config{
			TownRoot: tmpDir,
		},
		logger: log.New(io.Discard, "", 0), // Suppress log output
	}

	// When Dolt is unavailable, isRigOperational should return false
	// (fail-safe: assume not operational rather than risk starting docked rig)
	operational, reason := d.isRigOperational(rigName)
	if operational {
		t.Error("isRigOperational should return false when Dolt is unavailable (fail-safe)")
	}
	if reason == "" {
		t.Error("isRigOperational should provide a reason when returning false")
	}
	if !strings.Contains(reason, "Dolt unavailable") && !strings.Contains(reason, "cannot verify") {
		t.Errorf("reason should mention Dolt unavailable, got: %q", reason)
	}
}

// TestIsRigOperational_DockedRig verifies that docked rigs are correctly
// identified as not operational.
func TestIsRigOperational_DockedRig(t *testing.T) {
	tmpDir := t.TempDir()

	// Create rig with docked label on rig bead
	rigName := "dockedrig"
	rigPath := filepath.Join(tmpDir, rigName)
	if err := os.MkdirAll(filepath.Join(rigPath, "mayor", "rig", ".beads"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create config.json
	configPath := filepath.Join(rigPath, "config.json")
	configJSON := `{"beads": {"prefix": "dr"}}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Create town-level .beads with routes.jsonl
	townBeads := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatal(err)
	}
	routesContent := `{"prefix":"dr-","path":"dockedrig/mayor/rig"}`
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{
		config: &Config{
			TownRoot: tmpDir,
		},
		logger: log.New(io.Discard, "", 0),
	}

	// Without a rig bead, should fail-safe to not operational
	operational, reason := d.isRigOperational(rigName)
	if operational {
		t.Error("isRigOperational should return false when rig bead is missing")
	}
	t.Logf("Docked rig check returned: operational=%v, reason=%q", operational, reason)
}
