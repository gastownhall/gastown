package cmd

import (
	"context"
	"fmt"

	"github.com/jonbaldie/gastown/internal/sling"
	"github.com/jonbaldie/gastown/internal/style"
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
	result, err := executeSling(ctx, intent)
	return outcomeFromSlingResult(result), err
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
		NoOp:             result.NoOp,
	}
}

func executeDeepSling(ctx context.Context, intent sling.Intent) (*sling.Outcome, error) {
	return defaultSlingLifecycle.Execute(ctx, intent)
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
		DryRun:           slingDryRun,
		Create:           slingCreate,
		Subject:          slingSubject,
		Message:          slingMessage,
	}
}

// runRigBeadSling is the direct-command adapter: flags → Intent → Lifecycle.
func runRigBeadSling(ctx context.Context, beadID, rigName, formula, townRoot, beadsDir string) error {
	intent := intentFromCLIFlags(beadID, rigName, formula, townRoot, beadsDir)
	_, err := executeDeepSling(ctx, intent)
	return err
}
