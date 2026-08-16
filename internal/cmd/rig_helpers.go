package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/git"
	"github.com/jonbaldie/gastown/internal/rig"
	"github.com/jonbaldie/gastown/internal/wisp"
	"github.com/jonbaldie/gastown/internal/workspace"
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

// discoverRigsForTownRoot loads the rigs config for the given town root and
// returns all registered rigs. Callers that don't yet have a town root
// should use getAllRigs, which resolves it from the cwd first.
func discoverRigsForTownRoot(townRoot string) ([]*rig.Rig, error) {
	rigsConfig, err := config.LoadRigsConfig(constants.MayorRigsPath(townRoot))
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	return rigMgr.DiscoverRigs()
}

// autoInferRig returns the sole registered rig for a given townRoot, or an
// actionable error when the result is ambiguous. Callers use this when no
// --rig flag was provided and cwd-based detection found nothing (e.g. Deacon
// at HQ level on a non-default install where "gastown" rig does not exist).
func autoInferRig(townRoot string) (name, path string, err error) {
	rigs, err := discoverRigsForTownRoot(townRoot)
	if err != nil {
		return "", "", fmt.Errorf("cannot determine target rig: %w; use --rig=NAME", err)
	}

	switch len(rigs) {
	case 1:
		return rigs[0].Name, rigs[0].Path, nil
	case 0:
		return "", "", fmt.Errorf("cannot determine target rig: no rigs registered in this workspace; use --rig=NAME")
	default:
		names := make([]string, len(rigs))
		for i, r := range rigs {
			names[i] = r.Name
		}
		return "", "", fmt.Errorf("cannot determine target rig (available: %s); use --rig=NAME", strings.Join(names, ", "))
	}
}

// getAllRigs discovers all rigs in the current Gas Town workspace.
// Returns the list of rigs and any error.
func getAllRigs() ([]*rig.Rig, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return nil, fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	return discoverRigsForTownRoot(townRoot)
}
