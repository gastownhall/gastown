package cmd

import (
	"fmt"
	"sort"

	sessionpkg "github.com/jonbaldie/gastown/internal/session"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/spf13/cobra"
)

// cycleSession is the --session flag for cycle next/prev commands.
// When run via tmux key binding (run-shell), the session context may not be
// correct, so we pass the session name explicitly via #{session_name} expansion.
var cycleSession string

// cycleClient is the --client flag for cycle next/prev commands.
// When run from tmux run-shell, the spawned process has no client context,
// so switch-client without -c may target the wrong client. Pass the client
// TTY via #{client_tty} expansion to ensure the correct client is switched.
var cycleClient string

func init() {
	rootCmd.AddCommand(cycleCmd)
	cycleCmd.AddCommand(cycleNextCmd)
	cycleCmd.AddCommand(cyclePrevCmd)

	cycleNextCmd.Flags().StringVar(&cycleSession, "session", "", "Override current session (used by tmux binding)")
	cycleNextCmd.Flags().StringVar(&cycleClient, "client", "", "Target client TTY (used by tmux binding, e.g. #{client_tty})")
	cyclePrevCmd.Flags().StringVar(&cycleSession, "session", "", "Override current session (used by tmux binding)")
	cyclePrevCmd.Flags().StringVar(&cycleClient, "client", "", "Target client TTY (used by tmux binding, e.g. #{client_tty})")
}

var cycleCmd = &cobra.Command{
	Use:   "cycle",
	Short: "Cycle between sessions in the same group",
	Long: `Cycle between related tmux sessions based on the current session type.

Session groups:
- Town sessions: Mayor ↔ Deacon
- Crew sessions: All crew members in the same rig
- Rig ops sessions: Witness + Refinery + Polecats in the same rig

The appropriate cycling is detected automatically from the session name.

Examples:
  gt cycle next    # Switch to next session in group
  gt cycle prev    # Switch to previous session in group`,
}

var cycleNextCmd = &cobra.Command{
	Use:   "next",
	Short: "Switch to next session in group",
	Long: `Switch to the next session in the current group.

This command is typically invoked via the C-b n keybinding. It automatically
detects whether you're in a town-level session (Mayor/Deacon) or a crew session
and cycles within the appropriate group.

Examples:
  gt cycle next
  gt cycle next --session gt-gastown-witness  # Explicit session context`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cycleToSession(1, cycleSession, cycleClient)
	},
}

var cyclePrevCmd = &cobra.Command{
	Use:   "prev",
	Short: "Switch to previous session in group",
	Long: `Switch to the previous session in the current group.

This command is typically invoked via the C-b p keybinding. It automatically
detects whether you're in a town-level session (Mayor/Deacon) or a crew session
and cycles within the appropriate group.

Examples:
  gt cycle prev
  gt cycle prev --session gt-gastown-witness  # Explicit session context`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cycleToSession(-1, cycleSession, cycleClient)
	},
}

// cycleToSession dispatches to the appropriate cycling function based on session type.
// direction: 1 for next, -1 for previous
// sessionOverride: if non-empty, use this instead of detecting current session
// clientOverride: if non-empty, pass as -c flag to tmux switch-client
func cycleToSession(direction int, sessionOverride, clientOverride string) error {
	// Override the default tmux socket to match the calling tmux server.
	// PersistentPreRunE sets the socket to the town path hash (e.g., "gt-a1b2c3"), but
	// cycle is invoked from tmux run-shell which may be on a different socket
	// (e.g., "default"). Without this, switch-client silently fails because
	// the sessions aren't on the town-named socket.
	if socketName := tmux.SocketFromEnv(); socketName != "" {
		tmux.SetDefaultSocket(socketName)
	}

	session := sessionOverride
	if session == "" {
		var err error
		session, err = getCurrentTmuxSession()
		if err != nil {
			return nil // Not in tmux, nothing to do
		}
	}

	// Store client for use by cycleRigInfraSession
	cycleClientTarget = clientOverride

	// Check if it's a town-level session
	townLevelSessions := getTownLevelSessions()
	if townLevelSessions != nil {
		for _, townSession := range townLevelSessions {
			if session == townSession {
				return cycleTownSession(direction, session)
			}
		}
	}

	// Check if it's a crew session (format: <prefix>-crew-<name>)
	if identity, err := sessionpkg.ParseSessionName(session); err == nil && identity.Role == sessionpkg.RoleCrew {
		return cycleCrewSession(direction, session)
	}

	// Check if it's a rig ops session (witness, refinery, or polecat).
	// These all share one cycle group per rig.
	if rig := parseRigOpsSession(session); rig != "" {
		return cycleRigOpsSession(direction, session, rig)
	}

	// Unknown session type - do nothing
	return nil
}

// parseRigOpsSession extracts the rig name if this is a witness, refinery, or
// polecat session. Returns empty string if not a rig ops session.
func parseRigOpsSession(sess string) string {
	identity, err := sessionpkg.ParseSessionName(sess)
	if err != nil {
		return ""
	}
	switch identity.Role {
	case sessionpkg.RoleWitness, sessionpkg.RoleRefinery, sessionpkg.RolePolecat:
		return identity.Rig
	}
	return ""
}

// cycleClientTarget holds the client TTY to pass to switch-client -c.
// Set by cycleToSession from the --client flag. When empty, switch-client
// runs without -c (legacy behavior for backward compatibility).
var cycleClientTarget string

// resolveCurrentSession returns the current tmux session, using override if provided.
func resolveCurrentSession(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	return getCurrentTmuxSession()
}

// cycleInGroup cycles between sessions in a sorted group.
// direction: 1 for next, -1 for previous.
// currentSession: the current tmux session name.
// sessions: candidate sessions in the group (will be sorted).
// Returns nil if there's nothing to switch to.
func cycleInGroup(direction int, currentSession string, sessions []string) error {
	if len(sessions) == 0 {
		return nil
	}

	sort.Strings(sessions)

	currentIdx := -1
	for i, s := range sessions {
		if s == currentSession {
			currentIdx = i
			break
		}
	}

	if currentIdx == -1 {
		return nil // Current session not in list
	}

	targetIdx := (currentIdx + direction + len(sessions)) % len(sessions)
	if targetIdx == currentIdx {
		return nil // Only one session
	}

	args := []string{"switch-client"}
	if cycleClientTarget != "" {
		args = append(args, "-c", cycleClientTarget)
	}
	args = append(args, "-t", sessions[targetIdx])
	return tmux.BuildCommand(args...).Run()
}

// cycleRigOpsSession cycles between witness, refinery, and polecat sessions for a rig.
func cycleRigOpsSession(direction int, currentSession, rig string) error {
	allSessions, err := listTmuxSessions()
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}

	var sessions []string
	for _, s := range allSessions {
		if parseRigOpsSession(s) == rig {
			sessions = append(sessions, s)
		}
	}

	return cycleInGroup(direction, currentSession, sessions)
}

// listTmuxSessions returns all tmux session names.
func listTmuxSessions() ([]string, error) {
	out, err := tmux.BuildCommand("list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return nil, err
	}
	return splitLines(string(out)), nil
}
