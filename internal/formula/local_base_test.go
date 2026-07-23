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
