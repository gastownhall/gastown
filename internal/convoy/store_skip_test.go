package convoy

import (
	"testing"

	"github.com/jonbaldie/gastown/internal/testutil"
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
	t.Run("prefix", func(t *testing.T) {
		store, cleanup := setupTestStoreWithPrefix(t, "hq")
		cleanup()
		_ = store
		t.Fatal("setupTestStoreWithPrefix must skip when Dolt containers are disabled")
	})
}
