// Package rig provides rig management functionality.
package rig

import (
	"github.com/jonbaldie/gastown/internal/config"
)

// Rig represents a managed repository in the workspace.
type Rig struct {
	// Name is the rig identifier (directory name).
	Name string `json:"name"`

	// Path is the absolute path to the rig directory.
	Path string `json:"path"`

	// GitURL is the remote repository URL (fetch/pull).
	GitURL string `json:"git_url"`

	// PushURL is an optional push URL for read-only upstreams.
	// When set, polecats push here instead of to GitURL (e.g., personal fork).
	PushURL string `json:"push_url,omitempty"`

	// LocalRepo is an optional local repository used for reference clones.
	LocalRepo string `json:"local_repo,omitempty"`

	// Config is the rig-level configuration.
	Config *config.BeadsConfig `json:"config,omitempty"`

	// Polecats is the list of polecat names in this rig.
	Polecats []string `json:"polecats,omitempty"`

	// Crew is the list of crew worker names in this rig.
	// Crew workers are user-managed persistent workspaces.
	Crew []string `json:"crew,omitempty"`

	// HasWitness indicates if the rig has a witness agent.
	HasWitness bool `json:"has_witness"`

	// HasRefinery indicates if the rig has a refinery agent.
	HasRefinery bool `json:"has_refinery"`

	// HasMayor indicates if the rig has a mayor clone.
	HasMayor bool `json:"has_mayor"`
}

// AgentDirs are the standard agent directories in a rig.
// Note: witness doesn't have a /rig subdirectory (no clone needed).
var AgentDirs = []string{
	"polecats",
	"crew",
	"refinery/rig",
	"witness",
	"mayor/rig",
}

// RigSummary provides a concise overview of a rig.
type RigSummary struct {
	Name         string `json:"name"`
	PolecatCount int    `json:"polecat_count"`
	CrewCount    int    `json:"crew_count"`
	HasWitness   bool   `json:"has_witness"`
	HasRefinery  bool   `json:"has_refinery"`
}

// Summary returns a RigSummary for this rig.
func (r *Rig) Summary() RigSummary {
	return RigSummary{
		Name:         r.Name,
		PolecatCount: len(r.Polecats),
		CrewCount:    len(r.Crew),
		HasWitness:   r.HasWitness,
		HasRefinery:  r.HasRefinery,
	}
}

// BeadsPath returns the path to use for beads operations.
// Always returns the rig root path where .beads/ contains either:
//   - A local beads database (when repo doesn't track .beads/)
//   - A redirect file pointing to mayor/rig/.beads (when repo tracks .beads/)
//
// The redirect is set up by initBeads() during rig creation and followed
// automatically by the bd CLI and beads.ResolveBeadsDir().
//
// This ensures we never write to the user's repo clone (mayor/rig/) and
// all beads operations go through the redirect system.
func (r *Rig) BeadsPath() string {
	return r.Path
}

// DefaultBranch returns the configured default branch for this rig.
// Falls back to "main" if not configured or if config cannot be loaded.
func (r *Rig) DefaultBranch() string {
	cfg, err := LoadRigConfig(r.Path)
	if err != nil || cfg.DefaultBranch == "" {
		return "main"
	}
	return cfg.DefaultBranch
}
