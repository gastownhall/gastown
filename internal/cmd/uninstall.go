// ABOUTME: Command to completely uninstall Gas Town from the system.
// ABOUTME: Removes shell integration, wrappers, state, and optionally workspace.

package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/shell"
	"github.com/jonbaldie/gastown/internal/state"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/wrappers"
	"github.com/spf13/cobra"
)

var (
	uninstallWorkspace bool
	uninstallForce     bool
)

var uninstallCmd = &cobra.Command{
	Use:     "uninstall",
	GroupID: GroupConfig,
	Short:   "Remove Gas Town from the system",
	Long: `Completely remove Gas Town from the system.

By default, removes:
  - Shell integration (~/.zshrc or ~/.bashrc)
  - Wrapper scripts (~/bin/gt-codex, ~/bin/gt-gemini, ~/bin/gt-opencode)
  - State directory (~/.local/state/gastown/)
  - Config directory (~/.config/gastown/)
  - Cache directory (~/.cache/gastown/)

The workspace (e.g., ~/gt) is NOT removed unless --workspace is specified.

Use --force to skip confirmation prompts.

Examples:
  gt uninstall                    # Remove Gas Town, keep workspace
  gt uninstall --workspace        # Also remove workspace directory
  gt uninstall --force            # Skip confirmation`,
	RunE: runUninstall,
}

func init() {
	uninstallCmd.Flags().BoolVar(&uninstallWorkspace, "workspace", false,
		"Also remove the workspace directory (DESTRUCTIVE)")
	uninstallCmd.Flags().BoolVarP(&uninstallForce, "force", "f", false,
		"Skip confirmation prompts")
	rootCmd.AddCommand(uninstallCmd)
}

func runUninstall(cmd *cobra.Command, args []string) error {
	if !uninstallForce {
		fmt.Println("This will remove Gas Town from your system.")
		fmt.Println()
		fmt.Println("The following will be removed:")
		fmt.Printf("  • Shell integration (%s)\n", shell.RCFilePath(shell.DetectShell()))
		fmt.Printf("  • Wrapper scripts (%s)\n", wrappers.BinDir())
		fmt.Printf("  • State directory (%s)\n", state.StateDir())
		fmt.Printf("  • Config directory (%s)\n", state.ConfigDir())
		fmt.Printf("  • Cache directory (%s)\n", state.CacheDir())

		if uninstallWorkspace {
			fmt.Println()
			fmt.Printf("  %s WORKSPACE WILL BE DELETED\n", style.Warning.Render("⚠"))
			fmt.Println("     This cannot be undone!")
		}

		fmt.Println()
		fmt.Print("Continue? [y/N] ")

		reader := bufio.NewReader(os.Stdin)
		response, _ := reader.ReadString('\n')
		response = strings.TrimSpace(strings.ToLower(response))

		if response != "y" && response != "yes" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	var errors []string

	fmt.Println()
	fmt.Println("Removing Gas Town...")

	if err := shell.Remove(); err != nil {
		errors = append(errors, fmt.Sprintf("shell integration: %v", err))
	} else {
		fmt.Printf("  %s Removed shell integration\n", style.Success.Render("✓"))
	}

	if err := wrappers.Remove(); err != nil {
		errors = append(errors, fmt.Sprintf("wrapper scripts: %v", err))
	} else {
		fmt.Printf("  %s Removed wrapper scripts\n", style.Success.Render("✓"))
	}

	if err := os.RemoveAll(state.StateDir()); err != nil && !os.IsNotExist(err) {
		errors = append(errors, fmt.Sprintf("state directory: %v", err))
	} else {
		fmt.Printf("  %s Removed state directory\n", style.Success.Render("✓"))
	}

	if err := os.RemoveAll(state.ConfigDir()); err != nil && !os.IsNotExist(err) {
		errors = append(errors, fmt.Sprintf("config directory: %v", err))
	} else {
		fmt.Printf("  %s Removed config directory\n", style.Success.Render("✓"))
	}

	if err := os.RemoveAll(state.CacheDir()); err != nil && !os.IsNotExist(err) {
		errors = append(errors, fmt.Sprintf("cache directory: %v", err))
	} else {
		fmt.Printf("  %s Removed cache directory\n", style.Success.Render("✓"))
	}

	if uninstallWorkspace {
		workspaceDir := findWorkspaceForUninstall()
		if workspaceDir != "" {
			if err := os.RemoveAll(workspaceDir); err != nil {
				errors = append(errors, fmt.Sprintf("workspace: %v", err))
			} else {
				fmt.Printf("  %s Removed workspace: %s\n", style.Success.Render("✓"), workspaceDir)
			}
		}
	}

	if len(errors) > 0 {
		fmt.Println()
		fmt.Printf("%s Some components could not be removed:\n", style.Warning.Render("⚠"))
		for _, e := range errors {
			fmt.Printf("  • %s\n", e)
		}
		return fmt.Errorf("uninstall incomplete")
	}

	fmt.Println()
	fmt.Printf("%s Gas Town has been uninstalled\n", style.Success.Render("✓"))
	fmt.Println()
	fmt.Println("To reinstall, run:")
	fmt.Printf("  %s\n", style.Dim.Render("CGO_ENABLED=0 go install github.com/jonbaldie/gastown/cmd/gt@main"))
	fmt.Printf("  %s\n", style.Dim.Render("gt install ~/gt --shell"))

	return nil
}

func findWorkspaceForUninstall() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	candidates := []string{
		filepath.Join(home, "gt"),
		filepath.Join(home, "gastown"),
	}

	for _, path := range candidates {
		mayorDir := filepath.Join(path, "mayor")
		if _, err := os.Stat(mayorDir); err == nil {
			return path
		}
	}

	return ""
}
