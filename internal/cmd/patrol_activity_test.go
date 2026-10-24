package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
)

func TestLatestPatrolActivity_PicksNewestPatrolWisp(t *testing.T) {
	now := time.Date(2026, 10, 1, 1, 20, 0, 0, time.UTC)
	issues := []*beads.Issue{
		{ID: "hq-work", Title: "some hooked task", UpdatedAt: "2026-10-01T01:19:00Z"},
		{ID: "hq-wisp-old", Title: constants.MolWitnessPatrol, UpdatedAt: "2026-09-30T10:00:00Z"},
		{ID: "hq-wisp-pps2", Title: constants.MolWitnessPatrol, UpdatedAt: "2026-09-30T16:25:39Z"},
		nil,
	}

	got := latestPatrolActivity(issues, constants.MolWitnessPatrol, now)
	if got == nil {
		t.Fatal("expected patrol activity, got nil")
	}
	if got.ID != "hq-wisp-pps2" {
		t.Errorf("ID = %q, want hq-wisp-pps2", got.ID)
	}
	if got.UpdatedAt != "2026-09-30T16:25:39Z" {
		t.Errorf("UpdatedAt = %q", got.UpdatedAt)
	}
	wantAge := int64(now.Sub(time.Date(2026, 9, 30, 16, 25, 39, 0, time.UTC)).Seconds())
	if got.AgeSeconds != wantAge {
		t.Errorf("AgeSeconds = %d, want %d", got.AgeSeconds, wantAge)
	}
}

func TestLatestPatrolActivity_FallsBackToCreatedAt(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 10, 0, 0, time.UTC)
	issues := []*beads.Issue{
		{ID: "hq-wisp-a", Title: constants.MolRefineryPatrol, CreatedAt: "2026-10-01T00:00:00Z"},
	}
	got := latestPatrolActivity(issues, constants.MolRefineryPatrol, now)
	if got == nil || got.AgeSeconds != 600 {
		t.Fatalf("got %+v, want age 600s from created_at", got)
	}
}

func TestLatestPatrolActivity_NoPatrolOrUnknownTime(t *testing.T) {
	now := time.Now()
	if got := latestPatrolActivity([]*beads.Issue{
		{ID: "hq-x", Title: "not a patrol"},
	}, constants.MolWitnessPatrol, now); got != nil {
		t.Errorf("expected nil without a patrol wisp, got %+v", got)
	}

	got := latestPatrolActivity([]*beads.Issue{
		{ID: "hq-wisp-z", Title: constants.MolWitnessPatrol, UpdatedAt: "garbage"},
	}, constants.MolWitnessPatrol, now)
	if got == nil || got.UpdatedAt != "" || got.AgeSeconds != 0 {
		t.Errorf("unparseable timestamp should yield unknown age, got %+v", got)
	}
	if s := formatPatrolActivity(got); !strings.Contains(s, "unknown") {
		t.Errorf("formatPatrolActivity = %q, want unknown marker", s)
	}
}

func TestFormatPatrolActivity(t *testing.T) {
	if got := formatPatrolActivity(nil); got != "(no patrol hooked)" {
		t.Errorf("nil = %q", got)
	}
	got := formatPatrolActivity(&PatrolActivity{ID: "hq-wisp-1", UpdatedAt: "x", AgeSeconds: 3725})
	if got != "hq-wisp-1 (last update 1h2m5s ago)" {
		t.Errorf("got %q", got)
	}
}

func TestPatrolReportNextStep(t *testing.T) {
	for _, role := range []string{"witness", "refinery"} {
		next := patrolReportNextStep(role)
		if !strings.Contains(next, "Do NOT end your turn") || !strings.Contains(next, "first step") {
			t.Errorf("%s next step must tell the agent to start the next cycle without ending its turn, got %q", role, next)
		}
	}
	if next := patrolReportNextStep("deacon"); !strings.Contains(next, "gt handoff") {
		t.Errorf("deacon next step must require gt handoff, got %q", next)
	}
	if next := patrolReportNextStep("polecat"); next != "" {
		t.Errorf("non-patrol role should get no hint, got %q", next)
	}
}
