package cmd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestAssigneeAliases verifies that town-level agent addresses resolve to both
// spellings that appear in the assignee column ("deacon" from the patrol
// constructors, "deacon/" from sling), while every other role aliases to
// itself so discovery costs no extra queries (hq-12a).
func TestAssigneeAliases(t *testing.T) {
	tests := []struct {
		name     string
		assignee string
		want     []string
	}{
		{"deacon bare", "deacon", []string{"deacon", "deacon/"}},
		{"deacon slashed", "deacon/", []string{"deacon/", "deacon"}},
		{"mayor bare", "mayor", []string{"mayor", "mayor/"}},
		{"mayor slashed", "mayor/", []string{"mayor/", "mayor"}},
		{"witness", "gastown/witness", []string{"gastown/witness"}},
		{"refinery", "gastown/refinery", []string{"gastown/refinery"}},
		{"dog", "deacon/dogs/doctor", []string{"deacon/dogs/doctor"}},
		{"boot", "deacon/boot", []string{"deacon/boot"}},
		{"polecat", "gastown/polecats/morsov", []string{"gastown/polecats/morsov"}},
		{"empty", "", []string{""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := assigneeAliases(tt.assignee)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("assigneeAliases(%q) = %v, want %v", tt.assignee, got, tt.want)
			}
		})
	}
}

// TestFindActivePatrolTrailingSlashAssignee is the regression test for hq-12a:
// a patrol wisp created by `gt sling mol-deacon-patrol deacon` is pinned to
// "deacon/", but the patrol config uses "deacon". Before the fix, discovery
// missed the wisp entirely and `gt patrol report` failed with
// "no active patrol found for deacon", silently dropping the cycle's summary
// and step audit from the ledger.
func TestFindActivePatrolTrailingSlashAssignee(t *testing.T) {
	requireBd(t)
	tmpDir, b := setupPatrolTestDB(t)

	molName := "mol-deacon-patrol"

	// Sling-created wisps carry the canonical trailing-slash address.
	rootID := createHookedPatrol(t, b, molName, "deacon/", true /* withOpenChild */)

	// The patrol config pins the bare name, as `gt patrol new` does.
	cfg := PatrolConfig{
		RoleName:      "deacon",
		PatrolMolName: molName,
		BeadsDir:      tmpDir,
		Assignee:      "deacon",
		Beads:         b,
	}

	patrolID, _, found, findErr := findActivePatrol(cfg)
	if findErr != nil {
		t.Fatalf("findActivePatrol error: %v", findErr)
	}
	if !found {
		t.Fatal("expected sling-created patrol (assignee \"deacon/\") to be found by config assignee \"deacon\"")
	}
	if patrolID != rootID {
		t.Errorf("patrolID = %q, want %q", patrolID, rootID)
	}
}

// TestRenderPatrolWispDescriptionCarriesFormulaMarker covers the third facet of
// hq-12a: patrol wisps minted by `gt patrol new` / `gt patrol report` carried
// only the rendered formula prose, so `gt hook` reported "No molecule attached"
// and `bd mol current` could not resolve steps. The marker must parse back out
// of the description as an attachment field, ahead of the prose.
func TestRenderPatrolWispDescriptionCarriesFormulaMarker(t *testing.T) {
	desc, err := renderPatrolWispDescription(PatrolConfig{
		RoleName:      "deacon",
		PatrolMolName: "mol-deacon-patrol",
		BeadsDir:      t.TempDir(),
		Assignee:      "deacon",
		ExtraVars:     []string{"idle_effort_threshold=7"},
	})
	if err != nil {
		t.Fatalf("renderPatrolWispDescription: %v", err)
	}

	fields := beads.ParseAttachmentFields(&beads.Issue{Description: desc})
	if fields == nil {
		t.Fatalf("no attachment fields parsed from description:\n%s", desc)
	}
	if fields.AttachedFormula != "mol-deacon-patrol" {
		t.Errorf("AttachedFormula = %q, want %q", fields.AttachedFormula, "mol-deacon-patrol")
	}
	if !reflect.DeepEqual(fields.AttachedVars, []string{"idle_effort_threshold=7"}) {
		t.Errorf("AttachedVars = %v, want [idle_effort_threshold=7]", fields.AttachedVars)
	}

	// The prose must survive alongside the marker — agents read the steps from it.
	if !strings.Contains(desc, "**Formula Checklist**") {
		t.Errorf("description lost the formula checklist:\n%s", desc)
	}
	if !strings.HasPrefix(desc, "attached_formula: mol-deacon-patrol") {
		t.Errorf("marker is not the first line of the description:\n%s", desc)
	}
}
