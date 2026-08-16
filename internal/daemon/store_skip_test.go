package daemon

import (
	"testing"

	"github.com/steveyegge/gastown/internal/testutil"
)

func TestSetupTestStoreSkipsWithoutDoltContainer(t *testing.T) {
	if testutil.DoltContainersEnabled() {
		t.Skip("Dolt container is running")
	}
	t.Run("store", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		cleanup()
		_ = store
		t.Fatal("setupTestStore must skip when Dolt containers are disabled")
	})
}
