package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/polecat"
)

func TestPolecatSessionSet(t *testing.T) {
	setupPolecatTestRegistry(t)
	sessions := newPolecatSessionSet([]string{
		"gt-thunder",
		"gt-crew-dom",
		"gp-mirelurk",
		"not-a-polecat",
	})

	if got, ok := sessions.lookup("gastown", "thunder"); !ok || got != "gt-thunder" {
		t.Fatalf("lookup gastown/thunder = %q, %v", got, ok)
	}
	if _, ok := sessions.lookup("gastown", "dom"); ok {
		t.Fatal("crew session should not be indexed as polecat")
	}
	if got := sessions.namesForRig("gastown"); len(got) != 1 || got[0] != "gt-thunder" {
		t.Fatalf("namesForRig(gastown) = %v", got)
	}
}

func TestBuildPolecatInventoryItemMapsKnownState(t *testing.T) {
	setupPolecatTestRegistry(t)
	sessions := newPolecatSessionSet([]string{"gt-running"})
	item := buildPolecatInventoryItem("gastown", "running", "", nil, &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean)}, &beads.Issue{ID: "gt-hook", Status: string(beads.IssueStatusHooked)}, sessions)
	if item.State != polecat.StateWorking || item.Issue != "gt-hook" || !item.SessionRunning {
		t.Fatalf("item = %+v", item)
	}
}

func TestFailClosedAssignedWorkLookup(t *testing.T) {
	got := failClosedAssignedWorkLookup(polecat.WorkstateDisposition{Reusable: true, SafeToNuke: true, Verdict: polecat.WorkstateVerdictSafeToNuke}, errors.New("bd failed"))
	if got.Reusable || got.SafeToNuke || !got.NeedsRecovery || got.Reason != "active-work" {
		t.Fatalf("lookup error disposition = %+v", got)
	}
	if len(got.Blockers) != 1 || !strings.Contains(got.Blockers[0], "lookup_error") {
		t.Fatalf("blockers = %v, want lookup_error", got.Blockers)
	}
}

func TestPolecatSummaryIssueRankPrefersActiveWork(t *testing.T) {
	ordered := []*beads.Issue{
		{ID: "hook", Status: string(beads.IssueStatusHooked)},
		{ID: "progress", Status: string(beads.StatusInProgress)},
		{ID: "open", Status: string(beads.StatusOpen)},
		{ID: "blocked", Status: string(beads.StatusBlocked)},
		{ID: "deferred", Status: string(beads.StatusDeferred)},
	}
	for i := 1; i < len(ordered); i++ {
		if polecatSummaryIssueRank(ordered[i-1]) >= polecatSummaryIssueRank(ordered[i]) {
			t.Fatalf("rank(%s) should be before rank(%s)", ordered[i-1].Status, ordered[i].Status)
		}
	}
}

func TestPolecatNameFromAssignee(t *testing.T) {
	tests := []struct {
		assignee string
		wantName string
		wantOK   bool
	}{
		{assignee: "gastown/polecats/thunder", wantName: "thunder", wantOK: true},
		{assignee: "other/polecats/thunder"},
		{assignee: "gastown/crew/dom"},
		{assignee: "gastown/polecats/"},
		{assignee: "gastown/polecats/a/b"},
	}
	for _, tt := range tests {
		got, ok := polecatNameFromAssignee("gastown", tt.assignee)
		if got != tt.wantName || ok != tt.wantOK {
			t.Fatalf("polecatNameFromAssignee(%q) = %q, %v", tt.assignee, got, ok)
		}
	}
}
