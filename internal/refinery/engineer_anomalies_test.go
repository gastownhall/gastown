package refinery

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

type fakeMRRefChecker struct {
	exists    map[string]bool
	contained bool
	cherry    string
	err       error
}

func (f fakeMRRefChecker) RefExists(ref string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.exists[ref], nil
}

func (f fakeMRRefChecker) IsAncestor(_, _ string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.contained, nil
}

func (f fakeMRRefChecker) Cherry(_, _ string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.cherry, nil
}

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

func TestMalformedMRBranchEvidence(t *testing.T) {
	tests := []struct {
		name      string
		checker   fakeMRRefChecker
		branch    string
		target    string
		wantParts []string
	}{
		{
			name:   "contained",
			branch: "polecat/fix",
			target: "main",
			checker: fakeMRRefChecker{exists: map[string]bool{
				"origin/polecat/fix": true,
				"origin/main":        true,
			}, contained: true},
			wantParts: []string{"branch_containment=contained", "branch_ref=origin/polecat/fix", "target_ref=origin/main"},
		},
		{
			name:   "uncontained with patch count",
			branch: "polecat/fix",
			target: "main",
			checker: fakeMRRefChecker{exists: map[string]bool{
				"origin/polecat/fix": true,
				"origin/main":        true,
			}, cherry: "+ abc\n- def\n+ fed\n"},
			wantParts: []string{"branch_containment=uncontained", "unpreserved_patches=2"},
		},
		{
			name:      "missing branch",
			branch:    "polecat/missing",
			target:    "main",
			checker:   fakeMRRefChecker{exists: map[string]bool{"origin/main": true}},
			wantParts: []string{"branch_containment=unknown", "reason=missing"},
		},
		{
			name:      "lookup error",
			branch:    "polecat/fix",
			target:    "main",
			checker:   fakeMRRefChecker{err: errors.New("git exploded")},
			wantParts: []string{"branch_containment=unknown", "lookup_error:git exploded"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := malformedMRBranchEvidence(tt.checker, tt.branch, tt.target)
			for _, want := range tt.wantParts {
				if !strings.Contains(got, want) {
					t.Fatalf("malformedMRBranchEvidence() = %q, want to contain %q", got, want)
				}
			}
		})
	}
}
