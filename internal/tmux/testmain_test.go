package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/steveyegge/gastown/internal/testutil/hermetic"
)

// TestMain sets up a dedicated tmux server for the package's integration tests.
// All tests that call newTestTmux() share this isolated server, which is torn
// down after all tests complete. This prevents test sessions from appearing on
// the user's interactive tmux and avoids socket conflicts with other packages.
func TestMain(m *testing.M) {
	// Cut the caller's session and town out of the environment; this also
	// gives tmux a private socket directory so the test server cannot collide
	// with the caller's tmux server.
	restore := hermetic.Isolate()
	if err := hermetic.Check(); err != nil {
		fmt.Fprintf(os.Stderr, "tmux TestMain: %v\n", err)
		restore()
		os.Exit(1)
	}

	socket := fmt.Sprintf("gt-test-%d", os.Getpid())

	// Set defaultSocket so NewTmux() connects to the test server, not the
	// user's personal server or the sentinel that indicates "no town context".
	SetDefaultSocket(socket)

	// Start a sentinel session to keep the server alive for the entire test run.
	// Without this, tests that kill their last session inadvertently take down
	// the server, leaving a stale socket that prevents subsequent new-session
	// calls from restarting it (tmux sees the socket file but no listener).
	// The sentinel uses a name no individual test touches, so it outlives all
	// per-test sessions. TestMain kills the whole server at the end.
	if _, err := exec.LookPath("tmux"); err == nil {
		_ = exec.Command("tmux", "-u", "-L", socket, "new-session", "-d", "-s", "gt-test-sentinel").Run()
	}

	code := m.Run()

	// Kill the test tmux server and restore the original socket state.
	_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	SetDefaultSocket("")
	restore()

	os.Exit(code)
}
