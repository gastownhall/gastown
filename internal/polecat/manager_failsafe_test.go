package polecat

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

// writeFakeTmuxSessionFor creates a fake tmux binary where exactly one session
// exists. paneCommand controls what display-message/list-panes report for that
// session ("claude" simulates a live agent, "bash" an idle shell). Every
// kill-session invocation is appended to killLog.
func writeFakeTmuxSessionFor(t *testing.T, dir, liveSession, paneCommand, killLog string) {
	t.Helper()
	script := fmt.Sprintf("#!/bin/sh\n"+
		"case \"$*\" in\n"+
		"  *kill-session*) echo \"$*\" >> %s; exit 0;;\n"+
		"  *has-session*%s*) exit 0;;\n"+
		"  *has-session*) exit 1;;\n"+
		"  *%s*) echo '%s';;\n"+
		"  *) exit 1;;\n"+
		"esac\n", killLog, liveSession, liveSession, paneCommand)
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0755); err != nil {
		t.Fatalf("writing fake tmux: %v", err)
	}
}

func newFailsafeTestManager(t *testing.T, paneCommand string) (m *Manager, firstName, sessionName, killLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for tmux")
	}

	rigDir := t.TempDir()
	r := &rig.Rig{Name: "myrig", Path: rigDir}

	// Learn the first pool name deterministically (Allocate returns the first
	// available themed name) without touching tmux.
	probe := NewManager(r, nil, nil)
	names := probe.namePool.getNames()
	if len(names) < 2 {
		t.Fatalf("expected at least 2 pool names, got %d", len(names))
	}
	firstName = names[0]
	sessionName = session.PolecatSessionName(session.PrefixFor(r.Name), firstName)

	binDir := t.TempDir()
	killLog = filepath.Join(binDir, "kills.log")
	writeFakeTmuxSessionFor(t, binDir, sessionName, paneCommand, killLog)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	m = NewManager(r, nil, tmux.NewTmuxWithSocket(""))
	return m, firstName, sessionName, killLog
}

// TestAllocateNameSkipsLiveAgentSession verifies the fail-safe allocator: a
// name whose tmux session still hosts a live agent process must be skipped
// (marked in-use), never killed, and a different name allocated (2026-07-08
// incidents: allocation reclaimed live sessions).
func TestAllocateNameSkipsLiveAgentSession(t *testing.T) {
	m, firstName, _, killLog := newFailsafeTestManager(t, "claude")

	got, err := m.allocateNameSkippingLiveSessions()
	if err != nil {
		t.Fatalf("allocateNameSkippingLiveSessions: %v", err)
	}
	if got == firstName {
		t.Errorf("allocated %q, the name of a session with a live agent — must pick a different name", firstName)
	}

	// The live session must not have been killed.
	if _, err := os.Stat(killLog); err == nil {
		data, _ := os.ReadFile(killLog)
		t.Errorf("live agent session was killed during allocation: %s", data)
	}

	// The skipped name must be marked in-use so subsequent allocations skip it too.
	inUse := false
	for _, n := range m.namePool.ActiveNames() {
		if n == firstName {
			inUse = true
		}
	}
	if !inUse {
		t.Errorf("expected %q to be marked in-use after skip", firstName)
	}
}

// TestReconcilePoolWithProtectsLiveAgentOrphan verifies that a directory-less
// session with a live agent process is protected: not killed, and its name is
// kept in-use so the allocator cannot hand it out.
func TestReconcilePoolWithProtectsLiveAgentOrphan(t *testing.T) {
	m, firstName, _, killLog := newFailsafeTestManager(t, "claude")

	m.ReconcilePoolWith(nil, []string{firstName})

	if _, err := os.Stat(killLog); err == nil {
		data, _ := os.ReadFile(killLog)
		t.Errorf("live agent orphan session was killed by ReconcilePoolWith: %s", data)
	}

	inUse := strings.Join(m.namePool.ActiveNames(), ",")
	if !strings.Contains(inUse, firstName) {
		t.Errorf("expected %q to be kept in-use (protected), got in-use: %s", firstName, inUse)
	}
}

// TestReconcilePoolWithStillKillsDeadOrphan verifies the fail-safe does not
// over-protect: a directory-less session with NO live agent (idle shell) is
// still reaped and its name returned to the pool.
func TestReconcilePoolWithStillKillsDeadOrphan(t *testing.T) {
	m, firstName, _, killLog := newFailsafeTestManager(t, "bash")

	m.ReconcilePoolWith(nil, []string{firstName})

	data, err := os.ReadFile(killLog)
	if err != nil || !strings.Contains(string(data), "kill-session") {
		t.Errorf("expected dead orphan session to be killed, kill log: %q (err %v)", data, err)
	}

	for _, n := range m.namePool.ActiveNames() {
		if n == firstName {
			t.Errorf("expected %q to be available after dead-orphan reap, but it is in-use", firstName)
		}
	}
}
