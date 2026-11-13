// Package channelevents provides file-based event emission for named channels.
//
// Channel events are JSON files written to ~/gt/events/<channel>/*.event
// and consumed by await-event subscribers (e.g., the refinery watching for
// MERGE_READY events). This is distinct from the activity feed events in
// the events package (~/gt/.events.jsonl).
package channelevents

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/steveyegge/gastown/internal/testmode"
	"github.com/steveyegge/gastown/internal/workspace"
)

// ValidChannelName restricts channel names to safe characters (no path traversal).
var ValidChannelName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// emitSeq is an atomic counter to ensure unique event filenames even when
// time.Now().UnixNano() has low resolution.
var emitSeq atomic.Uint64

// ErrLiveTownInTest is returned when a test binary tries to emit into the
// live town's event directories. Channels are town-global and agents block
// on them, so a synthetic event from the test suite wakes every rig's
// refinery/witness for work that does not exist (gt-dog).
var ErrLiveTownInTest = errors.New("refusing to emit a channel event into the live town from a test binary")

// liveTownRoot is the town root this process started in, resolved once at
// package init — before any test can chdir into a temp directory. Tests that
// build their own town under t.TempDir() are unaffected; only writes that
// land inside the real workspace are refused.
var liveTownRoot = resolveLiveTownRoot()

func resolveLiveTownRoot() string {
	root, err := workspace.FindFromCwd()
	if err != nil || root == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil || home == "" {
			return ""
		}
		root = filepath.Join(home, "gt")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return root
	}
	return abs
}

// guardLiveTown blocks test binaries from writing into the live town root.
// This is a backstop: callers should already be skipping their side effects
// under testmode.Active(). It exists because a missed guard is silent
// production pollution that only shows up as token burn on other agents.
func guardLiveTown(eventDir string) error {
	if !testmode.Active() || liveTownRoot == "" {
		return nil
	}
	abs, err := filepath.Abs(eventDir)
	if err != nil {
		// Cannot prove the destination is safe — refuse.
		return fmt.Errorf("%w: %s", ErrLiveTownInTest, eventDir)
	}
	if abs == liveTownRoot || strings.HasPrefix(abs, liveTownRoot+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s (set %s to override)", ErrLiveTownInTest, abs, testmode.AllowEnv)
	}
	return nil
}

// Emit creates an event file in the channel directory, resolving the town
// root from the current working directory.
func Emit(channel, eventType string, payloadPairs []string) (string, error) {
	if !ValidChannelName.MatchString(channel) {
		return "", fmt.Errorf("invalid channel name %q: must match [a-zA-Z0-9_-]", channel)
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		home, _ := os.UserHomeDir()
		townRoot = filepath.Join(home, "gt")
	}
	return emitToDir(filepath.Join(townRoot, "events", channel), channel, eventType, payloadPairs)
}

// EmitToTown creates an event file using an explicit town root.
// Used by internal callers that already know the town root.
func EmitToTown(townRoot, channel, eventType string, payloadPairs []string) (string, error) {
	if !ValidChannelName.MatchString(channel) {
		return "", fmt.Errorf("invalid channel name %q: must match [a-zA-Z0-9_-]", channel)
	}

	return emitToDir(filepath.Join(townRoot, "events", channel), channel, eventType, payloadPairs)
}

// emitToDir writes an event file to the given directory, creating it if
// needed. It is the single funnel for every emission, so the live-town guard
// lives here — before any directory is created.
func emitToDir(eventDir, channel, eventType string, payloadPairs []string) (string, error) {
	if !ValidChannelName.MatchString(channel) {
		return "", fmt.Errorf("invalid channel name %q: must match [a-zA-Z0-9_-]", channel)
	}

	if err := guardLiveTown(eventDir); err != nil {
		return "", err
	}

	if err := os.MkdirAll(eventDir, 0755); err != nil {
		return "", fmt.Errorf("creating event directory: %w", err)
	}

	payload := make(map[string]string)
	for _, pair := range payloadPairs {
		key, val, found := strings.Cut(pair, "=")
		if found {
			payload[key] = val
		}
	}

	now := time.Now()
	event := map[string]interface{}{
		"type":      eventType,
		"channel":   channel,
		"timestamp": now.Format(time.RFC3339),
		"payload":   payload,
	}

	data, err := json.MarshalIndent(event, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling event: %w", err)
	}

	seq := emitSeq.Add(1)
	eventFile := filepath.Join(eventDir, fmt.Sprintf("%d-%d-%d.event", now.UnixNano(), seq, os.Getpid()))
	if err := os.WriteFile(eventFile, data, 0644); err != nil {
		return "", fmt.Errorf("writing event file: %w", err)
	}

	return eventFile, nil
}
