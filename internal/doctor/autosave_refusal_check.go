package doctor

import (
	"fmt"
	"sort"

	"github.com/steveyegge/gastown/internal/autosave"
)

// autosaveRefusalScanDepth bounds the walk. Markers land beside a worktree —
// <town>/<rig>/polecats/<name>/ is four levels — so five is generous without
// descending into repository contents.
const autosaveRefusalScanDepth = 5

// AutosaveRefusalCheck surfaces auto-saves that declined to commit.
//
// This check is the half of si-9wu1 that makes the refusal mean anything. The
// auto-save fires on gt done, at session end, when nobody is reading stdout —
// so a refusal that only prints is unobservable, and refusing silently would
// trade a silent destructive write for a silent non-save. The marker is durable
// and this is what a later reader hits without knowing to look for it.
//
// It reports StatusError rather than a warning because a refusal means real
// work is sitting uncommitted in a sandbox that nothing else will save.
type AutosaveRefusalCheck struct {
	BaseCheck
}

// NewAutosaveRefusalCheck creates the auto-save refusal check.
func NewAutosaveRefusalCheck() *AutosaveRefusalCheck {
	return &AutosaveRefusalCheck{
		BaseCheck: BaseCheck{
			CheckName:        "autosave-refusal",
			CheckDescription: "Report auto-saves that refused to commit a mass staged deletion",
			CheckCategory:    CategoryRig,
		},
	}
}

// Run scans the town for refusal markers.
func (c *AutosaveRefusalCheck) Run(ctx *CheckContext) *CheckResult {
	if ctx.TownRoot == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "No town root — nothing was scanned",
		}
	}

	markers := autosave.FindRefusals(ctx.TownRoot, autosaveRefusalScanDepth)
	if len(markers) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No refused auto-saves",
		}
	}

	details := make([]string, 0, len(markers))
	for _, m := range markers {
		details = append(details, autosave.Describe(m))
	}
	sort.Strings(details)

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusError,
		Message: fmt.Sprintf("%d worktree(s) hold work the auto-save refused to commit", len(markers)),
		Details: details,
		FixHint: "The working tree was not modified — the work is still there. Inspect with " +
			"'git -C <worktree> diff --cached --shortstat', commit deliberately or " +
			"'git -C <worktree> reset --hard HEAD', then delete the marker file.",
	}
}
