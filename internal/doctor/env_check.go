package doctor

import (
	"fmt"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/session"
	"github.com/jonbaldie/gastown/internal/tmux"
)

// SessionEnvReader abstracts tmux session environment reads for testing.
type SessionEnvReader interface {
	ListSessions() ([]string, error)
	GetAllEnvironment(session string) (map[string]string, error)
}

// SessionEnvWriter abstracts tmux session environment writes for testing.
type SessionEnvWriter interface {
	SetEnvironment(session, key, value string) error
}

// SessionEnvAccessor combines read and write access to tmux session environments.
type SessionEnvAccessor interface {
	SessionEnvReader
	SessionEnvWriter
}

// tmuxEnvReaderWriter wraps real tmux operations for both reading and writing.
type tmuxEnvReaderWriter struct {
	t *tmux.Tmux
}

func (r *tmuxEnvReaderWriter) ListSessions() ([]string, error) {
	return r.t.ListSessions()
}

func (r *tmuxEnvReaderWriter) GetAllEnvironment(session string) (map[string]string, error) {
	return r.t.GetAllEnvironment(session)
}

func (r *tmuxEnvReaderWriter) SetEnvironment(session, key, value string) error {
	return r.t.SetEnvironment(session, key, value)
}

// EnvVarsCheck verifies that tmux session environment variables match expected values.
type EnvVarsCheck struct {
	FixableCheck
	reader   SessionEnvReader   // nil means use real tmux
	accessor SessionEnvAccessor // non-nil when Fix() support is needed
}

// NewEnvVarsCheck creates a new env vars check.
func NewEnvVarsCheck() *EnvVarsCheck {
	return &EnvVarsCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "env-vars",
				CheckDescription: "Verify tmux session environment variables match expected values",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// NewEnvVarsCheckWithReader creates a check with a custom reader (for testing Run()).
func NewEnvVarsCheckWithReader(reader SessionEnvReader) *EnvVarsCheck {
	c := NewEnvVarsCheck()
	c.reader = reader
	return c
}

// NewEnvVarsCheckWithAccessor creates a check with a custom accessor (for testing Fix()).
func NewEnvVarsCheckWithAccessor(accessor SessionEnvAccessor) *EnvVarsCheck {
	c := NewEnvVarsCheck()
	c.accessor = accessor
	c.reader = accessor
	return c
}

// Run checks environment variables for all Gas Town sessions.
func (c *EnvVarsCheck) Run(ctx *CheckContext) *CheckResult {
	reader := c.reader
	if reader == nil {
		reader = &tmuxEnvReaderWriter{t: tmux.NewTmux()}
	}

	sessions, err := reader.ListSessions()
	if err != nil {
		// No tmux server - treat as success (valid when Gas Town is down)
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No tmux sessions running",
		}
	}

	// Filter to Gas Town sessions only (known rig prefixes and hq-*)
	var gtSessions []string
	for _, sess := range sessions {
		if session.IsKnownSession(sess) {
			gtSessions = append(gtSessions, sess)
		}
	}

	if len(gtSessions) == 0 {
		// No Gas Town sessions - treat as success (valid when Gas Town is down)
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No Gas Town sessions running",
		}
	}

	var mismatches []string
	var beadsDirWarnings []string
	checkedCount := 0

	for _, sess := range gtSessions {
		identity, err := session.ParseSessionName(sess)
		if err != nil {
			// Skip unparseable sessions
			continue
		}

		// Determine role for AgentEnv lookup.
		// Boot watchdog is parsed as deacon with name "boot", but AgentEnv
		// uses "boot" as a distinct role for env var generation.
		role := string(identity.Role)
		if identity.Role == session.RoleDeacon && identity.Name == "boot" {
			role = "boot"
		}

		// Get expected env vars based on role
		expected := config.AgentEnv(config.AgentEnvConfig{
			Role:      role,
			Rig:       identity.Rig,
			AgentName: identity.Name,
			TownRoot:  ctx.TownRoot,
		})

		// Get actual tmux env vars
		actual, err := reader.GetAllEnvironment(sess)
		if err != nil {
			mismatches = append(mismatches, fmt.Sprintf("%s: could not read env vars: %v", sess, err))
			continue
		}

		checkedCount++

		// Compare each expected var
		for key, expectedVal := range expected {
			actualVal, exists := actual[key]
			if !exists && expectedVal != "" {
				// Only flag missing vars when the expected value is non-empty.
				// An absent var has the same effect as an empty one (e.g. CLAUDECODE=""
				// prevents nested session detection, but so does CLAUDECODE being unset).
				mismatches = append(mismatches, fmt.Sprintf("%s: missing %s (expected %q)", sess, key, expectedVal))
			} else if exists && actualVal != expectedVal {
				mismatches = append(mismatches, fmt.Sprintf("%s: %s=%q (expected %q)", sess, key, actualVal, expectedVal))
			}
		}

		// Check for BEADS_DIR - this breaks routing-based lookups
		if beadsDir, exists := actual["BEADS_DIR"]; exists && beadsDir != "" {
			beadsDirWarnings = append(beadsDirWarnings, fmt.Sprintf("%s: BEADS_DIR=%q (breaks prefix routing)", sess, beadsDir))
		}
	}

	// Check for BEADS_DIR issues first (higher priority warning)
	if len(beadsDirWarnings) > 0 {
		details := beadsDirWarnings
		if len(mismatches) > 0 {
			details = append(details, "", "Other env var issues:")
			details = append(details, mismatches...)
		}
		details = append(details,
			"",
			"BEADS_DIR overrides prefix-based routing and breaks multi-rig lookups.",
		)
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("Found BEADS_DIR set in %d session(s)", len(beadsDirWarnings)),
			Details: details,
			FixHint: "Remove BEADS_DIR from session environment: gt shutdown && gt up",
		}
	}

	if len(mismatches) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("All %d session(s) have correct environment variables", checkedCount),
		}
	}

	// Add explanation about needing restart
	details := append(mismatches,
		"",
		"Note: Mismatched session env vars won't affect running Claude until sessions restart.",
	)

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("Found %d env var mismatch(es) across %d session(s)", len(mismatches), checkedCount),
		Details: details,
		FixHint: "Run 'gt doctor --fix' to apply missing env vars in-place, or 'gt shutdown && gt up' to restart",
	}
}

// Fix applies missing or incorrect env vars to all Gas Town tmux sessions in-place.
// The running Claude process is unaffected (it already has env vars from startup);
// this updates the tmux session store so future processes and gt doctor agree.
func (c *EnvVarsCheck) Fix(ctx *CheckContext) error {
	accessor := c.accessor
	if accessor == nil {
		accessor = &tmuxEnvReaderWriter{t: tmux.NewTmux()}
	}

	sessions, err := accessor.ListSessions()
	if err != nil {
		// No tmux server — nothing to fix.
		return nil
	}

	for _, sess := range sessions {
		if !session.IsKnownSession(sess) {
			continue
		}
		identity, err := session.ParseSessionName(sess)
		if err != nil {
			continue
		}

		role := string(identity.Role)
		if identity.Role == session.RoleDeacon && identity.Name == "boot" {
			role = "boot"
		}

		expected := config.AgentEnv(config.AgentEnvConfig{
			Role:      role,
			Rig:       identity.Rig,
			AgentName: identity.Name,
			TownRoot:  ctx.TownRoot,
		})

		actual, err := accessor.GetAllEnvironment(sess)
		if err != nil {
			continue
		}

		for key, expectedVal := range expected {
			actualVal, exists := actual[key]
			if !exists || actualVal != expectedVal {
				_ = accessor.SetEnvironment(sess, key, expectedVal)
			}
		}
	}
	return nil
}
