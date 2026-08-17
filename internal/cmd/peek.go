package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jonbaldie/gastown/internal/session"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

// Peek command flags
var peekLines int

func init() {
	rootCmd.AddCommand(peekCmd)
	peekCmd.Flags().IntVarP(&peekLines, "lines", "n", 100, "Number of lines to capture")
}

var peekCmd = &cobra.Command{
	Use:     "peek <rig/polecat> [count]",
	GroupID: GroupComm,
	Short:   "View recent output from a polecat or crew session",
	Long: `Capture and display recent terminal output from an agent session.

This is the ergonomic alias for 'gt session capture'. Use it to check
what an agent is currently doing or has recently output.

The nudge/peek pair provides the canonical interface for agent sessions:
  gt nudge - send messages TO a session (reliable delivery)
  gt peek  - read output FROM a session (capture-pane wrapper)

Supports polecats, crew workers, and town-level agents:
  - Polecats: rig/name or rig/polecats/name (e.g., greenplace/furiosa)
  - Crew: rig/crew/name format (e.g., beads/crew/dave)
  - Rig roles: rig/witness, rig/refinery
  - Town-level: mayor, deacon, boot (or hq/mayor, hq/deacon, hq/boot)

Examples:
  gt peek greenplace/furiosa              # Polecat short form
  gt peek greenplace/polecats/furiosa     # Polecat long form (same session)
  gt peek greenplace/furiosa 50           # Polecat: last 50 lines
  gt peek beads/crew/dave                 # Crew: last 100 lines
  gt peek beads/crew/dave -n 200          # Crew: last 200 lines
  gt peek greenplace/witness              # Witness
  gt peek greenplace/refinery             # Refinery
  gt peek mayor                           # Mayor: last 100 lines
  gt peek deacon -n 50                    # Deacon: last 50 lines`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runPeek,
}

// peekTownAgentSessions maps town-level peek aliases to tmux session names.
// Keep these before the identity parser: "hq/mayor" would otherwise look like a polecat.
var peekTownAgentSessions = map[string]string{
	"mayor":     session.MayorSessionName(),
	"hq/mayor":  session.MayorSessionName(),
	"deacon":    session.DeaconSessionName(),
	"hq/deacon": session.DeaconSessionName(),
	"boot":      session.BootSessionName(),
	"hq/boot":   session.BootSessionName(),
}

// peekSessionName maps a peek address to the tmux session name that runPeek captures.
// Accept both short (rig/name) and long (rig/polecats/name) polecat forms.
func peekSessionName(address string) (string, error) {
	if sessionName, ok := peekTownAgentSessions[address]; ok {
		return sessionName, nil
	}

	// Prefer the domain identity parser so long polecat paths, crew paths,
	// witness, and refinery share one mapping with mail and status.
	if ident, err := session.ParseAddress(address); err == nil {
		if name := ident.SessionName(); name != "" {
			return name, nil
		}
	}

	// Fall back for cwd-inferred short names that have no slash.
	rigName, polecatName, err := parseAddress(address)
	if err != nil {
		return "", err
	}
	switch {
	case polecatName == "witness":
		return session.WitnessSessionName(session.PrefixFor(rigName)), nil
	case polecatName == "refinery":
		return session.RefinerySessionName(session.PrefixFor(rigName)), nil
	case strings.HasPrefix(polecatName, "crew/"):
		crewName := strings.TrimPrefix(polecatName, "crew/")
		return session.CrewSessionName(session.PrefixFor(rigName), crewName), nil
	case strings.HasPrefix(polecatName, "polecats/"):
		pcName := strings.TrimPrefix(polecatName, "polecats/")
		return session.PolecatSessionName(session.PrefixFor(rigName), pcName), nil
	default:
		return session.PolecatSessionName(session.PrefixFor(rigName), polecatName), nil
	}
}

func runPeek(cmd *cobra.Command, args []string) error {
	address := args[0]

	// Handle optional positional count argument
	lines := peekLines
	if len(args) > 1 {
		n, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("invalid line count: %s", args[1])
		}
		lines = n
	}

	sessionName, err := peekSessionName(address)
	if err != nil {
		if !strings.Contains(address, "/") {
			return fmt.Errorf("not in a rig directory. Use full address format: gt peek <rig>/<polecat>")
		}
		return err
	}

	if _, ok := peekTownAgentSessions[address]; ok {
		_, err := workspace.FindFromCwdOrError()
		if err != nil {
			return fmt.Errorf("not in a Gas Town workspace: %w", err)
		}
		t := tmux.NewTmux()
		output, err := t.CapturePane(sessionName, lines)
		if err != nil {
			return fmt.Errorf("capturing %s: %w", address, err)
		}
		fmt.Print(output)
		return nil
	}

	rigName, _, err := parseAddress(address)
	if err != nil {
		if !strings.Contains(address, "/") {
			return fmt.Errorf("not in a rig directory. Use full address format: gt peek <rig>/<polecat>")
		}
		return err
	}

	mgr, _, err := getSessionManager(rigName)
	if err != nil {
		if !strings.Contains(address, "/") {
			return fmt.Errorf("not in a rig directory. Use full address format: gt peek <rig>/<polecat>")
		}
		return err
	}

	output, err := mgr.CaptureSession(sessionName, lines)
	if err != nil {
		return fmt.Errorf("capturing output: %w", err)
	}

	fmt.Print(output)
	return nil
}
