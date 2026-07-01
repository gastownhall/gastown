package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var catJSON bool

var catCmd = &cobra.Command{
	Use:     "cat <bead-id>",
	GroupID: GroupWork,
	Short:   "Display bead content",
	Long: `Display the content of a bead (issue, task, molecule, etc.).

This is a convenience wrapper around 'bd show' that integrates with gt.
Accepts any bead ID with a recognized prefix (gt-*, bd-*, hq-*, mol-*, etc.).

Examples:
  gt cat gt-abc123       # Show a gastown bead
  gt cat bd-abc123       # Show a beads bead
  gt cat hq-xyz789       # Show a town-level bead
  gt cat bd-abc --json   # Output as JSON`,
	Args: cobra.ExactArgs(1),
	RunE: runCat,
}

func init() {
	rootCmd.AddCommand(catCmd)
	catCmd.Flags().BoolVar(&catJSON, "json", false, "Output as JSON")
}

func runCat(cmd *cobra.Command, args []string) error {
	beadID := args[0]

	// Validate it looks like a bead ID
	if !isBeadID(beadID) {
		return fmt.Errorf("invalid bead ID %q (expected format: <prefix>-<id>, e.g. gt-abc123)", beadID)
	}

	// Build bd show command
	bdArgs := []string{"show", beadID}
	if catJSON {
		bdArgs = append(bdArgs, "--json")
	}

	bdCmd := issueTrackerCommand(bdArgs...)
	bdCmd.Stdout = os.Stdout
	bdCmd.Stderr = os.Stderr
	// Route to the correct rig database via prefix resolution.
	if dir := resolveBeadDir(beadID); dir != "" && dir != "." {
		bdCmd.Dir = dir
		bdCmd.Env = filterEnvKey(os.Environ(), "BEADS_DIR")
	}

	return bdCmd.Run()
}

// isBeadID checks if a string looks like a bead ID.
// Bead IDs have the format <prefix>-<id> where prefix is lowercase letters
// (e.g. gt-abc123, bd-xyz, hq-cv-foo, wisp-bar, mol-baz).
func isBeadID(s string) bool {
	// Must contain a dash and start with lowercase letters
	dashIdx := strings.Index(s, "-")
	if dashIdx <= 0 || dashIdx >= len(s)-1 {
		return false
	}
	// Prefix must be all lowercase letters
	for _, c := range s[:dashIdx] {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}
