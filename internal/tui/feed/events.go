package feed

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/util"
)

// EventSource represents a source of events
type EventSource interface {
	Events() <-chan Event
	Close() error
}

// BdActivitySource reads events from bd activity --follow
type BdActivitySource struct {
	cmd     *exec.Cmd
	events  chan Event
	cancel  context.CancelFunc
	workDir string
}

// NewBdActivitySource creates a new source that tails bd activity
func NewBdActivitySource(workDir string) (*BdActivitySource, error) {
	ctx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(ctx, "bd", "activity", "--follow")
	util.SetDetachedProcessGroup(cmd)
	cmd.Dir = workDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	source := &BdActivitySource{
		cmd:     cmd,
		events:  make(chan Event, 100),
		cancel:  cancel,
		workDir: workDir,
	}

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if event := parseBdActivityLine(line); event != nil {
				select {
				case source.events <- *event:
				default:
					// Drop event if channel full
				}
			}
		}
		close(source.events)
	}()

	return source, nil
}

// Events returns the event channel
func (s *BdActivitySource) Events() <-chan Event {
	return s.events
}

// Close stops the source
func (s *BdActivitySource) Close() error {
	s.cancel()
	return s.cmd.Wait()
}

// bd activity line pattern: [HH:MM:SS] SYMBOL BEAD_ID action · description
var bdActivityPattern = regexp.MustCompile(`^\[(\d{2}:\d{2}:\d{2})\]\s+([+→✓✗⊘📌])\s+(\S+)?\s*(\w+)?\s*·?\s*(.*)$`)

// parseBdActivityLine parses a line from bd activity output
func parseBdActivityLine(line string) *Event {
	matches := bdActivityPattern.FindStringSubmatch(line)
	if matches == nil {
		// Try simpler pattern
		return parseSimpleLine(line)
	}

	timeStr := matches[1]
	symbol := matches[2]
	beadID := matches[3]
	action := matches[4]
	message := matches[5]

	// Parse time (assume today)
	now := time.Now()
	t, err := time.Parse("15:04:05", timeStr)
	if err != nil {
		t = now
	} else {
		t = time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), t.Second(), 0, now.Location())
	}

	// Map symbol to event type
	eventType := "update"
	switch symbol {
	case "+":
		eventType = "create"
	case "→":
		eventType = "update"
	case "✓":
		eventType = "complete"
	case "✗":
		eventType = "fail"
	case "⊘":
		eventType = "delete"
	case "📌":
		eventType = "pin"
	}

	// Try to extract actor and rig from bead ID
	actor, rig, role := parseBeadContext(beadID)

	return &Event{
		Time:    t,
		Type:    eventType,
		Actor:   actor,
		Target:  beadID,
		Message: strings.TrimSpace(action + " " + message),
		Rig:     rig,
		Role:    role,
		Raw:     line,
	}
}

// parseSimpleLine handles lines that don't match the full pattern
func parseSimpleLine(line string) *Event {
	if strings.TrimSpace(line) == "" {
		return nil
	}

	// Try to extract timestamp
	var t time.Time
	if len(line) > 10 && line[0] == '[' {
		if idx := strings.Index(line, "]"); idx > 0 {
			timeStr := line[1:idx]
			now := time.Now()
			if parsed, err := time.Parse("15:04:05", timeStr); err == nil {
				t = time.Date(now.Year(), now.Month(), now.Day(),
					parsed.Hour(), parsed.Minute(), parsed.Second(), 0, now.Location())
			}
		}
	}

	if t.IsZero() {
		t = time.Now()
	}

	return &Event{
		Time:    t,
		Type:    "update",
		Message: line,
		Raw:     line,
	}
}

// parseBeadContext extracts actor/rig/role from a bead ID
// Uses canonical naming: prefix-rig-role-name
// Examples: gt-gastown-crew-joe, gt-gastown-witness, gt-mayor
func parseBeadContext(beadID string) (actor, rig, role string) {
	if beadID == "" {
		return
	}

	// Use the canonical parser
	parsedRig, parsedRole, name, ok := beads.ParseAgentBeadID(beadID)
	if !ok {
		return
	}

	rig = parsedRig
	role = parsedRole

	// Build actor identifier
	switch parsedRole {
	case constants.RoleMayor, constants.RoleDeacon:
		actor = parsedRole
	case constants.RoleWitness, constants.RoleRefinery:
		actor = parsedRole
	case constants.RoleCrew:
		if name != "" {
			actor = parsedRig + "/crew/" + name
		} else {
			actor = parsedRole
		}
	case constants.RolePolecat:
		if name != "" {
			actor = parsedRig + "/" + name
		} else {
			actor = parsedRole
		}
	}

	return
}

// GtEventsSource reads events from ~/gt/.events.jsonl (gt activity log)
type GtEventsSource struct {
	file   *os.File
	events chan Event
	cancel context.CancelFunc
}

// GtEvent is the structure of events in .events.jsonl
type GtEvent struct {
	Timestamp  string                 `json:"ts"`
	Source     string                 `json:"source"`
	Type       string                 `json:"type"`
	Actor      string                 `json:"actor"`
	Payload    map[string]interface{} `json:"payload"`
	Visibility string                 `json:"visibility"`
}

// NewGtEventsSource creates a source that tails ~/gt/.events.jsonl
func NewGtEventsSource(townRoot string) (*GtEventsSource, error) {
	eventsPath := filepath.Join(townRoot, ".events.jsonl")
	file, err := os.Open(eventsPath)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	source := &GtEventsSource{
		file:   file,
		events: make(chan Event, 200),
		cancel: cancel,
	}

	go source.tail(ctx)

	return source, nil
}

// tail loads recent history then follows the file for new events.
func (s *GtEventsSource) tail(ctx context.Context) {
	defer close(s.events)

	// Load recent events (last 200 lines) for initial display
	s.loadRecentEvents()

	// Seek to true EOF so the tail scanner starts cleanly,
	// regardless of the preload scanner's internal read-ahead buffer.
	_, _ = s.file.Seek(0, 2)

	// Now tail for new events
	scanner := bufio.NewScanner(s.file)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for scanner.Scan() {
				line := scanner.Text()
				if event := parseGtEventLine(line); event != nil {
					select {
					case s.events <- *event:
					default:
					}
				}
			}
		}
	}
}

// loadRecentEvents reads the last N lines of the file and emits them as events.
// Uses a ring buffer so memory is O(maxLines) regardless of file size.
func (s *GtEventsSource) loadRecentEvents() {
	const maxLines = 200

	if _, err := s.file.Seek(0, 0); err != nil {
		return
	}

	// Ring buffer: only keep the last maxLines lines in memory
	ring := make([]string, maxLines)
	idx := 0
	count := 0

	scanner := bufio.NewScanner(s.file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		ring[idx%maxLines] = scanner.Text()
		idx++
		count++
	}
	if scanner.Err() != nil {
		// Scanner failed (e.g. token too long) — seek to EOF so tail starts clean
		_, _ = s.file.Seek(0, 2)
		return
	}

	// Emit lines in order (oldest first)
	n := count
	if n > maxLines {
		n = maxLines
	}
	start := idx - n
	for i := start; i < idx; i++ {
		line := ring[i%maxLines]
		if event := parseGtEventLine(line); event != nil {
			select {
			case s.events <- *event:
			default:
			}
		}
	}
}

// Events returns the event channel
func (s *GtEventsSource) Events() <-chan Event {
	return s.events
}

// Close stops the source
func (s *GtEventsSource) Close() error {
	s.cancel()
	return s.file.Close()
}

// parseGtEventLine parses a line from .events.jsonl
func parseGtEventLine(line string) *Event {
	if strings.TrimSpace(line) == "" {
		return nil
	}

	var ge GtEvent
	if err := json.Unmarshal([]byte(line), &ge); err != nil {
		return nil
	}

	// Only show feed-visible events
	if ge.Visibility != "feed" && ge.Visibility != "both" {
		return nil
	}

	t, err := time.Parse(time.RFC3339, ge.Timestamp)
	if err != nil {
		t = time.Now()
	}

	// Extract rig from payload or actor
	rig := ""
	if ge.Payload != nil {
		if r, ok := ge.Payload["rig"].(string); ok {
			rig = r
		}
	}
	if rig == "" && ge.Actor != "" {
		// Extract rig from actor like "gastown/witness"
		parts := strings.Split(ge.Actor, "/")
		if len(parts) > 0 && parts[0] != constants.RoleMayor && parts[0] != constants.RoleDeacon {
			rig = parts[0]
		}
	}

	// Extract role from actor
	role := ""
	if ge.Actor != "" {
		parts := strings.Split(ge.Actor, "/")
		if len(parts) >= 2 {
			role = parts[len(parts)-1]
			// Check for known roles
			switch parts[len(parts)-1] {
			case constants.RoleWitness, constants.RoleRefinery:
				role = parts[len(parts)-1]
			default:
				// Could be polecat name - check second-to-last part
				if len(parts) >= 2 {
					switch parts[len(parts)-2] {
					case "polecats":
						role = constants.RolePolecat
					case constants.RoleCrew:
						role = constants.RoleCrew
					}
				}
			}
		} else if len(parts) == 1 {
			role = parts[0]
		}
	}

	// Build message from event type and payload
	message := buildEventMessage(ge.Type, ge.Payload)

	return &Event{
		Time:    t,
		Type:    ge.Type,
		Actor:   ge.Actor,
		Target:  getPayloadString(ge.Payload, "bead"),
		Message: message,
		Rig:     rig,
		Role:    role,
		Raw:     line,
	}
}

// buildEventMessage creates a human-readable message from event type and payload
func buildEventMessage(eventType string, payload map[string]interface{}) string {
	switch eventType {
	case "patrol_started":
		count := getPayloadInt(payload, "polecat_count")
		if msg := getPayloadString(payload, "message"); msg != "" {
			return msg
		}
		if count > 0 {
			return fmt.Sprintf("patrol started (%d polecats)", count)
		}
		return "patrol started"

	case "patrol_complete":
		count := getPayloadInt(payload, "polecat_count")
		if msg := getPayloadString(payload, "message"); msg != "" {
			return msg
		}
		if count > 0 {
			return fmt.Sprintf("patrol complete (%d polecats)", count)
		}
		return "patrol complete"

	case "polecat_checked":
		polecat := getPayloadString(payload, "polecat")
		status := getPayloadString(payload, "status")
		if polecat != "" {
			if status != "" {
				return fmt.Sprintf("checked %s (%s)", polecat, status)
			}
			return fmt.Sprintf("checked %s", polecat)
		}
		return "polecat checked"

	case "polecat_nudged":
		polecat := getPayloadString(payload, "polecat")
		reason := getPayloadString(payload, "reason")
		if polecat != "" {
			if reason != "" {
				return fmt.Sprintf("nudged %s: %s", polecat, reason)
			}
			return fmt.Sprintf("nudged %s", polecat)
		}
		return "polecat nudged"

	case "escalation_sent":
		target := getPayloadString(payload, "target")
		to := getPayloadString(payload, "to")
		reason := getPayloadString(payload, "reason")
		if target != "" && to != "" {
			if reason != "" {
				return fmt.Sprintf("escalated %s to %s: %s", target, to, reason)
			}
			return fmt.Sprintf("escalated %s to %s", target, to)
		}
		return "escalation sent"

	case "sling":
		bead := getPayloadString(payload, "bead")
		target := getPayloadString(payload, "target")
		if bead != "" && target != "" {
			return fmt.Sprintf("slung %s to %s", bead, target)
		}
		return "work slung"

	case "hook":
		bead := getPayloadString(payload, "bead")
		if bead != "" {
			return fmt.Sprintf("hooked %s", bead)
		}
		return "bead hooked"

	case "handoff":
		subject := getPayloadString(payload, "subject")
		if subject != "" {
			return fmt.Sprintf("handoff: %s", subject)
		}
		return "session handoff"

	case "done":
		bead := getPayloadString(payload, "bead")
		if bead != "" {
			return fmt.Sprintf("done: %s", bead)
		}
		return "work done"

	case "mail":
		subject := getPayloadString(payload, "subject")
		to := getPayloadString(payload, "to")
		if subject != "" {
			if to != "" {
				return fmt.Sprintf("→ %s: %s", to, subject)
			}
			return subject
		}
		return "mail sent"

	case "merged":
		worker := getPayloadString(payload, "worker")
		if worker != "" {
			return fmt.Sprintf("merged work from %s", worker)
		}
		return "merged"

	case "merge_failed":
		reason := getPayloadString(payload, "reason")
		if reason != "" {
			return fmt.Sprintf("merge failed: %s", reason)
		}
		return "merge failed"

	default:
		if msg := getPayloadString(payload, "message"); msg != "" {
			return msg
		}
		return eventType
	}
}

// getPayloadString extracts a string from payload
func getPayloadString(payload map[string]interface{}, key string) string {
	if payload == nil {
		return ""
	}
	if v, ok := payload[key].(string); ok {
		return v
	}
	return ""
}

// getPayloadInt extracts an int from payload
func getPayloadInt(payload map[string]interface{}, key string) int {
	if payload == nil {
		return 0
	}
	if v, ok := payload[key].(float64); ok {
		return int(v)
	}
	return 0
}

// CombinedSource merges events from multiple sources
type CombinedSource struct {
	sources []EventSource
	events  chan Event
	cancel  context.CancelFunc
}

// fanInTimeout is the maximum time a fan-in goroutine will wait for an event
// before checking if it should exit. This prevents goroutine leaks when a source
// channel blocks forever and the context is never canceled.
const fanInTimeout = 30 * time.Second

// NewCombinedSource creates a source that merges multiple event sources
func NewCombinedSource(sources ...EventSource) *CombinedSource {
	ctx, cancel := context.WithCancel(context.Background())

	combined := &CombinedSource{
		sources: sources,
		events:  make(chan Event, 100),
		cancel:  cancel,
	}

	// Fan-in from all sources with timeout to prevent goroutine leaks.
	// Each goroutine will exit if:
	// 1. Context is canceled (Close() called)
	// 2. Source channel is closed
	// 3. No event received for fanInTimeout (prevents indefinite blocking)
	for _, src := range sources {
		go func(s EventSource) {
			for {
				select {
				case <-ctx.Done():
					return
				case event, ok := <-s.Events():
					if !ok {
						return
					}
					select {
					case combined.events <- event:
					case <-ctx.Done():
						return
					default:
						// Drop if full
					}
				case <-time.After(fanInTimeout):
					// Timeout - check if we should exit
					select {
					case <-ctx.Done():
						return
					default:
						// Context still active, continue waiting
					}
				}
			}
		}(src)
	}

	return combined
}

// Events returns the combined event channel
func (c *CombinedSource) Events() <-chan Event {
	return c.events
}

// Close stops all sources
func (c *CombinedSource) Close() error {
	c.cancel()
	var lastErr error
	for _, src := range c.sources {
		if err := src.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// FindBeadsDir finds the beads directory for the given working directory
func FindBeadsDir(workDir string) (string, error) {
	// Walk up looking for .beads
	dir := workDir
	for {
		beadsPath := filepath.Join(dir, ".beads")
		if info, err := os.Stat(beadsPath); err == nil && info.IsDir() {
			return beadsPath, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", os.ErrNotExist
}
