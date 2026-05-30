package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// StaleBeadsRedirectCheck detects .beads directories that have both a redirect
// file AND stale data files. This can happen when:
// - A rig is added from a repo that already has .beads/ tracked in git
// - Crew workspaces are cloned from repos with existing .beads/ files
// - SetupRedirect failed or was run before cleanup logic was added
//
// Additionally, this check verifies redirect topology:
// - Worktrees (crew, polecats, refinery) should have redirects
// - Redirects should point to the correct canonical location
// - Redirect targets should exist
//
// When both redirect and data files exist, bd commands may use stale data
// instead of following the redirect.
type StaleBeadsRedirectCheck struct {
	FixableCheck
	staleLocations     []string        // Cached for Fix - dirs with stale files
	missingRedirects   []redirectIssue // Cached for Fix - worktrees missing redirects
	incorrectRedirects []redirectIssue // Cached for Fix - worktrees with wrong redirect target
}

// redirectIssue represents a missing or incorrect redirect.
type redirectIssue struct {
	worktreePath   string // Full path to the worktree (e.g., <rig>/crew/max)
	townRoot       string // Town root for SetupRedirect
	currentTarget  string // Current redirect target (empty if missing)
	expectedTarget string // Expected redirect target
}

// NewStaleBeadsRedirectCheck creates a new stale beads redirect check.
func NewStaleBeadsRedirectCheck() *StaleBeadsRedirectCheck {
	return &StaleBeadsRedirectCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "stale-beads-redirect",
				CheckDescription: "Check for stale files in .beads directories with redirects",
				CheckCategory:    CategoryCleanup,
			},
		},
	}
}

// staleFilePatterns are runtime files that should NOT exist alongside a redirect.
// These are gitignored runtime files that would conflict with redirected data.
// Note: config.yaml is NOT included because it may be tracked in git.
var staleFilePatterns = []string{
	// Dolt databases
	"*.db",
	"*.db-*",
	"*.db?*",
	// Legacy JSONL data files (stale in redirect locations)
	"interactions.jsonl",
	// Sync and metadata
	"metadata.json",
	"sync-state.json",
	"last-touched",
	".local_version",
	// Daemon runtime files
	"daemon.lock",
	"daemon.log",
	"daemon.pid",
	"bd.sock",
}

// Run checks for stale files in .beads directories that have redirects,
// and verifies redirect topology for all worktrees.
func (c *StaleBeadsRedirectCheck) Run(ctx *CheckContext) *CheckResult {
	var staleLocations []string
	var missingRedirects []redirectIssue
	var incorrectRedirects []redirectIssue

	// Get list of rigs to scan
	rigDirs, err := findRigDirs(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("Could not scan rigs: %v", err),
		}
	}

	// For each rig, check all potential .beads locations
	for _, rigDir := range rigDirs {
		locations := getBeadsDirsToCheck(rigDir)
		for _, beadsDir := range locations {
			if hasRedirectWithStaleFiles(beadsDir) {
				// Make path relative to town root for readability
				relPath, _ := filepath.Rel(ctx.TownRoot, beadsDir)
				if relPath == "" {
					relPath = beadsDir
				}
				staleLocations = append(staleLocations, relPath)
			}
		}

		// Verify redirect topology for this rig
		missing, incorrect := c.verifyRedirectTopology(ctx, rigDir)
		missingRedirects = append(missingRedirects, missing...)
		incorrectRedirects = append(incorrectRedirects, incorrect...)
	}

	// Cache for Fix
	c.staleLocations = staleLocations
	c.missingRedirects = missingRedirects
	c.incorrectRedirects = incorrectRedirects

	// Build result
	var details []string
	totalIssues := len(staleLocations) + len(missingRedirects) + len(incorrectRedirects)

	if totalIssues == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No stale beads files or redirect issues found",
		}
	}

	// Add details for each issue type
	for _, loc := range staleLocations {
		details = append(details, fmt.Sprintf("stale files: %s", loc))
	}
	for _, issue := range missingRedirects {
		relPath, _ := filepath.Rel(ctx.TownRoot, issue.worktreePath)
		details = append(details, fmt.Sprintf("missing redirect: %s", relPath))
	}
	for _, issue := range incorrectRedirects {
		relPath, _ := filepath.Rel(ctx.TownRoot, issue.worktreePath)
		details = append(details, fmt.Sprintf("incorrect redirect: %s (has %q, expected %q)",
			relPath, issue.currentTarget, issue.expectedTarget))
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d beads redirect issue(s) found", totalIssues),
		Details: details,
		FixHint: "Run 'gt doctor --fix' to repair redirects and remove stale files",
	}
}

// Fix removes stale files from .beads directories that have redirects,
// and creates/repairs missing or incorrect redirects.
func (c *StaleBeadsRedirectCheck) Fix(ctx *CheckContext) error {
	// Remove stale files
	for _, relPath := range c.staleLocations {
		beadsDir := filepath.Join(ctx.TownRoot, relPath)
		if err := cleanStaleBeadsFiles(beadsDir); err != nil {
			return fmt.Errorf("cleaning %s: %w", relPath, err)
		}
	}

	// Create missing redirects
	for _, issue := range c.missingRedirects {
		if err := beads.SetupRedirect(issue.townRoot, issue.worktreePath); err != nil {
			relPath, _ := filepath.Rel(ctx.TownRoot, issue.worktreePath)
			return fmt.Errorf("creating redirect for %s: %w", relPath, err)
		}
	}

	// Fix incorrect redirects (same as creating - SetupRedirect overwrites)
	for _, issue := range c.incorrectRedirects {
		if err := beads.SetupRedirect(issue.townRoot, issue.worktreePath); err != nil {
			relPath, _ := filepath.Rel(ctx.TownRoot, issue.worktreePath)
			return fmt.Errorf("fixing redirect for %s: %w", relPath, err)
		}
	}

	return nil
}

// findRigDirs returns all rig directories in the town.
func findRigDirs(townRoot string) ([]string, error) {
	var rigs []string

	entries, err := os.ReadDir(townRoot)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		// Skip hidden dirs, mayor, docs
		if strings.HasPrefix(name, ".") || name == "mayor" || name == "docs" {
			continue
		}

		rigPath := filepath.Join(townRoot, name)

		// A rig should have at least a .git directory (be a git repo)
		// or have a mayor/rig subdirectory
		if isLikelyRig(rigPath) {
			rigs = append(rigs, rigPath)
		}
	}

	return rigs, nil
}

// isLikelyRig checks if a directory looks like a rig.
func isLikelyRig(path string) bool {
	// Check for .git (it's a git repo)
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return true
	}
	// Check for mayor/rig (has the standard rig structure)
	if _, err := os.Stat(filepath.Join(path, "mayor", "rig")); err == nil {
		return true
	}
	// Check for .beads directory (has beads configured)
	if _, err := os.Stat(filepath.Join(path, ".beads")); err == nil {
		return true
	}
	return false
}

// getBeadsDirsToCheck returns all .beads directories to check for a rig.
func getBeadsDirsToCheck(rigDir string) []string {
	var dirs []string

	// Rig root .beads
	rigBeads := filepath.Join(rigDir, ".beads")
	if _, err := os.Stat(rigBeads); err == nil {
		dirs = append(dirs, rigBeads)
	}

	// Crew .beads directories: <rig>/crew/*/.beads
	crewDir := filepath.Join(rigDir, "crew")
	if entries, err := os.ReadDir(crewDir); err == nil {
		for _, entry := range entries {
			// Skip hidden directories (like .claude)
			if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
				beadsDir := filepath.Join(crewDir, entry.Name(), ".beads")
				if _, err := os.Stat(beadsDir); err == nil {
					dirs = append(dirs, beadsDir)
				}
			}
		}
	}

	// Refinery .beads: <rig>/refinery/rig/.beads
	refineryBeads := filepath.Join(rigDir, "refinery", "rig", ".beads")
	if _, err := os.Stat(refineryBeads); err == nil {
		dirs = append(dirs, refineryBeads)
	}

	// Polecats .beads directories: <rig>/polecats/*/.beads
	// Polecats may use nested structure: polecats/<name>/<rig_name>/.beads
	polecatsDir := filepath.Join(rigDir, "polecats")
	if entries, err := os.ReadDir(polecatsDir); err == nil {
		for _, entry := range entries {
			// Skip hidden directories
			if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
				clonePath := polecatClonePath(rigDir, entry.Name())
				beadsDir := filepath.Join(clonePath, ".beads")
				if _, err := os.Stat(beadsDir); err == nil {
					dirs = append(dirs, beadsDir)
				}
			}
		}
	}

	return dirs
}

// hasRedirectWithStaleFiles checks if a .beads directory has both a redirect
// file and stale data files.
func hasRedirectWithStaleFiles(beadsDir string) bool {
	// Must have redirect file
	redirectPath := filepath.Join(beadsDir, "redirect")
	if _, err := os.Stat(redirectPath); os.IsNotExist(err) {
		return false
	}

	// Check for any stale files
	for _, pattern := range staleFilePatterns {
		matches, err := filepath.Glob(filepath.Join(beadsDir, pattern))
		if err != nil {
			continue
		}
		if len(matches) > 0 {
			return true
		}
	}

	return false
}

// cleanStaleBeadsFiles removes stale files from a .beads directory,
// preserving the redirect file and .gitignore.
func cleanStaleBeadsFiles(beadsDir string) error {
	// Verify redirect exists before cleaning
	redirectPath := filepath.Join(beadsDir, "redirect")
	if _, err := os.Stat(redirectPath); os.IsNotExist(err) {
		return fmt.Errorf("no redirect file found - refusing to clean")
	}

	// Preserve redirect-local metadata only when it agrees with the redirect
	// target. If it points at a different DB, it is stale drift and must be
	// removed so bd follows the canonical target metadata.
	preserveMetadata := shouldPreserveRedirectMetadata(beadsDir)

	// Remove files matching stale patterns
	for _, pattern := range staleFilePatterns {
		matches, err := filepath.Glob(filepath.Join(beadsDir, pattern))
		if err != nil {
			continue
		}
		for _, match := range matches {
			if preserveMetadata && filepath.Base(match) == "metadata.json" {
				continue
			}
			if err := os.RemoveAll(match); err != nil {
				return fmt.Errorf("removing %s: %w", filepath.Base(match), err)
			}
		}
	}

	// Also remove mq directory if it exists
	mqDir := filepath.Join(beadsDir, "mq")
	if _, err := os.Stat(mqDir); err == nil {
		if err := os.RemoveAll(mqDir); err != nil {
			return fmt.Errorf("removing mq: %w", err)
		}
	}

	return nil
}

// metadataDoltDatabase returns the metadata.json dolt_database value, if any.
func metadataDoltDatabase(beadsDir string) string {
	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		return ""
	}
	var meta struct {
		DoltDatabase string `json:"dolt_database"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return ""
	}
	return strings.TrimSpace(meta.DoltDatabase)
}

func shouldPreserveRedirectMetadata(beadsDir string) bool {
	db := metadataDoltDatabase(beadsDir)
	if db == "" {
		return false
	}

	targetDir := redirectTargetDir(beadsDir)
	if targetDir == "" {
		return true
	}
	targetDB := metadataDoltDatabase(targetDir)
	return targetDB == "" || targetDB == db
}

func redirectTargetDir(beadsDir string) string {
	data, err := os.ReadFile(filepath.Join(beadsDir, "redirect"))
	if err != nil {
		return ""
	}
	target := strings.TrimSpace(string(data))
	if target == "" {
		return ""
	}
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(beadsDir), target))
}

// verifyRedirectTopology checks that all worktrees in a rig have correct redirects.
// Returns lists of missing and incorrect redirect issues.
func (c *StaleBeadsRedirectCheck) verifyRedirectTopology(ctx *CheckContext, rigDir string) (missing, incorrect []redirectIssue) {
	townRoot := ctx.TownRoot

	// Check if rig has beads configured at all
	rigBeadsPath := filepath.Join(rigDir, ".beads")
	mayorBeadsPath := filepath.Join(rigDir, "mayor", "rig", ".beads")

	// If neither location has beads, skip this rig (not configured)
	if !dirExists(rigBeadsPath) && !dirExists(mayorBeadsPath) {
		return nil, nil
	}

	// Get all worktrees that should have redirects
	worktrees := getWorktreePaths(rigDir)

	for _, worktreePath := range worktrees {
		// Skip if worktree doesn't exist
		if !dirExists(worktreePath) {
			continue
		}

		// Use the canonical ComputeRedirectTarget function from beads package
		// This ensures doctor check and SetupRedirect stay in sync
		expected, err := beads.ComputeRedirectTarget(townRoot, worktreePath)
		if err != nil {
			// "no beads found" is expected when beads isn't configured for this rig.
			// Other errors may indicate real problems - log them in verbose mode.
			if ctx.Verbose && !strings.Contains(err.Error(), "no beads found") {
				relPath, _ := filepath.Rel(townRoot, worktreePath)
				fmt.Printf("  [verbose] skipping %s: %v\n", relPath, err)
			}
			continue
		}
		actual := readRedirectTarget(worktreePath)

		if actual == "" {
			// Missing redirect
			missing = append(missing, redirectIssue{
				worktreePath:   worktreePath,
				townRoot:       townRoot,
				expectedTarget: expected,
			})
		} else if normalizeRedirectPath(actual) != normalizeRedirectPath(expected) {
			// Redirect exists but doesn't match expected
			incorrect = append(incorrect, redirectIssue{
				worktreePath:   worktreePath,
				townRoot:       townRoot,
				currentTarget:  actual,
				expectedTarget: expected,
			})
		}
	}

	return missing, incorrect
}

// getWorktreePaths returns all worktree paths that should have redirects.
func getWorktreePaths(rigDir string) []string {
	var paths []string

	// Crew workspaces: <rig>/crew/*
	crewDir := filepath.Join(rigDir, "crew")
	if entries, err := os.ReadDir(crewDir); err == nil {
		for _, entry := range entries {
			name := entry.Name()
			// Skip hidden directories (like .claude)
			if entry.IsDir() && !strings.HasPrefix(name, ".") {
				paths = append(paths, filepath.Join(crewDir, name))
			}
		}
	}

	// Polecats: <rig>/polecats/*
	// Polecats may use nested structure: polecats/<name>/<rig_name>/
	// where <rig_name>/ is the actual git worktree (clone path).
	polecatsDir := filepath.Join(rigDir, "polecats")
	if entries, err := os.ReadDir(polecatsDir); err == nil {
		for _, entry := range entries {
			name := entry.Name()
			// Skip hidden directories
			if entry.IsDir() && !strings.HasPrefix(name, ".") {
				paths = append(paths, polecatClonePath(rigDir, name))
			}
		}
	}

	// Refinery: <rig>/refinery/rig
	refineryPath := filepath.Join(rigDir, "refinery", "rig")
	if dirExists(refineryPath) {
		paths = append(paths, refineryPath)
	}

	return paths
}

// polecatClonePath returns the actual git worktree path for a polecat.
// Mirrors the logic in polecat/manager.go:clonePath() to handle both:
//   - New nested structure: polecats/<name>/<rig_name>/ (gives LLMs repo context)
//   - Old flat structure: polecats/<name>/ (backward compat)
func polecatClonePath(rigDir, polecatName string) string {
	rigName := filepath.Base(rigDir)
	polecatDir := filepath.Join(rigDir, "polecats", polecatName)

	// New structure: polecats/<name>/<rig_name>/
	nestedPath := filepath.Join(polecatDir, rigName)
	if dirExists(nestedPath) {
		return nestedPath
	}

	// Old structure: polecats/<name>/ (check for .git to confirm it's a worktree)
	if _, err := os.Stat(filepath.Join(polecatDir, ".git")); err == nil {
		return polecatDir
	}

	// Default to new structure (consistent with manager.go)
	return nestedPath
}

// readRedirectTarget reads the redirect target from a worktree's .beads/redirect file.
// Returns empty string if no redirect exists.
func readRedirectTarget(worktreePath string) string {
	redirectPath := filepath.Join(worktreePath, ".beads", "redirect")
	data, err := os.ReadFile(redirectPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// normalizeRedirectPath normalizes a redirect path for comparison.
func normalizeRedirectPath(path string) string {
	// Remove trailing newlines/spaces and clean the path
	path = strings.TrimSpace(path)
	// Normalize slashes
	path = filepath.ToSlash(path)
	// Remove trailing slash
	path = strings.TrimSuffix(path, "/")
	return path
}
