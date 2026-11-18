package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
)

func TestExpandOutputPath(t *testing.T) {
	tests := []struct {
		name      string
		directory string
		pattern   string
		reviewID  string
		legID     string
		want      string
	}{
		{
			name:      "basic expansion",
			directory: ".reviews/{{review_id}}",
			pattern:   "{{leg.id}}-findings.md",
			reviewID:  "abc123",
			legID:     "security",
			want:      ".reviews/abc123/security-findings.md",
		},
		{
			name:      "no templates",
			directory: ".output",
			pattern:   "results.md",
			reviewID:  "xyz",
			legID:     "test",
			want:      ".output/results.md",
		},
		{
			name:      "complex path",
			directory: "reviews/{{review_id}}/findings",
			pattern:   "leg-{{leg.id}}-analysis.md",
			reviewID:  "pr-123",
			legID:     "performance",
			want:      "reviews/pr-123/findings/leg-performance-analysis.md",
		},
		{
			name:      "go template expansion",
			directory: ".designs/{{.review_id}}",
			pattern:   "{{.leg.id}}.md",
			reviewID:  "abc123",
			legID:     "api",
			want:      ".designs/abc123/api.md",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandOutputPath(tt.directory, tt.pattern, tt.reviewID, tt.legID)
			if filepath.ToSlash(got) != tt.want {
				t.Errorf("expandOutputPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLegOutput(t *testing.T) {
	// Test LegOutput struct
	output := LegOutput{
		LegID:    "correctness",
		Title:    "Correctness Review",
		Status:   "closed",
		FilePath: "/tmp/findings.md",
		Content:  "## Findings\n\nNo issues found.",
		HasFile:  true,
	}

	if output.LegID != "correctness" {
		t.Errorf("LegID = %q, want %q", output.LegID, "correctness")
	}

	if output.Status != "closed" {
		t.Errorf("Status = %q, want %q", output.Status, "closed")
	}

	if !output.HasFile {
		t.Error("HasFile should be true")
	}
}

func TestConvoyMeta(t *testing.T) {
	// Test ConvoyMeta struct
	meta := ConvoyMeta{
		ID:        "hq-cv-abc",
		Title:     "Code Review: PR #123",
		Status:    "open",
		Formula:   "code-review",
		ReviewID:  "pr123",
		LegIssues: []string{"gt-leg1", "gt-leg2", "gt-leg3"},
	}

	if meta.ID != "hq-cv-abc" {
		t.Errorf("ID = %q, want %q", meta.ID, "hq-cv-abc")
	}

	if len(meta.LegIssues) != 3 {
		t.Errorf("len(LegIssues) = %d, want 3", len(meta.LegIssues))
	}
}

func TestLoadSynthesisFormulaUsesGTTownRootAndExistingConvoyMetadata(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test-town"}`), 0644); err != nil {
		t.Fatal(err)
	}
	formulasDir := filepath.Join(townRoot, ".beads", "formulas")
	if err := os.MkdirAll(formulasDir, 0755); err != nil {
		t.Fatal(err)
	}
	formulaName := "gt-town-root-only"
	if err := os.WriteFile(filepath.Join(formulasDir, formulaName+".formula.toml"), []byte(sprintfTestFormula(formulaName, "town tier sentinel")), 0644); err != nil {
		t.Fatal(err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GT_ROOT", "")
	t.Setenv("GT_TOWN_ROOT", townRoot)

	meta := &ConvoyMeta{}
	parseConvoyDescriptionMeta(meta, "Formula convoy: "+formulaName+"\n\nLegs: 1\nRig: gastown")
	if meta.Formula != formulaName {
		t.Fatalf("Formula = %q, want %q", meta.Formula, formulaName)
	}
	if meta.Rig != "gastown" {
		t.Fatalf("Rig = %q, want gastown", meta.Rig)
	}

	f, err := loadSynthesisFormula(meta, "")
	if err != nil {
		t.Fatalf("loadSynthesisFormula() error: %v", err)
	}
	if f == nil || f.Name != formulaName {
		t.Fatalf("loaded formula = %#v, want %q", f, formulaName)
	}
}

func TestLoadSynthesisFormulaPreservesExplicitPathPrecedence(t *testing.T) {
	townRoot := makeSynthesisTestWorkspace(t, "gastown")
	withSynthesisTownRoot(t, townRoot)

	formulasDir := filepath.Join(townRoot, ".beads", "formulas")
	if err := os.MkdirAll(formulasDir, 0755); err != nil {
		t.Fatal(err)
	}
	explicitPath := filepath.Join(formulasDir, "explicit.formula.toml")
	if err := os.WriteFile(explicitPath, []byte(sprintfTestFormula("explicit-path", "explicit path wins")), 0644); err != nil {
		t.Fatal(err)
	}

	meta := &ConvoyMeta{
		Formula:     "missing-named-formula",
		FormulaPath: explicitPath,
		Rig:         "gastown",
	}
	f, err := loadSynthesisFormula(meta, "gastown")
	if err != nil {
		t.Fatalf("loadSynthesisFormula() error: %v", err)
	}
	if f == nil || f.Name != "explicit-path" {
		t.Fatalf("loaded formula = %#v, want explicit-path", f)
	}
}

func TestLoadSynthesisFormulaRejectsFormulaPathOutsideFormulaRoots(t *testing.T) {
	townRoot := makeSynthesisTestWorkspace(t, "gastown")
	withSynthesisTownRoot(t, townRoot)

	explicitPath := filepath.Join(t.TempDir(), "outside.formula.toml")
	if err := os.WriteFile(explicitPath, []byte(sprintfTestFormula("outside", "outside root")), 0644); err != nil {
		t.Fatal(err)
	}

	meta := &ConvoyMeta{
		FormulaPath: explicitPath,
		Rig:         "gastown",
	}
	_, err := loadSynthesisFormula(meta, "gastown")
	if err == nil {
		t.Fatal("expected error for formula_path outside configured formula roots")
	}
	if !strings.Contains(err.Error(), "outside configured formula roots") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveSynthesisTargetRigUsesMetadataRig(t *testing.T) {
	townRoot := makeSynthesisTestWorkspace(t, "myrig")
	withSynthesisTownRoot(t, townRoot)

	got, err := resolveSynthesisTargetRigName(&ConvoyMeta{Rig: "myrig"}, "")
	if err != nil {
		t.Fatalf("resolveSynthesisTargetRigName() error: %v", err)
	}
	if got != "myrig" {
		t.Fatalf("resolveSynthesisTargetRigName() = %q, want myrig", got)
	}
}

func TestResolveSynthesisTargetRigRejectsUnknownExplicitRig(t *testing.T) {
	townRoot := makeSynthesisTestWorkspace(t, "gastown")
	withSynthesisTownRoot(t, townRoot)

	_, err := resolveSynthesisTargetRigName(&ConvoyMeta{}, "missing")
	if err == nil {
		t.Fatal("expected error for unknown explicit rig")
	}
	if !strings.Contains(err.Error(), "unknown target rig") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func makeSynthesisTestWorkspace(t *testing.T, rigNames ...string) string {
	t.Helper()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test-town"}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.RigsConfig{
		Version: 1,
		Rigs:    make(map[string]config.RigEntry),
	}
	for _, name := range rigNames {
		if err := os.MkdirAll(filepath.Join(townRoot, name), 0755); err != nil {
			t.Fatal(err)
		}
		cfg.Rigs[name] = config.RigEntry{}
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "rigs.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	return townRoot
}

func withSynthesisTownRoot(t *testing.T, townRoot string) {
	t.Helper()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GT_ROOT", "")
	t.Setenv("GT_TOWN_ROOT", townRoot)
}

func sprintfTestFormula(name, description string) string {
	return "formula = \"" + name + "\"\n" +
		"version = 1\n" +
		"description = \"" + description + "\"\n\n" +
		"[[steps]]\n" +
		"id = \"step-1\"\n" +
		"title = \"Step 1\"\n" +
		"description = \"Do it\"\n"
}
