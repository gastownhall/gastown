package cmd

import (
	"strings"
	"testing"
)

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

	tests := []struct {
		name string
		text string
		vars map[string]interface{}
		want string
	}{
		{
			name: "empty vars returns text unchanged",
			text: "Hello {{problem}}",
			vars: nil,
			want: "Hello {{problem}}",
		},
		{
			name: "single substitution",
			text: "Build {{problem}} for me",
			vars: map[string]interface{}{"problem": "a widget"},
			want: "Build a widget for me",
		},
		{
			name: "multiple substitutions",
			text: "{{problem}} given {{context}}",
			vars: map[string]interface{}{"problem": "P", "context": "C"},
			want: "P given C",
		},
		{
			name: "whitespace inside braces",
			text: "{{ problem }} and {{  context  }}",
			vars: map[string]interface{}{"problem": "P", "context": "C"},
			want: "P and C",
		},
		{
			name: "unknown placeholders preserved",
			text: "{{problem}} -> {{review_id}}",
			vars: map[string]interface{}{"problem": "P"},
			want: "P -> {{review_id}}",
		},
		{
			name: "no recursion: replacement value is not re-rendered",
			text: "{{problem}}",
			vars: map[string]interface{}{"problem": "{{context}}", "context": "C"},
			want: "{{context}}",
		},
		{
			name: "multiline text with placeholders",
			text: "Line 1: {{problem}}\nLine 2: {{context}}\nLine 3: end",
			vars: map[string]interface{}{"problem": "alpha", "context": "beta"},
			want: "Line 1: alpha\nLine 2: beta\nLine 3: end",
		},
		{
			name: "value with embedded commas/newlines preserved",
			text: "Idea: {{problem}}",
			vars: map[string]interface{}{"problem": "a, b, c\nmore"},
			want: "Idea: a, b, c\nmore",
		},
		{
			name: "no placeholders",
			text: "Plain text",
			vars: map[string]interface{}{"problem": "ignored"},
			want: "Plain text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := substituteFormulaVars(tt.text, tt.vars)
			if got != tt.want {
				t.Errorf("substituteFormulaVars\n  text=%q\n  vars=%v\n  got =%q\n  want=%q",
					tt.text, tt.vars, got, tt.want)
			}
		})
	}
}

func TestParseSetVars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want map[string]interface{}
	}{
		{"empty", nil, map[string]interface{}{}},
		{"single", []string{"problem=hello"}, map[string]interface{}{"problem": "hello"}},
		{"value with equals", []string{"q=a=b=c"}, map[string]interface{}{"q": "a=b=c"}},
		{"missing equals ignored", []string{"badentry"}, map[string]interface{}{}},
		{"empty key ignored", []string{"=val"}, map[string]interface{}{}},
		{"multiple", []string{"a=1", "b=2"}, map[string]interface{}{"a": "1", "b": "2"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseSetVars(tt.args)
			if len(got) != len(tt.want) {
				t.Fatalf("len(parseSetVars(%v)) = %d, want %d (%v vs %v)",
					tt.args, len(got), len(tt.want), got, tt.want)
			}
			for k, v := range tt.want {
				if gv, ok := got[k]; !ok {
					t.Errorf("parseSetVars(%v) missing key %q", tt.args, k)
				} else if gv != v {
					t.Errorf("parseSetVars(%v)[%q] = %v, want %v", tt.args, k, gv, v)
				}
			}
		})
	}

	// Sanity: round-trip a set var through substitution.
	t.Run("integration with substituteFormulaVars", func(t *testing.T) {
		t.Parallel()
		vars := parseSetVars([]string{"problem=Hello, world"})
		got := substituteFormulaVars("Idea: {{problem}}", vars)
		want := "Idea: Hello, world"
		if !strings.Contains(got, want) {
			t.Errorf("substitution dropped value: got %q, want contains %q", got, want)
		}
	})
}
