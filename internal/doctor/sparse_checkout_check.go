package doctor

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jonbaldie/gastown/internal/git"
)

// SparseCheckoutCheck detects legacy sparse checkout configurations that should be removed.
// Sparse checkout was previously used to exclude .claude/ from source repos, but this
// prevented valid .claude/ files in rigged repos from being used. Now that gastown's
// repo no longer has .claude/ files, sparse checkout is no longer needed.
//
// This check runs in both modes:
//   - With --rig: checks only the specified rig
//   - Without --rig: iterates over all rig directories in the town root
type SparseCheckoutCheck struct {
	FixableCheck
	townRoot      string
	affectedRepos []string // repos with legacy sparse checkout that should be removed
}

// NewSparseCheckoutCheck creates a new sparse checkout check.
func NewSparseCheckoutCheck() *SparseCheckoutCheck {
	return &SparseCheckoutCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "sparse-checkout",
				CheckDescription: "Check for legacy sparse checkout configuration that should be removed",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run checks if any git repos have legacy sparse checkout configured.
func (c *SparseCheckoutCheck) Run(ctx *CheckContext) *CheckResult {
	c.townRoot = ctx.TownRoot
	c.affectedRepos = nil

	// Collect rig paths to check
	var rigPaths []string
	if ctx.RigPath() != "" {
		// Single-rig mode
		rigPaths = []string{ctx.RigPath()}
	} else {
		// Town-wide mode: discover all rig directories
		rigPaths = c.discoverRigPaths(ctx.TownRoot)
	}

	if len(rigPaths) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No rigs found to check",
		}
	}

	// Check all rigs for legacy sparse checkout
	for _, rigPath := range rigPaths {
		c.checkRig(rigPath)
	}

	if len(c.affectedRepos) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No legacy sparse checkout configurations found",
		}
	}

	// Build details with relative paths from town root
	var details []string
	for _, repoPath := range c.affectedRepos {
		relPath, _ := filepath.Rel(c.townRoot, repoPath)
		if relPath == "" {
			relPath = repoPath
		}
		details = append(details, relPath)
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d repo(s) have legacy sparse checkout that should be removed", len(c.affectedRepos)),
		Details: details,
		FixHint: "Run 'gt doctor --fix' to remove sparse checkout and restore .claude/ files",
	}
}

// discoverRigPaths finds all rig directories in the town root.
// Skips known non-rig directories (mayor, deacon, daemon, .git, etc.).
func (c *SparseCheckoutCheck) discoverRigPaths(townRoot string) []string {
	entries, err := os.ReadDir(townRoot)
	if err != nil {
		return nil
	}

	var rigPaths []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Skip known non-rig directories
		if name == "mayor" || name == "deacon" || name == "daemon" ||
			name == ".git" || name == "docs" || name[0] == '.' {
			continue
		}
		// A rig directory has a config.json
		rigPath := filepath.Join(townRoot, name)
		if _, err := os.Stat(filepath.Join(rigPath, "config.json")); err == nil {
			rigPaths = append(rigPaths, rigPath)
		}
	}
	return rigPaths
}

// checkRig checks all worktree repos within a single rig for legacy sparse checkout.
func (c *SparseCheckoutCheck) checkRig(rigPath string) {
	repoPaths := []string{
		filepath.Join(rigPath, "mayor", "rig"),
		filepath.Join(rigPath, "refinery", "rig"),
	}

	// Add crew clones
	crewDir := filepath.Join(rigPath, "crew")
	if entries, err := os.ReadDir(crewDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() && entry.Name() != "README.md" {
				repoPaths = append(repoPaths, filepath.Join(crewDir, entry.Name()))
			}
		}
	}

	// Add polecat worktrees (nested structure: polecats/<name>/<rigname>/)
	polecatDir := filepath.Join(rigPath, "polecats")
	if entries, err := os.ReadDir(polecatDir); err == nil {
		rigName := filepath.Base(rigPath)
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			// The actual worktree is at polecats/<name>/<rigname>/
			worktreePath := filepath.Join(polecatDir, entry.Name(), rigName)
			if _, err := os.Stat(worktreePath); err == nil {
				repoPaths = append(repoPaths, worktreePath)
			} else {
				// Fallback: legacy flat layout polecats/<name>/
				repoPaths = append(repoPaths, filepath.Join(polecatDir, entry.Name()))
			}
		}
	}

	for _, repoPath := range repoPaths {
		// Skip if not a git repo
		if _, err := os.Stat(filepath.Join(repoPath, ".git")); os.IsNotExist(err) {
			continue
		}

		// Check if sparse checkout is configured (legacy configuration to remove)
		if git.IsSparseCheckoutConfigured(repoPath) {
			c.affectedRepos = append(c.affectedRepos, repoPath)
		}
	}
}

// Fix removes sparse checkout configuration from affected repos.
func (c *SparseCheckoutCheck) Fix(ctx *CheckContext) error {
	for _, repoPath := range c.affectedRepos {
		if err := git.RemoveSparseCheckout(repoPath); err != nil {
			relPath, _ := filepath.Rel(c.townRoot, repoPath)
			return fmt.Errorf("failed to remove sparse checkout for %s: %w", relPath, err)
		}
	}
	return nil
}
