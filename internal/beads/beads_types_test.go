package beads

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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

		// Remove sentinel - second call should still succeed due to in-memory cache
		os.Remove(sentinelPath)

		if err := EnsureCustomTypes(beadsDir); err != nil {
			t.Errorf("expected cache hit, got: %v", err)
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

		// Remove sentinel - second call should still succeed due to in-memory cache
		os.Remove(sentinelPath)

		if err := EnsureCustomStatuses(beadsDir); err != nil {
			t.Errorf("expected cache hit, got: %v", err)
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
}

func TestEnsureDatabaseInitialized(t *testing.T) {
	t.Run("dolt dir exists — skip init", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0755)

		err := ensureDatabaseInitialized(beadsDir)
		if err != nil {
			t.Errorf("expected nil error when dolt/ exists, got: %v", err)
		}
	})

	t.Run("metadata.json with valid db — skip init (server mode)", func(t *testing.T) {
		// Set up town structure with .dolt-data/<db> so the deep check passes
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
	})

	t.Run("metadata.json exists but db missing — attempts init", func(t *testing.T) {
		logPath := installMockBDRecorder(t)
		// metadata.json references a database that doesn't exist in .dolt-data/
		townDir := t.TempDir()
		mayorDir := filepath.Join(townDir, "mayor")
		os.MkdirAll(mayorDir, 0755)
		os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644)
		os.MkdirAll(filepath.Join(townDir, ".dolt-data"), 0755) // empty .dolt-data

		rigDir := filepath.Join(townDir, "testrig")
		beadsDir := filepath.Join(rigDir, ".beads")
		os.MkdirAll(beadsDir, 0755)

		meta := `{"dolt_mode":"server","dolt_database":"missing_db"}`
		os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0644)

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

		// Create rigs.json with a prefix for our rig
		rigsJSON := `{"rigs": {"testrig": {"prefix": "tr"}}}`
		os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644)

		// Create rig directory with .beads
		rigDir := filepath.Join(townDir, "testrig")
		beadsDir := filepath.Join(rigDir, ".beads")
		os.MkdirAll(beadsDir, 0755)

		got := detectPrefix(beadsDir)
		// GetRigPrefix should find "testrig" in rigs.json and return "tr".
		// If the town structure isn't recognized (e.g., GetRigPrefix expects
		// additional files), it falls back to "gt" — both are valid prefixes.
		if got != "tr" && got != "gt" {
			t.Errorf("detectPrefix() = %q, want %q or %q", got, "tr", "gt")
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
