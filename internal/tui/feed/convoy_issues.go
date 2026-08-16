package feed

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/util"
)

type trackedStatus struct {
	ID     string
	Status string
}

// getTrackedIssueStatus queries tracked issues and their status.
func getTrackedIssueStatus(beadsDir, convoyID string) []trackedStatus {
	if !convoyIDPattern.MatchString(convoyID) {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), constants.BdSubprocessTimeout)
	defer cancel()

	// Query tracked issues using bd dep list (returns full issue details)
	cmd := exec.CommandContext(ctx, "bd", "dep", "list", convoyID, "-t", "tracks", "--json")
	util.SetDetachedProcessGroup(cmd)
	cmd.Dir = beadsDir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil
	}

	var deps []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &deps); err != nil {
		return nil
	}

	// Extract raw issue IDs
	for i := range deps {
		deps[i].ID = beads.ExtractIssueID(deps[i].ID)
	}

	// Refresh status via cross-rig lookup. bd dep list returns status from
	// the dependency record in HQ beads which is never updated when cross-rig
	// issues (e.g., gt-* tracked by hq-* convoys) are closed in their rig.
	freshStatus := refreshTrackedStatus(ctx, deps)

	var tracked []trackedStatus
	for _, dep := range deps {
		status := dep.Status
		if fresh, ok := freshStatus[dep.ID]; ok {
			status = fresh
		}
		tracked = append(tracked, trackedStatus{ID: dep.ID, Status: status})
	}

	return tracked
}

// refreshTrackedStatus does a batch bd show to get current status for tracked issues.
func refreshTrackedStatus(ctx context.Context, deps []struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}) map[string]string {
	if len(deps) == 0 {
		return nil
	}

	args := []string{"show"}
	for _, d := range deps {
		args = append(args, d.ID)
	}
	args = append(args, "--json")

	cmd := exec.CommandContext(ctx, "bd", args...)
	util.SetDetachedProcessGroup(cmd)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return nil
	}

	var issues []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &issues); err != nil {
		return nil
	}

	result := make(map[string]string, len(issues))
	for _, issue := range issues {
		result[issue.ID] = issue.Status
	}
	return result
}
