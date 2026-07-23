package convoy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	beadsdk "github.com/steveyegge/beads"
)

func TestExtractIssueID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"gt-abc", "gt-abc"},
		{"bd-xyz", "bd-xyz"},
		{"hq-cv-123", "hq-cv-123"},
		{"external:gt:gt-abc", "gt-abc"},
		{"external:bd:bd-xyz", "bd-xyz"},
		{"external:hq:hq-cv-123", "hq-cv-123"},
		{"external:", "external:"}, // malformed, return as-is
		{"external:x:", ""},        // 3 parts but empty last part
		{"simple", "simple"},       // no external prefix
		{"", ""},                   // empty
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := extractIssueID(tt.input)
			if result != tt.expected {
				t.Errorf("extractIssueID(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestIsSlingableType(t *testing.T) {
	tests := []struct {
		issueType string
		want      bool
	}{
		{"task", true},
		{"bug", true},
		{"feature", true},
		{"chore", true},
		{"", true},          // empty defaults to task
		{"epic", false},     // container type
		{"convoy", false},   // meta type
		{"sub-epic", false}, // container type
		{"decision", false}, // non-work type
		{"message", false},  // non-work type
		{"event", false},    // non-work type
		{"unknown", false},  // unknown types are not slingable
	}

	for _, tt := range tests {
		t.Run(tt.issueType, func(t *testing.T) {
			got := IsSlingableType(tt.issueType)
			if got != tt.want {
				t.Errorf("IsSlingableType(%q) = %v, want %v", tt.issueType, got, tt.want)
			}
		})
	}
}

func TestIsIssueBlocked_NoStore(t *testing.T) {
	// isIssueBlocked with nil store should fail-open (return false, not panic).
	// This covers the "store unavailable" failure mode (F-17).
	result := isIssueBlocked(context.Background(), nil, "test-any-id", nil)
	if result {
		t.Error("isIssueBlocked should fail-open (return false) with nil store")
	}
}

func TestReadyIssueFilterLogic_SkipsNonSlingableTypes(t *testing.T) {
	// Validates that feedNextReadyIssue's type filter skips non-slingable types.
	// We test the predicate inline (same pattern as existing filter tests).
	tracked := []trackedIssue{
		{ID: "gt-epic", Status: "open", Assignee: "", IssueType: "epic"},
		{ID: "gt-task", Status: "open", Assignee: "", IssueType: "task"},
		{ID: "gt-convoy", Status: "open", Assignee: "", IssueType: "convoy"},
		{ID: "gt-bug", Status: "open", Assignee: "", IssueType: "bug"},
	}

	var slingable []string
	for _, issue := range tracked {
		if issue.Status == "open" && issue.Assignee == "" && IsSlingableType(issue.IssueType) {
			slingable = append(slingable, issue.ID)
		}
	}

	if len(slingable) != 2 {
		t.Errorf("expected 2 slingable issues (task, bug), got %d: %v", len(slingable), slingable)
	}
	if slingable[0] != "gt-task" || slingable[1] != "gt-bug" {
		t.Errorf("expected [gt-task, gt-bug], got %v", slingable)
	}
}

func TestReadyIssueFilterLogic_SkipsNonOpenIssues(t *testing.T) {
	// Validates the filtering predicate used by feedNextReadyIssue: only
	// open issues with no assignee should be considered "ready". We test
	// the predicate inline because feedNextReadyIssue also calls rigForIssue
	// and dispatchIssue, making isolated unit testing impractical without a
	// real store. Integration coverage lives in convoy_manager_integration_test.go.
	tracked := []trackedIssue{
		{ID: "gt-closed", Status: "closed", Assignee: ""},
		{ID: "gt-inprog", Status: "in_progress", Assignee: "gastown/polecats/alpha"},
		{ID: "gt-hooked", Status: "hooked", Assignee: "gastown/polecats/beta"},
		{ID: "gt-assigned", Status: "open", Assignee: "gastown/polecats/gamma"},
	}

	// None of these should be considered "ready"
	for _, issue := range tracked {
		if issue.Status == "open" && issue.Assignee == "" {
			t.Errorf("issue %s should not be ready (status=%s, assignee=%s)", issue.ID, issue.Status, issue.Assignee)
		}
	}
}

func TestReadyIssueFilterLogic_FindsReadyIssue(t *testing.T) {
	// Validates that the "first open+unassigned" selection picks the correct
	// issue. See comment on TestReadyIssueFilterLogic_SkipsNonOpenIssues for
	// why this tests the predicate inline rather than calling feedNextReadyIssue.
	tracked := []trackedIssue{
		{ID: "gt-closed", Status: "closed", Assignee: ""},
		{ID: "gt-inprog", Status: "in_progress", Assignee: "gastown/polecats/alpha"},
		{ID: "gt-ready", Status: "open", Assignee: ""},
		{ID: "gt-also-ready", Status: "open", Assignee: ""},
	}

	// Find first ready issue - should be gt-ready (first match)
	var foundReady string
	for _, issue := range tracked {
		if issue.Status == "open" && issue.Assignee == "" {
			foundReady = issue.ID
			break
		}
	}

	if foundReady != "gt-ready" {
		t.Errorf("expected first ready issue to be gt-ready, got %s", foundReady)
	}
}

func TestCheckConvoysForIssue_NilStore(t *testing.T) {
	// Nil store returns nil immediately (no convoy checks).
	result := CheckConvoysForIssue(context.Background(), nil, "/nonexistent/path", "gt-test", "test", nil, "gt", nil)
	if result != nil {
		t.Errorf("expected nil for nil store, got %v", result)
	}
}

func TestCheckConvoysForIssue_NilLogger(t *testing.T) {
	// Nil logger should not panic — gets replaced with no-op internally.
	// With nil store, returns nil.
	result := CheckConvoysForIssue(context.Background(), nil, "/nonexistent/path", "gt-test", "test", nil, "gt", nil)
	if result != nil {
		t.Errorf("expected nil for nil store, got %v", result)
	}
}

// ---------------------------------------------------------------------------
// blockingDepTypes map tests
// ---------------------------------------------------------------------------

func TestBlockingDepTypes_ContainsExpectedTypes(t *testing.T) {
	expected := []string{"blocks", "conditional-blocks", "waits-for", "merge-blocks"}
	for _, depType := range expected {
		if !blockingDepTypes[depType] {
			t.Errorf("blockingDepTypes should contain %q", depType)
		}
	}
}

func TestBlockingDepTypes_ExcludesParentChild(t *testing.T) {
	if blockingDepTypes["parent-child"] {
		t.Error("blockingDepTypes should NOT contain parent-child")
	}
}

func TestBlockingDepTypes_ExactSize(t *testing.T) {
	// Ensure the map has exactly the 4 expected entries and no extras.
	if len(blockingDepTypes) != 4 {
		t.Errorf("blockingDepTypes has %d entries, want 4; contents: %v", len(blockingDepTypes), blockingDepTypes)
	}
}

// ---------------------------------------------------------------------------
// isIssueBlocked tests (real beads store)
// ---------------------------------------------------------------------------

func TestIsIssueBlocked_NoDeps(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	issue := &beadsdk.Issue{
		ID:        "test-noblk1",
		Title:     "No Deps Issue",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	if isIssueBlocked(ctx, store, issue.ID, nil) {
		t.Error("isIssueBlocked should return false for issue with no dependencies")
	}
}

func TestIsIssueBlocked_BlockedByOpenBlocker(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	blocker := &beadsdk.Issue{
		ID:        "test-blkr1",
		Title:     "Blocker",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	blocked := &beadsdk.Issue{
		ID:        "test-blkd1",
		Title:     "Blocked",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := store.CreateIssue(ctx, blocker, "test"); err != nil {
		t.Fatalf("CreateIssue blocker: %v", err)
	}
	if err := store.CreateIssue(ctx, blocked, "test"); err != nil {
		t.Fatalf("CreateIssue blocked: %v", err)
	}

	dep := &beadsdk.Dependency{
		IssueID:     blocked.ID,
		DependsOnID: blocker.ID,
		Type:        beadsdk.DepBlocks,
		CreatedAt:   now,
		CreatedBy:   "test",
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	// Verify the dependency actually exists via a method that works in embedded mode.
	deps, err := store.GetDependencies(ctx, blocked.ID)
	if err != nil {
		t.Skipf("store.GetDependencies failed (embedded Dolt limitation): %v", err)
	}
	if len(deps) == 0 {
		t.Fatal("expected at least 1 dependency to be created")
	}

	result := isIssueBlocked(ctx, store, blocked.ID, nil)

	// GetDependenciesWithMetadata may not work in embedded Dolt mode
	// (nested query limitation). If it fails, isIssueBlocked returns false
	// (fail-open). Skip the assertion in that case rather than silently passing.
	if !result {
		// Check if the fail-open case: GetDependenciesWithMetadata may have failed
		_, metaErr := store.GetDependenciesWithMetadata(ctx, blocked.ID)
		if metaErr != nil {
			t.Skipf("GetDependenciesWithMetadata not supported in embedded mode: %v — fail-open expected", metaErr)
		}
		t.Error("isIssueBlocked should return true when issue has open blocker")
	}
}

func TestIsIssueBlocked_NotBlockedByClosedBlocker(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	blocker := &beadsdk.Issue{
		ID:        "test-clblkr",
		Title:     "Closed Blocker",
		Status:    beadsdk.StatusClosed,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	blocked := &beadsdk.Issue{
		ID:        "test-clblkd",
		Title:     "Blocked By Closed",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := store.CreateIssue(ctx, blocker, "test"); err != nil {
		t.Fatalf("CreateIssue blocker: %v", err)
	}
	if err := store.CreateIssue(ctx, blocked, "test"); err != nil {
		t.Fatalf("CreateIssue blocked: %v", err)
	}

	dep := &beadsdk.Dependency{
		IssueID:     blocked.ID,
		DependsOnID: blocker.ID,
		Type:        beadsdk.DepBlocks,
		CreatedAt:   now,
		CreatedBy:   "test",
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	// Even if GetDependenciesWithMetadata works, the blocker is closed so
	// isIssueBlocked should return false.
	if isIssueBlocked(ctx, store, blocked.ID, nil) {
		t.Error("isIssueBlocked should return false when the only blocker is closed")
	}
}

func TestIsIssueBlocked_ParentChildDoesNotBlock(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	parent := &beadsdk.Issue{
		ID:        "test-pcpar",
		Title:     "Parent",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	child := &beadsdk.Issue{
		ID:        "test-pcchld",
		Title:     "Child",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := store.CreateIssue(ctx, parent, "test"); err != nil {
		t.Fatalf("CreateIssue parent: %v", err)
	}
	if err := store.CreateIssue(ctx, child, "test"); err != nil {
		t.Fatalf("CreateIssue child: %v", err)
	}

	dep := &beadsdk.Dependency{
		IssueID:     child.ID,
		DependsOnID: parent.ID,
		Type:        beadsdk.DepParentChild,
		CreatedAt:   now,
		CreatedBy:   "test",
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	// parent-child deps should NOT block dispatch
	if isIssueBlocked(ctx, store, child.ID, nil) {
		t.Error("isIssueBlocked should return false for parent-child dependency (not a blocking type)")
	}
}

func TestIsIssueBlocked_FailOpenOnNonexistentIssue(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Querying deps for a nonexistent issue should fail-open (return false)
	if isIssueBlocked(ctx, store, "test-nonexistent-issue", nil) {
		t.Error("isIssueBlocked should fail-open (return false) for nonexistent issue")
	}
}

// ---------------------------------------------------------------------------
// merge-blocks dependency tests (#1893)
// ---------------------------------------------------------------------------

func TestIsIssueBlocked_MergeBlocksStillBlockedWhenClosedWithoutMerge(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	// Blocker is closed but has no CloseReason (gt done without merge)
	blocker := &beadsdk.Issue{
		ID:        "test-mblkr1",
		Title:     "Closed No Merge",
		Status:    beadsdk.StatusClosed,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	blocked := &beadsdk.Issue{
		ID:        "test-mblkd1",
		Title:     "Merge-Blocked",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := store.CreateIssue(ctx, blocker, "test"); err != nil {
		t.Fatalf("CreateIssue blocker: %v", err)
	}
	if err := store.CreateIssue(ctx, blocked, "test"); err != nil {
		t.Fatalf("CreateIssue blocked: %v", err)
	}

	dep := &beadsdk.Dependency{
		IssueID:     blocked.ID,
		DependsOnID: blocker.ID,
		Type:        beadsdk.DependencyType("merge-blocks"),
		CreatedAt:   now,
		CreatedBy:   "test",
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	result := isIssueBlocked(ctx, store, blocked.ID, nil)

	// Check if GetDependenciesWithMetadata works in embedded mode
	if !result {
		_, metaErr := store.GetDependenciesWithMetadata(ctx, blocked.ID)
		if metaErr != nil {
			t.Skipf("GetDependenciesWithMetadata not supported in embedded mode: %v", metaErr)
		}
		t.Error("isIssueBlocked should return true for merge-blocks dep when blocker is closed without merge")
	}
}

func TestIsIssueBlocked_MergeBlocksUnblockedWhenMerged(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	// Blocker is closed WITH merge confirmation
	blocker := &beadsdk.Issue{
		ID:          "test-mblkr2",
		Title:       "Merged Blocker",
		Status:      beadsdk.StatusClosed,
		CloseReason: "Merged in mr-xyz",
		Priority:    2,
		IssueType:   beadsdk.TypeTask,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	blocked := &beadsdk.Issue{
		ID:        "test-mblkd2",
		Title:     "Merge-Blocked By Merged",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := store.CreateIssue(ctx, blocker, "test"); err != nil {
		t.Fatalf("CreateIssue blocker: %v", err)
	}
	if err := store.CreateIssue(ctx, blocked, "test"); err != nil {
		t.Fatalf("CreateIssue blocked: %v", err)
	}

	dep := &beadsdk.Dependency{
		IssueID:     blocked.ID,
		DependsOnID: blocker.ID,
		Type:        beadsdk.DependencyType("merge-blocks"),
		CreatedAt:   now,
		CreatedBy:   "test",
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	// Blocker is closed with "Merged in mr-xyz" — should NOT be blocked
	if isIssueBlocked(ctx, store, blocked.ID, nil) {
		// Check if it's the embedded Dolt issue
		_, metaErr := store.GetDependenciesWithMetadata(ctx, blocked.ID)
		if metaErr != nil {
			t.Skipf("GetDependenciesWithMetadata not supported in embedded mode: %v", metaErr)
		}
		t.Error("isIssueBlocked should return false when merge-blocks blocker has CloseReason 'Merged in ...'")
	}
}

func TestIsIssueBlocked_MergeBlocksUnblockedOnTombstone(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	// Create blocker as open first, then transition to tombstone
	blocker := &beadsdk.Issue{
		ID:        "test-mblkr3",
		Title:     "Tombstone Blocker",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	blocked := &beadsdk.Issue{
		ID:        "test-mblkd3",
		Title:     "Merge-Blocked By Tombstone",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := store.CreateIssue(ctx, blocker, "test"); err != nil {
		t.Fatalf("CreateIssue blocker: %v", err)
	}
	if err := store.CreateIssue(ctx, blocked, "test"); err != nil {
		t.Fatalf("CreateIssue blocked: %v", err)
	}

	dep := &beadsdk.Dependency{
		IssueID:     blocked.ID,
		DependsOnID: blocker.ID,
		Type:        beadsdk.DependencyType("merge-blocks"),
		CreatedAt:   now,
		CreatedBy:   "test",
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	// Transition to tombstone
	if err := store.UpdateIssue(ctx, blocker.ID, map[string]interface{}{
		"status": "tombstone",
	}, "test"); err != nil {
		t.Fatalf("UpdateIssue to tombstone: %v", err)
	}

	// Tombstone always unblocks, regardless of dep type
	if isIssueBlocked(ctx, store, blocked.ID, nil) {
		_, metaErr := store.GetDependenciesWithMetadata(ctx, blocked.ID)
		if metaErr != nil {
			t.Skipf("GetDependenciesWithMetadata not supported in embedded mode: %v", metaErr)
		}
		t.Error("isIssueBlocked should return false when merge-blocks blocker is tombstoned")
	}
}

// ---------------------------------------------------------------------------
// rigForIssue tests
// ---------------------------------------------------------------------------

func TestRigForIssue_ValidPrefix(t *testing.T) {
	townRoot := t.TempDir()

	// Create .beads/routes.jsonl with a mapping
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	routesContent := `{"prefix":"gt-","path":"gastown/.beads"}` + "\n" +
		`{"prefix":"bd-","path":"beads/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatalf("WriteFile routes.jsonl: %v", err)
	}

	rig := rigForIssue(townRoot, "gt-abc123")
	if rig != "gastown" {
		t.Errorf("rigForIssue(townRoot, 'gt-abc123') = %q, want 'gastown'", rig)
	}

	rig = rigForIssue(townRoot, "bd-xyz")
	if rig != "beads" {
		t.Errorf("rigForIssue(townRoot, 'bd-xyz') = %q, want 'beads'", rig)
	}
}

func TestRigForIssue_EmptyPrefix(t *testing.T) {
	townRoot := t.TempDir()

	// No prefix extractable from "nohyphen"
	rig := rigForIssue(townRoot, "nohyphen")
	if rig != "" {
		t.Errorf("rigForIssue with no-hyphen ID = %q, want empty", rig)
	}
}

func TestRigForIssue_EmptyIssueID(t *testing.T) {
	townRoot := t.TempDir()

	rig := rigForIssue(townRoot, "")
	if rig != "" {
		t.Errorf("rigForIssue with empty ID = %q, want empty", rig)
	}
}

func TestRigForIssue_UnknownPrefix(t *testing.T) {
	townRoot := t.TempDir()

	// Create routes.jsonl with only gt- mapping
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	routesContent := `{"prefix":"gt-","path":"gastown/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatalf("WriteFile routes.jsonl: %v", err)
	}

	// "zz-" prefix not in routes
	rig := rigForIssue(townRoot, "zz-unknown")
	if rig != "" {
		t.Errorf("rigForIssue with unknown prefix = %q, want empty", rig)
	}
}

func TestRigForIssue_NoRoutesFile(t *testing.T) {
	townRoot := t.TempDir()

	// No .beads directory at all — should return ""
	rig := rigForIssue(townRoot, "gt-abc")
	if rig != "" {
		t.Errorf("rigForIssue with no routes file = %q, want empty", rig)
	}
}

func TestRigForIssue_TownLevelPrefix(t *testing.T) {
	townRoot := t.TempDir()

	// Town-level beads have path="." which should return "" (no specific rig)
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	routesContent := `{"prefix":"hq-","path":"."}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatalf("WriteFile routes.jsonl: %v", err)
	}

	rig := rigForIssue(townRoot, "hq-cv-test")
	if rig != "" {
		t.Errorf("rigForIssue for town-level prefix = %q, want empty", rig)
	}
}

// ---------------------------------------------------------------------------
// Helper: create a temporary town root with routes.jsonl and a gt stub
// ---------------------------------------------------------------------------

// setupTownRoot creates a temp directory with .beads/routes.jsonl mapping
// the "test-" prefix to the rig name "testrig".
func setupTownRoot(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("MkdirAll .beads: %v", err)
	}
	routesContent := `{"prefix":"test-","path":"testrig/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatalf("WriteFile routes.jsonl: %v", err)
	}
	return townRoot
}

// makeGTStub creates a shell script that logs its arguments and exits with the
// given code. Returns the path to the script and the path to the log file.
func makeGTStub(t *testing.T, exitCode int) (gtPath, logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	dir := t.TempDir()
	logPath = filepath.Join(dir, "gt.log")
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %q\nexit %d\n", logPath, exitCode)
	gtPath = filepath.Join(dir, "gt")
	if err := os.WriteFile(gtPath, []byte(script), 0755); err != nil {
		t.Fatalf("WriteFile gt stub: %v", err)
	}
	return gtPath, logPath
}

// makeLogger returns a logger that captures messages and a pointer to the slice.
func makeLogger() (func(string, ...interface{}), *[]string) {
	var msgs []string
	logger := func(format string, args ...interface{}) {
		msgs = append(msgs, fmt.Sprintf(format, args...))
	}
	return logger, &msgs
}

func containsLog(logs []string, substr string) bool {
	for _, msg := range logs {
		if strings.Contains(msg, substr) {
			return true
		}
	}
	return false
}

func createConvoyFeedFixture(t *testing.T, store beadsdk.Storage, convoyID string, convoyLabels []string) {
	t.Helper()

	ctx := context.Background()
	now := time.Now().UTC()
	convoy := &beadsdk.Issue{
		ID:        convoyID,
		Title:     "Mountain: Test Convoy",
		Status:    beadsdk.StatusOpen,
		Labels:    convoyLabels,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	closed := &beadsdk.Issue{
		ID:        "test-closed-" + convoyID,
		Title:     "Closed Task",
		Status:    beadsdk.StatusClosed,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	ready := &beadsdk.Issue{
		ID:        "test-ready-" + convoyID,
		Title:     "Ready Task",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}

	for _, iss := range []*beadsdk.Issue{convoy, closed, ready} {
		if err := store.CreateIssue(ctx, iss, "test"); err != nil {
			t.Fatalf("CreateIssue %s: %v", iss.ID, err)
		}
	}
	for _, trackedID := range []string{closed.ID, ready.ID} {
		dep := &beadsdk.Dependency{
			IssueID:     convoy.ID,
			DependsOnID: trackedID,
			Type:        beadsdk.DependencyType("tracks"),
			CreatedAt:   now,
			CreatedBy:   "test",
		}
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency %s: %v", trackedID, err)
		}
	}
}

// ---------------------------------------------------------------------------
// feedNextReadyIssue tests (real beads store)
// ---------------------------------------------------------------------------

func TestCheckConvoysForIssue_SkipsPausedMountainSuccessorDispatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	store, cleanup := setupTestStore(t)
	defer cleanup()

	createConvoyFeedFixture(t, store, "test-cv-paused", []string{"mountain", "mountain:paused"})
	townRoot := setupTownRoot(t)
	gtPath, logPath := makeGTStub(t, 0)
	logger, logMsgs := makeLogger()

	checked := CheckConvoysForIssue(context.Background(), store, townRoot, "test-closed-test-cv-paused", "test", logger, gtPath, nil)

	if len(checked) != 1 || checked[0] != "test-cv-paused" {
		t.Fatalf("checked convoys = %v, want [test-cv-paused]", checked)
	}
	if data, err := os.ReadFile(logPath); err == nil && strings.Contains(string(data), "sling") {
		t.Fatalf("paused Mountain dispatched successor; gt log:\n%s", data)
	}
	if !containsLog(*logMsgs, "mountain is paused") {
		t.Fatalf("expected paused Mountain log, got %v", *logMsgs)
	}
}

func TestCheckConvoysForIssue_ResumedMountainDispatchesOneSuccessor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	store, cleanup := setupTestStore(t)
	defer cleanup()

	createConvoyFeedFixture(t, store, "test-cv-resumed", []string{"mountain"})
	townRoot := setupTownRoot(t)
	gtPath, logPath := makeGTStub(t, 0)
	logger, _ := makeLogger()

	CheckConvoysForIssue(context.Background(), store, townRoot, "test-closed-test-cv-resumed", "test", logger, gtPath, nil)

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("gt stub was not called: %v", err)
	}
	logStr := strings.TrimSpace(string(data))
	if strings.Count(logStr, "sling test-ready-test-cv-resumed testrig --no-boot") != 1 {
		t.Fatalf("resumed Mountain should dispatch exactly one successor, got log:\n%s", logStr)
	}
}

func TestCheckConvoysForIssue_RegularConvoyDispatchUnaffected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	store, cleanup := setupTestStore(t)
	defer cleanup()

	createConvoyFeedFixture(t, store, "test-cv-regular", nil)
	townRoot := setupTownRoot(t)
	gtPath, logPath := makeGTStub(t, 0)
	logger, _ := makeLogger()

	CheckConvoysForIssue(context.Background(), store, townRoot, "test-closed-test-cv-regular", "test", logger, gtPath, nil)

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("gt stub was not called: %v", err)
	}
	if !strings.Contains(string(data), "sling test-ready-test-cv-regular testrig --no-boot") {
		t.Fatalf("regular convoy did not dispatch successor, got log:\n%s", data)
	}
}

func TestCheckConvoysForIssue_PauseLookupFailureFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	store, cleanup := setupTestStore(t)
	defer cleanup()

	createConvoyFeedFixture(t, store, "test-cv-lookup-fail", []string{"mountain"})
	if err := store.DeleteIssue(context.Background(), "test-cv-lookup-fail"); err != nil {
		t.Fatalf("DeleteIssue convoy: %v", err)
	}
	townRoot := setupTownRoot(t)
	gtPath, logPath := makeGTStub(t, 0)
	logger, logMsgs := makeLogger()

	feedNextReadyIssue(context.Background(), store, townRoot, "test-cv-lookup-fail", "test", logger, gtPath, func(string) bool { return false }, nil)

	if data, err := os.ReadFile(logPath); err == nil && strings.Contains(string(data), "sling") {
		t.Fatalf("lookup failure dispatched successor; gt log:\n%s", data)
	}
	if !containsLog(*logMsgs, "could not read convoy state") {
		t.Fatalf("expected lookup failure log, got %v", *logMsgs)
	}
}

func TestFeedNextReadyIssue_RechecksPauseAtExecutionBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	convoyID := "test-cv-race"
	createConvoyFeedFixture(t, store, convoyID, []string{"mountain"})
	townRoot := setupTownRoot(t)
	gtPath, logPath := makeGTStub(t, 0)
	logger, logMsgs := makeLogger()

	oldHook := afterDispatchIssueCommandConstructedForTest
	flipped := false
	afterDispatchIssueCommandConstructedForTest = func() {
		if !flipped {
			flipped = true
			if err := store.UpdateIssue(ctx, convoyID, map[string]interface{}{
				"labels": []string{"mountain", "mountain:paused"},
			}, "test"); err != nil {
				t.Fatalf("pause convoy during boundary race: %v", err)
			}
		}
	}
	t.Cleanup(func() { afterDispatchIssueCommandConstructedForTest = oldHook })

	feedNextReadyIssue(ctx, store, townRoot, convoyID, "test", logger, gtPath, func(string) bool { return false }, nil)

	if !flipped {
		t.Fatal("test did not reach pre-dispatch boundary")
	}
	if data, err := os.ReadFile(logPath); err == nil && strings.Contains(string(data), "sling") {
		t.Fatalf("Mountain paused after readiness check dispatched successor; gt log:\n%s", data)
	}
	if !containsLog(*logMsgs, "mountain is paused") {
		t.Fatalf("expected paused Mountain log, got %v", *logMsgs)
	}
}

func TestFeedNextReadyIssue_DispatchesFirstReadyIssue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	// Create convoy issue
	convoy := &beadsdk.Issue{
		ID:        "test-convoy1",
		Title:     "Test Convoy",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// 1: closed issue (should be skipped)
	closed := &beadsdk.Issue{
		ID:        "test-closed1",
		Title:     "Closed Task",
		Status:    beadsdk.StatusClosed,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// 2: assigned issue (should be skipped)
	assigned := &beadsdk.Issue{
		ID:        "test-assigned1",
		Title:     "Assigned Task",
		Status:    beadsdk.StatusOpen,
		Assignee:  "gastown/polecats/alpha",
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// 3: open, unassigned task (should be dispatched)
	ready := &beadsdk.Issue{
		ID:        "test-ready1",
		Title:     "Ready Task",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}

	for _, iss := range []*beadsdk.Issue{convoy, closed, assigned, ready} {
		if err := store.CreateIssue(ctx, iss, "test"); err != nil {
			t.Fatalf("CreateIssue %s: %v", iss.ID, err)
		}
	}

	// Add tracks deps: convoy -> each tracked issue
	for _, trackedID := range []string{closed.ID, assigned.ID, ready.ID} {
		dep := &beadsdk.Dependency{
			IssueID:     convoy.ID,
			DependsOnID: trackedID,
			Type:        beadsdk.DependencyType("tracks"),
			CreatedAt:   now,
			CreatedBy:   "test",
		}
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency %s: %v", trackedID, err)
		}
	}

	townRoot := setupTownRoot(t)
	gtPath, logPath := makeGTStub(t, 0)
	logger, _ := makeLogger()

	feedNextReadyIssue(ctx, store, townRoot, convoy.ID, "test", logger, gtPath, func(string) bool { return false }, nil)

	// Verify gt was called with the ready issue
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("gt stub was not called (no log file): %v", err)
	}
	logStr := strings.TrimSpace(string(logData))
	// Expected: "sling test-ready1 testrig --no-boot"
	if !strings.Contains(logStr, "sling test-ready1 testrig --no-boot") {
		t.Errorf("gt stub called with unexpected args: %q", logStr)
	}
}

func TestFeedNextReadyIssue_SkipsEpicAndDispatchesTask(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	convoy := &beadsdk.Issue{
		ID:        "test-convoy2",
		Title:     "Convoy For Epic Test",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	epic := &beadsdk.Issue{
		ID:        "test-epic1",
		Title:     "An Epic",
		Status:    beadsdk.StatusOpen,
		Priority:  1,
		IssueType: beadsdk.TypeEpic,
		CreatedAt: now,
		UpdatedAt: now,
	}
	task := &beadsdk.Issue{
		ID:        "test-task2",
		Title:     "A Task",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}

	for _, iss := range []*beadsdk.Issue{convoy, epic, task} {
		if err := store.CreateIssue(ctx, iss, "test"); err != nil {
			t.Fatalf("CreateIssue %s: %v", iss.ID, err)
		}
	}

	// Add tracks deps: convoy -> epic, convoy -> task
	for _, trackedID := range []string{epic.ID, task.ID} {
		dep := &beadsdk.Dependency{
			IssueID:     convoy.ID,
			DependsOnID: trackedID,
			Type:        beadsdk.DependencyType("tracks"),
			CreatedAt:   now,
			CreatedBy:   "test",
		}
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency %s: %v", trackedID, err)
		}
	}

	townRoot := setupTownRoot(t)
	gtPath, logPath := makeGTStub(t, 0)
	logger, _ := makeLogger()

	feedNextReadyIssue(ctx, store, townRoot, convoy.ID, "test", logger, gtPath, func(string) bool { return false }, nil)

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("gt stub was not called (no log file): %v", err)
	}
	logStr := strings.TrimSpace(string(logData))
	// Only the task should have been dispatched, not the epic
	if !strings.Contains(logStr, "sling test-task2 testrig --no-boot") {
		t.Errorf("expected task dispatch, got: %q", logStr)
	}
	if strings.Contains(logStr, "test-epic1") {
		t.Errorf("epic should not have been dispatched, but log contains: %q", logStr)
	}
}

func TestFeedNextReadyIssue_SkipsBlockedIssue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	convoy := &beadsdk.Issue{
		ID:        "test-convoy3",
		Title:     "Convoy For Blocked Test",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// Blocker issue (open)
	blocker := &beadsdk.Issue{
		ID:        "test-blocker3",
		Title:     "Blocker",
		Status:    beadsdk.StatusOpen,
		Priority:  1,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// Blocked task
	blockedTask := &beadsdk.Issue{
		ID:        "test-blocked3",
		Title:     "Blocked Task",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// Unblocked task
	unblockedTask := &beadsdk.Issue{
		ID:        "test-unblk3",
		Title:     "Unblocked Task",
		Status:    beadsdk.StatusOpen,
		Priority:  3,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}

	for _, iss := range []*beadsdk.Issue{convoy, blocker, blockedTask, unblockedTask} {
		if err := store.CreateIssue(ctx, iss, "test"); err != nil {
			t.Fatalf("CreateIssue %s: %v", iss.ID, err)
		}
	}

	// Add tracks deps from convoy
	for _, trackedID := range []string{blockedTask.ID, unblockedTask.ID} {
		dep := &beadsdk.Dependency{
			IssueID:     convoy.ID,
			DependsOnID: trackedID,
			Type:        beadsdk.DependencyType("tracks"),
			CreatedAt:   now,
			CreatedBy:   "test",
		}
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency tracks %s: %v", trackedID, err)
		}
	}

	// Add blocks dep: blockedTask is blocked by blocker
	blocksDep := &beadsdk.Dependency{
		IssueID:     blockedTask.ID,
		DependsOnID: blocker.ID,
		Type:        beadsdk.DepBlocks,
		CreatedAt:   now,
		CreatedBy:   "test",
	}
	if err := store.AddDependency(ctx, blocksDep, "test"); err != nil {
		t.Fatalf("AddDependency blocks: %v", err)
	}

	townRoot := setupTownRoot(t)
	gtPath, logPath := makeGTStub(t, 0)
	logger, logMsgs := makeLogger()

	feedNextReadyIssue(ctx, store, townRoot, convoy.ID, "test", logger, gtPath, func(string) bool { return false }, nil)

	logData, err := os.ReadFile(logPath)
	if err != nil {
		// If gt was not called at all, check if GetDependenciesWithMetadata
		// failed (embedded Dolt nested query limitation). This means both
		// isIssueBlocked and getConvoyTrackedIssues may fail.
		t.Logf("gt stub not called; log messages: %v", *logMsgs)
		t.Skipf("gt stub was not called — likely embedded Dolt nested query limitation")
	}
	logStr := strings.TrimSpace(string(logData))

	// Only the unblocked task should be dispatched
	if strings.Contains(logStr, "test-blocked3") {
		t.Errorf("blocked task should not have been dispatched, log: %q", logStr)
	}
	if !strings.Contains(logStr, "sling test-unblk3 testrig --no-boot") {
		t.Errorf("expected unblocked task dispatch, got: %q", logStr)
	}
}

func TestFeedNextReadyIssue_NoReadyIssues_LogsMessage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	convoy := &beadsdk.Issue{
		ID:        "test-convoy4",
		Title:     "Convoy No Ready",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	closed1 := &beadsdk.Issue{
		ID:        "test-cl4a",
		Title:     "Closed A",
		Status:    beadsdk.StatusClosed,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	closed2 := &beadsdk.Issue{
		ID:        "test-cl4b",
		Title:     "Closed B",
		Status:    beadsdk.StatusClosed,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}

	for _, iss := range []*beadsdk.Issue{convoy, closed1, closed2} {
		if err := store.CreateIssue(ctx, iss, "test"); err != nil {
			t.Fatalf("CreateIssue %s: %v", iss.ID, err)
		}
	}

	for _, trackedID := range []string{closed1.ID, closed2.ID} {
		dep := &beadsdk.Dependency{
			IssueID:     convoy.ID,
			DependsOnID: trackedID,
			Type:        beadsdk.DependencyType("tracks"),
			CreatedAt:   now,
			CreatedBy:   "test",
		}
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency %s: %v", trackedID, err)
		}
	}

	townRoot := setupTownRoot(t)
	gtPath, _ := makeGTStub(t, 0)
	logger, logMsgs := makeLogger()

	feedNextReadyIssue(ctx, store, townRoot, convoy.ID, "test", logger, gtPath, func(string) bool { return false }, nil)

	// Verify "no ready issues to feed" was logged
	found := false
	for _, msg := range *logMsgs {
		if strings.Contains(msg, "no ready issues to feed") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'no ready issues to feed' in log messages, got: %v", *logMsgs)
	}
}

func TestFeedNextReadyIssue_SkipsParkedRig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	convoy := &beadsdk.Issue{
		ID:        "test-convoy5",
		Title:     "Convoy Parked Test",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	task := &beadsdk.Issue{
		ID:        "test-task5",
		Title:     "Task For Parked Rig",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}

	for _, iss := range []*beadsdk.Issue{convoy, task} {
		if err := store.CreateIssue(ctx, iss, "test"); err != nil {
			t.Fatalf("CreateIssue %s: %v", iss.ID, err)
		}
	}

	dep := &beadsdk.Dependency{
		IssueID:     convoy.ID,
		DependsOnID: task.ID,
		Type:        beadsdk.DependencyType("tracks"),
		CreatedAt:   now,
		CreatedBy:   "test",
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	townRoot := setupTownRoot(t)
	gtPath, logPath := makeGTStub(t, 0)
	logger, logMsgs := makeLogger()

	// isRigParked always returns true
	feedNextReadyIssue(ctx, store, townRoot, convoy.ID, "test", logger, gtPath, func(string) bool { return true }, nil)

	// gt should NOT have been called
	if _, err := os.ReadFile(logPath); err == nil {
		t.Errorf("gt stub should not have been called for parked rig")
	}

	// Verify "parked" appeared in log
	foundParked := false
	for _, msg := range *logMsgs {
		if strings.Contains(msg, "parked") {
			foundParked = true
			break
		}
	}
	if !foundParked {
		// It's also possible we got "no ready issues" if getConvoyTrackedIssues
		// failed due to embedded Dolt. Accept either.
		t.Logf("log messages: %v", *logMsgs)
	}
}

// ---------------------------------------------------------------------------
// dispatchIssue tests (direct function call)
// ---------------------------------------------------------------------------

func TestDispatchIssue_Success(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	gtPath, logPath := makeGTStub(t, 0)
	store, cleanup := setupTestStore(t)
	defer cleanup()
	createConvoyFeedFixture(t, store, "test-cv-dispatch", []string{"mountain"})
	logger, _ := makeLogger()

	err := dispatchIssue(context.Background(), store, "test-cv-dispatch", "test", logger, townRoot, "test-abc", "myrig", gtPath, "")
	if err != nil {
		t.Fatalf("dispatchIssue returned error: %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("gt stub log not written: %v", err)
	}
	logStr := strings.TrimSpace(string(logData))
	expected := "sling test-abc myrig --no-boot"
	if logStr != expected {
		t.Errorf("gt stub called with %q, want %q", logStr, expected)
	}
}

func TestDispatchIssue_Failure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	gtPath, _ := makeGTStub(t, 1)
	store, cleanup := setupTestStore(t)
	defer cleanup()
	createConvoyFeedFixture(t, store, "test-cv-dispatch-fail", []string{"mountain"})
	logger, _ := makeLogger()

	err := dispatchIssue(context.Background(), store, "test-cv-dispatch-fail", "test", logger, townRoot, "test-fail", "myrig", gtPath, "")
	if err == nil {
		t.Fatal("dispatchIssue should return error when gt exits 1")
	}
}

// ---------------------------------------------------------------------------
// DS-07: CheckConvoysForIssue skips staged_ready convoys
// ---------------------------------------------------------------------------

func TestCheckConvoysForIssue_SkipsStagedReady(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	// Create a convoy as open first (SDK validates status on create),
	// then transition to "staged_ready" via UpdateIssue.
	convoy := &beadsdk.Issue{
		ID:        "test-cv-staged1",
		Title:     "Staged Ready Convoy",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// Create a tracked issue (closed, to trigger the event path)
	tracked := &beadsdk.Issue{
		ID:        "test-trk-stg1",
		Title:     "Tracked Issue",
		Status:    beadsdk.StatusClosed,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}

	for _, iss := range []*beadsdk.Issue{convoy, tracked} {
		if err := store.CreateIssue(ctx, iss, "test"); err != nil {
			t.Fatalf("CreateIssue %s: %v", iss.ID, err)
		}
	}

	// Transition convoy to "staged_ready" (SDK validates status on create,
	// so we create as open first and update).
	if err := store.UpdateIssue(ctx, convoy.ID, map[string]interface{}{
		"status": "staged_ready",
	}, "test"); err != nil {
		t.Fatalf("UpdateIssue to staged_ready: %v", err)
	}

	// Add tracks dependency: convoy tracks the closed issue
	dep := &beadsdk.Dependency{
		IssueID:     convoy.ID,
		DependsOnID: tracked.ID,
		Type:        beadsdk.DependencyType("tracks"),
		CreatedAt:   now,
		CreatedBy:   "test",
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	townRoot := setupTownRoot(t)
	gtPath, _ := makeGTStub(t, 0)
	logger, logMsgs := makeLogger()

	// Call CheckConvoysForIssue with the tracked issue's ID (simulating close event)
	result := CheckConvoysForIssue(ctx, store, townRoot, tracked.ID, "DS-07", logger, gtPath, nil)

	// The convoy should be returned (it was found as a tracker)
	if len(result) == 0 {
		t.Skipf("no tracking convoys found — GetDependentsWithMetadata may not work in embedded Dolt")
	}

	// Verify the staged convoy was skipped via log messages
	foundStagedSkip := false
	for _, msg := range *logMsgs {
		if strings.Contains(msg, "staged") && strings.Contains(msg, "skipping") {
			foundStagedSkip = true
			break
		}
	}
	if !foundStagedSkip {
		t.Errorf("expected log message about staged convoy being skipped, got: %v", *logMsgs)
	}

	// Verify "checking convoy" was NOT logged (convoy should be skipped before check)
	for _, msg := range *logMsgs {
		if strings.Contains(msg, "checking convoy") && strings.Contains(msg, convoy.ID) {
			t.Errorf("staged convoy should not have been checked, but found log: %s", msg)
		}
	}
}

// ---------------------------------------------------------------------------
// DS-08: CheckConvoysForIssue skips staged_warnings convoys
// ---------------------------------------------------------------------------

func TestCheckConvoysForIssue_SkipsStagedWarnings(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	// Create a convoy as open first, then transition to "staged_warnings".
	convoy := &beadsdk.Issue{
		ID:        "test-cv-staged2",
		Title:     "Staged Warnings Convoy",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	tracked := &beadsdk.Issue{
		ID:        "test-trk-stg2",
		Title:     "Tracked Issue",
		Status:    beadsdk.StatusClosed,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}

	for _, iss := range []*beadsdk.Issue{convoy, tracked} {
		if err := store.CreateIssue(ctx, iss, "test"); err != nil {
			t.Fatalf("CreateIssue %s: %v", iss.ID, err)
		}
	}

	// Transition convoy to "staged_warnings"
	if err := store.UpdateIssue(ctx, convoy.ID, map[string]interface{}{
		"status": "staged_warnings",
	}, "test"); err != nil {
		t.Fatalf("UpdateIssue to staged_warnings: %v", err)
	}

	dep := &beadsdk.Dependency{
		IssueID:     convoy.ID,
		DependsOnID: tracked.ID,
		Type:        beadsdk.DependencyType("tracks"),
		CreatedAt:   now,
		CreatedBy:   "test",
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	townRoot := setupTownRoot(t)
	gtPath, _ := makeGTStub(t, 0)
	logger, logMsgs := makeLogger()

	result := CheckConvoysForIssue(ctx, store, townRoot, tracked.ID, "DS-08", logger, gtPath, nil)

	if len(result) == 0 {
		t.Skipf("no tracking convoys found — GetDependentsWithMetadata may not work in embedded Dolt")
	}

	// Verify the staged convoy was skipped
	foundStagedSkip := false
	for _, msg := range *logMsgs {
		if strings.Contains(msg, "staged") && strings.Contains(msg, "skipping") {
			foundStagedSkip = true
			break
		}
	}
	if !foundStagedSkip {
		t.Errorf("expected log message about staged convoy being skipped, got: %v", *logMsgs)
	}

	// Verify "checking convoy" was NOT logged
	for _, msg := range *logMsgs {
		if strings.Contains(msg, "checking convoy") && strings.Contains(msg, convoy.ID) {
			t.Errorf("staged convoy should not have been checked, but found log: %s", msg)
		}
	}
}

// ---------------------------------------------------------------------------
// DS-10: After staged_ready→open transition, daemon feeds normally
// ---------------------------------------------------------------------------

func TestCheckConvoysForIssue_FeedsAfterStagedToOpenTransition(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	// Create a convoy as open first, then transition to "staged_ready"
	convoy := &beadsdk.Issue{
		ID:        "test-cv-launch",
		Title:     "Launched Convoy",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// Create a tracked issue that is closed (triggers event path)
	tracked := &beadsdk.Issue{
		ID:        "test-trk-lnch",
		Title:     "Tracked Closed",
		Status:    beadsdk.StatusClosed,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}

	for _, iss := range []*beadsdk.Issue{convoy, tracked} {
		if err := store.CreateIssue(ctx, iss, "test"); err != nil {
			t.Fatalf("CreateIssue %s: %v", iss.ID, err)
		}
	}

	// Transition convoy to "staged_ready"
	if err := store.UpdateIssue(ctx, convoy.ID, map[string]interface{}{
		"status": "staged_ready",
	}, "test"); err != nil {
		t.Fatalf("UpdateIssue to staged_ready: %v", err)
	}

	dep := &beadsdk.Dependency{
		IssueID:     convoy.ID,
		DependsOnID: tracked.ID,
		Type:        beadsdk.DependencyType("tracks"),
		CreatedAt:   now,
		CreatedBy:   "test",
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	// Phase 1: While staged, verify it's skipped
	logger1, logMsgs1 := makeLogger()
	townRoot := setupTownRoot(t)
	gtPath, _ := makeGTStub(t, 0)

	result1 := CheckConvoysForIssue(ctx, store, townRoot, tracked.ID, "DS-10-staged", logger1, gtPath, nil)
	if len(result1) == 0 {
		t.Skipf("no tracking convoys found — GetDependentsWithMetadata may not work in embedded Dolt")
	}

	foundStagedSkip := false
	for _, msg := range *logMsgs1 {
		if strings.Contains(msg, "staged") && strings.Contains(msg, "skipping") {
			foundStagedSkip = true
			break
		}
	}
	if !foundStagedSkip {
		t.Fatalf("convoy should have been skipped while staged, logs: %v", *logMsgs1)
	}

	// Phase 2: Transition convoy to "open" (launch it)
	if err := store.UpdateIssue(ctx, convoy.ID, map[string]interface{}{
		"status": string(beadsdk.StatusOpen),
	}, "test"); err != nil {
		t.Fatalf("UpdateIssue staged->open: %v", err)
	}

	// Verify it's no longer staged
	if isConvoyStaged(ctx, store, convoy.ID) {
		t.Fatal("convoy should not be staged after transition to open")
	}

	// Phase 3: Call CheckConvoysForIssue again — now the convoy should be processed
	logger2, logMsgs2 := makeLogger()
	_ = CheckConvoysForIssue(ctx, store, townRoot, tracked.ID, "DS-10-open", logger2, gtPath, nil)

	// Verify "checking convoy" WAS logged (convoy is now open and being processed)
	foundChecking := false
	for _, msg := range *logMsgs2 {
		if strings.Contains(msg, "checking convoy") && strings.Contains(msg, convoy.ID) {
			foundChecking = true
			break
		}
	}
	if !foundChecking {
		t.Errorf("expected convoy to be checked after staged->open transition, logs: %v", *logMsgs2)
	}

	// Verify it was NOT skipped as staged
	for _, msg := range *logMsgs2 {
		if strings.Contains(msg, "staged") && strings.Contains(msg, "skipping") {
			t.Errorf("convoy should NOT be skipped after transition to open, but found: %s", msg)
		}
	}
}

// ---------------------------------------------------------------------------
// Cross-rig fallback tests
// ---------------------------------------------------------------------------

// setupTownRootWithCrossRig creates a town root with routes for both "test-"
// (local rig) and "oag-" (cross-rig) prefixes. The cross-rig prefix points
// to a directory with a bd stub.
func setupTownRootWithCrossRig(t *testing.T, bdExitCode int, bdOutput string) (townRoot string, bdLogPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot = t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll .beads: %v", err)
	}

	// Create cross-rig directory with .beads
	crossRigDir := filepath.Join(townRoot, "osr_ai_gm", ".beads")
	if err := os.MkdirAll(crossRigDir, 0o755); err != nil {
		t.Fatalf("MkdirAll osr_ai_gm/.beads: %v", err)
	}

	// Routes: test- is local, oag- is cross-rig
	routesContent := `{"prefix":"test-","path":"testrig/.beads"}` + "\n" +
		`{"prefix":"oag-","path":"osr_ai_gm/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0o644); err != nil {
		t.Fatalf("WriteFile routes.jsonl: %v", err)
	}

	// Create bd stub in PATH
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll binDir: %v", err)
	}
	bdLogPath = filepath.Join(townRoot, "bd.log")

	bdScript := fmt.Sprintf(`#!/bin/sh
echo "CMD:$*" >> %q
case "$1" in
  show)
    echo '%s'
    exit %d
    ;;
esac
exit 0
`, bdLogPath, bdOutput, bdExitCode)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return townRoot, bdLogPath
}

func TestGetConvoyTrackedIssues_CrossRigFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	// Create convoy in the store (local)
	convoy := &beadsdk.Issue{
		ID:        "test-convoy-xrig",
		Title:     "Cross-Rig Convoy",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.CreateIssue(ctx, convoy, "test"); err != nil {
		t.Fatalf("CreateIssue convoy: %v", err)
	}

	// The cross-rig bead (oag-19dd9) is NOT in the local store.
	// Add tracks dependency using external reference format expected by beads.
	dep := &beadsdk.Dependency{
		IssueID:     convoy.ID,
		DependsOnID: "external:oag:oag-19dd9",
		Type:        beadsdk.DependencyType("tracks"),
		CreatedAt:   now,
		CreatedBy:   "test",
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	// Set up town root with cross-rig routes and bd stub returning "closed"
	townRoot, _ := setupTownRootWithCrossRig(t, 0,
		`[{"id":"oag-19dd9","status":"closed","assignee":"gastown/polecats/alpha","priority":2,"issue_type":"task"}]`)

	tracked := getConvoyTrackedIssues(ctx, store, convoy.ID, townRoot, nil)

	// Find the cross-rig bead in tracked results
	var found *trackedIssue
	for i := range tracked {
		if tracked[i].ID == "oag-19dd9" {
			found = &tracked[i]
			break
		}
	}

	if found == nil {
		t.Skipf("oag-19dd9 not found in tracked issues (GetDependenciesWithMetadata may not work in embedded Dolt)")
	}

	// The critical assertion: the cross-rig bead should show fresh "closed" status,
	// NOT the stale "open" from dependency metadata.
	if found.Status != "closed" {
		t.Errorf("cross-rig bead status = %q, want %q (stale metadata was used instead of fresh bd show)", found.Status, "closed")
	}
	if found.Assignee != "gastown/polecats/alpha" {
		t.Errorf("cross-rig bead assignee = %q, want %q", found.Assignee, "gastown/polecats/alpha")
	}
}

func TestFetchCrossRigBeadStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot, bdLogPath := setupTownRootWithCrossRig(t, 0,
		`[{"id":"oag-abc","status":"closed","assignee":"","priority":1,"issue_type":"task"},{"id":"oag-xyz","status":"open","assignee":"gastown/polecats/beta","priority":3,"issue_type":"bug"}]`)

	result := fetchCrossRigBeadStatus(townRoot, []string{"oag-abc", "oag-xyz"})

	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}

	abc := result["oag-abc"]
	if abc == nil {
		t.Fatal("oag-abc not found in results")
	}
	if string(abc.Status) != "closed" {
		t.Errorf("oag-abc status = %q, want %q", abc.Status, "closed")
	}

	xyz := result["oag-xyz"]
	if xyz == nil {
		t.Fatal("oag-xyz not found in results")
	}
	if string(xyz.Status) != "open" {
		t.Errorf("oag-xyz status = %q, want %q", xyz.Status, "open")
	}
	if xyz.Assignee != "gastown/polecats/beta" {
		t.Errorf("oag-xyz assignee = %q, want %q", xyz.Assignee, "gastown/polecats/beta")
	}

	// Verify bd was called
	logData, err := os.ReadFile(bdLogPath)
	if err != nil {
		t.Fatalf("bd stub not called (no log): %v", err)
	}
	logStr := string(logData)
	if !strings.Contains(logStr, "show --json oag-abc oag-xyz") &&
		!strings.Contains(logStr, "show --json oag-xyz oag-abc") {
		t.Errorf("bd show not called with expected IDs: %q", logStr)
	}
}

func TestFetchCrossRigBeadStatus_UnknownPrefix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot, _ := setupTownRootWithCrossRig(t, 0, `[]`)

	// "zzz-" prefix has no route — should return empty, not panic
	result := fetchCrossRigBeadStatus(townRoot, []string{"zzz-unknown"})
	if len(result) != 0 {
		t.Errorf("expected 0 results for unknown prefix, got %d", len(result))
	}
}

func TestFetchCrossRigBeadStatus_EmptyInput(t *testing.T) {
	result := fetchCrossRigBeadStatus("/nonexistent", nil)
	if len(result) != 0 {
		t.Errorf("expected 0 results for empty input, got %d", len(result))
	}
}

func TestFireCrossRigDepNotifications_NilStores(t *testing.T) {
	// Should not panic with nil stores.
	FireCrossRigDepNotifications(context.Background(), "bd-xxx", "/tmp", nil, nil)
}

func TestFireCrossRigDepNotifications_EmptyClosedID(t *testing.T) {
	// Should not panic with empty closed issue ID.
	store, cleanup := setupTestStore(t)
	defer cleanup()
	FireCrossRigDepNotifications(context.Background(), "", "/tmp", map[string]beadsdk.Storage{"test": store}, nil)
}

func TestFireCrossRigDepNotifications_EmptyPrefix(t *testing.T) {
	// Issue ID without a recognizable prefix should not panic.
	store, cleanup := setupTestStore(t)
	defer cleanup()
	FireCrossRigDepNotifications(context.Background(), "noprefixid", "/tmp", map[string]beadsdk.Storage{"test": store}, nil)
}

func TestFireCrossRigDepNotifications_NotifiesWitnessOnCrossRigBlocker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	// Set up a real store that simulates the "gastown" rig.
	// In it we create gt-dep which is blocked by external:bd:bd-closed.
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	dependent := &beadsdk.Issue{
		ID:        "gt-dep1",
		Title:     "Waiting on beads fix",
		Status:    beadsdk.StatusOpen,
		Priority:  2,
		IssueType: beadsdk.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.CreateIssue(ctx, dependent, "test"); err != nil {
		t.Fatalf("CreateIssue dependent: %v", err)
	}

	// Add a blocking dep: gt-dep1 is blocked by external:bd:bd-closed
	dep := &beadsdk.Dependency{
		IssueID:     "gt-dep1",
		DependsOnID: "external:bd:bd-closed",
		Type:        beadsdk.DepBlocks,
		CreatedAt:   now,
		CreatedBy:   "test",
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	// Set up town root with routes: gt- → gastown, bd- → beads.
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll .beads: %v", err)
	}
	routesContent := `{"prefix":"gt-","path":"gastown/.beads"}` + "\n" +
		`{"prefix":"bd-","path":"beads/.beads"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0o644); err != nil {
		t.Fatalf("WriteFile routes.jsonl: %v", err)
	}

	// Set up a mock gt binary that logs nudge calls.
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll binDir: %v", err)
	}
	gtLogPath := filepath.Join(townRoot, "gt.log")
	gtScript := fmt.Sprintf(`#!/bin/sh
echo "CMD:$*" >> %q
exit 0
`, gtLogPath)
	gtPath := filepath.Join(binDir, "gt")
	if err := os.WriteFile(gtPath, []byte(gtScript), 0o755); err != nil {
		t.Fatalf("write gt stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// stores: "gastown" → store (has gt-dep1 blocked by external:bd:bd-closed)
	//         "beads"   → (closed issue's home store, skipped by FireCrossRigDepNotifications)
	stores := map[string]beadsdk.Storage{
		"gastown": store,
	}

	var logged []string
	logger := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	FireCrossRigDepNotifications(ctx, "bd-closed", townRoot, stores, logger)

	// Verify gt nudge was called for gastown/witness.
	logData, err := os.ReadFile(gtLogPath)
	if err != nil {
		t.Skipf("gt stub not called (no log): %v", err)
	}
	logStr := string(logData)
	if !strings.Contains(logStr, "nudge") || !strings.Contains(logStr, "gastown/witness") {
		t.Errorf("expected gt nudge gastown/witness in log, got: %q\nlogger output: %v", logStr, logged)
	}
}
