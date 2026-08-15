package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/skills"
)

func TestNewSkillsCheck(t *testing.T) {
	check := NewSkillsCheck()
	if check.Name() != "skills-provisioned" {
		t.Errorf("Name() = %q, want skills-provisioned", check.Name())
	}
	if !check.CanFix() {
		t.Error("SkillsCheck should be fixable")
	}
}

func TestSkillsCheck_Run_Missing(t *testing.T) {
	townRoot := t.TempDir()
	check := NewSkillsCheck()
	result := check.Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusWarning {
		t.Fatalf("Status = %v, want warning", result.Status)
	}
	if len(check.missingSkills) == 0 {
		t.Fatal("expected missing skills")
	}
}

func TestSkillsCheck_Fix_ProvisionsSkills(t *testing.T) {
	townRoot := t.TempDir()
	check := NewSkillsCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	check.Run(ctx)
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix: %v", err)
	}
	if _, err := os.Stat(filepath.Join(townRoot, ".agents", "skills", "implement", "SKILL.md")); err != nil {
		t.Fatalf("fix did not provision implement: %v", err)
	}
	if got := skills.MissingFor(townRoot, "claude"); len(got) != 0 {
		t.Fatalf("still missing %v", got)
	}
}
