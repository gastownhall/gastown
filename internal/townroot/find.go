// Package townroot locates a Gas Town root from a filesystem path.
//
// The town root is the outermost directory that contains mayor/town.json.
// Nested rigs that were once standalone towns still have that marker; callers
// must not stop at the first match.
package townroot

import (
	"os"
	"path/filepath"
)

// Marker is the file that identifies a Gas Town root.
const Marker = "mayor/town.json"

// Find walks up from startDir and returns the outermost directory that
// contains mayor/town.json. It returns "" when no marker exists.
func Find(startDir string) string {
	dir := startDir
	if abs, err := filepath.Abs(startDir); err == nil {
		dir = abs
	}

	candidate := ""
	for {
		if _, err := os.Stat(filepath.Join(dir, Marker)); err == nil {
			candidate = dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return candidate
		}
		dir = parent
	}
}
