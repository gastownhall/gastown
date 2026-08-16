package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jonbaldie/gastown/internal/hooks"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var hooksListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show all managed settings.json locations",
	Long: `Show all managed .claude/settings.json locations and their sync status.

Displays each target with its override chain and whether it is
currently in sync with the base + overrides configuration.

Examples:
  gt hooks list            # Show all managed locations
  gt hooks list --json     # Output as JSON`,
	RunE: runHooksListTargets,
}

var hooksListJSON bool

func init() {
	hooksCmd.AddCommand(hooksListCmd)
	hooksListCmd.Flags().BoolVar(&hooksListJSON, "json", false, "Output as JSON")
}

// listTargetInfo holds display info for a single target.
type listTargetInfo struct {
	Target    string   `json:"target"`
	Overrides []string `json:"overrides"`
	Status    string   `json:"status"`
	Path      string   `json:"path"`
	Exists    bool     `json:"exists"`
}

func runHooksListTargets(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	targets, err := hooks.DiscoverTargets(townRoot)
	if err != nil {
		return fmt.Errorf("discovering targets: %w", err)
	}

	// Deduplicate targets by key (individual crew/polecat members share a key)
	// For list display, we show the group-level target, not individual members
	seen := make(map[string]bool)
	var uniqueTargets []hooks.Target
	for _, t := range targets {
		displayKey := t.DisplayKey()
		if !seen[displayKey] {
			seen[displayKey] = true
			uniqueTargets = append(uniqueTargets, t)
		}
	}

	var infos []listTargetInfo
	for _, target := range uniqueTargets {
		info := buildTargetInfo(target)
		infos = append(infos, info)
	}

	if hooksListJSON {
		return outputListJSON(infos)
	}

	return outputListHuman(infos)
}

func buildTargetInfo(target hooks.Target) listTargetInfo {
	overrides := hooks.GetApplicableOverrides(target.Key)

	// Filter to only overrides that actually exist on disk
	var activeOverrides []string
	for _, o := range overrides {
		if _, err := os.Stat(hooks.OverridePath(o)); err == nil {
			activeOverrides = append(activeOverrides, o)
		}
	}

	// Check if settings.json exists
	_, err := os.Stat(target.Path)
	exists := err == nil

	// Determine sync status
	status := "missing"
	if exists {
		expected, err := hooks.ComputeExpected(target.Key)
		if err != nil {
			status = "error"
		} else {
			current, err := hooks.LoadSettings(target.Path)
			if err != nil {
				status = "error"
			} else if hooks.HooksEqual(expected, &current.Hooks) {
				status = "in sync"
			} else {
				status = "out of sync"
			}
		}
	}

	return listTargetInfo{
		Target:    target.DisplayKey(),
		Overrides: activeOverrides,
		Status:    status,
		Path:      target.Path,
		Exists:    exists,
	}
}

func outputListJSON(infos []listTargetInfo) error {
	type listOutput struct {
		Targets      []listTargetInfo `json:"targets"`
		BasePath     string           `json:"base_path"`
		OverridesDir string           `json:"overrides_dir"`
	}

	output := listOutput{
		Targets:      infos,
		BasePath:     hooks.BasePath(),
		OverridesDir: hooks.OverridesDir(),
	}

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func outputListHuman(infos []listTargetInfo) error {
	// Calculate column widths based on visible text (excluding ANSI codes)
	targetWidth := len("Target")
	overridesWidth := len("Overrides")
	for _, info := range infos {
		if len(info.Target) > targetWidth {
			targetWidth = len(info.Target)
		}
		overrideStr := formatOverridesPlain(info.Overrides)
		if len(overrideStr) > overridesWidth {
			overridesWidth = len(overrideStr)
		}
	}

	// Cap widths
	if targetWidth > 30 {
		targetWidth = 30
	}
	if overridesWidth > 35 {
		overridesWidth = 35
	}

	// Header - pad plain text first, then style
	fmt.Printf("%s  %s  %s\n",
		style.Bold.Render(padRight("Target", targetWidth)),
		style.Bold.Render(padRight("Overrides", overridesWidth)),
		style.Bold.Render("Status"))

	// Rows - pad plain text content, then apply styles
	for _, info := range infos {
		overridePlain := formatOverridesPlain(info.Overrides)
		overrideStyled := formatOverrides(info.Overrides)
		statusStr := renderSyncStatus(info.Status)

		// Pad target (plain text, no ANSI)
		targetPadded := padRight(info.Target, targetWidth)

		// For overrides, add padding based on visible width difference
		overridePadded := overrideStyled + padRight("", overridesWidth-len(overridePlain))

		fmt.Printf("%s  %s  %s\n", targetPadded, overridePadded, statusStr)
	}

	// Footer
	fmt.Println()

	// Check if base config exists
	baseExists := "exists"
	if _, err := os.Stat(hooks.BasePath()); os.IsNotExist(err) {
		baseExists = "not found"
	}
	fmt.Printf("Base config: %s %s\n",
		style.Dim.Render(hooks.BasePath()),
		style.Dim.Render("("+baseExists+")"))

	// Count override files
	overrideCount := 0
	if entries, err := os.ReadDir(hooks.OverridesDir()); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				overrideCount++
			}
		}
	}
	fmt.Printf("Overrides:   %s (%d files)\n",
		style.Dim.Render(hooks.OverridesDir()),
		overrideCount)

	return nil
}

func formatOverrides(overrides []string) string {
	if len(overrides) == 0 {
		return style.Dim.Render("(none)")
	}
	return "[" + strings.Join(overrides, ", ") + "]"
}

// formatOverridesPlain returns the plain-text override string without ANSI codes.
func formatOverridesPlain(overrides []string) string {
	if len(overrides) == 0 {
		return "(none)"
	}
	return "[" + strings.Join(overrides, ", ") + "]"
}

// padRight pads s with spaces to width. If s is already >= width, returns s unchanged.
func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

func renderSyncStatus(status string) string {
	switch status {
	case "in sync":
		return style.Success.Render("✓ in sync")
	case "out of sync":
		return style.Warning.Render("⚠ out of sync")
	case "missing":
		return style.Dim.Render("- missing")
	case "error":
		return style.Error.Render("✖ error")
	default:
		return status
	}
}
