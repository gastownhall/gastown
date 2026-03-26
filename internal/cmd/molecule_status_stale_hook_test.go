package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestLookupHookedWork_SkipsStaleClosedHookBead verifies that the hook_bead DB
// slot pointing to a closed bead is ignored and the fallback query is used to
// find the new active work (gt-gkh2x4).
//
// Scenario: a polecat completed prior work (bead now closed), then was re-assigned
// via gt sling --force. The agent bead's hook_bead DB slot still points to the old
// closed bead. Without the fix, lookupHookedWork returned the closed bead, causing
// the polecat to crash on wake.
func TestLookupHookedWork_SkipsStaleClosedHookBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	// Minimal town workspace: mayor/town.json + .beads/
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0o755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// bd stub: agent bead has hook_bead=gt-old-closed-1 (closed).
	// Fallback list query returns gt-new-work-2 (hooked) for the reassigned work.
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
# Strip --allow-stale if present
if [ "$1" = "--allow-stale" ]; then shift; fi
cmd="$1"
shift
case "$cmd" in
  version)
    echo "stub"
    ;;
  show)
    case "$1" in
      gt-gastown-polecat-furiosa)
        # Agent bead with stale hook_bead pointing to a closed bead
        printf '[{"id":"gt-gastown-polecat-furiosa","title":"Furiosa","issue_type":"agent","status":"open","hook_bead":"gt-old-closed-1","description":""}]'
        ;;
      gt-old-closed-1)
        # Old bead is closed — should NOT be returned by lookupHookedWork
        printf '[{"id":"gt-old-closed-1","title":"Old work (done)","issue_type":"task","status":"closed","description":""}]'
        ;;
      *)
        echo '[]'
        ;;
    esac
    ;;
  list)
    for arg in "$@"; do
      case "$arg" in
        --status=hooked)
          printf '[{"id":"gt-new-work-2","title":"New work","issue_type":"task","status":"hooked","assignee":"gastown/polecats/furiosa","description":""}]'
          exit 0
          ;;
      esac
    done
    echo '[]'
    ;;
  *)
    exit 1
    ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BEADS_DIR", filepath.Join(townRoot, ".beads"))
	t.Setenv("GT_ROLE", "gastown/polecats/furiosa")
	t.Setenv("GT_POLECAT", "furiosa")

	// Chdir into townRoot so workspace.FindFromCwd() locates it
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	prevJSON := moleculeJSON
	t.Cleanup(func() { moleculeJSON = prevJSON })
	moleculeJSON = true

	out := captureStdout(t, func() {
		if err := runMoleculeStatus(nil, []string{"gastown/polecats/furiosa"}); err != nil {
			t.Fatalf("runMoleculeStatus: %v", err)
		}
	})

	// Parse the JSON output and verify the pinned bead is the new hooked work.
	var status MoleculeStatusInfo
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &status); err != nil {
		t.Fatalf("parse output JSON: %v\noutput: %s", err, out)
	}

	if !status.HasWork {
		t.Fatal("expected has_work=true (new hooked bead found via fallback)")
	}
	if status.PinnedBead == nil {
		t.Fatal("expected pinned_bead to be set")
	}
	if status.PinnedBead.ID != "gt-new-work-2" {
		t.Errorf("pinned_bead.id = %q, want %q", status.PinnedBead.ID, "gt-new-work-2")
	}
	if status.PinnedBead.Status != beads.StatusHooked {
		t.Errorf("pinned_bead.status = %q, want %q", status.PinnedBead.Status, beads.StatusHooked)
	}
}
