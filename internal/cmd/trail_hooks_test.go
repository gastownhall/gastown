package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/events"
)

func writeTrailEventsFile(t *testing.T, path string, entries []events.Event) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating events file: %v", err)
	}
	defer f.Close()

	for _, entry := range entries {
		b, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshaling event: %v", err)
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatalf("writing event: %v", err)
		}
	}
}

func TestReadHookTrailEntriesMissingFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".events.jsonl")

	got, err := readHookTrailEntries(path, time.Time{}, 20)
	if err != nil {
		t.Fatalf("readHookTrailEntries() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("readHookTrailEntries() len = %d, want 0", len(got))
	}
}

func TestReadHookTrailEntriesFiltersAndOrders(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".events.jsonl")
	base := time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC)

	writeTrailEventsFile(t, path, []events.Event{
		{
			Timestamp: base.Add(-4 * time.Hour).Format(time.RFC3339),
			Type:      events.TypeSling,
			Actor:     "rig/crew/kim",
			Payload:   map[string]interface{}{"bead": "gt-100"},
		},
		{
			Timestamp: base.Add(-3 * time.Hour).Format(time.RFC3339),
			Type:      events.TypeHook,
			Actor:     "rig/polecats/kim",
			Payload:   map[string]interface{}{"bead": "gt-101"},
		},
		{
			Timestamp: base.Add(-2 * time.Hour).Format(time.RFC3339),
			Type:      events.TypeUnhook,
			Actor:     "rig/polecats/lee",
			Payload:   map[string]interface{}{"bead": "gt-102"},
		},
		{
			Timestamp: base.Add(-1 * time.Hour).Format(time.RFC3339),
			Type:      events.TypeHook,
			Actor:     "",
			Payload:   map[string]interface{}{"bead": "gt-103"},
		},
	})

	got, err := readHookTrailEntries(path, time.Time{}, 10)
	if err != nil {
		t.Fatalf("readHookTrailEntries() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("readHookTrailEntries() len = %d, want 3", len(got))
	}

	if got[0].Type != events.TypeHook || got[0].Bead != "gt-103" || got[0].Actor != "unknown" {
		t.Fatalf("first entry = %+v, want newest hook with unknown actor", got[0])
	}
	if got[1].Type != events.TypeUnhook || got[1].Bead != "gt-102" {
		t.Fatalf("second entry = %+v, want unhook gt-102", got[1])
	}
	if got[2].Type != events.TypeHook || got[2].Bead != "gt-101" {
		t.Fatalf("third entry = %+v, want hook gt-101", got[2])
	}
}

func TestReadHookTrailEntriesSinceAndLimit(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".events.jsonl")
	base := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)

	writeTrailEventsFile(t, path, []events.Event{
		{
			Timestamp: base.Add(-3 * time.Hour).Format(time.RFC3339),
			Type:      events.TypeHook,
			Actor:     "rig/polecats/a",
			Payload:   map[string]interface{}{"bead": "gt-201"},
		},
		{
			Timestamp: base.Add(-2 * time.Hour).Format(time.RFC3339),
			Type:      events.TypeUnhook,
			Actor:     "rig/polecats/b",
			Payload:   map[string]interface{}{"bead": "gt-202"},
		},
		{
			Timestamp: base.Add(-1 * time.Hour).Format(time.RFC3339),
			Type:      events.TypeHook,
			Actor:     "rig/polecats/c",
			Payload:   map[string]interface{}{"bead": "gt-203"},
		},
	})

	since := base.Add(-90 * time.Minute)
	got, err := readHookTrailEntries(path, since, 1)
	if err != nil {
		t.Fatalf("readHookTrailEntries() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("readHookTrailEntries() len = %d, want 1", len(got))
	}
	if got[0].Bead != "gt-203" || got[0].Type != events.TypeHook {
		t.Fatalf("entry = %+v, want newest hook gt-203", got[0])
	}
}
