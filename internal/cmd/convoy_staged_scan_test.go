package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestStrandedScanExcludesStagedConvoys verifies that findStrandedConvoys
// queries bd with --status=open, which inherently excludes staged convoys
// (status "staged_ready" or "staged_warnings").
//
// This is a safety-net test for bead gt-csl.5.2: the stranded scan is safe
// by construction because it only queries open convoys, but we want a test
// proving that invariant so future refactors don't regress it.
func TestStrandedScanExcludesStagedConvoys(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stub")
	}

	// Set up a fake town with a bd stub that:
	// 1. Logs every invocation to bd.log (so we can inspect the query)
	// 2. For "list --status=open" returns an empty convoy set
	// 3. For any other list returns staged convoys (proving they'd appear
	//    if the filter were wrong)
	binDir := t.TempDir()
	townRoot := t.TempDir()
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatalf("mkdir townBeads: %v", err)
	}

	logPath := filepath.Join(binDir, "bd.log")
	bdPath := filepath.Join(binDir, "bd")

	// The stub logs the full command line, then:
	// - For list with --status=open: returns [] (no open convoys)
	// - For list without --status=open: returns a staged convoy (would be wrong)
	// - For other subcommands: returns []
	script := `#!/bin/sh
echo "$@" >> "` + logPath + `"

# Detect subcommand (skip flags)
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done

case "$cmd" in
  list)
    # Check if --status=open is present
    case "$*" in
      *--status=open*|*"--status open"*)
        # Correct query: return empty (no open convoys)
        echo '[]'
        ;;
      *)
        # Wrong query: would leak staged convoys
        echo '[{"id":"hq-cv-staged1","title":"Staged convoy"}]'
        ;;
    esac
    ;;
  *)
    echo '[]'
    ;;
esac
exit 0
`
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Call findStrandedConvoys — it should query bd list --status=open
	stranded, err := findStrandedConvoys(townBeads)
	if err != nil {
		t.Fatalf("findStrandedConvoys() error: %v", err)
	}

	// 1. Verify no staged convoys leaked into results.
	//    If the query used anything other than --status=open, the stub would
	//    have returned the staged convoy.
	for _, s := range stranded {
		if strings.Contains(s.ID, "staged") {
			t.Errorf("staged convoy %q leaked into stranded results — query is not filtering by --status=open", s.ID)
		}
	}

	// 2. Verify the bd command log contains --status=open.
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading bd.log: %v", err)
	}
	logStr := string(logData)

	// Find the list command line
	var listLine string
	for _, line := range strings.Split(logStr, "\n") {
		if strings.Contains(line, "list") {
			listLine = line
			break
		}
	}
	if listLine == "" {
		t.Fatal("bd was never called with a 'list' subcommand")
	}

	if !strings.Contains(listLine, "--status=open") {
		t.Errorf("bd list command does not include --status=open; got: %q", listLine)
	}

	// 3. Verify that --status=open would NOT match staged statuses.
	//    This is a string-level assertion: "staged_ready" != "open" and
	//    "staged_warnings" != "open".
	stagedStatuses := []string{"staged_ready", "staged_warnings"}
	for _, ss := range stagedStatuses {
		if ss == "open" {
			t.Errorf("staged status %q equals 'open' — stranded scan would include staged convoys!", ss)
		}
	}

	// 4. Verify the result is empty (our stub returns [] for --status=open).
	if len(stranded) != 0 {
		t.Errorf("expected 0 stranded convoys (stub returns [] for --status=open), got %d", len(stranded))
	}
}

// TestStrandedScanQueryShape verifies the exact arguments passed to bd
// by findStrandedConvoys, ensuring the --label=gt:convoy and --status=open
// flags are both present. This guards against flag drift in refactors.
func TestStrandedScanQueryShape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stub")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatalf("mkdir townBeads: %v", err)
	}

	logPath := filepath.Join(binDir, "bd.log")
	bdPath := filepath.Join(binDir, "bd")

	// Stub that logs args and returns empty list for everything.
	script := `#!/bin/sh
echo "$@" >> "` + logPath + `"
echo '[]'
exit 0
`
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := findStrandedConvoys(townBeads)
	if err != nil {
		t.Fatalf("findStrandedConvoys() error: %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading bd.log: %v", err)
	}

	// Find the list command line — bd may emit a version probe first.
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(lines) == 0 {
		t.Fatal("bd was never called")
	}

	var listLine string
	for _, line := range lines {
		if strings.Contains(line, "list") {
			listLine = line
			break
		}
	}
	if listLine == "" {
		t.Fatalf("bd was never called with a 'list' subcommand; log: %q", string(logData))
	}

	requiredFlags := []string{"list", "--label=gt:convoy", "--status=open", "--json", "--limit=0", "--flat"}
	for _, flag := range requiredFlags {
		if !strings.Contains(listLine, flag) {
			t.Errorf("bd list command missing %q; got: %q", flag, listLine)
		}
	}
}
