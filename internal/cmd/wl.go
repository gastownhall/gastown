package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/doltserver"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/wasteland"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

// wl command flags
var (
	wlJoinHandle      string
	wlJoinDisplayName string
)

var wlCmd = &cobra.Command{
	Use:     "wl",
	GroupID: GroupWork,
	Short:   "Wasteland federation commands",
	RunE:    requireSubcommand,
	Long: `Manage Wasteland federation — join communities, post work, earn reputation.

The Wasteland is a federation of Gas Towns via DoltHub. Each rig has a
sovereign fork of a shared commons database containing the wanted board
(open work), rig registry, and validated completions.

Getting started:
  gt wl join steveyegge/wl-commons   # Join the default wasteland

See https://github.com/jonbaldie/gastown for more information.`,
}

var wlJoinCmd = &cobra.Command{
	Use:   "join <upstream>",
	Short: "Join a wasteland by forking its commons",
	Long: `Join a wasteland community by forking its shared commons database.

This command:
  1. Forks the upstream commons to your DoltHub org
  2. Clones the fork locally
  3. Registers your rig in the rigs table
  4. Pushes the registration to your fork
  5. Saves wasteland configuration locally

The upstream argument is a DoltHub path like 'steveyegge/wl-commons'.

Required environment variables:
  DOLTHUB_TOKEN  - Your DoltHub API token
  DOLTHUB_ORG    - Your DoltHub organization name

Examples:
  gt wl join steveyegge/wl-commons
  gt wl join steveyegge/wl-commons --handle my-rig
  gt wl join steveyegge/wl-commons --display-name "Alice's Workshop"`,
	Args: cobra.ExactArgs(1),
	RunE: runWlJoin,
}

func init() {
	wlJoinCmd.Flags().StringVar(&wlJoinHandle, "handle", "", "Rig handle for registration (default: DoltHub org)")
	wlJoinCmd.Flags().StringVar(&wlJoinDisplayName, "display-name", "", "Display name for the rig registry")

	wlCmd.AddCommand(wlJoinCmd)
	rootCmd.AddCommand(wlCmd)
}

func runWlJoin(cmd *cobra.Command, args []string) error {
	upstream := args[0]

	// Parse upstream path (validate early)
	_, _, err := wasteland.ParseUpstream(upstream)
	if err != nil {
		return err
	}

	// Require DoltHub credentials
	token := doltserver.DoltHubToken()
	if token == "" {
		return fmt.Errorf("DOLTHUB_TOKEN environment variable is required\n\nGet your token from https://www.dolthub.com/settings/tokens")
	}

	forkOrg := doltserver.DoltHubOrg()
	if forkOrg == "" {
		return fmt.Errorf("DOLTHUB_ORG environment variable is required\n\nSet this to your DoltHub organization name")
	}

	// Find town root
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Fast path: check if already joined before loading town config.
	// This avoids failing on unrelated town-config errors for the no-op case.
	if existing, loadErr := wasteland.LoadConfig(townRoot); loadErr == nil {
		if existing.Upstream == upstream {
			fmt.Printf("%s Already joined wasteland: %s\n", style.Bold.Render("⚠"), upstream)
			fmt.Printf("  Handle: %s\n", existing.RigHandle)
			fmt.Printf("  Fork: %s/%s\n", existing.ForkOrg, existing.ForkDB)
			fmt.Printf("  Local: %s\n", existing.LocalDir)
			return nil
		}
		return fmt.Errorf("already joined to %s; run gt wl leave first", existing.Upstream)
	}

	// Load town config for identity (only needed for fresh join)
	townConfigPath := filepath.Join(townRoot, workspace.PrimaryMarker)
	townCfg, err := config.LoadTownConfig(townConfigPath)
	if err != nil {
		return fmt.Errorf("loading town config: %w", err)
	}

	// Determine town handle
	handle := wlJoinHandle
	if handle == "" {
		handle = forkOrg
	}

	displayName := wlJoinDisplayName
	if displayName == "" {
		if townCfg.PublicName != "" {
			displayName = townCfg.PublicName
		} else {
			displayName = townCfg.Name
		}
	}

	ownerEmail := townCfg.Owner
	gtVersion := "dev"

	svc := wasteland.NewService()
	svc.OnProgress = func(step string) {
		fmt.Printf("  %s\n", step)
	}

	fmt.Printf("Joining wasteland %s (fork to %s/%s)...\n", upstream, forkOrg, upstream[strings.Index(upstream, "/")+1:])
	cfg, err := svc.Join(upstream, forkOrg, token, handle, displayName, ownerEmail, gtVersion, townRoot)
	if err != nil {
		return err
	}

	fmt.Printf("\n%s Joined wasteland: %s\n", style.Bold.Render("✓"), upstream)
	fmt.Printf("  Handle: %s\n", cfg.RigHandle)
	fmt.Printf("  Fork: %s/%s\n", cfg.ForkOrg, cfg.ForkDB)
	fmt.Printf("  Local: %s\n", cfg.LocalDir)
	fmt.Printf("\n  %s\n", style.Dim.Render("Next: gt wl browse  — browse the wanted board"))
	return nil
}
