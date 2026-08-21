package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestAgentBeadsExistCheck_NoRoutes verifies the check handles missing routes.
func TestAgentBeadsExistCheck_NoRoutes(t *testing.T) {
	tmpDir := t.TempDir()

	// No .beads dir at all
	check := NewAgentBeadsCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	// With no routes, only global agents (deacon, mayor) are checked
	// They won't exist without Dolt, so we expect error
	t.Logf("Result: status=%v, message=%s", result.Status, result.Message)
	if result.Status == StatusOK {
		t.Error("expected error for missing global agent beads")
	}
}

// TestAgentBeadsExistCheck_NoRigs verifies the check handles empty routes.
func TestAgentBeadsExistCheck_NoRigs(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .beads dir with empty routes.jsonl
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewAgentBeadsCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	// With empty routes, only global agents (deacon, mayor) are checked
	// They won't exist without Dolt, so we expect error or warning
	t.Logf("Result: status=%v, message=%s", result.Status, result.Message)
}

func TestAgentBeadsExistCheckReportsLegacyTaskType(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fake bd")
	}
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	script := `#!/bin/sh
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done
case "$cmd" in
  version) printf 'bd test\n' ;;
  list) printf '[{"id":"hq-deacon","issue_type":"task","labels":["gt:agent"],"status":"open"},{"id":"hq-mayor","issue_type":"task","labels":["gt:agent"],"status":"open"}]\n' ;;
  mol) printf '{"wisps":[]}\n' ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result := NewAgentBeadsCheck().Run(&CheckContext{TownRoot: tmpDir})
	if result.Status != StatusWarning {
		t.Fatalf("status = %v, want warning; result=%#v", result.Status, result)
	}
	if !strings.Contains(result.Message, "require canonical type/label migration") {
		t.Fatalf("message = %q", result.Message)
	}
	if got := strings.Join(result.Details, ","); !strings.Contains(got, "hq-deacon") || !strings.Contains(got, "hq-mayor") {
		t.Fatalf("details = %v, want both legacy global agents", result.Details)
	}
}

// TestAgentBeadsExistCheck_ExpectedIDs verifies the check looks for correct agent bead IDs.
func TestAgentBeadsExistCheck_ExpectedIDs(t *testing.T) {
	tmpDir := t.TempDir()

	// Set up routes pointing to a rig with known prefix
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Use "sw" prefix to match sallaWork pattern
	routesContent := `{"prefix":"sw-","path":"sallaWork/mayor/rig"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create rig beads directory
	rigBeadsDir := filepath.Join(tmpDir, "sallaWork", "mayor", "rig", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	check := NewAgentBeadsCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	// Should report missing beads
	if result.Status == StatusOK {
		t.Errorf("expected error for missing agent beads, got: %s", result.Message)
	}

	// Should mention the expected bead IDs in details
	if len(result.Details) == 0 {
		t.Error("expected details to contain missing bead IDs")
	}

	// Verify the expected IDs are in the details
	expectedIDs := []string{"sw-sallaWork-witness", "sw-sallaWork-refinery"}
	for _, expectedID := range expectedIDs {
		found := false
		for _, detail := range result.Details {
			if detail == expectedID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected missing bead ID %s in details, got: %v", expectedID, result.Details)
		}
	}

	t.Logf("Result: status=%v, message=%s, details=%v", result.Status, result.Message, result.Details)
}

// TestAgentBeadsExistCheck_RespectsRigScope verifies that --rig excludes
// unrelated rig routes from agent-bead expectations.
func TestAgentBeadsExistCheck_RespectsRigScope(t *testing.T) {
	tmpDir := t.TempDir()

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	routesContent := strings.Join([]string{
		`{"prefix":"gs-","path":"gastown/mayor/rig"}`,
		`{"prefix":"do-","path":"coder_dotfiles/mayor/rig"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		filepath.Join(tmpDir, "gastown", "mayor", "rig", ".beads"),
		filepath.Join(tmpDir, "coder_dotfiles", "mayor", "rig", ".beads"),
	} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}

	check := NewAgentBeadsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: "gastown"}

	result := check.Run(ctx)

	if result.Status == StatusOK {
		t.Fatalf("expected missing agent beads for scoped rig, got OK")
	}
	for _, detail := range result.Details {
		if strings.HasPrefix(detail, "do-") {
			t.Fatalf("expected --rig scope to exclude coder_dotfiles agent bead %q, got details: %v", detail, result.Details)
		}
	}
	foundGastown := false
	for _, detail := range result.Details {
		if strings.HasPrefix(detail, "gs-") {
			foundGastown = true
			break
		}
	}
	if !foundGastown {
		t.Fatalf("expected scoped result to include gastown agent beads, got details: %v", result.Details)
	}
}

// TestAgentBeadsExistCheck_FixRespectsRigScope verifies that --fix with a rig
// scope does not create agent beads for unrelated rig prefixes.
func TestAgentBeadsExistCheck_FixRespectsRigScope(t *testing.T) {
	tmpDir := t.TempDir()

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	routesContent := strings.Join([]string{
		`{"prefix":"gs-","path":"gastown/mayor/rig"}`,
		`{"prefix":"do-","path":"coder_dotfiles/mayor/rig"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		filepath.Join(tmpDir, "gastown", "mayor", "rig", ".beads"),
		filepath.Join(tmpDir, "coder_dotfiles", "mayor", "rig", ".beads"),
	} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "gastown", "crew", "alice", ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "coder_dotfiles", "crew", "bella", ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	logFile := filepath.Join(tmpDir, "bd.log")
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	bdScript := filepath.Join(binDir, "bd")
	script := `#!/usr/bin/env bash
set -euo pipefail

logfile="` + logFile + `"

args=()
for arg in "$@"; do
  if [[ "$arg" == --allow-stale ]]; then
    continue
  fi
  args+=("$arg")
done

cmd=""
idx=0
for i in "${!args[@]}"; do
  if [[ "${args[$i]}" != -* ]]; then
    cmd="${args[$i]}"
    idx=$i
    break
  fi
done

if [[ -z "$cmd" ]]; then
  exit 0
fi

rest=("${args[@]:$((idx + 1))}")

case "$cmd" in
  list)
    printf '[]\n'
    ;;
  mol)
    if [[ "${rest[0]:-}" == "wisp" && "${rest[1]:-}" == "list" ]]; then
      printf '{"wisps":[]}\n'
      exit 0
    fi
    exit 1
    ;;
  show)
    exit 1
    ;;
  create)
    id=""
    title=""
    for arg in "${rest[@]}"; do
      case "$arg" in
        --id=*) id="${arg#--id=}" ;;
        --title=*) title="${arg#--title=}" ;;
      esac
    done
    printf 'create %s\n' "$id" >> "$logfile"
    printf '{"id":"%s","title":"%s","status":"open","labels":["gt:agent"]}\n' "$id" "$title"
    ;;
  update)
    if [[ ${#rest[@]} -gt 0 ]]; then
      printf 'update %s\n' "${rest[0]}" >> "$logfile"
    fi
    printf '{}'\n
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(bdScript, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", fmt.Sprintf("%s%c%s", binDir, os.PathListSeparator, os.Getenv("PATH")))

	check := NewAgentBeadsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: "gastown"}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() returned error: %v", err)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("reading fake bd log: %v", err)
	}
	log := string(data)
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		if strings.Contains(line, " do-") {
			t.Fatalf("expected scoped Fix() to avoid coder_dotfiles beads, got log line %q", line)
		}
	}
	if !strings.Contains(log, "create gs-gastown-witness") {
		t.Fatalf("expected scoped Fix() to create gastown witness bead, got log: %q", log)
	}
}

// TestListCrewWorkers_FiltersWorktrees verifies that listCrewWorkers skips
// git worktrees (directories where .git is a file) and only returns canonical
// crew workers (where .git is a directory). This is the fix for GH#2767.
func TestListCrewWorkers_FiltersWorktrees(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "myrig"
	crewDir := filepath.Join(tmpDir, rigName, "crew")

	// Create a canonical crew worker: .git is a directory
	canonicalDir := filepath.Join(crewDir, "alice")
	if err := os.MkdirAll(filepath.Join(canonicalDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create a worktree: .git is a file (contains gitdir pointer)
	worktreeDir := filepath.Join(crewDir, "alice-worktree")
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, ".git"),
		[]byte("gitdir: /path/to/main/.git/worktrees/alice-worktree\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a second canonical worker
	bobDir := filepath.Join(crewDir, "bob")
	if err := os.MkdirAll(filepath.Join(bobDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create a directory without .git at all (should be included — not a worktree)
	plainDir := filepath.Join(crewDir, "charlie")
	if err := os.MkdirAll(plainDir, 0755); err != nil {
		t.Fatal(err)
	}

	workers := listCrewWorkers(tmpDir, rigName)

	// Should include alice, bob, charlie but NOT alice-worktree
	expected := map[string]bool{"alice": false, "bob": false, "charlie": false}
	for _, w := range workers {
		if w == "alice-worktree" {
			t.Errorf("listCrewWorkers should skip worktree 'alice-worktree', got: %v", workers)
		}
		if _, ok := expected[w]; ok {
			expected[w] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("listCrewWorkers should include canonical worker %q, got: %v", name, workers)
		}
	}
}

// TestListPolecats_FiltersWorktrees verifies that listPolecats skips
// git worktrees, same as listCrewWorkers. See GH#2767.
func TestListPolecats_FiltersWorktrees(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "myrig"
	polecatDir := filepath.Join(tmpDir, rigName, "polecats")

	// Canonical polecat
	if err := os.MkdirAll(filepath.Join(polecatDir, "scout", ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Worktree polecat (.git is a file)
	wtDir := filepath.Join(polecatDir, "scout-wt")
	if err := os.MkdirAll(wtDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, ".git"),
		[]byte("gitdir: /path/to/main/.git/worktrees/scout-wt\n"), 0644); err != nil {
		t.Fatal(err)
	}

	polecats := listPolecats(tmpDir, rigName)

	if len(polecats) != 1 || polecats[0] != "scout" {
		t.Errorf("listPolecats should return only [scout], got: %v", polecats)
	}
}
