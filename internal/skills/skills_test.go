package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNames_IncludesStandingSkillDirectives(t *testing.T) {
	t.Parallel()

	names := Names()
	for _, name := range []string{"implement", "diagnosing-bugs", "to-spec", "to-tickets", "resolving-merge-conflicts"} {
		if !contains(names, name) {
			t.Fatalf("Names() missing %s: %v", name, names)
		}
	}
}

func TestProvisionFor_WritesUniversalAndAgentSkillTrees(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	if err := ProvisionFor(workDir, "claude"); err != nil {
		t.Fatalf("ProvisionFor: %v", err)
	}

	for _, name := range []string{"implement", "diagnosing-bugs", "to-spec", "to-tickets", "resolving-merge-conflicts"} {
		agentsSkill := filepath.Join(workDir, ".agents", "skills", name, "SKILL.md")
		if _, err := os.Stat(agentsSkill); err != nil {
			t.Errorf("missing universal skill %s: %v", name, err)
		}
		claudeSkill := filepath.Join(workDir, ".claude", "skills", name, "SKILL.md")
		if _, err := os.Stat(claudeSkill); err != nil {
			t.Errorf("missing claude skill %s: %v", name, err)
		}
	}
}

func TestProvisionFor_DoesNotOverwriteExistingSkill(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	custom := filepath.Join(workDir, ".agents", "skills", "implement", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(custom), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(custom, []byte("custom implement skill\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := ProvisionFor(workDir, "claude"); err != nil {
		t.Fatalf("ProvisionFor: %v", err)
	}

	got, err := os.ReadFile(custom)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "custom implement skill\n" {
		t.Fatalf("overwrote existing skill, got %q", got)
	}
}

func TestProvisionFor_CursorUsesCursorSkillsDir(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	if err := ProvisionFor(workDir, "cursor"); err != nil {
		t.Fatalf("ProvisionFor: %v", err)
	}

	path := filepath.Join(workDir, ".cursor", "skills", "implement", "SKILL.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("missing cursor skill: %v", err)
	}
}

func TestProvisionUserDir_WritesConfigDirSkills(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	if err := ProvisionUserDir(configDir); err != nil {
		t.Fatalf("ProvisionUserDir: %v", err)
	}

	path := filepath.Join(configDir, "skills", "diagnosing-bugs", "SKILL.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("missing user-dir skill: %v", err)
	}
}

func TestMissingFor_ReportsAbsentSkills(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	missing := MissingFor(workDir, "claude")
	if len(missing) == 0 {
		t.Fatal("expected missing skills in empty workspace")
	}
	if !contains(missing, "implement") {
		t.Fatalf("MissingFor should include implement, got %v", missing)
	}

	if err := ProvisionFor(workDir, "claude"); err != nil {
		t.Fatalf("ProvisionFor: %v", err)
	}
	if got := MissingFor(workDir, "claude"); len(got) != 0 {
		t.Fatalf("MissingFor after provision = %v, want empty", got)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
