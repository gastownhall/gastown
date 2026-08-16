package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/constants"
)

// RunResult represents the outcome of a plugin execution.
type RunResult string

const (
	ResultSuccess RunResult = "success"
	ResultFailure RunResult = "failure"
	ResultSkipped RunResult = "skipped"
)

// PluginRunRecord represents data for creating a plugin run bead.
type PluginRunRecord struct {
	PluginName  string
	RigName     string
	Result      RunResult
	Title       string
	Body        string
	ExtraLabels []string
}

// PluginRunBead represents a recorded plugin run from the ledger.
type PluginRunBead struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	Labels    []string  `json:"labels"`
	Result    RunResult `json:"-"` // Parsed from labels
}

// Recorder handles plugin run recording and querying.
type Recorder struct {
	townRoot string
}

// NewRecorder creates a new plugin run recorder.
func NewRecorder(townRoot string) *Recorder {
	return &Recorder{townRoot: townRoot}
}

// RecordRun creates an ephemeral bead for a plugin run.
// This is pure data writing - the caller decides what result to record.
func (r *Recorder) RecordRun(record PluginRunRecord) (string, error) {
	title := record.Title
	if title == "" {
		title = fmt.Sprintf("Plugin run: %s", record.PluginName)
	}

	// Build labels
	labels := []string{
		"type:plugin-run",
		fmt.Sprintf("plugin:%s", record.PluginName),
		fmt.Sprintf("result:%s", record.Result),
	}
	if record.RigName != "" {
		labels = append(labels, fmt.Sprintf("rig:%s", record.RigName))
	}
	labels = append(labels, record.ExtraLabels...)

	// Build bd create command
	args := []string{
		"create",
		"--ephemeral",
		"--json",
		"-t", "chore",
		"--title=" + title,
	}
	for _, label := range labels {
		args = append(args, "-l", label)
	}
	if record.Body != "" {
		args = append(args, "--description="+record.Body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), constants.BdCommandTimeout)
	defer cancel()
	townBeads := beads.ResolveBeadsDir(r.townRoot)
	cmd := beads.CommandContext(ctx, r.townRoot, townBeads, beads.MutationPinned, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("creating plugin run bead: %s: %w", stderr.String(), err)
	}

	// Parse created bead ID from JSON output
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return "", fmt.Errorf("parsing bd create output: %w", err)
	}

	// Close the receipt immediately — it exists for audit/cooldown-gate queries
	// (which use --all to include closed beads) but should not stay open.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), constants.BdCommandTimeout)
	defer closeCancel()
	closeCmd := beads.CommandContext(closeCtx, r.townRoot, townBeads, beads.MutationPinned, "close", result.ID, "--reason", "plugin run recorded")
	_ = closeCmd.Run() // Best-effort — reaper will catch it if this fails

	return result.ID, nil
}

// GetLastRun returns the most recent run for a plugin.
// Returns nil if no runs found.
func (r *Recorder) GetLastRun(pluginName string) (*PluginRunBead, error) {
	runs, err := r.queryRuns(pluginName, 1, "")
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	return runs[0], nil
}

// GetRunsSince returns all runs for a plugin since the given duration.
// Duration format: "1h", "24h", "7d", etc.
func (r *Recorder) GetRunsSince(pluginName string, since string) ([]*PluginRunBead, error) {
	return r.queryRuns(pluginName, 0, since)
}

// queryRuns queries plugin run beads from the ledger.
func (r *Recorder) queryRuns(pluginName string, limit int, since string) ([]*PluginRunBead, error) {
	args := []string{
		"list",
		"--json",
		"--all", // Include closed beads too
		"-l", "type:plugin-run",
		"-l", fmt.Sprintf("plugin:%s", pluginName),
	}
	if limit > 0 {
		args = append(args, fmt.Sprintf("--limit=%d", limit))
	}
	if since != "" {
		// Parse as Go duration and compute an absolute RFC3339 cutoff.
		// bd's compact duration uses "m" for months, but plugin gate
		// durations use Go's time.ParseDuration where "m" means minutes.
		// Passing an absolute timestamp avoids this unit mismatch.
		d, err := time.ParseDuration(since)
		if err != nil {
			return nil, fmt.Errorf("parsing duration %q: %w", since, err)
		}
		cutoff := time.Now().Add(-d).UTC().Format(time.RFC3339)
		args = append(args, "--created-after="+cutoff)
	}
	args = beads.InjectFlatForListJSON(args)

	ctx, cancel := context.WithTimeout(context.Background(), constants.BdCommandTimeout)
	defer cancel()
	cmd := beads.CommandContext(ctx, r.townRoot, beads.ResolveBeadsDir(r.townRoot), beads.ReadOnlyPinned, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Empty result is OK (no runs found)
		if stderr.Len() == 0 || stdout.String() == "[]\n" {
			return nil, nil
		}
		return nil, fmt.Errorf("querying plugin runs: %s: %w", stderr.String(), err)
	}

	// Parse JSON output
	var beads []struct {
		ID        string   `json:"id"`
		Title     string   `json:"title"`
		CreatedAt string   `json:"created_at"`
		Labels    []string `json:"labels"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &beads); err != nil {
		// Empty array is valid
		if stdout.String() == "[]\n" || stdout.Len() == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}

	// Convert to PluginRunBead with parsed result
	runs := make([]*PluginRunBead, 0, len(beads))
	for _, b := range beads {
		run := &PluginRunBead{
			ID:     b.ID,
			Title:  b.Title,
			Labels: b.Labels,
		}

		// Parse created_at
		if t, err := time.Parse(time.RFC3339, b.CreatedAt); err == nil {
			run.CreatedAt = t
		}

		// Extract result from labels
		for _, label := range b.Labels {
			if len(label) > 7 && label[:7] == "result:" {
				run.Result = RunResult(label[7:])
				break
			}
		}

		runs = append(runs, run)
	}

	return runs, nil
}

// CountRunsSince returns the count of runs for a plugin since the given duration.
// This is useful for cooldown gate evaluation.
func (r *Recorder) CountRunsSince(pluginName string, since string) (int, error) {
	runs, err := r.GetRunsSince(pluginName, since)
	if err != nil {
		return 0, err
	}
	return len(runs), nil
}
