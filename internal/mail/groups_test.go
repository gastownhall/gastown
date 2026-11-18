package mail

import (
	"strings"
	"testing"
)

// [si-zgo] THE PARITY GATE — advertised must route, routable must be advertised.
//
// The defect: `gt mail directory` listed `@crew`; `gt mail send @crew` answered "invalid group
// address". A vocabulary one side exports and the other never accepts.
//
// WHY THIS IS A ROUND TRIP AND NOT A SET COMPARISON, which is the whole design of this file:
// the original spec asked for set equality between the directory's list and the router's
// vocabulary. Now that the directory is DERIVED from GroupVocabulary, that comparison compares a
// thing to its own source and passes by construction — a tautology with a green tick. So the left
// side here is DATA (what the directory advertises) and the right side is BEHAVIOUR (what
// ParseGroupAddress actually accepts). Derivation cannot make those trivially equal.
//
// Both directions are required. "Advertised => routable" alone is satisfied by advertising
// nothing; "routable => advertised" alone is satisfied by advertising everything plus junk.

// advertisedAddresses is the group vocabulary as the directory renders it. The directory command
// composes exactly this (see internal/cmd/mail_directory.go), so this test reads what ships.
func advertisedAddresses() []string { return GroupAddresses() }

func TestEveryAdvertisedGroupAddressRoutes(t *testing.T) {
	advertised := advertisedAddresses()

	// DENOMINATOR ASSERT. Without it the loop below passes vacuously on an empty vocabulary —
	// "every advertised address routes" is trivially true when nothing is advertised. That is
	// the failure this repo keeps finding in its own guards, so it is asserted, not assumed.
	if len(advertised) == 0 {
		t.Fatal("advertised group set is EMPTY — every assertion below would pass vacuously")
	}

	for _, addr := range advertised {
		// The directory renders arity-1 groups as a pattern, "@crew/<rig>". Substitute a
		// concrete qualifier: the parser's contract is about SHAPE, and a real send resolves
		// the rig separately.
		probe := strings.Replace(addr, "/<rig>", "/somerig", 1)
		probe = strings.Replace(probe, "/<name>", "/somename", 1)

		if got := ParseGroupAddress(probe); got == nil {
			t.Errorf("directory advertises %q but the router refuses %q — "+
				"this is the si-zgo defect: a vocabulary one side exports and the other "+
				"never accepts", addr, probe)
		}
	}
}

func TestEveryRoutableGroupIsAdvertised(t *testing.T) {
	if len(GroupVocabulary) == 0 {
		t.Fatal("GroupVocabulary is EMPTY — the assertions below would pass vacuously")
	}

	advertised := make(map[string]bool, len(GroupVocabulary))
	for _, a := range advertisedAddresses() {
		advertised[a] = true
	}

	for _, spec := range GroupVocabulary {
		// The correctly-arity'd rendering, derived from the spec rather than spelled here —
		// spelling it would be the third copy of the vocabulary this bead exists to remove.
		if !advertised[spec.Address()] {
			t.Errorf("router accepts group %q (arity %d) but the directory does not advertise "+
				"%q — a sendable audience no reader can discover",
				spec.Name, spec.Arity(), spec.Address())
		}
	}
}

// NEGATIVE CONTROL. Both loops above are "everything in X satisfies P" and are satisfiable by an
// over-permissive parser: if ParseGroupAddress returned non-nil for anything, both would pass.
// This is what makes the absence of failures above evidence rather than decoration.
func TestTheParserRefusesAddressesOutsideTheVocabulary(t *testing.T) {
	refused := []struct {
		addr string
		why  string
	}{
		{"@crew", "arity-1 group written bare — THE original si-zgo defect; " +
			"crew of which rig?"},
		{"@polecats", "arity-1 group written bare"},
		{"@rig", "arity-1 group written bare"},
		{"@town/somerig", "arity-0 group given a qualifier"},
		{"@nonsense", "not in the vocabulary at all"},
		{"@crew/", "arity-1 group with an empty qualifier"},
	}

	for _, tc := range refused {
		if got := ParseGroupAddress(tc.addr); got != nil {
			t.Errorf("ParseGroupAddress(%q) returned %+v, want nil — %s", tc.addr, got, tc.why)
		}
	}
}

// The arity-1 groups must still route WITH a qualifier. Without this, the negative control above
// could be satisfied by a parser that refuses @crew in every form, which would "fix" si-zgo by
// deleting the audience.
func TestArityOneGroupsRouteWhenQualified(t *testing.T) {
	for _, spec := range GroupVocabulary {
		if spec.Arity() != 1 {
			continue
		}
		addr := "@" + spec.Name + "/somerig"
		got := ParseGroupAddress(addr)
		if got == nil {
			t.Fatalf("ParseGroupAddress(%q) = nil, want a parse — the qualified form is the "+
				"one that must work", addr)
		}
		if got.Rig != "somerig" {
			t.Errorf("ParseGroupAddress(%q).Rig = %q, want %q", addr, got.Rig, "somerig")
		}
		if got.Type != spec.Type {
			t.Errorf("ParseGroupAddress(%q).Type = %q, want %q", addr, got.Type, spec.Type)
		}
	}
}
