package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jonbaldie/gastown/internal/atomicfile"
)

// TownConfigExistsCheck verifies mayor/town.json exists.
type TownConfigExistsCheck struct {
	BaseCheck
}

// NewTownConfigExistsCheck creates a new town config exists check.
func NewTownConfigExistsCheck() *TownConfigExistsCheck {
	return &TownConfigExistsCheck{
		BaseCheck: BaseCheck{
			CheckName:        "town-config-exists",
			CheckDescription: "Check that mayor/town.json exists",
			CheckCategory:    CategoryCore,
		},
	}
}

// Run checks if mayor/town.json exists.
func (c *TownConfigExistsCheck) Run(ctx *CheckContext) *CheckResult {
	configPath := filepath.Join(ctx.TownRoot, "mayor", "town.json")

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "mayor/town.json not found",
			FixHint: "Run 'gt install' to initialize workspace",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "mayor/town.json exists",
	}
}

// TownConfigValidCheck verifies mayor/town.json is valid JSON with required fields.
type TownConfigValidCheck struct {
	BaseCheck
}

// NewTownConfigValidCheck creates a new town config validation check.
func NewTownConfigValidCheck() *TownConfigValidCheck {
	return &TownConfigValidCheck{
		BaseCheck: BaseCheck{
			CheckName:        "town-config-valid",
			CheckDescription: "Check that mayor/town.json is valid with required fields",
			CheckCategory:    CategoryCore,
		},
	}
}

// townConfig represents the structure of mayor/town.json.
type townConfig struct {
	Type    string `json:"type"`
	Version int    `json:"version"`
	Name    string `json:"name"`
}

// Run validates mayor/town.json contents.
func (c *TownConfigValidCheck) Run(ctx *CheckContext) *CheckResult {
	configPath := filepath.Join(ctx.TownRoot, "mayor", "town.json")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "Cannot read mayor/town.json",
			Details: []string{err.Error()},
		}
	}

	var config townConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "mayor/town.json is not valid JSON",
			Details: []string{err.Error()},
			FixHint: "Fix JSON syntax in mayor/town.json",
		}
	}

	var issues []string

	if config.Type != "town" {
		issues = append(issues, fmt.Sprintf("type should be 'town', got '%s'", config.Type))
	}
	if config.Version == 0 {
		issues = append(issues, "version field is missing or zero")
	}
	if config.Name == "" {
		issues = append(issues, "name field is missing or empty")
	}

	if len(issues) > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "mayor/town.json has invalid fields",
			Details: issues,
			FixHint: "Fix the field values in mayor/town.json",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("mayor/town.json valid (name=%s, version=%d)", config.Name, config.Version),
	}
}

// RigsRegistryExistsCheck verifies mayor/rigs.json exists.
type RigsRegistryExistsCheck struct {
	FixableCheck
}

// NewRigsRegistryExistsCheck creates a new rigs registry exists check.
func NewRigsRegistryExistsCheck() *RigsRegistryExistsCheck {
	return &RigsRegistryExistsCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "rigs-registry-exists",
				CheckDescription: "Check that mayor/rigs.json exists",
				CheckCategory:    CategoryCore,
			},
		},
	}
}

// Run checks if mayor/rigs.json exists.
func (c *RigsRegistryExistsCheck) Run(ctx *CheckContext) *CheckResult {
	rigsPath := filepath.Join(ctx.TownRoot, "mayor", "rigs.json")

	if _, err := os.Stat(rigsPath); os.IsNotExist(err) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "mayor/rigs.json not found (no rigs registered)",
			FixHint: "Run 'gt doctor --fix' to create empty rigs.json",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "mayor/rigs.json exists",
	}
}

// Fix creates an empty rigs.json file.
func (c *RigsRegistryExistsCheck) Fix(ctx *CheckContext) error {
	rigsPath := filepath.Join(ctx.TownRoot, "mayor", "rigs.json")

	emptyRigs := struct {
		Version int                    `json:"version"`
		Rigs    map[string]interface{} `json:"rigs"`
	}{
		Version: 1,
		Rigs:    make(map[string]interface{}),
	}

	data, err := json.MarshalIndent(emptyRigs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling empty rigs.json: %w", err)
	}

	return atomicfile.WriteFile(rigsPath, data, 0644)
}

// RigsRegistryValidCheck verifies mayor/rigs.json is valid and rigs exist.
type RigsRegistryValidCheck struct {
	FixableCheck
	missingRigs []string // Cached for Fix
}

// NewRigsRegistryValidCheck creates a new rigs registry validation check.
func NewRigsRegistryValidCheck() *RigsRegistryValidCheck {
	return &RigsRegistryValidCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "rigs-registry-valid",
				CheckDescription: "Check that registered rigs exist on disk",
				CheckCategory:    CategoryCore,
			},
		},
	}
}

// rigsConfig represents the structure of mayor/rigs.json.
type rigsConfig struct {
	Version int                    `json:"version"`
	Rigs    map[string]interface{} `json:"rigs"`
}

// Run validates mayor/rigs.json and checks that registered rigs exist.
func (c *RigsRegistryValidCheck) Run(ctx *CheckContext) *CheckResult {
	rigsPath := filepath.Join(ctx.TownRoot, "mayor", "rigs.json")

	data, err := os.ReadFile(rigsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusOK,
				Message: "No rigs.json (skipping validation)",
			}
		}
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "Cannot read mayor/rigs.json",
			Details: []string{err.Error()},
		}
	}

	var config rigsConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "mayor/rigs.json is not valid JSON",
			Details: []string{err.Error()},
			FixHint: "Fix JSON syntax in mayor/rigs.json",
		}
	}

	if len(config.Rigs) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No rigs registered",
		}
	}

	// Check each registered rig exists
	var missing []string
	var found int

	for rigName := range config.Rigs {
		rigPath := filepath.Join(ctx.TownRoot, rigName)
		if _, err := os.Stat(rigPath); os.IsNotExist(err) {
			missing = append(missing, rigName)
		} else {
			found++
		}
	}

	// Cache for Fix
	c.missingRigs = missing

	if len(missing) > 0 {
		details := make([]string, len(missing))
		for i, m := range missing {
			details[i] = fmt.Sprintf("Missing rig directory: %s/", m)
		}

		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d of %d registered rig(s) missing", len(missing), len(config.Rigs)),
			Details: details,
			FixHint: "Run 'gt doctor --fix' to remove missing rigs from registry",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("All %d registered rig(s) exist", found),
	}
}

// Fix removes missing rigs from the registry.
func (c *RigsRegistryValidCheck) Fix(ctx *CheckContext) error {
	if len(c.missingRigs) == 0 {
		return nil
	}

	rigsPath := filepath.Join(ctx.TownRoot, "mayor", "rigs.json")

	data, err := os.ReadFile(rigsPath)
	if err != nil {
		return fmt.Errorf("reading rigs.json: %w", err)
	}

	var config rigsConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parsing rigs.json: %w", err)
	}

	// Remove missing rigs
	for _, rig := range c.missingRigs {
		delete(config.Rigs, rig)
	}

	// Write back
	newData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling rigs.json: %w", err)
	}

	return atomicfile.WriteFile(rigsPath, newData, 0644)
}

// MayorExistsCheck verifies the mayor/ directory structure.
type MayorExistsCheck struct {
	BaseCheck
}

// NewMayorExistsCheck creates a new mayor directory check.
func NewMayorExistsCheck() *MayorExistsCheck {
	return &MayorExistsCheck{
		BaseCheck: BaseCheck{
			CheckName:        "mayor-exists",
			CheckDescription: "Check that mayor/ directory exists with required files",
			CheckCategory:    CategoryCore,
		},
	}
}

// Run checks if mayor/ directory exists with expected contents.
func (c *MayorExistsCheck) Run(ctx *CheckContext) *CheckResult {
	mayorPath := filepath.Join(ctx.TownRoot, "mayor")

	info, err := os.Stat(mayorPath)
	if os.IsNotExist(err) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "mayor/ directory not found",
			FixHint: "Run 'gt install' to initialize workspace",
		}
	}
	if !info.IsDir() {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "mayor exists but is not a directory",
			FixHint: "Remove mayor file and run 'gt install'",
		}
	}

	// Check for expected files
	var missing []string
	expectedFiles := []string{"town.json"}

	for _, f := range expectedFiles {
		path := filepath.Join(mayorPath, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			missing = append(missing, f)
		}
	}

	if len(missing) > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "mayor/ exists but missing expected files",
			Details: missing,
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "mayor/ directory exists with required files",
	}
}

// WorkspaceChecks returns all workspace-level health checks.
func WorkspaceChecks() []Check {
	return []Check{
		NewTownConfigExistsCheck(),
		NewTownConfigValidCheck(),
		NewRigsRegistryExistsCheck(),
		NewRigsRegistryValidCheck(),
		NewMayorExistsCheck(),
	}
}
