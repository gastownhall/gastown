package doctor

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/rig"
)

// RigBeadsCheck verifies that rig identity beads exist for all rigs.
// Rig identity beads track rig metadata like git URL, prefix, and operational state.
// They are created by gt rig add (see gt-zmznh) but may be missing for legacy rigs.
type RigBeadsCheck struct {
	FixableCheck
}

// NewRigBeadsCheck creates a new rig identity beads check.
func NewRigBeadsCheck() *RigBeadsCheck {
	return &RigBeadsCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "rig-beads-exist",
				CheckDescription: "Verify rig identity beads exist for all rigs",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run checks if rig identity beads exist for all rigs.
func (c *RigBeadsCheck) Run(ctx *CheckContext) *CheckResult {
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
	// Routes have format: prefix "gt-" -> path "gastown/mayor/rig"
	rigSet := make(map[string]struct {
		prefix    string
		beadsPath string
	})
	for _, r := range routes {
		// Extract rig name from path (first component)
		parts := strings.Split(r.Path, "/")
		if len(parts) >= 1 && parts[0] != "." {
			rigName := parts[0]
			prefix := strings.TrimSuffix(r.Prefix, "-")
			if _, exists := rigSet[rigName]; !exists {
				rigSet[rigName] = struct {
					prefix    string
					beadsPath string
				}{
					prefix:    prefix,
					beadsPath: r.Path,
				}
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

	// Check each rig for its identity bead
	for rigName, info := range rigSet {
		rigBeadsPath := filepath.Join(ctx.TownRoot, info.beadsPath)
		bd := beads.New(rigBeadsPath)

		rigBeadID := beads.RigBeadIDWithPrefix(info.prefix, rigName)
		if _, err := bd.Show(rigBeadID); err != nil {
			missing = append(missing, rigBeadID)
		}
		checked++
	}

	if len(missing) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("All %d rig identity beads exist", checked),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusError,
		Message: fmt.Sprintf("%d rig identity bead(s) missing", len(missing)),
		Details: missing,
		FixHint: "Run 'gt doctor --fix' to create missing rig identity beads",
	}
}

// Fix creates missing rig identity beads.
func (c *RigBeadsCheck) Fix(ctx *CheckContext) error {
	// Load routes to get rig info
	townBeadsDir := filepath.Join(ctx.TownRoot, ".beads")
	routes, err := beads.LoadRoutes(townBeadsDir)
	if err != nil {
		return fmt.Errorf("loading routes.jsonl: %w", err)
	}

	// Build unique rig list from routes
	rigSet := make(map[string]struct {
		prefix    string
		beadsPath string
	})
	for _, r := range routes {
		parts := strings.Split(r.Path, "/")
		if len(parts) >= 1 && parts[0] != "." {
			rigName := parts[0]
			prefix := strings.TrimSuffix(r.Prefix, "-")
			if _, exists := rigSet[rigName]; !exists {
				rigSet[rigName] = struct {
					prefix    string
					beadsPath string
				}{
					prefix:    prefix,
					beadsPath: r.Path,
				}
			}
		}
	}

	if len(rigSet) == 0 {
		return nil // No rigs to process
	}

	// Create missing rig identity beads — collect errors instead of failing
	// on first so one broken rig doesn't block fixes for others.
	var errs []error
	for rigName, info := range rigSet {
		rigBeadsPath := filepath.Join(ctx.TownRoot, info.beadsPath)
		bd := beads.New(rigBeadsPath)

		// Try to get git URL from rig config
		rigPath := filepath.Join(ctx.TownRoot, rigName)
		gitURL := ""
		if cfg, err := rig.LoadRigConfig(rigPath); err == nil {
			gitURL = cfg.GitURL
		}

		fields := &beads.RigFields{
			Repo:   gitURL,
			Prefix: info.prefix,
			State:  beads.RigStateActive,
		}

		rigBeadID := beads.RigBeadIDWithPrefix(info.prefix, rigName)
		if _, err := bd.EnsureRigBead(rigName, fields); err != nil {
			errs = append(errs, fmt.Errorf("ensuring %s: %w", rigBeadID, err))
		}
	}

	return errors.Join(errs...)
}
