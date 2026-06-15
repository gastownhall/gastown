package beads

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestNew verifies the constructor.
func TestNew(t *testing.T) {
	b := New("/some/path")
	if b == nil {
		t.Fatal("New returned nil")
	}
	if b.workDir != "/some/path" {
		t.Errorf("workDir = %q, want /some/path", b.workDir)
	}
}

// TestListOptions verifies ListOptions defaults.
func TestListOptions(t *testing.T) {
	opts := ListOptions{
		Status:   "open",
		Label:    "gt:task",
		Priority: 1,
	}
	if opts.Status != "open" {
		t.Errorf("Status = %q, want open", opts.Status)
	}
}

// TestListOptionsEphemeral verifies that Ephemeral flag is preserved.
func TestListOptionsEphemeral(t *testing.T) {
	opts := ListOptions{
		Label:     "gt:merge-request",
		Status:    "open",
		Priority:  -1,
		Ephemeral: true,
	}
	if !opts.Ephemeral {
		t.Error("Ephemeral should be true")
	}
}

// TestCreateOptions verifies CreateOptions fields.
func TestCreateOptions(t *testing.T) {
	opts := CreateOptions{
		Title:       "Test issue",
		Labels:      []string{"gt:task"},
		Priority:    2,
		Description: "A test description",
		Parent:      "gt-abc",
	}
	if opts.Title != "Test issue" {
		t.Errorf("Title = %q, want 'Test issue'", opts.Title)
	}
	if opts.Parent != "gt-abc" {
		t.Errorf("Parent = %q, want gt-abc", opts.Parent)
	}
}

// TestCreateOptionsRig verifies the Rig field targets the correct rig database (gt-7y7).
// When a polecat works on a cross-rig bead (e.g., hq-xxx), gt done must explicitly
// set Rig on CreateOptions so the MR bead lands in the polecat's rig database,
// not the town-level database where the source bead lives.
func TestCreateOptionsRig(t *testing.T) {
	opts := CreateOptions{
		Title:     "Merge: hq-abc",
		Labels:    []string{"gt:merge-request"},
		Ephemeral: true,
		Rig:       "gastown",
	}
	if opts.Rig != "gastown" {
		t.Errorf("Rig = %q, want %q", opts.Rig, "gastown")
	}

	// Zero value: Rig is empty string (no --repo flag passed).
	var empty CreateOptions
	if empty.Rig != "" {
		t.Errorf("zero-value Rig = %q, want empty string", empty.Rig)
	}
}

func TestBuildPinnedBDEnvUsesSelectedConnectionMetadata(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"dolt_database":"rigdb","dolt_server_host":"127.0.0.1","dolt_server_port":4407}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadata, 0644); err != nil {
		t.Fatal(err)
	}

	env := BuildPinnedBDEnv([]string{
		"PATH=/usr/bin",
		"BEADS_DIR=/wrong",
		"BEADS_DB=/wrong.db",
		"BD_DB=/wrong.bd",
		"BEADS_DOLT_DATABASE=legacy-hq",
		"BEADS_DOLT_SERVER_DATABASE=hq",
		"BEADS_DOLT_SERVER_HOST=wrong-host",
		"BEADS_DOLT_SERVER_PORT=9999",
		"BEADS_DOLT_PORT=9999",
		"BEADS_DOLT_DATA_DIR=/wrong/data",
		"BEADS_DOLT_FUTURE_SELECTOR=wrong",
		"BEADS_DOLT_AUTO_START=0",
	}, beadsDir)
	got := envMap(env)

	if got["BEADS_DIR"] != beadsDir {
		t.Fatalf("BEADS_DIR = %q, want %q in %v", got["BEADS_DIR"], beadsDir, env)
	}
	if got["BEADS_DOLT_SERVER_DATABASE"] != "rigdb" {
		t.Fatalf("BEADS_DOLT_SERVER_DATABASE = %q, want rigdb in %v", got["BEADS_DOLT_SERVER_DATABASE"], env)
	}
	if count := countEnvPrefix(env, "BEADS_DOLT_SERVER_DATABASE="); count != 1 {
		t.Fatalf("BEADS_DOLT_SERVER_DATABASE count = %d, want 1 in %v", count, env)
	}
	if got["BEADS_DOLT_SERVER_HOST"] != "127.0.0.1" {
		t.Fatalf("BEADS_DOLT_SERVER_HOST = %q, want 127.0.0.1 in %v", got["BEADS_DOLT_SERVER_HOST"], env)
	}
	if got["BEADS_DOLT_SERVER_PORT"] != "4407" || got["BEADS_DOLT_PORT"] != "4407" {
		t.Fatalf("ports = server:%q legacy:%q, want 4407 in %v", got["BEADS_DOLT_SERVER_PORT"], got["BEADS_DOLT_PORT"], env)
	}
	for _, key := range []string{"BEADS_DB", "BD_DB", "BEADS_DOLT_DATABASE", "BEADS_DOLT_DATA_DIR", "BEADS_DOLT_FUTURE_SELECTOR"} {
		if value, ok := got[key]; ok {
			t.Fatalf("%s should be stripped, got %q in %v", key, value, env)
		}
	}
	if got["BEADS_DOLT_AUTO_START"] != "0" {
		t.Fatalf("BEADS_DOLT_AUTO_START should be preserved, got %q in %v", got["BEADS_DOLT_AUTO_START"], env)
	}
}

func TestBuildPinnedBDEnvStripsCaseVariantTargetEnvWhenKeysAreCaseInsensitive(t *testing.T) {
	withCaseInsensitiveEnvKeys(t)

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"dolt_database":"rigdb","dolt_server_host":"127.0.0.1","dolt_server_port":4407}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadata, 0644); err != nil {
		t.Fatal(err)
	}

	env := BuildPinnedBDEnv([]string{
		"PATH=/usr/bin",
		"beads_dir=/wrong",
		"beads_db=/wrong.db",
		"bd_db=/wrong.bd",
		"beads_dolt_server_database=hq",
		"beads_dolt_server_host=wrong-host",
		"beads_dolt_server_port=9999",
		"beads_dolt_port=9999",
		"beads_dolt_data_dir=/wrong/data",
		"beads_dolt_auto_start=0",
		"bd_export_auto=true",
	}, beadsDir)
	got := envMap(env)

	for _, key := range []string{
		"beads_dir",
		"beads_db",
		"bd_db",
		"beads_dolt_server_database",
		"beads_dolt_server_host",
		"beads_dolt_server_port",
		"beads_dolt_port",
		"beads_dolt_data_dir",
		"bd_export_auto",
	} {
		if value, ok := got[key]; ok {
			t.Fatalf("case-variant %s should be stripped, got %q in %v", key, value, env)
		}
	}
	if got["BEADS_DIR"] != beadsDir || got["BEADS_DOLT_SERVER_DATABASE"] != "rigdb" {
		t.Fatalf("pinned target env not restored canonically: %v", env)
	}
	if got["BEADS_DOLT_SERVER_HOST"] != "127.0.0.1" || got["BEADS_DOLT_SERVER_PORT"] != "4407" || got["BEADS_DOLT_PORT"] != "4407" {
		t.Fatalf("connection env not restored canonically: %v", env)
	}
	if got["beads_dolt_auto_start"] != "0" {
		t.Fatalf("case-variant BEADS_DOLT_AUTO_START should be preserved, got %v", env)
	}
	if got["BD_EXPORT_AUTO"] != "false" {
		t.Fatalf("BD_EXPORT_AUTO = %q, want false in %v", got["BD_EXPORT_AUTO"], env)
	}
}

func TestBuildRoutingBDEnvStripsDatabaseButKeepsSelectedConnection(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"dolt_database":"rigdb","dolt_server_host":"127.0.0.1","dolt_server_port":4407}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadata, 0644); err != nil {
		t.Fatal(err)
	}

	env := BuildRoutingBDEnv([]string{
		"PATH=/usr/bin",
		"BEADS_DIR=/wrong",
		"BEADS_DOLT_SERVER_DATABASE=hq",
		"BEADS_DOLT_SERVER_HOST=wrong-host",
		"BEADS_DOLT_SERVER_PORT=9999",
		"BEADS_DOLT_PORT=9999",
	}, beadsDir)
	got := envMap(env)
	for _, key := range []string{"BEADS_DIR", "BEADS_DOLT_SERVER_DATABASE"} {
		if value, ok := got[key]; ok {
			t.Fatalf("%s should be absent for routed env, got %q in %v", key, value, env)
		}
	}
	if got["BEADS_DOLT_SERVER_HOST"] != "127.0.0.1" || got["BEADS_DOLT_SERVER_PORT"] != "4407" || got["BEADS_DOLT_PORT"] != "4407" {
		t.Fatalf("routing env did not use selected connection, got %v", env)
	}
}

func TestBuildPinnedBDEnvFallsBackToGTDoltPort(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"dolt_database":"rigdb"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadata, 0644); err != nil {
		t.Fatal(err)
	}

	env := BuildPinnedBDEnv([]string{
		"PATH=/usr/bin",
		"GT_DOLT_HOST=127.0.0.2",
		"GT_DOLT_PORT=5507",
	}, beadsDir)
	got := envMap(env)
	if got["BEADS_DOLT_SERVER_DATABASE"] != "rigdb" {
		t.Fatalf("BEADS_DOLT_SERVER_DATABASE = %q, want rigdb in %v", got["BEADS_DOLT_SERVER_DATABASE"], env)
	}
	if got["BEADS_DOLT_SERVER_HOST"] != "127.0.0.2" {
		t.Fatalf("BEADS_DOLT_SERVER_HOST = %q, want GT_DOLT_HOST fallback in %v", got["BEADS_DOLT_SERVER_HOST"], env)
	}
	if got["BEADS_DOLT_SERVER_PORT"] != "5507" || got["BEADS_DOLT_PORT"] != "5507" {
		t.Fatalf("ports = server:%q legacy:%q, want 5507 in %v", got["BEADS_DOLT_SERVER_PORT"], got["BEADS_DOLT_PORT"], env)
	}
}

func TestSuppressBDSideEffectsOverridesInherited(t *testing.T) {
	env := SuppressBDSideEffects([]string{
		"PATH=/usr/bin",
		"BD_EXPORT_AUTO=true",
		"BD_BACKUP_ENABLED=true",
		"BD_DOLT_AUTO_PUSH=true",
		"BD_NO_PUSH=false",
		"BD_EXPORT_GIT_ADD=true",
		"BD_NO_GIT_OPS=false",
	})
	got := envMap(env)
	for key, want := range map[string]string{
		"BEADS_NO_AUTO_IMPORT": "1",
		"BD_EXPORT_AUTO":       "false",
		"BD_BACKUP_ENABLED":    "false",
		"BD_DOLT_AUTO_PUSH":    "false",
		"BD_NO_PUSH":           "true",
		"BD_EXPORT_GIT_ADD":    "false",
		"BD_NO_GIT_OPS":        "true",
	} {
		if got[key] != want {
			t.Fatalf("%s = %q, want %q in %v", key, got[key], want, env)
		}
	}
}

func TestBuildReadOnlyBDEnvForcesReadOnly(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"dolt_database":"hq","dolt_server_host":"127.0.0.1","dolt_server_port":3307}`), 0644); err != nil {
		t.Fatal(err)
	}

	env := BuildReadOnlyRoutingBDEnv([]string{
		"PATH=/usr/bin",
		"BD_DOLT_AUTO_COMMIT=on",
		"BD_READONLY=false",
		"BEADS_DOLT_SERVER_DATABASE=wrong",
	}, beadsDir)
	got := envMap(env)

	if got["BD_DOLT_AUTO_COMMIT"] != "off" {
		t.Fatalf("BD_DOLT_AUTO_COMMIT = %q, want off in %v", got["BD_DOLT_AUTO_COMMIT"], env)
	}
	if got["BD_READONLY"] != "true" {
		t.Fatalf("BD_READONLY = %q, want true in %v", got["BD_READONLY"], env)
	}
	if _, ok := got["BEADS_DOLT_SERVER_DATABASE"]; ok {
		t.Fatalf("routing read-only env should not pin database: %v", env)
	}
}

func TestBuildMutationBDEnvForcesWritableCommit(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"dolt_database":"hq","dolt_server_host":"127.0.0.1","dolt_server_port":3307}`), 0644); err != nil {
		t.Fatal(err)
	}

	env := BuildMutationPinnedBDEnv([]string{
		"PATH=/usr/bin",
		"BD_DOLT_AUTO_COMMIT=off",
		"BD_READONLY=true",
		"BEADS_DIR=/wrong",
	}, beadsDir)
	got := envMap(env)

	if got["BEADS_DIR"] != beadsDir {
		t.Fatalf("BEADS_DIR = %q, want %q in %v", got["BEADS_DIR"], beadsDir, env)
	}
	if got["BEADS_DOLT_SERVER_DATABASE"] != "hq" {
		t.Fatalf("BEADS_DOLT_SERVER_DATABASE = %q, want hq in %v", got["BEADS_DOLT_SERVER_DATABASE"], env)
	}
	if got["BD_DOLT_AUTO_COMMIT"] != "on" {
		t.Fatalf("BD_DOLT_AUTO_COMMIT = %q, want on in %v", got["BD_DOLT_AUTO_COMMIT"], env)
	}
	if _, ok := got["BD_READONLY"]; ok {
		t.Fatalf("BD_READONLY should be absent for mutation env, got %q in %v", got["BD_READONLY"], env)
	}
}

func TestArgsAreReadOnlyClassifiesKnownReadCommands(t *testing.T) {
	cases := [][]string{
		{"show", "gt-123", "--json"},
		{"--allow-stale", "show", "gt-123", "--json"},
		{"query", "merge-request", "--json"},
		{"dep", "list", "hq-cv-123", "--json"},
		{"mol", "wisp", "list", "--json"},
		{"sql", "SELECT 1"},
		{"sql", "--csv", "SELECT 1"},
		{"config", "get", "issue_prefix"},
	}
	for _, args := range cases {
		if !ArgsAreReadOnly(args) {
			t.Fatalf("ArgsAreReadOnly(%v) = false, want true", args)
		}
		if got := SubprocessModeForArgs(args); got != ReadOnlyRouting {
			t.Fatalf("SubprocessModeForArgs(%v) = %v, want ReadOnlyRouting", args, got)
		}
	}
}

func TestArgsAreReadOnlyFailsClosedForMutations(t *testing.T) {
	cases := [][]string{
		{"update", "gt-123", "--status=open"},
		{"close", "gt-123"},
		{"mol", "wisp", "formula"},
		{"sql", "UPDATE issues SET status='open'"},
		{"config", "set", "issue_prefix", "gt"},
	}
	for _, args := range cases {
		if ArgsAreReadOnly(args) {
			t.Fatalf("ArgsAreReadOnly(%v) = true, want false", args)
		}
		if got := SubprocessModeForArgs(args); got != MutationRouting {
			t.Fatalf("SubprocessModeForArgs(%v) = %v, want MutationRouting", args, got)
		}
	}
}

func envMap(env []string) map[string]string {
	out := make(map[string]string)
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			out[parts[0]] = parts[1]
		}
	}
	return out
}

func withCaseInsensitiveEnvKeys(t *testing.T) {
	t.Helper()
	old := envKeysCaseInsensitive
	envKeysCaseInsensitive = true
	t.Cleanup(func() { envKeysCaseInsensitive = old })
}

// TestCreateRoutesSameDatabaseViaBEADSDIR verifies that when opts.Rig resolves
// to a .beads dir that exists, Create routes via BEADS_DIR (not --repo). (hq-1uf2)
//
// --repo with an absolute path triggers a second database connection while
// BEADS_DIR already holds one, causing a pthread_cond_wait deadlock when both
// paths resolve to the same database (polecat running gt done on its own rig).
func TestCreateRoutesSameDatabaseViaBEADSDIR(t *testing.T) {
	// Build a minimal town layout with a single rig.
	townRoot := t.TempDir()

	// mayor/town.json so FindTownRoot works
	majorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(majorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(majorDir, "town.json"), []byte(`{"name":"test"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}

	// town-level .beads with routes
	townBeadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir town .beads: %v", err)
	}
	routes := []Route{
		{Prefix: "hq-", Path: "."},
		{Prefix: "tr-", Path: "testrig"},
	}
	if err := WriteRoutes(townBeadsDir, routes); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	// testrig directory with its own .beads
	rigDir := filepath.Join(townRoot, "testrig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}

	// polecat worktree inside the testrig, redirected to rig .beads
	polecatDir := filepath.Join(rigDir, "polecats", "quartz")
	polecatBeadsDir := filepath.Join(polecatDir, ".beads")
	if err := os.MkdirAll(polecatBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir polecat .beads: %v", err)
	}
	// redirect points to the rig's .beads (simulating the real worktree layout)
	if err := os.WriteFile(filepath.Join(polecatBeadsDir, "redirect"), []byte("../../.beads"), 0644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	// Create a fake bd stub that captures the BEADS_DIR env var and args.
	// It must output valid JSON for the issue and NOT block.
	stubDir := t.TempDir()
	stubScript := `#!/bin/sh
# Capture args to a file for assertion
echo "$@" >> "` + filepath.Join(stubDir, "args.txt") + `"
echo '{"id":"tr-test1","title":"test","status":"open","priority":2,"type":"task","labels":[]}'
exit 0
`
	stubPath := filepath.Join(stubDir, "bd")
	if err := os.WriteFile(stubPath, []byte(stubScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+origPath)

	// Beads instance rooted at the polecat dir (same as gt done sets up).
	b := New(polecatDir)

	// Force town root detection (normally lazy from workDir walk)
	// by having mayor/town.json in townRoot above polecatDir.
	// The town root search walks up from polecatDir.
	// polecatDir is inside townRoot so the walk will find it.

	// Create with Rig="testrig" — same rig as the polecat's own rig.
	// Old code: appended --repo=<rigDir> → would open same DB twice → hang.
	// New code: routes via BEADS_DIR to rigBeadsDir → no --repo → no hang.
	_ = b.getTownRoot() // prime the lazy cache

	_, _ = b.Create(CreateOptions{
		Title: "Merge: hq-abc",
		Rig:   "testrig",
	})

	// Assert: args written by the stub must NOT contain --repo
	argsData, err := os.ReadFile(filepath.Join(stubDir, "args.txt"))
	if err != nil {
		t.Fatalf("reading stub args: %v", err)
	}
	argsStr := string(argsData)
	if strings.Contains(argsStr, "--repo") {
		t.Errorf("Create with Rig should not pass --repo to bd, got args: %q", argsStr)
	}

	// BEADS_DIR in the environment passed to bd should point to the rig's .beads dir.
	// The stub doesn't capture env, but we can verify by checking that rigBeadsDir exists
	// (which it does) — the routing logic path was exercised.
	if _, err := os.Stat(rigBeadsDir); err != nil {
		t.Errorf("rig .beads dir should exist: %v", err)
	}
}

func TestCreateRoutesToResolvedRigBeadsDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock for bd")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}

	townBeadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir town .beads: %v", err)
	}
	if err := WriteRoutes(townBeadsDir, []Route{
		{Prefix: "hq-", Path: "."},
		{Prefix: "tr-", Path: "testrig"},
	}); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	rigDir := filepath.Join(townRoot, "testrig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	canonicalRigBeadsDir := filepath.Join(rigDir, "mayor", "rig", ".beads")
	for _, dir := range []string{rigBeadsDir, canonicalRigBeadsDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "redirect"), []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}

	stubDir := t.TempDir()
	logPath := filepath.Join(stubDir, "bd.log")
	stubScript := `#!/bin/sh
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done
if [ "$cmd" = "create" ]; then
  printf 'beads_dir=%s\n' "$BEADS_DIR" >> "$MOCK_BD_LOG"
  printf 'args=%s\n' "$*" >> "$MOCK_BD_LOG"
  printf '{"id":"tr-test1","title":"test","status":"open","priority":2,"labels":[]}\n'
fi
exit 0
`
	stubPath := filepath.Join(stubDir, "bd")
	if err := os.WriteFile(stubPath, []byte(stubScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MOCK_BD_LOG", logPath)

	workerDir := filepath.Join(rigDir, "polecats", "quartz")
	if err := os.MkdirAll(workerDir, 0755); err != nil {
		t.Fatalf("mkdir worker: %v", err)
	}

	for _, tc := range []struct {
		name   string
		opts   CreateOptions
		withID bool
	}{
		{
			name: "explicit rig",
			opts: CreateOptions{Title: "Merge: hq-abc", Rig: "testrig", Ephemeral: true},
		},
		{
			name: "parent prefix",
			opts: CreateOptions{Title: "Child task", Parent: "tr-parent"},
		},
		{
			name:   "deterministic id explicit rig",
			opts:   CreateOptions{Title: "Fixed merge", Rig: "testrig"},
			withID: true,
		},
		{
			name:   "deterministic id parent prefix",
			opts:   CreateOptions{Title: "Fixed child", Parent: "tr-parent"},
			withID: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.Remove(logPath); err != nil && !os.IsNotExist(err) {
				t.Fatalf("remove log: %v", err)
			}

			bd := New(workerDir)
			if tc.withID {
				if _, err := bd.CreateWithID("tr-fixed", tc.opts); err != nil {
					t.Fatalf("CreateWithID: %v", err)
				}
			} else if _, err := bd.Create(tc.opts); err != nil {
				t.Fatalf("Create: %v", err)
			}

			logData, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read mock log: %v", err)
			}
			logOutput := string(logData)
			if !strings.Contains(logOutput, "beads_dir="+canonicalRigBeadsDir) {
				t.Fatalf("Create did not route to canonical rig beads dir %q:\n%s", canonicalRigBeadsDir, logOutput)
			}
			if strings.Contains(logOutput, "beads_dir="+rigBeadsDir+"\n") {
				t.Fatalf("Create used intermediate redirect beads dir %q:\n%s", rigBeadsDir, logOutput)
			}
			if tc.withID && !strings.Contains(logOutput, "--id=tr-fixed") {
				t.Fatalf("CreateWithID did not pass deterministic id:\n%s", logOutput)
			}
		})
	}
}

func TestCreateWithUnknownRigErrors(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	townBeadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir town .beads: %v", err)
	}
	if err := WriteRoutes(townBeadsDir, []Route{{Prefix: "hq-", Path: "."}}); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	workerDir := filepath.Join(townRoot, "gastown", "polecats", "rust")
	if err := os.MkdirAll(workerDir, 0755); err != nil {
		t.Fatalf("mkdir worker: %v", err)
	}

	_, err := New(workerDir).Create(CreateOptions{Title: "bad rig", Rig: "missing"})
	if err == nil || !strings.Contains(err.Error(), `unknown repo/rig alias "missing"`) {
		t.Fatalf("Create error = %v, want unknown rig alias", err)
	}
}

// TestIsFlagLikeTitle verifies flag-like title detection (gt-e0kx5).
func TestIsFlagLikeTitle(t *testing.T) {
	tests := []struct {
		title string
		want  bool
	}{
		// Flag-like (should be rejected)
		{"--help", true},
		{"--json", true},
		{"--verbose", true},
		{"-h", true},
		{"-v", true},
		{"--dry-run", true},
		{"--type=task", true},

		// Normal titles (should be allowed)
		{"Fix bug in parser", false},
		{"Add --help flag handling", false},
		{"Fix --help flag parsing", false},
		{"", false},
		{"hello", false},
		{"- list item", false}, // single dash with space is fine (markdown)
	}

	for _, tt := range tests {
		got := IsFlagLikeTitle(tt.title)
		if got != tt.want {
			t.Errorf("IsFlagLikeTitle(%q) = %v, want %v", tt.title, got, tt.want)
		}
	}
}

func TestBdSupportsAllowStale_ReprobesWhenBinaryPathChanges(t *testing.T) {
	bdAllowStaleMu.Lock()
	prevPath := bdAllowStalePath
	prevResult := bdAllowStaleResult
	bdAllowStaleMu.Unlock()
	ResetBdAllowStaleCacheForTest()
	t.Cleanup(func() {
		bdAllowStaleMu.Lock()
		bdAllowStalePath = prevPath
		bdAllowStaleResult = prevResult
		bdAllowStaleMu.Unlock()
	})

	supportingDir := t.TempDir()
	nonSupportingDir := t.TempDir()
	writeAllowStaleBDStub(t, supportingDir, true)
	writeAllowStaleBDStub(t, nonSupportingDir, false)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", supportingDir+string(os.PathListSeparator)+origPath)
	if !BdSupportsAllowStale() {
		t.Fatal("expected first stub to support --allow-stale")
	}

	t.Setenv("PATH", nonSupportingDir+string(os.PathListSeparator)+origPath)
	if BdSupportsAllowStale() {
		t.Fatal("expected second stub to be re-probed and report no --allow-stale support")
	}
}

func TestBdSupportsAllowStale_TimeoutTreatsProbeAsUnsupported(t *testing.T) {
	bdAllowStaleMu.Lock()
	prevPath := bdAllowStalePath
	prevResult := bdAllowStaleResult
	bdAllowStaleMu.Unlock()
	prevTimeout := bdAllowStaleProbeTimeout
	ResetBdAllowStaleCacheForTest()
	bdAllowStaleProbeTimeout = 100 * time.Millisecond
	t.Cleanup(func() {
		bdAllowStaleMu.Lock()
		bdAllowStalePath = prevPath
		bdAllowStaleResult = prevResult
		bdAllowStaleMu.Unlock()
		bdAllowStaleProbeTimeout = prevTimeout
	})

	hangingDir := t.TempDir()
	markerPath := filepath.Join(hangingDir, "allow-stale-timeout-marker")
	writeHangingAllowStaleBDStub(t, hangingDir, markerPath)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", hangingDir+string(os.PathListSeparator)+origPath)

	start := time.Now()
	if BdSupportsAllowStale() {
		t.Fatal("expected hanging probe to time out and report no --allow-stale support")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("expected probe timeout to return promptly, took %v", elapsed)
	}

	if runtime.GOOS != "windows" {
		time.Sleep(250 * time.Millisecond)
		if _, err := os.Stat(markerPath); err == nil {
			t.Fatal("expected timed-out probe to kill the entire process group")
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat timeout marker: %v", err)
		}
	}
}

func TestBDListSlowListDoesNotBlockUnrelatedList(t *testing.T) {
	if os.Getenv("GT_BEADS_LIST_CONCURRENCY_HELPER") == "1" {
		runBDListConcurrencyHelper(t)
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("process-level flock regression test is Unix-specific")
	}

	tmp := t.TempDir()
	if outer := FindTownRoot(tmp); outer != "" {
		t.Skipf("temp dir is nested under existing town root %s", outer)
	}
	townRoot := filepath.Join(tmp, "town")
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("create town mayor dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}

	slowWorkDir := filepath.Join(townRoot, "rig-a")
	fastWorkDir := filepath.Join(townRoot, "rig-b")
	for _, dir := range []string{slowWorkDir, fastWorkDir} {
		if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0755); err != nil {
			t.Fatalf("create beads dir %s: %v", dir, err)
		}
	}
	if got := FindTownRoot(slowWorkDir); got != townRoot {
		t.Fatalf("FindTownRoot(%s) = %s, want %s", slowWorkDir, got, townRoot)
	}

	markerDir := filepath.Join(tmp, "markers")
	if err := os.MkdirAll(markerDir, 0755); err != nil {
		t.Fatalf("create marker dir: %v", err)
	}
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	writeConcurrentBDListStub(t, binDir)
	helperPath := binDir + string(os.PathListSeparator) + os.Getenv("PATH")

	releaseSlow := func() {
		_ = os.WriteFile(filepath.Join(markerDir, "release-slow"), []byte("ok"), 0644)
	}

	slowCtx, slowCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer slowCancel()
	slowCmd := bdListConcurrencyHelperCommand(slowCtx, helperPath, markerDir, slowWorkDir, "open")
	type helperResult struct {
		output []byte
		err    error
	}
	slowDone := make(chan helperResult, 1)
	go func() {
		out, err := slowCmd.CombinedOutput()
		slowDone <- helperResult{output: out, err: err}
	}()
	slowConsumed := false
	t.Cleanup(func() {
		if slowConsumed {
			return
		}
		releaseSlow()
		slowCancel()
		select {
		case <-slowDone:
		case <-time.After(5 * time.Second):
		}
	})

	waitForFile(t, filepath.Join(markerDir, "slow-started"), 2*time.Second)

	fastCtx, fastCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer fastCancel()
	fastCmd := bdListConcurrencyHelperCommand(fastCtx, helperPath, markerDir, fastWorkDir, "closed")
	started := time.Now()
	fastOut, fastErr := fastCmd.CombinedOutput()
	fastElapsed := time.Since(started)
	if fastCtx.Err() == context.DeadlineExceeded {
		t.Fatalf("fast unrelated bd list blocked behind slow list; output:\n%s", fastOut)
	}
	if fastErr != nil {
		t.Fatalf("fast unrelated bd list failed after %s: %v\n%s", fastElapsed, fastErr, fastOut)
	}

	select {
	case res := <-slowDone:
		slowConsumed = true
		if res.err != nil {
			t.Fatalf("slow bd list failed early: %v\n%s", res.err, res.output)
		}
		t.Fatalf("slow bd list completed before overlap was verified; fake bd did not hold the slow command")
	default:
	}

	releaseSlow()
	select {
	case res := <-slowDone:
		slowConsumed = true
		if res.err != nil {
			t.Fatalf("slow bd list failed: %v\n%s", res.err, res.output)
		}
	case <-time.After(3 * time.Second):
		slowCancel()
		t.Fatalf("slow bd list did not finish")
	}
}

func runBDListConcurrencyHelper(t *testing.T) {
	ResetBdAllowStaleCacheForTest()
	workDir := os.Getenv("GT_BEADS_LIST_HELPER_WORKDIR")
	status := os.Getenv("GT_BEADS_LIST_HELPER_STATUS")
	if workDir == "" || status == "" {
		t.Fatalf("missing helper environment: workdir=%q status=%q", workDir, status)
	}
	if _, err := NewIsolated(workDir).List(ListOptions{Status: status}); err != nil {
		t.Fatalf("List(%s): %v", status, err)
	}
}

func bdListConcurrencyHelperCommand(ctx context.Context, path, markerDir, workDir, status string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBDListSlowListDoesNotBlockUnrelatedList$")
	cmd.Env = append(sanitizedBDListConcurrencyEnv(),
		"GT_BEADS_LIST_CONCURRENCY_HELPER=1",
		"GT_BEADS_LIST_MARKER_DIR="+markerDir,
		"GT_BEADS_LIST_HELPER_WORKDIR="+workDir,
		"GT_BEADS_LIST_HELPER_STATUS="+status,
		"PATH="+path,
	)
	return cmd
}

func sanitizedBDListConcurrencyEnv() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, item := range os.Environ() {
		switch {
		case strings.HasPrefix(item, "PATH="),
			strings.HasPrefix(item, "BD_ACTOR="),
			strings.HasPrefix(item, "BEADS_"),
			strings.HasPrefix(item, "GT_ROOT="),
			strings.HasPrefix(item, "HOME="),
			strings.HasPrefix(item, "GT_BEADS_LIST_CONCURRENCY_HELPER="),
			strings.HasPrefix(item, "GT_BEADS_LIST_MARKER_DIR="),
			strings.HasPrefix(item, "GT_BEADS_LIST_HELPER_WORKDIR="),
			strings.HasPrefix(item, "GT_BEADS_LIST_HELPER_STATUS="):
			continue
		default:
			env = append(env, item)
		}
	}
	return env
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func writeConcurrentBDListStub(t *testing.T, dir string) {
	t.Helper()
	scriptPath := filepath.Join(dir, "bd")
	script := `#!/bin/sh
set -eu

if [ "${1:-}" = "--allow-stale" ]; then
  shift
fi

if [ "${1:-}" = "version" ]; then
  exit 0
fi

if [ "${1:-}" != "list" ]; then
  echo "unexpected bd args: $*" >&2
  exit 1
fi

status=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "status" ]; then
    status="$arg"
    prev=""
    continue
  fi
  case "$arg" in
    --status=*) status="${arg#--status=}" ;;
    --status) prev="status" ;;
  esac
done

if [ "$status" = "open" ]; then
  : > "${GT_BEADS_LIST_MARKER_DIR}/slow-started"
  i=0
  while [ ! -e "${GT_BEADS_LIST_MARKER_DIR}/release-slow" ]; do
    i=$((i + 1))
    if [ "$i" -ge 1000 ]; then
      echo "timed out waiting for release-slow" >&2
      exit 2
    fi
    sleep 0.01
  done
fi

printf '[]\n'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write bd concurrency stub: %v", err)
	}
}

// writeAllowStaleBDStub creates a mock bd binary in dir.
//
// The detection function (BdSupportsAllowStaleWithEnv) ignores the exit code
// and checks output for "unknown flag" (matching real bd v0.60+ behavior where
// unknown flags exit 0 but print an error to stderr). The stubs must match:
//   - Supporting: exit 0, no output
//   - Non-supporting: exit 0, print "unknown flag" to stderr
func writeAllowStaleBDStub(t *testing.T, dir string, supportsAllowStale bool) {
	t.Helper()

	// bd v0.60+ exits 0 even on unknown flags, printing the error to stderr.
	// Detection now checks output for "unknown flag" rather than exit code.
	var scriptPath, script string
	if runtime.GOOS == "windows" {
		scriptPath = filepath.Join(dir, "bd.bat")
		if supportsAllowStale {
			script = `@echo off
setlocal enableextensions
if "%1"=="--allow-stale" echo bd test
exit /b 0
`
		} else {
			script = `@echo off
setlocal enableextensions
if "%1"=="--allow-stale" (
  echo Error: unknown flag: --allow-stale 1>&2
  exit /b 0
)
exit /b 0
`
		}
	} else {
		scriptPath = filepath.Join(dir, "bd")
		if supportsAllowStale {
			script = `#!/bin/sh
	echo "bd test"
exit 0
`
		} else {
			script = `#!/bin/sh
if [ "$1" = "--allow-stale" ]; then
  echo "Error: unknown flag: --allow-stale" >&2
fi
exit 0
`
		}
	}

	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
}

func writeHangingAllowStaleBDStub(t *testing.T, dir, markerPath string) {
	t.Helper()

	var scriptPath, script string
	if runtime.GOOS == "windows" {
		scriptPath = filepath.Join(dir, "bd.bat")
		script = `@echo off
setlocal enableextensions
if "%1"=="--allow-stale" (
  ping -n 6 127.0.0.1 >nul
)
exit /b 0
`
	} else {
		scriptPath = filepath.Join(dir, "bd")
		script = fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--allow-stale" ]; then
  (
    sleep 0.2
    : > %q
  ) &
  child=$!
  wait "$child"
fi
exit 0
`, markerPath)
	}

	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write hanging bd stub: %v", err)
	}
}

// TestUpdateOptions verifies UpdateOptions pointer fields.
func TestUpdateOptions(t *testing.T) {
	status := "in_progress"
	priority := 1
	opts := UpdateOptions{
		Status:   &status,
		Priority: &priority,
	}
	if *opts.Status != "in_progress" {
		t.Errorf("Status = %q, want in_progress", *opts.Status)
	}
	if *opts.Priority != 1 {
		t.Errorf("Priority = %d, want 1", *opts.Priority)
	}
}

// TestIsBeadsRepo tests repository detection.
func TestIsBeadsRepo(t *testing.T) {
	// Test with a non-beads directory
	tmpDir, err := os.MkdirTemp("", "beads-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	b := New(tmpDir)
	// Should return false since there's no .beads directory
	if b.IsBeadsRepo() {
		t.Error("IsBeadsRepo returned true for non-beads directory")
	}
}

// TestWrapError tests error wrapping.
// ZFC: Only test ErrNotFound detection. ErrNotARepo and ErrSyncConflict
// were removed as per ZFC - agents should handle those errors directly.
func TestWrapError(t *testing.T) {
	b := New("/test")

	tests := []struct {
		stderr  string
		wantErr error
		wantNil bool
	}{
		{"Issue not found: gt-xyz", ErrNotFound, false},
		{"gt-xyz not found", ErrNotFound, false},
	}

	for _, tt := range tests {
		err := b.wrapError(nil, tt.stderr, []string{"test"})
		if tt.wantNil {
			if err != nil {
				t.Errorf("wrapError(%q) = %v, want nil", tt.stderr, err)
			}
		} else {
			if err != tt.wantErr {
				t.Errorf("wrapError(%q) = %v, want %v", tt.stderr, err, tt.wantErr)
			}
		}
	}
}

// TestNormalizeBugTitle tests title normalization for duplicate detection.
func TestNormalizeBugTitle(t *testing.T) {
	tests := []struct {
		a, b string
		want bool // should they match?
	}{
		// Exact match after normalization
		{"test_foo fails", "test_foo fails", true},
		{"Test_foo Fails", "test_foo fails", true},
		{" test_foo fails ", "test_foo fails", true},

		// Common prefix stripping
		{"Pre-existing failure: test_foo fails", "test_foo fails", true},
		{"Pre-existing failure: test_foo fails", "Pre-existing: test_foo fails", true},
		{"Test failure: test_foo fails", "test_foo fails", true},

		// Different failures should NOT match
		{"test_foo fails", "test_bar fails", false},
		{"lint error in main.go", "test_foo fails", false},
	}

	for _, tt := range tests {
		na := normalizeBugTitle(tt.a)
		nb := normalizeBugTitle(tt.b)
		got := na == nb
		if got != tt.want {
			t.Errorf("normalizeBugTitle(%q) == normalizeBugTitle(%q): got %v, want %v (normalized: %q vs %q)",
				tt.a, tt.b, got, tt.want, na, nb)
		}
	}
}

// TestSearchOptions verifies SearchOptions fields.
func TestSearchOptions(t *testing.T) {
	opts := SearchOptions{
		Query:  "test failure",
		Status: "open",
		Label:  "gt:bug",
		Limit:  5,
	}
	if opts.Query != "test failure" {
		t.Errorf("Query = %q, want 'test failure'", opts.Query)
	}
	if opts.Status != "open" {
		t.Errorf("Status = %q, want 'open'", opts.Status)
	}
	if opts.Label != "gt:bug" {
		t.Errorf("Label = %q, want 'gt:bug'", opts.Label)
	}
}

// Integration test that runs against real bd if available
func TestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Find a beads repo (use current directory if it has .beads)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, ".beads")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("no .beads directory found in path")
		}
		dir = parent
	}

	// Resolve the actual beads directory (following redirect if present)
	// In multi-worktree setups, worktrees have .beads/redirect pointing to
	// the canonical beads location (e.g., mayor/rig/.beads)
	beadsDir := ResolveBeadsDir(dir)
	doltPath := filepath.Join(beadsDir, "dolt")
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		t.Skip("no dolt database found")
	}

	b := New(dir)

	// Test List
	t.Run("List", func(t *testing.T) {
		issues, err := b.List(ListOptions{Status: "open"})
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}
		t.Logf("Found %d open issues", len(issues))
	})

	// Test Ready
	t.Run("Ready", func(t *testing.T) {
		issues, err := b.Ready()
		if err != nil {
			t.Fatalf("Ready failed: %v", err)
		}
		t.Logf("Found %d ready issues", len(issues))
	})

	// Test Blocked
	t.Run("Blocked", func(t *testing.T) {
		issues, err := b.Blocked()
		if err != nil {
			t.Fatalf("Blocked failed: %v", err)
		}
		t.Logf("Found %d blocked issues", len(issues))
	})

	// Test Show (if we have issues)
	t.Run("Show", func(t *testing.T) {
		issues, err := b.List(ListOptions{})
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}
		if len(issues) == 0 {
			t.Skip("no issues to show")
		}

		issue, err := b.Show(issues[0].ID)
		if err != nil {
			t.Fatalf("Show(%s) failed: %v", issues[0].ID, err)
		}
		t.Logf("Showed issue: %s - %s", issue.ID, issue.Title)
	})
}

// TestParseMRFields tests parsing MR fields from issue descriptions.
func TestParseMRFields(t *testing.T) {
	tests := []struct {
		name       string
		issue      *Issue
		wantNil    bool
		wantFields *MRFields
	}{
		{
			name:    "nil issue",
			issue:   nil,
			wantNil: true,
		},
		{
			name:    "empty description",
			issue:   &Issue{Description: ""},
			wantNil: true,
		},
		{
			name:    "no MR fields",
			issue:   &Issue{Description: "This is just plain text\nwith no field markers"},
			wantNil: true,
		},
		{
			name: "all fields",
			issue: &Issue{
				Description: `branch: polecat/Nux/gt-xyz
target: main
source_issue: gt-xyz
worker: Nux
rig: gastown
merge_commit: abc123def
close_reason: merged`,
			},
			wantFields: &MRFields{
				Branch:      "polecat/Nux/gt-xyz",
				Target:      "main",
				SourceIssue: "gt-xyz",
				Worker:      "Nux",
				Rig:         "gastown",
				MergeCommit: "abc123def",
				CloseReason: "merged",
			},
		},
		{
			name: "partial fields",
			issue: &Issue{
				Description: `branch: polecat/Toast/gt-abc
target: integration/gt-epic
source_issue: gt-abc
worker: Toast`,
			},
			wantFields: &MRFields{
				Branch:      "polecat/Toast/gt-abc",
				Target:      "integration/gt-epic",
				SourceIssue: "gt-abc",
				Worker:      "Toast",
			},
		},
		{
			name: "mixed with prose",
			issue: &Issue{
				Description: `branch: polecat/Capable/gt-def
target: main
source_issue: gt-def

This MR fixes a critical bug in the authentication system.
Please review carefully.

worker: Capable
rig: wasteland`,
			},
			wantFields: &MRFields{
				Branch:      "polecat/Capable/gt-def",
				Target:      "main",
				SourceIssue: "gt-def",
				Worker:      "Capable",
				Rig:         "wasteland",
			},
		},
		{
			name: "alternate key formats",
			issue: &Issue{
				Description: `branch: polecat/Max/gt-ghi
source-issue: gt-ghi
merge-commit: 789xyz`,
			},
			wantFields: &MRFields{
				Branch:      "polecat/Max/gt-ghi",
				SourceIssue: "gt-ghi",
				MergeCommit: "789xyz",
			},
		},
		{
			name: "case insensitive keys",
			issue: &Issue{
				Description: `Branch: polecat/Furiosa/gt-jkl
TARGET: main
Worker: Furiosa
RIG: gastown`,
			},
			wantFields: &MRFields{
				Branch: "polecat/Furiosa/gt-jkl",
				Target: "main",
				Worker: "Furiosa",
				Rig:    "gastown",
			},
		},
		{
			name: "extra whitespace",
			issue: &Issue{
				Description: `  branch:   polecat/Nux/gt-mno
target:main
  worker:   Nux  `,
			},
			wantFields: &MRFields{
				Branch: "polecat/Nux/gt-mno",
				Target: "main",
				Worker: "Nux",
			},
		},
		{
			name: "ignores empty values",
			issue: &Issue{
				Description: `branch: polecat/Nux/gt-pqr
target:
source_issue: gt-pqr`,
			},
			wantFields: &MRFields{
				Branch:      "polecat/Nux/gt-pqr",
				SourceIssue: "gt-pqr",
			},
		},
		{
			name: "commit_sha field (GH#3032)",
			issue: &Issue{
				Description: `branch: polecat/nux/es-ixjt@mmw5d6mv
target: main
source_issue: es-ixjt
rig: gastown
commit_sha: a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2`,
			},
			wantFields: &MRFields{
				Branch:      "polecat/nux/es-ixjt@mmw5d6mv",
				Target:      "main",
				SourceIssue: "es-ixjt",
				Rig:         "gastown",
				CommitSHA:   "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
			},
		},
		{
			name: "null optional fields are empty",
			issue: &Issue{
				Description: `branch: polecat/Nux/gt-null
target: main
source_issue: gt-null
last_conflict_sha: null
conflict_task_id: null`,
			},
			wantFields: &MRFields{
				Branch:      "polecat/Nux/gt-null",
				Target:      "main",
				SourceIssue: "gt-null",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := ParseMRFields(tt.issue)

			if tt.wantNil {
				if fields != nil {
					t.Errorf("ParseMRFields() = %+v, want nil", fields)
				}
				return
			}

			if fields == nil {
				t.Fatal("ParseMRFields() = nil, want non-nil")
			}

			if fields.Branch != tt.wantFields.Branch {
				t.Errorf("Branch = %q, want %q", fields.Branch, tt.wantFields.Branch)
			}
			if fields.Target != tt.wantFields.Target {
				t.Errorf("Target = %q, want %q", fields.Target, tt.wantFields.Target)
			}
			if fields.SourceIssue != tt.wantFields.SourceIssue {
				t.Errorf("SourceIssue = %q, want %q", fields.SourceIssue, tt.wantFields.SourceIssue)
			}
			if fields.Worker != tt.wantFields.Worker {
				t.Errorf("Worker = %q, want %q", fields.Worker, tt.wantFields.Worker)
			}
			if fields.Rig != tt.wantFields.Rig {
				t.Errorf("Rig = %q, want %q", fields.Rig, tt.wantFields.Rig)
			}
			if fields.CommitSHA != tt.wantFields.CommitSHA {
				t.Errorf("CommitSHA = %q, want %q", fields.CommitSHA, tt.wantFields.CommitSHA)
			}
			if fields.MergeCommit != tt.wantFields.MergeCommit {
				t.Errorf("MergeCommit = %q, want %q", fields.MergeCommit, tt.wantFields.MergeCommit)
			}
			if fields.CloseReason != tt.wantFields.CloseReason {
				t.Errorf("CloseReason = %q, want %q", fields.CloseReason, tt.wantFields.CloseReason)
			}
			if fields.LastConflictSHA != tt.wantFields.LastConflictSHA {
				t.Errorf("LastConflictSHA = %q, want %q", fields.LastConflictSHA, tt.wantFields.LastConflictSHA)
			}
			if fields.ConflictTaskID != tt.wantFields.ConflictTaskID {
				t.Errorf("ConflictTaskID = %q, want %q", fields.ConflictTaskID, tt.wantFields.ConflictTaskID)
			}
		})
	}
}

// TestFormatMRFields tests formatting MR fields to string.
func TestFormatMRFields(t *testing.T) {
	tests := []struct {
		name   string
		fields *MRFields
		want   string
	}{
		{
			name:   "nil fields",
			fields: nil,
			want:   "",
		},
		{
			name:   "empty fields",
			fields: &MRFields{},
			want:   "",
		},
		{
			name: "all fields",
			fields: &MRFields{
				Branch:      "polecat/Nux/gt-xyz",
				Target:      "main",
				SourceIssue: "gt-xyz",
				Worker:      "Nux",
				Rig:         "gastown",
				MergeCommit: "abc123def",
				CloseReason: "merged",
			},
			want: `branch: polecat/Nux/gt-xyz
target: main
source_issue: gt-xyz
worker: Nux
rig: gastown
merge_commit: abc123def
close_reason: merged`,
		},
		{
			name: "partial fields",
			fields: &MRFields{
				Branch:      "polecat/Toast/gt-abc",
				Target:      "main",
				SourceIssue: "gt-abc",
				Worker:      "Toast",
			},
			want: `branch: polecat/Toast/gt-abc
target: main
source_issue: gt-abc
worker: Toast`,
		},
		{
			name: "only close fields",
			fields: &MRFields{
				MergeCommit: "deadbeef",
				CloseReason: "rejected",
			},
			want: `merge_commit: deadbeef
close_reason: rejected`,
		},
		{
			name: "with commit_sha (GH#3032)",
			fields: &MRFields{
				Branch:      "polecat/nux/es-ixjt@mmw5d6mv",
				Target:      "main",
				SourceIssue: "es-ixjt",
				Rig:         "gastown",
				CommitSHA:   "a1b2c3d4",
			},
			want: `branch: polecat/nux/es-ixjt@mmw5d6mv
target: main
source_issue: es-ixjt
rig: gastown
commit_sha: a1b2c3d4`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatMRFields(tt.fields)
			if got != tt.want {
				t.Errorf("FormatMRFields() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

// TestSetMRFields tests updating issue descriptions with MR fields.
func TestSetMRFields(t *testing.T) {
	tests := []struct {
		name   string
		issue  *Issue
		fields *MRFields
		want   string
	}{
		{
			name:  "nil issue",
			issue: nil,
			fields: &MRFields{
				Branch: "polecat/Nux/gt-xyz",
				Target: "main",
			},
			want: `branch: polecat/Nux/gt-xyz
target: main`,
		},
		{
			name:  "empty description",
			issue: &Issue{Description: ""},
			fields: &MRFields{
				Branch:      "polecat/Nux/gt-xyz",
				Target:      "main",
				SourceIssue: "gt-xyz",
			},
			want: `branch: polecat/Nux/gt-xyz
target: main
source_issue: gt-xyz`,
		},
		{
			name:  "preserve prose content",
			issue: &Issue{Description: "This is a description of the work.\n\nIt spans multiple lines."},
			fields: &MRFields{
				Branch: "polecat/Toast/gt-abc",
				Worker: "Toast",
			},
			want: `branch: polecat/Toast/gt-abc
worker: Toast

This is a description of the work.

It spans multiple lines.`,
		},
		{
			name: "replace existing fields",
			issue: &Issue{
				Description: `branch: polecat/Nux/gt-old
target: develop
source_issue: gt-old
commit_sha: oldsha
worker: Nux

Some existing prose content.`,
			},
			fields: &MRFields{
				Branch:      "polecat/Nux/gt-new",
				Target:      "main",
				SourceIssue: "gt-new",
				CommitSHA:   "newsha",
				Worker:      "Nux",
				MergeCommit: "abc123",
			},
			want: `branch: polecat/Nux/gt-new
target: main
source_issue: gt-new
worker: Nux
commit_sha: newsha
merge_commit: abc123

Some existing prose content.`,
		},
		{
			name: "preserve non-MR key-value lines",
			issue: &Issue{
				Description: `branch: polecat/Capable/gt-def
custom_field: some value
author: someone
target: main`,
			},
			fields: &MRFields{
				Branch:      "polecat/Capable/gt-ghi",
				Target:      "integration/epic",
				CloseReason: "merged",
			},
			want: `branch: polecat/Capable/gt-ghi
target: integration/epic
close_reason: merged

custom_field: some value
author: someone`,
		},
		{
			name:   "empty fields clears MR data",
			issue:  &Issue{Description: "branch: old\ntarget: old\n\nKeep this text."},
			fields: &MRFields{},
			want:   "Keep this text.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SetMRFields(tt.issue, tt.fields)
			if got != tt.want {
				t.Errorf("SetMRFields() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

// TestMRFieldsRoundTrip tests that parse/format round-trips correctly.
func TestMRFieldsRoundTrip(t *testing.T) {
	original := &MRFields{
		Branch:      "polecat/Nux/gt-xyz",
		Target:      "main",
		SourceIssue: "gt-xyz",
		Worker:      "Nux",
		Rig:         "gastown",
		MergeCommit: "abc123def789",
		CloseReason: "merged",
	}

	// Format to string
	formatted := FormatMRFields(original)

	// Parse back
	issue := &Issue{Description: formatted}
	parsed := ParseMRFields(issue)

	if parsed == nil {
		t.Fatal("round-trip parse returned nil")
	}

	if !reflect.DeepEqual(parsed, original) {
		t.Errorf("round-trip mismatch:\ngot  %+v\nwant %+v", parsed, original)
	}
}

// TestParseMRFieldsFromDesignDoc tests the example from the design doc.
func TestParseMRFieldsFromDesignDoc(t *testing.T) {
	// Example from docs/merge-queue-design.md
	description := `branch: polecat/Nux/gt-xyz
target: main
source_issue: gt-xyz
worker: Nux
rig: gastown`

	issue := &Issue{Description: description}
	fields := ParseMRFields(issue)

	if fields == nil {
		t.Fatal("ParseMRFields returned nil for design doc example")
	}

	// Verify all fields match the design doc
	if fields.Branch != "polecat/Nux/gt-xyz" {
		t.Errorf("Branch = %q, want polecat/Nux/gt-xyz", fields.Branch)
	}
	if fields.Target != "main" {
		t.Errorf("Target = %q, want main", fields.Target)
	}
	if fields.SourceIssue != "gt-xyz" {
		t.Errorf("SourceIssue = %q, want gt-xyz", fields.SourceIssue)
	}
	if fields.Worker != "Nux" {
		t.Errorf("Worker = %q, want Nux", fields.Worker)
	}
	if fields.Rig != "gastown" {
		t.Errorf("Rig = %q, want gastown", fields.Rig)
	}
}

// TestSetMRFieldsPreservesURL tests that URLs in prose are preserved.
func TestSetMRFieldsPreservesURL(t *testing.T) {
	// URLs contain colons which could be confused with key: value
	issue := &Issue{
		Description: `branch: old-branch
Check out https://example.com/path for more info.
Also see http://localhost:8080/api`,
	}

	fields := &MRFields{
		Branch: "new-branch",
		Target: "main",
	}

	result := SetMRFields(issue, fields)

	// URLs should be preserved
	if !strings.Contains(result, "https://example.com/path") {
		t.Error("HTTPS URL was not preserved")
	}
	if !strings.Contains(result, "http://localhost:8080/api") {
		t.Error("HTTP URL was not preserved")
	}
	if !strings.Contains(result, "branch: new-branch") {
		t.Error("branch field was not set")
	}
}

// TestParseAttachmentFields tests parsing attachment fields from issue descriptions.
func TestParseAttachmentFields(t *testing.T) {
	tests := []struct {
		name       string
		issue      *Issue
		wantNil    bool
		wantFields *AttachmentFields
	}{
		{
			name:    "nil issue",
			issue:   nil,
			wantNil: true,
		},
		{
			name:    "empty description",
			issue:   &Issue{Description: ""},
			wantNil: true,
		},
		{
			name:    "no attachment fields",
			issue:   &Issue{Description: "This is just plain text\nwith no attachment markers"},
			wantNil: true,
		},
		{
			name: "both fields",
			issue: &Issue{
				Description: `attached_molecule: mol-xyz
attached_at: 2025-12-21T15:30:00Z`,
			},
			wantFields: &AttachmentFields{
				AttachedMolecule: "mol-xyz",
				AttachedAt:       "2025-12-21T15:30:00Z",
			},
		},
		{
			name: "only molecule",
			issue: &Issue{
				Description: `attached_molecule: mol-abc`,
			},
			wantFields: &AttachmentFields{
				AttachedMolecule: "mol-abc",
			},
		},
		{
			name: "mixed with other content",
			issue: &Issue{
				Description: `attached_molecule: mol-def
attached_at: 2025-12-21T10:00:00Z

This is a handoff bead for the polecat.
Keep working on the current task.`,
			},
			wantFields: &AttachmentFields{
				AttachedMolecule: "mol-def",
				AttachedAt:       "2025-12-21T10:00:00Z",
			},
		},
		{
			name: "alternate key formats (hyphen)",
			issue: &Issue{
				Description: `attached-molecule: mol-ghi
attached-at: 2025-12-21T12:00:00Z`,
			},
			wantFields: &AttachmentFields{
				AttachedMolecule: "mol-ghi",
				AttachedAt:       "2025-12-21T12:00:00Z",
			},
		},
		{
			name: "case insensitive",
			issue: &Issue{
				Description: `Attached_Molecule: mol-jkl
ATTACHED_AT: 2025-12-21T14:00:00Z`,
			},
			wantFields: &AttachmentFields{
				AttachedMolecule: "mol-jkl",
				AttachedAt:       "2025-12-21T14:00:00Z",
			},
		},
		{
			name: "attached vars",
			issue: &Issue{
				Description: `attached_formula: mol-release
attached_vars: ["version=1.2.3","channel=stable"]`,
			},
			wantFields: &AttachmentFields{
				AttachedFormula: "mol-release",
				AttachedVars:    []string{"version=1.2.3", "channel=stable"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := ParseAttachmentFields(tt.issue)

			if tt.wantNil {
				if fields != nil {
					t.Errorf("ParseAttachmentFields() = %+v, want nil", fields)
				}
				return
			}

			if fields == nil {
				t.Fatal("ParseAttachmentFields() = nil, want non-nil")
			}

			if fields.AttachedMolecule != tt.wantFields.AttachedMolecule {
				t.Errorf("AttachedMolecule = %q, want %q", fields.AttachedMolecule, tt.wantFields.AttachedMolecule)
			}
			if fields.AttachedAt != tt.wantFields.AttachedAt {
				t.Errorf("AttachedAt = %q, want %q", fields.AttachedAt, tt.wantFields.AttachedAt)
			}
			if !reflect.DeepEqual(fields.AttachedVars, tt.wantFields.AttachedVars) {
				t.Errorf("AttachedVars = %#v, want %#v", fields.AttachedVars, tt.wantFields.AttachedVars)
			}
		})
	}
}

// TestFormatAttachmentFields tests formatting attachment fields to string.
func TestFormatAttachmentFields(t *testing.T) {
	tests := []struct {
		name   string
		fields *AttachmentFields
		want   string
	}{
		{
			name:   "nil fields",
			fields: nil,
			want:   "",
		},
		{
			name:   "empty fields",
			fields: &AttachmentFields{},
			want:   "",
		},
		{
			name: "both fields",
			fields: &AttachmentFields{
				AttachedMolecule: "mol-xyz",
				AttachedAt:       "2025-12-21T15:30:00Z",
			},
			want: `attached_molecule: mol-xyz
attached_at: 2025-12-21T15:30:00Z`,
		},
		{
			name: "only molecule",
			fields: &AttachmentFields{
				AttachedMolecule: "mol-abc",
			},
			want: "attached_molecule: mol-abc",
		},
		{
			name: "attached vars",
			fields: &AttachmentFields{
				AttachedFormula: "mol-release",
				AttachedVars:    []string{"version=1.2.3", "channel=stable"},
			},
			want: "attached_formula: mol-release\nattached_vars: [\"version=1.2.3\",\"channel=stable\"]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatAttachmentFields(tt.fields)
			if got != tt.want {
				t.Errorf("FormatAttachmentFields() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

// TestSetAttachmentFields tests updating issue descriptions with attachment fields.
func TestSetAttachmentFields(t *testing.T) {
	tests := []struct {
		name   string
		issue  *Issue
		fields *AttachmentFields
		want   string
	}{
		{
			name:  "nil issue",
			issue: nil,
			fields: &AttachmentFields{
				AttachedMolecule: "mol-xyz",
				AttachedAt:       "2025-12-21T15:30:00Z",
			},
			want: `attached_molecule: mol-xyz
attached_at: 2025-12-21T15:30:00Z`,
		},
		{
			name:  "empty description",
			issue: &Issue{Description: ""},
			fields: &AttachmentFields{
				AttachedMolecule: "mol-abc",
				AttachedAt:       "2025-12-21T10:00:00Z",
			},
			want: `attached_molecule: mol-abc
attached_at: 2025-12-21T10:00:00Z`,
		},
		{
			name:  "preserve prose content",
			issue: &Issue{Description: "This is a handoff bead description.\n\nKeep working on the task."},
			fields: &AttachmentFields{
				AttachedMolecule: "mol-def",
			},
			want: `attached_molecule: mol-def

This is a handoff bead description.

Keep working on the task.`,
		},
		{
			name: "replace existing fields",
			issue: &Issue{
				Description: `attached_molecule: mol-old
attached_at: 2025-12-20T10:00:00Z

Some existing prose content.`,
			},
			fields: &AttachmentFields{
				AttachedMolecule: "mol-new",
				AttachedAt:       "2025-12-21T15:30:00Z",
			},
			want: `attached_molecule: mol-new
attached_at: 2025-12-21T15:30:00Z

Some existing prose content.`,
		},
		{
			name:   "nil fields clears attachment",
			issue:  &Issue{Description: "attached_molecule: mol-old\nattached_at: 2025-12-20T10:00:00Z\n\nKeep this text."},
			fields: nil,
			want:   "Keep this text.",
		},
		{
			name:   "empty fields clears attachment",
			issue:  &Issue{Description: "attached_molecule: mol-old\n\nKeep this text."},
			fields: &AttachmentFields{},
			want:   "Keep this text.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SetAttachmentFields(tt.issue, tt.fields)
			if got != tt.want {
				t.Errorf("SetAttachmentFields() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

// TestAttachmentFieldsRoundTrip tests that parse/format round-trips correctly.
func TestAttachmentFieldsRoundTrip(t *testing.T) {
	original := &AttachmentFields{
		AttachedMolecule: "mol-roundtrip",
		AttachedAt:       "2025-12-21T15:30:00Z",
	}

	// Format to string
	formatted := FormatAttachmentFields(original)

	// Parse back
	issue := &Issue{Description: formatted}
	parsed := ParseAttachmentFields(issue)

	if parsed == nil {
		t.Fatal("round-trip parse returned nil")
	}

	if !reflect.DeepEqual(parsed, original) {
		t.Errorf("round-trip mismatch:\ngot  %+v\nwant %+v", parsed, original)
	}
}

// TestNoMergeField tests the no_merge field in AttachmentFields.
// The no_merge flag tells gt done to skip the merge queue and keep work on a feature branch.
func TestNoMergeField(t *testing.T) {
	t.Run("parse no_merge true", func(t *testing.T) {
		issue := &Issue{Description: "no_merge: true\ndispatched_by: mayor"}
		fields := ParseAttachmentFields(issue)
		if fields == nil {
			t.Fatal("ParseAttachmentFields() = nil")
		}
		if !fields.NoMerge {
			t.Error("NoMerge should be true")
		}
		if fields.DispatchedBy != "mayor" {
			t.Errorf("DispatchedBy = %q, want 'mayor'", fields.DispatchedBy)
		}
	})

	t.Run("parse no_merge false", func(t *testing.T) {
		issue := &Issue{Description: "no_merge: false\ndispatched_by: crew"}
		fields := ParseAttachmentFields(issue)
		if fields == nil {
			t.Fatal("ParseAttachmentFields() = nil")
		}
		if fields.NoMerge {
			t.Error("NoMerge should be false")
		}
	})

	t.Run("parse no-merge alternate format", func(t *testing.T) {
		issue := &Issue{Description: "no-merge: true"}
		fields := ParseAttachmentFields(issue)
		if fields == nil {
			t.Fatal("ParseAttachmentFields() = nil")
		}
		if !fields.NoMerge {
			t.Error("NoMerge should be true with hyphen format")
		}
	})

	t.Run("format no_merge", func(t *testing.T) {
		fields := &AttachmentFields{
			NoMerge:      true,
			DispatchedBy: "mayor",
		}
		got := FormatAttachmentFields(fields)
		if !strings.Contains(got, "no_merge: true") {
			t.Errorf("FormatAttachmentFields() missing no_merge, got:\n%s", got)
		}
		if !strings.Contains(got, "dispatched_by: mayor") {
			t.Errorf("FormatAttachmentFields() missing dispatched_by, got:\n%s", got)
		}
	})

	t.Run("round-trip with no_merge", func(t *testing.T) {
		original := &AttachmentFields{
			AttachedMolecule: "mol-test",
			AttachedAt:       "2026-01-24T12:00:00Z",
			DispatchedBy:     "gastown/crew/max",
			NoMerge:          true,
		}

		formatted := FormatAttachmentFields(original)
		issue := &Issue{Description: formatted}
		parsed := ParseAttachmentFields(issue)

		if parsed == nil {
			t.Fatal("round-trip parse returned nil")
		}
		if !reflect.DeepEqual(parsed, original) {
			t.Errorf("round-trip mismatch:\ngot  %+v\nwant %+v", parsed, original)
		}
	})
}

// TestResolveBeadsDir tests the redirect following logic.
func TestResolveBeadsDir(t *testing.T) {
	// Create temp directory structure
	tmpDir, err := os.MkdirTemp("", "beads-redirect-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	t.Run("no redirect", func(t *testing.T) {
		// Create a simple .beads directory without redirect
		workDir := filepath.Join(tmpDir, "no-redirect")
		beadsDir := filepath.Join(workDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		got := ResolveBeadsDir(workDir)
		want := beadsDir
		if got != want {
			t.Errorf("ResolveBeadsDir() = %q, want %q", got, want)
		}
	})

	t.Run("with redirect", func(t *testing.T) {
		// Create structure like: crew/max/.beads/redirect -> ../../mayor/rig/.beads
		workDir := filepath.Join(tmpDir, "crew", "max")
		localBeadsDir := filepath.Join(workDir, ".beads")
		targetBeadsDir := filepath.Join(tmpDir, "mayor", "rig", ".beads")

		// Create both directories
		if err := os.MkdirAll(localBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(targetBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create redirect file
		redirectPath := filepath.Join(localBeadsDir, "redirect")
		if err := os.WriteFile(redirectPath, []byte("../../mayor/rig/.beads\n"), 0644); err != nil {
			t.Fatal(err)
		}

		got := ResolveBeadsDir(workDir)
		want := targetBeadsDir
		if got != want {
			t.Errorf("ResolveBeadsDir() = %q, want %q", got, want)
		}
	})

	t.Run("no beads directory", func(t *testing.T) {
		// Directory with no .beads at all
		workDir := filepath.Join(tmpDir, "empty")
		if err := os.MkdirAll(workDir, 0755); err != nil {
			t.Fatal(err)
		}

		got := ResolveBeadsDir(workDir)
		want := filepath.Join(workDir, ".beads")
		if got != want {
			t.Errorf("ResolveBeadsDir() = %q, want %q", got, want)
		}
	})

	t.Run("empty redirect file", func(t *testing.T) {
		// Redirect file exists but is empty - should fall back to local
		workDir := filepath.Join(tmpDir, "empty-redirect")
		beadsDir := filepath.Join(workDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		redirectPath := filepath.Join(beadsDir, "redirect")
		if err := os.WriteFile(redirectPath, []byte("  \n"), 0644); err != nil {
			t.Fatal(err)
		}

		got := ResolveBeadsDir(workDir)
		want := beadsDir
		if got != want {
			t.Errorf("ResolveBeadsDir() = %q, want %q", got, want)
		}
	})

	t.Run("absolute path redirect", func(t *testing.T) {
		// Redirect file contains an absolute path (e.g., /Users/emech/.../gastown/.beads)
		// This was the path-doubling bug: filepath.Join(workDir, absPath) produces
		// workDir/Users/emech/... instead of using absPath directly.
		workDir := filepath.Join(tmpDir, "polecat", "chrome")
		localBeadsDir := filepath.Join(workDir, ".beads")
		targetBeadsDir := filepath.Join(tmpDir, "canonical", ".beads")

		if err := os.MkdirAll(localBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(targetBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Write absolute path redirect
		redirectPath := filepath.Join(localBeadsDir, "redirect")
		if err := os.WriteFile(redirectPath, []byte(targetBeadsDir+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		got := ResolveBeadsDir(workDir)
		if got != targetBeadsDir {
			t.Errorf("ResolveBeadsDir() = %q, want %q (absolute redirect should be used as-is)", got, targetBeadsDir)
		}
	})

	t.Run("absolute path in redirect chain", func(t *testing.T) {
		// Test absolute path handling in resolveBeadsDirWithDepth (chained redirects)
		firstBeadsDir := filepath.Join(tmpDir, "chain-test", "first", ".beads")
		finalBeadsDir := filepath.Join(tmpDir, "chain-test", "final", ".beads")

		if err := os.MkdirAll(firstBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(finalBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// First beads redirects via absolute path to final
		if err := os.WriteFile(filepath.Join(firstBeadsDir, "redirect"), []byte(finalBeadsDir+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		got := resolveBeadsDirWithDepth(firstBeadsDir, 3)
		if got != finalBeadsDir {
			t.Errorf("resolveBeadsDirWithDepth() = %q, want %q", got, finalBeadsDir)
		}
	})

	t.Run("circular redirect", func(t *testing.T) {
		// Redirect that points to itself (e.g., mayor/rig/.beads/redirect -> ../../mayor/rig/.beads)
		// This is the bug scenario from gt-csbjj
		workDir := filepath.Join(tmpDir, "mayor", "rig")
		beadsDir := filepath.Join(workDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create a circular redirect: ../../mayor/rig/.beads resolves back to .beads
		redirectPath := filepath.Join(beadsDir, "redirect")
		if err := os.WriteFile(redirectPath, []byte("../../mayor/rig/.beads\n"), 0644); err != nil {
			t.Fatal(err)
		}

		// ResolveBeadsDir should detect the circular redirect and return the original beadsDir
		got := ResolveBeadsDir(workDir)
		want := beadsDir
		if got != want {
			t.Errorf("ResolveBeadsDir() = %q, want %q (should ignore circular redirect)", got, want)
		}

		// The circular redirect file should have been removed
		if _, err := os.Stat(redirectPath); err == nil {
			t.Error("circular redirect file should have been removed, but it still exists")
		}
	})
}

func TestParseAgentBeadID(t *testing.T) {
	tests := []struct {
		input    string
		wantRig  string
		wantRole string
		wantName string
		wantOK   bool
	}{
		// Town-level agents
		{"gt-mayor", "", "mayor", "", true},
		{"gt-deacon", "", "deacon", "", true},
		// Rig-level singletons
		{"gt-gastown-witness", "gastown", "witness", "", true},
		{"gt-gastown-refinery", "gastown", "refinery", "", true},
		// Rig-level named agents
		{"gt-gastown-crew-joe", "gastown", "crew", "joe", true},
		{"gt-gastown-crew-max", "gastown", "crew", "max", true},
		{"gt-gastown-polecat-capable", "gastown", "polecat", "capable", true},
		// Names with hyphens
		{"gt-gastown-polecat-my-agent", "gastown", "polecat", "my-agent", true},
		// Worker name collides with role keyword
		{"gt-gastown-polecat-witness", "gastown", "polecat", "witness", true},
		{"gt-gastown-polecat-refinery", "gastown", "polecat", "refinery", true},
		{"gt-gastown-crew-witness", "gastown", "crew", "witness", true},
		{"gt-gastown-crew-refinery", "gastown", "crew", "refinery", true},
		{"gt-gastown-polecat-crew", "gastown", "polecat", "crew", true},
		{"gt-gastown-crew-polecat", "gastown", "crew", "polecat", true},
		// Worker name collides with role keyword + hyphenated rig
		{"gt-my-rig-polecat-witness", "my-rig", "polecat", "witness", true},
		// Collapsed form: prefix == rig (e.g., rig "ff" with prefix "ff")
		{"ff-witness", "ff", "witness", "", true},                // collapsed rig-level singleton
		{"ff-refinery", "ff", "refinery", "", true},              // collapsed rig-level singleton
		{"ff-polecat-nux", "ff", "polecat", "nux", true},         // collapsed named agent
		{"ff-crew-dave", "ff", "crew", "dave", true},             // collapsed named agent
		{"ff-polecat-war-boy", "ff", "polecat", "war-boy", true}, // collapsed named with hyphen
		// Parseable but not valid agent roles (IsAgentSessionBead will reject)
		{"gt-abc123", "", "abc123", "", true}, // Parses as town-level but not valid role
		// Other prefixes (bd-, hq-)
		{"bd-mayor", "", "mayor", "", true},                           // bd prefix town-level
		{"bd-beads-witness", "beads", "witness", "", true},            // bd prefix rig-level singleton
		{"bd-beads-polecat-pearl", "beads", "polecat", "pearl", true}, // bd prefix rig-level named
		{"hq-mayor", "", "mayor", "", true},                           // hq prefix town-level
		// Truly invalid patterns
		{"x-mayor", "", "", "", false},    // Prefix too short (1 char)
		{"abcd-mayor", "", "", "", false}, // Prefix too long (4 chars)
		{"", "", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			rig, role, name, ok := ParseAgentBeadID(tt.input)
			if ok != tt.wantOK {
				t.Errorf("ParseAgentBeadID(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
				return
			}
			if rig != tt.wantRig {
				t.Errorf("ParseAgentBeadID(%q) rig = %q, want %q", tt.input, rig, tt.wantRig)
			}
			if role != tt.wantRole {
				t.Errorf("ParseAgentBeadID(%q) role = %q, want %q", tt.input, role, tt.wantRole)
			}
			if name != tt.wantName {
				t.Errorf("ParseAgentBeadID(%q) name = %q, want %q", tt.input, name, tt.wantName)
			}
		})
	}
}

func TestIsAgentSessionBead(t *testing.T) {
	tests := []struct {
		beadID string
		want   bool
	}{
		// Agent session beads with gt- prefix (should return true)
		{"gt-mayor", true},
		{"gt-deacon", true},
		{"gt-gastown-witness", true},
		{"gt-gastown-refinery", true},
		{"gt-gastown-crew-joe", true},
		{"gt-gastown-polecat-capable", true},
		// Agent session beads with bd- prefix (should return true)
		{"bd-mayor", true},
		{"bd-deacon", true},
		{"bd-beads-witness", true},
		{"bd-beads-refinery", true},
		{"bd-beads-crew-joe", true},
		{"bd-beads-polecat-pearl", true},
		// Regular work beads (should return false)
		{"gt-abc123", false},
		{"gt-sb6m4", false},
		{"gt-u7dxq", false},
		{"bd-abc123", false},
		// Invalid beads
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.beadID, func(t *testing.T) {
			got := IsAgentSessionBead(tt.beadID)
			if got != tt.want {
				t.Errorf("IsAgentSessionBead(%q) = %v, want %v", tt.beadID, got, tt.want)
			}
		})
	}
}

// TestParseRoleConfig tests parsing role configuration from descriptions.
func TestParseRoleConfig(t *testing.T) {
	tests := []struct {
		name        string
		description string
		wantNil     bool
		wantConfig  *RoleConfig
	}{
		{
			name:        "empty description",
			description: "",
			wantNil:     true,
		},
		{
			name:        "no role config fields",
			description: "This is just plain text\nwith no role config fields",
			wantNil:     true,
		},
		{
			name: "all fields",
			description: `session_pattern: gt-{rig}-{name}
work_dir_pattern: {town}/{rig}/polecats/{name}
needs_pre_sync: true
start_command: exec claude --dangerously-skip-permissions
env_var: GT_ROLE=polecat
env_var: GT_RIG={rig}`,
			wantConfig: &RoleConfig{
				SessionPattern: "gt-{rig}-{name}",
				WorkDirPattern: "{town}/{rig}/polecats/{name}",
				NeedsPreSync:   true,
				StartCommand:   "exec claude --dangerously-skip-permissions",
				EnvVars:        map[string]string{"GT_ROLE": "polecat", "GT_RIG": "{rig}"},
			},
		},
		{
			name: "partial fields",
			description: `session_pattern: gt-mayor
work_dir_pattern: {town}`,
			wantConfig: &RoleConfig{
				SessionPattern: "gt-mayor",
				WorkDirPattern: "{town}",
				EnvVars:        map[string]string{},
			},
		},
		{
			name: "mixed with prose",
			description: `You are the Witness.

session_pattern: gt-{rig}-witness
work_dir_pattern: {town}/{rig}
needs_pre_sync: false

Your job is to monitor workers.`,
			wantConfig: &RoleConfig{
				SessionPattern: "gt-{rig}-witness",
				WorkDirPattern: "{town}/{rig}",
				NeedsPreSync:   false,
				EnvVars:        map[string]string{},
			},
		},
		{
			name: "alternate key formats (hyphen)",
			description: `session-pattern: gt-{rig}-{name}
work-dir-pattern: {town}/{rig}/polecats/{name}
needs-pre-sync: true`,
			wantConfig: &RoleConfig{
				SessionPattern: "gt-{rig}-{name}",
				WorkDirPattern: "{town}/{rig}/polecats/{name}",
				NeedsPreSync:   true,
				EnvVars:        map[string]string{},
			},
		},
		{
			name: "case insensitive keys",
			description: `SESSION_PATTERN: gt-mayor
Work_Dir_Pattern: {town}`,
			wantConfig: &RoleConfig{
				SessionPattern: "gt-mayor",
				WorkDirPattern: "{town}",
				EnvVars:        map[string]string{},
			},
		},
		{
			name: "ignores null values",
			description: `session_pattern: gt-{rig}-witness
work_dir_pattern: null
needs_pre_sync: false`,
			wantConfig: &RoleConfig{
				SessionPattern: "gt-{rig}-witness",
				EnvVars:        map[string]string{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := ParseRoleConfig(tt.description)

			if tt.wantNil {
				if config != nil {
					t.Errorf("ParseRoleConfig() = %+v, want nil", config)
				}
				return
			}

			if config == nil {
				t.Fatal("ParseRoleConfig() = nil, want non-nil")
			}

			if config.SessionPattern != tt.wantConfig.SessionPattern {
				t.Errorf("SessionPattern = %q, want %q", config.SessionPattern, tt.wantConfig.SessionPattern)
			}
			if config.WorkDirPattern != tt.wantConfig.WorkDirPattern {
				t.Errorf("WorkDirPattern = %q, want %q", config.WorkDirPattern, tt.wantConfig.WorkDirPattern)
			}
			if config.NeedsPreSync != tt.wantConfig.NeedsPreSync {
				t.Errorf("NeedsPreSync = %v, want %v", config.NeedsPreSync, tt.wantConfig.NeedsPreSync)
			}
			if config.StartCommand != tt.wantConfig.StartCommand {
				t.Errorf("StartCommand = %q, want %q", config.StartCommand, tt.wantConfig.StartCommand)
			}
			if len(config.EnvVars) != len(tt.wantConfig.EnvVars) {
				t.Errorf("EnvVars len = %d, want %d", len(config.EnvVars), len(tt.wantConfig.EnvVars))
			}
			for k, v := range tt.wantConfig.EnvVars {
				if config.EnvVars[k] != v {
					t.Errorf("EnvVars[%q] = %q, want %q", k, config.EnvVars[k], v)
				}
			}
		})
	}
}

// TestExpandRolePattern tests pattern expansion with placeholders.
func TestExpandRolePattern(t *testing.T) {
	tests := []struct {
		pattern  string
		townRoot string
		rig      string
		name     string
		role     string
		prefix   string
		want     string
	}{
		{
			pattern:  "gt-mayor",
			townRoot: "/Users/stevey/gt",
			want:     "gt-mayor",
		},
		{
			pattern:  "{prefix}-{role}",
			townRoot: "/Users/stevey/gt",
			rig:      "gastown",
			role:     "witness",
			prefix:   "gt",
			want:     "gt-witness",
		},
		{
			pattern:  "{prefix}-{name}",
			townRoot: "/Users/stevey/gt",
			rig:      "gastown",
			name:     "toast",
			prefix:   "gt",
			want:     "gt-toast",
		},
		{
			pattern:  "{town}/{rig}/polecats/{name}",
			townRoot: "/Users/stevey/gt",
			rig:      "gastown",
			name:     "toast",
			want:     "/Users/stevey/gt/gastown/polecats/toast",
		},
		{
			pattern:  "{town}/{rig}/refinery/rig",
			townRoot: "/Users/stevey/gt",
			rig:      "gastown",
			want:     "/Users/stevey/gt/gastown/refinery/rig",
		},
		{
			pattern:  "export GT_ROLE={role} GT_RIG={rig} BD_ACTOR={rig}/polecats/{name}",
			townRoot: "/Users/stevey/gt",
			rig:      "gastown",
			name:     "toast",
			role:     "polecat",
			want:     "export GT_ROLE=polecat GT_RIG=gastown BD_ACTOR=gastown/polecats/toast",
		},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			got := ExpandRolePattern(tt.pattern, tt.townRoot, tt.rig, tt.name, tt.role, tt.prefix)
			if got != tt.want {
				t.Errorf("ExpandRolePattern() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFormatRoleConfig tests formatting role config to string.
func TestFormatRoleConfig(t *testing.T) {
	tests := []struct {
		name   string
		config *RoleConfig
		want   string
	}{
		{
			name:   "nil config",
			config: nil,
			want:   "",
		},
		{
			name:   "empty config",
			config: &RoleConfig{EnvVars: map[string]string{}},
			want:   "",
		},
		{
			name: "all fields",
			config: &RoleConfig{
				SessionPattern: "gt-{rig}-{name}",
				WorkDirPattern: "{town}/{rig}/polecats/{name}",
				NeedsPreSync:   true,
				StartCommand:   "exec claude",
				EnvVars:        map[string]string{},
			},
			want: `session_pattern: gt-{rig}-{name}
work_dir_pattern: {town}/{rig}/polecats/{name}
needs_pre_sync: true
start_command: exec claude`,
		},
		{
			name: "only session pattern",
			config: &RoleConfig{
				SessionPattern: "gt-mayor",
				EnvVars:        map[string]string{},
			},
			want: "session_pattern: gt-mayor",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatRoleConfig(tt.config)
			if got != tt.want {
				t.Errorf("FormatRoleConfig() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

// TestRoleConfigRoundTrip tests that parse/format round-trips correctly.
func TestRoleConfigRoundTrip(t *testing.T) {
	original := &RoleConfig{
		SessionPattern: "gt-{rig}-{name}",
		WorkDirPattern: "{town}/{rig}/polecats/{name}",
		NeedsPreSync:   true,
		StartCommand:   "exec claude --dangerously-skip-permissions",
		EnvVars:        map[string]string{}, // Can't round-trip env vars due to order
	}

	// Format to string
	formatted := FormatRoleConfig(original)

	// Parse back
	parsed := ParseRoleConfig(formatted)

	if parsed == nil {
		t.Fatal("round-trip parse returned nil")
	}

	if parsed.SessionPattern != original.SessionPattern {
		t.Errorf("round-trip SessionPattern = %q, want %q", parsed.SessionPattern, original.SessionPattern)
	}
	if parsed.WorkDirPattern != original.WorkDirPattern {
		t.Errorf("round-trip WorkDirPattern = %q, want %q", parsed.WorkDirPattern, original.WorkDirPattern)
	}
	if parsed.NeedsPreSync != original.NeedsPreSync {
		t.Errorf("round-trip NeedsPreSync = %v, want %v", parsed.NeedsPreSync, original.NeedsPreSync)
	}
	if parsed.StartCommand != original.StartCommand {
		t.Errorf("round-trip StartCommand = %q, want %q", parsed.StartCommand, original.StartCommand)
	}
}

// TestParseRoleConfigWispTTLs tests parsing wisp_ttl_* fields from role config.
func TestParseRoleConfigWispTTLs(t *testing.T) {
	tests := []struct {
		name        string
		description string
		wantNil     bool
		wantTTLs    map[string]string
	}{
		{
			name: "single wisp TTL",
			description: `session_pattern: gt-{rig}-{name}
wisp_ttl_patrol: 48h`,
			wantTTLs: map[string]string{"patrol": "48h"},
		},
		{
			name: "multiple wisp TTLs",
			description: `wisp_ttl_patrol: 48h
wisp_ttl_error: 336h
wisp_ttl_gc_report: 24h`,
			wantTTLs: map[string]string{
				"patrol":    "48h",
				"error":     "336h",
				"gc_report": "24h",
			},
		},
		{
			name: "hyphenated key format",
			description: `wisp-ttl-patrol: 48h
wisp-ttl-error: 336h`,
			wantTTLs: map[string]string{
				"patrol": "48h",
				"error":  "336h",
			},
		},
		{
			name: "mixed with other role config fields",
			description: `session_pattern: gt-{rig}-{name}
work_dir_pattern: {town}/{rig}
wisp_ttl_patrol: 48h
ping_timeout: 30s
wisp_ttl_error: 336h`,
			wantTTLs: map[string]string{
				"patrol": "48h",
				"error":  "336h",
			},
		},
		{
			name:        "wisp TTL only (no other fields)",
			description: `wisp_ttl_patrol: 24h`,
			wantTTLs:    map[string]string{"patrol": "24h"},
		},
		{
			name:        "no wisp TTLs present",
			description: `session_pattern: gt-{rig}-{name}`,
			wantTTLs:    map[string]string{},
		},
		{
			name: "case insensitive keys",
			description: `WISP_TTL_PATROL: 48h
Wisp_TTL_Error: 336h`,
			wantTTLs: map[string]string{
				"patrol": "48h",
				"error":  "336h",
			},
		},
		{
			name:        "wisp TTL with default type",
			description: `wisp_ttl_default: 168h`,
			wantTTLs:    map[string]string{"default": "168h"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := ParseRoleConfig(tt.description)

			if tt.wantNil {
				if config != nil {
					t.Errorf("ParseRoleConfig() = %+v, want nil", config)
				}
				return
			}

			if config == nil {
				t.Fatal("ParseRoleConfig() = nil, want non-nil")
			}

			if len(config.WispTTLs) != len(tt.wantTTLs) {
				t.Errorf("WispTTLs len = %d, want %d\ngot: %v\nwant: %v",
					len(config.WispTTLs), len(tt.wantTTLs), config.WispTTLs, tt.wantTTLs)
			}
			for k, v := range tt.wantTTLs {
				if config.WispTTLs[k] != v {
					t.Errorf("WispTTLs[%q] = %q, want %q", k, config.WispTTLs[k], v)
				}
			}
		})
	}
}

// TestFormatRoleConfigWispTTLs tests that wisp TTLs are included in format output.
func TestFormatRoleConfigWispTTLs(t *testing.T) {
	config := &RoleConfig{
		SessionPattern: "gt-{rig}-{name}",
		EnvVars:        map[string]string{},
		WispTTLs: map[string]string{
			"patrol": "48h",
			"error":  "336h",
		},
	}

	formatted := FormatRoleConfig(config)

	if !strings.Contains(formatted, "wisp_ttl_error: 336h") {
		t.Errorf("formatted output missing wisp_ttl_error, got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "wisp_ttl_patrol: 48h") {
		t.Errorf("formatted output missing wisp_ttl_patrol, got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "session_pattern: gt-{rig}-{name}") {
		t.Errorf("formatted output missing session_pattern, got:\n%s", formatted)
	}
}

// TestRoleConfigWispTTLRoundTrip tests that wisp TTLs survive parse/format round-trip.
func TestRoleConfigWispTTLRoundTrip(t *testing.T) {
	original := &RoleConfig{
		SessionPattern: "gt-{rig}-{name}",
		EnvVars:        map[string]string{},
		WispTTLs: map[string]string{
			"patrol":    "48h",
			"error":     "336h",
			"gc_report": "24h",
		},
	}

	formatted := FormatRoleConfig(original)
	parsed := ParseRoleConfig(formatted)

	if parsed == nil {
		t.Fatal("round-trip parse returned nil")
	}

	if len(parsed.WispTTLs) != len(original.WispTTLs) {
		t.Fatalf("round-trip WispTTLs len = %d, want %d", len(parsed.WispTTLs), len(original.WispTTLs))
	}
	for k, v := range original.WispTTLs {
		if parsed.WispTTLs[k] != v {
			t.Errorf("round-trip WispTTLs[%q] = %q, want %q", k, parsed.WispTTLs[k], v)
		}
	}
}

// TestParseWispTTLKey tests the wisp TTL key parser directly.
func TestParseWispTTLKey(t *testing.T) {
	tests := []struct {
		key      string
		wantType string
		wantOK   bool
	}{
		{"wisp_ttl_patrol", "patrol", true},
		{"wisp_ttl_error", "error", true},
		{"wisp_ttl_gc_report", "gc_report", true},
		{"wisp-ttl-patrol", "patrol", true},
		{"wisp-ttl-error", "error", true},
		{"wispttlpatrol", "patrol", true},
		{"wisp_ttl_", "", false}, // empty type
		{"wisp-ttl-", "", false}, // empty type
		{"session_pattern", "", false},
		{"wisp_patrol", "", false},
		{"ttl_patrol", "", false},
		{"", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			gotType, gotOK := ParseWispTTLKey(tt.key)
			if gotOK != tt.wantOK {
				t.Errorf("ParseWispTTLKey(%q) ok = %v, want %v", tt.key, gotOK, tt.wantOK)
			}
			if gotType != tt.wantType {
				t.Errorf("ParseWispTTLKey(%q) type = %q, want %q", tt.key, gotType, tt.wantType)
			}
		})
	}
}

// TestDelegationStruct tests the Delegation struct serialization.
func TestDelegationStruct(t *testing.T) {
	tests := []struct {
		name       string
		delegation Delegation
		wantJSON   string
	}{
		{
			name: "full delegation",
			delegation: Delegation{
				Parent:      "hop://accenture.com/eng/proj-123/task-a",
				Child:       "hop://alice@example.com/main-town/gastown/gt-xyz",
				DelegatedBy: "hop://accenture.com",
				DelegatedTo: "hop://alice@example.com",
				Terms: &DelegationTerms{
					Portion:     "backend-api",
					Deadline:    "2025-06-01",
					CreditShare: 80,
				},
				CreatedAt: "2025-01-15T10:00:00Z",
			},
			wantJSON: `{"parent":"hop://accenture.com/eng/proj-123/task-a","child":"hop://alice@example.com/main-town/gastown/gt-xyz","delegated_by":"hop://accenture.com","delegated_to":"hop://alice@example.com","terms":{"portion":"backend-api","deadline":"2025-06-01","credit_share":80},"created_at":"2025-01-15T10:00:00Z"}`,
		},
		{
			name: "minimal delegation",
			delegation: Delegation{
				Parent:      "gt-abc",
				Child:       "gt-xyz",
				DelegatedBy: "steve",
				DelegatedTo: "alice",
			},
			wantJSON: `{"parent":"gt-abc","child":"gt-xyz","delegated_by":"steve","delegated_to":"alice"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.delegation)
			if err != nil {
				t.Fatalf("json.Marshal failed: %v", err)
			}
			if string(got) != tt.wantJSON {
				t.Errorf("json.Marshal = %s, want %s", string(got), tt.wantJSON)
			}

			// Test round-trip
			var parsed Delegation
			if err := json.Unmarshal(got, &parsed); err != nil {
				t.Fatalf("json.Unmarshal failed: %v", err)
			}
			if parsed.Parent != tt.delegation.Parent {
				t.Errorf("parsed.Parent = %s, want %s", parsed.Parent, tt.delegation.Parent)
			}
			if parsed.Child != tt.delegation.Child {
				t.Errorf("parsed.Child = %s, want %s", parsed.Child, tt.delegation.Child)
			}
			if parsed.DelegatedBy != tt.delegation.DelegatedBy {
				t.Errorf("parsed.DelegatedBy = %s, want %s", parsed.DelegatedBy, tt.delegation.DelegatedBy)
			}
			if parsed.DelegatedTo != tt.delegation.DelegatedTo {
				t.Errorf("parsed.DelegatedTo = %s, want %s", parsed.DelegatedTo, tt.delegation.DelegatedTo)
			}
		})
	}
}

// TestDelegationTerms tests the DelegationTerms struct.
func TestDelegationTerms(t *testing.T) {
	terms := &DelegationTerms{
		Portion:            "frontend",
		Deadline:           "2025-03-15",
		AcceptanceCriteria: "All tests passing, code reviewed",
		CreditShare:        70,
	}

	got, err := json.Marshal(terms)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var parsed DelegationTerms
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if parsed.Portion != terms.Portion {
		t.Errorf("parsed.Portion = %s, want %s", parsed.Portion, terms.Portion)
	}
	if parsed.Deadline != terms.Deadline {
		t.Errorf("parsed.Deadline = %s, want %s", parsed.Deadline, terms.Deadline)
	}
	if parsed.AcceptanceCriteria != terms.AcceptanceCriteria {
		t.Errorf("parsed.AcceptanceCriteria = %s, want %s", parsed.AcceptanceCriteria, terms.AcceptanceCriteria)
	}
	if parsed.CreditShare != terms.CreditShare {
		t.Errorf("parsed.CreditShare = %d, want %d", parsed.CreditShare, terms.CreditShare)
	}
}

// TestSetupRedirect tests the beads redirect setup for worktrees.
func TestSetupRedirect(t *testing.T) {
	t.Run("rig with own DB redirects to rig-level beads", func(t *testing.T) {
		// When rig has its own dolt_database in metadata.json, crew must
		// redirect to rig-level .beads (not town-level) to see correct prefix.
		townRoot := t.TempDir()
		townBeads := filepath.Join(townRoot, ".beads")
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		crewPath := filepath.Join(rigRoot, "crew", "max")

		// Create both town-level and rig-level beads
		if err := os.MkdirAll(filepath.Join(townBeads, "dolt"), 0755); err != nil {
			t.Fatalf("mkdir town beads: %v", err)
		}
		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		// Rig has its own database (e.g., laneassist with lc- prefix)
		meta := []byte(`{"dolt_database":"testrig","backend":"dolt"}`)
		if err := os.WriteFile(filepath.Join(rigBeads, "metadata.json"), meta, 0644); err != nil {
			t.Fatalf("write metadata: %v", err)
		}
		if err := os.MkdirAll(crewPath, 0755); err != nil {
			t.Fatalf("mkdir crew: %v", err)
		}

		if err := SetupRedirect(townRoot, crewPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		redirectPath := filepath.Join(crewPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		// 2 levels up to rig root: crew/max -> testrig, then .beads
		want := "../../.beads\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q", string(content), want)
		}

		// Verify redirect resolves to rig-level, NOT town-level
		resolved := ResolveBeadsDir(crewPath)
		if resolved != rigBeads {
			t.Errorf("resolved = %q, want %q (rig-level)", resolved, rigBeads)
		}
	})

	t.Run("rig without own DB redirects to town-level beads", func(t *testing.T) {
		// When rig has no own database, crew should use town-level .beads.
		townRoot := t.TempDir()
		townBeads := filepath.Join(townRoot, ".beads")
		rigRoot := filepath.Join(townRoot, "testrig")
		crewPath := filepath.Join(rigRoot, "crew", "max")

		// Create town-level beads with dolt DB
		if err := os.MkdirAll(filepath.Join(townBeads, "dolt"), 0755); err != nil {
			t.Fatalf("mkdir town beads: %v", err)
		}
		if err := os.MkdirAll(crewPath, 0755); err != nil {
			t.Fatalf("mkdir crew: %v", err)
		}

		if err := SetupRedirect(townRoot, crewPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		redirectPath := filepath.Join(crewPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		// 3 levels up: crew/max -> testrig -> townRoot, then .beads
		want := "../../../.beads\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q", string(content), want)
		}

		// Verify redirect resolves to town-level
		resolved := ResolveBeadsDir(crewPath)
		if resolved != townBeads {
			t.Errorf("resolved = %q, want %q", resolved, townBeads)
		}
	})

	t.Run("crew worktree falls back to rig-level beads", func(t *testing.T) {
		// When neither rig metadata nor town-level .beads exists, fall back to rig-level (2 levels up).
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		crewPath := filepath.Join(rigRoot, "crew", "max")

		// Create rig-level beads only (no town-level, no metadata.json)
		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		if err := os.MkdirAll(crewPath, 0755); err != nil {
			t.Fatalf("mkdir crew: %v", err)
		}

		// Run SetupRedirect
		if err := SetupRedirect(townRoot, crewPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		// Verify redirect was created
		redirectPath := filepath.Join(crewPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		want := "../../.beads\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q", string(content), want)
		}
	})

	t.Run("crew worktree with tracked beads", func(t *testing.T) {
		// Setup: town/rig/.beads/redirect -> mayor/rig/.beads (tracked).
		// Runtime metadata may coexist with the rig redirect; it must not cause
		// SetupRedirect to create a bd-incompatible redirect chain.
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		mayorRigBeads := filepath.Join(rigRoot, "mayor", "rig", ".beads")
		crewPath := filepath.Join(rigRoot, "crew", "max")

		// Create rig structure with tracked beads
		if err := os.MkdirAll(mayorRigBeads, 0755); err != nil {
			t.Fatalf("mkdir mayor/rig beads: %v", err)
		}
		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		// Create rig-level redirect to mayor/rig/.beads
		if err := os.WriteFile(filepath.Join(rigBeads, "redirect"), []byte("mayor/rig/.beads\n"), 0644); err != nil {
			t.Fatalf("write rig redirect: %v", err)
		}
		if err := os.WriteFile(filepath.Join(rigBeads, "metadata.json"), []byte(`{"dolt_database":"hq","backend":"dolt"}`), 0644); err != nil {
			t.Fatalf("write rig metadata: %v", err)
		}
		if err := os.MkdirAll(crewPath, 0755); err != nil {
			t.Fatalf("mkdir crew: %v", err)
		}

		// Run SetupRedirect
		if err := SetupRedirect(townRoot, crewPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		// Verify redirect goes directly to mayor/rig/.beads (no chain - bd CLI doesn't support chains)
		redirectPath := filepath.Join(crewPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		want := "../../mayor/rig/.beads\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q", string(content), want)
		}

		// Verify redirect resolves correctly
		resolved := ResolveBeadsDir(crewPath)
		// crew/max -> ../../mayor/rig/.beads (direct, no chain)
		if resolved != mayorRigBeads {
			t.Errorf("resolved = %q, want %q", resolved, mayorRigBeads)
		}
	})

	t.Run("crew worktree with absolute rig redirect", func(t *testing.T) {
		// Setup: rig/.beads/redirect contains an absolute path
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		crewPath := filepath.Join(rigRoot, "crew", "max")

		// Create an absolute target beads directory (simulates a canonical .beads outside the town)
		absTarget := filepath.Join(t.TempDir(), "canonical", ".beads")
		if err := os.MkdirAll(absTarget, 0755); err != nil {
			t.Fatalf("mkdir abs target: %v", err)
		}

		// Create rig structure with absolute redirect
		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		if err := os.WriteFile(filepath.Join(rigBeads, "redirect"), []byte(absTarget+"\n"), 0644); err != nil {
			t.Fatalf("write rig redirect: %v", err)
		}
		if err := os.MkdirAll(crewPath, 0755); err != nil {
			t.Fatalf("mkdir crew: %v", err)
		}

		// Run SetupRedirect
		if err := SetupRedirect(townRoot, crewPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		// Verify redirect is the absolute path (not upPath + absolutePath)
		redirectPath := filepath.Join(crewPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		want := absTarget + "\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q (absolute path should be passed through)", string(content), want)
		}

		// Verify redirect resolves correctly
		resolved := ResolveBeadsDir(crewPath)
		if resolved != absTarget {
			t.Errorf("resolved = %q, want %q", resolved, absTarget)
		}
	})

	t.Run("polecat worktree", func(t *testing.T) {
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		polecatPath := filepath.Join(rigRoot, "polecats", "worker1")

		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		if err := os.MkdirAll(polecatPath, 0755); err != nil {
			t.Fatalf("mkdir polecat: %v", err)
		}

		if err := SetupRedirect(townRoot, polecatPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		redirectPath := filepath.Join(polecatPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		want := "../../.beads\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q", string(content), want)
		}
	})

	t.Run("refinery worktree", func(t *testing.T) {
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		refineryPath := filepath.Join(rigRoot, "refinery", "rig")

		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		if err := os.MkdirAll(refineryPath, 0755); err != nil {
			t.Fatalf("mkdir refinery: %v", err)
		}

		if err := SetupRedirect(townRoot, refineryPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		redirectPath := filepath.Join(refineryPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		want := "../../.beads\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q", string(content), want)
		}
	})

	t.Run("cleans runtime files but preserves config files", func(t *testing.T) {
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		crewPath := filepath.Join(rigRoot, "crew", "max")
		crewBeads := filepath.Join(crewPath, ".beads")

		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		// Simulate worktree with both runtime and tracked files
		if err := os.MkdirAll(crewBeads, 0755); err != nil {
			t.Fatalf("mkdir crew beads: %v", err)
		}
		// Runtime files (should be removed)
		if err := os.WriteFile(filepath.Join(crewBeads, "daemon.lock"), []byte("1234"), 0644); err != nil {
			t.Fatalf("write daemon.lock: %v", err)
		}
		// Local beads metadata is per-machine configuration and must survive startup.
		if err := os.WriteFile(filepath.Join(crewBeads, "metadata.json"), []byte("{}"), 0644); err != nil {
			t.Fatalf("write metadata.json: %v", err)
		}
		// Config files (should be preserved)
		if err := os.WriteFile(filepath.Join(crewBeads, "config.yaml"), []byte("prefix: test"), 0644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		if err := os.WriteFile(filepath.Join(crewBeads, "README.md"), []byte("# Beads"), 0644); err != nil {
			t.Fatalf("write README: %v", err)
		}

		if err := SetupRedirect(townRoot, crewPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		// Verify runtime files were cleaned up
		if _, err := os.Stat(filepath.Join(crewBeads, "daemon.lock")); !os.IsNotExist(err) {
			t.Error("daemon.lock should have been removed")
		}
		if _, err := os.Stat(filepath.Join(crewBeads, "metadata.json")); err != nil {
			t.Errorf("metadata.json should have been preserved: %v", err)
		}

		// Verify config files were preserved
		if _, err := os.Stat(filepath.Join(crewBeads, "config.yaml")); err != nil {
			t.Errorf("config.yaml should have been preserved: %v", err)
		}
		if _, err := os.Stat(filepath.Join(crewBeads, "README.md")); err != nil {
			t.Errorf("README.md should have been preserved: %v", err)
		}

		// Verify redirect was created
		redirectPath := filepath.Join(crewBeads, "redirect")
		if _, err := os.Stat(redirectPath); err != nil {
			t.Errorf("redirect file should exist: %v", err)
		}
	})

	t.Run("rejects mayor/rig canonical location", func(t *testing.T) {
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		mayorRigPath := filepath.Join(rigRoot, "mayor", "rig")

		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		if err := os.MkdirAll(mayorRigPath, 0755); err != nil {
			t.Fatalf("mkdir mayor/rig: %v", err)
		}

		err := SetupRedirect(townRoot, mayorRigPath)
		if err == nil {
			t.Error("SetupRedirect should reject mayor/rig location")
		}
		if err != nil && !strings.Contains(err.Error(), "canonical") {
			t.Errorf("error should mention canonical location, got: %v", err)
		}
	})

	t.Run("rejects path too shallow", func(t *testing.T) {
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")

		if err := os.MkdirAll(rigRoot, 0755); err != nil {
			t.Fatalf("mkdir rig: %v", err)
		}

		err := SetupRedirect(townRoot, rigRoot)
		if err == nil {
			t.Error("SetupRedirect should reject rig root (too shallow)")
		}
	})

	t.Run("rejects town root without mutating beads config", func(t *testing.T) {
		townRoot := t.TempDir()
		townBeads := filepath.Join(townRoot, ".beads")
		metadata := []byte(`{"backend":"dolt","dolt_database":"hq"}`)

		if err := os.MkdirAll(townBeads, 0755); err != nil {
			t.Fatalf("mkdir town beads: %v", err)
		}
		if err := os.WriteFile(filepath.Join(townBeads, "metadata.json"), metadata, 0644); err != nil {
			t.Fatalf("write metadata: %v", err)
		}

		err := SetupRedirect(townRoot, townRoot)
		if err == nil {
			t.Fatal("SetupRedirect should reject town root")
		}
		if _, err := os.Stat(filepath.Join(townBeads, "redirect")); !os.IsNotExist(err) {
			t.Fatalf("town root redirect should not have been created, stat err=%v", err)
		}
		got, err := os.ReadFile(filepath.Join(townBeads, "metadata.json"))
		if err != nil {
			t.Fatalf("metadata.json should be preserved: %v", err)
		}
		if string(got) != string(metadata) {
			t.Fatalf("metadata changed: got %q want %q", got, metadata)
		}
	})

	t.Run("rejects worktree outside town root without mutating beads config", func(t *testing.T) {
		townRoot := t.TempDir()
		outsideRoot := t.TempDir()
		outsideWorktree := filepath.Join(outsideRoot, "crew", "max")
		outsideBeads := filepath.Join(outsideWorktree, ".beads")
		metadata := []byte(`{"backend":"dolt","dolt_database":"hq"}`)

		if err := os.MkdirAll(outsideBeads, 0755); err != nil {
			t.Fatalf("mkdir outside beads: %v", err)
		}
		if err := os.WriteFile(filepath.Join(outsideBeads, "metadata.json"), metadata, 0644); err != nil {
			t.Fatalf("write metadata: %v", err)
		}

		err := SetupRedirect(townRoot, outsideWorktree)
		if err == nil {
			t.Fatal("SetupRedirect should reject worktree outside town root")
		}
		if _, err := os.Stat(filepath.Join(outsideBeads, "redirect")); !os.IsNotExist(err) {
			t.Fatalf("outside redirect should not have been created, stat err=%v", err)
		}
		got, err := os.ReadFile(filepath.Join(outsideBeads, "metadata.json"))
		if err != nil {
			t.Fatalf("metadata.json should be preserved: %v", err)
		}
		if string(got) != string(metadata) {
			t.Fatalf("metadata changed: got %q want %q", got, metadata)
		}
	})

	t.Run("fails if rig beads missing", func(t *testing.T) {
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		crewPath := filepath.Join(rigRoot, "crew", "max")

		// No rig/.beads or mayor/rig/.beads created
		if err := os.MkdirAll(crewPath, 0755); err != nil {
			t.Fatalf("mkdir crew: %v", err)
		}

		err := SetupRedirect(townRoot, crewPath)
		if err == nil {
			t.Error("SetupRedirect should fail if rig .beads missing")
		}
	})

	t.Run("crew worktree with rig beads but no database", func(t *testing.T) {
		// Setup: rig/.beads exists (has metadata.json) but no actual database.
		// This is the dolt architecture where rig/.beads has metadata only and
		// the actual dolt DB lives at mayor/rig/.beads/dolt/.
		// The redirect should point to mayor/rig/.beads, not rig/.beads.
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		mayorRigBeads := filepath.Join(rigRoot, "mayor", "rig", ".beads")
		crewPath := filepath.Join(rigRoot, "crew", "max")

		// Create rig/.beads with metadata but NO database (no dolt/)
		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		if err := os.WriteFile(filepath.Join(rigBeads, "metadata.json"),
			[]byte(`{"database":"dolt","backend":"dolt","dolt_mode":"embedded"}`), 0644); err != nil {
			t.Fatalf("write metadata: %v", err)
		}
		// Create mayor/rig/.beads with dolt DB marker
		doltDir := filepath.Join(mayorRigBeads, "dolt")
		if err := os.MkdirAll(doltDir, 0755); err != nil {
			t.Fatalf("mkdir mayor dolt: %v", err)
		}
		if err := os.MkdirAll(crewPath, 0755); err != nil {
			t.Fatalf("mkdir crew: %v", err)
		}

		// Run SetupRedirect - should detect no DB at rig/.beads and fall back to mayor/rig/.beads
		if err := SetupRedirect(townRoot, crewPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		// Verify redirect points to mayor/rig/.beads (not rig/.beads)
		redirectPath := filepath.Join(crewPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		want := "../../mayor/rig/.beads\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q", string(content), want)
		}

		// Verify redirect resolves correctly
		resolved := ResolveBeadsDir(crewPath)
		if resolved != mayorRigBeads {
			t.Errorf("resolved = %q, want %q", resolved, mayorRigBeads)
		}
	})

	t.Run("crew worktree with mayor/rig beads only", func(t *testing.T) {
		// Setup: no rig/.beads, only mayor/rig/.beads exists
		// This is the tracked beads architecture where rig root has no .beads directory
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		mayorRigBeads := filepath.Join(rigRoot, "mayor", "rig", ".beads")
		crewPath := filepath.Join(rigRoot, "crew", "max")

		// Create only mayor/rig/.beads (no rig/.beads)
		if err := os.MkdirAll(mayorRigBeads, 0755); err != nil {
			t.Fatalf("mkdir mayor/rig beads: %v", err)
		}
		if err := os.MkdirAll(crewPath, 0755); err != nil {
			t.Fatalf("mkdir crew: %v", err)
		}

		// Run SetupRedirect - should succeed and point to mayor/rig/.beads
		if err := SetupRedirect(townRoot, crewPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		// Verify redirect points to mayor/rig/.beads
		redirectPath := filepath.Join(crewPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		want := "../../mayor/rig/.beads\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q", string(content), want)
		}

		// Verify redirect resolves correctly
		resolved := ResolveBeadsDir(crewPath)
		if resolved != mayorRigBeads {
			t.Errorf("resolved = %q, want %q", resolved, mayorRigBeads)
		}
	})

	t.Run("handles stale .beads file (not directory)", func(t *testing.T) {
		// Edge case: .beads exists as a file instead of directory
		// This can happen with unusual clone state or failed operations
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		crewPath := filepath.Join(rigRoot, "crew", "max")

		// Create rig structure
		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		if err := os.MkdirAll(crewPath, 0755); err != nil {
			t.Fatalf("mkdir crew: %v", err)
		}

		// Create .beads as a FILE (not directory) - simulating stale state
		staleBeadsFile := filepath.Join(crewPath, ".beads")
		if err := os.WriteFile(staleBeadsFile, []byte("stale content"), 0644); err != nil {
			t.Fatalf("write stale .beads file: %v", err)
		}

		// SetupRedirect should remove the file and create the directory
		if err := SetupRedirect(townRoot, crewPath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		// Verify .beads is now a directory
		info, err := os.Stat(staleBeadsFile)
		if err != nil {
			t.Fatalf("stat .beads: %v", err)
		}
		if !info.IsDir() {
			t.Errorf(".beads should be a directory, but is a file")
		}

		// Verify redirect was created
		redirectPath := filepath.Join(crewPath, ".beads", "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}

		want := "../../.beads\n"
		if string(content) != want {
			t.Errorf("redirect content = %q, want %q", string(content), want)
		}
	})
}

// TestResetAgentBeadForReuse_NukeRespawnCycle tests the preferred nuke→respawn
// lifecycle using ResetAgentBeadForReuse (gt-14b8o fix). This keeps the bead open
// with agent_state="nuked", avoiding the close/reopen cycle
// that fails on Dolt backends.
func TestResetAgentBeadForReuse_NukeRespawnCycle(t *testing.T) {
	t.Skip("bd CLI 0.47.2 bug: database writes don't commit")

	tmpDir := t.TempDir()
	bd := NewIsolated(tmpDir)
	if err := bd.Init("test"); err != nil {
		t.Fatalf("bd init: %v", err)
	}

	agentID := "test-testrig-polecat-reset"

	// Spawn 1: Create agent bead
	issue1, err := bd.CreateOrReopenAgentBead(agentID, agentID, &AgentFields{
		RoleType:   "polecat",
		Rig:        "testrig",
		AgentState: "spawning",
		HookBead:   "test-task-1",
	})
	if err != nil {
		t.Fatalf("Spawn 1: %v", err)
	}
	if issue1.Status != "open" {
		t.Errorf("Spawn 1: status = %q, want 'open'", issue1.Status)
	}

	// Nuke 1: Reset for reuse (bead stays open with cleared fields)
	err = bd.ResetAgentBeadForReuse(agentID, "polecat nuked")
	if err != nil {
		t.Fatalf("Nuke 1 - ResetAgentBeadForReuse: %v", err)
	}

	// Verify bead is still open with cleared fields
	nukedIssue, err := bd.Show(agentID)
	if err != nil {
		t.Fatalf("Show after nuke: %v", err)
	}
	if nukedIssue.Status != "open" {
		t.Errorf("After nuke: status = %q, want 'open' (bead should stay open)", nukedIssue.Status)
	}
	nukedFields := ParseAgentFields(nukedIssue.Description)
	if nukedFields.AgentState != "nuked" {
		t.Errorf("After nuke: agent_state = %q, want 'nuked'", nukedFields.AgentState)
	}
	if nukedFields.HookBead != "" {
		t.Errorf("After nuke: hook_bead = %q, want empty", nukedFields.HookBead)
	}

	// Spawn 2: CreateOrReopenAgentBead should detect open bead and update it
	issue2, err := bd.CreateOrReopenAgentBead(agentID, agentID, &AgentFields{
		RoleType:   "polecat",
		Rig:        "testrig",
		AgentState: "spawning",
		HookBead:   "test-task-2",
	})
	if err != nil {
		t.Fatalf("Spawn 2: %v", err)
	}
	if issue2.Status != "open" {
		t.Errorf("Spawn 2: status = %q, want 'open'", issue2.Status)
	}
	fields := ParseAgentFields(issue2.Description)
	if fields.HookBead != "test-task-2" {
		t.Errorf("Spawn 2: hook_bead = %q, want 'test-task-2'", fields.HookBead)
	}
	if fields.AgentState != "spawning" {
		t.Errorf("Spawn 2: agent_state = %q, want 'spawning'", fields.AgentState)
	}

	// Nuke 2: Reset again
	err = bd.ResetAgentBeadForReuse(agentID, "polecat nuked again")
	if err != nil {
		t.Fatalf("Nuke 2: %v", err)
	}

	// Spawn 3: Should still work
	issue3, err := bd.CreateOrReopenAgentBead(agentID, agentID, &AgentFields{
		RoleType:   "polecat",
		Rig:        "testrig",
		AgentState: "spawning",
		HookBead:   "test-task-3",
	})
	if err != nil {
		t.Fatalf("Spawn 3: %v", err)
	}
	fields = ParseAgentFields(issue3.Description)
	if fields.HookBead != "test-task-3" {
		t.Errorf("Spawn 3: hook_bead = %q, want 'test-task-3'", fields.HookBead)
	}

	t.Log("LIFECYCLE TEST PASSED: spawn → reset → respawn works without close/reopen")
}

// TestIsAgentBead verifies the IsAgentBead function correctly identifies agent
// beads by checking both the gt:agent label (preferred) and the legacy type field.
func TestIsAgentBead(t *testing.T) {
	tests := []struct {
		name  string
		issue *Issue
		want  bool
	}{
		{
			name:  "nil issue",
			issue: nil,
			want:  false,
		},
		{
			name: "agent with legacy type",
			issue: &Issue{
				ID:     "gt-gastown-polecat-toast",
				Type:   "agent",
				Labels: []string{},
			},
			want: true,
		},
		{
			name: "agent with gt:agent label",
			issue: &Issue{
				ID:     "gt-gastown-polecat-toast",
				Type:   "task",
				Labels: []string{"gt:agent"},
			},
			want: true,
		},
		{
			name: "agent with both type and label",
			issue: &Issue{
				ID:     "gt-gastown-polecat-toast",
				Type:   "agent",
				Labels: []string{"gt:agent", "other-label"},
			},
			want: true,
		},
		{
			name: "not an agent - task type without label",
			issue: &Issue{
				ID:     "gt-abc123",
				Type:   "task",
				Labels: []string{},
			},
			want: false,
		},
		{
			name: "not an agent - bug type with other labels",
			issue: &Issue{
				ID:     "gt-xyz456",
				Type:   "bug",
				Labels: []string{"priority-high", "blocked"},
			},
			want: false,
		},
		{
			name: "agent with gt:agent label and other labels",
			issue: &Issue{
				ID:     "gt-gastown-witness",
				Type:   "task",
				Labels: []string{"priority-high", "gt:agent", "status-running"},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsAgentBead(tt.issue)
			if got != tt.want {
				t.Errorf("IsAgentBead() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestFilterBeadsEnv_NilInput verifies filterBeadsEnv does not panic on nil.
func TestFilterBeadsEnv_NilInput(t *testing.T) {
	got := filterBeadsEnv(nil)
	if got == nil {
		t.Fatal("filterBeadsEnv(nil) returned nil, want empty slice")
	}
	if len(got) != 0 {
		t.Errorf("filterBeadsEnv(nil) returned %d items, want 0", len(got))
	}
}

// TestFilterBeadsEnv_EmptyInput verifies filterBeadsEnv on empty slice.
func TestFilterBeadsEnv_EmptyInput(t *testing.T) {
	got := filterBeadsEnv([]string{})
	if len(got) != 0 {
		t.Errorf("filterBeadsEnv([]) returned %d items, want 0", len(got))
	}
}

// TestFilterBeadsEnv_PreservesDoltPortVars verifies that GT_DOLT_PORT and
// BEADS_DOLT_PORT are preserved by filterBeadsEnv even though BEADS_* vars
// are otherwise stripped. Tests need these to reach test Dolt servers.
func TestFilterBeadsEnv_PreservesDoltPortVars(t *testing.T) {
	environ := []string{
		"BD_ACTOR=test-actor",
		"BEADS_DIR=/tmp/beads",
		"BEADS_DB=/tmp/beads.db",
		"BEADS_DOLT_PORT=13306",
		"GT_DOLT_PORT=13307",
		"GT_ROOT=/tmp/gt",
		"HOME=/home/test",
		"PATH=/usr/bin",
	}
	got := filterBeadsEnv(environ)
	want := []string{
		"BEADS_DOLT_PORT=13306",
		"GT_DOLT_PORT=13307",
		"PATH=/usr/bin",
	}
	if len(got) != len(want) {
		t.Fatalf("filterBeadsEnv returned %d items, want %d\n  got:  %v\n  want: %v",
			len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestNewIsolatedWithPort verifies the constructor sets serverPort.
func TestNewIsolatedWithPort(t *testing.T) {
	b := NewIsolatedWithPort("/tmp/test", 13307)
	if !b.isolated {
		t.Error("expected isolated=true")
	}
	if b.serverPort != 13307 {
		t.Errorf("serverPort = %d, want 13307", b.serverPort)
	}
}

func TestIsolatedWithPortOverridesInheritedDoltEnv(t *testing.T) {
	t.Setenv("GT_DOLT_PORT", "3307")
	t.Setenv("BEADS_DOLT_PORT", "3307")
	t.Setenv("BEADS_DOLT_AUTO_START", "1")

	b := NewIsolatedWithPort(t.TempDir(), 19999)
	for _, env := range []struct {
		name string
		got  []string
	}{
		{name: "run", got: b.buildRunEnv()},
		{name: "routing", got: b.buildRoutingEnv()},
	} {
		if got := countEnvPrefix(env.got, "GT_DOLT_PORT="); got != 1 {
			t.Fatalf("%s env GT_DOLT_PORT count = %d, want 1: %v", env.name, got, env.got)
		}
		if got := countEnvPrefix(env.got, "BEADS_DOLT_PORT="); got != 1 {
			t.Fatalf("%s env BEADS_DOLT_PORT count = %d, want 1: %v", env.name, got, env.got)
		}
		if got := countEnvPrefix(env.got, "BEADS_DOLT_AUTO_START="); got != 1 {
			t.Fatalf("%s env BEADS_DOLT_AUTO_START count = %d, want 1: %v", env.name, got, env.got)
		}
		if !containsEnv(env.got, "GT_DOLT_PORT=19999") || !containsEnv(env.got, "BEADS_DOLT_PORT=19999") || !containsEnv(env.got, "BEADS_DOLT_AUTO_START=0") {
			t.Fatalf("%s env missing isolated Dolt overrides: %v", env.name, env.got)
		}
	}
}

func countEnvPrefix(environ []string, prefix string) int {
	count := 0
	for _, env := range environ {
		if strings.HasPrefix(env, prefix) {
			count++
		}
	}
	return count
}

func containsEnv(environ []string, want string) bool {
	for _, env := range environ {
		if env == want {
			return true
		}
	}
	return false
}

func TestInitPassesServerFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix shell script bd stub")
	}

	bdAllowStaleMu.Lock()
	prevPath := bdAllowStalePath
	prevResult := bdAllowStaleResult
	bdAllowStaleMu.Unlock()
	ResetBdAllowStaleCacheForTest()
	t.Cleanup(func() {
		bdAllowStaleMu.Lock()
		bdAllowStalePath = prevPath
		bdAllowStaleResult = prevResult
		bdAllowStaleMu.Unlock()
	})

	stubDir := t.TempDir()
	logPath := filepath.Join(stubDir, "bd.log")
	stubPath := filepath.Join(stubDir, "bd")
	script := `#!/bin/sh
if [ "$1" = "--allow-stale" ]; then
  echo "Error: unknown flag: --allow-stale" >&2
  exit 0
fi
if [ "$1" != "init" ]; then
  echo "unexpected args=$*" > "$MOCK_BD_LOG"
  exit 2
fi
{
  printf 'args=%s\n' "$*"
  printf 'BEADS_DIR=%s\n' "$BEADS_DIR"
  printf 'GT_DOLT_PORT=%s\n' "$GT_DOLT_PORT"
  printf 'BEADS_DOLT_PORT=%s\n' "$BEADS_DOLT_PORT"
  printf 'BEADS_DOLT_AUTO_START=%s\n' "$BEADS_DOLT_AUTO_START"
} > "$MOCK_BD_LOG"
exit 0
`
	if err := os.WriteFile(stubPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MOCK_BD_LOG", logPath)

	workDir := t.TempDir()
	b := NewIsolatedWithPort(workDir, 19999)
	if err := b.Init("covertest"); err != nil {
		t.Fatalf("Init: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	log := string(data)
	checks := []string{
		"args=init --prefix covertest --quiet --server --server-port 19999",
		"BEADS_DIR=" + filepath.Join(workDir, ".beads"),
		"GT_DOLT_PORT=19999",
		"BEADS_DOLT_PORT=19999",
		"BEADS_DOLT_AUTO_START=0",
	}
	for _, check := range checks {
		if !strings.Contains(log, check) {
			t.Fatalf("bd init stub log missing %q:\n%s", check, log)
		}
	}
}

// ---------------------------------------------------------------------------
// stripEnvPrefixes tests (refactored from runWithRouting inline logic)
// ---------------------------------------------------------------------------

// TestStripEnvPrefixes verifies the generic prefix stripping used by runWithRouting.
func TestStripEnvPrefixes(t *testing.T) {
	tests := []struct {
		name     string
		environ  []string
		prefixes []string
		want     []string
	}{
		{
			name:     "strips single prefix",
			environ:  []string{"BEADS_DIR=/tmp", "PATH=/usr/bin", "HOME=/home"},
			prefixes: []string{"BEADS_DIR="},
			want:     []string{"PATH=/usr/bin", "HOME=/home"},
		},
		{
			name:     "strips multiple prefixes",
			environ:  []string{"BEADS_DIR=/tmp", "BD_ACTOR=test-actor", "PATH=/usr/bin"},
			prefixes: []string{"BEADS_DIR=", "BD_ACTOR="},
			want:     []string{"PATH=/usr/bin"},
		},
		{
			name:     "no matches",
			environ:  []string{"PATH=/usr/bin", "HOME=/home"},
			prefixes: []string{"BEADS_DIR=", "BD_ACTOR="},
			want:     []string{"PATH=/usr/bin", "HOME=/home"},
		},
		{
			name:     "empty prefixes",
			environ:  []string{"PATH=/usr/bin"},
			prefixes: []string{},
			want:     []string{"PATH=/usr/bin"},
		},
		{
			name:     "nil environ",
			environ:  nil,
			prefixes: []string{"BEADS_DIR="},
			want:     []string{},
		},
		{
			name:     "empty environ",
			environ:  []string{},
			prefixes: []string{"BEADS_DIR="},
			want:     []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripEnvPrefixes(tt.environ, tt.prefixes...)
			if len(got) != len(tt.want) {
				t.Fatalf("stripEnvPrefixes() returned %d items, want %d\n  got:  %v\n  want: %v",
					len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestStripEnvPrefixes_PreservesOrder verifies output ordering is stable.
func TestStripEnvPrefixes_PreservesOrder(t *testing.T) {
	environ := []string{"A=1", "BEADS_DIR=/tmp", "B=2", "BD_ACTOR=x", "C=3"}
	got := stripEnvPrefixes(environ, "BEADS_DIR=", "BD_ACTOR=")
	want := []string{"A=1", "B=2", "C=3"}

	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// translateDoltPort tests
// ---------------------------------------------------------------------------

// TestTranslateDoltPort verifies GT_DOLT_PORT → BEADS_DOLT_PORT translation.
// This is the core fix for hq-27t: gastown sets GT_DOLT_PORT but bd only reads
// BEADS_DOLT_PORT. Without translation, bd falls back to metadata.json port 3307.
func TestTranslateDoltPort(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		want []string
	}{
		{
			name: "translates GT to BEADS",
			env:  []string{"GT_DOLT_PORT=12345", "PATH=/usr/bin"},
			want: []string{"GT_DOLT_PORT=12345", "PATH=/usr/bin", "BEADS_DOLT_PORT=12345"},
		},
		{
			name: "skips if BEADS_DOLT_PORT already set",
			env:  []string{"GT_DOLT_PORT=12345", "BEADS_DOLT_PORT=99999"},
			want: []string{"GT_DOLT_PORT=12345", "BEADS_DOLT_PORT=99999"},
		},
		{
			name: "no-op without GT_DOLT_PORT",
			env:  []string{"PATH=/usr/bin"},
			want: []string{"PATH=/usr/bin"},
		},
		{
			name: "empty env",
			env:  []string{},
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := translateDoltPort(tt.env)
			if len(got) != len(tt.want) {
				t.Fatalf("translateDoltPort() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Integration tests — verify env behavior with real os.Environ()
// ---------------------------------------------------------------------------

// TestFilterBeadsEnv_Integration verifies filterBeadsEnv strips all expected
// vars from a real os.Environ() with multiple beads vars set.
func TestFilterBeadsEnv_Integration(t *testing.T) {
	t.Setenv("BD_ACTOR", "gastown/polecats/TestPolecat")
	t.Setenv("BEADS_DIR", "/tmp/test-beads")
	t.Setenv("GT_ROOT", "/tmp/test-gt-root")

	env := filterBeadsEnv(os.Environ())

	// BEADS_DOLT_PORT and GT_DOLT_PORT are explicitly preserved (test server access).
	// Check that other BEADS_* vars are still stripped.
	forbidden := []string{"BD_ACTOR=", "BEADS_DIR=", "BEADS_DB=", "GT_ROOT=", "HOME="}
	for _, e := range env {
		for _, prefix := range forbidden {
			if strings.HasPrefix(e, prefix) {
				t.Errorf("filterBeadsEnv did not strip %s (found: %s)", prefix, e)
			}
		}
	}
}

// TestBdBranch_SystemScenario_FilterBeadsEnvIsolation verifies filterBeadsEnv
// strips all beads-related vars from subprocess environment.
func TestBdBranch_SystemScenario_FilterBeadsEnvIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping system test in short mode")
	}

	t.Setenv("BD_ACTOR", "gastown/polecats/FilterTest")
	t.Setenv("BEADS_DIR", "/tmp/filter-test-beads")
	t.Setenv("GT_ROOT", "/tmp/filter-test-gt")

	filtered := filterBeadsEnv(os.Environ())

	// Verify beads-specific vars are stripped from the filtered env.
	forbidden := []string{"BD_ACTOR=", "BEADS_DIR=", "GT_ROOT="}
	for _, entry := range filtered {
		for _, prefix := range forbidden {
			if strings.HasPrefix(entry, prefix) {
				t.Errorf("filterBeadsEnv result still contains %s", entry)
			}
		}
	}
}

// TestBuildRunEnv verifies buildRunEnv() returns the correct environment
// for each mode: default (passthrough) and isolated (strip all beads vars).
func TestBuildRunEnv(t *testing.T) {
	tests := []struct {
		name     string
		isolated bool
		// envVars to inject via t.Setenv
		envVars map[string]string
		// mustContain: prefixes that MUST be present in the result
		mustContain []string
		// mustNotContain: prefixes that MUST NOT be present in the result
		mustNotContain []string
	}{
		{
			name:           "default preserves all vars",
			envVars:        map[string]string{"PATH": "/usr/bin"},
			mustContain:    []string{"PATH="},
			mustNotContain: nil,
		},
		{
			name:           "isolated strips all beads vars",
			isolated:       true,
			envVars:        map[string]string{"BD_ACTOR": "test-actor", "BEADS_DIR": "/tmp/beads"},
			mustNotContain: []string{"BD_ACTOR=", "BEADS_DIR="},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}
			b := &Beads{workDir: "/tmp", isolated: tt.isolated}
			env := b.buildRunEnv()

			for _, prefix := range tt.mustContain {
				found := false
				for _, e := range env {
					if strings.HasPrefix(e, prefix) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected %s to be present", prefix)
				}
			}
			for _, prefix := range tt.mustNotContain {
				for _, e := range env {
					if strings.HasPrefix(e, prefix) {
						t.Errorf("expected %s to be absent, got %s", prefix, e)
					}
				}
			}
		})
	}
}

// TestBuildRoutingEnv verifies buildRoutingEnv() returns the correct environment
// for each mode: default (strip BEADS_DIR only) and isolated (strip all beads vars).
func TestBuildRoutingEnv(t *testing.T) {
	tests := []struct {
		name           string
		isolated       bool
		envVars        map[string]string
		mustContain    []string
		mustNotContain []string
	}{
		{
			name:           "default strips BEADS_DIR only",
			envVars:        map[string]string{"BEADS_DIR": "/tmp/beads", "PATH": "/usr/bin"},
			mustContain:    []string{"PATH="},
			mustNotContain: []string{"BEADS_DIR="},
		},
		{
			name:           "isolated strips all beads vars",
			isolated:       true,
			envVars:        map[string]string{"BD_ACTOR": "test-actor", "BEADS_DIR": "/tmp/beads"},
			mustNotContain: []string{"BD_ACTOR=", "BEADS_DIR="},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}
			b := &Beads{workDir: "/tmp", isolated: tt.isolated}
			env := b.buildRoutingEnv()

			for _, prefix := range tt.mustContain {
				found := false
				for _, e := range env {
					if strings.HasPrefix(e, prefix) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected %s to be present", prefix)
				}
			}
			for _, prefix := range tt.mustNotContain {
				for _, e := range env {
					if strings.HasPrefix(e, prefix) {
						t.Errorf("expected %s to be absent, got %s", prefix, e)
					}
				}
			}
		})
	}
}

func TestBuildRunEnv_OverridesStaleDoltPortFromBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte("43113\n"), 0644); err != nil {
		t.Fatalf("write dolt-server.port: %v", err)
	}

	t.Setenv("BEADS_DOLT_PORT", "3307")

	env := (&Beads{workDir: tmpDir}).buildRunEnv()

	found := false
	for _, e := range env {
		switch e {
		case "BEADS_DOLT_PORT=43113":
			found = true
		case "BEADS_DOLT_PORT=3307":
			t.Fatalf("stale BEADS_DOLT_PORT preserved in env: %v", env)
		}
	}
	if !found {
		t.Fatalf("expected BEADS_DOLT_PORT=43113 in env, got %v", env)
	}
}

func TestBuildRoutingEnv_OverridesStaleDoltPortFromBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte("43113\n"), 0644); err != nil {
		t.Fatalf("write dolt-server.port: %v", err)
	}

	t.Setenv("BEADS_DOLT_PORT", "3307")

	env := (&Beads{workDir: tmpDir}).buildRoutingEnv()

	found := false
	for _, e := range env {
		switch e {
		case "BEADS_DOLT_PORT=43113":
			found = true
		case "BEADS_DOLT_PORT=3307":
			t.Fatalf("stale BEADS_DOLT_PORT preserved in env: %v", env)
		}
	}
	if !found {
		t.Fatalf("expected BEADS_DOLT_PORT=43113 in env, got %v", env)
	}
}

func TestRunEnv_StripsPollutedDoltEnvAndUsesRigMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix shell script bd stub")
	}

	bdAllowStaleMu.Lock()
	prevPath := bdAllowStalePath
	prevResult := bdAllowStaleResult
	bdAllowStaleMu.Unlock()
	ResetBdAllowStaleCacheForTest()
	t.Cleanup(func() {
		bdAllowStaleMu.Lock()
		bdAllowStalePath = prevPath
		bdAllowStaleResult = prevResult
		bdAllowStaleMu.Unlock()
	})

	workDir := t.TempDir()
	beadsDir := filepath.Join(workDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	metadata := []byte(`{"backend":"dolt","dolt_mode":"server","dolt_server_host":"127.0.0.1","dolt_server_port":3307,"dolt_database":"gastown"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadata, 0644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	stubDir := t.TempDir()
	logPath := filepath.Join(stubDir, "bd.log")
	stubPath := filepath.Join(stubDir, "bd")
	script := `#!/bin/sh
if [ "$1" = "--allow-stale" ]; then
  echo "Error: unknown flag: --allow-stale" >&2
  exit 0
fi
{
  printf 'BEADS_DIR=%s\n' "${BEADS_DIR:-}"
  printf 'BEADS_DOLT_DATA_DIR=%s\n' "${BEADS_DOLT_DATA_DIR:-}"
  printf 'BEADS_DOLT_HOST=%s\n' "${BEADS_DOLT_HOST:-}"
  printf 'BEADS_DOLT_PORT=%s\n' "${BEADS_DOLT_PORT:-}"
  printf 'BEADS_DOLT_SERVER_HOST=%s\n' "${BEADS_DOLT_SERVER_HOST:-}"
  printf 'BEADS_DOLT_SERVER_PORT=%s\n' "${BEADS_DOLT_SERVER_PORT:-}"
  printf 'BEADS_DOLT_SERVER_DATABASE=%s\n' "${BEADS_DOLT_SERVER_DATABASE:-}"
} > "$MOCK_BD_LOG"
if [ -n "${BEADS_DOLT_HOST:-}" ] || [ "${BEADS_DOLT_SERVER_DATABASE:-}" = "hq" ]; then
  printf 'hq\n'
  exit 0
fi
if [ "${BEADS_DIR:-}" = "${PWD}/.beads" ]; then
  printf 'gt\n'
  exit 0
fi
printf 'unknown\n'
`
	if err := os.WriteFile(stubPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MOCK_BD_LOG", logPath)
	t.Setenv("BEADS_DOLT_DATA_DIR", "/home/coder/gt/.dolt-data")
	t.Setenv("BEADS_DOLT_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_PORT", "3307")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "3307")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "hq")

	out, err := New(workDir).Run("config", "get", "issue_prefix")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "gt" {
		t.Fatalf("polluted env selected prefix %q, want gt", got)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	log := string(data)
	for _, forbidden := range []string{
		"BEADS_DOLT_DATA_DIR=/home/coder/gt/.dolt-data",
		"BEADS_DOLT_HOST=127.0.0.1",
		"BEADS_DOLT_SERVER_DATABASE=hq",
	} {
		if strings.Contains(log, forbidden) {
			t.Fatalf("stale Dolt env leaked into bd subprocess (%s):\n%s", forbidden, log)
		}
	}
	for _, want := range []string{
		"BEADS_DIR=" + beadsDir,
		"BEADS_DOLT_SERVER_DATABASE=gastown",
		"BEADS_DOLT_PORT=3307",
		"BEADS_DOLT_SERVER_HOST=127.0.0.1",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("bd subprocess env missing %q:\n%s", want, log)
		}
	}
}

func TestIsSubprocessCrash(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"normal error", fmt.Errorf("bd create: exit status 1"), false},
		{"not found", fmt.Errorf("bd show: not found"), false},
		{"signal segfault", fmt.Errorf("bd create: signal: segmentation fault"), true},
		{"signal killed", fmt.Errorf("bd create: signal: killed"), true},
		{"nil pointer in stderr", fmt.Errorf("bd create: nil pointer dereference"), true},
		{"panic in stderr", fmt.Errorf("bd create: panic: runtime error"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSubprocessCrash(tt.err); got != tt.want {
				t.Errorf("isSubprocessCrash(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestResolveBdSubprocessTimeout verifies the timeout default + GT_BD_TIMEOUT_SEC override.
func TestResolveBdSubprocessTimeout(t *testing.T) {
	tests := []struct {
		name    string
		envVal  string
		envSet  bool
		wantSec int
	}{
		{name: "default", envSet: false, wantSec: 60},
		{name: "override 5s", envSet: true, envVal: "5", wantSec: 5},
		{name: "override 120s", envSet: true, envVal: "120", wantSec: 120},
		{name: "invalid falls to default", envSet: true, envVal: "abc", wantSec: 60},
		{name: "zero falls to default", envSet: true, envVal: "0", wantSec: 60},
		{name: "negative falls to default", envSet: true, envVal: "-1", wantSec: 60},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envSet {
				t.Setenv("GT_BD_TIMEOUT_SEC", tt.envVal)
			} else {
				_ = os.Unsetenv("GT_BD_TIMEOUT_SEC")
			}
			got := resolveBdSubprocessTimeout()
			want := time.Duration(tt.wantSec) * time.Second
			if got != want {
				t.Errorf("resolveBdSubprocessTimeout() = %v, want %v", got, want)
			}
		})
	}
}
