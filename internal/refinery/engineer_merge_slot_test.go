package refinery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/rig"
)

func TestAcquireMainPushSlot_ImmediateAcquire(t *testing.T) {
	e := &Engineer{
		rig:    &rig.Rig{Name: "testrig"},
		output: io.Discard,
		mergeSlotEnsureExists: func() (string, error) {
			return "merge-slot", nil
		},
		mergeSlotAcquire: func(holder string, _ bool) (*beads.MergeSlotStatus, error) {
			return &beads.MergeSlotStatus{ID: "merge-slot", Available: true, Holder: holder}, nil
		},
		mergeSlotRelease: func(_ string) error { return nil },
	}

	holder, err := e.acquireMainPushSlot(context.Background())
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if holder == "" {
		t.Fatal("expected non-empty holder")
	}
	if !strings.HasPrefix(holder, "testrig/refinery/push/") {
		t.Errorf("holder %q does not start with expected prefix", holder)
	}
}

func TestAcquireMainPushSlot_RetrySuccess(t *testing.T) {
	var attempts int

	e := &Engineer{
		rig:                   &rig.Rig{Name: "testrig"},
		output:                io.Discard,
		mergeSlotMaxRetries:   3,
		mergeSlotRetryBackoff: time.Millisecond, // Fast for tests
		mergeSlotEnsureExists: func() (string, error) {
			return "merge-slot", nil
		},
		mergeSlotAcquire: func(holder string, _ bool) (*beads.MergeSlotStatus, error) {
			attempts++
			if attempts <= 2 {
				return &beads.MergeSlotStatus{ID: "merge-slot", Available: false, Holder: "other/refinery"}, nil
			}
			return &beads.MergeSlotStatus{ID: "merge-slot", Available: true, Holder: holder}, nil
		},
		mergeSlotRelease: func(_ string) error { return nil },
	}

	holder, err := e.acquireMainPushSlot(context.Background())
	if err != nil {
		t.Fatalf("expected success after retry, got error: %v", err)
	}
	if holder == "" {
		t.Fatal("expected non-empty holder")
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestAcquireMainPushSlot_MaxRetriesExceeded(t *testing.T) {
	e := &Engineer{
		rig:                   &rig.Rig{Name: "testrig"},
		output:                io.Discard,
		mergeSlotMaxRetries:   2,
		mergeSlotRetryBackoff: time.Millisecond,
		mergeSlotEnsureExists: func() (string, error) {
			return "merge-slot", nil
		},
		mergeSlotAcquire: func(_ string, _ bool) (*beads.MergeSlotStatus, error) {
			return &beads.MergeSlotStatus{ID: "merge-slot", Available: false, Holder: "other/refinery"}, nil
		},
		mergeSlotRelease: func(_ string) error { return nil },
	}

	_, err := e.acquireMainPushSlot(context.Background())
	if err == nil {
		t.Fatal("expected error when max retries exceeded")
	}
	if !errors.Is(err, errMergeSlotTimeout) {
		t.Errorf("expected errMergeSlotTimeout sentinel, got: %v", err)
	}
}

func TestAcquireMainPushSlot_SelfConflictHolderBypass(t *testing.T) {
	// When the slot is held by our own rig's conflict-resolution holder,
	// acquireMainPushSlot should proceed without acquiring (returns empty holder).
	e := &Engineer{
		rig:    &rig.Rig{Name: "testrig"},
		output: io.Discard,
		mergeSlotEnsureExists: func() (string, error) {
			return "merge-slot", nil
		},
		mergeSlotAcquire: func(_ string, _ bool) (*beads.MergeSlotStatus, error) {
			// Slot held by conflict-resolution path
			return &beads.MergeSlotStatus{ID: "merge-slot", Available: false, Holder: "testrig/refinery"}, nil
		},
		mergeSlotRelease: func(_ string) error { return nil },
	}

	holder, err := e.acquireMainPushSlot(context.Background())
	if err != nil {
		t.Fatalf("expected success when self-conflict holder, got error: %v", err)
	}
	if holder != "" {
		t.Errorf("expected empty holder (bypass), got %q", holder)
	}
}

func TestAcquireMainPushSlot_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	e := &Engineer{
		rig:                   &rig.Rig{Name: "testrig"},
		output:                io.Discard,
		mergeSlotMaxRetries:   10,
		mergeSlotRetryBackoff: time.Second, // Slow enough to allow cancellation
		mergeSlotEnsureExists: func() (string, error) {
			return "merge-slot", nil
		},
		mergeSlotAcquire: func(_ string, _ bool) (*beads.MergeSlotStatus, error) {
			return &beads.MergeSlotStatus{ID: "merge-slot", Available: false, Holder: "other/refinery"}, nil
		},
		mergeSlotRelease: func(_ string) error { return nil },
	}

	// Cancel after a short delay — should interrupt the retry sleep
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := e.acquireMainPushSlot(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error on context cancellation")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("expected context canceled error, got: %v", err)
	}
	// Should have exited much faster than a full retry cycle
	if elapsed > 2*time.Second {
		t.Errorf("cancellation took too long: %v (expected <2s)", elapsed)
	}
}

func TestAcquireMainPushSlot_ConcurrentSingleWriter(t *testing.T) {
	var mu sync.Mutex
	currentHolder := ""

	e := &Engineer{
		rig:                   &rig.Rig{Name: "testrig"},
		output:                io.Discard,
		mergeSlotMaxRetries:   0, // No retry — fail immediately if held
		mergeSlotRetryBackoff: time.Millisecond,
		mergeSlotEnsureExists: func() (string, error) {
			return "merge-slot", nil
		},
		mergeSlotAcquire: func(holder string, _ bool) (*beads.MergeSlotStatus, error) {
			mu.Lock()
			defer mu.Unlock()
			if currentHolder == "" {
				currentHolder = holder
				return &beads.MergeSlotStatus{ID: "merge-slot", Available: true, Holder: holder}, nil
			}
			return &beads.MergeSlotStatus{ID: "merge-slot", Available: false, Holder: currentHolder}, nil
		},
		mergeSlotRelease: func(holder string) error {
			mu.Lock()
			defer mu.Unlock()
			if currentHolder != holder {
				return fmt.Errorf("holder mismatch: %s != %s", currentHolder, holder)
			}
			currentHolder = ""
			return nil
		},
	}

	// Both goroutines attempt acquisition, then signal completion via results channel.
	// The barrier ensures neither goroutine exits until both have attempted,
	// preventing the race where one acquires+releases before the other starts.
	start := make(chan struct{})
	barrier := make(chan struct{})
	var wg sync.WaitGroup

	type result struct {
		holder string
		err    error
	}
	results := make(chan result, 2)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			holder, err := e.acquireMainPushSlot(ctx)
			results <- result{holder, err}
			<-barrier // Wait until both have attempted
		}()
	}
	close(start)

	// Collect both results
	r1 := <-results
	r2 := <-results

	// Release barrier so goroutines can finish
	close(barrier)
	wg.Wait()

	var successCount, failCount int
	for _, r := range []result{r1, r2} {
		if r.err != nil {
			failCount++
		} else {
			successCount++
		}
	}

	if successCount != 1 {
		t.Fatalf("expected exactly one successful slot acquisition, got %d", successCount)
	}
	if failCount != 1 {
		t.Fatalf("expected exactly one failed slot acquisition, got %d", failCount)
	}
}

func TestAcquireMainPushSlot_BackoffConverges(t *testing.T) {
	var attempts int

	e := &Engineer{
		rig:                   &rig.Rig{Name: "testrig"},
		output:                io.Discard,
		mergeSlotMaxRetries:   6,
		mergeSlotRetryBackoff: time.Millisecond, // Use millisecond to keep test fast
		mergeSlotEnsureExists: func() (string, error) {
			return "merge-slot", nil
		},
		mergeSlotAcquire: func(holder string, _ bool) (*beads.MergeSlotStatus, error) {
			attempts++
			if attempts <= 6 {
				return &beads.MergeSlotStatus{ID: "merge-slot", Available: false, Holder: "other/refinery"}, nil
			}
			return &beads.MergeSlotStatus{ID: "merge-slot", Available: true, Holder: holder}, nil
		},
		mergeSlotRelease: func(_ string) error { return nil },
	}

	// Verify the retry loop converges: the function completes within the
	// configured retry count and backoff doesn't grow unbounded.
	_, err := e.acquireMainPushSlot(context.Background())
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if attempts != 7 {
		t.Errorf("expected 7 attempts (1 initial + 6 retries), got %d", attempts)
	}
}

func TestAcquireMainPushSlot_EnsureExistsError_NotTimeout(t *testing.T) {
	// Infrastructure errors from mergeSlotEnsureExists must NOT be
	// errMergeSlotTimeout — they indicate beads is down, not contention.
	e := &Engineer{
		rig:    &rig.Rig{Name: "testrig"},
		output: io.Discard,
		mergeSlotEnsureExists: func() (string, error) {
			return "", fmt.Errorf("beads database unavailable")
		},
		mergeSlotAcquire: func(_ string, _ bool) (*beads.MergeSlotStatus, error) {
			t.Fatal("acquire should not be called when ensure fails")
			return nil, nil
		},
		mergeSlotRelease: func(_ string) error { return nil },
	}

	_, err := e.acquireMainPushSlot(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, errMergeSlotTimeout) {
		t.Errorf("infrastructure error should NOT be errMergeSlotTimeout, got: %v", err)
	}
}

func TestAcquireMainPushSlot_AcquireError_NotTimeout(t *testing.T) {
	// Infrastructure errors from mergeSlotAcquire (e.g., permission denied)
	// must NOT be errMergeSlotTimeout.
	e := &Engineer{
		rig:    &rig.Rig{Name: "testrig"},
		output: io.Discard,
		mergeSlotEnsureExists: func() (string, error) {
			return "merge-slot", nil
		},
		mergeSlotAcquire: func(_ string, _ bool) (*beads.MergeSlotStatus, error) {
			return nil, fmt.Errorf("permission denied")
		},
		mergeSlotRelease: func(_ string) error { return nil },
	}

	_, err := e.acquireMainPushSlot(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, errMergeSlotTimeout) {
		t.Errorf("infrastructure error should NOT be errMergeSlotTimeout, got: %v", err)
	}
}

func TestAcquireMainPushSlot_NilStatus_NotTimeout(t *testing.T) {
	// Nil status from mergeSlotAcquire is an infrastructure anomaly,
	// not contention — must NOT be errMergeSlotTimeout.
	e := &Engineer{
		rig:    &rig.Rig{Name: "testrig"},
		output: io.Discard,
		mergeSlotEnsureExists: func() (string, error) {
			return "merge-slot", nil
		},
		mergeSlotAcquire: func(_ string, _ bool) (*beads.MergeSlotStatus, error) {
			return nil, nil
		},
		mergeSlotRelease: func(_ string) error { return nil },
	}

	_, err := e.acquireMainPushSlot(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, errMergeSlotTimeout) {
		t.Errorf("nil-status error should NOT be errMergeSlotTimeout, got: %v", err)
	}
}
