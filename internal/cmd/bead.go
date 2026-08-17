package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/spf13/cobra"
)

var beadCmd = &cobra.Command{
	Use:     "bead",
	Aliases: []string{"bd"},
	GroupID: GroupWork,
	Short:   "Bead management utilities",
	Long: `Utilities for managing beads across repositories.

Provides operations that span multiple beads repositories, such as
moving beads between repos and viewing beads by ID with automatic
prefix-based routing.

Subcommands:
  move    Move a bead from one repository to another
  show    Show details of a bead (routes by prefix)
  read    Alias for show`,
}

var beadMoveCmd = &cobra.Command{
	Use:   "move <bead-id> <target-prefix>",
	Short: "Move a bead to a different repository",
	Long: `Move a bead from one repository to another.

This creates a copy of the bead in the target repository (with the new prefix)
and closes the source bead with a reference to the new location.

The target prefix determines which repository receives the bead.
Common prefixes: gt- (gastown), bd- (beads), hq- (headquarters)

Examples:
  gt bead move gt-abc123 bd-     # Move gt-abc123 to beads repo as bd-*
  gt bead move hq-xyz bd-        # Move hq-xyz to beads repo
  gt bead move bd-123 gt-        # Move bd-123 to gastown repo`,
	Args: cobra.ExactArgs(2),
	RunE: runBeadMove,
}

var beadMoveDryRun bool

var beadShowCmd = &cobra.Command{
	Use:   "show <bead-id> [flags]",
	Short: "Show details of a bead",
	Long: `Displays the full details of a bead by ID.

This is an alias for 'gt show'. All bd show flags are supported.

Examples:
  gt bead show gt-abc123          # Show a gastown issue
  gt bead show hq-xyz789          # Show a town-level bead
  gt bead show bd-def456          # Show a beads issue
  gt bead show gt-abc123 --json   # Output as JSON`,
	DisableFlagParsing: true, // Pass all flags through to bd show
	RunE: func(cmd *cobra.Command, args []string) error {
		return runShow(cmd, args)
	},
}

var beadReadCmd = &cobra.Command{
	Use:   "read <bead-id> [flags]",
	Short: "Show details of a bead (alias for 'show')",
	Long: `Displays the full details of a bead by ID.

This is an alias for 'gt bead show'. All bd show flags are supported.

Examples:
  gt bead read gt-abc123          # Show a gastown issue
  gt bead read hq-xyz789          # Show a town-level bead
  gt bead read bd-def456          # Show a beads issue
  gt bead read gt-abc123 --json   # Output as JSON`,
	DisableFlagParsing: true, // Pass all flags through to bd show
	RunE: func(cmd *cobra.Command, args []string) error {
		return runShow(cmd, args)
	},
}

func init() {
	beadMoveCmd.Flags().BoolVarP(&beadMoveDryRun, "dry-run", "n", false, "Show what would be done")
	beadCmd.AddCommand(beadMoveCmd)
	beadCmd.AddCommand(beadShowCmd)
	beadCmd.AddCommand(beadReadCmd)
	rootCmd.AddCommand(beadCmd)
}

// moveBeadInfo holds the essential fields we need to copy when moving beads
type moveBeadInfo struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Type        string   `json:"issue_type"`
	Priority    int      `json:"priority"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
	Assignee    string   `json:"assignee"`
	Status      string   `json:"status"`
}

func runBeadMove(cmd *cobra.Command, args []string) error {
	sourceID := args[0]
	targetPrefix := args[1]

	if beadMoveDryRun {
		if err := previewBeadMove(sourceID, targetPrefix, ""); err != nil {
			return err
		}
		fmt.Printf("\nDry run - would:\n")
		fmt.Printf("  1. Create new bead with prefix %s\n", normalizeBeadPrefix(targetPrefix))
		fmt.Printf("  2. Close %s with reference to new bead\n", sourceID)
		return nil
	}

	newID, err := moveBeadToPrefix(sourceID, targetPrefix, "")
	if err != nil {
		return err
	}
	fmt.Printf("\nBead moved: %s → %s\n", sourceID, newID)
	return nil
}

func normalizeBeadPrefix(prefix string) string {
	if prefix == "" {
		return "-"
	}
	if !strings.HasSuffix(prefix, "-") {
		return prefix + "-"
	}
	return prefix
}

func previewBeadMove(sourceID, targetPrefix, townRoot string) error {
	source, err := loadMoveBeadInfo(sourceID, townRoot)
	if err != nil {
		return err
	}
	targetPrefix = normalizeBeadPrefix(targetPrefix)
	fmt.Printf("%s Moving %s to %s...\n", style.Bold.Render("→"), sourceID, targetPrefix)
	fmt.Printf("  Title: %s\n", source.Title)
	fmt.Printf("  Type: %s\n", source.Type)
	return nil
}

func loadMoveBeadInfo(sourceID, townRoot string) (*moveBeadInfo, error) {
	sourceDir := resolveBeadDir(sourceID)
	if townRoot != "" {
		sourceDir = resolveBeadDirFromTownRoot(townRoot, sourceID)
	}
	// Get source bead details — resolve rig directory from prefix so that
	// rig-prefixed beads are found in their rig database (GH#2126).
	output, err := BdCmd("show", sourceID, "--json").
		Dir(sourceDir).
		StripBeadsDir().
		Output()
	if err != nil {
		return nil, fmt.Errorf("getting bead %s: %w", sourceID, err)
	}

	// bd show --json returns an array
	var sources []moveBeadInfo
	if err := json.Unmarshal(output, &sources); err != nil {
		return nil, fmt.Errorf("parsing bead data: %w", err)
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("bead %s not found", sourceID)
	}
	source := sources[0]

	// Don't move closed beads
	if source.Status == "closed" {
		return nil, fmt.Errorf("cannot move closed bead %s", sourceID)
	}

	// Guard against flag-like titles propagating during move (gt-e0kx5)
	if beads.IsFlagLikeTitle(source.Title) {
		return nil, fmt.Errorf("refusing to move bead: title %q looks like a CLI flag", source.Title)
	}
	return &source, nil
}

// moveBeadToPrefix copies sourceID into the repository that owns targetPrefix
// and closes the source with a pointer to the new ID. When townRoot is set,
// create/close are pinned to the resolved town/rig beads directories.
func moveBeadToPrefix(sourceID, targetPrefix, townRoot string) (string, error) {
	source, err := loadMoveBeadInfo(sourceID, townRoot)
	if err != nil {
		return "", err
	}

	targetPrefix = normalizeBeadPrefix(targetPrefix)

	fmt.Printf("%s Moving %s to %s...\n", style.Bold.Render("→"), sourceID, targetPrefix)
	fmt.Printf("  Title: %s\n", source.Title)
	fmt.Printf("  Type: %s\n", source.Type)

	// Build create command for target.
	// Skip --prefix for empty or bare "-" (normalization above turns "" into "-").
	createArgs := []string{"create"}
	if targetPrefix != "" && targetPrefix != "-" {
		createArgs = append(createArgs, "--prefix", targetPrefix)
	}
	createArgs = append(createArgs,
		"--title="+source.Title,
		"--type", source.Type,
		"--priority", fmt.Sprintf("%d", source.Priority),
		"--silent", // Only output the ID
	)

	if source.Description != "" {
		createArgs = append(createArgs, "--description", source.Description)
	}
	if source.Assignee != "" {
		createArgs = append(createArgs, "--assignee", source.Assignee)
	}
	for _, label := range source.Labels {
		createArgs = append(createArgs, "--label", label)
	}

	var newID string
	if townRoot != "" {
		targetDir := resolveBeadDirFromTownRoot(townRoot, targetPrefix+"x")
		out, err := BdCmd(createArgs...).
			Dir(targetDir).
			StripBeadsDir().
			WithAutoCommit().
			Output()
		if err != nil {
			return "", fmt.Errorf("creating new bead: %w", err)
		}
		newID = strings.TrimSpace(string(out))
	} else {
		createCmd := beads.Spawn(createArgs...)
		createCmd.Stderr = os.Stderr
		newIDBytes, err := createCmd.Output()
		if err != nil {
			return "", fmt.Errorf("creating new bead: %w", err)
		}
		newID = strings.TrimSpace(string(newIDBytes))
	}
	if newID == "" {
		return "", fmt.Errorf("creating new bead: empty ID")
	}

	fmt.Printf("%s Created %s\n", style.Bold.Render("✓"), newID)

	closeReason := fmt.Sprintf("Moved to %s", newID)
	if townRoot != "" {
		sourceDir := resolveBeadDirFromTownRoot(townRoot, sourceID)
		if err := BdCmd("close", sourceID, "--reason", closeReason).
			Dir(sourceDir).
			StripBeadsDir().
			WithAutoCommit().
			Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close source bead: %v\n", err)
			targetDir := resolveBeadDirFromTownRoot(townRoot, newID)
			if cleanupErr := BdCmd("close", newID, "--reason", "Cleanup: source bead close failed during move").
				Dir(targetDir).
				StripBeadsDir().
				WithAutoCommit().
				Run(); cleanupErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: also failed to clean up new bead %s: %v\n", newID, cleanupErr)
				fmt.Fprintf(os.Stderr, "Both %s and %s remain open - manual cleanup needed\n", sourceID, newID)
			} else {
				fmt.Fprintf(os.Stderr, "Cleaned up new bead %s\n", newID)
			}
			return "", err
		}
	} else {
		closeCmd := beads.Spawn("close", sourceID, "--reason", closeReason)
		closeCmd.Stderr = os.Stderr
		if err := closeCmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close source bead: %v\n", err)
			cleanupCmd := beads.Spawn("close", newID, "--reason", "Cleanup: source bead close failed during move")
			if cleanupErr := cleanupCmd.Run(); cleanupErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: also failed to clean up new bead %s: %v\n", newID, cleanupErr)
				fmt.Fprintf(os.Stderr, "Both %s and %s remain open - manual cleanup needed\n", sourceID, newID)
			} else {
				fmt.Fprintf(os.Stderr, "Cleaned up new bead %s\n", newID)
			}
			return "", err
		}
	}

	fmt.Printf("%s Closed %s (moved to %s)\n", style.Bold.Render("✓"), sourceID, newID)
	return newID, nil
}
