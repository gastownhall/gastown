package daemon

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/dog"
	"github.com/steveyegge/gastown/internal/tmux"
)

// testScriptPluginDaemon builds a Daemon whose log output is captured so
// tests can assert on the script-contract decisions.
func testScriptPluginDaemon(t *testing.T, townRoot string) (*Daemon, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(&syncWriter{buf: &buf}, "test: ", log.LstdFlags),
	}
	return d, &buf
}

// syncWriter serializes writes: the script goroutine and the test goroutine
// both touch the log buffer.
type syncWriter struct {
	mu  sync.Mutex
	buf *bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// writeScriptPlugin creates a town-level cooldown plugin with a run.sh.
// The gate has no duration so dispatchPlugins skips the bd-backed cooldown
// query (unavailable in unit tests).
func writeScriptPlugin(t *testing.T, townRoot, name, script string) {
	t.Helper()
	dir := filepath.Join(townRoot, "plugins", name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	pluginMD := "+++\nname = \"" + name + "\"\ndescription = \"script plugin\"\n\n[gate]\ntype = \"cooldown\"\n+++\n\n# Instructions\n"
	if err := os.WriteFile(filepath.Join(dir, "plugin.md"), []byte(pluginMD), 0644); err != nil {
		t.Fatalf("WriteFile plugin.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/usr/bin/env bash\n"+script+"\n"), 0755); err != nil {
		t.Fatalf("WriteFile run.sh: %v", err)
	}
}

func runDispatchPlugins(t *testing.T, d *Daemon, townRoot string) *dog.Manager {
	t.Helper()
	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	sm := dog.NewSessionManager(tmux.NewTmux(), townRoot, mgr)
	d.dispatchPlugins(mgr, sm, rigsConfig)
	d.scriptPluginWG.Wait()
	return mgr
}

func TestDispatchPlugins_ScriptExecutesInsteadOfDog(t *testing.T) {
	townRoot := t.TempDir()
	d, logs := testScriptPluginDaemon(t, townRoot)

	marker := filepath.Join(townRoot, "script-ran")
	writeScriptPlugin(t, townRoot, "test-script", "touch '"+marker+"'; exit 0")
	testSetupDogState(t, townRoot, "idle-dog", dog.StateIdle, time.Now().Add(-10*time.Minute))

	mgr := runDispatchPlugins(t, d, townRoot)

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("run.sh did not execute: %v", err)
	}
	dg, err := mgr.Get("idle-dog")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if dg.State != dog.StateIdle || dg.Work != "" {
		t.Errorf("dog = state %q work %q, want idle/empty (exit 0 must not dispatch the agent step)", dg.State, dg.Work)
	}
	if !strings.Contains(logs.String(), "no agent step needed") {
		t.Errorf("logs missing success decision:\n%s", logs.String())
	}
}

func TestDispatchPlugins_ScriptFailureFailsSafe(t *testing.T) {
	townRoot := t.TempDir()
	d, logs := testScriptPluginDaemon(t, townRoot)

	writeScriptPlugin(t, townRoot, "failing-script", "echo guard says no; exit 3")
	testSetupDogState(t, townRoot, "idle-dog", dog.StateIdle, time.Now().Add(-10*time.Minute))

	mgr := runDispatchPlugins(t, d, townRoot)

	dg, err := mgr.Get("idle-dog")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if dg.State != dog.StateIdle || dg.Work != "" {
		t.Errorf("dog = state %q work %q, want idle/empty (failure must not dispatch the agent step)", dg.State, dg.Work)
	}
	got := logs.String()
	if !strings.Contains(got, "FAILED (exit 3)") || !strings.Contains(got, "NOT dispatched") {
		t.Errorf("logs missing fail-safe decision:\n%s", got)
	}
	if !strings.Contains(got, "guard says no") {
		t.Errorf("logs missing script output tail:\n%s", got)
	}
}

func TestDispatchPlugins_ScriptNeedsAgentAttemptsDogDispatch(t *testing.T) {
	townRoot := t.TempDir()
	d, logs := testScriptPluginDaemon(t, townRoot)

	writeScriptPlugin(t, townRoot, "needs-agent", "echo judgment required; exit 10")
	testSetupDogState(t, townRoot, "idle-dog", dog.StateIdle, time.Now().Add(-10*time.Minute))

	runDispatchPlugins(t, d, townRoot)

	got := logs.String()
	if !strings.Contains(got, "requested agent step (exit 10)") {
		t.Errorf("logs missing needs-agent decision:\n%s", got)
	}
	// The temp town has no mail infrastructure, so the dispatch itself is
	// expected to fail and roll back — what matters is that exit 10 is the
	// one path that tries the agent step at all.
	if strings.Contains(got, "NOT dispatched") {
		t.Errorf("exit 10 must not take the fail-safe path:\n%s", got)
	}
}

func TestDispatchPlugins_ScriptInFlightNotRelaunched(t *testing.T) {
	townRoot := t.TempDir()
	d, logs := testScriptPluginDaemon(t, townRoot)

	counter := filepath.Join(townRoot, "run-count")
	// Slow enough that the second dispatch tick lands while in flight.
	writeScriptPlugin(t, townRoot, "slow-script",
		"echo x >> '"+counter+"'; sleep 1; exit 0")

	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	mgr := dog.NewManager(townRoot, rigsConfig)
	sm := dog.NewSessionManager(tmux.NewTmux(), townRoot, mgr)

	d.dispatchPlugins(mgr, sm, rigsConfig)
	d.dispatchPlugins(mgr, sm, rigsConfig)
	d.scriptPluginWG.Wait()

	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("script never ran: %v", err)
	}
	if runs := strings.Count(string(data), "x"); runs != 1 {
		t.Errorf("script ran %d times, want 1 (in-flight guard)", runs)
	}
	if !strings.Contains(logs.String(), "still in flight") {
		t.Errorf("logs missing in-flight skip:\n%s", logs.String())
	}
}
