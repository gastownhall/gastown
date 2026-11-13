package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrandedWispWorkDir_HQ(t *testing.T) {
	townRoot := t.TempDir()

	got := resolveStrandedWispWorkDir(townRoot, "hq")
	if got != townRoot {
		t.Fatalf("resolveStrandedWispWorkDir(%q, hq) = %q, want %q", townRoot, got, townRoot)
	}
}

func TestStrandedWispWorkDir_RoutedRig(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	routesContent := `{"prefix":"hq-","path":"."}
{"prefix":"sw-","path":"sallaWork/mayor/rig"}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(townRoot, "sallaWork/mayor/rig")
	if got := resolveStrandedWispWorkDir(townRoot, "sw"); got != want {
		t.Fatalf("resolveStrandedWispWorkDir(%q, sw) = %q, want %q", townRoot, got, want)
	}
}

// TestStrandedDurableLabelsAreLabelDriven pins the classifier to explicit
// labels. The check must never guess from titles or ID patterns — a bead is
// durable because it carries gt:rig or gt:escalation, not because it looks
// important.
func TestStrandedDurableLabelsAreLabelDriven(t *testing.T) {
	want := map[string]string{
		"gt:rig":        "rig",
		"gt:escalation": "escalation",
	}
	if len(strandedDurableLabels) != len(want) {
		t.Fatalf("strandedDurableLabels has %d entries, want %d: %v",
			len(strandedDurableLabels), len(want), strandedDurableLabels)
	}
	for label, kind := range want {
		if got := strandedDurableLabels[label]; got != kind {
			t.Errorf("strandedDurableLabels[%q] = %q, want %q", label, got, kind)
		}
	}
}

// TestStrandedRunSkipsWhenDoltUnavailable verifies the check degrades to OK
// rather than erroring when there is no Dolt server. Wisps never appear in
// JSONL exports (the table is in dolt_ignore), so there is no fallback source.
func TestStrandedRunSkipsWhenDoltUnavailable(t *testing.T) {
	townRoot := t.TempDir()

	check := NewCheckStrandedDurableWisps()
	result := check.Run(&CheckContext{TownRoot: townRoot})

	if result.Status != StatusOK {
		t.Errorf("status = %v, want StatusOK when Dolt is unavailable", result.Status)
	}
	if len(check.stranded) != 0 {
		t.Errorf("expected no findings, got %d", len(check.stranded))
	}
}

// TestStrandedFixIsNoopWithoutFindings guards the Fix path: with nothing
// detected it must not shell out to bd at all.
func TestStrandedFixIsNoopWithoutFindings(t *testing.T) {
	check := NewCheckStrandedDurableWisps()
	if err := check.Fix(&CheckContext{TownRoot: t.TempDir()}); err != nil {
		t.Fatalf("Fix with no findings: %v", err)
	}
}

func TestStrandedCheckMetadata(t *testing.T) {
	check := NewCheckStrandedDurableWisps()
	if check.Name() != "stranded-durable-wisps" {
		t.Errorf("Name() = %q", check.Name())
	}
	if !check.CanFix() {
		t.Error("check should be fixable")
	}
	if !strings.Contains(check.Description(), "wisps table") {
		t.Errorf("Description() = %q, want it to mention the wisps table", check.Description())
	}
}
