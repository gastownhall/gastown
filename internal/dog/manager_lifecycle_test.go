package dog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
)

// =============================================================================
// Test Fixtures
// =============================================================================

// testManager creates a Manager with a temporary town root for testing.
func testManager(t *testing.T) (*Manager, string) {
	t.Helper()
	tmpDir := t.TempDir()

	rigsConfig := &config.RigsConfig{
		Version: 1,
		Rigs: map[string]config.RigEntry{
			"gastown": {GitURL: "git@github.com:test/gastown.git"},
			"beads":   {GitURL: "git@github.com:test/beads.git"},
		},
	}

	m := NewManager(tmpDir, rigsConfig)
	return m, tmpDir
}

// testManagerNoRigs creates a Manager with no rigs configured.
func testManagerNoRigs(t *testing.T) (*Manager, string) {
	t.Helper()
	tmpDir := t.TempDir()

	rigsConfig := &config.RigsConfig{
		Version: 1,
		Rigs:    map[string]config.RigEntry{},
	}

	m := NewManager(tmpDir, rigsConfig)
	return m, tmpDir
}

// setupDogWithState creates a dog directory with a state file for testing.
// This bypasses Add() to test functions that don't require git worktrees.
func setupDogWithState(t *testing.T, m *Manager, name string, state *DogState) {
	t.Helper()

	dogPath := m.dogDir(name)
	if err := os.MkdirAll(dogPath, 0755); err != nil {
		t.Fatalf("Failed to create dog dir: %v", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal state: %v", err)
	}

	statePath := m.stateFilePath(name)
	if err := os.WriteFile(statePath, data, 0644); err != nil {
		t.Fatalf("Failed to write state file: %v", err)
	}
}

// =============================================================================
// Manager Creation Tests
// =============================================================================

func TestNewManager_PathConstruction(t *testing.T) {
	rigsConfig := &config.RigsConfig{
		Version: 1,
		Rigs: map[string]config.RigEntry{
			"testrig": {GitURL: "git@github.com:test/rig.git"},
		},
	}

	tests := []struct {
		name     string
		townRoot string
	}{
		{
			name:     "standard path",
			townRoot: "/home/user/gt",
		},
		{
			name:     "path with trailing slash",
			townRoot: "/tmp/town/",
		},
		{
			name:     "nested path",
			townRoot: "/a/b/c/d/e",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewManager(tt.townRoot, rigsConfig)

			if m.townRoot != tt.townRoot {
				t.Errorf("townRoot = %q, want %q", m.townRoot, tt.townRoot)
			}
			wantKennelPath := filepath.Join(tt.townRoot, "deacon", "dogs")
			if m.kennelPath != wantKennelPath {
				t.Errorf("kennelPath = %q, want %q", m.kennelPath, wantKennelPath)
			}
			if m.rigsConfig != rigsConfig {
				t.Error("rigsConfig not properly stored")
			}
		})
	}
}

// =============================================================================
// Dog Directory and Path Tests
// =============================================================================

func TestManager_dogDir(t *testing.T) {
	m, _ := testManager(t)

	tests := []struct {
		name     string
		dogName  string
		wantPath string
	}{
		{"simple name", "alpha", filepath.Join(m.kennelPath, "alpha")},
		{"hyphenated name", "my-dog", filepath.Join(m.kennelPath, "my-dog")},
		{"numeric name", "dog123", filepath.Join(m.kennelPath, "dog123")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.dogDir(tt.dogName)
			if got != tt.wantPath {
				t.Errorf("dogDir(%q) = %q, want %q", tt.dogName, got, tt.wantPath)
			}
		})
	}
}

func TestManager_stateFilePath(t *testing.T) {
	m, _ := testManager(t)

	dogName := "testdog"
	want := filepath.Join(m.kennelPath, dogName, ".dog.json")
	got := m.stateFilePath(dogName)

	if got != want {
		t.Errorf("stateFilePath(%q) = %q, want %q", dogName, got, want)
	}
}

func TestManager_exists(t *testing.T) {
	m, tmpDir := testManager(t)

	// Create a dog directory manually
	dogPath := filepath.Join(tmpDir, "deacon", "dogs", "existing-dog")
	if err := os.MkdirAll(dogPath, 0755); err != nil {
		t.Fatalf("Failed to create dog dir: %v", err)
	}

	tests := []struct {
		name    string
		dogName string
		want    bool
	}{
		{"existing dog", "existing-dog", true},
		{"non-existing dog", "ghost-dog", false},
		// Note: empty name returns true because dogDir("") == kennelPath which exists
		// This is an edge case that callers should avoid
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.exists(tt.dogName)
			if got != tt.want {
				t.Errorf("exists(%q) = %v, want %v", tt.dogName, got, tt.want)
			}
		})
	}
}

// =============================================================================
// State Load/Save Tests
// =============================================================================

func TestManager_saveState_loadState_roundtrip(t *testing.T) {
	m, tmpDir := testManager(t)

	// Create dog directory
	dogPath := filepath.Join(tmpDir, "deacon", "dogs", "testdog")
	if err := os.MkdirAll(dogPath, 0755); err != nil {
		t.Fatalf("Failed to create dog dir: %v", err)
	}

	now := time.Now().Round(time.Second)
	originalState := &DogState{
		Name:       "testdog",
		State:      StateWorking,
		LastActive: now,
		Work:       "hq-abc123",
		Worktrees: map[string]string{
			"gastown": "/path/to/gastown",
			"beads":   "/path/to/beads",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Save state
	if err := m.saveState("testdog", originalState); err != nil {
		t.Fatalf("saveState() error = %v", err)
	}

	// Verify file exists
	statePath := m.stateFilePath("testdog")
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		t.Fatal("State file was not created")
	}

	// Load state back
	loadedState, err := m.loadState("testdog")
	if err != nil {
		t.Fatalf("loadState() error = %v", err)
	}

	// Verify all fields
	if loadedState.Name != originalState.Name {
		t.Errorf("Name = %q, want %q", loadedState.Name, originalState.Name)
	}
	if loadedState.State != originalState.State {
		t.Errorf("State = %q, want %q", loadedState.State, originalState.State)
	}
	if loadedState.Work != originalState.Work {
		t.Errorf("Work = %q, want %q", loadedState.Work, originalState.Work)
	}
	if len(loadedState.Worktrees) != len(originalState.Worktrees) {
		t.Errorf("Worktrees len = %d, want %d", len(loadedState.Worktrees), len(originalState.Worktrees))
	}
}

func TestManager_loadState_nonExistent(t *testing.T) {
	m, _ := testManager(t)

	_, err := m.loadState("nonexistent")
	if err == nil {
		t.Error("loadState() expected error for non-existent dog")
	}
}

func TestManager_loadState_invalidJSON(t *testing.T) {
	m, tmpDir := testManager(t)

	// Create dog directory with invalid JSON
	dogPath := filepath.Join(tmpDir, "deacon", "dogs", "baddog")
	if err := os.MkdirAll(dogPath, 0755); err != nil {
		t.Fatalf("Failed to create dog dir: %v", err)
	}

	statePath := filepath.Join(dogPath, ".dog.json")
	if err := os.WriteFile(statePath, []byte("invalid json {{{"), 0644); err != nil {
		t.Fatalf("Failed to write invalid state: %v", err)
	}

	_, err := m.loadState("baddog")
	if err == nil {
		t.Error("loadState() expected error for invalid JSON")
	}
}

// =============================================================================
// Get Dog Tests
// =============================================================================

func TestManager_Get_success(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now().Round(time.Second)
	state := &DogState{
		Name:       "alpha",
		State:      StateWorking,
		LastActive: now,
		Work:       "test-work",
		Worktrees: map[string]string{
			"gastown": "/path/gastown",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	setupDogWithState(t, m, "alpha", state)

	dog, err := m.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if dog.Name != "alpha" {
		t.Errorf("Name = %q, want %q", dog.Name, "alpha")
	}
	if dog.State != StateWorking {
		t.Errorf("State = %q, want %q", dog.State, StateWorking)
	}
	if dog.Work != "test-work" {
		t.Errorf("Work = %q, want %q", dog.Work, "test-work")
	}
	if dog.Worktrees["gastown"] != "/path/gastown" {
		t.Errorf("Worktrees[gastown] = %q, want %q", dog.Worktrees["gastown"], "/path/gastown")
	}
}

func TestManager_Get_notFound(t *testing.T) {
	m, _ := testManager(t)

	_, err := m.Get("nonexistent")
	if err != ErrDogNotFound {
		t.Errorf("Get() error = %v, want ErrDogNotFound", err)
	}
}

func TestManager_Get_dirExistsButNoStateFile(t *testing.T) {
	m, tmpDir := testManager(t)

	// Create dog directory but no .dog.json (e.g., boot watchdog)
	dogPath := filepath.Join(tmpDir, "deacon", "dogs", "boot")
	if err := os.MkdirAll(dogPath, 0755); err != nil {
		t.Fatalf("Failed to create dog dir: %v", err)
	}

	_, err := m.Get("boot")
	if err != ErrDogNotFound {
		t.Errorf("Get() error = %v, want ErrDogNotFound for dir without state file", err)
	}
}

// =============================================================================
// List Dogs Tests
// =============================================================================

func TestManager_List_empty(t *testing.T) {
	m, _ := testManager(t)

	dogs, err := m.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(dogs) != 0 {
		t.Errorf("List() returned %d dogs, want 0", len(dogs))
	}
}

func TestManager_List_multipleDogs(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	// Create multiple dogs
	for _, name := range []string{"alpha", "beta", "gamma"} {
		state := &DogState{
			Name:       name,
			State:      StateIdle,
			LastActive: now,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		setupDogWithState(t, m, name, state)
	}

	dogs, err := m.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(dogs) != 3 {
		t.Errorf("List() returned %d dogs, want 3", len(dogs))
	}

	// Verify all dogs are present
	names := make(map[string]bool)
	for _, dog := range dogs {
		names[dog.Name] = true
	}
	for _, expected := range []string{"alpha", "beta", "gamma"} {
		if !names[expected] {
			t.Errorf("List() missing dog %q", expected)
		}
	}
}

func TestManager_List_skipsInvalidDogs(t *testing.T) {
	m, tmpDir := testManager(t)

	now := time.Now()
	// Create one valid dog
	state := &DogState{
		Name:       "valid",
		State:      StateIdle,
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupDogWithState(t, m, "valid", state)

	// Create directory without state file (should be skipped)
	invalidPath := filepath.Join(tmpDir, "deacon", "dogs", "invalid")
	if err := os.MkdirAll(invalidPath, 0755); err != nil {
		t.Fatalf("Failed to create invalid dir: %v", err)
	}

	dogs, err := m.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(dogs) != 1 {
		t.Errorf("List() returned %d dogs, want 1", len(dogs))
	}
	if dogs[0].Name != "valid" {
		t.Errorf("List() returned dog %q, want 'valid'", dogs[0].Name)
	}
}

// =============================================================================
// SetState Tests
// =============================================================================

func TestManager_SetState_success(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	state := &DogState{
		Name:       "alpha",
		State:      StateIdle,
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupDogWithState(t, m, "alpha", state)

	// Change state to working
	if err := m.SetState("alpha", StateWorking); err != nil {
		t.Fatalf("SetState() error = %v", err)
	}

	// Verify the state was updated
	dog, err := m.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if dog.State != StateWorking {
		t.Errorf("State = %q, want %q", dog.State, StateWorking)
	}
}

func TestManager_SetState_notFound(t *testing.T) {
	m, _ := testManager(t)

	err := m.SetState("nonexistent", StateWorking)
	if err != ErrDogNotFound {
		t.Errorf("SetState() error = %v, want ErrDogNotFound", err)
	}
}

func TestManager_SetState_updatesTimestamp(t *testing.T) {
	m, _ := testManager(t)

	oldTime := time.Now().Add(-1 * time.Hour)
	state := &DogState{
		Name:       "alpha",
		State:      StateIdle,
		LastActive: oldTime,
		CreatedAt:  oldTime,
		UpdatedAt:  oldTime,
	}
	setupDogWithState(t, m, "alpha", state)

	beforeUpdate := time.Now()
	time.Sleep(10 * time.Millisecond) // Ensure time difference

	if err := m.SetState("alpha", StateWorking); err != nil {
		t.Fatalf("SetState() error = %v", err)
	}

	dog, _ := m.Get("alpha")
	if !dog.LastActive.After(beforeUpdate) {
		t.Errorf("LastActive was not updated")
	}
}

// =============================================================================
// AssignWork Tests
// =============================================================================

func TestManager_AssignWork_success(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	state := &DogState{
		Name:       "alpha",
		State:      StateIdle,
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupDogWithState(t, m, "alpha", state)

	// Assign work
	if err := m.AssignWork("alpha", "hq-xyz789"); err != nil {
		t.Fatalf("AssignWork() error = %v", err)
	}

	// Verify work assignment
	dog, err := m.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if dog.State != StateWorking {
		t.Errorf("State = %q, want %q after AssignWork", dog.State, StateWorking)
	}
	if dog.Work != "hq-xyz789" {
		t.Errorf("Work = %q, want %q", dog.Work, "hq-xyz789")
	}
}

func TestManager_AssignWork_notFound(t *testing.T) {
	m, _ := testManager(t)

	err := m.AssignWork("nonexistent", "some-work")
	if err != ErrDogNotFound {
		t.Errorf("AssignWork() error = %v, want ErrDogNotFound", err)
	}
}

func TestManager_AssignWorkIfIdle_success(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	state := &DogState{
		Name:       "alpha",
		State:      StateIdle,
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupDogWithState(t, m, "alpha", state)

	assigned, err := m.AssignWorkIfIdle("alpha", "mol-dog-reaper")
	if err != nil {
		t.Fatalf("AssignWorkIfIdle() error = %v", err)
	}
	if assigned.State != StateWorking {
		t.Errorf("State = %q, want %q", assigned.State, StateWorking)
	}
	if assigned.Work != "mol-dog-reaper" {
		t.Errorf("Work = %q, want mol-dog-reaper", assigned.Work)
	}
	if assigned.WorkStartedAt.IsZero() {
		t.Fatal("WorkStartedAt should be set")
	}
}

func TestManager_AssignWorkIfIdle_workingDog(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	state := &DogState{
		Name:       "alpha",
		State:      StateWorking,
		Work:       "existing-work",
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupDogWithState(t, m, "alpha", state)

	assigned, err := m.AssignWorkIfIdle("alpha", "mol-dog-reaper")
	if err != ErrDogWorking {
		t.Fatalf("AssignWorkIfIdle() error = %v, want ErrDogWorking", err)
	}
	if assigned != nil {
		t.Fatalf("AssignWorkIfIdle() assigned = %#v, want nil", assigned)
	}

	dog, err := m.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if dog.Work != "existing-work" {
		t.Errorf("Work = %q, want existing-work", dog.Work)
	}
}

// =============================================================================
// ClearWork Tests
// =============================================================================

func TestManager_ClearWork_success(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	state := &DogState{
		Name:       "alpha",
		State:      StateWorking,
		Work:       "existing-work",
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupDogWithState(t, m, "alpha", state)

	// Clear work
	if err := m.ClearWork("alpha"); err != nil {
		t.Fatalf("ClearWork() error = %v", err)
	}

	// Verify work was cleared
	dog, err := m.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if dog.State != StateIdle {
		t.Errorf("State = %q, want %q after ClearWork", dog.State, StateIdle)
	}
	if dog.Work != "" {
		t.Errorf("Work = %q, want empty after ClearWork", dog.Work)
	}
}

func TestManager_ClearWork_notFound(t *testing.T) {
	m, _ := testManager(t)

	err := m.ClearWork("nonexistent")
	if err != ErrDogNotFound {
		t.Errorf("ClearWork() error = %v, want ErrDogNotFound", err)
	}
}

func TestManager_ClearWorkIfMatches_success(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	state := &DogState{
		Name:          "alpha",
		State:         StateWorking,
		Work:          "mol-dog-reaper",
		WorkStartedAt: now.Add(-time.Minute),
		LastActive:    now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	setupDogWithState(t, m, "alpha", state)

	cleared, err := m.ClearWorkIfMatches("alpha", "mol-dog-reaper", state.WorkStartedAt)
	if err != nil {
		t.Fatalf("ClearWorkIfMatches() error = %v", err)
	}
	if !cleared {
		t.Fatal("ClearWorkIfMatches() cleared = false, want true")
	}

	dog, err := m.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if dog.State != StateIdle {
		t.Errorf("State = %q, want %q", dog.State, StateIdle)
	}
	if dog.Work != "" {
		t.Errorf("Work = %q, want empty", dog.Work)
	}
	if !dog.WorkStartedAt.IsZero() {
		t.Errorf("WorkStartedAt = %v, want zero", dog.WorkStartedAt)
	}
}

func TestManager_ClearWorkIfMatches_mismatchNoOp(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now().Truncate(time.Second)
	workStarted := now.Add(-time.Minute)
	state := &DogState{
		Name:          "alpha",
		State:         StateWorking,
		Work:          "other-work",
		WorkStartedAt: workStarted,
		LastActive:    now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	setupDogWithState(t, m, "alpha", state)

	cleared, err := m.ClearWorkIfMatches("alpha", "mol-dog-reaper", workStarted)
	if err != nil {
		t.Fatalf("ClearWorkIfMatches() error = %v", err)
	}
	if cleared {
		t.Fatal("ClearWorkIfMatches() cleared = true, want false")
	}

	dog, err := m.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if dog.State != StateWorking {
		t.Errorf("State = %q, want %q", dog.State, StateWorking)
	}
	if dog.Work != "other-work" {
		t.Errorf("Work = %q, want other-work", dog.Work)
	}
	if !dog.WorkStartedAt.Equal(workStarted) {
		t.Errorf("WorkStartedAt = %v, want %v", dog.WorkStartedAt, workStarted)
	}
	if !dog.LastActive.Equal(now) {
		t.Errorf("LastActive = %v, want %v", dog.LastActive, now)
	}
}

func TestManager_ClearWorkIfMatches_sameWorkDifferentTimestampNoOp(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now().Truncate(time.Second)
	workStarted := now.Add(-time.Minute)
	state := &DogState{
		Name:          "alpha",
		State:         StateWorking,
		Work:          "mol-dog-reaper",
		WorkStartedAt: workStarted,
		LastActive:    now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	setupDogWithState(t, m, "alpha", state)

	cleared, err := m.ClearWorkIfMatches("alpha", "mol-dog-reaper", workStarted.Add(time.Second))
	if err != nil {
		t.Fatalf("ClearWorkIfMatches() error = %v", err)
	}
	if cleared {
		t.Fatal("ClearWorkIfMatches() cleared = true, want false")
	}

	dog, err := m.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if dog.State != StateWorking {
		t.Errorf("State = %q, want %q", dog.State, StateWorking)
	}
	if dog.Work != "mol-dog-reaper" {
		t.Errorf("Work = %q, want mol-dog-reaper", dog.Work)
	}
	if !dog.WorkStartedAt.Equal(workStarted) {
		t.Errorf("WorkStartedAt = %v, want %v", dog.WorkStartedAt, workStarted)
	}
}

func TestManager_ClearWorkIfMatches_notFound(t *testing.T) {
	m, root := testManager(t)

	cleared, err := m.ClearWorkIfMatches("nonexistent", "mol-dog-reaper", time.Now())
	if err != ErrDogNotFound {
		t.Fatalf("ClearWorkIfMatches() error = %v, want ErrDogNotFound", err)
	}
	if cleared {
		t.Fatal("ClearWorkIfMatches() cleared = true, want false")
	}
	if _, statErr := os.Stat(filepath.Join(root, "deacon", "dogs", "nonexistent")); !os.IsNotExist(statErr) {
		t.Fatalf("ClearWorkIfMatches() created dog dir unexpectedly: %v", statErr)
	}
}

// =============================================================================
// GetIdleDog Tests
// =============================================================================

func TestManager_GetIdleDog_noDogsReturnsNil(t *testing.T) {
	m, _ := testManager(t)

	dog, err := m.GetIdleDog()
	if err != nil {
		t.Fatalf("GetIdleDog() error = %v", err)
	}
	if dog != nil {
		t.Errorf("GetIdleDog() = %v, want nil when no dogs", dog)
	}
}

func TestManager_GetIdleDog_allWorkingReturnsNil(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	for _, name := range []string{"alpha", "beta"} {
		state := &DogState{
			Name:       name,
			State:      StateWorking,
			LastActive: now,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		setupDogWithState(t, m, name, state)
	}

	dog, err := m.GetIdleDog()
	if err != nil {
		t.Fatalf("GetIdleDog() error = %v", err)
	}
	if dog != nil {
		t.Errorf("GetIdleDog() = %v, want nil when all dogs working", dog)
	}
}

func TestManager_GetIdleDog_findsIdleDog(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	// Create working dog
	workingState := &DogState{
		Name:       "worker",
		State:      StateWorking,
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupDogWithState(t, m, "worker", workingState)

	// Create idle dog
	idleState := &DogState{
		Name:       "idler",
		State:      StateIdle,
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupDogWithState(t, m, "idler", idleState)

	dog, err := m.GetIdleDog()
	if err != nil {
		t.Fatalf("GetIdleDog() error = %v", err)
	}
	if dog == nil {
		t.Fatal("GetIdleDog() returned nil, want idle dog")
	}
	if dog.Name != "idler" {
		t.Errorf("GetIdleDog().Name = %q, want 'idler'", dog.Name)
	}
}

// =============================================================================
// IdleCount and WorkingCount Tests
// =============================================================================

func TestManager_IdleCount(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	// 2 idle, 1 working
	for i, name := range []string{"idle1", "idle2", "working1"} {
		state := StateIdle
		if i == 2 {
			state = StateWorking
		}
		dogState := &DogState{
			Name:       name,
			State:      state,
			LastActive: now,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		setupDogWithState(t, m, name, dogState)
	}

	count, err := m.IdleCount()
	if err != nil {
		t.Fatalf("IdleCount() error = %v", err)
	}
	if count != 2 {
		t.Errorf("IdleCount() = %d, want 2", count)
	}
}

func TestManager_WorkingCount(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	// 1 idle, 2 working
	for i, name := range []string{"idle1", "working1", "working2"} {
		state := StateIdle
		if i > 0 {
			state = StateWorking
		}
		dogState := &DogState{
			Name:       name,
			State:      state,
			LastActive: now,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		setupDogWithState(t, m, name, dogState)
	}

	count, err := m.WorkingCount()
	if err != nil {
		t.Fatalf("WorkingCount() error = %v", err)
	}
	if count != 2 {
		t.Errorf("WorkingCount() = %d, want 2", count)
	}
}

// =============================================================================
// Add Dog Tests (Spawn Behavior)
// =============================================================================

func TestManager_Add_noRigsReturnsError(t *testing.T) {
	m, _ := testManagerNoRigs(t)

	_, err := m.Add("alpha")
	if err != ErrNoRigs {
		t.Errorf("Add() error = %v, want ErrNoRigs", err)
	}
}

func TestManager_Add_duplicateReturnsError(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	state := &DogState{
		Name:       "existing",
		State:      StateIdle,
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupDogWithState(t, m, "existing", state)

	_, err := m.Add("existing")
	if err != ErrDogExists {
		t.Errorf("Add() error = %v, want ErrDogExists", err)
	}
}

// Note: Full Add() testing requires git repos. This tests error conditions only.
// Integration tests with real git repos should be in a separate _integration_test.go file.

// =============================================================================
// Remove Dog Tests (Kill Behavior)
// =============================================================================

func TestManager_Remove_notFound(t *testing.T) {
	m, _ := testManager(t)

	err := m.Remove("nonexistent")
	if err != ErrDogNotFound {
		t.Errorf("Remove() error = %v, want ErrDogNotFound", err)
	}
}

func TestManager_Remove_cleansUpDirectory(t *testing.T) {
	m, tmpDir := testManager(t)

	// Create a dog with minimal state (no worktrees to clean up)
	now := time.Now()
	state := &DogState{
		Name:       "doomed",
		State:      StateIdle,
		LastActive: now,
		Worktrees:  map[string]string{}, // Empty - no git cleanup needed
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupDogWithState(t, m, "doomed", state)

	// Verify dog exists
	dogPath := filepath.Join(tmpDir, "deacon", "dogs", "doomed")
	if _, err := os.Stat(dogPath); os.IsNotExist(err) {
		t.Fatal("Dog directory should exist before Remove")
	}

	// Remove the dog
	if err := m.Remove("doomed"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	// Verify directory was cleaned up
	if _, err := os.Stat(dogPath); !os.IsNotExist(err) {
		t.Error("Dog directory should not exist after Remove")
	}
}

func TestManager_Remove_handlesMissingStateFile(t *testing.T) {
	m, tmpDir := testManager(t)

	// Create dog directory but no state file
	dogPath := filepath.Join(tmpDir, "deacon", "dogs", "orphan")
	if err := os.MkdirAll(dogPath, 0755); err != nil {
		t.Fatalf("Failed to create dog dir: %v", err)
	}

	// Remove should still clean up the directory even without state file
	if err := m.Remove("orphan"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	// Verify directory was cleaned up
	if _, err := os.Stat(dogPath); !os.IsNotExist(err) {
		t.Error("Dog directory should not exist after Remove")
	}
}

// =============================================================================
// Refresh Tests (requires git, minimal unit testing)
// =============================================================================

func TestManager_Refresh_notFound(t *testing.T) {
	m, _ := testManager(t)

	err := m.Refresh("nonexistent")
	if err != ErrDogNotFound {
		t.Errorf("Refresh() error = %v, want ErrDogNotFound", err)
	}
}

func TestManager_RefreshRig_notFound(t *testing.T) {
	m, _ := testManager(t)

	err := m.RefreshRig("nonexistent", "gastown")
	if err != ErrDogNotFound {
		t.Errorf("RefreshRig() error = %v, want ErrDogNotFound", err)
	}
}

func TestManager_Refresh_rejectsWorkingDog(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	state := &DogState{
		Name:       "busy",
		State:      StateWorking,
		Work:       "important-task",
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupDogWithState(t, m, "busy", state)

	err := m.Refresh("busy")
	if err != ErrDogWorking {
		t.Errorf("Refresh() error = %v, want ErrDogWorking", err)
	}

	// Verify state was not modified
	dog, _ := m.Get("busy")
	if dog.State != StateWorking {
		t.Errorf("State changed to %q, want Working (unchanged)", dog.State)
	}
	if dog.Work != "important-task" {
		t.Errorf("Work changed to %q, want 'important-task' (unchanged)", dog.Work)
	}
}

func TestManager_RefreshRig_rejectsWorkingDog(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	state := &DogState{
		Name:       "busy",
		State:      StateWorking,
		Work:       "important-task",
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupDogWithState(t, m, "busy", state)

	err := m.RefreshRig("busy", "gastown")
	if err != ErrDogWorking {
		t.Errorf("RefreshRig() error = %v, want ErrDogWorking", err)
	}

	// Verify state was not modified
	dog, _ := m.Get("busy")
	if dog.State != StateWorking {
		t.Errorf("State changed to %q, want Working (unchanged)", dog.State)
	}
}

func TestManager_RefreshRig_unknownRig(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	state := &DogState{
		Name:       "alpha",
		State:      StateIdle,
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupDogWithState(t, m, "alpha", state)

	err := m.RefreshRig("alpha", "unknownrig")
	if err == nil {
		t.Error("RefreshRig() expected error for unknown rig")
	}
}

// =============================================================================
// State Transition Tests (Behavioral)
// =============================================================================

func TestManager_StateTransition_IdleToWorking(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	state := &DogState{
		Name:       "alpha",
		State:      StateIdle,
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupDogWithState(t, m, "alpha", state)

	// Assign work should transition to Working
	if err := m.AssignWork("alpha", "task-1"); err != nil {
		t.Fatalf("AssignWork() error = %v", err)
	}

	dog, _ := m.Get("alpha")
	if dog.State != StateWorking {
		t.Errorf("After AssignWork: State = %q, want Working", dog.State)
	}
}

func TestManager_StateTransition_WorkingToIdle(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	state := &DogState{
		Name:       "alpha",
		State:      StateWorking,
		Work:       "task-1",
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupDogWithState(t, m, "alpha", state)

	// Clear work should transition to Idle
	if err := m.ClearWork("alpha"); err != nil {
		t.Fatalf("ClearWork() error = %v", err)
	}

	dog, _ := m.Get("alpha")
	if dog.State != StateIdle {
		t.Errorf("After ClearWork: State = %q, want Idle", dog.State)
	}
	if dog.Work != "" {
		t.Errorf("After ClearWork: Work = %q, want empty", dog.Work)
	}
}

func TestManager_StateTransition_WorkReassignment(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	state := &DogState{
		Name:       "alpha",
		State:      StateWorking,
		Work:       "task-1",
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupDogWithState(t, m, "alpha", state)

	// Can reassign work while already working
	if err := m.AssignWork("alpha", "task-2"); err != nil {
		t.Fatalf("AssignWork() error = %v", err)
	}

	dog, _ := m.Get("alpha")
	if dog.Work != "task-2" {
		t.Errorf("After reassignment: Work = %q, want 'task-2'", dog.Work)
	}
}

// =============================================================================
// WorkStartedAt Tests
// =============================================================================

func TestManager_AssignWork_SetsWorkStartedAt(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	state := &DogState{
		Name:       "alpha",
		State:      StateIdle,
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupDogWithState(t, m, "alpha", state)

	before := time.Now()
	if err := m.AssignWork("alpha", "task-1"); err != nil {
		t.Fatalf("AssignWork() error = %v", err)
	}

	dog, err := m.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if dog.WorkStartedAt.IsZero() {
		t.Error("WorkStartedAt should be set after AssignWork")
	}
	if dog.WorkStartedAt.Before(before) {
		t.Error("WorkStartedAt should be >= time before AssignWork call")
	}
}

func TestManager_ClearWork_ClearsWorkStartedAt(t *testing.T) {
	m, _ := testManager(t)

	now := time.Now()
	state := &DogState{
		Name:          "alpha",
		State:         StateWorking,
		Work:          "task-1",
		WorkStartedAt: now.Add(-1 * time.Hour),
		LastActive:    now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	setupDogWithState(t, m, "alpha", state)

	if err := m.ClearWork("alpha"); err != nil {
		t.Fatalf("ClearWork() error = %v", err)
	}

	dog, err := m.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if !dog.WorkStartedAt.IsZero() {
		t.Errorf("WorkStartedAt = %v, want zero after ClearWork", dog.WorkStartedAt)
	}
}

// =============================================================================
// Error Constant Tests
// =============================================================================

func TestErrors_AreDistinct(t *testing.T) {
	errors := []error{ErrDogExists, ErrDogNotFound, ErrDogWorking, ErrNoRigs}
	errorStrings := make(map[string]bool)

	for _, err := range errors {
		s := err.Error()
		if errorStrings[s] {
			t.Errorf("Duplicate error string: %q", s)
		}
		errorStrings[s] = true
	}
}

func TestErrors_Messages(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{ErrDogExists, "dog already exists"},
		{ErrDogNotFound, "dog not found"},
		{ErrDogWorking, "dog is currently working"},
		{ErrNoRigs, "no rigs configured"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}
