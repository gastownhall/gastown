package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func writeExternalTrackingBdStub(t *testing.T, scriptBody string) {
	t.Helper()

	binDir := t.TempDir()
	bdPath := filepath.Join(binDir, "bd")
	script := "#!/bin/sh\n" + scriptBody
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func chdirExternalTrackingTest(t *testing.T, dir string) {
	t.Helper()

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
}

func makeExternalTrackingTownWorkspace(t *testing.T) (string, string, string) {
	t.Helper()

	townRoot := t.TempDir()
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test-town"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}

	expectedWD := townRoot
	if resolved, err := filepath.EvalSymlinks(townRoot); err == nil && resolved != "" {
		expectedWD = resolved
	}
	return townRoot, townBeads, expectedWD
}

func TestGetTrackedIssues_FallsBackToShowTrackedDependencies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	townRoot, townBeads, _ := makeExternalTrackingTownWorkspace(t)
	chdirExternalTrackingTest(t, townRoot)

	scriptBody := fmt.Sprintf(`
case "$*" in
  "--allow-stale version")
    exit 0
    ;;
  "dep list hq-cv-ext --direction=down --type=tracks --allow-stale --json")
    echo '[]'
    ;;
  "show hq-cv-ext --json")
    echo '[{"id":"hq-cv-ext","title":"External convoy","status":"open","issue_type":"convoy","dependencies":[{"id":"external:ghostty:ghostty-123","title":"Ghost 123","status":"open","type":"task","dependency_type":"tracks"},{"id":"external:ghostty:ghostty-456","title":"Ghost 456","status":"closed","type":"task","dependency_type":"tracks"},{"id":"gt-ignore","title":"Ignore me","status":"open","type":"task","dependency_type":"blocks"}]}]'
    ;;
  "--allow-stale show ghostty-123 --json")
    echo '[{"id":"ghostty-123","title":"Ghost 123","status":"open","issue_type":"task"}]'
    ;;
  "--allow-stale show ghostty-456 --json")
    echo '[{"id":"ghostty-456","title":"Ghost 456","status":"closed","issue_type":"task"}]'
    ;;
  *)
    echo "unexpected bd args: $*" >&2
    exit 1
    ;;
esac
`)
	writeExternalTrackingBdStub(t, scriptBody)

	tracked, err := getTrackedIssues(townBeads, "hq-cv-ext")
	if err != nil {
		t.Fatalf("getTrackedIssues: %v", err)
	}
	if len(tracked) != 2 {
		t.Fatalf("expected 2 tracked issues, got %d", len(tracked))
	}

	ids := []string{tracked[0].ID, tracked[1].ID}
	sort.Strings(ids)
	if ids[0] != "ghostty-123" || ids[1] != "ghostty-456" {
		t.Fatalf("unexpected tracked IDs: %v", ids)
	}

	statusByID := map[string]string{}
	for _, item := range tracked {
		statusByID[item.ID] = item.Status
	}
	if statusByID["ghostty-123"] != "open" || statusByID["ghostty-456"] != "closed" {
		t.Fatalf("unexpected tracked statuses: %#v", statusByID)
	}
}

// TestGetTrackedIssues_UnknownStatusForUnreachableCrossRig verifies the (gt-bs6)
// contract: when the tracked bead lives in a cross-rig DB that cannot be
// resolved from the convoy owner's cwd (routes.jsonl missing, rig parked, or
// rig beads DB unreachable), the returned tracked entry carries status
// trackedStatusUnknown instead of an empty string. Empty status was
// indistinguishable from a legitimately open bead and silenced the real
// failure mode noted in #2786.
func TestGetTrackedIssues_UnknownStatusForUnreachableCrossRig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	townRoot, townBeads, _ := makeExternalTrackingTownWorkspace(t)
	chdirExternalTrackingTest(t, townRoot)

	// bd sql returns a single cross-rig tracks edge. `bd show` fails for the
	// target bead (simulating an unreachable / unrouted rig DB). The function
	// must still return the tracked dep, with Status = trackedStatusUnknown.
	scriptBody := `
case "$*" in
  "--allow-stale version")
    exit 0
    ;;
  *sql*dependencies*)
    echo '[{"depends_on_id":"ws-foo"}]'
    ;;
  "show ws-foo --json")
    echo "no issue found matching \"ws-foo\"" >&2
    exit 1
    ;;
  *)
    echo "unexpected bd args: $*" >&2
    exit 1
    ;;
esac
`
	writeExternalTrackingBdStub(t, scriptBody)

	tracked, err := getTrackedIssues(townBeads, "hq-cv-unreach")
	if err != nil {
		t.Fatalf("getTrackedIssues: %v", err)
	}
	if len(tracked) != 1 {
		t.Fatalf("expected 1 tracked issue, got %d: %#v", len(tracked), tracked)
	}
	if tracked[0].ID != "ws-foo" {
		t.Fatalf("tracked[0].ID = %q, want %q", tracked[0].ID, "ws-foo")
	}
	if tracked[0].Status != trackedStatusUnknown {
		t.Fatalf("tracked[0].Status = %q, want %q", tracked[0].Status, trackedStatusUnknown)
	}
}

// TestCloseConvoyIfComplete_UnknownBlocksAutoClose verifies (gt-bs6) that an
// unknown-status tracked bead prevents convoy auto-close. The rig DB being
// temporarily unreachable must not be mistaken for a completed bead.
func TestCloseConvoyIfComplete_UnknownBlocksAutoClose(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	// No bd stub — closeConvoyIfComplete does not shell out when the convoy
	// isn't closable, which is exactly the scenario under test.
	townBeads := t.TempDir()
	tracked := []trackedIssueInfo{
		{ID: "ws-foo", Status: trackedStatusUnknown},
		{ID: "ws-bar", Status: "closed"},
	}

	out, err := captureConvoyStdoutErr(t, func() error {
		ready, err := closeConvoyIfComplete(townBeads, "hq-cv-unreach", "Mixed", tracked, false)
		if ready {
			t.Fatalf("closeConvoyIfComplete reported ready with unknown tracked status")
		}
		return err
	})
	if err != nil {
		t.Fatalf("closeConvoyIfComplete: %v", err)
	}
	if !strings.Contains(out, "unknown") {
		t.Fatalf("diagnostic missing 'unknown' label: %q", out)
	}
}

// TestGetIssueDetailsBatch_CrossRigRouting is the regression test for GH#3681.
//
// When townRoot has a local .beads/, the old exec.Command("bd", ...) ran from
// townRoot and bd bound to the town's .beads/, ignoring routes.jsonl. The new
// Beads.Show path calls ResolveRoutingTarget internally, which reads routes.jsonl
// and dispatches to the correct rig database regardless of cwd.
func TestGetIssueDetailsBatch_CrossRigRouting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	townRoot, townBeads, _ := makeExternalTrackingTownWorkspace(t)

	// routes.jsonl routes "gm-" beads to a gemba rig subdirectory.
	routes := `{"prefix":"gm-","path":"gemba"}` + "\n"
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gemba", ".beads"), 0755); err != nil {
		t.Fatalf("mkdir gemba/.beads: %v", err)
	}

	// cwd = townRoot, which also has .beads/ — the problematic layout from GH#3681.
	chdirExternalTrackingTest(t, townRoot)

	// The stub only responds to the routed form. Old code called
	// "bd show gm-abc --json" (no --allow-stale) from townRoot, which the
	// stub does not handle → exits 1 → result is empty. New code routes via
	// Beads.Show and calls "--allow-stale show gm-abc --json" from the rig dir.
	scriptBody := `
case "$*" in
  "--allow-stale version")
    exit 0
    ;;
  "--allow-stale show gm-abc --json")
    echo '[{"id":"gm-abc","title":"Gemba task","status":"closed","issue_type":"task"}]'
    ;;
  *)
    echo "unexpected bd args: $*" >&2
    exit 1
    ;;
esac
`
	writeExternalTrackingBdStub(t, scriptBody)

	result := getIssueDetailsBatch([]string{"gm-abc"})
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d: %#v", len(result), result)
	}
	if result["gm-abc"] == nil {
		t.Fatal("gm-abc missing from result")
	}
	if result["gm-abc"].Status != "closed" {
		t.Fatalf("expected status 'closed', got %q", result["gm-abc"].Status)
	}
}
