package cmd

import (
	"testing"

	"github.com/jonbaldie/gastown/internal/doltserver"
)

func TestLifecycle_PostClaimDone(t *testing.T) {
	t.Parallel()
	store := newFakeWLCommonsStore()

	// Post
	item := &doltserver.WantedItem{
		ID:          "w-life1",
		Title:       "Lifecycle test",
		Type:        "feature",
		Priority:    2,
		PostedBy:    "poster-rig",
		EffortLevel: "medium",
	}
	if err := postWanted(store, item); err != nil {
		t.Fatalf("postWanted() error: %v", err)
	}

	// Verify open
	got, _ := store.QueryWanted("w-life1")
	if got.Status != "open" {
		t.Fatalf("after post: Status = %q, want %q", got.Status, "open")
	}

	// Claim
	_, err := claimWanted(store, "w-life1", "claimer-rig")
	if err != nil {
		t.Fatalf("claimWanted() error: %v", err)
	}

	got, _ = store.QueryWanted("w-life1")
	if got.Status != "claimed" {
		t.Fatalf("after claim: Status = %q, want %q", got.Status, "claimed")
	}

	// Done
	if err := submitDone(store, "w-life1", "claimer-rig", "https://pr/1", "c-done1"); err != nil {
		t.Fatalf("submitDone() error: %v", err)
	}

	got, _ = store.QueryWanted("w-life1")
	if got.Status != "in_review" {
		t.Fatalf("after done: Status = %q, want %q", got.Status, "in_review")
	}
}

func TestLifecycle_DoubleClaim(t *testing.T) {
	t.Parallel()
	store := newFakeWLCommonsStore()
	_ = store.InsertWanted(&doltserver.WantedItem{
		ID:    "w-double",
		Title: "Double claim test",
	})

	// First claim succeeds
	_, err := claimWanted(store, "w-double", "rig-1")
	if err != nil {
		t.Fatalf("first claimWanted() error: %v", err)
	}

	// Second claim fails (status is now "claimed", not "open")
	_, err = claimWanted(store, "w-double", "rig-2")
	if err == nil {
		t.Fatal("second claimWanted() should fail for already-claimed item")
	}
}

func TestLifecycle_DoneWithoutClaim(t *testing.T) {
	t.Parallel()
	store := newFakeWLCommonsStore()
	_ = store.InsertWanted(&doltserver.WantedItem{
		ID:    "w-noclaim",
		Title: "No claim test",
	})

	// Submit done on open item should fail
	err := submitDone(store, "w-noclaim", "my-rig", "evidence", "c-test")
	if err == nil {
		t.Fatal("submitDone() should fail on open item")
	}
}

func TestLifecycle_ClaimCompletedItem(t *testing.T) {
	t.Parallel()
	store := newFakeWLCommonsStore()
	_ = store.InsertWanted(&doltserver.WantedItem{
		ID:    "w-completed",
		Title: "Completed item test",
	})

	// Claim and complete
	_ = store.ClaimWanted("w-completed", "rig-1")
	_ = store.SubmitCompletion("c-1", "w-completed", "rig-1", "evidence")

	// Trying to claim an in_review item should fail
	_, err := claimWanted(store, "w-completed", "rig-2")
	if err == nil {
		t.Fatal("claimWanted() should fail on in_review item")
	}
}
