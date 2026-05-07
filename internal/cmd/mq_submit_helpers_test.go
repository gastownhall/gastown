package cmd

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// fakeResolver is a test double for commitSHAResolver.
type fakeResolver struct {
	currentBranch    string
	currentBranchErr error

	fetchedRefs []string // remote/branch concatenated, in call order
	fetchErr    error

	revs    map[string]string // ref -> sha
	revErrs map[string]error  // ref -> error to return
}

func (f *fakeResolver) CurrentBranch() (string, error) {
	return f.currentBranch, f.currentBranchErr
}

func (f *fakeResolver) FetchBranch(remote, branch string) error {
	f.fetchedRefs = append(f.fetchedRefs, remote+"/"+branch)
	return f.fetchErr
}

func (f *fakeResolver) Rev(ref string) (string, error) {
	if err, ok := f.revErrs[ref]; ok {
		return "", err
	}
	if sha, ok := f.revs[ref]; ok {
		return sha, nil
	}
	return "", errors.New("no such ref: " + ref)
}

func TestResolveCommitSHA(t *testing.T) {
	tests := []struct {
		name           string
		explicitBranch string
		resolver       *fakeResolver
		wantSHA        string
		wantErr        bool
		wantFetched    []string // expected FetchBranch calls
	}{
		{
			name:           "no explicit branch — returns HEAD",
			explicitBranch: "",
			resolver: &fakeResolver{
				currentBranch: "polecat/quartz/co-toog",
				revs:          map[string]string{"HEAD": "deadbeef"},
			},
			wantSHA:     "deadbeef",
			wantFetched: nil, // never fetched
		},
		{
			name:           "explicit branch matches current — returns HEAD without fetching",
			explicitBranch: "polecat/quartz/co-toog",
			resolver: &fakeResolver{
				currentBranch: "polecat/quartz/co-toog",
				revs:          map[string]string{"HEAD": "abc123"},
			},
			wantSHA:     "abc123",
			wantFetched: nil,
		},
		{
			name:           "explicit branch differs from current — fetches and returns origin tip",
			explicitBranch: "polecat/jasper/co-5qy0",
			resolver: &fakeResolver{
				currentBranch: "main",
				revs:          map[string]string{"origin/polecat/jasper/co-5qy0": "0dc8c4b8"},
			},
			wantSHA:     "0dc8c4b8",
			wantFetched: []string{"origin/polecat/jasper/co-5qy0"},
		},
		{
			name:           "fetch fails but Rev succeeds — proceeds with possibly-stale ref",
			explicitBranch: "polecat/jasper/co-5qy0",
			resolver: &fakeResolver{
				currentBranch: "main",
				fetchErr:      errors.New("network unreachable"),
				revs:          map[string]string{"origin/polecat/jasper/co-5qy0": "stale123"},
			},
			wantSHA:     "stale123",
			wantFetched: []string{"origin/polecat/jasper/co-5qy0"},
		},
		{
			name:           "explicit branch differs and remote ref missing — returns error",
			explicitBranch: "polecat/missing/never-pushed",
			resolver: &fakeResolver{
				currentBranch: "main",
				// no entry in revs map → Rev returns "no such ref"
			},
			wantErr:     true,
			wantFetched: []string{"origin/polecat/missing/never-pushed"},
		},
		{
			name:           "CurrentBranch fails — falls back to HEAD",
			explicitBranch: "polecat/jasper/co-5qy0",
			resolver: &fakeResolver{
				currentBranchErr: errors.New("detached HEAD"),
				revs:             map[string]string{"HEAD": "headsha"},
			},
			wantSHA:     "headsha",
			wantFetched: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sha, err := resolveCommitSHA(tt.resolver, tt.explicitBranch)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveCommitSHA() = (%q, nil), want error", sha)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveCommitSHA() unexpected error: %v", err)
			}
			if sha != tt.wantSHA {
				t.Errorf("sha = %q, want %q", sha, tt.wantSHA)
			}

			if len(tt.resolver.fetchedRefs) != len(tt.wantFetched) {
				t.Errorf("fetched %v, want %v", tt.resolver.fetchedRefs, tt.wantFetched)
				return
			}
			for i, got := range tt.resolver.fetchedRefs {
				if got != tt.wantFetched[i] {
					t.Errorf("fetch[%d] = %q, want %q", i, got, tt.wantFetched[i])
				}
			}
		})
	}
}

// TestResolveCommitSHA_RealGit exercises the helper end-to-end against a real
// local git repo with a remote, validating the full mechanism: a worktree on
// branch A, --branch B passed, must return B's tip — not A's tip.
//
// This is the regression test for ps-7v7 / co-toog Bug 1.
func TestResolveCommitSHA_RealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Set up a "remote" repo (bare).
	remoteDir := t.TempDir()
	runGit(t, remoteDir, "init", "--bare")

	// Set up a "local" working clone.
	localDir := t.TempDir()
	runGit(t, localDir, "init", "-b", "main")
	runGit(t, localDir, "config", "user.email", "test@test.com")
	runGit(t, localDir, "config", "user.name", "Test User")
	runGit(t, localDir, "remote", "add", "origin", remoteDir)

	// Initial commit on main, push.
	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, localDir, "add", ".")
	runGit(t, localDir, "commit", "-m", "initial")
	runGit(t, localDir, "push", "-u", "origin", "main")

	mainSHA := strings.TrimSpace(captureGit(t, localDir, "rev-parse", "HEAD"))

	// Create feature branch with a different commit, push it.
	runGit(t, localDir, "checkout", "-b", "polecat/quartz/co-toog")
	if err := os.WriteFile(filepath.Join(localDir, "feature.md"), []byte("# Feature\n"), 0644); err != nil {
		t.Fatalf("write feature: %v", err)
	}
	runGit(t, localDir, "add", ".")
	runGit(t, localDir, "commit", "-m", "feature")
	runGit(t, localDir, "push", "-u", "origin", "polecat/quartz/co-toog")

	featureSHA := strings.TrimSpace(captureGit(t, localDir, "rev-parse", "HEAD"))

	if featureSHA == mainSHA {
		t.Fatalf("test setup error: feature SHA == main SHA")
	}

	// Now switch the worktree back to main — this simulates mayor's worktree
	// on a different branch invoking `gt mq submit --branch <polecat-branch>`.
	runGit(t, localDir, "checkout", "main")

	// Pre-bug, resolveCommitSHA(g, "polecat/quartz/co-toog") would return
	// HEAD == mainSHA. The fix returns the polecat branch tip == featureSHA.
	g := git.NewGit(localDir)
	got, err := resolveCommitSHA(g, "polecat/quartz/co-toog")
	if err != nil {
		t.Fatalf("resolveCommitSHA: %v", err)
	}

	if got != featureSHA {
		t.Errorf("got SHA %q (which equals main? %v); want feature SHA %q",
			got, got == mainSHA, featureSHA)
	}

	// Sanity-check: when explicitBranch matches the current branch, returns HEAD.
	gotHead, err := resolveCommitSHA(g, "main")
	if err != nil {
		t.Fatalf("resolveCommitSHA(main): %v", err)
	}
	if gotHead != mainSHA {
		t.Errorf("explicit==current path: got %q, want %q", gotHead, mainSHA)
	}
}

func TestShouldDeleteSupersededBranch(t *testing.T) {
	tests := []struct {
		name      string
		oldBranch string
		newBranch string
		want      bool
	}{
		{
			name:      "same branch — preserve (ps-7v7 / co-toog regression)",
			oldBranch: "polecat/jasper/co-5qy0",
			newBranch: "polecat/jasper/co-5qy0",
			want:      false,
		},
		{
			name:      "different polecat branches — delete (existing GH#2669 behavior)",
			oldBranch: "polecat/jasper/co-5qy0",
			newBranch: "polecat/quartz/co-5qy0",
			want:      true,
		},
		{
			name:      "non-polecat branch — preserve (contributor fork PR)",
			oldBranch: "feature/some-contrib-fork",
			newBranch: "polecat/quartz/co-toog",
			want:      false,
		},
		{
			name:      "old polecat with @timestamp suffix vs same without — different strings, delete",
			oldBranch: "polecat/quartz/co-1jdx@moucv7bt",
			newBranch: "polecat/quartz/co-1jdx",
			want:      true,
		},
		{
			name:      "identical timestamped branches — preserve",
			oldBranch: "polecat/quartz/co-1jdx@moucv7bt",
			newBranch: "polecat/quartz/co-1jdx@moucv7bt",
			want:      false,
		},
		{
			name:      "empty old branch — preserve (defensive)",
			oldBranch: "",
			newBranch: "polecat/quartz/co-toog",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldDeleteSupersededBranch(tt.oldBranch, tt.newBranch)
			if got != tt.want {
				t.Errorf("shouldDeleteSupersededBranch(%q, %q) = %v, want %v",
					tt.oldBranch, tt.newBranch, got, tt.want)
			}
		})
	}
}

// runGit runs a git command in dir and fails the test if it fails.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, string(out))
	}
}

// captureGit runs a git command in dir and returns its stdout.
func captureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}
