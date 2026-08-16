package cmd

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/jonbaldie/gastown/internal/style"
	"github.com/spf13/cobra"
)

// Resume command checks for handoff messages.

var resumeCmd = &cobra.Command{
	Use:     "resume",
	GroupID: GroupWork,
	Short:   "Check for handoff messages",
	Long: `Check the inbox for handoff messages and display them for continuation.

The resume command checks for messages with "HANDOFF" in the subject
and displays them formatted for easy continuation.

Examples:
  gt resume    # Check inbox for handoff messages`,
	RunE: runResume,
}

func init() {
	rootCmd.AddCommand(resumeCmd)
}

func runResume(cmd *cobra.Command, args []string) error {
	return checkHandoffMessages()
}

// checkHandoffMessages checks the inbox for handoff messages and displays them.
func checkHandoffMessages() error {
	// Get inbox in JSON format
	inboxCmd := exec.Command("gt", "mail", "inbox", "--json")
	output, err := inboxCmd.Output()
	if err != nil {
		// Fallback to non-JSON if --json not supported
		inboxCmd = exec.Command("gt", "mail", "inbox")
		output, err = inboxCmd.Output()
		if err != nil {
			return fmt.Errorf("checking inbox: %w", err)
		}
		// Check for HANDOFF in output
		outputStr := string(output)
		if !containsHandoff(outputStr) {
			fmt.Printf("%s No handoff messages in inbox\n", style.Dim.Render("○"))
			fmt.Printf("  Handoff messages have 'HANDOFF' in the subject.\n")
			return nil
		}
		fmt.Printf("%s Found handoff message(s):\n\n", style.Bold.Render("🤝"))
		fmt.Println(outputStr)
		fmt.Printf("\n%s Read with: gt mail read <id>\n", style.Bold.Render("→"))
		return nil
	}

	// Parse JSON output to find handoff messages
	var messages []struct {
		ID      string `json:"id"`
		Subject string `json:"subject"`
		From    string `json:"from"`
		Date    string `json:"date"`
		Body    string `json:"body"`
	}
	if err := json.Unmarshal(output, &messages); err != nil {
		// JSON parse failed, use plain text output
		inboxCmd = exec.Command("gt", "mail", "inbox")
		output, err = inboxCmd.Output()
		if err != nil {
			return fmt.Errorf("fallback inbox check failed: %w", err)
		}
		outputStr := string(output)
		if containsHandoff(outputStr) {
			fmt.Printf("%s Found handoff message(s):\n\n", style.Bold.Render("🤝"))
			fmt.Println(outputStr)
		} else {
			fmt.Printf("%s No handoff messages in inbox\n", style.Dim.Render("○"))
		}
		return nil
	}

	// Find messages with HANDOFF in subject
	type handoffMsg struct {
		ID      string
		Subject string
		From    string
		Date    string
		Body    string
	}
	var handoffs []handoffMsg
	for _, msg := range messages {
		if containsHandoff(msg.Subject) {
			handoffs = append(handoffs, handoffMsg{
				ID:      msg.ID,
				Subject: msg.Subject,
				From:    msg.From,
				Date:    msg.Date,
				Body:    msg.Body,
			})
		}
	}

	if len(handoffs) == 0 {
		fmt.Printf("%s No handoff messages in inbox\n", style.Dim.Render("○"))
		fmt.Printf("  Handoff messages have 'HANDOFF' in the subject.\n")
		fmt.Printf("  Use 'gt handoff -s \"...\"' to create one when handing off.\n")
		return nil
	}

	fmt.Printf("%s Found %d handoff message(s):\n\n", style.Bold.Render("🤝"), len(handoffs))

	for i, msg := range handoffs {
		fmt.Printf("--- Handoff %d: %s ---\n", i+1, msg.ID)
		fmt.Printf("Subject: %s\n", msg.Subject)
		fmt.Printf("From: %s\n", msg.From)
		if msg.Date != "" {
			fmt.Printf("Date: %s\n", msg.Date)
		}
		if msg.Body != "" {
			fmt.Printf("\n%s\n", msg.Body)
		}
		fmt.Println()
	}

	if len(handoffs) == 1 {
		fmt.Printf("%s Read full message: gt mail read %s\n", style.Bold.Render("→"), handoffs[0].ID)
	} else {
		fmt.Printf("%s Read messages: gt mail read <id>\n", style.Bold.Render("→"))
	}
	fmt.Printf("%s Clear after reading: gt mail close <id>\n", style.Dim.Render("💡"))

	return nil
}

// containsHandoff checks if a string contains "HANDOFF" (case-insensitive).
func containsHandoff(s string) bool {
	upper := strings.ToUpper(s)
	return strings.Contains(upper, "HANDOFF")
}
