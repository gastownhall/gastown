package dog

import (
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestAgentID(t *testing.T) {
	if got := AgentID("alpha"); got != "deacon/dogs/alpha" {
		t.Errorf("AgentID(alpha) = %q, want 'deacon/dogs/alpha'", got)
	}
}

func TestFirstHookedWork(t *testing.T) {
	tests := []struct {
		name     string
		issues   []*beads.Issue
		assignee string
		wantID   string
	}{
		{
			name:     "no issues",
			issues:   nil,
			assignee: "deacon/dogs/alpha",
		},
		{
			name: "matching assignee",
			issues: []*beads.Issue{
				{ID: "hq-wisp-03ph", Title: "Reap stale wisps", Assignee: "deacon/dogs/alpha"},
			},
			assignee: "deacon/dogs/alpha",
			wantID:   "hq-wisp-03ph",
		},
		{
			name: "another dog's work is ignored",
			issues: []*beads.Issue{
				{ID: "hq-wisp-03ph", Assignee: "deacon/dogs/bravo"},
			},
			assignee: "deacon/dogs/alpha",
		},
		{
			name: "skips entries with no ID",
			issues: []*beads.Issue{
				{ID: "", Assignee: "deacon/dogs/alpha"},
				{ID: "hq-wisp-abc", Assignee: "deacon/dogs/alpha"},
			},
			assignee: "deacon/dogs/alpha",
			wantID:   "hq-wisp-abc",
		},
		{
			name:     "nil entries do not panic",
			issues:   []*beads.Issue{nil},
			assignee: "deacon/dogs/alpha",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := firstHookedWork(tc.issues, tc.assignee)
			if tc.wantID == "" {
				if got != nil {
					t.Fatalf("got %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("got nil, want ID %q", tc.wantID)
			}
			if got.ID != tc.wantID {
				t.Errorf("ID = %q, want %q", got.ID, tc.wantID)
			}
		})
	}
}

func TestFirstHookedWork_ParsesAttachedFormula(t *testing.T) {
	issue := &beads.Issue{
		ID:          "hq-wisp-03ph",
		Assignee:    "deacon/dogs/alpha",
		Description: "attached_formula: mol-dog-reaper\nattached_at: 2026-08-26T15:58:25Z",
	}

	got := firstHookedWork([]*beads.Issue{issue}, "deacon/dogs/alpha")
	if got == nil {
		t.Fatal("got nil, want hooked work")
	}
	if got.Formula != "mol-dog-reaper" {
		t.Errorf("Formula = %q, want 'mol-dog-reaper'", got.Formula)
	}
}

func TestBeadsHookedWorkFinder_EmptyInputs(t *testing.T) {
	// A finder with no work dir (or an empty dog name) must be a safe no-op
	// rather than shelling out to bd.
	for _, tc := range []struct{ workDir, dogName string }{
		{"", "alpha"},
		{t.TempDir(), ""},
	} {
		got, err := NewBeadsHookedWorkFinder(tc.workDir).HookedWork(tc.dogName)
		if err != nil {
			t.Errorf("workDir=%q dog=%q: unexpected error: %v", tc.workDir, tc.dogName, err)
		}
		if got != nil {
			t.Errorf("workDir=%q dog=%q: got %+v, want nil", tc.workDir, tc.dogName, got)
		}
	}
}
