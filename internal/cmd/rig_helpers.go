package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/wisp"
	"github.com/steveyegge/gastown/internal/workspace"
)

// checkRigNotParkedOrDocked checks if a rig is parked or docked and returns
// an error if so. This prevents starting agents on rigs that have been
// intentionally taken offline.
func checkRigNotParkedOrDocked(rigName string) error {
	townRoot, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	if IsRigParked(townRoot, rigName) {
		return fmt.Errorf("rig '%s' is parked - use 'gt rig unpark %s' first", rigName, rigName)
	}

	prefix := "gt"
	if r.Config != nil && r.Config.Prefix != "" {
		prefix = r.Config.Prefix
	}

	if IsRigDocked(townRoot, rigName, prefix) {
		return fmt.Errorf("rig '%s' is docked - use 'gt rig undock %s' first", rigName, rigName)
	}

	return nil
}

// getRig finds the town root and retrieves the specified rig.
// This is the common boilerplate extracted from get*Manager functions.
// Returns the town root path and rig instance.
func getRig(rigName string) (string, *rig.Rig, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return "", nil, fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	rigsConfigPath := constants.MayorRigsPath(townRoot)
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(rigName)
	if err != nil {
		return "", nil, fmt.Errorf("rig '%s' not found", rigName)
	}

	return townRoot, r, nil
}

// hasRigBeadLabel checks if a rig's identity bead has a specific label.
// Returns false if the rig config or bead can't be loaded (safe default).
func hasRigBeadLabel(townRoot, rigName, label string) bool {
	rigPath := filepath.Join(townRoot, rigName)
	prefix := rigBeadsPrefix(townRoot, rigPath, rigName)
	if prefix == "" {
		return false
	}

	beadsPath := filepath.Join(rigPath, "mayor", "rig")
	if _, err := os.Stat(beadsPath); err != nil {
		beadsPath = rigPath
	}

	bd := beads.New(beadsPath)
	rigBeadID := beads.RigBeadIDWithPrefix(prefix, rigName)

	rigBead, err := bd.Show(rigBeadID)
	if err != nil {
		return false
	}

	for _, l := range rigBead.Labels {
		if l == label {
			return true
		}
	}
	return false
}

// IsRigParkedOrDocked checks if a rig is parked or docked by any mechanism
// (wisp ephemeral state or persistent bead labels). Returns (blocked, reason).
// This is the single entry point for all dispatch paths (sling, convoy launch,
// convoy stage) to check rig availability.
//
// Parked vs docked asymmetry: parked state is checked in both the wisp layer
// (ephemeral, set by "gt rig park") and bead labels (persistent fallback for
// when wisp state is lost during cleanup). Docked state is bead-label only
// because "gt rig dock" never writes to wisp — it persists exclusively via
// the rig identity bead's status:docked label.
func IsRigParkedOrDocked(townRoot, rigName string) (bool, string) {
	// Check wisp layer first (fast, local) — only relevant for parked state
	wispCfg := wisp.NewConfig(townRoot, rigName)
	if wispCfg.GetString(RigStatusKey) == RigStatusParked {
		return true, "parked"
	}

	// Single bead lookup for both parked and docked labels.
	// Look up the beads prefix from rigs.json (the rig registry), with fallback
	// to the rig's own config.json for isolated/test scenarios.
	rigPath := filepath.Join(townRoot, rigName)
	prefix := rigBeadsPrefix(townRoot, rigPath, rigName)
	if prefix == "" {
		return false, ""
	}

	beadsPath := filepath.Join(rigPath, "mayor", "rig")
	if _, err := os.Stat(beadsPath); err != nil {
		beadsPath = rigPath
	}

	// Skip bead lookup if prefix has no registered route. On installs where the
	// gastown infrastructure rig is not registered at the expected path, the
	// "gt-" prefix has no route entry, and Show() would emit a routing warning
	// once per convoy leg. The wisp check above already covers parked state.
	if beads.GetRigPathForPrefix(townRoot, prefix+"-") == "" {
		return false, ""
	}

	bd := beads.New(beadsPath)
	rigBeadID := beads.RigBeadIDWithPrefix(prefix, rigName)
	rigBead, err := bd.Show(rigBeadID)
	if err != nil {
		return false, ""
	}

	for _, l := range rigBead.Labels {
		if l == "status:parked" {
			return true, "parked"
		}
		if l == RigDockedLabel {
			return true, "docked"
		}
	}

	return false, ""
}

func rigBeadsPrefix(townRoot, rigPath, rigName string) string {
	rigsConfigPath := constants.MayorRigsPath(townRoot)
	if rigsConfig, err := config.LoadRigsConfig(rigsConfigPath); err == nil {
		if entry, ok := rigsConfig.Rigs[rigName]; ok && entry.BeadsConfig != nil && entry.BeadsConfig.Prefix != "" {
			return entry.BeadsConfig.Prefix
		}
	}

	rigConfigPath := filepath.Join(rigPath, "config.json")
	if rigCfg, err := config.LoadRigConfig(rigConfigPath); err == nil && rigCfg.Beads != nil && rigCfg.Beads.Prefix != "" {
		return rigCfg.Beads.Prefix
	}

	return ""
}

// getAllRigs discovers all rigs in the current Gas Town workspace.
// Returns the list of rigs and any error.
func getAllRigs() ([]*rig.Rig, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return nil, fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	rigs, err := rigMgr.DiscoverRigs()
	if err != nil {
		return nil, err
	}

	return rigs, nil
}
