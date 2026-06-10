package cmd

import (
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/tmux"
)

func TestClassifyPTNLanesRequiresLiveIssueHealthySessionAndUniqueHook(t *testing.T) {
	summary := classifyPTNLanes([]ptnLaneInput{
		{
			Name:          "productive",
			HookBead:      "ptn-ok",
			AgentState:    "working",
			IssueStatus:   "in_progress",
			SessionStatus: tmux.SessionHealthy.String(),
		},
		{
			Name:          "dead",
			HookBead:      "ptn-dead",
			AgentState:    "working",
			IssueStatus:   "open",
			SessionStatus: tmux.SessionDead.String(),
		},
		{
			Name:          "empty",
			AgentState:    "working",
			SessionStatus: tmux.SessionHealthy.String(),
		},
		{
			Name:          "dup-a",
			HookBead:      "ptn-dup",
			AgentState:    "working",
			IssueStatus:   "open",
			SessionStatus: tmux.SessionHealthy.String(),
		},
		{
			Name:          "dup-b",
			HookBead:      "ptn-dup",
			AgentState:    "working",
			IssueStatus:   "open",
			SessionStatus: tmux.SessionHealthy.String(),
		},
		{
			Name:          "review",
			HookBead:      "ptn-review",
			AgentState:    "review-needed",
			IssueStatus:   "open",
			SessionStatus: tmux.SessionHealthy.String(),
		},
		{
			Name:          "closed",
			HookBead:      "ptn-closed",
			AgentState:    "working",
			IssueStatus:   "closed",
			SessionStatus: tmux.SessionHealthy.String(),
		},
	})

	if summary.Productive != 1 {
		t.Fatalf("Productive = %d, want 1", summary.Productive)
	}
	byName := lanesByName(summary.Lanes)
	assertLaneReason(t, byName["dead"], tmux.SessionDead.String())
	assertLaneReason(t, byName["empty"], "no-issue")
	assertLaneReason(t, byName["dup-a"], "duplicate-hook")
	assertLaneReason(t, byName["dup-b"], "duplicate-hook")
	assertLaneReason(t, byName["review"], "review-needed")
	assertLaneReason(t, byName["closed"], "non-live-issue")
	if !byName["productive"].Productive {
		t.Fatalf("productive lane marked unhealthy: %#v", byName["productive"])
	}
}

func TestPTNNoPushDriftRequiresBacklogAndThreshold(t *testing.T) {
	now := time.Unix(10_000, 0)
	oldCommit := now.Add(-1 * time.Hour)
	freshCommit := now.Add(-10 * time.Minute)
	threshold := 45 * time.Minute

	if ptnNoPushDrift(now, oldCommit, threshold, 0, 0) {
		t.Fatal("no-push drift fired without ready work or open MRs")
	}
	if !ptnNoPushDrift(now, oldCommit, threshold, 1, 0) {
		t.Fatal("no-push drift did not fire with ready work")
	}
	if !ptnNoPushDrift(now, oldCommit, threshold, 0, 1) {
		t.Fatal("no-push drift did not fire with open MRs")
	}
	if ptnNoPushDrift(now, freshCommit, threshold, 1, 0) {
		t.Fatal("no-push drift fired for a fresh commit")
	}
	if ptnNoPushDrift(now, oldCommit, 0, 1, 0) {
		t.Fatal("no-push drift fired when disabled")
	}
	if ptnNoPushDrift(now, time.Time{}, threshold, 1, 0) {
		t.Fatal("no-push drift fired without a known last commit")
	}
}

func TestPTNDriftEscalationIsBoundedByAttemptsAndCooldown(t *testing.T) {
	now := time.Unix(20_000, 0)
	cooldown := 30 * time.Minute

	if shouldEscalatePTNDrift(ptnControllerState{ConsecutiveDrift: 2}, now, 3, cooldown) {
		t.Fatal("escalated before max attempts")
	}
	if !shouldEscalatePTNDrift(ptnControllerState{ConsecutiveDrift: 3}, now, 3, cooldown) {
		t.Fatal("did not escalate at max attempts")
	}
	recent := ptnControllerState{
		ConsecutiveDrift: 3,
		LastEscalationAt: now.Add(-10 * time.Minute),
	}
	if shouldEscalatePTNDrift(recent, now, 3, cooldown) {
		t.Fatal("escalated during cooldown")
	}
	cooled := ptnControllerState{
		ConsecutiveDrift: 3,
		LastEscalationAt: now.Add(-31 * time.Minute),
	}
	if !shouldEscalatePTNDrift(cooled, now, 3, cooldown) {
		t.Fatal("did not escalate after cooldown")
	}
}

func TestPTNDesiredStateDriftRequiresBacklogForUnderTarget(t *testing.T) {
	report := &ptnControllerReport{
		TargetProductive: 9,
		ProductiveLanes:  3,
		Services: map[string]ptnServiceStatus{
			"deacon":   {Running: true},
			"witness":  {Running: true},
			"refinery": {Running: true},
		},
	}
	if ptnDesiredStateDrift(report) {
		t.Fatal("under-target idle state drifted without ready work")
	}
	report.ReadyWork = 1
	if !ptnDesiredStateDrift(report) {
		t.Fatal("under-target state with ready work did not drift")
	}
}

func lanesByName(lanes []ptnLaneStatus) map[string]ptnLaneStatus {
	byName := make(map[string]ptnLaneStatus, len(lanes))
	for _, lane := range lanes {
		byName[lane.Name] = lane
	}
	return byName
}

func assertLaneReason(t *testing.T, lane ptnLaneStatus, reason string) {
	t.Helper()
	for _, got := range lane.Reasons {
		if got == reason {
			return
		}
	}
	t.Fatalf("lane %s reasons = %v, want %q", lane.Name, lane.Reasons, reason)
}
