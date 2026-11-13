package doctor

import (
	"testing"
	"time"
)

// TestCountAbandonedWisps covers the two ways this count went wrong (hq-675):
// the `bd mol wisp list --json` envelope was decoded as a bare array (always
// zero), and HOOKED wisps — live agent molecules — counted as abandoned.
func TestCountAbandonedWisps(t *testing.T) {
	cutoff := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	payload := []byte(`{
	  "wisps": [
	    {"id": "hq-wisp-old",    "status": "open",   "updated_at": "2026-08-27T09:00:00Z"},
	    {"id": "hq-wisp-fresh",  "status": "open",   "updated_at": "2026-08-27T12:30:00Z"},
	    {"id": "hq-wisp-hooked", "status": "hooked", "updated_at": "2026-08-27T09:00:00Z"},
	    {"id": "hq-wisp-closed", "status": "closed", "updated_at": "2026-08-27T09:00:00Z"},
	    {"id": "hq-wisp-nots",   "status": "open",   "updated_at": ""}
	  ],
	  "count": 5
	}`)

	if got := countAbandonedWisps(payload, cutoff); got != 1 {
		t.Errorf("countAbandonedWisps() = %d, want 1 (only the stale open wisp)", got)
	}
}

// TestCountAbandonedWispsEmpty verifies the empty and malformed cases are 0,
// not a panic.
func TestCountAbandonedWispsEmpty(t *testing.T) {
	cutoff := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	tests := map[string]string{
		"empty list": `{"wisps": [], "count": 0}`,
		"null list":  `{"wisps": null, "count": 0}`,
		"malformed":  `not json`,
	}

	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if got := countAbandonedWisps([]byte(payload), cutoff); got != 0 {
				t.Errorf("countAbandonedWisps(%s) = %d, want 0", name, got)
			}
		})
	}
}
