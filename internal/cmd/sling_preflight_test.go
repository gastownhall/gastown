package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestRunSlingPreflightsFormulaBondBeforeRigSpawn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell bd stub")
	}
	townRoot, rigDir := setupSlingPreflightTown(t)
	logPath := installPreflightFailingBDStub(t, townRoot)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	prevSpawn := spawnPolecatForSling
	prevDryRun := slingDryRun
	prevNoConvoy := slingNoConvoy
	prevHookRaw := slingHookRawBead
	prevFormula := slingFormula
	prevVars := slingVars
	prevOn := slingOnTarget
	t.Cleanup(func() {
		spawnPolecatForSling = prevSpawn
		slingDryRun = prevDryRun
		slingNoConvoy = prevNoConvoy
		slingHookRawBead = prevHookRaw
		slingFormula = prevFormula
		slingVars = prevVars
		slingOnTarget = prevOn
	})

	spawnCalled := false
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		spawnCalled = true
		return &SpawnedPolecatInfo{PolecatName: "toast", RigName: rigName, ClonePath: filepath.Join(rigDir, "polecat")}, nil
	}
	slingDryRun = false
	slingNoConvoy = true
	slingHookRawBead = false
	slingFormula = ""
	slingVars = nil
	slingOnTarget = ""

	err = runSling(nil, []string{"gt-preflight", "gastown/polecats/toast"})
	if err == nil || !strings.Contains(err.Error(), "formula bond preflight") {
		t.Fatalf("runSling error = %v, want formula bond preflight failure", err)
	}
	if spawnCalled {
		t.Fatal("spawnPolecatForSling called before formula bond preflight succeeded")
	}
	assertPreflightUsedRigBeadsDir(t, logPath, rigDir)
}

func TestExecuteSlingPreflightsFormulaBondBeforeSpawn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell bd stub")
	}
	townRoot, rigDir := setupSlingPreflightTown(t)
	logPath := installPreflightFailingBDStub(t, townRoot)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	prevSpawn := spawnPolecatForSling
	t.Cleanup(func() { spawnPolecatForSling = prevSpawn })
	spawnCalled := false
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		spawnCalled = true
		return &SpawnedPolecatInfo{PolecatName: "toast", RigName: rigName, ClonePath: filepath.Join(rigDir, "polecat")}, nil
	}

	_, err = executeSling(SlingParams{
		BeadID:           "gt-preflight",
		RigName:          "gastown",
		FormulaName:      "mol-polecat-work",
		FormulaFailFatal: true,
		NoConvoy:         true,
		NoBoot:           true,
		TownRoot:         townRoot,
		BeadsDir:         filepath.Join(rigDir, ".beads"),
	})
	if err == nil || !strings.Contains(err.Error(), "formula bond preflight") {
		t.Fatalf("executeSling error = %v, want formula bond preflight failure", err)
	}
	if spawnCalled {
		t.Fatal("spawnPolecatForSling called before formula bond preflight succeeded")
	}
	assertPreflightUsedRigBeadsDir(t, logPath, rigDir)
}

func setupSlingPreflightTown(t *testing.T) (string, string) {
	t.Helper()
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)

	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "gastown", "mayor", "rig")
	for _, dir := range []string{
		filepath.Join(townRoot, "mayor", "rig"),
		filepath.Join(townRoot, ".beads"),
		filepath.Join(rigDir, ".beads"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"type":"town","name":"test"}`), 0o644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown/mayor/rig"}`,
		`{"prefix":"hq-","path":"."}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0o644); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "metadata.json"), []byte(`{"dolt_database":"hq","dolt_server_host":"127.0.0.1","dolt_server_port":3307}`), 0o644); err != nil {
		t.Fatalf("write town metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigDir, ".beads", "metadata.json"), []byte(`{"dolt_database":"gastown","dolt_server_host":"127.0.0.2","dolt_server_port":4407}`), 0o644); err != nil {
		t.Fatalf("write rig metadata: %v", err)
	}
	t.Setenv("GT_TEST_NO_NUDGE", "1")
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1")
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("BEADS_DIR", filepath.Join(townRoot, ".beads"))
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "stale")
	t.Setenv("BEADS_DOLT_DATA_DIR", filepath.Join(townRoot, "wrong-data"))
	return townRoot, rigDir
}

func installPreflightFailingBDStub(t *testing.T, townRoot string) string {
	t.Helper()
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	script := `#!/bin/sh
set -eu
log_args=""
for arg in "$@"; do
  log_args="${log_args}${log_args:+ }${arg}"
done
printf '%s|%s|%s|%s\n' "$(pwd)" "${BEADS_DIR:-}" "${BEADS_DOLT_SERVER_DATABASE:-}" "$log_args" >> "${BD_LOG}"
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
  --version|version)
    echo 'bd version 1.0.5'
    exit 0
    ;;
  show)
    echo '[{"id":"gt-preflight","title":"Preflight issue","status":"open","assignee":"","description":"","issue_type":"bug"}]'
    exit 0
    ;;
  mol)
    sub="$1"
    if [ "$sub" = "bond" ]; then
      echo 'Error: gt-preflight not found (not an issue ID or formula name)' >&2
      exit 7
    fi
    ;;
esac
exit 0
`
	_ = writeBDStub(t, binDir, script, "")
	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func assertPreflightUsedRigBeadsDir(t *testing.T, logPath, rigDir string) {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 {
			t.Fatalf("malformed bd log line: %q", line)
		}
		args := parts[3]
		if !strings.Contains(args, "mol bond mol-polecat-work gt-preflight --dry-run --ephemeral") {
			continue
		}
		if parts[0] != rigDir {
			t.Fatalf("preflight cwd = %q, want %q (line %q)", parts[0], rigDir, line)
		}
		wantBeadsDir := filepath.Join(rigDir, ".beads")
		if parts[1] != wantBeadsDir {
			t.Fatalf("preflight BEADS_DIR = %q, want %q (line %q)", parts[1], wantBeadsDir, line)
		}
		if parts[2] != "" {
			t.Fatalf("preflight leaked BEADS_DOLT_SERVER_DATABASE %q (line %q)", parts[2], line)
		}
		return
	}
	t.Fatalf("preflight mol bond command not found in log:\n%s", data)
}
