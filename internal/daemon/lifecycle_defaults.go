package daemon

// DefaultLifecycleConfig returns a DaemonPatrolConfig with sensible defaults
// for the six-stage Dolt lifecycle (CREATE → LIVE → CLOSE → DECAY → COMPACT → FLATTEN).
//
// All patrols are enabled with conservative intervals:
//   - Wisp Reaper (DECAY): every 30m, delete closed wisps after 7d
//   - Compactor Dog (COMPACT): every 24h, threshold 2000 commits
//   - Checkpoint Dog: every 10m, auto-commit dirty polecat worktrees
//   - Doctor Dog (health): every 5m
//   - JSONL Git Backup: every 15m
//   - Dolt Filesystem Backup: every 15m
//   - Scheduled Maintenance (FLATTEN): daily at 03:00, threshold 1000
//   - Main Branch Test: every 30m, 10m timeout per rig
func DefaultLifecycleConfig() *DaemonPatrolConfig {
	threshold := 1000
	scrub := true
	return &DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:      true,
				IntervalStr:  "30m",
				MaxAgeStr:    "24h",
				DeleteAgeStr: "24h", // closed wisps deleted after 24h; live wisps table is scanned by mail/search, so keep it small (gt-ye21). Dolt AS OF retains history.
			},
			CompactorDog: &CompactorDogConfig{
				Enabled:     true,
				IntervalStr: "24h",
				Threshold:   defaultCompactorCommitThreshold,
			},
			CheckpointDog: &CheckpointDogConfig{
				Enabled:     true,
				IntervalStr: "10m",
			},
			DoctorDog: &DoctorDogConfig{
				Enabled:     true,
				IntervalStr: "5m",
			},
			JsonlGitBackup: &JsonlGitBackupConfig{
				Enabled:     true,
				IntervalStr: "15m",
				Scrub:       &scrub,
			},
			DoltBackup: &DoltBackupConfig{
				Enabled:     true,
				IntervalStr: "15m",
			},
			ScheduledMaintenance: &ScheduledMaintenanceConfig{
				Enabled:   true,
				Window:    "03:00",
				Interval:  "daily",
				Threshold: &threshold,
			},
			MainBranchTest: &MainBranchTestConfig{
				Enabled:     true,
				IntervalStr: "30m",
				TimeoutStr:  "10m",
			},
			Handler: &PatrolConfig{
				Enabled: true,
			},
		},
	}
}

// EnsureLifecycleDefaults populates missing patrol configuration with sensible
// defaults. It never overwrites existing user configuration — only fills in
// patrols that are nil (not yet configured).
//
// Returns true if any defaults were applied (caller should persist the config).
func EnsureLifecycleDefaults(config *DaemonPatrolConfig) bool {
	if config == nil {
		return false
	}

	defaults := DefaultLifecycleConfig()
	changed := false

	if config.Patrols == nil {
		config.Patrols = defaults.Patrols
		return true
	}

	p := config.Patrols
	d := defaults.Patrols

	if p.WispReaper == nil {
		p.WispReaper = d.WispReaper
		changed = true
	}
	if p.CompactorDog == nil {
		p.CompactorDog = d.CompactorDog
		changed = true
	}
	if p.CheckpointDog == nil {
		p.CheckpointDog = d.CheckpointDog
		changed = true
	}
	if p.DoctorDog == nil {
		p.DoctorDog = d.DoctorDog
		changed = true
	}
	if p.JsonlGitBackup == nil {
		p.JsonlGitBackup = d.JsonlGitBackup
		changed = true
	}
	if p.DoltBackup == nil {
		p.DoltBackup = d.DoltBackup
		changed = true
	}
	if p.ScheduledMaintenance == nil {
		p.ScheduledMaintenance = d.ScheduledMaintenance
		changed = true
	}
	if p.MainBranchTest == nil {
		p.MainBranchTest = d.MainBranchTest
		changed = true
	}
	if p.Handler == nil {
		p.Handler = d.Handler
		changed = true
	}

	return changed
}

// EnsureLifecycleConfigFile loads the patrol config from disk (or creates a new
// one if it doesn't exist), applies lifecycle defaults for any unconfigured
// patrols, and saves the result. Returns nil on success.
//
// This is the top-level function called by gt init and gt up.
func EnsureLifecycleConfigFile(townRoot string) error {
	config := LoadPatrolConfig(townRoot)
	if config == nil {
		config = DefaultLifecycleConfig()
		return SavePatrolConfig(townRoot, config)
	}

	if EnsureLifecycleDefaults(config) {
		return SavePatrolConfig(townRoot, config)
	}

	return nil // Already configured, nothing to do
}
