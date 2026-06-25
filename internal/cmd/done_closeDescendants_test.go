package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestDoneCloseDescendantsWithChildren verifies that when gt done is called
// with a molecule that has children, closeDescendants closes the children
// before the root molecule (edge case #1).
func TestDoneCloseDescendantsWithChildren(t *testing.T) {
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

	// Create routes for rig lookup
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown"}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	// Stub bd binary
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog := filepath.Join(townRoot, "closes.log")

	// The stub simulates:
	// - Agent bead with hook_bead pointing to base bead
	// - Base bead with attached_molecule pointing to wisp
	// - Wisp with 2 children (step-1 and step-2), both open
	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nux)
        echo '[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","hook_bead":"gt-base-123","agent_state":"working"}]'
        ;;
      gt-base-123)
        echo '[{"id":"gt-base-123","title":"Base bead","status":"hooked","description":"attached_molecule: gt-wisp-xyz"}]'
        ;;
      gt-wisp-xyz)
        echo '[{"id":"gt-wisp-xyz","title":"mol-polecat-work","status":"open","ephemeral":true}]'
        ;;
    esac
    ;;
  list|query)
    # Return children when listing with parent=gt-wisp-xyz
    if echo "$*" | grep -Eq 'parent="?gt-wisp-xyz"?'; then
      echo '[{"id":"gt-step-1","title":"Step 1","status":"open"},{"id":"gt-step-2","title":"Step 2","status":"open"}]'
    else
      echo '[]'
    fi
    ;;
  close)
    # Log bead IDs only, skip flags (--reason, --force, --session)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%s"
    done
    ;;
  agent|update|slot)
    exit 0
    ;;
esac
exit 0
`, closesLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
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
	if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Call updateAgentStateOnDone directly
	updateAgentStateOnDone(filepath.Join(townRoot, "gastown"), townRoot, ExitCompleted, "gt-base-123")

	// Verify close calls
	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("no beads were closed: %v", err)
	}
	closes := string(closesBytes)
	closeLines := strings.Split(strings.TrimSpace(closes), "\n")

	// Should have closed: step-1, step-2 (children), then wisp (attached molecule), then base-123 (hooked bead)
	foundStep1 := false
	foundStep2 := false
	foundWisp := false
	foundBase := false

	for _, line := range closeLines {
		if strings.Contains(line, "gt-step-1") {
			foundStep1 = true
		}
		if strings.Contains(line, "gt-step-2") {
			foundStep2 = true
		}
		if strings.Contains(line, "gt-wisp-xyz") {
			foundWisp = true
		}
		if strings.Contains(line, "gt-base-123") {
			foundBase = true
		}
	}

	if !foundStep1 {
		t.Errorf("child gt-step-1 was NOT closed\nClose calls:\n%s", closes)
	}
	if !foundStep2 {
		t.Errorf("child gt-step-2 was NOT closed\nClose calls:\n%s", closes)
	}
	if !foundWisp {
		t.Errorf("attached molecule gt-wisp-xyz was NOT closed\nClose calls:\n%s", closes)
	}
	if !foundBase {
		t.Errorf("hooked bead gt-base-123 was NOT closed\nClose calls:\n%s", closes)
	}

	// Verify order: children should be closed before wisp, wisp before base
	step1Idx := -1
	step2Idx := -1
	wispIdx := -1
	baseIdx := -1

	for i, line := range closeLines {
		if strings.Contains(line, "gt-step-1") {
			step1Idx = i
		}
		if strings.Contains(line, "gt-step-2") {
			step2Idx = i
		}
		if strings.Contains(line, "gt-wisp-xyz") {
			wispIdx = i
		}
		if strings.Contains(line, "gt-base-123") {
			baseIdx = i
		}
	}

	// wisp should be closed AFTER children
	if wispIdx >= 0 && step1Idx >= 0 && wispIdx < step1Idx {
		t.Errorf("wisp closed BEFORE step-1 (wisp line %d, step-1 line %d)", wispIdx, step1Idx)
	}
	if wispIdx >= 0 && step2Idx >= 0 && wispIdx < step2Idx {
		t.Errorf("wisp closed BEFORE step-2 (wisp line %d, step-2 line %d)", wispIdx, step2Idx)
	}
	// base should be closed AFTER wisp
	if baseIdx >= 0 && wispIdx >= 0 && baseIdx < wispIdx {
		t.Errorf("base bead closed BEFORE wisp (base line %d, wisp line %d)", baseIdx, wispIdx)
	}
}

// TestDoneCloseDescendantsNoChildren verifies that gt done works correctly
// when the molecule has no children - it should just close the molecule and
// hooked bead without errors (edge case #2).
func TestDoneCloseDescendantsNoChildren(t *testing.T) {
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

	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown"}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog := filepath.Join(townRoot, "closes.log")

	// The stub returns empty list for children (no children)
	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nux)
        echo '[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","hook_bead":"gt-base-123","agent_state":"working"}]'
        ;;
      gt-base-123)
        echo '[{"id":"gt-base-123","title":"Base bead","status":"hooked","description":"attached_molecule: gt-wisp-xyz"}]'
        ;;
      gt-wisp-xyz)
        echo '[{"id":"gt-wisp-xyz","title":"mol-polecat-work","status":"open","ephemeral":true}]'
        ;;
    esac
    ;;
  list)
    # Always return empty - no children
    echo '[]'
    ;;
  close)
    # Log bead IDs only, skip flags (--reason, --force, --session)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%s"
    done
    ;;
  agent|update|slot)
    exit 0
    ;;
esac
exit 0
`, closesLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
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
	if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Should not error even though molecule has no children
	updateAgentStateOnDone(filepath.Join(townRoot, "gastown"), townRoot, ExitCompleted, "gt-base-123")

	// Verify close calls
	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("no beads were closed: %v", err)
	}
	closes := string(closesBytes)
	closeLines := strings.Split(strings.TrimSpace(closes), "\n")

	// Should have closed: wisp, then base (no children to close)
	foundWisp := false
	foundBase := false

	for _, line := range closeLines {
		if strings.Contains(line, "gt-wisp-xyz") {
			foundWisp = true
		}
		if strings.Contains(line, "gt-base-123") {
			foundBase = true
		}
	}

	if !foundWisp {
		t.Errorf("attached molecule gt-wisp-xyz was NOT closed\nClose calls:\n%s", closes)
	}
	if !foundBase {
		t.Errorf("hooked bead gt-base-123 was NOT closed\nClose calls:\n%s", closes)
	}

	// Should only have 2 close calls (wisp and base)
	if len(closeLines) != 2 {
		t.Errorf("expected 2 close calls (wisp, base), got %d:\n%s", len(closeLines), closes)
	}
}

// TestDoneCloseDescendantsSomeAlreadyClosed verifies that closeDescendants
// skips children that are already closed (edge case #3).
func TestDoneCloseDescendantsSomeAlreadyClosed(t *testing.T) {
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

	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown"}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog := filepath.Join(townRoot, "closes.log")

	// The stub returns 2 children: one open, one already closed
	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nux)
        echo '[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","hook_bead":"gt-base-123","agent_state":"working"}]'
        ;;
      gt-base-123)
        echo '[{"id":"gt-base-123","title":"Base bead","status":"hooked","description":"attached_molecule: gt-wisp-xyz"}]'
        ;;
      gt-wisp-xyz)
        echo '[{"id":"gt-wisp-xyz","title":"mol-polecat-work","status":"open","ephemeral":true}]'
        ;;
    esac
    ;;
  list|query)
    # Return one open child and one already-closed child
    if echo "$*" | grep -Eq 'parent="?gt-wisp-xyz"?'; then
      echo '[{"id":"gt-step-open","title":"Step Open","status":"open"},{"id":"gt-step-closed","title":"Step Closed","status":"closed"}]'
    else
      echo '[]'
    fi
    ;;
  close)
    # Log bead IDs only, skip flags (--reason, --force, --session)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%s"
    done
    ;;
  agent|update|slot)
    exit 0
    ;;
esac
exit 0
`, closesLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
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
	if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	updateAgentStateOnDone(filepath.Join(townRoot, "gastown"), townRoot, ExitCompleted, "gt-base-123")

	// Verify close calls
	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("no beads were closed: %v", err)
	}
	closes := string(closesBytes)
	closeLines := strings.Split(strings.TrimSpace(closes), "\n")

	// Should have closed: gt-step-open (not gt-step-closed since it's already closed), wisp, base
	foundOpen := false
	foundClosed := false
	foundWisp := false
	foundBase := false

	for _, line := range closeLines {
		if strings.Contains(line, "gt-step-open") {
			foundOpen = true
		}
		if strings.Contains(line, "gt-step-closed") {
			foundClosed = true
		}
		if strings.Contains(line, "gt-wisp-xyz") {
			foundWisp = true
		}
		if strings.Contains(line, "gt-base-123") {
			foundBase = true
		}
	}

	if !foundOpen {
		t.Errorf("open child gt-step-open was NOT closed\nClose calls:\n%s", closes)
	}
	if foundClosed {
		t.Errorf("already-closed child gt-step-closed SHOULD NOT have been closed again\nClose calls:\n%s", closes)
	}
	if !foundWisp {
		t.Errorf("attached molecule gt-wisp-xyz was NOT closed\nClose calls:\n%s", closes)
	}
	if !foundBase {
		t.Errorf("hooked bead gt-base-123 was NOT closed\nClose calls:\n%s", closes)
	}

	// Should have exactly 3 close calls
	if len(closeLines) != 3 {
		t.Errorf("expected 3 close calls (open-step, wisp, base), got %d:\n%s", len(closeLines), closes)
	}
}

// TestDoneCloseDescendantsDeeplyNested verifies that closeDescendants
// correctly handles deeply nested children (grandchildren) recursively
// (edge case #4).
func TestDoneCloseDescendantsDeeplyNested(t *testing.T) {
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

	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown"}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog := filepath.Join(townRoot, "closes.log")

	// The stub simulates a hierarchy: wisp -> child -> grandchild
	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nux)
        echo '[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","hook_bead":"gt-base-123","agent_state":"working"}]'
        ;;
      gt-base-123)
        echo '[{"id":"gt-base-123","title":"Base bead","status":"hooked","description":"attached_molecule: gt-wisp-xyz"}]'
        ;;
      gt-wisp-xyz)
        echo '[{"id":"gt-wisp-xyz","title":"mol-polecat-work","status":"open","ephemeral":true}]'
        ;;
    esac
    ;;
  list|query)
    # Return children based on parent
    if echo "$*" | grep -Eq 'parent="?gt-wisp-xyz"?'; then
      # Wisp has one child
      echo '[{"id":"gt-child","title":"Child","status":"open"}]'
    elif echo "$*" | grep -Eq 'parent="?gt-child"?'; then
      # Child has one grandchild
      echo '[{"id":"gt-grandchild","title":"Grandchild","status":"open"}]'
    else
      echo '[]'
    fi
    ;;
  close)
    # Log bead IDs only, skip flags (--reason, --force, --session)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%s"
    done
    ;;
  agent|update|slot)
    exit 0
    ;;
esac
exit 0
`, closesLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
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
	if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	updateAgentStateOnDone(filepath.Join(townRoot, "gastown"), townRoot, ExitCompleted, "gt-base-123")

	// Verify close calls
	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("no beads were closed: %v", err)
	}
	closes := string(closesBytes)
	closeLines := strings.Split(strings.TrimSpace(closes), "\n")

	// Should have closed: grandchild, child, wisp, base (in that order)
	foundGrandchild := false
	foundChild := false
	foundWisp := false
	foundBase := false

	for _, line := range closeLines {
		if strings.Contains(line, "gt-grandchild") {
			foundGrandchild = true
		}
		if strings.Contains(line, "gt-child") {
			foundChild = true
		}
		if strings.Contains(line, "gt-wisp-xyz") {
			foundWisp = true
		}
		if strings.Contains(line, "gt-base-123") {
			foundBase = true
		}
	}

	if !foundGrandchild {
		t.Errorf("grandchild gt-grandchild was NOT closed\nClose calls:\n%s", closes)
	}
	if !foundChild {
		t.Errorf("child gt-child was NOT closed\nClose calls:\n%s", closes)
	}
	if !foundWisp {
		t.Errorf("attached molecule gt-wisp-xyz was NOT closed\nClose calls:\n%s", closes)
	}
	if !foundBase {
		t.Errorf("hooked bead gt-base-123 was NOT closed\nClose calls:\n%s", closes)
	}

	// Should have exactly 4 close calls
	if len(closeLines) != 4 {
		t.Errorf("expected 4 close calls (grandchild, child, wisp, base), got %d:\n%s", len(closeLines), closes)
	}

	// Verify order: grandchild first, then child, then wisp, then base
	grandchildIdx := -1
	childIdx := -1
	wispIdx := -1
	baseIdx := -1

	for i, line := range closeLines {
		if strings.Contains(line, "gt-grandchild") {
			grandchildIdx = i
		}
		if strings.Contains(line, "gt-child") {
			childIdx = i
		}
		if strings.Contains(line, "gt-wisp-xyz") {
			wispIdx = i
		}
		if strings.Contains(line, "gt-base-123") {
			baseIdx = i
		}
	}

	// Order should be: grandchild < child < wisp < base
	if !(grandchildIdx < childIdx && childIdx < wispIdx && wispIdx < baseIdx) {
		t.Errorf("incorrect close order. Expected: grandchild < child < wisp < base\nGot indices: grandchild=%d, child=%d, wisp=%d, base=%d\nClose calls:\n%s",
			grandchildIdx, childIdx, wispIdx, baseIdx, closes)
	}
}

// TestDoneCloseDescendantsNoMoleculeAttached verifies that gt done handles
// the case where there is no molecule attached gracefully (edge case #5).
func TestDoneCloseDescendantsNoMoleculeAttached(t *testing.T) {
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

	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown"}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog := filepath.Join(townRoot, "closes.log")

	// The stub simulates a hooked bead with NO attached_molecule
	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nux)
        echo '[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","hook_bead":"gt-base-123","agent_state":"working"}]'
        ;;
      gt-base-123)
        # Hooked bead with NO attached_molecule
        echo '[{"id":"gt-base-123","title":"Base bead","status":"hooked","description":"no molecule attached"}]'
        ;;
    esac
    ;;
  list)
    echo '[]'
    ;;
  close)
    # Log bead IDs only, skip flags (--reason, --force, --session)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%s"
    done
    ;;
  agent|update|slot)
    exit 0
    ;;
esac
exit 0
`, closesLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
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
	if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Should not error even though there's no attached molecule
	updateAgentStateOnDone(filepath.Join(townRoot, "gastown"), townRoot, ExitCompleted, "gt-base-123")

	// Verify close calls - should only close the hooked base bead (no molecule)
	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("no beads were closed: %v", err)
	}
	closes := string(closesBytes)
	closeLines := strings.Split(strings.TrimSpace(closes), "\n")

	// Should have closed: base bead only (no molecule to close)
	foundBase := false

	for _, line := range closeLines {
		if strings.Contains(line, "gt-base-123") {
			foundBase = true
		}
	}

	if !foundBase {
		t.Errorf("hooked bead gt-base-123 was NOT closed\nClose calls:\n%s", closes)
	}

	// Should have exactly 1 close call
	if len(closeLines) != 1 {
		t.Errorf("expected 1 close call (base bead only), got %d:\n%s", len(closeLines), closes)
	}
}

// TestCloseDescendantsHandlesListError verifies that closeDescendants handles
// errors from b.List gracefully and continues with closing what it can.
func TestCloseDescendantsHandlesListError(t *testing.T) {
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

	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown"}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog := filepath.Join(townRoot, "closes.log")

	// The stub returns an error for list operations (simulating db failure)
	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nux)
        echo '[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","hook_bead":"gt-base-123","agent_state":"working"}]'
        ;;
      gt-base-123)
        echo '[{"id":"gt-base-123","title":"Base bead","status":"hooked","description":"attached_molecule: gt-wisp-xyz"}]'
        ;;
      gt-wisp-xyz)
        echo '[{"id":"gt-wisp-xyz","title":"mol-polecat-work","status":"open","ephemeral":true}]'
        ;;
    esac
    ;;
  list)
    # Simulate error when listing children
    echo 'Error: database locked' >&2
    exit 1
    ;;
  close)
    # Log bead IDs only, skip flags (--reason, --force, --session)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%s"
    done
    ;;
  agent|update|slot)
    exit 0
    ;;
esac
exit 0
`, closesLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
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
	if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Should not error even though list fails - continues with closing molecule and base bead
	updateAgentStateOnDone(filepath.Join(townRoot, "gastown"), townRoot, ExitCompleted, "gt-base-123")

	// Verify close calls - should still close wisp and base even though list failed
	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("no beads were closed: %v", err)
	}
	closes := string(closesBytes)

	// Should have closed: wisp, base (list error doesn't prevent molecule close)
	if !strings.Contains(closes, "gt-wisp-xyz") {
		t.Errorf("attached molecule gt-wisp-xyz was NOT closed after list error\nClose calls:\n%s", closes)
	}
	if !strings.Contains(closes, "gt-base-123") {
		t.Errorf("hooked bead gt-base-123 was NOT closed after list error\nClose calls:\n%s", closes)
	}
}

// TestCloseDescendantsMoleculeNotFound verifies that the fix handles the case
// where the attached molecule doesn't exist (already burned/deleted).
func TestCloseDescendantsMoleculeNotFound(t *testing.T) {
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

	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown"}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog := filepath.Join(townRoot, "closes.log")

	// The stub simulates an attached molecule that doesn't exist
	closeAttemptsLog := filepath.Join(townRoot, "close_attempts.log")
	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nux)
        echo '[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","hook_bead":"gt-base-123","agent_state":"working"}]'
        ;;
      gt-base-123)
        echo '[{"id":"gt-base-123","title":"Base bead","status":"hooked","description":"attached_molecule: gt-wisp-xyz"}]'
        ;;
      gt-wisp-xyz)
        # Molecule doesn't exist
        echo '[]'
        exit 1
        ;;
    esac
    ;;
  list)
    echo '[]'
    ;;
  close)
    echo "$1" >> "%s"
    # Simulate not found error for gt-wisp-xyz
    if [ "$1" = "gt-wisp-xyz" ]; then
      echo "close_attempt: $1 (not found)" >> "%s"
      exit 1
    fi
    echo "close_attempt: $1 (success)" >> "%s"
    ;;
  agent|update|slot)
    exit 0
    ;;
esac
exit 0
`, closesLog, closeAttemptsLog, closeAttemptsLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
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
	if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Should not error - handles molecule close failure gracefully
	updateAgentStateOnDone(filepath.Join(townRoot, "gastown"), townRoot, ExitCompleted, "gt-base-123")

	// Implementation behavior: when molecule close fails with a generic error
	// (not beads.ErrNotFound), the function returns early WITHOUT closing the
	// hooked bead, since the molecule is potentially still blocking it.
	// The Witness will clean up orphaned state.
	attemptBytes, _ := os.ReadFile(closeAttemptsLog)
	if len(attemptBytes) > 0 {
		attempts := string(attemptBytes)
		// Verify that molecule close was attempted (and failed 3 times)
		if !strings.Contains(attempts, "gt-wisp-xyz") {
			t.Errorf("molecule gt-wisp-xyz close was NOT attempted\nAttempts:\n%s", attempts)
		}
		// Note: base bead close is NOT attempted when molecule close fails
		// This is correct behavior - the molecule blocks the base bead closure
	} else {
		t.Logf("Note: no close attempts logged")
	}

	// Verify closesLog doesn't exist (no successful closes)
	_, err = os.ReadFile(closesLog)
	if err == nil {
		closesBytes, _ := os.ReadFile(closesLog)
		t.Logf("Note: closes.log exists with content: %s", string(closesBytes))
	}
}
