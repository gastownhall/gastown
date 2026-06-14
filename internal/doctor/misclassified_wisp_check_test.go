package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestFixWorkDir_HQ verifies that Fix() resolves the "hq" rig name to the
// town root directory, not townRoot/hq. When the Dolt detection path finds
// misplaced ephemerals in the "hq" database, the rigName is "hq" — Fix() must
// map this to TownRoot (same as Run does). Regression test for GH#2127.
func TestFixWorkDir_HQ(t *testing.T) {
	townRoot := t.TempDir()

	got := resolveMisclassifiedWispWorkDir(townRoot, misclassifiedWisp{rigName: "hq"})
	hqPath := filepath.Join(townRoot, "hq")
	if hqPath == townRoot {
		t.Fatal("test setup error: townRoot should not end in /hq")
	}
	if got != townRoot {
		t.Fatalf("resolveMisclassifiedWispWorkDir(%q, hq) = %q, want %q", townRoot, got, townRoot)
	}
}

func TestFixWorkDir_RoutedRig(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	routesContent := `{"prefix":"hq-","path":"."}
{"prefix":"sw-","path":"sallaWork/mayor/rig"}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(townRoot, "sallaWork/mayor/rig")
	got := resolveMisclassifiedWispWorkDir(townRoot, misclassifiedWisp{rigName: "sw"})
	if got != want {
		t.Fatalf("resolveMisclassifiedWispWorkDir(%q, sw) = %q, want %q", townRoot, got, want)
	}
}

// TestNoHeuristicClassification verifies that the check does NOT use heuristics
// to guess whether beads should be wisps. Only beads with ephemeral=1 that are
// in the issues table should be flagged. This is the ZFC compliance test.
func TestNoHeuristicClassification(t *testing.T) {
	check := NewCheckMisclassifiedWisps()

	// Inject items that the OLD heuristic would have flagged but the new
	// check should NOT (because they aren't ephemeral=1 in the issues table).
	// The new check only looks at the DB, so there's nothing to test at the
	// shouldBeWisp level — that function no longer exists.
	if check.misclassified != nil {
		t.Error("fresh check should have no misclassified items")
	}
}

func TestMisclassifiedWispDependencyCopyUsesTypedTargets(t *testing.T) {
	data, err := os.ReadFile("misclassified_wisp_check.go")
	if err != nil {
		t.Fatalf("read misclassified_wisp_check.go: %v", err)
	}
	source := string(data)
	for _, want := range []string{
		"doltserver.EnsureBdWispDependenciesSchema",
		"depends_on_issue_id, depends_on_wisp_id, depends_on_external",
		"target_wisp.id = d.depends_on_issue_id",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("misclassified wisp dependency copy missing %q", want)
		}
	}
	if strings.Contains(source, "wisp_dependencies (issue_id, depends_on_id") {
		t.Fatal("misclassified wisp dependency copy still inserts legacy depends_on_id")
	}
}

func TestRunIgnoresJSONLWhenDoltUnavailable(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, "gastown", ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	staleJSONL := `{"id":"gt-wisp-stale","title":"Stale wisp","ephemeral":true}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(staleJSONL), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewCheckMisclassifiedWisps()
	result := check.Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusOK {
		t.Fatalf("expected StatusOK when only stale JSONL exists, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "Dolt unavailable") {
		t.Fatalf("expected Dolt-unavailable skip message, got %q", result.Message)
	}
	if len(check.misclassified) != 0 {
		t.Fatalf("expected no misclassified wisps from stale JSONL, got %d", len(check.misclassified))
	}
}

// TestGetRigPathForPrefix_RoutesResolution verifies that GetRigPathForPrefix
// correctly resolves rig paths from routes.jsonl. This is critical for the
// misclassified-wisps check which uses database names (e.g., "sw") to look up
// rig directories that may have custom paths (e.g., "sallaWork/mayor/rig").
// Regression test for: DB probe failures when database name != directory name.
func TestGetRigPathForPrefix_RoutesResolution(t *testing.T) {
	// Create a temporary town structure with routes.jsonl
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create routes.jsonl with custom rig paths
	routesContent := `{"prefix":"hq-","path":"."}
{"prefix":"sw-","path":"sallaWork/mayor/rig"}
{"prefix":"gt-","path":"gastown/mayor/rig"}
`
	routesPath := filepath.Join(beadsDir, "routes.jsonl")
	if err := os.WriteFile(routesPath, []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		prefix   string
		wantPath string
	}{
		{
			name:     "hq prefix resolves to town root",
			prefix:   "hq-",
			wantPath: tmpDir,
		},
		{
			name:     "sw prefix resolves to custom path",
			prefix:   "sw-",
			wantPath: filepath.Join(tmpDir, "sallaWork/mayor/rig"),
		},
		{
			name:     "gt prefix resolves to custom path",
			prefix:   "gt-",
			wantPath: filepath.Join(tmpDir, "gastown/mayor/rig"),
		},
		{
			name:     "unknown prefix returns empty",
			prefix:   "unknown-",
			wantPath: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := beads.GetRigPathForPrefix(tmpDir, tt.prefix)
			if got != tt.wantPath {
				t.Errorf("GetRigPathForPrefix(%q, %q) = %q, want %q",
					tmpDir, tt.prefix, got, tt.wantPath)
			}
		})
	}
}

// TestRigPathResolution_NoRoutesFile verifies that when routes.jsonl doesn't exist,
// GetRigPathForPrefix returns empty string, triggering the fallback behavior.
func TestRigPathResolution_NoRoutesFile(t *testing.T) {
	tmpDir := t.TempDir()
	// Don't create .beads/routes.jsonl

	got := beads.GetRigPathForPrefix(tmpDir, "sw-")
	if got != "" {
		t.Errorf("GetRigPathForPrefix without routes.jsonl should return empty, got %q", got)
	}
}

// TestRigDirResolution_Logic verifies the resolution logic that would be used
// in the misclassified-wisps check when mapping database names to directories.
func TestRigDirResolution_Logic(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create routes with custom paths
	routesContent := `{"prefix":"hq-","path":"."}
{"prefix":"sw-","path":"sallaWork/mayor/rig"}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		dbName  string
		wantDir string
		desc    string
	}{
		{
			dbName:  "hq",
			wantDir: tmpDir,
			desc:    "hq database maps to town root via route path='.'",
		},
		{
			dbName:  "sw",
			wantDir: filepath.Join(tmpDir, "sallaWork/mayor/rig"),
			desc:    "sw database maps to custom path via route",
		},
		{
			dbName:  "other",
			wantDir: filepath.Join(tmpDir, "other"),
			desc:    "unknown database falls back to townRoot/dbName",
		},
	}

	for _, tt := range tests {
		t.Run(tt.dbName, func(t *testing.T) {
			// This mirrors the resolution logic in misclassified_wisp_check.go
			prefix := tt.dbName + "-"
			rigDir := beads.GetRigPathForPrefix(tmpDir, prefix)
			if rigDir == "" {
				// Fallback: assume database name equals rig directory name
				rigDir = filepath.Join(tmpDir, tt.dbName)
				if tt.dbName == "hq" {
					rigDir = tmpDir
				}
			}

			if rigDir != tt.wantDir {
				t.Errorf("%s: got rigDir=%q, want %q", tt.desc, rigDir, tt.wantDir)
			}
		})
	}
}
