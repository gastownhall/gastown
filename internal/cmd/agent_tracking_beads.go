package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// townAgentBeadsDir returns the town-level beads directory for the current
// working directory, following redirects. Returns "" when the cwd is not
// inside a Gas Town workspace or the town beads directory cannot be resolved.
func townAgentBeadsDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	townRoot := beads.FindTownRoot(cwd)
	if townRoot == "" {
		return ""
	}
	return beads.ResolveBeadsDir(beads.GetTownBeadsPath(townRoot))
}

// resolveAgentBeadDir resolves the bead database that actually holds agentBead.
//
// Agent beads carry a rig prefix ("al-alphaprime2-witness") but are created in
// the TOWN database by beads.ForAgentBead, so neither prefix routing nor the
// cwd-local rig database finds them. resolveAgentTrackingBeadsDir on its own
// therefore points every rig agent at a database that holds no agent beads at
// all, which silently breaks idle/backoff/heartbeat tracking: the witness never
// persists its idle counter and so never reaches abbreviated patrol effort
// (hq-5z6).
//
// Resolution order:
//  1. the cwd-local (rig) database when it really holds the bead — this keeps
//     rig-local agent beads working for rigs that still have them;
//  2. the town database.
//
// When neither holds the bead the cwd-local directory is returned unchanged so
// callers keep emitting their existing "agent bead not found" diagnostics.
func resolveAgentBeadDir(agentBead string) (string, error) {
	local, err := resolveAgentTrackingBeadsDir()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(agentBead) == "" {
		return local, nil
	}
	if agentBeadExistsIn(local, agentBead) {
		return local, nil
	}

	townDir := townAgentBeadsDir()
	if townDir == "" || filepath.Clean(townDir) == filepath.Clean(local) {
		return local, nil
	}
	if agentBeadExistsIn(townDir, agentBead) {
		return townDir, nil
	}
	return local, nil
}

// agentBeadExistsIn reports whether agentBead is readable from beadsDir.
func agentBeadExistsIn(beadsDir, agentBead string) bool {
	_, err := getAllAgentLabels(agentBead, beadsDir)
	return err == nil
}
