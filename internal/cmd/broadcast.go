package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	broadcastRig    string
	broadcastAll    bool
	broadcastDryRun bool
)

func init() {
	broadcastCmd.Flags().StringVar(&broadcastRig, "rig", "", "Only broadcast to workers in this rig")
	broadcastCmd.Flags().BoolVar(&broadcastAll, "all", false, "Include all agents (mayor, witness, etc.), not just workers")
	broadcastCmd.Flags().BoolVar(&broadcastDryRun, "dry-run", false, "Show what would be sent without sending")
	rootCmd.AddCommand(broadcastCmd)
}

var broadcastCmd = &cobra.Command{
	Use:     "broadcast <message>",
	GroupID: GroupComm,
	Short:   "Send a nudge message to all workers",
	Long: `Broadcasts a message to all active workers (polecats and crew).

By default, only workers (polecats and crew) receive the message.
Use --all to include infrastructure agents (mayor, deacon, witness, refinery).

The message is sent as a nudge to each worker's Claude Code session.

Examples:
  gt broadcast "Check your mail"
  gt broadcast --rig greenplace "New priority work available"
  gt broadcast --all "System maintenance in 5 minutes"
  gt broadcast --dry-run "Test message"`,
	Args: cobra.ExactArgs(1),
	RunE: runBroadcast,
}

func runBroadcast(cmd *cobra.Command, args []string) error {
	message := args[0]

	if message == "" {
		return fmt.Errorf("message cannot be empty")
	}

	// Get all agent sessions (including polecats)
	agents, err := getAgentSessions(true)
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}

	// Get sender identity to exclude self
	sender := os.Getenv("BD_ACTOR")

	// Filter to target agents
	var targets []*AgentSession
	for _, agent := range agents {
		// Filter by rig if specified
		if broadcastRig != "" && agent.Rig != broadcastRig {
			continue
		}

		// Unless --all, only include workers (crew + polecats)
		if !broadcastAll {
			if agent.Type != AgentCrew && agent.Type != AgentPolecat {
				continue
			}
		}

		// Skip self to avoid interrupting own session
		if sender != "" && formatAgentName(agent) == sender {
			continue
		}

		targets = append(targets, agent)
	}

	if len(targets) == 0 {
		fmt.Println("No workers running to broadcast to.")
		if broadcastRig != "" {
			fmt.Printf("  (filtered by rig: %s)\n", broadcastRig)
		}
		return nil
	}

	// Dry run - just show what would be sent
	if broadcastDryRun {
		fmt.Printf("Would broadcast to %d agent(s):\n\n", len(targets))
		for _, agent := range targets {
			fmt.Printf("  %s %s\n", AgentTypeIcons[agent.Type], formatAgentName(agent))
		}
		fmt.Printf("\nMessage: %s\n", message)
		return nil
	}

	// Send nudges
	t := tmux.NewTmux()
	townRoot, _ := workspace.FindFromCwd()
	var succeeded, failed, skipped int
	var failures []string

	fmt.Printf("Broadcasting to %d agent(s)...\n\n", len(targets))

	for i, agent := range targets {
		agentName := formatAgentName(agent)

		// Check DND status before nudging
		if townRoot != "" {
			if shouldSend, level, _ := shouldNudgeTarget(townRoot, agentName, false); !shouldSend {
				skipped++
				fmt.Printf("  %s %s %s (DND: %s)\n", style.Dim.Render("○"), AgentTypeIcons[agent.Type], agentName, level)
				continue
			}
		}

		if err := t.NudgeSession(agent.Name, message); err != nil {
			failed++
			failures = append(failures, fmt.Sprintf("%s: %v", agentName, err))
			fmt.Printf("  %s %s %s\n", style.ErrorPrefix, AgentTypeIcons[agent.Type], agentName)
		} else {
			succeeded++
			fmt.Printf("  %s %s %s\n", style.SuccessPrefix, AgentTypeIcons[agent.Type], agentName)
		}

		// Small delay between nudges to avoid overwhelming tmux
		if i < len(targets)-1 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	fmt.Println()
	if failed > 0 {
		summary := fmt.Sprintf("Broadcast complete: %d succeeded, %d failed", succeeded, failed)
		if skipped > 0 {
			summary += fmt.Sprintf(", %d skipped (DND)", skipped)
		}
		fmt.Printf("%s %s\n", style.WarningPrefix, summary)
		for _, f := range failures {
			fmt.Printf("  %s\n", style.Dim.Render(f))
		}
		return fmt.Errorf("%d nudge(s) failed", failed)
	}

	summary := fmt.Sprintf("Broadcast complete: %d agent(s) nudged", succeeded)
	if skipped > 0 {
		summary += fmt.Sprintf(", %d skipped (DND)", skipped)
	}
	fmt.Printf("%s %s\n", style.SuccessPrefix, summary)
	return nil
}

// formatAgentName returns a display name for an agent.
func formatAgentName(agent *AgentSession) string {
	switch agent.Type {
	case AgentMayor:
		return "mayor"
	case AgentDeacon:
		return "deacon"
	case AgentWitness:
		return fmt.Sprintf("%s/witness", agent.Rig)
	case AgentRefinery:
		return fmt.Sprintf("%s/refinery", agent.Rig)
	case AgentCrew:
		return fmt.Sprintf("%s/crew/%s", agent.Rig, agent.AgentName)
	case AgentPolecat:
		return fmt.Sprintf("%s/%s", agent.Rig, agent.AgentName)
	}
	return agent.Name
}
