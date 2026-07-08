package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/templates"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

var doneCmd = &cobra.Command{
	Use:         "done",
	GroupID:     GroupWork,
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Signal work ready for merge queue",
	Long: `Signal that your work is complete and ready for the merge queue.

This is a convenience command for polecats that:
1. Submits the current branch to the merge queue
2. Auto-detects issue ID from branch name
3. Notifies the Witness with the exit outcome
4. Exits the polecat session after durable handoff
   (Witness/refinery cleanup owns the retired sandbox)

Exit statuses:
  COMPLETED      - Work done, MR submitted (default)
  ESCALATED      - Hit blocker, needs human intervention
  DEFERRED       - Work paused, issue still open

Examples:
  gt done                              # Submit branch, notify COMPLETED, exit session
  gt done --pre-verified               # Submit with pre-verification fast-path
  gt done --target feat/my-branch      # Explicit MR target branch
  gt done --pre-verified --target feat/contract-review  # Pre-verified with explicit target
  gt done --issue gt-abc               # Explicit issue ID
  gt done --skip-verify                # Audit-only escape hatch for non-code closes
  gt done --status ESCALATED           # Signal blocker, skip MR
  gt done --status DEFERRED            # Pause work, skip MR`,
	RunE:         runDone,
	SilenceUsage: true, // Don't print usage on operational errors (confuses agents)
}

var (
	doneIssue         string
	donePriority      int
	doneStatus        string
	doneCleanupStatus string
	doneResume        bool
	donePreVerified   bool
	doneTarget        string
	doneSkipVerify    bool
)

// Valid exit types for gt done
const (
	ExitCompleted = "COMPLETED"
	ExitEscalated = "ESCALATED"
	ExitDeferred  = "DEFERRED"
)

func doneContaminationBaseRef(defaultBranch, explicitTarget string) string {
	targetBranch := defaultBranch
	if explicitTarget != "" {
		targetBranch = strings.TrimSpace(explicitTarget)
		if strings.HasPrefix(targetBranch, "origin/") || strings.HasPrefix(targetBranch, "upstream/") {
			return targetBranch
		}
	}

	return "origin/" + targetBranch
}

func shouldUpdateAgentStateOnDone(pushFailed, mrFailed bool) bool {
	return !pushFailed && !mrFailed
}

func shouldRetirePolecatSessionAfterDone(exitType, mergeStrategy string, pushFailed, mrFailed bool) bool {
	if exitType != ExitCompleted || pushFailed || mrFailed {
		return false
	}
	return mergeStrategy != "local"
}

type doneSessionKiller interface {
	KillSessionWithProcessesExcluding(name string, excludePIDs []string) error
}

var newDoneSessionKiller = func() doneSessionKiller {
	return tmux.NewTmux()
}

var updateAgentStateOnDoneFn = updateAgentStateOnDone

func updateAgentStateAfterSubmission(cwd, townRoot, exitType, issueID string, pushFailed, mrFailed bool) error {
	if !shouldUpdateAgentStateOnDone(pushFailed, mrFailed) {
		style.PrintWarning("skipping agent cleanup because push or MR submission failed")
		return nil
	}
	return updateAgentStateOnDoneFn(cwd, townRoot, exitType, issueID)
}

func polecatSessionRetirementTarget(rigName, polecatName string, pid int) (string, []string, bool) {
	if rigName == "" || polecatName == "" || pid <= 0 {
		return "", nil, false
	}
	return session.PolecatSessionName(session.PrefixFor(rigName), polecatName), []string{fmt.Sprintf("%d", pid)}, true
}

func retirePolecatSessionAfterDone(rigName, polecatName string, pid int) error {
	sessionName, excludePIDs, ok := polecatSessionRetirementTarget(rigName, polecatName, pid)
	if !ok {
		return nil
	}
	return newDoneSessionKiller().KillSessionWithProcessesExcluding(sessionName, excludePIDs)
}

func cleanupStatusAfterSuccessfulPush(status string) string {
	if status == "unpushed" || status == "has_unpushed" {
		return "clean"
	}
	return status
}

var reviewEvidencePrefixes = []string{
	"report:",
	"findings:",
	"review:",
	"evidence:",
	"verdict:",
	"decision:",
	"pr-sheriff-evidence",
	"pr sheriff evidence",
}

var generatedCommentPrefixes = []string{
	"verified_push_",
	"mr created:",
}

func doneSourceCloseSkipReason(bd *beads.Beads, issueID string, issue *beads.Issue) (string, bool) {
	currentHead, _ := currentReviewEvidenceHead()
	return doneSourceCloseSkipReasonForHead(bd, issueID, issue, currentHead)
}

func doneSourceCloseSkipReasonForHead(bd *beads.Beads, issueID string, issue *beads.Issue, currentHead string) (string, bool) {
	issue, skipReason, fatal := loadDoneSourceIssue(bd, issueID, issue)
	if skipReason != "" {
		return skipReason, fatal
	}
	if skipReason, fatal := doneReviewOnlyCloseSkipReasonForHead(bd, issueID, issue, currentHead); skipReason != "" {
		return skipReason, fatal
	}
	if unchecked := beads.HasUncheckedCriteria(issue); unchecked > 0 {
		return fmt.Sprintf("issue %s has %d unchecked acceptance criteria — skipping close", issueID, unchecked), false
	}
	return "", false
}

func doneReviewOnlyCloseSkipReason(bd *beads.Beads, issueID string, issue *beads.Issue) (string, bool) {
	issue, skipReason, fatal := loadDoneSourceIssue(bd, issueID, issue)
	if skipReason != "" {
		return skipReason, fatal
	}
	attachment := beads.ParseAttachmentFields(issue)
	if attachment == nil || !attachment.ReviewOnly {
		return "", false
	}
	currentHead, err := currentReviewEvidenceHead()
	if err != nil {
		return fmt.Sprintf("could not verify review evidence for %s: %v", issueID, err), true
	}
	return doneReviewOnlyCloseSkipReasonForHead(bd, issueID, issue, currentHead)
}

func doneReviewOnlyCloseSkipReasonForHead(bd *beads.Beads, issueID string, issue *beads.Issue, currentHead string) (string, bool) {
	issue, skipReason, fatal := loadDoneSourceIssue(bd, issueID, issue)
	if skipReason != "" {
		return skipReason, fatal
	}
	attachment := beads.ParseAttachmentFields(issue)
	if attachment == nil || !attachment.ReviewOnly {
		return "", false
	}
	assignmentAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(attachment.AttachedAt))
	if err != nil {
		return fmt.Sprintf("review-only issue %s has no fresh assignment timestamp — re-sling it or add evidence after a fresh assignment", issueID), true
	}
	if strings.TrimSpace(issue.Assignee) == "" {
		return fmt.Sprintf("review-only issue %s has no assignee for evidence author validation", issueID), true
	}
	currentHead = strings.TrimSpace(currentHead)
	if currentHead == "" {
		return fmt.Sprintf("review-only issue %s has no current HEAD for evidence validation", issueID), true
	}
	hasEvidence, err := hasFreshReviewReportEvidence(bd, issueID, issue, assignmentAt, issue.Assignee, currentHead)
	if err != nil {
		return fmt.Sprintf("could not verify review evidence for %s: %v", issueID, err), true
	}
	if !hasEvidence {
		return fmt.Sprintf("review-only issue %s has no fresh review evidence comment for assignee %s and head %s", issueID, strings.TrimSpace(issue.Assignee), currentHead), true
	}
	return "", false
}

func loadDoneSourceIssue(bd *beads.Beads, issueID string, issue *beads.Issue) (*beads.Issue, string, bool) {
	if issueID == "" {
		return nil, "", false
	}
	if issue != nil {
		return issue, "", false
	}
	if bd == nil {
		return nil, fmt.Sprintf("could not inspect issue %s close eligibility", issueID), true
	}
	loaded, err := bd.Show(issueID)
	if err != nil {
		return nil, fmt.Sprintf("could not inspect issue %s close eligibility: %v", issueID, err), true
	}
	return loaded, "", false
}

func currentReviewEvidenceHead() (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolving current HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func hasFreshReviewReportEvidence(bd *beads.Beads, issueID string, issue *beads.Issue, assignmentAt time.Time, assignee, currentHead string) (bool, error) {
	if issue != nil {
		if hasFreshReviewEvidenceComment(issue.Comments, assignmentAt, assignee, currentHead) {
			return true, nil
		}
	}
	if bd == nil || issueID == "" {
		return false, nil
	}
	if store := bd.Store(); store != nil {
		comments, err := store.GetIssueComments(context.Background(), issueID)
		if err != nil {
			return false, err
		}
		for _, comment := range comments {
			if comment == nil {
				continue
			}
			candidate := beads.Comment{
				Author:    comment.Author,
				Text:      comment.Text,
				CreatedAt: comment.CreatedAt.Format(time.RFC3339Nano),
			}
			if hasFreshReviewEvidenceComment([]beads.Comment{candidate}, assignmentAt, assignee, currentHead) {
				return true, nil
			}
		}
		return false, nil
	}
	out, err := bd.Run("comments", issueID, "--json")
	if err != nil {
		return false, err
	}
	var comments []beads.Comment
	if err := json.Unmarshal(out, &comments); err != nil {
		return false, fmt.Errorf("parsing comments: %w", err)
	}
	if hasFreshReviewEvidenceComment(comments, assignmentAt, assignee, currentHead) {
		return true, nil
	}
	return false, nil
}

func hasFreshReviewEvidenceComment(comments []beads.Comment, assignmentAt time.Time, assignee, currentHead string) bool {
	assignee = strings.TrimSpace(assignee)
	currentHead = strings.TrimSpace(currentHead)
	if assignee == "" || currentHead == "" {
		return false
	}
	for _, comment := range comments {
		createdAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(comment.CreatedAt))
		if err != nil || !createdAt.After(assignmentAt) {
			continue
		}
		if strings.TrimSpace(comment.Author) != assignee {
			continue
		}
		if isGeneratedReviewComment(comment.Text) || !isReviewEvidenceText(comment.Text) {
			continue
		}
		if reviewEvidenceHeadSHA(comment.Text) != currentHead {
			continue
		}
		return true
	}
	return false
}

func isGeneratedReviewComment(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "" {
			continue
		}
		for _, prefix := range generatedCommentPrefixes {
			if strings.HasPrefix(lower, prefix) {
				return true
			}
		}
	}
	return false
}

func reviewEvidenceHeadSHA(text string) string {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		for _, key := range []string{"head_sha", "target_head_sha", "head"} {
			for _, sep := range []string{":", "="} {
				prefix := key + sep
				if strings.HasPrefix(lower, prefix) {
					return strings.TrimSpace(trimmed[len(prefix):])
				}
			}
		}
	}
	return ""
}

func isReviewEvidenceText(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		generated := false
		for _, prefix := range generatedCommentPrefixes {
			if strings.HasPrefix(lower, prefix) {
				generated = true
				break
			}
		}
		if generated {
			continue
		}
		for _, prefix := range reviewEvidencePrefixes {
			if strings.HasPrefix(lower, prefix) {
				return true
			}
		}
	}
	return false
}

func init() {
	doneCmd.Flags().StringVar(&doneIssue, "issue", "", "Source issue ID (default: parse from branch name)")
	doneCmd.Flags().IntVarP(&donePriority, "priority", "p", -1, "Override priority (0-4, default: inherit from issue)")
	doneCmd.Flags().StringVar(&doneStatus, "status", ExitCompleted, "Exit status: COMPLETED, ESCALATED, or DEFERRED")
	doneCmd.Flags().StringVar(&doneCleanupStatus, "cleanup-status", "", "Git cleanup status: clean, uncommitted, unpushed, stash, unknown (ZFC: agent-observed)")
	doneCmd.Flags().BoolVar(&doneResume, "resume", false, "Resume from last checkpoint (auto-detected, for Witness recovery)")
	doneCmd.Flags().BoolVar(&donePreVerified, "pre-verified", false, "Mark MR as pre-verified (polecat ran gates after rebasing onto target)")
	doneCmd.Flags().StringVar(&doneTarget, "target", "", "Explicit MR target branch (overrides formula_vars and auto-detection)")
	doneCmd.Flags().BoolVar(&doneSkipVerify, "skip-verify", false, "Skip verified-push checks for audit/test-only completion (recorded on bead)")

	rootCmd.AddCommand(doneCmd)
}

func runDone(cmd *cobra.Command, args []string) (retErr error) {
	defer func() { telemetry.RecordDone(context.Background(), strings.ToUpper(doneStatus), retErr) }()
	// Guard: Only polecats should call gt done
	// Crew, deacons, witnesses etc. don't use gt done - they persist across tasks.
	// Polecat sessions end with gt done — the session is cleaned up, but the
	// polecat's persistent identity (agent bead, CV chain) survives across assignments.
	actor := os.Getenv("BD_ACTOR")
	if actor != "" && !isPolecatActor(actor) {
		return fmt.Errorf("gt done is for polecats only (you are %s)\nPolecat sessions end with gt done — the session is cleaned up, but identity persists.\nOther roles persist across tasks and don't use gt done.", actor)
	}

	// Validate exit status
	exitType := strings.ToUpper(doneStatus)
	if exitType != ExitCompleted && exitType != ExitEscalated && exitType != ExitDeferred {
		return fmt.Errorf("invalid exit status '%s': must be COMPLETED, ESCALATED, or DEFERRED", doneStatus)
	}

	// Clean completions retire the live polecat session after durable handoff.
	// Failed, deferred, escalated, and local-review paths preserve the session for recovery.

	// Find workspace with fallback for deleted worktrees (hq-3xaxy)
	// If the polecat's worktree was deleted by Witness before gt done finishes,
	// getcwd will fail. We fall back to GT_TOWN_ROOT env var in that case.
	townRoot, cwd, err := workspace.FindFromCwdWithFallback()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Track if cwd is available - affects which operations we can do
	cwdAvailable := cwd != ""
	if !cwdAvailable {
		style.PrintWarning("working directory deleted (worktree nuked?), using fallback paths")
		// Try to get cwd from GT_POLECAT_PATH env var (set by session manager)
		if polecatPath := os.Getenv("GT_POLECAT_PATH"); polecatPath != "" {
			cwd = polecatPath // May still be gone, but we have a path to use
		}
	}

	// Find current rig - use cwd (which has fallback for deleted worktrees)
	// instead of findCurrentRig which calls os.Getwd() and fails on deleted cwd
	var rigName string
	if cwd != "" {
		relPath, err := filepath.Rel(townRoot, cwd)
		if err == nil {
			parts := strings.Split(relPath, string(filepath.Separator))
			if len(parts) > 0 && parts[0] != "" && parts[0] != "." {
				rigName = parts[0]
			}
		}
	}
	// Prefer GT_RIG over cwd-derived rig name when available.
	// When Claude Code resets shell cwd (e.g., to mayor/rig), the cwd-derived
	// rig name is wrong (e.g., "mayor" instead of "vets"). GT_RIG is set
	// reliably for polecats via session env injection.
	if envRig := os.Getenv("GT_RIG"); envRig != "" {
		rigName = envRig
	}
	if rigName == "" {
		return fmt.Errorf("cannot determine current rig (working directory may be deleted)")
	}

	// When gt is invoked via shell alias (cd ~/gt && gt), or when Claude Code
	// resets the shell CWD to mayor/rig, cwd is NOT the polecat's worktree.
	// Detect and reconstruct actual path.
	//
	// This triggers when cwd is:
	// - The town root itself (cd ~/gt && gt)
	// - The mayor rig path (Claude Code Bash tool CWD reset)
	// - Any non-polecat path within the rig
	cwdIsPolecatWorktree := strings.Contains(cwd, "/polecats/")
	if cwdAvailable && !cwdIsPolecatWorktree {
		if polecatName := os.Getenv("GT_POLECAT"); polecatName != "" && rigName != "" {
			polecatClone := filepath.Join(townRoot, rigName, "polecats", polecatName, rigName)
			if _, err := os.Stat(polecatClone); err == nil {
				cwd = polecatClone
			} else {
				polecatClone = filepath.Join(townRoot, rigName, "polecats", polecatName)
				if _, err := os.Stat(filepath.Join(polecatClone, ".git")); err == nil {
					cwd = polecatClone
				}
			}
		} else if crewName := os.Getenv("GT_CREW"); crewName != "" && rigName != "" {
			crewClone := filepath.Join(townRoot, rigName, "crew", crewName)
			if _, err := os.Stat(crewClone); err == nil {
				cwd = crewClone
			}
		}
	}

	// Normalize polecat CWD: polecats may run gt done from a subdirectory (e.g.,
	// beads-ide/ inside the repo). beads.ResolveBeadsDir only looks at cwd/.beads,
	// not parent dirs, so we must normalize to the git repo root before use.
	// Walk up from cwd until we find .git, stopping if we leave the polecats area.
	if cwdAvailable && cwdIsPolecatWorktree {
		candidate := cwd
		for {
			if _, statErr := os.Stat(filepath.Join(candidate, ".git")); statErr == nil {
				cwd = candidate
				break
			}
			parent := filepath.Dir(candidate)
			if parent == candidate || !strings.Contains(parent, "/polecats/") {
				break // hit filesystem root or left polecats area
			}
			candidate = parent
		}
	}

	// Initialize git - use cwd if available, otherwise use rig's mayor clone
	var g *git.Git
	if cwdAvailable {
		g = git.NewGit(cwd)
	} else {
		// Fallback: use the rig's mayor clone for git operations
		mayorClone := filepath.Join(townRoot, rigName, "mayor", "rig")
		g = git.NewGit(mayorClone)
	}

	// Get current branch - try env var first if cwd is gone
	var branch string
	if !cwdAvailable {
		// Try to get branch from GT_BRANCH env var (set by session manager)
		branch = os.Getenv("GT_BRANCH")
	}
	// CRITICAL FIX: Only call g.CurrentBranch() if we're using the cwd-based git.
	// When cwdAvailable is false, we fall back to the mayor clone for git operations,
	// but the mayor clone is on main/master - NOT the polecat branch. Calling
	// g.CurrentBranch() in that case would incorrectly return main/master.
	if branch == "" {
		if !cwdAvailable {
			// We don't have GT_BRANCH and we're using mayor clone - can't determine branch.
			// Session stays alive (persistent polecat model) — Witness handles recovery.
			return fmt.Errorf("cannot determine branch: GT_BRANCH not set and working directory unavailable")
		}
		var err error
		branch, err = g.CurrentBranch()
		if err != nil {
			// Last resort: try to extract from polecat name (polecat/<name>-<suffix>)
			if polecatName := os.Getenv("GT_POLECAT"); polecatName != "" {
				branch = fmt.Sprintf("polecat/%s", polecatName)
				style.PrintWarning("could not get branch from git, using fallback: %s", branch)
			} else {
				return fmt.Errorf("getting current branch: %w", err)
			}
		}
	}

	// Auto-detect cleanup status if not explicitly provided
	// This prevents premature polecat cleanup by ensuring witness knows git state
	if doneCleanupStatus == "" {
		if !cwdAvailable {
			// Can't detect git state without working directory, default to unknown
			doneCleanupStatus = "unknown"
			style.PrintWarning("cannot detect cleanup status - working directory deleted")
		} else {
			workStatus, err := g.CheckUncommittedWork()
			if err != nil {
				style.PrintWarning("could not auto-detect cleanup status: %v", err)
			} else {
				switch {
				case workStatus.HasUncommittedChanges:
					doneCleanupStatus = "uncommitted"
				case workStatus.StashCount > 0:
					doneCleanupStatus = "stash"
				default:
					// CheckUncommittedWork.UnpushedCommits doesn't work for branches
					// without upstream tracking (common for polecats). Use the more
					// robust BranchPushedToRemote which compares against origin/main.
					pushed, unpushedCount, err := g.BranchPushedToRemote(branch, "origin")
					if err != nil {
						style.PrintWarning("could not check if branch is pushed: %v", err)
						doneCleanupStatus = "unpushed" // err on side of caution
					} else if !pushed || unpushedCount > 0 {
						doneCleanupStatus = "unpushed"
					} else {
						doneCleanupStatus = "clean"
					}
				}
			}
		}
	}

	// SAFETY NET (gt-pvx, stash recovery): If we detected stashes belonging to
	// this branch, auto-pop them so the existing uncommitted-work auto-commit
	// path (below) catches the contents and saves them as a normal commit.
	//
	// Background: agents have been observed running `git stash` to clear the
	// working tree before rebase/checkout, then dying before `git stash pop`.
	// The stash entries become orphaned in .git/refs/stash, surviving for
	// indefinite periods and silently leaking work. By popping them on the way
	// out of `gt done`, the recovery flow turns "lost" stashes into a
	// committed safety-net snapshot.
	//
	// Pop happens oldest-first so the most recent state ends up on top of the
	// working tree (matches what a user would do manually). If any pop has
	// conflicts, we stop and let the agent/user resolve — surfacing the
	// conflict is better than silently dropping the stash.
	if cwdAvailable && doneCleanupStatus == "stash" {
		entries, err := g.StashListForBranch()
		if err != nil {
			style.PrintWarning("auto-pop: could not list stashes: %v — orphaned stashes may remain", err)
		} else if len(entries) > 0 {
			fmt.Printf("\n%s %d stash(es) detected on this branch — auto-popping (gt-pvx safety net)\n",
				style.Bold.Render("⚠"), len(entries))
			// Pop oldest first: iterate in reverse so newest lands on top.
			popFailed := false
			for i := len(entries) - 1; i >= 0; i-- {
				e := entries[i]
				fmt.Printf("  popping %s — %s\n", e.Ref, e.Message)
				if popErr := g.StashPop(e.Ref); popErr != nil {
					style.PrintWarning("auto-pop %s failed (likely conflict): %v", e.Ref, popErr)
					style.PrintWarning("stopping pop chain — resolve conflict manually then re-run gt done")
					popFailed = true
					break
				}
				// After each pop, stash refs shift; re-fetch the list before next pop.
				entries, err = g.StashListForBranch()
				if err != nil || len(entries) == 0 {
					break
				}
			}
			if !popFailed {
				// Re-evaluate cleanup status: pops likely produced uncommitted changes
				// that the next block will auto-commit. Worst case, status was already
				// uncommitted and the next block runs anyway.
				if workStatus, wsErr := g.CheckUncommittedWork(); wsErr == nil && workStatus.HasUncommittedChanges {
					doneCleanupStatus = "uncommitted"
					fmt.Printf("%s Stash content moved to working tree — will auto-commit below.\n",
						style.Bold.Render("✓"))
				} else {
					// Pops succeeded but produced nothing dirty (e.g. stashes were
					// already merged). Recompute status normally.
					doneCleanupStatus = ""
				}
			}
		}
	}

	// SAFETY NET: Auto-commit uncommitted work before ANY exit path (gt-pvx).
	// Polecats have been observed running gt done without committing their
	// implementation work (1000s of lines lost). This happened because:
	// 1. The agent skips the "commit changes" formula step
	// 2. The COMPLETED check blocks, but the agent retries with --status DEFERRED
	//    which skips all checks
	// 3. The agent's session dies after the error, before it can commit
	//
	// Auto-commit ensures work is NEVER lost regardless of exit type or agent behavior.
	// The commit message is clearly marked as an auto-save so reviewers know.
	if cwdAvailable && doneCleanupStatus == "uncommitted" {
		// Re-check to get file details (cleanup detection already confirmed uncommitted changes)
		workStatus, err := g.CheckUncommittedWork()
		if err == nil && workStatus.HasUncommittedChanges && !workStatus.CleanExcludingRuntime() {
			if len(workStatus.UnmergedFiles) > 0 {
				return fmt.Errorf("cannot auto-save unmerged conflicts: %s\nResolve conflicts first, or use --status DEFERRED to exit without completing", strings.Join(workStatus.UnmergedFiles, ", "))
			}

			fmt.Printf("\n%s Uncommitted changes detected — auto-saving to prevent work loss\n", style.Bold.Render("⚠"))
			fmt.Printf("  Files: %s\n\n", workStatus.String())

			// Stage all changes (git add -A), then unstage overlay/runtime files (gt-p35)
			// and any deletions of tracked files (gt-pvx safety: never commit deletions).
			if addErr := g.Add("-A"); addErr != nil {
				style.PrintWarning("auto-commit: git add failed: %v — uncommitted work may be at risk", addErr)
			} else {
				// Unstage Gas Town overlay files that git add -A picked up.
				// These are runtime artifacts that must not be committed to repos.
				_ = g.ResetFiles("CLAUDE.local.md")
				// Only unstage CLAUDE.md if it contains the overlay marker
				if claudeData, readErr := os.ReadFile(filepath.Join(cwd, "CLAUDE.md")); readErr == nil {
					if strings.Contains(string(claudeData), templates.PolecatLifecycleMarker) {
						_ = g.ResetFiles("CLAUDE.md")
					}
				}
				// Unstage runtime/ephemeral artifacts using the centralized git policy.
				for _, path := range workStatus.RuntimeArtifactPaths() {
					_ = g.ResetFiles(path)
				}
				// Unstage deletions of tracked files. A safety-net auto-commit should
				// preserve work (additions + modifications), never destroy it (deletions).
				// This prevents the bug where a polecat's working tree has a missing
				// tracked file (e.g. .beads/metadata.json) and the auto-save commits
				// the deletion, breaking infrastructure for subsequent sessions.
				if stagedDeletions, delErr := g.StagedDeletions(); delErr == nil && len(stagedDeletions) > 0 {
					_ = g.ResetFiles(stagedDeletions...)
				}
				// Build a descriptive commit message
				autoMsg := "fix: auto-save uncommitted implementation work (gt-pvx safety net)"
				if issueFromBranch := parseBranchName(branch).Issue; issueFromBranch != "" {
					autoMsg = fmt.Sprintf("fix: auto-save uncommitted implementation work (%s, gt-pvx safety net)", issueFromBranch)
				}
				if commitErr := g.Commit(autoMsg); commitErr != nil {
					style.PrintWarning("auto-commit: git commit failed: %v — uncommitted work may be at risk", commitErr)
				} else {
					fmt.Printf("%s Auto-committed uncommitted work (safety net)\n", style.Bold.Render("✓"))
					fmt.Printf("  The agent should have committed before running gt done.\n")
					fmt.Printf("  This auto-save prevents work loss.\n\n")
					doneCleanupStatus = "unpushed" // Update status — changes are now committed but not pushed
				}
			}
		}
	}

	// Parse branch info
	info := parseBranchName(branch)

	// Override with explicit flags
	issueID := doneIssue
	if issueID == "" {
		issueID = info.Issue
	}
	worker := info.Worker

	// Determine polecat name from sender detection
	sender := detectSender()

	// Get agent bead ID for cross-referencing
	var agentBeadID string
	if roleInfo, err := GetRoleWithContext(cwd, townRoot); err == nil {
		if actor := roleInfo.ActorString(); actor != "" {
			sender = actor
		}
		ctx := RoleContext{
			Role:     roleInfo.Role,
			Rig:      roleInfo.Rig,
			Polecat:  roleInfo.Polecat,
			TownRoot: townRoot,
			WorkDir:  cwd,
		}
		agentBeadID = getAgentBeadID(ctx)

		// Recreate the agent bead if it's missing (hq-xu4p). Done-intent
		// labels, checkpoints, and active_mr all write to it; when it's gone
		// every write fails 'issue not found' and witness zombie detection +
		// done-resume silently degrade. Best-effort: a failed recreate just
		// leaves the existing warnings.
		ensureAgentBeadExists(beads.New(cwd).ForAgentBead(), agentBeadID, ctx)

		// Completion now exits the live polecat session after durable handoff.
		// The agent bead keeps lifecycle metadata for witness/refinery cleanup.
	}
	polecatName := ""
	if parts := strings.Split(sender, "/"); len(parts) >= 2 {
		polecatName = parts[len(parts)-1]
	}

	var assignedIssueIDs []string
	loadAssignedIssueIDs := func() []string {
		if assignedIssueIDs == nil && sender != "" {
			assignedIssueIDs = findAssignedBeadsForAgent(cwd, sender)
		}
		return assignedIssueIDs
	}

	// If issue ID not set by flag or branch name, query for hooked beads
	// assigned to this agent. This replaces reading agent_bead.hook_bead
	// (hq-l6mm5: direct bead tracking instead of agent bead slot).
	if issueID == "" && sender != "" {
		if hookIssue, ambiguous := selectAssignedIssue("", loadAssignedIssueIDs()); hookIssue != "" {
			issueID = hookIssue
		} else if ambiguous {
			return fmt.Errorf("multiple active assignments found for %s; cannot infer issue from hook. Use --issue to disambiguate", sender)
		}
	}

	// Stale-branch guard (hq-l0fj): a redispatched polecat that reuses its
	// previous work branch carries the OLD bead-id in the branch name, which
	// would mis-attribute this MR (close credit goes to a closed bead; the
	// real issue stays open and hooked). When the branch-derived id differs
	// from the hooked bead, trust the hook. An explicit --issue flag still
	// wins, and subtask branches of the hooked bead (e.g. gt-abc.1 under
	// hooked gt-abc) are left alone.
	if doneIssue == "" && info.Issue != "" && sender != "" {
		if hookIssue, ambiguous := selectAssignedIssue(info.Issue, loadAssignedIssueIDs()); isStaleBranchIssue(info.Issue, hookIssue) {
			style.PrintWarning("branch %q embeds issue %s but your hooked bead is %s — submitting for %s (stale branch reuse?)", branch, info.Issue, hookIssue, hookIssue)
			fmt.Printf("  Fresh branches must be named polecat/<name>/<bead-id>+<suffix> for the bead you are working.\n")
			fmt.Printf("  Use --issue to override if the branch-derived id is actually correct.\n\n")
			issueID = hookIssue
		} else if ambiguous {
			return fmt.Errorf("branch %q embeds issue %s but %s has multiple active assignments; use --issue to disambiguate", branch, info.Issue, sender)
		}
	}

	// Write done-intent label EARLY, before push/MR operations.
	// If gt done crashes after this point, the Witness can detect the intent
	// and auto-nuke the zombie polecat.
	//
	// Also read existing checkpoints for resume capability (gt-aufru).
	// If gt done was interrupted (SIGTERM, context exhaustion, SIGKILL),
	// checkpoints indicate which stages completed. On re-invocation, we
	// skip those stages to avoid repeating work or hitting errors.
	checkpoints := map[DoneCheckpoint]string{}
	if agentBeadID != "" {
		// Agent bead lives in town DB despite rig prefix — bypass routing.
		bd := beads.New(cwd).ForAgentBead()
		setDoneIntentLabel(bd, agentBeadID, exitType)
		checkpoints = readDoneCheckpoints(bd, agentBeadID)
		if len(checkpoints) > 0 {
			fmt.Printf("%s Resuming gt done from checkpoint (previous run was interrupted)\n", style.Bold.Render("→"))
		}
	}

	// Write heartbeat state="exiting" (gt-3vr5: heartbeat v2).
	// Tells the witness we're in the gt done flow — trust the agent until
	// heartbeat goes stale. No timer-based inference needed.
	// Parallel to done-intent label for backwards compat during migration.
	if sessionName := os.Getenv("GT_SESSION"); sessionName != "" && townRoot != "" {
		polecat.TouchSessionHeartbeatWithState(townRoot, sessionName, polecat.HeartbeatExiting, "gt done", issueID)
	}

	// Get configured default branch for this rig
	defaultBranch := "main" // fallback
	if rigCfg, err := rig.LoadRigConfig(filepath.Join(townRoot, rigName)); err == nil && rigCfg.DefaultBranch != "" {
		defaultBranch = rigCfg.DefaultBranch
	}
	baseRef := g.CleanBaseRef("origin", defaultBranch, doneTarget)

	// For COMPLETED, we need an issue ID and branch must not be the default branch
	var mrID string
	var pushFailed bool
	var mrFailed bool
	var doneErrors []string
	var convoyInfo *ConvoyInfo // Populated if issue is tracked by a convoy
	if exitType == ExitCompleted {
		if branch == defaultBranch || branch == "master" {
			return fmt.Errorf("cannot submit %s/master branch to merge queue", defaultBranch)
		}

		// CRITICAL: Verify work exists before completing (hq-xthqf)
		// Polecats calling gt done without commits results in lost work.
		// We MUST check for:
		// 1. Working directory availability (can't verify git state without it)
		// 2. Uncommitted changes (work that would be lost)
		// 3. Unique commits compared to origin (ensures branch was pushed with actual work)

		// Block if working directory not available - can't verify git state
		if !cwdAvailable {
			return fmt.Errorf("cannot complete: working directory not available (worktree deleted?)\nUse --status DEFERRED to exit without completing")
		}

		// Block if there are uncommitted changes (would be lost on completion).
		// Runtime artifacts (.claude/, .opencode/, .beads/, .runtime/, __pycache__/) are
		// excluded — these are toolchain-managed and normally gitignored.
		// Without this filter, gt done fails on virtually every polecat because
		// Cursor creates .claude/ at runtime in every workspace.
		workStatus, err := g.CheckUncommittedWork()
		if err != nil {
			return fmt.Errorf("checking git status: %w", err)
		}
		if workStatus.HasUncommittedChanges && !workStatus.CleanExcludingRuntime() {
			return fmt.Errorf("cannot complete: uncommitted changes would be lost\nCommit your changes first, or use --status DEFERRED to exit without completing\nUncommitted: %s", workStatus.String())
		}

		// Check if branch has commits ahead of the clean target base. In fork-backed
		// rigs this is upstream/main, not the fork's origin/main.
		aheadCount, err := g.CommitsAhead(baseRef, "HEAD")
		if err != nil {
			// Fallback to local branch comparison if origin not available
			aheadCount, err = g.CommitsAhead(defaultBranch, branch)
			if err != nil {
				// Can't determine - assume work exists and continue
				style.PrintWarning("could not check commits ahead of %s: %v", defaultBranch, err)
				aheadCount = 1
			}
		}

		// Check no_merge or review_only flags on the hooked bead. When set,
		// this is a non-code task (email, research, analysis, PRD review)
		// where zero commits is expected.
		// Must be checked before the zero-commit guard below (GH#2496, gt-kvf).
		isNoMergeTask := false
		reviewOnlySource := false
		if issueID != "" {
			noMergeBd := beads.New(cwd)
			noMergeIssue, showErr := noMergeBd.Show(issueID)
			if showErr != nil {
				return fmt.Errorf("cannot inspect source issue %s before completion: %w", issueID, showErr)
			}
			if af := beads.ParseAttachmentFields(noMergeIssue); af != nil {
				if af.NoMerge || af.ReviewOnly {
					isNoMergeTask = true
				}
				reviewOnlySource = af.ReviewOnly
			}
		}

		// If no commits ahead, work was likely already merged or is a legitimate
		// report-only completion. Fork-backed rigs must not infer success from fork main.
		// For polecats, zero commits usually means the polecat sleepwalked through
		// implementation without writing code (gastown#1484, beads#emma).
		// The --cleanup-status=clean escape is preserved for legitimate report-only
		// tasks (audits, reviews) that the formula explicitly directs to use it.
		// no_merge/review_only tasks (GH#2496, gt-kvf) also bypass: non-code work has no commits by design.
		// IMPORTANT: The error message must NOT mention --cleanup-status=clean.
		// LLM agents read error messages and self-bypass (the original bug).
		if aheadCount == 0 {
			if os.Getenv("GT_POLECAT") != "" && doneCleanupStatus != "clean" && !isNoMergeTask {
				// Before failing, check whether commits exist on the remote feature branch.
				// After a polecat pushes to origin/<feature-branch> and submits an MR,
				// if master advances (e.g., other MRs land), the feature branch is no
				// longer ahead of origin/master — but the work WAS committed and pushed.
				// In that case, treat as "MR already submitted" and fall through. (GH#wd7)
				branchPushedWithWork := false
				if branch != defaultBranch {
					pushed, unpushed, pushErr := g.BranchPushedToRemote(branch, "origin")
					branchPushedWithWork = pushErr == nil && pushed && unpushed == 0
				}
				if !branchPushedWithWork {
					return fmt.Errorf("cannot complete: no commits on branch ahead of %s\n"+
						"Polecats must have at least 1 commit to submit.\n"+
						"If the bug was already fixed upstream: gt done --status DEFERRED\n"+
						"If you're blocked: gt done --status ESCALATED",
						baseRef)
				}
			}

			// Non-polecat (crew/mayor), polecat with --cleanup-status=clean
			// (report-only tasks like audits/reviews), or no_merge polecat
			// (non-code tasks like email/research per GH#2496):
			// zero commits is valid.
			fmt.Printf("%s Branch has no commits ahead of %s\n", style.Bold.Render("→"), baseRef)
			fmt.Printf("  Work was likely already merged or report-only.\n")
			fmt.Printf("  Skipping MR creation - completing without merge request.\n\n")

			// G15 fix: Close the base issue when completing with no MR.
			// Without this, no-op polecats (bug already fixed) leave issues stuck
			// in HOOKED state with assignee pointing to the nuked polecat.
			// Normally the Refinery closes after merge, but with no MR, nothing
			// would ever close the issue.
			if issueID != "" {
				bd := beads.New(cwd)

				skipClose := false
				if skipReason, fatal := doneSourceCloseSkipReason(bd, issueID, nil); skipReason != "" {
					style.PrintWarning("%s", skipReason)
					fmt.Printf("  The bead will remain open for witness/mayor review.\n")
					notifyDoneCloseSkipped(townRoot, rigName, sender, issueID, skipReason)
					if fatal {
						return fmt.Errorf("cannot complete review-only/no-MR work: %s", skipReason)
					}
					skipClose = true
				}

				if !skipClose {
					closeReason := "Completed with no code changes (already fixed or already merged)"
					noMRCommitSHA, _ := g.Rev("HEAD")
					if doneSkipVerify {
						noteVerifiedPushSkipped(cwd, issueID, defaultBranch, noMRCommitSHA, "--skip-verify on no-MR close")
						if noMRCommitSHA != "" {
							closeReason = fmt.Sprintf("%s\nskip_verify: true\ntarget_branch: %s\ncommit_sha: %s", closeReason, defaultBranch, noMRCommitSHA)
						}
					} else if !isNoMergeTask {
						if g.ForkBackedRemote("origin") {
							return fmt.Errorf("cannot close no-MR code bead in fork/upstream mode: %s has no commits ahead of %s; use the fork PR flow instead", branch, baseRef)
						}
						if verifyErr := g.VerifyPushedCommitReachableFromPushTarget("origin", defaultBranch, noMRCommitSHA); verifyErr != nil {
							noteVerifiedPushFailure(cwd, issueID, defaultBranch, noMRCommitSHA, verifyErr)
							return fmt.Errorf("cannot close no-MR code bead: %w", verifyErr)
						}
						if noMRCommitSHA != "" {
							closeReason = fmt.Sprintf("%s\ntarget_branch: %s\ncommit_sha: %s", closeReason, defaultBranch, noMRCommitSHA)
						}
					}
					// G15 fix: Force-close bypasses molecule dependency checks.
					// The polecat is about to be nuked — open wisps should not block closure.
					// Retry with backoff handles transient dolt lock contention (A2).
					var closeErr error
					for attempt := 1; attempt <= 3; attempt++ {
						closeErr = bd.ForceCloseWithReason(closeReason, issueID)
						if closeErr == nil {
							fmt.Printf("%s Issue %s closed (no MR needed)\n", style.Bold.Render("✓"), issueID)
							break
						}
						if attempt < 3 {
							style.PrintWarning("close attempt %d/3 failed: %v (retrying in %ds)", attempt, closeErr, attempt*2)
							time.Sleep(time.Duration(attempt*2) * time.Second)
						}
					}
					if closeErr != nil {
						style.PrintWarning("could not close issue %s after 3 attempts: %v (issue may be left HOOKED)", issueID, closeErr)
					}
				}
			}

			// Skip straight to witness notification (no MR needed)
			goto notifyWitness
		}

		if reviewOnlySource {
			return fmt.Errorf("cannot complete review-only issue %s with commits ahead of %s; add a fresh review evidence comment and complete without code changes", issueID, baseRef)
		}

		// Branch contamination preflight: check if branch is significantly behind
		// the effective target branch, which indicates the branch may contain stale merge-base
		// artifacts that will pollute the PR diff. (GH#2220)
		//
		// gh#3400: Refresh remote tracking refs first so contamination check (and
		// the auto-rebase below) sees the current clean base. In fork-backed rigs,
		// that base is upstream/main, not the fork's origin/main.
		contaminationBase := baseRef
		if doneTarget != "" && doneTarget != defaultBranch {
			contaminationBase = doneContaminationBaseRef(defaultBranch, doneTarget)
		}
		fetchRemote := git.RemoteForRef(contaminationBase)
		if fetchRemote == "" {
			fetchRemote = "origin"
		}
		if fetchErr := g.Fetch(fetchRemote); fetchErr != nil {
			style.PrintWarning("could not fetch %s before contamination check: %v (proceeding with local refs)", fetchRemote, fetchErr)
		}
		contam, err := g.CheckBranchContamination(contaminationBase)
		if err == nil && contam.Behind > 0 {
			const warnThreshold = 50
			const blockThreshold = 200
			if contam.Behind >= blockThreshold {
				return fmt.Errorf("branch contamination: %d commits behind %s (threshold: %d)\n"+
					"The branch is severely stale and will include unrelated changes in the PR.\n"+
					"Fix: git fetch %s && git rebase %s",
					contam.Behind, contaminationBase, blockThreshold, fetchRemote, contaminationBase)
			} else if contam.Behind >= warnThreshold {
				style.PrintWarning("branch is %d commits behind %s — consider rebasing to avoid PR contamination", contam.Behind, contaminationBase)
			}

			// gh#3400: Auto-rebase the polecat branch onto the latest target before
			// push, so the resulting MR/PR has a current base.
			alreadyPushed := checkpoints[CheckpointPushed] == branch
			rebased, skipReason, rebaseErr := autoRebaseOnTarget(g, contaminationBase, contam.Behind, donePreVerified, alreadyPushed)
			if rebaseErr != nil {
				return rebaseErr
			}
			if rebased {
				fmt.Printf("%s Branch rebased onto %s\n", style.Bold.Render("✓"), contaminationBase)
				// Recompute commits ahead since rebase rewrote history.
				aheadCount, _ = g.CommitsAhead(baseRef, "HEAD")
			} else if skipReason != "" {
				style.PrintWarning("branch is %d commits behind %s but %s; skipping auto-rebase", contam.Behind, contaminationBase, skipReason)
			}
		}

		// Strip Gas Town overlay from CLAUDE.md / CLAUDE.local.md (gt-p35).
		// Polecats commit the overlay (polecat lifecycle boilerplate) into repos,
		// overwriting project-specific CLAUDE.md content. Detect and revert before push.
		if stripped := stripOverlayCLAUDEmd(g, defaultBranch, baseRef); stripped {
			// Recalculate commits ahead since we added a cleanup commit
			aheadCount, _ = g.CommitsAhead(baseRef, "HEAD")
		}

		// Determine merge strategy from convoy (gt-myofa.3)
		// Convoys can override the default MR-based workflow:
		//   direct: push commits straight to target branch, bypass refinery
		//   mr:     default — create merge-request bead, refinery merges
		//   local:  keep on feature branch, no push, no MR (for human review/upstream PRs)
		//
		// Primary: read convoy info from the issue's attachment fields (gt-7b6wf fix).
		// gt sling stores convoy_id and merge_strategy on the issue when dispatching,
		// which avoids unreliable cross-rig dep resolution at gt done time.
		// Fallback: dep-based lookup via getConvoyInfoForIssue (for issues dispatched
		// before this fix, or where attachment fields weren't set).
		convoyInfo = getConvoyInfoFromIssue(issueID, cwd)
		if convoyInfo == nil {
			convoyInfo = getConvoyInfoForIssue(issueID)
		}

		// Handle "local" strategy: skip push and MR entirely
		if convoyInfo != nil && convoyInfo.MergeStrategy == "local" {
			fmt.Printf("%s Local merge strategy: skipping push and merge queue\n", style.Bold.Render("→"))
			fmt.Printf("  Branch: %s\n", branch)
			if issueID != "" {
				fmt.Printf("  Issue: %s\n", issueID)
			}
			fmt.Println()
			fmt.Printf("%s\n", style.Dim.Render("Work stays on local feature branch."))
			goto notifyWitness
		}

		// Handle "direct" strategy: push to target branch, skip MR
		if convoyInfo != nil && convoyInfo.MergeStrategy == "direct" {
			fmt.Printf("%s Direct merge strategy: pushing to %s\n", style.Bold.Render("→"), defaultBranch)
			// Push submodule changes before direct push (gt-dzs)
			pushSubmoduleChanges(g, baseRef)
			directRefspec := branch + ":" + defaultBranch
			directPushErr := g.Push("origin", directRefspec, false)
			if directPushErr != nil {
				pushFailed = true
				errMsg := fmt.Sprintf("direct push to %s failed: %v", defaultBranch, directPushErr)
				doneErrors = append(doneErrors, errMsg)
				style.PrintWarning("%s", errMsg)
				goto notifyWitness
			}
			directCommitSHA, _ := g.Rev("HEAD")
			if doneSkipVerify {
				noteVerifiedPushSkipped(cwd, issueID, defaultBranch, directCommitSHA, "--skip-verify on direct merge")
			} else if verifyErr := g.VerifyPushedCommitReachableFromPushTarget("origin", defaultBranch, directCommitSHA); verifyErr != nil {
				pushFailed = true
				errMsg := verifyErr.Error()
				doneErrors = append(doneErrors, errMsg)
				noteVerifiedPushFailure(cwd, issueID, defaultBranch, directCommitSHA, verifyErr)
				style.PrintWarning("%s\nDirect merge pushed but remote verification failed. Source bead will remain in progress.", errMsg)
				goto notifyWitness
			}
			fmt.Printf("%s Branch pushed directly to %s\n", style.Bold.Render("✓"), defaultBranch)
			doneCleanupStatus = cleanupStatusAfterSuccessfulPush(doneCleanupStatus)

			// Close the base issue — no MR/refinery will close it
			if issueID != "" {
				directBd := beads.New(cwd)
				if skipReason, fatal := doneReviewOnlyCloseSkipReason(directBd, issueID, nil); skipReason != "" {
					style.PrintWarning("%s", skipReason)
					notifyDoneCloseSkipped(townRoot, rigName, sender, issueID, skipReason)
					if fatal {
						return fmt.Errorf("cannot complete review-only work: %s", skipReason)
					}
				} else {
					closeReason := fmt.Sprintf("Direct merge to %s (convoy strategy)", defaultBranch)
					var closeErr error
					for attempt := 1; attempt <= 3; attempt++ {
						closeErr = directBd.ForceCloseWithReason(closeReason, issueID)
						if closeErr == nil {
							fmt.Printf("%s Issue %s closed (direct merge)\n", style.Bold.Render("✓"), issueID)
							break
						}
						if attempt < 3 {
							style.PrintWarning("close attempt %d/3 failed: %v (retrying in %ds)", attempt, closeErr, attempt*2)
							time.Sleep(time.Duration(attempt*2) * time.Second)
						}
					}
					if closeErr != nil {
						style.PrintWarning("could not close issue %s after 3 attempts: %v", issueID, closeErr)
					}
				}
			}

			goto notifyWitness
		}

		// Default: "mr" strategy (or no convoy) — push branch, create MR bead

		// Pre-declare push variables for checkpoint goto (gt-aufru)
		var refspec string
		var pushErr error
		var pushedCommitSHA string

		// Resume: skip push if already completed in a previous run (gt-aufru).
		// Validate checkpoint branch matches current branch (ge-sbo: stale checkpoint
		// on polecat reassignment causes new work to skip push for old branch).
		if checkpoints[CheckpointPushed] != "" {
			if checkpoints[CheckpointPushed] == branch {
				fmt.Printf("%s Branch already pushed (resumed from checkpoint)\n", style.Bold.Render("✓"))
				goto afterPush
			}
			// Stale checkpoint from a previous assignment — discard and push normally.
			fmt.Printf("→ Discarding stale push checkpoint (was for branch %s, now on %s)\n",
				checkpoints[CheckpointPushed], branch)
		}

		// CRITICAL: Push branch BEFORE creating MR bead (hq-6dk53, hq-a4ksk)
		// The MR bead triggers Refinery to process this branch. If the branch
		// isn't pushed yet, Refinery finds nothing to merge. The worktree gets
		// nuked at the end of gt done, so the commits are lost forever.
		//
		// Auto-push submodule changes BEFORE parent push (gt-dzs).
		// If the parent repo's submodule pointer references commits that don't
		// exist on the submodule's remote, the Refinery MR will be broken.
		// Detect modified submodules and push each one first.
		pushSubmoduleChanges(g, baseRef)

		// Use explicit refspec (branch:branch) to create the remote branch.
		// Without refspec, git push follows the tracking config — polecat branches
		// track origin/main, so a bare push sends commits to main directly,
		// bypassing the MR/refinery flow (G20 root cause).
		fmt.Printf("Pushing branch to remote...\n")
		refspec = branch + ":" + branch
		pushedCommitSHA, _ = g.Rev("HEAD")
		pushErr = g.Push("origin", refspec, false)
		if pushErr != nil {
			// Primary push failed — try fallback from the bare repo (GH #1348).
			// When polecat sessions are reused or worktrees are stale, the worktree's
			// git context may be broken. But the branch always exists in the bare repo
			// (.repo.git) because worktree commits share the same object database.
			style.PrintWarning("primary push failed: %v — trying bare repo fallback...", pushErr)
			rigPath := filepath.Join(townRoot, rigName)
			bareRepoPath := filepath.Join(rigPath, ".repo.git")
			if _, statErr := os.Stat(bareRepoPath); statErr == nil {
				bareGit := git.NewGitWithDir(bareRepoPath, "")
				pushErr = bareGit.Push("origin", refspec, false)
				if pushErr != nil {
					style.PrintWarning("bare repo push also failed: %v", pushErr)
				} else {
					fmt.Printf("%s Branch pushed via bare repo fallback\n", style.Bold.Render("✓"))
				}
			} else {
				// No bare repo — try mayor/rig as last resort
				mayorPath := filepath.Join(rigPath, "mayor", "rig")
				if _, statErr := os.Stat(mayorPath); statErr == nil {
					mayorGit := git.NewGit(mayorPath)
					pushErr = mayorGit.Push("origin", refspec, false)
					if pushErr != nil {
						style.PrintWarning("mayor/rig push also failed: %v", pushErr)
					} else {
						fmt.Printf("%s Branch pushed via mayor/rig fallback\n", style.Bold.Render("✓"))
					}
				}
			}
		}

		if pushErr != nil {
			// All push attempts failed
			pushFailed = true
			errMsg := fmt.Sprintf("push failed for branch '%s': %v", branch, pushErr)
			doneErrors = append(doneErrors, errMsg)
			style.PrintWarning("%s\nCommits exist locally but failed to push. Witness will be notified.", errMsg)
			goto notifyWitness
		}

		// Verify the pushed branch tip is the exact local commit before creating
		// any MR bead. Branch-exists checks are insufficient: a stale remote
		// branch can exist while the new commit never reached origin.
		if pushedCommitSHA == "" {
			pushedCommitSHA, _ = g.Rev("HEAD")
		}
		if doneSkipVerify {
			noteVerifiedPushSkipped(cwd, issueID, branch, pushedCommitSHA, "--skip-verify on branch push")
		} else if verifyErr := verifyPushedCommitWithBareFallback(g, townRoot, rigName, branch, pushedCommitSHA); verifyErr != nil {
			pushFailed = true
			errMsg := verifyErr.Error()
			doneErrors = append(doneErrors, errMsg)
			noteVerifiedPushFailure(cwd, issueID, branch, pushedCommitSHA, verifyErr)
			style.PrintWarning("%s\nCommits exist locally but verified push failed. Witness will be notified.", errMsg)
			goto notifyWitness
		}
		fmt.Printf("%s Branch pushed to origin\n", style.Bold.Render("✓"))

		// Fix cleanup_status after successful push (gt-wcr).
		// Status was detected before push, so "unpushed" is now stale.
		doneCleanupStatus = cleanupStatusAfterSuccessfulPush(doneCleanupStatus)

		// Write push checkpoint for resume (gt-aufru)
		if agentBeadID != "" {
			// Agent bead lives in town DB despite rig prefix — bypass routing.
			cpBd := beads.New(cwd).ForAgentBead()
			writeDoneCheckpoint(cpBd, agentBeadID, CheckpointPushed, branch)
		}

	afterPush:

		if issueID == "" {
			return fmt.Errorf("cannot determine source issue from branch '%s'; use --issue to specify", branch)
		}

		// Initialize beads — warn if resolved to a local .beads/ (no redirect).
		// Without a redirect, MR beads are invisible to the Refinery.
		resolvedBeads := beads.ResolveBeadsDir(cwd)
		if beads.IsLocalBeadsDir(cwd, resolvedBeads) {
			fmt.Fprintf(os.Stderr, "WARNING: beads resolved to local dir %s (no shared-beads redirect)\n", resolvedBeads)
			fmt.Fprintf(os.Stderr, "  MR beads written here will be invisible to the Refinery — run 'gt polecat repair' to fix\n")
		}
		bd := beads.NewWithBeadsDir(cwd, resolvedBeads)

		// Check for no_merge flag - if set, skip merge queue and notify for review
		sourceIssueForNoMerge, err := bd.Show(issueID)
		if err == nil {
			attachmentFields := beads.ParseAttachmentFields(sourceIssueForNoMerge)
			if attachmentFields != nil && attachmentFields.NoMerge {
				fmt.Printf("%s No-merge mode: skipping merge queue\n", style.Bold.Render("→"))
				fmt.Printf("  Branch: %s\n", branch)
				fmt.Printf("  Issue: %s\n", issueID)
				fmt.Println()

				// When merge_strategy=pr, create a GitHub PR for human review
				// instead of just leaving the branch on origin (gas-rfi).
				var prURL string
				noMergeSettingsPath := filepath.Join(townRoot, rigName, "settings", "config.json")
				if noMergeSettings, noMergeSettingsErr := config.LoadRigSettings(noMergeSettingsPath); noMergeSettingsErr == nil &&
					noMergeSettings.MergeQueue != nil && noMergeSettings.MergeQueue.MergeStrategy == "pr" {
					issueTitle := sourceIssueForNoMerge.Title
					prTitle := fmt.Sprintf("%s (%s)", issueTitle, issueID)
					if issueTitle == "" {
						prTitle = issueID
					}
					// Build PR body from bead description + diff stat
					var prBodyBuilder strings.Builder
					prBodyBuilder.WriteString("## Summary\n\n")
					if sourceIssueForNoMerge.Description != "" {
						// Strip attachment metadata lines from description
						descLines := strings.Split(sourceIssueForNoMerge.Description, "\n")
						var cleanDesc []string
						for _, line := range descLines {
							trimmed := strings.TrimSpace(line)
							if strings.HasPrefix(trimmed, "attached_") || strings.HasPrefix(trimmed, "dispatched_by:") || strings.HasPrefix(trimmed, "formula_vars:") {
								continue
							}
							cleanDesc = append(cleanDesc, line)
						}
						desc := strings.TrimSpace(strings.Join(cleanDesc, "\n"))
						if desc != "" {
							prBodyBuilder.WriteString(desc)
							prBodyBuilder.WriteString("\n\n")
						}
					}
					// Add diff stat for quick review context
					if diffStat, diffErr := g.DiffStat(baseRef + "..." + branch); diffErr == nil && diffStat != "" {
						prBodyBuilder.WriteString("## Changes\n\n```\n")
						prBodyBuilder.WriteString(diffStat)
						prBodyBuilder.WriteString("```\n\n")
					}
					prBodyBuilder.WriteString("---\n")
					prBodyBuilder.WriteString(fmt.Sprintf("*Polecat: %s | Issue: %s*\n", worker, issueID))
					prBody := prBodyBuilder.String()
					ghCmd := exec.CommandContext(context.Background(), "gh", "pr", "create",
						"--base", defaultBranch,
						"--head", branch,
						"--title", prTitle,
						"--body", prBody,
					)
					ghCmd.Dir = cwd
					prOutput, prErr := ghCmd.Output()
					if prErr != nil {
						style.PrintWarning("could not create GitHub PR: %v", prErr)
					} else {
						prURL = strings.TrimSpace(string(prOutput))
						fmt.Printf("%s GitHub PR created: %s\n", style.Bold.Render("✓"), prURL)
					}
				} else {
					fmt.Printf("%s\n", style.Dim.Render("Work stays on feature branch for human review."))
				}

				// Mail dispatcher with READY_FOR_REVIEW
				if dispatcher := attachmentFields.DispatchedBy; dispatcher != "" {
					townRouter := mail.NewRouter(townRoot)
					defer townRouter.WaitPendingNotifications()
					reviewBody := fmt.Sprintf("Branch: %s\nIssue: %s\nReady for review.", branch, issueID)
					if prURL != "" {
						reviewBody = fmt.Sprintf("Branch: %s\nIssue: %s\nPR: %s\nReady for review.", branch, issueID, prURL)
					}
					reviewMsg := &mail.Message{
						To:      dispatcher,
						From:    detectSender(),
						Subject: fmt.Sprintf("READY_FOR_REVIEW: %s", issueID),
						Body:    reviewBody,
					}
					if err := townRouter.Send(reviewMsg); err != nil {
						style.PrintWarning("could not notify dispatcher: %v", err)
					} else {
						fmt.Printf("%s Dispatcher notified: READY_FOR_REVIEW\n", style.Bold.Render("✓"))
					}
				}

				// No-merge work never goes through the refinery, so close the source bead
				// here after notifying the dispatcher. Otherwise hooked work remains open.
				if issueID != "" {
					canCloseIssue := true
					if skipReason, fatal := doneReviewOnlyCloseSkipReason(bd, issueID, sourceIssueForNoMerge); skipReason != "" {
						style.PrintWarning("%s", skipReason)
						notifyDoneCloseSkipped(townRoot, rigName, sender, issueID, skipReason)
						if fatal {
							return fmt.Errorf("cannot complete review-only/no-merge work: %s", skipReason)
						}
						canCloseIssue = false
					}
					if canCloseIssue && attachmentFields.AttachedMolecule != "" {
						if n := closeDescendants(bd, attachmentFields.AttachedMolecule); n > 0 {
							fmt.Fprintf(os.Stderr, "Closed %d molecule step(s) for %s\n", n, attachmentFields.AttachedMolecule)
						}
						if closeErr := forceCloseIssueWithRetry(
							bd.ForceCloseWithReason,
							attachmentFields.AttachedMolecule,
							"done",
							"Attached molecule %s closed",
						); closeErr != nil && !errors.Is(closeErr, beads.ErrNotFound) {
							style.PrintWarning("could not close attached molecule %s after 3 attempts: %v", attachmentFields.AttachedMolecule, closeErr)
							canCloseIssue = false
						}
					}

					closeReason := "No-merge work completed; merge queue skipped"
					if prURL != "" {
						closeReason = fmt.Sprintf("%s\npr_url: %s", closeReason, prURL)
					}
					if canCloseIssue {
						if closeErr := forceCloseIssueWithRetry(
							bd.ForceCloseWithReason,
							issueID,
							closeReason,
							"Issue %s closed (no-merge)",
						); closeErr != nil {
							style.PrintWarning("could not close issue %s after 3 attempts: %v (issue may be left HOOKED)", issueID, closeErr)
						}
					}
				}

				// Skip MR creation, go to witness notification
				goto notifyWitness
			}
		}

		// Fallback: check if issue belongs to a direct-merge convoy that the
		// primary check (line ~483) missed — e.g., issues dispatched before the
		// attachment-field fix, or where dep-based lookup failed at that point.
		// At this stage the branch was pushed to origin/<branch> (feature branch),
		// NOT to main. So we must push to main now before skipping MR creation.
		convoyInfo = getConvoyInfoFromIssue(issueID, cwd)
		if convoyInfo == nil {
			convoyInfo = getConvoyInfoForIssue(issueID)
		}
		if convoyInfo != nil && convoyInfo.MergeStrategy == "direct" {
			fmt.Printf("%s Late-detected direct merge strategy: pushing to %s\n", style.Bold.Render("→"), defaultBranch)
			fmt.Printf("  Convoy: %s\n", convoyInfo.ID)

			// Push branch directly to main (the earlier push went to origin/<branch>)
			directRefspec := branch + ":" + defaultBranch
			directPushErr := g.Push("origin", directRefspec, false)
			if directPushErr != nil {
				// Direct push failed — fall through to normal MR creation
				style.PrintWarning("late direct push to %s failed: %v — falling through to MR", defaultBranch, directPushErr)
			} else {
				lateDirectCommitSHA, _ := g.Rev("HEAD")
				if doneSkipVerify {
					noteVerifiedPushSkipped(cwd, issueID, defaultBranch, lateDirectCommitSHA, "--skip-verify on late direct merge")
				} else if verifyErr := g.VerifyPushedCommitReachableFromPushTarget("origin", defaultBranch, lateDirectCommitSHA); verifyErr != nil {
					pushFailed = true
					errMsg := verifyErr.Error()
					doneErrors = append(doneErrors, errMsg)
					noteVerifiedPushFailure(cwd, issueID, defaultBranch, lateDirectCommitSHA, verifyErr)
					style.PrintWarning("%s\nLate direct merge pushed but remote verification failed. Source bead will remain in progress.", errMsg)
					goto notifyWitness
				}
				fmt.Printf("%s Branch pushed directly to %s\n", style.Bold.Render("✓"), defaultBranch)
				doneCleanupStatus = cleanupStatusAfterSuccessfulPush(doneCleanupStatus)

				// Close the issue directly — refinery won't process it.
				if issueID != "" {
					if skipReason, fatal := doneReviewOnlyCloseSkipReason(bd, issueID, nil); skipReason != "" {
						style.PrintWarning("%s", skipReason)
						notifyDoneCloseSkipped(townRoot, rigName, sender, issueID, skipReason)
						if fatal {
							return fmt.Errorf("cannot complete review-only work: %s", skipReason)
						}
					} else {
						var closeErr error
						for attempt := 1; attempt <= 3; attempt++ {
							closeErr = bd.ForceCloseWithReason(
								fmt.Sprintf("Direct merge to %s (convoy strategy, late detection)", defaultBranch), issueID)
							if closeErr == nil {
								fmt.Printf("%s Issue %s closed (direct merge)\n", style.Bold.Render("✓"), issueID)
								break
							}
							if attempt < 3 {
								style.PrintWarning("close attempt %d/3 failed: %v (retrying in %ds)", attempt, closeErr, attempt*2)
								time.Sleep(time.Duration(attempt*2) * time.Second)
							}
						}
						if closeErr != nil {
							style.PrintWarning("could not close issue %s after 3 attempts: %v", issueID, closeErr)
						}
					}
				}

				goto notifyWitness
			}
		}

		// Determine target branch for the MR.
		// Priority: explicit --target flag > formula_vars base_branch > integration branch auto-detect > rig default.
		target := defaultBranch
		explicitTarget := false

		// 1. Explicit --target flag (highest priority — polecat knows its base branch).
		// This is the most reliable path: the formula passes {{base_branch}} directly,
		// avoiding any dependency on bd.Show() or Dolt availability.
		if doneTarget != "" {
			target = doneTarget
			explicitTarget = true
			fmt.Printf("  Target branch: %s (from --target flag)\n", target)
		}

		// 2. Check for --base-branch override in formula vars (stored on bead at sling time).
		// Fallback for polecats dispatched before --target flag existed, or when
		// the formula doesn't pass --target explicitly.
		if !explicitTarget && target == defaultBranch && sourceIssueForNoMerge != nil {
			if af := beads.ParseAttachmentFields(sourceIssueForNoMerge); af != nil {
				if bb := extractFormulaVar(af.FormulaVars, "base_branch"); bb != "" && bb != defaultBranch {
					target = bb
					fmt.Printf("  Target branch override: %s (from formula_vars)\n", target)
				}
			}
		} else if !explicitTarget && target == defaultBranch && sourceIssueForNoMerge == nil && issueID != "" {
			// sourceIssueForNoMerge is nil — bd.Show(issueID) failed earlier.
			// This is the silent failure path that caused 150+ procedure beads to
			// target main instead of feat/contract-review-procedure.
			style.PrintWarning("could not load source issue %s for target branch detection (Dolt/beads lookup failed) — using default branch %s", issueID, defaultBranch)
		}

		// 3. Auto-detect integration branch from epic hierarchy (if enabled).
		// Only overrides if no explicit target was set above.
		if !explicitTarget && target == defaultBranch {
			refineryEnabled := true
			settingsPath := filepath.Join(townRoot, rigName, "settings", "config.json")
			if settings, err := config.LoadRigSettings(settingsPath); err == nil && settings.MergeQueue != nil {
				refineryEnabled = settings.MergeQueue.IsRefineryIntegrationEnabled()
			}
			if refineryEnabled {
				autoTarget, err := beads.DetectIntegrationBranch(bd, g, issueID)
				if err == nil && autoTarget != "" {
					target = autoTarget
				}
			}
		}

		// Get source issue for priority inheritance
		var priority int
		if donePriority >= 0 {
			priority = donePriority
		} else {
			sourceIssue, err := bd.Show(issueID)
			if err != nil {
				priority = 2 // Default
			} else {
				priority = sourceIssue.Priority
			}
		}

		// Pre-declare for checkpoint goto (gt-aufru)
		var existingMR *beads.Issue
		var commitSHA string

		// GH#3032: Resolve HEAD commit SHA for MR dedup.
		// Branch name alone is not a valid dedup key — a polecat may push new
		// commits to the same branch after a gate failure. The commit SHA
		// distinguishes genuinely new submissions from idempotent retries.
		commitSHA, _ = g.Rev("HEAD")

		// Resume: skip MR creation if already completed in a previous run (gt-aufru).
		// Mirrors the push checkpoint pattern above. Without this, every retry
		// re-attempts bd.Create which hits unique constraints or creates duplicates.
		// Validate that the checkpoint MR corresponds to the current branch (ge-sbo:
		// stale checkpoint on polecat reassignment would reuse old MR for new work).
		if checkpoints[CheckpointMRCreated] != "" {
			cpMRID := checkpoints[CheckpointMRCreated]
			if cpMR, cpErr := bd.Show(cpMRID); cpErr == nil && cpMR != nil {
				branchPrefix := "branch: " + branch + "\n"
				if strings.HasPrefix(cpMR.Description, branchPrefix) {
					mrID = cpMRID
					fmt.Printf("%s MR already created (resumed from checkpoint: %s)\n", style.Bold.Render("✓"), mrID)
					goto afterMR
				}
				// Checkpoint MR is for a different branch — discard and create fresh.
				fmt.Printf("→ Discarding stale MR checkpoint %s (was for different branch)\n", cpMRID)
			}
			// If MR lookup fails, fall through to create/find MR normally.
		}

		// Check if MR bead already exists for this branch+SHA (idempotency)
		if commitSHA != "" {
			existingMR, err = bd.FindMRForBranchAndSHA(branch, commitSHA)
		} else {
			existingMR, err = bd.FindMRForBranch(branch)
		}
		if err != nil {
			style.PrintWarning("could not check for existing MR: %v", err)
			// Continue with creation attempt - Create will fail if duplicate
		}

		if existingMR != nil {
			// MR already exists with same branch AND commit — true idempotent retry
			mrID = existingMR.ID
			fmt.Printf("%s MR already exists (idempotent)\n", style.Bold.Render("✓"))
			fmt.Printf("  MR ID: %s\n", style.Bold.Render(mrID))
		} else {
			// Build MR bead title and description
			title := fmt.Sprintf("Merge: %s", issueID)
			description := fmt.Sprintf("branch: %s\ntarget: %s\nsource_issue: %s\nrig: %s",
				branch, target, issueID, rigName)
			if commitSHA != "" {
				description += fmt.Sprintf("\ncommit_sha: %s", commitSHA)
			}
			if doneSkipVerify {
				description += "\nskip_verify: true"
			}
			if worker != "" {
				description += fmt.Sprintf("\nworker: %s", worker)
			}
			if agentBeadID != "" {
				description += fmt.Sprintf("\nagent_bead: %s", agentBeadID)
			}

			// Add conflict resolution tracking fields (initialized, updated by Refinery)
			description += "\nretry_count: 0"
			description += "\nlast_conflict_sha: null"
			description += "\nconflict_task_id: null"

			// Phase 3: Add pre-verification metadata if polecat ran gates after rebasing.
			// The refinery uses these fields to fast-path merge without re-running gates.
			if donePreVerified {
				description += "\npre_verified: true"
				description += fmt.Sprintf("\npre_verified_at: %s", time.Now().UTC().Format(time.RFC3339))
				// Capture current clean target HEAD as the verified base.
				// The polecat rebased onto this SHA before running gates.
				verifiedBaseRef := g.CleanBaseRef("origin", defaultBranch, target)
				if verifiedBase, baseErr := g.Rev(verifiedBaseRef); baseErr == nil {
					description += fmt.Sprintf("\npre_verified_base: %s", verifiedBase)
				} else {
					style.PrintWarning("could not resolve %s for pre-verified base: %v (pre-verification data incomplete)", verifiedBaseRef, baseErr)
				}
			}

			mrIssue, err := bd.Create(beads.CreateOptions{
				Title:       title,
				Labels:      []string{"gt:merge-request"},
				Priority:    priority,
				Description: description,
				Ephemeral:   true,
				Rig:         rigName, // Ensure MR bead is created in the rig's database (gt-7y7)
			})
			if err != nil {
				// Non-fatal: record the error and skip to notifyWitness.
				// Push succeeded so branch is on remote, but MR bead failed.
				// Set mrFailed so the witness knows not to send MERGE_READY.
				mrFailed = true
				errMsg := fmt.Sprintf("MR bead creation failed: %v", err)
				doneErrors = append(doneErrors, errMsg)
				style.PrintWarning("%s\nBranch is pushed but MR bead not created. Witness will be notified.", errMsg)
				goto notifyWitness
			}
			mrID = mrIssue.ID

			// Guard against empty ID from bd create (observed in ephemeral/wisp mode).
			// Fail fast with a clear message rather than passing "" to bd.Show.
			if mrID == "" {
				mrFailed = true
				errMsg := "MR bead creation returned empty ID"
				doneErrors = append(doneErrors, errMsg)
				style.PrintWarning("%s\nBranch is pushed but MR bead has no ID. Witness will be notified.", errMsg)
				goto notifyWitness
			}

			// GH#1945: Verify MR bead is readable before considering it confirmed.
			// bd.Create() succeeds when the bead is written locally, but if the write
			// didn't persist (Dolt failure, corrupt state), we'd nuke the worktree
			// with no MR in the queue — losing the polecat's work permanently.
			if verifiedMR, verifyErr := bd.Show(mrID); verifyErr != nil || verifiedMR == nil {
				mrFailed = true
				errMsg := fmt.Sprintf("MR bead created but verification read-back failed (id=%s): %v", mrID, verifyErr)
				doneErrors = append(doneErrors, errMsg)
				style.PrintWarning("%s\nBranch is pushed but MR bead not confirmed. Preserving worktree.", errMsg)
				goto notifyWitness
			}

			// gt-gpy: Validate that the MR bead landed in the rig's database.
			// If the source bead has a cross-rig prefix (e.g., hq-), the routing
			// could still resolve to the wrong database despite Rig: rigName.
			// This is a warning-only guard — mrFailed is NOT set on mismatch.
			if prefixErr := beads.ValidateRigPrefix(townRoot, rigName, mrID); prefixErr != nil {
				style.PrintWarning("MR bead prefix mismatch: %v\nThe refinery may not find this MR — check 'gt mq list %s'", prefixErr, rigName)
			}

			// GH#3032: Supersede older open MRs for the same source issue.
			// When a polecat re-submits after fixing a gate failure, the old MR
			// (same branch, different SHA) is stale. Close it so the refinery
			// doesn't process the old submission.
			if issueID != "" {
				if oldMRs, findErr := bd.FindOpenMRsForIssue(issueID); findErr == nil {
					for _, old := range oldMRs {
						if old.ID == mrID {
							continue // skip the one we just created
						}
						reason := fmt.Sprintf("superseded by %s", mrID)
						if closeErr := bd.CloseWithReason(reason, old.ID); closeErr != nil {
							style.PrintWarning("could not supersede old MR %s: %v", old.ID, closeErr)
							continue
						}
						fmt.Printf("  %s Superseded old MR: %s\n", style.Dim.Render("○"), old.ID)
					}
				}
			}

			// Update agent bead with active_mr reference (for traceability).
			// Agent beads live in HQ regardless of rig prefix — bypass routing
			// via ForAgentBead() to avoid the "issue not found" warning that
			// leaves active_mr null after every gt done (hq-e73z).
			if agentBeadID != "" {
				if err := bd.ForAgentBead().UpdateAgentActiveMR(agentBeadID, mrID); err != nil {
					style.PrintWarning("could not update agent bead with active_mr: %v", err)
				}
			}

			// GH#2599: Back-link source issue to MR bead for discoverability.
			if issueID != "" {
				comment := fmt.Sprintf("MR created: %s", mrID)
				if _, err := bd.Run("comments", "add", issueID, comment); err != nil {
					style.PrintWarning("could not back-link source issue %s to MR %s: %v", issueID, mrID, err)
				}
			}

			// Success output
			fmt.Printf("%s Work submitted to merge queue (verified)\n", style.Bold.Render("✓"))
			fmt.Printf("  MR ID: %s\n", style.Bold.Render(mrID))

			// NOTE: Refinery nudge is deferred to AFTER the Dolt branch merge
			// (see post-merge nudge below). Nudging here would race with the
			// merge — refinery wakes up and queries main before the polecat's
			// Dolt branch (containing the MR bead) is merged.
		}

		// Write MR checkpoint for resume (gt-aufru)
		if mrID != "" && agentBeadID != "" {
			// Agent bead lives in town DB despite rig prefix — bypass routing.
			cpBd := beads.New(cwd).ForAgentBead()
			writeDoneCheckpoint(cpBd, agentBeadID, CheckpointMRCreated, mrID)
		}

	afterMR:
		fmt.Printf("  Source: %s\n", branch)
		fmt.Printf("  Target: %s\n", target)
		fmt.Printf("  Issue: %s\n", issueID)
		if worker != "" {
			fmt.Printf("  Worker: %s\n", worker)
		}
		fmt.Printf("  Priority: P%d\n", priority)
		fmt.Println()
		fmt.Printf("%s\n", style.Dim.Render("The Refinery will process your merge request."))
	} else {
		// For ESCALATED or DEFERRED, just print status
		fmt.Printf("%s Signaling %s\n", style.Bold.Render("→"), exitType)
		if issueID != "" {
			fmt.Printf("  Issue: %s\n", issueID)
		}
		fmt.Printf("  Branch: %s\n", branch)
	}

notifyWitness:
	// Nudge refinery — MR bead is already on main (transaction-based shared main).
	if shouldNudgeRefinery(exitType, mrID) {
		nudgeRefinery(rigName, "MERGE_READY received - check inbox for pending work")
	}

	// Write completion metadata to agent bead for audit trail.
	// Self-managed completion (gt-1qlg): metadata is retained for anomaly
	// detection and crash recovery by witness patrol, but the witness no
	// longer processes routine completions from these fields.
	fmt.Printf("\nNotifying Witness...\n")
	if agentBeadID != "" {
		// Agent bead lives in town DB despite rig prefix — bypass routing.
		completionBd := beads.New(cwd).ForAgentBead()
		meta := &beads.CompletionMetadata{
			ExitType:       exitType,
			MRID:           mrID,
			Branch:         branch,
			HookBead:       issueID,
			MRFailed:       mrFailed,
			PushFailed:     pushFailed,
			CompletionTime: time.Now().UTC().Format(time.RFC3339),
		}
		if err := completionBd.UpdateAgentCompletion(agentBeadID, meta); err != nil {
			style.PrintWarning("could not write completion metadata to agent bead: %v", err)
		}
	}

	// Write witness notification checkpoint for resume (gt-aufru)
	if agentBeadID != "" {
		// Agent bead lives in town DB despite rig prefix — bypass routing.
		cpBd := beads.New(cwd).ForAgentBead()
		writeDoneCheckpoint(cpBd, agentBeadID, CheckpointWitnessNotified, "ok")
	}

	// Log done event (townlog and activity feed)
	if err := LogDone(townRoot, sender, issueID); err != nil {
		style.PrintWarning("could not log done event: %v", err)
	}
	if err := events.LogFeed(events.TypeDone, sender, events.DonePayload(issueID, branch)); err != nil {
		style.PrintWarning("could not log feed event: %v", err)
	}

	// Update agent bead state (ZFC: self-report completion). If push/MR failed,
	// keep the hook intact so Witness can recover the still-open work.
	if err := updateAgentStateAfterSubmission(cwd, townRoot, exitType, issueID, pushFailed, mrFailed); err != nil {
		return err
	}

	// Nudge witness only after hook/cleanup state is updated. Otherwise witness can
	// evaluate slot availability against stale hook_bead or cleanup_status and emit
	// false SLOT_BLOCKED/SLOT_OPEN signals.
	nudgeWitness(rigName, fmt.Sprintf("POLECAT_DONE %s exit=%s", polecatName, exitType))
	fmt.Printf("%s Witness notified of %s (via nudge)\n", style.Bold.Render("✓"), exitType)

	// Clean successful polecats are retired after durable handoff. Preserve the
	// feature branch and metadata; Witness/refinery cleanup owns the sandbox.
	isPolecat := false
	retirePolecat := false
	if roleInfo, err := GetRoleWithContext(cwd, townRoot); err == nil && roleInfo.Role == RolePolecat {
		isPolecat = true

		if pushFailed || mrFailed {
			fmt.Printf("%s Work needs recovery (push or MR failed) — session preserved\n", style.Bold.Render("⚠"))
		}
		if exitType == ExitCompleted && issueID != "" && convoyInfo == nil {
			convoyInfo = getConvoyInfoFromIssue(issueID, cwd)
			if convoyInfo == nil {
				convoyInfo = getConvoyInfoForIssue(issueID)
			}
		}
		mergeStrategy := ""
		if convoyInfo != nil {
			mergeStrategy = convoyInfo.MergeStrategy
		}
		retirePolecat = shouldRetirePolecatSessionAfterDone(exitType, mergeStrategy, pushFailed, mrFailed)
		if retirePolecat {
			fmt.Printf("%s Polecat session retiring after durable handoff\n", style.Bold.Render("✓"))
		} else {
			fmt.Printf("%s Session preserved for recovery or local review\n", style.Bold.Render("→"))
		}
	}

	fmt.Println()
	if !isPolecat {
		fmt.Printf("%s Session exiting\n", style.Bold.Render("→"))
		fmt.Printf("  Witness will handle cleanup.\n")
	}

	// Retire the live session as the final action. The PID exclusion prevents
	// killing gt done before all metadata and notifications above are written.
	if retirePolecat {
		fmt.Printf("%s Terminating polecat session\n", style.Bold.Render("→"))
		if err := retirePolecatSessionAfterDone(rigName, polecatName, os.Getpid()); err != nil {
			style.PrintWarning("could not terminate polecat session: %v", err)
		}
	}

	return nil
}

// pushSubmoduleChanges detects submodules modified between baseRef
// and HEAD, and pushes each submodule's new commit to its remote before the
// parent repo push. This prevents the parent's submodule pointer from
// referencing commits that don't exist on the submodule's remote (gt-dzs).
func pushSubmoduleChanges(g *git.Git, baseRef string) {
	subChanges, err := g.SubmoduleChanges(baseRef, "HEAD")
	if err != nil {
		// Non-fatal: repos without submodules return nil, nil.
		// Only warn if the error is real (not just "no submodules").
		style.PrintWarning("could not detect submodule changes: %v", err)
		return
	}
	for _, sc := range subChanges {
		if sc.NewSHA == "" {
			continue // Submodule removed, nothing to push
		}
		shortSHA := sc.NewSHA
		if len(shortSHA) > 8 {
			shortSHA = shortSHA[:8]
		}
		fmt.Printf("Pushing submodule %s (%s)...\n", sc.Path, shortSHA)
		if subPushErr := g.PushSubmoduleCommit(sc.Path, sc.NewSHA, "origin"); subPushErr != nil {
			style.PrintWarning("submodule push failed for %s: %v (parent push may fail)", sc.Path, subPushErr)
		} else {
			fmt.Printf("%s Submodule %s pushed\n", style.Bold.Render("✓"), sc.Path)
		}
	}
}

func forceCloseIssueWithRetry(closeFn func(string, ...string) error, issueID, reason, successFormat string) error {
	return forceCloseIssueWithRetrySleep(closeFn, issueID, reason, successFormat, time.Sleep)
}

func forceCloseIssueWithRetrySleep(closeFn func(string, ...string) error, issueID, reason, successFormat string, sleep func(time.Duration)) error {
	var closeErr error
	for attempt := 1; attempt <= 3; attempt++ {
		closeErr = closeFn(reason, issueID)
		if closeErr == nil {
			fmt.Printf("%s "+successFormat+"\n", style.Bold.Render("✓"), issueID)
			return nil
		}
		if attempt < 3 {
			style.PrintWarning("close attempt %d/3 failed: %v (retrying in %ds)", attempt, closeErr, attempt*2)
			sleep(time.Duration(attempt*2) * time.Second)
		}
	}
	return closeErr
}

func notifyDoneCloseSkipped(townRoot, rigName, sender, issueID, reason string) {
	if townRoot == "" || rigName == "" || issueID == "" {
		return
	}
	if sender == "" {
		sender = fmt.Sprintf("%s/polecat", rigName)
	}

	router := mail.NewRouter(townRoot)
	defer router.WaitPendingNotifications()
	msg := &mail.Message{
		To:      fmt.Sprintf("%s/witness", rigName),
		From:    sender,
		Subject: fmt.Sprintf("DONE_CLOSE_SKIPPED: %s", issueID),
		Body: fmt.Sprintf("gt done skipped closing %s.\n\nReason: %s\n\nThe bead remains open for witness/mayor review.",
			issueID, reason),
	}
	if err := router.Send(msg); err != nil {
		style.PrintWarning("could not notify witness about skipped close: %v", err)
	} else {
		fmt.Printf("%s Witness notified: DONE_CLOSE_SKIPPED\n", style.Bold.Render("✓"))
	}
}

func noteVerifiedPushFailure(cwd, issueID, branch, commit string, verifyErr error) {
	if issueID == "" || cwd == "" {
		return
	}
	bd := beads.New(cwd)
	inProgress := "in_progress"
	_ = bd.Update(issueID, beads.UpdateOptions{Status: &inProgress})
	msg := fmt.Sprintf("verified_push_failed: commit %s not verified on origin/%s: %v", commit, branch, verifyErr)
	_, _ = bd.Run("comments", "add", issueID, msg)
}

func noteVerifiedPushSkipped(cwd, issueID, branch, commit, reason string) {
	if issueID == "" || cwd == "" {
		return
	}
	msg := fmt.Sprintf("verified_push_skipped: commit %s branch origin/%s reason=%s", commit, branch, reason)
	_, _ = beads.New(cwd).Run("comments", "add", issueID, msg)
}

func verifyPushedCommitWithBareFallback(g *git.Git, townRoot, rigName, branch, commit string) error {
	verifyErr := g.VerifyPushedCommit("origin", branch, commit)
	if verifyErr == nil {
		return nil
	}

	bareRepoPath := filepath.Join(townRoot, rigName, ".repo.git")
	if _, statErr := os.Stat(bareRepoPath); statErr != nil {
		return verifyErr
	}
	bareGit := git.NewGitWithDir(bareRepoPath, "")
	tip, tipErr := bareGit.Rev("refs/heads/" + branch)
	if tipErr == nil && strings.TrimSpace(tip) == strings.TrimSpace(commit) {
		return nil
	}
	return verifyErr
}

// shouldNudgeRefinery reports whether a gt done invocation may wake the
// refinery. Only COMPLETED exits create an MR bead; DEFERRED and ESCALATED
// exits (polecats finishing operational tasks with no code changes) must
// never emit MQ_SUBMIT, or the refinery wakes from backoff to find an empty
// merge queue (gh#3885). The exitType check is defensive: it holds the
// invariant even if a future code path populates mrID outside COMPLETED.
func shouldNudgeRefinery(exitType, mrID string) bool {
	return exitType == ExitCompleted && mrID != ""
}

// setDoneIntentLabel writes a done-intent:<type>:<unix-ts> label on the agent bead
// EARLY in gt done, before push/MR. This allows the Witness to detect polecats that
// crashed mid-gt-done: if the session is dead but done-intent exists, the polecat was
// trying to exit and should be auto-nuked.
//
// Follows the existing idle:N / backoff-until:TIMESTAMP label pattern.
// Non-fatal: if this fails, gt done continues without the safety net.
func setDoneIntentLabel(bd *beads.Beads, agentBeadID, exitType string) {
	if agentBeadID == "" {
		return
	}
	label := fmt.Sprintf("done-intent:%s:%d", exitType, time.Now().Unix())
	if err := bd.Update(agentBeadID, beads.UpdateOptions{
		AddLabels: []string{label},
	}); err != nil {
		// Non-fatal: warn but continue
		fmt.Fprintf(os.Stderr, "Warning: couldn't set done-intent label on %s: %v\n", agentBeadID, err)
	}
}

// clearDoneIntentLabel removes any done-intent:* label from the agent bead.
// Called at the end of updateAgentStateOnDone on clean exit.
// Uses read-modify-write pattern (same as clearAgentBackoffUntil).
func clearDoneIntentLabel(bd *beads.Beads, agentBeadID string) {
	if agentBeadID == "" {
		return
	}
	issue, err := bd.Show(agentBeadID)
	if err != nil {
		return // Agent bead gone, nothing to clear
	}

	var toRemove []string
	for _, label := range issue.Labels {
		if strings.HasPrefix(label, "done-intent:") {
			toRemove = append(toRemove, label)
		}
	}
	if len(toRemove) == 0 {
		return // No done-intent label to clear
	}

	if err := bd.Update(agentBeadID, beads.UpdateOptions{
		RemoveLabels: toRemove,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't clear done-intent label on %s: %v\n", agentBeadID, err)
	}
}

// DoneCheckpoint represents a checkpoint stage in the gt done flow (gt-aufru).
// Checkpoints are stored as labels on the agent bead, enabling resume after
// process interruption (context exhaustion, SIGTERM, etc.).
type DoneCheckpoint string

const (
	CheckpointPushed          DoneCheckpoint = "pushed"
	CheckpointMRCreated       DoneCheckpoint = "mr-created"
	CheckpointWitnessNotified DoneCheckpoint = "witness-notified"
)

// writeDoneCheckpoint writes a checkpoint label on the agent bead.
// Format: done-cp:<stage>:<value>:<unix-ts>
// Non-fatal: if this fails, gt done continues without the checkpoint.
func writeDoneCheckpoint(bd *beads.Beads, agentBeadID string, cp DoneCheckpoint, value string) {
	if agentBeadID == "" {
		return
	}
	label := fmt.Sprintf("done-cp:%s:%s:%d", cp, value, time.Now().Unix())
	if err := bd.Update(agentBeadID, beads.UpdateOptions{
		AddLabels: []string{label},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't write checkpoint %s on %s: %v\n", cp, agentBeadID, err)
	}
}

// readDoneCheckpoints reads all done-cp:* labels from the agent bead.
// Returns a map of checkpoint stage -> value. Empty map if none found.
func readDoneCheckpoints(bd *beads.Beads, agentBeadID string) map[DoneCheckpoint]string {
	checkpoints := make(map[DoneCheckpoint]string)
	if agentBeadID == "" {
		return checkpoints
	}
	issue, err := bd.Show(agentBeadID)
	if err != nil {
		return checkpoints
	}
	for _, label := range issue.Labels {
		if strings.HasPrefix(label, "done-cp:") {
			// Format: done-cp:<stage>:<value>:<ts>
			parts := strings.SplitN(label, ":", 4)
			if len(parts) >= 3 {
				stage := DoneCheckpoint(parts[1])
				value := parts[2]
				checkpoints[stage] = value
			}
		}
	}
	return checkpoints
}

// clearDoneCheckpoints removes all done-cp:* labels from the agent bead.
// Called on clean exit to prevent stale checkpoints from interfering with future runs.
func clearDoneCheckpoints(bd *beads.Beads, agentBeadID string) {
	if agentBeadID == "" {
		return
	}
	issue, err := bd.Show(agentBeadID)
	if err != nil {
		return
	}
	var toRemove []string
	for _, label := range issue.Labels {
		if strings.HasPrefix(label, "done-cp:") {
			toRemove = append(toRemove, label)
		}
	}
	if len(toRemove) == 0 {
		return
	}
	if err := bd.Update(agentBeadID, beads.UpdateOptions{
		RemoveLabels: toRemove,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't clear done checkpoints on %s: %v\n", agentBeadID, err)
	}
}

// updateAgentStateOnDone closes the hooked work bead and reports cleanup status.
// Uses issueID directly to find the hooked bead instead of reading the agent bead's
// hook_bead slot (hq-l6mm5: direct bead tracking).
//
// Clean completions use "done" to prevent dead completed sessions from
// re-entering the idle reuse pool before witness/refinery cleanup finishes.
// Escalated/deferred exits use "stuck" because they need recovery.
//
// Also self-reports cleanup_status for ZFC compliance (#10).
//
// BUG FIX (hq-3xaxy): This function must be resilient to working directory deletion.
// If the polecat's worktree is deleted before gt done finishes, we use env vars as fallback.
// All errors are warnings, not failures - gt done must complete even if bead ops fail.
func updateAgentStateOnDone(cwd, townRoot, exitType, issueID string) error {
	// Get role context - try multiple sources for resilience
	roleInfo, err := GetRoleWithContext(cwd, townRoot)
	if err != nil {
		// Fallback: try to construct role info from environment variables
		// This handles the case where cwd is deleted but env vars are set
		envRole := os.Getenv("GT_ROLE")
		envRig := os.Getenv("GT_RIG")
		envPolecat := os.Getenv("GT_POLECAT")

		if envRole == "" || envRig == "" {
			// Can't determine role, skip agent state update
			style.PrintWarning("could not determine role for agent state update (env: GT_ROLE=%q, GT_RIG=%q)", envRole, envRig)
			return nil
		}

		// Parse role string to get Role type
		parsedRole, _, _ := parseRoleString(envRole)

		roleInfo = RoleInfo{
			Role:     parsedRole,
			Rig:      envRig,
			Polecat:  envPolecat,
			TownRoot: townRoot,
			WorkDir:  cwd,
			Source:   "env-fallback",
		}
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
		style.PrintWarning("no agent bead ID found for %s/%s, skipping agent state update", ctx.Rig, ctx.Polecat)
		return nil
	}

	// Use rig path for bd commands.
	// IMPORTANT: Use the rig's directory (not polecat worktree) so bd commands
	// work even if the polecat worktree is deleted.
	var beadsPath string
	switch ctx.Role {
	case RoleMayor, RoleDeacon:
		beadsPath = townRoot
	default:
		beadsPath = filepath.Join(townRoot, ctx.Rig)
	}
	bd := beads.New(beadsPath)
	// agentBd bypasses prefix routing — agent beads (gt:agent label) live in
	// the town DB regardless of their ID prefix, but the rig-prefix routing
	// would otherwise misroute them to the rig DB and silently fail with
	// "issue not found". See beads.ForAgentBead docstring for details.
	agentBd := bd.ForAgentBead()

	// Find the hooked bead to close. Use issueID directly instead of reading
	// agent bead's hook_bead slot (hq-l6mm5: direct bead tracking).
	hookedBeadID := issueID
	if hookedBeadID == "" {
		// Fallback: query for hooked beads assigned to this agent
		agentID := roleInfo.ActorString()
		if found := findHookedBeadForAgent(bd, agentID); found != "" {
			hookedBeadID = found
		}
	}

	// Workflow step beads (*-wfs-*) are ephemeral formula steps managed by the workflow
	// engine. For these, DEFERRED means "step complete, no code commits" not "work
	// paused for resumption". Close them on DEFERRED so the convoy can advance.
	isWorkflowStep := strings.Contains(hookedBeadID, "-wfs-")

	if hookedBeadID != "" && (exitType != ExitDeferred || isWorkflowStep) {
		// BUG FIX (gt-pftz): Close hooked bead unless already terminal (closed/tombstone).
		// Previously checked hookedBead.Status == StatusHooked, but polecats update
		// their work bead to in_progress during work. The exact-match check caused
		// gt done to skip closing the bead, leaving it as unassigned open work after
		// the hook was cleared — triggering infinite dispatch loops.
		//
		// DEFERRED exits preserve the bead: work is paused, not done. The bead
		// stays open/in_progress so it can be resumed on the next session.
		// Exception: workflow step beads (*-wfs-*) are always closed — see above.
		if hookedBead, err := bd.Show(hookedBeadID); err == nil && !beads.IssueStatus(hookedBead.Status).IsTerminal() {
			// Guard: never close a rig identity bead. Polecats dispatched with the
			// rig bead as their hook (via mol-polecat-work) must not close permanent
			// infrastructure. Skip close and fall through to idle state update.
			if beads.HasLabel(hookedBead, "gt:rig") {
				fmt.Fprintf(os.Stderr, "Note: hooked bead %s is a rig identity bead (gt:rig) — skipping close\n", hookedBeadID)
				goto doneStateUpdate
			}

			if skipReason, fatal := doneReviewOnlyCloseSkipReason(bd, hookedBeadID, hookedBead); skipReason != "" {
				style.PrintWarning("%s", skipReason)
				fmt.Fprintf(os.Stderr, "  The bead will remain open for witness/mayor review.\n")
				notifyDoneCloseSkipped(townRoot, ctx.Rig, detectSender(), hookedBeadID, skipReason)
				if fatal {
					return fmt.Errorf("cannot complete review-only work: %s", skipReason)
				}
				goto doneStateUpdate
			}

			// BUG FIX: Close attached molecule (wisp) BEFORE closing hooked bead.
			// When using formula-on-bead (gt sling formula --on bead), the base bead
			// has attached_molecule pointing to the wisp. Without this fix, gt done
			// only closed the hooked bead, leaving the wisp orphaned.
			// Order matters: wisp closes -> unblocks base bead -> base bead closes.
			attachment := beads.ParseAttachmentFields(hookedBead)
			if attachment != nil && attachment.AttachedMolecule != "" {
				// Close molecule step descendants before closing the wisp root.
				// bd close doesn't cascade — without this, open/in_progress steps
				// from the molecule stay stuck forever after gt done completes.
				// Order: step children -> wisp root -> base bead.
				if n := closeDescendants(bd, attachment.AttachedMolecule); n > 0 {
					fmt.Fprintf(os.Stderr, "Closed %d molecule step(s) for %s\n", n, attachment.AttachedMolecule)
				}

				// Close the wisp root with --force and audit reason.
				// ForceCloseWithReason handles any status (hooked, open, in_progress)
				// and records the reason + session for attribution.
				// Same pattern as gt mol burn/squash (#1879).
				if closeErr := bd.ForceCloseWithReason("done", attachment.AttachedMolecule); closeErr != nil {
					if !errors.Is(closeErr, beads.ErrNotFound) {
						fmt.Fprintf(os.Stderr, "Warning: couldn't close attached molecule %s: %v\n", attachment.AttachedMolecule, closeErr)
						// Don't try to close hookedBeadID - it may still be blocked.
						// But DO clear hooks and update agent state (goto doneStateUpdate)
						// so the polecat isn't stuck in 'working' state (za-o9e).
						goto doneStateUpdate
					}
					// Not found = already burned/deleted by another path, continue
				}
			}

			// Acceptance criteria gate: skip close if criteria are unchecked.
			if unchecked := beads.HasUncheckedCriteria(hookedBead); unchecked > 0 {
				style.PrintWarning("hooked bead %s has %d unchecked acceptance criteria — skipping close", hookedBeadID, unchecked)
				fmt.Fprintf(os.Stderr, "  The bead will remain open for witness/mayor review.\n")
			} else if err := bd.Close(hookedBeadID); err != nil {
				// Non-fatal: warn but continue
				fmt.Fprintf(os.Stderr, "Warning: couldn't close hooked bead %s: %v\n", hookedBeadID, err)
			}
		}
	}

doneStateUpdate:
	// Clear hook_bead on the agent bead (gt-qbh). The hq-l6mm5 refactor made
	// SetHookBead/ClearHookBead no-ops, but the witness still reads the
	// hook_bead field from the agent bead snapshot. If the hooked bead is a
	// wisp that gets reaped, the witness can't verify it was closed and flags
	// the polecat as a zombie. Clearing hook_bead prevents this false positive.
	emptyHook := ""
	if err := agentBd.UpdateAgentDescriptionFields(agentBeadID, beads.AgentFieldUpdates{HookBead: &emptyHook}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't clear hook_bead on %s: %v\n", agentBeadID, err)
	}

	// Purge closed ephemeral beads (wisps) accumulated during this and prior sessions.
	// Without this, closed wisps from mol-polecat-work steps, mol-witness-patrol cycles,
	// etc. accumulate across sessions and pollute bd ready/list output (hq-6161m).
	// Best-effort: failures are non-fatal since the work is already done.
	purgeClosedEphemeralBeads(bd)

	// Completion metadata (exit_type, MR ID, branch) remains on the agent bead
	// for audit purposes and anomaly detection by witness patrol.
	doneState := string(beads.AgentStateDone)
	if exitType != ExitCompleted {
		doneState = "stuck"
	}
	// Use UpdateAgentState to sync both column and description (gt-ulom).
	if err := agentBd.UpdateAgentState(agentBeadID, doneState); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't set agent %s to %s: %v\n", agentBeadID, doneState, err)
	}

	// ZFC #10: Self-report cleanup status
	// Agent observes git state and passes cleanup status via --cleanup-status flag
	if doneCleanupStatus != "" {
		cleanupStatus := parseCleanupStatus(doneCleanupStatus)
		if cleanupStatus != polecat.CleanupUnknown {
			if err := agentBd.UpdateAgentCleanupStatus(agentBeadID, string(cleanupStatus)); err != nil {
				// Non-fatal: don't return — done-intent labels still need clearing (za-o9e)
				fmt.Fprintf(os.Stderr, "Warning: couldn't update agent %s cleanup status: %v\n", agentBeadID, err)
			}
		}
	}

	// Clear done-intent label and checkpoints on clean exit — gt done completed
	// successfully. If we don't reach here (crash/stuck), the Witness uses the
	// lingering labels to detect the zombie and resume from checkpoints.
	clearDoneIntentLabel(agentBd, agentBeadID)
	clearDoneCheckpoints(agentBd, agentBeadID)
	return nil
}

// ensureAgentBeadExists recreates a missing agent bead so done-intent labels,
// checkpoints, and active_mr writes don't silently fail (hq-xu4p). Only
// rig-level agents are handled — town agents (mayor/deacon) are owned by
// gt doctor. Best-effort: failures are warned, never fatal.
func ensureAgentBeadExists(bd *beads.Beads, id string, ctx RoleContext) {
	if id == "" {
		return
	}
	if issue, err := bd.Show(id); err == nil && issue != nil && issue.Status != string(beads.StatusClosed) {
		return // exists and is active
	}

	fields := &beads.AgentFields{Rig: ctx.Rig, AgentState: "idle"}
	var title string
	switch ctx.Role {
	case RolePolecat:
		fields.RoleType = "polecat"
		title = fmt.Sprintf("Polecat worker %s in %s - autonomous worker with persistent identity.", ctx.Polecat, ctx.Rig)
	case RoleWitness:
		fields.RoleType = "witness"
		title = fmt.Sprintf("Witness for %s - monitors polecat health and progress.", ctx.Rig)
	case RoleRefinery:
		fields.RoleType = "refinery"
		title = fmt.Sprintf("Refinery for %s - processes merge queue.", ctx.Rig)
	default:
		return
	}

	if _, err := bd.CreateOrReopenAgentBead(id, title, fields); err != nil {
		style.PrintWarning("agent bead %s missing and recreate failed: %v", id, err)
	} else {
		fmt.Printf("%s Recreated/reopened missing agent bead: %s\n", style.Bold.Render("✓"), id)
	}
}

// isStaleBranchIssue reports whether a branch-derived issue id should be
// overridden by the agent's hooked bead (hq-l0fj stale-branch guard).
// True when both ids exist, they differ, and the branch id is not a subtask
// of the hooked bead (e.g. branch gt-abc.1 under hooked gt-abc is fine).
func isStaleBranchIssue(branchIssue, hookedIssue string) bool {
	if branchIssue == "" || hookedIssue == "" {
		return false
	}
	return branchIssue != hookedIssue && !strings.HasPrefix(branchIssue, hookedIssue+".")
}

// selectAssignedIssue returns the one authoritative assignment to use for
// done attribution. Ambiguous assignment state is deliberately not guessed.
func selectAssignedIssue(branchIssue string, assigned []string) (string, bool) {
	unique := make(map[string]bool, len(assigned))
	for _, id := range assigned {
		if id != "" {
			unique[id] = true
		}
	}
	ids := make([]string, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	if len(ids) == 0 {
		return "", false
	}
	if branchIssue != "" {
		for _, id := range ids {
			if branchIssue == id || strings.HasPrefix(branchIssue, id+".") {
				return "", false
			}
		}
	}
	if len(ids) > 1 {
		return "", true
	}
	return ids[0], false
}

// findAssignedBeadsForAgent queries the same assignment locations as gt hook:
// the current rig, the target rig for rig agents, then town beads. The assigned
// work bead is authoritative; agent-bead hook slots are intentionally ignored.
func findAssignedBeadsForAgent(workDir, agentID string) []string {
	if agentID == "" {
		return nil
	}

	assigned := assignedIssueIDs(queryAssignedBeads(beads.New(workDir), agentID))
	if len(assigned) > 0 {
		return assigned
	}

	townRoot, err := findTownRoot()
	if err != nil || townRoot == "" {
		return nil
	}

	parts := strings.Split(agentID, "/")
	rigName := ""
	if len(parts) > 0 {
		rigName = parts[0]
	}
	if rigName != "" && rigName != "mayor" && rigName != "deacon" {
		rigWorkDir := filepath.Join(townRoot, rigName, "mayor", "rig")
		if rigWorkDir != workDir {
			assigned = assignedIssueIDs(queryAssignedBeads(beads.New(rigWorkDir), agentID))
			if len(assigned) > 0 {
				return assigned
			}
		}
	}

	townBeadsDir := filepath.Join(townRoot, ".beads")
	if _, err := os.Stat(townBeadsDir); err == nil {
		assigned = assignedIssueIDs(queryAssignedBeads(beads.New(townBeadsDir), agentID))
		if len(assigned) > 0 {
			return assigned
		}
	}
	if isTownLevelRole(agentID) {
		return assignedIssueIDs(scanAllRigsForHookedBeads(townRoot, agentID))
	}
	return nil
}

func queryAssignedBeads(bd *beads.Beads, agentID string) []*beads.Issue {
	hooked, err := bd.List(beads.ListOptions{
		Status:   beads.StatusHooked,
		Assignee: agentID,
		Priority: -1,
	})
	if err == nil && len(hooked) > 0 {
		return hooked
	}
	inProgress, err := bd.List(beads.ListOptions{
		Status:   "in_progress",
		Assignee: agentID,
		Priority: -1,
	})
	if err == nil {
		return inProgress
	}
	return nil
}

func assignedIssueIDs(assigned []*beads.Issue) []string {
	ids := make([]string, 0, len(assigned))
	for _, issue := range assigned {
		if issue != nil && issue.ID != "" {
			ids = append(ids, issue.ID)
		}
	}
	return ids
}

// findHookedBeadForAgent queries for the agent's current assignment bead.
// This is the authoritative source for what work a polecat is doing, since the
// work bead itself tracks status and assignee (hq-l6mm5).
//
// Both hooked AND in_progress are checked (hq-xa4z): polecats routinely claim
// their assignment with `bd update --status=in_progress` when starting work,
// which made a hooked-only lookup blind to the active assignment — the stale-
// branch guard and the hook fallback silently no-op'd (same class of bug as
// gt-pftz in the close path). Hooked wins over in_progress when both exist.
// Returns empty string if no assignment bead is found.
func findHookedBeadForAgent(bd *beads.Beads, agentID string) string {
	issueID, _ := selectAssignedIssue("", assignedIssueIDs(queryAssignedBeads(bd, agentID)))
	return issueID
}

// parseCleanupStatus converts a string flag value to a CleanupStatus.
// ZFC: Agent observes git state and passes the appropriate status.
func parseCleanupStatus(s string) polecat.CleanupStatus {
	switch strings.ToLower(s) {
	case "clean":
		return polecat.CleanupClean
	case "uncommitted", "has_uncommitted":
		return polecat.CleanupUncommitted
	case "stash", "has_stash":
		return polecat.CleanupStash
	case "unpushed", "has_unpushed":
		return polecat.CleanupUnpushed
	default:
		return polecat.CleanupUnknown
	}
}

// isPolecatActor checks if a BD_ACTOR value represents a polecat.
// Polecat actors have format: rigname/polecats/polecatname
// Non-polecat actors have formats like: gastown/crew/name, rigname/witness, etc.
func isPolecatActor(actor string) bool {
	parts := strings.Split(actor, "/")
	return len(parts) >= 2 && parts[1] == "polecats"
}

// stripOverlayCLAUDEmd detects and removes Gas Town overlay content from CLAUDE.md
// and CLAUDE.local.md before the branch is pushed. Polecats were committing the
// overlay (which contains polecat lifecycle boilerplate like "Idle Polecat Heresy",
// "gt done" protocol, etc.) into actual repos, overwriting project-specific CLAUDE.md
// content. (gt-p35)
//
// This runs after all commits but before push. If overlay files are detected in
// the branch diff, they are restored (CLAUDE.md) or removed (CLAUDE.local.md)
// and a cleanup commit is created.
//
// Returns true if a cleanup commit was created.
func stripOverlayCLAUDEmd(g *git.Git, defaultBranch, baseRef string) bool {
	// Check which files changed on this branch vs the clean target base.
	changedFiles, err := g.DiffNameOnly(baseRef, "HEAD")
	if err != nil {
		// Can't determine diff — skip silently (push will still work)
		return false
	}

	claudeChanged := false
	claudeLocalChanged := false
	for _, f := range changedFiles {
		switch f {
		case "CLAUDE.md":
			claudeChanged = true
		case "CLAUDE.local.md":
			claudeLocalChanged = true
		}
	}

	if !claudeChanged && !claudeLocalChanged {
		return false // Nothing to strip
	}

	needsCommit := false

	// Handle CLAUDE.md: check if the committed version contains overlay marker
	if claudeChanged {
		// Read current CLAUDE.md from HEAD
		currentContent, showErr := g.ShowFile("HEAD", "CLAUDE.md")
		if showErr == nil && strings.Contains(currentContent, templates.PolecatLifecycleMarker) {
			// Current CLAUDE.md has overlay content — restore from the clean base.
			origContent, origErr := g.ShowFile(baseRef, "CLAUDE.md")
			if origErr != nil {
				// CLAUDE.md didn't exist on the clean base — the overlay created it.
				// Remove it from tracking.
				if rmErr := g.RmCached("CLAUDE.md"); rmErr == nil {
					needsCommit = true
					fmt.Printf("%s Removed overlay CLAUDE.md (did not exist on %s)\n",
						style.Bold.Render("→"), defaultBranch)
				}
			} else {
				// CLAUDE.md existed on the clean base — restore original content
				_ = origContent // Restore via checkout
				if coErr := g.CheckoutFileFromRef(baseRef, "CLAUDE.md"); coErr == nil {
					if addErr := g.Add("CLAUDE.md"); addErr == nil {
						needsCommit = true
						fmt.Printf("%s Restored original CLAUDE.md (stripped Gas Town overlay)\n",
							style.Bold.Render("→"))
					}
				}
			}
		}
	}

	// Handle CLAUDE.local.md: always remove from commits (it's a runtime artifact)
	if claudeLocalChanged {
		if rmErr := g.RmCached("CLAUDE.local.md"); rmErr == nil {
			needsCommit = true
			fmt.Printf("%s Removed CLAUDE.local.md from branch (Gas Town overlay)\n",
				style.Bold.Render("→"))
		}
	}

	if !needsCommit {
		return false
	}

	// Create cleanup commit
	if commitErr := g.Commit("chore: strip Gas Town overlay from CLAUDE.md (gt-p35)"); commitErr != nil {
		style.PrintWarning("failed to create overlay cleanup commit: %v", commitErr)
		return false
	}

	fmt.Printf("%s Created cleanup commit to remove Gas Town overlay files\n",
		style.Bold.Render("✓"))
	return true
}

// purgeClosedEphemeralBeads removes closed ephemeral beads (wisps) that accumulated
// during this and prior sessions. Polecat/witness sessions create mol-polecat-work
// steps, mol-witness-patrol cycles, etc. as wisps. These get closed during normal
// operation but are never deleted, accumulating hundreds of rows that pollute
// bd ready/list output. (hq-6161m)
//
// Best-effort: errors are logged but don't block gt done completion.
func purgeClosedEphemeralBeads(bd *beads.Beads) {
	out, err := bd.Run("purge", "--force", "--quiet")
	if err != nil {
		// Non-fatal: purge failure shouldn't block session completion
		fmt.Fprintf(os.Stderr, "Warning: wisp purge failed: %v\n", err)
		return
	}
	// bd purge --force --quiet outputs the count of purged beads
	outStr := strings.TrimSpace(string(out))
	if outStr != "" && outStr != "0" {
		fmt.Fprintf(os.Stderr, "Purged closed ephemeral beads: %s\n", outStr)
	}
}
