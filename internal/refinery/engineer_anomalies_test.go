package refinery

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestDetectQueueAnomalies_StaleClaim(t *testing.T) {
	now := time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC)
	issues := []*beads.Issue{
		{
			ID:        "gt-warn",
			Status:    "open",
			Assignee:  "rig/refinery-1",
			UpdatedAt: now.Add(-3 * time.Hour).Format(time.RFC3339),
			Description: `branch: polecat/warn
target: main
worker: nux`,
		},
		{
			ID:        "gt-critical",
			Status:    "open",
			Assignee:  "rig/refinery-2",
			UpdatedAt: now.Add(-7 * time.Hour).Format(time.RFC3339),
			Description: `branch: polecat/critical
target: main
worker: nux`,
		},
	}

	anomalies := detectQueueAnomalies(issues, now, 2*time.Hour, func(branch string) (bool, bool, error) {
		return true, false, nil
	})

	if len(anomalies) != 2 {
		t.Fatalf("expected 2 anomalies, got %d", len(anomalies))
	}
	if anomalies[0].Type != "stale-claim" || anomalies[1].Type != "stale-claim" {
		t.Fatalf("expected stale-claim anomalies, got %+v", anomalies)
	}

	// ZFC: anomalies report raw data (type + age), no severity classification.
	// Agents classify severity from the age field.
	got := map[string]time.Duration{}
	for _, a := range anomalies {
		got[a.ID] = a.Age
	}
	if got["gt-warn"] < 3*time.Hour {
		t.Fatalf("gt-warn age = %v, want >= 3h", got["gt-warn"])
	}
	if got["gt-critical"] < 7*time.Hour {
		t.Fatalf("gt-critical age = %v, want >= 7h", got["gt-critical"])
	}
}

func TestDetectQueueAnomalies_OrphanedBranch(t *testing.T) {
	now := time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC)
	issues := []*beads.Issue{
		{
			ID:        "gt-orphan",
			Status:    "open",
			UpdatedAt: now.Add(-30 * time.Minute).Format(time.RFC3339),
			Description: `branch: polecat/orphan
target: main
worker: nux`,
		},
		{
			ID:        "gt-ok",
			Status:    "open",
			UpdatedAt: now.Add(-30 * time.Minute).Format(time.RFC3339),
			Description: `branch: polecat/ok
target: main
worker: nux`,
		},
	}

	anomalies := detectQueueAnomalies(issues, now, 2*time.Hour, func(branch string) (bool, bool, error) {
		if branch == "polecat/orphan" {
			return false, false, nil
		}
		return false, true, nil
	})

	if len(anomalies) != 1 {
		t.Fatalf("expected 1 anomaly, got %d (%+v)", len(anomalies), anomalies)
	}
	if anomalies[0].Type != "orphaned-branch" {
		t.Fatalf("anomaly type = %q, want orphaned-branch", anomalies[0].Type)
	}
	// ZFC: no severity field — agent classifies from type + context.
	if anomalies[0].ID != "gt-orphan" {
		t.Fatalf("anomaly ID = %q, want gt-orphan", anomalies[0].ID)
	}
}

func TestDetectQueueAnomalies_MalformedMRFields(t *testing.T) {
	now := time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC)
	issues := []*beads.Issue{
		{
			ID:          "gt-missing-fields",
			Status:      "open",
			Description: "created by older tooling before MR fields existed",
		},
		{
			ID:     "gt-missing-branch",
			Status: "open",
			Description: `target: main
source_issue: gt-src`,
		},
	}

	anomalies := detectQueueAnomalies(issues, now, 2*time.Hour, func(branch string) (bool, bool, error) {
		t.Fatalf("branch existence should not be checked for malformed MR fields, got %q", branch)
		return false, false, nil
	})

	if len(anomalies) != 2 {
		t.Fatalf("expected 2 anomalies, got %d (%+v)", len(anomalies), anomalies)
	}
	for _, anomaly := range anomalies {
		if anomaly.Type != "malformed-mr" {
			t.Fatalf("anomaly type = %q, want malformed-mr", anomaly.Type)
		}
		if !strings.Contains(anomaly.Detail, "MR bead") {
			t.Fatalf("anomaly detail missing MR context: %+v", anomaly)
		}
	}
}
