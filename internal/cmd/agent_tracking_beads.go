package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/beads"
)

// findCwdBeadsWorkDir finds the nearest .beads directory by walking up from CWD.
// It intentionally ignores BEADS_DIR for callers whose target is implied by
// the current rig worktree rather than inherited session environment.
func findCwdBeadsWorkDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	path := cwd
	for {
		if _, err := os.Stat(filepath.Join(path, ".beads")); err == nil {
			return path, nil
		}

		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}

	return "", fmt.Errorf("no .beads directory found")
}

// resolveAgentTrackingBeadsDir resolves the database used for agent identity
// state. Agent beads are town-owned even though their IDs use the owning rig's
// prefix, so allowing normal prefix routing (or pinning the cwd-local rig DB)
// sends a rig agent lookup to the work-issue database and returns not found.
//
// Prefer the town database discovered from CWD. The local/env resolver remains
// a fallback for standalone or partially initialized workspaces where no town
// marker is available yet.
func resolveAgentTrackingBeadsDir() (string, error) {
	cwd, err := os.Getwd()
	if err == nil {
		if townRoot := beads.FindTownRoot(cwd); townRoot != "" {
			if beadsDir := beads.ResolveBeadsDir(beads.GetTownBeadsPath(townRoot)); beadsDir != "" {
				return beadsDir, nil
			}
		}
	}

	workDir, localErr := findCwdBeadsWorkDir()
	if localErr != nil {
		workDir, localErr = findLocalBeadsDir()
	}
	if localErr != nil {
		if err != nil {
			return "", err
		}
		return "", localErr
	}

	beadsDir := beads.ResolveBeadsDir(workDir)
	if beadsDir == "" {
		return "", fmt.Errorf("not in a beads workspace")
	}
	return beadsDir, nil
}
