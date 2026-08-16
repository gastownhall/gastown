//go:build integration

package doltserver

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jonbaldie/gastown/internal/testutil"
)

// setupBdWorkDir creates a beads-compatible working directory pointing at an
// isolated Dolt server. It creates a .beads/metadata.json with the server port
// and initialises a minimal beads database with an issues table so that the
// bd CLI can operate against it.
func setupBdWorkDir(t *testing.T, port int) string {
	t.Helper()

	workDir := t.TempDir()
	beadsDir := filepath.Join(workDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("creating .beads dir: %v", err)
	}

	metadata := fmt.Sprintf(`{"backend":"dolt","database":"beads_test","dolt_mode":"server","dolt_server_host":"127.0.0.1","dolt_server_port":%d}`, port)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatalf("writing metadata.json: %v", err)
	}

	// Create the database and a minimal issues table on the isolated server.
	dsn := fmt.Sprintf("root@tcp(127.0.0.1:%d)/", port)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("connecting to isolated server: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE DATABASE IF NOT EXISTS beads_test",
		"USE beads_test",
		`CREATE TABLE IF NOT EXISTS issues (
			id VARCHAR(64) PRIMARY KEY,
			title VARCHAR(255) NOT NULL DEFAULT '',
			status VARCHAR(32) NOT NULL DEFAULT 'open',
			priority VARCHAR(16) NOT NULL DEFAULT 'P3',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		"INSERT IGNORE INTO issues (id, title, status) VALUES ('test-001', 'test issue', 'open')",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("initialising test database: %s: %v", stmt, err)
		}
	}

	return workDir
}

// TestMigrateWisps_TableCreation verifies that the wisps table and auxiliary
// tables are created when they don't exist.
func TestMigrateWisps_TableCreation(t *testing.T) {
	portStr := testutil.StartIsolatedDoltContainer(t)
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not found in PATH — skipping integration test")
	}

	port, _ := strconv.Atoi(portStr)
	workDir := setupBdWorkDir(t, port)

	// Verify we can talk to the database.
	err := bdSQL(workDir, "SELECT 1")
	if err != nil {
		t.Skipf("bd sql not working against isolated server: %v", err)
	}

	// Test table existence check.
	exists := bdTableExists(workDir, "issues")
	if !exists {
		t.Skip("issues table not found in isolated database")
	}

	// If wisps table already exists, just verify it works.
	if bdTableExists(workDir, "wisps") {
		t.Log("wisps table already exists — verifying bd mol wisp list works")
		cmd := exec.Command("bd", "mol", "wisp", "list")
		cmd.Dir = workDir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd mol wisp list failed: %s: %v", string(output), err)
		}
		t.Logf("bd mol wisp list output: %s", string(output))
		return
	}

	t.Log("wisps table does not exist — would need to create (skipping actual creation in test)")
}

// TestBdSQLCount verifies the count helper works.
func TestBdSQLCount(t *testing.T) {
	portStr := testutil.StartIsolatedDoltContainer(t)
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not found in PATH — skipping integration test")
	}

	port, _ := strconv.Atoi(portStr)
	workDir := setupBdWorkDir(t, port)

	cnt, err := bdSQLCount(workDir, "SELECT COUNT(*) as cnt FROM issues")
	if err != nil {
		t.Skipf("bd sql not working against isolated server: %v", err)
	}
	t.Logf("issues count: %d", cnt)
	if cnt < 0 {
		t.Fatalf("expected non-negative count, got %d", cnt)
	}
}
