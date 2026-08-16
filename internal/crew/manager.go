package crew

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/jonbaldie/gastown/internal/atomicfile"
	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/git"
	"github.com/jonbaldie/gastown/internal/nudge"
	"github.com/jonbaldie/gastown/internal/rig"
	"github.com/jonbaldie/gastown/internal/runtime"
	"github.com/jonbaldie/gastown/internal/session"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/jonbaldie/gastown/internal/util"
	"github.com/jonbaldie/gastown/internal/worker"
)

// Common errors
var (
	ErrCrewExists      = errors.New("crew worker already exists")
	ErrCrewNotFound    = errors.New("crew worker not found")
	ErrHasChanges      = errors.New("crew worker has uncommitted changes")
	ErrInvalidCrewName = errors.New("invalid crew name")
	ErrSessionRunning  = errors.New("session already running")
	ErrSessionNotFound = errors.New("session not found")
)

// startupDialogPolicy makes every unattended crew startup wait for the runtime
// and dismiss blocking dialogs, regardless of the selected agent preset.
func startupDialogPolicy(interactive bool) (waitForAgent, acceptDialogs bool) {
	if interactive {
		return false, false
	}
	return true, true
}

// StartOptions configures crew session startup.
type StartOptions struct {
	// Account specifies the account handle to use (overrides default).
	Account string

	// ClaudeConfigDir is resolved CLAUDE_CONFIG_DIR for the account.
	// If set, this is injected as an environment variable.
	ClaudeConfigDir string

	// KillExisting kills any existing session before starting (for restart operations).
	// If false and a session is running, Start() returns ErrSessionRunning.
	KillExisting bool

	// Topic is the startup nudge topic (e.g., "start", "restart", "refresh").
	// Defaults to "start" if empty.
	Topic string

	// Interactive removes --dangerously-skip-permissions for interactive/refresh mode.
	Interactive bool

	// AgentOverride specifies an alternate agent alias (e.g., for testing).
	AgentOverride string

	// ResumeSessionID resumes a previous agent session instead of starting fresh.
	// "last" means resume the most recent session (--resume with no session ID).
	// Any other non-empty value is a specific session ID to resume.
	ResumeSessionID string
}

// validateSessionID checks that a resume session ID contains only safe characters.
// Session IDs from Claude, Gemini, etc. are typically UUIDs or hex strings.
// This rejects shell metacharacters that could cause injection when the ID is
// interpolated into a shell command string.
func validateSessionID(id string) error {
	if id == "" || id == "last" {
		return nil
	}
	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return fmt.Errorf("invalid session ID %q: contains character %q; session IDs may only contain alphanumeric characters, hyphens, underscores, and dots", id, string(c))
		}
	}
	return nil
}

// buildResumeArgs validates the agent preset supports resume and returns the
// flag(s) to append to the command string. agentName is the resolved agent
// preset name (e.g. "claude", "gemini"). sessionID is "last" for auto-resume
// or a specific session ID.
func buildResumeArgs(agentName, sessionID string) (string, error) {
	preset := config.GetAgentPresetByName(agentName)
	if preset == nil || preset.ResumeFlag == "" {
		return "", fmt.Errorf("agent %q does not support session resume", agentName)
	}
	if preset.ResumeStyle == "subcommand" {
		return "", fmt.Errorf("--resume not yet supported for subcommand-style agents (e.g., %s); use the agent's native resume mechanism", agentName)
	}

	if sessionID == "last" {
		if preset.ContinueFlag == "" {
			return "", fmt.Errorf("agent %q does not support --resume without a session ID (no ContinueFlag configured); use --resume <session-id> instead", agentName)
		}
		return preset.ContinueFlag, nil
	}
	return preset.ResumeFlag + " " + config.ShellQuote(sessionID), nil
}

// validateCrewName checks that a crew name is safe and valid.
// Rejects path traversal attempts and characters that break agent ID parsing.
func validateCrewName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name cannot be empty", ErrInvalidCrewName)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("%w: %q is not allowed", ErrInvalidCrewName, name)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("%w: %q contains path separators", ErrInvalidCrewName, name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("%w: %q contains path traversal sequence", ErrInvalidCrewName, name)
	}
	// Reject characters that break agent ID parsing (same as rig names)
	if strings.ContainsAny(name, "-. ") {
		sanitized := strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(name)
		sanitized = strings.ToLower(sanitized)
		return fmt.Errorf("%w: %q contains invalid characters; hyphens, dots, and spaces are reserved for agent ID parsing. Try %q instead", ErrInvalidCrewName, name, sanitized)
	}
	return nil
}

// Manager handles crew worker lifecycle.
type Manager struct {
	rig *rig.Rig
	git *git.Git
}

// NewManager creates a new crew manager.
func NewManager(r *rig.Rig, g *git.Git) *Manager {
	return &Manager{
		rig: r,
		git: g,
	}
}

// crewDir returns the directory for a crew worker.
func (m *Manager) crewDir(name string) string {
	return filepath.Join(m.rig.Path, "crew", name)
}

// stateFile returns the state file path for a crew worker.
func (m *Manager) stateFile(name string) string {
	return filepath.Join(m.crewDir(name), "state.json")
}

// mailDir returns the mail directory path for a crew worker.
func (m *Manager) mailDir(name string) string {
	return filepath.Join(m.crewDir(name), "mail")
}

// exists checks if a crew worker exists.
func (m *Manager) exists(name string) bool {
	_, err := os.Stat(m.crewDir(name))
	return err == nil
}

// lockCrew acquires an exclusive file lock for a specific crew worker.
// This prevents concurrent gt processes from racing on the same crew worker's
// filesystem operations (Add, Remove, Rename, Start).
// Caller must defer fl.Unlock().
func (m *Manager) lockCrew(name string) (*flock.Flock, error) {
	lockDir := filepath.Join(m.rig.Path, ".runtime", "locks")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		return nil, fmt.Errorf("creating lock dir: %w", err)
	}
	lockPath := filepath.Join(lockDir, fmt.Sprintf("crew-%s.lock", name))
	fl := flock.New(lockPath)
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("acquiring crew lock for %s: %w", name, err)
	}
	return fl, nil
}

// Add creates a new crew worker with a clone of the rig.
func (m *Manager) Add(name string, createBranch bool) (*CrewWorker, error) {
	if err := validateCrewName(name); err != nil {
		return nil, err
	}
	fl, err := m.lockCrew(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = fl.Unlock() }()
	return m.addLocked(name, createBranch)
}

// addLocked creates a new crew worker, assumes caller holds lockCrew(name).
func (m *Manager) addLocked(name string, createBranch bool) (*CrewWorker, error) {
	if m.exists(name) {
		return nil, ErrCrewExists
	}

	crewPath := m.crewDir(name)

	// Create crew directory if needed
	crewBaseDir := filepath.Join(m.rig.Path, "crew")
	if err := os.MkdirAll(crewBaseDir, 0755); err != nil {
		return nil, fmt.Errorf("creating crew dir: %w", err)
	}

	if m.rig.GitURL == "" {
		return nil, fmt.Errorf("rig %q has no git URL configured — crew workspaces require a clonable repository (set git_url in rigs.json or re-add the rig with a remote URL)", m.rig.Name)
	}

	// Clone the rig repo on the configured default branch.
	// CloneBranch ensures the crew lands on the rig's default_branch even when
	// it differs from the remote's HEAD. Falls back gracefully for new/empty repos.
	defaultBranch := m.rig.DefaultBranch()
	if m.rig.LocalRepo != "" {
		if err := m.git.CloneBranchWithReference(m.rig.GitURL, crewPath, defaultBranch, m.rig.LocalRepo); err != nil {
			style.PrintWarning("could not clone branch %s with reference: %v", defaultBranch, err)
			// Try branch without reference (network fetch), then reference without branch
			if err := m.git.CloneBranch(m.rig.GitURL, crewPath, defaultBranch); err != nil {
				style.PrintWarning("could not clone branch %s: %v", defaultBranch, err)
				if err := m.git.CloneWithReference(m.rig.GitURL, crewPath, m.rig.LocalRepo); err != nil {
					style.PrintWarning("could not clone with reference: %v", err)
					if err := m.git.Clone(m.rig.GitURL, crewPath); err != nil {
						return nil, fmt.Errorf("cloning rig: %w", err)
					}
				}
			}
		}
	} else {
		if err := m.git.CloneBranch(m.rig.GitURL, crewPath, defaultBranch); err != nil {
			style.PrintWarning("could not clone branch %s, falling back to default: %v", defaultBranch, err)
			if err := m.git.Clone(m.rig.GitURL, crewPath); err != nil {
				return nil, fmt.Errorf("cloning rig: %w", err)
			}
		}
	}

	// Sync remotes from mayor/rig so crew clone matches the rig's remote config.
	// This prevents origin pointing to upstream instead of the fork.
	if err := m.syncRemotesFromRig(crewPath); err != nil {
		if m.rig.PushURL != "" {
			if rmErr := os.RemoveAll(crewPath); rmErr != nil {
				style.PrintWarning("could not clean up orphaned clone at %s: %v", crewPath, rmErr)
			}
			return nil, fmt.Errorf("syncing remotes from rig (push URL required): %w", err)
		}
		style.PrintWarning("could not sync remotes from rig: %v", err)
	}

	crewGit := git.NewGit(crewPath)
	branchName := defaultBranch

	// Optionally create a working branch
	if createBranch {
		branchName = fmt.Sprintf("crew/%s", name)
		if err := crewGit.CreateBranch(branchName); err != nil {
			_ = os.RemoveAll(crewPath) // best-effort cleanup
			return nil, fmt.Errorf("creating branch: %w", err)
		}
		if err := crewGit.Checkout(branchName); err != nil {
			_ = os.RemoveAll(crewPath) // best-effort cleanup
			return nil, fmt.Errorf("checking out branch: %w", err)
		}
	}

	// Create mail directory for mail delivery
	mailPath := m.mailDir(name)
	if err := os.MkdirAll(mailPath, 0755); err != nil {
		_ = os.RemoveAll(crewPath) // best-effort cleanup
		return nil, fmt.Errorf("creating mail dir: %w", err)
	}

	// Set up shared beads: crew uses rig's shared beads via redirect file
	if err := m.setupSharedBeads(crewPath); err != nil {
		// Non-fatal - crew can still work, warn but don't fail
		style.PrintWarning("could not set up shared beads: %v", err)
	}

	// Provision PRIME.md with Gas Town context for this worker.
	// This is the fallback if SessionStart hook fails - ensures crew workers
	// always have GUPP and essential Gas Town context.
	if err := beads.ProvisionPrimeMDForWorktree(crewPath); err != nil {
		// Non-fatal - crew can still work via hook, warn but don't fail
		style.PrintWarning("could not provision PRIME.md: %v", err)
	}

	// Copy overlay files from .runtime/overlay/ to crew root.
	// This allows services to have .env and other config files at their root.
	if err := rig.CopyOverlay(m.rig.Path, crewPath); err != nil {
		// Non-fatal - log warning but continue
		style.PrintWarning("could not copy overlay files: %v", err)
	}

	// Run setup hooks from .runtime/setup-hooks/.
	// These hooks can inject local config, copy secrets, or perform other setup tasks.
	if err := rig.RunSetupHooks(m.rig.Path, crewPath); err != nil {
		// Non-fatal - log warning but continue
		style.PrintWarning("could not run setup hooks: %v", err)
	}

	// Ensure .gitignore has required Gas Town patterns
	if err := rig.EnsureGitignorePatterns(crewPath); err != nil {
		// Non-fatal - log warning but continue
		style.PrintWarning("could not update .gitignore: %v", err)
	}

	// Install runtime settings in the shared crew parent directory.
	// Settings are passed to Claude Code via --settings flag.
	addTownRoot := filepath.Dir(m.rig.Path)
	addRuntimeConfig := config.ResolveWorkerAgentConfig(name, addTownRoot, m.rig.Path)
	crewSettingsDir := config.RoleSettingsDir("crew", m.rig.Path)
	if err := runtime.EnsureSettingsForRole(crewSettingsDir, crewPath, "crew", addRuntimeConfig); err != nil {
		// Non-fatal - log warning but continue
		style.PrintWarning("could not install runtime settings: %v", err)
	}

	// NOTE: Slash commands (.claude/commands/) are provisioned at town level by gt install.
	// All agents inherit them via Claude's directory traversal - no per-workspace copies needed.

	// NOTE: We intentionally do NOT write to CLAUDE.md here.
	// Gas Town context is injected ephemerally via SessionStart hook (gt prime).
	// Writing to CLAUDE.md would overwrite project instructions and leak
	// Gas Town internals into the project repo when workers commit/push.

	// Create crew worker state
	now := time.Now()
	crew := &CrewWorker{
		Name:      name,
		Rig:       m.rig.Name,
		ClonePath: crewPath,
		Branch:    branchName,
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Save state
	if err := m.saveState(crew); err != nil {
		_ = os.RemoveAll(crewPath) // best-effort cleanup
		return nil, fmt.Errorf("saving state: %w", err)
	}

	return crew, nil
}

// syncRemotesFromRig copies remote configuration from the mayor/rig repo to a crew clone.
// This ensures crew clones have the same origin (fork) and upstream as the rig,
// preventing repo ID mismatches and broken formula slinging.
func (m *Manager) syncRemotesFromRig(crewPath string) error {
	rigRepoPath := filepath.Join(m.rig.Path, "mayor", "rig")
	if _, err := os.Stat(rigRepoPath); err != nil {
		return fmt.Errorf("mayor/rig not found at %s", rigRepoPath)
	}

	rigGit := git.NewGit(rigRepoPath)
	crewGit := git.NewGit(crewPath)

	remotes, err := rigGit.Remotes()
	if err != nil {
		return fmt.Errorf("reading rig remotes: %w", err)
	}

	for _, remote := range remotes {
		if remote == "" || remote == "mayor" {
			continue // Skip empty and local-only remotes
		}

		url, err := rigGit.RemoteURL(remote)
		if err != nil {
			continue
		}

		// Check if remote exists in crew clone
		existingURL, existErr := crewGit.RemoteURL(remote)
		if existErr != nil {
			// Remote doesn't exist — add it
			if _, addErr := crewGit.AddRemote(remote, url); addErr != nil {
				style.PrintWarning("could not add remote %s: %v", remote, addErr)
			}
		} else if existingURL != url {
			// Remote exists but URL differs — update it
			if _, setErr := crewGit.SetRemoteURL(remote, url); setErr != nil {
				style.PrintWarning("could not update remote %s: %v", remote, setErr)
			}
		}

		// Sync push URL for read-only upstream forks.
		// Dual-source authority model: origin's push URL comes from town.json
		// (via m.rig.PushURL, which config.json populates). Non-origin remotes
		// get push URLs from mayor's git config. This split relies on town.json
		// and config.json staying in sync — RegisterRig writes both to ensure this.
		if remote == "origin" {
			configPushURL := strings.TrimSpace(m.rig.PushURL)
			if configPushURL != "" {
				if cfgErr := crewGit.ConfigurePushURL(remote, configPushURL); cfgErr != nil {
					return fmt.Errorf("syncing origin push URL: %w", cfgErr)
				}
			} else {
				// Config has no push URL — only clear if crew has a stale custom push URL.
				crewPush, pushErr := crewGit.GetPushURL(remote)
				crewFetch, fetchErr := crewGit.RemoteURL(remote)
				if pushErr != nil || fetchErr != nil {
					style.PrintWarning("could not read crew remote URLs for %s, skipping cleanup: push=%v fetch=%v", remote, pushErr, fetchErr)
				} else if crewPush != crewFetch {
					style.PrintWarning("clearing stale push URL for %s (was: %s)", remote, util.RedactURL(crewPush))
					if clrErr := crewGit.ClearPushURL(remote); clrErr != nil {
						style.PrintWarning("could not clear push URL for %s: %v", remote, clrErr)
					}
				}
			}
		} else {
			pushURL, pushErr := rigGit.GetPushURL(remote)
			if pushErr != nil {
				style.PrintWarning("could not read push URL for %s, skipping: %v", remote, pushErr)
			} else if pushURL != "" && pushURL != url {
				if cfgErr := crewGit.ConfigurePushURL(remote, pushURL); cfgErr != nil {
					style.PrintWarning("could not sync push URL for %s: %v", remote, cfgErr)
				}
			} else {
				// Mayor has no custom push URL — only clear if crew has a stale one.
				crewPush, cpErr := crewGit.GetPushURL(remote)
				crewFetch, cfErr := crewGit.RemoteURL(remote)
				if cpErr != nil || cfErr != nil {
					style.PrintWarning("could not read crew remote URLs for %s, skipping cleanup: push=%v fetch=%v", remote, cpErr, cfErr)
				} else if crewPush != crewFetch {
					style.PrintWarning("clearing stale push URL for %s (was: %s)", remote, util.RedactURL(crewPush))
					if clrErr := crewGit.ClearPushURL(remote); clrErr != nil {
						style.PrintWarning("could not clear push URL for %s: %v", remote, clrErr)
					}
				}
			}
		}
	}

	return nil
}

// Remove deletes a crew worker.
func (m *Manager) Remove(name string, force bool) error {
	if err := validateCrewName(name); err != nil {
		return err
	}
	fl, err := m.lockCrew(name)
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()
	if !m.exists(name) {
		return ErrCrewNotFound
	}

	crewPath := m.crewDir(name)

	if !force {
		crewGit := git.NewGit(crewPath)
		hasChanges, err := crewGit.HasUncommittedChanges()
		if err == nil && hasChanges {
			return ErrHasChanges
		}
	}

	// Remove directory
	if err := os.RemoveAll(crewPath); err != nil {
		return fmt.Errorf("removing crew dir: %w", err)
	}

	return nil
}

// List returns all crew workers in the rig.
func (m *Manager) List() ([]*CrewWorker, error) {
	crewBaseDir := filepath.Join(m.rig.Path, "crew")

	entries, err := os.ReadDir(crewBaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading crew dir: %w", err)
	}

	var workers []*CrewWorker
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		worker, err := m.Get(entry.Name())
		if err != nil {
			continue // Skip invalid workers
		}
		workers = append(workers, worker)
	}

	return workers, nil
}

// Get returns a specific crew worker by name.
func (m *Manager) Get(name string) (*CrewWorker, error) {
	if err := validateCrewName(name); err != nil {
		return nil, err
	}
	fl, err := m.lockCrew(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = fl.Unlock() }()
	return m.getLocked(name)
}

// getLocked returns a crew worker, assumes caller holds lockCrew(name).
func (m *Manager) getLocked(name string) (*CrewWorker, error) {
	if !m.exists(name) {
		return nil, ErrCrewNotFound
	}

	return m.loadState(name)
}

// saveState persists crew worker state to disk using atomic write.
func (m *Manager) saveState(crew *CrewWorker) error {
	stateFile := m.stateFile(crew.Name)
	if err := atomicfile.WriteJSON(stateFile, crew); err != nil {
		return fmt.Errorf("writing state: %w", err)
	}

	return nil
}

// loadState reads crew worker state from disk.
func (m *Manager) loadState(name string) (*CrewWorker, error) {
	stateFile := m.stateFile(name)

	data, err := os.ReadFile(stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			// Return minimal crew worker if state file missing
			return &CrewWorker{
				Name:      name,
				Rig:       m.rig.Name,
				ClonePath: m.crewDir(name),
			}, nil
		}
		return nil, fmt.Errorf("reading state: %w", err)
	}

	var crew CrewWorker
	if err := json.Unmarshal(data, &crew); err != nil {
		return nil, fmt.Errorf("parsing state: %w", err)
	}

	// Directory name is source of truth for Name and ClonePath.
	// state.json can become stale after directory rename, copy, or corruption.
	crew.Name = name
	crew.ClonePath = m.crewDir(name)

	// Rig only needs backfill when empty (less likely to drift)
	if crew.Rig == "" {
		crew.Rig = m.rig.Name
	}

	return &crew, nil
}

// Rename renames a crew worker from oldName to newName.
func (m *Manager) Rename(oldName, newName string) error {
	if err := validateCrewName(newName); err != nil {
		return err
	}
	// Lock both names in alphabetical order to prevent deadlock.
	first, second := oldName, newName
	if first > second {
		first, second = second, first
	}
	fl1, err := m.lockCrew(first)
	if err != nil {
		return err
	}
	defer func() { _ = fl1.Unlock() }()
	fl2, err := m.lockCrew(second)
	if err != nil {
		return err
	}
	defer func() { _ = fl2.Unlock() }()
	if !m.exists(oldName) {
		return ErrCrewNotFound
	}
	if m.exists(newName) {
		return ErrCrewExists
	}

	oldPath := m.crewDir(oldName)
	newPath := m.crewDir(newName)

	// Rename directory
	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("renaming crew dir: %w", err)
	}

	// Update state file with new name and path
	crew, err := m.loadState(newName)
	if err != nil {
		// Rollback on error (best-effort)
		_ = os.Rename(newPath, oldPath)
		return fmt.Errorf("loading state: %w", err)
	}

	crew.Name = newName
	crew.ClonePath = newPath
	crew.UpdatedAt = time.Now()

	if err := m.saveState(crew); err != nil {
		// Rollback on error (best-effort)
		_ = os.Rename(newPath, oldPath)
		return fmt.Errorf("saving state: %w", err)
	}

	return nil
}

// Pristine ensures a crew worker is up-to-date with remote.
// It runs git pull --rebase.
func (m *Manager) Pristine(name string) (*PristineResult, error) {
	if err := validateCrewName(name); err != nil {
		return nil, err
	}
	if !m.exists(name) {
		return nil, ErrCrewNotFound
	}

	crewPath := m.crewDir(name)
	crewGit := git.NewGit(crewPath)

	result := &PristineResult{
		Name: name,
	}

	// Check for uncommitted changes
	hasChanges, err := crewGit.HasUncommittedChanges()
	if err != nil {
		return nil, fmt.Errorf("checking changes: %w", err)
	}
	result.HadChanges = hasChanges

	// Pull latest (use origin and current branch)
	if err := crewGit.Pull("origin", ""); err != nil {
		result.PullError = err.Error()
	} else {
		result.Pulled = true
	}

	// Note: With Dolt backend, beads changes are persisted immediately - no sync needed
	result.Synced = true

	return result, nil
}

// PristineResult captures the results of a pristine operation.
type PristineResult struct {
	Name       string `json:"name"`
	HadChanges bool   `json:"had_changes"`
	Pulled     bool   `json:"pulled"`
	PullError  string `json:"pull_error,omitempty"`
	Synced     bool   `json:"synced"`
	SyncError  string `json:"sync_error,omitempty"`
}

// setupSharedBeads creates a redirect file so the crew worker uses the rig's shared .beads database.
// This eliminates the need for git sync between crew clones - all crew members share one database.
func (m *Manager) setupSharedBeads(crewPath string) error {
	townRoot := filepath.Dir(m.rig.Path)
	return beads.SetupRedirect(townRoot, crewPath)
}

// SessionName returns the tmux session name for a crew member.
func (m *Manager) SessionName(name string) string {
	return session.CrewSessionName(session.PrefixFor(m.rig.Name), name)
}

// Start creates and starts a tmux session for a crew member.
// If the crew member doesn't exist, it will be created first.
func (m *Manager) Start(name string, opts StartOptions) error {
	if err := validateCrewName(name); err != nil {
		return err
	}

	// Acquire lock to prevent concurrent Start/Remove races.
	fl, err := m.lockCrew(name)
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	// Get or create the crew worker (using locked variants to avoid lock re-entry)
	worker, err := m.getLocked(name)
	if err == ErrCrewNotFound {
		worker, err = m.addLocked(name, false) // No feature branch for crew
		if err != nil {
			return fmt.Errorf("creating crew workspace: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("getting crew worker: %w", err)
	}

	// Sync remotes (especially pushurl) on every start so that existing crew
	// clones pick up config changes made after initial creation.
	if err := m.syncRemotesFromRig(worker.ClonePath); err != nil {
		// Non-fatal for existing clones: pushurl may not be configured.
		// Only log when a push URL is expected but sync failed.
		if m.rig.PushURL != "" {
			style.PrintWarning("could not sync remotes to crew %s: %v", name, err)
		}
	}

	// Ensure runtime settings exist in the shared crew parent directory.
	// Settings are passed to Claude Code via --settings flag.
	townRoot := filepath.Dir(m.rig.Path)
	runtimeConfig := config.ResolveWorkerAgentConfig(name, townRoot, m.rig.Path)
	crewSettingsDir := config.RoleSettingsDir("crew", m.rig.Path)
	if err := runtime.EnsureSettingsForRole(crewSettingsDir, worker.ClonePath, "crew", runtimeConfig); err != nil {
		return fmt.Errorf("ensuring runtime settings: %w", err)
	}

	// Build startup command before killing any existing session so a
	// validation failure cannot leave the user without a running session.
	var claudeCmd string
	if opts.ResumeSessionID != "" {
		// Validate session ID to prevent shell injection. The ID is interpolated
		// into a shell command string, so reject anything with metacharacters.
		if err := validateSessionID(opts.ResumeSessionID); err != nil {
			return err
		}

		// Resume mode: build command without prompt, then append resume flag.
		// No beacon is passed as prompt - the resumed session already has context.
		// The SessionStart hook still fires and injects Gas Town metadata.
		claudeCmd, err = config.BuildCrewStartupCommandWithAgentOverride(m.rig.Name, name, m.rig.Path, "", opts.AgentOverride)
		if err != nil {
			return fmt.Errorf("building resume command: %w", err)
		}

		// Determine agent preset for resume flag.
		// Try worker-level agent config first, fall back to "claude".
		agentName := opts.AgentOverride
		if agentName == "" {
			if rc := config.ResolveWorkerAgentConfig(name, townRoot, m.rig.Path); rc != nil && rc.Provider != "" {
				agentName = rc.Provider
			} else {
				agentName = "claude"
			}
		}
		resumeArgs, err := buildResumeArgs(agentName, opts.ResumeSessionID)
		if err != nil {
			return err
		}
		claudeCmd += " " + resumeArgs
	} else {
		// Normal start: build beacon for predecessor discovery via /resume.
		// Only used in fresh-start mode — resumed sessions already have context.
		address := session.BeaconRecipient("crew", name, m.rig.Name)
		topic := opts.Topic
		if topic == "" {
			topic = "start"
		}
		beacon := session.FormatStartupBeacon(session.BeaconConfig{
			Recipient: address,
			Sender:    "human",
			Topic:     topic,
		})
		claudeCmd, err = config.BuildStartupCommandFromConfig(config.AgentEnvConfig{
			Role:        "crew",
			Rig:         m.rig.Name,
			AgentName:   name,
			TownRoot:    townRoot,
			Prompt:      beacon,
			Topic:       topic,
			SessionName: m.SessionName(name),
		}, m.rig.Path, beacon, opts.AgentOverride)
		if err != nil {
			return fmt.Errorf("building startup command: %w", err)
		}
	}

	t := tmux.NewTmux()
	sessionID := m.SessionName(name)

	// Check if session already exists — kill AFTER command is fully built
	// so validation failures don't destroy the user's running session.
	if opts.KillExisting {
		if _, err := session.KillExistingSession(t, sessionID, false); err != nil {
			return err
		}
	} else if _, err := session.KillExistingSession(t, sessionID, true); err != nil {
		if errors.Is(err, session.ErrSessionAlive) {
			return fmt.Errorf("%w: %s", ErrSessionRunning, sessionID)
		}
		return err
	}

	if opts.Interactive {
		claudeCmd = strings.Replace(claudeCmd, " --dangerously-skip-permissions", "", 1)
	}

	theme := tmux.ResolveSessionTheme(townRoot, m.rig.Name, "crew", name)
	if theme != nil {
		theme.Window = session.ResolveWindowTint(m.rig.Name, "crew")
		if theme.Window == nil && session.IsWindowTintEnabled(m.rig.Name) {
			factor := session.ResolveTintFactor(m.rig.Name)
			theme.Window = &tmux.WindowStyle{
				BG: tmux.DarkenColor(theme.BG, factor),
				FG: theme.FG,
			}
		}
	}

	waitForAgent, acceptBypass := startupDialogPolicy(opts.Interactive)

	topic := opts.Topic
	if topic == "" {
		topic = "start"
	}
	_, err = session.StartSession(t, session.SessionConfig{
		SessionID:        sessionID,
		WorkDir:          worker.ClonePath,
		Role:             "crew",
		TownRoot:         townRoot,
		RigPath:          m.rig.Path,
		RigName:          m.rig.Name,
		AgentName:        name,
		Command:          claudeCmd,
		AgentOverride:    opts.AgentOverride,
		RuntimeConfigDir: opts.ClaudeConfigDir,
		Theme:            theme,
		Beacon: session.BeaconConfig{
			Recipient: session.BeaconRecipient("crew", name, m.rig.Name),
			Sender:    "human",
			Topic:     topic,
		},
		WaitForAgent: waitForAgent,
		AcceptBypass: acceptBypass,
		TrackPID:     true,
	})
	if err != nil {
		return err
	}

	_ = t.SetCrewCycleBindings(sessionID)

	if !opts.Interactive {
		if _, pollerErr := nudge.StartPoller(townRoot, sessionID); pollerErr != nil {
			style.PrintWarning("could not start nudge poller for %s: %v", name, pollerErr)
		}
	}

	return nil
}

// Stop terminates a crew member's tmux session.
func (m *Manager) Stop(name string) error {
	if err := validateCrewName(name); err != nil {
		return err
	}

	t := tmux.NewTmux()
	sessionID := m.SessionName(name)

	// Check if session exists
	running, err := t.HasSession(sessionID)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return ErrSessionNotFound
	}

	// Stop the background nudge poller before killing the session.
	// Non-fatal — the poller will exit on its own when the session dies.
	townRoot := filepath.Dir(m.rig.Path)
	if pollerErr := nudge.StopPoller(townRoot, sessionID); pollerErr != nil {
		style.PrintWarning("could not stop nudge poller for %s: %v", name, pollerErr)
	}

	// Kill the session.
	// Use KillSessionWithProcesses to ensure all descendant processes are killed.
	// This prevents orphan bash processes from Claude's Bash tool surviving session termination.
	if err := t.KillSessionWithProcesses(sessionID); err != nil {
		return fmt.Errorf("killing session: %w", err)
	}
	if err := worker.MarkSessionStopped(townRoot, sessionID); err != nil {
		return fmt.Errorf("recording stopped worker run: %w", err)
	}

	return nil
}

// IsRunning checks if a crew member's session is active.
func (m *Manager) IsRunning(name string) (bool, error) {
	t := tmux.NewTmux()
	sessionID := m.SessionName(name)
	return t.HasSession(sessionID)
}
