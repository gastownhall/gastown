// Package agent provides shared types and utilities for Gas Town agents
// (witness, refinery, deacon, etc.).
package agent

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/jonbaldie/gastown/internal/atomicfile"
)

// StateManager handles loading and saving agent state to disk.
// It uses generics to work with any state type.
type StateManager[T any] struct {
	stateFilePath  string
	defaultFactory func() *T
}

// NewStateManager creates a new StateManager for the given state file path.
// The defaultFactory function is called when the state file doesn't exist
// to create a new state with default values.
func NewStateManager[T any](rigPath, stateFileName string, defaultFactory func() *T) *StateManager[T] {
	return &StateManager[T]{
		stateFilePath:  filepath.Join(rigPath, ".runtime", stateFileName),
		defaultFactory: defaultFactory,
	}
}

// StateFile returns the path to the state file.
func (m *StateManager[T]) StateFile() string {
	return m.stateFilePath
}

// Load loads agent state from disk.
// If the file doesn't exist, returns a new state created by the default factory.
func (m *StateManager[T]) Load() (*T, error) {
	data, err := os.ReadFile(m.stateFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return m.defaultFactory(), nil
		}
		return nil, err
	}

	var state T
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}

	return &state, nil
}

// Save persists agent state to disk using atomic write.
func (m *StateManager[T]) Save(state *T) error {
	dir := filepath.Dir(m.stateFilePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return atomicfile.WriteJSON(m.stateFilePath, state)
}
