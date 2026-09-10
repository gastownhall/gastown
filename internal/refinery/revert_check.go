package refinery

import (
	"fmt"
	"os/exec"
	"strings"
)

// DefaultRevertCheckDepth bounds how far back the per-file history walk goes.
//
// The walk is over commits that TOUCH the file, not all commits, so 80 is deep
// for an ordinary file. It is still a bound: a branch reverting something that
// landed further back than this window will not be seen. Raise it, or walk to
// the merge-base, if that matters for a given repo.
const DefaultRevertCheckDepth = 80

// RevertFinding is one file a branch would revert.
type RevertFinding struct {
	// Path is the file, relative to the repo root.
	Path string
	// BranchBlob is the blob the branch carries for Path.
	BranchBlob string
	// MainBlob is the blob the target branch currently carries for Path.
	MainBlob string
	// SupersededCommit is the commit on the target branch whose version of Path
	// is byte-identical to BranchBlob. The target has since moved past it.
	SupersededCommit string
	// SupersededSubject is that commit's subject line, for the halt message.
	SupersededSubject string
}

func (f RevertFinding) String() string {
	return fmt.Sprintf(
		"%s: branch blob %s is the superseded version from %s (%s); target is now at %s, so merging would undo that",
		f.Path, short(f.BranchBlob), short(f.SupersededCommit), f.SupersededSubject, short(f.MainBlob))
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// CheckForReverts reports files where merging branchRef into mainRef would
// silently revert work that already landed on mainRef.
//
// WHY A TEST SUITE CANNOT DO THIS. On 2026-09-09 in the alphaprime rig, guzzle's
// ap-s1wu undid dust's ap-w0qt about 40 seconds after it landed. Nothing
// conflicted, because guzzle's commit had dust's merge as its DIRECT PARENT, so
// to git it was an ordinary edit with nothing to three-way against. The pytest
// gate saw nothing either: a reverted-to-older test file is still a valid test
// file that passes. "This merge undid a landed change" is a property of the
// HISTORY, not of the tree, so no green suite can answer it.
//
// THE RULE. For each file the branch changes: if the branch's blob equals an
// OLDER blob of that file on the target (one the target has already moved past),
// the merge would revert landed work. This keys on a fact that cannot be true of
// ordinary forward work -- this exact content is a version the target already
// superseded -- and it needs neither a merge nor conflict markers.
//
// DISPOSITION IS A HALT, NOT A REJECT. A deliberate revert is legitimate work.
// A flagged branch should stop for human judgement rather than be refused.
//
// STATED LIMIT. This catches a branch whose file content matches a superseded
// version byte for byte. It will NOT catch a stale copy that also picked up
// unrelated edits, because such a blob matches no historical blob. It closes
// this exact signature, not the whole staleness class.
func CheckForReverts(repoDir, mainRef, branchRef string, depth int) ([]RevertFinding, error) {
	if depth <= 0 {
		depth = DefaultRevertCheckDepth
	}
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}

	// Files the branch adds, copies, modifies or renames relative to the merge
	// base. Deletions cannot carry a superseded blob, so they are out of scope.
	changed, err := git("diff", "--name-only", "--diff-filter=ACMR", mainRef+"..."+branchRef)
	if err != nil {
		return nil, fmt.Errorf("listing changed files between %s and %s: %w", mainRef, branchRef, err)
	}

	var findings []RevertFinding
	for _, path := range strings.Split(changed, "\n") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		branchBlob, err := git("rev-parse", branchRef+":"+path)
		if err != nil {
			continue // not present on the branch side; nothing to compare
		}
		mainBlob, err := git("rev-parse", mainRef+":"+path)
		if err != nil {
			continue // new file on the branch; it cannot revert anything
		}
		if branchBlob == mainBlob {
			continue // identical to the target; not a change at all
		}

		history, err := git("rev-list", fmt.Sprintf("-n%d", depth), mainRef, "--", path)
		if err != nil {
			continue
		}

		// Walk the target's history for this file, newest first. Only versions
		// STRICTLY OLDER than the target's current one count: we must pass the
		// current blob before a match means "already superseded".
		seenCurrent := false
		for _, commit := range strings.Split(history, "\n") {
			commit = strings.TrimSpace(commit)
			if commit == "" {
				continue
			}
			histBlob, err := git("rev-parse", commit+":"+path)
			if err != nil {
				continue
			}
			if histBlob == mainBlob {
				seenCurrent = true
				continue
			}
			if seenCurrent && histBlob == branchBlob {
				subject, _ := git("log", "-1", "--format=%s", commit)
				findings = append(findings, RevertFinding{
					Path:              path,
					BranchBlob:        branchBlob,
					MainBlob:          mainBlob,
					SupersededCommit:  commit,
					SupersededSubject: subject,
				})
				break
			}
		}
	}
	return findings, nil
}
