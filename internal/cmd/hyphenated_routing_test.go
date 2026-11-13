package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestHyphenatedConfiguredPrefixRoutesLookupHookAndFieldUpdate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fake bd")
	}
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)

	townRoot := t.TempDir()
	beadID := "home-projects-rig-home_projects"
	rigDir := filepath.Join(townRoot, "home-projects", "mayor", "rig")
	townBeadsDir := filepath.Join(townRoot, ".beads")
	for _, dir := range []string{
		filepath.Join(townRoot, "mayor"),
		townBeadsDir,
		filepath.Join(rigDir, ".beads"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test-town"}`), 0o644); err != nil {
		t.Fatalf("write town marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte("{\"prefix\":\"home-projects-\",\"path\":\"home-projects/mayor/rig\"}\n{\"prefix\":\"hq-\",\"path\":\".\"}\n"), 0o644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	bdScript := `#!/bin/sh
set -eu
printf '%s|%s\n' "$PWD" "$*" >> "$BD_LOG"
cmd="$1"
shift || true
if [ "$cmd" = "--allow-stale" ]; then
  cmd="$1"
  shift || true
fi
case "$cmd" in
  version)
    echo 'bd test'
    ;;
  show)
    if [ "$PWD" != "$TARGET_RIG" ]; then
      echo "wrong working directory: $PWD" >&2
      exit 1
    fi
    echo '[{"id":"home-projects-rig-home_projects","title":"Home projects issue","status":"open","assignee":"","description":""}]'
    ;;
  update)
    if [ "$PWD" != "$TARGET_RIG" ]; then
      echo "wrong working directory: $PWD" >&2
      exit 1
    fi
    ;;
  *)
    echo "unexpected bd command: $cmd" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_LOG", logPath)
	targetRig, err := filepath.EvalSymlinks(rigDir)
	if err != nil {
		t.Fatalf("resolve target rig: %v", err)
	}
	t.Setenv("TARGET_RIG", targetRig)
	t.Setenv("GT_TEST_SKIP_HOOK_VERIFY", "1")
	t.Setenv("GT_TEST_NO_NUDGE", "1")

	info, err := getBeadInfoFromTownRoot(townRoot, beadID)
	if err != nil {
		t.Fatalf("getBeadInfoFromTownRoot: %v", err)
	}
	if info.Title != "Home projects issue" {
		t.Fatalf("lookup title = %q, want Home projects issue", info.Title)
	}

	hookDir := beads.ResolveHookDir(townRoot, beadID, filepath.Join(townRoot, "fallback"))
	if hookDir != rigDir {
		t.Fatalf("ResolveHookDir = %q, want %q", hookDir, rigDir)
	}
	if err := hookBeadWithRetryWithTownRoot(beadID, "home-projects/polecats/Toast", hookDir, townRoot); err != nil {
		t.Fatalf("hookBeadWithRetryWithTownRoot: %v", err)
	}
	if err := storeFieldsInBeadFromTownRoot(townRoot, beadID, beadFieldUpdates{
		ConvoyID:      "hq-cv-test",
		MergeStrategy: "direct",
		ConvoyOwned:   true,
	}); err != nil {
		t.Fatalf("storeFieldsInBeadFromTownRoot: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	log := string(logBytes)
	if strings.Contains(log, "wrong working directory") {
		t.Fatalf("fake bd rejected a routed operation:\n%s", log)
	}
	if !strings.Contains(log, "update home-projects-rig-home_projects --status=hooked") {
		t.Fatalf("hook update missing from bd log:\n%s", log)
	}
	if !strings.Contains(log, "convoy_id: hq-cv-test") || !strings.Contains(log, "convoy_owned: true") {
		t.Fatalf("convoy ownership field update missing from bd log:\n%s", log)
	}
}
