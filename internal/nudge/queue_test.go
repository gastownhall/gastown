package nudge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEnqueueAndDrain(t *testing.T) {
	townRoot := t.TempDir()

	session := "gt-gastown-crew-sean"
	n1 := QueuedNudge{
		Sender:   "mayor",
		Message:  "Check your hook",
		Priority: PriorityNormal,
	}
	n2 := QueuedNudge{
		Sender:   "gastown/witness",
		Message:  "Polecat alpha is stuck",
		Priority: PriorityUrgent,
	}

	// Enqueue two nudges
	if err := Enqueue(townRoot, session, n1); err != nil {
		t.Fatalf("Enqueue n1: %v", err)
	}
	// Small delay to ensure different timestamps
	time.Sleep(time.Millisecond)
	if err := Enqueue(townRoot, session, n2); err != nil {
		t.Fatalf("Enqueue n2: %v", err)
	}

	// Check pending count
	count, err := Pending(townRoot, session)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if count != 2 {
		t.Errorf("Pending = %d, want 2", count)
	}

	// Drain
	nudges, err := Drain(townRoot, session)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != 2 {
		t.Fatalf("Drain returned %d nudges, want 2", len(nudges))
	}

	// Verify FIFO order
	if nudges[0].Sender != "mayor" {
		t.Errorf("nudges[0].Sender = %q, want %q", nudges[0].Sender, "mayor")
	}
	if nudges[1].Sender != "gastown/witness" {
		t.Errorf("nudges[1].Sender = %q, want %q", nudges[1].Sender, "gastown/witness")
	}

	// After drain, pending should be 0
	count, err = Pending(townRoot, session)
	if err != nil {
		t.Fatalf("Pending after drain: %v", err)
	}
	if count != 0 {
		t.Errorf("Pending after drain = %d, want 0", count)
	}
}

func TestDrainEmptyQueue(t *testing.T) {
	townRoot := t.TempDir()

	nudges, err := Drain(townRoot, "nonexistent-session")
	if err != nil {
		t.Fatalf("Drain empty: %v", err)
	}
	if len(nudges) != 0 {
		t.Errorf("Drain empty returned %d nudges, want 0", len(nudges))
	}
}

func TestDrainSkipsMalformed(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test"

	// Create queue dir and a malformed file
	dir := filepath.Join(townRoot, ".runtime", "nudge_queue", session)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "100.json"), []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	// Enqueue a valid nudge (with later timestamp)
	n := QueuedNudge{
		Sender:    "test",
		Message:   "valid",
		Timestamp: time.Now().Add(time.Second),
	}
	if err := Enqueue(townRoot, session, n); err != nil {
		t.Fatal(err)
	}

	nudges, err := Drain(townRoot, session)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != 1 {
		t.Fatalf("got %d nudges, want 1 (malformed should be skipped)", len(nudges))
	}
	if nudges[0].Message != "valid" {
		t.Errorf("got message %q, want %q", nudges[0].Message, "valid")
	}

	// Malformed file should have been cleaned up (renamed to .claimed then removed)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("queue dir should be empty after drain, got %d entries: %v", len(entries), names)
	}
}

func TestFormatForInjection_Normal(t *testing.T) {
	nudges := []QueuedNudge{
		{Sender: "mayor", Message: "Check status", Priority: PriorityNormal},
	}
	output := FormatForInjection(nudges)

	if output == "" {
		t.Fatal("FormatForInjection returned empty string")
	}
	if !strings.Contains(output, "<system-reminder>") {
		t.Error("missing <system-reminder> tag")
	}
	if !strings.Contains(output, "background notification") {
		t.Error("normal nudges should mention background notification")
	}
	if strings.Contains(output, "URGENT") {
		t.Error("normal nudges should not contain URGENT")
	}
}

func TestFormatForInjection_Urgent(t *testing.T) {
	nudges := []QueuedNudge{
		{Sender: "witness", Message: "Polecat stuck", Priority: PriorityUrgent},
		{Sender: "mayor", Message: "FYI", Priority: PriorityNormal},
	}
	output := FormatForInjection(nudges)

	if !strings.Contains(output, "URGENT") {
		t.Error("should mention URGENT for urgent nudges")
	}
	if !strings.Contains(output, "Handle urgent") {
		t.Error("should instruct agent to handle urgent nudges")
	}
	if !strings.Contains(output, "non-urgent") {
		t.Error("should mention non-urgent nudges")
	}
}

func TestFormatForInjection_Empty(t *testing.T) {
	output := FormatForInjection(nil)
	if output != "" {
		t.Errorf("FormatForInjection(nil) = %q, want empty", output)
	}
}

func TestPendingNonexistentDir(t *testing.T) {
	count, err := Pending("/nonexistent/path", "session")
	if err != nil {
		t.Fatalf("Pending on nonexistent dir should not error: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestEnqueueDefaults(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-defaults"

	// Enqueue with zero timestamp and empty priority — should get defaults
	n := QueuedNudge{
		Sender:  "test",
		Message: "hello",
	}
	if err := Enqueue(townRoot, session, n); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	nudges, err := Drain(townRoot, session)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != 1 {
		t.Fatalf("got %d nudges, want 1", len(nudges))
	}
	if nudges[0].Priority != PriorityNormal {
		t.Errorf("Priority = %q, want %q", nudges[0].Priority, PriorityNormal)
	}
	if nudges[0].Timestamp.IsZero() {
		t.Error("Timestamp should have been set to non-zero default")
	}
	if nudges[0].ExpiresAt.IsZero() {
		t.Error("ExpiresAt should have been set to non-zero default")
	}
	// Normal priority should get DefaultNormalTTL
	expectedExpiry := nudges[0].Timestamp.Add(DefaultNormalTTL)
	if !nudges[0].ExpiresAt.Equal(expectedExpiry) {
		t.Errorf("ExpiresAt = %v, want %v (Timestamp + DefaultNormalTTL)", nudges[0].ExpiresAt, expectedExpiry)
	}
}

func TestEnqueueUrgentTTL(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-urgent-ttl"

	n := QueuedNudge{
		Sender:   "test",
		Message:  "urgent message",
		Priority: PriorityUrgent,
	}
	if err := Enqueue(townRoot, session, n); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	nudges, err := Drain(townRoot, session)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != 1 {
		t.Fatalf("got %d nudges, want 1", len(nudges))
	}
	// Urgent priority should get DefaultUrgentTTL
	expectedExpiry := nudges[0].Timestamp.Add(DefaultUrgentTTL)
	if !nudges[0].ExpiresAt.Equal(expectedExpiry) {
		t.Errorf("ExpiresAt = %v, want %v (Timestamp + DefaultUrgentTTL)", nudges[0].ExpiresAt, expectedExpiry)
	}
}

func TestEnqueueCustomExpiry(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-custom-expiry"

	customExpiry := time.Now().Add(5 * time.Minute)
	n := QueuedNudge{
		Sender:    "test",
		Message:   "custom expiry",
		ExpiresAt: customExpiry,
	}
	if err := Enqueue(townRoot, session, n); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	nudges, err := Drain(townRoot, session)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != 1 {
		t.Fatalf("got %d nudges, want 1", len(nudges))
	}
	// Custom expiry should be preserved, not overwritten by default TTL
	if !nudges[0].ExpiresAt.Equal(customExpiry) {
		t.Errorf("ExpiresAt = %v, want %v (custom)", nudges[0].ExpiresAt, customExpiry)
	}
}

func TestDrainSkipsExpired(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-expired"

	// Enqueue an already-expired nudge
	expired := QueuedNudge{
		Sender:    "old-sender",
		Message:   "stale message",
		Timestamp: time.Now().Add(-time.Hour),
		ExpiresAt: time.Now().Add(-30 * time.Minute), // expired 30 min ago
	}
	if err := Enqueue(townRoot, session, expired); err != nil {
		t.Fatalf("Enqueue expired: %v", err)
	}

	// Enqueue a fresh nudge
	time.Sleep(time.Millisecond)
	fresh := QueuedNudge{
		Sender:  "new-sender",
		Message: "fresh message",
	}
	if err := Enqueue(townRoot, session, fresh); err != nil {
		t.Fatalf("Enqueue fresh: %v", err)
	}

	// Pending counts both (doesn't check expiry)
	pending, err := Pending(townRoot, session)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if pending != 2 {
		t.Errorf("Pending = %d, want 2 (counts all files)", pending)
	}

	// Drain should skip the expired nudge
	nudges, err := Drain(townRoot, session)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != 1 {
		t.Fatalf("Drain returned %d nudges, want 1 (expired should be skipped)", len(nudges))
	}
	if nudges[0].Sender != "new-sender" {
		t.Errorf("got sender %q, want %q", nudges[0].Sender, "new-sender")
	}

	// After drain, queue dir should be empty (both files removed)
	dir := filepath.Join(townRoot, ".runtime", "nudge_queue", session)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("queue dir should be empty after drain, got %d entries", len(entries))
	}
}

func TestEnqueueQueueDepthLimit(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-depth"

	// Fill the queue to MaxQueueDepth
	for i := 0; i < MaxQueueDepth; i++ {
		n := QueuedNudge{
			Sender:  "sender",
			Message: "msg",
		}
		if err := Enqueue(townRoot, session, n); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}

	// Next enqueue should fail
	overflow := QueuedNudge{
		Sender:  "sender",
		Message: "overflow",
	}
	err := Enqueue(townRoot, session, overflow)
	if err == nil {
		t.Fatal("expected error when queue is full")
	}
	if !strings.Contains(err.Error(), "is full") {
		t.Errorf("got error %q, want to contain 'is full'", err.Error())
	}

	// Verify pending count is at max
	pending, _ := Pending(townRoot, session)
	if pending != MaxQueueDepth {
		t.Errorf("Pending = %d, want %d", pending, MaxQueueDepth)
	}

	// After draining, enqueue should work again
	nudges, err := Drain(townRoot, session)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != MaxQueueDepth {
		t.Errorf("Drain returned %d, want %d", len(nudges), MaxQueueDepth)
	}

	err = Enqueue(townRoot, session, overflow)
	if err != nil {
		t.Errorf("Enqueue after drain should succeed: %v", err)
	}
}

func TestDrainSweepsOrphanedClaims(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-orphans"

	dir := filepath.Join(townRoot, ".runtime", "nudge_queue", session)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create an orphaned .claimed file with old mod time
	// Claim files now use the format: <original>.json.claimed.<suffix>
	orphanPath := filepath.Join(dir, "100.json.claimed.deadbeef")
	if err := os.WriteFile(orphanPath, []byte(`{"sender":"ghost"}`), 0644); err != nil {
		t.Fatal(err)
	}
	// Set mod time to well past the stale threshold
	oldTime := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(orphanPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Create a fresh .claimed file (should NOT be swept)
	freshClaimPath := filepath.Join(dir, "200.json.claimed.cafebabe")
	if err := os.WriteFile(freshClaimPath, []byte(`{"sender":"active"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Enqueue a valid nudge
	n := QueuedNudge{Sender: "test", Message: "valid"}
	if err := Enqueue(townRoot, session, n); err != nil {
		t.Fatal(err)
	}

	// First Drain: requeues the orphaned claim (rename .claimed → .json),
	// keeps the fresh claim, and returns the valid nudge.
	// The requeued file isn't in the current ReadDir snapshot, so it's
	// picked up on the next Drain call.
	nudges, err := Drain(townRoot, session)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != 1 {
		t.Fatalf("first Drain got %d nudges, want 1", len(nudges))
	}
	if nudges[0].Message != "valid" {
		t.Errorf("got message %q, want %q", nudges[0].Message, "valid")
	}

	// The orphaned .claimed file should have been requeued as .json
	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Error("orphaned .claimed file should no longer exist (requeued to .json)")
	}
	// Restored path strips everything from ".claimed" onward
	restoredPath := filepath.Join(dir, "100.json")
	if _, err := os.Stat(restoredPath); os.IsNotExist(err) {
		t.Error("restored .json file should exist after requeue")
	}

	// Second Drain: picks up the requeued orphan
	nudges2, err := Drain(townRoot, session)
	if err != nil {
		t.Fatalf("second Drain: %v", err)
	}
	if len(nudges2) != 1 {
		t.Fatalf("second Drain got %d nudges, want 1 (the requeued orphan)", len(nudges2))
	}
	if nudges2[0].Sender != "ghost" {
		t.Errorf("got sender %q, want %q", nudges2[0].Sender, "ghost")
	}

	// The fresh claim should still exist (not old enough to sweep)
	if _, err := os.Stat(freshClaimPath); os.IsNotExist(err) {
		t.Error("fresh .claimed file should NOT have been swept")
	}
}

func TestConcurrentEnqueueNoDuplicateLoss(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-concurrent"

	// Fire 20 concurrent enqueues — all should succeed without collision.
	const count = 20
	var wg sync.WaitGroup
	errs := make(chan error, count)

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n := QueuedNudge{
				Sender:  "sender",
				Message: strings.Repeat("x", i+1), // unique per goroutine
			}
			if err := Enqueue(townRoot, session, n); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Enqueue failed: %v", err)
	}

	// All 20 should be pending
	pending, err := Pending(townRoot, session)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if pending != count {
		t.Errorf("Pending = %d, want %d (some nudges lost to collision?)", pending, count)
	}

	// Drain should return all 20
	nudges, err := Drain(townRoot, session)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != count {
		t.Errorf("Drain returned %d, want %d", len(nudges), count)
	}
}

// --- DeliverAfter tests ---

// TestDrainSkipsDeferredNudge verifies that a nudge with a future DeliverAfter
// is not returned by Drain and remains in the queue.
func TestDrainSkipsDeferredNudge(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-deferred"

	deferred := QueuedNudge{
		Sender:       "system",
		Message:      "reply reminder",
		DeliverAfter: time.Now().Add(10 * time.Second), // far future
	}
	if err := Enqueue(townRoot, session, deferred); err != nil {
		t.Fatalf("Enqueue deferred: %v", err)
	}

	nudges, err := Drain(townRoot, session)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != 0 {
		t.Fatalf("Drain returned %d nudges, want 0 (deferred not ready)", len(nudges))
	}

	// File should still be in queue (not discarded)
	pending, err := Pending(townRoot, session)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if pending != 1 {
		t.Errorf("Pending = %d, want 1 (deferred nudge still in queue)", pending)
	}
}

// TestDrainDeliversDeferredNudgeWhenReady verifies that a nudge with a past
// DeliverAfter is delivered normally.
func TestDrainDeliversDeferredNudgeWhenReady(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-deferred-ready"

	ready := QueuedNudge{
		Sender:       "system",
		Message:      "reply reminder",
		DeliverAfter: time.Now().Add(-1 * time.Second), // already past
	}
	if err := Enqueue(townRoot, session, ready); err != nil {
		t.Fatalf("Enqueue ready: %v", err)
	}

	nudges, err := Drain(townRoot, session)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != 1 {
		t.Fatalf("Drain returned %d nudges, want 1 (deferred is ready)", len(nudges))
	}
	if nudges[0].Message != "reply reminder" {
		t.Errorf("got message %q, want %q", nudges[0].Message, "reply reminder")
	}
}

// TestDrainMixedDeferredAndReady verifies that only ready nudges are returned
// when a mix of deferred and immediately-deliverable nudges are queued.
func TestDrainMixedDeferredAndReady(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-mixed-deferred"

	// Enqueue: immediate, then deferred, then immediate (interleaved order).
	n1 := QueuedNudge{Sender: "mayor", Message: "immediate-1"}
	if err := Enqueue(townRoot, session, n1); err != nil {
		t.Fatalf("Enqueue n1: %v", err)
	}
	time.Sleep(time.Millisecond)

	deferred := QueuedNudge{
		Sender:       "system",
		Message:      "deferred",
		DeliverAfter: time.Now().Add(60 * time.Second),
	}
	if err := Enqueue(townRoot, session, deferred); err != nil {
		t.Fatalf("Enqueue deferred: %v", err)
	}
	time.Sleep(time.Millisecond)

	n2 := QueuedNudge{Sender: "witness", Message: "immediate-2"}
	if err := Enqueue(townRoot, session, n2); err != nil {
		t.Fatalf("Enqueue n2: %v", err)
	}

	nudges, err := Drain(townRoot, session)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != 2 {
		t.Fatalf("Drain returned %d nudges, want 2 (deferred stays in queue)", len(nudges))
	}
	if nudges[0].Message != "immediate-1" {
		t.Errorf("nudges[0].Message = %q, want %q", nudges[0].Message, "immediate-1")
	}
	if nudges[1].Message != "immediate-2" {
		t.Errorf("nudges[1].Message = %q, want %q", nudges[1].Message, "immediate-2")
	}

	// Deferred nudge remains in queue
	pending, err := Pending(townRoot, session)
	if err != nil {
		t.Fatalf("Pending after drain: %v", err)
	}
	if pending != 1 {
		t.Errorf("Pending = %d, want 1 (deferred nudge still in queue)", pending)
	}
}

func TestRemoveKindByThread(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-remove"

	keep := QueuedNudge{Sender: "system", Message: "keep", Kind: "mail", ThreadID: "thread-1"}
	removeA := QueuedNudge{Sender: "system", Message: "remove-a", Kind: "reply-reminder", ThreadID: "thread-1"}
	removeB := QueuedNudge{Sender: "system", Message: "remove-b", Kind: "reply-reminder", ThreadID: "thread-1"}
	otherThread := QueuedNudge{Sender: "system", Message: "other-thread", Kind: "reply-reminder", ThreadID: "thread-2"}

	for _, n := range []QueuedNudge{keep, removeA, removeB, otherThread} {
		if err := Enqueue(townRoot, session, n); err != nil {
			t.Fatalf("Enqueue(%q): %v", n.Message, err)
		}
		time.Sleep(time.Millisecond)
	}

	removed, err := RemoveKindByThread(townRoot, session, "reply-reminder", "thread-1")
	if err != nil {
		t.Fatalf("RemoveKindByThread: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}

	nudges, err := Drain(townRoot, session)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != 2 {
		t.Fatalf("Drain returned %d nudges, want 2", len(nudges))
	}
	if nudges[0].Message != "keep" {
		t.Fatalf("nudges[0].Message = %q, want %q", nudges[0].Message, "keep")
	}
	if nudges[1].Message != "other-thread" {
		t.Fatalf("nudges[1].Message = %q, want %q", nudges[1].Message, "other-thread")
	}
}

// TestDeferredNudgeDeliveredAfterDelay confirms that the same nudge is skipped
// before its deadline and delivered after its persisted deadline has elapsed.
func TestDeferredNudgeDeliveredAfterDelay(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-deferred-sequence"

	shortDelay := QueuedNudge{
		Sender:       "system",
		Message:      "reply via mail",
		DeliverAfter: time.Now().Add(time.Hour),
	}
	if err := Enqueue(townRoot, session, shortDelay); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// First Drain: not ready yet.
	nudges, err := Drain(townRoot, session)
	if err != nil {
		t.Fatalf("first Drain: %v", err)
	}
	if len(nudges) != 0 {
		t.Fatalf("first Drain: got %d nudges, want 0 (deferred not ready)", len(nudges))
	}

	// Move the persisted deadline into the past without depending on scheduler
	// timing. Under a loaded full-suite runner, a tiny real-time delay can elapse
	// before the first Drain and invert the assertion this test is meant to make.
	entries, err := os.ReadDir(queueDir(townRoot, session))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("queue entries = %d, want 1", len(entries))
	}
	path := filepath.Join(queueDir(townRoot, session), entries[0].Name())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var persisted QueuedNudge
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	persisted.DeliverAfter = time.Now().Add(-time.Second)
	data, err = json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Second Drain: ready now.
	nudges, err = Drain(townRoot, session)
	if err != nil {
		t.Fatalf("second Drain: %v", err)
	}
	if len(nudges) != 1 {
		t.Fatalf("second Drain: got %d nudges, want 1 (deferred now ready)", len(nudges))
	}
	if nudges[0].Message != "reply via mail" {
		t.Errorf("got message %q, want %q", nudges[0].Message, "reply via mail")
	}

	// Queue should now be empty.
	pending, err := Pending(townRoot, session)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if pending != 0 {
		t.Errorf("Pending = %d, want 0 (deferred nudge delivered)", pending)
	}
}

// TestZeroDeliverAfterIsImmediate verifies that a zero DeliverAfter (unset)
// is treated as immediately deliverable (not deferred).
func TestZeroDeliverAfterIsImmediate(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-zero-deliver-after"

	n := QueuedNudge{
		Sender:  "mayor",
		Message: "no delay",
		// DeliverAfter intentionally left zero
	}
	if err := Enqueue(townRoot, session, n); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	nudges, err := Drain(townRoot, session)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(nudges) != 1 {
		t.Fatalf("got %d nudges, want 1 (zero DeliverAfter = immediate)", len(nudges))
	}
}

func TestConcurrentDrainNoDoubleDeli(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-test-drain-race"

	// Enqueue 10 nudges
	const count = 10
	for i := 0; i < count; i++ {
		n := QueuedNudge{
			Sender:  "sender",
			Message: strings.Repeat("m", i+1),
		}
		if err := Enqueue(townRoot, session, n); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
		time.Sleep(time.Millisecond) // ensure ordering
	}

	// Race 5 concurrent Drains — total nudges collected should equal count.
	const drainers = 5
	var wg sync.WaitGroup
	results := make(chan []QueuedNudge, drainers)

	for i := 0; i < drainers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			nudges, err := Drain(townRoot, session)
			if err != nil {
				t.Errorf("concurrent Drain: %v", err)
				return
			}
			results <- nudges
		}()
	}
	wg.Wait()
	close(results)

	total := 0
	for nudges := range results {
		total += len(nudges)
	}

	// On Windows, transient sharing violations (antivirus, search indexer)
	// can prevent all concurrent drainers from claiming a file.  The nudge
	// stays as .json and is picked up on the next Drain — mirror that here
	// with a straggler sweep so the test validates no-loss, not one-shot
	// completeness.
	for retries := 0; retries < 3 && total < count; retries++ {
		time.Sleep(50 * time.Millisecond)
		stragglers, err := Drain(townRoot, session)
		if err != nil {
			t.Fatalf("straggler Drain: %v", err)
		}
		total += len(stragglers)
	}

	if total != count {
		t.Errorf("concurrent Drains delivered %d total nudges, want exactly %d (double-delivery or loss)", total, count)
	}

	// Verify no double-delivery: total must be exactly count, not more.
	if total > count {
		t.Errorf("double delivery detected: got %d total nudges, want exactly %d", total, count)
	}
}
