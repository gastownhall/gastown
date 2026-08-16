// Mountain failure tracking for the Mountain-Eater (Layer 1).
//
// When a polecat exits without completing its hooked bead, this module checks
// if the issue belongs to a convoy with the "mountain" label. For mountain
// convoys, it increments a failure count (stored as mountain:failures:N label).
// After 3 failures, the issue is auto-skipped (marked blocked + mountain:skipped).
// For regular convoys, a warning is logged.
//
// See docs/design/convoy/mountain-eater.md section 5.
package witness

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jonbaldie/gastown/internal/worker"
	"github.com/jonbaldie/gastown/internal/workspace"
)

// MountainMaxFailures is the number of polecat failures before an issue is
// auto-skipped in a mountain convoy. Exported for testing.
const MountainMaxFailures = 3

// ConvoyFailureResult tracks the result of convoy failure tracking for a single issue.
type ConvoyFailureResult struct {
	IssueID      string
	ConvoyID     string // Tracking convoy (if any)
	IsMountain   bool   // Convoy has "mountain" label
	FailureCount int    // New failure count after increment
	Skipped      bool   // Issue was auto-skipped (count >= MountainMaxFailures)
	Warning      string // Warning message for regular convoys
	Error        error
}

// trackConvoyFailures processes zombie detection results for convoy failure tracking.
// For each zombie that had active work on a hook_bead (polecat failed without
// completing), checks if the issue belongs to a convoy and tracks the failure.
// Called from DetectZombiePolecats after all zombies are collected.
func trackConvoyFailures(bd *BdCli, workDir string, result *DetectZombiePolecatsResult) {
	for i := range result.Zombies {
		zombie := &result.Zombies[i]

		// Only track failures for zombies that had active work on an issue and
		// didn't complete it. Submitted/orphan cleanup states can still carry a
		// hook_bead for traceability, but they must not increment mountain failure
		// counts.
		if zombie.HookBead == "" || !zombieImpliesActiveFailure(*zombie) {
			continue
		}
		townRoot, findErr := workspace.Find(workDir)
		if findErr != nil || townRoot == "" {
			townRoot = workDir
		}
		if !worker.StoppedWithoutDone(townRoot, zombie.HookBead) {
			continue
		}

		cfr := TrackConvoyFailure(bd, workDir, zombie.HookBead)
		if cfr == nil {
			continue // Not convoy-tracked
		}

		if cfr.IsMountain {
			if cfr.Skipped {
				fmt.Fprintf(os.Stderr, "witness: Mountain: skipped %s after %d failures (convoy %s)\n",
					cfr.IssueID, cfr.FailureCount, cfr.ConvoyID)
			} else {
				fmt.Fprintf(os.Stderr, "witness: Mountain: %s failure %d/%d (convoy %s)\n",
					cfr.IssueID, cfr.FailureCount, MountainMaxFailures, cfr.ConvoyID)
			}
		} else if cfr.Warning != "" {
			fmt.Fprintf(os.Stderr, "witness: %s\n", cfr.Warning)
		}

		if cfr.Error != nil {
			fmt.Fprintf(os.Stderr, "witness: convoy failure tracking error for %s: %v\n",
				cfr.IssueID, cfr.Error)
		}

		result.ConvoyFailures = append(result.ConvoyFailures, *cfr)
	}
}

func zombieImpliesActiveFailure(zombie ZombieResult) bool {
	switch zombie.Classification {
	case ZombieBeadClosedStillRunning, ZombieSubmittedStillRunning:
		return false
	}
	if zombie.Classification != "" {
		return zombie.Classification.ImpliesActiveWork()
	}
	return zombie.WasActive
}

// TrackConvoyFailure checks if an issue belongs to a convoy and tracks the
// polecat failure. For mountain convoys, increments the failure count and
// auto-skips after MountainMaxFailures. For regular convoys, returns a warning.
//
// Returns nil if the issue has no tracking convoy.
func TrackConvoyFailure(bd *BdCli, workDir, issueID string) *ConvoyFailureResult {
	if issueID == "" {
		return nil
	}

	// Find convoys tracking this issue (dependents with type "tracks")
	convoyIDs := getTrackingConvoysCLI(bd, workDir, issueID)
	if len(convoyIDs) == 0 {
		return nil
	}

	// Check each convoy for the "mountain" label
	for _, convoyID := range convoyIDs {
		labels := getBeadLabels(bd, workDir, convoyID)
		isMountain := hasLabel(labels, "mountain")

		result := &ConvoyFailureResult{
			IssueID:    issueID,
			ConvoyID:   convoyID,
			IsMountain: isMountain,
		}

		if isMountain {
			result.Error = trackMountainFailure(bd, workDir, issueID, result)
		} else {
			result.Warning = fmt.Sprintf("polecat failure on convoy-tracked issue %s (convoy %s)", issueID, convoyID)
		}

		// Return after processing first matching convoy (an issue typically
		// belongs to at most one active convoy).
		return result
	}

	return nil
}

// trackMountainFailure increments the failure count for a mountain-tracked
// issue and auto-skips if the count reaches MountainMaxFailures.
func trackMountainFailure(bd *BdCli, workDir, issueID string, result *ConvoyFailureResult) error {
	// Get current failure count from issue labels
	issueLabels := getBeadLabels(bd, workDir, issueID)
	currentCount := getMountainFailureCount(issueLabels)
	newCount := currentCount + 1
	result.FailureCount = newCount

	// Update failure count label
	if err := updateMountainFailureCount(bd, workDir, issueID, currentCount, newCount); err != nil {
		return fmt.Errorf("updating failure count: %w", err)
	}

	// Auto-skip after MountainMaxFailures
	if newCount >= MountainMaxFailures {
		if err := skipMountainIssue(bd, workDir, issueID, newCount); err != nil {
			return fmt.Errorf("skipping issue: %w", err)
		}
		result.Skipped = true
	}

	return nil
}

// getTrackingConvoysCLI finds convoy IDs that track a given issue using the bd CLI.
// Returns convoy IDs (dependents with type "tracks").
func getTrackingConvoysCLI(bd *BdCli, workDir, issueID string) []string {
	output, err := bd.Exec(workDir, "dep", "list", issueID, "--direction=up", "--type=tracks", "--json")
	if err != nil || output == "" || output == "[]" || output == "null" {
		return nil
	}

	var deps []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(output), &deps); err != nil {
		return nil
	}

	ids := make([]string, 0, len(deps))
	for _, d := range deps {
		ids = append(ids, d.ID)
	}
	return ids
}

// getBeadLabels returns the labels for a bead.
func getBeadLabels(bd *BdCli, workDir, beadID string) []string {
	output, err := bd.Exec(workDir, "show", beadID, "--json")
	if err != nil || output == "" {
		return nil
	}

	var issues []struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal([]byte(output), &issues); err != nil || len(issues) == 0 {
		return nil
	}
	return issues[0].Labels
}

// hasLabel checks if a label list contains a specific label.
func hasLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// getMountainFailureCount extracts the failure count from labels.
// Looks for labels matching "mountain:failures:N" and returns N.
// Returns 0 if no failure label is found.
func getMountainFailureCount(labels []string) int {
	for _, l := range labels {
		if after, ok := strings.CutPrefix(l, "mountain:failures:"); ok {
			if n, err := strconv.Atoi(after); err == nil {
				return n
			}
		}
	}
	return 0
}

// updateMountainFailureCount updates the mountain:failures:N label on an issue.
// Removes the old count label (if any) and adds the new one.
func updateMountainFailureCount(bd *BdCli, workDir, issueID string, oldCount, newCount int) error {
	args := []string{"update", issueID}
	if oldCount > 0 {
		args = append(args, "--remove-label", fmt.Sprintf("mountain:failures:%d", oldCount))
	}
	args = append(args, "--add-label", fmt.Sprintf("mountain:failures:%d", newCount))
	return bd.Run(workDir, args...)
}

// skipMountainIssue marks an issue as blocked and adds the mountain:skipped label.
// This removes the issue from the convoy's ready front, allowing the convoy to
// continue grinding around it.
func skipMountainIssue(bd *BdCli, workDir, issueID string, failureCount int) error {
	return bd.Run(workDir, "update", issueID,
		"--status=blocked",
		"--add-label", "mountain:skipped",
		"--notes", fmt.Sprintf("Skipped by Mountain-Eater after %d polecat failures", failureCount),
	)
}
