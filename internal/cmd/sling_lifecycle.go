package cmd

import (
	"context"
	"fmt"

	"github.com/steveyegge/gastown/internal/sling"
	"github.com/steveyegge/gastown/internal/style"
)

// defaultSlingLifecycle is the single production Lifecycle. Direct and
// deferred adapters invoke this rather than coordinating sling steps.
var defaultSlingLifecycle sling.Lifecycle = townSlingLifecycle{}

// tryAcquireSlingAssigneeLockFn is a seam so failure-path tests can inject
// assignee-lock faults without reproducing lifecycle policy in the test.
var tryAcquireSlingAssigneeLockFn = tryAcquireSlingAssigneeLock

// townSlingLifecycle is the deep Slinging implementation: validation through
// Hook and Bead attachment, with compensation on every fatal failure after
// Polecat creation.
type townSlingLifecycle struct{}

func (townSlingLifecycle) Execute(ctx context.Context, intent sling.Intent) (*sling.Outcome, error) {
	_ = ctx
	result, err := executeSling(paramsFromIntent(intent))
	return outcomeFromSlingResult(result), err
}

func paramsFromIntent(intent sling.Intent) SlingParams {
	return SlingParams{
		BeadID:           intent.BeadID,
		FormulaName:      intent.Formula,
		RigName:          intent.RigName,
		Args:             intent.Args,
		Vars:             append([]string(nil), intent.Vars...),
		Merge:            intent.Merge,
		BaseBranch:       intent.BaseBranch,
		ResumeBranch:     intent.ResumeBranch,
		Account:          intent.Account,
		Agent:            intent.Agent,
		Convoy:           intent.Convoy,
		NoConvoy:         intent.NoConvoy,
		Owned:            intent.Owned,
		NoMerge:          intent.NoMerge,
		Force:            intent.Force,
		HookRawBead:      intent.HookRawBead,
		NoBoot:           intent.NoBoot,
		Mode:             intent.Mode,
		ReviewOnly:       intent.ReviewOnly,
		SkipCook:         intent.SkipCook,
		FormulaFailFatal: intent.FormulaFailFatal,
		CallerContext:    intent.CallerContext,
		TownRoot:         intent.TownRoot,
		BeadsDir:         intent.BeadsDir,
	}
}

func intentFromSlingParams(params SlingParams) sling.Intent {
	return sling.Intent{
		BeadID:           params.BeadID,
		Formula:          params.FormulaName,
		RigName:          params.RigName,
		Args:             params.Args,
		Vars:             append([]string(nil), params.Vars...),
		Merge:            params.Merge,
		Convoy:           params.Convoy,
		BaseBranch:       params.BaseBranch,
		ResumeBranch:     params.ResumeBranch,
		Account:          params.Account,
		Agent:            params.Agent,
		Mode:             params.Mode,
		NoMerge:          params.NoMerge,
		ReviewOnly:       params.ReviewOnly,
		HookRawBead:      params.HookRawBead,
		Owned:            params.Owned,
		NoConvoy:         params.NoConvoy,
		Force:            params.Force,
		NoBoot:           params.NoBoot,
		SkipCook:         params.SkipCook,
		FormulaFailFatal: params.FormulaFailFatal,
		CallerContext:    params.CallerContext,
		TownRoot:         params.TownRoot,
		BeadsDir:         params.BeadsDir,
	}
}

func outcomeFromSlingResult(result *SlingResult) *sling.Outcome {
	if result == nil {
		return nil
	}
	return &sling.Outcome{
		BeadID:           result.BeadID,
		PolecatName:      result.PolecatName,
		ConvoyID:         result.ConvoyID,
		AttachedMolecule: result.AttachedMolecule,
		Success:          result.Success,
	}
}

func executeDeepSling(ctx context.Context, intent sling.Intent) (*sling.Outcome, error) {
	return defaultSlingLifecycle.Execute(ctx, intent)
}

func executeSlingIntent(params SlingParams) (*SlingResult, error) {
	outcome, err := executeDeepSling(context.Background(), intentFromSlingParams(params))
	if outcome == nil {
		return &SlingResult{BeadID: params.BeadID, ErrMsg: errMsg(err)}, err
	}
	result := &SlingResult{
		BeadID:           outcome.BeadID,
		PolecatName:      outcome.PolecatName,
		ConvoyID:         outcome.ConvoyID,
		Success:          outcome.Success,
		AttachedMolecule: outcome.AttachedMolecule,
		ErrMsg:           errMsg(err),
	}
	return result, err
}

func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// slingCompensation is the lifecycle-owned record of artifacts this attempt
// created. Compensation removes only those artifacts and restores prior
// raw-workflow fields rather than clearing the Bead indiscriminately.
type slingCompensation struct {
	reason        string
	spawnInfo     *SpawnedPolecatInfo
	beadID        string
	hookWorkDir   string
	createdConvoy string
	townRoot      string
	originalInfo  *beadInfo
	force         bool
}

func compensateSlingAttempt(c slingCompensation) {
	if c.spawnInfo != nil {
		fmt.Printf("  %s %s, rolling back spawned polecat %s...\n", style.Warning.Render("⚠"), c.reason, c.spawnInfo.PolecatName)
		rollbackSlingArtifactsFn(c.spawnInfo, c.beadID, c.hookWorkDir, c.createdConvoy)
	}
	restoreRollbackRawWorkflowFieldsFromCurrent(c.beadID, c.townRoot, c.hookWorkDir, c.originalInfo)
	if c.force && c.originalInfo != nil && c.originalInfo.Status == "pinned" {
		restorePinnedBead(c.townRoot, c.beadID, c.originalInfo.Assignee)
	}
}

func intentFromCLIFlags(beadID, rigName, formula, townRoot, beadsDir string) sling.Intent {
	mode := ""
	if slingRalph {
		mode = "ralph"
	}
	return sling.Intent{
		BeadID:           beadID,
		RigName:          rigName,
		Formula:          formula,
		Args:             slingArgs,
		Vars:             append([]string(nil), slingVars...),
		Merge:            slingMerge,
		BaseBranch:       slingBaseBranch,
		ResumeBranch:     slingResumeBranch,
		Account:          slingAccount,
		Agent:            slingAgent,
		Mode:             mode,
		NoMerge:          slingNoMerge,
		ReviewOnly:       slingReviewOnly,
		HookRawBead:      slingHookRawBead,
		Owned:            slingOwned,
		NoConvoy:         slingNoConvoy,
		Force:            slingForce,
		NoBoot:           slingNoBoot,
		FormulaFailFatal: true,
		CallerContext:    "sling",
		TownRoot:         townRoot,
		BeadsDir:         beadsDir,
	}
}

// runRigBeadSling is the direct-command adapter: flags → Intent → Lifecycle.
func runRigBeadSling(ctx context.Context, beadID, rigName, formula, townRoot, beadsDir string) error {
	if !slingForce {
		if err := checkCrossRigGuard(beadID, rigName+"/polecats/_", townRoot); err != nil {
			return err
		}
	}
	intent := intentFromCLIFlags(beadID, rigName, formula, townRoot, beadsDir)
	_, err := executeDeepSling(ctx, intent)
	return err
}
