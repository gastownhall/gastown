package formula

import (
	"strings"
	"testing"
)

// TestRefineryPatrolHasMergedPRSweep verifies that the refinery patrol formula
// includes a merged-pr-sweep step that detects PRs merged outside the refinery
// flow (owner-initiated GitHub merges).
//
// In repos with a no-self-merge rule, the repo owner merges every PR by hand.
// An owner-initiated merge fires no refinery gate event, so without a per-cycle
// sweep the MR bead stays open and the polecat sits PENDING_MR indefinitely
// (PR 788 sat 2h+ until a manual witness nudge).
//
// Regression test for op-3d5o.
func TestRefineryPatrolHasMergedPRSweep(t *testing.T) {
	content, err := formulasFS.ReadFile("formulas/mol-refinery-patrol.formula.toml")
	if err != nil {
		t.Fatalf("reading refinery patrol formula: %v", err)
	}

	f, err := Parse(content)
	if err != nil {
		t.Fatalf("parsing refinery patrol formula: %v", err)
	}

	var sweep, processBranch *Step
	for i := range f.Steps {
		switch f.Steps[i].ID {
		case "merged-pr-sweep":
			sweep = &f.Steps[i]
		case "process-branch":
			processBranch = &f.Steps[i]
		}
	}

	if sweep == nil {
		t.Fatal("refinery patrol formula must have a \"merged-pr-sweep\" step " +
			"(only signal for owner-initiated GitHub merges; see op-3d5o)")
	}
	if processBranch == nil {
		t.Fatal("refinery patrol formula must have a \"process-branch\" step")
	}

	// DAG position: the sweep runs after queue-scan (needs the MR list) and
	// before per-branch processing (drained MRs must not be re-processed).
	if !containsDep(sweep.Needs, "queue-scan") {
		t.Errorf("merged-pr-sweep must depend on \"queue-scan\", got needs=%v", sweep.Needs)
	}
	if !containsDep(processBranch.Needs, "merged-pr-sweep") {
		t.Errorf("process-branch must depend on \"merged-pr-sweep\" so drained MRs "+
			"are removed from the queue before rebase, got needs=%v", processBranch.Needs)
	}

	// The sweep must use the cheap PR-state check and run the full
	// post-merge drain on MERGED.
	requiredPatterns := []string{
		"gh pr view",             // one PR-state lookup per open MR
		"state,mergedAt",         // the cheap JSON fields
		"gt mq post-merge",       // closes MR bead + source issue
		"--skip-branch-delete",   // GitHub owns branch cleanup in PR mode
		"MERGED mail to witness", // releases the polecat worktree
	}
	for _, pattern := range requiredPatterns {
		if !strings.Contains(sweep.Description, pattern) {
			t.Errorf("merged-pr-sweep step missing required pattern %q\n"+
				"The sweep must check PR state cheaply and run the full "+
				"post-merge drain (witness mail + gt mq post-merge) on MERGED.",
				pattern)
		}
	}

	// CLOSED-without-merge must NOT be drained — the work didn't land.
	if !strings.Contains(sweep.Description, "CLOSED") {
		t.Error("merged-pr-sweep step must distinguish CLOSED (not merged) PRs " +
			"from MERGED ones — only MERGED PRs get the post-merge drain")
	}
}

// TestRefineryPatrolSweepRunsInAbbreviatedMode verifies the merged-pr-sweep
// step is explicitly included in the abbreviated (EFFORT: reduced) patrol
// rules. Idle rigs run abbreviated patrols almost exclusively, and an
// owner-initiated merge is precisely the event that arrives while the
// refinery is idle — if reduced cycles skip the sweep, the fix is dead code
// exactly when it is needed.
//
// Regression test for op-3d5o.
func TestRefineryPatrolSweepRunsInAbbreviatedMode(t *testing.T) {
	content, err := formulasFS.ReadFile("formulas/mol-refinery-patrol.formula.toml")
	if err != nil {
		t.Fatalf("reading refinery patrol formula: %v", err)
	}

	f, err := Parse(content)
	if err != nil {
		t.Fatalf("parsing refinery patrol formula: %v", err)
	}

	// The root description holds the "Abbreviated Patrol Mode" rules.
	if !strings.Contains(f.Description, "merged-pr-sweep") {
		t.Error("formula root description's Abbreviated Patrol rules must include " +
			"merged-pr-sweep (ALWAYS run when open MRs exist)")
	}

	// burn-or-loop repeats the EFFORT: reduced step list for the next cycle.
	var loopDesc string
	for _, step := range f.Steps {
		if step.ID == "burn-or-loop" {
			loopDesc = step.Description
			break
		}
	}
	if loopDesc == "" {
		t.Fatal("burn-or-loop step not found or has empty description")
	}
	if !strings.Contains(loopDesc, "merged-pr-sweep") {
		t.Error("burn-or-loop's EFFORT: reduced rules must include merged-pr-sweep " +
			"so abbreviated cycles still detect owner-initiated merges")
	}
}

// TestRefineryQueueScanGuardsOwnerMergedBranches verifies that queue-scan's
// "branch no longer exists" path checks for a merged PR before closing the
// MR bead. GitHub deletes the head branch when the owner merges a PR, so a
// bare close there would skip the post-merge drain: the source issue stays
// open and the witness never learns the work landed.
//
// Regression test for op-3d5o.
func TestRefineryQueueScanGuardsOwnerMergedBranches(t *testing.T) {
	content, err := formulasFS.ReadFile("formulas/mol-refinery-patrol.formula.toml")
	if err != nil {
		t.Fatalf("reading refinery patrol formula: %v", err)
	}

	f, err := Parse(content)
	if err != nil {
		t.Fatalf("parsing refinery patrol formula: %v", err)
	}

	var queueScanDesc string
	for _, step := range f.Steps {
		if step.ID == "queue-scan" {
			queueScanDesc = step.Description
			break
		}
	}
	if queueScanDesc == "" {
		t.Fatal("queue-scan step not found or has empty description")
	}

	if !strings.Contains(queueScanDesc, "gh pr view") ||
		!strings.Contains(queueScanDesc, "merged-pr-sweep") {
		t.Error("queue-scan's branch-missing path must check for an owner-merged PR " +
			"(gh pr view) and defer MERGED ones to the merged-pr-sweep drain instead " +
			"of closing the MR bead with \"Branch no longer exists\"")
	}
}

func containsDep(needs []string, dep string) bool {
	for _, n := range needs {
		if n == dep {
			return true
		}
	}
	return false
}
