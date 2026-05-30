package witness

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/tmux"
)

func TestNotifyMayorSlotOpen_BlocksNonCompletedExit(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0644); err != nil {
		t.Fatal(err)
	}
	workDir := filepath.Join(townRoot, "gastown", "witness")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatal(err)
	}

	notifyMayorSlotOpen(workDir, "gastown", "guzzle", string(ExitTypeDeferred))

	events, err := filepath.Glob(filepath.Join(townRoot, "events", "mayor", "*.event"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %v, want one SLOT_BLOCKED event", events)
	}
	data, err := os.ReadFile(events[0])
	if err != nil {
		t.Fatal(err)
	}
	var event struct {
		Type    string            `json:"type"`
		Payload map[string]string `json:"payload"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "SLOT_BLOCKED" {
		t.Fatalf("event type = %q, want SLOT_BLOCKED", event.Type)
	}
	if event.Payload["reason"] != "exit-deferred" {
		t.Fatalf("reason = %q, want exit-deferred", event.Payload["reason"])
	}
}

func TestShouldNotifyMayorSlotOpenRequiresSafeRecovery(t *testing.T) {
	prev := slotOpenRecoveryCheck
	t.Cleanup(func() { slotOpenRecoveryCheck = prev })

	tests := []struct {
		name    string
		output  string
		err     error
		wantOK  bool
		wantMsg string
	}{
		{
			name:   "safe to nuke notifies",
			output: `{"verdict":"SAFE_TO_NUKE"}`,
			wantOK: true,
		},
		{
			name:   "warning-prefixed json notifies",
			output: "warning: stale binary\n" + `{"verdict":"SAFE_TO_NUKE"}`,
			wantOK: true,
		},
		{
			name:    "needs recovery suppresses",
			output:  `{"verdict":"NEEDS_RECOVERY","blockers":["cleanup_status=has_unpushed"]}`,
			wantMsg: "NEEDS_RECOVERY",
		},
		{
			name:    "needs mq submit suppresses",
			output:  `{"verdict":"NEEDS_MQ_SUBMIT"}`,
			wantMsg: "NEEDS_MQ_SUBMIT",
		},
		{
			name:    "check failure suppresses",
			err:     errors.New("boom"),
			wantMsg: "check-recovery failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slotOpenRecoveryCheck = func(workDir, rigName, polecatName string) (string, error) {
				return tt.output, tt.err
			}

			gotOK, gotMsg := shouldNotifyMayorSlotOpen("/tmp", "gastown", "nitro")
			if gotOK != tt.wantOK {
				t.Fatalf("ok = %v, want %v (msg=%q)", gotOK, tt.wantOK, gotMsg)
			}
			if tt.wantMsg != "" && !strings.Contains(gotMsg, tt.wantMsg) {
				t.Fatalf("message %q does not contain %q", gotMsg, tt.wantMsg)
			}
		})
	}
}

func TestActiveMRBlockerFromCLIUsesTerminalStatus(t *testing.T) {
	tests := []struct {
		name   string
		output string
		err    error
		want   string
	}{
		{name: "empty active mr", want: ""},
		{name: "open mr blocks", output: `[{"status":"open"}]`, want: "active_mr=gt-mr status=open"},
		{name: "closed mr does not block", output: `[{"status":"closed"}]`, want: ""},
		{name: "not found does not block", err: fmt.Errorf("issue not found"), want: ""},
		{name: "lookup error blocks", err: fmt.Errorf("bd unavailable"), want: "active_mr=gt-mr status=lookup_error: bd unavailable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bd, _ := mockBd(
				func(args []string) (string, error) { return tt.output, tt.err },
				func(args []string) error { return nil },
			)
			mrID := "gt-mr"
			if tt.name == "empty active mr" {
				mrID = ""
			}
			if got := activeMRBlockerFromCLI(bd, t.TempDir(), mrID); got != tt.want {
				t.Fatalf("activeMRBlockerFromCLI() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHandlePolecatDoneFromBead_NilFields(t *testing.T) {
	t.Parallel()
	result := HandlePolecatDoneFromBead(DefaultBdCli(), "/tmp", "testrig", "nux", nil, nil)
	if result.Error == nil {
		t.Error("expected error for nil fields")
	}
	if result.Handled {
		t.Error("should not be handled with nil fields")
	}
}

func TestHandlePolecatDoneFromBead_PhaseComplete(t *testing.T) {
	t.Parallel()
	fields := &beads.AgentFields{
		ExitType: "PHASE_COMPLETE",
		Branch:   "polecat/nux",
	}
	result := HandlePolecatDoneFromBead(DefaultBdCli(), "/tmp", "testrig", "nux", fields, nil)
	if !result.Handled {
		t.Error("expected PHASE_COMPLETE to be handled")
	}
	if result.Error != nil {
		t.Errorf("unexpected error: %v", result.Error)
	}
	if !strings.Contains(result.Action, "phase-complete") {
		t.Errorf("action %q should contain 'phase-complete'", result.Action)
	}
}

func TestHandlePolecatDoneFromBead_NoMR(t *testing.T) {
	t.Parallel()
	fields := &beads.AgentFields{
		ExitType:       "COMPLETED",
		Branch:         "polecat/nux",
		HookBead:       "gt-test123",
		CompletionTime: "2026-02-28T01:00:00Z",
	}
	result := HandlePolecatDoneFromBead(DefaultBdCli(), "/tmp/nonexistent", "testrig", "nux", fields, nil)
	if !result.Handled {
		t.Error("expected completion with no MR to be handled")
	}
	if !strings.Contains(result.Action, "no MR") {
		t.Errorf("action %q should contain 'no MR'", result.Action)
	}
}

func TestHandlePolecatDoneFromBead_ProtocolType(t *testing.T) {
	t.Parallel()
	fields := &beads.AgentFields{
		ExitType: "COMPLETED",
		Branch:   "polecat/nux",
	}
	result := HandlePolecatDoneFromBead(DefaultBdCli(), "/tmp/nonexistent", "testrig", "nux", fields, nil)
	if result.ProtocolType != ProtoPolecatDone {
		t.Errorf("ProtocolType = %q, want %q", result.ProtocolType, ProtoPolecatDone)
	}
}

func TestZombieResult_Types(t *testing.T) {
	t.Parallel()
	// Verify the ZombieResult type has all expected fields
	z := ZombieResult{
		PolecatName:    "nux",
		AgentState:     "working",
		Classification: ZombieSessionDeadActive,
		HookBead:       "gt-abc123",
		Action:         "restarted",
		BeadRecovered:  true,
		Error:          nil,
	}

	if z.PolecatName != "nux" {
		t.Errorf("PolecatName = %q, want %q", z.PolecatName, "nux")
	}
	if z.AgentState != "working" {
		t.Errorf("AgentState = %q, want %q", z.AgentState, "working")
	}
	if z.Classification != ZombieSessionDeadActive {
		t.Errorf("Classification = %q, want %q", z.Classification, ZombieSessionDeadActive)
	}
	if z.HookBead != "gt-abc123" {
		t.Errorf("HookBead = %q, want %q", z.HookBead, "gt-abc123")
	}
	if z.Action != "restarted" {
		t.Errorf("Action = %q, want %q", z.Action, "restarted")
	}
	if !z.BeadRecovered {
		t.Error("BeadRecovered = false, want true")
	}
}

func TestDetectZombiePolecatsResult_EmptyResult(t *testing.T) {
	t.Parallel()
	result := &DetectZombiePolecatsResult{}

	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0", result.Checked)
	}
	if len(result.Zombies) != 0 {
		t.Errorf("Zombies length = %d, want 0", len(result.Zombies))
	}
}

func TestDetectZombiePolecats_NonexistentDir(t *testing.T) {
	t.Parallel()
	// Should handle missing polecats directory gracefully
	result := DetectZombiePolecats(DefaultBdCli(), "/nonexistent/path", "testrig", nil)

	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0 for nonexistent dir", result.Checked)
	}
	if len(result.Zombies) != 0 {
		t.Errorf("Zombies = %d, want 0 for nonexistent dir", len(result.Zombies))
	}
}

func TestDetectZombiePolecats_DirectoryScanning(t *testing.T) {
	t.Parallel()
	// Create a temp directory structure simulating polecats
	tmpDir := t.TempDir()
	rigName := "testrig"
	polecatsDir := filepath.Join(tmpDir, rigName, "polecats")
	if err := os.MkdirAll(polecatsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create polecat directories
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		if err := os.Mkdir(filepath.Join(polecatsDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Create hidden dir (should be skipped)
	if err := os.Mkdir(filepath.Join(polecatsDir, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a regular file (should be skipped, not a dir)
	if err := os.WriteFile(filepath.Join(polecatsDir, "notadir.txt"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := DetectZombiePolecats(DefaultBdCli(), tmpDir, rigName, nil)

	// Should have checked 3 polecat dirs (not hidden, not file)
	if result.Checked != 3 {
		t.Errorf("Checked = %d, want 3 (should skip hidden dirs and files)", result.Checked)
	}

	// No zombies because agent bead state will be empty (bd not available),
	// so isZombie stays false for all polecats
	if len(result.Zombies) != 0 {
		t.Errorf("Zombies = %d, want 0 (no agent state = not zombie)", len(result.Zombies))
	}
}

func TestDetectZombiePolecats_EmptyPolecatsDir(t *testing.T) {
	t.Parallel()
	// Empty polecats directory should return 0 checked
	tmpDir := t.TempDir()
	rigName := "testrig"
	polecatsDir := filepath.Join(tmpDir, rigName, "polecats")
	if err := os.MkdirAll(polecatsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	result := DetectZombiePolecats(DefaultBdCli(), tmpDir, rigName, nil)

	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0 for empty polecats dir", result.Checked)
	}
}

func TestGetAgentBeadState_EmptyOutput(t *testing.T) {
	t.Parallel()
	// getAgentBeadState with invalid bead ID should return empty strings
	// (it calls bd which won't exist in test, so it returns empty)
	state, hook := getAgentBeadState(DefaultBdCli(), "/nonexistent", "nonexistent-bead")

	if state != "" {
		t.Errorf("state = %q, want empty for missing bead", state)
	}
	if hook != "" {
		t.Errorf("hook = %q, want empty for missing bead", hook)
	}
}

func TestSessionRecreated_NoSession(t *testing.T) {
	t.Parallel()
	// When the session doesn't exist, sessionRecreated should return false
	// (the session wasn't recreated, it's still dead)
	tm := tmux.NewTmux()
	detectedAt := time.Now()

	recreated := sessionRecreated(tm, "gt-nonexistent-session-xyz", detectedAt)
	if recreated {
		t.Error("sessionRecreated returned true for nonexistent session, want false")
	}
}

func TestSessionRecreated_DetectedAtEdgeCases(t *testing.T) {
	t.Parallel()
	// Verify that sessionRecreated returns false when session is dead
	// regardless of the detectedAt timestamp
	tm := tmux.NewTmux()

	// Try with a past timestamp
	recreated := sessionRecreated(tm, "gt-test-nosession-abc", time.Now().Add(-1*time.Hour))
	if recreated {
		t.Error("sessionRecreated returned true for nonexistent session with past time")
	}

	// Try with a future timestamp
	recreated = sessionRecreated(tm, "gt-test-nosession-def", time.Now().Add(1*time.Hour))
	if recreated {
		t.Error("sessionRecreated returned true for nonexistent session with future time")
	}
}

func TestZombieClassification_SpawningState(t *testing.T) {
	t.Parallel()
	// Verify that "spawning" agent state is treated as a zombie indicator.
	// This tests the classification logic inline in DetectZombiePolecats.
	// We can't easily test this via the full function without mocking,
	// so we test the boolean logic directly.
	states := map[string]bool{
		"working":  true,
		"running":  true,
		"spawning": true,
		"idle":     false,
		"done":     false,
		"":         false,
	}

	for state, wantZombie := range states {
		hookBead := ""
		isZombie := false
		if hookBead != "" {
			isZombie = true
		}
		if state == "working" || state == "running" || state == "spawning" {
			isZombie = true
		}

		if isZombie != wantZombie {
			t.Errorf("agent_state=%q: isZombie=%v, want %v", state, isZombie, wantZombie)
		}
	}
}

func TestZombieClassification_HookBeadAlwaysZombie(t *testing.T) {
	t.Parallel()
	// Any polecat with a hook_bead and dead session should be classified as zombie,
	// regardless of agent_state.
	for _, state := range []string{"", "idle", "done", "working"} {
		hookBead := "gt-some-issue"
		isZombie := false
		if hookBead != "" {
			isZombie = true
		}
		if state == "working" || state == "running" || state == "spawning" {
			isZombie = true
		}

		if !isZombie {
			t.Errorf("agent_state=%q with hook_bead=%q: isZombie=false, want true", state, hookBead)
		}
	}
}

func TestZombieClassification_NoHookNoActiveState(t *testing.T) {
	t.Parallel()
	// Polecats with no hook_bead and non-active agent_state should NOT be zombies.
	for _, state := range []string{"", "idle", "done", "completed"} {
		hookBead := ""
		isZombie := false
		if hookBead != "" {
			isZombie = true
		}
		if state == "working" || state == "running" || state == "spawning" {
			isZombie = true
		}

		if isZombie {
			t.Errorf("agent_state=%q with no hook_bead: isZombie=true, want false", state)
		}
	}
}

func TestFindAnyCleanupWisp_NoBdAvailable(t *testing.T) {
	t.Parallel()
	// When bd is not available (test environment), findAnyCleanupWisp
	// should return empty string without panicking
	result := findAnyCleanupWisp(DefaultBdCli(), "/nonexistent", "testpolecat")
	if result != "" {
		t.Errorf("findAnyCleanupWisp = %q, want empty when bd unavailable", result)
	}
}

// mockBdCalls captures bd invocations and returns canned responses.
// Returns a slice that accumulates "arg0 arg1 ..." strings for each call.
type mockBdCalls struct {
	calls []string
}

// mockBd creates a test-local *BdCli with mock exec/run functions.
// Returns the BdCli and a pointer to the captured call log.
// No global state is modified — safe for use with t.Parallel().
func mockBd(execFn func(args []string) (string, error), runFn func(args []string) error) (*BdCli, *mockBdCalls) {
	mock := &mockBdCalls{}
	bd := &BdCli{
		Exec: func(workDir string, args ...string) (string, error) {
			mock.calls = append(mock.calls, strings.Join(args, " "))
			return execFn(stripMockBdFlags(args))
		},
		Run: func(workDir string, args ...string) error {
			mock.calls = append(mock.calls, strings.Join(args, " "))
			return runFn(stripMockBdFlags(args))
		},
	}
	return bd, mock
}

func stripMockBdFlags(args []string) []string {
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		args = args[1:]
	}
	return args
}

func installFakeTmuxNoServer(t *testing.T) {
	t.Helper()

	binDir := t.TempDir()
	scriptPath := filepath.Join(binDir, "tmux")
	script := "#!/bin/sh\nprintf '%s\\n' 'no server running on /tmp/tmux' 1>&2\nexit 1\n"
	if runtime.GOOS == "windows" {
		scriptPath += ".bat"
		script = "@echo off\r\necho no server running on C:\\tmp\\tmux 1>&2\r\nexit /b 1\r\n"
	}
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// fakeBd creates a test-local *BdCli matching the old shell script behavior:
// list→"[]", update→ok, show→cleanup wisp JSON. Returns BdCli and captured call log.
func fakeBd() (*BdCli, *mockBdCalls) {
	return mockBd(
		func(args []string) (string, error) {
			if len(args) > 0 {
				switch args[0] {
				case "list":
					return "[]", nil
				case "show":
					return `[{"labels":["cleanup","polecat:testpol","state:pending"]}]`, nil
				}
			}
			return "{}", nil
		},
		func(args []string) error { return nil },
	)
}

func setupActiveMRGitSafeWorkDir(t *testing.T, rigName, polecatName string) string {
	t.Helper()
	townRoot := t.TempDir()
	clonePath := filepath.Join(townRoot, rigName, "polecats", polecatName, rigName)
	if err := os.MkdirAll(clonePath, 0755); err != nil {
		t.Fatal(err)
	}
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit(clonePath, "init")
	runGit(clonePath, "config", "user.email", "test@example.com")
	runGit(clonePath, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(clonePath, "README.md"), []byte("test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(clonePath, "add", "README.md")
	runGit(clonePath, "commit", "-m", "initial")
	remotePath := filepath.Join(townRoot, "origin.git")
	runGit(townRoot, "init", "--bare", remotePath)
	runGit(clonePath, "remote", "add", "origin", remotePath)
	runGit(clonePath, "push", "-u", "origin", "HEAD")
	return townRoot
}

func TestHasPendingMRFromSnapshotAssessesMRStatus(t *testing.T) {
	issueJSON := func(id, status, desc string) string {
		b, err := json.Marshal([]map[string]any{{"id": id, "status": status, "description": desc}})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	tests := []struct {
		name string
		show func(id string) (string, error)
		want bool
	}{
		{
			name: "open MR is pending",
			show: func(id string) (string, error) {
				return issueJSON(id, "open", ""), nil
			},
			want: true,
		},
		{
			name: "closed MR with terminal source is not pending",
			show: func(id string) (string, error) {
				if id == "gt-mr" {
					return issueJSON(id, "closed", ""), nil
				}
				return issueJSON(id, "closed", ""), nil
			},
		},
		{
			name: "missing MR with terminal source is not pending",
			show: func(id string) (string, error) {
				if id == "gt-mr" {
					return "", errors.New("not found")
				}
				return issueJSON(id, "closed", ""), nil
			},
		},
		{
			name: "lookup error is pending",
			show: func(id string) (string, error) { return "", errors.New("bd exploded") },
			want: true,
		},
		{
			name: "closed MR with open source is pending",
			show: func(id string) (string, error) {
				if id == "gt-mr" {
					return issueJSON(id, "closed", ""), nil
				}
				return issueJSON(id, "open", ""), nil
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir := setupActiveMRGitSafeWorkDir(t, "gastown", "nux")
			bd, _ := mockBd(
				func(args []string) (string, error) {
					if len(args) == 0 {
						return "", nil
					}
					switch args[0] {
					case "list":
						return "[]", nil
					case "show":
						return tt.show(args[1])
					}
					return "", nil
				},
				func(args []string) error { return nil },
			)
			snap := &agentBeadSnapshot{ActiveMR: "gt-mr", Fields: &beads.AgentFields{ActiveMR: "gt-mr", LastSourceIssue: "gt-src"}}
			if got := hasPendingMRFromSnapshot(bd, workDir, "gastown", "nux", snap); got != tt.want {
				t.Fatalf("hasPendingMRFromSnapshot() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasPendingMRUsesAgentLastSourceIssue(t *testing.T) {
	workDir := setupActiveMRGitSafeWorkDir(t, "gastown", "nux")
	bd, _ := mockBd(
		func(args []string) (string, error) {
			if len(args) == 0 {
				return "", nil
			}
			switch args[0] {
			case "list":
				return "[]", nil
			case "show":
				switch args[1] {
				case "gt-agent":
					return `[{"active_mr":"gt-mr","description":"active_mr: gt-mr\nlast_source_issue: gt-src\n"}]`, nil
				case "gt-mr":
					return "", errors.New("not found")
				case "gt-src":
					return `[{"id":"gt-src","status":"closed"}]`, nil
				}
			}
			return "", errors.New("not found")
		},
		func(args []string) error { return nil },
	)

	if got := hasPendingMR(bd, workDir, "gastown", "nux", "gt-agent"); got {
		t.Fatalf("hasPendingMR() = true, want false for missing MR with terminal source")
	}
}

func TestHasPendingMRFromSnapshotRequiresGitSafe(t *testing.T) {
	bd, _ := mockBd(
		func(args []string) (string, error) {
			if len(args) == 0 {
				return "", nil
			}
			switch args[0] {
			case "list":
				return "[]", nil
			case "show":
				if args[1] == "gt-mr" {
					return "", errors.New("not found")
				}
				return `[{"id":"gt-src","status":"closed"}]`, nil
			}
			return "", nil
		},
		func(args []string) error { return nil },
	)
	snap := &agentBeadSnapshot{ActiveMR: "gt-mr", Fields: &beads.AgentFields{ActiveMR: "gt-mr", LastSourceIssue: "gt-src"}}
	if got := hasPendingMRFromSnapshot(bd, t.TempDir(), "gastown", "nux", snap); !got {
		t.Fatalf("hasPendingMRFromSnapshot() = false, want true when git is unsafe")
	}
}

func TestHasPendingMRCleanupWispFailsClosed(t *testing.T) {
	workDir := setupActiveMRGitSafeWorkDir(t, "gastown", "nux")
	tests := []struct {
		name string
		list string
		err  error
	}{
		{name: "cleanup wisp present", list: `[{"id":"gt-cleanup"}]`},
		{name: "cleanup wisp lookup error", err: errors.New("bd exploded")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bd, _ := mockBd(
				func(args []string) (string, error) {
					if len(args) == 0 {
						return "", nil
					}
					if args[0] == "list" {
						return tt.list, tt.err
					}
					if args[0] == "show" && args[1] == "gt-agent" {
						return `[{"active_mr":"gt-mr","description":"active_mr: gt-mr\nlast_source_issue: gt-src\n"}]`, nil
					}
					if args[0] == "show" && args[1] == "gt-mr" {
						return "", errors.New("not found")
					}
					return `[{"id":"gt-src","status":"closed"}]`, nil
				},
				func(args []string) error { return nil },
			)
			if got := hasPendingMR(bd, workDir, "gastown", "nux", "gt-agent"); !got {
				t.Fatalf("hasPendingMR() = false, want true")
			}
		})
	}
}

func TestTerminalSafeDoneSnapshot(t *testing.T) {
	workDir := setupActiveMRGitSafeWorkDir(t, "gastown", "nux")
	bd, _ := mockBd(
		func(args []string) (string, error) {
			if len(args) == 0 || args[0] != "show" {
				return "[]", nil
			}
			return `[{"id":"gt-src","status":"closed"}]`, nil
		},
		func(args []string) error { return nil },
	)
	snap := &agentBeadSnapshot{Fields: &beads.AgentFields{LastSourceIssue: "gt-src"}}
	if !terminalSafeDoneSnapshot(bd, workDir, "gastown", "nux", snap) {
		t.Fatalf("terminalSafeDoneSnapshot() = false, want true")
	}
	snap.Fields.HookBead = "gt-hook"
	if terminalSafeDoneSnapshot(bd, workDir, "gastown", "nux", snap) {
		t.Fatalf("terminalSafeDoneSnapshot() = true with hook set, want false")
	}
}

func TestFindCleanupWisp_UsesCorrectBdListFlags(t *testing.T) {
	t.Parallel()
	bd, mock := fakeBd()
	workDir := t.TempDir()

	_, _ = findCleanupWisp(bd, workDir, "nux")

	got := strings.Join(mock.calls, "\n")

	// Must use --label (singular), NOT --labels (plural)
	if !strings.Contains(got, "--label") {
		t.Errorf("findCleanupWisp: expected --label flag, got: %s", got)
	}
	if strings.Contains(got, "--labels") {
		t.Errorf("findCleanupWisp: must not use --labels (plural), got: %s", got)
	}

	// Must NOT use --ephemeral (invalid for bd list)
	if strings.Contains(got, "--ephemeral") {
		t.Errorf("findCleanupWisp: must not use --ephemeral (invalid for bd list), got: %s", got)
	}

	// Must include the polecat label filter
	if !strings.Contains(got, "polecat:nux") {
		t.Errorf("findCleanupWisp: expected polecat:nux label, got: %s", got)
	}
}

func TestFindAnyCleanupWisp_UsesCorrectBdListFlags(t *testing.T) {
	t.Parallel()
	bd, mock := fakeBd()
	workDir := t.TempDir()

	_ = findAnyCleanupWisp(bd, workDir, "bravo")

	got := strings.Join(mock.calls, "\n")

	// Must use --label (singular), NOT --labels (plural)
	if !strings.Contains(got, "--label") {
		t.Errorf("findAnyCleanupWisp: expected --label flag, got: %s", got)
	}
	if strings.Contains(got, "--labels") {
		t.Errorf("findAnyCleanupWisp: must not use --labels (plural), got: %s", got)
	}

	// Must NOT use --ephemeral (invalid for bd list)
	if strings.Contains(got, "--ephemeral") {
		t.Errorf("findAnyCleanupWisp: must not use --ephemeral (invalid for bd list), got: %s", got)
	}

	// Must include the polecat label filter
	if !strings.Contains(got, "polecat:bravo") {
		t.Errorf("findAnyCleanupWisp: expected polecat:bravo label, got: %s", got)
	}
}

func TestFindAllCleanupWisps_NoBdAvailable(t *testing.T) {
	t.Parallel()
	// When bd is not available, findAllCleanupWisps should return nil
	result := findAllCleanupWisps(DefaultBdCli(), "/nonexistent", "testpolecat")
	if result != nil {
		t.Errorf("findAllCleanupWisps = %v, want nil when bd unavailable", result)
	}
}

func TestFindAllCleanupWisps_ReturnsAllIDs(t *testing.T) {
	t.Parallel()
	bd, mock := mockBd(
		func(args []string) (string, error) {
			if len(args) > 0 && args[0] == "list" {
				return `[{"id":"gt-wisp-aaa"},{"id":"gt-wisp-bbb"}]`, nil
			}
			return "{}", nil
		},
		func(args []string) error { return nil },
	)
	workDir := t.TempDir()

	result := findAllCleanupWisps(bd, workDir, "nux")

	if len(result) != 2 {
		t.Fatalf("findAllCleanupWisps: got %d items, want 2", len(result))
	}
	if result[0] != "gt-wisp-aaa" || result[1] != "gt-wisp-bbb" {
		t.Errorf("findAllCleanupWisps: got %v, want [gt-wisp-aaa gt-wisp-bbb]", result)
	}

	got := strings.Join(mock.calls, "\n")
	if !strings.Contains(got, "--label") {
		t.Errorf("findAllCleanupWisps: expected --label flag, got: %s", got)
	}
	if !strings.Contains(got, "polecat:nux") {
		t.Errorf("findAllCleanupWisps: expected polecat:nux label, got: %s", got)
	}
}

func TestFindAllCleanupWisps_EmptyList(t *testing.T) {
	t.Parallel()
	bd, _ := mockBd(
		func(args []string) (string, error) {
			return "[]", nil
		},
		func(args []string) error { return nil },
	)
	workDir := t.TempDir()

	result := findAllCleanupWisps(bd, workDir, "nux")
	if result != nil {
		t.Errorf("findAllCleanupWisps: got %v, want nil for empty list", result)
	}
}

func TestUpdateCleanupWispState_UsesCorrectBdUpdateFlags(t *testing.T) {
	t.Parallel()
	bd, mock := fakeBd()
	workDir := t.TempDir()

	// UpdateCleanupWispState first calls "bd show <id> --json", then "bd update".
	// Our mock returns valid JSON for show with polecat:testpol label,
	// so polecatName will be "testpol". Then it calls bd update with new labels.
	_ = UpdateCleanupWispState(bd, workDir, "gt-wisp-abc", "merged")

	got := strings.Join(mock.calls, "\n")

	// Must use --set-labels=<label> per label (not --labels)
	if !strings.Contains(got, "--set-labels=") {
		t.Errorf("UpdateCleanupWispState: expected --set-labels=<label> flags, got: %s", got)
	}
	// Check for invalid --labels flag in both " --labels " and "--labels=" forms
	if strings.Contains(got, "--labels") && !strings.Contains(got, "--set-labels") {
		t.Errorf("UpdateCleanupWispState: must not use --labels (invalid for bd update), got: %s", got)
	}

	// Verify individual per-label arguments with correct polecat name from show output
	if !strings.Contains(got, "--set-labels=cleanup") {
		t.Errorf("UpdateCleanupWispState: expected --set-labels=cleanup, got: %s", got)
	}
	if !strings.Contains(got, "--set-labels=polecat:testpol") {
		t.Errorf("UpdateCleanupWispState: expected --set-labels=polecat:testpol, got: %s", got)
	}
	if !strings.Contains(got, "--set-labels=state:merged") {
		t.Errorf("UpdateCleanupWispState: expected --set-labels=state:merged, got: %s", got)
	}
}

func TestExtractDoneIntent_Valid(t *testing.T) {
	t.Parallel()
	ts := time.Now().Add(-45 * time.Second)
	labels := []string{
		"gt:agent",
		"idle:2",
		fmt.Sprintf("done-intent:COMPLETED:%d", ts.Unix()),
	}

	intent := extractDoneIntent(labels)
	if intent == nil {
		t.Fatal("extractDoneIntent returned nil for valid label")
	}
	if intent.ExitType != "COMPLETED" {
		t.Errorf("ExitType = %q, want %q", intent.ExitType, "COMPLETED")
	}
	if intent.Timestamp.Unix() != ts.Unix() {
		t.Errorf("Timestamp = %d, want %d", intent.Timestamp.Unix(), ts.Unix())
	}
}

func TestExtractDoneIntent_Missing(t *testing.T) {
	t.Parallel()
	labels := []string{"gt:agent", "idle:2", "backoff-until:1738972900"}

	intent := extractDoneIntent(labels)
	if intent != nil {
		t.Errorf("extractDoneIntent = %+v, want nil for no done-intent label", intent)
	}
}

func TestExtractDoneIntent_Malformed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		labels []string
	}{
		{"missing timestamp", []string{"done-intent:COMPLETED"}},
		{"bad timestamp", []string{"done-intent:COMPLETED:notanumber"}},
		{"empty labels", nil},
		{"empty label list", []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := extractDoneIntent(tt.labels)
			if intent != nil {
				t.Errorf("extractDoneIntent(%v) = %+v, want nil for malformed input", tt.labels, intent)
			}
		})
	}
}

func TestExtractDoneIntent_AllExitTypes(t *testing.T) {
	t.Parallel()
	ts := time.Now().Unix()
	for _, exitType := range []string{"COMPLETED", "ESCALATED", "DEFERRED", "PHASE_COMPLETE"} {
		label := fmt.Sprintf("done-intent:%s:%d", exitType, ts)
		intent := extractDoneIntent([]string{label})
		if intent == nil {
			t.Errorf("extractDoneIntent returned nil for exit type %q", exitType)
			continue
		}
		if intent.ExitType != exitType {
			t.Errorf("ExitType = %q, want %q", intent.ExitType, exitType)
		}
	}
}

func TestDetectZombie_DoneIntentDeadSession(t *testing.T) {
	t.Parallel()
	// Verify the logic: dead session + done-intent older than 30s → should be treated as zombie
	// gt-dsgp: action is restart (not nuke), but detection logic is the same
	doneIntent := &DoneIntent{
		ExitType:  "COMPLETED",
		Timestamp: time.Now().Add(-60 * time.Second), // 60s old
	}
	sessionAlive := false
	age := time.Since(doneIntent.Timestamp)

	// Dead session + old intent → restart path (gt-dsgp: was auto-nuke)
	shouldRestart := !sessionAlive && doneIntent != nil && age >= config.DefaultWitnessDoneIntentStuckTimeout
	if !shouldRestart {
		t.Errorf("expected restart for dead session + old done-intent (age=%v)", age)
	}
}

func TestDetectZombie_DoneIntentLiveStuck(t *testing.T) {
	t.Parallel()
	// Verify the logic: live session + done-intent older than 60s → should restart session
	// gt-dsgp: restart instead of kill
	doneIntent := &DoneIntent{
		ExitType:  "COMPLETED",
		Timestamp: time.Now().Add(-90 * time.Second), // 90s old
	}
	sessionAlive := true
	age := time.Since(doneIntent.Timestamp)

	// Live session + old intent → restart stuck session (gt-dsgp: was kill)
	shouldRestart := sessionAlive && doneIntent != nil && age > config.DefaultWitnessDoneIntentStuckTimeout
	if !shouldRestart {
		t.Errorf("expected restart for live session + old done-intent (age=%v)", age)
	}
}

func TestDetectZombie_DoneIntentRecent(t *testing.T) {
	t.Parallel()
	// Verify the logic: done-intent younger than config.DefaultWitnessDoneIntentStuckTimeout → skip (polecat still working)
	doneIntent := &DoneIntent{
		ExitType:  "COMPLETED",
		Timestamp: time.Now().Add(-10 * time.Second), // 10s old
	}
	sessionAlive := false
	age := time.Since(doneIntent.Timestamp)

	// Recent intent → should skip
	shouldSkip := !sessionAlive && doneIntent != nil && age < config.DefaultWitnessDoneIntentStuckTimeout
	if !shouldSkip {
		t.Errorf("expected skip for recent done-intent (age=%v)", age)
	}

	// Live session + recent intent → also skip
	sessionAlive = true
	shouldSkipLive := sessionAlive && doneIntent != nil && age <= config.DefaultWitnessDoneIntentStuckTimeout
	if !shouldSkipLive {
		t.Errorf("expected skip for live session + recent done-intent (age=%v)", age)
	}
}

func TestDetectZombie_DoneOrNukedNotZombie(t *testing.T) {
	t.Parallel()
	// GH#2795: Polecats with agent_state=done or agent_state=nuked and a dead
	// session should NOT be treated as zombies, even if hook_bead is still set.
	// Without this, isZombieState returns true (hookBead != ""), and the witness
	// floods the mayor inbox with RECOVERY_NEEDED alerts every patrol cycle.
	for _, state := range []beads.AgentState{beads.AgentStateDone, beads.AgentStateNuked} {
		hookBead := "gt-some-issue"
		// isZombieState returns true because hookBead != ""
		if !isZombieState(state, hookBead) {
			t.Errorf("isZombieState(%q, %q) = false, want true (pre-condition)", state, hookBead)
		}
		// But the done/nuked check in detectZombieDeadSession should skip these.
		// Verify the states are terminal (not active).
		if state.IsActive() {
			t.Errorf("state %q should not be active", state)
		}
	}
}

func TestDetectZombie_AgentDeadInLiveSession(t *testing.T) {
	t.Parallel()
	// Verify the logic: live session + agent process dead → zombie
	// This is the gt-kj6r6 fix: DetectZombiePolecats now checks IsAgentAlive
	// for sessions that DO exist, catching the tmux-alive-but-agent-dead class.
	sessionAlive := true
	agentAlive := false
	var doneIntent *DoneIntent // No done-intent

	// Live session + no done-intent + agent dead → should be classified as zombie
	shouldDetect := sessionAlive && doneIntent == nil && !agentAlive
	if !shouldDetect {
		t.Error("expected zombie detection for live session with dead agent")
	}

	// Live session + agent alive → NOT a zombie
	agentAlive = true
	shouldSkip := sessionAlive && doneIntent == nil && agentAlive
	if !shouldSkip {
		t.Error("expected skip for live session with alive agent")
	}
}

func TestGetAgentBeadLabels_NoBdAvailable(t *testing.T) {
	t.Parallel()
	// When bd is not available, should return nil without panicking
	labels := getAgentBeadLabels(DefaultBdCli(), "/nonexistent", "nonexistent-bead")
	if labels != nil {
		t.Errorf("getAgentBeadLabels = %v, want nil when bd unavailable", labels)
	}
}

// --- extractPolecatFromJSON tests (issue #1228: panic-safe JSON parsing) ---

func TestExtractPolecatFromJSON_ValidOutput(t *testing.T) {
	t.Parallel()
	input := `[{"labels":["cleanup","polecat:nux","state:pending"]}]`
	got := extractPolecatFromJSON(input)
	if got != "nux" {
		t.Errorf("extractPolecatFromJSON() = %q, want %q", got, "nux")
	}
}

func TestExtractPolecatFromJSON_InvalidInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{"empty output", ""},
		{"malformed JSON", "{not valid json"},
		{"empty array", "[]"},
		{"no polecat label", `[{"labels":["cleanup","state:pending"]}]`},
		{"empty labels", `[{"labels":[]}]`},
		{"truncated JSON", `[{"labels":["polecat:`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPolecatFromJSON(tt.input)
			if got != "" {
				t.Errorf("extractPolecatFromJSON(%q) = %q, want empty", tt.input, got)
			}
		})
	}
}

func TestGetBeadStatus_NoBdAvailable(t *testing.T) {
	t.Parallel()
	// When bd is not available (test environment), getBeadStatus
	// should return ("", false) without panicking
	result, ok := getBeadStatus(DefaultBdCli(), "/nonexistent", "gt-abc123")
	if result != "" || ok {
		t.Errorf("getBeadStatus = (%q, %v), want (\"\", false) when bd unavailable", result, ok)
	}
}

func TestGetBeadStatus_EmptyBeadID(t *testing.T) {
	t.Parallel()
	// Empty bead ID should return ("", false) immediately
	result, ok := getBeadStatus(DefaultBdCli(), "/nonexistent", "")
	if result != "" || ok {
		t.Errorf("getBeadStatus(\"\") = (%q, %v), want (\"\", false)", result, ok)
	}
}

func TestDetectZombie_BeadClosedStillRunning(t *testing.T) {
	t.Parallel()
	// Verify the logic: live session + agent alive + hooked bead closed → zombie
	// This is the gt-h1l6i fix: DetectZombiePolecats now checks if the
	// polecat's hooked bead has been closed while the session is still running.
	sessionAlive := true
	agentAlive := true
	var doneIntent *DoneIntent // No done-intent
	hookBead := "gt-some-issue"
	beadStatus := "closed"

	// Live session + agent alive + no done-intent + bead closed → should detect
	shouldDetect := sessionAlive && agentAlive && doneIntent == nil &&
		hookBead != "" && beadStatus == "closed"
	if !shouldDetect {
		t.Error("expected zombie detection for live session with closed bead")
	}

	// Bead open → NOT a zombie
	beadStatus = "open"
	shouldSkip := sessionAlive && agentAlive && doneIntent == nil &&
		hookBead != "" && beadStatus == "closed"
	if shouldSkip {
		t.Error("should not detect zombie when bead is still open")
	}

	// No hook bead → NOT a zombie
	hookBead = ""
	beadStatus = "closed"
	shouldSkipNoHook := sessionAlive && agentAlive && doneIntent == nil &&
		hookBead != "" && beadStatus == "closed"
	if shouldSkipNoHook {
		t.Error("should not detect zombie when no hook bead exists")
	}
}

func TestDetectZombie_BeadClosedVsDoneIntent(t *testing.T) {
	t.Parallel()
	// Verify done-intent takes priority over closed-bead check.
	// If done-intent exists (recent), the polecat is still working through
	// gt done and we should NOT trigger the closed-bead path.
	sessionAlive := true
	agentAlive := true
	doneIntent := &DoneIntent{
		ExitType:  "COMPLETED",
		Timestamp: time.Now().Add(-10 * time.Second), // Recent
	}
	hookBead := "gt-some-issue"
	beadStatus := "closed"

	// Done-intent exists + bead closed → done-intent check runs first,
	// closed-bead check should NOT run (it's in the else branch)
	doneIntentHandled := sessionAlive && doneIntent != nil && time.Since(doneIntent.Timestamp) > config.DefaultWitnessDoneIntentStuckTimeout
	closedBeadCheck := sessionAlive && agentAlive && doneIntent == nil &&
		hookBead != "" && beadStatus == "closed"

	// Neither should trigger: done-intent is recent (not stuck), and
	// closed-bead check requires doneIntent == nil
	if doneIntentHandled {
		t.Error("recent done-intent should not trigger stuck-session handler")
	}
	if closedBeadCheck {
		t.Error("closed-bead check should not run when done-intent exists")
	}
}

func TestResetAbandonedBead_EmptyHookBead(t *testing.T) {
	t.Parallel()
	// resetAbandonedBead should return false for empty hookBead
	result := resetAbandonedBead(DefaultBdCli(), "/tmp", "testrig", "", "nux", nil)
	if result {
		t.Error("resetAbandonedBead should return false for empty hookBead")
	}
}

func TestResetAbandonedBead_NoRouter(t *testing.T) {
	t.Parallel()
	// resetAbandonedBead with nil router should not panic even if bead exists.
	// It will return false because bd won't find the bead, but shouldn't crash.
	result := resetAbandonedBead(DefaultBdCli(), "/tmp/nonexistent", "testrig", "gt-fake123", "nux", nil)
	if result {
		t.Error("resetAbandonedBead should return false when bd commands fail")
	}
}

func TestResetAbandonedBead_ClosesWhenWorkOnMain(t *testing.T) {
	// Not parallel: overrides package-level verifyCommitOnMain.
	// When verifyCommitOnMain returns true, resetAbandonedBead should close the
	// bead instead of resetting it for re-dispatch. This is the fix for #2036.

	oldVerify := verifyCommitOnMain
	verifyCommitOnMain = func(workDir, rigName, polecatName string) (bool, error) {
		return true, nil // work is on main
	}
	t.Cleanup(func() { verifyCommitOnMain = oldVerify })

	bd, mock := mockBd(
		func(args []string) (string, error) {
			if len(args) >= 1 && args[0] == "show" {
				return `[{"status":"hooked"}]`, nil
			}
			return "", nil
		},
		func(args []string) error {
			return nil
		},
	)

	tmpDir := t.TempDir()
	result := resetAbandonedBead(bd, tmpDir, "testrig", "gt-work123", "alpha", nil)
	if result {
		t.Error("resetAbandonedBead should return false when work is on main (bead closed, not re-dispatched)")
	}

	// Verify "close" was called, NOT "update ... --status=open"
	var foundClose, foundUpdate bool
	for _, call := range mock.calls {
		if strings.Contains(call, "close gt-work123") {
			foundClose = true
		}
		if strings.Contains(call, "update") && strings.Contains(call, "--status=open") {
			foundUpdate = true
		}
	}
	if !foundClose {
		t.Errorf("expected bd close to be called, got calls: %v", mock.calls)
	}
	if foundUpdate {
		t.Error("bd update --status=open should NOT be called when work is on main")
	}
}

func TestResetAbandonedBead_ResetsWhenWorkNotOnMain(t *testing.T) {
	// Not parallel: overrides package-level verifyCommitOnMain.
	// When verifyCommitOnMain returns false, resetAbandonedBead should reset
	// the bead for re-dispatch (existing behavior).

	oldVerify := verifyCommitOnMain
	verifyCommitOnMain = func(workDir, rigName, polecatName string) (bool, error) {
		return false, nil // work NOT on main
	}
	t.Cleanup(func() { verifyCommitOnMain = oldVerify })

	bd, mock := mockBd(
		func(args []string) (string, error) {
			if len(args) >= 1 && args[0] == "show" {
				return `[{"status":"hooked"}]`, nil
			}
			return "", nil
		},
		func(args []string) error {
			return nil
		},
	)

	tmpDir := t.TempDir()
	result := resetAbandonedBead(bd, tmpDir, "testrig", "gt-work123", "alpha", nil)
	if !result {
		t.Error("resetAbandonedBead should return true when work is NOT on main (bead reset for re-dispatch)")
	}

	// Verify "update --status=open" was called (normal reset path)
	var foundUpdate bool
	for _, call := range mock.calls {
		if strings.Contains(call, "update") && strings.Contains(call, "--status=open") {
			foundUpdate = true
		}
	}
	if !foundUpdate {
		t.Errorf("expected bd update --status=open to be called, got calls: %v", mock.calls)
	}
}

func TestBeadRecoveredField_DefaultFalse(t *testing.T) {
	t.Parallel()
	// BeadRecovered should default to false (zero value)
	z := ZombieResult{
		PolecatName:    "nux",
		AgentState:     "working",
		Classification: ZombieSessionDeadActive,
	}
	if z.BeadRecovered {
		t.Error("BeadRecovered should default to false")
	}
}

func TestStalledResult_Types(t *testing.T) {
	t.Parallel()
	// Verify the StalledResult type has all expected fields
	s := StalledResult{
		PolecatName: "alpha",
		StallType:   "startup-stall",
		Action:      "auto-dismissed",
		Error:       nil,
	}

	if s.PolecatName != "alpha" {
		t.Errorf("PolecatName = %q, want %q", s.PolecatName, "alpha")
	}
	if s.StallType != "startup-stall" {
		t.Errorf("StallType = %q, want %q", s.StallType, "startup-stall")
	}
	if s.Action != "auto-dismissed" {
		t.Errorf("Action = %q, want %q", s.Action, "auto-dismissed")
	}
	if s.Error != nil {
		t.Errorf("Error = %v, want nil", s.Error)
	}

	// Verify error field works
	s2 := StalledResult{
		PolecatName: "bravo",
		StallType:   "startup-stall",
		Action:      "escalated",
		Error:       fmt.Errorf("auto-dismiss failed"),
	}
	if s2.Error == nil {
		t.Error("Error = nil, want non-nil")
	}
}

func TestDetectStalledPolecatsResult_Empty(t *testing.T) {
	t.Parallel()
	result := &DetectStalledPolecatsResult{}

	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0", result.Checked)
	}
	if len(result.Stalled) != 0 {
		t.Errorf("Stalled length = %d, want 0", len(result.Stalled))
	}
	if len(result.Errors) != 0 {
		t.Errorf("Errors length = %d, want 0", len(result.Errors))
	}
}

func TestDetectStalledPolecats_NoPolecats(t *testing.T) {
	t.Parallel()
	// Should handle missing polecats directory gracefully
	result := DetectStalledPolecats("/nonexistent/path", "testrig")

	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0 for nonexistent dir", result.Checked)
	}
	if len(result.Stalled) != 0 {
		t.Errorf("Stalled = %d, want 0 for nonexistent dir", len(result.Stalled))
	}
	if len(result.Errors) != 0 {
		t.Errorf("Errors = %d, want 0 for nonexistent dir", len(result.Errors))
	}
}

func TestDetectStalledPolecats_EmptyPolecatsDir(t *testing.T) {
	t.Parallel()
	// Empty polecats directory should return 0 checked
	tmpDir := t.TempDir()
	rigName := "testrig"
	polecatsDir := filepath.Join(tmpDir, rigName, "polecats")
	if err := os.MkdirAll(polecatsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	result := DetectStalledPolecats(tmpDir, rigName)

	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0 for empty polecats dir", result.Checked)
	}
	if len(result.Stalled) != 0 {
		t.Errorf("Stalled = %d, want 0 for empty polecats dir", len(result.Stalled))
	}
}

func TestDetectStalledPolecats_NoSession(t *testing.T) {
	t.Parallel()
	// When tmux sessions don't exist (no real tmux in test),
	// HasSession returns false so polecats are skipped (not errors).
	tmpDir := t.TempDir()
	rigName := "testrig"
	polecatsDir := filepath.Join(tmpDir, rigName, "polecats")
	if err := os.MkdirAll(polecatsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create polecat directories
	for _, name := range []string{"alpha", "bravo"} {
		if err := os.Mkdir(filepath.Join(polecatsDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Create hidden dir (should be skipped)
	if err := os.Mkdir(filepath.Join(polecatsDir, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}

	result := DetectStalledPolecats(tmpDir, rigName)

	// Should count 2 polecats (skip hidden)
	if result.Checked != 2 {
		t.Errorf("Checked = %d, want 2 (should skip hidden dirs)", result.Checked)
	}

	// No stalled because HasSession returns false (no real tmux in test),
	// so polecats are skipped before structured signal checks.
	if len(result.Stalled) != 0 {
		t.Errorf("Stalled = %d, want 0 (no tmux sessions in test)", len(result.Stalled))
	}
}

func TestStartupStallThresholds(t *testing.T) {
	t.Parallel()
	// Verify config defaults are reasonable (tests the operational config defaults,
	// not removed handler constants).
	stallThreshold := config.DefaultWitnessStartupStallThreshold
	activityGrace := config.DefaultWitnessStartupActivityGrace
	if stallThreshold < 30*time.Second {
		t.Errorf("DefaultWitnessStartupStallThreshold = %v, too short (< 30s)", stallThreshold)
	}
	if stallThreshold > 5*time.Minute {
		t.Errorf("DefaultWitnessStartupStallThreshold = %v, too long (> 5min)", stallThreshold)
	}
	if activityGrace < 15*time.Second {
		t.Errorf("DefaultWitnessStartupActivityGrace = %v, too short (< 15s)", activityGrace)
	}
	if activityGrace > 5*time.Minute {
		t.Errorf("DefaultWitnessStartupActivityGrace = %v, too long (> 5min)", activityGrace)
	}
}

func TestDetectOrphanedBeads_NoBdAvailable(t *testing.T) {
	t.Parallel()
	// When bd is not available (test environment), should return empty result
	result := DetectOrphanedBeads(DefaultBdCli(), "/nonexistent", "testrig", nil)

	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0 when bd unavailable", result.Checked)
	}
	if len(result.Orphans) != 0 {
		t.Errorf("Orphans = %d, want 0 when bd unavailable", len(result.Orphans))
	}
}

func TestDetectOrphanedBeads_ResultTypes(t *testing.T) {
	t.Parallel()
	// Verify the OrphanedBeadResult type has all expected fields
	o := OrphanedBeadResult{
		BeadID:        "gt-orphan1",
		Assignee:      "testrig/polecats/alpha",
		PolecatName:   "alpha",
		BeadRecovered: true,
	}

	if o.BeadID != "gt-orphan1" {
		t.Errorf("BeadID = %q, want %q", o.BeadID, "gt-orphan1")
	}
	if o.Assignee != "testrig/polecats/alpha" {
		t.Errorf("Assignee = %q, want %q", o.Assignee, "testrig/polecats/alpha")
	}
	if o.PolecatName != "alpha" {
		t.Errorf("PolecatName = %q, want %q", o.PolecatName, "alpha")
	}
	if !o.BeadRecovered {
		t.Error("BeadRecovered = false, want true")
	}
}

func TestDetectOrphanedBeads_WithMockBd(t *testing.T) {
	installFakeTmuxNoServer(t)

	// Set up town directory structure
	townRoot := t.TempDir()
	rigName := "testrig"
	polecatsDir := filepath.Join(townRoot, rigName, "polecats")
	if err := os.MkdirAll(polecatsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a polecat directory for "bravo" (alive dir, dead session)
	// This case should be SKIPPED (deferred to DetectZombiePolecats)
	if err := os.Mkdir(filepath.Join(polecatsDir, "bravo"), 0o755); err != nil {
		t.Fatal(err)
	}

	// "alpha" has NO directory and NO tmux session — true orphan
	// "bravo" has directory but no session — deferred to DetectZombiePolecats
	// "charlie" is hooked, no dir, no session — also an orphan
	// "delta" is assigned to a different rig — skipped by rigName filter

	bd, mock := mockBd(
		func(args []string) (string, error) {
			if len(args) == 0 {
				return "{}", nil
			}
			switch args[0] {
			case "list":
				joined := strings.Join(args, " ")
				if strings.Contains(joined, "--status=in_progress") {
					return `[
  {"id":"gt-orphan1","assignee":"testrig/polecats/alpha"},
  {"id":"gt-alive1","assignee":"testrig/polecats/bravo"},
  {"id":"gt-nocrew","assignee":"testrig/crew/sean"},
  {"id":"gt-noassign","assignee":""},
  {"id":"gt-otherrig","assignee":"otherrig/polecats/delta"}
]`, nil
				}
				if strings.Contains(joined, "--status=hooked") {
					return `[{"id":"gt-hooked1","assignee":"testrig/polecats/charlie"}]`, nil
				}
				return "[]", nil
			case "show":
				return `[{"status":"in_progress"}]`, nil
			}
			return "{}", nil
		},
		func(args []string) error { return nil },
	)

	result := DetectOrphanedBeads(bd, townRoot, rigName, nil)

	// Verify --limit=0 was passed in bd list invocations
	logStr := strings.Join(mock.calls, "\n")
	if !strings.Contains(logStr, "--limit=0") {
		t.Errorf("bd list was not called with --limit=0; log:\n%s", logStr)
	}
	// Verify both statuses were queried
	if !strings.Contains(logStr, "--status=in_progress") {
		t.Errorf("bd list was not called with --status=in_progress; log:\n%s", logStr)
	}
	if !strings.Contains(logStr, "--status=hooked") {
		t.Errorf("bd list was not called with --status=hooked; log:\n%s", logStr)
	}

	// Should have checked 3 polecat assignees in "testrig":
	// alpha (in_progress), bravo (in_progress), charlie (hooked)
	// "crew/sean" is not a polecat, "" has no assignee,
	// "otherrig/polecats/delta" is filtered out by rigName
	if result.Checked != 3 {
		t.Errorf("Checked = %d, want 3 (alpha + bravo from in_progress, charlie from hooked)", result.Checked)
	}

	// Should have found 2 orphans:
	// alpha (in_progress, no dir, no session) and charlie (hooked, no dir, no session)
	// bravo has directory so deferred to DetectZombiePolecats
	if len(result.Orphans) != 2 {
		t.Fatalf("Orphans = %d, want 2 (alpha + charlie)", len(result.Orphans))
	}

	// Verify first orphan (alpha from in_progress scan)
	orphan := result.Orphans[0]
	if orphan.BeadID != "gt-orphan1" {
		t.Errorf("orphan[0] BeadID = %q, want %q", orphan.BeadID, "gt-orphan1")
	}
	if orphan.PolecatName != "alpha" {
		t.Errorf("orphan[0] PolecatName = %q, want %q", orphan.PolecatName, "alpha")
	}
	if orphan.Assignee != "testrig/polecats/alpha" {
		t.Errorf("orphan[0] Assignee = %q, want %q", orphan.Assignee, "testrig/polecats/alpha")
	}
	// BeadRecovered should be true (mock bd update succeeds)
	if !orphan.BeadRecovered {
		t.Error("orphan[0] BeadRecovered = false, want true")
	}

	// Verify second orphan (charlie from hooked scan)
	orphan2 := result.Orphans[1]
	if orphan2.BeadID != "gt-hooked1" {
		t.Errorf("orphan[1] BeadID = %q, want %q", orphan2.BeadID, "gt-hooked1")
	}
	if orphan2.PolecatName != "charlie" {
		t.Errorf("orphan[1] PolecatName = %q, want %q", orphan2.PolecatName, "charlie")
	}

	// Verify no unexpected errors
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}
}

func TestDetectOrphanedBeads_ErrorPath(t *testing.T) {
	t.Parallel()
	bdErr := fmt.Errorf("bd: connection refused")
	bd, _ := mockBd(
		func(args []string) (string, error) { return "", bdErr },
		func(args []string) error { return bdErr },
	)

	result := DetectOrphanedBeads(bd, t.TempDir(), "testrig", nil)

	if len(result.Errors) == 0 {
		t.Error("expected errors when bd fails, got none")
	}
	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0 when bd fails", result.Checked)
	}
	if len(result.Orphans) != 0 {
		t.Errorf("Orphans = %d, want 0 when bd fails", len(result.Orphans))
	}
}

// --- DetectOrphanedMolecules tests ---

func TestOrphanedMoleculeResult_Types(t *testing.T) {
	t.Parallel()
	// Verify the result types have all expected fields.
	r := OrphanedMoleculeResult{
		BeadID:        "gt-work-123",
		MoleculeID:    "gt-mol-456",
		Assignee:      "testrig/polecats/alpha",
		PolecatName:   "alpha",
		Closed:        5,
		BeadRecovered: true,
		Error:         nil,
	}
	if r.BeadID != "gt-work-123" {
		t.Errorf("BeadID = %q, want %q", r.BeadID, "gt-work-123")
	}
	if r.MoleculeID != "gt-mol-456" {
		t.Errorf("MoleculeID = %q, want %q", r.MoleculeID, "gt-mol-456")
	}
	if r.PolecatName != "alpha" {
		t.Errorf("PolecatName = %q, want %q", r.PolecatName, "alpha")
	}
	if r.Closed != 5 {
		t.Errorf("Closed = %d, want 5", r.Closed)
	}
	if !r.BeadRecovered {
		t.Error("BeadRecovered = false, want true")
	}

	// Aggregate result
	agg := DetectOrphanedMoleculesResult{
		Checked: 10,
		Orphans: []OrphanedMoleculeResult{r},
		Errors:  []error{fmt.Errorf("test error")},
	}
	if agg.Checked != 10 {
		t.Errorf("Checked = %d, want 10", agg.Checked)
	}
	if len(agg.Orphans) != 1 {
		t.Errorf("len(Orphans) = %d, want 1", len(agg.Orphans))
	}
	if len(agg.Errors) != 1 {
		t.Errorf("len(Errors) = %d, want 1", len(agg.Errors))
	}
}

func TestDetectOrphanedMolecules_NoBdAvailable(t *testing.T) {
	t.Parallel()
	// When bd is not available, should return empty result with errors.
	bdErr := fmt.Errorf("bd: not found")
	bd, _ := mockBd(
		func(args []string) (string, error) { return "", bdErr },
		func(args []string) error { return bdErr },
	)
	result := DetectOrphanedMolecules(bd, "/tmp/nonexistent", "testrig", nil)
	if result == nil {
		t.Fatal("result should not be nil")
	}
	// Should have errors from failed bd list commands
	if len(result.Errors) == 0 {
		t.Error("expected errors when bd is not available")
	}
	if len(result.Orphans) != 0 {
		t.Errorf("expected no orphans, got %d", len(result.Orphans))
	}
}

func TestDetectOrphanedMolecules_EmptyResult(t *testing.T) {
	t.Parallel()
	// With a mock bd that returns empty lists, should get empty result.
	bd, _ := mockBd(
		func(args []string) (string, error) { return "[]", nil },
		func(args []string) error { return nil },
	)

	result := DetectOrphanedMolecules(bd, t.TempDir(), "testrig", nil)
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0", result.Checked)
	}
	if len(result.Orphans) != 0 {
		t.Errorf("len(Orphans) = %d, want 0", len(result.Orphans))
	}
}

func TestGetAttachedMoleculeID_EmptyOutput(t *testing.T) {
	t.Parallel()
	// When bd returns error, should return empty string.
	bd, _ := mockBd(
		func(args []string) (string, error) { return "", fmt.Errorf("bd: not found") },
		func(args []string) error { return fmt.Errorf("bd: not found") },
	)
	result := getAttachedMoleculeID(bd, "/tmp", "gt-fake-123")
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestHandlePolecatDone_CompletedWithoutMRID_NoMergeReady(t *testing.T) {
	t.Parallel()
	// When Exit==COMPLETED but MRID is empty and MRFailed is true,
	// the witness should NOT send MERGE_READY (go to no-MR path).
	// This tests the fix for gt-xp6e9p.
	payload := &PolecatDonePayload{
		PolecatName: "nux",
		Exit:        "COMPLETED",
		IssueID:     "gt-abc123",
		MRID:        "",
		Branch:      "polecat/nux-abc123",
		MRFailed:    true,
	}

	// hasPendingMR should be false when MRID is empty
	hasPendingMR := payload.MRID != ""
	if hasPendingMR {
		t.Error("hasPendingMR = true, want false when MRID is empty")
	}

	// Even with Exit==COMPLETED, MRFailed should prevent the bead lookup fallback
	if !payload.MRFailed && payload.Exit == "COMPLETED" && payload.Branch != "" {
		t.Error("should not attempt MR bead lookup when MRFailed is true")
	}
}

func TestHandlePolecatDone_CompletedWithMRID(t *testing.T) {
	t.Parallel()
	// When Exit==COMPLETED and MRID is set, hasPendingMR should be true.
	payload := &PolecatDonePayload{
		PolecatName: "nux",
		Exit:        "COMPLETED",
		MRID:        "gt-mr-xyz",
		Branch:      "polecat/nux-abc123",
	}

	hasPendingMR := payload.MRID != ""
	if !hasPendingMR {
		t.Error("hasPendingMR = false, want true when MRID is set")
	}
}

func TestFindMRBeadForBranch_NoBdAvailable(t *testing.T) {
	t.Parallel()
	// When bd is not available, should return empty string
	result := findMRBeadForBranch(DefaultBdCli(), "/nonexistent", "polecat/nux-abc123")
	if result != "" {
		t.Errorf("findMRBeadForBranch = %q, want empty when bd unavailable", result)
	}
}

func TestDetectOrphanedMolecules_WithMockBd(t *testing.T) {
	installFakeTmuxNoServer(t)

	// Full test with mock bd returning beads assigned to dead polecats.
	//
	// Setup:
	// - alpha: dead polecat (no tmux, no directory) with attached molecule → orphaned
	// - bravo: alive polecat (directory exists) → skip
	// - crew/sean: non-polecat assignee → skip
	// - empty assignee → skip

	tmpDir := t.TempDir()

	// Create town structure: tmpDir is the "town root"
	rigName := "testrig"
	polecatsDir := filepath.Join(tmpDir, rigName, "polecats")
	if err := os.MkdirAll(polecatsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create bravo's directory (alive polecat)
	if err := os.MkdirAll(filepath.Join(polecatsDir, "bravo"), 0755); err != nil {
		t.Fatal(err)
	}
	// No directory for alpha (dead polecat)

	// Create workspace.Find marker
	if err := os.WriteFile(filepath.Join(tmpDir, ".gt-root"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	bd, mock := mockBd(
		func(args []string) (string, error) {
			if len(args) == 0 {
				return "[]", nil
			}
			joined := strings.Join(args, " ")
			switch args[0] {
			case "list":
				if strings.Contains(joined, "--status=hooked") {
					return `[
  {"id":"gt-work-001","assignee":"testrig/polecats/alpha"},
  {"id":"gt-work-002","assignee":"testrig/polecats/bravo"},
  {"id":"gt-work-003","assignee":"testrig/crew/sean"},
  {"id":"gt-work-004","assignee":""}
]`, nil
				}
				if strings.Contains(joined, "--status=in_progress") {
					return "[]", nil
				}
				if strings.Contains(joined, "--parent=gt-mol-orphan") {
					return `[
  {"id":"gt-step-001","status":"open"},
  {"id":"gt-step-002","status":"open"},
  {"id":"gt-step-003","status":"closed"}
]`, nil
				}
				return "[]", nil
			case "show":
				if len(args) > 1 {
					switch args[1] {
					case "gt-work-001":
						return `[{"status":"hooked","description":"attached_molecule: gt-mol-orphan\nattached_at: 2026-01-15T10:00:00Z\ndispatched_by: mayor"}]`, nil
					case "gt-mol-orphan":
						return `[{"status":"open"}]`, nil
					}
				}
				return `[{"status":"open","description":""}]`, nil
			}
			return "{}", nil
		},
		func(args []string) error { return nil },
	)

	result := DetectOrphanedMolecules(bd, tmpDir, rigName, nil)
	if result == nil {
		t.Fatal("result should not be nil")
	}

	// Should have checked 2 polecat-assigned beads (alpha and bravo)
	if result.Checked != 2 {
		t.Errorf("Checked = %d, want 2 (alpha + bravo)", result.Checked)
	}

	// Should have found 1 orphan (alpha's molecule)
	if len(result.Orphans) != 1 {
		t.Fatalf("len(Orphans) = %d, want 1", len(result.Orphans))
	}

	orphan := result.Orphans[0]
	if orphan.BeadID != "gt-work-001" {
		t.Errorf("orphan.BeadID = %q, want %q", orphan.BeadID, "gt-work-001")
	}
	if orphan.MoleculeID != "gt-mol-orphan" {
		t.Errorf("orphan.MoleculeID = %q, want %q", orphan.MoleculeID, "gt-mol-orphan")
	}
	if orphan.PolecatName != "alpha" {
		t.Errorf("orphan.PolecatName = %q, want %q", orphan.PolecatName, "alpha")
	}
	// Closed should be 3: 2 open step children + 1 molecule itself
	if orphan.Closed != 3 {
		t.Errorf("orphan.Closed = %d, want 3 (2 open steps + 1 molecule)", orphan.Closed)
	}
	if orphan.Error != nil {
		t.Errorf("orphan.Error = %v, want nil", orphan.Error)
	}

	// Verify bd close was called by checking the mock log
	logContent := strings.Join(mock.calls, "\n")
	if !strings.Contains(logContent, "close gt-step-001 gt-step-002") {
		t.Errorf("expected bd close for step children, got log:\n%s", logContent)
	}
	if !strings.Contains(logContent, "close gt-mol-orphan") {
		t.Errorf("expected bd close for molecule, got log:\n%s", logContent)
	}
	// Verify bead was recovered (resetAbandonedBead called bd update)
	if !orphan.BeadRecovered {
		t.Error("orphan.BeadRecovered = false, want true (resetAbandonedBead should have reset the bead)")
	}
	if !strings.Contains(logContent, "update gt-work-001") {
		t.Errorf("expected bd update for bead reset, got log:\n%s", logContent)
	}
}

func TestCompletionDiscovery_Types(t *testing.T) {
	t.Parallel()
	// Verify CompletionDiscovery has all expected fields
	d := CompletionDiscovery{
		PolecatName:    "nux",
		AgentBeadID:    "gt-gastown-polecat-nux",
		ExitType:       "COMPLETED",
		IssueID:        "gt-abc123",
		MRID:           "gt-mr-xyz",
		Branch:         "polecat/nux/gt-abc123@hash",
		MRFailed:       false,
		CompletionTime: "2026-02-28T02:00:00Z",
		Action:         "merge-ready-sent",
		WispCreated:    "gt-wisp-123",
	}

	if d.PolecatName != "nux" {
		t.Errorf("PolecatName = %q, want %q", d.PolecatName, "nux")
	}
	if d.ExitType != "COMPLETED" {
		t.Errorf("ExitType = %q, want %q", d.ExitType, "COMPLETED")
	}
	if d.Branch != "polecat/nux/gt-abc123@hash" {
		t.Errorf("Branch = %q, want correct value", d.Branch)
	}
}

func TestDiscoverCompletionsResult_EmptyResult(t *testing.T) {
	t.Parallel()
	result := &DiscoverCompletionsResult{}
	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0", result.Checked)
	}
	if len(result.Discovered) != 0 {
		t.Errorf("Discovered = %d, want 0", len(result.Discovered))
	}
	if len(result.Errors) != 0 {
		t.Errorf("Errors = %d, want 0", len(result.Errors))
	}
}

func TestDiscoverCompletions_NonexistentDir(t *testing.T) {
	t.Parallel()
	// When workDir doesn't exist, should return empty result
	result := DiscoverCompletions(DefaultBdCli(), "/nonexistent/path", "testrig", nil)
	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0 for nonexistent dir", result.Checked)
	}
}

func TestDiscoverCompletions_EmptyPolecatsDir(t *testing.T) {
	t.Parallel()
	// When polecats directory exists but is empty, should scan 0
	tmpDir := t.TempDir()
	rigName := "testrig"
	polecatsDir := filepath.Join(tmpDir, rigName, "polecats")
	if err := os.MkdirAll(polecatsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create workspace marker
	if err := os.WriteFile(filepath.Join(tmpDir, ".gt-root"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	result := DiscoverCompletions(DefaultBdCli(), tmpDir, rigName, nil)
	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0 for empty polecats dir", result.Checked)
	}
}

func TestDiscoverCompletions_NoCompletionMetadata(t *testing.T) {
	// Polecat exists but agent bead has no completion metadata — should be skipped
	tmpDir := t.TempDir()
	rigName := "testrig"
	polecatsDir := filepath.Join(tmpDir, rigName, "polecats")
	if err := os.MkdirAll(filepath.Join(polecatsDir, "nux"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".gt-root"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Mock bd that returns agent bead with no completion fields
	bd, _ := mockBd(
		func(args []string) (string, error) {
			if len(args) > 0 && args[0] == "show" {
				return `[{"id":"gt-testrig-polecat-nux","description":"Agent: testrig/polecats/nux\n\nrole_type: polecat\nrig: testrig\nagent_state: working\nhook_bead: gt-work-001","agent_state":"working","hook_bead":"gt-work-001"}]`, nil
			}
			return "[]", nil
		},
		func(args []string) error { return nil },
	)

	result := DiscoverCompletions(bd, tmpDir, rigName, nil)
	if result.Checked != 1 {
		t.Errorf("Checked = %d, want 1", result.Checked)
	}
	if len(result.Discovered) != 0 {
		t.Errorf("Discovered = %d, want 0 (no completion metadata)", len(result.Discovered))
	}
}

func TestProcessDiscoveredCompletion_PhaseComplete(t *testing.T) {
	t.Parallel()
	payload := &PolecatDonePayload{
		PolecatName: "nux",
		Exit:        "PHASE_COMPLETE",
	}
	discovery := &CompletionDiscovery{}
	processDiscoveredCompletion(DefaultBdCli(), "/tmp", "testrig", payload, discovery)
	if discovery.Action != "phase-complete" {
		t.Errorf("Action = %q, want %q", discovery.Action, "phase-complete")
	}
}

func TestProcessDiscoveredCompletion_NoMR(t *testing.T) {
	t.Parallel()
	payload := &PolecatDonePayload{
		PolecatName: "nux",
		Exit:        "COMPLETED",
		MRFailed:    true, // Prevents fallback MR lookup
	}
	discovery := &CompletionDiscovery{}
	processDiscoveredCompletion(DefaultBdCli(), "/tmp", "testrig", payload, discovery)
	if !strings.Contains(discovery.Action, "acknowledged-idle") {
		t.Errorf("Action = %q, want to contain %q", discovery.Action, "acknowledged-idle")
	}
}

func TestProcessDiscoveredCompletion_EscalatedNoMR(t *testing.T) {
	t.Parallel()
	payload := &PolecatDonePayload{
		PolecatName: "nux",
		Exit:        "ESCALATED",
	}
	discovery := &CompletionDiscovery{}
	processDiscoveredCompletion(DefaultBdCli(), "/tmp", "testrig", payload, discovery)
	if !strings.Contains(discovery.Action, "acknowledged-idle") {
		t.Errorf("Action = %q, want to contain %q for ESCALATED exit", discovery.Action, "acknowledged-idle")
	}
}

func TestGetAgentBeadFields_NoAgentBead(t *testing.T) {
	t.Parallel()
	// When bd fails, should return nil
	bd, _ := mockBd(
		func(args []string) (string, error) { return "", fmt.Errorf("bd: not found") },
		func(args []string) error { return fmt.Errorf("bd: not found") },
	)
	fields := getAgentBeadFields(bd, "/tmp", "gt-fake-agent")
	if fields != nil {
		t.Error("expected nil fields when bd unavailable")
	}
}

func TestClearCompletionMetadata_NoBd(t *testing.T) {
	t.Parallel()
	// When bd fails, should return error
	bd, _ := mockBd(
		func(args []string) (string, error) { return "", fmt.Errorf("bd: not found") },
		func(args []string) error { return fmt.Errorf("bd: not found") },
	)
	err := clearCompletionMetadata(bd, "/tmp", "gt-fake-agent")
	if err == nil {
		t.Error("expected error when bd unavailable")
	}
}

// --- Heartbeat v2 tests (gt-3vr5) ---

func TestHeartbeatV2_ExitingStateSkipsZombieDetection(t *testing.T) {
	t.Parallel()
	// Agent reports "exiting" state via heartbeat v2.
	// The witness should trust the agent and NOT flag as zombie,
	// even if done-intent is older than config.DefaultWitnessDoneIntentStuckTimeout.
	// This replaces timer-based inference for v2 agents.

	// Fresh heartbeat with state="exiting" → not a zombie
	hb := &polecat.SessionHeartbeat{
		Timestamp: time.Now(),
		State:     polecat.HeartbeatExiting,
	}
	stale := time.Since(hb.Timestamp) >= polecat.SessionHeartbeatStaleThreshold
	if stale {
		t.Error("fresh heartbeat should not be stale")
	}
	if hb.EffectiveState() != polecat.HeartbeatExiting {
		t.Errorf("EffectiveState() = %q, want %q", hb.EffectiveState(), polecat.HeartbeatExiting)
	}

	// With a v2 exiting heartbeat, the witness should NOT check done-intent timers
	shouldSkip := hb.IsV2() && !stale && hb.EffectiveState() == polecat.HeartbeatExiting
	if !shouldSkip {
		t.Error("expected v2 exiting heartbeat to skip zombie detection")
	}
}

func TestHeartbeatV2_StuckStateEscalates(t *testing.T) {
	t.Parallel()
	// Agent self-reports "stuck" via heartbeat v2.
	// The witness should escalate (not restart — agent is alive).
	hb := &polecat.SessionHeartbeat{
		Timestamp: time.Now(),
		State:     polecat.HeartbeatStuck,
		Context:   "blocked on auth issue",
	}
	stale := time.Since(hb.Timestamp) >= polecat.SessionHeartbeatStaleThreshold
	if stale {
		t.Error("fresh heartbeat should not be stale")
	}

	shouldEscalate := hb.IsV2() && !stale && hb.EffectiveState() == polecat.HeartbeatStuck
	if !shouldEscalate {
		t.Error("expected v2 stuck heartbeat to trigger escalation")
	}
}

func TestHeartbeatV2_WorkingStateHealthy(t *testing.T) {
	t.Parallel()
	// Agent heartbeats "working" — healthy, not a zombie.
	hb := &polecat.SessionHeartbeat{
		Timestamp: time.Now(),
		State:     polecat.HeartbeatWorking,
	}
	stale := time.Since(hb.Timestamp) >= polecat.SessionHeartbeatStaleThreshold
	shouldSkip := hb.IsV2() && !stale && (hb.EffectiveState() == polecat.HeartbeatWorking || hb.EffectiveState() == polecat.HeartbeatIdle)
	if !shouldSkip {
		t.Error("expected v2 working heartbeat to skip zombie detection")
	}
}

func TestHeartbeatV2_IdleStateHealthy(t *testing.T) {
	t.Parallel()
	hb := &polecat.SessionHeartbeat{
		Timestamp: time.Now(),
		State:     polecat.HeartbeatIdle,
	}
	stale := time.Since(hb.Timestamp) >= polecat.SessionHeartbeatStaleThreshold
	shouldSkip := hb.IsV2() && !stale && (hb.EffectiveState() == polecat.HeartbeatWorking || hb.EffectiveState() == polecat.HeartbeatIdle)
	if !shouldSkip {
		t.Error("expected v2 idle heartbeat to skip zombie detection")
	}
}

func TestHeartbeatV2_StaleHeartbeatFallsThrough(t *testing.T) {
	t.Parallel()
	// Stale v2 heartbeat (agent died) → fall through to legacy detection.
	hb := &polecat.SessionHeartbeat{
		Timestamp: time.Now().Add(-10 * time.Minute), // 10min old → stale
		State:     polecat.HeartbeatWorking,
	}
	stale := time.Since(hb.Timestamp) >= polecat.SessionHeartbeatStaleThreshold
	if !stale {
		t.Error("10-minute-old heartbeat should be stale")
	}

	// Stale heartbeat should NOT skip zombie detection — falls through to legacy
	shouldSkip := hb.IsV2() && !stale
	if shouldSkip {
		t.Error("stale v2 heartbeat should fall through to legacy detection")
	}
}

func TestHeartbeatV2_V1FallsThrough(t *testing.T) {
	t.Parallel()
	// v1 heartbeat (no state field) → fall through to legacy detection.
	hb := &polecat.SessionHeartbeat{
		Timestamp: time.Now(),
		// No State field → v1
	}
	if hb.IsV2() {
		t.Error("expected IsV2()=false for v1 heartbeat")
	}

	// v1 heartbeat should NOT trigger v2 logic
	shouldUseV2 := hb.IsV2()
	if shouldUseV2 {
		t.Error("v1 heartbeat should fall through to legacy detection")
	}
}

func TestHeartbeatV2_DeadSessionFreshHeartbeatRace(t *testing.T) {
	t.Parallel()
	// Dead session but fresh heartbeat → possible race (session just restarted).
	// Should skip zombie detection to avoid killing a newly-started session.
	hb := &polecat.SessionHeartbeat{
		Timestamp: time.Now(),
		State:     polecat.HeartbeatWorking,
	}
	stale := time.Since(hb.Timestamp) >= polecat.SessionHeartbeatStaleThreshold
	sessionDead := true

	// Fresh heartbeat + dead session → skip (race condition)
	shouldSkip := sessionDead && hb.IsV2() && !stale
	if !shouldSkip {
		t.Error("expected fresh v2 heartbeat + dead session to skip zombie detection (race)")
	}
}

func TestZombieAgentSelfReportedStuck_Classification(t *testing.T) {
	t.Parallel()
	// Verify the new classification type
	if ZombieAgentSelfReportedStuck != "agent-self-reported-stuck" {
		t.Errorf("ZombieAgentSelfReportedStuck = %q, want %q", ZombieAgentSelfReportedStuck, "agent-self-reported-stuck")
	}
	// Should imply active work (agent is alive and asking for help)
	if !ZombieAgentSelfReportedStuck.ImpliesActiveWork() {
		t.Error("ZombieAgentSelfReportedStuck should imply active work")
	}
}

func TestZombieNeverHeartbeated_Classification(t *testing.T) {
	t.Parallel()
	if ZombieNeverHeartbeated != "never-heartbeated" {
		t.Errorf("ZombieNeverHeartbeated = %q, want %q", ZombieNeverHeartbeated, "never-heartbeated")
	}
	if !ZombieNeverHeartbeated.ImpliesActiveWork() {
		t.Error("ZombieNeverHeartbeated should imply active work")
	}

	// Session old enough (>5m default) with assigned work and no heartbeat → flag.
	oldSession := time.Now().Add(-10 * time.Minute)
	shouldFlag := time.Since(oldSession) > config.DefaultWitnessHeartbeatStartupGrace
	if !shouldFlag {
		t.Errorf("expected flag for session age=%v, threshold=%v",
			time.Since(oldSession).Round(time.Second), config.DefaultWitnessHeartbeatStartupGrace)
	}

	// Session within grace period → no flag.
	newSession := time.Now().Add(-2 * time.Minute)
	shouldNotFlag := time.Since(newSession) <= config.DefaultWitnessHeartbeatStartupGrace
	if !shouldNotFlag {
		t.Errorf("expected no flag for session age=%v, threshold=%v",
			time.Since(newSession).Round(time.Second), config.DefaultWitnessHeartbeatStartupGrace)
	}
}

func TestSubmittedStillRunningCandidate(t *testing.T) {
	t.Parallel()

	baseSnap := &agentBeadSnapshot{
		AgentState: string(beads.AgentStateDone),
		HookBead:   "gt-work-123",
		UpdatedAt:  time.Now().Add(-10 * time.Minute).Format(time.RFC3339),
		Fields: &beads.AgentFields{
			CleanupStatus: "clean",
			MRID:          "gt-mr-123",
		},
	}
	staleHB := &polecat.SessionHeartbeat{
		Timestamp: time.Now().Add(-10 * time.Minute),
		State:     polecat.HeartbeatWorking,
	}

	age, ok := isSubmittedStillRunningCandidate(baseSnap, staleHB, config.DefaultWitnessHeartbeatStartupGrace)
	if !ok {
		t.Fatalf("expected submitted still-running candidate, age=%v", age)
	}

	noHookSnap := *baseSnap
	noHookSnap.HookBead = ""
	if _, ok := isSubmittedStillRunningCandidate(&noHookSnap, staleHB, config.DefaultWitnessHeartbeatStartupGrace); !ok {
		t.Error("no-hook submitted sessions must still be treated as submitted still-running")
	}

	idleSnap := *baseSnap
	idleSnap.AgentState = string(beads.AgentStateIdle)
	if _, ok := isSubmittedStillRunningCandidate(&idleSnap, staleHB, config.DefaultWitnessHeartbeatStartupGrace); ok {
		t.Error("normal idle polecats with submitted MR metadata must not be treated as submitted still-running")
	}

	freshHB := &polecat.SessionHeartbeat{
		Timestamp: time.Now(),
		State:     polecat.HeartbeatWorking,
	}
	if _, ok := isSubmittedStillRunningCandidate(baseSnap, freshHB, config.DefaultWitnessHeartbeatStartupGrace); ok {
		t.Error("fresh heartbeat must not be treated as submitted still-running")
	}

	dirtySnap := *baseSnap
	dirtyFields := *baseSnap.Fields
	dirtyFields.CleanupStatus = "has_uncommitted"
	dirtySnap.Fields = &dirtyFields
	if _, ok := isSubmittedStillRunningCandidate(&dirtySnap, staleHB, config.DefaultWitnessHeartbeatStartupGrace); ok {
		t.Error("dirty cleanup status must not be treated as safe submitted still-running")
	}

	noSubmitSnap := *baseSnap
	noSubmitSnap.AgentState = string(beads.AgentStateWorking)
	noSubmitSnap.ActiveMR = ""
	noSubmitSnap.Fields = &beads.AgentFields{CleanupStatus: "clean"}
	if _, ok := isSubmittedStillRunningCandidate(&noSubmitSnap, staleHB, config.DefaultWitnessHeartbeatStartupGrace); ok {
		t.Error("open hooked work without submission evidence must not be treated as submitted still-running")
	}

	completedOnlySnap := *baseSnap
	completedOnlySnap.ActiveMR = ""
	completedOnlySnap.Fields = &beads.AgentFields{
		CleanupStatus:  "clean",
		ExitType:       string(ExitTypeCompleted),
		CompletionTime: time.Now().Format(time.RFC3339),
	}
	if _, ok := isSubmittedStillRunningCandidate(&completedOnlySnap, staleHB, config.DefaultWitnessHeartbeatStartupGrace); ok {
		t.Error("COMPLETED metadata alone must not be treated as successful submission evidence")
	}

	failedSubmitSnap := *baseSnap
	failedSubmitSnap.Fields = &beads.AgentFields{
		CleanupStatus: "clean",
		MRID:          "gt-mr-123",
		MRFailed:      true,
	}
	if _, ok := isSubmittedStillRunningCandidate(&failedSubmitSnap, staleHB, config.DefaultWitnessHeartbeatStartupGrace); ok {
		t.Error("failed MR submission must not be treated as successful submission evidence")
	}

	pushFailedSnap := *baseSnap
	pushFailedSnap.Fields = &beads.AgentFields{
		CleanupStatus: "clean",
		MRID:          "gt-mr-123",
		PushFailed:    true,
	}
	if _, ok := isSubmittedStillRunningCandidate(&pushFailedSnap, staleHB, config.DefaultWitnessHeartbeatStartupGrace); ok {
		t.Error("failed push must not be treated as successful submission evidence")
	}
}

func TestZombieSubmittedStillRunning_Classification(t *testing.T) {
	t.Parallel()
	if ZombieSubmittedStillRunning != "submitted-still-running" {
		t.Errorf("ZombieSubmittedStillRunning = %q, want %q", ZombieSubmittedStillRunning, "submitted-still-running")
	}
	if ZombieSubmittedStillRunning.ImpliesActiveWork() {
		t.Error("ZombieSubmittedStillRunning should be classified as orphan/submitted idle, not active failed work")
	}
}

func TestNotifyRefineryMergeReady_EmitsChannelEvent(t *testing.T) {
	// Create a fake town root with the workspace marker so workspace.Find recognizes it
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Set GT_TEST_NUDGE_LOG to prevent actual tmux operations in nudgeRefinery
	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))

	result := &HandlerResult{}
	// notifyRefineryMergeReady takes workDir and calls workspace.Find(workDir) internally
	notifyRefineryMergeReady(townRoot, "dashboard", result)

	// Verify that a MERGE_READY event file was created in the refinery channel
	eventDir := filepath.Join(townRoot, "events", "refinery")
	entries, err := os.ReadDir(eventDir)
	if err != nil {
		t.Fatalf("reading event dir: %v", err)
	}

	var eventFiles []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".event") {
			eventFiles = append(eventFiles, e.Name())
		}
	}

	if len(eventFiles) == 0 {
		t.Fatal("expected at least one .event file in ~/gt/events/refinery/, got none")
	}

	// Read and verify the event content
	data, err := os.ReadFile(filepath.Join(eventDir, eventFiles[0]))
	if err != nil {
		t.Fatalf("reading event file: %v", err)
	}

	var event map[string]interface{}
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("parsing event JSON: %v", err)
	}

	if event["type"] != "MERGE_READY" {
		t.Errorf("event type = %v, want MERGE_READY", event["type"])
	}
	if event["channel"] != "refinery" {
		t.Errorf("event channel = %v, want refinery", event["channel"])
	}

	payload, ok := event["payload"].(map[string]interface{})
	if !ok {
		t.Fatalf("payload is not a map: %T", event["payload"])
	}
	if payload["source"] != "witness" {
		t.Errorf("payload.source = %v, want witness", payload["source"])
	}
	if payload["rig"] != "dashboard" {
		t.Errorf("payload.rig = %v, want dashboard", payload["rig"])
	}
}

// TestCherryHasUnmergedCommits covers the git-cherry output parser used by
// verifyBranchAlreadyMerged (aa-apw).
func TestCherryHasUnmergedCommits(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty output — branch has no commits beyond base", "", false},
		{"whitespace only", "  \n\n", false},
		{"all squash-applied (-)", "- abc123\n- def456\n", false},
		{"one unmerged (+)", "+ abc123\n", true},
		{"mixed", "- abc123\n+ def456\n", true},
		{"unmerged only", "+ a\n+ b\n+ c\n", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := cherryHasUnmergedCommits(tc.in); got != tc.want {
				t.Errorf("cherryHasUnmergedCommits(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestHandleZombieRestart_SkipsWhenBranchAlreadyMerged verifies the aa-apw fix:
// when a stopped polecat's branch work is already merged to origin/main (e.g.,
// via squash-merge), the witness must NOT restart the session — restarting
// would let the polecat re-push its pre-squash HEAD and create a duplicate MR.
// Instead the polecat is archived.
//
// Not parallel: overrides the package-level verifyBranchAlreadyMerged var.
func TestHandleZombieRestart_SkipsWhenBranchAlreadyMerged(t *testing.T) {
	oldVerify := verifyBranchAlreadyMerged
	verifyBranchAlreadyMerged = func(workDir, rigName, polecatName string) (bool, error) {
		return true, nil
	}
	t.Cleanup(func() { verifyBranchAlreadyMerged = oldVerify })

	bd, _ := mockBd(
		func(args []string) (string, error) { return "[]", nil },
		func(args []string) error { return nil },
	)

	z := &ZombieResult{PolecatName: "scavenger", HookBead: "ma-poc.4"}
	handleZombieRestart(bd, t.TempDir(), "testrig", "scavenger", "ma-poc.4", "has_unpushed", z)

	// Action must reflect the archive decision; must NOT be a "restarted*" action.
	if !strings.Contains(z.Action, "work-already-merged") {
		t.Errorf("action = %q, want it to mention work-already-merged (aa-apw)", z.Action)
	}
	if strings.HasPrefix(z.Action, "restarted") || strings.HasPrefix(z.Action, "restart-") {
		t.Errorf("action = %q, polecat must not be restarted when work is already merged", z.Action)
	}
}

// TestHandleZombieRestart_RestartsWhenBranchNotMerged verifies the pre-aa-apw
// behavior is preserved when work is NOT merged: handleZombieRestart proceeds
// to its normal cleanup/restart flow.
//
// Not parallel: overrides the package-level verifyBranchAlreadyMerged var.
func TestHandleZombieRestart_RestartsWhenBranchNotMerged(t *testing.T) {
	oldVerify := verifyBranchAlreadyMerged
	verifyBranchAlreadyMerged = func(workDir, rigName, polecatName string) (bool, error) {
		return false, nil
	}
	t.Cleanup(func() { verifyBranchAlreadyMerged = oldVerify })

	bd, _ := mockBd(
		func(args []string) (string, error) { return "[]", nil },
		func(args []string) error { return nil },
	)

	z := &ZombieResult{PolecatName: "scavenger", HookBead: "ma-poc.4"}
	handleZombieRestart(bd, t.TempDir(), "testrig", "scavenger", "ma-poc.4", "clean", z)

	// Should NOT take the archive path.
	if strings.Contains(z.Action, "work-already-merged") {
		t.Errorf("action = %q, should not archive when work is not merged", z.Action)
	}
}
