package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/style"
)

var rigRoleCmd = &cobra.Command{
	Use:   "role",
	Short: "Configure agents and effort by role for one rig",
	Long: `Configure rig-specific agent profiles and thinking effort by role.

Rig role settings override town role settings for witness, refinery, polecat,
and crew sessions in the selected rig. Pi supports off, minimal, low, medium,
high, xhigh, and max thinking effort.`,
	RunE: requireSubcommand,
}

var rigRoleListCmd = &cobra.Command{
	Use:   "list <rig>",
	Short: "List effective role assignments for a rig",
	Args:  cobra.ExactArgs(1),
	RunE:  runRigRoleList,
}

var rigRoleSetCmd = &cobra.Command{
	Use:   "set <rig> <role> <agent> [effort]",
	Short: "Assign an agent profile and optional effort to a rig role",
	Long: `Assign an agent profile and optional thinking effort to a rig role.

Examples:
  gt rig role set sample witness pi-luna minimal
  gt rig role set sample polecat pi-luna xhigh`,
	Args: cobra.RangeArgs(3, 4),
	RunE: runRigRoleSet,
}

var rigRoleUnsetCmd = &cobra.Command{
	Use:   "unset <rig> <role>",
	Short: "Clear the agent and effort assigned to a rig role",
	Args:  cobra.ExactArgs(2),
	RunE:  runRigRoleUnset,
}

func init() {
	rigCmd.AddCommand(rigRoleCmd)
	rigRoleCmd.AddCommand(rigRoleListCmd)
	rigRoleCmd.AddCommand(rigRoleSetCmd)
	rigRoleCmd.AddCommand(rigRoleUnsetCmd)
}

func runRigRoleList(_ *cobra.Command, args []string) error {
	townRoot, selectedRig, err := getRig(args[0])
	if err != nil {
		return err
	}
	assignments, err := config.ResolveRigRoleAssignments(townRoot, selectedRig.Path)
	if err != nil {
		return err
	}

	fmt.Printf("%-10s %-20s %s\n", "ROLE", "AGENT", "EFFORT")
	for _, assignment := range assignments {
		agent := assignment.Agent
		if !assignment.RoleSpecific {
			agent += " (default)"
		}
		effort := assignment.Effort
		if effort == "" {
			effort = "runtime default"
		}
		fmt.Printf("%-10s %-20s %s\n", assignment.Role, agent, effort)
	}
	return nil
}

func runRigRoleSet(_ *cobra.Command, args []string) error {
	rigName, role, agent := args[0], args[1], args[2]
	townRoot, selectedRig, err := getRig(rigName)
	if err != nil {
		return err
	}
	effort := ""
	if len(args) == 4 {
		effort = args[3]
	}
	if err := config.SetRigRole(townRoot, selectedRig.Path, role, agent, effort); err != nil {
		return err
	}
	if effort == "" {
		fmt.Printf("Set rig %s role %s to agent %s (effort unchanged)\n",
			style.Bold.Render(rigName), style.Bold.Render(role), style.Bold.Render(agent))
	} else {
		fmt.Printf("Set rig %s role %s to agent %s with %s effort\n",
			style.Bold.Render(rigName), style.Bold.Render(role), style.Bold.Render(agent), style.Bold.Render(effort))
	}
	return nil
}

func runRigRoleUnset(_ *cobra.Command, args []string) error {
	rigName, role := args[0], args[1]
	_, selectedRig, err := getRig(rigName)
	if err != nil {
		return err
	}
	if err := config.UnsetRigRole(selectedRig.Path, role); err != nil {
		return err
	}
	fmt.Printf("Cleared rig %s role configuration for %s\n", style.Bold.Render(rigName), style.Bold.Render(role))
	return nil
}
