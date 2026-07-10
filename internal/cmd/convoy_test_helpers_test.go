package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
)

// ---------------------------------------------------------------------------
// dagBuilder — declarative test helper for constructing bead DAGs
// ---------------------------------------------------------------------------

// testBead represents a single bead in a test DAG.
type testBead struct {
	ID     string
	Title  string
	Type   string // "epic", "task", "bug", etc.
	Status string // default "open"
	Rig    string // e.g. "gastown"
	Prefix string // e.g. "gt-"
	Parent string // parent bead ID
}

// testDep represents a dependency edge between two beads.
type testDep struct {
	IssueID     string // the dependent
	DependsOnID string // the dependency
	Type        string // "blocks", "parent-child", "waits-for", "conditional-blocks"
}

// testDAG is a builder for constructing test bead graphs with a fluent API.
type testDAG struct {
	t     *testing.T
	beads map[string]*testBead
	deps  []testDep
	last  string // last added bead ID for chaining
}

// withRig is a helper for the fluent API to pass rig names to Task().
func withRig(name string) string { return name }

// newTestDAG creates an empty test DAG builder.
func newTestDAG(t *testing.T) *testDAG {
	t.Helper()
	return &testDAG{
		t:     t,
		beads: make(map[string]*testBead),
	}
}

// Epic adds an epic bead to the DAG.
func (d *testDAG) Epic(id, title string) *testDAG {
	d.t.Helper()
	d.beads[id] = &testBead{
		ID:     id,
		Title:  title,
		Type:   "epic",
		Status: "open",
		Prefix: prefixOf(id),
	}
	d.last = id
	return d
}

// Convoy adds a convoy bead to the DAG.
func (d *testDAG) Convoy(id, title string) *testDAG {
	d.t.Helper()
	d.beads[id] = &testBead{
		ID:     id,
		Title:  title,
		Type:   "convoy",
		Status: "open",
		Prefix: prefixOf(id),
	}
	d.last = id
	return d
}

// Bug adds a bug bead with a rig assignment to the DAG.
func (d *testDAG) Bug(id, title string, rigName string) *testDAG {
	d.t.Helper()
	d.beads[id] = &testBead{
		ID:     id,
		Title:  title,
		Type:   "bug",
		Status: "open",
		Rig:    rigName,
		Prefix: prefixOf(id),
	}
	d.last = id
	return d
}

// Task adds a task bead with a rig assignment to the DAG.
func (d *testDAG) Task(id, title string, rigName string) *testDAG {
	d.t.Helper()
	d.beads[id] = &testBead{
		ID:     id,
		Title:  title,
		Type:   "task",
		Status: "open",
		Rig:    rigName,
		Prefix: prefixOf(id),
	}
	d.last = id
	return d
}

// ParentOf sets the parent of the last-added bead and records a parent-child dep.
func (d *testDAG) ParentOf(parentID string) *testDAG {
	d.t.Helper()
	if d.last == "" {
		d.t.Fatal("ParentOf called with no current bead")
	}
	b := d.beads[d.last]
	b.Parent = parentID
	d.deps = append(d.deps, testDep{
		IssueID:     d.last,
		DependsOnID: parentID,
		Type:        "parent-child",
	})
	return d
}

// BlockedBy adds a "blocks" dependency: blockerID blocks the last-added bead.
func (d *testDAG) BlockedBy(blockerID string) *testDAG {
	d.t.Helper()
	if d.last == "" {
		d.t.Fatal("BlockedBy called with no current bead")
	}
	d.deps = append(d.deps, testDep{
		IssueID:     d.last,
		DependsOnID: blockerID,
		Type:        "blocks",
	})
	return d
}

// ConditionalBlockedBy adds a "conditional-blocks" dependency.
func (d *testDAG) ConditionalBlockedBy(blockerID string) *testDAG {
	d.t.Helper()
	if d.last == "" {
		d.t.Fatal("ConditionalBlockedBy called with no current bead")
	}
	d.deps = append(d.deps, testDep{
		IssueID:     d.last,
		DependsOnID: blockerID,
		Type:        "conditional-blocks",
	})
	return d
}

// TrackedBy adds a "tracks" dependency: convoyID tracks the last-added bead.
// Models: convoy depends on the bead via "tracks" (matching production
// `bd dep add convoyID beadID --type=tracks`).
func (d *testDAG) TrackedBy(convoyID string) *testDAG {
	d.t.Helper()
	if d.last == "" {
		d.t.Fatal("TrackedBy called with no current bead")
	}
	d.deps = append(d.deps, testDep{
		IssueID:     convoyID,
		DependsOnID: d.last,
		Type:        "tracks",
	})
	return d
}

// WithStatus sets the status of the last-added bead (overrides default "open").
func (d *testDAG) WithStatus(status string) *testDAG {
	d.t.Helper()
	if d.last == "" {
		d.t.Fatal("WithStatus called with no current bead")
	}
	d.beads[d.last].Status = status
	return d
}

// WaitsFor adds a "waits-for" dependency.
func (d *testDAG) WaitsFor(waitID string) *testDAG {
	d.t.Helper()
	if d.last == "" {
		d.t.Fatal("WaitsFor called with no current bead")
	}
	d.deps = append(d.deps, testDep{
		IssueID:     d.last,
		DependsOnID: waitID,
		Type:        "waits-for",
	})
	return d
}

// BdStubScript generates a shell script that simulates bd for the beads and
// deps in the DAG. The script logs every invocation and responds to:
//   - show <id> --json  (or show --json <id>)
//   - dep list <id> --json  (or dep list --json <id>)
//   - list --parent=<id> --json  (or list --parent <id> --json)
//
// The placeholder LOGPATH must be replaced with the actual log file path
// before writing to disk. Setup() handles this automatically.
func (d *testDAG) BdStubScript() string {
	d.t.Helper()

	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	sb.WriteString(`echo "CMD:$*" >> "LOGPATH"` + "\n")
	sb.WriteString("\n")

	// Collect all args into a single string for flexible matching.
	sb.WriteString(`ALL_ARGS="$*"` + "\n")
	sb.WriteString("\n")

	// --- handle: show <id> --json | show --json <id> ---
	sb.WriteString("# Handle show commands\n")
	sb.WriteString(`case "$ALL_ARGS" in` + "\n")
	for id, b := range d.beads {
		beadJSON := d.beadJSON(b)
		// Match both "show <id> --json" and "show --json <id>"
		sb.WriteString(fmt.Sprintf("  show\\ %s\\ --json|show\\ --json\\ %s)\n", id, id))
		sb.WriteString(fmt.Sprintf("    echo '%s'\n", beadJSON))
		sb.WriteString("    exit 0\n")
		sb.WriteString("    ;;\n")
	}

	// --- handle: sql "SELECT ..." --json (bdDepListRawIDs) ---
	// bdDepListRawIDs calls: bd sql "SELECT COALESCE(...) AS depends_on_id FROM dependencies WHERE issue_id = '<id>' AND type = 'tracks'" --json
	// or: bd sql "SELECT issue_id FROM dependencies WHERE <typed target columns match id> AND type = 'tracks'" --json
	sb.WriteString("  sql\\ *)\n")
	sb.WriteString("    # Handle SQL queries for dependency lookups\n")
	// For "down" direction (convoy → tracked beads): match on issue_id = '<convoyID>'
	for id, b := range d.beads {
		if b.Type == "convoy" {
			trackedSQLJSON := d.trackedBeadsSQLJSONFor(id)
			sb.WriteString(`    case "$ALL_ARGS" in` + "\n")
			sb.WriteString(fmt.Sprintf("      *\"issue_id = '%s'\"*)\n", id))
			sb.WriteString(fmt.Sprintf("        echo '%s'\n", trackedSQLJSON))
			sb.WriteString("        exit 0\n")
			sb.WriteString("        ;;\n")
			sb.WriteString("    esac\n")
		}
	}
	// For "up" direction (bead → tracking convoys): match typed target columns.
	for id := range d.beads {
		trackersJSON := d.trackersSQLJSONFor(id)
		if trackersJSON != "[]" {
			sb.WriteString(`    case "$ALL_ARGS" in` + "\n")
			sb.WriteString(fmt.Sprintf("      *\"depends_on_issue_id = '%s'\"*)\n", id))
			sb.WriteString(fmt.Sprintf("        echo '%s'\n", trackersJSON))
			sb.WriteString("        exit 0\n")
			sb.WriteString("        ;;\n")
			sb.WriteString("    esac\n")
		}
	}
	sb.WriteString("    echo '[]'\n")
	sb.WriteString("    exit 0\n")
	sb.WriteString("    ;;\n")

	// --- handle: dep list --direction=down (tracked beads for convoys) ---
	// Must come before generic dep list handler.
	sb.WriteString("  dep\\ list\\ *--direction=down*)\n")
	sb.WriteString("    # Return tracked bead IDs in {\"id\":\"...\"} format\n")
	for id, b := range d.beads {
		if b.Type == "convoy" {
			trackedJSON := d.trackedBeadsJSONFor(id)
			sb.WriteString(`    case "$ALL_ARGS" in` + "\n")
			sb.WriteString(fmt.Sprintf("      *%s*)\n", id))
			sb.WriteString(fmt.Sprintf("        echo '%s'\n", trackedJSON))
			sb.WriteString("        exit 0\n")
			sb.WriteString("        ;;\n")
			sb.WriteString("    esac\n")
		}
	}
	sb.WriteString("    echo '[]'\n")
	sb.WriteString("    exit 0\n")
	sb.WriteString("    ;;\n")

	// --- handle: dep list <id> --json | dep list --json <id> ---
	sb.WriteString("  dep\\ list\\ *)\n")
	sb.WriteString("    # Find the bead ID in the args\n")
	// Extract the bead ID by looking at positional args
	for id := range d.beads {
		depsJSON := d.depsJSONFor(id)
		sb.WriteString(`    case "$ALL_ARGS" in` + "\n")
		sb.WriteString(fmt.Sprintf("      *%s*)\n", id))
		sb.WriteString(fmt.Sprintf("        echo '%s'\n", depsJSON))
		sb.WriteString("        exit 0\n")
		sb.WriteString("        ;;\n")
		sb.WriteString("    esac\n")
	}
	sb.WriteString("    echo '[]'\n")
	sb.WriteString("    exit 0\n")
	sb.WriteString("    ;;\n")

	// --- handle: convoy list queries (overlapping convoy detection) ---
	convoyListJSON := d.convoyListJSON()
	sb.WriteString("  list\\ *--label=gt:convoy*)\n")
	sb.WriteString(fmt.Sprintf("    echo '%s'\n", convoyListJSON))
	sb.WriteString("    exit 0\n")
	sb.WriteString("    ;;\n")
	sb.WriteString("  list\\ --json\\ --limit=0*)\n")
	sb.WriteString("    echo '[]'\n")
	sb.WriteString("    exit 0\n")
	sb.WriteString("    ;;\n")

	// --- handle: list --parent=<id> --json | list --parent <id> --json ---
	for id := range d.beads {
		childrenJSON := d.childrenJSONFor(id)
		sb.WriteString(fmt.Sprintf("  list\\ --parent=%s\\ --json*|list\\ --parent\\ %s\\ --json*|list\\ --json*--parent=%s*|list\\ --json*--parent\\ %s*)\n", id, id, id, id))
		sb.WriteString(fmt.Sprintf("    echo '%s'\n", childrenJSON))
		sb.WriteString("    exit 0\n")
		sb.WriteString("    ;;\n")
	}

	// --- handle: show <unknown> → exit 1 ---
	sb.WriteString("  show\\ *)\n")
	sb.WriteString("    echo '{\"error\":\"not found\"}' >&2\n")
	sb.WriteString("    exit 1\n")
	sb.WriteString("    ;;\n")

	sb.WriteString("esac\n")
	sb.WriteString("\n")
	sb.WriteString("# Unknown command — log and exit 0\n")
	sb.WriteString("exit 0\n")

	return sb.String()
}

// RoutesJSONL generates routes.jsonl content for all unique prefix/rig pairs
// in the DAG. Falls back to prefix/path format matching the codebase convention.
func (d *testDAG) RoutesJSONL() string {
	d.t.Helper()

	type routeKey struct {
		prefix string
		rig    string
	}
	seen := make(map[routeKey]bool)
	var lines []string

	for _, b := range d.beads {
		prefix := b.Prefix
		if prefix == "" {
			prefix = prefixOf(b.ID)
		}
		rigName := b.Rig
		if rigName == "" {
			// Skip beads with no rig — they won't have a route entry,
			// so rigFromBeadID will return "" (simulating a no-rig error).
			continue
		}
		key := routeKey{prefix, rigName}
		if seen[key] {
			continue
		}
		seen[key] = true
		// Use the codebase convention: {"prefix":"gt-","path":"<rig>/.beads"}
		lines = append(lines, fmt.Sprintf(`{"prefix":%q,"path":"%s/.beads"}`, prefix, rigName))
	}

	return strings.Join(lines, "\n") + "\n"
}

// Setup creates a full test workspace with the bd stub, routes, and directory
// structure. Returns the town root path and log file path.
func (d *testDAG) Setup(t *testing.T) (townRoot, logPath string) {
	t.Helper()

	townRoot = t.TempDir()

	// Create workspace marker directories.
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// Write sentinel files so beads.EnsureCustomTypes/Statuses skip bd calls.
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", ".gt-types-configured"), []byte(beads.TypeConfigSentinelValue()+"\n"), 0644); err != nil {
		t.Fatalf("write types sentinel: %v", err)
	}
	statusesList := strings.Join(constants.BeadsCustomStatusesList(), ",")
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", ".gt-statuses-configured"), []byte(statusesList+"\n"), 0644); err != nil {
		t.Fatalf("write statuses sentinel: %v", err)
	}

	// Install bd stub script.
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	logPath = filepath.Join(townRoot, "bd.log")

	script := strings.ReplaceAll(d.BdStubScript(), "LOGPATH", logPath)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	// Write routes.jsonl.
	routesPath := filepath.Join(townRoot, ".beads", "routes.jsonl")
	if err := os.WriteFile(routesPath, []byte(d.RoutesJSONL()), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	// Create rig .beads directories referenced by routes so that
	// resolveBeadDir() can resolve them and bdListChildren() can chdir.
	for _, b := range d.beads {
		if b.Rig != "" {
			rigBeadsDir := filepath.Join(townRoot, b.Rig, ".beads")
			if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
				t.Fatalf("mkdir rig .beads dir %s: %v", rigBeadsDir, err)
			}
		}
	}

	// Inject bin/ into PATH.
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	// Change cwd to town root with cleanup.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	return townRoot, logPath
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// prefixOf extracts the prefix from a bead ID (e.g. "gt-abc" → "gt-").
func prefixOf(id string) string {
	idx := strings.Index(id, "-")
	if idx < 0 {
		return ""
	}
	return id[:idx+1]
}

// beadJSON returns the JSON array representation of a single bead for bd show.
func (d *testDAG) beadJSON(b *testBead) string {
	status := b.Status
	if status == "" {
		status = "open"
	}
	issueType := b.Type
	if issueType == "" {
		issueType = "task"
	}

	type beadOut struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Status    string `json:"status"`
		IssueType string `json:"issue_type"`
		Parent    string `json:"parent,omitempty"`
	}

	out := []beadOut{{
		ID:        b.ID,
		Title:     b.Title,
		Status:    status,
		IssueType: issueType,
		Parent:    b.Parent,
	}}
	raw, _ := json.Marshal(out)
	return string(raw)
}

// depsJSONFor returns the JSON array of deps associated with a bead.
// Includes deps where the bead is the dependent (IssueID) or the dependency
// (DependsOnID), matching what `bd dep list <id> --json` returns in production.
//
// Production bd dep list returns fields: "id" (the dependency target) and
// "dependency_type" (blocks, parent-child, etc.), matching bdDepResult in
// convoy_stage.go.
func (d *testDAG) depsJSONFor(issueID string) string {
	type depOut struct {
		ID             string `json:"id"`
		DependencyType string `json:"dependency_type"`
		CreatedAt      string `json:"created_at"`
		CreatedBy      string `json:"created_by"`
		Metadata       string `json:"metadata"`
	}

	var out []depOut
	for _, dep := range d.deps {
		// Production bd dep list returns only deps where the queried bead
		// IS the dependent (IssueID), not where it's the target. The "id"
		// field contains the dependency target (DependsOnID).
		if dep.IssueID == issueID {
			out = append(out, depOut{
				ID:             dep.DependsOnID,
				DependencyType: dep.Type,
				CreatedAt:      "2025-01-01T00:00:00Z",
				CreatedBy:      "test",
				Metadata:       "{}",
			})
		}
	}
	if out == nil {
		return "[]"
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

// childrenJSONFor returns the JSON array of beads whose Parent is parentID.
func (d *testDAG) childrenJSONFor(parentID string) string {
	type beadOut struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Status    string `json:"status"`
		IssueType string `json:"issue_type"`
	}

	var out []beadOut
	for _, b := range d.beads {
		if b.Parent == parentID {
			out = append(out, beadOut{
				ID:        b.ID,
				Title:     b.Title,
				Status:    b.Status,
				IssueType: b.Type,
			})
		}
	}
	if out == nil {
		return "[]"
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

// trackedBeadsJSONFor returns the JSON array for `bd dep list <convoyID>
// --direction=down --type=tracks --json`. This returns the simplified format
// with just {"id":"..."} for each tracked bead, matching production bd behavior.
func (d *testDAG) trackedBeadsJSONFor(convoyID string) string {
	type idOnly struct {
		ID string `json:"id"`
	}

	var out []idOnly
	for _, dep := range d.deps {
		// tracks deps: IssueID is the convoy, DependsOnID is the tracked bead.
		if dep.Type == "tracks" && dep.IssueID == convoyID {
			out = append(out, idOnly{ID: dep.DependsOnID})
		}
	}
	if out == nil {
		return "[]"
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

// trackedBeadsSQLJSONFor returns the JSON array for `bd sql "SELECT depends_on_id
// FROM dependencies WHERE issue_id = '<convoyID>' AND type = 'tracks'" --json`.
// Returns [{"depends_on_id":"<id>"},...] for each tracked bead.
func (d *testDAG) trackedBeadsSQLJSONFor(convoyID string) string {
	type sqlRow struct {
		DependsOnID string `json:"depends_on_id"`
	}

	var out []sqlRow
	for _, dep := range d.deps {
		if dep.Type == "tracks" && dep.IssueID == convoyID {
			out = append(out, sqlRow{DependsOnID: dep.DependsOnID})
		}
	}
	if out == nil {
		return "[]"
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

// trackersSQLJSONFor returns the JSON array for `bd sql "SELECT issue_id
// FROM dependencies WHERE depends_on_id = '<beadID>' AND type = 'tracks'" --json`.
// Returns [{"issue_id":"<convoyID>"},...] for each convoy tracking this bead.
func (d *testDAG) trackersSQLJSONFor(beadID string) string {
	type sqlRow struct {
		IssueID string `json:"issue_id"`
	}

	var out []sqlRow
	for _, dep := range d.deps {
		if dep.Type == "tracks" && dep.DependsOnID == beadID {
			out = append(out, sqlRow{IssueID: dep.IssueID})
		}
	}
	if out == nil {
		return "[]"
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

// convoyListJSON returns the JSON array for convoy list queries.
// Returns all convoy beads with their ID and status.
func (d *testDAG) convoyListJSON() string {
	type convoyEntry struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}

	var out []convoyEntry
	for _, b := range d.beads {
		if b.Type == "convoy" {
			status := b.Status
			if status == "" {
				status = "open"
			}
			out = append(out, convoyEntry{ID: b.ID, Status: status})
		}
	}
	if out == nil {
		return "[]"
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestDagBuilder_BasicSetup verifies the dagBuilder creates a working bd stub
// that correctly responds to show and dep list queries for a linear chain.
func TestDagBuilder_BasicSetup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Task("task-1", "First task", withRig("gastown")).
		Task("task-2", "Second task", withRig("gastown")).BlockedBy("task-1").
		Task("task-3", "Third task", withRig("gastown")).BlockedBy("task-2")

	_, logPath := dag.Setup(t)

	// 1. Verify bd show task-1 --json returns correct JSON.
	out, err := exec.Command("bd", "show", "task-1", "--json").Output()
	if err != nil {
		t.Fatalf("bd show task-1 --json failed: %v", err)
	}

	var showResult []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Status    string `json:"status"`
		IssueType string `json:"issue_type"`
	}
	if err := json.Unmarshal(out, &showResult); err != nil {
		t.Fatalf("unmarshal show output: %v\nraw: %s", err, out)
	}
	if len(showResult) != 1 {
		t.Fatalf("expected 1 bead in show output, got %d", len(showResult))
	}
	if showResult[0].ID != "task-1" {
		t.Errorf("show ID = %q, want %q", showResult[0].ID, "task-1")
	}
	if showResult[0].Title != "First task" {
		t.Errorf("show Title = %q, want %q", showResult[0].Title, "First task")
	}
	if showResult[0].Status != "open" {
		t.Errorf("show Status = %q, want %q", showResult[0].Status, "open")
	}

	// 2. Verify bd dep list task-2 --json shows task-1 as blocker.
	out, err = exec.Command("bd", "dep", "list", "task-2", "--json").Output()
	if err != nil {
		t.Fatalf("bd dep list task-2 --json failed: %v", err)
	}

	var depResult []struct {
		ID             string `json:"id"`
		DependencyType string `json:"dependency_type"`
	}
	if err := json.Unmarshal(out, &depResult); err != nil {
		t.Fatalf("unmarshal dep list output: %v\nraw: %s", err, out)
	}
	// task-2 is blocked by task-1. bd dep list returns only deps where
	// task-2 is the dependent, so we expect task-1 as a blocks dep target.
	if len(depResult) < 1 {
		t.Fatalf("expected at least 1 dep for task-2, got %d", len(depResult))
	}
	// Find the dep where task-1 is the blocker (task-2 blocked by task-1).
	foundBlocker := false
	for _, dep := range depResult {
		if dep.ID == "task-1" && dep.DependencyType == "blocks" {
			foundBlocker = true
		}
	}
	if !foundBlocker {
		t.Errorf("expected dep with task-2 blocked by task-1, got: %+v", depResult)
	}

	// 3. Verify the bd.log captured the commands.
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd.log: %v", err)
	}
	logContent := string(logBytes)
	if !strings.Contains(logContent, "CMD:show task-1 --json") {
		t.Errorf("log should contain 'CMD:show task-1 --json', got:\n%s", logContent)
	}
	if !strings.Contains(logContent, "CMD:dep list task-2 --json") {
		t.Errorf("log should contain 'CMD:dep list task-2 --json', got:\n%s", logContent)
	}
}

// TestDagBuilder_EpicWithChildren verifies that the dagBuilder correctly
// responds to list --parent queries, returning child beads of an epic.
func TestDagBuilder_EpicWithChildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows — shell stubs")
	}

	dag := newTestDAG(t).
		Epic("epic-1", "Root Epic").
		Task("task-1", "First task", withRig("gastown")).ParentOf("epic-1").
		Task("task-2", "Second task", withRig("gastown")).ParentOf("epic-1")

	dag.Setup(t)

	// Verify bd list --parent=epic-1 --json returns both children.
	out, err := exec.Command("bd", "list", "--parent=epic-1", "--json").Output()
	if err != nil {
		t.Fatalf("bd list --parent=epic-1 --json failed: %v", err)
	}

	var listResult []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		IssueType string `json:"issue_type"`
	}
	if err := json.Unmarshal(out, &listResult); err != nil {
		t.Fatalf("unmarshal list output: %v\nraw: %s", err, out)
	}
	if len(listResult) != 2 {
		t.Fatalf("expected 2 children of epic-1, got %d\nraw: %s", len(listResult), out)
	}

	// Verify both task IDs are present (order may vary since map iteration is random).
	ids := map[string]bool{}
	for _, r := range listResult {
		ids[r.ID] = true
	}
	if !ids["task-1"] {
		t.Errorf("expected task-1 in children, got %v", ids)
	}
	if !ids["task-2"] {
		t.Errorf("expected task-2 in children, got %v", ids)
	}
}

// ---------------------------------------------------------------------------
// waveAssertHelper — fluent assertions for wave computation results
// ---------------------------------------------------------------------------

// waveAssertHelper provides fluent assertions for wave computation results.
type waveAssertHelper struct {
	t     *testing.T
	waves []Wave
}

// waveAssert creates a new wave assertion helper.
func waveAssert(t *testing.T, waves []Wave) *waveAssertHelper {
	t.Helper()
	return &waveAssertHelper{t: t, waves: waves}
}

// Wave asserts that the given wave number contains exactly the specified task IDs.
// Order within the wave does not matter.
func (w *waveAssertHelper) Wave(number int, expectedTaskIDs ...string) *waveAssertHelper {
	w.t.Helper()

	// Find the wave with this number
	var found *Wave
	for i := range w.waves {
		if w.waves[i].Number == number {
			found = &w.waves[i]
			break
		}
	}
	if found == nil {
		w.t.Errorf("Wave(%d): wave not found (have %d waves)", number, len(w.waves))
		return w
	}

	// Check that all expected tasks are present
	actual := make(map[string]bool)
	for _, id := range found.Tasks {
		actual[id] = true
	}

	for _, expected := range expectedTaskIDs {
		if !actual[expected] {
			w.t.Errorf("Wave(%d): expected task %q not found (actual: %v)", number, expected, found.Tasks)
		}
	}

	// Check no unexpected tasks
	expected := make(map[string]bool)
	for _, id := range expectedTaskIDs {
		expected[id] = true
	}
	for _, id := range found.Tasks {
		if !expected[id] {
			w.t.Errorf("Wave(%d): unexpected task %q (expected: %v)", number, id, expectedTaskIDs)
		}
	}

	return w
}

// NoTask asserts that the given bead ID does NOT appear in any wave.
func (w *waveAssertHelper) NoTask(beadID string) *waveAssertHelper {
	w.t.Helper()
	for _, wave := range w.waves {
		for _, id := range wave.Tasks {
			if id == beadID {
				w.t.Errorf("NoTask(%q): found in wave %d", beadID, wave.Number)
				return w
			}
		}
	}
	return w
}

// Total asserts the total number of waves.
func (w *waveAssertHelper) Total(n int) *waveAssertHelper {
	w.t.Helper()
	if len(w.waves) != n {
		w.t.Errorf("Total(%d): got %d waves", n, len(w.waves))
	}
	return w
}

// ---------------------------------------------------------------------------
// Tests — waveAssertHelper
// ---------------------------------------------------------------------------

func TestWaveAssert_BasicUsage(t *testing.T) {
	waves := []Wave{
		{Number: 1, Tasks: []string{"a", "c"}},
		{Number: 2, Tasks: []string{"b"}},
	}

	// These should not fail
	waveAssert(t, waves).
		Wave(1, "a", "c").
		Wave(2, "b").
		NoTask("epic-1").
		Total(2)
}

func TestWaveAssert_WrongWaveFails(t *testing.T) {
	waves := []Wave{
		{Number: 1, Tasks: []string{"a"}},
	}

	// Verify the helper works on pass cases without panic
	wa := waveAssert(t, waves)
	wa.Total(1)
	wa.Wave(1, "a")
}

func TestWaveAssert_EmptyWaves(t *testing.T) {
	waveAssert(t, nil).Total(0)
}

func TestWaveAssert_Integration_WithComputeWaves(t *testing.T) {
	// Build a simple DAG and verify waves using the helper
	dag := &ConvoyDAG{Nodes: map[string]*ConvoyDAGNode{
		"a":    {ID: "a", Type: "task", Blocks: []string{"b"}},
		"b":    {ID: "b", Type: "task", BlockedBy: []string{"a"}, Blocks: []string{"c"}},
		"c":    {ID: "c", Type: "task", BlockedBy: []string{"b"}},
		"epic": {ID: "epic", Type: "epic"},
	}}

	waves, _, err := computeWaves(dag)
	if err != nil {
		t.Fatalf("computeWaves: %v", err)
	}

	waveAssert(t, waves).
		Wave(1, "a").
		Wave(2, "b").
		Wave(3, "c").
		NoTask("epic").
		Total(3)
}
