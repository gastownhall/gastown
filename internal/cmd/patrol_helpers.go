package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/cli"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/style"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// PatrolConfig holds role-specific patrol configuration.
type PatrolConfig struct {
	RoleName      string       // "deacon", "witness", "refinery"
	PatrolMolName string       // "mol-deacon-patrol", etc.
	BeadsDir      string       // where to look for beads
	Assignee      string       // agent identity for pinning
	HeaderEmoji   string       // display emoji
	HeaderTitle   string       // "Patrol Status", etc.
	WorkLoopSteps []string     // role-specific instructions
	ExtraVars     []string     // additional --var key=value args for wisp creation
	Beads         *beads.Beads // optional injected beads instance (for test isolation)
}

// maxStalePurgePerRun caps the number of stale patrol beads cleaned up in a
// single findActivePatrol call. Without a cap, N accumulated orphans produce
// N×K sequential Dolt queries (K = closeDescendants depth), overwhelming the
// server when multiple patrol agents call gt patrol report concurrently (gt-18dzn6p).
// Remaining stale beads are cleaned by burnPreviousPatrolWisps at cycle end.
const maxStalePurgePerRun = 5

// findActivePatrol finds an active patrol molecule for the role.
// Returns the patrol ID, display line, and whether one was found.
// Returns an error if discovery fails (e.g. transient bd failure),
// so callers can distinguish "no patrol" from "discovery failed"
// and avoid auto-spawning duplicates.
//
// Patrol molecules are intentionally hooked to the agent (hooked status).
// This function looks up hooked patrols and distinguishes active ones
// (with open/in_progress children) from stale ones (all children closed,
// e.g. after a squash that didn't close the root). Stale patrols are
// cleaned up incrementally (up to maxStalePurgePerRun per call); any
// remaining stale beads are cleaned by burnPreviousPatrolWisps at cycle end.
func findActivePatrol(cfg PatrolConfig) (patrolID, patrolLine string, found bool, err error) {
	b := cfg.Beads
	if b == nil {
		b = beads.New(cfg.BeadsDir)
	}

	// Find hooked patrol beads for this agent
	hookedBeads, listErr := b.List(beads.ListOptions{
		Status:   beads.StatusHooked,
		Assignee: cfg.Assignee,
		Priority: -1,
	})
	if listErr != nil {
		return "", "", false, fmt.Errorf("listing hooked beads: %w", listErr)
	}

	// Identify active patrol and collect stale ones for cleanup.
	// Stop scanning as soon as the active patrol is found to avoid N+1
	// checkHasOpenChildren queries when many accumulated orphans are present.
	// Stale cleanup is capped at maxStalePurgePerRun to limit write pressure.
	var activeBead *beads.Issue
	var staleIDs []string
	var skipped int // tracks patrols skipped due to child-listing errors

	for _, bead := range hookedBeads {
		if !strings.HasPrefix(bead.Title, cfg.PatrolMolName) {
			continue
		}

		hasOpen, err := checkHasOpenChildren(b, bead.ID)
		if err != nil {
			// Transient error — skip this bead entirely to avoid
			// destructive cleanup of a potentially active patrol.
			style.PrintWarning("could not check children for %s: %v", bead.ID, err)
			skipped++
			continue
		}

		if !hasOpen {
			// Stale patrol (no open children) — schedule for cleanup up to cap.
			// Excess stale beads are deferred to burnPreviousPatrolWisps.
			if len(staleIDs) < maxStalePurgePerRun {
				staleIDs = append(staleIDs, bead.ID)
			}
		} else if activeBead == nil {
			// Active patrol found — stop scanning to prevent N+1 queries.
			// Any unvisited stale beads will be cleaned by burnPreviousPatrolWisps
			// when the patrol cycle ends and autoSpawnPatrol is called.
			activeBead = bead
			break
		}
	}

	// Clean up stale patrols (capped at maxStalePurgePerRun)
	for _, id := range staleIDs {
		closeDescendants(b, id)
		if err := b.ForceCloseWithReason("stale patrol cleanup", id); err != nil {
			style.PrintWarning("could not close stale patrol %s: %v", id, err)
		}
	}

	if activeBead != nil {
		return activeBead.ID, formatBeadLine(activeBead), true, nil
	}

	// If we found matching patrols but skipped them all due to errors,
	// return an error so the caller doesn't auto-spawn a duplicate.
	if skipped > 0 {
		return "", "", false, fmt.Errorf("discovery incomplete: %d patrol(s) skipped due to child-listing errors", skipped)
	}
	return "", "", false, nil
}

// checkHasOpenChildren returns true if the given parent has any children
// that are not in closed status (i.e., open or in_progress).
// Returns an error if the child listing fails, so the caller can avoid
// destructive cleanup on transient failures.
//
// A parent with zero children is treated as "has open children" (returns true)
// to protect against a race where a freshly created wisp hasn't had its step
// children materialized yet. This prevents findActivePatrol from closing a
// just-created patrol during the window between root creation and step population.
func checkHasOpenChildren(b *beads.Beads, parentID string) (bool, error) {
	children, err := b.List(beads.ListOptions{
		Parent:   parentID,
		Status:   "all",
		Priority: -1,
	})
	if err != nil {
		return false, err
	}
	// Zero children means the wisp may still be materializing steps —
	// treat as active to avoid destroying a just-created patrol.
	if len(children) == 0 {
		return true, nil
	}
	for _, child := range children {
		if child.Status != "closed" {
			return true, nil
		}
	}
	return false, nil
}

// formatBeadLine formats a bead issue into a display line similar to bd list output.
func formatBeadLine(issue *beads.Issue) string {
	return fmt.Sprintf("%s  %s [%s]", issue.ID, issue.Title, issue.Status)
}

// burnPreviousPatrolWisps finds and burns all existing patrol wisps for a role.
// This prevents orphaned root wisp accumulation when a new patrol cycle starts
// without the previous one being properly closed (gt-92jh).
// Errors are logged as warnings but don't block new patrol creation.
func burnPreviousPatrolWisps(cfg PatrolConfig) {
	b := cfg.Beads
	if b == nil {
		b = beads.New(cfg.BeadsDir)
	}

	// Find all hooked patrol beads for this agent
	hookedBeads, err := b.List(beads.ListOptions{
		Status:   beads.StatusHooked,
		Assignee: cfg.Assignee,
		Priority: -1,
	})
	if err != nil {
		style.PrintWarning("burn: could not list hooked beads: %v", err)
		return
	}

	var burned int
	for _, bead := range hookedBeads {
		if !strings.HasPrefix(bead.Title, cfg.PatrolMolName) {
			continue
		}

		// Close all descendant wisps, then the root
		closeDescendants(b, bead.ID)
		if err := b.ForceCloseWithReason("burned: replaced by new patrol cycle", bead.ID); err != nil {
			style.PrintWarning("burn: could not close patrol %s: %v", bead.ID, err)
			continue
		}
		burned++
	}

	if burned > 0 {
		fmt.Printf("%s Burned %d previous patrol wisp(s)\n", style.Dim.Render("🔥"), burned)
	}
}

// autoSpawnPatrol creates and pins a new patrol wisp.
// Before creating, it burns any existing patrol wisps for this role to prevent
// orphaned root wisp accumulation (gt-92jh). This makes the function
// self-cleaning regardless of the caller.
// Returns the patrol ID or an error.
func autoSpawnPatrol(cfg PatrolConfig) (string, error) {
	if stop, err := refineryPatrolSafetyStop(cfg); err != nil {
		return "", err
	} else if stop != nil {
		return "", refinery.NewSafetyStoppedError(stop)
	}

	// Resolve the beads directory following redirects.
	// This ensures bd targets the correct database (e.g., rig database
	// instead of HQ) regardless of inherited BEADS_DIR. See gt-ctir.
	resolvedBeadsDir := beads.ResolveBeadsDir(cfg.BeadsDir)

	// Burn any existing patrol wisps for this role before creating a new one.
	// Without this, each patrol cycle leaks a root wisp into the DB, producing
	// ~500-700 orphans/day across all patrol formulas (gt-92jh).
	burnPreviousPatrolWisps(cfg)

	// Find the proto ID for the patrol molecule
	cmdCatalog := exec.Command("gt", "formula", "list")
	cmdCatalog.Dir = cfg.BeadsDir
	var stdoutCatalog, stderrCatalog bytes.Buffer
	cmdCatalog.Stdout = &stdoutCatalog
	cmdCatalog.Stderr = &stderrCatalog

	if err := cmdCatalog.Run(); err != nil {
		errMsg := strings.TrimSpace(stderrCatalog.String())
		if errMsg != "" {
			return "", fmt.Errorf("failed to list formulas: %s", errMsg)
		}
		return "", fmt.Errorf("failed to list formulas: %w", err)
	}

	// Find patrol molecule in formula list
	// Format: "formula-name         description"
	var protoID string
	catalogLines := strings.Split(stdoutCatalog.String(), "\n")
	for _, line := range catalogLines {
		if strings.Contains(line, cfg.PatrolMolName) {
			parts := strings.Fields(line)
			if len(parts) > 0 {
				protoID = parts[0]
				break
			}
		}
	}

	if protoID == "" {
		return "", fmt.Errorf("proto %s not found in catalog", cfg.PatrolMolName)
	}

	// Create the patrol wisp (root only — steps are read inline at prime time,
	// not tracked as individual DB rows). Child wisps are reserved for pour=true
	// formulas like releases where checkpoint recovery matters.
	spawnArgs := []string{"mol", "wisp", "create", protoID, "--root-only", "--actor", cfg.RoleName}
	for _, v := range cfg.ExtraVars {
		spawnArgs = append(spawnArgs, "--var", v)
	}
	cmdSpawn := BdCmd(spawnArgs...).
		WithAutoCommit().
		WithBeadsDir(resolvedBeadsDir).
		Dir(cfg.BeadsDir).
		Build()
	var stdoutSpawn, stderrSpawn bytes.Buffer
	cmdSpawn.Stdout = &stdoutSpawn
	cmdSpawn.Stderr = &stderrSpawn

	if err := cmdSpawn.Run(); err != nil {
		return "", fmt.Errorf("failed to create patrol wisp: %s", stderrSpawn.String())
	}

	// Parse the created molecule ID from output
	// Format: "Root issue: <rig>-wisp-<hash>" where rig prefix varies
	var patrolID string
	spawnOutput := stdoutSpawn.String()
	for _, line := range strings.Split(spawnOutput, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Root issue:") {
			patrolID = strings.TrimSpace(strings.TrimPrefix(line, "Root issue:"))
			break
		}
	}
	// Fallback: look for any token containing "-wisp-"
	if patrolID == "" {
		for _, line := range strings.Split(spawnOutput, "\n") {
			for _, p := range strings.Fields(line) {
				if strings.Contains(p, "-wisp-") {
					patrolID = p
					break
				}
			}
			if patrolID != "" {
				break
			}
		}
	}

	if patrolID == "" {
		return "", fmt.Errorf("created wisp but could not parse ID from output")
	}

	// Hook the wisp to the agent so gt mol status sees it
	if err := BdCmd("update", patrolID, "--status=hooked", "--assignee="+cfg.Assignee).
		WithAutoCommit().
		WithBeadsDir(resolvedBeadsDir).
		Dir(cfg.BeadsDir).
		Run(); err != nil {
		return patrolID, fmt.Errorf("created wisp %s but failed to hook", patrolID)
	}

	return patrolID, nil
}

// outputPatrolContext is the main function that handles patrol display logic.
// It finds or creates a patrol and outputs the status and work loop.
func outputPatrolContext(cfg PatrolConfig) {
	fmt.Println()
	fmt.Printf("%s\n\n", style.Bold.Render(fmt.Sprintf("## %s %s", cfg.HeaderEmoji, cfg.HeaderTitle)))

	// Try to find an active patrol
	patrolID, patrolLine, hasPatrol, findErr := findActivePatrol(cfg)

	if findErr != nil {
		// Discovery failed — do NOT auto-spawn to avoid creating duplicates
		style.PrintWarning("patrol discovery failed: %v", findErr)
		fmt.Println("Status: **Discovery failed** — cannot determine patrol state")
		fmt.Println(style.Dim.Render("Check bd connectivity and retry. Not spawning new patrol to avoid duplicates."))
		return
	}

	if !hasPatrol {
		// No active patrol - auto-spawn one
		fmt.Printf("Status: **No active patrol** - creating %s...\n", cfg.PatrolMolName)
		fmt.Println()

		var err error
		patrolID, err = autoSpawnPatrol(cfg)
		if err != nil {
			if errors.Is(err, refinery.ErrSafetyStopped) {
				fmt.Println(style.Dim.Render(err.Error()))
				return
			}
			if patrolID != "" {
				fmt.Printf("⚠ %s\n", err.Error())
			} else {
				fmt.Println(style.Dim.Render(err.Error()))
				fmt.Println(style.Dim.Render("Run `" + cli.Name() + " formula list` to troubleshoot."))
				return
			}
		} else {
			fmt.Printf("✓ Created and hooked patrol wisp: %s\n", patrolID)
		}
	} else {
		// Has active patrol - show status
		fmt.Println("Status: **Patrol Active**")
		fmt.Printf("Patrol: %s\n\n", strings.TrimSpace(patrolLine))
	}

	// Show patrol work loop instructions
	fmt.Printf("**%s Patrol Work Loop:**\n", cases.Title(language.English).String(cfg.RoleName))
	for i, step := range cfg.WorkLoopSteps {
		fmt.Printf("%d. %s\n", i+1, step)
	}

	if patrolID != "" {
		fmt.Println()
		fmt.Printf("Current patrol ID: %s\n", patrolID)
	}
}

func refineryPatrolSafetyStop(cfg PatrolConfig) (*refinery.SafetyStop, error) {
	if cfg.RoleName != "refinery" {
		return nil, nil
	}
	rigName := strings.TrimSuffix(cfg.Assignee, "/refinery")
	if rigName == cfg.Assignee || rigName == "" {
		return nil, nil
	}
	return refinery.ActiveSafetyStop(cfg.BeadsDir, rigName)
}
