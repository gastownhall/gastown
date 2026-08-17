package cmd

import (
	"fmt"
	"os"

	"github.com/jonbaldie/gastown/internal/mayor"
	gtnow "github.com/jonbaldie/gastown/internal/now"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/spf13/cobra"
)

var (
	nowMayor          string
	nowWorkers        string
	nowTown           string
	nowName           string
	nowNoAttach       bool
	nowRestartWorkers bool
	nowProvisionOnly  bool
)

var nowCmd = &cobra.Command{
	Use:     "now [path]",
	GroupID: GroupWorkspace,
	Short:   "Start a Town for this git repository and attach to the Mayor",
	Long: `Start a working Town from a git repository in about five seconds.

gt now finds or creates a Town (default ~/gt), registers this repository
as a Rig without a network clone, sets the mix, starts Dolt, the daemon,
and the Mayor, then attaches this terminal to the Mayor session.

Examples:
  gt now
  gt now --mayor cursor:grok-4.6:high --workers cursor:grok-4.6:low
  gt now --mayor cursor:high --workers cursor:low --no-attach
  gt now --town /tmp/test-town --name my_proj

Success is: you are in the Mayor session.`,
	Args:         cobra.MaximumNArgs(1),
	RunE:         runNow,
	SilenceUsage: true,
}

func init() {
	nowCmd.Flags().StringVar(&nowMayor, "mayor", "", "Mayor/Deacon profile: runtime[:model[:effort]]")
	nowCmd.Flags().StringVar(&nowWorkers, "workers", "", "Worker profile: runtime[:model[:effort]]")
	nowCmd.Flags().StringVar(&nowTown, "town", "", "Town path (default $GT_TOWN_ROOT or ~/gt)")
	nowCmd.Flags().StringVar(&nowName, "name", "", "Rig name (default: directory name)")
	nowCmd.Flags().BoolVar(&nowNoAttach, "no-attach", false, "Start the Mayor session without attaching")
	nowCmd.Flags().BoolVar(&nowRestartWorkers, "restart-workers", false, "Restart Witness and Refinery so they pick up a new worker mix")
	nowCmd.Flags().BoolVar(&nowProvisionOnly, "provision-only", false, "Finish deferred Town provision (internal)")
	_ = nowCmd.Flags().MarkHidden("provision-only")
	rootCmd.AddCommand(nowCmd)
}

func runNow(cmd *cobra.Command, args []string) error {
	repoArg := ""
	if len(args) > 0 {
		repoArg = args[0]
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving gt executable: %w", err)
	}

	result, err := gtnow.Run(cmd.Context(), gtnow.Options{
		RepoArg:        repoArg,
		TownFlag:       nowTown,
		Name:           nowName,
		MayorSpec:      nowMayor,
		WorkersSpec:    nowWorkers,
		RestartWorkers: nowRestartWorkers,
		NoAttach:       nowNoAttach,
		ProvisionOnly:  nowProvisionOnly,
		Executable:     exe,
		Stdout:         cmd.OutOrStdout(),
		Stderr:         cmd.ErrOrStderr(),
	}, gtnow.Hooks{
		EnsureDoltReady: ensureInstallDoltReady,
		InitBeads:       initNowBeads,
		InitAgentBeads:  initTownAgentBeads,
		EnsureDaemon:    ensureDaemon,
	})
	if err != nil {
		return err
	}
	if nowProvisionOnly {
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "gt now: town=%s rig=%s mix=%s dolt=%d\n",
		result.TownRoot, result.RigName, result.Mix, result.DoltPort)

	if result.AttachSession == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "Mayor session is running.")
		return nil
	}

	if err := tmux.NewTmux().AttachSession(mayor.SessionName()); err != nil {
		return fmt.Errorf("attaching to Mayor session: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "You are in the Mayor session.")
	return nil
}
