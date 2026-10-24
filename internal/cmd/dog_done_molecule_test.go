package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func stubDogMoleculeSeams(t *testing.T, work []*beads.Issue, listErr error, closeErr map[string]error) *[]string {
	t.Helper()
	prevList, prevClose := listDogHookedWorkFn, closeDogFormulaWispFn
	t.Cleanup(func() {
		listDogHookedWorkFn = prevList
		closeDogFormulaWispFn = prevClose
	})

	var closed []string
	listDogHookedWorkFn = func(townRoot, dogName string) ([]*beads.Issue, error) {
		return work, listErr
	}
	closeDogFormulaWispFn = func(id, workDir, reason string) error {
		if err := closeErr[id]; err != nil {
			return err
		}
		closed = append(closed, id+"|"+reason)
		return nil
	}
	return &closed
}

func TestCloseDogFormulaMoleculesClosesOnlyFormulaRoots(t *testing.T) {
	work := []*beads.Issue{
		{ID: "hq-wisp-reaper", Status: beads.StatusHooked, Description: "attached_formula: mol-dog-reaper\nattached_at: 2026-10-01T00:00:00Z"},
		// Duplicate (returned by both the hooked and in_progress queries).
		{ID: "hq-wisp-reaper", Status: beads.StatusHooked, Description: "attached_formula: mol-dog-reaper"},
		{ID: "hq-wisp-doctor", Status: "in_progress", Description: "attached_formula: mol-dog-doctor"},
		// Ordinary bead slung to the dog: the dog closes it, not dog done.
		{ID: "hq-abc", Status: beads.StatusHooked, Description: "plain work"},
		// Formula-on-bead: the base bead is real work, not a molecule root.
		{ID: "hq-def", Status: beads.StatusHooked, Description: "attached_molecule: hq-wisp-xyz\nattached_formula: mol-polecat-work"},
		nil,
	}
	closed := stubDogMoleculeSeams(t, work, nil, nil)

	got := closeDogFormulaMolecules(t.TempDir(), "alpha", dogDoneReason)
	if got != 2 {
		t.Fatalf("closeDogFormulaMolecules() = %d, want 2", got)
	}
	want := []string{
		"hq-wisp-reaper|" + dogDoneReason,
		"hq-wisp-doctor|" + dogDoneReason,
	}
	if !reflect.DeepEqual(*closed, want) {
		t.Fatalf("closed = %q, want %q", *closed, want)
	}
}

func TestCloseDogFormulaMoleculesBestEffort(t *testing.T) {
	work := []*beads.Issue{
		{ID: "hq-wisp-a", Description: "attached_formula: mol-dog-jsonl"},
		{ID: "hq-wisp-b", Description: "attached_formula: mol-dog-jsonl"},
	}
	closed := stubDogMoleculeSeams(t, work, nil, map[string]error{"hq-wisp-a": errors.New("dolt timeout")})

	if got := closeDogFormulaMolecules(t.TempDir(), "alpha", dogDoneReason); got != 1 {
		t.Fatalf("closeDogFormulaMolecules() = %d, want 1 (a failure must not stop the rest)", got)
	}
	if want := []string{"hq-wisp-b|" + dogDoneReason}; !reflect.DeepEqual(*closed, want) {
		t.Fatalf("closed = %q, want %q", *closed, want)
	}

	stubDogMoleculeSeams(t, nil, errors.New("list failed"), nil)
	if got := closeDogFormulaMolecules(t.TempDir(), "alpha", dogDoneReason); got != 0 {
		t.Fatalf("closeDogFormulaMolecules() with list error = %d, want 0", got)
	}
}

// TestCloseDogFormulaMoleculesClosesStepsThenRoot drives the real bd seams
// against a fake bd: every step of the hooked molecule and then the root must
// be force-closed (steps are chained by blocks deps, so a plain close of a
// later step fails while an earlier one is open).
func TestCloseDogFormulaMoleculesClosesStepsThenRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock")
	}
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "bd.log")
	installFakeBD(t, `#!/bin/sh
echo "$*" >> "`+logPath+`"
all="$*"
case "$all" in
*query*'status="hooked"'*'deacon/dogs/alpha'*)
  printf '%s\n' '[{"id":"hq-wisp-root","title":"mol-dog-reaper","status":"hooked","assignee":"deacon/dogs/alpha","ephemeral":true,"description":"attached_formula: mol-dog-reaper"},{"id":"hq-plain","title":"plain","status":"hooked","assignee":"deacon/dogs/alpha","description":"no fields"}]'
  exit 0 ;;
*query*)
  printf '[]\n'; exit 0 ;;
*list*--parent=hq-wisp-root*)
  printf '%s\n' '[{"id":"hq-wisp-s1","title":"Scan","status":"closed"},{"id":"hq-wisp-s2","title":"Reap","status":"open"},{"id":"hq-wisp-s3","title":"Report","status":"open"}]'
  exit 0 ;;
*list*)
  printf '[]\n'; exit 0 ;;
*close*)
  case "$all" in *--force*) exit 0 ;; esac
  echo "cannot close blocked issue" >&2; exit 1 ;;
esac
printf '[]\n'
exit 0
`)
	t.Setenv("BEADS_DIR", "")

	if got := closeDogFormulaMolecules(townRoot, "alpha", dogDoneReason); got != 1 {
		data, _ := os.ReadFile(logPath)
		t.Fatalf("closeDogFormulaMolecules() = %d, want 1\nbd calls:\n%s", got, data)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	var closes []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.Contains(line, "close") {
			closes = append(closes, line)
		}
	}
	if len(closes) != 2 {
		t.Fatalf("close calls = %q, want one for the open steps and one for the root", closes)
	}
	steps, root := closes[0], closes[1]
	for _, want := range []string{"--force", "hq-wisp-s2", "hq-wisp-s3"} {
		if !strings.Contains(steps, want) {
			t.Errorf("step close %q missing %q", steps, want)
		}
	}
	if strings.Contains(steps, "hq-wisp-s1") {
		t.Errorf("step close %q re-closed an already closed step", steps)
	}
	for _, want := range []string{"--force", "hq-wisp-root", dogDoneReason} {
		if !strings.Contains(root, want) {
			t.Errorf("root close %q missing %q", root, want)
		}
	}
	if strings.Contains(strings.Join(closes, "\n"), "hq-plain") {
		t.Errorf("plain hooked bead was closed: %q", closes)
	}
}
