//go:build !windows

package beads_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/testutil"
)

// TestOpenStoreFromConfig_RealDolt checks both sides of the database-creation
// policy against a real server: a stray .beads dir creates nothing, while the
// explicit init path still provisions the database named in metadata.json.
func TestOpenStoreFromConfig_RealDolt(t *testing.T) {
	port := testutil.StartIsolatedDoltContainer(t)
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", port)
	t.Setenv("BEADS_DOLT_PORT", port)
	t.Setenv("BEADS_DOLT_SERVER_USER", "root")
	t.Setenv("BEADS_DOLT_PASSWORD", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_CENTRAL_CONFIG", filepath.Join(t.TempDir(), "absent-server.json"))
	t.Setenv("BEADS_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "absent-credentials"))

	db, err := sql.Open("mysql", "root@tcp(127.0.0.1:"+port+")/")
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	stray := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if store, err := beads.OpenStoreFromConfig(ctx, stray); !errors.Is(err, beads.ErrStoreDatabaseNotConfigured) {
		if store != nil {
			_ = store.Close()
		}
		t.Fatalf("stray .beads: err = %v, want ErrStoreDatabaseNotConfigured", err)
	}
	if databaseExists(t, db, "beads") {
		t.Fatal("opening a stray .beads dir created the default 'beads' database")
	}

	rigBeads := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(rigBeads, 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := `{"backend":"dolt","dolt_mode":"server","dolt_database":"newrig"}`
	if err := os.WriteFile(filepath.Join(rigBeads, "metadata.json"), []byte(metadata), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := beads.CreateOrOpenStoreFromConfig(ctx, rigBeads)
	if err != nil {
		t.Fatalf("CreateOrOpenStoreFromConfig: %v", err)
	}
	_ = store.Close()
	if !databaseExists(t, db, "newrig") {
		t.Fatal("init path did not create the database named in metadata.json")
	}

	store, err = beads.OpenStoreFromConfig(ctx, rigBeads)
	if err != nil {
		t.Fatalf("reopening configured database: %v", err)
	}
	_ = store.Close()
	if databaseExists(t, db, "beads") {
		t.Fatal("default 'beads' database appeared on the server")
	}
}

func databaseExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	rows, err := db.Query("SHOW DATABASES")
	if err != nil {
		t.Fatalf("SHOW DATABASES: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var got string
		if err := rows.Scan(&got); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if got == name {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return false
}
