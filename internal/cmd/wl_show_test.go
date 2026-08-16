package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/doltserver"
)

func TestShowWantedJSON(t *testing.T) {
	store := newFakeWLCommonsStore()
	_ = store.InsertWanted(&doltserver.WantedItem{
		ID:          "w-json1",
		Title:       "Fix auth",
		Description: "Auth is broken",
		Project:     "gastown",
		Type:        "bug",
		Priority:    1,
		Tags:        []string{"auth", "urgent"},
		PostedBy:    "my-rig",
		Status:      "open",
		EffortLevel: "small",
		EvidenceURL: "https://example.com",
		CreatedAt:   "2026-01-01",
		UpdatedAt:   "2026-01-02",
	})

	out := captureStdout(t, func() {
		if err := showWanted(store, "w-json1", true); err != nil {
			t.Errorf("showWanted() error: %v", err)
		}
	})

	var item doltserver.WantedItem
	if err := json.Unmarshal([]byte(out), &item); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	if item.ID != "w-json1" {
		t.Errorf("ID = %q, want %q", item.ID, "w-json1")
	}
	if item.Title != "Fix auth" {
		t.Errorf("Title = %q, want %q", item.Title, "Fix auth")
	}
	if item.Description != "Auth is broken" {
		t.Errorf("Description = %q, want %q", item.Description, "Auth is broken")
	}
	if item.Priority != 1 {
		t.Errorf("Priority = %d, want 1", item.Priority)
	}
	if len(item.Tags) != 2 {
		t.Errorf("Tags = %v, want 2 elements", item.Tags)
	}
}

func TestShowWantedText(t *testing.T) {
	store := newFakeWLCommonsStore()
	_ = store.InsertWanted(&doltserver.WantedItem{
		ID:          "w-text1",
		Title:       "Improve logging",
		Description: "Add structured logs",
		Project:     "gastown",
		Type:        "feature",
		Priority:    2,
		Tags:        []string{"logging"},
		PostedBy:    "their-rig",
		Status:      "open",
		EffortLevel: "medium",
		EvidenceURL: "https://example.com/evidence",
		CreatedAt:   "2026-02-01",
		UpdatedAt:   "2026-02-02",
	})

	out := captureStdout(t, func() {
		if err := showWanted(store, "w-text1", false); err != nil {
			t.Errorf("showWanted() error: %v", err)
		}
	})

	for _, want := range []string{
		"ID:", "w-text1",
		"Title:", "Improve logging",
		"Description:", "Add structured logs",
		"Project:", "gastown",
		"Type:", "feature",
		"Priority:", "P2",
		"Tags:", "logging",
		"Posted By:", "their-rig",
		"Status:", "open",
		"Effort:", "medium",
		"Evidence URL:", "https://example.com/evidence",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestShowWantedNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeWLCommonsStore()

	err := showWanted(store, "w-nonexistent", false)
	if err == nil {
		t.Fatal("showWanted() expected error for non-existent ID")
	}
	if !strings.Contains(err.Error(), "w-nonexistent") {
		t.Errorf("error %q should mention the missing ID", err.Error())
	}
}

func TestShowWantedEmptyFields(t *testing.T) {
	store := newFakeWLCommonsStore()
	_ = store.InsertWanted(&doltserver.WantedItem{
		ID:    "w-sparse1",
		Title: "Minimal item",
		// All optional fields left empty
	})

	out := captureStdout(t, func() {
		if err := showWanted(store, "w-sparse1", false); err != nil {
			t.Errorf("showWanted() error: %v", err)
		}
	})

	if !strings.Contains(out, "w-sparse1") {
		t.Errorf("output missing ID\nfull output:\n%s", out)
	}
	if !strings.Contains(out, "Minimal item") {
		t.Errorf("output missing title\nfull output:\n%s", out)
	}
	// All labels must appear even when values are empty.
	for _, label := range []string{
		"ID:", "Title:", "Description:", "Project:", "Type:",
		"Priority:", "Tags:", "Posted By:", "Claimed By:", "Status:",
		"Effort:", "Evidence URL:",
	} {
		if !strings.Contains(out, label) {
			t.Errorf("output missing label %q\nfull output:\n%s", label, out)
		}
	}
	// Empty optional fields must render as "(none)".
	if strings.Count(out, "(none)") == 0 {
		t.Errorf("output missing (none) placeholder for empty fields\nfull output:\n%s", out)
	}
}

func TestShowWantedMultilineDescription(t *testing.T) {
	store := newFakeWLCommonsStore()
	_ = store.InsertWanted(&doltserver.WantedItem{
		ID:          "w-multi1",
		Title:       "Complex task",
		Description: "Line one\nLine two\nLine three",
	})

	out := captureStdout(t, func() {
		if err := showWanted(store, "w-multi1", false); err != nil {
			t.Errorf("showWanted() error: %v", err)
		}
	})

	if !strings.Contains(out, "Line one") {
		t.Errorf("output missing first line of description\nfull output:\n%s", out)
	}
	// All lines of a multiline description must appear.
	if !strings.Contains(out, "Line two") {
		t.Errorf("output missing second line of description\nfull output:\n%s", out)
	}
	if !strings.Contains(out, "Line three") {
		t.Errorf("output missing third line of description\nfull output:\n%s", out)
	}
	// Continuation lines must be indented with the 15-char (labelWidth+2) prefix.
	indent := strings.Repeat(" ", 15)
	if !strings.Contains(out, indent+"Line two") {
		t.Errorf("Line two not indented with %d-space prefix\nfull output:\n%s", 15, out)
	}
	if !strings.Contains(out, indent+"Line three") {
		t.Errorf("Line three not indented with %d-space prefix\nfull output:\n%s", 15, out)
	}
}
