package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
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
	// (which read closed beads too) but should not stay open.
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

// queryRuns returns a plugin's run receipts, newest first.
//
// Receipts live in two places. RecordRun has always created them with
// --ephemeral, and bd now stores ephemeral beads in the wisps table, which
// `bd list` does not read. Receipts written before that change are in the
// issues table. A reader that asks only `bd list` therefore sees no receipt
// newer than the changeover, finds no recent run, and every cooldown gate reads
// open. Both stores are read here and merged.
//
// Any bead labeled type:plugin-run and plugin:<name> is a receipt, whatever its
// title: RecordRun takes the title from the caller, and plugin scripts record
// their outcome as the title ("compactor-dog: all 15 DBs below threshold").
// "Plugin run: <name>" is only the default.
func (r *Recorder) queryRuns(pluginName string, limit int, since string) ([]*PluginRunBead, error) {
	var cutoff time.Time
	if since != "" {
		// Parse as Go duration and compute an absolute cutoff.
		// bd's compact duration uses "m" for months, but plugin gate
		// durations use Go's time.ParseDuration where "m" means minutes.
		// Passing an absolute timestamp avoids this unit mismatch.
		d, err := time.ParseDuration(since)
		if err != nil {
			return nil, fmt.Errorf("parsing duration %q: %w", since, err)
		}
		cutoff = time.Now().Add(-d).UTC()
	}

	issueRuns, err := r.queryIssueRuns(pluginName, limit, cutoff)
	if err != nil {
		return nil, err
	}
	// A failed wisp read is an error, not an empty result. Reporting "no runs"
	// when the store holding every recent receipt could not be read is how a
	// cooldown gate ends up open; callers decide what an unknown answer means.
	wispRuns, err := r.queryWispRuns(pluginName, limit, cutoff)
	if err != nil {
		return nil, err
	}

	return mergeRuns(issueRuns, wispRuns, limit, cutoff), nil
}

// queryIssueRuns reads receipts from the issues table via `bd list`. These are
// the receipts recorded before bd moved ephemeral beads to the wisps table.
func (r *Recorder) queryIssueRuns(pluginName string, limit int, cutoff time.Time) ([]*PluginRunBead, error) {
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
	if !cutoff.IsZero() {
		args = append(args, "--created-after="+cutoff.Format(time.RFC3339))
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
	var rows []struct {
		ID        string   `json:"id"`
		Title     string   `json:"title"`
		CreatedAt string   `json:"created_at"`
		Labels    []string `json:"labels"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		// Empty array is valid
		if stdout.String() == "[]\n" || stdout.Len() == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}

	runs := make([]*PluginRunBead, 0, len(rows))
	for _, row := range rows {
		runs = append(runs, newPluginRunBead(row.ID, row.Title, row.CreatedAt, row.Labels))
	}
	return runs, nil
}

// queryWispRuns reads receipts from the wisps table. `bd list` cannot enumerate
// wisps, so this goes through `bd sql`, the same route the mail package uses
// for wisp-backed messages.
func (r *Recorder) queryWispRuns(pluginName string, limit int, cutoff time.Time) ([]*PluginRunBead, error) {
	query := "SELECT w.id, w.title, w.created_at, GROUP_CONCAT(DISTINCT al.label) AS labels_csv " +
		"FROM wisps w " +
		"JOIN wisp_labels tl ON tl.issue_id = w.id AND tl.label = 'type:plugin-run' " +
		"JOIN wisp_labels pl ON pl.issue_id = w.id AND pl.label = '" + escapeSQLString("plugin:"+pluginName) + "' " +
		"JOIN wisp_labels al ON al.issue_id = w.id"
	if !cutoff.IsZero() {
		// Managed Dolt servers run in UTC and created_at is a zoneless
		// DATETIME, so the cutoff is compared as a bare UTC timestamp.
		query += " WHERE w.created_at > '" + cutoff.Format(wispSQLTimeLayout) + "'"
	}
	query += " GROUP BY w.id, w.title, w.created_at ORDER BY w.created_at DESC"
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	ctx, cancel := context.WithTimeout(context.Background(), constants.BdCommandTimeout)
	defer cancel()
	cmd := beads.CommandContext(ctx, r.townRoot, beads.ResolveBeadsDir(r.townRoot), beads.ReadOnlyPinned, "sql", "--json", query)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		output := stdout.String() + stderr.String()
		if wispTablesMissing(output) {
			// A database that predates the wisps tables holds every receipt
			// in the issues table, so there is nothing more to read.
			return nil, nil
		}
		return nil, fmt.Errorf("querying plugin run wisps: %s: %w", strings.TrimSpace(output), err)
	}

	var rows []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		CreatedAt string `json:"created_at"`
		LabelsCSV string `json:"labels_csv"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		return nil, fmt.Errorf("parsing bd sql output for plugin run wisps: %w", err)
	}

	runs := make([]*PluginRunBead, 0, len(rows))
	for _, row := range rows {
		var labels []string
		if row.LabelsCSV != "" {
			labels = strings.Split(row.LabelsCSV, ",")
		}
		runs = append(runs, newPluginRunBead(row.ID, row.Title, row.CreatedAt, labels))
	}
	return runs, nil
}

// wispSQLTimeLayout is the zoneless DATETIME form Dolt compares created_at against.
const wispSQLTimeLayout = "2006-01-02 15:04:05"

// wispTablesMissing reports whether a failed `bd sql` says the wisps tables do
// not exist, as opposed to failing for a reason that leaves the answer unknown.
func wispTablesMissing(output string) bool {
	if !strings.Contains(output, "wisp") {
		return false
	}
	return strings.Contains(output, "table not found") || // Dolt
		strings.Contains(output, "no such table") // SQLite
}

// escapeSQLString escapes backslashes and single quotes for Dolt/MySQL SQL string literals.
func escapeSQLString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "'", "''")
}

// newPluginRunBead builds a receipt from the fields both stores return.
func newPluginRunBead(id, title, createdAt string, labels []string) *PluginRunBead {
	run := &PluginRunBead{
		ID:     id,
		Title:  title,
		Labels: labels,
	}
	if t, ok := parseRunTimestamp(createdAt); ok {
		run.CreatedAt = t
	}
	// Extract result from labels
	for _, label := range labels {
		if len(label) > 7 && label[:7] == "result:" {
			run.Result = RunResult(label[7:])
			break
		}
	}
	return run
}

// parseRunTimestamp parses created_at as `bd list` emits it (RFC3339) and as
// `bd sql` may emit a DATETIME column. Bare timestamps are UTC.
func parseRunTimestamp(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02 15:04:05 -0700 MST",
		wispSQLTimeLayout,
	} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// mergeRuns combines receipts from both stores, newest first, without
// duplicates, keeping only those created after cutoff (when set) and at most
// limit of them (when positive).
//
// The cutoff is applied here as well as in each query so the window does not
// depend on a backend honoring the filter it was sent. A receipt whose
// timestamp could not be parsed is kept: the query that returned it already
// placed it inside the window, and dropping it would open the gate.
func mergeRuns(issueRuns, wispRuns []*PluginRunBead, limit int, cutoff time.Time) []*PluginRunBead {
	seen := make(map[string]bool, len(issueRuns)+len(wispRuns))
	runs := make([]*PluginRunBead, 0, len(issueRuns)+len(wispRuns))
	for _, source := range [][]*PluginRunBead{wispRuns, issueRuns} {
		for _, run := range source {
			if seen[run.ID] {
				continue
			}
			if !cutoff.IsZero() && !run.CreatedAt.IsZero() && !run.CreatedAt.After(cutoff) {
				continue
			}
			seen[run.ID] = true
			runs = append(runs, run)
		}
	}
	sort.SliceStable(runs, func(i, j int) bool {
		return runs[i].CreatedAt.After(runs[j].CreatedAt)
	})
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	return runs
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
