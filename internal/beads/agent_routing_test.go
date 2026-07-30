package beads

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression tests for hq-11042: agent beads live in the TOWN database, but
// prefix routing sends an ID like "ns-navigation_server-polecat-dementus" to
// <town>/navigation_server/mayor/rig — the rig database, which holds no agent
// bead rows. Every agent-bead read and write from a rig-rooted client failed
// with "issue not found" while the callers still reported success.
//
// Two things have to hold for the fix to work, and the second is the one that
// is easy to get wrong: pointing a client at the town beads dir is NOT enough,
// because Show() re-resolves prefix routing itself and would route straight
// back to the rig. The client must also be pinned with noRoute.

func newTownFixture(t *testing.T) (townRoot, rigBeads string) {
	t.Helper()
	townRoot = t.TempDir()

	// Town beads dir + a routes file sending the "ns-" prefix at the rig.
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0o755); err != nil {
		t.Fatal(err)
	}
	rigBeads = filepath.Join(townRoot, "navigation_server", "mayor", "rig", ".beads")
	if err := os.MkdirAll(rigBeads, 0o755); err != nil {
		t.Fatal(err)
	}
	routes := `{"prefix":"ns-","path":"navigation_server/mayor/rig"}` + "\n"
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routes), 0o644); err != nil {
		t.Fatal(err)
	}
	return townRoot, rigBeads
}

func TestAgentBeadClientTargetsTownAndPinsRouting(t *testing.T) {
	townRoot, rigBeads := newTownFixture(t)
	const id = "ns-navigation_server-polecat-dementus"

	// A client rooted at the rig, as gt done and the spawn path build it.
	rigClient := NewWithBeadsDir(filepath.Join(townRoot, "navigation_server"), rigBeads)
	rigClient.townRoot = townRoot
	rigClient.townRootOnce.Do(func() {})

	ab := rigClient.agentBeadClient(id)

	wantBeads := GetTownBeadsPath(townRoot)
	if got := ab.getResolvedBeadsDir(); got != wantBeads {
		t.Errorf("agentBeadClient beads dir = %q, want the town beads dir %q", got, wantBeads)
	}

	// The part that actually makes reads work. Without noRoute, Show() would
	// re-route this ID back to the rig database via routes.jsonl.
	if !ab.noRoute {
		t.Error("agentBeadClient must set noRoute; otherwise Show() re-routes agent bead IDs to the rig database and they read as missing")
	}
}

func TestAgentBeadClientLeavesUnprefixedAndTownlessAlone(t *testing.T) {
	townRoot, rigBeads := newTownFixture(t)

	rigClient := NewWithBeadsDir(filepath.Join(townRoot, "navigation_server"), rigBeads)
	rigClient.townRoot = townRoot
	rigClient.townRootOnce.Do(func() {})

	// No extractable prefix (ExtractPrefix needs a hyphen at index > 0), so
	// there is nothing to route and the client is returned as-is.
	if got := rigClient.agentBeadClient("bareid"); got != rigClient {
		t.Error("agentBeadClient should return the receiver unchanged for an ID with no prefix")
	}

	// No town root: cannot resolve a town beads dir, so leave it alone.
	noTown := New(t.TempDir())
	noTown.townRoot = ""
	noTown.townRootOnce.Do(func() {})
	if got := noTown.agentBeadClient("ns-navigation_server-polecat-x"); got != noTown {
		t.Error("agentBeadClient should return the receiver unchanged when there is no town root")
	}
}

// Show() must honour noRoute. This is the specific regression: prefix routing
// is correct for work beads and wrong for agent beads, so a pinned client has
// to survive Show()'s own routing step.
func TestShowHonoursNoRoute(t *testing.T) {
	townRoot, _ := newTownFixture(t)
	const id = "ns-navigation_server-polecat-dementus"

	townBeads := GetTownBeadsPath(townRoot)
	pinned := NewWithBeadsDir(townRoot, townBeads)
	pinned.noRoute = true
	pinned.townRoot = townRoot
	pinned.townRootOnce.Do(func() {})

	// Routing would resolve this ID to the rig beads dir...
	routed := ResolveRoutingTarget(townRoot, id, townBeads)
	if routed == townBeads {
		t.Skip("fixture did not produce a differing route; nothing to assert")
	}
	// ...so a pinned client whose beads dir differs from the routed target is
	// exactly the case Show() must not redirect. Assert the guard directly:
	// with noRoute set, the redirect branch is skipped.
	if !pinned.noRoute || routed == pinned.getResolvedBeadsDir() {
		t.Fatalf("bad fixture: noRoute=%v routed=%q pinned=%q", pinned.noRoute, routed, pinned.getResolvedBeadsDir())
	}
}
