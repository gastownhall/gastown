package doltserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	beadspkg "github.com/jonbaldie/gastown/internal/beads"
)

// TestFindRemote_NoRemote verifies FindRemote returns empty when no remote is configured.
func TestFindRemote_NoRemote(t *testing.T) {
	// Create a minimal dolt database directory
	dbDir := t.TempDir()
	doltDir := filepath.Join(dbDir, ".dolt")
	if err := os.MkdirAll(doltDir, 0755); err != nil {
		t.Fatalf("mkdir .dolt: %v", err)
	}

	// Initialize a bare dolt repo so "dolt remote -v" works
	if err := initDoltDB(dbDir); err != nil {
		t.Skipf("dolt not available: %v", err)
	}

	name, url, err := FindRemote(dbDir)
	if err != nil {
		t.Fatalf("FindRemote: %v", err)
	}
	if name != "" || url != "" {
		t.Errorf("expected empty remote, got name=%q url=%q", name, url)
	}
}

// TestSyncDatabases_EmptyDir verifies SyncDatabases handles missing data dir gracefully.
func TestSyncDatabases_EmptyDir(t *testing.T) {
	townRoot := t.TempDir()
	// No .dolt-data directory exists
	opts := SyncOptions{}
	results := SyncDatabases(townRoot, opts)
	// Should return empty or a single error result, not panic
	for _, r := range results {
		if r.Error != nil {
			// Acceptable — no data dir
			return
		}
	}
	// Also acceptable: empty results
}

// TestSyncDatabases_FilterSkipsOthers verifies the filter option.
func TestSyncDatabases_FilterSkipsOthers(t *testing.T) {
	townRoot := tempDirRetryCleanup(t)
	dataDir := filepath.Join(townRoot, ".dolt-data")

	// Create two fake database dirs with noms/manifest
	for _, db := range []string{"alpha", "beta"} {
		nomsDir := filepath.Join(dataDir, db, ".dolt", "noms")
		if err := os.MkdirAll(nomsDir, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(nomsDir, "manifest"), []byte("x"), 0644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
	}

	opts := SyncOptions{Filter: "alpha", DryRun: true}
	results := SyncDatabases(townRoot, opts)

	for _, r := range results {
		if r.Database == "beta" {
			t.Errorf("filter=alpha but beta was included in results")
		}
	}
}

// TestSyncDatabasesSQL_EmptyDir verifies SyncDatabasesSQL handles missing data dir.
func TestSyncDatabasesSQL_EmptyDir(t *testing.T) {
	townRoot := t.TempDir()
	opts := SyncOptions{}
	results := SyncDatabasesSQL(townRoot, opts)
	for _, r := range results {
		if r.Error != nil {
			return // acceptable
		}
	}
}

// TestSyncDatabasesSQL_FilterSkipsOthers verifies the SQL sync filter option.
func TestSyncDatabasesSQL_FilterSkipsOthers(t *testing.T) {
	townRoot := tempDirRetryCleanup(t)
	dataDir := filepath.Join(townRoot, ".dolt-data")

	for _, db := range []string{"alpha", "beta"} {
		nomsDir := filepath.Join(dataDir, db, ".dolt", "noms")
		if err := os.MkdirAll(nomsDir, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(nomsDir, "manifest"), []byte("x"), 0644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
	}

	opts := SyncOptions{Filter: "alpha", DryRun: true}
	results := SyncDatabasesSQL(townRoot, opts)

	for _, r := range results {
		if r.Database == "beta" {
			t.Errorf("filter=alpha but beta was included in results")
		}
	}
}

// TestValidSQLName verifies the defense-in-depth name validation.
func TestValidSQLName(t *testing.T) {
	valid := []string{"mydb", "beads_gastown", "my-db", "db.v2", "ABC123"}
	for _, name := range valid {
		if !validSQLName(name) {
			t.Errorf("validSQLName(%q) = false, want true", name)
		}
	}

	invalid := []string{"", "my`db", "db; DROP TABLE", "name'quote", "has space", "db\nline"}
	for _, name := range invalid {
		if validSQLName(name) {
			t.Errorf("validSQLName(%q) = true, want false", name)
		}
	}
}

func TestPurgeClosedEphemeralsUsesHardenedBDEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess test in short mode")
	}
	beadspkg.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beadspkg.ResetBdAllowStaleCacheForTest)

	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, "gastown", ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"dolt_database":"gastown","dolt_server_host":"metadata-host","dolt_server_port":3307}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadata, 0644); err != nil {
		t.Fatal(err)
	}

	stubDir := t.TempDir()
	logPath := filepath.Join(stubDir, "bd.log")
	stubPath := filepath.Join(stubDir, "bd")
	script := `#!/bin/sh
{
  printf 'args=%s\n' "$*"
  printf 'BEADS_DIR=%s\n' "${BEADS_DIR:-}"
  printf 'BEADS_DB=%s\n' "${BEADS_DB:-}"
  printf 'BD_DB=%s\n' "${BD_DB:-}"
  printf 'BEADS_DOLT_SERVER_DATABASE=%s\n' "${BEADS_DOLT_SERVER_DATABASE:-}"
  printf 'BEADS_DOLT_SERVER_HOST=%s\n' "${BEADS_DOLT_SERVER_HOST:-}"
  printf 'BEADS_DOLT_SERVER_PORT=%s\n' "${BEADS_DOLT_SERVER_PORT:-}"
  printf 'BEADS_DOLT_PORT=%s\n' "${BEADS_DOLT_PORT:-}"
  printf 'BD_DOLT_AUTO_COMMIT=%s\n' "${BD_DOLT_AUTO_COMMIT:-}"
} >> "$MOCK_BD_LOG"
if [ "$1" = "--allow-stale" ] && [ "$2" = "version" ]; then
  printf 'bd version\n'
  exit 0
fi
if [ "$1" = "--allow-stale" ] && [ "$2" = "purge" ]; then
  printf '{"purged_count":3}\n'
  exit 0
fi
printf 'unexpected args: %s\n' "$*" >&2
exit 2
`
	if err := os.WriteFile(stubPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MOCK_BD_LOG", logPath)
	t.Setenv("GT_DOLT_HOST", "127.0.0.2")
	t.Setenv("GT_DOLT_PORT", "5507")
	t.Setenv("BEADS_DIR", "/wrong")
	t.Setenv("BEADS_DB", "/wrong.db")
	t.Setenv("BD_DB", "/wrong.bd")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "wrong")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "stale-host")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "9999")
	t.Setenv("BEADS_DOLT_PORT", "9999")

	purged, err := PurgeClosedEphemerals(townRoot, "gastown", false)
	if err != nil {
		t.Fatalf("PurgeClosedEphemerals: %v", err)
	}
	if purged != 3 {
		t.Fatalf("purged = %d, want 3", purged)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	for _, want := range []string{
		"args=--allow-stale version",
		"args=--allow-stale purge --json",
		"BEADS_DIR=" + beadsDir,
		"BEADS_DOLT_SERVER_DATABASE=gastown",
		"BEADS_DOLT_SERVER_HOST=127.0.0.2",
		"BEADS_DOLT_SERVER_PORT=5507",
		"BEADS_DOLT_PORT=5507",
		"BD_DOLT_AUTO_COMMIT=on",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("bd log missing %q:\n%s", want, log)
		}
	}
	for _, forbidden := range []string{"BEADS_DB=/wrong.db", "BD_DB=/wrong.bd", "BEADS_DOLT_SERVER_DATABASE=wrong", "BEADS_DOLT_SERVER_HOST=stale-host", "BEADS_DOLT_SERVER_PORT=9999", "BEADS_DOLT_PORT=9999"} {
		if strings.Contains(log, forbidden) {
			t.Fatalf("stale env leaked via %q:\n%s", forbidden, log)
		}
	}
}

// tempDirRetryCleanup creates a temp directory with cleanup that tolerates
// brief file-lock delays on Windows (e.g., dolt subprocess handle release).
func tempDirRetryCleanup(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sync-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		for i := 0; i < 10; i++ {
			if err := os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Logf("warning: could not fully remove temp dir %s", dir)
	})
	return dir
}

// initDoltDB runs "dolt init" in a directory. Returns error if dolt isn't available.
func initDoltDB(dir string) error {
	cmd := exec.Command("dolt", "init", "--name", "test", "--email", "test@test.com")
	cmd.Dir = dir
	return cmd.Run()
}
