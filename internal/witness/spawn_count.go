package witness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/lock"
	"github.com/steveyegge/gastown/internal/workspace"
)

// respawnMu serializes in-process access to the respawn state file.
// Cross-process serialization is handled by lock.FlockAcquire on a
// sibling .flock file (see RecordBeadRespawn, ShouldBlockRespawn, etc.).
var respawnMu sync.Mutex

// beadRespawnRecord tracks how many times a single bead has been reset for re-dispatch.
type beadRespawnRecord struct {
	BeadID      string    `json:"bead_id"`
	Count       int       `json:"count"`
	LastRespawn time.Time `json:"last_respawn"`
}

// beadRespawnState holds respawn counts for all tracked beads.
type beadRespawnState struct {
	Beads       map[string]*beadRespawnRecord `json:"beads"`
	LastUpdated time.Time                     `json:"last_updated"`
}

func beadRespawnStateFile(townRoot string) string {
	return filepath.Join(townRoot, "witness", "bead-respawn-counts.json")
}

func loadBeadRespawnState(townRoot string) *beadRespawnState {
	data, err := os.ReadFile(beadRespawnStateFile(townRoot)) //nolint:gosec // G304: path from trusted townRoot
	if err != nil {
		return &beadRespawnState{Beads: make(map[string]*beadRespawnRecord)}
	}
	var state beadRespawnState
	if err := json.Unmarshal(data, &state); err != nil {
		return &beadRespawnState{Beads: make(map[string]*beadRespawnRecord)}
	}
	if state.Beads == nil {
		state.Beads = make(map[string]*beadRespawnRecord)
	}
	return &state
}

func saveBeadRespawnState(townRoot string, state *beadRespawnState) error {
	stateFile := beadRespawnStateFile(townRoot)
	if err := os.MkdirAll(filepath.Dir(stateFile), 0755); err != nil {
		return fmt.Errorf("creating witness dir: %w", err)
	}
	state.LastUpdated = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling respawn state: %w", err)
	}
	return os.WriteFile(stateFile, data, 0600)
}

// ShouldBlockRespawn returns true if the bead has already been respawned
// MaxBeadRespawns times (from operational config). When true, the caller
// should escalate to mayor instead of sending RECOVERED_BEAD to deacon
// for re-dispatch. This is the primary circuit breaker for spawn storms
// (clown show #22).
func ShouldBlockRespawn(workDir, beadID string) bool {
	respawnMu.Lock()
	defer respawnMu.Unlock()

	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		townRoot = workDir
	}
	maxRespawns := config.LoadOperationalConfig(townRoot).GetWitnessConfig().MaxBeadRespawnsV()

	// Cross-process flock to serialize with other witness instances.
	unlock, flockErr := lock.FlockAcquire(beadRespawnStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadBeadRespawnState(townRoot)
	rec, ok := state.Beads[beadID]
	if !ok {
		return false
	}
	return rec.Count >= maxRespawns
}

// RecordBeadRespawn increments the respawn count for beadID and returns the new count.
// workDir is the rig path; townRoot is resolved internally via workspace.Find.
// On state file errors the count is still incremented in memory and returned, so the
// caller can log/warn without blocking the respawn itself.
//
// Serialized via respawnMu (in-process) and flock (cross-process) to prevent
// concurrent patrol cycles from racing on the load-modify-save cycle.
func RecordBeadRespawn(workDir, beadID string) int {
	respawnMu.Lock()
	defer respawnMu.Unlock()

	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		townRoot = workDir
	}

	// Cross-process flock to serialize with other witness instances.
	unlock, flockErr := lock.FlockAcquire(beadRespawnStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadBeadRespawnState(townRoot)
	rec, ok := state.Beads[beadID]
	if !ok {
		rec = &beadRespawnRecord{BeadID: beadID}
		state.Beads[beadID] = rec
	}
	rec.Count++
	rec.LastRespawn = time.Now().UTC()
	_ = saveBeadRespawnState(townRoot, state) // Non-fatal: tracking failure must not block respawn
	return rec.Count
}

// RefundBeadRespawn decrements the respawn count for beadID and returns the new
// count, flooring at zero. It is the inverse of RecordBeadRespawn and exists so
// a dispatch that was charged but never established (failed reclaim, allocation,
// worktree setup or tmux startup) does not consume a circuit-breaker attempt.
//
// The count is charged before allocation so that concurrent spawners see the
// attempt immediately; this refunds it on the failure paths. Refunding the last
// charge removes the record entirely, matching ResetBeadRespawnCount so no
// zero-count residue is left behind.
//
// Serialized via respawnMu (in-process) and flock (cross-process), identically
// to RecordBeadRespawn.
func RefundBeadRespawn(workDir, beadID string) int {
	respawnMu.Lock()
	defer respawnMu.Unlock()

	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		townRoot = workDir
	}

	// Cross-process flock to serialize with other witness instances.
	unlock, flockErr := lock.FlockAcquire(beadRespawnStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadBeadRespawnState(townRoot)
	rec, ok := state.Beads[beadID]
	if !ok {
		return 0
	}
	rec.Count--
	if rec.Count <= 0 {
		delete(state.Beads, beadID)
		_ = saveBeadRespawnState(townRoot, state) // Non-fatal: mirrors RecordBeadRespawn
		return 0
	}
	rec.LastRespawn = time.Now().UTC()
	_ = saveBeadRespawnState(townRoot, state) // Non-fatal: mirrors RecordBeadRespawn
	return rec.Count
}

// ResetBeadRespawnCount resets the respawn counter for beadID to zero.
// Used by `gt sling respawn-reset` to allow re-dispatch after investigation.
func ResetBeadRespawnCount(workDir, beadID string) error {
	respawnMu.Lock()
	defer respawnMu.Unlock()

	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		townRoot = workDir
	}

	// Cross-process flock to serialize with other witness instances.
	unlock, flockErr := lock.FlockAcquire(beadRespawnStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadBeadRespawnState(townRoot)
	delete(state.Beads, beadID)
	return saveBeadRespawnState(townRoot, state)
}
