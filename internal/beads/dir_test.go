package beads

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsureDir_CreatesBeadsDirWithMode0700(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not reliable on Windows")
	}

	dir := filepath.Join(t.TempDir(), ".beads")
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	assertDirMode(t, dir, DirPerm)
}

func TestEnsureDir_TightensExisting0755(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not reliable on Windows")
	}

	dir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	assertDirMode(t, dir, 0o755)

	if err := EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	assertDirMode(t, dir, DirPerm)
}

func TestWriteRoutes_CreatesBeadsDirWithMode0700(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not reliable on Windows")
	}

	dir := filepath.Join(t.TempDir(), ".beads")
	if err := WriteRoutes(dir, nil); err != nil {
		t.Fatalf("WriteRoutes: %v", err)
	}
	assertDirMode(t, dir, DirPerm)
}

func assertDirMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
