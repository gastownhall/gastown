package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestDoneAgentLifecycleWritesUseTownFromTownAndRigContexts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock for bd")
	}

	townRoot, _ := filepath.EvalSymlinks(t.TempDir())
	townBeadsDir := filepath.Join(townRoot, ".beads")
	rigDir := filepath.Join(townRoot, "gastown", "mayor", "rig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	for _, dir := range []string{filepath.Join(townRoot, "mayor"), townBeadsDir, rigBeadsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0o644); err != nil {
		t.Fatalf("write town marker: %v", err)
	}
	if err := beads.WriteRoutes(townBeadsDir, []beads.Route{{Prefix: "hq-", Path: "."}, {Prefix: "gt-", Path: "gastown/mayor/rig"}}); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	script := fmt.Sprintf(`#!/bin/sh
printf 'beads_dir=%%s args=%%s\n' "${BEADS_DIR:-<unset>}" "$*" >> %q
cmd=""
for arg in "$@"; do case "$arg" in --*) ;; *) cmd="$arg"; break ;; esac; done
case "$cmd" in
  version) printf 'bd version 1.2.2\n' ;;
  show)
    case "$*" in
      *gt-gastown-polecat-rust*)
        [ "${BEADS_DIR:-}" = %q ] || exit 9
        printf '%%s\n' '[{"id":"gt-gastown-polecat-rust","title":"rust","issue_type":"task","labels":["gt:agent"],"status":"open","description":"role_type: polecat\nrig: gastown\nagent_state: working\nhook_bead: gt-work-1"}]'
        ;;
      *gt-work-1*)
        [ "${BEADS_DIR:-}" = %q ] || exit 8
        printf '%%s\n' '[{"id":"gt-work-1","title":"work","issue_type":"task","status":"hooked"}]'
        ;;
    esac
    ;;
  update)
    case "$*" in *gt-gastown-polecat-rust*) [ "${BEADS_DIR:-}" = %q ] || exit 9 ;; *) exit 7 ;; esac
    ;;
esac
`, logPath, townBeadsDir, rigBeadsDir, townBeadsDir)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	agentID := "gt-gastown-polecat-rust"
	workID := "gt-work-1"
	clients := []*beads.Beads{
		beads.NewWithBeadsDir(townRoot, townBeadsDir),
		beads.NewWithBeadsDir(rigDir, rigBeadsDir),
	}
	for _, client := range clients {
		if err := client.UpdateAgentCompletion(agentID, &beads.CompletionMetadata{ExitType: ExitCompleted, HookBead: workID}); err != nil {
			t.Fatalf("completion write: %v", err)
		}
		writeDoneCheckpoint(client, agentID, CheckpointPushed, "branch")
		empty := ""
		if err := client.UpdateAgentDescriptionFields(agentID, beads.AgentFieldUpdates{HookBead: &empty}); err != nil {
			t.Fatalf("hook-clear write: %v", err)
		}
		if _, err := client.Show(workID); err != nil {
			t.Fatalf("rig work lookup: %v", err)
		}
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake bd log: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(logBytes)), "\n") {
		switch {
		case strings.Contains(line, agentID):
			if !strings.Contains(line, "beads_dir="+townBeadsDir) || strings.Contains(line, "beads_dir="+rigBeadsDir) {
				t.Fatalf("agent lifecycle call was not town-scoped: %s", line)
			}
		case strings.Contains(line, workID):
			if !strings.Contains(line, "beads_dir="+rigBeadsDir) {
				t.Fatalf("work issue call was not rig-scoped: %s", line)
			}
		}
	}
}
