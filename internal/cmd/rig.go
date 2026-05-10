// Package cmd provides CLI commands for the gt tool.
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/crew"
	"github.com/steveyegge/gastown/internal/deps"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/hooks"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/suggest"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/wisp"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/workspace"
	"golang.org/x/term"
)

var rigCmd = &cobra.Command{
	Use:     "rig",
	GroupID: GroupWorkspace,
	Short:   "Manage rigs in the workspace",
	RunE:    requireSubcommand,
	Long: `Manage rigs (project containers) in the Gas Town workspace.

A rig is a container for managing a project and its agents:
  - refinery/rig/  Canonical main clone (Refinery's working copy)
  - mayor/rig/     Mayor's working clone for this rig
  - crew/<name>/   Human workspace(s)
  - witness/       Witness agent (no clone)
  - polecats/      Worker directories
  - .beads/        Rig-level issue tracking`,
}

var rigAddCmd = &cobra.Command{
	Use:   "add <name> <git-url>",
	Short: "Add a new rig to the workspace",
	Long: `Add a new rig by cloning a repository.

This creates a rig container with:
  - config.json           Rig configuration
  - .beads/               Rig-level issue tracking (initialized)
  - plugins/              Rig-level plugin directory
  - refinery/rig/         Canonical main clone
  - mayor/rig/            Mayor's working clone
  - crew/                 Empty crew directory (add members with 'gt crew add')
  - witness/              Witness agent directory
  - polecats/             Worker directory (empty)

The command also:
  - Seeds patrol molecules (Deacon, Witness, Refinery)
  - Creates ~/gt/plugins/ (town-level) if it doesn't exist
  - Creates <rig>/plugins/ (rig-level)

Use --adopt to register an existing directory instead of creating new:
  - Reads existing config.json if present
  - Auto-detects git URL from origin remote (git-url argument not required)
  - Adds entry to mayor/rigs.json

Example:
  gt rig add gastown https://github.com/steveyegge/gastown
  gt rig add my_project git@github.com:user/repo.git --prefix mp
  gt rig add existing_rig --adopt`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runRigAdd,
}

var rigListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all rigs in the workspace",
	Long: `List all rigs registered in the Gas Town workspace.

For each rig, displays:
  - Rig name and operational state (OPERATIONAL, PARKED, DOCKED)
  - Witness status (running/stopped)
  - Refinery status (running/stopped)
  - Number of polecats and crew members

Examples:
  gt rig list          # List all rigs with status
  gt rig list --json   # Output as JSON for scripting`,
	RunE: runRigList,
}

var rigRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a rig from the registry (does not delete files)",
	Long: `Remove a rig from the Gas Town registry.

This only removes the rig entry from mayor/rigs.json and cleans up
the beads route. The rig's files on disk are NOT deleted.

If the rig has running tmux sessions (witness, refinery, polecats, crew),
you must shut them down first with 'gt rig shutdown' or use --force to
kill them automatically.

To fully remove a rig, delete the directory manually after unregistering.

Examples:
  gt rig remove myproject                    # Unregister (fails if sessions running)
  gt rig remove myproject --force            # Kill sessions then unregister
  gt rig remove myproject && rm -rf myproject # Unregister and delete files`,
	Args: cobra.ExactArgs(1),
	RunE: runRigRemove,
}

var rigResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Reset rig state (handoff content, mail, stale issues)",
	Long: `Reset various rig state.

By default, resets all resettable state. Use flags to reset specific items.

Examples:
  gt rig reset              # Reset all state
  gt rig reset --handoff    # Clear handoff content only
  gt rig reset --mail       # Clear stale mail messages only
  gt rig reset --stale      # Reset orphaned in_progress issues
  gt rig reset --stale --dry-run  # Preview what would be reset`,
	RunE: runRigReset,
}

var rigBootCmd = &cobra.Command{
	Use:   "boot <rig>",
	Short: "Start witness and refinery for a rig",
	Long: `Start the witness and refinery agents for a rig.

This is the inverse of 'gt rig shutdown'. It starts:
- The witness (if not already running)
- The refinery (if not already running)

Polecats are NOT started by this command - they are spawned
on demand when work is assigned.

Examples:
  gt rig boot greenplace`,
	Args: cobra.ExactArgs(1),
	RunE: runRigBoot,
}

var rigStartCmd = &cobra.Command{
	Use:   "start <rig>...",
	Short: "Start witness and refinery on patrol for one or more rigs",
	Long: `Start the witness and refinery agents on patrol for one or more rigs.

This is similar to 'gt rig boot' but supports multiple rigs at once.
For each rig, it starts:
- The witness (if not already running)
- The refinery (if not already running)

Polecats are NOT started by this command - they are spawned
on demand when work is assigned.

Examples:
  gt rig start gastown
  gt rig start gastown beads
  gt rig start gastown beads myproject`,
	Args: cobra.MinimumNArgs(1),
	RunE: runRigStart,
}

var rigRebootCmd = &cobra.Command{
	Use:   "reboot <rig>",
	Short: "Restart witness and refinery for a rig",
	Long: `Restart the patrol agents (witness and refinery) for a rig.

This is equivalent to 'gt rig shutdown' followed by 'gt rig boot'.
Useful after polecats complete work and land their changes.

Examples:
  gt rig reboot greenplace
  gt rig reboot beads --force`,
	Args: cobra.ExactArgs(1),
	RunE: runRigReboot,
}

var rigShutdownCmd = &cobra.Command{
	Use:   "shutdown <rig>",
	Short: "Gracefully stop all rig agents",
	Long: `Stop all agents in a rig.

This command gracefully shuts down:
- All polecat sessions
- The refinery (if running)
- The witness (if running)

Before shutdown, checks all polecats for uncommitted work:
- Uncommitted changes (modified/untracked files)
- Stashes
- Unpushed commits

Use --force to force immediate shutdown (prompts if uncommitted work).
Use --nuclear to bypass ALL safety checks (will lose work!).

Examples:
  gt rig shutdown greenplace
  gt rig shutdown greenplace --force
  gt rig shutdown greenplace --nuclear  # DANGER: loses uncommitted work`,
	Args: cobra.ExactArgs(1),
	RunE: runRigShutdown,
}

var rigStatusCmd = &cobra.Command{
	Use:        "status [rig]",
	SuggestFor: []string{"health", "health-check", "healthcheck"},
	Short:      "Show detailed status for a specific rig",
	Long: `Show detailed status for a specific rig including all workers.

If no rig is specified, infers the rig from the current directory.

Displays:
- Rig information (name, path, beads prefix)
- Witness status (running/stopped, uptime)
- Refinery status (running/stopped, uptime, queue size)
- Polecats (name, state, assigned issue, session status)
- Crew members (name, branch, session status, git status)

Examples:
  gt rig status           # Infer rig from current directory
  gt rig status gastown
  gt rig status beads`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRigStatus,
}

var rigStopCmd = &cobra.Command{
	Use:   "stop <rig>...",
	Short: "Stop one or more rigs (shutdown semantics)",
	Long: `Stop all agents in one or more rigs.

This command is similar to 'gt rig shutdown' but supports multiple rigs.
For each rig, it gracefully shuts down:
- All polecat sessions
- The refinery (if running)
- The witness (if running)

Before shutdown, checks all polecats for uncommitted work:
- Uncommitted changes (modified/untracked files)
- Stashes
- Unpushed commits

Use --force to force immediate shutdown (prompts if uncommitted work).
Use --nuclear to bypass ALL safety checks (will lose work!).

Examples:
  gt rig stop gastown
  gt rig stop gastown beads
  gt rig stop --force gastown beads
  gt rig stop --nuclear gastown  # DANGER: loses uncommitted work`,
	Args: cobra.MinimumNArgs(1),
	RunE: runRigStop,
}

var rigRestartCmd = &cobra.Command{
	Use:   "restart <rig>...",
	Short: "Restart one or more rigs (stop then start)",
	Long: `Restart the patrol agents (witness and refinery) for one or more rigs.

This is equivalent to 'gt rig stop' followed by 'gt rig start' for each rig.
Useful after polecats complete work and land their changes.

Before shutdown, checks all polecats for uncommitted work:
- Uncommitted changes (modified/untracked files)
- Stashes
- Unpushed commits

Use --force to force immediate shutdown (prompts if uncommitted work).
Use --nuclear to bypass ALL safety checks (will lose work!).

Examples:
  gt rig restart gastown
  gt rig restart gastown beads
  gt rig restart --force gastown beads
  gt rig restart --nuclear gastown  # DANGER: loses uncommitted work`,
	Args: cobra.MinimumNArgs(1),
	RunE: runRigRestart,
}

// Flags
var (
	rigAddPrefix       string
	rigAddLocalRepo    string
	rigAddBranch       string
	rigAddPushURL      string
	rigAddUpstreamURL  string
	rigAddAdopt           bool
	rigAddAdoptURL       string
	rigAddAdoptForce     bool
	rigAddFilter         string
	rigAddSparseCheckout []string
	rigResetHandoff    bool
	rigResetMail       bool
	rigResetStale      bool
	rigResetDryRun     bool
	rigResetRole       string
	rigShutdownForce   bool
	rigShutdownNuclear bool
	rigRebootForce     bool
	rigRebootNuclear   bool
	rigStopForce       bool
	rigStopNuclear     bool
	rigRestartForce    bool
	rigRestartNuclear  bool
	rigListJSON        bool
	rigRemoveForce     bool
)

var (
	// Test seams for checkUncommittedWork.
	listPolecatsForWorkCheck = func(r *rig.Rig) ([]*polecat.Polecat, error) {
		polecatGit := git.NewGit(r.Path)
		polecatMgr := polecat.NewManager(r, polecatGit, nil) // nil tmux: just listing
		return polecatMgr.List()
	}
	checkPolecatWorkStatus = func(clonePath string) (*git.UncommittedWorkStatus, error) {
		pGit := git.NewGit(clonePath)
		return pGit.CheckUncommittedWork()
	}
	isStdinTerminal = func() bool {
		return term.IsTerminal(int(os.Stdin.Fd()))
	}
	promptYesNoUnsafeProceed = promptYesNo
)

func init() {
	rootCmd.AddCommand(rigCmd)
	rigCmd.AddCommand(rigAddCmd)
	rigCmd.AddCommand(rigBootCmd)
	rigCmd.AddCommand(rigListCmd)
	rigCmd.AddCommand(rigRebootCmd)
	rigCmd.AddCommand(rigRemoveCmd)
	rigCmd.AddCommand(rigResetCmd)
	rigCmd.AddCommand(rigRestartCmd)
	rigCmd.AddCommand(rigShutdownCmd)
	rigCmd.AddCommand(rigStartCmd)
	rigCmd.AddCommand(rigMenuCmd)
	rigCmd.AddCommand(rigStatusCmd)
	rigCmd.AddCommand(rigStopCmd)

	rigListCmd.Flags().BoolVar(&rigListJSON, "json", false, "Output as JSON")

	rigRemoveCmd.Flags().BoolVarP(&rigRemoveForce, "force", "f", false, "Kill running tmux sessions before removing (may lose uncommitted work)")

	rigAddCmd.Flags().StringVar(&rigAddPrefix, "prefix", "", "Beads issue prefix (default: derived from name)")
	rigAddCmd.Flags().StringVar(&rigAddLocalRepo, "local-repo", "", "Local repo path to share git objects (optional)")
	rigAddCmd.Flags().StringVar(&rigAddBranch, "branch", "", "Default branch name (default: auto-detected from remote)")
	rigAddCmd.Flags().StringVar(&rigAddPushURL, "push-url", "", "Push URL for read-only upstreams (push to fork)")
	rigAddCmd.Flags().StringVar(&rigAddUpstreamURL, "upstream-url", "", "Upstream repository URL (for fork workflows)")
	rigAddCmd.Flags().BoolVar(&rigAddAdopt, "adopt", false, "Adopt an existing directory instead of creating new")
	rigAddCmd.Flags().StringVar(&rigAddAdoptURL, "url", "", "Git remote URL for --adopt (default: auto-detected from origin)")
	rigAddCmd.Flags().BoolVar(&rigAddAdoptForce, "force", false, "With --adopt, register even if git remote cannot be detected")
	rigAddCmd.Flags().StringVar(&rigAddFilter, "filter", "", "Partial clone filter (e.g. \"blob:none\", \"tree:0\") to reduce clone size")
	rigAddCmd.Flags().StringSliceVar(&rigAddSparseCheckout, "sparse-checkout", nil, "Sparse checkout paths (cone mode); comma-separated or repeated")

	rigResetCmd.Flags().BoolVar(&rigResetHandoff, "handoff", false, "Clear handoff content")
	rigResetCmd.Flags().BoolVar(&rigResetMail, "mail", false, "Clear stale mail messages")
	rigResetCmd.Flags().BoolVar(&rigResetStale, "stale", false, "Reset orphaned in_progress issues (no active session)")
	rigResetCmd.Flags().BoolVar(&rigResetDryRun, "dry-run", false, "Show what would be reset without making changes")
	rigResetCmd.Flags().StringVar(&rigResetRole, "role", "", "Role to reset (default: auto-detect from cwd)")

	rigShutdownCmd.Flags().BoolVarP(&rigShutdownForce, "force", "f", false, "Force immediate shutdown (prompts if uncommitted work)")
	rigShutdownCmd.Flags().BoolVar(&rigShutdownNuclear, "nuclear", false, "DANGER: Bypass ALL safety checks (loses uncommitted work!)")

	rigRebootCmd.Flags().BoolVarP(&rigRebootForce, "force", "f", false, "Force immediate shutdown during reboot (prompts if uncommitted work)")
	rigRebootCmd.Flags().BoolVar(&rigRebootNuclear, "nuclear", false, "DANGER: Bypass ALL safety checks during reboot (loses uncommitted work!)")

	rigStopCmd.Flags().BoolVarP(&rigStopForce, "force", "f", false, "Force immediate shutdown (prompts if uncommitted work)")
	rigStopCmd.Flags().BoolVar(&rigStopNuclear, "nuclear", false, "DANGER: Bypass ALL safety checks (loses uncommitted work!)")

	rigRestartCmd.Flags().BoolVarP(&rigRestartForce, "force", "f", false, "Force immediate shutdown during restart (prompts if uncommitted work)")
	rigRestartCmd.Flags().BoolVar(&rigRestartNuclear, "nuclear", false, "DANGER: Bypass ALL safety checks (loses uncommitted work!)")
}

func confirmUnsafeProceed(force bool) bool {
	// If --force and interactive TTY, prompt.
	if force && isStdinTerminal() {
		fmt.Println()
		return promptYesNoUnsafeProceed("Proceed anyway?")
	}

	// Otherwise block with hint.
	if force {
		fmt.Printf("\n%s requires an interactive terminal. Use %s to skip all checks (DANGER: will lose work!)\n",
			style.Bold.Render("--force"), style.Bold.Render("--nuclear"))
	} else {
		fmt.Printf("\nUse %s to proceed with confirmation, or %s to skip all checks (DANGER: will lose work!)\n",
			style.Bold.Render("--force"), style.Bold.Render("--nuclear"))
	}
	return false
}

// checkUncommittedWork checks polecats in a rig for uncommitted work.
// operation is the verb shown in the warning (e.g. "stop", "shutdown", "restart").
// Returns true if the caller should proceed, false if it should abort.
// When force is true and stdin is a TTY, prompts the user to confirm.
// When force is true but stdin is NOT a TTY, blocks (same as no --force).
// All user-facing messages are printed internally.
func checkUncommittedWork(r *rig.Rig, rigName, operation string, force bool) (proceed bool) {
	polecats, err := listPolecatsForWorkCheck(r)
	if err != nil {
		fmt.Printf("%s Could not check polecats for uncommitted work: %v\n",
			style.Warning.Render("⚠"), err)
		return confirmUnsafeProceed(force)
	}
	if len(polecats) == 0 {
		return true
	}

	var problemPolecats []struct {
		name   string
		status *git.UncommittedWorkStatus
	}
	var checkErrors []struct {
		name string
		err  error
	}
	for _, p := range polecats {
		status, err := checkPolecatWorkStatus(p.ClonePath)
		if err != nil {
			checkErrors = append(checkErrors, struct {
				name string
				err  error
			}{p.Name, err})
			continue
		}
		if status == nil {
			checkErrors = append(checkErrors, struct {
				name string
				err  error
			}{p.Name, fmt.Errorf("no status returned")})
			continue
		}
		if !status.Clean() {
			problemPolecats = append(problemPolecats, struct {
				name   string
				status *git.UncommittedWorkStatus
			}{p.Name, status})
		}
	}
	if len(problemPolecats) == 0 && len(checkErrors) == 0 {
		return true
	}

	if len(problemPolecats) > 0 {
		fmt.Printf("\n%s Cannot %s %s - polecats have uncommitted work:\n",
			style.Warning.Render("⚠"), operation, rigName)
		for _, pp := range problemPolecats {
			fmt.Printf("  %s: %s\n", style.Bold.Render(pp.name), pp.status.String())
		}
	}
	if len(checkErrors) > 0 {
		fmt.Printf("\n%s Could not verify uncommitted work for:\n", style.Warning.Render("⚠"))
		for _, checkErr := range checkErrors {
			fmt.Printf("  %s: %v\n", style.Bold.Render(checkErr.name), checkErr.err)
		}
	}

	return confirmUnsafeProceed(force)
}

func runRigAdd(cmd *cobra.Command, args []string) error {
	name := args[0]

	// Handle --adopt mode: register existing directory
	if rigAddAdopt {
		return runRigAdopt(cmd, args)
	}

	// Normal add mode requires git URL
	if len(args) < 2 {
		return fmt.Errorf("git-url is required (or use --adopt to register an existing directory)")
	}
	gitURL := args[1]

	if !isGitRemoteURL(gitURL) {
		return fmt.Errorf("invalid git URL %q: expected a remote URL (e.g. https://, git@host:, ssh://, s3://, file:///abs/path)\n\nTo use a local repo as the source, pass a file:// URL. To register an already-assembled rig directory, use:\n  gt rig add %s --adopt", gitURL, name)
	}

	// Ensure beads (bd) is available before proceeding
	if err := deps.EnsureBeads(true); err != nil {
		return fmt.Errorf("beads dependency check failed: %w", err)
	}

	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rigs config
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		// Create new if doesn't exist
		rigsConfig = &config.RigsConfig{
			Version: 1,
			Rigs:    make(map[string]config.RigEntry),
		}
	}

	// Create rig manager
	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)

	fmt.Printf("Creating rig %s...\n", style.Bold.Render(name))
	fmt.Printf("  Repository: %s\n", gitURL)
	if rigAddLocalRepo != "" {
		fmt.Printf("  Local repo: %s\n", rigAddLocalRepo)
	}

	// Validate push URL if provided
	rigAddPushURL = strings.TrimSpace(rigAddPushURL)
	if rigAddPushURL != "" && !isGitRemoteURL(rigAddPushURL) {
		return fmt.Errorf("invalid push URL %q: expected a remote URL (e.g. https://, git@host:, ssh://, s3://)", rigAddPushURL)
	}

	// Validate upstream URL if provided
	rigAddUpstreamURL = strings.TrimSpace(rigAddUpstreamURL)
	if rigAddUpstreamURL != "" && !isGitRemoteURL(rigAddUpstreamURL) {
		return fmt.Errorf("invalid upstream URL %q: expected a remote URL (e.g. https://, git@host:, ssh://, s3://)", rigAddUpstreamURL)
	}

	// Validate clone filter if provided
	if rigAddFilter != "" {
		validFilters := []string{"blob:none", "tree:0"}
		valid := false
		for _, f := range validFilters {
			if rigAddFilter == f {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid --filter %q: supported values are %v", rigAddFilter, validFilters)
		}
		fmt.Printf("  Partial clone: --filter=%s\n", rigAddFilter)
	}
	if len(rigAddSparseCheckout) > 0 {
		fmt.Printf("  Sparse checkout: %v\n", rigAddSparseCheckout)
	}

	startTime := time.Now()

	// Add the rig
	newRig, err := mgr.AddRig(rig.AddRigOptions{
		Name:           name,
		GitURL:         gitURL,
		PushURL:        rigAddPushURL,
		UpstreamURL:    rigAddUpstreamURL,
		BeadsPrefix:    rigAddPrefix,
		LocalRepo:      rigAddLocalRepo,
		DefaultBranch:  rigAddBranch,
		CloneFilter:    rigAddFilter,
		SparseCheckout: rigAddSparseCheckout,
	})
	if err != nil {
		return fmt.Errorf("adding rig: %w", err)
	}
	// rigs.json is saved atomically inside AddRig; no separate save needed here.

	// Add new rig to daemon.json patrol config (witness + refinery rigs arrays)
	if err := config.AddRigToDaemonPatrols(townRoot, name); err != nil {
		// Non-fatal: daemon will still work, just won't auto-manage this rig
		fmt.Printf("  %s Could not update daemon.json patrols: %v\n", style.Warning.Render("!"), err)
	}

	// Route registration is now handled inside AddRig (before agent bead creation)
	// to avoid "no route found" warnings (#1424). Determine beadsWorkDir for rig identity bead.
	var beadsWorkDir string
	if newRig.Config.Prefix != "" {
		mayorRigBeads := filepath.Join(townRoot, name, "mayor", "rig", ".beads")
		if _, err := os.Stat(mayorRigBeads); err == nil {
			beadsWorkDir = filepath.Join(townRoot, name, "mayor", "rig")
		} else {
			beadsWorkDir = filepath.Join(townRoot, name)
		}
	}

	// Create rig identity bead
	if newRig.Config.Prefix != "" && beadsWorkDir != "" {
		bd := beads.New(beadsWorkDir)
		fields := &beads.RigFields{
			Repo:   gitURL,
			Prefix: newRig.Config.Prefix,
			State:  beads.RigStateActive,
		}
		if _, err := bd.CreateRigBead(name, fields); err != nil {
			// Non-fatal: rig is functional without the identity bead
			fmt.Printf("  %s Could not create rig identity bead: %v\n", style.Warning.Render("!"), err)
		} else {
			rigBeadID := beads.RigBeadIDWithPrefix(newRig.Config.Prefix, name)
			fmt.Printf("  Created rig identity bead: %s\n", rigBeadID)
		}

		// Create agent beads for the rig (witness, refinery)
		// This ensures they exist before the daemon tries to start them
		prefix := newRig.Config.Prefix
		witnessID := beads.WitnessBeadIDWithPrefix(prefix, name)
		if _, err := bd.CreateAgentBead(witnessID,
			fmt.Sprintf("Witness for %s - monitors polecat health and progress.", name),
			&beads.AgentFields{RoleType: "witness", Rig: name, AgentState: "idle"},
		); err != nil {
			fmt.Printf("  %s Could not create witness agent bead: %v\n", style.Warning.Render("!"), err)
		} else {
			fmt.Printf("  Created agent bead: %s\n", witnessID)
		}

		refineryID := beads.RefineryBeadIDWithPrefix(prefix, name)
		if _, err := bd.CreateAgentBead(refineryID,
			fmt.Sprintf("Refinery for %s - processes merge queue.", name),
			&beads.AgentFields{RoleType: "refinery", Rig: name, AgentState: "idle"},
		); err != nil {
			fmt.Printf("  %s Could not create refinery agent bead: %v\n", style.Warning.Render("!"), err)
		} else {
			fmt.Printf("  Created agent bead: %s\n", refineryID)
		}
	}

	// Auto-assign a namepool theme that doesn't collide with other rigs (gas-21k).
	autoAssignNamepoolTheme(townRoot, name, mgr)

	// Ensure hooks-base.json exists before syncing (needed for gt hooks diff).
	ensureHooksBase()

	// Sync hooks for the new rig's targets
	if err := syncRigHooks(townRoot, name); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to sync hooks for new rig: %v\n", err)
	}

	// Commit town-level config changes (rigs.json, daemon.json, routes.jsonl)
	// so they aren't reverted by git restore/checkout operations.
	commitTownConfigChanges(townRoot, name)

	// Refresh tmux cycle bindings on all running sessions so the new rig's
	// prefix is recognized by C-b n/p. Without this, existing sessions have
	// a stale grep pattern that doesn't include the new prefix.
	// See: https://github.com/steveyegge/gastown/issues/2299
	refreshCycleBindingsOnExistingSessions()

	elapsed := time.Since(startTime)

	// Read default branch from rig config
	defaultBranch := "main"
	if rigCfg, err := rig.LoadRigConfig(filepath.Join(townRoot, name)); err == nil && rigCfg.DefaultBranch != "" {
		defaultBranch = rigCfg.DefaultBranch
	}

	fmt.Printf("\n%s Rig created in %.1fs\n", style.Success.Render("✓"), elapsed.Seconds())
	fmt.Printf("\nStructure:\n")
	fmt.Printf("  %s/\n", name)
	fmt.Printf("  ├── config.json\n")
	fmt.Printf("  ├── .repo.git/        (shared bare repo for refinery+polecats)\n")
	fmt.Printf("  ├── .beads/           (prefix: %s)\n", newRig.Config.Prefix)
	fmt.Printf("  ├── plugins/          (rig-level plugins)\n")
	fmt.Printf("  ├── mayor/rig/        (clone: %s)\n", defaultBranch)
	fmt.Printf("  ├── refinery/rig/     (worktree: %s, sees polecat branches)\n", defaultBranch)
	fmt.Printf("  ├── crew/             (empty - add crew with 'gt crew add')\n")
	fmt.Printf("  ├── witness/\n")
	fmt.Printf("  └── polecats/         (.claude/ scaffolded for polecat sessions)\n")

	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  gt crew add <name> --rig %s   # Create your personal workspace\n", name)
	fmt.Printf("  cd %s/crew/<name>              # Start working\n", filepath.Join(townRoot, name))

	return nil
}

// GetRigLED returns the LED indicator for a rig based on session and operational state.
// Used by both rig list and statusline for consistent indicators:
//   - 🟢 = both witness and refinery running (fully active)
//   - 🟡 = one session running (partially active)
//   - ⚫ = nothing running (stopped)
//   - 🅿️ = parked (intentionally paused)
//   - 🛑 = docked (global shutdown)
func GetRigLED(hasWitness, hasRefinery bool, opState string) string {
	// Check operational state FIRST — parked/docked overrides session state.
	// Sessions may still be running during the race window after park/dock
	// but before sessions are killed (GH#2555).
	switch opState {
	case "PARKED":
		return "🅿️"
	case "DOCKED":
		return "🛑"
	}

	if hasWitness && hasRefinery {
		return "🟢"
	}
	if hasWitness || hasRefinery {
		return "🟡"
	}
	return "⚫"
}

// rigStatePriority returns a sort priority for a rig's state.
// Lower values sort first: active > partial > stopped > parked > docked.
func rigStatePriority(hasWitness, hasRefinery bool, opState string) int {
	if hasWitness && hasRefinery {
		return 0
	}
	if hasWitness || hasRefinery {
		return 1
	}
	switch opState {
	case "PARKED":
		return 3
	case "DOCKED":
		return 4
	default:
		return 2
	}
}

func runRigList(cmd *cobra.Command, args []string) error {
	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rigs config
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		fmt.Println("No rigs configured.")
		return nil
	}

	if len(rigsConfig.Rigs) == 0 {
		fmt.Println("No rigs configured.")
		fmt.Printf("\nAdd one with: %s\n", style.Dim.Render("gt rig add <name> <git-url>"))
		return nil
	}

	// Create rig manager to get details
	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)
	t := tmux.NewTmux()

	type rigInfo struct {
		Name        string `json:"name"`
		BeadsPrefix string `json:"beads_prefix"`
		Status      string `json:"status"`
		Witness     string `json:"witness"`
		Refinery    string `json:"refinery"`
		Polecats    int    `json:"polecats"`
		Crew        int    `json:"crew"`
		// sorting fields (not exported to JSON)
		sortPrio int
	}

	var rigs []rigInfo

	for name := range rigsConfig.Rigs {
		prefix := session.PrefixFor(name)

		r, err := mgr.GetRig(name)
		if err != nil {
			rigs = append(rigs, rigInfo{Name: name, BeadsPrefix: prefix, Status: "error", sortPrio: 99})
			continue
		}

		opState, _ := getRigOperationalState(townRoot, name)

		witnessSession := session.WitnessSessionName(prefix)
		refinerySession := session.RefinerySessionName(prefix)
		witnessRunning, _ := t.HasSession(witnessSession)
		refineryRunning, _ := t.HasSession(refinerySession)

		witnessStatus := "stopped"
		if witnessRunning {
			witnessStatus = "running"
		}
		refineryStatus := "stopped"
		if refineryRunning {
			refineryStatus = "running"
		}

		summary := r.Summary()
		rigs = append(rigs, rigInfo{
			Name:        name,
			BeadsPrefix: prefix,
			Status:      strings.ToLower(opState),
			Witness:     witnessStatus,
			Refinery:    refineryStatus,
			Polecats:    summary.PolecatCount,
			Crew:        summary.CrewCount,
			sortPrio:    rigStatePriority(witnessRunning, refineryRunning, opState),
		})
	}

	// Sort by state priority (active first), then alphabetically
	sort.Slice(rigs, func(i, j int) bool {
		if rigs[i].sortPrio != rigs[j].sortPrio {
			return rigs[i].sortPrio < rigs[j].sortPrio
		}
		return rigs[i].Name < rigs[j].Name
	})

	if rigListJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rigs)
	}

	fmt.Printf("Rigs in %s:\n\n", townRoot)
	for _, ri := range rigs {
		if ri.Status == "error" {
			fmt.Printf("  %s %s\n", style.Warning.Render("!"), ri.Name)
			continue
		}

		led := GetRigLED(ri.Witness == "running", ri.Refinery == "running", strings.ToUpper(ri.Status))
		// 🅿️ needs extra space for alignment
		space := " "
		if led == "🅿️" {
			space = "  "
		}

		fmt.Printf("%s%s%s\n", led, space, style.Bold.Render(ri.Name))

		witnessIcon := style.Dim.Render("○")
		if ri.Witness == "running" {
			witnessIcon = style.Success.Render("●")
		}
		refineryIcon := style.Dim.Render("○")
		if ri.Refinery == "running" {
			refineryIcon = style.Success.Render("●")
		}

		fmt.Printf("   Witness: %s %s  Refinery: %s %s\n",
			witnessIcon, ri.Witness, refineryIcon, ri.Refinery)
		fmt.Printf("   Polecats: %d  Crew: %d\n", ri.Polecats, ri.Crew)
		fmt.Println()
	}

	return nil
}

var rigMenuCmd = &cobra.Command{
	Use:    "menu",
	Short:  "Show interactive rig menu in tmux",
	Long:   `Display a tmux popup menu listing all rigs with status indicators and per-rig actions.`,
	Hidden: true, // Internal command called by keybinding
	RunE:   runRigMenu,
}

func runRigMenu(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil || len(rigsConfig.Rigs) == 0 {
		return fmt.Errorf("no rigs configured")
	}

	t := tmux.NewTmux()

	type menuRig struct {
		name     string
		led      string
		running  bool
		opState  string
		sortPrio int
	}

	var rigs []menuRig
	for name := range rigsConfig.Rigs {
		prefix := session.PrefixFor(name)
		opState, _ := getRigOperationalState(townRoot, name)

		witnessSession := session.WitnessSessionName(prefix)
		refinerySession := session.RefinerySessionName(prefix)
		hasWitness, _ := t.HasSession(witnessSession)
		hasRefinery, _ := t.HasSession(refinerySession)

		led := GetRigLED(hasWitness, hasRefinery, opState)
		rigs = append(rigs, menuRig{
			name:     name,
			led:      led,
			running:  hasWitness || hasRefinery,
			opState:  opState,
			sortPrio: rigStatePriority(hasWitness, hasRefinery, opState),
		})
	}

	sort.Slice(rigs, func(i, j int) bool {
		if rigs[i].sortPrio != rigs[j].sortPrio {
			return rigs[i].sortPrio < rigs[j].sortPrio
		}
		return rigs[i].name < rigs[j].name
	})

	menuArgs := []string{
		"display-menu",
		"-T", "#[align=centre,fg=cyan,bold]⛽ Rigs", //nolint:misspell // tmux uses British spelling
		"-x", "C",
		"-y", "C",
		"--",
	}

	keyIndex := 0
	for _, r := range rigs {
		// Rig name entry — opens status popup
		space := " "
		if r.led == "🅿️" {
			space = "  "
		}
		label := fmt.Sprintf("%s%s%s", r.led, space, r.name)
		key := shortcutKey(keyIndex)
		action := fmt.Sprintf("display-popup -E -w 80 -h 25 -T ' %s ' 'gt rig status %s; echo; echo \"Press any key to close\"; read -rsn1'", r.name, r.name)
		menuArgs = append(menuArgs, label, key, action)
		keyIndex++

		// Contextual actions (no shortcut keys)
		if r.running {
			menuArgs = append(menuArgs,
				"   Stop", "", fmt.Sprintf("run-shell 'gt rig stop %s'", r.name),
				"   Reboot", "", fmt.Sprintf("run-shell 'gt rig reboot %s'", r.name),
			)
		} else if r.opState == "PARKED" {
			menuArgs = append(menuArgs,
				"   Unpark", "", fmt.Sprintf("run-shell 'gt rig unpark %s'", r.name),
				"   Start", "", fmt.Sprintf("run-shell 'gt rig start %s'", r.name),
			)
		} else if r.opState == "DOCKED" {
			menuArgs = append(menuArgs,
				"   Undock", "", fmt.Sprintf("run-shell 'gt rig undock %s'", r.name),
			)
		} else {
			// Stopped but not parked/docked
			menuArgs = append(menuArgs,
				"   Start", "", fmt.Sprintf("run-shell 'gt rig start %s'", r.name),
			)
		}

		// Park/dock available for non-parked/docked rigs
		if r.opState != "PARKED" && r.opState != "DOCKED" {
			menuArgs = append(menuArgs,
				"   Park", "", fmt.Sprintf("run-shell 'gt rig park %s'", r.name),
			)
		}

		// Separator between rigs
		menuArgs = append(menuArgs, "")
	}

	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux not found: %w", err)
	}

	execCmd := exec.Command(tmuxPath, menuArgs...)
	execCmd.Stdin = os.Stdin
	execCmd.Stdout = os.Stdout
	execCmd.Stderr = os.Stderr
	return execCmd.Run()
}

func runRigRemove(cmd *cobra.Command, args []string) error {
	name := args[0]

	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rigs config
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		return fmt.Errorf("loading rigs config: %w", err)
	}

	// Get the rig's beads prefix before removing (needed for route cleanup)
	var beadsPrefix string
	if entry, ok := rigsConfig.Rigs[name]; ok && entry.BeadsConfig != nil {
		beadsPrefix = entry.BeadsConfig.Prefix
	}

	// Create rig manager
	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)

	// Check for running tmux sessions before removing
	t := tmux.NewTmux()
	sessions, sessErr := findRigSessions(t, name)
	if sessErr != nil {
		if !rigRemoveForce {
			return fmt.Errorf("could not verify session state for rig %s: %w (use --force to skip check)", name, sessErr)
		}
		fmt.Printf("  %s Could not check tmux sessions: %v (proceeding due to --force)\n", style.Warning.Render("!"), sessErr)
	}
	if len(sessions) > 0 {
		if !rigRemoveForce {
			fmt.Printf("%s Rig %s has %d running tmux session(s):\n",
				style.Warning.Render("⚠"), name, len(sessions))
			for _, s := range sessions {
				fmt.Printf("  - %s\n", s)
			}
			fmt.Printf("\nShut them down first:\n")
			fmt.Printf("  %s\n", style.Dim.Render(fmt.Sprintf("gt rig shutdown %s", name)))
			fmt.Printf("Or force removal:\n")
			fmt.Printf("  %s\n", style.Dim.Render(fmt.Sprintf("gt rig remove %s --force", name)))
			return fmt.Errorf("refusing to remove rig with running sessions")
		}

		// --force: kill all rig sessions (WARNING: may lose uncommitted work)
		fmt.Printf("Killing %d tmux session(s) for rig %s...\n", len(sessions), name)
		var killErrors []string
		for _, s := range sessions {
			if err := t.KillSessionWithProcesses(s); err != nil {
				fmt.Printf("  %s Failed to kill session %s: %v\n", style.Warning.Render("!"), s, err)
				killErrors = append(killErrors, s)
			} else {
				fmt.Printf("  Killed %s\n", s)
			}
		}
		if len(killErrors) > 0 {
			return fmt.Errorf("aborting remove: failed to kill %d session(s) (%s); rig left registered to avoid orphaned sessions",
				len(killErrors), strings.Join(killErrors, ", "))
		}
	}

	if err := mgr.RemoveRig(name); err != nil {
		if errors.Is(err, rig.ErrRigNotFound) {
			rigPath := filepath.Join(townRoot, name)
			if info, statErr := os.Stat(rigPath); statErr == nil && info.IsDir() {
				fmt.Printf("%s Rig %q is not registered but directory exists at %s\n\n",
					style.Warning.Render("!"), name, rigPath)
				fmt.Printf("This is an inconsistent state. To fix it, either:\n")
				fmt.Printf("  Adopt the directory:  %s\n",
					style.Dim.Render(fmt.Sprintf("gt rig add %s --adopt", name)))
				fmt.Printf("  Delete the directory: %s\n",
					style.Dim.Render(fmt.Sprintf("rm -rf %s", rigPath)))
				return fmt.Errorf("rig %q not in registry but directory exists", name)
			}
			// Directory doesn't exist either — suggest similar rig names
			suggestions := suggest.FindSimilar(name, mgr.ListRigNames(), 3)
			return fmt.Errorf("removing rig: %s",
				suggest.FormatSuggestion("rig", name, suggestions, ""))
		}
		return fmt.Errorf("removing rig: %w", err)
	}

	// Save updated config
	if err := config.SaveRigsConfig(rigsPath, rigsConfig); err != nil {
		return fmt.Errorf("saving rigs config: %w", err)
	}

	// Remove rig from daemon.json patrol config (witness + refinery rigs arrays)
	if err := config.RemoveRigFromDaemonPatrols(townRoot, name); err != nil {
		// Non-fatal: daemon will stop spawning for this rig anyway since it's unregistered
		fmt.Printf("  %s Could not update daemon.json patrols: %v\n", style.Warning.Render("!"), err)
	}

	// Remove route from routes.jsonl (issue #899)
	if beadsPrefix != "" {
		if err := beads.RemoveRoute(townRoot, beadsPrefix+"-"); err != nil {
			// Non-fatal: log warning but continue
			fmt.Printf("  %s Could not remove route from routes.jsonl: %v\n", style.Warning.Render("!"), err)
		}
	}

	fmt.Printf("%s Rig %s removed from registry\n", style.Success.Render("✓"), name)
	fmt.Printf("\nNote: Files at %s were NOT deleted.\n", filepath.Join(townRoot, name))
	fmt.Printf("To delete: %s\n", style.Dim.Render(fmt.Sprintf("rm -rf %s", filepath.Join(townRoot, name))))

	return nil
}

// refreshCycleBindingsOnExistingSessions forces a refresh of the tmux C-b n/p
// cycle bindings on any existing session. This is needed after gt rig add so
// the new rig's prefix is included in the grep pattern.
// Non-fatal: failure only means existing sessions need a restart to pick up the
// new prefix.
func refreshCycleBindingsOnExistingSessions() {
	t := tmux.NewTmux()
	sessions, err := t.ListSessions()
	if err != nil || len(sessions) == 0 {
		return
	}
	// Refresh bindings using any existing session as context.
	// SetCycleBindings' stale-pattern check will detect the mismatch and re-bind.
	_ = t.SetCycleBindings(sessions[0])
}

func runRigAdopt(_ *cobra.Command, args []string) error {
	name := args[0]

	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rigs config
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{
			Version: 1,
			Rigs:    make(map[string]config.RigEntry),
		}
	}

	// Create rig manager
	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)

	fmt.Printf("Adopting existing rig %s...\n", style.Bold.Render(name))

	// Validate --url if provided
	if rigAddAdoptURL != "" && !isGitRemoteURL(rigAddAdoptURL) {
		return fmt.Errorf("invalid git URL %q: expected a remote URL (e.g. https://, git@host:, ssh://, s3://, file:///abs/path)", rigAddAdoptURL)
	}

	// Validate --push-url if provided
	rigAddPushURL = strings.TrimSpace(rigAddPushURL)
	if rigAddPushURL != "" && !isGitRemoteURL(rigAddPushURL) {
		return fmt.Errorf("invalid push URL %q: expected a remote URL (e.g. https://, git@host:, ssh://, s3://, file:///abs/path)", rigAddPushURL)
	}

	// Validate --upstream-url if provided
	rigAddUpstreamURL = strings.TrimSpace(rigAddUpstreamURL)
	if rigAddUpstreamURL != "" && !isGitRemoteURL(rigAddUpstreamURL) {
		return fmt.Errorf("invalid upstream URL %q: expected a remote URL (e.g. https://, git@host:, ssh://, s3://, file:///abs/path)", rigAddUpstreamURL)
	}

	// Register the existing rig
	result, err := mgr.RegisterRig(rig.RegisterRigOptions{
		Name:        name,
		GitURL:      rigAddAdoptURL,
		PushURL:     rigAddPushURL,
		UpstreamURL: rigAddUpstreamURL,
		BeadsPrefix: rigAddPrefix,
		Force:       rigAddAdoptForce,
	})
	if err != nil {
		return fmt.Errorf("adopting rig: %w", err)
	}

	// Save updated config
	if err := config.SaveRigsConfig(rigsPath, rigsConfig); err != nil {
		return fmt.Errorf("saving rigs config: %w", err)
	}

	// Add adopted rig to daemon.json patrol config (witness + refinery rigs arrays)
	if err := config.AddRigToDaemonPatrols(townRoot, name); err != nil {
		fmt.Printf("  %s Could not update daemon.json patrols: %v\n", style.Warning.Render("!"), err)
	}

	// Add route to town-level routes.jsonl for prefix-based routing
	if result.BeadsPrefix != "" {
		routePath := name
		mayorRigBeads := filepath.Join(townRoot, name, "mayor", "rig", ".beads")
		if _, err := os.Stat(mayorRigBeads); err == nil {
			routePath = name + "/mayor/rig"
		}
		route := beads.Route{
			Prefix: result.BeadsPrefix + "-",
			Path:   routePath,
		}
		if err := beads.AppendRoute(townRoot, route); err != nil {
			fmt.Printf("  %s Could not update routes.jsonl: %v\n", style.Warning.Render("!"), err)
		}
	}

	// Commit town-level config changes (rigs.json, daemon.json, routes.jsonl)
	// so they aren't reverted by git restore/checkout operations.
	commitTownConfigChanges(townRoot, name)

	// Check for tracked beads and initialize database if missing (Issue #72)
	rigPath := filepath.Join(townRoot, name)
	beadsDirCandidates := []string{
		filepath.Join(rigPath, ".beads"),
		filepath.Join(rigPath, "mayor", "rig", ".beads"),
	}
	foundBeadsCandidate := false
	for _, beadsDir := range beadsDirCandidates {
		if _, err := os.Stat(beadsDir); err != nil {
			continue
		}
		foundBeadsCandidate = true

		// Detect prefix from Dolt metadata: try "bd config get issue_prefix" first,
		// then extract from metadata.json dolt_database name as fallback.
		// metadata.json survives clone (dolt/ is gitignored since bd v0.50+).
		prefixDetected := false
		metadataPath := filepath.Join(beadsDir, "metadata.json")
		if metaBytes, readErr := os.ReadFile(metadataPath); readErr == nil {
			var meta struct {
				Backend string `json:"backend"`
			}
			if json.Unmarshal(metaBytes, &meta) == nil && meta.Backend == "dolt" {
				workDir := filepath.Dir(beadsDir)
				bdCmd := exec.Command("bd", "config", "get", "issue_prefix")
				bdCmd.Dir = workDir
				if out, bdErr := bdCmd.Output(); bdErr == nil {
					detected := strings.TrimSpace(string(out))
					if detected != "" {
						if rigAddPrefix != "" && strings.TrimSuffix(rigAddPrefix, "-") != detected {
							return fmt.Errorf("prefix mismatch: source repo uses '%s' but --prefix '%s' was provided", detected, rigAddPrefix)
						}
						if result.BeadsPrefix == "" {
							result.BeadsPrefix = detected
						}
						prefixDetected = true
					}
				}
				// Fallback: extract prefix from dolt_database name in metadata.json.
				// Format: "beads_<prefix>" (e.g. "beads_my_project" → "my_project").
				// This survives clone because metadata.json is tracked by git.
				if !prefixDetected {
					var fullMeta struct {
						DoltDatabase string `json:"dolt_database"`
					}
					if json.Unmarshal(metaBytes, &fullMeta) == nil && strings.HasPrefix(fullMeta.DoltDatabase, "beads_") {
						detected := strings.TrimPrefix(fullMeta.DoltDatabase, "beads_")
						if detected != "" {
							if rigAddPrefix != "" && strings.TrimSuffix(rigAddPrefix, "-") != detected {
								return fmt.Errorf("prefix mismatch: source repo uses '%s' but --prefix '%s' was provided", detected, rigAddPrefix)
							}
							if result.BeadsPrefix == "" {
								result.BeadsPrefix = detected
							}
							prefixDetected = true
						}
					}
				}
			}
		}

		// Re-init database if metadata.json is missing or dolt/ directory is missing.
		// Since bd v0.50+, dolt/ is gitignored and won't exist after clone.
		// Use mgr.InitBeads() for consistency with the non-adopt path — it handles
		// BEADS_DIR env isolation, prefix validation, custom types config, tracked-beads
		// redirect, and fallback config creation.
		metadataPath = filepath.Join(beadsDir, "metadata.json")
		needsInit := false
		if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
			needsInit = true
		} else if metaBytes, readErr := os.ReadFile(metadataPath); readErr == nil {
			var meta struct {
				Backend string `json:"backend"`
			}
			if json.Unmarshal(metaBytes, &meta) == nil && meta.Backend == "dolt" {
				doltDir := filepath.Join(beadsDir, "dolt")
				if _, statErr := os.Stat(doltDir); os.IsNotExist(statErr) {
					needsInit = true
				}
			}
		}
		if needsInit {
			prefix := result.BeadsPrefix
			if prefix == "" {
				break
			}
			// Dolt server is required for beads init.
			if running, _, sErr := doltserver.IsRunning(townRoot); sErr != nil || !running {
				fmt.Printf("  %s Could not init bd database: Dolt server is not running\n", style.Warning.Render("!"))
				break
			}
			if err := mgr.InitBeads(rigPath, prefix, name); err != nil {
				fmt.Printf("  %s Could not init bd database: %v\n", style.Warning.Render("!"), err)
			} else {
				fmt.Printf("  %s Initialized beads database (Dolt)\n", style.Success.Render("✓"))
			}
		}
		break
	}

	// If no existing .beads/ candidate was found, initialize a fresh database
	// to match the behavior of the normal (non-adopt) gt rig add path.
	if !foundBeadsCandidate && result.BeadsPrefix != "" {
		// Dolt server is required for beads init.
		if running, _, sErr := doltserver.IsRunning(townRoot); sErr != nil || !running {
			fmt.Printf("  %s Could not init beads database: Dolt server is not running\n", style.Warning.Render("!"))
		} else if err := mgr.InitBeads(rigPath, result.BeadsPrefix, name); err != nil {
			fmt.Printf("  %s Could not init beads database: %v\n", style.Warning.Render("!"), err)
		} else {
			fmt.Printf("  %s Initialized beads database\n", style.Success.Render("✓"))
		}
	}

	// Create rig identity bead if prefix is set
	if result.BeadsPrefix != "" {
		mayorRigBeads := filepath.Join(rigPath, "mayor", "rig", ".beads")
		beadsWorkDir := rigPath
		if _, err := os.Stat(mayorRigBeads); err == nil {
			beadsWorkDir = filepath.Join(rigPath, "mayor", "rig")
		}

		bd := beads.New(beadsWorkDir)
		rigBeadID := beads.RigBeadIDWithPrefix(result.BeadsPrefix, name)

		// Check if bead already exists
		if _, err := bd.Show(rigBeadID); err != nil {
			fields := &beads.RigFields{
				Repo:   result.GitURL,
				Prefix: result.BeadsPrefix,
				State:  beads.RigStateActive,
			}
			if _, err := bd.CreateRigBead(name, fields); err != nil {
				fmt.Printf("  %s Could not create rig identity bead: %v\n", style.Warning.Render("!"), err)
			} else {
				fmt.Printf("  %s Created rig identity bead: %s\n", style.Success.Render("✓"), rigBeadID)
			}
		}

		// Create agent beads for the rig (witness, refinery)
		// This ensures they exist before the daemon tries to start them
		prefix := result.BeadsPrefix
		witnessID := beads.WitnessBeadIDWithPrefix(prefix, name)
		if _, err := bd.Show(witnessID); err != nil {
			if _, err := bd.CreateAgentBead(witnessID,
				fmt.Sprintf("Witness for %s - monitors polecat health and progress.", name),
				&beads.AgentFields{RoleType: "witness", Rig: name, AgentState: "idle"},
			); err != nil {
				fmt.Printf("  %s Could not create witness agent bead: %v\n", style.Warning.Render("!"), err)
			} else {
				fmt.Printf("  %s Created agent bead: %s\n", style.Success.Render("✓"), witnessID)
			}
		}

		refineryID := beads.RefineryBeadIDWithPrefix(prefix, name)
		if _, err := bd.Show(refineryID); err != nil {
			if _, err := bd.CreateAgentBead(refineryID,
				fmt.Sprintf("Refinery for %s - processes merge queue.", name),
				&beads.AgentFields{RoleType: "refinery", Rig: name, AgentState: "idle"},
			); err != nil {
				fmt.Printf("  %s Could not create refinery agent bead: %v\n", style.Warning.Render("!"), err)
			} else {
				fmt.Printf("  %s Created agent bead: %s\n", style.Success.Render("✓"), refineryID)
			}
		}
	}

	// Auto-assign a namepool theme that doesn't collide with other rigs (gas-21k).
	autoAssignNamepoolTheme(townRoot, name, mgr)

	// Ensure hooks-base.json exists and sync hooks for the adopted rig.
	ensureHooksBase()
	if err := syncRigHooks(townRoot, name); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to sync hooks for adopted rig: %v\n", err)
	}

	// Print results
	fmt.Printf("\n%s Rig %s adopted\n", style.Success.Render("✓"), name)
	if result.FromConfig {
		fmt.Printf("  %s Read configuration from existing config.json\n", style.Dim.Render("ℹ"))
	}
	fmt.Printf("  Repository: %s\n", result.GitURL)
	fmt.Printf("  Prefix: %s\n", result.BeadsPrefix)
	if result.DefaultBranch != "" {
		fmt.Printf("  Default branch: %s\n", result.DefaultBranch)
	}

	return nil
}

func runRigReset(cmd *cobra.Command, args []string) error {
	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// Determine role to reset
	roleKey := rigResetRole
	if roleKey == "" {
		// Auto-detect using env-aware role detection
		roleInfo, err := GetRoleWithContext(cwd, townRoot)
		if err != nil {
			return fmt.Errorf("detecting role: %w", err)
		}
		if roleInfo.Role == RoleUnknown {
			return fmt.Errorf("could not detect role; use --role to specify")
		}
		roleKey = string(roleInfo.Role)
	}

	// If no specific flags, reset all; otherwise only reset what's specified
	resetAll := !rigResetHandoff && !rigResetMail && !rigResetStale

	// Town beads for handoff/mail operations
	townBd := beads.New(townRoot)
	// Rig beads for issue operations (uses cwd to find .beads/)
	rigBd := beads.New(cwd)

	// Reset handoff content
	if resetAll || rigResetHandoff {
		if err := townBd.ClearHandoffContent(roleKey); err != nil {
			return fmt.Errorf("clearing handoff content: %w", err)
		}
		fmt.Printf("%s Cleared handoff content for %s\n", style.Success.Render("✓"), roleKey)
	}

	// Clear stale mail messages
	if resetAll || rigResetMail {
		result, err := townBd.ClearMail("Cleared during reset")
		if err != nil {
			return fmt.Errorf("clearing mail: %w", err)
		}
		if result.Closed > 0 || result.Cleared > 0 {
			fmt.Printf("%s Cleared mail: %d closed, %d pinned cleared\n",
				style.Success.Render("✓"), result.Closed, result.Cleared)
		} else {
			fmt.Printf("%s No mail to clear\n", style.Success.Render("✓"))
		}
	}

	// Reset stale in_progress issues
	if resetAll || rigResetStale {
		if err := runResetStale(rigBd, rigResetDryRun); err != nil {
			return fmt.Errorf("resetting stale issues: %w", err)
		}
	}

	return nil
}

// runResetStale resets in_progress issues whose assigned agent no longer has a session.
func runResetStale(bd *beads.Beads, dryRun bool) error {
	t := tmux.NewTmux()

	// Get all in_progress issues
	issues, err := bd.List(beads.ListOptions{
		Status:   "in_progress",
		Priority: -1, // All priorities
	})
	if err != nil {
		return fmt.Errorf("listing in_progress issues: %w", err)
	}

	if len(issues) == 0 {
		fmt.Printf("%s No in_progress issues found\n", style.Success.Render("✓"))
		return nil
	}

	var resetCount, skippedCount int
	var resetIssues []string

	for _, issue := range issues {
		if issue.Assignee == "" {
			continue // No assignee to check
		}

		// Parse assignee: rig/name or rig/crew/name
		sessionName, isPersistent := assigneeToSessionName(issue.Assignee)
		if sessionName == "" {
			continue // Couldn't parse assignee
		}

		// Check if session exists
		hasSession, err := t.HasSession(sessionName)
		if err != nil {
			// tmux error, skip this one
			continue
		}

		if hasSession {
			continue // Session exists, not stale
		}

		// For crew (persistent identities), only reset if explicitly checking sessions
		if isPersistent {
			skippedCount++
			if dryRun {
				fmt.Printf("  %s: %s %s\n",
					style.Dim.Render(issue.ID),
					issue.Assignee,
					style.Dim.Render("(persistent, skipped)"))
			}
			continue
		}

		// Session doesn't exist - this is stale
		if dryRun {
			fmt.Printf("  %s: %s (no session) → open\n",
				style.Bold.Render(issue.ID),
				issue.Assignee)
		} else {
			// Reset status to open and clear assignee
			openStatus := "open"
			emptyAssignee := ""
			if err := bd.Update(issue.ID, beads.UpdateOptions{
				Status:   &openStatus,
				Assignee: &emptyAssignee,
			}); err != nil {
				fmt.Printf("  %s Failed to reset %s: %v\n",
					style.Warning.Render("⚠"),
					issue.ID, err)
				continue
			}
		}
		resetCount++
		resetIssues = append(resetIssues, issue.ID)
	}

	if dryRun {
		if resetCount > 0 || skippedCount > 0 {
			fmt.Printf("\n%s Would reset %d issues, skip %d persistent\n",
				style.Dim.Render("(dry-run)"),
				resetCount, skippedCount)
		} else {
			fmt.Printf("%s No stale issues found\n", style.Success.Render("✓"))
		}
	} else {
		if resetCount > 0 {
			fmt.Printf("%s Reset %d stale issues: %v\n",
				style.Success.Render("✓"),
				resetCount, resetIssues)
		} else {
			fmt.Printf("%s No stale issues to reset\n", style.Success.Render("✓"))
		}
		if skippedCount > 0 {
			fmt.Printf("  Skipped %d persistent (crew) issues\n", skippedCount)
		}
	}

	return nil
}

// assigneeToSessionName converts an assignee (rig/name, rig/crew/name, or rig/polecats/name)
// to tmux session name.
// Returns the session name and whether this is a persistent identity (crew).
func assigneeToSessionName(assignee string) (sessionName string, isPersistent bool) {
	parts := strings.Split(assignee, "/")

	switch len(parts) {
	case 2:
		// rig/polecatName -> gt-rig-polecatName
		return session.PolecatSessionName(session.PrefixFor(parts[0]), parts[1]), false
	case 3:
		// rig/crew/name -> gt-rig-crew-name
		if parts[1] == "crew" {
			return session.CrewSessionName(session.PrefixFor(parts[0]), parts[2]), true
		}
		// rig/polecats/name -> gt-rig-name
		if parts[1] == "polecats" {
			return session.PolecatSessionName(session.PrefixFor(parts[0]), parts[2]), false
		}
		// Other 3-part formats not recognized
		return "", false
	default:
		return "", false
	}
}

// Helper to check if path exists
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runRigBoot(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rigs config and get rig
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(rigName)
	if err != nil {
		return fmt.Errorf("rig '%s' not found", rigName)
	}

	// Check if rig is parked or docked (uses bead labels + wisp state)
	if blocked, reason := IsRigParkedOrDocked(townRoot, rigName); blocked {
		return fmt.Errorf("rig '%s' is %s - use 'gt rig unpark' or 'gt rig undock' first", rigName, reason)
	}

	fmt.Printf("Booting rig %s...\n", style.Bold.Render(rigName))

	var started []string
	var skipped []string

	t := tmux.NewTmux()

	// 1. Start the witness
	// Check actual tmux session, not state file (may be stale)
	witnessSession := session.WitnessSessionName(session.PrefixFor(rigName))
	witnessRunning, _ := t.HasSession(witnessSession)
	if witnessRunning {
		skipped = append(skipped, "witness (already running)")
	} else {
		fmt.Printf("  Starting witness...\n")
		witMgr := witness.NewManager(r)
		if err := witMgr.Start(false, "", nil); err != nil {
			if err == witness.ErrAlreadyRunning {
				skipped = append(skipped, "witness (already running)")
			} else {
				return fmt.Errorf("starting witness: %w", err)
			}
		} else {
			started = append(started, "witness")
		}
	}

	// 2. Start the refinery
	// Check actual tmux session, not state file (may be stale)
	refinerySession := session.RefinerySessionName(session.PrefixFor(rigName))
	refineryRunning, _ := t.HasSession(refinerySession)
	if refineryRunning {
		skipped = append(skipped, "refinery (already running)")
	} else {
		fmt.Printf("  Starting refinery...\n")
		refMgr := refinery.NewManager(r)
		if err := refMgr.Start(false, ""); err != nil { // false = background mode
			return fmt.Errorf("starting refinery: %w", err)
		}
		started = append(started, "refinery")
	}

	// Report results
	if len(started) > 0 {
		fmt.Printf("%s Started: %s\n", style.Success.Render("✓"), strings.Join(started, ", "))
	}
	if len(skipped) > 0 {
		fmt.Printf("%s Skipped: %s\n", style.Dim.Render("•"), strings.Join(skipped, ", "))
	}

	return nil
}

func runRigStart(cmd *cobra.Command, args []string) error {
	// Find workspace once
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rigs config
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	t := tmux.NewTmux()

	var successRigs []string
	var failedRigs []string

	for _, rigName := range args {
		r, err := rigMgr.GetRig(rigName)
		if err != nil {
			fmt.Printf("%s Rig '%s' not found\n", style.Warning.Render("⚠"), rigName)
			failedRigs = append(failedRigs, rigName)
			continue
		}

		// Check if rig is parked or docked (uses bead labels + wisp state)
		if blocked, reason := IsRigParkedOrDocked(townRoot, rigName); blocked {
			fmt.Printf("%s Rig '%s' is %s - skipping (use 'gt rig unpark' or 'gt rig undock' first)\n",
				style.Warning.Render("⚠"), rigName, reason)
			continue
		}

		fmt.Printf("Starting rig %s...\n", style.Bold.Render(rigName))

		var started []string
		var skipped []string
		hasError := false

		// 1. Start the witness
		witnessSession := session.WitnessSessionName(session.PrefixFor(rigName))
		witnessRunning, _ := t.HasSession(witnessSession)
		if witnessRunning {
			skipped = append(skipped, "witness")
		} else {
			fmt.Printf("  Starting witness...\n")
			witMgr := witness.NewManager(r)
			if err := witMgr.Start(false, "", nil); err != nil {
				if err == witness.ErrAlreadyRunning {
					skipped = append(skipped, "witness")
				} else {
					fmt.Printf("  %s Failed to start witness: %v\n", style.Warning.Render("⚠"), err)
					hasError = true
				}
			} else {
				started = append(started, "witness")
			}
		}

		// 2. Start the refinery
		refinerySession := session.RefinerySessionName(session.PrefixFor(rigName))
		refineryRunning, _ := t.HasSession(refinerySession)
		if refineryRunning {
			skipped = append(skipped, "refinery")
		} else {
			fmt.Printf("  Starting refinery...\n")
			refMgr := refinery.NewManager(r)
			if err := refMgr.Start(false, ""); err != nil {
				fmt.Printf("  %s Failed to start refinery: %v\n", style.Warning.Render("⚠"), err)
				hasError = true
			} else {
				started = append(started, "refinery")
			}
		}

		// Report results for this rig
		if len(started) > 0 {
			fmt.Printf("  %s Started: %s\n", style.Success.Render("✓"), strings.Join(started, ", "))
		}
		if len(skipped) > 0 {
			fmt.Printf("  %s Skipped: %s (already running)\n", style.Dim.Render("•"), strings.Join(skipped, ", "))
		}

		if hasError {
			failedRigs = append(failedRigs, rigName)
		} else {
			successRigs = append(successRigs, rigName)
		}
		fmt.Println()
	}

	// Summary
	if len(successRigs) > 0 {
		fmt.Printf("%s Started rigs: %s\n", style.Success.Render("✓"), strings.Join(successRigs, ", "))
	}
	if len(failedRigs) > 0 {
		fmt.Printf("%s Failed rigs: %s\n", style.Warning.Render("⚠"), strings.Join(failedRigs, ", "))
		return fmt.Errorf("some rigs failed to start")
	}

	return nil
}

func runRigShutdown(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rigs config and get rig
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(rigName)
	if err != nil {
		return fmt.Errorf("rig '%s' not found", rigName)
	}

	// Check all polecats for uncommitted work (unless nuclear)
	if !rigShutdownNuclear && !checkUncommittedWork(r, rigName, "shutdown", rigShutdownForce) {
		return fmt.Errorf("refusing to shutdown with uncommitted work")
	}

	fmt.Printf("Shutting down rig %s...\n", style.Bold.Render(rigName))

	var errors []string

	// 1. Stop all polecat sessions
	t := tmux.NewTmux()
	polecatMgr := polecat.NewSessionManager(t, r)
	infos, err := polecatMgr.ListPolecats()
	if err == nil && len(infos) > 0 {
		fmt.Printf("  Stopping %d polecat session(s)...\n", len(infos))
		if err := polecatMgr.StopAll(rigShutdownForce); err != nil {
			errors = append(errors, fmt.Sprintf("polecat sessions: %v", err))
		}
	}

	// 2. Stop the refinery
	refMgr := refinery.NewManager(r)
	if running, _ := refMgr.IsRunning(); running {
		fmt.Printf("  Stopping refinery...\n")
		if err := refMgr.Stop(); err != nil {
			errors = append(errors, fmt.Sprintf("refinery: %v", err))
		}
	}

	// 3. Stop the witness
	witMgr := witness.NewManager(r)
	if running, _ := witMgr.IsRunning(); running {
		fmt.Printf("  Stopping witness...\n")
		if err := witMgr.Stop(); err != nil {
			errors = append(errors, fmt.Sprintf("witness: %v", err))
		}
	}

	if len(errors) > 0 {
		fmt.Printf("\n%s Some agents failed to stop:\n", style.Warning.Render("⚠"))
		for _, e := range errors {
			fmt.Printf("  - %s\n", e)
		}
		return fmt.Errorf("shutdown incomplete")
	}

	fmt.Printf("%s Rig %s shut down successfully\n", style.Success.Render("✓"), rigName)
	return nil
}

func runRigReboot(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	fmt.Printf("Rebooting rig %s...\n\n", style.Bold.Render(rigName))

	// Propagate reboot flags to shutdown globals
	rigShutdownForce = rigRebootForce
	rigShutdownNuclear = rigRebootNuclear

	// Shutdown first
	if err := runRigShutdown(cmd, args); err != nil {
		// If shutdown fails due to uncommitted work, propagate the error
		return err
	}

	fmt.Println() // Blank line between shutdown and boot

	// Boot
	if err := runRigBoot(cmd, args); err != nil {
		return fmt.Errorf("boot failed: %w", err)
	}

	fmt.Printf("\n%s Rig %s rebooted successfully\n", style.Success.Render("✓"), rigName)
	return nil
}

func runRigStatus(cmd *cobra.Command, args []string) error {
	var rigName string

	if len(args) > 0 {
		rigName = args[0]
	} else {
		// Infer rig from current directory
		roleInfo, err := GetRole()
		if err != nil {
			return fmt.Errorf("detecting rig from current directory: %w", err)
		}
		if roleInfo.Rig == "" {
			return fmt.Errorf("could not detect rig from current directory; please specify rig name")
		}
		rigName = roleInfo.Rig
	}

	// Get rig
	townRoot, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	t := tmux.NewTmux()

	// Header
	fmt.Printf("%s\n", style.Bold.Render(rigName))

	// Operational state
	opState, opSource := getRigOperationalState(townRoot, rigName)
	if opState == "OPERATIONAL" {
		fmt.Printf("  Status: %s\n", style.Success.Render(opState))
	} else if opState == "PARKED" {
		fmt.Printf("  Status: %s (%s)\n", style.Warning.Render(opState), opSource)
	} else if opState == "DOCKED" {
		fmt.Printf("  Status: %s (%s)\n", style.Dim.Render(opState), opSource)
	}

	fmt.Printf("  Path: %s\n", r.Path)
	if r.Config != nil && r.Config.Prefix != "" {
		fmt.Printf("  Beads prefix: %s-\n", r.Config.Prefix)
	}
	fmt.Println()

	// --- Parallel data gathering phase ---
	// All expensive operations (tmux health checks, beads queries, git status)
	// run concurrently. Display phase follows with pre-fetched data.
	var dataWg sync.WaitGroup

	// Witness status
	witMgr := witness.NewManager(r)
	var witnessRunning bool
	dataWg.Add(1)
	go func() {
		defer dataWg.Done()
		witnessRunning, _ = witMgr.IsRunning()
	}()

	// Refinery status + queue
	refMgr := refinery.NewManager(r)
	var refineryRunning bool
	var refineryQueue []refinery.QueueItem
	dataWg.Add(1)
	go func() {
		defer dataWg.Done()
		refineryRunning, _ = refMgr.IsRunning()
		if refineryRunning {
			refineryQueue, _ = refMgr.Queue()
		}
	}()

	// Polecats list (involves per-polecat beads + git queries)
	polecatGit := git.NewGit(r.Path)
	polecatMgr := polecat.NewManager(r, polecatGit, t)
	var polecats []*polecat.Polecat
	var polecatsErr error
	dataWg.Add(1)
	go func() {
		defer dataWg.Done()
		polecats, polecatsErr = polecatMgr.List()
	}()

	// Crew list
	crewMgr := crew.NewManager(r, git.NewGit(townRoot))
	var crewWorkers []*crew.CrewWorker
	var crewErr error
	dataWg.Add(1)
	go func() {
		defer dataWg.Done()
		crewWorkers, crewErr = crewMgr.List()
	}()

	dataWg.Wait()

	// --- Polecat + Crew session checks (parallel, after List completes) ---
	type polecatInfo struct {
		name       string
		state      polecat.State
		issue      string
		hasSession bool
	}
	var pInfos []polecatInfo
	type crewInfo struct {
		name       string
		hasSession bool
		branch     string
		dirty      bool
	}
	var cInfos []crewInfo

	var sessionWg sync.WaitGroup

	if polecatsErr == nil && len(polecats) > 0 {
		pInfos = make([]polecatInfo, len(polecats))
		for i, p := range polecats {
			pInfos[i] = polecatInfo{name: p.Name, state: p.State, issue: p.Issue}
			sessionWg.Add(1)
			go func(idx int, p *polecat.Polecat) {
				defer sessionWg.Done()
				sessionName := session.PolecatSessionName(session.PrefixFor(rigName), p.Name)
				pInfos[idx].hasSession, _ = t.HasSession(sessionName)
			}(i, p)
		}
	}

	if crewErr == nil && len(crewWorkers) > 0 {
		cInfos = make([]crewInfo, len(crewWorkers))
		for i, w := range crewWorkers {
			cInfos[i] = crewInfo{name: w.Name}
			sessionWg.Add(1)
			go func(idx int, w *crew.CrewWorker) {
				defer sessionWg.Done()
				sessionName := crewSessionName(rigName, w.Name)
				cInfos[idx].hasSession, _ = t.HasSession(sessionName)
				crewGit := git.NewGit(w.ClonePath)
				cInfos[idx].branch, _ = crewGit.CurrentBranch()
				gitStatus, _ := crewGit.Status()
				if gitStatus != nil && !gitStatus.Clean {
					cInfos[idx].dirty = true
				}
			}(i, w)
		}
	}

	sessionWg.Wait()

	// --- Display phase (all data pre-fetched) ---

	// Witness
	fmt.Printf("%s\n", style.Bold.Render("Witness"))
	if witnessRunning {
		fmt.Printf("  %s running\n", style.Success.Render("●"))
	} else {
		fmt.Printf("  %s stopped\n", style.Dim.Render("○"))
	}
	fmt.Println()

	// Refinery
	fmt.Printf("%s\n", style.Bold.Render("Refinery"))
	if refineryRunning {
		fmt.Printf("  %s running\n", style.Success.Render("●"))
		if len(refineryQueue) > 0 {
			fmt.Printf("  Queue: %d items\n", len(refineryQueue))
		}
	} else {
		fmt.Printf("  %s stopped\n", style.Dim.Render("○"))
	}
	fmt.Println()

	// Polecats
	fmt.Printf("%s", style.Bold.Render("Polecats"))
	if polecatsErr != nil || len(polecats) == 0 {
		fmt.Printf(" (none)\n")
	} else {
		fmt.Printf(" (%d)\n", len(polecats))
		for _, pi := range pInfos {
			sessionIcon := style.Dim.Render("○")
			if pi.hasSession {
				sessionIcon = style.Success.Render("●")
			}

			// Reconcile display state with tmux session liveness.
			// Per gt-zecmc design: tmux is ground truth for observable states.
			// If session is running but beads says done, the polecat is still alive.
			// If session is dead but beads says working, show "stalled" so the
			// witness can detect unsubmitted work (gt-3071b). Previously this
			// showed "done" which masked failures where polecats died before
			// running gt done, leaving work stranded in worktrees.
			displayState := pi.state
			if pi.hasSession && displayState == polecat.StateDone {
				displayState = polecat.StateWorking
			} else if !pi.hasSession && displayState == polecat.StateWorking {
				displayState = polecat.StateStalled
			}

			stateStr := string(displayState)
			if pi.issue != "" {
				stateStr = fmt.Sprintf("%s → %s", displayState, pi.issue)
			}

			fmt.Printf("  %s %s: %s\n", sessionIcon, pi.name, stateStr)
		}
	}
	fmt.Println()

	// Crew
	fmt.Printf("%s", style.Bold.Render("Crew"))
	if crewErr != nil || len(crewWorkers) == 0 {
		fmt.Printf(" (none)\n")
	} else {
		fmt.Printf(" (%d)\n", len(crewWorkers))
		for _, ci := range cInfos {
			sessionIcon := style.Dim.Render("○")
			if ci.hasSession {
				sessionIcon = style.Success.Render("●")
			}

			gitInfo := ""
			if ci.dirty {
				gitInfo = style.Warning.Render(" (dirty)")
			}

			fmt.Printf("  %s %s: %s%s\n", sessionIcon, ci.name, ci.branch, gitInfo)
		}
	}

	return nil
}

func runRigStop(cmd *cobra.Command, args []string) error {
	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rigs config
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)

	// Track results
	var succeeded []string
	var failed []string

	// Process each rig
	for _, rigName := range args {
		r, err := rigMgr.GetRig(rigName)
		if err != nil {
			fmt.Printf("%s Rig '%s' not found\n", style.Warning.Render("⚠"), rigName)
			failed = append(failed, rigName)
			continue
		}

		// Check all polecats for uncommitted work (unless nuclear)
		if !rigStopNuclear && !checkUncommittedWork(r, rigName, "stop", rigStopForce) {
			failed = append(failed, rigName)
			continue
		}

		fmt.Printf("Stopping rig %s...\n", style.Bold.Render(rigName))

		var errors []string

		// 1. Stop all polecat sessions
		t := tmux.NewTmux()
		polecatMgr := polecat.NewSessionManager(t, r)
		infos, err := polecatMgr.ListPolecats()
		if err == nil && len(infos) > 0 {
			fmt.Printf("  Stopping %d polecat session(s)...\n", len(infos))
			if err := polecatMgr.StopAll(rigStopForce); err != nil {
				errors = append(errors, fmt.Sprintf("polecat sessions: %v", err))
			}
		}

		// 2. Stop the refinery
		refMgr := refinery.NewManager(r)
		if running, _ := refMgr.IsRunning(); running {
			fmt.Printf("  Stopping refinery...\n")
			if err := refMgr.Stop(); err != nil {
				errors = append(errors, fmt.Sprintf("refinery: %v", err))
			}
		}

		// 3. Stop the witness
		witMgr := witness.NewManager(r)
		if running, _ := witMgr.IsRunning(); running {
			fmt.Printf("  Stopping witness...\n")
			if err := witMgr.Stop(); err != nil {
				errors = append(errors, fmt.Sprintf("witness: %v", err))
			}
		}

		if len(errors) > 0 {
			fmt.Printf("%s Some agents in %s failed to stop:\n", style.Warning.Render("⚠"), rigName)
			for _, e := range errors {
				fmt.Printf("  - %s\n", e)
			}
			failed = append(failed, rigName)
		} else {
			fmt.Printf("%s Rig %s stopped\n", style.Success.Render("✓"), rigName)
			succeeded = append(succeeded, rigName)
		}
	}

	// Summary
	if len(args) > 1 {
		fmt.Println()
		if len(succeeded) > 0 {
			fmt.Printf("%s Stopped: %s\n", style.Success.Render("✓"), strings.Join(succeeded, ", "))
		}
		if len(failed) > 0 {
			fmt.Printf("%s Failed: %s\n", style.Warning.Render("⚠"), strings.Join(failed, ", "))
			return fmt.Errorf("some rigs failed to stop")
		}
	} else if len(failed) > 0 {
		return fmt.Errorf("rig failed to stop")
	}

	return nil
}

func runRigRestart(cmd *cobra.Command, args []string) error {
	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rigs config
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	t := tmux.NewTmux()

	// Track results
	var succeeded []string
	var failed []string

	// Process each rig
	for _, rigName := range args {
		r, err := rigMgr.GetRig(rigName)
		if err != nil {
			fmt.Printf("%s Rig '%s' not found\n", style.Warning.Render("⚠"), rigName)
			failed = append(failed, rigName)
			continue
		}

		// Check all polecats for uncommitted work (unless nuclear)
		if !rigRestartNuclear && !checkUncommittedWork(r, rigName, "restart", rigRestartForce) {
			failed = append(failed, rigName)
			continue
		}

		fmt.Printf("Restarting rig %s...\n", style.Bold.Render(rigName))

		var stopErrors []string
		var startErrors []string

		// === STOP PHASE ===
		fmt.Printf("  Stopping...\n")

		// 1. Stop all polecat sessions
		polecatMgr := polecat.NewSessionManager(t, r)
		infos, err := polecatMgr.ListPolecats()
		if err == nil && len(infos) > 0 {
			fmt.Printf("    Stopping %d polecat session(s)...\n", len(infos))
			if err := polecatMgr.StopAll(rigRestartForce); err != nil {
				stopErrors = append(stopErrors, fmt.Sprintf("polecat sessions: %v", err))
			}
		}

		// 2. Stop the refinery
		refMgr := refinery.NewManager(r)
		if running, _ := refMgr.IsRunning(); running {
			fmt.Printf("    Stopping refinery...\n")
			if err := refMgr.Stop(); err != nil {
				stopErrors = append(stopErrors, fmt.Sprintf("refinery: %v", err))
			}
		}

		// 3. Stop the witness
		witMgr := witness.NewManager(r)
		if running, _ := witMgr.IsRunning(); running {
			fmt.Printf("    Stopping witness...\n")
			if err := witMgr.Stop(); err != nil {
				stopErrors = append(stopErrors, fmt.Sprintf("witness: %v", err))
			}
		}

		if len(stopErrors) > 0 {
			fmt.Printf("  %s Stop errors:\n", style.Warning.Render("⚠"))
			for _, e := range stopErrors {
				fmt.Printf("    - %s\n", e)
			}
			failed = append(failed, rigName)
			continue
		}

		// === START PHASE ===
		fmt.Printf("  Starting...\n")

		var started []string
		var skipped []string

		// 1. Start the witness
		witnessSession := session.WitnessSessionName(session.PrefixFor(rigName))
		witnessRunning, _ := t.HasSession(witnessSession)
		if witnessRunning {
			skipped = append(skipped, "witness")
		} else {
			fmt.Printf("    Starting witness...\n")
			if err := witMgr.Start(false, "", nil); err != nil {
				if err == witness.ErrAlreadyRunning {
					skipped = append(skipped, "witness")
				} else {
					fmt.Printf("    %s Failed to start witness: %v\n", style.Warning.Render("⚠"), err)
					startErrors = append(startErrors, fmt.Sprintf("witness: %v", err))
				}
			} else {
				started = append(started, "witness")
			}
		}

		// 2. Start the refinery
		refinerySession := session.RefinerySessionName(session.PrefixFor(rigName))
		refineryRunning, _ := t.HasSession(refinerySession)
		if refineryRunning {
			skipped = append(skipped, "refinery")
		} else {
			fmt.Printf("    Starting refinery...\n")
			if err := refMgr.Start(false, ""); err != nil {
				fmt.Printf("    %s Failed to start refinery: %v\n", style.Warning.Render("⚠"), err)
				startErrors = append(startErrors, fmt.Sprintf("refinery: %v", err))
			} else {
				started = append(started, "refinery")
			}
		}

		// Report results for this rig
		if len(started) > 0 {
			fmt.Printf("  %s Started: %s\n", style.Success.Render("✓"), strings.Join(started, ", "))
		}
		if len(skipped) > 0 {
			fmt.Printf("  %s Skipped: %s (already running)\n", style.Dim.Render("•"), strings.Join(skipped, ", "))
		}

		if len(startErrors) > 0 {
			fmt.Printf("  %s Start errors:\n", style.Warning.Render("⚠"))
			for _, e := range startErrors {
				fmt.Printf("    - %s\n", e)
			}
			failed = append(failed, rigName)
		} else {
			fmt.Printf("%s Rig %s restarted\n", style.Success.Render("✓"), rigName)
			succeeded = append(succeeded, rigName)
		}
		fmt.Println()
	}

	// Summary
	if len(args) > 1 {
		if len(succeeded) > 0 {
			fmt.Printf("%s Restarted: %s\n", style.Success.Render("✓"), strings.Join(succeeded, ", "))
		}
		if len(failed) > 0 {
			fmt.Printf("%s Failed: %s\n", style.Warning.Render("⚠"), strings.Join(failed, ", "))
			return fmt.Errorf("some rigs failed to restart")
		}
	} else if len(failed) > 0 {
		return fmt.Errorf("rig failed to restart")
	}

	return nil
}

// getRigOperationalState returns the operational state and source for a rig.
// It checks the wisp layer first (local/ephemeral), then rig bead labels (global).
// Returns state ("OPERATIONAL", "PARKED", or "DOCKED") and source ("local", "global - synced", or "default").
func getRigOperationalState(townRoot, rigName string) (state string, source string) {
	// Check wisp layer first (local/ephemeral overrides)
	wispConfig := wisp.NewConfig(townRoot, rigName)
	if status := wispConfig.GetString("status"); status != "" {
		switch strings.ToLower(status) {
		case "parked":
			return "PARKED", "local"
		case "docked":
			return "DOCKED", "local"
		}
	}

	// Check rig bead labels (global/synced)
	// Rig identity bead ID: <prefix>-rig-<name>
	// Look for status:docked or status:parked labels
	rigPath := filepath.Join(townRoot, rigName)
	rigBeadsDir := beads.ResolveBeadsDir(rigPath)
	bd := beads.NewWithBeadsDir(rigPath, rigBeadsDir)

	// Try to find the rig identity bead
	// Convention: <prefix>-rig-<rigName>
	// Try to get prefix from rig config.json, fall back to rigs.json registry
	var prefix string
	if rigCfg, err := rig.LoadRigConfig(rigPath); err == nil && rigCfg.Beads != nil {
		prefix = rigCfg.Beads.Prefix
	} else {
		// Fall back to registry (mayor/rigs.json) when config.json is missing
		prefix = config.GetRigPrefix(townRoot, rigName)
	}

	if prefix != "" {
		rigBeadID := fmt.Sprintf("%s-rig-%s", prefix, rigName)
		if issue, err := bd.Show(rigBeadID); err == nil {
			for _, label := range issue.Labels {
				if strings.HasPrefix(label, "status:") {
					statusValue := strings.TrimPrefix(label, "status:")
					switch strings.ToLower(statusValue) {
					case "docked":
						return "DOCKED", "global - synced"
					case "parked":
						return "PARKED", "global - synced"
					}
				}
			}
		}
	}

	// Default: operational
	return "OPERATIONAL", "default"
}

// ensureHooksBase creates ~/.gt/hooks-base.json from current defaults if it
// doesn't exist yet. Without this file, gt hooks diff has no reference point
// and cannot detect drift when default hooks change after initial setup.
// Non-fatal: missing base config is caught by gt doctor hooks-base-missing.
func ensureHooksBase() {
	if _, err := hooks.LoadBase(); err == nil {
		return // already exists
	}
	base := hooks.DefaultBase()
	if err := hooks.SaveBase(base); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: could not create hooks-base.json: %v\n", err)
		return
	}
	fmt.Printf("  Created hooks-base.json at %s\n", hooks.BasePath())
}

// syncRigHooks syncs hooks for a specific rig's targets after rig creation.
func syncRigHooks(townRoot, rigName string) error {
	targets, err := hooks.DiscoverTargets(townRoot)
	if err != nil {
		return err
	}

	synced := 0
	for _, target := range targets {
		if target.Rig != rigName {
			continue
		}
		if _, err := syncTarget(target, false); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: failed to sync hooks for %s: %v\n", target.DisplayKey(), err)
			continue
		}
		synced++
	}

	if synced > 0 {
		fmt.Printf("  Synced hooks for %d target(s)\n", synced)
	}
	return nil
}

// findRigSessions returns all tmux sessions belonging to the given rig.
// All rig sessions share the "<rigPrefix>-" prefix, so this catches witness,
// refinery, polecat, and crew sessions in one pass.
func findRigSessions(t *tmux.Tmux, rigName string) ([]string, error) {
	prefix := session.PrefixFor(rigName) + "-"
	all, err := t.ListSessions()
	if err != nil {
		return nil, fmt.Errorf("listing tmux sessions: %w", err)
	}
	var matches []string
	for _, name := range all {
		if strings.HasPrefix(name, prefix) {
			matches = append(matches, name)
		}
	}
	return matches, nil
}

// commitTownConfigChanges commits town-level config files (rigs.json, daemon.json,
// routes.jsonl) to the town repo after rig add/adopt. Without this commit, changes
// are silently reverted by any process that does a git restore/checkout.
func commitTownConfigChanges(townRoot, rigName string) {
	g := git.NewGit(townRoot)

	// Collect the town-level files that rig add/adopt modifies.
	files := []string{
		filepath.Join("mayor", "rigs.json"),
		filepath.Join("mayor", "daemon.json"),
		filepath.Join(".beads", "routes.jsonl"),
	}

	// Only stage files that actually exist (adopt may not touch all of them).
	var toAdd []string
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(townRoot, f)); err == nil {
			toAdd = append(toAdd, f)
		}
	}
	if len(toAdd) == 0 {
		return
	}

	if err := g.Add(toAdd...); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: could not stage town config files: %v\n", err)
		return
	}

	msg := fmt.Sprintf("chore: register rig %s in town config", rigName)
	if err := g.Commit(msg); err != nil {
		// If nothing changed (already committed), git commit returns an error — that's fine.
		if !strings.Contains(err.Error(), "nothing to commit") {
			fmt.Fprintf(os.Stderr, "  Warning: could not commit town config files: %v\n", err)
		}
	}
}

// isGitRemoteURL returns true if s looks like a remote git URL rather than a
// local path. Accepts any scheme:// URL (including file:// for explicit local
// mirrors) as well as SCP-style SSH URLs.
func isGitRemoteURL(s string) bool {
	// Reject flag-like strings (defense-in-depth against argument injection)
	if strings.HasPrefix(s, "-") {
		return false
	}
	// Reject absolute paths
	if strings.HasPrefix(s, "/") {
		return false
	}
	// Reject Windows-style paths (C:\...)
	if len(s) >= 3 && s[1] == ':' && (s[2] == '/' || s[2] == '\\') {
		return false
	}
	// Reject relative paths
	if strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return false
	}
	// Reject home-relative paths
	if strings.HasPrefix(s, "~/") {
		return false
	}
	// Accept any scheme:// URL where scheme is alphanumeric (plus + - .).
	// This covers https://, ssh://, git://, s3://, file://, codecommit://, etc.
	// Git invokes git-remote-<scheme> for non-builtin schemes.
	if idx := strings.Index(s, "://"); idx > 0 {
		scheme := s[:idx]
		for _, c := range scheme {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '+' || c == '-' || c == '.') {
				return false
			}
		}
		return true
	}
	// Accept SCP-style SSH URLs (user@host:path) where user and host are non-empty
	// and host contains no slashes (distinguishes from file:// or path-like strings)
	atIdx := strings.Index(s, "@")
	colonIdx := strings.Index(s, ":")
	if atIdx > 0 && colonIdx > atIdx+1 && !strings.Contains(s[:colonIdx], "/") {
		return true
	}
	return false
}

// autoAssignNamepoolTheme picks a namepool theme for a new rig that doesn't collide
// with themes already in use by other rigs. This ensures polecat names are unique
// across rigs (gas-21k). If all built-in themes are taken, falls back to hash-based
// selection where collisions are possible but unavoidable.
func autoAssignNamepoolTheme(townRoot, rigName string, mgr *rig.Manager) {
	usedThemes := mgr.UsedNamepoolThemes(polecat.ThemeForRig)
	chosenTheme := polecat.ThemeForRigAvoiding(rigName, usedThemes)
	settingsPath := filepath.Join(townRoot, rigName, "settings", "config.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		fmt.Printf("  %s Could not create settings directory: %v\n", style.Warning.Render("!"), err)
		return
	}
	rigSettings, err := config.LoadRigSettings(settingsPath)
	if err != nil {
		rigSettings = &config.RigSettings{
			Type:    "rig-settings",
			Version: 1,
		}
	}
	// Only set namepool theme if not already configured
	if rigSettings.Namepool != nil && rigSettings.Namepool.Style != "" {
		return
	}
	rigSettings.Namepool = &config.NamepoolConfig{
		Style: chosenTheme,
	}
	if err := config.SaveRigSettings(settingsPath, rigSettings); err != nil {
		fmt.Printf("  %s Could not save namepool theme: %v\n", style.Warning.Render("!"), err)
	} else {
		fmt.Printf("  Namepool theme: %s (auto-assigned for cross-rig uniqueness)\n", chosenTheme)
	}
}
