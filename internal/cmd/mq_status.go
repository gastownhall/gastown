package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/spf13/cobra"
)

// MRStatusOutput is the JSON output structure for gt mq status.
type MRStatusOutput struct {
	// Core issue fields
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Priority  int    `json:"priority"`
	Type      string `json:"type"`
	Assignee  string `json:"assignee,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	ClosedAt  string `json:"closed_at,omitempty"`

	// MR-specific fields
	Branch      string `json:"branch,omitempty"`
	Target      string `json:"target,omitempty"`
	SourceIssue string `json:"source_issue,omitempty"`
	Worker      string `json:"worker,omitempty"`
	Rig         string `json:"rig,omitempty"`
	MergeCommit string `json:"merge_commit,omitempty"`
	CloseReason string `json:"close_reason,omitempty"`

	// Dependencies
	DependsOn []DependencyInfo `json:"depends_on,omitempty"`
	Blocks    []DependencyInfo `json:"blocks,omitempty"`
}

// DependencyInfo represents a dependency or blocker.
type DependencyInfo struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority int    `json:"priority"`
	Type     string `json:"type"`
}

func runMqStatus(cmd *cobra.Command, args []string) error {
	mrID := args[0]

	// Use current working directory for beads operations
	// (beads repos are per-rig, not per-workspace)
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// Initialize beads client
	bd := beads.New(workDir)

	// Fetch the issue
	issue, err := bd.Show(mrID)
	if err != nil {
		if err == beads.ErrNotFound {
			return fmt.Errorf("merge request '%s' not found", mrID)
		}
		return fmt.Errorf("fetching merge request: %w", err)
	}

	// Parse MR-specific fields from description
	mrFields := beads.ParseMRFields(issue)

	// Build output structure
	output := MRStatusOutput{
		ID:        issue.ID,
		Title:     issue.Title,
		Status:    issue.Status,
		Priority:  issue.Priority,
		Type:      issue.Type,
		Assignee:  issue.Assignee,
		CreatedAt: issue.CreatedAt,
		UpdatedAt: issue.UpdatedAt,
		ClosedAt:  issue.ClosedAt,
	}

	// Add MR fields if present
	if mrFields != nil {
		output.Branch = mrFields.Branch
		output.Target = mrFields.Target
		output.SourceIssue = mrFields.SourceIssue
		output.Worker = mrFields.Worker
		output.Rig = mrFields.Rig
		output.MergeCommit = mrFields.MergeCommit
		output.CloseReason = mrFields.CloseReason
	}

	// Add dependency info from the issue's Dependencies field
	for _, dep := range issue.Dependencies {
		output.DependsOn = append(output.DependsOn, DependencyInfo{
			ID:       dep.ID,
			Title:    dep.Title,
			Status:   dep.Status,
			Priority: dep.Priority,
			Type:     dep.Type,
		})
	}

	// Add blocker info from the issue's Dependents field
	for _, dep := range issue.Dependents {
		output.Blocks = append(output.Blocks, DependencyInfo{
			ID:       dep.ID,
			Title:    dep.Title,
			Status:   dep.Status,
			Priority: dep.Priority,
			Type:     dep.Type,
		})
	}

	// JSON output
	if mqStatusJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	// Human-readable output
	return printMqStatus(issue, mrFields)
}

// printMqStatus prints detailed MR status in human-readable format.
func printMqStatus(issue *beads.Issue, mrFields *beads.MRFields) error {
	// Header
	fmt.Printf("%s %s\n", style.Bold.Render("📋 Merge Request:"), issue.ID)
	fmt.Printf("   %s\n\n", issue.Title)

	// Status section
	fmt.Printf("%s\n", style.Bold.Render("Status"))
	statusDisplay := formatStatus(issue.Status)
	fmt.Printf("   State:    %s\n", statusDisplay)
	fmt.Printf("   Priority: P%d\n", issue.Priority)
	if issue.Type != "" {
		fmt.Printf("   Type:     %s\n", issue.Type)
	}
	if issue.Assignee != "" {
		fmt.Printf("   Assignee: %s\n", issue.Assignee)
	}

	// Timestamps
	fmt.Printf("\n%s\n", style.Bold.Render("Timeline"))
	if issue.CreatedAt != "" {
		fmt.Printf("   Created: %s %s\n", issue.CreatedAt, formatTimeAgo(issue.CreatedAt))
	}
	if issue.UpdatedAt != "" && issue.UpdatedAt != issue.CreatedAt {
		fmt.Printf("   Updated: %s %s\n", issue.UpdatedAt, formatTimeAgo(issue.UpdatedAt))
	}
	if issue.ClosedAt != "" {
		fmt.Printf("   Closed:  %s %s\n", issue.ClosedAt, formatTimeAgo(issue.ClosedAt))
	}

	// MR-specific fields
	if mrFields != nil {
		fmt.Printf("\n%s\n", style.Bold.Render("Merge Details"))
		if mrFields.Branch != "" {
			fmt.Printf("   Branch:       %s\n", mrFields.Branch)
		}
		if mrFields.Target != "" {
			fmt.Printf("   Target:       %s\n", mrFields.Target)
		}
		if mrFields.SourceIssue != "" {
			fmt.Printf("   Source Issue: %s\n", mrFields.SourceIssue)
		}
		if mrFields.Worker != "" {
			fmt.Printf("   Worker:       %s\n", mrFields.Worker)
		}
		if mrFields.Rig != "" {
			fmt.Printf("   Rig:          %s\n", mrFields.Rig)
		}
		if mrFields.MergeCommit != "" {
			fmt.Printf("   Merge Commit: %s\n", mrFields.MergeCommit)
		}
		if mrFields.CloseReason != "" {
			fmt.Printf("   Close Reason: %s\n", mrFields.CloseReason)
		}
	}

	// Dependencies (what this MR is waiting on)
	if len(issue.Dependencies) > 0 {
		fmt.Printf("\n%s\n", style.Bold.Render("Waiting On"))
		for _, dep := range issue.Dependencies {
			statusIcon := getStatusIcon(dep.Status)
			fmt.Printf("   %s %s: %s %s\n",
				statusIcon,
				dep.ID,
				truncateString(dep.Title, 50),
				style.Dim.Render(fmt.Sprintf("[%s]", dep.Status)))
		}
	}

	// Blockers (what's waiting on this MR)
	if len(issue.Dependents) > 0 {
		fmt.Printf("\n%s\n", style.Bold.Render("Blocking"))
		for _, dep := range issue.Dependents {
			statusIcon := getStatusIcon(dep.Status)
			fmt.Printf("   %s %s: %s %s\n",
				statusIcon,
				dep.ID,
				truncateString(dep.Title, 50),
				style.Dim.Render(fmt.Sprintf("[%s]", dep.Status)))
		}
	}

	// Description (if present and not just MR fields)
	desc := getDescriptionWithoutMRFields(issue.Description)
	if desc != "" {
		fmt.Printf("\n%s\n", style.Bold.Render("Notes"))
		// Indent each line
		for _, line := range strings.Split(desc, "\n") {
			fmt.Printf("   %s\n", line)
		}
	}

	return nil
}

// formatStatus formats the status with appropriate styling.
func formatStatus(status string) string {
	switch status {
	case "open":
		return style.Info.Render("● open")
	case "in_progress":
		return style.Bold.Render("▶ in_progress")
	case "closed":
		return style.Dim.Render("✓ closed")
	default:
		return status
	}
}

// getStatusIcon returns an icon for the given status.
func getStatusIcon(status string) string {
	switch status {
	case "open":
		return "○"
	case "in_progress":
		return "▶"
	case "closed":
		return "✓"
	default:
		return "•"
	}
}

// formatTimeAgo formats a timestamp as a relative time string.
func formatTimeAgo(timestamp string) string {
	// Try parsing common formats
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}

	var t time.Time
	var err error
	for _, format := range formats {
		t, err = time.Parse(format, timestamp)
		if err == nil {
			break
		}
	}
	if err != nil {
		return "" // Can't parse, return empty
	}

	// Compare in UTC: Dolt stores timestamps in UTC, and time.Parse
	// without timezone info returns UTC times. Using time.Since (which
	// uses local time) caused false "in the future" for UTC timestamps
	// that appear to be tomorrow when local time is still today (gt-ty4).
	d := time.Now().UTC().Sub(t.UTC())
	if d < 0 {
		return style.Dim.Render("(in the future)")
	}

	var ago string
	if d < time.Minute {
		ago = fmt.Sprintf("%ds ago", int(d.Seconds()))
	} else if d < time.Hour {
		ago = fmt.Sprintf("%dm ago", int(d.Minutes()))
	} else if d < 24*time.Hour {
		ago = fmt.Sprintf("%dh ago", int(d.Hours()))
	} else {
		ago = fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}

	return style.Dim.Render("(" + ago + ")")
}

// truncateString truncates a string to maxLen, adding "..." if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// getDescriptionWithoutMRFields returns the description with MR field lines removed.
func getDescriptionWithoutMRFields(description string) string {
	if description == "" {
		return ""
	}

	// Known MR field keys (lowercase)
	mrKeys := map[string]bool{
		"branch":       true,
		"target":       true,
		"source_issue": true,
		"source-issue": true,
		"sourceissue":  true,
		"worker":       true,
		"rig":          true,
		"merge_commit": true,
		"merge-commit": true,
		"mergecommit":  true,
		"close_reason": true,
		"close-reason": true,
		"closereason":  true,
		"type":         true,
	}

	var lines []string
	for _, line := range strings.Split(description, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			lines = append(lines, line)
			continue
		}

		// Check if this is an MR field line
		colonIdx := strings.Index(trimmed, ":")
		if colonIdx != -1 {
			key := strings.ToLower(strings.TrimSpace(trimmed[:colonIdx]))
			if mrKeys[key] {
				continue // Skip MR field lines
			}
		}

		lines = append(lines, line)
	}

	// Trim leading/trailing blank lines
	result := strings.Join(lines, "\n")
	result = strings.TrimSpace(result)
	return result
}
