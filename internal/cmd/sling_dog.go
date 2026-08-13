package cmd

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/dog"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// maxDogPoolSize is the maximum number of dogs allowed in the pool.
// Pool dispatch auto-creates dogs up to this limit.
const maxDogPoolSize = 4

// IsDogTarget checks if target is a dog target pattern.
// Returns the dog name (or empty for pool dispatch) and true if it's a dog target.
// Patterns:
//   - "deacon/dogs" -> ("", true) - dispatch to any idle dog
//   - "deacon/dogs/alpha" -> ("alpha", true) - dispatch to specific dog
//   - "dog:" -> ("", true) - dispatch to any idle dog (shorthand)
//   - "dog:alpha" -> ("alpha", true) - dispatch to specific dog (shorthand)
func IsDogTarget(target string) (dogName string, isDog bool) {
	target = strings.ToLower(target)

	// Check for exact "deacon/dogs" (pool dispatch)
	if target == "deacon/dogs" || target == "dog:" {
		return "", true
	}

	// Check for "dog:<name>" shorthand (like rig:polecat syntax)
	if strings.HasPrefix(target, "dog:") {
		name := strings.TrimPrefix(target, "dog:")
		if name != "" && !strings.Contains(name, "/") {
			return name, true
		}
		return "", true // "dog:" without name = pool dispatch
	}

	// Check for "deacon/dogs/<name>" (specific dog)
	if strings.HasPrefix(target, "deacon/dogs/") {
		name := strings.TrimPrefix(target, "deacon/dogs/")
		if name != "" && !strings.Contains(name, "/") {
			return name, true
		}
	}

	return "", false
}

// DogDispatchOptions contains options for dispatching work to a dog.
type DogDispatchOptions struct {
	Create            bool         // Create dog if it doesn't exist
	WorkDesc          string       // Work description (formula or bead ID)
	WorkKind          dog.WorkKind // Whether WorkDesc is a source bead or formula
	DelaySessionStart bool         // If true, don't start session (caller will start later)
	AgentOverride     string       // Agent override (e.g., "codex", "gemini")
}

// DogDispatchInfo contains information about a dog dispatch.
type DogDispatchInfo struct {
	DogName string // Name of the dog
	AgentID string // Agent ID format (deacon/dogs/<name>)
	Pane    string // Tmux pane (empty if session start was delayed)
	Spawned bool   // True if dog was spawned (new)

	// Internal fields for delayed session start
	sessionDelayed bool
	townRoot       string
	workDesc       string
	workStartedAt  time.Time
	ownsWork       bool
	agentOverride  string
	rigsConfig     *config.RigsConfig
}

// DispatchToDog finds or spawns a dog for work dispatch.
// If dogName is empty, finds an idle dog from the pool.
// If opts.Create is true and no dogs exist, creates one.
// opts.WorkDesc is recorded in the dog's state so we know what it's working on.
// If opts.DelaySessionStart is true, the session is not started (caller must call StartDelayedSession).
func DispatchToDog(dogName string, opts DogDispatchOptions) (*DogDispatchInfo, error) {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return nil, fmt.Errorf("finding town root: %w", err)
	}

	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return nil, fmt.Errorf("loading rigs config: %w", err)
	}

	mgr := dog.NewManager(townRoot, rigsConfig)

	var targetDog *dog.Dog
	var spawned bool
	var workStartedAt time.Time

	if dogName != "" {
		// Specific dog requested
		targetDog, err = mgr.Get(dogName)
		if err != nil {
			if opts.Create {
				// Create the dog if it doesn't exist
				targetDog, err = mgr.Add(dogName)
				if err != nil {
					return nil, fmt.Errorf("creating dog %s: %w", dogName, err)
				}
				fmt.Printf("✓ Created dog %s\n", dogName)
				spawned = true
			} else {
				return nil, fmt.Errorf("dog %s not found (use --create to add)", dogName)
			}
		}

		agentID := fmt.Sprintf("deacon/dogs/%s", targetDog.Name)
		if existing, err := findHookedFormulaSingleton(townRoot, agentID, opts.WorkDesc); err != nil {
			return nil, fmt.Errorf("checking existing dog formula: %w", err)
		} else if existing != nil && dogWorksOnHook(targetDog, opts.WorkDesc, existing) {
			return &DogDispatchInfo{
				DogName:        targetDog.Name,
				AgentID:        agentID,
				Pane:           "",
				Spawned:        spawned,
				sessionDelayed: true,
				townRoot:       townRoot,
				workDesc:       opts.WorkDesc,
				workStartedAt:  targetDog.WorkStartedAt,
				ownsWork:       false,
				agentOverride:  opts.AgentOverride,
				rigsConfig:     rigsConfig,
			}, nil
		}
	} else {
		if existing, existingDogName, err := findHookedFormulaForDogPool(townRoot, opts.WorkDesc, func(hooked *beads.Issue, candidateDogName string) bool {
			candidateDog, getErr := mgr.Get(candidateDogName)
			return getErr == nil && dogWorksOnHook(candidateDog, opts.WorkDesc, hooked)
		}); err != nil {
			return nil, fmt.Errorf("checking existing dog formula: %w", err)
		} else if existing != nil {
			targetDog, err = mgr.Get(existingDogName)
			if err == nil && dogWorksOnHook(targetDog, opts.WorkDesc, existing) {
				agentID := fmt.Sprintf("deacon/dogs/%s", targetDog.Name)
				return &DogDispatchInfo{
					DogName:        targetDog.Name,
					AgentID:        agentID,
					Pane:           "",
					Spawned:        false,
					sessionDelayed: true,
					townRoot:       townRoot,
					workDesc:       opts.WorkDesc,
					workStartedAt:  targetDog.WorkStartedAt,
					ownsWork:       false,
					agentOverride:  opts.AgentOverride,
					rigsConfig:     rigsConfig,
				}, nil
			}
		}

		// Pool dispatch - find an idle dog
		for {
			targetDog, err = mgr.GetIdleDog()
			if err != nil {
				return nil, fmt.Errorf("finding idle dog: %w", err)
			}

			if targetDog == nil {
				// No idle dogs - auto-create one if pool is under max size.
				// Pool dispatch means "send to any available dog" - if none exist,
				// spawning one is the natural behavior (see mol-deacon-patrol:
				// "Spawn on demand when pool is empty").
				dogs, listErr := mgr.List()
				if listErr != nil {
					return nil, fmt.Errorf("listing dogs: %w", listErr)
				}
				if len(dogs) >= maxDogPoolSize {
					return nil, fmt.Errorf("no idle dogs available (pool at max %d, all busy)", maxDogPoolSize)
				}
				newName := generateDogName(mgr)
				targetDog, err = mgr.Add(newName)
				if err != nil {
					return nil, fmt.Errorf("creating dog %s: %w", newName, err)
				}
				fmt.Printf("✓ Auto-created dog %s (no idle dogs, pool %d/%d)\n", newName, len(dogs)+1, maxDogPoolSize)
				spawned = true
			}

			assignedState, err := mgr.AssignWorkIfIdleWithKind(targetDog.Name, opts.WorkDesc, opts.WorkKind)
			if errors.Is(err, dog.ErrDogWorking) {
				spawned = false
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("assigning idle dog work: %w", err)
			}
			workStartedAt = assignedState.WorkStartedAt
			break
		}
	}

	if dogName != "" {
		assignedState, err := mgr.AssignWorkIfIdleWithKind(targetDog.Name, opts.WorkDesc, opts.WorkKind)
		if err != nil {
			return nil, fmt.Errorf("assigning idle dog work: %w", err)
		}
		workStartedAt = assignedState.WorkStartedAt
	}

	// Build agent ID
	agentID := fmt.Sprintf("deacon/dogs/%s", targetDog.Name)

	// If delayed start, return info for later session start
	if opts.DelaySessionStart {
		fmt.Printf("Dog %s assigned (session start delayed)\n", targetDog.Name)
		return &DogDispatchInfo{
			DogName:        targetDog.Name,
			AgentID:        agentID,
			Pane:           "", // No pane yet
			Spawned:        spawned,
			sessionDelayed: true,
			townRoot:       townRoot,
			workDesc:       opts.WorkDesc,
			workStartedAt:  workStartedAt,
			ownsWork:       true,
			agentOverride:  opts.AgentOverride,
			rigsConfig:     rigsConfig,
		}, nil
	}

	// Ensure dog session is running (start if needed)
	t := tmux.NewTmux()
	sessMgr := dog.NewSessionManager(t, townRoot, mgr)

	sessOpts := dog.SessionStartOptions{
		WorkDesc:      opts.WorkDesc,
		AgentOverride: opts.AgentOverride,
	}
	pane, err := sessMgr.EnsureRunning(targetDog.Name, sessOpts)
	if err != nil {
		// Log but don't fail - dog state is set, session may start later
		style.PrintWarning("could not start dog session: %v", err)
		pane = ""
	}

	return &DogDispatchInfo{
		DogName:       targetDog.Name,
		AgentID:       agentID,
		Pane:          pane,
		Spawned:       spawned,
		workStartedAt: workStartedAt,
		ownsWork:      true,
	}, nil
}

func dogWorksOn(d *dog.Dog, work string) bool {
	return d != nil && d.State == dog.StateWorking && d.Work == work
}

func dogWorksOnHook(d *dog.Dog, work string, hooked *beads.Issue) bool {
	if !dogWorksOn(d, work) || d.WorkStartedAt.IsZero() {
		return false
	}
	fields := beads.ParseAttachmentFields(hooked)
	if fields == nil || fields.AttachedAt == "" {
		return false
	}
	attachedAt, err := time.Parse(time.RFC3339Nano, fields.AttachedAt)
	if err != nil {
		return false
	}
	return !attachedAt.Before(d.WorkStartedAt.UTC())
}

func (d *DogDispatchInfo) worksOnHook(hooked *beads.Issue) bool {
	if d == nil {
		return false
	}
	return dogWorksOnHook(&dog.Dog{
		Name:          d.DogName,
		State:         dog.StateWorking,
		Work:          d.workDesc,
		WorkStartedAt: d.workStartedAt,
	}, d.workDesc, hooked)
}

// verifyBareBeadAssignment performs the consumer-side readback required before
// starting a dog session. Source status alone is insufficient: dog state and
// the startup lookup must resolve the same bead. (GH#4516)
func (d *DogDispatchInfo) verifyBareBeadAssignment(beadID string) error {
	return d.verifyAssignment(beadID, beadID, dog.WorkKindBead)
}

// verifyFormulaAssignment applies the same consumer-side check to formula work.
// The dog state names the formula while startup resolves the hooked source/wisp.
func (d *DogDispatchInfo) verifyFormulaAssignment(sourceID string) error {
	return d.verifyAssignment(sourceID, d.workDesc, dog.WorkKindFormula)
}

func (d *DogDispatchInfo) verifyAssignment(sourceID, expectedWork string, expectedKind dog.WorkKind) error {
	if d == nil {
		return fmt.Errorf("missing dog dispatch state")
	}

	mgr := dog.NewManager(d.townRoot, d.rigsConfig)
	current, err := mgr.Get(d.DogName)
	if err != nil {
		return fmt.Errorf("reading dog state: %w", err)
	}
	if current.State != dog.StateWorking || current.Work != expectedWork || current.WorkKind != expectedKind ||
		current.WorkStartedAt.IsZero() || !current.WorkStartedAt.Equal(d.workStartedAt) {
		return fmt.Errorf("dog state mismatch: state=%q work=%q kind=%q started=%s; want working work=%q kind=%q started=%s",
			current.State, current.Work, current.WorkKind, current.WorkStartedAt.Format(time.RFC3339Nano),
			expectedWork, expectedKind, d.workStartedAt.Format(time.RFC3339Nano))
	}

	source, err := getBeadInfoFromTownRoot(d.townRoot, sourceID)
	if err != nil {
		return fmt.Errorf("reading source bead: %w", err)
	}
	if source.Status != beads.StatusHooked || source.Assignee != d.AgentID {
		return fmt.Errorf("source bead mismatch: status=%q assignee=%q; want hooked assignee=%q",
			source.Status, source.Assignee, d.AgentID)
	}

	resolved, err := findAssignedDogWork(RoleContext{
		Role:     RoleDog,
		Polecat:  d.DogName,
		TownRoot: d.townRoot,
		WorkDir:  filepath.Join(d.townRoot, "deacon", "dogs", d.DogName),
	}, d.AgentID)
	if err != nil {
		return fmt.Errorf("resolving dog startup hook: %w", err)
	}
	if resolved == nil || resolved.ID != sourceID {
		resolvedID := ""
		if resolved != nil {
			resolvedID = resolved.ID
		}
		return fmt.Errorf("dog startup hook mismatch: resolved=%q; want %q", resolvedID, sourceID)
	}
	return nil
}

// StartDelayedSession starts the dog session after bead setup is complete.
// This should only be called when DelaySessionStart was true during dispatch.
func (d *DogDispatchInfo) StartDelayedSession() (string, error) {
	if !d.sessionDelayed {
		return d.Pane, nil // Session was already started
	}

	t := tmux.NewTmux()
	mgr := dog.NewManager(d.townRoot, d.rigsConfig)
	sessMgr := dog.NewSessionManager(t, d.townRoot, mgr)

	opts := dog.SessionStartOptions{
		WorkDesc:      d.workDesc,
		AgentOverride: d.agentOverride,
	}
	pane, err := sessMgr.EnsureRunning(d.DogName, opts)
	if err != nil {
		if errors.Is(err, dog.ErrSessionRunning) {
			d.Pane = ""
			d.sessionDelayed = false
			return "", nil
		}
		return "", fmt.Errorf("starting dog session: %w", err)
	}

	d.Pane = pane
	d.sessionDelayed = false
	return pane, nil
}

func (d *DogDispatchInfo) clearWorkIfMatches() error {
	_, err := d.clearWorkIfMatchesResult()
	return err
}

func (d *DogDispatchInfo) clearWorkIfMatchesResult() (bool, error) {
	if d == nil || !d.ownsWork {
		return false, nil
	}
	mgr := dog.NewManager(d.townRoot, d.rigsConfig)
	return mgr.ClearWorkIfMatches(d.DogName, d.workDesc, d.workStartedAt)
}

func (d *DogDispatchInfo) clearWorkIfMatchesAfter(beforeClear func() bool) (bool, error) {
	if d == nil || !d.ownsWork {
		return false, nil
	}
	mgr := dog.NewManager(d.townRoot, d.rigsConfig)
	return mgr.ClearWorkIfMatchesAfter(d.DogName, d.workDesc, d.workStartedAt, beforeClear)
}

// generateDogName creates a unique dog name for pool expansion.
func generateDogName(mgr *dog.Manager) string {
	// Use Greek alphabet for dog names
	names := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"}

	dogs, _ := mgr.List()
	existing := make(map[string]bool)
	for _, d := range dogs {
		existing[d.Name] = true
	}

	for _, name := range names {
		if !existing[name] {
			return name
		}
	}

	// Fallback: numbered dogs
	for i := 1; i <= 100; i++ {
		name := fmt.Sprintf("dog%d", i)
		if !existing[name] {
			return name
		}
	}

	return fmt.Sprintf("dog%d", len(dogs)+1)
}
