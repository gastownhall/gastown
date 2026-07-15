package formula

import (
	"os"
	"os/exec"
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

func TestPolecatFormulaMatchesPolecatIssueIDsLiterally(t *testing.T) {
	data, err := os.ReadFile("formulas/mol-polecat-work.formula.toml")
	if err != nil {
		t.Fatalf("read mol-polecat-work formula: %v", err)
	}
	text := string(data)

	const branchCase = `case "$current_branch" in
      polecat/*/"{{issue}}"+*|polecat/*/"{{issue}}"@*)
        exit 0
        ;;
      *)
        exit 1
        ;;
    esac`
	if !strings.Contains(text, `case "$current_branch" in
      polecat/*/"{{issue}}"+*|polecat/*/"{{issue}}"@*)`) {
		t.Fatal("formula does not match the issue segment literally with a shell case")
	}
	if strings.Contains(text, `[[ "$current_branch" =~`) {
		t.Fatal("formula still matches the issue segment with an ERE")
	}

	for _, tc := range []struct {
		name    string
		branch  string
		matches bool
	}{
		{"dotted ID plus suffix", "polecat/alpha/gt-4kp9.5.5.1+mk123456", true},
		{"dotted ID legacy at suffix", "polecat/alpha/gt-4kp9.5.5.1@mk123456", true},
		{"dot lookalike does not match", "polecat/alpha/gt-4kp9x5.5.1+mk123456", false},
		{"different issue does not match", "polecat/alpha/gt-other.5.5.1+mk123456", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := `current_branch="$1"
` + strings.ReplaceAll(branchCase, "{{issue}}", "gt-4kp9.5.5.1")
			err := exec.Command("sh", "-c", script, "sh", tc.branch).Run()
			if got := err == nil; got != tc.matches {
				t.Fatalf("branch %q matches = %t, want %t", tc.branch, got, tc.matches)
			}
		})
	}
}
