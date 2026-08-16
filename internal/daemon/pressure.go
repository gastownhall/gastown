package daemon

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/tmux"
)

// PressureResult holds the outcome of a pressure check.
type PressureResult struct {
	// OK is true if spawning should proceed.
	OK bool

	// Reason describes why spawning was blocked (empty if OK).
	Reason string

	// LoadAvg1 is the 1-minute load average at check time.
	LoadAvg1 float64

	// MemAvailableGB is approximate available memory in GB.
	MemAvailableGB float64

	// ActiveSessions is the count of active Claude agent sessions.
	ActiveSessions int
}

// checkPressure evaluates system load and session concurrency to decide
// whether spawning a new agent session is safe. It checks:
//
//  1. CPU pressure: 1-minute load average vs threshold (per-core).
//  2. Memory pressure: available memory vs minimum threshold.
//  3. Session concurrency: active tmux sessions vs maximum cap.
//
// Infrastructure agents (deacon, witness, mayor) should NOT be gated by
// pressure—they are the monitoring/recovery layer. Only gate:
//   - Polecats (dispatchQueuedWork, crash restarts)
//   - Refineries
//   - Dogs
func (d *Daemon) checkPressure(_ string) PressureResult {
	cfg := d.loadOperationalConfig().GetDaemonConfig()

	cpuThreshold := cfg.PressureCPUThresholdV()
	memThreshold := cfg.PressureMemThresholdGBV()
	maxSessions := cfg.PressureMaxSessionsV()

	// All checks disabled (default) — skip entirely, no subprocess calls.
	if cpuThreshold <= 0 && memThreshold <= 0 && maxSessions <= 0 {
		return PressureResult{OK: true}
	}

	var result PressureResult
	result.OK = true

	// Tier 1: CPU pressure (load average per core)
	if cpuThreshold > 0 {
		result.LoadAvg1 = loadAverage1()
		numCPU := float64(runtime.NumCPU())
		loadPerCore := result.LoadAvg1 / numCPU
		if loadPerCore > cpuThreshold {
			result.OK = false
			result.Reason = fmt.Sprintf("cpu pressure: load/core %.2f exceeds threshold %.2f (load=%.1f, cores=%d)", loadPerCore, cpuThreshold, result.LoadAvg1, int(numCPU))
			return result
		}
	}

	// Tier 1: Memory pressure
	if memThreshold > 0 {
		result.MemAvailableGB = availableMemoryGB()
		if result.MemAvailableGB > 0 && result.MemAvailableGB < memThreshold {
			result.OK = false
			result.Reason = fmt.Sprintf("memory pressure: %.1fGB available, minimum %.1fGB", result.MemAvailableGB, memThreshold)
			return result
		}
	}

	// Tier 2: Session concurrency cap
	if maxSessions > 0 {
		result.ActiveSessions = d.countAgentSessions()
		if result.ActiveSessions >= maxSessions {
			result.OK = false
			result.Reason = fmt.Sprintf("session cap: %d active sessions, max %d", result.ActiveSessions, maxSessions)
			return result
		}
	}

	return result
}

// countAgentSessions counts active tmux sessions that belong to Gas Town agents.
// Uses the town's tmux socket so it only counts sessions for this town.
func (d *Daemon) countAgentSessions() int {
	t := tmux.NewTmux()
	sessions, err := t.ListSessions()
	if err != nil {
		return 0
	}

	count := 0
	for _, name := range sessions {
		if isAgentSession(name) {
			count++
		}
	}
	return count
}

// isAgentSession returns true if the tmux session name looks like a Gas Town agent.
// Agent sessions use prefixed names (e.g., "hq-mayor", "rig-witness", "rig-polecat-foo").
func isAgentSession(name string) bool {
	// Agent sessions contain role markers
	for _, marker := range []string{
		constants.RoleMayor,
		constants.RoleWitness,
		constants.RoleRefinery,
		constants.RolePolecat,
		constants.RoleDeacon,
		constants.RoleCrew,
		"boot",
		"dog",
	} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

// loadAverage1 returns the 1-minute load average.
// Falls back to 0 if unavailable (effectively disabling the check).
func loadAverage1() float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		// macOS: use sysctl
		return loadAverage1Sysctl()
	}
	var load1 float64
	_, _ = fmt.Sscanf(string(data), "%f", &load1)
	return load1
}
