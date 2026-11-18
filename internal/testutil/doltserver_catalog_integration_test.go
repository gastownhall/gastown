//go:build integration && !windows

package testutil

import (
	"context"
	"database/sql"
	"os/exec"
	"reflect"
	"sort"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/dolt"
)

// TestDoltTestEnvironmentLeavesProductionCatalogUnchanged is the end-to-end
// guard for gt-ci7. The first container stands in for production and is exposed
// through every inherited routing variable that caused the original leak. The
// test boundary must route bd init to the second, disposable container without
// changing the production catalog.
func TestDoltTestEnvironmentLeavesProductionCatalogUnchanged(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not available")
	}
	if !isDockerAvailable() {
		t.Skip("Docker not available")
	}

	_, productionPort := startCatalogTestDolt(t)
	_, disposablePort := startCatalogTestDolt(t)

	keys := append([]string{}, doltTargetSelectorEnvVars...)
	keys = append(keys,
		"GT_DOLT_HOST", "GT_DOLT_PORT", "BEADS_DOLT_SERVER_HOST",
		"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT", "BEADS_DOLT_AUTO_START",
		"GT_TEST_EXTERNAL_DOLT",
	)
	t.Cleanup(restoreEnvironment(t, keys))

	requireSetenv(t, "GT_DOLT_PORT", productionPort)
	requireSetenv(t, "BEADS_DOLT_SERVER_PORT", productionPort)
	requireSetenv(t, "BEADS_DOLT_PORT", productionPort)
	requireSetenv(t, "BEADS_DOLT_SERVER_DATABASE", "production")

	before := catalogForPort(t, productionPort)
	configureDoltTestProcessEnv(disposablePort)

	cmd := exec.Command("bd", "init", "--prefix", "catalogguard", "--server")
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init on disposable Dolt: %v\n%s", err, output)
	}

	after := catalogForPort(t, productionPort)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("production catalog changed: before=%v after=%v", before, after)
	}
	if got := catalogForPort(t, disposablePort); reflect.DeepEqual(got, before) {
		t.Fatalf("disposable catalog did not receive bd fixture database: %v", got)
	}
}

func startCatalogTestDolt(t *testing.T) (*dolt.DoltContainer, string) {
	t.Helper()
	ctx := context.Background()
	ctr, err := runDoltContainerWithRetry(ctx)
	if err != nil {
		t.Fatalf("start Dolt container: %v", err)
	}
	port, err := ctr.MappedPort(ctx, "3306/tcp")
	if err != nil {
		_ = testcontainers.TerminateContainer(ctr)
		t.Fatalf("map Dolt port: %v", err)
	}
	t.Cleanup(func() {
		_ = testcontainers.TerminateContainer(ctr)
	})
	return ctr, port.Port()
}

func catalogForPort(t *testing.T, port string) []string {
	t.Helper()
	db, err := sql.Open("mysql", "root:@tcp(127.0.0.1:"+port+")/")
	if err != nil {
		t.Fatalf("open Dolt %s: %v", port, err)
	}
	defer db.Close()

	rows, err := db.Query("SHOW DATABASES")
	if err != nil {
		t.Fatalf("show databases on Dolt %s: %v", port, err)
	}
	defer rows.Close()

	var catalog []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan catalog on Dolt %s: %v", port, err)
		}
		catalog = append(catalog, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read catalog on Dolt %s: %v", port, err)
	}
	sort.Strings(catalog)
	return catalog
}
