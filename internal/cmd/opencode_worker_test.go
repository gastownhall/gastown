package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveOpenCodeWorkKey(t *testing.T) {
	t.Run("configured key wins", func(t *testing.T) {
		if got := resolveOpenCodeWorkKey(t.TempDir(), " explicit-branch "); got != "explicit-branch" {
			t.Fatalf("resolveOpenCodeWorkKey = %q, want explicit-branch", got)
		}
	})

	t.Run("derives current branch", func(t *testing.T) {
		dir := t.TempDir()
		runOpenCodeGit(t, dir, "init")
		runOpenCodeGit(t, dir, "config", "user.email", "test@example.com")
		runOpenCodeGit(t, dir, "config", "user.name", "Gas Town Test")
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("test\n"), 0644); err != nil {
			t.Fatal(err)
		}
		runOpenCodeGit(t, dir, "add", "README.md")
		runOpenCodeGit(t, dir, "commit", "-m", "initial")
		runOpenCodeGit(t, dir, "checkout", "-b", "feature/test")

		if got := resolveOpenCodeWorkKey(dir, ""); got != "feature/test" {
			t.Fatalf("resolveOpenCodeWorkKey = %q, want feature/test", got)
		}
	})

	t.Run("configured HEAD derives detached revision", func(t *testing.T) {
		dir := t.TempDir()
		runOpenCodeGit(t, dir, "init")
		runOpenCodeGit(t, dir, "config", "user.email", "test@example.com")
		runOpenCodeGit(t, dir, "config", "user.name", "Gas Town Test")
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("test\n"), 0644); err != nil {
			t.Fatal(err)
		}
		runOpenCodeGit(t, dir, "add", "README.md")
		runOpenCodeGit(t, dir, "commit", "-m", "initial")
		runOpenCodeGit(t, dir, "checkout", "--detach")
		revisionCommand := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
		revisionBytes, err := revisionCommand.Output()
		if err != nil {
			t.Fatal(err)
		}
		if got, want := resolveOpenCodeWorkKey(dir, "HEAD"), strings.TrimSpace(string(revisionBytes)); got != want {
			t.Fatalf("resolveOpenCodeWorkKey = %q, want %q", got, want)
		}
	})
}

func runOpenCodeGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
