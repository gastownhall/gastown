package doctor

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/jonbaldie/gastown/internal/config"
)

// RoleConfigCheck verifies that role configuration is valid.
// Role definitions are now config-based (internal/config/roles/*.toml),
// not stored as beads. Built-in defaults are embedded in the binary.
// This check validates any user-provided overrides at:
//   - <town>/roles/<role>.toml (town-level overrides)
//   - <rig>/roles/<role>.toml (rig-level overrides)
type RoleConfigCheck struct {
	BaseCheck
}

// NewRoleBeadsCheck creates a new role config check.
// Note: Function name kept as NewRoleBeadsCheck for backward compatibility
// with existing doctor.go registration code.
func NewRoleBeadsCheck() *RoleConfigCheck {
	return &RoleConfigCheck{
		BaseCheck: BaseCheck{
			CheckName:        "role-config-valid",
			CheckDescription: "Verify role configuration is valid",
			CheckCategory:    CategoryConfig,
		},
	}
}

// Run checks if role config is valid.
func (c *RoleConfigCheck) Run(ctx *CheckContext) *CheckResult {
	var warnings []string
	var overrideCount int

	// Check town-level overrides
	townRolesDir := filepath.Join(ctx.TownRoot, "roles")
	if entries, err := os.ReadDir(townRolesDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && filepath.Ext(entry.Name()) == ".toml" {
				overrideCount++
				path := filepath.Join(townRolesDir, entry.Name())
				if err := validateRoleOverride(path); err != nil {
					warnings = append(warnings, fmt.Sprintf("town override %s: %v", entry.Name(), err))
				}
			}
		}
	}

	// Check rig-level overrides for each rig
	// Discover rigs by looking for directories with rig.json
	if entries, err := os.ReadDir(ctx.TownRoot); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			rigName := entry.Name()
			// Check if this is a rig (has rig.json)
			if _, err := os.Stat(filepath.Join(ctx.TownRoot, rigName, "rig.json")); err != nil {
				continue
			}
			rigRolesDir := filepath.Join(ctx.TownRoot, rigName, "roles")
			if roleEntries, err := os.ReadDir(rigRolesDir); err == nil {
				for _, roleEntry := range roleEntries {
					if !roleEntry.IsDir() && filepath.Ext(roleEntry.Name()) == ".toml" {
						overrideCount++
						path := filepath.Join(rigRolesDir, roleEntry.Name())
						if err := validateRoleOverride(path); err != nil {
							warnings = append(warnings, fmt.Sprintf("rig %s override %s: %v", rigName, roleEntry.Name(), err))
						}
					}
				}
			}
		}
	}

	if len(warnings) > 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusWarning,
			Message:  fmt.Sprintf("%d role config override(s) have issues", len(warnings)),
			Details:  warnings,
			FixHint:  "Check TOML syntax in role override files",
			Category: c.Category(),
		}
	}

	msg := "Role config uses built-in defaults"
	if overrideCount > 0 {
		msg = fmt.Sprintf("Role config valid (%d override file(s))", overrideCount)
	}

	return &CheckResult{
		Name:     c.Name(),
		Status:   StatusOK,
		Message:  msg,
		Category: c.Category(),
	}
}

// validateRoleOverride checks if a role override file is valid TOML.
func validateRoleOverride(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var def config.RoleDefinition
	if err := toml.Unmarshal(data, &def); err != nil {
		return fmt.Errorf("invalid TOML: %w", err)
	}

	return nil
}
