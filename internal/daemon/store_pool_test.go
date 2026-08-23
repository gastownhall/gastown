package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	beadsdk "github.com/steveyegge/beads"
)

// Three stores is the largest topology exercised by one daemon test (HQ,
// active rig, and parked rig). Tests in this package that use real stores are
// intentionally serial because BEADS_TEST_MODE is process-global.
const daemonTestStorePoolSize = 3

var (
	daemonTestStoreOnce sync.Once
	daemonTestStoreErr  error
	daemonTestStoreRoot string
	daemonTestStorePool chan *daemonTestStore
	daemonTestStores    []*daemonTestStore
)

type daemonTestStore struct {
	store    daemonTestPooledStorage
	db       *sql.DB
	baseline string
}

// nonClosingTestStore lets lifecycle tests verify that ConvoyManager calls
// Close without destroying a pooled connection. The fixture cleanup owns the
// underlying store and restores it before returning it to the pool.
type nonClosingTestStore struct {
	daemonTestPooledStorage
}

func (*nonClosingTestStore) Close() error { return nil }

type daemonTestVersionControl interface {
	CommitWithConfig(context.Context, string) error
	GetCurrentCommit(context.Context) (string, error)
}

type daemonTestRawDB interface {
	UnderlyingDB() *sql.DB
}

type daemonTestPooledStorage interface {
	beadsdk.Storage
	daemonTestVersionControl
	daemonTestRawDB
	DB() *sql.DB
	SetMetadata(context.Context, string, string) error
	GetMetadata(context.Context, string) (string, error)
}

func initDaemonTestStorePool() {
	daemonTestStoreRoot, daemonTestStoreErr = os.MkdirTemp("", "gt-daemon-store-pool-")
	if daemonTestStoreErr != nil {
		daemonTestStoreErr = fmt.Errorf("create store pool root: %w", daemonTestStoreErr)
		return
	}

	daemonTestStorePool = make(chan *daemonTestStore, daemonTestStorePoolSize)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	for i := range daemonTestStorePoolSize {
		doltPath := filepath.Join(daemonTestStoreRoot, fmt.Sprintf("store-%d", i), ".beads", "dolt")
		if err := os.MkdirAll(doltPath, 0755); err != nil {
			daemonTestStoreErr = fmt.Errorf("create store %d directory: %w", i, err)
			return
		}

		store, err := beadsdk.Open(ctx, doltPath)
		if err != nil {
			daemonTestStoreErr = fmt.Errorf("open store %d: %w", i, err)
			return
		}
		pooledStore, ok := store.(daemonTestPooledStorage)
		if !ok {
			_ = store.Close()
			daemonTestStoreErr = fmt.Errorf("store %d does not expose required test capabilities", i)
			return
		}

		if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
			_ = store.Close()
			daemonTestStoreErr = fmt.Errorf("configure store %d: %w", i, err)
			return
		}
		if err := pooledStore.CommitWithConfig(ctx, "test: daemon store baseline"); err != nil {
			_ = store.Close()
			daemonTestStoreErr = fmt.Errorf("commit store %d baseline: %w", i, err)
			return
		}
		baseline, err := pooledStore.GetCurrentCommit(ctx)
		if err != nil {
			_ = store.Close()
			daemonTestStoreErr = fmt.Errorf("read store %d baseline: %w", i, err)
			return
		}

		pooled := &daemonTestStore{store: pooledStore, db: pooledStore.UnderlyingDB(), baseline: baseline}
		daemonTestStores = append(daemonTestStores, pooled)
		daemonTestStorePool <- pooled
	}
}

func acquireDaemonTestStore(t *testing.T) (beadsdk.Storage, func()) {
	t.Helper()
	daemonTestStoreOnce.Do(initDaemonTestStorePool)
	if daemonTestStoreErr != nil {
		t.Skipf("beads store unavailable: %v", daemonTestStoreErr)
	}

	var pooled *daemonTestStore
	select {
	case pooled = <-daemonTestStorePool:
	case <-time.After(30 * time.Second):
		t.Fatal("timed out acquiring daemon test store")
	}

	if err := pooled.reset(); err != nil {
		daemonTestStorePool <- pooled
		t.Fatalf("reset daemon test store before use: %v", err)
	}

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			if err := pooled.reset(); err != nil {
				t.Errorf("reset daemon test store after use: %v", err)
			}
			daemonTestStorePool <- pooled
		})
	}
	return &nonClosingTestStore{daemonTestPooledStorage: pooled.store}, cleanup
}

func (s *daemonTestStore) reset() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := s.db.ExecContext(ctx, "CALL DOLT_RESET('--hard', ?)", s.baseline); err != nil {
		return fmt.Errorf("reset to baseline %s: %w", s.baseline, err)
	}

	// DOLT_RESET restores versioned tables. Beads intentionally excludes
	// clone-local state from Dolt history, so clear those tables explicitly.
	for _, table := range []string{
		"wisp_comments",
		"wisp_events",
		"wisp_dependencies",
		"wisp_labels",
		"wisps",
		"repo_mtimes",
		"local_metadata",
	} {
		if _, err := s.db.ExecContext(ctx, "DELETE FROM `"+table+"`"); err != nil {
			return fmt.Errorf("clear clone-local table %s: %w", table, err)
		}
	}
	return nil
}

func closeDaemonTestStorePool() {
	for _, pooled := range daemonTestStores {
		_ = pooled.store.Close()
	}
	if daemonTestStoreRoot != "" {
		_ = os.RemoveAll(daemonTestStoreRoot)
	}
}
