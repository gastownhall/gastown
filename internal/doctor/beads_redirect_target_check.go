package doctor

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/beads"
)

// BeadsRedirectTargetCheck validates that .beads/redirect files in crew/polecat/refinery
// worktrees point to targets that actually exist and have a working beads setup.
//
// This catches setup issues when cloning to a new machine where redirects might
// reference paths that don't exist yet (e.g., canonical beads location not initialized).
type BeadsRedirectTargetCheck struct {
	FixableCheck
	brokenTargets []brokenTarget // Cached for Fix
}

// brokenTarget represents a redirect whose target is missing or broken.
type brokenTarget struct {
	worktreePath string // Full path to the worktree
	target       string // Raw content of the redirect file
	resolvedPath string // Resolved absolute path of the target
	reason       string // Why the target is broken
}

// NewBeadsRedirectTargetCheck creates a new beads redirect target check.
func NewBeadsRedirectTargetCheck() *BeadsRedirectTargetCheck {
	return &BeadsRedirectTargetCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "beads-redirect-target",
				CheckDescription: "Check that beads redirect targets exist and are accessible",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run checks all worktree redirect files to verify their targets exist and are valid.
func (c *BeadsRedirectTargetCheck) Run(ctx *CheckContext) *CheckResult {
	var broken []brokenTarget

	rigDirs, err := findRigDirs(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("Could not scan rigs: %v", err),
		}
	}

	for _, rigDir := range rigDirs {
		worktrees := getWorktreePaths(rigDir)
		for _, wt := range worktrees {
			if !dirExists(wt) {
				continue
			}

			redirectFile := filepath.Join(wt, ".beads", "redirect")
			data, err := os.ReadFile(redirectFile)
			if err != nil {
				// No redirect file — not our concern (StaleBeadsRedirectCheck handles missing redirects)
				continue
			}

			target := strings.TrimSpace(string(data))
			if target == "" {
				continue
			}

			// Resolve the target path relative to the worktree.
			// Absolute paths are used as-is (same logic as beads.ResolveBeadsDir).
			var resolved string
			if filepath.IsAbs(target) {
				resolved = filepath.Clean(target)
			} else {
				resolved = filepath.Clean(filepath.Join(wt, target))
			}

			// Check 1: Does the target directory exist?
			info, err := os.Stat(resolved)
			if os.IsNotExist(err) {
				relWt, _ := filepath.Rel(ctx.TownRoot, wt)
				broken = append(broken, brokenTarget{
					worktreePath: wt,
					target:       target,
					resolvedPath: resolved,
					reason:       fmt.Sprintf("target does not exist: %s", relWt),
				})
				continue
			}
			if err != nil {
				relWt, _ := filepath.Rel(ctx.TownRoot, wt)
				broken = append(broken, brokenTarget{
					worktreePath: wt,
					target:       target,
					resolvedPath: resolved,
					reason:       fmt.Sprintf("target not accessible: %s (%v)", relWt, err),
				})
				continue
			}

			// Check 2: Is it a directory?
			if !info.IsDir() {
				relWt, _ := filepath.Rel(ctx.TownRoot, wt)
				broken = append(broken, brokenTarget{
					worktreePath: wt,
					target:       target,
					resolvedPath: resolved,
					reason:       fmt.Sprintf("target is not a directory: %s", relWt),
				})
				continue
			}

			// Check 3: Does the target have a working beads setup?
			// A valid beads directory should have at least one of: dolt/, redirect, config.yaml
			if !hasBeadsSetup(resolved) {
				relWt, _ := filepath.Rel(ctx.TownRoot, wt)
				broken = append(broken, brokenTarget{
					worktreePath: wt,
					target:       target,
					resolvedPath: resolved,
					reason:       fmt.Sprintf("target has no beads setup: %s", relWt),
				})
			}
		}
	}

	c.brokenTargets = broken

	if len(broken) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "All beads redirect targets are valid",
		}
	}

	var details []string
	for _, bt := range broken {
		relWt, _ := filepath.Rel(ctx.TownRoot, bt.worktreePath)
		relTarget, _ := filepath.Rel(ctx.TownRoot, bt.resolvedPath)
		details = append(details, fmt.Sprintf("%s → %s (%s)", relWt, relTarget, bt.reason))
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d broken redirect target(s)", len(broken)),
		Details: details,
		FixHint: "Run 'gt doctor --fix' to repair redirects, or 'bd init' to initialize beads",
	}
}

// Fix attempts to repair broken redirect targets by recomputing redirects.
// If the canonical beads location exists, SetupRedirect will point to it.
// If a target directory exists but is missing config.yaml, it is created
// from metadata.json defaults (this handles partial rig setups where the
// dolt database was initialized but config.yaml was not written).
// If no canonical location exists, the fix cannot proceed automatically.
func (c *BeadsRedirectTargetCheck) Fix(ctx *CheckContext) error {
	var unfixable []string

	for _, bt := range c.brokenTargets {
		// If the redirect target directory exists but lacks beads setup,
		// try to create config.yaml from metadata.json before giving up.
		// This handles the case where gt rig add partially completed: the
		// dolt database and metadata.json exist but config.yaml was never
		// written (e.g., due to a crash or interrupted setup).
		if strings.Contains(bt.reason, "no beads setup") && dirExists(bt.resolvedPath) {
			rigName := extractRigName(ctx.TownRoot, bt.worktreePath)
			if err := beads.EnsureConfigYAMLFromMetadataIfMissing(bt.resolvedPath, rigName); err != nil {
				log.Printf("[doctor] beads-redirect-target: could not create config.yaml from metadata in %s: %v", bt.resolvedPath, err)
			} else if hasBeadsSetup(bt.resolvedPath) {
				continue // Fixed — config.yaml created successfully
			}
		}

		// Check if the rig's canonical beads location exists
		relPath, err := filepath.Rel(ctx.TownRoot, bt.worktreePath)
		if err != nil {
			unfixable = append(unfixable, bt.worktreePath)
			continue
		}
		parts := strings.Split(filepath.ToSlash(relPath), "/")
		if len(parts) < 2 {
			unfixable = append(unfixable, bt.worktreePath)
			continue
		}

		rigRoot := filepath.Join(ctx.TownRoot, parts[0])
		rigBeads := filepath.Join(rigRoot, ".beads")
		mayorBeads := filepath.Join(rigRoot, "mayor", "rig", ".beads")

		// Check if either canonical location exists and has beads
		canonicalExists := false
		if hasBeadsSetup(rigBeads) {
			canonicalExists = true
		} else if hasBeadsSetup(mayorBeads) {
			canonicalExists = true
		}

		if !canonicalExists {
			relWt, _ := filepath.Rel(ctx.TownRoot, bt.worktreePath)
			unfixable = append(unfixable, relWt)
			continue
		}

		// Canonical location exists — recompute and rewrite the redirect
		if err := recomputeRedirect(ctx.TownRoot, bt.worktreePath); err != nil {
			relWt, _ := filepath.Rel(ctx.TownRoot, bt.worktreePath)
			unfixable = append(unfixable, relWt)
		}
	}

	if len(unfixable) > 0 {
		return fmt.Errorf("could not fix %d redirect(s) (no canonical beads found): %s",
			len(unfixable), strings.Join(unfixable, ", "))
	}

	return nil
}

// extractRigName derives the rig name from a worktree path within a town.
// For example, "/town/myrig/refinery/rig" returns "myrig".
func extractRigName(townRoot, worktreePath string) string {
	relPath, err := filepath.Rel(townRoot, worktreePath)
	if err != nil {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

// hasBeadsSetup checks whether a .beads directory has a working setup.
// A valid beads directory should have at least one of:
// - dolt/ directory (dolt database)
// - redirect file (chain to another location)
// - config.yaml (beads configuration)
func hasBeadsSetup(beadsDir string) bool {
	markers := []string{"dolt", "redirect", "config.yaml"}
	for _, marker := range markers {
		if _, err := os.Stat(filepath.Join(beadsDir, marker)); err == nil {
			return true
		}
	}
	return false
}

// recomputeRedirect rewrites a worktree's .beads/redirect to point to the correct target.
func recomputeRedirect(townRoot, worktreePath string) error {
	relPath, err := filepath.Rel(townRoot, worktreePath)
	if err != nil {
		return err
	}
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	if len(parts) < 2 {
		return fmt.Errorf("invalid worktree path")
	}

	rigRoot := filepath.Join(townRoot, parts[0])
	rigBeads := filepath.Join(rigRoot, ".beads")
	mayorBeads := filepath.Join(rigRoot, "mayor", "rig", ".beads")

	// Compute depth from worktree to rig root
	depth := len(parts) - 1
	upPath := strings.Repeat("../", depth)

	var redirectContent string
	switch {
	case hasBeadsSetup(rigBeads):
		// Check if rig beads has its own redirect (tracked beads case)
		rigRedirectFile := filepath.Join(rigBeads, "redirect")
		if data, err := os.ReadFile(rigRedirectFile); err == nil {
			rigTarget := strings.TrimSpace(string(data))
			if rigTarget != "" {
				// Skip intermediate hop, redirect directly to final destination.
				// Absolute paths are passed through as-is (same logic as beads.ComputeRedirectTarget).
				if filepath.IsAbs(rigTarget) {
					redirectContent = rigTarget
				} else {
					redirectContent = upPath + rigTarget
				}
				break
			}
		}
		redirectContent = upPath + ".beads"
	case hasBeadsSetup(mayorBeads):
		redirectContent = upPath + "mayor/rig/.beads"
	default:
		return fmt.Errorf("no valid beads location found")
	}

	// Ensure .beads directory exists
	worktreeBeads := filepath.Join(worktreePath, ".beads")
	if err := os.MkdirAll(worktreeBeads, 0755); err != nil {
		return err
	}

	// Write redirect file
	redirectFile := filepath.Join(worktreeBeads, "redirect")
	return os.WriteFile(redirectFile, []byte(redirectContent+"\n"), 0644)
}
