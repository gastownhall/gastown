package doctor

import (
	"bytes"
	"fmt"
	"github.com/jonbaldie/gastown/internal/beads"
	"path/filepath"
	"strings"
)

// RoutingModeCheck detects when beads routing.mode is set to "auto", which can
// cause issues to be unexpectedly routed to ~/.beads-planning instead of the
// local .beads directory. This happens because auto mode uses git remote URL
// to detect user role, and non-SSH URLs are interpreted as "contributor" mode.
//
// See: https://github.com/steveyegge/beads/issues/1165
type RoutingModeCheck struct {
	FixableCheck
}

// NewRoutingModeCheck creates a new routing mode check.
func NewRoutingModeCheck() *RoutingModeCheck {
	return &RoutingModeCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "routing-mode",
				CheckDescription: "Check beads routing.mode is explicit (prevents .beads-planning routing)",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// Run checks if routing.mode is set to "explicit".
func (c *RoutingModeCheck) Run(ctx *CheckContext) *CheckResult {
	// Check town-level beads config
	townBeadsDir := filepath.Join(ctx.TownRoot, ".beads")
	result := c.checkRoutingMode(townBeadsDir, "town")
	if result.Status != StatusOK {
		return result
	}

	// Also check rig-level beads if specified
	if ctx.RigName != "" {
		rigBeadsDir := filepath.Join(ctx.RigPath(), ".beads")
		rigResult := c.checkRoutingMode(rigBeadsDir, fmt.Sprintf("rig '%s'", ctx.RigName))
		if rigResult.Status != StatusOK {
			return rigResult
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "Beads routing.mode is explicit",
	}
}

// checkRoutingMode checks the routing mode in a specific beads directory.
func (c *RoutingModeCheck) checkRoutingMode(beadsDir, location string) *CheckResult {
	// Run bd config get routing.mode
	cmd := beads.Spawn("config", "get", "routing.mode")
	cmd.Dir = filepath.Dir(beadsDir)
	cmd.Env = append(cmd.Environ(), "BEADS_DIR="+beadsDir)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// If the config key doesn't exist, that means it defaults to "auto"
		if strings.Contains(stderr.String(), "not found") || strings.Contains(stderr.String(), "not set") {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusWarning,
				Message: fmt.Sprintf("routing.mode not set at %s (defaults to auto)", location),
				Details: []string{
					"Auto routing mode uses git remote URL to detect user role",
					"Non-SSH URLs (HTTPS or file paths) trigger routing to ~/.beads-planning",
					"This causes mail and issues to be stored in the wrong location",
					"See: https://github.com/steveyegge/beads/issues/1165",
				},
				FixHint: "Run 'gt doctor --fix' or 'bd config set routing.mode explicit'",
			}
		}
		// Other error - report as warning
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("Could not check routing.mode at %s: %v", location, err),
		}
	}

	mode := strings.TrimSpace(stdout.String())
	if mode != "explicit" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("routing.mode is '%s' at %s (should be 'explicit')", mode, location),
			Details: []string{
				"Auto routing mode uses git remote URL to detect user role",
				"Non-SSH URLs (HTTPS or file paths) trigger routing to ~/.beads-planning",
				"This causes mail and issues to be stored in the wrong location",
				"See: https://github.com/steveyegge/beads/issues/1165",
			},
			FixHint: "Run 'gt doctor --fix' or 'bd config set routing.mode explicit'",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("routing.mode is explicit at %s", location),
	}
}

// Fix sets routing.mode to "explicit" in both town and rig beads.
func (c *RoutingModeCheck) Fix(ctx *CheckContext) error {
	// Fix town-level beads
	townBeadsDir := filepath.Join(ctx.TownRoot, ".beads")
	if err := c.setRoutingMode(townBeadsDir); err != nil {
		return fmt.Errorf("fixing town beads: %w", err)
	}

	// Also fix rig-level beads if specified
	if ctx.RigName != "" {
		rigBeadsDir := filepath.Join(ctx.RigPath(), ".beads")
		if err := c.setRoutingMode(rigBeadsDir); err != nil {
			return fmt.Errorf("fixing rig %s beads: %w", ctx.RigName, err)
		}
	}

	return nil
}

// setRoutingMode sets routing.mode to "explicit" in the specified beads directory.
func (c *RoutingModeCheck) setRoutingMode(beadsDir string) error {
	cmd := beads.Spawn("config", "set", "routing.mode", "explicit")
	cmd.Dir = filepath.Dir(beadsDir)
	cmd.Env = append(cmd.Environ(), "BEADS_DIR="+beadsDir)

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bd config set failed: %s", strings.TrimSpace(string(output)))
	}

	return nil
}
