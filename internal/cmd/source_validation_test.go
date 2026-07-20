package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestRoutedIssueBeadsUsesTownRoutesForCustomPrefix(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)

	_, gotCurrent, gotRouted := routedIssueBeads(workDir, "bd-source")
	if gotCurrent != currentBeadsDir {
		t.Fatalf("current beads dir = %q, want %q", gotCurrent, currentBeadsDir)
	}
	if gotRouted != ownerBeadsDir {
		t.Fatalf("routed beads dir = %q, want %q", gotRouted, ownerBeadsDir)
	}
}

func TestSourceRouteContextNamesCurrentAndRoutedDB(t *testing.T) {
	context := sourceRouteContext("/town/gastown/.beads", "/town/beads/.beads")
	for _, want := range []string{"current_db=/town/gastown/.beads", "routed_db=/town/beads/.beads"} {
		if !strings.Contains(context, want) {
			t.Fatalf("source route context %q missing %q", context, want)
		}
	}
}

func TestResolveSubmitSourceIssueIgnoresCurrentRigMirror(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	installSubmitSourceBDStub(t, currentBeadsDir, ownerBeadsDir, false)

	source, err := resolveSubmitSourceIssue(workDir, "bd-source")
	if err != nil {
		t.Fatalf("resolveSubmitSourceIssue: %v", err)
	}
	if source.Issue.Title != "owner source" {
		t.Fatalf("source title = %q, want routed owner source (current-rig mirror must be ignored)", source.Issue.Title)
	}
	if source.CurrentBeadsDir != currentBeadsDir || source.RoutedBeadsDir != ownerBeadsDir {
		t.Fatalf("route = current %q routed %q, want current %q routed %q", source.CurrentBeadsDir, source.RoutedBeadsDir, currentBeadsDir, ownerBeadsDir)
	}
}

func TestResolveSubmitSourceIssueFailureNamesRoutingContext(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	installSubmitSourceBDStub(t, currentBeadsDir, ownerBeadsDir, true)

	_, err := resolveSubmitSourceIssue(workDir, "bd-source")
	if err == nil {
		t.Fatal("resolveSubmitSourceIssue succeeded, want routed owner lookup failure")
	}
	errText := err.Error()
	for _, want := range []string{"source_issue bd-source could not be resolved", "current_db=" + currentBeadsDir, "routed_db=" + ownerBeadsDir} {
		if !strings.Contains(errText, want) {
			t.Fatalf("error %q missing %q", errText, want)
		}
	}
}

func TestValidateMergeRequestSourceUsesPreResolvedSource(t *testing.T) {
	mr := &beads.Issue{ID: "gt-mr", Description: "source_issue: bd-source\n"}
	if err := validateMergeRequestSource(mr, "bd-source", nil); err == nil || !strings.Contains(err.Error(), "pre-resolved") {
		t.Fatalf("validateMergeRequestSource without source = %v, want pre-resolved error", err)
	}
	if err := validateMergeRequestSource(mr, "bd-source", &beads.Issue{ID: "bd-source", Type: "task"}); err != nil {
		t.Fatalf("validateMergeRequestSource with routed source: %v", err)
	}
}

func setupRoutedSourceTestTown(t *testing.T) (workDir, currentBeadsDir, ownerBeadsDir string) {
	t.Helper()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0o755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write town sentinel: %v", err)
	}

	workDir = filepath.Join(townRoot, "gastown", "polecats", "refuge", "checkout")
	currentBeadsDir = filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")
	ownerBeadsDir = filepath.Join(townRoot, "beads", "mayor", "rig", ".beads")
	townBeadsDir := filepath.Join(townRoot, ".beads")
	for _, dir := range []string{filepath.Join(workDir, ".beads"), currentBeadsDir, ownerBeadsDir, townBeadsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(workDir, ".beads", "redirect"), []byte("../../../mayor/rig/.beads\n"), 0o644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}
	if err := beads.WriteRoutes(townBeadsDir, []beads.Route{
		{Prefix: "gt-", Path: "gastown/mayor/rig"},
		{Prefix: "bd-", Path: "beads/mayor/rig"},
	}); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	return workDir, currentBeadsDir, ownerBeadsDir
}

func installSubmitSourceBDStub(t *testing.T, currentBeadsDir, ownerBeadsDir string, ownerMissing bool) {
	t.Helper()
	binDir := t.TempDir()
	ownerCase := fmt.Sprintf(`
if [ "$BEADS_DIR" = %q ]; then
  echo '[{"id":"bd-source","title":"owner source","status":"open","priority":1,"issue_type":"task"}]'
  exit 0
fi`, ownerBeadsDir)
	if ownerMissing {
		ownerCase = fmt.Sprintf(`
if [ "$BEADS_DIR" = %q ]; then
  echo "Issue not found in owner" >&2
  exit 1
fi`, ownerBeadsDir)
	}
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--allow-stale" ]; then
  shift
fi
if [ "$1" = "version" ]; then
  echo "bd stub"
  exit 0
fi
if [ "$1" = "show" ] && [ "$2" = "bd-source" ]; then
  if [ "$BEADS_DIR" = %q ]; then
    echo '[{"id":"bd-source","title":"current mirror","status":"open","priority":1,"issue_type":"task"}]'
    exit 0
  fi
%s
  echo "Issue not found in $BEADS_DIR" >&2
  exit 1
fi
echo "unexpected bd command: $*" >&2
exit 1
`, currentBeadsDir, ownerCase)
	path := filepath.Join(binDir, "bd")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	beads.ResetBdAllowStaleCacheForTest()
}
