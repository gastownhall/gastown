package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/cli"
	"github.com/jonbaldie/gastown/internal/events"
	"github.com/jonbaldie/gastown/internal/formula"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/telemetry"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/jonbaldie/gastown/internal/workspace"
)

type wispCreateJSON struct {
	NewEpicID string `json:"new_epic_id"`
	RootID    string `json:"root_id"`
	ResultID  string `json:"result_id"`
}

func parseWispIDFromJSON(jsonOutput []byte) (string, error) {
	var result wispCreateJSON
	if err := json.Unmarshal(jsonOutput, &result); err != nil {
		return "", fmt.Errorf("parsing wisp JSON: %w (output: %s)", err, trimJSONForError(jsonOutput))
	}

	switch {
	case result.NewEpicID != "":
		return result.NewEpicID, nil
	case result.RootID != "":
		return result.RootID, nil
	case result.ResultID != "":
		return result.ResultID, nil
	default:
		return "", fmt.Errorf("wisp JSON missing id field (expected one of new_epic_id, root_id, result_id); output: %s", trimJSONForError(jsonOutput))
	}
}

func trimJSONForError(jsonOutput []byte) string {
	s := strings.TrimSpace(string(jsonOutput))
	const maxLen = 500
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

func cleanupFailedDogFormulaWisp(wispRootID, formulaWorkDir string) error {
	return closeFormulaWisp(wispRootID, formulaWorkDir, "burned: dog session start failed")
}

func cleanupStaleDogFormulaWisp(wispRootID, formulaWorkDir string) error {
	return closeFormulaWisp(wispRootID, formulaWorkDir, "burned: stale dog formula hook replaced")
}

func closeFormulaMoleculeByID(bd *beads.Beads, moleculeID, reason string) error {
	if moleculeID == "" {
		return nil
	}
	var cleanupErr error
	if _, err := forceCloseDescendants(bd, moleculeID); err != nil && !errors.Is(err, beads.ErrNotFound) {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("force-close descendants: %w", err))
	}
	if err := bd.ForceCloseWithReason(reason, moleculeID); err != nil && !errors.Is(err, beads.ErrNotFound) {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("force-close formula molecule: %w", err))
	}
	return cleanupErr
}

func closeFormulaWisp(workBeadID, formulaWorkDir, reason string) error {
	if workBeadID == "" {
		return nil
	}
	bd := beads.New(formulaWorkDir)
	workBead, err := bd.Show(workBeadID)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("resolve formula cleanup target %s: %w", workBeadID, err)
	}

	moleculeID := workBeadID
	if fields := beads.ParseAttachmentFields(workBead); fields != nil && fields.AttachedMolecule != "" {
		moleculeID = fields.AttachedMolecule
	}

	cleanupErr := closeFormulaMoleculeByID(bd, moleculeID, reason)
	if moleculeID != workBeadID {
		if err := bd.ForceCloseWithReason(reason, workBeadID); err != nil && !errors.Is(err, beads.ErrNotFound) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("force-close formula dispatch bead: %w", err))
		}
	}
	return cleanupErr
}

var cleanupFailedDogFormulaWispFn = cleanupFailedDogFormulaWisp
var cleanupStaleDogFormulaWispFn = cleanupStaleDogFormulaWisp

func cleanupDelayedDogFormulaFailure(currentErr error, delayedDogInfo *DogDispatchInfo, wispRootID, formulaWorkDir string) error {
	var cleanupErr error
	if wispRootID != "" {
		if err := cleanupFailedDogFormulaWispFn(wispRootID, formulaWorkDir); err != nil {
			cleanupErr = fmt.Errorf("cleaning failed dog formula wisp %s: %w", wispRootID, err)
		}
	}
	// Keep typed dog state authoritative if source cleanup failed. Returning the
	// dog to the pool while its wisp remains hooked would create split ownership.
	if cleanupErr == nil {
		if err := delayedDogInfo.clearWorkIfMatches(); err != nil {
			cleanupErr = fmt.Errorf("clearing failed dog assignment: %w", err)
		}
	}
	if cleanupErr == nil {
		return currentErr
	}
	if currentErr == nil {
		return cleanupErr
	}
	return errors.Join(currentErr, cleanupErr)
}

func formulaSlingPrompt(formulaName string) string {
	if slingArgs != "" {
		return fmt.Sprintf("Formula %s slung. Args: %s. Run `"+cli.Name()+" hook` to see your hook, then execute using these args.", formulaName, slingArgs)
	}
	return fmt.Sprintf("Formula %s slung. Run `"+cli.Name()+" hook` to see your hook, then execute the steps.", formulaName)
}

func nudgeFormulaDog(delayedDogInfo *DogDispatchInfo, prompt string) error {
	dogSession := fmt.Sprintf("hq-dog-%s", delayedDogInfo.DogName)
	t := tmux.NewTmux()
	if err := t.NudgeSession(dogSession, prompt); err != nil {
		return fmt.Errorf("nudging dog %s: %w", delayedDogInfo.DogName, err)
	}
	fmt.Printf("%s Nudged dog %s\n", style.Bold.Render("▶"), delayedDogInfo.DogName)
	return nil
}

// findHookedFormulaSingleton returns the existing hooked bead for an assignee
// when that bead already carries the same attached_formula metadata.
func findHookedFormulaSingleton(workDir, targetAgent, formulaName string) (*beads.Issue, error) {
	if workDir == "" || targetAgent == "" || formulaName == "" {
		return nil, nil
	}

	b := beads.New(workDir)
	hookedBeads, err := listAssignedActiveWorkAcrossStatuses(b, targetAgent)
	if err != nil {
		return nil, err
	}

	return newestHookedFormula(hookedBeads, formulaName), nil
}

func newestHookedFormula(hookedBeads []*beads.Issue, formulaName string) *beads.Issue {
	var newest *beads.Issue
	var newestAt time.Time
	var newestHasAt bool
	for _, bead := range hookedBeads {
		fields := beads.ParseAttachmentFields(bead)
		if fields == nil || fields.AttachedFormula != formulaName {
			continue
		}
		attachedAt, hasAttachedAt := attachmentTime(fields)
		if newerAttachment(newest == nil, attachedAt, hasAttachedAt, newestAt, newestHasAt) {
			newest = bead
			newestAt = attachedAt
			newestHasAt = hasAttachedAt
		}
	}
	return newest
}

var findHookedFormulaSingletonFn = findHookedFormulaSingleton

func findHookedFormulaForDogPool(workDir, formulaName string, reusableDog func(*beads.Issue, string) bool) (*beads.Issue, string, error) {
	if workDir == "" || formulaName == "" {
		return nil, "", nil
	}

	b := beads.New(workDir)
	var hookedBeads []*beads.Issue
	for _, status := range activeWorkStatuses() {
		active, err := listBeadsAcrossTables(b, beads.ListOptions{
			Status:   status,
			Priority: -1,
			Limit:    0,
		})
		if err != nil {
			return nil, "", err
		}
		hookedBeads = append(hookedBeads, active...)
	}
	hookedBeads = mergeBeadLists(hookedBeads, nil)

	bead, dogName := reusableHookedDogFormula(hookedBeads, formulaName, reusableDog)
	return bead, dogName, nil
}

func reusableHookedDogFormula(hookedBeads []*beads.Issue, formulaName string, reusableDog func(*beads.Issue, string) bool) (*beads.Issue, string) {
	const dogAssigneePrefix = "deacon/dogs/"
	var newest *beads.Issue
	var newestDogName string
	var newestAt time.Time
	var newestHasAt bool
	for _, bead := range hookedBeads {
		if !strings.HasPrefix(bead.Assignee, dogAssigneePrefix) {
			continue
		}
		dogName := strings.TrimPrefix(bead.Assignee, dogAssigneePrefix)
		if dogName == "" || strings.Contains(dogName, "/") {
			continue
		}
		fields := beads.ParseAttachmentFields(bead)
		if fields == nil || fields.AttachedFormula != formulaName {
			continue
		}
		if reusableDog != nil && !reusableDog(bead, dogName) {
			continue
		}
		attachedAt, hasAttachedAt := attachmentTime(fields)
		if newerAttachment(newest == nil, attachedAt, hasAttachedAt, newestAt, newestHasAt) {
			newest = bead
			newestDogName = dogName
			newestAt = attachedAt
			newestHasAt = hasAttachedAt
		}
	}

	return newest, newestDogName
}

func attachmentTime(fields *beads.AttachmentFields) (time.Time, bool) {
	if fields == nil || fields.AttachedAt == "" {
		return time.Time{}, false
	}
	attachedAt, err := time.Parse(time.RFC3339Nano, fields.AttachedAt)
	if err != nil {
		return time.Time{}, false
	}
	return attachedAt, true
}

func newerAttachment(noCurrent bool, candidate time.Time, candidateOK bool, current time.Time, currentOK bool) bool {
	if noCurrent {
		return true
	}
	if candidateOK != currentOK {
		return candidateOK
	}
	return candidateOK && candidate.After(current)
}

func isDurableFormulaDispatch(issue *beads.Issue) bool {
	return issue != nil && !issue.Ephemeral && beads.HasLabel(issue, formulaDispatchLabel)
}

func isLegacyFormulaWisp(issue *beads.Issue) bool {
	return issue != nil && !isDurableFormulaDispatch(issue)
}

func formulaMoleculeID(issue *beads.Issue) string {
	if issue == nil {
		return ""
	}
	if fields := beads.ParseAttachmentFields(issue); fields != nil && fields.AttachedMolecule != "" {
		return fields.AttachedMolecule
	}
	return issue.ID
}

func shouldReuseExistingFormula(existing *beads.Issue, delayedDogInfo *DogDispatchInfo, force bool) bool {
	if existing == nil || force || !isDurableFormulaDispatch(existing) {
		return false
	}
	if delayedDogInfo == nil {
		return true
	}
	if delayedDogInfo.ownsWork {
		return false
	}
	return delayedDogInfo.worksOnHook(existing)
}

func rollbackIncompleteFormulaSling(dispatchBeadID, wispRootID, formulaWorkDir, reason string) error {
	var cleanupErr error
	if dispatchBeadID != "" {
		if err := closeFormulaWisp(dispatchBeadID, formulaWorkDir, reason); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if wispRootID != "" && wispRootID != dispatchBeadID {
		if err := closeFormulaMoleculeByID(beads.New(formulaWorkDir), wispRootID, reason); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

func unhookFormulaBead(beadID, formulaWorkDir, townRoot string) error {
	if beadID == "" {
		return nil
	}
	hookDir := beads.ResolveHookDir(townRoot, beadID, formulaWorkDir)
	if err := BdCmd("update", beadID, "--status=open", "--assignee=").
		Dir(hookDir).
		WithAutoCommit().
		Run(); err != nil {
		return fmt.Errorf("unhooking legacy formula wisp %s: %w", beadID, err)
	}
	return nil
}

func formulaDispatchFieldUpdates(formulaName, moleculeID, mode string) beadFieldUpdates {
	return beadFieldUpdates{
		Dispatcher:       detectActor(),
		Args:             slingArgs,
		Vars:             append([]string(nil), slingVars...),
		AttachedMolecule: moleculeID,
		AttachedFormula:  formulaName,
		Mode:             &mode,
		FormulaVars:      strings.Join(slingVars, "\n"),
	}
}

func persistAndHookFormulaDispatch(townRoot, formulaWorkDir, dispatchBeadID, targetAgent, formulaName, moleculeID, mode string) error {
	if err := storeFieldsInBeadFromTownRoot(townRoot, dispatchBeadID, formulaDispatchFieldUpdates(formulaName, moleculeID, mode)); err != nil {
		return fmt.Errorf("storing formula dispatch metadata before hook: %w", err)
	}
	hookDir := beads.ResolveHookDir(townRoot, dispatchBeadID, formulaWorkDir)
	if err := hookBeadWithRetryFn(dispatchBeadID, targetAgent, hookDir); err != nil {
		return err
	}
	fmt.Printf("%s Attached durable work to hook (status=hooked)\n", style.Bold.Render("✓"))
	if slingArgs != "" {
		fmt.Printf("%s Args stored in bead (durable)\n", style.Bold.Render("✓"))
	}
	payload := events.SlingPayload(dispatchBeadID, targetAgent)
	payload["formula"] = formulaName
	payload["molecule"] = moleculeID
	_ = events.LogFeed(events.TypeSling, detectActor(), payload)
	return nil
}

func migrateLegacyFormulaDispatch(existing *beads.Issue, formulaName, formulaWorkDir, townRoot, targetAgent, mode string) (string, string, error) {
	moleculeID := formulaMoleculeID(existing)
	dispatchBead, err := createFormulaDispatchBead(formulaName, formulaWorkDir)
	if err != nil {
		return "", moleculeID, err
	}
	fmt.Printf("%s Durable dispatch bead created: %s\n", style.Bold.Render("✓"), dispatchBead.ID)
	if err := persistAndHookFormulaDispatch(townRoot, formulaWorkDir, dispatchBead.ID, targetAgent, formulaName, moleculeID, mode); err != nil {
		return dispatchBead.ID, moleculeID, err
	}
	if err := unhookFormulaBead(existing.ID, formulaWorkDir, townRoot); err != nil {
		return dispatchBead.ID, moleculeID, err
	}
	fmt.Printf("%s Migrated legacy formula wisp %s onto durable dispatch %s\n",
		style.Bold.Render("✓"), existing.ID, dispatchBead.ID)
	return dispatchBead.ID, moleculeID, nil
}

// verifyFormulaExists checks that the formula exists using bd formula show.
// Formulas are TOML files (.formula.toml).
// Requests stale-read compatibility for consistency with verifyBeadExists.
func verifyFormulaExists(formulaName, workDir, townRoot string) error {
	if workDir == "" {
		workDir = townRoot
	}
	// Try bd formula show (handles all formula file formats)
	// Use Output() instead of Run() to detect bd exit 0 bug:
	// when formula not found, bd may exit 0 but produce empty stdout.
	// Stderr discarded — first attempt may fail expectedly (retry with mol- prefix).
	if out, err := BdCmd("formula", "show", formulaName).
		AllowStale().
		Dir(workDir).
		WithGTRoot(townRoot).
		Stderr(io.Discard).Output(); err == nil && len(out) > 0 {
		return nil
	}

	// Try with mol- prefix
	if out, err := BdCmd("formula", "show", "mol-"+formulaName).
		AllowStale().
		Dir(workDir).
		WithGTRoot(townRoot).
		Stderr(io.Discard).Output(); err == nil && len(out) > 0 {
		return nil
	}
	if _, err := formula.GetEmbeddedFormulaContent(formulaName); err == nil {
		return nil
	}
	if _, err := formula.GetEmbeddedFormulaContent("mol-" + formulaName); err == nil {
		return nil
	}

	return fmt.Errorf("formula '%s' not found (check 'bd formula list')", formulaName)
}

const formulaDispatchLabel = "gt:formula-dispatch"

func createFormulaDispatchBead(formulaName, formulaWorkDir string) (*beads.Issue, error) {
	issue, err := beads.New(formulaWorkDir).Create(beads.CreateOptions{
		Title:    formulaName,
		Labels:   []string{"gt:task", formulaDispatchLabel},
		Priority: 2,
		Actor:    detectActor(),
	})
	if err != nil {
		return nil, fmt.Errorf("creating durable formula dispatch bead: %w", err)
	}
	if issue == nil || issue.ID == "" {
		return nil, fmt.Errorf("creating durable formula dispatch bead: bd returned no issue ID")
	}
	return issue, nil
}

// runSlingFormula handles standalone formula slinging.
// Flow: cook → wisp → durable dispatch bead → attach dispatch bead to hook → nudge
func runSlingFormula(ctx context.Context, args []string) (err error) {
	formulaName := args[0]

	// Get town root early - needed for BEADS_DIR when running bd commands
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}
	townBeadsDir := filepath.Join(townRoot, ".beads")

	// Resolve target using shared dispatch logic
	var target string
	if len(args) > 1 {
		target = args[1]
	}
	var admission *polecatAdmissionHandle
	if !slingDryRun && target != "" {
		admissionRig := ""
		if rigName, isRig := IsRigName(target); isRig {
			admissionRig = rigName
		}
		if admissionRig != "" {
			admission, _, err = acquirePolecatAdmissionFn(townRoot, admissionRig, formulaName, "formula")
			if err != nil {
				return err
			}
			defer admission.Release()
		}
	}
	if !slingDryRun {
		if dogName, isDog := IsDogTarget(target); isDog && dogName == "" {
			poolUnlock, poolLockErr := tryAcquireSlingAssigneeLock(townRoot, "deacon/dogs")
			if poolLockErr != nil {
				return fmt.Errorf("serializing dog-pool formula sling for %s: %w", formulaName, poolLockErr)
			}
			defer poolUnlock()
		}
	}
	resolved, err := resolveTarget(target, ResolveTargetOptions{
		DryRun:               slingDryRun,
		Force:                slingForce,
		Create:               slingCreate,
		Account:              slingAccount,
		Agent:                slingAgent,
		NoBoot:               slingNoBoot,
		WorkDesc:             formulaName,
		TownRoot:             townRoot,
		SkipPolecatAdmission: admission != nil,
	})
	if err != nil {
		return err
	}
	targetAgent := resolved.Agent
	targetPane := resolved.Pane
	formulaWorkDir := resolved.WorkDir
	delayedDogInfo := resolved.DelayedDogInfo
	isSelfSling := resolved.IsSelfSling

	fmt.Printf("%s Slinging formula %s to %s...\n", style.Bold.Render("🎯"), formulaName, targetAgent)

	rollbackSpawned := func(beadID string) {
		if resolved.NewPolecatInfo == nil {
			return
		}
		fmt.Printf("%s Rolling back spawned polecat %s...\n", style.Warning.Render("⚠"), resolved.NewPolecatInfo.PolecatName)
		rollbackSlingArtifactsFn(resolved.NewPolecatInfo, beadID, formulaWorkDir, "")
	}

	// Resolve working directory for bd commands (routes to correct rig beads)
	// Fall back to townRoot (HQ beads) if no specific rig directory was determined
	if formulaWorkDir == "" {
		formulaWorkDir = townRoot
	}

	var wispRootID string
	var dispatchBeadID string
	formulaWorkComplete := false

	if slingDryRun {
		existing, err := findHookedFormulaSingletonFn(formulaWorkDir, targetAgent, formulaName)
		if err != nil {
			return fmt.Errorf("checking existing hooked formulas for %s: %w", targetAgent, err)
		}
		if existing != nil && !slingForce && isDurableFormulaDispatch(existing) {
			fmt.Printf("Would reuse existing formula %s on %s via %s\n", formulaName, targetAgent, existing.ID)
			return nil
		}

		fmt.Printf("Would cook formula: %s\n", formulaName)
		fmt.Printf("Would create wisp and pin to: %s\n", targetAgent)
		for _, v := range slingVars {
			fmt.Printf("  --var %s\n", v)
		}
		fmt.Printf("Would nudge pane: %s\n", targetPane)
		return nil
	}

	delayedDogComplete := false
	// Serialize standalone formula slings per assignee so same-formula retries
	// and handoffs cannot create duplicate hooked wisps for one target.
	assigneeUnlock, assigneeLockErr := tryAcquireSlingAssigneeLock(townRoot, targetAgent)
	if assigneeLockErr != nil {
		lockErr := fmt.Errorf("serializing formula sling for %s: %w", targetAgent, assigneeLockErr)
		if delayedDogInfo == nil {
			return lockErr
		}
		if clearErr := delayedDogInfo.clearWorkIfMatches(); clearErr != nil {
			return errors.Join(lockErr, fmt.Errorf("clearing failed dog assignment: %w", clearErr))
		}
		return lockErr
	}
	defer assigneeUnlock()
	defer func() {
		cleanupID := dispatchBeadID
		if cleanupID == "" {
			cleanupID = wispRootID
		}
		reason := "burned: formula sling failed"
		if delayedDogInfo != nil && !delayedDogComplete {
			reason = "burned: dog formula sling failed"
			if err != nil || cleanupID != "" {
				err = cleanupDelayedDogFormulaFailure(err, delayedDogInfo, cleanupID, formulaWorkDir)
			}
			err = errors.Join(err, rollbackIncompleteFormulaSling(dispatchBeadID, wispRootID, formulaWorkDir, reason))
			return
		}
		if err != nil && !formulaWorkComplete && cleanupID != "" {
			err = errors.Join(err, rollbackIncompleteFormulaSling(dispatchBeadID, wispRootID, formulaWorkDir, reason))
		}
	}()
	mode := ""
	if slingRalph {
		mode = "ralph"
	}

	existing, err := findHookedFormulaSingletonFn(formulaWorkDir, targetAgent, formulaName)
	if err != nil {
		return fmt.Errorf("checking existing hooked formulas for %s: %w", targetAgent, err)
	}
	if shouldReuseExistingFormula(existing, delayedDogInfo, slingForce) {
		existingMode := ""
		if fields := beads.ParseAttachmentFields(existing); fields != nil {
			existingMode = fields.Mode
		}
		if existingMode != mode {
			if err := storeFieldsInBeadFromTownRoot(townRoot, existing.ID, beadFieldUpdates{Mode: &mode}); err != nil {
				return fmt.Errorf("updating existing formula mode: %w", err)
			}
			if mode != "" || existingMode != "" {
				updateAgentMode(targetAgent, mode, "", townBeadsDir)
			}
		}
		fmt.Printf("%s Formula %s already hooked to %s via %s, no-op\n",
			style.Dim.Render("○"), formulaName, targetAgent, existing.ID)
		if delayedDogInfo != nil {
			if _, err := delayedDogInfo.completeFormulaStartup(existing.ID); err != nil {
				return fmt.Errorf("completing existing dog formula dispatch: %w", err)
			}
			if os.Getenv("GT_TEST_NO_NUDGE") == "" {
				if err := nudgeFormulaDog(delayedDogInfo, formulaSlingPrompt(formulaName)); err != nil {
					return err
				}
			}
			delayedDogComplete = true
		}
		return nil
	}
	if delayedDogInfo != nil && !delayedDogInfo.ownsWork && !delayedDogInfo.worksOnHook(existing) {
		return fmt.Errorf("dog formula reuse became stale before hook verification; retry dispatch")
	}
	if existing != nil && !slingForce && delayedDogInfo != nil && delayedDogInfo.ownsWork {
		if err := cleanupStaleDogFormulaWispFn(existing.ID, formulaWorkDir); err != nil {
			return fmt.Errorf("cleaning stale dog formula wisp %s: %w", existing.ID, err)
		}
	} else if isLegacyFormulaWisp(existing) && !slingForce {
		dispatchBeadID, wispRootID, err = migrateLegacyFormulaDispatch(existing, formulaName, formulaWorkDir, townRoot, targetAgent, mode)
		if err != nil {
			rollbackSpawned(dispatchBeadID)
			return err
		}
		return finishFormulaSling(resolved, delayedDogInfo, &delayedDogComplete, &formulaWorkComplete, townBeadsDir, formulaName, dispatchBeadID, targetAgent, &targetPane, isSelfSling, mode)
	}
	if admission == nil && strings.Contains(targetAgent, "/polecats/") {
		parts := strings.Split(targetAgent, "/")
		if len(parts) >= 3 {
			admission, _, err = acquirePolecatAdmissionFn(townRoot, parts[0], formulaName, "formula")
			if err != nil {
				return err
			}
			defer admission.Release()
		}
	}

	// Step 1: Cook the formula (ensures proto exists)
	fmt.Printf("  Cooking formula...\n")
	if err := BdCmd("cook", formulaName).
		Dir(formulaWorkDir).
		WithGTRoot(townRoot).
		Run(); err != nil {
		telemetry.RecordMolCook(ctx, formulaName, err)
		rollbackSpawned("")
		return fmt.Errorf("cooking formula: %w", err)
	}
	telemetry.RecordMolCook(ctx, formulaName, nil)

	// Step 2: Create wisp instance (ephemeral)
	fmt.Printf("  Creating wisp...\n")
	wispArgs := []string{"mol", "wisp", formulaName}
	for _, v := range slingVars {
		wispArgs = append(wispArgs, "--var", v)
	}
	wispArgs = append(wispArgs, "--json")

	wispOut, err := BdCmd(wispArgs...).
		Dir(formulaWorkDir).
		WithAutoCommit().
		WithGTRoot(townRoot).
		Output()
	if err != nil {
		rollbackSpawned("")
		return fmt.Errorf("creating wisp: %w", err)
	}

	// Parse wisp output to get the root ID
	wispRootID, err = parseWispIDFromJSON(wispOut)
	if err != nil {
		telemetry.RecordMolWisp(ctx, formulaName, "", "", err)
		rollbackSpawned("")
		return fmt.Errorf("parsing wisp output: %w", err)
	}
	telemetry.RecordMolWisp(ctx, formulaName, wispRootID, "", nil)

	fmt.Printf("%s Wisp created: %s\n", style.Bold.Render("✓"), wispRootID)

	// Work dispatch must live in the durable issues table. Wisps intentionally
	// remain outside Dolt history, so hooking the wisp directly strands the
	// assignment outside refs/dolt/data and makes remote agents see an empty hook.
	dispatchBead, err := createFormulaDispatchBead(formulaName, formulaWorkDir)
	if err != nil {
		return err
	}
	dispatchBeadID = dispatchBead.ID
	fmt.Printf("%s Durable dispatch bead created: %s\n", style.Bold.Render("✓"), dispatchBeadID)

	if delayedDogInfo != nil {
		if err := delayedDogInfo.persistWorkSource(dispatchBeadID); err != nil {
			return fmt.Errorf("recording dog formula source: %w", err)
		}
	}

	if err := persistAndHookFormulaDispatch(townRoot, formulaWorkDir, dispatchBeadID, targetAgent, formulaName, wispRootID, mode); err != nil {
		return err
	}
	return finishFormulaSling(resolved, delayedDogInfo, &delayedDogComplete, &formulaWorkComplete, townBeadsDir, formulaName, dispatchBeadID, targetAgent, &targetPane, isSelfSling, mode)
}

func finishFormulaSling(resolved *ResolvedTarget, delayedDogInfo *DogDispatchInfo, delayedDogComplete, formulaWorkComplete *bool, townBeadsDir, formulaName, dispatchBeadID, targetAgent string, targetPane *string, isSelfSling bool, mode string) error {
	if err := updateAgentHookBead(targetAgent, dispatchBeadID, "", townBeadsDir); err != nil {
		if delayedDogInfo != nil {
			return fmt.Errorf("updating dog agent hook: %w", err)
		}
		fmt.Printf("%s Could not update agent hook: %v\n", style.Dim.Render("Warning:"), err)
	}
	if mode != "" {
		updateAgentMode(targetAgent, mode, "", townBeadsDir)
	}

	if delayedDogInfo != nil {
		pane, err := delayedDogInfo.completeFormulaStartup(dispatchBeadID)
		if err != nil {
			return fmt.Errorf("completing dog formula dispatch: %w", err)
		}
		*targetPane = pane
	}

	if resolved.NewPolecatInfo != nil {
		pane, err := resolved.NewPolecatInfo.StartSession()
		if err != nil {
			rollbackSlingArtifactsFn(resolved.NewPolecatInfo, dispatchBeadID, "", "")
			return fmt.Errorf("starting polecat session: %w", err)
		}
		*targetPane = pane
	}

	*formulaWorkComplete = true

	if isSelfSling {
		fmt.Printf("%s Self-sling: work hooked, will process on next turn\n", style.Dim.Render("○"))
		return nil
	}

	if os.Getenv("GT_TEST_NO_NUDGE") != "" {
		if delayedDogInfo != nil {
			*delayedDogComplete = true
		}
		return nil
	}

	prompt := formulaSlingPrompt(formulaName)

	// Dog sessions need a nudge sent to their session (not to the bare pane ID
	// from StartDelayedSession, which is ambiguous on platforms where tmux pane
	// IDs are not globally unique). Use NudgeSession which qualifies the target
	// with the session name. (gt-etc)
	if delayedDogInfo != nil {
		if err := nudgeFormulaDog(delayedDogInfo, prompt); err != nil {
			return err
		}
		*delayedDogComplete = true
		return nil
	}

	if targetPane == nil || *targetPane == "" {
		fmt.Printf("%s No pane to nudge (agent will discover work via gt prime)\n", style.Dim.Render("○"))
		return nil
	}

	t := tmux.NewTmux()
	if err := t.NudgePane(*targetPane, prompt); err != nil {
		fmt.Printf("%s Could not nudge (no tmux?): %v\n", style.Dim.Render("○"), err)
		fmt.Printf("  Agent will discover work via gt prime / bd show\n")
	} else {
		fmt.Printf("%s Nudged to start\n", style.Bold.Render("▶"))
	}

	return nil
}
