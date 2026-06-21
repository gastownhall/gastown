// Package daemon provides the town-level background service for Gas Town.
//
// The daemon is a simple Go process (not a Claude agent) that:
// 1. Pokes agents periodically (heartbeat)
// 2. Processes lifecycle requests (cycle, restart, shutdown)
// 3. Restarts sessions when agents request cycling
//
// The daemon is a "dumb scheduler" - all intelligence is in agents.
package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/atomicfile"
	"github.com/steveyegge/gastown/internal/constants"
)

// Config holds daemon configuration.
type Config struct {
	// HeartbeatInterval is how often to poke agents.
	HeartbeatInterval time.Duration `json:"heartbeat_interval"`

	// TownRoot is the Gas Town workspace root.
	TownRoot string `json:"town_root"`

	// LogFile is the path to the daemon log file.
	LogFile string `json:"log_file"`

	// PidFile is the path to the PID file.
	PidFile string `json:"pid_file"`
}

// DefaultConfig returns the default daemon configuration.
func DefaultConfig(townRoot string) *Config {
	daemonDir := filepath.Join(townRoot, "daemon")
	return &Config{
		HeartbeatInterval: 5 * time.Minute, // Deacon wakes on mail too, no need to poke often
		TownRoot:          townRoot,
		LogFile:           filepath.Join(daemonDir, "daemon.log"),
		PidFile:           filepath.Join(daemonDir, "daemon.pid"),
	}
}

// State represents the daemon's runtime state.
type State struct {
	// Running indicates if the daemon is running.
	Running bool `json:"running"`

	// PID is the process ID of the daemon.
	PID int `json:"pid"`

	// StartedAt is when the daemon started.
	StartedAt time.Time `json:"started_at"`

	// LastHeartbeat is when the last heartbeat completed.
	LastHeartbeat time.Time `json:"last_heartbeat"`

	// HeartbeatCount is how many heartbeats have completed.
	HeartbeatCount int64 `json:"heartbeat_count"`
}

// StateFile returns the path to the state file.
func StateFile(townRoot string) string {
	return filepath.Join(townRoot, "daemon", "state.json")
}

// LoadState loads daemon state from disk.
func LoadState(townRoot string) (*State, error) {
	stateFile := StateFile(townRoot)
	data, err := os.ReadFile(stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			return &State{}, nil
		}
		return nil, err
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// SaveState saves daemon state to disk using atomic write.
func SaveState(townRoot string, state *State) error {
	stateFile := StateFile(townRoot)

	// Ensure daemon directory exists
	if err := os.MkdirAll(filepath.Dir(stateFile), 0755); err != nil {
		return err
	}

	return atomicfile.WriteJSON(stateFile, state)
}

// PatrolConfig holds configuration for a single patrol.
type PatrolConfig struct {
	// Enabled controls whether this patrol runs during heartbeat.
	Enabled bool `json:"enabled"`

	// Interval is how often to run this patrol (not used yet).
	Interval string `json:"interval,omitempty"`

	// Agent is the agent type for this patrol (not used yet).
	Agent string `json:"agent,omitempty"`

	// Rigs limits this patrol to specific rigs. If empty, all rigs are patrolled.
	Rigs []string `json:"rigs,omitempty"`
}

// PatrolsConfig holds configuration for all patrols.
type PatrolsConfig struct {
	Refinery       *PatrolConfig          `json:"refinery,omitempty"`
	Witness        *PatrolConfig          `json:"witness,omitempty"`
	Deacon         *PatrolConfig          `json:"deacon,omitempty"`
	Handler        *PatrolConfig          `json:"handler,omitempty"`
	DoltServer     *DoltServerConfig      `json:"dolt_server,omitempty"`
	DoltRemotes    *DoltRemotesConfig     `json:"dolt_remotes,omitempty"`
	DoltBackup     *DoltBackupConfig      `json:"dolt_backup,omitempty"`
	JsonlGitBackup *JsonlGitBackupConfig  `json:"jsonl_git_backup,omitempty"`
	WispReaper     *WispReaperConfig      `json:"wisp_reaper,omitempty"`
	DoctorDog      *DoctorDogConfig       `json:"doctor_dog,omitempty"`
	CompactorDog           *CompactorDogConfig            `json:"compactor_dog,omitempty"`
	CheckpointDog          *CheckpointDogConfig           `json:"checkpoint_dog,omitempty"`
	ScheduledMaintenance   *ScheduledMaintenanceConfig    `json:"scheduled_maintenance,omitempty"`
	MainBranchTest         *MainBranchTestConfig          `json:"main_branch_test,omitempty"`
	QuotaDog               *QuotaDogConfig                `json:"quota_dog,omitempty"`
	RestartTracker         *RestartTrackerConfig          `json:"restart_tracker,omitempty"`
	PolecatPatrol          *PolecatPatrolConfig           `json:"polecat_patrol,omitempty"`
}

// DoltRemotesConfig holds configuration for the dolt_remotes patrol.
// This patrol periodically pushes Dolt databases to their configured remotes.
type DoltRemotesConfig struct {
	// Enabled controls whether remote push runs.
	Enabled bool `json:"enabled"`

	// Interval is how often to push (default 15m).
	Interval time.Duration `json:"interval,omitempty"`

	// Databases lists specific database names to push.
	// If empty, auto-discovers databases with configured remotes.
	Databases []string `json:"databases,omitempty"`

	// Remote is the remote name to push to (default "origin").
	Remote string `json:"remote,omitempty"`

	// Branch is the branch to push (default "main").
	Branch string `json:"branch,omitempty"`
}

// DoltBackupConfig holds configuration for the dolt_backup patrol.
// This patrol periodically syncs Dolt databases to local filesystem backups.
type DoltBackupConfig struct {
	// Enabled controls whether backup sync runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to sync, as a string (e.g., "15m").
	IntervalStr string `json:"interval,omitempty"`

	// Databases lists specific database names to back up.
	// If empty, auto-discovers databases with configured backup remotes.
	Databases []string `json:"databases,omitempty"`
}

// JsonlGitBackupConfig holds configuration for the jsonl_git_backup patrol.
// This patrol exports issues to JSONL files, scrubs ephemeral data, and pushes to a git repo.
type JsonlGitBackupConfig struct {
	// Enabled controls whether JSONL git backup runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "15m").
	IntervalStr string `json:"interval,omitempty"`

	// Databases lists specific database names to export.
	// If empty, auto-discovers from dolt server.
	Databases []string `json:"databases,omitempty"`

	// GitRepo is the path to the git repository for backup.
	// Default: ~/.dolt-archive/git
	GitRepo string `json:"git_repo,omitempty"`

	// Scrub controls whether ephemeral data is filtered out.
	// Default: true
	Scrub *bool `json:"scrub,omitempty"`

	// SpikeThreshold is the maximum allowed percentage change in record counts
	// between consecutive exports. If the delta exceeds this threshold (in either
	// direction), the export is halted and escalated. Default: 0.20 (20%).
	SpikeThreshold *float64 `json:"spike_threshold,omitempty"`
}

// DaemonPatrolConfig is the structure of mayor/daemon.json.
type DaemonPatrolConfig struct {
	Type      string            `json:"type"`
	Version   int               `json:"version"`
	Heartbeat *PatrolConfig     `json:"heartbeat,omitempty"`
	Patrols   *PatrolsConfig    `json:"patrols,omitempty"`
	// Env holds environment variables to set at startup.
	// Propagated to all sessions spawned by the daemon and read by gt up/mayor attach.
	// Example: {"GT_DOLT_PORT": "43211"}
	Env       map[string]string `json:"env,omitempty"`
}

// PatrolConfigFile returns the path to the patrol config file.
func PatrolConfigFile(townRoot string) string {
	return filepath.Join(townRoot, constants.RoleMayor, "daemon.json")
}

// LoadPatrolConfig loads patrol configuration from mayor/daemon.json.
// Returns nil if the file doesn't exist or can't be parsed.
func LoadPatrolConfig(townRoot string) *DaemonPatrolConfig {
	configFile := PatrolConfigFile(townRoot)
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil
	}

	var config DaemonPatrolConfig
	if err := json.Unmarshal(data, &config); err != nil {
		// Log parse errors to help debug config issues (was previously silent).
		fmt.Fprintf(os.Stderr, "daemon: failed to parse %s: %v\n", configFile, err)
		return nil
	}
	return &config
}

// SavePatrolConfig saves patrol configuration to mayor/daemon.json.
func SavePatrolConfig(townRoot string, config *DaemonPatrolConfig) error {
	configFile := PatrolConfigFile(townRoot)

	// Ensure mayor directory exists
	if err := os.MkdirAll(filepath.Dir(configFile), 0755); err != nil {
		return err
	}

	return atomicfile.WriteJSON(configFile, config)
}

// IsPatrolEnabled checks if a patrol is enabled in the config.
// Returns true if the config doesn't exist (default enabled for backwards compatibility).
// Exception: opt-in patrols (dolt_remotes) default to disabled.
func IsPatrolEnabled(config *DaemonPatrolConfig, patrol string) bool {
	// Opt-in patrols: disabled unless explicitly enabled in config.
	// Must check before the nil-config fallback, otherwise nil config
	// returns true for patrols that should default to disabled.
	if patrol == "dolt_remotes" {
		if config == nil || config.Patrols == nil || config.Patrols.DoltRemotes == nil {
			return false
		}
		return config.Patrols.DoltRemotes.Enabled
	}
	if patrol == "dolt_backup" {
		if config == nil || config.Patrols == nil || config.Patrols.DoltBackup == nil {
			return false
		}
		return config.Patrols.DoltBackup.Enabled
	}
	if patrol == "jsonl_git_backup" {
		if config == nil || config.Patrols == nil || config.Patrols.JsonlGitBackup == nil {
			return false
		}
		return config.Patrols.JsonlGitBackup.Enabled
	}
	if patrol == "wisp_reaper" {
		if config == nil || config.Patrols == nil || config.Patrols.WispReaper == nil {
			return false
		}
		return config.Patrols.WispReaper.Enabled
	}
	if patrol == "doctor_dog" {
		if config == nil || config.Patrols == nil || config.Patrols.DoctorDog == nil {
			return false
		}
		return config.Patrols.DoctorDog.Enabled
	}
	if patrol == "compactor_dog" {
		if config == nil || config.Patrols == nil || config.Patrols.CompactorDog == nil {
			return false
		}
		return config.Patrols.CompactorDog.Enabled
	}
	if patrol == "checkpoint_dog" {
		if config == nil || config.Patrols == nil || config.Patrols.CheckpointDog == nil {
			return false
		}
		return config.Patrols.CheckpointDog.Enabled
	}
	if patrol == "scheduled_maintenance" {
		if config == nil || config.Patrols == nil || config.Patrols.ScheduledMaintenance == nil {
			return false
		}
		return config.Patrols.ScheduledMaintenance.Enabled
	}
	if patrol == "main_branch_test" {
		if config == nil || config.Patrols == nil || config.Patrols.MainBranchTest == nil {
			return false
		}
		return config.Patrols.MainBranchTest.Enabled
	}
	if patrol == "quota_dog" {
		if config == nil || config.Patrols == nil || config.Patrols.QuotaDog == nil {
			return false
		}
		return config.Patrols.QuotaDog.Enabled
	}
	if patrol == "polecat_patrol" {
		// Default ENABLED if unconfigured — this is the Layer 1 deterministic
		// patrol that closes the 8h zombie-detection latency gap. Operators
		// who want to disable it (e.g., headless CI rigs) must set
		// patrols.polecat_patrol.enabled = false explicitly.
		if config == nil || config.Patrols == nil || config.Patrols.PolecatPatrol == nil {
			return true
		}
		return config.Patrols.PolecatPatrol.Enabled
	}

	if config == nil || config.Patrols == nil {
		return true // Default: enabled
	}

	switch patrol {
	case constants.RoleRefinery:
		if config.Patrols.Refinery != nil {
			return config.Patrols.Refinery.Enabled
		}
	case constants.RoleWitness:
		if config.Patrols.Witness != nil {
			return config.Patrols.Witness.Enabled
		}
	case constants.RoleDeacon:
		if config.Patrols.Deacon != nil {
			return config.Patrols.Deacon.Enabled
		}
	case "handler":
		if config.Patrols.Handler != nil {
			return config.Patrols.Handler.Enabled
		}
	}
	return true // Default: enabled
}

// GetPatrolRigs returns the list of rigs for a patrol, or nil if all rigs should be patrolled.
func GetPatrolRigs(config *DaemonPatrolConfig, patrol string) []string {
	if config == nil || config.Patrols == nil {
		return nil // All rigs
	}

	switch patrol {
	case constants.RoleRefinery:
		if config.Patrols.Refinery != nil {
			return config.Patrols.Refinery.Rigs
		}
	case constants.RoleWitness:
		if config.Patrols.Witness != nil {
			return config.Patrols.Witness.Rigs
		}
	}
	return nil // All rigs
}

// loadDisabledPatrolsFromTownSettings loads the disabled_patrols list from
// town settings (settings/config.json) as a set for O(1) lookup.
func loadDisabledPatrolsFromTownSettings(townRoot string) map[string]bool {
	settingsPath := filepath.Join(townRoot, "settings", "config.json")
	data, err := os.ReadFile(settingsPath) //nolint:gosec // G304: path constructed internally
	if err != nil {
		return nil
	}
	var raw struct {
		DisabledPatrols []string `json:"disabled_patrols"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || len(raw.DisabledPatrols) == 0 {
		return nil
	}
	disabled := make(map[string]bool, len(raw.DisabledPatrols))
	for _, p := range raw.DisabledPatrols {
		disabled[p] = true
	}
	return disabled
}

// isPatrolActive checks whether a patrol should run, combining the
// daemon patrol config (mayor/daemon.json) with the town-level
// disabled_patrols list (settings/config.json). A patrol is active
// only if it is enabled in daemon config AND not in the disabled list.
func (d *Daemon) isPatrolActive(patrol string) bool {
	if d.disabledPatrols[patrol] {
		return false
	}
	return IsPatrolEnabled(d.patrolConfig, patrol)
}

// LifecycleAction represents a lifecycle request action.
type LifecycleAction string

const (
	// ActionCycle restarts the session with handoff.
	ActionCycle LifecycleAction = "cycle"

	// ActionRestart does a fresh restart without handoff.
	ActionRestart LifecycleAction = "restart"

	// ActionShutdown terminates without restart.
	ActionShutdown LifecycleAction = "shutdown"
)

// LifecycleRequest represents a request from an agent to the daemon.
type LifecycleRequest struct {
	// From is the agent requesting the action (e.g., "mayor/", "gastown/witness").
	From string `json:"from"`

	// Action is what lifecycle action to perform.
	Action LifecycleAction `json:"action"`

	// Timestamp is when the request was made.
	Timestamp time.Time `json:"timestamp"`
}
