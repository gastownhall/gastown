// Package deacon provides the Deacon agent infrastructure.
package deacon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/util"
)

// Default parameters for re-dispatch rate-limiting.
// Configurable via operational.deacon.max_redispatches and
// operational.deacon.redispatch_cooldown in settings/config.json.
const (
	// DefaultMaxRedispatches is the number of times a bead can be re-dispatched
	// before escalating to Mayor instead of re-slinging.
	DefaultMaxRedispatches = 3

	// DefaultRedispatchCooldown is the minimum time between re-dispatches of
	// the same bead. Prevents thrashing when a bead keeps killing polecats.
	DefaultRedispatchCooldown = 5 * time.Minute
)

// RedispatchState tracks re-dispatch attempts for recovered beads.
// Persisted to deacon/redispatch-state.json.
type RedispatchState struct {
	// Beads maps bead ID to their re-dispatch tracking state.
	Beads map[string]*BeadRedispatchState `json:"beads"`

	// LastUpdated is when this state was last written.
	LastUpdated time.Time `json:"last_updated"`
}

// BeadRedispatchState tracks the re-dispatch history for a single bead.
type BeadRedispatchState struct {
	// BeadID is the bead identifier.
	BeadID string `json:"bead_id"`

	// AttemptCount is total number of re-dispatch attempts for this bead.
	AttemptCount int `json:"attempt_count"`

	// LastAttemptTime is when the last re-dispatch was attempted.
	LastAttemptTime time.Time `json:"last_attempt_time,omitempty"`

	// LastRig is the rig where the last re-dispatch was sent.
	LastRig string `json:"last_rig,omitempty"`

	// LastAgent is the agent alias used for the last re-dispatch (empty = rig default).
	LastAgent string `json:"last_agent,omitempty"`

	// Escalated is true if this bead has been escalated to Mayor.
	Escalated bool `json:"escalated,omitempty"`

	// EscalatedAt is when the bead was escalated.
	EscalatedAt time.Time `json:"escalated_at,omitempty"`
}

// ModelEscalationRule defines a single agent promotion rule.
type ModelEscalationRule struct {
	// FromAgent is the agent alias that triggers this rule (e.g., "claude-sonnet").
	FromAgent string `json:"from_agent"`

	// ToAgent is the agent alias to promote to (e.g., "claude" for Opus).
	ToAgent string `json:"to_agent"`

	// PromoteAfterFailures is the number of total failures (initial + re-dispatches)
	// required before promoting to ToAgent.
	PromoteAfterFailures int `json:"promote_after_failures"`

	// Comment is an optional human-readable description of this rule.
	Comment string `json:"comment,omitempty"`
}

// ModelEscalationConfig defines per-rig agent promotion rules for re-dispatch.
// Loaded from <rig>/refinery/rig/.gastown/model-escalation.json.
type ModelEscalationConfig struct {
	Type              string                    `json:"type"`
	Version           int                       `json:"version"`
	Enabled           bool                      `json:"enabled"`
	Description       string                    `json:"description,omitempty"`
	Rules             []ModelEscalationRule     `json:"rules"`
	ValidationFailure *ValidationRecoveryConfig `json:"validation_failure,omitempty"`
	MaxTotalAttempts  int                       `json:"max_total_attempts,omitempty"`
	Fallback          string                    `json:"fallback,omitempty"`
}

// ModelEscalationConfigPath is the path within a rig project directory where
// the model escalation config is stored.
const ModelEscalationConfigPath = ".gastown/model-escalation.json"

// RedispatchResult describes the outcome of a re-dispatch attempt.
type RedispatchResult struct {
	BeadID    string `json:"bead_id"`
	Action    string `json:"action"` // "redispatched", "cooldown", "escalated", "error"
	TargetRig string `json:"target_rig,omitempty"`
	Attempts  int    `json:"attempts"`
	Message   string `json:"message,omitempty"`
	Error     error  `json:"error,omitempty"`
}

// RedispatchStateFile returns the path to the re-dispatch state file.
func RedispatchStateFile(townRoot string) string {
	return filepath.Join(townRoot, "deacon", "redispatch-state.json")
}

// LoadRedispatchState loads the re-dispatch state from disk.
// Returns empty state if file doesn't exist.
func LoadRedispatchState(townRoot string) (*RedispatchState, error) {
	stateFile := RedispatchStateFile(townRoot)

	data, err := os.ReadFile(stateFile) //nolint:gosec // G304: path is constructed from trusted townRoot
	if err != nil {
		if os.IsNotExist(err) {
			return &RedispatchState{
				Beads: make(map[string]*BeadRedispatchState),
			}, nil
		}
		return nil, fmt.Errorf("reading redispatch state: %w", err)
	}

	var state RedispatchState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing redispatch state: %w", err)
	}

	if state.Beads == nil {
		state.Beads = make(map[string]*BeadRedispatchState)
	}

	return &state, nil
}

// SaveRedispatchState saves the re-dispatch state to disk.
func SaveRedispatchState(townRoot string, state *RedispatchState) error {
	stateFile := RedispatchStateFile(townRoot)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(stateFile), 0755); err != nil {
		return fmt.Errorf("creating deacon directory: %w", err)
	}

	state.LastUpdated = time.Now().UTC()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling redispatch state: %w", err)
	}

	return os.WriteFile(stateFile, data, 0600)
}

// GetBeadState returns the re-dispatch state for a bead, creating if needed.
func (s *RedispatchState) GetBeadState(beadID string) *BeadRedispatchState {
	if s.Beads == nil {
		s.Beads = make(map[string]*BeadRedispatchState)
	}

	state, ok := s.Beads[beadID]
	if !ok {
		state = &BeadRedispatchState{BeadID: beadID}
		s.Beads[beadID] = state
	}
	return state
}

// IsInCooldown returns true if the bead was recently re-dispatched.
func (s *BeadRedispatchState) IsInCooldown(cooldown time.Duration) bool {
	if s.LastAttemptTime.IsZero() {
		return false
	}
	return time.Since(s.LastAttemptTime) < cooldown
}

// CooldownRemaining returns how long until cooldown expires.
func (s *BeadRedispatchState) CooldownRemaining(cooldown time.Duration) time.Duration {
	if s.LastAttemptTime.IsZero() {
		return 0
	}
	remaining := cooldown - time.Since(s.LastAttemptTime)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// ShouldEscalate returns true if the bead has exceeded the max re-dispatch attempts.
func (s *BeadRedispatchState) ShouldEscalate(maxAttempts int) bool {
	return s.AttemptCount >= maxAttempts
}

// RecordAttempt records a re-dispatch attempt for the bead.
func (s *BeadRedispatchState) RecordAttempt(rig string) {
	s.AttemptCount++
	s.LastAttemptTime = time.Now().UTC()
	s.LastRig = rig
}

// RecordEscalation records that the bead was escalated to Mayor.
func (s *BeadRedispatchState) RecordEscalation() {
	s.Escalated = true
	s.EscalatedAt = time.Now().UTC()
}

// Redispatch handles a RECOVERED_BEAD message by re-slinging the bead to an
// available polecat, or escalating to Mayor if the bead has failed too many times.
//
// Parameters:
//   - townRoot: the Gas Town workspace root
//   - beadID: the recovered bead to re-dispatch
//   - sourceRig: the rig from which the bead was recovered (empty = auto-detect from prefix)
//   - maxAttempts: max re-dispatches before escalating (0 = use default)
//   - cooldown: min time between re-dispatches (0 = use default)
func Redispatch(townRoot, beadID, sourceRig string, maxAttempts int, cooldown time.Duration) *RedispatchResult {
	result := &RedispatchResult{BeadID: beadID}

	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxRedispatches
	}
	if cooldown <= 0 {
		cooldown = DefaultRedispatchCooldown
	}

	// Load state
	state, err := LoadRedispatchState(townRoot)
	if err != nil {
		result.Action = "error"
		result.Error = fmt.Errorf("loading redispatch state: %w", err)
		return result
	}

	beadState := state.GetBeadState(beadID)
	result.Attempts = beadState.AttemptCount

	// Check if already escalated
	if beadState.Escalated {
		result.Action = "already-escalated"
		result.Message = fmt.Sprintf("bead already escalated to Mayor at %s", beadState.EscalatedAt.Format(time.RFC3339))
		return result
	}

	// Check cooldown
	if beadState.IsInCooldown(cooldown) {
		remaining := beadState.CooldownRemaining(cooldown)
		result.Action = "cooldown"
		result.Message = fmt.Sprintf("in cooldown (remaining: %s)", remaining.Round(time.Second))
		return result
	}

	// Check if we should escalate instead of re-dispatching
	if beadState.ShouldEscalate(maxAttempts) {
		result.Action = "escalated"
		result.Attempts = beadState.AttemptCount

		// Escalate to Mayor
		err := escalateToMayor(townRoot, beadID, beadState)
		if err != nil {
			result.Error = fmt.Errorf("escalating to mayor: %w", err)
			result.Message = fmt.Sprintf("failed to escalate after %d attempts: %v", beadState.AttemptCount, err)
		} else {
			beadState.RecordEscalation()
			result.Message = fmt.Sprintf("escalated to Mayor after %d failed re-dispatches", beadState.AttemptCount)
		}

		// Save state regardless of escalation success
		if saveErr := SaveRedispatchState(townRoot, state); saveErr != nil {
			// Log but don't fail - escalation mail was already sent
			result.Message += fmt.Sprintf(" (warning: state save failed: %v)", saveErr)
		}

		return result
	}

	// Determine target rig
	targetRig := sourceRig
	if targetRig == "" {
		targetRig = resolveRigFromBead(townRoot, beadID)
	}
	if targetRig == "" {
		result.Action = "error"
		result.Error = fmt.Errorf("cannot determine target rig for bead %s", beadID)
		return result
	}
	result.TargetRig = targetRig

	// Verify bead is still open (not already claimed or closed).
	// Only proceed when status is explicitly "open". Empty status (query
	// failure) is treated as "not open" to avoid re-dispatching closed
	// beads when bd show fails. (gt-sy8)
	beadStatus := getBeadStatusForRedispatch(townRoot, beadID)
	if beadStatus != "open" {
		result.Action = "skipped"
		if beadStatus == "" {
			result.Message = "could not determine bead status (treating as not open)"
		} else {
			result.Message = fmt.Sprintf("bead status is %q (expected open)", beadStatus)
		}
		return result
	}

	// Determine the agent that failed. After a re-dispatch, LastAgent is the
	// durable source of truth. On the first recovery, use the configured polecat
	// agent for the rig. This lets from_agent rules prevent a hosted worker from
	// being sent back to the hosted relief valve indefinitely.
	fromAgent := beadState.LastAgent
	if fromAgent == "" {
		fromAgent = configuredPolecatAgent(townRoot, targetRig)
	}

	// Determine agent override from model escalation config (if any).
	escalationAgent, matchedRule := resolveAgentDecision(townRoot, targetRig, beadState, fromAgent)
	if fromAgent != "" && !matchedRule {
		result.Action = "escalated"
		result.Message = fmt.Sprintf("no crash-recovery rule matches failed agent %q; escalated to Mayor", fromAgent)
		if err := escalateToMayor(townRoot, beadID, beadState); err != nil {
			result.Error = fmt.Errorf("escalating unmatched agent to mayor: %w", err)
		} else {
			beadState.RecordEscalation()
		}
		_ = SaveRedispatchState(townRoot, state)
		return result
	}

	// Re-dispatch via gt sling
	err = slingBead(townRoot, beadID, targetRig, escalationAgent)
	if err != nil {
		result.Action = "error"
		result.Error = fmt.Errorf("slinging bead to %s: %w", targetRig, err)

		// Record the failed attempt
		beadState.LastAgent = escalationAgent
		beadState.RecordAttempt(targetRig)
		_ = SaveRedispatchState(townRoot, state)

		return result
	}

	// Record successful dispatch
	beadState.LastAgent = escalationAgent
	beadState.RecordAttempt(targetRig)
	result.Action = "redispatched"
	result.Attempts = beadState.AttemptCount
	if escalationAgent != "" {
		result.Message = fmt.Sprintf("re-dispatched to %s with agent %q (attempt %d/%d)", targetRig, escalationAgent, beadState.AttemptCount, maxAttempts)
	} else {
		result.Message = fmt.Sprintf("re-dispatched to %s (attempt %d/%d)", targetRig, beadState.AttemptCount, maxAttempts)
	}

	// Save state
	if saveErr := SaveRedispatchState(townRoot, state); saveErr != nil {
		result.Message += fmt.Sprintf(" (warning: state save failed: %v)", saveErr)
	}

	return result
}

// PruneRedispatchState removes entries for beads that are no longer open.
// Call periodically to prevent unbounded state growth.
func PruneRedispatchState(townRoot string) (int, error) {
	state, err := LoadRedispatchState(townRoot)
	if err != nil {
		return 0, err
	}

	pruned := 0
	for beadID := range state.Beads {
		status := getBeadStatusForRedispatch(townRoot, beadID)
		// Remove entries for beads that are closed, or that we can't find
		if status == "closed" || status == "" {
			delete(state.Beads, beadID)
			pruned++
		}
	}

	if pruned > 0 {
		if err := SaveRedispatchState(townRoot, state); err != nil {
			return pruned, err
		}
	}

	return pruned, nil
}

// resolveRigFromBead determines the rig that owns a bead based on its prefix.
func resolveRigFromBead(townRoot, beadID string) string {
	prefix := beads.ExtractPrefix(beadID)
	if prefix == "" {
		return ""
	}
	return beads.GetRigNameForPrefix(townRoot, prefix)
}

// getBeadStatusForRedispatch returns the current status of a bead.
func getBeadStatusForRedispatch(townRoot, beadID string) string {
	cmd := beads.Command(townRoot, townBeadsDir(townRoot), beads.ReadOnlyRouting, "show", beadID, "--json")

	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	var issues []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(output, &issues); err != nil || len(issues) == 0 {
		return ""
	}
	return issues[0].Status
}

// LoadModelEscalationConfig loads model escalation rules from a rig project directory.
// Returns nil, nil if the file does not exist (no escalation configured).
func LoadModelEscalationConfig(rigProjectDir string) (*ModelEscalationConfig, error) {
	path := filepath.Join(rigProjectDir, ModelEscalationConfigPath)
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed from trusted rigProjectDir
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading model escalation config: %w", err)
	}

	var cfg ModelEscalationConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing model escalation config %s: %w", path, err)
	}
	return &cfg, nil
}

// resolveAgentForRedispatch determines which agent alias to use when re-dispatching
// a bead, based on the rig's model escalation config.
//
// The "total failures" seen by this bead is beadState.AttemptCount (prior re-dispatches)
// plus 1 (the original dispatch failure that triggered this re-dispatch).
// Rules fire when totalFailures >= rule.PromoteAfterFailures.
//
// Returns "" (empty) if no escalation is configured or no rule applies — the caller
// should omit the --agent flag and let the rig use its default.
func resolveAgentForRedispatch(townRoot, targetRig string, beadState *BeadRedispatchState, fromAgent ...string) string {
	source := ""
	if len(fromAgent) > 0 {
		source = fromAgent[0]
	}
	agent, _ := resolveAgentDecision(townRoot, targetRig, beadState, source)
	return agent
}

// resolveAgentDecision returns the target agent and whether an escalation rule
// matched. fromAgent is enforced when present; an empty from_agent in config is
// a wildcard for backwards compatibility.
func resolveAgentDecision(townRoot, targetRig string, beadState *BeadRedispatchState, fromAgent string) (string, bool) {
	// Rig project dir is <townRoot>/<rig>/refinery/rig
	rigProjectDir := filepath.Join(townRoot, targetRig, "refinery", "rig")

	cfg, err := LoadModelEscalationConfig(rigProjectDir)
	if err != nil || cfg == nil || !cfg.Enabled || len(cfg.Rules) == 0 {
		return "", true
	}

	// Total failures = prior re-dispatch attempts + 1 (initial failure that triggered
	// this re-dispatch call). AttemptCount is recorded AFTER each sling, so on entry
	// it reflects the number of completed re-dispatches, not the current one.
	totalFailures := beadState.AttemptCount + 1

	matchedSource := false
	for _, rule := range cfg.Rules {
		if rule.FromAgent != "" && fromAgent != "" && rule.FromAgent != fromAgent {
			continue
		}
		matchedSource = true
		if totalFailures >= rule.PromoteAfterFailures {
			return rule.ToAgent, true
		}
	}
	return "", matchedSource
}

// configuredPolecatAgent returns the rig's effective local worker alias when it
// is explicitly configured. Validation setups pin this value so a first crash
// can be matched against from_agent without relying on a dead tmux session.
func configuredPolecatAgent(townRoot, rigName string) string {
	settings, err := config.LoadRigSettings(config.RigSettingsPath(filepath.Join(townRoot, rigName)))
	if err != nil || settings == nil {
		return ""
	}
	if agent := strings.TrimSpace(settings.RoleAgents["polecat"]); agent != "" {
		return agent
	}
	return strings.TrimSpace(settings.Agent)
}

// slingBead dispatches a bead to a rig via gt sling.
// If agent is non-empty, passes --agent <agent> to override the rig's default.
func slingBead(townRoot, beadID, rig, agent string) error {
	args := []string{"sling", beadID, rig, "--force", "--no-convoy"}
	if agent != "" {
		args = append(args, "--agent", agent)
	}
	cmd := exec.Command("gt", args...)
	cmd.Dir = townRoot
	cmd.Env = deaconMutationRoutingEnv(townRoot)
	util.SetDetachedProcessGroup(cmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// escalateToMayor sends an escalation mail to the Mayor about a repeatedly-failing bead.
func escalateToMayor(townRoot, beadID string, beadState *BeadRedispatchState) error {
	subject := fmt.Sprintf("REDISPATCH_FAILED: %s (%d attempts)", beadID, beadState.AttemptCount)
	body := fmt.Sprintf(`Bead %s has been recovered and re-dispatched %d times but keeps failing.

Bead: %s
Attempts: %d
Last Rig: %s
Last Attempt: %s

This bead may have a systemic issue (e.g., causes polecat crashes).
Please investigate and either:
1. Fix the underlying issue and re-sling manually
2. Close/deprioritize the bead if it's not actionable
3. Increase the re-dispatch limit if the failures were transient`,
		beadID,
		beadState.AttemptCount,
		beadID,
		beadState.AttemptCount,
		beadState.LastRig,
		beadState.LastAttemptTime.Format(time.RFC3339),
	)

	cmd := exec.Command("gt", "mail", "send", "mayor/", "-s", subject, "-m", body)
	cmd.Dir = townRoot
	cmd.Env = deaconMutationRoutingEnv(townRoot)
	util.SetDetachedProcessGroup(cmd)
	return cmd.Run()
}

// ParseRecoveredBeadSubject extracts the bead ID from a RECOVERED_BEAD mail subject.
// Expected format: "RECOVERED_BEAD <bead-id>"
func ParseRecoveredBeadSubject(subject string) (beadID string, ok bool) {
	const prefix = "RECOVERED_BEAD "
	if !strings.HasPrefix(subject, prefix) {
		return "", false
	}
	beadID = strings.TrimSpace(strings.TrimPrefix(subject, prefix))
	if beadID == "" {
		return "", false
	}
	return beadID, true
}

// ParseRecoveredBeadBody extracts the source rig from a RECOVERED_BEAD mail body.
// Looks for "Polecat: <rig>/<name>" line.
func ParseRecoveredBeadBody(body string) (rig string) {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Polecat:") {
			polecatAddr := strings.TrimSpace(strings.TrimPrefix(line, "Polecat:"))
			parts := strings.SplitN(polecatAddr, "/", 2)
			if len(parts) >= 1 {
				return parts[0]
			}
		}
	}
	return ""
}
