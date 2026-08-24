//go:build runtimeprobe

package git

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRuntimeProbeCloneRealRepo exercises cloneInternal against a real
// multi-gigabyte repository and reports wall clock plus system-temp usage.
//
// Guarded behind the `runtimeprobe` build tag: it depends on a local repo
// path and takes tens of seconds, so it is not part of the normal suite. Run
// with:
//
//	go test -tags runtimeprobe ./internal/git/ -run TestRuntimeProbeCloneRealRepo -v \
//	    -probe-source /path/to/repo.git
func TestRuntimeProbeCloneRealRepo(t *testing.T) {
	source := os.Getenv("GT_PROBE_SOURCE")
	if source == "" {
		t.Skip("set GT_PROBE_SOURCE to a local repository path")
	}

	systmp := t.TempDir()
	t.Setenv("TMPDIR", systmp)

	town := newTownLikeRoot(t)
	headBefore := gitOut(t, town, "rev-parse", "HEAD")

	stop := make(chan struct{})
	staged := make(chan int, 1)
	go func() {
		max := 0
		for {
			select {
			case <-stop:
				staged <- max
				return
			default:
			}
			if entries, err := os.ReadDir(systmp); err == nil && len(entries) > max {
				max = len(entries)
			}
		}
	}()

	dest := filepath.Join(town, "rigs", "probe.git")
	g := NewGit(town)

	start := time.Now()
	err := g.CloneBare(source, dest)
	elapsed := time.Since(start)

	close(stop)
	peakStaged := <-staged

	if err != nil {
		t.Fatalf("CloneBare: %v", err)
	}

	t.Logf("clone wall clock: %s", elapsed)
	t.Logf("peak entries in system temp: %d", peakStaged)

	if peakStaged != 0 {
		t.Errorf("clone staged %d entries in the system temp dir; staging must happen next to the destination", peakStaged)
	}
	if got := gitOut(t, town, "rev-parse", "HEAD"); got != headBefore {
		t.Errorf("town HEAD moved: before %s, after %s", headBefore, got)
	}
	if got := townConfigValue(t, town, "core.hooksPath"); got != "" {
		t.Errorf("clone wrote core.hooksPath=%q into the town repository", got)
	}
	if got := townConfigValue(t, town, "core.sparseCheckout"); got != "" {
		t.Errorf("clone wrote core.sparseCheckout=%q into the town repository", got)
	}
	if refspec := gitOut(t, dest, "config", "--get", "remote.origin.fetch"); refspec == "" {
		t.Error("clone did not receive its origin refspec")
	}
}
