package cmd

import (
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// workflowAttachmentForHookedBead returns parsed attachment metadata when present
// and infers workflow context for root-only molecule wisps that carry the formula
// name in their title rather than in attached_formula fields.
func workflowAttachmentForHookedBead(hookedBead *beads.Issue) *beads.AttachmentFields {
	if hookedBead == nil {
		return nil
	}

	if attachment := beads.ParseAttachmentFields(hookedBead); attachment != nil {
		return attachment
	}

	if hookedBead.Type != "molecule" {
		return nil
	}

	formulaName := strings.TrimSpace(hookedBead.Title)
	if formulaName == "" || !strings.HasPrefix(formulaName, "mol-") {
		return nil
	}

	return &beads.AttachmentFields{
		AttachedFormula: formulaName,
	}
}

