package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/deacon"
)

type restartTrackerTestClock struct {
	now time.Time
}

func (c *restartTrackerTestClock) Now() time.Time {
	return c.now
}

func (c *restartTrackerTestClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

func TestRestartTrackerCrashLoopLatchAndWatchdogProbeCooldown(t *testing.T) {
	clock := &restartTrackerTestClock{now: time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)}
	cfg := RestartTrackerConfig{
		InitialBackoff:         time.Second,
		MaxBackoff:             time.Minute,
		BackoffMultiplier:      2,
		CrashLoopWindow:        15 * time.Minute,
		CrashLoopCount:         3,
		StabilityPeriod:        30 * time.Minute,
		CrashLoopProbeInterval: 30 * time.Minute,
	}
	rt := newRestartTrackerWithClock(t.TempDir(), cfg, clock.Now)

	for attempt := 1; attempt <= 3; attempt++ {
		newlyLatched := rt.RecordRestart("deacon")
		if got, want := newlyLatched, attempt == 3; got != want {
			t.Fatalf("attempt %d newlyLatched = %v, want %v", attempt, got, want)
		}
		if attempt < 3 {
			clock.Advance(time.Minute)
		}
	}
	if !rt.IsInCrashLoop("deacon") {
		t.Fatal("rapid retry threshold did not latch crash loop")
	}

	if rt.TakeWatchdogProbe("deacon") {
		t.Fatal("watchdog probe allowed before 30-minute cooldown")
	}
	clock.Advance(29 * time.Minute)
	if rt.TakeWatchdogProbe("deacon") {
		t.Fatal("watchdog probe allowed one minute before cooldown")
	}
	clock.Advance(time.Minute)
	if !rt.TakeWatchdogProbe("deacon") {
		t.Fatal("watchdog probe not allowed at 30-minute cooldown")
	}
	if rt.TakeWatchdogProbe("deacon") {
		t.Fatal("second watchdog probe allowed in same cooldown window")
	}

	// A local watchdog probe is still part of the latched crash incident. It
	// must not clear the latch merely because the ordinary stability period
	// elapsed, and it must not emit another latch alert.
	if newlyLatched := rt.RecordRestart("deacon"); newlyLatched {
		t.Fatal("watchdog probe restart emitted a duplicate latch transition")
	}
	if !rt.IsInCrashLoop("deacon") {
		t.Fatal("watchdog probe restart cleared persistent crash-loop latch")
	}

	clock.Advance(30 * time.Minute)
	if !rt.TakeWatchdogProbe("deacon") {
		t.Fatal("next watchdog probe not allowed after another 30 minutes")
	}
}

func TestRestartTrackerWatchdogProbePersistsAcrossReload(t *testing.T) {
	clock := &restartTrackerTestClock{now: time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := RestartTrackerConfig{
		CrashLoopCount:         1,
		CrashLoopProbeInterval: 30 * time.Minute,
	}
	rt := newRestartTrackerWithClock(root, cfg, clock.Now)
	if !rt.RecordRestart("deacon") {
		t.Fatal("single-restart threshold did not latch")
	}
	clock.Advance(30 * time.Minute)
	if !rt.TakeWatchdogProbe("deacon") {
		t.Fatal("expected first watchdog probe")
	}
	if err := rt.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded := newRestartTrackerWithClock(root, cfg, clock.Now)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.TakeWatchdogProbe("deacon") {
		t.Fatal("reload forgot consumed watchdog probe")
	}
	clock.Advance(30 * time.Minute)
	if !reloaded.TakeWatchdogProbe("deacon") {
		t.Fatal("reloaded tracker did not reopen probe gate after cooldown")
	}
}

func TestRestartTrackerWidelySpacedRestartsDoNotLatch(t *testing.T) {
	clock := &restartTrackerTestClock{now: time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)}
	rt := newRestartTrackerWithClock(t.TempDir(), RestartTrackerConfig{
		CrashLoopCount:  3,
		CrashLoopWindow: 15 * time.Minute,
		StabilityPeriod: time.Hour,
	}, clock.Now)

	for attempt := 0; attempt < 3; attempt++ {
		if rt.RecordRestart("deacon") {
			t.Fatalf("widely spaced attempt %d latched", attempt+1)
		}
		clock.Advance(10 * time.Minute)
	}
	if rt.IsInCrashLoop("deacon") {
		t.Fatal("three restarts spanning more than CrashLoopWindow latched")
	}
}

func TestRestartTrackerCrashLoopWindowPersistsAcrossReload(t *testing.T) {
	clock := &restartTrackerTestClock{now: time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := RestartTrackerConfig{
		CrashLoopCount:  3,
		CrashLoopWindow: 15 * time.Minute,
		StabilityPeriod: time.Hour,
	}
	rt := newRestartTrackerWithClock(root, cfg, clock.Now)
	rt.RecordRestart("deacon")
	clock.Advance(5 * time.Minute)
	rt.RecordRestart("deacon")
	if err := rt.Save(); err != nil {
		t.Fatal(err)
	}

	reloaded := newRestartTrackerWithClock(root, cfg, clock.Now)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Minute)
	if !reloaded.RecordRestart("deacon") {
		t.Fatal("reloaded tracker forgot rapid-restart window history")
	}
}

func TestRestartTrackerCorruptLoadPreservesStateAndFailsClosed(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "daemon")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rt := NewRestartTracker(root, RestartTrackerConfig{})
	rt.state.Agents["preserved"] = &AgentRestartInfo{CrashLoopSince: time.Now()}
	corrupt := `{"agents":{"injected":{"restart_count":9}}} trailing garbage`
	if err := os.WriteFile(filepath.Join(stateDir, "restart_state.json"), []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := rt.Load(); err == nil {
		t.Fatal("corrupt restart state loaded successfully")
	}
	if _, ok := rt.state.Agents["preserved"]; !ok {
		t.Fatalf("failed load replaced prior live state: %#v", rt.state)
	}
	if _, ok := rt.state.Agents["injected"]; ok {
		t.Fatalf("failed load partially mutated live state: %#v", rt.state)
	}
	if rt.CanRestart("unknown") {
		t.Fatal("corrupt restart state failed open for an unknown agent")
	}
	if !rt.IsInCrashLoop("unknown") {
		t.Fatal("corrupt restart state did not expose a fail-closed crash-loop latch")
	}
	if err := rt.Save(); err == nil {
		t.Fatal("tracker overwrote corrupt state after failed load")
	}

	var logs bytes.Buffer
	d := &Daemon{
		config:         &Config{TownRoot: root},
		ctx:            context.Background(),
		restartTracker: rt,
		logger:         log.New(&logs, "", 0),
	}
	if d.authorizeDeaconRestart("deacon") {
		t.Fatal("daemon resumed restart authorization after tracker load failure")
	}
	if err := os.MkdirAll(filepath.Join(root, "settings"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "settings", "config.json"),
		[]byte(`{"operational":{"daemon":{"boot_auto_spawn":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d.ensureBootRunning()
	if !strings.Contains(logs.String(), "hosted Boot suppressed") {
		t.Fatalf("corrupt state did not suppress hosted Boot: %s", logs.String())
	}
}

func TestRestartTrackerSaveIsAtomicAndMode0600(t *testing.T) {
	root := t.TempDir()
	rt := NewRestartTracker(root, RestartTrackerConfig{})
	rt.RecordRestart("deacon")
	if err := rt.Save(); err != nil {
		t.Fatalf("Save without precreated daemon directory: %v", err)
	}
	path := filepath.Join(root, "daemon", "restart_state.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("restart state mode = %04o, want 0600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state RestartState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("saved restart state is not valid JSON: %v", err)
	}
	temps, err := filepath.Glob(path + ".tmp.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(temps) != 0 {
		t.Fatalf("atomic save leaked temporary files: %v", temps)
	}
}

func TestRestartTrackerRejectsSemanticallyInvalidJSON(t *testing.T) {
	for _, body := range []string{
		`null`,
		`{"agents":{"deacon":null}}`,
	} {
		t.Run(body, func(t *testing.T) {
			root := t.TempDir()
			stateDir := filepath.Join(root, "daemon")
			if err := os.MkdirAll(stateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(stateDir, "restart_state.json"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			rt := NewRestartTracker(root, RestartTrackerConfig{})
			if err := rt.Load(); err == nil {
				t.Fatalf("semantically invalid restart state loaded: %s", body)
			}
			if rt.CanRestart("deacon") || !rt.IsInCrashLoop("deacon") {
				t.Fatalf("semantically invalid state did not fail closed: %s", body)
			}
		})
	}
}

func TestEnsureBootRunningCrashLoopSuppressesHostedPromotion(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "settings"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "settings", "config.json"),
		[]byte(`{"operational":{"daemon":{"boot_auto_spawn":true}}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	rt := NewRestartTracker(root, RestartTrackerConfig{})
	rt.state.Agents["deacon"] = &AgentRestartInfo{
		CrashLoopSince: time.Now().Add(-time.Minute),
	}
	var logs bytes.Buffer
	d := &Daemon{
		config:         &Config{TownRoot: root},
		ctx:            context.Background(),
		logger:         log.New(&logs, "", 0),
		restartTracker: rt,
	}

	d.ensureBootRunning()

	if !strings.Contains(logs.String(), "hosted Boot suppressed") {
		t.Fatalf("missing local-only crash-loop guard log: %s", logs.String())
	}
}

func TestDeaconCrashLoopProbeRequiresFreshHealthyLocalWatchdog(t *testing.T) {
	clock := &restartTrackerTestClock{now: time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := newRestartTrackerWithClock(root, RestartTrackerConfig{
		CrashLoopCount:         1,
		CrashLoopProbeInterval: 30 * time.Minute,
	}, clock.Now)
	rt.RecordRestart("deacon")
	clock.Advance(30 * time.Minute)

	d := &Daemon{
		config:         &Config{TownRoot: root},
		logger:         log.New(&bytes.Buffer{}, "", 0),
		restartTracker: rt,
	}
	writeLocalWatchdogState(t, root, "recovering-local", clock.now)
	if d.authorizeDeaconRestart("deacon") {
		t.Fatal("unhealthy local watchdog authorized crash-loop probe")
	}
	if !rt.state.Agents["deacon"].LastWatchdogProbe.IsZero() {
		t.Fatal("unhealthy watchdog consumed crash-loop probe")
	}

	writeLocalWatchdogState(t, root, "healthy", clock.now.Add(-3*time.Minute))
	if d.authorizeDeaconRestart("deacon") {
		t.Fatal("stale local watchdog authorized crash-loop probe")
	}
	if !rt.state.Agents["deacon"].LastWatchdogProbe.IsZero() {
		t.Fatal("stale watchdog consumed crash-loop probe")
	}

	writeLocalWatchdogState(t, root, "healthy", clock.now.Add(-time.Minute))
	if !d.authorizeDeaconRestart("deacon") {
		t.Fatal("fresh healthy watchdog did not authorize local crash-loop probe")
	}
	if rt.state.Agents["deacon"].LastWatchdogProbe.IsZero() {
		t.Fatal("authorized probe was not consumed")
	}
	if rt.CanRestart("deacon") {
		t.Fatal("test requires persistent latch; authorization must bypass ordinary CanRestart")
	}
	if d.authorizeDeaconRestart("deacon") {
		t.Fatal("second restart authorized in same probe interval")
	}
}

func TestDeaconCrashLoopProbePersistenceFailureFailsClosed(t *testing.T) {
	clock := &restartTrackerTestClock{now: time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)}
	root := t.TempDir()
	// A regular file at daemon/ makes restart_state.json unwritable.
	if err := os.WriteFile(filepath.Join(root, "daemon"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := newRestartTrackerWithClock(root, RestartTrackerConfig{
		CrashLoopCount:         1,
		CrashLoopProbeInterval: 30 * time.Minute,
	}, clock.Now)
	rt.RecordRestart("deacon")
	// This test isolates probe-token persistence; alert retries have their own
	// coverage and would attempt an unrelated external command here.
	rt.state.Agents["deacon"].CrashLoopAlertPending = false
	clock.Advance(30 * time.Minute)
	writeLocalWatchdogState(t, root, "healthy", clock.now)
	d := &Daemon{
		config:         &Config{TownRoot: root},
		ctx:            context.Background(),
		logger:         log.New(&bytes.Buffer{}, "", 0),
		restartTracker: rt,
	}

	mgr := &fakeDeaconProbeManager{}
	d.ensureDeaconRunningWithManager(mgr)
	if len(mgr.startAgents) != 0 || mgr.stopCalls != 0 {
		t.Fatalf("manager acted despite failed probe persistence: starts=%v stops=%d",
			mgr.startAgents, mgr.stopCalls)
	}
	if !rt.state.Agents["deacon"].LastWatchdogProbe.IsZero() {
		t.Fatal("failed persistence consumed probe in memory")
	}
}

type fakeDeaconProbeManager struct {
	startAgents []string
	stopCalls   int
	startErr    error
	stopErr     error
}

func (m *fakeDeaconProbeManager) Start(agent string) error {
	m.startAgents = append(m.startAgents, agent)
	return m.startErr
}

func (m *fakeDeaconProbeManager) Stop() error {
	m.stopCalls++
	return m.stopErr
}

func TestDeaconCrashLoopProbeFreshHeartbeatStabilizesButStaleForcesLocalRestart(t *testing.T) {
	newDaemon := func(t *testing.T) (*Daemon, *RestartTracker, *restartTrackerTestClock, string) {
		t.Helper()
		clock := &restartTrackerTestClock{now: time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)}
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "daemon"), 0o755); err != nil {
			t.Fatal(err)
		}
		rt := newRestartTrackerWithClock(root, RestartTrackerConfig{
			CrashLoopCount:         1,
			CrashLoopProbeInterval: 30 * time.Minute,
			StabilityPeriod:        30 * time.Minute,
		}, clock.Now)
		rt.RecordRestart("deacon")
		if err := rt.MarkCrashLoopAlertDeliveredPersisted("deacon"); err != nil {
			t.Fatal(err)
		}
		clock.Advance(31 * time.Minute)
		writeLocalWatchdogState(t, root, "healthy", clock.now)
		return &Daemon{
			config:         &Config{TownRoot: root},
			ctx:            context.Background(),
			logger:         log.New(&bytes.Buffer{}, "", 0),
			restartTracker: rt,
		}, rt, clock, root
	}

	t.Run("fresh heartbeat clears only after stability", func(t *testing.T) {
		d, rt, clock, root := newDaemon(t)
		if err := deacon.WriteHeartbeat(root, &deacon.Heartbeat{Timestamp: clock.now.Add(-time.Minute)}); err != nil {
			t.Fatal(err)
		}
		mgr := &fakeDeaconProbeManager{}
		d.ensureDeaconRunningWithManager(mgr)
		if len(mgr.startAgents) != 0 || mgr.stopCalls != 0 {
			t.Fatalf("fresh live Deacon was restarted: starts=%v stops=%d", mgr.startAgents, mgr.stopCalls)
		}
		if rt.IsInCrashLoop("deacon") {
			t.Fatal("fresh heartbeat after 30m stability did not clear latch")
		}
	})

	t.Run("stale existing process forces pinned local restart", func(t *testing.T) {
		d, rt, clock, root := newDaemon(t)
		if err := deacon.WriteHeartbeat(root, &deacon.Heartbeat{Timestamp: clock.now.Add(-10 * time.Minute)}); err != nil {
			t.Fatal(err)
		}
		mgr := &fakeDeaconProbeManager{}
		d.ensureDeaconRunningWithManager(mgr)
		if mgr.stopCalls != 1 {
			t.Fatalf("stop calls = %d, want 1", mgr.stopCalls)
		}
		if len(mgr.startAgents) != 1 || mgr.startAgents[0] != "opencode-local" {
			t.Fatalf("start agents = %v, want [opencode-local]", mgr.startAgents)
		}
		if !rt.IsInCrashLoop("deacon") {
			t.Fatal("forced local probe cleared crash-loop latch")
		}
	})

	t.Run("stale already-running result preserves latch", func(t *testing.T) {
		d, rt, clock, root := newDaemon(t)
		if err := deacon.WriteHeartbeat(root, &deacon.Heartbeat{Timestamp: clock.now.Add(-10 * time.Minute)}); err != nil {
			t.Fatal(err)
		}
		mgr := &fakeDeaconProbeManager{startErr: deacon.ErrAlreadyRunning}
		d.ensureDeaconRunningWithManager(mgr)
		if mgr.stopCalls != 1 {
			t.Fatalf("stop calls = %d, want 1", mgr.stopCalls)
		}
		if len(mgr.startAgents) != 1 || mgr.startAgents[0] != "opencode-local" {
			t.Fatalf("start agents = %v, want [opencode-local]", mgr.startAgents)
		}
		if !rt.IsInCrashLoop("deacon") {
			t.Fatal("stale ErrAlreadyRunning result cleared crash-loop latch")
		}
	})
}

func writeLocalWatchdogState(t *testing.T, root, status string, checkedAt time.Time) {
	t.Helper()
	dir := filepath.Join(root, "deacon")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"status":"` + status + `","checked_at":"` + checkedAt.UTC().Format(time.RFC3339) + `"}`
	if err := os.WriteFile(filepath.Join(dir, "lmstudio-watchdog.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAlertDeaconCrashLoopLatchUsesMockedHumanMailRoute(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("shell fixture requires Unix")
	}
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "gt.log")
	gtPath := filepath.Join(t.TempDir(), "gt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + logPath + "\n"
	if err := os.WriteFile(gtPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		config: &Config{TownRoot: root},
		logger: log.New(&bytes.Buffer{}, "", 0),
		gtPath: gtPath,
	}

	notifyCalls := 0
	d.alertDeaconCrashLoopLatchWithNotification(func(title, message string) error {
		notifyCalls++
		if title != "DEACON_CRASH_LOOP_LATCHED" || message == "" {
			t.Fatalf("unexpected notification: title=%q message=%q", title, message)
		}
		return errors.New("mock notification unavailable")
	})

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if calls := strings.TrimSpace(string(data)); !strings.HasPrefix(calls, "mail send --human ") {
		t.Fatalf("alert did not use supported human mail route: %q", calls)
	}
	if notifyCalls != 1 {
		t.Fatalf("notification calls = %d, want 1", notifyCalls)
	}
}

func TestFailedDeaconCrashLoopHumanAlertPersistsAndRetries(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("shell fixture requires Unix")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "gt.log")
	failMarker := filepath.Join(t.TempDir(), "fail-once")
	gtPath := filepath.Join(t.TempDir(), "gt")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + logPath + "\n" +
		"if [ ! -f " + failMarker + " ]; then touch " + failMarker + "; exit 1; fi\n"
	if err := os.WriteFile(gtPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := NewRestartTracker(root, RestartTrackerConfig{CrashLoopCount: 1})
	if !rt.RecordRestart("deacon") {
		t.Fatal("expected latch")
	}
	if err := rt.Save(); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		config:         &Config{TownRoot: root},
		logger:         log.New(&bytes.Buffer{}, "", 0),
		gtPath:         gtPath,
		restartTracker: rt,
	}
	notify := func(string, string) error { return nil }

	d.retryDeaconCrashLoopAlertWithNotification("deacon", notify)
	if !rt.CrashLoopAlertPending("deacon") {
		t.Fatal("failed durable alert was marked delivered")
	}
	reloaded := NewRestartTracker(root, RestartTrackerConfig{CrashLoopCount: 1})
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	d.restartTracker = reloaded
	d.retryDeaconCrashLoopAlertWithNotification("deacon", notify)
	if reloaded.CrashLoopAlertPending("deacon") {
		t.Fatal("successful durable alert remained pending")
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if calls := len(strings.Split(strings.TrimSpace(string(data)), "\n")); calls != 2 {
		t.Fatalf("human mail attempts = %d, want 2; calls=%q", calls, data)
	}
}
