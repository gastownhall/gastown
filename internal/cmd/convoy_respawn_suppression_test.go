package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFindStrandedConvoys_SuppressesRespawnLimitedIssue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stub")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"),
		[]byte(`{"prefix":"gt-","path":"gastown/mayor/rig"}`+"\n"), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	stateDir := filepath.Join(townRoot, "witness")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatalf("mkdir witness: %v", err)
	}
	state := `{
  "beads": {
    "gt-failed": {
      "bead_id": "gt-failed",
      "count": 3,
      "last_respawn": "2026-07-13T12:19:43Z"
    }
  },
  "last_updated": "2026-07-13T12:19:43Z"
}`
	if err := os.WriteFile(filepath.Join(stateDir, "bead-respawn-counts.json"), []byte(state), 0600); err != nil {
		t.Fatalf("write respawn state: %v", err)
	}

	bdScript := `#!/bin/sh
case "$*" in
  *"list "*"--label=gt:convoy"*)
    echo '[{"id":"hq-cv-failed","title":"Failed task convoy"}]'
    ;;
  *"sql "*"hq-cv-failed"*)
    echo '[{"depends_on_id":"gt-failed"}]'
    ;;
  *"dep list hq-cv-failed"*)
    echo '[{"id":"gt-failed","title":"Repeated failure","status":"open","issue_type":"task","assignee":"","dependency_type":"tracks"}]'
    ;;
  *"show"*"gt-failed"*)
    echo '[{"id":"gt-failed","title":"Repeated failure","status":"open","issue_type":"task","assignee":"","blocked_by":[],"blocked_by_count":0,"dependencies":[]}]'
    ;;
  *)
    echo '[]'
    ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stranded, err := findStrandedConvoys(townRoot)
	if err != nil {
		t.Fatalf("findStrandedConvoys: %v", err)
	}
	if len(stranded) != 1 {
		t.Fatalf("got %d convoys, want 1", len(stranded))
	}

	got := stranded[0]
	if got.ReadyCount != 0 || len(got.ReadyIssues) != 0 {
		t.Fatalf("respawn-limited issue remained ready: %+v", got)
	}
	if got.SuppressedCount != 1 || len(got.SuppressedIssues) != 1 {
		t.Fatalf("suppression metadata missing: %+v", got)
	}
	suppressed := got.SuppressedIssues[0]
	if suppressed.ID != "gt-failed" || suppressed.Reason != "respawn_limit" {
		t.Errorf("unexpected suppression: %+v", suppressed)
	}
	if suppressed.ResetCommand != "gt sling respawn-reset gt-failed" {
		t.Errorf("unexpected reset command: %q", suppressed.ResetCommand)
	}
}
