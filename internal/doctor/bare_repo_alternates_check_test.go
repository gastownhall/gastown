package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBareRepoAlternatesCheck_Name(t *testing.T) {
	check := NewBareRepoAlternatesCheck()
	if check.Name() != "bare-repo-alternates" {
		t.Errorf("expected name 'bare-repo-alternates', got %q", check.Name())
	}
	if !check.CanFix() {
		t.Error("expected CanFix to return true")
	}
}

func TestBareRepoAlternatesCheck_NoRig(t *testing.T) {
	check := NewBareRepoAlternatesCheck()
	ctx := &CheckContext{TownRoot: t.TempDir(), RigName: ""}
	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when no rig specified, got %v", result.Status)
	}
}

func TestBareRepoAlternatesCheck_NoBareRepo(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "testrig"), 0755); err != nil {
		t.Fatal(err)
	}
	check := NewBareRepoAlternatesCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: "testrig"}
	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when .repo.git is missing, got %v: %s", result.Status, result.Message)
	}
}

func TestBareRepoAlternatesCheck_NoAlternatesFile(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	bareRepo := filepath.Join(rigDir, ".repo.git")
	runGit(t, "", "init", "--bare", bareRepo)

	check := NewBareRepoAlternatesCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: "testrig"}
	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK with no alternates configured, got %v: %s", result.Status, result.Message)
	}
}

func TestBareRepoAlternatesCheck_LiveAlternateOK(t *testing.T) {
	tmpDir := t.TempDir()
	externalRepo := seedRepo(t, tmpDir, "external.git", "hello")

	rigDir := filepath.Join(tmpDir, "testrig")
	bareRepo := filepath.Join(rigDir, ".repo.git")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}
	runGit(t, "", "clone", "--bare", "--reference-if-able", externalRepo, externalRepo, bareRepo)
	assertHasAlternates(t, bareRepo)

	check := NewBareRepoAlternatesCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: "testrig"}
	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when the alternate target still exists, got %v: %s", result.Status, result.Message)
	}
}

// TestBareRepoAlternatesCheck_ReachableAfterRefetch is the "safely internalize"
// fixture: the bare repo was cloned with --reference-if-able against an external
// checkout (so it never copied the objects for itself), the external checkout is
// then deleted, and origin still has everything — so Fix must refetch the missing
// objects, prove every registered ref reachable, and drop the stale alternate.
func TestBareRepoAlternatesCheck_ReachableAfterRefetch(t *testing.T) {
	tmpDir := t.TempDir()

	originRepo := seedRepo(t, tmpDir, "origin.git", "hello")
	externalRepo := filepath.Join(tmpDir, "external.git")
	runGit(t, "", "clone", "--bare", originRepo, externalRepo)

	rigDir := filepath.Join(tmpDir, "testrig")
	bareRepo := filepath.Join(rigDir, ".repo.git")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}
	runGit(t, "", "clone", "--bare", "--reference-if-able", externalRepo, originRepo, bareRepo)
	runGit(t, bareRepo, "remote", "set-url", "origin", originRepo)
	assertHasAlternates(t, bareRepo)

	// Simulate the external checkout being deleted from under the rig.
	if err := os.RemoveAll(externalRepo); err != nil {
		t.Fatal(err)
	}

	check := NewBareRepoAlternatesCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: "testrig"}

	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Fatalf("expected StatusError for a stale alternate, got %v: %s", result.Status, result.Message)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix should recover objects still present on origin: %v", err)
	}

	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Fatalf("expected StatusOK after fix, got %v: %s", result.Status, result.Message)
	}
	if _, err := os.Stat(alternatesFilePath(bareRepo)); !os.IsNotExist(err) {
		t.Errorf("expected alternates file removed after successful internalization, stat err=%v", err)
	}
	out, err := exec.Command("git", "-C", bareRepo, "log", "--oneline", "main").CombinedOutput()
	if err != nil {
		t.Fatalf("branch unreadable after fix: %v\n%s", err, out)
	}
}

// TestBareRepoAlternatesCheck_UnreachableRefusesFix is the fail-closed fixture:
// the external checkout is deleted and origin cannot supply the missing objects
// either (matching a purely local, never-pushed branch). Fix must refuse and
// leave the alternates file byte-identical.
func TestBareRepoAlternatesCheck_UnreachableRefusesFix(t *testing.T) {
	tmpDir := t.TempDir()

	externalRepo := seedRepo(t, tmpDir, "external.git", "only-in-external")

	rigDir := filepath.Join(tmpDir, "testrig")
	bareRepo := filepath.Join(rigDir, ".repo.git")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}
	runGit(t, "", "clone", "--bare", "--reference-if-able", externalRepo, externalRepo, bareRepo)
	// No reachable origin — nothing to refetch from.
	runGit(t, bareRepo, "remote", "set-url", "origin", filepath.Join(tmpDir, "does-not-exist.git"))
	assertHasAlternates(t, bareRepo)

	origAlternates, err := os.ReadFile(alternatesFilePath(bareRepo))
	if err != nil {
		t.Fatalf("expected alternates file before repair: %v", err)
	}

	if err := os.RemoveAll(externalRepo); err != nil {
		t.Fatal(err)
	}

	check := NewBareRepoAlternatesCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: "testrig"}

	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Fatalf("expected StatusError for a stale alternate, got %v: %s", result.Status, result.Message)
	}

	if err := check.Fix(ctx); err == nil {
		t.Fatal("expected Fix to refuse when required objects are unreachable")
	}

	after, err := os.ReadFile(alternatesFilePath(bareRepo))
	if err != nil {
		t.Fatalf("alternates file missing after refused fix: %v", err)
	}
	if string(after) != string(origAlternates) {
		t.Errorf("alternates file mutated despite refused fix:\nbefore: %q\nafter:  %q", origAlternates, after)
	}
}

// seedRepo creates a bare repo at tmpDir/name containing one commit on "main"
// with the given file content, returning the bare repo's path.
func seedRepo(t *testing.T, tmpDir, name, content string) string {
	t.Helper()
	bareRepo := filepath.Join(tmpDir, name)
	runGit(t, "", "init", "--bare", "-b", "main", bareRepo)

	seed := filepath.Join(tmpDir, name+".seed")
	runGit(t, "", "clone", bareRepo, seed)
	if err := os.WriteFile(filepath.Join(seed, "file.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "file.txt")
	runGit(t, seed, "commit", "-m", "seed commit")
	runGit(t, seed, "push", "origin", "HEAD:refs/heads/main")
	return bareRepo
}

// assertHasAlternates fails the test if bareRepo has no objects/info/alternates file.
func assertHasAlternates(t *testing.T, bareRepo string) {
	t.Helper()
	if _, err := os.Stat(alternatesFilePath(bareRepo)); err != nil {
		t.Fatalf("expected alternates file to exist: %v", err)
	}
}
