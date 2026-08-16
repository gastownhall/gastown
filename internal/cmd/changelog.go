package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	changelogToday bool
	changelogWeek  bool
	changelogSince string
	changelogRig   string
	changelogJSON  bool
)

var changelogCmd = &cobra.Command{
	Use:     "changelog",
	GroupID: GroupWork,
	Short:   "Show completed work across rigs",
	Long: `Show a changelog of closed beads across all rigs in Gas Town.

Filters out ephemeral/internal beads (wisps, patrols) to show only real work.

Examples:
  gt changelog            # This week's completed work (default)
  gt changelog --today    # Today's completions
  gt changelog --week     # This week's completions
  gt changelog --since 2026-03-10  # Since a specific date
  gt changelog --rig gastown       # One rig only
  gt changelog --json              # JSON output`,
	RunE: runChangelog,
}

func init() {
	rootCmd.AddCommand(changelogCmd)
	changelogCmd.Flags().BoolVar(&changelogToday, "today", false, "Show today's completions")
	changelogCmd.Flags().BoolVar(&changelogWeek, "week", false, "Show this week's completions")
	changelogCmd.Flags().StringVar(&changelogSince, "since", "", "Show completions since date (YYYY-MM-DD)")
	changelogCmd.Flags().StringVar(&changelogRig, "rig", "", "Filter by rig name")
	changelogCmd.Flags().BoolVar(&changelogJSON, "json", false, "Output as JSON")
}

// ChangelogEntry is a single completed bead for changelog output.
type ChangelogEntry struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Type        string    `json:"type"`
	Rig         string    `json:"rig"`
	ClosedAt    time.Time `json:"closed_at"`
	CloseReason string    `json:"close_reason,omitempty"`
}

// closedBead is the raw shape from bd list --status=closed --json.
type closedBead struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	IssueType   string   `json:"issue_type"`
	Ephemeral   bool     `json:"ephemeral"`
	ClosedAt    string   `json:"closed_at"`
	CloseReason string   `json:"close_reason"`
	Labels      []string `json:"labels"`
}

func runChangelog(_ *cobra.Command, _ []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	since, err := changelogSinceTime()
	if err != nil {
		return err
	}

	entries, err := collectChangelogEntries(townRoot, since)
	if err != nil {
		return err
	}

	if changelogJSON {
		return printChangelogJSON(entries)
	}
	return printChangelog(entries, since)
}

// changelogSinceTime returns the cutoff time based on flags.
func changelogSinceTime() (time.Time, error) {
	return changelogSinceTimeAt(time.Now(), changelogToday, changelogSince)
}

func changelogSinceTimeAt(now time.Time, today bool, since string) (time.Time, error) {
	if since != "" {
		t, err := time.ParseInLocation("2006-01-02", since, time.Local)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid date %q: use YYYY-MM-DD", since)
		}
		return t, nil
	}
	if today {
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.Local), nil
	}
	// Default: this week (Monday)
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	monday := now.AddDate(0, 0, -(weekday - 1))
	y, m, d := monday.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local), nil
}

// collectChangelogEntries gathers closed beads from town + all rigs since the cutoff.
func collectChangelogEntries(townRoot string, since time.Time) ([]ChangelogEntry, error) {
	type location struct {
		path string
		rig  string // empty = HQ
	}

	locations := []location{{path: townRoot, rig: "hq"}}

	if changelogRig == "" {
		rigsConfigPath := filepath.Join(townRoot, constants.DirMayor, constants.FileRigsJSON)
		rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
		if err == nil && rigsConfig != nil {
			for rigName := range rigsConfig.Rigs {
				rigPath := filepath.Join(townRoot, rigName)
				if _, statErr := os.Stat(filepath.Join(rigPath, constants.DirBeads)); statErr == nil {
					locations = append(locations, location{path: rigPath, rig: rigName})
				}
			}
		}
	} else {
		rigPath := filepath.Join(townRoot, changelogRig)
		if _, statErr := os.Stat(filepath.Join(rigPath, constants.DirBeads)); statErr != nil {
			return nil, fmt.Errorf("rig %q not found or has no beads database", changelogRig)
		}
		locations = []location{{path: rigPath, rig: changelogRig}}
	}

	var all []ChangelogEntry
	for _, loc := range locations {
		entries, err := fetchClosedBeads(loc.path, loc.rig, since)
		if err != nil {
			// Non-fatal: rig may have no beads db
			continue
		}
		all = append(all, entries...)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].ClosedAt.After(all[j].ClosedAt)
	})
	return all, nil
}

// fetchClosedBeads queries a single beads location for non-ephemeral closed beads since cutoff.
func fetchClosedBeads(dir, rig string, since time.Time) ([]ChangelogEntry, error) {
	cmd := exec.Command("bd", "list", "--status=closed", "--all", "--limit=0", "--json")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var beads []closedBead
	if err := json.Unmarshal(out, &beads); err != nil {
		return nil, fmt.Errorf("parsing beads: %w", err)
	}

	var entries []ChangelogEntry
	for _, b := range beads {
		if isInternalBead(b) {
			continue
		}
		closedAt, err := time.Parse(time.RFC3339, b.ClosedAt)
		if err != nil {
			continue
		}
		if closedAt.Before(since) {
			continue
		}
		entries = append(entries, ChangelogEntry{
			ID:          b.ID,
			Title:       b.Title,
			Type:        b.IssueType,
			Rig:         rig,
			ClosedAt:    closedAt,
			CloseReason: b.CloseReason,
		})
	}
	return entries, nil
}

// isInternalBead returns true for ephemeral/system beads that aren't real work.
func isInternalBead(b closedBead) bool {
	if b.Ephemeral {
		return true
	}
	// Filter out system types
	switch b.IssueType {
	case "event":
		return true
	}
	// Filter out internal title patterns (wisps, patrols, mols)
	lower := strings.ToLower(b.Title)
	for _, prefix := range []string{"mol-", "wisp-", "plugin run:", "cost report"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func printChangelog(entries []ChangelogEntry, since time.Time) error {
	periodStr := formatPeriod(since)
	fmt.Printf("\n%s Changelog — %s\n\n", style.Bold.Render("📋"), periodStr)

	if len(entries) == 0 {
		fmt.Println(style.Dim.Render("  No completed work found for this period."))
		fmt.Println()
		return nil
	}

	// Group by rig
	byRig := make(map[string][]ChangelogEntry)
	var rigOrder []string
	seen := make(map[string]bool)
	for _, e := range entries {
		if !seen[e.Rig] {
			rigOrder = append(rigOrder, e.Rig)
			seen[e.Rig] = true
		}
		byRig[e.Rig] = append(byRig[e.Rig], e)
	}

	typeIcon := map[string]string{
		"bug":     "🐛",
		"feature": "✨",
		"task":    "✓",
		"epic":    "🏔",
	}

	for _, rig := range rigOrder {
		rigEntries := byRig[rig]
		label := strings.ToUpper(rig)
		fmt.Printf("%s  %s\n", style.Bold.Render(label), style.Dim.Render(fmt.Sprintf("(%d)", len(rigEntries))))
		for _, e := range rigEntries {
			icon := typeIcon[e.Type]
			if icon == "" {
				icon = "·"
			}
			date := style.Dim.Render(e.ClosedAt.Format("Jan 02"))
			fmt.Printf("  %s  %s  %s\n", icon, style.Dim.Render(e.ID), date+" "+e.Title)
		}
		fmt.Println()
	}

	fmt.Printf("%s %d issues closed across %d rig(s)\n",
		style.Dim.Render("Total:"), len(entries), len(rigOrder))
	fmt.Println()
	return nil
}

func printChangelogJSON(entries []ChangelogEntry) error {
	out, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

// formatPeriod returns a human-readable period label.
func formatPeriod(since time.Time) string {
	now := time.Now()
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.Local)

	// Check week start BEFORE today — on Mondays, weekStart == today and
	// "Week of ..." is more informative than "Today".
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	monday := now.AddDate(0, 0, -(weekday - 1))
	monY, monM, monD := monday.Date()
	weekStart := time.Date(monY, monM, monD, 0, 0, 0, 0, time.Local)
	if since.Equal(weekStart) {
		return fmt.Sprintf("Week of %s", since.Format("Jan 02, 2006"))
	}
	if since.Equal(today) {
		return "Today"
	}
	return fmt.Sprintf("Since %s", since.Format("Jan 02, 2006"))
}
