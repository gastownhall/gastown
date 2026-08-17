package worker

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Every accessor builds its own Store, and the daemon writes the same files
// from another process, so writers reach saveIndex with no shared lock. Staging
// under one fixed name made them race: the winner's rename consumed the staging
// file and the loser's rename failed with ENOENT.
func TestSaveIndexSurvivesConcurrentWriters(t *testing.T) {
	townRoot := t.TempDir()

	const writers = 16
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := newStore(townRoot)
			errs[i] = store.putRun(&Run{RunID: fmt.Sprintf("run-%d", i), SessionID: "hq-mayor", State: StateStarted})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("writer %d: %v", i, err)
		}
	}

	// A surviving index proves the writers renamed a real staging file rather
	// than leaving runs.json absent or truncated.
	idx, err := newStore(townRoot).loadIndex()
	if err != nil {
		t.Fatalf("loadIndex after concurrent writers: %v", err)
	}
	if len(idx.Runs) == 0 {
		t.Fatal("index holds no runs after concurrent writers")
	}
}

func TestWriteFileAtomicLeavesNoStagingFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.json")

	if err := writeFileAtomic(path, []byte(`{"runs":{}}`)); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading replaced file: %v", err)
	}
	if string(got) != `{"runs":{}}` {
		t.Fatalf("contents = %q", got)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != "runs.json" {
			t.Errorf("leftover staging file %q", entry.Name())
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != fileMode {
		t.Errorf("mode = %v, want %v", info.Mode().Perm(), os.FileMode(fileMode))
	}
}
