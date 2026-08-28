package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/agentaddr"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/session"
)

// The wisp leak in gt-cw1 was a write/write disagreement: `gt sling deacon`
// stored the assignee as "deacon/" while `gt patrol` stored and matched a bare
// "deacon", so patrol report could not see the wisp on its own hook. Both sides
// now derive the address from agentaddr, and this test fails if they drift.
func TestPatrolAssigneeMatchesSlingAssignee(t *testing.T) {
	cases := []struct {
		name          string
		identity      *session.AgentIdentity
		patrolConfig  PatrolConfig
		wantAssignee  string
		wantPatrolRig string
	}{
		{
			name:          "deacon",
			identity:      &session.AgentIdentity{Role: session.RoleDeacon},
			patrolConfig:  PatrolConfig{RoleName: "deacon", Assignee: "deacon"},
			wantAssignee:  "deacon/",
			wantPatrolRig: "",
		},
		{
			name:          "witness",
			identity:      &session.AgentIdentity{Role: session.RoleWitness, Rig: "gastown"},
			patrolConfig:  PatrolConfig{RoleName: "witness", Assignee: "gastown/witness"},
			wantAssignee:  "gastown/witness",
			wantPatrolRig: "gastown",
		},
		{
			name:          "refinery",
			identity:      &session.AgentIdentity{Role: session.RoleRefinery, Rig: "sandbox"},
			patrolConfig:  PatrolConfig{RoleName: "refinery", Assignee: "sandbox/refinery"},
			wantAssignee:  "sandbox/refinery",
			wantPatrolRig: "sandbox",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			slingAssignee := canonicalAssigneeAddress(c.identity)
			if slingAssignee != c.wantAssignee {
				t.Errorf("sling assignee = %q, want %q", slingAssignee, c.wantAssignee)
			}
			patrolAssignee := c.patrolConfig.assigneeAddress()
			if patrolAssignee != c.wantAssignee {
				t.Errorf("patrol assignee = %q, want %q", patrolAssignee, c.wantAssignee)
			}
			if patrolAssignee != slingAssignee {
				t.Errorf("patrol assignee %q != sling assignee %q", patrolAssignee, slingAssignee)
			}
			if got := patrolRigName(c.patrolConfig); got != c.wantPatrolRig {
				t.Errorf("patrolRigName = %q, want %q", got, c.wantPatrolRig)
			}
		})
	}
}

// A patrol reader must still find rows written by an older build, which stored
// the town-level assignee without its trailing slash.
func TestPatrolAssigneeVariantsCoverLegacyRows(t *testing.T) {
	variants := agentaddr.Variants(PatrolConfig{Assignee: "deacon"}.assigneeAddress())

	wantAll := []string{"deacon/", "deacon"}
	for _, want := range wantAll {
		found := false
		for _, got := range variants {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("variants %v missing %q", variants, want)
		}
	}
	if variants[0] != "deacon/" {
		t.Errorf("variants[0] = %q, want the canonical form %q", variants[0], "deacon/")
	}
}

// Dispatch resolves the agent; bd writes the step wisps without knowing it. A
// molecule dispatched to a dog left its steps assigned to the bare pool role
// "dog", where no per-agent lookup could reach them.
func TestStepsNeedingAssignee(t *testing.T) {
	target := "deacon/dogs/alpha"
	steps := []*beads.Issue{
		{ID: "hq-wisp-b97", Status: "open", Assignee: "dog"},
		{ID: "hq-wisp-c12", Status: "in_progress", Assignee: ""},
		{ID: "hq-wisp-d34", Status: "open", Assignee: "deacon/dogs/alpha"},
		{ID: "hq-wisp-e56", Status: "closed", Assignee: "dog"},
		{ID: "", Status: "open", Assignee: "dog"},
		nil,
	}

	got := stepsNeedingAssignee(steps, target)
	want := []string{"hq-wisp-b97", "hq-wisp-c12"}
	if len(got) != len(want) {
		t.Fatalf("stepsNeedingAssignee = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stepsNeedingAssignee = %v, want %v", got, want)
		}
	}
}

// A step already pointing at the agent under a different spelling is left
// alone: re-pointing it would cost a Dolt commit and change nothing.
func TestStepsNeedingAssigneeIgnoresEquivalentSpellings(t *testing.T) {
	cases := []struct {
		stepAssignee string
		target       string
	}{
		{"deacon", "deacon/"},
		{"deacon/", "deacon"},
		{"gastown/polecat/quartz", "gastown/polecats/quartz"},
		{"gastown/quartz", "gastown/polecats/quartz"},
	}

	for _, c := range cases {
		step := &beads.Issue{ID: "hq-wisp-1", Status: "open", Assignee: c.stepAssignee}
		if stepNeedsAssignee(step, c.target) {
			t.Errorf("step assigned %q considered stale against target %q", c.stepAssignee, c.target)
		}
	}
}

// Every `bd update` that sets an assignee goes through assigneeFlag, so a write
// site cannot store a non-canonical spelling even when its caller holds one.
// The bare "deacon" case is the exact split that stranded the patrol wisps.
func TestAssigneeFlagCanonicalizes(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want string
	}{
		{"bare town role", "deacon", "--assignee=deacon/"},
		{"town role already canonical", "deacon/", "--assignee=deacon/"},
		{"bare mayor", "mayor", "--assignee=mayor/"},
		{"dog worker", "deacon/dogs/alpha", "--assignee=deacon/dogs/alpha"},
		{"rig role", "gastown/witness", "--assignee=gastown/witness"},
		{"legacy singular polecat", "gastown/polecat/jasper", "--assignee=gastown/polecats/jasper"},
		{"crew worker", "gastown/crew/amber", "--assignee=gastown/crew/amber"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := assigneeFlag(tc.addr); got != tc.want {
				t.Errorf("assigneeFlag(%q) = %q, want %q", tc.addr, got, tc.want)
			}
		})
	}
}

// An address agentaddr cannot parse is written through unchanged rather than
// dropped, so a write site never silently loses an assignee it was handed.
func TestAssigneeFlagPreservesUnparseableAddress(t *testing.T) {
	const addr = "not/a/known/address"
	if got := assigneeFlag(addr); got != "--assignee="+addr {
		t.Errorf("assigneeFlag(%q) = %q, want passthrough", addr, got)
	}
	if got := agentaddr.Canonical(""); got != "" {
		t.Errorf("Canonical(\"\") = %q, want empty", got)
	}
}
