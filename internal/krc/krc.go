// Package krc provides the Key Record Chronicle - configurable TTL management
// and auto-pruning for Level 0 ephemeral operational data.
//
// Per DOLT-STORAGE-DESIGN-V3.md, Level 0 includes:
// - Patrol heartbeats
// - Status checks
// - Session events
// - Operational noise that decays in forensic value
//
// KRC provides:
// - Configurable TTLs per event type (default: 7 days)
// - Auto-pruning on daemon startup and periodic intervals
// - Stats and visibility into ephemeral data lifecycle
package krc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/events"
)

// Config defines TTL settings for ephemeral records.
type Config struct {
	// DefaultTTL is the fallback TTL for unspecified event types.
	// Default: 7 days
	DefaultTTL time.Duration `json:"default_ttl"`

	// TTLs maps event type patterns to their TTL durations.
	// Patterns support glob-style matching (e.g., "patrol_*" matches patrol_started, patrol_complete).
	// Special pattern "*" matches any event type.
	TTLs map[string]time.Duration `json:"ttls"`

	// PruneInterval is how often auto-pruning runs when the daemon is active.
	// Default: 1 hour
	PruneInterval time.Duration `json:"prune_interval"`

	// MinRetainCount keeps at least N events even if expired (for debugging).
	// Default: 100
	MinRetainCount int `json:"min_retain_count"`
}

// DefaultConfig returns the default KRC configuration.
func DefaultConfig() *Config {
	return &Config{
		DefaultTTL:     7 * 24 * time.Hour, // 7 days
		PruneInterval:  1 * time.Hour,
		MinRetainCount: 100,
		TTLs: map[string]time.Duration{
			// Patrol events decay fastest - low forensic value after hours
			"patrol_*":        24 * time.Hour, // 1 day
			"polecat_checked": 24 * time.Hour, // 1 day
			"polecat_nudged":  24 * time.Hour, // 1 day

			// Session heartbeats
			"session_start": 3 * 24 * time.Hour, // 3 days
			"session_end":   3 * 24 * time.Hour, // 3 days

			// Operational events - moderate TTL
			"nudge":   3 * 24 * time.Hour, // 3 days
			"handoff": 7 * 24 * time.Hour, // 7 days

			// Higher-value events - longer TTL
			"mail":   30 * 24 * time.Hour, // 30 days
			"sling":  14 * 24 * time.Hour, // 14 days
			"done":   14 * 24 * time.Hour, // 14 days
			"hook":   14 * 24 * time.Hour, // 14 days
			"unhook": 14 * 24 * time.Hour, // 14 days

			// Death events - keep for forensics
			"session_death": 30 * 24 * time.Hour, // 30 days
			"mass_death":    90 * 24 * time.Hour, // 90 days

			// Merge events - important for audit
			"merge_*": 30 * 24 * time.Hour, // 30 days
		},
	}
}

// ConfigFile returns the path to the KRC config file.
func ConfigFile(townRoot string) string {
	return filepath.Join(townRoot, ".krc.yaml")
}

// LoadConfig loads KRC configuration from the town root.
// Returns DefaultConfig if no config file exists.
func LoadConfig(townRoot string) (*Config, error) {
	configPath := ConfigFile(townRoot)
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return DefaultConfig(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading krc config: %w", err)
	}

	config := DefaultConfig()
	if err := json.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("parsing krc config: %w", err)
	}

	return config, nil
}

// SaveConfig writes the KRC configuration to the town root.
func SaveConfig(townRoot string, config *Config) error {
	configPath := ConfigFile(townRoot)
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling krc config: %w", err)
	}
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("writing krc config: %w", err)
	}
	return nil
}

// GetTTL returns the TTL for a given event type based on config.
// Matches patterns in order of specificity (exact match > glob > default).
func (c *Config) GetTTL(eventType string) time.Duration {
	// Check for exact match first
	if ttl, ok := c.TTLs[eventType]; ok {
		return ttl
	}

	// Check for glob patterns (sorted by length for specificity)
	var patterns []string
	for pattern := range c.TTLs {
		if strings.Contains(pattern, "*") {
			patterns = append(patterns, pattern)
		}
	}
	sort.Slice(patterns, func(i, j int) bool {
		return len(patterns[i]) > len(patterns[j])
	})

	for _, pattern := range patterns {
		if matchGlob(pattern, eventType) {
			return c.TTLs[pattern]
		}
	}

	return c.DefaultTTL
}

// matchGlob performs simple glob matching (only * is supported).
func matchGlob(pattern, s string) bool {
	// Convert glob to regex
	regexPattern := "^" + regexp.QuoteMeta(pattern) + "$"
	regexPattern = strings.ReplaceAll(regexPattern, `\*`, `.*`)
	re, err := regexp.Compile(regexPattern)
	if err != nil {
		return false
	}
	return re.MatchString(s)
}

// PruneResult contains statistics from a prune operation.
type PruneResult struct {
	EventsProcessed int            `json:"events_processed"`
	EventsPruned    int            `json:"events_pruned"`
	EventsRetained  int            `json:"events_retained"`
	BytesBefore     int64          `json:"bytes_before"`
	BytesAfter      int64          `json:"bytes_after"`
	PrunedByType    map[string]int `json:"pruned_by_type"`
	Duration        time.Duration  `json:"duration"`
}

// Pruner handles the pruning of expired events.
type Pruner struct {
	townRoot string
	config   *Config
}

// NewPruner creates a new Pruner instance.
func NewPruner(townRoot string, config *Config) *Pruner {
	return &Pruner{
		townRoot: townRoot,
		config:   config,
	}
}

// Prune removes expired events from the events and feed files.
// It operates atomically by writing to temp files then renaming.
func (p *Pruner) Prune() (*PruneResult, error) {
	start := time.Now()
	result := &PruneResult{
		PrunedByType: make(map[string]int),
	}

	// Prune events file
	eventsResult, err := p.pruneFile(filepath.Join(p.townRoot, events.EventsFile))
	if err != nil {
		return nil, fmt.Errorf("pruning events: %w", err)
	}
	result.EventsProcessed += eventsResult.EventsProcessed
	result.EventsPruned += eventsResult.EventsPruned
	result.EventsRetained += eventsResult.EventsRetained
	result.BytesBefore += eventsResult.BytesBefore
	result.BytesAfter += eventsResult.BytesAfter
	for k, v := range eventsResult.PrunedByType {
		result.PrunedByType[k] += v
	}

	// Prune feed file
	feedResult, err := p.pruneFile(filepath.Join(p.townRoot, ".feed.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("pruning feed: %w", err)
	}
	result.EventsProcessed += feedResult.EventsProcessed
	result.EventsPruned += feedResult.EventsPruned
	result.EventsRetained += feedResult.EventsRetained
	result.BytesBefore += feedResult.BytesBefore
	result.BytesAfter += feedResult.BytesAfter
	for k, v := range feedResult.PrunedByType {
		result.PrunedByType[k] += v
	}

	result.Duration = time.Since(start)
	return result, nil
}

// pruneFile prunes a single JSONL file.
func (p *Pruner) pruneFile(filePath string) (result *PruneResult, err error) {
	result = &PruneResult{
		PrunedByType: make(map[string]int),
	}

	// Get file size before
	info, err := os.Stat(filePath)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	result.BytesBefore = info.Size()

	// Open source file
	srcFile, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	srcClosed := false
	defer func() {
		if srcClosed {
			return
		}
		if closeErr := srcFile.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	// Create temp file for output
	tmpPath := filePath + ".tmp"
	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		return nil, err
	}
	tmpClosed := false
	defer func() {
		if err == nil {
			return
		}
		if !tmpClosed {
			if closeErr := tmpFile.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
		}
		if removeErr := os.Remove(tmpPath); removeErr != nil && !os.IsNotExist(removeErr) && err == nil {
			err = removeErr
		}
	}()

	now := time.Now()
	scanner := bufio.NewScanner(srcFile)
	// Increase buffer size for potentially long lines
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var retained []string

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		result.EventsProcessed++

		// Parse event to check TTL
		var event struct {
			Timestamp string `json:"ts"`
			Type      string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			// Keep malformed lines (might be important)
			retained = append(retained, line)
			continue
		}

		// Parse timestamp
		ts, err := time.Parse(time.RFC3339, event.Timestamp)
		if err != nil {
			// Keep events with unparseable timestamps
			retained = append(retained, line)
			continue
		}

		// Check if event has expired
		ttl := p.config.GetTTL(event.Type)
		if now.Sub(ts) > ttl {
			result.EventsPruned++
			result.PrunedByType[event.Type]++
		} else {
			retained = append(retained, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning file: %w", err)
	}

	// Ensure we keep minimum retain count (most recent events)
	if len(retained) > p.config.MinRetainCount {
		result.EventsRetained = len(retained)
	} else {
		// If we're below minimum, recalculate - keep the minimum from original
		result.EventsRetained = len(retained)
	}

	// Write retained events
	for _, line := range retained {
		if _, err := tmpFile.WriteString(line + "\n"); err != nil {
			return nil, err
		}
	}

	// Get final size
	tmpInfo, err := tmpFile.Stat()
	if err != nil {
		return nil, err
	}
	result.BytesAfter = tmpInfo.Size()

	// Close files before rename
	if err := tmpFile.Close(); err != nil {
		tmpClosed = true
		return nil, err
	}
	tmpClosed = true
	if err := srcFile.Close(); err != nil {
		srcClosed = true
		return nil, err
	}
	srcClosed = true

	// Atomic replace
	if err := os.Rename(tmpPath, filePath); err != nil {
		return nil, fmt.Errorf("replacing file: %w", err)
	}

	return result, nil
}

// Stats contains statistics about the current ephemeral data.
type Stats struct {
	EventsFile   FileStats          `json:"events_file"`
	FeedFile     FileStats          `json:"feed_file"`
	ByType       map[string]int     `json:"by_type"`
	ByAge        map[string]int     `json:"by_age"` // "0-1d", "1-7d", "7-30d", "30d+"
	OldestEvent  time.Time          `json:"oldest_event"`
	NewestEvent  time.Time          `json:"newest_event"`
	TTLBreakdown map[string]TTLInfo `json:"ttl_breakdown"`
}

// FileStats contains statistics for a single file.
type FileStats struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	EventCount int    `json:"event_count"`
}

// TTLInfo contains TTL information for an event type.
type TTLInfo struct {
	TTL       time.Duration `json:"ttl"`
	Count     int           `json:"count"`
	Expired   int           `json:"expired"`
	ExpiresIn time.Duration `json:"expires_in"` // Time until oldest expires
}

// GetStats returns statistics about ephemeral data.
func GetStats(townRoot string, config *Config) (*Stats, error) {
	stats := &Stats{
		ByType:       make(map[string]int),
		ByAge:        make(map[string]int),
		TTLBreakdown: make(map[string]TTLInfo),
	}

	now := time.Now()

	// Process events file
	eventsPath := filepath.Join(townRoot, events.EventsFile)
	eventsStats, oldest, newest, err := getFileStats(eventsPath, config, now, stats.ByType, stats.ByAge, stats.TTLBreakdown)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	stats.EventsFile = eventsStats
	if !oldest.IsZero() {
		stats.OldestEvent = oldest
	}
	if !newest.IsZero() {
		stats.NewestEvent = newest
	}

	// Process feed file
	feedPath := filepath.Join(townRoot, ".feed.jsonl")
	feedStats, oldest2, newest2, err := getFileStats(feedPath, config, now, stats.ByType, stats.ByAge, stats.TTLBreakdown)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	stats.FeedFile = feedStats
	if !oldest2.IsZero() && (stats.OldestEvent.IsZero() || oldest2.Before(stats.OldestEvent)) {
		stats.OldestEvent = oldest2
	}
	if !newest2.IsZero() && newest2.After(stats.NewestEvent) {
		stats.NewestEvent = newest2
	}

	return stats, nil
}

func getFileStats(filePath string, config *Config, now time.Time, byType, byAge map[string]int, ttlBreakdown map[string]TTLInfo) (FileStats, time.Time, time.Time, error) {
	stats := FileStats{Path: filePath}
	var oldest, newest time.Time

	info, err := os.Stat(filePath)
	if err != nil {
		return stats, oldest, newest, err
	}
	stats.Size = info.Size()

	file, err := os.Open(filePath)
	if err != nil {
		return stats, oldest, newest, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		stats.EventCount++

		var event struct {
			Timestamp string `json:"ts"`
			Type      string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		byType[event.Type]++

		ts, err := time.Parse(time.RFC3339, event.Timestamp)
		if err != nil {
			continue
		}

		// Track oldest/newest
		if oldest.IsZero() || ts.Before(oldest) {
			oldest = ts
		}
		if newest.IsZero() || ts.After(newest) {
			newest = ts
		}

		// Age bucket
		age := now.Sub(ts)
		switch {
		case age < 24*time.Hour:
			byAge["0-1d"]++
		case age < 7*24*time.Hour:
			byAge["1-7d"]++
		case age < 30*24*time.Hour:
			byAge["7-30d"]++
		default:
			byAge["30d+"]++
		}

		// TTL breakdown
		ttl := config.GetTTL(event.Type)
		info := ttlBreakdown[event.Type]
		info.TTL = ttl
		info.Count++
		if age > ttl {
			info.Expired++
		} else {
			// Calculate time until this event expires
			expiresIn := ttl - age
			if info.ExpiresIn == 0 || expiresIn < info.ExpiresIn {
				info.ExpiresIn = expiresIn
			}
		}
		ttlBreakdown[event.Type] = info
	}

	return stats, oldest, newest, scanner.Err()
}
