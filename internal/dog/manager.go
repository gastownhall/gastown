package dog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/steveyegge/gastown/internal/atomicfile"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
)

// Common errors
var (
	ErrDogExists   = errors.New("dog already exists")
	ErrDogNotFound = errors.New("dog not found")
	ErrDogWorking  = errors.New("dog is currently working")
	ErrNoRigs      = errors.New("no rigs configured")
	ErrInvalidName = errors.New("invalid dog name")
)

// Manager handles dog lifecycle in the kennel.
type Manager struct {
	townRoot   string
	kennelPath string // ~/gt/deacon/dogs/
	rigsConfig *config.RigsConfig
}

// NewManager creates a new dog manager.
func NewManager(townRoot string, rigsConfig *config.RigsConfig) *Manager {
	return &Manager{
		townRoot:   townRoot,
		kennelPath: filepath.Join(townRoot, "deacon", "dogs"),
		rigsConfig: rigsConfig,
	}
}

// lockDog acquires an exclusive file lock for a specific dog's state operations.
// This prevents concurrent load-modify-save races on .dog.json.
// Caller must defer fl.Unlock().
func (m *Manager) lockDog(name string) (*flock.Flock, error) {
	lockDir := m.dogDir(name)
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		return nil, fmt.Errorf("creating dog lock dir: %w", err)
	}
	lockPath := filepath.Join(lockDir, ".dog.lock")
	fl := flock.New(lockPath)
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("acquiring dog lock for %s: %w", name, err)
	}
	return fl, nil
}

// validateDogName checks that a dog name is safe for use as a directory name.
func validateDogName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name cannot be empty", ErrInvalidName)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("%w: name cannot contain path separators", ErrInvalidName)
	}
	if name == "." || name == ".." || strings.Contains(name, "..") {
		return fmt.Errorf("%w: name cannot contain path traversal", ErrInvalidName)
	}
	return nil
}

// dogDir returns the directory for a dog.
func (m *Manager) dogDir(name string) string {
	return filepath.Join(m.kennelPath, name)
}

// exists checks if a dog exists.
//
// A dog is identified by its state file, not by mere directory presence. A
// directory without .dog.json is debris from a failed Add and must not be
// mistaken for a real dog, otherwise the name is permanently unusable while
// List reports an empty kennel.
func (m *Manager) exists(name string) bool {
	_, err := os.Stat(m.stateFilePath(name))
	return err == nil
}

// dirExists reports whether any kennel entry occupies the name, valid or not.
// Removal must reach debris that exists() intentionally ignores.
func (m *Manager) dirExists(name string) bool {
	_, err := os.Stat(m.dogDir(name))
	return err == nil
}

// stateFilePath returns the path to a dog's state file.
func (m *Manager) stateFilePath(name string) string {
	return filepath.Join(m.dogDir(name), ".dog.json")
}

// nonDogKennelMarkers are state files that identify a kennel occupant which is
// deliberately not a dog. The Boot watchdog lives in the kennel and owns
// .boot-status.json; classifying it as debris would invite its destruction.
var nonDogKennelMarkers = []string{".boot-status.json"}

// isNonDogKennelEntry reports whether a kennel entry is a known non-dog
// occupant that must never be treated as removable residue.
func (m *Manager) isNonDogKennelEntry(name string) bool {
	for _, marker := range nonDogKennelMarkers {
		if _, err := os.Stat(filepath.Join(m.dogDir(name), marker)); err == nil {
			return true
		}
	}
	return false
}

// discoverWorktrees reconstructs the rig -> worktree map from disk for a kennel
// entry whose state file is missing. Each direct subdirectory of a dog is a rig
// worktree, so debris left by a partially applied Add remains removable.
func (m *Manager) discoverWorktrees(name string) map[string]string {
	dogPath := m.dogDir(name)
	entries, err := os.ReadDir(dogPath)
	if err != nil {
		return nil
	}

	worktrees := make(map[string]string)
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		worktrees[entry.Name()] = filepath.Join(dogPath, entry.Name())
	}
	return worktrees
}

// Add creates a new dog in the kennel with worktrees into each rig.
// Each dog gets a worktree per rig (e.g., dogs/alpha/gastown/, dogs/alpha/beads/).
// Worktrees are created from each rig's bare repo (.repo.git) or mayor/rig.
func (m *Manager) Add(name string) (dogResult *Dog, err error) {
	if err := validateDogName(name); err != nil {
		return nil, err
	}
	if m.exists(name) {
		return nil, ErrDogExists
	}
	// A directory without .dog.json is debris from an earlier failed Add.
	// Clear it so the name stays usable instead of being burned forever.
	if m.dirExists(name) {
		if err := m.Remove(name); err != nil {
			return nil, fmt.Errorf("clearing incomplete dog %s: %w", name, err)
		}
	}

	// Verify we have rigs to create worktrees into
	if len(m.rigsConfig.Rigs) == 0 {
		return nil, ErrNoRigs
	}

	dogPath := m.dogDir(name)

	// Create kennel dir if needed
	if err := os.MkdirAll(m.kennelPath, 0755); err != nil {
		return nil, fmt.Errorf("creating kennel dir: %w", err)
	}

	// Create dog directory
	if err := os.MkdirAll(dogPath, 0755); err != nil {
		return nil, fmt.Errorf("creating dog dir: %w", err)
	}

	// Track cleanup on failure. Registered git worktrees survive a bare
	// os.RemoveAll, so roll back through Remove, which unregisters them first.
	// A rollback that cannot prove it removed the artifact must say so in the
	// returned error: silently leaving residue is what burns the name and
	// empties the kennel.
	success := false
	defer func() {
		if success {
			return
		}
		if rbErr := m.Remove(name); rbErr != nil {
			err = fmt.Errorf("%w (rollback failed, kennel entry %s left as debris: %v)", err, name, rbErr)
		}
	}()

	// Create worktrees into each rig. A dog is a cross-rig worker, so one
	// unreachable rig must not abort the whole kennel entry: build the dog from
	// the rigs that work and report the ones that were skipped. Only a dog with
	// no usable rig at all is a failure.
	worktrees := make(map[string]string)
	var skipped []string
	for rigName := range m.rigsConfig.Rigs {
		worktreePath, err := m.createRigWorktree(dogPath, name, rigName)
		if err != nil {
			skipped = append(skipped, rigName)
			style.PrintWarning("skipping rig %s for dog %s: %v", rigName, name, err)
			continue
		}
		worktrees[rigName] = worktreePath
	}
	if len(worktrees) == 0 {
		return nil, fmt.Errorf("no usable rig for dog %s (all %d rig(s) failed)", name, len(skipped))
	}

	// Create initial state file
	now := time.Now()
	state := &DogState{
		Name:        name,
		State:       StateIdle,
		LastActive:  now,
		Worktrees:   worktrees,
		SkippedRigs: skipped,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := m.saveState(name, state); err != nil {
		return nil, fmt.Errorf("saving state: %w", err)
	}

	success = true
	return &Dog{
		Name:       name,
		State:      StateIdle,
		Path:       dogPath,
		Worktrees:  worktrees,
		LastActive: now,
		CreatedAt:  now,
	}, nil
}

// createRigWorktree creates a worktree for a dog into a specific rig.
// Uses the rig's bare repo (.repo.git) if available, otherwise mayor/rig.
// Branch naming: dog/<dog-name>-<rig>-<timestamp> for uniqueness.
func (m *Manager) createRigWorktree(dogPath, dogName, rigName string) (string, error) {
	rigPath := filepath.Join(m.townRoot, rigName)
	worktreePath := filepath.Join(dogPath, rigName)

	// Find the repo base (bare repo or mayor/rig)
	repoGit, err := m.findRepoBase(rigPath)
	if err != nil {
		return "", fmt.Errorf("finding repo base for %s: %w", rigName, err)
	}

	// Determine the start point for the new worktree
	// Use origin/<default-branch> to ensure we start from the rig's configured branch
	defaultBranch := "main"
	if rigCfg, err := rig.LoadRigConfig(rigPath); err == nil && rigCfg.DefaultBranch != "" {
		defaultBranch = rigCfg.DefaultBranch
	}
	startPoint := fmt.Sprintf("origin/%s", defaultBranch)

	// Unique branch per dog-rig combination
	branchName := fmt.Sprintf("dog/%s-%s-%d", dogName, rigName, time.Now().UnixMilli())

	// Create worktree with new branch from default branch
	if err := repoGit.WorktreeAddFromRef(worktreePath, branchName, startPoint); err != nil {
		return "", fmt.Errorf("creating worktree from %s: %w", startPoint, err)
	}

	return worktreePath, nil
}

// findRepoBase locates the git repo base for a rig.
// Prefers .repo.git (bare repo), falls back to mayor/rig.
func (m *Manager) findRepoBase(rigPath string) (*git.Git, error) {
	// Check for shared bare repo
	bareRepoPath := filepath.Join(rigPath, ".repo.git")
	if info, err := os.Stat(bareRepoPath); err == nil && info.IsDir() {
		return git.NewGitWithDir(bareRepoPath, ""), nil
	}

	// Fall back to mayor/rig
	mayorPath := filepath.Join(rigPath, "mayor", "rig")
	if _, err := os.Stat(mayorPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("no repo base found (neither .repo.git nor mayor/rig exists)")
	}
	return git.NewGit(mayorPath), nil
}

// Remove deletes a dog from the kennel.
// Removes all worktrees and the dog directory.
func (m *Manager) Remove(name string) error {
	if err := validateDogName(name); err != nil {
		return err
	}
	// Removal targets any kennel entry, including debris left by a failed Add
	// that never wrote .dog.json. Gating on exists() here would strand that
	// debris forever and permanently burn the name.
	if !m.dirExists(name) {
		return ErrDogNotFound
	}
	// Never destroy a known non-dog kennel occupant (e.g. the Boot watchdog).
	if m.isNonDogKennelEntry(name) {
		return fmt.Errorf("%s is not a dog (protected kennel occupant)", name)
	}

	dogPath := m.dogDir(name)

	// Load state to get worktree paths
	state, err := m.loadState(name)
	if err != nil {
		// State file may be missing, proceed with cleanup
		state = &DogState{Worktrees: make(map[string]string)}
	}

	// Debris from a failed Add has worktrees on disk but no state recording
	// them. Recover them from the directory itself, otherwise os.RemoveAll
	// below silently fails on registered git worktrees and the entry survives.
	worktrees := state.Worktrees
	if len(worktrees) == 0 {
		worktrees = m.discoverWorktrees(name)
	}

	// Remove worktrees from each rig
	for rigName, worktreePath := range worktrees {
		rigPath := filepath.Join(m.townRoot, rigName)
		repoGit, err := m.findRepoBase(rigPath)
		if err != nil {
			// Log but continue with other rigs
			style.PrintWarning("could not find repo base for %s: %v", rigName, err)
			continue
		}

		// Try to remove worktree properly
		if err := repoGit.WorktreeRemove(worktreePath, true); err != nil {
			// Log but continue - will remove directory below
			style.PrintWarning("could not remove worktree %s: %v", worktreePath, err)
		}

		// Prune stale entries
		_ = repoGit.WorktreePrune()
	}

	// Remove dog directory
	if err := os.RemoveAll(dogPath); err != nil {
		return fmt.Errorf("removing dog dir: %w", err)
	}

	// A rollback that cannot prove it removed the artifact must fail loudly.
	// Silent survival is what burns the name and empties the kennel.
	if _, err := os.Stat(dogPath); err == nil {
		return fmt.Errorf("dog dir %s still present after removal", dogPath)
	}

	return nil
}

// List returns all dogs in the kennel.
func (m *Manager) List() ([]*Dog, error) {
	entries, err := os.ReadDir(m.kennelPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading kennel: %w", err)
	}

	var dogs []*Dog
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		dog, err := m.Get(entry.Name())
		if err != nil {
			continue // Skip invalid dogs; ListDebris reports them
		}
		dogs = append(dogs, dog)
	}

	return dogs, nil
}

// ListDebris returns kennel entries that occupy a name but are not valid dogs,
// i.e. directories without .dog.json left behind by a failed Add. Reporting
// them is what turns a silently empty kennel into a diagnosable one.
func (m *Manager) ListDebris() ([]string, error) {
	entries, err := os.ReadDir(m.kennelPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading kennel: %w", err)
	}

	var debris []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if m.isNonDogKennelEntry(entry.Name()) {
			continue
		}
		if _, err := m.Get(entry.Name()); err != nil {
			debris = append(debris, entry.Name())
		}
	}
	sort.Strings(debris)
	return debris, nil
}

// Get returns a specific dog by name.
// Returns ErrDogNotFound if the dog directory or .dog.json state file doesn't exist.
func (m *Manager) Get(name string) (*Dog, error) {
	if err := validateDogName(name); err != nil {
		return nil, err
	}
	if !m.exists(name) {
		return nil, ErrDogNotFound
	}

	state, err := m.loadState(name)
	if err != nil {
		// No .dog.json means this isn't a valid dog worker
		// (e.g., "boot" is the boot watchdog using .boot-status.json, not a dog)
		return nil, ErrDogNotFound
	}

	return &Dog{
		Name:          name,
		State:         state.State,
		Path:          m.dogDir(name),
		Worktrees:     state.Worktrees,
		LastActive:    state.LastActive,
		Work:          state.Work,
		WorkStartedAt: state.WorkStartedAt,
		CreatedAt:     state.CreatedAt,
	}, nil
}

// SetState updates a dog's state and last-active timestamp.
func (m *Manager) SetState(name string, state State) error {
	if err := validateDogName(name); err != nil {
		return err
	}
	if !m.exists(name) {
		return ErrDogNotFound
	}

	// Acquire per-dog lock to prevent concurrent load-modify-save races
	fl, err := m.lockDog(name)
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	dogState, err := m.loadState(name)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	dogState.State = state
	dogState.LastActive = time.Now()
	dogState.UpdatedAt = time.Now()

	return m.saveState(name, dogState)
}

// AssignWork assigns work to a dog and sets it to working state.
func (m *Manager) AssignWork(name, work string) error {
	if err := validateDogName(name); err != nil {
		return err
	}
	if !m.exists(name) {
		return ErrDogNotFound
	}

	// Acquire per-dog lock to prevent concurrent load-modify-save races
	fl, err := m.lockDog(name)
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	state, err := m.loadState(name)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	state.State = StateWorking
	state.Work = work
	state.WorkStartedAt = time.Now()
	state.LastActive = time.Now()
	state.UpdatedAt = time.Now()

	return m.saveState(name, state)
}

// AssignWorkIfIdle assigns work only if the dog is still idle, returning the
// saved state so callers can later perform exact compare-and-clear cleanup.
func (m *Manager) AssignWorkIfIdle(name, work string) (*DogState, error) {
	if err := validateDogName(name); err != nil {
		return nil, err
	}
	if !m.exists(name) {
		return nil, ErrDogNotFound
	}

	fl, err := m.lockDog(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = fl.Unlock() }()

	state, err := m.loadState(name)
	if err != nil {
		return nil, fmt.Errorf("loading state: %w", err)
	}
	if state.State != StateIdle {
		return nil, ErrDogWorking
	}

	state.State = StateWorking
	state.Work = work
	state.WorkStartedAt = time.Now()
	state.LastActive = time.Now()
	state.UpdatedAt = time.Now()

	if err := m.saveState(name, state); err != nil {
		return nil, err
	}
	return state, nil
}

// ClearWork clears a dog's work assignment and sets it to idle.
func (m *Manager) ClearWork(name string) error {
	if err := validateDogName(name); err != nil {
		return err
	}
	if !m.exists(name) {
		return ErrDogNotFound
	}

	// Acquire per-dog lock to prevent concurrent load-modify-save races
	fl, err := m.lockDog(name)
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	state, err := m.loadState(name)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	state.State = StateIdle
	state.Work = ""
	state.WorkStartedAt = time.Time{}
	state.LastActive = time.Now()
	state.UpdatedAt = time.Now()

	return m.saveState(name, state)
}

// ClearWorkIfMatches clears a dog's work assignment only if it still matches
// the expected work and assignment timestamp. The compare-and-clear happens
// under the dog lock so failed dispatch cleanup cannot erase a newer assignment.
func (m *Manager) ClearWorkIfMatches(name, expectedWork string, expectedStartedAt time.Time) (bool, error) {
	if err := validateDogName(name); err != nil {
		return false, err
	}
	if !m.exists(name) {
		return false, ErrDogNotFound
	}

	fl, err := m.lockDog(name)
	if err != nil {
		return false, err
	}
	defer func() { _ = fl.Unlock() }()

	state, err := m.loadState(name)
	if err != nil {
		return false, fmt.Errorf("loading state: %w", err)
	}
	if state.Work != expectedWork || !state.WorkStartedAt.Equal(expectedStartedAt) {
		return false, nil
	}

	state.State = StateIdle
	state.Work = ""
	state.WorkStartedAt = time.Time{}
	state.LastActive = time.Now()
	state.UpdatedAt = time.Now()

	return true, m.saveState(name, state)
}

// Refresh recreates all worktrees for a dog with fresh branches.
// This is useful when worktrees have drifted or become stale.
// Each rig is refreshed atomically with a state save, so a failure at rig N
// leaves rigs 1..N-1 correctly updated and rigs N+1..M untouched.
func (m *Manager) Refresh(name string) error {
	if err := validateDogName(name); err != nil {
		return err
	}
	if !m.exists(name) {
		return ErrDogNotFound
	}

	// Acquire per-dog lock to prevent concurrent load-modify-save races
	fl, err := m.lockDog(name)
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	state, err := m.loadState(name)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	// Refuse to refresh a working dog — its agent is using the worktrees.
	if state.State == StateWorking {
		return ErrDogWorking
	}

	dogPath := m.dogDir(name)

	// Refresh each rig atomically: remove old, create new, persist state.
	for rigName := range m.rigsConfig.Rigs {
		rigPath := filepath.Join(m.townRoot, rigName)
		oldWorktreePath := state.Worktrees[rigName]

		// Find repo base
		repoGit, err := m.findRepoBase(rigPath)
		if err != nil {
			// Save partial progress before returning
			state.LastActive = time.Now()
			state.UpdatedAt = time.Now()
			_ = m.saveState(name, state)
			return fmt.Errorf("finding repo base for %s: %w", rigName, err)
		}

		// Remove old worktree if it exists
		if oldWorktreePath != "" {
			_ = repoGit.WorktreeRemove(oldWorktreePath, true)
			_ = os.RemoveAll(oldWorktreePath)
			_ = repoGit.WorktreePrune()
		}

		// Fetch latest from origin
		_ = repoGit.Fetch("origin")

		// Create fresh worktree
		worktreePath, err := m.createRigWorktree(dogPath, name, rigName)
		if err != nil {
			// Old worktree is gone but new one failed. Remove stale path
			// from state so it doesn't reference a deleted directory.
			delete(state.Worktrees, rigName)
			state.UpdatedAt = time.Now()
			_ = m.saveState(name, state)
			return fmt.Errorf("creating worktree for %s: %w", rigName, err)
		}

		// Persist state after each rig so completed rigs aren't lost on
		// a later failure.
		state.Worktrees[rigName] = worktreePath
		state.LastActive = time.Now()
		state.UpdatedAt = time.Now()
		if err := m.saveState(name, state); err != nil {
			return fmt.Errorf("saving state after refreshing %s: %w", rigName, err)
		}
	}

	return nil
}

// RefreshRig recreates the worktree for a specific rig.
func (m *Manager) RefreshRig(name, rigName string) error {
	if err := validateDogName(name); err != nil {
		return err
	}
	if !m.exists(name) {
		return ErrDogNotFound
	}

	if _, ok := m.rigsConfig.Rigs[rigName]; !ok {
		return fmt.Errorf("rig %s not found in config", rigName)
	}

	// Acquire per-dog lock to prevent concurrent load-modify-save races
	fl, err := m.lockDog(name)
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	state, err := m.loadState(name)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	// Refuse to refresh a working dog — its agent is using the worktrees.
	if state.State == StateWorking {
		return ErrDogWorking
	}

	dogPath := m.dogDir(name)
	rigPath := filepath.Join(m.townRoot, rigName)
	oldWorktreePath := state.Worktrees[rigName]

	// Find repo base
	repoGit, err := m.findRepoBase(rigPath)
	if err != nil {
		return fmt.Errorf("finding repo base: %w", err)
	}

	// Remove old worktree if it exists
	if oldWorktreePath != "" {
		_ = repoGit.WorktreeRemove(oldWorktreePath, true)
		_ = os.RemoveAll(oldWorktreePath)
		_ = repoGit.WorktreePrune()
	}

	// Fetch latest
	_ = repoGit.Fetch("origin")

	// Create fresh worktree
	worktreePath, err := m.createRigWorktree(dogPath, name, rigName)
	if err != nil {
		// Old worktree is gone but new one failed. Remove stale path
		// from state so it doesn't reference a deleted directory.
		delete(state.Worktrees, rigName)
		state.UpdatedAt = time.Now()
		_ = m.saveState(name, state)
		return fmt.Errorf("creating worktree: %w", err)
	}

	// Update state
	state.Worktrees[rigName] = worktreePath
	state.LastActive = time.Now()
	state.UpdatedAt = time.Now()

	return m.saveState(name, state)
}

// CleanupStaleBranches removes orphaned dog branches from all rigs.
// Returns total branches deleted across all rigs.
func (m *Manager) CleanupStaleBranches() (int, error) {
	totalDeleted := 0

	for rigName := range m.rigsConfig.Rigs {
		rigPath := filepath.Join(m.townRoot, rigName)
		repoGit, err := m.findRepoBase(rigPath)
		if err != nil {
			continue
		}

		deleted, err := m.cleanupStaleBranchesForRig(repoGit, rigName)
		if err != nil {
			style.PrintWarning("cleanup failed for rig %s: %v", rigName, err)
			continue
		}
		totalDeleted += deleted
	}

	return totalDeleted, nil
}

// cleanupStaleBranchesForRig removes orphaned dog branches in a specific rig.
func (m *Manager) cleanupStaleBranchesForRig(repoGit *git.Git, rigName string) (int, error) {
	// List all dog branches
	branches, err := repoGit.ListBranches("dog/*")
	if err != nil {
		return 0, err
	}

	if len(branches) == 0 {
		return 0, nil
	}

	// Get list of current dogs
	dogs, err := m.List()
	if err != nil {
		return 0, err
	}

	// Build set of current dog branches for this rig
	currentBranches := make(map[string]bool)
	for _, dog := range dogs {
		if dog.Worktrees != nil {
			if worktreePath, ok := dog.Worktrees[rigName]; ok {
				// Get branch name for this worktree
				worktreeGit := git.NewGit(worktreePath)
				if branch, err := worktreeGit.CurrentBranch(); err == nil {
					currentBranches[branch] = true
				}
			}
		}
	}

	// Delete orphaned branches
	deleted := 0
	for _, branch := range branches {
		if currentBranches[branch] {
			continue
		}
		if err := repoGit.DeleteBranch(branch, true); err != nil {
			style.PrintWarning("could not delete branch %s: %v", branch, err)
			continue
		}
		deleted++
	}

	return deleted, nil
}

// loadState loads a dog's state from .dog.json.
func (m *Manager) loadState(name string) (*DogState, error) {
	data, err := os.ReadFile(m.stateFilePath(name))
	if err != nil {
		return nil, err
	}

	var state DogState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}

	return &state, nil
}

// saveState saves a dog's state to .dog.json using atomic write (write-to-temp + rename).
// This prevents concurrent loadState from seeing a truncated/empty file.
func (m *Manager) saveState(name string, state *DogState) error {
	return atomicfile.WriteJSON(m.stateFilePath(name), state)
}

// GetIdleDog returns an idle dog suitable for work assignment.
// Returns nil if no idle dogs are available.
func (m *Manager) GetIdleDog() (*Dog, error) {
	dogs, err := m.List()
	if err != nil {
		return nil, err
	}

	for _, dog := range dogs {
		if dog.State == StateIdle {
			return dog, nil
		}
	}

	return nil, nil // No idle dogs
}

// IdleCount returns the number of idle dogs.
func (m *Manager) IdleCount() (int, error) {
	dogs, err := m.List()
	if err != nil {
		return 0, err
	}

	count := 0
	for _, dog := range dogs {
		if dog.State == StateIdle {
			count++
		}
	}
	return count, nil
}

// WorkingCount returns the number of working dogs.
func (m *Manager) WorkingCount() (int, error) {
	dogs, err := m.List()
	if err != nil {
		return 0, err
	}

	count := 0
	for _, dog := range dogs {
		if dog.State == StateWorking {
			count++
		}
	}
	return count, nil
}
