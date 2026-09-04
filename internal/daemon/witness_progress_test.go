package daemon

import (
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/agenthealth"
)

// TestWitnessActionFor_StalledWitnessDoesNotStandDaemonDown is the spawn-guard
// half of gt-o57: a witness whose session exists but whose hooked work has not
// advanced past the threshold must not be passed as healthy.
func TestWitnessActionFor_StalledWitnessDoesNotStandDaemonDown(t *testing.T) {
	stalled := agenthealth.Assess(agenthealth.Input{
		SessionAlive: true,
		HookedWork: []agenthealth.Work{{
			ID:        "gt-wisp-g3dxmv",
			CreatedAt: time.Now().Add(-21 * time.Hour),
			UpdatedAt: time.Now().Add(-21 * time.Hour),
		}},
		Threshold: 30 * time.Minute,
	})

	if stalled.State != agenthealth.StateDegraded {
		t.Fatalf("State = %q, want %q", stalled.State, agenthealth.StateDegraded)
	}
	if got := witnessActionFor(stalled.State); got != witnessActionRecover {
		t.Errorf("witnessActionFor(degraded) = %v, want witnessActionRecover", got)
	}
}

// TestWitnessActionFor_FrozenMatchesCrashed pins the inversion fix: the frozen
// witness and the crashed one get the same direction of response. If degraded
// ever maps to skip-spawn again, the original bug is back.
func TestWitnessActionFor_FrozenMatchesCrashed(t *testing.T) {
	crashed := witnessActionFor(agenthealth.StateStopped)
	frozen := witnessActionFor(agenthealth.StateDegraded)
	if crashed != frozen {
		t.Errorf("crashed action = %v, frozen action = %v; the frozen case must be handled at least as well as the crashed case", crashed, frozen)
	}
}

func TestWitnessActionFor(t *testing.T) {
	cases := []struct {
		state agenthealth.State
		want  witnessAction
	}{
		{agenthealth.StateHealthy, witnessActionSkipSpawn},
		{agenthealth.StateDegraded, witnessActionRecover},
		{agenthealth.StateStopped, witnessActionRecover},
		{agenthealth.StateUnknown, witnessActionReportUnknown},
		{agenthealth.State("something new"), witnessActionReportUnknown},
	}
	for _, tc := range cases {
		if got := witnessActionFor(tc.state); got != tc.want {
			t.Errorf("witnessActionFor(%q) = %v, want %v", tc.state, got, tc.want)
		}
	}
}

func TestStallTracker_Cooldown(t *testing.T) {
	tracker := newStallTracker()

	if !tracker.shouldNudge("gastown") {
		t.Fatal("first nudge suppressed")
	}
	if tracker.shouldNudge("gastown") {
		t.Error("second nudge fired inside the cooldown window")
	}
	if !tracker.shouldNudge("other-rig") {
		t.Error("cooldown leaked across rigs")
	}

	// A recovered witness resets the window so the next stall alerts promptly.
	tracker.clear("gastown")
	if !tracker.shouldNudge("gastown") {
		t.Error("nudge still suppressed after clear")
	}
}

func TestStallTracker_NotifyHasItsOwnWindow(t *testing.T) {
	tracker := newStallTracker()
	if !tracker.shouldNudge("gastown") {
		t.Fatal("first nudge suppressed")
	}
	if !tracker.shouldNotify("gastown") {
		t.Error("notify suppressed by the nudge window; the two must be independent")
	}
	if tracker.shouldNotify("gastown") {
		t.Error("second notify fired inside the cooldown window")
	}
}

// TestStallTracker_NilIsPermissive keeps a Daemon built without a tracker (as
// tests do) from silently swallowing stall signals.
func TestStallTracker_NilIsPermissive(t *testing.T) {
	var tracker *stallTracker
	if !tracker.shouldNudge("gastown") || !tracker.shouldNotify("gastown") {
		t.Error("nil tracker suppressed a stall signal")
	}
	tracker.clear("gastown") // must not panic
}

// TestWitnessActionFor_AgreesWithSuppressesSpawn keeps the daemon's switch and
// the detector's own spawn rule from drifting apart: a state that suppresses a
// spawn must map to skip, and one that does not must never map to skip.
func TestWitnessActionFor_AgreesWithSuppressesSpawn(t *testing.T) {
	states := []agenthealth.State{
		agenthealth.StateHealthy,
		agenthealth.StateDegraded,
		agenthealth.StateStopped,
		agenthealth.StateUnknown,
	}
	for _, state := range states {
		suppresses := agenthealth.Assessment{State: state}.SuppressesSpawn()
		skips := witnessActionFor(state) == witnessActionSkipSpawn
		if suppresses != skips {
			t.Errorf("state %q: SuppressesSpawn()=%v but witnessActionFor gives skip=%v", state, suppresses, skips)
		}
	}
}
