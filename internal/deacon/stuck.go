// Package deacon provides the Deacon agent infrastructure.
package deacon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
)

// Default parameters for stuck-session detection.
// These are fallbacks when no config override exists.
// Configurable via operational.deacon in settings/config.json.
const (
	DefaultPingTimeout         = 30 * time.Second // How long to wait for response
	DefaultConsecutiveFailures = 3                // Failures before force-kill
	DefaultCooldown            = 5 * time.Minute  // Minimum time between force-kills
)

// StuckConfig holds configurable parameters for stuck-session detection.
type StuckConfig struct {
	PingTimeout         time.Duration `json:"ping_timeout"`
	ConsecutiveFailures int           `json:"consecutive_failures"`
	Cooldown            time.Duration `json:"cooldown"`
}

// DefaultStuckConfig returns the default stuck detection config.
func DefaultStuckConfig() *StuckConfig {
	return &StuckConfig{
		PingTimeout:         DefaultPingTimeout,
		ConsecutiveFailures: DefaultConsecutiveFailures,
		Cooldown:            DefaultCooldown,
	}
}

// LoadStuckConfig loads stuck detection config from town settings, falling
// back to compiled-in defaults. The townRoot parameter is used to locate
// the settings/config.json file.
func LoadStuckConfig(townRoot string) *StuckConfig {
	opCfg := config.LoadOperationalConfig(townRoot)
	deaconCfg := opCfg.GetDeaconConfig()
	return &StuckConfig{
		PingTimeout:         deaconCfg.PingTimeoutD(),
		ConsecutiveFailures: deaconCfg.ConsecutiveFailuresV(),
		Cooldown:            deaconCfg.CooldownD(),
	}
}

// AgentHealthState tracks the health check state for a single agent.
type AgentHealthState struct {
	// AgentID is the identifier (e.g., "gastown/polecats/max" or "deacon")
	AgentID string `json:"agent_id"`

	// LastPingTime is when we last sent a HEALTH_CHECK nudge
	LastPingTime time.Time `json:"last_ping_time,omitempty"`

	// LastResponseTime is when the agent last updated their activity
	LastResponseTime time.Time `json:"last_response_time,omitempty"`

	// ConsecutiveFailures counts how many health checks failed in a row
	ConsecutiveFailures int `json:"consecutive_failures"`

	// LastForceKillTime is when we last force-killed this agent
	LastForceKillTime time.Time `json:"last_force_kill_time,omitempty"`

	// ForceKillCount is total number of force-kills for this agent
	ForceKillCount int `json:"force_kill_count"`
}

// HealthCheckState holds health check state for all monitored agents.
type HealthCheckState struct {
	// Agents maps agent ID to their health state
	Agents map[string]*AgentHealthState `json:"agents"`

	// LastUpdated is when this state was last written
	LastUpdated time.Time `json:"last_updated"`
}

// HealthCheckStateFile returns the path to the health check state file.
func HealthCheckStateFile(townRoot string) string {
	return filepath.Join(townRoot, "deacon", "health-check-state.json")
}

// LoadHealthCheckState loads the health check state from disk.
// Returns empty state if file doesn't exist.
func LoadHealthCheckState(townRoot string) (*HealthCheckState, error) {
	stateFile := HealthCheckStateFile(townRoot)

	data, err := os.ReadFile(stateFile) //nolint:gosec // G304: path is constructed from trusted townRoot
	if err != nil {
		if os.IsNotExist(err) {
			// Return empty state
			return &HealthCheckState{
				Agents: make(map[string]*AgentHealthState),
			}, nil
		}
		return nil, fmt.Errorf("reading health check state: %w", err)
	}

	var state HealthCheckState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing health check state: %w", err)
	}

	if state.Agents == nil {
		state.Agents = make(map[string]*AgentHealthState)
	}

	return &state, nil
}

// SaveHealthCheckState saves the health check state to disk.
func SaveHealthCheckState(townRoot string, state *HealthCheckState) error {
	stateFile := HealthCheckStateFile(townRoot)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(stateFile), 0755); err != nil {
		return fmt.Errorf("creating deacon directory: %w", err)
	}

	state.LastUpdated = time.Now().UTC()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling health check state: %w", err)
	}

	return os.WriteFile(stateFile, data, 0600)
}

// GetAgentState returns the health state for an agent, creating if needed.
func (s *HealthCheckState) GetAgentState(agentID string) *AgentHealthState {
	if s.Agents == nil {
		s.Agents = make(map[string]*AgentHealthState)
	}

	state, ok := s.Agents[agentID]
	if !ok {
		state = &AgentHealthState{AgentID: agentID}
		s.Agents[agentID] = state
	}
	return state
}

// HealthCheckResult represents the outcome of a health check.
type HealthCheckResult struct {
	AgentID             string        `json:"agent_id"`
	Responded           bool          `json:"responded"`
	ResponseTime        time.Duration `json:"response_time,omitempty"`
	ConsecutiveFailures int           `json:"consecutive_failures"`
	ShouldForceKill     bool          `json:"should_force_kill"`
	InCooldown          bool          `json:"in_cooldown"`
	CooldownRemaining   time.Duration `json:"cooldown_remaining,omitempty"`
}

// Common errors for stuck-session detection.
var (
	ErrAgentInCooldown = errors.New("agent is in cooldown period after recent force-kill")
	ErrAgentNotFound   = errors.New("agent not found or session doesn't exist")
	ErrAgentResponsive = errors.New("agent is responsive, no action needed")
)

// RecordPing records that a health check ping was sent to an agent.
func (s *AgentHealthState) RecordPing() {
	s.LastPingTime = time.Now().UTC()
}

// RecordResponse records that an agent responded to a health check.
// This resets the consecutive failure counter.
func (s *AgentHealthState) RecordResponse() {
	s.LastResponseTime = time.Now().UTC()
	s.ConsecutiveFailures = 0
}

// RecordFailure records that an agent failed to respond to a health check.
func (s *AgentHealthState) RecordFailure() {
	s.ConsecutiveFailures++
}

// RecordForceKill records that an agent was force-killed.
func (s *AgentHealthState) RecordForceKill() {
	s.LastForceKillTime = time.Now().UTC()
	s.ForceKillCount++
	s.ConsecutiveFailures = 0 // Reset after kill
}

// IsInCooldown returns true if the agent was recently force-killed.
func (s *AgentHealthState) IsInCooldown(cooldown time.Duration) bool {
	if s.LastForceKillTime.IsZero() {
		return false
	}
	return time.Since(s.LastForceKillTime) < cooldown
}

// CooldownRemaining returns how long until cooldown expires.
func (s *AgentHealthState) CooldownRemaining(cooldown time.Duration) time.Duration {
	if s.LastForceKillTime.IsZero() {
		return 0
	}
	remaining := cooldown - time.Since(s.LastForceKillTime)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// ShouldForceKill returns true if the agent has exceeded the failure threshold.
func (s *AgentHealthState) ShouldForceKill(threshold int) bool {
	return s.ConsecutiveFailures >= threshold
}
