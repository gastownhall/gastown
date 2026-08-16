package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/git"
	"github.com/jonbaldie/gastown/internal/polecat"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var tapPolecatStopCmd = &cobra.Command{
	Use:   "polecat-stop-check",
	Short: "Auto-run gt done on session Stop if polecat has pending work",
	Long: `Safety net for the "idle polecat" problem: polecats that finish work
but forget to call gt done before the session ends.

This command is designed to run from a Claude Code Stop hook. It checks:
1. Whether this is a polecat session (GT_POLECAT env var)
2. Whether gt done has already run (heartbeat state is "exiting" or "idle")
3. Whether the polecat has commits, stashes, or non-runtime dirty work

If the polecat has pending work that wasn't submitted, this command
runs gt done to submit it. If gt done already ran or there's nothing
to submit, it exits silently.

Exit codes:
  0 - No action needed (not a polecat, already done, or gt done succeeded)
  1 - gt done was attempted but failed`,
	RunE:         runTapPolecatStop,
	SilenceUsage: true,
}

func init() {
	tapCmd.AddCommand(tapPolecatStopCmd)
}

func runTapPolecatStop(cmd *cobra.Command, args []string) error {
	// Only applies to polecats
	polecatName := os.Getenv("GT_POLECAT")
	if polecatName == "" {
		return nil // Not a polecat session — nothing to do
	}

	sessionName := os.Getenv("GT_SESSION")
	if sessionName == "" {
		return nil // No session tracking — can't check state
	}

	// Find town root for heartbeat check
	townRoot, _, _ := workspace.FindFromCwdWithFallback()
	if townRoot == "" {
		townRoot = os.Getenv("GT_TOWN_ROOT")
	}
	if townRoot == "" {
		return nil // Can't find workspace — exit quietly
	}

	// Check heartbeat state: if already "exiting" or "idle", gt done already ran
	hb := polecat.ReadSessionHeartbeat(townRoot, sessionName)
	if hb != nil {
		state := hb.EffectiveState()
		if state == polecat.HeartbeatExiting || state == polecat.HeartbeatIdle {
			return nil // gt done already ran or polecat is idle — nothing to do
		}
	}

	// Check if the polecat is on a feature branch with work to submit.
	rigName := os.Getenv("GT_RIG")
	if rigName == "" {
		return nil
	}

	// Reconstruct polecat worktree path
	polecatDir := filepath.Join(townRoot, rigName, "polecats", polecatName)
	// Try the nested clone layout first (polecats/<name>/<rig>/)
	cloneDir := filepath.Join(polecatDir, rigName)
	if _, err := os.Stat(filepath.Join(cloneDir, ".git")); err != nil {
		// Fall back to flat layout
		cloneDir = polecatDir
		if _, err := os.Stat(filepath.Join(cloneDir, ".git")); err != nil {
			return nil // No git repo found — exit quietly
		}
	}

	// Check current branch — skip if on main/master
	branchCmd := exec.Command("git", "-C", cloneDir, "rev-parse", "--abbrev-ref", "HEAD")
	branchOut, err := branchCmd.Output()
	if err != nil {
		return nil // Can't determine branch — exit quietly
	}
	branch := strings.TrimSpace(string(branchOut))
	if branch == "main" || branch == "master" || branch == "HEAD" {
		return nil // On default branch — nothing to submit
	}

	pending, reason, err := polecatStopPendingWork(cloneDir, branch)
	if err != nil || !pending {
		return nil // Can't check, or no work to submit — don't block session stop
	}

	// Polecat has pending work! Run gt done as a safety net.
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "⚠️  Polecat %s has pending work on branch %s (%s)\n", polecatName, branch, reason)
	fmt.Fprintf(os.Stderr, "   Auto-running gt done as safety net...\n")
	fmt.Fprintf(os.Stderr, "\n")

	// Find gt binary path
	gtBin, err := os.Executable()
	if err != nil {
		gtBin = "gt"
	}

	// Run gt done in the polecat's worktree context
	doneCmd := exec.Command(gtBin, "done")
	doneCmd.Dir = cloneDir
	doneCmd.Stdout = os.Stdout
	doneCmd.Stderr = os.Stderr
	// Inherit environment (GT_POLECAT, GT_RIG, etc. are already set)
	doneCmd.Env = os.Environ()

	if err := doneCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  Auto gt done failed: %v\n", err)
		fmt.Fprintf(os.Stderr, "   Witness will handle cleanup.\n")
		// Don't return error — don't block session stop
		return nil
	}

	return nil
}

func polecatStopPendingWork(cloneDir, branch string) (bool, string, error) {
	g := git.NewGit(cloneDir)
	workStatus, err := g.CheckUncommittedWork()
	if err != nil {
		return false, "", err
	}

	if workStatus.HasUncommittedChanges && !workStatus.CleanExcludingRuntime() {
		return true, fmt.Sprintf("%d non-runtime dirty file(s)", len(workStatus.NonRuntimePaths())), nil
	}
	if workStatus.StashCount > 0 {
		return true, fmt.Sprintf("%d branch stash(es)", workStatus.StashCount), nil
	}

	targetStatus, err := g.BranchTargetStatus(branch, "origin", nil)
	if err != nil {
		return false, "", err
	}
	if !targetStatus.Preserved && targetStatus.UnpreservedPatchCount > 0 {
		return true, fmt.Sprintf("%d unsubmitted commit(s)", targetStatus.UnpreservedPatchCount), nil
	}

	return false, "", nil
}
