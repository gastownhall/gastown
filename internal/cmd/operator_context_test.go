package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOperatorContextFreshAndRequired(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "CONTEXT.md"), []byte("town rules\n"), 0600)
	os.WriteFile(filepath.Join(root, "CONTEXT.paths"), []byte("# required\nCLAUDE.md\nCLAUDE.md\n"), 0600)
	if _, err := renderOperatorContext(root, ""); err == nil {
		t.Fatal("missing configured source accepted")
	}
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("Python backend"), 0600)
	result, err := renderOperatorContext(root, "")
	if err != nil || strings.Count(result, "Python backend") != 1 || !strings.Contains(result, "SHA-256:") {
		t.Fatalf("%q %v", result, err)
	}
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("Updated rules"), 0600)
	result, err = renderOperatorContext(root, "")
	if err != nil || !strings.Contains(result, "Updated rules") || strings.Contains(result, "Python backend") {
		t.Fatalf("stale context: %q %v", result, err)
	}
}

func TestOperatorContextUsesActualWorktree(t *testing.T) {
	root := t.TempDir()
	worker := t.TempDir()
	for path, content := range map[string]string{
		filepath.Join(root, "CONTEXT.paths"): "",
		filepath.Join(worker, ".git"):        "gitdir: isolated",
		filepath.Join(worker, "CLAUDE.md"):   "actual checkout Python rules",
		filepath.Join(worker, "AGENTS.md"):   "actual checkout ownership rules",
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := renderOperatorContext(root, worker)
	if err != nil || !strings.Contains(result, "actual checkout Python rules") || !strings.Contains(result, "actual checkout ownership rules") {
		t.Fatalf("%q %v", result, err)
	}
}
