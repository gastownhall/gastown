// Package beads provides redirect resolution for beads databases.
package beads

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ResolveBeadsDir returns the actual beads directory, following any redirect.
// If workDir/.beads/redirect exists, it reads the redirect path and resolves it
// relative to workDir (not the .beads directory). Otherwise, returns workDir/.beads.
//
// This is essential for crew workers and polecats that use shared beads via redirect.
// The redirect file contains a relative path like "../../mayor/rig/.beads".
//
// Example: if we're at crew/max/ and .beads/redirect contains "../../mayor/rig/.beads",
// the redirect is resolved from crew/max/ (not crew/max/.beads/), giving us
// mayor/rig/.beads at the rig root level.
//
// Circular redirect detection: If the resolved path equals the original beads directory,
// this indicates an errant redirect file that should be removed. The function logs a
// warning and returns the original beads directory.
func ResolveBeadsDir(workDir string) string {
	if filepath.Base(workDir) == ".beads" {
		workDir = filepath.Dir(workDir)
	}
	beadsDir := filepath.Join(workDir, ".beads")
	redirectPath := filepath.Join(beadsDir, "redirect")

	// Check for redirect file
	data, err := os.ReadFile(redirectPath) //nolint:gosec // G304: path is constructed internally
	if err != nil {
		// No redirect, use local .beads
		return beadsDir
	}

	// Read and clean the redirect path
	redirectTarget := strings.TrimSpace(string(data))
	if redirectTarget == "" {
		return beadsDir
	}

	// Resolve redirect target. Absolute paths are used as-is;
	// relative paths are resolved from workDir.
	var resolved string
	if filepath.IsAbs(redirectTarget) {
		resolved = filepath.Clean(redirectTarget)
	} else {
		resolved = filepath.Clean(filepath.Join(workDir, redirectTarget))
	}

	// Detect circular redirects: if resolved path equals original beads dir,
	// this is an errant redirect file (e.g., redirect in mayor/rig/.beads pointing to itself)
	if resolved == beadsDir {
		fmt.Fprintf(os.Stderr, "Warning: circular redirect detected in %s (points to itself), ignoring redirect\n", redirectPath)
		// Remove the errant redirect file to prevent future warnings
		if err := os.Remove(redirectPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not remove errant redirect file: %v\n", err)
		}
		return beadsDir
	}

	// Follow redirect chains (e.g., crew/.beads -> rig/.beads -> mayor/rig/.beads)
	// This is intentional for the rig-level redirect architecture.
	// Limit depth to prevent infinite loops from misconfigured redirects.
	return resolveBeadsDirWithDepth(resolved, 3)
}

// resolveBeadsDirWithDepth follows redirect chains with a depth limit.
func resolveBeadsDirWithDepth(beadsDir string, maxDepth int) string {
	if maxDepth <= 0 {
		fmt.Fprintf(os.Stderr, "Warning: redirect chain too deep at %s, stopping\n", beadsDir)
		return beadsDir
	}

	redirectPath := filepath.Join(beadsDir, "redirect")
	data, err := os.ReadFile(redirectPath) //nolint:gosec // G304: path is constructed internally
	if err != nil {
		// No redirect, this is the final destination
		return beadsDir
	}

	redirectTarget := strings.TrimSpace(string(data))
	if redirectTarget == "" {
		return beadsDir
	}

	// Resolve redirect target. Absolute paths are used as-is;
	// relative paths are resolved from parent of beadsDir.
	workDir := filepath.Dir(beadsDir)
	var resolved string
	if filepath.IsAbs(redirectTarget) {
		resolved = filepath.Clean(redirectTarget)
	} else {
		resolved = filepath.Clean(filepath.Join(workDir, redirectTarget))
	}

	// Detect circular redirect
	if resolved == beadsDir {
		fmt.Fprintf(os.Stderr, "Warning: circular redirect detected in %s, stopping\n", redirectPath)
		return beadsDir
	}

	// Recursively follow
	return resolveBeadsDirWithDepth(resolved, maxDepth-1)
}

// cleanBeadsRuntimeFiles removes redirect-local runtime and identity files from
// a worktree .beads directory while preserving non-identity docs/config such as
// formulas/, README.md, and .gitignore. Tracked identity files are hidden from
// the worktree index before removal so managed worktrees stay clean.
// This is safe to call even if the directory doesn't exist.
func cleanBeadsRuntimeFiles(worktreePath, beadsDir string) error {
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return nil // Nothing to clean
	}

	// Runtime files/patterns and redirect-local identity files that are unsafe to
	// keep next to a redirect. metadata.json/config.yaml can cause bd to bind to
	// the worktree .beads before following the redirect.
	runtimePatterns := []string{
		// Redirect-local identity/config
		"metadata.json", "config.yaml",
		// Daemon runtime
		"daemon.lock", "daemon.log", "daemon.pid", "bd.sock",
		// Sync state
		"last-touched",
		// Version tracking
		".local_version",
		// Redirect file (we're about to recreate it)
		"redirect",
		// Runtime directories
		"mq",
	}

	var firstErr error
	for _, pattern := range runtimePatterns {
		matches, err := filepath.Glob(filepath.Join(beadsDir, pattern))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, match := range matches {
			if filepath.Base(match) == "metadata.json" || filepath.Base(match) == "config.yaml" {
				if err := hideTrackedWorktreePath(worktreePath, match); err != nil && firstErr == nil {
					firstErr = err
					continue
				}
			}
			if err := os.RemoveAll(match); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}

	return firstErr
}

func hideTrackedWorktreePath(worktreePath, path string) error {
	rel, err := filepath.Rel(worktreePath, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	rel = filepath.ToSlash(rel)
	if err := exec.Command("git", "-C", worktreePath, "ls-files", "--error-unmatch", "--", rel).Run(); err != nil {
		return nil
	}
	output, err := exec.Command("git", "-C", worktreePath, "update-index", "--skip-worktree", "--", rel).CombinedOutput()
	if err != nil {
		return fmt.Errorf("marking %s skip-worktree: %s: %w", rel, strings.TrimSpace(string(output)), err)
	}
	return nil
}

// ComputeRedirectTarget computes the expected redirect target for a worktree.
// This is the canonical function for determining what a redirect should contain.
// Both SetupRedirect and doctor checks should use this to stay in sync.
//
// Parameters:
//   - townRoot: the town root directory (e.g., ~/gt)
//   - worktreePath: the worktree directory (e.g., <rig>/crew/<name> or <rig>/refinery/rig)
//
// Returns the redirect target path (e.g., "../../.beads" or "../../mayor/rig/.beads"),
// or an error if the path is invalid or no beads location exists.
func ComputeRedirectTarget(townRoot, worktreePath string) (string, error) {
	townRootAbs, err := filepath.Abs(townRoot)
	if err != nil {
		return "", fmt.Errorf("resolving town root: %w", err)
	}
	worktreeAbs, err := filepath.Abs(worktreePath)
	if err != nil {
		return "", fmt.Errorf("resolving worktree path: %w", err)
	}
	if worktreeAbs == townRootAbs {
		return "", fmt.Errorf("cannot create redirect at town root")
	}
	if rel, err := filepath.Rel(townRootAbs, worktreeAbs); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("worktree path %s is outside town root %s", worktreePath, townRoot)
	}

	// Get rig root from worktree path
	// worktreePath = <town>/<rig>/crew/<name> or <town>/<rig>/refinery/rig etc.
	relPath, err := filepath.Rel(townRootAbs, worktreeAbs)
	if err != nil {
		return "", fmt.Errorf("computing relative path: %w", err)
	}
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid worktree path: must be at least 2 levels deep from town root")
	}

	// Safety check: prevent creating redirect in canonical beads location (mayor/rig).
	// This would create a circular redirect chain since rig/.beads redirects to mayor/rig/.beads.
	// Check both parts[0] (worktree IS the mayor dir, e.g., <town>/mayor/rig) and
	// parts[1] (worktree is inside a rig's mayor, e.g., <town>/<rig>/mayor/rig).
	if parts[0] == "mayor" || (len(parts) >= 2 && parts[1] == "mayor") {
		return "", fmt.Errorf("cannot create redirect in canonical beads location (mayor/rig)")
	}

	rigName := parts[0]
	rigRoot := filepath.Join(townRootAbs, rigName)
	townBeadsPath := filepath.Join(townRootAbs, ".beads")
	rigBeadsPath := filepath.Join(rigRoot, ".beads")
	mayorBeadsPath := filepath.Join(rigRoot, "mayor", "rig", ".beads")

	// Check rig-level .beads first: if the rig has its own database
	// (metadata.json with dolt_database), crew must use rig-level beads
	// so they see the correct prefix (e.g., lc- for laneassist, not hq-).
	// If the rig-level .beads is itself a redirect, flatten it here: bd does
	// not support redirect chains and will ignore the worktree redirect.
	if rigHasOwnDB(rigBeadsPath) {
		depth := len(parts) - 1
		upPath := strings.Repeat("../", depth)
		if redirectPath, ok := directRigRedirectTarget(upPath, filepath.Join(rigBeadsPath, "redirect")); ok {
			return redirectPath, nil
		}
		return upPath + ".beads", nil
	}

	// Rig has no own database — try town-level .beads (has routes.jsonl,
	// config.yaml, Dolt server info, and hq- prefix).
	townBeadsHasDB := false
	if info, err := os.Stat(townBeadsPath); err == nil && info.IsDir() {
		if _, err := os.Stat(filepath.Join(townBeadsPath, "dolt")); err == nil {
			townBeadsHasDB = true
		} else if _, err := os.Stat(filepath.Join(townBeadsPath, "config.yaml")); err == nil {
			townBeadsHasDB = true
		}
	}

	// Only use town-level beads if the rig doesn't have its own redirect chain.
	// Rigs using Dolt server (not embedded DB) have a .beads/redirect file pointing
	// to mayor/rig/.beads — this must take priority over the town fallback.
	rigHasRedirect := false
	if _, err := os.Stat(filepath.Join(rigBeadsPath, "redirect")); err == nil {
		rigHasRedirect = true
	}

	if townBeadsHasDB && !rigHasRedirect {
		depth := len(parts)
		upPath := strings.Repeat("../", depth)
		return upPath + ".beads", nil
	}

	// Neither rig nor town has a database — fall back to rig-level beads.
	usesMayorFallback := false
	rigBeadsExists := false
	if _, err := os.Stat(rigBeadsPath); err == nil {
		rigBeadsExists = true
	}
	rigHasDB := false
	if rigBeadsExists {
		// Check for actual database: dolt/ directory
		if _, err := os.Stat(filepath.Join(rigBeadsPath, "dolt")); err == nil {
			rigHasDB = true
		} else if _, err := os.Stat(filepath.Join(rigBeadsPath, "redirect")); err == nil {
			// A redirect file is a valid beads configuration (tracked beads case).
			// initBeads creates this to point to mayor/rig/.beads.
			rigHasDB = true
		}
	}

	if !rigBeadsExists || !rigHasDB {
		// Rig .beads doesn't exist or has no database — check mayor/rig/.beads
		if _, err := os.Stat(mayorBeadsPath); os.IsNotExist(err) {
			if !rigBeadsExists {
				return "", fmt.Errorf("no beads found at %s, %s, or %s", townBeadsPath, rigBeadsPath, mayorBeadsPath)
			}
			// Rig .beads exists but has no DB and mayor path doesn't exist either.
			// Fall through to use rig path (best effort).
		} else {
			usesMayorFallback = true
		}
	}

	// Compute relative path from worktree to rig root
	// e.g., crew/<name> (depth 2) -> ../../.beads
	//       refinery/rig (depth 2) -> ../../.beads
	depth := len(parts) - 1 // subtract 1 for rig name itself
	upPath := strings.Repeat("../", depth)

	var redirectPath string
	if usesMayorFallback {
		// Direct redirect to mayor/rig/.beads since rig/.beads doesn't exist
		redirectPath = upPath + "mayor/rig/.beads"
	} else {
		redirectPath = upPath + ".beads"

		// Check if rig-level beads has a redirect (tracked beads case).
		// If so, redirect directly to the final destination to avoid chains.
		// The bd CLI doesn't support redirect chains, so we must skip intermediate hops.
		if target, ok := directRigRedirectTarget(upPath, filepath.Join(rigBeadsPath, "redirect")); ok {
			redirectPath = target
		}
	}

	return redirectPath, nil
}

func directRigRedirectTarget(upPath, rigRedirectPath string) (string, bool) {
	data, err := os.ReadFile(rigRedirectPath) //nolint:gosec // G304: path is constructed internally
	if err != nil {
		return "", false
	}
	rigRedirectTarget := strings.TrimSpace(string(data))
	if rigRedirectTarget == "" {
		return "", false
	}
	if filepath.IsAbs(rigRedirectTarget) {
		// Absolute redirect — pass through as-is (ResolveBeadsDir handles it).
		return rigRedirectTarget, true
	}
	// Relative redirect (e.g., "mayor/rig/.beads" for tracked beads).
	// Redirect worktree directly to the final destination.
	return upPath + rigRedirectTarget, true
}

// SetupRedirect creates a .beads/redirect file for a worktree to point to the rig's shared beads.
// This is used by crew, polecats, and refinery worktrees to share the rig's beads database.
//
// Parameters:
//   - townRoot: the town root directory (e.g., ~/gt)
//   - worktreePath: the worktree directory (e.g., <rig>/crew/<name> or <rig>/refinery/rig)
//
// The function:
//  1. Computes the relative path from worktree to rig-level .beads
//  2. Cleans up runtime files (preserving tracked files like formulas/)
//  3. Creates the redirect file
//
// Safety: This function refuses to create redirects in the canonical beads location
// (mayor/rig) to prevent circular redirect chains.
func SetupRedirect(townRoot, worktreePath string) error {
	redirectPath, err := ComputeRedirectTarget(townRoot, worktreePath)
	if err != nil {
		return err
	}

	// Warn only when using mayor fallback WITHOUT a redirect file.
	// When rig/.beads/redirect exists pointing to mayor/rig/.beads, that's the
	// intended configuration for tracked beads — not a fallback worth warning about.
	if strings.Contains(redirectPath, "mayor/rig/.beads") {
		relPath, _ := filepath.Rel(townRoot, worktreePath)
		parts := strings.Split(filepath.ToSlash(relPath), "/")
		rigRoot := filepath.Join(townRoot, parts[0])
		rigRedirectPath := filepath.Join(rigRoot, ".beads", "redirect")
		if _, err := os.Stat(rigRedirectPath); os.IsNotExist(err) {
			// No redirect file — this is an unexpected fallback
			rigBeadsPath := filepath.Join(rigRoot, ".beads")
			mayorBeadsPath := filepath.Join(rigRoot, "mayor", "rig", ".beads")
			fmt.Fprintf(os.Stderr, "Warning: rig .beads not found at %s, using %s\n", rigBeadsPath, mayorBeadsPath)
			fmt.Fprintf(os.Stderr, "  Run 'bd doctor' to fix rig beads configuration\n")
		}
	}

	// Clean up runtime/identity files in .beads/ but preserve non-identity tracked
	// files (formulas/, README.md, etc.).
	worktreeBeadsDir := filepath.Join(worktreePath, ".beads")

	// Handle edge cases: if .beads exists as a file or symlink, remove the path
	// itself before cleanup. Using Lstat prevents cleanup from traversing a
	// symlinked .beads and mutating its target.
	// This can happen with stale state from previous failed operations or
	// unusual clone state. MkdirAll would fail with "file exists" in this case.
	if info, err := os.Lstat(worktreeBeadsDir); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		if err := hideTrackedWorktreePath(worktreePath, worktreeBeadsDir); err != nil {
			return fmt.Errorf("hiding stale .beads path: %w", err)
		}
		if err := os.Remove(worktreeBeadsDir); err != nil {
			return fmt.Errorf("removing stale .beads path: %w", err)
		}
	}

	if err := cleanBeadsRuntimeFiles(worktreePath, worktreeBeadsDir); err != nil {
		return fmt.Errorf("cleaning runtime files: %w", err)
	}

	// Create .beads directory if it doesn't exist
	if err := os.MkdirAll(worktreeBeadsDir, 0755); err != nil {
		return fmt.Errorf("creating .beads dir: %w", err)
	}

	// Create redirect file
	redirectFile := filepath.Join(worktreeBeadsDir, "redirect")
	if err := os.WriteFile(redirectFile, []byte(redirectPath+"\n"), 0644); err != nil {
		return fmt.Errorf("creating redirect file: %w", err)
	}

	return nil
}

// IsLocalBeadsDir returns true if resolvedPath is the cwd's own .beads/ directory
// (i.e., no redirect was followed). This indicates the beads client will write to
// a local database that other agents (e.g., the Refinery) will never read.
func IsLocalBeadsDir(cwd, resolvedPath string) bool {
	localBeads := filepath.Join(cwd, ".beads")
	cleanResolved, _ := filepath.Abs(resolvedPath)
	cleanLocal, _ := filepath.Abs(localBeads)
	return cleanResolved == cleanLocal
}

// rigHasOwnDB checks if a rig's .beads/metadata.json declares its own
// dolt_database. Rigs with their own database (e.g., laneassist with "lc-"
// prefix) must not be redirected to town-level beads ("hq-" prefix).
func rigHasOwnDB(rigBeadsPath string) bool {
	metadataPath := filepath.Join(rigBeadsPath, "metadata.json")
	data, err := os.ReadFile(metadataPath) //nolint:gosec // G304: trusted beads path
	if err != nil {
		return false
	}
	var meta struct {
		DoltDatabase string `json:"dolt_database"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return false
	}
	return meta.DoltDatabase != ""
}
