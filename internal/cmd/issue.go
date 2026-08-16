package cmd

import (
	"fmt"
	"os"

	"github.com/jonbaldie/gastown/internal/session"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/spf13/cobra"
)

var issueCmd = &cobra.Command{
	Use:     "issue",
	GroupID: GroupConfig,
	Short:   "Manage current issue for status line display",
	Long: `Manage the current issue displayed in the tmux status line.

Sets, clears, or shows the active issue ID stored in the tmux session
environment. The status line uses this to display what you're working on.`,
}

var issueSetCmd = &cobra.Command{
	Use:   "set <issue-id>",
	Short: "Set the current issue (shown in tmux status line)",
	Long: `Set the current issue ID in the tmux session environment.

The issue ID appears in the tmux status line so you can see at a glance
what you're working on. Requires an active tmux session.`,
	Args: cobra.ExactArgs(1),
	RunE: runIssueSet,
}

var issueClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear the current issue from status line",
	Long: `Clear the current issue from the tmux session environment.

Removes the issue ID so it no longer appears in the tmux status line.`,
	RunE: runIssueClear,
}

var issueShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the current issue",
	Long: `Show the current issue ID from the tmux session environment.

Displays the issue ID currently set for the tmux status line, or
indicates that no issue is set.`,
	RunE: runIssueShow,
}

func init() {
	rootCmd.AddCommand(issueCmd)
	issueCmd.AddCommand(issueSetCmd)
	issueCmd.AddCommand(issueClearCmd)
	issueCmd.AddCommand(issueShowCmd)
}

func runIssueSet(cmd *cobra.Command, args []string) error {
	issueID := args[0]

	// Get current tmux session
	session := os.Getenv("TMUX_PANE")
	if session == "" {
		// Try to detect from GT env vars
		session = detectCurrentSession()
		if session == "" {
			return fmt.Errorf("not in a tmux session")
		}
	}

	t := tmux.NewTmux()
	if err := t.SetEnvironment(session, "GT_ISSUE", issueID); err != nil {
		return fmt.Errorf("setting issue: %w", err)
	}

	fmt.Printf("Issue set to: %s\n", issueID)
	return nil
}

func runIssueClear(cmd *cobra.Command, args []string) error {
	session := os.Getenv("TMUX_PANE")
	if session == "" {
		session = detectCurrentSession()
		if session == "" {
			return fmt.Errorf("not in a tmux session")
		}
	}

	t := tmux.NewTmux()
	// Set to empty string to clear
	if err := t.SetEnvironment(session, "GT_ISSUE", ""); err != nil {
		return fmt.Errorf("clearing issue: %w", err)
	}

	fmt.Println("Issue cleared")
	return nil
}

func runIssueShow(cmd *cobra.Command, args []string) error {
	session := os.Getenv("TMUX_PANE")
	if session == "" {
		session = detectCurrentSession()
		if session == "" {
			return fmt.Errorf("not in a tmux session")
		}
	}

	t := tmux.NewTmux()
	issue, err := t.GetEnvironment(session, "GT_ISSUE")
	if err != nil {
		return fmt.Errorf("getting issue: %w", err)
	}

	if issue == "" {
		fmt.Println("No issue set")
	} else {
		fmt.Printf("Current issue: %s\n", issue)
	}
	return nil
}

// detectCurrentSession tries to find the tmux session name from env.
func detectCurrentSession() string {
	// Try to build session name from GT env vars
	role := os.Getenv("GT_ROLE")
	rig := os.Getenv("GT_RIG")
	polecat := os.Getenv("GT_POLECAT")
	crew := os.Getenv("GT_CREW")

	// Gate polecat path on GT_ROLE: coordinators may have stale GT_POLECAT.
	if rig != "" {
		if polecat != "" {
			parsedRole, _, _ := parseRoleString(role)
			if role == "" || parsedRole == RolePolecat {
				return session.PolecatSessionName(session.PrefixFor(rig), polecat)
			}
		}
		if crew != "" {
			return session.CrewSessionName(session.PrefixFor(rig), crew)
		}
	}

	// Check if we're mayor (handles both bare and compound forms)
	parsedRole, _, _ := parseRoleString(role)
	if parsedRole == RoleMayor {
		return getMayorSessionName()
	}

	return ""
}
