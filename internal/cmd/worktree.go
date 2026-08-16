package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/git"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

// Worktree command flags
var (
	worktreeNoCD bool
)

var worktreeCmd = &cobra.Command{
	Use:     "worktree <rig>",
	GroupID: GroupWorkspace,
	Short:   "Create worktree in another rig for cross-rig work",
	Long: `Create a git worktree in another rig for cross-rig work.

This command is for crew workers who need to work on another rig's codebase
while maintaining their identity. It creates a worktree in the target rig's
crew/ directory with a name that identifies your source rig and identity.

The worktree is created at: ~/gt/<target-rig>/crew/<source-rig>-<name>/

For example, if you're gastown/crew/joe and run 'gt worktree beads':
- Creates worktree at ~/gt/beads/crew/gastown-joe/
- The worktree checks out main branch
- Your identity (BD_ACTOR, GT_ROLE) remains gastown/crew/joe

Use --no-cd to just print the path without printing shell commands.

Examples:
  gt worktree beads         # Create worktree in beads rig
  gt worktree gastown       # Create worktree in gastown rig (from another rig)
  gt worktree beads --no-cd # Just print the path`,
	Args: cobra.ExactArgs(1),
	RunE: runWorktree,
}

var worktreeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all cross-rig worktrees owned by current crew member",
	Long: `List all git worktrees created for cross-rig work.

This command scans all rigs in the workspace and finds worktrees
that belong to the current crew member. Each worktree is shown with
its git status summary.

Example output:
  Cross-rig worktrees for gastown/crew/joe:

    beads     ~/gt/beads/crew/gastown-joe/     (clean)
    mayor     ~/gt/mayor/crew/gastown-joe/     (2 uncommitted)`,
	RunE: runWorktreeList,
}

// Worktree remove command flags
var (
	worktreeRemoveForce bool
)

var worktreeRemoveCmd = &cobra.Command{
	Use:   "remove <rig>",
	Short: "Remove a cross-rig worktree",
	Long: `Remove a git worktree created for cross-rig work.

This command removes a worktree that was previously created with 'gt worktree <rig>'.
It will refuse to remove a worktree with uncommitted changes unless --force is used.

Examples:
  gt worktree remove beads         # Remove beads worktree
  gt worktree remove beads --force # Force remove even with uncommitted changes`,
	Args: cobra.ExactArgs(1),
	RunE: runWorktreeRemove,
}

func init() {
	worktreeCmd.Flags().BoolVar(&worktreeNoCD, "no-cd", false, "Just print path (don't print cd command)")
	worktreeCmd.AddCommand(worktreeListCmd)

	worktreeRemoveCmd.Flags().BoolVarP(&worktreeRemoveForce, "force", "f", false, "Force remove even with uncommitted changes")
	worktreeCmd.AddCommand(worktreeRemoveCmd)

	rootCmd.AddCommand(worktreeCmd)
}

func runWorktree(cmd *cobra.Command, args []string) error {
	targetRig := args[0]

	// Detect current crew identity from cwd
	detected, err := detectCrewFromCwd()
	if err != nil {
		return fmt.Errorf("must be in a crew workspace to use this command: %w", err)
	}

	sourceRig := detected.rigName
	crewName := detected.crewName

	// Cannot create worktree in your own rig
	if targetRig == sourceRig {
		return fmt.Errorf("already in rig '%s' - use gt worktree to work in a different rig", targetRig)
	}

	// Verify target rig exists
	_, targetRigInfo, err := getRig(targetRig)
	if err != nil {
		return fmt.Errorf("rig '%s' not found - run 'gt rig list' to see available rigs", targetRig)
	}

	// Compute worktree path: ~/gt/<target-rig>/crew/<source-rig>-<name>/
	worktreeName := fmt.Sprintf("%s-%s", sourceRig, crewName)
	worktreePath := filepath.Join(constants.RigCrewPath(targetRigInfo.Path), worktreeName)

	// Check if worktree already exists
	if _, err := os.Stat(worktreePath); err == nil {
		// Worktree exists
		if worktreeNoCD {
			fmt.Println(worktreePath)
		} else {
			fmt.Printf("%s Worktree already exists at %s\n", style.Success.Render("✓"), worktreePath)
			fmt.Printf("cd %s\n", worktreePath)
		}
		return nil
	}

	// Get the source rig's git repository (the bare repo for worktrees)
	// For cross-rig work, we need to use the target rig's repository
	// The target rig's mayor/rig is the main clone we create worktrees from
	targetMayorRig := constants.RigMayorPath(targetRigInfo.Path)
	g := git.NewGit(targetMayorRig)

	// Ensure crew directory exists in target rig
	crewDir := constants.RigCrewPath(targetRigInfo.Path)
	if err := os.MkdirAll(crewDir, 0755); err != nil {
		return fmt.Errorf("creating crew directory: %w", err)
	}

	// Fetch latest from remote before creating worktree
	if err := g.Fetch("origin"); err != nil {
		// Non-fatal - continue with local state
		fmt.Printf("%s Warning: could not fetch from origin: %v\n", style.Warning.Render("⚠"), err)
	}

	// Create the worktree on main branch
	// Use WorktreeAddExistingForce because main may already be checked out
	// in other worktrees (e.g., mayor/rig). This is safe for cross-rig work.
	if err := g.WorktreeAddExistingForce(worktreePath, "main"); err != nil {
		return fmt.Errorf("creating worktree: %w", err)
	}

	// Configure git author for identity preservation
	worktreeGit := git.NewGit(worktreePath)
	bdActor := fmt.Sprintf("%s/crew/%s", sourceRig, crewName)

	// Set local git config for this worktree
	if err := setGitConfig(worktreePath, "user.name", bdActor); err != nil {
		fmt.Printf("%s Warning: could not set git author name: %v\n", style.Warning.Render("⚠"), err)
	}

	fmt.Printf("%s Created worktree for cross-rig work\n", style.Success.Render("✓"))
	fmt.Printf("  Source: %s/crew/%s\n", sourceRig, crewName)
	fmt.Printf("  Target: %s\n", worktreePath)
	fmt.Printf("  Branch: main\n")
	fmt.Println()

	// Pull latest main in the new worktree
	if err := worktreeGit.Pull("origin", "main"); err != nil {
		fmt.Printf("%s Warning: could not pull latest: %v\n", style.Warning.Render("⚠"), err)
	}

	if worktreeNoCD {
		fmt.Println(worktreePath)
	} else {
		fmt.Printf("To enter the worktree:\n")
		fmt.Printf("  cd %s\n", worktreePath)
		fmt.Println()
		fmt.Printf("Environment variables to preserve your identity:\n")
		if runtime.GOOS == "windows" {
			fmt.Printf("  $env:BD_ACTOR=%s\n", bdActor)
			fmt.Printf("  $env:GT_ROLE=crew\n")
			fmt.Printf("  $env:GT_RIG=%s\n", sourceRig)
			fmt.Printf("  $env:GT_CREW=%s\n", crewName)
		} else {
			fmt.Printf("  export BD_ACTOR=%s\n", bdActor)
			fmt.Printf("  export GT_ROLE=crew\n")
			fmt.Printf("  export GT_RIG=%s\n", sourceRig)
			fmt.Printf("  export GT_CREW=%s\n", crewName)
		}
	}

	return nil
}

// setGitConfig sets a git config value in the specified worktree.
func setGitConfig(worktreePath, key, value string) error {
	cmd := exec.Command("git", "-C", worktreePath, "config", key, value)
	return cmd.Run()
}

func runWorktreeList(cmd *cobra.Command, args []string) error {
	// Detect current crew identity from cwd
	detected, err := detectCrewFromCwd()
	if err != nil {
		return fmt.Errorf("must be in a crew workspace to use this command: %w", err)
	}

	sourceRig := detected.rigName
	crewName := detected.crewName
	worktreeName := fmt.Sprintf("%s-%s", sourceRig, crewName)

	// Find town root
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rigs config to list all rigs
	rigsConfigPath := constants.MayorRigsPath(townRoot)
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return fmt.Errorf("loading rigs config: %w", err)
	}

	fmt.Printf("Cross-rig worktrees for %s/crew/%s:\n\n", sourceRig, crewName)

	found := false
	for rigName := range rigsConfig.Rigs {
		// Skip our own rig - worktrees are for cross-rig work
		if rigName == sourceRig {
			continue
		}

		// Rig path is simply townRoot/<rigName>
		rigPath := filepath.Join(townRoot, rigName)

		// Check if worktree exists: <rig>/crew/<source-rig>-<name>/
		worktreePath := filepath.Join(constants.RigCrewPath(rigPath), worktreeName)

		if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
			continue
		}

		// Worktree exists - get git status
		statusSummary := getGitStatusSummary(worktreePath)

		// Format the path for display (use ~ for home directory)
		displayPath := worktreePath
		if home, err := os.UserHomeDir(); err == nil {
			if rel, err := filepath.Rel(home, worktreePath); err == nil && !filepath.IsAbs(rel) {
				displayPath = "~/" + rel
			}
		}

		fmt.Printf("  %-10s %s     (%s)\n", rigName, displayPath, statusSummary)
		found = true
	}

	if !found {
		fmt.Printf("  (none)\n")
		fmt.Printf("\nCreate a worktree with: gt worktree <rig>\n")
	}

	return nil
}

// getGitStatusSummary returns a brief status summary for a git directory.
func getGitStatusSummary(dir string) string {
	g := git.NewGit(dir)

	// Check for uncommitted changes
	status, err := g.Status()
	if err != nil {
		return "error"
	}

	if status.Clean {
		return "clean"
	}

	// Count uncommitted files (modified, added, deleted, untracked)
	uncommitted := len(status.Modified) + len(status.Added) + len(status.Deleted) + len(status.Untracked)

	return fmt.Sprintf("%d uncommitted", uncommitted)
}

func runWorktreeRemove(cmd *cobra.Command, args []string) error {
	targetRig := args[0]

	// Detect current crew identity from cwd
	detected, err := detectCrewFromCwd()
	if err != nil {
		return fmt.Errorf("must be in a crew workspace to use this command: %w", err)
	}

	sourceRig := detected.rigName
	crewName := detected.crewName

	// Cannot remove worktree in your own rig (doesn't make sense)
	if targetRig == sourceRig {
		return fmt.Errorf("cannot remove worktree in your own rig '%s'", targetRig)
	}

	// Verify target rig exists
	_, targetRigInfo, err := getRig(targetRig)
	if err != nil {
		return fmt.Errorf("rig '%s' not found - run 'gt rig list' to see available rigs", targetRig)
	}

	// Compute worktree path: ~/gt/<target-rig>/crew/<source-rig>-<name>/
	worktreeName := fmt.Sprintf("%s-%s", sourceRig, crewName)
	worktreePath := filepath.Join(constants.RigCrewPath(targetRigInfo.Path), worktreeName)

	// Check if worktree exists
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return fmt.Errorf("worktree does not exist at %s", worktreePath)
	}

	// Check for uncommitted changes (unless --force)
	if !worktreeRemoveForce {
		statusSummary := getGitStatusSummary(worktreePath)
		if statusSummary != "clean" && statusSummary != "error" {
			return fmt.Errorf("worktree has %s - use --force to remove anyway", statusSummary)
		}
	}

	// Get the target rig's mayor path (where the main git repo is)
	targetMayorRig := constants.RigMayorPath(targetRigInfo.Path)
	g := git.NewGit(targetMayorRig)

	// Remove the worktree
	if err := g.WorktreeRemove(worktreePath, worktreeRemoveForce); err != nil {
		return fmt.Errorf("removing worktree: %w", err)
	}

	fmt.Printf("%s Removed worktree at %s\n", style.Success.Render("✓"), worktreePath)

	return nil
}
