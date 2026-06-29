package cmd

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/testutil"
)

func TestBuildWitnessPatrolVars_NilContext(t *testing.T) {
	ctx := RoleContext{}
	vars := buildWitnessPatrolVars(ctx)
	if len(vars) != 0 {
		t.Errorf("expected empty vars for nil context, got %v", vars)
	}
}

func TestBuildWitnessPatrolVars_InjectsRigAndPrefix(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildWitnessPatrolVars(ctx)
	if len(vars) != 2 {
		t.Fatalf("expected 2 vars (rig, prefix), got %v", vars)
	}
	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}
	if got := varMap["rig"]; got != "testrig" {
		t.Errorf("rig = %q, want %q", got, "testrig")
	}
	if got := varMap["prefix"]; got != "gt" {
		t.Errorf("prefix = %q, want %q (default fallback)", got, "gt")
	}
}

func TestBuildRefineryPatrolVars_NilContext(t *testing.T) {
	ctx := RoleContext{}
	vars := buildRefineryPatrolVars(ctx)
	if len(vars) != 0 {
		t.Errorf("expected empty vars for nil context, got %v", vars)
	}
}

func TestBuildRefineryPatrolVars_MissingSettings(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(filepath.Join(rigDir, "settings"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)
	// target_branch should always be present (falls back to "main" without rig config)
	if len(vars) != 1 {
		t.Errorf("expected 1 var (target_branch) when settings file missing, got %v", vars)
	}
	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}
	if got := varMap["target_branch"]; got != "main" {
		t.Errorf("target_branch = %q, want %q", got, "main")
	}
}

func TestAutoSpawnPatrol_RefinerySafetyStoppedSkipsWispCreate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock bd script uses POSIX shell")
	}
	townRoot := setupRefinerySafetyStopTown(t)
	logPath := installRefinerySafetyStopMockBD(t)

	_, err := autoSpawnPatrol(PatrolConfig{
		RoleName:      "refinery",
		PatrolMolName: constants.MolRefineryPatrol,
		BeadsDir:      townRoot,
		Assignee:      "testrig/refinery",
	})
	if !errors.Is(err, refinery.ErrSafetyStopped) {
		t.Fatalf("autoSpawnPatrol error = %v, want ErrSafetyStopped", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	if strings.Contains(string(logData), "mol wisp create") || strings.Contains(string(logData), "update ") {
		t.Fatalf("autoSpawnPatrol mutated patrol state despite safety stop; log:\n%s", logData)
	}
}

func setupRefinerySafetyStopTown(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()
	for _, dir := range []string{filepath.Join(townRoot, "mayor"), filepath.Join(townRoot, ".beads"), filepath.Join(townRoot, "testrig")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0o644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	return townRoot
}

func installRefinerySafetyStopMockBD(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done
case "$cmd" in
  version)
    echo "bd test"
    ;;
  show)
    printf '%s\n' '[{"id":"gt-testrig-refinery","title":"Refinery","issue_type":"task","labels":["gt:agent","safety_stop:hq-vmrwr"],"status":"open","description":"role_type: refinery\nrig: testrig\nagent_state: idle"}]'
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func TestBuildRefineryPatrolVars_NilMergeQueue(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write settings with no merge_queue
	settings := config.RigSettings{
		Type:    "rig-settings",
		Version: 1,
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)
	// target_branch should always be present (falls back to "main" without rig config)
	if len(vars) != 1 {
		t.Errorf("expected 1 var (target_branch) when merge_queue is nil, got %v", vars)
	}
	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}
	if got := varMap["target_branch"]; got != "main" {
		t.Errorf("target_branch = %q, want %q", got, "main")
	}
}

func TestBuildRefineryPatrolVars_FullConfig(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write rig config.json with default_branch (source of truth for default branch)
	rigConfig := map[string]interface{}{"type": "rig", "version": 1, "name": "testrig"}
	rigData, _ := json.Marshal(rigConfig)
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), rigData, 0o644); err != nil {
		t.Fatal(err)
	}

	mq := config.DefaultMergeQueueConfig()
	settings := config.RigSettings{
		Type:       "rig-settings",
		Version:    1,
		MergeQueue: mq,
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)

	// DefaultMergeQueueConfig: refinery_enabled=true, auto_land=false, run_tests=true,
	// test_command="" (language-agnostic), target_branch="main" (from rig config),
	// delete_merged_branches=true, judgment_enabled=false, review_depth="standard"
	// merge_strategy is omitted when not explicitly set (formula default "direct" applies)
	// New commands (setup, typecheck, lint, build) default to empty = omitted
	// judgment_enabled defaults to false, review_depth defaults to "standard"
	expected := map[string]string{
		"integration_branch_refinery_enabled": "true",
		"integration_branch_auto_land":        "false",
		"run_tests":                           "true",
		"target_branch":                       "main",
		"delete_merged_branches":              "true",
		"judgment_enabled":                    "false",
		"review_depth":                        "standard",
		"require_review":                      "false",
	}

	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}

	for key, want := range expected {
		got, ok := varMap[key]
		if !ok {
			t.Errorf("missing var %q", key)
			continue
		}
		if got != want {
			t.Errorf("var %q = %q, want %q", key, got, want)
		}
	}

	// Verify empty commands and unset strategy are NOT included
	for _, shouldBeAbsent := range []string{"setup_command", "typecheck_command", "lint_command", "build_command", "merge_strategy"} {
		if _, ok := varMap[shouldBeAbsent]; ok {
			t.Errorf("%q should be omitted when empty/unset", shouldBeAbsent)
		}
	}

	if len(vars) != len(expected) {
		t.Errorf("expected %d vars, got %d: %v", len(expected), len(vars), vars)
	}
}

func TestBuildRefineryPatrolVars_AllCommandsSet(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mq := config.DefaultMergeQueueConfig()
	mq.SetupCommand = "pnpm install"
	mq.TypecheckCommand = "tsc --noEmit"
	mq.LintCommand = "eslint ."
	mq.BuildCommand = "pnpm build"
	settings := config.RigSettings{
		Type:       "rig-settings",
		Version:    1,
		MergeQueue: mq,
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)

	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}

	// All configured commands should be present (test_command is empty by default)
	commandExpected := map[string]string{
		"setup_command":     "pnpm install",
		"typecheck_command": "tsc --noEmit",
		"lint_command":      "eslint .",
		"build_command":     "pnpm build",
	}
	for key, want := range commandExpected {
		got, ok := varMap[key]
		if !ok {
			t.Errorf("missing var %q", key)
			continue
		}
		if got != want {
			t.Errorf("var %q = %q, want %q", key, got, want)
		}
	}
}

func TestBuildRefineryPatrolVars_EmptyTestCommand(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	falseVal := false
	trueVal2 := true
	mq := &config.MergeQueueConfig{
		Enabled:              true,
		RunTests:             &falseVal,
		TestCommand:          "", // empty - should be omitted
		DeleteMergedBranches: &trueVal2,
	}
	settings := config.RigSettings{
		Type:       "rig-settings",
		Version:    1,
		MergeQueue: mq,
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)

	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}

	// test_command should not be present when empty
	if _, ok := varMap["test_command"]; ok {
		t.Error("test_command should be omitted when empty")
	}

	// All command vars should be omitted when empty
	for _, cmd := range []string{"setup_command", "typecheck_command", "lint_command", "build_command"} {
		if _, ok := varMap[cmd]; ok {
			t.Errorf("%q should be omitted when empty", cmd)
		}
	}

	// run_tests should be "false"
	if got := varMap["run_tests"]; got != "false" {
		t.Errorf("run_tests = %q, want %q", got, "false")
	}
}

func TestBuildRefineryPatrolVars_BoolFormat(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write rig config.json with default_branch = "develop"
	rigConfig := map[string]interface{}{"type": "rig", "version": 1, "name": "testrig", "default_branch": "develop"}
	rigData, _ := json.Marshal(rigConfig)
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), rigData, 0o644); err != nil {
		t.Fatal(err)
	}

	trueVal := true
	falseVal2 := false
	mq := &config.MergeQueueConfig{
		Enabled:                          true,
		IntegrationBranchAutoLand:        &trueVal,
		IntegrationBranchRefineryEnabled: &trueVal,
		RunTests:                         &trueVal,
		SetupCommand:                     "npm ci",
		TypecheckCommand:                 "tsc --noEmit",
		LintCommand:                      "eslint .",
		TestCommand:                      "make test",
		BuildCommand:                     "make build",
		DeleteMergedBranches:             &falseVal2,
	}
	settings := config.RigSettings{
		Type:       "rig-settings",
		Version:    1,
		MergeQueue: mq,
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)

	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}

	// Check bool format is "true"/"false" strings
	if got := varMap["integration_branch_auto_land"]; got != "true" {
		t.Errorf("integration_branch_auto_land = %q, want %q", got, "true")
	}
	if got := varMap["delete_merged_branches"]; got != "false" {
		t.Errorf("delete_merged_branches = %q, want %q", got, "false")
	}
	if got := varMap["target_branch"]; got != "develop" {
		t.Errorf("target_branch = %q, want %q", got, "develop")
	}
	if got := varMap["test_command"]; got != "make test" {
		t.Errorf("test_command = %q, want %q", got, "make test")
	}
	if got := varMap["setup_command"]; got != "npm ci" {
		t.Errorf("setup_command = %q, want %q", got, "npm ci")
	}
	if got := varMap["typecheck_command"]; got != "tsc --noEmit" {
		t.Errorf("typecheck_command = %q, want %q", got, "tsc --noEmit")
	}
	if got := varMap["lint_command"]; got != "eslint ." {
		t.Errorf("lint_command = %q, want %q", got, "eslint .")
	}
	if got := varMap["build_command"]; got != "make build" {
		t.Errorf("build_command = %q, want %q", got, "make build")
	}
}

func TestBuildRefineryPatrolVars_DefaultBranchWithoutMQ(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write rig config with custom default_branch but NO settings/config.json
	rigConfig := map[string]interface{}{
		"type": "rig", "version": 1, "name": "testrig",
		"default_branch": "gastown",
	}
	rigData, _ := json.Marshal(rigConfig)
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), rigData, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)

	// target_branch must be "gastown" even without merge_queue settings
	if len(vars) != 1 {
		t.Errorf("expected 1 var (target_branch), got %d: %v", len(vars), vars)
	}
	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}
	if got := varMap["target_branch"]; got != "gastown" {
		t.Errorf("target_branch = %q, want %q (should read rig config even without MQ settings)", got, "gastown")
	}
}

func TestBuildRefineryPatrolVars_MergeStrategy(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mq := config.DefaultMergeQueueConfig()
	mq.MergeStrategy = "pr"
	settings := config.RigSettings{
		Type:       "rig-settings",
		Version:    1,
		MergeQueue: mq,
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)

	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}

	if got := varMap["merge_strategy"]; got != "pr" {
		t.Errorf("merge_strategy = %q, want %q (rig-level config must override formula default)", got, "pr")
	}
}

func TestBuildRefineryPatrolVars_MergeStrategyDefaultOmitted(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// MergeStrategy not set — should not be injected (formula default "direct" applies)
	mq := config.DefaultMergeQueueConfig()
	settings := config.RigSettings{
		Type:       "rig-settings",
		Version:    1,
		MergeQueue: mq,
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)

	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}

	// merge_strategy should be absent when not explicitly configured
	if _, ok := varMap["merge_strategy"]; ok {
		t.Error("merge_strategy should be omitted when not configured (let formula default apply)")
	}
}

func TestBuildRefineryPatrolVars_RequireReview(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mq := config.DefaultMergeQueueConfig()
	mq.MergeStrategy = "pr"
	requireReview := true
	mq.RequireReview = &requireReview
	settings := config.RigSettings{
		Type:       "rig-settings",
		Version:    1,
		MergeQueue: mq,
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)

	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}

	if got := varMap["require_review"]; got != "true" {
		t.Errorf("require_review = %q, want %q", got, "true")
	}
	if got := varMap["merge_strategy"]; got != "pr" {
		t.Errorf("merge_strategy = %q, want %q", got, "pr")
	}
}

// splitFirstEquals splits a string on the first '=' only.
func splitFirstEquals(s string) []string {
	idx := -1
	for i, c := range s {
		if c == '=' {
			idx = i
			break
		}
	}
	if idx < 0 {
		return []string{s}
	}
	return []string{s[:idx], s[idx+1:]}
}

// --- Patrol discovery tests (findActivePatrol) ---

func requireBd(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd CLI not installed, skipping patrol test")
	}
}

func setupPatrolTestDB(t *testing.T) (string, *beads.Beads) {
	t.Helper()
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())
	tmpDir := t.TempDir()
	b := beads.NewIsolatedWithPort(tmpDir, port)
	// Use a unique prefix per test run to avoid cross-run contamination
	// in the shared Dolt database.
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	prefix := "pt" + hex.EncodeToString(buf[:])
	if err := b.Init(prefix); err != nil {
		t.Fatalf("bd init: %v", err)
	}

	// Clean up the test database after the test to avoid leaking
	// beads_pt* databases on the shared Dolt server.
	dbName := "beads_" + prefix
	t.Cleanup(func() {
		dsn := fmt.Sprintf("root:@tcp(127.0.0.1:%s)/", testutil.DoltContainerPort())
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			t.Logf("cleanup: failed to connect to dolt server to drop %s: %v", dbName, err)
			return
		}
		defer db.Close()
		if _, err := db.Exec("DROP DATABASE IF EXISTS `" + dbName + "`"); err != nil {
			t.Logf("cleanup: failed to drop %s: %v", dbName, err)
		}
		// Purge dropped databases to prevent accumulation on disk
		db.Exec("CALL dolt_purge_dropped_databases()") //nolint:errcheck
	})

	return tmpDir, b
}

// createHookedPatrol creates a bead with a patrol title and hooks it.
// If withOpenChild is true, creates an open child bead to simulate an active patrol.
func createHookedPatrol(t *testing.T, b *beads.Beads, molName, assignee string, withOpenChild bool) string {
	t.Helper()
	root, err := b.Create(beads.CreateOptions{
		Title:    molName + " (wisp)",
		Priority: -1,
	})
	if err != nil {
		t.Fatalf("create patrol root: %v", err)
	}

	hooked := beads.StatusHooked
	if err := b.Update(root.ID, beads.UpdateOptions{
		Status:   &hooked,
		Assignee: &assignee,
	}); err != nil {
		t.Fatalf("hook patrol: %v", err)
	}

	if withOpenChild {
		_, err := b.Create(beads.CreateOptions{
			Title:    "inbox-check",
			Parent:   root.ID,
			Priority: -1,
		})
		if err != nil {
			t.Fatalf("create child: %v", err)
		}
	}
	return root.ID
}

func TestFindActivePatrolHooked(t *testing.T) {
	requireBd(t)
	tmpDir, b := setupPatrolTestDB(t)

	molName := "mol-test-patrol"
	assignee := "testrig/witness"

	rootID := createHookedPatrol(t, b, molName, assignee, true /* withOpenChild */)

	cfg := PatrolConfig{
		PatrolMolName: molName,
		BeadsDir:      tmpDir,
		Assignee:      assignee,
		Beads:         b,
	}

	patrolID, _, found, findErr := findActivePatrol(cfg)
	if findErr != nil {
		t.Fatalf("findActivePatrol error: %v", findErr)
	}
	if !found {
		t.Fatal("expected to find active patrol, got not found")
	}
	if patrolID != rootID {
		t.Errorf("patrolID = %q, want %q", patrolID, rootID)
	}

	// Verify the patrol is still hooked (not closed)
	issue, err := b.Show(rootID)
	if err != nil {
		t.Fatalf("show patrol: %v", err)
	}
	if issue.Status != beads.StatusHooked {
		t.Errorf("patrol status = %q, want %q", issue.Status, beads.StatusHooked)
	}
}

func TestFindActivePatrolStale(t *testing.T) {
	requireBd(t)
	tmpDir, b := setupPatrolTestDB(t)

	molName := "mol-test-patrol"
	assignee := "testrig/witness"

	// Create a patrol with a closed child (simulates post-squash state)
	rootID := createHookedPatrol(t, b, molName, assignee, true /* with child */)

	// Close the child to make the patrol stale
	children, err := b.List(beads.ListOptions{Parent: rootID, Status: "all", Priority: -1})
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	for _, child := range children {
		if closeErr := b.ForceCloseWithReason("test cleanup", child.ID); closeErr != nil {
			t.Fatalf("close child: %v", closeErr)
		}
	}

	cfg := PatrolConfig{
		PatrolMolName: molName,
		BeadsDir:      tmpDir,
		Assignee:      assignee,
		Beads:         b,
	}

	_, _, found, findErr := findActivePatrol(cfg)
	if findErr != nil {
		t.Fatalf("findActivePatrol error: %v", findErr)
	}
	if found {
		t.Fatal("expected stale patrol (all children closed) to NOT be found as active")
	}

	// Verify the stale patrol was closed
	issue, err := b.Show(rootID)
	if err != nil {
		t.Fatalf("show patrol: %v", err)
	}
	if issue.Status != "closed" {
		t.Errorf("stale patrol status = %q, want %q", issue.Status, "closed")
	}
}

func TestFindActivePatrolZeroChildren(t *testing.T) {
	requireBd(t)
	tmpDir, b := setupPatrolTestDB(t)

	molName := "mol-test-patrol"
	assignee := "testrig/witness"

	// Create a patrol with NO children — simulates a freshly created wisp
	// whose steps haven't materialized yet. Should be treated as active,
	// not stale, to prevent race condition.
	rootID := createHookedPatrol(t, b, molName, assignee, false /* no children */)

	cfg := PatrolConfig{
		PatrolMolName: molName,
		BeadsDir:      tmpDir,
		Assignee:      assignee,
		Beads:         b,
	}

	patrolID, _, found, findErr := findActivePatrol(cfg)
	if findErr != nil {
		t.Fatalf("findActivePatrol error: %v", findErr)
	}
	if !found {
		t.Fatal("expected zero-children patrol to be treated as active (not stale)")
	}
	if patrolID != rootID {
		t.Errorf("patrolID = %q, want %q", patrolID, rootID)
	}

	// Verify it was NOT closed
	issue, err := b.Show(rootID)
	if err != nil {
		t.Fatalf("show patrol: %v", err)
	}
	if issue.Status != beads.StatusHooked {
		t.Errorf("zero-children patrol status = %q, want %q (should remain hooked)", issue.Status, beads.StatusHooked)
	}
}

func TestFindActivePatrolMultiple(t *testing.T) {
	requireBd(t)
	tmpDir, b := setupPatrolTestDB(t)

	molName := "mol-test-patrol"
	assignee := "testrig/witness"

	// Create 2 stale patrols (with closed children) and 1 active patrol (with open child)
	stale1 := createHookedPatrol(t, b, molName, assignee, true)
	stale2 := createHookedPatrol(t, b, molName, assignee, true)
	activeID := createHookedPatrol(t, b, molName, assignee, true)

	// Close children of stale patrols to make them stale
	for _, staleID := range []string{stale1, stale2} {
		children, err := b.List(beads.ListOptions{Parent: staleID, Status: "all", Priority: -1})
		if err != nil {
			t.Fatalf("list children of %s: %v", staleID, err)
		}
		for _, child := range children {
			if closeErr := b.ForceCloseWithReason("test cleanup", child.ID); closeErr != nil {
				t.Fatalf("close child: %v", closeErr)
			}
		}
	}

	cfg := PatrolConfig{
		PatrolMolName: molName,
		BeadsDir:      tmpDir,
		Assignee:      assignee,
		Beads:         b,
	}

	patrolID, _, found, findErr := findActivePatrol(cfg)
	if findErr != nil {
		t.Fatalf("findActivePatrol error: %v", findErr)
	}
	if !found {
		t.Fatal("expected to find active patrol")
	}
	if patrolID != activeID {
		t.Errorf("patrolID = %q, want %q (should return the active one)", patrolID, activeID)
	}

	// Verify active patrol is still hooked
	issue, err := b.Show(activeID)
	if err != nil {
		t.Fatalf("show active: %v", err)
	}
	if issue.Status != beads.StatusHooked {
		t.Errorf("active patrol status = %q, want %q", issue.Status, beads.StatusHooked)
	}

	// Stale patrol cleanup is not guaranteed when an active patrol is found —
	// findActivePatrol breaks early on active discovery to prevent N+1 Dolt queries
	// (gt-18dzn6p). Remaining stale beads are cleaned by burnPreviousPatrolWisps
	// when the patrol cycle ends. Verify stale beads are either closed or still hooked
	// (not left in an intermediate broken state).
	for _, id := range []string{stale1, stale2} {
		staleIssue, showErr := b.Show(id)
		if showErr != nil {
			t.Fatalf("show stale %s: %v", id, showErr)
		}
		if staleIssue.Status != "closed" && staleIssue.Status != beads.StatusHooked {
			t.Errorf("stale patrol %s status = %q, want closed or hooked", id, staleIssue.Status)
		}
	}
}

// TestFindActivePatrol_StaleCleanupCapped verifies that when many stale patrols
// accumulate with no active patrol, cleanup is capped at maxStalePurgePerRun per call
// to prevent overwhelming Dolt with sequential write queries (gt-18dzn6p).
func TestFindActivePatrol_StaleCleanupCapped(t *testing.T) {
	requireBd(t)
	tmpDir, b := setupPatrolTestDB(t)

	molName := "mol-test-patrol"
	assignee := "testrig/witness"

	// Create more stale patrols than maxStalePurgePerRun (currently 5)
	numStale := maxStalePurgePerRun + 3 // e.g., 8 total
	staleIDs := make([]string, numStale)
	for i := 0; i < numStale; i++ {
		id := createHookedPatrol(t, b, molName, assignee, true /* with child */)
		staleIDs[i] = id

		// Close the child to make the patrol stale
		children, err := b.List(beads.ListOptions{Parent: id, Status: "all", Priority: -1})
		if err != nil {
			t.Fatalf("list children of %s: %v", id, err)
		}
		for _, child := range children {
			if closeErr := b.ForceCloseWithReason("test cleanup", child.ID); closeErr != nil {
				t.Fatalf("close child of %s: %v", id, closeErr)
			}
		}
	}

	cfg := PatrolConfig{
		PatrolMolName: molName,
		BeadsDir:      tmpDir,
		Assignee:      assignee,
		Beads:         b,
	}

	_, _, found, findErr := findActivePatrol(cfg)
	if findErr != nil {
		t.Fatalf("findActivePatrol error: %v", findErr)
	}
	if found {
		t.Fatal("expected no active patrol (all stale)")
	}

	// Count how many stale patrols were actually closed
	closedCount := 0
	hookedCount := 0
	for _, id := range staleIDs {
		issue, err := b.Show(id)
		if err != nil {
			t.Fatalf("show stale %s: %v", id, err)
		}
		switch issue.Status {
		case "closed":
			closedCount++
		case beads.StatusHooked:
			hookedCount++
		default:
			t.Errorf("stale patrol %s unexpected status %q", id, issue.Status)
		}
	}

	// Cleanup must be capped: at most maxStalePurgePerRun beads closed per run
	if closedCount > maxStalePurgePerRun {
		t.Errorf("closed %d stale patrols, want at most %d (cap exceeded — Dolt DoS risk)",
			closedCount, maxStalePurgePerRun)
	}
	// But at least some cleanup must happen (the cap should not be zero)
	if closedCount == 0 {
		t.Errorf("no stale patrols were closed, expected up to %d", maxStalePurgePerRun)
	}
	// Total accounted for
	if closedCount+hookedCount != numStale {
		t.Errorf("closed=%d + hooked=%d != total=%d", closedCount, hookedCount, numStale)
	}
}

func TestBurnPreviousPatrolWisps(t *testing.T) {
	requireBd(t)
	tmpDir, b := setupPatrolTestDB(t)

	molName := "mol-test-patrol"
	assignee := "testrig/witness"

	// Create 3 hooked patrol wisps (simulating accumulated orphans)
	id1 := createHookedPatrol(t, b, molName, assignee, true)
	id2 := createHookedPatrol(t, b, molName, assignee, false)
	id3 := createHookedPatrol(t, b, molName, assignee, true)

	cfg := PatrolConfig{
		PatrolMolName: molName,
		BeadsDir:      tmpDir,
		Assignee:      assignee,
		Beads:         b,
	}

	burnPreviousPatrolWisps(cfg)

	// All 3 patrols should now be closed
	for _, id := range []string{id1, id2, id3} {
		issue, err := b.Show(id)
		if err != nil {
			t.Fatalf("show %s: %v", id, err)
		}
		if issue.Status != "closed" {
			t.Errorf("patrol %s status = %q, want %q after burn", id, issue.Status, "closed")
		}
	}
}

func TestBurnPreviousPatrolWisps_IgnoresOtherBeads(t *testing.T) {
	requireBd(t)
	tmpDir, b := setupPatrolTestDB(t)

	molName := "mol-test-patrol"
	assignee := "testrig/witness"

	// Create a patrol wisp (should be burned)
	patrolID := createHookedPatrol(t, b, molName, assignee, true)

	// Create a non-patrol hooked bead (should NOT be burned)
	other, err := b.Create(beads.CreateOptions{
		Title:    "some-other-work",
		Priority: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	hooked := beads.StatusHooked
	if err := b.Update(other.ID, beads.UpdateOptions{
		Status:   &hooked,
		Assignee: &assignee,
	}); err != nil {
		t.Fatal(err)
	}

	cfg := PatrolConfig{
		PatrolMolName: molName,
		BeadsDir:      tmpDir,
		Assignee:      assignee,
		Beads:         b,
	}

	burnPreviousPatrolWisps(cfg)

	// Patrol should be closed
	issue, err := b.Show(patrolID)
	if err != nil {
		t.Fatalf("show patrol: %v", err)
	}
	if issue.Status != "closed" {
		t.Errorf("patrol status = %q, want %q", issue.Status, "closed")
	}

	// Non-patrol bead should still be hooked
	otherIssue, err := b.Show(other.ID)
	if err != nil {
		t.Fatalf("show other: %v", err)
	}
	if otherIssue.Status != beads.StatusHooked {
		t.Errorf("non-patrol bead status = %q, want %q (should not be burned)", otherIssue.Status, beads.StatusHooked)
	}
}
