package beads

import (
	"fmt"
	"os"
)

// DirPerm is the mode Beads recommends for .beads directories.
// 0700 keeps issue data private and silences bd's world-readable warning.
const DirPerm os.FileMode = 0o700

// EnsureDir creates path if needed and sets it to DirPerm.
// os.MkdirAll does not tighten an existing 0755 directory, so this always chmods.
func EnsureDir(path string) error {
	if err := os.MkdirAll(path, DirPerm); err != nil {
		return fmt.Errorf("creating beads directory %s: %w", path, err)
	}
	if err := os.Chmod(path, DirPerm); err != nil {
		return fmt.Errorf("setting beads directory mode %s: %w", path, err)
	}
	return nil
}
