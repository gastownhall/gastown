package cmd

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/doltserver"
	"github.com/jonbaldie/gastown/internal/process"
	"github.com/jonbaldie/gastown/internal/session"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/jonbaldie/gastown/internal/worker"
)

// TestImmediateShutdownStopsTownDoltAndWorkerServe is the red-capable loop
// for the UAT bug: gt shutdown stops tmux/daemon but leaves this town's Dolt
// SQL server and `gt worker serve --town <that-town>` running.
func TestImmediateShutdownStopsTownDoltAndWorkerServe(t *testing.T) {
	if testing.Short() {
		t.Skip("starts local stand-in processes")
	}

	townRoot := writeTownMarker(t)
	dataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dataDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("listener: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	port := doltserver.FindFreePort(3348)
	if port == 0 {
		t.Fatal("no free port from 3348")
	}
	t.Setenv("GT_DOLT_PORT", strconv.Itoa(port))

	doltCmd := exec.Command("python3", "-c", `
import socket, sys, time
port = int(sys.argv[1])
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", port))
s.listen(1)
while True:
    time.sleep(1)
`, strconv.Itoa(port), "--config", configPath)
	doltCmd.Dir = dataDir
	if err := doltCmd.Start(); err != nil {
		t.Fatalf("start dolt stand-in: %v", err)
	}
	t.Cleanup(func() { _ = doltCmd.Process.Kill(); _, _ = doltCmd.Process.Wait() })

	waitUntil(t, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	})

	if err := os.WriteFile(filepath.Join(townRoot, "daemon", "dolt.pid"), []byte(strconv.Itoa(doltCmd.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gtBin := buildTinyGTStandIn(t)
	workerCmd := exec.Command(gtBin, "worker", "serve", "--town", townRoot)
	if err := workerCmd.Start(); err != nil {
		t.Fatalf("start worker stand-in: %v", err)
	}
	t.Cleanup(func() { _ = workerCmd.Process.Kill(); _, _ = workerCmd.Process.Wait() })

	waitFor := time.Now().Add(2 * time.Second)
	for time.Now().Before(waitFor) {
		running, pid, err := doltserver.IsRunning(townRoot)
		if err == nil && running && pid == doltCmd.Process.Pid && containsPID(worker.FindServePIDs(townRoot), workerCmd.Process.Pid) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	running, pid, err := doltserver.IsRunning(townRoot)
	if err != nil || !running || pid != doltCmd.Process.Pid {
		t.Fatalf("setup: town Dolt stand-in not detected (running=%v pid=%d err=%v)", running, pid, err)
	}
	if !containsPID(worker.FindServePIDs(townRoot), workerCmd.Process.Pid) {
		t.Fatalf("setup: worker serve stand-in not detected (args=%q)", process.CommandLine(workerCmd.Process.Pid))
	}

	tm := tmux.NewTmuxWithSocket("gt-fix-shutdown-dolt")
	if err := runImmediateShutdown(tm, nil, townRoot); err != nil {
		t.Fatalf("runImmediateShutdown: %v", err)
	}

	running, pid, err = doltserver.IsRunning(townRoot)
	if err != nil {
		t.Fatalf("dolt status after shutdown: %v", err)
	}
	if running {
		t.Errorf("gt shutdown left town Dolt running (PID %d on port %d)", pid, port)
	}
	// Reap the child so a killed worker is not left as a zombie owned by this test.
	done := make(chan struct{})
	go func() { _, _ = workerCmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
	}
	if leftover := worker.FindServePIDs(townRoot); len(leftover) > 0 {
		t.Errorf("gt shutdown left gt worker serve --town %s running (PIDs %v)", townRoot, leftover)
	}
	if process.Alive(workerCmd.Process.Pid) {
		stat, _ := exec.Command("ps", "-p", strconv.Itoa(workerCmd.Process.Pid), "-o", "stat=").Output()
		if !strings.Contains(string(stat), "Z") {
			t.Errorf("gt shutdown left worker stand-in PID %d alive (stat=%q)", workerCmd.Process.Pid, strings.TrimSpace(string(stat)))
		}
	}
	if conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond); dialErr == nil {
		_ = conn.Close()
		t.Errorf("town Dolt port %d still listening after shutdown", port)
	}
}

var cachedTinyGT string

func buildTinyGTStandIn(t *testing.T) string {
	t.Helper()
	if cachedTinyGT != "" {
		if _, err := os.Stat(cachedTinyGT); err == nil {
			return cachedTinyGT
		}
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\nimport \"time\"\nfunc main() { for { time.Sleep(time.Second) } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "gt")
	cmd := exec.Command("go", "build", "-o", out, src)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build tiny gt: %v\n%s", err, data)
	}
	cachedTinyGT = out
	return out
}

func waitUntil(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for stand-in process")
}

func containsPID(pids []int, want int) bool {
	for _, pid := range pids {
		if pid == want {
			return true
		}
	}
	return false
}

// TestImmediateShutdownKillsLatePolecatSession is the red-capable loop for the
// UAT3 leftover: Witness spawned cl-nux after shutdown listed sessions once.
func TestImmediateShutdownKillsLatePolecatSession(t *testing.T) {
	if testing.Short() {
		t.Skip("starts tmux sessions")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	old := session.DefaultRegistry()
	reg := session.NewPrefixRegistry()
	reg.Register("cl", "clock")
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })

	socket := fmt.Sprintf("gt-fix-shutdown-late-%d", time.Now().UnixNano())
	tm := tmux.NewTmuxWithSocket(socket)
	t.Cleanup(func() {
		_ = tm.KillServer()
	})

	early := "cl-furiosa"
	late := "cl-nux"
	if err := tm.NewSession(early, ""); err != nil {
		t.Fatalf("NewSession %s: %v", early, err)
	}

	// Witness-style spawn: a new known session appears after the first list.
	go func() {
		time.Sleep(80 * time.Millisecond)
		_ = tm.NewSession(late, "")
	}()

	if err := runImmediateShutdown(tm, []string{early}, t.TempDir()); err != nil {
		t.Fatalf("runImmediateShutdown: %v", err)
	}

	exists, err := tm.HasSession(late)
	if err != nil {
		t.Fatalf("HasSession %s: %v", late, err)
	}
	if exists {
		t.Fatalf("gt shutdown left late polecat session %s running", late)
	}
}
