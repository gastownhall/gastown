package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/util"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Polecat command flags
var (
	polecatListJSON  bool
	polecatListAll   bool
	polecatForce     bool
	polecatRemoveAll bool
)

var polecatCmd = &cobra.Command{
	Use:     "polecat",
	Aliases: []string{"polecats"},
	GroupID: GroupAgents,
	Short:   "Manage polecats (persistent identity, ephemeral sessions)",
	RunE:    requireSubcommand,
	Long: `Manage polecat lifecycle in rigs.

Polecats have PERSISTENT IDENTITY but EPHEMERAL SESSIONS. Each polecat has
a permanent agent bead and CV chain that accumulates work history across
assignments. Sessions and sandboxes are ephemeral — spawned for specific
tasks, cleaned up on completion — but the identity persists.

A polecat is either:
  - Working: Actively doing assigned work
  - Stalled: Session crashed mid-work (needs Witness intervention)
  - Zombie: Finished but gt done failed (needs cleanup)
  - Nuked: Session ended, identity persists (ready for next assignment)

Self-cleaning model: When work completes, the polecat runs 'gt done',
which pushes the branch, submits to the merge queue, and exits. The
Witness then nukes the sandbox. The polecat's identity (agent bead)
persists with agent_state=nuked, preserving work history.

Session vs sandbox: The Claude session cycles frequently (handoffs,
compaction). The git worktree (sandbox) persists until nuke. Work
survives session restarts.

Cats build features. Dogs clean up messes.`,
}

var polecatListCmd = &cobra.Command{
	Use:   "list [rig]",
	Short: "List polecats in a rig",
	Long: `List polecats in a rig or all rigs.

In the transient model, polecats exist only while working. The list shows
all polecats with their states:
  - working: Actively working on an issue
  - done: Completed work, waiting for cleanup
  - stuck: Needs assistance

Examples:
  gt polecat list greenplace
  gt polecat list --all
  gt polecat list greenplace --json`,
	RunE: runPolecatList,
}

var polecatAddCmd = &cobra.Command{
	Use:        "add <rig> <name>",
	Short:      "Add a new polecat to a rig (DEPRECATED)",
	Deprecated: "use 'gt polecat identity add' instead. This command will be removed in v1.0.",
	Long: `Add a new polecat to a rig.

DEPRECATED: Use 'gt polecat identity add' instead. This command will be removed in v1.0.

Creates a polecat directory, clones the rig repo, creates a work branch,
and initializes state.

Example:
  gt polecat identity add greenplace Toast  # Preferred
  gt polecat add greenplace Toast           # Deprecated`,
	Args: cobra.ExactArgs(2),
	RunE: runPolecatAdd,
}

var polecatRemoveCmd = &cobra.Command{
	Use:   "remove <rig>/<polecat>... | <rig> --all",
	Short: "Remove polecats from a rig",
	Long: `Remove one or more polecats from a rig.

Fails if session is running (stop first).
Warns if uncommitted changes exist.
Use --force to bypass checks.

Examples:
  gt polecat remove greenplace/Toast
  gt polecat remove greenplace/Toast greenplace/Furiosa
  gt polecat remove greenplace --all
  gt polecat remove greenplace --all --force`,
	Args: cobra.MinimumNArgs(1),
	RunE: runPolecatRemove,
}

var polecatStatusCmd = &cobra.Command{
	Use:   "status <rig>/<polecat>",
	Short: "Show detailed status for a polecat",
	Long: `Show detailed status for a polecat.

Displays comprehensive information including:
  - Current lifecycle state (working, done, stuck, idle)
  - Assigned issue (if any)
  - Session status (running/stopped, attached/detached)
  - Session creation time
  - Last activity time

NOTE: The argument is <rig>/<polecat> — a single argument with a slash
separator, NOT two separate arguments. For example: greenplace/Toast

Examples:
  gt polecat status greenplace/Toast
  gt polecat status greenplace/Toast --json`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatStatus,
}

var (
	polecatStatusJSON                    bool
	polecatGitStateJSON                  bool
	polecatGCDryRun                      bool
	polecatNukeAll                       bool
	polecatNukeDryRun                    bool
	polecatNukeForce                     bool
	polecatCheckRecoveryJSON             bool
	polecatCheckRecoveryReconcileCleanup bool
	polecatPoolInitDryRun                bool
	polecatPoolInitSize                  int
)

var polecatGCCmd = &cobra.Command{
	Use:   "gc <rig>",
	Short: "Garbage collect stale polecat branches",
	Long: `Garbage collect stale polecat branches in a rig.

Polecats use unique timestamped branches (polecat/<name>-<timestamp>) to
prevent drift issues. Over time, these branches accumulate when stale
polecats are repaired.

This command removes orphaned branches:
  - Branches for polecats that no longer exist
  - Old timestamped branches (keeps only the current one per polecat)

Examples:
  gt polecat gc greenplace
  gt polecat gc greenplace --dry-run`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatGC,
}

var polecatNukeCmd = &cobra.Command{
	Use:   "nuke <rig>/<polecat>... | <rig> --all",
	Short: "Completely destroy a polecat (session, worktree, branch, agent bead)",
	Long: `Completely destroy a polecat and all its artifacts.

This is the nuclear option for post-merge cleanup. It:
  1. Kills the Claude session (if running)
  2. Deletes the git worktree (bypassing all safety checks)
  3. Deletes the polecat branch
  4. Closes the agent bead (if exists)

SAFETY CHECKS: The command refuses to nuke a polecat if:
  - cleanup_status is dirty, unknown, or missing
  - Worktree fallback detects unpushed/uncommitted/stashed changes
  - Polecat has an open merge request (MR bead or active_mr)
  - Polecat has work on its hook

Use --force to bypass safety checks (LOSES WORK).
Use --dry-run to see what would happen and safety check status.

Examples:
  gt polecat nuke greenplace/Toast
  gt polecat nuke greenplace/Toast greenplace/Furiosa
  gt polecat nuke greenplace --all
  gt polecat nuke greenplace --all --dry-run
  gt polecat nuke greenplace/Toast --force  # bypass safety checks`,
	Args: cobra.MinimumNArgs(1),
	RunE: runPolecatNuke,
}

var polecatGitStateCmd = &cobra.Command{
	Use:   "git-state <rig>/<polecat>",
	Short: "Show git state for pre-kill verification",
	Long: `Show git state for a polecat's worktree.

Used by the Witness for pre-kill verification to ensure no work is lost.
Returns whether the worktree is clean (safe to kill) or dirty (needs cleanup).

Checks:
  - Working tree: uncommitted changes
  - Unpushed commits: commits ahead of origin/main
  - Stashes: stashed changes

Examples:
  gt polecat git-state greenplace/Toast
  gt polecat git-state greenplace/Toast --json`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatGitState,
}

var polecatCheckRecoveryCmd = &cobra.Command{
	Use:   "check-recovery <rig>/<polecat>",
	Short: "Check if polecat needs recovery vs safe to nuke",
	Long: `Check recovery status of a polecat based on cleanup_status, active_mr, and merge queue state.

Used by the Witness to determine appropriate cleanup action:
  - SAFE_TO_NUKE: cleanup_status is 'clean', active_mr is terminal, AND work submitted to merge queue
  - NEEDS_MQ_SUBMIT: git is clean but work was never submitted to the merge queue
  - NEEDS_RECOVERY: cleanup_status, active_mr, or fallback git predicates require recovery

This prevents accidental data loss when cleaning up dormant polecats.
The Witness should escalate NEEDS_RECOVERY and NEEDS_MQ_SUBMIT cases to the Mayor.

Examples:
  gt polecat check-recovery greenplace/Toast
  gt polecat check-recovery greenplace/Toast --json`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatCheckRecovery,
}

var (
	polecatStaleJSON      bool
	polecatStaleThreshold int
	polecatStaleCleanup   bool
	polecatStaleDryRun    bool
	polecatPruneDryRun    bool
	polecatPruneRemote    bool
)

var polecatStaleCmd = &cobra.Command{
	Use:   "stale <rig>",
	Short: "Detect stale polecats that may need cleanup",
	Long: `Detect stale polecats in a rig that are candidates for cleanup.

A polecat is considered stale if:
  - No active tmux session
  - Way behind main (>threshold commits) OR no agent bead
  - Has no uncommitted work that could be lost

The default threshold is 20 commits behind main.

Use --cleanup to automatically nuke stale polecats that are safe to remove.
Use --dry-run with --cleanup to see what would be cleaned.

Examples:
  gt polecat stale greenplace
  gt polecat stale greenplace --threshold 50
  gt polecat stale greenplace --json
  gt polecat stale greenplace --cleanup
  gt polecat stale greenplace --cleanup --dry-run`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatStale,
}

var polecatPruneCmd = &cobra.Command{
	Use:   "prune <rig>",
	Short: "Prune stale polecat branches (local and remote)",
	Long: `Prune stale polecat branches in a rig.

Finds and deletes polecat branches that are no longer needed:
  - Branches fully merged to main
  - Branches whose remote tracking branch was deleted (post-merge cleanup)
  - Branches for polecats that no longer exist (orphaned)

Uses safe deletion (git branch -d) — only removes fully merged branches.
Also cleans up remote polecat branches that are fully merged.

Use --dry-run to preview what would be pruned.
Use --remote to also prune remote polecat branches on origin.

Examples:
  gt polecat prune greenplace
  gt polecat prune greenplace --dry-run
  gt polecat prune greenplace --remote`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatPrune,
}

var polecatPoolInitCmd = &cobra.Command{
	Use:   "pool-init <rig>",
	Short: "Initialize a persistent polecat pool for a rig",
	Long: `Initialize a persistent polecat pool for a rig.

Creates N polecats with identities and worktrees in IDLE state,
ready for immediate work assignment via gt sling.

Pool size is determined by (in priority order):
  1. --size flag
  2. polecat_pool_size in rig config.json
  3. Default: 4

Polecat names come from:
  1. polecat_names in rig config.json (if specified)
  2. The rig's name pool theme (default: mad-max)

Existing polecats are preserved — only new ones are created
to reach the target pool size.

Examples:
  gt polecat pool-init gastown
  gt polecat pool-init gastown --size 6
  gt polecat pool-init gastown --dry-run`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatPoolInit,
}

func init() {
	// List flags
	polecatListCmd.Flags().BoolVar(&polecatListJSON, "json", false, "Output as JSON")
	polecatListCmd.Flags().BoolVar(&polecatListAll, "all", false, "List polecats in all rigs")

	// Remove flags
	polecatRemoveCmd.Flags().BoolVarP(&polecatForce, "force", "f", false, "Force removal, bypassing checks")
	polecatRemoveCmd.Flags().BoolVar(&polecatRemoveAll, "all", false, "Remove all polecats in the rig")

	// Status flags
	polecatStatusCmd.Flags().BoolVar(&polecatStatusJSON, "json", false, "Output as JSON")

	// Git-state flags
	polecatGitStateCmd.Flags().BoolVar(&polecatGitStateJSON, "json", false, "Output as JSON")

	// GC flags
	polecatGCCmd.Flags().BoolVar(&polecatGCDryRun, "dry-run", false, "Show what would be deleted without deleting")

	// Nuke flags
	polecatNukeCmd.Flags().BoolVar(&polecatNukeAll, "all", false, "Nuke all polecats in the rig")
	polecatNukeCmd.Flags().BoolVar(&polecatNukeDryRun, "dry-run", false, "Show what would be nuked without doing it")
	polecatNukeCmd.Flags().BoolVarP(&polecatNukeForce, "force", "f", false, "Force nuke, bypassing all safety checks (LOSES WORK)")

	// Check-recovery flags
	polecatCheckRecoveryCmd.Flags().BoolVar(&polecatCheckRecoveryJSON, "json", false, "Output as JSON")
	polecatCheckRecoveryCmd.Flags().BoolVar(&polecatCheckRecoveryReconcileCleanup, "reconcile-cleanup", false, "Safely rewrite stale cleanup_status=clean when live recovery predicates prove no work is at risk")

	// Stale flags
	polecatStaleCmd.Flags().BoolVar(&polecatStaleJSON, "json", false, "Output as JSON")
	polecatStaleCmd.Flags().IntVar(&polecatStaleThreshold, "threshold", 20, "Commits behind main to consider stale")
	polecatStaleCmd.Flags().BoolVar(&polecatStaleCleanup, "cleanup", false, "Automatically nuke stale polecats")
	polecatStaleCmd.Flags().BoolVar(&polecatStaleDryRun, "dry-run", false, "Show what would be cleaned without doing it")

	// Prune flags
	polecatPruneCmd.Flags().BoolVar(&polecatPruneDryRun, "dry-run", false, "Show what would be pruned without doing it")
	polecatPruneCmd.Flags().BoolVar(&polecatPruneRemote, "remote", false, "Also prune remote polecat branches on origin")

	// Pool-init flags
	polecatPoolInitCmd.Flags().BoolVar(&polecatPoolInitDryRun, "dry-run", false, "Show what would be created without doing it")
	polecatPoolInitCmd.Flags().IntVar(&polecatPoolInitSize, "size", 0, "Pool size (overrides rig config)")

	// Add subcommands
	polecatCmd.AddCommand(polecatListCmd)
	polecatCmd.AddCommand(polecatAddCmd)
	polecatCmd.AddCommand(polecatRemoveCmd)
	polecatCmd.AddCommand(polecatStatusCmd)
	polecatCmd.AddCommand(polecatGitStateCmd)
	polecatCmd.AddCommand(polecatCheckRecoveryCmd)
	polecatCmd.AddCommand(polecatGCCmd)
	polecatCmd.AddCommand(polecatNukeCmd)
	polecatCmd.AddCommand(polecatStaleCmd)
	polecatCmd.AddCommand(polecatPruneCmd)
	polecatCmd.AddCommand(polecatPoolInitCmd)

	rootCmd.AddCommand(polecatCmd)
}

// PolecatListItem represents a polecat in list output.
type PolecatListItem struct {
	Rig                  string        `json:"rig"`
	Name                 string        `json:"name"`
	State                polecat.State `json:"state"`
	Issue                string        `json:"issue,omitempty"`
	CleanupStatus        string        `json:"cleanup_status,omitempty"`
	ActiveMR             string        `json:"active_mr,omitempty"`
	Branch               string        `json:"branch,omitempty"`
	Verdict              string        `json:"verdict,omitempty"`
	Reason               string        `json:"reason,omitempty"`
	Reusable             bool          `json:"reusable"`
	SafeToNuke           bool          `json:"safe_to_nuke"`
	NeedsRecovery        bool          `json:"needs_recovery"`
	NeedsMQSubmit        bool          `json:"needs_mq_submit"`
	MQStatus             string        `json:"mq_status,omitempty"`
	CountsTowardCapacity bool          `json:"counts_toward_capacity"`
	ReuseStatus          string        `json:"reuse_status,omitempty"`
	SessionRunning       bool          `json:"session_running"`
	Zombie               bool          `json:"zombie,omitempty"`
	SessionName          string        `json:"session_name,omitempty"`
}

// effectivePolecatState returns the observable state used by polecat list output.
// Active work is ground truth for working; tmux liveness alone is not enough
// because persistent polecats may keep a reusable live session after completion.
// Zombie entries are never auto-rewritten.
func effectivePolecatState(item PolecatListItem) polecat.State {
	state := item.State
	// A running session only implies working when there is active work attached.
	// Without an issue, rewriting idle/done to working recreates "Issue: (none)".
	if item.SessionRunning && item.Issue != "" && (state == polecat.StateDone || state == polecat.StateIdle) {
		return polecat.StateWorking
	}
	// When session is dead but beads still says "working", mark as stalled
	// (not done — work was interrupted, not completed). The manager's loadFromBeads
	// now returns StateStalled for this case, but list reconciliation may override.
	if !item.SessionRunning && !item.Zombie && state == polecat.StateWorking {
		return polecat.StateStalled
	}
	return state
}

type reuseMRShower interface {
	Show(issueID string) (*beads.Issue, error)
}

func activeMRBlocksReuse(bd reuseMRShower, mrID, sourceHint string, requireGitSafe, gitSafe bool) bool {
	assessment := polecat.AssessActiveMR(bd, polecat.ActiveMRInput{ActiveMR: mrID, SourceIssueHint: sourceHint, RequireGitSafe: requireGitSafe, GitSafe: gitSafe})
	return assessment.Pending
}

func polecatReuseStatus(state polecat.State, cleanupStatus, activeMR, branch string, activeMRBlocks, staleCleanupSafe bool) string {
	input := polecat.WorkstateInput{State: state, CleanupStatus: polecat.CleanupStatus(cleanupStatus), ActiveMR: activeMR, Branch: branch}
	if activeMRBlocks {
		input.ActiveMRBlocker = "active_mr=" + activeMR + " status=open"
	}
	if staleCleanupSafe {
		input.IgnoreCleanupStatus = true
	}
	return polecat.DecideWorkstate(input).ReuseStatus
}

// getPolecatManager creates a polecat manager for the given rig.
func getPolecatManager(rigName string) (*polecat.Manager, *rig.Rig, error) {
	_, r, err := getRig(rigName)
	if err != nil {
		return nil, nil, err
	}

	polecatGit := git.NewGit(r.Path)
	t := tmux.NewTmux()
	mgr := polecat.NewManager(r, polecatGit, t)

	return mgr, r, nil
}

func runPolecatList(cmd *cobra.Command, args []string) error {
	var rigs []*rig.Rig

	if polecatListAll {
		// List all rigs
		allRigs, err := getAllRigs()
		if err != nil {
			return err
		}
		rigs = allRigs
	} else {
		// Need a rig name
		if len(args) < 1 {
			return fmt.Errorf("rig name required (or use --all)")
		}
		_, r, err := getPolecatManager(args[0])
		if err != nil {
			return err
		}
		rigs = []*rig.Rig{r}
	}

	// Collect polecats from all rigs
	t := tmux.NewTmux()
	allPolecats := make([]PolecatListItem, 0)

	for _, r := range rigs {
		polecatGit := git.NewGit(r.Path)
		mgr := polecat.NewManager(r, polecatGit, t)
		polecatMgr := polecat.NewSessionManager(t, r)
		bd := beads.New(r.Path)

		polecats, err := mgr.List()
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to list polecats in %s: %v\n", r.Name, err)
			continue
		}

		// Track known polecat names from filesystem for zombie detection
		knownNames := make(map[string]bool)
		for _, p := range polecats {
			running, _ := polecatMgr.IsRunning(p.Name)
			cleanupStatus := ""
			activeMR := ""
			agentBeadID := polecatBeadIDForRig(r, r.Name, p.Name)
			if _, fields, err := bd.GetAgentBead(agentBeadID); err == nil && fields != nil {
				cleanupStatus = fields.CleanupStatus
				activeMR = fields.ActiveMR
			}
			state := effectivePolecatState(PolecatListItem{
				State:          p.State,
				Issue:          p.Issue,
				SessionRunning: running,
			})
			disposition := mgr.WorkstateDispositionForPolecat(p.Name, state, p.Issue)
			allPolecats = append(allPolecats, PolecatListItem{
				Rig:                  r.Name,
				Name:                 p.Name,
				State:                state,
				Issue:                p.Issue,
				CleanupStatus:        cleanupStatus,
				ActiveMR:             activeMR,
				Branch:               p.Branch,
				Verdict:              disposition.Verdict,
				Reason:               disposition.Reason,
				Reusable:             disposition.Reusable,
				SafeToNuke:           disposition.SafeToNuke,
				NeedsRecovery:        disposition.NeedsRecovery,
				NeedsMQSubmit:        disposition.NeedsMQSubmit,
				MQStatus:             disposition.MQStatus,
				CountsTowardCapacity: disposition.CountsTowardCapacity,
				ReuseStatus:          disposition.ReuseStatus,
				SessionRunning:       running,
			})
			knownNames[p.Name] = true
		}

		// Discover zombie tmux sessions: sessions without matching worktree directories.
		// These occur when a worktree is deleted but the tmux session persists
		// (incomplete nuke or session naming mismatch).
		zombieSessions, _ := findRigPolecatSessions(r.Name)
		for _, sessionName := range zombieSessions {
			_, polecatName, ok := parsePolecatSessionName(sessionName)
			if !ok {
				continue
			}
			if !knownNames[polecatName] {
				allPolecats = append(allPolecats, PolecatListItem{
					Rig:            r.Name,
					Name:           polecatName,
					State:          polecat.StateZombie,
					SessionRunning: true,
					Zombie:         true,
					SessionName:    sessionName,
				})
			}
		}
	}

	// Output
	if polecatListJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(allPolecats)
	}

	if len(allPolecats) == 0 {
		fmt.Println("No polecats found.")
		return nil
	}

	fmt.Printf("%s\n\n", style.Bold.Render("Polecats"))
	for _, p := range allPolecats {
		// Session indicator
		sessionStatus := style.Dim.Render("○")
		if p.SessionRunning {
			sessionStatus = style.Success.Render("●")
		}

		// State color
		stateStr := string(p.State)
		switch p.State {
		case polecat.StateWorking:
			stateStr = style.Info.Render(stateStr)
		case polecat.StateStuck:
			stateStr = style.Warning.Render(stateStr)
		case polecat.StateStalled:
			stateStr = style.Error.Render(stateStr)
		case polecat.StateReviewNeeded:
			stateStr = style.Warning.Render(stateStr)
		case polecat.StateDone:
			stateStr = style.Success.Render(stateStr)
		case polecat.StateZombie:
			stateStr = style.Error.Render(stateStr)
		default:
			stateStr = style.Dim.Render(stateStr)
		}

		fmt.Printf("  %s %s/%s  %s\n", sessionStatus, p.Rig, p.Name, stateStr)
		if p.Issue != "" {
			fmt.Printf("    %s\n", style.Dim.Render(p.Issue))
		}
		if p.ReuseStatus != "" {
			details := "reuse: " + p.ReuseStatus
			if p.CleanupStatus != "" {
				details += " cleanup=" + p.CleanupStatus
			}
			if p.ActiveMR != "" {
				details += " active_mr=" + p.ActiveMR
			}
			fmt.Printf("    %s\n", style.Dim.Render(details))
		}
		if p.Zombie && p.SessionName != "" {
			fmt.Printf("    %s\n", style.Dim.Render("session: "+p.SessionName+" (no worktree)"))
		}
	}

	return nil
}

func runPolecatAdd(cmd *cobra.Command, args []string) error {
	// Emit deprecation warning
	fmt.Fprintf(os.Stderr, "%s 'gt polecat add' is deprecated. Use 'gt polecat identity add' instead.\n",
		style.Warning.Render("Warning:"))
	fmt.Fprintf(os.Stderr, "         This command will be removed in v1.0.\n\n")

	rigName := args[0]
	polecatName := args[1]

	mgr, _, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	fmt.Printf("Adding polecat %s to rig %s...\n", polecatName, rigName)

	p, err := mgr.Add(polecatName)
	if err != nil {
		return fmt.Errorf("adding polecat: %w", err)
	}

	fmt.Printf("%s Polecat %s added.\n", style.SuccessPrefix, p.Name)
	fmt.Printf("  %s\n", style.Dim.Render(p.ClonePath))
	fmt.Printf("  Branch: %s\n", style.Dim.Render(p.Branch))

	return nil
}

func runPolecatRemove(cmd *cobra.Command, args []string) error {
	targets, err := resolvePolecatTargets(args, polecatRemoveAll)
	if err != nil {
		return err
	}

	if len(targets) == 0 {
		fmt.Println("No polecats to remove.")
		return nil
	}

	// Remove each polecat
	t := tmux.NewTmux()
	var removeErrors []string
	removed := 0

	for _, p := range targets {
		// Check if session is running
		if !polecatForce {
			polecatMgr := polecat.NewSessionManager(t, p.r)
			running, _ := polecatMgr.IsRunning(p.polecatName)
			if running {
				removeErrors = append(removeErrors, fmt.Sprintf("%s/%s: session is running (stop first or use --force)", p.rigName, p.polecatName))
				continue
			}
		}

		fmt.Printf("Removing polecat %s/%s...\n", p.rigName, p.polecatName)

		if err := p.mgr.Remove(p.polecatName, polecatForce); err != nil {
			if errors.Is(err, polecat.ErrHasChanges) {
				removeErrors = append(removeErrors, fmt.Sprintf("%s/%s: has uncommitted changes (use --force)", p.rigName, p.polecatName))
			} else {
				removeErrors = append(removeErrors, fmt.Sprintf("%s/%s: %v", p.rigName, p.polecatName, err))
			}
			continue
		}

		fmt.Printf("  %s removed\n", style.Success.Render("✓"))
		removed++
	}

	// Report results
	if len(removeErrors) > 0 {
		fmt.Printf("\n%s Some removals failed:\n", style.Warning.Render("Warning:"))
		for _, e := range removeErrors {
			fmt.Printf("  - %s\n", e)
		}
	}

	if removed > 0 {
		fmt.Printf("\n%s Removed %d polecat(s).\n", style.SuccessPrefix, removed)
	}

	if len(removeErrors) > 0 {
		return fmt.Errorf("%d removal(s) failed", len(removeErrors))
	}

	return nil
}

// PolecatStatus represents detailed polecat status for JSON output.
type PolecatStatus struct {
	Rig            string        `json:"rig"`
	Name           string        `json:"name"`
	State          polecat.State `json:"state"`
	Issue          string        `json:"issue,omitempty"`
	ClonePath      string        `json:"clone_path"`
	Branch         string        `json:"branch"`
	SessionRunning bool          `json:"session_running"`
	SessionID      string        `json:"session_id,omitempty"`
	Attached       bool          `json:"attached,omitempty"`
	Windows        int           `json:"windows,omitempty"`
	CreatedAt      string        `json:"created_at,omitempty"`
	LastActivity   string        `json:"last_activity,omitempty"`
}

func runPolecatStatus(cmd *cobra.Command, args []string) error {
	rigName, polecatName, err := parseAddress(args[0])
	if err != nil {
		return err
	}

	mgr, r, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	// Get polecat info
	p, err := mgr.Get(polecatName)
	if err != nil {
		return fmt.Errorf("polecat '%s' not found in rig '%s'", polecatName, rigName)
	}

	// Get session info
	t := tmux.NewTmux()
	polecatMgr := polecat.NewSessionManager(t, r)
	sessInfo, err := polecatMgr.Status(polecatName)
	if err != nil {
		// Non-fatal - continue without session info
		sessInfo = &polecat.SessionInfo{
			Polecat: polecatName,
			Running: false,
		}
	}

	// JSON output
	if polecatStatusJSON {
		status := PolecatStatus{
			Rig:            rigName,
			Name:           polecatName,
			State:          p.State,
			Issue:          p.Issue,
			ClonePath:      p.ClonePath,
			Branch:         p.Branch,
			SessionRunning: sessInfo.Running,
			SessionID:      sessInfo.SessionID,
			Attached:       sessInfo.Attached,
			Windows:        sessInfo.Windows,
		}
		if !sessInfo.Created.IsZero() {
			status.CreatedAt = sessInfo.Created.Format("2006-01-02 15:04:05")
		}
		if !sessInfo.LastActivity.IsZero() {
			status.LastActivity = sessInfo.LastActivity.Format("2006-01-02 15:04:05")
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(status)
	}

	// Human-readable output
	fmt.Printf("%s\n\n", style.Bold.Render(fmt.Sprintf("Polecat: %s/%s", rigName, polecatName)))

	// State with color
	stateStr := string(p.State)
	switch p.State {
	case polecat.StateWorking:
		stateStr = style.Info.Render(stateStr)
	case polecat.StateStuck:
		stateStr = style.Warning.Render(stateStr)
	case polecat.StateStalled:
		stateStr = style.Error.Render(stateStr)
	case polecat.StateReviewNeeded:
		stateStr = style.Warning.Render(stateStr)
	case polecat.StateDone:
		stateStr = style.Success.Render(stateStr)
	default:
		stateStr = style.Dim.Render(stateStr)
	}
	fmt.Printf("  State:         %s\n", stateStr)

	// Issue
	if p.Issue != "" {
		fmt.Printf("  Issue:         %s\n", p.Issue)
	} else {
		fmt.Printf("  Issue:         %s\n", style.Dim.Render("(none)"))
	}

	// Clone path and branch
	fmt.Printf("  Clone:         %s\n", style.Dim.Render(p.ClonePath))
	fmt.Printf("  Branch:        %s\n", style.Dim.Render(p.Branch))

	// Session info
	fmt.Println()
	fmt.Printf("%s\n", style.Bold.Render("Session"))

	if sessInfo.Running {
		fmt.Printf("  Status:        %s\n", style.Success.Render("running"))
		fmt.Printf("  Session ID:    %s\n", style.Dim.Render(sessInfo.SessionID))

		if sessInfo.Attached {
			fmt.Printf("  Attached:      %s\n", style.Info.Render("yes"))
		} else {
			fmt.Printf("  Attached:      %s\n", style.Dim.Render("no"))
		}

		if sessInfo.Windows > 0 {
			fmt.Printf("  Windows:       %d\n", sessInfo.Windows)
		}

		if !sessInfo.Created.IsZero() {
			fmt.Printf("  Created:       %s\n", sessInfo.Created.Format("2006-01-02 15:04:05"))
		}

		if !sessInfo.LastActivity.IsZero() {
			// Show relative time for activity
			ago := formatActivityTime(sessInfo.LastActivity)
			fmt.Printf("  Last Activity: %s (%s)\n",
				sessInfo.LastActivity.Format("15:04:05"),
				style.Dim.Render(ago))
		}
	} else {
		fmt.Printf("  Status:        %s\n", style.Dim.Render("not running"))
	}

	return nil
}

// formatActivityTime returns a human-readable relative time string.
func formatActivityTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d seconds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

// GitState represents the git state of a polecat's worktree.
type GitState struct {
	Clean                 bool     `json:"clean"`
	UncommittedFiles      []string `json:"uncommitted_files"`
	UnpushedCommits       int      `json:"unpushed_commits"`
	ComparisonBase        string   `json:"comparison_base,omitempty"`
	UnpreservedPatchCount int      `json:"unpreserved_patch_count"`
	StashCount            int      `json:"stash_count"`                  // Current-branch stashes: per-polecat risk.
	SharedStashCount      int      `json:"shared_stash_count,omitempty"` // Other branch stashes visible through the shared repo.
}

func runPolecatGitState(cmd *cobra.Command, args []string) error {
	rigName, polecatName, err := parseAddress(args[0])
	if err != nil {
		return err
	}

	mgr, r, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	// Verify polecat exists
	p, err := mgr.Get(polecatName)
	if err != nil {
		return fmt.Errorf("polecat '%s' not found in rig '%s'", polecatName, rigName)
	}

	// Get git state from the polecat's worktree
	state, err := getGitState(p.ClonePath)
	if err != nil {
		return fmt.Errorf("getting git state: %w", err)
	}

	// JSON output
	if polecatGitStateJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(state)
	}

	// Human-readable output
	fmt.Printf("%s\n\n", style.Bold.Render(fmt.Sprintf("Git State: %s/%s", r.Name, polecatName)))

	// Working tree status
	if len(state.UncommittedFiles) == 0 {
		fmt.Printf("  Working Tree:  %s\n", style.Success.Render("clean"))
	} else {
		fmt.Printf("  Working Tree:  %s\n", style.Warning.Render("dirty"))
		fmt.Printf("  Uncommitted:   %s\n", style.Warning.Render(fmt.Sprintf("%d files", len(state.UncommittedFiles))))
		for _, f := range state.UncommittedFiles {
			fmt.Printf("                 %s\n", style.Dim.Render(f))
		}
	}

	// Unpushed commits
	if state.ComparisonBase != "" {
		fmt.Printf("  Comparison:   %s (%d unpreserved patch(es))\n", style.Dim.Render(state.ComparisonBase), state.UnpreservedPatchCount)
	}
	if state.UnpushedCommits == 0 {
		fmt.Printf("  Unpushed:      %s\n", style.Success.Render("0 commits"))
	} else {
		fmt.Printf("  Unpushed:      %s\n", style.Warning.Render(fmt.Sprintf("%d commits ahead", state.UnpushedCommits)))
	}

	// Stashes
	if state.StashCount == 0 {
		fmt.Printf("  Branch Stashes: %s\n", style.Dim.Render("0"))
	} else {
		fmt.Printf("  Branch Stashes: %s\n", style.Warning.Render(fmt.Sprintf("%d", state.StashCount)))
	}
	if state.SharedStashCount > 0 {
		fmt.Printf("  Shared Stashes: %s\n", style.Dim.Render(fmt.Sprintf("%d (repo-wide, not this branch)", state.SharedStashCount)))
	}

	// Verdict
	fmt.Println()
	if state.Clean {
		fmt.Printf("  Verdict:       %s\n", style.Success.Render("CLEAN (safe to kill)"))
	} else {
		fmt.Printf("  Verdict:       %s\n", style.Error.Render("DIRTY (needs cleanup)"))
	}

	return nil
}

// getGitState checks the git state of a worktree.
func getGitState(worktreePath string) (*GitState, error) {
	return getGitStateWithTargets(worktreePath, nil)
}

func getGitStateWithTargets(worktreePath string, targets []string) (*GitState, error) {
	state := &GitState{
		Clean:            true,
		UncommittedFiles: []string{},
	}

	worktreeGit := git.NewGit(worktreePath)
	workStatus, err := worktreeGit.CheckUncommittedWork()
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}
	if workStatus.HasUncommittedChanges {
		state.UncommittedFiles = workStatus.NonRuntimePaths()
		if len(state.UncommittedFiles) > 0 {
			state.Clean = false
		}
	}
	if workStatus.StashCount > 0 {
		state.StashCount = workStatus.StashCount
		state.Clean = false
	}

	branch, _ := worktreeGit.CurrentBranch()
	if preservation, preserveErr := worktreeGit.BranchPreservationStatus(branch, "origin", targets); preserveErr == nil {
		state.ComparisonBase = preservation.ComparisonBase
		state.UnpreservedPatchCount = preservation.UnpreservedPatchCount
		if preservation.UnpreservedPatchCount > 0 {
			state.UnpushedCommits = preservation.UnpreservedPatchCount
			state.Clean = false
		}
	}

	// Check for stashes using Git.StashCount() which filters by current branch.
	// Without branch filtering, worktrees see repo-wide stashes and produce
	// false "NEEDS_RECOVERY" verdicts for worktrees with zero stashes of their own.
	if totalStashes, stashErr := worktreeGit.StashCountAll(); stashErr == nil && totalStashes > state.StashCount {
		state.SharedStashCount = totalStashes - state.StashCount
	}

	return state, nil
}

// RecoveryStatus represents whether a polecat needs recovery or is safe to nuke.
type RecoveryStatus struct {
	Rig                  string                `json:"rig"`
	Polecat              string                `json:"polecat"`
	CleanupStatus        polecat.CleanupStatus `json:"cleanup_status"`
	NeedsRecovery        bool                  `json:"needs_recovery"`
	Verdict              string                `json:"verdict"` // SAFE_TO_NUKE, PENDING_MR, NEEDS_RECOVERY, or NEEDS_MQ_SUBMIT
	Reason               string                `json:"reason,omitempty"`
	Reusable             bool                  `json:"reusable"`
	SafeToNuke           bool                  `json:"safe_to_nuke"`
	NeedsMQSubmit        bool                  `json:"needs_mq_submit"`
	CountsTowardCapacity bool                  `json:"counts_toward_capacity"`
	ReuseStatus          string                `json:"reuse_status,omitempty"`
	Branch               string                `json:"branch,omitempty"`
	Issue                string                `json:"issue,omitempty"`
	MQStatus             string                `json:"mq_status,omitempty"` // "submitted", "not_submitted", "not_required", "unknown"
	ActiveMR             string                `json:"active_mr,omitempty"`
	Blockers             []string              `json:"blockers,omitempty"`
	Diagnostics          []string              `json:"diagnostics,omitempty"`
	Reconciled           bool                  `json:"reconciled,omitempty"`
}

func runPolecatCheckRecovery(cmd *cobra.Command, args []string) error {
	rigName, polecatName, err := parseAddress(args[0])
	if err != nil {
		return err
	}

	mgr, r, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	// Verify polecat exists and get info
	p, err := mgr.Get(polecatName)
	if err != nil {
		return fmt.Errorf("polecat '%s' not found in rig '%s'", polecatName, rigName)
	}

	// Get cleanup_status from agent bead
	// We need to read it directly from beads since manager doesn't expose it
	rigPath := r.Path
	bd := beads.New(rigPath)
	agentBeadID := polecatBeadIDForRig(r, rigName, polecatName)
	agentIssue, fields, err := bd.GetAgentBead(agentBeadID)

	status := RecoveryStatus{
		Rig:     rigName,
		Polecat: polecatName,
		Branch:  p.Branch,
		Issue:   p.Issue,
	}
	beadTerminal := isAssignedBeadTerminal(bd, status.Issue)
	workTerminal := beadTerminal
	targetRefs := recoveryTargetRefs(bd, status.Issue, status.ActiveMR, status.Branch)
	input := polecat.WorkstateInput{State: p.State, CleanupStatus: polecat.CleanupUnknown, Branch: p.Branch}
	var gitState *GitState
	var gitErr error
	gitStateLoaded := false
	loadGitState := func() {
		if gitStateLoaded {
			return
		}
		gitState, gitErr = getGitStateWithTargets(p.ClonePath, targetRefs)
		gitStateLoaded = true
	}

	if err != nil || fields == nil {
		// No agent bead or no cleanup_status - fall back to git check.
		loadGitState()
		if gitErr != nil {
			input.CleanupStatus = polecat.CleanupUnknown
			input.GitCheckFailed = true
			input.GitCheckFailedReason = fmt.Sprintf("git_state=unknown path=%s: %v", p.ClonePath, gitErr)
		} else if gitState.Clean {
			input.CleanupStatus = polecat.CleanupClean
		} else if gitState.UnpushedCommits > 0 {
			input.CleanupStatus = polecat.CleanupUnpushed
			input.UnpushedCommits = gitState.UnpushedCommits
		} else if gitState.StashCount > 0 {
			input.CleanupStatus = polecat.CleanupStash
			input.StashCount = gitState.StashCount
		} else {
			input.CleanupStatus = polecat.CleanupUncommitted
			input.GitDirty = true
			input.GitDirtyReason = fmt.Sprintf("git_state=has_uncommitted uncommitted_files=%d", len(gitState.UncommittedFiles))
		}
	} else {
		// Use cleanup_status from agent bead, then overlay direct git and MQ facts.
		input.CleanupStatus = polecat.CleanupStatus(fields.CleanupStatus)
		status.ActiveMR = fields.ActiveMR
		targetRefs = recoveryTargetRefs(bd, status.Issue, status.ActiveMR, status.Branch)
		input.ActiveMR = fields.ActiveMR
		hookBead := agentHookBead(agentIssue, fields)
		hookSafe, hookTerminal, hookBlocker := hookBeadSafeForCleanup(bd, hookBead)
		workTerminal = beadTerminal || hookTerminal
		sourceHint := agentSourceIssueHint(status.Issue, fields)
		if status.Issue == "" && sourceHint != "" {
			status.Issue = sourceHint
		}
		if !beadTerminal && sourceHint != "" {
			beadTerminal = isAssignedBeadTerminal(bd, sourceHint)
			workTerminal = beadTerminal || hookTerminal
		}
		if hookBlocker != "" {
			input.HookBead = hookBead
		}
		input.PushFailed = fields.PushFailed
		input.MRFailed = fields.MRFailed
		assignee := fmt.Sprintf("%s/polecats/%s", rigName, polecatName)
		partialSpawn, diagnostic := partialSpawnWithoutDurableHook(bd, fields, assignee, status.Issue)
		if diagnostic != "" {
			status.Diagnostics = append(status.Diagnostics, diagnostic)
		}
		activeMRAssessment := polecat.ActiveMRAssessment{}
		if fields.ActiveMR != "" {
			gitSafe := activeMRGitSafeForWorktree(p.ClonePath)
			activeMRAssessment = polecat.AssessActiveMR(bd, polecat.ActiveMRInput{
				ActiveMR:        fields.ActiveMR,
				SourceIssueHint: sourceHint,
				RequireGitSafe:  true,
				GitSafe:         gitSafe,
			})
			if status.Issue == "" && activeMRAssessment.SourceIssue != "" {
				status.Issue = activeMRAssessment.SourceIssue
			}
			if activeMRAssessment.SourceTerminal {
				beadTerminal = true
				workTerminal = true
			}
			if activeMRAssessment.Pending {
				input.ActiveMRBlocker = activeMRAssessment.Reason
			}
		}
		input.PartialSpawnWithoutDurableHook = partialSpawn
		if blocker := cleanupStatusBlockerForRecovery(input.CleanupStatus, partialSpawn); blocker == "" && !input.CleanupStatus.IsSafe() {
			input.IgnoreCleanupStatus = true
		} else if blocker != "" {
			if input.CleanupStatus == polecat.CleanupUnpushed {
				loadGitState()
			}
			gitSafe := activeMRGitSafeForWorktree(p.ClonePath)
			if staleCleanupStatusCanBeIgnoredForRecovery(input.CleanupStatus, workTerminal, hookSafe, !activeMRAssessment.Pending, gitSafe) {
				input.IgnoreCleanupStatus = true
				status.Diagnostics = append(status.Diagnostics, fmt.Sprintf("ignored_stale_cleanup_status=%s direct_git_state=safe work_ref=terminal", input.CleanupStatus))
			}
		}
		loadGitState()
		applyGitStateToWorkstateInput(&input, p.ClonePath, gitState, gitErr)
	}

	status.CleanupStatus = input.CleanupStatus
	applyMQFactsToWorkstateInput(&input, &status, bd, workTerminal, p.ClonePath, targetRefs, gitState, gitErr)
	disposition := polecat.DecideWorkstate(input)
	applyWorkstateDispositionToRecoveryStatus(&status, disposition)

	if polecatCheckRecoveryReconcileCleanup {
		reconcileCleanupStatusIfSafe(&status, bd, agentBeadID, p, fields)
	}

	// JSON output
	if polecatCheckRecoveryJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(status)
	}

	// Human-readable output
	fmt.Printf("%s\n\n", style.Bold.Render(fmt.Sprintf("Recovery Status: %s/%s", rigName, polecatName)))
	fmt.Printf("  Cleanup Status:  %s\n", status.CleanupStatus)
	if status.Branch != "" {
		fmt.Printf("  Branch:          %s\n", status.Branch)
	}
	if status.Issue != "" {
		fmt.Printf("  Issue:           %s\n", status.Issue)
	}
	if status.ActiveMR != "" {
		fmt.Printf("  Active MR:       %s\n", status.ActiveMR)
	}
	if len(status.Diagnostics) > 0 {
		fmt.Printf("  Diagnostics:     %s\n", strings.Join(status.Diagnostics, "; "))
	}
	fmt.Println()

	switch status.Verdict {
	case "NEEDS_MQ_SUBMIT":
		fmt.Printf("  Verdict:         %s\n", style.Warning.Render("NEEDS_MQ_SUBMIT"))
		fmt.Printf("  MQ Status:       %s\n", status.MQStatus)
		fmt.Println()
		fmt.Printf("  %s Work is pushed but was never submitted to the merge queue.\n", style.Warning.Render("⚠"))
		fmt.Println("  Submit to MQ before cleanup, or the branch will be orphaned.")
	case "PENDING_MR":
		fmt.Printf("  Verdict:         %s\n", style.Warning.Render("PENDING_MR"))
		fmt.Println()
		fmt.Println("  Work is waiting on an active merge request; preserve this polecat until it lands.")
	case "NEEDS_RECOVERY":
		fmt.Printf("  Verdict:         %s\n", style.Error.Render("NEEDS_RECOVERY"))
		fmt.Println()
		if len(status.Blockers) > 0 {
			fmt.Printf("  %s Cleanup refused by these predicate(s):\n", style.Warning.Render("⚠"))
			for _, blocker := range status.Blockers {
				fmt.Printf("    - %s\n", blocker)
			}
		} else {
			fmt.Printf("  %s Cleanup refused by an unknown recovery predicate.\n", style.Warning.Render("⚠"))
		}
		fmt.Println("  Escalate to Mayor for recovery before cleanup.")
	default:
		fmt.Printf("  Verdict:         %s\n", style.Success.Render("SAFE_TO_NUKE"))
		if status.MQStatus != "" {
			fmt.Printf("  MQ Status:       %s\n", status.MQStatus)
		}
		fmt.Println()
		fmt.Printf("  %s Safe to nuke - no work at risk.\n", style.Success.Render("✓"))
	}

	return nil
}

func applyGitStateToWorkstateInput(input *polecat.WorkstateInput, worktreePath string, gitState *GitState, gitErr error) {
	if gitErr != nil {
		input.GitCheckFailed = true
		input.GitCheckFailedReason = recoveryGitStateBlocker(worktreePath, gitState, gitErr)
		return
	}
	if gitState == nil || gitState.Clean {
		return
	}
	if gitState.UnpushedCommits > 0 {
		input.UnpushedCommits = gitState.UnpushedCommits
	}
	if gitState.StashCount > 0 {
		input.StashCount = gitState.StashCount
	}
	if len(gitState.UncommittedFiles) > 0 {
		input.GitDirty = true
		input.GitDirtyReason = fmt.Sprintf("git_state=has_uncommitted uncommitted_files=%d", len(gitState.UncommittedFiles))
	}
}

func applyMQFactsToWorkstateInput(input *polecat.WorkstateInput, status *RecoveryStatus, bd *beads.Beads, beadTerminal bool, worktreePath string, targetRefs []string, gitState *GitState, gitErr error) {
	if status.Branch == "" {
		return
	}
	input.MQCheckRequired = true
	input.AssignedBeadTerminal = beadTerminal
	input.HasSubmittableWork = hasSubmittableWorkForRecovery(worktreePath, targetRefs, gitState, gitErr)
	input.MQNotRequired = isMQNotRequiredSource(bd, status.Issue)
	if !input.HasSubmittableWork || input.MQNotRequired || input.AssignedBeadTerminal {
		return
	}
	mr, mrErr := bd.FindMRForBranchAny(status.Branch)
	if mrErr != nil {
		input.MQLookupFailed = true
		return
	}
	input.MRSubmitted = mr != nil
}

func applyWorkstateDispositionToRecoveryStatus(status *RecoveryStatus, disposition polecat.WorkstateDisposition) {
	status.Verdict = disposition.Verdict
	status.Reason = disposition.Reason
	status.Reusable = disposition.Reusable
	status.SafeToNuke = disposition.SafeToNuke
	status.NeedsRecovery = disposition.NeedsRecovery
	status.NeedsMQSubmit = disposition.NeedsMQSubmit
	status.CountsTowardCapacity = disposition.CountsTowardCapacity
	status.ReuseStatus = disposition.ReuseStatus
	status.MQStatus = disposition.MQStatus
	status.Blockers = disposition.Blockers
}

type issueShower interface {
	Show(issueID string) (*beads.Issue, error)
}

func cleanupStatusBlocker(status polecat.CleanupStatus) string {
	switch status {
	case polecat.CleanupClean:
		return ""
	case "":
		return "cleanup_status=<missing>"
	case polecat.CleanupUnknown:
		return "cleanup_status=unknown"
	default:
		return fmt.Sprintf("cleanup_status=%s", status)
	}
}

func cleanupStatusBlockerForRecovery(status polecat.CleanupStatus, partialSpawnWithoutHook bool) string {
	if partialSpawnWithoutHook && (status == "" || status == polecat.CleanupUnknown) {
		return ""
	}
	return cleanupStatusBlocker(status)
}

func agentHookBead(agentIssue *beads.Issue, fields *beads.AgentFields) string {
	if agentIssue != nil && agentIssue.HookBead != "" {
		return agentIssue.HookBead
	}
	if fields != nil {
		return fields.HookBead
	}
	return ""
}

func staleCleanupStatusCanBeIgnoredForRecovery(status polecat.CleanupStatus, workTerminal, hookSafe, activeMRSafe, gitSafe bool) bool {
	return status == polecat.CleanupUnpushed &&
		workTerminal &&
		hookSafe &&
		activeMRSafe &&
		gitSafe
}

func activeMRGitSafeForWorktree(worktreePath string) bool {
	g := git.NewGit(worktreePath)
	branch, err := g.CurrentBranch()
	if err != nil || branch == "" {
		return false
	}
	status, err := g.CheckUncommittedWork()
	if err != nil || !status.CleanExcludingRuntime() || status.StashCount > 0 || status.UnpushedCommits > 0 {
		return false
	}
	pushed, unpushed, err := g.BranchPushedToRemote(branch, "origin")
	if err != nil {
		return false
	}
	return pushed && unpushed == 0
}

func hookBeadSafeForCleanup(bd issueShower, hookBead string) (safe bool, terminal bool, blocker string) {
	if hookBead == "" {
		return true, false, ""
	}
	if bd == nil {
		return false, false, fmt.Sprintf("hook_bead=%s status=unverified", hookBead)
	}
	issue, err := bd.Show(hookBead)
	if err != nil {
		return false, false, fmt.Sprintf("hook_bead=%s status=lookup_error: %v", hookBead, err)
	}
	if issue == nil {
		return false, false, fmt.Sprintf("hook_bead=%s status=missing", hookBead)
	}
	if !beads.IssueStatus(issue.Status).IsTerminal() {
		return false, false, fmt.Sprintf("hook_bead=%s status=%s", hookBead, issue.Status)
	}
	return true, true, ""
}

type cleanupStatusUpdater interface {
	UpdateAgentCleanupStatus(id string, cleanupStatus string) error
}

func reconcileCleanupStatusIfSafe(status *RecoveryStatus, updater cleanupStatusUpdater, agentBeadID string, p *polecat.Polecat, fields *beads.AgentFields) {
	previous, ok := cleanupStatusReconcileCandidate(status, p, fields)
	if !ok {
		return
	}
	if updater == nil {
		status.NeedsRecovery = true
		status.Verdict = "NEEDS_RECOVERY"
		status.Blockers = append(status.Blockers, "cleanup_reconcile_failed: updater unavailable")
		return
	}
	if err := updater.UpdateAgentCleanupStatus(agentBeadID, string(polecat.CleanupClean)); err != nil {
		status.NeedsRecovery = true
		status.Verdict = "NEEDS_RECOVERY"
		status.Blockers = append(status.Blockers, fmt.Sprintf("cleanup_reconcile_failed: %v", err))
		return
	}
	status.CleanupStatus = polecat.CleanupClean
	status.Reconciled = true
	status.Diagnostics = append(status.Diagnostics, fmt.Sprintf("reconciled_cleanup_status=clean previous=%s", previous))
}

func cleanupStatusReconcileCandidate(status *RecoveryStatus, p *polecat.Polecat, fields *beads.AgentFields) (polecat.CleanupStatus, bool) {
	if status == nil || p == nil || fields == nil {
		return "", false
	}
	previous := polecat.CleanupStatus(fields.CleanupStatus)
	if previous == "" || previous == polecat.CleanupClean {
		return previous, false
	}
	if p.State != polecat.StateIdle || beads.AgentState(fields.AgentState) != beads.AgentStateIdle {
		return previous, false
	}
	if status.NeedsRecovery || status.Verdict != "SAFE_TO_NUKE" {
		return previous, false
	}
	if status.Branch != "" && status.MQStatus != "submitted" && status.MQStatus != "not_required" {
		return previous, false
	}
	return previous, true
}

func agentSourceIssueHint(currentIssue string, fields *beads.AgentFields) string {
	if currentIssue != "" {
		return currentIssue
	}
	if fields == nil {
		return ""
	}
	if fields.LastSourceIssue != "" {
		return fields.LastSourceIssue
	}
	return fields.HookBead
}

func partialSpawnWithoutDurableHook(bd issueShower, fields *beads.AgentFields, assignee, currentIssue string) (bool, string) {
	if bd == nil || fields == nil || fields.AgentState != "spawning" || fields.HookBead == "" || currentIssue != "" {
		return false, ""
	}
	issue, err := bd.Show(fields.HookBead)
	if err != nil || issue == nil {
		return false, ""
	}
	if (issue.Status == beads.StatusHooked && issue.Assignee == assignee) || issue.Assignee == assignee {
		return false, ""
	}
	return true, fmt.Sprintf("partial_spawn_without_durable_hook agent_state=%s hook_bead=%s hook_status=%s hook_assignee=%q", fields.AgentState, fields.HookBead, issue.Status, issue.Assignee)
}

func recoveryGitStateBlocker(worktreePath string, gitState *GitState, gitErr error) string {
	if gitErr != nil {
		return fmt.Sprintf("git_state=unknown path=%s: %v", worktreePath, gitErr)
	}
	if gitState == nil || gitState.Clean {
		return ""
	}
	if gitState.UnpushedCommits > 0 {
		return fmt.Sprintf("git_state=has_unpushed unpushed_commits=%d", gitState.UnpushedCommits)
	}
	if gitState.StashCount > 0 {
		return fmt.Sprintf("git_state=has_stash stash_count=%d", gitState.StashCount)
	}
	return fmt.Sprintf("git_state=has_uncommitted uncommitted_files=%d", len(gitState.UncommittedFiles))
}

func activeMRBlocker(bd issueShower, mrID, sourceHint string, requireGitSafe, gitSafe bool) string {
	assessment := polecat.AssessActiveMR(bd, polecat.ActiveMRInput{
		ActiveMR:        mrID,
		SourceIssueHint: sourceHint,
		RequireGitSafe:  requireGitSafe,
		GitSafe:         gitSafe,
	})
	if assessment.Pending {
		return assessment.Reason
	}
	return ""
}

func hasSubmittableWorkForRecovery(worktreePath string, targetRefs []string, gitState *GitState, gitErr error) bool {
	g := git.NewGit(worktreePath)
	branch, _ := g.CurrentBranch()
	if status, err := g.BranchTargetStatus(branch, "origin", targetRefs); err == nil {
		return status.UnpreservedPatchCount > 0
	}
	if branch, err := g.CurrentBranch(); err == nil && branch != "" && !isRecoveryBaseBranch(branch) {
		if pushed, _, err := g.BranchPushedToRemote(branch, "origin"); err == nil && pushed {
			return true
		}
	}
	return gitErr != nil || (gitState != nil && gitState.UnpushedCommits > 0)
}

func isRecoveryBaseBranch(branch string) bool {
	return branch == "main" || branch == "master" || strings.HasPrefix(branch, "integration/")
}

func recoveryTargetRefs(bd *beads.Beads, issueID, activeMR, branch string) []string {
	var refs []string
	appendMRTarget := func(issue *beads.Issue) {
		if fields := beads.ParseMRFields(issue); fields != nil && fields.Target != "" {
			refs = append(refs, fields.Target)
		}
	}
	if bd != nil {
		if activeMR != "" {
			if issue, err := bd.Show(activeMR); err == nil {
				appendMRTarget(issue)
			}
		}
		if branch != "" {
			if issue, err := bd.FindMRForBranchAny(branch); err == nil {
				appendMRTarget(issue)
			}
		}
		if issueID != "" {
			if issue, err := bd.Show(issueID); err == nil {
				appendAttachmentTargets(&refs, bd, issue)
			}
		}
	}
	return uniqueStrings(refs)
}

func appendAttachmentTargets(refs *[]string, bd *beads.Beads, issue *beads.Issue) {
	attachment := beads.ParseAttachmentFields(issue)
	if attachment == nil {
		return
	}
	appendBaseBranchVars(refs, attachment.FormulaVars)
	for _, value := range attachment.AttachedVars {
		appendBaseBranchVars(refs, value)
	}
	if attachment.ConvoyID != "" && bd != nil {
		if convoy, err := bd.Show(attachment.ConvoyID); err == nil {
			if fields := beads.ParseConvoyFields(convoy); fields != nil && fields.BaseBranch != "" {
				*refs = append(*refs, fields.BaseBranch)
			}
		}
	}
}

func appendBaseBranchVars(refs *[]string, vars string) {
	for _, line := range strings.Split(vars, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(key) != "base_branch" {
			continue
		}
		if value = strings.TrimSpace(value); value != "" {
			*refs = append(*refs, value)
		}
	}
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// mrFinder is the subset of *beads.Beads that applyMQCheck needs. It lets us
// unit-test the verdict logic without a real bd binary.
type mrFinder interface {
	FindMRForBranchAny(branch string) (*beads.Issue, error)
}

// isAssignedBeadTerminal reports whether the polecat's assigned bead (if any)
// is in a terminal status (closed/tombstone). Returns false on any lookup
// failure — callers must only use this to *skip* further escalation, never to
// escalate, so a false negative is safe.
func isAssignedBeadTerminal(bd *beads.Beads, issueID string) bool {
	if issueID == "" || bd == nil {
		return false
	}
	issue, err := bd.Show(issueID)
	if err != nil || issue == nil {
		return false
	}
	return beads.IssueStatus(issue.Status).IsTerminal()
}

// isMQNotRequiredSource reports whether the source bead intentionally bypasses
// the internal merge queue. The caller still gates this on SAFE_TO_NUKE so dirty
// or unpushed local work is never hidden by source metadata.
func isMQNotRequiredSource(bd issueShower, issueID string) bool {
	if issueID == "" || bd == nil {
		return false
	}
	issue, err := bd.Show(issueID)
	if err != nil || issue == nil {
		return false
	}
	attachment := beads.ParseAttachmentFields(issue)
	if attachment == nil {
		return false
	}
	if attachment.NoMerge || attachment.ReviewOnly {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(attachment.MergeStrategy), "local")
}

// applyMQCheck mutates status based on merge-queue state for the polecat's
// branch. If beadTerminal is true, the assigned bead is already closed, so
// there is nothing to submit and we leave the verdict as SAFE_TO_NUKE.
//
// This guard fixes the zombie-restart loop documented in bead aa-55d8:
// a closed "no-op audit" bead (e.g. aa-xtee) used to report NEEDS_MQ_SUBMIT
// forever, causing witness patrols to restart the polecat on every cycle.
func applyMQCheck(status *RecoveryStatus, bd mrFinder, beadTerminal, hasSubmittableWork, mqNotRequired bool) {
	if !hasSubmittableWork || mqNotRequired {
		// No commits/content ahead of the integration branch means gt done had
		// nothing to enqueue; treating that as missing MQ submission causes
		// recovery loops on no-op/report-only assignments.
		status.MQStatus = "not_required"
		return
	}
	if beadTerminal {
		// Work exists, but the bead is already terminal.
		status.MQStatus = "submitted"
		return
	}
	mr, mrErr := bd.FindMRForBranchAny(status.Branch)
	if mrErr != nil {
		// Can't verify MQ — fail closed until the queue state can be checked.
		status.MQStatus = "unknown"
		status.NeedsRecovery = true
		status.Verdict = "NEEDS_RECOVERY"
		status.Blockers = append(status.Blockers, fmt.Sprintf("mq_lookup_error: %v", mrErr))
		return
	}
	if mr != nil {
		status.MQStatus = "submitted"
		return
	}
	// Work was pushed but never entered the merge queue
	status.MQStatus = "not_submitted"
	status.NeedsRecovery = true
	status.Verdict = "NEEDS_MQ_SUBMIT"
}

func runPolecatGC(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	mgr, r, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	fmt.Printf("Garbage collecting stale polecat branches in %s...\n\n", r.Name)

	if polecatGCDryRun {
		// Dry run - list branches that would be deleted
		repoGit := git.NewGit(r.Path)

		// List all polecat branches
		branches, err := repoGit.ListBranches("polecat/*")
		if err != nil {
			return fmt.Errorf("listing branches: %w", err)
		}

		if len(branches) == 0 {
			fmt.Println("No polecat branches found.")
			return nil
		}

		// Get current branches
		polecats, err := mgr.List()
		if err != nil {
			return fmt.Errorf("listing polecats: %w", err)
		}

		currentBranches := make(map[string]bool)
		for _, p := range polecats {
			currentBranches[p.Branch] = true
		}

		// Show what would be deleted
		toDelete := 0
		for _, branch := range branches {
			if !currentBranches[branch] {
				fmt.Printf("  Would delete: %s\n", style.Dim.Render(branch))
				toDelete++
			} else {
				fmt.Printf("  Keep (in use): %s\n", style.Success.Render(branch))
			}
		}

		fmt.Printf("\nWould delete %d branch(es), keep %d\n", toDelete, len(branches)-toDelete)
		return nil
	}

	// Actually clean up
	deleted, err := mgr.CleanupStaleBranches()
	if err != nil {
		return fmt.Errorf("cleanup failed: %w", err)
	}

	if deleted == 0 {
		fmt.Println("No stale branches to clean up.")
	} else {
		fmt.Printf("%s Deleted %d stale branch(es).\n", style.SuccessPrefix, deleted)
	}

	return nil
}

// splitLines splits a string into non-empty lines.
func splitLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func runPolecatNuke(cmd *cobra.Command, args []string) error {
	targets, err := resolvePolecatTargets(args, polecatNukeAll)
	if err != nil {
		return err
	}

	if len(targets) == 0 {
		fmt.Println("No polecats to nuke.")
		return nil
	}

	// Safety checks: refuse to nuke polecats with active work unless --force is set
	if !polecatNukeForce && !polecatNukeDryRun {
		var blocked []*SafetyCheckResult
		for _, p := range targets {
			result := checkPolecatSafety(p)
			if result.Blocked {
				blocked = append(blocked, result)
			}
		}

		if len(blocked) > 0 {
			displaySafetyCheckBlocked(blocked)
			return fmt.Errorf("blocked: %d polecat(s) failed nuke safety checks: %s", len(blocked), formatSafetyCheckBlockers(blocked))
		}
	}

	// Nuke each polecat
	var nukeErrors []string
	nuked := 0
	batchPurge := !polecatNukeDryRun && len(targets) > 1
	purgeRigs := make(map[string]*rig.Rig)
	dryRunBlocked := 0

	for _, p := range targets {
		if polecatNukeDryRun {
			blocked := !polecatNukeForce && checkPolecatSafety(p).Blocked
			if blocked {
				fmt.Printf("Would refuse to nuke %s/%s without --force:\n", p.rigName, p.polecatName)
				dryRunBlocked++
			} else {
				fmt.Printf("Would nuke %s/%s:\n", p.rigName, p.polecatName)
			}
			fmt.Printf("  - Kill session: gt-%s-%s\n", p.rigName, p.polecatName)
			fmt.Printf("  - Delete worktree: %s/polecats/%s\n", p.r.Path, p.polecatName)
			fmt.Printf("  - Delete branch (if exists)\n")
			fmt.Printf("  - Reset agent bead: %s\n", polecatBeadIDForRig(p.r, p.rigName, p.polecatName))

			if displayDryRunSafetyCheck(p) && !blocked {
				dryRunBlocked++
			}
			fmt.Println()
			continue
		}

		if polecatNukeForce {
			fmt.Printf("%s Nuking %s/%s (--force)...\n", style.Warning.Render("⚠"), p.rigName, p.polecatName)
		} else {
			fmt.Printf("Nuking %s/%s...\n", p.rigName, p.polecatName)
		}

		if err := nukePolecatFullWithOptions(p.polecatName, p.rigName, p.mgr, p.r, nukePolecatOptions{PurgeClosedEphemerals: !batchPurge}); err != nil {
			nukeErrors = append(nukeErrors, fmt.Sprintf("%s/%s: %v", p.rigName, p.polecatName, err))
			continue
		}

		nuked++
		if batchPurge {
			purgeRigs[p.r.Path] = p.r
		}
	}
	if batchPurge && len(purgeRigs) > 0 {
		for _, r := range purgeRigs {
			purgeClosedEphemeralBeads(beads.New(r.Path))
		}
	}

	// Report results
	if polecatNukeDryRun {
		if dryRunBlocked > 0 {
			fmt.Printf("\n%s %s\n", style.Warning.Render("⚠"), dryRunNukeSummary(len(targets), dryRunBlocked))
		} else {
			fmt.Printf("\n%s %s\n", style.Info.Render("ℹ"), dryRunNukeSummary(len(targets), dryRunBlocked))
		}
		return nil
	}

	if len(nukeErrors) > 0 {
		fmt.Printf("\n%s Some nukes failed:\n", style.Warning.Render("Warning:"))
		for _, e := range nukeErrors {
			fmt.Printf("  - %s\n", e)
		}
	}

	if nuked > 0 {
		fmt.Printf("\n%s Nuked %d polecat(s).\n", style.SuccessPrefix, nuked)
	}

	// Final cleanup: Kill any orphaned Claude processes that escaped the session termination.
	// This catches processes that called setsid() or were reparented during session shutdown.
	if !polecatNukeDryRun {
		cleanupOrphanedProcesses()
	}

	if len(nukeErrors) > 0 {
		return fmt.Errorf("%d nuke(s) failed", len(nukeErrors))
	}

	return nil
}

func dryRunNukeSummary(total, blocked int) string {
	if blocked > 0 {
		return fmt.Sprintf("Would refuse to nuke %d of %d polecat(s) without --force.", blocked, total)
	}
	return fmt.Sprintf("Would nuke %d polecat(s).", total)
}

// nukePolecatFull performs the complete cleanup sequence for a single polecat:
// 1. Kill tmux session
// 2. Delete worktree (via RemoveWithOptions with nuclear=true)
// 3. Delete git branch
// 4. Close agent bead
// This is the canonical cleanup path used by both `polecat nuke` and `polecat stale --cleanup`.
func nukePolecatFull(polecatName, rigName string, mgr *polecat.Manager, r *rig.Rig) error {
	return nukePolecatFullWithOptions(polecatName, rigName, mgr, r, nukePolecatOptions{PurgeClosedEphemerals: true})
}

type nukePolecatOptions struct {
	PurgeClosedEphemerals bool
}

func nukePolecatFullWithOptions(polecatName, rigName string, mgr *polecat.Manager, r *rig.Rig, opts nukePolecatOptions) error {
	t := tmux.NewTmux()

	// Step 1: Kill tmux session unconditionally to prevent ghost sessions
	// when IsRunning fails to detect the session.
	sessMgr := polecat.NewSessionManager(t, r)
	if err := sessMgr.Stop(polecatName, true); err != nil {
		if !errors.Is(err, polecat.ErrSessionNotFound) {
			fmt.Printf("  %s session kill failed: %v\n", style.Warning.Render("⚠"), err)
		}
	} else {
		fmt.Printf("  %s killed session\n", style.Success.Render("✓"))
	}

	// Step 2: Get polecat info before deletion (for branch name + hooked work bead)
	polecatInfo, getErr := mgr.Get(polecatName)
	var branchToDelete string
	if getErr == nil && polecatInfo != nil {
		branchToDelete = polecatInfo.Branch
	}

	// Step 2.5: Burn any molecule attached to the polecat's hooked work bead.
	// Without this, nuked polecats leave orphan molecule refs that block re-sling.
	// The stale attached_molecule in the work bead's description causes sling to
	// fail with "bead already has N attached molecule(s)" on re-dispatch (gt-npzy).
	if getErr == nil && polecatInfo != nil && polecatInfo.Issue != "" {
		nukeCleanupMolecules(polecatInfo.Issue, r)
	}

	// Step 2.75: Best-effort push before nuke (gt-4vr guardrail).
	// Try to preserve any unpushed commits on the branch. If push fails,
	// proceed — --force already means "I accept data loss".
	if branchToDelete != "" {
		var pushGit *git.Git
		// Try worktree first (may still exist), then bare repo fallback.
		// Use ClonePath from the polecat record — the worktree lives at
		// <rig>/polecats/<name>/<rigName>/, not <rig>/polecats/<name>/.
		if polecatInfo != nil && polecatInfo.ClonePath != "" {
			if _, statErr := os.Stat(polecatInfo.ClonePath); statErr == nil {
				pushGit = git.NewGit(polecatInfo.ClonePath)
			}
		}
		if pushGit == nil {
			bareRepoPath := filepath.Join(r.Path, ".repo.git")
			if info, statErr := os.Stat(bareRepoPath); statErr == nil && info.IsDir() {
				pushGit = git.NewGitWithDir(bareRepoPath, "")
			}
		}
		if pushGit != nil {
			refspec := branchToDelete + ":" + branchToDelete
			if err := pushGit.Push("origin", refspec, false); err != nil {
				fmt.Printf("  %s best-effort push failed (proceeding): %v\n", style.Dim.Render("○"), err)
			} else {
				fmt.Printf("  %s pushed branch %s before nuke\n", style.Success.Render("✓"), branchToDelete)
			}
		}
	}

	// Step 3: Delete worktree (nuclear=true to bypass safety checks for stale polecats)
	if err := mgr.RemoveWithOptions(polecatName, true, true, false); err != nil {
		if errors.Is(err, polecat.ErrPolecatNotFound) {
			fmt.Printf("  %s worktree already gone\n", style.Dim.Render("○"))
			resetPolecatAgentBeadForReuse(r, rigName, polecatName)
		} else {
			return fmt.Errorf("worktree removal failed: %w", err)
		}
	} else {
		fmt.Printf("  %s deleted worktree\n", style.Success.Render("✓"))
	}

	// Step 4: Delete local branch (if we know it)
	// Local branch can always be deleted (worktree is already gone).
	// Remote branch is never deleted during nuke — the refinery owns
	// remote branch cleanup after successful merge (gt mq post-merge).
	// This prevents the race where nuke deletes the branch before the
	// refinery has a chance to merge it. (gt-v5ku)
	if branchToDelete != "" {
		repoGit := getRepoGitForRig(r.Path)
		if err := repoGit.DeleteBranch(branchToDelete, true); err != nil {
			fmt.Printf("  %s branch delete: %v\n", style.Dim.Render("○"), err)
		} else {
			fmt.Printf("  %s deleted local branch %s\n", style.Success.Render("✓"), branchToDelete)
		}
		fmt.Printf("  %s remote branch preserved for refinery merge\n", style.Dim.Render("○"))
	}

	// Step 5: Purge closed ephemeral beads (wisps) accumulated during sessions.
	// Without this, closed wisps from mol-polecat-work steps, mol-witness-patrol
	// cycles, etc. accumulate across sessions and pollute bd ready/list (hq-6161m).
	if opts.PurgeClosedEphemerals {
		purgeClosedEphemeralBeads(beads.New(r.Path))
	}

	return nil
}

func resetPolecatAgentBeadForReuse(r *rig.Rig, rigName, polecatName string) {
	agentBeadID := polecatBeadIDForRig(r, rigName, polecatName)
	bd := beads.New(r.Path)
	if err := bd.ForAgentBead().ResetAgentBeadForReuse(agentBeadID, "nuked"); err != nil {
		fmt.Printf("  %s agent bead not found or already cleaned\n", style.Dim.Render("○"))
	} else {
		fmt.Printf("  %s reset agent bead %s\n", style.Success.Render("✓"), agentBeadID)
	}
}

// nukeCleanupMolecules burns any molecule attached to a work bead during polecat nuke.
// This prevents stale attached_molecule references from blocking re-dispatch (gt-npzy).
// Best-effort: failures are logged but don't abort the nuke.
func nukeCleanupMolecules(workBeadID string, r *rig.Rig) {
	// Use mayor/rig as workDir so ResolveBeadsDir finds the Dolt-backed
	// .beads/ directory, not the gitignored rig-root .beads/. Without this,
	// detach/close operations route to the wrong database and the stale
	// molecule attachment persists on the work bead. (gt--1up)
	bd := beads.New(filepath.Join(r.Path, "mayor", "rig"))

	// Fetch the work bead to check for attached molecules
	issue, err := bd.Show(workBeadID)
	if err != nil {
		fmt.Printf("  %s molecule cleanup: could not fetch work bead %s: %v\n",
			style.Dim.Render("○"), workBeadID, err)
		return
	}

	attachment := beads.ParseAttachmentFields(issue)
	if attachment == nil || attachment.AttachedMolecule == "" {
		return // No molecule attached — nothing to clean up
	}

	moleculeID := attachment.AttachedMolecule

	// Force-close descendant steps before detaching (prevents orphaned step beads).
	// Uses force variant since nuke is destructive — must succeed even for beads in
	// invalid states. Best-effort — log but proceed in nuke path.
	if _, err := forceCloseDescendants(bd, moleculeID); err != nil {
		style.PrintWarning("nuke: could not close descendants of %s: %v", moleculeID, err)
	}

	// Detach the molecule with audit trail
	if _, detachErr := bd.DetachMoleculeWithAudit(workBeadID, beads.DetachOptions{
		Operation: "burn",
		Reason:    "polecat nuked: cleaning stale molecule",
	}); detachErr != nil {
		fmt.Printf("  %s molecule detach failed for %s: %v\n",
			style.Warning.Render("⚠"), moleculeID, detachErr)
		return
	}

	// Remove dependency bond so collectExistingMolecules won't find the
	// closed molecule and block re-dispatch. Without this, the bond persists
	// and every sling attempt fails with "bead has existing molecule(s)".
	if err := bd.RemoveDependency(workBeadID, moleculeID); err != nil {
		fmt.Printf("  %s molecule bond removal failed for %s → %s: %v\n",
			style.Warning.Render("⚠"), workBeadID, moleculeID, err)
		// Non-fatal: detach already cleared the description pointer.
	}

	// Force-close the orphaned wisp root so it doesn't linger
	if closeErr := bd.ForceCloseWithReason("burned: polecat nuked", moleculeID); closeErr != nil {
		fmt.Printf("  %s molecule root close failed for %s: %v\n",
			style.Warning.Render("⚠"), moleculeID, closeErr)
	} else {
		fmt.Printf("  %s burned stale molecule %s from work bead %s\n",
			style.Success.Render("✓"), moleculeID, workBeadID)
	}
}

// cleanupOrphanedProcesses kills Claude processes that survived session termination.
// Uses aggressive zombie detection via tmux session verification.
func cleanupOrphanedProcesses() {
	results, err := util.CleanupZombieClaudeProcesses()
	if err != nil {
		// Non-fatal: log and continue
		fmt.Printf("  %s orphan cleanup check failed: %v\n", style.Dim.Render("○"), err)
		return
	}

	if len(results) == 0 {
		return
	}

	// Report what was cleaned up
	var killed, escalated int
	for _, r := range results {
		switch r.Signal {
		case "SIGTERM", "SIGKILL":
			killed++
		case "UNKILLABLE":
			escalated++
		}
	}

	if killed > 0 {
		fmt.Printf("  %s cleaned up %d orphaned process(es)\n", style.Success.Render("✓"), killed)
	}
	if escalated > 0 {
		fmt.Printf("  %s %d process(es) survived SIGKILL (unkillable)\n", style.Warning.Render("⚠"), escalated)
	}
}

func runPolecatStale(cmd *cobra.Command, args []string) error {
	rigName := args[0]
	mgr, r, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	fmt.Printf("Detecting stale polecats in %s (threshold: %d commits behind main)...\n\n", r.Name, polecatStaleThreshold)

	staleInfos, err := mgr.DetectStalePolecats(polecatStaleThreshold)
	if err != nil {
		return fmt.Errorf("detecting stale polecats: %w", err)
	}

	if len(staleInfos) == 0 {
		fmt.Println("No polecats found.")
		return nil
	}

	// JSON output
	if polecatStaleJSON {
		return json.NewEncoder(os.Stdout).Encode(staleInfos)
	}

	// Summary counts
	var staleCount, safeCount int
	for _, info := range staleInfos {
		if info.IsStale {
			staleCount++
		} else {
			safeCount++
		}
	}

	// Display results
	for _, info := range staleInfos {
		statusIcon := style.Success.Render("●")
		statusText := "active"
		if info.IsStale {
			statusIcon = style.Warning.Render("○")
			statusText = "stale"
		}

		fmt.Printf("%s %s (%s)\n", statusIcon, style.Bold.Render(info.Name), statusText)

		// Session status
		if info.HasActiveSession {
			fmt.Printf("    Session: %s\n", style.Success.Render("running"))
		} else {
			fmt.Printf("    Session: %s\n", style.Dim.Render("stopped"))
		}

		// Commits behind
		if info.CommitsBehind > 0 {
			behindStyle := style.Dim
			if info.CommitsBehind >= polecatStaleThreshold {
				behindStyle = style.Warning
			}
			fmt.Printf("    Behind main: %s\n", behindStyle.Render(fmt.Sprintf("%d commits", info.CommitsBehind)))
		}

		// Agent state
		if info.AgentState != "" {
			fmt.Printf("    Agent state: %s\n", info.AgentState)
		} else {
			fmt.Printf("    Agent state: %s\n", style.Dim.Render("no bead"))
		}

		// Uncommitted work
		if info.HasUncommittedWork {
			fmt.Printf("    Uncommitted: %s\n", style.Error.Render("yes"))
		}

		// Reason
		fmt.Printf("    Reason: %s\n", info.Reason)
		fmt.Println()
	}

	// Summary
	fmt.Printf("Summary: %d stale, %d active\n", staleCount, safeCount)

	// Cleanup if requested
	if polecatStaleCleanup && staleCount > 0 {
		fmt.Println()
		if polecatStaleDryRun {
			fmt.Printf("Would clean up %d stale polecat(s):\n", staleCount)
			for _, info := range staleInfos {
				if info.IsStale {
					fmt.Printf("  - %s: %s\n", info.Name, info.Reason)
				}
			}
		} else {
			fmt.Printf("Cleaning up %d stale polecat(s)...\n", staleCount)
			nuked := 0
			batchPurge := staleCount > 1
			for _, info := range staleInfos {
				if !info.IsStale {
					continue
				}
				fmt.Printf("Nuking %s...\n", info.Name)
				if err := nukePolecatFullWithOptions(info.Name, rigName, mgr, r, nukePolecatOptions{PurgeClosedEphemerals: !batchPurge}); err != nil {
					fmt.Printf("  %s (%v)\n", style.Error.Render("failed"), err)
				} else {
					nuked++
				}
			}
			if batchPurge && nuked > 0 {
				purgeClosedEphemeralBeads(beads.New(r.Path))
			}
			fmt.Printf("\n%s Nuked %d stale polecat(s).\n", style.SuccessPrefix, nuked)

			// Clean up any orphaned processes that survived session termination
			cleanupOrphanedProcesses()
		}
	}

	return nil
}

func runPolecatPrune(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	_, r, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	// Use the mayor/rig clone (or bare repo) for branch operations
	var repoGit *git.Git
	bareRepoPath := filepath.Join(r.Path, ".repo.git")
	if info, statErr := os.Stat(bareRepoPath); statErr == nil && info.IsDir() {
		repoGit = git.NewGitWithDir(bareRepoPath, "")
	} else {
		repoGit = git.NewGit(filepath.Join(r.Path, "mayor", "rig"))
	}

	fmt.Printf("Pruning stale polecat branches in %s...\n", r.Name)

	// First, prune stale remote-tracking refs so we detect deleted remote branches
	if err := repoGit.FetchPrune("origin"); err != nil {
		fmt.Printf("  %s fetch --prune: %v (continuing anyway)\n", style.Warning.Render("⚠"), err)
	}

	// Prune local branches that are merged or have no remote
	pruned, err := repoGit.PruneStaleBranches("polecat/*", polecatPruneDryRun)
	if err != nil {
		return fmt.Errorf("pruning local branches: %w", err)
	}

	if len(pruned) == 0 {
		fmt.Println("No stale local polecat branches found.")
	} else {
		verb := "Pruned"
		if polecatPruneDryRun {
			verb = "Would prune"
		}
		for _, b := range pruned {
			fmt.Printf("  %s %s (%s)\n", style.Success.Render("✓"), b.Name, b.Reason)
		}
		fmt.Printf("\n%s %d local branch(es).\n", verb, len(pruned))
	}

	// Optionally prune remote polecat branches
	if polecatPruneRemote {
		fmt.Println()
		fmt.Println("Pruning remote polecat branches...")

		defaultBranch := repoGit.RemoteDefaultBranch()
		remoteRefs, lsErr := repoGit.ListPushRemoteRefsWithHashes("origin", "refs/heads/polecat/")
		if lsErr != nil {
			return fmt.Errorf("listing remote refs: %w", lsErr)
		}

		remotePruned := 0
		for _, ref := range remoteRefs {
			if !strings.HasPrefix(ref.Name, "refs/heads/") {
				continue
			}
			branch := strings.TrimPrefix(ref.Name, "refs/heads/")
			// Use the listed remote tip, not the short branch name, so remote-only
			// branches can be classified without a local branch.
			merged, mergeErr := repoGit.IsAncestor(ref.Hash, "origin/"+defaultBranch)
			if mergeErr != nil {
				continue
			}
			if !merged {
				continue
			}

			if polecatPruneDryRun {
				fmt.Printf("  Would delete remote: %s\n", style.Dim.Render(branch))
			} else {
				if delErr := repoGit.DeleteRemoteBranch("origin", branch); delErr != nil {
					fmt.Printf("  %s remote %s: %v\n", style.Warning.Render("⚠"), branch, delErr)
				} else {
					fmt.Printf("  %s deleted remote %s\n", style.Success.Render("✓"), branch)
				}
			}
			remotePruned++
		}

		if remotePruned == 0 {
			fmt.Println("No stale remote polecat branches found.")
		} else {
			verb := "Pruned"
			if polecatPruneDryRun {
				verb = "Would prune"
			}
			fmt.Printf("\n%s %d remote branch(es).\n", verb, remotePruned)
		}
	}

	return nil
}

// runPolecatPoolInit creates a persistent polecat pool for a rig.
// Creates N polecats with identities and worktrees in IDLE state.
// Existing polecats are preserved — only new ones are created.
func runPolecatPoolInit(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	mgr, r, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	// Determine pool size: flag > rig config > default
	poolSize := 4 // default
	rigCfg, cfgErr := rig.LoadRigConfig(r.Path)
	if cfgErr == nil && rigCfg.PolecatPoolSize > 0 {
		poolSize = rigCfg.PolecatPoolSize
	}
	if polecatPoolInitSize > 0 {
		poolSize = polecatPoolInitSize
	}

	// Determine names: rig config > name pool theme
	var fixedNames []string
	if cfgErr == nil && len(rigCfg.PolecatNames) > 0 {
		fixedNames = rigCfg.PolecatNames
	}

	// List existing polecats to avoid recreating them
	existing, err := mgr.List()
	if err != nil {
		return fmt.Errorf("listing existing polecats: %w", err)
	}
	existingNames := make(map[string]bool)
	for _, p := range existing {
		existingNames[p.Name] = true
	}

	fmt.Printf("Initializing persistent polecat pool for %s (target size: %d)\n", rigName, poolSize)
	if len(existing) > 0 {
		fmt.Printf("  Existing polecats: %d\n", len(existing))
	}

	// Build the list of names to create
	var namesToCreate []string
	if len(fixedNames) > 0 {
		// Use configured names, skip ones that already exist
		for _, name := range fixedNames {
			if len(namesToCreate)+len(existingNames) >= poolSize {
				break
			}
			if !existingNames[name] {
				namesToCreate = append(namesToCreate, name)
			}
		}
	} else {
		// Use name pool allocation for new names
		namePool := mgr.GetNamePool()
		namePool.Reconcile(existingNamesList(existing))
		for len(namesToCreate)+len(existingNames) < poolSize {
			name, allocErr := namePool.Allocate()
			if allocErr != nil {
				return fmt.Errorf("allocating polecat name: %w", allocErr)
			}
			if !existingNames[name] {
				namesToCreate = append(namesToCreate, name)
			}
		}
	}

	if len(namesToCreate) == 0 {
		fmt.Printf("\n%s Pool already at target size (%d polecats).\n", style.Bold.Render("✓"), len(existing))
		return nil
	}

	if polecatPoolInitDryRun {
		fmt.Printf("\nWould create %d polecat(s):\n", len(namesToCreate))
		for _, name := range namesToCreate {
			fmt.Printf("  %s %s\n", style.Dim.Render("→"), name)
		}
		return nil
	}

	// Create each polecat
	fmt.Printf("\nCreating %d polecat(s)...\n", len(namesToCreate))
	created := 0
	for _, name := range namesToCreate {
		fmt.Printf("  %s Creating %s...", style.Dim.Render("→"), name)
		p, addErr := mgr.Add(name)
		if addErr != nil {
			fmt.Printf(" %s %v\n", style.Warning.Render("FAILED"), addErr)
			continue
		}
		// Set agent state to idle (polecat was created without work).
		// Use the retry variant: createAgentBeadWithRetry above leaves a brief
		// Dolt MVCC visibility window where the just-committed bead isn't yet
		// readable by the next UpdateAgentState query, surfacing as "issue not
		// found". Retries with backoff close that window — same pattern as
		// SetAgentStateWithRetry's other call site in polecat_spawn.go.
		if stateErr := mgr.SetAgentStateWithRetry(name, "idle"); stateErr != nil {
			fmt.Printf(" %s (created but couldn't set idle state: %v)\n", style.Warning.Render("⚠"), stateErr)
		} else {
			fmt.Printf(" %s (%s)\n", style.Success.Render("✓"), style.Dim.Render(p.ClonePath))
		}
		created++
	}

	fmt.Printf("\n%s Pool initialized: %d created, %d total (target: %d)\n",
		style.Bold.Render("✓"), created, created+len(existing), poolSize)

	// Sync hooks so all polecat settings.json files reflect current defaults.
	// Pool-init may run long after rig-add, when gt defaults have changed.
	townRoot, twErr := workspace.FindFromCwdOrError()
	if twErr == nil {
		ensureHooksBase()
		if err := syncRigHooks(townRoot, rigName); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to sync hooks after pool-init: %v\n", err)
		}
	}

	return nil
}

// existingNamesList extracts polecat names from a slice of Polecat pointers.
func existingNamesList(polecats []*polecat.Polecat) []string {
	names := make([]string, len(polecats))
	for i, p := range polecats {
		names[i] = p.Name
	}
	return names
}
