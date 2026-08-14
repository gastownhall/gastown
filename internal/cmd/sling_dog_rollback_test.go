package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/dog"
)

func TestSQLStringLiteralEscapesDoltSpecialCharacters(t *testing.T) {
	got := sqlStringLiteral("path\\name's\nnext")
	want := "'path\\\\name''s\nnext'"
	if got != want {
		t.Fatalf("sqlStringLiteral() = %q, want %q", got, want)
	}
}

func TestRollbackFailedDogDispatchReleasesDogAndSource(t *testing.T) {
	townRoot := t.TempDir()
	for _, dir := range []string{filepath.Join(townRoot, "mayor"), filepath.Join(townRoot, ".beads")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	rigs := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	if err := config.SaveRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"), rigs); err != nil {
		t.Fatalf("save rigs config: %v", err)
	}
	startedAt := time.Date(2026, 7, 17, 5, 0, 0, 0, time.UTC)
	writeDogStateForDispatchTest(t, townRoot, "alpha", &dog.DogState{
		Name:          "alpha",
		State:         dog.StateWorking,
		Work:          "gt-new",
		WorkKind:      dog.WorkKindBead,
		WorkStartedAt: startedAt,
		LastActive:    startedAt,
		CreatedAt:     startedAt.Add(-time.Hour),
		UpdatedAt:     startedAt,
	})

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	statePath := filepath.Join(townRoot, "rollback-state")
	bdScript := `#!/bin/sh
echo "$@" >> "$DOG_ROLLBACK_LOG"
for arg in "$@"; do
  case "$arg" in
    "UPDATE issues"*)
      if [ "$(cat "$DOG_ROLLBACK_STATE" 2>/dev/null)" = beta ]; then
        case "$arg" in
          *"WHERE id='gt-new' AND status='hooked' AND assignee='deacon/dogs/alpha'"*) ;;
          *) echo restored > "$DOG_ROLLBACK_STATE" ;;
        esac
      else
        echo restored > "$DOG_ROLLBACK_STATE"
      fi
      ;;
  esac
  if [ "$arg" = "show" ]; then
    case "$(cat "$DOG_ROLLBACK_STATE" 2>/dev/null)" in
      restored) echo '[{"id":"gt-new","title":"Requested dog work","description":"","status":"open","assignee":""}]' ;;
      beta) echo '[{"id":"gt-new","title":"Requested dog work","description":"","status":"hooked","assignee":"deacon/dogs/beta"}]' ;;
      *)
        printf '[{"id":"gt-new","title":"Requested dog work","description":"%s","status":"hooked","assignee":"deacon/dogs/alpha"}]\n' "$DOG_ROLLBACK_DESCRIPTION"
        [ "$DOG_ROLLBACK_RACE" = "1" ] && echo beta > "$DOG_ROLLBACK_STATE"
        ;;
    esac
    exit 0
  fi
done
exit 0
`
	writeBDStub(t, binDir, bdScript, "@echo off\nexit /b 0\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOG_ROLLBACK_LOG", logPath)
	t.Setenv("DOG_ROLLBACK_STATE", statePath)

	dispatch := &DogDispatchInfo{
		DogName:       "alpha",
		AgentID:       "deacon/dogs/alpha",
		townRoot:      townRoot,
		workDesc:      "gt-new",
		workStartedAt: startedAt,
		ownsWork:      true,
		rigsConfig:    rigs,
	}
	rollbackFailedDogDispatch(dispatch, townRoot, "gt-new", townRoot, "", "open", "", "", &beadInfo{Status: "open"})

	got, err := dog.NewManager(townRoot, rigs).Get("alpha")
	if err != nil {
		t.Fatalf("get dog: %v", err)
	}
	if got.State != dog.StateIdle || got.Work != "" || !got.WorkStartedAt.IsZero() {
		t.Fatalf("dog assignment survived rollback: state=%q work=%q started=%v", got.State, got.Work, got.WorkStartedAt)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "sql UPDATE issues SET status='open', assignee='', description=''") {
		t.Fatalf("atomic source restoration missing from bd log:\n%s", logText)
	}

	// If metadata committed over an intervening description edit, rollback uses
	// the exact value read immediately before its storage-level CAS.
	if err := os.WriteFile(logPath, nil, 0644); err != nil {
		t.Fatalf("clear bd log: %v", err)
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatalf("reset rollback state: %v", err)
	}
	t.Setenv("DOG_ROLLBACK_DESCRIPTION", "merged metadata")
	restoreFailedDogSlingSource(townRoot, "gt-new", townRoot, "deacon/dogs/alpha", "merged metadata", "open", "", &beadInfo{Status: "open"})
	logData, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log after ambiguous metadata write: %v", err)
	}
	if !strings.Contains(string(logData), "description='merged metadata'") {
		t.Fatalf("rollback did not CAS against the latest description:\n%s", logData)
	}
	t.Setenv("DOG_ROLLBACK_DESCRIPTION", "")

	// A newer assignment must survive a delayed rollback from this dispatch.
	if err := os.WriteFile(logPath, nil, 0644); err != nil {
		t.Fatalf("clear bd log for race: %v", err)
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatalf("reset rollback race state: %v", err)
	}
	t.Setenv("DOG_ROLLBACK_RACE", "1")
	restoreFailedDogSlingSource(townRoot, "gt-new", townRoot, "deacon/dogs/alpha", "", "open", "", &beadInfo{Status: "open"})
	logData, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log after concurrent change: %v", err)
	}
	if !strings.Contains(string(logData), "sql UPDATE issues") {
		t.Fatalf("rollback race never reached conditional SQL:\n%s", logData)
	}
	state, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read rollback race state: %v", err)
	}
	if strings.TrimSpace(string(state)) != "beta" {
		t.Fatalf("conditional rollback overwrote newer source assignment; state=%q\n%s", state, logData)
	}
}
