package workflow

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// fakeBD installs a POSIX-shell bd on PATH that keeps one issue's labels in a
// state file and applies update --add-label/--remove-label to it.
func fakeBD(t *testing.T, id string, labels []string) (stateFile, logFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX shell fake bd")
	}
	binDir := t.TempDir()
	stateDir := t.TempDir()
	stateFile = filepath.Join(stateDir, "labels")
	logFile = filepath.Join(stateDir, "bd.log")
	if err := os.WriteFile(stateFile, []byte(strings.Join(labels, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$BD_LOG"
[ "$1" = "--allow-stale" ] && shift
case "$1" in
  version) echo "bd version test"; exit 0 ;;
  show)
    labels=$(awk 'NF{printf "%s\"%s\"", (n++?",":""), $0}' "$BD_STATE")
    printf '[{"id":"%s","title":"T","status":"open","issue_type":"task","labels":[%s]}]\n' "$2" "$labels"
    exit 0 ;;
  update)
    shift 2
    for a in "$@"; do
      case "$a" in
        --add-label=*) printf '%s\n' "${a#--add-label=}" >> "$BD_STATE" ;;
        --remove-label=*) grep -vxF "${a#--remove-label=}" "$BD_STATE" > "$BD_STATE.tmp"; mv "$BD_STATE.tmp" "$BD_STATE" ;;
      esac
    done
    exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_LOG", logFile)
	t.Setenv("BD_STATE", stateFile)
	return stateFile, logFile
}

func TestBeadsItems_StepLabelReplaced(t *testing.T) {
	entered := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	stateFile, logFile := fakeBD(t, "gt-abc", []string{
		"gtb:wf:task", "gtb:step:spec", "gtb:entered:" + entered.Format(time.RFC3339), "gtb:rig:gastown",
	})
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	items := NewBeadsItems(beads.NewIsolated(workDir))

	before, err := items.Get("gt-abc")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if before.Step != "spec" || before.Kind != KindTask || !before.StepEnteredAt.Equal(entered) || before.Rig != "gastown" {
		t.Fatalf("before = %+v", before)
	}

	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	if err := items.SetStep("gt-abc", "plan", at); err != nil {
		t.Fatalf("SetStep: %v", err)
	}
	raw, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	labels := strings.Fields(string(raw))
	has := func(l string) bool {
		for _, x := range labels {
			if x == l {
				return true
			}
		}
		return false
	}
	if !has("gtb:step:plan") || has("gtb:step:spec") {
		log, _ := os.ReadFile(logFile)
		t.Fatalf("labels = %v\nbd log:\n%s", labels, log)
	}
	if !has("gtb:entered:"+at.Format(time.RFC3339)) || has("gtb:entered:"+entered.Format(time.RFC3339)) {
		t.Fatalf("entered label not replaced: %v", labels)
	}

	after, err := items.Get("gt-abc")
	if err != nil {
		t.Fatal(err)
	}
	if after.Step != "plan" || !after.StepEnteredAt.Equal(at) {
		t.Fatalf("after = %+v", after)
	}

	if err := items.SetWaiting("gt-abc", "approve-plan"); err != nil {
		t.Fatal(err)
	}
	if got, _ := items.Get("gt-abc"); got.WaitingForHuman != "approve-plan" {
		t.Fatalf("waiting = %q", got.WaitingForHuman)
	}
	if err := items.SetWaiting("gt-abc", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := items.Get("gt-abc"); got.WaitingForHuman != "" {
		t.Fatalf("waiting not cleared: %q", got.WaitingForHuman)
	}
}

func TestBeadsItems_SetBranchReplaces(t *testing.T) {
	stateFile, _ := fakeBD(t, "gt-abc", []string{"gtb:wf:task", "gtb:step:plan", "gtb:branch:polecat/old"})
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	items := NewBeadsItems(beads.NewIsolated(workDir))
	if got, _ := items.Get("gt-abc"); got == nil || got.Branch != "polecat/old" {
		t.Fatalf("before = %+v", got)
	}
	if err := items.SetBranch("gt-abc", "polecat/new"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(stateFile)
	if strings.Contains(string(raw), "gtb:branch:polecat/old") || !strings.Contains(string(raw), "gtb:branch:polecat/new") {
		t.Fatalf("labels = %q", raw)
	}
	if got, _ := items.Get("gt-abc"); got.Branch != "polecat/new" {
		t.Fatalf("after = %+v", got)
	}
}

func TestMemItems_Lifecycle(t *testing.T) {
	items := NewMemItems()
	it, err := items.Create("gastown", KindFix, "Broken thing", "")
	if err != nil {
		t.Fatal(err)
	}
	if it.Step != "report" || it.Kind != KindFix || it.Rig != "gastown" || it.StepEnteredAt.IsZero() {
		t.Fatalf("created = %+v", it)
	}
	at := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	if err := items.SetStep(it.ID, "reproduce", at); err != nil {
		t.Fatal(err)
	}
	if err := items.SetWaiting(it.ID, "approve"); err != nil {
		t.Fatal(err)
	}
	if err := items.SetBranch(it.ID, "polecat/p1"); err != nil {
		t.Fatal(err)
	}
	got, err := items.Get(it.ID)
	if err != nil || got.Step != "reproduce" || !got.StepEnteredAt.Equal(at) || got.WaitingForHuman != "approve" || got.Branch != "polecat/p1" {
		t.Fatalf("got = %+v, %v", got, err)
	}
	if _, err := items.Create("other", KindTask, "x", ""); err != nil {
		t.Fatal(err)
	}
	list, err := items.List("gastown")
	if err != nil || len(list) != 1 || list[0].ID != it.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, err := items.Get("missing"); err == nil {
		t.Fatal("Get(missing) succeeded")
	}
}
