package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/events"
	"github.com/jonbaldie/gastown/internal/mail"
	"github.com/jonbaldie/gastown/internal/sling"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/worker"
)

// SlingResult captures the outcome of executeSling for caller-level tracking.
type SlingResult struct {
	BeadID           string
	PolecatName      string
	SpawnInfo        *SpawnedPolecatInfo
	ConvoyID         string
	Success          bool
	NoOp             bool
	ErrMsg           string
	AttachedMolecule string
}

// executeSling is the single Slinging entry: named targets when RigName is
// empty, otherwise polecat/rig dispatch. Adapters pass sling.Intent.
//
//   - per-bead flock
//   - parked/docked, flag-like title, closed/tombstone, deferred
//   - cross-rig guard (unless Force)
//   - dead-agent auto-force and rig-level idempotent no-op
//   - spawn, convoy, formula, hook, compensation
//   - witness wake unless NoBoot
//
// Batch, epic, convoy, and queue adapters pass NoBoot=true and coalesce
// wake after the loop. Single-bead CLI dispatch lets this function wake.
//
// Steps:
//  1. Get bead info + status check
//  2. Burn stale molecules (if formula and force)
//  3. Spawn polecat (via spawnPolecatForSling)
//  4. Auto-convoy (if !NoConvoy)
//  5. Cook formula (unless SkipCook)
//  6. Instantiate formula on bead (wisp + bond)
//  7. Hook bead with retry
//  8. Log sling event
//  9. Update agent hook_bead state
//  10. Store fields in bead (dispatcher, args, attached_molecule, no_merge)
//  11. Create Dolt branch
//  12. Start polecat session
//  13. Wake witness unless NoBoot
//
// executeRigSling is the polecat/rig dispatch body. Adapters invoke it
// through executeSling / defaultSlingLifecycle so Convoy reuse, Formula,
// Hook, attachment, and compensation stay in one place.
func executeSling(ctx context.Context, intent sling.Intent) (*SlingResult, error) {
	if intent.RigName == "" {
		return executeNamedTargetSling(ctx, intent)
	}
	return executeRigSling(ctx, intent)
}

func executeRigSling(ctx context.Context, intent sling.Intent) (*SlingResult, error) {
	townRoot := intent.TownRoot
	if townRoot == "" {
		var err error
		townRoot, err = findTownRoot()
		if err != nil {
			return nil, err
		}
	}

	// Acquire per-bead flock to prevent concurrent dispatch races (TOCTOU).
	// The CLI path (runSling) has its own flock; this closes the gap where
	// batch sling and queue dispatch could race against each other or against
	// a concurrent CLI invocation.
	releaseLock, err := tryAcquireSlingBeadLock(townRoot, intent.BeadID)
	if err != nil {
		return &SlingResult{BeadID: intent.BeadID, ErrMsg: err.Error()}, err
	}
	defer releaseLock()

	beadsDir := intent.BeadsDir
	if beadsDir == "" {
		beadsDir = filepath.Join(townRoot, ".beads")
	}

	result := &SlingResult{
		BeadID: intent.BeadID,
	}

	if live, liveErr := worker.LiveRunFromStore(townRoot, intent.BeadID); liveErr == nil && live != nil {
		result.ErrMsg = "live run"
		return result, fmt.Errorf("%w: bead %s already has live run %s", worker.ErrLiveRun, intent.BeadID, live.RunID)
	}

	// 0. Check if rig is parked or docked before dispatching (gt-4owfd.1, gt-11y)
	if intent.RigName != "" {
		if blocked, reason := IsRigParkedOrDocked(townRoot, intent.RigName); blocked {
			result.ErrMsg = "rig " + reason
			undoCmd := "gt rig unpark"
			if reason == "docked" {
				undoCmd = "gt rig undock"
			}
			return result, fmt.Errorf("cannot sling to %s rig %q\n%s %s", reason, intent.RigName, undoCmd, intent.RigName)
		}
	}

	// 1. Get bead info + status check
	info, err := getBeadInfoFromTownRoot(townRoot, intent.BeadID)
	if err != nil {
		result.ErrMsg = err.Error()
		return result, fmt.Errorf("could not get bead info: %w", err)
	}

	explicitForce := intent.Force
	status := evaluateSlingStatus(info, intent.BeadID, explicitForce, intent.Merge, isDefaultRigSlingNoop(intent, info, townRoot), false)
	if status.Err != nil {
		result.ErrMsg = status.ErrMsg
		return result, status.Err
	}
	if status.NoOp {
		result.Success = true
		result.NoOp = true
		if name, ok := polecatNameFromAssignee(intent.RigName, info.Assignee); ok {
			result.PolecatName = name
		}
		return result, nil
	}
	intent.Force = status.Force
	intent.Merge = status.Merge

	if intent.RigName != "" {
		movedID, err := ensureBeadInTargetRig(intent.BeadID, intent.RigName, townRoot, intent.DryRun)
		if err != nil {
			result.ErrMsg = err.Error()
			return result, err
		}
		if movedID != intent.BeadID {
			intent.BeadID = movedID
			result.BeadID = movedID
			info, err = getBeadInfoFromTownRoot(townRoot, movedID)
			if err != nil {
				result.ErrMsg = err.Error()
				return result, fmt.Errorf("could not get moved bead info: %w", err)
			}
		}
	}

	if intent.RigName != "" && !explicitForce {
		if err := checkCrossRigGuard(intent.BeadID, intent.RigName+"/polecats/_", townRoot); err != nil {
			result.ErrMsg = err.Error()
			return result, err
		}
	}

	// Send LIFECYCLE:Shutdown to the witness when force-stealing a bead from a
	// live polecat. Without this, the old polecat becomes a zombie — still running
	// but unaware it lost its hook. Mirrors the same logic in runSling (sling.go).
	if (info.Status == "hooked" || info.Status == "in_progress") && intent.Force && info.Assignee != "" {
		assigneeParts := strings.Split(info.Assignee, "/")
		if len(assigneeParts) >= 3 && assigneeParts[1] == "polecats" {
			oldRigName := assigneeParts[0]
			oldPolecatName := assigneeParts[2]
			if townRoot != "" {
				callerCtx := intent.CallerContext
				if callerCtx == "" {
					callerCtx = "sling"
				}
				router := mail.NewRouter(townRoot)
				shutdownMsg := &mail.Message{
					From:     callerCtx,
					To:       fmt.Sprintf("%s/witness", oldRigName),
					Subject:  fmt.Sprintf("LIFECYCLE:Shutdown %s", oldPolecatName),
					Body:     fmt.Sprintf("Reason: work_reassigned\nRequestedBy: %s\nBead: %s\nNewAssignee: %s", callerCtx, intent.BeadID, intent.RigName),
					Type:     mail.TypeTask,
					Priority: mail.PriorityHigh,
				}
				if err := router.Send(shutdownMsg); err != nil {
					fmt.Printf("  %s Could not send shutdown to witness: %v\n", style.Dim.Render("Warning:"), err)
				} else {
					fmt.Printf("  %s Sent LIFECYCLE:Shutdown to %s/witness for %s\n", style.Bold.Render("→"), oldRigName, oldPolecatName)
				}
				router.WaitPendingNotifications()
			}
		}
	}

	// 2. Burn stale molecules (if formula applies)
	if intent.Formula != "" {
		existingMolecules, err := collectExistingMoleculesForBead(info, intent.BeadID, townRoot)
		if err != nil {
			result.ErrMsg = fmt.Sprintf("molecule check failed: %v", err)
			return result, fmt.Errorf("checking existing molecule bonds: %w", err)
		}
		if len(existingMolecules) > 0 {
			// Auto-burn when bead is unassigned (molecules are definitionally stale),
			// or when the assigned agent's session is dead. This unblocks the daemon's
			// stranded convoy scan which never passes --force.
			stale := intent.Force ||
				(info.Assignee == "" && (info.Status == "open" || info.Status == "in_progress")) ||
				(info.Assignee != "" && isHookedAgentDeadFn(info.Assignee))
			if stale {
				fmt.Printf("  %s Burning %d stale molecule(s): %s\n",
					style.Warning.Render("⚠"), len(existingMolecules), strings.Join(existingMolecules, ", "))
				if err := burnExistingMolecules(existingMolecules, intent.BeadID, townRoot); err != nil {
					result.ErrMsg = fmt.Sprintf("burn failed: %v", err)
					return result, fmt.Errorf("burning stale molecules: %w", err)
				}
			} else {
				result.ErrMsg = "has existing molecule(s)"
				return result, fmt.Errorf("bead %s has existing molecule(s) (use --force)", intent.BeadID)
			}
		}
	}

	// 3. Spawn polecat (via spawnPolecatForSling)
	spawnOpts := SlingSpawnOptions{
		TownRoot:     townRoot,
		Force:        intent.Force,
		Account:      intent.Account,
		HookBead:     intent.BeadID,
		Agent:        intent.Agent,
		BaseBranch:   intent.BaseBranch,
		ResumeBranch: intent.ResumeBranch,
		// Create is always true for rig targets: executeSling only handles
		// rig-targeted dispatch (batch sling + queue dispatch), where a fresh
		// polecat must be spawned. The single-sling path (runSling) handles
		// the --create flag for non-rig targets via resolveTarget.
		Create: true,
	}
	spawnInfo, err := spawnPolecatForSling(intent.RigName, spawnOpts)
	if err != nil {
		result.ErrMsg = err.Error()
		return result, fmt.Errorf("failed to spawn polecat: %w", err)
	}
	result.SpawnInfo = spawnInfo
	result.PolecatName = spawnInfo.PolecatName

	targetAgent := spawnInfo.AgentID()
	hookWorkDir := spawnInfo.ClonePath

	// 4. Convoy: reuse a recorded identity, reuse an existing tracker, or create.
	convoyID, createdConvoy := resolveSlingConvoy(intent, info)
	result.ConvoyID = convoyID
	rollbackConvoyID := ""
	if createdConvoy {
		rollbackConvoyID = convoyID
	}
	rollbackSpawnedPolecat := func(rollbackBeadID, reason string) {
		compensateSlingAttempt(slingCompensation{
			reason:        reason,
			spawnInfo:     spawnInfo,
			beadID:        rollbackBeadID,
			hookWorkDir:   hookWorkDir,
			createdConvoy: rollbackConvoyID,
			townRoot:      townRoot,
			originalInfo:  info,
			force:         intent.Force,
		})
	}

	// 5. Cook formula (unless SkipCook)
	formulaCooked := intent.SkipCook
	if intent.Formula != "" && !formulaCooked {
		workDir := beads.ResolveHookDir(townRoot, intent.BeadID, hookWorkDir)
		if err := CookFormula(intent.Formula, workDir, townRoot); err != nil {
			if intent.FormulaFailFatal {
				// Rollback spawned polecat on fatal cook failure
				rollbackSpawnedPolecat(intent.BeadID, "Formula cook failed")
				result.ErrMsg = fmt.Sprintf("cook failed: %v", err)
				return result, fmt.Errorf("cooking formula %s: %w", intent.Formula, err)
			}
			fmt.Printf("  %s Could not cook formula %s: %v\n", style.Dim.Render("Warning:"), intent.Formula, err)
		} else {
			formulaCooked = true
		}
	}

	// 6. Instantiate formula on bead (wisp + bond)
	beadToHook := intent.BeadID
	attachedMoleculeID := ""
	var allVars []string
	varsForAttachment := append([]string(nil), intent.Vars...)
	formulaVarsForAttachment := strings.Join(varsForAttachment, "\n")
	if intent.Formula != "" && formulaCooked {
		// Auto-inject rig command vars as defaults (user --var flags override)
		rigCmdVars := loadRigCommandVars(townRoot, intent.RigName)
		// Build per-bead vars: rig defaults first, then user vars (higher priority)
		allVars = append(rigCmdVars, intent.Vars...)
		if spawnInfo.BaseBranch != "" && spawnInfo.BaseBranch != "main" {
			allVars = append(allVars, fmt.Sprintf("base_branch=%s", spawnInfo.BaseBranch))
		}
		if intent.ResumeBranch != "" {
			allVars = append(allVars, fmt.Sprintf("resume_branch=%s", intent.ResumeBranch))
		}

		// GH#gt-zqvj: Inject prior attempt context when re-dispatching an issue
		// that already has an open MR from a previous polecat. The new polecat
		// gets the old branch name so it can cherry-pick prior work instead of
		// starting from scratch.
		if priorVars := lookupPriorAttempt(beadsDir, intent.BeadID); len(priorVars) > 0 {
			allVars = append(allVars, priorVars...)
			fmt.Printf("  %s Prior attempt found — context injected for polecat\n", style.Dim.Render("↻"))
		}
		varsForAttachment = append([]string(nil), allVars...)
		formulaVarsForAttachment = strings.Join(allVars, "\n")
		formulaResult, err := InstantiateFormulaOnBead(ctx, intent.Formula, intent.BeadID, info.Title, hookWorkDir, townRoot, true, allVars)
		if err != nil {
			if intent.FormulaFailFatal {
				// Rollback spawned polecat on fatal formula failure
				rollbackSpawnedPolecat(intent.BeadID, "Formula instantiation failed")
				result.ErrMsg = fmt.Sprintf("formula failed: %v", err)
				return result, fmt.Errorf("instantiating formula %s: %w", intent.Formula, err)
			}
			// Best-effort: in batch mode, a formula instantiation failure should not abort or rollback the
			// spawned polecat. We still hook the raw bead so work can proceed (e.g., missing required vars).
			fmt.Printf("  %s Could not apply formula: %v (hooking raw bead)\n", style.Dim.Render("Warning:"), err)
		} else {
			fmt.Printf("  %s Formula %s applied\n", style.Bold.Render("✓"), intent.Formula)
			beadToHook = formulaResult.BeadToHook
			attachedMoleculeID = formulaResult.WispRootID
			if len(formulaResult.FormulaVars) > 0 {
				allVars = formulaResult.FormulaVars
				varsForAttachment = append([]string(nil), allVars...)
				formulaVarsForAttachment = strings.Join(allVars, "\n")
			}
		}
	}
	result.AttachedMolecule = attachedMoleculeID

	actor := detectActor()
	fieldUpdates := newSlingDispatchFieldUpdates(actor, intent, varsForAttachment, formulaVarsForAttachment, convoyID, attachedMoleculeID)

	// 7. Hook bead with retry
	// Acquire per-assignee lock to serialize concurrent hook writes (issue #3114).
	assigneeUnlock, assigneeLockErr := tryAcquireSlingAssigneeLockFn(townRoot, targetAgent)
	if assigneeLockErr != nil {
		rollbackSpawnedPolecat(intent.BeadID, "Assignee lock failed")
		result.ErrMsg = "assignee lock failed"
		return result, fmt.Errorf("serializing hook write for %s: %w", targetAgent, assigneeLockErr)
	}
	defer assigneeUnlock()
	if attachedMoleculeID == "" && slingFieldsRequireDurableWrite(fieldUpdates) {
		if err := storeFieldsInBeadFromTownRoot(townRoot, beadToHook, fieldUpdates); err != nil {
			rollbackSpawnedPolecat(beadToHook, "Raw sling metadata failed")
			result.ErrMsg = "raw sling metadata failed"
			return result, fmt.Errorf("storing raw sling metadata before hook: %w", err)
		}
	}
	hookDir := beads.ResolveHookDir(townRoot, beadToHook, hookWorkDir)
	if err := hookBeadWithRetryWithTownRootFn(beadToHook, targetAgent, hookDir, townRoot); err != nil {
		// Clean up all partial sling state, including raw metadata stored before hook.
		rollbackSpawnedPolecat(beadToHook, "Hook failed")
		result.ErrMsg = "hook failed"
		return result, fmt.Errorf("failed to hook bead: %w", err)
	}

	fmt.Printf("  %s Work attached to %s\n", style.Bold.Render("✓"), spawnInfo.PolecatName)

	// 8. Log sling event
	_ = events.LogFeed(events.TypeSling, actor, events.SlingPayload(beadToHook, targetAgent))

	// 9. Update agent hook_bead state
	if err := updateAgentHookBead(targetAgent, beadToHook, hookWorkDir, beadsDir); err != nil {
		fmt.Printf("%s Could not update agent hook: %v\n", style.Dim.Render("Warning:"), err)
	}

	// 10. Store fields in bead (dispatcher, args, attached_molecule, no_merge, mode)
	// Use beadToHook for the update target (may differ from beadID when formula-on-bead)
	if err := storeFieldsInBeadFromTownRoot(townRoot, beadToHook, fieldUpdates); err != nil {
		if slingFieldsRequireDurableWrite(fieldUpdates) {
			rollbackSpawnedPolecat(beadToHook, "Durable sling metadata failed")
			result.ErrMsg = "sling metadata failed"
			return result, fmt.Errorf("storing sling metadata: %w", err)
		}
		fmt.Printf("  %s Could not store fields in bead: %v\n", style.Dim.Render("Warning:"), err)
	}

	// Update agent bead mode for stuck-detector Ralph thresholds. Reuse/reset clears stale mode.
	if intent.Mode != "" {
		updateAgentMode(targetAgent, intent.Mode, hookWorkDir, beadsDir)
	}

	// 11. Start polecat session
	pane, err := spawnInfo.StartSession()
	if err != nil {
		fmt.Printf("  %s Could not start session: %v, cleaning up partial state...\n", style.Dim.Render("✗"), err)
		rollbackSpawnedPolecat(beadToHook, "Session failed")
		result.ErrMsg = fmt.Sprintf("session failed: %v", err)
		return result, fmt.Errorf("starting polecat session: %w", err)
	}
	fmt.Printf("  %s Session started for %s\n", style.Bold.Render("▶"), spawnInfo.PolecatName)
	_ = pane

	if !intent.NoBoot && intent.RigName != "" {
		wakeRigAgents(intent.RigName)
	}

	result.Success = true
	return result, nil
}

// isDefaultRigSlingNoop reports whether this rig dispatch would only repeat
// work already hooked to a live polecat in the same rig. Auto-applied
// mol-polecat-work (or the rig default formula) is not new formula work.
func isDefaultRigSlingNoop(intent sling.Intent, info *beadInfo, townRoot string) bool {
	if intent.RigName == "" || info == nil {
		return false
	}
	if !matchesSlingTarget(intent.RigName, info.Assignee, "") {
		return false
	}
	defaultFormula := resolveFormula("", intent.HookRawBead, townRoot, intent.RigName)
	return intent.Formula == "" || intent.Formula == defaultFormula
}

// findTownRoot is defined in hook.go
