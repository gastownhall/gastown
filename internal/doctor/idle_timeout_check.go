package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/beads"
)

// IdleTimeoutCheck verifies that all rigs have dolt.idle-timeout set to "0"
// to prevent per-rig idle-monitors from spawning duplicate Dolt servers.
// Gas Town uses a centralized Dolt server managed by systemd.
type IdleTimeoutCheck struct {
	FixableCheck
}

// NewIdleTimeoutCheck creates a new idle timeout check.
func NewIdleTimeoutCheck() *IdleTimeoutCheck {
	return &IdleTimeoutCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "idle-timeout-config",
				CheckDescription: "Verify all rigs have dolt.idle-timeout set to \"0\" (centralized Dolt)",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run checks if all rigs have dolt.idle-timeout set to "0".
func (c *IdleTimeoutCheck) Run(ctx *CheckContext) *CheckResult {
	// Load routes to get rig info
	townBeadsDir := filepath.Join(ctx.TownRoot, ".beads")
	routes, err := beads.LoadRoutes(townBeadsDir)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Could not load routes.jsonl",
		}
	}

	// Build unique rig list from routes
	rigSet := make(map[string]string) // rigName -> beadsPath
	for _, r := range routes {
		parts := strings.Split(r.Path, "/")
		if len(parts) >= 1 && parts[0] != "." {
			rigName := parts[0]
			if _, exists := rigSet[rigName]; !exists {
				rigSet[rigName] = r.Path
			}
		}
	}

	if len(rigSet) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No rigs to check",
		}
	}

	var missing []string
	var checked int

	// Check each rig for idle-timeout config
	for rigName, beadsPath := range rigSet {
		// beadsPath from routes is the rig path (e.g., "gastown/mayor/rig" or "gastown")
		// We need to find the .beads directory within that path
		rigPath := filepath.Join(ctx.TownRoot, beadsPath)
		// Check for .beads in the rig path
		configPath := filepath.Join(rigPath, ".beads", "config.yaml")

		data, err := os.ReadFile(configPath)
		if err != nil {
			// Config file missing - will be created by EnsureConfigYAML
			missing = append(missing, fmt.Sprintf("%s (config.yaml missing)", rigName))
			checked++
			continue
		}

		content := string(data)
		if !strings.Contains(content, "dolt.idle-timeout:") ||
			!strings.Contains(content, "dolt.idle-timeout: \"0\"") {
			missing = append(missing, rigName)
		}
		checked++
	}

	if len(missing) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("All %d rigs have dolt.idle-timeout set to \"0\"", checked),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d rig(s) missing dolt.idle-timeout: \"0\"", len(missing)),
		Details: missing,
		FixHint: "Run 'gt doctor --fix' to add idle-timeout config to all rigs",
	}
}

// Fix adds dolt.idle-timeout: "0" to all rig config.yaml files.
func (c *IdleTimeoutCheck) Fix(ctx *CheckContext) error {
	// Load routes to get rig info
	townBeadsDir := filepath.Join(ctx.TownRoot, ".beads")
	routes, err := beads.LoadRoutes(townBeadsDir)
	if err != nil {
		return fmt.Errorf("loading routes.jsonl: %w", err)
	}

	// Build unique rig list from routes
	rigSet := make(map[string]string) // rigName -> beadsPath
	for _, r := range routes {
		parts := strings.Split(r.Path, "/")
		if len(parts) >= 1 && parts[0] != "." {
			rigName := parts[0]
			if _, exists := rigSet[rigName]; !exists {
				rigSet[rigName] = r.Path
			}
		}
	}

	// Fix each rig
	for rigName, beadsPath := range rigSet {
		// beadsPath from routes is the rig path (e.g., "gastown/mayor/rig" or "gastown")
		rigPath := filepath.Join(ctx.TownRoot, beadsPath)
		// The .beads directory is within the rig path
		rigBeadsPath := filepath.Join(rigPath, ".beads")

		// Use EnsureConfigYAML to add idle-timeout if missing
		// This is idempotent - won't modify if already correct
		if err := beads.EnsureConfigYAML(rigBeadsPath, ""); err != nil {
			return fmt.Errorf("fixing %s: %w", rigName, err)
		}
	}

	return nil
}
