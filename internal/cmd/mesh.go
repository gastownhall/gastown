package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/workspace"
)

var meshCmd = &cobra.Command{
	Use:   "mesh [command] [args...]",
	Short: "GT Mesh federation — connect Gas Town instances",
	Long: `GT Mesh enables collaborative coding across multiple Gas Town instances.

The mesh plugin must be installed first:
  curl -fsSL https://raw.githubusercontent.com/Deepwork-AI/gt-mesh/main/install.sh | bash

Common commands:
  gt mesh init          Initialize this GT as a mesh node (coordinator)
  gt mesh join <code>   Join an existing mesh with an invite code
  gt mesh status        Show mesh status and connected peers
  gt mesh invite        Generate an invite code for a new peer
  gt mesh peers         List connected peers
  gt mesh send          Send a message to another GT
  gt mesh inbox         Check incoming mesh messages
  gt mesh feed          View mesh activity feed
  gt mesh sync          Force sync with DoltHub
  gt mesh help          Show all mesh commands

Plugin location: $GT_ROOT/.gt-plugins/mesh`,
	DisableFlagParsing: true,
	Args:               cobra.ArbitraryArgs,
	RunE:               runMesh,
}

func init() {
	meshCmd.GroupID = GroupWork
	rootCmd.AddCommand(meshCmd)
}

func runMesh(cmd *cobra.Command, args []string) error {
	// Handle help flags before delegation
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		return cmd.Help()
	}

	pluginPath, err := findMeshPlugin()
	if err != nil {
		return err
	}

	meshArgs := args
	proc := exec.Command(pluginPath, meshArgs...)
	proc.Stdin = os.Stdin
	proc.Stdout = os.Stdout
	proc.Stderr = os.Stderr
	proc.Env = append(os.Environ(), "GT_ROOT="+mustFindTownRoot())

	if err := proc.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("mesh: %w", err)
	}
	return nil
}

// findMeshPlugin locates the mesh plugin script, resolving symlinks so that
// the script runs from its real directory (needed for MESH_DIR detection).
// Checks $GT_ROOT/.gt-plugins/mesh first, then falls back to the
// default install location ~/gt/.gt-mesh/scripts/mesh.sh.
func findMeshPlugin() (string, error) {
	// Check $GT_ROOT/.gt-plugins/mesh (installed location)
	townRoot := mustFindTownRoot()
	if townRoot != "" {
		pluginPath := filepath.Join(townRoot, ".gt-plugins", "mesh")
		if resolved, err := filepath.EvalSymlinks(pluginPath); err == nil {
			return resolved, nil
		}
	}

	// Fall back to GT_ROOT env var
	if gtRoot := os.Getenv("GT_ROOT"); gtRoot != "" {
		pluginPath := filepath.Join(gtRoot, ".gt-plugins", "mesh")
		if resolved, err := filepath.EvalSymlinks(pluginPath); err == nil {
			return resolved, nil
		}
	}

	return "", fmt.Errorf(`gt mesh plugin not installed.

Install it with:
  curl -fsSL https://raw.githubusercontent.com/Deepwork-AI/gt-mesh/main/install.sh | bash

Or clone and install manually:
  git clone https://github.com/Deepwork-AI/gt-mesh.git ~/gt/.gt-mesh
  bash ~/gt/.gt-mesh/install.sh`)
}

// mustFindTownRoot returns the town root directory, or "" if not found.
func mustFindTownRoot() string {
	root, _ := workspace.FindFromCwd()
	return root
}
