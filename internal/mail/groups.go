package mail

import (
	"strings"

	"github.com/steveyegge/gastown/internal/constants"
)

// [si-zgo] THE GROUP-ADDRESS VOCABULARY — one owner, for both the send path and the directory.
//
// WHY THIS FILE EXISTS. `gt mail directory` advertised `@crew` and `gt mail send @crew` answered
// "invalid group address: @crew". A vocabulary one side exports and the other never accepts.
// silicon/refinery hit it trying to reach two crew members and had to route through the Mayor.
//
// THE ELEMENT IS (GROUP, ARITY), NOT A BARE TOKEN — and that is what resolves the defect rather
// than papering over it. Modelled as bare strings, the two obvious repairs are both wrong:
// teaching send to accept bare `@crew` invents an audience that does not exist (crew of WHICH
// rig?), and dropping `@crew` from the directory hides a real, reachable one. With arity in the
// element, `@crew` is a genuine member of the vocabulary AND the directory's bare rendering of it
// was malformed. The directory's bug was rendering an arity-1 group in arity-0 form.
//
// EVERY CONSUMER COMPOSES THIS. Do not re-spell the set anywhere — a hand-written copy is a gate
// that silently stops tracking the real one, and this defect is what that looks like in
// production. tests/TestNoConsumerHardcodesTheGroupVocabulary enforces it by AST.

// GroupSpec is one entry in the group-address vocabulary: the bare group token, whether it
// requires a qualifier, and what the router resolves it to.
type GroupSpec struct {
	// Name is the token after '@', e.g. "town" or "crew".
	Name string
	// Qualifier names the required argument ("rig", "name"), or "" for an arity-0 group.
	Qualifier string
	// Type and RoleType are what parseGroupAddress yields for this entry.
	Type     GroupType
	RoleType string
}

// Arity reports how many qualifiers the group requires: 0 or 1.
func (g GroupSpec) Arity() int {
	if g.Qualifier == "" {
		return 0
	}
	return 1
}

// Address renders the canonical form a human can act on: "@town" for arity 0, "@crew/<rig>" for
// arity 1. An arity-1 group rendered without its qualifier is exactly the si-zgo defect, so the
// rendering is derived from Arity rather than written per entry.
func (g GroupSpec) Address() string {
	if g.Arity() == 0 {
		return "@" + g.Name
	}
	return "@" + g.Name + "/<" + g.Qualifier + ">"
}

// GroupVocabulary is the single source of truth for group addresses. The send path (see
// parseGroupAddress) and `gt mail directory` both derive from it.
var GroupVocabulary = []GroupSpec{
	{Name: "overseer", Type: GroupTypeOverseer},
	{Name: "town", Type: GroupTypeTown},
	{Name: "witnesses", Type: GroupTypeRole, RoleType: constants.RoleWitness},
	{Name: "dogs", Type: GroupTypeRole, RoleType: "dog"},
	{Name: "refineries", Type: GroupTypeRole, RoleType: constants.RoleRefinery},
	{Name: "deacons", Type: GroupTypeRole, RoleType: constants.RoleDeacon},

	{Name: "rig", Qualifier: "rig", Type: GroupTypeRig},
	{Name: constants.RoleCrew, Qualifier: "rig", Type: GroupTypeRigRole, RoleType: constants.RoleCrew},
	{Name: "polecats", Qualifier: "rig", Type: GroupTypeRigRole, RoleType: constants.RolePolecat},
}

// lookupGroup returns the vocabulary entry for a bare group name.
func lookupGroup(name string) (GroupSpec, bool) {
	for _, g := range GroupVocabulary {
		if g.Name == name {
			return g, true
		}
	}
	return GroupSpec{}, false
}

// GroupAddresses renders every address in the vocabulary, canonically. This is what the directory
// advertises — derived, so a group added here reaches readers without a second edit.
func GroupAddresses() []string {
	out := make([]string, 0, len(GroupVocabulary))
	for _, g := range GroupVocabulary {
		out = append(out, g.Address())
	}
	return out
}

// ParseGroupAddress is the exported entry point for parsing a group address, so tests and
// consumers can ask the ROUTER whether an address routes rather than re-deriving the rule.
// Returns nil for anything the vocabulary does not admit, including an arity-1 group written
// without its qualifier (bare "@crew") and an arity-0 group written with one ("@town/x").
func ParseGroupAddress(address string) *ParsedGroup {
	return parseGroupAddress(address)
}

// splitGroupAddress splits "@name/qualifier" into its parts. The qualifier is "" when absent.
func splitGroupAddress(address string) (name, qualifier string) {
	trimmed := strings.TrimPrefix(address, "@")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], ""
}
