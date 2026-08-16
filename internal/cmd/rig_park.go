package cmd

import (
	"fmt"

	"github.com/jonbaldie/gastown/internal/refinery"
	"github.com/jonbaldie/gastown/internal/session"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/jonbaldie/gastown/internal/wisp"
	"github.com/jonbaldie/gastown/internal/witness"
	"github.com/spf13/cobra"
)

// RigStatusKey is the wisp config key for rig operational status.
const RigStatusKey = "status"

// RigStatusParked is the value indicating a rig is parked.
const RigStatusParked = "parked"

var rigParkCmd = &cobra.Command{
	Use:   "park <rig>...",
	Short: "Park one or more rigs (stops agents, daemon won't auto-restart)",
	Long: `Park rigs to temporarily disable them.

Parking a rig:
  - Stops the witness if running
  - Stops the refinery if running
  - Sets status=parked in the wisp layer (local/ephemeral)
  - The daemon respects this status and won't auto-restart agents

This is a Level 1 (local/ephemeral) operation:
  - Only affects this town
  - Disappears on wisp cleanup
  - Use 'gt rig unpark' to resume normal operation

Examples:
  gt rig park gastown
  gt rig park beads gastown mayor`,
	Args: cobra.MinimumNArgs(1),
	RunE: runRigPark,
}

var rigUnparkCmd = &cobra.Command{
	Use:   "unpark <rig>...",
	Short: "Unpark one or more rigs (allow daemon to auto-restart agents)",
	Long: `Unpark rigs to resume normal operation.

Unparking a rig:
  - Removes the parked status from the wisp layer
  - Allows the daemon to auto-restart agents
  - Does NOT automatically start agents (use 'gt rig start' for that)

Examples:
  gt rig unpark gastown
  gt rig unpark beads gastown mayor`,
	Args: cobra.MinimumNArgs(1),
	RunE: runRigUnpark,
}

func init() {
	rigCmd.AddCommand(rigParkCmd)
	rigCmd.AddCommand(rigUnparkCmd)
}

func runRigPark(cmd *cobra.Command, args []string) error {
	var errs []error

	for _, rigName := range args {
		if err := parkOneRig(rigName); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", rigName, err))
		}
	}

	if len(errs) > 0 {
		for _, err := range errs {
			fmt.Printf("%s %v\n", style.Error.Render("✗"), err)
		}
		return fmt.Errorf("failed to park %d rig(s)", len(errs))
	}

	return nil
}

func parkOneRig(rigName string) error {
	// Get rig and town root
	townRoot, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	fmt.Printf("Parking rig %s...\n", style.Bold.Render(rigName))

	var stoppedAgents []string

	t := tmux.NewTmux()

	// Stop witness if running
	witnessSession := session.WitnessSessionName(session.PrefixFor(rigName))
	witnessRunning, _ := t.HasSession(witnessSession)
	if witnessRunning {
		fmt.Printf("  Stopping witness...\n")
		witMgr := witness.NewManager(r)
		if err := witMgr.Stop(); err != nil {
			fmt.Printf("  %s Failed to stop witness: %v\n", style.Warning.Render("!"), err)
		} else {
			stoppedAgents = append(stoppedAgents, "Witness stopped")
		}
	}

	// Stop refinery if running
	refinerySession := session.RefinerySessionName(session.PrefixFor(rigName))
	refineryRunning, _ := t.HasSession(refinerySession)
	if refineryRunning {
		fmt.Printf("  Stopping refinery...\n")
		refMgr := refinery.NewManager(r)
		if err := refMgr.Stop(); err != nil {
			fmt.Printf("  %s Failed to stop refinery: %v\n", style.Warning.Render("!"), err)
		} else {
			stoppedAgents = append(stoppedAgents, "Refinery stopped")
		}
	}

	// Set parked status in wisp layer
	wispCfg := wisp.NewConfig(townRoot, rigName)
	if err := wispCfg.Set(RigStatusKey, RigStatusParked); err != nil {
		return fmt.Errorf("setting parked status: %w", err)
	}

	// Output
	fmt.Printf("%s Rig %s parked (local only)\n", style.Success.Render("✓"), rigName)
	for _, msg := range stoppedAgents {
		fmt.Printf("  %s\n", msg)
	}
	fmt.Printf("  Daemon will not auto-restart\n")

	return nil
}

func runRigUnpark(cmd *cobra.Command, args []string) error {
	var errs []error

	for _, rigName := range args {
		if err := unparkOneRig(rigName); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", rigName, err))
		}
	}

	if len(errs) > 0 {
		for _, err := range errs {
			fmt.Printf("%s %v\n", style.Error.Render("✗"), err)
		}
		return fmt.Errorf("failed to unpark %d rig(s)", len(errs))
	}

	return nil
}

func unparkOneRig(rigName string) error {
	// Get rig and town root
	townRoot, _, err := getRig(rigName)
	if err != nil {
		return err
	}

	// Remove parked status from wisp layer
	wispCfg := wisp.NewConfig(townRoot, rigName)
	if err := wispCfg.Unset(RigStatusKey); err != nil {
		return fmt.Errorf("clearing parked status: %w", err)
	}

	fmt.Printf("%s Rig %s unparked\n", style.Success.Render("✓"), rigName)
	fmt.Printf("  Daemon can now auto-restart agents\n")
	fmt.Printf("  Use '%s' to start agents immediately\n", style.Dim.Render("gt rig start "+rigName))

	return nil
}

// IsRigParked checks if a rig is parked.
// Checks the wisp layer (ephemeral) first, then falls back to the rig
// identity bead's status:parked label (persistent). This ensures parked
// state survives wisp cleanup. (Fixes upstream #2079)
func IsRigParked(townRoot, rigName string) bool {
	// Check wisp layer first (fast, local)
	wispCfg := wisp.NewConfig(townRoot, rigName)
	if wispCfg.GetString(RigStatusKey) == RigStatusParked {
		return true
	}

	// Fall back to persistent bead label
	return hasRigBeadLabel(townRoot, rigName, "status:parked")
}
