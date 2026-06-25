package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestActiveWorkMergeBeadListsDedupeAndSort(t *testing.T) {
	primary := []*beads.Issue{
		{ID: "gt-older", UpdatedAt: "2026-01-01T00:00:00Z"},
		{ID: "gt-same", UpdatedAt: "2026-01-02T00:00:00Z", Title: "durable"},
	}
	secondary := []*beads.Issue{
		{ID: "gt-newer", UpdatedAt: "2026-01-03T00:00:00Z"},
		{ID: "gt-same", UpdatedAt: "2026-01-04T00:00:00Z", Title: "wisp"},
	}

	got := mergeBeadLists(primary, secondary)
	if len(got) != 3 {
		t.Fatalf("mergeBeadLists length = %d, want 3", len(got))
	}

	wantIDs := []string{"gt-newer", "gt-same", "gt-older"}
	for i, want := range wantIDs {
		if got[i].ID != want {
			t.Fatalf("mergeBeadLists[%d].ID = %q, want %q (all=%v)", i, got[i].ID, want, got)
		}
	}
	if got[1].Title != "durable" {
		t.Fatalf("duplicate should keep primary issue, got title %q", got[1].Title)
	}
}
