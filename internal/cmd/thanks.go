package cmd

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/charmbracelet/lipgloss"
	"github.com/jonbaldie/gastown/internal/ui"
	"github.com/spf13/cobra"
)

// Style definitions for thanks output using ui package colors
var (
	thanksTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(ui.ColorWarn)

	thanksSubtitleStyle = lipgloss.NewStyle().
				Foreground(ui.ColorMuted)

	thanksSectionStyle = lipgloss.NewStyle().
				Foreground(ui.ColorAccent).
				Bold(true)

	thanksNameStyle = lipgloss.NewStyle().
			Foreground(ui.ColorPass)

	thanksDimStyle = lipgloss.NewStyle().
			Foreground(ui.ColorMuted)
)

// thanksBoxStyle returns a bordered box style for the thanks header
func thanksBoxStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(ui.ColorMuted).
		Padding(1, 4).
		Width(width - 4).
		Align(lipgloss.Center)
}

// gastownContributors maps HUMAN contributor names to their commit counts.
// Agent names (gastown/*, beads/*, lowercase single-word names) are excluded.
// Generated from: git shortlog -sn --all (then filtered for humans only)
var gastownContributors = map[string]int{
	"Steve Yegge":               2056,
	"Mike Lady":                 19,
	"Olivier Debeuf De Rijcker": 13,
	"Danno Mayer":               11,
	"Dan Shapiro":               7,
	"Subhrajit Makur":           7,
	"Julian Knutsen":            5,
	"Darko Luketic":             4,
	"Martin Emde":               4,
	"Greg Hughes":               3,
	"Avyukth":                   2,
	"Ben Kraus":                 2,
	"Joshua Vial":               2,
	"Austin Wallace":            1,
	"Cameron Palmer":            1,
	"Chris Sloane":              1,
	"Cong":                      1,
	"Dave Laird":                1,
	"Dave Williams":             1,
	"Jacob":                     1,
	"Johann Taberlet":           1,
	"Joshua Samuel":             1,
	"Madison Bullard":           1,
	"PepijnSenders":             1,
	"Raymond Weitekamp":         1,
	"Sohail Mohammad":           1,
	"Zachary Rosen":             1,
}

var thanksCmd = &cobra.Command{
	Use:     "thanks",
	Short:   "Thank the human contributors to Gas Town",
	GroupID: GroupDiag,
	Long: `Display acknowledgments to all the humans who have contributed
to the Gas Town project. This command celebrates the collaborative
effort behind the multi-agent workspace manager.`,
	Run: func(cmd *cobra.Command, args []string) {
		printThanksPage()
	},
}

// getContributorsSorted returns contributor names sorted by commit count descending
func getContributorsSorted() []string {
	names := make([]string, 0, len(gastownContributors))
	for name := range gastownContributors {
		names = append(names, name)
	}

	slices.SortFunc(names, func(a, b string) int {
		// sort by commit count descending, then by name ascending for ties
		countCmp := cmp.Compare(gastownContributors[b], gastownContributors[a])
		if countCmp != 0 {
			return countCmp
		}
		return cmp.Compare(a, b)
	})

	return names
}

// printThanksPage renders the complete thanks page
func printThanksPage() {
	fmt.Println()

	// get sorted contributors, split into featured (top 20) and rest
	sorted := getContributorsSorted()
	featuredCount := 20
	if len(sorted) < featuredCount {
		featuredCount = len(sorted)
	}
	featured := sorted[:featuredCount]
	additional := sorted[featuredCount:]

	// calculate content width based on 4 columns
	cols := 4
	contentWidth := calculateColumnsWidth(featured, cols)
	if contentWidth < 60 {
		contentWidth = 60
	}

	// build header content
	title := thanksTitleStyle.Render("THANK YOU!")
	subtitle := thanksSubtitleStyle.Render("To all the humans who contributed to Gas Town")
	headerContent := title + "\n\n" + subtitle

	// render header in bordered box
	header := thanksBoxStyle(contentWidth).Render(headerContent)
	fmt.Println(header)
	fmt.Println()

	// print featured contributors section
	fmt.Println(thanksSectionStyle.Render("  Featured Contributors"))
	fmt.Println()
	printThanksColumns(featured, cols)

	// print additional contributors if any
	if len(additional) > 0 {
		fmt.Println()
		fmt.Println(thanksSectionStyle.Render("  Additional Contributors"))
		fmt.Println()
		printThanksWrappedList("", additional, contentWidth)
	}
	fmt.Println()
}

// calculateColumnsWidth determines the width needed for n columns of names
func calculateColumnsWidth(names []string, cols int) int {
	maxWidth := 0
	for _, name := range names {
		if len(name) > maxWidth {
			maxWidth = len(name)
		}
	}

	// cap at 20 characters per column
	if maxWidth > 20 {
		maxWidth = 20
	}

	// add padding between columns
	colWidth := maxWidth + 2

	return colWidth * cols
}

// printThanksColumns prints names in n columns, reading left-to-right
func printThanksColumns(names []string, cols int) {
	if len(names) == 0 {
		return
	}

	// find max width for alignment
	maxWidth := 0
	for _, name := range names {
		if len(name) > maxWidth {
			maxWidth = len(name)
		}
	}
	if maxWidth > 20 {
		maxWidth = 20
	}
	colWidth := maxWidth + 2

	// print in rows, reading left to right (matches bd thanks)
	for i := 0; i < len(names); i += cols {
		fmt.Print("  ")
		for j := 0; j < cols && i+j < len(names); j++ {
			name := names[i+j]
			if len(name) > 20 {
				name = name[:17] + "..."
			}
			// pad BEFORE styling to avoid ANSI code width issues
			padded := fmt.Sprintf("%-*s", colWidth, name)
			fmt.Print(thanksNameStyle.Render(padded))
		}
		fmt.Println()
	}
}

// printThanksWrappedList prints a comma-separated list with word wrapping
func printThanksWrappedList(label string, names []string, maxWidth int) {
	indent := "  "

	fmt.Print(indent)
	lineLen := len(indent)

	if label != "" {
		fmt.Print(thanksSectionStyle.Render(label) + " ")
		lineLen += len(label) + 1
	}

	for i, name := range names {
		suffix := ", "
		if i == len(names)-1 {
			suffix = ""
		}
		entry := name + suffix

		if lineLen+len(entry) > maxWidth && lineLen > len(indent) {
			fmt.Println()
			fmt.Print(indent)
			lineLen = len(indent)
		}

		fmt.Print(thanksDimStyle.Render(entry))
		lineLen += len(entry)
	}
	fmt.Println()
}

func init() {
	rootCmd.AddCommand(thanksCmd)
}
