package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func captureConvoyStdoutErr(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	runErr := fn()

	_ = w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy stdout: %v", err)
	}
	_ = r.Close()

	return buf.String(), runErr
}

func writeRoutingBdStub(t *testing.T, scriptBody string) {
	t.Helper()

	binDir := t.TempDir()
	bdPath := filepath.Join(binDir, "bd")
	script := "#!/bin/sh\n" + scriptBody
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func chdirConvoyTest(t *testing.T, dir string) {
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

func makeRoutingTownWorkspace(t *testing.T) (string, string) {
	t.Helper()

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
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
	return townRoot, expectedWD
}

func TestRunConvoyList_UsesTownRootAndStripsBeadsDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	townRoot, expectedWD := makeRoutingTownWorkspace(t)
	chdirConvoyTest(t, townRoot)
	t.Setenv("BEADS_DIR", "/wrong/.beads")

	scriptBody := fmt.Sprintf(`
# Allow-stale version probe is exempt from BEADS_DIR check.
if [ "$*" = "--allow-stale version" ]; then
  exit 0
fi

if [ "$BEADS_DIR" != "%s/.beads" ]; then
  echo "expected hardened BEADS_DIR, got $BEADS_DIR" >&2
  exit 1
fi

case "$*" in
	  "list --label=gt:convoy --json --limit=0 --all --flat")
	    if [ "$PWD" != "%s" ]; then
	      echo "expected town root, got $PWD" >&2
	      exit 1
	    fi
	    echo '[{"id":"hq-cv-town","title":"Town convoy","status":"open","created_at":"2026-03-09T00:00:00Z","labels":["gt:convoy"]}]'
	    ;;
	  "list --json --limit=0 --all --flat")
	    echo '[]'
	    ;;
  "dep list hq-cv-town --direction=down --type=tracks --allow-stale --json")
    if [ "$PWD" != "%s" ]; then
      echo "expected town root, got $PWD" >&2
      exit 1
    fi
    echo '[]'
    ;;
  "show hq-cv-town --json")
    if [ "$PWD" != "%s" ]; then
      echo "expected town root, got $PWD" >&2
      exit 1
    fi
    echo '[{"id":"hq-cv-town","title":"Town convoy","status":"open","issue_type":"convoy","dependencies":[]}]'
    ;;
  *)
    echo "unexpected bd args: $*" >&2
    exit 1
    ;;
esac
`, expectedWD, expectedWD, expectedWD, expectedWD)
	writeRoutingBdStub(t, scriptBody)

	oldJSON, oldAll, oldStatus, oldTree := convoyListJSON, convoyListAll, convoyListStatus, convoyListTree
	convoyListJSON = true
	convoyListAll = true
	convoyListStatus = ""
	convoyListTree = false
	t.Cleanup(func() {
		convoyListJSON = oldJSON
		convoyListAll = oldAll
		convoyListStatus = oldStatus
		convoyListTree = oldTree
	})

	out, err := captureConvoyStdoutErr(t, func() error {
		return runConvoyList(nil, nil)
	})
	if err != nil {
		t.Fatalf("runConvoyList: %v", err)
	}
	if !strings.Contains(out, `"id": "hq-cv-town"`) {
		t.Fatalf("expected convoy JSON output, got:\n%s", out)
	}
}

func TestRunConvoyStatus_UsesTownRootAndStripsBeadsDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	townRoot, expectedWD := makeRoutingTownWorkspace(t)
	chdirConvoyTest(t, townRoot)
	t.Setenv("BEADS_DIR", "/wrong/.beads")

	scriptBody := fmt.Sprintf(`
# Allow-stale version probe is exempt from BEADS_DIR check.
if [ "$*" = "--allow-stale version" ]; then
  exit 0
fi

if [ "$BEADS_DIR" != "%s/.beads" ]; then
  echo "expected hardened BEADS_DIR, got $BEADS_DIR" >&2
  exit 1
fi

case "$*" in
  "show hq-cv-status --json")
    if [ "$PWD" != "%s" ]; then
      echo "expected town root, got $PWD" >&2
      exit 1
    fi
    echo '[{"id":"hq-cv-status","title":"Status convoy","status":"open","issue_type":"convoy","created_at":"2026-03-09T00:00:00Z","labels":[],"dependencies":[]}]'
    ;;
  "dep list hq-cv-status --direction=down --type=tracks --allow-stale --json")
    if [ "$PWD" != "%s" ]; then
      echo "expected town root, got $PWD" >&2
      exit 1
    fi
    echo '[]'
    ;;
  *)
    echo "unexpected bd args: $*" >&2
    exit 1
    ;;
esac
`, expectedWD, expectedWD, expectedWD)
	writeRoutingBdStub(t, scriptBody)

	oldJSON := convoyStatusJSON
	convoyStatusJSON = false
	t.Cleanup(func() { convoyStatusJSON = oldJSON })

	out, err := captureConvoyStdoutErr(t, func() error {
		return runConvoyStatus(nil, []string{"hq-cv-status"})
	})
	if err != nil {
		t.Fatalf("runConvoyStatus: %v", err)
	}
	if !strings.Contains(out, "hq-cv-status") || !strings.Contains(out, "Progress:  0/0 completed") {
		t.Fatalf("unexpected status output:\n%s", out)
	}
}

func TestGetTrackedIssues_RoutesShowByPrefix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	townRoot, expectedTownWD := makeRoutingTownWorkspace(t)
	chdirConvoyTest(t, townRoot)
	t.Setenv("BEADS_DIR", "/wrong/.beads")

	routes := `{"prefix":"hq-","path":"."}
{"prefix":"ws-","path":"worker/.beads"}
`
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "worker", ".beads"), 0755); err != nil {
		t.Fatalf("mkdir worker/.beads: %v", err)
	}

	expectedWorkerWD := filepath.Join(expectedTownWD, "worker")

	scriptBody := fmt.Sprintf(`
if [ "$*" = "--allow-stale version" ]; then
  exit 0
fi

case "$1" in
  sql)
    echo '[{"depends_on_id":"ws-123"},{"depends_on_id":"hq-456"}]'
    exit 0
    ;;
  show)
    if [ -n "$BEADS_DIR" ]; then
      echo "BEADS_DIR leaked: $BEADS_DIR" >&2
      exit 1
    fi
    case "$*" in
      "show ws-123 hq-456 --json"|"show hq-456 ws-123 --json")
        echo "mixed-prefix batch not supported" >&2
        exit 1
        ;;
      "show ws-123 --json")
        if [ "$PWD" != "%s" ]; then
          echo "expected worker dir, got $PWD" >&2
          exit 1
        fi
        echo '[{"id":"ws-123","title":"Worker issue","status":"open","issue_type":"task"}]'
        exit 0
        ;;
      "show hq-456 --json")
        if [ "$PWD" != "%s" ]; then
          echo "expected town root, got $PWD" >&2
          exit 1
        fi
        echo '[{"id":"hq-456","title":"Town issue","status":"closed","issue_type":"task"}]'
        exit 0
        ;;
    esac
    ;;
esac

echo "unexpected bd args: $*" >&2
exit 1
`, expectedWorkerWD, expectedTownWD)
	writeRoutingBdStub(t, scriptBody)

	tracked, err := getTrackedIssues(filepath.Join(townRoot, ".beads"), "hq-cv-route")
	if err != nil {
		t.Fatalf("getTrackedIssues: %v", err)
	}
	if len(tracked) != 2 {
		t.Fatalf("expected 2 tracked issues, got %d: %#v", len(tracked), tracked)
	}

	statusByID := map[string]string{}
	for _, item := range tracked {
		statusByID[item.ID] = item.Status
	}
	if statusByID["ws-123"] != "open" {
		t.Fatalf("ws-123 status = %q, want %q", statusByID["ws-123"], "open")
	}
	if statusByID["hq-456"] != "closed" {
		t.Fatalf("hq-456 status = %q, want %q", statusByID["hq-456"], "closed")
	}
}

// TestConvoyCreate_UsesTrackingHelper verifies convoy create delegates tracking
// to the in-process helper instead of shelling out to `bd dep add`.
func TestConvoyCreate_UsesTrackingHelper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	townRoot, expectedWD := makeRoutingTownWorkspace(t)
	chdirConvoyTest(t, townRoot)

	// Write sentinel files to skip EnsureCustomTypes/Statuses (they call bd
	// config set/get which isn't relevant to routing).
	beadsDir := filepath.Join(townRoot, ".beads")
	typesList := "agent,role,rig,convoy,slot,queue,event,message,molecule,gate,merge-request"
	_ = os.WriteFile(filepath.Join(beadsDir, ".gt-types-configured"), []byte(typesList), 0644)
	_ = os.WriteFile(filepath.Join(beadsDir, ".gt-statuses-configured"), []byte("staged_ready,staged_warnings"), 0644)

	var helperTownRoot, helperConvoyID, helperIssueID string
	oldAddTracking := addTrackingRelationFn
	addTrackingRelationFn = func(townRoot, convoyID, issueID string) error {
		helperTownRoot = townRoot
		helperConvoyID = convoyID
		helperIssueID = issueID
		return nil
	}
	t.Cleanup(func() { addTrackingRelationFn = oldAddTracking })

	scriptBody := `
case "$1" in
  create)
    echo '[{"id":"hq-cv-test"}]'
    ;;
  init|config)
    exit 0
    ;;
  *)
    echo '[]'
    ;;
esac
`
	writeRoutingBdStub(t, scriptBody)

	// Override the entropy source for deterministic convoy IDs.
	oldEntropy := convoyIDEntropy
	convoyIDEntropy = strings.NewReader("abcde")
	t.Cleanup(func() { convoyIDEntropy = oldEntropy })

	_, err := captureConvoyStdoutErr(t, func() error {
		return runConvoyCreate(nil, []string{"test-convoy", "mo-2sh.1"})
	})
	if err != nil {
		t.Fatalf("runConvoyCreate: %v", err)
	}

	if helperTownRoot != expectedWD {
		t.Errorf("tracking helper townRoot = %q, want %q", helperTownRoot, expectedWD)
	}
	if helperConvoyID != "hq-cv-pqrst" {
		t.Errorf("tracking helper convoyID = %q, want %q", helperConvoyID, "hq-cv-pqrst")
	}
	if helperIssueID != "mo-2sh.1" {
		t.Errorf("tracking helper issueID = %q, want %q", helperIssueID, "mo-2sh.1")
	}
}

func TestConvoyAdd_UsesTrackingHelper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	townRoot, expectedWD := makeRoutingTownWorkspace(t)
	chdirConvoyTest(t, townRoot)

	var helperTownRoot, helperConvoyID string
	var helperIssues []string
	oldAddTracking := addTrackingRelationFn
	addTrackingRelationFn = func(townRoot, convoyID, issueID string) error {
		helperTownRoot = townRoot
		helperConvoyID = convoyID
		helperIssues = append(helperIssues, issueID)
		return nil
	}
	t.Cleanup(func() { addTrackingRelationFn = oldAddTracking })

	scriptBody := fmt.Sprintf(`
case "$*" in
  "show hq-cv-test --json")
    if [ "$PWD" != "%s" ]; then
      echo "expected town root, got $PWD" >&2
      exit 1
    fi
    echo '[{"id":"hq-cv-test","title":"Test Convoy","status":"open","issue_type":"convoy"}]'
    ;;
  *)
    echo "unexpected bd args: $*" >&2
    exit 1
    ;;
esac
`, expectedWD)
	writeRoutingBdStub(t, scriptBody)

	_, err := captureConvoyStdoutErr(t, func() error {
		return runConvoyAdd(nil, []string{"hq-cv-test", "ag-95s.1", "ag-95s.2"})
	})
	if err != nil {
		t.Fatalf("runConvoyAdd: %v", err)
	}

	if helperTownRoot != expectedWD {
		t.Errorf("tracking helper townRoot = %q, want %q", helperTownRoot, expectedWD)
	}
	if helperConvoyID != "hq-cv-test" {
		t.Errorf("tracking helper convoyID = %q, want %q", helperConvoyID, "hq-cv-test")
	}
	if got := strings.Join(helperIssues, ","); got != "ag-95s.1,ag-95s.2" {
		t.Errorf("tracking helper issues = %q, want %q", got, "ag-95s.1,ag-95s.2")
	}
}
