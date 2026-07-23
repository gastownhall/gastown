package daemon

import (
	"context"
	"strings"
	"testing"

	beadsdk "github.com/steveyegge/beads"
)

type metadataWriter interface {
	SetMetadata(ctx context.Context, key, value string) error
}

func TestCheckBeadsStoreCompatibility_AllowsMatchingVersion(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	metadataStore, ok := store.(metadataWriter)
	if !ok {
		t.Skip("store does not expose metadata writes")
	}
	if err := metadataStore.SetMetadata(ctx, "bd_version", "0.62.0"); err != nil {
		t.Fatalf("SetMetadata(bd_version): %v", err)
	}

	if err := checkBeadsStoreCompatibility(ctx, map[string]beadsdk.Storage{"hq": store}, "0.62.0"); err != nil {
		t.Fatalf("checkBeadsStoreCompatibility returned unexpected error: %v", err)
	}
}

func TestCheckBeadsStoreCompatibility_RejectsNewerWorkspaceVersion(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()
	metadataStore, ok := store.(metadataWriter)
	if !ok {
		t.Skip("store does not expose metadata writes")
	}
	if err := metadataStore.SetMetadata(ctx, "bd_version", "9.9.9"); err != nil {
		t.Fatalf("SetMetadata(bd_version): %v", err)
	}

	err := checkBeadsStoreCompatibility(ctx, map[string]beadsdk.Storage{"hq": store}, "0.62.0")
	if err == nil {
		t.Fatal("expected incompatibility error, got nil")
	}
	if !strings.Contains(err.Error(), "workspace bd_version 9.9.9 is newer than embedded beads 0.62.0") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLimitDaemonBeadsStoreConnections(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	accessor, ok := store.(beadsDBAccessor)
	if !ok || accessor.DB() == nil {
		t.Skip("store does not expose its SQL pool")
	}

	limitDaemonBeadsStoreConnections(store)
	if got := accessor.DB().Stats().MaxOpenConnections; got != daemonBeadsStoreMaxConns {
		t.Fatalf("MaxOpenConnections = %d, want %d", got, daemonBeadsStoreMaxConns)
	}
}
