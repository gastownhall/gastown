package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
)

type polecatAuthorityBoundaryFixture struct {
	townRoot    string
	townBeads   string
	rigPath     string
	rigBeads    string
	logPath     string
	agentBeadID string
}

func setupPolecatAuthorityBoundaryFixture(t *testing.T, activeIssues ...*beads.Issue) polecatAuthorityBoundaryFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock for bd")
	}

	townRoot := t.TempDir()
	townBeads := filepath.Join(townRoot, ".beads")
	rigPath := filepath.Join(townRoot, "gastown")
	rigBeads := filepath.Join(rigPath, ".beads")
	for _, dir := range []string{
		filepath.Join(townRoot, "mayor"),
		townBeads,
		rigBeads,
		filepath.Join(rigPath, "polecats", "nux"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	if err := beads.WriteRoutes(townBeads, []beads.Route{
		{Prefix: "hq-", Path: "."},
		{Prefix: "gt-", Path: "gastown"},
	}); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	configureScheduler(t, townRoot, 2, 1)
	if err := config.SaveRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"), &config.RigsConfig{
		Version: config.CurrentRigsVersion,
		Rigs: map[string]config.RigEntry{
			"gastown": {GitURL: "https://example.invalid/gastown.git"},
		},
	}); err != nil {
		t.Fatalf("SaveRigsConfig: %v", err)
	}

	agentBeadID := beads.PolecatBeadIDWithPrefix("gt", "gastown", "nux")
	agentIssue := &beads.Issue{
		ID:     agentBeadID,
		Title:  "Polecat nux",
		Status: string(beads.StatusOpen),
		Type:   "agent",
		Labels: []string{"gt:agent"},
		Description: beads.FormatAgentDescription("Polecat nux", &beads.AgentFields{
			AgentState:    string(beads.AgentStateDone),
			CleanupStatus: string(polecat.CleanupClean),
		}),
	}
	townAgentsJSON := marshalIssuesForTest(t, []*beads.Issue{agentIssue})
	rigActiveJSON := marshalIssuesForTest(t, activeIssues)

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	script := `#!/bin/sh
if [ "$1" = "--allow-stale" ] && [ "$2" = "version" ]; then
  exit 1
fi
printf '%s|%s\n' "${BEADS_DIR:-}" "$*" >> "$MOCK_BD_LOG"
case "$1" in
  version)
    echo "bd mock"
    exit 0
    ;;
  list)
    if [ "${MOCK_AUTHORITY_FAIL:-}" = "1" ] && [ "${BEADS_DIR:-}" = "$MOCK_TOWN_BEADS" ]; then
      echo "town authority unavailable" >&2
      exit 7
    fi
    if [ "${BEADS_DIR:-}" = "$MOCK_TOWN_BEADS" ]; then
      printf '%s\n' "$MOCK_TOWN_AGENTS_JSON"
    else
      printf '[]\n'
    fi
    exit 0
    ;;
  query)
    if [ "${BEADS_DIR:-}" = "$MOCK_RIG_BEADS" ]; then
      printf '%s\n' "$MOCK_RIG_ACTIVE_JSON"
    else
      printf '[]\n'
    fi
    exit 0
    ;;
  *)
    printf '[]\n'
    exit 0
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MOCK_BD_LOG", logPath)
	t.Setenv("MOCK_TOWN_BEADS", townBeads)
	t.Setenv("MOCK_RIG_BEADS", rigBeads)
	t.Setenv("MOCK_TOWN_AGENTS_JSON", townAgentsJSON)
	t.Setenv("MOCK_RIG_ACTIVE_JSON", rigActiveJSON)
	beads.ResetBdAllowStaleCacheForTest()

	return polecatAuthorityBoundaryFixture{
		townRoot:    townRoot,
		townBeads:   townBeads,
		rigPath:     rigPath,
		rigBeads:    rigBeads,
		logPath:     logPath,
		agentBeadID: agentBeadID,
	}
}

func marshalIssuesForTest(t *testing.T, issues []*beads.Issue) string {
	t.Helper()
	if issues == nil {
		issues = []*beads.Issue{}
	}
	data, err := json.Marshal(issues)
	if err != nil {
		t.Fatalf("marshal issues: %v", err)
	}
	return string(data)
}

func readAuthorityBoundaryLog(t *testing.T, fixture polecatAuthorityBoundaryFixture) string {
	t.Helper()
	data, err := os.ReadFile(fixture.logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	return string(data)
}

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

func TestCollectPolecatListItemsUsesAgentAuthorityAndRigActiveWork(t *testing.T) {
	setupPolecatTestRegistry(t)
	r := &rig.Rig{Name: "gastown", Path: t.TempDir()}
	agentID := beads.PolecatBeadIDWithPrefix("gt", "gastown", "nux")
	agentIssue := &beads.Issue{
		ID: agentID,
		Description: beads.FormatAgentDescription("Polecat nux", &beads.AgentFields{
			AgentState:    string(beads.AgentStateDone),
			CleanupStatus: string(polecat.CleanupClean),
		}),
	}

	items := collectPolecatListItemsFromAuthority(
		r,
		[]string{"nux"},
		map[string]*beads.Issue{agentID: agentIssue},
		map[string]*beads.Issue{},
		nil,
		nil,
	)
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	if items[0].State != polecat.StateDone || items[0].CleanupStatus != string(polecat.CleanupClean) || !items[0].Reusable || items[0].NeedsRecovery {
		t.Fatalf("item from town agent authority = %+v, want done clean reusable", items[0])
	}

	activeWork := &beads.Issue{ID: "gt-hook", Status: string(beads.IssueStatusHooked), Assignee: "gastown/polecats/nux"}
	items = collectPolecatListItemsFromAuthority(
		r,
		[]string{"nux"},
		map[string]*beads.Issue{agentID: agentIssue},
		map[string]*beads.Issue{"nux": activeWork},
		nil,
		nil,
	)
	if items[0].Issue != "gt-hook" || !items[0].NeedsRecovery || !items[0].CountsTowardCapacity || items[0].Reusable {
		t.Fatalf("item with rig active work = %+v, want active work to remain authoritative", items[0])
	}
}

func TestPolecatInventoryReadsTownAgentAuthorityWithoutRigAgentBead(t *testing.T) {
	setupPolecatTestRegistry(t)
	fixture := setupPolecatAuthorityBoundaryFixture(t)
	bd := beads.New(fixture.rigPath)

	agents, err := listPolecatAgentBeads(bd)
	if err != nil {
		t.Fatalf("listPolecatAgentBeads: %v", err)
	}
	activeWork, err := listActivePolecatWorkByName(bd, "gastown")
	if err != nil {
		t.Fatalf("listActivePolecatWorkByName: %v", err)
	}

	items := collectPolecatListItemsFromAuthority(
		&rig.Rig{Name: "gastown", Path: fixture.rigPath},
		[]string{"nux"},
		agents,
		activeWork,
		nil,
		nil,
	)
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	if items[0].State != polecat.StateDone || items[0].CleanupStatus != string(polecat.CleanupClean) || !items[0].Reusable || items[0].NeedsRecovery {
		t.Fatalf("item = %+v, want town authority to make done clean polecat reusable", items[0])
	}

	logOutput := readAuthorityBoundaryLog(t, fixture)
	if !strings.Contains(logOutput, fixture.townBeads+"|list") {
		t.Fatalf("agent bead list did not use town authority:\n%s", logOutput)
	}
	if strings.Contains(logOutput, fixture.rigBeads+"|list") {
		t.Fatalf("agent bead list used rig-local beads dir:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, fixture.rigBeads+"|query") {
		t.Fatalf("active work query did not use rig store:\n%s", logOutput)
	}
}

func TestPolecatInventoryActiveWorkRemainsRigRouted(t *testing.T) {
	setupPolecatTestRegistry(t)
	activeIssue := &beads.Issue{ID: "gt-hook", Status: string(beads.IssueStatusHooked), Assignee: "gastown/polecats/nux"}
	fixture := setupPolecatAuthorityBoundaryFixture(t, activeIssue)
	bd := beads.New(fixture.rigPath)

	agents, err := listPolecatAgentBeads(bd)
	if err != nil {
		t.Fatalf("listPolecatAgentBeads: %v", err)
	}
	activeWork, err := listActivePolecatWorkByName(bd, "gastown")
	if err != nil {
		t.Fatalf("listActivePolecatWorkByName: %v", err)
	}

	items := collectPolecatListItemsFromAuthority(
		&rig.Rig{Name: "gastown", Path: fixture.rigPath},
		[]string{"nux"},
		agents,
		activeWork,
		nil,
		nil,
	)
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	if items[0].Issue != "gt-hook" || !items[0].NeedsRecovery || !items[0].CountsTowardCapacity || items[0].Reusable {
		t.Fatalf("item = %+v, want rig active work to override reusable town metadata", items[0])
	}

	logOutput := readAuthorityBoundaryLog(t, fixture)
	if !strings.Contains(logOutput, fixture.townBeads+"|list") || !strings.Contains(logOutput, fixture.rigBeads+"|query") {
		t.Fatalf("expected town agent list and rig active query:\n%s", logOutput)
	}
}

func TestCollectPolecatListItemsMissingAgentAuthorityFailsClosed(t *testing.T) {
	setupPolecatTestRegistry(t)
	r := &rig.Rig{Name: "gastown", Path: t.TempDir()}

	items := collectPolecatListItemsFromAuthority(
		r,
		[]string{"nux"},
		map[string]*beads.Issue{},
		map[string]*beads.Issue{},
		nil,
		nil,
	)
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	if !items[0].NeedsRecovery || items[0].Reason != "cleanup-unknown" || !items[0].CountsTowardCapacity {
		t.Fatalf("missing agent authority item = %+v, want cleanup-unknown recovery", items[0])
	}
}

func TestPolecatInventoryAuthorityLookupFailureFailsClosed(t *testing.T) {
	setupPolecatTestRegistry(t)
	fixture := setupPolecatAuthorityBoundaryFixture(t)
	t.Setenv("MOCK_AUTHORITY_FAIL", "1")
	bd := beads.New(fixture.rigPath)

	agents, agentErr := listPolecatAgentBeads(bd)
	if agentErr == nil {
		t.Fatal("listPolecatAgentBeads error = nil, want authority lookup failure")
	}
	items := collectPolecatListItemsFromAuthority(
		&rig.Rig{Name: "gastown", Path: fixture.rigPath},
		[]string{"nux"},
		agents,
		map[string]*beads.Issue{},
		nil,
		nil,
	)
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	if !items[0].NeedsRecovery || items[0].Reason != "cleanup-unknown" || !items[0].CountsTowardCapacity {
		t.Fatalf("item after authority failure = %+v, want cleanup-unknown recovery", items[0])
	}
}
