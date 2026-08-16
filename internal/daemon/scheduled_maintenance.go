package daemon

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/util"
)

const (
	// defaultMaintenanceCheckInterval is how often the daemon checks if it's
	// within the maintenance window. Short interval (5 min) ensures we don't
	// miss a narrow window, but the actual maintenance only runs once per window.
	defaultMaintenanceCheckInterval = 5 * time.Minute

	// defaultMaintenanceThreshold is the minimum commit count before maintenance
	// triggers. Lower than compactor_dog (10k) since this is user-configured
	// scheduled maintenance, not emergency compaction.
	defaultMaintenanceThreshold = 1000
)

// ScheduledMaintenanceConfig holds configuration for the scheduled_maintenance patrol.
// User opts in via:
//
//	gt config set maintenance.window 03:00
//	gt config set maintenance.interval daily
//
// The daemon checks commit counts per DB during the window and runs
// `gt maintain --force` when any DB exceeds the threshold.
type ScheduledMaintenanceConfig struct {
	// Enabled controls whether scheduled maintenance runs.
	Enabled bool `json:"enabled"`

	// Window is the time of day to start maintenance (e.g., "03:00").
	// Uses 24-hour format HH:MM in local time.
	Window string `json:"window,omitempty"`

	// Interval controls how often maintenance runs.
	// Supported values: "daily", "weekly", "monthly", or a Go duration (e.g., "48h").
	// Default: "daily".
	Interval string `json:"interval,omitempty"`

	// Threshold is the minimum commit count before maintenance triggers.
	// Default: 1000.
	Threshold *int `json:"threshold,omitempty"`
}

// maintenanceCheckInterval returns the configured check interval, or the default (5m).
func maintenanceCheckInterval(config *DaemonPatrolConfig) time.Duration {
	// The check interval is not user-configurable — it's internal.
	// We just need to poll often enough to catch the window.
	return defaultMaintenanceCheckInterval
}

// maintenanceThreshold returns the configured commit threshold, or the default (1000).
func maintenanceThreshold(config *DaemonPatrolConfig) int {
	if config != nil && config.Patrols != nil && config.Patrols.ScheduledMaintenance != nil {
		if config.Patrols.ScheduledMaintenance.Threshold != nil {
			return *config.Patrols.ScheduledMaintenance.Threshold
		}
	}
	return defaultMaintenanceThreshold
}

// maintenanceWindow returns the configured window start time (HH:MM), or empty string.
func maintenanceWindow(config *DaemonPatrolConfig) string {
	if config != nil && config.Patrols != nil && config.Patrols.ScheduledMaintenance != nil {
		return config.Patrols.ScheduledMaintenance.Window
	}
	return ""
}

// maintenanceInterval returns the configured interval string, or "daily".
func maintenanceInterval(config *DaemonPatrolConfig) string {
	if config != nil && config.Patrols != nil && config.Patrols.ScheduledMaintenance != nil {
		if config.Patrols.ScheduledMaintenance.Interval != "" {
			return config.Patrols.ScheduledMaintenance.Interval
		}
	}
	return "daily"
}

// parseWindowTime parses an HH:MM string and returns the hour and minute.
func parseWindowTime(window string) (hour, minute int, err error) {
	parts := strings.SplitN(window, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid window format %q: expected HH:MM", window)
	}
	hour, err = strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("invalid hour in window %q: expected 0-23", window)
	}
	minute, err = strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("invalid minute in window %q: expected 0-59", window)
	}
	return hour, minute, nil
}

// isInMaintenanceWindow checks if the given time falls within the maintenance window.
// The window is 1 hour starting at the configured HH:MM.
func isInMaintenanceWindow(now time.Time, window string) bool {
	hour, minute, err := parseWindowTime(window)
	if err != nil {
		return false
	}

	windowStart := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	windowEnd := windowStart.Add(1 * time.Hour)

	return !now.Before(windowStart) && now.Before(windowEnd)
}

// shouldRunMaintenance checks if maintenance should run based on the interval
// and the last run time. Returns true if enough time has passed since the last run.
func shouldRunMaintenance(now time.Time, lastRun time.Time, interval string) bool {
	if lastRun.IsZero() {
		return true // Never run before
	}

	var minGap time.Duration
	switch interval {
	case "daily":
		minGap = 20 * time.Hour // Slightly less than 24h to avoid drift
	case "weekly":
		minGap = 6 * 24 * time.Hour
	case "monthly":
		minGap = 27 * 24 * time.Hour
	default:
		// Try parsing as Go duration
		d, err := time.ParseDuration(interval)
		if err != nil || d <= 0 {
			minGap = 20 * time.Hour // Fall back to daily
		} else {
			minGap = d - (d / 10) // 90% of configured interval to avoid drift
		}
	}

	return now.Sub(lastRun) >= minGap
}

// runScheduledMaintenance checks if we're in the maintenance window and
// if any database exceeds the commit threshold, runs `gt maintain --force`.
func (d *Daemon) runScheduledMaintenance() {
	if !d.isPatrolActive("scheduled_maintenance") {
		return
	}

	window := maintenanceWindow(d.patrolConfig)
	if window == "" {
		d.logger.Printf("scheduled_maintenance: no window configured, skipping")
		return
	}

	now := time.Now()

	// Check if we're in the maintenance window.
	if !isInMaintenanceWindow(now, window) {
		return // Not in window — silent skip (this fires every 5 minutes)
	}

	// Check if we already ran recently (respect interval).
	interval := maintenanceInterval(d.patrolConfig)
	if !shouldRunMaintenance(now, d.lastMaintenanceRun, interval) {
		return // Already ran this window
	}

	d.logger.Printf("scheduled_maintenance: in window %s, checking commit counts", window)

	// Check if any database exceeds the threshold.
	threshold := maintenanceThreshold(d.patrolConfig)
	databases := d.compactorDatabases() // Reuse the same DB discovery
	if len(databases) == 0 {
		d.logger.Printf("scheduled_maintenance: no databases found")
		return
	}

	needsMaintenance := false
	for _, dbName := range databases {
		commitCount, err := d.compactorCountCommits(dbName)
		if err != nil {
			d.logger.Printf("scheduled_maintenance: %s: error counting commits: %v", dbName, err)
			continue
		}
		if commitCount >= threshold {
			d.logger.Printf("scheduled_maintenance: %s: %d commits >= threshold %d — maintenance needed",
				dbName, commitCount, threshold)
			needsMaintenance = true
			break
		}
		d.logger.Printf("scheduled_maintenance: %s: %d commits (below threshold %d)",
			dbName, commitCount, threshold)
	}

	if !needsMaintenance {
		d.logger.Printf("scheduled_maintenance: all databases below threshold, skipping")
		d.lastMaintenanceRun = now // Don't re-check until next interval
		return
	}

	// Run gt maintain --force --threshold <threshold>
	d.logger.Printf("scheduled_maintenance: running gt maintain --force --threshold %d", threshold)

	cmd := exec.CommandContext(d.ctx, d.gtPath, "maintain", "--force",
		"--threshold", strconv.Itoa(threshold))
	cmd.Dir = d.config.TownRoot
	util.SetDetachedProcessGroup(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		d.logger.Printf("scheduled_maintenance: gt maintain failed: %v\nOutput: %s", err, string(output))
		d.escalate("scheduled_maintenance", fmt.Sprintf("gt maintain --force failed: %v", err))
	} else {
		d.logger.Printf("scheduled_maintenance: gt maintain completed successfully")
		if len(output) > 0 {
			// Log last few lines of output
			lines := strings.Split(strings.TrimSpace(string(output)), "\n")
			tail := lines
			if len(tail) > 5 {
				tail = tail[len(tail)-5:]
			}
			for _, line := range tail {
				d.logger.Printf("scheduled_maintenance: %s", line)
			}
		}
	}

	d.lastMaintenanceRun = now
}
