package worker

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/process"
)

func TestIsTownWorkerServeArgs(t *testing.T) {
	t.Parallel()
	town := "/tmp/gt-uat2/town"
	other := "/tmp/other-town"
	cases := []struct {
		name string
		args string
		want bool
	}{
		{"real serve", "gt worker serve --town /tmp/gt-uat2/town", true},
		{"abs binary", "/usr/local/bin/gt worker serve --town /tmp/gt-uat2/town", true},
		{"equals form", "gt worker serve --town=/tmp/gt-uat2/town", true},
		{"status is not serve", "gt worker status --town /tmp/gt-uat2/town", false},
		{"other town", "gt worker serve --town /tmp/other-town", false},
		{"unrelated python", "python3 app.py --town /tmp/gt-uat2/town", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTownWorkerServeArgs(tc.args, town); got != tc.want {
				t.Errorf("isTownWorkerServeArgs(%q, %q) = %v, want %v", tc.args, town, got, tc.want)
			}
			if tc.want && isTownWorkerServeArgs(tc.args, other) {
				t.Errorf("matched other town for %q", tc.args)
			}
		})
	}
}

func TestStopServeStopsOnlyThisTown(t *testing.T) {
	if testing.Short() {
		t.Skip("starts local stand-in processes")
	}

	thisTown := t.TempDir()
	otherTown := t.TempDir()
	thisCmd := startWorkerStandIn(t, thisTown)
	otherCmd := startWorkerStandIn(t, otherTown)
	thisPID := thisCmd.Process.Pid
	otherPID := otherCmd.Process.Pid

	waitUntil(t, func() bool {
		return containsPID(FindServePIDs(thisTown), thisPID) &&
			containsPID(FindServePIDs(otherTown), otherPID)
	})

	if stopped := StopServe(thisTown); stopped == 0 {
		t.Fatal("StopServe stopped 0 processes for this town")
	}
	done := make(chan struct{})
	go func() { _, _ = thisCmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
	}
	if leftover := FindServePIDs(thisTown); len(leftover) > 0 {
		t.Fatalf("this town worker still detected after StopServe: %v", leftover)
	}
	if !process.Alive(otherPID) {
		t.Fatalf("other town worker PID %d was killed", otherPID)
	}
}

func startWorkerStandIn(t *testing.T, townRoot string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(buildTinyGTStandIn(t), "worker", "serve", "--town", townRoot)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start worker stand-in: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return cmd
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
	t.Fatal("timed out waiting for worker stand-in")
}

func containsPID(pids []int, want int) bool {
	for _, pid := range pids {
		if pid == want {
			return true
		}
	}
	return false
}
