package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/checkpoint"
	"github.com/steveyegge/gastown/internal/constants"
)

// captureStdout redirects os.Stdout to a pipe, calls fn, then returns whatever
// fn wrote. Reading happens in a goroutine so the pipe buffer cannot deadlock
// even when fn produces more output than the OS pipe buffer (4 KB on Windows).
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	// Drain the read side concurrently so writes never block.
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		io.Copy(&buf, r)
		close(done)
	}()

	fn()

	w.Close()
	<-done
	os.Stdout = oldStdout
	return buf.String()
}

func writeTestRoutes(t *testing.T, townRoot string, routes []beads.Route) {
	t.Helper()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("create beads dir: %v", err)
	}
	if err := beads.WriteRoutes(beadsDir, routes); err != nil {
		t.Fatalf("write routes: %v", err)
	}
}

func TestGetAgentBeadID_UsesRigPrefix(t *testing.T) {
	townRoot := t.TempDir()
	writeTestRoutes(t, townRoot, []beads.Route{
		{Prefix: "bd-", Path: "beads/mayor/rig"},
	})

	cases := []struct {
		name string
		ctx  RoleContext
		want string
	}{
		{
			name: "mayor",
			ctx: RoleContext{
				Role:     RoleMayor,
				TownRoot: townRoot,
			},
			want: "hq-mayor",
		},
		{
			name: "deacon",
			ctx: RoleContext{
				Role:     RoleDeacon,
				TownRoot: townRoot,
			},
			want: "hq-deacon",
		},
		{
			name: "witness",
			ctx: RoleContext{
				Role:     RoleWitness,
				Rig:      "beads",
				TownRoot: townRoot,
			},
			want: "bd-beads-witness",
		},
		{
			name: "refinery",
			ctx: RoleContext{
				Role:     RoleRefinery,
				Rig:      "beads",
				TownRoot: townRoot,
			},
			want: "bd-beads-refinery",
		},
		{
			name: "polecat",
			ctx: RoleContext{
				Role:     RolePolecat,
				Rig:      "beads",
				Polecat:  "lex",
				TownRoot: townRoot,
			},
			want: "bd-beads-polecat-lex",
		},
		{
			name: "crew",
			ctx: RoleContext{
				Role:     RoleCrew,
				Rig:      "beads",
				Polecat:  "lex",
				TownRoot: townRoot,
			},
			want: "bd-beads-crew-lex",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := getAgentBeadID(tc.ctx)
			if got != tc.want {
				t.Fatalf("getAgentBeadID() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRigBeadsRootPrefersRouteResolvedRigDir(t *testing.T) {
	townRoot := t.TempDir()
	writeTestRoutes(t, townRoot, []beads.Route{
		{Prefix: "gt-", Path: "gastown/mayor/rig"},
		{Prefix: "hq-", Path: "."},
	})

	ctx := RoleContext{
		Role:     RolePolecat,
		Rig:      "gastown",
		Polecat:  "toast",
		TownRoot: townRoot,
		WorkDir:  filepath.Join(townRoot, "gastown", "polecats", "toast", "gastown"),
	}

	got := rigBeadsRoot(ctx)
	want := filepath.Join(townRoot, "gastown", "mayor", "rig")
	if got != want {
		t.Fatalf("rigBeadsRoot() = %q, want route-resolved %q", got, want)
	}
}

func TestRigBeadsRootFallsBackWhenRouteMissing(t *testing.T) {
	townRoot := t.TempDir()
	ctx := RoleContext{
		Role:     RolePolecat,
		Rig:      "gastown",
		TownRoot: townRoot,
		WorkDir:  filepath.Join(townRoot, "gastown", "polecats", "toast", "gastown"),
	}

	got := rigBeadsRoot(ctx)
	want := filepath.Join(townRoot, "gastown")
	if got != want {
		t.Fatalf("rigBeadsRoot() = %q, want fallback %q", got, want)
	}
}

func TestPrimeFlagCombinations(t *testing.T) {
	gtBin := buildGT(t)

	cases := []struct {
		name      string
		args      []string
		wantError bool
		errorMsg  string
	}{
		{
			name:      "state_alone_is_valid",
			args:      []string{"prime", "--state"},
			wantError: false, // May fail for other reasons (not in workspace), but not flag validation
		},
		{
			name:      "state_with_hook_errors",
			args:      []string{"prime", "--state", "--hook"},
			wantError: true,
			errorMsg:  "--state cannot be combined with other flags",
		},
		{
			name:      "state_with_dry_run_errors",
			args:      []string{"prime", "--state", "--dry-run"},
			wantError: true,
			errorMsg:  "--state cannot be combined with other flags",
		},
		{
			name:      "state_with_explain_errors",
			args:      []string{"prime", "--state", "--explain"},
			wantError: true,
			errorMsg:  "--state cannot be combined with other flags",
		},
		{
			name:      "dry_run_and_explain_valid",
			args:      []string{"prime", "--dry-run", "--explain"},
			wantError: false, // May fail for other reasons, but not flag validation
		},
		{
			name:      "hook_and_dry_run_valid",
			args:      []string{"prime", "--hook", "--dry-run"},
			wantError: false, // May fail for other reasons, but not flag validation
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(gtBin, tc.args...)
			output, err := cmd.CombinedOutput()

			if tc.wantError {
				if err == nil {
					t.Fatalf("expected error, got success with output: %s", output)
				}
				if tc.errorMsg != "" && !strings.Contains(string(output), tc.errorMsg) {
					t.Fatalf("expected error containing %q, got: %s", tc.errorMsg, output)
				}
			}
			// For non-error cases, we don't fail on other errors (like "not in workspace")
			// because we're only testing flag validation
			if !tc.wantError && tc.errorMsg != "" && strings.Contains(string(output), tc.errorMsg) {
				t.Fatalf("unexpected error message %q in output: %s", tc.errorMsg, output)
			}
		})
	}
}

// TestCheckHandoffMarkerDryRun tests that dry-run mode doesn't remove the handoff marker.
func TestCheckHandoffMarkerDryRun(t *testing.T) {
	workDir := t.TempDir()

	// Create .runtime directory and handoff marker
	runtimeDir := filepath.Join(workDir, constants.DirRuntime)
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatalf("create runtime dir: %v", err)
	}

	markerPath := filepath.Join(runtimeDir, constants.FileHandoffMarker)
	prevSession := "test-session-123"
	if err := os.WriteFile(markerPath, []byte(prevSession), 0644); err != nil {
		t.Fatalf("write handoff marker: %v", err)
	}

	// Enable explain mode for this test
	oldExplain := primeExplain
	primeExplain = true
	defer func() { primeExplain = oldExplain }()

	// Capture stdout to verify explain output
	output := captureStdout(t, func() {
		checkHandoffMarkerDryRun(workDir)
	})

	// Verify marker still exists (not removed in dry-run)
	if _, err := os.Stat(markerPath); os.IsNotExist(err) {
		t.Fatalf("handoff marker was removed in dry-run mode")
	}

	// Verify marker content unchanged
	data, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read handoff marker: %v", err)
	}
	if string(data) != prevSession {
		t.Fatalf("marker content changed: got %q, want %q", string(data), prevSession)
	}

	// Verify explain output mentions dry-run
	if !strings.Contains(output, "dry-run") {
		t.Fatalf("expected explain output to mention dry-run, got: %s", output)
	}
}

// TestCheckHandoffMarkerDryRun_NoMarker tests dry-run when no marker exists.
func TestCheckHandoffMarkerDryRun_NoMarker(t *testing.T) {
	workDir := t.TempDir()

	// Create .runtime directory but no marker
	runtimeDir := filepath.Join(workDir, constants.DirRuntime)
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatalf("create runtime dir: %v", err)
	}

	// Enable explain mode
	oldExplain := primeExplain
	primeExplain = true
	defer func() { primeExplain = oldExplain }()

	// Should not panic when marker doesn't exist
	output := captureStdout(t, func() {
		checkHandoffMarkerDryRun(workDir)
	})

	// Verify explain output indicates no marker
	if !strings.Contains(output, "no handoff marker") {
		t.Fatalf("expected explain output to indicate no marker, got: %s", output)
	}
}

// TestDetectSessionState tests detectSessionState for all states.
func TestDetectSessionState(t *testing.T) {
	t.Run("normal_state", func(t *testing.T) {
		workDir := t.TempDir()
		ctx := RoleContext{
			Role:    RoleMayor,
			WorkDir: workDir,
		}

		state := detectSessionState(ctx)

		if state.State != "normal" {
			t.Fatalf("expected state 'normal', got %q", state.State)
		}
		if state.Role != RoleMayor {
			t.Fatalf("expected role Mayor, got %q", state.Role)
		}
	})

	t.Run("post_handoff_state", func(t *testing.T) {
		workDir := t.TempDir()

		// Create handoff marker
		runtimeDir := filepath.Join(workDir, constants.DirRuntime)
		if err := os.MkdirAll(runtimeDir, 0755); err != nil {
			t.Fatalf("create runtime dir: %v", err)
		}
		prevSession := "predecessor-session-abc"
		markerPath := filepath.Join(runtimeDir, constants.FileHandoffMarker)
		if err := os.WriteFile(markerPath, []byte(prevSession), 0644); err != nil {
			t.Fatalf("write handoff marker: %v", err)
		}

		ctx := RoleContext{
			Role:    RolePolecat,
			Rig:     "beads",
			Polecat: "jade",
			WorkDir: workDir,
		}

		state := detectSessionState(ctx)

		if state.State != "post-handoff" {
			t.Fatalf("expected state 'post-handoff', got %q", state.State)
		}
		if state.PrevSession != prevSession {
			t.Fatalf("expected prev_session %q, got %q", prevSession, state.PrevSession)
		}
	})

	t.Run("crash_recovery_state", func(t *testing.T) {
		workDir := t.TempDir()

		// Create a checkpoint (simulating a crashed session)
		cp := &checkpoint.Checkpoint{
			SessionID:  "crashed-session",
			HookedBead: "bd-test123",
			StepTitle:  "Working on feature X",
			Timestamp:  time.Now().Add(-1 * time.Hour), // 1 hour old
		}
		if err := checkpoint.Write(workDir, cp); err != nil {
			t.Fatalf("write checkpoint: %v", err)
		}

		ctx := RoleContext{
			Role:    RolePolecat,
			Rig:     "beads",
			Polecat: "jade",
			WorkDir: workDir,
		}

		state := detectSessionState(ctx)

		if state.State != "crash-recovery" {
			t.Fatalf("expected state 'crash-recovery', got %q", state.State)
		}
		if state.CheckpointAge == "" {
			t.Fatalf("expected checkpoint_age to be set")
		}
	})

	t.Run("crash_recovery_only_for_workers", func(t *testing.T) {
		workDir := t.TempDir()

		// Create a checkpoint
		cp := &checkpoint.Checkpoint{
			SessionID:  "crashed-session",
			HookedBead: "bd-test123",
			StepTitle:  "Working on feature X",
			Timestamp:  time.Now().Add(-1 * time.Hour),
		}
		if err := checkpoint.Write(workDir, cp); err != nil {
			t.Fatalf("write checkpoint: %v", err)
		}

		// Mayor should NOT enter crash-recovery (only polecat/crew)
		ctx := RoleContext{
			Role:    RoleMayor,
			WorkDir: workDir,
		}

		state := detectSessionState(ctx)

		// Mayor should see normal state, not crash-recovery
		if state.State != "normal" {
			t.Fatalf("expected Mayor to have 'normal' state despite checkpoint, got %q", state.State)
		}
	})

	t.Run("autonomous_state_hooked_bead", func(t *testing.T) {
		// Skip: bd CLI 0.47.2 has a bug where database writes don't commit
		// ("sql: database is closed" during auto-flush). This blocks tests
		// that need to create issues. See internal issue for tracking.
		t.Skip("bd CLI 0.47.2 bug: database writes don't commit")

		// Skip if bd CLI is not available
		if _, err := exec.LookPath("bd"); err != nil {
			t.Skip("bd binary not found in PATH")
		}

		workDir := t.TempDir()
		townRoot := workDir

		// Initialize beads database
		initCmd := exec.Command("bd", "init", "--prefix=bd-")
		initCmd.Dir = workDir
		if output, err := initCmd.CombinedOutput(); err != nil {
			t.Fatalf("bd init failed: %v\n%s", err, output)
		}

		// Write routes file
		beadsDir := filepath.Join(workDir, ".beads")
		routes := []beads.Route{{Prefix: "bd-", Path: "."}}
		if err := beads.WriteRoutes(beadsDir, routes); err != nil {
			t.Fatalf("write routes: %v", err)
		}

		// Create a hooked bead assigned to beads/polecats/jade
		b := beads.New(workDir)
		issue, err := b.Create(beads.CreateOptions{
			Title:    "Test hooked bead",
			Priority: 2,
		})
		if err != nil {
			t.Fatalf("create bead: %v", err)
		}

		// Update bead to set status and assignee
		status := beads.StatusHooked
		assignee := "beads/polecats/jade"
		if err := b.Update(issue.ID, beads.UpdateOptions{
			Status:   &status,
			Assignee: &assignee,
		}); err != nil {
			t.Fatalf("update bead: %v", err)
		}

		ctx := RoleContext{
			Role:     RolePolecat,
			Rig:      "beads",
			Polecat:  "jade",
			WorkDir:  workDir,
			TownRoot: townRoot,
		}

		state := detectSessionState(ctx)

		if state.State != "autonomous" {
			t.Fatalf("expected state 'autonomous', got %q", state.State)
		}
		if state.HookedBead != issue.ID {
			t.Fatalf("expected hooked_bead %q, got %q", issue.ID, state.HookedBead)
		}
	})
}

// TestOutputState tests outputState function output formats.
func TestOutputState(t *testing.T) {
	t.Run("text_output", func(t *testing.T) {
		workDir := t.TempDir()
		ctx := RoleContext{
			Role:    RoleMayor,
			WorkDir: workDir,
		}

		output := captureStdout(t, func() {
			outputState(ctx, false)
		})

		if !strings.Contains(output, "state: normal") {
			t.Fatalf("expected 'state: normal' in output, got: %s", output)
		}
		if !strings.Contains(output, "role: mayor") {
			t.Fatalf("expected 'role: mayor' in output, got: %s", output)
		}
	})

	t.Run("json_output", func(t *testing.T) {
		workDir := t.TempDir()
		ctx := RoleContext{
			Role:    RolePolecat,
			Rig:     "beads",
			Polecat: "jade",
			WorkDir: workDir,
		}

		output := captureStdout(t, func() {
			outputState(ctx, true)
		})

		// Parse JSON output
		var state SessionState
		if err := json.Unmarshal([]byte(output), &state); err != nil {
			t.Fatalf("failed to parse JSON output: %v, output was: %s", err, output)
		}

		if state.State != "normal" {
			t.Fatalf("expected state 'normal', got %q", state.State)
		}
		if state.Role != RolePolecat {
			t.Fatalf("expected role 'polecat', got %q", state.Role)
		}
	})

	t.Run("json_output_post_handoff", func(t *testing.T) {
		workDir := t.TempDir()

		// Create handoff marker
		runtimeDir := filepath.Join(workDir, constants.DirRuntime)
		if err := os.MkdirAll(runtimeDir, 0755); err != nil {
			t.Fatalf("create runtime dir: %v", err)
		}
		prevSession := "prev-session-xyz"
		markerPath := filepath.Join(runtimeDir, constants.FileHandoffMarker)
		if err := os.WriteFile(markerPath, []byte(prevSession), 0644); err != nil {
			t.Fatalf("write marker: %v", err)
		}

		ctx := RoleContext{
			Role:    RolePolecat,
			Rig:     "beads",
			Polecat: "jade",
			WorkDir: workDir,
		}

		output := captureStdout(t, func() {
			outputState(ctx, true)
		})

		// Parse JSON
		var state SessionState
		if err := json.Unmarshal([]byte(output), &state); err != nil {
			t.Fatalf("failed to parse JSON: %v", err)
		}

		if state.State != "post-handoff" {
			t.Fatalf("expected state 'post-handoff', got %q", state.State)
		}
		if state.PrevSession != prevSession {
			t.Fatalf("expected prev_session %q, got %q", prevSession, state.PrevSession)
		}
	})
}

// TestExplain tests the explain function output.
func TestExplain(t *testing.T) {
	t.Run("explain_enabled_condition_true", func(t *testing.T) {
		// Enable explain mode
		oldExplain := primeExplain
		primeExplain = true
		defer func() { primeExplain = oldExplain }()

		output := captureStdout(t, func() {
			explain(true, "This is a test explanation")
		})

		if !strings.Contains(output, "[EXPLAIN]") {
			t.Fatalf("expected [EXPLAIN] tag in output, got: %s", output)
		}
		if !strings.Contains(output, "This is a test explanation") {
			t.Fatalf("expected explanation text in output, got: %s", output)
		}
	})

	t.Run("explain_enabled_condition_false", func(t *testing.T) {
		// Enable explain mode
		oldExplain := primeExplain
		primeExplain = true
		defer func() { primeExplain = oldExplain }()

		output := captureStdout(t, func() {
			explain(false, "This should not appear")
		})

		if strings.Contains(output, "[EXPLAIN]") {
			t.Fatalf("expected no [EXPLAIN] tag when condition is false, got: %s", output)
		}
	})

	t.Run("explain_disabled", func(t *testing.T) {
		// Disable explain mode
		oldExplain := primeExplain
		primeExplain = false
		defer func() { primeExplain = oldExplain }()

		output := captureStdout(t, func() {
			explain(true, "This should not appear either")
		})

		if strings.Contains(output, "[EXPLAIN]") {
			t.Fatalf("expected no [EXPLAIN] tag when explain mode disabled, got: %s", output)
		}
	})
}

// TestDryRunSkipsSideEffects tests that --dry-run skips various side effects via CLI.
func TestDryRunSkipsSideEffects(t *testing.T) {
	gtBin := buildGT(t)

	// Create a temp workspace
	townRoot := t.TempDir()

	// Set up minimal workspace structure
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("create beads dir: %v", err)
	}

	// Write routes
	routes := []beads.Route{{Prefix: "bd-", Path: "."}}
	if err := beads.WriteRoutes(beadsDir, routes); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	// Create handoff marker that should NOT be removed in dry-run
	runtimeDir := filepath.Join(townRoot, constants.DirRuntime)
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatalf("create runtime dir: %v", err)
	}
	markerPath := filepath.Join(runtimeDir, constants.FileHandoffMarker)
	if err := os.WriteFile(markerPath, []byte("prev-session"), 0644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	// Run gt prime --dry-run --explain
	cmd := exec.Command(gtBin, "prime", "--dry-run", "--explain")
	cmd.Dir = townRoot
	output, _ := cmd.CombinedOutput()

	// The command may fail for other reasons (not fully configured workspace)
	// but we can check:
	// 1. Marker still exists
	if _, err := os.Stat(markerPath); os.IsNotExist(err) {
		t.Fatalf("handoff marker was removed in dry-run mode")
	}

	// 2. Output mentions skipped operations
	outputStr := string(output)
	// Check for explain output about dry-run (if workspace was valid enough to get there)
	if strings.Contains(outputStr, "bd prime") && !strings.Contains(outputStr, "skipped") {
		t.Logf("Note: output doesn't explicitly mention skipping bd prime: %s", outputStr)
	}
}

// TestIsCompactResume tests the isCompactResume detection logic including
// compaction-triggered handoff cycles (GH#1965).
func TestIsCompactResume(t *testing.T) {
	// Save and restore package-level state
	origSource := primeHookSource
	origReason := primeHandoffReason
	defer func() {
		primeHookSource = origSource
		primeHandoffReason = origReason
	}()

	cases := []struct {
		name          string
		hookSource    string
		handoffReason string
		wantCompact   bool
	}{
		{
			name:        "fresh_startup",
			hookSource:  "startup",
			wantCompact: false,
		},
		{
			name:        "compact_source",
			hookSource:  "compact",
			wantCompact: true,
		},
		{
			name:        "resume_source",
			hookSource:  "resume",
			wantCompact: true,
		},
		{
			name:        "clear_source",
			hookSource:  "clear",
			wantCompact: false,
		},
		{
			name:          "compaction_handoff_cycle",
			hookSource:    "startup",
			handoffReason: "compaction",
			wantCompact:   true,
		},
		{
			name:          "normal_handoff_not_compact",
			hookSource:    "startup",
			handoffReason: "",
			wantCompact:   false,
		},
		{
			name:          "idle_handoff_not_compact",
			hookSource:    "startup",
			handoffReason: "idle",
			wantCompact:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			primeHookSource = tc.hookSource
			primeHandoffReason = tc.handoffReason

			got := isCompactResume()
			if got != tc.wantCompact {
				t.Fatalf("isCompactResume() = %v, want %v (source=%q, reason=%q)",
					got, tc.wantCompact, tc.hookSource, tc.handoffReason)
			}
		})
	}
}

func TestHookSessionBeaconLines(t *testing.T) {
	origStructured := primeStructuredSessionStartOutput
	defer func() {
		primeStructuredSessionStartOutput = origStructured
	}()

	primeStructuredSessionStartOutput = false
	lines := hookSessionBeaconLines("abc", "startup")
	if len(lines) != 2 || lines[0] != "[session:abc]" || lines[1] != "[source:startup]" {
		t.Fatalf("hookSessionBeaconLines() = %v", lines)
	}

	primeStructuredSessionStartOutput = true
	lines = hookSessionBeaconLines("abc", "startup")
	if len(lines) != 0 {
		t.Fatalf("hookSessionBeaconLines() in structured mode = %v, want no beacon lines", lines)
	}
}

func TestFormatSessionMetadataLine(t *testing.T) {
	origStructured := primeStructuredSessionStartOutput
	defer func() { primeStructuredSessionStartOutput = origStructured }()

	primeStructuredSessionStartOutput = false
	if got := formatSessionMetadataLine("crew/quick", "sess-1"); !strings.HasPrefix(got, "[GAS TOWN] ") {
		t.Fatalf("formatSessionMetadataLine() = %q, want bracketed prefix", got)
	}

	primeStructuredSessionStartOutput = true
	if got := formatSessionMetadataLine("crew/quick", "sess-1"); strings.HasPrefix(got, "[") {
		t.Fatalf("formatSessionMetadataLine() structured = %q, should not start with '['", got)
	}
}

func TestStructuredOutputOnlyForSessionStart(t *testing.T) {
	origStructured := primeStructuredSessionStartOutput
	defer func() { primeStructuredSessionStartOutput = origStructured }()

	// Simulate a non-SessionStart hook event (e.g., Stop)
	primeStructuredSessionStartOutput = false
	input := hookInput{HookEventName: "Stop"}
	primeStructuredSessionStartOutput = input.HookEventName == "SessionStart"
	if primeStructuredSessionStartOutput {
		t.Fatal("primeStructuredSessionStartOutput should be false for HookEventName=Stop")
	}

	// Verify beacon lines are emitted (not suppressed) for non-SessionStart
	lines := hookSessionBeaconLines("abc", "startup")
	if len(lines) != 2 {
		t.Fatalf("hookSessionBeaconLines() for non-SessionStart = %v, want 2 beacon lines", lines)
	}

	// Verify metadata line retains brackets for non-SessionStart
	if got := formatSessionMetadataLine("crew/quick", "sess-1"); !strings.HasPrefix(got, "[GAS TOWN]") {
		t.Fatalf("formatSessionMetadataLine() for non-SessionStart = %q, want bracketed prefix", got)
	}
}

// TestCheckHandoffMarkerParsesReason tests that checkHandoffMarker correctly
// parses the reason field from the marker file (GH#1965).
func TestCheckHandoffMarkerParsesReason(t *testing.T) {
	// Save and restore package-level state
	origReason := primeHandoffReason
	defer func() { primeHandoffReason = origReason }()

	t.Run("marker_with_reason", func(t *testing.T) {
		primeHandoffReason = ""
		workDir := t.TempDir()

		runtimeDir := filepath.Join(workDir, constants.DirRuntime)
		if err := os.MkdirAll(runtimeDir, 0755); err != nil {
			t.Fatalf("create runtime dir: %v", err)
		}

		// Write marker with session ID and reason
		markerPath := filepath.Join(runtimeDir, constants.FileHandoffMarker)
		if err := os.WriteFile(markerPath, []byte("test-session-456\ncompaction"), 0644); err != nil {
			t.Fatalf("write marker: %v", err)
		}

		// Capture stdout (checkHandoffMarker outputs the warning)
		captureStdout(t, func() {
			checkHandoffMarker(workDir)
		})

		// Verify reason was parsed
		if primeHandoffReason != "compaction" {
			t.Fatalf("primeHandoffReason = %q, want %q", primeHandoffReason, "compaction")
		}

		// Verify marker was removed
		if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
			t.Fatalf("handoff marker was not removed")
		}
	})

	t.Run("marker_without_reason", func(t *testing.T) {
		primeHandoffReason = ""
		workDir := t.TempDir()

		runtimeDir := filepath.Join(workDir, constants.DirRuntime)
		if err := os.MkdirAll(runtimeDir, 0755); err != nil {
			t.Fatalf("create runtime dir: %v", err)
		}

		// Write marker with session ID only (backward compat)
		markerPath := filepath.Join(runtimeDir, constants.FileHandoffMarker)
		if err := os.WriteFile(markerPath, []byte("test-session-789"), 0644); err != nil {
			t.Fatalf("write marker: %v", err)
		}

		captureStdout(t, func() {
			checkHandoffMarker(workDir)
		})

		// Verify reason is empty (backward compatible)
		if primeHandoffReason != "" {
			t.Fatalf("primeHandoffReason = %q, want empty", primeHandoffReason)
		}
	})

	t.Run("no_marker", func(t *testing.T) {
		primeHandoffReason = ""
		workDir := t.TempDir()

		checkHandoffMarker(workDir)

		// Verify reason is still empty
		if primeHandoffReason != "" {
			t.Fatalf("primeHandoffReason = %q, want empty", primeHandoffReason)
		}
	})
}

// TestOutputContinuationDirective tests that the continuation directive
// outputs the expected content without the full autonomous mode block (GH#1965).
func TestOutputContinuationDirective(t *testing.T) {
	t.Run("basic_bead", func(t *testing.T) {
		bead := &beads.Issue{
			ID:    "gt-test123",
			Title: "Test bead title",
		}
		output := captureStdout(t, func() {
			outputContinuationDirective(bead, false)
		})

		// Should contain continuation directive
		if !strings.Contains(output, "CONTINUE HOOKED WORK") {
			t.Fatalf("expected 'CONTINUE HOOKED WORK' in output, got: %s", output)
		}
		if !strings.Contains(output, "gt-test123") {
			t.Fatalf("expected bead ID in output, got: %s", output)
		}

		// Should NOT contain autonomous mode language
		if strings.Contains(output, "AUTONOMOUS WORK MODE") {
			t.Fatalf("continuation directive should NOT contain 'AUTONOMOUS WORK MODE', got: %s", output)
		}
		if strings.Contains(output, "Announce:") {
			t.Fatalf("continuation directive should NOT contain 'Announce:', got: %s", output)
		}
	})

	t.Run("bead_with_molecule", func(t *testing.T) {
		bead := &beads.Issue{
			ID:    "gt-mol456",
			Title: "Molecule bead",
		}
		output := captureStdout(t, func() {
			outputContinuationDirective(bead, true)
		})

		if !strings.Contains(output, "bd mol current") {
			t.Fatalf("expected molecule hint in output, got: %s", output)
		}
	})
}

func TestCheckSlungWork_StandaloneFormulaUsesWorkflowOutput(t *testing.T) {
	ctx := RoleContext{Role: RoleCrew}
	hookedBead := &beads.Issue{
		ID:    "gt-wisp-xyz",
		Title: "Standalone formula work",
		Description: strings.Join([]string{
			"attached_formula: mol-nonexistent",
			`attached_vars: ["version=1.2.3"]`,
		}, "\n"),
	}

	var found bool
	var gotErr error
	output := captureStdout(t, func() {
		found, gotErr = checkSlungWork(ctx, hookedBead)
	})
	if gotErr != nil {
		t.Fatalf("checkSlungWork() error = %v", gotErr)
	}

	if !found {
		t.Fatalf("checkSlungWork() = false, want true")
	}
	if !strings.Contains(output, "ATTACHED FORMULA") {
		t.Fatalf("expected standalone formula hook to use workflow output, got:\n%s", output)
	}
	if strings.Contains(output, "Bead details:") {
		t.Fatalf("expected standalone formula hook to skip plain bead preview, got:\n%s", output)
	}
	if !strings.Contains(output, "--var version=1.2.3") {
		t.Fatalf("expected standalone formula context to be shown, got:\n%s", output)
	}
}

func TestCheckSlungWork_RefinerySafetyStoppedSkipsWorkflowOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock bd script uses POSIX shell")
	}
	townRoot := setupRefinerySafetyStopTown(t)
	installRefinerySafetyStopMockBD(t)

	ctx := RoleContext{Role: RoleRefinery, Rig: "testrig", TownRoot: townRoot}
	hookedBead := &beads.Issue{
		ID:    "gt-wisp-refinery",
		Title: constants.MolRefineryPatrol,
		Description: strings.Join([]string{
			"attached_formula: " + constants.MolRefineryPatrol,
			`attached_vars: ["target_branch=main"]`,
		}, "\n"),
	}

	var found bool
	var gotErr error
	output := captureStdout(t, func() {
		found, gotErr = checkSlungWork(ctx, hookedBead)
	})
	if gotErr != nil {
		t.Fatalf("checkSlungWork() error = %v", gotErr)
	}
	if !found {
		t.Fatalf("checkSlungWork() = false, want true")
	}
	if !strings.Contains(output, "REFINERY SAFETY STOP ACTIVE") {
		t.Fatalf("expected safety-stop directive, got:\n%s", output)
	}
	for _, forbidden := range []string{"ATTACHED FORMULA", "AUTONOMOUS WORK MODE", "Work through each patrol step"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("did not expect %q while safety-stopped, got:\n%s", forbidden, output)
		}
	}
}

func TestOutputStartupDirective_RefinerySafetyStoppedSkipsPatrolNew(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock bd script uses POSIX shell")
	}
	townRoot := setupRefinerySafetyStopTown(t)
	installRefinerySafetyStopMockBD(t)

	output := captureStdout(t, func() {
		outputStartupDirective(RoleContext{Role: RoleRefinery, Rig: "testrig", TownRoot: townRoot})
	})
	if !strings.Contains(output, "No patrol needed. Exit cleanly.") {
		t.Fatalf("expected safety-stop startup directive, got:\n%s", output)
	}
	if strings.Contains(output, "gt patrol new") || strings.Contains(output, "create patrol") {
		t.Fatalf("startup directive should not tell safety-stopped refinery to create patrol, got:\n%s", output)
	}
}

func TestCheckSlungWork_RalphModeUsesLoopDirective(t *testing.T) {
	configDir := t.TempDir()
	pluginsDir := filepath.Join(configDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "installed_plugins.json"), []byte(`{"plugins":{"ralph-loop@claude-plugins-official":{}}}`), 0644); err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)

	ctx := RoleContext{Role: RoleCrew}
	hookedBead := &beads.Issue{
		ID:    "gt-wisp-ralph",
		Title: "Ralph workflow",
		Description: strings.Join([]string{
			"attached_molecule: gt-wisp-ralph",
			"attached_args: do the loop",
			"mode: ralph",
		}, "\n"),
	}

	var found bool
	var gotErr error
	output := captureStdout(t, func() {
		found, gotErr = checkSlungWork(ctx, hookedBead)
	})
	if gotErr != nil {
		t.Fatalf("checkSlungWork() error = %v", gotErr)
	}
	if !found {
		t.Fatalf("checkSlungWork() = false, want true")
	}
	if !strings.Contains(output, "/ralph-loop ") || !strings.Contains(output, "--completion-promise DONE") {
		t.Fatalf("expected ralph-loop directive, got:\n%s", output)
	}
	if strings.Contains(output, "Formula Checklist") {
		t.Fatalf("ralph mode should emit plugin directive instead of inline checklist, got:\n%s", output)
	}
}

// TestCompactResumeReminder_PolecatGetsGtDone verifies that polecats get a
// gt done reminder after context compaction. This is the regression test for
// the polecats-no-gt-done bug: after long work sessions, compaction drops the
// formula checklist and the agent forgets to call gt done.
func TestCompactResumeReminder_PolecatGetsGtDone(t *testing.T) {
	ctx := RoleContext{Role: RolePolecat}
	// Simulate compact source
	primeHookSource = "compact"
	defer func() { primeHookSource = "" }()

	output := captureStdout(t, func() {
		runPrimeCompactResume(ctx)
	})

	if !strings.Contains(output, "gt done") {
		t.Fatalf("compact/resume for polecat must remind about gt done, got:\n%s", output)
	}
}

// TestCompactResumeReminder_NonPolecatNoGtDone verifies that non-polecat roles
// do NOT get the gt done reminder (it's polecat-specific).
func TestCompactResumeReminder_NonPolecatNoGtDone(t *testing.T) {
	ctx := RoleContext{Role: RoleCrew}
	primeHookSource = "compact"
	defer func() { primeHookSource = "" }()

	output := captureStdout(t, func() {
		runPrimeCompactResume(ctx)
	})

	if strings.Contains(output, "gt done") {
		t.Fatalf("compact/resume for non-polecat should NOT mention gt done, got:\n%s", output)
	}
}

func TestEnsureBeadsRedirect_WitnessCreatesRedirect(t *testing.T) {
	townRoot := t.TempDir()
	rigRoot := filepath.Join(townRoot, "testrig")
	witnessDir := filepath.Join(rigRoot, "witness")
	mayorBeadsDir := filepath.Join(rigRoot, "mayor", "rig", ".beads")
	if err := os.MkdirAll(witnessDir, 0755); err != nil {
		t.Fatalf("mkdir witness dir: %v", err)
	}
	if err := os.MkdirAll(mayorBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir mayor beads dir: %v", err)
	}

	ctx := RoleContext{
		Role:     RoleWitness,
		WorkDir:  witnessDir,
		TownRoot: townRoot,
	}

	ensureBeadsRedirect(ctx)

	redirectPath := filepath.Join(witnessDir, ".beads", "redirect")
	content, err := os.ReadFile(redirectPath)
	if err != nil {
		t.Fatalf("read redirect: %v", err)
	}
	if got, want := string(content), "../mayor/rig/.beads\n"; got != want {
		t.Fatalf("redirect content = %q, want %q", got, want)
	}
}

func TestEnsureBeadsRedirect_RepairsExistingRedirectChain(t *testing.T) {
	townRoot := t.TempDir()
	rigRoot := filepath.Join(townRoot, "testrig")
	rigBeadsDir := filepath.Join(rigRoot, ".beads")
	mayorBeadsDir := filepath.Join(rigRoot, "mayor", "rig", ".beads")
	workDir := filepath.Join(rigRoot, "polecats", "worker1", "testrig")
	workBeadsDir := filepath.Join(workDir, ".beads")

	if err := os.MkdirAll(mayorBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir mayor beads dir: %v", err)
	}
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir rig beads dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "redirect"), []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "metadata.json"), []byte(`{"dolt_database":"hq","backend":"dolt"}`), 0644); err != nil {
		t.Fatalf("write rig metadata: %v", err)
	}
	if err := os.MkdirAll(workBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir work beads dir: %v", err)
	}

	// Old polecat worktrees can keep this bd-incompatible chain:
	// worktree/.beads -> rig/.beads -> mayor/rig/.beads.
	redirectPath := filepath.Join(workBeadsDir, "redirect")
	if err := os.WriteFile(redirectPath, []byte("../../../.beads\n"), 0644); err != nil {
		t.Fatalf("write stale redirect: %v", err)
	}

	ctx := RoleContext{
		Role:     RolePolecat,
		WorkDir:  workDir,
		TownRoot: townRoot,
	}

	ensureBeadsRedirect(ctx)

	content, err := os.ReadFile(redirectPath)
	if err != nil {
		t.Fatalf("read redirect: %v", err)
	}
	if got, want := string(content), "../../../mayor/rig/.beads\n"; got != want {
		t.Fatalf("redirect content = %q, want %q", got, want)
	}
}

func TestOutputRalphLoopDirective_PluginInstalled(t *testing.T) {
	attachment := &beads.AttachmentFields{
		Mode:            "ralph",
		AttachedFormula: "mol-polecat-work",
		AttachedArgs:    "Run story audit, fix worst gap, commit, loop",
		FormulaVars:     "base_branch=main",
	}
	var gotErr error
	output := captureStdout(t, func() {
		gotErr = outputRalphLoopDirectiveWithPluginCheck(RoleContext{}, attachment, true, t.TempDir())
	})
	if gotErr != nil {
		t.Fatalf("outputRalphLoopDirectiveWithPluginCheck: %v", gotErr)
	}

	if !strings.Contains(output, "/ralph-loop ") {
		t.Fatalf("expected /ralph-loop command, got:\n%s", output)
	}
	if !strings.Contains(output, "--completion-promise DONE") {
		t.Fatalf("expected completion promise flag, got:\n%s", output)
	}
	if !strings.Contains(output, "Formula Checklist") {
		t.Fatalf("expected rendered formula checklist in prompt, got:\n%s", output)
	}
	if !strings.Contains(output, "story audit") {
		t.Fatalf("expected attached args in prompt, got:\n%s", output)
	}
	if !strings.Contains(output, `<promise>DONE</promise>`) {
		t.Fatalf("expected DONE promise instruction in prompt, got:\n%s", output)
	}
}

func TestOutputRalphLoopDirective_PluginMissing(t *testing.T) {
	var gotErr error
	output := captureStdout(t, func() {
		gotErr = outputRalphLoopDirectiveWithPluginCheck(RoleContext{}, &beads.AttachmentFields{Mode: "ralph"}, false, "/tmp/claude-test")
	})
	if gotErr == nil {
		t.Fatal("expected missing plugin error")
	}
	if !strings.Contains(gotErr.Error(), ralphLoopPluginID) || !strings.Contains(gotErr.Error(), "/plugin install") {
		t.Fatalf("missing plugin error not actionable: %v", gotErr)
	}
	if strings.Contains(output, "/ralph-loop") {
		t.Fatalf("should not emit /ralph-loop when plugin is missing, got:\n%s", output)
	}
}

func TestRalphLoopPluginInstalledIn(t *testing.T) {
	pluginsDir := filepath.Join(t.TempDir(), "plugins")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	manifestPath := filepath.Join(pluginsDir, "installed_plugins.json")
	if err := os.WriteFile(manifestPath, []byte(`{"plugins":{"ralph-loop@claude-plugins-official":{},"ralph-loop-evil@claude-plugins-official":{}}}`), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	installed, err := ralphLoopPluginInstalledIn(manifestPath)
	if err != nil {
		t.Fatalf("ralphLoopPluginInstalledIn: %v", err)
	}
	if !installed {
		t.Fatal("expected canonical ralph-loop plugin to be detected")
	}

	if err := os.WriteFile(manifestPath, []byte(`{"plugins":{"ralph-loop-evil@claude-plugins-official":{}}}`), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	installed, err = ralphLoopPluginInstalledIn(manifestPath)
	if err != nil {
		t.Fatalf("ralphLoopPluginInstalledIn malicious key: %v", err)
	}
	if installed {
		t.Fatal("must not accept prefix-like plugin IDs")
	}
}

func TestIsRalphLoopPluginInstalledUsesClaudeConfigDir(t *testing.T) {
	configDir := t.TempDir()
	pluginsDir := filepath.Join(configDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "installed_plugins.json"), []byte(`{"plugins":{"ralph-loop@claude-plugins-official":{}}}`), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)

	installed, gotConfigDir, err := isRalphLoopPluginInstalled()
	if err != nil {
		t.Fatalf("isRalphLoopPluginInstalled: %v", err)
	}
	if !installed {
		t.Fatal("expected plugin installed via CLAUDE_CONFIG_DIR")
	}
	if gotConfigDir != configDir {
		t.Fatalf("configDir = %q, want %q", gotConfigDir, configDir)
	}
}

func TestQuoteForRalphLoop(t *testing.T) {
	quoted := quoteForRalphLoop("line1\r\nline2 \"quoted\" \\ $HOME `cmd`")
	if !strings.HasPrefix(quoted, `"`) || !strings.HasSuffix(quoted, `"`) {
		t.Fatalf("expected double-quoted prompt, got %q", quoted)
	}
	for _, forbidden := range []string{"\n", "\r"} {
		if strings.Contains(quoted, forbidden) {
			t.Fatalf("quoted prompt contains raw line break %q: %q", forbidden, quoted)
		}
	}
	for _, want := range []string{`\n`, `\"`, `\\`, `\$`, "\\`"} {
		if !strings.Contains(quoted, want) {
			t.Fatalf("quoted prompt missing escape %q: %q", want, quoted)
		}
	}
}

func TestIsBeadNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"no issue found lowercase", errors.New("bd: no issue found"), true},
		{"No issue found cased", errors.New("ERROR: No issue found in DB"), true},
		{"not found", errors.New("error: not found in beads"), true},
		{"issue not found phrasing", errors.New("rpc: issue not found"), true},
		{"connection refused", errors.New("dial tcp: connection refused"), false},
		{"random error", errors.New("schema mismatch"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBeadNotFound(tt.err); got != tt.want {
				t.Errorf("isBeadNotFound(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestErrHookUnresolvable_IsErrors(t *testing.T) {
	wrapped := fmt.Errorf("%w: agent=foo hook_bead=hq-igrp", ErrHookUnresolvable)
	if !errors.Is(wrapped, ErrHookUnresolvable) {
		t.Fatalf("errors.Is should report wrapped err matches ErrHookUnresolvable")
	}
}
