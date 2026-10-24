package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

func runMoleculeAttach(cmd *cobra.Command, args []string) error {
	var pinnedBeadID, moleculeID string

	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	// Pinned beads — handoff beads and agent beads alike — live in the town
	// database, but they carry rig prefixes, so prefix routing sends every
	// lookup to the rig database where they do not exist. ForAgentBead re-roots
	// at the town beads dir and disables routing; without it, attach fails with
	// "fetching pinned bead: issue not found" from any rig context.
	b := beads.New(workDir).ForAgentBead()

	if len(args) == 2 {
		// Explicit: gt mol attach <pinned-bead-id> <molecule-id>
		pinnedBeadID = args[0]
		moleculeID = args[1]
	} else {
		// Auto-detect: gt mol attach <molecule-id>
		// Find the agent's pinned handoff bead (same pattern as mol burn/squash)
		moleculeID = args[0]

		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting current directory: %w", err)
		}

		townRoot, err := workspace.FindFromCwd()
		if err != nil {
			return fmt.Errorf("finding workspace: %w", err)
		}
		if townRoot == "" {
			return fmt.Errorf("not in a Gas Town workspace")
		}

		roleInfo, err := GetRoleWithContext(cwd, townRoot)
		if err != nil {
			return fmt.Errorf("detecting role: %w", err)
		}
		roleCtx := RoleContext{
			Role:     roleInfo.Role,
			Rig:      roleInfo.Rig,
			Polecat:  roleInfo.Polecat,
			TownRoot: townRoot,
			WorkDir:  cwd,
		}
		target := buildAgentIdentity(roleCtx)
		if target == "" {
			return fmt.Errorf("cannot determine agent identity (role: %s)", roleCtx.Role)
		}

		role := extractRoleFromIdentity(target)

		// Create the handoff bead if this role doesn't have one yet. Attaching is
		// the point at which a role first needs its pin, and nothing else in the
		// codebase creates it — requiring it to pre-exist meant attach could never
		// succeed for a role that had not somehow acquired one already.
		handoff, err := b.GetOrCreateHandoffBead(role)
		if err != nil {
			return fmt.Errorf("finding handoff bead: %w", err)
		}
		pinnedBeadID = handoff.ID
	}

	// Attach the molecule
	issue, err := b.AttachMolecule(pinnedBeadID, moleculeID)
	if err != nil {
		return fmt.Errorf("attaching molecule: %w", err)
	}

	attachment := beads.ParseAttachmentFields(issue)
	fmt.Printf("%s Attached %s to %s\n", style.Bold.Render("✓"), moleculeID, pinnedBeadID)
	if attachment != nil && attachment.AttachedAt != "" {
		fmt.Printf("  attached_at: %s\n", attachment.AttachedAt)
	}

	return nil
}

func runMoleculeDetach(cmd *cobra.Command, args []string) error {
	pinnedBeadID := args[0]

	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	b := beads.New(workDir)

	// Check current attachment first
	attachment, err := b.GetAttachment(pinnedBeadID)
	if err != nil {
		return fmt.Errorf("checking attachment: %w", err)
	}

	if attachment == nil {
		fmt.Printf("%s No molecule attached to %s\n", style.Dim.Render("ℹ"), pinnedBeadID)
		return nil
	}

	previousMolecule := attachment.AttachedMolecule

	// Detach the molecule with audit logging
	_, err = b.DetachMoleculeWithAudit(pinnedBeadID, beads.DetachOptions{
		Operation: "detach",
		Agent:     detectCurrentAgent(),
	})
	if err != nil {
		return fmt.Errorf("detaching molecule: %w", err)
	}

	fmt.Printf("%s Detached %s from %s\n", style.Bold.Render("✓"), previousMolecule, pinnedBeadID)

	return nil
}

func runMoleculeAttachment(cmd *cobra.Command, args []string) error {
	pinnedBeadID := args[0]

	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	b := beads.New(workDir)

	// Get the issue
	issue, err := b.Show(pinnedBeadID)
	if err != nil {
		return fmt.Errorf("getting issue: %w", err)
	}

	attachment := beads.ParseAttachmentFields(issue)

	if moleculeJSON {
		type attachmentOutput struct {
			IssueID          string `json:"issue_id"`
			IssueTitle       string `json:"issue_title"`
			Status           string `json:"status"`
			AttachedMolecule string `json:"attached_molecule,omitempty"`
			AttachedAt       string `json:"attached_at,omitempty"`
		}
		out := attachmentOutput{
			IssueID:    issue.ID,
			IssueTitle: issue.Title,
			Status:     issue.Status,
		}
		if attachment != nil {
			out.AttachedMolecule = attachment.AttachedMolecule
			out.AttachedAt = attachment.AttachedAt
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	// Human-readable output
	fmt.Printf("\n%s: %s\n", style.Bold.Render(issue.ID), issue.Title)
	fmt.Printf("Status: %s\n", issue.Status)

	if attachment == nil || attachment.AttachedMolecule == "" {
		fmt.Printf("\n%s\n", style.Dim.Render("No molecule attached"))
	} else {
		fmt.Printf("\n%s\n", style.Bold.Render("Attached Molecule:"))
		fmt.Printf("  ID: %s\n", attachment.AttachedMolecule)
		if attachment.AttachedAt != "" {
			fmt.Printf("  Attached at: %s\n", attachment.AttachedAt)
		}
	}

	return nil
}

// detectCurrentAgent returns the current agent identity based on GT_ROLE or working directory.
// Returns empty string if identity cannot be determined.
func detectCurrentAgent() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return ""
	}

	roleInfo, err := GetRoleWithContext(cwd, townRoot)
	if err != nil {
		return ""
	}
	ctx := RoleContext{
		Role:     roleInfo.Role,
		Rig:      roleInfo.Rig,
		Polecat:  roleInfo.Polecat,
		TownRoot: townRoot,
		WorkDir:  cwd,
	}
	return buildAgentIdentity(ctx)
}
