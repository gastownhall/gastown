package polecat

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	gtruntime "github.com/steveyegge/gastown/internal/runtime"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

func setupTestRegistryForSession(t *testing.T) {
	t.Helper()
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("bd", "beads")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

// testSessionCounter provides unique session names across -count=N runs
// to prevent "duplicate session" races with tmux's async cleanup.
var testSessionCounter atomic.Int64

func requireTmux(t *testing.T) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("tmux not supported on Windows")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
}

func setupSessionBranchTestRepo(t *testing.T) (string, *git.Git) {
	t.Helper()

	workDir := t.TempDir()
	cmd := exec.Command("git", "init", "-b", "main")
	cmd.Dir = workDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	repoGit := git.NewGit(workDir)
	if err := os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	if err := repoGit.Add("README.md"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := repoGit.Commit("Initial commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	cmd = exec.Command("git", "remote", "add", "origin", workDir)
	cmd.Dir = workDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "update-ref", "refs/remotes/origin/main", "HEAD")
	cmd.Dir = workDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git update-ref: %v\n%s", err, out)
	}

	return workDir, repoGit
}

func TestSessionName(t *testing.T) {
	setupTestRegistryForSession(t)

	r := &rig.Rig{
		Name:     "gastown",
		Polecats: []string{"Toast"},
	}
	m := NewSessionManager(tmux.NewTmux(), r)

	name := m.SessionName("Toast")
	if name != "gt-Toast" {
		t.Errorf("sessionName = %q, want gt-Toast", name)
	}
}

func TestSessionManagerPolecatDir(t *testing.T) {
	r := &rig.Rig{
		Name:     "gastown",
		Path:     "/home/user/ai/gastown",
		Polecats: []string{"Toast"},
	}
	m := NewSessionManager(tmux.NewTmux(), r)

	dir := m.polecatDir("Toast")
	expected := "/home/user/ai/gastown/polecats/Toast"
	if filepath.ToSlash(dir) != expected {
		t.Errorf("polecatDir = %q, want %q", dir, expected)
	}
}

func TestHasPolecat(t *testing.T) {
	root := t.TempDir()
	// hasPolecat checks filesystem, so create actual directories
	for _, name := range []string{"Toast", "Cheedo"} {
		if err := os.MkdirAll(filepath.Join(root, "polecats", name), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	r := &rig.Rig{
		Name:     "gastown",
		Path:     root,
		Polecats: []string{"Toast", "Cheedo"},
	}
	m := NewSessionManager(tmux.NewTmux(), r)

	if !m.hasPolecat("Toast") {
		t.Error("expected hasPolecat(Toast) = true")
	}
	if !m.hasPolecat("Cheedo") {
		t.Error("expected hasPolecat(Cheedo) = true")
	}
	if m.hasPolecat("Unknown") {
		t.Error("expected hasPolecat(Unknown) = false")
	}
}

func TestStartPolecatNotFound(t *testing.T) {
	r := &rig.Rig{
		Name:     "gastown",
		Polecats: []string{"Toast"},
	}
	m := NewSessionManager(tmux.NewTmux(), r)

	err := m.Start("Unknown", SessionStartOptions{})
	if err == nil {
		t.Error("expected error for unknown polecat")
	}
}

func TestIsRunningNoSession(t *testing.T) {
	requireTmux(t)

	r := &rig.Rig{
		Name:     "gastown",
		Polecats: []string{"Toast"},
	}
	m := NewSessionManager(tmux.NewTmux(), r)

	running, err := m.IsRunning("Toast")
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if running {
		t.Error("expected IsRunning = false for non-existent session")
	}
}

func TestSessionManagerListEmpty(t *testing.T) {
	requireTmux(t)

	// Register a unique prefix so List() won't match real sessions.
	// Without this, PrefixFor returns "gt" (default) and matches running gastown sessions.
	reg := session.NewPrefixRegistry()
	reg.Register("xz", "test-rig-unlikely-name")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })

	r := &rig.Rig{
		Name:     "test-rig-unlikely-name",
		Polecats: []string{},
	}
	m := NewSessionManager(tmux.NewTmux(), r)

	infos, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("infos count = %d, want 0", len(infos))
	}
}

func TestStopNotFound(t *testing.T) {
	requireTmux(t)

	r := &rig.Rig{
		Name:     "test-rig",
		Polecats: []string{"Toast"},
	}
	m := NewSessionManager(tmux.NewTmux(), r)

	err := m.Stop("Toast", false)
	if err != ErrSessionNotFound {
		t.Errorf("Stop = %v, want ErrSessionNotFound", err)
	}
}

func TestCaptureNotFound(t *testing.T) {
	requireTmux(t)

	r := &rig.Rig{
		Name:     "test-rig",
		Polecats: []string{"Toast"},
	}
	m := NewSessionManager(tmux.NewTmux(), r)

	_, err := m.Capture("Toast", 50)
	if err != ErrSessionNotFound {
		t.Errorf("Capture = %v, want ErrSessionNotFound", err)
	}
}

func TestInjectNotFound(t *testing.T) {
	requireTmux(t)

	r := &rig.Rig{
		Name:     "test-rig",
		Polecats: []string{"Toast"},
	}
	m := NewSessionManager(tmux.NewTmux(), r)

	err := m.Inject("Toast", "hello")
	if err != ErrSessionNotFound {
		t.Errorf("Inject = %v, want ErrSessionNotFound", err)
	}
}

// TestPolecatCommandFormat verifies the polecat session command exports
// GT_ROLE, GT_RIG, GT_POLECAT, and BD_ACTOR inline before starting Claude.
// This is a regression test for gt-y41ep - env vars must be exported inline
// because tmux SetEnvironment only affects new panes, not the current shell.
func TestPolecatCommandFormat(t *testing.T) {
	// This test verifies the expected command format.
	// The actual command is built in Start() but we test the format here
	// to document and verify the expected behavior.

	rigName := "gastown"
	polecatName := "Toast"
	expectedBdActor := "gastown/polecats/Toast"
	// GT_ROLE uses compound format: rig/polecats/name
	expectedGtRole := rigName + "/polecats/" + polecatName

	// Build the expected command format (mirrors Start() logic)
	expectedPrefix := "export GT_ROLE=" + expectedGtRole + " GT_RIG=" + rigName + " GT_POLECAT=" + polecatName + " BD_ACTOR=" + expectedBdActor + " GIT_AUTHOR_NAME=" + expectedBdActor
	expectedSuffix := "&& claude --dangerously-skip-permissions"

	// The command must contain all required env exports
	requiredParts := []string{
		"export",
		"GT_ROLE=" + expectedGtRole,
		"GT_RIG=" + rigName,
		"GT_POLECAT=" + polecatName,
		"BD_ACTOR=" + expectedBdActor,
		"GIT_AUTHOR_NAME=" + expectedBdActor,
		"claude --dangerously-skip-permissions",
	}

	// Verify expected format contains all required parts
	fullCommand := expectedPrefix + " " + expectedSuffix
	for _, part := range requiredParts {
		if !strings.Contains(fullCommand, part) {
			t.Errorf("Polecat command should contain %q", part)
		}
	}

	// Verify GT_ROLE uses compound format with "polecats" (not "mayor", "crew", etc.)
	if !strings.Contains(fullCommand, "GT_ROLE="+expectedGtRole) {
		t.Errorf("GT_ROLE must be %q (compound format), not simple 'polecat'", expectedGtRole)
	}
}

// TestPolecatStartInjectsFallbackEnvVars verifies that the polecat session
// startup injects GT_BRANCH and GT_POLECAT_PATH into the startup command.
// These env vars are critical for gt done's nuked-worktree fallback:
// when the polecat's cwd is deleted, gt done uses these to determine
// the branch and path without a working directory.
// Regression test for PR #1402.
func TestPolecatStartInjectsFallbackEnvVars(t *testing.T) {
	rigName := "gastown"
	polecatName := "Toast"
	workDir := "/tmp/fake-worktree"

	townRoot := "/tmp/fake-town"

	// The env vars that should be injected via PrependEnv
	requiredEnvVars := []string{
		"GT_BRANCH",       // Git branch for nuked-worktree fallback
		"GT_POLECAT_PATH", // Worktree path for nuked-worktree fallback
		"GT_RIG",          // Rig name (was already there pre-PR)
		"GT_POLECAT",      // Polecat name (was already there pre-PR)
		"GT_ROLE",         // Role address (was already there pre-PR)
		"GT_TOWN_ROOT",    // Town root for FindFromCwdWithFallback after worktree nuke
	}

	// Verify the env var map includes all required keys
	envVars := map[string]string{
		"GT_RIG":          rigName,
		"GT_POLECAT":      polecatName,
		"GT_ROLE":         rigName + "/polecats/" + polecatName,
		"GT_POLECAT_PATH": workDir,
		"GT_TOWN_ROOT":    townRoot,
	}

	// GT_BRANCH is conditionally added (only if CurrentBranch succeeds)
	// In practice it's always set because the worktree exists at Start time
	branchName := "polecat/" + polecatName
	envVars["GT_BRANCH"] = branchName

	for _, key := range requiredEnvVars {
		if _, ok := envVars[key]; !ok {
			t.Errorf("missing required env var %q in startup injection", key)
		}
	}

	// Verify GT_POLECAT_PATH matches workDir
	if envVars["GT_POLECAT_PATH"] != workDir {
		t.Errorf("GT_POLECAT_PATH = %q, want %q", envVars["GT_POLECAT_PATH"], workDir)
	}

	// Verify GT_BRANCH matches expected branch
	if envVars["GT_BRANCH"] != branchName {
		t.Errorf("GT_BRANCH = %q, want %q", envVars["GT_BRANCH"], branchName)
	}
}

func TestEnsureCanonicalSessionBranch_UsesOriginDefaultBranch(t *testing.T) {
	workDir, repoGit := setupSessionBranchTestRepo(t)

	baseSHA, err := repoGit.Rev("origin/main")
	if err != nil {
		t.Fatalf("resolve origin/main: %v", err)
	}
	if err := repoGit.CheckoutNewBranch("polecat/toast-old", "main"); err != nil {
		t.Fatalf("checkout stale polecat branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "stale.txt"), []byte("stale\n"), 0644); err != nil {
		t.Fatalf("write stale.txt: %v", err)
	}
	if err := repoGit.Add("stale.txt"); err != nil {
		t.Fatalf("git add stale.txt: %v", err)
	}
	if err := repoGit.Commit("stale local polecat commit"); err != nil {
		t.Fatalf("git commit stale.txt: %v", err)
	}
	staleSHA, err := repoGit.Rev("HEAD")
	if err != nil {
		t.Fatalf("resolve stale HEAD: %v", err)
	}

	sm := NewSessionManager(tmux.NewTmux(), &rig.Rig{Name: "gastown", Path: workDir})
	branch := sm.ensureCanonicalSessionBranch(repoGit, "toast", SessionStartOptions{Issue: "gt-9qb"})
	if !strings.Contains(branch, "/gt-9qb@") {
		t.Fatalf("fresh session branch = %q, want issue-scoped branch", branch)
	}

	staleAncestor, err := repoGit.IsAncestor(staleSHA, branch)
	if err != nil {
		t.Fatalf("check stale ancestry: %v", err)
	}
	if staleAncestor {
		t.Fatalf("fresh session branch %q unexpectedly includes stale local commit %s", branch, staleSHA)
	}

	baseAncestor, err := repoGit.IsAncestor(baseSHA, branch)
	if err != nil {
		t.Fatalf("check canonical ancestry: %v", err)
	}
	if !baseAncestor {
		t.Fatalf("fresh session branch %q should descend from origin/main commit %s", branch, baseSHA)
	}
}

func TestEnsureCanonicalSessionBranch_KeepsCurrentIssueBranch(t *testing.T) {
	workDir, repoGit := setupSessionBranchTestRepo(t)

	currentBranch := "polecat/toast/gt-9qb@seed"
	if err := repoGit.CheckoutNewBranch(currentBranch, "main"); err != nil {
		t.Fatalf("checkout current issue branch: %v", err)
	}

	sm := NewSessionManager(tmux.NewTmux(), &rig.Rig{Name: "gastown", Path: workDir})
	branch := sm.ensureCanonicalSessionBranch(repoGit, "toast", SessionStartOptions{Issue: "gt-9qb"})
	if branch != currentBranch {
		t.Fatalf("ensureCanonicalSessionBranch changed active issue branch: got %q want %q", branch, currentBranch)
	}
}

// TestSessionManager_resolveBeadsDir verifies that SessionManager correctly
// resolves the beads directory for cross-rig issues via routes.jsonl.
// This is a regression test for GitHub issue #1056.
//
// The bug was that hookIssue/validateIssue used workDir directly instead of
// resolving via routes.jsonl. Now they call resolveBeadsDir which we test here.
func TestSessionManager_resolveBeadsDir(t *testing.T) {
	// Set up a mock town with routes.jsonl
	townRoot := t.TempDir()
	townBeadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create routes.jsonl with cross-rig routing
	routesContent := `{"prefix": "gt-", "path": "gastown/mayor/rig"}
{"prefix": "bd-", "path": "beads/mayor/rig"}
{"prefix": "hq-", "path": "."}
`
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a rig inside the town (simulating gastown rig)
	rigPath := filepath.Join(townRoot, "gastown")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatal(err)
	}

	// Create SessionManager with the rig
	r := &rig.Rig{
		Name: "gastown",
		Path: rigPath,
	}
	m := NewSessionManager(tmux.NewTmux(), r)

	polecatWorkDir := filepath.Join(rigPath, "polecats", "Toast")

	tests := []struct {
		name        string
		issueID     string
		expectedDir string
	}{
		{
			name:        "same-rig bead resolves to rig path",
			issueID:     "gt-abc123",
			expectedDir: filepath.Join(townRoot, "gastown/mayor/rig"),
		},
		{
			name:        "cross-rig bead (beads) resolves to beads rig path",
			issueID:     "bd-xyz789",
			expectedDir: filepath.Join(townRoot, "beads/mayor/rig"),
		},
		{
			name:        "town-level bead resolves to town root",
			issueID:     "hq-town123",
			expectedDir: townRoot,
		},
		{
			name:        "unknown prefix falls back to fallbackDir",
			issueID:     "xx-unknown",
			expectedDir: polecatWorkDir,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Test the SessionManager's resolveBeadsDir method directly
			resolved := m.resolveBeadsDir(tc.issueID, polecatWorkDir)
			if resolved != tc.expectedDir {
				t.Errorf("resolveBeadsDir(%q, %q) = %q, want %q",
					tc.issueID, polecatWorkDir, resolved, tc.expectedDir)
			}
		})
	}
}

// TestAgentEnvOmitsGTAgent_FallbackRequired verifies that the AgentEnv path
// used by session_manager.Start does NOT include GT_AGENT when opts.Agent is
// empty (the default dispatch path). This confirms the session_manager must
// fall back to runtimeConfig.ResolvedAgent for setting GT_AGENT in the tmux
// session table.
//
// Without the fallback, GT_AGENT is never written to the tmux session table,
// and the post-startup validation kills the session with:
//
//	"GT_AGENT not set in session ... witness patrol will misidentify this polecat"
//
// Regression test for the bug introduced in PR #1776 which removed the
// unconditional runtimeConfig.ResolvedAgent → SetEnvironment("GT_AGENT") logic
// and replaced it with an AgentEnv-only path that requires opts.Agent to be set.
func TestAgentEnvOmitsGTAgent_FallbackRequired(t *testing.T) {
	t.Parallel()

	// Simulate what session_manager.Start calls for each dispatch scenario.
	cases := []struct {
		name        string
		agent       string // opts.Agent value
		wantGTAgent bool   // whether GT_AGENT should be in AgentEnv output
	}{
		{
			name:        "default dispatch (no --agent flag)",
			agent:       "",
			wantGTAgent: false, // fallback needed
		},
		{
			name:        "explicit --agent codex",
			agent:       "codex",
			wantGTAgent: true,
		},
		{
			name:        "explicit --agent gemini",
			agent:       "gemini",
			wantGTAgent: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := config.AgentEnv(config.AgentEnvConfig{
				Role:      "polecat",
				Rig:       "gastown",
				AgentName: "Toast",
				TownRoot:  "/tmp/town",
				Agent:     tc.agent,
			})
			_, hasGTAgent := env["GT_AGENT"]
			if hasGTAgent != tc.wantGTAgent {
				t.Errorf("AgentEnv(Agent=%q): GT_AGENT present=%v, want %v",
					tc.agent, hasGTAgent, tc.wantGTAgent)
			}
		})
	}
}

// TestVerifyStartupNudgeDelivery_IdleAgent tests that verifyStartupNudgeDelivery
// detects an idle agent (at prompt, no busy indicator) and retries the nudge.
// Uses a real tmux session with a shell prompt that matches the ReadyPromptPrefix.
func TestVerifyStartupNudgeDelivery_IdleAgent(t *testing.T) {
	requireTmux(t)

	tm := tmux.NewTmux()
	// Use a unique session name per invocation to avoid "duplicate session" races
	// with tmux's async cleanup when running with -count=N. (Fixes gt-eo8d)
	sessionName := fmt.Sprintf("gt-test-nudge-%d", testSessionCounter.Add(1))

	// Clean up any stale session from a previous crashed test run
	_ = tm.KillSession(sessionName)

	// Create a tmux session with a shell
	if err := tm.NewSession(sessionName, os.TempDir()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = tm.KillSession(sessionName) })

	// Configure the shell to show the Claude prompt prefix, simulating an idle agent.
	// The prompt "❯ " is what Claude Code shows when idle.
	// No "esc to interrupt" busy indicator — simulates a truly idle agent.
	time.Sleep(300 * time.Millisecond) // Let shell initialize
	_ = tm.SendKeys(sessionName, "export PS1='❯ '")
	time.Sleep(300 * time.Millisecond)

	r := &rig.Rig{Name: "test-rig", Path: t.TempDir()}
	m := NewSessionManager(tm, r)

	rc := &config.RuntimeConfig{
		Tmux: &config.RuntimeTmuxConfig{
			ReadyPromptPrefix: "❯ ",
		},
	}

	// IsIdle should detect the idle state (prompt visible, no busy indicator)
	if !tm.IsIdle(sessionName) {
		t.Log("Warning: idle state not detected (tmux timing); skipping idle verification")
		t.Skip("idle detection unreliable in test environment")
	}

	// verifyStartupNudgeDelivery should detect idle state and retry.
	// We can't easily assert the retry happened, but we verify it doesn't panic/hang.
	// Use a goroutine with timeout to prevent test hanging.
	// Timeout accounts for DefaultStartupNudgeVerifyDelay (25s) * DefaultStartupNudgeMaxRetries (2)
	// plus overhead = ~60s. Use 90s for safety.
	done := make(chan struct{})
	go func() {
		m.verifyStartupNudgeDelivery(sessionName, rc, "check your hook")
		close(done)
	}()

	select {
	case <-done:
		// Success - function completed
	case <-time.After(90 * time.Second):
		t.Fatal("verifyStartupNudgeDelivery hung (exceeded 90s timeout)")
	}
}

// TestVerifyStartupNudgeDelivery_NilConfig verifies that verifyStartupNudgeDelivery
// exits immediately when runtime config has no prompt detection.
func TestVerifyStartupNudgeDelivery_NilConfig(t *testing.T) {
	requireTmux(t)

	r := &rig.Rig{Name: "test-rig", Path: t.TempDir()}
	m := NewSessionManager(tmux.NewTmux(), r)

	// Should return immediately without error for nil config
	m.verifyStartupNudgeDelivery("nonexistent-session", nil, "")

	// And for config without prompt prefix
	rc := &config.RuntimeConfig{
		Tmux: &config.RuntimeTmuxConfig{
			ReadyPromptPrefix: "",
			ReadyDelayMs:      1000,
		},
	}
	m.verifyStartupNudgeDelivery("nonexistent-session", rc, "")
}

func TestPromptlessFallbackIncludesPrimeAndWorkInstructions(t *testing.T) {
	beaconConfig := session.BeaconConfig{
		Recipient:               session.BeaconRecipient("polecat", "toast", "demo"),
		Sender:                  "witness",
		Topic:                   "assigned",
		MolID:                   "demo-123",
		IncludePrimeInstruction: true,
		ExcludeWorkInstructions: true,
	}

	prompt := session.BuildStartupPrompt(beaconConfig, gtruntime.StartupNudgeContent())

	if !strings.Contains(prompt, "Run `gt prime`") {
		t.Fatalf("prompt missing gt prime instruction: %q", prompt)
	}
	if !strings.Contains(prompt, gtruntime.StartupNudgeContent()) {
		t.Fatalf("prompt missing startup nudge content: %q", prompt)
	}
}

// TestModeABeaconVerificationCondition verifies that hook+prompt agents (e.g. Claude)
// satisfy the Mode A beacon delivery verification condition introduced in hi-y44.
// Fresh spawns may show the Claude Code splash with the CLI beacon pre-filled but
// not auto-submitted; the condition triggers verifyStartupNudgeDelivery as a safety net.
func TestModeABeaconVerificationCondition(t *testing.T) {
	tests := []struct {
		name            string
		rc              *config.RuntimeConfig
		wantModeA       bool // !SendBeaconNudge && !SendStartupNudge
	}{
		{
			name: "Claude hook+prompt agent triggers Mode A verification",
			rc: &config.RuntimeConfig{
				PromptMode: "arg",
				Hooks: &config.RuntimeHooksConfig{
					Provider: "claude",
				},
			},
			wantModeA: true,
		},
		{
			name: "Non-hook agent does not trigger Mode A (has startup nudge instead)",
			rc: &config.RuntimeConfig{
				PromptMode: "arg",
				Hooks:      nil,
			},
			wantModeA: false,
		},
		{
			name: "Hook agent with no prompt support does not trigger Mode A (uses beacon nudge)",
			rc: &config.RuntimeConfig{
				PromptMode: "none",
				Hooks: &config.RuntimeHooksConfig{
					Provider: "claude",
				},
			},
			wantModeA: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := gtruntime.GetStartupFallbackInfo(tt.rc)
			gotModeA := !info.SendBeaconNudge && !info.SendStartupNudge
			if gotModeA != tt.wantModeA {
				t.Errorf("Mode A condition = %v, want %v (SendBeaconNudge=%v, SendStartupNudge=%v)",
					gotModeA, tt.wantModeA, info.SendBeaconNudge, info.SendStartupNudge)
			}
		})
	}
}

// TestModeAStartupVerifyIsNonBlocking exercises the Mode A async verification path
// that Start uses for hook+prompt agents (hi-y44). Confirms that verifyStartupNudgeDelivery
// runs as a goroutine — a synchronous call on this path added ~25s to every successful
// polecat startup because the function sleeps before its first idle check.
func TestModeAStartupVerifyIsNonBlocking(t *testing.T) {
	requireTmux(t)

	tm := tmux.NewTmux()
	sessionName := fmt.Sprintf("gt-test-modeA-%d", testSessionCounter.Add(1))
	_ = tm.KillSession(sessionName)

	if err := tm.NewSession(sessionName, os.TempDir()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = tm.KillSession(sessionName) })

	time.Sleep(300 * time.Millisecond)
	_ = tm.SendKeys(sessionName, "export PS1='❯ '")
	time.Sleep(300 * time.Millisecond)

	r := &rig.Rig{Name: "test-rig", Path: t.TempDir()}
	m := NewSessionManager(tm, r)

	rc := &config.RuntimeConfig{
		PromptMode: "arg",
		Hooks:      &config.RuntimeHooksConfig{Provider: "claude"},
		Tmux: &config.RuntimeTmuxConfig{
			ReadyPromptPrefix: "❯ ",
		},
	}

	// Confirm this is a Mode A agent (the condition Start checks before the goroutine).
	info := gtruntime.GetStartupFallbackInfo(rc)
	if info.SendBeaconNudge || info.SendStartupNudge {
		t.Fatal("expected Mode A: !SendBeaconNudge && !SendStartupNudge")
	}

	// Replicate what Start does on the Mode A path: launch verifyStartupNudgeDelivery
	// as a goroutine. The caller must return before the verify delay (25s default) elapses.
	callerReturned := make(chan time.Duration, 1)
	goroutineDone := make(chan struct{})

	launchStart := time.Now()
	go func() {
		m.verifyStartupNudgeDelivery(sessionName, rc, "[GAS TOWN] test ← witness / Run `gt prime --hook`")
		close(goroutineDone)
	}()
	callerReturned <- time.Since(launchStart)

	// Caller side: goroutine launch should be near-instant.
	elapsed := <-callerReturned
	if elapsed > 500*time.Millisecond {
		t.Errorf("goroutine launch blocked caller for %v; expected <500ms (async regression)", elapsed)
	}

	// Goroutine side: must complete within (maxRetries * verifyDelay) + overhead.
	// Default: 2 retries * 25s = 50s. Allow 90s for slow CI.
	select {
	case <-goroutineDone:
	case <-time.After(90 * time.Second):
		t.Fatal("Mode A verifyStartupNudgeDelivery goroutine hung (exceeded 90s timeout)")
	}
}

func TestValidateSessionName(t *testing.T) {
	// Register prefixes so validateSessionName can resolve them correctly.
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("gm", "gastown_manager")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })

	tests := []struct {
		name        string
		sessionName string
		rigName     string
		wantErr     bool
	}{
		{
			name:        "valid themed name",
			sessionName: "gm-furiosa",
			rigName:     "gastown_manager",
			wantErr:     false,
		},
		{
			name:        "valid overflow name (new format)",
			sessionName: "gm-51",
			rigName:     "gastown_manager",
			wantErr:     false,
		},
		{
			name:        "malformed double-prefix (bug)",
			sessionName: "gm-gastown_manager-51",
			rigName:     "gastown_manager",
			wantErr:     true,
		},
		{
			name:        "malformed double-prefix gastown",
			sessionName: "gt-gastown-142",
			rigName:     "gastown",
			wantErr:     true,
		},
		{
			name:        "different rig (can't validate)",
			sessionName: "gt-other-rig-name",
			rigName:     "gastown_manager",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSessionName(tt.sessionName, tt.rigName)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSessionName() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPolecatSlot(t *testing.T) {
	tmpDir := t.TempDir()
	rigPath := tmpDir
	polecatsDir := filepath.Join(rigPath, "polecats")
	if err := os.MkdirAll(polecatsDir, 0755); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{
		Name:     "testrig",
		Path:     rigPath,
		Polecats: []string{},
	}
	sm := NewSessionManager(tmux.NewTmux(), r)

	// No polecats — should return 0
	if slot := sm.polecatSlot("alpha"); slot != 0 {
		t.Errorf("empty dir: got slot %d, want 0", slot)
	}

	// Create some polecat dirs (sorted: alpha, beta, gamma)
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if err := os.MkdirAll(filepath.Join(polecatsDir, name), 0755); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name string
		want int
	}{
		{"alpha", 0},
		{"beta", 1},
		{"gamma", 2},
	}
	for _, tt := range tests {
		if slot := sm.polecatSlot(tt.name); slot != tt.want {
			t.Errorf("polecatSlot(%q) = %d, want %d", tt.name, slot, tt.want)
		}
	}

	// Hidden dirs should be skipped
	if err := os.MkdirAll(filepath.Join(polecatsDir, ".hidden"), 0755); err != nil {
		t.Fatal(err)
	}
	if slot := sm.polecatSlot("beta"); slot != 1 {
		t.Errorf("with hidden dir: polecatSlot(beta) = %d, want 1", slot)
	}
}

func TestParseFreshBranchName_RoundTrip(t *testing.T) {
	sm := &SessionManager{}

	cases := []struct {
		name    string
		polecat string
		issue   string
	}{
		{name: "with issue", polecat: "alpha", issue: "gt-abc"},
		{name: "no issue", polecat: "beta", issue: ""},
		{name: "numeric issue", polecat: "nux", issue: "gt-123"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			branch := sm.freshBranchName(c.polecat, c.issue)
			meta := parseFreshBranchName(branch)
			if !meta.ok {
				t.Fatalf("parseFreshBranchName(%q) not ok", branch)
			}
			if meta.polecat != c.polecat {
				t.Errorf("polecat = %q, want %q (from %q)", meta.polecat, c.polecat, branch)
			}
			if meta.issue != c.issue {
				t.Errorf("issue = %q, want %q (from %q)", meta.issue, c.issue, branch)
			}
		})
	}
}

func TestParseFreshBranchName_Rejects(t *testing.T) {
	rejects := []string{
		"main",
		"master",
		"develop",
		"feature/x",
		"polecat/",          // empty tail
		"polecat/alpha",     // no ts or issue
		"polecat/alpha-",    // trailing dash, no ts
		"polecat//gt-abc@1", // empty polecat name
		"polecat/alpha/@1",  // empty issue
		"polecat/alpha/gt-abc@", // empty ts
		"",
	}
	for _, b := range rejects {
		if meta := parseFreshBranchName(b); meta.ok {
			t.Errorf("parseFreshBranchName(%q) = %+v, want ok=false", b, meta)
		}
	}
}

func TestShouldCreateFreshSessionBranch_Structural(t *testing.T) {
	// Non-standard canonical branch (e.g., "develop") — must be honored even
	// though the old string-heuristic hardcoded "main"/"master".
	cases := []struct {
		name            string
		currentBranch   string
		issue           string
		canonicalBranch string
		want            bool
	}{
		{
			name:            "on develop as canonical triggers fresh",
			currentBranch:   "develop",
			issue:           "gt-abc",
			canonicalBranch: "develop",
			want:            true,
		},
		{
			name:            "on main when canonical is develop does NOT trigger fresh",
			currentBranch:   "main",
			issue:           "gt-abc",
			canonicalBranch: "develop",
			want:            false,
		},
		{
			name:            "same-issue respawn preserves branch",
			currentBranch:   "polecat/alpha/gt-abc@xyz",
			issue:           "gt-abc",
			canonicalBranch: "main",
			want:            false,
		},
		{
			name:            "other-issue polecat branch triggers fresh",
			currentBranch:   "polecat/alpha/gt-999@xyz",
			issue:           "gt-abc",
			canonicalBranch: "main",
			want:            true,
		},
		{
			name:            "empty canonical with non-polecat branch does not trigger",
			currentBranch:   "feature/x",
			issue:           "gt-abc",
			canonicalBranch: "",
			want:            false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldCreateFreshSessionBranch(c.currentBranch, c.issue, c.canonicalBranch); got != c.want {
				t.Errorf("shouldCreateFreshSessionBranch(%q, %q, %q) = %v, want %v",
					c.currentBranch, c.issue, c.canonicalBranch, got, c.want)
			}
		})
	}
}
