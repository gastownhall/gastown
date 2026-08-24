package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// gitOut runs git in dir and returns its trimmed output. It deliberately
// bypasses the Git wrapper so these tests observe the production code path
// without the wrapper's own guards influencing fixture setup or assertions.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newTownLikeRoot builds a directory that is simultaneously a git repository
// and a Gas Town root. This is the exact shape that triggered the historical
// incident where `gt rig add` wrote into the town's own .git: a town
// initialized with --git, with rigs cloned into subdirectories beneath it.
func newTownLikeRoot(t *testing.T) string {
	t.Helper()
	town := t.TempDir()

	runGit(t, town, "init", "-q", ".")
	runGit(t, town, "config", "user.email", "town@test.local")
	runGit(t, town, "config", "user.name", "Town Test")
	if err := os.WriteFile(filepath.Join(town, "README"), []byte("town\n"), 0o644); err != nil {
		t.Fatalf("writing town README: %v", err)
	}
	runGit(t, town, "add", "-A")
	runGit(t, town, "commit", "-q", "-m", "town init")

	// mayor/rigs.json marks this as a town root for isTownRoot().
	mayorDir := filepath.Join(town, "mayor")
	if err := os.MkdirAll(mayorDir, 0o755); err != nil {
		t.Fatalf("creating mayor dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("writing rigs.json: %v", err)
	}

	return town
}

// newSourceRepoWithGithooks creates a clonable source repository that carries a
// tracked .githooks directory, so configureHooksPath is actually exercised
// after the clone instead of returning early.
func newSourceRepoWithGithooks(t *testing.T) string {
	t.Helper()
	src := t.TempDir()

	runGit(t, src, "init", "-q", ".")
	runGit(t, src, "config", "user.email", "src@test.local")
	runGit(t, src, "config", "user.name", "Source Test")

	hooksDir := filepath.Join(src, ".githooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("creating .githooks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("writing pre-push hook: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatalf("writing source file: %v", err)
	}
	runGit(t, src, "add", "-A")
	runGit(t, src, "commit", "-q", "-m", "source init")

	return src
}

// townConfigValue reads a config key straight from the town's .git directory.
// Using --git-dir (rather than -C) keeps the assertion itself immune to the
// discovery escape it is meant to detect.
func townConfigValue(t *testing.T, town, key string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", filepath.Join(town, ".git"), "config", "--get", key)
	out, err := cmd.Output()
	if err != nil {
		// exit 1 means the key is unset, which is the expected healthy state.
		return ""
	}
	return strings.TrimSpace(string(out))
}

// TestCloneDoesNotMutateAncestorTownRepo is the regression guard for the
// incident described in b73ee919: cloning a rig into a subdirectory of a
// git-initialized town root must never write configuration into the town's own
// repository.
//
// The historical fix staged the clone in os.MkdirTemp("") and moved it into
// place, which hid the defect rather than removing it. The actual escape is in
// post-clone configuration running `git -C <path>` against a path that git does
// not recognize as a repository, at which point discovery walks up and finds
// the town.
func TestCloneDoesNotMutateAncestorTownRepo(t *testing.T) {
	town := newTownLikeRoot(t)
	src := newSourceRepoWithGithooks(t)

	townHeadBefore := gitOut(t, town, "rev-parse", "HEAD")
	townStatusBefore := gitOut(t, town, "status", "--porcelain", "--untracked-files=no")

	dest := filepath.Join(town, "myrig", "clone")
	g := NewGit(town)
	if err := g.Clone(src, dest); err != nil {
		t.Fatalf("Clone into town subdirectory: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
		t.Fatalf("clone destination is not a repository: %v", err)
	}

	// The town's own repository must be untouched.
	if got := townConfigValue(t, town, "core.hooksPath"); got != "" {
		t.Errorf("clone wrote core.hooksPath=%q into the town repository; discovery escaped to the ancestor repo", got)
	}
	if got := townConfigValue(t, town, "core.sparseCheckout"); got != "" {
		t.Errorf("clone wrote core.sparseCheckout=%q into the town repository; discovery escaped to the ancestor repo", got)
	}
	if _, err := os.Stat(filepath.Join(town, ".git", "info", "sparse-checkout")); err == nil {
		t.Error("clone wrote a sparse-checkout file into the town repository; discovery escaped to the ancestor repo")
	}
	if got := gitOut(t, town, "rev-parse", "HEAD"); got != townHeadBefore {
		t.Errorf("town HEAD moved: before %s, after %s", townHeadBefore, got)
	}
	// The clone legitimately adds myrig/ as an untracked path; what must not
	// change is the state of anything the town already tracked.
	if got := gitOut(t, town, "status", "--porcelain", "--untracked-files=no"); got != townStatusBefore {
		t.Errorf("town tracked files changed:\nbefore:\n%s\nafter:\n%s", townStatusBefore, got)
	}

	// The clone itself must still receive its hooks configuration.
	cloneHooks := gitOut(t, dest, "config", "--get", "core.hooksPath")
	if cloneHooks != ".githooks" {
		t.Errorf("clone core.hooksPath = %q, want .githooks", cloneHooks)
	}
}

// TestCloneBareDoesNotMutateAncestorTownRepo covers the bare-clone branch of
// cloneInternal, which is the path `gt rig add` uses for <rig>/.repo.git.
func TestCloneBareDoesNotMutateAncestorTownRepo(t *testing.T) {
	town := newTownLikeRoot(t)
	src := newSourceRepoWithGithooks(t)

	townHeadBefore := gitOut(t, town, "rev-parse", "HEAD")

	dest := filepath.Join(town, "myrig", ".repo.git")
	g := NewGit(town)
	if err := g.CloneBare(src, dest); err != nil {
		t.Fatalf("CloneBare into town subdirectory: %v", err)
	}

	if got := townConfigValue(t, town, "remote.origin.fetch"); got != "" {
		t.Errorf("bare clone wrote remote.origin.fetch=%q into the town repository", got)
	}
	if got := townConfigValue(t, town, "core.hooksPath"); got != "" {
		t.Errorf("bare clone wrote core.hooksPath=%q into the town repository", got)
	}
	if got := gitOut(t, town, "rev-parse", "HEAD"); got != townHeadBefore {
		t.Errorf("town HEAD moved: before %s, after %s", townHeadBefore, got)
	}

	refspec := gitOut(t, dest, "config", "--get", "remote.origin.fetch")
	if refspec == "" {
		t.Error("bare clone did not receive its origin refspec")
	}
}

// TestCloneDoesNotUseSystemTempDir enforces the operator constraint that clone
// staging must not touch the system temp directory. /tmp frequently lives on a
// different, smaller filesystem than the town root; staging multi-gigabyte
// repositories there costs a full cross-device copy and risks exhausting the
// partition that also holds /, /var/log and container storage.
func TestCloneDoesNotUseSystemTempDir(t *testing.T) {
	probe := t.TempDir()
	t.Setenv("TMPDIR", probe)

	town := newTownLikeRoot(t)
	src := newSourceRepoWithGithooks(t)

	// Staging directories are removed by a deferred cleanup, so a check that
	// runs after Clone() returns would find the temp dir empty and pass even
	// when staging did happen. Sample continuously while the clone runs.
	stop := make(chan struct{})
	seen := make(chan []string, 1)
	go func() {
		var found []string
		for {
			select {
			case <-stop:
				seen <- found
				return
			default:
			}
			entries, err := os.ReadDir(probe)
			if err == nil {
				for _, e := range entries {
					name := e.Name()
					if !slices.Contains(found, name) {
						found = append(found, name)
					}
				}
			}
		}
	}()

	dest := filepath.Join(town, "myrig", "clone")
	g := NewGit(town)
	err := g.Clone(src, dest)
	close(stop)
	observed := <-seen

	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if len(observed) != 0 {
		t.Errorf("clone staged %d entries in the system temp dir (%v); staging must happen next to the destination",
			len(observed), observed)
	}
}
