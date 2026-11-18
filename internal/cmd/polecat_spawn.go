// Package cmd provides polecat spawning utilities for gt sling.
package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/workspace"
)

const minPolecatDirsPerRig = 30

// SpawnedPolecatInfo contains info about a spawned polecat session.
type SpawnedPolecatInfo struct {
	RigName     string // Rig name (e.g., "gastown")
	PolecatName string // Polecat name (e.g., "Toast")
	ClonePath   string // Path to polecat's git worktree
	SessionName string // Tmux session name (e.g., "gt-gastown-p-Toast")
	Pane        string // Tmux pane ID (empty until StartSession is called)
	BaseBranch  string // Effective base branch (e.g., "main", "integration/epic-id")
	Branch      string // Git branch name (for cleanup on rollback)

	// Internal fields for deferred session start
	account string
	agent   string
}

// AgentID returns the agent identifier (e.g., "gastown/polecats/Toast")
func (s *SpawnedPolecatInfo) AgentID() string {
	return fmt.Sprintf("%s/polecats/%s", s.RigName, s.PolecatName)
}

// SessionStarted returns true if the tmux session has been started.
func (s *SpawnedPolecatInfo) SessionStarted() bool {
	return s.Pane != ""
}

// SlingSpawnOptions contains options for spawning a polecat via sling.
type SlingSpawnOptions struct {
	TownRoot      string // Gas Town workspace root; falls back to cwd when empty
	Force         bool   // Force spawn even if polecat has uncommitted work
	Account       string // Claude Code account handle to use
	Create        bool   // Create polecat if it doesn't exist (currently always true for sling)
	HookBead      string // Bead ID to set as hook_bead at spawn time (atomic assignment)
	Agent         string // Agent override for this spawn (e.g., "gemini", "codex", "claude-haiku")
	BaseBranch    string // Override base branch for polecat worktree (e.g., "develop", "release/v2")
	ResumeBranch  string // Resume an existing branch (e.g. PR head) instead of creating polecat/<name>/<bead>+<ts>
	SkipAdmission bool   // Caller already holds a polecat admission reservation
}

func effectivePolecatDirCap(configured int) int {
	if configured < minPolecatDirsPerRig {
		return minPolecatDirsPerRig
	}
	return configured
}

func reclaimBrokenIdlePolecatForSling(polecatMgr *polecat.Manager) (bool, error) {
	polecats, err := polecatMgr.List()
	if err != nil {
		return false, err
	}

	for _, candidate := range polecats {
		if candidate == nil || candidate.State != polecat.StateIdle || candidate.Issue != "" {
			continue
		}
		verifyErr := verifyWorktreeExists(candidate.ClonePath)
		if verifyErr == nil || !polecat.IsStructuralWorktreeError(verifyErr) {
			continue
		}

		fmt.Printf("  Reclaiming broken idle polecat %s before allocation: %v\n", candidate.Name, verifyErr)
		if err := polecatMgr.ReclaimBrokenIdlePolecat(candidate.Name); err != nil {
			fmt.Printf("  Broken idle polecat %s was not safe to reclaim: %v\n", candidate.Name, err)
			continue
		}
		fmt.Printf("  %s Broken idle polecat %s reclaimed before assigning new work\n", style.Bold.Render("✓"), candidate.Name)
		return true, nil
	}

	return false, nil
}

// slingPolecatEnv holds the town, rig and manager handles that every sling
// polecat path needs. Resolved in one place so a preflight added for the spawn
// path cannot go missing from the resume path.
type slingPolecatEnv struct {
	townRoot   string
	rigName    string
	rig        *rig.Rig
	tmux       *tmux.Tmux
	polecatMgr *polecat.Manager
}

// resolveSlingPolecatEnv loads the rig and polecat manager for a sling and runs
// the preflight checks that must pass before any polecat is touched.
func resolveSlingPolecatEnv(rigName string, opts SlingSpawnOptions) (*slingPolecatEnv, error) {
	// Find workspace
	townRoot := opts.TownRoot
	if townRoot == "" {
		var err error
		townRoot, err = workspace.FindFromCwdOrError()
		if err != nil {
			return nil, fmt.Errorf("not in a Gas Town workspace: %w", err)
		}
	}

	// Load rig config
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(rigName)
	if err != nil {
		return nil, fmt.Errorf("rig '%s' not found", rigName)
	}

	// Get polecat manager (with tmux for session-aware allocation)
	polecatGit := git.NewGit(r.Path)
	t := tmux.NewTmux()
	polecatMgr := polecat.NewManager(r, polecatGit, t)

	// Pre-spawn Dolt health check (gt-94llt7): verify Dolt is reachable before
	// allocating a polecat. Prevents orphaned polecats when Dolt is down.
	if err := polecatMgr.CheckDoltHealth(); err != nil {
		return nil, fmt.Errorf("pre-spawn health check failed: %w", err)
	}

	// Pre-spawn admission control (gt-1obzke): verify Dolt server has connection
	// capacity before spawning. Prevents connection storms during mass sling.
	if err := polecatMgr.CheckDoltServerCapacity(); err != nil {
		return nil, fmt.Errorf("admission control: %w", err)
	}

	if blocked, reason := IsRigParkedOrDocked(townRoot, rigName); blocked {
		undoCmd := "gt rig unpark"
		if reason == "docked" {
			undoCmd = "gt rig undock"
		}
		return nil, fmt.Errorf("cannot sling to %s rig %q\n%s %s", reason, rigName, undoCmd, rigName)
	}

	return &slingPolecatEnv{townRoot: townRoot, rigName: rigName, rig: r, tmux: t, polecatMgr: polecatMgr}, nil
}

// errReuseUnavailable marks a reuse failure the caller may recover from by
// allocating a different polecat. Failures that happen AFTER reuse succeeded are
// returned bare: allocating around those would hide a broken polecat record.
var errReuseUnavailable = errors.New("cannot reuse idle polecat")

// Seams for tests. Production uses the functions they are initialised with.
var (
	resolveSlingPolecatEnvFn   = resolveSlingPolecatEnv
	polecatResumableBranchFn   = polecatResumableBranch
	reuseIdlePolecatForSlingFn = reuseIdlePolecatForSling
)

// resolveSlingBaseBranch computes the base branch a sling should start from,
// origin-qualified for the polecat manager.
//
// ResumeBranch (gh#3602) takes precedence: when resuming an existing branch we
// must not start from main or auto-detect an integration branch.
func resolveSlingBaseBranch(r *rig.Rig, opts SlingSpawnOptions) string {
	baseBranch := opts.BaseBranch
	if opts.ResumeBranch != "" {
		return baseBranch
	}
	if baseBranch == "" && opts.HookBead != "" {
		// Auto-detect: check if the hooked bead's parent epic has an integration branch
		settingsPath := filepath.Join(r.Path, "settings", "config.json")
		polecatIntegrationEnabled := true
		if settings, err := config.LoadRigSettings(settingsPath); err == nil && settings.MergeQueue != nil {
			polecatIntegrationEnabled = settings.MergeQueue.IsPolecatIntegrationEnabled()
		}
		if polecatIntegrationEnabled {
			repoGit, repoErr := getRigGit(r.Path)
			if repoErr == nil {
				bd := beads.New(r.Path)
				detected, detectErr := beads.DetectIntegrationBranch(bd, repoGit, opts.HookBead)
				if detectErr == nil && detected != "" {
					baseBranch = "origin/" + detected
					fmt.Printf("  Auto-detected integration branch: %s\n", detected)
				}
			}
		}
	}
	if baseBranch != "" && !strings.HasPrefix(baseBranch, "origin/") {
		baseBranch = "origin/" + baseBranch
	}
	return baseBranch
}

// reuseIdlePolecatForSling puts work on an existing polecat using branch-only
// operations — its own worktree, no worktree add/remove. Phase 3 of
// persistent-polecat-pool: eliminates ~5s worktree creation overhead.
//
// A reuse that is unsafe or fails returns an error wrapping errReuseUnavailable,
// which the pool path treats as "allocate a different polecat instead of
// repairing this worktree destructively".
func reuseIdlePolecatForSling(env *slingPolecatEnv, polecatName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
	r := env.rig
	rigName := env.rigName
	baseBranch := resolveSlingBaseBranch(r, opts)

	addOpts := polecat.AddOptions{
		HookBead:     opts.HookBead,
		BaseBranch:   baseBranch,
		ResumeBranch: opts.ResumeBranch,
	}
	if _, err := env.polecatMgr.ReuseIdlePolecat(polecatName, addOpts); err != nil {
		return nil, fmt.Errorf("%w %s: %w", errReuseUnavailable, polecatName, err)
	}

	polecatObj, err := env.polecatMgr.Get(polecatName)
	if err != nil {
		return nil, fmt.Errorf("getting idle polecat after reuse: %w", err)
	}
	if err := verifyWorktreeExists(polecatObj.ClonePath); err != nil {
		return nil, fmt.Errorf("worktree verification failed for reused %s: %w", polecatName, err)
	}

	polecatSessMgr := polecat.NewSessionManager(env.tmux, r)
	sessionName := polecatSessMgr.SessionName(polecatName)

	fmt.Printf("%s Polecat %s reused (idle → working, session start deferred)\n", style.Bold.Render("✓"), polecatName)
	_ = events.LogFeed(events.TypeSpawn, "gt", events.SpawnPayload(rigName, polecatName))

	effectiveBranch := strings.TrimPrefix(baseBranch, "origin/")
	if effectiveBranch == "" {
		effectiveBranch = r.DefaultBranch()
	}
	if opts.ResumeBranch != "" {
		effectiveBranch = opts.ResumeBranch
	}

	return &SpawnedPolecatInfo{
		RigName:     rigName,
		PolecatName: polecatName,
		ClonePath:   polecatObj.ClonePath,
		SessionName: sessionName,
		Pane:        "",
		BaseBranch:  effectiveBranch,
		Branch:      polecatObj.Branch,
		account:     opts.Account,
		agent:       opts.Agent,
	}, nil
}

// resumeOptionsForPolecat fills in the branch a resume of a NAMED polecat should
// land on, given the branch its worktree is currently sitting on.
//
// With no --branch and no --base-branch, that branch is the polecat's own — but
// only when it holds THIS bead's work. This is the point of si-n7vl: recovering
// an idle polecat must not require the caller to name a branch, because naming
// one is what put two worktrees on a single ref (si-d6kw).
//
// The test is "does the branch encode the bead being slung", not "does the
// branch look like work". Those differ, and the difference is a live failure:
// idle keeper sits on polecat/keeper/si-aka.37+ms43gm2z, so slinging a NEW bead
// si-aka.99 to keeper under the looser test would land si-aka.99's work on
// si-aka.37's branch and contaminate its MR — with the sling reporting success
// and no symptom but two beads on one ref (Mayor's ruling, 2026-07-29).
//
// Ambiguity resolves to fresh: an unparseable branch, a detached HEAD, the rig
// default branch, or a parent/child id that does not match exactly. A fresh
// branch is recoverable; contamination is not.
func resumeOptionsForPolecat(currentBranch, defaultBranch string, opts SlingSpawnOptions) SlingSpawnOptions {
	if opts.ResumeBranch != "" || opts.BaseBranch != "" {
		return opts
	}
	if currentBranch == "" || currentBranch == defaultBranch {
		return opts
	}
	// Decode with the inverse of the producer (FormatGeneratedBranchName), so a
	// change to the branch convention cannot leave this reading the old one.
	meta, ok := polecat.ParseBranchName(currentBranch)
	if !ok {
		return opts
	}
	if opts.HookBead != "" && meta.Issue != opts.HookBead {
		return opts
	}
	opts.ResumeBranch = currentBranch
	return opts
}

// ResumePolecatForSling reattaches work to a polecat named explicitly in a sling
// target, on ITS worktree and ITS branch. Used by gt sling when the target names
// a polecat whose tmux session is gone but whose sandbox is intact.
//
// The idle polecat keeps its worktree and its branch when its session dies;
// only the session is missing. Spawning a fresh polecat instead — which is what
// gt did before si-n7vl — silently substitutes a different polecat on a fresh
// branch off main, so the work being recovered is never reattached.
func ResumePolecatForSling(rigName, polecatName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
	env, err := resolveSlingPolecatEnvFn(rigName, opts)
	if err != nil {
		return nil, err
	}

	currentBranch, err := polecatResumableBranchFn(env, polecatName)
	if err != nil {
		return nil, err
	}

	requestedBranch := opts.ResumeBranch
	opts = resumeOptionsForPolecat(currentBranch, env.rig.DefaultBranch(), opts)
	if opts.ResumeBranch != requestedBranch {
		fmt.Printf("Resuming polecat %s on its existing branch %s\n", polecatName, opts.ResumeBranch)
	}

	return reuseIdlePolecatForSlingFn(env, polecatName, opts)
}

// polecatResumableBranch checks that a named polecat still has a usable worktree
// and reports the branch that worktree is currently on. An error means there is
// nothing to resume and the caller should allocate a fresh polecat instead.
func polecatResumableBranch(env *slingPolecatEnv, polecatName string) (string, error) {
	polecatObj, err := env.polecatMgr.Get(polecatName)
	if err != nil {
		return "", fmt.Errorf("polecat '%s' not found in rig '%s': %w", polecatName, env.rigName, err)
	}
	if err := verifyWorktreeExists(polecatObj.ClonePath); err != nil {
		return "", fmt.Errorf("polecat '%s' has no usable worktree at %s: %w", polecatName, polecatObj.ClonePath, err)
	}

	// Read the branch off the worktree, not off the polecat record: the worktree
	// is the thing being resumed, and the record can lag it.
	branch, err := git.NewGit(polecatObj.ClonePath).CurrentBranch()
	if err != nil {
		// Not fatal: an unreadable HEAD just means there is no branch to carry
		// over, and reuse falls back to minting a fresh one.
		style.PrintWarning("could not read current branch of %s: %v", polecatName, err)
		return "", nil
	}
	return branch, nil
}

// SpawnPolecatForSling creates a fresh polecat and optionally starts its session.
// This is used by gt sling when the target is a rig name.
// The caller (sling) handles hook attachment and nudging.
func SpawnPolecatForSling(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
	env, err := resolveSlingPolecatEnv(rigName, opts)
	if err != nil {
		return nil, err
	}
	townRoot, r, t, polecatMgr := env.townRoot, env.rig, env.tmux, env.polecatMgr

	var admission *polecatAdmissionHandle
	if !opts.SkipAdmission {
		admission, _, err = acquirePolecatAdmissionFn(townRoot, rigName, opts.HookBead, "spawn-or-reuse")
		if err != nil {
			return nil, err
		}
		defer admission.Release()
	}

	// Per-bead respawn circuit breaker (clown show #22):
	// Track how many times this bead has been slung. Block after N attempts
	// to prevent witness→deacon→sling feedback loops.
	if opts.HookBead != "" && !opts.Force {
		if witness.ShouldBlockRespawn(townRoot, opts.HookBead) {
			maxRespawns := config.LoadOperationalConfig(townRoot).GetWitnessConfig().MaxBeadRespawnsV()
			return nil, fmt.Errorf("respawn limit reached for %s (%d attempts). "+
				"This bead keeps failing — investigate before re-dispatching.\n"+
				"Override: gt sling %s %s --force\n"+
				"Reset:    gt sling respawn-reset %s",
				opts.HookBead, maxRespawns,
				opts.HookBead, rigName, opts.HookBead)
		}
		witness.RecordBeadRespawn(townRoot, opts.HookBead)
	}

	if reclaimed, err := reclaimBrokenIdlePolecatForSling(polecatMgr); err != nil {
		style.PrintWarning("could not reclaim broken idle polecat before allocation: %v", err)
	} else if reclaimed {
		fmt.Println("  Allocating fresh polecat after reclaiming broken idle sandbox...")
	}

	// Persistent polecat model (gt-4ac): try to reuse an idle polecat first.
	// Idle polecats have completed their work but kept their sandbox (worktree).
	// Reusing avoids the overhead of creating a new worktree.
	idlePolecat, findErr := polecatMgr.FindIdlePolecat()
	if findErr == nil && idlePolecat != nil {
		polecatName := idlePolecat.Name
		fmt.Printf("Reusing idle polecat: %s\n", polecatName)

		info, reuseErr := reuseIdlePolecatForSling(env, polecatName, opts)
		if reuseErr == nil {
			return info, nil
		}
		// Only an unsafe-or-failed reuse falls through to a fresh allocation.
		// A failure AFTER reuse succeeded (missing polecat record, unverifiable
		// worktree) is a hard error: allocating around it would hide it.
		if !errors.Is(reuseErr, errReuseUnavailable) {
			return nil, reuseErr
		}
		fmt.Printf("  %v; allocating new...\n", reuseErr)
	}

	// Per-rig directory cap: prevent unbounded worktree accumulation, but only
	// after trying safe reuse. A reusable preserved polecat should not be blocked
	// just because the rig is already at the directory cap.
	maxPolecatDirsPerRig := effectivePolecatDirCap(r.GetIntConfig("max_polecats"))
	rigPolecatDir := filepath.Join(townRoot, rigName, "polecats")
	if entries, err := os.ReadDir(rigPolecatDir); err == nil {
		dirCount := 0
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				dirCount++
			}
		}
		if dirCount >= maxPolecatDirsPerRig {
			return nil, fmt.Errorf("rig %s has %d polecat directories (max %d). "+
				"Resolve recovery-needed polecats before allocating more slots: gt polecat list %s",
				rigName, dirCount, maxPolecatDirsPerRig, rigName)
		}
	}

	// Determine base branch for polecat worktree.
	baseBranch := resolveSlingBaseBranch(r, opts)

	// Build add options with hook_bead set atomically at spawn time
	addOpts := polecat.AddOptions{
		HookBead:     opts.HookBead,
		BaseBranch:   baseBranch,
		ResumeBranch: opts.ResumeBranch,
	}

	// No idle polecat available — allocate and create atomically (GH#2215).
	// AllocateAndAdd holds the pool lock through directory creation, preventing
	// concurrent processes from allocating the same name.
	polecatName, _, err := polecatMgr.AllocateAndAdd(addOpts)
	if err != nil {
		return nil, fmt.Errorf("allocating and creating polecat: %w", err)
	}
	fmt.Printf("Created polecat: %s\n", polecatName)

	// Get polecat object for path info
	polecatObj, err := polecatMgr.Get(polecatName)
	if err != nil {
		return nil, fmt.Errorf("getting polecat after creation: %w", err)
	}

	// Verify worktree was actually created (fixes #1070)
	// The identity bead may exist but worktree creation can fail silently
	if err := verifyWorktreeExists(polecatObj.ClonePath); err != nil {
		// Clean up the partial state before returning error
		_ = polecatMgr.Remove(polecatName, true) // force=true to clean up partial state
		return nil, fmt.Errorf("worktree verification failed for %s: %w\nHint: try 'gt polecat nuke %s/%s --force' to clean up",
			polecatName, err, rigName, polecatName)
	}

	// Get session manager for session name (session start is deferred)
	polecatSessMgr := polecat.NewSessionManager(t, r)
	sessionName := polecatSessMgr.SessionName(polecatName)

	fmt.Printf("%s Polecat %s spawned (session start deferred)\n", style.Bold.Render("✓"), polecatName)

	// Log spawn event to activity feed
	_ = events.LogFeed(events.TypeSpawn, "gt", events.SpawnPayload(rigName, polecatName))

	// Compute effective base branch (strip origin/ prefix since formula prepends it)
	effectiveBranch := strings.TrimPrefix(baseBranch, "origin/")
	if effectiveBranch == "" {
		effectiveBranch = r.DefaultBranch()
	}
	if opts.ResumeBranch != "" {
		effectiveBranch = opts.ResumeBranch
	}

	return &SpawnedPolecatInfo{
		RigName:     rigName,
		PolecatName: polecatName,
		ClonePath:   polecatObj.ClonePath,
		SessionName: sessionName,
		Pane:        "", // Empty until StartSession is called
		BaseBranch:  effectiveBranch,
		Branch:      polecatObj.Branch,
		account:     opts.Account,
		agent:       opts.Agent,
	}, nil
}

// StartSession starts the tmux session for a spawned polecat.
// This is called after the molecule/bead is attached, so the polecat
// sees its work when gt prime runs on session start.
// Returns the pane ID after session start.
func (s *SpawnedPolecatInfo) StartSession() (string, error) {
	if s.SessionStarted() {
		return s.Pane, nil
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return "", fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rig config
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(s.RigName)
	if err != nil {
		return "", fmt.Errorf("rig '%s' not found", s.RigName)
	}

	// Resolve account
	accountsPath := constants.MayorAccountsPath(townRoot)
	claudeConfigDir, _, err := config.ResolveAccountConfigDir(accountsPath, s.account)
	if err != nil {
		return "", fmt.Errorf("resolving account: %w", err)
	}

	// Start session
	t := tmux.NewTmux()
	polecatSessMgr := polecat.NewSessionManager(t, r)

	fmt.Printf("Starting session for %s/%s...\n", s.RigName, s.PolecatName)
	startOpts := polecat.SessionStartOptions{
		RuntimeConfigDir: claudeConfigDir,
		Agent:            s.agent,
	}
	if err := polecatSessMgr.Start(s.PolecatName, startOpts); err != nil {
		return "", fmt.Errorf("starting session: %w", err)
	}

	// Wait for runtime to be fully ready before returning.
	// When an agent override is specified (e.g., --agent codex), resolve the runtime
	// config from the override so WaitForRuntimeReady uses the correct readiness
	// strategy (delay-based for Codex vs prompt-polling for Claude). Without this,
	// ResolveRoleAgentConfig returns the default agent (Claude) and polls for "❯ "
	// in a Codex session, always timing out after 30 seconds (gt-1j3m).
	spawnTownRoot := filepath.Dir(r.Path)
	var runtimeConfig *config.RuntimeConfig
	if s.agent != "" {
		rc, _, err := config.ResolveAgentConfigWithOverride(spawnTownRoot, r.Path, s.agent)
		if err != nil {
			style.PrintWarning("resolving agent config for %s: %v (using default)", s.agent, err)
			runtimeConfig = config.ResolveRoleAgentConfig("polecat", spawnTownRoot, r.Path)
		} else {
			runtimeConfig = rc
		}
	} else {
		runtimeConfig = config.ResolveRoleAgentConfig("polecat", spawnTownRoot, r.Path)
	}
	if err := t.WaitForRuntimeReady(s.SessionName, runtimeConfig, 30*time.Second); err != nil {
		style.PrintWarning("runtime may not be fully ready: %v", err)
	}

	// Update agent state with retry logic (gt-94llt7: fail-safe Dolt writes).
	// Note: warn-only, not fail-hard. The tmux session is already started above,
	// so returning an error here would leave an orphaned session with no cleanup path.
	// The polecat can still function without the agent state update — it only affects
	// monitoring visibility, not correctness. Compare with createAgentBeadWithRetry
	// which fails hard because a polecat without an agent bead is untrackable.
	polecatGit := git.NewGit(r.Path)
	polecatMgr := polecat.NewManager(r, polecatGit, t)
	if err := polecatMgr.SetAgentStateWithRetry(s.PolecatName, "working"); err != nil {
		style.PrintWarning("could not update agent state after retries: %v", err)
	}

	// Update issue status from hooked to in_progress.
	// Also warn-only for the same reason: session is already running.
	if err := polecatMgr.SetState(s.PolecatName, polecat.StateWorking); err != nil {
		style.PrintWarning("could not update issue status to in_progress: %v", err)
	}

	// Get pane — if this fails, the session may have died during startup.
	// Kill the dead session to prevent "session already running" on next attempt (gt-jn40ft).
	pane, err := getSessionPane(s.SessionName)
	if err != nil {
		// Session likely died — clean up the tmux session so it doesn't block re-sling
		_ = t.KillSession(s.SessionName)
		return "", fmt.Errorf("getting pane for %s (session likely died during startup): %w", s.SessionName, err)
	}

	s.Pane = pane
	return pane, nil
}

// IsRigName checks if a target string is a rig name (not a role or path).
// Returns the rig name and true if it's a valid rig.
func IsRigName(target string) (string, bool) {
	// If it contains a slash, it's a path format (rig/role or rig/crew/name)
	if strings.Contains(target, "/") {
		return "", false
	}

	// Check known non-rig role names
	switch strings.ToLower(target) {
	case constants.RoleMayor, "may", constants.RoleDeacon, "dea", constants.RoleCrew, constants.RoleWitness, "wit", constants.RoleRefinery, "ref":
		return "", false
	}

	// Try to load as a rig
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return "", false
	}

	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return "", false
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	_, err = rigMgr.GetRig(target)
	if err != nil {
		return "", false
	}

	return target, true
}

// verifyWorktreeExists checks that a git worktree was actually created at the given path
// and that it is a functional git repository. Returns an error if the worktree is missing,
// has a broken .git reference, or fails basic git validation. (GH#2056)
func verifyWorktreeExists(clonePath string) error {
	return polecat.VerifyWorktreeExists(clonePath)
}
