package beads

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// newAgentRoutingFixture builds a fake town (mayor/town.json + .beads) with one
// rig whose worker dir has its own .beads, and installs a mock bd that records
// every invocation's cwd/BEADS_DIR/args to logPath. Returns (townRoot, workerDir).
func newAgentRoutingFixture(t *testing.T, logPath string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("path assertions are Unix-oriented")
	}

	// Resolve symlinks so path assertions match shell pwd output.
	townRoot, _ := filepath.EvalSymlinks(t.TempDir())
	for _, dir := range []string{
		filepath.Join(townRoot, "mayor"),
		filepath.Join(townRoot, ".beads"),
		filepath.Join(townRoot, "imported", "mayor", "rig", ".beads"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte("{\"prefix\":\"pt-\",\"path\":\"imported/mayor/rig\"}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	installMockBDListRecorder(t, logPath)

	return townRoot, filepath.Join(townRoot, "imported", "mayor", "rig")
}

// installMockBDListRecorder places a fake bd in PATH that logs invocations and
// answers list/wisp queries with empty results.
func installMockBDListRecorder(t *testing.T, logPath string) {
	t.Helper()

	binDir := t.TempDir()
	script := `#!/bin/sh
printf 'pwd=%s\n' "$(pwd)" >> "$MOCK_BD_LOG"
printf 'args=%s\n' "$*" >> "$MOCK_BD_LOG"

case "$1" in
  list)
    printf '[]\n'
    ;;
  mol)
    printf '{"wisps":[],"count":0}\n'
    ;;
esac
exit 0
`
	scriptPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MOCK_BD_LOG", logPath)
}

// TestListAgentBeads_RoutesToTownStore verifies the roster read goes to the
// town database — the same store CreateAgentBead/GetAgentBead route to.
// Before this routing existed, a rig-bound wrapper listed the rig store while
// writes went to the town store: freshly spawned polecats were invisible to
// gt polecat identity list / capacity / dispatch (split-brain, hq-abv).
func TestListAgentBeads_RoutesToTownStore(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "bd.log")
	townRoot, workerDir := newAgentRoutingFixture(t, logPath)

	bd := NewWithBeadsDir(workerDir, filepath.Join(workerDir, ".beads"))
	if _, err := bd.ListAgentBeads(); err != nil {
		t.Fatalf("ListAgentBeads: %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read mock bd log: %v", err)
	}
	logOutput := string(logData)
	if !strings.Contains(logOutput, "pwd="+townRoot+"\n") {
		t.Errorf("ListAgentBeads did not run in town root %s:\n%s", townRoot, logOutput)
	}
	if strings.Contains(logOutput, "pwd="+workerDir+"\n") {
		t.Errorf("ListAgentBeads ran in rig dir %s (should route to town store):\n%s", workerDir, logOutput)
	}
	if !strings.Contains(logOutput, "args=list --label=gt:agent") {
		t.Errorf("mock bd log missing agent list call:\n%s", logOutput)
	}
}

// TestListAgentBeadsLocal_ScansBoundStore verifies the explicit store-local
// variant does NOT route: doctor/status use it to scan a specific database
// (town and each rig) when hunting for misplaced or stale agent beads.
func TestListAgentBeadsLocal_ScansBoundStore(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "bd.log")
	townRoot, workerDir := newAgentRoutingFixture(t, logPath)

	bd := NewWithBeadsDir(workerDir, filepath.Join(workerDir, ".beads"))
	if _, err := bd.ListAgentBeadsLocal(); err != nil {
		t.Fatalf("ListAgentBeadsLocal: %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read mock bd log: %v", err)
	}
	logOutput := string(logData)
	if !strings.Contains(logOutput, "pwd="+workerDir+"\n") {
		t.Errorf("ListAgentBeadsLocal did not run in bound rig dir %s:\n%s", workerDir, logOutput)
	}
	if strings.Contains(logOutput, "pwd="+townRoot+"\n") {
		t.Errorf("ListAgentBeadsLocal routed to town root %s (should stay store-local):\n%s", townRoot, logOutput)
	}
}
