// Package agentaddr provides the single canonical representation of a Gas Town
// agent address.
//
// Gas Town writes an agent's address into bead assignee fields, hook records
// and mail envelopes. Before this package each command built that string by
// hand, so the same agent was stored under several spellings: `gt sling deacon`
// wrote "deacon/" while `gt patrol new` wrote "deacon", and an exact-match
// lookup for one could not see rows written by the other. That mismatch
// stranded hooked patrol wisps and leaked them into the open backlog.
//
// The rule this package enforces is: parse liberally, store one way, and match
// across every historical spelling.
//
//   - Parse accepts every spelling Gas Town has ever written.
//   - Address.String renders the one canonical form that must be stored.
//   - Address.Variants lists the equivalent spellings a read must also match,
//     so rows written before this package are still found.
package agentaddr

import (
	"strings"
)

// Role is the canonical role token of an agent address.
type Role string

// Canonical roles. These mirror session.Role, but agentaddr is a leaf package
// with no Gas Town dependencies so that both internal/cmd and internal/mail can
// use it without an import cycle.
const (
	RoleUnknown  Role = ""
	RoleOverseer Role = "overseer"
	RoleMayor    Role = "mayor"
	RoleDeacon   Role = "deacon"
	RoleBoot     Role = "boot"
	RoleDog      Role = "dog"
	RoleWitness  Role = "witness"
	RoleRefinery Role = "refinery"
	RolePolecat  Role = "polecat"
	RoleCrew     Role = "crew"
)

// Address is a parsed Gas Town agent address.
//
// Rig is empty for town-level agents (mayor, deacon, boot, dogs, overseer).
// Name is empty for singleton roles (mayor, deacon, witness, refinery).
type Address struct {
	Rig  string
	Role Role
	Name string
}

// townLevel reports whether the role lives at the town level, i.e. has no rig.
func (a Address) townLevel() bool {
	switch a.Role {
	case RoleOverseer, RoleMayor, RoleDeacon, RoleBoot, RoleDog:
		return true
	default:
		return false
	}
}

// needsName reports whether the role identifies a specific named worker.
func (a Address) needsName() bool {
	switch a.Role {
	case RoleDog, RolePolecat, RoleCrew:
		return true
	default:
		return false
	}
}

// IsComplete reports whether the address identifies exactly one agent.
//
// An incomplete address is a bare pool role such as "dog" or "witness": the
// role is known but the rig or worker name is missing, so it cannot be stored
// as an assignee. Dispatch code uses this to detect the bug where child step
// wisps inherited the bare pool role instead of the resolved root address.
func (a Address) IsComplete() bool {
	if a.Role == RoleUnknown {
		return false
	}
	if !a.townLevel() && a.Rig == "" {
		return false
	}
	if a.needsName() && a.Name == "" {
		return false
	}
	return true
}

// String renders the canonical stored form of the address.
//
// There is exactly one correct string for each agent:
//
//	overseer               → "overseer"
//	mayor                  → "mayor/"
//	deacon                 → "deacon/"
//	boot                   → "deacon/boot"
//	dog alpha              → "deacon/dogs/alpha"
//	witness of gastown     → "gastown/witness"
//	refinery of gastown    → "gastown/refinery"
//	polecat toast, gastown → "gastown/polecats/toast"
//	crew max, gastown      → "gastown/crew/max"
//
// An incomplete address renders as its bare role token, which is never a valid
// assignee. Callers that store the result must check IsComplete first.
func (a Address) String() string {
	if !a.IsComplete() {
		return string(a.Role)
	}
	switch a.Role {
	case RoleOverseer:
		return "overseer"
	case RoleMayor:
		return "mayor/"
	case RoleDeacon:
		return "deacon/"
	case RoleBoot:
		return "deacon/boot"
	case RoleDog:
		return "deacon/dogs/" + a.Name
	case RoleWitness:
		return a.Rig + "/witness"
	case RoleRefinery:
		return a.Rig + "/refinery"
	case RolePolecat:
		return a.Rig + "/polecats/" + a.Name
	case RoleCrew:
		return a.Rig + "/crew/" + a.Name
	default:
		return ""
	}
}

// Variants returns every spelling that refers to this agent, canonical form
// first. Reads must match all of them: rows written before this package exists
// still carry the legacy spellings, and a lookup that only matched the
// canonical form would silently skip them.
func (a Address) Variants() []string {
	canonical := a.String()
	if !a.IsComplete() {
		return uniqueNonEmpty([]string{canonical})
	}

	var out []string
	switch a.Role {
	case RoleOverseer:
		out = []string{"overseer", "overseer/"}
	case RoleMayor:
		out = []string{"mayor/", "mayor"}
	case RoleDeacon:
		out = []string{"deacon/", "deacon"}
	case RoleBoot:
		out = []string{"deacon/boot", "boot"}
	case RoleDog:
		out = []string{
			"deacon/dogs/" + a.Name,
			"deacon/dog/" + a.Name,
			"dog:" + a.Name,
		}
	case RoleWitness:
		// "/witness" is the legacy unqualified form written before the rig was
		// resolved at the write site.
		out = []string{a.Rig + "/witness", "/witness"}
	case RoleRefinery:
		out = []string{a.Rig + "/refinery", "/refinery"}
	case RolePolecat:
		out = []string{
			a.Rig + "/polecats/" + a.Name,
			a.Rig + "/polecat/" + a.Name,
			a.Rig + "/" + a.Name,
		}
	case RoleCrew:
		out = []string{
			a.Rig + "/crew/" + a.Name,
			a.Rig + "/" + a.Name,
		}
	default:
		out = []string{canonical}
	}
	return uniqueNonEmpty(append([]string{canonical}, out...))
}

// Parse converts any spelling Gas Town has written into a canonical Address.
// The second return value is false when the input cannot be recognised at all;
// callers should then leave the original string untouched rather than guess.
func Parse(s string) (Address, bool) {
	trimmed := strings.TrimSpace(s)
	// Collapse repeated trailing slashes ("slingshot///") but remember that a
	// town-level role legitimately ends in one.
	for strings.HasSuffix(trimmed, "/") {
		trimmed = strings.TrimSuffix(trimmed, "/")
	}
	if trimmed == "" {
		return Address{}, false
	}

	// "dog:alpha" / "dog:" shorthand used by sling targets.
	if rest, ok := strings.CutPrefix(strings.ToLower(trimmed), "dog:"); ok {
		if strings.Contains(rest, "/") {
			return Address{}, false
		}
		return Address{Role: RoleDog, Name: sameCase(trimmed, rest)}, true
	}

	parts := strings.Split(trimmed, "/")
	for _, part := range parts {
		if part == "" {
			// An interior empty segment ("gastown//witness") is not an address.
			// A leading empty segment is the legacy "/witness" form, handled
			// below, so only reject when it is not that shape.
			if !(len(parts) == 2 && parts[0] == "") {
				return Address{}, false
			}
		}
	}

	switch len(parts) {
	case 1:
		return parseBare(parts[0])
	case 2:
		return parseTwo(parts[0], parts[1])
	case 3:
		return parseThree(parts[0], parts[1], parts[2])
	default:
		return Address{}, false
	}
}

func parseBare(token string) (Address, bool) {
	switch strings.ToLower(token) {
	case "overseer":
		return Address{Role: RoleOverseer}, true
	case "mayor":
		return Address{Role: RoleMayor}, true
	case "deacon":
		return Address{Role: RoleDeacon}, true
	case "boot":
		return Address{Role: RoleBoot}, true
	// Bare pool roles: recognised, but incomplete. They are the exact strings
	// that leaked onto child step wisps, so naming them lets callers detect the
	// mistake instead of storing them.
	case "dog", "dogs":
		return Address{Role: RoleDog}, true
	case "witness":
		return Address{Role: RoleWitness}, true
	case "refinery":
		return Address{Role: RoleRefinery}, true
	case "polecat", "polecats":
		return Address{Role: RolePolecat}, true
	case "crew":
		return Address{Role: RoleCrew}, true
	default:
		return Address{}, false
	}
}

func parseTwo(first, second string) (Address, bool) {
	lowerFirst, lowerSecond := strings.ToLower(first), strings.ToLower(second)
	// Rig names are lowercase identifiers (they name a directory and a Dolt
	// database), so casing them differently never means a different rig.
	first = lowerFirst

	// Legacy unqualified rig-scoped form: "/witness", "/refinery".
	if first == "" {
		switch lowerSecond {
		case "witness":
			return Address{Role: RoleWitness}, true
		case "refinery":
			return Address{Role: RoleRefinery}, true
		default:
			return Address{}, false
		}
	}

	// Town-level singletons are town-level however they were addressed:
	// "gastown/mayor" is still the one mayor.
	switch lowerSecond {
	case "mayor":
		return Address{Role: RoleMayor}, true
	case "deacon":
		return Address{Role: RoleDeacon}, true
	}

	if lowerFirst == "deacon" {
		switch lowerSecond {
		case "boot":
			return Address{Role: RoleBoot}, true
		case "dog", "dogs":
			return Address{Role: RoleDog}, true
		default:
			return Address{}, false
		}
	}

	switch lowerSecond {
	case "witness":
		return Address{Rig: first, Role: RoleWitness}, true
	case "refinery":
		return Address{Rig: first, Role: RoleRefinery}, true
	case "polecat", "polecats", "crew":
		// A pool with a rig but no worker name.
		if lowerSecond == "crew" {
			return Address{Rig: first, Role: RoleCrew}, true
		}
		return Address{Rig: first, Role: RolePolecat}, true
	case "overseer", "boot":
		return Address{}, false
	default:
		// "gastown/toast" is the shorthand polecat form.
		return Address{Rig: first, Role: RolePolecat, Name: second}, true
	}
}

func parseThree(first, second, third string) (Address, bool) {
	lowerFirst, lowerSecond := strings.ToLower(first), strings.ToLower(second)
	first = lowerFirst

	if lowerFirst == "deacon" {
		if lowerSecond == "dog" || lowerSecond == "dogs" {
			return Address{Role: RoleDog, Name: third}, true
		}
		return Address{}, false
	}

	switch lowerSecond {
	case "polecat", "polecats":
		return Address{Rig: first, Role: RolePolecat, Name: third}, true
	case "crew":
		return Address{Rig: first, Role: RoleCrew, Name: third}, true
	default:
		return Address{}, false
	}
}

// Normalize returns the canonical stored form of addr.
//
// Unrecognised and incomplete input is returned trimmed but otherwise
// unchanged: normalizing must never invent an address that identifies a
// different agent than the caller meant.
func Normalize(addr string) string {
	parsed, ok := Parse(addr)
	if !ok || !parsed.IsComplete() {
		return strings.TrimSpace(addr)
	}
	return parsed.String()
}

// Variants returns every spelling equivalent to addr, canonical form first.
// Unrecognised input yields just itself, so callers can always use the result
// as their complete match set.
func Variants(addr string) []string {
	parsed, ok := Parse(addr)
	if !ok || !parsed.IsComplete() {
		if trimmed := strings.TrimSpace(addr); trimmed != "" {
			return []string{trimmed}
		}
		return nil
	}
	return parsed.Variants()
}

// Equal reports whether two addresses name the same agent, comparing across
// spellings and ignoring case.
func Equal(a, b string) bool {
	parsedA, okA := Parse(a)
	parsedB, okB := Parse(b)
	if okA && okB && parsedA.IsComplete() && parsedB.IsComplete() {
		return strings.EqualFold(parsedA.String(), parsedB.String())
	}
	return MatchKey(a) == MatchKey(b)
}

// MatchKey returns the loose comparison key for an address: trimmed,
// lowercased, with a trailing slash removed, so that "Mayor/" and "mayor"
// compare equal.
//
// This is the normalization that used to live unexported in the mail send path,
// where it was the only reason mail resolved these addresses reliably. It is
// kept byte-for-byte identical so that moving it here changes no mail
// behaviour. Prefer Equal for new code: it also matches across the
// polecats/polecat and dogs/dog spellings, which this key does not.
func MatchKey(addr string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(addr)), "/")
}

// sameCase recovers the original casing of a suffix that was matched against a
// lowercased copy of the whole string.
func sameCase(original, lowerSuffix string) string {
	if len(lowerSuffix) == 0 {
		return ""
	}
	return original[len(original)-len(lowerSuffix):]
}

func uniqueNonEmpty(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
