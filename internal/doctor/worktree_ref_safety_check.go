package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/git"
)

// si-mefr, the detection half of si-d6kw.
//
// Two worktrees of one repository on one branch is a state git normally refuses.
// gt reached it anyway, and the consequences do not need an agent to act: a
// commit in either worktree appears in the other as a delta to re-commit, and
// the auto-save commits it. On 2026-07-28 that produced a 434-line deletion at
// 08:01 DURING an explicit stop-write with both agents complying, and a
// fleet-wide sweep then found keeper armed with 7767 staged line-deletions and
// coma with 6824, each aimed at a branch a live worker was on.
//
// si-d6kw closed the sling route. It did not close every route:
// WorktreeAddExistingForce still has legitimate callers, and a hand-run
// "git worktree add --force" bypasses all of it. Nothing reported either state
// until these checks. Both are cheap — no network, one git invocation per repo
// for the first and one per worktree for the second.

// armedStagingThreshold is the staged line-deletion count above which a worktree
// is reported as armed. Ordinary editing stages small deletions constantly; the
// artifacts that mattered were three and four orders of magnitude larger (434,
// 6824, 7767). A threshold that fires on normal work is a check somebody turns
// off, and a check that is off is worse than none because its silence still
// reads as reassurance.
const armedStagingThreshold = 100

// sharedRef is one branch held by more than one live worktree.
type sharedRef struct {
	Branch string
	Paths  []string
}

// sharedRefs returns the branches held by two or more LIVE worktrees.
//
// The prunable filter is load-bearing, not tidiness. Git keeps an
// administrative record for a worktree whose directory is gone, and reports it
// in "worktree list" exactly like a live one. Reaping a polecat is routine, so
// without this filter every reaped worktree reads as a second holder, the check
// cries wolf on a healthy fleet, and it gets deleted — after which there is no
// check at all. Detached worktrees are skipped for the mirror-image reason: they
// hold no branch, and grouping on an empty branch name would collapse them all
// into one fictional shared ref.
func sharedRefs(worktrees []git.Worktree) []sharedRef {
	byBranch := make(map[string][]string)
	for _, wt := range worktrees {
		if wt.Prunable || wt.Branch == "" {
			continue
		}
		byBranch[wt.Branch] = append(byBranch[wt.Branch], wt.Path)
	}

	var shared []sharedRef
	for branch, paths := range byBranch {
		if len(paths) < 2 {
			continue
		}
		sort.Strings(paths)
		shared = append(shared, sharedRef{Branch: branch, Paths: paths})
	}
	sort.Slice(shared, func(i, j int) bool { return shared[i].Branch < shared[j].Branch })
	return shared
}

// rigRepoPaths returns the bare repository of every rig under the town root,
// honouring ctx.RigName when set.
func rigRepoPaths(ctx *CheckContext) []string {
	entries, err := os.ReadDir(ctx.TownRoot)
	if err != nil {
		return nil
	}

	var repos []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if ctx.RigName != "" && entry.Name() != ctx.RigName {
			continue
		}
		rigPath := filepath.Join(ctx.TownRoot, entry.Name())
		if !isRigDir(rigPath) {
			continue
		}
		repoPath := filepath.Join(rigPath, ".repo.git")
		if info, err := os.Stat(repoPath); err == nil && info.IsDir() {
			repos = append(repos, repoPath)
		}
	}
	sort.Strings(repos)
	return repos
}

// WorktreeSharedRefCheck reports branches checked out by two or more live
// worktrees of the same repository.
type WorktreeSharedRefCheck struct {
	BaseCheck
}

// NewWorktreeSharedRefCheck creates the shared-ref check.
func NewWorktreeSharedRefCheck() *WorktreeSharedRefCheck {
	return &WorktreeSharedRefCheck{
		BaseCheck: BaseCheck{
			CheckName:        "worktree-shared-ref",
			CheckDescription: "Verify no branch is checked out by two live worktrees of one repo",
			CheckCategory:    CategoryRig,
		},
	}
}

// Run enumerates every rig repository and groups its live worktrees by branch.
func (c *WorktreeSharedRefCheck) Run(ctx *CheckContext) *CheckResult {
	var details []string
	examined := 0
	var readErrs []string

	for _, repoPath := range rigRepoPaths(ctx) {
		worktrees, err := git.NewGit(repoPath).WorktreeList()
		if err != nil {
			readErrs = append(readErrs, fmt.Sprintf("%s: %v", repoPath, err))
			continue
		}
		examined += len(worktrees)

		for _, sr := range sharedRefs(worktrees) {
			details = append(details, fmt.Sprintf("%s held by %d worktrees: %s",
				sr.Branch, len(sr.Paths), strings.Join(sr.Paths, ", ")))
		}
	}

	// A check that examined nothing has not found the fleet healthy — it has
	// found nothing, and reporting OK for that is how a broken enumeration goes
	// unnoticed indefinitely.
	if examined == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "No worktrees examined — nothing was checked",
			Details: readErrs,
			FixHint: "Expected <town>/<rig>/.repo.git for at least one rig; verify the town layout",
		}
	}

	if len(details) == 0 {
		msg := fmt.Sprintf("No shared refs across %d worktree(s)", examined)
		if len(readErrs) > 0 {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusWarning,
				Message: msg + fmt.Sprintf(", but %d repo(s) could not be read", len(readErrs)),
				Details: readErrs,
			}
		}
		return &CheckResult{Name: c.Name(), Status: StatusOK, Message: msg}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusError,
		Message: fmt.Sprintf("%d branch(es) checked out by multiple live worktrees", len(details)),
		Details: append(details, readErrs...),
		FixHint: "Move the LIVE worker to its own branch FIRST, then disarm the other worktree. " +
			"Touching the shared ref while a live worker is on it is what risks real work.",
	}
}

// WorktreeArmedStagingCheck reports worktrees whose index is staged to remove a
// large number of lines — the state a rebase under a shared ref leaves behind,
// and which the auto-save will commit without any agent acting.
type WorktreeArmedStagingCheck struct {
	BaseCheck
}

// NewWorktreeArmedStagingCheck creates the armed-staging check.
func NewWorktreeArmedStagingCheck() *WorktreeArmedStagingCheck {
	return &WorktreeArmedStagingCheck{
		BaseCheck: BaseCheck{
			CheckName:        "worktree-armed-staging",
			CheckDescription: "Verify no worktree is staged to delete a large number of lines",
			CheckCategory:    CategoryRig,
		},
	}
}

// Run measures staged line-deletions in every live worktree of every rig.
func (c *WorktreeArmedStagingCheck) Run(ctx *CheckContext) *CheckResult {
	var details []string
	examined := 0
	var readErrs []string

	for _, repoPath := range rigRepoPaths(ctx) {
		worktrees, err := git.NewGit(repoPath).WorktreeList()
		if err != nil {
			readErrs = append(readErrs, fmt.Sprintf("%s: %v", repoPath, err))
			continue
		}

		for _, wt := range worktrees {
			if wt.Prunable {
				continue
			}
			if _, err := os.Stat(wt.Path); err != nil {
				continue
			}
			examined++

			deletions, err := git.NewGit(wt.Path).StagedLineDeletions()
			if err != nil {
				readErrs = append(readErrs, fmt.Sprintf("%s: %v", wt.Path, err))
				continue
			}
			if deletions >= armedStagingThreshold {
				details = append(details, fmt.Sprintf("%s (%s): %d lines staged for deletion",
					wt.Path, wt.Branch, deletions))
			}
		}
	}

	if examined == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "No worktrees examined — nothing was checked",
			Details: readErrs,
			FixHint: "Expected <town>/<rig>/.repo.git for at least one rig; verify the town layout",
		}
	}

	if len(details) == 0 {
		msg := fmt.Sprintf("No armed worktrees across %d examined", examined)
		if len(readErrs) > 0 {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusWarning,
				Message: msg + fmt.Sprintf(", but %d could not be read", len(readErrs)),
				Details: readErrs,
			}
		}
		return &CheckResult{Name: c.Name(), Status: StatusOK, Message: msg}
	}

	sort.Strings(details)
	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusError,
		Message: fmt.Sprintf("%d worktree(s) staged to delete >=%d lines", len(details), armedStagingThreshold),
		Details: append(details, readErrs...),
		FixHint: "Inspect with 'git -C <path> diff --cached --shortstat'. If it is a rebase artifact, " +
			"'git -C <path> reset --hard HEAD' drops the staging without moving the branch.",
	}
}
