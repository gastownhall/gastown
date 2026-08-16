package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/rig"
)

// BranchCheck detects persistent infrastructure roles (witness, refinery)
// that are not on the main branch. Crew members are excluded because they
// legitimately use feature branches during PR workflows.
type BranchCheck struct {
	FixableCheck
	offMainDirs []string // Cached during Run for use in Fix
}

// NewBranchCheck creates a new branch check.
func NewBranchCheck() *BranchCheck {
	return &BranchCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "persistent-role-branches",
				CheckDescription: "Detect infrastructure roles (witness/refinery) not on main branch",
				CheckCategory:    CategoryCleanup,
			},
		},
	}
}

// Run checks if persistent role directories are on main branch.
func (c *BranchCheck) Run(ctx *CheckContext) *CheckResult {
	var offMain []string
	var onMain int

	// Find all persistent role directories
	dirs := c.findPersistentRoleDirs(ctx.TownRoot)

	for _, dir := range dirs {
		branch, err := c.getCurrentBranch(dir)
		if err != nil {
			// Skip directories that aren't git repos
			continue
		}

		if c.isExpectedBranch(ctx.TownRoot, dir, branch) {
			onMain++
		} else {
			offMain = append(offMain, fmt.Sprintf("%s (on %s)", c.relativePath(ctx.TownRoot, dir), branch))
		}
	}

	// Cache for Fix
	c.offMainDirs = nil
	for _, dir := range dirs {
		branch, err := c.getCurrentBranch(dir)
		if err != nil {
			continue
		}
		if !c.isExpectedBranch(ctx.TownRoot, dir, branch) {
			c.offMainDirs = append(c.offMainDirs, dir)
		}
	}

	if len(offMain) == 0 {
		if onMain == 0 {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusOK,
				Message: "No persistent role directories found",
			}
		}
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("All %d persistent roles on main branch", onMain),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d persistent role(s) not on main branch", len(offMain)),
		Details: offMain,
		FixHint: "Run 'gt doctor --fix' to switch to main, or manually: git checkout main && git pull",
	}
}

// Fix switches all off-main directories to their expected branch.
// Uses the rig's default_branch if configured, otherwise "main".
func (c *BranchCheck) Fix(ctx *CheckContext) error {
	if len(c.offMainDirs) == 0 {
		return nil
	}

	var lastErr error
	for _, dir := range c.offMainDirs {
		targetBranch := c.expectedBranch(ctx.TownRoot, dir)

		if err := c.checkoutWithWorktreeRetry(dir, targetBranch); err != nil {
			lastErr = err
			continue
		}

		// git pull --rebase
		cmd := exec.Command("git", "pull", "--rebase")
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			// Pull failure is not fatal, just warn
			continue
		}
	}

	return lastErr
}

// checkoutWithWorktreeRetry attempts git checkout, and if it fails because the
// branch is already checked out in another worktree (typically .repo.git), it
// detaches that worktree's HEAD to free the branch and retries.
func (c *BranchCheck) checkoutWithWorktreeRetry(dir, branch string) error {
	cmd := exec.Command("git", "checkout", branch) //nolint:gosec // G204: branch name from config
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}

	// Check if failure is due to worktree branch conflict.
	// Git error looks like: fatal: 'main' is already checked out at '/path/to/.repo.git'
	conflictPath := parseWorktreeConflict(string(output))
	if conflictPath == "" {
		return fmt.Errorf("%s: git checkout %s failed: %s", dir, branch, strings.TrimSpace(string(output)))
	}

	// Only auto-resolve conflicts with bare repos (.repo.git).
	// Detaching HEAD in a bare repo is safe since it has no working tree.
	if !strings.HasSuffix(conflictPath, ".repo.git") {
		return fmt.Errorf("%s: branch %q is already checked out at %s (not a bare repo, cannot auto-resolve)",
			dir, branch, conflictPath)
	}

	// Detach the bare repo's HEAD to free the branch.
	// We use checkout --detach which points HEAD at the current commit without a branch.
	detachCmd := exec.Command("git", "-C", conflictPath, "checkout", "--detach") //nolint:gosec // G204: path from git output
	if detachOutput, detachErr := detachCmd.CombinedOutput(); detachErr != nil {
		return fmt.Errorf("%s: cannot detach HEAD in %s to free branch %q: %s",
			dir, conflictPath, branch, strings.TrimSpace(string(detachOutput)))
	}

	// Retry the checkout now that the branch is freed.
	retryCmd := exec.Command("git", "checkout", branch) //nolint:gosec // G204: branch name from config
	retryCmd.Dir = dir
	if retryOutput, retryErr := retryCmd.CombinedOutput(); retryErr != nil {
		return fmt.Errorf("%s: git checkout %s failed after detaching %s: %s",
			dir, branch, conflictPath, strings.TrimSpace(string(retryOutput)))
	}

	return nil
}

// parseWorktreeConflict extracts the conflicting path from a git checkout error.
// Returns empty string if the error is not a worktree branch conflict.
// Git versions use different formats:
//   - Older: "fatal: 'branch' is already checked out at '/path/to/repo'"
//   - Newer: "fatal: 'branch' is already used by worktree at '/path/to/repo'"
func parseWorktreeConflict(output string) string {
	markers := []string{
		"is already checked out at '",
		"is already used by worktree at '",
	}
	for _, marker := range markers {
		idx := strings.Index(output, marker)
		if idx < 0 {
			continue
		}
		rest := output[idx+len(marker):]
		endIdx := strings.Index(rest, "'")
		if endIdx < 0 {
			continue
		}
		return rest[:endIdx]
	}
	return ""
}

// expectedBranch returns the branch a persistent role directory should be on.
// Checks the rig's default_branch config, falling back to "main".
func (c *BranchCheck) expectedBranch(townRoot, dir string) string {
	rel, err := filepath.Rel(townRoot, dir)
	if err != nil {
		return "main"
	}
	parts := strings.SplitN(filepath.ToSlash(rel), "/", 2)
	if len(parts) < 1 {
		return "main"
	}
	rigPath := filepath.Join(townRoot, parts[0])
	cfg, err := rig.LoadRigConfig(rigPath)
	if err != nil || cfg.DefaultBranch == "" {
		return "main"
	}
	return cfg.DefaultBranch
}

// isExpectedBranch checks if a directory is on the expected branch.
// For rigs with a custom default_branch, that branch is expected.
// Otherwise, main or master are expected.
func (c *BranchCheck) isExpectedBranch(townRoot, dir, branch string) bool {
	if branch == "main" || branch == "master" {
		return true
	}
	// Derive the rig path from the directory.
	// Directories are like <town>/<rig>/refinery/rig or <town>/<rig>/crew/<name>.
	rel, err := filepath.Rel(townRoot, dir)
	if err != nil {
		return false
	}
	parts := strings.SplitN(filepath.ToSlash(rel), "/", 2)
	if len(parts) < 1 {
		return false
	}
	rigPath := filepath.Join(townRoot, parts[0])
	cfg, err := rig.LoadRigConfig(rigPath)
	if err != nil || cfg.DefaultBranch == "" {
		return false
	}
	return branch == cfg.DefaultBranch
}

// findPersistentRoleDirs finds infrastructure directories that should be on main:
// - <rig>/witness/rig (if exists)
// - <rig>/refinery/rig (if exists)
//
// Crew members are excluded because they legitimately use feature branches
// during PR workflows (fix/, feat/, chore/ branches).
func (c *BranchCheck) findPersistentRoleDirs(townRoot string) []string {
	var dirs []string

	// Find all rigs
	entries, err := os.ReadDir(townRoot)
	if err != nil {
		return dirs
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Skip non-rig directories
		name := entry.Name()
		if name == "mayor" || name == ".beads" || strings.HasPrefix(name, ".") {
			continue
		}

		rigPath := filepath.Join(townRoot, name)

		// Check if this looks like a rig (has crew/, polecats/, witness/, or refinery/)
		if !c.isRig(rigPath) {
			continue
		}

		// Add witness/rig if exists
		witnessRig := filepath.Join(rigPath, "witness", "rig")
		if _, err := os.Stat(witnessRig); err == nil {
			dirs = append(dirs, witnessRig)
		}

		// Add refinery/rig if exists
		refineryRig := filepath.Join(rigPath, "refinery", "rig")
		if _, err := os.Stat(refineryRig); err == nil {
			dirs = append(dirs, refineryRig)
		}
	}

	return dirs
}

// isRig checks if a directory looks like a rig.
func (c *BranchCheck) isRig(path string) bool {
	markers := []string{"crew", "polecats", "witness", "refinery"}
	for _, marker := range markers {
		if _, err := os.Stat(filepath.Join(path, marker)); err == nil {
			return true
		}
	}
	return false
}

// getCurrentBranch returns the current git branch for a directory.
func (c *BranchCheck) getCurrentBranch(dir string) (string, error) {
	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// relativePath returns path relative to base, or the full path if that fails.
func (c *BranchCheck) relativePath(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}

// CloneDivergenceCheck detects when git clones have drifted significantly apart.
// This is an emergency condition - all clones should be tracking origin/main
// and staying reasonably in sync. Divergence here is different from beads-sync
// divergence, which is expected.
type CloneDivergenceCheck struct {
	BaseCheck
}

// NewCloneDivergenceCheck creates a new clone divergence check.
func NewCloneDivergenceCheck() *CloneDivergenceCheck {
	return &CloneDivergenceCheck{
		BaseCheck: BaseCheck{
			CheckName:        "clone-divergence",
			CheckDescription: "Detect emergency divergence between git clones",
			CheckCategory:    CategoryCleanup,
		},
	}
}

// cloneInfo holds information about a single clone.
type cloneInfo struct {
	path     string
	branch   string
	headSHA  string
	behindBy int // commits behind origin/main
}

// Run checks for significant divergence between clones.
func (c *CloneDivergenceCheck) Run(ctx *CheckContext) *CheckResult {
	clones := c.findAllClones(ctx.TownRoot)
	if len(clones) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No clones found",
		}
	}

	// Gather info about each clone
	var infos []cloneInfo
	for _, path := range clones {
		info, err := c.getCloneInfo(path)
		if err != nil {
			continue // Skip problematic clones
		}
		infos = append(infos, info)
	}

	if len(infos) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No valid git clones found",
		}
	}

	// Check for clones significantly behind origin/main
	var warnings []string
	var errors []string

	for _, info := range infos {
		relPath := c.relativePath(ctx.TownRoot, info.path)

		// Only check clones on main branch (others are caught by BranchCheck)
		if info.branch != "main" && info.branch != "master" {
			continue
		}

		if info.behindBy > 50 {
			errors = append(errors, fmt.Sprintf("%s: %d commits behind origin/main (EMERGENCY)", relPath, info.behindBy))
		} else if info.behindBy > 10 {
			warnings = append(warnings, fmt.Sprintf("%s: %d commits behind origin/main", relPath, info.behindBy))
		}
	}

	if len(errors) > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("%d clone(s) critically diverged", len(errors)),
			Details: append(errors, warnings...),
			FixHint: "Run 'git pull --rebase' in affected directories",
		}
	}

	if len(warnings) > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d clone(s) behind origin/main", len(warnings)),
			Details: warnings,
			FixHint: "Run 'git pull --rebase' in affected directories",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("All %d clones in sync with origin/main", len(infos)),
	}
}

// findAllClones finds all git clones in the workspace.
func (c *CloneDivergenceCheck) findAllClones(townRoot string) []string {
	var clones []string

	entries, err := os.ReadDir(townRoot)
	if err != nil {
		return clones
	}

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || entry.Name() == "mayor" || entry.Name() == "docs" {
			continue
		}

		rigPath := filepath.Join(townRoot, entry.Name())

		// Check standard clone locations
		locations := []string{
			"mayor/rig",
			"witness/rig",
			"refinery/rig",
		}

		for _, loc := range locations {
			path := filepath.Join(rigPath, loc)
			if c.isGitRepo(path) {
				clones = append(clones, path)
			}
		}

		// Add crew members
		crewPath := filepath.Join(rigPath, "crew")
		if crewEntries, err := os.ReadDir(crewPath); err == nil {
			for _, crew := range crewEntries {
				if crew.IsDir() && !strings.HasPrefix(crew.Name(), ".") {
					path := filepath.Join(crewPath, crew.Name())
					if c.isGitRepo(path) {
						clones = append(clones, path)
					}
				}
			}
		}

		// Add polecats (handle both new and old structures)
		// New structure: polecats/<name>/<rigname>/
		// Old structure: polecats/<name>/
		rigName := entry.Name()
		polecatsPath := filepath.Join(rigPath, "polecats")
		if polecatEntries, err := os.ReadDir(polecatsPath); err == nil {
			for _, polecat := range polecatEntries {
				if polecat.IsDir() && !strings.HasPrefix(polecat.Name(), ".") {
					// Try new structure first
					path := filepath.Join(polecatsPath, polecat.Name(), rigName)
					if !c.isGitRepo(path) {
						// Fall back to old structure
						path = filepath.Join(polecatsPath, polecat.Name())
					}
					if c.isGitRepo(path) {
						clones = append(clones, path)
					}
				}
			}
		}
	}

	return clones
}

// isGitRepo checks if a directory is a git repository.
func (c *CloneDivergenceCheck) isGitRepo(path string) bool {
	gitDir := filepath.Join(path, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		return true
	}
	return false
}

// getCloneInfo gathers information about a clone.
func (c *CloneDivergenceCheck) getCloneInfo(path string) (cloneInfo, error) {
	info := cloneInfo{path: path}

	// Get current branch
	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = path
	out, err := cmd.Output()
	if err != nil {
		return info, err
	}
	info.branch = strings.TrimSpace(string(out))

	// Get HEAD SHA
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = path
	out, err = cmd.Output()
	if err != nil {
		return info, err
	}
	info.headSHA = strings.TrimSpace(string(out))

	// Count commits behind origin/main (uses existing refs, may be stale)
	cmd = exec.Command("git", "rev-list", "--count", "HEAD..origin/main")
	cmd.Dir = path
	out, err = cmd.Output()
	if err != nil {
		// origin/main might not exist, treat as 0 behind
		info.behindBy = 0
		return info, nil
	}

	var behind int
	_, _ = fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &behind)
	info.behindBy = behind

	return info, nil
}

// relativePath returns path relative to base.
func (c *CloneDivergenceCheck) relativePath(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}
