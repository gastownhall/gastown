package agenthealth

import (
	"errors"
	"testing"
	"time"
)

var now = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

func hooked(id string, updatedAgo time.Duration) Work {
	return Work{ID: id, CreatedAt: now.Add(-updatedAgo), UpdatedAt: now.Add(-updatedAgo)}
}

// TestAssess_StalledButAliveIsNotHealthy is the acceptance case from gt-o57:
// the session exists and the hooked wisp is older than the threshold, so the
// agent must NOT be reported healthy and must NOT suppress its own spawn.
func TestAssess_StalledButAliveIsNotHealthy(t *testing.T) {
	got := Assess(Input{
		SessionAlive: true,
		HookedWork:   []Work{hooked("gt-wisp-g3dxmv", 21*time.Hour)},
		Threshold:    30 * time.Minute,
		Now:          now,
	})

	if got.State != StateDegraded {
		t.Fatalf("State = %q, want %q (reason: %s)", got.State, StateDegraded, got.Reason)
	}
	if got.SuppressesSpawn() {
		t.Error("SuppressesSpawn() = true; a stalled agent must not suppress its own replacement")
	}
	if !got.NeedsAttention() {
		t.Error("NeedsAttention() = false, want true")
	}
	if !got.HookIdleKnown || got.HookIdle != 21*time.Hour {
		t.Errorf("HookIdle = %v (known=%v), want 21h", got.HookIdle, got.HookIdleKnown)
	}
	if got.HookBeadID != "gt-wisp-g3dxmv" {
		t.Errorf("HookBeadID = %q, want gt-wisp-g3dxmv", got.HookBeadID)
	}
	if want := "running, hook idle 21h"; got.Describe() != want {
		t.Errorf("Describe() = %q, want %q", got.Describe(), want)
	}
}

// TestAssess_FrozenHandledAtLeastAsWellAsCrashed guards the inversion that made
// the original bug worse than a gap: the old guard respawned a CRASHED agent
// but protected a FROZEN one. Neither state may suppress a spawn.
func TestAssess_FrozenHandledAtLeastAsWellAsCrashed(t *testing.T) {
	crashed := Assess(Input{SessionAlive: false, Threshold: 30 * time.Minute, Now: now})
	frozen := Assess(Input{
		SessionAlive: true,
		HookedWork:   []Work{hooked("gt-wisp-1", 72*time.Hour)},
		Threshold:    30 * time.Minute,
		Now:          now,
	})

	if crashed.SuppressesSpawn() {
		t.Error("crashed agent suppressed spawn")
	}
	if frozen.SuppressesSpawn() {
		t.Error("frozen agent suppressed spawn — the original defect has been reintroduced")
	}
	if crashed.State != StateStopped {
		t.Errorf("crashed State = %q, want %q", crashed.State, StateStopped)
	}
}

func TestAssess_Undecidable(t *testing.T) {
	cases := []struct {
		name string
		in   Input
	}{
		{"session liveness unknown", Input{SessionErr: errors.New("tmux down"), Threshold: time.Hour, Now: now}},
		{"hook unreadable", Input{SessionAlive: true, WorkErr: errors.New("dolt down"), Threshold: time.Hour, Now: now}},
		{"no threshold configured", Input{SessionAlive: true, HookedWork: []Work{hooked("gt-1", time.Minute)}, Now: now}},
		{"hooked work has no timestamp", Input{SessionAlive: true, HookedWork: []Work{{ID: "gt-1"}}, Threshold: time.Hour, Now: now}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Assess(tc.in)
			if got.State != StateUnknown {
				t.Errorf("State = %q, want %q (reason: %s)", got.State, StateUnknown, got.Reason)
			}
			if got.SuppressesSpawn() {
				t.Error("an undecidable check suppressed a spawn; it must never assert the reassuring answer")
			}
			if !got.NeedsAttention() {
				t.Error("NeedsAttention() = false, want true")
			}
		})
	}
}

func TestAssess_Healthy(t *testing.T) {
	cases := []struct {
		name string
		in   Input
	}{
		{"hook advanced recently", Input{
			SessionAlive: true,
			HookedWork:   []Work{hooked("gt-1", time.Minute)},
			Threshold:    30 * time.Minute,
			Now:          now,
		}},
		{"nothing on hook", Input{SessionAlive: true, Threshold: 30 * time.Minute, Now: now}},
		{"one stale bead but another advancing", Input{
			SessionAlive: true,
			HookedWork:   []Work{hooked("gt-old", 40*time.Hour), hooked("gt-new", time.Minute)},
			Threshold:    30 * time.Minute,
			Now:          now,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Assess(tc.in)
			if got.State != StateHealthy {
				t.Errorf("State = %q, want %q (reason: %s)", got.State, StateHealthy, got.Reason)
			}
			if !got.SuppressesSpawn() {
				t.Error("SuppressesSpawn() = false, want true")
			}
			if got.NeedsAttention() {
				t.Error("NeedsAttention() = true, want false")
			}
		})
	}
}

// TestAssess_ThresholdIsAnInput pins the calibration knob: the same unadvancing
// hook reads healthy or degraded depending only on the configured threshold, so
// the meaning of the signal can be changed without rewriting the detector.
func TestAssess_ThresholdIsAnInput(t *testing.T) {
	work := []Work{hooked("gt-wisp-1", 2*time.Hour)}

	lenient := Assess(Input{SessionAlive: true, HookedWork: work, Threshold: 24 * time.Hour, Now: now})
	if lenient.State != StateHealthy {
		t.Errorf("lenient threshold: State = %q, want %q", lenient.State, StateHealthy)
	}

	strict := Assess(Input{SessionAlive: true, HookedWork: work, Threshold: 30 * time.Minute, Now: now})
	if strict.State != StateDegraded {
		t.Errorf("strict threshold: State = %q, want %q", strict.State, StateDegraded)
	}
}

func TestAssess_ExactlyAtThresholdIsDegraded(t *testing.T) {
	got := Assess(Input{
		SessionAlive: true,
		HookedWork:   []Work{hooked("gt-1", 30*time.Minute)},
		Threshold:    30 * time.Minute,
		Now:          now,
	})
	if got.State != StateDegraded {
		t.Errorf("State = %q, want %q", got.State, StateDegraded)
	}
}

func TestAssess_ClockSkewDoesNotUnderflow(t *testing.T) {
	got := Assess(Input{
		SessionAlive: true,
		HookedWork:   []Work{{ID: "gt-1", UpdatedAt: now.Add(time.Hour)}},
		Threshold:    30 * time.Minute,
		Now:          now,
	})
	if got.State != StateHealthy {
		t.Errorf("State = %q, want %q", got.State, StateHealthy)
	}
	if got.HookIdle != 0 {
		t.Errorf("HookIdle = %v, want 0", got.HookIdle)
	}
}

func TestWorkProgressFallsBackToCreatedAt(t *testing.T) {
	w := Work{ID: "gt-1", CreatedAt: now.Add(-time.Hour)}
	if !w.Progress().Equal(now.Add(-time.Hour)) {
		t.Errorf("Progress() = %v, want created_at", w.Progress())
	}
}

func TestFormatIdle(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{45 * time.Second, "45s"},
		{12 * time.Minute, "12m"},
		{3 * time.Hour, "3h"},
		{21 * time.Hour, "21h"},
		{79*time.Hour + 30*time.Minute, "3d"},
		{-time.Second, "0s"},
	}
	for _, tc := range cases {
		if got := FormatIdle(tc.in); got != tc.want {
			t.Errorf("FormatIdle(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
