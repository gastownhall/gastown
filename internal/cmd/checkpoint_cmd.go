package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/checkpoint"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var checkpointCmd = &cobra.Command{
	Use:     "checkpoint",
	GroupID: GroupDiag,
	Short:   "Manage session checkpoints for crash recovery",
	Long: `Manage checkpoints for polecat session crash recovery.

Checkpoints capture the current work state so that if a session crashes,
the next session can resume from where it left off.

Checkpoint data includes:
- Current molecule and step
- Hooked bead
- Modified files list
- Git branch and last commit
- Timestamp

Checkpoints are stored in .polecat-checkpoint.json in the polecat directory.`,
}

var checkpointWriteCmd = &cobra.Command{
	Use:   "write",
	Short: "Write a checkpoint of current session state",
	Long: `Capture and write the current session state to a checkpoint file.

This is typically called:
- After closing a molecule step
- Periodically during long work sessions
- Before handoff to another session

The checkpoint captures git state, molecule progress, and hooked work.`,
	RunE: runCheckpointWrite,
}

var checkpointReadCmd = &cobra.Command{
	Use:   "read",
	Short: "Read and display the current checkpoint",
	Long:  `Read and display the checkpoint file if one exists.`,
	RunE:  runCheckpointRead,
}

var checkpointClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear the checkpoint file",
	Long:  `Remove the checkpoint file. Use after work is complete or checkpoint is no longer needed.`,
	RunE:  runCheckpointClear,
}

var (
	checkpointNotes    string
	checkpointMolecule string
	checkpointStep     string
)

func init() {
	checkpointCmd.AddCommand(checkpointWriteCmd)
	checkpointCmd.AddCommand(checkpointReadCmd)
	checkpointCmd.AddCommand(checkpointClearCmd)

	checkpointWriteCmd.Flags().StringVar(&checkpointNotes, "notes", "",
		"Add notes to the checkpoint")
	checkpointWriteCmd.Flags().StringVar(&checkpointMolecule, "molecule", "",
		"Override molecule ID (auto-detected if not specified)")
	checkpointWriteCmd.Flags().StringVar(&checkpointStep, "step", "",
		"Override step ID (auto-detected if not specified)")

	rootCmd.AddCommand(checkpointCmd)
}

func runCheckpointWrite(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// Detect role context
	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return fmt.Errorf("not in a Gas Town workspace")
	}

	roleInfo, err := GetRoleWithContext(cwd, townRoot)
	if err != nil {
		return fmt.Errorf("detecting role: %w", err)
	}

	// Only polecats and crew workers use checkpoints
	if roleInfo.Role != RolePolecat && roleInfo.Role != RoleCrew {
		fmt.Printf("%s Checkpoints only apply to polecats and crew workers\n",
			style.Dim.Render("○"))
		return nil
	}

	// Capture current state
	cp, err := checkpoint.Capture(cwd)
	if err != nil {
		return fmt.Errorf("capturing checkpoint: %w", err)
	}

	// Add notes if provided
	if checkpointNotes != "" {
		cp.WithNotes(checkpointNotes)
	}

	// Try to detect molecule context if not overridden
	if checkpointMolecule == "" || checkpointStep == "" {
		moleculeID, stepID, stepTitle := detectMoleculeContext(cwd, roleInfo)
		if checkpointMolecule == "" {
			checkpointMolecule = moleculeID
		}
		if checkpointStep == "" {
			checkpointStep = stepID
		}
		if stepTitle != "" {
			cp.WithMolecule(checkpointMolecule, checkpointStep, stepTitle)
		}
	}

	// Add molecule context
	if checkpointMolecule != "" {
		cp.WithMolecule(checkpointMolecule, checkpointStep, "")
	}

	// Detect hooked bead
	hookedBead := detectHookedBead(cwd, roleInfo)
	if hookedBead != "" {
		cp.WithHookedBead(hookedBead)
	}

	// Write checkpoint
	if err := checkpoint.Write(cwd, cp); err != nil {
		return fmt.Errorf("writing checkpoint: %w", err)
	}

	fmt.Printf("%s Checkpoint written\n", style.Bold.Render("✓"))
	fmt.Printf("  %s\n", cp.Summary())

	return nil
}

func runCheckpointRead(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	cp, err := checkpoint.Read(cwd)
	if err != nil {
		return fmt.Errorf("reading checkpoint: %w", err)
	}

	if cp == nil {
		fmt.Printf("%s No checkpoint exists\n", style.Dim.Render("○"))
		return nil
	}

	fmt.Printf("%s\n\n", style.Bold.Render("Checkpoint"))
	fmt.Printf("Timestamp: %s (%s ago)\n", cp.Timestamp.Format("2006-01-02 15:04:05"), cp.Age().Round(1))

	if cp.MoleculeID != "" {
		fmt.Printf("Molecule: %s\n", cp.MoleculeID)
	}
	if cp.CurrentStep != "" {
		fmt.Printf("Step: %s\n", cp.CurrentStep)
	}
	if cp.StepTitle != "" {
		fmt.Printf("Step Title: %s\n", cp.StepTitle)
	}
	if cp.HookedBead != "" {
		fmt.Printf("Hooked Bead: %s\n", cp.HookedBead)
	}
	if cp.Branch != "" {
		fmt.Printf("Branch: %s\n", cp.Branch)
	}
	if cp.LastCommit != "" {
		fmt.Printf("Last Commit: %s\n", cp.LastCommit[:min(12, len(cp.LastCommit))])
	}
	if len(cp.ModifiedFiles) > 0 {
		fmt.Printf("Modified Files: %d\n", len(cp.ModifiedFiles))
		for _, f := range cp.ModifiedFiles {
			fmt.Printf("  - %s\n", f)
		}
	}
	if cp.Notes != "" {
		fmt.Printf("Notes: %s\n", cp.Notes)
	}
	if cp.SessionID != "" {
		fmt.Printf("Session ID: %s\n", cp.SessionID)
	}

	return nil
}

func runCheckpointClear(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	if err := checkpoint.Remove(cwd); err != nil {
		return fmt.Errorf("removing checkpoint: %w", err)
	}

	fmt.Printf("%s Checkpoint cleared\n", style.Bold.Render("✓"))
	return nil
}

// detectMoleculeContext tries to detect the current molecule and step from beads.
func detectMoleculeContext(workDir string, ctx RoleInfo) (moleculeID, stepID, stepTitle string) {
	b := beads.New(workDir)

	// Get agent identity for query
	roleCtx := RoleContext{
		Role:    ctx.Role,
		Rig:     ctx.Rig,
		Polecat: ctx.Polecat,
	}
	assignee := getAgentIdentity(roleCtx)
	if assignee == "" {
		return "", "", ""
	}

	// Find in-progress issues for this agent
	issues, err := b.List(beads.ListOptions{
		Status:   "in_progress",
		Assignee: assignee,
		Priority: -1,
	})
	if err != nil || len(issues) == 0 {
		return "", "", ""
	}

	// Check for molecule metadata
	for _, issue := range issues {
		// Look for instantiated_from in description
		lines := strings.Split(issue.Description, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "instantiated_from:") {
				moleculeID = strings.TrimSpace(strings.TrimPrefix(line, "instantiated_from:"))
				stepID = issue.ID
				stepTitle = issue.Title
				return moleculeID, stepID, stepTitle
			}
		}
	}

	return "", "", ""
}

// detectHookedBead finds the currently hooked bead for the agent.
func detectHookedBead(workDir string, ctx RoleInfo) string {
	b := beads.New(workDir)

	// Get agent identity
	roleCtx := RoleContext{
		Role:    ctx.Role,
		Rig:     ctx.Rig,
		Polecat: ctx.Polecat,
	}
	assignee := getAgentIdentity(roleCtx)
	if assignee == "" {
		return ""
	}

	// Find hooked beads for this agent
	hookedBeads, err := b.List(beads.ListOptions{
		Status:   beads.StatusHooked,
		Assignee: assignee,
		Priority: -1,
	})
	if err != nil || len(hookedBeads) == 0 {
		return ""
	}

	return hookedBeads[0].ID
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
