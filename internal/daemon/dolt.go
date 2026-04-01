package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/steveyegge/gastown/internal/doltserver"
)

const doltCmdTimeout = 15 * time.Second

// DefaultDoltHealthCheckInterval is how often the dedicated Dolt health check
// ticker fires, independent of the general daemon heartbeat (3 min).
// 30 seconds provides fast crash detection: a Dolt server crash is detected
// within 30s instead of up to 3 minutes.
const DefaultDoltHealthCheckInterval = 30 * time.Second

// DoltServerConfig holds configuration for the Dolt SQL server.
type DoltServerConfig struct {
	// Enabled controls whether the daemon manages a Dolt server.
	Enabled bool `json:"enabled"`

	// External indicates the server is externally managed (daemon monitors only).
	External bool `json:"external,omitempty"`

	// Port is the MySQL protocol port (default 3306).
	Port int `json:"port,omitempty"`

	// Host is the bind/connect address (default 127.0.0.1).
	Host string `json:"host,omitempty"`

	// User is the MySQL user name (default root).
	User string `json:"user,omitempty"`

	// Password is the MySQL password. Empty means no password.
	Password string `json:"password,omitempty"`

	// DataDir is the directory containing Dolt databases.
	// Each subdirectory becomes a database.
	DataDir string `json:"data_dir,omitempty"`

	// LogFile is the path to the Dolt server log file.
	LogFile string `json:"log_file,omitempty"`

	// AutoRestart controls whether to restart on crash.
	AutoRestart bool `json:"auto_restart,omitempty"`

	// RestartDelay is the initial delay before restarting after crash (default 5s).
	RestartDelay time.Duration `json:"restart_delay,omitempty"`

	// MaxRestartDelay is the maximum backoff delay (default 5min).
	MaxRestartDelay time.Duration `json:"max_restart_delay,omitempty"`

	// MaxRestartsInWindow is the maximum number of restarts allowed within
	// RestartWindow before escalating instead of retrying (default 5).
	MaxRestartsInWindow int `json:"max_restarts_in_window,omitempty"`

	// RestartWindow is the time window for counting restarts (default 10min).
	RestartWindow time.Duration `json:"restart_window,omitempty"`

	// HealthyResetInterval is how long the server must stay healthy before
	// the backoff counter resets (default 5min).
	HealthyResetInterval time.Duration `json:"healthy_reset_interval,omitempty"`

	// HealthCheckInterval is how often to run the Dolt health check,
	// independent of the general daemon heartbeat. This enables fast
	// detection of Dolt server crashes without changing the overall
	// heartbeat frequency. Default 30s.
	HealthCheckInterval time.Duration `json:"health_check_interval,omitempty"`
}

// DefaultDoltServerConfig returns sensible defaults for Dolt server config.
func DefaultDoltServerConfig(townRoot string) *DoltServerConfig {
	return &DoltServerConfig{
		Enabled:              false, // Opt-in
		Port:                 3306,
		Host:                 "127.0.0.1",
		User:                 "root",
		DataDir:              filepath.Join(townRoot, "dolt"),
		LogFile:              filepath.Join(townRoot, "daemon", "dolt-server.log"),
		AutoRestart:          true,
		RestartDelay:         5 * time.Second,
		MaxRestartDelay:      5 * time.Minute,
		MaxRestartsInWindow:  5,
		RestartWindow:        10 * time.Minute,
		HealthyResetInterval: 5 * time.Minute,
		HealthCheckInterval:  DefaultDoltHealthCheckInterval,
	}
}

// DoltServerStatus represents the current status of the Dolt server.
type DoltServerStatus struct {
	Running   bool      `json:"running"`
	PID       int       `json:"pid,omitempty"`
	Port      int       `json:"port,omitempty"`
	Host      string    `json:"host,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	Version   string    `json:"version,omitempty"`
	Databases []string  `json:"databases,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// doltAlertCooldown is the minimum interval between alerts of the same type.
// Alerts within the cooldown window are suppressed to prevent wisp storms.
const doltAlertCooldown = 5 * time.Minute

// doltAlertTracker deduplicates Dolt alerts using fingerprint-based cooldowns.
// Each alert type has a fingerprint; if the same fingerprint fires within the
// cooldown window, the alert is suppressed.
type doltAlertTracker struct {
	mu       sync.Mutex
	sent     map[string]time.Time // fingerprint → last sent time
	cooldown time.Duration
}

func newDoltAlertTracker(cooldown time.Duration) *doltAlertTracker {
	return &doltAlertTracker{
		sent:     make(map[string]time.Time),
		cooldown: cooldown,
	}
}

// shouldSend returns true if the alert fingerprint has not been sent within
// the cooldown window. If true, it records the current time for the fingerprint.
func (t *doltAlertTracker) shouldSend(fingerprint string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.sent[fingerprint]; ok {
		if time.Since(last) < t.cooldown {
			return false
		}
	}
	t.sent[fingerprint] = time.Now()
	return true
}

// reset clears all recorded alert times, allowing fresh alerts after recovery.
func (t *doltAlertTracker) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sent = make(map[string]time.Time)
}

// DoltServerManager manages the Dolt SQL server lifecycle.
type DoltServerManager struct {
	config   *DoltServerConfig
	townRoot string
	logger   func(format string, v ...interface{})

	mu        sync.Mutex
	process   *os.Process
	startedAt time.Time
	lastCheck time.Time

	// Backoff state for restart logic
	currentDelay    time.Duration // Current backoff delay (grows exponentially)
	restartTimes    []time.Time   // Timestamps of recent restarts within window
	lastHealthyTime time.Time     // Last time the server was confirmed healthy
	escalated       bool          // Whether we've already escalated (avoid spamming)
	restarting      bool          // Whether a restart is in progress (guards against concurrent restarts)

	// Alert deduplication — prevents wisp storms during crash loops
	alertTracker *doltAlertTracker

	// Identity verification state
	lastIdentityCheck time.Time // Last time we ran the database identity check

	// Health check warnings (Option B throttling for doctor molecule).
	// Populated by checkHealthLocked(), consumed by Daemon.ensureDoltServerRunning().
	lastWarnings []string // Warnings from the most recent health check

	// onRecoveryFn is called (in a goroutine) when the Dolt server transitions
	// from unhealthy back to healthy, i.e., when the DOLT_UNHEALTHY signal file
	// is cleared after having been present. Set by SetRecoveryCallback.
	// Protected by mu.
	onRecoveryFn func()

	// Test hooks (nil = use real implementations; set only in tests)
	healthCheckFn      func() error
	writeProbeCheckFn  func() error
	identityCheckFn    func() error // nil = use real VerifyServerDataDir
	startFn            func() error
	runningFn          func() (int, bool)
	stopFn             func()
	sleepFn            func(time.Duration)
	nowFn              func() time.Time
	escalateFn         func(int)
	unhealthyAlertFn   func(error)
	readOnlyAlertFn    func(error)
	crashAlertFn       func(int)
	listDatabasesFn    func() ([]string, error)
}

// NewDoltServerManager creates a new Dolt server manager.
func NewDoltServerManager(townRoot string, config *DoltServerConfig, logger func(format string, v ...interface{})) *DoltServerManager {
	if config == nil {
		config = DefaultDoltServerConfig(townRoot)
	}
	return &DoltServerManager{
		config:       config,
		townRoot:     townRoot,
		logger:       logger,
		alertTracker: newDoltAlertTracker(doltAlertCooldown),
	}
}

// SetRecoveryCallback registers fn to be called (in a goroutine) whenever Dolt
// transitions from unhealthy back to healthy. Only the most recently registered
// callback is used. Pass nil to clear the callback.
func (m *DoltServerManager) SetRecoveryCallback(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onRecoveryFn = fn
}

func (m *DoltServerManager) now() time.Time {
	if m.nowFn != nil {
		return m.nowFn()
	}
	return time.Now()
}

func (m *DoltServerManager) doSleep(d time.Duration) {
	if m.sleepFn != nil {
		m.sleepFn(d)
		return
	}
	time.Sleep(d)
}

// pidFile returns the path to the Dolt server PID file.
// Production (port 3307) uses the canonical "dolt.pid" for compatibility with
// gt dolt start/stop. Other ports get a port-specific name to avoid collisions.
func (m *DoltServerManager) pidFile() string {
	if m.config.Port == 3307 {
		return filepath.Join(m.townRoot, "daemon", "dolt.pid")
	}
	return filepath.Join(m.townRoot, "daemon", fmt.Sprintf("dolt-%d.pid", m.config.Port))
}

// IsEnabled returns whether Dolt server management is enabled.
func (m *DoltServerManager) IsEnabled() bool {
	return m.config != nil && m.config.Enabled
}

// IsExternal returns whether the Dolt server is externally managed.
func (m *DoltServerManager) IsExternal() bool {
	return m.config != nil && m.config.External
}

// isRemote returns true when the daemon's Dolt config points to a non-local server.
func (m *DoltServerManager) isRemote() bool {
	if m.config == nil {
		return false
	}
	switch strings.ToLower(m.config.Host) {
	case "", "127.0.0.1", "localhost", "::1", "[::1]":
		return false
	}
	return true
}

// buildDoltSQLCmd constructs a dolt sql command using daemon config, mirroring
// the doltserver.buildDoltSQLCmd pattern for local-vs-remote command construction.
func (m *DoltServerManager) buildDoltSQLCmd(ctx context.Context, args ...string) *exec.Cmd {
	var fullArgs []string
	fullArgs = append(fullArgs, "sql")

	if m.isRemote() {
		host := m.config.Host
		if host == "" {
			host = "127.0.0.1"
		}
		user := m.config.User
		if user == "" {
			user = "root"
		}
		fullArgs = append(fullArgs,
			"--host", host,
			"--port", strconv.Itoa(m.config.Port),
			"--user", user,
			"--no-tls",
		)
	}

	fullArgs = append(fullArgs, args...)
	cmd := exec.CommandContext(ctx, "dolt", fullArgs...)
	setSysProcAttr(cmd)

	// Always set cmd.Dir to DataDir — even for remote connections (GH#2537).
	// Without this, dolt auto-creates .doltcfg/privileges.db in $CWD,
	// which accumulates stray privilege files that cause "multiple
	// .doltcfg directories detected" or "Access denied" errors.
	cmd.Dir = m.config.DataDir

	if m.isRemote() && m.config.Password != "" {
		cmd.Env = append(os.Environ(), "DOLT_CLI_PASSWORD="+m.config.Password)
	}

	return cmd
}

// HealthCheckInterval returns the configured health check interval,
// falling back to DefaultDoltHealthCheckInterval if not explicitly set.
func (m *DoltServerManager) HealthCheckInterval() time.Duration {
	if m.config != nil && m.config.HealthCheckInterval > 0 {
		return m.config.HealthCheckInterval
	}
	return DefaultDoltHealthCheckInterval
}

// Status returns the current status of the Dolt server.
func (m *DoltServerManager) Status() *DoltServerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	status := &DoltServerStatus{
		Port: m.config.Port,
		Host: m.config.Host,
	}

	// Check if process is running
	pid, running := m.isRunning()
	status.Running = running
	status.PID = pid

	if running {
		status.StartedAt = m.startedAt

		// Get version
		if version, err := m.getDoltVersion(); err == nil {
			status.Version = version
		}

		// List databases
		if databases, err := m.listDatabases(); err == nil {
			status.Databases = databases
		}
	}

	return status
}

// isRunning checks if the Dolt server process is running.
// Must be called with m.mu held.
func (m *DoltServerManager) isRunning() (int, bool) {
	if m.runningFn != nil {
		return m.runningFn()
	}
	// First check our tracked process
	if m.process != nil {
		if isProcessAlive(m.process) {
			return m.process.Pid, true
		}
		// Process died, clear it
		m.process = nil
	}

	// Check PID file with nonce-based ownership verification
	pid, alive, err := verifyPIDOwnership(m.pidFile())
	if err != nil || pid == 0 {
		return 0, false
	}

	if !alive {
		// Process not running, clean up stale PID file
		_ = os.Remove(m.pidFile())
		return 0, false
	}

	// Verify it's actually our dolt server by checking port connectivity.
	// More reliable than ps string matching (ZFC fix: gt-utuk).
	if !m.isDoltServerOnPort() {
		_ = os.Remove(m.pidFile())
		return 0, false
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return 0, false
	}
	m.process = process
	return pid, true
}

// isDoltServerOnPort checks if the configured dolt port is accepting connections.
// More reliable than ps string matching for process identity verification.
func (m *DoltServerManager) isDoltServerOnPort() bool {
	addr := net.JoinHostPort(m.config.Host, strconv.Itoa(m.config.Port))
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// EnsureRunning ensures the Dolt server is running.
// If not running, starts it. If running but unhealthy, restarts it.
// Uses exponential backoff and a max-restart cap to avoid crash-looping.
func (m *DoltServerManager) EnsureRunning() error {
	if !m.IsEnabled() {
		return nil
	}

	if m.IsExternal() {
		// External mode: just check health, don't manage lifecycle
		return m.checkHealth()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Another goroutine is already restarting — skip to avoid double-starts
	if m.restarting {
		m.logger("Dolt server restart already in progress, skipping")
		return nil
	}

	pid, running := m.isRunning()
	if running {
		// Already running, check health
		m.lastCheck = m.now()
		if err := m.checkHealthLocked(); err != nil {
			m.logger("Dolt server unhealthy: %v, restarting...", err)
			m.sendUnhealthyAlert(err)
			m.writeUnhealthySignal("health_check_failed", err.Error())
			m.captureGoroutineDump()
			m.stopLocked()
			return m.restartWithBackoff()
		}
		// Check write capability (read-only detection).
		// The health check above only verifies read connectivity.
		// Under concurrent write load, Dolt can enter a persistent read-only
		// state that requires a server restart to clear.
		if err := m.checkWriteHealthLocked(); err != nil {
			m.logger("Dolt server read-only: %v, restarting...", err)
			m.sendReadOnlyAlert(err)
			m.writeUnhealthySignal("read_only", err.Error())
			m.captureGoroutineDump()
			m.stopLocked()
			return m.restartWithBackoff()
		}
		// Periodic identity check: verify the server is serving the correct databases.
		// Runs every 5 minutes (not every health tick) since imposters are rare.
		const identityCheckInterval = 5 * time.Minute
		now := m.now()
		if now.Sub(m.lastIdentityCheck) >= identityCheckInterval {
			m.lastIdentityCheck = now
			if err := m.checkDatabaseIdentityLocked(); err != nil {
				m.logger("Dolt server identity check failed: %v, restarting...", err)
				m.sendUnhealthyAlert(fmt.Errorf("identity check: %w", err))
				m.writeUnhealthySignal("imposter_detected", err.Error())
				m.captureGoroutineDump()
				m.stopLocked()
				// Also kill any imposters before restarting
				if killErr := doltserver.KillImposters(m.townRoot); killErr != nil {
					m.logger("Warning: failed to kill imposters: %v", killErr)
				}
				time.Sleep(500 * time.Millisecond)
				return m.restartWithBackoff()
			}
		}

		// Server is healthy — clear any stale unhealthy signal and reset backoff
		m.clearUnhealthySignal()
		m.maybeResetBackoff()
		return nil
	}

	// Not running, start it
	if pid > 0 {
		m.logger("Dolt server PID %d is dead, cleaning up and restarting...", pid)
		m.sendCrashAlert(pid)
		m.writeUnhealthySignal("server_dead", fmt.Sprintf("PID %d is dead", pid))
	}
	return m.restartWithBackoff()
}

// restartWithBackoff attempts to restart the Dolt server with exponential backoff
// and a max-restart cap. If the cap is exceeded, it escalates instead of retrying.
// Must be called with m.mu held.
func (m *DoltServerManager) restartWithBackoff() error {
	now := m.now()

	// Prune restart times outside the window
	m.pruneRestartTimes(now)

	// Check if we've exceeded the restart cap
	maxRestarts := m.config.MaxRestartsInWindow
	if maxRestarts <= 0 {
		maxRestarts = 5
	}
	if len(m.restartTimes) >= maxRestarts {
		if !m.escalated {
			m.escalated = true
			m.logger("Dolt server restart cap reached (%d restarts in %v), escalating to mayor",
				len(m.restartTimes), m.config.RestartWindow)
			m.sendEscalationMail(len(m.restartTimes))
		}
		return fmt.Errorf("dolt server restart cap exceeded (%d restarts in %v); escalated to mayor",
			len(m.restartTimes), m.config.RestartWindow)
	}

	// Mark restart in progress to prevent concurrent restarts during backoff sleep
	m.restarting = true
	defer func() { m.restarting = false }()

	// Apply exponential backoff delay
	delay := m.getBackoffDelay()
	if delay > 0 {
		m.logger("Backing off %v before Dolt server restart (attempt %d in window)",
			delay, len(m.restartTimes)+1)
		// Unlock during sleep so we don't hold the mutex during backoff
		m.mu.Unlock()
		m.doSleep(delay)
		m.mu.Lock()

		// Re-check after re-acquiring the lock: another goroutine may have
		// started the server while we were sleeping (TOCTOU guard).
		if _, running := m.isRunning(); running {
			m.logger("Dolt server started by another goroutine during backoff, skipping")
			return nil
		}
	}

	// Record this restart attempt
	m.restartTimes = append(m.restartTimes, m.now())

	// Advance the backoff for next time
	m.advanceBackoff()

	return m.startLocked()
}

// getBackoffDelay returns the current backoff delay.
func (m *DoltServerManager) getBackoffDelay() time.Duration {
	if m.currentDelay <= 0 {
		return m.config.RestartDelay
	}
	return m.currentDelay
}

// advanceBackoff doubles the current delay up to MaxRestartDelay.
func (m *DoltServerManager) advanceBackoff() {
	baseDelay := m.config.RestartDelay
	if baseDelay <= 0 {
		baseDelay = 5 * time.Second
	}
	maxDelay := m.config.MaxRestartDelay
	if maxDelay <= 0 {
		maxDelay = 5 * time.Minute
	}

	if m.currentDelay <= 0 {
		m.currentDelay = baseDelay
	}
	m.currentDelay *= 2
	if m.currentDelay > maxDelay {
		m.currentDelay = maxDelay
	}
}

// pruneRestartTimes removes restart timestamps outside the configured window.
func (m *DoltServerManager) pruneRestartTimes(now time.Time) {
	window := m.config.RestartWindow
	if window <= 0 {
		window = 10 * time.Minute
	}
	cutoff := now.Add(-window)
	pruned := m.restartTimes[:0]
	for _, t := range m.restartTimes {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}
	m.restartTimes = pruned
}

// maybeResetBackoff resets backoff state if the server has been healthy
// for the configured HealthyResetInterval.
// Must be called with m.mu held.
func (m *DoltServerManager) maybeResetBackoff() {
	now := m.now()
	resetInterval := m.config.HealthyResetInterval
	if resetInterval <= 0 {
		resetInterval = 5 * time.Minute
	}

	if m.lastHealthyTime.IsZero() {
		m.lastHealthyTime = now
		return
	}

	if now.Sub(m.lastHealthyTime) >= resetInterval {
		if m.currentDelay > 0 || len(m.restartTimes) > 0 || m.escalated {
			m.logger("Dolt server healthy for %v, resetting backoff state", resetInterval)
			m.currentDelay = 0
			m.restartTimes = nil
			m.escalated = false
			if m.alertTracker != nil {
				m.alertTracker.reset()
			}
		}
		// Reset the healthy timestamp after a successful reset so the next
		// reset interval is measured from now, not from the original detection.
		m.lastHealthyTime = now
	}
}

// sendEscalationMail sends a mail to the mayor when the Dolt server has
// exceeded its restart cap, indicating a systemic issue.
// Runs the mail command asynchronously to avoid blocking the mutex.
func (m *DoltServerManager) sendEscalationMail(restartCount int) {
	if m.escalateFn != nil {
		m.escalateFn(restartCount)
		return
	}
	subject := fmt.Sprintf("ESCALATION: Dolt server crash-looping (%d restarts)", restartCount)
	body := fmt.Sprintf(`The Dolt server has restarted %d times within %v and has been capped.

The daemon will NOT restart it again until the backoff window expires or the issue is resolved.

Possible causes:
- Bad configuration
- Corrupt data directory
- Disk full
- Port conflict

Data dir: %s
Log file: %s
Host: %s:%d

Action needed: Investigate and fix the root cause, then restart the daemon or the Dolt server manually.`,
		restartCount, m.config.RestartWindow,
		m.config.DataDir, m.config.LogFile,
		m.config.Host, m.config.Port)

	townRoot := m.townRoot
	logger := m.logger

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "gt", "mail", "send", "mayor/", "-s", subject, "-m", body, "--wisp-type", "escalation") //nolint:gosec // G204: args are constructed internally
		setSysProcAttr(cmd)
		cmd.Dir = townRoot
		cmd.Env = os.Environ()

		if err := cmd.Run(); err != nil {
			logger("Warning: failed to send escalation mail to mayor: %v", err)
		} else {
			logger("Sent escalation mail to mayor about Dolt server crash-loop")
		}
	}()
}

// sendCrashAlert nudges the mayor when the Dolt server is found dead.
// This is for single crash detection — distinct from crash-loop escalation.
// Uses nudge (not mail) to avoid creating wisps, and applies a cooldown
// to prevent alert storms during crash loops.
// Runs asynchronously to avoid blocking.
func (m *DoltServerManager) sendCrashAlert(deadPID int) {
	if m.crashAlertFn != nil {
		m.crashAlertFn(deadPID)
		return
	}
	if !m.alertTracker.shouldSend("dolt-crash") {
		m.logger("Dolt crash alert suppressed (cooldown active)")
		return
	}
	message := fmt.Sprintf(`ALERT: Dolt server crashed. PID %d found dead, daemon is restarting. Data dir: %s, Log: %s, Host: %s:%d`,
		deadPID,
		m.config.DataDir, m.config.LogFile,
		m.config.Host, m.config.Port)

	townRoot := m.townRoot
	logger := m.logger

	go func() {
		sendDoltAlertNudge(townRoot, "mayor/", message, logger)
	}()
}

// sendUnhealthyAlert nudges the mayor when the Dolt server fails health checks.
// The server is running but not responding to queries.
// Uses nudge (not mail) to avoid creating wisps, and applies a cooldown
// to prevent alert storms. Witnesses detect degraded state via the
// DOLT_UNHEALTHY signal file.
// Runs asynchronously.
func (m *DoltServerManager) sendUnhealthyAlert(healthErr error) {
	if m.unhealthyAlertFn != nil {
		m.unhealthyAlertFn(healthErr)
		return
	}
	if !m.alertTracker.shouldSend("dolt-unhealthy") {
		m.logger("Dolt unhealthy alert suppressed (cooldown active)")
		return
	}
	message := fmt.Sprintf(`ALERT: Dolt server unhealthy — failing health checks, daemon is restarting. Error: %v. Data dir: %s, Host: %s:%d`,
		healthErr,
		m.config.DataDir,
		m.config.Host, m.config.Port)

	townRoot := m.townRoot
	logger := m.logger

	go func() {
		sendDoltAlertNudge(townRoot, "mayor/", message, logger)
	}()
}

// sendDoltAlertNudge sends a Dolt alert as a nudge (ephemeral, no wisp created).
// Used for transient alerts (crash, unhealthy) that don't need to survive session death.
func sendDoltAlertNudge(townRoot, recipient, message string, logger func(format string, v ...interface{})) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gt", "nudge", recipient, message) //nolint:gosec // G204: args are constructed internally
	setSysProcAttr(cmd)
	cmd.Dir = townRoot
	cmd.Env = os.Environ()

	if err := cmd.Run(); err != nil {
		logger("Warning: failed to nudge %s about Dolt alert: %v", recipient, err)
	}
}

// sendDoltAlertMail sends a Dolt alert mail to a specific recipient.
func sendDoltAlertMail(townRoot, recipient, subject, body string, logger func(format string, v ...interface{})) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gt", "mail", "send", recipient, "-s", subject, "-m", body) //nolint:gosec // G204: args are constructed internally
	setSysProcAttr(cmd)
	cmd.Dir = townRoot
	cmd.Env = os.Environ()

	if err := cmd.Run(); err != nil {
		logger("Warning: failed to send Dolt alert to %s: %v", recipient, err)
	}
}

// sendDoltAlertToWitnesses sends a Dolt alert to all rig witnesses.
// Discovers rigs from mayor/rigs.json and sends to each <rig>/witness.
func sendDoltAlertToWitnesses(townRoot, subject, body string, logger func(format string, v ...interface{})) {
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	data, err := os.ReadFile(rigsPath)
	if err != nil {
		return // No rigs.json, nothing to notify
	}

	var parsed struct {
		Rigs map[string]interface{} `json:"rigs"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return
	}

	for rigName := range parsed.Rigs {
		recipient := rigName + "/witness"
		sendDoltAlertMail(townRoot, recipient, subject, body, logger)
	}
}

// unhealthySignalFile returns the path to the DOLT_UNHEALTHY signal file.
// Witness patrols can check for this file to detect degraded Dolt state.
// Production (port 3307) uses the canonical name; other ports get a suffix
// so multiple instances don't clobber each other's signal files.
func (m *DoltServerManager) unhealthySignalFile() string {
	if m.config.Port == 3307 {
		return filepath.Join(m.townRoot, "daemon", "DOLT_UNHEALTHY")
	}
	return filepath.Join(m.townRoot, "daemon", fmt.Sprintf("DOLT_UNHEALTHY_%d", m.config.Port))
}

// writeUnhealthySignal writes the DOLT_UNHEALTHY signal file.
// This file signals to witness patrols that the Dolt server is degraded.
func (m *DoltServerManager) writeUnhealthySignal(reason, detail string) {
	payload := fmt.Sprintf(`{"reason":%q,"detail":%q,"timestamp":%q}`,
		reason, detail, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(m.unhealthySignalFile(), []byte(payload), 0644); err != nil {
		m.logger("Warning: failed to write DOLT_UNHEALTHY signal: %v", err)
	}
}

// clearUnhealthySignal removes the DOLT_UNHEALTHY signal file when the server is healthy.
// If the signal file was present (meaning Dolt was previously unhealthy), it fires the
// onRecoveryFn callback in a goroutine to trigger a convoy recovery sweep.
// Must be called with mu held (onRecoveryFn is protected by mu).
func (m *DoltServerManager) clearUnhealthySignal() {
	signalFile := m.unhealthySignalFile()
	_, wasUnhealthy := os.Stat(signalFile)
	_ = os.Remove(signalFile)
	// Transition detected: was unhealthy, now healthy — fire recovery callback.
	if wasUnhealthy == nil && m.onRecoveryFn != nil {
		fn := m.onRecoveryFn
		go fn()
	}
}

// IsDoltUnhealthy checks if the DOLT_UNHEALTHY signal file exists.
// This is a package-level function for use by witness patrols and other consumers.
func IsDoltUnhealthy(townRoot string) bool {
	_, err := os.Stat(filepath.Join(townRoot, "daemon", "DOLT_UNHEALTHY"))
	return err == nil
}

// writeDaemonDoltConfig writes a Dolt config.yaml to configPath using the
// daemon's DoltServerConfig. Unlike CLI flags, config.yaml can set
// read_timeout_millis and write_timeout_millis, which prevents CLOSE_WAIT
// accumulation when clients disconnect without completing their SQL sessions.
func writeDaemonDoltConfig(cfg *DoltServerConfig, configPath string) error {
	hostLine := ""
	if cfg.Host != "" {
		hostLine = fmt.Sprintf("\n  host: %s", cfg.Host)
	}
	content := fmt.Sprintf(`# Dolt SQL server configuration — managed by Gas Town daemon
# Do not edit manually; overwritten on each daemon-managed server start.

log_level: info

listener:
  port: %d%s
  read_timeout_millis: 30000
  write_timeout_millis: 30000
  max_connections: 1000

data_dir: %q

behavior:
  dolt_transaction_commit: false
  auto_gc_behavior:
    enable: true
    archive_level: 1
`,
		cfg.Port,
		hostLine,
		cfg.DataDir,
	)
	return os.WriteFile(configPath, []byte(content), 0600)
}

// Start starts the Dolt SQL server.
func (m *DoltServerManager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startLocked()
}

// startLocked starts the Dolt server. Must be called with m.mu held.
func (m *DoltServerManager) startLocked() error {
	if m.startFn != nil {
		return m.startFn()
	}

	// Re-check if the server is already running to close the TOCTOU window.
	// Another goroutine may have started the server while we were waiting
	// for the mutex (via Start()) or during backoff sleep (via restartWithBackoff()).
	if _, running := m.isRunning(); running {
		m.logger("Dolt server already running, skipping start")
		return nil
	}

	// Ensure data directory exists
	if err := os.MkdirAll(m.config.DataDir, 0755); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}

	// Check if dolt is installed
	doltPath, err := exec.LookPath("dolt")
	if err != nil {
		return fmt.Errorf("dolt not found in PATH: %w", err)
	}

	// Write config.yaml with timeouts before starting. CLI flags like --port
	// silently override the config file but cannot set timeout fields, so we
	// use --config instead. This prevents CLOSE_WAIT accumulation that occurs
	// when Dolt uses its 8-hour default read/write timeouts. (gt-ch5)
	configPath := filepath.Join(m.config.DataDir, "config.yaml")
	if err := writeDaemonDoltConfig(m.config, configPath); err != nil {
		m.logger("Warning: failed to write Dolt config.yaml: %v", err)
	}

	// Build command arguments
	args := []string{
		"sql-server",
		"--config", configPath,
	}

	// Open log file
	logFile, err := os.OpenFile(m.config.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("opening log file: %w", err)
	}

	// Start dolt sql-server as background process
	cmd := exec.Command(doltPath, args...)
	cmd.Dir = m.config.DataDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	// Detach from this process group so it survives daemon restart
	setSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		if closeErr := logFile.Close(); closeErr != nil {
			m.logger("Warning: failed to close dolt log file: %v", closeErr)
		}
		return fmt.Errorf("starting dolt sql-server: %w", err)
	}

	// Don't wait for it - it's a long-running server
	go func() {
		_ = cmd.Wait()
		if closeErr := logFile.Close(); closeErr != nil {
			m.logger("Warning: failed to close dolt log file: %v", closeErr)
		}
	}()

	m.process = cmd.Process
	m.startedAt = time.Now()

	// Write PID file with nonce for ownership verification
	if _, err := writePIDFile(m.pidFile(), cmd.Process.Pid); err != nil {
		m.logger("Warning: failed to write PID file: %v", err)
	}

	m.logger("Started Dolt SQL server (PID %d) on %s:%d", cmd.Process.Pid, m.config.Host, m.config.Port)

	// Wait a moment for server to initialize
	time.Sleep(500 * time.Millisecond)

	// Verify it started successfully
	if err := m.checkHealthLocked(); err != nil {
		m.logger("Warning: Dolt server may not be healthy: %v", err)
	}

	return nil
}

// Stop stops the Dolt SQL server.
func (m *DoltServerManager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
	return nil
}

// stopLocked stops the Dolt server. Must be called with m.mu held.
// captureGoroutineDump sends SIGQUIT to the Dolt server to dump goroutine stacks
// to its log file. Per Tim Sehn (Dolt CEO): kill -QUIT prints all goroutine stacks
// to stderr, which is redirected to the server log. Called before stopping an
// unhealthy server so the dump captures what it was stuck on.
func (m *DoltServerManager) captureGoroutineDump() {
	pid, running := m.isRunning()
	if !running {
		return
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	m.logger("Capturing goroutine dump from Dolt server (PID %d) before restart...", pid)
	if runtime.GOOS == "windows" {
		m.logger("Goroutine dump via SIGQUIT not supported on Windows, skipping")
		return
	}
	if err := process.Signal(syscall.SIGQUIT); err != nil {
		m.logger("Warning: failed to send SIGQUIT for goroutine dump: %v", err)
		return
	}
	// Give the server a moment to write the dump to its log file.
	time.Sleep(500 * time.Millisecond)
	m.logger("Goroutine dump written to server log. View with: gt dolt logs -n 200")
}

func (m *DoltServerManager) stopLocked() {
	if m.stopFn != nil {
		m.stopFn()
		return
	}
	pid, running := m.isRunning()
	if !running {
		return
	}

	m.logger("Stopping Dolt SQL server (PID %d)...", pid)

	process, err := os.FindProcess(pid)
	if err != nil {
		return // Already gone
	}

	// Send termination signal for graceful shutdown
	if err := sendTermSignal(process); err != nil {
		m.logger("Warning: failed to send termination signal: %v", err)
	}

	// Wait for graceful shutdown (up to 5 seconds)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			if !isProcessAlive(process) {
				close(done)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	select {
	case <-done:
		m.logger("Dolt SQL server stopped gracefully")
	case <-time.After(30 * time.Second):
		// Force kill — 30s allows Dolt to flush its append-only journal under load.
		// A SIGKILL mid-journal-write causes corruption requiring dolt fsck to recover.
		m.logger("Dolt SQL server did not stop gracefully after 30s, forcing termination")
		_ = sendKillSignal(process)
	}

	// Clean up
	_ = os.Remove(m.pidFile())
	m.process = nil
}

// checkHealth checks if the Dolt server is healthy (can accept connections).
func (m *DoltServerManager) checkHealth() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.checkHealthLocked()
}

// checkHealthLocked checks health. Must be called with m.mu held.
// Performs a connectivity check (SELECT active_branch()) with latency measurement, and logs
// warnings for degraded resource conditions (high latency, high connection count,
// disk usage). Returns an error only if the server is unreachable.
// Warnings are collected in m.lastWarnings for Option B throttling: the daemon
// pours a mol-dog-doctor molecule only when anomalies are detected.
func (m *DoltServerManager) checkHealthLocked() error {
	m.lastWarnings = nil // Reset warnings each check cycle.

	if m.healthCheckFn != nil {
		return m.healthCheckFn()
	}
	// 1. Connectivity + latency: time a SELECT active_branch()
	// Per Tim Sehn (Dolt CEO): active_branch() is a lightweight probe that
	// won't block behind queued queries, unlike SELECT 1 which goes through
	// the full query executor.
	ctx, cancel := context.WithTimeout(context.Background(), doltCmdTimeout)
	defer cancel()

	start := time.Now()
	cmd := m.buildDoltSQLCmd(ctx, "-q", "SELECT active_branch()")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("health check failed: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	latency := time.Since(start)
	if latency > 1*time.Second {
		w := fmt.Sprintf("Dolt health check latency %v exceeds 1s threshold — server may be under stress", latency.Round(time.Millisecond))
		m.lastWarnings = append(m.lastWarnings, w)
		m.logger("Warning: %s", w)
	}

	// 2. Connection count (best-effort, non-fatal)
	if w := m.checkConnectionCount(); w != "" {
		m.lastWarnings = append(m.lastWarnings, w)
		m.logger("Warning: %s", w)
	}

	// 3. Disk space (best-effort, non-fatal)
	if w := m.checkDiskUsage(); w != "" {
		m.lastWarnings = append(m.lastWarnings, w)
		m.logger("Warning: %s", w)
	}

	// 4. Database count (best-effort, non-fatal) — orphan detection
	if w := m.checkDatabaseCount(); w != "" {
		m.lastWarnings = append(m.lastWarnings, w)
		m.logger("Warning: %s", w)
	}

	// 5. Backup freshness (best-effort, non-fatal)
	for _, w := range m.checkBackupFreshness() {
		m.lastWarnings = append(m.lastWarnings, w)
		m.logger("Warning: %s", w)
	}

	return nil
}

// LastWarnings returns warnings from the most recent health check.
// Used by the Daemon for Option B throttling: only pour a mol-dog-doctor
// molecule when anomalies are detected.
func (m *DoltServerManager) LastWarnings() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastWarnings
}

// checkConnectionCount queries the connection count and returns a warning if approaching the limit.
// Non-fatal: failures return empty string.
func (m *DoltServerManager) checkConnectionCount() string {
	ctx, cancel := context.WithTimeout(context.Background(), doltCmdTimeout)
	defer cancel()
	cmd := m.buildDoltSQLCmd(ctx,
		"-r", "csv",
		"-q", "SELECT COUNT(*) AS cnt FROM information_schema.PROCESSLIST",
	)

	output, err := cmd.Output()
	if err != nil {
		return "" // non-fatal
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 {
		return ""
	}
	count, err := strconv.Atoi(strings.TrimSpace(lines[len(lines)-1]))
	if err != nil {
		return ""
	}

	// Use the doltserver package default (50) as a reasonable cap reference
	maxConn := 50
	threshold := (maxConn * 80) / 100
	if count >= threshold {
		return fmt.Sprintf("Dolt connection count %d is at %d%% of max %d — approaching limit",
			count, (count*100)/maxConn, maxConn)
	}
	return ""
}

// checkDiskUsage checks disk usage of the data directory and returns a warning
// if it exceeds 1 GB. Non-fatal: failures return empty string.
func (m *DoltServerManager) checkDiskUsage() string {
	dataDir := m.config.DataDir
	if dataDir == "" {
		return ""
	}

	var total int64
	_ = filepath.Walk(dataDir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})

	const gb = 1024 * 1024 * 1024
	if total > gb {
		return fmt.Sprintf("Dolt data directory %s is %.1f GB", dataDir, float64(total)/float64(gb))
	}
	return ""
}

// checkDatabaseCount queries the database list and returns a warning if the count exceeds
// what's expected based on the data directory contents. Non-fatal: failures return empty string.
// The expected count is derived from subdirectories in the data dir (each is a registered DB).
func (m *DoltServerManager) checkDatabaseCount() string {
	databases, err := m.getDatabases()
	if err != nil {
		return "" // non-fatal
	}

	// Derive expected count from data directory — each subdirectory is a database.
	// This adapts automatically as users add/remove rigs.
	expected := m.countDataDirDatabases()
	if expected == 0 {
		expected = 6 // Fallback if data dir can't be read
	}

	// Allow a small buffer (3) above expected for transient states.
	threshold := expected + 3
	if len(databases) > threshold {
		return fmt.Sprintf("%d databases detected (expected ~%d, threshold %d) — possible orphan/test database accumulation: %v",
			len(databases), expected, threshold, databases)
	}
	return ""
}

// countDataDirDatabases counts subdirectories in the Dolt data directory.
// Each subdirectory corresponds to a registered database.
func (m *DoltServerManager) countDataDirDatabases() int {
	dataDir := m.config.DataDir
	if dataDir == "" {
		return 0
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			count++
		}
	}
	return count
}

// checkBackupFreshness checks if Dolt backups are fresh. Returns warnings for any configured
// backup database that hasn't been synced in over 2 hours. Non-fatal: failures return nil.
func (m *DoltServerManager) checkBackupFreshness() []string {
	backupDir := filepath.Join(m.townRoot, ".dolt-backup")
	info, err := os.Stat(backupDir)
	if err != nil || !info.IsDir() {
		return nil // No backup directory — backup patrol may not be configured
	}

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return nil
	}

	const staleThreshold = 2 * time.Hour
	now := time.Now()
	var warnings []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dbInfo, err := entry.Info()
		if err != nil {
			continue
		}
		age := now.Sub(dbInfo.ModTime())
		if age > staleThreshold {
			warnings = append(warnings, fmt.Sprintf("Dolt backup %q is %.0f minutes old (threshold %.0fm) — backup patrol may be stalled",
				entry.Name(), age.Minutes(), staleThreshold.Minutes()))
		}
	}
	return warnings
}

// checkWriteHealthLocked probes the Dolt server's write capability by attempting
// a test write operation. If the server is in read-only mode (e.g., from concurrent
// write contention on the manifest), the write probe will fail with a characteristic
// error. Returns an error only if the server is confirmed read-only.
// Must be called with m.mu held.
func (m *DoltServerManager) checkWriteHealthLocked() error {
	if m.writeProbeCheckFn != nil {
		return m.writeProbeCheckFn()
	}

	// Get a database to test writes against
	databases, err := m.getDatabases()
	if err != nil || len(databases) == 0 {
		return nil // Skip write probe if no databases available
	}

	db := databases[0]

	ctx, cancel := context.WithTimeout(context.Background(), doltCmdTimeout)
	defer cancel()

	// Attempt a write operation to detect read-only mode.
	// CREATE TABLE IF NOT EXISTS is idempotent (safe if table lingers from previous probe).
	// REPLACE INTO always writes a row, testing the storage layer even if the table existed.
	// DROP TABLE IF EXISTS cleans up.
	// If ANY statement triggers "database is read only", the command fails and we detect it.
	query := fmt.Sprintf(
		"USE `%s`; CREATE TABLE IF NOT EXISTS `__gt_health_probe` (v INT PRIMARY KEY); REPLACE INTO `__gt_health_probe` VALUES (1); DROP TABLE IF EXISTS `__gt_health_probe`",
		db,
	)
	cmd := m.buildDoltSQLCmd(ctx, "-q", query)

	output, err := cmd.CombinedOutput()
	if err != nil {
		errMsg := strings.TrimSpace(string(output))
		if isReadOnlyError(errMsg) {
			return fmt.Errorf("dolt server is in read-only mode: %s", errMsg)
		}
		// Non-read-only failures: log warning but don't fail health check.
		// These could be transient issues (timeout, lock contention) that
		// don't indicate a persistent read-only state.
		m.logger("Warning: Dolt write probe failed (non-read-only): %v (%s)", err, errMsg)
	}

	return nil
}

// checkDatabaseIdentityLocked verifies the running Dolt server is serving the
// correct databases from the expected data directory. Detects "imposter" servers
// where another process (e.g., bd's embedded Dolt) hijacked the port.
// Must be called with m.mu held.
func (m *DoltServerManager) checkDatabaseIdentityLocked() error {
	if m.identityCheckFn != nil {
		return m.identityCheckFn()
	}

	// Use the doltserver package's verification which checks --data-dir
	// on the process command line and falls back to database comparison.
	legitimate, err := doltserver.VerifyServerDataDir(m.townRoot)
	if err != nil {
		return fmt.Errorf("server identity verification failed: %w", err)
	}
	if !legitimate {
		return fmt.Errorf("server is an imposter (wrong data directory)")
	}

	// Additional check: verify expected databases have data.
	// If the server is serving from the right dir but databases are empty,
	// something else is wrong.
	expectedDBs, fsErr := doltserver.ListDatabases(m.townRoot)
	if fsErr != nil || len(expectedDBs) == 0 {
		return nil // Can't verify further without expected databases
	}

	// Spot-check one database: query for issues or wisps table existence.
	// Agent beads may be in the wisps table after migration, so check both.
	db := expectedDBs[0]
	ctx, cancel := context.WithTimeout(context.Background(), doltCmdTimeout)
	defer cancel()

	// Try issues table first
	issueCount := -1
	query := fmt.Sprintf("SELECT COUNT(*) AS cnt FROM `%s`.`issues`", db)
	cmd := m.buildDoltSQLCmd(ctx, "-r", "csv", "-q", query)
	if output, queryErr := cmd.Output(); queryErr == nil {
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		if len(lines) >= 2 {
			if c, err := strconv.Atoi(strings.TrimSpace(lines[len(lines)-1])); err == nil {
				issueCount = c
			}
		}
	}

	// Also try wisps table (may not exist yet)
	wispCount := -1
	wispCtx, wispCancel := context.WithTimeout(context.Background(), doltCmdTimeout)
	defer wispCancel()
	wispQuery := fmt.Sprintf("SELECT COUNT(*) AS cnt FROM `%s`.`wisps`", db)
	wispCmd := m.buildDoltSQLCmd(wispCtx, "-r", "csv", "-q", wispQuery)
	if wispOutput, wispErr := wispCmd.Output(); wispErr == nil {
		wispLines := strings.Split(strings.TrimSpace(string(wispOutput)), "\n")
		if len(wispLines) >= 2 {
			if c, err := strconv.Atoi(strings.TrimSpace(wispLines[len(wispLines)-1])); err == nil {
				wispCount = c
			}
		}
	}

	// If neither table exists, that's OK (not all DBs have beads)
	if issueCount < 0 && wispCount < 0 {
		return nil
	}

	// Total across both tables
	totalCount := 0
	if issueCount > 0 {
		totalCount += issueCount
	}
	if wispCount > 0 {
		totalCount += wispCount
	}

	// If we know the filesystem has data but the server returns 0 rows
	// across both tables, this is suspicious.
	if totalCount == 0 {
		dbDir := doltserver.RigDatabaseDir(m.townRoot, db)
		commitDir := filepath.Join(dbDir, ".dolt", "noms")
		if info, err := os.Stat(commitDir); err == nil && info.IsDir() {
			var totalSize int64
			_ = filepath.Walk(dbDir, func(_ string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() {
					totalSize += info.Size()
				}
				return nil
			})
			if totalSize > 1024*1024 { // > 1MB
				return fmt.Errorf("database %q has %s on disk but 0 rows in server (issues+wisps) — possible imposter",
					db, formatDiskSize(totalSize))
			}
		}
	}

	return nil
}

// formatDiskSize returns a human-readable size string.
func formatDiskSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// getDatabases returns the list of databases. Uses the test hook if set.
func (m *DoltServerManager) getDatabases() ([]string, error) {
	if m.listDatabasesFn != nil {
		return m.listDatabasesFn()
	}
	return m.listDatabases()
}

// isReadOnlyError checks if an error message indicates a Dolt read-only state.
func isReadOnlyError(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "read only") ||
		strings.Contains(lower, "read-only") ||
		strings.Contains(lower, "readonly")
}

// sendReadOnlyAlert sends an alert when the Dolt server enters read-only mode.
// This is distinct from unhealthy alerts — the server is running and responding
// to reads, but cannot accept writes. Runs asynchronously.
func (m *DoltServerManager) sendReadOnlyAlert(readOnlyErr error) {
	if m.readOnlyAlertFn != nil {
		m.readOnlyAlertFn(readOnlyErr)
		return
	}
	subject := "ALERT: Dolt server entered READ-ONLY mode"
	body := fmt.Sprintf(`The Dolt server is running but has entered read-only mode.
All write operations (beads create, update, close) will fail until the server is restarted.

The daemon is restarting the server automatically.

Error: %v

Data dir: %s
Log file: %s
Host: %s:%d

This typically occurs under heavy concurrent write load when multiple agents
contend for the storage manifest. If it recurs frequently, consider reducing
concurrent polecat count or staggering write-heavy operations.`,
		readOnlyErr,
		m.config.DataDir, m.config.LogFile,
		m.config.Host, m.config.Port)

	townRoot := m.townRoot
	logger := m.logger

	go func() {
		sendDoltAlertMail(townRoot, "mayor/", subject, body, logger)
		sendDoltAlertToWitnesses(townRoot, subject, body, logger)
	}()
}

// getDoltVersion returns the Dolt server version.
func (m *DoltServerManager) getDoltVersion() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), doltCmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "dolt", "version")
	setSysProcAttr(cmd)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	// Parse "dolt version X.Y.Z"
	line := strings.TrimSpace(string(output))
	parts := strings.Fields(line)
	if len(parts) >= 3 {
		return parts[2], nil
	}
	return line, nil
}

// listDatabases returns the list of databases in the Dolt server.
// Delegates to doltserver.ListDatabases which caches results and deduplicates
// concurrent queries to avoid the thundering herd problem (GH#2180).
func (m *DoltServerManager) listDatabases() ([]string, error) {
	return doltserver.ListDatabases(m.townRoot)
}

// CountDoltServers returns the count of running dolt sql-server processes.
// Uses lsof-based listener discovery instead of pgrep string matching (ZFC fix: gt-fj87).
func CountDoltServers() int {
	return len(doltserver.FindAllDoltListeners())
}

// StopAllDoltServers stops all dolt sql-server processes.
// Returns (killed, remaining).
// Uses lsof-based discovery and direct signal delivery instead of pkill -f (ZFC fix: gt-fj87).
func StopAllDoltServers(force bool) (int, int) {
	listeners := doltserver.FindAllDoltListeners()
	if len(listeners) == 0 {
		return 0, 0
	}

	// Deduplicate PIDs (one process may listen on multiple ports).
	seen := make(map[int]bool)
	var pids []int
	for _, l := range listeners {
		if !seen[l.PID] {
			seen[l.PID] = true
			pids = append(pids, l.PID)
		}
	}
	before := len(pids)

	for _, pid := range pids {
		if p, err := os.FindProcess(pid); err == nil {
			if force {
				_ = sendKillSignal(p)
			} else {
				_ = sendTermSignal(p)
			}
		}
	}

	if !force {
		time.Sleep(2 * time.Second)
		// Check if any survived, escalate to kill.
		remaining := doltserver.FindAllDoltListeners()
		if len(remaining) > 0 {
			for _, l := range remaining {
				if p, err := os.FindProcess(l.PID); err == nil {
					_ = sendKillSignal(p)
				}
			}
		}
	}

	time.Sleep(100 * time.Millisecond)

	after := CountDoltServers()
	killed := before - after
	if killed < 0 {
		killed = 0
	}
	return killed, after
}
