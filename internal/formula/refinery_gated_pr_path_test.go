package formula

import (
	"strings"
	"testing"
)

func TestRefineryPatrolGatedItemsAlwaysUsePRPath(t *testing.T) {
	f := loadRefineryPatrolFormula(t)
	if f.Version < 18 {
		t.Fatalf("formula version = %d, want >= 18", f.Version)
	}

	mergePush := requireFormulaStep(t, f, "merge-push")
	if !strings.Contains(mergePush.Description, "gated item") || !strings.Contains(mergePush.Description, "never a direct") {
		t.Fatal("merge-push must name the gated exception: a gated item always takes the PR path, never a direct merge")
	}
	for _, want := range []string{"gtb mq gate", "gh pr merge", "gt mq post-merge"} {
		if !strings.Contains(mergePush.Description, want) {
			t.Fatalf("merge-push missing %q in gated PR path", want)
		}
	}
}
