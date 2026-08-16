package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/git"
	"github.com/jonbaldie/gastown/internal/instructions"
	"github.com/jonbaldie/gastown/internal/testutil/gitcmd"
)

func TestStripOverlayInstructionFiles_RemovesAgentsOverlay(t *testing.T) {
	dir := initOverlayRepo(t)
	overlay := polecatOverlayText()
	writeAndCommit(t, dir, "feature", map[string]string{
		"AGENTS.md": overlay,
		"CLAUDE.md": overlay,
	})

	g := git.NewGit(dir)
	if !stripOverlayInstructionFiles(g, "main", "main") {
		t.Fatal("expected overlay pair to be stripped")
	}

	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if _, err := g.ShowFile("HEAD", name); err == nil {
			t.Errorf("%s still present on HEAD after strip", name)
		}
	}
}

func TestStripOverlayInstructionFiles_RemovesLocalPair(t *testing.T) {
	dir := initOverlayRepo(t)
	writeAndCommit(t, dir, "feature", map[string]string{
		"AGENTS.local.md": polecatOverlayText(),
		"CLAUDE.local.md": polecatOverlayText(),
	})

	g := git.NewGit(dir)
	if !stripOverlayInstructionFiles(g, "main", "main") {
		t.Fatal("expected local overlay pair to be stripped")
	}
	for _, name := range []string{"AGENTS.local.md", "CLAUDE.local.md"} {
		if _, err := g.ShowFile("HEAD", name); err == nil {
			t.Errorf("%s still present on HEAD after strip", name)
		}
	}
}

func TestStripOverlayInstructionFiles_RestoresConstitution(t *testing.T) {
	dir := initOverlayRepo(t)
	constitution := "# Project\nKeep this file.\n"
	gitcmd.Run(t, dir, "checkout", "main")
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(constitution), 0644); err != nil {
		t.Fatal(err)
	}
	gitcmd.Run(t, dir, "add", "CLAUDE.md")
	gitcmd.Run(t, dir, "commit", "-m", "add constitution")

	writeAndCommit(t, dir, "feature", map[string]string{
		"CLAUDE.md": polecatOverlayText(),
	})

	g := git.NewGit(dir)
	if !stripOverlayInstructionFiles(g, "main", "main") {
		t.Fatal("expected constitution restore")
	}
	got, err := g.ShowFile("HEAD", "CLAUDE.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Keep this file") {
		t.Fatalf("constitution not restored: %q", got)
	}
	if instructions.IsGasTownOverlay(got) {
		t.Fatal("restored CLAUDE.md still looks like a Gas Town overlay")
	}
}

func TestStripOverlayInstructionFiles_LeavesConstitutionAlone(t *testing.T) {
	dir := initOverlayRepo(t)
	constitution := "# Project\nKeep this file.\n"
	gitcmd.Run(t, dir, "checkout", "main")
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(constitution), 0644); err != nil {
		t.Fatal(err)
	}
	gitcmd.Run(t, dir, "add", "CLAUDE.md")
	gitcmd.Run(t, dir, "commit", "-m", "add constitution")

	writeAndCommit(t, dir, "feature", map[string]string{
		"README.md": "# still working\n",
	})

	g := git.NewGit(dir)
	if stripOverlayInstructionFiles(g, "main", "main") {
		t.Fatal("constitution-only branch should not create a strip commit")
	}
	got, err := g.ShowFile("HEAD", "CLAUDE.md")
	if err != nil {
		t.Fatal(err)
	}
	if got != constitution && !strings.Contains(got, "Keep this file") {
		t.Fatalf("constitution changed: %q", got)
	}
}

func TestStripOverlayInstructionFiles_LeavesNonOverlayLocalAgents(t *testing.T) {
	dir := initOverlayRepo(t)
	custom := "# Local notes\nNot a Gas Town overlay.\n"
	writeAndCommit(t, dir, "feature", map[string]string{
		"AGENTS.local.md": custom,
	})

	g := git.NewGit(dir)
	if stripOverlayInstructionFiles(g, "main", "main") {
		t.Fatal("non-overlay AGENTS.local.md should not be stripped")
	}
	got, err := g.ShowFile("HEAD", "AGENTS.local.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Not a Gas Town overlay") {
		t.Fatalf("AGENTS.local.md changed: %q", got)
	}
	if instructions.IsGasTownOverlay(got) {
		t.Fatal("non-overlay AGENTS.local.md was treated as overlay")
	}
}

func polecatOverlayText() string {
	return "# Polecat Context\n\n## 🚨 THE " + instructions.LifecycleMarker + " 🚨\n\ngt done\n"
}

func initOverlayRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitcmd.Run(t, dir, "init", "-b", "main")
	gitcmd.Run(t, dir, "config", "user.name", "test")
	gitcmd.Run(t, dir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# repo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitcmd.Run(t, dir, "add", "README.md")
	gitcmd.Run(t, dir, "commit", "-m", "init")
	return dir
}

func writeAndCommit(t *testing.T, dir, branch string, files map[string]string) {
	t.Helper()
	gitcmd.Run(t, dir, "checkout", "-B", branch)
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		gitcmd.Run(t, dir, "add", name)
	}
	gitcmd.Run(t, dir, "commit", "-m", "add overlay")
}
