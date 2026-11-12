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

// resolveAgentTrackingBeadsDir resolves the bead database used for agent state.
// Agent tracking follows the agent's current rig, so cwd-local redirects must
// win over an inherited town-level BEADS_DIR. The env-first resolver remains a
// fallback for contexts that do not have a cwd-local .beads directory.
func resolveAgentTrackingBeadsDir() (string, error) {
	workDir, err := findCwdBeadsWorkDir()
	if err != nil {
		workDir, err = findLocalBeadsDir()
	}
	if err != nil {
		return "", err
	}

	beadsDir := beads.ResolveBeadsDir(workDir)
	if beadsDir == "" {
		return "", fmt.Errorf("not in a beads workspace")
	}
	return beadsDir, nil
}

// resolveAgentBeadTrackingDir resolves the database that actually contains an
// agent bead. Patrol commands normally start in a rig checkout, but canonical
// agent beads may still live in the town database. In that case the ID alone
// does not carry enough routing information (it commonly has the rig prefix),
// so probe the cwd-local database first and then the town database.
func resolveAgentBeadTrackingDir(agentBead string) (string, error) {
	currentBeadsDir, err := resolveAgentTrackingBeadsDir()
	if err != nil {
		return "", err
	}

	if _, err := getAllAgentLabels(agentBead, currentBeadsDir); err == nil {
		return currentBeadsDir, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return currentBeadsDir, nil
	}
	townRoot := beads.FindTownRoot(cwd)
	if townRoot == "" {
		return currentBeadsDir, nil
	}
	townBeadsDir := beads.ResolveBeadsDir(beads.GetTownBeadsPath(townRoot))
	if townBeadsDir == "" || filepath.Clean(townBeadsDir) == filepath.Clean(currentBeadsDir) {
		return currentBeadsDir, nil
	}

	if _, err := getAllAgentLabels(agentBead, townBeadsDir); err == nil {
		return townBeadsDir, nil
	}
	return currentBeadsDir, nil
}
