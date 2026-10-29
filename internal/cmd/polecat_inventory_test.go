package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
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

func TestBuildPolecatInventoryItem(t *testing.T) {
	setupPolecatTestRegistry(t)
	sessions := newPolecatSessionSet([]string{"gt-running"})
	tests := []struct {
		name         string
		polecatName  string
		fields       *beads.AgentFields
		activeWork   *beads.Issue
		wantState    polecat.State
		wantIssue    string
		wantVerdict  string
		wantReusable bool
		wantRecovery bool
		wantCapacity bool
	}{
		{
			name:         "clean idle reusable",
			polecatName:  "idle",
			fields:       &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean)},
			wantState:    polecat.StateIdle,
			wantVerdict:  polecat.WorkstateVerdictSafeToNuke,
			wantReusable: true,
		},
		{
			name:         "hooked running is working capacity",
			polecatName:  "running",
			fields:       &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean)},
			activeWork:   &beads.Issue{ID: "gt-hook", Status: string(beads.IssueStatusHooked), Assignee: "gastown/polecats/running"},
			wantState:    polecat.StateWorking,
			wantIssue:    "gt-hook",
			wantVerdict:  polecat.WorkstateVerdictWorking,
			wantCapacity: true,
		},
		{
			name:         "open stopped is stalled capacity",
			polecatName:  "stopped",
			fields:       &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean)},
			activeWork:   &beads.Issue{ID: "gt-open", Status: string(beads.StatusOpen), Assignee: "gastown/polecats/stopped"},
			wantState:    polecat.StateStalled,
			wantIssue:    "gt-open",
			wantVerdict:  polecat.WorkstateVerdictNeedsRecovery,
			wantRecovery: true,
			wantCapacity: true,
		},
		{
			name:         "deferred protects without capacity",
			polecatName:  "deferred",
			fields:       &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean)},
			activeWork:   &beads.Issue{ID: "gt-deferred", Status: string(beads.StatusDeferred), Assignee: "gastown/polecats/deferred"},
			wantState:    polecat.StateIdle,
			wantIssue:    "gt-deferred",
			wantVerdict:  polecat.WorkstateVerdictNeedsRecovery,
			wantRecovery: true,
		},
		{
			name:         "hook fallback protects without capacity",
			polecatName:  "hookonly",
			fields:       &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean), HookBead: "gt-old"},
			wantState:    polecat.StateIdle,
			wantVerdict:  polecat.WorkstateVerdictNeedsRecovery,
			wantRecovery: true,
		},
		{
			name:         "paused agent state protects without capacity",
			polecatName:  "paused",
			fields:       &beads.AgentFields{AgentState: string(beads.AgentStatePaused), CleanupStatus: string(polecat.CleanupClean)},
			wantState:    polecat.StateIdle,
			wantVerdict:  polecat.WorkstateVerdictNeedsRecovery,
			wantRecovery: true,
		},
		{
			name:        "active mr is pending non capacity",
			polecatName: "pendingmr",
			fields:      &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean), ActiveMR: "gt-mr"},
			wantState:   polecat.StateIdle,
			wantVerdict: polecat.WorkstateVerdictPendingMR,
		},
		{
			name:         "done without active mr and clean cleanup is reusable",
			polecatName:  "done",
			fields:       &beads.AgentFields{AgentState: string(beads.AgentStateDone), CleanupStatus: string(polecat.CleanupClean)},
			wantState:    polecat.StateDone,
			wantVerdict:  polecat.WorkstateVerdictSafeToNuke,
			wantReusable: true,
		},
		{
			name:         "done without active mr blocks reuse when cleanup is dirty",
			polecatName:  "donedirty",
			fields:       &beads.AgentFields{AgentState: string(beads.AgentStateDone), CleanupStatus: string(polecat.CleanupUnpushed)},
			wantState:    polecat.StateDone,
			wantVerdict:  polecat.WorkstateVerdictNeedsRecovery,
			wantRecovery: true,
			wantCapacity: true,
		},
		{
			name:        "done with active mr remains pending",
			polecatName: "donepending",
			fields:      &beads.AgentFields{AgentState: string(beads.AgentStateDone), CleanupStatus: string(polecat.CleanupClean), ActiveMR: "gt-mr"},
			wantState:   polecat.StateDone,
			wantVerdict: polecat.WorkstateVerdictPendingMR,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := buildPolecatInventoryItem("gastown", tt.polecatName, tt.fields, tt.activeWork, sessions)
			if item.State != tt.wantState || item.Issue != tt.wantIssue || item.Disposition.Verdict != tt.wantVerdict || item.Disposition.Reusable != tt.wantReusable || item.Disposition.NeedsRecovery != tt.wantRecovery || item.Disposition.CountsTowardCapacity != tt.wantCapacity {
				t.Fatalf("item = %+v disposition=%+v", item, item.Disposition)
			}
		})
	}
}

func TestBuildPolecatInventoryItemActiveWorkLookupErrorFailsClosed(t *testing.T) {
	item := buildPolecatInventoryItemFromEvidence(
		"gastown",
		"lookup",
		&beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean)},
		polecatActiveWorkLookupError(errors.New("bd failed")),
		polecatSessionSet{},
	)

	if item.Disposition.Reusable || item.Disposition.SafeToNuke || !item.Disposition.NeedsRecovery || item.Disposition.CountsTowardCapacity {
		t.Fatalf("lookup error disposition = %+v", item.Disposition)
	}
	if item.Disposition.Reason != "active-work" {
		t.Fatalf("reason = %q, want active-work", item.Disposition.Reason)
	}
	if len(item.Disposition.Blockers) != 1 || !strings.Contains(item.Disposition.Blockers[0], "lookup_error") {
		t.Fatalf("blockers = %v, want lookup_error", item.Disposition.Blockers)
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

// stubPolecatBlockedProbe replaces the tmux probe for the duration of a test.
func stubPolecatBlockedProbe(t *testing.T, reason string) {
	t.Helper()
	prev := probePolecatBlocked
	probePolecatBlocked = func(string) string { return reason }
	t.Cleanup(func() { probePolecatBlocked = prev })
}

// TestBuildPolecatInventoryItemReportsBlockedNotWorking is the regression test
// for hq-3l8r. Three rho polecats held hooked work with live tmux sessions and a
// dead Codex token; `gt polecat list` called all three "working" for six hours
// while 972 commits piled up unpushed across 32 worktrees.
//
// The registry derived "working" from `tmux list-sessions` name presence alone
// (polecat_inventory.go), so the two rows below differ in exactly one input: the
// evidence-of-progress probe. Everything else — the hooked bead, the assignee,
// the agent fields, the live session — is identical, which is what makes this a
// proof rather than a demonstration. Revert the probe call and "blocked" becomes
// "working" here, exactly as it did in production.
func TestBuildPolecatInventoryItemReportsBlockedNotWorking(t *testing.T) {
	setupPolecatTestRegistry(t)
	sessions := newPolecatSessionSet([]string{"gt-obsidian"})
	fields := &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean)}
	work := &beads.Issue{ID: "gt-4di", Status: string(beads.IssueStatusHooked), Assignee: "gastown/polecats/obsidian"}

	// Control: session live, probe sees progress → working (unchanged behaviour).
	stubPolecatBlockedProbe(t, "")
	working := buildPolecatInventoryItem("gastown", "obsidian", fields, work, sessions)
	if working.State != polecat.StateWorking {
		t.Fatalf("progressing polecat state = %q, want %q", working.State, polecat.StateWorking)
	}
	if working.Disposition.Verdict != polecat.WorkstateVerdictWorking {
		t.Fatalf("progressing polecat verdict = %q, want %q", working.Disposition.Verdict, polecat.WorkstateVerdictWorking)
	}

	// Same live session, same hooked bead, no evidence of progress → blocked.
	stubPolecatBlockedProbe(t, "provider error: access token could not be refreshed")
	blocked := buildPolecatInventoryItem("gastown", "obsidian", fields, work, sessions)
	if blocked.State == polecat.StateWorking {
		t.Fatal("polecat with a live session but no evidence of progress reported as working; a session that exists is not a session that works (hq-3l8r)")
	}
	if blocked.State != polecat.StateBlocked {
		t.Fatalf("blocked polecat state = %q, want %q", blocked.State, polecat.StateBlocked)
	}
	if blocked.Disposition.Verdict == polecat.WorkstateVerdictWorking {
		t.Fatalf("blocked polecat verdict = %q, want anything but %q", blocked.Disposition.Verdict, polecat.WorkstateVerdictWorking)
	}
	// The operator must be able to read the cause off the row, not just the state.
	if !strings.Contains(strings.Join(blocked.Disposition.Blockers, " "), "access token could not be refreshed") {
		t.Fatalf("blockers = %v, want the probe's reason surfaced", blocked.Disposition.Blockers)
	}
	// The slot stays occupied: blocked work is not free capacity.
	if !blocked.Disposition.CountsTowardCapacity {
		t.Fatal("blocked polecat freed its capacity slot; the work is still on its hook")
	}

	// And the list layer must not promote it back to working.
	if got := effectivePolecatState(PolecatListItem{
		State:                blocked.State,
		Issue:                blocked.Issue,
		SessionRunning:       blocked.SessionRunning,
		CountsTowardCapacity: blocked.Disposition.CountsTowardCapacity,
	}); got != polecat.StateBlocked {
		t.Fatalf("effectivePolecatState(blocked) = %q, want %q", got, polecat.StateBlocked)
	}
}

// TestBuildPolecatInventoryItemDoesNotProbeDeadSessions keeps "no session" as
// stalled rather than blocked: the two have different remedies (respawn vs.
// unblock the agent) and collapsing them loses that.
func TestBuildPolecatInventoryItemDoesNotProbeDeadSessions(t *testing.T) {
	setupPolecatTestRegistry(t)
	probed := false
	prev := probePolecatBlocked
	probePolecatBlocked = func(string) string { probed = true; return "should not be consulted" }
	t.Cleanup(func() { probePolecatBlocked = prev })

	item := buildPolecatInventoryItem("gastown", "gone",
		&beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean)},
		&beads.Issue{ID: "gt-4di", Status: string(beads.IssueStatusHooked), Assignee: "gastown/polecats/gone"},
		newPolecatSessionSet(nil))

	if probed {
		t.Error("probed tmux for a polecat with no session; wasted work")
	}
	if item.State != polecat.StateStalled {
		t.Fatalf("sessionless polecat state = %q, want %q", item.State, polecat.StateStalled)
	}
}
