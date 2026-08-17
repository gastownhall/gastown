package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/sling"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
)

// convoyScheduleOpts holds options for convoy schedule operations.
type convoyScheduleOpts struct {
	Formula     string
	HookRawBead bool
	Force       bool
	DryRun      bool
	NoBoot      bool
}

// runConvoyScheduleByID schedules all open tracked issues of a convoy.
func runConvoyScheduleByID(convoyID string, opts convoyScheduleOpts) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	if err := verifyBeadExists(convoyID); err != nil {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	townBeads := filepath.Join(townRoot, ".beads")
	tracked, err := getTrackedIssues(townBeads, convoyID)
	if err != nil {
		return fmt.Errorf("getting tracked issues: %w", err)
	}

	if len(tracked) == 0 {
		fmt.Printf("Convoy %s has no tracked issues.\n", convoyID)
		return nil
	}

	type scheduleCandidate struct {
		ID      string
		Title   string
		RigName string
	}
	var candidates []scheduleCandidate
	skippedClosed := 0
	skippedAssigned := 0
	skippedScheduled := 0
	skippedNoRig := 0

	// Batch-check scheduling status for all tracked issues (single DB query).
	var beadIDs []string
	for _, t := range tracked {
		beadIDs = append(beadIDs, t.ID)
	}
	scheduledSet := areScheduled(beadIDs)

	for _, t := range tracked {
		if t.Status == "closed" || t.Status == "tombstone" {
			skippedClosed++
			continue
		}

		if t.Assignee != "" && !opts.Force {
			skippedAssigned++
			continue
		}

		if scheduledSet[t.ID] {
			skippedScheduled++
			continue
		}

		rigName := resolveRigForBead(townRoot, t.ID)
		if rigName == "" {
			skippedNoRig++
			prefix := beads.ExtractPrefix(t.ID)
			fmt.Printf("  %s %s: cannot resolve rig from prefix %q (town-root or unknown)\n",
				style.Dim.Render("○"), t.ID, prefix)
			continue
		}

		candidates = append(candidates, scheduleCandidate{ID: t.ID, Title: t.Title, RigName: rigName})
	}

	if len(candidates) == 0 {
		fmt.Printf("No issues to schedule from convoy %s", convoyID)
		if skippedClosed > 0 || skippedAssigned > 0 || skippedScheduled > 0 || skippedNoRig > 0 {
			fmt.Printf(" (%d closed, %d assigned, %d already scheduled, %d no rig)",
				skippedClosed, skippedAssigned, skippedScheduled, skippedNoRig)
		}
		fmt.Println()
		return nil
	}

	formula := opts.Formula

	if opts.DryRun {
		fmt.Printf("%s Would schedule %d issue(s) from convoy %s:\n",
			style.Bold.Render("DRY-RUN"), len(candidates), convoyID)
		if formula != "" {
			fmt.Printf("  Formula: %s\n", formula)
		} else {
			fmt.Printf("  Hook raw beads (no formula)\n")
		}
		for _, c := range candidates {
			fmt.Printf("  Would schedule: %s -> %s (%s)\n", c.ID, c.RigName, c.Title)
		}
		if skippedClosed > 0 || skippedAssigned > 0 || skippedScheduled > 0 || skippedNoRig > 0 {
			fmt.Printf("\nSkipped: %d closed, %d assigned, %d already scheduled, %d no rig\n",
				skippedClosed, skippedAssigned, skippedScheduled, skippedNoRig)
		}
		return nil
	}

	fmt.Printf("%s Scheduling %d issue(s) from convoy %s...\n",
		style.Bold.Render("📋"), len(candidates), convoyID)

	successCount := 0
	for _, c := range candidates {
		err := scheduleBead(c.ID, c.RigName, ScheduleOptions{
			Formula:     formula,
			NoConvoy:    true, // Already tracked by this convoy
			Force:       opts.Force,
			HookRawBead: opts.HookRawBead,
		})
		if err != nil {
			fmt.Printf("  %s %s: %v\n", style.Dim.Render("✗"), c.ID, err)
			continue
		}
		successCount++
	}

	fmt.Printf("\n%s Scheduled %d/%d issue(s) from convoy %s\n",
		style.Bold.Render("📊"), successCount, len(candidates), convoyID)
	if skippedClosed > 0 || skippedAssigned > 0 || skippedScheduled > 0 || skippedNoRig > 0 {
		fmt.Printf("  Skipped: %d closed, %d assigned, %d already scheduled, %d no rig\n",
			skippedClosed, skippedAssigned, skippedScheduled, skippedNoRig)
	}

	if successCount == 0 {
		return fmt.Errorf("all %d schedule attempts failed for convoy %s", len(candidates), convoyID)
	}
	return nil
}

// runConvoySlingByID immediately dispatches all open tracked issues of a convoy.
// Used when max_polecats=-1 (direct dispatch mode). Each tracked issue goes
// through the deep Slinging lifecycle. Reuses this convoy identity.
func runConvoySlingByID(convoyID string, opts convoyScheduleOpts) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	if err := verifyBeadExists(convoyID); err != nil {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	townBeads := filepath.Join(townRoot, ".beads")
	tracked, err := getTrackedIssues(townBeads, convoyID)
	if err != nil {
		return fmt.Errorf("getting tracked issues: %w", err)
	}

	if len(tracked) == 0 {
		fmt.Printf("Convoy %s has no tracked issues.\n", convoyID)
		return nil
	}

	type slingCandidate struct {
		ID      string
		Title   string
		RigName string
	}
	var candidates []slingCandidate
	skippedClosed := 0
	skippedAssigned := 0
	skippedNoRig := 0

	for _, t := range tracked {
		if t.Status == "closed" || t.Status == "tombstone" {
			skippedClosed++
			continue
		}
		if t.Assignee != "" && !opts.Force {
			skippedAssigned++
			continue
		}
		rigName := resolveRigForBead(townRoot, t.ID)
		if rigName == "" {
			skippedNoRig++
			prefix := beads.ExtractPrefix(t.ID)
			fmt.Printf("  %s %s: cannot resolve rig from prefix %q (town-root or unknown)\n",
				style.Dim.Render("○"), t.ID, prefix)
			continue
		}
		candidates = append(candidates, slingCandidate{ID: t.ID, Title: t.Title, RigName: rigName})
	}

	if len(candidates) == 0 {
		fmt.Printf("No issues to dispatch from convoy %s", convoyID)
		if skippedClosed > 0 || skippedAssigned > 0 || skippedNoRig > 0 {
			fmt.Printf(" (%d closed, %d assigned, %d no rig)",
				skippedClosed, skippedAssigned, skippedNoRig)
		}
		fmt.Println()
		return nil
	}

	formula := opts.Formula

	if opts.DryRun {
		fmt.Printf("%s Would dispatch %d issue(s) from convoy %s:\n",
			style.Bold.Render("DRY-RUN"), len(candidates), convoyID)
		for _, c := range candidates {
			fmt.Printf("  Would dispatch: %s -> %s (%s)\n", c.ID, c.RigName, c.Title)
		}
		if skippedClosed > 0 || skippedAssigned > 0 || skippedNoRig > 0 {
			fmt.Printf("\nSkipped: %d closed, %d assigned, %d no rig\n",
				skippedClosed, skippedAssigned, skippedNoRig)
		}
		return nil
	}

	fmt.Printf("%s Dispatching %d issue(s) from convoy %s...\n",
		style.Bold.Render("▶"), len(candidates), convoyID)

	successCount := 0
	successfulRigs := make(map[string]bool)
	for i, c := range candidates {
		if slingMaxConcurrent > 0 && i >= slingMaxConcurrent {
			fmt.Printf("  %s Reached --max-concurrent spawn batch size (%d), remaining will be scheduled next cycle\n", style.Dim.Render("○"), slingMaxConcurrent)
			break
		}

		fmt.Printf("\n[%d/%d] Dispatching %s → %s...\n", i+1, len(candidates), c.ID, c.RigName)
		_, err := executeDeepSling(context.Background(), sling.Intent{
			BeadID:        c.ID,
			RigName:       c.RigName,
			Formula:       formula,
			Force:         opts.Force,
			HookRawBead:   opts.HookRawBead,
			Convoy:        convoyID,
			NoConvoy:      true, // Already tracked by this convoy
			NoBoot:        true, // coalesced after the loop
			CallerContext: "convoy-sling",
			TownRoot:      townRoot,
			BeadsDir:      filepath.Join(townRoot, ".beads"),
		})
		if err != nil {
			fmt.Printf("  %s %s: %v\n", style.Dim.Render("✗"), c.ID, err)
			continue
		}
		successCount++
		successfulRigs[c.RigName] = true

		// Brief delay between spawns to avoid Dolt contention
		if i < len(candidates)-1 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	// Wake rig agents for each unique rig that had successful dispatches
	if !opts.NoBoot {
		for rig := range successfulRigs {
			wakeRigAgents(rig)
		}
	}

	fmt.Printf("\n%s Dispatched %d/%d issue(s) from convoy %s\n",
		style.Bold.Render("📊"), successCount, len(candidates), convoyID)
	if skippedClosed > 0 || skippedAssigned > 0 || skippedNoRig > 0 {
		fmt.Printf("  Skipped: %d closed, %d assigned, %d no rig\n",
			skippedClosed, skippedAssigned, skippedNoRig)
	}

	if successCount == 0 {
		return fmt.Errorf("all %d dispatch attempts failed for convoy %s", len(candidates), convoyID)
	}
	return nil
}
