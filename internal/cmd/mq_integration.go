package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// defaultIntegrationBranchTemplate is kept for local backward compat references.
var defaultIntegrationBranchTemplate = beads.DefaultIntegrationBranchTemplate

// LandConflictError is returned by `gt mq integration land` when the merge of
// an integration branch into its target branch fails due to conflicts. Callers
// (notably the refinery patrol formula) can detect this typed error to drive a
// structured recovery path — escalate to mayor, file a conflict-resolution
// bead, and stop the patrol cycle for the epic — instead of parsing message
// text. The temp .land-worktree is always cleaned up before this is returned.
type LandConflictError struct {
	EpicID        string
	Branch        string
	TargetBranch  string
	ConflictPaths []string
	Underlying    error
}

func (e *LandConflictError) Error() string {
	if len(e.ConflictPaths) == 0 {
		return fmt.Sprintf("merge of %s into %s failed with conflict: %v", e.Branch, e.TargetBranch, e.Underlying)
	}
	return fmt.Sprintf("merge of %s into %s failed with conflict in %d file(s): %s",
		e.Branch, e.TargetBranch, len(e.ConflictPaths), strings.Join(e.ConflictPaths, ", "))
}

func (e *LandConflictError) Unwrap() error { return e.Underlying }

// invalidBranchCharsRegex matches characters that are invalid in git branch names.
// Git branch names cannot contain: ~ ^ : \ ? * [ space, .., @{, or end with .lock
var invalidBranchCharsRegex = regexp.MustCompile(`[~^:\s\\?*\[]|\.\.|\.\.|@\{`)

// buildIntegrationBranchName wraps beads.BuildIntegrationBranchName for local callers.
func buildIntegrationBranchName(template, epicID, epicTitle string) string {
	return beads.BuildIntegrationBranchName(template, epicID, epicTitle)
}

// extractEpicPrefix wraps beads.ExtractEpicPrefix for local callers.
func extractEpicPrefix(epicID string) string {
	return beads.ExtractEpicPrefix(epicID)
}

// validateBranchName checks if a branch name is valid for git.
// Returns an error if the branch name contains invalid characters.
// maxBranchNameLen is the maximum allowed branch name length.
// GitHub's limit is 244 bytes after refs/heads/. 200 leaves headroom for any template.
const maxBranchNameLen = 200

func validateBranchName(branchName string) error {
	if branchName == "" {
		return fmt.Errorf("branch name cannot be empty")
	}

	if len(branchName) > maxBranchNameLen {
		return fmt.Errorf("branch name too long (%d chars, max %d)", len(branchName), maxBranchNameLen)
	}

	// Check for invalid characters
	if invalidBranchCharsRegex.MatchString(branchName) {
		return fmt.Errorf("branch name %q contains invalid characters (~ ^ : \\ ? * [ space, .., or @{)", branchName)
	}

	// Check for .lock suffix
	if strings.HasSuffix(branchName, ".lock") {
		return fmt.Errorf("branch name %q cannot end with .lock", branchName)
	}

	// Check for leading/trailing slashes or dots
	if strings.HasPrefix(branchName, "/") || strings.HasSuffix(branchName, "/") {
		return fmt.Errorf("branch name %q cannot start or end with /", branchName)
	}
	if strings.HasPrefix(branchName, ".") || strings.HasSuffix(branchName, ".") {
		return fmt.Errorf("branch name %q cannot start or end with .", branchName)
	}

	// Check for consecutive slashes
	if strings.Contains(branchName, "//") {
		return fmt.Errorf("branch name %q cannot contain consecutive slashes", branchName)
	}

	return nil
}

// getIntegrationBranchField wraps beads.GetIntegrationBranchField for local callers.
func getIntegrationBranchField(description string) string {
	return beads.GetIntegrationBranchField(description)
}

// getRigGit returns a Git object for the rig's repository.
// Prefers .repo.git (bare repo) if it exists, falls back to mayor/rig.
func getRigGit(rigPath string) (*git.Git, error) {
	bareRepoPath := filepath.Join(rigPath, ".repo.git")
	if info, err := os.Stat(bareRepoPath); err == nil && info.IsDir() {
		return git.NewGitWithDir(bareRepoPath, ""), nil
	}
	mayorPath := filepath.Join(rigPath, "mayor", "rig")
	if _, err := os.Stat(mayorPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("no repo base found (neither .repo.git nor mayor/rig exists)")
	}
	return git.NewGit(mayorPath), nil
}

// createLandWorktree creates a temporary worktree from .repo.git for land operations.
// This avoids disrupting running agents (refinery, mayor) by operating in an isolated worktree.
// The caller MUST call the returned cleanup function when done (typically via defer).
// The worktree is checked out to startBranch (e.g., "main").
//
// A global file lock serializes ALL land operations within a rig, even those targeting
// different branches (e.g., landing epic-A to main and epic-C to staging). This is
// intentional: the fixed .land-worktree path is reused across operations, and single-rig
// simplicity is preferred over parallel landing. If per-branch parallelism is needed in
// the future, both the lock and worktree paths should be made per-target-branch.
func createLandWorktree(rigPath, startBranch string) (*git.Git, func(), error) {
	landPath := filepath.Join(rigPath, ".land-worktree")
	noop := func() {}

	// Acquire file lock to prevent concurrent land operations from racing.
	// Matches the lockPolecat() pattern used elsewhere in the codebase.
	lockDir := filepath.Join(rigPath, ".runtime", "locks")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		return nil, noop, fmt.Errorf("creating lock dir: %w", err)
	}
	fl := flock.New(filepath.Join(lockDir, "land-worktree.lock"))
	if err := fl.Lock(); err != nil {
		return nil, noop, fmt.Errorf("acquiring land worktree lock: %w", err)
	}

	// Get bare repo for worktree creation
	bareRepoPath := filepath.Join(rigPath, ".repo.git")
	if _, err := os.Stat(bareRepoPath); err != nil {
		_ = fl.Unlock()
		return nil, noop, fmt.Errorf("bare repo not found at %s: %w", bareRepoPath, err)
	}
	bareGit := git.NewGitWithDir(bareRepoPath, "")

	// Clean up any stale worktree from a previous failed run
	if _, err := os.Stat(landPath); err == nil {
		_ = bareGit.WorktreeRemove(landPath, true)
		_ = os.RemoveAll(landPath)
	}

	// Create worktree checked out to the target branch.
	// Use --force because the branch may already be checked out in refinery/rig.
	if err := bareGit.WorktreeAddExistingForce(landPath, startBranch); err != nil {
		_ = fl.Unlock()
		return nil, noop, fmt.Errorf("creating land worktree: %w", err)
	}

	cleanup := func() {
		_ = bareGit.WorktreeRemove(landPath, true)
		_ = os.RemoveAll(landPath)
		_ = fl.Unlock()
	}

	return git.NewGit(landPath), cleanup, nil
}

// branchNameExists checks if a branch name exists locally or on origin.
func branchNameExists(g *git.Git, name string) bool {
	if exists, _ := g.BranchExists(name); exists {
		return true
	}
	if exists, _ := g.RemoteBranchExists("origin", name); exists {
		return true
	}
	return false
}

// extractEpicNumericSuffix extracts the suffix after the last hyphen in an epic ID.
// Examples: "gt-123" -> "123", "PROJ-456" -> "456", "a-b-c" -> "c", "abc" -> "abc"
func extractEpicNumericSuffix(epicID string) string {
	if idx := strings.LastIndex(epicID, "-"); idx >= 0 {
		suffix := epicID[idx+1:]
		if suffix != "" {
			return suffix
		}
	}
	return epicID
}

// resolveUniqueBranchName checks if branchName already exists and disambiguates
// by appending the epic's numeric suffix if needed. Returns an error if both the
// original and disambiguated names are taken.
func resolveUniqueBranchName(g *git.Git, branchName, epicID string) (string, error) {
	if !branchNameExists(g, branchName) {
		return branchName, nil
	}

	// Disambiguate: append -<numeric-suffix> from epic ID
	disambiguated := branchName + "-" + extractEpicNumericSuffix(epicID)
	if err := validateBranchName(disambiguated); err != nil {
		return "", fmt.Errorf("disambiguated branch name invalid: %w", err)
	}
	if !branchNameExists(g, disambiguated) {
		fmt.Printf("  %s\n", style.Dim.Render(
			fmt.Sprintf("(branch '%s' already exists, using '%s')", branchName, disambiguated)))
		return disambiguated, nil
	}

	return "", fmt.Errorf("branch names '%s' and '%s' both exist; use --branch to specify a custom name", branchName, disambiguated)
}

// resolveIntegrationBranchName reads the stored integration branch name from epic
// metadata, falling back to template computation if no metadata exists.
// This is the correct way to resolve an epic's integration branch name from callers
// that only have an epic ID (e.g., mq list --epic, mq submit --epic).
// Note: without a BranchChecker, this cannot try the legacy fallback with existence
// checking. Callers with git access should use resolveEpicBranch directly.
func resolveIntegrationBranchName(bd *beads.Beads, rigPath, epicID string) string {
	epic, err := bd.Show(epicID)
	if err != nil {
		// Can't look up epic — fall back to legacy template with epic ID.
		// Using the {epic} template avoids producing invalid branch names
		// (the {title} template with no title would fall back to epic ID anyway).
		return buildIntegrationBranchName(beads.LegacyIntegrationBranchTemplate, epicID, "")
	}
	// Delegate to resolveEpicBranch (nil checker = no existence check)
	return resolveEpicBranch(epic, rigPath, nil)
}

// resolveEpicBranch resolves an epic's integration branch name.
// Resolution order: metadata → configured template → legacy {epic} template.
// When checker is non-nil, branch existence is verified and the legacy template
// is tried as a fallback for epics created before the {title} default.
func resolveEpicBranch(epic *beads.Issue, rigPath string, checker beads.BranchChecker) string {
	// 1. Explicit metadata takes precedence
	if branch := getIntegrationBranchField(epic.Description); branch != "" {
		return branch
	}

	// 2. Compute from configured template
	template := getIntegrationBranchTemplate(rigPath, "")
	primaryBranch := buildIntegrationBranchName(template, epic.ID, epic.Title)

	// 3. Without a checker, best-effort return the primary name
	if checker == nil {
		return primaryBranch
	}

	// 4. Check if primary branch exists
	if branchExistsAnywhere(checker, primaryBranch) {
		return primaryBranch
	}

	// 5. Try legacy {epic} template as fallback for pre-{title} epics
	legacyBranch := buildIntegrationBranchName(beads.LegacyIntegrationBranchTemplate, epic.ID, epic.Title)
	if legacyBranch != primaryBranch && branchExistsAnywhere(checker, legacyBranch) {
		return legacyBranch
	}

	// 6. Nothing found — return primary name (callers handle "not found").
	// Note: if neither primary nor legacy branch exists, the caller will get a "does not exist"
	// error. This can happen when the integration branch template changed since the epic was
	// created (e.g., {epic} → {title}). Check the epic's metadata or the rig's
	// integration_branch_template setting if the branch name looks wrong.
	return primaryBranch
}

// branchExistsAnywhere checks if a branch exists on the remote or locally.
func branchExistsAnywhere(checker beads.BranchChecker, name string) bool {
	exists, err := checker.RemoteBranchExists("origin", name)
	if err == nil && exists {
		return true
	}
	// Remote not found or check failed — try local
	localExists, _ := checker.BranchExists(name)
	return localExists
}

// getIntegrationBranchTemplate returns the integration branch template to use.
// Priority: CLI flag > rig config > default
func getIntegrationBranchTemplate(rigPath, cliOverride string) string {
	if cliOverride != "" {
		return cliOverride
	}

	// Try to load rig settings
	settingsPath := filepath.Join(rigPath, "settings", "config.json")
	settings, err := config.LoadRigSettings(settingsPath)
	if err != nil {
		return defaultIntegrationBranchTemplate
	}

	if settings.MergeQueue != nil && settings.MergeQueue.IntegrationBranchTemplate != "" {
		return settings.MergeQueue.IntegrationBranchTemplate
	}

	return defaultIntegrationBranchTemplate
}

// IntegrationStatusOutput is the JSON output structure for integration status.
type IntegrationStatusOutput struct {
	Epic            string                       `json:"epic"`
	Branch          string                       `json:"branch"`
	BaseBranch      string                       `json:"base_branch"`
	Created         string                       `json:"created,omitempty"`
	AheadOfBase     int                          `json:"ahead_of_base"`
	MergedMRs       []IntegrationStatusMRSummary `json:"merged_mrs"`
	PendingMRs      []IntegrationStatusMRSummary `json:"pending_mrs"`
	ReadyToLand     bool                         `json:"ready_to_land"`
	AutoLandEnabled bool                         `json:"auto_land_enabled"`
	ChildrenTotal   int                          `json:"children_total"`
	ChildrenClosed  int                          `json:"children_closed"`
}

// IntegrationStatusMRSummary represents a merge request in the integration status output.
type IntegrationStatusMRSummary struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status,omitempty"`
}

// runMqIntegrationCreate creates an integration branch for an epic.
func runMqIntegrationCreate(cmd *cobra.Command, args []string) error {
	epicID := args[0]

	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Find current rig
	_, r, err := findCurrentRig(townRoot)
	if err != nil {
		return err
	}

	// Initialize beads for the rig
	bd := beads.New(r.Path)

	// 1. Verify epic exists
	epic, err := bd.Show(epicID)
	if err != nil {
		if err == beads.ErrNotFound {
			return fmt.Errorf("epic '%s' not found", epicID)
		}
		return fmt.Errorf("fetching epic: %w", err)
	}

	// Verify it's actually an epic
	if epic.Type != "epic" {
		return fmt.Errorf("'%s' is a %s, not an epic", epicID, epic.Type)
	}

	// Check for existing integration branch metadata
	if existing := getIntegrationBranchField(epic.Description); existing != "" && !mqIntegrationCreateForce {
		return fmt.Errorf("epic '%s' already has integration branch '%s'\n\nUse --force to recreate", epicID, existing)
	}

	// Build integration branch name from template
	template := getIntegrationBranchTemplate(r.Path, mqIntegrationCreateBranch)
	branchName := buildIntegrationBranchName(template, epicID, epic.Title)

	// Validate the branch name
	if err := validateBranchName(branchName); err != nil {
		return fmt.Errorf("invalid branch name: %w", err)
	}

	// Warn if the branch name doesn't start with "integration/" — the pre-push
	// hook guardrail only protects branches under that prefix.
	if !strings.HasPrefix(branchName, "integration/") {
		fmt.Printf("  %s Branch '%s' is outside the integration/ namespace.\n",
			style.Bold.Render("⚠"),
			branchName)
		fmt.Printf("    The pre-push hook guardrail won't cover this branch.\n")
	}

	// Initialize git for the rig
	g, err := getRigGit(r.Path)
	if err != nil {
		return fmt.Errorf("initializing git: %w", err)
	}

	// Check if integration branch already exists (local or remote).
	// With {title} templates, two epics can produce the same branch name.
	// Disambiguate by appending the epic's numeric suffix (e.g., -123).
	branchName, err = resolveUniqueBranchName(g, branchName, epicID)
	if err != nil {
		return err
	}

	// Ensure we have latest refs
	fmt.Printf("Fetching latest from origin...\n")
	if err := g.Fetch("origin"); err != nil {
		return fmt.Errorf("fetching from origin: %w", err)
	}

	// 2. Create branch from base (default: rig's default_branch)
	baseBranchName := r.DefaultBranch()
	if mqIntegrationCreateBaseBranch != "" {
		baseBranchName = strings.TrimPrefix(mqIntegrationCreateBaseBranch, "origin/")
	}
	baseBranch := "origin/" + baseBranchName
	baseBranchDisplay := baseBranchName
	fmt.Printf("Creating branch '%s' from %s...\n", branchName, baseBranchDisplay)
	if err := g.CreateBranchFrom(branchName, baseBranch); err != nil {
		return fmt.Errorf("creating branch: %w", err)
	}

	// 3. Push to origin
	fmt.Printf("Pushing to origin...\n")
	if err := g.Push("origin", branchName, false); err != nil {
		// Clean up local branch on push failure (best-effort cleanup)
		_ = g.DeleteBranch(branchName, true)
		return fmt.Errorf("pushing to origin: %w", err)
	}

	// 4. Store integration branch info in epic metadata
	// Update the epic's description to include the integration branch info
	newDesc := addIntegrationBranchField(epic.Description, branchName)
	// Always store base_branch so land knows where to merge back
	newDesc = beads.AddBaseBranchField(newDesc, baseBranchDisplay)
	if newDesc != epic.Description {
		if err := bd.Update(epicID, beads.UpdateOptions{Description: &newDesc}); err != nil {
			// Non-fatal - branch was created, just metadata update failed
			fmt.Printf("  %s\n", style.Dim.Render("(warning: could not update epic metadata)"))
		}
	}

	// Success output
	fmt.Printf("\n%s Created integration branch\n", style.Bold.Render("✓"))
	fmt.Printf("  Epic:   %s\n", epicID)
	fmt.Printf("  Branch: %s\n", branchName)
	fmt.Printf("  From:   %s\n", baseBranchDisplay)
	fmt.Printf("\n  Future MRs for this epic's children can target:\n")
	fmt.Printf("    gt mq submit --epic %s\n", epicID)

	return nil
}

// addIntegrationBranchField wraps beads.AddIntegrationBranchField for local callers.
func addIntegrationBranchField(description, branchName string) string {
	return beads.AddIntegrationBranchField(description, branchName)
}

// runMqIntegrationLand merges an integration branch to main.
func runMqIntegrationLand(cmd *cobra.Command, args []string) error {
	epicID := args[0]

	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Find current rig
	_, r, err := findCurrentRig(townRoot)
	if err != nil {
		return err
	}

	// Initialize beads and git for the rig
	// Use getRigGit for early ref-only checks (branch exists, fetch).
	// Work-tree operations (checkout, merge, push) use a temporary worktree created later.
	bd := beads.New(r.Path)
	g, err := getRigGit(r.Path)
	if err != nil {
		return fmt.Errorf("initializing git: %w", err)
	}

	// Show what we're about to do
	if mqIntegrationLandDryRun {
		fmt.Printf("%s Dry run - no changes will be made\n\n", style.Bold.Render("🔍"))
	}

	// 1. Verify epic exists
	epic, err := bd.Show(epicID)
	if err != nil {
		if err == beads.ErrNotFound {
			return fmt.Errorf("epic '%s' not found", epicID)
		}
		return fmt.Errorf("fetching epic: %w", err)
	}

	if epic.Type != "epic" {
		return fmt.Errorf("'%s' is a %s, not an epic", epicID, epic.Type)
	}

	epicAlreadyClosed := epic.Status == "closed"
	if epicAlreadyClosed {
		fmt.Printf("  %s Epic is already closed (may have been landed by another process)\n",
			style.Bold.Render("⚠"))
	}

	// Fetch early so resolveEpicBranch and subsequent branch-existence
	// checks operate on up-to-date refs (matches status which also fetches first).
	fmt.Printf("Fetching latest from origin...\n")
	if err := g.Fetch("origin"); err != nil {
		return fmt.Errorf("fetching from origin: %w", err)
	}

	// Get integration branch name — tries metadata, then {title} template,
	// then legacy {epic} template with branch existence checking.
	branchName := resolveEpicBranch(epic, r.Path, g)

	// Read base_branch from epic metadata (where to merge back)
	// Fall back to rig's default_branch for backward compat with pre-base-branch epics
	targetBranch := beads.GetBaseBranchField(epic.Description)
	if targetBranch == "" {
		targetBranch = r.DefaultBranch()
	}

	fmt.Printf("Landing integration branch for epic: %s\n", epicID)
	fmt.Printf("  Title: %s\n\n", epic.Title)

	// 2. Verify integration branch exists
	fmt.Printf("Checking integration branch...\n")
	exists, err := g.BranchExists(branchName)
	if err != nil {
		return fmt.Errorf("checking branch existence: %w", err)
	}

	// Check remote — land uses origin/ refs throughout, so the branch must be pushed
	remoteExists, err := g.RemoteBranchExists("origin", branchName)
	if err != nil {
		return fmt.Errorf("checking remote branch: %w", err)
	}

	if !exists && !remoteExists {
		return fmt.Errorf("integration branch '%s' does not exist (locally or on origin)", branchName)
	}
	if exists && !remoteExists {
		return fmt.Errorf("integration branch '%s' exists locally but not on origin — push it first", branchName)
	}
	if !exists {
		// Remote-only: fetch and create local tracking branch
		fmt.Printf("Fetching integration branch from origin...\n")
		if err := g.FetchBranch("origin", branchName); err != nil {
			return fmt.Errorf("fetching branch: %w", err)
		}
	}
	fmt.Printf("  %s Branch exists (local and remote)\n", style.Bold.Render("✓"))

	// 3. Verify all MRs targeting this integration branch are merged
	fmt.Printf("Checking open merge requests...\n")
	openMRs, err := findOpenMRsForIntegration(bd, branchName)
	if err != nil {
		return fmt.Errorf("checking open MRs: %w", err)
	}

	if len(openMRs) > 0 {
		fmt.Printf("\n  %s Open merge requests targeting %s:\n", style.Bold.Render("⚠"), branchName)
		for _, mr := range openMRs {
			fmt.Printf("    - %s: %s\n", mr.ID, mr.Title)
		}
		fmt.Println()

		if !mqIntegrationLandForce {
			return fmt.Errorf("cannot land: %d open MRs (use --force to override)", len(openMRs))
		}
		fmt.Printf("  %s Proceeding anyway (--force)\n", style.Dim.Render("⚠"))
	} else {
		fmt.Printf("  %s No open MRs targeting integration branch\n", style.Bold.Render("✓"))
	}

	// 4. Verify all epic children are closed
	fmt.Printf("Checking epic children status...\n")
	children, err := bd.List(beads.ListOptions{
		Parent:   epicID,
		Status:   "all",
		Priority: -1,
	})
	if err != nil {
		return fmt.Errorf("checking epic children: %w", err)
	}

	var openChildren []*beads.Issue
	for _, child := range children {
		if child.Status != "closed" {
			openChildren = append(openChildren, child)
		}
	}

	if len(openChildren) > 0 {
		fmt.Printf("\n  %s Open children of %s:\n", style.Bold.Render("⚠"), epicID)
		for _, child := range openChildren {
			fmt.Printf("    - %s [%s]: %s\n", child.ID, child.Status, child.Title)
		}
		fmt.Println()

		if !mqIntegrationLandForce {
			return fmt.Errorf("cannot land: %d children still open/in_progress (use --force to override)", len(openChildren))
		}
		fmt.Printf("  %s Proceeding anyway (--force)\n", style.Dim.Render("⚠"))
	} else if len(children) > 0 {
		fmt.Printf("  %s All %d children closed\n", style.Bold.Render("✓"), len(children))
	} else {
		fmt.Printf("  %s No children found (landing empty integration branch)\n", style.Dim.Render("ℹ"))
	}

	// Dry run stops here
	if mqIntegrationLandDryRun {
		fmt.Printf("\n%s Dry run complete. Would perform:\n", style.Bold.Render("🔍"))
		fmt.Printf("  1. Merge %s to %s (--no-ff)\n", branchName, targetBranch)
		if !mqIntegrationLandSkipTests {
			fmt.Printf("  2. Run tests on %s\n", targetBranch)
		}
		fmt.Printf("  3. Push %s to origin\n", targetBranch)
		fmt.Printf("  4. Delete integration branch (local and remote)\n")
		fmt.Printf("  5. Update epic status to closed\n")
		return nil
	}

	// Idempotency check: if integration branch is already an ancestor of the
	// target branch, the merge was already completed (e.g., previous run crashed
	// after push but before cleanup). Skip directly to branch deletion and epic close.
	alreadyMerged, err := g.IsAncestor("origin/"+branchName, "origin/"+targetBranch)
	if err == nil && alreadyMerged {
		fmt.Printf("  %s Integration branch already merged into %s — skipping to cleanup\n",
			style.Bold.Render("✓"), targetBranch)
		if warnings := cleanupIntegrationBranch(g, bd, epicID, branchName, targetBranch, epicAlreadyClosed); len(warnings) > 0 {
			return fmt.Errorf("landed but cleanup incomplete: %s", strings.Join(warnings, "; "))
		}
		return nil
	}

	// Create a temporary worktree for the merge operation.
	// This avoids disrupting running agents (refinery, mayor) whose worktrees
	// would be corrupted by checkout/merge operations.
	fmt.Printf("Creating temporary worktree for merge...\n")
	landGit, cleanup, err := createLandWorktree(r.Path, targetBranch)
	if err != nil {
		return fmt.Errorf("creating land worktree: %w", err)
	}
	defer cleanup()

	// Pull latest target branch into the worktree
	if err := landGit.Pull("origin", targetBranch); err != nil {
		// Non-fatal if pull fails (e.g., first time)
		fmt.Printf("  %s\n", style.Dim.Render(fmt.Sprintf("(pull from origin/%s skipped)", targetBranch)))
	}

	// 4. Merge integration branch into target
	fmt.Printf("Merging %s to %s...\n", branchName, targetBranch)
	mergeMsg := fmt.Sprintf("Merge %s: %s\n\nEpic: %s", branchName, epic.Title, epicID)
	if err := landGit.MergeNoFF("origin/"+branchName, mergeMsg); err != nil {
		// Capture conflicting files BEFORE aborting (abort wipes them).
		// Best-effort: if GetConflictingFiles fails, we still want to abort and return.
		conflictPaths, _ := landGit.GetConflictingFiles()

		// Abort merge and reset worktree defensively. cleanup() will remove
		// the entire .land-worktree dir on defer, but if AbortMerge somehow
		// partially fails we still want HEAD pointing at a clean state for
		// any debug inspection that happens before cleanup runs.
		_ = landGit.AbortMerge()
		_ = landGit.ResetHard("HEAD")

		if len(conflictPaths) > 0 {
			// Structured marker line — refinery patrol formula greps for
			// "LAND_FAILED:" to drive its consolidation-failure-handler step.
			fmt.Fprintf(os.Stderr, "LAND_FAILED: epic=%s branch=%s target=%s reason=conflict files=%s\n",
				epicID, branchName, targetBranch, strings.Join(conflictPaths, ","))
			return &LandConflictError{
				EpicID:        epicID,
				Branch:        branchName,
				TargetBranch:  targetBranch,
				ConflictPaths: conflictPaths,
				Underlying:    err,
			}
		}

		// Non-conflict merge failure (e.g., index lock, disk error).
		fmt.Fprintf(os.Stderr, "LAND_FAILED: epic=%s branch=%s target=%s reason=merge-error\n",
			epicID, branchName, targetBranch)
		return fmt.Errorf("merge failed: %w", err)
	}
	fmt.Printf("  %s Merged successfully\n", style.Bold.Render("✓"))

	// 5. Run tests (if configured and not skipped)
	if !mqIntegrationLandSkipTests {
		testCmd := getTestCommand(r.Path)
		if testCmd != "" {
			fmt.Printf("Running tests: %s\n", testCmd)
			if err := runTestCommand(landGit.WorkDir(), testCmd); err != nil {
				// Tests failed - no need to reset, worktree is temporary
				fmt.Printf("  %s Tests failed\n", style.Bold.Render("✗"))
				return fmt.Errorf("tests failed: %w", err)
			}
			fmt.Printf("  %s Tests passed\n", style.Bold.Render("✓"))
		} else {
			fmt.Printf("  %s\n", style.Dim.Render("(no test command configured)"))
		}
	} else {
		fmt.Printf("  %s\n", style.Dim.Render("(tests skipped)"))
	}

	// Verify the merge actually brought changes (guard against empty merges).
	// An empty merge means conflict resolution discarded all integration branch work,
	// which would silently lose data if we proceed to delete the branch.
	verifyCmd := exec.Command("git", "diff", "--stat", "HEAD~1..HEAD")
	verifyCmd.Dir = landGit.WorkDir()
	diffOutput, verifyErr := verifyCmd.Output()
	if verifyErr == nil && len(strings.TrimSpace(string(diffOutput))) == 0 {
		return fmt.Errorf("merge produced no file changes — integration branch work may have been discarded during conflict resolution\n"+
			"  Integration branch '%s' has NOT been deleted.\n"+
			"  Inspect manually: git diff %s...origin/%s", branchName, targetBranch, branchName)
	}

	// 6. Push to origin
	fmt.Printf("Pushing %s to origin...\n", targetBranch)
	if err := landGit.PushWithEnv("origin", targetBranch, false, []string{"GT_INTEGRATION_LAND=1"}); err != nil {
		return fmt.Errorf("push failed: %w", err)
	}
	fmt.Printf("  %s Pushed to origin\n", style.Bold.Render("✓"))

	if warnings := cleanupIntegrationBranch(g, bd, epicID, branchName, targetBranch, epicAlreadyClosed); len(warnings) > 0 {
		return fmt.Errorf("landed but cleanup incomplete: %s", strings.Join(warnings, "; "))
	}
	return nil
}

// cleanupIntegrationBranch closes the epic and deletes the integration branch (local + remote).
// Shared by the normal merge path and the idempotency early-return path.
// If epicAlreadyClosed is true, skips the bd.Close call (another process already closed it).
// Returns a list of warnings for any cleanup steps that failed (non-fatal).
//
// Epic close happens BEFORE branch deletion so that a crash between the two
// steps leaves the operation in a retriable state (branch still exists for
// idempotent re-run, but the epic is already marked done).
func cleanupIntegrationBranch(g *git.Git, bd *beads.Beads, epicID, branchName, targetBranch string, epicAlreadyClosed bool) []string {
	var warnings []string

	// Close epic first — ensures retriable state if branch deletion fails
	fmt.Printf("Updating epic status...\n")
	if epicAlreadyClosed {
		fmt.Printf("  %s Epic was already closed (skipping)\n", style.Dim.Render("—"))
	} else if err := bd.Close(epicID); err != nil {
		// Epic close failure is fatal — branch deletion without a closed epic
		// leaves a non-retriable state (no branch for idempotent re-run).
		return append(warnings, fmt.Sprintf("could not close epic (aborting cleanup): %v", err))
	} else {
		fmt.Printf("  %s Epic closed\n", style.Bold.Render("✓"))
	}

	// Delete integration branch (use bare repo git — ref-only operations)
	fmt.Printf("Deleting integration branch...\n")
	// Delete remote first
	if err := g.DeleteRemoteBranch("origin", branchName); err != nil {
		warning := fmt.Sprintf("could not delete remote branch: %v", err)
		warnings = append(warnings, warning)
		fmt.Printf("  %s\n", style.Dim.Render(fmt.Sprintf("(%s)", warning)))
	} else {
		fmt.Printf("  %s Deleted from origin\n", style.Bold.Render("✓"))
	}
	// Delete local
	if err := g.DeleteBranch(branchName, true); err != nil {
		warning := fmt.Sprintf("could not delete local branch: %v", err)
		warnings = append(warnings, warning)
		fmt.Printf("  %s\n", style.Dim.Render(fmt.Sprintf("(%s)", warning)))
	} else {
		fmt.Printf("  %s Deleted locally\n", style.Bold.Render("✓"))
	}

	// Report result
	if len(warnings) > 0 {
		fmt.Printf("\n%s Landed integration branch (with %d cleanup warning(s))\n", style.Bold.Render("⚠"), len(warnings))
	} else {
		fmt.Printf("\n%s Successfully landed integration branch\n", style.Bold.Render("✓"))
	}
	fmt.Printf("  Epic:   %s\n", epicID)
	fmt.Printf("  Branch: %s → %s\n", branchName, targetBranch)

	return warnings
}

// findOpenMRsForIntegration finds all non-closed merge requests targeting an integration branch.
// Uses Status "all" instead of "open" to catch in_progress MRs (refinery race),
// then post-filters to exclude closed MRs.
func findOpenMRsForIntegration(bd *beads.Beads, targetBranch string) ([]*beads.Issue, error) {
	// List all merge requests at any priority (MRs have Type: "task" with label "gt:merge-request").
	// Use Status "all" to catch in_progress MRs that the refinery may have picked up.
	opts := beads.ListOptions{
		Label:    "gt:merge-request",
		Status:   "all",
		Priority: -1,
	}
	allMRs, err := bd.List(opts)
	if err != nil {
		return nil, err
	}

	// Filter to MRs targeting this branch, excluding closed (merged) MRs
	targeted := filterMRsByTarget(allMRs, targetBranch)
	var open []*beads.Issue
	for _, mr := range targeted {
		if mr.Status != "closed" {
			open = append(open, mr)
		}
	}
	return open, nil
}

// filterMRsByTarget filters merge requests to those targeting a specific branch.
func filterMRsByTarget(mrs []*beads.Issue, targetBranch string) []*beads.Issue {
	var result []*beads.Issue
	for _, mr := range mrs {
		fields := beads.ParseMRFields(mr)
		if fields != nil && fields.Target == targetBranch {
			result = append(result, mr)
		}
	}
	return result
}

// getTestCommand returns the test command from rig settings.
func getTestCommand(rigPath string) string {
	settingsPath := filepath.Join(rigPath, "settings", "config.json")
	settings, err := config.LoadRigSettings(settingsPath)
	if err != nil {
		return ""
	}
	if settings.MergeQueue != nil && settings.MergeQueue.TestCommand != "" {
		return settings.MergeQueue.TestCommand
	}
	return ""
}

// runTestCommand executes a test command in the given directory.
// Trust boundary: TestCommand comes from rig's config.json (operator-controlled
// infrastructure config), not from PR branches or user input. Shell execution
// is intentional for flexibility (pipes, env vars, quoted args, etc).
func runTestCommand(workDir, testCmd string) error {
	if testCmd == "" {
		return nil
	}

	cmd := exec.Command("sh", "-c", testCmd) //nolint:gosec // G204: TestCommand is from trusted rig config
	cmd.Dir = workDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// runMqIntegrationStatus shows the status of an integration branch for an epic.
func runMqIntegrationStatus(cmd *cobra.Command, args []string) error {
	epicID := args[0]

	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Find current rig
	_, r, err := findCurrentRig(townRoot)
	if err != nil {
		return err
	}

	// Initialize beads and git for the rig
	bd := beads.New(r.Path)
	g, err := getRigGit(r.Path)
	if err != nil {
		return fmt.Errorf("initializing git: %w", err)
	}

	// Fetch from origin to ensure we have latest refs (needed for branch detection)
	if err := g.Fetch("origin"); err != nil {
		// Non-fatal, continue with local data
	}

	// Fetch epic to get stored branch name
	epic, err := bd.Show(epicID)
	if err != nil {
		if err == beads.ErrNotFound {
			return fmt.Errorf("epic '%s' not found", epicID)
		}
		return fmt.Errorf("fetching epic: %w", err)
	}

	// Get integration branch name — tries metadata, then {title} template,
	// then legacy {epic} template with branch existence checking.
	branchName := resolveEpicBranch(epic, r.Path, g)

	// Read base_branch from epic metadata (where to merge back)
	// Fall back to rig's default_branch for backward compat with pre-base-branch epics
	baseBranch := beads.GetBaseBranchField(epic.Description)
	if baseBranch == "" {
		baseBranch = r.DefaultBranch()
	}

	// Check if integration branch exists (locally or remotely)
	localExists, _ := g.BranchExists(branchName)
	remoteExists, _ := g.RemoteBranchExists("origin", branchName)

	if !localExists && !remoteExists {
		return fmt.Errorf("integration branch '%s' does not exist", branchName)
	}

	// Determine which ref to use for comparison
	ref := branchName
	if !localExists && remoteExists {
		ref = "origin/" + branchName
	}

	// Get branch creation date
	createdDate, err := g.BranchCreatedDate(ref)
	if err != nil {
		createdDate = "" // Non-fatal
	}

	// Get commits ahead of base branch
	aheadCount, err := g.CommitsAhead("origin/"+baseBranch, ref)
	if err != nil {
		aheadCount = 0 // Non-fatal
	}

	// Query for MRs targeting this integration branch (use resolved name)
	targetBranch := branchName

	// Get all merge-request issues (MRs have Type: "task" with label "gt:merge-request")
	allMRs, err := bd.List(beads.ListOptions{
		Label:    "gt:merge-request",
		Status:   "all",
		Priority: -1,
	})
	if err != nil {
		return fmt.Errorf("querying merge requests: %w", err)
	}

	// Filter by target branch and separate into merged/pending
	var mergedMRs, pendingMRs []*beads.Issue
	for _, mr := range allMRs {
		fields := beads.ParseMRFields(mr)
		if fields == nil {
			continue
		}
		if fields.Target != targetBranch {
			continue
		}

		if mr.Status == "closed" {
			mergedMRs = append(mergedMRs, mr)
		} else {
			pendingMRs = append(pendingMRs, mr)
		}
	}

	// Check if auto-land is enabled in settings
	settingsPath := filepath.Join(r.Path, "settings", "config.json")
	settings, _ := config.LoadRigSettings(settingsPath) // Ignore error, use defaults
	autoLandEnabled := false
	if settings != nil && settings.MergeQueue != nil {
		autoLandEnabled = settings.MergeQueue.IsIntegrationBranchAutoLandEnabled()
	}

	// Query children of the epic to determine if ready to land
	// Use status "all" to include both open and closed children
	// Use Priority -1 to disable priority filtering
	children, err := bd.List(beads.ListOptions{
		Parent:   epicID,
		Status:   "all",
		Priority: -1,
	})
	childrenTotal := 0
	childrenClosed := 0
	if err == nil {
		for _, child := range children {
			childrenTotal++
			if child.Status == "closed" {
				childrenClosed++
			}
		}
	}

	readyToLand := isReadyToLand(aheadCount, childrenTotal, childrenClosed, len(pendingMRs))

	// Build output structure
	output := IntegrationStatusOutput{
		Epic:            epicID,
		Branch:          branchName,
		BaseBranch:      baseBranch,
		Created:         createdDate,
		AheadOfBase:     aheadCount,
		MergedMRs:       make([]IntegrationStatusMRSummary, 0, len(mergedMRs)),
		PendingMRs:      make([]IntegrationStatusMRSummary, 0, len(pendingMRs)),
		ReadyToLand:     readyToLand,
		AutoLandEnabled: autoLandEnabled,
		ChildrenTotal:   childrenTotal,
		ChildrenClosed:  childrenClosed,
	}

	for _, mr := range mergedMRs {
		// Extract the title without "Merge: " prefix for cleaner display
		title := strings.TrimPrefix(mr.Title, "Merge: ")
		output.MergedMRs = append(output.MergedMRs, IntegrationStatusMRSummary{
			ID:    mr.ID,
			Title: title,
		})
	}

	for _, mr := range pendingMRs {
		title := strings.TrimPrefix(mr.Title, "Merge: ")
		output.PendingMRs = append(output.PendingMRs, IntegrationStatusMRSummary{
			ID:     mr.ID,
			Title:  title,
			Status: mr.Status,
		})
	}

	// JSON output
	if mqIntegrationStatusJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	// Human-readable output
	return printIntegrationStatus(&output)
}

// isReadyToLand determines if an integration branch is ready to land.
// Ready when: has commits ahead of main, has children, all children closed, no pending MRs.
func isReadyToLand(aheadCount, childrenTotal, childrenClosed, pendingMRCount int) bool {
	return aheadCount > 0 &&
		childrenTotal > 0 &&
		childrenTotal == childrenClosed &&
		pendingMRCount == 0
}

// printIntegrationStatus prints the integration status in human-readable format.
func printIntegrationStatus(output *IntegrationStatusOutput) error {
	fmt.Printf("Integration: %s\n", style.Bold.Render(output.Branch))
	if output.Created != "" {
		fmt.Printf("Created: %s\n", output.Created)
	}
	fmt.Printf("Ahead of %s: %d commits\n", output.BaseBranch, output.AheadOfBase)
	fmt.Printf("Epic children: %d/%d closed\n", output.ChildrenClosed, output.ChildrenTotal)

	// Merged MRs
	fmt.Printf("\nMerged MRs (%d):\n", len(output.MergedMRs))
	if len(output.MergedMRs) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(none)"))
	} else {
		for _, mr := range output.MergedMRs {
			fmt.Printf("  %-12s  %s\n", mr.ID, mr.Title)
		}
	}

	// Pending MRs
	fmt.Printf("\nPending MRs (%d):\n", len(output.PendingMRs))
	if len(output.PendingMRs) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(none)"))
	} else {
		for _, mr := range output.PendingMRs {
			statusInfo := ""
			if mr.Status != "" && mr.Status != "open" {
				statusInfo = fmt.Sprintf(" (%s)", mr.Status)
			}
			fmt.Printf("  %-12s  %s%s\n", mr.ID, mr.Title, style.Dim.Render(statusInfo))
		}
	}

	// Landing status
	fmt.Println()
	if output.ReadyToLand {
		fmt.Printf("%s Integration branch is ready to land.\n", style.Bold.Render("✓"))
		if output.AutoLandEnabled {
			fmt.Printf("  Auto-land: %s\n", style.Bold.Render("enabled"))
		} else {
			fmt.Printf("  Auto-land: %s\n", style.Dim.Render("disabled"))
			fmt.Printf("  Run: gt mq integration land %s\n", output.Epic)
		}
	} else {
		if output.ChildrenTotal == 0 {
			fmt.Printf("%s Epic has no children yet.\n", style.Dim.Render("○"))
		} else if output.ChildrenClosed < output.ChildrenTotal {
			fmt.Printf("%s Waiting for %d/%d children to close.\n",
				style.Dim.Render("○"), output.ChildrenTotal-output.ChildrenClosed, output.ChildrenTotal)
		} else if len(output.PendingMRs) > 0 {
			fmt.Printf("%s Waiting for %d pending MRs to merge.\n",
				style.Dim.Render("○"), len(output.PendingMRs))
		} else if output.AheadOfBase == 0 {
			fmt.Printf("%s No commits ahead of %s.\n", style.Dim.Render("○"), output.BaseBranch)
		}
		// Show auto-land status even when not ready
		if output.AutoLandEnabled {
			fmt.Printf("  Auto-land: %s (will land when ready)\n", style.Bold.Render("enabled"))
		} else {
			fmt.Printf("  Auto-land: %s\n", style.Dim.Render("disabled"))
		}
	}

	return nil
}
