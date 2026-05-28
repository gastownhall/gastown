package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCheckAndCloseCompletedConvoys_SkipsAfterJsonlReimport reproduces the
// duplicate-notification loop from gst-4a3.
//
// Scenario:
//  1. Patrol runs once: convoy is open, all tracked issues are closed → auto-close
//     fires, notification is sent, and gt:convoy-auto-closed is stamped on the
//     convoy bead.
//  2. A .beads/issues.jsonl auto-import resurrects the convoy back to status=open
//     (the jsonl row predates the close). The label, which lives outside the
//     issue row, survives.
//  3. Patrol runs again: the previous fix (description-based completion_notified_at)
//     was ALSO blown away by the reimport, so it would re-fire. The label-based
//     guard must catch this and suppress.
//
// The test simulates the reimport by having the bd stub respond differently
// before and after the label is stamped — the marker file flips bd's perception
// of the convoy's labels for the next patrol.
func TestCheckAndCloseCompletedConvoys_SkipsAfterJsonlReimport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	townRoot, townBeads, _ := makeExternalTrackingTownWorkspace(t)
	chdirExternalTrackingTest(t, townRoot)

	binDir := t.TempDir()
	markerPath := filepath.Join(binDir, "auto-closed.marker")
	mailLogPath := filepath.Join(binDir, "mail.log")
	bdLogPath := filepath.Join(binDir, "bd.log")

	bdScript := `#!/bin/sh
echo "$@" >> "` + bdLogPath + `"
if [ "$1" = "--allow-stale" ]; then
  shift
fi
ARGS="$*"
case "$1" in
  version)
    exit 0
    ;;
  list)
    # The first list call is the labelled query: --label=gt:convoy.
    # The fallback "legacy" call has no label filter — return empty there.
    case "$ARGS" in
      *--label=gt:convoy*)
        if [ -f "` + markerPath + `" ]; then
          LABELS='["gt:convoy","gt:convoy-auto-closed"]'
        else
          LABELS='["gt:convoy"]'
        fi
        printf '[{"id":"hq-cv-loop","title":"Loop convoy","status":"open","issue_type":"convoy","description":"Owner: mayor/","created_at":"2026-05-25T02:00:00Z","labels":%s}]\n' "$LABELS"
        exit 0
        ;;
      *)
        echo '[]'
        exit 0
        ;;
    esac
    ;;
  sql)
    # bdDepListRawIDs: tracked issues for the convoy.
    case "$ARGS" in
      *"issue_id = 'hq-cv-loop'"*)
        echo '[{"depends_on_id":"gt-tracked"}]'
        exit 0
        ;;
      *)
        echo '[]'
        exit 0
        ;;
    esac
    ;;
  show)
    case "$ARGS" in
      *hq-cv-loop*)
        if [ -f "` + markerPath + `" ]; then
          # After auto-close: completion_notified_at would normally be in
          # description, but jsonl reimport stripped it. The label is what
          # actually saves us.
          echo '[{"id":"hq-cv-loop","title":"Loop convoy","status":"open","issue_type":"convoy","description":"Owner: mayor/","created_at":"2026-05-25T02:00:00Z","labels":["gt:convoy","gt:convoy-auto-closed"]}]'
        else
          echo '[{"id":"hq-cv-loop","title":"Loop convoy","status":"open","issue_type":"convoy","description":"Owner: mayor/","created_at":"2026-05-25T02:00:00Z","labels":["gt:convoy"]}]'
        fi
        exit 0
        ;;
      *gt-tracked*)
        echo '[{"id":"gt-tracked","title":"Tracked task","status":"closed","issue_type":"task"}]'
        exit 0
        ;;
      *)
        echo '[]'
        exit 0
        ;;
    esac
    ;;
  close)
    exit 0
    ;;
  update)
    exit 0
    ;;
  label)
    if [ "$2" = "add" ] && [ "$4" = "gt:convoy-auto-closed" ]; then
      touch "` + markerPath + `"
    fi
    exit 0
    ;;
esac
exit 0
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	gtScript := `#!/bin/sh
if [ "$1" = "mail" ] && [ "$2" = "send" ]; then
  echo "$@" >> "` + mailLogPath + `"
fi
exit 0
`
	gtPath := filepath.Join(binDir, "gt")
	if err := os.WriteFile(gtPath, []byte(gtScript), 0755); err != nil {
		t.Fatalf("write gt stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// First patrol — should close + notify + stamp label.
	out1, err := captureConvoyStdoutErr(t, func() error {
		_, err := checkAndCloseCompletedConvoys(townBeads, false)
		return err
	})
	if err != nil {
		t.Fatalf("first patrol: %v\nstdout:\n%s", err, out1)
	}
	if !strings.Contains(out1, "Auto-closed convoy") {
		t.Fatalf("first patrol should auto-close; stdout:\n%s", out1)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("auto-closed label marker missing after first patrol: %v", err)
	}

	// Second patrol — the convoy is "open" again (jsonl reimport), but the
	// label survives. Expect a silent skip: no auto-close, no notification.
	out2, err := captureConvoyStdoutErr(t, func() error {
		_, err := checkAndCloseCompletedConvoys(townBeads, false)
		return err
	})
	if err != nil {
		t.Fatalf("second patrol: %v\nstdout:\n%s", err, out2)
	}
	if strings.Contains(out2, "Auto-closed convoy") {
		t.Fatalf("second patrol must NOT re-close the convoy; stdout:\n%s", out2)
	}

	mailBytes, err := os.ReadFile(mailLogPath)
	if err != nil {
		// Empty mail log is fine — but if completely missing the gt stub may
		// not have been hit. Fall back to treating "no file" as zero mails.
		mailBytes = nil
	}
	mailSends := strings.Count(string(mailBytes), "mail send")
	if mailSends > 1 {
		t.Fatalf("duplicate convoy-complete mails fired (got %d, want ≤1):\n%s", mailSends, string(mailBytes))
	}
}
