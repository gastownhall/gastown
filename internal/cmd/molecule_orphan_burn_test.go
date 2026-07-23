package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

type orphanBurnReader map[string]*beads.Issue

func (r orphanBurnReader) ShowMultiple(_ []string) (map[string]*beads.Issue, error) {
	return r, nil
}

func TestBuildOrphanBurnPlanCanonicalSetAndDigest(t *testing.T) {
	reader := orphanBurnReader{
		"hq-wisp-a": {ID: "hq-wisp-a", Ephemeral: true, Type: "task", Labels: []string{"gt:wisp"}},
		"hq-wisp-b": {ID: "hq-wisp-b", Ephemeral: true, Type: "molecule"},
	}
	one, err := buildOrphanBurnPlan(reader, []string{" HQ-WISP-B ", "hq-wisp-a", "hq-wisp-a"})
	if err != nil {
		t.Fatal(err)
	}
	two, err := buildOrphanBurnPlan(reader, []string{"hq-wisp-a", "hq-wisp-b"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(one.Safe, ","); got != "hq-wisp-a,hq-wisp-b" {
		t.Fatalf("safe set = %q", got)
	}
	if one.SetDigest != two.SetDigest {
		t.Fatalf("canonical digests differ: %s != %s", one.SetDigest, two.SetDigest)
	}
}

func TestBuildOrphanBurnPlanReproduces59vs60Incident(t *testing.T) {
	reader := make(orphanBurnReader)
	requested := make([]string, 0, 60)
	for i := 0; i < 59; i++ {
		id := fmtWispID(i)
		requested = append(requested, id)
		reader[id] = &beads.Issue{ID: id, Ephemeral: true, Type: "task", Labels: []string{"gt:wisp"}}
	}
	requested = append(requested, "hq-wisp-d5y")
	reader["hq-wisp-d5y"] = &beads.Issue{
		ID: "hq-wisp-d5y", Ephemeral: true, Type: "message", Labels: []string{"gt:wisp", "gt:message"},
	}

	plan, err := buildOrphanBurnPlan(reader, requested)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Requested) != 60 || len(plan.Safe) != 59 {
		t.Fatalf("requested=%d safe=%d, want 60 and 59", len(plan.Requested), len(plan.Safe))
	}
	if equalStringSets(plan.Requested, plan.Safe) {
		t.Fatal("59-vs-60 batch must be rejected before mutation")
	}
	if len(plan.Excluded) != 1 || plan.Excluded[0].ID != "hq-wisp-d5y" {
		t.Fatalf("exclusions = %#v", plan.Excluded)
	}
}

func TestOrphanBurnPreservesAuditEventAndProtectedLabels(t *testing.T) {
	tests := []struct {
		name  string
		issue *beads.Issue
	}{
		{"message type", &beads.Issue{ID: "hq-wisp-a", Ephemeral: true, Type: "message"}},
		{"escalation label", &beads.Issue{ID: "hq-wisp-b", Ephemeral: true, Type: "task", Labels: []string{"gt:escalation"}}},
		{"audit type", &beads.Issue{ID: "hq-wisp-c", Ephemeral: true, Type: "audit"}},
		{"event label", &beads.Issue{ID: "hq-wisp-d", Ephemeral: true, Type: "task", Labels: []string{"gt:event"}}},
		{"keep label", &beads.Issue{ID: "hq-wisp-e", Ephemeral: true, Type: "task", Labels: []string{"gt:keep"}}},
		{"durable bead", &beads.Issue{ID: "hq-task", Type: "task"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if reason := orphanBurnPreserveReason(tt.issue); reason == "" {
				t.Fatal("expected preservation reason")
			}
		})
	}
}

func TestOrphanBurnDigestRejectsExtraOrMissingSafeID(t *testing.T) {
	validatedIDs := []string{"hq-wisp-a", "hq-wisp-b"}
	validated := burnSetDigest(validatedIDs)
	for _, ids := range [][]string{{"hq-wisp-a"}, {"hq-wisp-a", "hq-wisp-b", "hq-wisp-c"}} {
		plan := orphanBurnPlan{Requested: ids, Safe: ids, SetDigest: burnSetDigest(ids)}
		if err := validateOrphanBurnExecution(plan, validated); err == nil {
			t.Fatalf("validation unexpectedly accepted %v", ids)
		}
	}
	exact := orphanBurnPlan{Requested: validatedIDs, Safe: validatedIDs, SetDigest: validated}
	if err := validateOrphanBurnExecution(exact, validated); err != nil {
		t.Fatalf("exact reviewed set rejected: %v", err)
	}
	preservedExtra := orphanBurnPlan{
		Requested: []string{"hq-wisp-a", "hq-wisp-b", "hq-wisp-message"},
		Safe:      validatedIDs,
		SetDigest: validated,
	}
	if err := validateOrphanBurnExecution(preservedExtra, validated); err == nil {
		t.Fatal("explicitly supplied preserved ID was accepted")
	}
}

func fmtWispID(i int) string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	return "hq-wisp-test" + string(digits[i/36]) + string(digits[i%36])
}
