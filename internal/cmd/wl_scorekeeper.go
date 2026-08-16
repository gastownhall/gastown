package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/jonbaldie/gastown/internal/doltserver"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/wasteland"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	wlScorekeeperJSON bool
	wlScorekeeperPush bool
)

var wlScorekeeperCmd = &cobra.Command{
	Use:   "scorekeeper",
	Short: "Compute tier standings and update leaderboard",
	Args:  cobra.NoArgs,
	RunE:  runWlScorekeeper,
	Long: `Run the scorekeeper — compute tier standings from stamp data and materialize
into the leaderboard table.

The scorekeeper reads all stamps, computes per-rig statistics (stamp count,
average quality, skill coverage), determines tiers, and writes the results
to the leaderboard table in wl-commons.

Without clusters, max achievable tier is 'contributor' (3+ stamps).
'trusted' and above require cluster_breadth >= 1 (Phase 2).

EXAMPLES:
  gt wl scorekeeper             # Compute and update leaderboard
  gt wl scorekeeper --json      # Output computation summary as JSON
  gt wl scorekeeper --push      # Update leaderboard and push to DoltHub`,
}

func init() {
	wlScorekeeperCmd.Flags().BoolVar(&wlScorekeeperJSON, "json", false, "Output computation summary as JSON")
	wlScorekeeperCmd.Flags().BoolVar(&wlScorekeeperPush, "push", false, "Push updated commons to DoltHub after computation")
	wlCmd.AddCommand(wlScorekeeperCmd)
}

func runWlScorekeeper(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	dbName := wasteland.ResolveDBName(townRoot)
	if !doltserver.DatabaseExists(townRoot, dbName) {
		return fmt.Errorf("database %q not found\nJoin a wasteland first with: gt wl join <org/db>", dbName)
	}

	store := doltserver.NewWLCommonsWithDB(townRoot, dbName)
	return runScorekeeperWithStore(store)
}

func runScorekeeperWithStore(store doltserver.WLCommonsStore) error {
	if !wlScorekeeperJSON {
		fmt.Printf("%s Running scorekeeper...\n", style.Bold.Render("⚡"))
	}

	entries, err := doltserver.RunScorekeeper(store)
	if err != nil {
		return fmt.Errorf("scorekeeper failed: %w", err)
	}

	// Compute tier distribution
	tierDist := make(map[string]int)
	for _, e := range entries {
		tierDist[e.Tier]++
	}

	if wlScorekeeperJSON {
		summary := struct {
			RigsScored  int            `json:"rigs_scored"`
			TierDist    map[string]int `json:"tier_distribution"`
			MaxTier     string         `json:"max_tier"`
			ClusterNote string         `json:"cluster_note"`
		}{
			RigsScored:  len(entries),
			TierDist:    tierDist,
			MaxTier:     highestTier(tierDist),
			ClusterNote: "no clusters computed — max tier limited to contributor",
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(summary)
	}

	fmt.Printf("\n%s Scorekeeper run complete:\n", style.Bold.Render("✓"))
	fmt.Printf("  Rigs scored: %d\n", len(entries))

	fmt.Printf("  Tier distribution:")
	for _, tier := range []string{"governor", "admin", "maintainer", "trusted", "contributor", "newcomer"} {
		if n, ok := tierDist[tier]; ok && n > 0 {
			fmt.Printf(" %d %s,", n, tier)
		}
	}
	fmt.Println()

	ht := highestTier(tierDist)
	if ht != "" {
		fmt.Printf("  Highest tier: %s\n", ht)
	}
	fmt.Printf("  %s\n", style.Dim.Render("Note: no clusters computed — max tier limited to contributor"))

	return nil
}

// highestTier returns the highest tier present in the distribution.
func highestTier(dist map[string]int) string {
	tiers := []string{"governor", "admin", "maintainer", "trusted", "contributor", "newcomer"}
	for _, t := range tiers {
		if n, ok := dist[t]; ok && n > 0 {
			return t
		}
	}
	return ""
}
