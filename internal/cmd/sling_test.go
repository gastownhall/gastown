package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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

func TestSlingFormulaOnBeadRoutesBDCommandsToTargetRig(t *testing.T) {
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

	// Stub bd so we can observe the working directory for cook/wisp/bond.
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -e
echo "$(pwd)|$*" >> "${BD_LOG}"
cmd="$1"
shift || true
case "$cmd" in
  show)
    echo '[{"title":"Test issue","status":"open","assignee":"","description":""}]'
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
        echo '{"new_epic_id":"gt-wisp-xyz"}'
        ;;
      bond)
        echo '{"root_id":"gt-wisp-xyz"}'
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
if "%cmd%"=="show" (
  echo [{"title":"Test issue","status":"open","assignee":"","description":""}]
  exit /b 0
)
if "%cmd%"=="formula" (
  echo {"name":"test-formula"}
  exit /b 0
)
if "%cmd%"=="cook" exit /b 0
if "%cmd%"=="mol" (
  if "%sub%"=="wisp" (
    echo {"new_epic_id":"gt-wisp-xyz"}
    exit /b 0
  )
  if "%sub%"=="bond" (
    echo {"root_id":"gt-wisp-xyz"}
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
	logLines := strings.Split(strings.TrimSpace(string(logBytes)), "\n")

	wantDir := rigDir
	if resolved, err := filepath.EvalSymlinks(wantDir); err == nil {
		wantDir = resolved
	}
	gotCook := false
	gotWisp := false
	gotBond := false

	for _, line := range logLines {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		dir := parts[0]
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			dir = resolved
		}
		args := parts[1]

		switch {
		case strings.Contains(args, "cook "):
			gotCook = true
			// cook doesn't need database context, runs from cwd
		case strings.Contains(args, "mol wisp "):
			gotWisp = true
			if dir != wantDir {
				t.Fatalf("bd mol wisp ran in %q, want %q (args: %q)", dir, wantDir, args)
			}
		case strings.Contains(args, "mol bond "):
			gotBond = true
			if dir != wantDir {
				t.Fatalf("bd mol bond ran in %q, want %q (args: %q)", dir, wantDir, args)
			}
		}
	}

	if !gotCook || !gotWisp || !gotBond {
		t.Fatalf("missing expected bd commands: cook=%v wisp=%v bond=%v (log: %q)", gotCook, gotWisp, gotBond, string(logBytes))
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
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", "mayor", "rig"), 0755); err != nil {
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
	t.Cleanup(func() {
		slingVars = prevVars
		slingDryRun = prevDryRun
		slingNoBoot = prevNoBoot
	})

	slingVars = []string{"version=1.2.3", "channel=stable"}
	slingDryRun = false
	slingNoBoot = true

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

// TestSlingFormulaOnBeadPassesFeatureAndIssueVars verifies that when using
// gt sling <formula> --on <bead>, both --var feature=<title> and --var issue=<beadID>
// are passed to the bd mol wisp command.
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

	// Stub bd so we can observe the arguments passed to mol wisp.
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
        echo '{"new_epic_id":"gt-wisp-xyz"}'
        ;;
      bond)
        echo '{"root_id":"gt-wisp-xyz"}'
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
    echo {^"new_epic_id^":^"gt-wisp-xyz^"}
    exit /b 0
  )
  if "%sub%"=="bond" (
    echo {^"root_id^":^"gt-wisp-xyz^"}
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

	// Find the mol wisp command and verify both --var arguments
	logLines := strings.Split(string(logBytes), "\n")
	var wispLine string
	for _, line := range logLines {
		if strings.Contains(line, "mol wisp") {
			wispLine = line
			break
		}
	}

	if wispLine == "" {
		t.Fatalf("mol wisp command not found in log: %s", string(logBytes))
	}

	// Verify --var feature=<title> is present
	if !containsVarArg(wispLine, "feature", "My Test Feature") {
		t.Errorf("mol wisp missing --var feature=<title>\ngot: %s", wispLine)
	}

	// Verify --var issue=<beadID> is present
	if !containsVarArg(wispLine, "issue", "gt-abc123") {
		t.Errorf("mol wisp missing --var issue=<beadID>\ngot: %s", wispLine)
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
        echo '{"new_epic_id":"gt-wisp-xyz"}'
        ;;
      bond)
        echo '{"root_id":"gt-wisp-xyz"}'
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
    echo {^"new_epic_id^":^"gt-wisp-xyz^"}
    exit /b 0
  )
  if "%sub%"=="bond" (
    echo {^"root_id^":^"gt-wisp-xyz^"}
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
			name:        "town-level: hq bead to rig (rejected — belongs to town root)",
			beadID:      "hq-abc123",
			targetAgent: "gastown/polecats/Toast",
			wantErr:     true,
		},
		{
			name:        "unknown prefix: rejected (no route maps to target rig)",
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
				if !strings.Contains(errMsg, "cross-rig mismatch") && !strings.Contains(errMsg, "town root") {
					t.Errorf("expected cross-rig or town-root error, got: %v", err)
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

func TestBuildSlingFieldUpdatesIncludesConvoyFields(t *testing.T) {
	got := buildSlingFieldUpdates(
		"mayor",
		"review this",
		[]string{"feature=test"},
		"gt-wisp-test",
		"mol-polecat-work",
		false,
		false,
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
