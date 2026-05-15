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

// TestWorkflowStepTargetInteractiveStaysOnRig locks gt-3798: an interactive
// step is session-bound and must never be routed by `target`, even when the
// formula declares one. workflowStepTarget must return the rig, not the role.
func TestWorkflowStepTargetInteractiveStaysOnRig(t *testing.T) {
	t.Parallel()

	step := formula.Step{Target: "mayor", Interactive: true}
	if got := workflowStepTarget(step, "gastown"); got != "gastown" {
		t.Fatalf("workflowStepTarget(interactive, target=mayor) = %q, want %q", got, "gastown")
	}
}

// TestWorkflowStepDescriptionInteractiveOmitsTarget locks gt-3798: an
// interactive step must not carry a routable workflow_target line, otherwise
// the auto-dispatch override would redirect it away from the human's session.
func TestWorkflowStepDescriptionInteractiveOmitsTarget(t *testing.T) {
	t.Parallel()

	description := "Talk to the human\n\nClarify the requirements"
	got := workflowStepDescription(formula.Step{Target: "mayor", Interactive: true}, description)
	if got != description {
		t.Fatalf("workflowStepDescription(interactive) = %q, want raw %q", got, description)
	}
	if strings.Contains(got, workflowTargetField) {
		t.Fatalf("interactive step description must not contain %q: %q", workflowTargetField, got)
	}
}

// TestResolveReviewID locks gt-4032-A: --set review_id=X must win so dry-run
// and real execution compute the same review ID; everything else falls back
// to a fresh generated short ID.
func TestResolveReviewID(t *testing.T) {
	t.Parallel()

	if got := resolveReviewID(map[string]interface{}{"review_id": "fixed-id"}); got != "fixed-id" {
		t.Fatalf("resolveReviewID(explicit) = %q, want %q", got, "fixed-id")
	}

	for _, name := range []string{"whitespace", "missing", "non-string"} {
		t.Run(name, func(t *testing.T) {
			var setVars map[string]interface{}
			switch name {
			case "whitespace":
				setVars = map[string]interface{}{"review_id": "   "}
			case "missing":
				setVars = map[string]interface{}{"problem": "x"}
			case "non-string":
				setVars = map[string]interface{}{"review_id": 42}
			}
			got := resolveReviewID(setVars)
			if got == "" || len(got) != 5 {
				t.Fatalf("resolveReviewID(%s) = %q, want generated 5-char id", name, got)
			}
		})
	}
}

// TestSynthesisDescriptionRendersOutputVars locks gt-4032-B: the synthesis
// description is rendered with an .output.* / --set context (mirroring the
// leg context) so {{.output.directory}}/{{.output.synthesis}} and {{.problem}}
// resolve to concrete values instead of being stored as raw template text.
func TestSynthesisDescriptionRendersOutputVars(t *testing.T) {
	t.Parallel()

	outputDir := ".prd-reviews/fixed-id"
	synthesisFile := "prd-review.md"
	synCtx := map[string]interface{}{
		"formula_name": "mol-prd-review",
		"review_id":    "fixed-id",
		"output": map[string]interface{}{
			"directory": outputDir,
			"synthesis": synthesisFile,
		},
		"output_path": filepath.Join(outputDir, synthesisFile),
		"problem":     "AI Clause Generator",
	}

	tmpl := "Input: {{.output.directory}}/\nOutput: {{.output.directory}}/{{.output.synthesis}}\nReview: {{.problem}}"
	got, err := renderTemplate(tmpl, synCtx)
	if err != nil {
		t.Fatalf("renderTemplate() error: %v", err)
	}
	want := "Input: .prd-reviews/fixed-id/\nOutput: .prd-reviews/fixed-id/prd-review.md\nReview: AI Clause Generator"
	if got != want {
		t.Fatalf("renderTemplate() = %q, want %q", got, want)
	}
}
