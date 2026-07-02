package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/templates"
	"github.com/steveyegge/gastown/internal/util"
	"github.com/steveyegge/gastown/internal/workspace"
)

var exitCmd = &cobra.Command{
	Use:         "exit",
	GroupID:     GroupWork,
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Save work and exit (dispatch-and-kill model)",
	Long: `Save all work and exit the polecat session cleanly.

Lightweight alternative to gt done for the dispatch-and-kill model:
1. Auto-commits any uncommitted work (safety net)
2. Pushes branch to origin
3. Persists a completion note to the bead
4. Exits — the daemon reaper handles the rest

Does NOT:
- Submit to merge queue (no MR beads)
- Notify witnesses (we don't use them)
- Transition to IDLE (polecats are fire-and-forget)
- Close the bead (archivist does this)

Examples:
  gt exit                              # Auto-save, push, exit
  gt exit --notes "Added type hints to 21 files"
  gt exit --issue sbx-gastown-abc      # Explicit issue ID`,
	RunE:         runExit,
	SilenceUsage: true,
}

var (
	exitNotes string
	exitIssue string
)

func init() {
	exitCmd.Flags().StringVar(&exitNotes, "notes", "", "Completion notes to persist on the bead")
	exitCmd.Flags().StringVar(&exitIssue, "issue", "", "Issue ID (default: auto-detect from branch name)")
	rootCmd.AddCommand(exitCmd)
}

func runExit(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cannot determine working directory: %w", err)
	}

	g := git.NewGit(cwd)

	branch, err := g.CurrentBranch()
	if err != nil {
		return fmt.Errorf("cannot determine current branch: %w", err)
	}

	// Track exit-path outcomes so we can emit a structured event on the way
	// out. The deacon polls daemon/polecat-events.jsonl for wake activity;
	// the mayor is paged via `gt escalate` when success=false.
	exitSuccess := true
	var exitFailures []string
	recordFailure := func(reason string) {
		exitSuccess = false
		exitFailures = append(exitFailures, reason)
	}

	// Auto-detect issue from branch name
	issueID := exitIssue
	if issueID == "" {
		issueID = parseBranchName(branch).Issue
	}

	// Fallback: query for hooked beads assigned to this agent.
	// Modern polecat branches (polecat/<worker>-<timestamp>) don't embed the
	// issue ID, so parseBranchName returns "". Query beads directly for
	// status=hooked + assignee — same pattern gt done uses (hq-l6mm5).
	if issueID == "" {
		sender := detectSender()
		if sender != "" {
			bd := beads.New(beads.ResolveBeadsDir(townRoot))
			if hookIssue := findHookedBeadForAgent(bd, sender); hookIssue != "" {
				issueID = hookIssue
				fmt.Printf("%s Issue resolved from hooked bead: %s\n", style.Bold.Render("✓"), issueID)
			}
		}
	}

	// Determine rig name
	rigName := os.Getenv("GT_RIG")
	if rigName == "" {
		rigName, _ = workspace.FindRigFromCwd(cwd, townRoot)
	}

	// 1. AUTO-COMMIT SAFETY NET
	// Check for uncommitted work and commit it to prevent loss
	workStatus, err := g.CheckUncommittedWork()
	if err == nil && workStatus.HasUncommittedChanges && !workStatus.CleanExcludingRuntime() {
		fmt.Printf("\n%s Uncommitted changes detected — auto-saving\n", style.Bold.Render("⚠"))
		fmt.Printf("  Files: %s\n\n", workStatus.String())

		if addErr := g.Add("-A"); addErr != nil {
			style.PrintWarning("auto-save: git add failed: %v", addErr)
		} else {
			// Unstage runtime scaffolding (.beads/ .runtime/ .claude/ .logs/
			// __pycache__/ node_modules) that git add -A swept in. git add -A
			// stages already-tracked runtime files regardless of ignore rules,
			// so this commit-time guard is the reliable exclusion point — without
			// it, polecat scaffolding leaks into child-repo PRs (sbx-gastown-8awxz).
			_ = g.UnstageRuntimeArtifacts()
			// Unstage overlay files
			_ = g.ResetFiles("CLAUDE.local.md")
			if claudeData, readErr := os.ReadFile(filepath.Join(cwd, "CLAUDE.md")); readErr == nil {
				if strings.Contains(string(claudeData), templates.PolecatLifecycleMarker) {
					_ = g.ResetFiles("CLAUDE.md")
				}
			}
			autoMsg := "fix: auto-save uncommitted work (gt exit safety net)"
			if issueID != "" {
				autoMsg = fmt.Sprintf("fix: auto-save uncommitted work (%s)", issueID)
			}
			if commitErr := g.Commit(autoMsg); commitErr != nil {
				style.PrintWarning("auto-save: git commit failed: %v", commitErr)
			} else {
				fmt.Printf("%s Auto-committed uncommitted work\n", style.Bold.Render("✓"))
			}
		}
	}

	// 2. PUSH TO ORIGIN
	defaultBranch := "main"
	aheadCount, err := g.CommitsAhead("origin/"+defaultBranch, branch)
	if err == nil && aheadCount > 0 {
		fmt.Printf("%s Pushing %d commit(s) to origin...\n", style.Bold.Render("→"), aheadCount)
		if pushErr := g.Push("origin", "HEAD", false); pushErr != nil {
			style.PrintWarning("push failed: %v — work is committed locally but not on remote", pushErr)
			recordFailure(fmt.Sprintf("push failed: %v", pushErr))
		} else {
			fmt.Printf("%s Branch pushed\n", style.Bold.Render("✓"))
		}
	} else {
		// Try push anyway — CommitsAhead may fail on detached HEAD or missing remote ref
		if pushErr := g.Push("origin", "HEAD", false); pushErr != nil {
			fmt.Printf("%s Nothing to push or push failed\n", style.Dim.Render("○"))
		} else {
			fmt.Printf("%s Branch pushed\n", style.Bold.Render("✓"))
		}
	}

	// 3. PERSIST COMPLETION NOTES TO BEAD
	if issueID != "" {
		notes := exitNotes
		if notes == "" {
			notes = fmt.Sprintf("Polecat exit: branch %s pushed. Rig: %s.", branch, rigName)
		}

		bdCmd := exec.Command("bd", buildExitNotesArgs(issueID, notes)...)
		bdCmd.Dir = townRoot
		bdCmd.Env = append(os.Environ(), "BEADS_DIR="+beads.ResolveBeadsDir(townRoot))
		util.SetDetachedProcessGroup(bdCmd)
		if err := bdCmd.Run(); err != nil {
			style.PrintWarning("could not persist notes to %s: %v", issueID, err)
		} else {
			fmt.Printf("%s Notes persisted to %s\n", style.Bold.Render("✓"), issueID)
		}

		// Read the bead's accumulated fields for domain note and mayor nudge.
		info := readExitBeadInfo(townRoot, issueID)

		// Write bead notes as a domain note for the archivist_dog to pick up.
		// The archivist_dog scans rigs/<rig>/domain/notes/*.md on a timer and
		// dispatches a bridge-local Opus agent to collate findings.
		if rigName != "" {
			notesDir := filepath.Join(townRoot, "rigs", rigName, "domain", "notes")
			if err := os.MkdirAll(notesDir, 0755); err == nil {
				// Collect notes from attached molecule wisps (formula step beads)
				molID := extractAttachedMolecule(info.Description)
				wisps := collectWispNotes(townRoot, molID)

				noteFile := filepath.Join(notesDir, issueID+".md")
				noteContent := buildDomainNote(issueID, branch, info.Notes, info.Design, notes, wisps)
				if err := os.WriteFile(noteFile, []byte(noteContent), 0644); err == nil {
					fmt.Printf("%s Domain note written for archivist: %s\n", style.Bold.Render("✓"), filepath.Base(noteFile))
				}
			}
		}

		// Close the bead — archivist extracts knowledge later via daemon trigger
		closeCmd := exec.Command("bd", "close", issueID, "--reason",
			fmt.Sprintf("Polecat exit: branch %s, rig %s", branch, rigName))
		closeCmd.Dir = townRoot
		closeCmd.Env = append(os.Environ(), "BEADS_DIR="+beads.ResolveBeadsDir(townRoot))
		util.SetDetachedProcessGroup(closeCmd)
		if err := closeCmd.Run(); err != nil {
			style.PrintWarning("could not close %s: %v", issueID, err)
			recordFailure(fmt.Sprintf("bead close failed: %v", err))
		} else {
			fmt.Printf("%s Bead %s closed\n", style.Bold.Render("✓"), issueID)
		}

		// 3b. NUDGE MAYOR — ephemeral notification so mayor doesn't poll reaper log
		nudgeMayor(townRoot, issueID, info.Title, branch)
	} else {
		fmt.Printf("%s No issue ID detected — skipping bead update\n", style.Dim.Render("○"))
		recordFailure("no issue ID detected")
	}

	// Resolve polecat name for the event payload and tmux teardown.
	polecatName := os.Getenv("GT_POLECAT")
	if polecatName == "" {
		// Derive from branch: polecat/<name>-<timestamp>
		parts := strings.Split(branch, "/")
		if len(parts) >= 2 {
			namePart := parts[1]
			if idx := strings.LastIndex(namePart, "-"); idx > 0 {
				polecatName = namePart[:idx]
			}
		}
	}

	// 4. EMIT WAKE SIGNAL — appends a JSONL line to daemon/polecat-events.jsonl
	// (the file the town deacon polls), nudges the deacon as belt-and-suspenders
	// insurance, and escalates to the mayor on failure paths.
	reason := strings.Join(exitFailures, "; ")
	ev := newPolecatExitEvent(rigName, polecatName, issueID, exitSuccess, reason)
	if werr := writePolecatExitEvent(townRoot, ev); werr != nil {
		style.PrintWarning("could not write polecat event: %v", werr)
	} else {
		fmt.Printf("%s Polecat event emitted (success=%t)\n", style.Bold.Render("✓"), exitSuccess)
	}
	signalDeaconOfExit(townRoot, issueID)
	if !exitSuccess {
		escalatePolecatExitFailure(townRoot, polecatName, reason)
	}

	// 5. SELF-TERMINATE
	// Spawn a detached child (`gt _reap-session`) to kill this polecat's tmux
	// session after a 3-second grace period. Must be a separate process, not an
	// in-process goroutine — gt exits as soon as runExit returns, and a
	// goroutine scheduled for +3s would die with it (sbx-gastown-xpuv).
	if rigName != "" && polecatName != "" {
		sessionName := session.PolecatSessionName(session.PrefixFor(rigName), polecatName)
		if err := scheduleSelfTerminate(townRoot, sessionName, 3*time.Second); err != nil {
			style.PrintWarning("could not schedule self-terminate: %v", err)
		} else {
			fmt.Printf("\n%s Work saved. Session terminating in 3s.\n", style.Bold.Render("✓"))
		}
	} else {
		fmt.Printf("\n%s Work saved.\n", style.Bold.Render("✓"))
	}

	return nil
}

// scheduleSelfTerminate is the seam used by gt exit to schedule its own tmux
// session teardown. Replaceable in tests so we don't spawn real subprocesses.
var scheduleSelfTerminate = defaultScheduleSelfTerminate

// defaultScheduleSelfTerminate launches a detached `gt _reap-session` child
// that outlives this process. The child sleeps for `delay`, then kills the
// named tmux session on the town socket configured by InitRegistry.
func defaultScheduleSelfTerminate(townRoot, sessionName string, delay time.Duration) error {
	gtBin, err := os.Executable()
	if err != nil || gtBin == "" {
		gtBin = "gt"
	}
	cmd := exec.Command(gtBin, "_reap-session",
		"--session", sessionName,
		"--delay", delay.String())
	cmd.Dir = townRoot
	cmd.Env = os.Environ()
	// Detach from gt's stdio and session so the child survives gt's exit and
	// the tmux pane's controlling-terminal teardown.
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	util.SetDaemonProcessGroup(cmd)
	return cmd.Start()
}

// buildExitNotesArgs returns the args for the `bd update` invocation that
// persists the gt-exit boilerplate. We use --append-notes (not --notes) so
// design checkpoints and findings the polecat persisted earlier in the
// session are preserved rather than clobbered (sbx-gastown-9tf8).
func buildExitNotesArgs(issueID, notes string) []string {
	return []string{"update", issueID, "--append-notes", notes}
}

// exitBeadInfo holds fields extracted from bd show --json for exit processing.
type exitBeadInfo struct {
	Notes       string
	Design      string
	Title       string
	Description string
}

// readExitBeadInfo runs `bd show <id> --json` and extracts key fields.
func readExitBeadInfo(townRoot, issueID string) exitBeadInfo {
	bdCmd := exec.Command("bd", "show", issueID, "--json")
	bdCmd.Dir = townRoot
	bdCmd.Env = append(os.Environ(), "BEADS_DIR="+beads.ResolveBeadsDir(townRoot))
	out, err := bdCmd.Output()
	if err != nil || len(out) == 0 {
		return exitBeadInfo{}
	}

	// bd show --json returns an array with one element
	var items []struct {
		Notes       string `json:"notes"`
		Design      string `json:"design"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(out, &items); err != nil || len(items) == 0 {
		return exitBeadInfo{}
	}
	return exitBeadInfo{
		Notes:       items[0].Notes,
		Design:      items[0].Design,
		Title:       items[0].Title,
		Description: items[0].Description,
	}
}

// extractAttachedMolecule parses the attached_molecule field from a bead's description.
func extractAttachedMolecule(description string) string {
	for _, line := range strings.Split(description, "\n") {
		if strings.HasPrefix(line, "attached_molecule: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "attached_molecule: "))
		}
	}
	return ""
}

// wispNote holds a single wisp child's notes.
type wispNote struct {
	ID    string
	Title string
	Notes string
}

// collectWispNotes queries children of a molecule and returns any with notes.
func collectWispNotes(townRoot, moleculeID string) []wispNote {
	if moleculeID == "" {
		return nil
	}

	// Get child IDs from molecule
	listCmd := exec.Command("bd", "list", "--parent", moleculeID, "--all", "--json", "--limit", "0")
	listCmd.Dir = townRoot
	listCmd.Env = append(os.Environ(), "BEADS_DIR="+beads.ResolveBeadsDir(townRoot))
	out, err := listCmd.Output()
	if err != nil || len(out) == 0 {
		return nil
	}

	var children []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(out, &children); err != nil || len(children) == 0 {
		return nil
	}

	// Fetch full details (including notes) for all children in one call
	args := []string{"show", "--json"}
	for _, c := range children {
		args = append(args, c.ID)
	}
	showCmd := exec.Command("bd", args...)
	showCmd.Dir = townRoot
	showCmd.Env = append(os.Environ(), "BEADS_DIR="+beads.ResolveBeadsDir(townRoot))
	out, err = showCmd.Output()
	if err != nil || len(out) == 0 {
		return nil
	}

	var details []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Notes string `json:"notes"`
	}
	if err := json.Unmarshal(out, &details); err != nil {
		return nil
	}

	var result []wispNote
	for _, d := range details {
		if d.Notes != "" {
			result = append(result, wispNote{ID: d.ID, Title: d.Title, Notes: d.Notes})
		}
	}
	return result
}

// buildDomainNote assembles the domain note markdown from bead fields.
func buildDomainNote(issueID, branch, beadNotes, beadDesign, exitNotes string, wispNotes []wispNote) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# %s\n\nSource: polecat exit, bead %s, branch %s\n", issueID, issueID, branch))

	if beadNotes != "" {
		b.WriteString("\n## Notes\n\n")
		b.WriteString(beadNotes)
		b.WriteString("\n")
	}

	if beadDesign != "" {
		b.WriteString("\n## Design\n\n")
		b.WriteString(beadDesign)
		b.WriteString("\n")
	}

	if len(wispNotes) > 0 {
		b.WriteString("\n## Wisp Notes\n\n")
		for _, w := range wispNotes {
			b.WriteString(fmt.Sprintf("### %s: %s\n\n", w.ID, w.Title))
			b.WriteString(w.Notes)
			b.WriteString("\n\n")
		}
	}

	// If no bead fields or wisp notes had content, fall back to the exit notes
	if beadNotes == "" && beadDesign == "" && len(wispNotes) == 0 && exitNotes != "" {
		b.WriteString("\n")
		b.WriteString(exitNotes)
		b.WriteString("\n")
	}

	return b.String()
}

// nudgeMayor sends a completion notification to the mayor via gt nudge.
func nudgeMayor(townRoot, issueID, title, branch string) {
	msg := fmt.Sprintf("Completed %s: %s. Branch: %s", issueID, title, branch)

	// Try to find PR URL for the branch
	prCmd := exec.Command("gh", "pr", "list", "--head", branch, "--json", "url", "--limit", "1")
	prCmd.Dir = townRoot
	if out, err := prCmd.Output(); err == nil && len(out) > 0 {
		var prs []struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(out, &prs) == nil && len(prs) > 0 && prs[0].URL != "" {
			msg = fmt.Sprintf("Completed %s: %s. PR: %s", issueID, title, prs[0].URL)
		}
	}

	nudgeCmd := exec.Command("gt", "nudge", "mayor", "-m", msg)
	nudgeCmd.Dir = townRoot
	nudgeCmd.Env = os.Environ()
	util.SetDetachedProcessGroup(nudgeCmd)
	if err := nudgeCmd.Run(); err != nil {
		style.PrintWarning("could not nudge mayor: %v", err)
	} else {
		fmt.Printf("%s Mayor notified of completion\n", style.Bold.Render("✓"))
	}
}
