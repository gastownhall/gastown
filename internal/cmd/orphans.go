package cmd

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/constants"
	gitpkg "github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/util"
	"github.com/steveyegge/gastown/internal/workspace"
)

var orphansCmd = &cobra.Command{
	Use:     "orphans",
	GroupID: GroupWork,
	Short:   "Find lost polecat work",
	Long: `Find orphaned commits and unmerged polecat branches.

Polecat work can get lost when:
- Session killed before merge
- Refinery fails to process
- Network issues during push

This command scans for:
1. Orphaned commits via 'git fsck --unreachable' (filtered by --days/--all)
2. Unmerged polecat worktree branches (always shown)

Note: --days and --all only apply to orphaned commits, not polecat branches.

Examples:
  gt orphans              # Last 7 days (default), infers rig from cwd
  gt orphans --rig=gastown # Target a specific rig
  gt orphans --days=14    # Last 2 weeks
  gt orphans --all        # Show all orphans (no date filter)`,
	RunE: runOrphans,
}

var (
	orphansDays int
	orphansAll  bool
	orphansRig  string

	// Kill commits command flags
	orphansKillDryRun bool
	orphansKillDays   int
	orphansKillAll    bool
	orphansKillForce  bool

	// Process orphan flags
	orphansProcsForce      bool
	orphansProcsAggressive bool
)

// Commit orphan kill command
var orphansKillCmd = &cobra.Command{
	Use:   "kill",
	Short: "Remove all orphans (commits and processes)",
	Long: `Remove orphaned commits and kill orphaned agent processes.

This command performs a complete orphan cleanup:
1. Finds and removes orphaned commits via 'git gc --prune=now'
2. Finds and kills orphaned agent processes (PPID=1)

WARNING: This operation is irreversible. Once commits are pruned,
they cannot be recovered.

Note: This only affects orphaned commits and processes. Unmerged polecat
branches (shown by 'gt orphans') must be recovered or cleaned up manually.

The command will:
1. Find orphaned commits (same as 'gt orphans')
2. Find orphaned agent processes (same as 'gt orphans procs')
3. Show what will be removed/killed
4. Ask for confirmation (unless --force)
5. Run git gc and kill processes

Examples:
  gt orphans kill              # Kill orphans from last 7 days (default)
  gt orphans kill --days=14    # Kill orphans from last 2 weeks
  gt orphans kill --all        # Kill all orphans
  gt orphans kill --dry-run    # Preview without deleting
  gt orphans kill --force      # Skip confirmation prompt`,
	RunE: runOrphansKill,
}

// Process orphan commands
var orphansProcsCmd = &cobra.Command{
	Use:   "procs",
	Short: "Manage orphaned agent processes",
	Long: `Find and kill agent processes that have become orphaned (PPID=1).

These are processes that survived session termination and are now
parented to init/launchd. They consume resources and should be killed.

	Use --aggressive to detect ALL orphaned agent processes by cross-referencing
	against active tmux sessions. Any tracked agent process NOT in a gt-* or hq-* session
	is considered an orphan. This catches processes that have been reparented to
	something other than init (PPID != 1).

	Examples:
	  gt orphans procs              # List orphaned agent processes (PPID=1 only)
  gt orphans procs list         # Same as above
  gt orphans procs --aggressive # List ALL orphaned processes (tmux verification)
  gt orphans procs kill         # Kill orphaned processes`,
	RunE: runOrphansListProcesses, // Default to list
}

var orphansProcsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List orphaned agent processes",
	Long: `List agent processes that have become orphaned (PPID=1).

These are processes that survived session termination and are now
parented to init/launchd. They consume resources and should be killed.

	Use --aggressive to detect ALL orphaned agent processes by cross-referencing
	against active tmux sessions. Any tracked agent process NOT in a gt-* or hq-* session
	is considered an orphan.

Excludes:
- tmux server processes
- Claude.app desktop application processes

Examples:
  gt orphans procs list             # Show orphans with PPID=1
  gt orphans procs list --aggressive # Show ALL orphans (tmux verification)`,
	RunE: runOrphansListProcesses,
}

var orphansProcsKillCmd = &cobra.Command{
	Use:   "kill",
	Short: "Kill orphaned agent processes",
	Long: `Kill agent processes that have become orphaned (PPID=1).

Without flags, prompts for confirmation before killing.
Use -f/--force to kill without confirmation.
Use --aggressive to kill ALL orphaned processes (not just PPID=1).

Examples:
  gt orphans procs kill             # Kill with confirmation
  gt orphans procs kill -f          # Force kill without confirmation
  gt orphans procs kill --aggressive # Kill ALL orphans (tmux verification)`,
	RunE: runOrphansKillProcesses,
}

func init() {
	orphansCmd.Flags().IntVar(&orphansDays, "days", 7, "Show orphans from last N days")
	orphansCmd.Flags().BoolVar(&orphansAll, "all", false, "Show all orphans (no date filter)")
	orphansCmd.PersistentFlags().StringVar(&orphansRig, "rig", "", "Target rig name (required when not in a rig directory)")

	// Kill commits command flags
	orphansKillCmd.Flags().BoolVar(&orphansKillDryRun, "dry-run", false, "Preview without deleting")
	orphansKillCmd.Flags().IntVar(&orphansKillDays, "days", 7, "Kill orphans from last N days")
	orphansKillCmd.Flags().BoolVar(&orphansKillAll, "all", false, "Kill all orphans (no date filter)")
	orphansKillCmd.Flags().BoolVar(&orphansKillForce, "force", false, "Skip confirmation prompt")

	// Process orphan kill command flags
	orphansProcsKillCmd.Flags().BoolVarP(&orphansProcsForce, "force", "f", false, "Kill without confirmation")

	// Aggressive flag for all procs commands (persistent so it applies to subcommands)
	orphansProcsCmd.PersistentFlags().BoolVar(&orphansProcsAggressive, "aggressive", false, "Use tmux session verification to find ALL orphans (not just PPID=1)")

	// Wire up subcommands
	orphansProcsCmd.AddCommand(orphansProcsListCmd)
	orphansProcsCmd.AddCommand(orphansProcsKillCmd)

	orphansCmd.AddCommand(orphansKillCmd)
	orphansCmd.AddCommand(orphansProcsCmd)

	rootCmd.AddCommand(orphansCmd)
}

// OrphanCommit represents an unreachable commit
type OrphanCommit struct {
	SHA     string
	Date    time.Time
	Author  string
	Subject string
}

func runOrphans(cmd *cobra.Command, args []string) error {
	// Find workspace to determine rig root
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Find rig: use --rig flag if provided, otherwise infer from cwd
	var rigName string
	var r *rig.Rig
	if orphansRig != "" {
		_, r, err = getRig(orphansRig)
		if err != nil {
			return err
		}
		rigName = orphansRig
	} else {
		rigName, r, err = findCurrentRig(townRoot)
		if err != nil {
			return fmt.Errorf("not in a rig directory. Use --rig <name> to specify the target rig, or run from within a rig directory")
		}
	}

	// We need to run from the mayor's clone (main git repo for the rig)
	mayorPath := r.Path + "/mayor/rig"
	foundAnything := false

	// --- Orphaned commits (git fsck) ---
	fmt.Printf("Scanning for orphaned commits in %s...\n\n", rigName)

	orphans, err := findOrphanCommits(mayorPath)
	if err != nil {
		return fmt.Errorf("finding orphans: %w", err)
	}

	// Filter by date unless --all
	cutoff := time.Now().AddDate(0, 0, -orphansDays)
	var filtered []OrphanCommit
	for _, o := range orphans {
		if orphansAll || o.Date.After(cutoff) {
			filtered = append(filtered, o)
		}
	}

	if len(filtered) > 0 {
		foundAnything = true
		fmt.Printf("%s Found %d orphaned commit(s):\n\n", style.Warning.Render("⚠"), len(filtered))
		for _, o := range filtered {
			age := formatAge(o.Date)
			fmt.Printf("  %s %s\n", style.Bold.Render(shortHash(o.SHA)), o.Subject)
			fmt.Printf("    %s by %s\n\n", style.Dim.Render(age), o.Author)
		}
		fmt.Printf("%s\n", style.Dim.Render("To recover a commit:"))
		fmt.Printf("%s\n", style.Dim.Render("  git cherry-pick <sha>     # Apply to current branch"))
		fmt.Printf("%s\n", style.Dim.Render("  git show <sha>            # View full commit"))
		fmt.Printf("%s\n\n", style.Dim.Render("  git branch rescue <sha>   # Create branch from commit"))
	}

	// --- Unmerged polecat worktree branches ---
	defaultBranch := r.DefaultBranch()
	fmt.Printf("Scanning polecat worktrees for unmerged branches...\n\n")

	polecatBranches, skipped, err := findOrphanPolecatBranches(r.Path, rigName, defaultBranch)
	if err != nil {
		// Non-fatal: report the error but continue
		fmt.Printf("%s Could not scan polecat worktrees: %v\n\n", style.Dim.Render("ℹ"), err)
	} else if len(polecatBranches) > 0 {
		foundAnything = true
		fmt.Printf("%s Found %d unmerged polecat branch(es):\n\n", style.Warning.Render("⚠"), len(polecatBranches))
		for _, b := range polecatBranches {
			fmt.Printf("  %s %s (%d commit(s) ahead of %s)\n",
				style.Bold.Render(b.Polecat), b.Branch, b.AheadCount, defaultBranch)
			if b.LatestSubject != "" {
				fmt.Printf("    %s %s\n", style.Dim.Render("latest:"), b.LatestSubject)
			}
			if b.HasUncommitted {
				fmt.Printf("    %s\n", style.Warning.Render("has uncommitted changes"))
			}
			fmt.Printf("    %s %s\n", style.Dim.Render("path:"), b.WorktreePath)
			fmt.Println()
		}
		fmt.Printf("%s\n", style.Dim.Render("To recover polecat work:"))
		fmt.Printf("  %s\n", style.Dim.Render("cd <path>                   # Enter the worktree (see path above)"))
		fmt.Printf("  %s\n\n", style.Dim.Render(fmt.Sprintf("git log %s..HEAD        # View unmerged commits", defaultBranch)))
	}

	if len(skipped) > 0 {
		fmt.Printf("%s Skipped %d polecat(s) due to errors:\n", style.Warning.Render("⚠"), len(skipped))
		for _, s := range skipped {
			fmt.Printf("  %s: %s\n", s.Polecat, s.Err)
		}
		fmt.Println()
	}

	if !foundAnything {
		if len(orphans) > 0 && len(filtered) == 0 {
			fmt.Printf("%s No orphaned commits in the last %d days\n", style.Bold.Render("✓"), orphansDays)
			fmt.Printf("%s Use --days=N or --all to see older orphans\n", style.Dim.Render("Hint:"))
		} else {
			fmt.Printf("%s No orphaned work found\n", style.Bold.Render("✓"))
		}
	}

	return nil
}

// OrphanBranch represents a polecat worktree with unmerged work.
type OrphanBranch struct {
	Polecat        string // Polecat name
	Branch         string // Branch name
	AheadCount     int    // Commits ahead of default branch
	LatestSubject  string // Subject of the latest commit
	HasUncommitted bool   // Whether the worktree has uncommitted changes
	WorktreePath   string // Actual resolved worktree path
}

// skippedPolecat records a polecat that was skipped due to errors during scanning.
type skippedPolecat struct {
	Polecat string
	Err     string
}

// resolvePolecatWorktree determines the worktree path for a polecat,
// mirroring the canonical clonePath logic in polecat/session_manager.go.
func resolvePolecatWorktree(polecatsDir, polecatName, rigName string) string {
	// New structure: polecats/<name>/<rigname>/
	newPath := filepath.Join(polecatsDir, polecatName, rigName)
	if info, err := os.Stat(newPath); err == nil && info.IsDir() {
		return newPath
	}

	// Old structure: polecats/<name>/ (backward compat)
	oldPath := filepath.Join(polecatsDir, polecatName)
	if info, err := os.Stat(oldPath); err == nil && info.IsDir() {
		gitPath := filepath.Join(oldPath, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			return oldPath
		}
	}

	return "" // No valid worktree found
}

// findOrphanPolecatBranches scans polecat worktrees for branches with
// commits that have not been merged to the default branch.
func findOrphanPolecatBranches(rigPath, rigName, defaultBranch string) ([]OrphanBranch, []skippedPolecat, error) {
	polecatsDir := filepath.Join(rigPath, constants.DirPolecats)
	entries, err := os.ReadDir(polecatsDir)
	if os.IsNotExist(err) {
		return nil, nil, nil // No polecats directory
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading polecats dir: %w", err)
	}

	var branches []OrphanBranch
	var skipped []skippedPolecat

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		polecatName := entry.Name()

		worktreePath := resolvePolecatWorktree(polecatsDir, polecatName, rigName)
		if worktreePath == "" {
			continue // No valid git worktree
		}

		g := gitpkg.NewGit(worktreePath)

		// Get current branch
		branch, err := g.CurrentBranch()
		if err != nil {
			skipped = append(skipped, skippedPolecat{polecatName, fmt.Sprintf("cannot determine branch: %v", err)})
			continue
		}
		branch = strings.TrimSpace(branch)
		if branch == "" || branch == "HEAD" || branch == defaultBranch {
			continue // On default branch or detached HEAD — nothing unmerged
		}

		// Count commits ahead of default branch (try local ref, then origin/)
		baseRef := defaultBranch
		revListCmd := exec.Command("git", "-C", worktreePath, "rev-list", "--count", baseRef+"..HEAD")
		countOut, err := revListCmd.Output()
		if err != nil {
			baseRef = "origin/" + defaultBranch
			revListCmd = exec.Command("git", "-C", worktreePath, "rev-list", "--count", baseRef+"..HEAD")
			countOut, err = revListCmd.Output()
			if err != nil {
				skipped = append(skipped, skippedPolecat{polecatName, fmt.Sprintf("rev-list failed: %v", err)})
				continue
			}
		}
		count, err := strconv.Atoi(strings.TrimSpace(string(countOut)))
		if err != nil || count == 0 {
			continue // No commits ahead
		}

		// Get the latest commit subject
		logCmd := exec.Command("git", "-C", worktreePath, "log", "-1", "--format=%s")
		logOut, err := logCmd.Output()
		latestSubject := ""
		if err != nil {
			skipped = append(skipped, skippedPolecat{polecatName, fmt.Sprintf("git log failed: %v", err)})
			continue
		}
		latestSubject = strings.TrimSpace(string(logOut))

		// Check for uncommitted changes
		gitStatus, err := g.Status()
		hasUncommitted := false
		if err != nil {
			skipped = append(skipped, skippedPolecat{polecatName, fmt.Sprintf("git status failed: %v", err)})
			continue
		}
		hasUncommitted = !gitStatus.Clean

		branches = append(branches, OrphanBranch{
			Polecat:        polecatName,
			Branch:         branch,
			AheadCount:     count,
			LatestSubject:  latestSubject,
			HasUncommitted: hasUncommitted,
			WorktreePath:   worktreePath,
		})
	}

	return branches, skipped, nil
}

// findOrphanCommits runs git fsck and parses orphaned commits
func findOrphanCommits(repoPath string) ([]OrphanCommit, error) {
	// Run git fsck to find unreachable objects
	fsckCmd := exec.Command("git", "fsck", "--unreachable", "--no-reflogs")
	fsckCmd.Dir = repoPath

	var fsckOut, fsckErr bytes.Buffer
	fsckCmd.Stdout = &fsckOut
	fsckCmd.Stderr = &fsckErr

	if err := fsckCmd.Run(); err != nil {
		// git fsck returns non-zero if there are issues, but we still get output
		// Only fail if we got no output at all
		if fsckOut.Len() == 0 {
			// Include stderr in error message for debugging
			errMsg := strings.TrimSpace(fsckErr.String())
			if errMsg != "" {
				return nil, fmt.Errorf("git fsck failed: %w (%s)", err, errMsg)
			}
			return nil, fmt.Errorf("git fsck failed: %w", err)
		}
	}

	// Parse commit SHAs from output
	var commitSHAs []string
	scanner := bufio.NewScanner(&fsckOut)
	for scanner.Scan() {
		line := scanner.Text()
		// Format: "unreachable commit <sha>"
		if strings.HasPrefix(line, "unreachable commit ") {
			sha := strings.TrimPrefix(line, "unreachable commit ")
			commitSHAs = append(commitSHAs, sha)
		}
	}

	if len(commitSHAs) == 0 {
		return nil, nil
	}

	// Get details for each commit
	var orphans []OrphanCommit
	for _, sha := range commitSHAs {
		commit, err := getCommitDetails(repoPath, sha)
		if err != nil {
			continue // Skip commits we can't parse
		}

		// Skip stash-like and routine sync commits
		if isNoiseCommit(commit.Subject) {
			continue
		}

		orphans = append(orphans, commit)
	}

	return orphans, nil
}

// getCommitDetails retrieves commit metadata
func getCommitDetails(repoPath, sha string) (OrphanCommit, error) {
	// Format: timestamp|author|subject
	cmd := exec.Command("git", "log", "-1", "--format=%at|%an|%s", sha)
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		return OrphanCommit{}, err
	}

	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 3)
	if len(parts) < 3 {
		return OrphanCommit{}, fmt.Errorf("unexpected format")
	}

	timestamp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return OrphanCommit{}, err
	}

	return OrphanCommit{
		SHA:     sha,
		Date:    time.Unix(timestamp, 0),
		Author:  parts[1],
		Subject: parts[2],
	}, nil
}

// isNoiseCommit returns true for stash-related or routine sync commits
func isNoiseCommit(subject string) bool {
	// Git stash creates commits with these prefixes
	noisePrefixes := []string{
		"WIP on ",
		"index on ",
		"On ",              // "On branch: message"
		"stash@{",          // Direct stash reference
		"untracked files ", // Stash with untracked
	}

	for _, prefix := range noisePrefixes {
		if strings.HasPrefix(subject, prefix) {
			return true
		}
	}

	return false
}

// formatAge returns a human-readable age string
func formatAge(t time.Time) string {
	d := time.Since(t)

	if d < time.Hour {
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
	days := int(d.Hours() / 24)
	if days == 1 {
		return "1 day ago"
	}
	return fmt.Sprintf("%d days ago", days)
}

// runOrphansKill removes orphaned commits and kills orphaned processes
func runOrphansKill(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Find rig: use --rig flag if provided, otherwise infer from cwd
	var rigName string
	var r *rig.Rig
	if orphansRig != "" {
		_, r, err = getRig(orphansRig)
		if err != nil {
			return err
		}
		rigName = orphansRig
	} else {
		rigName, r, err = findCurrentRig(townRoot)
		if err != nil {
			return fmt.Errorf("not in a rig directory. Use --rig <name> to specify the target rig, or run from within a rig directory")
		}
	}

	mayorPath := r.Path + "/mayor/rig"

	// Find orphaned commits
	fmt.Printf("Scanning for orphaned commits in %s...\n", rigName)
	commitOrphans, err := findOrphanCommits(mayorPath)
	if err != nil {
		return fmt.Errorf("finding orphan commits: %w", err)
	}

	// Filter commits by date
	cutoff := time.Now().AddDate(0, 0, -orphansKillDays)
	var filteredCommits []OrphanCommit
	for _, o := range commitOrphans {
		if orphansKillAll || o.Date.After(cutoff) {
			filteredCommits = append(filteredCommits, o)
		}
	}

	// Find orphaned processes
	fmt.Printf("Scanning for orphaned agent processes...\n\n")
	procOrphans, err := findOrphanProcesses()
	if err != nil {
		return fmt.Errorf("finding orphan processes: %w", err)
	}

	// Check if there's anything to do
	if len(filteredCommits) == 0 && len(procOrphans) == 0 {
		fmt.Printf("%s No orphans found\n", style.Bold.Render("✓"))
		return nil
	}

	// Show orphaned commits
	if len(filteredCommits) > 0 {
		fmt.Printf("%s Found %d orphaned commit(s) to remove:\n\n", style.Warning.Render("⚠"), len(filteredCommits))
		for _, o := range filteredCommits {
			fmt.Printf("  %s %s\n", style.Bold.Render(shortHash(o.SHA)), o.Subject)
			fmt.Printf("    %s by %s\n\n", style.Dim.Render(formatAge(o.Date)), o.Author)
		}
	} else if len(commitOrphans) > 0 {
		fmt.Printf("%s No orphaned commits in the last %d days (use --days=N or --all)\n\n",
			style.Dim.Render("ℹ"), orphansKillDays)
	}

	// Show orphaned processes
	if len(procOrphans) > 0 {
		fmt.Printf("%s Found %d orphaned agent process(es) to kill:\n\n", style.Warning.Render("⚠"), len(procOrphans))
		for _, o := range procOrphans {
			displayArgs := o.Args
			if len(displayArgs) > 80 {
				displayArgs = displayArgs[:77] + "..."
			}
			fmt.Printf("  %s %s\n", style.Bold.Render(fmt.Sprintf("PID %d", o.PID)), displayArgs)
		}
		fmt.Println()
	}

	if orphansKillDryRun {
		fmt.Printf("%s Dry run - no changes made\n", style.Dim.Render("ℹ"))
		return nil
	}

	// Confirmation
	if !orphansKillForce {
		fmt.Printf("%s\n", style.Warning.Render("WARNING: This operation is irreversible!"))
		total := len(filteredCommits) + len(procOrphans)
		fmt.Printf("Remove %d orphan(s)? [y/N] ", total)
		var response string
		_, _ = fmt.Scanln(&response)
		if strings.ToLower(strings.TrimSpace(response)) != "y" {
			fmt.Printf("%s Canceled\n", style.Dim.Render("ℹ"))
			return nil
		}
	}

	// Kill orphaned commits
	if len(filteredCommits) > 0 {
		fmt.Printf("\nRunning git gc --prune=now...\n")
		gcCmd := exec.Command("git", "gc", "--prune=now")
		gcCmd.Dir = mayorPath
		gcCmd.Stdout = os.Stdout
		gcCmd.Stderr = os.Stderr
		if err := gcCmd.Run(); err != nil {
			return fmt.Errorf("git gc failed: %w", err)
		}
		fmt.Printf("%s Removed %d orphaned commit(s)\n", style.Bold.Render("✓"), len(filteredCommits))
	}

	// Kill orphaned processes
	if len(procOrphans) > 0 {
		fmt.Printf("\nKilling orphaned processes...\n")
		// Use SIGKILL with --force for immediate termination, SIGTERM otherwise
		signal := syscall.SIGTERM
		if orphansKillForce {
			signal = syscall.SIGKILL
		}

		var killed, failed int
		for _, o := range procOrphans {
			proc, err := os.FindProcess(o.PID)
			if err != nil {
				fmt.Printf("  %s PID %d: %v\n", style.Error.Render("✗"), o.PID, err)
				failed++
				continue
			}

			if err := proc.Signal(signal); err != nil {
				if err == os.ErrProcessDone {
					fmt.Printf("  %s PID %d: already terminated\n", style.Dim.Render("○"), o.PID)
					continue
				}
				fmt.Printf("  %s PID %d: %v\n", style.Error.Render("✗"), o.PID, err)
				failed++
				continue
			}

			fmt.Printf("  %s PID %d killed\n", style.Bold.Render("✓"), o.PID)
			killed++
		}

		fmt.Printf("%s %d process(es) killed", style.Bold.Render("✓"), killed)
		if failed > 0 {
			fmt.Printf(", %d failed", failed)
		}
		fmt.Println()
	}

	fmt.Printf("\n%s Orphan cleanup complete\n", style.Bold.Render("✓"))
	return nil
}

// OrphanProcess represents an agent process that has become orphaned (PPID=1)
type OrphanProcess struct {
	PID  int
	Args string
}

// findOrphanProcesses finds agent processes with PPID=1 (orphaned)
func findOrphanProcesses() ([]OrphanProcess, error) {
	// Run ps to get all processes with PID, PPID, and args
	cmd := exec.Command("ps", "-eo", "pid,ppid,args")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("running ps: %w", err)
	}

	var orphans []OrphanProcess
	scanner := bufio.NewScanner(bytes.NewReader(out))

	// Skip header line
	if scanner.Scan() {
		// First line is header, skip it
	}

	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}

		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}

		// Only interested in orphans (PPID=1)
		if ppid != 1 {
			continue
		}

		// Reconstruct the args (rest of the fields)
		args := strings.Join(fields[2:], " ")

		// Check if it's a claude-related process
		if !isClaudeProcess(args) {
			continue
		}

		// Exclude processes we don't want to kill
		if isExcludedProcess(args) {
			continue
		}

		orphans = append(orphans, OrphanProcess{
			PID:  pid,
			Args: args,
		})
	}

	return orphans, nil
}

// isClaudeProcess checks if the process is claude-related
func isClaudeProcess(args string) bool {
	argsLower := strings.ToLower(args)
	return strings.Contains(argsLower, "claude")
}

// isExcludedProcess checks if the process should be excluded from orphan list
func isExcludedProcess(args string) bool {
	// Exclude any tmux process (server, new-session, etc.)
	// These may contain agent names in args but are tmux processes, not actual agent processes
	if strings.HasPrefix(args, "tmux ") || strings.HasPrefix(args, "/usr/bin/tmux") {
		return true
	}

	// Exclude Claude.app desktop application processes
	if strings.Contains(args, "Claude.app") || strings.Contains(args, "/Applications/Claude") {
		return true
	}

	// Exclude Claude Helper processes (part of Claude.app)
	if strings.Contains(args, "Claude Helper") {
		return true
	}

	return false
}

// runOrphansListProcesses lists orphaned agent processes
func runOrphansListProcesses(cmd *cobra.Command, args []string) error {
	if orphansProcsAggressive {
		return runOrphansListProcessesAggressive()
	}

	orphans, err := findOrphanProcesses()
	if err != nil {
		return fmt.Errorf("finding orphan processes: %w", err)
	}

	if len(orphans) == 0 {
		fmt.Printf("%s No orphaned agent processes found (PPID=1)\n", style.Bold.Render("✓"))
		fmt.Printf("%s Use --aggressive to find orphans via tmux session verification\n", style.Dim.Render("Hint:"))
		return nil
	}

	fmt.Printf("%s Found %d orphaned agent process(es) with PPID=1:\n\n", style.Warning.Render("⚠"), len(orphans))

	for _, o := range orphans {
		// Truncate args for display
		displayArgs := o.Args
		if len(displayArgs) > 80 {
			displayArgs = displayArgs[:77] + "..."
		}
		fmt.Printf("  %s %s\n", style.Bold.Render(fmt.Sprintf("PID %d", o.PID)), displayArgs)
	}

	fmt.Printf("\n%s\n", style.Dim.Render("Use 'gt orphans procs kill' to terminate these processes"))
	fmt.Printf("%s\n", style.Dim.Render("Use --aggressive to find more orphans via tmux session verification"))

	return nil
}

// runOrphansListProcessesAggressive lists orphans using tmux session verification.
// This finds ALL tracked agent processes not in any gt-* or hq-* tmux session.
func runOrphansListProcessesAggressive() error {
	zombies, err := util.FindZombieClaudeProcesses()
	if err != nil {
		return fmt.Errorf("finding zombie processes: %w", err)
	}

	if len(zombies) == 0 {
		fmt.Printf("%s No orphaned agent processes found (aggressive mode)\n", style.Bold.Render("✓"))
		return nil
	}

	fmt.Printf("%s Found %d orphaned agent process(es) not in any tmux session:\n\n", style.Warning.Render("⚠"), len(zombies))

	for _, z := range zombies {
		ageStr := formatProcessAge(z.Age)
		fmt.Printf("  %s %s (age: %s, tty: %s)\n",
			style.Bold.Render(fmt.Sprintf("PID %d", z.PID)),
			z.Cmd,
			style.Dim.Render(ageStr),
			z.TTY)
	}

	fmt.Printf("\n%s\n", style.Dim.Render("Use 'gt orphans procs kill --aggressive' to terminate these processes"))

	return nil
}

// formatProcessAge formats seconds into a human-readable age string
func formatProcessAge(seconds int) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm%ds", seconds/60, seconds%60)
	}
	hours := seconds / 3600
	mins := (seconds % 3600) / 60
	return fmt.Sprintf("%dh%dm", hours, mins)
}

// runOrphansKillProcesses kills orphaned agent processes
func runOrphansKillProcesses(cmd *cobra.Command, args []string) error {
	if orphansProcsAggressive {
		return runOrphansKillProcessesAggressive()
	}

	orphans, err := findOrphanProcesses()
	if err != nil {
		return fmt.Errorf("finding orphan processes: %w", err)
	}

	if len(orphans) == 0 {
		fmt.Printf("%s No orphaned agent processes found (PPID=1)\n", style.Bold.Render("✓"))
		fmt.Printf("%s Use --aggressive to find orphans via tmux session verification\n", style.Dim.Render("Hint:"))
		return nil
	}

	// Show what we're about to kill
	fmt.Printf("%s Found %d orphaned agent process(es) with PPID=1:\n\n", style.Warning.Render("⚠"), len(orphans))
	for _, o := range orphans {
		displayArgs := o.Args
		if len(displayArgs) > 80 {
			displayArgs = displayArgs[:77] + "..."
		}
		fmt.Printf("  %s %s\n", style.Bold.Render(fmt.Sprintf("PID %d", o.PID)), displayArgs)
	}
	fmt.Println()

	// Confirm unless --force
	if !orphansProcsForce {
		fmt.Printf("Kill these %d process(es)? [y/N] ", len(orphans))
		var response string
		_, _ = fmt.Scanln(&response)
		response = strings.ToLower(strings.TrimSpace(response))
		if response != "y" && response != "yes" {
			fmt.Println("Aborted")
			return nil
		}
	}

	// Kill the processes
	// Use SIGKILL with --force for immediate termination, SIGTERM otherwise
	signal := syscall.SIGTERM
	if orphansProcsForce {
		signal = syscall.SIGKILL
	}

	var killed, failed int
	for _, o := range orphans {
		proc, err := os.FindProcess(o.PID)
		if err != nil {
			fmt.Printf("  %s PID %d: %v\n", style.Error.Render("✗"), o.PID, err)
			failed++
			continue
		}

		if err := proc.Signal(signal); err != nil {
			// Process may have already exited
			if err == os.ErrProcessDone {
				fmt.Printf("  %s PID %d: already terminated\n", style.Dim.Render("○"), o.PID)
				continue
			}
			fmt.Printf("  %s PID %d: %v\n", style.Error.Render("✗"), o.PID, err)
			failed++
			continue
		}

		fmt.Printf("  %s PID %d killed\n", style.Bold.Render("✓"), o.PID)
		killed++
	}

	fmt.Printf("\n%s %d killed", style.Bold.Render("Summary:"), killed)
	if failed > 0 {
		fmt.Printf(", %d failed", failed)
	}
	fmt.Println()

	return nil
}

// runOrphansKillProcessesAggressive kills orphans using tmux session verification.
// This kills ALL tracked agent processes not in any gt-* or hq-* tmux session.
func runOrphansKillProcessesAggressive() error {
	zombies, err := util.FindZombieClaudeProcesses()
	if err != nil {
		return fmt.Errorf("finding zombie processes: %w", err)
	}

	if len(zombies) == 0 {
		fmt.Printf("%s No orphaned agent processes found (aggressive mode)\n", style.Bold.Render("✓"))
		return nil
	}

	// Show what we're about to kill
	fmt.Printf("%s Found %d orphaned agent process(es) not in any tmux session:\n\n", style.Warning.Render("⚠"), len(zombies))
	for _, z := range zombies {
		ageStr := formatProcessAge(z.Age)
		fmt.Printf("  %s %s (age: %s, tty: %s)\n",
			style.Bold.Render(fmt.Sprintf("PID %d", z.PID)),
			z.Cmd,
			style.Dim.Render(ageStr),
			z.TTY)
	}
	fmt.Println()

	// Confirm unless --force
	if !orphansProcsForce {
		fmt.Printf("Kill these %d process(es)? [y/N] ", len(zombies))
		var response string
		_, _ = fmt.Scanln(&response)
		response = strings.ToLower(strings.TrimSpace(response))
		if response != "y" && response != "yes" {
			fmt.Println("Aborted")
			return nil
		}
	}

	// Kill the processes
	// Use SIGKILL with --force for immediate termination, SIGTERM otherwise
	signal := syscall.SIGTERM
	if orphansProcsForce {
		signal = syscall.SIGKILL
	}

	var killed, failed int
	for _, z := range zombies {
		proc, err := os.FindProcess(z.PID)
		if err != nil {
			fmt.Printf("  %s PID %d: %v\n", style.Error.Render("✗"), z.PID, err)
			failed++
			continue
		}

		if err := proc.Signal(signal); err != nil {
			// Process may have already exited
			if err == os.ErrProcessDone {
				fmt.Printf("  %s PID %d: already terminated\n", style.Dim.Render("○"), z.PID)
				continue
			}
			fmt.Printf("  %s PID %d: %v\n", style.Error.Render("✗"), z.PID, err)
			failed++
			continue
		}

		fmt.Printf("  %s PID %d killed\n", style.Bold.Render("✓"), z.PID)
		killed++
	}

	fmt.Printf("\n%s %d killed", style.Bold.Render("Summary:"), killed)
	if failed > 0 {
		fmt.Printf(", %d failed", failed)
	}
	fmt.Println()

	return nil
}
