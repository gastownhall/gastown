package cmd

import (
	"fmt"
	"testing"

	"github.com/jonbaldie/gastown/internal/doltserver"
)

func TestPostWanted_Success(t *testing.T) {
	t.Parallel()
	store := newFakeWLCommonsStore()

	item := &doltserver.WantedItem{
		ID:          "w-test123",
		Title:       "Fix auth bug",
		Description: "Auth is broken",
		Project:     "gastown",
		Type:        "bug",
		Priority:    1,
		Tags:        []string{"auth", "urgent"},
		PostedBy:    "my-rig",
		EffortLevel: "small",
	}

	if err := postWanted(store, item); err != nil {
		t.Fatalf("postWanted() error: %v", err)
	}

	// Verify it was stored
	got, err := store.QueryWanted("w-test123")
	if err != nil {
		t.Fatalf("QueryWanted() error: %v", err)
	}
	if got.Title != "Fix auth bug" {
		t.Errorf("Title = %q, want %q", got.Title, "Fix auth bug")
	}
	if got.Status != "open" {
		t.Errorf("Status = %q, want %q", got.Status, "open")
	}
}

func TestPostWanted_EmptyID(t *testing.T) {
	t.Parallel()
	store := newFakeWLCommonsStore()

	item := &doltserver.WantedItem{
		ID:    "",
		Title: "Some title",
	}

	err := postWanted(store, item)
	if err == nil {
		t.Fatal("postWanted() expected error for empty ID")
	}
}

func TestPostWanted_EmptyTitle(t *testing.T) {
	t.Parallel()
	store := newFakeWLCommonsStore()

	item := &doltserver.WantedItem{
		ID:    "w-test",
		Title: "",
	}

	err := postWanted(store, item)
	if err == nil {
		t.Fatal("postWanted() expected error for empty title")
	}
}

func TestPostWanted_EnsureDBFails(t *testing.T) {
	t.Parallel()
	store := newFakeWLCommonsStore()
	store.EnsureDBErr = fmt.Errorf("server down")

	item := &doltserver.WantedItem{
		ID:    "w-test",
		Title: "Test",
	}

	err := postWanted(store, item)
	if err == nil {
		t.Fatal("postWanted() expected error when EnsureDB fails")
	}
}

func TestValidatePostInputs_ValidType(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{"feature", "bug", "design", "rfc", "docs", ""} {
		if err := validatePostInputs(typ, "medium", 2); err != nil {
			t.Errorf("validatePostInputs(type=%q) unexpected error: %v", typ, err)
		}
	}
}

func TestValidatePostInputs_InvalidType(t *testing.T) {
	t.Parallel()
	err := validatePostInputs("invalid", "medium", 2)
	if err == nil {
		t.Error("validatePostInputs(type=invalid) expected error")
	}
}

func TestValidatePostInputs_InvalidEffort(t *testing.T) {
	t.Parallel()
	err := validatePostInputs("bug", "huge", 2)
	if err == nil {
		t.Error("validatePostInputs(effort=huge) expected error")
	}
}

func TestValidatePostInputs_PriorityBounds(t *testing.T) {
	t.Parallel()
	if err := validatePostInputs("", "medium", -1); err == nil {
		t.Error("validatePostInputs(priority=-1) expected error")
	}
	if err := validatePostInputs("", "medium", 5); err == nil {
		t.Error("validatePostInputs(priority=5) expected error")
	}
	// Valid bounds
	for _, p := range []int{0, 1, 2, 3, 4} {
		if err := validatePostInputs("", "medium", p); err != nil {
			t.Errorf("validatePostInputs(priority=%d) unexpected error: %v", p, err)
		}
	}
}
