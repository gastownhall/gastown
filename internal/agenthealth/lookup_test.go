package agenthealth

import (
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

type stubLister struct {
	issues []*beads.Issue // durable table
	wisps  []*beads.Issue // ephemeral table
	err    error
	calls  []beads.ListOptions
}

func (s *stubLister) List(opts beads.ListOptions) ([]*beads.Issue, error) {
	s.calls = append(s.calls, opts)
	if s.err != nil {
		return nil, s.err
	}
	if opts.Ephemeral {
		return s.wisps, nil
	}
	return s.issues, nil
}

// TestLookupHookedWork_MergesWispTable is the reason this helper exists: a
// witness's patrol wisp lives in the ephemeral table, so an issues-only query
// sees an empty hook and reports a stalled witness as having nothing to stall on.
func TestLookupHookedWork_MergesWispTable(t *testing.T) {
	lister := &stubLister{
		issues: []*beads.Issue{{ID: "gt-1", UpdatedAt: "2026-09-03T08:54:21Z", CreatedAt: "2026-09-03T08:54:21Z"}},
		wisps:  []*beads.Issue{{ID: "gt-wisp-g3dxmv", UpdatedAt: "2026-09-03T08:54:21Z", CreatedAt: "2026-09-03T08:54:21Z"}},
	}

	got, err := LookupHookedWork(lister, "gastown/witness")
	if err != nil {
		t.Fatalf("LookupHookedWork: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d work items, want 2 (issues + wisps)", len(got))
	}

	want := time.Date(2026, 9, 3, 8, 54, 21, 0, time.UTC)
	if !got[0].UpdatedAt.Equal(want) {
		t.Errorf("UpdatedAt = %v, want %v", got[0].UpdatedAt, want)
	}

	if len(lister.calls) != 2 {
		t.Fatalf("got %d List calls, want 2", len(lister.calls))
	}
	for _, call := range lister.calls {
		if call.Status != beads.StatusHooked {
			t.Errorf("Status = %q, want %q", call.Status, beads.StatusHooked)
		}
		if call.Assignee != "gastown/witness" {
			t.Errorf("Assignee = %q, want gastown/witness", call.Assignee)
		}
		if call.Priority != -1 {
			t.Errorf("Priority = %d, want -1 (no priority filter)", call.Priority)
		}
	}
}

func TestLookupHookedWork_DeduplicatesAcrossTables(t *testing.T) {
	dup := &beads.Issue{ID: "gt-wisp-1", UpdatedAt: "2026-09-03T08:54:21Z"}
	got, err := LookupHookedWork(&stubLister{issues: []*beads.Issue{dup}, wisps: []*beads.Issue{dup}}, "gastown/witness")
	if err != nil {
		t.Fatalf("LookupHookedWork: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d work items, want 1", len(got))
	}
}

// TestLookupHookedWork_PropagatesError keeps the undecidable case undecidable:
// a query failure must reach Assess as WorkErr rather than as an empty hook,
// which would read as healthy.
func TestLookupHookedWork_PropagatesError(t *testing.T) {
	_, err := LookupHookedWork(&stubLister{err: errors.New("dolt unreachable")}, "gastown/witness")
	if err == nil {
		t.Fatal("LookupHookedWork returned nil error; a failed query must not look like an empty hook")
	}

	got := Assess(Input{SessionAlive: true, WorkErr: err, Threshold: 30 * time.Minute})
	if got.State != StateUnknown {
		t.Errorf("State = %q, want %q", got.State, StateUnknown)
	}
}

func TestParseBeadTime(t *testing.T) {
	cases := []struct {
		in   string
		zero bool
	}{
		{"2026-09-03T08:54:21Z", false},
		{"2026-09-03T08:54:21.123456Z", false},
		{"2026-09-03T08:54:21", false},
		{"", true},
		{"not a time", true},
	}
	for _, tc := range cases {
		got := parseBeadTime(tc.in)
		if got.IsZero() != tc.zero {
			t.Errorf("parseBeadTime(%q).IsZero() = %v, want %v", tc.in, got.IsZero(), tc.zero)
		}
	}
}
