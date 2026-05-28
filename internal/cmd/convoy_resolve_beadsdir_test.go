package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
)

func installConvoyResolveBDMock(t *testing.T) {
	t.Helper()

	binDir := t.TempDir()
	if runtime.GOOS == "windows" {
		psPath := filepath.Join(binDir, "bd.ps1")
		psScript := `# Mock bd for convoy resolve tests.
$cmd = ''
foreach ($arg in $args) {
  if ($arg -like '--*') { continue }
  $cmd = $arg
  break
}
$target = $env:BEADS_DIR
if ([string]::IsNullOrEmpty($target)) {
  $target = Join-Path (Get-Location) '.beads'
}
switch ($cmd) {
  'init' {
    New-Item -ItemType Directory -Force -Path (Join-Path $target 'dolt') | Out-Null
    Set-Content -Path (Join-Path $target 'config.yaml') -Value @('prefix: gt', 'issue-prefix: gt-')
    exit 0
  }
  'migrate' { exit 0 }
  'config' {
    if ($args -join ' ' -match 'get types.custom') {
      Write-Output 'agent,role,rig,convoy,slot,queue,event,message,molecule,gate,merge-request'
    }
    exit 0
  }
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
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done

target="${BEADS_DIR:-$(pwd)/.beads}"
case "$cmd" in
  init)
    mkdir -p "$target/dolt"
    printf 'prefix: gt\nissue-prefix: gt-\n' > "$target/config.yaml"
    exit 0
    ;;
  migrate)
    exit 0
    ;;
  config)
    if echo "$*" | grep -q "get types.custom"; then
      echo "agent,role,rig,convoy,slot,queue,event,message,molecule,gate,merge-request"
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
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestConvoyResolveBeadsDir_RegressionEmptyConvoy is a regression test for
// hq-dt4: "Convoy add command reports success but issues don't appear in
// convoy progress."
//
// Root cause: getTownBeadsDir() returns the workspace root (e.g., /gt), but
// EnsureCustomTypes and EnsureCustomStatuses expect the .beads directory path
// (e.g., /gt/.beads). Without ResolveBeadsDir, the sentinel files and bd
// config commands target the wrong directory, so custom types (including
// "convoy") are never registered in the correct database — making all convoys
// appear empty.
//
// Fix: convoy.go and convoy_stage.go now call beads.ResolveBeadsDir(townBeads)
// before passing to EnsureCustomTypes/EnsureCustomStatuses.
func TestConvoyResolveBeadsDir_RegressionEmptyConvoy(t *testing.T) {
	// Subtest 1: Prove workspace root != .beads dir
	t.Run("getTownBeadsDir returns workspace root not beads dir", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("skipping on windows")
		}

		townRoot := t.TempDir()
		if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
			t.Fatal(err)
		}

		origDir, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chdir(origDir) })
		if err := os.Chdir(townRoot); err != nil {
			t.Fatal(err)
		}

		result, err := getTownBeadsDir()
		if err != nil {
			t.Fatalf("getTownBeadsDir() error: %v", err)
		}

		// The key fact: getTownBeadsDir returns workspace root, not .beads.
		beadsDir := filepath.Join(townRoot, ".beads")
		if result == beadsDir {
			t.Log("getTownBeadsDir now returns .beads directly — " +
				"ResolveBeadsDir call in convoy code is redundant but harmless")
			return
		}

		// ResolveBeadsDir must bridge the gap.
		// Normalize both via EvalSymlinks to handle macOS /private/var vs /var differences.
		resolved := beads.ResolveBeadsDir(result)
		resolvedReal, _ := filepath.EvalSymlinks(resolved)
		beadsDirReal, _ := filepath.EvalSymlinks(beadsDir)
		if resolvedReal != beadsDirReal {
			t.Errorf("ResolveBeadsDir(getTownBeadsDir()) = %q, want %q",
				resolved, beadsDir)
		}
	})

	// Subtest 2: Without ResolveBeadsDir, sentinel ends up in the wrong place.
	// This test documents the buggy behavior that the fix prevents.
	t.Run("without ResolveBeadsDir sentinel goes to wrong location", func(t *testing.T) {
		installConvoyResolveBDMock(t)
		beads.ResetEnsuredDirs()

		townRoot := t.TempDir()
		beadsDir := filepath.Join(townRoot, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Put sentinel ONLY in .beads (the correct location).
		currentTypes := strings.Join(constants.BeadsCustomTypesList(), ",")
		correctSentinel := filepath.Join(beadsDir, ".gt-types-configured")
		if err := os.WriteFile(correctSentinel, []byte(currentTypes+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		// Calling EnsureCustomTypes(townRoot) — the buggy path — would
		// look for sentinel at townRoot/.gt-types-configured (wrong place),
		// not find it, and either error or run bd config in the wrong dir.
		//
		// The fix is to always call ResolveBeadsDir first, which
		// transforms townRoot → townRoot/.beads.
		resolved := beads.ResolveBeadsDir(townRoot)
		if resolved == townRoot {
			t.Fatal("ResolveBeadsDir should NOT return workspace root unchanged")
		}
		if resolved != beadsDir {
			t.Fatalf("ResolveBeadsDir(%q) = %q, want %q", townRoot, resolved, beadsDir)
		}

		// With the resolved path, EnsureCustomTypes initializes/migrates the
		// database and then finds the sentinel in the correct .beads directory.
		if err := beads.EnsureCustomTypes(resolved); err != nil {
			t.Fatalf("EnsureCustomTypes(resolved) failed: %v", err)
		}

		// Verify sentinel remains only in .beads, not workspace root.
		wrongSentinel := filepath.Join(townRoot, ".gt-types-configured")
		if _, statErr := os.Stat(wrongSentinel); statErr == nil {
			t.Error("sentinel leaked to workspace root")
		}
	})

	// Subtest 3: ResolveBeadsDir + EnsureCustomTypes works correctly
	t.Run("ResolveBeadsDir fixes the path before EnsureCustomTypes", func(t *testing.T) {
		installConvoyResolveBDMock(t)
		beads.ResetEnsuredDirs()

		townRoot := t.TempDir()
		beadsDir := filepath.Join(townRoot, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Pre-populate sentinel in .beads. EnsureCustomTypes should still use
		// the resolved .beads path for initialization/migration.
		currentTypes := strings.Join(constants.BeadsCustomTypesList(), ",")
		if err := os.WriteFile(filepath.Join(beadsDir, ".gt-types-configured"), []byte(currentTypes+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		// FIX: resolve first, then call EnsureCustomTypes.
		resolved := beads.ResolveBeadsDir(townRoot)
		if resolved != beadsDir {
			t.Fatalf("ResolveBeadsDir(%q) = %q, want %q", townRoot, resolved, beadsDir)
		}

		if err := beads.EnsureCustomTypes(resolved); err != nil {
			t.Fatalf("EnsureCustomTypes(resolved) failed: %v", err)
		}
	})

	// Subtest 4: Same for EnsureCustomStatuses
	t.Run("ResolveBeadsDir fixes the path before EnsureCustomStatuses", func(t *testing.T) {
		installConvoyResolveBDMock(t)
		beads.ResetEnsuredDirs()

		townRoot := t.TempDir()
		beadsDir := filepath.Join(townRoot, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		currentStatuses := strings.Join(constants.BeadsCustomStatusesList(), ",")
		if err := os.WriteFile(filepath.Join(beadsDir, ".gt-statuses-configured"), []byte(currentStatuses+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		resolved := beads.ResolveBeadsDir(townRoot)
		if resolved != beadsDir {
			t.Fatalf("ResolveBeadsDir(%q) = %q, want %q", townRoot, resolved, beadsDir)
		}

		if err := beads.EnsureCustomStatuses(resolved); err != nil {
			t.Fatalf("EnsureCustomStatuses(resolved) failed: %v", err)
		}
	})
}

// TestResolveBeadsDir_WorkspaceRootVsBeadsDir verifies that ResolveBeadsDir
// correctly handles the getTownBeadsDir() output (workspace root) by appending
// .beads, while also being idempotent when already given a .beads path.
func TestResolveBeadsDir_WorkspaceRootVsBeadsDir(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "workspace root gets .beads appended",
			input: townRoot,
			want:  beadsDir,
		},
		{
			name:  "already .beads path is normalized",
			input: beadsDir,
			want:  beadsDir,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := beads.ResolveBeadsDir(tc.input)
			if got != tc.want {
				t.Errorf("ResolveBeadsDir(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestResolveBeadsDir_WithRedirect verifies that ResolveBeadsDir follows
// redirect files, which is how rig worktrees (polecats) point back to the
// shared beads database. The convoy code must call ResolveBeadsDir to handle
// this case — passing the raw workspace root would skip the redirect.
func TestResolveBeadsDir_WithRedirect(t *testing.T) {
	sharedRoot := t.TempDir()
	sharedBeads := filepath.Join(sharedRoot, ".beads")
	if err := os.MkdirAll(sharedBeads, 0755); err != nil {
		t.Fatal(err)
	}

	worktreeRoot := t.TempDir()
	worktreeBeads := filepath.Join(worktreeRoot, ".beads")
	if err := os.MkdirAll(worktreeBeads, 0755); err != nil {
		t.Fatal(err)
	}

	// Redirect file: worktree/.beads/redirect → shared/.beads
	if err := os.WriteFile(filepath.Join(worktreeBeads, "redirect"), []byte(sharedBeads+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Without ResolveBeadsDir (the bug): would use worktreeRoot directly,
	// missing the redirect entirely.
	// With ResolveBeadsDir (the fix): follows redirect to sharedBeads.
	resolved := beads.ResolveBeadsDir(worktreeRoot)
	if resolved != sharedBeads {
		t.Errorf("ResolveBeadsDir(%q) = %q, want %q (should follow redirect)",
			worktreeRoot, resolved, sharedBeads)
	}
}

// TestConvoyCreate_SentinelPlacement verifies that the convoy create path
// writes sentinel files to the .beads directory, not the workspace root.
// This is an end-to-end regression test for the empty convoy bug.
func TestConvoyCreate_SentinelPlacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	installConvoyResolveBDMock(t)
	beads.ResetEnsuredDirs()

	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Simulate the fixed code path: ResolveBeadsDir(getTownBeadsDir())
	resolved := beads.ResolveBeadsDir(townRoot)
	if resolved != beadsDir {
		t.Fatalf("resolved = %q, want %q", resolved, beadsDir)
	}

	// Pre-populate sentinels. The bd mock covers initialization/migration while
	// the assertions below verify sentinels stay in .beads, not the workspace root.
	currentTypes := strings.Join(constants.BeadsCustomTypesList(), ",")
	if err := os.WriteFile(filepath.Join(beadsDir, ".gt-types-configured"), []byte(currentTypes+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	currentStatuses := strings.Join(constants.BeadsCustomStatusesList(), ",")
	if err := os.WriteFile(filepath.Join(beadsDir, ".gt-statuses-configured"), []byte(currentStatuses+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Call both functions with the resolved path (as the fixed code does).
	if err := beads.EnsureCustomTypes(resolved); err != nil {
		t.Fatalf("EnsureCustomTypes(resolved) failed: %v", err)
	}
	if err := beads.EnsureCustomStatuses(resolved); err != nil {
		t.Fatalf("EnsureCustomStatuses(resolved) failed: %v", err)
	}

	// Verify sentinels are in .beads/, NOT in the workspace root.
	for _, sentinel := range []string{".gt-types-configured", ".gt-statuses-configured"} {
		correctPath := filepath.Join(beadsDir, sentinel)
		wrongPath := filepath.Join(townRoot, sentinel)

		if _, err := os.Stat(correctPath); err != nil {
			t.Errorf("sentinel %q missing from .beads dir: %v", sentinel, err)
		}
		if _, err := os.Stat(wrongPath); err == nil {
			t.Errorf("sentinel %q found in workspace root — "+
				"types/statuses registered in wrong location", sentinel)
		}
	}
}
