package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/formula"
)

// TestAutoInferRig verifies the rig auto-selection logic used when --rig is
// not provided and cwd-based detection finds nothing (e.g. Deacon at HQ level
// on a non-default install where "gastown" rig does not exist).
func TestAutoInferRig(t *testing.T) {
	t.Parallel()

	makeWorkspace := func(t *testing.T) (root string) {
		t.Helper()
		root = t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "mayor"), 0o755); err != nil {
			t.Fatalf("mkdir mayor: %v", err)
		}
		return root
	}

	writeRigsJSON := func(t *testing.T, root string, rigNames []string) {
		t.Helper()
		cfg := &config.RigsConfig{
			Version: 1,
			Rigs:    make(map[string]config.RigEntry),
		}
		for _, name := range rigNames {
			cfg.Rigs[name] = config.RigEntry{}
		}
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshal rigs.json: %v", err)
		}
		path := filepath.Join(root, "mayor", "rigs.json")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write rigs.json: %v", err)
		}
	}

	t.Run("single rig auto-selects", func(t *testing.T) {
		t.Parallel()
		root := makeWorkspace(t)
		rigDir := filepath.Join(root, "myrig")
		if err := os.MkdirAll(rigDir, 0o755); err != nil {
			t.Fatalf("mkdir myrig: %v", err)
		}
		writeRigsJSON(t, root, []string{"myrig"})

		name, path, err := autoInferRig(root)
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}
		if name != "myrig" {
			t.Errorf("name = %q, want %q", name, "myrig")
		}
		if path != rigDir {
			t.Errorf("path = %q, want %q", path, rigDir)
		}
	})

	t.Run("multiple rigs require explicit --rig", func(t *testing.T) {
		t.Parallel()
		root := makeWorkspace(t)
		for _, name := range []string{"rig1", "rig2"} {
			if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", name, err)
			}
		}
		writeRigsJSON(t, root, []string{"rig1", "rig2"})

		_, _, err := autoInferRig(root)
		if err == nil {
			t.Fatal("expected error for multiple rigs, got nil")
		}
		if !strings.Contains(err.Error(), "cannot determine target rig") {
			t.Errorf("expected rig-detection error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "--rig=NAME") {
			t.Errorf("error should suggest --rig=NAME, got: %v", err)
		}
		if !strings.Contains(err.Error(), "rig1") || !strings.Contains(err.Error(), "rig2") {
			t.Errorf("error should list available rigs, got: %v", err)
		}
	})

	t.Run("no rigs registered", func(t *testing.T) {
		t.Parallel()
		root := makeWorkspace(t)
		writeRigsJSON(t, root, []string{})

		_, _, err := autoInferRig(root)
		if err == nil {
			t.Fatal("expected error for no rigs, got nil")
		}
		if !strings.Contains(err.Error(), "no rigs registered") {
			t.Errorf("error should mention no rigs registered, got: %v", err)
		}
		if !strings.Contains(err.Error(), "--rig=NAME") {
			t.Errorf("error should suggest --rig=NAME, got: %v", err)
		}
	})

	t.Run("malformed rigs.json surfaces error", func(t *testing.T) {
		t.Parallel()
		root := makeWorkspace(t)
		path := filepath.Join(root, "mayor", "rigs.json")
		if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
			t.Fatalf("write rigs.json: %v", err)
		}

		// discoverRigsForTownRoot silently falls back to an empty config on
		// parse error, so autoInferRig surfaces the "no rigs registered" path.
		_, _, err := autoInferRig(root)
		if err == nil {
			t.Fatal("expected error for malformed rigs.json, got nil")
		}
		if !strings.Contains(err.Error(), "no rigs registered") {
			t.Errorf("expected no-rigs error (fallback from malformed JSON), got: %v", err)
		}
	})
}

func TestValidateFormulaRunRigNameRejectsPathLike(t *testing.T) {
	for _, rigName := range []string{"", "../outside", "rig/other", `rig\other`, "/tmp/rig"} {
		if err := validateFormulaRunRigName(rigName); err == nil {
			t.Fatalf("validateFormulaRunRigName(%q) = nil, want error", rigName)
		}
	}
	if err := validateFormulaRunRigName("gastown"); err != nil {
		t.Fatalf("validateFormulaRunRigName(gastown) error: %v", err)
	}
}

func TestBuildConvoyLegSlingArgs_AlwaysIncludesNoConvoy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		agent      string
		reviewOnly bool
		wantFlags  []string
	}{
		{"no agent no review", "", false, []string{"--no-convoy"}},
		{"with agent", "claude", false, []string{"--no-convoy", "--agent", "claude"}},
		{"review only", "", true, []string{"--no-convoy", "--review-only"}},
		{"agent and review", "gemini", true, []string{"--no-convoy", "--agent", "gemini", "--review-only"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := buildConvoyLegSlingArgs("bead-1", "myrig", "desc", "title", tt.agent, tt.reviewOnly)
			for _, want := range tt.wantFlags {
				if !slices.Contains(got, want) {
					t.Errorf("buildConvoyLegSlingArgs() missing %q in %v", want, got)
				}
			}
			if got[0] != "sling" {
				t.Errorf("first arg must be 'sling', got %q", got[0])
			}
		})
	}
}

func TestBuildWorkflowStepSlingArgs_AlwaysIncludesNoConvoy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		agent string
	}{
		{"no agent", ""},
		{"with agent", "claude-haiku"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := buildWorkflowStepSlingArgs("bead-2", "myrig", "desc", "title", tt.agent)
			if !slices.Contains(got, "--no-convoy") {
				t.Errorf("buildWorkflowStepSlingArgs() missing --no-convoy in %v", got)
			}
			if got[0] != "sling" {
				t.Errorf("first arg must be 'sling', got %q", got[0])
			}
			if tt.agent != "" && !slices.Contains(got, tt.agent) {
				t.Errorf("buildWorkflowStepSlingArgs() missing agent %q in %v", tt.agent, got)
			}
		})
	}
}

func TestResolveFormulaLegAgent_Precedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		legAgent     string
		cliAgent     string
		formulaAgent string
		want         string
	}{
		{"all empty", "", "", "", ""},
		{"formula only", "", "", "gemini", "gemini"},
		{"cli only", "", "codex", "", "codex"},
		{"leg only", "claude-haiku", "", "", "claude-haiku"},
		{"cli overrides formula", "", "codex", "gemini", "codex"},
		{"leg overrides cli", "claude-haiku", "codex", "gemini", "claude-haiku"},
		{"leg overrides formula", "claude-haiku", "", "gemini", "claude-haiku"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := resolveFormulaLegAgent(tt.legAgent, tt.cliAgent, tt.formulaAgent)
			if got != tt.want {
				t.Errorf("resolveFormulaLegAgent(%q, %q, %q) = %q, want %q",
					tt.legAgent, tt.cliAgent, tt.formulaAgent, got, tt.want)
			}
		})
	}
}

func TestSubstituteFormulaVars(t *testing.T) {
	t.Parallel()

	vars := map[string]interface{}{
		"problem": "First paragraph.\n\nSecond paragraph.",
		"context": "existing code",
	}
	got := substituteFormulaVars("Problem: {{ problem }}\nContext: {{context}}\nKeep: {{review_id}}", vars)
	want := "Problem: First paragraph.\n\nSecond paragraph.\nContext: existing code\nKeep: {{review_id}}"
	if got != want {
		t.Fatalf("substituteFormulaVars() = %q, want %q", got, want)
	}
}

func TestParseSetVarsPreservesMultilineValues(t *testing.T) {
	t.Parallel()

	got := parseSetVars([]string{"problem=First\n\nSecond", "context=a=b"})
	if got["problem"] != "First\n\nSecond" {
		t.Fatalf("problem = %q, want multiline value", got["problem"])
	}
	if got["context"] != "a=b" {
		t.Fatalf("context = %q, want value with equals", got["context"])
	}
}

func TestWorkflowStepTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		step formula.Step
		want string
	}{
		{name: "default rig", step: formula.Step{}, want: "gastown"},
		{name: "explicit rig", step: formula.Step{Target: "rig"}, want: "gastown"},
		{name: "mayor", step: formula.Step{Target: "mayor"}, want: "mayor"},
		{name: "crew path", step: formula.Step{Target: "gastown/crew/alex"}, want: "gastown/crew/alex"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := workflowStepTarget(tt.step, "gastown"); got != tt.want {
				t.Fatalf("workflowStepTarget() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWorkflowStepDescriptionAddsTargetMetadata(t *testing.T) {
	t.Parallel()

	description := "Line one\n\nLine two"
	got := workflowStepDescription(formula.Step{Target: "mayor"}, description)
	want := "workflow_target: mayor\n\nLine one\n\nLine two"
	if got != want {
		t.Fatalf("workflowStepDescription() = %q, want %q", got, want)
	}
}

func TestWorkflowStepTargetFromDescription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		description string
		want        string
	}{
		{name: "no metadata", description: "Body only", want: ""},
		{name: "mayor", description: "workflow_target: mayor\n\nBody", want: "mayor"},
		{name: "rig alias", description: "workflow_target: rig\n\nBody", want: "gastown"},
		{name: "path target", description: "workflow_target: gastown/crew/alex\n\nBody", want: "gastown/crew/alex"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := workflowStepTargetFromDescription(tt.description, "gastown"); got != tt.want {
				t.Fatalf("workflowStepTargetFromDescription() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAttachmentFormulaVarsPrefersAttachedVars(t *testing.T) {
	t.Parallel()

	attachment := &beads.AttachmentFields{
		AttachedVars: []string{"problem=First\n\nSecond"},
		FormulaVars:  "problem=First\n\ntruncated",
	}
	got := attachmentFormulaVars(attachment)
	want := []string{"problem=First\n\nSecond"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attachmentFormulaVars() = %#v, want %#v", got, want)
	}
}

func TestAttachmentFormulaVarsRoundTripsPersistedVars(t *testing.T) {
	t.Parallel()

	desc := beads.SetAttachmentFields(&beads.Issue{Description: "Body"}, &beads.AttachmentFields{
		AttachedFormula: "mol-polecat-work",
		AttachedVars:    []string{"feature=Attached Feature"},
		FormulaVars:     "feature=Persisted Feature\nissue=gt-123\nbase_branch=main",
	})
	attachment := beads.ParseAttachmentFields(&beads.Issue{Description: desc})
	got := attachmentFormulaVars(attachment)
	want := []string{"feature=Attached Feature", "issue=gt-123", "base_branch=main"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attachmentFormulaVars() = %#v, want %#v\nDescription:\n%s", got, want, desc)
	}
}

func TestApplyFormulaVarsUsesWorkflowBareSyntax(t *testing.T) {
	t.Parallel()

	got := applyFormulaVars("bd show {{issue}}\nkeep {{.issue}}", map[string]string{"issue": "gt-123"})
	want := "bd show gt-123\nkeep {{.issue}}"
	if got != want {
		t.Fatalf("applyFormulaVars() = %q, want %q", got, want)
	}
}

func TestRenderTemplateUsesGoDotSyntax(t *testing.T) {
	t.Parallel()

	ctx := map[string]interface{}{"issue": "gt-123"}
	got, err := renderTemplate("bd show {{.issue}}", ctx)
	if err != nil {
		t.Fatalf("renderTemplate() dotted syntax error: %v", err)
	}
	if got != "bd show gt-123" {
		t.Fatalf("renderTemplate() = %q, want %q", got, "bd show gt-123")
	}

	if _, err := renderTemplate("bd show {{issue}}", ctx); err == nil {
		t.Fatal("renderTemplate() with bare syntax succeeded; want Go template error")
	}
}

func TestDesignFormulaOutputUsesReviewID(t *testing.T) {
	t.Parallel()

	content, err := formula.GetEmbeddedFormulaContent("design")
	if err != nil {
		t.Fatalf("GetEmbeddedFormulaContent(design): %v", err)
	}
	f, err := formula.Parse(content)
	if err != nil {
		t.Fatalf("Parse(design): %v", err)
	}
	if f.Output == nil {
		t.Fatal("design formula missing output config")
	}

	got, err := renderTemplate(f.Output.Directory, map[string]interface{}{"review_id": "abc123"})
	if err != nil {
		t.Fatalf("render output directory: %v", err)
	}
	if got != ".designs/abc123" {
		t.Fatalf("output directory = %q, want %q", got, ".designs/abc123")
	}
}

func TestSynthesisDescriptionRendersOutputContext(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"design", "mol-prd-review", "mol-plan-review", "code-review"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			content, err := formula.GetEmbeddedFormulaContent(name)
			if err != nil {
				t.Fatalf("GetEmbeddedFormulaContent(%s): %v", name, err)
			}
			f, err := formula.Parse(content)
			if err != nil {
				t.Fatalf("Parse(%s): %v", name, err)
			}
			if f.Synthesis == nil || f.Output == nil {
				t.Fatalf("%s missing synthesis or output config", name)
			}

			ctx := formulaTemplateContext(name, "local files", "abc123", 0, "", nil, nil,
				map[string]interface{}{
					"context":    "extra context",
					"plan":       "test plan",
					"prd_review": "prd-review.md",
					"problem":    "test problem",
					"scope":      "test scope",
				})
			addOutputTemplateContext(ctx, ".out/abc123", f.Output.Synthesis)

			got, err := renderTemplate(f.Synthesis.Description, ctx)
			if err != nil {
				t.Fatalf("render synthesis description: %v", err)
			}
			if strings.Contains(got, "{{.") || strings.Contains(got, "<no value>") {
				t.Fatalf("synthesis description left template placeholders unrendered: %q", got)
			}
			if !strings.Contains(got, ".out/abc123") {
				t.Fatalf("synthesis description missing rendered output directory: %q", got)
			}
			if name == "design" {
				for _, want := range []string{
					"All dimension analyses from: .out/abc123/",
					"A synthesized design at: .out/abc123/design-doc.md",
					"# Design: test problem",
				} {
					if !strings.Contains(got, want) {
						t.Fatalf("synthesis description missing %q", want)
					}
				}
			}
		})
	}
}

func TestFormulaRunExamplesUseSetVars(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"design", "mol-idea-to-plan"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			content, err := formula.GetEmbeddedFormulaContent(name)
			if err != nil {
				t.Fatalf("GetEmbeddedFormulaContent(%s): %v", name, err)
			}
			text := string(content)
			for _, bad := range []string{"--problem=", "--context=", "--plan="} {
				if strings.Contains(text, bad) {
					t.Fatalf("%s still contains invalid gt formula run flag %q", name, bad)
				}
			}
		})
	}

	idea, err := formula.GetEmbeddedFormulaContent("mol-idea-to-plan")
	if err != nil {
		t.Fatalf("GetEmbeddedFormulaContent(mol-idea-to-plan): %v", err)
	}
	ideaText := string(idea)
	if strings.Contains(ideaText, "<design-id>") {
		t.Fatal("mol-idea-to-plan still references stale <design-id> output paths")
	}
	if strings.Contains(ideaText, ".designs/<review-id>") {
		t.Fatal("mol-idea-to-plan conflates design output ID with PRD review ID")
	}
	for _, want := range []string{
		"--set problem=\"{{problem}}\"",
		"--set context=\"See .prd-reviews/{{review_id}}/prd-draft.md. {{context}}\"",
		"--set context=\"PRD with clarifications: .prd-reviews/{{review_id}}/prd-draft.md. {{context}}\"",
		".designs/<design-review-id>/design-doc.md",
	} {
		if !strings.Contains(ideaText, want) {
			t.Fatalf("mol-idea-to-plan missing %q", want)
		}
	}

	design, err := formula.GetEmbeddedFormulaContent("design")
	if err != nil {
		t.Fatalf("GetEmbeddedFormulaContent(design): %v", err)
	}
	if !strings.Contains(string(design), "gt formula run design --set problem=") {
		t.Fatal("design usage examples do not mention --set problem=")
	}
}
