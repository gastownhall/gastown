package cmd

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/jonbaldie/gastown/internal/doltserver"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/wasteland"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var wlCharsheetJSON bool

var wlCharsheetCmd = &cobra.Command{
	Use:   "charsheet [handle]",
	Short: "Display a rig's character sheet",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runWlCharsheet,
	Long: `Display a rig's character sheet — stamp geometry, skills, top stamps, and badges.

The character sheet renders a rig's position in value-space from accumulated stamps.
If no handle is specified, shows your own character sheet.

EXAMPLES:
  gt wl charsheet                # Your own character sheet
  gt wl charsheet alice-dev      # Another rig's sheet
  gt wl charsheet --json         # JSON output for tooling`,
}

func init() {
	wlCharsheetCmd.Flags().BoolVar(&wlCharsheetJSON, "json", false, "Output as JSON")
	wlCmd.AddCommand(wlCharsheetCmd)
}

func runWlCharsheet(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	handle := ""
	if len(args) > 0 {
		handle = args[0]
	} else {
		wlCfg, err := wasteland.LoadConfig(townRoot)
		if err != nil {
			return fmt.Errorf("loading wasteland config: %w", err)
		}
		handle = wlCfg.RigHandle
	}

	dbName := wasteland.ResolveDBName(townRoot)
	if !doltserver.DatabaseExists(townRoot, dbName) {
		return fmt.Errorf("database %q not found\nJoin a wasteland first with: gt wl join <org/db>", dbName)
	}

	store := doltserver.NewWLCommonsWithDB(townRoot, dbName)
	sheet, err := doltserver.AssembleCharacterSheet(store, handle)
	if err != nil {
		return fmt.Errorf("assembling character sheet: %w", err)
	}

	if wlCharsheetJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(sheet)
	}

	renderCharsheetASCII(sheet)
	return nil
}

func renderCharsheetASCII(sheet *doltserver.CharacterSheet) {
	// Header
	fmt.Printf("=== %s (%s) ===\n", style.Bold.Render(sheet.Handle), sheet.Tier)
	fmt.Printf("Stamps: %d | Clusters: %d\n", sheet.StampCount, sheet.ClusterBreadth)

	// Stamp Geometry
	fmt.Printf("\n%s\n", style.Bold.Render("Value Dimensions:"))
	if len(sheet.StampGeometry) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(no stamps yet)"))
	} else {
		for _, dim := range []string{"quality", "reliability", "creativity"} {
			if g, ok := sheet.StampGeometry[dim]; ok {
				bar := renderBar(g.Avg, 5.0, 10)
				label := fmt.Sprintf("%-14s", dim+":")
				fmt.Printf("  %s %s %.1f/5 (%d stamps)\n", label, bar, g.Avg, g.Count)
			}
		}
	}

	// Skill Coverage
	fmt.Printf("\n%s\n", style.Bold.Render("Top Skills:"))
	if len(sheet.SkillCoverage) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(no skills recorded)"))
	} else {
		var parts []string
		shown := 0
		for _, skill := range sheet.SkillCoverage {
			total := skill.PeerStamps + skill.BootBlocks
			if total < 2 {
				continue
			}
			suffix := fmt.Sprintf("%d stamps", total)
			if skill.BootBlocks > 0 && skill.PeerStamps == 0 {
				suffix = fmt.Sprintf("%d boot blocks", skill.BootBlocks)
			}
			parts = append(parts, fmt.Sprintf("%s (%s)", skill.Skill, suffix))
			shown++
			if shown >= 8 {
				break
			}
		}
		if len(parts) > 0 {
			fmt.Printf("  %s\n", strings.Join(parts, ", "))
		} else {
			fmt.Printf("  %s\n", style.Dim.Render("(no skills recorded)"))
		}
	}

	// Top Stamps
	if len(sheet.TopStamps) > 0 {
		fmt.Printf("\n%s\n", style.Bold.Render("Top Stamps:"))
		for _, s := range sheet.TopStamps {
			valParts := formatValenceMap(s.Valence)
			msg := s.Message
			if msg == "" {
				msg = s.ContextType
			}
			if len(msg) > 60 {
				msg = msg[:57] + "..."
			}
			fmt.Printf("  %s: %q  [%s]\n", s.Author, msg, valParts)
		}
	}

	// Warnings
	if len(sheet.Warnings) > 0 {
		fmt.Printf("\n%s\n", style.Warning.Render("WARNINGS:"))
		for _, w := range sheet.Warnings {
			fmt.Printf("  [%s] %s: %q  [%s]\n", w.Severity, w.Author, w.Message, w.Date)
		}
	}

	// Badges
	fmt.Printf("\n%s ", style.Bold.Render("Badges:"))
	if len(sheet.Badges) == 0 {
		fmt.Printf("%s\n", style.Dim.Render("(none)"))
	} else {
		names := make([]string, len(sheet.Badges))
		for i, b := range sheet.Badges {
			names[i] = b.Type
		}
		fmt.Printf("%s\n", strings.Join(names, ", "))
	}

	// Tier
	tierUnlocks := map[string]string{
		"newcomer":    "browse, fork, claim work",
		"contributor": "post wanted items, endorse others",
		"trusted":     "direct branch writes (skip claim PRs)",
		"maintainer":  "validate completions, stamp others",
		"admin":       "merge to main, admin commons",
		"governor":    "create wastelands, governance voice",
	}
	unlocks := tierUnlocks[sheet.Tier]
	if unlocks == "" {
		unlocks = "browse, fork, claim work"
	}
	fmt.Printf("\n%s %s (unlocked: %s)\n", style.Bold.Render("Tier:"), sheet.Tier, unlocks)
}

// renderBar creates a visual bar like "████████░░" of the given width.
func renderBar(value, maxVal float64, width int) string {
	filled := int(math.Round(value / maxVal * float64(width)))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// formatValenceMap formats a typed valence map as "quality:5 reliability:4".
func formatValenceMap(vals map[string]float64) string {
	var parts []string
	for _, dim := range []string{"quality", "reliability", "creativity"} {
		if v, ok := vals[dim]; ok {
			parts = append(parts, fmt.Sprintf("%s:%.0f", dim, v))
		}
	}
	return strings.Join(parts, " ")
}
