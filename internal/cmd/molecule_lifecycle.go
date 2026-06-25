package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/workspace"
)

// runMoleculeBurn burns (destroys) the current molecule attachment.
func runMoleculeBurn(cmd *cobra.Command, args []string) (retErr error) {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// Find town root
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding workspace: %w", err)
	}
	if townRoot == "" {
		return fmt.Errorf("not in a Gas Town workspace")
	}

	// Determine target agent
	var target string
	if len(args) > 0 {
		target = args[0]
	} else {
		// Auto-detect using env-aware role detection
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
		target = buildAgentIdentity(roleCtx)
		if target == "" {
			return fmt.Errorf("cannot determine agent identity (role: %s)", roleCtx.Role)
		}
	}

	// Find beads directory
	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	b := beads.New(workDir)

	// Find agent's pinned bead (handoff bead)
	role := extractRoleFromIdentity(target)

	handoff, err := b.FindHandoffBead(role)
	if err != nil {
		return fmt.Errorf("finding handoff bead: %w", err)
	}
	if handoff == nil {
		return fmt.Errorf("no handoff bead found for %s (looked for %q with pinned status)", target, beads.HandoffBeadTitle(role))
	}

	// Check for attached molecule
	attachment := beads.ParseAttachmentFields(handoff)
	if attachment == nil || attachment.AttachedMolecule == "" {
		fmt.Printf("%s No molecule attached to %s - nothing to burn\n",
			style.Dim.Render("ℹ"), target)
		return nil
	}

	moleculeID := attachment.AttachedMolecule

	// Recursively close all descendant step issues before detaching
	// This prevents orphaned step issues from accumulating (gt-psj76.1)
	childrenClosed := closeDescendants(b, moleculeID)
	defer func() {
		ctx := context.Background()
		if cmd != nil {
			ctx = cmd.Context()
		}
		telemetry.RecordMolBurn(ctx, moleculeID, childrenClosed, retErr)
	}()

	// Detach the molecule with audit logging (this "burns" it by removing the attachment)
	_, err = b.DetachMoleculeWithAudit(handoff.ID, beads.DetachOptions{
		Operation: "burn",
		Agent:     target,
		Reason:    "molecule burned by agent",
	})
	if err != nil {
		return fmt.Errorf("detaching molecule: %w", err)
	}
	// Close the molecule root after detach so the audit sees original status.
	// Without this, the wisp root stays in "hooked" status indefinitely,
	// causing patrol molecule leaks (issue #1828).
	rootClosed := true
	if closeErr := b.ForceCloseWithReason("burned", moleculeID); closeErr != nil {
		style.PrintWarning("could not close molecule root %s: %v", moleculeID, closeErr)
		rootClosed = false
	}

	if moleculeJSON {
		result := map[string]interface{}{
			"burned":          moleculeID,
			"from":            target,
			"handoff_id":      handoff.ID,
			"children_closed": childrenClosed,
			"root_closed":     rootClosed,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	fmt.Printf("%s Burned molecule %s from %s\n",
		style.Bold.Render("🔥"), moleculeID, target)
	if childrenClosed > 0 {
		fmt.Printf("  Closed %d step issues\n", childrenClosed)
	}

	return nil
}

// runMoleculeSquash squashes the current molecule into a digest.
func runMoleculeSquash(cmd *cobra.Command, args []string) (retErr error) {
	// Parse jitter early so invalid flags fail fast, but defer the sleep
	// until after workspace/attachment validation so no-op invocations
	// (wrong directory, no attached molecule) don't wait unnecessarily.
	var jitterMax time.Duration
	if moleculeJitter != "" {
		var err error
		jitterMax, err = time.ParseDuration(moleculeJitter)
		if err != nil {
			return fmt.Errorf("invalid --jitter duration %q: %w", moleculeJitter, err)
		}
		if jitterMax < 0 {
			return fmt.Errorf("--jitter must be non-negative, got %v", jitterMax)
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// Find town root
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding workspace: %w", err)
	}
	if townRoot == "" {
		return fmt.Errorf("not in a Gas Town workspace")
	}

	// Determine target agent
	var target string
	if len(args) > 0 {
		target = args[0]
	} else {
		// Auto-detect using env-aware role detection
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
		target = buildAgentIdentity(roleCtx)
		if target == "" {
			return fmt.Errorf("cannot determine agent identity (role: %s)", roleCtx.Role)
		}
	}

	// Find beads directory
	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	b := beads.New(workDir)

	// Find agent's pinned bead (handoff bead)
	role := extractRoleFromIdentity(target)

	handoff, err := b.FindHandoffBead(role)
	if err != nil {
		return fmt.Errorf("finding handoff bead: %w", err)
	}
	if handoff == nil {
		return fmt.Errorf("no handoff bead found for %s (looked for %q with pinned status)", target, beads.HandoffBeadTitle(role))
	}

	// Check for attached molecule
	attachment := beads.ParseAttachmentFields(handoff)
	if attachment == nil || attachment.AttachedMolecule == "" {
		fmt.Printf("%s No molecule attached to %s - nothing to squash\n",
			style.Dim.Render("ℹ"), target)
		return nil
	}

	moleculeID := attachment.AttachedMolecule

	var doneSteps, totalSteps int
	defer func() {
		telemetry.RecordMolSquash(cmd.Context(), moleculeID, doneSteps, totalSteps, !moleculeNoDigest, retErr)
	}()

	// Apply jitter before acquiring any Dolt locks.
	// Multiple patrol agents (deacon, witness, refinery) squash concurrently at
	// cycle end, causing exclusive-lock contention. A random pre-sleep
	// desynchronizes them without changing semantics.
	if jitterMax > 0 {
		//nolint:gosec // weak RNG is fine for jitter
		sleep := time.Duration(rand.Int63n(int64(jitterMax)))
		fmt.Fprintf(os.Stderr, "jitter: sleeping %v before squash\n", sleep)
		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case <-time.After(sleep):
		}
	}

	// Recursively close all descendant step issues before squashing
	// This prevents orphaned step issues from accumulating (gt-psj76.1)
	childrenClosed := closeDescendants(b, moleculeID)

	// Skip digest creation if --no-digest flag is set (gt-t2bjt).
	// Patrol molecules (deacon, witness, refinery) run frequently and their
	// digests pollute the database with thousands of low-value beads.
	if !moleculeNoDigest {
		// Get progress info for the digest
		progress, _ := getMoleculeProgressInfo(b, moleculeID)

		// Create a digest issue
		digestTitle := fmt.Sprintf("Digest: %s", moleculeID)
		digestDesc := fmt.Sprintf(`Squashed molecule execution.

molecule: %s
agent: %s
squashed_at: %s
`, moleculeID, target, time.Now().UTC().Format(time.RFC3339))

		if moleculeSummary != "" {
			digestDesc += fmt.Sprintf("\n## Summary\n%s\n", moleculeSummary)
		}

		if progress != nil {
			doneSteps = progress.DoneSteps
			totalSteps = progress.TotalSteps
			digestDesc += fmt.Sprintf(`
## Execution Summary
- Steps: %d/%d completed
- Status: %s
`, progress.DoneSteps, progress.TotalSteps, func() string {
				if progress.Complete {
					return "complete"
				}
				return "partial"
			}())
		}

		// Create the digest bead (ephemeral to avoid git pollution)
		// Per-cycle digests are aggregated daily by 'gt patrol digest'
		digestIssue, err := b.Create(beads.CreateOptions{
			Title:       digestTitle,
			Description: digestDesc,
			Labels:      []string{"gt:task"},
			Priority:    4, // P4 - backlog priority for digests
			Actor:       target,
			Ephemeral:   true, // Don't export to JSONL - daily aggregation handles permanent record
		})
		if err != nil {
			return fmt.Errorf("creating digest: %w", err)
		}

		// Add the digest label (non-fatal: digest works without label)
		_ = b.Update(digestIssue.ID, beads.UpdateOptions{
			AddLabels: []string{"digest"},
		})

		// Close the digest immediately
		closedStatus := "closed"
		err = b.Update(digestIssue.ID, beads.UpdateOptions{
			Status: &closedStatus,
		})
		if err != nil {
			style.PrintWarning("Created digest but couldn't close it: %v", err)
		}
	}

	// Detach the molecule from the handoff bead with audit logging
	detachReason := "molecule squashed (no digest)"
	if !moleculeNoDigest {
		detachReason = "molecule squashed"
	}
	_, err = b.DetachMoleculeWithAudit(handoff.ID, beads.DetachOptions{
		Operation: "squash",
		Agent:     target,
		Reason:    detachReason,
	})
	if err != nil {
		return fmt.Errorf("detaching molecule: %w", err)
	}

	// Close the molecule root after detach so the audit sees original status.
	// Without this, the wisp root stays in "hooked" status indefinitely,
	// causing patrol molecule leaks (issue #1828).
	rootClosed := true
	if closeErr := b.ForceCloseWithReason("squashed", moleculeID); closeErr != nil {
		style.PrintWarning("could not close molecule root %s: %v", moleculeID, closeErr)
		rootClosed = false
	}

	if moleculeJSON {
		result := map[string]interface{}{
			"squashed":        moleculeID,
			"from":            target,
			"handoff_id":      handoff.ID,
			"children_closed": childrenClosed,
			"digest_skipped":  moleculeNoDigest,
			"root_closed":     rootClosed,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	if moleculeNoDigest {
		fmt.Printf("%s Squashed molecule %s (no digest)\n",
			style.Bold.Render("📦"), moleculeID)
	} else {
		fmt.Printf("%s Squashed molecule %s\n",
			style.Bold.Render("📦"), moleculeID)
	}
	if childrenClosed > 0 {
		fmt.Printf("  Closed %d step issues\n", childrenClosed)
	}

	return nil
}

// closeDescendants recursively closes all descendant issues of a parent.
// Returns the count of issues closed. Logs warnings on errors but doesn't fail.
func closeDescendants(b *beads.Beads, parentID string) int {
	count, err := closeDescendantsImpl(b, parentID, false)
	if err != nil {
		style.PrintWarning("closing descendants of %s: %v", parentID, err)
	}
	return count
}

// forceCloseDescendants is like closeDescendants but uses force-close,
// which succeeds even for beads in invalid states. Returns the count of
// issues closed and any error encountered. Callers should check the error
// to avoid closing a parent while children survive (gt-7lx3).
func forceCloseDescendants(b *beads.Beads, parentID string) (int, error) {
	return closeDescendantsImpl(b, parentID, true)
}

func closeDescendantsImpl(b *beads.Beads, parentID string, force bool) (int, error) {
	children, err := listChildrenAcrossTables(b, parentID)
	if err != nil {
		return 0, fmt.Errorf("listing children of %s: %w", parentID, err)
	}

	if len(children) == 0 {
		return 0, nil
	}

	// First, recursively close grandchildren
	totalClosed := 0
	var errs []error
	for _, child := range children {
		closed, childErr := closeDescendantsImpl(b, child.ID, force)
		totalClosed += closed
		if childErr != nil {
			errs = append(errs, childErr)
		}
	}

	// Then close direct children
	var idsToClose []string
	for _, child := range children {
		if child.Status != "closed" {
			idsToClose = append(idsToClose, child.ID)
		}
	}

	if len(idsToClose) > 0 {
		var closeErr error
		if force {
			closeErr = b.ForceCloseWithReason("burned: force-close descendants", idsToClose...)
		} else {
			closeErr = b.Close(idsToClose...)
		}
		if closeErr != nil {
			errs = append(errs, fmt.Errorf("closing children of %s: %w", parentID, closeErr))
		} else {
			totalClosed += len(idsToClose)
		}
	}

	if len(errs) > 0 {
		return totalClosed, errors.Join(errs...)
	}
	return totalClosed, nil
}
