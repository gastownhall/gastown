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
	"github.com/jonbaldie/gastown/internal/workspace"
)

// executeNamedTargetSling is the Lifecycle body for non-rig Slinging:
// Mayor, Crew, dogs, self, and explicit agent paths. Rig/queue dispatch
// stays in executeSling (RigName set).
func executeNamedTargetSling(ctx context.Context, intent sling.Intent) (*SlingResult, error) {
	beadID := intent.BeadID
	formulaName := intent.Formula
	townRoot := intent.TownRoot
	if townRoot == "" {
		var err error
		townRoot, err = findTownRoot()
		if err != nil {
			return nil, err
		}
	}
	beadsDir := intent.BeadsDir
	if beadsDir == "" {
		beadsDir = filepath.Join(townRoot, ".beads")
	}
	result := &SlingResult{BeadID: beadID}
	vars := append([]string(nil), intent.Vars...)

	releaseSlingLock, err := tryAcquireSlingBeadLock(townRoot, beadID)
	if err != nil {
		result.ErrMsg = err.Error()
		return result, err
	}
	defer releaseSlingLock()

	info, err := getBeadInfoFromTownRoot(townRoot, beadID)
	if err != nil {
		result.ErrMsg = err.Error()
		return result, fmt.Errorf("checking bead status: %w", err)
	}

	sameTarget := false
	formulaRefresh := formulaName != ""
	if intent.Target == "" || intent.Target == "." {
		if sa, _, _, err := resolveSelfTarget(); err == nil {
			sameTarget = matchesSlingTarget(intent.Target, info.Assignee, sa)
		}
	} else {
		sameTarget = matchesSlingTarget(intent.Target, info.Assignee, "")
	}
	status := evaluateSlingStatus(info, beadID, intent.Force, intent.Merge, sameTarget, formulaRefresh)
	if status.Err != nil {
		result.ErrMsg = status.ErrMsg
		return result, status.Err
	}
	if status.NoOp {
		result.Success = true
		result.NoOp = true
		return result, nil
	}
	mergeStrategy := status.Merge
	force := status.Force
	originalStatus := info.Status
	originalAssignee := info.Assignee

	resolved, err := resolveTarget(intent.Target, ResolveTargetOptions{
		DryRun:       intent.DryRun,
		Force:        force,
		Create:       intent.Create,
		Account:      intent.Account,
		Agent:        intent.Agent,
		NoBoot:       intent.NoBoot,
		HookBead:     beadID,
		BeadID:       beadID,
		WorkDesc:     formulaName,
		TownRoot:     townRoot,
		BaseBranch:   intent.BaseBranch,
		ResumeBranch: intent.ResumeBranch,
	})
	if err != nil {
		result.ErrMsg = err.Error()
		return result, err
	}
	if resolved.BeadID != "" && resolved.BeadID != beadID {
		beadID = resolved.BeadID
		intent.BeadID = resolved.BeadID
		result.BeadID = resolved.BeadID
		info, err = getBeadInfoFromTownRoot(townRoot, beadID)
		if err != nil {
			result.ErrMsg = err.Error()
			return result, fmt.Errorf("checking moved bead status: %w", err)
		}
	}
	targetAgent := resolved.Agent
	targetPane := resolved.Pane
	hookWorkDir := resolved.WorkDir
	hookSetAtomically := resolved.HookSetAtomically
	var admission *polecatAdmissionHandle
	if !intent.DryRun && !hookSetAtomically && strings.Contains(targetAgent, "/polecats/") {
		parts := strings.Split(targetAgent, "/")
		if len(parts) >= 3 {
			var snapshot polecatCapacitySnapshot
			admission, snapshot, err = acquirePolecatAdmissionFn(townRoot, parts[0], beadID, "direct-target")
			if err != nil {
				result.ErrMsg = err.Error()
				return result, err
			}
			defer admission.Release()
			if snapshot.Max > 0 {
				fmt.Printf("%s Polecat capacity reserved (%d free of %d)\n", style.Dim.Render("○"), snapshot.Free, snapshot.Max)
			}
		}
	}
	delayedDogInfo := resolved.DelayedDogInfo
	newPolecatInfo := resolved.NewPolecatInfo
	isSelfSling := resolved.IsSelfSling
	var convoyID string
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
		if force && (oldPolecatShuttingDown || previousDogCleared) && (originalStatus == "hooked" || originalStatus == "in_progress") {
			rollbackStatus, rollbackAssignee = "open", ""
		}
		restored := rollbackFailedDogDispatch(delayedDogInfo, townRoot, beadID, hookWorkDir, dogDispatchDescription, rollbackStatus, rollbackAssignee, convoyID, info)
		cleanupSafe := restored || dogFormulaSourceStillOriginal(townRoot, beadID, info)
		if cleanupSafe && result.AttachedMolecule != "" {
			cleanupRolledBackDogMolecule(result.AttachedMolecule, beadID, townRoot)
		}
	}()
	rollbackSpawnedPolecat := func(reason string) {
		if newPolecatInfo != nil {
			fmt.Printf("%s %s, rolling back spawned polecat %s...\n", style.Warning.Render("⚠"), reason, newPolecatInfo.PolecatName)
			rollbackSlingArtifactsFn(newPolecatInfo, beadID, hookWorkDir, "")
		}
		restoreRollbackRawWorkflowFieldsFromCurrent(beadID, townRoot, hookWorkDir, info)
		if force && originalStatus == "pinned" {
			restorePinnedBead(townRoot, beadID, originalAssignee)
		}
	}

	if newPolecatInfo != nil && newPolecatInfo.BaseBranch != "" && newPolecatInfo.BaseBranch != "main" {
		vars = append(vars, fmt.Sprintf("base_branch=%s", newPolecatInfo.BaseBranch))
	}
	if intent.ResumeBranch != "" {
		vars = append(vars, fmt.Sprintf("resume_branch=%s", intent.ResumeBranch))
	}

	if strings.Contains(targetAgent, "/polecats/") && !force && !isSelfSling {
		if err := checkCrossRigGuard(beadID, targetAgent, townRoot); err != nil {
			rollbackSpawnedPolecat("Cross-rig guard failed")
			result.ErrMsg = err.Error()
			return result, err
		}
	}

	if formulaName != "" {
		fmt.Printf("%s Slinging formula %s on %s to %s...\n", style.Bold.Render("🎯"), formulaName, beadID, targetAgent)
	} else {
		fmt.Printf("%s Slinging %s to %s...\n", style.Bold.Render("🎯"), beadID, targetAgent)
	}

	if (info.Status == "hooked" || info.Status == "in_progress") && force && info.Assignee != "" {
		fmt.Printf("%s Bead already hooked to %s, forcing reassignment...\n", style.Warning.Render("⚠"), info.Assignee)
		if intent.DryRun {
			fmt.Printf("Would send LIFECYCLE:Shutdown to previous assignee %s\n", info.Assignee)
			fmt.Printf("Would unhook %s from previous assignee\n", beadID)
		} else {
			requester := "gt-sling"
			if polecat := os.Getenv("GT_POLECAT"); polecat != "" {
				requester = polecat
			} else if user := os.Getenv("USER"); user != "" {
				requester = user
			}

			assigneeParts := strings.Split(info.Assignee, "/")
			if len(assigneeParts) >= 3 && assigneeParts[1] == "polecats" {
				oldRigName := assigneeParts[0]
				oldPolecatName := assigneeParts[2]

				if townRoot != "" {
					router := mail.NewRouter(townRoot)
					defer router.WaitPendingNotifications()
					callerCtx := intent.CallerContext
					if callerCtx == "" {
						callerCtx = "gt-sling"
					}
					shutdownMsg := &mail.Message{
						From:     callerCtx,
						To:       fmt.Sprintf("%s/witness", oldRigName),
						Subject:  fmt.Sprintf("LIFECYCLE:Shutdown %s", oldPolecatName),
						Body:     fmt.Sprintf("Reason: work_reassigned\nRequestedBy: %s\nBead: %s\nNewAssignee: %s", requester, beadID, targetAgent),
						Type:     mail.TypeTask,
						Priority: mail.PriorityHigh,
					}
					if err := router.Send(shutdownMsg); err != nil {
						fmt.Printf("%s Could not send shutdown to witness: %v\n", style.Dim.Render("Warning:"), err)
					} else {
						fmt.Printf("%s Sent LIFECYCLE:Shutdown to %s/witness for %s\n", style.Bold.Render("→"), oldRigName, oldPolecatName)
					}
				}
			}

			unhookDir := beads.ResolveHookDir(townRoot, beadID, "")
			if err := BdCmd("update", beadID, "--status=open", "--assignee=").
				Dir(unhookDir).
				WithAutoCommit().
				Run(); err != nil {
				fmt.Printf("%s Could not unhook bead from old owner: %v\n", style.Dim.Render("Warning:"), err)
			}
		}
	}

	if !intent.NoConvoy && formulaName == "" {
		if intent.DryRun {
			fmt.Printf("Would create convoy 'Work: %s' if needed\n", info.Title)
			fmt.Printf("Would add tracking relation to %s if needed\n", beadID)
			if mergeStrategy != "" {
				fmt.Printf("Would set convoy merge strategy: %s\n", mergeStrategy)
			}
		} else {
			existingConvoy := isTrackedByConvoy(beadID)
			if existingConvoy == "" {
				var err error
				convoyID, err = createAutoConvoy(beadID, info.Title, intent.Owned, mergeStrategy, intent.BaseBranch)
				if err != nil {
					if delayedDogInfo != nil {
						result.ErrMsg = err.Error()
						return result, fmt.Errorf("creating dog dispatch convoy: %w", err)
					}
					fmt.Printf("%s Could not create auto-convoy: %v\n", style.Dim.Render("Warning:"), err)
				} else {
					fmt.Printf("%s Created convoy 🚚 %s\n", style.Bold.Render("→"), convoyID)
					fmt.Printf("  Tracking: %s\n", beadID)
					if intent.Owned {
						fmt.Printf("  Lifecycle: caller-managed (owned)\n")
					}
					if mergeStrategy != "" {
						fmt.Printf("  Merge:    %s\n", mergeStrategy)
					}
				}
			} else {
				convoyID = existingConvoy
				fmt.Printf("%s Already tracked by convoy %s\n", style.Dim.Render("○"), existingConvoy)
			}
		}
	}
	result.ConvoyID = convoyID

	if formulaName == "" && !intent.HookRawBead && strings.Contains(targetAgent, "/polecats/") {
		targetRig := ""
		if parts := strings.SplitN(targetAgent, "/", 2); len(parts) >= 1 {
			targetRig = parts[0]
		}
		formulaName = resolveFormula(slingFormula, false, townRoot, targetRig)
		if slingFormula != "" {
			fmt.Printf("  Applying %s for polecat work...\n", formulaName)
		} else {
			fmt.Printf("  Auto-applying %s for polecat work...\n", formulaName)
		}
	}

	if formulaName != "" {
		existingMolecules, err := collectExistingMoleculesForBead(info, beadID, townRoot)
		if err != nil {
			result.ErrMsg = err.Error()
			return result, fmt.Errorf("checking existing molecule bonds: %w", err)
		}
		if len(existingMolecules) > 0 {
			stale := force || isOrphanMolecule(info)
			if intent.DryRun && stale {
				fmt.Printf("  Would burn %d stale molecule(s): %s\n",
					len(existingMolecules), strings.Join(existingMolecules, ", "))
			} else if stale {
				fmt.Printf("  %s Burning %d stale molecule(s) from previous assignment: %s\n",
					style.Warning.Render("⚠"), len(existingMolecules), strings.Join(existingMolecules, ", "))
				if err := burnExistingMolecules(existingMolecules, beadID, townRoot); err != nil {
					result.ErrMsg = err.Error()
					return result, fmt.Errorf("burning stale molecules: %w", err)
				}
				cleaned, err := getBeadInfoFromTownRoot(townRoot, beadID)
				if err != nil {
					result.ErrMsg = err.Error()
					return result, fmt.Errorf("reading bead after burning stale molecules: %w", err)
				}
				info.Description = cleaned.Description
			} else {
				result.ErrMsg = "has existing molecule(s)"
				return result, fmt.Errorf("bead %s already has %d attached molecule(s): %s\nUse --force to replace, or --hook-raw-bead to skip formula",
					beadID, len(existingMolecules), strings.Join(existingMolecules, ", "))
			}
		}
	}

	if intent.DryRun {
		if formulaName != "" {
			fmt.Printf("Would instantiate formula %s:\n", formulaName)
			fmt.Printf("  1. bd cook %s\n", formulaName)
			fmt.Printf("  2. bd mol bond %s %s --json --ephemeral --var feature=\"%s\" --var issue=\"%s\"\n", formulaName, beadID, info.Title, beadID)
			fmt.Printf("  3. bd update %s --status=hooked --assignee=%s\n", beadID, targetAgent)
		} else {
			fmt.Printf("Would run: bd update %s --status=hooked --assignee=%s\n", beadID, targetAgent)
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
		result.Success = true
		return result, nil
	}

	formulaVarsForAttachment := strings.Join(vars, "\n")
	varsForAttachment := append([]string(nil), vars...)
	attachedMoleculeID := ""
	if formulaName != "" {
		fmt.Printf("  Instantiating formula %s...\n", formulaName)

		if parts := strings.SplitN(targetAgent, "/", 2); len(parts) >= 1 && parts[0] != "" {
			rigCmdVars := loadRigCommandVars(townRoot, parts[0])
			vars = append(rigCmdVars, vars...)
			varsForAttachment = append([]string(nil), vars...)
			formulaVarsForAttachment = strings.Join(vars, "\n")
		}

		instResult, err := InstantiateFormulaOnBead(ctx, formulaName, beadID, info.Title, hookWorkDir, townRoot, false, vars)
		if err != nil {
			if newPolecatInfo != nil {
				rollbackSpawnedPolecat("Formula instantiation failed")
			}
			result.ErrMsg = err.Error()
			return result, fmt.Errorf("instantiating formula %s: %w", formulaName, err)
		}

		fmt.Printf("%s Formula wisp created: %s\n", style.Bold.Render("✓"), instResult.WispRootID)
		fmt.Printf("%s Formula bonded to %s\n", style.Bold.Render("✓"), beadID)

		attachedMoleculeID = instResult.WispRootID
		if len(instResult.FormulaVars) > 0 {
			varsForAttachment = append([]string(nil), instResult.FormulaVars...)
			formulaVarsForAttachment = strings.Join(instResult.FormulaVars, "\n")
		}
	}
	result.AttachedMolecule = attachedMoleculeID

	actor := detectActor()
	mode := intent.Mode
	fieldUpdates := buildSlingFieldUpdates(
		actor,
		intent.Args,
		varsForAttachment,
		attachedMoleculeID,
		formulaName,
		intent.NoMerge,
		intent.ReviewOnly,
		mode,
		formulaVarsForAttachment,
		convoyID,
		mergeStrategy,
		intent.Owned,
	)
	assigneeUnlock, assigneeLockErr := tryAcquireSlingAssigneeLock(townRoot, targetAgent)
	if assigneeLockErr != nil {
		result.ErrMsg = assigneeLockErr.Error()
		return result, fmt.Errorf("serializing hook write for %s: %w", targetAgent, assigneeLockErr)
	}
	defer assigneeUnlock()
	if attachedMoleculeID == "" && slingFieldsRequireDurableWrite(fieldUpdates) {
		storedDescription, err := storeFieldsInBeadFromTownRootWithDescription(townRoot, beadID, fieldUpdates)
		if err != nil {
			if newPolecatInfo != nil {
				fmt.Printf("%s Raw sling metadata failed, cleaning up spawned polecat %s...\n", style.Warning.Render("⚠"), newPolecatInfo.PolecatName)
				cleanupSpawnedPolecat(newPolecatInfo, newPolecatInfo.RigName, convoyID)
			}
			restoreRollbackRawWorkflowFieldsFromCurrent(beadID, townRoot, hookWorkDir, info)
			result.ErrMsg = "raw sling metadata failed"
			return result, fmt.Errorf("storing raw sling metadata before hook: %w", err)
		}
		if delayedDogInfo != nil {
			dogDispatchDescription = storedDescription
		}
	}
	hookDir := beads.ResolveHookDir(townRoot, beadID, hookWorkDir)
	if err := hookBeadWithRetryFn(beadID, targetAgent, hookDir); err != nil {
		rollbackSpawnedPolecat("Hook failed")
		result.ErrMsg = "hook failed"
		return result, err
	}

	if targetAgent == "mayor/" {
		if root, err := workspace.FindFromCwd(); err == nil && root != "" {
			session := "hq-mayor"
			message := fmt.Sprintf("Hook updated: attached bead %s", beadID)
			_ = nudge.Enqueue(root, session, nudge.QueuedNudge{
				Sender:   "sling",
				Message:  message,
				Priority: nudge.PriorityNormal,
			})
		} else if townRoot != "" {
			_ = nudge.Enqueue(townRoot, "hq-mayor", nudge.QueuedNudge{
				Sender:   "sling",
				Message:  fmt.Sprintf("Hook updated: attached bead %s", beadID),
				Priority: nudge.PriorityNormal,
			})
		}
	}

	fmt.Printf("%s Work attached to hook (status=hooked)\n", style.Bold.Render("✓"))

	_ = events.LogFeed(events.TypeSling, actor, events.SlingPayload(beadID, targetAgent))

	if !hookSetAtomically {
		if err := updateAgentHookBead(targetAgent, beadID, hookWorkDir, beadsDir); err != nil {
			if delayedDogInfo != nil {
				result.ErrMsg = err.Error()
				return result, fmt.Errorf("updating dog agent hook: %w", err)
			}
			fmt.Printf("%s Could not update agent hook: %v\n", style.Dim.Render("Warning:"), err)
		}
	}

	storedDescription, storeErr := storeFieldsInBeadFromTownRootWithDescription(townRoot, beadID, fieldUpdates)
	if storeErr != nil {
		if delayedDogInfo != nil && attachedMoleculeID != "" {
			result.ErrMsg = storeErr.Error()
			return result, fmt.Errorf("storing dog formula attachment metadata: %w", storeErr)
		}
		if slingFieldsRequireDurableWrite(fieldUpdates) {
			rollbackSpawnedPolecat("Durable sling metadata failed")
			result.ErrMsg = "sling metadata failed"
			return result, fmt.Errorf("storing sling metadata: %w", storeErr)
		}
		fmt.Printf("%s Could not store fields in bead: %v\n", style.Dim.Render("Warning:"), storeErr)
	} else {
		if delayedDogInfo != nil {
			dogDispatchDescription = storedDescription
		}
		if intent.Args != "" {
			fmt.Printf("%s Args stored in bead (durable)\n", style.Bold.Render("✓"))
		}
		if intent.NoMerge {
			fmt.Printf("%s No-merge mode enabled (work stays on feature branch)\n", style.Bold.Render("✓"))
		}
		if intent.ReviewOnly {
			fmt.Printf("%s Review-only mode: assignee must evaluate and report back, NOT merge/commit/push\n", style.Bold.Render("⚠"))
		}
	}
	if mode != "" {
		updateAgentMode(targetAgent, mode, hookWorkDir, beadsDir)
	}

	if delayedDogInfo != nil && force && strings.HasPrefix(originalAssignee, "deacon/dogs/") && originalAssignee != targetAgent {
		if err := clearPreviousDogAssignment(townRoot, originalAssignee, beadID, info.Description); err != nil {
			result.ErrMsg = err.Error()
			return result, fmt.Errorf("clearing previous dog assignment: %w", err)
		}
		previousDogCleared = true
	}

	freshlySpawned := newPolecatInfo != nil
	if freshlySpawned {
		pane, err := newPolecatInfo.StartSession()
		if err != nil {
			rollbackSpawnedPolecat("Session failed")
			result.ErrMsg = err.Error()
			return result, fmt.Errorf("starting polecat session: %w", err)
		}
		targetPane = pane
		result.PolecatName = newPolecatInfo.PolecatName
	}

	if delayedDogInfo != nil {
		pane, err := completeBareDogDispatch(delayedDogInfo, beadID, convoyID, attachedMoleculeID, intent.Subject, intent.Args)
		if err != nil {
			result.ErrMsg = err.Error()
			return result, err
		}
		targetPane = pane
		dogDispatchComplete = true
	} else if freshlySpawned {
		// Fresh polecat already got StartupNudge from SessionManager.Start()
	} else if isSelfSling {
		fmt.Printf("%s Self-sling: work hooked, will process on next turn\n", style.Dim.Render("○"))
	} else if targetPane == "" {
		fmt.Printf("%s No pane to nudge (agent will discover work via gt prime)\n", style.Dim.Render("○"))
	} else {
		sessionName := getSessionFromPane(targetPane)
		if sessionName != "" {
			if err := ensureAgentReady(sessionName); err != nil {
				fmt.Printf("%s Could not verify agent ready: %v\n", style.Dim.Render("○"), err)
			}
		}

		if err := injectStartPrompt(targetPane, beadID, intent.Subject, intent.Args); err != nil {
			fmt.Printf("%s Could not nudge (no tmux?): %v\n", style.Dim.Render("○"), err)
			fmt.Printf("  Agent will discover work via gt prime / bd show\n")
		} else {
			fmt.Printf("%s Start prompt sent\n", style.Bold.Render("▶"))
		}
	}

	result.Success = true
	return result, nil
}
