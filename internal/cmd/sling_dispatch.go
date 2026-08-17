package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/events"
	"github.com/jonbaldie/gastown/internal/mail"
	"github.com/jonbaldie/gastown/internal/nudge"
	"github.com/jonbaldie/gastown/internal/sling"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/worker"
	"github.com/jonbaldie/gastown/internal/workspace"
)

// executeSling is the test adapter around Lifecycle.Execute's one sequence.
func executeSling(ctx context.Context, intent sling.Intent) (*SlingResult, error) {
	return runTownSling(ctx, intent)
}

// SlingResult captures the outcome of one Lifecycle attempt for caller-level tracking.
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

// slingDestination is the resolved Hook target for one Lifecycle attempt.
// Target resolve and Polecat spawn stay inside this step.
type slingDestination struct {
	Agent             string
	Pane              string
	WorkDir           string
	SpawnInfo         *SpawnedPolecatInfo
	DelayedDog        *DogDispatchInfo
	HookSetAtomically bool
	IsSelfSling       bool
	Admission         *polecatAdmissionHandle
}

// runTownSling is the one Sling sequence owned by Lifecycle.Execute.
// Destination resolve and Polecat spawn are internal steps. Every fatal
// failure after a durable artifact exists uses the same compensation record.
func runTownSling(ctx context.Context, intent sling.Intent) (*SlingResult, error) {
	townRoot := intent.TownRoot
	if townRoot == "" {
		var err error
		townRoot, err = findTownRoot()
		if err != nil {
			return nil, err
		}
	}

	releaseLock, err := tryAcquireSlingBeadLock(townRoot, intent.BeadID)
	if err != nil {
		return &SlingResult{BeadID: intent.BeadID, ErrMsg: err.Error()}, err
	}
	defer releaseLock()

	beadsDir := intent.BeadsDir
	if beadsDir == "" {
		beadsDir = filepath.Join(townRoot, ".beads")
	}

	result := &SlingResult{BeadID: intent.BeadID}

	if live, liveErr := worker.LiveRunFromStore(townRoot, intent.BeadID); liveErr == nil && live != nil {
		result.ErrMsg = "live run"
		return result, fmt.Errorf("%w: bead %s already has live run %s", worker.ErrLiveRun, intent.BeadID, live.RunID)
	}

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

	info, err := getBeadInfoFromTownRoot(townRoot, intent.BeadID)
	if err != nil {
		result.ErrMsg = err.Error()
		return result, fmt.Errorf("could not get bead info: %w", err)
	}

	explicitForce := intent.Force
	sameTarget, formulaRefresh := slingSameTarget(intent, info, townRoot)
	status := evaluateSlingStatus(info, intent.BeadID, explicitForce, intent.Merge, sameTarget, formulaRefresh)
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
	originalStatus := info.Status
	originalAssignee := info.Assignee

	dest, movedID, err := resolveSlingDestination(intent, townRoot, explicitForce)
	if err != nil {
		result.ErrMsg = err.Error()
		return result, err
	}
	if dest.Admission != nil {
		defer dest.Admission.Release()
	}
	if movedID != "" && movedID != intent.BeadID {
		intent.BeadID = movedID
		result.BeadID = movedID
		info, err = getBeadInfoFromTownRoot(townRoot, movedID)
		if err != nil {
			result.ErrMsg = err.Error()
			return result, fmt.Errorf("could not get moved bead info: %w", err)
		}
	}

	targetAgent := dest.Agent
	targetPane := dest.Pane
	hookWorkDir := dest.WorkDir
	spawnInfo := dest.SpawnInfo
	delayedDogInfo := dest.DelayedDog
	result.SpawnInfo = spawnInfo
	if spawnInfo != nil {
		result.PolecatName = spawnInfo.PolecatName
	}

	vars := append([]string(nil), intent.Vars...)
	formulaName := intent.Formula
	dogDispatchDescription := info.Description
	previousDogCleared := false
	dogDispatchComplete := delayedDogInfo == nil
	defer func() {
		if dogDispatchComplete {
			return
		}
		rollbackStatus, rollbackAssignee := originalStatus, originalAssignee
		oldAssigneeParts := strings.Split(originalAssignee, "/")
		oldPolecatShuttingDown := len(oldAssigneeParts) >= 3 && oldAssigneeParts[1] == "polecats"
		if intent.Force && (oldPolecatShuttingDown || previousDogCleared) && (originalStatus == "hooked" || originalStatus == "in_progress") {
			rollbackStatus, rollbackAssignee = "open", ""
		}
		restored := rollbackFailedDogDispatch(delayedDogInfo, townRoot, intent.BeadID, hookWorkDir, dogDispatchDescription, rollbackStatus, rollbackAssignee, result.ConvoyID, info)
		cleanupSafe := restored || dogFormulaSourceStillOriginal(townRoot, intent.BeadID, info)
		if cleanupSafe && result.AttachedMolecule != "" {
			cleanupRolledBackDogMolecule(result.AttachedMolecule, intent.BeadID, townRoot)
		}
	}()

	if spawnInfo != nil && spawnInfo.BaseBranch != "" && spawnInfo.BaseBranch != "main" {
		vars = append(vars, fmt.Sprintf("base_branch=%s", spawnInfo.BaseBranch))
	}
	if intent.ResumeBranch != "" {
		vars = append(vars, fmt.Sprintf("resume_branch=%s", intent.ResumeBranch))
	}

	if intent.RigName == "" {
		if formulaName != "" {
			fmt.Printf("%s Slinging formula %s on %s to %s...\n", style.Bold.Render("🎯"), formulaName, intent.BeadID, targetAgent)
		} else {
			fmt.Printf("%s Slinging %s to %s...\n", style.Bold.Render("🎯"), intent.BeadID, targetAgent)
		}
	}

	if err := forceClearOldHook(intent, info, targetAgent, townRoot); err != nil {
		result.ErrMsg = err.Error()
		return result, err
	}

	if formulaName == "" && !intent.HookRawBead && strings.Contains(targetAgent, "/polecats/") {
		targetRig := intent.RigName
		if targetRig == "" {
			if parts := strings.SplitN(targetAgent, "/", 2); len(parts) >= 1 {
				targetRig = parts[0]
			}
		}
		formulaName = resolveFormula(slingFormula, false, townRoot, targetRig)
		if slingFormula != "" {
			fmt.Printf("  Applying %s for polecat work...\n", formulaName)
		} else if intent.RigName == "" {
			fmt.Printf("  Auto-applying %s for polecat work...\n", formulaName)
		}
		intent.Formula = formulaName
	}

	if intent.Formula != "" {
		existingMolecules, err := collectExistingMoleculesForBead(info, intent.BeadID, townRoot)
		if err != nil {
			result.ErrMsg = fmt.Sprintf("molecule check failed: %v", err)
			return result, fmt.Errorf("checking existing molecule bonds: %w", err)
		}
		if len(existingMolecules) > 0 {
			stale := intent.Force ||
				isOrphanMolecule(info) ||
				(info.Assignee == "" && (info.Status == "open" || info.Status == "in_progress")) ||
				(info.Assignee != "" && isHookedAgentDeadFn(info.Assignee))
			if intent.DryRun && stale {
				fmt.Printf("  Would burn %d stale molecule(s): %s\n",
					len(existingMolecules), strings.Join(existingMolecules, ", "))
			} else if stale {
				fmt.Printf("  %s Burning %d stale molecule(s): %s\n",
					style.Warning.Render("⚠"), len(existingMolecules), strings.Join(existingMolecules, ", "))
				if err := burnExistingMolecules(existingMolecules, intent.BeadID, townRoot); err != nil {
					result.ErrMsg = fmt.Sprintf("burn failed: %v", err)
					return result, fmt.Errorf("burning stale molecules: %w", err)
				}
				cleaned, err := getBeadInfoFromTownRoot(townRoot, intent.BeadID)
				if err != nil {
					result.ErrMsg = err.Error()
					return result, fmt.Errorf("reading bead after burning stale molecules: %w", err)
				}
				info.Description = cleaned.Description
			} else {
				result.ErrMsg = "has existing molecule(s)"
				return result, fmt.Errorf("bead %s has existing molecule(s) (use --force)", intent.BeadID)
			}
		}
	}

	convoyID, createdConvoy := resolveAttemptConvoy(intent, info)
	result.ConvoyID = convoyID
	rollbackConvoyID := ""
	if createdConvoy {
		rollbackConvoyID = convoyID
	}
	compensate := func(rollbackBeadID, reason string) {
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

	if intent.DryRun {
		printSlingDryRun(intent, info, formulaName, targetAgent, targetPane, convoyID)
		result.Success = true
		return result, nil
	}

	formulaCooked := intent.SkipCook
	if intent.Formula != "" && !formulaCooked {
		workDir := beads.ResolveHookDir(townRoot, intent.BeadID, hookWorkDir)
		if err := CookFormula(intent.Formula, workDir, townRoot); err != nil {
			if intent.FormulaFailFatal {
				compensate(intent.BeadID, "Formula cook failed")
				result.ErrMsg = fmt.Sprintf("cook failed: %v", err)
				return result, fmt.Errorf("cooking formula %s: %w", intent.Formula, err)
			}
			fmt.Printf("  %s Could not cook formula %s: %v\n", style.Dim.Render("Warning:"), intent.Formula, err)
		} else {
			formulaCooked = true
		}
	}

	beadToHook := intent.BeadID
	attachedMoleculeID := ""
	varsForAttachment := append([]string(nil), vars...)
	formulaVarsForAttachment := strings.Join(varsForAttachment, "\n")
	if intent.Formula != "" && formulaCooked {
		rigName := intent.RigName
		if rigName == "" {
			if parts := strings.SplitN(targetAgent, "/", 2); len(parts) >= 1 {
				rigName = parts[0]
			}
		}
		if rigName != "" {
			rigCmdVars := loadRigCommandVars(townRoot, rigName)
			vars = append(rigCmdVars, vars...)
		}
		if intent.RigName != "" {
			if priorVars := lookupPriorAttempt(beadsDir, intent.BeadID); len(priorVars) > 0 {
				vars = append(vars, priorVars...)
				fmt.Printf("  %s Prior attempt found — context injected for polecat\n", style.Dim.Render("↻"))
			}
		}
		varsForAttachment = append([]string(nil), vars...)
		formulaVarsForAttachment = strings.Join(vars, "\n")
		skipCook := intent.RigName != ""
		formulaResult, err := InstantiateFormulaOnBead(ctx, intent.Formula, intent.BeadID, info.Title, hookWorkDir, townRoot, skipCook, vars)
		if err != nil {
			if intent.FormulaFailFatal {
				compensate(intent.BeadID, "Formula instantiation failed")
				result.ErrMsg = fmt.Sprintf("formula failed: %v", err)
				return result, fmt.Errorf("instantiating formula %s: %w", intent.Formula, err)
			}
			fmt.Printf("  %s Could not apply formula: %v (hooking raw bead)\n", style.Dim.Render("Warning:"), err)
		} else {
			fmt.Printf("  %s Formula %s applied\n", style.Bold.Render("✓"), intent.Formula)
			beadToHook = formulaResult.BeadToHook
			attachedMoleculeID = formulaResult.WispRootID
			if len(formulaResult.FormulaVars) > 0 {
				vars = formulaResult.FormulaVars
				varsForAttachment = append([]string(nil), vars...)
				formulaVarsForAttachment = strings.Join(vars, "\n")
			}
		}
	}
	result.AttachedMolecule = attachedMoleculeID

	actor := detectActor()
	fieldUpdates := newSlingDispatchFieldUpdates(actor, intent, varsForAttachment, formulaVarsForAttachment, convoyID, attachedMoleculeID)

	assigneeUnlock, assigneeLockErr := tryAcquireSlingAssigneeLockFn(townRoot, targetAgent)
	if assigneeLockErr != nil {
		compensate(intent.BeadID, "Assignee lock failed")
		result.ErrMsg = "assignee lock failed"
		return result, fmt.Errorf("serializing hook write for %s: %w", targetAgent, assigneeLockErr)
	}
	defer assigneeUnlock()

	if attachedMoleculeID == "" && slingFieldsRequireDurableWrite(fieldUpdates) {
		storedDescription, err := storeFieldsInBeadFromTownRootWithDescription(townRoot, beadToHook, fieldUpdates)
		if err != nil {
			compensate(beadToHook, "Raw sling metadata failed")
			result.ErrMsg = "raw sling metadata failed"
			return result, fmt.Errorf("storing raw sling metadata before hook: %w", err)
		}
		if delayedDogInfo != nil {
			dogDispatchDescription = storedDescription
		}
	}

	hookDir := beads.ResolveHookDir(townRoot, beadToHook, hookWorkDir)
	if err := hookBeadWithRetryWithTownRootFn(beadToHook, targetAgent, hookDir, townRoot); err != nil {
		compensate(beadToHook, "Hook failed")
		result.ErrMsg = "hook failed"
		return result, fmt.Errorf("failed to hook bead: %w", err)
	}

	if targetAgent == "mayor/" {
		nudgeMayorHook(townRoot, beadToHook)
	}

	if spawnInfo != nil {
		fmt.Printf("  %s Work attached to %s\n", style.Bold.Render("✓"), spawnInfo.PolecatName)
	} else {
		fmt.Printf("%s Work attached to hook (status=hooked)\n", style.Bold.Render("✓"))
	}

	_ = events.LogFeed(events.TypeSling, actor, events.SlingPayload(beadToHook, targetAgent))

	if !dest.HookSetAtomically {
		if err := updateAgentHookBead(targetAgent, beadToHook, hookWorkDir, beadsDir); err != nil {
			if delayedDogInfo != nil {
				result.ErrMsg = err.Error()
				return result, fmt.Errorf("updating dog agent hook: %w", err)
			}
			fmt.Printf("%s Could not update agent hook: %v\n", style.Dim.Render("Warning:"), err)
		}
	}

	storedDescription, storeErr := storeFieldsInBeadFromTownRootWithDescription(townRoot, beadToHook, fieldUpdates)
	if storeErr != nil {
		if delayedDogInfo != nil && attachedMoleculeID != "" {
			result.ErrMsg = storeErr.Error()
			return result, fmt.Errorf("storing dog formula attachment metadata: %w", storeErr)
		}
		if slingFieldsRequireDurableWrite(fieldUpdates) {
			compensate(beadToHook, "Durable sling metadata failed")
			result.ErrMsg = "sling metadata failed"
			return result, fmt.Errorf("storing sling metadata: %w", storeErr)
		}
		fmt.Printf("  %s Could not store fields in bead: %v\n", style.Dim.Render("Warning:"), storeErr)
	} else {
		if delayedDogInfo != nil {
			dogDispatchDescription = storedDescription
		}
		if intent.Args != "" && intent.RigName == "" {
			fmt.Printf("%s Args stored in bead (durable)\n", style.Bold.Render("✓"))
		}
		if intent.NoMerge && intent.RigName == "" {
			fmt.Printf("%s No-merge mode enabled (work stays on feature branch)\n", style.Bold.Render("✓"))
		}
		if intent.ReviewOnly && intent.RigName == "" {
			fmt.Printf("%s Review-only mode: assignee must evaluate and report back, NOT merge/commit/push\n", style.Bold.Render("⚠"))
		}
	}

	if intent.Mode != "" {
		updateAgentMode(targetAgent, intent.Mode, hookWorkDir, beadsDir)
	}

	if delayedDogInfo != nil && intent.Force && strings.HasPrefix(originalAssignee, "deacon/dogs/") && originalAssignee != targetAgent {
		if err := clearPreviousDogAssignment(townRoot, originalAssignee, intent.BeadID, info.Description); err != nil {
			result.ErrMsg = err.Error()
			return result, fmt.Errorf("clearing previous dog assignment: %w", err)
		}
		previousDogCleared = true
	}

	freshlySpawned := spawnInfo != nil
	if freshlySpawned {
		pane, err := spawnInfo.StartSession()
		if err != nil {
			fmt.Printf("  %s Could not start session: %v, cleaning up partial state...\n", style.Dim.Render("✗"), err)
			compensate(beadToHook, "Session failed")
			result.ErrMsg = fmt.Sprintf("session failed: %v", err)
			return result, fmt.Errorf("starting polecat session: %w", err)
		}
		targetPane = pane
		result.PolecatName = spawnInfo.PolecatName
		if intent.RigName != "" {
			fmt.Printf("  %s Session started for %s\n", style.Bold.Render("▶"), spawnInfo.PolecatName)
		}
	}

	if delayedDogInfo != nil {
		pane, err := completeBareDogDispatch(delayedDogInfo, beadToHook, convoyID, attachedMoleculeID, intent.Subject, intent.Args)
		if err != nil {
			result.ErrMsg = err.Error()
			return result, err
		}
		targetPane = pane
		dogDispatchComplete = true
	} else if freshlySpawned {
		// Fresh polecat already got StartupNudge from SessionManager.Start().
	} else if dest.IsSelfSling {
		fmt.Printf("%s Self-sling: work hooked, will process on next turn\n", style.Dim.Render("○"))
	} else if targetPane == "" {
		if intent.RigName == "" {
			fmt.Printf("%s No pane to nudge (agent will discover work via gt prime)\n", style.Dim.Render("○"))
		}
	} else if intent.RigName == "" {
		sessionName := getSessionFromPane(targetPane)
		if sessionName != "" {
			if err := ensureAgentReady(sessionName); err != nil {
				fmt.Printf("%s Could not verify agent ready: %v\n", style.Dim.Render("○"), err)
			}
		}
		if err := injectStartPrompt(targetPane, beadToHook, intent.Subject, intent.Args); err != nil {
			fmt.Printf("%s Could not nudge (no tmux?): %v\n", style.Dim.Render("○"), err)
			fmt.Printf("  Agent will discover work via gt prime / bd show\n")
		} else {
			fmt.Printf("%s Start prompt sent\n", style.Bold.Render("▶"))
		}
	}

	if !intent.NoBoot && intent.RigName != "" {
		wakeRigAgents(intent.RigName)
	}

	result.Success = true
	return result, nil
}

func slingSameTarget(intent sling.Intent, info *beadInfo, townRoot string) (sameTarget, formulaRefresh bool) {
	if intent.RigName != "" {
		return isDefaultRigSlingNoop(intent, info, townRoot), false
	}
	formulaRefresh = intent.Formula != ""
	if intent.Target == "" || intent.Target == "." {
		if sa, _, _, err := resolveSelfTarget(); err == nil {
			return matchesSlingTarget(intent.Target, info.Assignee, sa), formulaRefresh
		}
		return false, formulaRefresh
	}
	return matchesSlingTarget(intent.Target, info.Assignee, ""), formulaRefresh
}

func resolveSlingDestination(intent sling.Intent, townRoot string, explicitForce bool) (slingDestination, string, error) {
	if intent.RigName != "" {
		movedID, err := ensureBeadInTargetRig(intent.BeadID, intent.RigName, townRoot, intent.DryRun)
		if err != nil {
			return slingDestination{}, "", err
		}
		beadID := intent.BeadID
		if movedID != "" {
			beadID = movedID
		}
		if !explicitForce {
			if err := checkCrossRigGuard(beadID, intent.RigName+"/polecats/_", townRoot); err != nil {
				return slingDestination{}, movedID, err
			}
		}
		spawnInfo, err := spawnPolecatForSling(intent.RigName, SlingSpawnOptions{
			TownRoot:     townRoot,
			Force:        intent.Force,
			Account:      intent.Account,
			HookBead:     beadID,
			Agent:        intent.Agent,
			BaseBranch:   intent.BaseBranch,
			ResumeBranch: intent.ResumeBranch,
			Create:       true,
		})
		if err != nil {
			return slingDestination{}, movedID, fmt.Errorf("failed to spawn polecat: %w", err)
		}
		return slingDestination{
			Agent:     spawnInfo.AgentID(),
			WorkDir:   spawnInfo.ClonePath,
			SpawnInfo: spawnInfo,
		}, movedID, nil
	}

	resolved, err := resolveTarget(intent.Target, ResolveTargetOptions{
		DryRun:       intent.DryRun,
		Force:        intent.Force,
		Create:       intent.Create,
		Account:      intent.Account,
		Agent:        intent.Agent,
		NoBoot:       intent.NoBoot,
		HookBead:     intent.BeadID,
		BeadID:       intent.BeadID,
		WorkDesc:     intent.Formula,
		TownRoot:     townRoot,
		BaseBranch:   intent.BaseBranch,
		ResumeBranch: intent.ResumeBranch,
	})
	if err != nil {
		return slingDestination{}, "", err
	}
	dest := slingDestination{
		Agent:             resolved.Agent,
		Pane:              resolved.Pane,
		WorkDir:           resolved.WorkDir,
		SpawnInfo:         resolved.NewPolecatInfo,
		DelayedDog:        resolved.DelayedDogInfo,
		HookSetAtomically: resolved.HookSetAtomically,
		IsSelfSling:       resolved.IsSelfSling,
	}
	if !intent.DryRun && !resolved.HookSetAtomically && strings.Contains(resolved.Agent, "/polecats/") {
		parts := strings.Split(resolved.Agent, "/")
		if len(parts) >= 3 {
			admission, snapshot, err := acquirePolecatAdmissionFn(townRoot, parts[0], resolved.BeadID, "direct-target")
			if err != nil {
				return slingDestination{}, resolved.BeadID, err
			}
			dest.Admission = admission
			if snapshot.Max > 0 {
				fmt.Printf("%s Polecat capacity reserved (%d free of %d)\n", style.Dim.Render("○"), snapshot.Free, snapshot.Max)
			}
		}
	}
	if resolved.BeadID != "" && resolved.BeadID != intent.BeadID {
		return dest, resolved.BeadID, nil
	}
	return dest, "", nil
}

func forceClearOldHook(intent sling.Intent, info *beadInfo, targetAgent, townRoot string) error {
	if info == nil || (info.Status != "hooked" && info.Status != "in_progress") || !intent.Force || info.Assignee == "" {
		return nil
	}
	if intent.RigName == "" {
		fmt.Printf("%s Bead already hooked to %s, forcing reassignment...\n", style.Warning.Render("⚠"), info.Assignee)
	}
	if intent.DryRun {
		fmt.Printf("Would send LIFECYCLE:Shutdown to previous assignee %s\n", info.Assignee)
		fmt.Printf("Would unhook %s from previous assignee\n", intent.BeadID)
		return nil
	}

	requester := intent.CallerContext
	if requester == "" {
		requester = "sling"
	}
	if polecat := os.Getenv("GT_POLECAT"); polecat != "" {
		requester = polecat
	} else if user := os.Getenv("USER"); user != "" && intent.RigName == "" {
		requester = user
	}

	assigneeParts := strings.Split(info.Assignee, "/")
	if len(assigneeParts) >= 3 && assigneeParts[1] == "polecats" {
		oldRigName := assigneeParts[0]
		oldPolecatName := assigneeParts[2]
		if townRoot != "" {
			callerCtx := intent.CallerContext
			if callerCtx == "" {
				if intent.RigName == "" {
					callerCtx = "gt-sling"
				} else {
					callerCtx = "sling"
				}
			}
			newAssignee := targetAgent
			if newAssignee == "" {
				newAssignee = intent.RigName
			}
			router := mail.NewRouter(townRoot)
			shutdownMsg := &mail.Message{
				From:     callerCtx,
				To:       fmt.Sprintf("%s/witness", oldRigName),
				Subject:  fmt.Sprintf("LIFECYCLE:Shutdown %s", oldPolecatName),
				Body:     fmt.Sprintf("Reason: work_reassigned\nRequestedBy: %s\nBead: %s\nNewAssignee: %s", requester, intent.BeadID, newAssignee),
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

	unhookDir := beads.ResolveHookDir(townRoot, intent.BeadID, "")
	if err := BdCmd("update", intent.BeadID, "--status=open", "--assignee=").
		Dir(unhookDir).
		WithAutoCommit().
		Run(); err != nil {
		fmt.Printf("%s Could not unhook bead from old owner: %v\n", style.Dim.Render("Warning:"), err)
	}
	return nil
}

func resolveAttemptConvoy(intent sling.Intent, info *beadInfo) (string, bool) {
	if intent.DryRun {
		if !intent.NoConvoy && intent.Formula == "" && intent.Convoy == "" {
			fmt.Printf("Would create convoy 'Work: %s' if needed\n", info.Title)
			fmt.Printf("Would add tracking relation to %s if needed\n", intent.BeadID)
			if intent.Merge != "" {
				fmt.Printf("Would set convoy merge strategy: %s\n", intent.Merge)
			}
		}
		return intent.Convoy, false
	}
	if intent.Formula != "" && intent.Convoy == "" {
		if intent.NoConvoy {
			return "", false
		}
		existing := isTrackedByConvoy(intent.BeadID)
		if existing != "" {
			fmt.Printf("%s Already tracked by convoy %s\n", style.Dim.Render("○"), existing)
		}
		return existing, false
	}
	return resolveSlingConvoy(intent, info)
}

func printSlingDryRun(intent sling.Intent, info *beadInfo, formulaName, targetAgent, targetPane string, convoyID string) {
	_ = convoyID
	if formulaName != "" {
		fmt.Printf("Would instantiate formula %s:\n", formulaName)
		fmt.Printf("  1. bd cook %s\n", formulaName)
		fmt.Printf("  2. bd mol bond %s %s --json --ephemeral --var feature=\"%s\" --var issue=\"%s\"\n", formulaName, intent.BeadID, info.Title, intent.BeadID)
		fmt.Printf("  3. bd update %s --status=hooked --assignee=%s\n", intent.BeadID, targetAgent)
	} else {
		fmt.Printf("Would run: bd update %s --status=hooked --assignee=%s\n", intent.BeadID, targetAgent)
	}
	if intent.Subject != "" {
		fmt.Printf("  subject (in nudge): %s\n", intent.Subject)
	}
	if intent.Message != "" {
		fmt.Printf("  context: %s\n", intent.Message)
	}
	if intent.Args != "" {
		fmt.Printf("  args (in nudge): %s\n", intent.Args)
	}
	fmt.Printf("Would inject start prompt to pane: %s\n", targetPane)
}

func nudgeMayorHook(townRoot, beadID string) {
	message := fmt.Sprintf("Hook updated: attached bead %s", beadID)
	if root, err := workspace.FindFromCwd(); err == nil && root != "" {
		_ = nudge.Enqueue(root, "hq-mayor", nudge.QueuedNudge{
			Sender:   "sling",
			Message:  message,
			Priority: nudge.PriorityNormal,
		})
		return
	}
	if townRoot != "" {
		_ = nudge.Enqueue(townRoot, "hq-mayor", nudge.QueuedNudge{
			Sender:   "sling",
			Message:  message,
			Priority: nudge.PriorityNormal,
		})
	}
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
