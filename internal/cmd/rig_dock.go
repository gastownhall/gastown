package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/wisp"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/workspace"
)

// RigDockedLabel is the label set on rig identity beads when docked.
const RigDockedLabel = "status:docked"

var rigDockCmd = &cobra.Command{
	Use:   "dock <rig>",
	Short: "Dock a rig (global, persistent shutdown)",
	Long: `Dock a rig to persistently disable it across all clones.

Docking a rig:
  - Stops the witness if running
  - Stops the refinery if running
  - Stops all polecat sessions if running
  - Sets status:docked label on the rig identity bead
  - Syncs via git so all clones see the docked status

This is a Level 2 (global/persistent) operation:
  - Affects all clones of this rig (via git sync)
  - Persists until explicitly undocked
  - The daemon respects this status and won't auto-restart agents

Use 'gt rig undock' to resume normal operation.

Examples:
  gt rig dock gastown
  gt rig dock beads`,
	Args: cobra.ExactArgs(1),
	RunE: runRigDock,
}

var rigUndockCmd = &cobra.Command{
	Use:   "undock <rig>",
	Short: "Undock a rig (remove global docked status)",
	Long: `Undock a rig to remove the persistent docked status.

Undocking a rig:
  - Removes the status:docked label from the rig identity bead
  - Syncs via git so all clones see the undocked status
  - Allows the daemon to auto-restart agents
  - Does NOT automatically start agents (use 'gt rig start' for that)

Examples:
  gt rig undock gastown
  gt rig undock beads`,
	Args: cobra.ExactArgs(1),
	RunE: runRigUndock,
}

func init() {
	rigCmd.AddCommand(rigDockCmd)
	rigCmd.AddCommand(rigUndockCmd)
}

func runRigDock(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	// Check we're on main branch - docking on other branches won't persist
	branchCmd := exec.Command("git", "branch", "--show-current")
	branchOutput, err := branchCmd.Output()
	if err == nil {
		currentBranch := strings.TrimSpace(string(branchOutput))
		if currentBranch != "main" && currentBranch != "master" {
			return fmt.Errorf("cannot dock: must be on main branch (currently on %s)\n"+
				"Docking on other branches won't persist. Run: git checkout main", currentBranch)
		}
	}

	// Get rig
	_, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	// Get rig prefix for bead ID
	prefix := "gt" // default
	if r.Config != nil && r.Config.Prefix != "" {
		prefix = r.Config.Prefix
	}

	// Find or create the rig identity bead (idempotent; handles duplicates
	// and Dolt query hiccups gracefully — gt-d8681).
	bd := beads.New(r.BeadsPath())
	rigBead, err := bd.EnsureRigBead(rigName, &beads.RigFields{
		Repo:   r.GitURL,
		Prefix: prefix,
		State:  beads.RigStateActive,
	})
	if err != nil {
		return fmt.Errorf("ensuring rig identity bead: %w", err)
	}

	// Check if already docked
	for _, label := range rigBead.Labels {
		if label == RigDockedLabel {
			fmt.Printf("%s Rig %s is already docked\n", style.Dim.Render("•"), rigName)
			return nil
		}
	}

	fmt.Printf("Docking rig %s...\n", style.Bold.Render(rigName))

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

	// Stop polecat sessions if any
	polecatMgr := polecat.NewSessionManager(t, r)
	polecatInfos, err := polecatMgr.List()
	if err == nil && len(polecatInfos) > 0 {
		fmt.Printf("  Stopping %d polecat session(s)...\n", len(polecatInfos))
		if err := polecatMgr.StopAll(false); err != nil {
			fmt.Printf("  %s Failed to stop polecat sessions: %v\n", style.Warning.Render("!"), err)
		} else {
			stoppedAgents = append(stoppedAgents, fmt.Sprintf("%d polecat session(s) stopped", len(polecatInfos)))
		}
	}

	// Set docked label on rig identity bead
	if err := bd.Update(rigBead.ID, beads.UpdateOptions{
		AddLabels: []string{RigDockedLabel},
	}); err != nil {
		return fmt.Errorf("setting docked label: %w", err)
	}

	// Write docked status to wisp layer so IsRigParkedOrDocked can detect it
	// even when Dolt is unreachable (e.g. during gt up startup race).
	townRoot, twErr := workspace.FindFromCwdOrError()
	if twErr == nil {
		wispCfg := wisp.NewConfig(townRoot, rigName)
		if err := wispCfg.Set("status", "docked"); err != nil {
			fmt.Printf("  %s Could not write wisp docked status: %v\n", style.Warning.Render("!"), err)
		}
	}

	// Remove rig from daemon.json patrol config so daemon stops spawning
	// witness/refinery sessions for this rig on every heartbeat cycle.
	if twErr == nil {
		if err := config.RemoveRigFromDaemonPatrols(townRoot, rigName); err != nil {
			fmt.Printf("  %s Could not update daemon.json patrols: %v\n", style.Warning.Render("!"), err)
		}
	}

	// Output
	fmt.Printf("%s Rig %s docked (global)\n", style.Success.Render("✓"), rigName)
	fmt.Printf("  Label added: %s\n", RigDockedLabel)
	for _, msg := range stoppedAgents {
		fmt.Printf("  %s\n", msg)
	}
	fmt.Printf("  Beads changes persisted via Dolt\n")

	return nil
}

func runRigUndock(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	// Check we're on main branch - undocking on other branches won't persist
	branchCmd := exec.Command("git", "branch", "--show-current")
	branchOutput, err := branchCmd.Output()
	if err == nil {
		currentBranch := strings.TrimSpace(string(branchOutput))
		if currentBranch != "main" && currentBranch != "master" {
			return fmt.Errorf("cannot undock: must be on main branch (currently on %s)\n"+
				"Undocking on other branches won't persist. Run: git checkout main", currentBranch)
		}
	}

	// Get rig and town root
	_, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	// Get rig prefix for bead ID
	prefix := "gt" // default
	if r.Config != nil && r.Config.Prefix != "" {
		prefix = r.Config.Prefix
	}

	// Find the rig identity bead
	rigBeadID := beads.RigBeadIDWithPrefix(prefix, rigName)
	bd := beads.New(r.BeadsPath())

	// Check if rig bead exists, create if not
	rigBead, err := bd.Show(rigBeadID)
	if err != nil {
		// Rig identity bead doesn't exist (legacy rig) - can't be docked
		fmt.Printf("%s Rig %s has no identity bead and is not docked\n", style.Dim.Render("•"), rigName)
		return nil
	}

	// Check if actually docked
	isDocked := false
	for _, label := range rigBead.Labels {
		if label == RigDockedLabel {
			isDocked = true
			break
		}
	}
	if !isDocked {
		fmt.Printf("%s Rig %s is not docked\n", style.Dim.Render("•"), rigName)
		return nil
	}

	// Remove docked label from rig identity bead
	if err := bd.Update(rigBeadID, beads.UpdateOptions{
		RemoveLabels: []string{RigDockedLabel},
	}); err != nil {
		return fmt.Errorf("removing docked label: %w", err)
	}

	// Clear docked status from wisp layer (mirrors gt rig dock writing it).
	townRoot, twErr := workspace.FindFromCwdOrError()
	if twErr == nil {
		wispCfg := wisp.NewConfig(townRoot, rigName)
		_ = wispCfg.Unset("status")
	}

	// Re-add rig to daemon.json patrol config so daemon resumes spawning
	// witness/refinery sessions for this rig.
	if twErr == nil {
		if err := config.AddRigToDaemonPatrols(townRoot, rigName); err != nil {
			fmt.Printf("  %s Could not update daemon.json patrols: %v\n", style.Warning.Render("!"), err)
		}
	}

	fmt.Printf("%s Rig %s undocked\n", style.Success.Render("✓"), rigName)
	fmt.Printf("  Label removed: %s\n", RigDockedLabel)
	fmt.Printf("  Daemon can now auto-restart agents\n")
	fmt.Printf("  Use '%s' to start agents immediately\n", style.Dim.Render("gt rig start "+rigName))

	return nil
}

// IsRigDocked checks if a rig is docked by checking for the status:docked label
// on the rig identity bead. This function is exported for use by the daemon.
func IsRigDocked(townRoot, rigName, prefix string) bool {
	// Construct the rig beads path
	rigPath := filepath.Join(townRoot, rigName)
	beadsPath := filepath.Join(rigPath, "mayor", "rig")
	if _, err := os.Stat(beadsPath); err != nil {
		beadsPath = rigPath
	}

	bd := beads.New(beadsPath)
	rigBeadID := beads.RigBeadIDWithPrefix(prefix, rigName)

	rigBead, err := bd.Show(rigBeadID)
	if err != nil {
		return false
	}

	for _, label := range rigBead.Labels {
		if label == RigDockedLabel {
			return true
		}
	}
	return false
}
