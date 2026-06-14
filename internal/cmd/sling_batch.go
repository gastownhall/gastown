package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// runBatchSling handles slinging multiple beads to a rig.
// Each bead gets its own freshly spawned polecat.
func runBatchSling(beadIDs []string, rigName string, townBeadsDir string) error {
	// Validate all beads exist before spawning any polecats
	for _, beadID := range beadIDs {
		if err := verifyBeadExists(beadID); err != nil {
			return fmt.Errorf("bead '%s' not found", beadID)
		}
	}
	townRoot := filepath.Dir(townBeadsDir)
	for _, beadID := range beadIDs {
		if err := verifyBeadExistsInTargetRigDatabase(beadID, rigName, townRoot); err != nil {
			return err
		}
	}

	// Cross-rig guard: check all beads match the target rig before spawning (gt-myecw)
	if !slingForce {
		for _, beadID := range beadIDs {
			prefix := beads.ExtractPrefix(beadID)
			beadRig := beads.GetRigNameForPrefix(townRoot, prefix)
			if prefix != "" && beadRig != "" && beadRig != rigName {
				others := make([]string, 0, len(beadIDs)-1)
				for _, id := range beadIDs {
					if id != beadID {
						others = append(others, id)
					}
				}
				// Build the full command suggestion safely — avoid appending to
				// beadIDs which may share a backing array with the caller's args.
				allArgs := make([]string, len(beadIDs)+1)
				copy(allArgs, beadIDs)
				allArgs[len(beadIDs)] = rigName
				return fmt.Errorf("bead %s (prefix %q) belongs to rig %q, but target is %q\n\n"+
					"  Options:\n"+
					"    1. Remove the mismatched bead from this batch:\n"+
					"         gt sling %s\n"+
					"    2. Sling the mismatched bead to its own rig:\n"+
					"         gt sling %s %s\n"+
					"    3. Use --force to override the cross-rig guard:\n"+
					"         gt sling %s --force\n",
					beadID, strings.TrimSuffix(prefix, "-"), beadRig, rigName,
					strings.Join(others, " "),
					beadID, beadRig,
					strings.Join(allArgs, " "))
			} else if err := checkCrossRigGuard(beadID, rigName+"/polecats/_", townRoot); err != nil {
				// Fall back to generic guard for edge cases (empty prefix, town-level beads)
				return err
			}
		}
	}

	// Issue #288: Auto-apply formula for batch sling (resolved via flags)
	formulaName := resolveFormula(slingFormula, slingHookRawBead, filepath.Dir(townBeadsDir), rigName)

	if slingDryRun {
		fmt.Printf("%s Batch slinging %d beads to rig '%s':\n", style.Bold.Render("🎯"), len(beadIDs), rigName)
		if formulaName != "" {
			fmt.Printf("  Would cook %s formula once\n", formulaName)
		} else {
			fmt.Printf("  Would hook raw beads (no formula)\n")
		}
		for _, beadID := range beadIDs {
			if formulaName != "" {
				fmt.Printf("  Would spawn polecat and apply %s to: %s\n", formulaName, beadID)
			} else {
				fmt.Printf("  Would spawn polecat and hook raw: %s\n", beadID)
			}
		}
		return nil
	}

	fmt.Printf("%s Batch slinging %d beads to rig '%s'...\n", style.Bold.Render("🎯"), len(beadIDs), rigName)

	if slingMaxConcurrent > 0 {
		fmt.Printf("  Spawn batch size: %d (spawns N, pauses, spawns N more)\n", slingMaxConcurrent)
	}

	// Cook formula once before the loop for efficiency
	formulaCooked := false

	// Pre-cook formula before the loop (batch optimization: cook once, instantiate many)
	if formulaName != "" {
		workDir := beads.ResolveHookDir(townRoot, beadIDs[0], "")
		if err := CookFormula(formulaName, workDir, townRoot); err != nil {
			fmt.Printf("  %s Could not pre-cook formula %s: %v\n", style.Dim.Render("Warning:"), formulaName, err)
			// Fall back: each executeSling call will try to cook individually
		} else {
			formulaCooked = true
		}
	}

	// Track results for summary
	type batchResult struct {
		beadID  string
		polecat string
		success bool
		errMsg  string
	}
	results := make([]batchResult, 0, len(beadIDs))
	activeCount := 0 // Track active spawns for --max-concurrent throttling

	var slingMode string
	if slingRalph {
		slingMode = "ralph"
	}

	// Dispatch each bead via executeSling
	for i, beadID := range beadIDs {
		// Spawn-rate throttle: when --max-concurrent is set, pause between batches
		// of N spawns. This does NOT limit total concurrent polecats — all spawned
		// polecats remain running. It only slows down how fast they are created.
		if slingMaxConcurrent > 0 && activeCount >= slingMaxConcurrent {
			fmt.Printf("\n%s Spawn batch of %d complete, pausing before next batch...\n",
				style.Warning.Render("⏳"), slingMaxConcurrent)
			// Wait for sessions to settle before spawning more
			for wait := 0; wait < 30; wait++ {
				time.Sleep(2 * time.Second)
				if wait >= 2 {
					break
				}
			}
			// Reset counter after cooldown — polecats become self-sufficient
			// quickly, so we use time-based batching rather than precise counting
			activeCount = 0
		}

		fmt.Printf("\n[%d/%d] Slinging %s...\n", i+1, len(beadIDs), beadID)

		params := SlingParams{
			BeadID:           beadID,
			FormulaName:      formulaName,
			RigName:          rigName,
			Args:             slingArgs,
			Vars:             slingVars,
			Merge:            slingMerge,
			BaseBranch:       slingBaseBranch,
			Account:          slingAccount,
			Agent:            slingAgent,
			NoConvoy:         slingNoConvoy,
			Owned:            slingOwned,
			NoMerge:          slingNoMerge,
			ReviewOnly:       slingReviewOnly,
			Force:            slingForce,
			HookRawBead:      slingHookRawBead,
			NoBoot:           slingNoBoot,
			Mode:             slingMode,
			SkipCook:         formulaCooked,
			FormulaFailFatal: false, // Batch: warn + hook raw on formula failure
			CallerContext:    "batch-sling",
			TownRoot:         townRoot,
			BeadsDir:         townBeadsDir,
		}

		result, err := executeSling(params)
		if err != nil {
			errMsg := ""
			if result != nil {
				errMsg = result.ErrMsg
			}
			if errMsg == "" {
				errMsg = err.Error()
			}
			polecatName := ""
			if result != nil {
				polecatName = result.PolecatName
			}
			results = append(results, batchResult{beadID: beadID, polecat: polecatName, success: false, errMsg: errMsg})
			fmt.Printf("  %s %s\n", style.Dim.Render("✗"), errMsg)
			continue
		}

		activeCount++
		results = append(results, batchResult{beadID: beadID, polecat: result.PolecatName, success: true})

		// Delay between spawns to prevent Dolt lock contention — sequential
		// spawns without delay cause database lock timeouts when multiple bd
		// operations (agent bead creation, hook setting) overlap.
		if i < len(beadIDs)-1 {
			time.Sleep(2 * time.Second)
		}
	}

	if !slingNoBoot {
		wakeRigAgents(rigName)
	}

	// Print summary
	successCount := 0
	for _, r := range results {
		if r.success {
			successCount++
		}
	}

	fmt.Printf("\n%s Batch sling complete: %d/%d succeeded\n", style.Bold.Render("📊"), successCount, len(beadIDs))
	if successCount < len(beadIDs) {
		for _, r := range results {
			if !r.success {
				fmt.Printf("  %s %s: %s\n", style.Dim.Render("✗"), r.beadID, r.errMsg)
			}
		}
	}
	if successCount == 0 && len(beadIDs) > 0 {
		return fmt.Errorf("batch sling failed: 0/%d succeeded", len(beadIDs))
	}

	return nil
}

// cleanupSpawnedPolecat removes a polecat that was spawned but whose session/hook failed,
// preventing orphaned polecats from accumulating. Cleans up worktree, agent bead, git branch,
// and optionally the associated auto-convoy.
func cleanupSpawnedPolecat(spawnInfo *SpawnedPolecatInfo, rigName, convoyID string) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return
	}
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return
	}
	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(rigName)
	if err != nil {
		return
	}
	polecatGit := git.NewGit(r.Path)
	t := tmux.NewTmux()
	polecatMgr := polecat.NewManager(r, polecatGit, t)
	if err := polecatMgr.Remove(spawnInfo.PolecatName, true); err != nil {
		fmt.Printf("  %s Could not clean up orphaned polecat %s: %v\n",
			style.Dim.Render("Warning:"), spawnInfo.PolecatName, err)
	} else {
		fmt.Printf("  %s Cleaned up orphaned polecat %s\n",
			style.Dim.Render("○"), spawnInfo.PolecatName)
	}

	// Delete the git branch if we know it (following nukePolecatFull pattern)
	if spawnInfo.Branch != "" {
		repoGit := getRepoGitForRig(r.Path)
		deletePolecatBranch(spawnInfo.Branch, repoGit, false)
	}

	// Close the auto-convoy if one was created
	if convoyID != "" {
		closeConvoy(convoyID, "Sling rollback - hook failed")
	}
}

// allBeadIDs returns true if every arg looks like a bead ID (syntactic check).
func allBeadIDs(args []string) bool {
	for _, arg := range args {
		if !looksLikeBeadID(arg) {
			return false
		}
	}
	return len(args) > 0
}

// resolveRigFromBeadIDs resolves the target rig from bead prefixes.
// All beads must resolve to the same rig. Returns an error with suggested
// actions if any prefix cannot be resolved or if beads span multiple rigs.
func resolveRigFromBeadIDs(beadIDs []string, townRoot string) (string, error) {
	var resolvedRig string
	mismatches := []string{} // "bead-id -> rig" for error reporting

	for _, beadID := range beadIDs {
		prefix := beads.ExtractPrefix(beadID)
		if prefix == "" {
			return "", fmt.Errorf("cannot resolve rig for %s: no valid prefix\n\n"+
				"  Options:\n"+
				"    1. Specify the rig explicitly:\n"+
				"         gt sling %s <rig>\n"+
				"    2. Check the bead ID is correct:\n"+
				"         bd show %s\n",
				beadID, strings.Join(beadIDs, " "), beadID)
		}

		rigName := beads.GetRigNameForPrefix(townRoot, prefix)
		if rigName == "" {
			return "", fmt.Errorf("cannot resolve rig for %s: prefix %q is not mapped to any rig\n\n"+
				"  The prefix may belong to a town-level bead or the routes are not configured.\n\n"+
				"  Options:\n"+
				"    1. Specify the rig explicitly:\n"+
				"         gt sling %s <rig>\n"+
				"    2. Check the bead's route mapping:\n"+
				"         cat .beads/routes.jsonl | grep %s\n"+
				"    3. Create the bead from the target rig directory instead:\n"+
				"         cd <rig> && bd create --title=...\n",
				beadID, prefix, strings.Join(beadIDs, " "), prefix)
		}

		if resolvedRig == "" {
			resolvedRig = rigName
		}
		mismatches = append(mismatches, fmt.Sprintf("    %s (prefix %s) -> %s", beadID, prefix, rigName))

		if rigName != resolvedRig {
			return "", fmt.Errorf("beads resolve to different rigs:\n\n%s\n\n"+
				"  All beads in a batch sling must target the same rig.\n\n"+
				"  Options:\n"+
				"    1. Sling each rig's beads separately:\n"+
				"         gt sling <bead1> <bead2> ...   (beads for %s)\n"+
				"         gt sling <bead3> <bead4> ...   (beads for %s)\n"+
				"    2. Specify the target rig explicitly:\n"+
				"         gt sling %s <rig>\n",
				strings.Join(mismatches, "\n"),
				resolvedRig, rigName,
				strings.Join(beadIDs, " "))
		}
	}

	if resolvedRig == "" {
		return "", fmt.Errorf("could not resolve rig from bead prefixes")
	}

	return resolvedRig, nil
}

// getRepoGitForRig creates a Git client for the rig's repository.
// It tries the bare repo first, then falls back to the mayor/rig directory.
func getRepoGitForRig(rigPath string) *git.Git {
	bareRepoPath := filepath.Join(rigPath, ".repo.git")
	if info, statErr := os.Stat(bareRepoPath); statErr == nil && info.IsDir() {
		return git.NewGitWithDir(bareRepoPath, "")
	}
	return git.NewGit(filepath.Join(rigPath, "mayor", "rig"))
}

// deletePolecatBranch deletes a local git branch for a polecat.
// Remote branch is never deleted during nuke — the refinery owns remote
// branch cleanup after successful merge (gt mq post-merge). (gt-v5ku)
func deletePolecatBranch(branchName string, repoGit *git.Git, hasPendingMR bool) {
	_ = hasPendingMR // preserved for API compat, no longer consulted
	if err := repoGit.DeleteBranch(branchName, true); err != nil {
		fmt.Printf("  %s branch delete: %v\n", style.Dim.Render("○"), err)
	} else {
		fmt.Printf("  %s deleted local branch %s\n", style.Success.Render("✓"), branchName)
	}
	fmt.Printf("  %s remote branch preserved for refinery merge\n", style.Dim.Render("○"))
}

// closeConvoy closes a convoy with the given reason.
// It is a best-effort operation that logs warnings on failure.
func closeConvoy(convoyID, reason string) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		fmt.Printf("  %s Could not find workspace to close convoy %s: %v\n", style.Dim.Render("Warning:"), convoyID, err)
		return
	}
	townBeads := filepath.Join(townRoot, ".beads")
	closeArgs := []string{"close", convoyID, "-r", reason}
	if err := BdCmd(closeArgs...).Dir(townBeads).WithAutoCommit().Run(); err != nil {
		fmt.Printf("  %s Could not close convoy %s: %v\n", style.Dim.Render("Warning:"), convoyID, err)
	} else {
		fmt.Printf("  %s Closed convoy %s\n", style.Dim.Render("○"), convoyID)
	}
}
