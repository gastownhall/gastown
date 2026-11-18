// Package autosave records when the gt-pvx auto-save safety net declines to
// commit, in a form that outlives the session it happened in.
//
// si-9wu1, Mayor's ruling hq-vpt06 (2026-07-30).
//
// The auto-save unstages FILE deletions before committing, on the stated policy
// that a safety net "should preserve work (additions + modifications), never
// destroy it (deletions)". That policy is the hole. A rebase under a shared ref
// leaves the delta staged as MODIFICATIONS to files that still exist, so the
// file-status query behind that guard returns empty while thousands of lines
// are staged to disappear — and the auto-save commits them. That is commit
// 504a5ed: 5 files, +26, -434, landed at 08:01:47 during an explicit stop-write
// with both agents complying.
//
// The ruling chose REFUSE over unstage-the-removal or commit-and-record, on an
// argument about LOSS rather than tidiness:
//
//	refuse           the tree is untouched and the sandbox outlives the session.
//	                 "the safety net did not fire" is not "work was destroyed".
//	unstage          on a real refactor this commits additions WITHOUT their
//	                 removals — a broken half-refactor presented as a save.
//	commit + record  the commit still LANDS, and on a shared ref landing IS the
//	                 harm: 504a5ed moving the ref is what set dag and toast
//	                 oscillating. A recorded pre-commit HEAD makes the manual
//	                 reset tidier, not unnecessary.
//
// Only one of those has "did nothing" as its failure mode.
//
// This package exists rather than living in internal/cmd because internal/cmd
// imports internal/doctor to register checks, and the doctor check that
// surfaces these markers would close that loop into an import cycle.
package autosave

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MarkerName is written BESIDE a worktree, never inside it: "git clean -f" and
// "git reset --hard" run in these worktrees routinely and would erase a record
// kept within.
const MarkerName = ".gt-autosave-refused.json"

// Threshold is the staged line-deletion count at which the auto-save declines.
//
// It must stay equal to doctor's armedStagingThreshold — a detector that
// reports a hazard the safety net cheerfully commits is worse than neither.
// TestRefusalThresholdMatchesDoctorThreshold pins them together.
//
// A plain line threshold, deliberately. The ruling offered a refinement — gate
// on whether the ref is shared, since a mass removal in a SOLO worktree is
// usually a real refactor — and left the call to the implementer. Declined:
// under REFUSE a false positive costs nothing (the tree survives, the marker
// says why, and si-n7vl now makes resuming that polecat possible), so the
// refinement buys accuracy the failure mode does not need. The ruling's own
// tiebreaker was that a safety net should be simple enough to reason about at
// 3am.
const Threshold = 100

// Refusal is the durable record of a declined auto-save.
type Refusal struct {
	RefusedAt       time.Time `json:"refused_at"`
	WorktreePath    string    `json:"worktree_path"`
	Branch          string    `json:"branch"`
	StagedDeletions int       `json:"staged_line_deletions"`
	Threshold       int       `json:"threshold"`
	Reason          string    `json:"reason"`
}

// WriteRefusal records a declined auto-save beside the worktree and returns the
// marker path.
//
// The ruling made this non-optional, and the reason is what makes "print a loud
// warning" insufficient on its own: the auto-save fires on gt done, AT SESSION
// END, when nobody is reading stdout. A refusal printed to a dying terminal is
// unobservable, and that trades a silent destructive write for a silent
// non-save — the same defect class pointing the other way. The record has to be
// something a later reader hits WITHOUT knowing to look for it, which is what
// doctor's autosave-refusal check provides.
func WriteRefusal(worktreePath, branch string, deletions int) (string, error) {
	rec := Refusal{
		RefusedAt:       time.Now(),
		WorktreePath:    worktreePath,
		Branch:          branch,
		StagedDeletions: deletions,
		Threshold:       Threshold,
		Reason: "auto-save declined: the index is staged to delete more lines than the safety net " +
			"will commit unreviewed. THE WORKING TREE WAS NOT MODIFIED — the work is still on " +
			"disk. This is the signature a rebase leaves in a worktree that shares its branch " +
			"with another worktree (si-d6kw), and it can also be a genuine large refactor. " +
			"Inspect with 'git -C <worktree> diff --cached --shortstat', then either commit it " +
			"deliberately or 'git -C <worktree> reset --hard HEAD' to drop the staging without " +
			"moving the branch. Delete this file once resolved.",
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(filepath.Dir(worktreePath), MarkerName)
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		return "", err
	}
	return path, nil
}

// FindRefusals returns every refusal marker under root, to a bounded depth.
//
// Bounded rather than a full walk because a town root contains entire
// repositories, and markers only ever land beside a worktree — at most a few
// levels down, e.g. <town>/<rig>/polecats/<name>/.
func FindRefusals(root string, maxDepth int) []string {
	var found []string

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// An unreadable subtree must not abort the scan; a partial answer
			// beats no answer for a detector.
			return nil //nolint:nilerr
		}
		if d.IsDir() {
			if pathDepth(root, path) >= maxDepth {
				return fs.SkipDir
			}
			switch d.Name() {
			case ".git", ".repo.git", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == MarkerName {
			found = append(found, path)
		}
		return nil
	})
	return found
}

// pathDepth returns how many directory levels path sits below root.
func pathDepth(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return len(strings.Split(rel, string(filepath.Separator)))
}

// Describe renders a marker for an operator, falling back to the raw path when
// the file cannot be parsed — a corrupt marker still means a refusal happened,
// and dropping it silently would be the same unobservable non-report the marker
// exists to prevent.
func Describe(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("%s (unreadable: %v)", path, err)
	}
	var rec Refusal
	if err := json.Unmarshal(data, &rec); err != nil {
		return fmt.Sprintf("%s (unparseable marker: %v)", path, err)
	}
	return fmt.Sprintf("%s (%s): %d lines staged for deletion, auto-save refused %s",
		rec.WorktreePath, rec.Branch, rec.StagedDeletions, rec.RefusedAt.Format(time.RFC3339))
}
