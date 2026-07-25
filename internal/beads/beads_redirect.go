// Package beads provides redirect resolution for beads databases.
package beads

import (
	"encoding/json"
	"fmt"
	"os"
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

// cleanBeadsRuntimeFiles removes gitignored runtime files from a .beads directory
// while preserving tracked files (formulas/, README.md, config.yaml, .gitignore).
// This is safe to call even if the directory doesn't exist.
func cleanBeadsRuntimeFiles(beadsDir string) error {
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return nil // Nothing to clean
	}

	// Runtime files/patterns that are gitignored and safe to remove
	runtimePatterns := []string{
		// Daemon runtime
		"daemon.lock", "daemon.log", "daemon.pid", "bd.sock",
		// Sync state
		"last-touched", "metadata.json",
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
			if err := os.RemoveAll(match); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}

	return firstErr
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
	// Get rig root from worktree path
	// worktreePath = <town>/<rig>/crew/<name> or <town>/<rig>/refinery/rig etc.
	relPath, err := filepath.Rel(townRoot, worktreePath)
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
	rigRoot := filepath.Join(townRoot, rigName)
	townBeadsPath := filepath.Join(townRoot, ".beads")
	rigBeadsPath := filepath.Join(rigRoot, ".beads")
	mayorBeadsPath := filepath.Join(rigRoot, "mayor", "rig", ".beads")

	// Check rig-level .beads first: if the rig has its own database
	// (metadata.json with dolt_database), crew must use rig-level beads
	// so they see the correct prefix (e.g., lc- for laneassist, not hq-).
	if rigHasOwnDB(rigBeadsPath) {
		depth := len(parts) - 1
		upPath := strings.Repeat("../", depth)
		// Collapse the chain: rig/.beads may itself be a redirect to
		// mayor/rig/.beads, and bd stops at the first hop (see
		// collapseRigRedirect).
		return collapseRigRedirect(rigBeadsPath, upPath), nil
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
		// Check if rig-level beads has a redirect (tracked beads case).
		// If so, redirect directly to the final destination to avoid chains.
		redirectPath = collapseRigRedirect(rigBeadsPath, upPath)
	}

	return redirectPath, nil
}

// collapseRigRedirect returns the redirect target a worktree should use to reach
// the rig's beads database in ONE hop, given upPath — the "../" prefix that
// walks from the worktree up to the rig root.
//
// The bd CLI does not follow redirect chains: it prints "redirect chains not
// allowed, ignoring redirect in <dir>" and stops at the intermediate directory,
// leaving lookups pointed at the wrong database (or none). So when
// <rig>/.beads is itself just a redirect — the tracked-beads layout, where the
// real database lives under mayor/rig/.beads — skip the intermediate hop and
// point straight at the final destination.
//
// Missing the collapse in the rig-has-own-DB path is what made bd fail with
// "issue not found" when run from a polecat worktree in fastlio2_ros2 and
// navigation_server, the two rigs whose .beads has both metadata.json and a
// redirect (hq-szhze).
func collapseRigRedirect(rigBeadsPath, upPath string) string {
	data, err := os.ReadFile(filepath.Join(rigBeadsPath, "redirect")) //nolint:gosec // G304: trusted beads path
	if err != nil {
		return upPath + ".beads"
	}
	target := strings.TrimSpace(string(data))
	if target == "" {
		return upPath + ".beads"
	}
	if filepath.IsAbs(target) {
		// Absolute redirect — pass through as-is (ResolveBeadsDir handles it).
		return target
	}
	// Relative redirect (e.g., "mayor/rig/.beads") — resolved from the rig root,
	// so re-anchor it to the worktree.
	return upPath + target
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

	// Clean up runtime files in .beads/ but preserve tracked files (formulas/, README.md, etc.)
	worktreeBeadsDir := filepath.Join(worktreePath, ".beads")

	// Handle edge case: if .beads exists as a file (not directory), remove it.
	// This can happen with stale state from previous failed operations or
	// unusual clone state. MkdirAll would fail with "file exists" in this case.
	if info, err := os.Stat(worktreeBeadsDir); err == nil && !info.IsDir() {
		if err := os.Remove(worktreeBeadsDir); err != nil {
			return fmt.Errorf("removing stale .beads file: %w", err)
		}
	}

	if err := cleanBeadsRuntimeFiles(worktreeBeadsDir); err != nil {
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
