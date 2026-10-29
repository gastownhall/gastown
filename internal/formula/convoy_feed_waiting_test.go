package formula

import (
	"strings"
	"testing"
)

func TestConvoyFeedReadyRuleSkipsWaitingGatedItems(t *testing.T) {
	content, err := formulasFS.ReadFile("formulas/mol-convoy-feed.formula.toml")
	if err != nil {
		t.Fatalf("reading convoy feed formula: %v", err)
	}
	f, err := Parse(content)
	if err != nil {
		t.Fatalf("parsing convoy feed formula: %v", err)
	}
	ready := requireFormulaStep(t, f, "load-convoy")
	if !strings.Contains(ready.Description, "gtb:waiting:") {
		t.Fatal("convoy feed ready rule must skip issues labelled gtb:waiting:")
	}
}
