package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newAncestorRepo builds a git repository with a subdirectory that is NOT a
// repository. Any git command run against that subdirectory with plain `-C`
// will fall back to discovery, walk up, and operate on the ancestor.
//
// This is the shape behind the town-root incident: rig paths live beneath a
// town root that is itself a git repository, and a command aimed at a rig
// directory that does not exist yet silently retargets the town.
func newAncestorRepo(t *testing.T) (ancestor, nonRepoChild string) {
	t.Helper()
	ancestor = t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", ancestor}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", ".")
	run("config", "user.email", "ancestor@test.local")
	run("config", "user.name", "Ancestor Test")
	if err := os.WriteFile(filepath.Join(ancestor, "README"), []byte("ancestor\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "ancestor init")

	nonRepoChild = filepath.Join(ancestor, "rigs", "notarepo")
	if err := os.MkdirAll(nonRepoChild, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	return ancestor, nonRepoChild
}

// ancestorConfig reads a config key straight out of the ancestor's git
// directory. Addressing --git-dir keeps the assertion itself immune to the
// discovery escape it is checking for.
func ancestorConfig(t *testing.T, ancestor, key string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", filepath.Join(ancestor, ".git"), "config", "--get", key)
	out, err := cmd.Output()
	if err != nil {
		return "" // exit 1 means unset, which is the healthy state
	}
	return strings.TrimSpace(string(out))
}

// TestDiscoveryEscapeIsReal documents the underlying git behaviour this package
// has to defend against. If this ever stops holding, the guards below become
// unnecessary rather than wrong.
func TestDiscoveryEscapeIsReal(t *testing.T) {
	ancestor, child := newAncestorRepo(t)

	out, err := exec.Command("git", "-C", child, "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		t.Fatalf("rev-parse in non-repo child: %v", err)
	}
	resolved := strings.TrimSpace(string(out))
	want := filepath.Join(ancestor, ".git")

	if resolved != want {
		t.Fatalf("expected discovery to escape to %s, got %s", want, resolved)
	}
	t.Logf("confirmed: `git -C %s` resolves to the ancestor at %s", child, resolved)
}

// TestConfigureHooksPathDoesNotEscape covers the leak fixed in gtf-1cz.2.
// configureHooksPath returns early unless .githooks exists, so the directory is
// created to force the write path to run.
func TestConfigureHooksPathDoesNotEscape(t *testing.T) {
	ancestor, child := newAncestorRepo(t)

	if err := os.MkdirAll(filepath.Join(child, ".githooks"), 0o755); err != nil {
		t.Fatalf("mkdir .githooks: %v", err)
	}

	// The child is not a repository, so this must fail rather than write
	// somewhere else.
	if err := configureHooksPath(child); err == nil {
		t.Error("configureHooksPath accepted a non-repository path instead of failing")
	}

	if got := ancestorConfig(t, ancestor, "core.hooksPath"); got != "" {
		t.Errorf("core.hooksPath=%q was written into the ancestor repository", got)
	}
}

// TestSparseCheckoutHelpersDoNotEscape covers the helpers that read or write
// sparse-checkout state. The historical incident wrote sparse-checkout patterns
// into the town repository, so these are the highest-consequence paths.
func TestSparseCheckoutHelpersDoNotEscape(t *testing.T) {
	ancestor, child := newAncestorRepo(t)

	// Reads must not report the ancestor's state as the child's.
	if IsSparseCheckoutConfigured(child) {
		t.Error("IsSparseCheckoutConfigured reported the ancestor's configuration for a non-repository path")
	}

	// Writes must not land in the ancestor.
	_ = RemoveSparseCheckout(child)
	if got := ancestorConfig(t, ancestor, "core.sparseCheckout"); got != "" {
		t.Errorf("RemoveSparseCheckout wrote core.sparseCheckout=%q into the ancestor", got)
	}

	_ = InitSparseCheckout(child, []string{"docs"})
	if got := ancestorConfig(t, ancestor, "core.sparseCheckout"); got != "" {
		t.Errorf("InitSparseCheckout wrote core.sparseCheckout=%q into the ancestor", got)
	}
	if _, err := os.Stat(filepath.Join(ancestor, ".git", "info", "sparse-checkout")); err == nil {
		t.Error("InitSparseCheckout wrote a sparse-checkout file into the ancestor")
	}
}

// TestIsValidSubmoduleReferenceDoesNotEscape guards the check that decides
// whether a path may serve as a --reference for submodule update. A leaked
// answer would point git at the wrong repository's objects.
func TestIsValidSubmoduleReferenceDoesNotEscape(t *testing.T) {
	_, child := newAncestorRepo(t)

	if isValidSubmoduleReference(child) {
		t.Error("isValidSubmoduleReference accepted a non-repository path")
	}
}

// TestHasTrackedGitmodulesDoesNotEscape guards the check that gates submodule
// initialization. A leaked answer would run submodule init against the wrong
// repository.
func TestHasTrackedGitmodulesDoesNotEscape(t *testing.T) {
	ancestor, child := newAncestorRepo(t)

	// Track a .gitmodules in the ancestor so a leak produces a true answer.
	gm := filepath.Join(ancestor, ".gitmodules")
	if err := os.WriteFile(gm, []byte("[submodule \"x\"]\n\tpath = x\n\turl = ./x\n"), 0o644); err != nil {
		t.Fatalf("write .gitmodules: %v", err)
	}
	for _, args := range [][]string{{"add", ".gitmodules"}, {"commit", "-q", "-m", "add gitmodules"}} {
		cmd := exec.Command("git", append([]string{"-C", ancestor}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	// Give the child its own untracked .gitmodules: os.Stat passes, so the
	// decision falls to the tracked-in-index check, which is the part that
	// can escape.
	if err := os.WriteFile(filepath.Join(child, ".gitmodules"), []byte(""), 0o644); err != nil {
		t.Fatalf("write child .gitmodules: %v", err)
	}

	if hasTrackedGitmodules(child) {
		t.Error("hasTrackedGitmodules answered from the ancestor's index for a non-repository path")
	}
}
