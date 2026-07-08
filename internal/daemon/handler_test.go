package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/dog"
	"github.com/steveyegge/gastown/internal/tmux"
)

// testHandlerDaemon creates a minimal Daemon with a logger for handler tests.
func testHandlerDaemon(t *testing.T, townRoot string) *Daemon {
	t.Helper()
	return &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(os.Stderr, "test: ", log.LstdFlags),
	}
}

func requireTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
}

// testSetupDogState creates a dog directory with a .dog.json state file.
func testSetupDogState(t *testing.T, townRoot, name string, state dog.State, lastActive time.Time) {
	t.Helper()

	kennelDir := filepath.Join(townRoot, "deacon", "dogs", name)
	if err := os.MkdirAll(kennelDir, 0755); err != nil {
		t.Fatalf("Failed to create kennel dir for %s: %v", name, err)
	}

	ds := &dog.DogState{
		Name:       name,
		State:      state,
		LastActive: lastActive,
		Worktrees:  map[string]string{},
		CreatedAt:  lastActive,
		UpdatedAt:  lastActive,
	}

	data, err := json.MarshalIndent(ds, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal dog state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(kennelDir, ".dog.json"), data, 0644); err != nil {
		t.Fatalf("Failed to write dog state: %v", err)
	}
}

// testDogExists checks if a dog directory exists in the kennel.
func testDogExists(townRoot, name string) bool {
	_, err := os.Stat(filepath.Join(townRoot, "deacon", "dogs", name, ".dog.json"))
	return err == nil
}

// testSetupWorkingDogState creates a working dog with a work assignment.
func testSetupWorkingDogState(t *testing.T, townRoot, name, work string, lastActive time.Time) {
	t.Helper()

	kennelDir := filepath.Join(townRoot, "deacon", "dogs", name)
	if err := os.MkdirAll(kennelDir, 0755); err != nil {
		t.Fatalf("Failed to create kennel dir for %s: %v", name, err)
	}

	ds := &dog.DogState{
		Name:       name,
		State:      dog.StateWorking,
		Work:       work,
		LastActive: lastActive,
		Worktrees:  map[string]string{},
		CreatedAt:  lastActive,
		UpdatedAt:  lastActive,
	}

	data, err := json.MarshalIndent(ds, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal dog state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(kennelDir, ".dog.json"), data, 0644); err != nil {
		t.Fatalf("Failed to write dog state: %v", err)
	}
}

func TestDetectStaleWorkingDogs_ClearsStaleWorkers(t *testing.T) {
	requireTmux(t)

	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Dog working for 3 hours with no activity — should be cleared.
	testSetupWorkingDogState(t, townRoot, "stale", constants.MolConvoyFeed, time.Now().Add(-3*time.Hour))

	d.detectStaleWorkingDogs(mgr, sm, &config.DaemonThresholds{})

	dg, err := mgr.Get("stale")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if dg.State != dog.StateIdle {
		t.Errorf("stale dog state = %q, want idle", dg.State)
	}
	if dg.Work != "" {
		t.Errorf("stale dog work = %q, want empty", dg.Work)
	}
}

func TestDetectStaleWorkingDogs_KillsSessionBeforeClearing(t *testing.T) {
	requireTmux(t)

	oldSocket := tmux.GetDefaultSocket()
	socketName := fmt.Sprintf("gt-test-dog-stale-%d", time.Now().UnixNano())
	tmux.SetDefaultSocket(socketName)
	t.Cleanup(func() { tmux.SetDefaultSocket(oldSocket) })

	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	testSetupWorkingDogState(t, townRoot, "stale", constants.MolConvoyFeed, time.Now().Add(-3*time.Hour))

	sessionName := sm.SessionName("stale")
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession(%q): %v", sessionName, err)
	}
	t.Cleanup(func() { _ = tm.KillSession(sessionName) })

	d.detectStaleWorkingDogs(mgr, sm, &config.DaemonThresholds{})

	dg, err := mgr.Get("stale")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if dg.State != dog.StateIdle {
		t.Errorf("stale dog state = %q, want idle", dg.State)
	}
	if dg.Work != "" {
		t.Errorf("stale dog work = %q, want empty", dg.Work)
	}
	if has, err := tm.HasSession(sessionName); err != nil {
		t.Fatalf("HasSession(%q): %v", sessionName, err)
	} else if has {
		t.Errorf("stale dog session %q still exists after cleanup", sessionName)
	}
}

func TestDetectStaleWorkingDogs_SkipsRecentWorkers(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Dog working for 30 minutes — should NOT be cleared.
	testSetupWorkingDogState(t, townRoot, "active", constants.MolConvoyFeed, time.Now().Add(-30*time.Minute))

	d.detectStaleWorkingDogs(mgr, sm, &config.DaemonThresholds{})

	dg, err := mgr.Get("active")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if dg.State != dog.StateWorking {
		t.Errorf("active dog state = %q, want working", dg.State)
	}
	if dg.Work != constants.MolConvoyFeed {
		t.Errorf("active dog work = %q, want %s", dg.Work, constants.MolConvoyFeed)
	}
}

func TestDetectStaleWorkingDogs_SkipsIdleDogs(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Idle dog with old last_active — should NOT be touched by this function.
	testSetupDogState(t, townRoot, "idle-old", dog.StateIdle, time.Now().Add(-5*time.Hour))

	d.detectStaleWorkingDogs(mgr, sm, &config.DaemonThresholds{})

	dg, err := mgr.Get("idle-old")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if dg.State != dog.StateIdle {
		t.Errorf("idle dog state = %q, want idle", dg.State)
	}
}

func TestDetectStaleWorkingDogs_EmptyKennel(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Should not panic or error with empty kennel.
	d.detectStaleWorkingDogs(mgr, sm, &config.DaemonThresholds{})
}

func TestDetectStaleWorkingDogs_Constants(t *testing.T) {
	if staleWorkingTimeout != 2*time.Hour {
		t.Errorf("staleWorkingTimeout = %v, want 2h", staleWorkingTimeout)
	}
}

func TestReapIdleDogs_SkipsWorkingDogs(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Create a working dog with old LastActive — should NOT be reaped.
	testSetupDogState(t, townRoot, "worker", dog.StateWorking, time.Now().Add(-5*time.Hour))

	d.reapIdleDogs(mgr, sm, &config.DaemonThresholds{})

	if !testDogExists(townRoot, "worker") {
		t.Error("working dog should not be removed by reapIdleDogs")
	}
}

func TestReapIdleDogs_SkipsRecentlyActiveDogs(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Create idle dogs that were active recently — should NOT be reaped.
	for i := 0; i < 6; i++ {
		name := "recent-" + string(rune('a'+i))
		testSetupDogState(t, townRoot, name, dog.StateIdle, time.Now().Add(-30*time.Minute))
	}

	d.reapIdleDogs(mgr, sm, &config.DaemonThresholds{})

	// All dogs should still exist.
	dogs, err := mgr.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(dogs) != 6 {
		t.Errorf("expected 6 dogs after reap, got %d", len(dogs))
	}
}

func TestReapIdleDogs_RemovesLongIdleDogsWhenPoolOversized(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows: requires tmux")
	}
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Create 6 idle dogs: 4 recent, 2 long-idle.
	// Pool is 6 > maxDogPoolSize(4), so long-idle dogs should be removed.
	for i := 0; i < 4; i++ {
		name := "recent-" + string(rune('a'+i))
		testSetupDogState(t, townRoot, name, dog.StateIdle, time.Now().Add(-10*time.Minute))
	}
	testSetupDogState(t, townRoot, "old-1", dog.StateIdle, time.Now().Add(-5*time.Hour))
	testSetupDogState(t, townRoot, "old-2", dog.StateIdle, time.Now().Add(-6*time.Hour))

	d.reapIdleDogs(mgr, sm, &config.DaemonThresholds{})

	// Long-idle dogs should be removed, recent ones kept.
	dogs, err := mgr.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}

	if len(dogs) > maxDogPoolSize {
		t.Errorf("expected pool trimmed to at most %d, got %d", maxDogPoolSize, len(dogs))
	}

	// Verify the old dogs were removed.
	if testDogExists(townRoot, "old-1") {
		t.Error("old-1 should have been removed")
	}
	if testDogExists(townRoot, "old-2") {
		t.Error("old-2 should have been removed")
	}
}

func TestReapIdleDogs_DoesNotRemoveWhenPoolAtMaxSize(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Create exactly maxDogPoolSize idle dogs, all long-idle.
	// Pool is NOT oversized, so none should be removed.
	for i := 0; i < maxDogPoolSize; i++ {
		name := "idle-" + string(rune('a'+i))
		testSetupDogState(t, townRoot, name, dog.StateIdle, time.Now().Add(-5*time.Hour))
	}

	d.reapIdleDogs(mgr, sm, &config.DaemonThresholds{})

	dogs, err := mgr.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(dogs) != maxDogPoolSize {
		t.Errorf("expected %d dogs (pool not oversized), got %d", maxDogPoolSize, len(dogs))
	}
}

func TestReapIdleDogs_StopsRemovingAtMaxPoolSize(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows: requires tmux")
	}
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Create 7 idle dogs, all long-idle.
	// Should remove 3 to get down to maxDogPoolSize(4).
	for i := 0; i < 7; i++ {
		name := "dog-" + string(rune('a'+i))
		testSetupDogState(t, townRoot, name, dog.StateIdle, time.Now().Add(-5*time.Hour))
	}

	d.reapIdleDogs(mgr, sm, &config.DaemonThresholds{})

	dogs, err := mgr.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(dogs) > maxDogPoolSize {
		t.Errorf("expected pool trimmed to %d, got %d", maxDogPoolSize, len(dogs))
	}
}

func TestReapIdleDogs_MixedStates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows: requires tmux")
	}
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// 2 working + 3 recent idle + 2 long-idle = 7 total.
	// Pool is oversized (7 > 4). Only long-idle IDLE dogs should be removed.
	// Working dogs are never touched.
	testSetupDogState(t, townRoot, "worker-a", dog.StateWorking, time.Now().Add(-5*time.Hour))
	testSetupDogState(t, townRoot, "worker-b", dog.StateWorking, time.Now().Add(-5*time.Hour))
	testSetupDogState(t, townRoot, "recent-a", dog.StateIdle, time.Now().Add(-10*time.Minute))
	testSetupDogState(t, townRoot, "recent-b", dog.StateIdle, time.Now().Add(-10*time.Minute))
	testSetupDogState(t, townRoot, "recent-c", dog.StateIdle, time.Now().Add(-10*time.Minute))
	testSetupDogState(t, townRoot, "old-a", dog.StateIdle, time.Now().Add(-5*time.Hour))
	testSetupDogState(t, townRoot, "old-b", dog.StateIdle, time.Now().Add(-6*time.Hour))

	d.reapIdleDogs(mgr, sm, &config.DaemonThresholds{})

	// Working dogs must survive.
	if !testDogExists(townRoot, "worker-a") {
		t.Error("worker-a should not be removed")
	}
	if !testDogExists(townRoot, "worker-b") {
		t.Error("worker-b should not be removed")
	}

	// Long-idle dogs should be removed (pool was 7 > 4).
	if testDogExists(townRoot, "old-a") {
		t.Error("old-a should have been removed")
	}
	if testDogExists(townRoot, "old-b") {
		t.Error("old-b should have been removed")
	}

	// Recent idle dogs should survive.
	if !testDogExists(townRoot, "recent-a") {
		t.Error("recent-a should not be removed")
	}
}

func TestReapIdleDogs_EmptyKennel(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	// Should not panic or error with empty kennel.
	d.reapIdleDogs(mgr, sm, &config.DaemonThresholds{})
}

func TestReapIdleDogs_Constants(t *testing.T) {
	if dogIdleSessionTimeout != 1*time.Hour {
		t.Errorf("dogIdleSessionTimeout = %v, want 1h", dogIdleSessionTimeout)
	}
	if dogIdleRemoveTimeout != 4*time.Hour {
		t.Errorf("dogIdleRemoveTimeout = %v, want 4h", dogIdleRemoveTimeout)
	}
	if maxDogPoolSize != 4 {
		t.Errorf("maxDogPoolSize = %d, want 4", maxDogPoolSize)
	}
}

func TestDispatchPlugins_SkipsManualGatePlugin(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	pluginDir := filepath.Join(townRoot, "plugins", "test-manual")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	pluginMD := "+++\nname = \"test-manual\"\ndescription = \"manual gate plugin\"\n\n[gate]\ntype = \"manual\"\n+++\n\n# Instructions\n"
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.md"), []byte(pluginMD), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	testSetupDogState(t, townRoot, "idle-dog", dog.StateIdle, time.Now().Add(-10*time.Minute))

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	d.dispatchPlugins(mgr, sm, rigsConfig)

	dg, err := mgr.Get("idle-dog")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if dg.State != dog.StateIdle {
		t.Errorf("dog state = %q, want idle (manual-gate plugin must not auto-dispatch)", dg.State)
	}
	if dg.Work != "" {
		t.Errorf("dog work = %q, want empty (manual-gate plugin must not auto-dispatch)", dg.Work)
	}
}
func TestFindDispatchableDog_PicksFirstIdleWhenNoSessionsLive(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	testSetupDogState(t, townRoot, "alpha", dog.StateIdle, time.Now())
	testSetupDogState(t, townRoot, "bravo", dog.StateIdle, time.Now())

	mgr := dog.NewManager(townRoot, nil)
	sm := dog.NewSessionManager(tmux.NewTmux(), townRoot, mgr)

	got := findDispatchableDog(mgr, sm, d.logger)
	if got == nil {
		t.Fatal("findDispatchableDog returned nil; expected an idle dog")
	}
	if got.Name != "alpha" && got.Name != "bravo" {
		t.Errorf("findDispatchableDog = %q, want alpha or bravo", got.Name)
	}
}

func TestCleanupStuckDogs_ClearsDeadSessionWorker(t *testing.T) {
	requireTmux(t)

	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	sm := dog.NewSessionManager(tmux.NewTmux(), townRoot, mgr)

	testSetupWorkingDogState(t, townRoot, "alpha", constants.MolDogReaper, time.Now())

	d.cleanupStuckDogs(mgr, sm)

	dg, err := mgr.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if dg.State != dog.StateIdle {
		t.Errorf("stuck dog state = %q, want idle", dg.State)
	}
	if dg.Work != "" {
		t.Errorf("stuck dog work = %q, want empty", dg.Work)
	}
}

func TestCleanupStuckDogs_ClearsAgentDeadWorker(t *testing.T) {
	requireTmux(t)

	oldSocket := tmux.GetDefaultSocket()
	socketName := fmt.Sprintf("gt-test-dog-cleanup-%d", time.Now().UnixNano())
	tmux.SetDefaultSocket(socketName)
	t.Cleanup(func() { tmux.SetDefaultSocket(oldSocket) })

	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	tm := tmux.NewTmux()
	sm := dog.NewSessionManager(tm, townRoot, mgr)

	testSetupWorkingDogState(t, townRoot, "alpha", constants.MolDogReaper, time.Now())

	sessionName := sm.SessionName("alpha")
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession(%q): %v", sessionName, err)
	}
	t.Cleanup(func() { _ = tm.KillSession(sessionName) })
	time.Sleep(200 * time.Millisecond)

	d.cleanupStuckDogs(mgr, sm)

	dg, err := mgr.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if dg.State != dog.StateIdle {
		t.Errorf("agent-dead dog state = %q, want idle", dg.State)
	}
	if dg.Work != "" {
		t.Errorf("agent-dead dog work = %q, want empty", dg.Work)
	}
	if has, err := tm.HasSession(sessionName); err != nil {
		t.Fatalf("HasSession(%q): %v", sessionName, err)
	} else if has {
		t.Errorf("agent-dead session %q still exists after cleanup", sessionName)
	}
}

func TestCleanupStuckDogs_SkipsIdleDogs(t *testing.T) {
	townRoot := t.TempDir()
	d := testHandlerDaemon(t, townRoot)

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	sm := dog.NewSessionManager(tmux.NewTmux(), townRoot, mgr)

	testSetupDogState(t, townRoot, "alpha", dog.StateIdle, time.Now())

	d.cleanupStuckDogs(mgr, sm)

	dg, err := mgr.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if dg.State != dog.StateIdle {
		t.Errorf("idle dog state = %q, want idle", dg.State)
	}
}
