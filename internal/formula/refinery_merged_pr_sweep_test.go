package formula

import (
	"strings"
	"testing"
)

func TestRefineryPatrolMergedPRSweepUsesAuthoritativeLookup(t *testing.T) {
	f := loadRefineryPatrolFormula(t)

	queueScan := requireFormulaStep(t, f, "queue-scan")
	mergedSweep := requireFormulaStep(t, f, "merged-pr-sweep")
	processBranch := requireFormulaStep(t, f, "process-branch")
	mergePush := requireFormulaStep(t, f, "merge-push")

	if !containsStepNeed(mergedSweep, "queue-scan") {
		t.Fatalf("merged-pr-sweep needs = %v, want queue-scan", mergedSweep.Needs)
	}
	if !containsStepNeed(processBranch, "merged-pr-sweep") {
		t.Fatalf("process-branch needs = %v, want merged-pr-sweep", processBranch.Needs)
	}

	for _, step := range []struct {
		id          string
		description string
	}{
		{"queue-scan", queueScan.Description},
		{"merged-pr-sweep", mergedSweep.Description},
		{"process-branch", processBranch.Description},
		{"merge-push", mergePush.Description},
	} {
		if !strings.Contains(step.description, "gt mq pr-status") {
			t.Fatalf("%s missing gt mq pr-status authoritative lookup", step.id)
		}
	}

	if strings.Contains(queueScan.Description, `bd close <mr-id> --reason "Branch no longer exists"`) ||
		strings.Contains(processBranch.Description, `bd close <mr-id> --reason "Branch no longer exists"`) {
		t.Fatal("missing-branch guards must not close before authoritative PR lookup")
	}
	if !strings.Contains(mergedSweep.Description, "PR_STATE=MERGED") ||
		!strings.Contains(mergedSweep.Description, "gt mq post-merge <rig> <mr-id> --skip-branch-delete") {
		t.Fatal("merged-pr-sweep must drain MERGED PRs via post-merge with branch deletion skipped")
	}
	if !strings.Contains(mergedSweep.Description, "PR_STATE=CLOSED") ||
		!strings.Contains(mergedSweep.Description, "do NOT send MERGED") {
		t.Fatal("merged-pr-sweep must not drain closed-unmerged PRs")
	}
}

func TestRefineryPatrolDoesNotUseBranchFormPRLookup(t *testing.T) {
	contentBytes, err := formulasFS.ReadFile("formulas/mol-refinery-patrol.formula.toml")
	if err != nil {
		t.Fatalf("reading refinery formula: %v", err)
	}
	content := string(contentBytes)

	forbidden := []string{
		"gh pr view <polecat-branch>",
		"gh pr checks <polecat-branch>",
		"gh pr merge <polecat-branch>",
		"gh pr list --head <polecat-branch>",
	}
	for _, pattern := range forbidden {
		if strings.Contains(content, pattern) {
			t.Fatalf("refinery formula contains forbidden branch-form PR lookup %q", pattern)
		}
	}
}

func loadRefineryPatrolFormula(t *testing.T) *Formula {
	t.Helper()
	content, err := formulasFS.ReadFile("formulas/mol-refinery-patrol.formula.toml")
	if err != nil {
		t.Fatalf("reading refinery formula: %v", err)
	}
	f, err := Parse(content)
	if err != nil {
		t.Fatalf("parsing refinery formula: %v", err)
	}
	return f
}

func requireFormulaStep(t *testing.T, f *Formula, id string) Step {
	t.Helper()
	for _, step := range f.Steps {
		if step.ID == id {
			return step
		}
	}
	t.Fatalf("step %q not found", id)
	return Step{}
}

func containsStepNeed(step Step, need string) bool {
	for _, got := range step.Needs {
		if got == need {
			return true
		}
	}
	return false
}
