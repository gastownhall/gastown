package beads

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
)

func installMockBDRecorder(t *testing.T) string {
	t.Helper()

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")

	if runtime.GOOS == "windows" {
		psPath := filepath.Join(binDir, "bd.ps1")
		psScript := `# Mock bd for beads tests.
$logFile = '` + strings.ReplaceAll(logPath, "'", "''") + `'
Add-Content -Path $logFile -Value ($args -join ' ')

$cmd = ''
foreach ($arg in $args) {
  if ($arg -like '--*') { continue }
  $cmd = $arg
  break
}

switch ($cmd) {
  'init' {
    $target = $env:BEADS_DIR
    if ([string]::IsNullOrEmpty($target)) {
      $target = Join-Path (Get-Location) '.beads'
    }

    $prefix = 'gt'
    for ($i = 0; $i -lt $args.Length; $i++) {
      if ($args[$i] -like '--prefix=*') {
        $prefix = $args[$i].Substring(9)
      } elseif ($args[$i] -eq '--prefix' -and $i + 1 -lt $args.Length) {
        $prefix = $args[$i + 1]
      }
    }

    New-Item -ItemType Directory -Force -Path (Join-Path $target 'dolt') | Out-Null
    Set-Content -Path (Join-Path $target 'config.yaml') -Value @("prefix: " + $prefix, "issue-prefix: " + $prefix + "-")
    exit 0
  }
  'config' {
    if ($args.Length -ge 3 -and $args[1] -eq 'get' -and $args[2] -eq 'status.custom') {
      Write-Output ''
    }
    if ($args.Length -ge 3 -and $args[1] -eq 'get' -and $args[2] -eq 'types.custom') {
      Write-Output 'agent,role,rig,convoy,slot,queue,event,message,molecule,gate,merge-request'
    }
    exit 0
  }
  'migrate' { exit 0 }
  default { exit 0 }
}
`
		cmdScript := "@echo off\r\npwsh -NoProfile -NoLogo -File \"" + psPath + "\" %*\r\n"
		if err := os.WriteFile(psPath, []byte(psScript), 0644); err != nil {
			t.Fatalf("write mock bd ps1: %v", err)
		}
		if err := os.WriteFile(filepath.Join(binDir, "bd.cmd"), []byte(cmdScript), 0644); err != nil {
			t.Fatalf("write mock bd cmd: %v", err)
		}
	} else {
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
  init)
    target="${BEADS_DIR:-$(pwd)/.beads}"
    prefix="gt"
    for arg in "$@"; do
      case "$arg" in
        --prefix=*) prefix="${arg#--prefix=}" ;;
      esac
    done
    mkdir -p "$target/dolt"
    printf 'prefix: %s\nissue-prefix: %s-\n' "$prefix" "$prefix" > "$target/config.yaml"
    exit 0
    ;;
  config)
    # Return types list for "config get types.custom" verification
    if echo "$*" | grep -q "get types.custom"; then
      echo "agent,role,rig,convoy,slot,queue,event,message,molecule,gate,merge-request"
    fi
    exit 0
    ;;
  migrate)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
		if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
			t.Fatalf("write mock bd: %v", err)
		}
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func readMockBDLog(t *testing.T, logPath string) string {
	t.Helper()

	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read mock bd log: %v", err)
	}
	return string(data)
}

func requireLogLineContaining(t *testing.T, logOutput, needle string) string {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(logOutput), "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	t.Fatalf("mock bd log missing %q:\n%s", needle, logOutput)
	return ""
}

func TestFindTownRoot(t *testing.T) {
	// Create a temporary town structure
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create nested directories
	deepDir := filepath.Join(tmpDir, "rig1", "crew", "worker1")
	if err := os.MkdirAll(deepDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a nested rig that was originally a standalone town
	// (has its own mayor/town.json inside the outer town)
	rigDir := filepath.Join(tmpDir, "myrig", "mayor", "rig")
	rigMayorDir := filepath.Join(rigDir, "mayor")
	if err := os.MkdirAll(rigMayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigMayorDir, "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		startDir string
		expected string
	}{
		{"from town root", tmpDir, tmpDir},
		{"from mayor dir", mayorDir, tmpDir},
		{"from deep nested dir", deepDir, tmpDir},
		{"from non-town dir", t.TempDir(), ""},
		{"nested town prefers outermost", rigBeadsDir, tmpDir},
		{"nested rig dir prefers outermost", rigDir, tmpDir},
	}

	// Add nested town test case: inner town inside outer town
	innerTown := filepath.Join(tmpDir, "imported", "gastown")
	if err := os.MkdirAll(filepath.Join(innerTown, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(innerTown, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	innerDeepDir := filepath.Join(innerTown, "crew", "worker2")
	if err := os.MkdirAll(innerDeepDir, 0755); err != nil {
		t.Fatal(err)
	}
	tests = append(tests, struct {
		name     string
		startDir string
		expected string
	}{"prefers outermost town root", innerDeepDir, tmpDir})

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := FindTownRoot(tc.startDir)
			if result != tc.expected {
				t.Errorf("FindTownRoot(%q) = %q, want %q", tc.startDir, result, tc.expected)
			}
		})
	}
}

func TestResolveRoutingTarget(t *testing.T) {
	// Create a temporary town with routes
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create mayor/town.json for FindTownRoot
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create routes.jsonl
	routesContent := `{"prefix": "gt-", "path": "gastown/mayor/rig"}
{"prefix": "hq-", "path": "."}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create the rig beads directory
	rigBeadsDir := filepath.Join(tmpDir, "gastown", "mayor", "rig", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	fallback := "/fallback/.beads"

	tests := []struct {
		name     string
		townRoot string
		beadID   string
		expected string
	}{
		{
			name:     "rig-level bead routes to rig",
			townRoot: tmpDir,
			beadID:   "gt-gastown-polecat-Toast",
			expected: rigBeadsDir,
		},
		{
			name:     "town-level bead routes to town",
			townRoot: tmpDir,
			beadID:   "hq-mayor",
			expected: beadsDir,
		},
		{
			name:     "unknown prefix falls back",
			townRoot: tmpDir,
			beadID:   "xx-unknown",
			expected: fallback,
		},
		{
			name:     "empty townRoot falls back",
			townRoot: "",
			beadID:   "gt-gastown-polecat-Toast",
			expected: fallback,
		},
		{
			name:     "no prefix falls back",
			townRoot: tmpDir,
			beadID:   "noprefixid",
			expected: fallback,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := ResolveRoutingTarget(tc.townRoot, tc.beadID, fallback)
			if result != tc.expected {
				t.Errorf("ResolveRoutingTarget(%q, %q, %q) = %q, want %q",
					tc.townRoot, tc.beadID, fallback, result, tc.expected)
			}
		})
	}
}

func TestEnsureCustomTypes(t *testing.T) {
	// Reset the in-memory cache before testing
	ResetEnsuredDirs()

	t.Run("empty beads dir returns error", func(t *testing.T) {
		err := EnsureCustomTypes("")
		if err == nil {
			t.Error("expected error for empty beads dir")
		}
	})

	t.Run("non-existent beads dir returns error", func(t *testing.T) {
		err := EnsureCustomTypes("/nonexistent/path/.beads")
		if err == nil {
			t.Error("expected error for non-existent beads dir")
		}
	})

	t.Run("sentinel file triggers cache hit", func(t *testing.T) {
		logPath := installMockBDRecorder(t)
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create sentinel file with current types list
		currentTypes := strings.Join(constants.BeadsCustomTypesList(), ",")
		sentinelPath := filepath.Join(beadsDir, typesSentinel)
		if err := os.WriteFile(sentinelPath, []byte(currentTypes+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		// Reset cache to ensure we're testing sentinel detection
		ResetEnsuredDirs()

		// This should succeed without running bd (sentinel matches)
		err := EnsureCustomTypes(beadsDir)
		if err != nil {
			t.Errorf("expected success with sentinel file, got: %v", err)
		}

		logOutput := readMockBDLog(t, logPath)
		if !strings.Contains(logOutput, "migrate --yes") {
			t.Fatalf("mock bd log %q missing schema migration before sentinel hit", logOutput)
		}
		if strings.Contains(logOutput, "config set types.custom") {
			t.Fatalf("matching sentinel should skip type reconfiguration:\n%s", logOutput)
		}
	})

	t.Run("stale sentinel triggers re-configuration", func(t *testing.T) {
		logPath := installMockBDRecorder(t)
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create sentinel file with old/legacy content (gt-zmy, gt-26f)
		sentinelPath := filepath.Join(beadsDir, typesSentinel)
		if err := os.WriteFile(sentinelPath, []byte("v1\n"), 0644); err != nil {
			t.Fatal(err)
		}

		ResetEnsuredDirs()

		err := EnsureCustomTypes(beadsDir)
		if err != nil {
			t.Fatalf("EnsureCustomTypes: %v", err)
		}

		if got := strings.TrimSpace(string(mustReadFile(t, sentinelPath))); got != strings.Join(constants.BeadsCustomTypesList(), ",") {
			t.Fatalf("types sentinel = %q, want current configured types", got)
		}

		logOutput := readMockBDLog(t, logPath)
		for _, want := range []string{"init", "config set types.custom"} {
			if !strings.Contains(logOutput, want) {
				t.Fatalf("mock bd log %q missing %q", logOutput, want)
			}
		}
	})

	t.Run("in-memory cache prevents repeated calls", func(t *testing.T) {
		logPath := installMockBDRecorder(t)
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create sentinel with current types to avoid bd call
		currentTypes := strings.Join(constants.BeadsCustomTypesList(), ",")
		sentinelPath := filepath.Join(beadsDir, typesSentinel)
		if err := os.WriteFile(sentinelPath, []byte(currentTypes+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		ResetEnsuredDirs()

		// First call
		if err := EnsureCustomTypes(beadsDir); err != nil {
			t.Fatal(err)
		}

		logBeforeSecondCall := readMockBDLog(t, logPath)

		// Remove sentinel - second call should still succeed due to in-memory cache
		os.Remove(sentinelPath)

		if err := EnsureCustomTypes(beadsDir); err != nil {
			t.Errorf("expected cache hit, got: %v", err)
		}
		if logAfterSecondCall := readMockBDLog(t, logPath); logAfterSecondCall != logBeforeSecondCall {
			t.Fatalf("in-memory cache should avoid additional bd calls:\nbefore:\n%s\nafter:\n%s", logBeforeSecondCall, logAfterSecondCall)
		}
	})
}

func TestEnsureCustomTypes_VerifyPersistence(t *testing.T) {
	t.Run("sentinel not written when db verify fails", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("test uses Unix shell script mock for bd")
		}
		// Install a mock bd that succeeds on "config set" but returns empty
		// on "config get types.custom" — simulating a silent write failure.
		binDir := t.TempDir()
		logPath := filepath.Join(binDir, "bd.log")
		script := `#!/bin/sh
LOG_FILE='` + logPath + `'
printf '%s\n' "$*" >> "$LOG_FILE"
cmd=""
for arg in "$@"; do
  case "$arg" in --*) ;; *) cmd="$arg"; break ;; esac
done
case "$cmd" in
  init)
    target="${BEADS_DIR:-$(pwd)/.beads}"
    mkdir -p "$target/dolt"
    printf 'prefix: gt\nissue-prefix: gt-\n' > "$target/config.yaml"
    exit 0
    ;;
  config)
    # "config set" succeeds but "config get types.custom" returns empty
    if echo "$*" | grep -q "get types.custom"; then
      echo ""
    fi
    exit 0
    ;;
  migrate) exit 0 ;;
  *) exit 0 ;;
esac
`
		if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
			t.Fatalf("write mock bd: %v", err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		ResetEnsuredDirs()

		err := EnsureCustomTypes(beadsDir)
		if err == nil {
			t.Fatal("expected error when types.custom verify fails, got nil")
		}
		if !strings.Contains(err.Error(), "not persisted") {
			t.Fatalf("expected 'not persisted' error, got: %v", err)
		}

		// Sentinel file should NOT have been written
		sentinelPath := filepath.Join(beadsDir, typesSentinel)
		if _, err := os.Stat(sentinelPath); !os.IsNotExist(err) {
			t.Error("sentinel file should not exist when verify fails")
		}
	})
}

func TestEnsureCustomTypesMigratesExistingServerDatabaseBeforeConfigWrite(t *testing.T) {
	logPath := installMockBDRecorder(t)

	townDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townDir, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townDir, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townDir, ".dolt-data", "testdb"), 0755); err != nil {
		t.Fatalf("mkdir db: %v", err)
	}

	beadsDir := filepath.Join(townDir, "testrig", ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	meta := `{"dolt_mode":"server","dolt_database":"testdb"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	ResetEnsuredDirs()
	if err := EnsureCustomTypes(beadsDir); err != nil {
		t.Fatalf("EnsureCustomTypes: %v", err)
	}

	logOutput := readMockBDLog(t, logPath)
	migrateIndex := strings.Index(logOutput, "migrate --yes")
	configIndex := strings.Index(logOutput, "config set types.custom")
	if migrateIndex == -1 {
		t.Fatalf("mock bd log missing schema migration:\n%s", logOutput)
	}
	if configIndex == -1 {
		t.Fatalf("mock bd log missing custom type config write:\n%s", logOutput)
	}
	if migrateIndex > configIndex {
		t.Fatalf("schema migration ran after config write:\n%s", logOutput)
	}
}

func TestEnsureSchemaMigratedUsesPinnedMetadataDatabase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock for bd")
	}

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	script := `#!/bin/sh
printf 'args=%s beads=%s db=%s data_dir=%s auto=%s readonly=%s\n' "$*" "${BEADS_DIR:-<unset>}" "${BEADS_DOLT_SERVER_DATABASE:-<unset>}" "${BEADS_DOLT_DATA_DIR:-<unset>}" "${BD_DOLT_AUTO_COMMIT:-<unset>}" "${BD_READONLY:-<unset>}" >> "$BD_LOG"
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_LOG", logPath)
	t.Setenv("BEADS_DIR", "/wrong")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "stale")
	t.Setenv("BEADS_DOLT_DATA_DIR", "/wrong/data")
	t.Setenv("BD_READONLY", "true")

	townDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townDir, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townDir, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}

	beadsDir := filepath.Join(townDir, "testrig", ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	meta := `{"dolt_mode":"server","dolt_database":"rigdb","dolt_server_host":"127.0.0.1","dolt_server_port":4407}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	ResetEnsuredDirs()
	if err := EnsureSchemaMigrated(beadsDir); err != nil {
		t.Fatalf("EnsureSchemaMigrated: %v", err)
	}
	if err := EnsureSchemaMigrated(beadsDir); err != nil {
		t.Fatalf("EnsureSchemaMigrated second call: %v", err)
	}

	logOutput := readMockBDLog(t, logPath)
	lines := strings.Split(strings.TrimSpace(logOutput), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected one cached migration call, got %d:\n%s", len(lines), logOutput)
	}
	for _, want := range []string{
		"args=migrate --yes",
		"beads=" + beadsDir,
		"db=rigdb",
		"data_dir=<unset>",
		"auto=on",
		"readonly=<unset>",
	} {
		if !strings.Contains(logOutput, want) {
			t.Fatalf("schema migration env log missing %q:\n%s", want, logOutput)
		}
	}
	if strings.Contains(logOutput, "stale") || strings.Contains(logOutput, "/wrong") || strings.Contains(logOutput, ".dolt-data") {
		t.Fatalf("stale inherited beads env leaked into migration:\n%s", logOutput)
	}
}

func TestEnsureSchemaMigratedDoesNotCreateServerModeDoltDiscoveryDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock for bd")
	}

	logPath := installMockBDRecorder(t)
	ResetEnsuredDirs()

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	meta := `{"dolt_mode":"server","dolt_database":"rigdb"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	if err := EnsureSchemaMigrated(beadsDir); err != nil {
		t.Fatalf("EnsureSchemaMigrated: %v", err)
	}

	if _, err := os.Stat(filepath.Join(beadsDir, "dolt")); !os.IsNotExist(err) {
		t.Fatalf("EnsureSchemaMigrated created local dolt discovery dir, stat err=%v", err)
	}
	if logOutput := readMockBDLog(t, logPath); !strings.Contains(logOutput, "migrate --yes") {
		t.Fatalf("mock bd log missing schema migration:\n%s", logOutput)
	}
}

func TestEnsureDatabaseInitializedUsesMutationEnvForRecoveryCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock for bd")
	}

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	script := `#!/bin/sh
printf 'args=%s beads=%s db=%s data_dir=%s auto=%s readonly=%s export=%s no_push=%s\n' "$*" "${BEADS_DIR:-<unset>}" "${BEADS_DOLT_SERVER_DATABASE:-<unset>}" "${BEADS_DOLT_DATA_DIR:-<unset>}" "${BD_DOLT_AUTO_COMMIT:-<unset>}" "${BD_READONLY:-<unset>}" "${BD_EXPORT_AUTO:-<unset>}" "${BD_NO_PUSH:-<unset>}" >> "$BD_LOG"

cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done

target="${BEADS_DIR:-$(pwd)/.beads}"
case "$cmd" in
  migrate)
    if [ -f "$target/.allow-migrate" ]; then
      exit 0
    fi
    echo "Error: unknown database 'rigdb'" >&2
    exit 1
    ;;
  init)
    mkdir -p "$target/dolt"
    touch "$target/.allow-migrate"
    exit 0
    ;;
  config)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_LOG", logPath)
	t.Setenv("BEADS_DIR", "/wrong")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "stale")
	t.Setenv("BEADS_DOLT_DATA_DIR", "/wrong/data")
	t.Setenv("BD_DOLT_AUTO_COMMIT", "off")
	t.Setenv("BD_READONLY", "true")
	t.Setenv("BD_EXPORT_AUTO", "true")
	t.Setenv("BD_NO_PUSH", "false")

	townDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townDir, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townDir, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	beadsDir := filepath.Join(townDir, "testrig", ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	meta := `{"dolt_mode":"server","dolt_database":"rigdb","dolt_server_host":"127.0.0.1","dolt_server_port":4407}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	ResetEnsuredDirs()
	if err := ensureDatabaseInitialized(beadsDir); err != nil {
		t.Fatalf("ensureDatabaseInitialized: %v", err)
	}

	logOutput := readMockBDLog(t, logPath)
	for _, line := range []string{
		requireLogLineContaining(t, logOutput, "args=init --prefix gt --server --force"),
		requireLogLineContaining(t, logOutput, "args=config set issue_prefix gt"),
	} {
		for _, want := range []string{
			"beads=" + beadsDir,
			"db=rigdb",
			"data_dir=<unset>",
			"auto=on",
			"readonly=<unset>",
			"export=false",
			"no_push=true",
		} {
			if !strings.Contains(line, want) {
				t.Fatalf("recovery mutation env log missing %q in line:\n%s\nfull log:\n%s", want, line, logOutput)
			}
		}
		if strings.Contains(line, "stale") || strings.Contains(line, "/wrong") {
			t.Fatalf("stale inherited env leaked into recovery mutation line:\n%s\nfull log:\n%s", line, logOutput)
		}
	}
}

func TestEnsureCustomStatuses(t *testing.T) {
	ResetEnsuredDirs()

	t.Run("empty beads dir returns error", func(t *testing.T) {
		err := EnsureCustomStatuses("")
		if err == nil {
			t.Error("expected error for empty beads dir")
		}
	})

	t.Run("non-existent beads dir returns error", func(t *testing.T) {
		err := EnsureCustomStatuses("/nonexistent/path/.beads")
		if err == nil {
			t.Error("expected error for non-existent beads dir")
		}
	})

	t.Run("sentinel file triggers cache hit", func(t *testing.T) {
		logPath := installMockBDRecorder(t)
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create sentinel file with current statuses list
		currentStatuses := strings.Join(constants.BeadsCustomStatusesList(), ",")
		sentinelPath := filepath.Join(beadsDir, statusesSentinel)
		if err := os.WriteFile(sentinelPath, []byte(currentStatuses+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		ResetEnsuredDirs()

		// This should succeed without running bd (sentinel matches)
		err := EnsureCustomStatuses(beadsDir)
		if err != nil {
			t.Errorf("expected success with sentinel file, got: %v", err)
		}

		logOutput := readMockBDLog(t, logPath)
		if !strings.Contains(logOutput, "migrate --yes") {
			t.Fatalf("mock bd log %q missing schema migration before sentinel hit", logOutput)
		}
		if strings.Contains(logOutput, "config set status.custom") {
			t.Fatalf("matching sentinel should skip status reconfiguration:\n%s", logOutput)
		}
	})

	t.Run("stale sentinel triggers re-configuration", func(t *testing.T) {
		logPath := installMockBDRecorder(t)
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create sentinel file with old/stale content
		sentinelPath := filepath.Join(beadsDir, statusesSentinel)
		if err := os.WriteFile(sentinelPath, []byte("old_status\n"), 0644); err != nil {
			t.Fatal(err)
		}

		ResetEnsuredDirs()

		cacheKey := beadsDir + ":statuses"
		err := EnsureCustomStatuses(beadsDir)
		if err != nil {
			t.Fatalf("EnsureCustomStatuses: %v", err)
		}

		if !ensuredDirs[cacheKey] {
			t.Fatal("expected successful reconfiguration to populate statuses cache")
		}
		if got := strings.TrimSpace(string(mustReadFile(t, sentinelPath))); got != strings.Join(constants.BeadsCustomStatusesList(), ",") {
			t.Fatalf("statuses sentinel = %q, want current configured statuses", got)
		}

		logOutput := readMockBDLog(t, logPath)
		for _, want := range []string{"init", "config get status.custom", "config set status.custom"} {
			if !strings.Contains(logOutput, want) {
				t.Fatalf("mock bd log %q missing %q", logOutput, want)
			}
		}
	})

	t.Run("in-memory cache prevents repeated calls", func(t *testing.T) {
		logPath := installMockBDRecorder(t)
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create sentinel with current statuses to avoid bd call
		currentStatuses := strings.Join(constants.BeadsCustomStatusesList(), ",")
		sentinelPath := filepath.Join(beadsDir, statusesSentinel)
		if err := os.WriteFile(sentinelPath, []byte(currentStatuses+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		ResetEnsuredDirs()

		// First call
		if err := EnsureCustomStatuses(beadsDir); err != nil {
			t.Fatal(err)
		}

		logBeforeSecondCall := readMockBDLog(t, logPath)

		// Remove sentinel - second call should still succeed due to in-memory cache
		os.Remove(sentinelPath)

		if err := EnsureCustomStatuses(beadsDir); err != nil {
			t.Errorf("expected cache hit, got: %v", err)
		}
		if logAfterSecondCall := readMockBDLog(t, logPath); logAfterSecondCall != logBeforeSecondCall {
			t.Fatalf("in-memory cache should avoid additional bd calls:\nbefore:\n%s\nafter:\n%s", logBeforeSecondCall, logAfterSecondCall)
		}
	})

	t.Run("cache key does not collide with types cache", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		ResetEnsuredDirs()

		// Manually set the types cache entry (simulating EnsureCustomTypes ran)
		ensuredMu.Lock()
		ensuredDirs[beadsDir] = true
		ensuredMu.Unlock()

		// Statuses cache should NOT be hit — different key
		cacheKey := beadsDir + ":statuses"
		ensuredMu.Lock()
		cached := ensuredDirs[cacheKey]
		ensuredMu.Unlock()

		if cached {
			t.Error("statuses cache key should not collide with types cache key")
		}
	})

	// Regression for gt-kbi: when `bd config get status.custom` returns the
	// unset sentinel "status.custom (not set)", EnsureCustomStatuses must NOT
	// merge that literal string into the value passed to `bd config set` —
	// bd rejects it via the [a-z][a-z0-9_-]* validator, breaking gt convoy.
	t.Run("unset sentinel from bd config get is filtered (gt-kbi)", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("test uses Unix shell script mock for bd")
		}

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
  init)
    target="${BEADS_DIR:-$(pwd)/.beads}"
    mkdir -p "$target/dolt"
    printf 'prefix: gt\nissue-prefix: gt-\n' > "$target/config.yaml"
    exit 0
    ;;
  config)
    # Simulate the real bd behaviour for an unset key: stdout carries the
    # "<key> (not set)" sentinel and exit status is 0.
    if echo "$*" | grep -q "get status.custom"; then
      echo "status.custom (not set)"
    fi
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
		if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
			t.Fatalf("write mock bd: %v", err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0755); err != nil {
			t.Fatal(err)
		}
		ResetEnsuredDirs()

		if err := EnsureCustomStatuses(beadsDir); err != nil {
			t.Fatalf("EnsureCustomStatuses: %v", err)
		}

		logOutput := readMockBDLog(t, logPath)
		if strings.Contains(logOutput, "(not set)") {
			t.Fatalf("bd config set received the unset sentinel as a status value:\n%s", logOutput)
		}

		// Verify the set call carries exactly the canonical statuses list.
		want := "config set status.custom " + strings.Join(constants.BeadsCustomStatusesList(), ",")
		if !strings.Contains(logOutput, want) {
			t.Fatalf("bd config set missing canonical statuses\nwant substring: %q\ngot:\n%s", want, logOutput)
		}
	})
}

func TestEnsureCustomStatusesUsesMutationEnvForConfigSet(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock for bd")
	}

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	script := `#!/bin/sh
printf 'args=%s beads=%s auto=%s readonly=%s export=%s no_push=%s\n' "$*" "${BEADS_DIR:-<unset>}" "${BD_DOLT_AUTO_COMMIT:-<unset>}" "${BD_READONLY:-<unset>}" "${BD_EXPORT_AUTO:-<unset>}" "${BD_NO_PUSH:-<unset>}" >> "$BD_LOG"

cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done

case "$cmd" in
  config)
    if echo "$*" | grep -q "get status.custom"; then
      echo ""
    fi
    exit 0
    ;;
  migrate)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_LOG", logPath)
	t.Setenv("BEADS_DIR", "/wrong")
	t.Setenv("BD_DOLT_AUTO_COMMIT", "off")
	t.Setenv("BD_READONLY", "true")
	t.Setenv("BD_EXPORT_AUTO", "true")
	t.Setenv("BD_NO_PUSH", "false")

	townDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townDir, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townDir, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	beadsDir := filepath.Join(townDir, "testrig", ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0755); err != nil {
		t.Fatalf("mkdir beads dolt: %v", err)
	}

	ResetEnsuredDirs()
	if err := EnsureCustomStatuses(beadsDir); err != nil {
		t.Fatalf("EnsureCustomStatuses: %v", err)
	}

	logOutput := readMockBDLog(t, logPath)
	line := requireLogLineContaining(t, logOutput, "args=config set status.custom")
	for _, want := range []string{
		"beads=" + beadsDir,
		"auto=on",
		"readonly=<unset>",
		"export=false",
		"no_push=true",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("status mutation env log missing %q in line:\n%s\nfull log:\n%s", want, line, logOutput)
		}
	}
	if strings.Contains(line, "/wrong") {
		t.Fatalf("stale inherited env leaked into status mutation line:\n%s\nfull log:\n%s", line, logOutput)
	}
}

func TestParseConfigOutput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"only whitespace", "  \n  \n", ""},
		{"plain value", "agent,role,rig\n", "agent,role,rig"},
		{"value after Note prefix", "Note: background sync off\nagent,role\n", "agent,role"},
		{"value after multiple Note prefixes", "Note: a\nNote: b\nagent\n", "agent"},
		{"unset sentinel filtered", "status.custom (not set)\n", ""},
		{"unset sentinel followed by value", "status.custom (not set)\nstaged_ready\n", "staged_ready"},
		{"Note prefix is case-sensitive", "note: lower-case\n", "note: lower-case"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseConfigOutput([]byte(tt.input)); got != tt.want {
				t.Errorf("ParseConfigOutput(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestEnsureDatabaseInitialized(t *testing.T) {
	t.Run("dolt dir exists — migrate without init", func(t *testing.T) {
		logPath := installMockBDRecorder(t)
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0755)

		err := ensureDatabaseInitialized(beadsDir)
		if err != nil {
			t.Errorf("expected nil error when dolt/ exists, got: %v", err)
		}
		logOutput := readMockBDLog(t, logPath)
		if strings.Contains(logOutput, "init") {
			t.Fatalf("mock bd log %q unexpectedly ran init", logOutput)
		}
		if !strings.Contains(logOutput, "migrate --yes") {
			t.Fatalf("mock bd log %q missing schema migration", logOutput)
		}
	})

	t.Run("metadata.json with valid db — migrate without init (server mode)", func(t *testing.T) {
		logPath := installMockBDRecorder(t)
		townDir := t.TempDir()
		mayorDir := filepath.Join(townDir, "mayor")
		os.MkdirAll(mayorDir, 0755)
		os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644)

		rigDir := filepath.Join(townDir, "testrig")
		beadsDir := filepath.Join(rigDir, ".beads")
		os.MkdirAll(beadsDir, 0755)

		// Create the referenced database in .dolt-data/
		os.MkdirAll(filepath.Join(townDir, ".dolt-data", "testdb"), 0755)

		meta := `{"dolt_mode":"server","dolt_database":"testdb"}`
		os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0644)

		err := ensureDatabaseInitialized(beadsDir)
		if err != nil {
			t.Errorf("expected nil error when metadata.json + .dolt-data/<db> exist, got: %v", err)
		}
		logOutput := readMockBDLog(t, logPath)
		if strings.Contains(logOutput, "init") {
			t.Fatalf("mock bd log %q unexpectedly ran init", logOutput)
		}
		if !strings.Contains(logOutput, "migrate --yes") {
			t.Fatalf("mock bd log %q missing schema migration", logOutput)
		}
	})

	t.Run("server metadata without local data dir — migrate without init", func(t *testing.T) {
		logPath := installMockBDRecorder(t)
		townDir := t.TempDir()
		mayorDir := filepath.Join(townDir, "mayor")
		os.MkdirAll(mayorDir, 0755)
		os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644)

		rigDir := filepath.Join(townDir, "testrig")
		beadsDir := filepath.Join(rigDir, ".beads")
		os.MkdirAll(beadsDir, 0755)

		meta := `{"dolt_mode":"server","dolt_database":"external_db"}`
		os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0644)

		if err := ensureDatabaseInitialized(beadsDir); err != nil {
			t.Fatalf("ensureDatabaseInitialized: %v", err)
		}

		logOutput := readMockBDLog(t, logPath)
		if strings.Contains(logOutput, "init") {
			t.Fatalf("mock bd log %q unexpectedly ran init", logOutput)
		}
		if !strings.Contains(logOutput, "migrate --yes") {
			t.Fatalf("mock bd log %q missing schema migration", logOutput)
		}
		if got := string(mustReadFile(t, filepath.Join(beadsDir, "metadata.json"))); got != meta {
			t.Fatalf("metadata.json changed: got %s want %s", got, meta)
		}
	})

	t.Run("server metadata with missing database — init self heals", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("test uses Unix shell script mock for bd")
		}

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

target="${BEADS_DIR:-$(pwd)/.beads}"
case "$cmd" in
  migrate)
    if [ -f "$target/.allow-migrate" ]; then
      exit 0
    fi
    echo "Error: unknown database 'testdb'" >&2
    exit 1
    ;;
  init)
    mkdir -p "$target/dolt"
    touch "$target/.allow-migrate"
    exit 0
    ;;
  config)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
		if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
			t.Fatalf("write mock bd: %v", err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		ResetEnsuredDirs()

		townDir := t.TempDir()
		mayorDir := filepath.Join(townDir, "mayor")
		os.MkdirAll(mayorDir, 0755)
		os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644)

		rigDir := filepath.Join(townDir, "testrig")
		beadsDir := filepath.Join(rigDir, ".beads")
		os.MkdirAll(beadsDir, 0755)
		meta := `{"dolt_mode":"server","dolt_database":"testdb"}`
		os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0644)

		if err := ensureDatabaseInitialized(beadsDir); err != nil {
			t.Fatalf("ensureDatabaseInitialized: %v", err)
		}

		logOutput := readMockBDLog(t, logPath)
		firstMigrate := strings.Index(logOutput, "migrate --yes")
		initCall := strings.Index(logOutput, "init --prefix gt --server")
		lastMigrate := strings.LastIndex(logOutput, "migrate --yes")
		if firstMigrate == -1 || initCall == -1 || lastMigrate == firstMigrate {
			t.Fatalf("mock bd log missing migrate/init/migrate recovery sequence:\n%s", logOutput)
		}
		if !(firstMigrate < initCall && initCall < lastMigrate) {
			t.Fatalf("mock bd log has wrong recovery order:\n%s", logOutput)
		}
	})

	t.Run("server metadata with sync remote bootstraps without discard flags", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("test uses Unix shell script mock for bd")
		}

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

target="${BEADS_DIR:-$(pwd)/.beads}"
case "$cmd" in
  migrate)
    if [ -f "$target/.allow-migrate" ]; then
      exit 0
    fi
    echo "Error: unknown database 'testdb'" >&2
    exit 1
    ;;
  init)
    echo "init should not run for configured sync.remote" >&2
    exit 10
    ;;
  bootstrap)
    args=" $*"
    case "$args" in
      *" --yes"*) ;;
      *) echo "missing bootstrap --yes" >&2; exit 11 ;;
    esac
    for forbidden in " --force" " --reinit-local" " --discard-remote" " --destroy-token="; do
      case "$args" in
        *"$forbidden"*) echo "unexpected destructive flag $forbidden" >&2; exit 12 ;;
      esac
    done
    touch "$target/.allow-migrate"
    exit 0
    ;;
  config)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
		if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
			t.Fatalf("write mock bd: %v", err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		ResetEnsuredDirs()

		townDir := t.TempDir()
		mayorDir := filepath.Join(townDir, "mayor")
		os.MkdirAll(mayorDir, 0755)
		os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644)

		rigDir := filepath.Join(townDir, "testrig")
		beadsDir := filepath.Join(rigDir, ".beads")
		os.MkdirAll(beadsDir, 0755)
		meta := `{"dolt_mode":"server","dolt_database":"testdb"}`
		os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0644)
		configYAML := "prefix: gt\nsync.remote: \"git+https://github.com/example/repo.git\"\n"
		os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configYAML), 0644)

		if err := ensureDatabaseInitialized(beadsDir); err != nil {
			t.Fatalf("ensureDatabaseInitialized: %v", err)
		}

		logOutput := readMockBDLog(t, logPath)
		requireLogLineContaining(t, logOutput, "bootstrap --yes")
		for _, forbidden := range []string{"--force", "--reinit-local", "--discard-remote", "--destroy-token="} {
			if strings.Contains(logOutput, forbidden) {
				t.Fatalf("mock bd log contains destructive flag %q:\n%s", forbidden, logOutput)
			}
		}
	})

	t.Run("server metadata with existing local database does not force init after transient unknown database", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("test uses Unix shell script mock for bd")
		}

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
  migrate)
    echo "Error: unknown database 'testdb'" >&2
    exit 1
    ;;
  init)
    echo "init should not run" >&2
    exit 10
    ;;
  *)
    exit 0
    ;;
esac
`
		if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
			t.Fatalf("write mock bd: %v", err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		ResetEnsuredDirs()

		townDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(townDir, "mayor"), 0755); err != nil {
			t.Fatalf("mkdir mayor: %v", err)
		}
		if err := os.WriteFile(filepath.Join(townDir, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
			t.Fatalf("write town.json: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(townDir, ".dolt-data", "testdb", ".dolt"), 0755); err != nil {
			t.Fatalf("mkdir local database: %v", err)
		}

		rigDir := filepath.Join(townDir, "testrig")
		beadsDir := filepath.Join(rigDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("mkdir beads: %v", err)
		}
		meta := `{"dolt_mode":"server","dolt_database":"testdb"}`
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0644); err != nil {
			t.Fatalf("write metadata: %v", err)
		}
		configYAML := "prefix: gt\nsync.remote: \"git+https://github.com/example/repo.git\"\n"
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configYAML), 0644); err != nil {
			t.Fatalf("write config.yaml: %v", err)
		}

		err := ensureDatabaseInitialized(beadsDir)
		if err == nil {
			t.Fatal("expected migration error when server reports unknown database despite local database existing")
		}
		if !strings.Contains(err.Error(), "unknown database") {
			t.Fatalf("ensureDatabaseInitialized error = %v, want unknown database", err)
		}

		logOutput := readMockBDLog(t, logPath)
		if strings.Contains(logOutput, "init") {
			t.Fatalf("forced init ran despite existing local database:\n%s", logOutput)
		}
	})

	t.Run("server metadata with failed forced init retries recovery", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("test uses Unix shell script mock for bd")
		}

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

target="${BEADS_DIR:-$(pwd)/.beads}"
case "$cmd" in
  migrate)
    if [ -f "$target/.allow-migrate" ]; then
      exit 0
    fi
    echo "Error: unknown database 'testdb'" >&2
    exit 1
    ;;
  init)
    mkdir -p "$target/dolt"
    if [ ! -f "$target/.init-failed" ]; then
      touch "$target/.init-failed"
      echo "transient init failure" >&2
      exit 1
    fi
    touch "$target/.allow-migrate"
    exit 0
    ;;
  config)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
		if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
			t.Fatalf("write mock bd: %v", err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		ResetEnsuredDirs()

		townDir := t.TempDir()
		mayorDir := filepath.Join(townDir, "mayor")
		os.MkdirAll(mayorDir, 0755)
		os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644)

		rigDir := filepath.Join(townDir, "testrig")
		beadsDir := filepath.Join(rigDir, ".beads")
		os.MkdirAll(beadsDir, 0755)
		meta := `{"dolt_mode":"server","dolt_database":"testdb"}`
		os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0644)

		if err := ensureDatabaseInitialized(beadsDir); err == nil {
			t.Fatalf("expected transient forced init failure")
		}
		if _, err := os.Stat(filepath.Join(beadsDir, "dolt")); err != nil {
			t.Fatalf("expected failed init path to leave dolt discovery dir: %v", err)
		}

		if err := ensureDatabaseInitialized(beadsDir); err != nil {
			t.Fatalf("second ensureDatabaseInitialized: %v", err)
		}

		logOutput := readMockBDLog(t, logPath)
		if got := strings.Count(logOutput, "init --prefix gt --server --force"); got != 2 {
			t.Fatalf("expected forced init to be retried twice, got %d:\n%s", got, logOutput)
		}
	})

	t.Run("no database artifacts — attempts bd init", func(t *testing.T) {
		logPath := installMockBDRecorder(t)
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		os.MkdirAll(beadsDir, 0755)

		if err := ensureDatabaseInitialized(beadsDir); err != nil {
			t.Fatalf("ensureDatabaseInitialized: %v", err)
		}

		logOutput := readMockBDLog(t, logPath)
		for _, want := range []string{"init --prefix gt --server", "config set issue_prefix", "migrate --yes"} {
			if !strings.Contains(logOutput, want) {
				t.Fatalf("mock bd log %q missing %q", logOutput, want)
			}
		}
	})
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func TestIsMissingServerDatabaseMigrationError(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		{
			name: "unknown database",
			msg:  "Error: unknown database 'rigdb'",
			want: true,
		},
		{
			name: "database name not found",
			msg:  "database rigdb not found",
			want: true,
		},
		{
			name: "quoted database name does not exist",
			msg:  `database "rig-db" does not exist`,
			want: true,
		},
		{
			name: "migration artifact not found",
			msg:  "database migration file not found",
			want: false,
		},
		{
			name: "unrelated missing table",
			msg:  "schema table not found",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMissingServerDatabaseMigrationError(errors.New(tt.msg)); got != tt.want {
				t.Fatalf("isMissingServerDatabaseMigrationError(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}

func TestRestoreTrackedConfigYAMLRestoresDeletedSnapshot(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	snapshot := &TrackedConfigSnapshot{
		path: configPath,
		data: []byte("prefix: keep\nissue-prefix: keep\n"),
		mode: 0600,
	}

	if err := restoreTrackedConfigYAML(snapshot); err != nil {
		t.Fatalf("restoreTrackedConfigYAML: %v", err)
	}
	got := mustReadFile(t, configPath)
	if string(got) != string(snapshot.data) {
		t.Fatalf("restored config.yaml = %q, want %q", string(got), string(snapshot.data))
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat restored config.yaml: %v", err)
	}
	if info.Mode().Perm() != snapshot.mode {
		t.Fatalf("restored mode = %v, want %v", info.Mode().Perm(), snapshot.mode)
	}
}

func TestRestoreTrackedConfigYAMLRestoresModeDrift(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("prefix: keep\nissue-prefix: keep\n")
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	snapshot := &TrackedConfigSnapshot{
		path: configPath,
		data: data,
		mode: 0600,
	}

	if err := restoreTrackedConfigYAML(snapshot); err != nil {
		t.Fatalf("restoreTrackedConfigYAML: %v", err)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat config.yaml: %v", err)
	}
	if info.Mode().Perm() != snapshot.mode {
		t.Fatalf("restored mode = %v, want %v", info.Mode().Perm(), snapshot.mode)
	}
}

func TestDetectPrefix(t *testing.T) {
	t.Run("config.yaml unquoted prefix", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		os.MkdirAll(beadsDir, 0755)
		os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("issue-prefix: myrig-\n"), 0644)

		got := detectPrefix(beadsDir)
		if got != "myrig" {
			t.Errorf("detectPrefix() = %q, want %q", got, "myrig")
		}
	})

	t.Run("config.yaml double-quoted prefix", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		os.MkdirAll(beadsDir, 0755)
		os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("prefix: \"myrig\"\n"), 0644)

		got := detectPrefix(beadsDir)
		if got != "myrig" {
			t.Errorf("detectPrefix() = %q, want %q", got, "myrig")
		}
	})

	t.Run("config.yaml single-quoted prefix", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		os.MkdirAll(beadsDir, 0755)
		os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("prefix: 'myrig'\n"), 0644)

		got := detectPrefix(beadsDir)
		if got != "myrig" {
			t.Errorf("detectPrefix() = %q, want %q", got, "myrig")
		}
	})

	t.Run("config.yaml double-quoted prefix with trailing dash", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		os.MkdirAll(beadsDir, 0755)
		os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("prefix: \"myrig-\"\n"), 0644)

		got := detectPrefix(beadsDir)
		if got != "myrig" {
			t.Errorf("detectPrefix() = %q, want %q (quotes stripped before dash trim)", got, "myrig")
		}
	})

	t.Run("config.yaml single-quoted prefix with trailing dash", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		os.MkdirAll(beadsDir, 0755)
		os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("prefix: 'myrig-'\n"), 0644)

		got := detectPrefix(beadsDir)
		if got != "myrig" {
			t.Errorf("detectPrefix() = %q, want %q (quotes stripped before dash trim)", got, "myrig")
		}
	})

	t.Run("config.yaml invalid prefix falls through to default", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		os.MkdirAll(beadsDir, 0755)
		os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("prefix: 123-invalid\n"), 0644)

		got := detectPrefix(beadsDir)
		if got != "gt" {
			t.Errorf("detectPrefix() = %q, want %q", got, "gt")
		}
	})

	t.Run("no config.yaml falls through to default", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		os.MkdirAll(beadsDir, 0755)

		got := detectPrefix(beadsDir)
		if got != "gt" {
			t.Errorf("detectPrefix() = %q, want %q", got, "gt")
		}
	})

	t.Run("rigs.json authoritative source", func(t *testing.T) {
		// Create town structure with rigs.json
		townDir := t.TempDir()
		mayorDir := filepath.Join(townDir, "mayor")
		os.MkdirAll(mayorDir, 0755)
		os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644)

		if err := config.SaveRigsConfig(filepath.Join(mayorDir, "rigs.json"), &config.RigsConfig{
			Version: config.CurrentRigsVersion,
			Rigs: map[string]config.RigEntry{
				"testrig": {
					GitURL:  "git@example.com:testrig.git",
					AddedAt: time.Now(),
					BeadsConfig: &config.BeadsConfig{
						Prefix: "tr-",
					},
				},
			},
		}); err != nil {
			t.Fatalf("SaveRigsConfig: %v", err)
		}

		// Create rig directory with .beads
		rigDir := filepath.Join(townDir, "testrig")
		beadsDir := filepath.Join(rigDir, ".beads")
		os.MkdirAll(beadsDir, 0755)

		got := detectPrefix(beadsDir)
		if got != "tr" {
			t.Errorf("detectPrefix() = %q, want %q", got, "tr")
		}
	})

	t.Run("non-routed rigs.json beats conflicting config.yaml", func(t *testing.T) {
		townDir := t.TempDir()
		mayorDir := filepath.Join(townDir, "mayor")
		os.MkdirAll(mayorDir, 0755)
		os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644)
		if err := config.SaveRigsConfig(filepath.Join(mayorDir, "rigs.json"), &config.RigsConfig{
			Version: config.CurrentRigsVersion,
			Rigs: map[string]config.RigEntry{
				"testrig": {
					GitURL:  "git@example.com:testrig.git",
					AddedAt: time.Now(),
					BeadsConfig: &config.BeadsConfig{
						Prefix: "tr-",
					},
				},
			},
		}); err != nil {
			t.Fatalf("SaveRigsConfig: %v", err)
		}

		beadsDir := filepath.Join(townDir, "testrig", ".beads")
		os.MkdirAll(beadsDir, 0755)
		os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("prefix: local\nissue-prefix: local\n"), 0644)

		if got := detectPrefix(beadsDir); got != "tr" {
			t.Errorf("detectPrefix() with conflicting non-routed config.yaml = %q, want %q", got, "tr")
		}
	})

	t.Run("routed path falls back to default", func(t *testing.T) {
		// Routed beads path: mayor/rig/.beads — filepath.Base(filepath.Dir)
		// yields "rig", not the actual rig name. Should fall back to "gt".
		townDir := t.TempDir()
		mayorDir := filepath.Join(townDir, "mayor")
		os.MkdirAll(mayorDir, 0755)
		os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644)

		rigDir := filepath.Join(townDir, "myrig")
		routedDir := filepath.Join(rigDir, "mayor", "rig")
		beadsDir := filepath.Join(routedDir, ".beads")
		os.MkdirAll(beadsDir, 0755)

		got := detectPrefix(beadsDir)
		// "rig" won't be found in rigs.json → falls to "gt" default
		if got != "gt" {
			t.Errorf("detectPrefix() for routed path = %q, want %q", got, "gt")
		}
	})

	t.Run("routed path prefers local config.yaml", func(t *testing.T) {
		townDir := t.TempDir()
		mayorDir := filepath.Join(townDir, "mayor")
		os.MkdirAll(mayorDir, 0755)
		os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644)

		beadsDir := filepath.Join(townDir, "myrig", "mayor", "rig", ".beads")
		os.MkdirAll(beadsDir, 0755)
		os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("prefix: custom\nissue-prefix: custom\n"), 0644)

		if got := detectPrefix(beadsDir); got != "custom" {
			t.Errorf("detectPrefix() for routed path with config.yaml = %q, want %q", got, "custom")
		}
	})
}

func TestStripYAMLQuotes(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{`"myrig"`, "myrig"},
		{`'myrig'`, "myrig"},
		{"myrig", "myrig"},
		{`""`, ""},
		{`"a"`, "a"},
		{`"`, `"`},
		{"", ""},
	}
	for _, tc := range tests {
		got := stripYAMLQuotes(tc.input)
		if got != tc.expected {
			t.Errorf("stripYAMLQuotes(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestBeads_getTownRoot(t *testing.T) {
	// Create a temporary town
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create nested directory
	rigDir := filepath.Join(tmpDir, "myrig", "mayor", "rig")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}

	b := New(rigDir)

	// First call should find town root
	root1 := b.getTownRoot()
	if root1 != tmpDir {
		t.Errorf("first getTownRoot() = %q, want %q", root1, tmpDir)
	}

	// Second call should return cached value
	root2 := b.getTownRoot()
	if root2 != root1 {
		t.Errorf("second getTownRoot() = %q, want cached %q", root2, root1)
	}

	// Verify caching works (sync.Once ensures single execution)
	if b.townRoot != tmpDir {
		t.Errorf("expected townRoot to be cached as %q, got %q", tmpDir, b.townRoot)
	}
}
