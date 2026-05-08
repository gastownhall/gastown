package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	"github.com/steveyegge/gastown/internal/townlog"
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
4. Syncs worktree to main and transitions polecat to IDLE
   (sandbox preserved, session stays alive for reuse)

Exit statuses:
  COMPLETED      - Work done, MR submitted (default)
  ESCALATED      - Hit blocker, needs human intervention
  DEFERRED       - Work paused, issue still open

Examples:
  gt done                              # Submit branch, notify COMPLETED, transition to IDLE
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
		targetBranch = strings.TrimPrefix(explicitTarget, "origin/")
	}

	return "origin/" + targetBranch
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

	// Persistent polecat model (gt-hdf8): sessions stay alive after gt done.
	// No deferred session kill — the polecat transitions to IDLE with sandbox
	// preserved. The Witness handles any cleanup if the polecat gets stuck.

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
				// Unstage runtime/ephemeral directories (mirrors checkpoint_dog exclusions).
				for _, dir := range []string{".beads/", ".claude/", ".runtime/", "__pycache__/"} {
					_ = g.ResetFiles(dir)
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
	polecatName := ""
	if parts := strings.Split(sender, "/"); len(parts) >= 2 {
		polecatName = parts[len(parts)-1]
	}

	// Get agent bead ID for cross-referencing
	var agentBeadID string
	if roleInfo, err := GetRoleWithContext(cwd, townRoot); err == nil {
		ctx := RoleContext{
			Role:     roleInfo.Role,
			Rig:      roleInfo.Rig,
			Polecat:  roleInfo.Polecat,
			TownRoot: townRoot,
			WorkDir:  cwd,
		}
		agentBeadID = getAgentBeadID(ctx)

		// Persistent polecat model (gt-hdf8): no deferred session kill.
		// Sessions stay alive after gt done — polecat transitions to IDLE.
	}

	// If issue ID not set by flag or branch name, query for hooked beads
	// assigned to this agent. This replaces reading agent_bead.hook_bead
	// (hq-l6mm5: direct bead tracking instead of agent bead slot).
	if issueID == "" && sender != "" {
		bd := beads.New(cwd)
		if hookIssue := findHookedBeadForAgent(bd, sender); hookIssue != "" {
			issueID = hookIssue
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
		bd := beads.New(cwd)
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
		// Runtime artifacts (.claude/, .beads/, .runtime/, __pycache__/) are
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

		// Check if branch has commits ahead of origin/default
		// If not, work may have been pushed directly to main - that's fine, just skip MR
		originDefault := "origin/" + defaultBranch
		aheadCount, err := g.CommitsAhead(originDefault, "HEAD")
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
		if issueID != "" {
			noMergeBd := beads.New(cwd)
			if noMergeIssue, showErr := noMergeBd.Show(issueID); showErr == nil {
				if af := beads.ParseAttachmentFields(noMergeIssue); af != nil && (af.NoMerge || af.ReviewOnly) {
					isNoMergeTask = true
				}
			}
		}

		// If no commits ahead, work was likely pushed directly to main (or already merged)
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
						originDefault)
				}
			}

			// Non-polecat (crew/mayor), polecat with --cleanup-status=clean
			// (report-only tasks like audits/reviews), or no_merge polecat
			// (non-code tasks like email/research per GH#2496):
			// zero commits is valid.
			fmt.Printf("%s Branch has no commits ahead of %s\n", style.Bold.Render("→"), originDefault)
			fmt.Printf("  Work was likely pushed directly to main or already merged.\n")
			fmt.Printf("  Skipping MR creation - completing without merge request.\n\n")

			// G15 fix: Close the base issue when completing with no MR.
			// Without this, no-op polecats (bug already fixed) leave issues stuck
			// in HOOKED state with assignee pointing to the nuked polecat.
			// Normally the Refinery closes after merge, but with no MR, nothing
			// would ever close the issue.
			if issueID != "" {
				bd := beads.New(cwd)

				// Acceptance criteria gate: check for unchecked criteria before closing.
				// If criteria exist and are unchecked, warn and skip close — the bead stays
				// open for witness/mayor to handle.
				skipClose := false
				if issue, err := bd.Show(issueID); err == nil {
					if unchecked := beads.HasUncheckedCriteria(issue); unchecked > 0 {
						style.PrintWarning("issue %s has %d unchecked acceptance criteria — skipping close", issueID, unchecked)
						fmt.Printf("  The bead will remain open for witness/mayor review.\n")
						skipClose = true
					}
				}

				if !skipClose {
					closeReason := "Completed with no code changes (already fixed or pushed directly to main)"
					noMRCommitSHA, _ := g.Rev("HEAD")
					if doneSkipVerify {
						noteVerifiedPushSkipped(cwd, issueID, defaultBranch, noMRCommitSHA, "--skip-verify on no-MR close")
						if noMRCommitSHA != "" {
							closeReason = fmt.Sprintf("%s\nskip_verify: true\ntarget_branch: %s\ncommit_sha: %s", closeReason, defaultBranch, noMRCommitSHA)
						}
					} else if !isNoMergeTask {
						if verifyErr := g.VerifyPushedCommit("origin", defaultBranch, noMRCommitSHA); verifyErr != nil {
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

		// Branch contamination preflight: check if branch is significantly behind
		// the effective target branch, which indicates the branch may contain stale merge-base
		// artifacts that will pollute the PR diff. (GH#2220)
		//
		// gh#3400: Refresh remote tracking refs first so contamination check (and
		// the auto-rebase below) sees the current state of origin. Without this,
		// the local view of origin/<base> may be stale and we'd skip a rebase that
		// is actually needed.
		contaminationBase := doneContaminationBaseRef(defaultBranch, doneTarget)
		if fetchErr := g.Fetch("origin"); fetchErr != nil {
			style.PrintWarning("could not fetch origin before contamination check: %v (proceeding with local refs)", fetchErr)
		}
		contam, err := g.CheckBranchContamination(contaminationBase)
		if err == nil && contam.Behind > 0 {
			const warnThreshold = 50
			const blockThreshold = 200
			if contam.Behind >= blockThreshold {
				return fmt.Errorf("branch contamination: %d commits behind %s (threshold: %d)\n"+
					"The branch is severely stale and will include unrelated changes in the PR.\n"+
					"Fix: git fetch origin && git rebase %s",
					contam.Behind, contaminationBase, blockThreshold, contaminationBase)
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
				aheadCount, _ = g.CommitsAhead("origin/"+defaultBranch, "HEAD")
			} else if skipReason != "" {
				style.PrintWarning("branch is %d commits behind %s but %s; skipping auto-rebase", contam.Behind, contaminationBase, skipReason)
			}
		}

		// Strip Gas Town overlay from CLAUDE.md / CLAUDE.local.md (gt-p35).
		// Polecats commit the overlay (polecat lifecycle boilerplate) into repos,
		// overwriting project-specific CLAUDE.md content. Detect and revert before push.
		if stripped := stripOverlayCLAUDEmd(g, defaultBranch); stripped {
			// Recalculate commits ahead since we added a cleanup commit
			aheadCount, _ = g.CommitsAhead("origin/"+defaultBranch, "HEAD")
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
			pushSubmoduleChanges(g, defaultBranch)
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
			} else if verifyErr := g.VerifyPushedCommit("origin", defaultBranch, directCommitSHA); verifyErr != nil {
				pushFailed = true
				errMsg := verifyErr.Error()
				doneErrors = append(doneErrors, errMsg)
				noteVerifiedPushFailure(cwd, issueID, defaultBranch, directCommitSHA, verifyErr)
				style.PrintWarning("%s\nDirect merge pushed but remote verification failed. Source bead will remain in progress.", errMsg)
				goto notifyWitness
			}
			fmt.Printf("%s Branch pushed directly to %s\n", style.Bold.Render("✓"), defaultBranch)

			// Close the base issue — no MR/refinery will close it
			if issueID != "" {
				directBd := beads.New(cwd)
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
		pushSubmoduleChanges(g, defaultBranch)

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
		if doneCleanupStatus == "unpushed" {
			doneCleanupStatus = "clean"
		}

		// Write push checkpoint for resume (gt-aufru)
		if agentBeadID != "" {
			cpBd := beads.New(cwd)
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
					if diffStat, diffErr := g.DiffStat(defaultBranch + "..." + branch); diffErr == nil && diffStat != "" {
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
				} else if verifyErr := g.VerifyPushedCommit("origin", defaultBranch, lateDirectCommitSHA); verifyErr != nil {
					pushFailed = true
					errMsg := verifyErr.Error()
					doneErrors = append(doneErrors, errMsg)
					noteVerifiedPushFailure(cwd, issueID, defaultBranch, lateDirectCommitSHA, verifyErr)
					style.PrintWarning("%s\nLate direct merge pushed but remote verification failed. Source bead will remain in progress.", errMsg)
					goto notifyWitness
				}
				fmt.Printf("%s Branch pushed directly to %s\n", style.Bold.Render("✓"), defaultBranch)

				// Close the issue directly — refinery won't process it.
				if issueID != "" {
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

				goto notifyWitness
			}
		}

		// Determine target branch for the MR.
		// Priority: explicit --target flag > formula_vars base_branch > integration branch auto-detect > rig default.
		target := defaultBranch

		// 1. Explicit --target flag (highest priority — polecat knows its base branch).
		// This is the most reliable path: the formula passes {{base_branch}} directly,
		// avoiding any dependency on bd.Show() or Dolt availability.
		if doneTarget != "" && doneTarget != defaultBranch {
			target = doneTarget
			fmt.Printf("  Target branch: %s (from --target flag)\n", target)
		}

		// 2. Check for --base-branch override in formula vars (stored on bead at sling time).
		// Fallback for polecats dispatched before --target flag existed, or when
		// the formula doesn't pass --target explicitly.
		if target == defaultBranch && sourceIssueForNoMerge != nil {
			if af := beads.ParseAttachmentFields(sourceIssueForNoMerge); af != nil {
				if bb := extractFormulaVar(af.FormulaVars, "base_branch"); bb != "" && bb != defaultBranch {
					target = bb
					fmt.Printf("  Target branch override: %s (from formula_vars)\n", target)
				}
			}
		} else if target == defaultBranch && sourceIssueForNoMerge == nil && issueID != "" {
			// sourceIssueForNoMerge is nil — bd.Show(issueID) failed earlier.
			// This is the silent failure path that caused 150+ procedure beads to
			// target main instead of feat/contract-review-procedure.
			style.PrintWarning("could not load source issue %s for target branch detection (Dolt/beads lookup failed) — using default branch %s", issueID, defaultBranch)
		}

		// 3. Auto-detect integration branch from epic hierarchy (if enabled).
		// Only overrides if no explicit target was set above.
		if target == defaultBranch {
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
				// Capture current origin/target HEAD as the verified base.
				// The polecat rebased onto this SHA before running gates.
				if verifiedBase, baseErr := g.Rev("origin/" + target); baseErr == nil {
					description += fmt.Sprintf("\npre_verified_base: %s", verifiedBase)
				} else {
					style.PrintWarning("could not resolve origin/%s for pre-verified base: %v (pre-verification data incomplete)", target, baseErr)
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

			// Update agent bead with active_mr reference (for traceability)
			if agentBeadID != "" {
				if err := bd.UpdateAgentActiveMR(agentBeadID, mrID); err != nil {
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
			cpBd := beads.New(cwd)
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
	if mrID != "" {
		nudgeRefinery(rigName, "MERGE_READY received - check inbox for pending work")
	}

	// Write completion metadata to agent bead for audit trail.
	// Self-managed completion (gt-1qlg): metadata is retained for anomaly
	// detection and crash recovery by witness patrol, but the witness no
	// longer processes routine completions from these fields.
	fmt.Printf("\nNotifying Witness...\n")
	if agentBeadID != "" {
		completionBd := beads.New(cwd)
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

	// Nudge witness via tmux (observability, not critical path).
	// Self-managed completion (gt-1qlg): witness no longer processes routine completions.
	// The nudge is kept for observability — witness logs the event but doesn't
	// need to act on it. Nudges are free (no Dolt commit).
	nudgeWitness(rigName, fmt.Sprintf("POLECAT_DONE %s exit=%s", polecatName, exitType))
	fmt.Printf("%s Witness notified of %s (via nudge)\n", style.Bold.Render("✓"), exitType)

	// Write witness notification checkpoint for resume (gt-aufru)
	if agentBeadID != "" {
		cpBd := beads.New(cwd)
		writeDoneCheckpoint(cpBd, agentBeadID, CheckpointWitnessNotified, "ok")
	}

	// Log done event (townlog and activity feed)
	if err := LogDone(townRoot, sender, issueID); err != nil {
		style.PrintWarning("could not log done event: %v", err)
	}
	if err := events.LogFeed(events.TypeDone, sender, events.DonePayload(issueID, branch)); err != nil {
		style.PrintWarning("could not log feed event: %v", err)
	}

	// Update agent bead state (ZFC: self-report completion)
	updateAgentStateOnDone(cwd, townRoot, exitType, issueID)

	// Persistent polecat model (gt-hdf8): polecats transition to IDLE after completion.
	// Session stays alive, sandbox preserved, worktree synced to main for reuse.
	// "done means idle" - not "done means dead".
	isPolecat := false
	if roleInfo, err := GetRoleWithContext(cwd, townRoot); err == nil && roleInfo.Role == RolePolecat {
		isPolecat = true

		fmt.Printf("%s Sandbox preserved for reuse (persistent polecat)\n", style.Bold.Render("✓"))

		if pushFailed || mrFailed {
			fmt.Printf("%s Work needs recovery (push or MR failed) — session preserved\n", style.Bold.Render("⚠"))
		}

		// Sync worktree to main so the polecat is ready for new assignments.
		// Phase 3 of persistent-polecat-pool: DONE→IDLE syncs to main and deletes old branch.
		// Non-fatal: if sync fails, the polecat is still IDLE and the Witness
		// or next gt sling can handle the branch state.
		//
		// GUARD (gt-pvx): Refuse to sync if uncommitted changes remain.
		// If the auto-commit safety net above failed (git add/commit error),
		// switching branches would discard the work. Better to leave the worktree
		// dirty on the feature branch so work can be recovered.
		syncSafe := true
		if cwdAvailable {
			if ws, wsErr := g.CheckUncommittedWork(); wsErr == nil && ws.HasUncommittedChanges && !ws.CleanExcludingRuntime() {
				syncSafe = false
				style.PrintWarning("uncommitted changes still present — skipping worktree sync to preserve work")
				fmt.Printf("  Files: %s\n", ws.String())
			}
		}
		if cwdAvailable && !pushFailed && syncSafe {
			// Remember the old branch so we can delete it after switching
			oldBranch := branch

			fmt.Printf("%s Syncing worktree to %s...\n", style.Bold.Render("→"), defaultBranch)
			if err := g.Checkout(defaultBranch); err != nil {
				style.PrintWarning("could not checkout %s: %v (worktree stays on feature branch)", defaultBranch, err)
			} else if err := g.Pull("origin", defaultBranch); err != nil {
				style.PrintWarning("could not pull %s: %v (worktree on %s but may be stale)", defaultBranch, defaultBranch, err)
			} else {
				fmt.Printf("%s Worktree synced to %s\n", style.Bold.Render("✓"), defaultBranch)
			}

			// Delete the old polecat branch (non-fatal: cleanup only).
			// This prevents stale branch accumulation from persistent polecats.
			if oldBranch != "" && oldBranch != defaultBranch && oldBranch != "master" {
				if err := g.DeleteBranch(oldBranch, true); err != nil {
					style.PrintWarning("could not delete old branch %s: %v", oldBranch, err)
				} else {
					fmt.Printf("%s Deleted old branch %s\n", style.Bold.Render("✓"), oldBranch)
				}
			}
		}

		fmt.Printf("%s Polecat transitioned to IDLE — ready for new work\n", style.Bold.Render("✓"))
	}

	fmt.Println()
	if !isPolecat {
		fmt.Printf("%s Session exiting\n", style.Bold.Render("→"))
		fmt.Printf("  Witness will handle cleanup.\n")
	}

	// Self-terminate AFTER all cleanup is complete (opt-in via config).
	// When enabled, polecats kill their session after gt done finishes
	// instead of transitioning to IDLE. This gives fresh context windows
	// per task, reduces token waste, and eliminates stale state bugs.
	// Must be the LAST thing gt done does — everything above must complete first.
	if isPolecat {
		daemonCfg := config.LoadOperationalConfig(townRoot).GetDaemonConfig()
		if daemonCfg.PolecatSelfTerminate != nil && *daemonCfg.PolecatSelfTerminate {
			fmt.Printf("%s Self-terminating session (polecat_self_terminate=true)\n", style.Bold.Render("✓"))
			sessionName := session.PolecatSessionName(session.PrefixFor(rigName), polecatName)
			go func() {
				time.Sleep(3 * time.Second)
				t := tmux.NewTmux()
				_ = t.KillSessionWithProcesses(sessionName)
			}()
		}
	}

	return nil
}

// pushSubmoduleChanges detects submodules modified between origin/defaultBranch
// and HEAD, and pushes each submodule's new commit to its remote before the
// parent repo push. This prevents the parent's submodule pointer from
// referencing commits that don't exist on the submodule's remote (gt-dzs).
func pushSubmoduleChanges(g *git.Git, defaultBranch string) {
	subChanges, err := g.SubmoduleChanges("origin/"+defaultBranch, "HEAD")
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
// Per gt-zecmc: observable states ("done", "idle") removed - use tmux to discover.
// Non-observable states ("stuck", "awaiting-gate") are still set since they represent
// intentional agent decisions that can't be observed from tmux.
//
// Also self-reports cleanup_status for ZFC compliance (#10).
//
// BUG FIX (hq-3xaxy): This function must be resilient to working directory deletion.
// If the polecat's worktree is deleted before gt done finishes, we use env vars as fallback.
// All errors are warnings, not failures - gt done must complete even if bead ops fail.
func updateAgentStateOnDone(cwd, townRoot, exitType, issueID string) {
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
			return
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
		return
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

	if hookedBeadID != "" && exitType != ExitDeferred {
		// BUG FIX (gt-pftz): Close hooked bead unless already terminal (closed/tombstone).
		// Previously checked hookedBead.Status == StatusHooked, but polecats update
		// their work bead to in_progress during work. The exact-match check caused
		// gt done to skip closing the bead, leaving it as unassigned open work after
		// the hook was cleared — triggering infinite dispatch loops.
		//
		// DEFERRED exits preserve the bead: work is paused, not done. The bead
		// stays open/in_progress so it can be resumed on the next session.
		if hookedBead, err := bd.Show(hookedBeadID); err == nil && !beads.IssueStatus(hookedBead.Status).IsTerminal() {
			// Guard: never close a rig identity bead. Polecats dispatched with the
			// rig bead as their hook (via mol-polecat-work) must not close permanent
			// infrastructure. Skip close and fall through to idle state update.
			if beads.HasLabel(hookedBead, "gt:rig") {
				fmt.Fprintf(os.Stderr, "Note: hooked bead %s is a rig identity bead (gt:rig) — skipping close\n", hookedBeadID)
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
						// Don't try to close hookedBeadID - it may still be blocked
						// The Witness will clean up orphaned state
						return
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
	if err := bd.UpdateAgentDescriptionFields(agentBeadID, beads.AgentFieldUpdates{HookBead: &emptyHook}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't clear hook_bead on %s: %v\n", agentBeadID, err)
	}

	// Purge closed ephemeral beads (wisps) accumulated during this and prior sessions.
	// Without this, closed wisps from mol-polecat-work steps, mol-witness-patrol cycles,
	// etc. accumulate across sessions and pollute bd ready/list output (hq-6161m).
	// Best-effort: failures are non-fatal since the work is already done.
	purgeClosedEphemeralBeads(bd)

	// Self-managed completion (gt-1qlg, polecat-self-managed-completion.md Phase 2):
	// Polecat sets agent_state=idle directly, skipping the intermediate "done" state.
	// The witness is no longer in the critical path for routine completions.
	// Completion metadata (exit_type, MR ID, branch) remains on the agent bead
	// for audit purposes and anomaly detection by witness patrol.
	// Exception: ESCALATED exits use "stuck" — the polecat needs help.
	doneState := "idle"
	if exitType == ExitEscalated {
		doneState = "stuck"
	}
	// Use UpdateAgentState to sync both column and description (gt-ulom).
	if err := bd.UpdateAgentState(agentBeadID, doneState); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't set agent %s to %s: %v\n", agentBeadID, doneState, err)
	}

	// ZFC #10: Self-report cleanup status
	// Agent observes git state and passes cleanup status via --cleanup-status flag
	if doneCleanupStatus != "" {
		cleanupStatus := parseCleanupStatus(doneCleanupStatus)
		if cleanupStatus != polecat.CleanupUnknown {
			if err := bd.UpdateAgentCleanupStatus(agentBeadID, string(cleanupStatus)); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: couldn't update agent %s cleanup status: %v\n", agentBeadID, err)
				return
			}
		}
	}

	// Clear done-intent label and checkpoints on clean exit — gt done completed
	// successfully. If we don't reach here (crash/stuck), the Witness uses the
	// lingering labels to detect the zombie and resume from checkpoints.
	clearDoneIntentLabel(bd, agentBeadID)
	clearDoneCheckpoints(bd, agentBeadID)
}

// findHookedBeadForAgent queries for beads with status=hooked assigned to this agent.
// This is the authoritative source for what work a polecat is doing, since the
// work bead itself tracks status and assignee (hq-l6mm5).
// Returns empty string if no hooked bead is found.
func findHookedBeadForAgent(bd *beads.Beads, agentID string) string {
	if agentID == "" {
		return ""
	}
	hookedBeads, err := bd.List(beads.ListOptions{
		Status:   beads.StatusHooked,
		Assignee: agentID,
		Priority: -1,
	})
	if err != nil || len(hookedBeads) == 0 {
		return ""
	}
	return hookedBeads[0].ID
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

// selfNukePolecat deletes this polecat's worktree.
// DEPRECATED (gt-4ac): No longer called from gt done. Polecats now go idle
// instead of self-nuking. Kept for explicit nuke scenarios.
// This is safe because:
// 1. Work has been pushed to origin (verified below)
// 2. We're about to exit anyway
// 3. Unix allows deleting directories while processes run in them
func selfNukePolecat(roleInfo RoleInfo, _ string) error {
	if roleInfo.Role != RolePolecat || roleInfo.Polecat == "" || roleInfo.Rig == "" {
		return fmt.Errorf("not a polecat: role=%s, polecat=%s, rig=%s", roleInfo.Role, roleInfo.Polecat, roleInfo.Rig)
	}

	// Get polecat manager using existing helper
	mgr, _, err := getPolecatManager(roleInfo.Rig)
	if err != nil {
		return fmt.Errorf("getting polecat manager: %w", err)
	}

	// Verify branch actually exists on a remote before nuking local copy.
	// If push didn't land (no remote, auth failure, etc.), preserve worktree
	// so Witness/Refinery can still access the branch.
	clonePath := mgr.ClonePath(roleInfo.Polecat)
	polecatGit := git.NewGit(clonePath)
	remotes, err := polecatGit.Remotes()
	if err != nil || len(remotes) == 0 {
		return fmt.Errorf("no git remotes configured — preserving worktree to prevent data loss")
	}
	branchName, err := polecatGit.CurrentBranch()
	if err != nil {
		return fmt.Errorf("cannot determine current branch — preserving worktree: %w", err)
	}
	pushed := false
	for _, remote := range remotes {
		exists, err := polecatGit.RemoteBranchExists(remote, branchName)
		if err == nil && exists {
			pushed = true
			break
		}
	}
	if !pushed {
		return fmt.Errorf("branch %s not found on any remote — preserving worktree", branchName)
	}

	// Use nuclear=true since we verified the branch is pushed
	// selfNuke=true because polecat is deleting its own worktree from inside it
	if err := mgr.RemoveWithOptions(roleInfo.Polecat, true, true, true); err != nil {
		return fmt.Errorf("removing worktree: %w", err)
	}

	return nil
}

// isPolecatActor checks if a BD_ACTOR value represents a polecat.
// Polecat actors have format: rigname/polecats/polecatname
// Non-polecat actors have formats like: gastown/crew/name, rigname/witness, etc.
func isPolecatActor(actor string) bool {
	parts := strings.Split(actor, "/")
	return len(parts) >= 2 && parts[1] == "polecats"
}

// selfKillSession terminates the polecat's own tmux session after logging the event.
// DEPRECATED (gt-hdf8): No longer called from gt done. Polecats now transition to
// IDLE with session preserved instead of self-killing. Kept for explicit kill scenarios
// (e.g., Witness-directed termination).
//
// The polecat determines its session from environment variables:
// - GT_RIG: the rig name
// - GT_POLECAT: the polecat name
// Session name format: gt-<rig>-<polecat>
func selfKillSession(townRoot string, roleInfo RoleInfo) error {
	// Get session info from environment (set at session startup)
	rigName := os.Getenv("GT_RIG")
	polecatName := os.Getenv("GT_POLECAT")

	// Fall back to roleInfo if env vars not set (shouldn't happen but be safe)
	if rigName == "" {
		rigName = roleInfo.Rig
	}
	if polecatName == "" {
		polecatName = roleInfo.Polecat
	}

	if rigName == "" || polecatName == "" {
		return fmt.Errorf("cannot determine session: rig=%q, polecat=%q", rigName, polecatName)
	}

	sessionName := session.PolecatSessionName(session.PrefixFor(rigName), polecatName)
	agentID := fmt.Sprintf("%s/polecats/%s", rigName, polecatName)

	// Log to townlog (human-readable audit log)
	if townRoot != "" {
		logger := townlog.NewLogger(townRoot)
		_ = logger.Log(townlog.EventKill, agentID, "self-clean: done means idle")
	}

	// Log to events (JSON audit log with structured payload)
	_ = events.LogFeed(events.TypeSessionDeath, agentID,
		events.SessionDeathPayload(sessionName, agentID, "self-clean: done means idle", "gt done"))

	// Kill our own tmux session with proper process cleanup
	// This will terminate Claude and all child processes, completing the self-cleaning cycle.
	// We use KillSessionWithProcessesExcluding to ensure no orphaned processes are left behind,
	// while excluding our own PID to avoid killing ourselves before cleanup completes.
	// The tmux kill-session at the end will terminate us along with the session.
	t := tmux.NewTmux()
	myPID := strconv.Itoa(os.Getpid())
	if err := t.KillSessionWithProcessesExcluding(sessionName, []string{myPID}); err != nil {
		return fmt.Errorf("killing session %s: %w", sessionName, err)
	}

	return nil
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
func stripOverlayCLAUDEmd(g *git.Git, defaultBranch string) bool {
	originRef := "origin/" + defaultBranch

	// Check which files changed on this branch vs origin/main
	changedFiles, err := g.DiffNameOnly(originRef, "HEAD")
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
			// Current CLAUDE.md has overlay content — restore from origin
			origContent, origErr := g.ShowFile(originRef, "CLAUDE.md")
			if origErr != nil {
				// CLAUDE.md didn't exist on origin/main — the overlay created it.
				// Remove it from tracking.
				if rmErr := g.RmCached("CLAUDE.md"); rmErr == nil {
					needsCommit = true
					fmt.Printf("%s Removed overlay CLAUDE.md (did not exist on %s)\n",
						style.Bold.Render("→"), defaultBranch)
				}
			} else {
				// CLAUDE.md existed on origin — restore original content
				_ = origContent // Restore via checkout
				if coErr := g.CheckoutFileFromRef(originRef, "CLAUDE.md"); coErr == nil {
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
