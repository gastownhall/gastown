package fsutil

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeRandomFile(t *testing.T, path string, size int) []byte {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("generating random data: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return data
}

func TestCopyFileReproducesContentAndMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")

	want := writeRandomFile(t, src, 128*1024)
	if err := os.Chmod(src, 0o640); err != nil {
		t.Fatalf("chmod src: %v", err)
	}

	if err := CopyFile(src, dst); err != nil {
		t.Fatalf("CopyFile: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading dst: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("destination content differs from source")
	}

	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat dst: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Errorf("destination mode = %o, want 640", got)
	}
}

func TestCopyFileTruncatesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")

	want := writeRandomFile(t, src, 1024)
	if err := os.WriteFile(dst, bytes.Repeat([]byte("x"), 8192), 0o644); err != nil {
		t.Fatalf("seeding dst: %v", err)
	}

	if err := CopyFile(src, dst); err != nil {
		t.Fatalf("CopyFile: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading dst: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("destination not truncated: got %d bytes, want %d", len(got), len(want))
	}
}

func TestCopyFileEmptyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "empty")
	dst := filepath.Join(dir, "empty-copy")

	if err := os.WriteFile(src, nil, 0o644); err != nil {
		t.Fatalf("writing empty src: %v", err)
	}
	if err := CopyFile(src, dst); err != nil {
		t.Fatalf("CopyFile: %v", err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat dst: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("destination size = %d, want 0", info.Size())
	}
}

func TestCopyFileRejectsNonRegularSource(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "adir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := CopyFile(sub, filepath.Join(dir, "out")); err == nil {
		t.Error("CopyFile accepted a directory as source")
	}
}

func TestCopyFileMissingSource(t *testing.T) {
	dir := t.TempDir()
	if err := CopyFile(filepath.Join(dir, "nope"), filepath.Join(dir, "out")); err == nil {
		t.Error("CopyFile accepted a missing source")
	}
}

func TestCopyDirReproducesTree(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.MkdirAll(filepath.Join(src, "nested", "deeper"), 0o755); err != nil {
		t.Fatalf("creating tree: %v", err)
	}
	top := writeRandomFile(t, filepath.Join(src, "top.bin"), 4096)
	deep := writeRandomFile(t, filepath.Join(src, "nested", "deeper", "deep.bin"), 4096)

	if err := os.Mkdir(filepath.Join(src, "emptydir"), 0o755); err != nil {
		t.Fatalf("creating empty dir: %v", err)
	}

	if err := CopyDir(src, dst); err != nil {
		t.Fatalf("CopyDir: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dst, "top.bin"))
	if err != nil {
		t.Fatalf("reading copied top.bin: %v", err)
	}
	if !bytes.Equal(got, top) {
		t.Error("top.bin content differs")
	}

	got, err = os.ReadFile(filepath.Join(dst, "nested", "deeper", "deep.bin"))
	if err != nil {
		t.Fatalf("reading copied deep.bin: %v", err)
	}
	if !bytes.Equal(got, deep) {
		t.Error("deep.bin content differs")
	}

	if info, err := os.Stat(filepath.Join(dst, "emptydir")); err != nil || !info.IsDir() {
		t.Error("empty directory was not reproduced")
	}
}

func TestCopyDirPreservesSymlinksAsLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevation on Windows")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	writeRandomFile(t, filepath.Join(src, "target.bin"), 512)
	if err := os.Symlink("target.bin", filepath.Join(src, "link.bin")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}

	if err := CopyDir(src, dst); err != nil {
		t.Fatalf("CopyDir: %v", err)
	}

	info, err := os.Lstat(filepath.Join(dst, "link.bin"))
	if err != nil {
		t.Fatalf("lstat copied link: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink was dereferenced instead of recreated")
	}
	target, err := os.Readlink(filepath.Join(dst, "link.bin"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != "target.bin" {
		t.Errorf("link target = %q, want target.bin", target)
	}
}

func TestCopyDirRejectsNonDirectory(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "afile")
	writeRandomFile(t, src, 16)
	if err := CopyDir(src, filepath.Join(dir, "out")); err == nil {
		t.Error("CopyDir accepted a regular file as source")
	}
}

// TestCopyFileFallsBackWithoutCloneSupport exercises the streaming path by
// copying onto tmpfs, which implements no clone ioctl. On a machine where
// /dev/shm is unavailable the test is skipped rather than silently passing.
func TestCopyFileFallsBackWithoutCloneSupport(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("tmpfs fallback probe is Linux-specific")
	}
	if _, err := os.Stat("/dev/shm"); err != nil {
		t.Skip("/dev/shm unavailable")
	}

	shmDir, err := os.MkdirTemp("/dev/shm", "fsutil-*")
	if err != nil {
		t.Skipf("cannot use /dev/shm: %v", err)
	}
	defer func() { _ = os.RemoveAll(shmDir) }()

	src := filepath.Join(shmDir, "src.bin")
	dst := filepath.Join(shmDir, "dst.bin")
	want := writeRandomFile(t, src, 64*1024)

	if err := CopyFile(src, dst); err != nil {
		t.Fatalf("CopyFile on tmpfs: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading dst: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("tmpfs copy content differs")
	}
}

// TestCopyFileAcrossFilesystems covers the EXDEV path, where cloning is
// impossible and the streaming fallback must carry the copy.
func TestCopyFileAcrossFilesystems(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cross-filesystem probe is Linux-specific")
	}
	if _, err := os.Stat("/dev/shm"); err != nil {
		t.Skip("/dev/shm unavailable")
	}

	shmDir, err := os.MkdirTemp("/dev/shm", "fsutil-*")
	if err != nil {
		t.Skipf("cannot use /dev/shm: %v", err)
	}
	defer func() { _ = os.RemoveAll(shmDir) }()

	src := filepath.Join(shmDir, "src.bin")
	want := writeRandomFile(t, src, 32*1024)

	dst := filepath.Join(t.TempDir(), "dst.bin")
	if err := CopyFile(src, dst); err != nil {
		t.Fatalf("cross-filesystem CopyFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading dst: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("cross-filesystem copy content differs")
	}
}
