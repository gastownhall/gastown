package refinery

import (
	"testing"

	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/tmux"
)

// TestManagerUsesInjectedTmuxClient pins the injection seam that keeps the
// refinery off the operator's real tmux server.
//
// Regression: Manager.start, Stop, Status, IsRunning, IsHealthy and
// BlockForkRigStart each called tmux.NewTmux() inline. That constructor falls
// back to $GT_TOWN_SOCKET when no default socket is set, so a test run could
// resolve the live town and KillSessionWithProcesses would take down real
// refinery and witness sessions — which is exactly what happened when the
// internal/daemon suite was run on a machine with a running town.
func TestManagerUsesInjectedTmuxClient(t *testing.T) {
	m := NewManager(&rig.Rig{Name: "testrig", Path: t.TempDir()})

	// Default: no client injected, so the manager resolves the process default.
	if m.tmuxClient() == nil {
		t.Fatal("tmuxClient must never return nil")
	}

	// Injected: the manager must hand back exactly what was pinned, otherwise
	// a caller that isolated its socket is silently ignored.
	isolated := tmux.NewTmuxWithSocket("gt-test-refinery-isolation")
	m.SetTmux(isolated)
	if got := m.tmuxClient(); got != isolated {
		t.Fatalf("tmuxClient returned %p, want the injected client %p", got, isolated)
	}
}

// TestManagerTmuxCallSitesAreRouted asserts that no method reintroduces an
// inline tmux.NewTmux(). It injects a client on an isolated socket and drives
// the read-only session methods: none may observe a session, because the
// isolated socket has no server. If a method bypasses the seam it would query
// the default/town server instead, where sessions can legitimately exist.
func TestManagerTmuxCallSitesAreRouted(t *testing.T) {
	m := NewManager(&rig.Rig{Name: "testrig", Path: t.TempDir()})
	m.SetTmux(tmux.NewTmuxWithSocket("gt-test-refinery-empty"))

	if running, _ := m.IsRunning(); running {
		t.Error("IsRunning reported a session on an isolated empty socket; a call site bypassed the tmux seam")
	}
	if status := m.IsHealthy(0); status == tmux.SessionHealthy {
		t.Error("IsHealthy reported healthy on an isolated empty socket; a call site bypassed the tmux seam")
	}
	if _, err := m.Status(); err == nil {
		t.Error("Status succeeded on an isolated empty socket; a call site bypassed the tmux seam")
	}
	if err := m.Stop(); err != ErrNotRunning {
		t.Errorf("Stop returned %v, want ErrNotRunning on an isolated empty socket", err)
	}
}
