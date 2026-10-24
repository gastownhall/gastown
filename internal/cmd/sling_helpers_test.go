package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/channelevents"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/workspace"
)

func setupSlingTestRegistry(t *testing.T) {
	t.Helper()
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("bd", "beads")
	reg.Register("mp", "my-project")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

// TestNudgeRefinerySessionName verifies that nudgeRefinery constructs the
// correct tmux session name ({prefix}-refinery) and passes the message.
func TestNudgeRefinerySessionName(t *testing.T) {
	setupSlingTestRegistry(t)
	logPath := filepath.Join(t.TempDir(), "nudge.log")
	t.Setenv("GT_TEST_NUDGE_LOG", logPath)

	tests := []struct {
		name        string
		rigName     string
		message     string
		wantSession string
	}{
		{
			name:        "simple rig name",
			rigName:     "gastown",
			message:     "MERGE_READY received - check inbox for pending work",
			wantSession: "gt-refinery",
		},
		{
			name:        "hyphenated rig name",
			rigName:     "my-project",
			message:     "MERGE_READY received - check inbox for pending work",
			wantSession: "mp-refinery",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Truncate log for each subtest
			if err := os.WriteFile(logPath, nil, 0644); err != nil {
				t.Fatalf("truncate log: %v", err)
			}

			nudgeRefinery(tt.rigName, tt.message)

			logBytes, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read log: %v", err)
			}
			logContent := string(logBytes)

			// Verify session name
			wantPrefix := "nudge:" + tt.wantSession + ":"
			if !strings.Contains(logContent, wantPrefix) {
				t.Errorf("nudgeRefinery(%q) session = got log %q, want prefix %q",
					tt.rigName, logContent, wantPrefix)
			}

			// Verify message is passed through
			if !strings.Contains(logContent, tt.message) {
				t.Errorf("nudgeRefinery() message not found in log: got %q, want %q",
					logContent, tt.message)
			}
		})
	}
}

// TestWakeRigAgentsDoesNotNudgeRefinery verifies that wakeRigAgents only
// nudges the witness, not the refinery. The refinery should only be nudged
// when an MR is actually created (via nudgeRefinery), not at polecat dispatch time.
func TestWakeRigAgentsDoesNotNudgeRefinery(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "nudge.log")
	t.Setenv("GT_TEST_NUDGE_LOG", logPath)

	// wakeRigAgents calls exec.Command("gt", "rig", "boot", ...) and tmux.NudgeSession.
	// The boot command and witness nudge will fail silently (no real rig/tmux).
	// We only care that nudgeRefinery is NOT called (no log entries).
	wakeRigAgents("testrig")

	// Check that no refinery nudge was logged
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		// File doesn't exist = no nudges logged = correct
		return
	}
	if strings.Contains(string(logBytes), "refinery") {
		t.Errorf("wakeRigAgents() should not nudge refinery, but log contains: %s", string(logBytes))
	}
}

// TestNudgeRefineryNoOpWithoutLog verifies that nudgeRefinery doesn't panic
// or error when the test log env var is unset.
//
// This test used to clear GT_TEST_NUDGE_LOG "to exercise the real tmux path",
// which made every `go test ./internal/cmd/...` run emit a live MQ_SUBMIT
// event into the town-global refinery channel and send real tmux keys
// (gt-dog). Two things now prevent that, and this test asserts both:
//
//  1. nudgeRefinery is inert under testmode.Active(), so the side effects
//     never start; and
//  2. the town root is isolated to a temp dir, so even an unguarded emit
//     could not reach production.
//
// The no-panic behaviour the test was written to check is still covered.
func TestNudgeRefineryNoOpWithoutLog(t *testing.T) {
	setupSlingTestRegistry(t)
	townRoot := newIsolatedTownRoot(t)

	// Ensure the log hook is NOT set: the inert-by-default guard, not the
	// hook, is what must keep this call from touching shared state.
	t.Setenv("GT_TEST_NUDGE_LOG", "")

	// Should not panic even though no tmux session exists.
	nudgeRefinery("nonexistent-rig", "test message")

	assertNoEventsEmitted(t, townRoot)
}

// TestNudgeHelpersEmitNoEventsInTests is the regression test for gt-dog: the
// nudge helpers must not write into any town's event channels when running
// under the test suite, even with a fully-formed workspace reachable from the
// working directory and no GT_TEST_NUDGE_LOG hook set.
func TestNudgeHelpersEmitNoEventsInTests(t *testing.T) {
	setupSlingTestRegistry(t)
	townRoot := newIsolatedTownRoot(t)
	t.Setenv("GT_TEST_NUDGE_LOG", "")

	// Sanity check: the isolated town really is discoverable, so a missing
	// guard would produce events here rather than silently finding nothing.
	found, err := workspace.FindFromCwd()
	if err != nil || found != townRoot {
		t.Fatalf("workspace.FindFromCwd() = %q, %v; want %q — test fixture is not a discoverable town", found, err, townRoot)
	}

	nudgeRefinery("nonexistent-rig", "test message")
	nudgeWitness("nonexistent-rig", "test message")

	assertNoEventsEmitted(t, townRoot)
}

// TestEmitToTownRefusesLiveTownInTests covers the backstop: even if a caller
// forgets the testmode guard, channelevents refuses to write into the town
// root this test binary was launched from.
func TestEmitToTownRefusesLiveTownInTests(t *testing.T) {
	liveRoot, err := workspace.FindFromCwd()
	if err != nil || liveRoot == "" {
		t.Skip("skipping: not running inside a Gas Town workspace")
	}

	if _, err := channelevents.EmitToTown(liveRoot, "refinery", "MQ_SUBMIT", []string{"source=sling"}); !errors.Is(err, channelevents.ErrLiveTownInTest) {
		t.Fatalf("EmitToTown(live town root) error = %v; want ErrLiveTownInTest", err)
	}
}

// newIsolatedTownRoot creates a throwaway Gas Town workspace, makes it the
// working directory, and clears the env vars that would otherwise let
// workspace resolution escape to the real town. Returns the town root.
func newIsolatedTownRoot(t *testing.T) string {
	t.Helper()

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("creating mayor dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, workspace.PrimaryMarker), []byte("{}"), 0644); err != nil {
		t.Fatalf("writing town marker: %v", err)
	}

	t.Setenv("GT_TOWN_ROOT", townRoot)
	t.Setenv("GT_ROOT", townRoot)
	t.Chdir(townRoot)

	// t.TempDir() can hand back a symlinked path (/tmp -> /private/tmp on
	// macOS); workspace.Find deliberately does not resolve symlinks, so
	// compare against what the process actually sees.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getting working directory: %v", err)
	}
	return cwd
}

// assertNoEventsEmitted fails if any channel event file exists under townRoot.
func assertNoEventsEmitted(t *testing.T, townRoot string) {
	t.Helper()

	eventsDir := filepath.Join(townRoot, "events")
	entries, err := os.ReadDir(eventsDir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("reading %s: %v", eventsDir, err)
	}
	if len(entries) > 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("test emitted channel events into %s: %v", eventsDir, names)
	}
}

func TestIsDeferredBead(t *testing.T) {
	tests := []struct {
		name string
		info *beadInfo
		want bool
	}{
		{"open bead is not deferred", &beadInfo{Status: "open", Description: "some task"}, false},
		{"in_progress bead is not deferred", &beadInfo{Status: "in_progress", Description: "working on it"}, false},
		{"deferred status", &beadInfo{Status: "deferred", Description: "some task"}, true},
		{"description says deferred to post-launch", &beadInfo{Status: "open", Description: "deferred to post-launch"}, true},
		{"description says deferred to post launch", &beadInfo{Status: "open", Description: "deferred to post launch"}, true},
		{"description says status: deferred", &beadInfo{Status: "open", Description: "status: deferred\nsome other notes"}, true},
		{"case insensitive description", &beadInfo{Status: "open", Description: "Deferred to Post-Launch"}, true},
		{"deferred keyword not in deferral phrase", &beadInfo{Status: "open", Description: "the user deferred this action"}, false},
		{"empty description", &beadInfo{Status: "open", Description: ""}, false},
		{"hooked bead not deferred", &beadInfo{Status: "hooked", Description: "some work"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDeferredBead(tt.info); got != tt.want {
				t.Errorf("isDeferredBead(%+v) = %v, want %v", tt.info, got, tt.want)
			}
		})
	}
}

func TestCollectExistingMoleculesFiltersClosedMolecules(t *testing.T) {
	tests := []struct {
		name string
		info *beadInfo
		want []string
	}{
		{
			name: "open molecule is collected",
			info: &beadInfo{
				Dependencies: []beads.IssueDep{
					{ID: "bd-wisp-abc", Status: "open"},
				},
			},
			want: []string{"bd-wisp-abc"},
		},
		{
			name: "closed molecule is skipped",
			info: &beadInfo{
				Dependencies: []beads.IssueDep{
					{ID: "bd-wisp-abc", Status: "closed"},
				},
			},
			want: nil,
		},
		{
			name: "tombstone molecule is skipped",
			info: &beadInfo{
				Dependencies: []beads.IssueDep{
					{ID: "bd-wisp-abc", Status: "tombstone"},
				},
			},
			want: nil,
		},
		{
			name: "mixed: open kept, closed skipped",
			info: &beadInfo{
				Dependencies: []beads.IssueDep{
					{ID: "bd-wisp-dead", Status: "closed"},
					{ID: "bd-wisp-live", Status: "in_progress"},
				},
			},
			want: []string{"bd-wisp-live"},
		},
		{
			name: "non-wisp dependency ignored regardless of status",
			info: &beadInfo{
				Dependencies: []beads.IssueDep{
					{ID: "bd-regular-dep", Status: "open"},
				},
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collectExistingMolecules(tt.info)
			if len(got) != len(tt.want) {
				t.Fatalf("collectExistingMolecules() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("collectExistingMolecules()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestCollectExistingMoleculeDepsReadsCanonicalWispEdges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix shell script bd stub")
	}

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	script := `#!/bin/sh
echo "$*" >> "${BD_LOG}"
if [ "$1" = "sql" ]; then
  case "$2" in
    *wisp_dependencies*depends_on_issue_id*depends_on_wisp_id*)
      echo '[{"issue_id":"gt-wisp-live"},{"issue_id":"gt-wisp-live"},{"issue_id":"gt-wisp-other"}]'
      exit 0
      ;;
  esac
  echo 'unexpected query' >&2
  exit 1
fi
exit 1
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_LOG", logPath)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(binDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	got, err := collectExistingMoleculeDeps("gt-work", "")
	if err != nil {
		t.Fatalf("collectExistingMoleculeDeps: %v", err)
	}
	want := []string{"gt-wisp-live", "gt-wisp-other"}
	if len(got) != len(want) {
		t.Fatalf("collectExistingMoleculeDeps() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("collectExistingMoleculeDeps()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestIsSlingConfigError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"not initialized", fmt.Errorf("database not initialized"), true},
		{"no such table", fmt.Errorf("no such table: issues"), true},
		{"table not found", fmt.Errorf("table not found: issues"), true},
		{"issue_prefix missing", fmt.Errorf("issue_prefix not configured"), true},
		{"no database", fmt.Errorf("no database found"), true},
		{"database not found", fmt.Errorf("database not found"), true},
		{"connection refused", fmt.Errorf("connection refused"), true},
		{"circuit breaker", fmt.Errorf("Dolt circuit breaker is open: server appears down"), true},
		{"server appears down", fmt.Errorf("server appears down"), true},
		{"server down", fmt.Errorf("server down"), true},
		{"server not running", fmt.Errorf("Dolt server is not running"), true},
		{"server may not be running", fmt.Errorf("Dolt server may not be running"), true},
		{"transient error", fmt.Errorf("optimistic lock failed"), false},
		{"generic error", fmt.Errorf("something else"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSlingConfigError(tt.err); got != tt.want {
				t.Errorf("isSlingConfigError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestHookBeadWithRetryFailsFastOnBdStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix shell script bd stub")
	}
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)

	binDir := t.TempDir()
	countPath := filepath.Join(binDir, "count")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--allow-stale" ]; then
  echo "Error: unknown flag: --allow-stale" >&2
  exit 0
fi
count=0
if [ -f %[1]q ]; then count=$(cat %[1]q); fi
count=$((count + 1))
printf '%%s' "$count" > %[1]q
echo "Dolt circuit breaker is open: server appears down" >&2
exit 1
`, countPath)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1")

	err := hookBeadWithRetry("gt-work", "gastown/polecats/rust", t.TempDir())
	if err == nil {
		t.Fatal("hookBeadWithRetry error = nil, want fail-fast error")
	}
	if !strings.Contains(err.Error(), "Dolt circuit breaker is open") {
		t.Fatalf("error missing bd stderr: %v", err)
	}
	if !strings.Contains(err.Error(), "Safe next action") {
		t.Fatalf("error missing reconciliation guidance: %v", err)
	}
	countBytes, readErr := os.ReadFile(countPath)
	if readErr != nil {
		t.Fatalf("read count: %v", readErr)
	}
	if got := strings.TrimSpace(string(countBytes)); got != "1" {
		t.Fatalf("bd update invoked %s times, want 1", got)
	}
}
