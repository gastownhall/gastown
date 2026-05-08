package daemon

import (
	"os"
	"strings"
)

// bdReadOnlyEnv returns an environment slice for read-only bd/gt subprocess
// calls invoked by the daemon. It forces BD_DOLT_AUTO_COMMIT=off so that
// read-only operations (status checks, list, show) do not request a Dolt
// auto-commit on completion. Without this, every read-only call opens a
// fresh connection to attempt a no-op commit, producing thousands of
// failed-but-counted connections per hour on idle towns and spamming
// dolt.log. See gh#3596.
//
// Existing BD_DOLT_AUTO_COMMIT entries are filtered out before appending
// the authoritative "off" value, because glibc getenv() returns the first
// matching entry — a stale "on" earlier in the slice would otherwise win.
func bdReadOnlyEnv() []string {
	base := os.Environ()
	filtered := make([]string, 0, len(base)+1)
	for _, e := range base {
		if !strings.HasPrefix(e, "BD_DOLT_AUTO_COMMIT=") {
			filtered = append(filtered, e)
		}
	}
	return append(filtered, "BD_DOLT_AUTO_COMMIT=off")
}
