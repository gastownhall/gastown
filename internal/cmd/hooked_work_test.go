package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestResolveHookLookupWorkDirUsesRouteOwnedRigDir(t *testing.T) {
	townRoot := t.TempDir()
	townBeadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir town beads: %v", err)
	}
	rigDir := filepath.Join(townRoot, "gastown", "mayor", "rig")
	if err := os.MkdirAll(filepath.Join(rigDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir rig beads: %v", err)
	}
	if err := beads.WriteRoutes(townBeadsDir, []beads.Route{
		{Prefix: "hq-", Path: "."},
		{Prefix: "ht-", Path: "gastown/mayor/rig"},
	}); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	localWorkDir := filepath.Join(townRoot, "gastown", "polecats", "toast")
	got := resolveHookLookupWorkDir(localWorkDir, "gastown/refinery", townRoot)
	if got != rigDir {
		t.Fatalf("resolveHookLookupWorkDir() = %q, want %q", got, rigDir)
	}
}

func TestResolveHookLookupWorkDirLeavesTownLevelTargetLocal(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "mayor")
	got := resolveHookLookupWorkDir(workDir, "mayor/", t.TempDir())
	if got != workDir {
		t.Fatalf("resolveHookLookupWorkDir() = %q, want %q", got, workDir)
	}
}

func TestActiveWorkMergeBeadListsDedupeAndSort(t *testing.T) {
	primary := []*beads.Issue{
		{ID: "gt-older", UpdatedAt: "2026-01-01T00:00:00Z"},
		{ID: "gt-same", UpdatedAt: "2026-01-02T00:00:00Z", Title: "durable"},
		{ID: "gt-whole", UpdatedAt: "2026-01-03T00:00:00Z"},
	}
	secondary := []*beads.Issue{
		{ID: "gt-fractional", UpdatedAt: "2026-01-03T00:00:00.1Z"},
		{ID: "gt-same", UpdatedAt: "2026-01-04T00:00:00Z", Title: "wisp"},
	}

	got := mergeBeadLists(primary, secondary)
	if len(got) != 4 {
		t.Fatalf("mergeBeadLists length = %d, want 4", len(got))
	}

	wantIDs := []string{"gt-fractional", "gt-whole", "gt-same", "gt-older"}
	for i, want := range wantIDs {
		if got[i].ID != want {
			t.Fatalf("mergeBeadLists[%d].ID = %q, want %q (all=%v)", i, got[i].ID, want, got)
		}
	}
	if got[2].Title != "durable" {
		t.Fatalf("duplicate should keep primary issue, got title %q", got[2].Title)
	}
}
