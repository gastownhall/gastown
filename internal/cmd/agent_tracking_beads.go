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

// resolveAgentTrackingBeadsDirForID locates an agent identity across its
// registered rig ledger and the town ledger, preferring the rig record. State
// and await commands use the returned source so a resolver result remains
// writable regardless of which ledger currently owns the identity.
func resolveAgentTrackingBeadsDirForID(agentBead string) (string, error) {
	currentBeadsDir, err := resolveAgentTrackingBeadsDir()
	if err != nil {
		return "", err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	rigName := ""
	if parsedRig, _, _, ok := beads.ParseAgentBeadID(agentBead); ok {
		rigName = parsedRig
	}
	candidates, err := findAgentBeadCandidates(cwd, currentBeadsDir, rigName)
	if err != nil {
		return "", err
	}

	matches := make([]agentBeadCandidate, 0, 1)
	for _, candidate := range candidates {
		if candidate.ID == agentBead {
			matches = append(matches, candidate)
		}
	}
	match, err := pickBestAgentBead(matches)
	if err != nil {
		return "", err
	}
	if match == nil {
		return "", fmt.Errorf("agent bead not found in rig or town ledgers: %s", agentBead)
	}
	return match.BeadsDir, nil
}
