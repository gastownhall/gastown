package daemon

import (
	"strconv"
	"time"

	"github.com/jonbaldie/gastown/internal/constants"
)

// Operational constants — timeouts needed to perform checks.
const (
	defaultDoctorDogInterval = 5 * time.Minute
)

// Default advisory thresholds — used for recommendations in the report.
// These are defaults; override via DoctorDogConfig fields.
const (
	defaultDoctorDogLatencyAlertMs     = 5000.0
	defaultDoctorDogOrphanAlertCount   = 20
	defaultDoctorDogBackupStaleSeconds = 3600.0
)

// DoctorDogConfig holds configuration for the doctor_dog patrol.
type DoctorDogConfig struct {
	// Enabled controls whether the doctor dog runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "5m").
	IntervalStr string `json:"interval,omitempty"`

	// Databases lists the expected production databases.
	// If empty, uses the default set.
	Databases []string `json:"databases,omitempty"`

	// Advisory thresholds — when exceeded, recommendations are added to the report.
	// Agents (Mayor/Deacon) read the report and decide what actions to take.
	// Zero values mean "use default".

	// LatencyAlertMs: latency threshold in ms. Default: 5000 (5s).
	LatencyAlertMs float64 `json:"latency_alert_ms,omitempty"`

	// OrphanAlertCount: database count threshold. Default: 20.
	OrphanAlertCount int `json:"orphan_alert_count,omitempty"`

	// BackupStaleSeconds: backup age threshold in seconds. Default: 3600 (1hr).
	BackupStaleSeconds float64 `json:"backup_stale_seconds,omitempty"`
}

// doctorDogThresholds returns the effective thresholds, using config overrides or defaults.
func doctorDogThresholds(config *DaemonPatrolConfig) (latencyMs float64, orphanCount int, backupStaleSec float64) {
	latencyMs = defaultDoctorDogLatencyAlertMs
	orphanCount = defaultDoctorDogOrphanAlertCount
	backupStaleSec = defaultDoctorDogBackupStaleSeconds

	if config != nil && config.Patrols != nil && config.Patrols.DoctorDog != nil {
		cfg := config.Patrols.DoctorDog
		if cfg.LatencyAlertMs > 0 {
			latencyMs = cfg.LatencyAlertMs
		}
		if cfg.OrphanAlertCount > 0 {
			orphanCount = cfg.OrphanAlertCount
		}
		if cfg.BackupStaleSeconds > 0 {
			backupStaleSec = cfg.BackupStaleSeconds
		}
	}
	return
}

// doctorDogInterval returns the configured interval, or the default (5m).
func doctorDogInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.DoctorDog != nil {
		if config.Patrols.DoctorDog.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.DoctorDog.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultDoctorDogInterval
}

// doctorDogDatabases returns the list of production databases for health checks.
func doctorDogDatabases(config *DaemonPatrolConfig) []string {
	if config != nil && config.Patrols != nil && config.Patrols.DoctorDog != nil {
		if len(config.Patrols.DoctorDog.Databases) > 0 {
			return config.Patrols.DoctorDog.Databases
		}
	}
	return []string{"hq", "gt", "mo"}
}

// runDoctorDog pours a mol-dog-doctor molecule for agent execution.
// The daemon is a thin ticker — it creates the molecule and agents (Deacon)
// execute the formula steps (probe, inspect, report). This follows ZFC:
// daemons schedule, agents decide and act.
func (d *Daemon) runDoctorDog() {
	if !d.isPatrolActive("doctor_dog") {
		return
	}

	d.logger.Printf("doctor_dog: pouring molecule for agent execution")

	port := d.doltServerPort()
	latencyThreshold, orphanCount, backupStaleSec := doctorDogThresholds(d.patrolConfig)

	mol := d.pourDogMolecule(constants.MolDogDoctor, map[string]string{
		"port":              strconv.Itoa(port),
		"latency_threshold": strconv.FormatFloat(latencyThreshold, 'f', 0, 64) + "ms",
		"orphan_threshold":  strconv.Itoa(orphanCount),
		"backup_threshold":  strconv.FormatFloat(backupStaleSec, 'f', 0, 64) + "s",
	})
	defer mol.close()

	if mol.rootID == "" {
		d.logger.Printf("doctor_dog: molecule pour failed (non-fatal), skipping cycle")
		return
	}

	d.logger.Printf("doctor_dog: poured %s → %s", constants.MolDogDoctor, mol.rootID)
}
