package formula

import (
	"os"
	"strings"
	"testing"
)

func TestPolecatFormulaUsesResolvedBaseRef(t *testing.T) {
	data, err := os.ReadFile("formulas/mol-polecat-work.formula.toml")
	if err != nil {
		t.Fatalf("read mol-polecat-work formula: %v", err)
	}
	text := string(data)

	if strings.Contains(text, "origin/{{base_branch}}") {
		t.Fatal("formula still rebases against mutable origin/{{base_branch}}")
	}
	if !strings.Contains(text, "{{base_ref}}") {
		t.Fatal("formula does not use resolved base_ref")
	}
	if !strings.Contains(text, "[vars.base_ref]") {
		t.Fatal("formula does not declare base_ref")
	}
	if got := strings.Count(text, "--base-ref {{base_ref}}"); got != 2 {
		t.Fatalf("formula passes --base-ref to gt done %d times, want 2", got)
	}
}

func TestPolecatFormulaGuardsStableLocalBranchReplay(t *testing.T) {
	data, err := os.ReadFile("formulas/mol-polecat-work.formula.toml")
	if err != nil {
		t.Fatalf("read mol-polecat-work formula: %v", err)
	}
	text := string(data)

	guardStart := strings.Index(text, `case "{{base_ref}}" in`)
	if guardStart == -1 {
		t.Fatal("formula does not scope the branch-preservation guard to base_ref")
	}

	for _, instruction := range []string{"git checkout", "git rebase"} {
		if mutationStart := strings.Index(text, instruction); mutationStart != -1 && mutationStart < guardStart {
			t.Fatalf("%q must not appear before the stable-local-base guard", instruction)
		}
	}

	localBaseScope := strings.Index(text[guardStart:], "refs/heads/*)")
	if localBaseScope == -1 {
		t.Fatal("formula does not limit the branch-preservation guard to refs/heads/*")
	}
	if !strings.Contains(text[guardStart:], `git merge-base --is-ancestor "{{base_ref}}" HEAD`) {
		t.Fatal("formula does not verify that the stable local base is in the current branch")
	}
	if !strings.Contains(text[guardStart:], "Preserve the current branch") {
		t.Fatal("formula does not explicitly preserve a valid current branch")
	}
	if !strings.Contains(text[guardStart:], "Do not checkout, rebase, reset, or switch branches") {
		t.Fatal("formula does not prohibit branch mutation for the stable-local-base guard")
	}

	rejectedMRRework := strings.Index(text, "Exception — rejected-MR rework")
	if rejectedMRRework == -1 {
		t.Fatal("formula does not contain rejected-MR rework instructions")
	}
	if guardStart > rejectedMRRework {
		t.Fatal("stable-local-base guard must appear before rejected-MR rework instructions")
	}
}
