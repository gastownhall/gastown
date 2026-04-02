package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/deps"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	updateNoPark  bool
	updateUpstream string
	updateDryRun  bool
)

var updateCmd = &cobra.Command{
	Use:     "update",
	GroupID: GroupWorkspace,
	Short:   "Pull upstream, rebuild binaries, and restart agents",
	Long: `Pull the latest changes from upstream and rebuild Gas Town binaries.

This wraps the manual multi-step update workflow into a single safe command:

  1. Park all rigs        Stop agents to avoid index lock contention
  2. Pull upstream        git pull <upstream> main --rebase
  3. Rebuild binaries     go install ./cmd/gt && go install ./cmd/bd
  4. Unpark all rigs      Resume agent spawning
  5. Post-update check    gt doctor --fix (optional)

The command is safe to run while agents are active — parking prevents
working tree churn and index lock contention during the pull.

Flags:
  --no-park       Skip park/unpark (for quick patches when agents are idle)
  --upstream      Override remote name (default: upstream)
  --dry-run       Show what would happen without executing

Examples:
  gt update                          # Full update cycle
  gt update --no-park                # Skip park/unpark
  gt update --upstream origin        # Pull from origin instead of upstream
  gt update --dry-run                # Preview steps`,
	RunE:         runUpdate,
	SilenceUsage: true,
}

func init() {
	updateCmd.Flags().BoolVar(&updateNoPark, "no-park", false, "Skip park/unpark (for quick patches)")
	updateCmd.Flags().StringVar(&updateUpstream, "upstream", "upstream", "Remote to pull from")
	updateCmd.Flags().BoolVar(&updateDryRun, "dry-run", false, "Show what would happen")
	rootCmd.AddCommand(updateCmd)
}

func runUpdate(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	g := git.NewGit(townRoot)

	// Verify we're on main
	branch, err := g.CurrentBranch()
	if err != nil {
		return fmt.Errorf("could not determine current branch: %w", err)
	}
	if branch != "main" && branch != "master" {
		return fmt.Errorf("must be on main branch to update (currently on %s)", branch)
	}

	// Verify the upstream remote exists
	remotes, err := g.Remotes()
	if err != nil {
		return fmt.Errorf("could not list remotes: %w", err)
	}
	remoteExists := false
	for _, r := range remotes {
		if r == updateUpstream {
			remoteExists = true
			break
		}
	}
	if !remoteExists {
		return fmt.Errorf("remote '%s' not found (available: %s)", updateUpstream, strings.Join(remotes, ", "))
	}

	if updateDryRun {
		fmt.Printf("\n%s Dry run — showing what would happen\n\n", style.Bold.Render("gt update"))
		updateDryRunSteps()
		return nil
	}

	// Check for uncommitted changes
	dirty, err := g.HasUncommittedChanges()
	if err != nil {
		return fmt.Errorf("could not check working tree: %w", err)
	}
	if dirty {
		return fmt.Errorf("working tree has uncommitted changes — commit or stash first")
	}

	fmt.Printf("\n%s Updating Gas Town from %s/main\n\n", style.Bold.Render("gt update"), updateUpstream)

	// Step 1: Park all rigs
	if !updateNoPark {
		if err := updateParkAll(townRoot); err != nil {
			return err
		}
	} else {
		fmt.Printf("  %s Skipping park (--no-park)\n", style.Dim.Render("→"))
	}

	// Step 2: Pull upstream
	pullErr := updatePullUpstream(g)

	// If pull failed, unpark before returning the error
	if pullErr != nil {
		if !updateNoPark {
			updateUnparkAll(townRoot)
		}
		return pullErr
	}

	// Step 3: Rebuild binaries
	rebuildErr := updateRebuildBinaries(townRoot)

	// Step 4: Unpark all rigs
	if !updateNoPark {
		updateUnparkAll(townRoot)
	} else {
		fmt.Printf("  %s Skipping unpark (--no-park)\n", style.Dim.Render("→"))
	}

	if rebuildErr != nil {
		return rebuildErr
	}

	// Step 5: Optional doctor --fix
	fmt.Printf("\n  %s Post-update health check\n", style.Bold.Render("5."))
	fmt.Printf("     Run %s to verify workspace health\n", style.Dim.Render("gt doctor --fix"))

	fmt.Printf("\n%s Update complete\n\n", style.SuccessPrefix)
	return nil
}

func updateDryRunSteps() {
	step := 1
	if !updateNoPark {
		fmt.Printf("  %s Park all rigs %s\n", style.Bold.Render(fmt.Sprintf("%d.", step)), style.Dim.Render("(gt rig park --all)"))
		step++
	}
	fmt.Printf("  %s Pull upstream %s\n", style.Bold.Render(fmt.Sprintf("%d.", step)), style.Dim.Render(fmt.Sprintf("(git fetch %s && git rebase %s/main)", updateUpstream, updateUpstream)))
	step++
	fmt.Printf("  %s Rebuild binaries %s\n", style.Bold.Render(fmt.Sprintf("%d.", step)), style.Dim.Render(fmt.Sprintf("(go install ./cmd/gt && go install %s)", deps.BeadsInstallPath)))
	step++
	if !updateNoPark {
		fmt.Printf("  %s Unpark all rigs %s\n", style.Bold.Render(fmt.Sprintf("%d.", step)), style.Dim.Render("(gt rig unpark --all)"))
		step++
	}
	fmt.Printf("  %s Post-update check %s\n", style.Bold.Render(fmt.Sprintf("%d.", step)), style.Dim.Render("(gt doctor --fix)"))
	fmt.Println()
}

func updateParkAll(townRoot string) error {
	fmt.Printf("  %s Parking all rigs...\n", style.Bold.Render("1."))

	rigs, err := getAllRigs()
	if err != nil {
		return fmt.Errorf("discovering rigs: %w", err)
	}

	if len(rigs) == 0 {
		fmt.Printf("     %s No rigs found\n", style.Dim.Render("→"))
		return nil
	}

	parked := 0
	for _, r := range rigs {
		if IsRigParked(townRoot, r.Name) {
			fmt.Printf("     %s %s already parked\n", style.Dim.Render("→"), r.Name)
			continue
		}
		if err := parkOneRig(r.Name); err != nil {
			fmt.Printf("     %s Failed to park %s: %v\n", style.WarningPrefix, r.Name, err)
		} else {
			parked++
		}
	}

	if parked > 0 {
		fmt.Printf("     %s Parked %d rig(s)\n", style.SuccessPrefix, parked)
	}
	return nil
}

func updatePullUpstream(g *git.Git) error {
	fmt.Printf("\n  %s Pulling from %s/main...\n", style.Bold.Render("2."), updateUpstream)

	// Fetch first, then rebase — safer than pull --rebase for diagnostics
	fmt.Printf("     Fetching %s...\n", updateUpstream)
	if err := g.Fetch(updateUpstream); err != nil {
		return fmt.Errorf("fetch %s failed: %w", updateUpstream, err)
	}

	fmt.Printf("     Rebasing onto %s/main...\n", updateUpstream)
	if err := g.Rebase(updateUpstream + "/main"); err != nil {
		return fmt.Errorf("rebase onto %s/main failed (resolve conflicts manually): %w", updateUpstream, err)
	}

	fmt.Printf("     %s Up to date with %s/main\n", style.SuccessPrefix, updateUpstream)
	return nil
}

func updateRebuildBinaries(townRoot string) error {
	fmt.Printf("\n  %s Rebuilding binaries...\n", style.Bold.Render("3."))

	// Install gt from local source
	fmt.Printf("     Installing gt...\n")
	cmd := exec.Command("go", "install", "./cmd/gt")
	cmd.Dir = townRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go install ./cmd/gt failed: %w", err)
	}
	fmt.Printf("     %s gt installed\n", style.SuccessPrefix)

	// Install bd from remote (it lives in a separate repo)
	fmt.Printf("     Installing bd...\n")
	cmd = exec.Command("go", "install", deps.BeadsInstallPath)
	cmd.Dir = townRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("     %s bd install failed: %v\n", style.WarningPrefix, err)
		fmt.Printf("     %s gt was installed successfully; bd can be retried with: go install %s\n",
			style.Dim.Render("→"), deps.BeadsInstallPath)
	} else {
		fmt.Printf("     %s bd installed\n", style.SuccessPrefix)
	}

	return nil
}

func updateUnparkAll(townRoot string) {
	fmt.Printf("\n  %s Unparking all rigs...\n", style.Bold.Render("4."))

	rigs, err := getAllRigs()
	if err != nil {
		fmt.Printf("     %s Could not discover rigs: %v\n", style.WarningPrefix, err)
		return
	}

	unparked := 0
	for _, r := range rigs {
		if !IsRigParked(townRoot, r.Name) {
			continue
		}
		if err := unparkOneRig(r.Name); err != nil {
			fmt.Printf("     %s Failed to unpark %s: %v\n", style.WarningPrefix, r.Name, err)
		} else {
			unparked++
		}
	}

	if unparked > 0 {
		fmt.Printf("     %s Unparked %d rig(s)\n", style.SuccessPrefix, unparked)
	}
}
