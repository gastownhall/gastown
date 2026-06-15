package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type bdEnvLogEntry struct {
	Args     string
	PWD      string
	BeadsDir string
	Database string
	Host     string
	Port     string
	Legacy   string
	DataDir  string
	BeadsDB  string
	BDDB     string
	Export   string
}

func TestConvoyStageBDHelpersPinRouteMetadataUnderStaleEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	townRoot, rigDir, logPath := setupRoutedBDEnvStub(t)
	poisonBDTargetEnv(t, townRoot)

	if _, err := bdShow("gt-abc"); err != nil {
		t.Fatalf("bdShow gt: %v", err)
	}
	if _, err := bdDepList("gt-abc"); err != nil {
		t.Fatalf("bdDepList gt: %v", err)
	}
	if _, err := bdListChildren("gt-abc"); err != nil {
		t.Fatalf("bdListChildren gt: %v", err)
	}
	if _, err := bdShow("hq-cv-found"); err != nil {
		t.Fatalf("bdShow hq: %v", err)
	}

	logs := readBDEnvLog(t, logPath)
	assertBDEnvLog(t, findBDEnvLog(t, logs, "show gt-abc"), rigDir, "gastown", "127.0.0.2", "4407")
	assertBDEnvLog(t, findBDEnvLog(t, logs, "dep list gt-abc"), rigDir, "gastown", "127.0.0.2", "4407")
	assertBDEnvLog(t, findBDEnvLog(t, logs, "list --parent=gt-abc"), rigDir, "gastown", "127.0.0.2", "4407")
	assertBDEnvLog(t, findBDEnvLog(t, logs, "show hq-cv-found"), townRoot, "hq", "127.0.0.1", "3307")
}

func TestSlingAutoConvoyCheckPinsTrackerShowToHQUnderStaleEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	townRoot, _, logPath := setupRoutedBDEnvStub(t)
	poisonBDTargetEnv(t, townRoot)

	if got := isTrackedByConvoy("gt-abc"); got != "hq-cv-found" {
		t.Fatalf("isTrackedByConvoy() = %q, want hq-cv-found", got)
	}

	logs := readBDEnvLog(t, logPath)
	assertBDEnvLogWithBeadsDir(t, findBDEnvLog(t, logs, "sql SELECT issue_id"), filepath.Join(townRoot, ".beads"), filepath.Join(townRoot, ".beads"), "hq", "127.0.0.1", "3307")
	assertBDEnvLog(t, findBDEnvLog(t, logs, "show hq-cv-found"), townRoot, "hq", "127.0.0.1", "3307")
}

func setupRoutedBDEnvStub(t *testing.T) (townRoot, rigDir, logPath string) {
	t.Helper()

	townRoot = setupShowInvocationTown(t)
	rigDir = filepath.Join(townRoot, "gastown", "mayor", "rig")
	binDir := t.TempDir()
	logPath = filepath.Join(t.TempDir(), "bd-env.log")

	script := fmt.Sprintf(`#!/bin/sh
	printf '%%s|%%s|%%s|%%s|%%s|%%s|%%s|%%s|%%s|%%s|%%s\n' "$*" "$(pwd)" "${BEADS_DIR:-}" "${BEADS_DOLT_SERVER_DATABASE:-}" "${BEADS_DOLT_SERVER_HOST:-}" "${BEADS_DOLT_SERVER_PORT:-}" "${BEADS_DOLT_PORT:-}" "${BEADS_DOLT_DATA_DIR:-}" "${BEADS_DB:-}" "${BD_DB:-}" "${BD_EXPORT_AUTO:-}" >> "%s"

case "$1" in
  show)
    case "$2" in
      hq-cv-*) echo '[{"id":"hq-cv-found","title":"HQ convoy","status":"open","issue_type":"convoy","labels":["gt:convoy"]}]' ;;
      *) echo '[{"id":"gt-abc","title":"GT task","status":"open","issue_type":"task","labels":[]}]' ;;
    esac
    exit 0
    ;;
  dep)
    echo '[{"id":"gt-blocker","dependency_type":"blocks"}]'
    exit 0
    ;;
  list)
    echo '[{"id":"gt-child","title":"Child","status":"open","issue_type":"task","labels":[]}]'
    exit 0
    ;;
  sql)
    echo '[{"issue_id":"hq-cv-found"}]'
    exit 0
    ;;
esac

echo "unexpected bd args: $*" >&2
exit 1
`, logPath)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	return townRoot, rigDir, logPath
}

func poisonBDTargetEnv(t *testing.T, townRoot string) {
	t.Helper()
	t.Setenv("BEADS_DIR", filepath.Join(townRoot, "wrong", ".beads"))
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "stale")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "wrong-host")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "9999")
	t.Setenv("BEADS_DOLT_PORT", "9999")
	t.Setenv("BEADS_DOLT_DATA_DIR", filepath.Join(townRoot, "wrong-data"))
	t.Setenv("BEADS_DB", filepath.Join(townRoot, "wrong.db"))
	t.Setenv("BD_DB", filepath.Join(townRoot, "wrong.bd"))
	t.Setenv("BD_EXPORT_AUTO", "true")
}

func readBDEnvLog(t *testing.T, logPath string) []bdEnvLogEntry {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd env log: %v", err)
	}
	var entries []bdEnvLogEntry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) != 11 {
			t.Fatalf("malformed bd env log line %q: fields=%v", line, fields)
		}
		entries = append(entries, bdEnvLogEntry{
			Args:     fields[0],
			PWD:      fields[1],
			BeadsDir: fields[2],
			Database: fields[3],
			Host:     fields[4],
			Port:     fields[5],
			Legacy:   fields[6],
			DataDir:  fields[7],
			BeadsDB:  fields[8],
			BDDB:     fields[9],
			Export:   fields[10],
		})
	}
	return entries
}

func findBDEnvLog(t *testing.T, entries []bdEnvLogEntry, argsPart string) bdEnvLogEntry {
	t.Helper()
	for _, entry := range entries {
		if strings.Contains(entry.Args, argsPart) {
			return entry
		}
	}
	t.Fatalf("bd env log missing args containing %q in %+v", argsPart, entries)
	return bdEnvLogEntry{}
}

func assertBDEnvLog(t *testing.T, entry bdEnvLogEntry, wantDir, wantDB, wantHost, wantPort string) {
	t.Helper()
	assertBDEnvLogWithBeadsDir(t, entry, wantDir, filepath.Join(wantDir, ".beads"), wantDB, wantHost, wantPort)
}

func assertBDEnvLogWithBeadsDir(t *testing.T, entry bdEnvLogEntry, wantDir, wantBeadsDir, wantDB, wantHost, wantPort string) {
	t.Helper()
	if got, want := cleanTestPath(entry.PWD), cleanTestPath(wantDir); got != want {
		t.Fatalf("%q PWD = %q, want %q", entry.Args, entry.PWD, wantDir)
	}
	if got, want := cleanTestPath(entry.BeadsDir), cleanTestPath(wantBeadsDir); got != want {
		t.Fatalf("%q BEADS_DIR = %q, want %q", entry.Args, entry.BeadsDir, wantBeadsDir)
	}
	if entry.Database != wantDB {
		t.Fatalf("%q BEADS_DOLT_SERVER_DATABASE = %q, want %q", entry.Args, entry.Database, wantDB)
	}
	if entry.Host != wantHost {
		t.Fatalf("%q BEADS_DOLT_SERVER_HOST = %q, want %q", entry.Args, entry.Host, wantHost)
	}
	if entry.Port != wantPort {
		t.Fatalf("%q BEADS_DOLT_SERVER_PORT = %q, want %q", entry.Args, entry.Port, wantPort)
	}
	if entry.Legacy != wantPort {
		t.Fatalf("%q BEADS_DOLT_PORT = %q, want %q", entry.Args, entry.Legacy, wantPort)
	}
	if entry.DataDir != "" {
		t.Fatalf("%q BEADS_DOLT_DATA_DIR should be stripped, got %q", entry.Args, entry.DataDir)
	}
	if entry.BeadsDB != "" || entry.BDDB != "" {
		t.Fatalf("%q stale DB env leaked: BEADS_DB=%q BD_DB=%q", entry.Args, entry.BeadsDB, entry.BDDB)
	}
	if entry.Export != "false" {
		t.Fatalf("%q BD_EXPORT_AUTO = %q, want false", entry.Args, entry.Export)
	}
}

func cleanTestPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}
