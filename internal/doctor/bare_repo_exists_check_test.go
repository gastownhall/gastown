package doctor

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/git"
)

func TestBareRepoExistsCheck_Name(t *testing.T) {
	check := NewBareRepoExistsCheck()
	if check.Name() != "bare-repo-exists" {
		t.Errorf("expected name 'bare-repo-exists', got %q", check.Name())
	}
	if !check.CanFix() {
		t.Error("expected CanFix to return true")
	}
}

func TestBareRepoExistsCheck_NoRig(t *testing.T) {
	check := NewBareRepoExistsCheck()
	ctx := &CheckContext{TownRoot: t.TempDir(), RigName: ""}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when no rig specified, got %v", result.Status)
	}
}

func TestBareRepoExistsCheck_BareRepoExists(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	// Create a real bare repo so the structural health check passes.
	bareRepo := initBareRepoWithRemote(t, rigDir, "https://github.com/example/repo.git")
	setupWorktreeRef(t, rigDir, bareRepo)

	check := NewBareRepoExistsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when .repo.git exists, got %v: %s", result.Status, result.Message)
	}
}

func TestBareRepoExistsCheck_NoBareRepoNoWorktrees(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	// Create refinery/rig with a .git directory (not a worktree)
	refineryRig := filepath.Join(rigDir, "refinery", "rig", ".git")
	if err := os.MkdirAll(refineryRig, 0755); err != nil {
		t.Fatal(err)
	}

	check := NewBareRepoExistsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when no worktrees depend on .repo.git, got %v", result.Status)
	}
}

func TestBareRepoExistsCheck_MissingBareRepo(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	// Create refinery/rig with a .git file pointing to missing .repo.git
	refineryRig := filepath.Join(rigDir, "refinery", "rig")
	if err := os.MkdirAll(refineryRig, 0755); err != nil {
		t.Fatal(err)
	}

	gitContent := "gitdir: " + filepath.Join(rigDir, ".repo.git", "worktrees", "rig") + "\n"
	if err := os.WriteFile(filepath.Join(refineryRig, ".git"), []byte(gitContent), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBareRepoExistsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Errorf("expected StatusError when .repo.git is missing, got %v", result.Status)
	}
	if !strings.Contains(result.Message, "missing .repo.git") {
		t.Errorf("expected message about missing .repo.git, got %q", result.Message)
	}
	if len(result.Details) < 2 {
		t.Errorf("expected at least 2 details (bare repo path + worktree), got %d", len(result.Details))
	}
}

func TestBareRepoExistsCheck_MultipleWorktreesMissing(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	bareRepoTarget := filepath.Join(rigDir, ".repo.git")

	// Create refinery/rig worktree
	refineryRig := filepath.Join(rigDir, "refinery", "rig")
	if err := os.MkdirAll(refineryRig, 0755); err != nil {
		t.Fatal(err)
	}
	gitContent := "gitdir: " + filepath.Join(bareRepoTarget, "worktrees", "rig") + "\n"
	if err := os.WriteFile(filepath.Join(refineryRig, ".git"), []byte(gitContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create polecat worktree
	polecatDir := filepath.Join(rigDir, "polecats", "worker1", rigName)
	if err := os.MkdirAll(polecatDir, 0755); err != nil {
		t.Fatal(err)
	}
	polecatGit := "gitdir: " + filepath.Join(bareRepoTarget, "worktrees", "worker1") + "\n"
	if err := os.WriteFile(filepath.Join(polecatDir, ".git"), []byte(polecatGit), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBareRepoExistsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Errorf("expected StatusError, got %v", result.Status)
	}
	if !strings.Contains(result.Message, "2 worktree") {
		t.Errorf("expected message about 2 worktrees, got %q", result.Message)
	}
}

func TestBareRepoExistsCheck_RelativeGitdir(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	// Create refinery/rig with a relative .git reference
	refineryRig := filepath.Join(rigDir, "refinery", "rig")
	if err := os.MkdirAll(refineryRig, 0755); err != nil {
		t.Fatal(err)
	}

	// Relative path from refinery/rig/ to .repo.git/worktrees/rig
	gitContent := "gitdir: ../../.repo.git/worktrees/rig\n"
	if err := os.WriteFile(filepath.Join(refineryRig, ".git"), []byte(gitContent), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBareRepoExistsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Errorf("expected StatusError for broken relative gitdir, got %v", result.Status)
	}
}

func TestBareRepoExistsCheck_NonRepoGitWorktree(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	// Create refinery/rig with a .git file pointing to something other than .repo.git
	refineryRig := filepath.Join(rigDir, "refinery", "rig")
	if err := os.MkdirAll(refineryRig, 0755); err != nil {
		t.Fatal(err)
	}

	gitContent := "gitdir: /some/other/path/worktrees/rig\n"
	if err := os.WriteFile(filepath.Join(refineryRig, ".git"), []byte(gitContent), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBareRepoExistsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	// Worktree doesn't reference .repo.git, so this should pass
	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when worktrees don't reference .repo.git, got %v", result.Status)
	}
}

// initBareRepoWithRemote creates a real git bare repo with an origin remote.
// Returns the bare repo path.
func initBareRepoWithRemote(t *testing.T, rigDir, fetchURL string) string {
	t.Helper()
	bareRepo := filepath.Join(rigDir, ".repo.git")
	cmd := exec.Command("git", "init", "--bare", bareRepo)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare failed: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "-C", bareRepo, "remote", "add", "origin", fetchURL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add failed: %v\n%s", err, out)
	}
	return bareRepo
}

// setupWorktreeRef creates a refinery/rig directory with a .git file pointing to .repo.git.
func setupWorktreeRef(t *testing.T, rigDir, bareRepo string) {
	t.Helper()
	refineryRig := filepath.Join(rigDir, "refinery", "rig")
	if err := os.MkdirAll(refineryRig, 0755); err != nil {
		t.Fatal(err)
	}
	worktreeDir := filepath.Join(bareRepo, "worktrees", "rig")
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		t.Fatal(err)
	}
	gitContent := "gitdir: " + filepath.Join(bareRepo, "worktrees", "rig") + "\n"
	if err := os.WriteFile(filepath.Join(refineryRig, ".git"), []byte(gitContent), 0644); err != nil {
		t.Fatal(err)
	}
}

// writeConfigJSON writes a config.json with optional push_url.
func writeConfigJSON(t *testing.T, rigDir, gitURL, pushURL string) {
	t.Helper()
	cfg := map[string]string{"git_url": gitURL}
	if pushURL != "" {
		cfg["push_url"] = pushURL
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestBareRepoExistsCheck_PushURLMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	fetchURL := "https://github.com/example/repo.git"
	bareRepo := initBareRepoWithRemote(t, rigDir, fetchURL)

	// Set a push URL on the bare repo that differs from config
	cmd := exec.Command("git", "-C", bareRepo, "remote", "set-url", "--push", "origin", "https://github.com/user/wrong-fork.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("set-url --push failed: %v\n%s", err, out)
	}

	// Config says the push URL should be something else
	writeConfigJSON(t, rigDir, fetchURL, "https://github.com/user/correct-fork.git")
	setupWorktreeRef(t, rigDir, bareRepo)

	check := NewBareRepoExistsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning for push URL mismatch, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "push URL") {
		t.Errorf("expected message about push URL, got %q", result.Message)
	}
}

func TestBareRepoExistsCheck_LegacyConfigIgnoresPushURL(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	fetchURL := "https://github.com/example/repo.git"
	bareRepo := initBareRepoWithRemote(t, rigDir, fetchURL)

	// Set a push URL on the bare repo (may be from a pre-push_url-feature setup)
	cmd := exec.Command("git", "-C", bareRepo, "remote", "set-url", "--push", "origin", "https://github.com/user/old-fork.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("set-url --push failed: %v\n%s", err, out)
	}

	// Config has NO push_url — legacy config that predates the push_url feature.
	// Doctor should NOT flag a mismatch; the existing push URL may be intentional.
	writeConfigJSON(t, rigDir, fetchURL, "")
	setupWorktreeRef(t, rigDir, bareRepo)

	check := NewBareRepoExistsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK for legacy config (no push_url field), got %v: %s", result.Status, result.Message)
	}
}

func TestBareRepoExistsCheck_PushURLMatchesConfig(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	fetchURL := "https://github.com/example/repo.git"
	pushURL := "https://github.com/user/fork.git"
	bareRepo := initBareRepoWithRemote(t, rigDir, fetchURL)

	// Set push URL matching config
	cmd := exec.Command("git", "-C", bareRepo, "remote", "set-url", "--push", "origin", pushURL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("set-url --push failed: %v\n%s", err, out)
	}

	writeConfigJSON(t, rigDir, fetchURL, pushURL)
	setupWorktreeRef(t, rigDir, bareRepo)

	check := NewBareRepoExistsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when push URL matches config, got %v: %s", result.Status, result.Message)
	}
}

func TestBareRepoExistsCheck_FixPushURLMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	fetchURL := "https://github.com/example/repo.git"
	correctPushURL := "https://github.com/user/correct-fork.git"
	bareRepo := initBareRepoWithRemote(t, rigDir, fetchURL)

	// Set wrong push URL
	cmd := exec.Command("git", "-C", bareRepo, "remote", "set-url", "--push", "origin", "https://github.com/user/wrong-fork.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("set-url --push failed: %v\n%s", err, out)
	}

	writeConfigJSON(t, rigDir, fetchURL, correctPushURL)
	setupWorktreeRef(t, rigDir, bareRepo)

	check := NewBareRepoExistsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	// Run should detect mismatch
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v: %s", result.Status, result.Message)
	}

	// Fix should correct it
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}

	// Re-run should show OK
	check2 := NewBareRepoExistsCheck()
	result2 := check2.Run(ctx)
	if result2.Status != StatusOK {
		t.Errorf("expected StatusOK after fix, got %v: %s", result2.Status, result2.Message)
	}
}

// corruptBareRepo simulates the recurring partial-shell corruption mode:
// .repo.git is reduced to objects/ + worktrees/ (no HEAD, refs, config, hooks, info).
func corruptBareRepo(t *testing.T, bareRepo string) {
	t.Helper()
	for _, name := range []string{"HEAD", "config", "refs", "hooks", "info", "packed-refs"} {
		_ = os.RemoveAll(filepath.Join(bareRepo, name))
	}
}

func TestBareRepoExistsCheck_CorruptBareRepoDetected(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	bareRepo := initBareRepoWithRemote(t, rigDir, "https://github.com/example/repo.git")
	setupWorktreeRef(t, rigDir, bareRepo)
	corruptBareRepo(t, bareRepo)

	check := NewBareRepoExistsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Fatalf("expected StatusError for corrupt bare repo, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(strings.ToLower(result.Message), "corrupt") &&
		!strings.Contains(strings.ToLower(result.Message), "unusable") {
		t.Errorf("expected message to mention corruption/unusable, got %q", result.Message)
	}
	if result.FixHint == "" {
		t.Error("expected non-empty FixHint for corrupt bare repo")
	}
}

func TestBareRepoExistsCheck_FixCorruptBareRepoReclones(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	// Stand up an upstream bare repo on disk to clone from.
	upstream := filepath.Join(tmpDir, "upstream.git")
	if out, err := exec.Command("git", "init", "--bare", upstream).CombinedOutput(); err != nil {
		t.Fatalf("git init upstream failed: %v\n%s", err, out)
	}
	// Seed upstream with a commit so clone has a default branch.
	work := filepath.Join(tmpDir, "work")
	if out, err := exec.Command("git", "init", "-b", "main", work).CombinedOutput(); err != nil {
		t.Fatalf("git init work failed: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(work, "README"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", work, "-c", "user.email=t@t", "-c", "user.name=t", "add", "README"},
		{"-C", work, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "init"},
		{"-C", work, "remote", "add", "origin", upstream},
		{"-C", work, "push", "origin", "main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	// Set up the rig with a corrupt .repo.git pointing at our upstream.
	bareRepo := initBareRepoWithRemote(t, rigDir, upstream)
	writeConfigJSON(t, rigDir, upstream, "")
	setupWorktreeRef(t, rigDir, bareRepo)
	corruptBareRepo(t, bareRepo)

	check := NewBareRepoExistsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Fatalf("expected StatusError for corrupt repo, got %v: %s", result.Status, result.Message)
	}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}

	// The bare repo must be re-cloned and pass the structural health check.
	if err := bareRepoHealth(bareRepo); err != nil {
		t.Fatalf("bare repo still unhealthy after Fix: %v", err)
	}
	// Re-running the check should now return OK.
	check2 := NewBareRepoExistsCheck()
	if result2 := check2.Run(ctx); result2.Status != StatusOK {
		t.Errorf("expected StatusOK after Fix re-cloned, got %v: %s", result2.Status, result2.Message)
	}
}

func TestBareRepoRefspecCheck_CorruptBareRepoErrors(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	bareRepo := initBareRepoWithRemote(t, rigDir, "https://github.com/example/repo.git")
	corruptBareRepo(t, bareRepo)

	check := NewBareRepoRefspecCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Fatalf("expected StatusError on corrupt bare repo, got %v: %s", result.Status, result.Message)
	}
}

func TestBareRepoRefspecCheck_FixRefusesCorrupt(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	bareRepo := initBareRepoWithRemote(t, rigDir, "https://github.com/example/repo.git")
	corruptBareRepo(t, bareRepo)

	check := NewBareRepoRefspecCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	if err := check.Fix(ctx); err == nil {
		t.Fatal("expected Fix to refuse writing config to a corrupt bare repo, got nil error")
	}
	// Critically: Fix must NOT have created .repo.git/config (the original bug).
	if _, err := os.Stat(filepath.Join(bareRepo, "config")); err == nil {
		t.Error("Fix wrote .repo.git/config on a corrupt bare repo — this is the bug it must prevent")
	}
}

func TestBareRepoExistsCheck_FixCorruptPreservesWorktreeHead(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	upstream := filepath.Join(tmpDir, "upstream.git")
	if out, err := exec.Command("git", "init", "--bare", upstream).CombinedOutput(); err != nil {
		t.Fatalf("git init upstream: %v\n%s", err, out)
	}
	work := filepath.Join(tmpDir, "work")
	for _, args := range [][]string{
		{"init", "-b", "main", work},
		{"-C", work, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init"},
		{"-C", work, "remote", "add", "origin", upstream},
		{"-C", work, "push", "origin", "main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	bareRepo := initBareRepoWithRemote(t, rigDir, upstream)
	writeConfigJSON(t, rigDir, upstream, "")
	setupWorktreeRef(t, rigDir, bareRepo)
	// Pre-populate the worktree's HEAD with a non-default branch ref.
	customHead := "ref: refs/heads/feature-branch\n"
	headFile := filepath.Join(bareRepo, "worktrees", "rig", "HEAD")
	if err := os.WriteFile(headFile, []byte(customHead), 0644); err != nil {
		t.Fatal(err)
	}
	corruptBareRepo(t, bareRepo)

	check := NewBareRepoExistsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}
	if result := check.Run(ctx); result.Status != StatusError {
		t.Fatalf("expected StatusError, got %v: %s", result.Status, result.Message)
	}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}

	// After Fix, the re-registered worktree HEAD should match the captured value,
	// not the default "refs/heads/main".
	got, err := os.ReadFile(headFile)
	if err != nil {
		t.Fatalf("reading re-registered HEAD: %v", err)
	}
	if string(got) != customHead {
		t.Errorf("expected captured HEAD %q after re-clone, got %q", customHead, string(got))
	}
}

func TestBareRepoExistsCheck_FixSkipsRepairedBetweenRunAndFix(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	bareRepo := initBareRepoWithRemote(t, rigDir, "https://github.com/example/repo.git")
	setupWorktreeRef(t, rigDir, bareRepo)
	corruptBareRepo(t, bareRepo)

	check := NewBareRepoExistsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}
	if result := check.Run(ctx); result.Status != StatusError {
		t.Fatalf("expected StatusError, got %v: %s", result.Status, result.Message)
	}

	// Operator manually repairs the repo between Run and Fix (TOCTOU window).
	// Re-init the bare repo in place.
	if err := os.RemoveAll(bareRepo); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "--bare", bareRepo).CombinedOutput(); err != nil {
		t.Fatalf("re-init: %v\n%s", err, out)
	}
	// Capture the new HEAD inode/mtime to verify Fix doesn't touch it.
	infoBefore, err := os.Stat(filepath.Join(bareRepo, "HEAD"))
	if err != nil {
		t.Fatal(err)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}

	infoAfter, err := os.Stat(filepath.Join(bareRepo, "HEAD"))
	if err != nil {
		t.Fatalf("HEAD missing after Fix — Fix should have skipped a healthy repo: %v", err)
	}
	if !infoBefore.ModTime().Equal(infoAfter.ModTime()) {
		t.Errorf("Fix re-created the bare repo even though it was healthy at Fix time")
	}
}

func TestBareRepoHealth_RejectsNonBareRepo(t *testing.T) {
	tmpDir := t.TempDir()
	work := filepath.Join(tmpDir, "work")
	if out, err := exec.Command("git", "init", "-b", "main", work).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	// Point bareRepoHealth at the .git directory of a non-bare repo.
	if err := bareRepoHealth(filepath.Join(work, ".git")); err == nil {
		t.Error("expected bareRepoHealth to reject non-bare .git directory")
	}
}

func TestBareRepoRefspecCheck_HealthyRepoStillFixes(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	bareRepo := initBareRepoWithRemote(t, rigDir, "https://github.com/example/repo.git")
	// Strip the refspec so Fix has work to do.
	if out, err := exec.Command("git", "-C", bareRepo, "config", "--unset", "remote.origin.fetch").CombinedOutput(); err != nil {
		t.Fatalf("unset refspec failed: %v\n%s", err, out)
	}

	check := NewBareRepoRefspecCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	if result := check.Run(ctx); result.Status != StatusError {
		t.Fatalf("expected StatusError when refspec missing, got %v: %s", result.Status, result.Message)
	}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed on healthy repo: %v", err)
	}
	if result := check.Run(ctx); result.Status != StatusOK {
		t.Errorf("expected StatusOK after Fix, got %v: %s", result.Status, result.Message)
	}
}

func TestBareRepoExistsCheck_FixPreservesLegacyPushURL(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"
	rigDir := filepath.Join(tmpDir, rigName)

	fetchURL := "https://github.com/example/repo.git"
	legacyPushURL := "https://github.com/user/old-fork.git"
	bareRepo := initBareRepoWithRemote(t, rigDir, fetchURL)

	// Set a push URL (from a pre-push_url-feature setup)
	cmd := exec.Command("git", "-C", bareRepo, "remote", "set-url", "--push", "origin", legacyPushURL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("set-url --push failed: %v\n%s", err, out)
	}

	// Config has no push_url — legacy config
	writeConfigJSON(t, rigDir, fetchURL, "")
	setupWorktreeRef(t, rigDir, bareRepo)

	check := NewBareRepoExistsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: rigName}

	// Run should NOT flag a mismatch for legacy configs
	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Fatalf("expected StatusOK for legacy config, got %v: %s", result.Status, result.Message)
	}

	// Verify the push URL is preserved (not cleared)
	bareGit := git.NewGitWithDir(bareRepo, "")
	actualPush, err := bareGit.GetPushURL("origin")
	if err != nil {
		t.Fatalf("GetPushURL failed: %v", err)
	}
	if actualPush != legacyPushURL {
		t.Errorf("expected push URL to be preserved as %q, got %q", legacyPushURL, actualPush)
	}
}
