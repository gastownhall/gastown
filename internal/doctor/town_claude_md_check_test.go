package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/templates"
)

func TestTownCLAUDEmdCheck_Missing(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)

	if result.Status != StatusError {
		t.Errorf("expected StatusError for missing file, got %v", result.Status)
	}
	if !check.fileMissing {
		t.Error("expected fileMissing=true")
	}
}

func TestTownCLAUDEmdCheck_Complete(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Write the canonical content
	canonical := templates.TownRootCLAUDEmd()
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(canonical), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK for complete file, got %v: %s", result.Status, result.Message)
	}
}

func TestTownCLAUDEmdCheck_MissingSections(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Write only the identity anchor (no Dolt or communication sections)
	content := `# Gas Town

This is a Gas Town workspace. Your identity and role are determined by ` + "`gt prime`" + `.

Run ` + "`gt prime`" + ` for full context after compaction, clear, or new session.
`
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning for missing sections, got %v", result.Status)
	}
	if len(check.missingSections) != 3 {
		t.Errorf("expected 3 missing sections, got %d", len(check.missingSections))
	}
}

func TestTownCLAUDEmdCheck_PartialSections(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Write identity anchor + Dolt section but no communication hygiene
	content := `# Gas Town

This is a Gas Town workspace.

## Dolt Server — Operational Awareness

Dolt is the data plane for beads.
`
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning, got %v", result.Status)
	}
	if len(check.missingSections) != 2 {
		t.Errorf("expected 2 missing sections, got %d", len(check.missingSections))
	}
	if check.missingSections[0].Name != "Communication hygiene" {
		t.Errorf("expected 'Communication hygiene' missing, got %q", check.missingSections[0].Name)
	}
}

func TestTownCLAUDEmdCheck_Fix_MissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Fatalf("expected StatusError, got %v", result.Status)
	}

	// Fix should create the file from canonical
	if err := check.Fix(ctx); err != nil {
		t.Fatal(err)
	}

	// Verify file was created
	data, err := os.ReadFile(filepath.Join(tmpDir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if !strings.Contains(content, "## Dolt Server") {
		t.Error("created file missing Dolt Server section")
	}
	if !strings.Contains(content, "### Communication hygiene") {
		t.Error("created file missing Communication hygiene section")
	}
	if !strings.Contains(content, "## Product Documentation Authority") {
		t.Error("created file missing Product Documentation Authority section")
	}
	if !strings.Contains(content, "`HANDOFF.md` is historical only") {
		t.Error("created file does not mark HANDOFF.md historical")
	}
}

func TestTownCLAUDEmdCheck_Fix_AppendSections(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Write minimal anchor + a user custom section
	original := `# Gas Town

This is a Gas Town workspace.

## My Custom Section

This is user-added content that should be preserved.
`
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v", result.Status)
	}

	// Fix should append missing sections
	if err := check.Fix(ctx); err != nil {
		t.Fatal(err)
	}

	// Verify file was updated
	data, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	// User's custom section should be preserved
	if !strings.Contains(content, "## My Custom Section") {
		t.Error("user custom section was not preserved")
	}
	if !strings.Contains(content, "user-added content") {
		t.Error("user custom content was not preserved")
	}

	// Missing sections should be appended
	if !strings.Contains(content, "## Dolt Server") {
		t.Error("Dolt Server section was not appended")
	}
	if !strings.Contains(content, "### Communication hygiene") {
		t.Error("Communication hygiene section was not appended")
	}
	if !strings.Contains(content, "## Product Documentation Authority") {
		t.Error("Product Documentation Authority section was not appended")
	}
	if !strings.Contains(content, "`HANDOFF.md` is historical only") {
		t.Error("appended documentation section does not mark HANDOFF.md historical")
	}
}

func TestTownCLAUDEmdCheck_Fix_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Write the canonical content
	canonical := templates.TownRootCLAUDEmd()
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(canonical), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Fatalf("expected StatusOK, got %v", result.Status)
	}

	// Fix on an OK file should be a no-op
	if err := check.Fix(ctx); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != canonical {
		t.Error("fix modified a complete file (should be idempotent)")
	}
}

func TestParseH2Sections(t *testing.T) {
	content := `# Header

Preamble text.

## Section One

Content one.

## Section Two

Content two.
### Subsection

Sub content.

## Section Three

Content three.
`

	sections := parseH2Sections(content)

	if len(sections) != 4 { // preamble + 3 H2 sections
		t.Fatalf("expected 4 sections, got %d", len(sections))
	}

	// Preamble
	if sections[0].heading != "" {
		t.Errorf("preamble should have empty heading, got %q", sections[0].heading)
	}
	if !strings.Contains(sections[0].content, "Preamble text") {
		t.Error("preamble missing expected content")
	}

	// Section One
	if sections[1].heading != "## Section One" {
		t.Errorf("expected '## Section One', got %q", sections[1].heading)
	}

	// Section Two (should include H3 subsection)
	if sections[2].heading != "## Section Two" {
		t.Errorf("expected '## Section Two', got %q", sections[2].heading)
	}
	if !strings.Contains(sections[2].content, "### Subsection") {
		t.Error("Section Two should include H3 subsection")
	}

	// Section Three
	if sections[3].heading != "## Section Three" {
		t.Errorf("expected '## Section Three', got %q", sections[3].heading)
	}
}

func TestIsIdentityAnchor_MinimalAnchor(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "CLAUDE.md")

	content := `# Gas Town

Run ` + "`gt prime`" + ` for full context.
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if !isIdentityAnchor(path) {
		t.Error("minimal anchor should be recognized as identity anchor")
	}
}

func TestIsIdentityAnchor_ExpandedCLAUDEmd(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "CLAUDE.md")

	// Write canonical content (many lines)
	if err := os.WriteFile(path, []byte(templates.TownRootCLAUDEmd()), 0644); err != nil {
		t.Fatal(err)
	}

	if !isIdentityAnchor(path) {
		t.Error("expanded CLAUDE.md should be recognized as identity anchor")
	}
}

func TestIsIdentityAnchor_NonGasTownFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "CLAUDE.md")

	content := `# My Project

This is a regular project CLAUDE.md, not Gas Town.
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if isIdentityAnchor(path) {
		t.Error("non-Gas Town file should not be recognized as identity anchor")
	}
}
