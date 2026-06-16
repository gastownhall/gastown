package daemon

import (
	"log"
	"os"
	"testing"
)

func TestParseWispID(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantID string
	}{
		{
			name:   "standard wisp output",
			input:  "✓ Spawned wisp: gt-wisp-abc123 — Reap stale wisps",
			wantID: "gt-wisp-abc123",
		},
		{
			name:   "wisp ID with ANSI codes",
			input:  "\033[32m✓\033[0m Spawned wisp: \033[1mgt-wisp-xyz789\033[0m — Title",
			wantID: "gt-wisp-xyz789",
		},
		{
			name:   "empty output",
			input:  "",
			wantID: "",
		},
		{
			name:   "no wisp ID in output",
			input:  "Error: something went wrong",
			wantID: "",
		},
		{
			name:   "wisp ID at end of line",
			input:  "Created gt-wisp-def456",
			wantID: "gt-wisp-def456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseWispID(tt.input)
			if got != tt.wantID {
				t.Errorf("parseWispID(%q) = %q, want %q", tt.input, got, tt.wantID)
			}
		})
	}
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no ANSI", "hello", "hello"},
		{"color code", "\033[32mgreen\033[0m", "green"},
		{"bold", "\033[1mbold\033[0m", "bold"},
		{"multiple codes", "\033[32m✓\033[0m \033[1mtext\033[0m", "✓ text"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripANSI(tt.input)
			if got != tt.want {
				t.Errorf("stripANSI(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// newTestDogMol returns a dogMol with a discarding logger for unit tests.
func newTestDogMol() *dogMol {
	return &dogMol{
		stepIDs: make(map[string]string),
		logger:  log.New(os.Stderr, "", 0),
	}
}

func TestParsePour(t *testing.T) {
	t.Run("captures root and step IDs from bd mol wisp --json", func(t *testing.T) {
		// Exactly the shape `bd mol wisp mol-dog-doctor --json` emits.
		raw := `{
			"created": 4,
			"id_mapping": {
				"mol-dog-doctor": "hq-wisp-root1",
				"mol-dog-doctor.probe": "hq-wisp-probe1",
				"mol-dog-doctor.inspect": "hq-wisp-inspect1",
				"mol-dog-doctor.report": "hq-wisp-report1"
			},
			"new_epic_id": "hq-wisp-root1",
			"phase": "vapor",
			"schema_version": 1
		}`
		dm := newTestDogMol()
		dm.parsePour(raw, "mol-dog-doctor")

		if dm.rootID != "hq-wisp-root1" {
			t.Fatalf("rootID = %q, want hq-wisp-root1", dm.rootID)
		}
		want := map[string]string{
			"probe":   "hq-wisp-probe1",
			"inspect": "hq-wisp-inspect1",
			"report":  "hq-wisp-report1",
		}
		if len(dm.stepIDs) != len(want) {
			t.Fatalf("stepIDs = %v, want %v", dm.stepIDs, want)
		}
		for slug, id := range want {
			if dm.stepIDs[slug] != id {
				t.Errorf("stepIDs[%q] = %q, want %q", slug, dm.stepIDs[slug], id)
			}
		}
		// The root must not be recorded as a step.
		if _, ok := dm.stepIDs["mol-dog-doctor"]; ok {
			t.Error("root formula key must not be recorded as a step")
		}
	})

	t.Run("falls back to text root ID on non-JSON output", func(t *testing.T) {
		dm := newTestDogMol()
		dm.parsePour("✓ Created wisp: hq-wisp-fallback — mol-dog-doctor", "mol-dog-doctor")
		if dm.rootID != "hq-wisp-fallback" {
			t.Fatalf("rootID = %q, want hq-wisp-fallback (text fallback)", dm.rootID)
		}
		if len(dm.stepIDs) != 0 {
			t.Errorf("stepIDs should be empty on fallback, got %v", dm.stepIDs)
		}
	})

	t.Run("uses id_mapping root when new_epic_id is absent", func(t *testing.T) {
		raw := `{"id_mapping":{"mol-dog-reaper":"hq-wisp-r","mol-dog-reaper.scan":"hq-wisp-s"}}`
		dm := newTestDogMol()
		dm.parsePour(raw, "mol-dog-reaper")
		if dm.rootID != "hq-wisp-r" {
			t.Fatalf("rootID = %q, want hq-wisp-r", dm.rootID)
		}
		if dm.stepIDs["scan"] != "hq-wisp-s" {
			t.Errorf("stepIDs[scan] = %q, want hq-wisp-s", dm.stepIDs["scan"])
		}
	})
}

func TestDogMolGracefulDegradation(t *testing.T) {
	// A dogMol with empty rootID should be a no-op for all operations.
	dm := &dogMol{
		rootID:  "",
		stepIDs: make(map[string]string),
	}

	// These should not panic or error — graceful degradation.
	dm.closeStep("scan")
	dm.failStep("scan", "test failure")
	dm.close()
}
