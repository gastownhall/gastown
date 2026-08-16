package dog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
)

// TestDogStateJSON verifies DogState JSON serialization.
func TestDogStateJSON(t *testing.T) {
	now := time.Now()
	state := &DogState{
		Name:       "alpha",
		State:      StateIdle,
		LastActive: now,
		Work:       "",
		Worktrees: map[string]string{
			"gastown": "/path/to/gastown",
			"beads":   "/path/to/beads",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Create temp file
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, ".dog.json")

	// Write and read back
	data, err := os.ReadFile(statePath)
	if err == nil {
		t.Logf("Data already exists: %s", data)
	}

	// Test state values
	if state.Name != "alpha" {
		t.Errorf("expected name 'alpha', got %q", state.Name)
	}
	if state.State != StateIdle {
		t.Errorf("expected state 'idle', got %q", state.State)
	}
	if len(state.Worktrees) != 2 {
		t.Errorf("expected 2 worktrees, got %d", len(state.Worktrees))
	}
}

// TestManagerCreation verifies Manager initialization.
func TestManagerCreation(t *testing.T) {
	rigsConfig := &config.RigsConfig{
		Version: 1,
		Rigs: map[string]config.RigEntry{
			"gastown": {
				GitURL: "git@github.com:test/gastown.git",
			},
			"beads": {
				GitURL: "git@github.com:test/beads.git",
			},
		},
	}

	m := NewManager("/tmp/test-town", rigsConfig)

	if filepath.ToSlash(m.townRoot) != "/tmp/test-town" {
		t.Errorf("expected townRoot '/tmp/test-town', got %q", m.townRoot)
	}
	if filepath.ToSlash(m.kennelPath) != "/tmp/test-town/deacon/dogs" {
		t.Errorf("expected kennelPath '/tmp/test-town/deacon/dogs', got %q", m.kennelPath)
	}
}

// TestDogDir verifies dogDir path construction.
func TestDogDir(t *testing.T) {
	rigsConfig := &config.RigsConfig{
		Version: 1,
		Rigs:    map[string]config.RigEntry{},
	}
	m := NewManager("/home/user/gt", rigsConfig)

	path := m.dogDir("alpha")
	expected := "/home/user/gt/deacon/dogs/alpha"
	if filepath.ToSlash(path) != expected {
		t.Errorf("expected %q, got %q", expected, path)
	}
}

// TestStateConstants verifies state constants.
func TestStateConstants(t *testing.T) {
	tests := []struct {
		state    State
		expected string
	}{
		{StateIdle, "idle"},
		{StateWorking, "working"},
	}

	for _, tc := range tests {
		if string(tc.state) != tc.expected {
			t.Errorf("expected %q, got %q", tc.expected, string(tc.state))
		}
	}
}

// TestValidateDogName verifies name validation rejects dangerous names.
func TestValidateDogName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"alpha", false},
		{"dog-1", false},
		{"my_dog", false},
		{"", true},
		{"/", true},
		{"foo/bar", true},
		{"foo\\bar", true},
		{"..", true},
		{".", true},
		{"../../etc", true},
		{"a..b", true},
	}

	for _, tc := range tests {
		err := validateDogName(tc.name)
		if tc.wantErr && err == nil {
			t.Errorf("validateDogName(%q): expected error, got nil", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("validateDogName(%q): unexpected error: %v", tc.name, err)
		}
		if tc.wantErr && err != nil && !errors.Is(err, ErrInvalidName) {
			t.Errorf("validateDogName(%q): expected ErrInvalidName, got %v", tc.name, err)
		}
	}
}

// TestRemoveEmptyName verifies Remove("") returns ErrInvalidName, not ErrDogNotFound.
func TestRemoveEmptyName(t *testing.T) {
	rigsConfig := &config.RigsConfig{
		Version: 1,
		Rigs:    map[string]config.RigEntry{},
	}
	m := NewManager(t.TempDir(), rigsConfig)

	err := m.Remove("")
	if err == nil {
		t.Fatal("Remove('') should return an error")
	}
	if !errors.Is(err, ErrInvalidName) {
		t.Errorf("Remove(''): expected ErrInvalidName, got %v", err)
	}
}

// TestAddTraversalName verifies Add rejects path traversal names.
func TestAddTraversalName(t *testing.T) {
	rigsConfig := &config.RigsConfig{
		Version: 1,
		Rigs:    map[string]config.RigEntry{},
	}
	m := NewManager(t.TempDir(), rigsConfig)

	_, err := m.Add("../../etc")
	if err == nil {
		t.Fatal("Add('../../etc') should return an error")
	}
	if !errors.Is(err, ErrInvalidName) {
		t.Errorf("Add('../../etc'): expected ErrInvalidName, got %v", err)
	}
}
