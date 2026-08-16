package cmd

import (
	"fmt"

	gitpkg "github.com/jonbaldie/gastown/internal/git"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/spf13/cobra"
)

var (
	pruneBranchesDryRun  bool
	pruneBranchesPattern string
)

var pruneBranchesCmd = &cobra.Command{
	Use:     "prune-branches",
	GroupID: GroupWork,
	Short:   "Remove stale local polecat tracking branches",
	Long: `Remove local branches that were created when tracking remote polecat branches.

When polecats push branches to origin, other clones create local tracking
branches via git fetch. After the remote branch is deleted (post-merge),
git fetch --prune removes the remote tracking ref but the local branch
persists indefinitely.

This command finds and removes local branches matching the pattern (default:
polecat/*) that are either:
  - Fully merged to the default branch (main)
  - Have no corresponding remote tracking branch (remote was deleted)

Safety: Uses git branch -d (not -D) so only fully-merged branches are deleted.
Never deletes the current branch or the default branch.

Examples:
  gt prune-branches              # Clean up stale polecat branches
  gt prune-branches --dry-run    # Show what would be deleted
  gt prune-branches --pattern "feature/*"  # Custom pattern`,
	RunE: runPruneBranches,
}

func init() {
	pruneBranchesCmd.Flags().BoolVar(&pruneBranchesDryRun, "dry-run", false, "Show what would be deleted without deleting")
	pruneBranchesCmd.Flags().StringVar(&pruneBranchesPattern, "pattern", "polecat/*", "Branch name pattern to match")

	rootCmd.AddCommand(pruneBranchesCmd)
}

func runPruneBranches(cmd *cobra.Command, args []string) error {
	g := gitpkg.NewGit(".")
	if !g.IsRepo() {
		return fmt.Errorf("not a git repository")
	}

	// Run fetch --prune first to clean up stale remote tracking refs
	if err := g.FetchPrune("origin"); err != nil {
		// Non-fatal: we can still prune based on current state
		fmt.Printf("%s Warning: git fetch --prune failed: %v\n", style.Warning.Render("⚠"), err)
	}

	pruned, err := g.PruneStaleBranches(pruneBranchesPattern, pruneBranchesDryRun)
	if err != nil {
		return fmt.Errorf("pruning branches: %w", err)
	}

	if len(pruned) == 0 {
		fmt.Printf("%s No stale branches found matching %q\n", style.Bold.Render("✓"), pruneBranchesPattern)
		return nil
	}

	if pruneBranchesDryRun {
		fmt.Printf("%s Would prune %d branch(es):\n\n", style.Warning.Render("⚠"), len(pruned))
	} else {
		fmt.Printf("%s Pruned %d branch(es):\n\n", style.Bold.Render("✓"), len(pruned))
	}

	for _, b := range pruned {
		reasonStr := ""
		switch b.Reason {
		case "merged":
			reasonStr = "merged to main"
		case "no-remote":
			reasonStr = "remote branch deleted"
		case "no-remote-merged":
			reasonStr = "remote deleted, merged to main"
		}
		fmt.Printf("  %s %s (%s)\n",
			style.Dim.Render("•"),
			b.Name,
			style.Dim.Render(reasonStr))
	}
	fmt.Println()

	return nil
}
