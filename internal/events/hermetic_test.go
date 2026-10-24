package events

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/testutil/hermetic"
)

// TestHermeticEnv_FeedEventsStayOutOfEnclosingTown guards against tests
// writing to the live town's event feed. Polecat and crew worktrees live
// inside the town, so a test whose working directory is in such a worktree
// used to discover the town from its cwd and append events (nudges, "done")
// to the town's .events.jsonl, where agents and the feed act on them.
func TestHermeticEnv_FeedEventsStayOutOfEnclosingTown(t *testing.T) {
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "mayor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(town, "mayor", "town.json"), []byte(`{"name":"live"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(town, "gastown", "polecats", "nitro", "gastown")
	pkgDir := filepath.Join(worktree, "internal", "events")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "go.mod"), []byte("module example.com/gastown\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(pkgDir)
	feed := filepath.Join(town, EventsFile)

	// Without isolation the enclosing town is found from the cwd.
	t.Setenv(hermetic.IsolatedVar, "")
	t.Setenv(hermetic.TownSearchCeilingVar, "")
	if err := LogFeed(TypeNudge, "gastown/polecats/nitro", nil); err != nil {
		t.Fatalf("LogFeed without isolation: %v", err)
	}
	if _, err := os.Stat(feed); err != nil {
		t.Fatalf("precondition: expected un-isolated LogFeed to reach the enclosing town: %v", err)
	}
	if err := os.Remove(feed); err != nil {
		t.Fatal(err)
	}

	restore := hermetic.Isolate()
	defer restore()
	if err := LogFeed(TypeDone, "gastown/polecats/nitro", nil); err != nil {
		t.Fatalf("LogFeed: %v", err)
	}
	if _, err := os.Stat(feed); !os.IsNotExist(err) {
		t.Fatalf("isolated LogFeed wrote to the enclosing town's feed %s (stat err: %v)", feed, err)
	}
}
