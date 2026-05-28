package cmd

// Shared test helpers for scheduler tests. No build tag — compiled for both
// integration and e2e_agent builds. Helpers that need bd/gt binaries take
// explicit paths and env slices so callers control isolation.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
	"github.com/steveyegge/gastown/internal/testutil"
)

// --- Environment helpers ---

// cleanSchedulerTestEnv returns os.Environ() with GT_*/BD_* variables removed
// (except GT_DOLT_PORT and BEADS_DOLT_PORT) and HOME overridden to tmpHome.
// This isolates gt/bd processes from the host while preserving test Dolt routing.
func cleanSchedulerTestEnv(tmpHome string) []string {
	return testutil.CleanGTEnv("HOME=" + tmpHome)
}

func schedulerBDEnv(dir string, mutating bool) []string {
	base := testutil.CleanGTEnv()
	beadsDir := filepath.Join(dir, ".beads")
	if mutating {
		return beads.BuildMutationPinnedBDEnv(base, beadsDir)
	}
	return beads.BuildReadOnlyPinnedBDEnv(base, beadsDir)
}

func schedulerBDEnvWithHome(dir, homeDir string, mutating bool) []string {
	base := testutil.CleanGTEnv("HOME=" + homeDir)
	beadsDir := filepath.Join(dir, ".beads")
	if mutating {
		return beads.BuildMutationPinnedBDEnv(base, beadsDir)
	}
	return beads.BuildReadOnlyPinnedBDEnv(base, beadsDir)
}

// --- File helpers ---

// writeJSONFile marshals v as indented JSON and writes it to path,
// creating parent directories as needed.
func writeJSONFile(t *testing.T, path string, v interface{}) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal JSON for %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// --- Scheduler config helpers ---

// configureScheduler writes a TownSettings file with the given scheduler configuration.
// maxPolecats > 0 enables deferred dispatch; -1 means direct dispatch.
func configureScheduler(t *testing.T, hqPath string, maxPolecats, batchSize int) {
	t.Helper()
	settings := config.NewTownSettings()
	settings.Scheduler = &capacity.SchedulerConfig{
		MaxPolecats: &maxPolecats,
		BatchSize:   &batchSize,
	}
	writeJSONFile(t, config.TownSettingsPath(hqPath), settings)
}

// --- gt command helpers ---

// runGTCmdOutput runs a gt command and returns stdout only.
// Fails the test if the command exits non-zero.
func runGTCmdOutput(t *testing.T, binary, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("gt %v failed: %v\nstdout:\n%s\nstderr:\n%s", args, err, out, stderr.String())
	}
	return string(out)
}

// runGTCmdMayFail runs a gt command and returns combined output and any error.
// Does NOT fail the test on non-zero exit.
func runGTCmdMayFail(t *testing.T, binary, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// --- Scheduler query helpers ---

// getSchedulerStatus runs `gt scheduler status --json` and returns the parsed output.
func getSchedulerStatus(t *testing.T, gtBinary, dir string, env []string) map[string]interface{} {
	t.Helper()
	out := runGTCmdOutput(t, gtBinary, dir, env, "scheduler", "status", "--json")
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("parse scheduler status JSON: %v\nraw: %s", err, out)
	}
	return result
}

// getSchedulerList runs `gt scheduler list --json` and returns the parsed output.
func getSchedulerList(t *testing.T, gtBinary, dir string, env []string) []map[string]interface{} {
	t.Helper()
	out := runGTCmdOutput(t, gtBinary, dir, env, "scheduler", "list", "--json")
	var result []map[string]interface{}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("parse scheduler list JSON: %v\nraw: %s", err, out)
	}
	return result
}

// --- Bead helpers ---

var schedulerBeadIDCounter atomic.Int64

func schedulerNextBeadID(t *testing.T, dir string) string {
	t.Helper()
	prefix := schedulerConfiguredPrefix(t, dir)
	return fmt.Sprintf("%s-%06x", prefix, schedulerBeadIDCounter.Add(1))
}

func schedulerConfiguredPrefix(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".beads", "config.yaml"))
	if err != nil {
		t.Fatalf("read scheduler beads config in %s: %v", dir, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		for _, key := range []string{"issue-prefix:", "prefix:"} {
			if strings.HasPrefix(trimmed, key) {
				value := strings.TrimSpace(strings.TrimPrefix(trimmed, key))
				value = strings.Trim(value, `"'`)
				value = strings.TrimSuffix(value, "-")
				if value != "" {
					return value
				}
			}
		}
	}
	t.Fatalf("scheduler beads config in %s has no prefix", dir)
	return ""
}

// createTestBead creates a bead with the given title using bd create and returns
// the auto-generated bead ID.
func createTestBead(t *testing.T, dir, title string) string {
	t.Helper()
	args := []string{"create", "--title=" + title, "--type=task",
		"--description=Integration test bead", "--id=" + schedulerNextBeadID(t, dir), "--force", "--json"}
	cmd := exec.Command("bd", args...)
	cmd.Dir = dir
	cmd.Env = schedulerBDEnv(dir, true)
	out, err := cmd.Output()
	if err != nil {
		// Capture stderr for diagnostics
		cmd2 := exec.Command("bd", args...)
		cmd2.Dir = dir
		cmd2.Env = schedulerBDEnv(dir, true)
		combined, _ := cmd2.CombinedOutput()
		t.Fatalf("bd create failed: %v\n%s", err, combined)
	}
	var issue struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &issue); err != nil {
		t.Fatalf("parse bd create output: %v\nraw: %s", err, out)
	}
	if issue.ID == "" {
		t.Fatalf("bd create returned empty ID\nraw: %s", out)
	}
	return issue.ID
}

// beadHasLabel checks whether a bead has the specified label.
// Runs bd show --json from dir and inspects the labels array.
func beadHasLabel(t *testing.T, beadID, label, dir string) bool {
	t.Helper()
	args := beads.MaybePrependAllowStale([]string{"show", beadID, "--json"})
	cmd := exec.Command("bd", args...)
	cmd.Dir = dir
	cmd.Env = schedulerBDEnv(dir, false)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bd show %s failed: %v", beadID, err)
	}
	var issues []struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(out, &issues); err != nil {
		t.Fatalf("parse bd show %s: %v", beadID, err)
	}
	if len(issues) == 0 {
		t.Fatalf("bd show %s returned no results", beadID)
	}
	for _, l := range issues[0].Labels {
		if l == label {
			return true
		}
	}
	return false
}

// getBeadDescription returns the description of a bead via bd show --json.
func getBeadDescription(t *testing.T, beadID, dir string) string {
	t.Helper()
	args := beads.MaybePrependAllowStale([]string{"show", beadID, "--json"})
	cmd := exec.Command("bd", args...)
	cmd.Dir = dir
	cmd.Env = schedulerBDEnv(dir, false)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("bd show %s failed: %v\nstderr: %s", beadID, err, exitErr.Stderr)
		}
		t.Fatalf("bd show %s failed: %v", beadID, err)
	}
	var issues []struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(out, &issues); err != nil {
		t.Fatalf("parse bd show %s: %v", beadID, err)
	}
	if len(issues) == 0 {
		t.Fatalf("bd show %s returned no results", beadID)
	}
	return issues[0].Description
}

// updateBeadDescription updates a bead's description via bd update.
func updateBeadDescription(t *testing.T, beadID, description, dir string) {
	t.Helper()
	cmd := exec.Command("bd", "update", beadID, "--description="+description)
	cmd.Dir = dir
	cmd.Env = schedulerBDEnv(dir, true)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd update %s description failed: %v\n%s", beadID, err, out)
	}
}

// addBeadLabel adds a label to a bead via bd update.
func addBeadLabel(t *testing.T, beadID, label, dir string) {
	t.Helper()
	cmd := exec.Command("bd", "update", beadID, "--add-label="+label)
	cmd.Dir = dir
	cmd.Env = schedulerBDEnv(dir, true)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd update %s --add-label=%s failed: %v\n%s", beadID, label, err, out)
	}
}

// addBeadDependency adds a blocking dependency: blocker blocks blocked.
func addBeadDependency(t *testing.T, blocked, blocker, dir string) {
	t.Helper()
	cmd := exec.Command("bd", "dep", "add", blocked, blocker)
	cmd.Dir = dir
	cmd.Env = schedulerBDEnv(dir, true)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd dep add %s %s failed: %v\n%s", blocked, blocker, err, out)
	}
}

// addBeadDependencyOfType adds a dependency with a specific type (e.g., "tracks",
// "depends_on"). The from bead must exist in the local DB at dir; the to bead can
// be in a different DB if routes.jsonl is present in dir's .beads/.
func addBeadDependencyOfType(t *testing.T, from, to, depType, dir string) {
	t.Helper()
	targetColumn := "depends_on_issue_id"
	if strings.HasPrefix(to, "external:") {
		targetColumn = "depends_on_external"
	}

	oldSchemaQuery := fmt.Sprintf(
		"REPLACE INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id) VALUES ('%s', '%s', '%s', NOW(), 'scheduler-test', JSON_OBJECT(), '')",
		sqlStringLiteral(from),
		sqlStringLiteral(to),
		sqlStringLiteral(depType),
	)
	if out, err := runSchedulerSQL(dir, oldSchemaQuery); err != nil {
		if !isDependencyTargetColumnError(fmt.Errorf("%s", out)) {
			t.Fatalf("bd sql dep %s %s --type=%s failed: %v\n%s", from, to, depType, err, out)
		}
		newSchemaQuery := fmt.Sprintf(
			"REPLACE INTO dependencies (issue_id, %s, type, created_at, created_by, metadata, thread_id) VALUES ('%s', '%s', '%s', NOW(), 'scheduler-test', JSON_OBJECT(), '')",
			targetColumn,
			sqlStringLiteral(from),
			sqlStringLiteral(to),
			sqlStringLiteral(depType),
		)
		if out, err := runSchedulerSQL(dir, newSchemaQuery); err != nil {
			t.Fatalf("bd sql dep %s %s --type=%s failed: %v\n%s", from, to, depType, err, out)
		}
	}
}

func runSchedulerSQL(dir, query string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bd", "sql", query)
	cmd.Dir = dir
	cmd.Env = schedulerBDEnv(dir, true)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, err
}

func sqlStringLiteral(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// createTestBeadOfType creates a bead with the given title and issue type (e.g.,
// "epic", "convoy", "task") and returns the auto-generated bead ID.
func createTestBeadOfType(t *testing.T, dir, title, issueType string) string {
	t.Helper()
	args := []string{"create", "--title=" + title, "--type=" + issueType,
		"--description=Integration test bead", "--id=" + schedulerNextBeadID(t, dir), "--force", "--json"}
	cmd := exec.Command("bd", args...)
	cmd.Dir = dir
	cmd.Env = schedulerBDEnv(dir, true)
	out, err := cmd.Output()
	if err != nil {
		cmd2 := exec.Command("bd", args...)
		cmd2.Dir = dir
		cmd2.Env = schedulerBDEnv(dir, true)
		combined, _ := cmd2.CombinedOutput()
		t.Fatalf("bd create --type=%s failed: %v\n%s", issueType, err, combined)
	}
	var issue struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &issue); err != nil {
		t.Fatalf("parse bd create output: %v\nraw: %s", err, out)
	}
	if issue.ID == "" {
		t.Fatalf("bd create returned empty ID\nraw: %s", out)
	}
	return issue.ID
}

// slingToScheduler runs `gt sling <bead> <rig> --hook-raw-bead` in deferred mode.
// The test setup (configureScheduler) sets max_polecats > 0, so gt sling
// automatically defers dispatch without a --scheduler flag.
// Uses --hook-raw-bead to skip formula cooking (no formula infrastructure
// in integration tests).
func slingToScheduler(t *testing.T, gtBinary, dir string, env []string, beadID, rig string, extraFlags ...string) string {
	t.Helper()
	args := []string{"sling", beadID, rig, "--hook-raw-bead"}
	args = append(args, extraFlags...)
	return runGTCmdOutput(t, gtBinary, dir, env, args...)
}
