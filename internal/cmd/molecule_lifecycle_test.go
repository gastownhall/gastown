package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// TestExtractRoleFromIdentity verifies that role names are correctly extracted
// from agent identity strings, including trailing slashes and compound paths.
func TestExtractRoleFromIdentity(t *testing.T) {
	tests := []struct {
		target string
		want   string
	}{
		{"gastown/refinery", "refinery"},
		{"gastown/witness", "witness"},
		{"gastown/crew/jack", "jack"},
		{"gastown/polecats/nux", "nux"},
		{"mayor/", "mayor"},
		{"deacon/", "deacon"},
		{"deacon/boot", "boot"},
		{"refinery", "refinery"},
	}
	for _, tc := range tests {
		got := extractRoleFromIdentity(tc.target)
		if got != tc.want {
			t.Errorf("extractRoleFromIdentity(%q) = %q, want %q", tc.target, got, tc.want)
		}
	}
}

// TestSquashJitterInvalidDuration verifies that an invalid --jitter value
// returns a parse error immediately (before any workspace operations).
func TestSquashJitterInvalidDuration(t *testing.T) {
	prev := moleculeJitter
	t.Cleanup(func() { moleculeJitter = prev })

	moleculeJitter = "bogus"
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := runMoleculeSquash(cmd, nil)
	if err == nil {
		t.Fatal("expected error for invalid jitter duration, got nil")
	}
	if !strings.Contains(err.Error(), "invalid --jitter duration") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestSquashJitterNegativeDuration verifies that a negative --jitter value
// is rejected with a clear error rather than silently skipped.
func TestSquashJitterNegativeDuration(t *testing.T) {
	prev := moleculeJitter
	t.Cleanup(func() { moleculeJitter = prev })

	moleculeJitter = "-5s"
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := runMoleculeSquash(cmd, nil)
	if err == nil {
		t.Fatal("expected error for negative jitter duration, got nil")
	}
	if !strings.Contains(err.Error(), "non-negative") {
		t.Errorf("expected non-negative error, got: %v", err)
	}
}

// TestSquashJitterZeroDuration verifies that --jitter 0s proceeds without
// sleeping (the jitterMax > 0 guard skips the sleep block).
// This tests the parse path only — the command will fail at workspace lookup
// since we run from a temp directory outside any gastown workspace.
func TestSquashJitterZeroDuration(t *testing.T) {
	prev := moleculeJitter
	t.Cleanup(func() { moleculeJitter = prev })

	// Run from a temp dir so workspace.FindFromCwd() fails
	tmpDir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	moleculeJitter = "0s"
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err = runMoleculeSquash(cmd, nil)
	// Should fail at workspace lookup, NOT at jitter parsing
	if err == nil {
		t.Fatal("expected workspace error, got nil")
	}
	if strings.Contains(err.Error(), "jitter") {
		t.Errorf("jitter 0s should be accepted, but got jitter error: %v", err)
	}
}

// TestSquashJitterContextCancellation verifies that the jitter sleep respects
// context cancellation and returns promptly instead of blocking.
func TestSquashJitterContextCancellation(t *testing.T) {
	prev := moleculeJitter
	t.Cleanup(func() { moleculeJitter = prev })

	// Run from a temp dir so workspace.FindFromCwd() fails
	tmpDir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Use a long jitter so the sleep would block if cancellation didn't work
	moleculeJitter = "10m"
	cmd := &cobra.Command{}

	// Create a pre-cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd.SetContext(ctx)

	start := time.Now()
	err = runMoleculeSquash(cmd, nil)
	elapsed := time.Since(start)

	// Should return quickly (the workspace lookup might fail first on some systems,
	// but if it reaches the jitter block, cancellation should fire immediately)
	if elapsed > 5*time.Second {
		t.Errorf("jitter sleep should have been cancelled, but took %v", elapsed)
	}
	// Accept either context error or workspace error (depends on execution order)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestSlingFormulaOnBeadHooksBaseBead verifies that when using
// "gt sling <formula> --on <bead>", the BASE bead is hooked (not the wisp).
//
// Current bug: The code hooks the wisp (compound root) instead of the base bead.
// This causes lifecycle issues:
// - Base bead stays open after wisp completes
// - gt done closes wisp, not the actual work item
// - Orphaned base beads accumulate
//
// Expected behavior: Hook the base bead, store attached_molecule pointing to wisp.
// gt hook/gt prime can follow attached_molecule to find the workflow steps.
func TestSlingFormulaOnBeadHooksBaseBead(t *testing.T) {
	townRoot := t.TempDir()

	// Minimal workspace marker
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Create routes
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

	// Stub bd to track which bead gets hooked
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -e
echo "$*" >> "${BD_LOG}"
if [ "$1" = "--allow-stale" ]; then
  shift
fi
cmd="$1"
shift || true
case "$cmd" in
  show)
    # Return the base bead info
    echo '[{"id":"gt-abc123","title":"Bug to fix","status":"open","assignee":"","description":""}]'
    ;;
  formula)
    echo '{"name":"mol-polecat-work"}'
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
echo %*>>"%BD_LOG%"
set "cmd=%1"
set "sub=%2"
if "%cmd%"=="--allow-stale" (
  set "cmd=%2"
  set "sub=%3"
)
if "%cmd%"=="show" (
  echo [{^"id^":^"gt-abc123^",^"title^":^"Bug to fix^",^"status^":^"open^",^"assignee^":^"^",^"description^":^"^"}]
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
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
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

	// Save and restore global flag state
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
	slingOnTarget = "gt-abc123" // The base bead

	if err := runSling(nil, []string{"mol-polecat-work"}); err != nil {
		t.Fatalf("runSling: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}

	// Find the update command that sets status=hooked
	// Expected: should hook gt-abc123 (base bead)
	// Current bug: hooks gt-wisp-xyz (wisp)
	logLines := strings.Split(string(logBytes), "\n")
	var hookedBeadID string
	for _, line := range logLines {
		if strings.Contains(line, "update") && strings.Contains(line, "--status=hooked") {
			// Extract the bead ID being hooked
			// Format: "update <beadID> --status=hooked ..."
			parts := strings.Fields(line)
			for i, part := range parts {
				if part == "update" && i+1 < len(parts) {
					hookedBeadID = parts[i+1]
					break
				}
			}
			break
		}
	}

	if hookedBeadID == "" {
		t.Fatalf("no hooked bead found in log:\n%s", string(logBytes))
	}

	// The BASE bead (gt-abc123) should be hooked, not the wisp (gt-wisp-xyz)
	if hookedBeadID != "gt-abc123" {
		t.Errorf("wrong bead hooked: got %q, want %q (base bead)\n"+
			"Current behavior hooks the wisp instead of the base bead.\n"+
			"This causes orphaned base beads when gt done closes only the wisp.\n"+
			"Log:\n%s", hookedBeadID, "gt-abc123", string(logBytes))
	}
}

// TestSlingFormulaOnBeadSetsAttachedMoleculeInBaseBead verifies that when using
// "gt sling <formula> --on <bead>", the attached_molecule field is set in the
// BASE bead's description (pointing to the wisp), not in the wisp itself.
//
// Current bug: attached_molecule is stored as a self-reference in the wisp.
// This is semantically meaningless (wisp points to itself) and breaks
// compound resolution from the base bead.
//
// Expected behavior: Store attached_molecule in the base bead pointing to wisp.
// This enables:
// - Compound resolution: base bead -> attached_molecule -> wisp
// - gt hook/gt prime: read base bead, follow attached_molecule to show wisp steps
func TestSlingFormulaOnBeadSetsAttachedMoleculeInBaseBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows batch script JSON output causes storeAttachedMoleculeInBead to fail silently")
	}
	townRoot := t.TempDir()

	// Minimal workspace marker
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}

	// Create routes
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

	// Stub bd to track which bead gets attached_molecule set
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -e
echo "$*" >> "${BD_LOG}"
if [ "$1" = "--allow-stale" ]; then
  shift
fi
cmd="$1"
shift || true
case "$cmd" in
  show)
    # Return bead info without attached_molecule initially
    echo '[{"id":"gt-abc123","title":"Bug to fix","status":"open","assignee":"","description":""}]'
    ;;
  formula)
    echo '{"name":"mol-polecat-work"}'
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
echo %*>>"%BD_LOG%"
set "cmd=%1"
set "sub=%2"
if "%cmd%"=="--allow-stale" (
  set "cmd=%2"
  set "sub=%3"
)
if "%cmd%"=="show" (
  echo [{^"id^":^"gt-abc123^",^"title^":^"Bug to fix^",^"status^":^"open^",^"assignee^":^"^",^"description^":^"^"}]
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
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
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

	// Save and restore global flag state
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
	slingOnTarget = "gt-abc123" // The base bead

	if err := runSling(nil, []string{"mol-polecat-work"}); err != nil {
		t.Fatalf("runSling: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}

	// Find update commands that set attached_molecule
	// Expected: "update gt-abc123 --description=...attached_molecule: gt-wisp-xyz..."
	// Current bug: "update gt-wisp-xyz --description=...attached_molecule: gt-wisp-xyz..."
	logLines := strings.Split(string(logBytes), "\n")
	var attachedMoleculeTarget string
	for _, line := range logLines {
		if strings.Contains(line, "update") && strings.Contains(line, "attached_molecule") {
			// Extract the bead ID being updated
			parts := strings.Fields(line)
			for i, part := range parts {
				if part == "update" && i+1 < len(parts) {
					attachedMoleculeTarget = parts[i+1]
					break
				}
			}
			break
		}
	}

	if attachedMoleculeTarget == "" {
		t.Fatalf("no attached_molecule update found in log:\n%s", string(logBytes))
	}

	// attached_molecule should be set on the BASE bead, not the wisp
	if attachedMoleculeTarget != "gt-abc123" {
		t.Errorf("attached_molecule set on wrong bead: got %q, want %q (base bead)\n"+
			"Current behavior stores attached_molecule in the wisp as a self-reference.\n"+
			"This breaks compound resolution (base bead has no pointer to wisp).\n"+
			"Log:\n%s", attachedMoleculeTarget, "gt-abc123", string(logBytes))
	}
}

// TestBurnClosesWispRoot verifies that runMoleculeBurn closes the wisp root
// via ForceCloseWithReason after detaching. Without this, patrol molecule roots
// stay in "hooked" status indefinitely (issue #1828).
func TestBurnClosesWispRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()

	// Workspace marker: mayor/ directory (SecondaryMarker)
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}

	// .beads directory (for findLocalBeadsDir and lockBead)
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}

	// Stub bd binary
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog := filepath.Join(townRoot, "closes.log")
	// The handoff bead has attached_molecule: gt-wisp-mol1
	bdScript := fmt.Sprintf(`#!/bin/sh
# Strip --allow-stale
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"; shift
case "$cmd" in
  list)
    # FindHandoffBead: list --json --status=pinned --limit=0
    if echo "$*" | grep -q "status=pinned"; then
      echo '[{"id":"gt-handoff-1","title":"witness Handoff","status":"pinned","description":"attached_molecule: gt-wisp-mol1"}]'
    else
      # closeDescendants: list --json --parent=... --status=all
      echo '[]'
    fi
    ;;
  show)
    # DetachMoleculeWithAudit calls Show twice
    echo '[{"id":"gt-handoff-1","title":"witness Handoff","status":"pinned","description":"attached_molecule: gt-wisp-mol1"}]'
    ;;
  update)
    exit 0
    ;;
  close)
    # Log every close call
    echo "$*" >> "%s"
    ;;
esac
exit 0
`, closesLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "witness")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_RIG", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("BEADS_DIR", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Save and restore global flag state
	prevJSON := moleculeJSON
	t.Cleanup(func() { moleculeJSON = prevJSON })
	moleculeJSON = false

	err = runMoleculeBurn(nil, []string{"witness"})
	if err != nil {
		t.Fatalf("runMoleculeBurn: %v", err)
	}

	// Verify that bd close was called with the molecule root ID
	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("no close calls logged (closes.log not found): bd close was never called")
	}
	closes := string(closesBytes)
	if !strings.Contains(closes, "gt-wisp-mol1") {
		t.Errorf("molecule root gt-wisp-mol1 was NOT closed.\n"+
			"ForceCloseWithReason should be called after DetachMoleculeWithAudit.\n"+
			"Close calls: %s", closes)
	}
}

// TestSquashClosesWispRoot verifies that runMoleculeSquash closes the wisp root
// via ForceCloseWithReason after detaching. Without this, patrol molecule roots
// stay in "hooked" status indefinitely (issue #1828).
func TestSquashClosesWispRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()

	// Workspace marker
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}

	// .beads directory
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}

	// Stub bd binary
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog := filepath.Join(townRoot, "closes.log")
	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"; shift
case "$cmd" in
  list)
    if echo "$*" | grep -q "status=pinned"; then
      echo '[{"id":"gt-handoff-1","title":"refinery Handoff","status":"pinned","description":"attached_molecule: gt-wisp-patrol1"}]'
    else
      echo '[]'
    fi
    ;;
  show)
    echo '[{"id":"gt-handoff-1","title":"refinery Handoff","status":"pinned","description":"attached_molecule: gt-wisp-patrol1"}]'
    ;;
  update)
    exit 0
    ;;
  create)
    # Digest creation
    echo '{"id":"gt-digest-1","title":"Digest"}'
    ;;
  close)
    echo "$*" >> "%s"
    ;;
esac
exit 0
`, closesLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "refinery")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_RIG", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("BEADS_DIR", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Save and restore global flag state
	prevJSON := moleculeJSON
	prevJitter := moleculeJitter
	prevNoDigest := moleculeNoDigest
	prevSummary := moleculeSummary
	t.Cleanup(func() {
		moleculeJSON = prevJSON
		moleculeJitter = prevJitter
		moleculeNoDigest = prevNoDigest
		moleculeSummary = prevSummary
	})
	moleculeJSON = false
	moleculeJitter = ""
	moleculeNoDigest = true // skip digest to simplify mock
	moleculeSummary = ""

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err = runMoleculeSquash(cmd, []string{"refinery"})
	if err != nil {
		t.Fatalf("runMoleculeSquash: %v", err)
	}

	// Verify that bd close was called with the molecule root ID
	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("no close calls logged (closes.log not found): bd close was never called")
	}
	closes := string(closesBytes)
	if !strings.Contains(closes, "gt-wisp-patrol1") {
		t.Errorf("molecule root gt-wisp-patrol1 was NOT closed.\n"+
			"ForceCloseWithReason should be called after DetachMoleculeWithAudit.\n"+
			"Close calls: %s", closes)
	}
}

// TestSquashClosesDescendantsAndRoot verifies the full close order:
// descendants are closed first, then the root molecule itself.
func TestSquashClosesDescendantsAndRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog := filepath.Join(townRoot, "closes.log")
	// Mock returns 2 children for the molecule, one open and one closed
	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"; shift
case "$cmd" in
  list)
    if echo "$*" | grep -q "status=pinned"; then
      echo '[{"id":"gt-handoff-1","title":"witness Handoff","status":"pinned","description":"attached_molecule: gt-wisp-mol2"}]'
    elif echo "$*" | grep -q "parent=gt-wisp-mol2"; then
      echo '[{"id":"gt-step-1","title":"Step 1","status":"open"},{"id":"gt-step-2","title":"Step 2","status":"closed"}]'
    elif echo "$*" | grep -q "parent=gt-step"; then
      echo '[]'
    else
      echo '[]'
    fi
    ;;
  show)
    echo '[{"id":"gt-handoff-1","title":"witness Handoff","status":"pinned","description":"attached_molecule: gt-wisp-mol2"}]'
    ;;
  update)
    exit 0
    ;;
  close)
    echo "$*" >> "%s"
    ;;
esac
exit 0
`, closesLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "witness")
	t.Setenv("GT_POLECAT", "")
	t.Setenv("GT_CREW", "")
	t.Setenv("GT_RIG", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("BEADS_DIR", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	prevJSON := moleculeJSON
	t.Cleanup(func() { moleculeJSON = prevJSON })
	moleculeJSON = false

	err = runMoleculeBurn(nil, []string{"witness"})
	if err != nil {
		t.Fatalf("runMoleculeBurn: %v", err)
	}

	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("no close calls logged: %v", err)
	}
	closes := string(closesBytes)
	closeLines := strings.Split(strings.TrimSpace(closes), "\n")

	// Verify: gt-step-1 is closed (open child), gt-step-2 is NOT closed (already closed)
	foundStep1 := false
	foundRoot := false
	for _, line := range closeLines {
		if strings.Contains(line, "gt-step-1") {
			foundStep1 = true
		}
		if strings.Contains(line, "gt-wisp-mol2") {
			foundRoot = true
		}
	}

	if !foundStep1 {
		t.Errorf("open child gt-step-1 was NOT closed.\nClose calls:\n%s", closes)
	}
	if !foundRoot {
		t.Errorf("molecule root gt-wisp-mol2 was NOT closed.\nClose calls:\n%s", closes)
	}

	// Verify root is closed AFTER children (root should appear in a later close call)
	step1Idx := -1
	rootIdx := -1
	for i, line := range closeLines {
		if strings.Contains(line, "gt-step-1") {
			step1Idx = i
		}
		if strings.Contains(line, "gt-wisp-mol2") {
			rootIdx = i
		}
	}
	if step1Idx >= 0 && rootIdx >= 0 && rootIdx < step1Idx {
		t.Errorf("root was closed BEFORE children (root line %d, child line %d).\n"+
			"Expected: descendants first, then root.\nClose calls:\n%s",
			rootIdx, step1Idx, closes)
	}
}

// TestDoneClosesAttachedMolecule verifies that gt done closes both the hooked
// bead AND its attached molecule (wisp).
//
// Current bug: gt done only closes the hooked bead. If base bead is hooked
// with attached_molecule pointing to wisp, the wisp becomes orphaned.
//
// Expected behavior: gt done should:
// 1. Check for attached_molecule in hooked bead
// 2. Close the attached molecule (wisp) first
// 3. Close the hooked bead (base bead)
//
// This ensures no orphaned wisps remain after work completes.
func TestDoneClosesAttachedMolecule(t *testing.T) {
	townRoot := t.TempDir()

	// Create rig structure - use simple rig name that matches routes lookup
	rigPath := filepath.Join(townRoot, "gastown")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// Create routes - path first part must match GT_RIG for prefix lookup
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown"}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	// Stub bd to track close calls
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	closesPath := filepath.Join(townRoot, "closes.log")

	// The stub simulates:
	// - Agent bead gt-agent-nux with hook_bead = gt-abc123 (base bead)
	// - Base bead gt-abc123 with attached_molecule: gt-wisp-xyz, status=hooked
	// - Wisp gt-wisp-xyz (the attached molecule)
	bdScript := fmt.Sprintf(`#!/bin/sh
echo "$*" >> "%s/bd.log"
# Strip --allow-stale
while [ "$1" = "--allow-stale" ]; do
  shift
done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nux)
        echo '[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","hook_bead":"gt-abc123","agent_state":"working"}]'
        ;;
      gt-abc123)
        echo '[{"id":"gt-abc123","title":"Bug to fix","status":"hooked","description":"attached_molecule: gt-wisp-xyz"}]'
        ;;
      gt-wisp-xyz)
        echo '[{"id":"gt-wisp-xyz","title":"mol-polecat-work","status":"open","ephemeral":true}]'
        ;;
      *)
        echo '[]'
        ;;
    esac
    ;;
  close)
    echo "$1" >> "%s"
    ;;
  agent|update|slot)
    exit 0
    ;;
esac
exit 0
`, townRoot, closesPath)

	bdScriptWindows := fmt.Sprintf(`@echo off
setlocal enableextensions
echo %%*>>"%s\bd.log"
set "cmd=%%1"
set "beadID=%%2"
:strip_flags
if "%%cmd%%"=="--allow-stale" (
  set "cmd=%%2"
  set "beadID=%%3"
  shift
  goto strip_flags
)
if "%%cmd%%"=="show" (
  if "%%beadID%%"=="gt-gastown-polecat-nux" (
    echo [{^"id^":^"gt-gastown-polecat-nux^",^"title^":^"Polecat nux^",^"status^":^"open^",^"hook_bead^":^"gt-abc123^",^"agent_state^":^"working^"}]
    exit /b 0
  )
  if "%%beadID%%"=="gt-abc123" (
    echo [{^"id^":^"gt-abc123^",^"title^":^"Bug to fix^",^"status^":^"hooked^",^"description^":^"attached_molecule: gt-wisp-xyz^"}]
    exit /b 0
  )
  if "%%beadID%%"=="gt-wisp-xyz" (
    echo [{^"id^":^"gt-wisp-xyz^",^"title^":^"mol-polecat-work^",^"status^":^"open^",^"ephemeral^":true}]
    exit /b 0
  )
  echo []
  exit /b 0
)
if "%%cmd%%"=="close" (
  echo %%beadID%%>>"%s"
  exit /b 0
)
if "%%cmd%%"=="agent" exit /b 0
if "%%cmd%%"=="update" exit /b 0
if "%%cmd%%"=="slot" exit /b 0
exit /b 0
`, townRoot, closesPath)

	if runtime.GOOS == "windows" {
		bdPath := filepath.Join(binDir, "bd.cmd")
		if err := os.WriteFile(bdPath, []byte(bdScriptWindows), 0644); err != nil {
			t.Fatalf("write bd stub: %v", err)
		}
	} else {
		bdPath := filepath.Join(binDir, "bd")
		if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
			t.Fatalf("write bd stub: %v", err)
		}
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GT_ROLE", "polecat")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "nux")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(rigPath); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Call the unexported function directly (same package)
	// updateAgentStateOnDone(cwd, townRoot, exitType, issueID, false, false)
	// Pass issueID directly — hq-l6mm5 removed agent bead hook slot lookup
	updateAgentStateOnDone(rigPath, townRoot, ExitCompleted, "gt-abc123", false, false)

	// Read the close log to see what got closed
	closesBytes, err := os.ReadFile(closesPath)
	if err != nil {
		// No closes happened at all - that's a failure
		t.Fatalf("no beads were closed (closes.log doesn't exist)")
	}
	closes := string(closesBytes)
	closeLines := strings.Split(strings.TrimSpace(closes), "\n")

	// Check that attached molecule gt-wisp-xyz was closed
	foundWisp := false
	foundBase := false
	for _, line := range closeLines {
		if strings.Contains(line, "gt-wisp-xyz") {
			foundWisp = true
		}
		if strings.Contains(line, "gt-abc123") {
			foundBase = true
		}
	}

	if !foundWisp {
		t.Errorf("attached molecule gt-wisp-xyz was NOT closed\n"+
			"gt done should close the attached_molecule before closing the hooked bead.\n"+
			"This leaves orphaned wisps after work completes.\n"+
			"Beads closed: %v", closeLines)
	}

	if !foundBase {
		t.Errorf("hooked bead gt-abc123 was NOT closed\n"+
			"Beads closed: %v", closeLines)
	}
}
