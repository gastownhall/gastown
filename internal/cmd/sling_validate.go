package cmd

import (
	"fmt"
	"strings"
)

// knownRoles lists valid second-segment roles in path-style sling targets.
var knownRoles = map[string]bool{
	"polecats": true,
	"crew":     true,
	"witness":  true,
	"refinery": true,
}

// ValidateTarget performs lightweight pre-checks on a sling target string,
// catching common mistakes before resolveTarget can trigger side-effects
// like polecat spawning. It returns a non-nil error with a helpful message
// when the target is clearly malformed.
//
// It intentionally does NOT duplicate the full resolution logic — valid
// targets that pass this check are still resolved by resolveTarget.
func ValidateTarget(target string) error {
	// Self, empty, and role shortcuts are always fine.
	if target == "" || target == "." {
		return nil
	}

	// No slashes → could be rig name or role shortcut; let resolveTarget decide.
	if !strings.Contains(target, "/") {
		return nil
	}

	parts := strings.Split(target, "/")

	// Reject empty segments: "rig//polecats", "/polecats", "rig/polecats/"
	for i, p := range parts {
		if p == "" {
			return fmt.Errorf("invalid target %q: empty path segment at position %d\n"+
				"Valid formats:\n"+
				"  <rig>                  auto-spawn polecat\n"+
				"  <rig>/polecats/<name>  specific polecat\n"+
				"  <rig>/crew/<name>      crew worker\n"+
				"  <rig>/witness          rig witness\n"+
				"  <rig>/refinery         rig refinery\n"+
				"  deacon/dogs            dog pool\n"+
				"  mayor                  town mayor",
				target, i)
		}
	}

	// Dog targets are valid at any depth (deacon/dogs, deacon/dogs/<name>).
	// Deacon sub-path validation is handled downstream by IsDogTarget/resolveTarget.
	if strings.ToLower(parts[0]) == "deacon" {
		return nil
	}

	// Mayor has no sub-agents.
	if strings.ToLower(parts[0]) == "mayor" {
		return fmt.Errorf("invalid target %q: mayor does not have sub-agents\n"+
			"Use 'mayor' to target the mayor directly", target)
	}

	// Path targets: parts[0] = rig, parts[1] = role or shorthand name.
	// Two-segment paths like "gastown/nux" are polecat/crew shorthand —
	// resolvePathToSession handles these by trying polecat then crew lookup.
	// We only validate when the second segment IS a known role.
	if len(parts) >= 2 {
		role := strings.ToLower(parts[1])
		if knownRoles[role] {
			// Known role: apply role-specific constraints.
			if role == "witness" || role == "refinery" {
				// Witness and refinery are singleton roles — no sub-agents.
				if len(parts) > 2 {
					return fmt.Errorf("invalid target %q: %s does not have named sub-agents\n"+
						"Usage: %s/%s", target, role, parts[0], role)
				}
			} else if len(parts) == 2 {
				// Crew and polecats require a name segment.
				if role == "crew" {
					return fmt.Errorf("invalid target %q: crew requires a worker name\n"+
						"Usage: %s/crew/<name>", target, parts[0])
				}
				return fmt.Errorf("invalid target %q: polecats requires a polecat name\n"+
					"Usage: %s/polecats/<name>\n"+
					"Or use just %q to auto-spawn a polecat", target, parts[0], parts[0])
			}
			// Too many segments for role paths: rig/role/name/extra
			if len(parts) > 3 {
				return fmt.Errorf("invalid target %q: too many path segments (max 3: rig/role/name)", target)
			}
		} else if len(parts) > 2 {
			// Not a known role but has 3+ segments — not a valid shorthand.
			return fmt.Errorf("invalid target %q: unknown role %q\n"+
				"Valid roles after a rig name:\n"+
				"  %s/polecats/<name>  specific polecat\n"+
				"  %s/crew/<name>      crew worker\n"+
				"  %s/witness          rig witness\n"+
				"  %s/refinery         rig refinery\n"+
				"Or use just %q to target by name shorthand",
				target, parts[1], parts[0], parts[0], parts[0], parts[0], parts[0])
		}
		// else: 2-segment with unknown role → polecat/crew shorthand, let resolveTarget handle.
	}

	return nil
}

// ValidateFormulaFamily checks that the formula family is appropriate for the target agent.
// Specifically, it prevents polecat formulas (mol-polecat-*) from being dispatched to dogs,
// which is the invalid combination that caused "DELETED molecule + INVALID formula" issues.
// Returns a clear error if the formula is inappropriate for the target.
func ValidateFormulaFamily(formulaName string, targetAgent string) error {
	// No formula to validate
	if formulaName == "" {
		return nil
	}

	// Check if this is a polecat-specific formula
	isPolecatFormula := strings.HasPrefix(formulaName, "mol-polecat-")
	if !isPolecatFormula {
		return nil
	}

	// Check if target is a dog
	isDogTarget := strings.Contains(targetAgent, "/dogs/")
	if isDogTarget {
		return fmt.Errorf("formula %q is designed for polecats but target %q is a dog\n"+
			"Polecat formulas cannot be dispatched to dogs", formulaName, targetAgent)
	}

	return nil
}
