package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// spawnPolecatForSling is a seam for tests. Production uses SpawnPolecatForSling.
var spawnPolecatForSling = SpawnPolecatForSling

// resolveTargetAgentFn is a seam for tests. Production uses resolveTargetAgent.
var resolveTargetAgentFn = resolveTargetAgent

// resolveTargetAgent converts a target spec to agent ID, pane, and hook root.
func resolveTargetAgent(target string) (agentID string, pane string, hookRoot string, err error) {
	// First resolve to session name
	sessionName, err := resolveRoleToSession(target)
	if err != nil {
		return "", "", "", err
	}

	// Convert session name to agent ID format (this doesn't require tmux)
	agentID = sessionToAgentID(sessionName)

	// Get the pane for that session
	pane, err = getSessionPane(sessionName)
	if err != nil {
		return "", "", "", fmt.Errorf("getting pane for %s: %w", sessionName, err)
	}

	// Get the target's working directory for hook storage
	t := tmux.NewTmux()
	hookRoot, err = t.GetPaneWorkDir(sessionName)
	if err != nil {
		return "", "", "", fmt.Errorf("getting working dir for %s: %w", sessionName, err)
	}

	return agentID, pane, hookRoot, nil
}

// sessionToAgentID converts a session name to agent ID format.
// Uses session.ParseSessionName for consistent parsing across the codebase.
func sessionToAgentID(sessionName string) string {
	identity, err := session.ParseSessionName(sessionName)
	if err != nil {
		// Fallback for unparseable sessions
		return sessionName
	}
	return canonicalAssigneeAddress(identity)
}

// canonicalAssigneeAddress returns the address used for bead assignees and
// hook-status queries. This matches the form emitted by resolveSelfTarget and
// buildAgentIdentity: town-level agents (mayor, deacon) get a trailing slash.
// session.AgentIdentity.Address() returns the bare name for those roles, which
// causes the read/write mismatch in GH#3699.
func canonicalAssigneeAddress(identity *session.AgentIdentity) string {
	addr := identity.Address()
	switch identity.Role {
	case session.RoleMayor, session.RoleDeacon:
		if !strings.HasSuffix(addr, "/") {
			return addr + "/"
		}
	}
	return addr
}

// resolveSelfTarget determines agent identity, pane, and hook root for slinging to self.
func resolveSelfTarget() (agentID string, pane string, hookRoot string, err error) {
	roleInfo, err := GetRole()
	if err != nil {
		return "", "", "", fmt.Errorf("detecting role: %w", err)
	}

	// Build agent identity from role
	// Town-level agents use trailing slash to match addressToIdentity() normalization
	switch roleInfo.Role {
	case RoleMayor:
		agentID = "mayor/"
	case RoleDeacon:
		agentID = "deacon/"
	case RoleBoot:
		agentID = "deacon/boot"
	case RoleWitness:
		agentID = fmt.Sprintf("%s/witness", roleInfo.Rig)
	case RoleRefinery:
		agentID = fmt.Sprintf("%s/refinery", roleInfo.Rig)
	case RolePolecat:
		agentID = fmt.Sprintf("%s/polecats/%s", roleInfo.Rig, roleInfo.Polecat)
	case RoleCrew:
		agentID = fmt.Sprintf("%s/crew/%s", roleInfo.Rig, roleInfo.Polecat)
	case RoleDog:
		agentID = fmt.Sprintf("deacon/dogs/%s", roleInfo.Polecat)
	default:
		return "", "", "", fmt.Errorf("cannot determine agent identity (role: %s)", roleInfo.Role)
	}

	pane = os.Getenv("TMUX_PANE")
	hookRoot = roleInfo.Home
	if hookRoot == "" {
		// Fallback to git root if home not determined
		hookRoot, err = detectCloneRoot()
		if err != nil {
			return "", "", "", fmt.Errorf("detecting clone root: %w", err)
		}
	}

	return agentID, pane, hookRoot, nil
}

// ResolveTargetOptions controls target resolution behavior.
type ResolveTargetOptions struct {
	DryRun   bool
	Force    bool
	Create   bool
	Account  string
	Agent    string
	NoBoot   bool
	HookBead     string // Bead ID to set atomically during polecat spawn (empty = skip)
	BeadID       string // For cross-rig guard checks (empty = skip guard)
	TownRoot     string
	WorkDesc     string // Description for dog dispatch (defaults to HookBead if empty)
	BaseBranch   string // Override base branch for polecat worktree
	ResumeBranch string // Existing branch to resume (e.g. PR head); mutually exclusive with BaseBranch
}

// ResolvedTarget holds the results of target resolution.
type ResolvedTarget struct {
	Agent             string
	Pane              string
	WorkDir           string
	HookSetAtomically bool
	DelayedDogInfo    *DogDispatchInfo
	NewPolecatInfo    *SpawnedPolecatInfo
	IsSelfSling       bool
}

// resolveTarget resolves a target specification to agent, pane, and working directory.
// Handles: "." or empty (self), dog targets, rig targets (auto-spawn polecat),
// existing agents (with dead polecat fallback).
func resolveTarget(target string, opts ResolveTargetOptions) (*ResolvedTarget, error) {
	result := &ResolvedTarget{}

	// Empty target or "." = self-sling
	if target == "" || target == "." {
		agentID, pane, workDir, err := resolveSelfTarget()
		if err != nil {
			if target == "." {
				return nil, fmt.Errorf("resolving self for '.' target: %w", err)
			}
			return nil, err
		}
		result.Agent = agentID
		result.Pane = pane
		result.WorkDir = workDir
		result.IsSelfSling = true
		return result, nil
	}

	// Dog target
	if dogName, isDog := IsDogTarget(target); isDog {
		if opts.DryRun {
			if dogName == "" {
				fmt.Printf("Would dispatch to idle dog in kennel\n")
				result.Agent = "deacon/dogs/<idle>"
			} else {
				fmt.Printf("Would dispatch to dog '%s'\n", dogName)
				result.Agent = fmt.Sprintf("deacon/dogs/%s", dogName)
			}
			result.Pane = "<dog-pane>"
			return result, nil
		}
		workDesc := opts.WorkDesc
		if workDesc == "" {
			workDesc = opts.HookBead
		}
		dispatchOpts := DogDispatchOptions{
			Create:            opts.Create,
			WorkDesc:          workDesc,
			DelaySessionStart: true,
			AgentOverride:     opts.Agent,
		}
		dispatchInfo, err := DispatchToDog(dogName, dispatchOpts)
		if err != nil {
			return nil, fmt.Errorf("dispatching to dog: %w", err)
		}
		result.Agent = dispatchInfo.AgentID
		result.DelayedDogInfo = dispatchInfo
		fmt.Printf("Dispatched to dog %s (session start delayed)\n", dispatchInfo.DogName)
		return result, nil
	}

	// Rig target (auto-spawn polecat)
	if rigName, isRig := IsRigName(target); isRig {
		// Check if rig is parked or docked before dispatching (gt-4owfd.1, gt-11y)
		townRoot := opts.TownRoot
		if townRoot == "" {
			townRoot, _ = workspace.FindFromCwd()
		}
		if townRoot != "" {
			if blocked, reason := IsRigParkedOrDocked(townRoot, rigName); blocked {
				undoCmd := "gt rig unpark"
				if reason == "docked" {
					undoCmd = "gt rig undock"
				}
				return nil, fmt.Errorf("cannot sling to %s rig %q\n%s %s", reason, rigName, undoCmd, rigName)
			}
		}

		if opts.BeadID != "" && !opts.Force {
			if err := checkCrossRigGuard(opts.BeadID, rigName+"/polecats/_", opts.TownRoot); err != nil {
				return nil, err
			}
		}
		if opts.DryRun {
			fmt.Printf("Would spawn fresh polecat in rig '%s'\n", rigName)
			result.Agent = fmt.Sprintf("%s/polecats/<new>", rigName)
			result.Pane = "<new-pane>"
			return result, nil
		}
		fmt.Printf("Target is rig '%s', spawning fresh polecat...\n", rigName)
		spawnOpts := SlingSpawnOptions{
			Force:        opts.Force,
			Account:      opts.Account,
			Create:       opts.Create,
			HookBead:     opts.HookBead,
			Agent:        opts.Agent,
			BaseBranch:   opts.BaseBranch,
			ResumeBranch: opts.ResumeBranch,
		}
		spawnInfo, err := spawnPolecatForSling(rigName, spawnOpts)
		if err != nil {
			return nil, fmt.Errorf("spawning polecat: %w", err)
		}
		result.Agent = spawnInfo.AgentID()
		result.NewPolecatInfo = spawnInfo
		result.WorkDir = spawnInfo.ClonePath
		result.HookSetAtomically = opts.HookBead != ""
		if !opts.NoBoot {
			wakeRigAgents(rigName)
		}
		return result, nil
	}

	// Existing agent (with dead polecat fallback).
	// Uses resolveTargetAgentFn seam — crew, mayor, and all existing agents
	// resolve here, getting their pane for nudge delivery (gt-in7b).
	agentID, pane, workDir, err := resolveTargetAgentFn(target)
	if err != nil {
		if isPolecatTarget(target) {
			parts := strings.Split(target, "/")
			if len(parts) >= 3 && parts[1] == "polecats" {
				rigName := parts[0]
				if opts.BeadID != "" && !opts.Force {
					if err := checkCrossRigGuard(opts.BeadID, rigName+"/polecats/_", opts.TownRoot); err != nil {
						return nil, err
					}
				}
				fmt.Printf("Target polecat has no active session, spawning fresh polecat in rig '%s'...\n", rigName)
				spawnOpts := SlingSpawnOptions{
					Force:        opts.Force,
					Account:      opts.Account,
					Create:       opts.Create,
					HookBead:     opts.HookBead,
					Agent:        opts.Agent,
					BaseBranch:   opts.BaseBranch,
					ResumeBranch: opts.ResumeBranch,
				}
				spawnInfo, spawnErr := spawnPolecatForSling(rigName, spawnOpts)
				if spawnErr != nil {
					return nil, fmt.Errorf("spawning polecat to replace dead polecat: %w", spawnErr)
				}
				result.Agent = spawnInfo.AgentID()
				result.NewPolecatInfo = spawnInfo
				result.WorkDir = spawnInfo.ClonePath
				result.HookSetAtomically = opts.HookBead != ""
				if !opts.NoBoot {
					wakeRigAgents(rigName)
				}
				return result, nil
			}
		}
		return nil, fmt.Errorf("resolving target: %w", err)
	}
	result.Agent = agentID
	result.Pane = pane
	result.WorkDir = workDir
	return result, nil
}
