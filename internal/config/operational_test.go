package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestSessionThresholds_Defaults(t *testing.T) {
	t.Parallel()

	// Nil OperationalConfig should return defaults
	var op *OperationalConfig
	session := op.GetSessionConfig()

	if got := session.ClaudeStartTimeoutD(); got != DefaultClaudeStartTimeout {
		t.Errorf("ClaudeStartTimeout: got %v, want %v", got, DefaultClaudeStartTimeout)
	}
	if got := session.GUPPViolationTimeoutD(); got != DefaultGUPPViolationTimeout {
		t.Errorf("GUPPViolationTimeout: got %v, want %v", got, DefaultGUPPViolationTimeout)
	}
	if got := session.HungSessionThresholdD(); got != DefaultHungSessionThreshold {
		t.Errorf("HungSessionThreshold: got %v, want %v", got, DefaultHungSessionThreshold)
	}
	if got := session.StartupNudgeMaxRetriesV(); got != DefaultStartupNudgeMaxRetries {
		t.Errorf("StartupNudgeMaxRetries: got %v, want %v", got, DefaultStartupNudgeMaxRetries)
	}
}

func TestSessionThresholds_Overrides(t *testing.T) {
	t.Parallel()

	retries := 5
	op := &OperationalConfig{
		Session: &SessionThresholds{
			ClaudeStartTimeout:     "120s",
			GUPPViolationTimeout:   "1h",
			HungSessionThreshold:   "45m",
			StartupNudgeMaxRetries: &retries,
		},
	}

	session := op.GetSessionConfig()
	if got := session.ClaudeStartTimeoutD(); got != 120*time.Second {
		t.Errorf("ClaudeStartTimeout: got %v, want 120s", got)
	}
	if got := session.GUPPViolationTimeoutD(); got != time.Hour {
		t.Errorf("GUPPViolationTimeout: got %v, want 1h", got)
	}
	if got := session.HungSessionThresholdD(); got != 45*time.Minute {
		t.Errorf("HungSessionThreshold: got %v, want 45m", got)
	}
	if got := session.StartupNudgeMaxRetriesV(); got != 5 {
		t.Errorf("StartupNudgeMaxRetries: got %v, want 5", got)
	}
}

func TestSessionThresholds_InvalidDuration(t *testing.T) {
	t.Parallel()

	op := &OperationalConfig{
		Session: &SessionThresholds{
			ClaudeStartTimeout: "not-a-duration",
		},
	}

	session := op.GetSessionConfig()
	if got := session.ClaudeStartTimeoutD(); got != DefaultClaudeStartTimeout {
		t.Errorf("invalid duration should fallback to default: got %v, want %v", got, DefaultClaudeStartTimeout)
	}
}

func TestNudgeThresholds_Defaults(t *testing.T) {
	t.Parallel()

	op := &OperationalConfig{}
	nudge := op.GetNudgeConfig()

	if got := nudge.ReadyTimeoutD(); got != DefaultNudgeReadyTimeout {
		t.Errorf("ReadyTimeout: got %v, want %v", got, DefaultNudgeReadyTimeout)
	}
	if got := nudge.MaxQueueDepthV(); got != DefaultNudgeMaxQueueDepth {
		t.Errorf("MaxQueueDepth: got %v, want %v", got, DefaultNudgeMaxQueueDepth)
	}
	if got := nudge.StaleClaimThresholdD(); got != DefaultNudgeStaleClaimTimeout {
		t.Errorf("StaleClaimThreshold: got %v, want %v", got, DefaultNudgeStaleClaimTimeout)
	}
}

func TestDaemonThresholds_Defaults(t *testing.T) {
	t.Parallel()

	op := &OperationalConfig{}
	daemon := op.GetDaemonConfig()

	if got := daemon.DogIdleSessionTimeoutD(); got != DefaultDogIdleSessionTimeout {
		t.Errorf("DogIdleSessionTimeout: got %v, want %v", got, DefaultDogIdleSessionTimeout)
	}
	if got := daemon.MaxDogPoolSizeV(); got != DefaultMaxDogPoolSize {
		t.Errorf("MaxDogPoolSize: got %v, want %v", got, DefaultMaxDogPoolSize)
	}
	if got := daemon.MaxLifecycleMessageAgeD(); got != DefaultMaxLifecycleMessageAge {
		t.Errorf("MaxLifecycleMessageAge: got %v, want %v", got, DefaultMaxLifecycleMessageAge)
	}
	if got := daemon.RecoveryHeartbeatIntervalD(); got != DefaultRecoveryHeartbeatInterval {
		t.Errorf("RecoveryHeartbeatInterval: got %v, want %v", got, DefaultRecoveryHeartbeatInterval)
	}
	if got := daemon.BootSpawnCooldownD(); got != DefaultBootSpawnCooldown {
		t.Errorf("BootSpawnCooldown: got %v, want %v", got, DefaultBootSpawnCooldown)
	}
	if got := daemon.DeaconGracePeriodD(); got != DefaultDeaconGracePeriod {
		t.Errorf("DeaconGracePeriod: got %v, want %v", got, DefaultDeaconGracePeriod)
	}
}

func TestDaemonThresholds_Overrides(t *testing.T) {
	t.Parallel()

	poolSize := 8
	op := &OperationalConfig{
		Daemon: &DaemonThresholds{
			DogIdleSessionTimeout: "2h",
			MaxDogPoolSize:        &poolSize,
		},
	}

	daemon := op.GetDaemonConfig()
	if got := daemon.DogIdleSessionTimeoutD(); got != 2*time.Hour {
		t.Errorf("DogIdleSessionTimeout: got %v, want 2h", got)
	}
	if got := daemon.MaxDogPoolSizeV(); got != 8 {
		t.Errorf("MaxDogPoolSize: got %v, want 8", got)
	}
	// Non-overridden fields should still return defaults
	if got := daemon.MassDeathThresholdV(); got != DefaultMassDeathThreshold {
		t.Errorf("MassDeathThreshold: got %v, want %v (default)", got, DefaultMassDeathThreshold)
	}
}

func TestDaemonThresholds_NewFieldOverrides(t *testing.T) {
	t.Parallel()

	op := &OperationalConfig{
		Daemon: &DaemonThresholds{
			RecoveryHeartbeatInterval: "5m",
			BootSpawnCooldown:         "90s",
			DeaconGracePeriod:         "10m",
		},
	}

	daemon := op.GetDaemonConfig()
	if got := daemon.RecoveryHeartbeatIntervalD(); got != 5*time.Minute {
		t.Errorf("RecoveryHeartbeatInterval: got %v, want 5m", got)
	}
	if got := daemon.BootSpawnCooldownD(); got != 90*time.Second {
		t.Errorf("BootSpawnCooldown: got %v, want 90s", got)
	}
	if got := daemon.DeaconGracePeriodD(); got != 10*time.Minute {
		t.Errorf("DeaconGracePeriod: got %v, want 10m", got)
	}
}

func TestDeaconThresholds_Defaults(t *testing.T) {
	t.Parallel()

	op := &OperationalConfig{}
	deacon := op.GetDeaconConfig()

	if got := deacon.PingTimeoutD(); got != DefaultDeaconPingTimeout {
		t.Errorf("PingTimeout: got %v, want %v", got, DefaultDeaconPingTimeout)
	}
	if got := deacon.ConsecutiveFailuresV(); got != DefaultDeaconConsecutiveFailures {
		t.Errorf("ConsecutiveFailures: got %v, want %v", got, DefaultDeaconConsecutiveFailures)
	}
}

func TestPolecatThresholds_Defaults(t *testing.T) {
	t.Parallel()

	op := &OperationalConfig{}
	polecat := op.GetPolecatConfig()

	if got := polecat.HeartbeatStaleThresholdD(); got != DefaultPolecatHeartbeatStale {
		t.Errorf("HeartbeatStale: got %v, want %v", got, DefaultPolecatHeartbeatStale)
	}
	if got := polecat.DoltMaxRetriesV(); got != DefaultPolecatDoltMaxRetries {
		t.Errorf("DoltMaxRetries: got %v, want %v", got, DefaultPolecatDoltMaxRetries)
	}
}

func TestDoltThresholds_Defaults(t *testing.T) {
	t.Parallel()

	op := &OperationalConfig{}
	dolt := op.GetDoltConfig()

	if got := dolt.HealthCheckIntervalD(); got != DefaultDoltHealthCheckInterval {
		t.Errorf("HealthCheckInterval: got %v, want %v", got, DefaultDoltHealthCheckInterval)
	}
	if got := dolt.SlowQueryThresholdD(); got != DefaultDoltSlowQueryThreshold {
		t.Errorf("SlowQueryThreshold: got %v, want %v", got, DefaultDoltSlowQueryThreshold)
	}
}

func TestLoadOperationalConfig_NonexistentDir(t *testing.T) {
	t.Parallel()

	op := LoadOperationalConfig("/nonexistent/town/root")
	// Should return valid empty config, not nil
	if op == nil {
		t.Fatal("LoadOperationalConfig should never return nil")
	}
	// Defaults should work
	if got := op.GetSessionConfig().GUPPViolationTimeoutD(); got != DefaultGUPPViolationTimeout {
		t.Errorf("expected default GUPP timeout, got %v", got)
	}
}

func TestLoadOperationalConfig_WithConfig(t *testing.T) {
	t.Parallel()

	// Create temp town root with settings/config.json
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatal(err)
	}

	retries := 7
	settings := TownSettings{
		Type:    "town-settings",
		Version: 1,
		Operational: &OperationalConfig{
			Session: &SessionThresholds{
				GUPPViolationTimeout:   "45m",
				StartupNudgeMaxRetries: &retries,
			},
			Daemon: &DaemonThresholds{
				DogIdleSessionTimeout: "3h",
			},
		},
	}

	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	op := LoadOperationalConfig(dir)

	if got := op.GetSessionConfig().GUPPViolationTimeoutD(); got != 45*time.Minute {
		t.Errorf("GUPPViolationTimeout: got %v, want 45m", got)
	}
	if got := op.GetSessionConfig().StartupNudgeMaxRetriesV(); got != 7 {
		t.Errorf("StartupNudgeMaxRetries: got %v, want 7", got)
	}
	if got := op.GetDaemonConfig().DogIdleSessionTimeoutD(); got != 3*time.Hour {
		t.Errorf("DogIdleSessionTimeout: got %v, want 3h", got)
	}
	// Non-overridden subsystems should return defaults
	if got := op.GetNudgeConfig().MaxQueueDepthV(); got != DefaultNudgeMaxQueueDepth {
		t.Errorf("MaxQueueDepth: got %v, want %v (default)", got, DefaultNudgeMaxQueueDepth)
	}
}

func TestMailThresholds_Defaults(t *testing.T) {
	t.Parallel()

	op := &OperationalConfig{}
	mail := op.GetMailConfig()

	if got := mail.IdleNotifyTimeoutD(); got != DefaultMailIdleNotifyTimeout {
		t.Errorf("IdleNotifyTimeout: got %v, want %v", got, DefaultMailIdleNotifyTimeout)
	}
	if got := mail.BdReadTimeoutD(); got != DefaultMailBdReadTimeout {
		t.Errorf("BdReadTimeout: got %v, want %v", got, DefaultMailBdReadTimeout)
	}
	if got := mail.MaxConcurrentAckOpsV(); got != DefaultMailMaxConcurrentAcks {
		t.Errorf("MaxConcurrentAckOps: got %v, want %v", got, DefaultMailMaxConcurrentAcks)
	}
	if got := mail.ReplyReminderDelayD(); got != DefaultMailReplyReminderDelay {
		t.Errorf("ReplyReminderDelay: got %v, want %v", got, DefaultMailReplyReminderDelay)
	}
}

func TestMailThresholds_ReplyReminderDelayOverride(t *testing.T) {
	t.Parallel()

	op := &OperationalConfig{
		Mail: &MailThresholds{
			ReplyReminderDelay: "1m",
		},
	}
	if got := op.GetMailConfig().ReplyReminderDelayD(); got != time.Minute {
		t.Errorf("ReplyReminderDelay override: got %v, want 1m", got)
	}
}

func TestMailThresholds_ReplyReminderDelayDisabled(t *testing.T) {
	t.Parallel()

	// "0s" disables reply reminders.
	op := &OperationalConfig{
		Mail: &MailThresholds{
			ReplyReminderDelay: "0s",
		},
	}
	if got := op.GetMailConfig().ReplyReminderDelayD(); got != 0 {
		t.Errorf("ReplyReminderDelay disabled: got %v, want 0", got)
	}
}

func TestWebThresholds_Overrides(t *testing.T) {
	t.Parallel()

	maxCmds := 20
	maxSubject := 1000
	op := &OperationalConfig{
		Web: &WebThresholds{
			MaxConcurrentCommands: &maxCmds,
			MaxSubjectLen:         &maxSubject,
		},
	}

	web := op.GetWebConfig()
	if got := web.MaxConcurrentCommandsV(); got != 20 {
		t.Errorf("MaxConcurrentCommands: got %v, want 20", got)
	}
	if got := web.MaxSubjectLenV(); got != 1000 {
		t.Errorf("MaxSubjectLen: got %v, want 1000", got)
	}
	// Non-overridden field
	if got := web.MaxBodyLenV(); got != DefaultWebMaxBodyLen {
		t.Errorf("MaxBodyLen: got %v, want %v (default)", got, DefaultWebMaxBodyLen)
	}
}

func TestWitnessThresholds_Defaults(t *testing.T) {
	t.Parallel()

	var op *OperationalConfig
	wit := op.GetWitnessConfig()

	if got := wit.StartupStallThresholdD(); got != DefaultWitnessStartupStallThreshold {
		t.Errorf("StartupStallThreshold: got %v, want %v", got, DefaultWitnessStartupStallThreshold)
	}
	if got := wit.StartupActivityGraceD(); got != DefaultWitnessStartupActivityGrace {
		t.Errorf("StartupActivityGrace: got %v, want %v", got, DefaultWitnessStartupActivityGrace)
	}
	if got := wit.MaxBeadRespawnsV(); got != DefaultWitnessMaxBeadRespawns {
		t.Errorf("MaxBeadRespawns: got %v, want %v", got, DefaultWitnessMaxBeadRespawns)
	}
	if got := wit.DoneIntentStuckTimeoutD(); got != DefaultWitnessDoneIntentStuckTimeout {
		t.Errorf("DoneIntentStuckTimeout: got %v, want %v", got, DefaultWitnessDoneIntentStuckTimeout)
	}
	if got := wit.DoneIntentRecentGraceD(); got != DefaultWitnessDoneIntentRecentGrace {
		t.Errorf("DoneIntentRecentGrace: got %v, want %v", got, DefaultWitnessDoneIntentRecentGrace)
	}
}

func TestWitnessThresholds_Overrides(t *testing.T) {
	t.Parallel()

	maxRespawns := 5
	op := &OperationalConfig{
		Witness: &WitnessThresholds{
			StartupStallThreshold:  "2m",
			StartupActivityGrace:   "45s",
			MaxBeadRespawns:        &maxRespawns,
			DoneIntentStuckTimeout: "90s",
			DoneIntentRecentGrace:  "15s",
		},
	}

	wit := op.GetWitnessConfig()
	if got := wit.StartupStallThresholdD(); got != 2*time.Minute {
		t.Errorf("StartupStallThreshold: got %v, want 2m", got)
	}
	if got := wit.StartupActivityGraceD(); got != 45*time.Second {
		t.Errorf("StartupActivityGrace: got %v, want 45s", got)
	}
	if got := wit.MaxBeadRespawnsV(); got != 5 {
		t.Errorf("MaxBeadRespawns: got %v, want 5", got)
	}
	if got := wit.DoneIntentStuckTimeoutD(); got != 90*time.Second {
		t.Errorf("DoneIntentStuckTimeout: got %v, want 90s", got)
	}
	if got := wit.DoneIntentRecentGraceD(); got != 15*time.Second {
		t.Errorf("DoneIntentRecentGrace: got %v, want 15s", got)
	}
}

func TestPressureThresholds_Defaults(t *testing.T) {
	t.Parallel()

	var op *OperationalConfig
	dt := op.GetDaemonConfig()

	if got := dt.PressureCPUThresholdV(); got != DefaultPressureCPUThreshold {
		t.Errorf("PressureCPUThreshold: got %v, want %v", got, DefaultPressureCPUThreshold)
	}
	if got := dt.PressureMemThresholdGBV(); got != DefaultPressureMemThresholdGB {
		t.Errorf("PressureMemThresholdGB: got %v, want %v", got, DefaultPressureMemThresholdGB)
	}
	wantSessions := runtime.NumCPU() * 2
	if wantSessions < 4 {
		wantSessions = 4
	}
	if got := dt.PressureMaxSessionsV(); got != wantSessions {
		t.Errorf("PressureMaxSessions: got %v, want %v", got, wantSessions)
	}
}

func TestPressureThresholds_Overrides(t *testing.T) {
	t.Parallel()

	cpu := 0.5
	mem := 8.0
	sessions := 10
	dt := &DaemonThresholds{
		PressureCPUThreshold:   &cpu,
		PressureMemThresholdGB: &mem,
		PressureMaxSessions:    &sessions,
	}
	if got := dt.PressureCPUThresholdV(); got != 0.5 {
		t.Errorf("PressureCPUThreshold: got %v, want 0.5", got)
	}
	if got := dt.PressureMemThresholdGBV(); got != 8.0 {
		t.Errorf("PressureMemThresholdGB: got %v, want 8.0", got)
	}
	if got := dt.PressureMaxSessionsV(); got != 10 {
		t.Errorf("PressureMaxSessions: got %v, want 10", got)
	}
}

func TestPressureThresholds_DisabledWithZero(t *testing.T) {
	t.Parallel()

	zero := 0.0
	zeroInt := 0
	dt := &DaemonThresholds{
		PressureCPUThreshold:   &zero,
		PressureMemThresholdGB: &zero,
		PressureMaxSessions:    &zeroInt,
	}
	if dt.PressureCPUThresholdV() != 0 {
		t.Error("zero CPU threshold should disable check")
	}
	if dt.PressureMemThresholdGBV() != 0 {
		t.Error("zero mem threshold should disable check")
	}
	if dt.PressureMaxSessionsV() != 0 {
		t.Error("zero max sessions should mean unlimited")
	}
}

func TestPressureThresholds_NilReceiver(t *testing.T) {
	t.Parallel()

	var dt *DaemonThresholds
	if dt.PressureCPUThresholdV() != DefaultPressureCPUThreshold {
		t.Error("nil DaemonThresholds should return default CPU threshold")
	}
	if dt.PressureMemThresholdGBV() != DefaultPressureMemThresholdGB {
		t.Error("nil DaemonThresholds should return default mem threshold")
	}
	wantSessions := runtime.NumCPU() * 2
	if wantSessions < 4 {
		wantSessions = 4
	}
	if dt.PressureMaxSessionsV() != wantSessions {
		t.Error("nil DaemonThresholds should return default max sessions")
	}
}

func TestPressureThresholds_JSON(t *testing.T) {
	t.Parallel()

	jsonData := `{
		"daemon": {
			"pressure_cpu_threshold": 0.7,
			"pressure_mem_threshold_gb": 4.0,
			"pressure_max_sessions": 8
		}
	}`

	dir := t.TempDir()
	configDir := filepath.Join(dir, "settings")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}

	var raw struct {
		Daemon *DaemonThresholds `json:"daemon"`
	}
	if err := json.Unmarshal([]byte(jsonData), &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Daemon.PressureCPUThresholdV() != 0.7 {
		t.Errorf("JSON CPU threshold: got %v, want 0.7", raw.Daemon.PressureCPUThresholdV())
	}
	if raw.Daemon.PressureMemThresholdGBV() != 4.0 {
		t.Errorf("JSON mem threshold: got %v, want 4.0", raw.Daemon.PressureMemThresholdGBV())
	}
	if raw.Daemon.PressureMaxSessionsV() != 8 {
		t.Errorf("JSON max sessions: got %v, want 8", raw.Daemon.PressureMaxSessionsV())
	}
}
