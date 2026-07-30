package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/dog"
	"github.com/steveyegge/gastown/internal/session"
)

func TestModelCrashSupervisorIntervals(t *testing.T) {
	if modelCrashScanInterval != 60*time.Second {
		t.Fatalf("scan interval = %v, want 60s", modelCrashScanInterval)
	}
	if modelCrashControlProbeInterval != 30*time.Minute {
		t.Fatalf("control probe interval = %v, want 30m", modelCrashControlProbeInterval)
	}
	if modelCrashProgressResetInterval != 30*time.Minute {
		t.Fatalf("progress reset interval = %v, want 30m", modelCrashProgressResetInterval)
	}
	if modelStallNudgeAfter != 15*time.Minute || modelStallLocalRestartAfter != 30*time.Minute ||
		modelStallGoEscalateAfter != 60*time.Minute {
		t.Fatalf("stall intervals = %v/%v/%v",
			modelStallNudgeAfter, modelStallLocalRestartAfter, modelStallGoEscalateAfter)
	}
}

func TestModelCrashWorkerWorkUnitExtraction(t *testing.T) {
	const identity = "rig/polecats/slit"
	if got, active := modelCrashPolecatWorkUnit(&AgentBeadInfo{
		HookBead: "gt-hook-a",
		State:    string(beads.AgentStateWorking),
	}, false, identity, identity); !active || got != "gt-hook-a" {
		t.Fatalf("active polecat work unit = %q/%v, want gt-hook-a/true", got, active)
	}
	if got, active := modelCrashPolecatWorkUnit(&AgentBeadInfo{
		HookBead: "gt-hook-a",
		State:    string(beads.AgentStateWorking),
	}, false, "rig/polecats/nux", identity); active || got != "" {
		t.Fatalf("stale reassigned hook became work unit = %q/%v", got, active)
	}
	for _, tt := range []struct {
		name   string
		info   *AgentBeadInfo
		closed bool
	}{
		{name: "no hook", info: &AgentBeadInfo{}},
		{name: "done", info: &AgentBeadInfo{HookBead: "gt-hook", State: string(beads.AgentStateDone)}},
		{name: "closed hook", info: &AgentBeadInfo{HookBead: "gt-hook"}, closed: true},
	} {
		t.Run("polecat "+tt.name, func(t *testing.T) {
			if got, active := modelCrashPolecatWorkUnit(tt.info, tt.closed, identity, identity); active || got != "" {
				t.Fatalf("idle polecat work unit = %q/%v", got, active)
			}
		})
	}

	if got, active := modelCrashDogWorkUnit(&dog.Dog{
		State: dog.StateWorking,
		Work:  "mol-dog-work",
	}); !active || got != "mol-dog-work" {
		t.Fatalf("active Dog work unit = %q/%v, want mol-dog-work/true", got, active)
	}
	for _, candidate := range []*dog.Dog{
		{State: dog.StateIdle, Work: "old-work"},
		{State: dog.StateWorking},
	} {
		if got, active := modelCrashDogWorkUnit(candidate); active || got != "" {
			t.Fatalf("idle Dog work unit = %q/%v", got, active)
		}
	}
}

type fakeModelCrashClock struct {
	now time.Time
}

func (c *fakeModelCrashClock) Now() time.Time { return c.now }

type modelCrashAction struct {
	kind     string
	identity string
	agent    string
}

type fakeModelCrashExecutor struct {
	sessions          []modelCrashSession
	watchdog          modelCrashWatchdog
	watchdogErr       error
	activePolecats    map[string]bool
	escalationAllowed map[string]bool
	actions           []modelCrashAction
	restartErr        error
	alertFailures     int
}

func (e *fakeModelCrashExecutor) Sessions() ([]modelCrashSession, error) {
	return e.sessions, nil
}

func (e *fakeModelCrashExecutor) LMWatchdog() (modelCrashWatchdog, error) {
	return e.watchdog, e.watchdogErr
}

func (e *fakeModelCrashExecutor) Restart(s modelCrashSession, agent string) error {
	e.actions = append(e.actions, modelCrashAction{kind: "restart", identity: s.Identity, agent: agent})
	return e.restartErr
}

func (e *fakeModelCrashExecutor) Nudge(s modelCrashSession, message string) error {
	e.actions = append(e.actions, modelCrashAction{kind: "nudge", identity: s.Identity})
	return nil
}

func (e *fakeModelCrashExecutor) RecoveryPolicy(modelCrashSession) modelCrashRecoveryPolicy {
	return defaultModelCrashRecoveryPolicy()
}

func (e *fakeModelCrashExecutor) ActivePolecat(s modelCrashSession) bool {
	return e.activePolecats[s.Identity]
}

func (e *fakeModelCrashExecutor) AllowsContinuation(s modelCrashSession, fromAgent, toAgent string) bool {
	return e.escalationAllowed[s.Identity] && fromAgent == "opencode-local" && toAgent == "opencode-go"
}

func (e *fakeModelCrashExecutor) Alert(key, message string) error {
	e.actions = append(e.actions, modelCrashAction{kind: "alert", identity: key})
	if e.alertFailures > 0 {
		e.alertFailures--
		return errors.New("alert failed")
	}
	return nil
}

func fatalLocalSession(identity, role, instance string) modelCrashSession {
	candidate := modelCrashSession{
		Name:       "tmux-" + identity,
		InstanceID: instance,
		Identity:   identity,
		Role:       role,
		Agent:      "opencode-local",
		Output:     session.ModelCrashFatalSignature,
	}
	if role == "polecat" || role == "dog" {
		candidate.WorkUnit = "work-" + identity
	}
	return candidate
}

func healthyWatchdog(now time.Time) modelCrashWatchdog {
	return modelCrashWatchdog{Status: "healthy", CheckedAt: now.Add(-30 * time.Second)}
}

func newTestModelCrashSupervisor(t *testing.T, clock *fakeModelCrashClock, executor *fakeModelCrashExecutor) *modelCrashSupervisor {
	t.Helper()
	return newModelCrashSupervisor(filepath.Join(t.TempDir(), "model-crash-supervisor.json"), clock, executor)
}

func TestModelCrashSupervisorRequiresTwoIdenticalObservations(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	clock := &fakeModelCrashClock{now: now}
	executor := &fakeModelCrashExecutor{
		sessions: []modelCrashSession{fatalLocalSession("rig/polecats/toast", "polecat", "run-1")},
		watchdog: healthyWatchdog(now),
	}
	supervisor := newTestModelCrashSupervisor(t, clock, executor)

	if err := supervisor.Scan(); err != nil {
		t.Fatal(err)
	}
	if len(executor.actions) != 0 {
		t.Fatalf("first observation took action: %#v", executor.actions)
	}

	clock.now = clock.now.Add(modelCrashScanInterval)
	executor.watchdog = healthyWatchdog(clock.now)
	if err := supervisor.Scan(); err != nil {
		t.Fatal(err)
	}
	if len(executor.actions) != 1 || executor.actions[0].agent != "opencode-local" {
		t.Fatalf("second identical observation actions = %#v, want one local restart", executor.actions)
	}
}

func TestModelCrashSupervisorStallLadder(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	clock := &fakeModelCrashClock{now: now}
	candidate := fatalLocalSession("rig/polecats/nux", "polecat", "run-1")
	candidate.Output = "waiting"
	candidate.HeartbeatAt = now.Add(-modelStallNudgeAfter)
	executor := &fakeModelCrashExecutor{
		sessions:          []modelCrashSession{candidate},
		watchdog:          healthyWatchdog(now),
		activePolecats:    map[string]bool{candidate.Identity: true},
		escalationAllowed: map[string]bool{candidate.Identity: true},
	}
	supervisor := newTestModelCrashSupervisor(t, clock, executor)

	if err := supervisor.Scan(); err != nil {
		t.Fatal(err)
	}
	if len(executor.actions) != 1 || executor.actions[0].kind != "nudge" {
		t.Fatalf("15m actions = %#v, want one nudge", executor.actions)
	}

	clock.now = candidate.HeartbeatAt.Add(modelStallLocalRestartAfter)
	executor.watchdog = healthyWatchdog(clock.now)
	if err := supervisor.Scan(); err != nil {
		t.Fatal(err)
	}
	if len(executor.actions) != 2 || executor.actions[1].agent != "opencode-local" {
		t.Fatalf("30m actions = %#v, want local restart", executor.actions)
	}

	clock.now = candidate.HeartbeatAt.Add(modelStallGoEscalateAfter)
	executor.watchdog = healthyWatchdog(clock.now)
	if err := supervisor.Scan(); err != nil {
		t.Fatal(err)
	}
	if len(executor.actions) != 3 || executor.actions[2].agent != "opencode-go" {
		t.Fatalf("60m actions = %#v, want Go continuation", executor.actions)
	}

	clock.now = clock.now.Add(time.Minute)
	executor.watchdog = healthyWatchdog(clock.now)
	if err := supervisor.Scan(); err != nil {
		t.Fatal(err)
	}
	if len(executor.actions) != 3 {
		t.Fatalf("ladder replayed action: %#v", executor.actions)
	}
}

func TestModelCrashSupervisorCorrelationIncludesSessionInstanceAndFingerprint(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	clock := &fakeModelCrashClock{now: now}
	executor := &fakeModelCrashExecutor{
		sessions: []modelCrashSession{fatalLocalSession("deacon", "deacon", "run-1")},
		watchdog: healthyWatchdog(now),
	}
	supervisor := newTestModelCrashSupervisor(t, clock, executor)

	if err := supervisor.Scan(); err != nil {
		t.Fatal(err)
	}
	executor.sessions[0].InstanceID = "run-2"
	clock.now = clock.now.Add(modelCrashScanInterval)
	executor.watchdog = healthyWatchdog(clock.now)
	if err := supervisor.Scan(); err != nil {
		t.Fatal(err)
	}
	if len(executor.actions) != 0 {
		t.Fatalf("different session instance correlated with prior crash: %#v", executor.actions)
	}
}

func TestModelCrashSupervisorLongObservationGapRequiresFreshPair(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	clock := &fakeModelCrashClock{now: now}
	executor := &fakeModelCrashExecutor{
		sessions: []modelCrashSession{fatalLocalSession("deacon", "deacon", "run-1")},
		watchdog: healthyWatchdog(now),
	}
	supervisor := newTestModelCrashSupervisor(t, clock, executor)

	if err := supervisor.Scan(); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(2*modelCrashScanInterval + time.Second)
	executor.watchdog = healthyWatchdog(clock.now)
	if err := supervisor.Scan(); err != nil {
		t.Fatal(err)
	}
	if len(executor.actions) != 0 {
		t.Fatalf("stale observation pair took action: %#v", executor.actions)
	}

	clock.now = clock.now.Add(modelCrashScanInterval)
	executor.watchdog = healthyWatchdog(clock.now)
	if err := supervisor.Scan(); err != nil {
		t.Fatal(err)
	}
	if len(executor.actions) != 1 || executor.actions[0].agent != "opencode-local" {
		t.Fatalf("fresh adjacent pair actions = %#v, want one local restart", executor.actions)
	}
}

func TestModelCrashSupervisorRequiresFreshHealthyLMWatchdog(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		watchdog modelCrashWatchdog
		err      error
	}{
		{name: "recovering", watchdog: modelCrashWatchdog{Status: "recovering-local", CheckedAt: now}},
		{name: "stale", watchdog: modelCrashWatchdog{Status: "healthy", CheckedAt: now.Add(-modelCrashWatchdogMaxAge - time.Second)}},
		{name: "unreadable", err: errors.New("read failed")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &fakeModelCrashClock{now: now}
			executor := &fakeModelCrashExecutor{
				sessions:    []modelCrashSession{fatalLocalSession("deacon", "deacon", "run-1")},
				watchdog:    tt.watchdog,
				watchdogErr: tt.err,
			}
			supervisor := newTestModelCrashSupervisor(t, clock, executor)
			if err := supervisor.Scan(); err != nil {
				t.Fatal(err)
			}
			clock.now = clock.now.Add(modelCrashScanInterval)
			if err := supervisor.Scan(); err != nil {
				t.Fatal(err)
			}
			if len(executor.actions) != 0 {
				t.Fatalf("unsafe watchdog state took action: %#v", executor.actions)
			}
		})
	}
}

func TestModelCrashSupervisorCoversLocalRolesButExcludesBoot(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	clock := &fakeModelCrashClock{now: now}
	executor := &fakeModelCrashExecutor{
		sessions: []modelCrashSession{
			fatalLocalSession("mayor", "mayor", "mayor-1"),
			fatalLocalSession("deacon", "deacon", "deacon-1"),
			fatalLocalSession("rig/witness", "witness", "witness-1"),
			fatalLocalSession("rig/refinery", "refinery", "refinery-1"),
			fatalLocalSession("rig/crew/max", "crew", "crew-1"),
			fatalLocalSession("rig/polecats/toast", "polecat", "polecat-1"),
			fatalLocalSession("deacon/dogs/scout", "dog", "dog-1"),
			fatalLocalSession("deacon/boot", "boot", "boot-1"),
			{Name: "hosted", InstanceID: "hosted-1", Identity: "rig/crew/hosted", Role: "crew", Agent: "opencode-go", Output: session.ModelCrashFatalSignature},
		},
		watchdog: healthyWatchdog(now),
	}
	supervisor := newTestModelCrashSupervisor(t, clock, executor)

	if err := supervisor.Scan(); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(modelCrashScanInterval)
	executor.watchdog = healthyWatchdog(clock.now)
	if err := supervisor.Scan(); err != nil {
		t.Fatal(err)
	}

	restarts := 0
	for _, action := range executor.actions {
		if action.kind == "restart" {
			restarts++
			if action.identity == "deacon/boot" || action.identity == "rig/crew/hosted" {
				t.Fatalf("excluded session restarted: %#v", action)
			}
		}
	}
	if restarts != 7 {
		t.Fatalf("local role restart count = %d, want 7; actions=%#v", restarts, executor.actions)
	}
}

func TestModelCrashSupervisorEscalatesActivePolecatExactlyOnceAfterRepeat(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	identity := "rig/polecats/toast"
	clock := &fakeModelCrashClock{now: now}
	executor := &fakeModelCrashExecutor{
		sessions:          []modelCrashSession{fatalLocalSession(identity, "polecat", "run-local")},
		watchdog:          healthyWatchdog(now),
		activePolecats:    map[string]bool{identity: true},
		escalationAllowed: map[string]bool{identity: true},
	}
	supervisor := newTestModelCrashSupervisor(t, clock, executor)

	scanTwice := func(instance string) {
		t.Helper()
		executor.sessions[0].InstanceID = instance
		for i := 0; i < 2; i++ {
			executor.watchdog = healthyWatchdog(clock.now)
			if err := supervisor.Scan(); err != nil {
				t.Fatal(err)
			}
			clock.now = clock.now.Add(modelCrashScanInterval)
		}
	}
	scanTwice("run-local")
	scanTwice("run-repeat")
	scanTwice("run-go-repeat")

	var local, hosted int
	for _, action := range executor.actions {
		if action.kind != "restart" {
			continue
		}
		switch action.agent {
		case "opencode-local":
			local++
		case "opencode-go":
			hosted++
		}
	}
	if local != 1 || hosted != 1 {
		t.Fatalf("restart counts local=%d hosted=%d, want 1/1; actions=%#v", local, hosted, executor.actions)
	}
}

func TestModelCrashSupervisorWorkUnitChangeResetsPolecatBudgets(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	identity := "rig/polecats/toast"
	clock := &fakeModelCrashClock{now: now}
	candidate := fatalLocalSession(identity, "polecat", "work-a-local")
	candidate.WorkUnit = "hook-a"
	executor := &fakeModelCrashExecutor{
		sessions:          []modelCrashSession{candidate},
		watchdog:          healthyWatchdog(now),
		activePolecats:    map[string]bool{identity: true},
		escalationAllowed: map[string]bool{identity: true},
	}
	supervisor := newTestModelCrashSupervisor(t, clock, executor)

	scanPair := func(instance, workUnit string) {
		t.Helper()
		executor.sessions[0].InstanceID = instance
		executor.sessions[0].WorkUnit = workUnit
		for i := 0; i < 2; i++ {
			executor.watchdog = healthyWatchdog(clock.now)
			if err := supervisor.Scan(); err != nil {
				t.Fatal(err)
			}
			clock.now = clock.now.Add(modelCrashScanInterval)
		}
	}

	scanPair("work-a-local", "hook-a")
	scanPair("work-a-repeat", "hook-a")
	scanPair("work-b-local", "hook-b")
	scanPair("work-b-repeat", "hook-b")

	var local, hosted int
	for _, action := range executor.actions {
		if action.kind != "restart" {
			continue
		}
		switch action.agent {
		case "opencode-local":
			local++
		case "opencode-go":
			hosted++
		}
	}
	if local != 2 || hosted != 2 {
		t.Fatalf("work-unit restart budgets local=%d hosted=%d, want 2/2; actions=%#v",
			local, hosted, executor.actions)
	}
	state := supervisor.state.Sessions[identity]
	if state == nil || state.WorkUnit != "hook-b" ||
		state.LocalRestarts != 1 || state.GoContinuations != 1 {
		t.Fatalf("current work unit did not own fresh budgets: %#v", state)
	}
	data, err := os.ReadFile(supervisor.statePath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted modelCrashState
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if got := persisted.Sessions[identity]; got == nil || got.WorkUnit != "hook-b" {
		t.Fatalf("current work unit was not durable: %#v", got)
	}
}

func TestModelCrashSupervisorIgnoresIdleWorkersButCoversControlsAndCrew(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	clock := &fakeModelCrashClock{now: now}
	idlePolecat := fatalLocalSession("rig/polecats/idle", "polecat", "polecat-1")
	idlePolecat.WorkUnit = ""
	idleDog := fatalLocalSession("deacon/dogs/idle", "dog", "dog-1")
	idleDog.WorkUnit = ""
	executor := &fakeModelCrashExecutor{
		sessions: []modelCrashSession{
			idlePolecat,
			idleDog,
			fatalLocalSession("deacon", "deacon", "deacon-1"),
			fatalLocalSession("rig/crew/max", "crew", "crew-1"),
		},
		watchdog: healthyWatchdog(now),
	}
	supervisor := newTestModelCrashSupervisor(t, clock, executor)

	for i := 0; i < 2; i++ {
		executor.watchdog = healthyWatchdog(clock.now)
		if err := supervisor.Scan(); err != nil {
			t.Fatal(err)
		}
		clock.now = clock.now.Add(modelCrashScanInterval)
	}
	for _, action := range executor.actions {
		if action.identity == idlePolecat.Identity || action.identity == idleDog.Identity {
			t.Fatalf("idle worker took recovery action: %#v", executor.actions)
		}
	}
	if len(executor.actions) != 2 {
		t.Fatalf("control/Crew restart actions = %#v, want 2", executor.actions)
	}
	if supervisor.state.Sessions[idlePolecat.Identity] != nil ||
		supervisor.state.Sessions[idleDog.Identity] != nil {
		t.Fatalf("idle workers gained incidents: %#v", supervisor.state.Sessions)
	}
}

func TestModelCrashSupervisorDeniesHostedContinuationWithoutActivePolicy(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		role    string
		active  bool
		allowed bool
	}{
		{name: "control role", role: "deacon", active: true, allowed: true},
		{name: "dog role", role: "dog", active: true, allowed: true},
		{name: "inactive polecat", role: "polecat", active: false, allowed: true},
		{name: "missing worktree policy", role: "polecat", active: true, allowed: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity := "rig/polecats/toast"
			if tt.role != "polecat" {
				identity = tt.role
			}
			clock := &fakeModelCrashClock{now: now}
			executor := &fakeModelCrashExecutor{
				sessions:          []modelCrashSession{fatalLocalSession(identity, tt.role, "run-local")},
				watchdog:          healthyWatchdog(now),
				activePolecats:    map[string]bool{identity: tt.active},
				escalationAllowed: map[string]bool{identity: tt.allowed},
			}
			supervisor := newTestModelCrashSupervisor(t, clock, executor)

			for _, instance := range []string{"run-local", "run-repeat"} {
				executor.sessions[0].InstanceID = instance
				for i := 0; i < 2; i++ {
					executor.watchdog = healthyWatchdog(clock.now)
					if err := supervisor.Scan(); err != nil {
						t.Fatal(err)
					}
					clock.now = clock.now.Add(modelCrashScanInterval)
				}
			}
			for _, action := range executor.actions {
				if action.kind == "restart" && action.agent == "opencode-go" {
					t.Fatalf("unauthorized hosted continuation: %#v", executor.actions)
				}
			}

			state := supervisor.state.Sessions[identity]
			if state == nil || state.NextProbeAt.Sub(clock.now) > modelCrashControlProbeInterval {
				t.Fatalf("local-only session missing bounded 30m probe state: %#v", state)
			}

			restartsBeforeProbe := len(executor.actions)
			clock.now = state.NextProbeAt.Add(-time.Second)
			executor.watchdog = healthyWatchdog(clock.now)
			if err := supervisor.Scan(); err != nil {
				t.Fatal(err)
			}
			if len(executor.actions) != restartsBeforeProbe {
				t.Fatalf("control probe ran before 30m cooldown: %#v", executor.actions)
			}

			clock.now = state.NextProbeAt
			executor.watchdog = healthyWatchdog(clock.now)
			if err := supervisor.Scan(); err != nil {
				t.Fatal(err)
			}
			last := executor.actions[len(executor.actions)-1]
			if last.kind != "restart" || last.agent != "opencode-local" {
				t.Fatalf("30m control probe action = %#v, want local restart", last)
			}
		})
	}
}

func TestModelCrashSupervisorControlProbeFailureReentersCooldown(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	clock := &fakeModelCrashClock{now: now}
	executor := &fakeModelCrashExecutor{
		sessions: []modelCrashSession{fatalLocalSession("deacon", "deacon", "run-local")},
		watchdog: healthyWatchdog(now),
	}
	supervisor := newTestModelCrashSupervisor(t, clock, executor)

	for _, instance := range []string{"run-local", "run-repeat"} {
		executor.sessions[0].InstanceID = instance
		for i := 0; i < 2; i++ {
			executor.watchdog = healthyWatchdog(clock.now)
			if err := supervisor.Scan(); err != nil {
				t.Fatal(err)
			}
			clock.now = clock.now.Add(modelCrashScanInterval)
		}
	}
	state := supervisor.state.Sessions["deacon"]
	executor.restartErr = errors.New("probe restart failed")
	clock.now = state.NextProbeAt
	executor.watchdog = healthyWatchdog(clock.now)
	if err := supervisor.Scan(); err != nil {
		t.Fatal(err)
	}
	if !state.NextProbeAt.Equal(clock.now.Add(modelCrashControlProbeInterval)) {
		t.Fatalf("next probe = %v, want %v", state.NextProbeAt, clock.now.Add(modelCrashControlProbeInterval))
	}
}

func TestModelCrashSupervisorResetsIncidentOnlyAfterObservedProgressWindow(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	identity := "rig/polecats/toast"
	clock := &fakeModelCrashClock{now: now}
	executor := &fakeModelCrashExecutor{
		sessions: []modelCrashSession{fatalLocalSession(identity, "polecat", "run-local")},
		watchdog: healthyWatchdog(now),
	}
	supervisor := newTestModelCrashSupervisor(t, clock, executor)

	for i := 0; i < 2; i++ {
		executor.watchdog = healthyWatchdog(clock.now)
		if err := supervisor.Scan(); err != nil {
			t.Fatal(err)
		}
		clock.now = clock.now.Add(modelCrashScanInterval)
	}
	executor.sessions[0].Output = "assistant: making verified progress"
	executor.sessions[0].InstanceID = "run-recovered"
	executor.watchdog = healthyWatchdog(clock.now)
	if err := supervisor.Scan(); err != nil {
		t.Fatal(err)
	}
	if supervisor.state.Sessions[identity] == nil {
		t.Fatal("incident reset immediately instead of waiting for observed progress window")
	}

	clock.now = clock.now.Add(modelCrashProgressResetInterval - time.Second)
	executor.watchdog = healthyWatchdog(clock.now)
	if err := supervisor.Scan(); err != nil {
		t.Fatal(err)
	}
	if supervisor.state.Sessions[identity] == nil {
		t.Fatal("incident reset before 30m observed progress")
	}

	clock.now = clock.now.Add(time.Second)
	executor.watchdog = healthyWatchdog(clock.now)
	if err := supervisor.Scan(); err != nil {
		t.Fatal(err)
	}
	if supervisor.state.Sessions[identity] != nil {
		t.Fatalf("incident persisted after 30m observed progress: %#v", supervisor.state.Sessions[identity])
	}
}

func TestModelCrashSupervisorPersistsStateAndDeduplicatesAlerts(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	identity := "deacon"
	townRoot := t.TempDir()
	statePath := modelCrashStateFile(townRoot)
	clock := &fakeModelCrashClock{now: now}
	executor := &fakeModelCrashExecutor{
		sessions: []modelCrashSession{fatalLocalSession(identity, "deacon", "run-1")},
		watchdog: healthyWatchdog(now),
	}

	supervisor := newModelCrashSupervisor(statePath, clock, executor)
	if err := supervisor.Scan(); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(modelCrashScanInterval)
	executor.watchdog = healthyWatchdog(clock.now)
	if err := supervisor.Scan(); err != nil {
		t.Fatal(err)
	}

	reloaded := newModelCrashSupervisor(statePath, clock, executor)
	if err := reloaded.load(); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.state.Sessions[identity]; got == nil || got.LocalRestarts != 1 {
		t.Fatalf("durable local restart state = %#v", got)
	}
	health, err := LoadModelCrashSessionHealth(townRoot, executor.sessions[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if health.IncidentID == "" || health.RecoveryAction != "local-restart" || health.RecoveryExhausted {
		t.Fatalf("additive health state = %#v", health)
	}

	executor.sessions[0].InstanceID = "run-repeat"
	for i := 0; i < 4; i++ {
		executor.watchdog = healthyWatchdog(clock.now)
		if err := reloaded.Scan(); err != nil {
			t.Fatal(err)
		}
		clock.now = clock.now.Add(modelCrashScanInterval)
	}
	alerts := 0
	for _, action := range executor.actions {
		if action.kind == "alert" {
			alerts++
		}
	}
	if alerts != 1 {
		t.Fatalf("alert count = %d, want one repeat-crash exhaustion alert; actions=%#v", alerts, executor.actions)
	}
}

func TestModelCrashSupervisorReloadExhaustsPendingActionsWithoutReplay(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		action          string
		localRestarts   int
		goContinuations int
		controlProbes   int
	}{
		{action: "local-restart-pending", localRestarts: 1},
		{action: "go-continuation-pending", localRestarts: 1, goContinuations: 1},
		{action: "local-probe-pending", localRestarts: 1, controlProbes: 1},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			identity := "rig/polecats/toast"
			clock := &fakeModelCrashClock{now: now}
			executor := &fakeModelCrashExecutor{
				sessions:      []modelCrashSession{fatalLocalSession(identity, "polecat", "run-1")},
				watchdog:      healthyWatchdog(now),
				alertFailures: 1,
			}
			statePath := filepath.Join(t.TempDir(), "model-crash-supervisor.json")
			before := newModelCrashSupervisor(statePath, clock, executor)
			observationKey := "run-1|" + session.ModelCrashFatalFingerprint
			before.state.Sessions[identity] = &modelCrashSessionState{
				SessionName:           "tmux-" + identity,
				IncidentID:            "model-crash-interrupted",
				WorkUnit:              executor.sessions[0].WorkUnit,
				ObservationKey:        observationKey,
				HandledObservationKey: observationKey,
				LocalRestarts:         tt.localRestarts,
				GoContinuations:       tt.goContinuations,
				ControlProbes:         tt.controlProbes,
				RecoveryAction:        tt.action,
			}
			if err := before.save(); err != nil {
				t.Fatal(err)
			}

			reloaded := newModelCrashSupervisor(statePath, clock, executor)
			if err := reloaded.Scan(); err != nil {
				t.Fatal(err)
			}
			got := reloaded.state.Sessions[identity]
			if got == nil || !got.RecoveryExhausted || strings.HasSuffix(got.RecoveryAction, "-pending") {
				t.Fatalf("reloaded pending action was not exhausted: %#v", got)
			}
			for _, action := range executor.actions {
				if action.kind == "restart" {
					t.Fatalf("reloaded pending action was replayed: %#v", executor.actions)
				}
			}
			if len(reloaded.state.PendingAlerts) != 1 {
				t.Fatalf("failed interruption alert was not durable: %#v", reloaded.state)
			}

			executor.alertFailures = 0
			again := newModelCrashSupervisor(statePath, clock, executor)
			if err := again.Scan(); err != nil {
				t.Fatal(err)
			}
			if len(again.state.PendingAlerts) != 0 || len(again.state.Alerts) != 1 {
				t.Fatalf("interruption alert was not retried and deduplicated: %#v", again.state)
			}
			for _, action := range executor.actions {
				if action.kind == "restart" {
					t.Fatalf("pending action replayed after second reload: %#v", executor.actions)
				}
			}
		})
	}
}

func TestModelCrashSupervisorRetriesFailedAlertThenDeduplicatesSuccess(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	clock := &fakeModelCrashClock{now: now}
	executor := &fakeModelCrashExecutor{alertFailures: 1}
	supervisor := newTestModelCrashSupervisor(t, clock, executor)

	if supervisor.alertOnce("incident:exhausted", "recovery exhausted") {
		t.Fatal("failed alert reported as delivered")
	}
	if !supervisor.alertOnce("incident:exhausted", "recovery exhausted") {
		t.Fatal("retry after failed alert was suppressed")
	}
	if supervisor.alertOnce("incident:exhausted", "recovery exhausted") {
		t.Fatal("successful alert was not deduplicated")
	}
	if got := len(executor.actions); got != 2 {
		t.Fatalf("alert attempts = %d, want 2", got)
	}
}

func TestModelCrashRestartArgsUseSafeRolePrimitives(t *testing.T) {
	tests := []struct {
		session modelCrashSession
		want    string
	}{
		{session: modelCrashSession{Role: "mayor", Identity: "mayor"}, want: "mayor restart --agent opencode-local"},
		{session: modelCrashSession{Role: "deacon", Identity: "deacon"}, want: "deacon restart --agent opencode-local"},
		{session: modelCrashSession{Role: "witness", Identity: "rig/witness"}, want: "witness restart rig --agent opencode-local"},
		{session: modelCrashSession{Role: "refinery", Identity: "rig/refinery"}, want: "refinery restart rig --agent opencode-local"},
		{session: modelCrashSession{Role: "crew", Identity: "rig/crew/max"}, want: "crew restart rig/max --agent opencode-local"},
		{session: modelCrashSession{Role: "polecat", Identity: "rig/polecats/toast"}, want: "session restart rig/toast --force --agent opencode-local"},
	}
	for _, tt := range tests {
		args, err := modelCrashRestartArgs(tt.session, "opencode-local")
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(args, " "); got != tt.want {
			t.Fatalf("restart args = %q, want %q", got, tt.want)
		}
	}
	if _, err := modelCrashRestartArgs(modelCrashSession{Role: "boot", Identity: "deacon/boot"}, "opencode-local"); err == nil {
		t.Fatal("Boot unexpectedly has a model-crash restart primitive")
	}
}

func TestModelCrashContinuationRequiresExplicitWorktreePolicy(t *testing.T) {
	workDir := t.TempDir()
	executor := &daemonModelCrashExecutor{daemon: &Daemon{config: &Config{TownRoot: t.TempDir()}}}
	candidate := modelCrashSession{Role: "polecat", WorkDir: workDir}

	if executor.AllowsContinuation(candidate, "opencode-local", "opencode-go") {
		t.Fatal("missing policy authorized hosted continuation")
	}
	if err := os.MkdirAll(filepath.Join(workDir, ".gastown"), 0o755); err != nil {
		t.Fatal(err)
	}
	policy := `{
		"type": "model-escalation",
		"version": 1,
		"enabled": true,
		"rules": [{
			"from_agent": "opencode-local",
			"to_agent": "opencode-go",
			"promote_after_failures": 1
		}]
	}`
	if err := os.WriteFile(filepath.Join(workDir, ".gastown", "model-escalation.json"), []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	if !executor.AllowsContinuation(candidate, "opencode-local", "opencode-go") {
		t.Fatal("explicit local-to-Go worktree policy did not authorize continuation")
	}
}

func TestModelCrashAlertUsesDurableHumanRouteAndBestEffortNotification(t *testing.T) {
	if got := strings.Join(modelCrashHumanAlertArgs("subject", "message"), " "); got != "mail send --human -s subject -m message" {
		t.Fatalf("human alert args = %q", got)
	}
	var mailed, notified bool
	executor := &daemonModelCrashExecutor{
		sendHuman: func(subject, message string) error {
			mailed = subject != "" && message == "recovery exhausted"
			return nil
		},
		notify: func(subject, message string) error {
			notified = subject != "" && message == "recovery exhausted"
			return errors.New("notifications unavailable")
		},
	}
	if err := executor.Alert("incident:exhausted", "recovery exhausted"); err != nil {
		t.Fatalf("best-effort notification made durable alert fail: %v", err)
	}
	if !mailed || !notified {
		t.Fatalf("alert routes mailed=%v notified=%v, want both attempted", mailed, notified)
	}
}
