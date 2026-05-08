// Package git provides a wrapper for git operations via subprocess.
package git

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/util"
)

// GitError contains raw output from a git command for agent observation.
// ZFC: Callers observe the raw output and decide what to do.
// The error interface methods provide human-readable messages, but agents
// should use Stdout/Stderr for programmatic observation.
type GitError struct {
	Command string // The git command that failed (e.g., "merge", "push")
	Args    []string
	Stdout  string // Raw stdout output
	Stderr  string // Raw stderr output
	Err     error  // Underlying error (e.g., exit code)
}

func (e *GitError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("git %s: %s", e.Command, e.Stderr)
	}
	return fmt.Sprintf("git %s: %v", e.Command, e.Err)
}

func (e *GitError) Unwrap() error {
	return e.Err
}

// moveDir moves a directory from src to dest. It first tries os.Rename for
// efficiency, but falls back to copy+delete if src and dest are on different
// filesystems (which causes EXDEV error on rename).
func moveDir(src, dest string) error {
	// Try rename first - works if same filesystem
	if err := os.Rename(src, dest); err == nil {
		return nil
	}

	// Rename failed, use platform-specific copy for cross-filesystem moves
	if err := copyDirPreserving(src, dest); err != nil {
		return fmt.Errorf("copying directory: %w", err)
	}
	if err := os.RemoveAll(src); err != nil {
		return fmt.Errorf("removing source after copy: %w", err)
	}
	return nil
}

// Git wraps git operations for a working directory.
type Git struct {
	workDir string
	gitDir  string // Optional: explicit git directory (for bare repos)
}

// NewGit creates a new Git wrapper for the given directory.
func NewGit(workDir string) *Git {
	return &Git{workDir: workDir}
}

// NewGitWithDir creates a Git wrapper with an explicit git directory.
// This is used for bare repos where gitDir points to the .git directory
// and workDir may be empty or point to a worktree.
func NewGitWithDir(gitDir, workDir string) *Git {
	return &Git{gitDir: gitDir, workDir: workDir}
}

// WorkDir returns the working directory for this Git instance.
func (g *Git) WorkDir() string {
	return g.workDir
}

// IsRepo returns true if the workDir is a git repository.
func (g *Git) IsRepo() bool {
	_, err := g.run("rev-parse", "--git-dir")
	return err == nil
}

// run executes a git command and returns stdout.
func (g *Git) run(args ...string) (string, error) {
	// If gitDir is set (bare repo), prepend --git-dir flag
	if g.gitDir != "" {
		args = append([]string{"--git-dir=" + g.gitDir}, args...)
	}

	cmd := exec.Command("git", args...)
	util.SetDetachedProcessGroup(cmd)
	if g.workDir != "" {
		cmd.Dir = g.workDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", g.wrapError(err, stdout.String(), stderr.String(), args)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// pushTimeout is the maximum time a git push is allowed to run before being
// killed. This prevents gt done from hanging indefinitely when the remote
// (e.g. GitLab) is unreachable or slow.
const pushTimeout = 60 * time.Second

// runWithTimeout executes a git command with a deadline. If the command does
// not finish within the timeout, the process is killed and an error is returned.
func (g *Git) runWithTimeout(timeout time.Duration, args ...string) (_ string, _ error) { //nolint:unparam // string return kept for consistency with Run()
	if g.gitDir != "" {
		args = append([]string{"--git-dir=" + g.gitDir}, args...)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	util.SetDetachedProcessGroup(cmd)
	if g.workDir != "" {
		cmd.Dir = g.workDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("git %s timed out after %v (remote may be unreachable)", args[0], timeout)
		}
		return "", g.wrapError(err, stdout.String(), stderr.String(), args)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// runWithEnv executes a git command with additional environment variables.
func (g *Git) runWithEnv(args []string, extraEnv []string) (_ string, _ error) { //nolint:unparam // string return kept for consistency with Run()
	return g.runWithEnvAndTimeout(args, extraEnv, 0)
}

// runWithEnvAndTimeout executes a git command with extra env vars and an
// optional timeout. Pass 0 for no timeout.
func (g *Git) runWithEnvAndTimeout(args []string, extraEnv []string, timeout time.Duration) (_ string, _ error) {
	if g.gitDir != "" {
		args = append([]string{"--git-dir=" + g.gitDir}, args...)
	}

	var cmd *exec.Cmd
	var cancel context.CancelFunc
	if timeout > 0 {
		var ctx context.Context
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
		cmd = exec.CommandContext(ctx, "git", args...)
	} else {
		cmd = exec.Command("git", args...)
	}
	if cancel != nil {
		defer cancel()
	}
	util.SetDetachedProcessGroup(cmd)

	if g.workDir != "" {
		cmd.Dir = g.workDir
	}
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if timeout > 0 {
			// Check if the context's deadline was exceeded
			if errors.Is(err, context.DeadlineExceeded) {
				return "", fmt.Errorf("git %s timed out after %v (remote may be unreachable)", args[0], timeout)
			}
		}
		return "", g.wrapError(err, stdout.String(), stderr.String(), args)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// wrapError wraps git errors with context.
// ZFC: Returns GitError with raw output for agent observation.
// Does not detect or interpret error types - agents should observe and decide.
func (g *Git) wrapError(err error, stdout, stderr string, args []string) error {
	stdout = strings.TrimSpace(stdout)
	stderr = strings.TrimSpace(stderr)

	// Determine command name (first arg, or first non-flag arg)
	command := ""
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			command = arg
			break
		}
	}
	if command == "" && len(args) > 0 {
		command = args[0]
	}

	return &GitError{
		Command: command,
		Args:    args,
		Stdout:  stdout,
		Stderr:  stderr,
		Err:     err,
	}
}

// cloneOptions configures a clone operation for cloneInternal.
type cloneOptions struct {
	bare         bool   // Pass --bare to git clone
	reference    string // Pass --reference-if-able <path> to git clone
	singleBranch bool   // Pass --single-branch to git clone (only fetch default branch)
	depth        int    // Pass --depth N to git clone (shallow clone); 0 means full history
	branch       string // Pass --branch <name> to git clone (checkout specific branch)
	filter       string // Pass --filter=<spec> to git clone (e.g. "blob:none", "tree:0")
}

// cloneInternal runs `git clone` in an isolated temp directory, moves the result
// to dest, and applies post-clone configuration (hooks or refspec).
func (g *Git) cloneInternal(url, dest string, opts cloneOptions) error {
	// Ensure destination directory's parent exists
	destParent := filepath.Dir(dest)
	if err := os.MkdirAll(destParent, 0755); err != nil {
		return fmt.Errorf("creating destination parent: %w", err)
	}
	// Run clone from a temporary directory to completely isolate from any
	// git repo at the process cwd. Then move the result to the destination.
	tmpDir, err := os.MkdirTemp("", "gt-clone-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	tmpDest := filepath.Join(tmpDir, filepath.Base(dest))

	// Build clone args
	var args []string
	// Windows symlink fix for non-bare reference clones
	if opts.reference != "" && !opts.bare && runtime.GOOS == "windows" {
		args = append(args, "-c", "core.symlinks=true")
	}
	args = append(args, "clone")
	if opts.bare {
		args = append(args, "--bare")
	}
	if opts.singleBranch {
		args = append(args, "--single-branch")
	}
	if opts.filter != "" {
		args = append(args, "--filter="+opts.filter)
	}
	if opts.depth > 0 {
		args = append(args, "--depth", fmt.Sprintf("%d", opts.depth))
	}
	if opts.branch != "" {
		args = append(args, "--branch", opts.branch)
	}
	if opts.reference != "" {
		args = append(args, "--reference-if-able", opts.reference)
	}
	args = append(args, url, tmpDest)

	cmd := exec.Command("git", args...)
	util.SetDetachedProcessGroup(cmd)
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "GIT_CEILING_DIRECTORIES="+tmpDir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return g.wrapError(err, stdout.String(), stderr.String(), args)
	}

	// Move to final destination (handles cross-filesystem moves)
	if err := moveDir(tmpDest, dest); err != nil {
		return fmt.Errorf("moving clone to destination: %w", err)
	}

	// Post-clone configuration
	if opts.bare {
		// Configure refspec so worktrees can fetch and see origin/* refs.
		// For single-branch shallow clones, only set the config without
		// fetching all branches (which would defeat the purpose of --single-branch).
		return configureRefspec(dest, opts.singleBranch)
	}
	// Configure hooks path for Gas Town clones
	if err := configureHooksPath(dest); err != nil {
		return err
	}
	// Initialize submodules if present
	return InitSubmodules(dest)
}

// Clone clones a repository to the destination.
// Uses --single-branch --depth 1 for efficiency on repos with many branches.
func (g *Git) Clone(url, dest string) error {
	return g.cloneInternal(url, dest, cloneOptions{singleBranch: true, depth: 1})
}

// CloneWithReference clones a repository using a local repo as an object reference.
// This saves disk by sharing objects without changing remotes.
// Uses --single-branch --depth 1 for efficiency on repos with many branches.
func (g *Git) CloneWithReference(url, dest, reference string) error {
	return g.cloneInternal(url, dest, cloneOptions{reference: reference, singleBranch: true, depth: 1})
}

// CloneBranch clones a specific branch with --single-branch --depth 1.
// Use this when you know which branch you need (avoids fetching all branches).
func (g *Git) CloneBranch(url, dest, branch string) error {
	return g.cloneInternal(url, dest, cloneOptions{singleBranch: true, depth: 1, branch: branch})
}

// CloneBranchWithReference clones a specific branch using a local repo as reference.
func (g *Git) CloneBranchWithReference(url, dest, branch, reference string) error {
	return g.cloneInternal(url, dest, cloneOptions{singleBranch: true, depth: 1, branch: branch, reference: reference})
}

// CloneBare clones a repository as a bare repo (no working directory).
// This is used for the shared repo architecture where all worktrees share a single git database.
func (g *Git) CloneBare(url, dest string) error {
	return g.cloneInternal(url, dest, cloneOptions{bare: true, singleBranch: true, depth: 1})
}

// CloneBareWithBranch clones a bare repo, checking out a specific branch.
// Use this when the desired default branch differs from the remote HEAD.
func (g *Git) CloneBareWithBranch(url, dest, branch string) error {
	return g.cloneInternal(url, dest, cloneOptions{bare: true, singleBranch: true, depth: 1, branch: branch})
}

// CloneBarePartial clones a bare repo with a partial clone filter (e.g. "blob:none", "tree:0").
// Does not use --depth since partial clones handle size reduction via the filter.
func (g *Git) CloneBarePartial(url, dest, filter string) error {
	return g.cloneInternal(url, dest, cloneOptions{bare: true, singleBranch: true, filter: filter})
}

// CloneBarePartialWithBranch clones a bare repo with a partial clone filter and specific branch.
func (g *Git) CloneBarePartialWithBranch(url, dest, filter, branch string) error {
	return g.cloneInternal(url, dest, cloneOptions{bare: true, singleBranch: true, filter: filter, branch: branch})
}

// CloneBarePartialWithReference clones a bare repo with a partial clone filter and local reference.
func (g *Git) CloneBarePartialWithReference(url, dest, filter, reference string) error {
	return g.cloneInternal(url, dest, cloneOptions{bare: true, singleBranch: true, filter: filter, reference: reference})
}

// CloneBarePartialWithReferenceAndBranch clones a bare repo with a partial clone filter, local reference, and specific branch.
func (g *Git) CloneBarePartialWithReferenceAndBranch(url, dest, filter, reference, branch string) error {
	return g.cloneInternal(url, dest, cloneOptions{bare: true, singleBranch: true, filter: filter, reference: reference, branch: branch})
}

// CloneBranchPartialWithReference clones a specific branch with a partial clone filter and reference.
func (g *Git) CloneBranchPartialWithReference(url, dest, branch, filter, reference string) error {
	return g.cloneInternal(url, dest, cloneOptions{singleBranch: true, filter: filter, branch: branch, reference: reference})
}

// CloneBranchPartial clones a specific branch with a partial clone filter.
func (g *Git) CloneBranchPartial(url, dest, branch, filter string) error {
	return g.cloneInternal(url, dest, cloneOptions{singleBranch: true, filter: filter, branch: branch})
}

// configureHooksPath sets core.hooksPath to use the repo's .githooks directory
// if it exists. This ensures Gas Town agents use the pre-push hook that blocks
// pushes to non-main branches (internal PRs are not allowed).
func configureHooksPath(repoPath string) error {
	hooksDir := filepath.Join(repoPath, ".githooks")
	if _, err := os.Stat(hooksDir); os.IsNotExist(err) {
		// No .githooks directory, nothing to configure
		return nil
	}

	cmd := exec.Command("git", "-C", repoPath, "config", "core.hooksPath", ".githooks")
	util.SetDetachedProcessGroup(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("configuring hooks path: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

// ConfigureHooksPath sets core.hooksPath for the repo/worktree if .githooks exists.
func (g *Git) ConfigureHooksPath() error {
	return configureHooksPath(g.workDir)
}

// configureRefspec sets remote.origin.fetch to the standard refspec for bare repos.
// Bare clones don't have this set by default, which breaks worktrees that need to
// fetch and see origin/* refs. Without this, `git fetch` only updates FETCH_HEAD
// and origin/main never appears in refs/remotes/origin/main.
// See: https://github.com/anthropics/gastown/issues/286
//
// When singleBranch is true, fetches only the default branch's ref instead of all
// branches. This prevents failures on repos with many branches where a full fetch
// would error with "some local refs could not be updated".
func configureRefspec(repoPath string, singleBranch bool) error {
	gitDir := repoPath
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err == nil {
		gitDir = filepath.Join(repoPath, ".git")
	}
	gitDir = filepath.Clean(gitDir)

	var stderr bytes.Buffer
	configCmd := exec.Command("git", "--git-dir", gitDir, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	util.SetDetachedProcessGroup(configCmd)
	configCmd.Stderr = &stderr
	if err := configCmd.Run(); err != nil {
		return fmt.Errorf("configuring refspec: %s", strings.TrimSpace(stderr.String()))
	}

	if singleBranch {
		// For shallow single-branch clones, fetch only the HEAD branch to create
		// the origin/<branch> ref that worktrees need. A full `git fetch origin`
		// would try to fetch ALL remote branches (due to the refspec we just set),
		// which fails on repos with many branches.
		//
		// Detect HEAD branch name, then fetch only that specific branch.
		var headOut bytes.Buffer
		headCmd := exec.Command("git", "--git-dir", gitDir, "symbolic-ref", "HEAD")
		util.SetDetachedProcessGroup(headCmd)
		headCmd.Stdout = &headOut
		headCmd.Stderr = &stderr
		if err := headCmd.Run(); err != nil {
			// Fallback: if HEAD is detached, try fetching all (shouldn't happen for clones)
			fetchCmd := exec.Command("git", "--git-dir", gitDir, "fetch", "--depth", "1", "origin")
			util.SetDetachedProcessGroup(fetchCmd)
			fetchCmd.Stderr = &stderr
			if fetchErr := fetchCmd.Run(); fetchErr != nil {
				return fmt.Errorf("fetching origin: %s", strings.TrimSpace(stderr.String()))
			}
			return nil
		}
		headRef := strings.TrimSpace(headOut.String())       // e.g. "refs/heads/main"
		branch := strings.TrimPrefix(headRef, "refs/heads/") // e.g. "main"
		refspec := branch + ":refs/remotes/origin/" + branch // e.g. "main:refs/remotes/origin/main"

		fetchCmd := exec.Command("git", "--git-dir", gitDir, "fetch", "--depth", "1", "origin", refspec)
		util.SetDetachedProcessGroup(fetchCmd)
		fetchCmd.Stderr = &stderr
		if err := fetchCmd.Run(); err != nil {
			return fmt.Errorf("fetching origin %s: %s", branch, strings.TrimSpace(stderr.String()))
		}
		return nil
	}

	fetchCmd := exec.Command("git", "--git-dir", gitDir, "fetch", "origin")
	util.SetDetachedProcessGroup(fetchCmd)
	fetchCmd.Stderr = &stderr
	if err := fetchCmd.Run(); err != nil {
		return fmt.Errorf("fetching origin: %s", strings.TrimSpace(stderr.String()))
	}

	return nil
}

// CloneBareWithReference clones a bare repository using a local repo as an object reference.
// Uses --single-branch --depth 1 for efficiency on repos with many branches.
func (g *Git) CloneBareWithReference(url, dest, reference string) error {
	return g.cloneInternal(url, dest, cloneOptions{bare: true, reference: reference, singleBranch: true, depth: 1})
}

// CloneBareWithReferenceAndBranch clones a bare repo using a local reference, checking out a specific branch.
func (g *Git) CloneBareWithReferenceAndBranch(url, dest, reference, branch string) error {
	return g.cloneInternal(url, dest, cloneOptions{bare: true, reference: reference, singleBranch: true, depth: 1, branch: branch})
}

// Checkout checks out the given ref.
func (g *Git) Checkout(ref string) error {
	_, err := g.run("checkout", ref)
	return err
}

// CheckoutNewBranch creates a new branch from startPoint and checks it out.
// Equivalent to: git checkout -b <branch> <startPoint>
func (g *Git) CheckoutNewBranch(branch, startPoint string) error {
	_, err := g.run("checkout", "-b", branch, startPoint)
	return err
}

// CheckoutResetBranch creates or resets a branch to startPoint and checks it out.
// Equivalent to: git checkout -B <branch> <startPoint>. Unlike CheckoutNewBranch
// this does not fail when the branch already exists locally — useful when reusing
// a worktree that previously had the same branch checked out.
func (g *Git) CheckoutResetBranch(branch, startPoint string) error {
	_, err := g.run("checkout", "-B", branch, startPoint)
	return err
}

// Fetch fetches from the remote.
func (g *Git) Fetch(remote string) error {
	_, err := g.run("fetch", remote)
	return err
}

// FetchPrune fetches from the remote and prunes stale remote-tracking refs.
// This removes remote-tracking branches for branches that no longer exist on the remote.
func (g *Git) FetchPrune(remote string) error {
	_, err := g.run("fetch", "--prune", remote)
	return err
}

// FetchBranch fetches a specific branch from the remote.
func (g *Git) FetchBranch(remote, branch string) error {
	_, err := g.run("fetch", remote, branch)
	return err
}

// FetchBranchShallow fetches a single branch with --depth 1 and creates the
// remote tracking ref (e.g. origin/<branch>). Use this on shallow single-branch
// clones to add a branch that wasn't included in the initial clone.
func (g *Git) FetchBranchShallow(remote, branch string) error {
	refspec := branch + ":refs/remotes/" + remote + "/" + branch
	_, err := g.run("fetch", "--depth", "1", remote, refspec)
	return err
}

// Pull pulls from the remote branch.
func (g *Git) Pull(remote, branch string) error {
	_, err := g.run("pull", remote, branch)
	return err
}

// ConfigurePushURL sets the push URL for a remote while keeping the fetch URL.
// This is useful for read-only upstream repos where you want to push to a fork.
// Example: ConfigurePushURL("origin", "https://github.com/user/fork.git")
func (g *Git) ConfigurePushURL(remote, pushURL string) error {
	_, err := g.run("remote", "set-url", remote, "--push", pushURL)
	return err
}

// ClearPushURL removes a custom push URL for a remote, reverting to the fetch URL.
// If no custom push URL is set, this is a no-op.
// Uses --unset-all to handle multi-valued pushurl entries; with --unset-all,
// exit code 5 unambiguously means "key not found" (safe to ignore).
func (g *Git) ClearPushURL(remote string) error {
	_, err := g.run("config", "--unset-all", fmt.Sprintf("remote.%s.pushurl", remote))
	if err != nil {
		// git config --unset-all returns exit code 5 if the key doesn't exist — that's fine.
		var ge *GitError
		if errors.As(err, &ge) {
			var exitErr *exec.ExitError
			if errors.As(ge.Err, &exitErr) && exitErr.ExitCode() == 5 {
				return nil
			}
		}
		return err
	}
	return nil
}

// GetPushURL returns the effective push URL for a remote.
// Note: git returns the fetch URL when no custom push URL is configured, so this
// never returns empty for a valid remote. Compare with RemoteURL to detect custom push URLs.
func (g *Git) GetPushURL(remote string) (string, error) {
	out, err := g.run("remote", "get-url", "--push", remote)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Push pushes to the remote branch with a timeout to prevent indefinite hangs
// when the remote is unreachable.
func (g *Git) Push(remote, branch string, force bool) error {
	args := []string{"push", remote, branch}
	if force {
		args = append(args, "--force")
	}
	_, err := g.runWithTimeout(pushTimeout, args...)
	return err
}

// PushWithEnv pushes with additional environment variables.
// Used by gt mq integration land to set GT_INTEGRATION_LAND=1, which the
// pre-push hook checks to allow integration branch content landing on main.
func (g *Git) PushWithEnv(remote, branch string, force bool, env []string) error {
	args := []string{"push", remote, branch}
	if force {
		args = append(args, "--force")
	}
	_, err := g.runWithEnvAndTimeout(args, env, pushTimeout)
	return err
}

// Add stages files for commit.
func (g *Git) Add(paths ...string) error {
	args := append([]string{"add"}, paths...)
	_, err := g.run(args...)
	return err
}

// Commit creates a commit with the given message.
func (g *Git) Commit(message string) error {
	_, err := g.run("commit", "-m", message)
	return err
}

// CommitAll stages all changes and commits.
func (g *Git) CommitAll(message string) error {
	_, err := g.run("commit", "-am", message)
	return err
}

// ResetFiles unstages files without modifying the working tree.
// Equivalent to: git reset HEAD -- <paths>
func (g *Git) ResetFiles(paths ...string) error {
	args := append([]string{"reset", "HEAD", "--"}, paths...)
	_, err := g.run(args...)
	return err
}

// StagedDeletions returns the list of tracked files staged for deletion.
// Used by auto-save to unstage deletions — safety nets should preserve work, not destroy it.
func (g *Git) StagedDeletions() ([]string, error) {
	out, err := g.run("diff", "--cached", "--name-only", "--diff-filter=D")
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\n"), nil
}

// ShowFile returns the contents of a file at a given ref (e.g., "origin/main:CLAUDE.md").
// Returns empty string and no error if the file does not exist at that ref.
func (g *Git) ShowFile(ref, path string) (string, error) {
	out, err := g.run("show", ref+":"+path)
	if err != nil {
		// "does not exist" or "exists on disk, but not in" are expected for missing files
		return "", err
	}
	return out, nil
}

// CheckoutFileFromRef restores a file from a given ref (e.g., "origin/main").
// Equivalent to: git checkout <ref> -- <path>
func (g *Git) CheckoutFileFromRef(ref string, paths ...string) error {
	args := append([]string{"checkout", ref, "--"}, paths...)
	_, err := g.run(args...)
	return err
}

// RmCached removes files from the index without deleting from the working tree.
// Equivalent to: git rm --cached --force <paths>
func (g *Git) RmCached(paths ...string) error {
	args := append([]string{"rm", "--cached", "--force", "--ignore-unmatch"}, paths...)
	_, err := g.run(args...)
	return err
}

// DiffNameOnly returns filenames changed between two refs.
// Equivalent to: git diff --name-only <base>...<head>
func (g *Git) DiffNameOnly(base, head string) ([]string, error) {
	out, err := g.run("diff", "--name-only", base+"..."+head)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(strings.TrimSpace(out), "\n"), nil
}

// GitStatus represents the status of the working directory.
type GitStatus struct {
	Clean     bool
	Modified  []string
	Added     []string
	Deleted   []string
	Untracked []string
}

// Status returns the current git status.
func (g *Git) Status() (*GitStatus, error) {
	out, err := g.run("status", "--porcelain")
	if err != nil {
		return nil, err
	}

	status := &GitStatus{Clean: true}
	if out == "" {
		return status, nil
	}

	// Get skip-worktree files once (sparse checkout). These appear as 'D' in
	// --porcelain output but are not real deletions — they are hidden by the
	// sparse-checkout cone. Filtering them prevents gt done from blocking on
	// 897+ phantom deletions in polecat sparse worktrees.
	skipWorktree := g.skipWorktreeFiles()

	status.Clean = false
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 3 {
			continue
		}
		code := line[:2]
		file := line[3:]

		switch {
		case strings.Contains(code, "M"):
			status.Modified = append(status.Modified, file)
		case strings.Contains(code, "A"):
			status.Added = append(status.Added, file)
		case strings.Contains(code, "D"):
			// Skip files hidden by sparse-checkout (skip-worktree bit set).
			if !skipWorktree[file] {
				status.Deleted = append(status.Deleted, file)
			}
		case strings.Contains(code, "?"):
			status.Untracked = append(status.Untracked, file)
		}
	}

	// Recheck clean: if all entries were skip-worktree deletions, we're actually clean.
	if len(status.Modified) == 0 && len(status.Added) == 0 &&
		len(status.Deleted) == 0 && len(status.Untracked) == 0 {
		status.Clean = true
	}

	return status, nil
}

// skipWorktreeFiles returns a set of file paths that have the skip-worktree
// bit set (sparse-checkout hidden files). Uses `git ls-files -v` and filters
// for lines starting with 'S' (uppercase = skip-worktree). Non-fatal: returns
// empty map on error so callers degrade gracefully.
func (g *Git) skipWorktreeFiles() map[string]bool {
	out, err := g.run("ls-files", "-v")
	if err != nil || out == "" {
		return nil
	}
	result := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		// Format: "<flag> <path>" where flag is uppercase letter for skip-worktree
		if len(line) < 3 || line[0] != 'S' {
			continue
		}
		result[line[2:]] = true
	}
	return result
}

// CurrentBranch returns the current branch name.
func (g *Git) CurrentBranch() (string, error) {
	return g.run("rev-parse", "--abbrev-ref", "HEAD")
}

// DefaultBranch returns the default branch name (what HEAD points to).
// This works for both regular and bare repositories.
// Returns "main" as fallback if detection fails.
func (g *Git) DefaultBranch() string {
	// Try symbolic-ref first (works for bare repos)
	branch, err := g.run("symbolic-ref", "--short", "HEAD")
	if err == nil && branch != "" {
		return branch
	}
	// Fallback to main
	return "main"
}

// RemoteDefaultBranch returns the default branch from the remote (origin).
// This is useful in worktrees where HEAD may not reflect the repo's actual default.
// Checks origin/HEAD first, then falls back to checking if master/main exists.
// Returns "main" as final fallback.
func (g *Git) RemoteDefaultBranch() string {
	// Try to get from origin/HEAD symbolic ref
	out, err := g.run("symbolic-ref", "refs/remotes/origin/HEAD")
	if err == nil && out != "" {
		// Returns refs/remotes/origin/main -> extract branch name
		parts := strings.Split(out, "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}

	// Fallback: check if origin/master exists
	_, err = g.run("rev-parse", "--verify", "origin/master")
	if err == nil {
		return "master"
	}

	// Fallback: check if origin/main exists
	_, err = g.run("rev-parse", "--verify", "origin/main")
	if err == nil {
		return "main"
	}

	return "main" // final fallback
}

// HasUncommittedChanges returns true if there are uncommitted changes.
func (g *Git) HasUncommittedChanges() (bool, error) {
	status, err := g.Status()
	if err != nil {
		return false, err
	}
	return !status.Clean, nil
}

// RemoteURL returns the URL for the given remote.
func (g *Git) RemoteURL(remote string) (string, error) {
	return g.run("remote", "get-url", remote)
}

// AddRemote adds a new remote with the given name and URL.
func (g *Git) AddRemote(name, url string) (string, error) {
	return g.run("remote", "add", name, url)
}

// SetRemoteURL updates the URL for an existing remote.
func (g *Git) SetRemoteURL(name, url string) (string, error) {
	return g.run("remote", "set-url", name, url)
}

// AddUpstreamRemote adds or updates the 'upstream' git remote.
// This is idempotent - if the remote already exists with the same URL, it's a no-op.
// If the remote exists with a different URL, it's updated.
func (g *Git) AddUpstreamRemote(upstreamURL string) error {
	has, err := g.HasUpstreamRemote()
	if err != nil {
		return err
	}
	if has {
		current, err := g.GetUpstreamURL()
		if err != nil {
			return err
		}
		if current == upstreamURL {
			return nil
		}
		_, err = g.run("remote", "set-url", "upstream", upstreamURL)
		return err
	}
	_, err = g.run("remote", "add", "upstream", upstreamURL)
	return err
}

// GetUpstreamURL returns the URL of the upstream remote.
// Returns empty string if upstream remote doesn't exist.
func (g *Git) GetUpstreamURL() (string, error) {
	out, err := g.run("remote", "get-url", "upstream")
	if err != nil {
		if strings.Contains(err.Error(), "No such remote") {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// HasUpstreamRemote returns true if an upstream remote is configured.
func (g *Git) HasUpstreamRemote() (bool, error) {
	_, err := g.run("remote", "get-url", "upstream")
	if err != nil {
		if strings.Contains(err.Error(), "No such remote") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// FetchUpstream fetches from the upstream remote.
func (g *Git) FetchUpstream() error {
	_, err := g.run("fetch", "upstream")
	return err
}

// Remotes returns the list of configured remote names.
func (g *Git) Remotes() ([]string, error) {
	out, err := g.run("remote")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// ConfigGet returns the value of a git config key.
// Returns empty string if the key is not set.
func (g *Git) ConfigGet(key string) (string, error) {
	out, err := g.run("config", "--get", key)
	if err != nil {
		// git config --get returns exit code 1 if key not found
		return "", nil
	}
	return out, nil
}

// Merge merges the given branch into the current branch.
func (g *Git) Merge(branch string) error {
	_, err := g.run("merge", branch)
	return err
}

// MergeNoFF merges the given branch with --no-ff flag and a custom message.
func (g *Git) MergeNoFF(branch, message string) error {
	_, err := g.run("merge", "--no-ff", "-m", message, branch)
	return err
}

// MergeFFOnly performs a fast-forward-only merge of the given ref into the current branch.
// This ensures what you tested is exactly what lands — no merge commits are created.
// Returns an error if the merge cannot be performed as a fast-forward.
func (g *Git) MergeFFOnly(ref string) error {
	_, err := g.run("merge", "--ff-only", ref)
	return err
}

// MergeSquash performs a squash merge of the given branch and commits with the provided message.
// This stages all changes from the branch without creating a merge commit, then commits them
// as a single commit with the given message. This eliminates redundant merge commits while
// preserving the original commit message from the source branch.
func (g *Git) MergeSquash(branch, message string) error {
	// Stage all changes from the branch without committing
	if _, err := g.run("merge", "--squash", branch); err != nil {
		return err
	}
	// Commit the staged changes with the provided message
	_, err := g.run("commit", "-m", message)
	return err
}

// GetBranchCommitMessage returns the commit message of the HEAD commit on the given branch.
// This is useful for preserving the original conventional commit message (feat:/fix:) when
// performing squash merges.
func (g *Git) GetBranchCommitMessage(branch string) (string, error) {
	return g.run("log", "-1", "--format=%B", branch)
}

// RecentCommits returns the last n commits as one-line summaries (hash + subject).
// Returns empty string if there are no commits or the repo is empty.
func (g *Git) RecentCommits(n int) (string, error) {
	return g.run("log", "--oneline", fmt.Sprintf("-%d", n))
}

// DeleteRemoteBranch deletes a branch on the remote.
func (g *Git) DeleteRemoteBranch(remote, branch string) error {
	_, err := g.runWithTimeout(pushTimeout, "push", remote, "--delete", branch)
	return err
}

// HasOpenPR checks whether the given branch has an open pull request on GitHub.
// Uses the gh CLI to query for open PRs with the branch as head ref.
// Returns false on any error (fail-open: branch deletion proceeds if gh is unavailable).
func (g *Git) HasOpenPR(branch string) bool {
	cmd := exec.Command("gh", "pr", "list", "--head", branch, "--state", "open", "--json", "number", "--limit", "1")
	cmd.Dir = g.workDir
	out, err := cmd.Output()
	if err != nil {
		return false // fail-open: can't determine PR state, allow deletion
	}
	out = bytes.TrimSpace(out)
	// Empty array "[]" means no open PRs
	return len(out) > 2
}

// FindPRNumber returns the GitHub PR number for the given branch, or 0 if none exists.
// Uses the gh CLI to query for open PRs with the branch as head ref.
func (g *Git) FindPRNumber(branch string) (int, error) {
	cmd := exec.Command("gh", "pr", "list", "--head", branch, "--state", "open", "--json", "number", "--limit", "1")
	cmd.Dir = g.workDir
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("gh pr list failed: %w", err)
	}
	out = bytes.TrimSpace(out)
	if len(out) <= 2 {
		return 0, nil // No open PR
	}
	var prs []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(out, &prs); err != nil {
		return 0, fmt.Errorf("failed to parse gh pr list output: %w", err)
	}
	if len(prs) == 0 {
		return 0, nil
	}
	return prs[0].Number, nil
}

// IsPRApproved checks whether a GitHub PR has at least one approving review.
// Returns true if approved, false if not (or on error).
func (g *Git) IsPRApproved(prNumber int) (bool, error) {
	// Use gh pr view which includes review decision
	cmd := exec.Command("gh", "pr", "view", fmt.Sprintf("%d", prNumber), "--json", "reviewDecision")
	cmd.Dir = g.workDir
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("gh pr view failed: %w", err)
	}
	var result struct {
		ReviewDecision string `json:"reviewDecision"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &result); err != nil {
		return false, fmt.Errorf("failed to parse gh pr view output: %w", err)
	}
	// APPROVED is the GitHub review decision when at least one approving review exists
	return result.ReviewDecision == "APPROVED", nil
}

// GhPrMerge merges a GitHub PR using the gh CLI, respecting branch protection rules.
// The method parameter should be "merge", "squash", or "rebase".
// Returns the merge commit SHA on success.
func (g *Git) GhPrMerge(prNumber int, method string) (string, error) {
	args := []string{"pr", "merge", fmt.Sprintf("%d", prNumber), "--" + method, "--delete-branch"}
	cmd := exec.Command("gh", args...)
	cmd.Dir = g.workDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh pr merge failed: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// After merge, pull the target branch to get the merge commit locally
	if _, pullErr := g.run("pull", "origin"); pullErr != nil {
		// Non-fatal: the merge succeeded on GitHub, we just can't get the SHA locally
		return "", nil
	}
	// Get the latest commit on HEAD (should be the merge commit)
	sha, revErr := g.Rev("HEAD")
	if revErr != nil {
		return "", nil // Merge succeeded, just can't determine SHA
	}
	return sha, nil
}

// FindBitbucketPRNumber returns the Bitbucket PR ID for the given branch, or 0 if none exists.
// It queries the Bitbucket REST API for open PRs with the branch as source.
func (g *Git) FindBitbucketPRNumber(workspace, repoSlug, branch string) (int, error) {
	// Use curl since there is no official Bitbucket CLI equivalent to gh.
	// The BITBUCKET_TOKEN env var provides authentication.
	token := os.Getenv("BITBUCKET_TOKEN")
	if token == "" {
		return 0, fmt.Errorf("BITBUCKET_TOKEN is required for Bitbucket PR operations")
	}
	url := fmt.Sprintf("https://api.bitbucket.org/2.0/repositories/%s/%s/pullrequests?q=source.branch.name%%3D%%22%s%%22+AND+state%%3D%%22OPEN%%22&pagelen=1",
		workspace, repoSlug, branch)
	cmd := exec.Command("curl", "-s", "-H", "Authorization: Bearer "+token, url)
	cmd.Dir = g.workDir
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("bitbucket API request failed: %w", err)
	}
	var resp struct {
		Values []struct {
			ID int `json:"id"`
		} `json:"values"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &resp); err != nil {
		return 0, fmt.Errorf("failed to parse Bitbucket response: %w", err)
	}
	if len(resp.Values) == 0 {
		return 0, nil
	}
	return resp.Values[0].ID, nil
}

// IsBitbucketPRApproved checks whether a Bitbucket PR has at least one approving reviewer.
func (g *Git) IsBitbucketPRApproved(workspace, repoSlug string, prID int) (bool, error) {
	token := os.Getenv("BITBUCKET_TOKEN")
	if token == "" {
		return false, fmt.Errorf("BITBUCKET_TOKEN is required for Bitbucket PR operations")
	}
	url := fmt.Sprintf("https://api.bitbucket.org/2.0/repositories/%s/%s/pullrequests/%d",
		workspace, repoSlug, prID)
	cmd := exec.Command("curl", "-s", "-H", "Authorization: Bearer "+token, url)
	cmd.Dir = g.workDir
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("bitbucket API request failed: %w", err)
	}
	var pr struct {
		Participants []struct {
			Role     string `json:"role"`
			Approved bool   `json:"approved"`
		} `json:"participants"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &pr); err != nil {
		return false, fmt.Errorf("failed to parse Bitbucket response: %w", err)
	}
	for _, p := range pr.Participants {
		if p.Role == "REVIEWER" && p.Approved {
			return true, nil
		}
	}
	return false, nil
}

// BitbucketPRMerge merges a Bitbucket PR via the REST API.
// The strategy parameter should be "merge_commit", "squash", or "fast_forward".
// Returns the merge commit SHA on success (if available).
func (g *Git) BitbucketPRMerge(workspace, repoSlug string, prID int, strategy string) (string, error) {
	token := os.Getenv("BITBUCKET_TOKEN")
	if token == "" {
		return "", fmt.Errorf("BITBUCKET_TOKEN is required for Bitbucket PR operations")
	}
	url := fmt.Sprintf("https://api.bitbucket.org/2.0/repositories/%s/%s/pullrequests/%d/merge",
		workspace, repoSlug, prID)
	body := fmt.Sprintf(`{"merge_strategy":"%s","close_source_branch":true}`, strategy)
	cmd := exec.Command("curl", "-s", "-X", "POST",
		"-H", "Authorization: Bearer "+token,
		"-H", "Content-Type: application/json",
		"-d", body, url)
	cmd.Dir = g.workDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("bitbucket merge failed: %s: %w", strings.TrimSpace(string(out)), err)
	}

	var resp struct {
		MergeCommit struct {
			Hash string `json:"hash"`
		} `json:"merge_commit"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &resp); err != nil {
		// Merge may have succeeded but response parsing failed — pull to get SHA.
		if _, pullErr := g.run("pull", "origin"); pullErr == nil {
			if sha, revErr := g.Rev("HEAD"); revErr == nil {
				return sha, nil
			}
		}
		return "", nil
	}

	// Sync local state after remote merge.
	if _, pullErr := g.run("pull", "origin"); pullErr != nil {
		return resp.MergeCommit.Hash, nil
	}
	if resp.MergeCommit.Hash != "" {
		return resp.MergeCommit.Hash, nil
	}
	sha, _ := g.Rev("HEAD")
	return sha, nil
}

// ListRemoteRefs returns remote ref names matching a prefix using ls-remote.
// The prefix filters refs (e.g., "refs/heads/polecat/" for all polecat branches).
// Returns full ref names like "refs/heads/polecat/furiosa-abc123".
func (g *Git) ListRemoteRefs(remote, prefix string) ([]string, error) {
	out, err := g.run("ls-remote", "--refs", remote, prefix+"*")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var refs []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// ls-remote output format: <sha>\t<refname>
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			refs = append(refs, parts[1])
		}
	}
	return refs, nil
}

// ListPushRemoteRefs lists remote refs from the push URL when it differs from
// the fetch URL. With a fork-based workflow (pushurl configured), branches are
// pushed to the fork but ls-remote reads from the fetch URL (upstream). This
// method queries the push URL so cleanup can find branches that were pushed.
// Falls back to ListRemoteRefs if no custom push URL is configured.
func (g *Git) ListPushRemoteRefs(remote, prefix string) ([]string, error) {
	fetchURL, fetchErr := g.RemoteURL(remote)
	pushURL, pushErr := g.GetPushURL(remote)
	if fetchErr != nil || pushErr != nil || pushURL == fetchURL {
		return g.ListRemoteRefs(remote, prefix)
	}
	// Query the push URL directly
	return g.ListRemoteRefs(pushURL, prefix)
}

// Rebase rebases the current branch onto the given ref.
func (g *Git) Rebase(onto string) error {
	_, err := g.run("rebase", onto)
	return err
}

// AbortMerge aborts a merge in progress.
func (g *Git) AbortMerge() error {
	_, err := g.run("merge", "--abort")
	return err
}

// CheckConflicts performs a test merge to check if source can be merged into target
// without conflicts. Returns a list of conflicting files, or empty slice if clean.
// The merge is always aborted after checking - no actual changes are made.
//
// The caller must ensure the working directory is clean before calling this.
// After return, the working directory is restored to the target branch.
func (g *Git) CheckConflicts(source, target string) ([]string, error) {
	// Checkout the target branch
	if err := g.Checkout(target); err != nil {
		return nil, fmt.Errorf("checkout target %s: %w", target, err)
	}

	// Attempt test merge with --no-commit --no-ff
	// We need to capture both stdout and stderr to detect conflicts
	_, mergeErr := g.runMergeCheck("merge", "--no-commit", "--no-ff", source)

	if mergeErr != nil {
		// ZFC: Use git's porcelain output to detect conflicts instead of parsing stderr.
		// GetConflictingFiles() uses `git diff --diff-filter=U` which is the proper way.
		conflicts, err := g.GetConflictingFiles()
		if err == nil && len(conflicts) > 0 {
			// Abort the test merge (best-effort cleanup)
			_ = g.AbortMerge()
			return conflicts, nil
		}

		// No unmerged files detected - this is some other merge error
		_ = g.AbortMerge()
		return nil, mergeErr
	}

	// Merge succeeded (no conflicts) - abort the test merge
	// Use reset since --abort won't work on successful merge (best-effort cleanup)
	_, _ = g.run("reset", "--hard", "HEAD")
	return nil, nil
}

// runMergeCheck runs a git merge command and returns error info from both stdout and stderr.
// ZFC: Returns GitError with raw output for agent observation.
func (g *Git) runMergeCheck(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	util.SetDetachedProcessGroup(cmd)
	cmd.Dir = g.workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		// ZFC: Return raw output for observation, don't interpret CONFLICT
		return "", g.wrapError(err, stdout.String(), stderr.String(), args)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// GetConflictingFiles returns the list of files with merge conflicts.
// ZFC: Uses git's porcelain output (diff --diff-filter=U) instead of parsing stderr.
// This is the proper way to detect conflicts without violating ZFC.
func (g *Git) GetConflictingFiles() ([]string, error) {
	// git diff --name-only --diff-filter=U shows unmerged files
	out, err := g.run("diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}

	if out == "" {
		return nil, nil
	}

	files := strings.Split(out, "\n")
	// Filter out empty strings
	var result []string
	for _, f := range files {
		if f != "" {
			result = append(result, f)
		}
	}
	return result, nil
}

// AbortRebase aborts a rebase in progress.
func (g *Git) AbortRebase() error {
	_, err := g.run("rebase", "--abort")
	return err
}

// CreateBranch creates a new branch.
func (g *Git) CreateBranch(name string) error {
	_, err := g.run("branch", name)
	return err
}

// CreateBranchFrom creates a new branch from a specific ref.
func (g *Git) CreateBranchFrom(name, ref string) error {
	_, err := g.run("branch", name, ref)
	return err
}

// BranchExists checks if a branch exists locally.
func (g *Git) BranchExists(name string) (bool, error) {
	_, err := g.run("show-ref", "--verify", "--quiet", "refs/heads/"+name)
	if err != nil {
		// Exit code 1 means branch doesn't exist
		if strings.Contains(err.Error(), "exit status 1") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// RefExists checks if a ref exists (works for any ref including origin/<branch>).
// Uses show-ref for fully-qualified refs, falls back to rev-parse for short refs.
func (g *Git) RefExists(ref string) (bool, error) {
	// Fully-qualified refs (refs/...) use show-ref which has a stable exit code contract:
	// exit 0 = exists, exit 1 = missing, exit >1 = error.
	if strings.HasPrefix(ref, "refs/") {
		_, err := g.run("show-ref", "--verify", "--quiet", ref)
		if err != nil {
			if strings.Contains(err.Error(), "exit status 1") {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}

	// Short refs (e.g., origin/main) need rev-parse --verify.
	_, err := g.run("rev-parse", "--verify", ref)
	if err != nil {
		// Only treat "ref missing" as false — propagate other failures
		// (e.g. corrupted repo, permissions, disk I/O).
		var gitErr *GitError
		if errors.As(err, &gitErr) &&
			strings.Contains(gitErr.Stderr, "Needed a single revision") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// IsEmpty returns true if the repository has no refs (an empty/unborn repo).
// This is the case for newly-created repos with no commits.
func (g *Git) IsEmpty() (bool, error) {
	out, err := g.run("show-ref")
	if err != nil {
		// git show-ref exits 1 when there are no refs — that means empty
		if strings.Contains(err.Error(), "exit status 1") {
			return true, nil
		}
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

// RemoteBranchExists checks if a branch exists on the remote.
// NOTE: For named remotes with a separate pushurl, this checks the fetch URL.
// Use PushRemoteBranchExists to verify branches that were pushed.
func (g *Git) RemoteBranchExists(remote, branch string) (bool, error) {
	out, err := g.run("ls-remote", "--heads", remote, branch)
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// RemoteBranchTip returns the SHA at refs/heads/<branch> on the remote.
// An empty SHA with nil error means the branch is missing.
func (g *Git) RemoteBranchTip(remote, branch string) (string, error) {
	out, err := g.run("ls-remote", "--heads", remote, branch)
	if err != nil {
		return "", err
	}
	return parseLSRemoteTip(out, branch), nil
}

// PushRemoteBranchExists checks if a branch exists on the push target of a remote.
// With a fork-based or local-bare-repo workflow (pushurl configured), pushes go to
// the push URL but ls-remote resolves the fetch URL. This method queries the push
// URL directly so verification matches where the branch was actually pushed.
// Falls back to RemoteBranchExists when no custom push URL is configured.
func (g *Git) PushRemoteBranchExists(remote, branch string) (bool, error) {
	fetchURL, fetchErr := g.RemoteURL(remote)
	pushURL, pushErr := g.GetPushURL(remote)
	if fetchErr != nil || pushErr != nil || pushURL == fetchURL {
		return g.RemoteBranchExists(remote, branch)
	}
	out, err := g.run("ls-remote", "--heads", pushURL, branch)
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// PushRemoteBranchTip returns the SHA at refs/heads/<branch> on the push target.
// This mirrors PushRemoteBranchExists: when remote.<name>.pushurl differs from
// the fetch URL, verification must query the push URL because that is where the
// preceding git push wrote.
func (g *Git) PushRemoteBranchTip(remote, branch string) (string, error) {
	fetchURL, fetchErr := g.RemoteURL(remote)
	pushURL, pushErr := g.GetPushURL(remote)
	if fetchErr != nil || pushErr != nil || pushURL == fetchURL {
		return g.RemoteBranchTip(remote, branch)
	}
	return g.RemoteBranchTip(pushURL, branch)
}

// VerifyPushedCommit verifies that the push target branch tip is exactly commit.
// gt/refinery callers invoke this immediately after a push, before closing beads
// or creating downstream merge artifacts. Exact-tip verification catches the
// dangerous case where git push exits 0 but leaves the remote branch stale.
func (g *Git) VerifyPushedCommit(remote, branch, commit string) error {
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return fmt.Errorf("verified_push_failed: empty commit for %s/%s", remote, branch)
	}
	tip, err := g.PushRemoteBranchTip(remote, branch)
	if err != nil {
		return fmt.Errorf("verified_push_failed: unable to read %s/%s: %w", remote, branch, err)
	}
	if tip == "" {
		return fmt.Errorf("verified_push_failed: branch %s/%s missing after push (expected %s)", remote, branch, shortSHA(commit))
	}
	if tip != commit {
		return fmt.Errorf("verified_push_failed: commit %s not on %s/%s (remote tip %s)", shortSHA(commit), remote, branch, shortSHA(tip))
	}
	return nil
}

func parseLSRemoteTip(out, branch string) string {
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		if parts[1] == "refs/heads/"+branch {
			return parts[0]
		}
	}
	return ""
}

func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// RemoteTrackingBranchExists checks if a remote-tracking branch ref exists locally
// (e.g. refs/remotes/origin/main), without hitting the network.
func (g *Git) RemoteTrackingBranchExists(remote, branch string) (bool, error) {
	ref := fmt.Sprintf("refs/remotes/%s/%s", remote, branch)
	_, err := g.run("show-ref", "--verify", "--quiet", ref)
	if err != nil {
		if strings.Contains(err.Error(), "exit status 1") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// DeleteBranch deletes a local branch.
func (g *Git) DeleteBranch(name string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := g.run("branch", flag, name)
	return err
}

// ListBranches returns all local branches matching a pattern.
// Pattern uses git's pattern matching (e.g., "polecat/*" matches all polecat branches).
// Returns branch names without the refs/heads/ prefix.
func (g *Git) ListBranches(pattern string) ([]string, error) {
	args := []string{"branch", "--list", "--format=%(refname:short)"}
	if pattern != "" {
		args = append(args, pattern)
	}
	out, err := g.run(args...)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// ResetBranch force-updates a branch to point to a ref.
// This is useful for resetting stale polecat branches to main.
// NOTE: This uses `git branch -f` which fails on the currently checked-out branch.
// Use ResetHard instead when the target branch is checked out.
func (g *Git) ResetBranch(name, ref string) error {
	_, err := g.run("branch", "-f", name, ref)
	return err
}

// ResetHard resets the current working tree and index to the given ref.
// Unlike ResetBranch, this works on the currently checked-out branch.
func (g *Git) ResetHard(ref string) error {
	_, err := g.run("reset", "--hard", ref)
	return err
}

// CleanForce removes untracked files and directories from the working tree.
// Excludes .runtime/ to preserve agent lock files and session state.
func (g *Git) CleanForce() error {
	_, err := g.run("clean", "-fd", "--exclude=.runtime")
	return err
}

// Rev returns the commit hash for the given ref.
func (g *Git) Rev(ref string) (string, error) {
	return g.run("rev-parse", ref)
}

// IsAncestor checks if ancestor is an ancestor of descendant.
func (g *Git) IsAncestor(ancestor, descendant string) (bool, error) {
	_, err := g.run("merge-base", "--is-ancestor", ancestor, descendant)
	if err != nil {
		// Exit code 1 means not an ancestor, not an error
		if strings.Contains(err.Error(), "exit status 1") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Cherry runs `git cherry <upstream> <head>` to list commits on head that are
// not yet on upstream, comparing by patch-id. Each output line is prefixed with
// "+ " (patch not on upstream) or "- " (patch already applied upstream, e.g.
// via squash merge). Used to detect already-merged work that plain ancestor
// checks miss. See aa-apw.
func (g *Git) Cherry(upstream, head string) (string, error) {
	return g.run("cherry", upstream, head)
}

// WorktreeAdd creates a new worktree at the given path with a new branch.
// The new branch is created from the current HEAD.
// Skips LFS smudge filter during checkout (see WorktreeAddFromRef).
func (g *Git) WorktreeAdd(path, branch string) error {
	if _, err := g.runWithEnv(
		[]string{"worktree", "add", "-b", branch, path},
		[]string{"GIT_LFS_SKIP_SMUDGE=1"},
	); err != nil {
		return err
	}
	return InitSubmodules(path, g.submoduleReferencePath())
}

// WorktreeAddFromRef creates a new worktree at the given path with a new branch
// starting from the specified ref (e.g., "origin/main").
// Skips LFS smudge filter during checkout to avoid downloading large LFS objects
// over NFS (~72s for 473MB). LFS files appear as pointer files initially;
// callers can run "git lfs pull" later when LFS content is actually needed.
func (g *Git) WorktreeAddFromRef(path, branch, startPoint string) error {
	if _, err := g.runWithEnv(
		[]string{"worktree", "add", "-b", branch, path, startPoint},
		[]string{"GIT_LFS_SKIP_SMUDGE=1"},
	); err != nil {
		return err
	}
	return InitSubmodules(path, g.submoduleReferencePath())
}

// WorktreeAddDetached creates a new worktree at the given path with a detached HEAD.
// Skips LFS smudge filter during checkout (see WorktreeAddFromRef).
func (g *Git) WorktreeAddDetached(path, ref string) error {
	if _, err := g.runWithEnv(
		[]string{"worktree", "add", "--detach", path, ref},
		[]string{"GIT_LFS_SKIP_SMUDGE=1"},
	); err != nil {
		return err
	}
	return InitSubmodules(path, g.submoduleReferencePath())
}

// WorktreeAddExisting creates a new worktree at the given path for an existing branch.
// Skips LFS smudge filter during checkout (see WorktreeAddFromRef).
func (g *Git) WorktreeAddExisting(path, branch string) error {
	if _, err := g.runWithEnv(
		[]string{"worktree", "add", path, branch},
		[]string{"GIT_LFS_SKIP_SMUDGE=1"},
	); err != nil {
		return err
	}
	return InitSubmodules(path, g.submoduleReferencePath())
}

// WorktreeAddExistingForce creates a new worktree even if the branch is already checked out elsewhere.
// This is useful for cross-rig worktrees where multiple clones need to be on main.
func (g *Git) WorktreeAddExistingForce(path, branch string) error {
	if _, err := g.run("worktree", "add", "--force", path, branch); err != nil {
		return err
	}
	return InitSubmodules(path, g.submoduleReferencePath())
}

// submoduleReferencePath returns the mayor/rig path to use as --reference
// for submodule init. For bare repos (.repo.git), this resolves to the
// sibling mayor/rig directory which contains the initialized submodules.
// Returns empty string if no suitable reference path exists or if the
// reference repo is a shallow clone (git rejects shallow references).
func (g *Git) submoduleReferencePath() string {
	// For bare repos, the gitDir is <rig>/.repo.git
	// The reference clone is at <rig>/mayor/rig/
	if g.gitDir != "" {
		rigDir := filepath.Dir(g.gitDir)
		mayorRig := filepath.Join(rigDir, "mayor", "rig")
		if isValidSubmoduleReference(mayorRig) {
			return mayorRig
		}
	}

	// For regular clones (workDir-based), the workDir itself could be mayor/rig
	// but we don't want to reference ourselves. Check for a sibling .repo.git
	// to find the rig root, then use mayor/rig.
	if g.workDir != "" {
		dir := g.workDir
		for i := 0; i < 4; i++ {
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			if _, err := os.Stat(filepath.Join(parent, ".repo.git")); err == nil {
				mayorRig := filepath.Join(parent, "mayor", "rig")
				if mayorRig != g.workDir && isValidSubmoduleReference(mayorRig) {
					return mayorRig
				}
				break
			}
			dir = parent
		}
	}

	return ""
}

// isValidSubmoduleReference checks if a path is suitable as a --reference
// for git submodule update. It must have a tracked .gitmodules and not be a
// shallow clone (git rejects shallow repos as references).
func isValidSubmoduleReference(repoPath string) bool {
	if !hasTrackedGitmodules(repoPath) {
		return false
	}
	// Check if shallow — git rev-parse --is-shallow-repository
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--is-shallow-repository")
	util.SetDetachedProcessGroup(cmd)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != "true"
}

// IsSparseCheckoutConfigured checks if sparse checkout is enabled for a given repo/worktree.
// This is used by doctor to detect legacy sparse checkout configurations that should be removed.
func IsSparseCheckoutConfigured(repoPath string) bool {
	cmd := exec.Command("git", "-C", repoPath, "config", "core.sparseCheckout")
	util.SetDetachedProcessGroup(cmd)
	output, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(output)) == "true"
}

// RemoveSparseCheckout disables sparse checkout for a repo/worktree and restores all files.
// This is used by doctor to clean up legacy sparse checkout configurations.
func RemoveSparseCheckout(repoPath string) error {
	// Use git sparse-checkout disable which properly restores hidden files
	cmd := exec.Command("git", "-C", repoPath, "sparse-checkout", "disable")
	util.SetDetachedProcessGroup(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("disabling sparse checkout: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

// WorktreeRemove removes a worktree.
func (g *Git) WorktreeRemove(path string, force bool) error {
	args := []string{"worktree", "remove", path}
	if force {
		args = append(args, "--force")
	}
	_, err := g.run(args...)
	return err
}

// WorktreeMove moves a worktree to a new path, updating all git references.
// This is the correct way to relocate a worktree — using os.Rename breaks
// the .git file and worktree registry references. (GH#2056)
func (g *Git) WorktreeMove(oldPath, newPath string) error {
	_, err := g.run("worktree", "move", oldPath, newPath)
	return err
}

// WorktreePrune removes worktree entries for deleted paths.
func (g *Git) WorktreePrune() error {
	_, err := g.run("worktree", "prune")
	return err
}

// Worktree represents a git worktree.
type Worktree struct {
	Path   string
	Branch string
	Commit string
}

// WorktreeList returns all worktrees for this repository.
func (g *Git) WorktreeList() ([]Worktree, error) {
	out, err := g.run("worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}

	var worktrees []Worktree
	var current Worktree

	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			if current.Path != "" {
				worktrees = append(worktrees, current)
				current = Worktree{}
			}
			continue
		}

		switch {
		case strings.HasPrefix(line, "worktree "):
			current.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "HEAD "):
			current.Commit = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			current.Branch = strings.TrimPrefix(line, "branch refs/heads/")
		}
	}

	// Don't forget the last one
	if current.Path != "" {
		worktrees = append(worktrees, current)
	}

	return worktrees, nil
}

// BranchCreatedDate returns the date when a branch was created.
// This uses the committer date of the first commit on the branch.
// Returns date in YYYY-MM-DD format.
func (g *Git) BranchCreatedDate(branch string) (string, error) {
	// Get the date of the first commit on the branch that's not on the default branch
	// Use merge-base to find where the branch diverged
	defaultBranch := g.RemoteDefaultBranch()
	mergeBase, err := g.run("merge-base", defaultBranch, branch)
	if err != nil {
		// If merge-base fails, fall back to the branch tip's date
		out, err := g.run("log", "-1", "--format=%cs", branch)
		if err != nil {
			return "", err
		}
		return out, nil
	}

	// Get the first commit after the merge base on this branch
	out, err := g.run("log", "--format=%cs", "--reverse", mergeBase+".."+branch)
	if err != nil {
		return "", err
	}

	// Get the first line (first commit's date)
	lines := strings.Split(out, "\n")
	if len(lines) > 0 && lines[0] != "" {
		return lines[0], nil
	}

	// If no commits after merge-base, the branch points to merge-base
	// Return the merge-base commit date
	out, err = g.run("log", "-1", "--format=%cs", mergeBase)
	if err != nil {
		return "", err
	}
	return out, nil
}

// CommitsAhead returns the number of commits that branch has ahead of base.
// DiffStat returns the --stat output for a diff range (e.g., "main...feature").
func (g *Git) DiffStat(rangeSpec string) (string, error) {
	return g.run("diff", "--stat", rangeSpec)
}

// For example, CommitsAhead("main", "feature") returns how many commits
// are on feature that are not on main.
func (g *Git) CommitsAhead(base, branch string) (int, error) {
	out, err := g.run("rev-list", "--count", base+".."+branch)
	if err != nil {
		return 0, err
	}

	var count int
	_, err = fmt.Sscanf(out, "%d", &count)
	if err != nil {
		return 0, fmt.Errorf("parsing commit count: %w", err)
	}

	return count, nil
}

// CountCommitsBehind returns the number of commits that HEAD is behind the given ref.
// For example, CountCommitsBehind("origin/main") returns how many commits
// are on origin/main that are not on the current HEAD.
func (g *Git) CountCommitsBehind(ref string) (int, error) {
	out, err := g.run("rev-list", "--count", "HEAD.."+ref)
	if err != nil {
		return 0, err
	}

	var count int
	_, err = fmt.Sscanf(out, "%d", &count)
	if err != nil {
		return 0, fmt.Errorf("parsing commit count: %w", err)
	}

	return count, nil
}

// BranchContamination holds the result of a branch contamination check.
type BranchContamination struct {
	Behind int // commits HEAD is behind base (e.g., origin/main)
	Ahead  int // commits HEAD is ahead of base
}

// CheckBranchContamination checks whether the current branch has diverged
// significantly from a base ref (typically origin/main). Returns the number
// of commits behind and ahead, letting callers decide severity thresholds.
// (GH#2220)
func (g *Git) CheckBranchContamination(baseRef string) (BranchContamination, error) {
	var result BranchContamination

	behind, err := g.CountCommitsBehind(baseRef)
	if err != nil {
		return result, fmt.Errorf("counting commits behind %s: %w", baseRef, err)
	}
	result.Behind = behind

	ahead, err := g.CommitsAhead(baseRef, "HEAD")
	if err != nil {
		return result, fmt.Errorf("counting commits ahead of %s: %w", baseRef, err)
	}
	result.Ahead = ahead

	return result, nil
}

// StashCount returns the number of stashes belonging to the current branch.
// Git stashes are stored in the main repo (.git/refs/stash) and shared across
// all worktrees. Counting all stashes is incorrect for worktree-based polecats:
// a fresh polecat worktree would inherit stash count from siblings, blocking
// Remove(force=true) on work it never created. Filter by current branch name
// to only count stashes that actually belong to this worktree.
func (g *Git) StashCount() (int, error) {
	out, err := g.run("stash", "list")
	if err != nil {
		return 0, err
	}

	if out == "" {
		return 0, nil
	}

	// Get current branch to filter stashes.
	// If we can't determine the branch (detached HEAD, error), count all
	// stashes as a safe fallback — better to over-count than silently lose work.
	branch, branchErr := g.CurrentBranch()
	filterByBranch := branchErr == nil && branch != "" && branch != "HEAD"

	// Stash reflog lines have the format:
	//   stash@{N}: WIP on <branch>: <hash> <message>
	//   stash@{N}: On <branch>: <message>
	// We anchor the match to ": WIP on <branch>:" or ": On <branch>:" to avoid
	// false positives from commit messages that happen to contain "on <branch>:".
	wipPrefix := ": WIP on " + branch + ":"
	onPrefix := ": On " + branch + ":"

	lines := strings.Split(out, "\n")
	count := 0
	for _, line := range lines {
		if line == "" {
			continue
		}
		if filterByBranch {
			if !strings.Contains(line, wipPrefix) && !strings.Contains(line, onPrefix) {
				continue
			}
		}
		count++
	}
	return count, nil
}

// StashEntry represents one entry from `git stash list`, scoped to the current branch.
type StashEntry struct {
	Ref     string // e.g. "stash@{2}"
	Message string // e.g. "WIP on main: <hash> <subject>"
}

// StashListForBranch returns all stash entries belonging to the current branch,
// ordered as `git stash list` returns them (newest first, i.e. stash@{0} first).
// Filtering matches StashCount: only entries with ": WIP on <branch>:" or
// ": On <branch>:" prefixes are returned, since stashes are global to the repo
// but conceptually belong to the worktree where they were created.
func (g *Git) StashListForBranch() ([]StashEntry, error) {
	out, err := g.run("stash", "list")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	branch, branchErr := g.CurrentBranch()
	filterByBranch := branchErr == nil && branch != "" && branch != "HEAD"
	wipPrefix := ": WIP on " + branch + ":"
	onPrefix := ": On " + branch + ":"

	var entries []StashEntry
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if filterByBranch {
			if !strings.Contains(line, wipPrefix) && !strings.Contains(line, onPrefix) {
				continue
			}
		}
		// Lines have the form "stash@{N}: <message>"
		colonIdx := strings.Index(line, ":")
		if colonIdx <= 0 {
			continue
		}
		entries = append(entries, StashEntry{
			Ref:     line[:colonIdx],
			Message: strings.TrimSpace(line[colonIdx+1:]),
		})
	}
	return entries, nil
}

// StashPop applies the given stash ref to the working tree and drops it on success.
// Returns an error if the pop has conflicts (working tree is left as-is for manual
// resolution). Callers should treat conflict errors as "stop, escalate to user".
func (g *Git) StashPop(ref string) error {
	if ref == "" {
		return fmt.Errorf("stash ref required")
	}
	if _, err := g.run("stash", "pop", ref); err != nil {
		return fmt.Errorf("git stash pop %s: %w", ref, err)
	}
	return nil
}

// UnpushedCommits returns the number of commits that are not pushed to the remote.
// It checks if the current branch has an upstream and counts commits ahead.
// Returns 0 if there is no upstream configured.
func (g *Git) UnpushedCommits() (int, error) {
	// Get the upstream branch
	upstream, err := g.run("rev-parse", "--abbrev-ref", "@{u}")
	if err != nil {
		// No upstream configured - this is common for polecat branches
		// Check if we can compare against origin/main instead
		// If we can't get any reference, return 0 (benefit of the doubt)
		return 0, nil
	}

	// Count commits between upstream and HEAD
	out, err := g.run("rev-list", "--count", upstream+"..HEAD")
	if err != nil {
		return 0, err
	}

	var count int
	_, err = fmt.Sscanf(out, "%d", &count)
	if err != nil {
		return 0, fmt.Errorf("parsing unpushed count: %w", err)
	}

	return count, nil
}

// UncommittedWorkStatus contains information about uncommitted work in a repo.
type UncommittedWorkStatus struct {
	HasUncommittedChanges bool
	StashCount            int
	UnpushedCommits       int
	// Details for error messages
	ModifiedFiles  []string
	UntrackedFiles []string
}

// Clean returns true if there is no uncommitted work.
func (s *UncommittedWorkStatus) Clean() bool {
	return !s.HasUncommittedChanges && s.StashCount == 0 && s.UnpushedCommits == 0
}

// CleanExcludingBeads returns true if the only uncommitted changes are .beads/ files.
// This is useful for polecat stale detection where beads database files are synced
// across worktrees and shouldn't block cleanup.
func (s *UncommittedWorkStatus) CleanExcludingBeads() bool {
	// Stashes and unpushed commits always count as uncommitted work
	if s.StashCount > 0 || s.UnpushedCommits > 0 {
		return false
	}

	// Check if all modified files are beads files
	for _, f := range s.ModifiedFiles {
		if !isBeadsPath(f) {
			return false
		}
	}

	// Check if all untracked files are beads files
	for _, f := range s.UntrackedFiles {
		if !isBeadsPath(f) {
			return false
		}
	}

	return true
}

// isBeadsPath returns true if the path is a .beads/ file.
func isBeadsPath(path string) bool {
	return strings.Contains(path, ".beads/") || strings.Contains(path, ".beads\\")
}

// isGasTownRuntimePath returns true if the path is a Gas Town or Cursor runtime
// artifact that should not block gt done. These paths are managed by the toolchain,
// not by the developer, and are normally gitignored via EnsureGitignorePatterns.
func isGasTownRuntimePath(path string) bool {
	prefixes := []string{
		".beads/", ".beads\\",
		".claude/", ".claude\\",
		".runtime/", ".runtime\\",
		".logs/", ".logs\\",
		"__pycache__/", "__pycache__\\",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) || strings.Contains(path, "/"+prefix) {
			return true
		}
	}
	// Also match bare directory entries from git status (e.g. ".claude/")
	bare := strings.TrimSuffix(strings.TrimSuffix(path, "/"), "\\")
	for _, name := range []string{".beads", ".claude", ".runtime", ".logs", "__pycache__"} {
		if bare == name {
			return true
		}
	}
	// CLAUDE.local.md is a Gas Town overlay file written by CreatePolecatCLAUDEmd.
	// It must not be staged by the auto-commit safety net or committed to the repo.
	if bare == "CLAUDE.local.md" {
		return true
	}
	return false
}

// CleanExcludingRuntime returns true if the only uncommitted changes are Gas Town
// runtime artifacts (.beads/, .claude/, .runtime/, .logs/, __pycache__/).
// Used by gt done to avoid blocking completion on toolchain-managed files.
//
// Note: UnpushedCommits and StashCount are intentionally NOT checked here. This
// function only evaluates whether uncommitted *file* changes are runtime artifacts.
// Unpushed commits represent committed (but not yet pushed) work, and stashes
// survive worktree deletion — both are handled separately and shouldn't block
// completion on runtime-only dirt (gas-7vg).
func (s *UncommittedWorkStatus) CleanExcludingRuntime() bool {
	for _, f := range s.ModifiedFiles {
		if !isGasTownRuntimePath(f) {
			return false
		}
	}

	for _, f := range s.UntrackedFiles {
		if !isGasTownRuntimePath(f) {
			return false
		}
	}

	return true
}

// String returns a human-readable summary of uncommitted work.
func (s *UncommittedWorkStatus) String() string {
	var issues []string
	if s.HasUncommittedChanges {
		issues = append(issues, fmt.Sprintf("%d uncommitted change(s)", len(s.ModifiedFiles)+len(s.UntrackedFiles)))
	}
	if s.StashCount > 0 {
		issues = append(issues, fmt.Sprintf("%d stash(es)", s.StashCount))
	}
	if s.UnpushedCommits > 0 {
		issues = append(issues, fmt.Sprintf("%d unpushed commit(s)", s.UnpushedCommits))
	}
	if len(issues) == 0 {
		return "clean"
	}
	return strings.Join(issues, ", ")
}

// CheckUncommittedWork performs a comprehensive check for uncommitted work.
func (g *Git) CheckUncommittedWork() (*UncommittedWorkStatus, error) {
	status := &UncommittedWorkStatus{}

	// Check git status
	gitStatus, err := g.Status()
	if err != nil {
		return nil, fmt.Errorf("checking git status: %w", err)
	}
	status.HasUncommittedChanges = !gitStatus.Clean
	status.ModifiedFiles = append(gitStatus.Modified, gitStatus.Added...)
	status.ModifiedFiles = append(status.ModifiedFiles, gitStatus.Deleted...)
	status.UntrackedFiles = gitStatus.Untracked

	// Check stashes
	stashCount, err := g.StashCount()
	if err != nil {
		return nil, fmt.Errorf("checking stashes: %w", err)
	}
	status.StashCount = stashCount

	// Check unpushed commits
	unpushed, err := g.UnpushedCommits()
	if err != nil {
		return nil, fmt.Errorf("checking unpushed commits: %w", err)
	}
	status.UnpushedCommits = unpushed

	return status, nil
}

// BranchPushedToRemote checks if a branch has been pushed to the remote.
// Returns (pushed bool, unpushedCount int, err).
// This handles polecat branches that don't have upstream tracking configured.
func (g *Git) BranchPushedToRemote(localBranch, remote string) (bool, int, error) {
	remoteBranch := remote + "/" + localBranch

	// Resolve the push URL: with a split fetch/push configuration (e.g.,
	// polecats pushing to a local bare repo), ls-remote against the remote
	// name resolves the fetch URL (GitHub) not the push target.
	lsTarget := remote
	if fetchURL, ferr := g.RemoteURL(remote); ferr == nil {
		if pushURL, perr := g.GetPushURL(remote); perr == nil && pushURL != fetchURL {
			lsTarget = pushURL
		}
	}

	// Check if the remote branch exists via ls-remote and save the output.
	// The output contains the SHA which we reuse in the fallback path below,
	// avoiding a redundant second ls-remote call.
	lsOut, err := g.run("ls-remote", "--heads", lsTarget, localBranch)
	if err != nil {
		return false, 0, fmt.Errorf("checking remote branch: %w", err)
	}

	if lsOut == "" {
		// Remote branch doesn't exist - count commits since origin/main (or HEAD if that fails)
		count, err := g.run("rev-list", "--count", "origin/main..HEAD")
		if err != nil {
			// Fallback: just count all commits on HEAD
			count, err = g.run("rev-list", "--count", "HEAD")
			if err != nil {
				return false, 0, fmt.Errorf("counting commits: %w", err)
			}
		}
		var n int
		_, err = fmt.Sscanf(count, "%d", &n)
		if err != nil {
			return false, 0, fmt.Errorf("parsing commit count: %w", err)
		}
		// If there are any commits since main, branch is not pushed
		return n == 0, n, nil
	}

	// Remote branch exists - fetch to ensure we have the local tracking ref
	// This handles the case where we just pushed and origin/branch doesn't exist locally yet
	_, fetchErr := g.run("fetch", remote, localBranch)

	// In worktrees, the fetch may not update refs/remotes/origin/<branch> due to
	// missing refspecs. If the remote ref doesn't exist locally, create it from FETCH_HEAD.
	// See: gt-cehl8 (gt done fails in worktrees due to missing origin tracking ref)
	remoteRef := "refs/remotes/" + remoteBranch
	if _, err := g.run("rev-parse", "--verify", remoteRef); err != nil {
		// Remote ref doesn't exist locally - update it from FETCH_HEAD if fetch succeeded.
		// Best-effort: if this fails, the code below falls back to the saved ls-remote SHA.
		if fetchErr == nil {
			_, _ = g.run("update-ref", remoteRef, "FETCH_HEAD")
		}
	}

	// Check if local is ahead
	count, err := g.run("rev-list", "--count", remoteBranch+"..HEAD")
	if err != nil {
		// Fallback: If we can't use the tracking ref (possibly missing remote.origin.fetch),
		// use the SHA from the ls-remote call above instead of hitting the network again.
		// See: gt-0eh3r (gt done fails in worktree with missing remote.origin.fetch config)
		parts := strings.Fields(strings.TrimSpace(lsOut))
		if len(parts) == 0 {
			return false, 0, fmt.Errorf("counting unpushed commits: %w (invalid ls-remote output)", err)
		}
		remoteSHA := parts[0]

		// Count commits from remote SHA to HEAD
		count, err = g.run("rev-list", "--count", remoteSHA+"..HEAD")
		if err != nil {
			return false, 0, fmt.Errorf("counting unpushed commits (fallback): %w", err)
		}
	}

	var n int
	_, err = fmt.Sscanf(count, "%d", &n)
	if err != nil {
		return false, 0, fmt.Errorf("parsing unpushed count: %w", err)
	}

	return n == 0, n, nil
}

// PrunedBranch represents a local branch that was pruned (or would be pruned in dry-run).
type PrunedBranch struct {
	Name   string // Branch name (e.g., "polecat/rictus-mkb0vq9f")
	Reason string // Why it was pruned: "merged", "no-remote", "no-remote-merged"
}

// PruneStaleBranches finds and deletes local branches matching a pattern that are
// stale — either fully merged to the default branch or whose remote tracking branch
// no longer exists (indicating the remote branch was deleted after merge).
//
// This addresses cross-clone branch accumulation: when polecats push branches to
// origin, other clones create local tracking branches via git fetch. After the
// remote branch is deleted (post-merge), git fetch --prune removes the remote
// tracking ref but the local branch persists indefinitely.
//
// Safety: never deletes the current branch or the default branch (main/master).
// Uses git branch -d (not -D), so only fully-merged branches are deleted.
func (g *Git) PruneStaleBranches(pattern string, dryRun bool) ([]PrunedBranch, error) {
	if pattern == "" {
		pattern = "polecat/*"
	}

	// Get current branch to avoid deleting it
	currentBranch, _ := g.CurrentBranch()
	defaultBranch := g.RemoteDefaultBranch()

	// List all local branches matching the pattern
	branches, err := g.ListBranches(pattern)
	if err != nil {
		return nil, fmt.Errorf("listing branches: %w", err)
	}

	var pruned []PrunedBranch
	for _, branch := range branches {
		branch = strings.TrimSpace(branch)
		if branch == "" || branch == currentBranch || branch == defaultBranch {
			continue
		}

		// Check if the remote tracking branch still exists
		hasRemote, err := g.RemoteTrackingBranchExists("origin", branch)
		if err != nil {
			continue // Skip on error, don't fail the whole operation
		}

		// Check if the branch is merged to the default branch
		merged, err := g.IsAncestor(branch, "origin/"+defaultBranch)
		if err != nil {
			// If we can't determine merge status, only prune if remote is gone
			if hasRemote {
				continue
			}
			// Remote gone and can't check merge status — skip to be safe
			continue
		}

		var reason string
		if merged && !hasRemote {
			reason = "no-remote-merged"
		} else if merged {
			reason = "merged"
		} else if !hasRemote {
			reason = "no-remote"
		} else {
			continue // Branch has remote and is not merged — keep it
		}

		if !dryRun {
			// Use -d (not -D) for safety — only deletes fully merged branches.
			// For "no-remote" branches that aren't merged, -d will fail safely.
			if err := g.DeleteBranch(branch, false); err != nil {
				// If -d fails (not merged), skip this branch
				continue
			}
		}

		pruned = append(pruned, PrunedBranch{
			Name:   branch,
			Reason: reason,
		})
	}

	return pruned, nil
}

// SubmoduleChange represents a changed submodule pointer between two refs.
type SubmoduleChange struct {
	Path   string // Submodule path relative to repo root
	OldSHA string // Previous commit SHA (or empty for new submodule)
	NewSHA string // New commit SHA (or empty for removed submodule)
	URL    string // Submodule remote URL from .gitmodules
}

// InitSubmodules initializes and updates submodules if .gitmodules exists.
// This is a no-op for repos without submodules.
//
// If referencePath is non-empty and contains submodules, --reference is used
// to share git objects from a local clone instead of fetching from remote.
// This makes submodule init near-instant for large submodules (e.g. 655MB gitlabhq).
func InitSubmodules(repoPath string, referencePath ...string) error {
	if !hasTrackedGitmodules(repoPath) {
		return nil
	}

	args := []string{"-C", repoPath, "submodule", "update", "--init", "--recursive"}

	// Use --reference to share objects from a local clone (avoids remote fetch)
	if len(referencePath) > 0 && referencePath[0] != "" {
		refPath := referencePath[0]
		if hasTrackedGitmodules(refPath) {
			args = append(args, "--reference", refPath)
		}
	}

	cmd := exec.Command("git", args...)
	util.SetDetachedProcessGroup(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("initializing submodules: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

// hasTrackedGitmodules checks whether .gitmodules exists on disk AND is tracked
// by git. After a submodule-to-monorepo migration, .gitmodules may linger as an
// untracked file (e.g., in a stale mayor/rig clone or bare repo worktree) even
// though it has been removed from the repository. Checking only os.Stat would
// incorrectly trigger submodule init on these stale artifacts.
func hasTrackedGitmodules(repoPath string) bool {
	gitmodules := filepath.Join(repoPath, ".gitmodules")
	if _, err := os.Stat(gitmodules); os.IsNotExist(err) {
		return false
	}
	// Verify .gitmodules is actually tracked in the index.
	cmd := exec.Command("git", "-C", repoPath, "ls-files", "--error-unmatch", ".gitmodules")
	return cmd.Run() == nil
}

// InitSparseCheckout initializes sparse checkout with cone mode and configures
// the given paths. If paths is empty, initializes with cone mode only (checkout root files).
func InitSparseCheckout(repoPath string, paths []string) error {
	// Initialize sparse checkout in cone mode
	cmd := exec.Command("git", "-C", repoPath, "sparse-checkout", "init", "--cone")
	util.SetDetachedProcessGroup(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("initializing sparse checkout: %s", strings.TrimSpace(stderr.String()))
	}
	if len(paths) > 0 {
		args := append([]string{"-C", repoPath, "sparse-checkout", "set"}, paths...)
		cmd = exec.Command("git", args...)
		util.SetDetachedProcessGroup(cmd)
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("setting sparse checkout paths: %s", strings.TrimSpace(stderr.String()))
		}
	}
	return nil
}

// SubmoduleChanges detects submodule pointer changes between two refs.
// Returns nil if no submodules changed or if the repo has no submodules.
func (g *Git) SubmoduleChanges(base, head string) ([]SubmoduleChange, error) {
	// git diff --raw shows mode 160000 for gitlink (submodule) entries
	out, err := g.run("diff", "--raw", base, head)
	if err != nil {
		return nil, fmt.Errorf("diffing for submodule changes: %w", err)
	}
	if out == "" {
		return nil, nil
	}

	var changes []SubmoduleChange
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: :oldmode newmode oldsha newsha status\tpath
		// Submodule entries have mode 160000
		if !strings.Contains(line, "160000") {
			continue
		}
		// Parse the raw diff line
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		path := strings.TrimSpace(parts[1])
		// Skip .claude/ paths — Claude Code creates worktrees under
		// .claude/worktrees/ with .git files (worktree pointers) that git
		// reports as gitlinks. These are not real submodules. (gt-dg7)
		if strings.HasPrefix(path, ".claude/") {
			continue
		}
		fields := strings.Fields(parts[0])
		if len(fields) < 5 {
			continue
		}
		oldSHA := fields[2]
		newSHA := fields[3]
		// Null SHAs (all zeros) indicate added/removed submodules
		if strings.Repeat("0", len(oldSHA)) == oldSHA {
			oldSHA = ""
		}
		if strings.Repeat("0", len(newSHA)) == newSHA {
			newSHA = ""
		}

		change := SubmoduleChange{
			Path:   path,
			OldSHA: oldSHA,
			NewSHA: newSHA,
		}

		// Try to get the submodule URL from .gitmodules on the head ref
		url, urlErr := g.submoduleURL(head, path)
		if urlErr == nil {
			change.URL = url
		}

		changes = append(changes, change)
	}
	return changes, nil
}

// submoduleURL reads the URL for a submodule from .gitmodules at a given ref.
// Uses git config -f to parse the file correctly regardless of field ordering.
func (g *Git) submoduleURL(ref, submodulePath string) (string, error) {
	// Write .gitmodules from the ref to a temp file so we can use git config -f
	content, err := g.run("show", ref+":.gitmodules")
	if err != nil {
		return "", err
	}
	tmpFile, err := os.CreateTemp("", "gitmodules-*")
	if err != nil {
		return "", fmt.Errorf("creating temp file for .gitmodules: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.WriteString(content); err != nil {
		tmpFile.Close()
		return "", fmt.Errorf("writing temp .gitmodules: %w", err)
	}
	tmpFile.Close()

	// List all submodule.<name>.path entries to find the section matching our path
	cmd := exec.Command("git", "config", "-f", tmpFile.Name(), "--get-regexp", `^submodule\..*\.path$`)
	util.SetDetachedProcessGroup(cmd)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("reading submodule paths from .gitmodules: %w", err)
	}

	var sectionName string
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: submodule.<name>.path <value>
		parts := strings.SplitN(line, " ", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[1]) == submodulePath {
			key := parts[0]
			key = strings.TrimPrefix(key, "submodule.")
			key = strings.TrimSuffix(key, ".path")
			sectionName = key
			break
		}
	}
	if sectionName == "" {
		return "", fmt.Errorf("submodule URL not found for path %s", submodulePath)
	}

	// Get the URL for this section
	urlCmd := exec.Command("git", "config", "-f", tmpFile.Name(), "--get", "submodule."+sectionName+".url")
	util.SetDetachedProcessGroup(urlCmd)
	var urlOut bytes.Buffer
	urlCmd.Stdout = &urlOut
	if err := urlCmd.Run(); err != nil {
		return "", fmt.Errorf("reading URL for submodule %s: %w", sectionName, err)
	}
	url := strings.TrimSpace(urlOut.String())
	if url == "" {
		return "", fmt.Errorf("submodule URL not found for path %s", submodulePath)
	}
	return url, nil
}

// PushSubmoduleCommit pushes a specific commit SHA from a submodule to its remote.
// The submodulePath is relative to the repo working directory.
// The commit must exist in the submodule's object store (shared via .repo.git/modules/).
func (g *Git) PushSubmoduleCommit(submodulePath, sha, remote string) error {
	absPath := filepath.Join(g.workDir, submodulePath)
	// Detect the remote's default branch (don't assume main)
	defaultBranch, err := submoduleDefaultBranch(absPath, remote)
	if err != nil {
		return fmt.Errorf("detecting default branch for submodule %s: %w", submodulePath, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", absPath, "push", remote, sha+":refs/heads/"+defaultBranch)
	util.SetDetachedProcessGroup(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("pushing submodule %s timed out after %v (remote may be unreachable)", submodulePath, pushTimeout)
		}
		abbrev := sha
		if len(abbrev) > 8 {
			abbrev = abbrev[:8]
		}
		return fmt.Errorf("pushing submodule %s commit %s: %s", submodulePath, abbrev, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// submoduleDefaultBranch detects the default branch of a submodule's remote.
// Tries local refs first to avoid network round-trips, falling back to remote queries.
func submoduleDefaultBranch(submodulePath, remote string) (string, error) {
	// Try local symbolic-ref first (no network, fastest)
	symCmd := exec.Command("git", "-C", submodulePath, "symbolic-ref", "refs/remotes/"+remote+"/HEAD")
	util.SetDetachedProcessGroup(symCmd)
	if symOut, err := symCmd.Output(); err == nil {
		ref := strings.TrimSpace(string(symOut))
		// refs/remotes/origin/HEAD -> refs/remotes/origin/main -> main
		if parts := strings.Split(ref, "/"); len(parts) > 0 {
			branch := parts[len(parts)-1]
			if branch != "" {
				return branch, nil
			}
		}
	}

	// Try local tracking refs (no network)
	for _, candidate := range []string{"main", "master"} {
		check := exec.Command("git", "-C", submodulePath, "rev-parse", "--verify", "--quiet", "refs/remotes/"+remote+"/"+candidate)
		util.SetDetachedProcessGroup(check)
		if check.Run() == nil {
			return candidate, nil
		}
	}

	// Fallback: network query via ls-remote
	for _, candidate := range []string{"main", "master"} {
		check := exec.Command("git", "-C", submodulePath, "ls-remote", "--exit-code", remote, "refs/heads/"+candidate)
		util.SetDetachedProcessGroup(check)
		if check.Run() == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not determine default branch for remote %s", remote)
}
