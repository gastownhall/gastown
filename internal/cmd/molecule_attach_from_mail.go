package cmd

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/mail"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

// runMoleculeAttachFromMail handles the "gt mol attach-from-mail <mail-id>" command.
// It reads a mail message, extracts the molecule ID from the body, and attaches
// it to the current agent's hook (pinned bead).
func runMoleculeAttachFromMail(cmd *cobra.Command, args []string) error {
	mailID := args[0]

	// Get current working directory and town root
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return fmt.Errorf("not in a Gas Town workspace")
	}

	// Detect agent role and identity using env-aware detection
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
	agentIdentity := buildAgentIdentity(roleCtx)
	if agentIdentity == "" {
		return fmt.Errorf("cannot determine agent identity (role: %s)", roleCtx.Role)
	}

	// Get the agent's mailbox
	mailWorkDir, err := findMailWorkDir()
	if err != nil {
		return fmt.Errorf("finding mail workspace: %w", err)
	}

	router := mail.NewRouter(mailWorkDir)
	mailbox, err := router.GetMailbox(agentIdentity)
	if err != nil {
		return fmt.Errorf("getting mailbox: %w", err)
	}

	// Read the mail message
	msg, err := mailbox.Get(mailID)
	if err != nil {
		return fmt.Errorf("reading mail message: %w", err)
	}

	// Extract molecule ID from mail body
	moleculeID := extractMoleculeIDFromMail(msg.Body)
	if moleculeID == "" {
		return fmt.Errorf("no attached_molecule field found in mail body")
	}

	// Find local beads directory
	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	b := beads.New(workDir)

	// Find the agent's pinned bead (hook)
	pinnedBeads, err := b.List(beads.ListOptions{
		Status:   beads.StatusPinned,
		Assignee: agentIdentity,
		Priority: -1,
	})
	if err != nil {
		return fmt.Errorf("listing pinned beads: %w", err)
	}

	if len(pinnedBeads) == 0 {
		return fmt.Errorf("no pinned bead found for agent %s - create one first", agentIdentity)
	}

	// Use the first pinned bead as the hook
	hookBead := pinnedBeads[0]

	// Check if molecule exists
	_, err = b.Show(moleculeID)
	if err != nil {
		return fmt.Errorf("molecule %s not found: %w", moleculeID, err)
	}

	// Attach the molecule to the hook
	issue, err := b.AttachMolecule(hookBead.ID, moleculeID)
	if err != nil {
		return fmt.Errorf("attaching molecule: %w", err)
	}

	// Mark mail as read
	if err := mailbox.MarkRead(mailID); err != nil {
		// Non-fatal: log warning but don't fail
		style.PrintWarning("could not mark mail as read: %v", err)
	}

	// Output success
	attachment := beads.ParseAttachmentFields(issue)
	fmt.Printf("%s Attached molecule from mail\n", style.Bold.Render("✓"))
	fmt.Printf("  Mail: %s\n", mailID)
	fmt.Printf("  Hook: %s\n", hookBead.ID)
	fmt.Printf("  Molecule: %s\n", moleculeID)
	if attachment != nil && attachment.AttachedAt != "" {
		fmt.Printf("  Attached at: %s\n", attachment.AttachedAt)
	}
	fmt.Printf("\n%s Run 'gt hook' to see progress\n", style.Dim.Render("Hint:"))

	return nil
}

// extractMoleculeIDFromMail extracts a molecule ID from a mail message body.
// It looks for patterns like:
//   - attached_molecule: <id>
//   - molecule_id: <id>
//   - molecule: <id>
//
// The ID is expected to be on the same line after the colon.
func extractMoleculeIDFromMail(body string) string {
	// Try various patterns for molecule ID in mail body (case-insensitive)
	patterns := []string{
		`(?i)attached_molecule:\s*(\S+)`,
		`(?i)molecule_id:\s*(\S+)`,
		`(?i)molecule:\s*(\S+)`,
		`(?i)mol:\s*(\S+)`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(body)
		if len(matches) >= 2 {
			return strings.TrimSpace(matches[1])
		}
	}

	return ""
}
