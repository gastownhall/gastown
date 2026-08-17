package polecat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/git"
	"github.com/jonbaldie/gastown/internal/rig"
)

// TestRemoveNuclearDoesNotPushWhenOriginIsReadOnly reproduces the UAT
// symptom: gt shutdown --nuclear still tries to push unpushed polecat
// branches to a third-party origin that already 403s, then deletes the
// worktree. Expected: skip the push and leave the local branch intact.
func TestRemoveNuclearDoesNotPushWhenOriginIsReadOnly(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "origin.git")
	runReadonlyGit(t, root, "init", "--bare", remote)

	// Record every push attempt, then reject it the way GitHub 403 / archived remotes do.
	pushAttempted := filepath.Join(root, "push-attempted")
	hooksDir := filepath.Join(remote, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	hook := `#!/bin/sh
echo attempted >> "` + pushAttempted + `"
echo "remote: Permission to mattn/go-isatty.git denied to user." >&2
echo "fatal: unable to access 'https://github.com/mattn/go-isatty.git/': The requested URL returned error: 403" >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-receive"), []byte(hook), 0755); err != nil {
		t.Fatal(err)
	}

	mayorRig := filepath.Join(root, "mayor", "rig")
	if err := os.MkdirAll(mayorRig, 0755); err != nil {
		t.Fatal(err)
	}
	runReadonlyGit(t, mayorRig, "init")
	runReadonlyGit(t, mayorRig, "config", "user.email", "test@example.com")
	runReadonlyGit(t, mayorRig, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(mayorRig, "README.md"), []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runReadonlyGit(t, mayorRig, "add", "README.md")
	runReadonlyGit(t, mayorRig, "commit", "-m", "base")
	runReadonlyGit(t, mayorRig, "branch", "-M", "main")
	runReadonlyGit(t, mayorRig, "remote", "add", "origin", remote)
	// Seed origin/main before the hook is the only remaining path; the
	// hook already blocks, so push main through a one-shot disable.
	if err := os.Rename(filepath.Join(hooksDir, "pre-receive"), filepath.Join(hooksDir, "pre-receive.off")); err != nil {
		t.Fatal(err)
	}
	runReadonlyGit(t, mayorRig, "push", "-u", "origin", "main")
	if err := os.Rename(filepath.Join(hooksDir, "pre-receive.off"), filepath.Join(hooksDir, "pre-receive")); err != nil {
		t.Fatal(err)
	}

	polecatName := "furiosa"
	clonePath := filepath.Join(root, "polecats", polecatName, "thirdparty")
	if err := os.MkdirAll(filepath.Dir(clonePath), 0755); err != nil {
		t.Fatal(err)
	}
	runReadonlyGit(t, mayorRig, "worktree", "add", "-b", "polecat/furiosa/ck-7vj", clonePath, "main")
	if err := os.WriteFile(filepath.Join(clonePath, "work.txt"), []byte("local work\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runReadonlyGit(t, clonePath, "add", "work.txt")
	runReadonlyGit(t, clonePath, "commit", "-m", "local polecat work")
	// Point origin at a hosted URL so this matches a public third-party clone
	// with no --push-url. The 403 hook remains on the local file remote only
	// as a tripwire if a push is still attempted.
	runReadonlyGit(t, clonePath, "remote", "set-url", "origin", "https://github.com/mattn/go-isatty.git")

	headBefore := strings.TrimSpace(readonlyGitOutput(t, clonePath, "rev-parse", "HEAD"))
	r := &rig.Rig{Name: "thirdparty", Path: root}
	m := NewManager(r, git.NewGit(root), nil)

	if err := m.RemoveWithOptions(polecatName, true, true, true); err != nil {
		t.Fatalf("nuclear remove: %v", err)
	}

	// Symptom 1: nuclear remove must not attempt a doomed origin push.
	if data, err := os.ReadFile(pushAttempted); err == nil && len(data) > 0 {
		t.Fatalf("nuclear remove pushed to a 403 origin (UAT symptom): %q", data)
	}
	if out := readonlyGitOutput(t, remote, "branch", "--list", "polecat/furiosa/ck-7vj"); strings.TrimSpace(out) != "" {
		t.Fatalf("nuclear remove pushed polecat branch to read-only origin: %q", out)
	}

	// Symptom 2: local commits must still be reachable after a skipped push.
	// Deleting the worktree after a 403 is the UAT "WORK AT RISK" path.
	if _, err := os.Stat(clonePath); err == nil {
		t.Fatal("worktree still present after nuclear remove; test fixture unexpected")
	}
	if out := readonlyGitOutput(t, mayorRig, "cat-file", "-t", headBefore); strings.TrimSpace(out) != "commit" {
		t.Fatalf("local polecat commit %s was not preserved after nuclear remove: %q", headBefore, out)
	}
	if out := readonlyGitOutput(t, mayorRig, "branch", "--list", "polecat/furiosa/ck-7vj"); !strings.Contains(out, "polecat/furiosa/ck-7vj") {
		t.Fatalf("nuclear remove deleted local branch after refusing a 403 push; got %q", out)
	}
}

func TestShouldPushBeforeRemoval(t *testing.T) {
	tests := []struct {
		name string
		in   removalPushDecision
		want bool
	}{
		{name: "writable origin with push url", in: removalPushDecision{HasPushURL: true}, want: true},
		{name: "local merge never pushes", in: removalPushDecision{HasPushURL: true, LocalMerge: true}, want: false},
		{name: "prior 403 never pushes", in: removalPushDecision{HasPushURL: true, PriorPushFail: true}, want: false},
		{name: "third-party clone without push url", in: removalPushDecision{}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldPushBeforeRemoval(tt.in); got != tt.want {
				t.Fatalf("shouldPushBeforeRemoval(%+v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsLocalGitRemote(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{in: "/tmp/origin.git", want: true},
		{in: "file:///tmp/origin.git", want: true},
		{in: "https://github.com/mattn/go-isatty.git", want: false},
		{in: "git@github.com:kelseyhightower/envconfig.git", want: false},
	}
	for _, tt := range tests {
		if got := isLocalGitRemote(tt.in); got != tt.want {
			t.Fatalf("isLocalGitRemote(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func runReadonlyGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func readonlyGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}
