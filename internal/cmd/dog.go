package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/dog"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/plugin"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Dog command flags
var (
	dogListJSON   bool
	dogStatusJSON bool
	dogForce      bool
	dogRemoveAll  bool
	dogCallAll    bool

	// Dispatch flags
	dogDispatchPlugin string
	dogDispatchRig    string
	dogDispatchCreate bool
	dogDispatchDog    string
	dogDispatchJSON   bool
	dogDispatchDryRun bool

	// Health-check flags
	dogHealthJSON          bool
	dogHealthAutoClear     bool
	dogHealthMaxInactivity time.Duration
)

var dogCmd = &cobra.Command{
	Use:     "dog",
	Aliases: []string{"dogs"},
	GroupID: GroupAgents,
	Short:   "Manage dogs (cross-rig infrastructure workers)",
	Long: `Manage dogs - reusable workers for infrastructure and cleanup.

CATS VS DOGS:
  Polecats (cats) build features. One rig. Ephemeral sessions (one task, then nuked).
  Dogs clean up messes. Cross-rig. Reusable (multiple tasks, eventually recycled).

Dogs are managed by the Deacon for town-level work:
  - Infrastructure tasks (rebuilding, syncing, migrations)
  - Cleanup operations (orphan branches, stale files)
  - Cross-rig work that spans multiple projects

Each dog has worktrees into every configured rig, enabling cross-project
operations. Dogs return to idle state after completing work (unlike cats).

The kennel is at ~/gt/deacon/dogs/. The Deacon dispatches work to dogs.`,
}

var dogAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Create a new dog in the kennel",
	Long: `Create a new dog in the kennel with multi-rig worktrees.

Each dog gets a worktree per configured rig (e.g., gastown, beads).
The dog starts in idle state, ready to receive work from the Deacon.

Example:
  gt dog add alpha
  gt dog add bravo`,
	Args: cobra.ExactArgs(1),
	RunE: runDogAdd,
}

var dogRemoveCmd = &cobra.Command{
	Use:   "remove <name>... | --all",
	Short: "Remove dogs from the kennel",
	Long: `Remove one or more dogs from the kennel.

Removes all worktrees and the dog directory.
Use --force to remove even if dog is in working state.

Examples:
  gt dog remove alpha
  gt dog remove alpha bravo
  gt dog remove --all
  gt dog remove alpha --force`,
	Args: func(cmd *cobra.Command, args []string) error {
		if dogRemoveAll {
			return nil
		}
		if len(args) < 1 {
			return fmt.Errorf("requires at least 1 dog name (or use --all)")
		}
		return nil
	},
	RunE: runDogRemove,
}

var dogListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all dogs in the kennel",
	Long: `List all dogs in the kennel with their status.

Shows each dog's state (idle/working), current work assignment,
and last active timestamp.

Examples:
  gt dog list
  gt dog list --json`,
	RunE: runDogList,
}

var dogCallCmd = &cobra.Command{
	Use:   "call [name]",
	Short: "Wake idle dog(s) for work",
	Long: `Wake an idle dog to prepare for work.

With a name, wakes the specific dog.
With --all, wakes all idle dogs.
Without arguments, wakes one idle dog (if available).

This updates the dog's last-active timestamp and can trigger
session creation for the dog's worktrees.

Examples:
  gt dog call alpha
  gt dog call --all
  gt dog call`,
	RunE: runDogCall,
}

var dogDoneCmd = &cobra.Command{
	Use:   "done [name]",
	Short: "Mark dog as done and return to idle",
	Long: `Mark a dog as done with its current work and return to idle state.

Dogs should call this when they complete their work assignment.
This clears the work field and sets state to idle, making the dog
available for new work.

Without a name argument, auto-detects the current dog from the working
directory (must be run from within a dog's worktree).

Examples:
  gt dog done         # Auto-detect from cwd
  gt dog done alpha   # Explicit name`,
	Args: cobra.MaximumNArgs(1),
	RunE: runDogDone,
}

var dogClearCmd = &cobra.Command{
	Use:   "clear <name>",
	Short: "Reset a stuck dog to idle state",
	Long: `Reset a stuck dog to idle state.

Use this when a dog is stuck in "working" state but its session has died.
The Deacon uses this during patrol to clear dogs that have timed out.

By default, refuses to clear a dog if its tmux session still exists.
Use --force to clear even if the session is alive.

Examples:
  gt dog clear alpha           # Clear if session is dead
  gt dog clear alpha --force   # Force clear even if session exists`,
	Args: cobra.ExactArgs(1),
	RunE: runDogClear,
}

var dogStatusCmd = &cobra.Command{
	Use:   "status [name]",
	Short: "Show detailed dog status",
	Long: `Show detailed status for a specific dog or summary for all dogs.

With a name, shows detailed info including:
  - State (idle/working)
  - Current work assignment
  - Worktree paths per rig
  - Last active timestamp

Without a name, shows pack summary:
  - Total dogs
  - Idle/working counts
  - Pack health

Examples:
  gt dog status alpha
  gt dog status
  gt dog status --json`,
	RunE: runDogStatus,
}

var dogDispatchCmd = &cobra.Command{
	Use:   "dispatch --plugin <name>",
	Short: "Dispatch plugin execution to a dog",
	Long: `Dispatch a plugin for execution by a dog worker.

This is the formalized command for sending plugin work to dogs. The Deacon
uses this during patrol cycles to dispatch plugins with open gates.

The command:
1. Finds the plugin definition (plugin.md)
2. Assigns work to an idle dog (marks as working)
3. Sends mail with plugin instructions to the dog
4. Returns immediately (non-blocking)

The dog discovers the work via its mail inbox and executes the plugin
instructions. On completion, the dog sends DOG_DONE mail to deacon/.

Examples:
  gt dog dispatch --plugin rebuild-gt
  gt dog dispatch --plugin rebuild-gt --rig gastown
  gt dog dispatch --plugin rebuild-gt --dog alpha
  gt dog dispatch --plugin rebuild-gt --create
  gt dog dispatch --plugin rebuild-gt --dry-run
  gt dog dispatch --plugin rebuild-gt --json`,
	RunE: runDogDispatch,
}

var dogHealthCheckCmd = &cobra.Command{
	Use:   "health-check [name]",
	Short: "Check dog health (zombies, hung, orphans)",
	Long: `Check dog health and detect problems.

Detects:
  - Zombies: state=working but tmux session or agent process is dead
  - Hung: agent alive but no tmux activity for too long
  - Orphans: dog idle but tmux session still exists

With --auto-clear, zombies are automatically returned to idle state.
Hung dogs are reported only (Deacon decides per ZFC principle).

Exit codes:
  0 = all healthy
  1 = error
  2 = needs attention

Examples:
  gt dog health-check
  gt dog health-check alpha
  gt dog health-check --json
  gt dog health-check --auto-clear
  gt dog health-check --max-inactivity 1h`,
	Args: cobra.MaximumNArgs(1),
	RunE: runDogHealthCheck,
}

func init() {
	// List flags
	dogListCmd.Flags().BoolVar(&dogListJSON, "json", false, "Output as JSON")

	// Remove flags
	dogRemoveCmd.Flags().BoolVarP(&dogForce, "force", "f", false, "Force removal even if working")
	dogRemoveCmd.Flags().BoolVar(&dogRemoveAll, "all", false, "Remove all dogs")

	// Call flags
	dogCallCmd.Flags().BoolVar(&dogCallAll, "all", false, "Wake all idle dogs")

	// Clear flags (reuses dogForce from remove)
	dogClearCmd.Flags().BoolVarP(&dogForce, "force", "f", false, "Force clear even if session exists")

	// Status flags
	dogStatusCmd.Flags().BoolVar(&dogStatusJSON, "json", false, "Output as JSON")

	// Dispatch flags
	dogDispatchCmd.Flags().StringVar(&dogDispatchPlugin, "plugin", "", "Plugin name to dispatch (required)")
	dogDispatchCmd.Flags().StringVar(&dogDispatchRig, "rig", "", "Limit plugin search to specific rig")
	dogDispatchCmd.Flags().StringVar(&dogDispatchDog, "dog", "", "Dispatch to specific dog (default: any idle)")
	dogDispatchCmd.Flags().BoolVar(&dogDispatchCreate, "create", false, "Create a dog if none idle")
	dogDispatchCmd.Flags().BoolVar(&dogDispatchJSON, "json", false, "Output as JSON")
	dogDispatchCmd.Flags().BoolVarP(&dogDispatchDryRun, "dry-run", "n", false, "Show what would be done without doing it")
	_ = dogDispatchCmd.MarkFlagRequired("plugin")

	// Health-check flags
	dogHealthCheckCmd.Flags().BoolVar(&dogHealthJSON, "json", false, "Output as JSON")
	dogHealthCheckCmd.Flags().BoolVar(&dogHealthAutoClear, "auto-clear", false, "Auto-clear zombie dogs")
	dogHealthCheckCmd.Flags().DurationVar(&dogHealthMaxInactivity, "max-inactivity", 10*time.Minute, "Max inactivity before considering hung")

	// Add subcommands
	dogCmd.AddCommand(dogAddCmd)
	dogCmd.AddCommand(dogRemoveCmd)
	dogCmd.AddCommand(dogListCmd)
	dogCmd.AddCommand(dogCallCmd)
	dogCmd.AddCommand(dogClearCmd)
	dogCmd.AddCommand(dogDoneCmd)
	dogCmd.AddCommand(dogStatusCmd)
	dogCmd.AddCommand(dogDispatchCmd)
	dogCmd.AddCommand(dogHealthCheckCmd)

	rootCmd.AddCommand(dogCmd)
}

// getDogManager creates a dog.Manager with the current town root.
//
// Use FindFromCwdOrError so we honor GT_TOWN_ROOT/GT_ROOT env vars when
// invoked from a dog worktree (e.g. ~/gt/deacon/dogs/alpha/<rig>/), where
// FindFromCwd alone might walk up to a non-town ancestor or stop at a path
// without mayor/rigs.json — which previously broke `gt dog done` and
// blocked DOG_DONE delivery (hq-zyvo).
func getDogManager() (*dog.Manager, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return nil, fmt.Errorf("finding town root: %w", err)
	}

	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return nil, fmt.Errorf("loading rigs config: %w", err)
	}

	return dog.NewManager(townRoot, rigsConfig).
		WithRigBlockedCheck(func(rigName string) (bool, string) {
			return IsRigParkedOrDocked(townRoot, rigName)
		}), nil
}

func runDogAdd(cmd *cobra.Command, args []string) error {
	name := args[0]

	// Validate name
	if strings.ContainsAny(name, "/\\. ") {
		return fmt.Errorf("dog name cannot contain /, \\, ., or spaces")
	}

	mgr, err := getDogManager()
	if err != nil {
		return err
	}

	d, err := mgr.Add(name)
	if err != nil {
		return fmt.Errorf("adding dog %s: %w", name, err)
	}

	fmt.Printf("✓ Created dog %s in kennel\n", style.Bold.Render(name))
	fmt.Printf("  Path: %s\n", d.Path)
	fmt.Printf("  Worktrees:\n")
	for rigName, path := range d.Worktrees {
		fmt.Printf("    %s: %s\n", rigName, path)
	}

	// Create agent bead for the dog
	townRoot, _ := workspace.FindFromCwd()
	if townRoot != "" {
		b := beads.New(townRoot)
		location := filepath.Join("deacon", "dogs", name)

		issue, err := b.CreateDogAgentBead(name, location)
		if err != nil {
			// Non-fatal: warn but don't fail dog creation
			fmt.Printf("  Warning: could not create agent bead: %v\n", err)
		} else {
			fmt.Printf("  Agent bead: %s\n", issue.ID)
		}
	}

	return nil
}

func runDogRemove(cmd *cobra.Command, args []string) error {
	mgr, err := getDogManager()
	if err != nil {
		return err
	}

	var names []string
	if dogRemoveAll {
		dogs, err := mgr.List()
		if err != nil {
			return fmt.Errorf("listing dogs: %w", err)
		}
		for _, d := range dogs {
			names = append(names, d.Name)
		}
		if len(names) == 0 {
			fmt.Println("No dogs in kennel")
			return nil
		}
	} else {
		names = args
	}

	// Get beads client for cleanup
	townRoot, _ := workspace.FindFromCwd()
	var b *beads.Beads
	if townRoot != "" {
		b = beads.New(townRoot)
	}

	var removeErrors []string
	removed := 0

	for _, name := range names {
		// Get() only recognizes a valid dog (.dog.json present). A name given
		// explicitly may instead be debris from a failed Add — a directory
		// with no state file, which is exactly what burns a kennel name.
		// Remove() targets any kennel entry via dirExists(), so fall through
		// to it instead of skipping; it still refuses a genuinely absent name
		// and protected occupants like the Boot watchdog.
		d, err := mgr.Get(name)
		if err == nil && d.State == dog.StateWorking && !dogForce {
			removeErrors = append(removeErrors, fmt.Sprintf("%s: is working (use --force to remove anyway)", name))
			continue
		}

		if err := mgr.Remove(name); err != nil {
			if errors.Is(err, dog.ErrDogNotFound) {
				style.PrintWarning("dog %s not found, skipping", name)
				continue
			}
			removeErrors = append(removeErrors, fmt.Sprintf("%s: %v", name, err))
			continue
		}

		fmt.Printf("✓ Removed dog %s\n", name)
		removed++

		// Reset agent bead for the dog (preserves persistent identity)
		if b != nil {
			if err := b.ResetDogAgentBead(name); err != nil {
				// Non-fatal: warn but don't fail dog removal
				fmt.Printf("  Warning: could not reset agent bead: %v\n", err)
			}
		}
	}

	if len(removeErrors) > 0 {
		fmt.Printf("\nSome removals failed:\n")
		for _, e := range removeErrors {
			fmt.Printf("  - %s\n", e)
		}
	}

	if removed > 0 {
		fmt.Printf("\n✓ Removed %d dog(s).\n", removed)
	}

	if len(removeErrors) > 0 {
		return fmt.Errorf("%d removal(s) failed", len(removeErrors))
	}

	return nil
}

func runDogList(cmd *cobra.Command, args []string) error {
	mgr, err := getDogManager()
	if err != nil {
		return err
	}

	dogs, err := mgr.List()
	if err != nil {
		return fmt.Errorf("listing dogs: %w", err)
	}

	// Debris is a kennel entry that occupies a name without being a valid dog.
	// Reporting it is the whole point of tracking it: an invisible orphan is
	// what made "gt dog add <name>" fail with "already exists" while
	// "gt dog list" claimed the kennel was empty.
	debris, debrisErr := mgr.ListDebris()
	if debrisErr != nil {
		style.PrintWarning("could not scan kennel for debris: %v", debrisErr)
	}

	if len(dogs) == 0 {
		if dogListJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(struct {
				Dogs   []json.RawMessage `json:"dogs"`
				Debris []string          `json:"debris"`
			}{Dogs: nil, Debris: debris})
		} else {
			fmt.Println("No dogs in kennel")
			printDogDebris(debris)
		}
		return nil
	}

	if dogListJSON {
		type DogListItem struct {
			Name          string            `json:"name"`
			State         dog.State         `json:"state"`
			Work          string            `json:"work,omitempty"`
			WorkStartedAt *time.Time        `json:"work_started_at,omitempty"`
			LastActive    time.Time         `json:"last_active"`
			Worktrees     map[string]string `json:"worktrees,omitempty"`
		}

		var items []DogListItem
		for _, d := range dogs {
			item := DogListItem{
				Name:       d.Name,
				State:      d.State,
				Work:       d.Work,
				LastActive: d.LastActive,
				Worktrees:  d.Worktrees,
			}
			if !d.WorkStartedAt.IsZero() {
				t := d.WorkStartedAt
				item.WorkStartedAt = &t
			}
			items = append(items, item)
		}

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if len(debris) > 0 {
			return enc.Encode(struct {
				Dogs   []DogListItem `json:"dogs"`
				Debris []string      `json:"debris"`
			}{Dogs: items, Debris: debris})
		}
		return enc.Encode(items)
	}

	// Pretty print
	fmt.Println(style.Bold.Render("The Pack"))
	fmt.Println()

	idleCount := 0
	workingCount := 0

	for _, d := range dogs {
		stateIcon := "○"
		stateStyle := style.Dim
		if d.State == dog.StateWorking {
			stateIcon = "●"
			stateStyle = style.Bold
			workingCount++
		} else {
			idleCount++
		}

		line := fmt.Sprintf("  %s %s", stateIcon, stateStyle.Render(d.Name))
		if d.Work != "" {
			line += fmt.Sprintf(" → %s", style.Dim.Render(d.Work))
		}
		fmt.Println(line)
	}

	fmt.Println()
	fmt.Printf("  %d idle, %d working\n", idleCount, workingCount)
	printDogDebris(debris)

	return nil
}

// printDogDebris surfaces kennel entries that are not valid dogs, with the
// canonical command to reclaim each name.
func printDogDebris(debris []string) {
	if len(debris) == 0 {
		return
	}
	fmt.Println()
	fmt.Println(style.Bold.Render("Debris (invalid kennel entries)"))
	for _, name := range debris {
		fmt.Printf("  ✗ %s\n", name)
	}
	fmt.Println()
	fmt.Printf("  %s\n", style.Dim.Render("Reclaim with: gt dog remove <name>"))
}

func runDogCall(cmd *cobra.Command, args []string) error {
	mgr, err := getDogManager()
	if err != nil {
		return err
	}

	if dogCallAll {
		// Wake all idle dogs
		dogs, err := mgr.List()
		if err != nil {
			return fmt.Errorf("listing dogs: %w", err)
		}

		woken := 0
		for _, d := range dogs {
			if d.State == dog.StateIdle {
				if err := mgr.SetState(d.Name, dog.StateIdle); err != nil {
					style.PrintWarning("failed to wake %s: %v", d.Name, err)
					continue
				}
				woken++
				fmt.Printf("✓ Called %s\n", d.Name)
			}
		}

		if woken == 0 {
			fmt.Println("No idle dogs to call")
		} else {
			fmt.Printf("\n%d dog(s) ready\n", woken)
		}
		return nil
	}

	if len(args) > 0 {
		// Wake specific dog
		name := args[0]
		d, err := mgr.Get(name)
		if err != nil {
			return fmt.Errorf("getting dog %s: %w", name, err)
		}

		if d.State == dog.StateWorking {
			fmt.Printf("Dog %s is already working (use 'gt dog done %s' when complete)\n", name, name)
			return nil
		}

		if err := mgr.SetState(name, dog.StateIdle); err != nil {
			return fmt.Errorf("waking dog %s: %w", name, err)
		}

		fmt.Printf("✓ Called %s - ready for work\n", name)
		return nil
	}

	// Wake one idle dog
	d, err := mgr.GetIdleDog()
	if err != nil {
		return fmt.Errorf("getting idle dog: %w", err)
	}

	if d == nil {
		fmt.Println("No idle dogs available")
		return nil
	}

	if err := mgr.SetState(d.Name, dog.StateIdle); err != nil {
		return fmt.Errorf("waking dog %s: %w", d.Name, err)
	}

	fmt.Printf("✓ Called %s - ready for work\n", d.Name)
	return nil
}

func runDogClear(cmd *cobra.Command, args []string) error {
	name := args[0]

	mgr, err := getDogManager()
	if err != nil {
		return err
	}

	d, err := mgr.Get(name)
	if err != nil {
		return fmt.Errorf("getting dog %s: %w", name, err)
	}

	// Check if already idle
	if d.State == dog.StateIdle && d.Work == "" {
		fmt.Printf("Dog %s is already idle\n", name)
		return nil
	}

	// Check for live tmux session
	if !dogForce {
		sessionName := fmt.Sprintf("hq-dog-%s", name)
		tm := tmux.NewTmux()
		if has, _ := tm.HasSession(sessionName); has {
			return fmt.Errorf("dog %s has an active session (%s)\nUse --force to clear anyway", name, sessionName)
		}
	}

	// Clear work and return to idle
	if err := mgr.ClearWork(name); err != nil {
		return fmt.Errorf("clearing work for dog %s: %w", name, err)
	}

	fmt.Printf("✓ Cleared dog %s (now idle)\n", name)
	if d.Work != "" {
		fmt.Printf("  Previous work: %s\n", d.Work)
	}
	return nil
}

func runDogDone(cmd *cobra.Command, args []string) error {
	mgr, err := getDogManager()
	if err != nil {
		return err
	}

	var name string
	if len(args) > 0 {
		name = args[0]
	} else {
		// Auto-detect dog from cwd
		// Dog worktrees are at ~/gt/deacon/dogs/<name>/<rig>/
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting cwd: %w", err)
		}

		// Look for /deacon/dogs/<name>/ in path
		parts := splitPathComponents(cwd)
		for i := 0; i < len(parts)-1; i++ {
			if parts[i] == "dogs" && i > 0 && parts[i-1] == "deacon" {
				name = parts[i+1]
				break
			}
		}

		if name == "" {
			return fmt.Errorf("could not detect dog name from cwd: %s\nRun from a dog worktree or specify name: gt dog done <name>", cwd)
		}
	}

	d, err := mgr.Get(name)
	if err != nil {
		return fmt.Errorf("getting dog %s: %w", name, err)
	}

	// Always close accumulated plugin mails, even if dog is already idle.
	// Plugin dispatch mails accumulate across sessions and must be cleaned up
	// regardless of current work state.
	closePluginMails(name)

	if d.State == dog.StateIdle && d.Work == "" {
		fmt.Printf("Dog %s is already idle with no work\n", name)
		return nil
	}

	if err := mgr.ClearWork(name); err != nil {
		return fmt.Errorf("clearing work for dog %s: %w", name, err)
	}

	fmt.Printf("✓ Dog %s returned to kennel (idle)\n", name)

	// Auto-terminate the tmux session after a short delay.
	// Dogs run inside tmux sessions (hq-dog-<name>). Without this, the
	// Claude agent idles at the prompt indefinitely after completing work,
	// wasting resources until the stale-working detector kills it (2 hours).
	// The delay lets the agent see the success output before termination.
	//
	// We disable remain-on-exit first — otherwise kill-session leaves a
	// dead pane that the deacon's health-check reports as an orphan.
	sessionID := fmt.Sprintf("hq-dog-%s", name)
	t := tmux.NewTmux()
	_ = t.SetRemainOnExit(sessionID, false)
	fmt.Printf("  Session %s will terminate in 3s\n", sessionID)

	// Kill the tmux session after a short delay using a goroutine.
	// Previous approach used bash -c "sleep 3 && tmux kill-session" which
	// fails silently on Windows. The goroutine is cross-platform and uses
	// the tmux package which handles the socket name automatically.
	go func() {
		time.Sleep(3 * time.Second)
		if err := t.KillSession(sessionID); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to kill session %s: %v\n", sessionID, err)
		}
	}()

	// Wait for the goroutine to finish (the process will exit after kill).
	time.Sleep(4 * time.Second)

	return nil
}

func splitPathComponents(path string) []string {
	if path == "" {
		return nil
	}

	return strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	})
}

// closePluginMails archives all open "Plugin: " dispatch mails from a dog's inbox.
// Plugin dispatch mails sent by the daemon accumulate because gt dog done never
// closed them. On every UserPromptSubmit hook, gt mail check --inject re-injects
// ALL open mails, causing context to balloon. This function cleans up eagerly.
// It is best-effort: failures are logged but do not prevent dog from going idle.
func closePluginMails(dogName string) {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return // not in a Gas Town workspace, skip cleanup
	}

	dogAddress := fmt.Sprintf("deacon/dogs/%s", dogName)
	router := mail.NewRouterWithTownRoot(townRoot, townRoot)
	mailbox, err := router.GetMailbox(dogAddress)
	if err != nil {
		return
	}

	messages, err := mailbox.List()
	if err != nil {
		return
	}

	closed := 0
	for _, msg := range messages {
		// Archive read AND unread direct plugin dispatch mail. The dog must read
		// the dispatch mail to execute the plugin, so skipping read mail left
		// every executed dispatch bead open forever. Keep this scoped to Deacon
		// dispatches so CC or human messages with a similar subject are preserved.
		if !strings.HasPrefix(msg.Subject, "Plugin: ") {
			continue
		}
		if mail.AddressToIdentity(msg.To) != mail.AddressToIdentity(dogAddress) {
			continue
		}
		sender := mail.AddressToIdentity(msg.From)
		if sender != "deacon/" && sender != "daemon" {
			continue
		}
		if archErr := mailbox.Archive(msg.ID); archErr == nil {
			closed++
		}
	}

	if closed > 0 {
		fmt.Printf("  Closed %d stale plugin mail(s) from inbox\n", closed)
	}
}

func runDogStatus(cmd *cobra.Command, args []string) error {
	mgr, err := getDogManager()
	if err != nil {
		return err
	}

	if len(args) > 0 {
		// Show specific dog status
		name := args[0]
		return showDogStatus(mgr, name)
	}

	// Show pack summary
	return showPackStatus(mgr)
}

func showDogStatus(mgr *dog.Manager, name string) error {
	d, err := mgr.Get(name)
	if err != nil {
		return fmt.Errorf("getting dog %s: %w", name, err)
	}

	if dogStatusJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(d)
	}

	fmt.Printf("Dog: %s\n\n", style.Bold.Render(d.Name))
	fmt.Printf("  State:       %s\n", d.State)
	if d.Work != "" {
		fmt.Printf("  Work:        %s\n", d.Work)
	} else {
		fmt.Printf("  Work:        %s\n", style.Dim.Render("(none)"))
	}
	fmt.Printf("  Path:        %s\n", d.Path)
	fmt.Printf("  Last Active: %s\n", dogFormatTimeAgo(d.LastActive))
	fmt.Printf("  Created:     %s\n", d.CreatedAt.Format("2006-01-02 15:04"))

	if len(d.Worktrees) > 0 {
		fmt.Println("\nWorktrees:")
		for rigName, path := range d.Worktrees {
			// Check if worktree exists
			exists := "✓"
			if _, err := os.Stat(path); os.IsNotExist(err) {
				exists = "✗"
			}
			fmt.Printf("  %s %s: %s\n", exists, rigName, path)
		}
	}

	// Check for tmux session
	sessionName := fmt.Sprintf("hq-dog-%s", name)
	tm := tmux.NewTmux()
	if has, _ := tm.HasSession(sessionName); has {
		fmt.Printf("\nSession: %s (running)\n", sessionName)
	}

	return nil
}

func showPackStatus(mgr *dog.Manager) error {
	dogs, err := mgr.List()
	if err != nil {
		return fmt.Errorf("listing dogs: %w", err)
	}

	if dogStatusJSON {
		type PackStatus struct {
			Total     int    `json:"total"`
			Idle      int    `json:"idle"`
			Working   int    `json:"working"`
			KennelDir string `json:"kennel_dir"`
		}

		townRoot, _ := workspace.FindFromCwd()
		status := PackStatus{
			Total:     len(dogs),
			KennelDir: filepath.Join(townRoot, "deacon", "dogs"),
		}
		for _, d := range dogs {
			if d.State == dog.StateIdle {
				status.Idle++
			} else {
				status.Working++
			}
		}

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(status)
	}

	fmt.Println(style.Bold.Render("Pack Status"))
	fmt.Println()

	if len(dogs) == 0 {
		fmt.Println("  No dogs in kennel")
		fmt.Println()
		fmt.Println("  Use 'gt dog add <name>' to add a dog")
		return nil
	}

	idleCount := 0
	workingCount := 0
	for _, d := range dogs {
		if d.State == dog.StateIdle {
			idleCount++
		} else {
			workingCount++
		}
	}

	fmt.Printf("  Total:   %d\n", len(dogs))
	fmt.Printf("  Idle:    %d\n", idleCount)
	fmt.Printf("  Working: %d\n", workingCount)

	if idleCount > 0 {
		fmt.Println()
		fmt.Println(style.Dim.Render("  Ready for work. Use 'gt dog call' to wake."))
	}

	return nil
}

// dogFormatTimeAgo formats a time as a relative string like "2 hours ago".
func dogFormatTimeAgo(t time.Time) string {
	if t.IsZero() {
		return "(unknown)"
	}

	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	}
}

func runDogHealthCheck(cmd *cobra.Command, args []string) error {
	mgr, err := getDogManager()
	if err != nil {
		return err
	}

	tm := tmux.NewTmux()
	hc := dog.NewHealthChecker(mgr, tm)

	var results []dog.DogHealthResult

	if len(args) > 0 {
		// Single dog
		d, err := mgr.Get(args[0])
		if err != nil {
			return fmt.Errorf("getting dog %s: %w", args[0], err)
		}
		r := hc.Check(d, dogHealthMaxInactivity, dogHealthAutoClear)
		results = []dog.DogHealthResult{r}
	} else {
		// All dogs
		results, err = hc.CheckAll(dogHealthMaxInactivity, dogHealthAutoClear)
		if err != nil {
			return err
		}
	}

	attention := dog.NeedsAttentionCount(results)

	if dogHealthJSON {
		type HealthReport struct {
			Dogs           []dog.DogHealthResult `json:"dogs"`
			NeedsAttention int                   `json:"needs_attention"`
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(HealthReport{Dogs: results, NeedsAttention: attention}); err != nil {
			return err
		}
	} else {
		if len(results) == 0 {
			fmt.Println("No dogs in kennel")
			return nil
		}

		fmt.Println(style.Bold.Render("Dog Health Check"))
		fmt.Println()

		for _, r := range results {
			icon := "✓"
			if r.NeedsAttention {
				icon = "✗"
			}
			line := fmt.Sprintf("  %s %s [%s] session=%s", icon, r.Name, r.State, r.SessionStatus)
			if r.WorkDuration > 0 {
				line += fmt.Sprintf(" duration=%s", r.WorkDuration.Truncate(time.Second))
			}
			if r.AutoCleared {
				line += " (auto-cleared)"
			}
			fmt.Println(line)
			if r.Recommendation != "" && r.NeedsAttention {
				fmt.Printf("    → %s\n", r.Recommendation)
			}
		}

		fmt.Println()
		if attention > 0 {
			fmt.Printf("  %d dog(s) need attention\n", attention)
		} else {
			fmt.Println("  All dogs healthy")
		}
	}

	// Exit code 2 for needs-attention
	if attention > 0 {
		os.Exit(2)
	}

	return nil
}

// runDogDispatch dispatches plugin execution to a dog worker.
func runDogDispatch(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	// Get rig names for plugin scanner
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return fmt.Errorf("loading rigs config: %w", err)
	}

	var rigNames []string
	for rigName := range rigsConfig.Rigs {
		rigNames = append(rigNames, rigName)
	}

	// If --rig specified, search only that rig
	if dogDispatchRig != "" {
		rigNames = []string{dogDispatchRig}
	}

	// Find the plugin using scanner
	scanner := plugin.NewScanner(townRoot, rigNames)
	p, err := scanner.GetPlugin(dogDispatchPlugin)
	if err != nil {
		return fmt.Errorf("finding plugin: %w", err)
	}

	// Get dog manager (reuse rigsConfig from above)
	mgr := dog.NewManager(townRoot, rigsConfig).
		WithRigBlockedCheck(func(rigName string) (bool, string) {
			return IsRigParkedOrDocked(townRoot, rigName)
		})

	// Find target dog
	var targetDog *dog.Dog
	var dogCreated bool
	if dogDispatchDog != "" {
		// Specific dog requested
		targetDog, err = mgr.Get(dogDispatchDog)
		if err != nil {
			return fmt.Errorf("getting dog %s: %w", dogDispatchDog, err)
		}
		if targetDog.State == dog.StateWorking {
			return fmt.Errorf("dog %s is already working", dogDispatchDog)
		}
	} else {
		// Find idle dog from pool
		targetDog, err = mgr.GetIdleDog()
		if err != nil {
			return fmt.Errorf("finding idle dog: %w", err)
		}

		if targetDog == nil {
			if dogDispatchCreate {
				// Create a new dog (reuse generateDogName from sling_dog.go)
				newName := generateDogName(mgr)
				if dogDispatchDryRun {
					targetDog = &dog.Dog{Name: newName, State: dog.StateIdle}
					dogCreated = true
				} else {
					targetDog, err = mgr.Add(newName)
					if err != nil {
						return fmt.Errorf("creating dog %s: %w", newName, err)
					}
					dogCreated = true

					// Create agent bead for the dog
					b := beads.New(townRoot)
					location := filepath.Join("deacon", "dogs", newName)
					if _, beadErr := b.CreateDogAgentBead(newName, location); beadErr != nil {
						// Non-fatal warning
						if !dogDispatchJSON {
							fmt.Printf("  Warning: could not create agent bead: %v\n", beadErr)
						}
					}
				}
			} else {
				return fmt.Errorf("no idle dogs available (use --create to add one)")
			}
		}
	}

	// Prepare dispatch result for JSON output
	workDesc := fmt.Sprintf("plugin:%s", p.Name)
	result := dogDispatchResult{
		Plugin:     p.Name,
		PluginPath: p.Path,
		Dog:        targetDog.Name,
		DogCreated: dogCreated,
		Work:       workDesc,
		DryRun:     dogDispatchDryRun,
	}
	if p.RigName != "" {
		result.PluginRig = p.RigName
	}

	// Dry-run mode: show what would happen and exit
	if dogDispatchDryRun {
		if dogDispatchJSON {
			return json.NewEncoder(os.Stdout).Encode(result)
		}
		fmt.Printf("Dry run - would dispatch:\n")
		fmt.Printf("  Plugin: %s\n", p.Name)
		if p.RigName != "" {
			fmt.Printf("  Location: %s/plugins/%s\n", p.RigName, p.Name)
		} else {
			fmt.Printf("  Location: plugins/%s (town-level)\n", p.Name)
		}
		fmt.Printf("  Dog: %s%s\n", targetDog.Name, ifStr(dogCreated, " (would create)", ""))
		fmt.Printf("  Work: %s\n", workDesc)
		return nil
	}

	// Ensure dog has an agent bead before sending mail.
	// Dogs created before agent beads were added, or whose bead creation
	// failed silently, won't have one. The mail router requires agent beads
	// to validate recipients.
	b := beads.New(townRoot)
	if existing, _ := b.FindDogAgentBead(targetDog.Name); existing == nil {
		location := filepath.Join("deacon", "dogs", targetDog.Name)
		if _, beadErr := b.CreateDogAgentBead(targetDog.Name, location); beadErr != nil {
			if !dogDispatchJSON {
				fmt.Printf("  Warning: could not create agent bead: %v\n", beadErr)
			}
		}
	}

	// Assign work FIRST (before sending mail) to prevent race condition
	// If this fails, we haven't sent any mail yet
	if err := mgr.AssignWork(targetDog.Name, workDesc); err != nil {
		return fmt.Errorf("assigning work to dog: %w", err)
	}

	// Create and send mail message with plugin instructions
	dogAddress := fmt.Sprintf("deacon/dogs/%s", targetDog.Name)
	subject := fmt.Sprintf("Plugin: %s", p.Name)
	body := p.FormatMailBody()

	router := mail.NewRouterWithTownRoot(townRoot, townRoot)
	defer router.WaitPendingNotifications()
	msg := &mail.Message{
		From:      "deacon/",
		To:        dogAddress,
		Subject:   subject,
		Body:      body,
		Timestamp: time.Now(),
	}

	if err := router.Send(msg); err != nil {
		// Rollback: clear work assignment since mail failed
		if clearErr := mgr.ClearWork(targetDog.Name); clearErr != nil {
			// Log rollback failure but return original error
			if !dogDispatchJSON {
				fmt.Printf("  Warning: rollback failed: %v\n", clearErr)
			}
		}
		return fmt.Errorf("sending plugin mail to dog: %w", err)
	}

	// Ensure dog session is running so it can read the mail.
	// Without this, dispatched work sits in mail with no session to read it.
	t := tmux.NewTmux()
	sessMgr := dog.NewSessionManager(t, townRoot, mgr)
	sessOpts := dog.SessionStartOptions{
		WorkDesc: workDesc,
	}
	result.SessionStarted = true
	if _, sessErr := sessMgr.EnsureRunning(targetDog.Name, sessOpts); sessErr != nil {
		result.SessionStarted = false
		// Roll back the work assignment: without a running session the dog
		// cannot read its mail, leaving it stuck in StateWorking (zombie).
		// Clearing work returns it to idle so it can be re-dispatched.
		// See: github.com/steveyegge/gastown/issues/2748
		if clearErr := mgr.ClearWork(targetDog.Name); clearErr != nil {
			warn := fmt.Sprintf("session start failed AND rollback failed for dog %s — dog stuck in StateWorking, run: gt dog health-check --auto-clear: %v", targetDog.Name, clearErr)
			result.Warnings = append(result.Warnings, warn)
			if !dogDispatchJSON {
				style.PrintWarning("%s", warn)
			}
		}
		warn := fmt.Sprintf("dog dispatch: session start failed for %s (work rolled back, re-dispatch with: gt dog dispatch --plugin %s): %v", targetDog.Name, p.Name, sessErr)
		result.Warnings = append(result.Warnings, warn)
		if !dogDispatchJSON {
			style.PrintWarning("%s", warn)
		}
		if escErr := dogEscalateBestEffort(warn); escErr != nil {
			if !dogDispatchJSON {
				style.PrintWarning("escalation also failed (%v) — escalate manually: gt escalate --severity medium %q", escErr, warn)
			}
		}
	}

	// Verify the work state write is readable. A read-back failure here
	// indicates state corruption, not a timing race.
	// See: github.com/steveyegge/gastown/issues/2748
	result.WorkConfirmed = false
	if d, getErr := mgr.Get(targetDog.Name); getErr != nil {
		warn := fmt.Sprintf("dog dispatch: could not verify work assignment for %s: %v", targetDog.Name, getErr)
		result.Warnings = append(result.Warnings, warn)
		if !dogDispatchJSON {
			style.PrintWarning("%s", warn)
		}
		_ = dogEscalateBestEffort(warn)
	} else if d.Work != "" {
		result.WorkConfirmed = true
	} else {
		warn := fmt.Sprintf("dog dispatch: work assignment cleared for %s between dispatch and verify — re-dispatch required", targetDog.Name)
		result.Warnings = append(result.Warnings, warn)
		if !dogDispatchJSON {
			style.PrintWarning("%s", warn)
		}
		_ = dogEscalateBestEffort(warn)
	}

	// Success - output result
	if dogDispatchJSON {
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	fmt.Printf("%s Found plugin: %s\n", style.Bold.Render("✓"), p.Name)
	if p.RigName != "" {
		fmt.Printf("  Location: %s/plugins/%s\n", p.RigName, p.Name)
	} else {
		fmt.Printf("  Location: plugins/%s (town-level)\n", p.Name)
	}
	if dogCreated {
		fmt.Printf("%s Created dog %s (pool was empty)\n", style.Bold.Render("✓"), targetDog.Name)
	}
	fmt.Printf("%s Dispatching to dog: %s\n", style.Bold.Render("🐕"), targetDog.Name)
	fmt.Printf("%s Plugin dispatched (non-blocking)\n", style.Bold.Render("✓"))
	fmt.Printf("  Dog: %s\n", targetDog.Name)
	fmt.Printf("  Work: %s\n", workDesc)

	return nil
}

// dogDispatchResult is the JSON output for gt dog dispatch.
type dogDispatchResult struct {
	Plugin         string   `json:"plugin"`
	PluginRig      string   `json:"plugin_rig,omitempty"`
	PluginPath     string   `json:"plugin_path"`
	Dog            string   `json:"dog"`
	DogCreated     bool     `json:"dog_created,omitempty"`
	Work           string   `json:"work"`
	DryRun         bool     `json:"dry_run,omitempty"`
	SessionStarted bool     `json:"session_started"`
	WorkConfirmed  bool     `json:"work_confirmed"`
	Warnings       []string `json:"warnings,omitempty"`
}

// dogEscalateBestEffort fires a MEDIUM escalation via gt escalate.
func dogEscalateBestEffort(msg string) error {
	cmd := exec.Command("gt", "escalate", "--severity", "medium", msg)
	return cmd.Run()
}

// ifStr returns ifTrue if cond is true, otherwise ifFalse.
func ifStr(cond bool, ifTrue, ifFalse string) string {
	if cond {
		return ifTrue
	}
	return ifFalse
}
