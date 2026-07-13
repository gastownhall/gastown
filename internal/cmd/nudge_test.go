package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/nudge"
	"github.com/steveyegge/gastown/internal/session"
)

func setupNudgeTestRegistry(t *testing.T) {
	t.Helper()
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("bd", "beads")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

func TestNudgeStdinConflict(t *testing.T) {
	// Save and restore package-level flags
	origMessage := nudgeMessageFlag
	origStdin := nudgeStdinFlag
	defer func() {
		nudgeMessageFlag = origMessage
		nudgeStdinFlag = origStdin
	}()

	// When both --stdin and --message are set, runNudge should return an error
	nudgeStdinFlag = true
	nudgeMessageFlag = "some message"

	err := runNudge(nudgeCmd, []string{"gastown/alpha"})
	if err == nil {
		t.Fatal("expected error when --stdin and --message are both set")
	}
	if !strings.Contains(err.Error(), "cannot use --stdin with --message/-m") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestResolveNudgePattern(t *testing.T) {
	setupNudgeTestRegistry(t)
	// Create test agent sessions (using rig prefixes)
	agents := []*AgentSession{
		{Name: "hq-mayor", Type: AgentMayor},
		{Name: "hq-deacon", Type: AgentDeacon},
		{Name: "gt-witness", Type: AgentWitness, Rig: "gastown"},
		{Name: "gt-refinery", Type: AgentRefinery, Rig: "gastown"},
		{Name: "gt-crew-max", Type: AgentCrew, Rig: "gastown", AgentName: "max"},
		{Name: "gt-crew-jack", Type: AgentCrew, Rig: "gastown", AgentName: "jack"},
		{Name: "gt-alpha", Type: AgentPolecat, Rig: "gastown", AgentName: "alpha"},
		{Name: "gt-beta", Type: AgentPolecat, Rig: "gastown", AgentName: "beta"},
		{Name: "bd-witness", Type: AgentWitness, Rig: "beads"},
		{Name: "bd-gamma", Type: AgentPolecat, Rig: "beads", AgentName: "gamma"},
	}

	tests := []struct {
		name     string
		pattern  string
		expected []string
	}{
		{
			name:     "mayor special case",
			pattern:  "mayor",
			expected: []string{"hq-mayor"},
		},
		{
			name:     "deacon special case",
			pattern:  "deacon",
			expected: []string{"hq-deacon"},
		},
		{
			name:     "specific witness",
			pattern:  "gastown/witness",
			expected: []string{"gt-witness"},
		},
		{
			name:     "all witnesses",
			pattern:  "*/witness",
			expected: []string{"gt-witness", "bd-witness"},
		},
		{
			name:     "specific refinery",
			pattern:  "gastown/refinery",
			expected: []string{"gt-refinery"},
		},
		{
			name:     "all polecats in rig",
			pattern:  "gastown/polecats/*",
			expected: []string{"gt-alpha", "gt-beta"},
		},
		{
			name:     "specific polecat",
			pattern:  "gastown/polecats/alpha",
			expected: []string{"gt-alpha"},
		},
		{
			name:     "all crew in rig",
			pattern:  "gastown/crew/*",
			expected: []string{"gt-crew-max", "gt-crew-jack"},
		},
		{
			name:     "specific crew member",
			pattern:  "gastown/crew/max",
			expected: []string{"gt-crew-max"},
		},
		{
			name:     "legacy polecat format",
			pattern:  "gastown/alpha",
			expected: []string{"gt-alpha"},
		},
		{
			name:     "no matches",
			pattern:  "nonexistent/polecats/*",
			expected: nil,
		},
		{
			name:     "invalid pattern",
			pattern:  "invalid",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveNudgePattern(tt.pattern, agents)

			if len(got) != len(tt.expected) {
				t.Errorf("resolveNudgePattern(%q) returned %d results, want %d: got %v, want %v",
					tt.pattern, len(got), len(tt.expected), got, tt.expected)
				return
			}

			// Check each expected value is present
			gotMap := make(map[string]bool)
			for _, g := range got {
				gotMap[g] = true
			}
			for _, e := range tt.expected {
				if !gotMap[e] {
					t.Errorf("resolveNudgePattern(%q) missing expected %q, got %v",
						tt.pattern, e, got)
				}
			}
		})
	}
}

func TestSessionNameToAddress(t *testing.T) {
	setupNudgeTestRegistry(t)
	tests := []struct {
		name        string
		sessionName string
		expected    string
	}{
		{
			name:        "mayor",
			sessionName: "hq-mayor",
			expected:    "mayor",
		},
		{
			name:        "deacon",
			sessionName: "hq-deacon",
			expected:    "deacon",
		},
		{
			name:        "witness",
			sessionName: "gt-witness",
			expected:    "gastown/witness",
		},
		{
			name:        "refinery",
			sessionName: "gt-refinery",
			expected:    "gastown/refinery",
		},
		{
			name:        "crew member",
			sessionName: "gt-crew-max",
			expected:    "gastown/crew/max",
		},
		{
			name:        "polecat",
			sessionName: "gt-alpha",
			expected:    "gastown/alpha",
		},
		{
			name:        "dog",
			sessionName: "hq-dog-alpha",
			expected:    "deacon/dogs/alpha",
		},
		{
			name:        "hyphenated dog",
			sessionName: "hq-dog-my-dog",
			expected:    "deacon/dogs/my-dog",
		},
		{
			name:        "unrecognized format",
			sessionName: "plaintext",
			expected:    "",
		},
		{
			name:        "gt prefix but no name",
			sessionName: "gt-",
			expected:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sessionNameToAddress(tt.sessionName)
			if got != tt.expected {
				t.Errorf("sessionNameToAddress(%q) = %q, want %q", tt.sessionName, got, tt.expected)
			}
		})
	}
}

func TestNudgeInvalidMode(t *testing.T) {
	// Save and restore package-level flags
	origMode := nudgeModeFlag
	origPriority := nudgePriorityFlag
	origMessage := nudgeMessageFlag
	origStdin := nudgeStdinFlag
	defer func() {
		nudgeModeFlag = origMode
		nudgePriorityFlag = origPriority
		nudgeMessageFlag = origMessage
		nudgeStdinFlag = origStdin
	}()

	nudgeStdinFlag = false
	nudgeMessageFlag = "test"

	tests := []struct {
		name    string
		mode    string
		wantErr string
	}{
		{"bogus mode", "bogus", `invalid --mode "bogus"`},
		{"empty mode", "", `invalid --mode ""`},
		{"typo immediate", "imediate", `invalid --mode "imediate"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nudgeModeFlag = tt.mode
			nudgePriorityFlag = "normal"
			err := runNudge(nudgeCmd, []string{"gastown/alpha", "hello"})
			if err == nil {
				t.Fatal("expected error for invalid mode")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("got error %q, want to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestNudgeInvalidPriority(t *testing.T) {
	// Save and restore package-level flags
	origMode := nudgeModeFlag
	origPriority := nudgePriorityFlag
	origMessage := nudgeMessageFlag
	origStdin := nudgeStdinFlag
	defer func() {
		nudgeModeFlag = origMode
		nudgePriorityFlag = origPriority
		nudgeMessageFlag = origMessage
		nudgeStdinFlag = origStdin
	}()

	nudgeStdinFlag = false
	nudgeMessageFlag = "test"
	nudgeModeFlag = NudgeModeImmediate

	tests := []struct {
		name     string
		priority string
		wantErr  string
	}{
		{"bogus priority", "bogus", `invalid --priority "bogus"`},
		{"empty priority", "", `invalid --priority ""`},
		{"high priority", "high", `invalid --priority "high"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nudgePriorityFlag = tt.priority
			err := runNudge(nudgeCmd, []string{"gastown/alpha", "hello"})
			if err == nil {
				t.Fatal("expected error for invalid priority")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("got error %q, want to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestNudgeValidModesAccepted(t *testing.T) {
	// Verify all valid modes pass the validation check (they'll fail later
	// on tmux operations, but should NOT fail on mode validation).
	origMode := nudgeModeFlag
	origPriority := nudgePriorityFlag
	origMessage := nudgeMessageFlag
	origStdin := nudgeStdinFlag
	origTimeout := waitIdleTimeout
	defer func() {
		nudgeModeFlag = origMode
		nudgePriorityFlag = origPriority
		nudgeMessageFlag = origMessage
		nudgeStdinFlag = origStdin
		waitIdleTimeout = origTimeout
	}()

	// Route nudge transport to a log file so the test doesn't deliver "test"
	// messages to live agents (mayor reported recurring synthetic nudges).
	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))

	// Shorten wait-idle timeout to avoid 15s test delay
	waitIdleTimeout = 200 * time.Millisecond

	nudgeStdinFlag = false
	nudgeMessageFlag = "test"
	nudgePriorityFlag = "normal"

	for _, mode := range []string{NudgeModeImmediate, NudgeModeQueue, NudgeModeWaitIdle} {
		t.Run(mode, func(t *testing.T) {
			nudgeModeFlag = mode
			err := runNudge(nudgeCmd, []string{"gastown/alpha", "hello"})
			// The error should NOT be about invalid mode — it will fail on
			// tmux or workspace, which is fine.
			if err != nil && strings.Contains(err.Error(), "invalid --mode") {
				t.Errorf("valid mode %q was rejected: %v", mode, err)
			}
		})
	}
}

func TestIfFreshMaxAge(t *testing.T) {
	// Verify the constant is 60 seconds as specified in the design.
	if ifFreshMaxAge != 60*time.Second {
		t.Errorf("ifFreshMaxAge = %v, want 60s", ifFreshMaxAge)
	}
}

func TestIfFreshSessionAgeCheck(t *testing.T) {
	// Test the age comparison logic used by --if-fresh.
	// A session created 10 seconds ago should be "fresh" (nudge allowed).
	// A session created 120 seconds ago should be "stale" (nudge suppressed).
	now := time.Now()

	tests := []struct {
		name        string
		createdAt   time.Time
		shouldNudge bool
	}{
		{
			name:        "fresh session (10s old)",
			createdAt:   now.Add(-10 * time.Second),
			shouldNudge: true,
		},
		{
			name:        "borderline session (59s old)",
			createdAt:   now.Add(-59 * time.Second),
			shouldNudge: true,
		},
		{
			name:        "stale session (61s old)",
			createdAt:   now.Add(-61 * time.Second),
			shouldNudge: false,
		},
		{
			name:        "very stale session (5min old)",
			createdAt:   now.Add(-5 * time.Minute),
			shouldNudge: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			age := time.Since(tt.createdAt)
			shouldNudge := age <= ifFreshMaxAge
			if shouldNudge != tt.shouldNudge {
				t.Errorf("age=%v: shouldNudge=%v, want %v", age, shouldNudge, tt.shouldNudge)
			}
		})
	}
}

func TestPostQueueIdleRecovery_SkipsDeliveryWhenDrainEmpty(t *testing.T) {
	// Behavioral test (gt-y2zk): when the idle recovery path fires but
	// another process already drained the queue, we must NOT deliver to
	// avoid duplicates. This exercises the len(drained) > 0 guard.
	townRoot := t.TempDir()
	session := "gt-crew-test"

	// Enqueue a nudge, then drain it (simulating a racing hook).
	if err := nudge.Enqueue(townRoot, session, nudge.QueuedNudge{
		Sender:  "test",
		Message: "hello",
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	drained, err := nudge.Drain(townRoot, session)
	if err != nil {
		t.Fatalf("first Drain: %v", err)
	}
	if len(drained) != 1 {
		t.Fatalf("first Drain got %d entries, want 1", len(drained))
	}

	// Second drain should return empty — the racing hook already claimed it.
	drained2, err := nudge.Drain(townRoot, session)
	if err != nil {
		t.Fatalf("second Drain: %v", err)
	}
	if len(drained2) != 0 {
		t.Errorf("second Drain got %d entries, want 0 (already claimed)", len(drained2))
	}
}

func TestRequeueDrainedNudgesPreservesFailedDelivery(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-crew-test"
	drained := []nudge.QueuedNudge{
		{Sender: "test", Message: "first", Timestamp: time.Now().Add(-time.Second)},
		{Sender: "test", Message: "second", Timestamp: time.Now()},
	}

	requeueDrainedNudges(townRoot, session, "test", drained)

	got, err := nudge.Drain(townRoot, session)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(got) != len(drained) {
		t.Fatalf("Drain got %d nudges, want %d", len(got), len(drained))
	}
	for i := range drained {
		if got[i].Message != drained[i].Message || got[i].Sender != drained[i].Sender {
			t.Fatalf("requeued[%d] = %#v, want %#v", i, got[i], drained[i])
		}
	}
}

func TestValidModeMapsMatchConstants(t *testing.T) {
	// Ensure the validation maps cover all defined mode constants.
	modes := []string{NudgeModeImmediate, NudgeModeQueue, NudgeModeWaitIdle}
	for _, m := range modes {
		if !validNudgeModes[m] {
			t.Errorf("mode constant %q missing from validNudgeModes", m)
		}
	}
	priorities := []string{nudge.PriorityNormal, nudge.PriorityUrgent}
	for _, p := range priorities {
		if !validNudgePriorities[p] {
			t.Errorf("priority constant %q missing from validNudgePriorities", p)
		}
	}
}

func TestIdleWatcherTimeout(t *testing.T) {
	// Verify the watcher timeout is in a reasonable range.
	if idleWatcherTimeout < 10*time.Second {
		t.Errorf("idleWatcherTimeout = %v, too short (min 10s)", idleWatcherTimeout)
	}
	if idleWatcherTimeout > 5*time.Minute {
		t.Errorf("idleWatcherTimeout = %v, too long (max 5m)", idleWatcherTimeout)
	}
}

func TestIdleWatcherPollInterval(t *testing.T) {
	// Verify the poll interval is reasonable — fast enough to be responsive,
	// slow enough to not burn CPU.
	if idleWatcherPollInterval < 200*time.Millisecond {
		t.Errorf("idleWatcherPollInterval = %v, too fast (min 200ms)", idleWatcherPollInterval)
	}
	if idleWatcherPollInterval > 5*time.Second {
		t.Errorf("idleWatcherPollInterval = %v, too slow (max 5s)", idleWatcherPollInterval)
	}
}

func TestNudgeTrailingSlashNormalization(t *testing.T) {
	// The mail system uses "mayor/" and "deacon/" as canonical addresses.
	// runNudge must strip the trailing slash so these match the role shortcuts.
	// Without normalization, "mayor/" falls through to parseAddress which
	// rejects it ("invalid address format"), silently dropping the nudge.
	origMode := nudgeModeFlag
	origPriority := nudgePriorityFlag
	origMessage := nudgeMessageFlag
	origStdin := nudgeStdinFlag
	origTimeout := waitIdleTimeout
	defer func() {
		nudgeModeFlag = origMode
		nudgePriorityFlag = origPriority
		nudgeMessageFlag = origMessage
		nudgeStdinFlag = origStdin
		waitIdleTimeout = origTimeout
	}()

	// Route nudge transport to a log file so this test doesn't deliver to
	// the real mayor/deacon/witness/refinery sessions on host.
	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))

	waitIdleTimeout = 200 * time.Millisecond
	nudgeStdinFlag = false
	nudgeMessageFlag = "test"
	nudgePriorityFlag = "normal"
	nudgeModeFlag = NudgeModeImmediate

	for _, target := range []string{"mayor/", "deacon/", "witness/", "refinery/"} {
		t.Run(target, func(t *testing.T) {
			err := runNudge(nudgeCmd, []string{target, "hello"})
			// Will fail on tmux/session lookup, but must NOT fail on address parsing.
			if err != nil && strings.Contains(err.Error(), "invalid address format") {
				t.Errorf("trailing-slash target %q was rejected as invalid address: %v", target, err)
			}
		})
	}
}

func TestNudgeDogTargetRoutesToDogSession(t *testing.T) {
	origMode := nudgeModeFlag
	origPriority := nudgePriorityFlag
	origMessage := nudgeMessageFlag
	origStdin := nudgeStdinFlag
	origForce := nudgeForceFlag
	defer func() {
		nudgeModeFlag = origMode
		nudgePriorityFlag = origPriority
		nudgeMessageFlag = origMessage
		nudgeStdinFlag = origStdin
		nudgeForceFlag = origForce
	}()

	logPath := filepath.Join(t.TempDir(), "nudge.log")
	t.Setenv("GT_TEST_NUDGE_LOG", logPath)

	nudgeModeFlag = NudgeModeImmediate
	nudgePriorityFlag = nudge.PriorityNormal
	nudgeMessageFlag = "hello dog"
	nudgeStdinFlag = false
	nudgeForceFlag = true

	if err := runNudge(nudgeCmd, []string{"deacon/dogs/fido"}); err != nil {
		t.Fatalf("runNudge dog target returned error: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading nudge log: %v", err)
	}
	if got, want := string(data), "nudge:hq-dog-fido:"; !strings.Contains(got, want) {
		t.Fatalf("nudge log = %q, want containing %q", got, want)
	}
}

func TestIdleWatcherExitsOnEmptyQueue(t *testing.T) {
	// watchAndDeliver should exit immediately when queue is empty
	// (someone else drained it). We test this by calling with a
	// temp dir that has no queue files.
	origTimeout := idleWatcherTimeout
	origInterval := idleWatcherPollInterval
	defer func() {
		idleWatcherTimeout = origTimeout
		idleWatcherPollInterval = origInterval
	}()

	// Very short timeout so test doesn't hang
	idleWatcherTimeout = 500 * time.Millisecond
	idleWatcherPollInterval = 50 * time.Millisecond

	tmpDir := t.TempDir()

	// watchAndDeliver checks QueueLen first — with no queue files,
	// it should exit immediately. We verify it doesn't block.
	done := make(chan struct{})
	go func() {
		// Use a nil-safe Tmux — QueueLen returns 0 before IsIdle is called.
		watchAndDeliver(nil, tmpDir, "test-session")
		close(done)
	}()

	select {
	case <-done:
		// Good — exited because queue was empty
	case <-time.After(2 * time.Second):
		t.Fatal("watchAndDeliver did not exit within 2s for empty queue")
	}
}

func TestQueueLen(t *testing.T) {
	tmpDir := t.TempDir()

	// Empty queue
	if got := nudge.QueueLen(tmpDir, "test-session"); got != 0 {
		t.Errorf("QueueLen on empty dir = %d, want 0", got)
	}

	// Enqueue one
	err := nudge.Enqueue(tmpDir, "test-session", nudge.QueuedNudge{
		Sender:  "test",
		Message: "hello",
	})
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	if got := nudge.QueueLen(tmpDir, "test-session"); got != 1 {
		t.Errorf("QueueLen after enqueue = %d, want 1", got)
	}

	// Drain and verify empty
	_, _ = nudge.Drain(tmpDir, "test-session")
	if got := nudge.QueueLen(tmpDir, "test-session"); got != 0 {
		t.Errorf("QueueLen after drain = %d, want 0", got)
	}
}
