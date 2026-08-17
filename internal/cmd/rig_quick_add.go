// ABOUTME: Quick-add command for adding a repo to Gas Town with minimal friction.
// ABOUTME: Used by shell hook for automatic "add to Gas Town?" prompts.

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/rig"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	quickAddUser  string
	quickAddYes   bool
	quickAddQuiet bool
)

var rigQuickAddCmd = &cobra.Command{
	Use:    "quick-add [path]",
	Short:  "Quickly add current repo to Gas Town",
	Hidden: true,
	Long: `Quickly add a git repository to Gas Town with minimal interaction.

This command is designed for the shell hook's "Add to Gas Town?" prompt.
It infers the rig name from the directory and git URL from the remote.

Examples:
  gt rig quick-add                    # Add current directory
  gt rig quick-add ~/Repos/myproject  # Add specific path
  gt rig quick-add --yes              # Non-interactive`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRigQuickAdd,
}

func init() {
	rigCmd.AddCommand(rigQuickAddCmd)
	rigQuickAddCmd.Flags().StringVar(&quickAddUser, "user", "", "Crew workspace name (default: $USER)")
	rigQuickAddCmd.Flags().BoolVar(&quickAddYes, "yes", false, "Non-interactive, assume yes")
	rigQuickAddCmd.Flags().BoolVar(&quickAddQuiet, "quiet", false, "Minimal output")
}

func runRigQuickAdd(cmd *cobra.Command, args []string) error {
	targetPath := "."
	if len(args) > 0 {
		targetPath = args[0]
	}

	absPath, err := filepath.Abs(targetPath)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}

	if townRoot, err := workspace.Find(absPath); err == nil && townRoot != "" {
		return fmt.Errorf("already part of a Gas Town workspace: %s", townRoot)
	}

	gitRoot, err := findGitRoot(absPath)
	if err != nil {
		return fmt.Errorf("not a git repository: %w", err)
	}

	gitURL, err := findGitRemoteURL(gitRoot)
	if err != nil {
		return fmt.Errorf("no git remote found: %w", err)
	}

	rigName := rig.SanitizeName(filepath.Base(gitRoot))

	townRoot, err := findOrCreateTown()
	if err != nil {
		return fmt.Errorf("finding Gas Town: %w", err)
	}

	rigPath := filepath.Join(townRoot, rigName)
	if _, err := os.Stat(rigPath); err == nil {
		return fmt.Errorf("rig %q already exists in %s", rigName, townRoot)
	}

	originalName := filepath.Base(gitRoot)
	if rigName != originalName && !quickAddQuiet {
		fmt.Printf("Note: Using %q as rig name (sanitized from %q)\n", rigName, originalName)
	}

	if !quickAddQuiet {
		fmt.Printf("Adding %s to Gas Town...\n", style.Bold.Render(rigName))
		fmt.Printf("  Repository: %s\n", gitURL)
		fmt.Printf("  Town: %s\n", townRoot)
	}

	addArgs := []string{"rig", "add", rigName, gitURL}
	addCmd := exec.Command("gt", addArgs...)
	addCmd.Dir = townRoot
	addCmd.Stdout = os.Stdout
	addCmd.Stderr = os.Stderr
	if err := addCmd.Run(); err != nil {
		fmt.Printf("\n%s Failed to add rig. You can try manually:\n", style.Warning.Render("⚠"))
		fmt.Printf("  cd %s && gt rig add %s %s\n", townRoot, rigName, gitURL)
		return fmt.Errorf("gt rig add failed: %w", err)
	}

	user := quickAddUser
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" {
		user = "default"
	}

	if !quickAddQuiet {
		fmt.Printf("\nCreating crew workspace for %s...\n", user)
	}

	crewArgs := []string{"crew", "add", user, "--rig", rigName}
	crewCmd := exec.Command("gt", crewArgs...)
	crewCmd.Dir = filepath.Join(townRoot, rigName)
	crewCmd.Stdout = os.Stdout
	crewCmd.Stderr = os.Stderr
	if err := crewCmd.Run(); err != nil {
		fmt.Printf("  %s Could not create crew workspace: %v\n", style.Dim.Render("⚠"), err)
		fmt.Printf("  Run manually: cd %s && gt crew add %s --rig %s\n", filepath.Join(townRoot, rigName), user, rigName)
	}

	crewPath := filepath.Join(townRoot, rigName, "crew", user)
	if !quickAddQuiet {
		fmt.Printf("\n%s Added to Gas Town!\n", style.Success.Render("✓"))
		fmt.Printf("\nYour workspace: %s\n", style.Bold.Render(crewPath))
	}

	fmt.Printf("GT_CREW_PATH=%s\n", crewPath)

	return nil
}

func findGitRoot(path string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = path
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func findGitRemoteURL(gitRoot string) (string, error) {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = gitRoot
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func findOrCreateTown() (string, error) {
	// Priority 1: GT_TOWN_ROOT env var (explicit user preference)
	if townRoot := os.Getenv("GT_TOWN_ROOT"); townRoot != "" {
		if isValidTown(townRoot) {
			return townRoot, nil
		}
	}

	// Priority 2: Try to find from cwd (supports multiple town installations)
	if townRoot, err := workspace.FindFromCwd(); err == nil && townRoot != "" {
		return townRoot, nil
	}

	// Priority 3: Fall back to well-known locations
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	candidates := []string{
		filepath.Join(home, "gt"),
		filepath.Join(home, "gastown"),
	}

	for _, path := range candidates {
		if isValidTown(path) {
			return path, nil
		}
	}

	return "", fmt.Errorf("no Gas Town found - run 'gt install ~/gt' first")
}

// isValidTown checks if a path is a valid Gas Town installation.
func isValidTown(path string) bool {
	mayorDir := filepath.Join(path, "mayor")
	_, err := os.Stat(mayorDir)
	return err == nil
}
