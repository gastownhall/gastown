package convoy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	beadsRouting "github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/testutil"
	beadsdk "github.com/steveyegge/beads"
)

// setupTestStoreWithPrefix opens a test store and sets a specific prefix.
func setupTestStoreWithPrefix(t *testing.T, prefix string) (beadsdk.Storage, func()) {
	t.Helper()
	testutil.SkipWithoutDoltContainer(t)
	t.Setenv("BEADS_TEST_MODE", "1")

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	doltPath := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltPath, 0755); err != nil {
		t.Skipf("cannot create test dir: %v", err)
	}

	ctx := context.Background()
	store, err := beadsdk.Open(ctx, doltPath)
	if err != nil {
		t.Skipf("beads store unavailable (CGO/Dolt required): %v", err)
	}

	if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		_ = store.Close()
		t.Skipf("SetConfig issue_prefix: %v", err)
	}

	cleanup := func() { _ = store.Close() }
	return store, cleanup
}

func TestStoreResolver_ResolveIssues_SingleStore(t *testing.T) {
	store, cleanup := setupTestStoreWithPrefix(t, "hq")
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	issue := &beadsdk.Issue{
		ID:        "hq-test1",
		Title:     "Test Issue",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Create a town root with routes pointing hq- to "."
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	os.MkdirAll(beadsDir, 0755)
	beadsRouting.WriteRoutes(beadsDir, []beadsRouting.Route{
		{Prefix: "hq-", Path: "."},
	})

	resolver := NewStoreResolver(townRoot, map[string]beadsdk.Storage{
		"hq": store,
	})

	result := resolver.ResolveIssues(ctx, []string{"hq-test1"})
	if len(result) != 1 {
		t.Fatalf("ResolveIssues returned %d issues, want 1", len(result))
	}
	if result["hq-test1"] == nil {
		t.Fatal("hq-test1 not found in result")
	}
	if string(result["hq-test1"].Status) != "open" {
		t.Errorf("status = %s, want open", result["hq-test1"].Status)
	}
}

func TestStoreResolver_ResolveIssues_CrossStore(t *testing.T) {
	hqStore, hqCleanup := setupTestStoreWithPrefix(t, "hq")
	defer hqCleanup()
	dsStore, dsCleanup := setupTestStoreWithPrefix(t, "ds")
	defer dsCleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	// Create issue in "ds" store only
	dsIssue := &beadsdk.Issue{
		ID:        "ds-abc",
		Title:     "Dashboard Issue",
		Status:    beadsdk.StatusClosed,
		Priority:  1,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := dsStore.CreateIssue(ctx, dsIssue, "test"); err != nil {
		t.Fatalf("CreateIssue ds: %v", err)
	}

	// Create issue in "hq" store
	hqIssue := &beadsdk.Issue{
		ID:        "hq-xyz",
		Title:     "HQ Issue",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := hqStore.CreateIssue(ctx, hqIssue, "test"); err != nil {
		t.Fatalf("CreateIssue hq: %v", err)
	}

	// Set up routes
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	os.MkdirAll(beadsDir, 0755)
	beadsRouting.WriteRoutes(beadsDir, []beadsRouting.Route{
		{Prefix: "hq-", Path: "."},
		{Prefix: "ds-", Path: "dashboard"},
	})

	resolver := NewStoreResolver(townRoot, map[string]beadsdk.Storage{
		"hq":        hqStore,
		"dashboard": dsStore,
	})

	// Resolve both cross-store IDs
	result := resolver.ResolveIssues(ctx, []string{"ds-abc", "hq-xyz"})
	if len(result) != 2 {
		t.Fatalf("ResolveIssues returned %d issues, want 2", len(result))
	}
	if result["ds-abc"] == nil {
		t.Fatal("ds-abc not found in result")
	}
	if string(result["ds-abc"].Status) != "closed" {
		t.Errorf("ds-abc status = %s, want closed", result["ds-abc"].Status)
	}
	if result["hq-xyz"] == nil {
		t.Fatal("hq-xyz not found in result")
	}
	if string(result["hq-xyz"].Status) != "open" {
		t.Errorf("hq-xyz status = %s, want open", result["hq-xyz"].Status)
	}
}

func TestStoreResolver_NilStores(t *testing.T) {
	resolver := NewStoreResolver("/nonexistent", nil)
	result := resolver.ResolveIssues(context.Background(), []string{"ds-abc"})
	if len(result) != 0 {
		t.Errorf("expected empty result for nil stores, got %d", len(result))
	}
}

func TestStoreResolver_EmptyIDs(t *testing.T) {
	resolver := NewStoreResolver("/nonexistent", map[string]beadsdk.Storage{})
	result := resolver.ResolveIssues(context.Background(), nil)
	if len(result) != 0 {
		t.Errorf("expected empty result for nil IDs, got %d", len(result))
	}
}

func TestStoreResolver_StoreForID_ExternalFormat(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	os.MkdirAll(beadsDir, 0755)
	beadsRouting.WriteRoutes(beadsDir, []beadsRouting.Route{
		{Prefix: "ds-", Path: "dashboard"},
	})

	resolver := NewStoreResolver(townRoot, nil)
	storeName := resolver.storeForID("external:ds:ds-abc")
	if storeName != "dashboard" {
		t.Errorf("storeForID(external:ds:ds-abc) = %q, want %q", storeName, "dashboard")
	}
}
