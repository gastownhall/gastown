package agentaddr

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCanonicalForms(t *testing.T) {
	cases := []struct {
		in   string
		want Address
	}{
		// Town-level singletons, in every spelling seen in the wisps table.
		{"deacon", Address{Role: RoleDeacon}},
		{"deacon/", Address{Role: RoleDeacon}},
		{"  deacon/  ", Address{Role: RoleDeacon}},
		{"Deacon", Address{Role: RoleDeacon}},
		{"gastown/deacon", Address{Role: RoleDeacon}},
		{"mayor", Address{Role: RoleMayor}},
		{"mayor/", Address{Role: RoleMayor}},
		{"gastown/mayor", Address{Role: RoleMayor}},
		{"overseer", Address{Role: RoleOverseer}},
		{"boot", Address{Role: RoleBoot}},
		{"deacon/boot", Address{Role: RoleBoot}},

		// Rig-scoped singletons, including the legacy unqualified form.
		{"gastown/witness", Address{Rig: "gastown", Role: RoleWitness}},
		{"sandbox/refinery", Address{Rig: "sandbox", Role: RoleRefinery}},
		{"/witness", Address{Role: RoleWitness}},
		{"/refinery", Address{Role: RoleRefinery}},

		// Named workers.
		{"gastown/polecats/obsidian", Address{Rig: "gastown", Role: RolePolecat, Name: "obsidian"}},
		{"gastown/polecat/obsidian", Address{Rig: "gastown", Role: RolePolecat, Name: "obsidian"}},
		{"gastown/obsidian", Address{Rig: "gastown", Role: RolePolecat, Name: "obsidian"}},
		{"gastown/crew/max", Address{Rig: "gastown", Role: RoleCrew, Name: "max"}},
		{"deacon/dogs/alpha", Address{Role: RoleDog, Name: "alpha"}},
		{"deacon/dog/alpha", Address{Role: RoleDog, Name: "alpha"}},
		{"dog:alpha", Address{Role: RoleDog, Name: "alpha"}},

		// Bare pool roles: recognised but not complete.
		{"dog", Address{Role: RoleDog}},
		{"dogs", Address{Role: RoleDog}},
		{"deacon/dogs", Address{Role: RoleDog}},
		{"witness", Address{Rig: "", Role: RoleWitness}},
		{"gastown/polecats", Address{Rig: "gastown", Role: RolePolecat}},
	}

	for _, c := range cases {
		got, ok := Parse(c.in)
		if !ok {
			t.Errorf("Parse(%q) returned ok=false, want a parsed address", c.in)
			continue
		}
		if got != c.want {
			t.Errorf("Parse(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParseRejectsNonAddresses(t *testing.T) {
	for _, in := range []string{"", "   ", "/", "///", "gastown//witness", "a/b/c/d", "gastown/overseer"} {
		if got, ok := Parse(in); ok {
			t.Errorf("Parse(%q) = %+v, ok=true; want ok=false", in, got)
		}
	}
}

func TestStringIsTheSingleCanonicalForm(t *testing.T) {
	cases := []struct {
		addr Address
		want string
	}{
		{Address{Role: RoleOverseer}, "overseer"},
		{Address{Role: RoleMayor}, "mayor/"},
		{Address{Role: RoleDeacon}, "deacon/"},
		{Address{Role: RoleBoot}, "deacon/boot"},
		{Address{Role: RoleDog, Name: "alpha"}, "deacon/dogs/alpha"},
		{Address{Rig: "gastown", Role: RoleWitness}, "gastown/witness"},
		{Address{Rig: "gastown", Role: RoleRefinery}, "gastown/refinery"},
		{Address{Rig: "gastown", Role: RolePolecat, Name: "obsidian"}, "gastown/polecats/obsidian"},
		{Address{Rig: "gastown", Role: RoleCrew, Name: "max"}, "gastown/crew/max"},
	}
	for _, c := range cases {
		if got := c.addr.String(); got != c.want {
			t.Errorf("Address%+v.String() = %q, want %q", c.addr, got, c.want)
		}
	}
}

// Every spelling of one agent must normalize to the same stored string. This is
// the property whose absence stranded hooked patrol wisps.
func TestNormalizeCollapsesEverySpelling(t *testing.T) {
	groups := [][]string{
		{"deacon", "deacon/", "Deacon", "gastown/deacon", " deacon "},
		{"mayor", "mayor/", "MAYOR", "gastown/mayor"},
		{"gastown/witness", "gastown/witness/", "GASTOWN/WITNESS"},
		{"deacon/dogs/alpha", "deacon/dog/alpha", "dog:alpha"},
		{"gastown/polecats/obsidian", "gastown/polecat/obsidian", "gastown/obsidian"},
	}
	for _, group := range groups {
		want := Normalize(group[0])
		for _, spelling := range group {
			if got := Normalize(spelling); got != want {
				t.Errorf("Normalize(%q) = %q, want %q (same agent as %q)", spelling, got, want, group[0])
			}
			if !Equal(spelling, group[0]) {
				t.Errorf("Equal(%q, %q) = false, want true", spelling, group[0])
			}
		}
	}
}

// "/witness" names a witness whose rig was dropped at the write site. It is a
// variant to match on read, but it cannot be normalized: nothing in the string
// says which rig, and guessing would assign the bead to the wrong agent.
func TestRiglessWitnessIsMatchedButNotNormalized(t *testing.T) {
	parsed, ok := Parse("/witness")
	if !ok || parsed.Role != RoleWitness {
		t.Fatalf("Parse(\"/witness\") = %+v, ok=%v; want a witness", parsed, ok)
	}
	if parsed.IsComplete() {
		t.Error("Parse(\"/witness\").IsComplete() = true, want false: the rig is unknown")
	}
	if got := Normalize("/witness"); got != "/witness" {
		t.Errorf("Normalize(\"/witness\") = %q, want it left alone", got)
	}
	if !contains(Variants("gastown/witness"), "/witness") {
		t.Error("Variants(gastown/witness) must include the legacy \"/witness\" rows")
	}
}

func TestNormalizeLeavesUnrecognisedInputAlone(t *testing.T) {
	for _, in := range []string{"not an address", "gastown//witness", "a/b/c/d"} {
		if got := Normalize(in); got != strings.TrimSpace(in) {
			t.Errorf("Normalize(%q) = %q, want the input unchanged", in, got)
		}
	}
}

// A bare pool role must never be treated as a storable assignee: that is the
// bug where child step wisps were written as "dog" instead of the resolved
// "deacon/dogs/alpha".
func TestBarePoolRolesAreIncomplete(t *testing.T) {
	for _, in := range []string{"dog", "dogs", "deacon/dogs", "witness", "refinery", "crew", "gastown/polecats"} {
		parsed, ok := Parse(in)
		if !ok {
			t.Fatalf("Parse(%q) = ok false, want the pool role recognised", in)
		}
		if parsed.IsComplete() {
			t.Errorf("Parse(%q).IsComplete() = true, want false", in)
		}
	}
	for _, in := range []string{"deacon", "mayor/", "gastown/witness", "deacon/dogs/alpha", "gastown/polecats/obsidian"} {
		parsed, _ := Parse(in)
		if !parsed.IsComplete() {
			t.Errorf("Parse(%q).IsComplete() = false, want true", in)
		}
	}
}

func TestVariantsLeadWithCanonicalAndCoverLegacySpellings(t *testing.T) {
	cases := []struct {
		in       string
		contains []string
	}{
		{"deacon", []string{"deacon/", "deacon"}},
		{"deacon/", []string{"deacon/", "deacon"}},
		{"mayor", []string{"mayor/", "mayor"}},
		{"gastown/witness", []string{"gastown/witness", "/witness"}},
		{"gastown/polecats/obsidian", []string{"gastown/polecats/obsidian", "gastown/polecat/obsidian", "gastown/obsidian"}},
		{"deacon/dogs/alpha", []string{"deacon/dogs/alpha", "deacon/dog/alpha", "dog:alpha"}},
	}
	for _, c := range cases {
		got := Variants(c.in)
		if len(got) == 0 || got[0] != Normalize(c.in) {
			t.Errorf("Variants(%q) = %v, want canonical form %q first", c.in, got, Normalize(c.in))
		}
		for _, want := range c.contains {
			if !contains(got, want) {
				t.Errorf("Variants(%q) = %v, missing %q", c.in, got, want)
			}
		}
	}
}

func TestVariantsHasNoDuplicates(t *testing.T) {
	for _, in := range []string{"deacon", "mayor", "gastown/witness", "gastown/polecats/obsidian", "deacon/dogs/alpha", "overseer"} {
		got := Variants(in)
		seen := map[string]bool{}
		for _, v := range got {
			if seen[v] {
				t.Errorf("Variants(%q) = %v, contains duplicate %q", in, got, v)
			}
			seen[v] = true
		}
	}
}

func TestVariantsOfUnrecognisedInputIsItself(t *testing.T) {
	if got := Variants("not an address"); !reflect.DeepEqual(got, []string{"not an address"}) {
		t.Errorf("Variants(unrecognised) = %v, want the input itself", got)
	}
	if got := Variants("  "); got != nil {
		t.Errorf("Variants(blank) = %v, want nil", got)
	}
}

// MatchKey must stay byte-for-byte what the mail send path used, so that
// adopting the shared package changes no mail behaviour.
func TestMatchKeyPreservesLegacyMailNormalization(t *testing.T) {
	legacy := func(addr string) string {
		return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(addr)), "/")
	}
	for _, in := range []string{
		"", "  ", "mayor", "Mayor/", "deacon/", "DEACON", " gastown/witness ",
		"gastown/polecats/Toast", "overseer", "//", "not an address",
	} {
		if got, want := MatchKey(in), legacy(in); got != want {
			t.Errorf("MatchKey(%q) = %q, want %q (legacy mail behaviour)", in, got, want)
		}
	}
}

// The concrete leak: `gt sling deacon` wrote "deacon/" and `gt patrol report`
// looked up "deacon". Through the canonical type they are one agent.
func TestSlingAndPatrolSpellingsResolveToOneAgent(t *testing.T) {
	slingWrote := Normalize("deacon/")
	patrolLookedUp := Normalize("deacon")
	if slingWrote != patrolLookedUp {
		t.Fatalf("sling wrote %q but patrol looked up %q; they must be identical", slingWrote, patrolLookedUp)
	}
	if !contains(Variants("deacon"), "deacon/") || !contains(Variants("deacon"), "deacon") {
		t.Errorf("Variants(deacon) = %v, must match rows written under both spellings", Variants("deacon"))
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
