package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/agentaddr"
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
// hook-status queries.
//
// session.AgentIdentity.Address() returns the bare name for town-level agents,
// which is what caused the read/write mismatch in GH#3699. Routing it through
// agentaddr gives every write site the one canonical spelling.
func canonicalAssigneeAddress(identity *session.AgentIdentity) string {
	return agentaddr.Normalize(identity.Address())
}

// resolveSelfTarget determines agent identity, pane, and hook root for slinging to self.
func resolveSelfTarget() (agentID string, pane string, hookRoot string, err error) {
	roleInfo, err := GetRole()
	if err != nil {
		return "", "", "", fmt.Errorf("detecting role: %w", err)
	}

	// Build the agent identity from the role. agentaddr renders the one
	// canonical spelling, so this write site cannot drift from the others.
	addr, ok := addressForRole(roleInfo.Role, roleInfo.Rig, roleInfo.Polecat)
	if !ok {
		return "", "", "", fmt.Errorf("cannot determine agent identity (role: %s)", roleInfo.Role)
	}
	agentID = addr.String()

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
	DryRun               bool
	Force                bool
	Create               bool
	Account              string
	Agent                string
	NoBoot               bool
	HookBead             string // Bead ID to set atomically during polecat spawn (empty = skip)
	BeadID               string // For cross-rig guard checks (empty = skip guard)
	TownRoot             string
	WorkDesc             string // Description for dog dispatch (defaults to HookBead if empty)
	BaseBranch           string // Override base branch for polecat worktree
	ResumeBranch         string // Existing branch to resume (e.g. PR head); mutually exclusive with BaseBranch
	SkipPolecatAdmission bool   // Caller already holds a capacity reservation
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
		if opts.BeadID != "" {
			if err := verifyBeadExistsInTargetRigDatabase(opts.BeadID, rigName, opts.TownRoot); err != nil {
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
			TownRoot:      opts.TownRoot,
			Force:         opts.Force,
			Account:       opts.Account,
			Create:        opts.Create,
			HookBead:      opts.HookBead,
			Agent:         opts.Agent,
			BaseBranch:    opts.BaseBranch,
			ResumeBranch:  opts.ResumeBranch,
			SkipAdmission: opts.SkipPolecatAdmission,
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
		if rigName, ok := missingPolecatTargetRig(target, opts.Create, opts.TownRoot); ok {
			if opts.BeadID != "" && !opts.Force {
				if err := checkCrossRigGuard(opts.BeadID, rigName+"/polecats/_", opts.TownRoot); err != nil {
					return nil, err
				}
			}
			if opts.BeadID != "" {
				if err := verifyBeadExistsInTargetRigDatabase(opts.BeadID, rigName, opts.TownRoot); err != nil {
					return nil, err
				}
			}
			fmt.Printf("Target polecat has no active session, spawning fresh polecat in rig '%s'...\n", rigName)
			spawnOpts := SlingSpawnOptions{
				TownRoot:      opts.TownRoot,
				Force:         opts.Force,
				Account:       opts.Account,
				Create:        opts.Create,
				HookBead:      opts.HookBead,
				Agent:         opts.Agent,
				BaseBranch:    opts.BaseBranch,
				ResumeBranch:  opts.ResumeBranch,
				SkipAdmission: opts.SkipPolecatAdmission,
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
		return nil, fmt.Errorf("resolving target: %w", err)
	}
	if opts.BeadID != "" && isPolecatTarget(agentID) {
		parts := strings.Split(agentID, "/")
		if len(parts) >= 3 && parts[1] == "polecats" {
			rigName := parts[0]
			if err := verifyBeadExistsInTargetRigDatabase(opts.BeadID, rigName, opts.TownRoot); err != nil {
				return nil, err
			}
		}
	}
	result.Agent = agentID
	result.Pane = pane
	result.WorkDir = workDir
	// Detect self-sling by pane: a named target (e.g. "deacon") that resolves to
	// the caller's own tmux pane should not inject the ack prompt — the caller is
	// already running and knows about the hook (GH#3839).
	if pane != "" && pane == os.Getenv("TMUX_PANE") {
		result.IsSelfSling = true
	}
	return result, nil
}

func missingPolecatTargetRig(target string, allowShorthand bool, townRoot string) (string, bool) {
	if isPolecatTarget(target) {
		parts := strings.Split(target, "/")
		return parts[0], true
	}
	if !allowShorthand {
		return "", false
	}
	parts := strings.Split(target, "/")
	if len(parts) != 2 || knownRoles[strings.ToLower(parts[1])] {
		return "", false
	}
	if townRoot == "" {
		townRoot = detectTownRootFromCwd()
	}
	if townRoot != "" {
		if info, err := os.Stat(filepath.Join(townRoot, parts[0], "crew", parts[1])); err == nil && info.IsDir() {
			return "", false
		}
	}
	return parts[0], true
}

// addressForRole converts a detected role context into a canonical address.
// The second return value is false when the role does not identify one agent —
// an unknown role, or a rig-scoped role detected without its rig.
func addressForRole(role Role, rigName, workerName string) (agentaddr.Address, bool) {
	var addr agentaddr.Address
	switch role {
	case RoleMayor:
		addr = agentaddr.Address{Role: agentaddr.RoleMayor}
	case RoleDeacon:
		addr = agentaddr.Address{Role: agentaddr.RoleDeacon}
	case RoleBoot:
		addr = agentaddr.Address{Role: agentaddr.RoleBoot}
	case RoleWitness:
		addr = agentaddr.Address{Rig: rigName, Role: agentaddr.RoleWitness}
	case RoleRefinery:
		addr = agentaddr.Address{Rig: rigName, Role: agentaddr.RoleRefinery}
	case RolePolecat:
		addr = agentaddr.Address{Rig: rigName, Role: agentaddr.RolePolecat, Name: workerName}
	case RoleCrew:
		addr = agentaddr.Address{Rig: rigName, Role: agentaddr.RoleCrew, Name: workerName}
	case RoleDog:
		addr = agentaddr.Address{Role: agentaddr.RoleDog, Name: workerName}
	default:
		return agentaddr.Address{}, false
	}
	return addr, addr.IsComplete()
}

// assigneeFlag renders the `--assignee` flag for a `bd update`, canonicalizing
// the address on the way through.
//
// Every write site used to interpolate whatever string it happened to hold, so
// the same agent landed in storage under several spellings and exact-match
// lookups missed rows that plainly existed (gt-cw1). Routing the flag through
// one helper means a new write site cannot reintroduce the split by accident.
//
// Normalize leaves unrecognised or incomplete input untouched apart from
// trimming, so a write site that holds something this package cannot parse
// still stores what the caller meant rather than a guess.
func assigneeFlag(addr string) string {
	return "--assignee=" + agentaddr.Normalize(addr)
}
