// Package feed provides the feed daemon that curates raw events into a user-facing feed.
//
// The curator:
// 1. Tails ~/gt/.events.jsonl (raw events)
// 2. Filters by visibility tag (drops audit-only events)
// 3. Deduplicates repeated updates (5 molecule updates → "agent active")
// 4. Aggregates related events (3 issues closed → "batch complete")
// 5. Writes curated events to ~/gt/.feed.jsonl
package feed

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/events"
)

// FeedFile is the name of the curated feed file.
const FeedFile = ".feed.jsonl"

// FeedEvent is the structure of events written to the feed.
type FeedEvent struct {
	Timestamp string                 `json:"ts"`
	Source    string                 `json:"source"`
	Type      string                 `json:"type"`
	Actor     string                 `json:"actor"`
	Summary   string                 `json:"summary"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	Count     int                    `json:"count,omitempty"` // For aggregated events
}

// Curator manages the feed curation process.
// ZFC: State is derived from the events file, not cached in memory.
type Curator struct {
	townRoot        string
	maxFeedFileSize int64
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	startOnce       sync.Once // prevents concurrent Start() calls from spawning multiple goroutines
	startErr        error     // result of the one-shot Start; visible to all callers via sync.Once happens-before

	// feedMu guards in-process access to the feed file. The flock in
	// readRecentFeedEvents/writeFeedEvent coordinates across processes;
	// this mutex coordinates goroutines within the same process.
	feedMu sync.Mutex

	// Configurable deduplication/aggregation settings (from TownSettings.FeedCurator)
	doneDedupeWindow     time.Duration
	slingAggregateWindow time.Duration
	minAggregateCount    int
}

// NewCurator creates a new feed curator.
// Loads FeedCurator config from TownSettings; falls back to defaults if missing.
func NewCurator(townRoot string) *Curator {
	ctx, cancel := context.WithCancel(context.Background())

	cfg := config.DefaultFeedCuratorConfig()
	if townRoot != "" {
		if ts, err := config.LoadOrCreateTownSettings(config.TownSettingsPath(townRoot)); err == nil && ts.FeedCurator != nil {
			// Replace entire default — individual fields fall back below.
			// Duration fields get fallbacks via ParseDurationOrDefault (empty string → default).
			// Non-duration fields need explicit zero-value guards.
			cfg = ts.FeedCurator
		}
	}

	minAgg := cfg.MinAggregateCount
	if minAgg <= 0 {
		minAgg = 3 // default: aggregate after 3+ events
	}

	return &Curator{
		townRoot:             townRoot,
		maxFeedFileSize:      maxFeedFileSize,
		ctx:                  ctx,
		cancel:               cancel,
		doneDedupeWindow:     config.ParseDurationOrDefault(cfg.DoneDedupeWindow, 10*time.Second),
		slingAggregateWindow: config.ParseDurationOrDefault(cfg.SlingAggregateWindow, 30*time.Second),
		minAggregateCount:    minAgg,
	}
}

// Start begins the curator goroutine. It is safe to call concurrently;
// only the first call starts the goroutine — subsequent calls are no-ops.
func (c *Curator) Start() error {
	c.startOnce.Do(func() {
		eventsPath := filepath.Join(c.townRoot, events.EventsFile)

		// Open events file, creating if needed
		file, err := os.OpenFile(eventsPath, os.O_RDONLY|os.O_CREATE, 0600)
		if err != nil {
			c.startErr = fmt.Errorf("opening events file: %w", err)
			return
		}

		// Seek to end to only process new events
		if _, err := file.Seek(0, io.SeekEnd); err != nil {
			_ = file.Close() //nolint:gosec // G104: best effort cleanup on error
			c.startErr = fmt.Errorf("seeking to end: %w", err)
			return
		}

		c.wg.Add(1)
		go c.run(file)
	})
	return c.startErr
}

// Stop gracefully stops the curator.
func (c *Curator) Stop() {
	c.cancel()
	c.wg.Wait()
}

// run is the main curator loop.
// ZFC: No in-memory state to clean up - state is derived from the events file.
func (c *Curator) run(file *os.File) {
	defer c.wg.Done()
	defer file.Close()

	reader := bufio.NewReader(file)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return

		case <-ticker.C:
			// Read available lines
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					break // No more data available
				}
				c.processLine(line)
			}
		}
	}
}

// processLine processes a single line from the events file.
func (c *Curator) processLine(line string) {
	if line == "" || line == "\n" {
		return
	}

	var rawEvent events.Event
	if err := json.Unmarshal([]byte(line), &rawEvent); err != nil {
		return // Skip malformed lines
	}

	// Filter by visibility - only process feed-visible events
	if rawEvent.Visibility != events.VisibilityFeed && rawEvent.Visibility != events.VisibilityBoth {
		return
	}

	// Apply deduplication and aggregation
	if c.shouldDedupe(&rawEvent) {
		return
	}

	// Write to feed
	c.writeFeedEvent(&rawEvent)
}

// shouldDedupe checks if an event should be deduplicated.
// ZFC: Derives state from the FEED file (what we've already output), not in-memory cache.
// Returns true if the event should be dropped.
func (c *Curator) shouldDedupe(event *events.Event) bool {
	switch event.Type {
	case events.TypeDone:
		// Dedupe repeated done events from same actor within window
		// Check if we've already written a done event for this actor to the feed
		recentFeedEvents, err := c.readRecentFeedEvents(c.doneDedupeWindow)
		if err != nil {
			log.Printf("warning: reading recent feed events for dedup: %v", err)
			return false // Fail-open: don't dedupe if we can't read the feed
		}
		for _, e := range recentFeedEvents {
			if e.Type == events.TypeDone && e.Actor == event.Actor {
				return true // Skip duplicate (already in feed)
			}
		}
		return false
	}

	// Sling and mail events are not deduplicated, only aggregated in writeFeedEvent
	return false
}

// maxFeedFileSize is the maximum .feed.jsonl size before truncation.
// When exceeded, the file is truncated to keep the newest half.
const maxFeedFileSize int64 = 10 * 1024 * 1024 // 10MB

// tailReadSize is the max bytes to read from the end of a file when
// scanning for recent events. 1MB covers any realistic time window.
const tailReadSize int64 = 1 << 20

// readRecentFeedEvents reads feed events from the feed file within the given time window.
// ZFC: The feed file is the observable state of what we've already output.
// Reads at most tailReadSize bytes from the end to bound memory usage.
func (c *Curator) readRecentFeedEvents(window time.Duration) ([]FeedEvent, error) {
	feedPath := filepath.Join(c.townRoot, FeedFile)

	// In-process mutex complements the flock (which only coordinates across processes).
	c.feedMu.Lock()
	defer c.feedMu.Unlock()

	// Acquire shared read lock to prevent partial reads during concurrent writes
	fl := flock.New(feedPath + ".lock")
	if err := fl.RLock(); err != nil {
		return nil, fmt.Errorf("acquiring feed read lock: %w", err)
	}
	defer fl.Unlock() //nolint:errcheck // best-effort unlock

	f, err := os.Open(feedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening feed file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat feed file: %w", err)
	}
	if info.Size() == 0 {
		return nil, nil
	}

	// Seek to at most tailReadSize bytes before EOF
	seekTo := info.Size() - tailReadSize
	if seekTo < 0 {
		seekTo = 0
	}
	if _, err := f.Seek(seekTo, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seeking feed file: %w", err)
	}

	scanner := bufio.NewScanner(f)
	if seekTo > 0 {
		scanner.Scan() // skip potential partial first line at cut point
	}

	cutoff := time.Now().Add(-window)
	var result []FeedEvent
	for scanner.Scan() {
		var event FeedEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339, event.Timestamp)
		if err != nil {
			continue
		}
		if !ts.Before(cutoff) {
			result = append(result, event)
		}
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("scanning feed file: %w", err)
	}
	return result, nil
}

// readRecentEvents reads events from the events file within the given time window.
// ZFC: This is the observable state that replaces in-memory caching.
// Reads at most tailReadSize bytes from the end to bound memory usage.
func (c *Curator) readRecentEvents(window time.Duration) ([]events.Event, error) {
	eventsPath := filepath.Join(c.townRoot, events.EventsFile)
	f, err := os.Open(eventsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening events file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat events file: %w", err)
	}
	if info.Size() == 0 {
		return nil, nil
	}

	seekTo := info.Size() - tailReadSize
	if seekTo < 0 {
		seekTo = 0
	}
	if _, err := f.Seek(seekTo, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seeking events file: %w", err)
	}

	scanner := bufio.NewScanner(f)
	if seekTo > 0 {
		scanner.Scan() // skip potential partial first line at cut point
	}

	cutoff := time.Now().Add(-window)
	var result []events.Event
	for scanner.Scan() {
		var event events.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339, event.Timestamp)
		if err != nil {
			continue
		}
		if !ts.Before(cutoff) {
			result = append(result, event)
		}
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("scanning events file: %w", err)
	}
	return result, nil
}

// countRecentSlings counts sling events from an actor within the given window.
// ZFC: Derives count from the events file, not in-memory cache.
func (c *Curator) countRecentSlings(actor string, window time.Duration) int {
	recentEvents, err := c.readRecentEvents(window)
	if err != nil {
		log.Printf("warning: reading recent events for aggregation: %v", err)
	}
	count := 0
	for _, e := range recentEvents {
		if e.Type == events.TypeSling && e.Actor == actor {
			count++
		}
	}
	return count
}

// writeFeedEvent writes a curated event to the feed file.
// ZFC: Aggregation is derived from the events file, not in-memory cache.
func (c *Curator) writeFeedEvent(event *events.Event) {
	feedEvent := FeedEvent{
		Timestamp: event.Timestamp,
		Source:    event.Source,
		Type:      event.Type,
		Actor:     event.Actor,
		Summary:   c.generateSummary(event),
		Payload:   event.Payload,
	}

	// Check for aggregation opportunity (ZFC: derive from events file)
	if event.Type == events.TypeSling {
		slingCount := c.countRecentSlings(event.Actor, c.slingAggregateWindow)
		if slingCount >= c.minAggregateCount {
			feedEvent.Count = slingCount
			feedEvent.Summary = fmt.Sprintf("%s dispatching work to %d agents", event.Actor, slingCount)
		}
	}

	data, err := json.Marshal(feedEvent)
	if err != nil {
		log.Printf("warning: marshaling feed event: %v", err)
		return
	}
	data = append(data, '\n')

	feedPath := filepath.Join(c.townRoot, FeedFile)

	// In-process mutex complements the flock (which only coordinates across processes).
	c.feedMu.Lock()
	defer c.feedMu.Unlock()

	// Acquire cross-process file lock to prevent interleaved writes
	fl := flock.New(feedPath + ".lock")
	if err := fl.Lock(); err != nil {
		log.Printf("warning: acquiring feed file lock: %v", err)
		return
	}
	defer fl.Unlock() //nolint:errcheck // best-effort unlock

	// Truncate if file exceeds max size (keep newest half to avoid thrashing)
	if info, err := os.Stat(feedPath); err == nil && info.Size() > c.maxFeedFileSize {
		c.truncateFeedFile(feedPath, info.Size())
	}

	f, err := os.OpenFile(feedPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		log.Printf("warning: opening feed file: %v", err)
		return
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		log.Printf("warning: writing feed event: %v", err)
		return
	}
	if err := f.Close(); err != nil {
		log.Printf("warning: closing feed file after write: %v", err)
	}
}

// truncateFeedFile keeps the newest half of the feed file using atomic rename.
// Must be called under the feed file flock.
func (c *Curator) truncateFeedFile(feedPath string, currentSize int64) {
	keepBytes := currentSize / 2

	f, err := os.Open(feedPath)
	if err != nil {
		return
	}
	defer f.Close()

	// Seek to the start of the portion we want to keep
	startOffset := currentSize - keepBytes
	if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
		return
	}

	reader := bufio.NewReader(f)

	// Skip to the first complete line (discard partial line at the cut point)
	if _, err := reader.ReadString('\n'); err != nil {
		return // no complete line found in the kept portion
	}

	// Write retained content to a temp file
	tmpPath := feedPath + ".truncate.tmp"
	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return
	}

	if _, err := io.Copy(tmp, reader); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return
	}
	tmp.Close()

	// Close the read handle before rename — Windows cannot rename over open files.
	f.Close()

	// Atomic replace
	os.Rename(tmpPath, feedPath) //nolint:errcheck // best-effort truncation
}

// generateSummary creates a human-readable summary of an event.
func (c *Curator) generateSummary(event *events.Event) string {
	switch event.Type {
	case events.TypeSling:
		if target, ok := event.Payload["target"].(string); ok {
			if bead, ok := event.Payload["bead"].(string); ok {
				return fmt.Sprintf("%s assigned %s to %s", event.Actor, bead, target)
			}
		}
		return fmt.Sprintf("%s dispatched work", event.Actor)

	case events.TypeDone:
		if bead, ok := event.Payload["bead"].(string); ok {
			return fmt.Sprintf("%s completed work on %s", event.Actor, bead)
		}
		return fmt.Sprintf("%s signaled done", event.Actor)

	case events.TypeHandoff:
		return fmt.Sprintf("%s handed off to fresh session", event.Actor)

	case events.TypeMail:
		if to, ok := event.Payload["to"].(string); ok {
			if subj, ok := event.Payload["subject"].(string); ok {
				return fmt.Sprintf("%s → %s: %s", event.Actor, to, subj)
			}
		}
		return fmt.Sprintf("%s sent mail", event.Actor)

	case events.TypePatrolStarted:
		if rig, ok := event.Payload["rig"].(string); ok {
			return fmt.Sprintf("%s patrol started for %s", event.Actor, rig)
		}
		return fmt.Sprintf("%s started patrol", event.Actor)

	case events.TypePatrolComplete:
		if msg, ok := event.Payload["message"].(string); ok {
			return msg
		}
		return fmt.Sprintf("%s completed patrol", event.Actor)

	case events.TypeMerged:
		if worker, ok := event.Payload["worker"].(string); ok {
			return fmt.Sprintf("Merged work from %s", worker)
		}
		return "Work merged"

	case events.TypeMergeFailed:
		if reason, ok := event.Payload["reason"].(string); ok {
			return fmt.Sprintf("Merge failed: %s", reason)
		}
		return "Merge failed"

	case events.TypeSessionDeath:
		session, _ := event.Payload["session"].(string)
		reason, _ := event.Payload["reason"].(string)
		if session != "" && reason != "" {
			return fmt.Sprintf("Session %s terminated: %s", session, reason)
		}
		if session != "" {
			return fmt.Sprintf("Session %s terminated", session)
		}
		return "Session terminated"

	case events.TypeMassDeath:
		count, _ := event.Payload["count"].(float64) // JSON numbers are float64
		possibleCause, _ := event.Payload["possible_cause"].(string)
		if count > 0 && possibleCause != "" {
			return fmt.Sprintf("MASS DEATH: %d sessions died - %s", int(count), possibleCause)
		}
		if count > 0 {
			return fmt.Sprintf("MASS DEATH: %d sessions died simultaneously", int(count))
		}
		return "Multiple sessions died simultaneously"

	default:
		return fmt.Sprintf("%s: %s", event.Actor, event.Type)
	}
}
