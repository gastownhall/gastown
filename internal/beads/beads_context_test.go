package beads

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	beadsdk "github.com/steveyegge/beads"
)

type contextBlockingStore struct {
	*mockStorage
}

func (s *contextBlockingStore) SearchIssues(ctx context.Context, _ string, _ beadsdk.IssueFilter) ([]*beadsdk.Issue, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestWithContextCancelsSlowBDQuery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell bd stub")
	}

	root := t.TempDir()
	workDir := filepath.Join(root, "rig")
	if err := os.MkdirAll(filepath.Join(workDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}
	stub := `#!/bin/sh
if [ "${1:-}" = "--allow-stale" ]; then
  shift
fi
if [ "${1:-}" = "version" ]; then
  echo "bd test"
  exit 0
fi
sleep 30
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(stub), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ResetBdAllowStaleCacheForTest()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := NewIsolated(workDir).WithContext(ctx).List(ListOptions{
		Status:   StatusHooked,
		Assignee: "gastown/polecats/test",
		Priority: -1,
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("slow bd list unexpectedly succeeded")
	}
	if elapsed > time.Second {
		t.Fatalf("caller deadline was not propagated: query returned after %s", elapsed)
	}
	if !strings.Contains(err.Error(), "bd list") {
		t.Fatalf("error does not identify the canceled query: %v", err)
	}
}

func TestWithContextCancelsSlowStoreQuery(t *testing.T) {
	store := &contextBlockingStore{mockStorage: newMockStorage()}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := NewWithStore("/tmp/test", store).WithContext(ctx).List(ListOptions{})
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("slow store query error = %v, want deadline exceeded", err)
	}
	if elapsed > time.Second {
		t.Fatalf("store query ignored caller deadline: %s", elapsed)
	}
}
