package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestActiveWorkMergeBeadListsDedupeAndSort(t *testing.T) {
	primary := []*beads.Issue{
		{ID: "gt-older", UpdatedAt: "2026-01-01T00:00:00Z"},
		{ID: "gt-same", UpdatedAt: "2026-01-02T00:00:00Z", Title: "durable"},
		{ID: "gt-whole", UpdatedAt: "2026-01-03T00:00:00Z"},
	}
	secondary := []*beads.Issue{
		{ID: "gt-fractional", UpdatedAt: "2026-01-03T00:00:00.1Z"},
		{ID: "gt-same", UpdatedAt: "2026-01-04T00:00:00Z", Title: "wisp"},
	}

	got := mergeBeadLists(primary, secondary)
	if len(got) != 4 {
		t.Fatalf("mergeBeadLists length = %d, want 4", len(got))
	}

	wantIDs := []string{"gt-fractional", "gt-whole", "gt-same", "gt-older"}
	for i, want := range wantIDs {
		if got[i].ID != want {
			t.Fatalf("mergeBeadLists[%d].ID = %q, want %q (all=%v)", i, got[i].ID, want, got)
		}
	}
	if got[2].Title != "durable" {
		t.Fatalf("duplicate should keep primary issue, got title %q", got[2].Title)
	}
}
