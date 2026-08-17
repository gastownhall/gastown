package wisp

import (
	"fmt"
	"path/filepath"

	"github.com/jonbaldie/gastown/internal/atomicfile"
	"github.com/jonbaldie/gastown/internal/beads"
)

// EnsureDir ensures the .beads directory exists in the given root.
func EnsureDir(root string) (string, error) {
	dir := filepath.Join(root, WispDir)
	if err := beads.EnsureDir(dir); err != nil {
		return "", fmt.Errorf("create beads dir: %w", err)
	}
	return dir, nil
}

// WispPath returns the full path to a file in the beads directory.
func WispPath(root, filename string) string {
	return filepath.Join(root, WispDir, filename)
}

// writeJSON is a helper to write JSON files atomically.
func writeJSON(path string, v interface{}) error {
	if err := atomicfile.WriteJSON(path, v); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}
