package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// branchInfo holds parsed branch information.
type branchInfo struct {
	Branch string // Full branch name
	Issue  string // Issue ID extracted from branch
	Worker string // Worker name (polecat name)
}

// issuePattern matches issue IDs in branch names (e.g., "gt-xyz" or "gt-abc.1")
var issuePattern = regexp.MustCompile(`([a-z]+-[a-z0-9]+(?:\.[0-9]+)?)`)

// parseBranchName extracts issue ID and worker from a branch name.
// Supports formats:
//   - polecat/<worker>/<issue>[+|@]<suffix>  → issue=<issue>, worker=<worker>
//   - polecat/<worker>/<issue>  → issue=<issue>, worker=<worker>
//   - polecat/<worker>-<suffix>  → issue="", worker=<worker>
//   - <issue>                   → issue=<issue>, worker=""
func parseBranchName(branch string) branchInfo {
	info := branchInfo{Branch: branch}

	if meta, ok := polecat.ParseBranchName(branch); ok {
		info.Worker = meta.Polecat
		info.Issue = meta.Issue
		return info
	}
	if strings.HasPrefix(branch, "polecat/") {
		return info
	}

	// Try to find an issue ID pattern in the branch name
	// Common patterns: prefix-xxx, prefix-xxx.n (subtask)
	if matches := issuePattern.FindStringSubmatch(branch); len(matches) > 1 {
		info.Issue = matches[1]
	}

	return info
}

func runMqSubmit(cmd *cobra.Command, args []string) error {
	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Find current rig
	rigName, _, err := findCurrentRig(townRoot)
	if err != nil {
		return err
	}

	// Initialize git for the current directory
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// When gt is invoked via shell alias (cd ~/gt && gt), cwd is the town
	// root, not the polecat's worktree. Reconstruct actual path.
	if cwd == townRoot {
		// Gate polecat cwd switch on GT_ROLE: coordinators may have stale GT_POLECAT.
		isPolecat := false
		if role := os.Getenv("GT_ROLE"); role != "" {
			parsedRole, _, _ := parseRoleString(role)
			isPolecat = parsedRole == RolePolecat
		} else {
			isPolecat = os.Getenv("GT_POLECAT") != ""
		}
		if polecatName := os.Getenv("GT_POLECAT"); polecatName != "" && rigName != "" && isPolecat {
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

	g := git.NewGit(cwd)

	// Get current branch
	branch := mqSubmitBranch
	if branch == "" {
		branch, err = g.CurrentBranch()
		if err != nil {
			return fmt.Errorf("getting current branch: %w", err)
		}
	}

	// Get configured default branch for this rig
	defaultBranch := "main" // fallback
	if rigCfg, err := rig.LoadRigConfig(filepath.Join(townRoot, rigName)); err == nil && rigCfg.DefaultBranch != "" {
		defaultBranch = rigCfg.DefaultBranch
	}

	if branch == defaultBranch || branch == "master" {
		return fmt.Errorf("cannot submit %s/master branch to merge queue", defaultBranch)
	}

	// Parse branch info
	info := parseBranchName(branch)

	// Override with explicit flags
	issueID := mqSubmitIssue
	if issueID == "" {
		issueID = info.Issue
	}
	worker := info.Worker

	if issueID == "" {
		return fmt.Errorf("cannot determine source issue from branch '%s'; use --issue to specify", branch)
	}

	// Initialize current-rig beads for merge-request queue operations, then
	// resolve the source through town-level routing for source-owned operations.
	bd := beads.New(cwd)
	sourceInfo, err := resolveSubmitSourceIssue(cwd, issueID)
	if err != nil {
		return fmt.Errorf("source issue validation failed: %w", err)
	}
	sourceBD := sourceInfo.BD
	sourceIssue := sourceInfo.Issue

	// Determine target branch
	// Priority: explicit --epic > formula_vars base_branch > integration branch auto-detect > rig default.
	target := defaultBranch
	if mqSubmitEpic != "" {
		// Explicit --epic flag: read stored branch name, fall back to template
		rigPath := filepath.Join(townRoot, rigName)
		target = resolveIntegrationBranchName(sourceBD, rigPath, mqSubmitEpic)
	} else {
		// Check for explicit --base-branch override in formula vars on the source issue.
		// When gt sling dispatches with --base-branch, the value is persisted in
		// the bead's formula_vars field. Without this check, MRs created via
		// gt mq submit always target the rig's default branch (usually main),
		// even when the polecat was working against a feature branch.
		if af := beads.ParseAttachmentFields(sourceIssue); af != nil {
			if bb := extractFormulaVar(af.FormulaVars, "base_branch"); bb != "" && bb != defaultBranch {
				target = bb
				fmt.Printf("  Target branch override: %s (from formula_vars)\n", target)
			}
		}

		// Auto-detect: check if source issue has a parent epic with an integration branch
		// Only if no explicit base_branch was found above
		if target == defaultBranch {
			refineryEnabled := true
			rigPath := filepath.Join(townRoot, rigName)
			settingsPath := filepath.Join(rigPath, "settings", "config.json")
			if settings, err := config.LoadRigSettings(settingsPath); err == nil && settings.MergeQueue != nil {
				refineryEnabled = settings.MergeQueue.IsRefineryIntegrationEnabled()
			}
			if refineryEnabled {
				autoTarget, err := beads.DetectIntegrationBranch(sourceBD, g, issueID)
				if err != nil {
					// Non-fatal: log and continue with default branch as target
					fmt.Printf("  %s\n", style.Dim.Render(fmt.Sprintf("(note: %v)", err)))
				} else if autoTarget != "" {
					target = autoTarget
				}
			}
		}
	}

	// Get source issue for priority inheritance and dependency check
	var priority int
	if mqSubmitPriority >= 0 {
		priority = mqSubmitPriority
	} else {
		priority = sourceIssue.Priority
	}

	// Enforce molecule step dependencies before allowing submit.
	// If the source issue has an attached molecule, verify that prerequisite
	// steps are complete. This prevents polecats from skipping steps like
	// self-review, build-check, or state-update.
	if !mqSubmitSkipDeps && !mqSubmitResubmit && sourceIssue != nil {
		if err := checkMoleculeStepDeps(sourceBD, sourceIssue); err != nil {
			return err
		}
	}

	// GH#3032/wa-skj: resolve the submitted branch tip for MR dedup and
	// verification. With --branch this can differ from the checked-out HEAD.
	commitSHA, shaErr := resolveMQSubmitCommitSHA(g, branch)
	if shaErr != nil {
		style.PrintWarning("could not resolve submitted branch SHA: %v (falling back to branch-only dedup)", shaErr)
	}

	// Build MR bead title and description
	title := fmt.Sprintf("Merge: %s", issueID)
	description := fmt.Sprintf("branch: %s\ntarget: %s\nsource_issue: %s\nrig: %s",
		branch, target, issueID, rigName)
	if commitSHA != "" {
		description += fmt.Sprintf("\ncommit_sha: %s", commitSHA)
	}
	if worker != "" {
		description += fmt.Sprintf("\nworker: %s", worker)
	}

	// Verify before either an idempotent success or a new MR registration.
	// Refinery's later branch check is local-ref based, so missing/stale pushes
	// must fail here instead of producing a delayed refinery rejection.
	if err := verifyMQSubmitPushedBranch(g, branch, commitSHA); err != nil {
		return err
	}

	// Check if MR bead already exists for this branch+SHA (idempotency)
	var mrIssue *beads.Issue
	var existingMR *beads.Issue
	if commitSHA != "" {
		existingMR, err = bd.FindMRForBranchAndSHA(branch, commitSHA)
	} else {
		existingMR, err = bd.FindMRForBranch(branch)
	}
	if err != nil {
		style.PrintWarning("could not check for existing MR: %v", err)
		// Dedup check failed — fall through to create a new MR
	}

	if existingMR != nil {
		if err := validateMergeRequestSource(existingMR, issueID, sourceIssue); err != nil {
			return fmt.Errorf("existing merge request validation failed: %w", err)
		}
		mrIssue = existingMR
		fmt.Printf("%s MR already exists (idempotent)\n", style.Bold.Render("✓"))
	} else {
		// Create MR bead (ephemeral wisp - will be cleaned up after merge)
		mrIssue, err = bd.Create(beads.CreateOptions{
			Title:       title,
			Labels:      []string{"gt:merge-request"},
			Priority:    priority,
			Description: description,
			Ephemeral:   true,
			Rig:         rigName, // Ensure MR bead is created in the rig's database (gt-7y7)
		})
		if err != nil {
			return fmt.Errorf("creating merge request bead: %w", err)
		}

		// gt-gpy: Validate MR bead landed in the rig's database (warning only).
		if prefixErr := beads.ValidateRigPrefix(townRoot, rigName, mrIssue.ID); prefixErr != nil {
			style.PrintWarning("MR bead prefix mismatch: %v\nThe refinery may not find this MR — check 'gt mq list %s'", prefixErr, rigName)
		}

		// Nudge refinery to pick up the new MR
		nudgeRefinery(rigName, "MERGE_READY received - check inbox for pending work")

		// GH#2599: Back-link source issue to MR bead for discoverability.
		if issueID != "" {
			comment := fmt.Sprintf("MR created: %s", mrIssue.ID)
			if err := sourceBD.AddComment(issueID, comment); err != nil {
				style.PrintWarning("could not back-link source issue %s to MR %s: %v", issueID, mrIssue.ID, err)
			}
		}

		// Supersede older open MRs for the same source issue.
		// When a new polecat reattempts an issue, the old MR (different branch)
		// is orphaned. Close it so the queue and GitHub PRs stay clean.
		if issueID != "" {
			if oldMRs, err := bd.FindOpenMRsForIssue(issueID); err == nil {
				for _, old := range oldMRs {
					if old.ID == mrIssue.ID {
						continue // skip the one we just created
					}
					reason := fmt.Sprintf("superseded by %s", mrIssue.ID)
					if err := bd.CloseWithReason(reason, old.ID); err != nil {
						style.PrintWarning("could not supersede old MR %s: %v", old.ID, err)
						continue
					}
					fmt.Printf("  %s Superseded old MR: %s\n", style.Dim.Render("○"), old.ID)

					// Delete the old remote branch to auto-close the GitHub PR.
					// Only polecat branches — non-polecat branches may belong to
					// contributor forks; deleting them closes upstream PRs. (GH#2669)
					oldFields := beads.ParseMRFields(old)
					if oldFields != nil && strings.HasPrefix(oldFields.Branch, "polecat/") {
						g := git.NewGit(cwd)
						if err := g.DeleteRemoteBranch("origin", oldFields.Branch); err != nil {
							style.PrintWarning("could not delete superseded branch %s: %v", oldFields.Branch, err)
						} else {
							fmt.Printf("  %s Deleted remote branch: %s\n", style.Dim.Render("○"), oldFields.Branch)
						}
					}
				}
			}
		}
	}

	// Success output
	fmt.Printf("%s Submitted to merge queue\n", style.Bold.Render("✓"))
	fmt.Printf("  MR ID: %s\n", style.Bold.Render(mrIssue.ID))
	fmt.Printf("  Source: %s\n", branch)
	fmt.Printf("  Target: %s\n", target)
	fmt.Printf("  Issue: %s\n", issueID)
	if worker != "" {
		fmt.Printf("  Worker: %s\n", worker)
	}
	fmt.Printf("  Priority: P%d\n", priority)

	// Auto-cleanup for polecats: if this is a polecat branch and cleanup not disabled,
	// send lifecycle request and wait for termination
	if worker != "" && !mqSubmitNoCleanup {
		fmt.Println()
		fmt.Printf("%s Auto-cleanup: polecat work submitted\n", style.Bold.Render("✓"))
		if err := polecatCleanup(rigName, worker, townRoot); err != nil {
			// Non-fatal: warn but return success (MR was created)
			style.PrintWarning("Could not auto-cleanup: %v", err)
			fmt.Println(style.Dim.Render("  You may need to run 'gt handoff --shutdown' manually"))
			return nil
		}
		// polecatCleanup may timeout while waiting, but MR was already created
	}

	return nil
}

func resolveMQSubmitCommitSHA(g *git.Git, branch string) (string, error) {
	return g.Rev(fmt.Sprintf("refs/heads/%s^{commit}", branch))
}

func verifyMQSubmitPushedBranch(g *git.Git, branch, commitSHA string) error {
	if commitSHA != "" {
		if err := g.VerifyPushedCommit("origin", branch, commitSHA); err != nil {
			return fmt.Errorf("%w\n\nHint: run 'git push origin %s' first (or 'gt done'), then re-run 'gt mq submit'", err, branch)
		}
		return nil
	}

	exists, err := g.PushRemoteBranchExists("origin", branch)
	if err != nil {
		return fmt.Errorf("verify branch on origin: %w\n\nHint: run 'git push origin %s' first (or 'gt done'), then re-run 'gt mq submit'", err, branch)
	}
	if !exists {
		return fmt.Errorf("branch %q not found on origin\n\nHint: run 'git push origin %s' first (or 'gt done'), then re-run 'gt mq submit'", branch, branch)
	}
	return nil
}

// checkMoleculeStepDeps verifies that all prerequisite molecule steps are closed
// before allowing submission to the merge queue. Returns an error listing
// incomplete steps if any prerequisites are not yet done.
func checkMoleculeStepDeps(bd *beads.Beads, sourceIssue *beads.Issue) error {
	// Check if issue has an attached molecule
	fields := beads.ParseAttachmentFields(sourceIssue)
	if fields == nil || fields.AttachedMolecule == "" {
		return nil // No molecule attached — no enforcement needed
	}

	moleculeID := fields.AttachedMolecule

	// List all molecule steps (children of the molecule)
	children, err := bd.List(beads.ListOptions{
		Parent:   moleculeID,
		Status:   "all",
		Priority: -1,
	})
	if err != nil {
		// If we can't list steps, warn but don't block submission
		style.PrintWarning("could not check molecule steps for %s: %v", moleculeID, err)
		return nil
	}

	return validateMoleculePrereqs(children)
}

// validateMoleculePrereqs checks that all molecule steps that are prerequisites
// of the submit step are closed. Returns an error listing incomplete steps.
// Extracted for testability — accepts step data directly.
func validateMoleculePrereqs(children []*beads.Issue) error {
	if len(children) == 0 {
		return nil // No steps to check
	}

	// Find the submit step — it's the step whose title contains "submit"
	// (case-insensitive). All steps that come before it in the dependency
	// chain must be closed.
	submitSeq := 999999
	for _, child := range children {
		titleLower := strings.ToLower(child.Title)
		if strings.Contains(titleLower, "submit") {
			seq := extractStepSequence(child.ID)
			if seq < submitSeq {
				submitSeq = seq
			}
			break
		}
	}

	// Collect incomplete prerequisite steps.
	// A prerequisite is any step sequenced before the submit step (by step
	// number suffix) that is not closed. Steps at or after the submit step
	// are post-submit (await-verdict, self-clean) and don't need to be done.
	var incompleteSteps []*beads.Issue
	for _, child := range children {
		seq := extractStepSequence(child.ID)
		if seq >= submitSeq {
			continue // This is the submit step or a post-submit step
		}
		if child.Status != "closed" {
			incompleteSteps = append(incompleteSteps, child)
		}
	}

	if len(incompleteSteps) == 0 {
		return nil // All prerequisites are closed
	}

	// Sort by sequence for readable output
	sortStepsBySequence(incompleteSteps)

	// Build error message listing incomplete steps
	var sb strings.Builder
	sb.WriteString("molecule step dependencies not met — incomplete prerequisite steps:\n")
	for _, step := range incompleteSteps {
		sb.WriteString(fmt.Sprintf("  ✗ %s: %s [%s]\n", step.ID, step.Title, step.Status))
	}
	sb.WriteString(fmt.Sprintf("\nComplete these steps before submitting, or use --skip-deps to override."))

	return fmt.Errorf("%s", sb.String())
}

// polecatCleanup sends a lifecycle shutdown request to the witness and waits for termination.
// This is called after a polecat successfully submits an MR.
func polecatCleanup(rigName, worker, townRoot string) error {
	// Send lifecycle request to witness
	manager := rigName + "/witness"
	subject := fmt.Sprintf("LIFECYCLE: polecat-%s requesting shutdown", worker)
	body := fmt.Sprintf(`Lifecycle request from polecat %s.

Action: shutdown
Reason: MR submitted to merge queue
Time: %s

Please verify state and execute lifecycle action.
`, worker, time.Now().Format(time.RFC3339))

	// Send via gt mail
	cmd := exec.Command("gt", "mail", "send", manager,
		"-s", subject,
		"-m", body,
	)
	cmd.Dir = townRoot

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sending lifecycle request: %w: %s", err, string(out))
	}
	fmt.Printf("%s Sent shutdown request to %s\n", style.Bold.Render("✓"), manager)

	// Wait for retirement with periodic status
	fmt.Println()
	fmt.Printf("%s Waiting for retirement...\n", style.Dim.Render("◌"))
	fmt.Println(style.Dim.Render("(Witness will terminate this session)"))

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Timeout after 5 minutes to prevent indefinite blocking
	const maxCleanupWait = 5 * time.Minute
	timeout := time.After(maxCleanupWait)

	waitStart := time.Now()
	for {
		select {
		case <-ticker.C:
			elapsed := time.Since(waitStart).Round(time.Second)
			fmt.Printf("%s Still waiting (%v elapsed)...\n", style.Dim.Render("◌"), elapsed)
			if elapsed >= 2*time.Minute {
				fmt.Println(style.Dim.Render("  Hint: If witness isn't responding, you may need to:"))
				fmt.Println(style.Dim.Render("  - Check if witness is running: gt rig status"))
				fmt.Println(style.Dim.Render("  - Use Ctrl+C to abort and manually exit"))
			}
		case <-timeout:
			fmt.Printf("%s Timeout waiting for polecat retirement\n", style.WarningPrefix)
			fmt.Println(style.Dim.Render("  The polecat may have already terminated, or witness is unresponsive."))
			fmt.Println(style.Dim.Render("  You can verify with: gt polecat status"))
			return nil // Don't fail the MR submission just because cleanup timed out
		}
	}
}
