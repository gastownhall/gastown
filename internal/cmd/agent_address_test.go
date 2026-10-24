package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/agentaddr"
	"github.com/steveyegge/gastown/internal/session"
)

// TestPatrolAssigneeMatchesSlingAssignee pins the invariant the address split
// broke: the string `gt patrol new` stores must be the string `gt sling` stores
// for the same agent. When they diverged, `gt patrol report` could not see a
// slung patrol and stranded its wisp.
func TestPatrolAssigneeMatchesSlingAssignee(t *testing.T) {
	cases := []struct {
		name     string
		role     Role
		rig      string
		identity *session.AgentIdentity
		want     string
	}{
		{
			name:     "deacon is town level and keeps its trailing slash",
			role:     RoleDeacon,
			identity: &session.AgentIdentity{Role: session.RoleDeacon},
			want:     "deacon/",
		},
		{
			name:     "witness is qualified by its rig",
			role:     RoleWitness,
			rig:      "gastown",
			identity: &session.AgentIdentity{Role: session.RoleWitness, Rig: "gastown"},
			want:     "gastown/witness",
		},
		{
			name:     "refinery is qualified by its rig",
			role:     RoleRefinery,
			rig:      "gastown",
			identity: &session.AgentIdentity{Role: session.RoleRefinery, Rig: "gastown"},
			want:     "gastown/refinery",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := buildPatrolConfig(tc.role, RoleInfo{Role: tc.role, Rig: tc.rig})
			if err != nil {
				t.Fatalf("buildPatrolConfig: %v", err)
			}
			if cfg.Assignee != tc.want {
				t.Errorf("patrol assignee = %q, want %q", cfg.Assignee, tc.want)
			}
			if got := canonicalAssigneeAddress(tc.identity); got != tc.want {
				t.Errorf("sling assignee = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPatrolAssigneeVariantsFindLegacyRows covers the read side: rows written
// before canonicalization carry the old spelling, so a lookup that matched only
// the canonical form would skip them.
func TestPatrolAssigneeVariantsFindLegacyRows(t *testing.T) {
	cfg, err := buildPatrolConfig(RoleDeacon, RoleInfo{Role: RoleDeacon})
	if err != nil {
		t.Fatalf("buildPatrolConfig: %v", err)
	}

	variants := cfg.assigneeVariants()
	if len(variants) == 0 || variants[0] != "deacon/" {
		t.Fatalf("variants = %v, want canonical %q first", variants, "deacon/")
	}

	var sawLegacy bool
	for _, v := range variants {
		if v == "deacon" {
			sawLegacy = true
		}
	}
	if !sawLegacy {
		t.Errorf("variants %v do not include the legacy bare %q spelling", variants, "deacon")
	}
}

// TestPatrolRigNameTownLevel guards the naive-cut bug: the canonical town-level
// address ends in a slash, which cutting at the first slash reports as a rig
// named "deacon".
func TestPatrolRigNameTownLevel(t *testing.T) {
	if got := patrolRigName(PatrolConfig{Assignee: "deacon/"}); got != "" {
		t.Errorf("patrolRigName(%q) = %q, want empty (town level)", "deacon/", got)
	}
	if got := patrolRigName(PatrolConfig{Assignee: "gastown/witness"}); got != "gastown" {
		t.Errorf("patrolRigName(%q) = %q, want %q", "gastown/witness", got, "gastown")
	}
}

// TestAddressForRoleRejectsIncompleteRoles ensures a rig-scoped role detected
// without its rig is refused rather than stored as a bare pool name.
func TestAddressForRoleRejectsIncompleteRoles(t *testing.T) {
	if addr, ok := addressForRole(RoleWitness, "", ""); ok {
		t.Errorf("addressForRole(witness, no rig) = %q, want rejected", addr.String())
	}
	if addr, ok := addressForRole(RolePolecat, "gastown", ""); ok {
		t.Errorf("addressForRole(polecat, no name) = %q, want rejected", addr.String())
	}
	addr, ok := addressForRole(RoleDog, "", "alpha")
	if !ok {
		t.Fatalf("addressForRole(dog, alpha) rejected, want accepted")
	}
	if got := addr.String(); got != "deacon/dogs/alpha" {
		t.Errorf("dog address = %q, want %q", got, "deacon/dogs/alpha")
	}
}

// TestNormalizeAddressPreservesMailBehaviour pins that routing mail through the
// shared package did not change how mail resolves an address.
func TestNormalizeAddressPreservesMailBehaviour(t *testing.T) {
	cases := map[string]string{
		"Mayor/":          "mayor",
		"mayor":           "mayor",
		"  deacon/  ":     "deacon",
		"gastown/Witness": "gastown/witness",
	}
	for in, want := range cases {
		if got := normalizeAddress(in); got != want {
			t.Errorf("normalizeAddress(%q) = %q, want %q", in, got, want)
		}
		if got := agentaddr.MatchKey(in); got != want {
			t.Errorf("agentaddr.MatchKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAssigneeFlagCanonicalizesWriteSites pins the gap that made 4780 worth
// porting onto this branch: every `bd update --assignee=` write site now routes
// through one helper, so the same agent cannot land in storage under two
// spellings depending on which command wrote the row (gt-cw1).
func TestAssigneeFlagCanonicalizesWriteSites(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want string
	}{
		{
			name: "bare deacon gains the trailing slash sling always wrote",
			addr: "deacon",
			want: "--assignee=deacon/",
		},
		{
			name: "deacon with trailing slash is already canonical",
			addr: "deacon/",
			want: "--assignee=deacon/",
		},
		{
			name: "legacy singular polecat segment becomes plural",
			addr: "gastown/polecat/toast",
			want: "--assignee=gastown/polecats/toast",
		},
		{
			name: "legacy singular dog segment becomes plural",
			addr: "deacon/dog/alpha",
			want: "--assignee=deacon/dogs/alpha",
		},
		{
			name: "rig-scoped deacon collapses to the one town-level deacon",
			addr: "gastown/deacon",
			want: "--assignee=deacon/",
		},
		{
			name: "surrounding space is trimmed",
			addr: "  gastown/polecats/toast  ",
			want: "--assignee=gastown/polecats/toast",
		},
		{
			name: "already-canonical worker address is unchanged",
			addr: "gastown/polecats/toast",
			want: "--assignee=gastown/polecats/toast",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := assigneeFlag(tc.addr); got != tc.want {
				t.Errorf("assigneeFlag(%q) = %q, want %q", tc.addr, got, tc.want)
			}
		})
	}
}

// TestAssigneeFlagPreservesUnparseableInput guards the deliberate conservatism
// in Normalize: a write site holding something this package cannot parse must
// still store what the caller meant rather than a guess or an empty assignee.
func TestAssigneeFlagPreservesUnparseableInput(t *testing.T) {
	for _, addr := range []string{"", "not/a/real/address/at/all", "overseer"} {
		want := "--assignee=" + agentaddr.Normalize(addr)
		if got := assigneeFlag(addr); got != want {
			t.Errorf("assigneeFlag(%q) = %q, want %q", addr, got, want)
		}
	}
}
