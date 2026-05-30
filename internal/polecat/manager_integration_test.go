//go:build integration

package polecat

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/testutil"
	"github.com/steveyegge/gastown/internal/tmux"
)

var polecatManagerIntegrationCounter atomic.Int32

func initBeadsDBWithPrefix(t *testing.T, dir, prefix string) {
	t.Helper()
	testutil.RequireDoltContainer(t)

	args := []string{"init", "--quiet", "--prefix", prefix, "--server-port", testutil.DoltContainerPort()}
	cmd := exec.Command("bd", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init failed in %s: %v\n%s", dir, err, output)
	}

	issuesPath := filepath.Join(dir, ".beads", "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(""), 0644); err != nil {
		t.Fatalf("create issues.jsonl in %s: %v", dir, err)
	}
}

func requireTmuxIntegration(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed, skipping integration test")
	}
}

func startLiveSession(t *testing.T, sessionName string) {
	t.Helper()

	tm := tmux.NewTmux()
	if err := tm.NewSessionWithCommand(sessionName, "", "sleep 60"); err != nil {
		t.Fatalf("start tmux session %s: %v", sessionName, err)
	}
	t.Cleanup(func() {
		_ = tm.KillSessionWithProcesses(sessionName)
	})
}

// TestManagerGetPrefersHookedBeadOverStaleAgentHook verifies that manager.Get
// reports the current hooked work bead when agent hook_bead is stale.
func TestManagerGetPrefersHookedBeadOverStaleAgentHook(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping integration test")
	}
	testutil.RequireDoltContainer(t)

	n := polecatManagerIntegrationCounter.Add(1)
	prefix := fmt.Sprintf("pm%d", n)

	townRoot := t.TempDir()
	rigName := "testrig"
	rigPath := filepath.Join(townRoot, rigName)
	mayorRigPath := filepath.Join(rigPath, "mayor", "rig")

	if err := os.MkdirAll(mayorRigPath, 0755); err != nil {
		t.Fatalf("mkdir mayor rig path: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(rigPath, "polecats", "toast"), 0755); err != nil {
		t.Fatalf("mkdir polecat dir: %v", err)
	}

	// Rig .beads redirects to mayor/rig/.beads so NewManager resolves correctly.
	rigBeadsDir := filepath.Join(rigPath, ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "redirect"), []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}

	// Town routing with unique prefix for this test DB.
	townBeadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir town .beads: %v", err)
	}
	routes := []beads.Route{
		{Prefix: "hq-", Path: "."},
		{Prefix: prefix + "-", Path: filepath.Join(rigName, "mayor", "rig")},
	}
	if err := beads.WriteRoutes(townBeadsDir, routes); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	initBeadsDBWithPrefix(t, mayorRigPath, prefix)

	r := &rig.Rig{Name: rigName, Path: rigPath}
	mgr := NewManager(r, git.NewGit(rigPath), nil)

	stale, err := mgr.beads.Create(beads.CreateOptions{
		Title:    "stale old issue",
		Type:     "task",
		Priority: 2,
	})
	if err != nil {
		t.Fatalf("create stale issue: %v", err)
	}
	current, err := mgr.beads.Create(beads.CreateOptions{
		Title:    "current hooked issue",
		Type:     "task",
		Priority: 2,
	})
	if err != nil {
		t.Fatalf("create current issue: %v", err)
	}

	assignee := mgr.assigneeID("toast")
	hooked := beads.StatusHooked
	if err := mgr.beads.Update(current.ID, beads.UpdateOptions{
		Status:   &hooked,
		Assignee: &assignee,
	}); err != nil {
		t.Fatalf("hook current issue: %v", err)
	}

	agentID := mgr.agentBeadID("toast")
	if _, err := mgr.beads.CreateOrReopenAgentBead(agentID, assignee, &beads.AgentFields{
		HookBead:   stale.ID,
		AgentState: string(beads.AgentStateWorking),
	}); err != nil {
		t.Fatalf("create agent bead with stale hook: %v", err)
	}

	p, err := mgr.Get("toast")
	if err != nil {
		t.Fatalf("mgr.Get(toast): %v", err)
	}

	if p.State != StateWorking {
		t.Fatalf("polecat state = %q, want %q", p.State, StateWorking)
	}
	if p.Issue != current.ID {
		t.Fatalf("polecat issue = %q, want hooked issue %q (stale hook %q)", p.Issue, current.ID, stale.ID)
	}
}

func TestManagerTreatsLiveSessionWithoutWorkAsReviewNeeded(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping integration test")
	}
	requireTmuxIntegration(t)
	testutil.RequireDoltContainer(t)

	n := polecatManagerIntegrationCounter.Add(1)
	prefix := fmt.Sprintf("pm%d", n)

	townRoot := t.TempDir()
	rigName := "testrig"
	rigPath := filepath.Join(townRoot, rigName)
	mayorRigPath := filepath.Join(rigPath, "mayor", "rig")

	if err := os.MkdirAll(mayorRigPath, 0755); err != nil {
		t.Fatalf("mkdir mayor rig path: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(rigPath, "polecats", "toast"), 0755); err != nil {
		t.Fatalf("mkdir polecat dir: %v", err)
	}

	rigBeadsDir := filepath.Join(rigPath, ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "redirect"), []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}

	townBeadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir town .beads: %v", err)
	}
	routes := []beads.Route{
		{Prefix: "hq-", Path: "."},
		{Prefix: prefix + "-", Path: filepath.Join(rigName, "mayor", "rig")},
	}
	if err := beads.WriteRoutes(townBeadsDir, routes); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	initBeadsDBWithPrefix(t, mayorRigPath, prefix)

	r := &rig.Rig{Name: rigName, Path: rigPath}
	tm := tmux.NewTmux()
	mgr := NewManager(r, git.NewGit(rigPath), tm)

	agentID := mgr.agentBeadID("toast")
	assignee := mgr.assigneeID("toast")
	if _, err := mgr.beads.CreateOrReopenAgentBead(agentID, assignee, &beads.AgentFields{
		AgentState: string(beads.AgentStateIdle),
	}); err != nil {
		t.Fatalf("create idle agent bead: %v", err)
	}

	sessionName := NewSessionManager(tm, r).SessionName("toast")
	startLiveSession(t, sessionName)

	p, err := mgr.Get("toast")
	if err != nil {
		t.Fatalf("mgr.Get(toast): %v", err)
	}
	if p.State != StateReviewNeeded {
		t.Fatalf("polecat state = %q, want %q when tmux session is alive without work", p.State, StateReviewNeeded)
	}
	if p.Issue != "" {
		t.Fatalf("polecat issue = %q, want empty when no active hooked/assigned work exists", p.Issue)
	}

	idle, err := mgr.FindIdlePolecat()
	if err != nil {
		t.Fatalf("mgr.FindIdlePolecat(): %v", err)
	}
	if idle != nil {
		t.Fatalf("FindIdlePolecat() = %q, want nil while session %s needs review", idle.Name, sessionName)
	}
}
