//go:build linux && cowproof

package fsutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCloneProducesSharedExtents proves that CopyFile actually shares extents
// on a COW filesystem, rather than merely producing a correct copy.
//
// Guarded behind the `cowproof` tag because it needs a writable directory on a
// COW filesystem and the btrfs userspace tools. Run with:
//
//	GT_COWPROOF_DIR=/path/on/btrfs go test -tags cowproof ./internal/fsutil/ -v
func TestCloneProducesSharedExtents(t *testing.T) {
	base := os.Getenv("GT_COWPROOF_DIR")
	if base == "" {
		t.Skip("set GT_COWPROOF_DIR to a directory on a COW filesystem")
	}
	if _, err := exec.LookPath("btrfs"); err != nil {
		t.Skip("btrfs userspace tools unavailable")
	}

	dir, err := os.MkdirTemp(base, "cowproof-*")
	if err != nil {
		t.Fatalf("creating work dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")

	// 64 MiB is large enough that a byte copy is clearly distinguishable from
	// a clone, both in elapsed time and in the extent accounting.
	writeRandomFile(t, src, 64*1024*1024)
	if err := exec.Command("sync").Run(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	start := time.Now()
	if err := CopyFile(src, dst); err != nil {
		t.Fatalf("CopyFile: %v", err)
	}
	elapsed := time.Since(start)
	if err := exec.Command("sync").Run(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	out, err := exec.Command("btrfs", "filesystem", "du", "-s", "--raw", dst).CombinedOutput()
	if err != nil {
		t.Fatalf("btrfs filesystem du: %v\n%s", err, out)
	}
	t.Logf("CopyFile of 64MiB took %s", elapsed)
	t.Logf("btrfs accounting for destination:\n%s", out)

	// Output is a header line followed by: Total Exclusive "Set shared" Filename
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		t.Fatalf("unexpected btrfs output:\n%s", out)
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 3 {
		t.Fatalf("unexpected btrfs row: %q", lines[len(lines)-1])
	}
	exclusive := fields[1]

	if exclusive != "0" {
		t.Errorf("destination has %s exclusive bytes; extents were copied, not shared", exclusive)
	}
}
