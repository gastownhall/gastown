package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/jonbaldie/gastown/internal/formula"
)

// OverlayHealthCheck verifies that formula overlay files reference valid step IDs.
// It scans overlay files at both town-level and rig-level, loads the referenced
// formula from the embedded binary, and checks that every step_id in the overlay
// matches a real step in the formula. Fix mode removes stale step-override entries.
type OverlayHealthCheck struct {
	FixableCheck
}

// NewOverlayHealthCheck creates a new overlay health check.
func NewOverlayHealthCheck() *OverlayHealthCheck {
	return &OverlayHealthCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "overlay-health",
				CheckDescription: "Check formula overlay step IDs are valid",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// overlayFile represents a discovered overlay file with its parsed contents.
type overlayFile struct {
	Path        string
	FormulaName string
	Overlay     *formula.FormulaOverlay
	ParseErr    error    // non-nil if TOML parsing failed
	StaleIDs    []string // step IDs that don't match any formula step
}

// Run checks all formula overlay files for stale step IDs and malformed TOML.
func (c *OverlayHealthCheck) Run(ctx *CheckContext) *CheckResult {
	files := c.scanOverlays(ctx.TownRoot)

	if len(files) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "no overlay files found",
		}
	}

	var malformed, stale, ok int
	var details []string

	for _, f := range files {
		if f.ParseErr != nil {
			malformed++
			details = append(details, fmt.Sprintf("%s: malformed TOML: %v", f.Path, f.ParseErr))
			continue
		}
		if len(f.StaleIDs) > 0 {
			stale++
			details = append(details, fmt.Sprintf("%s: stale step IDs: %s",
				f.Path, strings.Join(f.StaleIDs, ", ")))
			continue
		}
		ok++
	}

	if malformed > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("%d malformed overlay(s)", malformed),
			Details: details,
			FixHint: "Fix malformed TOML in overlay files manually",
		}
	}

	if stale > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d overlay(s) with stale step IDs", stale),
			Details: details,
			FixHint: "Run 'gt doctor --fix' to remove stale step overrides",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("%d overlay(s) healthy", ok),
	}
}

// Fix removes stale step-override entries from overlay files.
// Malformed TOML files are left untouched (require manual intervention).
func (c *OverlayHealthCheck) Fix(ctx *CheckContext) error {
	files := c.scanOverlays(ctx.TownRoot)

	for _, f := range files {
		if f.ParseErr != nil || len(f.StaleIDs) == 0 {
			continue
		}

		// Build set of stale IDs for quick lookup.
		staleSet := make(map[string]bool, len(f.StaleIDs))
		for _, id := range f.StaleIDs {
			staleSet[id] = true
		}

		// Filter out stale overrides.
		var kept []formula.StepOverride
		for _, so := range f.Overlay.StepOverrides {
			if !staleSet[so.StepID] {
				kept = append(kept, so)
			}
		}

		if len(kept) == 0 {
			// All overrides were stale — remove the file entirely.
			if err := os.Remove(f.Path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("removing empty overlay %s: %w", f.Path, err)
			}
			continue
		}

		// Re-encode with remaining overrides.
		f.Overlay.StepOverrides = kept
		if err := writeOverlayFile(f.Path, f.Overlay); err != nil {
			return fmt.Errorf("writing overlay %s: %w", f.Path, err)
		}
	}

	return nil
}

// scanOverlays discovers and validates all overlay files in the workspace.
func (c *OverlayHealthCheck) scanOverlays(townRoot string) []overlayFile {
	var results []overlayFile

	// Scan town-level overlays.
	townDir := filepath.Join(townRoot, "formula-overlays")
	results = append(results, scanOverlayDir(townDir)...)

	// Scan rig-level overlays by reading rigs.json.
	rigNames := loadRigNames(filepath.Join(townRoot, "mayor", "rigs.json"))
	for rigName := range rigNames {
		rigDir := filepath.Join(townRoot, rigName, "formula-overlays")
		results = append(results, scanOverlayDir(rigDir)...)
	}

	return results
}

// scanOverlayDir reads all .toml files in a formula-overlays directory,
// parses each one, and validates step IDs against the embedded formula.
func scanOverlayDir(dir string) []overlayFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // directory doesn't exist — that's fine
	}

	var results []overlayFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		formulaName := strings.TrimSuffix(entry.Name(), ".toml")

		of := overlayFile{
			Path:        path,
			FormulaName: formulaName,
		}

		// Parse the overlay file.
		data, err := os.ReadFile(path) //nolint:gosec // G304: trusted overlay directory
		if err != nil {
			of.ParseErr = err
			results = append(results, of)
			continue
		}

		var overlay formula.FormulaOverlay
		if _, err := toml.Decode(string(data), &overlay); err != nil {
			of.ParseErr = fmt.Errorf("parsing TOML: %w", err)
			results = append(results, of)
			continue
		}
		of.Overlay = &overlay

		// Load the embedded formula to get valid step IDs.
		embeddedContent, err := formula.GetEmbeddedFormulaContent(formulaName)
		if err != nil {
			// Formula not found in embedded binary — all step IDs are stale.
			for _, so := range overlay.StepOverrides {
				of.StaleIDs = append(of.StaleIDs, so.StepID)
			}
			results = append(results, of)
			continue
		}

		f, err := formula.Parse(embeddedContent)
		if err != nil {
			// Embedded formula can't be parsed — skip validation.
			results = append(results, of)
			continue
		}

		// Build set of valid IDs from the formula.
		validIDs := make(map[string]bool)
		for _, id := range f.GetAllIDs() {
			validIDs[id] = true
		}

		// Check each override step_id.
		for _, so := range overlay.StepOverrides {
			if !validIDs[so.StepID] {
				of.StaleIDs = append(of.StaleIDs, so.StepID)
			}
		}

		results = append(results, of)
	}

	return results
}

// writeOverlayFile encodes a FormulaOverlay back to TOML and writes it to disk.
func writeOverlayFile(path string, overlay *formula.FormulaOverlay) error {
	var buf strings.Builder
	encoder := toml.NewEncoder(&buf)
	if err := encoder.Encode(overlay); err != nil {
		return fmt.Errorf("encoding TOML: %w", err)
	}
	return os.WriteFile(path, []byte(buf.String()), 0644) //nolint:gosec // G306: overlay files are not sensitive
}
