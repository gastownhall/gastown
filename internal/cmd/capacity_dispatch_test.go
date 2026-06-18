package cmd

import (
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

func TestShouldFireCrossRigEscalation_Debounces(t *testing.T) {
	resetCrossRigEscalationStateForTest()
	t.Cleanup(resetCrossRigEscalationStateForTest)

	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	if !shouldFireCrossRigEscalation("walletui", "hq", now) {
		t.Fatalf("first call must fire")
	}
	// Second call inside the debounce window must NOT fire.
	if shouldFireCrossRigEscalation("walletui", "hq", now.Add(30*time.Minute)) {
		t.Fatalf("second call inside debounce window must not fire")
	}
	// After the debounce window elapses, fire again.
	if !shouldFireCrossRigEscalation("walletui", "hq", now.Add(crossRigEscalationDebounce+time.Minute)) {
		t.Fatalf("call past debounce window must fire")
	}
}

func TestShouldFireCrossRigEscalation_KeyedByRigAndPrefix(t *testing.T) {
	resetCrossRigEscalationStateForTest()
	t.Cleanup(resetCrossRigEscalationStateForTest)

	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)

	if !shouldFireCrossRigEscalation("walletui", "hq", now) {
		t.Fatalf("walletui/hq first call must fire")
	}
	// Different rig — should fire independently.
	if !shouldFireCrossRigEscalation("furiosa", "hq", now) {
		t.Fatalf("furiosa/hq must fire (different rig)")
	}
	// Different prefix on same rig — should fire independently.
	if !shouldFireCrossRigEscalation("walletui", "wisp", now) {
		t.Fatalf("walletui/wisp must fire (different prefix)")
	}
	// Same (rig, prefix) repeats — debounced.
	if shouldFireCrossRigEscalation("walletui", "hq", now.Add(time.Minute)) {
		t.Fatalf("walletui/hq repeat must not fire")
	}
}

func TestValidatePendingBeadForDispatchRejectsNonConcreteWork(t *testing.T) {
	bad := capacity.PendingBead{
		ID:         "ctx-1",
		WorkBeadID: "gt-wisp-abc",
		IssueType:  "task",
		Ephemeral:  true,
	}
	err := validatePendingBeadForDispatch("", bad, false)
	if !errors.Is(err, errNonConcreteWorkBead) {
		t.Fatalf("validatePendingBeadForDispatch error = %v, want errNonConcreteWorkBead", err)
	}
}

func TestConcreteWorkAssessmentForScheduledBeadInfo(t *testing.T) {
	info := beadStatusInfo{Status: "open", IssueType: "task", Labels: []string{"gt:sling-context"}, Ephemeral: true}
	if got := concreteWorkAssessment("gt-wisp-ctx", info); got.Concrete {
		t.Fatalf("concreteWorkAssessment = concrete, want non-concrete")
	}
}
