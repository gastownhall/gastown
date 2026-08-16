package cmd

import (
	"testing"

	"github.com/jonbaldie/gastown/internal/doltserver"
)

func TestClaimWanted_Success(t *testing.T) {
	t.Parallel()
	store := newFakeWLCommonsStore()
	_ = store.InsertWanted(&doltserver.WantedItem{
		ID:    "w-abc123",
		Title: "Fix auth bug",
	})

	item, err := claimWanted(store, "w-abc123", "my-rig")
	if err != nil {
		t.Fatalf("claimWanted() error: %v", err)
	}
	if item.Title != "Fix auth bug" {
		t.Errorf("Title = %q, want %q", item.Title, "Fix auth bug")
	}

	// Verify status was updated in store
	updated, _ := store.QueryWanted("w-abc123")
	if updated.Status != "claimed" {
		t.Errorf("Status = %q, want %q", updated.Status, "claimed")
	}
	if updated.ClaimedBy != "my-rig" {
		t.Errorf("ClaimedBy = %q, want %q", updated.ClaimedBy, "my-rig")
	}
}

func TestClaimWanted_NotOpen(t *testing.T) {
	t.Parallel()
	store := newFakeWLCommonsStore()
	_ = store.InsertWanted(&doltserver.WantedItem{
		ID:     "w-abc123",
		Title:  "Fix auth bug",
		Status: "claimed",
	})

	_, err := claimWanted(store, "w-abc123", "my-rig")
	if err == nil {
		t.Fatal("claimWanted() expected error for non-open item")
	}
}

func TestClaimWanted_NotFound(t *testing.T) {
	t.Parallel()
	store := newFakeWLCommonsStore()

	_, err := claimWanted(store, "w-nonexistent", "my-rig")
	if err == nil {
		t.Fatal("claimWanted() expected error for missing item")
	}
}
