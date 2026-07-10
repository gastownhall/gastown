package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
)

func writeBDStub(t *testing.T, binDir string, unixScript string, windowsScript string) string {
	t.Helper()

	var path string
	if runtime.GOOS == "windows" {
		path = filepath.Join(binDir, "bd.cmd")
		if err := os.WriteFile(path, []byte(windowsScript), 0644); err != nil {
			t.Fatalf("write bd stub: %v", err)
		}
		return path
	}

	path = filepath.Join(binDir, "bd")
	if err := os.WriteFile(path, []byte(unixScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	return path
}

func setupMutableBDRawSlingTest(t *testing.T, initialDescription string) (townRoot, rigPath, descPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mutable POSIX bd stub")
	}

	townRoot = t.TempDir()
	rigPath = filepath.Join(townRoot, "gastown", "mayor", "rig")
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"version":1}`), 0644); err != nil {
		t.Fatalf("write town marker: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(rigPath, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir rig beads: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir town beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(`{"prefix":"gt-","path":"gastown/mayor/rig"}`+"\n"), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	rigs := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{
		"gastown": {GitURL: "git@github.com:test/gastown.git", AddedAt: time.Now().Truncate(time.Second), BeadsConfig: &config.BeadsConfig{Repo: "local", Prefix: "gt"}},
	}}
	if err := config.SaveRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"), rigs); err != nil {
		t.Fatalf("SaveRigsConfig: %v", err)
	}

	descPath = filepath.Join(townRoot, "description.txt")
	statusPath := filepath.Join(townRoot, "status.txt")
	assigneePath := filepath.Join(townRoot, "assignee.txt")
	if err := os.WriteFile(descPath, []byte(initialDescription), 0644); err != nil {
		t.Fatalf("write description: %v", err)
	}
	if err := os.WriteFile(statusPath, []byte("open"), 0644); err != nil {
		t.Fatalf("write status: %v", err)
	}
	if err := os.WriteFile(assigneePath, []byte(""), 0644); err != nil {
		t.Fatalf("write assignee: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
set -eu
if [ "${1:-}" = "--allow-stale" ] && [ "${2:-}" = "version" ]; then
  echo "bd test"
  exit 0
fi
while [ "$#" -gt 0 ]; do
  case "$1" in
    --allow-stale) shift ;;
    *) break ;;
  esac
done
cmd="${1:-}"
if [ "$#" -gt 0 ]; then shift; fi
case "$cmd" in
  show)
    desc=""
    if [ -f "$BD_DESC_FILE" ]; then
      desc=$(awk 'BEGIN{first=1} {gsub(/\\/,"\\\\"); gsub(/"/,"\\\""); if(!first){printf "\\n"} printf "%s",$0; first=0}' "$BD_DESC_FILE")
    fi
    status="open"
    if [ -f "$BD_STATUS_FILE" ]; then status=$(cat "$BD_STATUS_FILE"); fi
    assignee=""
    if [ -f "$BD_ASSIGNEE_FILE" ]; then assignee=$(cat "$BD_ASSIGNEE_FILE"); fi
    printf '[{"id":"gt-rawrollback","title":"Test issue","status":"%s","assignee":"%s","description":"%s","dependencies":[]}]\n' "$status" "$assignee" "$desc"
    ;;
  update)
    for arg in "$@"; do
      case "$arg" in
        --description=*) printf "%s" "${arg#--description=}" > "$BD_DESC_FILE" ;;
        --status=*) printf "%s" "${arg#--status=}" > "$BD_STATUS_FILE" ;;
        --assignee=*) printf "%s" "${arg#--assignee=}" > "$BD_ASSIGNEE_FILE" ;;
      esac
    done
    ;;
  version)
    echo "bd test"
    ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_DESC_FILE", descPath)
	t.Setenv("BD_STATUS_FILE", statusPath)
	t.Setenv("BD_ASSIGNEE_FILE", assigneePath)
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_TEST_NO_NUDGE", "1")
	t.Setenv("GT_TEST_ATTACHED_MOLECULE_LOG", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	return townRoot, rigPath, descPath
}

func readMutableBDDescription(t *testing.T, descPath string) string {
	t.Helper()
	b, err := os.ReadFile(descPath)
	if err != nil {
		t.Fatalf("read description: %v", err)
	}
	return string(b)
}

func assertNoRawReviewMetadata(t *testing.T, desc string) {
	t.Helper()
	if strings.Contains(desc, "no_merge: true") || strings.Contains(desc, "review_only: true") {
		t.Fatalf("stale raw review metadata remains in description:\n%s", desc)
	}
	fields := beads.ParseAttachmentFields(&beads.Issue{Description: desc})
	if fields != nil && (fields.NoMerge || fields.ReviewOnly) {
		t.Fatalf("parsed stale raw review metadata from description: %+v", fields)
	}
}

func assertHasRawReviewMetadata(t *testing.T, desc string) {
	t.Helper()
	fields := beads.ParseAttachmentFields(&beads.Issue{Description: desc})
	if fields == nil || !fields.NoMerge || !fields.ReviewOnly {
		t.Fatalf("raw review metadata missing from description:\n%s", desc)
	}
	if fields.AttachedAt == "" {
		t.Fatalf("raw review metadata missing attached_at:\n%s", desc)
	}
	if _, err := time.Parse(time.RFC3339Nano, fields.AttachedAt); err != nil {
		t.Fatalf("attached_at %q is not RFC3339Nano: %v", fields.AttachedAt, err)
	}
}

func containsVarArg(line, key, value string) bool {
	plain := "--var " + key + "=" + value
	if strings.Contains(line, plain) {
		return true
	}
	quoted := "--var \"" + key + "=" + value + "\""
	return strings.Contains(line, quoted)
}

func TestParseWispIDFromJSON(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantID  string
		wantErr bool
	}{
		{
			name:   "new_epic_id",
			json:   `{"new_epic_id":"gt-wisp-abc","created":7,"phase":"vapor"}`,
			wantID: "gt-wisp-abc",
		},
		{
			name:   "root_id legacy",
			json:   `{"root_id":"gt-wisp-legacy"}`,
			wantID: "gt-wisp-legacy",
		},
		{
			name:   "result_id forward compat",
			json:   `{"result_id":"gt-wisp-result"}`,
			wantID: "gt-wisp-result",
		},
		{
			name:   "precedence prefers new_epic_id",
			json:   `{"root_id":"gt-wisp-legacy","new_epic_id":"gt-wisp-new"}`,
			wantID: "gt-wisp-new",
		},
		{
			name:    "missing id keys",
			json:    `{"created":7,"phase":"vapor"}`,
			wantErr: true,
		},
		{
			name:    "invalid JSON",
			json:    `{"new_epic_id":`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, err := parseWispIDFromJSON([]byte(tt.json))
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseWispIDFromJSON() error = %v, wantErr %v", err, tt.wantErr)
			}
			if gotID != tt.wantID {
				t.Fatalf("parseWispIDFromJSON() id = %q, want %q", gotID, tt.wantID)
			}
		})
	}
}

func TestExtractIssueID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"unwraps external format", "external:gt-mol:gt-mol-abc123", "gt-mol-abc123"},
		{"unwraps beads external", "external:beads-task:beads-task-xyz", "beads-task-xyz"},
		{"passes through hq IDs", "hq-abc123", "hq-abc123"},
		{"passes through plain IDs", "gt-abc123", "gt-abc123"},
		{"handles malformed external (only 2 parts)", "external:gt-mol", "external:gt-mol"},
		{"handles empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := beads.ExtractIssueID(tt.id)
			if got != tt.want {
				t.Errorf("ExtractIssueID(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestSlingNewlyCreatedRigBeadRoutesBDCommandsToTargetRig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows: shell stub redacts multiline descriptions")
	}
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)

	townRoot := t.TempDir()
	newBeadID := "gt-new123"

	// Minimal workspace marker so workspace.FindFromCwd() succeeds.
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Create a rig path that owns gt-* beads, and a routes.jsonl pointing to it.
	rigDir := filepath.Join(townRoot, "gastown", "mayor", "rig")
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(rigDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir rigDir: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown/mayor/rig"}`,
		`{"prefix":"hq-","path":"."}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "metadata.json"), []byte(`{"dolt_database":"hq","dolt_server_host":"127.0.0.1","dolt_server_port":3307}`), 0644); err != nil {
		t.Fatalf("write town metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigDir, ".beads", "metadata.json"), []byte(`{"dolt_database":"gastown","dolt_server_host":"127.0.0.2","dolt_server_port":4407}`), 0644); err != nil {
		t.Fatalf("write rig metadata: %v", err)
	}

	// Stub bd so we can observe that a newly-created rig bead's formula,
	// hook, and metadata writes all resolve to the target rig database.
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -e
log_args=""
for arg in "$@"; do
  case "$arg" in
    --description=*attached_molecule:*gt-wisp-xyz*attached_formula:*mol-polecat-work*) arg="--description=<attached-molecule-and-formula-fields>" ;;
    --description=*no_merge:*true*review_only:*true*) arg="--description=<review-only-fields>" ;;
    --description=*review_only:*true*no_merge:*true*) arg="--description=<review-only-fields>" ;;
    --description=*) arg="--description=<redacted>" ;;
  esac
  log_args="${log_args}${log_args:+ }${arg}"
done
printf '%s|%s|%s|%s|%s|%s|%s|%s\n' "$(pwd)" "${BEADS_DIR:-}" "${BEADS_DOLT_SERVER_DATABASE:-}" "${BEADS_DB:-}" "${BD_DB:-}" "${BEADS_DOLT_DATA_DIR:-}" "${GT_DOLT_DATA:-}" "$log_args" >> "${BD_LOG}"
cmd="$1"
shift || true
while [ "$cmd" = "--db" ] || [ "$cmd" = "--allow-stale" ]; do
  if [ "$cmd" = "--db" ]; then
    shift || true
  fi
  cmd="$1"
  shift || true
done
case "$cmd" in
  show)
    echo '[{"title":"Test issue","status":"open","assignee":"","description":""}]'
    ;;
  create)
    echo '{"id":"gt-new123","title":"New sling smoke","status":"open","assignee":""}'
    ;;
  formula)
    # formula show <name> - must output something for verifyFormulaExists
    echo '{"name":"test-formula"}'
    exit 0
    ;;
  cook)
    exit 0
    ;;
	  mol)
		sub="$1"
		shift || true
		case "$sub" in
		  wisp)
			echo 'legacy mol wisp should not be called' >&2
			exit 1
			;;
		  bond)
			echo '{"result_id":"gt-abc123","id_mapping":{"mol-polecat-work":"gt-wisp-xyz"}}'
			;;
		esac
    ;;
  update)
    exit 0
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
setlocal enableextensions
echo %CD%^|%BEADS_DIR%^|%BEADS_DOLT_SERVER_DATABASE%^|%BEADS_DB%^|%BD_DB%^|%BEADS_DOLT_DATA_DIR%^|%GT_DOLT_DATA%^|%*>>"%BD_LOG%"
set "cmd=%1"
set "sub=%2"
if "%cmd%"=="--allow-stale" (
  set "cmd=%2"
  set "sub=%3"
)
if "%cmd%"=="show" (
  echo [{"title":"Test issue","status":"open","assignee":"","description":""}]
  exit /b 0
)
if "%cmd%"=="create" (
  echo {"id":"gt-new123","title":"New sling smoke","status":"open","assignee":""}
  exit /b 0
)
if "%cmd%"=="formula" (
  echo {"name":"test-formula"}
  exit /b 0
)
if "%cmd%"=="cook" exit /b 0
if "%cmd%"=="mol" (
  if "%sub%"=="wisp" (
    echo legacy mol wisp should not be called 1>&2
    exit /b 1
  )
  if "%sub%"=="bond" (
    echo {"result_id":"gt-abc123","id_mapping":{"mol-polecat-work":"gt-wisp-xyz"}}
    exit /b 0
  )
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "") // Prevent inheriting real tmux pane from test runner

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Ensure we don't leak global flag state across tests.
	prevOn := slingOnTarget
	prevVars := slingVars
	prevDryRun := slingDryRun
	prevNoConvoy := slingNoConvoy
	prevHookRawBead := slingHookRawBead
	prevReviewOnly := slingReviewOnly
	prevNoMerge := slingNoMerge
	prevResolveTargetAgent := resolveTargetAgentFn
	t.Cleanup(func() {
		slingOnTarget = prevOn
		slingVars = prevVars
		slingDryRun = prevDryRun
		slingNoConvoy = prevNoConvoy
		slingHookRawBead = prevHookRawBead
		slingReviewOnly = prevReviewOnly
		slingNoMerge = prevNoMerge
		resolveTargetAgentFn = prevResolveTargetAgent
	})

	slingDryRun = false
	slingNoConvoy = true
	slingHookRawBead = false
	slingReviewOnly = false
	slingNoMerge = false
	slingVars = nil
	slingOnTarget = ""
	resolveTargetAgentFn = func(target string) (agentID string, pane string, hookRoot string, err error) {
		if target != "gastown/polecats/toast" {
			t.Fatalf("resolveTargetAgent target = %q, want gastown/polecats/toast", target)
		}
		return "gastown/polecats/toast", "", filepath.Join(townRoot, "gastown", "polecats", "toast", "gastown"), nil
	}

	// Prevent real tmux nudge from firing during tests (causes agent self-interruption)
	t.Setenv("GT_TEST_NO_NUDGE", "1")
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1") // Stub bd doesn't track state
	// Poison the ambient beads target: all mutating commands must override this
	// with the route-resolved target rig database.
	t.Setenv("BEADS_DIR", filepath.Join(townRoot, ".beads"))
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "hq")
	t.Setenv("BEADS_DB", filepath.Join(townRoot, "wrong.db"))
	t.Setenv("BD_DB", filepath.Join(townRoot, "wrong.bd"))
	t.Setenv("BEADS_DOLT_DATA_DIR", filepath.Join(townRoot, "wrong-data"))
	t.Setenv("GT_DOLT_DATA", filepath.Join(townRoot, "wrong-gt-data"))

	createOut, err := BdCmd("create", "--json", "--title=New sling smoke", "--type=task").
		Dir(rigDir).
		Output()
	if err != nil {
		t.Fatalf("create new rig bead: %v", err)
	}
	if !strings.Contains(string(createOut), newBeadID) {
		t.Fatalf("created bead output = %q, want %s", createOut, newBeadID)
	}

	if err := runSling(nil, []string{newBeadID, "gastown/polecats/toast"}); err != nil {
		t.Fatalf("runSling: %v", err)
	}

	// Also exercise the explicit formula-on-bead path; this is the older
	// --on-style route that must use the same target rig database.
	slingOnTarget = newBeadID
	if err := runSling(nil, []string{"mol-review"}); err != nil {
		t.Fatalf("runSling: %v", err)
	}

	slingOnTarget = ""
	slingHookRawBead = true
	slingReviewOnly = true
	slingNoMerge = true
	if err := runSling(nil, []string{newBeadID, "gastown/polecats/toast"}); err != nil {
		t.Fatalf("runSling raw review-only: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	logLines := strings.Split(strings.TrimSpace(string(logBytes)), "\n")

	wantDir := rigDir
	if resolved, err := filepath.EvalSymlinks(wantDir); err == nil {
		wantDir = resolved
	}
	wantBeadsDir := filepath.Join(rigDir, ".beads")
	if resolved, err := filepath.EvalSymlinks(wantBeadsDir); err == nil {
		wantBeadsDir = resolved
	}
	gotPolecatCook := false
	gotReviewCook := false
	gotBondCount := 0
	gotCreate := false
	gotTargetDBCheck := false
	gotFormulaShow := false
	gotHook := false
	gotMetadata := false
	gotReviewOnlyMetadata := false
	assertTargetRig := func(kind, dir, beadsDir, database, beadsDB, bdDB, dataDir, gtData, args string) {
		t.Helper()
		if dir != wantDir {
			t.Fatalf("bd %s ran in %q, want %q (args: %q)", kind, dir, wantDir, args)
		}
		if beadsDir != wantBeadsDir {
			t.Fatalf("bd %s used BEADS_DIR %q, want %q (args: %q)", kind, beadsDir, wantBeadsDir, args)
		}
		if database != "gastown" {
			t.Fatalf("bd %s used BEADS_DOLT_SERVER_DATABASE %q, want gastown (args: %q)", kind, database, args)
		}
		if beadsDB != "" || bdDB != "" || dataDir != "" {
			t.Fatalf("bd %s leaked stale DB env BEADS_DB=%q BD_DB=%q BEADS_DOLT_DATA_DIR=%q (args: %q)", kind, beadsDB, bdDB, dataDir, args)
		}
		if gtData != "" {
			t.Fatalf("bd %s leaked GT_DOLT_DATA=%q (args: %q)", kind, gtData, args)
		}
	}

	firstReviewOnlyMetadataIndex := -1
	lastHookIndex := -1
	for i, line := range logLines {
		parts := strings.SplitN(line, "|", 8)
		if len(parts) != 8 {
			t.Fatalf("malformed bd log line: %q", line)
		}
		dir := parts[0]
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			dir = resolved
		}
		beadsDir := parts[1]
		if resolved, err := filepath.EvalSymlinks(beadsDir); err == nil {
			beadsDir = resolved
		}
		database := parts[2]
		beadsDB := parts[3]
		bdDB := parts[4]
		dataDir := parts[5]
		gtData := parts[6]
		args := parts[7]

		switch {
		case strings.Contains(args, "create "):
			gotCreate = true
			assertTargetRig("create", dir, beadsDir, database, beadsDB, bdDB, dataDir, gtData, args)
		case strings.Contains(args, "show "+newBeadID) && strings.Contains(args, "--json"):
			gotTargetDBCheck = true
			assertTargetRig("target DB check", dir, beadsDir, database, beadsDB, bdDB, dataDir, gtData, args)
		case strings.Contains(args, "sql SELECT DISTINCT wisp_dependencies.issue_id"):
			assertTargetRig("molecule dep check", dir, beadsDir, database, beadsDB, bdDB, dataDir, gtData, args)
		case strings.Contains(args, "formula show "):
			gotFormulaShow = true
			assertTargetRig("formula show", dir, beadsDir, database, beadsDB, bdDB, dataDir, gtData, args)
		case strings.Contains(args, "cook "):
			switch {
			case strings.Contains(args, "mol-polecat-work"):
				gotPolecatCook = true
			case strings.Contains(args, "mol-review"):
				gotReviewCook = true
			default:
				t.Fatalf("bd cook args = %q, want expected formula", args)
			}
			assertTargetRig("cook", dir, beadsDir, database, beadsDB, bdDB, dataDir, gtData, args)
		case strings.Contains(args, "mol bond "):
			gotBondCount++
			if !strings.Contains(args, "--ephemeral") {
				t.Fatalf("bd mol bond args = %q, want --ephemeral", args)
			}
			assertTargetRig("mol bond", dir, beadsDir, database, beadsDB, bdDB, dataDir, gtData, args)
		case strings.Contains(args, "update "+newBeadID) && strings.Contains(args, "--status=hooked"):
			gotHook = true
			lastHookIndex = i
			assertTargetRig("hook update", dir, beadsDir, database, beadsDB, bdDB, dataDir, gtData, args)
		case strings.Contains(args, "update "+newBeadID) && strings.Contains(args, "--description=<attached-molecule-and-formula-fields>"):
			gotMetadata = true
			assertTargetRig("metadata update", dir, beadsDir, database, beadsDB, bdDB, dataDir, gtData, args)
		case strings.Contains(args, "update "+newBeadID) && strings.Contains(args, "--description=<review-only-fields>"):
			gotReviewOnlyMetadata = true
			if firstReviewOnlyMetadataIndex == -1 {
				firstReviewOnlyMetadataIndex = i
			}
			assertTargetRig("review-only metadata update", dir, beadsDir, database, beadsDB, bdDB, dataDir, gtData, args)
		case strings.Contains(args, "update "+newBeadID) && strings.Contains(args, "--description="):
			assertTargetRig("description update", dir, beadsDir, database, beadsDB, bdDB, dataDir, gtData, args)
		case args == "--version" || strings.HasPrefix(args, "version") || strings.Contains(args, " version") || strings.Contains(args, "show gt-rig-") || strings.Contains(args, "show mol-"):
			// Explicitly exempt non-target-bead lookups; every gt-new123 operation
			// above must still prove it is pinned to the gastown database.
		default:
			t.Fatalf("unexpected bd command without routing assertion: %q", line)
		}
	}

	if !gotCreate || !gotTargetDBCheck || !gotFormulaShow || !gotPolecatCook || !gotReviewCook || gotBondCount < 2 || !gotHook || !gotMetadata || !gotReviewOnlyMetadata {
		t.Fatalf("missing expected bd commands: create=%v targetDBCheck=%v formulaShow=%v polecatCook=%v reviewCook=%v bondCount=%d hook=%v metadata=%v reviewOnlyMetadata=%v (log: %q)",
			gotCreate, gotTargetDBCheck, gotFormulaShow, gotPolecatCook, gotReviewCook, gotBondCount, gotHook, gotMetadata, gotReviewOnlyMetadata, string(logBytes))
	}
	if firstReviewOnlyMetadataIndex == -1 || lastHookIndex == -1 || firstReviewOnlyMetadataIndex > lastHookIndex {
		t.Fatalf("review-only metadata must be stored before raw hook assignment: metadataIndex=%d hookIndex=%d log: %q", firstReviewOnlyMetadataIndex, lastHookIndex, string(logBytes))
	}
}

func TestRoutedBeadReadUsesCanonicalShowWithoutUnsupportedAllowStale(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows: shell stub uses POSIX syntax")
	}
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)

	townRoot := t.TempDir()
	beadID := "gt-new123"
	rigDir := filepath.Join(townRoot, "gastown", "mayor", "rig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	for _, dir := range []string{filepath.Join(townRoot, "mayor"), filepath.Join(townRoot, ".beads"), rigBeadsDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"type":"town","name":"test"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown/mayor/rig"}`,
		`{"prefix":"hq-","path":"."}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -eu
printf '%s|%s|%s\n' "$(pwd)" "${BEADS_DIR:-}" "$*" >> "${BD_LOG}"
if [ "$1" = "--allow-stale" ]; then
  echo "Error: unknown flag: --allow-stale" >&2
  exit 1
fi
case "$1" in
  version)
    echo "bd 1.0.3"
    ;;
  show)
    if [ "${BEADS_DIR:-}" != "${RIG_BEADS}" ]; then
      echo "wrong database: ${BEADS_DIR:-}" >&2
      exit 1
    fi
    echo '[{"id":"gt-new123","title":"Routed bead","status":"open","assignee":"","description":"body","issue_type":"bug","labels":["x"],"dependencies":[{"id":"gt-wisp-old","status":"open"}]}]'
    ;;
esac
`
	_ = writeBDStub(t, binDir, bdScript, "")

	t.Setenv("BD_LOG", logPath)
	t.Setenv("RIG_BEADS", rigBeadsDir)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BEADS_DIR", filepath.Join(townRoot, ".beads"))
	t.Setenv("GT_ROOT", townRoot)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	if err := verifyBeadExists(beadID); err != nil {
		t.Fatalf("verifyBeadExists: %v", err)
	}
	info, err := getBeadInfoFromTownRoot(townRoot, beadID)
	if err != nil {
		t.Fatalf("getBeadInfoFromTownRoot: %v", err)
	}
	if info.Title != "Routed bead" || info.IssueType != "bug" || len(info.Labels) != 1 || len(info.Dependencies) != 1 {
		t.Fatalf("info = %+v, want routed issue fields preserved", info)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	showCalls := 0
	for _, line := range strings.Split(strings.TrimSpace(string(logBytes)), "\n") {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 || !strings.Contains(parts[2], "show "+beadID) {
			continue
		}
		showCalls++
		if strings.Contains(parts[2], "--allow-stale") {
			t.Fatalf("bd show used unsupported --allow-stale: %q", line)
		}
		if parts[1] != rigBeadsDir {
			t.Fatalf("bd show BEADS_DIR = %q, want %q (line %q)", parts[1], rigBeadsDir, line)
		}
	}
	if showCalls == 0 {
		t.Fatalf("no bd show calls logged: %s", logBytes)
	}
}

func TestSlingRollsBackSpawnedPolecatOnInstantiateFailure(t *testing.T) {
	townRoot := t.TempDir()

	// Minimal workspace marker so workspace.FindFromCwd() succeeds.
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Register rig so IsRigName("gastown") succeeds.
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigs := &config.RigsConfig{
		Version: 1,
		Rigs: map[string]config.RigEntry{
			"gastown": {
				GitURL:    "git@github.com:test/gastown.git",
				LocalRepo: "",
				AddedAt:   time.Now().Truncate(time.Second),
				BeadsConfig: &config.BeadsConfig{
					Repo:   "local",
					Prefix: "gt-",
				},
			},
		},
	}
	if err := config.SaveRigsConfig(rigsPath, rigs); err != nil {
		t.Fatalf("SaveRigsConfig: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads"), 0755); err != nil {
		t.Fatalf("mkdir rig beads dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir rig dir: %v", err)
	}

	// Routes: gt-* resolves to gastown's rig beads dir.
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown/mayor/rig"}`,
		`{"prefix":"hq-","path":"."}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	// Stub bd: make mol wisp fail to simulate missing required vars.
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
set -e
cmd="$1"
shift || true
while [ "$cmd" = "--db" ] || [ "$cmd" = "--allow-stale" ]; do
  if [ "$cmd" = "--db" ]; then
    shift || true
  fi
  cmd="$1"
  shift || true
done
case "$cmd" in
  show)
    echo '[{"title":"Test issue","status":"open","assignee":"","description":""}]'
    exit 0
    ;;
  update)
    exit 0
    ;;
  cook)
    exit 0
    ;;
  mol)
    sub="$1"
    shift || true
    case "$sub" in
      wisp)
        echo "missing required vars" 1>&2
        exit 1
        ;;
    esac
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
setlocal enableextensions
if "%1"=="--db" (
  shift
  shift
)
set "cmd=%1"
set "sub=%2"
if "%cmd%"=="show" (
  echo [{"title":"Test issue","status":"open","assignee":"","description":""}]
  exit /b 0
)
if "%cmd%"=="update" exit /b 0
if "%cmd%"=="cook" exit /b 0
if "%cmd%"=="mol" (
  if "%sub%"=="wisp" (
    echo missing required vars 1>&2
    exit /b 1
  )
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("GT_TEST_NO_NUDGE", "1")
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Ensure we don't leak global flag/seam state across tests.
	prevNoConvoy := slingNoConvoy
	prevNoBoot := slingNoBoot
	prevDryRun := slingDryRun
	prevHookRaw := slingHookRawBead
	prevSpawn := spawnPolecatForSling
	prevRollback := rollbackSlingArtifactsFn
	t.Cleanup(func() {
		slingNoConvoy = prevNoConvoy
		slingNoBoot = prevNoBoot
		slingDryRun = prevDryRun
		slingHookRawBead = prevHookRaw
		spawnPolecatForSling = prevSpawn
		rollbackSlingArtifactsFn = prevRollback
	})

	slingDryRun = false
	slingNoConvoy = true
	slingNoBoot = true
	slingHookRawBead = false

	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		return &SpawnedPolecatInfo{
			RigName:     rigName,
			PolecatName: "Toast",
			ClonePath:   filepath.Join(townRoot, "fake-polecat"),
		}, nil
	}

	rollbackCalled := false
	rollbackSlingArtifactsFn = func(spawnInfo *SpawnedPolecatInfo, beadID, hookWorkDir, convoyID string) {
		rollbackCalled = true
		if spawnInfo == nil || spawnInfo.PolecatName != "Toast" {
			t.Fatalf("unexpected spawnInfo in rollback: %+v", spawnInfo)
		}
		if beadID != "gt-abc123" {
			t.Fatalf("unexpected beadID in rollback: %q", beadID)
		}
		if want := filepath.Join(townRoot, "fake-polecat"); hookWorkDir != want {
			t.Fatalf("unexpected hookWorkDir in rollback: got %q want %q", hookWorkDir, want)
		}
	}

	err = runSling(nil, []string{"gt-abc123", "gastown"})
	if err == nil {
		t.Fatalf("expected error from runSling")
	}
	if !rollbackCalled {
		t.Fatalf("expected rollbackSlingArtifacts to be called")
	}
}

func TestSlingRollsBackSpawnedPolecatOnHookFailure(t *testing.T) {
	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"version":1}`), 0644); err != nil {
		t.Fatalf("write town marker: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads"), 0755); err != nil {
		t.Fatalf("mkdir gastown mayor rig: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(`{"prefix":"gt-","path":"gastown/mayor/rig"}`+"\n"), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	rigs := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{
		"gastown": {GitURL: "git@github.com:test/gastown.git", AddedAt: time.Now().Truncate(time.Second), BeadsConfig: &config.BeadsConfig{Repo: "local", Prefix: "gt-"}},
	}}
	if err := config.SaveRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"), rigs); err != nil {
		t.Fatalf("SaveRigsConfig: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    show) echo '[{"title":"Test issue","status":"open","assignee":"","description":""}]'; exit 0 ;;
    update) exit 0 ;;
  esac
done
exit 0
`
	bdScriptWindows := `@echo off
for %%a in (%*) do (
  if "%%a"=="show" (echo [{"title":"Test issue","status":"open","assignee":"","description":""}]& exit /b 0)
  if "%%a"=="update" exit /b 0
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_TEST_NO_NUDGE", "1")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	prevNoConvoy := slingNoConvoy
	prevNoBoot := slingNoBoot
	prevHookRaw := slingHookRawBead
	prevSpawn := spawnPolecatForSling
	prevResolveTargetAgent := resolveTargetAgentFn
	prevRollback := rollbackSlingArtifactsFn
	prevHook := hookBeadWithRetryFn
	t.Cleanup(func() {
		slingNoConvoy = prevNoConvoy
		slingNoBoot = prevNoBoot
		slingHookRawBead = prevHookRaw
		spawnPolecatForSling = prevSpawn
		resolveTargetAgentFn = prevResolveTargetAgent
		rollbackSlingArtifactsFn = prevRollback
		hookBeadWithRetryFn = prevHook
	})
	slingNoConvoy = true
	slingNoBoot = true
	slingHookRawBead = true

	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		return &SpawnedPolecatInfo{RigName: rigName, PolecatName: "Toast", ClonePath: filepath.Join(townRoot, "fake-polecat")}, nil
	}
	resolveTargetAgentFn = func(target string) (agentID string, pane string, hookRoot string, err error) {
		return "", "", "", errors.New("simulated dead target")
	}
	hookBeadWithRetryFn = func(beadID, targetAgent, hookDir string) error {
		return errors.New("simulated hook failure")
	}

	rollbackCalled := false
	rollbackSlingArtifactsFn = func(spawnInfo *SpawnedPolecatInfo, beadID, hookWorkDir, convoyID string) {
		rollbackCalled = true
		if spawnInfo == nil || spawnInfo.PolecatName != "Toast" {
			t.Fatalf("unexpected spawnInfo in rollback: %+v", spawnInfo)
		}
	}

	err = runSling(nil, []string{"gt-abc123", "gastown/polecats/toast"})
	if err == nil {
		t.Fatalf("expected hook failure from runSling")
	}
	if !rollbackCalled {
		t.Fatalf("expected rollbackSlingArtifacts to be called")
	}
}

func TestSlingRejectsBeadMissingFromTargetRigBeforeSpawn(t *testing.T) {
	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigs := &config.RigsConfig{
		Version: 1,
		Rigs: map[string]config.RigEntry{
			"gastown": {
				GitURL:  "git@github.com:test/gastown.git",
				AddedAt: time.Now().Truncate(time.Second),
				BeadsConfig: &config.BeadsConfig{
					Repo:   "local",
					Prefix: "zz-",
				},
			},
		},
	}
	if err := config.SaveRigsConfig(rigsPath, rigs); err != nil {
		t.Fatalf("SaveRigsConfig: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads"), 0755); err != nil {
		t.Fatalf("mkdir target rig dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"."}`,
		`{"prefix":"zz-","path":"gastown/mayor/rig"}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -e
echo "$*" >> "${BD_LOG}"
cmd="$1"
shift || true
if [ "$cmd" = "--allow-stale" ]; then
  cmd="$1"
  shift || true
fi
case "$cmd" in
  show)
    if [ "${BEADS_DIR:-}" = "${TARGET_BEADS_DIR}" ]; then
      # The direct target-rig DB lookup must fail: the bead only resolves from HQ.
      exit 1
    fi
    echo '[{"title":"HQ-owned issue","status":"open","assignee":"","description":""}]'
    ;;
  mol|update|cook)
    echo "unexpected side effect: $cmd" >&2
    exit 2
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
echo %*>>"%BD_LOG%"
set "cmd=%1"
if "%cmd%"=="show" (
  if "%BEADS_DIR%"=="%TARGET_BEADS_DIR%" exit /b 1
  echo [{"title":"HQ-owned issue","status":"open","assignee":"","description":""}]
  exit /b 0
)
if "%cmd%"=="mol" exit /b 2
if "%cmd%"=="update" exit /b 2
if "%cmd%"=="cook" exit /b 2
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("TARGET_BEADS_DIR", filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("GT_TEST_NO_NUDGE", "1")
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	prevNoConvoy := slingNoConvoy
	prevNoBoot := slingNoBoot
	prevSpawn := spawnPolecatForSling
	t.Cleanup(func() {
		slingNoConvoy = prevNoConvoy
		slingNoBoot = prevNoBoot
		spawnPolecatForSling = prevSpawn
	})
	slingNoConvoy = true
	slingNoBoot = true

	spawnCalled := false
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		spawnCalled = true
		return &SpawnedPolecatInfo{RigName: rigName, PolecatName: "toast", ClonePath: filepath.Join(townRoot, "fake-polecat")}, nil
	}

	err = runSling(nil, []string{"gt-r2405", "gastown"})
	if err == nil {
		t.Fatal("expected target-rig database validation error")
	}
	if !strings.Contains(err.Error(), "not present in target rig") {
		t.Fatalf("unexpected error: %v", err)
	}
	if spawnCalled {
		t.Fatal("spawnPolecatForSling was called before target-rig database validation rejected the bead")
	}
}

func TestTargetRigDatabaseAllowsRouteResolvedGtBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows: shell stub uses POSIX env logging")
	}
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)

	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "gastown", "mayor", "rig")
	for _, dir := range []string{filepath.Join(townRoot, ".beads"), filepath.Join(townRoot, "mayor", "rig"), filepath.Join(rigDir, ".beads")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown/mayor/rig"}`,
		`{"prefix":"hq-","path":"."}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigDir, ".beads", "metadata.json"), []byte(`{"dolt_database":"gastown","dolt_server_host":"127.0.0.1","dolt_server_port":3307}`), 0644); err != nil {
		t.Fatalf("write rig metadata: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -e
if [ "$1" = "--allow-stale" ] && [ "${2:-}" = "version" ]; then
  echo 'bd version 1.0.0'
  exit 0
fi
printf '%s|%s|%s|%s|%s\n' "$*" "$(pwd)" "${BEADS_DIR:-}" "${BEADS_DOLT_DATA_DIR:-}" "${BEADS_DOLT_SERVER_DATABASE:-}" >> "${BD_LOG}"
cmd="$1"
shift || true
if [ "$cmd" = "--allow-stale" ]; then
  cmd="$1"
  shift || true
fi
case "$cmd" in
  show)
    echo '[{"title":"Route issue","status":"open","assignee":"","description":""}]'
    ;;
  *)
    echo "unexpected command: $cmd" >&2
    exit 2
    ;;
esac
`
	_ = writeBDStub(t, binDir, bdScript, "")

	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GT_DOLT_DATA", filepath.Join(townRoot, ".dolt-data"))
	t.Setenv("BEADS_DOLT_DATA_DIR", filepath.Join(townRoot, "wrong-data"))
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "hq")

	if err := verifyBeadExistsInTargetRigDatabase("gt-hq-oy83-cleanup", "gastown", townRoot); err != nil {
		t.Fatalf("verifyBeadExistsInTargetRigDatabase: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	line := strings.TrimSpace(string(logBytes))
	parts := strings.Split(line, "|")
	if len(parts) != 5 {
		t.Fatalf("malformed bd log: %q", line)
	}
	if !strings.Contains(parts[0], "show gt-hq-oy83-cleanup --json") {
		t.Fatalf("bd args = %q, want route-resolved show", parts[0])
	}
	if parts[1] != rigDir {
		t.Fatalf("bd cwd = %q, want %q", parts[1], rigDir)
	}
	if want := filepath.Join(rigDir, ".beads"); parts[2] != want {
		t.Fatalf("BEADS_DIR = %q, want %q", parts[2], want)
	}
	if parts[3] != "" {
		t.Fatalf("BEADS_DOLT_DATA_DIR leaked: %q", parts[3])
	}
	if parts[4] != "gastown" {
		t.Fatalf("BEADS_DOLT_SERVER_DATABASE = %q, want gastown", parts[4])
	}
}

func setupCrossDatabaseSlingGuardTest(t *testing.T) (townRoot, logPath string) {
	t.Helper()

	townRoot = t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigs := &config.RigsConfig{
		Version: 1,
		Rigs: map[string]config.RigEntry{
			"gastown": {
				GitURL:  "git@github.com:test/gastown.git",
				AddedAt: time.Now().Truncate(time.Second),
				BeadsConfig: &config.BeadsConfig{
					Repo:   "local",
					Prefix: "zz-",
				},
			},
		},
	}
	if err := config.SaveRigsConfig(rigsPath, rigs); err != nil {
		t.Fatalf("SaveRigsConfig: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads"), 0755); err != nil {
		t.Fatalf("mkdir target rig dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"."}`,
		`{"prefix":"zz-","path":"gastown/mayor/rig"}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath = filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -e
echo "$*" >> "${BD_LOG}"
cmd="$1"
shift || true
if [ "$cmd" = "--allow-stale" ]; then
  cmd="$1"
  shift || true
fi
case "$cmd" in
  show)
    if [ "${BEADS_DIR:-}" = "${TARGET_BEADS_DIR}" ]; then
      exit 1
    fi
    echo '[{"title":"HQ-owned issue","status":"open","assignee":"","description":""}]'
    ;;
  create|update|cook|mol|close|dep)
    echo "unexpected side effect: $cmd" >&2
    exit 2
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
echo %*>>"%BD_LOG%"
set "cmd=%1"
if "%cmd%"=="show" (
  if "%BEADS_DIR%"=="%TARGET_BEADS_DIR%" exit /b 1
  echo [{"title":"HQ-owned issue","status":"open","assignee":"","description":""}]
  exit /b 0
)
if "%cmd%"=="create" exit /b 2
if "%cmd%"=="update" exit /b 2
if "%cmd%"=="cook" exit /b 2
if "%cmd%"=="mol" exit /b 2
if "%cmd%"=="close" exit /b 2
if "%cmd%"=="dep" exit /b 2
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("TARGET_BEADS_DIR", filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("GT_TEST_NO_NUDGE", "1")
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	return townRoot, logPath
}

func TestScheduleBeadRejectsMissingTargetRigDatabaseBeforeContext(t *testing.T) {
	_, logPath := setupCrossDatabaseSlingGuardTest(t)

	err := scheduleBead("gt-r2405", "gastown", ScheduleOptions{})
	if err == nil {
		t.Fatal("expected target-rig database validation error")
	}
	if !strings.Contains(err.Error(), "not present in target rig") {
		t.Fatalf("unexpected error: %v", err)
	}

	logBytes, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("read bd log: %v", readErr)
	}
	log := string(logBytes)
	for _, sideEffect := range []string{"create", "update", "cook", "mol", "close", "dep"} {
		if strings.Contains(log, sideEffect) {
			t.Fatalf("bd side effect %q ran before target-rig database validation rejected the bead; log:\n%s", sideEffect, log)
		}
	}
}

func TestBatchSlingRejectsMissingTargetRigDatabaseBeforeSpawn(t *testing.T) {
	townRoot, _ := setupCrossDatabaseSlingGuardTest(t)

	prevDryRun := slingDryRun
	prevForce := slingForce
	prevSpawn := spawnPolecatForSling
	t.Cleanup(func() {
		slingDryRun = prevDryRun
		slingForce = prevForce
		spawnPolecatForSling = prevSpawn
	})
	slingDryRun = false
	slingForce = false

	spawnCalled := false
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		spawnCalled = true
		return &SpawnedPolecatInfo{RigName: rigName, PolecatName: "toast", ClonePath: filepath.Join(townRoot, "fake-polecat")}, nil
	}

	err := runBatchSling([]string{"gt-r2405"}, "gastown", filepath.Join(townRoot, ".beads"))
	if err == nil {
		t.Fatal("expected target-rig database validation error")
	}
	if !strings.Contains(err.Error(), "not present in target rig") {
		t.Fatalf("unexpected error: %v", err)
	}
	if spawnCalled {
		t.Fatal("spawnPolecatForSling was called before target-rig database validation rejected the bead")
	}
}

func TestSchedulerRejectsReviewOnlyForEpicConvoy(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("review-only", false, "")
	if err := cmd.Flags().Set("review-only", "true"); err != nil {
		t.Fatalf("set review-only flag: %v", err)
	}

	for _, mode := range []string{"epic", "convoy"} {
		err := validateNoTaskOnlySchedulerFlags(cmd, mode)
		if err == nil {
			t.Fatalf("validateNoTaskOnlySchedulerFlags(%s) accepted --review-only", mode)
		}
		if !strings.Contains(err.Error(), "--review-only") {
			t.Fatalf("validateNoTaskOnlySchedulerFlags(%s) error = %v, want --review-only", mode, err)
		}
	}
}

func TestExecuteSlingRejectsMissingTargetRigDatabaseBeforeSpawn(t *testing.T) {
	townRoot, _ := setupCrossDatabaseSlingGuardTest(t)

	prevSpawn := spawnPolecatForSling
	t.Cleanup(func() { spawnPolecatForSling = prevSpawn })

	spawnCalled := false
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		spawnCalled = true
		return &SpawnedPolecatInfo{RigName: rigName, PolecatName: "toast", ClonePath: filepath.Join(townRoot, "fake-polecat")}, nil
	}

	_, err := executeSling(SlingParams{
		BeadID:   "gt-r2405",
		RigName:  "gastown",
		TownRoot: townRoot,
		BeadsDir: filepath.Join(townRoot, ".beads"),
	})
	if err == nil {
		t.Fatal("expected target-rig database validation error")
	}
	if !strings.Contains(err.Error(), "not present in target rig") {
		t.Fatalf("unexpected error: %v", err)
	}
	if spawnCalled {
		t.Fatal("spawnPolecatForSling was called before target-rig database validation rejected the bead")
	}
}

func TestResolveTargetRejectsLivePolecatMissingTargetRigDatabase(t *testing.T) {
	townRoot, _ := setupCrossDatabaseSlingGuardTest(t)

	prevResolve := resolveTargetAgentFn
	t.Cleanup(func() { resolveTargetAgentFn = prevResolve })
	resolveTargetAgentFn = func(target string) (string, string, string, error) {
		return "gastown/polecats/toast", "%1", filepath.Join(townRoot, "gastown", "polecats", "toast"), nil
	}

	for _, target := range []string{"gastown/polecats/toast", "gastown/toast", "gt-gastown-polecat-toast"} {
		t.Run(target, func(t *testing.T) {
			_, err := resolveTarget(target, ResolveTargetOptions{
				BeadID:   "gt-r2405",
				TownRoot: townRoot,
			})
			if err == nil {
				t.Fatal("expected target-rig database validation error")
			}
			if !strings.Contains(err.Error(), "not present in target rig") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestResolveTargetCreateSpawnsPolecatShorthandWhenPaneMissing(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	prevResolve := resolveTargetAgentFn
	prevSpawn := spawnPolecatForSling
	t.Cleanup(func() {
		resolveTargetAgentFn = prevResolve
		spawnPolecatForSling = prevSpawn
	})
	resolveTargetAgentFn = func(target string) (string, string, string, error) {
		return "", "", "", errors.New("getting pane for gt-toast: exit status 1")
	}

	spawnCalled := false
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		spawnCalled = true
		if rigName != "gastown" {
			t.Fatalf("rigName = %q, want gastown", rigName)
		}
		if !opts.Create {
			t.Fatal("expected Create option to be preserved")
		}
		return &SpawnedPolecatInfo{RigName: rigName, PolecatName: "toast", ClonePath: filepath.Join(townRoot, "fake-polecat")}, nil
	}

	got, err := resolveTarget("gastown/toast", ResolveTargetOptions{Create: true, NoBoot: true})
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if !spawnCalled {
		t.Fatal("expected spawnPolecatForSling to be called")
	}
	if got.Agent != "gastown/polecats/toast" {
		t.Fatalf("Agent = %q, want gastown/polecats/toast", got.Agent)
	}
}

func TestResolveTargetCreateDoesNotSpawnCrewShorthandWhenPaneMissing(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", "crew", "toast"), 0755); err != nil {
		t.Fatalf("mkdir crew: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	prevResolve := resolveTargetAgentFn
	prevSpawn := spawnPolecatForSling
	t.Cleanup(func() {
		resolveTargetAgentFn = prevResolve
		spawnPolecatForSling = prevSpawn
	})
	resolveTargetAgentFn = func(target string) (string, string, string, error) {
		return "", "", "", errors.New("getting pane for gt-crew-toast: exit status 1")
	}

	spawnCalled := false
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		spawnCalled = true
		return nil, errors.New("unexpected spawn")
	}

	_, err = resolveTarget("gastown/toast", ResolveTargetOptions{Create: true, NoBoot: true})
	if err == nil {
		t.Fatal("expected resolve error for missing crew pane")
	}
	if spawnCalled {
		t.Fatal("crew shorthand must not spawn a polecat")
	}
}

func TestTargetRigDatabaseLookupFailsClosedWithoutTownRoot(t *testing.T) {
	err := verifyBeadExistsInTargetRigDatabase("gt-r2405", "gastown", "")
	if err == nil {
		t.Fatal("expected fail-closed error without town root")
	}
	if !strings.Contains(err.Error(), "town root is unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRollbackSlingArtifactsBurnsAttachedMolecules(t *testing.T) {
	townRoot, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
set -e
cmd="$1"
shift || true
case "$cmd" in
  update)
    exit 0
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
set "cmd=%1"
if "%cmd%"=="update" exit /b 0
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	prevGetBead := getBeadInfoForRollback
	prevCollect := collectExistingMoleculesForRollback
	prevBurn := burnExistingMoleculesForRollback
	t.Cleanup(func() {
		getBeadInfoForRollback = prevGetBead
		collectExistingMoleculesForRollback = prevCollect
		burnExistingMoleculesForRollback = prevBurn
	})

	getBeadInfoForRollback = func(beadID string) (*beadInfo, error) {
		if beadID != "gt-abc123" {
			t.Fatalf("unexpected bead id: %q", beadID)
		}
		return &beadInfo{
			Description: "attached_molecule: gt-wisp-stale",
			Dependencies: []beads.IssueDep{
				{ID: "gt-wisp-stale"},
			},
		}, nil
	}
	collectExistingMoleculesForRollback = collectExistingMolecules

	burnCalled := false
	burnExistingMoleculesForRollback = func(molecules []string, beadID, gotTownRoot string) error {
		burnCalled = true
		if beadID != "gt-abc123" {
			t.Fatalf("unexpected burn bead id: %q", beadID)
		}
		if gotTownRoot != townRoot {
			t.Fatalf("unexpected town root: got %q want %q", gotTownRoot, townRoot)
		}
		if len(molecules) != 1 || molecules[0] != "gt-wisp-stale" {
			t.Fatalf("unexpected molecules to burn: %#v", molecules)
		}
		return nil
	}

	rollbackSlingArtifacts(&SpawnedPolecatInfo{
		RigName:     "gastown",
		PolecatName: "Toast",
	}, "gt-abc123", "", "")

	if !burnCalled {
		t.Fatalf("expected rollbackSlingArtifacts to burn attached molecules")
	}
}

func TestRollbackSlingArtifactsClearsRawReviewOnlyMetadata(t *testing.T) {
	initial := strings.Join([]string{
		"attached_at: 2026-06-30T12:00:00Z",
		"no_merge: true",
		"review_only: true",
		"dispatched_by: mayor/",
		"",
		"Keep this body.",
	}, "\n")
	townRoot, _, descPath := setupMutableBDRawSlingTest(t, initial)

	rollbackSlingArtifacts(&SpawnedPolecatInfo{
		RigName:     "gastown",
		PolecatName: "toast",
		ClonePath:   filepath.Join(townRoot, "gastown", "polecats", "toast"),
	}, "gt-rawrollback", filepath.Join(townRoot, "gastown", "polecats", "toast"), "")

	desc := readMutableBDDescription(t, descPath)
	assertNoRawReviewMetadata(t, desc)
	if !strings.Contains(desc, "dispatched_by: mayor/") {
		t.Fatalf("rollback did not preserve unrelated dispatch metadata:\n%s", desc)
	}
	if !strings.Contains(desc, "Keep this body.") {
		t.Fatalf("rollback did not preserve description body:\n%s", desc)
	}
}

func TestRollbackSlingArtifactsKeepsMetadataWhenMoleculeBurnFails(t *testing.T) {
	initial := strings.Join([]string{
		"attached_molecule: gt-wisp-stale",
		"attached_at: 2026-06-30T12:00:00Z",
		"no_merge: true",
		"review_only: true",
		"",
		"Keep this body.",
	}, "\n")
	townRoot, _, descPath := setupMutableBDRawSlingTest(t, initial)

	prevCollect := collectExistingMoleculesForRollback
	prevBurn := burnExistingMoleculesForRollback
	t.Cleanup(func() {
		collectExistingMoleculesForRollback = prevCollect
		burnExistingMoleculesForRollback = prevBurn
	})
	collectExistingMoleculesForRollback = func(info *beadInfo) []string {
		return []string{"gt-wisp-stale"}
	}
	burnExistingMoleculesForRollback = func(molecules []string, beadID, townRoot string) error {
		return errors.New("forced burn failure")
	}

	rollbackSlingArtifacts(&SpawnedPolecatInfo{
		RigName:     "gastown",
		PolecatName: "toast",
		ClonePath:   filepath.Join(townRoot, "gastown", "polecats", "toast"),
	}, "gt-rawrollback", filepath.Join(townRoot, "gastown", "polecats", "toast"), "")

	desc := readMutableBDDescription(t, descPath)
	if !strings.Contains(desc, "attached_molecule: gt-wisp-stale") {
		t.Fatalf("rollback hid attached molecule after burn failure:\n%s", desc)
	}
	assertHasRawReviewMetadata(t, desc)
}

func TestRollbackSlingArtifactsClearsRawReviewOnlyMetadataAfterMoleculeBurnSucceeds(t *testing.T) {
	initial := strings.Join([]string{
		"attached_molecule: gt-wisp-stale",
		"attached_at: 2026-06-30T12:00:00Z",
		"no_merge: true",
		"review_only: true",
		"",
		"Keep this body.",
	}, "\n")
	townRoot, _, descPath := setupMutableBDRawSlingTest(t, initial)

	prevCollect := collectExistingMoleculesForRollback
	prevBurn := burnExistingMoleculesForRollback
	t.Cleanup(func() {
		collectExistingMoleculesForRollback = prevCollect
		burnExistingMoleculesForRollback = prevBurn
	})
	collectExistingMoleculesForRollback = func(info *beadInfo) []string {
		return []string{"gt-wisp-stale"}
	}
	burnExistingMoleculesForRollback = func(molecules []string, beadID, townRoot string) error {
		if len(molecules) != 1 || molecules[0] != "gt-wisp-stale" {
			t.Fatalf("unexpected molecules: %#v", molecules)
		}
		updated := strings.Join([]string{
			"attached_at: 2026-06-30T12:00:00Z",
			"no_merge: true",
			"review_only: true",
			"",
			"Keep this body.",
		}, "\n")
		if err := os.WriteFile(descPath, []byte(updated), 0644); err != nil {
			t.Fatalf("simulate molecule burn: %v", err)
		}
		return nil
	}

	rollbackSlingArtifacts(&SpawnedPolecatInfo{
		RigName:     "gastown",
		PolecatName: "toast",
		ClonePath:   filepath.Join(townRoot, "gastown", "polecats", "toast"),
	}, "gt-rawrollback", filepath.Join(townRoot, "gastown", "polecats", "toast"), "")

	desc := readMutableBDDescription(t, descPath)
	assertNoRawReviewMetadata(t, desc)
	if strings.Contains(desc, "attached_molecule: gt-wisp-stale") {
		t.Fatalf("rollback reintroduced stale attached molecule:\n%s", desc)
	}
	if !strings.Contains(desc, "Keep this body.") {
		t.Fatalf("rollback did not preserve description body:\n%s", desc)
	}
}

func TestRestoreRollbackRawWorkflowFieldsFromCurrentRestoresOriginalValues(t *testing.T) {
	current := strings.Join([]string{
		"no_merge: true",
		"review_only: true",
		"dispatched_by: mayor/",
		"",
		"Keep this body.",
	}, "\n")
	townRoot, _, descPath := setupMutableBDRawSlingTest(t, current)
	original := &beadInfo{Description: strings.Join([]string{
		"no_merge: true",
		"",
		"Original body.",
	}, "\n")}

	restoreRollbackRawWorkflowFieldsFromCurrent("gt-rawrollback", townRoot, filepath.Join(townRoot, "gastown", "polecats", "toast"), original)

	desc := readMutableBDDescription(t, descPath)
	fields := beads.ParseAttachmentFields(&beads.Issue{Description: desc})
	if fields == nil || !fields.NoMerge || fields.ReviewOnly {
		t.Fatalf("rollback did not restore original workflow values: %+v\n%s", fields, desc)
	}
	if !strings.Contains(desc, "dispatched_by: mayor/") || !strings.Contains(desc, "Keep this body.") {
		t.Fatalf("rollback did not preserve current metadata/body:\n%s", desc)
	}
}

func TestRunSlingRawReviewOnlyExistingTargetHookFailureClearsPreHookMetadata(t *testing.T) {
	townRoot, _, descPath := setupMutableBDRawSlingTest(t, "Keep this body.")
	workDir := filepath.Join(townRoot, "gastown", "crew", "toast")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("mkdir workDir: %v", err)
	}

	prevHookRaw := slingHookRawBead
	prevNoMerge := slingNoMerge
	prevReviewOnly := slingReviewOnly
	prevNoConvoy := slingNoConvoy
	prevDryRun := slingDryRun
	prevResolve := resolveTargetAgentFn
	prevHook := hookBeadWithRetryFn
	t.Cleanup(func() {
		slingHookRawBead = prevHookRaw
		slingNoMerge = prevNoMerge
		slingReviewOnly = prevReviewOnly
		slingNoConvoy = prevNoConvoy
		slingDryRun = prevDryRun
		resolveTargetAgentFn = prevResolve
		hookBeadWithRetryFn = prevHook
	})
	slingHookRawBead = true
	slingNoMerge = true
	slingReviewOnly = true
	slingNoConvoy = true
	slingDryRun = false
	resolveTargetAgentFn = func(target string) (string, string, string, error) {
		return "gastown/crew/toast", "", workDir, nil
	}
	hookBeadWithRetryFn = func(beadID, targetAgent, hookDir string) error {
		assertHasRawReviewMetadata(t, readMutableBDDescription(t, descPath))
		return errors.New("forced hook failure")
	}

	err := runSling(nil, []string{"gt-rawrollback", "gastown/crew/toast"})
	if err == nil {
		t.Fatal("expected hook failure from runSling")
	}
	desc := readMutableBDDescription(t, descPath)
	assertNoRawReviewMetadata(t, desc)
	if !strings.Contains(desc, "Keep this body.") {
		t.Fatalf("rollback did not preserve description body:\n%s", desc)
	}
}

func TestExecuteSlingRawReviewOnlyHookFailureClearsPreHookMetadata(t *testing.T) {
	townRoot, rigPath, descPath := setupMutableBDRawSlingTest(t, "Keep this body.")

	prevSpawn := spawnPolecatForSling
	prevHook := hookBeadWithRetryWithTownRootFn
	t.Cleanup(func() {
		spawnPolecatForSling = prevSpawn
		hookBeadWithRetryWithTownRootFn = prevHook
	})
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		return &SpawnedPolecatInfo{
			RigName:     rigName,
			PolecatName: "toast",
			ClonePath:   filepath.Join(townRoot, "gastown", "polecats", "toast"),
		}, nil
	}
	hookBeadWithRetryWithTownRootFn = func(beadID, targetAgent, hookDir, townRoot string) error {
		assertHasRawReviewMetadata(t, readMutableBDDescription(t, descPath))
		return errors.New("forced hook failure")
	}

	_, err := executeSling(SlingParams{
		BeadID:      "gt-rawrollback",
		RigName:     "gastown",
		TownRoot:    townRoot,
		BeadsDir:    filepath.Join(rigPath, ".beads"),
		HookRawBead: true,
		NoMerge:     true,
		ReviewOnly:  true,
		NoConvoy:    true,
		NoBoot:      true,
	})
	if err == nil {
		t.Fatal("expected hook failure from executeSling")
	}
	assertNoRawReviewMetadata(t, readMutableBDDescription(t, descPath))
}

func TestExecuteSlingRawReviewOnlyHookFailureRestoresOriginalMetadata(t *testing.T) {
	initial := strings.Join([]string{
		"no_merge: true",
		"",
		"Keep this body.",
	}, "\n")
	townRoot, rigPath, descPath := setupMutableBDRawSlingTest(t, initial)

	prevSpawn := spawnPolecatForSling
	prevHook := hookBeadWithRetryWithTownRootFn
	t.Cleanup(func() {
		spawnPolecatForSling = prevSpawn
		hookBeadWithRetryWithTownRootFn = prevHook
	})
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		return &SpawnedPolecatInfo{
			RigName:     rigName,
			PolecatName: "toast",
			ClonePath:   filepath.Join(townRoot, "gastown", "polecats", "toast"),
		}, nil
	}
	hookBeadWithRetryWithTownRootFn = func(beadID, targetAgent, hookDir, townRoot string) error {
		assertHasRawReviewMetadata(t, readMutableBDDescription(t, descPath))
		return errors.New("forced hook failure")
	}

	_, err := executeSling(SlingParams{
		BeadID:      "gt-rawrollback",
		RigName:     "gastown",
		TownRoot:    townRoot,
		BeadsDir:    filepath.Join(rigPath, ".beads"),
		HookRawBead: true,
		NoMerge:     true,
		ReviewOnly:  true,
		NoConvoy:    true,
		NoBoot:      true,
	})
	if err == nil {
		t.Fatal("expected hook failure from executeSling")
	}
	desc := readMutableBDDescription(t, descPath)
	fields := beads.ParseAttachmentFields(&beads.Issue{Description: desc})
	if fields == nil || !fields.NoMerge || fields.ReviewOnly {
		t.Fatalf("rollback did not restore original asymmetric metadata: %+v\n%s", fields, desc)
	}
	if !strings.Contains(desc, "Keep this body.") {
		t.Fatalf("rollback did not preserve description body:\n%s", desc)
	}
}

func TestExecuteSlingRawReviewOnlySuccessKeepsMetadata(t *testing.T) {
	townRoot, rigPath, descPath := setupMutableBDRawSlingTest(t, "Keep this body.")

	prevSpawn := spawnPolecatForSling
	prevHook := hookBeadWithRetryWithTownRootFn
	t.Cleanup(func() {
		spawnPolecatForSling = prevSpawn
		hookBeadWithRetryWithTownRootFn = prevHook
	})
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		return &SpawnedPolecatInfo{
			RigName:     rigName,
			PolecatName: "toast",
			ClonePath:   filepath.Join(townRoot, "gastown", "polecats", "toast"),
			Pane:        "%1",
		}, nil
	}
	hookBeadWithRetryWithTownRootFn = func(beadID, targetAgent, hookDir, townRoot string) error {
		assertHasRawReviewMetadata(t, readMutableBDDescription(t, descPath))
		return nil
	}

	result, err := executeSling(SlingParams{
		BeadID:      "gt-rawrollback",
		RigName:     "gastown",
		TownRoot:    townRoot,
		BeadsDir:    filepath.Join(rigPath, ".beads"),
		HookRawBead: true,
		NoMerge:     true,
		ReviewOnly:  true,
		NoConvoy:    true,
		NoBoot:      true,
	})
	if err != nil {
		t.Fatalf("executeSling succeeded with error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("executeSling result not successful: %+v", result)
	}
	assertHasRawReviewMetadata(t, readMutableBDDescription(t, descPath))
}

func TestSlingFormulaRollsBackSpawnedPolecatOnWispFailure(t *testing.T) {
	townRoot := t.TempDir()

	// Minimal workspace marker so workspace.FindFromCwd() succeeds.
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Register rig so IsRigName("gastown") succeeds.
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigs := &config.RigsConfig{
		Version: 1,
		Rigs: map[string]config.RigEntry{
			"gastown": {
				GitURL:    "git@github.com:test/gastown.git",
				LocalRepo: "",
				AddedAt:   time.Now().Truncate(time.Second),
				BeadsConfig: &config.BeadsConfig{
					Repo:   "local",
					Prefix: "gt-",
				},
			},
		},
	}
	if err := config.SaveRigsConfig(rigsPath, rigs); err != nil {
		t.Fatalf("SaveRigsConfig: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir rig beads dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir rig dir: %v", err)
	}

	// Stub bd: cook succeeds; mol wisp fails to simulate missing required vars.
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
set -e
cmd="$1"
shift || true
case "$cmd" in
  cook)
    exit 0
    ;;
  mol)
    sub="$1"
    shift || true
    case "$sub" in
      wisp)
        echo "missing required vars" 1>&2
        exit 1
        ;;
    esac
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
setlocal enableextensions
set "cmd=%1"
set "sub=%2"
if "%cmd%"=="cook" exit /b 0
if "%cmd%"=="mol" (
  if "%sub%"=="wisp" (
    echo missing required vars 1>&2
    exit /b 1
  )
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("GT_TEST_NO_NUDGE", "1")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Ensure we don't leak global flag/seam state across tests.
	prevNoBoot := slingNoBoot
	prevDryRun := slingDryRun
	prevSpawn := spawnPolecatForSling
	prevRollback := rollbackSlingArtifactsFn
	t.Cleanup(func() {
		slingNoBoot = prevNoBoot
		slingDryRun = prevDryRun
		spawnPolecatForSling = prevSpawn
		rollbackSlingArtifactsFn = prevRollback
	})

	slingDryRun = false
	slingNoBoot = true

	fakeWorkDir := filepath.Join(townRoot, "fake-polecat")
	if err := os.MkdirAll(fakeWorkDir, 0755); err != nil {
		t.Fatalf("mkdir fakeWorkDir: %v", err)
	}
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		return &SpawnedPolecatInfo{
			RigName:     rigName,
			PolecatName: "Toast",
			ClonePath:   fakeWorkDir,
		}, nil
	}

	rollbackCalled := false
	rollbackSlingArtifactsFn = func(spawnInfo *SpawnedPolecatInfo, beadID, hookWorkDir, convoyID string) {
		rollbackCalled = true
		if spawnInfo == nil || spawnInfo.PolecatName != "Toast" {
			t.Fatalf("unexpected spawnInfo in rollback: %+v", spawnInfo)
		}
		if beadID != "" {
			t.Fatalf("unexpected beadID in rollback: %q", beadID)
		}
		if hookWorkDir != fakeWorkDir {
			t.Fatalf("unexpected hookWorkDir in rollback: got %q want %q", hookWorkDir, fakeWorkDir)
		}
	}

	err = runSlingFormula(context.Background(), []string{"mol-anything", "gastown"})
	if err == nil {
		t.Fatalf("expected error from runSlingFormula")
	}
	if !rollbackCalled {
		t.Fatalf("expected rollbackSlingArtifactsFn to be called")
	}
}

func TestRunSlingFormulaPersistsVarContext(t *testing.T) {
	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}

	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -e
echo "$PWD|$*" >> "${BD_LOG}"
cmd="$1"
shift || true
case "$cmd" in
  formula)
    echo '{"name":"mol-anything"}'
    ;;
  cook)
    exit 0
    ;;
  mol)
    sub="$1"
    shift || true
    case "$sub" in
      wisp)
        echo '{"new_epic_id":"gt-wisp-xyz"}'
        ;;
    esac
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
setlocal enableextensions
echo %CD%^|%*>>"%BD_LOG%"
set "cmd=%1"
set "sub=%2"
if "%cmd%"=="formula" (
  echo {"name":"mol-anything"}
  exit /b 0
)
if "%cmd%"=="cook" exit /b 0
if "%cmd%"=="mol" (
  if "%sub%"=="wisp" (
    echo {"new_epic_id":"gt-wisp-xyz"}
    exit /b 0
  )
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	attachedLogPath := filepath.Join(townRoot, "attached-molecule.log")
	t.Setenv("GT_TEST_ATTACHED_MOLECULE_LOG", attachedLogPath)
	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("GT_TEST_NO_NUDGE", "1")
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	prevVars := slingVars
	prevDryRun := slingDryRun
	prevNoBoot := slingNoBoot
	prevRalph := slingRalph
	t.Cleanup(func() {
		slingVars = prevVars
		slingDryRun = prevDryRun
		slingNoBoot = prevNoBoot
		slingRalph = prevRalph
	})

	slingVars = []string{"version=1.2.3", "channel=stable"}
	slingDryRun = false
	slingNoBoot = true
	slingRalph = true

	if err := runSlingFormula(context.Background(), []string{"mol-anything"}); err != nil {
		t.Fatalf("runSlingFormula: %v", err)
	}

	attachmentBytes, err := os.ReadFile(attachedLogPath)
	if err != nil {
		t.Fatalf("read attachment log: %v", err)
	}
	attachment := string(attachmentBytes)

	if !strings.Contains(attachment, "attached_formula: mol-anything") {
		t.Fatalf("formula attachment missing from persisted description:\n%s", attachment)
	}
	if !strings.Contains(attachment, "version=1.2.3") || !strings.Contains(attachment, "channel=stable") {
		t.Fatalf("formula vars missing from persisted description:\n%s", attachment)
	}
	if !strings.Contains(attachment, "mode: ralph") {
		t.Fatalf("ralph mode missing from persisted standalone formula description:\n%s", attachment)
	}
}

func TestRunSlingFormulaNoOpWhenSameFormulaAlreadyHooked(t *testing.T) {
	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}

	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -e
echo "$PWD|$*" >> "${BD_LOG}"
cmd="$1"
shift || true
case "$cmd" in
  cook|mol|update)
    exit 0
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
setlocal enableextensions
echo %CD%^|%*>>"%BD_LOG%"
set "cmd=%1"
if "%cmd%"=="cook" exit /b 0
if "%cmd%"=="mol" exit /b 0
if "%cmd%"=="update" exit /b 0
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("GT_TEST_NO_NUDGE", "1")
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	prevDryRun := slingDryRun
	prevNoBoot := slingNoBoot
	prevForce := slingForce
	prevFindSingleton := findHookedFormulaSingletonFn
	t.Cleanup(func() {
		slingDryRun = prevDryRun
		slingNoBoot = prevNoBoot
		slingForce = prevForce
		findHookedFormulaSingletonFn = prevFindSingleton
	})

	slingDryRun = false
	slingNoBoot = true
	slingForce = false
	findHookedFormulaSingletonFn = func(workDir, targetAgent, formulaName string) (*beads.Issue, error) {
		return &beads.Issue{ID: "gt-wisp-existing"}, nil
	}

	if err := runSlingFormula(context.Background(), []string{"mol-anything"}); err != nil {
		t.Fatalf("runSlingFormula: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read bd log: %v", err)
	}
	log := string(logBytes)

	if strings.Contains(log, "cook ") || strings.Contains(log, "mol wisp") || strings.Contains(log, "update ") {
		t.Fatalf("expected same-formula sling to no-op before creating a new wisp, got:\n%s", log)
	}
}

func TestRunSlingFormulaUpdatesModeWhenSameFormulaAlreadyHooked(t *testing.T) {
	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
exit 0
`
	bdScriptWindows := `@echo off
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	attachedLogPath := filepath.Join(townRoot, "attached-molecule.log")
	t.Setenv("GT_TEST_ATTACHED_MOLECULE_LOG", attachedLogPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("GT_TEST_NO_NUDGE", "1")
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	prevDryRun := slingDryRun
	prevNoBoot := slingNoBoot
	prevForce := slingForce
	prevRalph := slingRalph
	prevFindSingleton := findHookedFormulaSingletonFn
	t.Cleanup(func() {
		slingDryRun = prevDryRun
		slingNoBoot = prevNoBoot
		slingForce = prevForce
		slingRalph = prevRalph
		findHookedFormulaSingletonFn = prevFindSingleton
	})

	slingDryRun = false
	slingNoBoot = true
	slingForce = false
	slingRalph = false
	findHookedFormulaSingletonFn = func(workDir, targetAgent, formulaName string) (*beads.Issue, error) {
		return &beads.Issue{ID: "gt-wisp-existing", Description: "attached_formula: mol-anything\nmode: ralph"}, nil
	}

	if err := runSlingFormula(context.Background(), []string{"mol-anything"}); err != nil {
		t.Fatalf("runSlingFormula: %v", err)
	}

	attachmentBytes, err := os.ReadFile(attachedLogPath)
	if err != nil {
		t.Fatalf("read attachment log: %v", err)
	}
	if strings.Contains(string(attachmentBytes), "mode: ralph") {
		t.Fatalf("same-formula normal sling should clear stale ralph mode, got:\n%s", string(attachmentBytes))
	}
}

// TestSlingFormulaOnBeadPassesFeatureAndIssueVars verifies that when using
// gt sling <formula> --on <bead>, both --var feature=<title> and --var issue=<beadID>
// are passed to the canonical bd mol bond command.
func TestSlingFormulaOnBeadPassesFeatureAndIssueVars(t *testing.T) {
	townRoot := t.TempDir()

	// Minimal workspace marker so workspace.FindFromCwd() succeeds.
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Create a rig path that owns gt-* beads, and a routes.jsonl pointing to it.
	rigDir := filepath.Join(townRoot, "gastown", "mayor", "rig")
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatalf("mkdir rigDir: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown/mayor/rig"}`,
		`{"prefix":"hq-","path":"."}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	// Stub bd so we can observe the arguments passed to mol bond.
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	// The stub returns a specific title so we can verify it appears in --var feature=
	bdScript := `#!/bin/sh
set -e
echo "ARGS:$*" >> "${BD_LOG}"
cmd="$1"
shift || true
case "$cmd" in
  show)
    echo '[{"title":"My Test Feature","status":"open","assignee":"","description":""}]'
    ;;
  formula)
    # formula show <name> - must output something for verifyFormulaExists
    echo '{"name":"mol-review"}'
    exit 0
    ;;
  cook)
    exit 0
    ;;
	  mol)
		sub="$1"
		shift || true
		case "$sub" in
		  wisp)
			echo 'legacy mol wisp should not be called' >&2
			exit 1
			;;
		  bond)
			echo '{"result_id":"gt-abc123","id_mapping":{"mol-review":"gt-wisp-xyz"}}'
			;;
		esac
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
setlocal enableextensions
echo ARGS:%*>>"%BD_LOG%"
set "cmd=%1"
set "sub=%2"
if "%cmd%"=="show" (
  echo [{^"title^":^"My Test Feature^",^"status^":^"open^",^"assignee^":^"^",^"description^":^"^"}]
  exit /b 0
)
if "%cmd%"=="formula" (
  echo {^"name^":^"mol-review^"}
  exit /b 0
)
if "%cmd%"=="cook" exit /b 0
if "%cmd%"=="mol" (
  if "%sub%"=="wisp" (
    echo legacy mol wisp should not be called 1>&2
    exit /b 1
  )
  if "%sub%"=="bond" (
    echo {^"result_id^":^"gt-abc123^",^"id_mapping^":{^"mol-review^":^"gt-wisp-xyz^"}}
    exit /b 0
  )
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	attachedLogPath := filepath.Join(townRoot, "attached-molecule.log")
	t.Setenv("GT_TEST_ATTACHED_MOLECULE_LOG", attachedLogPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "") // Prevent inheriting real tmux pane from test runner

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Ensure we don't leak global flag state across tests.
	prevOn := slingOnTarget
	prevVars := slingVars
	prevDryRun := slingDryRun
	prevNoConvoy := slingNoConvoy
	t.Cleanup(func() {
		slingOnTarget = prevOn
		slingVars = prevVars
		slingDryRun = prevDryRun
		slingNoConvoy = prevNoConvoy
	})

	slingDryRun = false
	slingNoConvoy = true
	slingVars = nil
	slingOnTarget = "gt-abc123"

	// Prevent real tmux nudge from firing during tests (causes agent self-interruption)
	t.Setenv("GT_TEST_NO_NUDGE", "1")
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1") // Stub bd doesn't track state

	if err := runSling(nil, []string{"mol-review"}); err != nil {
		t.Fatalf("runSling: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}

	// Find the mol bond command and verify both --var arguments
	logLines := strings.Split(string(logBytes), "\n")
	var bondLine string
	for _, line := range logLines {
		if strings.Contains(line, "mol bond") {
			bondLine = line
			break
		}
	}

	if bondLine == "" {
		t.Fatalf("mol bond command not found in log: %s", string(logBytes))
	}

	// Verify --var feature=<title> is present
	if !containsVarArg(bondLine, "feature", "My Test Feature") {
		t.Errorf("mol bond missing --var feature=<title>\ngot: %s", bondLine)
	}

	// Verify --var issue=<beadID> is present
	if !containsVarArg(bondLine, "issue", "gt-abc123") {
		t.Errorf("mol bond missing --var issue=<beadID>\ngot: %s", bondLine)
	}
}

// TestVerifyBeadExistsAllowStale reproduces the bug in gtl-ncq where beads
// visible via regular bd show fail due to database staleness.
// The fix uses --allow-stale to skip the staleness check for existence verification.
func TestVerifyBeadExistsAllowStale(t *testing.T) {
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)

	townRoot := t.TempDir()

	// Create minimal workspace structure
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Create a stub bd that always succeeds for "show" commands.
	// The real test is that verifyBeadExists calls bd show and parses
	// the output correctly. The --allow-stale flag may or may not be
	// present depending on BdSupportsAllowStale() cache state.
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
set -e
cmd="$1"
shift || true
# Strip --allow-stale if present (it's a global flag before subcommand)
if [ "$cmd" = "--allow-stale" ]; then
  cmd="$1"
  shift || true
fi
case "$cmd" in
  show)
    echo '[{"title":"Test bead","status":"open","assignee":""}]'
    ;;
  version)
    echo "bd 0.1.0"
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
set "cmd=%1"
if "%cmd%"=="--allow-stale" set "cmd=%2"
if "%cmd%"=="show" (
  echo [{"title":"Test bead","status":"open","assignee":""}]
  exit /b 0
)
if "%cmd%"=="version" (
  echo bd 0.1.0
  exit /b 0
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// EXPECTED: verifyBeadExists should use --allow-stale and succeed
	beadID := "jv-v599"
	err = verifyBeadExists(beadID)
	if err != nil {
		t.Errorf("verifyBeadExists(%q) failed: %v\nExpected --allow-stale to skip sync check", beadID, err)
	}
}

// TestSlingWithAllowStale tests the full gt sling flow with --allow-stale fix.
// This is an integration test for the gtl-ncq bug.
func TestSlingWithAllowStale(t *testing.T) {
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)

	townRoot := t.TempDir()

	// Create minimal workspace structure
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Create stub bd that handles show/update commands.
	// The --allow-stale flag may or may not be present depending on
	// BdSupportsAllowStale() cache state, so we accept it either way.
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
set -e
cmd="$1"
shift || true
# Strip --allow-stale if present (it's a global flag before subcommand)
if [ "$cmd" = "--allow-stale" ]; then
  cmd="$1"
  shift || true
fi
case "$cmd" in
  show)
    echo '[{"title":"Synced bead","status":"open","assignee":""}]'
    ;;
  update)
    exit 0
    ;;
  version)
    echo "bd 0.1.0"
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
set "cmd=%1"
if "%cmd%"=="--allow-stale" set "cmd=%2"
if "%cmd%"=="show" (
  echo [{"title":"Synced bead","status":"open","assignee":""}]
  exit /b 0
)
if "%cmd%"=="update" exit /b 0
if "%cmd%"=="version" (
  echo bd 0.1.0
  exit /b 0
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "crew")
	t.Setenv("GT_CREW", "jv")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("TMUX_PANE", "") // Prevent inheriting real tmux pane from test runner

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Save and restore global flags
	prevDryRun := slingDryRun
	prevNoConvoy := slingNoConvoy
	t.Cleanup(func() {
		slingDryRun = prevDryRun
		slingNoConvoy = prevNoConvoy
	})

	slingDryRun = true
	slingNoConvoy = true

	// Prevent real tmux nudge from firing during tests (causes agent self-interruption)
	t.Setenv("GT_TEST_NO_NUDGE", "1")

	// EXPECTED: gt sling should use --allow-stale and succeed
	beadID := "jv-v599"
	err = runSling(nil, []string{beadID})
	if err != nil {
		// Check if it's the specific error we're testing for
		if strings.Contains(err.Error(), "is not a valid bead or formula") {
			t.Errorf("gt sling failed to recognize bead %q: %v\nExpected --allow-stale to skip sync check", beadID, err)
		} else {
			// Some other error - might be expected in dry-run mode
			t.Logf("gt sling returned error (may be expected in test): %v", err)
		}
	}
}

func TestBdCmdStripsUnsupportedAllowStale(t *testing.T) {
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)

	townRoot := t.TempDir()
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}

	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
printf '%s\n' "$*" >> "$BD_LOG"
for arg in "$@"; do
  if [ "$arg" = "--allow-stale" ]; then
    echo "Error: unknown flag: --allow-stale" >&2
    exit 1
  fi
done
case "$1" in
  show)
    echo '[{"title":"Routed bead","status":"open","assignee":""}]'
    ;;
  version)
    echo "bd version 1.0.3"
    ;;
  *)
    exit 1
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
>>"%BD_LOG%" echo %*
for %%A in (%*) do if "%%~A"=="--allow-stale" (
  echo Error: unknown flag: --allow-stale 1>&2
  exit /b 1
)
if "%1"=="show" (
  echo [{"title":"Routed bead","status":"open","assignee":""}]
  exit /b 0
)
if "%1"=="version" (
  echo bd version 1.0.3
  exit /b 0
)
exit /b 1
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := BdCmd("show", "gt-rca-epic-routing.3", "--json").
		AllowStale().
		Dir(townRoot).
		Output()
	if err != nil {
		t.Fatalf("BdCmd show with unsupported --allow-stale failed: %v", err)
	}
	if !strings.Contains(string(out), "Routed bead") {
		t.Fatalf("BdCmd show output = %q, want routed bead JSON", string(out))
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	log := string(logBytes)
	if strings.Contains(log, "show gt-rca-epic-routing.3 --json --allow-stale") {
		t.Fatalf("BdCmd passed unsupported --allow-stale to show command:\n%s", log)
	}
	if !strings.Contains(log, "show gt-rca-epic-routing.3 --json") {
		t.Fatalf("BdCmd did not run expected show command:\n%s", log)
	}
}

// TestLooksLikeBeadID tests the bead ID pattern recognition function.
// This ensures gt sling accepts bead IDs even when routing-based verification fails.
// Fixes: gt sling bd-ka761 failing with 'not a valid bead or formula'
//
// Note: looksLikeBeadID is a fallback check in sling. The actual sling flow is:
// 1. Try verifyBeadExists (routing-based lookup)
// 2. Try verifyFormulaExists (formula check)
// 3. Fall back to looksLikeBeadID pattern match
// So "mol-release" matches the pattern but won't be treated as bead in practice
// because it would be caught by formula verification first.
func TestLooksLikeBeadID(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		// Valid bead IDs - should return true
		{"gt-abc123", true},
		{"bd-ka761", true},
		{"hq-cv-abc", true},
		{"ap-qtsup.16", true},
		{"beads-xyz", true},
		{"jv-v599", true},
		{"gt-9e8s5", true},
		{"hq-00gyg", true},

		// Short prefixes that match pattern (but may be formulas in practice)
		{"mol-release", true}, // 3-char prefix matches pattern (formula check runs first in sling)
		{"mol-abc123", true},  // 3-char prefix matches pattern

		// Non-bead strings - should return false
		{"formula-name", false}, // "formula" is 7 chars (> 5)
		{"mayor", false},        // no hyphen
		{"gastown", false},      // no hyphen
		{"deacon/dogs", false},  // contains slash
		{"", false},             // empty
		{"-abc", false},         // starts with hyphen
		{"GT-abc", false},       // uppercase prefix
		{"123-abc", false},      // numeric prefix
		{"a-", false},           // nothing after hyphen
		{"aaaaaa-b", false},     // prefix too long (6 chars)

		// Injection / invalid suffix characters - should return false
		{"gt-abc;rm -rf /", false}, // shell injection in suffix
		{"gt-abc$(cmd)", false},    // command substitution in suffix
		{"gt-abc&bg", false},       // ampersand in suffix
		{"gt-abc|pipe", false},     // pipe in suffix
		{"gt-abc`tick`", false},    // backtick in suffix
		{"gt-abc>redir", false},    // redirect in suffix
		{"gt-abc<redir", false},    // redirect in suffix
		{"gt-abc'quote", false},    // single quote in suffix
		{"gt-abc\"dquote", false},  // double quote in suffix
		{"gt-abc\\slash", false},   // backslash in suffix
		{"gt-abc xyz", false},      // space in suffix
		{"gt-ABC", false},          // uppercase in suffix
		{"gt-abc/path", false},     // slash in suffix
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := looksLikeBeadID(tt.input)
			if got != tt.want {
				t.Errorf("looksLikeBeadID(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestSlingFormulaOnBeadSetsAttachedMolecule verifies that when using
// gt sling <formula> --on <bead>, the attached_molecule field is set in the
// hooked bead's description after bonding. This is required for gt hook to
// recognize the molecule attachment.
//
// Bug: The original code bonds the wisp to the bead and sets status=hooked,
// but doesn't record attached_molecule in the description. This causes
// gt hook to report "No molecule attached".
func TestSlingFormulaOnBeadSetsAttachedMolecule(t *testing.T) {
	townRoot := t.TempDir()

	// Minimal workspace marker so workspace.FindFromCwd() succeeds.
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Create a rig path that owns gt-* beads, and a routes.jsonl pointing to it.
	rigDir := filepath.Join(townRoot, "gastown", "mayor", "rig")
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatalf("mkdir rigDir: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown/mayor/rig"}`,
		`{"prefix":"hq-","path":"."}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	// Stub bd so we can observe the arguments passed to update commands.
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	// The stub logs all commands to a file for verification
	bdScript := `#!/bin/sh
set -e
echo "$PWD|$*" >> "${BD_LOG}"
cmd="$1"
shift || true
case "$cmd" in
  show)
    echo '[{"title":"Bug to fix","status":"open","assignee":"","description":""}]'
    ;;
  formula)
    echo '{"name":"mol-polecat-work"}'
    exit 0
    ;;
  cook)
    exit 0
    ;;
  mol)
    sub="$1"
    shift || true
    case "$sub" in
      wisp)
        echo 'legacy mol wisp should not be called' >&2
        exit 1
        ;;
      bond)
        echo '{"result_id":"gt-abc123","id_mapping":{"mol-polecat-work":"gt-wisp-xyz"}}'
        ;;
    esac
    ;;
  update)
    # Just succeed
    exit 0
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
setlocal enableextensions
echo %CD%^|%*>>"%BD_LOG%"
set "cmd=%1"
set "sub=%2"
if "%cmd%"=="show" (
  echo [{^"title^":^"Bug to fix^",^"status^":^"open^",^"assignee^":^"^",^"description^":^"^"}]
  exit /b 0
)
if "%cmd%"=="formula" (
  echo {^"name^":^"mol-polecat-work^"}
  exit /b 0
)
if "%cmd%"=="cook" exit /b 0
if "%cmd%"=="mol" (
  if "%sub%"=="wisp" (
    echo legacy mol wisp should not be called 1>&2
    exit /b 1
  )
  if "%sub%"=="bond" (
    echo {^"result_id^":^"gt-abc123^",^"id_mapping^":{^"mol-polecat-work^":^"gt-wisp-xyz^"}}
    exit /b 0
  )
)
if "%cmd%"=="update" exit /b 0
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	attachedLogPath := filepath.Join(townRoot, "attached-molecule.log")
	t.Setenv("GT_TEST_ATTACHED_MOLECULE_LOG", attachedLogPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "") // Prevent inheriting real tmux pane from test runner

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Ensure we don't leak global flag state across tests.
	prevOn := slingOnTarget
	prevVars := slingVars
	prevDryRun := slingDryRun
	prevNoConvoy := slingNoConvoy
	t.Cleanup(func() {
		slingOnTarget = prevOn
		slingVars = prevVars
		slingDryRun = prevDryRun
		slingNoConvoy = prevNoConvoy
	})

	slingDryRun = false
	slingNoConvoy = true
	slingVars = nil
	slingOnTarget = "gt-abc123" // The bug bead we're applying formula to

	// Prevent real tmux nudge from firing during tests (causes agent self-interruption)
	t.Setenv("GT_TEST_NO_NUDGE", "1")
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1") // Stub bd doesn't track state

	if err := runSling(nil, []string{"mol-polecat-work"}); err != nil {
		t.Fatalf("runSling: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}

	// After bonding (mol bond), there should be an update call that includes
	// --description with attached_molecule field. This is what gt hook looks for.
	logLines := strings.Split(string(logBytes), "\n")

	// Find all update commands after the bond
	sawBond := false
	foundAttachedMolecule := false
	for _, line := range logLines {
		if strings.Contains(line, "mol bond") {
			sawBond = true
			continue
		}
		if sawBond && strings.Contains(line, "update") {
			// Check if this update sets attached_molecule in description
			if strings.Contains(line, "attached_molecule") {
				foundAttachedMolecule = true
				break
			}
		}
	}

	if !sawBond {
		t.Fatalf("mol bond command not found in log:\n%s", string(logBytes))
	}

	if !foundAttachedMolecule {
		if descBytes, err := os.ReadFile(attachedLogPath); err == nil {
			if strings.Contains(string(descBytes), "attached_molecule") {
				foundAttachedMolecule = true
			}
		}
	}

	if !foundAttachedMolecule {
		attachedLog := "<missing>"
		if descBytes, err := os.ReadFile(attachedLogPath); err == nil {
			attachedLog = string(descBytes)
		}
		t.Errorf("after mol bond, expected update with attached_molecule in description\n"+
			"This is required for gt hook to recognize the molecule attachment.\n"+
			"Log output:\n%s\nAttached log:\n%s", string(logBytes), attachedLog)
	}

	descBytes, err := os.ReadFile(attachedLogPath)
	if err != nil {
		t.Fatalf("read attached log: %v", err)
	}
	attachment := beads.ParseAttachmentFields(&beads.Issue{Description: string(descBytes)})
	if attachment == nil {
		t.Fatalf("parse attached fields returned nil:\n%s", string(descBytes))
	}
	if attachment.AttachedMolecule != "gt-wisp-xyz" {
		t.Fatalf("attached_molecule = %q, want gt-wisp-xyz\nDescription:\n%s", attachment.AttachedMolecule, string(descBytes))
	}
	vars := map[string]string{}
	for _, kv := range attachmentFormulaVars(attachment) {
		key, value, ok := strings.Cut(kv, "=")
		if ok {
			vars[key] = value
		}
	}
	if vars["feature"] != "Bug to fix" || vars["issue"] != "gt-abc123" {
		t.Fatalf("formula vars did not roundtrip through attached bead description: %#v\nDescription:\n%s", vars, string(descBytes))
	}
}

// TestSlingNoMergeFlag verifies that gt sling --no-merge stores the no_merge flag
// in the bead's description. This flag tells gt done to skip the merge queue
// and keep work on the feature branch for human review.
func TestSlingNoMergeFlag(t *testing.T) {
	townRoot := t.TempDir()

	// Minimal workspace marker so workspace.FindFromCwd() succeeds.
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Create stub bd that logs update commands
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -e
echo "ARGS:$*" >> "${BD_LOG}"
cmd="$1"
shift || true
case "$cmd" in
  show)
    echo '[{"title":"Test issue","status":"open","assignee":"","description":""}]'
    ;;
  update)
    exit 0
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
setlocal enableextensions
echo ARGS:%*>>"%BD_LOG%"
set "cmd=%1"
if not "%cmd%"=="show" goto :notshow
echo [{"title":"Test issue","status":"open","assignee":"","description":""}]
exit /b 0
:notshow
if "%cmd%"=="update" exit /b 0
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	// Use GT_TEST_ATTACHED_MOLECULE_LOG to capture the description content directly.
	// On Windows, multi-line --description= args break batch script logging because
	// newlines in command-line arguments cause cmd.exe to treat subsequent lines as
	// separate commands.
	molLogPath := filepath.Join(townRoot, "mol.log")
	t.Setenv("GT_TEST_ATTACHED_MOLECULE_LOG", molLogPath)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("GT_TEST_NO_NUDGE", "1")
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1") // Stub bd doesn't track state

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Save and restore global flags
	prevDryRun := slingDryRun
	prevNoConvoy := slingNoConvoy
	prevNoMerge := slingNoMerge
	t.Cleanup(func() {
		slingDryRun = prevDryRun
		slingNoConvoy = prevNoConvoy
		slingNoMerge = prevNoMerge
	})

	slingDryRun = false
	slingNoConvoy = true
	slingNoMerge = true // This is what we're testing

	if err := runSling(nil, []string{"gt-test123"}); err != nil {
		t.Fatalf("runSling: %v", err)
	}

	// Check the molecule log file written by storeFieldsInBead (via GT_TEST_ATTACHED_MOLECULE_LOG).
	// This directly captures the description content without relying on batch stub logging.
	molBytes, err := os.ReadFile(molLogPath)
	if err != nil {
		t.Fatalf("read molecule log: %v", err)
	}
	molContent := string(molBytes)
	if !strings.Contains(molContent, "no_merge: true") {
		t.Errorf("--no-merge flag not stored in bead description\nDescription:\n%s", molContent)
	}
}

func TestSlingRalphFlagStoresMode(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -e
echo "ARGS:$*" >> "${BD_LOG}"
cmd="$1"
shift || true
case "$cmd" in
  show)
    echo '[{"title":"Test issue","status":"open","assignee":"","description":""}]'
    ;;
  update)
    exit 0
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
setlocal enableextensions
echo ARGS:%*>>"%BD_LOG%"
set "cmd=%1"
if not "%cmd%"=="show" goto :notshow
echo [{"title":"Test issue","status":"open","assignee":"","description":""}]
exit /b 0
:notshow
if "%cmd%"=="update" exit /b 0
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	molLogPath := filepath.Join(townRoot, "mol.log")
	t.Setenv("GT_TEST_ATTACHED_MOLECULE_LOG", molLogPath)
	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("GT_TEST_NO_NUDGE", "1")
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	prevDryRun := slingDryRun
	prevNoConvoy := slingNoConvoy
	prevRalph := slingRalph
	t.Cleanup(func() {
		slingDryRun = prevDryRun
		slingNoConvoy = prevNoConvoy
		slingRalph = prevRalph
	})
	slingDryRun = false
	slingNoConvoy = true
	slingRalph = true

	if err := runSling(nil, []string{"gt-test123"}); err != nil {
		t.Fatalf("runSling: %v", err)
	}

	molBytes, err := os.ReadFile(molLogPath)
	if err != nil {
		t.Fatalf("read molecule log: %v", err)
	}
	molContent := string(molBytes)
	if !strings.Contains(molContent, "mode: ralph") {
		t.Fatalf("--ralph flag not stored in bead description\nDescription:\n%s", molContent)
	}
}

// TestSlingSetsDoltAutoCommitOff verifies that gt sling sets BD_DOLT_AUTO_COMMIT=off
// for all child bd processes. Under concurrent load (batch slinging), auto-commits
// from individual bd writes cause manifest contention and 'database is read only'
// errors. The Dolt server handles commits — individual auto-commits are unnecessary.
// Fixes: gt-u6n6a

// TestCheckCrossRigGuard verifies that cross-rig sling is rejected when a bead's
// prefix doesn't match the target rig. This prevents slinging beads-codebase issues
// to gastown polecats, which cannot fix code in a different rig's repo.
// Fixes: gt-myecw
func TestCheckCrossRigGuard(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	routesContent := `{"prefix":"gt-","path":"gastown/mayor/rig"}
{"prefix":"bd-","path":"beads/mayor/rig"}
{"prefix":"hq-","path":"."}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		beadID      string
		targetAgent string
		wantErr     bool
	}{
		{
			name:        "same rig: gt bead to gastown polecat",
			beadID:      "gt-abc123",
			targetAgent: "gastown/polecats/Toast",
			wantErr:     false,
		},
		{
			name:        "same rig: bd bead to beads polecat",
			beadID:      "bd-ka761",
			targetAgent: "beads/polecats/obsidian",
			wantErr:     false,
		},
		{
			name:        "cross-rig: bd bead to gastown polecat",
			beadID:      "bd-ka761",
			targetAgent: "gastown/polecats/Toast",
			wantErr:     true,
		},
		{
			name:        "cross-rig: gt bead to beads polecat",
			beadID:      "gt-abc123",
			targetAgent: "beads/polecats/obsidian",
			wantErr:     true,
		},
		{
			// Known town-root prefix: warn but allow. A crew member with a broken
			// redirect chain may create hq-* beads that legitimately target a rig
			// polecat (gt-gbu). Hard-rejecting silently drops all their polecat work.
			name:        "town-level: hq bead to rig (warns but allows — gt-gbu)",
			beadID:      "hq-abc123",
			targetAgent: "gastown/polecats/Toast",
			wantErr:     false,
		},
		{
			// Truly unknown prefix (not in routes.jsonl): hard reject.
			name:        "unknown prefix: rejected (no route exists at all)",
			beadID:      "xx-unknown",
			targetAgent: "gastown/polecats/Toast",
			wantErr:     true,
		},
		{
			name:        "empty bead prefix: allowed",
			beadID:      "nohyphen",
			targetAgent: "gastown/polecats/Toast",
			wantErr:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkCrossRigGuard(tc.beadID, tc.targetAgent, tmpDir)
			if (err != nil) != tc.wantErr {
				t.Errorf("checkCrossRigGuard(%q, %q) error = %v, wantErr %v", tc.beadID, tc.targetAgent, err, tc.wantErr)
			}
			if err != nil && tc.wantErr {
				errMsg := err.Error()
				if !strings.Contains(errMsg, "cross-rig mismatch") && !strings.Contains(errMsg, "not in routes") {
					t.Errorf("expected cross-rig mismatch or unknown-prefix error, got: %v", err)
				}
				if !strings.Contains(errMsg, "--force") {
					t.Errorf("error should mention --force override, got: %v", err)
				}
				if !strings.Contains(errMsg, "bd create") {
					t.Errorf("error should mention bd create, got: %v", err)
				}
			}
		})
	}
}

func TestIsHookedAgentDead_UnknownFormat(t *testing.T) {
	// Unknown assignee formats should return false (conservative)
	tests := []struct {
		name     string
		assignee string
	}{
		{"empty", ""},
		{"unknown_single", "foobar"},
		{"four_parts", "a/b/c/d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if isHookedAgentDead(tt.assignee) {
				t.Errorf("isHookedAgentDead(%q) = true, want false (unknown format)", tt.assignee)
			}
		})
	}
}

func TestIsHookedAgentDead_NoTmuxSession(t *testing.T) {
	// For a known assignee format where no tmux session exists,
	// isHookedAgentDead should return true (session is dead).
	// Use a highly unlikely polecat name to ensure no collision with real sessions.
	result := isHookedAgentDead("nonexistent_rig_xyz/polecats/ghost_polecat_999")
	// This might return true (no session) or false (tmux not available).
	// We just verify it doesn't panic.
	_ = result
}

func TestSlingSetsDoltAutoCommitOff(t *testing.T) {
	townRoot := t.TempDir()

	// Minimal workspace marker
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Create stub bd that logs BD_DOLT_AUTO_COMMIT env var
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -e
echo "ENV:BD_DOLT_AUTO_COMMIT=${BD_DOLT_AUTO_COMMIT}|$*" >> "${BD_LOG}"
cmd="$1"
shift || true
case "$cmd" in
  show)
    echo '[{"title":"Test issue","status":"open","assignee":"","description":""}]'
    ;;
  update)
    exit 0
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
setlocal enableextensions
echo ENV:BD_DOLT_AUTO_COMMIT=%BD_DOLT_AUTO_COMMIT%^|%*>>"%BD_LOG%"
set "cmd=%1"
if not "%cmd%"=="show" goto :notshow
echo [{"title":"Test issue","status":"open","assignee":"","description":""}]
exit /b 0
:notshow
if "%cmd%"=="update" exit /b 0
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	molLogPath := filepath.Join(townRoot, "mol.log")
	t.Setenv("GT_TEST_ATTACHED_MOLECULE_LOG", molLogPath)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("GT_TEST_NO_NUDGE", "1")
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1")
	// Ensure BD_DOLT_AUTO_COMMIT is NOT set before sling runs
	t.Setenv("BD_DOLT_AUTO_COMMIT", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Save and restore global flags
	prevDryRun := slingDryRun
	prevNoConvoy := slingNoConvoy
	t.Cleanup(func() {
		slingDryRun = prevDryRun
		slingNoConvoy = prevNoConvoy
	})

	slingDryRun = false
	slingNoConvoy = true

	if err := runSling(nil, []string{"gt-test456"}); err != nil {
		t.Fatalf("runSling: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}

	// Verify that ALL bd commands received BD_DOLT_AUTO_COMMIT=off
	logLines := strings.Split(strings.TrimSpace(string(logBytes)), "\n")
	if len(logLines) == 0 {
		t.Fatal("no bd commands logged")
	}

	for _, line := range logLines {
		if line == "" {
			continue
		}
		// Commands using .WithAutoCommit() (e.g., "update --status=hooked")
		// legitimately override to "on" for sequential consistency.
		if strings.Contains(line, "update") && strings.Contains(line, "--status=hooked") {
			continue
		}
		if !strings.Contains(line, "ENV:BD_DOLT_AUTO_COMMIT=off|") {
			t.Errorf("bd command missing BD_DOLT_AUTO_COMMIT=off: %s", line)
		}
	}
}

func TestHookBeadWithRetryForcesAutoCommit(t *testing.T) {
	townRoot := t.TempDir()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "bd.log")

	bdScript := `#!/usr/bin/env sh
printf 'ENV:BD_DOLT_AUTO_COMMIT=%s|%s\n' "$BD_DOLT_AUTO_COMMIT" "$*" >> "$BD_LOG"
exit 0
`
	bdScriptWindows := `@echo off
setlocal enableextensions
echo ENV:BD_DOLT_AUTO_COMMIT=%BD_DOLT_AUTO_COMMIT%^|%*>>"%BD_LOG%"
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_DOLT_AUTO_COMMIT", "off")
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1")

	if err := hookBeadWithRetry("gt-test123", "gastown/polecats/toast", townRoot); err != nil {
		t.Fatalf("hookBeadWithRetry: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	logText := string(logBytes)
	if !strings.Contains(logText, "ENV:BD_DOLT_AUTO_COMMIT=on|") {
		t.Fatalf("hook update did not force auto-commit; log:\n%s", logText)
	}
}

func TestBuildSlingFieldUpdatesIncludesConvoyFields(t *testing.T) {
	got := buildSlingFieldUpdates(
		"mayor",
		"review this",
		[]string{"feature=test"},
		"gt-wisp-test",
		"mol-polecat-work",
		false,
		false,
		"ralph",
		"feature=test",
		"hq-cv-test1",
		"local",
		true,
	)

	if got.ConvoyID != "hq-cv-test1" {
		t.Fatalf("ConvoyID = %q, want %q", got.ConvoyID, "hq-cv-test1")
	}
	if got.MergeStrategy != "local" {
		t.Fatalf("MergeStrategy = %q, want %q", got.MergeStrategy, "local")
	}
	if !got.ConvoyOwned {
		t.Fatal("ConvoyOwned = false, want true")
	}
	if got.Mode == nil || *got.Mode != "ralph" {
		t.Fatalf("Mode = %v, want ralph", got.Mode)
	}
}

func TestStoreFieldsInBeadConvoyFields(t *testing.T) {
	t.Setenv("GT_TEST_ATTACHED_MOLECULE_LOG", filepath.Join(t.TempDir(), "mol.log"))
	logPath := os.Getenv("GT_TEST_ATTACHED_MOLECULE_LOG")

	if err := storeFieldsInBead("gt-test123", beadFieldUpdates{
		ConvoyID:      "hq-cv-test1",
		MergeStrategy: "local",
		ConvoyOwned:   true,
	}); err != nil {
		t.Fatalf("storeFieldsInBead: %v", err)
	}

	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "convoy_id: hq-cv-test1") {
		t.Fatalf("missing convoy_id in description:\n%s", text)
	}
	if !strings.Contains(text, "merge_strategy: local") {
		t.Fatalf("missing merge_strategy in description:\n%s", text)
	}
	if !strings.Contains(text, "convoy_owned: true") {
		t.Fatalf("missing convoy_owned in description:\n%s", text)
	}
}

func TestBeadFieldModeUpdateCanClearStaleRalphMode(t *testing.T) {
	issue := &beads.Issue{Description: "attached_formula: mol-polecat-work\nmode: ralph"}
	fields := beads.ParseAttachmentFields(issue)
	if fields == nil {
		t.Fatal("expected attachment fields")
	}
	mode := ""
	updates := beadFieldUpdates{Mode: &mode}
	if updates.Mode != nil {
		fields.Mode = *updates.Mode
	}
	desc := beads.SetAttachmentFields(issue, fields)
	if strings.Contains(desc, "mode: ralph") || strings.Contains(desc, "mode:") {
		t.Fatalf("expected stale ralph mode to be cleared, got:\n%s", desc)
	}
	if !strings.Contains(desc, "attached_formula: mol-polecat-work") {
		t.Fatalf("expected unrelated attachment fields preserved, got:\n%s", desc)
	}
}

func TestStoreFieldsInBeadFormulaSetsAttachedAt(t *testing.T) {
	t.Setenv("GT_TEST_ATTACHED_MOLECULE_LOG", filepath.Join(t.TempDir(), "mol.log"))
	logPath := os.Getenv("GT_TEST_ATTACHED_MOLECULE_LOG")

	if err := storeFieldsInBead("gt-test123", beadFieldUpdates{
		AttachedFormula: "mol-dog-reaper",
	}); err != nil {
		t.Fatalf("storeFieldsInBead: %v", err)
	}

	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	fields := beads.ParseAttachmentFields(&beads.Issue{Description: string(body)})
	if fields == nil || fields.AttachedFormula != "mol-dog-reaper" || fields.AttachedAt == "" {
		t.Fatalf("formula attachment fields = %#v, want formula and attached_at", fields)
	}
	if _, err := time.Parse(time.RFC3339Nano, fields.AttachedAt); err != nil {
		t.Fatalf("attached_at %q is not RFC3339Nano: %v", fields.AttachedAt, err)
	}
}

func TestStoreFieldsInBeadRawReviewRefreshesAttachedAt(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "raw-review.log")
	t.Setenv("GT_TEST_ATTACHED_MOLECULE_LOG", logPath)

	stale := "2026-06-30T12:00:00Z"
	if err := os.WriteFile(logPath, []byte("attached_at: "+stale+"\nno_merge: true\nreview_only: true\n"), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	if err := storeFieldsInBead("gt-test123", beadFieldUpdates{
		NoMerge:    true,
		ReviewOnly: true,
	}); err != nil {
		t.Fatalf("storeFieldsInBead: %v", err)
	}

	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	fields := beads.ParseAttachmentFields(&beads.Issue{Description: string(body)})
	if fields == nil || !fields.NoMerge || !fields.ReviewOnly {
		t.Fatalf("raw review fields = %#v", fields)
	}
	if fields.AttachedAt == "" || fields.AttachedAt == stale {
		t.Fatalf("attached_at = %q, want refreshed from %q", fields.AttachedAt, stale)
	}
	if _, err := time.Parse(time.RFC3339Nano, fields.AttachedAt); err != nil {
		t.Fatalf("attached_at %q is not RFC3339Nano: %v", fields.AttachedAt, err)
	}
}

// TestSlingIdempotentNoOp verifies that slinging a bead to the same target
// it's already assigned to returns a no-op instead of an error.
func TestSlingIdempotentNoOp(t *testing.T) {
	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
set -e
cmd="$1"
shift || true
case "$cmd" in
  show)
    echo '[{"title":"Test issue","status":"hooked","assignee":"gastown/polecats/toast","description":""}]'
    ;;
  update)
    exit 0
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
set "cmd=%1"
if "%cmd%"=="show" (
  echo [{"title":"Test issue","status":"hooked","assignee":"gastown/polecats/toast","description":""}]
  exit /b 0
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("TMUX_PANE", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Stub isHookedAgentDead to return false (agent is alive)
	prevDeadFn := isHookedAgentDeadFn
	t.Cleanup(func() { isHookedAgentDeadFn = prevDeadFn })
	isHookedAgentDeadFn = func(assignee string) bool { return false }

	prevForce := slingForce
	prevNoConvoy := slingNoConvoy
	t.Cleanup(func() {
		slingForce = prevForce
		slingNoConvoy = prevNoConvoy
	})
	slingForce = false
	slingNoConvoy = true

	// Sling to same target — should no-op (return nil, no error)
	err = runSling(nil, []string{"gt-test123", "gastown/polecats/toast"})
	if err != nil {
		t.Fatalf("expected no-op nil return, got error: %v", err)
	}
}

// TestSlingIdempotentNoOp_Pinned verifies that slinging a pinned bead to the
// same target returns a no-op, just like the hooked case.
func TestSlingIdempotentNoOp_Pinned(t *testing.T) {
	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
set -e
cmd="$1"
shift || true
case "$cmd" in
  show)
    echo '[{"title":"Test issue","status":"pinned","assignee":"gastown/polecats/toast","description":""}]'
    ;;
  update)
    exit 0
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
set "cmd=%1"
if "%cmd%"=="show" (
  echo [{"title":"Test issue","status":"pinned","assignee":"gastown/polecats/toast","description":""}]
  exit /b 0
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("TMUX_PANE", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Pinned beads don't check isHookedAgentDead, but stub it anyway for safety
	prevDeadFn := isHookedAgentDeadFn
	t.Cleanup(func() { isHookedAgentDeadFn = prevDeadFn })
	isHookedAgentDeadFn = func(assignee string) bool { return false }

	prevForce := slingForce
	prevNoConvoy := slingNoConvoy
	t.Cleanup(func() {
		slingForce = prevForce
		slingNoConvoy = prevNoConvoy
	})
	slingForce = false
	slingNoConvoy = true

	// Sling pinned bead to same target — should no-op (return nil, no error)
	err = runSling(nil, []string{"gt-test-pinned", "gastown/polecats/toast"})
	if err != nil {
		t.Fatalf("expected no-op nil return for pinned bead, got error: %v", err)
	}
}

// TestSlingDeadAgentBypassesIdempotency verifies that a dead hooked agent
// triggers auto-force re-sling even when the target matches the current assignee.
func TestSlingDeadAgentBypassesIdempotency(t *testing.T) {
	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -e
echo "$*" >> "${BD_LOG}"
cmd="$1"
shift || true
case "$cmd" in
  show)
    echo '[{"title":"Test issue","status":"hooked","assignee":"gastown/polecats/test-dead-polecat-xxxx","description":""}]'
    ;;
  update)
    exit 0
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
echo %*>>"%BD_LOG%"
set "cmd=%1"
if "%cmd%"=="show" (
  echo [{"title":"Test issue","status":"hooked","assignee":"gastown/polecats/test-dead-polecat-xxxx","description":""}]
  exit /b 0
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("GT_TEST_NO_NUDGE", "1")
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Stub isHookedAgentDead to return true (agent is dead)
	prevDeadFn := isHookedAgentDeadFn
	t.Cleanup(func() { isHookedAgentDeadFn = prevDeadFn })
	isHookedAgentDeadFn = func(assignee string) bool { return true }

	prevForce := slingForce
	prevNoConvoy := slingNoConvoy
	prevDryRun := slingDryRun
	t.Cleanup(func() {
		slingForce = prevForce
		slingNoConvoy = prevNoConvoy
		slingDryRun = prevDryRun
	})
	slingForce = false
	slingNoConvoy = true
	slingDryRun = true // dry-run to avoid side effects from resolveTarget

	// Capture stdout to verify the "auto-forcing re-sling" message is printed.
	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	// Sling with matching target but dead agent — should NOT no-op.
	// The auto-force path proceeds into resolveTarget which will fail
	// because the polecat doesn't exist in tmux. Use a unique name that
	// will never collide with a real running polecat session.
	err = runSling(nil, []string{"gt-test456", "gastown/polecats/test-dead-polecat-xxxx"})

	w.Close()
	os.Stdout = origStdout
	var captured bytes.Buffer
	_, _ = captured.ReadFrom(r)
	stdout := captured.String()

	if err == nil {
		t.Fatal("expected error from resolveTarget (proving auto-force bypassed idempotency), got nil (no-op)")
	}
	// Must NOT be the "already hooked" error — that would mean idempotency kicked in
	if strings.Contains(err.Error(), "already") && strings.Contains(err.Error(), "hooked") {
		t.Fatalf("got 'already hooked' error, meaning idempotency was NOT bypassed for dead agent: %v", err)
	}
	// Verify the auto-force message was printed (direct signal that dead-agent path was taken)
	if !strings.Contains(stdout, "auto-forcing re-sling") {
		t.Fatalf("expected 'auto-forcing re-sling' in stdout, got: %q", stdout)
	}
}

// TestSlingForceBypassesIdempotency verifies that --force skips the
// idempotency check and proceeds with re-sling even for matching targets.
func TestSlingForceBypassesIdempotency(t *testing.T) {
	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -e
echo "$*" >> "${BD_LOG}"
cmd="$1"
shift || true
case "$cmd" in
  show)
    echo '[{"title":"Test issue","status":"hooked","assignee":"gastown/polecats/toast","description":""}]'
    ;;
  update)
    exit 0
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
echo %*>>"%BD_LOG%"
set "cmd=%1"
if "%cmd%"=="show" (
  echo [{"title":"Test issue","status":"hooked","assignee":"gastown/polecats/toast","description":""}]
  exit /b 0
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("GT_TEST_NO_NUDGE", "1")
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1")

	molLogPath := filepath.Join(townRoot, "mol.log")
	t.Setenv("GT_TEST_ATTACHED_MOLECULE_LOG", molLogPath)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	prevForce := slingForce
	prevNoConvoy := slingNoConvoy
	prevDryRun := slingDryRun
	t.Cleanup(func() {
		slingForce = prevForce
		slingNoConvoy = prevNoConvoy
		slingDryRun = prevDryRun
	})
	slingForce = true // --force
	slingNoConvoy = true
	slingDryRun = true

	// --force bypasses the entire pinned/hooked guard including idempotency.
	// resolveTarget will fail because rig doesn't exist, but the key assertion
	// is that we don't get an "already hooked" error (idempotency no-op is skipped).
	err = runSling(nil, []string{"gt-test789", "gastown/polecats/toast"})
	if err == nil {
		// In dry-run + force mode, resolveTarget still runs.
		// nil is acceptable if resolveTarget succeeded.
	} else if strings.Contains(err.Error(), "already") && strings.Contains(err.Error(), "hooked") {
		t.Fatalf("got 'already hooked' error, meaning --force did not bypass guard: %v", err)
	}
	// Any other error (e.g., from resolveTarget) is fine — proves we got past the guard.
}

// TestSlingFormulaOnBeadBypassesIdempotency verifies that formula-on-bead mode
// (gt sling <formula> --on <bead>) does NOT idempotent no-op even when the
// target assignment already matches. The user expects formula instantiation to run.
func TestSlingFormulaOnBeadBypassesIdempotency(t *testing.T) {
	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	// The bd stub must handle both "show" (for verifyBeadExists) and
	// "formula" (for verifyFormulaExists) subcommands so that the test
	// reaches the idempotency guard rather than failing earlier.
	bdScript := `#!/bin/sh
set -e
cmd="$1"
shift || true
case "$cmd" in
  show)
    echo '[{"title":"Test issue","status":"hooked","assignee":"gastown/polecats/toast","description":""}]'
    ;;
  formula)
    echo '{"name":"test-formula","steps":[]}'
    ;;
  update)
    exit 0
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
set "cmd=%1"
if "%cmd%"=="show" (
  echo [{"title":"Test issue","status":"hooked","assignee":"gastown/polecats/toast","description":""}]
  exit /b 0
)
if "%cmd%"=="formula" (
  echo {"name":"test-formula","steps":[]}
  exit /b 0
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("TMUX_PANE", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Stub isHookedAgentDead to return false (agent is alive)
	prevDeadFn := isHookedAgentDeadFn
	t.Cleanup(func() { isHookedAgentDeadFn = prevDeadFn })
	isHookedAgentDeadFn = func(assignee string) bool { return false }

	prevForce := slingForce
	prevNoConvoy := slingNoConvoy
	prevOnTarget := slingOnTarget
	t.Cleanup(func() {
		slingForce = prevForce
		slingNoConvoy = prevNoConvoy
		slingOnTarget = prevOnTarget
	})
	slingForce = false
	slingNoConvoy = true
	slingOnTarget = "gt-test-formula-on-bead" // --on flag: bead ID

	// Formula-on-bead with matching target. The idempotency guard must NOT
	// return nil (no-op) or "already hooked" error — it should fall through
	// to resolveTarget/formula instantiation. The call will fail downstream
	// (rig doesn't exist in test env) but must get PAST the guard.
	err = runSling(nil, []string{"test-formula", "gastown/polecats/toast"})
	if err == nil {
		t.Fatal("expected error from downstream (resolve), got nil (idempotent no-op was incorrectly triggered)")
	}
	// Must NOT be the "already hooked" error — that means the guard blocked formula execution
	if strings.Contains(err.Error(), "already") && strings.Contains(err.Error(), "hooked") {
		t.Fatalf("got 'already hooked' error in formula-on-bead mode; guard should have been bypassed: %v", err)
	}
	// Must NOT be "formula not found" — that would mean the bd stub is broken
	if strings.Contains(err.Error(), "formula") && strings.Contains(err.Error(), "not found") {
		t.Fatalf("got 'formula not found' error; bd stub should handle formula subcommand: %v", err)
	}
}

// TestSlingIdempotentNoOp_PinnedSelfDot verifies that slinging a pinned bead
// with "." (self-target) returns a no-op when self-resolution matches the assignee.
// Uses mayor role since polecats cannot sling.
func TestSlingIdempotentNoOp_PinnedSelfDot(t *testing.T) {
	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	// Bead is pinned to "mayor/" — matches the mayor self-resolution.
	bdScript := `#!/bin/sh
set -e
cmd="$1"
shift || true
case "$cmd" in
  show)
    echo '[{"title":"Test issue","status":"pinned","assignee":"mayor/","description":""}]'
    ;;
  update)
    exit 0
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
set "cmd=%1"
if "%cmd%"=="show" (
  echo [{"title":"Test issue","status":"pinned","assignee":"mayor/","description":""}]
  exit /b 0
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("TMUX_PANE", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Pinned beads don't check isHookedAgentDead, but stub it for safety
	prevDeadFn := isHookedAgentDeadFn
	t.Cleanup(func() { isHookedAgentDeadFn = prevDeadFn })
	isHookedAgentDeadFn = func(assignee string) bool { return false }

	prevForce := slingForce
	prevNoConvoy := slingNoConvoy
	t.Cleanup(func() {
		slingForce = prevForce
		slingNoConvoy = prevNoConvoy
	})
	slingForce = false
	slingNoConvoy = true

	// Sling pinned bead with dot target — self-resolution as mayor returns
	// "mayor/" which normalizes to "mayor", matching the assignee. Should no-op.
	err = runSling(nil, []string{"gt-test-pinned-dot", "."})
	if err != nil {
		t.Fatalf("expected no-op nil return for pinned bead with dot target, got error: %v", err)
	}
}

// TestSlingPolecatEnvCheck verifies that the polecat guard in runSling uses
// GT_ROLE as the authoritative check, so coordinators with a stale GT_POLECAT
// in their environment are not blocked from slinging (GH #664).
func TestSlingPolecatEnvCheck(t *testing.T) {
	tests := []struct {
		name      string
		role      string
		polecat   string
		wantBlock bool
	}{
		{
			name:      "bare polecat role is blocked",
			role:      "polecat",
			polecat:   "alpha",
			wantBlock: true,
		},
		{
			name:      "compound polecat role is blocked",
			role:      "gastown/polecats/Toast",
			polecat:   "Toast",
			wantBlock: true,
		},
		{
			name:      "mayor with stale GT_POLECAT is NOT blocked",
			role:      "mayor",
			polecat:   "alpha",
			wantBlock: false,
		},
		{
			name:      "compound witness with stale GT_POLECAT is NOT blocked",
			role:      "gastown/witness",
			polecat:   "alpha",
			wantBlock: false,
		},
		{
			name:      "crew with stale GT_POLECAT is NOT blocked",
			role:      "crew",
			polecat:   "alpha",
			wantBlock: false,
		},
		{
			name:      "compound crew with stale GT_POLECAT is NOT blocked",
			role:      "gastown/crew/den",
			polecat:   "alpha",
			wantBlock: false,
		},
		{
			name:      "no GT_ROLE with GT_POLECAT set is blocked",
			role:      "",
			polecat:   "alpha",
			wantBlock: true,
		},
		{
			name:      "no GT_ROLE and no GT_POLECAT is not blocked",
			role:      "",
			polecat:   "",
			wantBlock: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GT_ROLE", tt.role)
			t.Setenv("GT_POLECAT", tt.polecat)

			// We only test the polecat guard, so we call runSling with no args.
			// It will either fail at the guard or panic/fail later (missing args).
			// We only care whether the error is the polecat-block message.
			var blocked bool
			func() {
				defer func() {
					if r := recover(); r != nil {
						// Panic means we got past the guard — not blocked
						blocked = false
					}
				}()
				err := runSling(nil, nil)
				blocked = err != nil && strings.Contains(err.Error(), "polecats cannot sling")
			}()

			if blocked != tt.wantBlock {
				if tt.wantBlock {
					t.Errorf("expected polecat block but was not blocked (GT_ROLE=%q GT_POLECAT=%q)", tt.role, tt.polecat)
				} else {
					t.Errorf("unexpected polecat block with GT_ROLE=%q GT_POLECAT=%q", tt.role, tt.polecat)
				}
			}
		})
	}
}

// TestSlingNudgeCrewAndMayor verifies that slinging to crew or mayor targets
// with an active session includes the nudge (inject start prompt) step.
// This is a regression test for gt-in7b: the generic resolveTarget + nudge
// flow handles all target types, not just polecats.
func TestSlingNudgeCrewAndMayor(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		wantAgent  string
		wantPaneIn string // substring expected in dry-run "Would inject" output
	}{
		{
			name:       "crew target gets nudge pane",
			target:     "gastown/crew/max",
			wantAgent:  "gastown/crew/max",
			wantPaneIn: "%99",
		},
		{
			name:       "mayor target gets nudge pane",
			target:     "mayor",
			wantAgent:  "mayor/",
			wantPaneIn: "%99",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			townRoot := t.TempDir()

			if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
				t.Fatalf("mkdir .beads: %v", err)
			}
			rigDir := filepath.Join(townRoot, "gastown", "mayor", "rig")
			if err := os.MkdirAll(rigDir, 0755); err != nil {
				t.Fatalf("mkdir rigDir: %v", err)
			}
			routes := `{"prefix":"gt-","path":"gastown/mayor/rig"}` + "\n" +
				`{"prefix":"hq-","path":"."}` + "\n"
			if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
				t.Fatalf("write routes: %v", err)
			}

			binDir := filepath.Join(townRoot, "bin")
			if err := os.MkdirAll(binDir, 0755); err != nil {
				t.Fatalf("mkdir binDir: %v", err)
			}
			bdScript := `#!/bin/sh
cmd="$1"
shift || true
case "$cmd" in
  show)
    echo '[{"title":"Test issue","status":"open","assignee":"","description":""}]'
    ;;
  update)
    exit 0
    ;;
esac
exit 0
`
			bdScriptWindows := `@echo off
set "cmd=%1"
if "%cmd%"=="show" (
  echo [{"title":"Test issue","status":"open","assignee":"","description":""}]
  exit /b 0
)
exit /b 0
`
			_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv(EnvGTRole, "mayor")
			t.Setenv("GT_POLECAT", "")
			t.Setenv("GT_CREW", "")
			t.Setenv("TMUX_PANE", "")
			t.Setenv("GT_TEST_NO_NUDGE", "1")
			t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1")

			cwd, err := os.Getwd()
			if err != nil {
				t.Fatalf("getwd: %v", err)
			}
			t.Cleanup(func() { _ = os.Chdir(cwd) })
			if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
				t.Fatalf("chdir: %v", err)
			}

			// Mock resolveTargetAgentFn to return a fake pane (no real tmux needed)
			prevFn := resolveTargetAgentFn
			t.Cleanup(func() { resolveTargetAgentFn = prevFn })
			resolveTargetAgentFn = func(target string) (string, string, string, error) {
				return tt.wantAgent, "%99", townRoot, nil
			}

			prevDryRun := slingDryRun
			prevNoConvoy := slingNoConvoy
			t.Cleanup(func() {
				slingDryRun = prevDryRun
				slingNoConvoy = prevNoConvoy
			})
			slingDryRun = true
			slingNoConvoy = true

			// Capture stdout
			origStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w
			t.Cleanup(func() { os.Stdout = origStdout })

			err = runSling(nil, []string{"gt-abc123", tt.target})

			w.Close()
			os.Stdout = origStdout
			var captured bytes.Buffer
			_, _ = captured.ReadFrom(r)
			stdout := captured.String()

			if err != nil {
				t.Fatalf("runSling: %v", err)
			}

			// Verify the dry-run output includes the nudge pane
			if !strings.Contains(stdout, "Would inject start prompt to pane: "+tt.wantPaneIn) {
				t.Errorf("expected nudge pane %q in output, got:\n%s", tt.wantPaneIn, stdout)
			}

			// Verify correct agent was resolved
			if !strings.Contains(stdout, tt.wantAgent) {
				t.Errorf("expected agent %q in output, got:\n%s", tt.wantAgent, stdout)
			}
		})
	}
}

// Regression for GH#3903: dry-run should not perform auto-convoy lookup.
func TestSlingDryRunSkipsConvoyLookup(t *testing.T) {
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	rigDir := filepath.Join(townRoot, "gastown", "mayor", "rig")
	if err := os.MkdirAll(filepath.Join(rigDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir rigDir: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gastown/mayor/rig"}` + "\n" +
		`{"prefix":"hq-","path":"."}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -e
printf '%s\n' "$*" >> "$BD_LOG"
cmd="$1"
shift || true
while [ "$cmd" = "--allow-stale" ] || [ "$cmd" = "--db" ]; do
  if [ "$cmd" = "--db" ]; then
    shift || true
  fi
  cmd="$1"
  shift || true
done
case "$cmd" in
  show)
    echo '[{"title":"Dry run issue","status":"open","assignee":"","description":""}]'
    ;;
  version)
    echo "bd version 1.0.3"
    ;;
  sql|list|dep|create|update)
    echo "unexpected dry-run convoy lookup or write: $cmd $*" >&2
    exit 37
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
echo %*>>"%BD_LOG%"
set "cmd=%1"
if "%cmd%"=="--allow-stale" set "cmd=%2"
if "%cmd%"=="--db" set "cmd=%3"
if "%cmd%"=="show" (
  echo [{"title":"Dry run issue","status":"open","assignee":"","description":""}]
  exit /b 0
)
if "%cmd%"=="version" (
  echo bd version 1.0.3
  exit /b 0
)
if "%cmd%"=="sql" exit /b 37
if "%cmd%"=="list" exit /b 37
if "%cmd%"=="dep" exit /b 37
if "%cmd%"=="create" exit /b 37
if "%cmd%"=="update" exit /b 37
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("GT_TEST_NO_NUDGE", "1")
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	prevDryRun := slingDryRun
	prevNoConvoy := slingNoConvoy
	prevMerge := slingMerge
	prevOwned := slingOwned
	prevOnTarget := slingOnTarget
	prevVars := slingVars
	prevFormula := slingFormula
	prevHookRawBead := slingHookRawBead
	prevResolveTargetAgent := resolveTargetAgentFn
	t.Cleanup(func() {
		slingDryRun = prevDryRun
		slingNoConvoy = prevNoConvoy
		slingMerge = prevMerge
		slingOwned = prevOwned
		slingOnTarget = prevOnTarget
		slingVars = prevVars
		slingFormula = prevFormula
		slingHookRawBead = prevHookRawBead
		resolveTargetAgentFn = prevResolveTargetAgent
	})
	slingDryRun = true
	slingNoConvoy = false
	slingMerge = ""
	slingOwned = false
	slingOnTarget = ""
	slingVars = nil
	slingFormula = ""
	slingHookRawBead = false
	resolveTargetAgentFn = func(target string) (string, string, string, error) {
		if target != "gastown/crew/max" {
			t.Fatalf("resolveTargetAgent target = %q, want gastown/crew/max", target)
		}
		return "gastown/crew/max", "%99", filepath.Join(townRoot, "gastown", "crew", "max", "gastown"), nil
	}

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = w
	err = runSling(nil, []string{"gt-abc123", "gastown/crew/max"})
	_ = w.Close()
	os.Stdout = origStdout
	var captured bytes.Buffer
	_, _ = captured.ReadFrom(r)
	stdout := captured.String()
	if err != nil {
		t.Fatalf("runSling: %v\nstdout:\n%s", err, stdout)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(logBytes)), "\n") {
		fields := strings.Fields(line)
		for len(fields) > 0 {
			switch fields[0] {
			case "--allow-stale":
				fields = fields[1:]
			case "--db":
				if len(fields) < 3 {
					t.Fatalf("malformed bd --db command in log: %q", line)
				}
				fields = fields[2:]
			default:
				goto commandFound
			}
		}
	commandFound:
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "sql", "list", "dep", "create", "update":
			t.Fatalf("dry-run invoked forbidden bd command %q in log:\n%s", fields[0], logBytes)
		}
	}
	if !strings.Contains(stdout, "Would create convoy 'Work: Dry run issue' if needed") {
		t.Fatalf("dry-run output missing conditional convoy plan:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Would run: bd update gt-abc123 --status=hooked --assignee=gastown/crew/max") {
		t.Fatalf("dry-run output missing hook plan:\n%s", stdout)
	}
}

// TestSlingRejectsDeferredBead verifies that gt sling refuses to sling beads
// with deferred status or deferral keywords in their description (gt-1326mw).
// This prevents wasting polecat slots on low-priority deferred work.
func TestSlingRejectsDeferredBead(t *testing.T) {
	tests := []struct {
		name      string
		bdOutput  string // JSON response from bd show --json
		force     bool
		wantError string // expected error substring, empty = no error expected
	}{
		{
			name:      "deferred status is rejected",
			bdOutput:  `[{"title":"Epic cleanup","status":"deferred","assignee":"","description":"some task"}]`,
			wantError: "refusing to sling deferred bead",
		},
		{
			name:      "deferred to post-launch in description is rejected",
			bdOutput:  `[{"title":"Nice to have feature","status":"open","assignee":"","description":"deferred to post-launch"}]`,
			wantError: "refusing to sling deferred bead",
		},
		{
			name:      "status: deferred in description is rejected",
			bdOutput:  `[{"title":"Low-pri work","status":"open","assignee":"","description":"status: deferred, will revisit later"}]`,
			wantError: "refusing to sling deferred bead",
		},
		{
			name:     "deferred status with --force is allowed",
			bdOutput: `[{"title":"Re-activated work","status":"deferred","assignee":"","description":"re-activated from deferred"}]`,
			force:    true,
		},
		{
			name:     "open bead is allowed",
			bdOutput: `[{"title":"Normal work","status":"open","assignee":"","description":"just a regular task"}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			townRoot := t.TempDir()
			if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}

			binDir := filepath.Join(townRoot, "bin")
			if err := os.MkdirAll(binDir, 0755); err != nil {
				t.Fatalf("mkdir bin: %v", err)
			}

			// Create bd stub that returns the test bead info
			bdScript := "#!/bin/sh\necho '" + tt.bdOutput + "'\n"
			bdScriptWindows := "@echo off\r\necho " + tt.bdOutput + "\r\n"
			writeBDStub(t, binDir, bdScript, bdScriptWindows)

			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv(EnvGTRole, "crew")
			t.Setenv("GT_CREW", "jv")
			t.Setenv("GT_POLECAT", "")
			t.Setenv("TMUX_PANE", "")
			t.Setenv("GT_TEST_NO_NUDGE", "1")

			cwd, err := os.Getwd()
			if err != nil {
				t.Fatalf("getwd: %v", err)
			}
			t.Cleanup(func() { _ = os.Chdir(cwd) })
			if err := os.Chdir(townRoot); err != nil {
				t.Fatalf("chdir: %v", err)
			}

			prevDryRun := slingDryRun
			prevNoConvoy := slingNoConvoy
			prevForce := slingForce
			t.Cleanup(func() {
				slingDryRun = prevDryRun
				slingNoConvoy = prevNoConvoy
				slingForce = prevForce
			})
			slingDryRun = true
			slingNoConvoy = true
			slingForce = tt.force

			err = runSling(nil, []string{"gt-test123"})

			if tt.wantError != "" {
				if err == nil {
					t.Fatalf("expected error containing %q but got nil", tt.wantError)
				}
				if !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("expected error containing %q, got: %v", tt.wantError, err)
				}
			} else if err != nil {
				// Some errors are OK in dry-run (e.g., "finding town root" when workspace not fully set up).
				// We only fail if the error is about deferred rejection, which shouldn't happen.
				if strings.Contains(err.Error(), "refusing to sling deferred") {
					t.Fatalf("unexpected deferred rejection: %v", err)
				}
			}
		})
	}
}

// TestRunSlingResumeFlagValidation verifies the gh#3602 mutual-exclusion rules
// for --branch / --pr / --base-branch. The validation block runs before any
// I/O, so we can exercise it without a live workspace.
func TestRunSlingResumeFlagValidation(t *testing.T) {
	tests := []struct {
		name         string
		resumeBranch string
		resumePR     int
		baseBranch   string
		wantError    string
	}{
		{
			name:         "branch and pr together is rejected",
			resumeBranch: "feature/foo",
			resumePR:     42,
			wantError:    "--branch and --pr are mutually exclusive",
		},
		{
			name:         "branch with base-branch is rejected",
			resumeBranch: "feature/foo",
			baseBranch:   "develop",
			wantError:    "--base-branch cannot be combined with --branch or --pr",
		},
		{
			name:       "pr with base-branch is rejected",
			resumePR:   42,
			baseBranch: "develop",
			wantError:  "--base-branch cannot be combined with --branch or --pr",
		},
	}

	t.Setenv(EnvGTRole, "")
	t.Setenv("GT_POLECAT", "")

	prevResumeBranch := slingResumeBranch
	prevResumePR := slingResumePR
	prevBaseBranch := slingBaseBranch
	t.Cleanup(func() {
		slingResumeBranch = prevResumeBranch
		slingResumePR = prevResumePR
		slingBaseBranch = prevBaseBranch
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slingResumeBranch = tt.resumeBranch
			slingResumePR = tt.resumePR
			slingBaseBranch = tt.baseBranch

			err := runSling(nil, []string{"gt-test"})
			if err == nil {
				t.Fatalf("expected error containing %q but got nil", tt.wantError)
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantError, err)
			}
		})
	}
}

// TestSlingStandaloneFormulaInDeferredMode is a regression test for gh#3917.
//
// When scheduler.max_polecats > 0 (deferred dispatch mode), `gt sling <formula> <rig>`
// was rejected with "standalone formula cannot be scheduled (use --on <bead>)" even
// though the help text and documented examples explicitly show this usage.
//
// Fix: fall through to runSlingFormula instead of erroring. Standalone formula
// slinging (cook+wisp+attach) is not bead-based capacity-scheduled dispatch.
func TestSlingStandaloneFormulaInDeferredMode(t *testing.T) {
	townRoot := t.TempDir()

	// Workspace marker: workspace.FindFromCwdOrError needs mayor/town.json
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte(`{"name":"test","version":2}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}

	// Rig registry: IsRigName("testrig") requires testrig in rigs.json
	rigsJSON := `{"version":1,"rigs":{"testrig":{"git_url":"file:///dev/null"}}}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatalf("write rigs.json: %v", err)
	}

	// Town settings: scheduler.max_polecats > 0 activates deferred dispatch
	settingsDir := filepath.Join(townRoot, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("mkdir settings: %v", err)
	}
	settingsJSON := `{"version":1,"scheduler":{"max_polecats":10,"batch_size":3}}`
	settingsPath := config.TownSettingsPath(townRoot)
	if err := os.WriteFile(settingsPath, []byte(settingsJSON), 0644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	// .beads routes
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"testrig/mayor/rig"}` + "\n" + `{"prefix":"hq-","path":"."}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	// Stub bd: formula show must return output so verifyFormulaExists returns nil
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
cmd="$1"
shift || true
case "$cmd" in
  formula) echo '{"name":"mol-test-formula"}'; exit 0 ;;
  show)    echo '[{"title":"Test","status":"open","assignee":"","description":""}]' ;;
  cook)    exit 0 ;;
  mol)
    sub="$1"; shift || true
    case "$sub" in
      wisp) echo '{"new_epic_id":"gt-wisp-xyz"}' ;;
    esac ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
set "cmd=%1"
if "%cmd%"=="formula" ( echo {"name":"mol-test-formula"} & exit /b 0 )
if "%cmd%"=="show" ( echo [{"title":"Test","status":"open","assignee":"","description":""}] & exit /b 0 )
if "%cmd%"=="cook" exit /b 0
if "%cmd%"=="mol" if "%2"=="wisp" ( echo {"new_epic_id":"gt-wisp-xyz"} & exit /b 0 )
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// chdir into the town so workspace.FindFromCwd() resolves townRoot
	rigDir := filepath.Join(townRoot, "mayor", "rig")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(rigDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("GT_TEST_NO_NUDGE", "1")
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1")

	// Save and restore global sling state
	prevDryRun := slingDryRun
	prevNoConvoy := slingNoConvoy
	prevVars := slingVars
	prevOnTarget := slingOnTarget
	t.Cleanup(func() {
		slingDryRun = prevDryRun
		slingNoConvoy = prevNoConvoy
		slingVars = prevVars
		slingOnTarget = prevOnTarget
	})
	slingDryRun = true // avoid real polecat spawning
	slingNoConvoy = true
	slingVars = nil
	slingOnTarget = ""

	// Regression: before the fix, this returned "standalone formula cannot be scheduled".
	err = runSling(nil, []string{"mol-test-formula", "testrig"})
	if err != nil && strings.Contains(err.Error(), "standalone formula cannot be scheduled") {
		t.Fatalf("gh#3917 regression: standalone formula rejected in deferred mode: %v", err)
	}
	// Any other error (e.g., no polecat to spawn) is acceptable — the guard is what we're testing.
}

// TestResolveTargetSelfSlingByPane verifies that a named target resolving to the
// caller's own tmux pane sets IsSelfSling=true (GH#3839). Without this, gt sling
// deacon (from the deacon itself) injects the ack prompt into the running agent's
// pane, wedging it mid-command.
func TestResolveTargetSelfSlingByPane(t *testing.T) {
	const callerPane = "%42"

	prev := resolveTargetAgentFn
	t.Cleanup(func() { resolveTargetAgentFn = prev })

	t.Run("named_target_same_pane_is_self_sling", func(t *testing.T) {
		resolveTargetAgentFn = func(_ string) (string, string, string, error) {
			return "deacon/", callerPane, "/home/deacon", nil
		}
		t.Setenv("TMUX_PANE", callerPane)

		result, err := resolveTarget("deacon", ResolveTargetOptions{})
		if err != nil {
			t.Fatalf("resolveTarget: %v", err)
		}
		if !result.IsSelfSling {
			t.Error("expected IsSelfSling=true when named target pane matches caller pane")
		}
	})

	t.Run("named_target_different_pane_is_not_self_sling", func(t *testing.T) {
		resolveTargetAgentFn = func(_ string) (string, string, string, error) {
			return "deacon/", "%99", "/home/deacon", nil
		}
		t.Setenv("TMUX_PANE", callerPane)

		result, err := resolveTarget("deacon", ResolveTargetOptions{})
		if err != nil {
			t.Fatalf("resolveTarget: %v", err)
		}
		if result.IsSelfSling {
			t.Error("expected IsSelfSling=false when named target pane differs from caller pane")
		}
	})

	t.Run("empty_pane_is_not_self_sling", func(t *testing.T) {
		resolveTargetAgentFn = func(_ string) (string, string, string, error) {
			return "deacon/", "", "/home/deacon", nil
		}
		t.Setenv("TMUX_PANE", callerPane)

		result, err := resolveTarget("deacon", ResolveTargetOptions{})
		if err != nil {
			t.Fatalf("resolveTarget: %v", err)
		}
		if result.IsSelfSling {
			t.Error("expected IsSelfSling=false when resolved pane is empty (no tmux)")
		}
	})
}
