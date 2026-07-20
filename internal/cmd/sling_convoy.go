package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/workspace"
)

// slingGenerateShortID generates a short random ID (5 lowercase chars).
func slingGenerateShortID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return strings.ToLower(base32.StdEncoding.EncodeToString(b)[:5])
}

// isTrackedByConvoy checks if an issue is already being tracked by a convoy.
// Returns the convoy ID if tracked, empty string otherwise.
//
// Uses bdDepListRawIDs for cross-database dep resolution (GH #2624).
// For direction=up queries, the raw SQL approach queries the same table but
// looks for rows where depends_on_id matches the beadID, returning the
// issue_id (which is the convoy). Since this only returns IDs (no issue_type
// or status), we verify each candidate via bd show.
func isTrackedByConvoy(beadID string) string {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return ""
	}
	townBeads := filepath.Join(townRoot, ".beads")

	// Primary: Use raw dep query to find what tracks this issue (direction=up).
	// This returns convoy IDs that have a "tracks" dep on beadID.
	trackerIDs, err := bdDepListRawIDs(townBeads, beadID, "up", "tracks")
	if err == nil && len(trackerIDs) > 0 {
		// Check each tracker to find an open convoy
		for _, trackerID := range trackerIDs {
			result, err := bdShow(trackerID)
			if err != nil {
				continue
			}
			if isConvoyIssue(result.IssueType, result.Labels) && result.Status == "open" {
				return trackerID
			}
		}
	}

	// Fallback: Query convoys directly by description pattern
	// This is more robust when cross-rig routing has issues (G19, G21)
	// Auto-convoys have description "Auto-created convoy tracking <beadID>"
	return findConvoyByDescription(townRoot, beadID)
}

// findConvoyByDescription searches open convoys for one tracking the given beadID.
// Checks both convoy descriptions (for auto-created convoys) and tracked deps
// (for manually-created convoys where the description won't match).
// Returns convoy ID if found, empty string otherwise.
func findConvoyByDescription(townRoot, beadID string) string {
	townBeads := filepath.Join(townRoot, ".beads")

	convoys, err := listConvoyIssues(townBeads, "open", false)
	if err != nil {
		return ""
	}

	// Check if any convoy's description mentions tracking this beadID
	// (matches auto-created convoys with "Auto-created convoy tracking <beadID>")
	trackingPattern := fmt.Sprintf("tracking %s", beadID)
	for _, convoy := range convoys {
		if strings.Contains(convoy.Description, trackingPattern) {
			return convoy.ID
		}
	}

	// Check tracked deps of each convoy (for manually-created convoys).
	// This handles the case where cross-rig dep resolution (direction=up) fails
	// but the convoy does have a tracks dependency on the bead.
	for _, convoy := range convoys {
		if convoyTracksBead(townBeads, convoy.ID, beadID) {
			return convoy.ID
		}
	}

	return ""
}

// convoyTracksBead checks if a convoy has a tracks dependency on the given beadID.
// Uses bdDepListRawIDs for cross-database dep resolution (GH #2624).
func convoyTracksBead(beadsDir, convoyID, beadID string) bool {
	trackedIDs, err := bdDepListRawIDs(beadsDir, convoyID, "down", "tracks")
	if err != nil {
		return false
	}

	for _, id := range trackedIDs {
		if id == beadID {
			return true
		}
	}
	return false
}

// ConvoyInfo holds convoy details for an issue's tracking convoy.
type ConvoyInfo struct {
	ID            string // Convoy bead ID (e.g., "hq-cv-abc")
	Owned         bool   // true if convoy has gt:owned label
	MergeStrategy string // "direct", "mr", "local", or "" (default = mr)
}

// IsOwnedDirect returns true if the convoy is owned with direct merge strategy.
// This is the key check for skipping witness/refinery merge pipeline.
func (c *ConvoyInfo) IsOwnedDirect() bool {
	return c != nil && c.Owned && c.MergeStrategy == "direct"
}

// getConvoyInfoForIssue checks if an issue is tracked by a convoy and returns its info.
// Returns nil if not tracked by any convoy.
func getConvoyInfoForIssue(issueID string) *ConvoyInfo {
	convoyID := isTrackedByConvoy(issueID)
	if convoyID == "" {
		return nil
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return nil
	}
	townBeads := filepath.Join(townRoot, ".beads")

	var stderr bytes.Buffer
	stdout, err := BdCmd("show", convoyID, "--json").
		AllowStale().
		Dir(townRoot).
		WithBeadsDir(townBeads).
		Stderr(&stderr).
		Output()

	if err != nil {
		// Check if this is a "not found" error (phantom convoy) vs transient error.
		// Phantom convoys occur when a convoy bead is deleted from HQ but tracking
		// deps still exist in local beads DB (gt-9xum2). Return nil to treat as
		// untracked, allowing normal MR flow to proceed.
		stderrStr := stderr.String()
		if strings.Contains(stderrStr, "not found") ||
			strings.Contains(stderrStr, "Issue not found") ||
			strings.Contains(stderrStr, "no issue found") {
			return nil // Phantom convoy - proceed without convoy context
		}
		// Other error (transient) - return basic info as fallback
		return &ConvoyInfo{ID: convoyID}
	}

	var convoys []struct {
		Labels      []string `json:"labels"`
		Description string   `json:"description"`
	}
	if err := json.Unmarshal(stdout, &convoys); err != nil || len(convoys) == 0 {
		return &ConvoyInfo{ID: convoyID}
	}

	info := &ConvoyInfo{ID: convoyID}

	// Check for gt:owned label
	for _, label := range convoys[0].Labels {
		if label == "gt:owned" {
			info.Owned = true
			break
		}
	}

	// Parse merge strategy from description using typed accessor
	info.MergeStrategy = convoyMergeFromFields(convoys[0].Description)

	return info
}

// getConvoyInfoFromIssue reads convoy info directly from the issue's attachment fields.
// This is the primary lookup method (gt-7b6wf fix): gt sling stores convoy_id and
// merge_strategy on the issue when dispatching, avoiding unreliable cross-rig dep
// resolution. Returns nil if the issue has no convoy fields in its description.
func getConvoyInfoFromIssue(issueID, cwd string) *ConvoyInfo {
	if issueID == "" {
		return nil
	}

	bd := beads.New(beads.ResolveBeadsDir(cwd))
	issue, err := bd.Show(issueID)
	if err != nil {
		return nil
	}

	return getConvoyInfoFromSourceIssue(issue)
}

func getConvoyInfoFromSourceIssue(issue *beads.Issue) *ConvoyInfo {
	attachment := beads.ParseAttachmentFields(issue)
	if attachment == nil || attachment.ConvoyID == "" {
		return nil
	}

	return &ConvoyInfo{
		ID:            attachment.ConvoyID,
		MergeStrategy: attachment.MergeStrategy,
		Owned:         attachment.ConvoyOwned,
	}
}

// printConvoyConflict prints detailed information about a bead that is already
// tracked by another convoy, including all beads in that convoy with their
// statuses, and recommended actions the user can take.
func printConvoyConflict(beadID, convoyID string) {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		fmt.Printf("\n  %s is already tracked by convoy %s\n", beadID, convoyID)
		return
	}
	townBeads := filepath.Join(townRoot, ".beads")

	var convoyTitle string
	showOut, err := BdCmd("show", convoyID, "--json").
		AllowStale().
		Dir(townRoot).
		WithBeadsDir(townBeads).
		Stderr(io.Discard).
		Output()
	if err == nil {
		var items []struct {
			Title string `json:"title"`
		}
		if json.Unmarshal(showOut, &items) == nil && len(items) > 0 {
			convoyTitle = items[0].Title
		}
	}

	fmt.Printf("\n  Conflict: %s is already tracked by convoy %s", beadID, convoyID)
	if convoyTitle != "" {
		fmt.Printf(" (%s)", convoyTitle)
	}
	fmt.Println()

	// Get all beads in the conflicting convoy
	tracked, err := getTrackedIssues(townBeads, convoyID)
	if err == nil && len(tracked) > 0 {
		fmt.Printf("\n  Beads in convoy %s:\n", convoyID)
		for _, t := range tracked {
			marker := " "
			if t.ID == beadID {
				marker = "→"
			}
			statusIcon := "○"
			switch t.Status {
			case "open":
				statusIcon = "●"
			case "closed":
				statusIcon = "✓"
			case "hooked", "pinned":
				statusIcon = "◆"
			}
			title := t.Title
			if title == "" {
				title = "(no title)"
			}
			suffix := ""
			if t.ID == beadID {
				suffix = "  ← conflict"
			}
			fmt.Printf("    %s %s %s  %s [%s]%s\n", marker, statusIcon, t.ID, title, t.Status, suffix)
		}
	}

	fmt.Printf("\n  Options:\n")
	fmt.Printf("    1. Remove the bead from this batch:\n")
	fmt.Printf("         gt sling <other-beads...> <rig>   (without %s)\n", beadID)
	fmt.Printf("    2. Move the bead to the new batch (remove from existing convoy first):\n")
	fmt.Printf("         bd dep remove %s %s --type=tracks\n", convoyID, beadID)
	fmt.Printf("         gt sling <all-beads...> <rig>\n")
	fmt.Printf("    3. Close the existing convoy and re-sling all beads together:\n")
	fmt.Printf("         gt convoy close %s --reason \"re-batching\"\n", convoyID)
	fmt.Printf("         gt sling <all-beads...> <rig>\n")
	fmt.Printf("    4. Add the other beads to the existing convoy instead:\n")
	fmt.Printf("         gt convoy add %s <other-beads...>\n", convoyID)
	fmt.Println()
}

// createBatchConvoy creates a single auto-convoy that tracks all beads in a batch sling.
// Returns the convoy ID and the list of bead IDs that were successfully tracked.
// Callers should only stamp ConvoyID on beads in the tracked set — a bead whose
// dep add failed should not reference a convoy that has no knowledge of it.
// If owned is true, the convoy is marked with gt:owned label.
// beadIDs must be non-empty. The convoy title uses the rig name and bead count.
func createBatchConvoy(beadIDs []string, rigName string, owned bool, mergeStrategy, baseBranch string) (string, []string, error) {
	if len(beadIDs) == 0 {
		return "", nil, fmt.Errorf("no beads to track")
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return "", nil, fmt.Errorf("finding town root: %w", err)
	}

	townBeads := filepath.Join(townRoot, ".beads")

	convoyID := fmt.Sprintf("hq-cv-%s", slingGenerateShortID())

	convoyTitle := fmt.Sprintf("Batch: %d beads to %s", len(beadIDs), rigName)
	prose := fmt.Sprintf("Auto-created convoy tracking %d beads", len(beadIDs))
	description := beads.SetConvoyFields(&beads.Issue{Description: prose}, &beads.ConvoyFields{
		Merge:      mergeStrategy,
		BaseBranch: baseBranch,
	})

	createArgs := []string{
		"create",
		"--type=task",
		"--id=" + convoyID,
		"--title=" + convoyTitle,
		"--description=" + description,
		"--labels=" + convoyLabels(owned),
	}
	if beads.NeedsForceForID(convoyID) {
		createArgs = append(createArgs, "--force")
	}

	// Use BdCmd with WithAutoCommit to ensure convoy is persisted even when
	// gt sling has set BD_DOLT_AUTO_COMMIT=off globally (gt-9xum2 root cause fix).
	if out, err := BdCmd(createArgs...).Dir(townBeads).WithAutoCommit().CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("creating batch convoy: %w\noutput: %s", err, out)
	}

	// Add tracking relations for all beads, recording which succeed.
	// Use WithAutoCommit for the same reason as above.
	var tracked []string
	for _, beadID := range beadIDs {
		if err := addTrackingRelationFn(townRoot, convoyID, beadID); err != nil {
			// Log but continue — partial tracking is better than no tracking
			fmt.Printf("  Warning: could not track %s in convoy: %v\n", beadID, err)
		} else {
			tracked = append(tracked, beadID)
		}
	}

	return convoyID, tracked, nil
}

// createAutoConvoy creates an auto-convoy for a single issue and tracks it.
// If owned is true, the convoy is marked with the gt:owned label for caller-managed lifecycle.
// mergeStrategy is optional: "direct", "mr", or "local" (empty = default mr).
// Returns the created convoy ID.
func createAutoConvoy(beadID, beadTitle string, owned bool, mergeStrategy, baseBranch string) (_ string, retErr error) {
	defer func() { telemetry.RecordConvoyCreate(context.Background(), beadID, retErr) }()
	// Guard against flag-like titles propagating into convoy names (gt-e0kx5)
	if beads.IsFlagLikeTitle(beadTitle) {
		return "", fmt.Errorf("refusing to create convoy: bead title %q looks like a CLI flag", beadTitle)
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return "", fmt.Errorf("finding town root: %w", err)
	}

	townBeads := filepath.Join(townRoot, ".beads")

	// Generate convoy ID with hq-cv- prefix for visual distinction
	// The hq-cv- prefix is registered in routes during gt install
	convoyID := fmt.Sprintf("hq-cv-%s", slingGenerateShortID())

	// Create convoy with title "Work: <issue-title>"
	convoyTitle := fmt.Sprintf("Work: %s", beadTitle)
	prose := fmt.Sprintf("Auto-created convoy tracking %s", beadID)
	description := beads.SetConvoyFields(&beads.Issue{Description: prose}, &beads.ConvoyFields{
		Merge:      mergeStrategy,
		BaseBranch: baseBranch,
	})

	createArgs := []string{
		"create",
		"--type=task",
		"--id=" + convoyID,
		"--title=" + convoyTitle,
		"--description=" + description,
		"--labels=" + convoyLabels(owned),
	}
	if beads.NeedsForceForID(convoyID) {
		createArgs = append(createArgs, "--force")
	}

	// Use BdCmd with WithAutoCommit to ensure convoy is persisted even when
	// gt sling has set BD_DOLT_AUTO_COMMIT=off globally (gt-9xum2 root cause fix).
	if out, err := BdCmd(createArgs...).Dir(townBeads).WithAutoCommit().CombinedOutput(); err != nil {
		return "", fmt.Errorf("creating convoy: %w\noutput: %s", err, out)
	}

	// Add tracking relation: convoy tracks the issue.
	if err := addTrackingRelationFn(townRoot, convoyID, beadID); err != nil {
		fmt.Printf("Warning: Could not create auto-convoy tracking: %v\n", err)
	}

	return convoyID, nil
}
