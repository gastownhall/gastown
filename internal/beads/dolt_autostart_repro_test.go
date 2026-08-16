package beads

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestOpenStoreDoesNotAutoStartDolt is the regression for leftover
// `dolt sql-server` processes after in-process store opens.
//
// beadsdk.OpenFromConfig defaults AutoStart=true. Without an explicit
// opt-out, a missing town Dolt server makes the SDK spawn
// `dolt sql-server -H 127.0.0.1 -P <ephemeral>` in .beads/dolt, detach
// it to PPID 1, and leave it running after store.Close().
func TestOpenStoreDoesNotAutoStartDolt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("lsof-based leak check is unix-only")
	}

	// Clear both opt-outs so production OpenStore must disable auto-start.
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	t.Setenv("BEADS_TEST_MODE", "")

	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		for _, pid := range doltServersUnder(t, beadsDir) {
			proc, err := os.FindProcess(pid)
			if err == nil {
				_ = proc.Kill()
			}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, cleanup, err := NewWithBeadsDir(townRoot, beadsDir).OpenStore(ctx)
	if cleanup != nil {
		cleanup()
	}
	_ = store
	_ = err

	if leaks := doltServersUnder(t, beadsDir); len(leaks) > 0 {
		t.Fatalf("OpenStore left dolt sql-server process(es) %v under %s", leaks, beadsDir)
	}
}

func doltServersUnder(t *testing.T, beadsDir string) []int {
	t.Helper()
	out, err := exec.Command("pgrep", "-f", "dolt sql-server").Output()
	if err != nil {
		return nil
	}
	var leaked []int
	for _, pidStr := range strings.Fields(string(out)) {
		cwdOut, err := exec.Command("lsof", "-a", "-p", pidStr, "-d", "cwd", "-Fn").Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(cwdOut), "\n") {
			if strings.HasPrefix(line, "n") && strings.Contains(line[1:], beadsDir) {
				pid, err := strconv.Atoi(pidStr)
				if err == nil && pid > 0 {
					leaked = append(leaked, pid)
				}
			}
		}
	}
	return leaked
}
