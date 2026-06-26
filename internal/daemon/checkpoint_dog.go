package daemon

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/util"
)

const (
	defaultCheckpointDogInterval = 10 * time.Minute
)

// CheckpointDogConfig holds configuration for the checkpoint_dog patrol.
type CheckpointDogConfig struct {
	// Enabled controls whether the checkpoint dog runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "10m").
	IntervalStr string `json:"interval,omitempty"`
}

// checkpointDogInterval returns the configured interval, or the default (10m).
func checkpointDogInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.CheckpointDog != nil {
		if config.Patrols.CheckpointDog.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.CheckpointDog.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultCheckpointDogInterval
}

// runtimeExcludeDirs are directories to unstage after git add -A.
// These contain runtime/ephemeral data that should not be checkpointed.
var runtimeExcludeDirs = []string{
	".claude/",
	".beads/",
	".runtime/",
	"__pycache__/",
}

// runCheckpointDog auto-commits WIP changes in active polecat worktrees.
// This protects against data loss when sessions crash or hit context limits.
//
// ## ZFC Exemption
// The checkpoint dog executes git operations directly (same pattern as
// compactor_dog's SQL operations). The daemon pours a molecule for
// observability, then runs git commands via exec.Command.
func (d *Daemon) runCheckpointDog() {
	if !d.isPatrolActive("checkpoint_dog") {
		return
	}

	d.logger.Printf("checkpoint_dog: starting cycle")

	mol := d.pourDogMolecule(constants.MolDogCheckpoint, nil)
	defer mol.close()

	rigs := d.getKnownRigs()
	totalScanned := 0
	totalCheckpointed := 0

	for _, rigName := range rigs {
		scanned, checkpointed := d.checkpointRigPolecats(rigName)
		totalScanned += scanned
		totalCheckpointed += checkpointed
	}

	mol.closeStep("scan")
	mol.closeStep("checkpoint")

	d.logger.Printf("checkpoint_dog: cycle complete — scanned %d worktrees, checkpointed %d",
		totalScanned, totalCheckpointed)
	mol.closeStep("report")
}

// checkpointRigPolecats checkpoints dirty polecat worktrees in a single rig.
// Returns (scanned, checkpointed) counts.
func (d *Daemon) checkpointRigPolecats(rigName string) (int, int) {
	polecatsDir := filepath.Join(d.config.TownRoot, rigName, "polecats")
	polecats, err := listPolecatWorktrees(polecatsDir)
	if err != nil {
		return 0, 0
	}

	scanned := 0
	checkpointed := 0

	for _, polecatName := range polecats {
		scanned++

		// Check if tmux session is alive — only checkpoint active sessions.
		// Dead sessions can't benefit from checkpoints.
		sessionName := session.PolecatSessionName(session.PrefixFor(rigName), polecatName)
		alive, err := d.tmux.HasSession(sessionName)
		if err != nil {
			d.logger.Printf("checkpoint_dog: error checking session %s: %v", sessionName, err)
			continue
		}
		if !alive {
			continue
		}

		// Polecat layout: prefer <polecatsDir>/<name>/<rigName>/ (the new
		// nested layout where the outer <name>/ dir is a container with
		// per-polecat scaffolding and the inner dir is the actual git
		// worktree). Fall back to <polecatsDir>/<name>/ for the legacy
		// flat layout still supported by polecat.Manager. Both candidates
		// must contain `.git` — never fall back to a parent dir, since
		// the original bug here was exactly that: an empty <name>/
		// container caused git to walk up to the top-level workspace's
		// .git and commit "WIP: checkpoint (auto)" on the workspace's
		// branch (usually main) instead of the polecat's branch.
		// (gt-checkpoint-workdir fix.)
		workDir := resolveCheckpointWorkDir(polecatsDir, polecatName, rigName)
		if workDir == "" {
			continue // Neither layout has a usable .git — skip silently.
		}
		if d.checkpointWorktree(workDir, rigName, polecatName) {
			checkpointed++
		}
	}

	return scanned, checkpointed
}

// checkpointWorktree creates a WIP checkpoint commit for a single worktree.
// Returns true if a checkpoint was created.
func (d *Daemon) checkpointWorktree(workDir, rigName, polecatName string) bool {
	// Check git status (exclude runtime dirs from consideration)
	statusOut, err := runGitCmd(workDir, "status", "--porcelain")
	if err != nil {
		d.logger.Printf("checkpoint_dog: git status failed in %s/%s: %v", rigName, polecatName, err)
		return false
	}
	if strings.TrimSpace(statusOut) == "" {
		return false // Clean worktree
	}

	// Stage everything
	if _, err := runGitCmd(workDir, "add", "-A"); err != nil {
		d.logger.Printf("checkpoint_dog: git add -A failed in %s/%s: %v", rigName, polecatName, err)
		return false
	}

	// Unstage runtime/ephemeral directories
	for _, dir := range runtimeExcludeDirs {
		// git reset HEAD -- <dir> is safe even if dir doesn't exist (exits 0)
		_, _ = runGitCmd(workDir, "reset", "HEAD", "--", dir)
	}

	// Unstage nested runtime/ephemeral dirs (e.g. web/.beads/, service/foo/.claude/).
	// The root-level loop above only resets top-level dirs like .beads/ — it does NOT
	// cover paths like web/.beads/redirect. When a file was previously tracked on the
	// branch (committed before a gitignore rule landed), git add -A re-stages it on
	// every checkpoint even though it is now gitignored, because git tracks changes to
	// known paths regardless of .gitignore. Scanning staged paths and unstaging any
	// that contain a runtime-dir component at any depth fixes this.
	if stagedOut, err := runGitCmd(workDir, "diff", "--cached", "--name-only"); err == nil {
		for _, dir := range nestedRuntimeArtifactDirs(stagedOut) {
			_, _ = runGitCmd(workDir, "reset", "HEAD", "--", dir)
		}
	}

	// Unstage deletions of tracked files. A checkpoint should preserve work
	// (additions + modifications), never commit deletions of tracked files.
	// This prevents the bug where a polecat's working tree has a missing
	// tracked file and the checkpoint commits the deletion (gt-pvx fix).
	if delOut, err := runGitCmd(workDir, "diff", "--cached", "--name-only", "--diff-filter=D"); err == nil {
		if dels := strings.TrimSpace(delOut); dels != "" {
			for _, f := range strings.Split(dels, "\n") {
				if f != "" {
					_, _ = runGitCmd(workDir, "reset", "HEAD", "--", f)
				}
			}
		}
	}

	// Check if anything is staged after exclusions
	diffOut, err := runGitCmd(workDir, "diff", "--cached", "--quiet")
	if err == nil && strings.TrimSpace(diffOut) == "" {
		// --quiet exits 0 if no diff → nothing staged
		return false
	}

	// Commit the checkpoint
	if _, err := runGitCmd(workDir, "commit", "-m", "WIP: checkpoint (auto)"); err != nil {
		d.logger.Printf("checkpoint_dog: git commit failed in %s/%s: %v", rigName, polecatName, err)
		return false
	}

	d.logger.Printf("checkpoint_dog: created WIP checkpoint in %s/%s", rigName, polecatName)
	return true
}

// isGitWorktree reports whether the given directory is the root of a git
// worktree (has its own `.git` file or directory). Used to guard checkpoint
// commits against the "wrong-dir" failure mode where git operations in a
// non-worktree directory walk up the filesystem tree and commit on the
// parent workspace's branch.
func isGitWorktree(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// resolveCheckpointWorkDir picks the actual git-worktree directory for a
// polecat, supporting both the new nested layout (polecats/<name>/<rigName>/)
// and the legacy flat layout (polecats/<name>/) that polecat.Manager still
// recognizes for backward compatibility. Returns "" if neither candidate is
// a git worktree, in which case the caller MUST skip the polecat — never
// fall back to a parent directory, since git would walk up to the top-level
// workspace's .git and commit on the wrong branch (this is the bug this
// helper exists to prevent).
func resolveCheckpointWorkDir(polecatsDir, polecatName, rigName string) string {
	nested := filepath.Join(polecatsDir, polecatName, rigName)
	if isGitWorktree(nested) {
		return nested
	}
	flat := filepath.Join(polecatsDir, polecatName)
	if isGitWorktree(flat) {
		return flat
	}
	return ""
}

// nestedRuntimeArtifactDirs scans staged file paths and returns the deduplicated
// set of path prefixes that end at a runtime artifact directory component. It
// skips root-level dirs (depth 0) because those are already handled by the
// runtimeExcludeDirs reset loop. Only truly nested occurrences (depth ≥ 1) are
// returned so callers can issue targeted git reset HEAD -- <prefix>/ commands.
//
// Example: given "web/.beads/redirect\nservice/foo/.claude/settings.json",
// returns ["web/.beads/", "service/foo/.claude/"].
func nestedRuntimeArtifactDirs(stagedOutput string) []string {
	seen := make(map[string]bool)
	var dirs []string
	for _, f := range strings.Split(strings.TrimSpace(stagedOutput), "\n") {
		if f == "" {
			continue
		}
		parts := strings.Split(filepath.ToSlash(f), "/")
		for i, part := range parts {
			if i == 0 {
				// Root-level already handled by runtimeExcludeDirs loop.
				continue
			}
			for _, dir := range runtimeExcludeDirs {
				if part == strings.TrimSuffix(dir, "/") {
					prefix := strings.Join(parts[:i+1], "/") + "/"
					if !seen[prefix] {
						seen[prefix] = true
						dirs = append(dirs, prefix)
					}
					break
				}
			}
		}
	}
	return dirs
}

// runGitCmd executes a git command in the given directory and returns stdout.
func runGitCmd(workDir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workDir
	util.SetDetachedProcessGroup(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("%s: %s", err, errMsg)
		}
		return "", err
	}

	return strings.TrimSpace(stdout.String()), nil
}
