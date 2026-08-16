package rig

import (
	"path/filepath"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/runtime"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
)

// Provision makes a Worker workspace ready. Callers supply the rig path,
// the work directory, and the Role. The six steps run in one order:
// shared beads redirect, PRIME.md, overlay, local git exclude, runtime
// settings, setup hooks. Ignore patterns stay in .git/info/exclude so they
// do not reach the tracked Rig tree.
func Provision(rigPath, workDir, role string) error {
	townRoot, err := workspace.Find(rigPath)
	if err != nil || townRoot == "" {
		townRoot = filepath.Dir(rigPath)
	}

	if err := beads.SetupRedirect(townRoot, workDir); err != nil {
		style.PrintWarning("could not set up beads redirect: %v", err)
	}
	if err := beads.ProvisionPrimeMDForWorktree(workDir); err != nil {
		style.PrintWarning("could not provision PRIME.md: %v", err)
	}
	if err := CopyOverlay(rigPath, workDir); err != nil {
		style.PrintWarning("could not copy overlay files: %v", err)
	}
	if err := EnsureLocalExcludePatterns(workDir); err != nil {
		style.PrintWarning("could not update local git excludes: %v", err)
	}

	settingsDir := config.RoleSettingsDir(role, rigPath)
	if settingsDir == "" {
		settingsDir = workDir
	}
	rc := config.ResolveRoleAgentConfig(role, townRoot, rigPath)
	if err := runtime.EnsureSettingsForRole(settingsDir, workDir, role, rc); err != nil {
		style.PrintWarning("could not install runtime settings: %v", err)
	}
	if err := RunSetupHooks(rigPath, workDir); err != nil {
		style.PrintWarning("could not run setup hooks: %v", err)
	}
	return nil
}
