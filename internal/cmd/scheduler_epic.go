package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// epicScheduleOpts holds options for epic schedule operations.
type epicScheduleOpts struct {
	Formula     string
	HookRawBead bool
	Force       bool
	DryRun      bool
	NoBoot      bool
}

// runEpicScheduleByID schedules all open children of an epic.
func runEpicScheduleByID(epicID string, opts epicScheduleOpts) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	if err := verifyBeadExists(epicID); err != nil {
		return fmt.Errorf("epic '%s' not found", epicID)
	}

	children, err := getEpicChildren(epicID)
	if err != nil {
		return fmt.Errorf("listing children of %s: %w", epicID, err)
	}

	if len(children) == 0 {
		fmt.Printf("Epic %s has no child issues.\n", epicID)
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

	// Batch-check scheduling status for all children (single DB query).
	var childIDs []string
	for _, c := range children {
		childIDs = append(childIDs, c.ID)
	}
	scheduledSet := areScheduled(childIDs)

	for _, c := range children {
		if c.Status == "closed" || c.Status == "tombstone" {
			skippedClosed++
			continue
		}

		if c.Assignee != "" && !opts.Force {
			skippedAssigned++
			continue
		}

		if scheduledSet[c.ID] {
			skippedScheduled++
			continue
		}

		rigName := resolveRigForBead(townRoot, c.ID)
		if rigName == "" {
			skippedNoRig++
			prefix := beads.ExtractPrefix(c.ID)
			fmt.Printf("  %s %s: cannot resolve rig from prefix %q (town-root or unknown)\n",
				style.Dim.Render("○"), c.ID, prefix)
			continue
		}

		candidates = append(candidates, scheduleCandidate{ID: c.ID, Title: c.Title, RigName: rigName})
	}

	if len(candidates) == 0 {
		fmt.Printf("No children to schedule from epic %s", epicID)
		if skippedClosed > 0 || skippedAssigned > 0 || skippedScheduled > 0 || skippedNoRig > 0 {
			fmt.Printf(" (%d closed, %d assigned, %d already scheduled, %d no rig)",
				skippedClosed, skippedAssigned, skippedScheduled, skippedNoRig)
		}
		fmt.Println()
		return nil
	}

	formula := opts.Formula

	if opts.DryRun {
		fmt.Printf("%s Would schedule %d child(ren) from epic %s:\n",
			style.Bold.Render("DRY-RUN"), len(candidates), epicID)
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

	fmt.Printf("%s Scheduling %d child(ren) from epic %s...\n",
		style.Bold.Render("📋"), len(candidates), epicID)

	successCount := 0
	for _, c := range candidates {
		err := scheduleBead(c.ID, c.RigName, ScheduleOptions{
			Formula:     formula,
			Force:       opts.Force,
			HookRawBead: opts.HookRawBead,
			NoConvoy:    true, // Epic is the organizing structure
		})
		if err != nil {
			fmt.Printf("  %s %s: %v\n", style.Dim.Render("✗"), c.ID, err)
			continue
		}
		successCount++
	}

	fmt.Printf("\n%s Scheduled %d/%d child(ren) from epic %s\n",
		style.Bold.Render("📊"), successCount, len(candidates), epicID)
	if skippedClosed > 0 || skippedAssigned > 0 || skippedScheduled > 0 || skippedNoRig > 0 {
		fmt.Printf("  Skipped: %d closed, %d assigned, %d already scheduled, %d no rig\n",
			skippedClosed, skippedAssigned, skippedScheduled, skippedNoRig)
	}

	if successCount == 0 {
		return fmt.Errorf("all %d schedule attempts failed for epic %s", len(candidates), epicID)
	}
	return nil
}

// runEpicSlingByID immediately dispatches all open children of an epic.
// Used when max_polecats=-1 (direct dispatch mode). Each child gets its own
// polecat via executeSling(). Respects --max-concurrent throttling.
func runEpicSlingByID(epicID string, opts epicScheduleOpts) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	if err := verifyBeadExists(epicID); err != nil {
		return fmt.Errorf("epic '%s' not found", epicID)
	}

	children, err := getEpicChildren(epicID)
	if err != nil {
		return fmt.Errorf("listing children of %s: %w", epicID, err)
	}

	if len(children) == 0 {
		fmt.Printf("Epic %s has no child issues.\n", epicID)
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

	for _, c := range children {
		if c.Status == "closed" || c.Status == "tombstone" {
			skippedClosed++
			continue
		}
		if c.Assignee != "" && !opts.Force {
			skippedAssigned++
			continue
		}
		rigName := resolveRigForBead(townRoot, c.ID)
		if rigName == "" {
			skippedNoRig++
			prefix := beads.ExtractPrefix(c.ID)
			fmt.Printf("  %s %s: cannot resolve rig from prefix %q (town-root or unknown)\n",
				style.Dim.Render("○"), c.ID, prefix)
			continue
		}
		candidates = append(candidates, slingCandidate{ID: c.ID, Title: c.Title, RigName: rigName})
	}

	if len(candidates) == 0 {
		fmt.Printf("No children to dispatch from epic %s", epicID)
		if skippedClosed > 0 || skippedAssigned > 0 || skippedNoRig > 0 {
			fmt.Printf(" (%d closed, %d assigned, %d no rig)",
				skippedClosed, skippedAssigned, skippedNoRig)
		}
		fmt.Println()
		return nil
	}

	formula := opts.Formula

	if opts.DryRun {
		fmt.Printf("%s Would dispatch %d child(ren) from epic %s:\n",
			style.Bold.Render("DRY-RUN"), len(candidates), epicID)
		for _, c := range candidates {
			fmt.Printf("  Would dispatch: %s -> %s (%s)\n", c.ID, c.RigName, c.Title)
		}
		if skippedClosed > 0 || skippedAssigned > 0 || skippedNoRig > 0 {
			fmt.Printf("\nSkipped: %d closed, %d assigned, %d no rig\n",
				skippedClosed, skippedAssigned, skippedNoRig)
		}
		return nil
	}

	fmt.Printf("%s Dispatching %d child(ren) from epic %s...\n",
		style.Bold.Render("▶"), len(candidates), epicID)

	successCount := 0
	successfulRigs := make(map[string]bool)
	for i, c := range candidates {
		if slingMaxConcurrent > 0 && i >= slingMaxConcurrent {
			fmt.Printf("  %s Reached --max-concurrent spawn batch size (%d), remaining will be scheduled next cycle\n", style.Dim.Render("○"), slingMaxConcurrent)
			break
		}

		fmt.Printf("\n[%d/%d] Dispatching %s → %s...\n", i+1, len(candidates), c.ID, c.RigName)
		_, err := executeSling(SlingParams{
			BeadID:        c.ID,
			RigName:       c.RigName,
			FormulaName:   formula,
			Force:         opts.Force,
			HookRawBead:   opts.HookRawBead,
			NoConvoy:      true, // Epic is the organizing structure
			NoBoot:        opts.NoBoot,
			CallerContext: "epic-sling",
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

	fmt.Printf("\n%s Dispatched %d/%d child(ren) from epic %s\n",
		style.Bold.Render("📊"), successCount, len(candidates), epicID)
	if skippedClosed > 0 || skippedAssigned > 0 || skippedNoRig > 0 {
		fmt.Printf("  Skipped: %d closed, %d assigned, %d no rig\n",
			skippedClosed, skippedAssigned, skippedNoRig)
	}

	if successCount == 0 {
		return fmt.Errorf("all %d dispatch attempts failed for epic %s", len(candidates), epicID)
	}
	return nil
}

// epicChild holds info about a child issue of an epic.
type epicChild struct {
	ID       string
	Title    string
	Status   string
	Assignee string
	Labels   []string
}

// getEpicChildren returns child issues of an epic via dependency lookup.
// Prefers raw SQL (bdDepListRawIDs) which handles cross-database deps correctly.
// Falls back to bd dep list for older bd versions (see GH #2624, #2832).
func getEpicChildren(epicID string) ([]epicChild, error) {
	dir := resolveBeadDir(epicID)

	// bd sql queries the database discovered from cmd.Dir. When the epic lives
	// in a rig database (not HQ), we must resolve to the rig's directory so
	// bd sql queries the correct database. resolveBeadDir returns the town root
	// (for bd CLI routing), but bd sql doesn't use routes.jsonl.
	sqlDir := dir
	if prefix := beads.ExtractPrefix(epicID); prefix != "" {
		townRoot, err := workspace.FindFromCwd()
		if err == nil {
			if rigPath := beads.GetRigPathForPrefix(townRoot, prefix); rigPath != "" {
				sqlDir = rigPath
			}
		}
	}

	// Prefer raw SQL — handles cross-database deps. Falls back to bd dep list
	// if bd sql is not available (older bd versions).
	childIDs, err := bdDepListRawIDs(sqlDir, epicID, "down", "depends_on")
	if err != nil {
		// bd sql not supported — fall back to bd dep list.
		childIDs, err = bdDepListFallback(dir, epicID)
		if err != nil {
			return nil, fmt.Errorf("querying epic children for %s: %w", epicID, err)
		}
	}

	children := make([]epicChild, 0, len(childIDs))
	for _, id := range childIDs {
		info, err := getBeadInfo(id)
		if err != nil {
			children = append(children, epicChild{
				ID: id,
			})
			continue
		}
		children = append(children, epicChild{
			ID:       id,
			Title:    info.Title,
			Status:   info.Status,
			Assignee: info.Assignee,
			Labels:   info.Labels,
		})
	}

	return children, nil
}

// bdDepListFallback uses bd dep list to get child dependency IDs.
// This is the legacy path — it uses a SQL JOIN with the issues table which
// silently drops cross-database dependencies. Used as fallback when bd sql
// is not available.
func bdDepListFallback(dir, epicID string) ([]string, error) {
	stdout, err := BdCmd("dep", "list", epicID,
		"--direction=down", "--type=depends_on", "--json").
		AllowStale().
		Dir(dir).
		StripBeadsDir().
		Stderr(io.Discard).
		Output()
	if err != nil {
		if len(stdout) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("bd dep list %s: %w", epicID, err)
	}

	var deps []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(stdout, &deps); err != nil {
		return nil, fmt.Errorf("parsing dependency list: %w", err)
	}

	ids := make([]string, 0, len(deps))
	for _, dep := range deps {
		id := beads.ExtractIssueID(dep.ID)
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
