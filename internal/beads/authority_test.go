package beads

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	beadsdk "github.com/steveyegge/beads"
)

func TestAuthorityForBeadRoutesByPrefix(t *testing.T) {
	town := newAuthorityTown(t)

	auth := NewAuthority(town.root)

	rig := auth.ForBead("gt-abc")
	if rig.BeadsDir() != town.rigBeads {
		t.Fatalf("ForBead(gt-abc).BeadsDir() = %q, want %q", rig.BeadsDir(), town.rigBeads)
	}
	if rig.WorkDir() != town.rigDir {
		t.Fatalf("ForBead(gt-abc).WorkDir() = %q, want %q", rig.WorkDir(), town.rigDir)
	}
	if !rig.Routed() {
		t.Fatal("ForBead(gt-abc) should be routed")
	}

	hq := auth.ForBead("hq-mayor")
	if hq.BeadsDir() != town.townBeads {
		t.Fatalf("ForBead(hq-mayor).BeadsDir() = %q, want %q", hq.BeadsDir(), town.townBeads)
	}
	if hq.WorkDir() != town.root {
		t.Fatalf("ForBead(hq-mayor).WorkDir() = %q, want %q", hq.WorkDir(), town.root)
	}
	if !hq.Routed() {
		t.Fatal("ForBead(hq-mayor) should be routed")
	}
}

func TestAuthorityForBeadUnknownPrefixUsesFallback(t *testing.T) {
	town := newAuthorityTown(t)
	auth := NewAuthority(town.root)

	s := auth.ForBead("xx-unknown")
	if s.BeadsDir() != town.townBeads {
		t.Fatalf("unknown prefix BeadsDir() = %q, want town fallback %q", s.BeadsDir(), town.townBeads)
	}
	if s.Routed() {
		t.Fatal("unknown prefix should not be routed")
	}
}

func TestAuthorityFromBeadsDirFindsTownRoutesFromWorktree(t *testing.T) {
	town := newAuthorityTown(t)
	worktreeBeads := filepath.Join(town.root, "gastown", "polecats", "chrome", "gastown", ".beads")
	if err := os.MkdirAll(worktreeBeads, 0755); err != nil {
		t.Fatal(err)
	}

	auth := NewAuthorityFromBeadsDir(worktreeBeads)
	s := auth.ForBead("gt-abc")
	if s.BeadsDir() != town.rigBeads {
		t.Fatalf("worktree ForBead(gt-abc).BeadsDir() = %q, want %q", s.BeadsDir(), town.rigBeads)
	}

	hq := auth.ForBead("hq-wisp-abc")
	if hq.BeadsDir() != town.townBeads {
		t.Fatalf("worktree ForBead(hq-wisp).BeadsDir() = %q, want %q", hq.BeadsDir(), town.townBeads)
	}
}

func TestAuthorityForBeadFollowsRedirect(t *testing.T) {
	town := newAuthorityTown(t)
	canonical := filepath.Join(town.root, "canonical", ".beads")
	if err := os.MkdirAll(canonical, 0755); err != nil {
		t.Fatal(err)
	}
	redirect := filepath.Join(town.rigDir, ".beads", "redirect")
	if err := os.WriteFile(redirect, []byte(canonical+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s := NewAuthority(town.root).ForBead("gt-abc")
	if s.BeadsDir() != canonical {
		t.Fatalf("redirected BeadsDir() = %q, want %q", s.BeadsDir(), canonical)
	}
	if s.WorkDir() != filepath.Dir(canonical) {
		t.Fatalf("redirected WorkDir() = %q, want %q", s.WorkDir(), filepath.Dir(canonical))
	}
}

func TestAuthorityForAgentBeadStaysInTown(t *testing.T) {
	town := newAuthorityTown(t)
	s := NewAuthority(town.root).ForAgentBead("gt-gastown-polecat-Toast")
	if s.BeadsDir() != town.townBeads {
		t.Fatalf("ForAgentBead BeadsDir() = %q, want town %q", s.BeadsDir(), town.townBeads)
	}
	if s.WorkDir() != town.root {
		t.Fatalf("ForAgentBead WorkDir() = %q, want town root %q", s.WorkDir(), town.root)
	}
}

func TestAuthorityFromBeadsDirWithoutTownFallsBack(t *testing.T) {
	fallback := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(fallback, 0755); err != nil {
		t.Fatal(err)
	}
	s := NewAuthorityFromBeadsDir(fallback).ForBead("gt-abc")
	if s.BeadsDir() != fallback {
		t.Fatalf("no-town BeadsDir() = %q, want %q", s.BeadsDir(), fallback)
	}
	if s.Routed() {
		t.Fatal("no-town prefix should not be routed")
	}
}

type authorityTown struct {
	root      string
	townBeads string
	rigDir    string
	rigBeads  string
}

func newAuthorityTown(t *testing.T) authorityTown {
	t.Helper()
	root := t.TempDir()
	townBeads := filepath.Join(root, ".beads")
	rigDir := filepath.Join(root, "gastown", "mayor", "rig")
	rigBeads := filepath.Join(rigDir, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rigBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	routes := `{"prefix": "gt-", "path": "gastown/mayor/rig"}
{"prefix": "hq-", "path": "."}
`
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatal(err)
	}
	return authorityTown{root: root, townBeads: townBeads, rigDir: rigDir, rigBeads: rigBeads}
}

func TestSessionShowReadsRoutedDatabase(t *testing.T) {
	town := newAuthorityTown(t)
	logPath := installAuthorityBDMock(t)

	issue, err := NewAuthority(town.root).ForBead("gt-abc").Show()
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	if issue == nil || issue.ID != "gt-abc" || issue.Title != "routed" {
		t.Fatalf("Show() issue = %+v, want id=gt-abc title=routed", issue)
	}

	logOutput := readMockBDLog(t, logPath)
	if !strings.Contains(logOutput, "BEADS_DIR="+town.rigBeads) {
		t.Fatalf("Show did not pin BEADS_DIR to rig database\nlog:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "show gt-abc") {
		t.Fatalf("Show did not invoke bd show\nlog:\n%s", logOutput)
	}
}

func TestSessionHookUpdateCloseUseRoutedDatabase(t *testing.T) {
	town := newAuthorityTown(t)
	logPath := installAuthorityBDMock(t)
	s := NewAuthority(town.root).ForBead("gt-abc")

	if err := s.Hook("gastown/polecats/Toast"); err != nil {
		t.Fatalf("Hook() error = %v", err)
	}
	if err := s.Close("done"); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	logOutput := readMockBDLog(t, logPath)
	for _, want := range []string{
		"BEADS_DIR=" + town.rigBeads,
		"--status=hooked",
		"--assignee=gastown/polecats/Toast",
		"close gt-abc --reason=done",
	} {
		if !strings.Contains(logOutput, want) {
			t.Fatalf("missing %q\nlog:\n%s", want, logOutput)
		}
	}
}

func TestSessionForAgentBeadShowStaysInTown(t *testing.T) {
	town := newAuthorityTown(t)
	logPath := installAuthorityBDMock(t)

	issue, err := NewAuthority(town.root).ForAgentBead("gt-gastown-polecat-Toast").Show()
	if err != nil {
		t.Fatalf("ForAgentBead Show() error = %v", err)
	}
	if issue == nil || issue.ID != "gt-gastown-polecat-Toast" {
		t.Fatalf("Show() issue = %+v", issue)
	}

	logOutput := readMockBDLog(t, logPath)
	if !strings.Contains(logOutput, "BEADS_DIR="+town.townBeads) {
		t.Fatalf("ForAgentBead Show did not pin town BEADS_DIR\nlog:\n%s", logOutput)
	}
	if strings.Contains(logOutput, "BEADS_DIR="+town.rigBeads) {
		t.Fatalf("ForAgentBead Show leaked rig BEADS_DIR\nlog:\n%s", logOutput)
	}
}

func TestSessionShowUsesStoreAdapter(t *testing.T) {
	town := newAuthorityTown(t)
	store := newMockStorage()
	if err := store.CreateIssue(context.Background(), &beadsdk.Issue{
		Title:    "from-store",
		Priority: 2,
	}, "actor"); err != nil {
		t.Fatal(err)
	}

	issue, err := NewAuthority(town.root).ForBead("test-1").WithStore(store).Show()
	if err != nil {
		t.Fatalf("Show() via store error = %v", err)
	}
	if issue == nil || issue.Title != "from-store" {
		t.Fatalf("Show() issue = %+v, want title from-store", issue)
	}
}

func installAuthorityBDMock(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	if runtime.GOOS == "windows" {
		t.Skip("authority bd mock is unix-only")
	}
	script := `#!/bin/sh
LOG_FILE='` + logPath + `'
printf 'BEADS_DIR=%s CWD=%s %s\n' "${BEADS_DIR:-}" "$(pwd)" "$*" >> "$LOG_FILE"

cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done

case "$cmd" in
  show)
    id=""
    for arg in "$@"; do
      case "$arg" in
        --*) ;;
        show) ;;
        *) id="$arg"; break ;;
      esac
    done
    printf '[{"id":"%s","title":"routed","status":"open","priority":2}]\n' "$id"
    exit 0
    ;;
  update|close|version)
    echo ok
    exit 0
    ;;
  *)
    echo ok
    exit 0
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ResetBdAllowStaleCacheForTest()
	t.Setenv("GT_BD_TIMEOUT_SEC", "5")
	return logPath
}
