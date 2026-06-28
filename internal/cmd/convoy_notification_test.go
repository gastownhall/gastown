package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNotifyConvoyCompletion_StampsAndSkipsDuplicate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	statePath := filepath.Join(binDir, "notified.state")
	mailLogPath := filepath.Join(binDir, "mail.log")
	exportLogPath := filepath.Join(binDir, "export.log")
	bdPath := filepath.Join(binDir, "bd")
	gtPath := filepath.Join(binDir, "gt")

	bdScript := `#!/bin/sh
STATE="` + statePath + `"
EXPORT_LOG="` + exportLogPath + `"
if [ "$1" = "--allow-stale" ]; then
  shift
fi
case "$1" in
  version)
    exit 0
    ;;
  show)
    if [ -f "$STATE" ]; then
      printf '%s\n' '[{"id":"hq-cv-dup","description":"Owner: mayor/\ncompletion_notified_at: 2026-05-25T02:30:00Z","created_at":"2026-05-25T02:00:00Z"}]'
    else
      printf '%s\n' '[{"id":"hq-cv-dup","description":"Owner: mayor/","created_at":"2026-05-25T02:00:00Z"}]'
    fi
    exit 0
    ;;
  update)
    touch "$STATE"
    exit 0
    ;;
  export)
    echo "$@" >> "$EXPORT_LOG"
    exit 0
    ;;
  sql)
    printf '%s\n' '[]'
    exit 0
    ;;
esac
exit 0
`
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	gtScript := `#!/bin/sh
if [ "$1" = "mail" ] && [ "$2" = "send" ]; then
  echo "$@" >> "` + mailLogPath + `"
fi
exit 0
`
	if err := os.WriteFile(gtPath, []byte(gtScript), 0755); err != nil {
		t.Fatalf("write gt stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	notifyConvoyCompletion(townRoot, "hq-cv-dup", "Duplicate Guard")
	notifyConvoyCompletion(townRoot, "hq-cv-dup", "Duplicate Guard")

	data, err := os.ReadFile(mailLogPath)
	if err != nil {
		t.Fatalf("read mail log: %v", err)
	}
	if got := strings.Count(string(data), "mail send"); got != 1 {
		t.Fatalf("mail sends = %d, want 1; log:\n%s", got, string(data))
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("completion notification state was not recorded: %v", err)
	}
	exportData, err := os.ReadFile(exportLogPath)
	if err != nil {
		t.Fatalf("read export log: %v", err)
	}
	if got := strings.Count(string(exportData), "export -o"); got != 1 {
		t.Fatalf("bd export calls = %d, want 1; log:\n%s", got, string(exportData))
	}
	if !strings.Contains(string(exportData), filepath.Join(townRoot, ".beads", "issues.jsonl")) {
		t.Fatalf("bd export did not target town issues.jsonl; log:\n%s", string(exportData))
	}
}

func TestCloseConvoyIfComplete_ExportsJSONLBeforeNotification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	binDir := t.TempDir()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	orderPath := filepath.Join(binDir, "order.log")
	bdPath := filepath.Join(binDir, "bd")
	gtPath := filepath.Join(binDir, "gt")

	bdScript := `#!/bin/sh
ORDER="` + orderPath + `"
if [ "$1" = "--allow-stale" ]; then
  shift
fi
case "$1" in
  version)
    exit 0
    ;;
  close)
    echo close >> "$ORDER"
    exit 0
    ;;
  export)
    echo export:"$@" >> "$ORDER"
    exit 0
    ;;
  show)
    printf '%s\n' '[{"id":"hq-cv-done","description":"Owner: mayor/","created_at":"2026-05-25T02:00:00Z"}]'
    exit 0
    ;;
  update)
    echo update >> "$ORDER"
    exit 0
    ;;
  sql)
    printf '%s\n' '[]'
    exit 0
    ;;
esac
exit 1
`
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	gtScript := `#!/bin/sh
if [ "$1" = "mail" ] && [ "$2" = "send" ]; then
  echo mail >> "` + orderPath + `"
fi
exit 0
`
	if err := os.WriteFile(gtPath, []byte(gtScript), 0755); err != nil {
		t.Fatalf("write gt stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	closed, err := closeConvoyIfComplete(townRoot, "hq-cv-done", "Done Convoy", []trackedIssueInfo{
		{ID: "gt-done", Status: "closed"},
	}, false)
	if err != nil {
		t.Fatalf("closeConvoyIfComplete returned error: %v", err)
	}
	if !closed {
		t.Fatal("closeConvoyIfComplete returned closed=false, want true")
	}

	data, err := os.ReadFile(orderPath)
	if err != nil {
		t.Fatalf("read order log: %v", err)
	}
	got := strings.TrimSpace(string(data))
	want := strings.Join([]string{
		"close",
		"export:export -o " + filepath.Join(townRoot, ".beads", "issues.jsonl"),
		"update",
		"export:export -o " + filepath.Join(townRoot, ".beads", "issues.jsonl"),
		"mail",
	}, "\n")
	if got != want {
		t.Fatalf("operation order mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}
