// Package beads: in-process Dolt read path.
//
// Read-only bd operations (list/show/ready/blocked/search) normally shell out to
// the `bd` binary, which costs ~1s of cold-start per call (190MB Go binary init,
// cobra parse, Dolt TCP connect + MySQL handshake, schema migration checks).
//
// When direct reads are enabled, those operations instead query Dolt directly via
// the beads SDK (beadsdk.OpenFromConfig) from gt's own process — the same code
// path the daemon's ConvoyManager already uses. This reuses the existing
// in-process store integration (store.go) rather than reimplementing bd's SQL.
//
// Writes (create/update/close/dep) deliberately never use this path: they stay on
// the bd subprocess for dolt_commit atomicity. Only the read methods call
// readStore(); the explicit store field (SetStore/NewWithStore) is unchanged and
// still serves both reads and writes for opted-in callers (daemon, tracking).
//
// Rollout is opt-in (default off) because the Dolt data plane is fragile (see
// CLAUDE.md) and the SDK read path can diverge subtly from `bd list` default
// filters. Enable with GT_BD_DIRECT_READ=1 once validated for a workload.
package beads

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	beadsdk "github.com/steveyegge/beads"
)

// directReadEnvVar gates the in-process Dolt read path.
const directReadEnvVar = "GT_BD_DIRECT_READ"

// readStoreOpenTimeout bounds the initial OpenFromConfig so a wedged or
// unreachable Dolt server cannot hang the first read; on timeout we fall back to
// the bd subprocess (which has its own retry/--allow-stale handling).
const readStoreOpenTimeout = 10 * time.Second

// DirectReadEnabled reports whether read-only bd operations should bypass the
// subprocess and query Dolt in-process. Controlled by GT_BD_DIRECT_READ; off
// unless explicitly set to 1/true/on/yes.
func DirectReadEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(directReadEnvVar))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// openReadStoreFunc opens an in-process store for a beads directory. It is a
// package var so tests can substitute a fake without a live Dolt server.
var openReadStoreFunc = func(ctx context.Context, beadsDir string) (beadsdk.Storage, error) {
	return beadsdk.OpenFromConfig(ctx, beadsDir)
}

// Process-wide read-store cache. We open at most one store (one *sql.DB
// connection pool, which is goroutine-safe) per beads directory and reuse it for
// the lifetime of the process. This bounds connections (one pool per DB, same as
// the daemon) and amortizes OpenFromConfig's view-recreation/schema check across
// all reads. The pools are intentionally never closed mid-process; the OS
// reclaims them at exit. Long-lived holders that need explicit lifecycle should
// use SetStore instead.
var (
	readStoreMu    sync.Mutex
	readStoreCache = map[string]beadsdk.Storage{} // beadsDir -> store (nil entry = open failed)
	readStoreSeen  = map[string]bool{}            // beadsDir -> attempted (negative-cache marker)
)

// readStore returns a store suitable for read-only operations, or nil to signal
// the caller should fall back to the bd subprocess.
//
//   - An explicit store (SetStore/NewWithStore) is always honored — preserves
//     existing daemon/tracking behavior, including writes.
//   - Otherwise, when direct reads are enabled and not in isolated (test) mode,
//     returns a process-wide cached store for this instance's beads directory.
//
// Write methods must NOT call this; they gate on b.store directly so a read-only
// store never changes write semantics.
func (b *Beads) readStore() beadsdk.Storage {
	if b.store != nil {
		return b.store
	}
	if b.isolated || !DirectReadEnabled() {
		return nil
	}
	beadsDir := b.getResolvedBeadsDir()
	if beadsDir == "" {
		return nil
	}
	return cachedReadStore(beadsDir)
}

// cachedReadStore returns the process-wide store for beadsDir, opening it once.
// On open failure it negative-caches nil so repeated reads don't thrash the
// (possibly down) Dolt server with timeouts; callers fall back to the subprocess.
func cachedReadStore(beadsDir string) beadsdk.Storage {
	readStoreMu.Lock()
	defer readStoreMu.Unlock()

	if readStoreSeen[beadsDir] {
		return readStoreCache[beadsDir]
	}

	ctx, cancel := context.WithTimeout(context.Background(), readStoreOpenTimeout)
	defer cancel()

	store, err := openReadStoreFunc(ctx, beadsDir)
	readStoreSeen[beadsDir] = true
	if err != nil {
		readStoreCache[beadsDir] = nil
		return nil
	}
	readStoreCache[beadsDir] = store
	return store
}

// ResetReadStoreCacheForTest clears the process-wide read-store cache. Tests that
// swap openReadStoreFunc or reuse beads directories within one process must call
// this so a prior result (including a negative-cached failure) does not leak.
func ResetReadStoreCacheForTest() {
	readStoreMu.Lock()
	readStoreCache = map[string]beadsdk.Storage{}
	readStoreSeen = map[string]bool{}
	readStoreMu.Unlock()
}
