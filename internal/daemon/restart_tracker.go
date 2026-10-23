package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/atomicfile"
)

// RestartTrackerConfig holds configurable parameters for restart tracking.
// All fields have sensible defaults if zero-valued.
type RestartTrackerConfig struct {
	// InitialBackoff is the delay before the first retry (default 30s).
	InitialBackoff time.Duration `json:"initial_backoff,omitempty"`

	// MaxBackoff is the maximum backoff delay (default 10m).
	MaxBackoff time.Duration `json:"max_backoff,omitempty"`

	// BackoffMultiplier scales the backoff on each retry (default 2.0).
	BackoffMultiplier float64 `json:"backoff_multiplier,omitempty"`

	// CrashLoopWindow is the time window for counting crash-loop restarts (default 15m).
	CrashLoopWindow time.Duration `json:"crash_loop_window,omitempty"`

	// CrashLoopCount is how many restarts within the window trigger crash-loop state (default 5).
	CrashLoopCount int `json:"crash_loop_count,omitempty"`

	// StabilityPeriod is how long an agent must run without restarting
	// before its backoff resets (default 30m).
	StabilityPeriod time.Duration `json:"stability_period,omitempty"`

	// PauseBackoff is the fixed delay applied when an agent is paused due
	// to a transient external limit (e.g., Claude usage-limit reached)
	// rather than a true crash. Does not escalate and does not count toward
	// the crash-loop fault budget. Default 60s — long enough for the
	// quota_dog patrol to rotate accounts (5m cadence), short enough to
	// recover quickly when the limit resets.
	PauseBackoff time.Duration `json:"pause_backoff,omitempty"`

	// CrashLoopProbeInterval is the minimum time between local watchdog
	// probes after a crash-loop latch (default 30m). These probes never
	// authorize hosted-agent promotion.
	CrashLoopProbeInterval time.Duration `json:"crash_loop_probe_interval,omitempty"`
}

// DefaultRestartTrackerConfig returns the default restart tracker configuration.
func DefaultRestartTrackerConfig() RestartTrackerConfig {
	return RestartTrackerConfig{
		InitialBackoff:         30 * time.Second,
		MaxBackoff:             10 * time.Minute,
		BackoffMultiplier:      2.0,
		CrashLoopWindow:        15 * time.Minute,
		CrashLoopCount:         5,
		StabilityPeriod:        30 * time.Minute,
		PauseBackoff:           60 * time.Second,
		CrashLoopProbeInterval: 30 * time.Minute,
	}
}

// withDefaults returns a config with zero fields filled from defaults.
func (c RestartTrackerConfig) withDefaults() RestartTrackerConfig {
	d := DefaultRestartTrackerConfig()
	if c.InitialBackoff <= 0 {
		c.InitialBackoff = d.InitialBackoff
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = d.MaxBackoff
	}
	if c.BackoffMultiplier <= 0 {
		c.BackoffMultiplier = d.BackoffMultiplier
	}
	if c.CrashLoopWindow <= 0 {
		c.CrashLoopWindow = d.CrashLoopWindow
	}
	if c.CrashLoopCount <= 0 {
		c.CrashLoopCount = d.CrashLoopCount
	}
	if c.StabilityPeriod <= 0 {
		c.StabilityPeriod = d.StabilityPeriod
	}
	if c.PauseBackoff <= 0 {
		c.PauseBackoff = d.PauseBackoff
	}
	if c.CrashLoopProbeInterval <= 0 {
		c.CrashLoopProbeInterval = d.CrashLoopProbeInterval
	}
	return c
}

// RestartTracker tracks agent restart attempts with exponential backoff.
// This prevents runaway restart loops when an agent keeps crashing.
type RestartTracker struct {
	mu       sync.RWMutex
	townRoot string
	config   RestartTrackerConfig
	state    *RestartState
	now      func() time.Time
	loadErr  error
}

// RestartState persists restart tracking data.
type RestartState struct {
	Agents map[string]*AgentRestartInfo `json:"agents"`
}

// AgentRestartInfo tracks restart info for a single agent.
type AgentRestartInfo struct {
	LastRestart           time.Time   `json:"last_restart"`
	RestartCount          int         `json:"restart_count"`
	RecentRestarts        []time.Time `json:"recent_restarts,omitempty"`
	BackoffUntil          time.Time   `json:"backoff_until"`
	CrashLoopSince        time.Time   `json:"crash_loop_since,omitempty"`
	LastWatchdogProbe     time.Time   `json:"last_watchdog_probe,omitempty"`
	CrashLoopAlertPending bool        `json:"crash_loop_alert_pending,omitempty"`
}

// NewRestartTracker creates a new restart tracker with the given config.
// Zero-valued config fields are filled with defaults.
func NewRestartTracker(townRoot string, cfg RestartTrackerConfig) *RestartTracker {
	return newRestartTrackerWithClock(townRoot, cfg, time.Now)
}

func newRestartTrackerWithClock(townRoot string, cfg RestartTrackerConfig, now func() time.Time) *RestartTracker {
	if now == nil {
		now = time.Now
	}
	return &RestartTracker{
		townRoot: townRoot,
		config:   cfg.withDefaults(),
		state:    &RestartState{Agents: make(map[string]*AgentRestartInfo)},
		now:      now,
	}
}

// restartStateFile returns the path to the restart state file.
func (rt *RestartTracker) restartStateFile() string {
	return filepath.Join(rt.townRoot, "daemon", "restart_state.json")
}

// Load loads the restart state from disk.
func (rt *RestartTracker) Load() error {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	data, err := os.ReadFile(rt.restartStateFile())
	if err != nil {
		if os.IsNotExist(err) {
			rt.state = &RestartState{Agents: make(map[string]*AgentRestartInfo)}
			rt.loadErr = nil
			return nil // No state file yet
		}
		rt.loadErr = err
		return err
	}

	// Decode into a temporary value so malformed JSON cannot partially mutate
	// live restart authorization state.
	var next *RestartState
	if err := json.Unmarshal(data, &next); err != nil {
		rt.loadErr = err
		return err
	}
	if next == nil {
		err := fmt.Errorf("restart state is null")
		rt.loadErr = err
		return err
	}
	if next.Agents == nil {
		next.Agents = make(map[string]*AgentRestartInfo)
	}
	for agentID, info := range next.Agents {
		if info == nil {
			err := fmt.Errorf("restart state for agent %q is null", agentID)
			rt.loadErr = err
			return err
		}
	}
	rt.state = next
	rt.loadErr = nil
	return nil
}

// Save persists the restart state to disk.
func (rt *RestartTracker) Save() error {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.saveLocked()
}

func (rt *RestartTracker) saveLocked() error {
	if rt.loadErr != nil {
		return fmt.Errorf("restart state unavailable after failed load: %w", rt.loadErr)
	}
	if err := os.MkdirAll(filepath.Dir(rt.restartStateFile()), 0o700); err != nil {
		return err
	}
	return atomicfile.WriteJSONWithPerm(rt.restartStateFile(), rt.state, 0o600)
}

// CanRestart checks if an agent can be restarted (not in backoff).
func (rt *RestartTracker) CanRestart(agentID string) bool {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	if rt.loadErr != nil {
		return false
	}
	info, exists := rt.state.Agents[agentID]
	if !exists {
		return true
	}

	// Check if in crash loop
	if !info.CrashLoopSince.IsZero() {
		return false
	}

	// Check backoff period
	return !rt.now().Before(info.BackoffUntil)
}

// RecordRestart records a restart attempt and calculates next backoff. It
// returns true only when this call newly latches a crash loop, allowing the
// caller to emit one deduplicated alert.
func (rt *RestartTracker) RecordRestart(agentID string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	now := rt.now()
	info, exists := rt.state.Agents[agentID]
	if !exists {
		info = &AgentRestartInfo{}
		rt.state.Agents[agentID] = info
	}

	// Check if previous restart was stable (long ago)
	// A crash-loop latch is persistent: the 30-minute local probes must not
	// clear it merely because they are intentionally far apart.
	if info.CrashLoopSince.IsZero() &&
		!info.LastRestart.IsZero() &&
		now.Sub(info.LastRestart) > rt.config.StabilityPeriod {
		// Reset backoff - agent was stable
		info.RestartCount = 0
		info.RecentRestarts = nil
	}

	info.LastRestart = now
	info.RestartCount++
	if info.CrashLoopSince.IsZero() {
		cutoff := now.Add(-rt.config.CrashLoopWindow)
		recent := info.RecentRestarts[:0]
		for _, restart := range info.RecentRestarts {
			if restart.After(cutoff) {
				recent = append(recent, restart)
			}
		}
		info.RecentRestarts = append(recent, now)
	}

	// Calculate backoff with exponential increase
	backoffDuration := rt.config.InitialBackoff
	for i := 1; i < info.RestartCount && backoffDuration < rt.config.MaxBackoff; i++ {
		backoffDuration = time.Duration(float64(backoffDuration) * rt.config.BackoffMultiplier)
	}
	if backoffDuration > rt.config.MaxBackoff {
		backoffDuration = rt.config.MaxBackoff
	}
	info.BackoffUntil = now.Add(backoffDuration)

	// Check for crash loop
	newlyLatched := false
	if info.CrashLoopSince.IsZero() && len(info.RecentRestarts) >= rt.config.CrashLoopCount {
		info.CrashLoopSince = now
		info.CrashLoopAlertPending = true
		newlyLatched = true
	}
	return newlyLatched
}

// RecordPause records that an agent is paused due to a transient external
// limit (e.g., Claude usage-limit reached) rather than a crashing.
//
// Applies the fixed PauseBackoff delay without escalating, and does NOT
// increment RestartCount or set CrashLoopSince. Use this when the agent's
// failure is a rate-limit response — restarts under these conditions are
// not a sign of instability and should not count against the crash-loop
// fault budget.
func (rt *RestartTracker) RecordPause(agentID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	now := rt.now()
	info, exists := rt.state.Agents[agentID]
	if !exists {
		info = &AgentRestartInfo{}
		rt.state.Agents[agentID] = info
	}

	info.LastRestart = now
	info.BackoffUntil = now.Add(rt.config.PauseBackoff)
}

// RecordSuccess records that an agent is running successfully.
// Call this periodically for healthy agents to reset their backoff.
func (rt *RestartTracker) RecordSuccess(agentID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	info, exists := rt.state.Agents[agentID]
	if !exists {
		return
	}

	// If agent has been stable for the stability period, reset tracking
	if rt.now().Sub(info.LastRestart) >= rt.config.StabilityPeriod {
		info.RestartCount = 0
		info.RecentRestarts = nil
		info.CrashLoopSince = time.Time{}
		info.BackoffUntil = time.Time{}
		info.LastWatchdogProbe = time.Time{}
	}
}

// IsInCrashLoop returns true if the agent is detected as crash-looping.
func (rt *RestartTracker) IsInCrashLoop(agentID string) bool {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	// Unknown persisted state is an authorization failure, including for
	// hosted Boot suppression paths that consult only the crash-loop latch.
	if rt.loadErr != nil {
		return true
	}
	info, exists := rt.state.Agents[agentID]
	if !exists {
		return false
	}
	return !info.CrashLoopSince.IsZero()
}

// GetBackoffRemaining returns how long until the agent can be restarted.
func (rt *RestartTracker) GetBackoffRemaining(agentID string) time.Duration {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	info, exists := rt.state.Agents[agentID]
	if !exists {
		return 0
	}

	remaining := info.BackoffUntil.Sub(rt.now())
	if remaining < 0 {
		return 0
	}
	return remaining
}

// TakeWatchdogProbe atomically consumes one local-only recovery probe for a
// crash-looping agent when its cooldown has elapsed. It never clears the latch
// and carries no model or hosted-promotion semantics.
func (rt *RestartTracker) TakeWatchdogProbe(agentID string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	if rt.loadErr != nil {
		return false
	}
	info, exists := rt.state.Agents[agentID]
	if !exists || info.CrashLoopSince.IsZero() {
		return false
	}

	last := info.LastWatchdogProbe
	if last.IsZero() {
		last = info.CrashLoopSince
	}
	now := rt.now()
	if now.Before(last.Add(rt.config.CrashLoopProbeInterval)) {
		return false
	}
	info.LastWatchdogProbe = now
	return true
}

// TakeWatchdogProbePersisted consumes a probe only if the updated gate can be
// durably saved. Persistence failure rolls the in-memory timestamp back so the
// daemon fails closed without starting a local probe.
func (rt *RestartTracker) TakeWatchdogProbePersisted(agentID string) (bool, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	if rt.loadErr != nil {
		return false, fmt.Errorf("restart state unavailable after failed load: %w", rt.loadErr)
	}
	info, exists := rt.state.Agents[agentID]
	if !exists || info.CrashLoopSince.IsZero() {
		return false, nil
	}
	last := info.LastWatchdogProbe
	if last.IsZero() {
		last = info.CrashLoopSince
	}
	now := rt.now()
	if now.Before(last.Add(rt.config.CrashLoopProbeInterval)) {
		return false, nil
	}
	previous := info.LastWatchdogProbe
	info.LastWatchdogProbe = now
	if err := rt.saveLocked(); err != nil {
		info.LastWatchdogProbe = previous
		return false, err
	}
	return true, nil
}

func (rt *RestartTracker) CrashLoopAlertPending(agentID string) bool {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	info := rt.state.Agents[agentID]
	return info != nil && info.CrashLoopAlertPending
}

func (rt *RestartTracker) MarkCrashLoopAlertDeliveredPersisted(agentID string) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	info := rt.state.Agents[agentID]
	if info == nil || !info.CrashLoopAlertPending {
		return nil
	}
	info.CrashLoopAlertPending = false
	if err := rt.saveLocked(); err != nil {
		info.CrashLoopAlertPending = true
		return err
	}
	return nil
}

// ClearCrashLoop manually clears the crash loop state for an agent.
func (rt *RestartTracker) ClearCrashLoop(agentID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	info, exists := rt.state.Agents[agentID]
	if exists {
		info.CrashLoopSince = time.Time{}
		info.RestartCount = 0
		info.RecentRestarts = nil
		info.BackoffUntil = time.Time{}
		info.LastWatchdogProbe = time.Time{}
		info.CrashLoopAlertPending = false
	}
}

// ClearAgentBackoff clears the crash loop and backoff state for an agent on disk.
// Used by 'gt daemon clear-backoff' to reset an agent stuck in crash loop.
// The daemon reloads this on next heartbeat (or immediately on SIGUSR2).
func ClearAgentBackoff(townRoot, agentID string) error {
	rt := NewRestartTracker(townRoot, RestartTrackerConfig{})
	if err := rt.Load(); err != nil {
		return fmt.Errorf("loading restart state: %w", err)
	}
	rt.ClearCrashLoop(agentID)
	return rt.Save()
}
