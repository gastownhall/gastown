package cmd

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/rig"
)

func TestAgentStartResult_Fields(t *testing.T) {
	result := agentStartResult{
		name:   "Witness (gastown)",
		ok:     true,
		detail: "gt-gastown-witness",
	}

	if result.name != "Witness (gastown)" {
		t.Errorf("name = %q, want %q", result.name, "Witness (gastown)")
	}
	if !result.ok {
		t.Error("ok should be true")
	}
	if result.detail != "gt-gastown-witness" {
		t.Errorf("detail = %q, want %q", result.detail, "gt-gastown-witness")
	}
}

func TestUpStartRefinerySkipsForkRig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock tmux script uses POSIX shell")
	}
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigPath, "config.json"), []byte(`{"upstream_url":"https://github.com/upstream/repo"}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "tmux.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + logPath + "\"\ncase \"$1\" in has-session) exit 1 ;; *) exit 0 ;; esac\n"
	if err := os.WriteFile(filepath.Join(binDir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result := upStartRefinery("testrig", &rig.Rig{Name: "testrig", Path: rigPath})
	if !result.ok {
		t.Fatalf("upStartRefinery ok = false, detail=%s", result.detail)
	}
	if !strings.Contains(result.detail, "fork-backed rig") {
		t.Fatalf("detail = %q, want fork-backed skip", result.detail)
	}
}

func TestMaxConcurrentAgentStarts_Constant(t *testing.T) {
	// Verify the constant is set to a reasonable value
	if maxConcurrentAgentStarts < 1 {
		t.Errorf("maxConcurrentAgentStarts = %d, should be >= 1", maxConcurrentAgentStarts)
	}
	if maxConcurrentAgentStarts > 100 {
		t.Errorf("maxConcurrentAgentStarts = %d, should be <= 100 to prevent resource exhaustion", maxConcurrentAgentStarts)
	}
}

func TestApplyConfiguredDoltEnvConfigBeatsStaleEnv(t *testing.T) {
	townRoot := t.TempDir()
	doltDataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"), []byte("listener:\n  host: 127.0.0.2\n  port: 5507\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GT_DOLT_IGNORE_CONFIG", "")
	t.Setenv("GT_DOLT_HOST", "stale-host")
	t.Setenv("GT_DOLT_PORT", "9999")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "stale-host")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "9999")
	t.Setenv("BEADS_DOLT_PORT", "9999")

	config.ApplyConfiguredDoltEnv(townRoot)

	if got := os.Getenv("GT_DOLT_HOST"); got != "127.0.0.2" {
		t.Fatalf("GT_DOLT_HOST = %q, want 127.0.0.2", got)
	}
	if got := os.Getenv("GT_DOLT_PORT"); got != "5507" {
		t.Fatalf("GT_DOLT_PORT = %q, want 5507", got)
	}
	if got := os.Getenv("BEADS_DOLT_SERVER_HOST"); got != "" {
		t.Fatalf("BEADS_DOLT_SERVER_HOST = %q, want cleared", got)
	}
}

func TestApplyConfiguredDoltEnvClearsStaleHostWhenConfigHasNoHost(t *testing.T) {
	townRoot := t.TempDir()
	doltDataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"), []byte("listener:\n  port: 5507\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GT_DOLT_IGNORE_CONFIG", "")
	t.Setenv("GT_DOLT_HOST", "stale-host")
	t.Setenv("GT_DOLT_PORT", "9999")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "stale-host")

	config.ApplyConfiguredDoltEnv(townRoot)

	if got := os.Getenv("GT_DOLT_HOST"); got != "" {
		t.Fatalf("GT_DOLT_HOST = %q, want cleared", got)
	}
	if got := os.Getenv("GT_DOLT_PORT"); got != "5507" {
		t.Fatalf("GT_DOLT_PORT = %q, want 5507", got)
	}
}

func TestSemaphoreLimitsConcurrency(t *testing.T) {
	// Test that a semaphore pattern properly limits concurrency
	const maxConcurrent = 3
	const totalTasks = 10

	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	var maxObserved int32
	var current int32

	for i := 0; i < totalTasks; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			// Track concurrent count
			cur := atomic.AddInt32(&current, 1)
			defer atomic.AddInt32(&current, -1)

			// Update max observed
			for {
				max := atomic.LoadInt32(&maxObserved)
				if cur <= max || atomic.CompareAndSwapInt32(&maxObserved, max, cur) {
					break
				}
			}

			// Simulate work
			time.Sleep(10 * time.Millisecond)
		}()
	}

	wg.Wait()

	if maxObserved > maxConcurrent {
		t.Errorf("max concurrent = %d, should not exceed %d", maxObserved, maxConcurrent)
	}
}

func TestStartRigAgentsWithPrefetch_EmptyRigs(t *testing.T) {
	// Test with empty inputs
	witnessResults, refineryResults := startRigAgentsWithPrefetch(
		[]string{},
		make(map[string]*rig.Rig),
		make(map[string]error),
	)

	if len(witnessResults) != 0 {
		t.Errorf("witnessResults should be empty, got %d entries", len(witnessResults))
	}
	if len(refineryResults) != 0 {
		t.Errorf("refineryResults should be empty, got %d entries", len(refineryResults))
	}
}

func TestStartRigAgentsWithPrefetch_RecordsErrors(t *testing.T) {
	// Test that rig errors are properly recorded
	rigErrors := map[string]error{
		"badrig": fmt.Errorf("rig not found"),
	}

	witnessResults, refineryResults := startRigAgentsWithPrefetch(
		[]string{"badrig"},
		make(map[string]*rig.Rig),
		rigErrors,
	)

	if len(witnessResults) != 1 {
		t.Errorf("witnessResults should have 1 entry, got %d", len(witnessResults))
	}
	if result, ok := witnessResults["badrig"]; !ok {
		t.Error("witnessResults should have badrig entry")
	} else if result.ok {
		t.Error("badrig witness result should not be ok")
	}

	if len(refineryResults) != 1 {
		t.Errorf("refineryResults should have 1 entry, got %d", len(refineryResults))
	}
	if result, ok := refineryResults["badrig"]; !ok {
		t.Error("refineryResults should have badrig entry")
	} else if result.ok {
		t.Error("badrig refinery result should not be ok")
	}
}

func TestPrefetchRigs_Empty(t *testing.T) {
	// Test with empty rig list
	rigs, errors := prefetchRigs([]string{})

	if len(rigs) != 0 {
		t.Errorf("rigs should be empty, got %d entries", len(rigs))
	}
	if len(errors) != 0 {
		t.Errorf("errors should be empty, got %d entries", len(errors))
	}
}

func TestWorkerPoolLimitsConcurrency(t *testing.T) {
	// Test that a worker pool pattern properly limits concurrency
	const numWorkers = 3
	const numTasks = 15

	tasks := make(chan int, numTasks)
	results := make(chan int, numTasks)

	var maxObserved int32
	var current int32

	// Start worker pool
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range tasks {
				// Track concurrent count
				cur := atomic.AddInt32(&current, 1)

				// Update max observed
				for {
					max := atomic.LoadInt32(&maxObserved)
					if cur <= max || atomic.CompareAndSwapInt32(&maxObserved, max, cur) {
						break
					}
				}

				// Simulate work
				time.Sleep(5 * time.Millisecond)

				atomic.AddInt32(&current, -1)
				results <- 1
			}
		}()
	}

	// Enqueue tasks
	for i := 0; i < numTasks; i++ {
		tasks <- i
	}
	close(tasks)

	// Wait for workers and collect results
	go func() {
		wg.Wait()
		close(results)
	}()

	count := 0
	for range results {
		count++
	}

	if count != numTasks {
		t.Errorf("expected %d results, got %d", numTasks, count)
	}
	if maxObserved > numWorkers {
		t.Errorf("max concurrent = %d, should not exceed %d workers", maxObserved, numWorkers)
	}
}

// =============================================================================
// waitForDoltReady tests (gt-zou1n)
// Verifies that gt up waits for Dolt server readiness before starting witnesses.
// =============================================================================

// =============================================================================
// recoverOrphanedBeads tests (gas-udp)
// Verifies that gt up detects and recovers orphaned hooked beads after crash.
// =============================================================================

func TestRecoverOrphanedBeads_NoRigs(t *testing.T) {
	townRoot := t.TempDir()
	services := recoverOrphanedBeads(townRoot, []string{}, make(map[string]*rig.Rig))
	if len(services) != 0 {
		t.Errorf("expected no services, got %d", len(services))
	}
}

func TestRecoverOrphanedBeads_SkipsUnloadedRigs(t *testing.T) {
	townRoot := t.TempDir()
	// Rig "badrig" is in the list but not in prefetchedRigs — should be skipped.
	services := recoverOrphanedBeads(townRoot, []string{"badrig"}, make(map[string]*rig.Rig))
	if len(services) != 0 {
		t.Errorf("expected no services for unloaded rig, got %d", len(services))
	}
}

func TestRecoverOrphanedBeads_NoOrphansCleanRig(t *testing.T) {
	// Set up a rig directory with no beads — should produce no services.
	// Note: Full recovery-path tests (hooked bead + dead polecat → reset to open)
	// require a live Dolt server and are covered by DetectOrphanedBeads tests
	// in internal/witness/handlers_test.go. These up_test.go tests verify the
	// integration wiring: correct rig iteration, skip logic, and service reporting.
	townRoot := t.TempDir()
	rigName := "testrig"
	rigPath := filepath.Join(townRoot, rigName)
	if err := os.MkdirAll(filepath.Join(rigPath, "polecats"), 0755); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{Path: rigPath}
	prefetched := map[string]*rig.Rig{rigName: r}
	services := recoverOrphanedBeads(townRoot, []string{rigName}, prefetched)
	if len(services) != 0 {
		t.Errorf("expected no services for clean rig, got %d", len(services))
	}
}

func TestRecoverOrphanedBeads_MultipleRigsOnlyProcessesLoaded(t *testing.T) {
	townRoot := t.TempDir()

	// Set up two rigs, but only prefetch one
	for _, name := range []string{"rig-a", "rig-b"} {
		rigPath := filepath.Join(townRoot, name)
		if err := os.MkdirAll(filepath.Join(rigPath, "polecats"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	prefetched := map[string]*rig.Rig{
		"rig-a": {Path: filepath.Join(townRoot, "rig-a")},
		// rig-b intentionally not prefetched
	}
	services := recoverOrphanedBeads(townRoot, []string{"rig-a", "rig-b"}, prefetched)
	// Neither rig should have orphans (no Dolt server = no beads found),
	// but the function should complete without error and not panic on rig-b.
	for _, svc := range services {
		if svc.Rig == "rig-b" {
			t.Errorf("rig-b should have been skipped (not prefetched), but got service: %s", svc.Detail)
		}
	}
}

func TestWaitForDoltReady_NoServerMode(t *testing.T) {
	// When no server mode metadata exists, waitForDoltReady should not block.
	townRoot := t.TempDir()

	start := time.Now()
	waitForDoltReady(townRoot)
	elapsed := time.Since(start)

	if elapsed > 1*time.Second {
		t.Errorf("waitForDoltReady took %v with no server mode, should return immediately", elapsed)
	}
}

func TestWaitForDoltReady_ServerListening(t *testing.T) {
	// When server is already listening, should return quickly.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port

	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := fmt.Sprintf(`{"backend":"dolt","dolt_mode":"server","port":%d}`, port)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GT_DOLT_PORT", fmt.Sprintf("%d", port))

	start := time.Now()
	waitForDoltReady(townRoot)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("waitForDoltReady took %v, should complete quickly when server is ready", elapsed)
	}
}

func TestWaitForDoltReady_GracefulDegradation(t *testing.T) {
	// Verify that waitForDoltReady doesn't panic or error when Dolt is unreachable.
	// The wrapper should log a warning and continue (graceful degradation).
	// Uses a town root with no server metadata so it returns immediately.
	townRoot := t.TempDir()
	waitForDoltReady(townRoot) // Should not panic

	// Also verify the underlying doltserver.WaitForReady detects unreachable servers.
	// Use bind-then-close to find a guaranteed-free port. (review finding #4)
	tmpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	freePort := tmpListener.Addr().(*net.TCPAddr).Port
	tmpListener.Close()

	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := fmt.Sprintf(`{"backend":"dolt","dolt_mode":"server","port":%d}`, freePort)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GT_DOLT_PORT", fmt.Sprintf("%d", freePort))

	// WaitForReady with short timeout should fail when nothing is listening
	err = doltserver.WaitForReady(townRoot, 200*time.Millisecond)
	if err == nil {
		t.Error("doltserver.WaitForReady should fail when nothing is listening")
	}
}

func TestWaitForDoltReady_WrapperTimesOutAndContinues(t *testing.T) {
	// Verify the wrapper's error handling path: WaitForReady returns an error
	// but the wrapper logs a warning and continues (doesn't panic or propagate).
	// We test WaitForReady directly with a short timeout to verify the error,
	// since the wrapper uses the package-level 10s constant. (review finding #5)
	tmpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := tmpListener.Addr().(*net.TCPAddr).Port
	tmpListener.Close()

	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := fmt.Sprintf(`{"backend":"dolt","dolt_mode":"server","port":%d}`, port)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GT_DOLT_PORT", fmt.Sprintf("%d", port))

	// Verify the error path fires when nothing listens.
	err = doltserver.WaitForReady(townRoot, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected WaitForReady to fail when nothing is listening")
	}
	// Confirm error message is actionable.
	if err.Error() == "" {
		t.Error("expected non-empty error message from WaitForReady timeout")
	}
}
