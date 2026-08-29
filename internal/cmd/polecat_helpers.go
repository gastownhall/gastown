package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
)

// polecatTarget represents a polecat to operate on.
type polecatTarget struct {
	rigName     string
	polecatName string
	mgr         *polecat.Manager
	r           *rig.Rig
}

// resolvePolecatTargets builds a list of polecats from command args.
// If useAll is true, the first arg is treated as a rig name and all polecats in it are returned.
// Otherwise, args are parsed as rig/polecat addresses.
func resolvePolecatTargets(args []string, useAll bool) ([]polecatTarget, error) {
	var targets []polecatTarget

	if useAll {
		// --all flag: first arg is just the rig name
		rigName := args[0]
		// Check if it looks like rig/polecat format
		if _, _, err := parseAddress(rigName); err == nil {
			return nil, fmt.Errorf("with --all, provide just the rig name (e.g., 'gt polecat <cmd> %s --all')", strings.Split(rigName, "/")[0])
		}

		mgr, r, err := getPolecatManager(rigName)
		if err != nil {
			return nil, err
		}

		polecats, err := mgr.List()
		if err != nil {
			return nil, fmt.Errorf("listing polecats: %w", err)
		}

		for _, p := range polecats {
			targets = append(targets, polecatTarget{
				rigName:     rigName,
				polecatName: p.Name,
				mgr:         mgr,
				r:           r,
			})
		}
	} else {
		// Multiple rig/polecat arguments - require explicit rig/polecat format
		for _, arg := range args {
			// Validate format: must contain "/" to avoid misinterpreting rig names as polecat names
			if !strings.Contains(arg, "/") {
				return nil, fmt.Errorf("invalid address '%s': must be in 'rig/polecat' format (e.g., 'gastown/Toast')", arg)
			}

			rigName, polecatName, err := parseAddress(arg)
			if err != nil {
				return nil, fmt.Errorf("invalid address '%s': %w", arg, err)
			}

			mgr, r, err := getPolecatManager(rigName)
			if err != nil {
				return nil, err
			}

			targets = append(targets, polecatTarget{
				rigName:     rigName,
				polecatName: polecatName,
				mgr:         mgr,
				r:           r,
			})
		}
	}

	return targets, nil
}

// SafetyCheckResult holds the result of safety checks for a polecat.
type SafetyCheckResult struct {
	Polecat       string
	Blocked       bool
	Reasons       []string
	CleanupStatus polecat.CleanupStatus
	HookBead      string
	HookStale     bool // true if hooked bead is closed
	ActiveMR      string
	OpenMR        string
	GitState      *GitState
}

// checkPolecatSafety performs safety checks before destructive operations.
// Returns nil if the polecat is safe to operate on, or a SafetyCheckResult with reasons if blocked.
func checkPolecatSafety(target polecatTarget) *SafetyCheckResult {
	result := &SafetyCheckResult{
		Polecat: fmt.Sprintf("%s/%s", target.rigName, target.polecatName),
	}

	// Get polecat info for branch name
	polecatInfo, infoErr := target.mgr.Get(target.polecatName)

	// Check 1: Unpushed commits via cleanup_status or git state
	bd := beads.New(target.r.Path)
	agentBeadID := polecatBeadIDForRig(target.r, target.rigName, target.polecatName)
	agentIssue, fields, err := bd.GetAgentBead(agentBeadID)

	if err != nil || fields == nil {
		// No agent bead - fall back to git check
		if infoErr == nil && polecatInfo != nil {
			gitState, gitErr := getGitState(polecatInfo.ClonePath)
			result.GitState = gitState
			if gitErr != nil {
				result.Reasons = append(result.Reasons, "cannot check git state")
			} else if !gitState.Clean {
				if gitState.UnpushedCommits > 0 {
					result.Reasons = append(result.Reasons, fmt.Sprintf("has %d unpushed commit(s)", gitState.UnpushedCommits))
				} else if len(gitState.UncommittedFiles) > 0 {
					result.Reasons = append(result.Reasons, fmt.Sprintf("has %d uncommitted file(s)", len(gitState.UncommittedFiles)))
				} else if gitState.StashCount > 0 {
					result.Reasons = append(result.Reasons, fmt.Sprintf("has %d stash(es)", gitState.StashCount))
				}
			}
		}
	} else {
		// Check cleanup_status from agent bead
		result.CleanupStatus = polecat.CleanupStatus(fields.CleanupStatus)
		switch result.CleanupStatus {
		case polecat.CleanupClean:
			// OK
		default:
			result.Reasons = append(result.Reasons, cleanupStatusBlocker(result.CleanupStatus))
		}

		// Check 3: Work on hook
		hookBead := agentIssue.HookBead
		if hookBead == "" {
			hookBead = fields.HookBead
		}
		if hookBead != "" {
			result.HookBead = hookBead
			// Check if hooked bead is still active (not closed)
			hookedIssue, err := bd.Show(hookBead)
			if err == nil && hookedIssue != nil {
				if hookedIssue.Status != "closed" {
					result.Reasons = append(result.Reasons, fmt.Sprintf("has work on hook (%s)", hookBead))
				} else {
					result.HookStale = true
				}
			} else {
				result.Reasons = append(result.Reasons, fmt.Sprintf("has work on hook (%s, unverified)", hookBead))
			}
		}

		if fields.ActiveMR != "" {
			result.ActiveMR = fields.ActiveMR
			if blocker := activeMRBlocker(bd, fields.ActiveMR); blocker != "" {
				result.Reasons = append(result.Reasons, blocker)
			}
		}
	}

	// Check 2: Open MR beads for this branch
	if infoErr == nil && polecatInfo != nil && polecatInfo.Branch != "" {
		mr, mrErr := bd.FindMRForBranch(polecatInfo.Branch)
		if mrErr == nil && mr != nil {
			result.OpenMR = mr.ID
			result.Reasons = append(result.Reasons, fmt.Sprintf("has open MR (%s)", mr.ID))
		}
	}

	result.Blocked = len(result.Reasons) > 0
	return result
}

func rigPrefix(r *rig.Rig) string {
	townRoot := filepath.Dir(r.Path)
	return beads.GetPrefixForRig(townRoot, r.Name)
}

func polecatBeadIDForRig(r *rig.Rig, rigName, polecatName string) string {
	return beads.PolecatBeadIDWithPrefix(rigPrefix(r), rigName, polecatName)
}

// displaySafetyCheckBlocked prints blocked polecats and guidance.
func displaySafetyCheckBlocked(blocked []*SafetyCheckResult) {
	displaySafetyCheckBlockedTo(os.Stderr, blocked)
}

func displaySafetyCheckBlockedTo(w io.Writer, blocked []*SafetyCheckResult) {
	fmt.Fprintf(w, "%s Cannot nuke the following polecats:\n\n", style.Error.Render("Error:"))
	var polecatList []string
	for _, b := range blocked {
		fmt.Fprintf(w, "  %s:\n", style.Bold.Render(b.Polecat))
		for _, r := range b.Reasons {
			fmt.Fprintf(w, "    - %s\n", r)
		}
		polecatList = append(polecatList, b.Polecat)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Safety checks failed. Resolve issues before nuking, or use --force.")
	fmt.Fprintln(w, "Options:")
	fmt.Fprintln(w, "  1. Complete work: gt done (from polecat session)")
	fmt.Fprintln(w, "  2. Push changes: git push (from polecat worktree)")
	fmt.Fprintln(w, "  3. Escalate: gt mail send mayor/ -s \"RECOVERY_NEEDED\" -m \"...\"")
	fmt.Fprintf(w, "  4. Force nuke (LOSES WORK): gt polecat nuke --force %s\n", strings.Join(polecatList, " "))
	fmt.Fprintln(w)
}

func formatSafetyCheckBlockers(blocked []*SafetyCheckResult) string {
	parts := make([]string, 0, len(blocked))
	for _, b := range blocked {
		parts = append(parts, fmt.Sprintf("%s: %s", b.Polecat, strings.Join(b.Reasons, "; ")))
	}
	return strings.Join(parts, " | ")
}

// displayDryRunSafetyCheck shows safety check status for dry-run mode. It returns true when a normal nuke would refuse.
func displayDryRunSafetyCheck(target polecatTarget) bool {
	fmt.Printf("\n  Safety checks:\n")
	result := checkPolecatSafety(target)
	polecatInfo, infoErr := target.mgr.Get(target.polecatName)
	bd := beads.New(target.r.Path)
	agentBeadID := polecatBeadIDForRig(target.r, target.rigName, target.polecatName)
	agentIssue, fields, err := bd.GetAgentBead(agentBeadID)

	// Check 1: cleanup status or fallback git state
	if err != nil || fields == nil {
		if infoErr == nil && polecatInfo != nil {
			gitState, gitErr := getGitState(polecatInfo.ClonePath)
			if gitErr != nil {
				fmt.Printf("    - Git state: %s\n", style.Warning.Render("cannot check"))
			} else if gitState.Clean {
				fmt.Printf("    - Git state: %s\n", style.Success.Render("clean"))
			} else {
				fmt.Printf("    - Git state: %s\n", style.Error.Render("dirty"))
			}
		} else {
			fmt.Printf("    - Git state: %s\n", style.Dim.Render("unknown (no polecat info)"))
		}
		fmt.Printf("    - Hook: %s\n", style.Dim.Render("unknown (no agent bead)"))
	} else {
		cleanupStatus := polecat.CleanupStatus(fields.CleanupStatus)
		if cleanupStatus.IsSafe() {
			fmt.Printf("    - Cleanup status: %s\n", style.Success.Render(string(cleanupStatus)))
		} else if cleanupStatus.RequiresRecovery() {
			fmt.Printf("    - Cleanup status: %s\n", style.Error.Render(string(cleanupStatus)))
		} else {
			statusText := string(cleanupStatus)
			if statusText == "" {
				statusText = "<missing>"
			}
			fmt.Printf("    - Cleanup status: %s\n", style.Warning.Render(statusText))
		}

		hookBead := agentIssue.HookBead
		if hookBead == "" {
			hookBead = fields.HookBead
		}
		if hookBead != "" {
			hookedIssue, err := bd.Show(hookBead)
			if err == nil && hookedIssue != nil && hookedIssue.Status == "closed" {
				fmt.Printf("    - Hook: %s (%s, closed - stale)\n", style.Warning.Render("stale"), hookBead)
			} else {
				fmt.Printf("    - Hook: %s (%s)\n", style.Error.Render("has work"), hookBead)
			}
		} else {
			fmt.Printf("    - Hook: %s\n", style.Success.Render("empty"))
		}

		if fields.ActiveMR != "" {
			if blocker := activeMRBlocker(bd, fields.ActiveMR); blocker != "" {
				fmt.Printf("    - Active MR: %s (%s)\n", style.Error.Render("blocked"), blocker)
			} else {
				fmt.Printf("    - Active MR: %s (%s)\n", style.Success.Render("terminal"), fields.ActiveMR)
			}
		}
	}

	// Check 2: Open MR
	if infoErr == nil && polecatInfo != nil && polecatInfo.Branch != "" {
		mr, mrErr := bd.FindMRForBranch(polecatInfo.Branch)
		if mrErr == nil && mr != nil {
			fmt.Printf("    - Open MR: %s (%s)\n", style.Error.Render("yes"), mr.ID)
		} else {
			fmt.Printf("    - Open MR: %s\n", style.Success.Render("none"))
		}
	} else {
		fmt.Printf("    - Open MR: %s\n", style.Dim.Render("unknown (no branch info)"))
	}

	return result.Blocked
}
