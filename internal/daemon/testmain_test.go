package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/testutil"
	"github.com/steveyegge/gastown/internal/tmux"
)

func TestMain(m *testing.M) {
	// Start an ephemeral Dolt container for this package's tests.
	// convoy_manager_test.go calls setupTestStore which sets BEADS_TEST_MODE=1,
	// causing the beads SDK to create testdb_<hash> databases. By routing
	// those to an isolated container (via BEADS_DOLT_PORT), the databases are
	// destroyed when the container is terminated at cleanup —
	// preventing orphan accumulation in the shared production Dolt data dir.
	//
	// When Docker is unavailable, Dolt-needing tests self-skip via
	// setupTestStore → beadsdk.Open failure. Non-Dolt tests (e.g.
	// boot_spawn_frequency_test.go) still run. (fixes gt-kw4449)
	if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon TestMain: Dolt container unavailable (%v), Dolt-dependent tests will skip\n", err)
	}

	// Isolate tmux sessions on a package-specific socket.
	// handler_test.go creates tmux.NewTmux() instances that query has-session;
	// polecat_health_test.go uses fake tmux stubs but still constructs Tmux
	// instances. Routing all of these to an isolated socket prevents
	// interference with the user's tmux and other packages' tests.
	//
	// This isolation is FAIL-CLOSED and unconditional. It used to be gated on
	// exec.LookPath("tmux"), which was unsafe: tmux.NewTmux() falls back to
	// $GT_TOWN_SOCKET when no default socket is set, so any un-isolated run
	// operated on the operator's live town and KillSessionWithProcesses took
	// down real refinery and witness sessions. A test process that cannot
	// isolate its tmux socket must not run at all.
	tmuxSocket := fmt.Sprintf("gt-test-daemon-%d", os.Getpid())
	tmux.SetDefaultSocket(tmuxSocket)
	if got := tmux.GetDefaultSocket(); got != tmuxSocket {
		fmt.Fprintf(os.Stderr,
			"daemon TestMain: refusing to run un-isolated: tmux socket is %q, want %q\n", got, tmuxSocket)
		os.Exit(1)
	}
	// Neutralise the town-socket fallback for this process. Even if some code
	// path resolves an empty default socket, it must not reach the real town.
	if err := os.Unsetenv("GT_TOWN_SOCKET"); err != nil {
		fmt.Fprintf(os.Stderr, "daemon TestMain: cannot clear GT_TOWN_SOCKET: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	// Only ever tear down our own isolated socket, never the default server.
	if _, err := exec.LookPath("tmux"); err == nil {
		_ = exec.Command("tmux", "-L", tmuxSocket, "kill-server").Run()
	}
	socketPath := filepath.Join(tmux.SocketDir(), tmuxSocket)
	_ = os.Remove(socketPath)
	testutil.TerminateDoltContainer()
	os.Exit(code)
}
