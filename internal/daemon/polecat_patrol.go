package daemon

import (
	"context"
	"os/exec"
	"time"
)

// Operational constants for the polecat patrol.
const (
	defaultPolecatPatrolInterval = 5 * time.Minute
	defaultPolecatPatrolTimeout  = 4 * time.Minute
)

// PolecatPatrolConfig holds configuration for the polecat patrol.
//
// This patrol runs `gt patrol scan --rig <X> --notify` for each known rig on
// a deterministic timer. Without it, polecat zombie/dead-session detection
// depends on the witness LLM agent's judgment about when to scan — which on
// m365 was observed to be 8+ hours between scans (glaicier gt-e4b3 evidence).
//
// The scan command itself contains the detection + auto-restart logic
// (internal/witness/handlers.go + internal/cmd/patrol_scan.go). This patrol
// just gives it deterministic cadence.
type PolecatPatrolConfig struct {
	// Enabled controls whether the polecat patrol runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "5m").
	IntervalStr string `json:"interval,omitempty"`

	// TimeoutStr is the per-rig scan timeout (e.g., "4m").
	TimeoutStr string `json:"timeout,omitempty"`

	// Rigs lists rig names to patrol. Empty = patrol all known rigs.
	Rigs []string `json:"rigs,omitempty"`
}

// polecatPatrolInterval returns the configured or default patrol interval.
func polecatPatrolInterval(config *DaemonPatrolConfig) time.Duration {
	if config == nil || config.Patrols == nil || config.Patrols.PolecatPatrol == nil {
		return defaultPolecatPatrolInterval
	}
	if config.Patrols.PolecatPatrol.IntervalStr == "" {
		return defaultPolecatPatrolInterval
	}
	d, err := time.ParseDuration(config.Patrols.PolecatPatrol.IntervalStr)
	if err != nil || d <= 0 {
		return defaultPolecatPatrolInterval
	}
	return d
}

// polecatPatrolTimeout returns the configured or default per-rig scan timeout.
func polecatPatrolTimeout(config *DaemonPatrolConfig) time.Duration {
	if config == nil || config.Patrols == nil || config.Patrols.PolecatPatrol == nil {
		return defaultPolecatPatrolTimeout
	}
	if config.Patrols.PolecatPatrol.TimeoutStr == "" {
		return defaultPolecatPatrolTimeout
	}
	d, err := time.ParseDuration(config.Patrols.PolecatPatrol.TimeoutStr)
	if err != nil || d <= 0 {
		return defaultPolecatPatrolTimeout
	}
	return d
}

// runPolecatPatrol invokes `gt patrol scan --rig <X> --notify` for each
// configured rig sequentially. Errors are logged but do not stop the loop —
// one bad rig must not block the others.
//
// Each rig invocation runs under a context with timeout so a hung scan
// can't wedge the daemon's main loop.
func (d *Daemon) runPolecatPatrol() {
	rigs := d.polecatPatrolRigs()
	if len(rigs) == 0 {
		d.logger.Printf("polecat patrol: no rigs to scan (config empty + no rigs registered)")
		return
	}

	timeout := polecatPatrolTimeout(d.patrolConfig)

	for _, rig := range rigs {
		// New context per rig — one slow scan must not eat into the next rig's budget.
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		cmd := exec.CommandContext(ctx, d.gtPath, "patrol", "scan", "--rig", rig, "--notify")
		out, err := cmd.CombinedOutput()
		cancel()

		if err != nil {
			d.logger.Printf("polecat patrol: rig=%s failed: %v (output: %s)", rig, err, truncate(string(out), 500))
		} else {
			d.logger.Printf("polecat patrol: rig=%s ok (%d bytes output)", rig, len(out))
		}
	}
}

// polecatPatrolRigs returns the list of rigs to scan. Priority:
//   1. Config-supplied list (PolecatPatrolConfig.Rigs) if non-empty
//   2. All known rigs from the daemon's rig registry
//   3. Empty list (no-op patrol)
func (d *Daemon) polecatPatrolRigs() []string {
	if d.patrolConfig != nil && d.patrolConfig.Patrols != nil &&
		d.patrolConfig.Patrols.PolecatPatrol != nil &&
		len(d.patrolConfig.Patrols.PolecatPatrol.Rigs) > 0 {
		return d.patrolConfig.Patrols.PolecatPatrol.Rigs
	}
	return d.getKnownRigs()
}

// truncate clips a string to maxLen with an ellipsis indicator.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...[truncated]"
}
