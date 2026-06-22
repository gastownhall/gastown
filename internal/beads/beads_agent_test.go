package beads

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func installMockBDFixedShowOutput(t *testing.T, showOutput string) {
	t.Helper()

	binDir := t.TempDir()
	if runtime.GOOS == "windows" {
		scriptPath := filepath.Join(binDir, "bd.cmd")
		script := "@echo off\r\n" +
			"setlocal EnableDelayedExpansion\r\n" +
			"set \"cmd=\"\r\n" +
			":findcmd\r\n" +
			"if \"%~1\"==\"\" goto havecmd\r\n" +
			"set \"arg=%~1\"\r\n" +
			"if /I \"!arg:~0,2!\"==\"--\" (\r\n" +
			"  shift\r\n" +
			"  goto findcmd\r\n" +
			")\r\n" +
			"set \"cmd=%~1\"\r\n" +
			":havecmd\r\n" +
			"if /I \"%cmd%\"==\"version\" exit /b 0\r\n" +
			"if /I \"%cmd%\"==\"show\" (\r\n" +
			"  echo(%MOCK_BD_SHOW_OUTPUT%\r\n" +
			"  exit /b 0\r\n" +
			")\r\n" +
			"exit /b 0\r\n"
		if err := os.WriteFile(scriptPath, []byte(script), 0644); err != nil {
			t.Fatalf("write mock bd: %v", err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("MOCK_BD_SHOW_OUTPUT", showOutput)
		return
	}

	script := `#!/bin/sh
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done

case "$cmd" in
  version)
    exit 0
    ;;
  show)
    printf '%s\n' "$MOCK_BD_SHOW_OUTPUT"
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
	scriptPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MOCK_BD_SHOW_OUTPUT", showOutput)
}

func installMockBDShowRecorder(t *testing.T, showOutput string) string {
	t.Helper()

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")

	script := `#!/bin/sh
LOG_FILE='` + logPath + `'
printf '%s\n' "$*" >> "$LOG_FILE"

cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done

case "$cmd" in
  version)
    exit 0
    ;;
  show)
    printf '%s\n' "$MOCK_BD_SHOW_OUTPUT"
    exit 0
    ;;
  update)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
	scriptPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MOCK_BD_SHOW_OUTPUT", showOutput)
	return logPath
}

func installMockBDRequireExplicitBeadsDir(t *testing.T, expectedBeadsDir string) {
	t.Helper()

	binDir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done

target="${BEADS_DIR:-$(pwd)/.beads}"
if [ "$target" != "%s" ]; then
  echo "wrong target $target" >&2
  exit 9
fi

case "$cmd" in
  version)
    exit 0
    ;;
  show)
    printf '%%s\n' '[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","issue_type":"agent","labels":["gt:agent"],"description":"role_type: polecat\nrig: gastown\nagent_state: idle\nhook_bead: null","agent_state":"idle"}]'
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`, expectedBeadsDir)
	scriptPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestGetAgentBead_PrefersDescriptionAgentState(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	installMockBDFixedShowOutput(t, `[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","issue_type":"agent","labels":["gt:agent"],"description":"role_type: polecat\nrig: gastown\nagent_state: spawning\nhook_bead: null","agent_state":"idle"}]`)

	bd := NewIsolated(tmpDir)
	issue, fields, err := bd.GetAgentBead("gt-gastown-polecat-nux")
	if err != nil {
		t.Fatalf("GetAgentBead: %v", err)
	}
	if issue == nil {
		t.Fatal("GetAgentBead returned nil issue")
	}
	if fields == nil {
		t.Fatal("GetAgentBead returned nil fields")
	}
	if issue.AgentState != "idle" {
		t.Fatalf("issue.AgentState = %q, want %q", issue.AgentState, "idle")
	}
	// Description agent_state ("spawning") now takes priority over the legacy
	// structured column ("idle") per the bd 0.62+ contract.
	if fields.AgentState != "spawning" {
		t.Fatalf("fields.AgentState = %q, want %q (description should win)", fields.AgentState, "spawning")
	}
}

func TestGetAgentBead_FallsBackToDescriptionAgentState(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	installMockBDFixedShowOutput(t, `[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","issue_type":"agent","labels":["gt:agent"],"description":"role_type: polecat\nrig: gastown\nagent_state: spawning\nhook_bead: null"}]`)

	bd := NewIsolated(tmpDir)
	_, fields, err := bd.GetAgentBead("gt-gastown-polecat-nux")
	if err != nil {
		t.Fatalf("GetAgentBead: %v", err)
	}
	if fields == nil {
		t.Fatal("GetAgentBead returned nil fields")
	}
	if fields.AgentState != "spawning" {
		t.Fatalf("fields.AgentState = %q, want %q", fields.AgentState, "spawning")
	}
}

func TestUpdateAgentState_UsesUpdateDescriptionPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for bd")
	}
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	logPath := installMockBDShowRecorder(t, `[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","issue_type":"agent","labels":["gt:agent"],"description":"role_type: polecat\nrig: gastown\nagent_state: spawning\nhook_bead: null"}]`)
	bd := NewIsolated(tmpDir)

	if err := bd.UpdateAgentState("gt-gastown-polecat-nux", "working"); err != nil {
		t.Fatalf("UpdateAgentState: %v", err)
	}

	logOutput := readMockBDLog(t, logPath)
	if !strings.Contains(logOutput, "show gt-gastown-polecat-nux --json") {
		t.Fatalf("mock bd log %q missing show call", logOutput)
	}
	if !strings.Contains(logOutput, "update gt-gastown-polecat-nux") {
		t.Fatalf("mock bd log %q missing update call", logOutput)
	}
	// Should NOT use the obsolete bd agent state or bd set-state path
	if strings.Contains(logOutput, "agent state") || strings.Contains(logOutput, "set-state") {
		t.Fatalf("mock bd log %q unexpectedly used obsolete bd agent state / set-state path", logOutput)
	}
}

func TestUpdateAgentState_UsesExplicitBeadsDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for bd")
	}
	workDir := t.TempDir()
	targetBeadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(targetBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir target .beads: %v", err)
	}

	installMockBDRequireExplicitBeadsDir(t, targetBeadsDir)

	bd := NewWithBeadsDir(workDir, targetBeadsDir)
	if err := bd.UpdateAgentState("gt-gastown-polecat-nux", "spawning"); err != nil {
		t.Fatalf("UpdateAgentState: %v", err)
	}
}

func TestIsAgentBeadByID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		// Full-form IDs (prefix != rig): prefix-rig-role[-name]
		{name: "full witness", id: "gt-gastown-witness", want: true},
		{name: "full refinery", id: "gt-gastown-refinery", want: true},
		{name: "full crew with name", id: "gt-gastown-crew-krystian", want: true},
		{name: "full polecat with name", id: "gt-gastown-polecat-Toast", want: true},
		{name: "full deacon", id: "sh-shippercrm-deacon", want: true},
		{name: "full mayor", id: "ax-axon-mayor", want: true},

		// Collapsed-form IDs (prefix == rig): prefix-role[-name]
		// These have only 2 parts for witness/refinery, must still be detected.
		{name: "collapsed witness", id: "bcc-witness", want: true},
		{name: "collapsed refinery", id: "bcc-refinery", want: true},
		{name: "collapsed crew with name", id: "bcc-crew-krystian", want: true},
		{name: "collapsed polecat with name", id: "bcc-polecat-obsidian", want: true},

		// Non-agent IDs
		{name: "regular issue", id: "gt-12345", want: false},
		{name: "task bead", id: "bcc-fix-button-color", want: false},
		{name: "single part", id: "witness", want: false},
		{name: "empty string", id: "", want: false},
		{name: "patrol molecule", id: "mol-patrol-abc123", want: false},
		{name: "merge request", id: "gt-mr-1234", want: false},

		// Edge cases
		{name: "role in first position", id: "witness-something", want: false},
		{name: "beads prefix collapsed", id: "bd-beads-witness", want: true},
		{name: "beads crew", id: "bd-beads-crew-krystian", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAgentBeadByID(tt.id)
			if got != tt.want {
				t.Errorf("isAgentBeadByID(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

func TestMergeAgentBeadSources(t *testing.T) {
	t.Run("issues override duplicate wisp ids", func(t *testing.T) {
		issuesByID := map[string]*Issue{
			"hq-deacon": {ID: "hq-deacon", Type: "agent", Labels: []string{"gt:agent"}},
		}
		wispsByID := map[string]*Issue{
			"hq-deacon": {ID: "hq-deacon"},
		}

		merged := mergeAgentBeadSources(issuesByID, wispsByID)
		if len(merged) != 1 {
			t.Fatalf("len(merged) = %d, want 1", len(merged))
		}
		if merged["hq-deacon"].Type != "agent" {
			t.Fatalf("merged issue type = %q, want %q", merged["hq-deacon"].Type, "agent")
		}
		if len(merged["hq-deacon"].Labels) != 1 || merged["hq-deacon"].Labels[0] != "gt:agent" {
			t.Fatalf("merged labels = %v, want [gt:agent]", merged["hq-deacon"].Labels)
		}
	})

	t.Run("wisps are included when missing from issues", func(t *testing.T) {
		issuesByID := map[string]*Issue{
			"hq-mayor": {ID: "hq-mayor", Type: "agent", Labels: []string{"gt:agent"}},
		}
		wispsByID := map[string]*Issue{
			"bom-bti_ops_match-witness": {ID: "bom-bti_ops_match-witness"},
		}

		merged := mergeAgentBeadSources(issuesByID, wispsByID)
		if len(merged) != 2 {
			t.Fatalf("len(merged) = %d, want 2", len(merged))
		}
		if _, ok := merged["hq-mayor"]; !ok {
			t.Fatalf("expected hq-mayor in merged set")
		}
		if _, ok := merged["bom-bti_ops_match-witness"]; !ok {
			t.Fatalf("expected bom-bti_ops_match-witness in merged set")
		}
	})

	t.Run("handles nil maps", func(t *testing.T) {
		merged := mergeAgentBeadSources(nil, nil)
		if len(merged) != 0 {
			t.Fatalf("len(merged) = %d, want 0", len(merged))
		}
	})
}

func TestListAgentBeadsUsesIssueOnlyQueryAndIssueWinsDuplicateWisp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for bd")
	}
	ResetBdAllowStaleCacheForTest()
	t.Cleanup(ResetBdAllowStaleCacheForTest)

	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	script := `#!/bin/sh
if [ "$1" = "--allow-stale" ]; then
  echo "Error: unknown flag: --allow-stale" >&2
  exit 0
fi
printf '%s\n' "$*" >> "` + logPath + `"
cmd="$1"
shift || true
case "$cmd" in
  query)
    case "$*" in
      *ephemeral=false*gt:agent*)
        echo '[{"id":"hq-deacon","title":"Durable Deacon","status":"open","issue_type":"task","labels":["gt:agent"],"description":"role_type: deacon"}]'
        exit 0
        ;;
    esac
    echo "unexpected query: $*" >&2
    exit 2
    ;;
  mol)
    if [ "$1" = "wisp" ] && [ "$2" = "list" ]; then
      echo '{"wisps":[{"id":"hq-deacon","title":"Stale Deacon Wisp","status":"open","issue_type":"agent","labels":["gt:agent"]},{"id":"hq-wisp-only","title":"Wisp-only Agent","status":"open","issue_type":"agent","labels":["gt:agent"]}]}'
      exit 0
    fi
    ;;
  list)
    echo "bd list must not be used for agent issue lookup" >&2
    exit 7
    ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	agents, err := NewIsolated(tmpDir).ListAgentBeads()
	if err != nil {
		t.Fatalf("ListAgentBeads: %v", err)
	}
	if got := agents["hq-deacon"]; got == nil || got.Title != "Durable Deacon" {
		t.Fatalf("hq-deacon = %+v, want durable issue to win", got)
	}
	if got := agents["hq-wisp-only"]; got == nil || got.Title != "Wisp-only Agent" {
		t.Fatalf("hq-wisp-only = %+v, want wisp fallback", got)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(logBytes)), "\n") {
		if line == "list" || strings.HasPrefix(line, "list ") {
			t.Fatalf("agent issue lookup used bd list: %q\nfull log:\n%s", line, logBytes)
		}
	}
	log := string(logBytes)
	if !strings.Contains(log, "query --json ephemeral=false") || !strings.Contains(log, "label=\"gt:agent\"") || !strings.Contains(log, "--limit=0") {
		t.Fatalf("agent issue lookup did not use expected issue-only query; log:\n%s", log)
	}
}

func TestListAgentIssueBeadsStatusAllUsesAll(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks for bd")
	}
	ResetBdAllowStaleCacheForTest()
	t.Cleanup(ResetBdAllowStaleCacheForTest)

	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	script := `#!/bin/sh
if [ "$1" = "--allow-stale" ]; then
  echo "Error: unknown flag: --allow-stale" >&2
  exit 0
fi
printf '%s\n' "$*" >> "` + logPath + `"
if [ "$1" = "query" ]; then
  echo '[]'
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := NewIsolated(tmpDir).ListAgentIssueBeads("all"); err != nil {
		t.Fatalf("ListAgentIssueBeads(all): %v", err)
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	log := string(logBytes)
	if !strings.Contains(log, "--all") || !strings.Contains(log, "--limit=0") {
		t.Fatalf("ListAgentIssueBeads(all) missing --all/--limit=0; log:\n%s", log)
	}
}

func installMockBDCreateRecorder(t *testing.T, logPath string) {
	t.Helper()

	binDir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Skip("cross-rig create recorder test not implemented on Windows")
	}

	script := `#!/bin/sh
printf 'pwd=%s\n' "$(pwd)" >> "$MOCK_BD_LOG"
printf 'beads_dir=%s\n' "$BEADS_DIR" >> "$MOCK_BD_LOG"
printf 'args=%s\n' "$*" >> "$MOCK_BD_LOG"
printf 'call=%s args=%s\n' "$BEADS_DIR" "$*" >> "$MOCK_BD_LOG"

cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done

case "$cmd" in
  create)
    printf '{"id":"pt-imported-polecat-shiny","title":"shiny","status":"open"}\n'
    exit 0
    ;;
  show)
    if [ -n "$MOCK_BD_SHOW_ERROR" ]; then
      echo "$MOCK_BD_SHOW_ERROR" >&2
      exit 1
    fi
    if [ -n "$MOCK_BD_SHOW_OUTPUT" ]; then
      printf '%s\n' "$MOCK_BD_SHOW_OUTPUT"
      exit 0
    fi
    echo 'Issue not found' >&2
    exit 1
    ;;
  slot|config|migrate|init|update)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
	scriptPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MOCK_BD_LOG", logPath)
}

func TestCreateAgentBead_UsesTownRootForCrossRigRoutes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path assertions are Unix-oriented")
	}

	// Resolve symlinks so path assertions match shell pwd output.
	// On macOS, t.TempDir() returns /var/... but pwd resolves to /private/var/...
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

	logPath := filepath.Join(townRoot, "bd.log")
	installMockBDCreateRecorder(t, logPath)

	workerDir := filepath.Join(townRoot, "imported", "mayor", "rig")
	bd := NewWithBeadsDir(workerDir, filepath.Join(workerDir, ".beads"))

	issue, err := bd.CreateAgentBead("pt-imported-polecat-shiny", "shiny", &AgentFields{
		RoleType:   "polecat",
		Rig:        "imported",
		AgentState: "spawning",
		HookBead:   "pt-task-1",
	})
	if err != nil {
		t.Fatalf("CreateAgentBead: %v", err)
	}
	if issue == nil {
		t.Fatal("CreateAgentBead returned nil issue")
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read mock bd log: %v", err)
	}
	logOutput := string(logData)
	if !strings.Contains(logOutput, "pwd="+townRoot) {
		t.Fatalf("mock bd log missing town root cwd:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "beads_dir="+filepath.Join(townRoot, ".beads")) {
		t.Fatalf("mock bd log missing town-root BEADS_DIR:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "create --json --id=pt-imported-polecat-shiny") {
		t.Fatalf("mock bd log missing create call:\n%s", logOutput)
	}
	// Note: hook_bead slot is no longer set — bd slot removed in v0.62 (hq-l6mm5).
	// Work bead status=hooked and assignee=<agent> is now the authoritative source.
}

func TestCreateAgentBead_ParsesMockCreateOutput(t *testing.T) {
	raw := []byte(`{"id":"pt-imported-polecat-shiny","title":"shiny","status":"open"}`)
	var issue Issue
	if err := json.Unmarshal(raw, &issue); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if issue.ID != "pt-imported-polecat-shiny" {
		t.Fatalf("issue.ID = %q", issue.ID)
	}
}

func setupAgentRoutingTestTown(t *testing.T) (townRoot, townBeadsDir, rigDir, rigBeadsDir string) {
	t.Helper()

	townRoot, _ = filepath.EvalSymlinks(t.TempDir())
	townBeadsDir = filepath.Join(townRoot, ".beads")
	rigDir = filepath.Join(townRoot, "gastown", "mayor", "rig")
	rigBeadsDir = filepath.Join(rigDir, ".beads")
	for _, dir := range []string{filepath.Join(townRoot, "mayor"), townBeadsDir, rigBeadsDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteRoutes(townBeadsDir, []Route{{Prefix: "hq-", Path: "."}, {Prefix: "gt-", Path: "gastown/mayor/rig"}}); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	return townRoot, townBeadsDir, rigDir, rigBeadsDir
}

func TestUpdate_RigPrefixedAgentBeadUsesTownRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path assertions are Unix-oriented")
	}

	townRoot, townBeadsDir, rigDir, rigBeadsDir := setupAgentRoutingTestTown(t)
	logPath := filepath.Join(townRoot, "bd.log")
	installMockBDCreateRecorder(t, logPath)
	t.Setenv("MOCK_BD_SHOW_OUTPUT", `[{"id":"gt-gastown-polecat-rust","title":"rust","issue_type":"task","labels":["gt:agent"],"description":"role_type: polecat\nrig: gastown\nagent_state: idle"}]`)

	bd := NewWithBeadsDir(rigDir, rigBeadsDir)
	if err := bd.Update("gt-gastown-polecat-rust", UpdateOptions{AddLabels: []string{"done-intent:COMPLETED:1778598000"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	logOutput := readMockBDLog(t, logPath)
	if !strings.Contains(logOutput, "pwd="+townRoot) {
		t.Fatalf("mock bd log missing town root cwd:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "call="+townBeadsDir+" args=show gt-gastown-polecat-rust --json") {
		t.Fatalf("mock bd log missing town show call:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "call="+townBeadsDir+" args=update gt-gastown-polecat-rust --add-label=done-intent:COMPLETED:1778598000") {
		t.Fatalf("mock bd log missing routed update call:\n%s", logOutput)
	}
	if strings.Contains(logOutput, "call="+rigBeadsDir+" args=update gt-gastown-polecat-rust") {
		t.Fatalf("agent update used rig BEADS_DIR:\n%s", logOutput)
	}
}

func TestShow_RigPrefixedAgentBeadUsesTownRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path assertions are Unix-oriented")
	}

	townRoot, townBeadsDir, rigDir, rigBeadsDir := setupAgentRoutingTestTown(t)
	logPath := filepath.Join(townRoot, "bd.log")
	installMockBDCreateRecorder(t, logPath)
	t.Setenv("MOCK_BD_SHOW_OUTPUT", `[{"id":"gt-gastown-polecat-rust","title":"rust","issue_type":"task","labels":["gt:agent"],"description":"role_type: polecat\nrig: gastown\nagent_state: idle"}]`)

	bd := NewWithBeadsDir(rigDir, rigBeadsDir)
	issue, err := bd.Show("gt-gastown-polecat-rust")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if issue.ID != "gt-gastown-polecat-rust" {
		t.Fatalf("Show returned %q", issue.ID)
	}

	logOutput := readMockBDLog(t, logPath)
	if !strings.Contains(logOutput, "call="+townBeadsDir+" args=show gt-gastown-polecat-rust --json") {
		t.Fatalf("mock bd log missing town show call:\n%s", logOutput)
	}
	if strings.Contains(logOutput, "call="+rigBeadsDir+" args=show gt-gastown-polecat-rust --json") {
		t.Fatalf("agent show used rig BEADS_DIR:\n%s", logOutput)
	}
}

func TestUpdate_AgentShapedWorkBeadUsesRigRootWhenTownAgentMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path assertions are Unix-oriented")
	}

	townRoot, townBeadsDir, rigDir, rigBeadsDir := setupAgentRoutingTestTown(t)
	logPath := filepath.Join(townRoot, "bd.log")
	installMockBDCreateRecorder(t, logPath)

	bd := NewWithBeadsDir(rigDir, rigBeadsDir)
	status := "in_progress"
	if err := bd.Update("gt-gastown-polecat-cleanup", UpdateOptions{Status: &status}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	logOutput := readMockBDLog(t, logPath)
	if !strings.Contains(logOutput, "call="+townBeadsDir+" args=show gt-gastown-polecat-cleanup --json") {
		t.Fatalf("mock bd log missing town lookup:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "call="+rigBeadsDir+" args=update gt-gastown-polecat-cleanup --status=in_progress") {
		t.Fatalf("mock bd log missing rig update call:\n%s", logOutput)
	}
}

func TestUpdate_AgentShapedWorkBeadUsesRigRootWhenTownRecordIsNotAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path assertions are Unix-oriented")
	}

	townRoot, townBeadsDir, rigDir, rigBeadsDir := setupAgentRoutingTestTown(t)
	logPath := filepath.Join(townRoot, "bd.log")
	installMockBDCreateRecorder(t, logPath)
	t.Setenv("MOCK_BD_SHOW_OUTPUT", `[{"id":"gt-gastown-polecat-cleanup","title":"not an agent","issue_type":"task","labels":[]}]`)

	bd := NewWithBeadsDir(rigDir, rigBeadsDir)
	status := "in_progress"
	if err := bd.Update("gt-gastown-polecat-cleanup", UpdateOptions{Status: &status}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	logOutput := readMockBDLog(t, logPath)
	if !strings.Contains(logOutput, "call="+townBeadsDir+" args=show gt-gastown-polecat-cleanup --json") {
		t.Fatalf("mock bd log missing town lookup:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "call="+rigBeadsDir+" args=update gt-gastown-polecat-cleanup --status=in_progress") {
		t.Fatalf("mock bd log missing rig update call:\n%s", logOutput)
	}
}

func TestUpdate_AgentShapedWorkBeadStopsOnTownLookupError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path assertions are Unix-oriented")
	}

	townRoot, townBeadsDir, rigDir, rigBeadsDir := setupAgentRoutingTestTown(t)
	logPath := filepath.Join(townRoot, "bd.log")
	installMockBDCreateRecorder(t, logPath)
	t.Setenv("MOCK_BD_SHOW_ERROR", "database not found")

	bd := NewWithBeadsDir(rigDir, rigBeadsDir)
	status := "in_progress"
	if err := bd.Update("gt-gastown-polecat-cleanup", UpdateOptions{Status: &status}); err == nil {
		t.Fatal("Update succeeded despite town lookup error")
	}

	logOutput := readMockBDLog(t, logPath)
	if !strings.Contains(logOutput, "call="+townBeadsDir+" args=show gt-gastown-polecat-cleanup --json") {
		t.Fatalf("mock bd log missing town lookup:\n%s", logOutput)
	}
	if strings.Contains(logOutput, "call="+rigBeadsDir+" args=update gt-gastown-polecat-cleanup") {
		t.Fatalf("lookup error fell back to rig update:\n%s", logOutput)
	}
}

func TestUpdate_RoutedPrefixStopsAtResolvedBeadsDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path assertions are Unix-oriented")
	}

	townRoot, townBeadsDir, _, _ := setupAgentRoutingTestTown(t)
	rigDir := filepath.Join(townRoot, "gastown", "mayor", "rig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	poisonBeadsDir := filepath.Join(rigDir, "poison", ".beads")
	if err := os.MkdirAll(poisonBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir poison beads dir: %v", err)
	}
	if err := WriteRoutes(rigBeadsDir, []Route{{Prefix: "gt-", Path: "poison"}}); err != nil {
		t.Fatalf("write rig routes: %v", err)
	}

	logPath := filepath.Join(townRoot, "bd.log")
	installMockBDCreateRecorder(t, logPath)

	bd := NewWithBeadsDir(townRoot, townBeadsDir)
	status := "in_progress"
	if err := bd.Update("gt-work-123", UpdateOptions{Status: &status}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	logOutput := readMockBDLog(t, logPath)
	if !strings.Contains(logOutput, "call="+rigBeadsDir+" args=update gt-work-123 --status=in_progress") {
		t.Fatalf("mock bd log missing terminal rig update call:\n%s", logOutput)
	}
	if strings.Contains(logOutput, poisonBeadsDir) {
		t.Fatalf("routed update re-routed through rig-local routes:\n%s", logOutput)
	}
}

func TestForIssueID_RoutedPrefixDropsStore(t *testing.T) {
	townRoot, townBeadsDir, _, rigBeadsDir := setupAgentRoutingTestTown(t)
	bd := NewWithBeadsDirAndStore(townRoot, townBeadsDir, newMockStorage())

	target, err := bd.forIssueID("gt-work-123")
	if err != nil {
		t.Fatalf("forIssueID: %v", err)
	}
	if target == bd {
		t.Fatal("forIssueID returned original wrapper, want routed target")
	}
	if target.store != nil {
		t.Fatal("routed target retained in-process store")
	}
	if !target.noRoute {
		t.Fatal("routed target is not terminal noRoute wrapper")
	}
	if target.beadsDir != rigBeadsDir {
		t.Fatalf("target.beadsDir = %q, want %q", target.beadsDir, rigBeadsDir)
	}
}

func TestForAgentBeadDropsStore(t *testing.T) {
	_, townBeadsDir, rigDir, rigBeadsDir := setupAgentRoutingTestTown(t)
	bd := NewWithBeadsDirAndStore(rigDir, rigBeadsDir, newMockStorage())

	target := bd.ForAgentBead()
	if target == bd {
		t.Fatal("ForAgentBead returned original wrapper, want town target")
	}
	if target.store != nil {
		t.Fatal("ForAgentBead target retained in-process store")
	}
	if !target.noRoute {
		t.Fatal("ForAgentBead target is not terminal noRoute wrapper")
	}
	if target.beadsDir != townBeadsDir {
		t.Fatalf("target.beadsDir = %q, want %q", target.beadsDir, townBeadsDir)
	}
}

func TestCreateOrReopenAgentBeadExistingUsesTownBeadsDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock for bd")
	}

	townRoot, _ := filepath.EvalSymlinks(t.TempDir())
	townBeadsDir := filepath.Join(townRoot, ".beads")
	rigDir := filepath.Join(townRoot, "gastown", "mayor", "rig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	for _, dir := range []string{filepath.Join(townRoot, "mayor"), townBeadsDir, rigBeadsDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteRoutes(townBeadsDir, []Route{{Prefix: "hq-", Path: "."}, {Prefix: "gt-", Path: "gastown/mayor/rig"}}); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townBeadsDir, ".gt-types-configured"), []byte("v1\n"), 0644); err != nil {
		t.Fatalf("write types sentinel: %v", err)
	}

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	script := fmt.Sprintf(`#!/bin/sh
LOG=%q
EXPECTED=%q
printf 'beads_dir=%%s args=%%s\n' "${BEADS_DIR:-<unset>}" "$*" >> "$LOG"
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done
if [ "$cmd" != "version" ] && [ "${BEADS_DIR:-}" != "$EXPECTED" ]; then
  echo "wrong BEADS_DIR ${BEADS_DIR:-<unset>}" >&2
  exit 9
fi
case "$cmd" in
  version|update|reopen)
    exit 0
    ;;
  create)
    echo 'already exists' >&2
    exit 1
    ;;
  show)
    printf '%%s\n' '[{"id":"gt-gastown-polecat-rust","title":"old","issue_type":"task","labels":["gt:agent"],"status":"open","description":"role_type: polecat\nrig: gastown\nagent_state: idle\nhook_bead: old"}]'
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`, logPath, townBeadsDir)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	bd := NewWithBeadsDir(rigDir, rigBeadsDir)
	if _, err := bd.CreateOrReopenAgentBead("gt-gastown-polecat-rust", "gt-gastown-polecat-rust", &AgentFields{
		RoleType:   "polecat",
		Rig:        "gastown",
		AgentState: "spawning",
	}); err != nil {
		t.Fatalf("CreateOrReopenAgentBead: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read mock log: %v", err)
	}
	logOutput := string(logBytes)
	if strings.Contains(logOutput, "beads_dir="+rigBeadsDir) {
		t.Fatalf("CreateOrReopenAgentBead used rig BEADS_DIR; log:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "beads_dir="+townBeadsDir) || !strings.Contains(logOutput, "args=show") || !strings.Contains(logOutput, "args=update") {
		t.Fatalf("CreateOrReopenAgentBead did not use town BEADS_DIR for existing bead path; log:\n%s", logOutput)
	}
}
