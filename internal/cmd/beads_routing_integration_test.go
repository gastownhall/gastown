//go:build integration

// Package cmd contains integration tests for beads routing and redirects.
//
// Run with: go test -tags=integration ./internal/cmd -run TestBeadsRouting -v
package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/jonbaldie/gastown/internal/beads"
)

// routingTestCounter generates unique prefixes for each routing test to isolate
// Dolt databases on the shared server.
var routingTestCounter atomic.Int32

// setupRoutingTestTown creates a minimal Gas Town with multiple rigs for testing routing.
// Uses fixed prefixes (hq, gt, tr) — suitable for tests that don't create Dolt databases.
// Returns townRoot.
func setupRoutingTestTown(t *testing.T) string {
	t.Helper()
	return setupRoutingTestTownWithPrefixes(t, "hq", "gt", "tr")
}

// setupRoutingTestTownWithPrefixes creates a minimal Gas Town with multiple rigs,
// using the given prefixes for routes and beads config. Use unique prefixes when
// tests create Dolt databases to avoid collisions.
// Returns townRoot.
func setupRoutingTestTownWithPrefixes(t *testing.T, hqPrefix, gtPrefix, trPrefix string) string {
	t.Helper()

	townRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	// Create mayor/town.json so FindTownRoot() can detect this as a Gas Town root
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte(`{"name":"test"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}

	// Create town-level .beads directory
	townBeadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir town .beads: %v", err)
	}

	// Create routes.jsonl with multiple rigs
	routes := []beads.Route{
		{Prefix: hqPrefix + "-", Path: "."},                 // Town-level beads
		{Prefix: gtPrefix + "-", Path: "gastown/mayor/rig"}, // Gastown rig
		{Prefix: trPrefix + "-", Path: "testrig/mayor/rig"}, // Test rig
	}
	if err := beads.WriteRoutes(townBeadsDir, routes); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	// Create gastown rig structure
	gasRigPath := filepath.Join(townRoot, "gastown", "mayor", "rig")
	if err := os.MkdirAll(gasRigPath, 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}

	// Create gastown .beads directory with its own config
	gasBeadsDir := filepath.Join(gasRigPath, ".beads")
	if err := os.MkdirAll(gasBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir gastown .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gasBeadsDir, "config.yaml"), []byte("prefix: "+gtPrefix+"\n"), 0644); err != nil {
		t.Fatalf("write gastown config: %v", err)
	}

	// Create testrig structure
	testRigPath := filepath.Join(townRoot, "testrig", "mayor", "rig")
	if err := os.MkdirAll(testRigPath, 0755); err != nil {
		t.Fatalf("mkdir testrig: %v", err)
	}

	// Create testrig .beads directory
	testBeadsDir := filepath.Join(testRigPath, ".beads")
	if err := os.MkdirAll(testBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir testrig .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(testBeadsDir, "config.yaml"), []byte("prefix: "+trPrefix+"\n"), 0644); err != nil {
		t.Fatalf("write testrig config: %v", err)
	}

	// Create polecats directory with redirect
	polecatsDir := filepath.Join(townRoot, "gastown", "polecats", "rictus")
	if err := os.MkdirAll(polecatsDir, 0755); err != nil {
		t.Fatalf("mkdir polecats: %v", err)
	}

	// Create redirect file for polecat -> mayor/rig/.beads
	// Path: gastown/polecats/rictus -> ../../mayor/rig/.beads -> gastown/mayor/rig/.beads
	polecatBeadsDir := filepath.Join(polecatsDir, ".beads")
	if err := os.MkdirAll(polecatBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir polecat .beads: %v", err)
	}
	redirectContent := "../../mayor/rig/.beads"
	if err := os.WriteFile(filepath.Join(polecatBeadsDir, "redirect"), []byte(redirectContent), 0644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	// Create crew directory with redirect
	// Path: gastown/crew/max -> ../../mayor/rig/.beads -> gastown/mayor/rig/.beads
	crewDir := filepath.Join(townRoot, "gastown", "crew", "max")
	if err := os.MkdirAll(crewDir, 0755); err != nil {
		t.Fatalf("mkdir crew: %v", err)
	}

	crewBeadsDir := filepath.Join(crewDir, ".beads")
	if err := os.MkdirAll(crewBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir crew .beads: %v", err)
	}
	crewRedirect := "../../mayor/rig/.beads"
	if err := os.WriteFile(filepath.Join(crewBeadsDir, "redirect"), []byte(crewRedirect), 0644); err != nil {
		t.Fatalf("write crew redirect: %v", err)
	}

	return townRoot
}

func initBeadsDBWithPrefix(t *testing.T, dir, prefix string) {
	t.Helper()

	args := []string{"init", "--quiet", "--prefix", prefix}
	// Forward GT_DOLT_PORT so bd connects to the ephemeral test server
	// instead of defaulting to port 3307. bd v1.0.0+ requires --server.
	if p := os.Getenv("GT_DOLT_PORT"); p != "" {
		args = append(args, "--server", "--server-port", p)
	}
	cmd := exec.Command("bd", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init failed in %s: %v\n%s", dir, err, output)
	}

	// Create issues.jsonl — bd expects this file for git-tracked issue data.
	issuesPath := filepath.Join(dir, ".beads", "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(""), 0644); err != nil {
		t.Fatalf("create issues.jsonl in %s: %v", dir, err)
	}
}

func createTestIssue(t *testing.T, dir, title string) *beads.Issue {
	t.Helper()

	args := []string{"create", "--json", "--title", title, "--type", "task",
		"--description", "Integration test issue"}
	cmd := exec.Command("bd", args...)
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		combinedCmd := exec.Command("bd", args...)
		combinedCmd.Dir = dir
		combinedOutput, _ := combinedCmd.CombinedOutput()
		t.Fatalf("create issue in %s: %v\n%s", dir, err, combinedOutput)
	}

	var issue beads.Issue
	if err := json.Unmarshal(output, &issue); err != nil {
		t.Fatalf("parse create output in %s: %v", dir, err)
	}
	if issue.ID == "" {
		t.Fatalf("create issue in %s returned empty ID", dir)
	}
	return &issue
}

func hasIssueID(issues []*beads.Issue, id string) bool {
	for _, issue := range issues {
		if issue.ID == id {
			return true
		}
	}
	return false
}

// TestBeadsRoutingFromTownRoot verifies that bd show routes to correct rig
// based on issue ID prefix when run from town root.
func TestBeadsRoutingFromTownRoot(t *testing.T) {
	// Skip if bd is not available
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping routing test")
	}
	requireDoltServer(t)

	n := routingTestCounter.Add(1)
	hqP := fmt.Sprintf("rhq%d", n)
	gtP := fmt.Sprintf("rgt%d", n)
	trP := fmt.Sprintf("rtr%d", n)

	townRoot := setupRoutingTestTownWithPrefixes(t, hqP, gtP, trP)

	initBeadsDBWithPrefix(t, townRoot, hqP)

	gastownRigPath := filepath.Join(townRoot, "gastown", "mayor", "rig")
	testrigRigPath := filepath.Join(townRoot, "testrig", "mayor", "rig")
	initBeadsDBWithPrefix(t, gastownRigPath, gtP)
	initBeadsDBWithPrefix(t, testrigRigPath, trP)

	townIssue := createTestIssue(t, townRoot, "Town-level routing test")
	gastownIssue := createTestIssue(t, gastownRigPath, "Gastown routing test")
	testrigIssue := createTestIssue(t, testrigRigPath, "Testrig routing test")

	tests := []struct {
		id    string
		title string
	}{
		{townIssue.ID, townIssue.Title},
		{gastownIssue.ID, gastownIssue.Title},
		{testrigIssue.ID, testrigIssue.Title},
	}

	townBeads := beads.New(townRoot)
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			issue, err := townBeads.Show(tc.id)
			if err != nil {
				t.Fatalf("bd show %s failed: %v", tc.id, err)
			}
			if issue.ID != tc.id {
				t.Errorf("issue.ID = %s, want %s", issue.ID, tc.id)
			}
			if issue.Title != tc.title {
				t.Errorf("issue.Title = %q, want %q", issue.Title, tc.title)
			}
		})
	}
}

// TestBeadsRedirectResolution verifies that redirect files are followed correctly.
func TestBeadsRedirectResolution(t *testing.T) {
	townRoot := setupRoutingTestTown(t)

	tests := []struct {
		name     string
		workDir  string
		expected string // Expected resolved path (relative to townRoot)
	}{
		{
			name:     "polecat redirect",
			workDir:  "gastown/polecats/rictus",
			expected: "gastown/mayor/rig/.beads",
		},
		{
			name:     "crew redirect",
			workDir:  "gastown/crew/max",
			expected: "gastown/mayor/rig/.beads",
		},
		{
			name:     "no redirect (mayor/rig)",
			workDir:  "gastown/mayor/rig",
			expected: "gastown/mayor/rig/.beads",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fullWorkDir := filepath.Join(townRoot, tc.workDir)
			resolved := beads.ResolveBeadsDir(fullWorkDir)

			expectedFull := filepath.Join(townRoot, tc.expected)
			if resolved != expectedFull {
				t.Errorf("ResolveBeadsDir(%s) = %s, want %s", tc.workDir, resolved, expectedFull)
			}
		})
	}
}

// TestBeadsCircularRedirectDetection verifies that circular redirects are detected.
func TestBeadsCircularRedirectDetection(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a beads directory with a redirect pointing to itself
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create redirect file pointing to itself (circular)
	redirectContent := ".beads" // Points to current .beads (circular)
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte(redirectContent), 0644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	// ResolveBeadsDir should detect the circular redirect and return the original
	resolved := beads.ResolveBeadsDir(tmpDir)
	if resolved != beadsDir {
		t.Errorf("expected circular redirect to return original beads dir, got %s", resolved)
	}

	// The redirect file should have been removed
	redirectPath := filepath.Join(beadsDir, "redirect")
	if _, err := os.Stat(redirectPath); !os.IsNotExist(err) {
		t.Error("circular redirect file should have been removed")
	}
}

// TestBeadsPrefixConflictDetection verifies that duplicate prefixes are detected.
func TestBeadsPrefixConflictDetection(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create routes with a duplicate prefix
	routes := []beads.Route{
		{Prefix: "gt-", Path: "gastown/mayor/rig"},
		{Prefix: "gt-", Path: "other/mayor/rig"}, // Duplicate!
		{Prefix: "bd-", Path: "beads/mayor/rig"},
	}
	if err := beads.WriteRoutes(beadsDir, routes); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	// FindConflictingPrefixes should detect the duplicate
	conflicts, err := beads.FindConflictingPrefixes(beadsDir)
	if err != nil {
		t.Fatalf("FindConflictingPrefixes: %v", err)
	}

	if len(conflicts) == 0 {
		t.Error("expected to find conflicts, got none")
	}

	if paths, ok := conflicts["gt-"]; !ok {
		t.Error("expected conflict for prefix 'gt-'")
	} else if len(paths) != 2 {
		t.Errorf("expected 2 conflicting paths for 'gt-', got %d", len(paths))
	}
}

// TestBeadsListFromPolecatDirectory verifies that bd list works from polecat directories.
func TestBeadsListFromPolecatDirectory(t *testing.T) {
	// Skip if bd is not available
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping test")
	}
	requireDoltServer(t)

	n := routingTestCounter.Add(1)
	gtP := fmt.Sprintf("rgt%d", n)

	townRoot := setupRoutingTestTownWithPrefixes(t, "hq", gtP, "tr")
	polecatDir := filepath.Join(townRoot, "gastown", "polecats", "rictus")

	rigPath := filepath.Join(townRoot, "gastown", "mayor", "rig")
	initBeadsDBWithPrefix(t, rigPath, gtP)

	issue := createTestIssue(t, rigPath, "Polecat list redirect test")

	issues, err := beads.New(polecatDir).List(beads.ListOptions{
		Status:   "open",
		Priority: -1,
	})
	if err != nil {
		t.Fatalf("bd list from polecat dir failed: %v", err)
	}

	if !hasIssueID(issues, issue.ID) {
		t.Errorf("bd list from polecat dir missing issue %s", issue.ID)
	}
}

// TestBeadsListFromCrewDirectory verifies that bd list works from crew directories.
func TestBeadsListFromCrewDirectory(t *testing.T) {
	// Skip if bd is not available
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping test")
	}
	requireDoltServer(t)

	n := routingTestCounter.Add(1)
	gtP := fmt.Sprintf("rgt%d", n)

	townRoot := setupRoutingTestTownWithPrefixes(t, "hq", gtP, "tr")
	crewDir := filepath.Join(townRoot, "gastown", "crew", "max")

	rigPath := filepath.Join(townRoot, "gastown", "mayor", "rig")
	initBeadsDBWithPrefix(t, rigPath, gtP)

	issue := createTestIssue(t, rigPath, "Crew list redirect test")

	issues, err := beads.New(crewDir).List(beads.ListOptions{
		Status:   "open",
		Priority: -1,
	})
	if err != nil {
		t.Fatalf("bd list from crew dir failed: %v", err)
	}
	if !hasIssueID(issues, issue.ID) {
		t.Errorf("bd list from crew dir missing issue %s", issue.ID)
	}
}

// TestBeadsRoutesLoading verifies that routes.jsonl is loaded correctly.
func TestBeadsRoutesLoading(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create routes.jsonl with various entries
	routesContent := `{"prefix": "hq-", "path": "."}
{"prefix": "gt-", "path": "gastown/mayor/rig"}
# Comment line should be ignored
{"prefix": "bd-", "path": "beads/mayor/rig"}

{"prefix": "tr-", "path": "testrig/mayor/rig"}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	routes, err := beads.LoadRoutes(beadsDir)
	if err != nil {
		t.Fatalf("LoadRoutes: %v", err)
	}

	if len(routes) != 4 {
		t.Errorf("expected 4 routes, got %d", len(routes))
	}

	// Verify specific routes
	expectedPrefixes := map[string]string{
		"hq-": ".",
		"gt-": "gastown/mayor/rig",
		"bd-": "beads/mayor/rig",
		"tr-": "testrig/mayor/rig",
	}

	for _, r := range routes {
		if expected, ok := expectedPrefixes[r.Prefix]; ok {
			if r.Path != expected {
				t.Errorf("route %s: path = %q, want %q", r.Prefix, r.Path, expected)
			}
		} else {
			t.Errorf("unexpected prefix: %s", r.Prefix)
		}
	}
}

// TestBeadsAppendRoute verifies that routes can be appended and updated.
func TestBeadsAppendRoute(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Append first route
	route1 := beads.Route{Prefix: "gt-", Path: "gastown/mayor/rig"}
	if err := beads.AppendRoute(tmpDir, route1); err != nil {
		t.Fatalf("AppendRoute 1: %v", err)
	}

	// Append second route
	route2 := beads.Route{Prefix: "bd-", Path: "beads/mayor/rig"}
	if err := beads.AppendRoute(tmpDir, route2); err != nil {
		t.Fatalf("AppendRoute 2: %v", err)
	}

	// Verify both routes exist
	routes, err := beads.LoadRoutes(beadsDir)
	if err != nil {
		t.Fatalf("LoadRoutes: %v", err)
	}
	if len(routes) != 2 {
		t.Errorf("expected 2 routes, got %d", len(routes))
	}

	// Update existing route (same prefix, different path)
	route1Updated := beads.Route{Prefix: "gt-", Path: "newpath/mayor/rig"}
	if err := beads.AppendRoute(tmpDir, route1Updated); err != nil {
		t.Fatalf("AppendRoute update: %v", err)
	}

	// Verify update
	routes, _ = beads.LoadRoutes(beadsDir)
	if len(routes) != 2 {
		t.Errorf("expected 2 routes after update, got %d", len(routes))
	}

	for _, r := range routes {
		if r.Prefix == "gt-" && r.Path != "newpath/mayor/rig" {
			t.Errorf("route update failed: got path %q", r.Path)
		}
	}
}

// TestBeadsRemoveRoute verifies that routes can be removed.
func TestBeadsRemoveRoute(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create initial routes
	routes := []beads.Route{
		{Prefix: "gt-", Path: "gastown/mayor/rig"},
		{Prefix: "bd-", Path: "beads/mayor/rig"},
	}
	if err := beads.WriteRoutes(beadsDir, routes); err != nil {
		t.Fatalf("WriteRoutes: %v", err)
	}

	// Remove one route
	if err := beads.RemoveRoute(tmpDir, "gt-"); err != nil {
		t.Fatalf("RemoveRoute: %v", err)
	}

	// Verify removal
	remaining, _ := beads.LoadRoutes(beadsDir)
	if len(remaining) != 1 {
		t.Errorf("expected 1 route after removal, got %d", len(remaining))
	}
	if remaining[0].Prefix != "bd-" {
		t.Errorf("wrong route remaining: %s", remaining[0].Prefix)
	}
}

// TestSlingCrossRigRoutingResolution verifies that sling can resolve rig paths
// for cross-rig bead hooking using ExtractPrefix and GetRigPathForPrefix.
// This is the fix for https://github.com/steveyegge/gastown/issues/148
func TestSlingCrossRigRoutingResolution(t *testing.T) {
	townRoot := setupRoutingTestTown(t)

	tests := []struct {
		beadID       string
		expectedPath string // Relative to townRoot, or "." for town-level
	}{
		{"gt-mol-abc", "gastown/mayor/rig"},
		{"tr-task-xyz", "testrig/mayor/rig"},
		{"hq-cv-123", "."}, // Town-level beads
	}

	for _, tc := range tests {
		t.Run(tc.beadID, func(t *testing.T) {
			// Step 1: Extract prefix from bead ID
			prefix := beads.ExtractPrefix(tc.beadID)
			if prefix == "" {
				t.Fatalf("ExtractPrefix(%q) returned empty", tc.beadID)
			}

			// Step 2: Resolve rig path from prefix
			rigPath := beads.GetRigPathForPrefix(townRoot, prefix)
			if rigPath == "" {
				t.Fatalf("GetRigPathForPrefix(%q, %q) returned empty", townRoot, prefix)
			}

			// Step 3: Verify the path is correct
			var expectedFull string
			if tc.expectedPath == "." {
				expectedFull = townRoot
			} else {
				expectedFull = filepath.Join(townRoot, tc.expectedPath)
			}

			if rigPath != expectedFull {
				t.Errorf("GetRigPathForPrefix resolved to %q, want %q", rigPath, expectedFull)
			}

			// Step 4: Verify the .beads directory exists at that path
			beadsDir := filepath.Join(rigPath, ".beads")
			if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
				t.Errorf(".beads directory doesn't exist at resolved path: %s", beadsDir)
			}
		})
	}
}

// TestSlingCrossRigUnknownPrefix verifies behavior for unknown prefixes.
func TestSlingCrossRigUnknownPrefix(t *testing.T) {
	townRoot := setupRoutingTestTown(t)

	// An unknown prefix should return empty string
	unknownBeadID := "xx-unknown-123"
	prefix := beads.ExtractPrefix(unknownBeadID)
	if prefix != "xx-" {
		t.Fatalf("ExtractPrefix(%q) = %q, want %q", unknownBeadID, prefix, "xx-")
	}

	rigPath := beads.GetRigPathForPrefix(townRoot, prefix)
	if rigPath != "" {
		t.Errorf("GetRigPathForPrefix for unknown prefix returned %q, want empty", rigPath)
	}
}

// TestBeadsGetPrefixForRig verifies prefix lookup by rig name.
func TestBeadsGetPrefixForRig(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create routes
	routes := []beads.Route{
		{Prefix: "gt-", Path: "gastown/mayor/rig"},
		{Prefix: "bd-", Path: "beads/mayor/rig"},
		{Prefix: "hq-", Path: "."},
	}
	if err := beads.WriteRoutes(beadsDir, routes); err != nil {
		t.Fatalf("WriteRoutes: %v", err)
	}

	tests := []struct {
		rigName  string
		expected string
	}{
		{"gastown", "gt"},
		{"beads", "bd"},
		{"unknown", "gt"}, // Default
		{"", "gt"},        // Empty -> default
	}

	for _, tc := range tests {
		t.Run(tc.rigName, func(t *testing.T) {
			result := beads.GetPrefixForRig(tmpDir, tc.rigName)
			if result != tc.expected {
				t.Errorf("GetPrefixForRig(%q) = %q, want %q", tc.rigName, result, tc.expected)
			}
		})
	}
}
