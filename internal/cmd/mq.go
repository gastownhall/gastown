package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
)

// MQ command flags
var (
	// Submit flags
	mqSubmitBranch    string
	mqSubmitIssue     string
	mqSubmitEpic      string
	mqSubmitPriority  int
	mqSubmitNoCleanup bool
	mqSubmitSkipDeps  bool
	mqSubmitResubmit  bool

	// Retry flags
	mqRetryNow bool

	// Reject flags
	mqRejectReason string
	mqRejectNotify bool
	mqRejectStdin  bool // Read reason from stdin

	// List command flags
	mqListReady  bool
	mqListStatus string
	mqListWorker string
	mqListEpic   string
	mqListJSON   bool
	mqListVerify bool

	// Status command flags
	mqStatusJSON bool

	// Integration land flags
	mqIntegrationLandForce     bool
	mqIntegrationLandSkipTests bool
	mqIntegrationLandDryRun    bool

	// Integration status flags
	mqIntegrationStatusJSON bool

	// Integration create flags
	mqIntegrationCreateBranch     string
	mqIntegrationCreateBaseBranch string
	mqIntegrationCreateForce      bool
)

var mqCmd = &cobra.Command{
	Use:     "mq",
	Aliases: []string{"mr"},
	GroupID: GroupWork,
	Short:   "Merge queue operations",
	RunE:    requireSubcommand,
	Long: `Manage merge requests and the merge queue for a rig.

Alias: 'gt mr' is equivalent to 'gt mq' (merge request vs merge queue).

The merge queue tracks work branches from polecats waiting to be merged.
Use these commands to view, submit, retry, and manage merge requests.`,
}

var mqSubmitCmd = &cobra.Command{
	Use:   "submit",
	Short: "Submit current branch to the merge queue",
	Long: `Submit the current branch to the merge queue.

Creates a merge-request bead that will be processed by the Refinery.

Auto-detection:
  - Branch: current git branch
  - Issue: parsed from branch name (e.g., polecat/Nux/gp-xyz → gt-xyz)
  - Worker: parsed from branch name
  - Rig: detected from current directory
  - Target: automatically determined (see below)
  - Priority: inherited from source issue

Target branch auto-detection:
  1. If --epic is specified: target the integration branch for <epic> (using configured template)
  2. If source issue has a parent epic with an integration branch: target it
  3. Otherwise: target main

This ensures batch work on epics automatically flows to integration branches.

Polecat auto-cleanup:
  When run from a polecat work branch (polecat/<worker>/<issue>), this command
  automatically triggers polecat shutdown after submitting the MR. The polecat
  sends a lifecycle request to its Witness and waits for termination.

  Use --no-cleanup to disable this behavior (e.g., if you want to submit
  multiple MRs or continue working).

Examples:
  gt mq submit                           # Auto-detect everything + auto-cleanup
  gt mq submit --issue gp-abc            # Explicit issue
  gt mq submit --epic gt-xyz             # Target integration branch explicitly
  gt mq submit --priority 0              # Override priority (P0)
  gt mq submit --no-cleanup              # Submit without auto-cleanup`,
	RunE: runMqSubmit,
}

var mqRetryCmd = &cobra.Command{
	Use:   "retry <rig> <mr-id>",
	Short: "Retry a failed merge request",
	Long: `Retry a failed merge request.

Resets a failed MR so it can be processed again by the refinery.
The MR must be in a failed state (open with an error).

Examples:
  gt mq retry greenplace gp-mr-abc123
  gt mq retry greenplace gp-mr-abc123 --now`,
	Args: cobra.ExactArgs(2),
	RunE: runMQRetry,
}

var mqListCmd = &cobra.Command{
	Use:   "list <rig>",
	Short: "Show the merge queue",
	Long: `Show the merge queue for a rig.

Lists all pending merge requests waiting to be processed.

Output format:
  ID          STATUS       PRIORITY  BRANCH                    WORKER  AGE
  gt-mr-001   ready        P0        polecat/Nux/gp-xyz        Nux     5m
  gt-mr-002   in_progress  P1        polecat/Toast/gt-abc      Toast   12m
  gt-mr-003   blocked      P1        polecat/Capable/gt-def    Capable 8m
              (waiting on gt-mr-001)

Examples:
  gt mq list greenplace
  gt mq list greenplace --ready
  gt mq list greenplace --status=open
  gt mq list greenplace --worker=Nux`,
	Args: cobra.ExactArgs(1),
	RunE: runMQList,
}

var mqRejectCmd = &cobra.Command{
	Use:   "reject <rig> <mr-id-or-branch>",
	Short: "Reject a merge request",
	Long: `Manually reject a merge request.

This closes the MR with a 'rejected' status without merging.
The source issue is NOT closed (work is not done).

Examples:
  gt mq reject greenplace polecat/Nux/gp-xyz --reason "Does not meet requirements"
  gt mq reject greenplace mr-Nux-12345 --reason "Superseded by other work" --notify`,
	Args: cobra.ExactArgs(2),
	RunE: runMQReject,
}

// Post-merge flags
var mqPostMergeSkipBranchDelete bool

var mqPostMergeCmd = &cobra.Command{
	Use:   "post-merge <rig> <mr-id>",
	Short: "Run post-merge cleanup (close MR, delete branch)",
	Long: `Perform post-merge cleanup after a successful merge.

This command consolidates post-merge steps into a single atomic operation:
  1. Close the MR bead (status: merged)
  2. Close the source issue
  3. Delete the remote polecat branch (unless --skip-branch-delete)

Designed for use by the refinery formula after a successful merge to main.
The branch name is read from the MR bead, so no manual branch argument is needed.

Examples:
  gt mq post-merge gastown gt-mr-abc123
  gt mq post-merge gastown gt-mr-abc123 --skip-branch-delete`,
	Args: cobra.ExactArgs(2),
	RunE: runMQPostMerge,
}

var mqStatusCmd = &cobra.Command{
	Use:   "status <id>",
	Short: "Show detailed merge request status",
	Long: `Display detailed information about a merge request.

Shows all MR fields, current status with timestamps, dependencies,
blockers, and processing history.

Example:
  gt mq status gp-mr-abc123`,
	Args: cobra.ExactArgs(1),
	RunE: runMqStatus,
}

var mqIntegrationCmd = &cobra.Command{
	Use:   "integration",
	Short: "Manage integration branches for epics",
	RunE:  requireSubcommand,
	Long: `Manage integration branches for batch work on epics.

Integration branches allow multiple MRs for an epic to target a shared
branch instead of main. After all epic work is complete, the integration
branch is landed to main as a single atomic unit.

Commands:
  create  Create an integration branch for an epic
  land    Merge integration branch to main
  status  Show integration branch status`,
}

var mqIntegrationCreateCmd = &cobra.Command{
	Use:   "create <epic-id>",
	Short: "Create an integration branch for an epic",
	Long: `Create an integration branch for batch work on an epic.

Creates a branch from main and pushes it to origin. Future MRs for this
epic's children can target this branch.

Branch naming:
  Default: integration/<sanitized-title> (e.g., integration/add-user-auth)
  Config:  Set merge_queue.integration_branch_template in rig settings
  Override: Use --branch flag for one-off customization

Template variables:
  {title}  - Sanitized epic title (e.g., "add-user-authentication")
  {epic}   - Full epic ID (e.g., "RA-123")
  {prefix} - Epic prefix before first hyphen (e.g., "RA")
  {user}   - Git user.name (e.g., "klauern")

If two epics produce the same branch name, a numeric suffix from the
epic ID is appended automatically (e.g., integration/add-auth-123).

Actions:
  1. Verify epic exists
  2. Create branch from main (using template or --branch)
  3. Push to origin
  4. Store actual branch name in epic metadata

Examples:
  gt mq integration create gt-auth-epic
  # Creates integration/add-user-authentication (from epic title)

  gt mq integration create RA-123 --branch "klauern/PROJ-1234/{epic}"
  # Creates klauern/PROJ-1234/RA-123`,
	Args: cobra.ExactArgs(1),
	RunE: runMqIntegrationCreate,
}

var mqIntegrationLandCmd = &cobra.Command{
	Use:   "land <epic-id>",
	Short: "Merge integration branch to main",
	Long: `Merge an epic's integration branch to main.

Lands all work for an epic by merging its integration branch to main
as a single atomic merge commit.

Actions:
  1. Verify all MRs targeting integration/<epic> are merged
  2. Verify integration branch exists
  3. Merge integration/<epic> to main (--no-ff)
  4. Run tests on main
  5. Push to origin
  6. Delete integration branch
  7. Update epic status

Options:
  --force       Land even if some MRs still open
  --skip-tests  Skip test run
  --dry-run     Preview only, make no changes

Examples:
  gt mq integration land gt-auth-epic
  gt mq integration land gt-auth-epic --dry-run
  gt mq integration land gt-auth-epic --force --skip-tests`,
	Args: cobra.ExactArgs(1),
	RunE: runMqIntegrationLand,
}

var mqIntegrationStatusCmd = &cobra.Command{
	Use:   "status <epic-id>",
	Short: "Show integration branch status for an epic",
	Long: `Display the status of an integration branch.

Shows:
  - Integration branch name and creation date
  - Number of commits ahead of main
  - Merged MRs (closed, targeting integration branch)
  - Pending MRs (open, targeting integration branch)

Example:
  gt mq integration status gt-auth-epic`,
	Args: cobra.ExactArgs(1),
	RunE: runMqIntegrationStatus,
}

func init() {
	// Submit flags
	mqSubmitCmd.Flags().StringVar(&mqSubmitBranch, "branch", "", "Source branch (default: current branch)")
	mqSubmitCmd.Flags().StringVar(&mqSubmitIssue, "issue", "", "Source issue ID (default: parse from branch name)")
	mqSubmitCmd.Flags().StringVar(&mqSubmitEpic, "epic", "", "Target epic's integration branch instead of main")
	mqSubmitCmd.Flags().IntVarP(&mqSubmitPriority, "priority", "p", -1, "Override priority (0-4, default: inherit from issue)")
	mqSubmitCmd.Flags().BoolVar(&mqSubmitNoCleanup, "no-cleanup", false, "Don't auto-cleanup after submit (for polecats)")
	mqSubmitCmd.Flags().BoolVar(&mqSubmitSkipDeps, "skip-deps", false, "Skip molecule step dependency check")
	mqSubmitCmd.Flags().BoolVar(&mqSubmitResubmit, "resubmit", false, "Resubmit after a fix (skips dependency check)")

	// Retry flags
	mqRetryCmd.Flags().BoolVar(&mqRetryNow, "now", false, "Immediately process instead of waiting for refinery loop")

	// List flags
	mqListCmd.Flags().BoolVar(&mqListReady, "ready", false, "Show only ready-to-merge (no blockers)")
	mqListCmd.Flags().StringVar(&mqListStatus, "status", "", "Filter by status (open, in_progress, closed)")
	mqListCmd.Flags().StringVar(&mqListWorker, "worker", "", "Filter by worker name")
	mqListCmd.Flags().StringVar(&mqListEpic, "epic", "", "Show MRs targeting integration/<epic>")
	mqListCmd.Flags().BoolVar(&mqListJSON, "json", false, "Output as JSON")
	mqListCmd.Flags().BoolVar(&mqListVerify, "verify", false, "Verify branches exist in git (shows MISSING for deleted branches)")

	// Reject flags
	mqRejectCmd.Flags().StringVarP(&mqRejectReason, "reason", "r", "", "Reason for rejection (required unless --stdin)")
	mqRejectCmd.Flags().BoolVar(&mqRejectNotify, "notify", false, "Send mail notification to worker")
	mqRejectCmd.Flags().BoolVar(&mqRejectStdin, "stdin", false, "Read reason from stdin (avoids shell quoting issues)")

	// Status flags
	mqStatusCmd.Flags().BoolVar(&mqStatusJSON, "json", false, "Output as JSON")

	// Post-merge flags
	mqPostMergeCmd.Flags().BoolVar(&mqPostMergeSkipBranchDelete, "skip-branch-delete", false, "Skip remote branch deletion")

	// Add subcommands
	mqCmd.AddCommand(mqSubmitCmd)
	mqCmd.AddCommand(mqRetryCmd)
	mqCmd.AddCommand(mqListCmd)
	mqCmd.AddCommand(mqRejectCmd)
	mqCmd.AddCommand(mqStatusCmd)
	mqCmd.AddCommand(mqPostMergeCmd)

	// Integration branch subcommands
	mqIntegrationCreateCmd.Flags().StringVar(&mqIntegrationCreateBranch, "branch", "", "Override branch name template (supports {title}, {epic}, {prefix}, {user})")
	mqIntegrationCreateCmd.Flags().StringVar(&mqIntegrationCreateBaseBranch, "base-branch", "", "Create integration branch from this branch instead of main")
	mqIntegrationCreateCmd.Flags().BoolVar(&mqIntegrationCreateForce, "force", false, "Recreate integration branch even if one already exists")
	mqIntegrationCmd.AddCommand(mqIntegrationCreateCmd)

	// Integration land flags
	mqIntegrationLandCmd.Flags().BoolVar(&mqIntegrationLandForce, "force", false, "Land even if some MRs still open")
	mqIntegrationLandCmd.Flags().BoolVar(&mqIntegrationLandSkipTests, "skip-tests", false, "Skip test run")
	mqIntegrationLandCmd.Flags().BoolVar(&mqIntegrationLandDryRun, "dry-run", false, "Preview only, make no changes")
	mqIntegrationCmd.AddCommand(mqIntegrationLandCmd)

	// Integration status flags
	mqIntegrationStatusCmd.Flags().BoolVar(&mqIntegrationStatusJSON, "json", false, "Output as JSON")
	mqIntegrationCmd.AddCommand(mqIntegrationStatusCmd)

	mqCmd.AddCommand(mqIntegrationCmd)

	rootCmd.AddCommand(mqCmd)
}

// findCurrentRig determines the current rig from the working directory.
// Returns the rig name and rig object, or an error if not in a rig.
func findCurrentRig(townRoot string) (string, *rig.Rig, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", nil, fmt.Errorf("getting current directory: %w", err)
	}

	// Get relative path from town root to cwd
	relPath, err := filepath.Rel(townRoot, cwd)
	if err != nil {
		return "", nil, fmt.Errorf("computing relative path: %w", err)
	}

	// The first component of the relative path should be the rig name
	parts := strings.Split(relPath, string(filepath.Separator))
	rigName := ""
	if len(parts) > 0 && parts[0] != "" && parts[0] != "." {
		rigName = parts[0]
	}

	// When gt is invoked via shell alias (cd ~/gt && gt), cwd is the town
	// root and relPath is ".". Fall back to GT_RIG env var.
	if rigName == "" {
		rigName = os.Getenv("GT_RIG")
	}
	if rigName == "" {
		return "", nil, fmt.Errorf("not inside a rig directory (and GT_RIG not set)")
	}

	// Load rig manager and get the rig
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(rigName)
	if err != nil {
		return "", nil, fmt.Errorf("rig '%s' not found: %w", rigName, err)
	}

	return rigName, r, nil
}

func runMQRetry(cmd *cobra.Command, args []string) error {
	rigName := args[0]
	mrID := args[1]

	mgr, _, _, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	// Get the MR first to show info
	mr, err := mgr.GetMR(mrID)
	if err != nil {
		if err == refinery.ErrMRNotFound {
			return fmt.Errorf("merge request '%s' not found in rig '%s'", mrID, rigName)
		}
		return fmt.Errorf("getting merge request: %w", err)
	}

	// Show what we're retrying
	fmt.Printf("Retrying merge request: %s\n", mrID)
	fmt.Printf("  Branch: %s\n", mr.Branch)
	fmt.Printf("  Worker: %s\n", mr.Worker)
	if mr.Error != "" {
		fmt.Printf("  Previous error: %s\n", style.Dim.Render(mr.Error))
	}

	// Perform the retry
	if err := mgr.Retry(mrID, mqRetryNow); err != nil {
		if err == refinery.ErrMRNotFailed {
			return fmt.Errorf("merge request '%s' has not failed (status: %s)", mrID, mr.Status)
		}
		return fmt.Errorf("retrying merge request: %w", err)
	}

	if mqRetryNow {
		fmt.Printf("%s Merge request processed\n", style.Bold.Render("✓"))
	} else {
		fmt.Printf("%s Merge request queued for retry\n", style.Bold.Render("✓"))
		fmt.Printf("  %s\n", style.Dim.Render("Will be processed on next refinery cycle"))
	}

	return nil
}

func runMQReject(cmd *cobra.Command, args []string) error {
	// Handle --stdin: read reason from stdin (avoids shell quoting issues)
	if mqRejectStdin {
		if mqRejectReason != "" {
			return fmt.Errorf("cannot use --stdin with --reason/-r")
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		mqRejectReason = strings.TrimRight(string(data), "\n")
	}

	// Require reason via --reason or --stdin
	if mqRejectReason == "" {
		return fmt.Errorf("required flag \"reason\" not set (use --reason/-r or --stdin)")
	}

	rigName := args[0]
	mrIDOrBranch := args[1]

	mgr, _, _, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	result, err := mgr.RejectMR(mrIDOrBranch, mqRejectReason, mqRejectNotify)
	if err != nil {
		return fmt.Errorf("rejecting MR: %w", err)
	}

	fmt.Printf("%s Rejected: %s\n", style.Bold.Render("✗"), result.Branch)
	fmt.Printf("  Worker: %s\n", result.Worker)
	fmt.Printf("  Reason: %s\n", mqRejectReason)

	if result.IssueID != "" {
		fmt.Printf("  Issue:  %s %s\n", result.IssueID, style.Dim.Render("(not closed - work not done)"))
	}

	if mqRejectNotify {
		fmt.Printf("  %s\n", style.Dim.Render("Worker notified via mail"))
	}

	return nil
}

func runMQPostMerge(_ *cobra.Command, args []string) error {
	rigName := args[0]
	mrID := args[1]

	mgr, r, _, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}

	// Run beads-level cleanup (close MR bead + source issue)
	result, err := mgr.PostMerge(mrID)
	if err != nil {
		return fmt.Errorf("post-merge cleanup: %w", err)
	}

	mr := result.MR
	fmt.Printf("%s Post-merge: %s\n", style.Bold.Render("✓"), mr.ID)
	fmt.Printf("  Branch: %s\n", mr.Branch)
	fmt.Printf("  Worker: %s\n", mr.Worker)

	if result.MRClosed {
		fmt.Printf("  %s MR closed (merged)\n", style.Success.Render("✓"))
	}
	if result.SourceIssueClosed {
		fmt.Printf("  %s Source issue closed: %s\n", style.Success.Render("✓"), result.SourceIssueID)
	} else if result.SourceIssueNotFound {
		fmt.Printf("  %s Source issue: %s %s\n", style.Dim.Render("○"), result.SourceIssueID, style.Dim.Render("(already closed or not found)"))
	}

	// Delete remote branch unless skipped
	if mr.Branch == "" {
		fmt.Printf("  %s No branch name in MR (skipping branch delete)\n", style.Dim.Render("○"))
		return nil
	}

	if mqPostMergeSkipBranchDelete {
		fmt.Printf("  %s Branch delete skipped (--skip-branch-delete)\n", style.Dim.Render("○"))
		return nil
	}

	// Check rig config for delete_merged_branches setting
	settingsPath := filepath.Join(r.Path, "settings", "config.json")
	deleteEnabled := true // default: delete merged branches
	if settings, err := config.LoadRigSettings(settingsPath); err == nil {
		if settings.MergeQueue != nil {
			deleteEnabled = settings.MergeQueue.IsDeleteMergedBranchesEnabled()
		}
	}

	if !deleteEnabled {
		fmt.Printf("  %s Branch delete disabled by config\n", style.Dim.Render("○"))
		return nil
	}

	// Get git client for the rig
	rigGit, err := getRigGit(r.Path)
	if err != nil {
		return fmt.Errorf("remote branch delete: %w", err)
	}

	// Delete remote branch — but skip if there's an open PR on it.
	// Deleting a branch with an open PR causes GitHub to auto-close the PR
	// as "closed" (not "merged"), destroying the PR audit trail. (gas-fk4)
	if rigGit.HasOpenPullRequest(git.PullRequestRef{URL: mr.PRURL, Number: mr.PRNumber, Branch: mr.Branch, HeadSHA: mr.CommitSHA}) {
		fmt.Printf("  %s Skipping remote branch delete for %s: open PR exists (gas-fk4)\n", style.Dim.Render("○"), mr.Branch)
	} else if err := rigGit.DeleteRemoteBranch("origin", mr.Branch); err != nil {
		return fmt.Errorf("remote branch delete %s: %w", mr.Branch, err)
	} else {
		fmt.Printf("  %s Deleted remote branch: %s\n", style.Success.Render("✓"), mr.Branch)
	}

	// Also clean up the local tracking ref if it exists
	if err := rigGit.DeleteBranch(mr.Branch, true); err != nil {
		// Not a warning — local branch often doesn't exist
		_ = err
	} else {
		fmt.Printf("  %s Deleted local branch: %s\n", style.Success.Render("✓"), mr.Branch)
	}

	return nil
}
