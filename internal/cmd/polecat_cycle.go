package cmd

import (
	"fmt"
	"strings"

	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/session"
)

// cyclePolecatSession switches to the next or previous polecat session in the same rig.
// direction: 1 for next, -1 for previous
// sessionOverride: if non-empty, use this instead of detecting current session
func cyclePolecatSession(direction int, sessionOverride string) error {
	currentSession, err := resolveCurrentSession(sessionOverride)
	if err != nil {
		return fmt.Errorf("not in a tmux session: %w", err)
	}
	if currentSession == "" {
		return fmt.Errorf("not in a tmux session")
	}

	rigName, _, ok := parsePolecatSessionName(currentSession)
	if !ok {
		return nil
	}

	sessions, err := findRigPolecatSessions(rigName)
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}

	return cycleInGroup(direction, currentSession, sessions)
}

// parsePolecatSessionName extracts rig and polecat name from a tmux session name.
// Format: gt-<rig>-<name> where name is NOT crew-*, witness, refinery, mayor, or deacon.
// Returns empty strings and false if the format doesn't match.
//
// Delegates to session.ParseSessionName for consistent parsing of hyphenated
// rig names (e.g., gt-my-rig-Toast correctly yields rig="my-rig", name="Toast").
func parsePolecatSessionName(sessionName string) (rigName, polecatName string, ok bool) { //nolint:unparam // polecatName kept for API consistency
	identity, err := session.ParseSessionName(sessionName)
	if err != nil {
		return "", "", false
	}
	if identity.Role != session.RolePolecat {
		return "", "", false
	}
	if identity.Rig == "" || identity.Name == "" {
		return "", "", false
	}
	// Exclude names that are reserved for other session types.
	// Mayor/deacon use hq- prefix in practice, but gt-<rig>-mayor/deacon
	// patterns should still be excluded defensively.
	switch identity.Name {
	case constants.RoleMayor, constants.RoleDeacon:
		return "", "", false
	}
	return identity.Rig, identity.Name, true
}

// findRigPolecatSessions returns all polecat sessions for a given rig.
// Finds sessions matching gt-<rig>-<name> pattern, excluding crew, witness,
// and refinery sessions.
func findRigPolecatSessions(rigName string) ([]string, error) { //nolint:unparam // error return kept for future use
	allSessions, err := listTmuxSessions()
	if err != nil {
		return nil, nil
	}

	prefix := session.PrefixFor(rigName) + "-"
	var sessions []string

	for _, s := range allSessions {
		if !strings.HasPrefix(s, prefix) {
			continue
		}
		if _, _, ok := parsePolecatSessionName(s); ok {
			sessions = append(sessions, s)
		}
	}

	return sessions, nil
}
