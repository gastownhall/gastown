package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
)

// Polecat identity command flags
var (
	polecatIdentityListJSON    bool
	polecatIdentityShowJSON    bool
	polecatIdentityRemoveForce bool
)

var polecatIdentityCmd = &cobra.Command{
	Use:     "identity",
	Aliases: []string{"id"},
	Short:   "Manage polecat identities",
	Long: `Manage polecat identity beads in rigs.

Identity beads track polecat metadata, CV history, and lifecycle state.
Use subcommands to create, list, show, rename, or remove identities.`,
	RunE: requireSubcommand,
}

var polecatIdentityAddCmd = &cobra.Command{
	Use:   "add <rig> [name]",
	Short: "Create an identity bead for a polecat",
	Long: `Create an identity bead for a polecat in a rig.

If name is not provided, a name will be generated from the rig's name pool.

The identity bead tracks:
  - Role type (polecat)
  - Rig assignment
  - Agent state
  - Hook bead (current work)
  - Cleanup status

Example:
  gt polecat identity add gastown Toast
  gt polecat identity add gastown  # auto-generate name`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runPolecatIdentityAdd,
}

var polecatIdentityListCmd = &cobra.Command{
	Use:   "list <rig>",
	Short: "List polecat identity beads in a rig",
	Long: `List all polecat identity beads in a rig.

Shows:
  - Polecat name
  - Agent state
  - Current hook (if any)
  - Whether worktree exists

Example:
  gt polecat identity list gastown
  gt polecat identity list gastown --json`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatIdentityList,
}

var polecatIdentityShowCmd = &cobra.Command{
	Use:   "show <rig> <name>",
	Short: "Show polecat identity with CV summary",
	Long: `Show detailed identity information for a polecat including work history.

Displays:
  - Identity bead ID and creation date
  - Session count
  - Completion statistics (issues completed, failed, abandoned)
  - Language breakdown from file extensions
  - Work type breakdown (feat, fix, refactor, etc.)
  - Recent work list with relative timestamps

Examples:
  gt polecat identity show gastown Toast
  gt polecat identity show gastown Toast --json`,
	Args: cobra.ExactArgs(2),
	RunE: runPolecatIdentityShow,
}

var polecatIdentityRenameCmd = &cobra.Command{
	Use:   "rename <rig> <old-name> <new-name>",
	Short: "Rename a polecat identity (preserves CV)",
	Long: `Rename a polecat identity bead, preserving CV history.

The rename:
  1. Creates a new identity bead with the new name
  2. Copies CV history links to the new bead
  3. Closes the old bead with a reference to the new one

Safety checks:
  - Old identity must exist
  - New name must not already exist
  - Polecat session must not be running

Example:
  gt polecat identity rename gastown Toast Imperator`,
	Args: cobra.ExactArgs(3),
	RunE: runPolecatIdentityRename,
}

var polecatIdentityRemoveCmd = &cobra.Command{
	Use:   "remove <rig> <name>",
	Short: "Remove a polecat identity",
	Long: `Remove a polecat identity bead.

Safety checks:
  - No active tmux session
  - No work on hook (unless using --force)
  - Warns if CV exists

Use --force to bypass safety checks.

Example:
  gt polecat identity remove gastown Toast
  gt polecat identity remove gastown Toast --force`,
	Args: cobra.ExactArgs(2),
	RunE: runPolecatIdentityRemove,
}

func init() {
	// List flags
	polecatIdentityListCmd.Flags().BoolVar(&polecatIdentityListJSON, "json", false, "Output as JSON")

	// Show flags
	polecatIdentityShowCmd.Flags().BoolVar(&polecatIdentityShowJSON, "json", false, "Output as JSON")

	// Remove flags
	polecatIdentityRemoveCmd.Flags().BoolVarP(&polecatIdentityRemoveForce, "force", "f", false, "Force removal, bypassing safety checks")

	// Add subcommands to identity
	polecatIdentityCmd.AddCommand(polecatIdentityAddCmd)
	polecatIdentityCmd.AddCommand(polecatIdentityListCmd)
	polecatIdentityCmd.AddCommand(polecatIdentityShowCmd)
	polecatIdentityCmd.AddCommand(polecatIdentityRenameCmd)
	polecatIdentityCmd.AddCommand(polecatIdentityRemoveCmd)

	// Add identity to polecat command
	polecatCmd.AddCommand(polecatIdentityCmd)
}

// IdentityInfo holds identity bead information for display.
type IdentityInfo struct {
	Rig            string `json:"rig"`
	Name           string `json:"name"`
	BeadID         string `json:"bead_id"`
	AgentState     string `json:"agent_state,omitempty"`
	HookBead       string `json:"hook_bead,omitempty"`
	CleanupStatus  string `json:"cleanup_status,omitempty"`
	WorktreeExists bool   `json:"worktree_exists"`
	SessionRunning bool   `json:"session_running"`
}

// IdentityDetails holds detailed identity information for show command.
type IdentityDetails struct {
	IdentityInfo
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	CreatedAt   string   `json:"created_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
	CVBeads     []string `json:"cv_beads,omitempty"`
}

// CVSummary represents the CV/work history summary for a polecat.
type CVSummary struct {
	Identity         string           `json:"identity"`
	Created          string           `json:"created,omitempty"`
	Sessions         int              `json:"sessions"`
	IssuesCompleted  int              `json:"issues_completed"`
	IssuesFailed     int              `json:"issues_failed"`
	IssuesAbandoned  int              `json:"issues_abandoned"`
	Languages        map[string]int   `json:"languages,omitempty"`
	WorkTypes        map[string]int   `json:"work_types,omitempty"`
	AvgCompletionMin int              `json:"avg_completion_minutes,omitempty"`
	FirstPassRate    float64          `json:"first_pass_rate,omitempty"`
	RecentWork       []RecentWorkItem `json:"recent_work,omitempty"`
}

// RecentWorkItem represents a recent work item in the CV.
type RecentWorkItem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Type      string `json:"type,omitempty"`
	Completed string `json:"completed"`
	Ago       string `json:"ago"`
}

func runPolecatIdentityAdd(cmd *cobra.Command, args []string) error {
	rigName := args[0]
	var polecatName string

	if len(args) > 1 {
		polecatName = args[1]
	}

	// Get rig
	_, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	// Generate name if not provided
	if polecatName == "" {
		polecatGit := git.NewGit(r.Path)
		t := tmux.NewTmux()
		mgr := polecat.NewManager(r, polecatGit, t)
		polecatName, err = mgr.AllocateName()
		if err != nil {
			return fmt.Errorf("generating polecat name: %w", err)
		}
		fmt.Printf("Generated name: %s\n", polecatName)
	}

	// Check if identity already exists
	bd := polecatIdentityBeads(r)
	beadID := polecatBeadIDForRig(r, rigName, polecatName)
	existingIssue, _, _ := bd.GetAgentBead(beadID)
	if existingIssue != nil && existingIssue.Status != "closed" {
		return fmt.Errorf("identity bead %s already exists", beadID)
	}

	// Create identity bead
	fields := &beads.AgentFields{
		RoleType:   "polecat",
		Rig:        rigName,
		AgentState: "idle",
	}

	title := fmt.Sprintf("Polecat %s in %s", polecatName, rigName)
	issue, err := bd.CreateOrReopenAgentBead(beadID, title, fields)
	if err != nil {
		return fmt.Errorf("creating identity bead: %w", err)
	}

	fmt.Printf("%s Created identity bead: %s\n", style.SuccessPrefix, issue.ID)
	fmt.Printf("  Polecat: %s\n", polecatName)
	fmt.Printf("  Rig:     %s\n", rigName)

	return nil
}

func runPolecatIdentityList(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	// Get rig
	_, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	// Get all agent beads
	bd := polecatIdentityBeads(r)
	agentBeads, err := bd.ListAgentBeads()
	if err != nil {
		return fmt.Errorf("listing agent beads: %w", err)
	}

	// Filter for polecat beads in this rig
	identities := []IdentityInfo{} // Initialize to empty slice (not nil) for JSON
	t := tmux.NewTmux()
	polecatMgr := polecat.NewSessionManager(t, r)

	for id, issue := range agentBeads {
		// Parse the bead ID to check if it's a polecat for this rig
		beadRig, role, name, ok := beads.ParseAgentBeadID(id)
		if !ok || role != constants.RolePolecat || beadRig != rigName {
			continue
		}

		// Skip closed beads
		if issue.Status == "closed" {
			continue
		}

		fields := beads.ParseAgentFields(issue.Description)

		// Check if worktree exists
		worktreeExists := false
		mgr := polecat.NewManager(r, nil, t)
		if p, err := mgr.Get(name); err == nil && p != nil {
			worktreeExists = true
		}

		// Check if session is running
		sessionRunning, _ := polecatMgr.IsRunning(name)

		info := IdentityInfo{
			Rig:            rigName,
			Name:           name,
			BeadID:         id,
			AgentState:     fields.AgentState,
			HookBead:       issue.HookBead,
			CleanupStatus:  fields.CleanupStatus,
			WorktreeExists: worktreeExists,
			SessionRunning: sessionRunning,
		}
		if info.HookBead == "" {
			info.HookBead = fields.HookBead
		}
		identities = append(identities, info)
	}

	// JSON output
	if polecatIdentityListJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(identities)
	}

	// Human-readable output
	if len(identities) == 0 {
		fmt.Printf("No polecat identities found in %s.\n", rigName)
		return nil
	}

	fmt.Printf("%s\n\n", style.Bold.Render(fmt.Sprintf("Polecat Identities in %s", rigName)))

	for _, info := range identities {
		// Status indicators
		sessionIcon := style.Dim.Render("○")
		if info.SessionRunning {
			sessionIcon = style.Success.Render("●")
		}

		worktreeIcon := ""
		if info.WorktreeExists {
			worktreeIcon = " " + style.Dim.Render("[worktree]")
		}

		// Agent state with color
		stateStr := info.AgentState
		if stateStr == "" {
			stateStr = "unknown"
		}
		switch stateStr {
		case "working":
			stateStr = style.Info.Render(stateStr)
		case "done":
			stateStr = style.Success.Render(stateStr)
		case "stuck":
			stateStr = style.Warning.Render(stateStr)
		default:
			stateStr = style.Dim.Render(stateStr)
		}

		fmt.Printf("  %s %s  %s%s\n", sessionIcon, style.Bold.Render(info.Name), stateStr, worktreeIcon)

		if info.HookBead != "" {
			fmt.Printf("    Hook: %s\n", style.Dim.Render(info.HookBead))
		}
	}

	fmt.Printf("\n%d identity bead(s)\n", len(identities))
	return nil
}

func runPolecatIdentityShow(cmd *cobra.Command, args []string) error {
	rigName := args[0]
	polecatName := args[1]

	// Get rig
	_, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	// Get identity bead
	bd := polecatIdentityBeads(r)
	beadID := polecatBeadIDForRig(r, rigName, polecatName)
	issue, fields, err := bd.GetAgentBead(beadID)
	if err != nil {
		return fmt.Errorf("getting identity bead: %w", err)
	}
	if issue == nil {
		return fmt.Errorf("identity bead %s not found", beadID)
	}

	// Check worktree and session
	t := tmux.NewTmux()
	polecatMgr := polecat.NewSessionManager(t, r)
	mgr := polecat.NewManager(r, nil, t)

	worktreeExists := false
	var clonePath string
	if p, err := mgr.Get(polecatName); err == nil && p != nil {
		worktreeExists = true
		clonePath = p.ClonePath
	}
	sessionRunning, _ := polecatMgr.IsRunning(polecatName)

	// Build CV summary with enhanced analytics
	cv := buildCVSummary(r.Path, rigName, polecatName, beadID, clonePath)

	// JSON output - include both identity details and CV
	if polecatIdentityShowJSON {
		output := struct {
			IdentityInfo
			Title     string     `json:"title"`
			CreatedAt string     `json:"created_at,omitempty"`
			UpdatedAt string     `json:"updated_at,omitempty"`
			CV        *CVSummary `json:"cv,omitempty"`
		}{
			IdentityInfo: IdentityInfo{
				Rig:            rigName,
				Name:           polecatName,
				BeadID:         beadID,
				AgentState:     fields.AgentState,
				HookBead:       issue.HookBead,
				CleanupStatus:  fields.CleanupStatus,
				WorktreeExists: worktreeExists,
				SessionRunning: sessionRunning,
			},
			Title:     issue.Title,
			CreatedAt: issue.CreatedAt,
			UpdatedAt: issue.UpdatedAt,
			CV:        cv,
		}
		if output.HookBead == "" {
			output.HookBead = fields.HookBead
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	// Human-readable output
	fmt.Printf("\n%s %s/%s\n", style.Bold.Render("Identity:"), rigName, polecatName)
	fmt.Printf("  Bead ID:       %s\n", beadID)
	fmt.Printf("  Title:         %s\n", issue.Title)

	// Status
	sessionStr := style.Dim.Render("stopped")
	if sessionRunning {
		sessionStr = style.Success.Render("running")
	}
	fmt.Printf("  Session:       %s\n", sessionStr)

	worktreeStr := style.Dim.Render("no")
	if worktreeExists {
		worktreeStr = style.Success.Render("yes")
	}
	fmt.Printf("  Worktree:      %s\n", worktreeStr)

	// Agent state
	stateStr := fields.AgentState
	if stateStr == "" {
		stateStr = "unknown"
	}
	switch stateStr {
	case "working":
		stateStr = style.Info.Render(stateStr)
	case "done":
		stateStr = style.Success.Render(stateStr)
	case "stuck":
		stateStr = style.Warning.Render(stateStr)
	default:
		stateStr = style.Dim.Render(stateStr)
	}
	fmt.Printf("  Agent State:   %s\n", stateStr)

	// Hook
	hookBead := issue.HookBead
	if hookBead == "" {
		hookBead = fields.HookBead
	}
	if hookBead != "" {
		fmt.Printf("  Hook:          %s\n", hookBead)
	} else {
		fmt.Printf("  Hook:          %s\n", style.Dim.Render("(empty)"))
	}

	// Cleanup status
	if fields.CleanupStatus != "" {
		fmt.Printf("  Cleanup:       %s\n", fields.CleanupStatus)
	}

	// Timestamps
	if issue.CreatedAt != "" {
		fmt.Printf("  Created:       %s\n", style.Dim.Render(issue.CreatedAt))
	}
	if issue.UpdatedAt != "" {
		fmt.Printf("  Updated:       %s\n", style.Dim.Render(issue.UpdatedAt))
	}

	// CV Summary section with enhanced analytics
	fmt.Printf("\n%s\n", style.Bold.Render("CV Summary:"))
	fmt.Printf("  Sessions:         %d\n", cv.Sessions)
	fmt.Printf("  Issues completed: %s\n", style.Success.Render(fmt.Sprintf("%d", cv.IssuesCompleted)))
	fmt.Printf("  Issues failed:    %s\n", formatCountStyled(cv.IssuesFailed, style.Error))
	fmt.Printf("  Issues abandoned: %s\n", formatCountStyled(cv.IssuesAbandoned, style.Warning))

	// Language stats
	if len(cv.Languages) > 0 {
		fmt.Printf("\n  %s %s\n", style.Bold.Render("Languages:"), formatLanguageStats(cv.Languages))
	}

	// Work type stats
	if len(cv.WorkTypes) > 0 {
		fmt.Printf("  %s     %s\n", style.Bold.Render("Types:"), formatWorkTypeStats(cv.WorkTypes))
	}

	// Performance metrics
	if cv.AvgCompletionMin > 0 {
		fmt.Printf("\n  Avg completion time: %d minutes\n", cv.AvgCompletionMin)
	}
	if cv.FirstPassRate > 0 {
		fmt.Printf("  First-pass success:  %.0f%%\n", cv.FirstPassRate*100)
	}

	// Recent work
	if len(cv.RecentWork) > 0 {
		fmt.Printf("\n%s\n", style.Bold.Render("Recent work:"))
		for _, work := range cv.RecentWork {
			typeStr := ""
			if work.Type != "" {
				typeStr = work.Type + ": "
			}
			title := work.Title
			if len(title) > 40 {
				title = title[:37] + "..."
			}
			fmt.Printf("  %-10s %s%s  %s\n", work.ID, typeStr, title, style.Dim.Render(work.Ago))
		}
	}

	fmt.Println()
	return nil
}

func runPolecatIdentityRename(cmd *cobra.Command, args []string) error {
	rigName := args[0]
	oldName := args[1]
	newName := args[2]

	// Validate names
	if oldName == newName {
		return fmt.Errorf("old and new names are the same")
	}

	// Get rig
	_, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	bd := polecatIdentityBeads(r)
	oldBeadID := polecatBeadIDForRig(r, rigName, oldName)
	newBeadID := polecatBeadIDForRig(r, rigName, newName)

	// Check old identity exists
	oldIssue, oldFields, err := bd.GetAgentBead(oldBeadID)
	if err != nil {
		return fmt.Errorf("getting old identity bead: %w", err)
	}
	if oldIssue == nil || oldIssue.Status == "closed" {
		return fmt.Errorf("identity bead %s not found or already closed", oldBeadID)
	}

	// Check new identity doesn't exist
	newIssue, _, _ := bd.GetAgentBead(newBeadID)
	if newIssue != nil && newIssue.Status != "closed" {
		return fmt.Errorf("identity bead %s already exists", newBeadID)
	}

	// Safety check: no active session
	t := tmux.NewTmux()
	polecatMgr := polecat.NewSessionManager(t, r)
	running, _ := polecatMgr.IsRunning(oldName)
	if running {
		return fmt.Errorf("cannot rename: polecat session %s is running", oldName)
	}

	// Create new identity bead with inherited fields
	newFields := &beads.AgentFields{
		RoleType:          "polecat",
		Rig:               rigName,
		AgentState:        oldFields.AgentState,
		HookBead:          oldFields.HookBead,
		CleanupStatus:     oldFields.CleanupStatus,
		ActiveMR:          oldFields.ActiveMR,
		NotificationLevel: oldFields.NotificationLevel,
	}

	newTitle := fmt.Sprintf("Polecat %s in %s", newName, rigName)
	_, err = bd.CreateOrReopenAgentBead(newBeadID, newTitle, newFields)
	if err != nil {
		return fmt.Errorf("creating new identity bead: %w", err)
	}

	// Close old bead with reference to new one
	closeReason := fmt.Sprintf("renamed to %s", newBeadID)
	if err := bd.CloseWithReason(closeReason, oldBeadID); err != nil {
		// Try to clean up new bead
		_ = bd.CloseWithReason("rename failed", newBeadID)
		return fmt.Errorf("closing old identity bead: %w", err)
	}

	fmt.Printf("%s Renamed identity:\n", style.SuccessPrefix)
	fmt.Printf("  Old: %s\n", oldBeadID)
	fmt.Printf("  New: %s\n", newBeadID)
	fmt.Printf("\n%s Note: If a worktree exists for %s, you'll need to recreate it with the new name.\n",
		style.Warning.Render("⚠"), oldName)

	return nil
}

func runPolecatIdentityRemove(cmd *cobra.Command, args []string) error {
	rigName := args[0]
	polecatName := args[1]

	// Get rig
	_, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	bd := polecatIdentityBeads(r)
	beadID := polecatBeadIDForRig(r, rigName, polecatName)

	// Check identity exists
	issue, fields, err := bd.GetAgentBead(beadID)
	if err != nil {
		return fmt.Errorf("getting identity bead: %w", err)
	}
	if issue == nil {
		return fmt.Errorf("identity bead %s not found", beadID)
	}
	if issue.Status == "closed" {
		return fmt.Errorf("identity bead %s is already closed", beadID)
	}

	// Safety checks (unless --force)
	if !polecatIdentityRemoveForce {
		var reasons []string

		// Check for active session
		t := tmux.NewTmux()
		polecatMgr := polecat.NewSessionManager(t, r)
		running, _ := polecatMgr.IsRunning(polecatName)
		if running {
			reasons = append(reasons, "session is running")
		}

		// Check for work on hook
		hookBead := issue.HookBead
		if hookBead == "" && fields != nil {
			hookBead = fields.HookBead
		}
		if hookBead != "" {
			// Check if hooked bead is still open
			hookedIssue, _ := bd.Show(hookBead)
			if hookedIssue != nil && hookedIssue.Status != "closed" {
				reasons = append(reasons, fmt.Sprintf("has work on hook (%s)", hookBead))
			}
		}

		if len(reasons) > 0 {
			fmt.Printf("%s Cannot remove identity %s:\n", style.Error.Render("Error:"), beadID)
			for _, r := range reasons {
				fmt.Printf("  - %s\n", r)
			}
			fmt.Println("\nUse --force to bypass safety checks.")
			return fmt.Errorf("safety checks failed")
		}

		// Warn if CV exists
		assignee := fmt.Sprintf("%s/%s", rigName, polecatName)
		cvBeads, _ := bd.ListByAssignee(assignee)
		cvCount := 0
		for _, cv := range cvBeads {
			if cv.ID != beadID && cv.Status == "closed" {
				cvCount++
			}
		}
		if cvCount > 0 {
			fmt.Printf("%s Warning: This polecat has %d completed work item(s) in CV.\n",
				style.Warning.Render("⚠"), cvCount)
		}
	}

	// Close the identity bead
	if err := bd.CloseWithReason("removed via gt polecat identity remove", beadID); err != nil {
		return fmt.Errorf("closing identity bead: %w", err)
	}

	fmt.Printf("%s Removed identity bead: %s\n", style.SuccessPrefix, beadID)
	return nil
}

// buildCVSummary constructs the CV summary for a polecat.
// Returns a partial CV on errors rather than failing - CV data is best-effort.
func buildCVSummary(rigPath, rigName, polecatName, identityBeadID, clonePath string) *CVSummary {
	cv := &CVSummary{
		Identity:   identityBeadID,
		Languages:  make(map[string]int),
		WorkTypes:  make(map[string]int),
		RecentWork: []RecentWorkItem{},
	}

	// Use clonePath for beads queries (has proper redirect setup)
	// Fall back to rigPath if clonePath is empty
	beadsQueryPath := clonePath
	if beadsQueryPath == "" {
		beadsQueryPath = rigPath
	}

	// Get agent bead info for creation date
	bd := beads.New(beadsQueryPath).ForRoutedAgentBead()
	agentBead, _, err := bd.GetAgentBead(identityBeadID)
	if err == nil && agentBead != nil {
		if agentBead.CreatedAt != "" && len(agentBead.CreatedAt) >= 10 {
			cv.Created = agentBead.CreatedAt[:10] // Just the date part
		}
	}

	// Count sessions from checkpoint files (session history)
	cv.Sessions = countPolecatSessions(rigPath, polecatName)

	// Query completed issues assigned to this polecat
	assignee := fmt.Sprintf("%s/polecats/%s", rigName, polecatName)
	completedIssues, err := queryAssignedIssues(beadsQueryPath, assignee, "closed")
	if err == nil {
		cv.IssuesCompleted = len(completedIssues)

		// Extract work types from issue titles/types
		for _, issue := range completedIssues {
			workType := extractWorkType(issue.Title, issue.Type)
			if workType != "" {
				cv.WorkTypes[workType]++
			}

			// Add to recent work (limit to 5)
			if len(cv.RecentWork) < 5 {
				ago := formatRelativeTimeCV(issue.Updated)
				cv.RecentWork = append(cv.RecentWork, RecentWorkItem{
					ID:        issue.ID,
					Title:     issue.Title,
					Type:      workType,
					Completed: issue.Updated,
					Ago:       ago,
				})
			}
		}
	}

	// Query failed/escalated issues
	escalatedIssues, err := queryAssignedIssues(beadsQueryPath, assignee, "escalated")
	if err == nil {
		cv.IssuesFailed = len(escalatedIssues)
	}

	// Query abandoned issues (deferred)
	deferredIssues, err := queryAssignedIssues(beadsQueryPath, assignee, "deferred")
	if err == nil {
		cv.IssuesAbandoned = len(deferredIssues)
	}

	// Get language stats from git commits
	if clonePath != "" {
		langStats := getLanguageStats(clonePath)
		if len(langStats) > 0 {
			cv.Languages = langStats
		}
	}

	// Calculate first-pass success rate
	total := cv.IssuesCompleted + cv.IssuesFailed + cv.IssuesAbandoned
	if total > 0 {
		cv.FirstPassRate = float64(cv.IssuesCompleted) / float64(total)
	}

	return cv
}

// IssueInfo holds basic issue information for CV queries.
type IssueInfo struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Type    string `json:"issue_type"`
	Status  string `json:"status"`
	Updated string `json:"updated_at"`
}

// queryAssignedIssues queries beads for issues assigned to a specific agent.
func queryAssignedIssues(rigPath, assignee, status string) ([]IssueInfo, error) {
	// Use bd list with filters
	args := []string{"list", "--assignee=" + assignee, "--json"}
	if status != "" {
		args = append(args, "--status="+status)
	}

	cmd := exec.Command("bd", args...)
	cmd.Dir = rigPath
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	if len(out) == 0 {
		return []IssueInfo{}, nil
	}

	var issues []IssueInfo
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, err
	}

	// Sort by updated date (most recent first)
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].Updated > issues[j].Updated
	})

	return issues, nil
}

// extractWorkType extracts the work type from issue title or type.
func extractWorkType(title, issueType string) string {
	// Check explicit issue type first
	switch issueType {
	case "bug":
		return "fix"
	case "task", "feature":
		return "feat"
	case "epic":
		return "epic"
	}

	// Try to extract from conventional commit-style title
	title = strings.ToLower(title)
	prefixes := []string{"feat:", "fix:", "refactor:", "docs:", "test:", "chore:", "style:", "perf:"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(title, prefix) {
			return strings.TrimSuffix(prefix, ":")
		}
	}

	// Try to infer from keywords
	if strings.Contains(title, "fix") || strings.Contains(title, "bug") {
		return "fix"
	}
	if strings.Contains(title, "add") || strings.Contains(title, "implement") || strings.Contains(title, "create") {
		return "feat"
	}
	if strings.Contains(title, "refactor") || strings.Contains(title, "cleanup") {
		return "refactor"
	}

	return ""
}

// getLanguageStats analyzes git history to determine language distribution.
func getLanguageStats(clonePath string) map[string]int {
	stats := make(map[string]int)

	// Get list of files changed in commits by this author
	// We use git log with --name-only to get file names
	cmd := exec.Command("git", "log", "--name-only", "--pretty=format:", "--diff-filter=ACMR", "-100")
	cmd.Dir = clonePath
	out, err := cmd.Output()
	if err != nil {
		return stats
	}

	// Count file extensions
	extCount := make(map[string]int)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ext := filepath.Ext(line)
		if ext != "" {
			extCount[ext]++
		}
	}

	// Map extensions to languages
	extToLang := map[string]string{
		".go":    "Go",
		".ts":    "TypeScript",
		".tsx":   "TypeScript",
		".js":    "JavaScript",
		".jsx":   "JavaScript",
		".py":    "Python",
		".rs":    "Rust",
		".java":  "Java",
		".rb":    "Ruby",
		".c":     "C",
		".cpp":   "C++",
		".h":     "C",
		".hpp":   "C++",
		".cs":    "C#",
		".swift": "Swift",
		".kt":    "Kotlin",
		".scala": "Scala",
		".php":   "PHP",
		".sh":    "Shell",
		".bash":  "Shell",
		".zsh":   "Shell",
		".md":    "Markdown",
		".yaml":  "YAML",
		".yml":   "YAML",
		".json":  "JSON",
		".toml":  "TOML",
		".sql":   "SQL",
		".html":  "HTML",
		".css":   "CSS",
		".scss":  "SCSS",
	}

	for ext, count := range extCount {
		if lang, ok := extToLang[ext]; ok {
			stats[lang] += count
		}
	}

	return stats
}

// formatRelativeTimeCV returns a human-readable relative time string for CV display.
func formatRelativeTimeCV(timestamp string) string {
	// Try RFC3339 format with timezone (ISO 8601)
	t, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		// Try RFC3339Nano
		t, err = time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			// Try without timezone
			t, err = time.Parse("2006-01-02T15:04:05", timestamp)
			if err != nil {
				// Try alternative format
				t, err = time.Parse("2006-01-02 15:04:05", timestamp)
				if err != nil {
					// Try date only
					t, err = time.Parse("2006-01-02", timestamp)
					if err != nil {
						return ""
					}
				}
			}
		}
	}

	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", hours)
	case d < 7*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	default:
		weeks := int(d.Hours() / 24 / 7)
		if weeks == 1 {
			return "1w ago"
		}
		return fmt.Sprintf("%dw ago", weeks)
	}
}

// formatCountStyled formats a count with appropriate styling using lipgloss.Style.
func formatCountStyled(count int, s lipgloss.Style) string {
	if count == 0 {
		return style.Dim.Render("0")
	}
	return s.Render(strconv.Itoa(count))
}

// countPolecatSessions counts the number of sessions from checkpoint files.
func countPolecatSessions(rigPath, polecatName string) int {
	// Look for checkpoint files in the polecat's directory
	checkpointDir := filepath.Join(rigPath, "polecats", polecatName, ".checkpoints")
	entries, err := os.ReadDir(checkpointDir)
	if err != nil {
		// Also check at rig level
		checkpointDir = filepath.Join(rigPath, ".checkpoints")
		entries, err = os.ReadDir(checkpointDir)
		if err != nil {
			return 0
		}
	}

	// Count checkpoint files that contain this polecat's name
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.Contains(entry.Name(), polecatName) {
			count++
		}
	}

	// If no checkpoint files found, return at least 1 if polecat exists
	if count == 0 {
		return 1
	}
	return count
}

// formatLanguageStats formats language statistics for display.
func formatLanguageStats(langs map[string]int) string {
	// Sort by count descending
	type langCount struct {
		lang  string
		count int
	}
	var sorted []langCount
	for lang, count := range langs {
		sorted = append(sorted, langCount{lang, count})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].count > sorted[j].count
	})

	// Format top languages
	var parts []string
	for i, lc := range sorted {
		if i >= 3 { // Show top 3
			break
		}
		parts = append(parts, fmt.Sprintf("%s (%d)", lc.lang, lc.count))
	}
	return strings.Join(parts, ", ")
}

// formatWorkTypeStats formats work type statistics for display.
func formatWorkTypeStats(types map[string]int) string {
	// Sort by count descending
	type typeCount struct {
		typ   string
		count int
	}
	var sorted []typeCount
	for typ, count := range types {
		sorted = append(sorted, typeCount{typ, count})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].count > sorted[j].count
	})

	// Format all types
	var parts []string
	for _, tc := range sorted {
		parts = append(parts, fmt.Sprintf("%s (%d)", tc.typ, tc.count))
	}
	return strings.Join(parts, ", ")
}
