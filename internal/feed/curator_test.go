package feed

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/events"
)

func TestCurator_FiltersByVisibility(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "feed-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create events file with test events
	eventsPath := filepath.Join(tmpDir, events.EventsFile)
	feedPath := filepath.Join(tmpDir, FeedFile)

	// Write a feed-visible event
	feedEvent := events.Event{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Source:     "gt",
		Type:       events.TypeSling,
		Actor:      "mayor",
		Payload:    map[string]interface{}{"bead": "gt-123", "target": "gastown/slit"},
		Visibility: events.VisibilityFeed,
	}
	feedData, _ := json.Marshal(feedEvent)

	// Write an audit-only event (should be filtered out)
	auditEvent := events.Event{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Source:     "gt",
		Type:       "internal_check",
		Actor:      "daemon",
		Visibility: events.VisibilityAudit,
	}
	auditData, _ := json.Marshal(auditEvent)

	// Create events file
	if err := os.WriteFile(eventsPath, []byte{}, 0644); err != nil {
		t.Fatalf("creating events file: %v", err)
	}

	// Start curator
	curator := NewCurator(tmpDir)
	if err := curator.Start(); err != nil {
		t.Fatalf("starting curator: %v", err)
	}
	defer curator.Stop()

	// Give curator time to start
	time.Sleep(50 * time.Millisecond)

	// Append events
	f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("opening events file: %v", err)
	}
	f.Write(append(feedData, '\n'))
	f.Write(append(auditData, '\n'))
	f.Close()

	// Wait for processing
	time.Sleep(300 * time.Millisecond)

	// Check feed file
	feedContent, err := os.ReadFile(feedPath)
	if err != nil {
		t.Fatalf("reading feed file: %v", err)
	}

	// Should contain feed event but not audit event
	if len(feedContent) == 0 {
		t.Error("feed file is empty, expected at least one event")
	}

	var writtenEvent FeedEvent
	if err := json.Unmarshal(feedContent[:len(feedContent)-1], &writtenEvent); err != nil {
		t.Fatalf("parsing feed event: %v", err)
	}

	if writtenEvent.Type != events.TypeSling {
		t.Errorf("expected type %s, got %s", events.TypeSling, writtenEvent.Type)
	}
	if writtenEvent.Actor != "mayor" {
		t.Errorf("expected actor 'mayor', got %s", writtenEvent.Actor)
	}
}

func TestCurator_DedupesDoneEvents(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feed-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	eventsPath := filepath.Join(tmpDir, events.EventsFile)
	feedPath := filepath.Join(tmpDir, FeedFile)

	// Create events file
	if err := os.WriteFile(eventsPath, []byte{}, 0644); err != nil {
		t.Fatalf("creating events file: %v", err)
	}

	// Start curator
	curator := NewCurator(tmpDir)
	if err := curator.Start(); err != nil {
		t.Fatalf("starting curator: %v", err)
	}
	defer curator.Stop()

	time.Sleep(50 * time.Millisecond)

	// Write 3 identical done events from same actor
	f, _ := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0644)
	for i := 0; i < 3; i++ {
		doneEvent := events.Event{
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
			Source:     "gt",
			Type:       events.TypeDone,
			Actor:      "gastown/slit",
			Payload:    map[string]interface{}{"bead": "slit-12345"},
			Visibility: events.VisibilityFeed,
		}
		data, _ := json.Marshal(doneEvent)
		f.Write(append(data, '\n'))
	}
	f.Close()

	// Wait for processing
	time.Sleep(300 * time.Millisecond)

	// Count feed events
	feedContent, _ := os.ReadFile(feedPath)
	lines := 0
	for _, b := range feedContent {
		if b == '\n' {
			lines++
		}
	}

	// Should only have 1 event due to deduplication
	if lines != 1 {
		t.Errorf("expected 1 feed event after deduplication, got %d", lines)
	}
}

// --- Config loading tests ---

func TestCurator_DefaultConfig_NoSettingsFile(t *testing.T) {
	// When no settings file exists, NewCurator should use defaults.
	tmpDir := t.TempDir()

	curator := NewCurator(tmpDir)
	defer curator.Stop()

	if curator.doneDedupeWindow != 10*time.Second {
		t.Errorf("doneDedupeWindow = %v, want 10s", curator.doneDedupeWindow)
	}
	if curator.slingAggregateWindow != 30*time.Second {
		t.Errorf("slingAggregateWindow = %v, want 30s", curator.slingAggregateWindow)
	}
	if curator.minAggregateCount != 3 {
		t.Errorf("minAggregateCount = %d, want 3", curator.minAggregateCount)
	}
}

func TestCurator_DefaultConfig_EmptyTownRoot(t *testing.T) {
	// Empty townRoot should use defaults without crashing.
	curator := NewCurator("")
	defer curator.Stop()

	if curator.doneDedupeWindow != 10*time.Second {
		t.Errorf("doneDedupeWindow = %v, want 10s", curator.doneDedupeWindow)
	}
	if curator.minAggregateCount != 3 {
		t.Errorf("minAggregateCount = %d, want 3", curator.minAggregateCount)
	}
}

func TestCurator_CustomConfig_FromSettingsFile(t *testing.T) {
	// Write a settings/config.json with custom FeedCurator values.
	tmpDir := t.TempDir()
	settingsDir := filepath.Join(tmpDir, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	ts := config.NewTownSettings()
	ts.FeedCurator = &config.FeedCuratorConfig{
		DoneDedupeWindow:     "20s",
		SlingAggregateWindow: "1m",
		MinAggregateCount:    7,
	}
	if err := config.SaveTownSettings(filepath.Join(settingsDir, "config.json"), ts); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	curator := NewCurator(tmpDir)
	defer curator.Stop()

	if curator.doneDedupeWindow != 20*time.Second {
		t.Errorf("doneDedupeWindow = %v, want 20s", curator.doneDedupeWindow)
	}
	if curator.slingAggregateWindow != 1*time.Minute {
		t.Errorf("slingAggregateWindow = %v, want 1m", curator.slingAggregateWindow)
	}
	if curator.minAggregateCount != 7 {
		t.Errorf("minAggregateCount = %d, want 7", curator.minAggregateCount)
	}
}

func TestCurator_PartialConfig_FallsBackToDefaults(t *testing.T) {
	// Settings file exists but FeedCurator section is absent → defaults.
	tmpDir := t.TempDir()
	settingsDir := filepath.Join(tmpDir, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	ts := config.NewTownSettings()
	// No FeedCurator set
	if err := config.SaveTownSettings(filepath.Join(settingsDir, "config.json"), ts); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	curator := NewCurator(tmpDir)
	defer curator.Stop()

	if curator.doneDedupeWindow != 10*time.Second {
		t.Errorf("doneDedupeWindow = %v, want 10s (default)", curator.doneDedupeWindow)
	}
	if curator.slingAggregateWindow != 30*time.Second {
		t.Errorf("slingAggregateWindow = %v, want 30s (default)", curator.slingAggregateWindow)
	}
	if curator.minAggregateCount != 3 {
		t.Errorf("minAggregateCount = %d, want 3 (default)", curator.minAggregateCount)
	}
}

func TestCurator_PartialFeedCuratorConfig_EmptyDurations(t *testing.T) {
	// FeedCurator section exists but some duration fields are empty → fallback defaults.
	tmpDir := t.TempDir()
	settingsDir := filepath.Join(tmpDir, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	ts := config.NewTownSettings()
	ts.FeedCurator = &config.FeedCuratorConfig{
		DoneDedupeWindow: "25s",
		// SlingAggregateWindow left empty → fallback
		MinAggregateCount: 10,
	}
	if err := config.SaveTownSettings(filepath.Join(settingsDir, "config.json"), ts); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	curator := NewCurator(tmpDir)
	defer curator.Stop()

	if curator.doneDedupeWindow != 25*time.Second {
		t.Errorf("doneDedupeWindow = %v, want 25s", curator.doneDedupeWindow)
	}
	// Empty string → ParseDurationOrDefault falls back to 30s
	if curator.slingAggregateWindow != 30*time.Second {
		t.Errorf("slingAggregateWindow = %v, want 30s (fallback)", curator.slingAggregateWindow)
	}
	if curator.minAggregateCount != 10 {
		t.Errorf("minAggregateCount = %d, want 10", curator.minAggregateCount)
	}
}

func TestCurator_PartialFeedCuratorConfig_ZeroMinAggregate(t *testing.T) {
	// FeedCurator section exists with durations but MinAggregateCount is omitted (zero value).
	// Must fall back to default 3, NOT use 0 (which would aggregate every sling event).
	tmpDir := t.TempDir()
	settingsDir := filepath.Join(tmpDir, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	ts := config.NewTownSettings()
	ts.FeedCurator = &config.FeedCuratorConfig{
		DoneDedupeWindow:     "25s",
		SlingAggregateWindow: "1m",
		// MinAggregateCount intentionally omitted (zero value)
	}
	if err := config.SaveTownSettings(filepath.Join(settingsDir, "config.json"), ts); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	curator := NewCurator(tmpDir)
	defer curator.Stop()

	if curator.minAggregateCount != 3 {
		t.Errorf("minAggregateCount = %d, want 3 (default for zero/omitted value)", curator.minAggregateCount)
	}
}

func TestCurator_InvalidDurationString_FallsBack(t *testing.T) {
	// Invalid duration string in config → falls back to default.
	tmpDir := t.TempDir()
	settingsDir := filepath.Join(tmpDir, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	ts := config.NewTownSettings()
	ts.FeedCurator = &config.FeedCuratorConfig{
		DoneDedupeWindow:     "not-valid",
		SlingAggregateWindow: "also-bad",
		MinAggregateCount:    3,
	}
	if err := config.SaveTownSettings(filepath.Join(settingsDir, "config.json"), ts); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	curator := NewCurator(tmpDir)
	defer curator.Stop()

	if curator.doneDedupeWindow != 10*time.Second {
		t.Errorf("doneDedupeWindow = %v, want 10s (fallback for invalid)", curator.doneDedupeWindow)
	}
	if curator.slingAggregateWindow != 30*time.Second {
		t.Errorf("slingAggregateWindow = %v, want 30s (fallback for invalid)", curator.slingAggregateWindow)
	}
}

func TestCurator_GeneratesSummary(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "feed-test-*")
	defer os.RemoveAll(tmpDir)

	curator := NewCurator(tmpDir)

	tests := []struct {
		event    *events.Event
		expected string
	}{
		{
			event: &events.Event{
				Type:    events.TypeSling,
				Actor:   "mayor",
				Payload: map[string]interface{}{"bead": "gt-123", "target": "gastown/slit"},
			},
			expected: "mayor assigned gt-123 to gastown/slit",
		},
		{
			event: &events.Event{
				Type:    events.TypeDone,
				Actor:   "gastown/slit",
				Payload: map[string]interface{}{"bead": "slit-12345"},
			},
			expected: "gastown/slit completed work on slit-12345",
		},
		{
			event: &events.Event{
				Type:  events.TypeHandoff,
				Actor: "gastown/witness",
			},
			expected: "gastown/witness handed off to fresh session",
		},
	}

	for _, tc := range tests {
		summary := curator.generateSummary(tc.event)
		if summary != tc.expected {
			t.Errorf("generateSummary(%s): expected %q, got %q", tc.event.Type, tc.expected, summary)
		}
	}
}

// --- Truncation and size limit tests ---

func TestCurator_TruncatesAtMaxSize(t *testing.T) {
	tmpDir := t.TempDir()
	feedPath := filepath.Join(tmpDir, FeedFile)

	curator := NewCurator(tmpDir)
	defer curator.Stop()
	curator.maxFeedFileSize = 1024 // override for testing

	// Write events directly to the feed file to exceed the limit
	f, err := os.OpenFile(feedPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		ev := FeedEvent{
			Timestamp: time.Now().Add(time.Duration(i) * time.Second).UTC().Format(time.RFC3339),
			Source:    "gt",
			Type:      "test",
			Actor:     "test-actor",
			Summary:   fmt.Sprintf("test event %d with some padding to make it longer", i),
		}
		data, _ := json.Marshal(ev)
		f.Write(append(data, '\n'))
	}
	f.Close()

	// Verify file exceeds limit
	info, _ := os.Stat(feedPath)
	if info.Size() <= 1024 {
		t.Fatalf("test setup: file size %d should exceed 1024", info.Size())
	}

	// Write one more event through curator (triggers truncation)
	curator.writeFeedEvent(&events.Event{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Source:     "gt",
		Type:       events.TypeDone,
		Actor:      "test-actor",
		Payload:    map[string]interface{}{"bead": "test"},
		Visibility: events.VisibilityFeed,
	})

	// Verify file was truncated
	info, err = os.Stat(feedPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 1024+512 { // allow small overshoot for the new write
		t.Errorf("file size %d should be near or below limit after truncation", info.Size())
	}

	// Verify content is valid JSONL
	content, _ := os.ReadFile(feedPath)
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		if line == "" {
			continue
		}
		var ev FeedEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Errorf("malformed line after truncation: %v", err)
		}
	}
}

func TestCurator_ReadRecentFeedEventsLargeFile(t *testing.T) {
	tmpDir := t.TempDir()
	feedPath := filepath.Join(tmpDir, FeedFile)

	// Write a large feed file with events spanning hours.
	// Events are spaced 1440ms apart over 2h (5000 * 1440ms = 7200s = 2h).
	// We use a 2-minute window for the read to avoid flakiness on slow CI
	// machines where writing 5000 events may take several seconds, shifting
	// the cutoff (time.Now()-window inside readRecentFeedEvents) forward
	// enough to exclude events near "now" captured before the writes.
	f, err := os.OpenFile(feedPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := 0; i < 5000; i++ {
		ts := now.Add(-2*time.Hour + time.Duration(i)*time.Millisecond*1440)
		ev := FeedEvent{
			Timestamp: ts.UTC().Format(time.RFC3339),
			Source:    "gt",
			Type:      events.TypeDone,
			Actor:     "test-actor",
			Summary:   fmt.Sprintf("event %d", i),
		}
		data, _ := json.Marshal(ev)
		f.Write(append(data, '\n'))
	}
	f.Close()

	curator := NewCurator(tmpDir)

	// Use a 2-minute window: ~83 events expected (120s / 1.44s per event),
	// well under 5000, so filtering is still validated.
	result, err := curator.readRecentFeedEvents(2 * time.Minute)
	if err != nil {
		t.Fatalf("readRecentFeedEvents error: %v", err)
	}

	if len(result) > 500 {
		t.Errorf("readRecentFeedEvents returned %d events for 2m window, expected << 5000", len(result))
	}
	if len(result) == 0 {
		t.Error("readRecentFeedEvents returned 0 events, expected at least some recent ones")
	}
}

func TestCurator_FeedFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permissions are not supported on Windows")
	}
	tmpDir := t.TempDir()
	feedPath := filepath.Join(tmpDir, FeedFile)

	curator := NewCurator(tmpDir)

	curator.writeFeedEvent(&events.Event{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Source:     "gt",
		Type:       events.TypeDone,
		Actor:      "test-actor",
		Payload:    map[string]interface{}{"bead": "test"},
		Visibility: events.VisibilityFeed,
	})

	info, err := os.Stat(feedPath)
	if err != nil {
		t.Fatalf("feed file not created: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("feed file permissions = %o, want 0600", perm)
	}
}

func TestCurator_DefaultMaxFeedFileSize(t *testing.T) {
	curator := NewCurator(t.TempDir())
	if curator.maxFeedFileSize != maxFeedFileSize {
		t.Errorf("maxFeedFileSize = %d, want %d", curator.maxFeedFileSize, maxFeedFileSize)
	}
}

// TestCurator_ConcurrentStartIsIdempotent verifies that calling Start()
// concurrently from multiple goroutines only spawns one curator goroutine.
// Without the sync.Once guard, each call would open the events file and
// spawn a separate run() goroutine, causing duplicate event processing
// and data races on the shared bufio.Reader (if file handles were shared).
//
// Regression test for steveyegge/gastown#1230 item 6.
func TestCurator_ConcurrentStartIsIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	eventsPath := filepath.Join(tmpDir, events.EventsFile)
	if err := os.WriteFile(eventsPath, []byte{}, 0644); err != nil {
		t.Fatalf("creating events file: %v", err)
	}

	curator := NewCurator(tmpDir)
	defer curator.Stop()

	// Call Start() from 10 goroutines concurrently.
	const goroutines = 10
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			errs[idx] = curator.Start()
		}(i)
	}
	wg.Wait()

	// All calls should succeed (startErr is nil when init succeeds).
	for i, err := range errs {
		if err != nil {
			t.Errorf("Start() goroutine %d: unexpected error: %v", i, err)
		}
	}

	// Write one event and verify exactly one feed event is produced
	// (not N duplicates from N goroutines).
	time.Sleep(50 * time.Millisecond)

	f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	ev := events.Event{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Source:     "gt",
		Type:       events.TypeDone,
		Actor:      "test-actor",
		Payload:    map[string]interface{}{"bead": "test"},
		Visibility: events.VisibilityFeed,
	}
	data, _ := json.Marshal(ev)
	f.Write(append(data, '\n'))
	f.Close()

	time.Sleep(300 * time.Millisecond)

	feedPath := filepath.Join(tmpDir, FeedFile)
	feedContent, err := os.ReadFile(feedPath)
	if err != nil {
		t.Fatalf("reading feed: %v", err)
	}

	lines := 0
	for _, line := range strings.Split(strings.TrimSpace(string(feedContent)), "\n") {
		if line != "" {
			lines++
		}
	}
	if lines != 1 {
		t.Errorf("expected exactly 1 feed event, got %d (concurrent Start spawned multiple goroutines?)", lines)
	}
}

// TestCurator_ConcurrentFeedReadWrite verifies that concurrent reads and
// writes to the feed file don't race. The in-process feedMu mutex
// coordinates goroutines within the same process, while flock coordinates
// across processes.
//
// Regression test for steveyegge/gastown#1230 item 7.
func TestCurator_ConcurrentFeedReadWrite(t *testing.T) {
	tmpDir := t.TempDir()
	curator := NewCurator(tmpDir)
	defer curator.Stop()

	// Seed the feed file with one event so reads have something to find.
	curator.writeFeedEvent(&events.Event{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Source:     "gt",
		Type:       events.TypeDone,
		Actor:      "seed-actor",
		Payload:    map[string]interface{}{"bead": "seed"},
		Visibility: events.VisibilityFeed,
	})

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	// Half writers, half readers — run with -race to detect data races.
	for i := 0; i < goroutines; i++ {
		if i%2 == 0 {
			go func(n int) {
				defer wg.Done()
				curator.writeFeedEvent(&events.Event{
					Timestamp:  time.Now().UTC().Format(time.RFC3339),
					Source:     "gt",
					Type:       events.TypeDone,
					Actor:      fmt.Sprintf("actor-%d", n),
					Payload:    map[string]interface{}{"bead": fmt.Sprintf("bead-%d", n)},
					Visibility: events.VisibilityFeed,
				})
			}(i)
		} else {
			go func() {
				defer wg.Done()
				_, _ = curator.readRecentFeedEvents(10 * time.Second)
			}()
		}
	}

	wg.Wait()

	// Verify feed file is valid JSONL (no partial/interleaved lines).
	feedPath := filepath.Join(tmpDir, FeedFile)
	content, err := os.ReadFile(feedPath)
	if err != nil {
		t.Fatalf("reading feed: %v", err)
	}
	for i, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		if line == "" {
			continue
		}
		var ev FeedEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Errorf("line %d: malformed JSON after concurrent writes: %v", i, err)
		}
	}
}

// TestCurator_StartErrorPersistsAcrossCalls verifies that if Start() fails
// (e.g., events file cannot be created), subsequent calls return the same
// error rather than silently returning nil.
//
// Regression test: startErr must be a struct field (not function-local) so
// sync.Once's happens-before guarantee makes it visible to all callers.
func TestCurator_StartErrorPersistsAcrossCalls(t *testing.T) {
	// Use a non-existent directory so OpenFile fails.
	curator := NewCurator("/nonexistent/path/that/does/not/exist")
	defer curator.Stop()

	err1 := curator.Start()
	if err1 == nil {
		t.Fatal("first Start() should have failed with a non-existent town root")
	}

	// Second call must return the same error, not nil.
	err2 := curator.Start()
	if err2 == nil {
		t.Fatal("second Start() returned nil after first call failed — startErr not persisted")
	}
	if err1.Error() != err2.Error() {
		t.Errorf("error mismatch: first=%q, second=%q", err1, err2)
	}
}

// TestCurator_ReadRecentFeedEvents_ScannerError verifies that IO errors
// during scanning are returned, not silently swallowed.
// Regression test for gt-0e4.
func TestCurator_ReadRecentFeedEvents_ScannerError(t *testing.T) {
	tmpDir := t.TempDir()
	feedPath := filepath.Join(tmpDir, FeedFile)

	// Write a valid event followed by a line exceeding bufio.Scanner's
	// default max token size (64KB). This triggers a scanner error.
	f, err := os.OpenFile(feedPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	// Write one valid event
	ev := FeedEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Source:    "gt",
		Type:      events.TypeDone,
		Actor:     "test-actor",
		Summary:   "valid event",
	}
	data, _ := json.Marshal(ev)
	f.Write(append(data, '\n'))

	// Write a line that exceeds the default scanner buffer (64KB)
	longLine := make([]byte, 70*1024)
	for i := range longLine {
		longLine[i] = 'x'
	}
	f.Write(longLine)
	f.Write([]byte{'\n'})
	f.Close()

	curator := NewCurator(tmpDir)
	result, err := curator.readRecentFeedEvents(1 * time.Hour)

	// Should return an error from scanner.Err()
	if err == nil {
		t.Fatal("readRecentFeedEvents should return scanner error for oversized line")
	}
	if !strings.Contains(err.Error(), "scanning feed file") {
		t.Errorf("error should mention scanning, got: %v", err)
	}
	// Should still return the partial results read before the error
	if len(result) != 1 {
		t.Errorf("expected 1 partial result before scanner error, got %d", len(result))
	}
}

// TestCurator_ReadRecentEvents_ScannerError verifies that IO errors
// during events file scanning are returned, not silently swallowed.
// Regression test for gt-0e4.
func TestCurator_ReadRecentEvents_ScannerError(t *testing.T) {
	tmpDir := t.TempDir()
	eventsPath := filepath.Join(tmpDir, events.EventsFile)

	// Write a valid event followed by an oversized line
	f, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	ev := events.Event{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Source:     "gt",
		Type:       events.TypeDone,
		Actor:      "test-actor",
		Visibility: events.VisibilityFeed,
	}
	data, _ := json.Marshal(ev)
	f.Write(append(data, '\n'))

	// Oversized line triggers scanner.Err()
	longLine := make([]byte, 70*1024)
	for i := range longLine {
		longLine[i] = 'y'
	}
	f.Write(longLine)
	f.Write([]byte{'\n'})
	f.Close()

	curator := NewCurator(tmpDir)
	result, err := curator.readRecentEvents(1 * time.Hour)

	if err == nil {
		t.Fatal("readRecentEvents should return scanner error for oversized line")
	}
	if !strings.Contains(err.Error(), "scanning events file") {
		t.Errorf("error should mention scanning, got: %v", err)
	}
	// Partial results returned
	if len(result) != 1 {
		t.Errorf("expected 1 partial result before scanner error, got %d", len(result))
	}
}
