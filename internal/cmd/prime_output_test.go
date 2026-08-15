package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutputRoleDirectives(t *testing.T) {
	t.Parallel()

	t.Run("no directives emits nothing visible", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		ctx := RoleContext{
			Role:     RolePolecat,
			TownRoot: townRoot,
			Rig:      "myrig",
		}

		var buf bytes.Buffer
		outputRoleDirectives(ctx, &buf, false)
		out := buf.String()

		if strings.Contains(out, "Directives") {
			t.Errorf("expected no header when no directives, got: %s", out)
		}
	})

	t.Run("town-level directive emits town header", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		dir := filepath.Join(townRoot, "directives")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "polecat.md"), []byte("Always be polite."), 0644); err != nil {
			t.Fatal(err)
		}

		ctx := RoleContext{
			Role:     RolePolecat,
			TownRoot: townRoot,
			Rig:      "myrig",
		}

		var buf bytes.Buffer
		outputRoleDirectives(ctx, &buf, false)
		out := buf.String()

		if !strings.Contains(out, "## Town Directives") {
			t.Errorf("expected Town Directives header, got: %s", out)
		}
		if !strings.Contains(out, "Always be polite.") {
			t.Errorf("expected directive content, got: %s", out)
		}
	})

	t.Run("rig-level directive emits rig header", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		dir := filepath.Join(townRoot, "myrig", "directives")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "witness.md"), []byte("Watch closely."), 0644); err != nil {
			t.Fatal(err)
		}

		ctx := RoleContext{
			Role:     RoleWitness,
			TownRoot: townRoot,
			Rig:      "myrig",
		}

		var buf bytes.Buffer
		outputRoleDirectives(ctx, &buf, false)
		out := buf.String()

		if !strings.Contains(out, "## Rig Directives") {
			t.Errorf("expected Rig Directives header, got: %s", out)
		}
		if !strings.Contains(out, "Watch closely.") {
			t.Errorf("expected directive content, got: %s", out)
		}
	})

	t.Run("both levels emits combined header", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()

		townDir := filepath.Join(townRoot, "directives")
		if err := os.MkdirAll(townDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(townDir, "polecat.md"), []byte("Town rule."), 0644); err != nil {
			t.Fatal(err)
		}

		rigDir := filepath.Join(townRoot, "myrig", "directives")
		if err := os.MkdirAll(rigDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rigDir, "polecat.md"), []byte("Rig rule."), 0644); err != nil {
			t.Fatal(err)
		}

		ctx := RoleContext{
			Role:     RolePolecat,
			TownRoot: townRoot,
			Rig:      "myrig",
		}

		var buf bytes.Buffer
		outputRoleDirectives(ctx, &buf, false)
		out := buf.String()

		if !strings.Contains(out, "## Town & Rig Directives") {
			t.Errorf("expected combined header, got: %s", out)
		}
		if !strings.Contains(out, "Town rule.") {
			t.Errorf("expected town content, got: %s", out)
		}
		if !strings.Contains(out, "Rig rule.") {
			t.Errorf("expected rig content, got: %s", out)
		}
	})

	t.Run("explain mode shows file paths", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()

		ctx := RoleContext{
			Role:     RolePolecat,
			TownRoot: townRoot,
			Rig:      "myrig",
		}

		var buf bytes.Buffer
		outputRoleDirectives(ctx, &buf, true)
		out := buf.String()

		if !strings.Contains(out, "[EXPLAIN]") {
			t.Errorf("expected EXPLAIN output, got: %s", out)
		}
		if !strings.Contains(out, filepath.Join("directives", "polecat.md")) {
			t.Errorf("expected file path in explain output, got: %s", out)
		}
	})

	t.Run("empty rig name skips rig path", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()

		townDir := filepath.Join(townRoot, "directives")
		if err := os.MkdirAll(townDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(townDir, "mayor.md"), []byte("Mayor directive."), 0644); err != nil {
			t.Fatal(err)
		}

		ctx := RoleContext{
			Role:     RoleMayor,
			TownRoot: townRoot,
			Rig:      "",
		}

		var buf bytes.Buffer
		outputRoleDirectives(ctx, &buf, false)
		out := buf.String()

		if !strings.Contains(out, "## Town Directives") {
			t.Errorf("expected Town Directives header, got: %s", out)
		}
		if !strings.Contains(out, "Mayor directive.") {
			t.Errorf("expected directive content, got: %s", out)
		}
	})
}

func TestOutputCommandQuickReferenceBootBlocksRawTmux(t *testing.T) {
	output := captureStdout(t, func() {
		outputCommandQuickReference(RoleContext{Role: RoleBoot})
	})

	for _, want := range []string{
		"gt nudge deacon",
		"blocked; can stage unsubmitted input",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("Boot quick reference missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "tmux send-keys~~ (unreliable)") {
		t.Fatalf("Boot quick reference still calls raw tmux merely unreliable:\n%s", output)
	}
}

func TestOutputRoleContext_IncludesSkillDirectives(t *testing.T) {
	output := captureStdout(t, func() {
		if _, err := outputRoleContext(RoleContext{
			Role:     RoleCrew,
			TownRoot: t.TempDir(),
			Rig:      "testrig",
			Polecat:  "joe",
		}); err != nil {
			t.Fatalf("outputRoleContext: %v", err)
		}
	})

	for _, want := range []string{
		"Working on production code: use /implement's SKILL.md rigorously.",
		"Looking at a bug: use /diagnosing-bugs's SKILL.md rigorously.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("role context missing %q:\n%s", want, output)
		}
	}
}

func TestOutputSkillDirectives_TwoBluntLines(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	outputSkillDirectives(&buf)
	got := buf.String()

	want := []string{
		"Working on production code: use /implement's SKILL.md rigorously.",
		"Looking at a bug: use /diagnosing-bugs's SKILL.md rigorously.",
	}
	for _, line := range want {
		if !strings.Contains(got, line) {
			t.Fatalf("missing %q in:\n%s", line, got)
		}
	}
	if strings.Contains(got, "Use /tdd where possible") {
		t.Fatal("must not repeat /implement SKILL.md contents")
	}
	if strings.Contains(got, "Build a feedback loop") {
		t.Fatal("must not repeat /diagnosing-bugs SKILL.md contents")
	}

	lines := nonEmptyLines(got)
	if len(lines) != 2 {
		t.Fatalf("want exactly 2 lines, got %d: %q", len(lines), lines)
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
