package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestShouldReuseExistingFormulaIgnoresLegacyWisps(t *testing.T) {
	wisp := &beads.Issue{
		ID:          "gt-wisp-existing",
		Ephemeral:   true,
		Labels:      []string{"gt:task"},
		Description: "attached_formula: mol-anything",
	}
	if shouldReuseExistingFormula(wisp, nil, false) {
		t.Fatal("legacy wisp must not be reused as durable formula dispatch")
	}

	unlabeled := &beads.Issue{ID: "gt-wisp-unlabeled", Description: "attached_formula: mol-anything"}
	if shouldReuseExistingFormula(unlabeled, nil, false) {
		t.Fatal("unlabeled hooked formula must not be reused after the durability fix")
	}

	durable := &beads.Issue{
		ID:          "gt-dispatch",
		Labels:      []string{formulaDispatchLabel},
		Description: "attached_formula: mol-anything",
	}
	if !shouldReuseExistingFormula(durable, nil, false) {
		t.Fatal("durable formula dispatch should still be reused")
	}
}

func TestRunSlingFormulaMigratesLegacyWispToDurableDispatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	_, logPath, attachedLogPath := setupFormulaSlingTown(t, `#!/bin/sh
set -e
echo "$PWD|$*" >> "${BD_LOG}"
cmd="$1"
shift || true
case "$cmd" in
  formula)
    echo '{"name":"mol-anything"}'
    ;;
  create)
    echo '{"id":"gt-dispatch-xyz","title":"mol-anything","status":"open"}'
    ;;
  cook|update)
    exit 0
    ;;
  mol)
    echo "unexpected new wisp after legacy hook" >&2
    exit 1
    ;;
esac
exit 0
`)

	prevFind := findHookedFormulaSingletonFn
	prevHook := hookBeadWithRetryFn
	prevDryRun, prevNoBoot := slingDryRun, slingNoBoot
	t.Cleanup(func() {
		findHookedFormulaSingletonFn = prevFind
		hookBeadWithRetryFn = prevHook
		slingDryRun, slingNoBoot = prevDryRun, prevNoBoot
	})

	slingDryRun = false
	slingNoBoot = true
	findHookedFormulaSingletonFn = func(workDir, targetAgent, formulaName string) (*beads.Issue, error) {
		return &beads.Issue{
			ID:          "gt-wisp-existing",
			Ephemeral:   true,
			Description: "attached_formula: mol-anything",
		}, nil
	}

	var hookedID string
	hookBeadWithRetryFn = func(beadID, targetAgent, hookDir string) error {
		hookedID = beadID
		return nil
	}

	if err := runSlingFormula(context.Background(), []string{"mol-anything"}); err != nil {
		t.Fatalf("runSlingFormula: %v", err)
	}

	if hookedID != "gt-dispatch-xyz" {
		t.Fatalf("hooked %q, want durable dispatch gt-dispatch-xyz", hookedID)
	}

	attachmentBytes, err := os.ReadFile(attachedLogPath)
	if err != nil {
		t.Fatalf("read attachment log: %v", err)
	}
	attachment := string(attachmentBytes)
	if !strings.Contains(attachment, "attached_molecule: gt-wisp-existing") {
		t.Fatalf("migrated dispatch must keep the legacy wisp as its molecule:\n%s", attachment)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	log := string(logBytes)
	if strings.Contains(log, "mol wisp") || strings.Contains(log, "cook ") {
		t.Fatalf("legacy wisp migration created a new formula instead of promoting the existing wisp:\n%s", log)
	}
	if !strings.Contains(log, "update gt-wisp-existing --status=open --assignee=") {
		t.Fatalf("legacy wisp remained hooked after migration:\n%s", log)
	}
}

func TestRunSlingFormulaHooksDurableDispatchNotWisp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	_, _, attachedLogPath := setupFormulaSlingTown(t, `#!/bin/sh
set -e
cmd="$1"
shift || true
case "$cmd" in
  formula)
    echo '{"name":"mol-anything"}'
    ;;
  create)
    echo '{"id":"gt-dispatch-xyz","title":"mol-anything","status":"open"}'
    ;;
  cook)
    exit 0
    ;;
  mol)
    echo '{"new_epic_id":"gt-wisp-xyz"}'
    ;;
esac
exit 0
`)

	prevHook := hookBeadWithRetryFn
	prevDryRun, prevNoBoot := slingDryRun, slingNoBoot
	t.Cleanup(func() {
		hookBeadWithRetryFn = prevHook
		slingDryRun, slingNoBoot = prevDryRun, prevNoBoot
	})
	slingDryRun = false
	slingNoBoot = true

	var hookedID string
	hookBeadWithRetryFn = func(beadID, targetAgent, hookDir string) error {
		hookedID = beadID
		return nil
	}

	if err := runSlingFormula(context.Background(), []string{"mol-anything"}); err != nil {
		t.Fatalf("runSlingFormula: %v", err)
	}
	if hookedID != "gt-dispatch-xyz" {
		t.Fatalf("hooked %q, want durable dispatch gt-dispatch-xyz", hookedID)
	}

	attachmentBytes, err := os.ReadFile(attachedLogPath)
	if err != nil {
		t.Fatalf("read attachment log: %v", err)
	}
	if !strings.Contains(string(attachmentBytes), "attached_molecule: gt-wisp-xyz") {
		t.Fatalf("durable dispatch missing molecule pointer:\n%s", attachmentBytes)
	}
}

func TestRunSlingFormulaCleansDispatchAndMoleculeOnHookFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()
	setupFormulaSlingWorkspace(t, townRoot)
	closesLog := filepath.Join(townRoot, "closes.log")
	writeFormulaSlingBDStub(t, townRoot, fmt.Sprintf(`#!/bin/sh
set -e
cmd="$1"
shift || true
case "$cmd" in
  formula)
    echo '{"name":"mol-anything"}'
    ;;
  create)
    echo '{"id":"gt-dispatch-xyz","title":"mol-anything","status":"open"}'
    ;;
  cook)
    exit 0
    ;;
  mol)
    echo '{"new_epic_id":"gt-wisp-xyz"}'
    ;;
  show)
    case "$1" in
      gt-dispatch-xyz)
        echo '[{"id":"gt-dispatch-xyz","status":"open","description":"attached_molecule: gt-wisp-xyz"}]'
        ;;
      gt-wisp-xyz)
        echo '[{"id":"gt-wisp-xyz","status":"open","ephemeral":true}]'
        ;;
      *)
        echo '[]'
        ;;
    esac
    ;;
  list)
    echo '[]'
    ;;
  close)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%s"
    done
    ;;
esac
exit 0
`, closesLog))

	t.Setenv("GT_TEST_ATTACHED_MOLECULE_LOG", "")
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
	prevHook := hookBeadWithRetryFn
	prevDryRun, prevNoBoot := slingDryRun, slingNoBoot
	t.Cleanup(func() {
		hookBeadWithRetryFn = prevHook
		slingDryRun, slingNoBoot = prevDryRun, prevNoBoot
	})
	slingDryRun = false
	slingNoBoot = true
	hookBeadWithRetryFn = func(beadID, targetAgent, hookDir string) error {
		return errors.New("hook failed")
	}

	if err := runSlingFormula(context.Background(), []string{"mol-anything"}); err == nil {
		t.Fatal("expected hook failure")
	}

	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("expected cleanup closes, got %v", err)
	}
	closes := string(closesBytes)
	if !strings.Contains(closes, "gt-dispatch-xyz") {
		t.Fatalf("failed sling did not close durable dispatch:\n%s", closes)
	}
	if !strings.Contains(closes, "gt-wisp-xyz") {
		t.Fatalf("failed sling did not close formula molecule:\n%s", closes)
	}
}

func TestUpdateAgentStateOnDoneDeferredClosesFormulaDispatchAndMolecule(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot, closesLog := setupFormulaDoneTown(t, `#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    case "$1" in
      gt-gastown-polecat-nux)
        echo '[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","agent_state":"working"}]'
        ;;
      gt-dispatch-1)
        echo '[{"id":"gt-dispatch-1","title":"mol-dog-reaper","status":"hooked","labels":["gt:task","gt:formula-dispatch"],"description":"attached_molecule: gt-wisp-xyz"}]'
        ;;
      gt-wisp-xyz)
        echo '[{"id":"gt-wisp-xyz","title":"mol-dog-reaper","status":"open","ephemeral":true}]'
        ;;
    esac
    ;;
  list)
    echo '[]'
    ;;
  close)
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
`)

	if err := updateAgentStateOnDone(filepath.Join(townRoot, "gastown"), townRoot, ExitDeferred, "gt-dispatch-1"); err != nil {
		t.Fatalf("updateAgentStateOnDone deferred formula: %v", err)
	}

	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("expected closes, got %v", err)
	}
	closes := string(closesBytes)
	if !strings.Contains(closes, "gt-dispatch-1") {
		t.Fatalf("DEFERRED formula completion did not close dispatch bead:\n%s", closes)
	}
	if !strings.Contains(closes, "gt-wisp-xyz") {
		t.Fatalf("DEFERRED formula completion did not close attached molecule:\n%s", closes)
	}
}

func TestUpdateAgentStateOnDoneDeferredShowFailureDoesNotFail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot, closesLog := setupFormulaDoneTown(t, `#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    case "$1" in
      gt-gastown-polecat-nux)
        echo '[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","agent_state":"working"}]'
        ;;
      gt-ordinary)
        echo "transient read error" >&2
        exit 1
        ;;
    esac
    ;;
  list)
    echo '[]'
    ;;
  close)
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
`)

	if err := updateAgentStateOnDone(filepath.Join(townRoot, "gastown"), townRoot, ExitDeferred, "gt-ordinary"); err != nil {
		t.Fatalf("ordinary DEFERRED completion must stay warning-only on Show failure, got %v", err)
	}
	if _, err := os.ReadFile(closesLog); !os.IsNotExist(err) {
		t.Fatalf("ordinary deferred work was closed after a classification read error")
	}
}

func setupFormulaSlingTown(t *testing.T, bdScript string) (townRoot, logPath, attachedLogPath string) {
	t.Helper()
	townRoot = t.TempDir()
	setupFormulaSlingWorkspace(t, townRoot)
	logPath = filepath.Join(townRoot, "bd.log")
	attachedLogPath = filepath.Join(townRoot, "attached-molecule.log")
	writeFormulaSlingBDStub(t, townRoot, bdScript)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("GT_TEST_ATTACHED_MOLECULE_LOG", attachedLogPath)
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
	return townRoot, logPath, attachedLogPath
}

func setupFormulaSlingWorkspace(t *testing.T, townRoot string) {
	t.Helper()
	for _, dir := range []string{
		filepath.Join(townRoot, "mayor", "rig"),
		filepath.Join(townRoot, ".beads"),
		filepath.Join(townRoot, "bin"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
}

func writeFormulaSlingBDStub(t *testing.T, townRoot, script string) {
	t.Helper()
	binDir := filepath.Join(townRoot, "bin")
	_ = writeBDStub(t, binDir, script, "@echo off\nexit /b 0\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func setupFormulaDoneTown(t *testing.T, bdScript string) (townRoot, closesLog string) {
	t.Helper()
	townRoot = t.TempDir()
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

	closesLog = filepath.Join(townRoot, "closes.log")
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	_ = writeBDStub(t, binDir, fmt.Sprintf(bdScript, closesLog), "@echo off\nexit /b 0\n")

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
	return townRoot, closesLog
}
