package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/style"
)

// A bead ID is an identifier, and a prefix of one is not a shorter identifier --
// it is an ambiguous one. gt mq list used to cut the ID to 12 characters, and real
// IDs are 13, so the queue printed a prefix. Observed on alphaprime 2026-09-09:
// the queue showed "ap-wisp-osik", and `bd show ap-wisp-osik` matched TWO beads --
// ap-wisp-osik8, the actual merge request, and ap-wisp-osik6, an unrelated task.
// Anything resolving the displayed value loosely could act on the wrong bead, and
// gt mq list is the documented source of truth for the queue.
func TestMQListIDColumnFitsARealBeadID(t *testing.T) {
	// The collision that was actually observed, plus the longest shapes in use.
	ids := []string{"ap-wisp-osik8", "ap-wisp-osik6", "hq-wisp-tuudi", "hq-cv-w2jw6"}

	var idCol style.Column
	found := false
	for _, c := range buildMQListColumns(false) {
		if c.Name == "ID" {
			idCol, found = c, true
			break
		}
	}
	if !found {
		t.Fatal("buildMQListColumns has no ID column")
	}

	for _, id := range ids {
		if len(id) > idCol.Width {
			t.Errorf("ID column Width=%d cannot hold %q (%d chars); the renderer would cut it",
				idCol.Width, id, len(id))
		}
	}
}

// The ID must survive rendering intact -- not merely fit the column in principle.
func TestMQListRendersFullBeadID(t *testing.T) {
	const id = "ap-wisp-osik8"

	table := style.NewTable(buildMQListColumns(false)...)
	table.AddRow(id, "1.0", "P1", "cv-x", "branch", "main", "open", "1h")
	out := table.Render()

	if !strings.Contains(out, id) {
		t.Fatalf("rendered queue does not contain the full ID %q.\nGot:\n%s", id, out)
	}
	// The ambiguous 12-character prefix must never appear on its own.
	if strings.Contains(out, id[:12]) && !strings.Contains(out, id) {
		t.Fatalf("rendered queue shows the ambiguous prefix %q instead of %q", id[:12], id)
	}
}
