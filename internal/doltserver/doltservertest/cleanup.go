// Package doltservertest provides test helpers that depend on the doltserver
// package. It lives outside internal/testutil so that testutil can stay free of
// any doltserver import: the white-box "package doltserver" test files import
// testutil (for StartIsolatedDoltContainer), and routing doltserver-dependent
// helpers through testutil would close an import cycle
// (doltserver test -> testutil -> doltserver).
package doltservertest

import (
	"testing"

	"github.com/steveyegge/gastown/internal/doltserver"
)

// ReapOwnedDoltOnCleanup registers test cleanup for Dolt servers whose metadata
// and process args prove they belong to townRoot. It never kills by broad name or
// port, so production Dolt is protected when tests run inside a real workspace.
func ReapOwnedDoltOnCleanup(t testing.TB, townRoot string) {
	t.Helper()
	t.Cleanup(func() {
		stopped, err := doltserver.ReapOwnedTestServers(townRoot)
		if err != nil {
			t.Logf("owned Dolt cleanup skipped: %v", err)
			return
		}
		if stopped > 0 {
			t.Logf("stopped %d owned Dolt sql-server process(es)", stopped)
		}
	})
}
