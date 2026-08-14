// Package tmuxtest holds process-level helpers for tests that wrap a real
// tmux binary with testdata/tmuxstub.sh.
package tmuxtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// RealTmuxOrSkip returns the host tmux path, or skips the test if tmux is missing.
func RealTmuxOrSkip(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	return path
}

// SocketName returns an isolated tmux socket name derived from the test name.
func SocketName(t *testing.T, prefix string) string {
	t.Helper()
	return prefix + strings.ReplaceAll(t.Name(), "/", "-")
}

// StartSession starts a detached tmux session on an isolated socket and kills
// that server when the test ends.
func StartSession(t *testing.T, realTmux, socket, sessionName, shellCmd string) {
	t.Helper()
	cmd := exec.Command(realTmux, "-u", "-L", socket, "new-session", "-d", "-s", sessionName, "sh", "-c", shellCmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command(realTmux, "-L", socket, "kill-server").Run()
	})
}

// InstallStub copies testdata/tmuxstub.sh to a temp executable named tmux.
func InstallStub(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src := filepath.Join(filepath.Dir(file), "..", "testdata", "tmuxstub.sh")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read tmux stub: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatalf("write tmux stub: %v", err)
	}
	return dst
}

// ShellQuote wraps path in single quotes for a sh -c command string.
func ShellQuote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}
