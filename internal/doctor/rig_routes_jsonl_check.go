package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/config"
)

// RigRoutesJSONLCheck detects and fixes routes.jsonl files in rig .beads directories.
//
// Rig-level routes.jsonl files are problematic because:
// 1. bd's routing walks up to find town root (via mayor/town.json) and uses town-level routes.jsonl
// 2. If a rig has its own routes.jsonl, bd uses it and never finds town routes, breaking cross-rig routing
// 3. These files often exist due to a bug where bd's auto-export wrote issue data to routes.jsonl
//
// Fix: Delete routes.jsonl unconditionally. The Dolt database is the source
// of truth.
type RigRoutesJSONLCheck struct {
	FixableCheck
	// affectedRigs tracks which rigs have routes.jsonl
	affectedRigs []rigRoutesInfo
}

type rigRoutesInfo struct {
	rigName    string
	routesPath string
}

// NewRigRoutesJSONLCheck creates a new check for rig-level routes.jsonl files.
func NewRigRoutesJSONLCheck() *RigRoutesJSONLCheck {
	return &RigRoutesJSONLCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "rig-routes-jsonl",
				CheckDescription: "Check for routes.jsonl in rig .beads directories",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// Run checks for routes.jsonl files in rig .beads directories.
func (c *RigRoutesJSONLCheck) Run(ctx *CheckContext) *CheckResult {
	c.affectedRigs = nil // Reset

	// Get list of rigs from multiple sources
	rigDirs := c.findRigDirectories(ctx.TownRoot)

	if len(rigDirs) == 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "No rigs to check",
			Category: c.Category(),
		}
	}

	var problems []string

	for _, rigDir := range rigDirs {
		rigName := filepath.Base(rigDir)
		beadsDir := filepath.Join(rigDir, ".beads")
		routesPath := filepath.Join(beadsDir, beads.RoutesFileName)

		// Check if routes.jsonl exists in this rig's .beads directory
		if _, err := os.Stat(routesPath); os.IsNotExist(err) {
			continue // Good - no rig-level routes.jsonl
		}

		// routes.jsonl exists - it should be deleted
		problems = append(problems, fmt.Sprintf("%s: has routes.jsonl (will delete - breaks cross-rig routing)", rigName))

		c.affectedRigs = append(c.affectedRigs, rigRoutesInfo{
			rigName:    rigName,
			routesPath: routesPath,
		})
	}

	if len(c.affectedRigs) == 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  fmt.Sprintf("No rig-level routes.jsonl files (%d rigs checked)", len(rigDirs)),
			Category: c.Category(),
		}
	}

	return &CheckResult{
		Name:     c.Name(),
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d rig(s) have routes.jsonl (breaks routing)", len(c.affectedRigs)),
		Details:  problems,
		FixHint:  "Run 'gt doctor --fix' to delete these files",
		Category: c.Category(),
	}
}

// Fix deletes routes.jsonl files in rig .beads directories.
// The Dolt database is the source of truth.
func (c *RigRoutesJSONLCheck) Fix(ctx *CheckContext) error {
	// Re-run check to populate affectedRigs if needed
	if len(c.affectedRigs) == 0 {
		result := c.Run(ctx)
		if result.Status == StatusOK {
			return nil // Nothing to fix
		}
	}

	for _, info := range c.affectedRigs {
		if err := os.Remove(info.routesPath); err != nil {
			return fmt.Errorf("deleting %s: %w", info.routesPath, err)
		}
	}

	return nil
}

// findRigDirectories finds all rig directories in the town.
func (c *RigRoutesJSONLCheck) findRigDirectories(townRoot string) []string {
	var rigDirs []string
	seen := make(map[string]bool)

	// Source 1: rigs.json registry
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	if rigsConfig, err := config.LoadRigsConfig(rigsPath); err == nil {
		for rigName := range rigsConfig.Rigs {
			rigPath := filepath.Join(townRoot, rigName)
			if _, err := os.Stat(rigPath); err == nil && !seen[rigPath] {
				rigDirs = append(rigDirs, rigPath)
				seen[rigPath] = true
			}
		}
	}

	// Source 2: routes.jsonl (for rigs that may not be in registry)
	townBeadsDir := filepath.Join(townRoot, ".beads")
	townBeadsInfo, townBeadsErr := os.Stat(townBeadsDir)
	if routes, err := beads.LoadRoutes(townBeadsDir); err == nil {
		for _, route := range routes {
			if route.Path == "." || route.Path == "" {
				continue // Skip town root
			}
			// Extract rig name (first path component)
			parts := strings.Split(route.Path, "/")
			if len(parts) > 0 && parts[0] != "" {
				rigPath := filepath.Join(townRoot, parts[0])
				if _, err := os.Stat(rigPath); err == nil && !seen[rigPath] {
					rigDirs = append(rigDirs, rigPath)
					seen[rigPath] = true
				}
			}
		}
	}

	// Source 3: Look for directories with .beads subdirs (for unregistered rigs)
	entries, err := os.ReadDir(townRoot)
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			// Skip known non-rig directories
			if entry.Name() == "mayor" || entry.Name() == ".beads" || entry.Name() == ".git" {
				continue
			}
			rigPath := filepath.Join(townRoot, entry.Name())
			beadsDir := filepath.Join(rigPath, ".beads")
			beadsDirInfo, err := os.Stat(beadsDir)
			if err != nil {
				continue // .beads doesn't exist
			}
			// Skip if this dir's .beads resolves to the town root .beads
			// (e.g. deacon uses a symlinked .beads dir pointing to town beads)
			if townBeadsErr == nil && os.SameFile(townBeadsInfo, beadsDirInfo) {
				continue
			}
			if !seen[rigPath] {
				rigDirs = append(rigDirs, rigPath)
				seen[rigPath] = true
			}
		}
	}

	return rigDirs
}
