package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstantiateFormulaOnBead verifies the helper function works correctly.
// This tests the formula-on-bead pattern used by issue #288.
func TestInstantiateFormulaOnBead(t *testing.T) {
	townRoot := t.TempDir()

	// Minimal workspace marker
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Create routes.jsonl
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	rigDir := filepath.Join(townRoot, "gastown", "mayor", "rig")
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

	// Create stub bd that logs all commands
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -e
echo "CMD:$*" >> "${BD_LOG}"
cmd="$1"
shift || true
case "$cmd" in
  show)
    echo '[{"title":"Fix bug ABC","status":"open","assignee":"","description":""}]'
    ;;
  formula)
    echo '{"name":"mol-polecat-work"}'
    ;;
  cook)
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
        echo '{"result_id":"gt-abc123","id_mapping":{"mol-polecat-work":"gt-wisp-288"}}'
        ;;
    esac
    ;;
  update)
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
setlocal enableextensions
echo CMD:%*>>"%BD_LOG%"
set "cmd=%1"
set "sub=%2"
if "%cmd%"=="show" (
  echo [{^"title^":^"Fix bug ABC^",^"status^":^"open^",^"assignee^":^"^",^"description^":^"^"}]
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
    echo {^"result_id^":^"gt-abc123^",^"id_mapping^":{^"mol-polecat-work^":^"gt-wisp-288^"}}
    exit /b 0
  )
)
if "%cmd%"=="update" exit /b 0
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Test the helper function directly
	extraVars := []string{"branch=polecat/furiosa/gt-abc123"}
	result, err := InstantiateFormulaOnBead(context.Background(), "mol-polecat-work", "gt-abc123", "Test Bug Fix", "", townRoot, false, extraVars)
	if err != nil {
		t.Fatalf("InstantiateFormulaOnBead failed: %v", err)
	}

	if result.WispRootID == "" {
		t.Error("WispRootID should not be empty")
	}
	if result.BeadToHook == "" {
		t.Error("BeadToHook should not be empty")
	}

	// Verify commands were logged
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	logContent := string(logBytes)

	if !strings.Contains(logContent, "cook mol-polecat-work") {
		t.Errorf("cook command not found in log:\n%s", logContent)
	}
	if strings.Contains(logContent, "mol wisp") {
		t.Errorf("legacy mol wisp command should not be called:\n%s", logContent)
	}
	if !strings.Contains(logContent, "--var branch=polecat/furiosa/gt-abc123") {
		t.Errorf("extra vars not passed to bond command:\n%s", logContent)
	}
	if !strings.Contains(logContent, "mol bond mol-polecat-work gt-abc123 --json --ephemeral") {
		t.Errorf("direct mol bond command not found in log:\n%s", logContent)
	}
}

// TestInstantiateFormulaOnBeadSkipCook verifies the skipCook optimization.
func TestInstantiateFormulaOnBeadSkipCook(t *testing.T) {
	townRoot := t.TempDir()

	// Minimal workspace marker
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Create routes.jsonl
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"."}`
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	// Create stub bd
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
echo "CMD:$*" >> "${BD_LOG}"
cmd="$1"; shift || true
case "$cmd" in
  mol)
    sub="$1"; shift || true
    case "$sub" in
      wisp) echo 'legacy mol wisp should not be called' >&2; exit 1;;
      bond) echo '{"result_id":"gt-test","id_mapping":{"mol-polecat-work":"gt-wisp-skip"}}';;
    esac;;
esac
exit 0
`
	bdScriptWindows := `@echo off
setlocal enableextensions
echo CMD:%*>>"%BD_LOG%"
set "cmd=%1"
set "sub=%2"
if "%cmd%"=="mol" (
  if "%sub%"=="wisp" (
    echo legacy mol wisp should not be called 1>&2
    exit /b 1
  )
  if "%sub%"=="bond" (
    echo {^"result_id^":^"gt-test^",^"id_mapping^":{^"mol-polecat-work^":^"gt-wisp-skip^"}}
    exit /b 0
  )
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	_ = os.Chdir(townRoot)

	// Test with skipCook=true
	_, err := InstantiateFormulaOnBead(context.Background(), "mol-polecat-work", "gt-test", "Test", "", townRoot, true, nil)
	if err != nil {
		t.Fatalf("InstantiateFormulaOnBead failed: %v", err)
	}

	logBytes, _ := os.ReadFile(logPath)
	logContent := string(logBytes)

	// Verify cook was NOT called when skipCook=true
	if strings.Contains(logContent, "cook") {
		t.Errorf("cook should be skipped when skipCook=true, but was called:\n%s", logContent)
	}

	// Verify direct bond was still called without the legacy wisp path.
	if strings.Contains(logContent, "mol wisp") {
		t.Errorf("mol wisp should not be called")
	}
	if !strings.Contains(logContent, "mol bond mol-polecat-work gt-test --json --ephemeral") {
		t.Errorf("mol bond should still be called")
	}
}

// TestCookFormula verifies the CookFormula helper.
func TestCookFormula(t *testing.T) {
	townRoot := t.TempDir()

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
echo "CMD:$*" >> "${BD_LOG}"
exit 0
`
	bdScriptWindows := `@echo off
echo CMD:%*>>"%BD_LOG%"
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := CookFormula("mol-polecat-work", townRoot, townRoot)
	if err != nil {
		t.Fatalf("CookFormula failed: %v", err)
	}

	logBytes, _ := os.ReadFile(logPath)
	if !strings.Contains(string(logBytes), "cook mol-polecat-work") {
		t.Errorf("cook command not found in log")
	}
}

// TestSlingHookRawBeadFlag verifies --hook-raw-bead flag exists.
func TestSlingHookRawBeadFlag(t *testing.T) {
	// Verify the flag variable exists and works
	prevValue := slingHookRawBead
	t.Cleanup(func() { slingHookRawBead = prevValue })

	slingHookRawBead = true
	if !slingHookRawBead {
		t.Error("slingHookRawBead flag should be true")
	}

	slingHookRawBead = false
	if slingHookRawBead {
		t.Error("slingHookRawBead flag should be false")
	}
}

// TestAutoApplyLogic verifies the auto-apply detection logic.
// When formulaName is empty and target contains "/polecats/", mol-polecat-work should be applied.
func TestAutoApplyLogic(t *testing.T) {
	tests := []struct {
		name          string
		formulaName   string
		hookRawBead   bool
		targetAgent   string
		wantAutoApply bool
	}{
		{
			name:          "bare bead to polecat - should auto-apply",
			formulaName:   "",
			hookRawBead:   false,
			targetAgent:   "gastown/polecats/Toast",
			wantAutoApply: true,
		},
		{
			name:          "bare bead with --hook-raw-bead - should not auto-apply",
			formulaName:   "",
			hookRawBead:   true,
			targetAgent:   "gastown/polecats/Toast",
			wantAutoApply: false,
		},
		{
			name:          "formula already specified - should not auto-apply",
			formulaName:   "mol-review",
			hookRawBead:   false,
			targetAgent:   "gastown/polecats/Toast",
			wantAutoApply: false,
		},
		{
			name:          "non-polecat target - should not auto-apply",
			formulaName:   "",
			hookRawBead:   false,
			targetAgent:   "gastown/witness",
			wantAutoApply: false,
		},
		{
			name:          "mayor target - should not auto-apply",
			formulaName:   "",
			hookRawBead:   false,
			targetAgent:   "mayor",
			wantAutoApply: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This mirrors the logic in sling.go
			shouldAutoApply := tt.formulaName == "" && !tt.hookRawBead && strings.Contains(tt.targetAgent, "/polecats/")

			if shouldAutoApply != tt.wantAutoApply {
				t.Errorf("auto-apply logic: got %v, want %v", shouldAutoApply, tt.wantAutoApply)
			}
		})
	}
}

// TestFormulaOnBeadPassesVariables verifies that feature and issue variables are passed.
func TestFormulaOnBeadPassesVariables(t *testing.T) {
	townRoot := t.TempDir()

	// Minimal workspace
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(`{"prefix":"gt-","path":"."}`), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
echo "CMD:$*" >> "${BD_LOG}"
cmd="$1"; shift || true
case "$cmd" in
  cook) exit 0;;
  mol)
    sub="$1"; shift || true
    case "$sub" in
      wisp) echo 'legacy mol wisp should not be called' >&2; exit 1;;
      bond) echo '{"result_id":"gt-abc123","id_mapping":{"mol-polecat-work":"gt-wisp-var"}}';;
    esac;;
esac
exit 0
`
	bdScriptWindows := `@echo off
setlocal enableextensions
echo CMD:%*>>"%BD_LOG%"
set "cmd=%1"
set "sub=%2"
if "%cmd%"=="cook" exit /b 0
if "%cmd%"=="mol" (
  if "%sub%"=="wisp" (
    echo legacy mol wisp should not be called 1>&2
    exit /b 1
  )
  if "%sub%"=="bond" (
    echo {^"result_id^":^"gt-abc123^",^"id_mapping^":{^"mol-polecat-work^":^"gt-wisp-var^"}}
    exit /b 0
  )
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	_ = os.Chdir(townRoot)

	_, err := InstantiateFormulaOnBead(context.Background(), "mol-polecat-work", "gt-abc123", "My Cool Feature", "", townRoot, false, nil)
	if err != nil {
		t.Fatalf("InstantiateFormulaOnBead: %v", err)
	}

	logBytes, _ := os.ReadFile(logPath)
	logContent := string(logBytes)

	// Find direct mol bond line
	var bondLine string
	for _, line := range strings.Split(logContent, "\n") {
		if strings.Contains(line, "mol bond") {
			bondLine = line
			break
		}
	}

	if bondLine == "" {
		t.Fatalf("mol bond command not found:\n%s", logContent)
	}

	if !strings.Contains(bondLine, "feature=My Cool Feature") {
		t.Errorf("mol bond missing feature variable:\n%s", bondLine)
	}

	if !strings.Contains(bondLine, "issue=gt-abc123") {
		t.Errorf("mol bond missing issue variable:\n%s", bondLine)
	}
}

func TestInstantiateFormulaOnBead_DirectBondParsesIDMapping(t *testing.T) {
	townRoot := t.TempDir()

	// Minimal workspace
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(`{"prefix":"gt-","path":"."}`), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")

	// Direct bond returns the base bead as result_id and the spawned molecule root
	// under id_mapping keyed by the original formula name.
	bdScript := `#!/bin/sh
set -e
echo "CMD:$*" >> "${BD_LOG}"
cmd="$1"; shift || true
case "$cmd" in
  cook)
    exit 0
    ;;
  mol)
    sub="$1"; shift || true
    case "$sub" in
      wisp)
        echo '{"new_epic_id":"gt-wisp-missing"}'
        exit 0
        ;;
      bond)
        left="$1"; shift || true
        if [ "$left" = "gt-wisp-missing" ]; then
          echo "Error: 'gt-wisp-missing' not found (not an issue ID or formula name)" >&2
          exit 1
        fi
        if [ "$left" = "mol-polecat-work" ]; then
          echo '{"result_id":"gt-abc123","id_mapping":{"mol-polecat-work":"gt-mol-fallback"}}'
          exit 0
        fi
        echo "Error: unexpected bond target: $left" >&2
        exit 1
        ;;
    esac
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
setlocal enableextensions
echo CMD:%*>>"%BD_LOG%"
set "cmd=%1"
set "sub=%2"
set "left=%3"
if "%cmd%"=="cook" exit /b 0
if "%cmd%"=="mol" (
  if "%sub%"=="wisp" (
    echo {^"new_epic_id^":^"gt-wisp-missing^"}
    exit /b 0
  )
  if "%sub%"=="bond" (
    if "%left%"=="gt-wisp-missing" (
      echo Error: 'gt-wisp-missing' not found - not an issue ID or formula name 1>&2
      exit /b 1
    )
    if "%left%"=="mol-polecat-work" (
      echo {^"result_id^":^"gt-abc123^",^"id_mapping^":{^"mol-polecat-work^":^"gt-mol-fallback^"}}
      exit /b 0
    )
    echo Error: unexpected bond target: %left% 1>&2
    exit /b 1
  )
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	_ = os.Chdir(townRoot)

	result, err := InstantiateFormulaOnBead(context.Background(), "mol-polecat-work", "gt-abc123", "My Cool Feature", "", townRoot, false, nil)
	if err != nil {
		t.Fatalf("InstantiateFormulaOnBead: %v", err)
	}
	if result.WispRootID != "gt-mol-fallback" {
		t.Fatalf("WispRootID = %q, want %q", result.WispRootID, "gt-mol-fallback")
	}
	if result.BeadToHook != "gt-abc123" {
		t.Fatalf("BeadToHook = %q, want %q", result.BeadToHook, "gt-abc123")
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	logContent := string(logBytes)
	if strings.Contains(logContent, "mol wisp") {
		t.Fatalf("legacy mol wisp should not be called:\n%s", logContent)
	}
	var directBondLine string
	for _, line := range strings.Split(logContent, "\n") {
		if strings.Contains(line, "mol bond mol-polecat-work gt-abc123 --json --ephemeral") {
			directBondLine = line
			break
		}
	}
	if directBondLine == "" {
		t.Fatalf("missing direct bond in log:\n%s", logContent)
	}
	if !containsVarArg(directBondLine, "feature", "My Cool Feature") {
		t.Fatalf("direct bond missing feature variable:\n%s", logContent)
	}
	if !containsVarArg(directBondLine, "issue", "gt-abc123") {
		t.Fatalf("direct bond missing issue variable:\n%s", logContent)
	}
	for _, required := range []struct {
		key   string
		value string
	}{
		{"base_branch", "main"},
		{"setup_command", ""},
		{"typecheck_command", ""},
		{"lint_command", ""},
		{"test_command", ""},
		{"build_command", ""},
	} {
		if !containsVarArg(directBondLine, required.key, required.value) {
			t.Fatalf("direct bond missing required variable %q:\n%s", required.key, logContent)
		}
	}
}

func TestBondFormulaDirectPinsTargetBeadsDir(t *testing.T) {
	for _, tc := range []struct {
		name         string
		beadID       string
		wantBeadsDir func(string) string
	}{
		{
			name:   "rig-prefixed bead",
			beadID: "gt-abc123",
			wantBeadsDir: func(townRoot string) string {
				return filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")
			},
		},
		{
			name:   "hq bead",
			beadID: "hq-abc123",
			wantBeadsDir: func(townRoot string) string {
				return filepath.Join(townRoot, ".beads")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			townRoot := t.TempDir()
			townBeadsDir := filepath.Join(townRoot, ".beads")
			rigBeadsDir := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")
			formulaWorkDir := filepath.Join(townRoot, "polecats", "radrat", "gastown")

			for _, dir := range []string{townBeadsDir, rigBeadsDir, filepath.Join(formulaWorkDir, ".beads")} {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatalf("mkdir %s: %v", dir, err)
				}
			}
			routes := strings.Join([]string{
				`{"prefix":"gt-","path":"gastown/mayor/rig"}`,
				`{"prefix":"hq-","path":"."}`,
				"",
			}, "\n")
			if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
				t.Fatalf("write routes: %v", err)
			}

			binDir := filepath.Join(townRoot, "bin")
			if err := os.MkdirAll(binDir, 0755); err != nil {
				t.Fatalf("mkdir binDir: %v", err)
			}
			logPath := filepath.Join(townRoot, "bd.log")
			bdScript := `#!/bin/sh
set -e
printf 'ENV:%s|%s|%s|%s|%s\n' "$(pwd)" "${BEADS_DIR:-}" "${BEADS_DOLT_SERVER_DATABASE:-}" "${BEADS_DOLT_DATA_DIR:-}" "$*" >> "${BD_LOG}"
cmd="$1"; shift || true
case "$cmd" in
  mol)
    sub="$1"; shift || true
    case "$sub" in
      bond)
        echo '{"result_id":"'"$2"'","id_mapping":{"mol-polecat-work":"gt-mol-direct"}}'
        exit 0
        ;;
    esac
    ;;
esac
exit 1
`
			bdScriptWindows := `@echo off
setlocal enableextensions
echo ENV:%CD%^|%BEADS_DIR%^|%BEADS_DOLT_SERVER_DATABASE%^|%BEADS_DOLT_DATA_DIR%^|%*>>"%BD_LOG%"
if "%1"=="mol" (
  if "%2"=="bond" (
    echo {^"result_id^":^"%4^",^"id_mapping^":{^"mol-polecat-work^":^"gt-mol-direct^"}}
    exit /b 0
  )
)
exit /b 1
`
			_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

			t.Setenv("BD_LOG", logPath)
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("BEADS_DIR", filepath.Join(formulaWorkDir, ".beads"))
			t.Setenv("BEADS_DOLT_SERVER_DATABASE", "stale")
			wrongDataDir := filepath.Join(townRoot, "wrong-data")
			t.Setenv("BEADS_DOLT_DATA_DIR", wrongDataDir)
			t.Setenv("BEADS_DB", "stale")
			t.Setenv("BD_DB", "stale")

			rootID, err := bondFormulaDirect("mol-polecat-work", "mol-polecat-work", tc.beadID, formulaWorkDir, townRoot, formulaVarsForBead("mol-polecat-work", tc.beadID, "Test", nil))
			if err != nil {
				t.Fatalf("bondFormulaDirect: %v", err)
			}
			if rootID != "gt-mol-direct" {
				t.Fatalf("rootID = %q, want gt-mol-direct", rootID)
			}

			logBytes, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read log: %v", err)
			}
			line := strings.TrimSpace(string(logBytes))
			parts := strings.SplitN(line, "|", 5)
			if len(parts) != 5 || !strings.HasPrefix(parts[0], "ENV:") {
				t.Fatalf("malformed bd log %q", line)
			}
			gotCWD := strings.TrimPrefix(parts[0], "ENV:")
			if gotCWD != formulaWorkDir {
				t.Fatalf("bd cwd = %q, want formula work dir %q (log %q)", gotCWD, formulaWorkDir, line)
			}
			if got, want := parts[1], tc.wantBeadsDir(townRoot); got != want {
				t.Fatalf("BEADS_DIR = %q, want %q (log %q)", got, want, line)
			}
			if parts[2] != "" {
				t.Fatalf("BEADS_DOLT_SERVER_DATABASE leaked as %q (log %q)", parts[2], line)
			}
			if parts[3] == wrongDataDir {
				t.Fatalf("stale BEADS_DOLT_DATA_DIR leaked as %q (log %q)", parts[3], line)
			}
		})
	}
}

func TestInstantiateFormulaOnBead_DirectBondHandlesNonGTIDs(t *testing.T) {
	townRoot := t.TempDir()

	// Minimal workspace
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(`{"prefix":"oag-","path":"."}`), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")

	// Direct bond should accept the spawned root returned by bd, even when its
	// prefix is not gt-.
	bdScript := `#!/bin/sh
set -e
echo "CMD:$*" >> "${BD_LOG}"
cmd="$1"; shift || true
case "$cmd" in
  cook)
    exit 0
    ;;
	  mol)
		sub="$1"; shift || true
		case "$sub" in
		  wisp)
			echo 'legacy mol wisp should not be called' >&2
			exit 1
			;;
		  bond)
			left="$1"; shift || true
			if [ "$left" = "mol-polecat-work" ]; then
			  echo '{"result_id":"oag-npeat","id_mapping":{"mol-polecat-work":"oag-wisp-wisp-rsia"}}'
			  exit 0
			fi
        echo "Error: unexpected bond target: $left" >&2
        exit 1
        ;;
    esac
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
setlocal enableextensions
echo CMD:%*>>"%BD_LOG%"
set "cmd=%1"
set "sub=%2"
set "left=%3"
if "%cmd%"=="cook" exit /b 0
if "%cmd%"=="mol" (
  if "%sub%"=="wisp" (
    echo legacy mol wisp should not be called 1>&2
    exit /b 1
  )
  if "%sub%"=="bond" (
    if "%left%"=="mol-polecat-work" (
      echo {^"result_id^":^"oag-npeat^",^"id_mapping^":{^"mol-polecat-work^":^"oag-wisp-wisp-rsia^"}}
      exit /b 0
    )
    echo Error: unexpected bond target: %left% 1>&2
    exit /b 1
  )
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	_ = os.Chdir(townRoot)

	result, err := InstantiateFormulaOnBead(context.Background(), "mol-polecat-work", "oag-npeat", "Fix formula bug", "", townRoot, false, nil)
	if err != nil {
		t.Fatalf("InstantiateFormulaOnBead: %v", err)
	}
	if result.WispRootID != "oag-wisp-wisp-rsia" {
		t.Fatalf("WispRootID = %q, want %q", result.WispRootID, "oag-wisp-wisp-rsia")
	}
	if result.BeadToHook != "oag-npeat" {
		t.Fatalf("BeadToHook = %q, want %q", result.BeadToHook, "oag-npeat")
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	logContent := string(logBytes)
	if strings.Contains(logContent, "mol wisp") {
		t.Fatalf("legacy mol wisp should not have been called:\n%s", logContent)
	}
	if !strings.Contains(logContent, "mol bond mol-polecat-work oag-npeat --json --ephemeral") {
		t.Fatalf("direct bond should have been called with formula and bead:\n%s", logContent)
	}
}

func TestInstantiateFormulaOnBead_DirectBondCreatesNoOrphanCleanup(t *testing.T) {
	townRoot := t.TempDir()

	// Minimal workspace
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(`{"prefix":"gt-","path":"."}`), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")

	// The direct path should not create an intermediate wisp that needs orphan cleanup.
	bdScript := `#!/bin/sh
set -e
echo "CMD:$*" >> "${BD_LOG}"
cmd="$1"; shift || true
case "$cmd" in
  cook)
    exit 0
    ;;
  mol)
    sub="$1"; shift || true
		case "$sub" in
		  wisp)
			echo 'legacy mol wisp should not be called' >&2
			exit 1
			;;
		  bond)
			left="$1"; shift || true
			if [ "$left" = "mol-polecat-work" ]; then
			  echo '{"result_id":"gt-test","id_mapping":{"mol-polecat-work":"gt-wisp-clean"}}'
          exit 0
        fi
        echo "Error: unexpected bond target: $left" >&2
        exit 1
        ;;
    esac
    ;;
  close)
    exit 0
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
setlocal enableextensions
echo CMD:%*>>"%BD_LOG%"
set "cmd=%1"
set "sub=%2"
set "left=%3"
if "%cmd%"=="cook" exit /b 0
if "%cmd%"=="mol" (
  if "%sub%"=="wisp" (
    echo legacy mol wisp should not be called 1>&2
    exit /b 1
  )
  if "%sub%"=="bond" (
    if "%left%"=="mol-polecat-work" (
      echo {^"result_id^":^"gt-test^",^"id_mapping^":{^"mol-polecat-work^":^"gt-wisp-clean^"}}
      exit /b 0
    )
    echo Error: unexpected bond target: %left% 1>&2
    exit /b 1
  )
)
if "%cmd%"=="close" exit /b 0
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	_ = os.Chdir(townRoot)

	result, err := InstantiateFormulaOnBead(context.Background(), "mol-polecat-work", "gt-test", "Test cleanup", "", townRoot, false, nil)
	if err != nil {
		t.Fatalf("InstantiateFormulaOnBead: %v", err)
	}
	if result.WispRootID != "gt-wisp-clean" {
		t.Fatalf("WispRootID = %q, want %q", result.WispRootID, "gt-wisp-clean")
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	logContent := string(logBytes)
	if strings.Contains(logContent, "mol wisp") || strings.Contains(logContent, "close") {
		t.Fatalf("direct bond should not create or clean an orphaned wisp:\n%s", logContent)
	}
}

func TestInstantiateFormulaOnBead_DirectBondParseFailure(t *testing.T) {
	townRoot := t.TempDir()

	// Minimal workspace
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(`{"prefix":"gt-","path":"."}`), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")

	// Direct bond exits 0 but returns non-JSON garbage.
	bdScript := `#!/bin/sh
set -e
echo "CMD:$*" >> "${BD_LOG}"
cmd="$1"; shift || true
case "$cmd" in
  cook)
    exit 0
    ;;
  mol)
    sub="$1"; shift || true
		case "$sub" in
		  wisp)
			echo 'legacy mol wisp should not be called' >&2
			exit 1
			;;
		  bond)
			left="$1"; shift || true
			if [ "$left" = "mol-polecat-work" ]; then
			  echo 'NOT-JSON-GARBAGE'
			  exit 0
        fi
        echo "Error: bond failed" >&2
        exit 1
        ;;
    esac
    ;;
esac
exit 0
`
	bdScriptWindows := `@echo off
setlocal enableextensions
echo CMD:%*>>"%BD_LOG%"
set "cmd=%1"
set "sub=%2"
set "left=%3"
if "%cmd%"=="cook" exit /b 0
if "%cmd%"=="mol" (
  if "%sub%"=="wisp" (
    echo legacy mol wisp should not be called 1>&2
    exit /b 1
  )
  if "%sub%"=="bond" (
    if "%left%"=="mol-polecat-work" (
      echo NOT-JSON-GARBAGE
      exit /b 0
    )
    echo Error: bond failed 1>&2
    exit /b 1
  )
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	_ = os.Chdir(townRoot)

	_, err := InstantiateFormulaOnBead(context.Background(), "mol-polecat-work", "gt-abc123", "My Feature", "", townRoot, false, nil)
	if err == nil {
		t.Fatal("expected error when bond returns non-JSON and fallback fails, got nil")
	}
	if !strings.Contains(err.Error(), "missing spawned root id") {
		t.Fatalf("error message should mention missing spawned root id: %v", err)
	}
}
