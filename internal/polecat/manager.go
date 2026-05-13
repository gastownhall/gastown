// Liftoff test: 2026-02-08T14:00:00

package polecat

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gofrs/flock"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/runtime"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/templates"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/util"
)

// Retry constants for Dolt operations (matching hook update pattern in sling.go).
// Configurable via operational.polecat in settings/config.json.
const (
	doltMaxRetries  = 10
	doltBaseBackoff = 500 * time.Millisecond
	doltBackoffMax  = 30 * time.Second

	// doltStateRetries is a reduced retry count for SetAgentStateWithRetry.
	// Agent state is a monitoring concern, not a correctness requirement (see
	// comment on SetAgentStateWithRetry). 10 retries with exponential backoff
	// wastes ~2 minutes on persistent failures, blocking `gt sling` for no
	// benefit since the caller already treats errors as warn-only.
	// 3 retries (total backoff ~3.5s) is sufficient to ride out transient
	// Dolt hiccups without punishing interactive workflows.
	doltStateRetries = 3
)

// doltBackoff calculates exponential backoff with ±25% jitter for a given attempt (1-indexed).
// Formula: base * 2^(attempt-1) * (1 ± 25% random), capped at doltBackoffMax.
func doltBackoff(attempt int) time.Duration {
	backoff := doltBaseBackoff
	for i := 1; i < attempt; i++ {
		backoff *= 2
		if backoff > doltBackoffMax {
			backoff = doltBackoffMax
			break
		}
	}
	// Apply ±25% jitter
	jitter := 1.0 + (rand.Float64()-0.5)*0.5 // range [0.75, 1.25]
	result := time.Duration(float64(backoff) * jitter)
	if result > doltBackoffMax {
		result = doltBackoffMax
	}
	return result
}

// isDoltOptimisticLockError returns true if the error is an optimistic lock / serialization failure.
// These indicate transient write conflicts from concurrent Dolt operations — worth retrying.
func isDoltOptimisticLockError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "optimistic lock") ||
		strings.Contains(msg, "serialization failure") ||
		strings.Contains(msg, "lock wait timeout") ||
		strings.Contains(msg, "try restarting transaction") ||
		strings.Contains(msg, "database is read only") ||
		strings.Contains(msg, "cannot update manifest")
}

// isDoltConfigError returns true if the error indicates a configuration or initialization
// problem rather than a transient failure. Config errors should NOT be retried because
// they will fail identically on every attempt, wasting ~3 minutes in the retry loop.
// See gt-2ra: polecat spawn hang when Dolt DB not initialized.
func isDoltConfigError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not initialized") ||
		strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "table not found") ||
		strings.Contains(msg, "issue_prefix") ||
		strings.Contains(msg, "no database") ||
		strings.Contains(msg, "database not found") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "configure custom types") ||
		strings.Contains(msg, "identity mismatch") ||
		strings.Contains(msg, "Unknown database")
}

// Common errors
var (
	ErrPolecatExists      = errors.New("polecat already exists")
	ErrPolecatNotFound    = errors.New("polecat not found")
	ErrHasChanges         = errors.New("polecat has uncommitted changes")
	ErrHasUncommittedWork = errors.New("polecat has uncommitted work")
	ErrShellInWorktree    = errors.New("shell working directory is inside polecat worktree")
	ErrDoltUnhealthy      = errors.New("dolt health check failed")
	ErrDoltAtCapacity     = errors.New("dolt server at connection capacity")
	ErrDiskSpaceLow       = errors.New("insufficient disk space")
)

// UncommittedWorkError provides details about uncommitted work.
type UncommittedWorkError struct {
	PolecatName string
	Status      *git.UncommittedWorkStatus
}

func (e *UncommittedWorkError) Error() string {
	return fmt.Sprintf("polecat %s has uncommitted work: %s", e.PolecatName, e.Status.String())
}

func (e *UncommittedWorkError) Unwrap() error {
	return ErrHasUncommittedWork
}

// Manager handles polecat lifecycle.
type Manager struct {
	rig      *rig.Rig
	git      *git.Git
	beads    *beads.Beads
	namePool *NamePool
	tmux     *tmux.Tmux
	townRoot string // Computed once at construction; used by agentBeadID for deterministic IDs
}

// NewManager creates a new polecat manager.
func NewManager(r *rig.Rig, g *git.Git, t *tmux.Tmux) *Manager {
	// Use the resolved beads directory to find where bd commands should run.
	// For tracked beads: rig/.beads/redirect -> mayor/rig/.beads, so use mayor/rig
	// For local beads: rig/.beads is the database, so use rig root
	resolvedBeads := beads.ResolveBeadsDir(r.Path)
	beadsPath := filepath.Dir(resolvedBeads) // Get the directory containing .beads

	// Compute town root once for deterministic use across all Manager methods.
	// Rig path is always filepath.Join(townRoot, rigName), so filepath.Dir is correct
	// and avoids the non-determinism of workspace.Find which can fail or resolve
	// differently depending on call-site context (gt-lph).
	townRoot := filepath.Dir(r.Path)

	// Try to load rig settings for namepool config
	settingsPath := filepath.Join(r.Path, "settings", "config.json")
	var pool *NamePool

	settings, err := config.LoadRigSettings(settingsPath)
	if err == nil && settings.Namepool != nil {
		// If style is set but not built-in and no explicit names, resolve custom theme
		names := settings.Namepool.Names
		if len(names) == 0 && settings.Namepool.Style != "" && !IsBuiltinTheme(settings.Namepool.Style) {
			if resolved, rErr := ResolveThemeNames(townRoot, settings.Namepool.Style); rErr == nil {
				names = resolved
			}
		}
		pool = NewNamePoolWithConfig(
			r.Path,
			r.Name,
			settings.Namepool.Style,
			names,
			settings.Namepool.MaxBeforeNumbering,
		)
	} else {
		// Fallback: check rig-level config.json for polecat_names
		// (pool-init and gt rig config write namepool config here).
		if rigCfg, rcErr := rig.LoadRigConfig(r.Path); rcErr == nil && len(rigCfg.PolecatNames) > 0 {
			pool = NewNamePoolWithConfig(r.Path, r.Name, "", rigCfg.PolecatNames, 0)
		} else {
			pool = NewNamePool(r.Path, r.Name)
		}
	}

	// Set town root for custom theme resolution in getNames()
	pool.SetTownRoot(townRoot)

	_ = pool.Load() // non-fatal: state file may not exist for new rigs

	return &Manager{
		rig:      r,
		git:      g,
		beads:    beads.NewWithBeadsDir(beadsPath, resolvedBeads),
		namePool: pool,
		tmux:     t,
		townRoot: townRoot,
	}
}

// GetNamePool returns the manager's name pool for external use (e.g., pool init).
func (m *Manager) GetNamePool() *NamePool {
	return m.namePool
}

// lockPolecat acquires an exclusive file lock for a specific polecat.
// This prevents concurrent gt processes from racing on the same polecat's
// filesystem operations (Add, Remove, RepairWorktree).
// Caller must defer fl.Unlock().
func (m *Manager) lockPolecat(name string) (*flock.Flock, error) {
	lockDir := filepath.Join(m.rig.Path, ".runtime", "locks")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		return nil, fmt.Errorf("creating lock dir: %w", err)
	}
	lockPath := filepath.Join(lockDir, fmt.Sprintf("polecat-%s.lock", name))
	fl := flock.New(lockPath)
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("acquiring polecat lock for %s: %w", name, err)
	}
	return fl, nil
}

// lockPool acquires an exclusive file lock for name pool operations.
// This prevents concurrent gt processes from racing on AllocateName/ReconcilePool.
// Caller must defer fl.Unlock().
func (m *Manager) lockPool() (*flock.Flock, error) {
	lockDir := filepath.Join(m.rig.Path, ".runtime", "locks")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		return nil, fmt.Errorf("creating lock dir: %w", err)
	}
	lockPath := filepath.Join(lockDir, "polecat-pool.lock")
	fl := flock.New(lockPath)
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("acquiring pool lock: %w", err)
	}
	return fl, nil
}

// CheckDoltHealth verifies that the Dolt database is reachable before spawning.
// Returns an error if Dolt exists but is unhealthy after retries.
// Returns nil if beads is not configured (test/setup environments).
// If read-only errors persist after retries, attempts server recovery (gt-chx92).
// Fails fast on configuration/initialization errors (gt-2ra).
func (m *Manager) CheckDoltHealth() error {
	var lastErr error
	for attempt := 1; attempt <= doltMaxRetries; attempt++ {
		// Use a lightweight beads operation to verify Dolt is responsive
		_, err := m.beads.Show("__health_check_nonexistent__")
		if err == nil || errors.Is(err, beads.ErrNotFound) || strings.Contains(err.Error(), "not found") {
			// Dolt is healthy — a "not found" error means the DB responded
			return nil
		}
		// Optimistic lock errors mean Dolt is alive but busy with concurrent writes
		if isDoltOptimisticLockError(err) {
			return nil
		}
		// If beads isn't configured at all, skip the health check
		if strings.Contains(err.Error(), "does not exist") || errors.Is(err, beads.ErrNotInstalled) {
			return nil
		}
		// Fail fast on config/init errors — retrying won't help (gt-2ra, gas-tc4)
		if isDoltConfigError(err) {
			return fmt.Errorf("%w: %v\n\nRecovery: run 'gt doctor --fix' to repair database configuration.\n"+
				"If that doesn't help, try: bd init --force --server", ErrDoltUnhealthy, err)
		}
		lastErr = err
		if attempt < doltMaxRetries {
			backoff := doltBackoff(attempt)
			style.PrintWarning("Dolt health check attempt %d failed, retrying in %v...", attempt, backoff)
			time.Sleep(backoff)
		}
	}

	// If the persistent failure looks like read-only, attempt server recovery
	// before giving up. This is the gt-level recovery path (gt-chx92).
	if lastErr != nil && doltserver.IsReadOnlyError(lastErr.Error()) {
		if recoverErr := doltserver.RecoverReadOnly(m.townRoot); recoverErr == nil {
			// Recovery succeeded — verify health once more
			_, err := m.beads.Show("__health_check_nonexistent__")
			if err == nil || errors.Is(err, beads.ErrNotFound) || strings.Contains(err.Error(), "not found") {
				return nil
			}
		}
	}

	return fmt.Errorf("%w: %v\n\nRecovery: run 'gt doctor --fix' to diagnose and repair Dolt configuration", ErrDoltUnhealthy, lastErr)
}

// CheckDoltServerCapacity verifies the Dolt server has capacity for new connections.
// This is an admission control gate: if the server is near its max_connections limit,
// spawning another polecat (which will make many bd calls) could overwhelm it.
// Returns nil if capacity is available, ErrDoltAtCapacity if the server is overloaded.
// Fails closed if the check errors — a server that can't report capacity is likely
// already under stress (gt-lfc0d).
func (m *Manager) CheckDoltServerCapacity() error {
	// NOTE: Prior to gt-lph, this method called workspace.Find to locate townRoot,
	// which could fail and silently skip the capacity check (return nil). Now that
	// m.townRoot is computed deterministically at Manager construction, errors from
	// HasConnectionCapacity always propagate — this is intentional. A server that
	// can't report capacity is likely under stress, and silently passing was a
	// latent bug that allowed connection storms under load (gt-lfc0d).
	hasCapacity, active, err := doltserver.HasConnectionCapacity(m.townRoot)
	if err != nil {
		// Fail closed: if we can't check capacity, the server may be overloaded.
		// Proceeding optimistically caused read-only mode under load (gt-lfc0d).
		return fmt.Errorf("%w: capacity check failed: %v", ErrDoltAtCapacity, err)
	}

	if !hasCapacity {
		return fmt.Errorf("%w: %d active connections (server near limit)", ErrDoltAtCapacity, active)
	}

	return nil
}

// createAgentBeadWithRetry wraps CreateOrReopenAgentBead with retry logic.
// For transient Dolt failures (server exists but write fails), retries with backoff
// and fails hard — a polecat without an agent bead is untrackable.
// If beads is not configured (no .beads directory), warns and returns nil
// since this indicates a test/setup environment, not a Dolt failure.
// Fails fast on configuration/initialization errors (gt-2ra) — these are not
// transient and retrying them wastes ~3 minutes for identical failures.
func (m *Manager) createAgentBeadWithRetry(agentID string, fields *beads.AgentFields) error {
	var lastErr error
	for attempt := 1; attempt <= doltMaxRetries; attempt++ {
		_, err := m.beads.CreateOrReopenAgentBead(agentID, agentID, fields)
		if err == nil {
			return nil
		}
		lastErr = err
		// If beads directory doesn't exist, this is a test/setup env — warn only
		if strings.Contains(err.Error(), "does not exist") || errors.Is(err, beads.ErrNotInstalled) {
			style.PrintWarning("could not create agent bead (beads not configured): %v", err)
			return nil
		}
		// Fail fast on config/init errors — retrying won't help (gt-2ra)
		if isDoltConfigError(err) {
			return fmt.Errorf("agent bead creation failed (DB not initialized — not retrying): %w", err)
		}
		if attempt < doltMaxRetries {
			backoff := doltBackoff(attempt)
			style.PrintWarning("agent bead creation attempt %d failed, retrying in %v: %v", attempt, backoff, err)
			time.Sleep(backoff)
		}
	}
	return fmt.Errorf("creating agent bead after %d attempts: %w", doltMaxRetries, lastErr)
}

// SetAgentStateWithRetry wraps SetAgentState with retry logic.
// Returns an error after exhausting retries, but callers may choose to warn
// rather than fail — e.g., in StartSession where the tmux session is already
// running and failing hard would orphan it. Agent state is a monitoring
// concern, not a correctness requirement.
// Fails fast on configuration/initialization errors (gt-2ra).
func (m *Manager) SetAgentStateWithRetry(name string, state string) error {
	var lastErr error
	for attempt := 1; attempt <= doltStateRetries; attempt++ {
		err := m.SetAgentState(name, state)
		if err == nil {
			return nil
		}
		lastErr = err
		// Fail fast on config/init errors — retrying won't help (gt-2ra)
		if isDoltConfigError(err) {
			return fmt.Errorf("setting agent state failed (DB not initialized — not retrying): %w", err)
		}
		if attempt < doltStateRetries {
			backoff := doltBackoff(attempt)
			style.PrintWarning("SetAgentState attempt %d failed, retrying in %v: %v", attempt, backoff, err)
			time.Sleep(backoff)
		}
	}
	return fmt.Errorf("setting agent state after %d attempts: %w", doltStateRetries, lastErr)
}

// assigneeID returns the beads assignee identifier for a polecat.
// Format: "rig/polecats/polecatName" (e.g., "gastown/polecats/Toast")
func (m *Manager) assigneeID(name string) string {
	return fmt.Sprintf("%s/polecats/%s", m.rig.Name, name)
}

// agentBeadID returns the agent bead ID for a polecat.
// Format: "<prefix>-<rig>-polecat-<name>" (e.g., "gt-gastown-polecat-Toast", "bd-beads-polecat-obsidian")
// The prefix is looked up from routes.jsonl to support rigs with custom prefixes.
// Uses the town root computed at Manager construction for deterministic IDs
// regardless of call site (gt-lph).
func (m *Manager) agentBeadID(name string) string {
	prefix := beads.GetPrefixForRig(m.townRoot, m.rig.Name)
	return beads.PolecatBeadIDWithPrefix(prefix, m.rig.Name, name)
}

// getCleanupStatusFromBead reads the cleanup_status from the polecat's agent bead.
// Returns CleanupUnknown if the bead doesn't exist or has no cleanup_status.
// ZFC #10: This is the ZFC-compliant way to check if removal is safe.
func (m *Manager) getCleanupStatusFromBead(name string) CleanupStatus {
	agentID := m.agentBeadID(name)
	_, fields, err := m.beads.GetAgentBead(agentID)
	if err != nil || fields == nil {
		return CleanupUnknown
	}
	if fields.CleanupStatus == "" {
		return CleanupUnknown
	}
	return CleanupStatus(fields.CleanupStatus)
}

// checkCleanupStatus validates the cleanup status against removal safety rules.
// Returns an error if removal should be blocked based on the status.
// force=true: allow has_uncommitted and has_unpushed, block has_stash
// force=false: block all non-clean statuses
func (m *Manager) checkCleanupStatus(name string, status CleanupStatus, force bool) error {
	// Clean status is always safe
	if status.IsSafe() {
		return nil
	}

	// With force, uncommitted changes can be bypassed
	if force && status.CanForceRemove() {
		return nil
	}

	// Map status to appropriate error
	switch status {
	case CleanupUncommitted:
		return &UncommittedWorkError{
			PolecatName: name,
			Status:      &git.UncommittedWorkStatus{HasUncommittedChanges: true},
		}
	case CleanupStash:
		return &UncommittedWorkError{
			PolecatName: name,
			Status:      &git.UncommittedWorkStatus{StashCount: 1},
		}
	case CleanupUnpushed:
		return &UncommittedWorkError{
			PolecatName: name,
			Status:      &git.UncommittedWorkStatus{UnpushedCommits: 1},
		}
	default:
		// Unknown status - be conservative and block
		return &UncommittedWorkError{
			PolecatName: name,
			Status:      &git.UncommittedWorkStatus{HasUncommittedChanges: true},
		}
	}
}

// repoBase returns the git directory and Git object to use for worktree operations.
// Prefers the shared bare repo (.repo.git) if it exists, otherwise falls back to mayor/rig.
// The bare repo architecture allows all worktrees (refinery, polecats) to share branch visibility.
func (m *Manager) repoBase() (*git.Git, error) {
	// First check for shared bare repo (new architecture)
	bareRepoPath := filepath.Join(m.rig.Path, ".repo.git")
	if info, err := os.Stat(bareRepoPath); err == nil && info.IsDir() {
		// Bare repo exists - use it
		return git.NewGitWithDir(bareRepoPath, ""), nil
	}

	// Fall back to mayor/rig (legacy architecture)
	mayorPath := filepath.Join(m.rig.Path, "mayor", "rig")
	if _, err := os.Stat(mayorPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("no repo base found (neither .repo.git nor mayor/rig exists)")
	}
	return git.NewGit(mayorPath), nil
}

// polecatDir returns the parent directory for a polecat.
// This is polecats/<name>/ - the polecat's home directory.
func (m *Manager) polecatDir(name string) string {
	return filepath.Join(m.rig.Path, "polecats", name)
}

// pendingPath returns the path of the allocation reservation marker for a name.
// Written inside the pool lock by AllocateName; removed by AddWithOptions after
// the polecat directory is created. Prevents concurrent processes from allocating
// the same name during the window between pool save and directory creation.
func (m *Manager) pendingPath(name string) string {
	return filepath.Join(m.rig.Path, "polecats", name+".pending")
}

// clonePath returns the path where the git worktree lives.
// New structure: polecats/<name>/<rigname>/ - gives LLMs recognizable repo context.
// Falls back to old structure: polecats/<name>/ for backward compatibility.
func (m *Manager) clonePath(name string) string {
	// New structure: polecats/<name>/<rigname>/
	newPath := filepath.Join(m.rig.Path, "polecats", name, m.rig.Name)
	if info, err := os.Stat(newPath); err == nil && info.IsDir() {
		return newPath
	}

	// Old structure: polecats/<name>/ (backward compat)
	oldPath := filepath.Join(m.rig.Path, "polecats", name)
	if info, err := os.Stat(oldPath); err == nil && info.IsDir() {
		// Check if this is actually a git worktree (has .git file or dir)
		gitPath := filepath.Join(oldPath, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			return oldPath
		}
	}

	// Default to new structure for new polecats
	return newPath
}

// ClonePath returns the path to a polecat's git worktree.
func (m *Manager) ClonePath(name string) string {
	return m.clonePath(name)
}

// exists checks if a polecat exists.
func (m *Manager) exists(name string) bool {
	_, err := os.Stat(m.polecatDir(name))
	return err == nil
}

// AddOptions configures polecat creation.
type AddOptions struct {
	HookBead   string // Bead ID to set as hook_bead at spawn time (atomic assignment)
	BaseBranch string // Override base branch for worktree (e.g., "origin/integration/gt-epic")
	// ResumeBranch reuses an existing branch (typically a PR head) for the polecat
	// worktree instead of creating a fresh polecat/<name>/<bead>@<ts> branch (gh#3602).
	// When set, the polecat's branch IS this branch — pushes go back to the same ref,
	// updating the existing PR. Mutually exclusive with BaseBranch (resume implies its
	// own start point). When empty, normal fresh-branch behavior is used.
	ResumeBranch string
}

// Add creates a new polecat as a git worktree from the repo base.
// Uses the shared bare repo (.repo.git) if available, otherwise mayor/rig.
// This is much faster than a full clone and shares objects with all worktrees.
// buildBranchName creates a branch name using the configured template or default format.
// Supported template variables:
// - {user}: git config user.name
// - {year}: current year (YY format)
// - {month}: current month (MM format)
// - {name}: polecat name
// - {issue}: issue ID (without prefix)
// - {description}: sanitized issue title
// - {timestamp}: unique timestamp
//
// If no template is configured or template is empty, uses default format:
// - polecat/{name}/{issue}@{timestamp} when issue is available
// - polecat/{name}-{timestamp} otherwise
func (m *Manager) buildBranchName(name, issue string) string {
	template := m.rig.GetStringConfig("polecat_branch_template")

	// No template configured - use default behavior for backward compatibility
	if template == "" {
		timestamp := strconv.FormatInt(time.Now().UnixMilli(), 36)
		if issue != "" {
			return fmt.Sprintf("polecat/%s/%s@%s", name, issue, timestamp)
		}
		return fmt.Sprintf("polecat/%s-%s", name, timestamp)
	}

	// Build template variables
	vars := make(map[string]string)

	// {user} - from git config user.name
	if userName, err := m.git.ConfigGet("user.name"); err == nil && userName != "" {
		vars["{user}"] = userName
	} else {
		vars["{user}"] = "unknown"
	}

	// {year} and {month}
	now := time.Now()
	vars["{year}"] = now.Format("06")  // YY format
	vars["{month}"] = now.Format("01") // MM format

	// {name}
	vars["{name}"] = name

	// {timestamp}
	vars["{timestamp}"] = strconv.FormatInt(now.UnixMilli(), 36)

	// {issue} - issue ID without prefix
	if issue != "" {
		// Strip prefix (e.g., "gt-123" -> "123")
		if idx := strings.Index(issue, "-"); idx >= 0 {
			vars["{issue}"] = issue[idx+1:]
		} else {
			vars["{issue}"] = issue
		}
	} else {
		vars["{issue}"] = ""
	}

	// {description} - try to get from beads if issue is set
	if issue != "" {
		if issueData, err := m.beads.Show(issue); err == nil && issueData.Title != "" {
			// Sanitize title for branch name: lowercase, replace spaces/special chars with hyphens
			desc := strings.ToLower(issueData.Title)
			desc = strings.Map(func(r rune) rune {
				if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
					return r
				}
				return '-'
			}, desc)
			// Remove consecutive hyphens and trim
			desc = strings.Trim(desc, "-")
			for strings.Contains(desc, "--") {
				desc = strings.ReplaceAll(desc, "--", "-")
			}
			// Limit length to keep branch names reasonable
			if len(desc) > 40 {
				desc = desc[:40]
			}
			vars["{description}"] = desc
		} else {
			vars["{description}"] = ""
		}
	} else {
		vars["{description}"] = ""
	}

	// Replace all variables in template
	result := template
	for key, value := range vars {
		result = strings.ReplaceAll(result, key, value)
	}

	// Clean up any remaining empty segments (e.g., "adam///" -> "adam")
	parts := strings.Split(result, "/")
	cleanParts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			cleanParts = append(cleanParts, part)
		}
	}
	result = strings.Join(cleanParts, "/")

	return result
}

// Polecat state is derived from beads assignee field, not state.json.
//
// Branch naming: Each polecat run gets a unique branch (polecat/<name>-<timestamp>).
// This prevents drift issues from stale branches and ensures a clean starting state.
// Old branches are ephemeral and never pushed to origin.
func (m *Manager) Add(name string) (*Polecat, error) {
	return m.AddWithOptions(name, AddOptions{})
}

// AllocateAndAdd atomically allocates a name and creates a polecat.
// This eliminates the TOCTOU race between AllocateName and AddWithOptions
// (GH#2215) by holding the pool lock through directory creation, ensuring
// no concurrent process can allocate the same name.
func (m *Manager) AllocateAndAdd(opts AddOptions) (string, *Polecat, error) {
	// Hold pool lock across allocation + directory creation to close the
	// race window where a concurrent AllocateName could miss the pending
	// marker and reallocate the same name.
	poolLock, err := m.lockPool()
	if err != nil {
		return "", nil, err
	}

	m.reconcilePoolInternal()

	name, err := m.namePool.Allocate()
	if err != nil {
		_ = poolLock.Unlock()
		return "", nil, err
	}

	if err := m.namePool.Save(); err != nil {
		_ = poolLock.Unlock()
		return "", nil, fmt.Errorf("saving pool state: %w", err)
	}

	// Acquire per-polecat lock while still holding pool lock
	polecatLock, err := m.lockPolecat(name)
	if err != nil {
		_ = poolLock.Unlock()
		return "", nil, err
	}

	// Create polecat directory while holding both locks
	polecatDir := m.polecatDir(name)
	if err := os.MkdirAll(polecatDir, 0755); err != nil {
		_ = polecatLock.Unlock()
		_ = poolLock.Unlock()
		return "", nil, fmt.Errorf("creating polecat dir: %w", err)
	}

	// Kill any lingering tmux session for this name (gt-pqf9x)
	if m.tmux != nil {
		sessionName := session.PolecatSessionName(session.PrefixFor(m.rig.Name), name)
		if alive, _ := m.tmux.HasSession(sessionName); alive {
			_ = m.tmux.KillSessionWithProcesses(sessionName)
		}
	}

	// Directory exists — pool lock can be released. No concurrent AllocateName
	// can reallocate this name because reconcilePoolInternal will see the directory.
	_ = poolLock.Unlock()

	// Continue with the rest of AddWithOptions under the polecat lock only.
	// addWithOptionsLocked expects the polecat directory to already exist
	// and the polecat lock to be held by the caller.
	p, err := m.addWithOptionsLocked(name, opts, polecatDir)
	_ = polecatLock.Unlock()
	if err != nil {
		return "", nil, err
	}
	return name, p, nil
}

// addWithOptionsLocked performs the expensive parts of polecat creation
// (worktree, beads, settings) after the directory has been created.
// Caller MUST hold the polecat lock and have already created polecatDir.
func (m *Manager) addWithOptionsLocked(name string, opts AddOptions, polecatDir string) (_ *Polecat, retErr error) {
	defer func() { telemetry.RecordPolecatSpawn(context.Background(), name, retErr) }()

	// Pre-check: Verify sufficient disk space before expensive worktree creation.
	if level, msg, err := util.CheckDiskSpace(m.rig.Path); err == nil && level == util.DiskSpaceCritical {
		return nil, fmt.Errorf("%w: %s", ErrDiskSpaceLow, msg)
	}

	clonePath := filepath.Join(polecatDir, m.rig.Name)
	branchName := m.buildBranchName(name, opts.HookBead)
	if opts.ResumeBranch != "" {
		branchName = opts.ResumeBranch
	}

	// Track resources created for rollback on error.
	var worktreeCreated bool
	cleanupOnError := func() {
		aid := m.agentBeadID(name)
		_ = m.beads.ResetAgentBeadForReuse(aid, "spawn rollback")

		if worktreeCreated {
			if rg, repoErr := m.repoBase(); repoErr == nil {
				_ = rg.WorktreeRemove(clonePath, true)
			}
		}

		_ = os.RemoveAll(polecatDir)

		m.namePool.Release(name)
		_ = m.namePool.Save()
	}

	repoGit, err := m.repoBase()
	if err != nil {
		cleanupOnError()
		return nil, fmt.Errorf("finding repo base: %w", err)
	}

	if err := repoGit.Fetch("origin"); err != nil {
		style.PrintWarning("could not fetch origin: %v", err)
	}

	if opts.ResumeBranch != "" {
		// Resume an existing branch (gh#3602). Make sure we have the latest tip
		// for the named branch, then attach the worktree directly. WorktreeAddExistingForce
		// handles the case where another worktree previously had this branch checked out.
		if err := repoGit.FetchBranch("origin", opts.ResumeBranch); err != nil {
			style.PrintWarning("could not fetch resume branch %s: %v", opts.ResumeBranch, err)
		}
		if err := repoGit.WorktreeAddExistingForce(clonePath, opts.ResumeBranch); err != nil {
			cleanupOnError()
			return nil, fmt.Errorf("creating worktree on existing branch %s: %w", opts.ResumeBranch, err)
		}
		worktreeCreated = true
	} else {
		var startPoint string
		if opts.BaseBranch != "" {
			startPoint = opts.BaseBranch
		} else {
			defaultBranch := "main"
			if rigCfg, err := rig.LoadRigConfig(m.rig.Path); err == nil && rigCfg.DefaultBranch != "" {
				defaultBranch = rigCfg.DefaultBranch
			}
			startPoint = fmt.Sprintf("origin/%s", defaultBranch)
		}

		if exists, err := repoGit.RefExists(startPoint); err != nil {
			cleanupOnError()
			return nil, fmt.Errorf("checking ref %s: %w", startPoint, err)
		} else if !exists {
			cleanupOnError()
			return nil, fmt.Errorf("configured default_branch not found as %s in bare repo\n\n"+
				"Possible causes:\n"+
				"  - Branch doesn't exist on the remote (create it there first)\n"+
				"  - default_branch is misconfigured (check %s/config.json)\n"+
				"  - Bare repo fetch failed (try: git -C %s fetch origin)\n\n"+
				"Run 'gt doctor' to diagnose.",
				startPoint, m.rig.Path, filepath.Join(m.rig.Path, ".repo.git"))
		}

		if err := repoGit.WorktreeAddFromRef(clonePath, branchName, startPoint); err != nil {
			cleanupOnError()
			return nil, fmt.Errorf("creating worktree from %s: %w", startPoint, err)
		}
		worktreeCreated = true
	}

	// Provision CLAUDE.md with gt done instructions (same as AddWithOptions path).
	lockedRigName := filepath.Base(m.rig.Path)
	if _, err := templates.CreatePolecatCLAUDEmd(clonePath, lockedRigName, name); err != nil {
		style.PrintWarning("could not provision polecat CLAUDE.md: %v", err)
	}

	if err := m.setupSharedBeads(clonePath); err != nil {
		cleanupOnError()
		return nil, fmt.Errorf("setting up shared beads: %w (polecat cannot submit MRs without shared beads)", err)
	}

	if err := beads.ProvisionPrimeMDForWorktree(clonePath); err != nil {
		style.PrintWarning("could not provision PRIME.md: %v", err)
	}

	if err := rig.CopyOverlay(m.rig.Path, clonePath); err != nil {
		style.PrintWarning("could not copy overlay files: %v", err)
	}

	if err := rig.EnsureLocalExcludePatterns(clonePath); err != nil {
		style.PrintWarning("could not update local git excludes: %v", err)
	}

	townRoot := filepath.Dir(m.rig.Path)
	runtimeConfig := config.ResolveRoleAgentConfig("polecat", townRoot, m.rig.Path)
	polecatSettingsDir := config.RoleSettingsDir("polecat", m.rig.Path)
	if err := runtime.EnsureSettingsForRole(polecatSettingsDir, clonePath, "polecat", runtimeConfig); err != nil {
		style.PrintWarning("could not install runtime settings: %v", err)
	}

	if err := rig.RunSetupHooks(m.rig.Path, clonePath); err != nil {
		style.PrintWarning("could not run setup hooks: %v", err)
	}

	agentID := m.agentBeadID(name)
	if err = m.createAgentBeadWithRetry(agentID, &beads.AgentFields{
		RoleType:   "polecat",
		Rig:        m.rig.Name,
		AgentState: "spawning",
		HookBead:   opts.HookBead,
	}); err != nil {
		cleanupOnError()
		return nil, fmt.Errorf("agent bead required for polecat tracking: %w", err)
	}

	now := time.Now()
	polecat := &Polecat{
		Name:      name,
		Rig:       m.rig.Name,
		State:     StateWorking,
		ClonePath: clonePath,
		Branch:    branchName,
		CreatedAt: now,
		UpdatedAt: now,
	}

	return polecat, nil
}

// AddWithOptions creates a new polecat with the specified options.
// This allows setting hook_bead atomically at creation time, avoiding
// cross-beads routing issues when slinging work to new polecats.
func (m *Manager) AddWithOptions(name string, opts AddOptions) (_ *Polecat, retErr error) {
	defer func() { telemetry.RecordPolecatSpawn(context.Background(), name, retErr) }()
	// Acquire per-polecat file lock to prevent concurrent Add/Remove/Repair races
	fl, err := m.lockPolecat(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = fl.Unlock() }()

	if m.exists(name) {
		return nil, ErrPolecatExists
	}

	// Pre-check: Verify sufficient disk space before creating worktree.
	// Spawning a polecat creates a git worktree, copies overlay files, and writes
	// beads state — all requiring disk I/O. If the disk is nearly full, fail early
	// with a clear message rather than leaving a half-created polecat.
	// See: disk-space-resilience — 5 polecats died silently on disk exhaustion.
	if level, msg, err := util.CheckDiskSpace(m.rig.Path); err == nil && level == util.DiskSpaceCritical {
		return nil, fmt.Errorf("%w: %s", ErrDiskSpaceLow, msg)
	}

	// New structure: polecats/<name>/<rigname>/ for LLM ergonomics
	// The polecat's home dir is polecats/<name>/, worktree is polecats/<name>/<rigname>/
	polecatDir := m.polecatDir(name)
	clonePath := filepath.Join(polecatDir, m.rig.Name)

	// Build branch name using configured template or default format.
	// When resuming an existing branch (gh#3602), use that branch's name directly
	// so pushes go back to the same ref and update the existing PR.
	branchName := m.buildBranchName(name, opts.HookBead)
	if opts.ResumeBranch != "" {
		branchName = opts.ResumeBranch
	}

	// Create polecat directory (polecats/<name>/)
	if err := os.MkdirAll(polecatDir, 0755); err != nil {
		return nil, fmt.Errorf("creating polecat dir: %w", err)
	}

	// Directory created — remove the allocation reservation marker.
	// reconcilePoolInternal will now find the directory directly and treat the
	// name as in-use without needing the .pending file.
	_ = os.Remove(m.pendingPath(name))

	// Track resources created for rollback on error.
	// AddWithOptions creates several resources in sequence (directory, worktree,
	// agent bead); on failure, all created resources must be cleaned up to prevent
	// leaking names, orphaning beads, or leaving stale worktree registrations.
	// See: gt-2vs22
	var worktreeCreated bool
	cleanupOnError := func() {
		// Best-effort reset of agent bead (may have been partially created
		// by a failed createAgentBeadWithRetry)
		aid := m.agentBeadID(name)
		_ = m.beads.ResetAgentBeadForReuse(aid, "spawn rollback")

		// Remove git worktree registration if worktree was successfully added.
		// Must happen before directory removal so git can clean up properly.
		if worktreeCreated {
			if rg, repoErr := m.repoBase(); repoErr == nil {
				_ = rg.WorktreeRemove(clonePath, true)
			}
		}

		// Remove polecat directory
		_ = os.RemoveAll(polecatDir)

		// Release name back to pool so it can be reallocated immediately
		// rather than waiting for the next reconcile cycle.
		m.namePool.Release(name)
		_ = m.namePool.Save()
	}

	// Get the repo base (bare repo or mayor/rig)
	repoGit, err := m.repoBase()
	if err != nil {
		cleanupOnError()
		return nil, fmt.Errorf("finding repo base: %w", err)
	}

	// Fetch latest from origin to ensure worktree starts from up-to-date code
	if err := repoGit.Fetch("origin"); err != nil {
		// Non-fatal - proceed with potentially stale code
		style.PrintWarning("could not fetch origin: %v", err)
	}

	if opts.ResumeBranch != "" {
		// Resume an existing branch (gh#3602): attach the worktree directly to the
		// named branch. WorktreeAddExistingForce tolerates the branch being checked
		// out elsewhere (stale worktree), and the explicit fetch ensures we have
		// the latest tip before checkout.
		if err := repoGit.FetchBranch("origin", opts.ResumeBranch); err != nil {
			style.PrintWarning("could not fetch resume branch %s: %v", opts.ResumeBranch, err)
		}
		if err := repoGit.WorktreeAddExistingForce(clonePath, opts.ResumeBranch); err != nil {
			cleanupOnError()
			return nil, fmt.Errorf("creating worktree on existing branch %s: %w", opts.ResumeBranch, err)
		}
		worktreeCreated = true
	} else {
		// Determine the start point for the new worktree
		var startPoint string
		if opts.BaseBranch != "" {
			startPoint = opts.BaseBranch
		} else {
			defaultBranch := "main"
			if rigCfg, err := rig.LoadRigConfig(m.rig.Path); err == nil && rigCfg.DefaultBranch != "" {
				defaultBranch = rigCfg.DefaultBranch
			}
			startPoint = fmt.Sprintf("origin/%s", defaultBranch)
		}

		// Validate that startPoint ref exists before attempting worktree creation
		if exists, err := repoGit.RefExists(startPoint); err != nil {
			cleanupOnError()
			return nil, fmt.Errorf("checking ref %s: %w", startPoint, err)
		} else if !exists {
			cleanupOnError()
			return nil, fmt.Errorf("configured default_branch not found as %s in bare repo\n\n"+
				"Possible causes:\n"+
				"  - Branch doesn't exist on the remote (create it there first)\n"+
				"  - default_branch is misconfigured (check %s/config.json)\n"+
				"  - Bare repo fetch failed (try: git -C %s fetch origin)\n\n"+
				"Run 'gt doctor' to diagnose.",
				startPoint, m.rig.Path, filepath.Join(m.rig.Path, ".repo.git"))
		}

		// Always create fresh branch - unique name guarantees no collision
		// git worktree add -b polecat/<name>-<timestamp> <path> <startpoint>
		// Worktree goes in polecats/<name>/<rigname>/ for LLM ergonomics
		if err := repoGit.WorktreeAddFromRef(clonePath, branchName, startPoint); err != nil {
			cleanupOnError()
			return nil, fmt.Errorf("creating worktree from %s: %w", startPoint, err)
		}
		worktreeCreated = true
	}

	// Provision CLAUDE.md with gt done instructions and lifecycle context.
	// This is the primary mechanism for polecats to learn about completion —
	// the file persists across compaction and session restarts (unlike ephemeral
	// gt prime output which scrolls past and gets lost).
	rigName := filepath.Base(m.rig.Path)
	if _, err := templates.CreatePolecatCLAUDEmd(clonePath, rigName, name); err != nil {
		// Non-fatal — polecat can still learn via gt prime hook
		style.PrintWarning("could not provision polecat CLAUDE.md: %v", err)
	}

	// Set up shared beads: polecat uses rig's .beads via redirect file.
	// This eliminates git sync overhead - all polecats share one database.
	// Fatal: without shared beads, gt done writes MR beads to a local .beads/
	// that the Refinery never reads, causing the merge queue to stay empty.
	if err := m.setupSharedBeads(clonePath); err != nil {
		cleanupOnError()
		return nil, fmt.Errorf("setting up shared beads: %w (polecat cannot submit MRs without shared beads)", err)
	}

	// Provision PRIME.md with Gas Town context for this worker.
	// This is the fallback if SessionStart hook fails - ensures polecats
	// always have GUPP and essential Gas Town context.
	if err := beads.ProvisionPrimeMDForWorktree(clonePath); err != nil {
		// Non-fatal - polecat can still work via hook, warn but don't fail
		style.PrintWarning("could not provision PRIME.md: %v", err)
	}

	// Copy overlay files from .runtime/overlay/ to polecat root.
	// This allows services to have .env and other config files at their root.
	if err := rig.CopyOverlay(m.rig.Path, clonePath); err != nil {
		// Non-fatal - log warning but continue
		style.PrintWarning("could not copy overlay files: %v", err)
	}

	// Keep worktree runtime ignores local so the tracked tree stays clean.
	if err := rig.EnsureLocalExcludePatterns(clonePath); err != nil {
		style.PrintWarning("could not update local git excludes: %v", err)
	}

	// Install runtime settings in the shared polecats parent directory.
	// Settings are passed to Claude Code via --settings flag.
	townRoot := filepath.Dir(m.rig.Path)
	runtimeConfig := config.ResolveRoleAgentConfig("polecat", townRoot, m.rig.Path)
	polecatSettingsDir := config.RoleSettingsDir("polecat", m.rig.Path)
	if err := runtime.EnsureSettingsForRole(polecatSettingsDir, clonePath, "polecat", runtimeConfig); err != nil {
		// Non-fatal - log warning but continue
		style.PrintWarning("could not install runtime settings: %v", err)
	}

	// Run setup hooks from .runtime/setup-hooks/.
	// These hooks can inject local git config, copy secrets, or perform other setup tasks.
	if err := rig.RunSetupHooks(m.rig.Path, clonePath); err != nil {
		// Non-fatal - log warning but continue
		style.PrintWarning("could not run setup hooks: %v", err)
	}

	// NOTE: Slash commands (.claude/commands/) are provisioned at town level by gt install.
	// All agents inherit them via Claude's directory traversal - no per-workspace copies needed.

	// Create or reopen agent bead for ZFC compliance (self-report state).
	// State starts as "spawning" - will be updated to "working" when Claude starts.
	// HookBead is set atomically at creation time if provided (avoids cross-beads routing issues).
	// Uses CreateOrReopenAgentBead to handle re-spawning with same name (GH #332).
	// Retries with backoff — a polecat without an agent bead is untrackable (gt-94llt7).
	agentID := m.agentBeadID(name)
	if err = m.createAgentBeadWithRetry(agentID, &beads.AgentFields{
		RoleType:   "polecat",
		Rig:        m.rig.Name,
		AgentState: "spawning",
		HookBead:   opts.HookBead, // Set atomically at spawn time
	}); err != nil {
		// Hard fail — an untrackable polecat is worse than no polecat
		cleanupOnError()
		return nil, fmt.Errorf("agent bead required for polecat tracking: %w", err)
	}

	// Return polecat with working state (transient model: polecats are spawned with work)
	// State is derived from beads, not stored in state.json
	now := time.Now()
	polecat := &Polecat{
		Name:      name,
		Rig:       m.rig.Name,
		State:     StateWorking, // Transient model: polecat spawns with work
		ClonePath: clonePath,
		Branch:    branchName,
		CreatedAt: now,
		UpdatedAt: now,
	}

	return polecat, nil
}

// Remove deletes a polecat worktree.
// If force is true, removes even with uncommitted changes and unpushed commits.
// Stashes still block removal with force (use nuclear=true to bypass all checks).
func (m *Manager) Remove(name string, force bool) error {
	return m.RemoveWithOptions(name, force, false, false)
}

// RemoveWithOptions deletes a polecat worktree with explicit control over safety checks.
// force=true: bypass uncommitted changes and unpushed commits check
// nuclear=true: bypass ALL safety checks including stashes
// selfNuke=true: bypass cwd-in-worktree check (for polecat deleting its own worktree)
//
// ZFC #10: Uses cleanup_status from agent bead if available (polecat self-report),
// falls back to git check for backward compatibility.
func (m *Manager) RemoveWithOptions(name string, force, nuclear, selfNuke bool) (retErr error) {
	defer func() { telemetry.RecordPolecatRemove(context.Background(), name, retErr) }()
	// Acquire per-polecat file lock to prevent concurrent Remove races
	fl, err := m.lockPolecat(name)
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	if !m.exists(name) {
		return ErrPolecatNotFound
	}

	// Clone path is where the git worktree lives (new or old structure)
	clonePath := m.clonePath(name)
	// Polecat dir is the parent directory (polecats/<name>/)
	polecatDir := m.polecatDir(name)

	// Check for uncommitted work unless bypassed
	if !nuclear {
		// ZFC #10: First try to read cleanup_status from agent bead
		// This is the ZFC-compliant path - trust what the polecat reported
		cleanupStatus := m.getCleanupStatusFromBead(name)

		if cleanupStatus != CleanupUnknown {
			// ZFC path: Use polecat's self-reported status
			if err := m.checkCleanupStatus(name, cleanupStatus, force); err != nil {
				return err
			}
		} else {
			// Fallback path: Check git directly (for polecats that haven't reported yet)
			polecatGit := git.NewGit(clonePath)
			status, err := polecatGit.CheckUncommittedWork()
			if err == nil && !status.Clean() {
				if force {
					// Force mode: bypass uncommitted changes and unpushed commits.
					// Only block on stashes, which represent intentional work-in-progress.
					if status.StashCount > 0 {
						return &UncommittedWorkError{PolecatName: name, Status: status}
					}
				} else {
					return &UncommittedWorkError{PolecatName: name, Status: status}
				}
			}
		}
	}

	// Even nuclear mode must not delete worktrees with unmerged MRs.
	// The nuclear flag bypasses git-status checks (needed for self-nuke)
	// but MR status is a higher-level concern that should always be checked.
	if !force {
		agentID := m.agentBeadID(name)
		_, fields, aErr := m.beads.GetAgentBead(agentID)
		if aErr == nil && fields != nil && fields.ActiveMR != "" {
			mrBead, mrErr := m.beads.Show(fields.ActiveMR)
			if mrErr == nil && mrBead != nil && beads.IssueStatus(mrBead.Status).BlocksRemoval() {
				return fmt.Errorf("cannot remove polecat %s: MR %s is still open in merge queue\nRefinery will process the MR and clean up after merge\nUse --force to override (risks data loss)", name, fields.ActiveMR)
			}
		}
	}

	// Reset agent bead FIRST, before any filesystem operations.
	// This prevents a race where a concurrent sling allocates the same name,
	// sets hook_bead, and then has it cleared by this cleanup. By resetting
	// the agent bead first (clearing fields, setting agent_state="nuked"),
	// concurrent slings see a clean bead and CreateOrReopenAgentBead can
	// simply update it without needing close/reopen (which fails on Dolt).
	// See gt-14b8o: close/reopen cycle breaks on Dolt backend.
	agentID := m.agentBeadID(name)
	if err := m.beads.ResetAgentBeadForReuse(agentID, "polecat removed"); err != nil {
		// Only log if not "not found" - it's ok if it doesn't exist
		if !errors.Is(err, beads.ErrNotFound) {
			style.PrintWarning("could not reset agent bead %s: %v", agentID, err)
		}
	}

	// Unassign any work beads still pointing at this polecat (gt-e4u1).
	// Without this, beads remain assigned to a ghost polecat (status in_progress,
	// assignee set) after removal, permanently stuck with no one working on them.
	m.unassignWorkBeads(name)

	// Check if user's shell is cd'd into the worktree (prevents broken shell)
	// This check runs unless selfNuke=true (polecat deleting its own worktree).
	// When a polecat calls `gt done`, it's inside its worktree by design - the session
	// will be killed immediately after, so breaking the shell is expected and harmless.
	// See: https://github.com/steveyegge/gastown/issues/942
	if !selfNuke {
		cwd, cwdErr := os.Getwd()
		if cwdErr == nil {
			// Normalize paths for comparison
			cwdAbs, absErr1 := filepath.Abs(cwd)
			cloneAbs, absErr2 := filepath.Abs(clonePath)
			polecatAbs, absErr3 := filepath.Abs(polecatDir)

			if absErr1 != nil || absErr2 != nil || absErr3 != nil {
				// If we can't resolve paths, refuse to nuke for safety
				return fmt.Errorf("cannot verify shell safety: failed to resolve paths")
			}

			if strings.HasPrefix(cwdAbs, cloneAbs) || strings.HasPrefix(cwdAbs, polecatAbs) {
				return fmt.Errorf("%w: your shell is in %s\n\nPlease cd elsewhere first, then retry:\n  cd ~/gt\n  gt polecat nuke %s/%s --force",
					ErrShellInWorktree, cwd, m.rig.Name, name)
			}
		}
	}

	// Best-effort: Push the polecat's branch to remote before removing the worktree.
	// This preserves committed work that hasn't been pushed yet — without this,
	// nuking a stalled polecat (e.g., after disk space recovery) permanently loses
	// any commits on the branch. The push is non-blocking: failures are warnings,
	// not errors, so nuke still proceeds. See: disk-space-resilience.
	polecatGit := git.NewGit(clonePath)
	if branch, brErr := polecatGit.CurrentBranch(); brErr == nil && branch != "" {
		pushed, unpushedCount, checkErr := polecatGit.BranchPushedToRemote(branch, "origin")
		if checkErr == nil && !pushed && unpushedCount > 0 {
			if pushErr := polecatGit.Push("origin", branch, false); pushErr != nil {
				style.PrintWarning("could not push branch %s before removal (%d unpushed commit(s)): %v",
					branch, unpushedCount, pushErr)
				style.PrintWarning("WORK AT RISK: branch %s has %d unpushed commit(s) in worktree %s",
					branch, unpushedCount, clonePath)
			}
		}
	}

	// Get repo base to remove the worktree properly
	repoGit, err := m.repoBase()
	if err != nil {
		// Best-effort: try to prune stale worktree entries from both possible repo locations.
		// This handles edge cases where the repo base is corrupted but worktree entries exist.
		bareRepoPath := filepath.Join(m.rig.Path, ".repo.git")
		if info, statErr := os.Stat(bareRepoPath); statErr == nil && info.IsDir() {
			bareGit := git.NewGitWithDir(bareRepoPath, "")
			_ = bareGit.WorktreePrune()
		}
		mayorRigPath := filepath.Join(m.rig.Path, "mayor", "rig")
		if info, statErr := os.Stat(mayorRigPath); statErr == nil && info.IsDir() {
			mayorGit := git.NewGit(mayorRigPath)
			_ = mayorGit.WorktreePrune()
		}
		// Fall back to direct removal if repo base not found
		return os.RemoveAll(polecatDir)
	}

	// Try to remove as a worktree first (use force flag for worktree removal too)
	if err := repoGit.WorktreeRemove(clonePath, force); err != nil {
		// Fall back to direct removal if worktree removal fails
		// (e.g., if this is an old-style clone, not a worktree)
		if removeErr := os.RemoveAll(clonePath); removeErr != nil {
			return fmt.Errorf("removing clone path: %w", removeErr)
		}
	} else {
		// GT-1L3MY9: git worktree remove may leave untracked directories behind.
		// Clean up any leftover files (overlay files, .beads/, setup hook outputs, etc.)
		// Use RemoveAll to handle non-empty directories with untracked files.
		_ = os.RemoveAll(clonePath)
	}

	// Also remove the parent polecat directory
	// (for new structure: polecats/<name>/ contains only polecats/<name>/<rigname>/)
	if polecatDir != clonePath {
		// GT-1L3MY9: Clean up any orphaned files at polecat level.
		// Use RemoveAll to handle non-empty directories with leftover files.
		_ = os.RemoveAll(polecatDir)
	}

	// Prune any stale worktree entries (non-fatal: cleanup only)
	_ = repoGit.WorktreePrune()

	// Verify removal succeeded (fixes #618)
	// The above removal attempts may fail silently on permissions, symlinks, or busy files
	if err := verifyRemovalComplete(polecatDir, clonePath); err != nil {
		// Log warning but don't fail - the polecat is effectively "removed" from Gas Town's perspective
		style.PrintWarning("incomplete removal for %s: %v", name, err)
	}

	// Release name back to pool if it's a pooled name (non-fatal: state file update)
	m.namePool.Release(name)
	_ = m.namePool.Save()

	return nil
}

// verifyRemovalComplete checks that polecat directories were actually removed.
// If they still exist, it attempts more aggressive cleanup and returns an error
// describing what couldn't be removed.
func verifyRemovalComplete(polecatDir, clonePath string) error {
	var remaining []string

	// Check if clone path still exists
	if _, err := os.Stat(clonePath); err == nil {
		// Try one more aggressive removal
		if removeErr := forceRemoveDir(clonePath); removeErr != nil {
			remaining = append(remaining, clonePath)
		}
	}

	// Check if polecat dir still exists (and is different from clone path)
	if polecatDir != clonePath {
		if _, err := os.Stat(polecatDir); err == nil {
			if removeErr := forceRemoveDir(polecatDir); removeErr != nil {
				remaining = append(remaining, polecatDir)
			}
		}
	}

	if len(remaining) > 0 {
		return fmt.Errorf("directories still exist after removal: %v", remaining)
	}
	return nil
}

// forceRemoveDir attempts aggressive removal of a directory.
// It handles permission issues by making files writable before removal.
func forceRemoveDir(dir string) error {
	// First try normal removal
	if err := os.RemoveAll(dir); err == nil {
		return nil
	}

	// Walk the directory and make everything writable, then try again
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Continue on error
		}
		// Make writable (0755 for dirs, 0644 for files)
		if d.IsDir() {
			//nolint:gosec // Controlled cleanup of a path inside the allocated polecat directory.
			_ = os.Chmod(path, 0755)
		} else {
			//nolint:gosec // Controlled cleanup of a path inside the allocated polecat directory.
			_ = os.Chmod(path, 0644)
		}
		return nil
	})

	// Try removal again after fixing permissions
	return os.RemoveAll(dir)
}

// AllocateName allocates a name from the name pool.
// Returns a themed pooled name (furiosa, nux, etc.) if available,
// otherwise returns an overflow name (just a number like "51").
// The rig prefix is added by SessionName to create full session names like "gt-<rig>-51".
// After allocation, kills any lingering tmux session for the name (gt-pqf9x)
// to prevent "session already running" errors when reusing names from dead polecats.
func (m *Manager) AllocateName() (string, error) {
	// Acquire pool lock to prevent concurrent allocations from racing
	fl, err := m.lockPool()
	if err != nil {
		return "", err
	}
	defer func() { _ = fl.Unlock() }()

	// Reconcile without re-acquiring the pool lock
	m.reconcilePoolInternal()

	name, err := m.namePool.Allocate()
	if err != nil {
		return "", err
	}

	if err := m.namePool.Save(); err != nil {
		return "", fmt.Errorf("saving pool state: %w", err)
	}

	// Write a reservation marker inside the pool lock scope.
	// This closes the TOCTOU window between pool save and directory creation:
	// reconcilePoolInternal uses directories as the source of truth for in-use
	// names, so a concurrent process calling AllocateName (after this lock is
	// released but before AddWithOptions creates the directory) would see the
	// name as available and reallocate it. The marker acts as a stand-in
	// directory until AddWithOptions removes it after os.MkdirAll succeeds.
	// Stale markers (process crashed before AddWithOptions) are cleaned up by
	// cleanupOrphanPolecatState after pendingMaxAge.
	if err := os.MkdirAll(filepath.Join(m.rig.Path, "polecats"), 0755); err != nil {
		return "", fmt.Errorf("creating polecats dir for reservation marker: %w", err)
	}
	if err := os.WriteFile(m.pendingPath(name), []byte(fmt.Sprintf("%d", os.Getpid())), 0644); err != nil {
		return "", fmt.Errorf("writing reservation marker: %w", err)
	}

	// Kill any lingering tmux session for this name (gt-pqf9x).
	// ReconcilePool kills sessions for names without directories, but a name
	// can be allocated after its directory was cleaned up while the tmux session
	// lingers (race between cleanup and allocation). This extra check ensures
	// no stale session blocks the new polecat's session creation.
	if m.tmux != nil {
		sessionName := session.PolecatSessionName(session.PrefixFor(m.rig.Name), name)
		if alive, _ := m.tmux.HasSession(sessionName); alive {
			_ = m.tmux.KillSessionWithProcesses(sessionName)
		}
	}

	return name, nil
}

// ReleaseName releases a name back to the pool.
// This is called when a polecat is removed.
func (m *Manager) ReleaseName(name string) {
	m.namePool.Release(name)
	_ = m.namePool.Save() // non-fatal: state file update
}

// RepairWorktree repairs a stale polecat by removing it and creating a fresh worktree.
// This is NOT for normal operation - it handles reconciliation when AllocateName
// returns a name that unexpectedly already exists (stale state recovery).
//
// The polecat starts with the latest code from origin/<default-branch>.
// The name is preserved (not released to pool) since we're repairing immediately.
// force controls whether to bypass uncommitted changes check.
//
// Branch naming: Each repair gets a unique branch (polecat/<name>-<timestamp>).
// Old branches are left for garbage collection - they're never pushed to origin.
func (m *Manager) RepairWorktree(name string, force bool) (*Polecat, error) {
	return m.RepairWorktreeWithOptions(name, force, AddOptions{})
}

// RepairWorktreeWithOptions repairs a stale polecat and creates a fresh worktree with options.
// This is NOT for normal operation - see RepairWorktree for context.
// Allows setting hook_bead atomically at repair time.
// After repair, uses new structure: polecats/<name>/<rigname>/
func (m *Manager) RepairWorktreeWithOptions(name string, force bool, opts AddOptions) (*Polecat, error) {
	// Acquire per-polecat file lock to prevent concurrent Repair/Remove races
	fl, err := m.lockPolecat(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = fl.Unlock() }()

	if !m.exists(name) {
		return nil, ErrPolecatNotFound
	}

	// Get the old clone path (may be old or new structure)
	oldClonePath := m.clonePath(name)
	polecatGit := git.NewGit(oldClonePath)

	// New clone path uses new structure
	polecatDir := m.polecatDir(name)
	newClonePath := filepath.Join(polecatDir, m.rig.Name)

	// Get the repo base (bare repo or mayor/rig)
	repoGit, err := m.repoBase()
	if err != nil {
		return nil, fmt.Errorf("finding repo base: %w", err)
	}

	// Check for uncommitted work unless forced
	if !force {
		status, err := polecatGit.CheckUncommittedWork()
		if err == nil && !status.Clean() {
			return nil, &UncommittedWorkError{PolecatName: name, Status: status}
		}
	}

	// Fetch latest from origin to ensure we have fresh commits (non-fatal: may be offline)
	_ = repoGit.Fetch("origin")

	// Ensure polecat directory exists for new structure
	if err := os.MkdirAll(polecatDir, 0755); err != nil {
		return nil, fmt.Errorf("creating polecat dir: %w", err)
	}

	// Build branch name. When resuming an existing branch (gh#3602), use that
	// branch's name directly so pushes update the existing PR.
	branchName := m.buildBranchName(name, opts.HookBead)
	if opts.ResumeBranch != "" {
		branchName = opts.ResumeBranch
	}

	tmpClonePath := newClonePath + ".repair-tmp"
	_ = os.RemoveAll(tmpClonePath) // clean up any leftover temp dir

	if opts.ResumeBranch != "" {
		// Resume an existing branch: fetch and attach the temp worktree directly
		// to the named branch instead of creating a fresh polecat/<name>/<bead>@<ts>.
		if err := repoGit.FetchBranch("origin", opts.ResumeBranch); err != nil {
			style.PrintWarning("could not fetch resume branch %s: %v", opts.ResumeBranch, err)
		}
		if err := repoGit.WorktreeAddExistingForce(tmpClonePath, opts.ResumeBranch); err != nil {
			return nil, fmt.Errorf("creating fresh worktree on existing branch %s: %w", opts.ResumeBranch, err)
		}
	} else {
		// Determine the start point for the new worktree
		var startPoint string
		if opts.BaseBranch != "" {
			startPoint = opts.BaseBranch
		} else {
			defaultBranch := "main"
			if rigCfg, err := rig.LoadRigConfig(m.rig.Path); err == nil && rigCfg.DefaultBranch != "" {
				defaultBranch = rigCfg.DefaultBranch
			}
			startPoint = fmt.Sprintf("origin/%s", defaultBranch)
		}

		// Validate that startPoint ref exists before attempting worktree creation
		if exists, err := repoGit.RefExists(startPoint); err != nil {
			return nil, fmt.Errorf("checking ref %s: %w", startPoint, err)
		} else if !exists {
			return nil, fmt.Errorf("configured default_branch not found as %s in bare repo\n\n"+
				"Possible causes:\n"+
				"  - Branch doesn't exist on the remote (create it there first)\n"+
				"  - default_branch is misconfigured (check %s/config.json)\n"+
				"  - Bare repo fetch failed (try: git -C %s fetch origin)\n\n"+
				"Run 'gt doctor' to diagnose.",
				startPoint, m.rig.Path, filepath.Join(m.rig.Path, ".repo.git"))
		}

		// Create fresh worktree to a temporary path first, so we can roll back if it fails.
		// This prevents destroying the old worktree before the new one is confirmed working.
		if err := repoGit.WorktreeAddFromRef(tmpClonePath, branchName, startPoint); err != nil {
			return nil, fmt.Errorf("creating fresh worktree from %s: %w", startPoint, err)
		}
	}

	// New worktree created successfully — now safe to remove old worktree and reset bead.
	// Remove old worktree BEFORE resetting bead to prevent name collision if a new
	// spawn sees the clean bead while the old worktree still exists.
	if err := repoGit.WorktreeRemove(oldClonePath, true); err != nil {
		// Fall back to direct removal
		if removeErr := os.RemoveAll(oldClonePath); removeErr != nil {
			// Clean up temp worktree before returning
			_ = repoGit.WorktreeRemove(tmpClonePath, true)
			_ = os.RemoveAll(tmpClonePath)
			return nil, fmt.Errorf("removing old clone path: %w", removeErr)
		}
	}

	// Reset agent bead AFTER old worktree is confirmed removed.
	// NOTE: We use ResetAgentBeadForReuse to avoid the close/reopen cycle
	// that fails on Dolt backend (gt-14b8o).
	agentID := m.agentBeadID(name)
	if err := m.beads.ResetAgentBeadForReuse(agentID, "polecat repair"); err != nil {
		if !errors.Is(err, beads.ErrNotFound) {
			style.PrintWarning("could not reset old agent bead %s: %v", agentID, err)
		}
	}

	// Prune stale worktree entries (non-fatal: cleanup only)
	_ = repoGit.WorktreePrune()

	// Move temp worktree to final location using git worktree move.
	// os.Rename breaks worktrees: the .git file and registry gitdir still
	// reference the old temp path, leaving a broken worktree. (GH#2056)
	if err := repoGit.WorktreeMove(tmpClonePath, newClonePath); err != nil {
		// Clean up temp worktree if move fails
		_ = repoGit.WorktreeRemove(tmpClonePath, true)
		_ = os.RemoveAll(tmpClonePath)
		return nil, fmt.Errorf("moving repaired worktree to final path: %w", err)
	}

	// Provision CLAUDE.md (same as spawn path — repair creates a fresh worktree).
	repairRigName := filepath.Base(m.rig.Path)
	if _, err := templates.CreatePolecatCLAUDEmd(newClonePath, repairRigName, name); err != nil {
		style.PrintWarning("could not provision polecat CLAUDE.md during repair: %v", err)
	}

	// Set up shared beads — fatal during repair too, same reason as spawn.
	if err := m.setupSharedBeads(newClonePath); err != nil {
		_ = repoGit.WorktreeRemove(newClonePath, true)
		_ = os.RemoveAll(newClonePath)
		return nil, fmt.Errorf("setting up shared beads after repair: %w (polecat cannot submit MRs without shared beads)", err)
	}

	// Copy overlay files from .runtime/overlay/ to polecat root.
	if err := rig.CopyOverlay(m.rig.Path, newClonePath); err != nil {
		style.PrintWarning("could not copy overlay files: %v", err)
	}

	// Keep worktree runtime ignores local so the tracked tree stays clean.
	if err := rig.EnsureLocalExcludePatterns(newClonePath); err != nil {
		style.PrintWarning("could not update local git excludes: %v", err)
	}

	// NOTE: Slash commands inherited from town level - no per-workspace copies needed.

	// Create or reopen agent bead for ZFC compliance
	// HookBead is set atomically at recreation time if provided.
	// Uses CreateOrReopenAgentBead to handle re-spawning with same name (GH #332).
	// Retries with backoff — a polecat without an agent bead is untrackable (gt-94llt7).
	if err = m.createAgentBeadWithRetry(agentID, &beads.AgentFields{
		RoleType:   "polecat",
		Rig:        m.rig.Name,
		AgentState: "spawning",
		HookBead:   opts.HookBead, // Set atomically at spawn time
	}); err != nil {
		// Hard fail — clean up the new worktree since we can't track this polecat
		_ = repoGit.WorktreeRemove(newClonePath, true)
		_ = os.RemoveAll(newClonePath)
		// Remove polecatDir to prevent limbo state where m.exists(name) returns true
		// but no valid worktree exists. Matches AddWithOptions cleanupOnError behavior.
		_ = os.RemoveAll(polecatDir)
		return nil, fmt.Errorf("agent bead required for polecat tracking: %w", err)
	}

	// Return fresh polecat in working state (transient model: polecats are spawned with work)
	now := time.Now()
	return &Polecat{
		Name:      name,
		Rig:       m.rig.Name,
		State:     StateWorking,
		ClonePath: newClonePath,
		Branch:    branchName,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// ReuseIdlePolecat prepares an idle polecat for new work using branch-only operations.
// Unlike RepairWorktreeWithOptions, this does NOT create/remove git worktrees.
// It simply creates a fresh branch on the existing worktree, which eliminates the
// ~5s overhead of worktree creation. Phase 3 of persistent-polecat-pool.md.
//
// Steps:
//  1. Verify polecat exists and worktree is accessible
//  2. Fetch latest from origin
//  3. Create fresh branch: git checkout -b <branch> <startPoint>
//  4. Reset agent bead and set hook_bead atomically
//  5. Return polecat in working state
func (m *Manager) ReuseIdlePolecat(name string, opts AddOptions) (*Polecat, error) {
	// Acquire per-polecat file lock to prevent concurrent reuse/remove races
	fl, err := m.lockPolecat(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = fl.Unlock() }()

	if !m.exists(name) {
		return nil, ErrPolecatNotFound
	}

	// Kill any existing session unconditionally before reuse.
	// The polecat was found idle (no hooked work), so even a "live" session is
	// just Claude sitting at a dead ❯ prompt from the previous task. Leaving it
	// alive prevents StartSession from creating a fresh session with a proper
	// gt prime --hook cycle to discover the newly hooked bead.
	//
	// Previously, non-stale sessions returned ErrSessionRunning here, causing the
	// caller to allocate a new polecat. But heartbeat freshness can race with
	// session lifecycle (e.g. a compact/resume hook refreshes the heartbeat while
	// the session is functionally idle), leaving the old session alive and the
	// new work undiscovered.
	if running, _ := m.polecatSessionState(name); running {
		sessionName := session.PolecatSessionName(session.PrefixFor(m.rig.Name), name)
		if err := m.tmux.KillSessionWithProcesses(sessionName); err != nil {
			return nil, fmt.Errorf("killing existing session %s for reuse: %w", sessionName, err)
		}
		// Remove stale heartbeat so SessionManager.Start doesn't see leftover data.
		townRoot := filepath.Dir(m.rig.Path)
		RemoveSessionHeartbeat(townRoot, sessionName)
	}

	// Get worktree path (must already exist for reuse)
	clonePath := m.clonePath(name)
	if _, err := os.Stat(clonePath); err != nil {
		return nil, fmt.Errorf("idle polecat worktree not found at %s: %w", clonePath, err)
	}

	polecatGit := git.NewGit(clonePath)

	// Fetch latest from origin (non-fatal: may be offline)
	repoGit, err := m.repoBase()
	if err == nil {
		_ = repoGit.Fetch("origin")
	}
	// Also fetch in the worktree itself so it has the latest refs
	_ = polecatGit.Fetch("origin")

	// Determine the start point for the new branch.
	// When resuming an existing branch (gh#3602), the start point IS that branch's
	// remote tip — we want HEAD on the named branch, not on a detached fresh ref.
	var startPoint string
	switch {
	case opts.ResumeBranch != "":
		// Fetch the resume branch directly so origin/<branch> is up-to-date even
		// on shallow / single-branch reference clones.
		if repoGit != nil {
			if err := repoGit.FetchBranch("origin", opts.ResumeBranch); err != nil {
				style.PrintWarning("could not fetch resume branch %s on bare repo: %v", opts.ResumeBranch, err)
			}
		}
		if err := polecatGit.FetchBranch("origin", opts.ResumeBranch); err != nil {
			style.PrintWarning("could not fetch resume branch %s in worktree: %v", opts.ResumeBranch, err)
		}
		startPoint = "origin/" + opts.ResumeBranch
	case opts.BaseBranch != "":
		startPoint = opts.BaseBranch
	default:
		defaultBranch := "main"
		if rigCfg, err := rig.LoadRigConfig(m.rig.Path); err == nil && rigCfg.DefaultBranch != "" {
			defaultBranch = rigCfg.DefaultBranch
		}
		startPoint = fmt.Sprintf("origin/%s", defaultBranch)
	}

	// Validate that startPoint ref exists
	if exists, err := polecatGit.RefExists(startPoint); err != nil {
		return nil, fmt.Errorf("checking ref %s: %w", startPoint, err)
	} else if !exists {
		return nil, fmt.Errorf("start point %s not found — fall back to full repair", startPoint)
	}

	// GH#2536: Clean worktree state before branch switch — the worktree may have
	// stale state from a previous dog/pool dispatch (uncommitted changes, untracked
	// files, detached HEAD, or checked out on an old dog/alpha-* branch).
	// Reset to the start point directly (not HEAD) to avoid "local changes would
	// be overwritten" errors when the start point has different file content.
	_ = polecatGit.ResetHard(startPoint)
	_ = polecatGit.CleanForce()

	// Re-provision CLAUDE.md after reset — git reset --hard restores the tracked
	// version (which lacks gt done instructions), and git clean -f removes any
	// untracked CLAUDE.md we previously wrote. Without this, reused polecats
	// lose all lifecycle instructions and never call gt done.
	reuseRigName := filepath.Base(m.rig.Path)
	if _, err := templates.CreatePolecatCLAUDEmd(clonePath, reuseRigName, name); err != nil {
		style.PrintWarning("could not re-provision polecat CLAUDE.md on reuse: %v", err)
	}

	// Create or reset the branch tracking the start point. For resume, the branch
	// IS opts.ResumeBranch (so pushes go back to the existing PR head). For fresh
	// work, build a new polecat/<name>/<bead>@<ts> branch.
	branchName := m.buildBranchName(name, opts.HookBead)
	if opts.ResumeBranch != "" {
		branchName = opts.ResumeBranch
		// CheckoutResetBranch (`git checkout -B`) creates or resets the branch to
		// the start point. Use this instead of CheckoutNewBranch because the local
		// branch may already exist from a prior run on this idle polecat.
		if err := polecatGit.CheckoutResetBranch(branchName, startPoint); err != nil {
			return nil, fmt.Errorf("checking out resume branch %s from %s: %w", branchName, startPoint, err)
		}
	} else {
		if err := polecatGit.CheckoutNewBranch(branchName, startPoint); err != nil {
			// checkout -b fails if branch already exists or other edge case.
			// Fall back to: checkout start point, then create branch.
			_ = polecatGit.Checkout(startPoint)
			if err2 := polecatGit.CheckoutNewBranch(branchName, startPoint); err2 != nil {
				return nil, fmt.Errorf("creating branch %s from %s (retry after cleanup): %w", branchName, startPoint, err2)
			}
		}
	}

	// Verify the worktree is actually on the expected branch
	if actual, err := polecatGit.CurrentBranch(); err == nil && actual != branchName {
		return nil, fmt.Errorf("branch mismatch after checkout: expected %s, got %s", branchName, actual)
	}

	// Reset agent bead for reuse
	agentID := m.agentBeadID(name)
	if err := m.beads.ResetAgentBeadForReuse(agentID, "idle polecat reuse"); err != nil {
		if !errors.Is(err, beads.ErrNotFound) {
			style.PrintWarning("could not reset agent bead %s: %v", agentID, err)
		}
	}

	// Create or reopen agent bead with hook_bead set atomically
	if err = m.createAgentBeadWithRetry(agentID, &beads.AgentFields{
		RoleType:   "polecat",
		Rig:        m.rig.Name,
		AgentState: "spawning",
		HookBead:   opts.HookBead,
	}); err != nil {
		return nil, fmt.Errorf("agent bead required for polecat tracking: %w", err)
	}

	// Sync agent_state column to "spawning" (gt-ulom).
	// createAgentBeadWithRetry sets agent_state in the description only.
	// The column stays stale (e.g., "idle" from previous gt done) until
	// StartSession sets it to "working". Without this, the column and
	// description diverge, causing dashboards to show incorrect state.
	if err := m.beads.UpdateAgentState(agentID, "spawning"); err != nil {
		style.PrintWarning("could not sync agent_state column to spawning: %v", err)
	}

	now := time.Now()
	return &Polecat{
		Name:      name,
		Rig:       m.rig.Name,
		State:     StateWorking,
		ClonePath: clonePath,
		Branch:    branchName,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// ReconcilePool derives pool InUse state from existing polecat directories and active sessions.
// This implements ZFC: InUse is discovered from filesystem and tmux, not tracked separately.
// Called before each allocation to ensure InUse reflects reality.
//
// In addition to directory checks, this also:
// - Kills orphaned tmux sessions (sessions without directories are broken)
func (m *Manager) ReconcilePool() {
	fl, err := m.lockPool()
	if err != nil {
		return
	}
	defer func() { _ = fl.Unlock() }()

	m.reconcilePoolInternal()
}

// reconcilePoolInternal performs pool reconciliation without acquiring the pool lock.
// Called by ReconcilePool (which holds the lock) and AllocateName (which also holds it).
func (m *Manager) reconcilePoolInternal() {
	// Get polecats with existing directories
	polecats, err := m.List()
	if err != nil {
		return
	}

	var namesWithDirs []string
	for _, p := range polecats {
		namesWithDirs = append(namesWithDirs, p.Name)
	}

	// Include names with pending reservation markers.
	// A .pending file means AllocateName has claimed the name but AddWithOptions
	// hasn't created the directory yet. Without this, Reconcile would see no
	// directory and treat the name as available, causing a duplicate allocation.
	polecatsDir := filepath.Join(m.rig.Path, "polecats")
	if entries, err := os.ReadDir(polecatsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".pending") {
				namesWithDirs = append(namesWithDirs, strings.TrimSuffix(e.Name(), ".pending"))
			}
		}
	}

	// Get names with tmux sessions
	var namesWithSessions []string
	if m.tmux != nil {
		poolNames := m.namePool.getNames()
		for _, name := range poolNames {
			sessionName := session.PolecatSessionName(session.PrefixFor(m.rig.Name), name)
			hasSession, _ := m.tmux.HasSession(sessionName)
			if hasSession {
				namesWithSessions = append(namesWithSessions, name)
			}
		}
	}

	m.ReconcilePoolWith(namesWithDirs, namesWithSessions)

	// Prune any stale git worktree entries (handles manually deleted directories)
	if repoGit, err := m.repoBase(); err == nil {
		_ = repoGit.WorktreePrune()
	}
}

// ReconcilePoolWith reconciles the name pool given lists of names from different sources.
// This is the testable core of ReconcilePool.
//
// - namesWithDirs: names that have existing worktree directories (in use)
// - namesWithSessions: names that have tmux sessions
//
// Names with sessions but no directories are orphans and their sessions are killed.
// Only namesWithDirs are marked as in-use for allocation.
func (m *Manager) ReconcilePoolWith(namesWithDirs, namesWithSessions []string) {
	dirSet := make(map[string]bool)
	for _, name := range namesWithDirs {
		dirSet[name] = true
	}

	// Kill orphaned or stale sessions.
	// - No directory: orphan session, always kill (worktree was removed but tmux lingered)
	// - Has directory but dead process: stale session from crashed startup (gt-jn40ft)
	// Use KillSessionWithProcesses to ensure all descendant processes are killed.
	if m.tmux != nil {
		townRoot := filepath.Dir(m.rig.Path)
		for _, name := range namesWithSessions {
			sessionName := session.PolecatSessionName(session.PrefixFor(m.rig.Name), name)
			if !dirSet[name] {
				// Orphan: session exists but no directory
				_ = m.tmux.KillSessionWithProcesses(sessionName)
				RemoveSessionHeartbeat(townRoot, sessionName)
			} else if isSessionProcessDead(m.tmux, sessionName, townRoot) {
				// Stale: directory exists but session's process has died
				_ = m.tmux.KillSessionWithProcesses(sessionName)
				RemoveSessionHeartbeat(townRoot, sessionName)
			}
		}
	}

	m.namePool.Reconcile(namesWithDirs)
	// Note: No Save() needed - InUse is transient state, only OverflowNext is persisted

	// Clean up orphaned polecat state (fixes #698)
	m.cleanupOrphanPolecatState()
}

// isSessionProcessDead checks if a polecat session's agent has exited.
//
// Uses heartbeat-based liveness detection (gt-qjtq): checks whether the session's
// heartbeat file has been updated recently. Polecat sessions touch their heartbeat
// via gt commands (gt prime, gt hook, bd show, etc.) which run frequently during
// normal operation. A stale heartbeat indicates the agent is no longer active.
//
// Falls back to PID signal probing when no heartbeat file exists (backward
// compatibility for sessions started before heartbeat support was added).
//
// Returns true only when we can confirm the process is dead, not on transient
// failures (gt-kncti: permission denied false positives).
func isSessionProcessDead(t *tmux.Tmux, sessionName string, townRoot string) bool {
	// Primary: heartbeat-based liveness check (gt-qjtq ZFC fix).
	if townRoot != "" {
		stale, exists := IsSessionHeartbeatStale(townRoot, sessionName)
		if exists {
			return stale
		}
		// No heartbeat file — fall through to PID-based check for backward compatibility.
	}

	// Fallback: PID signal probing (legacy, for sessions without heartbeat support).
	pidStr, err := t.GetPanePID(sessionName)
	if err != nil {
		// Tmux query failed — could be permission denied, server busy, etc.
		// Don't assume dead; let a future cycle retry.
		return false
	}
	if pidStr == "" {
		// No PID means no process — session is dead.
		return true
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		// Got a non-numeric PID — shouldn't happen, but don't kill.
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	// On Unix, Signal(0) checks if process exists without sending a signal
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return true
	}
	return false
}

// pendingMaxAge is how long a .pending reservation marker may exist before
// it is considered stale. gt sling completes in seconds, so 5 minutes is
// a conservative bound that avoids false positives on slow machines.
// Configurable via operational.polecat.pending_max_age in settings/config.json.
const pendingMaxAge = 5 * time.Minute

// cleanupOrphanPolecatState removes partial/broken polecat state during allocation.
// This handles the race condition where worktree creation fails mid-way, leaving:
// - Empty polecat directories without .git
// - Directories with invalid/corrupt .git files
// - Stale git worktree registrations
// - Stale .pending reservation markers (gt sling crashed before AddWithOptions)
func (m *Manager) cleanupOrphanPolecatState() {
	polecatsDir := filepath.Join(m.rig.Path, "polecats")

	entries, err := os.ReadDir(polecatsDir)
	if err != nil {
		return // polecats dir doesn't exist, nothing to clean
	}

	for _, entry := range entries {
		// Clean up stale allocation reservation markers.
		// A .pending file older than pendingMaxAge means gt sling crashed after
		// AllocateName but before AddWithOptions created the directory. Remove it
		// so the name can be reallocated on the next reconcile.
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".pending") {
			info, err := entry.Info()
			if err == nil && time.Since(info.ModTime()) > pendingMaxAge {
				_ = os.Remove(filepath.Join(polecatsDir, entry.Name()))
			}
			continue
		}

		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		name := entry.Name()
		polecatDir := filepath.Join(polecatsDir, name)

		// Check if this is a valid polecat with a working worktree
		clonePath := filepath.Join(polecatDir, m.rig.Name)
		gitPath := filepath.Join(clonePath, ".git")

		// Check if clone directory exists
		if _, err := os.Stat(clonePath); os.IsNotExist(err) {
			// Empty polecat directory without clone - remove it
			_ = os.RemoveAll(polecatDir)
			continue
		}

		// Check if .git exists (file for worktree, or directory for full clone)
		if _, err := os.Stat(gitPath); os.IsNotExist(err) {
			// Clone exists but no .git - incomplete worktree, remove it
			_ = os.RemoveAll(polecatDir)
			continue
		}
	}
}

// PoolStatus returns information about the name pool.
func (m *Manager) PoolStatus() (active int, names []string) {
	return m.namePool.ActiveCount(), m.namePool.ActiveNames()
}

// List returns all polecats in the rig.
// Loads polecat state in parallel to avoid sequential bd subprocess overhead.
func (m *Manager) List() ([]*Polecat, error) {
	polecatsDir := filepath.Join(m.rig.Path, "polecats")

	entries, err := os.ReadDir(polecatsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading polecats dir: %w", err)
	}

	// Filter to valid directories first
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		names = append(names, entry.Name())
	}

	// Load all polecats in parallel — each loadFromBeads call involves
	// multiple bd/git subprocess calls that are independent per polecat.
	results := make([]*Polecat, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()
			p, err := m.Get(name)
			if err != nil {
				return // Skip invalid polecats (leaves nil in results)
			}
			results[idx] = p
		}(i, name)
	}
	wg.Wait()

	// Compact — remove nil entries from failed Gets
	polecats := make([]*Polecat, 0, len(results))
	for _, p := range results {
		if p != nil {
			polecats = append(polecats, p)
		}
	}

	return polecats, nil
}

// FindIdlePolecat returns the first idle polecat in the rig, or nil if none.
// Idle polecats have completed their work and have a preserved sandbox (worktree)
// that can be reused by gt sling without creating a new worktree.
// Persistent polecat model (gt-4ac).
func (m *Manager) FindIdlePolecat() (*Polecat, error) {
	polecats, err := m.List()
	if err != nil {
		return nil, err
	}
	for _, p := range polecats {
		if p.State == StateIdle {
			return p, nil
		}
	}
	return nil, nil
}

// Get returns a specific polecat by name.
// State is derived from beads assignee field + tmux session state:
// - If an issue is assigned to this polecat: StateWorking
// - If no issue but tmux session is running: StateWorking (session alive = still working)
// - If no issue and no tmux session: StateIdle (persistent, ready for reuse)
func (m *Manager) Get(name string) (*Polecat, error) {
	if !m.exists(name) {
		return nil, ErrPolecatNotFound
	}

	return m.loadFromBeads(name)
}

// SetState updates a polecat's state.
// In the beads model, state is derived from issue status:
// - StateWorking: issue status set to in_progress
// SetAgentState updates the agent bead's agent_state field.
// This is called after a polecat session successfully starts to transition
// from "spawning" to "working", making gt polecat identity show accurate status.
// Valid states: "spawning", "working", "done", "stuck", "idle"
func (m *Manager) SetAgentState(name string, state string) error {
	agentID := m.agentBeadID(name)
	return m.beads.UpdateAgentState(agentID, state)
}

// - StateDone: assignee cleared from issue (polecat ready for cleanup)
// - StateStuck: issue status set to blocked (if supported)
// If beads is not available, this is a no-op.
func (m *Manager) SetState(name string, state State) error {
	if !m.exists(name) {
		return ErrPolecatNotFound
	}

	// Find the issue assigned to this polecat
	assignee := m.assigneeID(name)
	issue, err := m.beads.GetAssignedIssue(assignee)
	if err != nil {
		// If beads is not available, treat as no-op (state can't be changed)
		return nil
	}

	switch state {
	case StateWorking:
		// Set issue to in_progress if there is one.
		// Skip if status is "hooked" — sling sets this, and changing it here causes
		// merge conflicts when gt done runs. The polecat should claim work via gt prime,
		// not have sling change status during spawn (gt-zecmc).
		if issue != nil && issue.Status != beads.StatusHooked {
			status := "in_progress"
			if err := m.beads.Update(issue.ID, beads.UpdateOptions{Status: &status}); err != nil {
				return fmt.Errorf("setting issue status: %w", err)
			}
		}
	case StateDone:
		// Clear assignment when done (polecat ready for cleanup)
		if issue != nil {
			empty := ""
			if err := m.beads.Update(issue.ID, beads.UpdateOptions{Assignee: &empty}); err != nil {
				return fmt.Errorf("clearing assignee: %w", err)
			}
		}
	case StateStuck:
		// Mark issue as blocked if supported, otherwise just note in issue
		if issue != nil {
			// For now, just keep the assignment - the issue's blocked_by would indicate stuck
			// We could add a status="blocked" here if beads supports it
		}
	}

	return nil
}

// AssignIssue assigns an issue to a polecat by setting the issue's assignee in beads.
func (m *Manager) AssignIssue(name, issue string) error {
	if !m.exists(name) {
		return ErrPolecatNotFound
	}

	// Set the issue's assignee to this polecat
	assignee := m.assigneeID(name)
	status := "in_progress"
	if err := m.beads.Update(issue, beads.UpdateOptions{
		Assignee: &assignee,
		Status:   &status,
	}); err != nil {
		return fmt.Errorf("setting issue assignee: %w", err)
	}

	return nil
}

// ClearIssue removes the issue assignment from a polecat.
// In the transient model, this transitions to Done state for cleanup.
// This clears the assignee from the currently assigned issue in beads.
// If beads is not available, this is a no-op.
func (m *Manager) ClearIssue(name string) error {
	if !m.exists(name) {
		return ErrPolecatNotFound
	}

	// Find the issue assigned to this polecat
	assignee := m.assigneeID(name)
	issue, err := m.beads.GetAssignedIssue(assignee)
	if err != nil {
		// If beads is not available, treat as no-op
		return nil
	}

	if issue == nil {
		// No issue assigned, nothing to clear
		return nil
	}

	// Clear the assignee from the issue
	empty := ""
	if err := m.beads.Update(issue.ID, beads.UpdateOptions{
		Assignee: &empty,
	}); err != nil {
		return fmt.Errorf("clearing issue assignee: %w", err)
	}

	return nil
}

// unassignWorkBeads finds all active work beads assigned to a polecat and resets them
// to status=open with an empty assignee, so they can be picked up by another polecat.
// This must be called during polecat removal to prevent orphaned beads (gt-e4u1).
// Agent beads are skipped (handled separately by ResetAgentBeadForReuse).
// Errors are logged as warnings but do not block removal.
func (m *Manager) unassignWorkBeads(name string) {
	assignee := m.assigneeID(name)
	// Check all active work statuses that could leave orphaned beads
	for _, status := range []string{"open", "in_progress", beads.StatusHooked} {
		issues, err := m.beads.List(beads.ListOptions{
			Status:   status,
			Assignee: assignee,
			Priority: -1,
		})
		if err != nil {
			style.PrintWarning("could not list %s beads for %s: %v", status, name, err)
			continue
		}
		for _, issue := range issues {
			// Skip agent beads — handled by ResetAgentBeadForReuse
			if beads.IsAgentBead(issue) {
				continue
			}
			// Skip protected beads (standing orders, role defs, etc.) —
			// they should retain their status and assignee across polecat lifecycles.
			if beads.IsProtectedBead(issue) {
				continue
			}
			openStatus := "open"
			empty := ""
			if err := m.beads.Update(issue.ID, beads.UpdateOptions{
				Status:   &openStatus,
				Assignee: &empty,
			}); err != nil {
				style.PrintWarning("could not unassign bead %s from %s: %v", issue.ID, name, err)
			}
		}
	}
}

// loadFromBeads gets polecat info from hooked work beads + beads assignee field + tmux session state.
// State derivation priority:
//  1. Work bead status=hooked + assignee=<polecat> → working (authoritative source)
//  2. Legacy agent hook_bead that still points to a currently hooked bead for this assignee
//     → working (compatibility fallback during migration)
//  3. Issue assigned via beads assignee (open/in_progress/hooked) → working
//  4. Live tmux session → working (session active even if assignment not yet recorded)
//  5. agent_state=idle with no live session → idle
//  6. None of the above → idle
func (m *Manager) loadFromBeads(name string) (*Polecat, error) {
	// Use clonePath which handles both new (polecats/<name>/<rigname>/)
	// and old (polecats/<name>/) structures
	clonePath := m.clonePath(name)

	// Get actual branch from worktree (branches are now timestamped)
	polecatGit := git.NewGit(clonePath)
	branchName, err := polecatGit.CurrentBranch()
	if err != nil {
		// Fall back to old format if we can't read the branch
		branchName = fmt.Sprintf("polecat/%s", name)
	}

	assignee := m.assigneeID(name)

	// Cross-check tmux session liveness once for use in state derivation below.
	// When a tmux session has died (e.g., due to disk space exhaustion or OOM),
	// beads may still report the polecat as "working" because the bead state was
	// never updated. Without this check, `gt polecat list` shows zombies as working.
	// See: disk-space-resilience — all 5 polecats appeared "working" after sessions died.
	//
	// When tmux is nil (e.g., no tmux available or in tests), we cannot determine
	// session state, so we must NOT assume the session is dead — default to alive.
	sessionRunning, sessionStale := m.polecatSessionState(name)
	sessionDead := m.tmux != nil && (!sessionRunning || sessionStale)

	// Primary source: the work bead itself (status=hooked + assignee).
	// This is the direct-tracking model introduced in hq-l6mm5.
	hookedBeads, hookedErr := m.beads.List(beads.ListOptions{
		Status:   beads.StatusHooked,
		Assignee: assignee,
		Priority: -1,
	})
	if hookedErr == nil && len(hookedBeads) > 0 {
		state := StateWorking
		if sessionDead {
			state = StateStalled
		}
		return &Polecat{
			Name:      name,
			Rig:       m.rig.Name,
			State:     state,
			ClonePath: clonePath,
			Branch:    branchName,
			Issue:     hookedBeads[0].ID,
		}, nil
	}

	// Compatibility fallback: if legacy hook_bead is still set, only trust it when
	// it resolves to a currently hooked bead for this assignee. This avoids stale
	// issue reporting when hook_bead diverges from the work bead state.
	agentID := m.agentBeadID(name)
	_, fields, agentErr := m.beads.GetAgentBead(agentID)
	if agentErr == nil && fields != nil && fields.HookBead != "" {
		if hookIssue, err := m.beads.Show(fields.HookBead); err == nil &&
			isCurrentHookedIssueForAssignee(hookIssue, assignee) {
			state := StateWorking
			if sessionDead {
				state = StateStalled
			}
			return &Polecat{
				Name:      name,
				Rig:       m.rig.Name,
				State:     state,
				ClonePath: clonePath,
				Branch:    branchName,
				Issue:     fields.HookBead,
			}, nil
		}
	}

	// Fallback: Query beads for assigned issue (for polecats without agent beads
	// or with empty hook_bead)
	issue, beadsErr := m.beads.GetAssignedIssue(assignee)
	if beadsErr != nil {
		// If beads query fails, cross-check tmux session state.
		// Previously defaulted to StateWorking; now detects stalled sessions.
		state := StateWorking
		if sessionDead {
			state = StateStalled
		}
		return &Polecat{
			Name:      name,
			Rig:       m.rig.Name,
			State:     state,
			ClonePath: clonePath,
			Branch:    branchName,
		}, nil
	}

	// Persistent model: has issue = working, otherwise a live tmux session still
	// means working even if beads state has fallen behind.
	issueID := ""
	if issue != nil {
		issueID = issue.ID
	} else if running, stale := m.polecatSessionState(name); running && !stale {
		return &Polecat{
			Name:      name,
			Rig:       m.rig.Name,
			State:     StateWorking,
			ClonePath: clonePath,
			Branch:    branchName,
		}, nil
	}

	// Persistent polecat model (gt-4ac): only trust agent_state=idle once the
	// tmux session is gone. This prevents reusing a polecat that still has a live
	// session when its bead state was cleared early.
	state := StateIdle
	if issueID != "" {
		state = StateWorking
	} else if agentErr == nil && fields != nil && beads.AgentState(fields.AgentState) == beads.AgentStateIdle {
		state = StateIdle
	}

	return &Polecat{
		Name:      name,
		Rig:       m.rig.Name,
		State:     state,
		ClonePath: clonePath,
		Branch:    branchName,
		Issue:     issueID,
	}, nil
}

func (m *Manager) polecatSessionState(name string) (running bool, stale bool) {
	if m.tmux == nil {
		return false, false
	}

	sessionName := session.PolecatSessionName(session.PrefixFor(m.rig.Name), name)
	running, err := m.tmux.HasSession(sessionName)
	if err != nil || !running {
		return false, false
	}

	return true, NewSessionManager(m.tmux, m.rig).isSessionStale(sessionName)
}

func isCurrentHookedIssueForAssignee(issue *beads.Issue, assignee string) bool {
	return issue != nil &&
		issue.Status == beads.StatusHooked &&
		issue.Assignee == assignee
}

// setupSharedBeads creates a redirect file so the polecat uses the rig's shared .beads database.
// This eliminates the need for git sync between polecat clones - all polecats share one database.
// Also propagates beads git config (role, issue_prefix) so bd commands work without warnings.
func (m *Manager) setupSharedBeads(clonePath string) error {
	townRoot := filepath.Dir(m.rig.Path)
	if err := beads.SetupRedirect(townRoot, clonePath); err != nil {
		return err
	}

	// Propagate beads git config to the worktree so bd commands in polecat
	// sessions don't warn about missing role/prefix.
	prefix := beads.GetPrefixForRig(townRoot, m.rig.Name)
	if prefix != "" {
		cmd := exec.Command("git", "-C", clonePath, "config", "beads.issue-prefix", prefix)
		util.SetDetachedProcessGroup(cmd)
		_ = cmd.Run()
	}
	cmd := exec.Command("git", "-C", clonePath, "config", "beads.role", "contributor")
	util.SetDetachedProcessGroup(cmd)
	_ = cmd.Run()

	return nil
}

// CleanupStaleBranches removes orphaned polecat branches that are no longer in use.
// This includes:
// - Branches for polecats that no longer exist
// - Old timestamped branches (keeps only the most recent per polecat name)
// Returns the number of branches deleted.
func (m *Manager) CleanupStaleBranches() (int, error) {
	repoGit, err := m.repoBase()
	if err != nil {
		return 0, fmt.Errorf("finding repo base: %w", err)
	}

	// List all polecat branches
	branches, err := repoGit.ListBranches("polecat/*")
	if err != nil {
		return 0, fmt.Errorf("listing branches: %w", err)
	}

	if len(branches) == 0 {
		return 0, nil
	}

	// Get list of existing polecats
	polecats, err := m.List()
	if err != nil {
		return 0, fmt.Errorf("listing polecats: %w", err)
	}

	// Build set of current polecat branches (from actual polecat objects)
	currentBranches := make(map[string]bool)
	for _, p := range polecats {
		currentBranches[p.Branch] = true
	}

	// Delete branches not in current set
	deleted := 0
	for _, branch := range branches {
		if currentBranches[branch] {
			continue // This branch is in use
		}
		// Delete orphaned branch
		if err := repoGit.DeleteBranch(branch, true); err != nil {
			// Log but continue - non-fatal
			style.PrintWarning("could not delete branch %s: %v", branch, err)
			continue
		}
		deleted++
	}

	return deleted, nil
}

// StalenessInfo contains details about a polecat's staleness.
type StalenessInfo struct {
	Name               string
	CommitsBehind      int    // How many commits behind origin/main
	HasActiveSession   bool   // Whether tmux session is running
	HasUncommittedWork bool   // Whether there's uncommitted or unpushed work
	AgentState         string // From agent bead (empty if no bead)
	IsStale            bool   // Overall assessment: safe to clean up
	Reason             string // Why it's considered stale (or not)
}

// DetectStalePolecats identifies polecats that are candidates for cleanup.
// A polecat is considered stale if:
// - No active tmux session AND
// - Either: way behind main (>threshold commits) OR no agent bead/activity
// - Has no uncommitted work that could be lost
//
// threshold: minimum commits behind main to consider "way behind" (e.g., 20)
func (m *Manager) DetectStalePolecats(threshold int) ([]*StalenessInfo, error) {
	polecats, err := m.List()
	if err != nil {
		return nil, fmt.Errorf("listing polecats: %w", err)
	}

	if len(polecats) == 0 {
		return nil, nil
	}

	// Get default branch from rig config
	defaultBranch := "main"
	if rigCfg, err := rig.LoadRigConfig(m.rig.Path); err == nil && rigCfg.DefaultBranch != "" {
		defaultBranch = rigCfg.DefaultBranch
	}

	var results []*StalenessInfo
	for _, p := range polecats {
		info := &StalenessInfo{
			Name: p.Name,
		}

		// Check for active tmux session
		// Session name follows pattern: gt-<rig>-<polecat>
		sessionName := session.PolecatSessionName(session.PrefixFor(m.rig.Name), p.Name)
		info.HasActiveSession = checkTmuxSession(sessionName)

		// Check how far behind main
		polecatGit := git.NewGit(p.ClonePath)
		info.CommitsBehind = countCommitsBehind(polecatGit, defaultBranch)

		// Check for uncommitted work (excluding .beads/ files which are synced across worktrees)
		status, err := polecatGit.CheckUncommittedWork()
		if err == nil && !status.CleanExcludingBeads() {
			info.HasUncommittedWork = true
		}

		// Check agent bead state
		agentID := m.agentBeadID(p.Name)
		_, fields, err := m.beads.GetAgentBead(agentID)
		if err == nil && fields != nil {
			info.AgentState = fields.AgentState
		}

		// Determine staleness
		info.IsStale, info.Reason = assessStaleness(info, threshold)
		results = append(results, info)
	}

	return results, nil
}

// checkTmuxSession checks if a tmux session exists.
func checkTmuxSession(sessionName string) bool {
	// Use has-session command which returns 0 if session exists
	cmd := tmux.BuildCommand("has-session", "-t", sessionName)
	return cmd.Run() == nil
}

// countCommitsBehind counts how many commits a worktree is behind origin/<defaultBranch>.
func countCommitsBehind(g *git.Git, defaultBranch string) int {
	// Use rev-list to count commits: origin/main..HEAD shows commits ahead,
	// HEAD..origin/main shows commits behind
	remoteBranch := "origin/" + defaultBranch
	count, err := g.CountCommitsBehind(remoteBranch)
	if err != nil {
		return 0 // Can't determine, assume not behind
	}
	return count
}

// assessStaleness determines if a polecat should be cleaned up.
// Per gt-zecmc: uses tmux state (HasActiveSession) rather than agent_state
// since observable states (running, done, idle) are no longer recorded in beads.
func assessStaleness(info *StalenessInfo, threshold int) (bool, string) {
	// Never clean up if there's uncommitted work
	if info.HasUncommittedWork {
		return false, "has uncommitted work"
	}

	// If session is active, not stale (tmux is source of truth for liveness)
	if info.HasActiveSession {
		return false, "session active"
	}

	// No active session - this polecat is a cleanup candidate
	// Check for reasons to keep it:

	// Check for non-observable states that indicate intentional pause
	// (stuck, awaiting-gate are still stored in beads per gt-zecmc)
	if beads.AgentState(info.AgentState).ProtectsFromCleanup() {
		return false, fmt.Sprintf("agent_state=%s (intentional pause)", info.AgentState)
	}

	// No session and way behind main = stale
	if info.CommitsBehind >= threshold {
		return true, fmt.Sprintf("%d commits behind main, no active session", info.CommitsBehind)
	}

	// No session and no agent bead = abandoned, clean up
	if info.AgentState == "" {
		return true, "no agent bead, no active session"
	}

	// No session but has agent bead without special state = clean up
	// (The session is the source of truth for liveness)
	return true, "no active session"
}
