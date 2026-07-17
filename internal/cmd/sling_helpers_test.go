package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/session"
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

func TestUpdateAgentModeRoutesPrefixedPolecatToRig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX bd stub")
	}

	townRoot := t.TempDir()
	hqBeadsDir := filepath.Join(townRoot, ".beads")
	rigDir := filepath.Join(townRoot, "gastown")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	for _, dir := range []string{
		filepath.Join(townRoot, "mayor"),
		hqBeadsDir,
		rigBeadsDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write town marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hqBeadsDir, "routes.jsonl"), []byte(`{"prefix":"gt-","path":"gastown"}
{"prefix":"hq-","path":"."}
`), 0o644); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -eu
printf 'BEADS_DIR=%s %s\n' "$BEADS_DIR" "$*" >> "$BD_LOG"
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done
case "$cmd" in
  version) exit 0 ;;
  show) printf '[{"id":"gt-gastown-polecat-nux","title":"Nux","status":"open","description":""}]\n' ;;
  update)
    body=$(cat)
    case "$body" in
      *"mode: ralph"*) printf 'mode=ralph\n' >> "$BD_LOG" ;;
      *) printf 'mode=missing\n' >> "$BD_LOG" ;;
    esac
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir town root: %v", err)
	}

	updateAgentMode("gastown/polecats/nux", "ralph", "", "")

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	logOutput := string(log)
	resolvedRigBeadsDir, err := filepath.EvalSymlinks(rigBeadsDir)
	if err != nil {
		t.Fatalf("resolve rig beads directory: %v", err)
	}
	resolvedHQBeadsDir, err := filepath.EvalSymlinks(hqBeadsDir)
	if err != nil {
		t.Fatalf("resolve HQ beads directory: %v", err)
	}
	if !strings.Contains(logOutput, "BEADS_DIR="+resolvedRigBeadsDir+" update gt-gastown-polecat-nux") {
		t.Fatalf("mode update did not target rig beads:\n%s", logOutput)
	}
	if strings.Contains(logOutput, "BEADS_DIR="+resolvedHQBeadsDir+" update gt-gastown-polecat-nux") {
		t.Fatalf("mode update targeted HQ beads:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "mode=ralph") {
		t.Fatalf("ralph mode metadata was not written:\n%s", logOutput)
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
// or error when called without the test log env var and without a real tmux session.
// The tmux NudgeSession call should fail silently.
func TestNudgeRefineryNoOpWithoutLog(t *testing.T) {
	// Ensure test log is NOT set so we exercise the real tmux path
	t.Setenv("GT_TEST_NUDGE_LOG", "")

	// Should not panic even though no tmux session exists
	nudgeRefinery("nonexistent-rig", "test message")
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
