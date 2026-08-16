// Package workspace provides workspace detection and management.
package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jonbaldie/gastown/internal/townroot"
)

// ErrNotFound indicates no workspace was found.
var ErrNotFound = errors.New("not in a Gas Town workspace")

// Markers used to detect a Gas Town workspace.
const (
	// PrimaryMarker is the main config file that identifies a workspace.
	// The town.json file lives in mayor/ along with other mayor config.
	PrimaryMarker = "mayor/town.json"

	// SecondaryMarker is an alternative indicator at the town level.
	// Note: This can match rig-level mayors too, so we continue searching
	// upward after finding this to look for primary markers.
	SecondaryMarker = "mayor"
)

// Find locates the town root by walking up from the given directory.
// Primary detection uses townroot.Find (outermost mayor/town.json).
// If no town.json exists, Find falls back to the outermost mayor/ directory.
// Does not resolve symlinks to stay consistent with os.Getwd().
func Find(startDir string) (string, error) {
	absDir, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}

	if primary := townroot.Find(absDir); primary != "" {
		return primary, nil
	}

	var secondaryMatch string
	current := absDir
	for {
		if info, err := os.Stat(filepath.Join(current, SecondaryMarker)); err == nil && info.IsDir() {
			secondaryMatch = current
		}

		parent := filepath.Dir(current)
		if parent == current {
			return secondaryMatch, nil
		}
		current = parent
	}
}

// FindOrError is like Find but returns a user-friendly error if not found.
func FindOrError(startDir string) (string, error) {
	root, err := Find(startDir)
	if err != nil {
		return "", err
	}
	if root == "" {
		return "", ErrNotFound
	}
	return root, nil
}

// FindFromCwd locates the town root from the current working directory.
func FindFromCwd() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting current directory: %w", err)
	}
	return Find(cwd)
}

// FindFromCwdOrError is like FindFromCwd but returns an error if not found.
// It searches for a workspace starting from the CWD. If none is found, it
// falls back to the GT_TOWN_ROOT or GT_ROOT environment variables.
func FindFromCwdOrError() (string, error) {
	cwd, err := os.Getwd()
	if err == nil {
		root, err := Find(cwd)
		if err == nil && root != "" {
			return root, nil
		}
	}

	// Fallback: try GT_TOWN_ROOT or GT_ROOT env vars (set by shell integration or session manager)
	for _, envName := range []string{"GT_TOWN_ROOT", "GT_ROOT"} {
		if townRoot := os.Getenv(envName); townRoot != "" {
			// Verify it's actually a workspace
			if ok, _ := IsWorkspace(townRoot); ok {
				return townRoot, nil
			}
		}
	}

	if err != nil {
		return "", fmt.Errorf("getting current directory: %w", err)
	}
	return "", ErrNotFound
}

// FindFromCwdWithFallback is like FindFromCwdOrError but returns (townRoot, cwd, error).
// If getcwd fails, returns (townRoot, "", nil) using GT_TOWN_ROOT fallback.
// This is useful for commands like `gt done` that need to continue even if the
// working directory is deleted (e.g., polecat worktree nuked by Witness).
func FindFromCwdWithFallback() (townRoot string, cwd string, err error) {
	cwd, err = os.Getwd()
	if err != nil {
		// Fallback: try GT_TOWN_ROOT env var
		if townRoot = os.Getenv("GT_TOWN_ROOT"); townRoot != "" {
			// Verify it's actually a workspace
			if _, statErr := os.Stat(filepath.Join(townRoot, PrimaryMarker)); statErr == nil {
				return townRoot, "", nil // cwd is gone but townRoot is valid
			}
		}
		return "", "", fmt.Errorf("getting current directory: %w", err)
	}

	townRoot, err = FindOrError(cwd)
	if err != nil {
		return "", "", err
	}
	return townRoot, cwd, nil
}

// IsWorkspace checks if the given directory is a Gas Town workspace root.
// A directory is a workspace if it has a primary marker (mayor/town.json)
// or a secondary marker (mayor/ directory).
func IsWorkspace(dir string) (bool, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false, fmt.Errorf("resolving path: %w", err)
	}

	// Check for primary marker (mayor/town.json)
	primaryPath := filepath.Join(absDir, PrimaryMarker)
	if _, err := os.Stat(primaryPath); err == nil {
		return true, nil
	}

	// Check for secondary marker (mayor/ directory)
	secondaryPath := filepath.Join(absDir, SecondaryMarker)
	info, err := os.Stat(secondaryPath)
	if err == nil && info.IsDir() {
		return true, nil
	}

	return false, nil
}

// GetTownName loads the town name from the workspace's town.json config.
// This is used for generating unique tmux session names that avoid collisions
// when running multiple Gas Town instances.
func GetTownName(townRoot string) (string, error) {
	townConfigPath := filepath.Join(townRoot, PrimaryMarker)
	data, err := os.ReadFile(townConfigPath) //nolint:gosec // G304: path is the town marker
	if err != nil {
		return "", fmt.Errorf("loading town config: %w", err)
	}
	var cfg struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("parsing town config: %w", err)
	}
	return cfg.Name, nil
}

// GetTownNameFromCwd locates the town root from the current working directory
// and returns the town name from its configuration.
func GetTownNameFromCwd() (string, error) {
	townRoot, err := FindFromCwdOrError()
	if err != nil {
		return "", err
	}
	return GetTownName(townRoot)
}

// MustGetTownName returns the town name or panics if it cannot be loaded.
// Use sparingly - prefer GetTownName with proper error handling.
func MustGetTownName(townRoot string) string {
	name, err := GetTownName(townRoot)
	if err != nil {
		panic(fmt.Sprintf("failed to get town name: %v", err))
	}
	return name
}
