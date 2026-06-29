package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/style"
)

func runMQList(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	_, r, _, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	// Create beads wrapper for the rig - use BeadsPath() to get the git-synced location
	b := beads.New(r.BeadsPath())

	// Create git client for branch verification when --verify is set
	var gitClient *git.Git
	if mqListVerify {
		// Use the refinery's rig worktree to check branches
		refineryRigPath := filepath.Join(r.Path, "refinery", "rig")
		gitClient = git.NewGit(refineryRigPath)
	}

	// Build list options - query for merge-request label.
	// Use ListMergeRequests to query both the issues table and wisps table,
	// since MRs are created as ephemeral (wisps) by gt mq submit (GH#2446).
	// Priority -1 means no priority filter (otherwise 0 would filter to P0 only).
	opts := beads.ListOptions{
		Label:    "gt:merge-request",
		Priority: -1,
		Rig:      rigName,
	}

	// Apply status filter if specified
	if mqListStatus != "" {
		opts.Status = mqListStatus
	} else if !mqListReady {
		// Default to open if not showing ready
		opts.Status = "open"
	}

	var issues []*beads.Issue

	if mqListReady {
		// Query all open MRs and filter out blocked ones manually.
		// Cannot use b.Ready() because it excludes ephemeral beads,
		// and MRs are ephemeral by design (see gt-t5t6y).
		opts.Status = "open"
		allOpen, err := b.ListMergeRequests(opts)
		if err != nil {
			return fmt.Errorf("querying ready MRs: %w", err)
		}
		for _, issue := range allOpen {
			if !isMergeRequestReadyForSelection(issue) {
				continue
			}
			issues = append(issues, issue)
		}
	} else {
		issues, err = b.ListMergeRequests(opts)
		if err != nil {
			return fmt.Errorf("querying merge queue: %w", err)
		}
	}

	// Apply additional filters and calculate scores
	now := time.Now()
	type scoredIssue struct {
		issue           *beads.Issue
		fields          *beads.MRFields
		score           float64
		branchMissing   bool // true if branch doesn't exist in git (when --verify is set)
		branchVerifyErr bool // true if git check errored (corrupt repo, permission, etc.)
	}
	var scored []scoredIssue

	for _, issue := range issues {
		// Manual status filtering as workaround for bd list not respecting --status filter
		if mqListReady {
			// Ready view should only show open MRs
			if issue.Status != "open" {
				continue
			}
		} else if mqListStatus != "" && !strings.EqualFold(mqListStatus, "all") {
			// Explicit status filter should match exactly
			if !strings.EqualFold(issue.Status, mqListStatus) {
				continue
			}
		} else if mqListStatus == "" && issue.Status != "open" {
			// Default case (no status specified) should only show open
			continue
		}

		// Parse MR fields
		fields := beads.ParseMRFields(issue)

		// Filter by rig — wisps are shared across all rigs in the Dolt server,
		// so we must filter to only show MRs belonging to this rig.
		if fields != nil && fields.Rig != "" && !strings.EqualFold(fields.Rig, rigName) {
			continue
		}

		// Filter by worker
		if mqListWorker != "" {
			worker := ""
			if fields != nil {
				worker = fields.Worker
			}
			if !strings.EqualFold(worker, mqListWorker) {
				continue
			}
		}

		// Filter by epic (target branch)
		if mqListEpic != "" {
			target := ""
			if fields != nil {
				target = fields.Target
			}
			expectedTarget := resolveIntegrationBranchName(b, r.Path, mqListEpic)
			if target != expectedTarget {
				continue
			}
		}

		// Check branch existence if --verify is set (local + remote-tracking refs)
		branchMissing, branchVerifyErr := verifyBranch(mqListVerify, gitClient, fields)

		// Calculate priority score
		score := calculateMRScore(issue, fields, now)
		scored = append(scored, scoredIssue{issue: issue, fields: fields, score: score, branchMissing: branchMissing, branchVerifyErr: branchVerifyErr})
	}

	// Sort by score descending (highest priority first)
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	// Extract filtered issues for JSON output compatibility
	var filtered []*beads.Issue
	for _, s := range scored {
		filtered = append(filtered, s.issue)
	}

	// JSON output
	if mqListJSON {
		if mqListVerify {
			// Extend JSON with verification results
			type verifiedIssue struct {
				*beads.Issue
				BranchExists *bool `json:"branch_exists,omitempty"`
				VerifyError  bool  `json:"verify_error,omitempty"`
			}
			var verified []verifiedIssue
			for _, s := range scored {
				vi := verifiedIssue{Issue: s.issue}
				if s.fields != nil && s.fields.Branch != "" {
					if s.branchVerifyErr {
						vi.VerifyError = true
					} else {
						exists := !s.branchMissing
						vi.BranchExists = &exists
					}
				}
				verified = append(verified, vi)
			}
			return outputJSON(verified)
		}
		return outputJSON(filtered)
	}

	// Human-readable output
	fmt.Printf("%s Merge queue for '%s':\n\n", style.Bold.Render("📋"), rigName)

	if len(filtered) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(empty)"))
		return nil
	}

	// Create styled table - add GIT column when --verify is set
	table := style.NewTable(buildMQListColumns(mqListVerify)...)

	// Add rows using scored items (already sorted by score)
	for _, item := range scored {
		issue := item.issue
		fields := item.fields

		// Determine display status
		displayStatus := issue.Status
		if issue.Status == "open" {
			if beads.HasUnresolvedBlockers(issue) {
				displayStatus = "blocked"
			} else {
				displayStatus = "ready"
			}
		}

		// Format status with styling
		styledStatus := displayStatus
		switch displayStatus {
		case "ready":
			styledStatus = style.Success.Render("ready")
		case "in_progress":
			styledStatus = style.Warning.Render("active")
		case "blocked":
			styledStatus = style.Dim.Render("blocked")
		case "closed":
			styledStatus = style.Dim.Render("closed")
		}

		// Get MR fields
		branch := ""
		target := ""
		convoyID := ""
		if fields != nil {
			branch = fields.Branch
			target = fields.Target
			convoyID = fields.ConvoyID
		}
		if target == "" {
			target = style.Dim.Render("(unset)")
		}

		// Format convoy column
		convoyDisplay := style.Dim.Render("(none)")
		if convoyID != "" {
			// Truncate convoy ID for display
			if len(convoyID) > 12 {
				convoyID = convoyID[:12]
			}
			convoyDisplay = convoyID
		}

		// Format priority with color
		priority := fmt.Sprintf("P%d", issue.Priority)
		if issue.Priority <= 1 {
			priority = style.Error.Render(priority)
		} else if issue.Priority == 2 {
			priority = style.Warning.Render(priority)
		}

		// Format score
		scoreStr := fmt.Sprintf("%.1f", item.score)

		// Format branch status when --verify is set
		gitStatus := ""
		if mqListVerify {
			if item.branchVerifyErr {
				gitStatus = style.Warning.Render("ERR")
			} else if item.branchMissing {
				gitStatus = style.Error.Render("MISSING")
			} else {
				gitStatus = style.Success.Render("OK")
			}
		}

		// Calculate age
		age := formatMRAge(issue.CreatedAt)

		// Truncate ID if needed
		displayID := issue.ID
		if len(displayID) > 12 {
			displayID = displayID[:12]
		}

		// Build row with conditional GIT column
		if mqListVerify {
			table.AddRow(displayID, scoreStr, priority, convoyDisplay, branch, target, styledStatus, gitStatus, style.Dim.Render(age))
		} else {
			table.AddRow(displayID, scoreStr, priority, convoyDisplay, branch, target, styledStatus, style.Dim.Render(age))
		}
	}

	fmt.Print(table.Render())

	// Show summary of missing branches when --verify is set
	if mqListVerify {
		missingCount := 0
		for _, item := range scored {
			if item.branchMissing {
				missingCount++
			}
		}
		if missingCount > 0 {
			fmt.Printf("\n  %s %d MR(s) with missing branches\n",
				style.Error.Render("⚠"),
				missingCount)
		}
	}

	// Show blocking details below table
	for _, item := range scored {
		issue := item.issue
		displayStatus := issue.Status
		if issue.Status == "open" && beads.HasUnresolvedBlockers(issue) {
			displayStatus = "blocked"
		}
		if blockerID := beads.FirstUnresolvedBlockerID(issue); displayStatus == "blocked" && blockerID != "" {
			displayID := issue.ID
			if len(displayID) > 12 {
				displayID = displayID[:12]
			}
			fmt.Printf("  %s %s\n", style.Dim.Render(displayID+":"),
				style.Dim.Render(fmt.Sprintf("waiting on %s", blockerID)))
		}
	}

	return nil
}

// formatMRAge formats the age of an MR from its created_at timestamp.
func formatMRAge(createdAt string) string {
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		// Try other formats
		t, err = time.Parse("2006-01-02T15:04:05Z", createdAt)
		if err != nil {
			return "?"
		}
	}

	d := time.Since(t)

	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// outputJSON outputs data as JSON.
func outputJSON(data interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

func buildMQListColumns(verify bool) []style.Column {
	columns := []style.Column{
		{Name: "ID", Width: 12},
		{Name: "SCORE", Width: 7, Align: style.AlignRight},
		{Name: "PRI", Width: 4},
		{Name: "CONVOY", Width: 12},
		{Name: "BRANCH", Width: 24},
		{Name: "TARGET", Width: 24},
		{Name: "STATUS", Width: 10},
	}
	if verify {
		columns = append(columns, style.Column{Name: "GIT", Width: 8})
	}
	return append(columns, style.Column{Name: "AGE", Width: 6, Align: style.AlignRight})
}

// calculateMRScore computes the priority score for an MR using the refinery scoring function.
// Higher scores mean higher priority (process first).
func calculateMRScore(issue *beads.Issue, fields *beads.MRFields, now time.Time) float64 {
	// Parse MR creation time
	mrCreatedAt, err := time.Parse(time.RFC3339, issue.CreatedAt)
	if err != nil {
		mrCreatedAt, err = time.Parse("2006-01-02T15:04:05Z", issue.CreatedAt)
		if err != nil {
			mrCreatedAt = now // Fallback to now if parsing fails
		}
	}

	// Build score input
	input := refinery.ScoreInput{
		Priority:    issue.Priority,
		MRCreatedAt: mrCreatedAt,
		Now:         now,
	}

	// Add fields from MR metadata if available
	if fields != nil {
		input.RetryCount = fields.RetryCount

		// Parse convoy created at if available
		if fields.ConvoyCreatedAt != "" {
			if convoyTime, err := time.Parse(time.RFC3339, fields.ConvoyCreatedAt); err == nil {
				input.ConvoyCreatedAt = &convoyTime
			}
		}
	}

	return refinery.ScoreMRWithDefaults(input)
}

// branchVerifier abstracts git branch existence checks for testability.
type branchVerifier interface {
	BranchExists(branch string) (bool, error)
	RemoteTrackingBranchExists(remote, branch string) (bool, error)
}

// verifyBranch checks if a branch exists locally or as a remote-tracking ref.
// Returns (missing, verifyErr).
func verifyBranch(verify bool, client branchVerifier, fields *beads.MRFields) (bool, bool) {
	if !verify || client == nil || fields == nil || fields.Branch == "" {
		return false, false
	}
	localExists, err := client.BranchExists(fields.Branch)
	if err != nil {
		return false, true
	}
	if localExists {
		return false, false
	}
	// Also check remote-tracking ref (polecats often only have origin refs)
	remoteExists, rerr := client.RemoteTrackingBranchExists("origin", fields.Branch)
	if rerr != nil {
		return false, true
	}
	if !remoteExists {
		return true, false
	}
	return false, false
}
