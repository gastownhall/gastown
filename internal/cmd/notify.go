package cmd

import (
	"fmt"
	"os"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var notifyCmd = &cobra.Command{
	Use:     "notify [verbose|normal|muted]",
	GroupID: GroupComm,
	Short:   "Set notification level",
	Long: `Control the notification level for the current agent.

Notification levels:
  verbose  All notifications (mail, convoy events, status updates)
  normal   Important notifications only (default)
  muted    Silent/DND mode - batch notifications for later

Without arguments, shows the current notification level.

Examples:
  gt notify           # Show current level
  gt notify verbose   # Enable all notifications
  gt notify normal    # Default notification level
  gt notify muted     # Enable DND mode

Related: gt dnd - quick toggle for DND mode`,
	Args: cobra.MaximumNArgs(1),
	RunE: runNotify,
}

func init() {
	rootCmd.AddCommand(notifyCmd)
}

func runNotify(cmd *cobra.Command, args []string) error {
	// Get current agent bead ID
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	roleInfo, err := GetRoleWithContext(cwd, townRoot)
	if err != nil {
		return fmt.Errorf("determining role: %w", err)
	}

	ctx := RoleContext{
		Role:     roleInfo.Role,
		Rig:      roleInfo.Rig,
		Polecat:  roleInfo.Polecat,
		TownRoot: townRoot,
		WorkDir:  cwd,
	}

	agentBeadID := getAgentBeadID(ctx)
	if agentBeadID == "" {
		return fmt.Errorf("could not determine agent bead ID for role %s", roleInfo.Role)
	}

	bd := beads.New(townRoot)

	// Get current level
	currentLevel, err := bd.GetAgentNotificationLevel(agentBeadID)
	if err != nil {
		// Agent bead might not exist yet - default to normal
		currentLevel = beads.NotifyNormal
	}

	// No args: show current level
	if len(args) == 0 {
		showNotificationLevel(currentLevel)
		return nil
	}

	// Set new level
	newLevel := args[0]
	switch newLevel {
	case beads.NotifyVerbose, beads.NotifyNormal, beads.NotifyMuted:
		// Valid level
	default:
		return fmt.Errorf("invalid level %q: use verbose, normal, or muted", newLevel)
	}

	if err := bd.UpdateAgentNotificationLevel(agentBeadID, newLevel); err != nil {
		return fmt.Errorf("setting notification level: %w", err)
	}

	fmt.Printf("%s Notification level set to %s\n", style.SuccessPrefix, style.Bold.Render(newLevel))
	showNotificationLevelDescription(newLevel)

	return nil
}

func showNotificationLevel(level string) {
	if level == "" {
		level = beads.NotifyNormal
	}

	icon := "🔔"
	switch level {
	case beads.NotifyVerbose:
		icon = "🔊"
	case beads.NotifyMuted:
		icon = "🔕"
	}

	fmt.Printf("%s Notification level: %s\n", icon, style.Bold.Render(level))
	showNotificationLevelDescription(level)
}

func showNotificationLevelDescription(level string) {
	switch level {
	case beads.NotifyVerbose:
		fmt.Printf("  %s\n", style.Dim.Render("All notifications: mail, convoy events, status updates"))
	case beads.NotifyNormal:
		fmt.Printf("  %s\n", style.Dim.Render("Important notifications: convoy landed, escalations"))
	case beads.NotifyMuted:
		fmt.Printf("  %s\n", style.Dim.Render("Silent mode: notifications batched for later review"))
	}
}
