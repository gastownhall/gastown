package refinery

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"strconv"
	"testing"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/testutil"
)

// initTestBeads creates a beads database for rigPath on the shared test Dolt
// container and returns an isolated wrapper for it.
//
// Every test gets its own prefix, and therefore its own database. Reusing a
// fixed prefix makes the second bd init target a database that already belongs
// to an earlier test's project, and bd then refuses to connect with
// "PROJECT IDENTITY MISMATCH". The database is dropped when the test ends so
// the shared container does not accumulate them.
func initTestBeads(t *testing.T, rigPath string) *beads.Beads {
	t.Helper()
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())

	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	prefix := "rf" + hex.EncodeToString(buf[:])

	b := beads.NewIsolatedWithPort(rigPath, port)
	if err := b.Init(prefix); err != nil {
		t.Skipf("bd init unavailable: %v", err)
	}

	dbName := beads.DatabaseNameFromMetadata(filepath.Join(rigPath, ".beads"))
	if dbName == "" {
		dbName = prefix
	}
	t.Cleanup(func() {
		db, err := sql.Open("mysql", "root:@tcp(127.0.0.1:"+testutil.DoltContainerPort()+")/")
		if err != nil {
			t.Logf("cleanup: connect to drop %s: %v", dbName, err)
			return
		}
		defer db.Close()
		if _, err := db.Exec("DROP DATABASE IF EXISTS `" + dbName + "`"); err != nil {
			t.Logf("cleanup: drop %s: %v", dbName, err)
		}
		_, _ = db.Exec("CALL dolt_purge_dropped_databases()")
	})

	return b
}
