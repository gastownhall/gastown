package dog

import (
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/tmux"
)

// mockSessionChecker implements sessionChecker for testing.
type mockSessionChecker struct {
	healthResults  map[string]tmux.ZombieStatus // session -> status
	sessionsAlive  map[string]bool              // session -> exists
	killedSessions []string
}

func newMockChecker() *mockSessionChecker {
	return &mockSessionChecker{
		healthResults: make(map[string]tmux.ZombieStatus),
		sessionsAlive: make(map[string]bool),
	}
}

func (m *mockSessionChecker) CheckSessionHealth(session string, _ time.Duration) tmux.ZombieStatus {
	if s, ok := m.healthResults[session]; ok {
		return s
	}
	return tmux.SessionDead
}

func (m *mockSessionChecker) HasSession(name string) (bool, error) {
	return m.sessionsAlive[name], nil
}

func (m *mockSessionChecker) KillSession(name string) error {
	m.killedSessions = append(m.killedSessions, name)
	return nil
}

// =============================================================================
// Healthy dogs
// =============================================================================

func TestHealth_IdleDog_NoSession(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateIdle, LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, false)

	if r.NeedsAttention {
		t.Error("idle dog with no session should not need attention")
	}
	if r.SessionStatus != "none" {
		t.Errorf("session_status = %q, want 'none'", r.SessionStatus)
	}
	if r.WorkDuration != 0 {
		t.Errorf("work_duration = %v, want 0", r.WorkDuration)
	}
}

func TestHealth_WorkingDog_Healthy(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	workStart := now.Add(-10 * time.Minute)
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateWorking, Work: "task-1",
		WorkStartedAt: workStart, LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.healthResults["hq-dog-alpha"] = tmux.SessionHealthy
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, false)

	if r.NeedsAttention {
		t.Error("healthy working dog should not need attention")
	}
	if r.SessionStatus != "healthy" {
		t.Errorf("session_status = %q, want 'healthy'", r.SessionStatus)
	}
	if r.WorkDuration < 10*time.Minute {
		t.Errorf("work_duration = %v, want >= 10m", r.WorkDuration)
	}
}

// =============================================================================
// Zombies
// =============================================================================

func TestHealth_Zombie_SessionDead(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateWorking, Work: "task-1",
		WorkStartedAt: now.Add(-1 * time.Hour), LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.healthResults["hq-dog-alpha"] = tmux.SessionDead
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, false)

	if !r.NeedsAttention {
		t.Error("zombie (SessionDead) should need attention")
	}
	if r.AutoCleared {
		t.Error("should not auto-clear when autoClear=false")
	}
}

func TestHealth_Zombie_AgentDead(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateWorking, Work: "task-1",
		WorkStartedAt: now.Add(-1 * time.Hour), LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.healthResults["hq-dog-alpha"] = tmux.AgentDead
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, false)

	if !r.NeedsAttention {
		t.Error("zombie (AgentDead) should need attention")
	}
	if r.AutoCleared {
		t.Error("should not auto-clear when autoClear=false")
	}
}

// =============================================================================
// Hung
// =============================================================================

func TestHealth_Hung_ReportOnly(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateWorking, Work: "task-1",
		WorkStartedAt: now.Add(-2 * time.Hour), LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.healthResults["hq-dog-alpha"] = tmux.AgentHung
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, false) // autoClear=false: report only

	if !r.NeedsAttention {
		t.Error("hung dog should need attention")
	}
	if r.AutoCleared {
		t.Error("hung dog should NOT be auto-cleared when autoClear=false")
	}
	if r.SessionStatus != "agent-hung" {
		t.Errorf("session_status = %q, want 'agent-hung'", r.SessionStatus)
	}
}

func TestHealth_Hung_AutoCleared(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateWorking, Work: "plugin:task-1", WorkKind: WorkKindPlugin,
		WorkStartedAt: now.Add(-2 * time.Hour), LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.healthResults["hq-dog-alpha"] = tmux.AgentHung
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, true) // autoClear=true: kill and reclaim

	if !r.NeedsAttention {
		t.Error("hung dog should need attention")
	}
	if !r.AutoCleared {
		t.Error("hung dog should be auto-cleared when autoClear=true")
	}
	if len(mc.killedSessions) != 1 || mc.killedSessions[0] != "hq-dog-alpha" {
		t.Errorf("killedSessions = %v, want [hq-dog-alpha]", mc.killedSessions)
	}

	// Verify state was cleared
	d2, _ := m.Get("alpha")
	if d2.State != StateIdle {
		t.Errorf("state = %q, want idle after auto-clear", d2.State)
	}
}

// =============================================================================
// Auto-clear zombies
// =============================================================================

func TestHealth_AutoClear_SessionDead(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateWorking, Work: "plugin:task-1", WorkKind: WorkKindPlugin,
		WorkStartedAt: now.Add(-1 * time.Hour), LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.healthResults["hq-dog-alpha"] = tmux.SessionDead
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, true)

	if !r.AutoCleared {
		t.Error("zombie (SessionDead) should be auto-cleared")
	}

	// Verify state was actually cleared
	d2, _ := m.Get("alpha")
	if d2.State != StateIdle {
		t.Errorf("state = %q, want idle after auto-clear", d2.State)
	}
	if d2.Work != "" {
		t.Errorf("work = %q, want empty after auto-clear", d2.Work)
	}
}

func TestHealth_AutoClear_AgentDead(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateWorking, Work: "plugin:task-1", WorkKind: WorkKindPlugin,
		WorkStartedAt: now.Add(-1 * time.Hour), LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.healthResults["hq-dog-alpha"] = tmux.AgentDead
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, true)

	if !r.AutoCleared {
		t.Error("zombie (AgentDead) should be auto-cleared")
	}

	// Verify session was killed
	if len(mc.killedSessions) != 1 || mc.killedSessions[0] != "hq-dog-alpha" {
		t.Errorf("killedSessions = %v, want [hq-dog-alpha]", mc.killedSessions)
	}

	// Verify state was cleared
	d2, _ := m.Get("alpha")
	if d2.State != StateIdle {
		t.Errorf("state = %q, want idle after auto-clear", d2.State)
	}
}

// =============================================================================
// Orphan sessions
// =============================================================================

func TestHealth_Orphan_IdleWithSession(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateIdle, LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.sessionsAlive["hq-dog-alpha"] = true
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, false)

	if !r.NeedsAttention {
		t.Error("orphan session should need attention")
	}
	if r.SessionStatus != "orphan" {
		t.Errorf("session_status = %q, want 'orphan'", r.SessionStatus)
	}
}

func TestHealth_Orphan_AutoCleared(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateIdle, LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.sessionsAlive["hq-dog-alpha"] = true
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, true) // autoClear=true: kill orphan session

	if !r.NeedsAttention {
		t.Error("orphan session should need attention")
	}
	if !r.AutoCleared {
		t.Error("orphan session should be auto-cleared when autoClear=true")
	}
	if len(mc.killedSessions) != 1 || mc.killedSessions[0] != "hq-dog-alpha" {
		t.Errorf("killedSessions = %v, want [hq-dog-alpha]", mc.killedSessions)
	}
}

// =============================================================================
// WorkDuration computation
// =============================================================================

func TestHealth_WorkDuration_ZeroStartedAt(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	// Working dog with zero WorkStartedAt (legacy state file)
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateWorking, Work: "task-1",
		LastActive: now, CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.healthResults["hq-dog-alpha"] = tmux.SessionHealthy
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, false)

	if r.WorkDuration != 0 {
		t.Errorf("work_duration = %v, want 0 for zero WorkStartedAt", r.WorkDuration)
	}
}

// =============================================================================
// CheckAll
// =============================================================================

func TestHealth_CheckAll_MultipleDogs(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()

	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateIdle, LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})
	setupDogWithState(t, m, "beta", &DogState{
		Name: "beta", State: StateWorking, Work: "task-1",
		WorkStartedAt: now.Add(-1 * time.Hour), LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.healthResults["hq-dog-beta"] = tmux.SessionDead // zombie
	hc := NewHealthChecker(m, mc)

	results, err := hc.CheckAll(30*time.Minute, false)
	if err != nil {
		t.Fatalf("CheckAll() error = %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("CheckAll() returned %d results, want 2", len(results))
	}

	attention := NeedsAttentionCount(results)
	if attention != 1 {
		t.Errorf("NeedsAttentionCount = %d, want 1", attention)
	}
}

// =============================================================================
// NeedsAttentionCount
// =============================================================================

func TestNeedsAttentionCount(t *testing.T) {
	results := []DogHealthResult{
		{Name: "a", NeedsAttention: false},
		{Name: "b", NeedsAttention: true},
		{Name: "c", NeedsAttention: true},
		{Name: "d", NeedsAttention: false},
	}

	if got := NeedsAttentionCount(results); got != 2 {
		t.Errorf("NeedsAttentionCount = %d, want 2", got)
	}

	if got := NeedsAttentionCount(nil); got != 0 {
		t.Errorf("NeedsAttentionCount(nil) = %d, want 0", got)
	}
}
