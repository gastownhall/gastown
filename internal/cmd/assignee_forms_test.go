package cmd

import (
	"reflect"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestBeadAssignee(t *testing.T) {
	cases := map[string]string{
		"deacon/":            "deacon",
		"mayor/":             "mayor",
		"deacon":             "deacon",
		"source/witness":     "source/witness",
		"deacon/boot":        "deacon/boot",
		"deacon/dogs/alpha":  "deacon/dogs/alpha",
		"source/polecats/nx": "source/polecats/nx",
	}
	for in, want := range cases {
		if got := beadAssignee(in); got != want {
			t.Errorf("beadAssignee(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAssigneeQueryForms(t *testing.T) {
	cases := map[string][]string{
		// Town-level agents historically have rows in both forms: patrol and
		// BD_ACTOR use the bare name, sling/hook wrote the mail-address form
		// with a trailing slash (hq-p9l).
		"deacon":  {"deacon", "deacon/"},
		"deacon/": {"deacon", "deacon/"},
		"mayor":   {"mayor", "mayor/"},
		"mayor/":  {"mayor", "mayor/"},
		// Everyone else has a single canonical form.
		"source/witness":    {"source/witness"},
		"deacon/boot":       {"deacon/boot"},
		"deacon/dogs/alpha": {"deacon/dogs/alpha"},
	}
	for in, want := range cases {
		if got := assigneeQueryForms(in); !reflect.DeepEqual(got, want) {
			t.Errorf("assigneeQueryForms(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestListAssignedActiveWork_TownLevelFormSkew reproduces the live hq-p9l
// symptom: a patrol wisp hooked with the bare assignee ("deacon", written by
// gt patrol new and matching BD_ACTOR) was invisible to gt hook / gt mol
// status, which query the mail-address form ("deacon/").
func TestListAssignedActiveWork_TownLevelFormSkew(t *testing.T) {
	requireBd(t)
	_, b := setupPatrolTestDB(t)

	rootID := createHookedPatrol(t, b, "mol-test-patrol", "deacon", true)

	found, err := listAssignedActiveWork(b, "deacon/")
	if err != nil {
		t.Fatalf("listAssignedActiveWork: %v", err)
	}
	if len(found) == 0 {
		t.Fatalf("hooked bead %s with assignee %q not found when querying %q", rootID, "deacon", "deacon/")
	}
	if found[0].ID != rootID {
		t.Errorf("found %s, want %s", found[0].ID, rootID)
	}
}

// TestFindActivePatrol_LegacySlashAssignee covers the reverse skew: wisps
// hooked by older sling builds carry the trailing-slash assignee ("deacon/"),
// and gt patrol report (which queries the bare form) reported "no active
// patrol found" and spawned duplicates.
func TestFindActivePatrol_LegacySlashAssignee(t *testing.T) {
	requireBd(t)
	tmpDir, b := setupPatrolTestDB(t)

	molName := "mol-test-patrol"
	rootID := createHookedPatrol(t, b, molName, "deacon/", true)

	cfg := PatrolConfig{
		PatrolMolName: molName,
		BeadsDir:      tmpDir,
		Assignee:      "deacon",
		Beads:         b,
	}

	patrolID, _, found, err := findActivePatrol(cfg)
	if err != nil {
		t.Fatalf("findActivePatrol: %v", err)
	}
	if !found {
		t.Fatalf("active patrol %s with legacy assignee %q not found for %q", rootID, "deacon/", "deacon")
	}
	if patrolID != rootID {
		t.Errorf("patrolID = %q, want %q", patrolID, rootID)
	}

	// The patrol must remain hooked — tolerant discovery must not treat the
	// legacy-form row as stale and close it.
	issue, err := b.Show(rootID)
	if err != nil {
		t.Fatalf("show patrol: %v", err)
	}
	if issue.Status != beads.StatusHooked {
		t.Errorf("patrol status = %q, want %q", issue.Status, beads.StatusHooked)
	}
}
