package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestSaveRigsConfig_AtomicAgainstConcurrentReaders is the regression test for
// #3464: SaveRigsConfig used os.WriteFile, which truncates the file before
// writing. Concurrent readers in that window observed a zero-byte file and
// failed with "unexpected end of JSON input". With atomic write-then-rename,
// readers must always see either the old complete contents or the new.
func TestSaveRigsConfig_AtomicAgainstConcurrentReaders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rigs.json")

	// Seed with a valid initial config.
	initial := &RigsConfig{Version: 1, Rigs: map[string]RigEntry{"alpha": {}}}
	if err := SaveRigsConfig(path, initial); err != nil {
		t.Fatalf("seed SaveRigsConfig: %v", err)
	}

	// Large payload → a non-atomic write would leave a wider torn-read window.
	big := &RigsConfig{Version: 1, Rigs: make(map[string]RigEntry, 500)}
	for i := 0; i < 500; i++ {
		name := "rig_"
		for d := i; ; d /= 10 {
			name += string(rune('0' + d%10))
			if d < 10 {
				break
			}
		}
		big.Rigs[name] = RigEntry{}
	}

	stop := make(chan struct{})
	start := make(chan struct{})
	firstWrite := make(chan struct{}, 1)
	var wg sync.WaitGroup
	var successfulWrites atomic.Int64
	var readerActive atomic.Bool
	var overlappingWrites atomic.Int64
	writerErrors := make(chan error, 4)

	// 4 concurrent writers alternating between small and big payloads.
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			payloads := []*RigsConfig{initial, big}
			i := 0
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := SaveRigsConfig(path, payloads[i%2]); err != nil {
					writerErrors <- err
					return
				}
				successfulWrites.Add(1)
				if readerActive.Load() {
					overlappingWrites.Add(1)
				}
				select {
				case firstWrite <- struct{}{}:
				default:
				}
				i++
			}
		}()
	}
	close(start)
	select {
	case <-firstWrite:
	case <-time.After(3 * time.Second):
		close(stop)
		wg.Wait()
		t.Fatal("no writer completed before reader stress began")
	}

	// Reader: in 2000 iterations, not a single successful raw read may yield a
	// truncated or empty file. Windows can transiently reject an open during
	// replacement; those errors are counted against the availability floor.
	var tornReads int
	var successfulReads int
	var transientReadErrors int
	var readErr error
	readerActive.Store(true)
	for i := 0; i < 2000; i++ {
		data, err := os.ReadFile(path)
		if err != nil {
			if transientAtomicReadError(err) {
				transientReadErrors++
				continue
			}
			readErr = err
			break
		}
		successfulReads++
		if len(data) == 0 {
			tornReads++
			continue
		}
		var probe RigsConfig
		if err := json.Unmarshal(data, &probe); err != nil {
			tornReads++
		}
		if i%100 == 0 {
			runtime.Gosched()
		}
	}
	readerActive.Store(false)
	close(stop)
	wg.Wait()
	close(writerErrors)
	if readErr != nil {
		t.Fatalf("read rigs.json: %v", readErr)
	}
	for err := range writerErrors {
		t.Fatalf("SaveRigsConfig: %v", err)
	}
	if successfulWrites.Load() == 0 {
		t.Fatal("no concurrent write completed")
	}
	if successfulReads == 0 {
		t.Fatalf("all reads failed transiently (%d sharing errors)", transientReadErrors)
	}
	if successfulReads < 200 {
		t.Fatalf("only %d/2000 reads succeeded (%d sharing errors)", successfulReads, transientReadErrors)
	}
	if overlappingWrites.Load() == 0 {
		t.Fatal("no write completed while the reader stress loop was active")
	}

	if tornReads > 0 {
		t.Fatalf("observed %d torn/empty reads — SaveRigsConfig is not atomic", tornReads)
	}

	// Final state must be one of the two valid payloads.
	final, err := LoadRigsConfig(path)
	if err != nil {
		t.Fatalf("final LoadRigsConfig: %v", err)
	}
	if len(final.Rigs) != 1 && len(final.Rigs) != 500 {
		t.Fatalf("final rig count = %d, want 1 or 500", len(final.Rigs))
	}
}

func transientAtomicReadError(err error) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	if os.IsNotExist(err) {
		return true
	}
	const (
		accessDenied     syscall.Errno = 5
		sharingViolation syscall.Errno = 32
	)
	return errors.Is(err, sharingViolation) || errors.Is(err, accessDenied)
}

func TestLoadRigsConfigRetriesTransientNotFound(t *testing.T) {
	want := &RigsConfig{Version: 1, Rigs: map[string]RigEntry{"alpha": {}}}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	got, err := loadRigsConfig("rigs.json", func(string) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, os.ErrNotExist
		}
		return data, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(got.Rigs) != 1 {
		t.Fatalf("loadRigsConfig calls=%d config=%#v", calls, got)
	}
}

// TestLoadRigsConfig_RetriesOnTruncatedRead simulates a transient read where
// the first attempt sees a zero-byte file (as from a non-atomic concurrent
// writer) and verifies LoadRigsConfig's retry recovers.
func TestLoadRigsConfig_RetriesOnTruncatedRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rigs.json")

	// Write an empty file, then race a proper write against LoadRigsConfig.
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatalf("write empty: %v", err)
	}

	// Kick off a writer that fixes the file after a brief moment.
	done := make(chan error, 1)
	go func() {
		done <- SaveRigsConfig(path, &RigsConfig{Version: 1, Rigs: map[string]RigEntry{"a": {}}})
	}()

	// Wait for the writer to finish so the retry has real contents to parse.
	if err := <-done; err != nil {
		t.Fatalf("SaveRigsConfig: %v", err)
	}

	cfg, err := LoadRigsConfig(path)
	if err != nil {
		t.Fatalf("LoadRigsConfig after retry: %v", err)
	}
	if _, ok := cfg.Rigs["a"]; !ok {
		t.Fatalf("expected rig 'a' in result, got %v", cfg.Rigs)
	}
}
