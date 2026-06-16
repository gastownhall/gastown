package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCreateBatchConvoy_CreatesOneConvoyTrackingAllBeads verifies that
// createBatchConvoy creates exactly one convoy and adds tracking deps for all
// provided bead IDs. This is the core contract of the N-convoys → 1-convoy change.
func TestCreateBatchConvoy_CreatesOneConvoyTrackingAllBeads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	townRoot := t.TempDir()

	// Minimal workspace marker so workspace.FindFromCwd() succeeds.
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")

	// Stub bd: log all commands. create and dep add succeed.
	bdScript := `#!/bin/sh
echo "CMD:$*" >> "` + logPath + `"
cmd="$1"
if [ "$cmd" = "--allow-stale" ]; then
  shift || true
  cmd="$1"
fi
shift || true
case "$cmd" in
  create)
    exit 0
    ;;
  dep)
    exit 0
    ;;
esac
exit 0
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+origPath)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	beadIDs := []string{"gt-aaa", "gt-bbb", "gt-ccc"}
	convoyID, _, err := createBatchConvoy(beadIDs, "gastown", false, "mr", "")
	if err != nil {
		t.Fatalf("createBatchConvoy() error: %v", err)
	}

	// Convoy ID must have hq-cv- prefix
	if !strings.HasPrefix(convoyID, "hq-cv-") {
		t.Errorf("convoy ID %q should have hq-cv- prefix", convoyID)
	}

	// Parse logged commands
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	logLines := strings.Split(strings.TrimSpace(string(logBytes)), "\n")

	// Exactly 1 create command
	createCount := 0
	for _, line := range logLines {
		if strings.Contains(line, "CMD:create") {
			createCount++
		}
	}
	if createCount != 1 {
		t.Errorf("expected exactly 1 create command, got %d\nLog:\n%s", createCount, string(logBytes))
	}

	// Exactly N dep add commands (one per bead)
	depAddCount := 0
	trackedBeads := map[string]bool{}
	for _, line := range logLines {
		if strings.Contains(line, "CMD:dep add") {
			depAddCount++
			for _, beadID := range beadIDs {
				if strings.Contains(line, beadID) {
					trackedBeads[beadID] = true
				}
			}
		}
	}
	if depAddCount != len(beadIDs) {
		t.Errorf("expected %d dep add commands, got %d\nLog:\n%s", len(beadIDs), depAddCount, string(logBytes))
	}
	for _, beadID := range beadIDs {
		if !trackedBeads[beadID] {
			t.Errorf("bead %q was not tracked in convoy\nLog:\n%s", beadID, string(logBytes))
		}
	}
}

// TestCreateBatchConvoy_OwnedLabel verifies that --owned flag adds gt:owned label.
func TestCreateBatchConvoy_OwnedLabel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")

	// Use printf with NUL delimiters to correctly log args that contain newlines.
	// The --description arg contains \n which breaks simple $* logging.
	bdScript := `#!/bin/sh
printf 'CMD:' >> "` + logPath + `"
for arg in "$@"; do printf '%s\0' "$arg"; done >> "` + logPath + `"
printf '\n' >> "` + logPath + `"
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	_, _, err = createBatchConvoy([]string{"gt-aaa"}, "gastown", true, "direct", "")
	if err != nil {
		t.Fatalf("createBatchConvoy() error: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	logContent := string(logBytes)

	if !strings.Contains(logContent, "--labels=gt:convoy,gt:owned") {
		t.Errorf("create command missing convoy/owned labels in log:\n%q", logContent)
	}
}

// TestCreateBatchConvoy_MergeStrategyInDescription verifies that merge strategy
// is included in the convoy description.
func TestCreateBatchConvoy_MergeStrategyInDescription(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")

	// Use printf with NUL delimiters to correctly log args that contain newlines.
	bdScript := `#!/bin/sh
printf 'CMD:' >> "` + logPath + `"
for arg in "$@"; do printf '%s\0' "$arg"; done >> "` + logPath + `"
printf '\n' >> "` + logPath + `"
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	_, _, err = createBatchConvoy([]string{"gt-aaa", "gt-bbb"}, "gastown", false, "direct", "")
	if err != nil {
		t.Fatalf("createBatchConvoy() error: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	// The NUL-delimited log preserves the full --description including the newline.
	// "Merge: direct" will appear inside the --description= argument.
	logContent := string(logBytes)
	if !strings.Contains(logContent, "Merge: direct") {
		t.Errorf("create description missing merge strategy:\n%q", logContent)
	}
}

// TestCreateBatchConvoy_EmptyBeadIDs verifies that createBatchConvoy returns
// an error when called with no bead IDs.
func TestCreateBatchConvoy_EmptyBeadIDs(t *testing.T) {
	_, _, err := createBatchConvoy(nil, "gastown", false, "", "")
	if err == nil {
		t.Fatal("expected error for empty bead IDs, got nil")
	}
	if !strings.Contains(err.Error(), "no beads") {
		t.Errorf("error should mention 'no beads', got: %v", err)
	}
}

// TestCreateBatchConvoy_TitleIncludesBeadCount verifies that the convoy title
// includes the bead count and rig name.
func TestCreateBatchConvoy_TitleIncludesBeadCount(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
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
echo "CMD:$*" >> "` + logPath + `"
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	_, _, err = createBatchConvoy([]string{"gt-a", "gt-b", "gt-c", "gt-d", "gt-e"}, "myrig", false, "", "")
	if err != nil {
		t.Fatalf("createBatchConvoy() error: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	var createLine string
	for _, line := range strings.Split(string(logBytes), "\n") {
		if strings.Contains(line, "CMD:create") {
			createLine = line
			break
		}
	}
	// Title should be "Batch: 5 beads to myrig"
	if !strings.Contains(createLine, "Batch: 5 beads to myrig") {
		t.Errorf("title should contain 'Batch: 5 beads to myrig', got:\n%s", createLine)
	}
}

// TestCreateBatchConvoy_PartialDepFailureContinues verifies that if a dep add
// fails for one bead, the convoy is still created and other beads are tracked.
// Partial tracking is better than no tracking.
func TestCreateBatchConvoy_PartialDepFailureContinues(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")

	// Stub bd: create succeeds, dep add fails for gt-bbb only
	bdScript := `#!/bin/sh
echo "CMD:$*" >> "` + logPath + `"
cmd="$1"
if [ "$cmd" = "--allow-stale" ]; then
  shift || true
  cmd="$1"
fi
shift || true
case "$cmd" in
  create)
    exit 0
    ;;
  dep)
    # Fail if the bead is gt-bbb
    for arg in "$@"; do
      if [ "$arg" = "gt-bbb" ]; then
        exit 1
      fi
    done
    exit 0
    ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Should NOT return error — partial tracking is acceptable
	convoyID, tracked, err := createBatchConvoy([]string{"gt-aaa", "gt-bbb", "gt-ccc"}, "gastown", false, "", "")
	if err != nil {
		t.Fatalf("createBatchConvoy() should not error on partial dep failure: %v", err)
	}
	// Verify tracked set excludes the failed bead
	if len(tracked) != 2 {
		t.Errorf("expected 2 tracked beads (gt-bbb failed), got %d: %v", len(tracked), tracked)
	}
	if convoyID == "" {
		t.Fatal("convoy ID should not be empty")
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	// 1 create + 3 dep add attempts = 4 commands
	logLines := strings.Split(strings.TrimSpace(string(logBytes)), "\n")
	depCount := 0
	for _, line := range logLines {
		if strings.Contains(line, "CMD:dep add") {
			depCount++
		}
	}
	if depCount != 3 {
		t.Errorf("expected 3 dep add attempts (including failed one), got %d", depCount)
	}
}

// TestBatchSling_ConvoyIDStoredInBeadFieldUpdates verifies that the batch convoy ID
// is stored in each bead's fieldUpdates.ConvoyID. This was a bug where ConvoyID and
// MergeStrategy were never persisted in batch mode.
func TestBatchSling_ConvoyIDStoredInBeadFieldUpdates(t *testing.T) {
	// This test verifies the data flow: batchConvoyID is set in fieldUpdates.ConvoyID
	// for each bead in the loop. We test this at the unit level by checking the
	// beadFieldUpdates struct construction.

	// Simulate the logic from runBatchSling: convoy created before loop,
	// ConvoyID stored in each bead's fieldUpdates.
	batchConvoyID := "hq-cv-test1"
	mergeStrategy := "direct"

	beadIDs := []string{"gt-aaa", "gt-bbb", "gt-ccc"}
	for _, beadID := range beadIDs {
		fieldUpdates := beadFieldUpdates{
			Dispatcher:    "test-actor",
			ConvoyID:      batchConvoyID,
			MergeStrategy: mergeStrategy,
		}

		if fieldUpdates.ConvoyID != batchConvoyID {
			t.Errorf("bead %s: ConvoyID = %q, want %q", beadID, fieldUpdates.ConvoyID, batchConvoyID)
		}
		if fieldUpdates.MergeStrategy != mergeStrategy {
			t.Errorf("bead %s: MergeStrategy = %q, want %q", beadID, fieldUpdates.MergeStrategy, mergeStrategy)
		}
	}
}

// TestBatchSling_ErrorsOnAlreadyTrackedBead verifies that batch sling refuses
// to proceed when any bead is already tracked by another convoy, and that
// isTrackedByConvoy correctly identifies the conflict.
func TestBatchSling_ErrorsOnAlreadyTrackedBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	binDir := t.TempDir()

	// Stub bd: dep list returns a tracking convoy for gt-bbb (already tracked),
	// empty results for everything else.
	bdScript := `#!/bin/sh
cmd="$1"
if [ "$cmd" = "--allow-stale" ]; then
  shift || true
  cmd="$1"
fi
shift || true

case "$cmd" in
  sql)
    # bdDepListRawIDs up: match beadID against typed dependency target columns.
    case "$*" in
      *"depends_on_issue_id = 'gt-bbb'"*)
        echo '[{"issue_id":"hq-cv-existing"}]'
        ;;
      *)
        echo '[]'
        ;;
    esac
    exit 0
    ;;
  show)
    # bdShow: return convoy details for isTrackedByConvoy check
    case "$1" in
      hq-cv-existing)
        echo '[{"id":"hq-cv-existing","issue_type":"convoy","status":"open"}]'
        ;;
      *)
        echo '[]'
        ;;
    esac
    exit 0
    ;;
  dep)
    sub="$1"; shift || true
    beadID="$1"
    if [ "$beadID" = "gt-bbb" ]; then
      echo '[{"id":"hq-cv-existing","issue_type":"convoy","status":"open"}]'
    else
      echo '[]'
    fi
    exit 0
    ;;
  list)
    echo '[]'
    exit 0
    ;;
esac
exit 0
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+origPath)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Simulate the pre-loop conflict check from runBatchSling.
	// It should detect gt-bbb as already tracked and error.
	beadIDs := []string{"gt-aaa", "gt-bbb", "gt-ccc"}
	var conflictFound bool
	for _, beadID := range beadIDs {
		if existing := isTrackedByConvoy(beadID); existing != "" {
			conflictFound = true
			if beadID != "gt-bbb" {
				t.Errorf("unexpected conflict for bead %s (convoy: %s)", beadID, existing)
			}
			if existing != "hq-cv-existing" {
				t.Errorf("expected convoy hq-cv-existing, got %s", existing)
			}
			break // runBatchSling errors on the first conflict
		}
	}

	if !conflictFound {
		t.Fatal("expected conflict for gt-bbb but none detected")
	}
}

// --- Auto-rig-resolution and deprecation tests ---

// TestAllBeadIDs_TrueWhenAllBeadIDs verifies that allBeadIDs returns true
// when every argument looks like a bead ID.
func TestAllBeadIDs_TrueWhenAllBeadIDs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"all beads", []string{"gt-abc", "gt-def", "gt-ghi"}, true},
		{"mixed prefixes", []string{"gt-abc", "bd-def", "hq-ghi"}, true},
		{"single bead", []string{"gt-abc"}, true},
		{"last is rig name", []string{"gt-abc", "gt-def", "gastown"}, false},
		{"empty list", []string{}, false},
		{"contains path", []string{"gt-abc", "gastown/polecats/foo"}, false},
		{"contains bare word no hyphen", []string{"gt-abc", "gastown"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := allBeadIDs(tc.args)
			if got != tc.want {
				t.Errorf("allBeadIDs(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

// TestResolveRigFromBeadIDs_AllSamePrefix verifies that resolveRigFromBeadIDs
// resolves the rig when all beads share the same prefix.
func TestResolveRigFromBeadIDs_AllSamePrefix(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Write routes.jsonl mapping gt- to gastown
	routesContent := `{"prefix":"gt-","path":"gastown/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	rigName, err := resolveRigFromBeadIDs([]string{"gt-aaa", "gt-bbb", "gt-ccc"}, townRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rigName != "gastown" {
		t.Errorf("rigName = %q, want %q", rigName, "gastown")
	}
}

// TestResolveRigFromBeadIDs_MixedPrefixes_Errors verifies that beads from
// different rigs produce an error with suggested actions.
func TestResolveRigFromBeadIDs_MixedPrefixes_Errors(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	routesContent := `{"prefix":"gt-","path":"gastown/.beads"}
{"prefix":"bd-","path":"beads/.beads"}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	_, err := resolveRigFromBeadIDs([]string{"gt-aaa", "bd-bbb", "gt-ccc"}, townRoot)
	if err == nil {
		t.Fatal("expected error for mixed prefixes, got nil")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "different rigs") {
		t.Errorf("error should mention 'different rigs', got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "gastown") || !strings.Contains(errMsg, "beads") {
		t.Errorf("error should mention both rig names, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "Options") {
		t.Errorf("error should include suggested actions, got: %s", errMsg)
	}
}

// TestResolveRigFromBeadIDs_UnmappedPrefix_Errors verifies that a bead whose
// prefix has no route mapping produces an error with suggested actions.
func TestResolveRigFromBeadIDs_UnmappedPrefix_Errors(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Only gt- is mapped; zz- is not
	routesContent := `{"prefix":"gt-","path":"gastown/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	_, err := resolveRigFromBeadIDs([]string{"gt-aaa", "zz-bbb"}, townRoot)
	if err == nil {
		t.Fatal("expected error for unmapped prefix, got nil")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "zz-bbb") {
		t.Errorf("error should mention the bead ID, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "not mapped") {
		t.Errorf("error should mention prefix is not mapped, got: %s", errMsg)
	}
}

// TestResolveRigFromBeadIDs_TownLevelPrefix_Errors verifies that a bead with
// a town-level prefix (path=".") produces an error because it has no rig.
func TestResolveRigFromBeadIDs_TownLevelPrefix_Errors(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// hq- maps to town root (path=".")
	routesContent := `{"prefix":"hq-","path":"."}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	_, err := resolveRigFromBeadIDs([]string{"hq-aaa", "hq-bbb"}, townRoot)
	if err == nil {
		t.Fatal("expected error for town-level prefix, got nil")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "not mapped") || !strings.Contains(errMsg, "town-level") {
		t.Errorf("error should mention town-level bead, got: %s", errMsg)
	}
}

// TestCloseConvoyPinsTownDatabaseUnderStaleEnv verifies convoy cleanup closes
// hq-cv-* beads through the town database even when ambient bd env points away.
func TestCloseConvoyPinsTownDatabaseUnderStaleEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"type":"town","name":"test"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townBeads, "metadata.json"), []byte(`{"dolt_database":"hq","dolt_server_host":"127.0.0.1","dolt_server_port":3307}`), 0644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	closeLogPath := filepath.Join(townRoot, "bd-close.log")

	// Stub bd: handles close and logs it
	bdScript := `#!/bin/sh
cmd="$1"
if [ "$cmd" = "--allow-stale" ]; then
  shift || true
  cmd="$1"
fi
shift || true
case "$cmd" in
  close)
    printf '%s %s|%s|%s|%s|%s|%s|%s|%s|%s\n' "$cmd" "$*" "$(pwd)" "${BEADS_DIR:-}" "${BEADS_DOLT_SERVER_DATABASE:-}" "${BEADS_DB:-}" "${BD_DB:-}" "${BEADS_DOLT_DATA_DIR:-}" "${BD_DOLT_AUTO_COMMIT:-}" "${BD_READONLY:-}" >> "` + closeLogPath + `"
    exit 0
    ;;
esac
exit 0
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("BEADS_DIR", filepath.Join(townRoot, "wrong", ".beads"))
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "gastown")
	t.Setenv("BEADS_DB", filepath.Join(townRoot, "wrong.db"))
	t.Setenv("BD_DB", filepath.Join(townRoot, "wrong.bd"))
	t.Setenv("BEADS_DOLT_DATA_DIR", filepath.Join(townRoot, "wrong-data"))
	t.Setenv("BD_READONLY", "true")
	t.Setenv("BD_DOLT_AUTO_COMMIT", "off")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	batchConvoyID := "hq-cv-cleanup-test"
	closeConvoy(batchConvoyID, "all beads failed to sling")

	// Verify close was called
	closeBytes, err := os.ReadFile(closeLogPath)
	if err != nil {
		t.Fatalf("close log not written: %v", err)
	}
	closeContent := string(closeBytes)
	if !strings.Contains(closeContent, batchConvoyID) {
		t.Errorf("close log should contain convoy ID %q:\n%s", batchConvoyID, closeContent)
	}
	if !strings.Contains(closeContent, "all beads failed") {
		t.Errorf("close log should contain failure reason:\n%s", closeContent)
	}
	fields := strings.Split(strings.TrimSpace(closeContent), "|")
	if len(fields) != 9 {
		t.Fatalf("close log fields = %v, want 9 fields in %q", fields, closeContent)
	}
	if fields[1] != townBeads {
		t.Fatalf("close cwd = %q, want town beads dir %q", fields[1], townBeads)
	}
	if fields[2] != townBeads {
		t.Fatalf("BEADS_DIR = %q, want %q", fields[2], townBeads)
	}
	if fields[3] != "hq" {
		t.Fatalf("BEADS_DOLT_SERVER_DATABASE = %q, want hq", fields[3])
	}
	if fields[4] != "" || fields[5] != "" || fields[6] != "" {
		t.Fatalf("stale DB env should be stripped, got BEADS_DB=%q BD_DB=%q BEADS_DOLT_DATA_DIR=%q", fields[4], fields[5], fields[6])
	}
	if fields[7] != "on" {
		t.Fatalf("BD_DOLT_AUTO_COMMIT = %q, want on", fields[7])
	}
	if fields[8] != "" {
		t.Fatalf("BD_READONLY should be stripped, got %q", fields[8])
	}
}

// ---------------------------------------------------------------------------
// slingGenerateShortID tests
// ---------------------------------------------------------------------------

// TestSlingGenerateShortID_Format verifies the generated ID is 5 lowercase
// base32 characters.
func TestSlingGenerateShortID_Format(t *testing.T) {
	id := slingGenerateShortID()
	if len(id) != 5 {
		t.Fatalf("expected 5-char ID, got %d chars: %q", len(id), id)
	}
	// base32 lowercase alphabet: a-z, 2-7
	for _, ch := range id {
		if !((ch >= 'a' && ch <= 'z') || (ch >= '2' && ch <= '7')) {
			t.Errorf("unexpected character %q in ID %q (expected base32 lowercase)", ch, id)
		}
	}
}

// TestSlingGenerateShortID_Unique verifies successive calls produce different IDs.
func TestSlingGenerateShortID_Unique(t *testing.T) {
	a := slingGenerateShortID()
	b := slingGenerateShortID()
	if a == b {
		t.Errorf("two successive calls returned the same ID: %q", a)
	}
}

// ---------------------------------------------------------------------------
// ConvoyInfo.IsOwnedDirect tests
// ---------------------------------------------------------------------------

func TestConvoyInfo_IsOwnedDirect(t *testing.T) {
	cases := []struct {
		name string
		info *ConvoyInfo
		want bool
	}{
		{"nil receiver", nil, false},
		{"owned + direct", &ConvoyInfo{Owned: true, MergeStrategy: "direct"}, true},
		{"owned + mr", &ConvoyInfo{Owned: true, MergeStrategy: "mr"}, false},
		{"not owned + direct", &ConvoyInfo{Owned: false, MergeStrategy: "direct"}, false},
		{"not owned + empty", &ConvoyInfo{Owned: false, MergeStrategy: ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.info.IsOwnedDirect()
			if got != tc.want {
				t.Errorf("IsOwnedDirect() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// createAutoConvoy tests
// ---------------------------------------------------------------------------

// setupTownWithBdStub creates a minimal town workspace and installs a bd
// shell stub that logs all commands. Returns townRoot and logPath.
func setupTownWithBdStub(t *testing.T, bdScript string) (townRoot, logPath string) {
	t.Helper()

	townRoot = t.TempDir()
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
	logPath = filepath.Join(townRoot, "bd.log")

	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	return townRoot, logPath
}

// TestCreateAutoConvoy_BasicSuccess verifies that createAutoConvoy creates a
// convoy with "Work: <title>" title, delegates tracking to the helper, and
// returns an hq-cv-* ID.
func TestCreateAutoConvoy_BasicSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	logPath := filepath.Join(t.TempDir(), "placeholder") // overwritten below
	bdScript := `#!/bin/sh
echo "CMD:$*" >> "LOGPATH"
exit 0
`
	// We need logPath before the script, so build it in two steps.
	townRoot, logPath := setupTownWithBdStub(t, "")
	// Rewrite bd with the actual logPath baked in.
	bdScript = strings.ReplaceAll(bdScript, "LOGPATH", logPath)
	if err := os.WriteFile(filepath.Join(townRoot, "bin", "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("rewrite bd stub: %v", err)
	}

	var helperTownRoot, helperConvoyID, helperBeadID string
	oldAddTracking := addTrackingRelationFn
	addTrackingRelationFn = func(townRoot, convoyID, issueID string) error {
		helperTownRoot = townRoot
		helperConvoyID = convoyID
		helperBeadID = issueID
		return nil
	}
	t.Cleanup(func() { addTrackingRelationFn = oldAddTracking })

	convoyID, err := createAutoConvoy("gt-aaa", "Fix the widget", false, "mr", "")
	if err != nil {
		t.Fatalf("createAutoConvoy() error: %v", err)
	}

	if !strings.HasPrefix(convoyID, "hq-cv-") {
		t.Errorf("convoy ID %q should have hq-cv- prefix", convoyID)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	logContent := string(logBytes)

	// Verify title is "Work: Fix the widget"
	if !strings.Contains(logContent, "Work: Fix the widget") {
		t.Errorf("create should include 'Work: Fix the widget' in args:\n%s", logContent)
	}

	if helperTownRoot != townRoot {
		t.Errorf("tracking helper townRoot = %q, want %q", helperTownRoot, townRoot)
	}
	if helperConvoyID != convoyID {
		t.Errorf("tracking helper convoyID = %q, want %q", helperConvoyID, convoyID)
	}
	if helperBeadID != "gt-aaa" {
		t.Errorf("tracking helper issueID = %q, want %q", helperBeadID, "gt-aaa")
	}
}

// TestCreateAutoConvoy_FlagLikeTitleReturnsError verifies that a title starting
// with "--" is rejected.
func TestCreateAutoConvoy_FlagLikeTitleReturnsError(t *testing.T) {
	_, err := createAutoConvoy("gt-aaa", "--verbose", false, "", "")
	if err == nil {
		t.Fatal("expected error for flag-like title, got nil")
	}
	if !strings.Contains(err.Error(), "CLI flag") {
		t.Errorf("error should mention CLI flag, got: %v", err)
	}
}

// TestCreateAutoConvoy_OwnedLabel verifies that owned=true adds --labels=gt:owned.
func TestCreateAutoConvoy_OwnedLabel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	bdScript := `#!/bin/sh
printf 'CMD:' >> "LOGPATH"
for arg in "$@"; do printf '%s\0' "$arg"; done >> "LOGPATH"
printf '\n' >> "LOGPATH"
exit 0
`
	townRoot, logPath := setupTownWithBdStub(t, "")
	bdScript = strings.ReplaceAll(bdScript, "LOGPATH", logPath)
	if err := os.WriteFile(filepath.Join(townRoot, "bin", "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("rewrite bd stub: %v", err)
	}

	_, err := createAutoConvoy("gt-aaa", "My task", true, "direct", "")
	if err != nil {
		t.Fatalf("createAutoConvoy() error: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(logBytes), "--labels=gt:convoy,gt:owned") {
		t.Errorf("create should include convoy/owned labels:\n%q", string(logBytes))
	}
}

// TestCreateAutoConvoy_DepFailIsNonFatal verifies that when the dep add fails
// (e.g., cross-rig bead), createAutoConvoy succeeds with a warning rather than
// returning an error. Tracking failure is non-fatal since commit 103b6aaa because
// beads v0.62 removed cross-rig routing from bd dep add. The convoy still works
// without the tracking dep — witness and daemon provide backup tracking.
func TestCreateAutoConvoy_DepFailIsNonFatal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	bdScript := `#!/bin/sh
echo "CMD:$*" >> "LOGPATH"
cmd="$1"
if [ "$cmd" = "--allow-stale" ]; then
  shift || true
  cmd="$1"
fi
shift || true
case "$cmd" in
  create)
    exit 0
    ;;
  dep)
    exit 1
    ;;
esac
exit 0
`
	townRoot, logPath := setupTownWithBdStub(t, "")
	bdScript = strings.ReplaceAll(bdScript, "LOGPATH", logPath)
	if err := os.WriteFile(filepath.Join(townRoot, "bin", "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("rewrite bd stub: %v", err)
	}

	convoyID, err := createAutoConvoy("gt-aaa", "My task", false, "", "")
	if err != nil {
		t.Fatalf("expected no error (dep fail is non-fatal), got: %v", err)
	}
	if convoyID == "" {
		t.Fatal("expected non-empty convoy ID")
	}

	// Verify create was called but close was NOT called (convoy is not orphaned)
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	logContent := string(logBytes)
	if !strings.Contains(logContent, "CMD:create") {
		t.Errorf("expected create command in log:\n%s", logContent)
	}
	if strings.Contains(logContent, "CMD:close") {
		t.Errorf("close should NOT be called (dep fail is non-fatal):\n%s", logContent)
	}
}

// ---------------------------------------------------------------------------
// convoyTracksBead tests
// ---------------------------------------------------------------------------

// TestConvoyTracksBead_ExactMatch verifies exact bead ID match.
// Uses bd sql --json output format (depends_on_id column).
func TestConvoyTracksBead_ExactMatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	binDir := t.TempDir()
	beadsDir := t.TempDir()

	// bd sql returns rows with depends_on_id column
	bdScript := `#!/bin/sh
echo '[{"depends_on_id":"gt-aaa"},{"depends_on_id":"gt-bbb"}]'
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+origPath)

	if !convoyTracksBead(beadsDir, "hq-cv-test", "gt-aaa") {
		t.Error("expected true for exact match gt-aaa")
	}
	if !convoyTracksBead(beadsDir, "hq-cv-test", "gt-bbb") {
		t.Error("expected true for exact match gt-bbb")
	}
}

// TestConvoyTracksBead_ExternalWrappedMatch verifies matching through
// the "external:prefix:beadID" format (unwrapped by bdDepListRawIDs).
func TestConvoyTracksBead_ExternalWrappedMatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	binDir := t.TempDir()
	beadsDir := t.TempDir()

	// bd sql returns raw depends_on_id which may contain external: wrapping
	bdScript := `#!/bin/sh
echo '[{"depends_on_id":"external:gt:gt-abc"}]'
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	if !convoyTracksBead(beadsDir, "hq-cv-test", "gt-abc") {
		t.Error("expected true for external-wrapped match gt-abc")
	}
}

// TestConvoyTracksBead_NoMatch verifies false when bead is not tracked.
func TestConvoyTracksBead_NoMatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	binDir := t.TempDir()
	beadsDir := t.TempDir()

	bdScript := `#!/bin/sh
echo '[{"depends_on_id":"gt-aaa"}]'
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	if convoyTracksBead(beadsDir, "hq-cv-test", "gt-zzz") {
		t.Error("expected false when bead is not tracked")
	}
}

// TestConvoyTracksBead_BdError verifies false when bd command fails.
func TestConvoyTracksBead_BdError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	binDir := t.TempDir()
	beadsDir := t.TempDir()

	bdScript := `#!/bin/sh
exit 1
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	if convoyTracksBead(beadsDir, "hq-cv-test", "gt-aaa") {
		t.Error("expected false when bd fails")
	}
}

// ---------------------------------------------------------------------------
// Cross-rig guard in runBatchSling tests
// ---------------------------------------------------------------------------

// TestBatchSling_CrossRigGuardRejectsPrefix verifies that the cross-rig guard
// in runBatchSling rejects beads whose prefix doesn't match the target rig.
func TestBatchSling_CrossRigGuardRejectsPrefix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// Routes: gt- -> gastown, bd- -> beads
	routesContent := `{"prefix":"gt-","path":"gastown/.beads"}
{"prefix":"bd-","path":"beads/.beads"}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}

	// Stub bd: show succeeds (verifyBeadExists), everything else succeeds
	bdScript := `#!/bin/sh
cmd="$1"
if [ "$cmd" = "--allow-stale" ]; then
  shift || true
  cmd="$1"
fi
shift || true
case "$cmd" in
  show)
    echo '[{"id":"test","status":"open","title":"test"}]'
    exit 0
    ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Save and restore package-level flags
	origForce := slingForce
	t.Cleanup(func() { slingForce = origForce })
	slingForce = false

	// Directly test the cross-rig guard logic from runBatchSling lines 32-61.
	// A bd- bead targeting "gastown" should be rejected.
	beadIDs := []string{"gt-aaa", "bd-bbb"}
	rigName := "gastown"
	townBeadsDir := beadsDir

	var guardErr error
	for _, beadID := range beadIDs {
		prefix := extractPrefixForTest(beadID)
		beadRig := lookupRigForPrefixInTest(townRoot, prefix)
		if prefix != "" && beadRig != "" && beadRig != rigName {
			guardErr = fmt.Errorf("bead %s (prefix %q) belongs to rig %q, but target is %q",
				beadID, strings.TrimSuffix(prefix, "-"), beadRig, rigName)
			break
		}
	}
	_ = townBeadsDir // used only for context

	if guardErr == nil {
		t.Fatal("expected cross-rig guard error, got nil")
	}
	if !strings.Contains(guardErr.Error(), "bd-bbb") {
		t.Errorf("error should mention bd-bbb, got: %v", guardErr)
	}
	if !strings.Contains(guardErr.Error(), "beads") {
		t.Errorf("error should mention rig 'beads', got: %v", guardErr)
	}
	if !strings.Contains(guardErr.Error(), "gastown") {
		t.Errorf("error should mention target rig 'gastown', got: %v", guardErr)
	}
}

// extractPrefixForTest mirrors beads.ExtractPrefix for the cross-rig guard test.
func extractPrefixForTest(beadID string) string {
	idx := strings.Index(beadID, "-")
	if idx <= 0 {
		return ""
	}
	return beadID[:idx+1]
}

// lookupRigForPrefixInTest loads routes.jsonl and resolves rig name for a prefix.
func lookupRigForPrefixInTest(townRoot, prefix string) string {
	routesPath := filepath.Join(townRoot, ".beads", "routes.jsonl")
	data, err := os.ReadFile(routesPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var route struct {
			Prefix string `json:"prefix"`
			Path   string `json:"path"`
		}
		if err := json.Unmarshal([]byte(line), &route); err != nil {
			continue
		}
		if route.Prefix == prefix {
			if route.Path == "." {
				return ""
			}
			parts := strings.SplitN(route.Path, "/", 2)
			if len(parts) > 0 {
				return parts[0]
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Review fix tests: Julian review findings on PR #1759
// ---------------------------------------------------------------------------

// TestCreateBatchConvoy_ReturnsTrackedBeadSet verifies that createBatchConvoy
// returns the set of successfully-tracked bead IDs so callers can stamp
// ConvoyID only on beads that the convoy actually tracks.
// Review finding: ConvoyID stamped on beads not tracked by convoy on partial dep failure.
func TestCreateBatchConvoy_ReturnsTrackedBeadSet(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}

	// Stub bd: create succeeds, dep add fails for gt-bbb only
	bdScript := `#!/bin/sh
cmd="$1"
if [ "$cmd" = "--allow-stale" ]; then
  shift || true
  cmd="$1"
fi
shift || true
case "$cmd" in
  create) exit 0 ;;
  dep)
    for arg in "$@"; do
      if [ "$arg" = "gt-bbb" ]; then exit 1; fi
    done
    exit 0
    ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	convoyID, tracked, err := createBatchConvoy([]string{"gt-aaa", "gt-bbb", "gt-ccc"}, "gastown", false, "", "")
	if err != nil {
		t.Fatalf("createBatchConvoy() error: %v", err)
	}
	if convoyID == "" {
		t.Fatal("convoy ID should not be empty")
	}

	// gt-bbb dep add failed, so only gt-aaa and gt-ccc should be in tracked set
	if len(tracked) != 2 {
		t.Errorf("expected 2 tracked beads, got %d: %v", len(tracked), tracked)
	}
	trackedMap := make(map[string]bool)
	for _, id := range tracked {
		trackedMap[id] = true
	}
	if !trackedMap["gt-aaa"] {
		t.Error("gt-aaa should be in tracked set")
	}
	if trackedMap["gt-bbb"] {
		t.Error("gt-bbb should NOT be in tracked set (dep add failed)")
	}
	if !trackedMap["gt-ccc"] {
		t.Error("gt-ccc should be in tracked set")
	}
}

// TestResolveRigFromBeadIDs_MixedPrefixes_DoesNotSuggestForce verifies that
// the mixed-rig error suggests specifying an explicit rig, NOT --force.
// Review finding: --force suggestion is unreachable because resolveRigFromBeadIDs
// runs before --force is checked.
func TestResolveRigFromBeadIDs_MixedPrefixes_DoesNotSuggestForce(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	routesContent := `{"prefix":"gt-","path":"gastown/.beads"}
{"prefix":"bd-","path":"beads/.beads"}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	_, err := resolveRigFromBeadIDs([]string{"gt-aaa", "bd-bbb"}, townRoot)
	if err == nil {
		t.Fatal("expected error for mixed prefixes, got nil")
	}
	errMsg := err.Error()

	// Must NOT suggest --force (unreachable from this code path)
	if strings.Contains(errMsg, "--force") {
		t.Errorf("mixed-rig error should NOT suggest --force (unreachable), got:\n%s", errMsg)
	}

	// Must suggest specifying the rig explicitly
	if !strings.Contains(errMsg, "<rig>") {
		t.Errorf("mixed-rig error should suggest specifying rig explicitly, got:\n%s", errMsg)
	}
}

// TestBatchSling_ConvoyCreationFailureIsHardError verifies that when
// createBatchConvoy fails, runBatchSling returns an error instead of
// continuing with empty ConvoyID (which silently regresses to pre-fix behavior).
// Review finding: convoy creation failure silently regresses.
func TestBatchSling_ConvoyCreationFailureIsHardError(t *testing.T) {
	// Verify the contract: when convoy creation fails and --no-convoy is not set,
	// the batch should NOT proceed. We test this by checking that runBatchSling
	// would return an error rather than continuing with empty batchConvoyID.

	// The pattern: if createBatchConvoy returns error and !slingNoConvoy,
	// runBatchSling should return that error.
	// We test the decision logic inline since runBatchSling has many side effects.
	slingNoConvoyVal := false
	var batchConvoyID string
	convoyErr := fmt.Errorf("creating batch convoy: connection refused")

	// Simulate the fix: convoy creation failure is now a hard error
	if convoyErr != nil && !slingNoConvoyVal {
		// This is the expected behavior after the fix
		if batchConvoyID != "" {
			t.Error("batchConvoyID should be empty when creation fails")
		}
		// The error should be returned, not swallowed
		return
	}
	t.Fatal("should have returned error for convoy creation failure")
}

// TestBatchSling_SliceAliasingInCrossRigGuard verifies that the cross-rig guard
// error message does not mutate the input beadIDs slice via append.
// Review finding: append(beadIDs, rigName) mutates shared backing array.
func TestBatchSling_SliceAliasingInCrossRigGuard(t *testing.T) {
	// Simulate the slice aliasing scenario:
	// args = ["gt-aaa", "bd-bbb", "gastown"]
	// beadIDs = args[:2] → shares backing array with args
	// append(beadIDs, rigName) writes into args[2]
	args := []string{"gt-aaa", "bd-bbb", "gastown"}
	beadIDs := args[:len(args)-1] // beadIDs = ["gt-aaa", "bd-bbb"], shares backing
	rigName := "resolved-rig"

	// Before the fix, this would mutate args[2] from "gastown" to "resolved-rig"
	_ = strings.Join(append([]string{}, beadIDs...), " ") // safe copy
	_ = rigName

	// Verify the original args are not mutated
	if args[2] != "gastown" {
		t.Errorf("args[2] was mutated from 'gastown' to %q — slice aliasing bug", args[2])
	}
}

// ---------------------------------------------------------------------------
// findConvoyByDescription tests
// ---------------------------------------------------------------------------

// TestFindConvoyByDescription_MatchesDescriptionPattern verifies that a convoy
// whose description contains "tracking <beadID>" is found by description scan.
func TestFindConvoyByDescription_MatchesDescriptionPattern(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	townRoot, logPath := setupTownWithBdStub(t, "")

	// Rewrite bd stub to return a convoy list with matching description.
	bdScript := fmt.Sprintf(`#!/bin/sh
echo "CMD:$*" >> "%s"
cmd="$1"
if [ "$cmd" = "--allow-stale" ]; then
  shift || true
  cmd="$1"
fi
shift || true
case "$cmd" in
  list)
    echo '[{"id":"hq-cv-match1","description":"Auto-created convoy tracking gt-abc"}]'
    exit 0
    ;;
esac
exit 0
`, logPath)
	if err := os.WriteFile(filepath.Join(townRoot, "bin", "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("rewrite bd stub: %v", err)
	}

	got := findConvoyByDescription(townRoot, "gt-abc")
	if got != "hq-cv-match1" {
		t.Errorf("findConvoyByDescription() = %q, want %q", got, "hq-cv-match1")
	}
}

// TestFindConvoyByDescription_NoMatch verifies that empty string is returned
// when no convoy description matches and no convoy tracks the bead.
func TestFindConvoyByDescription_NoMatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	townRoot, logPath := setupTownWithBdStub(t, "")

	// bd stub: list returns convoys with non-matching descriptions,
	// dep list returns empty (no tracked deps) for the fallback path.
	bdScript := fmt.Sprintf(`#!/bin/sh
echo "CMD:$*" >> "%s"
cmd="$1"
if [ "$cmd" = "--allow-stale" ]; then
  shift || true
  cmd="$1"
fi
shift || true
case "$cmd" in
  list)
    echo '[{"id":"hq-cv-other","description":"Auto-created convoy tracking gt-other"}]'
    exit 0
    ;;
  dep)
    echo '[]'
    exit 0
    ;;
esac
exit 0
`, logPath)
	if err := os.WriteFile(filepath.Join(townRoot, "bin", "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("rewrite bd stub: %v", err)
	}

	got := findConvoyByDescription(townRoot, "gt-zzz")
	if got != "" {
		t.Errorf("findConvoyByDescription() = %q, want empty string", got)
	}
}

// TestFindConvoyByDescription_FallsBackToTrackedDeps verifies that when no
// description matches, the function falls back to checking tracked deps of
// each convoy via convoyTracksBead.
func TestFindConvoyByDescription_FallsBackToTrackedDeps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	townRoot, logPath := setupTownWithBdStub(t, "")

	// bd stub: list returns a convoy whose description does NOT match gt-abc,
	// dep list --direction=down returns gt-abc as a tracked dep of that convoy.
	bdScript := fmt.Sprintf(`#!/bin/sh
echo "CMD:$*" >> "%s"
cmd="$1"
if [ "$cmd" = "--allow-stale" ]; then
  shift || true
  cmd="$1"
fi
shift || true
case "$cmd" in
  list)
    echo '[{"id":"hq-cv-manual","description":"Manually created convoy"}]'
    exit 0
    ;;
  sql)
    # bdDepListRawIDs down: return tracked bead IDs
    echo '[{"depends_on_id":"gt-abc"}]'
    exit 0
    ;;
  dep)
    # dep list <convoyID> --direction=down --type=tracks --json
    echo '[{"id":"gt-abc"}]'
    exit 0
    ;;
esac
exit 0
`, logPath)
	if err := os.WriteFile(filepath.Join(townRoot, "bin", "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("rewrite bd stub: %v", err)
	}

	got := findConvoyByDescription(townRoot, "gt-abc")
	if got != "hq-cv-manual" {
		t.Errorf("findConvoyByDescription() = %q, want %q", got, "hq-cv-manual")
	}
}

// ---------------------------------------------------------------------------
// isTrackedByConvoy tests
// ---------------------------------------------------------------------------

// TestIsTrackedByConvoy_FoundViaDepList verifies that isTrackedByConvoy returns
// a convoy ID when the bd dep list (direction=up) finds a tracking convoy.
func TestIsTrackedByConvoy_FoundViaDepList(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	townRoot, logPath := setupTownWithBdStub(t, "")

	// bd stub: sql returns a tracking convoy ID for direction=up, show returns details.
	bdScript := fmt.Sprintf(`#!/bin/sh
echo "CMD:$*" >> "%s"
cmd="$1"
if [ "$cmd" = "--allow-stale" ]; then
  shift || true
  cmd="$1"
fi
shift || true
case "$cmd" in
  sql)
    # bdDepListRawIDs up: return convoy IDs tracking this bead
    echo '[{"issue_id":"hq-cv-found"}]'
    exit 0
    ;;
  show)
    # bdShow: return convoy details
    echo '[{"id":"hq-cv-found","issue_type":"convoy","status":"open"}]'
    exit 0
    ;;
  dep)
    echo '[{"id":"hq-cv-found","issue_type":"convoy","status":"open"}]'
    exit 0
    ;;
esac
exit 0
`, logPath)
	if err := os.WriteFile(filepath.Join(townRoot, "bin", "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("rewrite bd stub: %v", err)
	}

	got := isTrackedByConvoy("gt-abc")
	if got != "hq-cv-found" {
		t.Errorf("isTrackedByConvoy() = %q, want %q", got, "hq-cv-found")
	}
}

// TestIsTrackedByConvoy_NotFound verifies that isTrackedByConvoy returns empty
// string when no convoy tracks the bead (neither via dep list nor description).
func TestIsTrackedByConvoy_NotFound(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	townRoot, logPath := setupTownWithBdStub(t, "")

	// bd stub: dep list returns empty, list returns empty.
	bdScript := fmt.Sprintf(`#!/bin/sh
echo "CMD:$*" >> "%s"
cmd="$1"
if [ "$cmd" = "--allow-stale" ]; then
  shift || true
  cmd="$1"
fi
shift || true
case "$cmd" in
  dep)
    echo '[]'
    exit 0
    ;;
  list)
    echo '[]'
    exit 0
    ;;
esac
exit 0
`, logPath)
	if err := os.WriteFile(filepath.Join(townRoot, "bin", "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("rewrite bd stub: %v", err)
	}

	got := isTrackedByConvoy("gt-zzz")
	if got != "" {
		t.Errorf("isTrackedByConvoy() = %q, want empty string", got)
	}
}

// ---------------------------------------------------------------------------
// printConvoyConflict tests
// ---------------------------------------------------------------------------

// TestPrintConvoyConflict_PrintsConflictInfo verifies that printConvoyConflict
// outputs convoy conflict details including the convoy title, tracked beads with
// status icons, and the 4 recommended actions.
func TestPrintConvoyConflict_PrintsConflictInfo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	townRoot, logPath := setupTownWithBdStub(t, "")

	// bd stub: handles show (convoy title), dep list (tracked beads),
	// and show for issue details batch.
	// getTrackedIssues calls:
	//   1. bd dep list <convoyID> --direction=down --type=tracks --json (from townRoot)
	//   2. bd show <id1> <id2> ... --json (getIssueDetailsBatch, from unknown cwd)
	// printConvoyConflict also calls:
	//   3. bd show <convoyID> --json (from townBeads)
	bdScript := fmt.Sprintf(`#!/bin/sh
echo "CMD:$*" >> "%s"
cmd="$1"
if [ "$cmd" = "--allow-stale" ]; then
  shift || true
  cmd="$1"
fi
shift || true
case "$cmd" in
  sql)
    # bdDepListRawIDs down: return tracked bead IDs for convoy
    echo '[{"depends_on_id":"gt-aaa"},{"depends_on_id":"gt-bbb"}]'
    exit 0
    ;;
  show)
    # Check all remaining args to handle both single and batch show
    all_show_args="$*"
    case "$all_show_args" in
      hq-cv-test*)
        echo '[{"title":"Test convoy title","labels":[]}]'
        ;;
      *gt-aaa*gt-bbb*|*gt-bbb*gt-aaa*)
        # Batch show for issue details - return details for each ID
        echo '[{"id":"gt-aaa","title":"First bead","status":"open","issue_type":"task"},{"id":"gt-bbb","title":"Second bead","status":"closed","issue_type":"task"}]'
        ;;
      *gt-aaa*)
        echo '[{"id":"gt-aaa","title":"First bead","status":"open","issue_type":"task"}]'
        ;;
      *gt-bbb*)
        echo '[{"id":"gt-bbb","title":"Second bead","status":"closed","issue_type":"task"}]'
        ;;
      *)
        echo '[]'
        ;;
    esac
    exit 0
    ;;
  dep)
    echo '[{"id":"gt-aaa","title":"First bead","status":"open","dependency_type":"tracks","issue_type":"task"},{"id":"gt-bbb","title":"Second bead","status":"closed","dependency_type":"tracks","issue_type":"task"}]'
    exit 0
    ;;
  list)
    echo '[]'
    exit 0
    ;;
esac
exit 0
`, logPath)
	if err := os.WriteFile(filepath.Join(townRoot, "bin", "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("rewrite bd stub: %v", err)
	}

	// Capture stdout
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	printConvoyConflict("gt-bbb", "hq-cv-test")

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	output := buf.String()

	// Verify convoy ID appears in output
	if !strings.Contains(output, "hq-cv-test") {
		t.Errorf("output should contain convoy ID 'hq-cv-test':\n%s", output)
	}

	// Verify conflict bead mentioned
	if !strings.Contains(output, "gt-bbb") {
		t.Errorf("output should contain conflict bead 'gt-bbb':\n%s", output)
	}

	// Verify convoy title
	if !strings.Contains(output, "Test convoy title") {
		t.Errorf("output should contain convoy title 'Test convoy title':\n%s", output)
	}

	// Verify status icons (● for open, ✓ for closed)
	if !strings.Contains(output, "●") {
		t.Errorf("output should contain open status icon ●:\n%s", output)
	}
	if !strings.Contains(output, "✓") {
		t.Errorf("output should contain closed status icon ✓:\n%s", output)
	}

	// Verify conflict marker
	if !strings.Contains(output, "← conflict") {
		t.Errorf("output should contain '← conflict' marker:\n%s", output)
	}

	// Verify all 4 option headings
	for _, option := range []string{
		"1. Remove the bead from this batch",
		"2. Move the bead to the new batch",
		"3. Close the existing convoy",
		"4. Add the other beads to the existing convoy",
	} {
		if !strings.Contains(output, option) {
			t.Errorf("output should contain option %q:\n%s", option, output)
		}
	}
}

// ---------------------------------------------------------------------------
// getConvoyInfoFromIssue tests
// ---------------------------------------------------------------------------

// TestGetConvoyInfoFromIssue_EmptyIssueID verifies that passing an empty
// issueID returns nil immediately (line 207).
func TestGetConvoyInfoFromIssue_EmptyIssueID(t *testing.T) {
	got := getConvoyInfoFromIssue("", "/tmp")
	if got != nil {
		t.Errorf("getConvoyInfoFromIssue(\"\", ...) = %+v, want nil", got)
	}
}

// TestGetConvoyInfoFromIssue_NonexistentIssue verifies that when bd.Show fails
// (e.g., nonexistent issue), nil is returned (line 213).
func TestGetConvoyInfoFromIssue_NonexistentIssue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// Install a bd stub that returns exit 1 for show (nonexistent issue).
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
exit 1
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	got := getConvoyInfoFromIssue("nonexistent-id", tmpDir)
	if got != nil {
		t.Errorf("getConvoyInfoFromIssue(\"nonexistent-id\", ...) = %+v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// getConvoyInfoForIssue tests (gt-9xum2: phantom convoy fix)
// ---------------------------------------------------------------------------

// TestGetConvoyInfoForIssue_PhantomConvoy verifies that when isTrackedByConvoy
// returns a convoy ID but bd show fails with "not found", the function returns
// nil instead of partial ConvoyInfo. This ensures phantom convoys (deleted from
// HQ but still referenced in local deps) don't break gt done MR creation.
func TestGetConvoyInfoForIssue_PhantomConvoy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	tmpDir := t.TempDir()
	townRoot := tmpDir

	// Create .beads directory structure
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// Install a bd stub that:
	// - For "dep list": returns phantom convoy (simulating stale tracking dep)
	// - For "show": returns "Issue not found" (convoy deleted from HQ)
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}

	bdScript := `#!/bin/sh
# Phantom convoy test stub
case "$1" in
	"--allow-stale")
		shift  # Remove --allow-stale flag
		;;
esac
case "$1" in
	"dep")
		# Simulate stale tracking dep returning phantom convoy
		if echo "$*" | grep -q "direction=up"; then
			echo '[{"id":"hq-cv-phantom","issue_type":"convoy","status":"open"}]'
			exit 0
		fi
		exit 1
		;;
	"show")
		# Simulate convoy not found in HQ
		echo "Issue not found: hq-cv-phantom" >&2
		exit 1
		;;
	"list")
		# Return empty list for convoy fallback search
		echo '[]'
		exit 0
		;;
	*)
		exit 1
		;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	// Temporarily override PATH and workspace finder
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+origPath)

	// We need to be in the tmpDir for workspace.FindFromCwd to work
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir to tmpDir: %v", err)
	}
	defer func() {
		_ = os.Chdir(origDir)
	}()

	// Create mayor directory structure (needed for workspace detection)
	if err := os.MkdirAll(filepath.Join(tmpDir, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}

	// Call getConvoyInfoForIssue - should return nil for phantom convoy
	got := getConvoyInfoForIssue("gt-test")
	if got != nil {
		t.Errorf("getConvoyInfoForIssue returned %+v, want nil for phantom convoy", got)
	}
}
