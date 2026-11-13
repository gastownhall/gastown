// Package channelevents provides file-based event emission for named channels.
//
// Channel events are JSON files written to ~/gt/events/<channel>/*.event
// and consumed by await-event subscribers (e.g., the refinery watching for
// MERGE_READY events). This is distinct from the activity feed events in
// the events package (~/gt/.events.jsonl).
package channelevents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/steveyegge/gastown/internal/workspace"
)

// RigChannelSeparator separates the rig namespace from the base channel in a
// rig-scoped event channel identifier (e.g., "gastown/refinery").
const RigChannelSeparator = "/"

// UnsetRig is the placeholder default for an unresolved rig context. It is
// treated as "no rig" so bare/single-rig deployments keep working unchanged.
const UnsetRig = "UNSET_RIG"

// ValidChannelName restricts channel names to safe characters (no path traversal).
// Channels may contain a single slash to separate a rig namespace from the
// base channel (e.g., "gastown/refinery"). Empty segments, dots, spaces and
// dots-dot ("..") segments are rejected so the name is a safe single directory
// path below <town>/events/.
var ValidChannelName = regexp.MustCompile(`^[a-zA-Z0-9_-]+(?:/[a-zA-Z0-9_-]+)*$`)

// Scoped returns the rig-scoped event channel for the given base channel,
// namespacing it under the rig (e.g., Scoped("gastown", "refinery") ==
// "gastown/refinery"). When rig is empty or the unresolved UNSET_RIG
// placeholder, the base channel is returned unchanged so single-rig and bare
// town deployments preserve the historical "refinery" channel.
func Scoped(rig, channel string) string {
	if rig == "" || rig == UnsetRig || channel == "" {
		return channel
	}
	return rig + RigChannelSeparator + channel
}

// ResolveRigFromContext determines the current rig for channel namespacing.
// It prefers the GT_RIG environment variable (set for polecat/refinery/crew
// sessions) and falls back to path detection from the current working
// directory within townRoot. Returns "" when no rig context applies.
func ResolveRigFromContext(townRoot string) string {
	if rig := os.Getenv("GT_RIG"); rig != "" && rig != UnsetRig {
		return rig
	}
	if townRoot == "" {
		return ""
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	// Normalize symlinks consistently: os.Getwd() returns the physical path
	// (e.g. /private/tmp when started under /tmp) while callers may pass a
	// logical townRoot. Resolving both prevents Rel() from producing ".." and
	// misreporting "no rig". Falls back to the original path on eval failure.
	townRootEval := townRoot
	if e, err := filepath.EvalSymlinks(townRoot); err == nil {
		townRootEval = e
	}
	cwdEval := cwd
	if e, err := filepath.EvalSymlinks(cwd); err == nil {
		cwdEval = e
	}
	rel, err := filepath.Rel(townRootEval, cwdEval)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	first := strings.Split(rel, string(filepath.Separator))[0]
	if first == "" || first == "." || first == ".." {
		return ""
	}
	// Only treat the first path component as a rig if it is a real directory
	// inside the town root (avoids misfiring at the town root itself).
	if info, err := os.Stat(filepath.Join(townRootEval, first)); err == nil && info.IsDir() {
		return first
	}
	return ""
}

// emitSeq is an atomic counter to ensure unique event filenames even when
// time.Now().UnixNano() has low resolution.
var emitSeq atomic.Uint64

// Emit creates an event file in the channel directory, resolving the town
// root from the current working directory. When a rig context is resolvable,
// the channel is automatically namespaced under the rig (e.g., "refinery"
// becomes "gastown/refinery") so cross-rig consumers on the same base channel
// do not collide.
func Emit(channel, eventType string, payloadPairs []string) (string, error) {
	if !ValidChannelName.MatchString(channel) {
		return "", fmt.Errorf("invalid channel name %q: must match [a-zA-Z0-9_-] optionally joined by '/' (e.g. gastown/refinery)", channel)
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		home, _ := os.UserHomeDir()
		townRoot = filepath.Join(home, "gt")
	}
	channel = Scoped(ResolveRigFromContext(townRoot), channel)
	if !ValidChannelName.MatchString(channel) {
		return "", fmt.Errorf("invalid channel name %q: must match [a-zA-Z0-9_-] optionally joined by '/' (e.g. gastown/refinery)", channel)
	}
	eventDir := filepath.Join(townRoot, "events", channel)
	if err := os.MkdirAll(eventDir, 0755); err != nil {
		return "", fmt.Errorf("creating event directory: %w", err)
	}

	return emitToDir(eventDir, channel, eventType, payloadPairs)
}

// EmitToTown creates an event file using an explicit town root.
// Used by internal callers that already know the town root.
func EmitToTown(townRoot, channel, eventType string, payloadPairs []string) (string, error) {
	if !ValidChannelName.MatchString(channel) {
		return "", fmt.Errorf("invalid channel name %q: must match [a-zA-Z0-9_-] optionally joined by '/' (e.g. gastown/refinery)", channel)
	}

	eventDir := filepath.Join(townRoot, "events", channel)
	if err := os.MkdirAll(eventDir, 0755); err != nil {
		return "", fmt.Errorf("creating event directory: %w", err)
	}
	return emitToDir(eventDir, channel, eventType, payloadPairs)
}

// emitToDir writes an event file to the given directory.
func emitToDir(eventDir, channel, eventType string, payloadPairs []string) (string, error) {
	if !ValidChannelName.MatchString(channel) {
		return "", fmt.Errorf("invalid channel name %q: must match [a-zA-Z0-9_-] optionally joined by '/' (e.g. gastown/refinery)", channel)
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
