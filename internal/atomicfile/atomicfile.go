// Package atomicfile provides atomic file-write primitives (write-to-temp +
// rename). Kept as a leaf package with no internal dependencies so any other
// package — including low-level ones that util/ transitively depends on — can
// use it without creating an import cycle.
package atomicfile

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// WriteJSON writes JSON data to a file atomically with mode 0644.
// It first writes to a temporary file in the same directory, then renames it
// to the target path. This prevents data corruption if the process crashes
// during write. The rename operation is atomic on POSIX systems.
func WriteJSON(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(path, data, 0644)
}

// WriteJSONWithPerm is like WriteJSON but uses the given file mode.
func WriteJSONWithPerm(path string, v interface{}, perm os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(path, data, perm)
}

// EnsureDirAndWriteJSON creates parent directories (mode 0755) if needed, then
// atomically writes JSON with mode 0644.
func EnsureDirAndWriteJSON(path string, v interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return WriteJSON(path, v)
}

// EnsureDirAndWriteJSONWithPerm is like EnsureDirAndWriteJSON but uses the
// given file mode for the output file.
func EnsureDirAndWriteJSONWithPerm(path string, v interface{}, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return WriteJSONWithPerm(path, v, perm)
}

// WriteFile writes data to a file atomically by writing to a unique temp file
// in the same directory and then renaming it over the target. The rename is
// atomic on POSIX systems; concurrent writers each produce self-consistent
// content because each uses a distinct temp file.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	// "*" in the pattern is replaced with a random suffix by os.CreateTemp,
	// preventing concurrent writers from colliding on the same temp file.
	f, err := os.CreateTemp(dir, base+".tmp.*")
	if err != nil {
		return err
	}
	tmpName := f.Name()

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpName)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}

	// CreateTemp uses 0600 by default; apply the caller's permissions.
	if err := os.Chmod(tmpName, perm); err != nil {
		os.Remove(tmpName)
		return err
	}

	if err := replaceFile(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}

	return nil
}
