package version

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// --- git-backed test helpers ---

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitCommit writes file and creates a commit, returning its full hash.
func gitCommit(t *testing.T, dir, file, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "--no-gpg-sign", "-m", "c-"+file)
	return gitRun(t, dir, "rev-parse", "HEAD")
}

func newGitRepo(t *testing.T) string {
	t.Helper()
	// These tests create tiny temp-dir repos and shell out to git a handful
	// of times — fast and deterministic, so they run even under -short. CI
	// runs `-short`; skipping here left stale.go's staleness logic at 0%
	// patch coverage (GH#4034 follow-up). Only skip if git is unavailable.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

// setBinaryCommit overrides the build-time commit for the duration of the test.
func setBinaryCommit(t *testing.T, c string) {
	t.Helper()
	orig := Commit
	t.Cleanup(func() { Commit = orig })
	Commit = c
}

func TestShortCommit(t *testing.T) {
	tests := []struct {
		name   string
		hash   string
		expect string
	}{
		{"full SHA", "abcdef1234567890abcdef1234567890abcdef12", "abcdef123456"},
		{"exactly 12", "abcdef123456", "abcdef123456"},
		{"short hash", "abcdef", "abcdef"},
		{"empty", "", ""},
		{"13 chars", "abcdef1234567", "abcdef123456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShortCommit(tt.hash)
			if got != tt.expect {
				t.Errorf("ShortCommit(%q) = %q, want %q", tt.hash, got, tt.expect)
			}
		})
	}
}

func TestCommitsMatch(t *testing.T) {
	tests := []struct {
		name   string
		a, b   string
		expect bool
	}{
		{"identical full", "abcdef1234567890", "abcdef1234567890", true},
		{"prefix match short-long", "abcdef1234567", "abcdef1234567890abcd", true},
		{"prefix match long-short", "abcdef1234567890abcd", "abcdef1234567", true},
		{"no match", "abcdef1234567", "1234567abcdef", false},
		{"too short a", "abc", "abcdef1234567", false},
		{"too short b", "abcdef1234567", "abc", false},
		{"both too short", "abc", "abc", false},
		{"exactly 7 chars match", "abcdefg", "abcdefg", true},
		{"exactly 7 chars no match", "abcdefg", "abcdefh", false},
		{"6 chars too short", "abcdef", "abcdef", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := commitsMatch(tt.a, tt.b)
			if got != tt.expect {
				t.Errorf("commitsMatch(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.expect)
			}
		})
	}
}

func TestSetCommit(t *testing.T) {
	original := Commit
	defer func() { Commit = original }()

	SetCommit("abc123def456")
	if Commit != "abc123def456" {
		t.Errorf("SetCommit did not set Commit; got %q", Commit)
	}
}

func TestIsBuildBranch(t *testing.T) {
	tests := []struct {
		branch string
		want   bool
	}{
		{"main", true},
		{"master", true},
		{"carry/operational", true},
		{"carry/staging", true},
		{"carry/", true},
		{"fix/something", false},
		{"feat/new-thing", false},
		{"develop", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.branch, func(t *testing.T) {
			if got := isBuildBranch(tt.branch); got != tt.want {
				t.Errorf("isBuildBranch(%q) = %v, want %v", tt.branch, got, tt.want)
			}
		})
	}
}

func TestCheckStaleBinary_NoCommit(t *testing.T) {
	original := Commit
	defer func() { Commit = original }()

	Commit = ""
	// Force resolveCommitHash to return empty by clearing Commit
	// (vcs.revision from build info may still be set, so this test
	// verifies the error path when no commit is available)
	info := CheckStaleBinary(t.TempDir())
	if info == nil {
		t.Fatal("CheckStaleBinary returned nil")
	}
	// Either we get an error (no commit) or we get a valid result from build info
	// Both are acceptable outcomes
	if info.BinaryCommit == "" && info.Error == nil {
		t.Error("expected error when binary commit is empty")
	}
}

// TestCheckStaleBinary_FeatureBranchBinaryAtMainTip is the GH#4034 regression:
// the resolved worktree is on a feature branch but the binary is at the main
// tip. Before the fix this falsely reported "N commits behind"; now it must be
// reported as not stale (compared against main, not the feature HEAD).
func TestCheckStaleBinary_FeatureBranchBinaryAtMainTip(t *testing.T) {
	dir := newGitRepo(t)
	gitCommit(t, dir, "a.go", "1")
	mainTip := gitCommit(t, dir, "b.go", "2")
	gitRun(t, dir, "branch", "-M", "main")
	gitRun(t, dir, "checkout", "-q", "-b", "feat/x")
	gitCommit(t, dir, "c.go", "unmerged feature work")
	setBinaryCommit(t, mainTip)

	info := CheckStaleBinary(dir)
	if info.Error != nil {
		t.Fatalf("unexpected error: %v", info.Error)
	}
	if info.Skipped {
		t.Fatalf("expected not skipped (main resolvable), got skip: %s", info.SkipReason)
	}
	if info.IsStale {
		t.Errorf("binary is at main tip on a feature branch — must NOT be stale (GH#4034)")
	}
	if info.OnMainBranch {
		t.Errorf("OnMainBranch should be false on feat/x")
	}
	if info.CompareRef != "main" {
		t.Errorf("CompareRef = %q, want \"main\"", info.CompareRef)
	}
	if info.RepoCommit != mainTip {
		t.Errorf("RepoCommit = %q, want main tip %q", info.RepoCommit, mainTip)
	}
}

// TestCheckStaleBinary_FeatureBranchBinaryBehindMain: on a feature branch with
// a binary genuinely behind main — must still be reported stale, counted
// against main (not the feature HEAD).
func TestCheckStaleBinary_FeatureBranchBinaryBehindMain(t *testing.T) {
	dir := newGitRepo(t)
	old := gitCommit(t, dir, "a.go", "1")
	gitCommit(t, dir, "b.go", "2")
	mainTip := gitCommit(t, dir, "c.go", "3")
	gitRun(t, dir, "branch", "-M", "main")
	gitRun(t, dir, "checkout", "-q", "-b", "feat/x")
	gitCommit(t, dir, "d.go", "feature work")
	setBinaryCommit(t, old)

	info := CheckStaleBinary(dir)
	if info.Error != nil {
		t.Fatalf("unexpected error: %v", info.Error)
	}
	if !info.IsStale {
		t.Fatalf("binary behind main must be stale")
	}
	if info.CompareRef != "main" {
		t.Errorf("CompareRef = %q, want \"main\"", info.CompareRef)
	}
	if info.RepoCommit != mainTip {
		t.Errorf("RepoCommit = %q, want main tip %q", info.RepoCommit, mainTip)
	}
	if info.CommitsBehind != 2 {
		t.Errorf("CommitsBehind = %d, want 2 (counted against main)", info.CommitsBehind)
	}
	if !info.IsForward {
		t.Errorf("IsForward should be true (binary is ancestor of main)")
	}
	if info.OnMainBranch {
		t.Errorf("OnMainBranch should be false on feat/x")
	}
}

// TestCheckStaleBinary_OnMainBehind: on a build branch, behind HEAD — the
// pre-existing behavior must be unchanged (compare against HEAD/the branch).
func TestCheckStaleBinary_OnMainBehind(t *testing.T) {
	dir := newGitRepo(t)
	old := gitCommit(t, dir, "a.go", "1")
	tip := gitCommit(t, dir, "b.go", "2")
	gitRun(t, dir, "branch", "-M", "main")
	setBinaryCommit(t, old)

	info := CheckStaleBinary(dir)
	if info.Error != nil {
		t.Fatalf("unexpected error: %v", info.Error)
	}
	if !info.OnMainBranch {
		t.Fatalf("OnMainBranch should be true on main")
	}
	if !info.IsStale {
		t.Fatalf("binary behind main HEAD must be stale")
	}
	if info.CompareRef != "main" {
		t.Errorf("CompareRef = %q, want \"main\"", info.CompareRef)
	}
	if info.RepoCommit != tip {
		t.Errorf("RepoCommit = %q, want %q", info.RepoCommit, tip)
	}
	if info.CommitsBehind != 1 {
		t.Errorf("CommitsBehind = %d, want 1", info.CommitsBehind)
	}
}

// TestCheckStaleBinary_NoBuildBranchSkips: feature branch, no main/master/
// carry/remote — the check must skip rather than diff against feature HEAD.
func TestCheckStaleBinary_NoBuildBranchSkips(t *testing.T) {
	dir := newGitRepo(t)
	c1 := gitCommit(t, dir, "a.go", "1")
	gitRun(t, dir, "branch", "-M", "feature/only")
	setBinaryCommit(t, c1)

	info := CheckStaleBinary(dir)
	if info.Error != nil {
		t.Fatalf("unexpected error: %v", info.Error)
	}
	if !info.Skipped {
		t.Fatalf("expected Skipped when no build-branch ref exists")
	}
	if info.SkipReason == "" {
		t.Errorf("SkipReason should be set when skipped")
	}
	if info.IsStale {
		t.Errorf("IsStale must be false when skipped")
	}
	if info.OnMainBranch {
		t.Errorf("OnMainBranch must be false on feature/only")
	}
}

func TestResolveBuildBranchRef(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping git-backed test in -short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	t.Run("prefers local main", func(t *testing.T) {
		dir := newGitRepo(t)
		c1 := gitCommit(t, dir, "a.go", "1")
		gitRun(t, dir, "branch", "-M", "main")
		gitRun(t, dir, "branch", "carry/operational")
		gitRun(t, dir, "checkout", "-q", "-b", "feat/x")
		ref, ok := resolveBuildBranchRef(dir, c1)
		if !ok || ref != "main" {
			t.Errorf("got (%q,%v), want (\"main\",true)", ref, ok)
		}
	})

	t.Run("routes to carry when binary not on main", func(t *testing.T) {
		dir := newGitRepo(t)
		gitCommit(t, dir, "a.go", "1")
		gitRun(t, dir, "branch", "-M", "main")
		gitRun(t, dir, "checkout", "-q", "-b", "carry/operational")
		carryOnly := gitCommit(t, dir, "b.go", "fork work")
		gitRun(t, dir, "checkout", "-q", "-b", "feat/x")
		ref, ok := resolveBuildBranchRef(dir, carryOnly)
		if !ok || ref != "carry/operational" {
			t.Errorf("got (%q,%v), want (\"carry/operational\",true)", ref, ok)
		}
	})

	t.Run("ambiguous carry is skipped", func(t *testing.T) {
		dir := newGitRepo(t)
		c1 := gitCommit(t, dir, "a.go", "1")
		gitRun(t, dir, "branch", "-M", "feature/only")
		gitRun(t, dir, "branch", "carry/a")
		gitRun(t, dir, "branch", "carry/b")
		if ref, ok := resolveBuildBranchRef(dir, c1); ok {
			t.Errorf("got (%q,%v), want (\"\",false) for ambiguous carry/*", ref, ok)
		}
	})

	t.Run("falls back to origin/main", func(t *testing.T) {
		dir := newGitRepo(t)
		c1 := gitCommit(t, dir, "a.go", "1")
		gitRun(t, dir, "branch", "-M", "feature/only")
		gitRun(t, dir, "update-ref", "refs/remotes/origin/main", c1)
		ref, ok := resolveBuildBranchRef(dir, c1)
		if !ok || ref != "origin/main" {
			t.Errorf("got (%q,%v), want (\"origin/main\",true)", ref, ok)
		}
	})
}

func TestStaleBinaryInfo_Describe(t *testing.T) {
	const (
		bin  = "abc1234567890def"
		repo = "fed0987654321cba"
	)
	tests := []struct {
		name    string
		info    StaleBinaryInfo
		subject string
		want    string
	}{
		{
			name:    "commits behind known",
			info:    StaleBinaryInfo{BinaryCommit: bin, RepoCommit: repo, CompareRef: "main", CommitsBehind: 3},
			subject: "Binary",
			want:    "Binary is 3 commits behind main (built from abc123456789, main at fed098765432)",
		},
		{
			name:    "count unknown falls back to stale wording",
			info:    StaleBinaryInfo{BinaryCommit: bin, RepoCommit: repo, CompareRef: "origin/main", CommitsBehind: 0},
			subject: "gt binary",
			want:    "gt binary is stale (built from abc123456789, origin/main at fed098765432)",
		},
		{
			name:    "short hashes are not truncated",
			info:    StaleBinaryInfo{BinaryCommit: "abc123", RepoCommit: "def456", CompareRef: "carry/ops", CommitsBehind: 1},
			subject: "Binary",
			want:    "Binary is 1 commits behind carry/ops (built from abc123, carry/ops at def456)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.Describe(tt.subject); got != tt.want {
				t.Errorf("Describe(%q) = %q, want %q", tt.subject, got, tt.want)
			}
		})
	}
}
