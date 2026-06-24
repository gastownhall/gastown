package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/nudge"
	"github.com/steveyegge/gastown/internal/runtime"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var hookCmd = &cobra.Command{
	Use:         "hook [bead-id] [target]",
	Aliases:     []string{"work"},
	GroupID:     GroupWork,
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Show or attach work on a hook",
	Long: `Show what's on your hook, or attach new work.

With no arguments, shows your current hook status (alias for 'gt mol status').
With a bead ID, attaches that work to your hook.
With a bead ID and target, attaches work to another agent's hook.

The hook is the "durability primitive" - work on your hook survives session
restarts, context compaction, and handoffs. When you restart (via gt handoff),
your SessionStart hook finds the attached work and you continue from where
you left off.

Examples:
  gt hook                                    # Show what's on my hook
  gt hook status                             # Same as above
  gt hook gt-abc                             # Attach issue gt-abc to your hook
  gt hook gt-abc -s "Fix the bug"            # With subject for handoff mail
  gt hook gt-abc gastown/crew/max            # Attach gt-abc to max's hook

Related commands:
  gt sling <bead>    # Hook + start now (keep context)
  gt handoff <bead>  # Hook + restart (fresh context)
  gt unsling         # Remove work from hook`,
	Args: cobra.MaximumNArgs(2),
	RunE: runHookOrStatus,
}

// hookStatusCmd shows hook status (alias for mol status)
var hookStatusCmd = &cobra.Command{
	Use:   "status [target]",
	Short: "Show what's on your hook",
	Long: `Show what's slung on your hook.

This is an alias for 'gt mol status'. Shows what work is currently
attached to your hook, along with progress information.

Examples:
  gt hook status                    # Show my hook
  gt hook status greenplace/nux     # Show nux's hook`,
	Args: cobra.MaximumNArgs(1),
	RunE: runMoleculeStatus,
}

// hookShowCmd shows hook status in compact one-line format
var hookShowCmd = &cobra.Command{
	Use:   "show [agent]",
	Short: "Show what's on an agent's hook (compact)",
	Long: `Show what's on any agent's hook in compact one-line format.

With no argument, shows your own hook status (auto-detected from context).

Use cases:
- Mayor checking what polecats are working on
- Witness checking polecat status
- Debugging coordination issues
- Quick status overview

Examples:
  gt hook show                         # What's on MY hook? (auto-detect)
  gt hook show gastown/polecats/nux    # What's nux working on?
  gt hook show gastown/witness         # What's the witness hooked to?
  gt hook show mayor                   # What's the mayor working on?

Output format (one line):
  gastown/polecats/nux: gt-abc123 'Fix the widget bug' [in_progress]`,
	Args: cobra.MaximumNArgs(1),
	RunE: runHookShow,
}

// hookAttachCmd attaches a bead to a hook (alias for 'gt hook <bead-id>')
var hookAttachCmd = &cobra.Command{
	Use:   "attach <bead-id> [target]",
	Short: "Attach work to a hook",
	Long: `Attach a bead to your hook or another agent's hook.

With just a bead ID, attaches to your own hook (same as 'gt hook <bead-id>').
With a target, attaches to another agent's hook (for remote dispatch).

Examples:
  gt hook attach gt-abc                    # Attach to my hook
  gt hook attach gt-abc gastown/crew/max   # Attach to max's hook`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHook(cmd, args)
	},
}

// hookDetachCmd detaches a bead from a hook (alias for 'gt hook clear')
var hookDetachCmd = &cobra.Command{
	Use:   "detach <bead-id> [target]",
	Short: "Detach work from a hook",
	Long: `Remove a specific bead from a hook (same as 'gt hook clear <bead-id>').

Examples:
  gt hook detach gt-abc               # Detach gt-abc from my hook
  gt hook detach gt-abc gastown/nux   # Detach gt-abc from nux's hook`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runUnslingWith(cmd, args, hookDryRun, hookForce)
	},
}

// hookClearCmd clears the hook (alias for 'gt unhook')
var hookClearCmd = &cobra.Command{
	Use:   "clear [bead-id] [target]",
	Short: "Clear your hook (alias for 'gt unhook')",
	Long: `Remove work from your hook (alias for 'gt unhook').

With no arguments, clears your own hook. With a bead ID, only clears
if that specific bead is currently hooked. With a target, operates on
another agent's hook.

Examples:
  gt hook clear                       # Clear my hook (whatever's there)
  gt hook clear gt-abc                # Only clear if gt-abc is hooked
  gt hook clear greenplace/joe        # Clear joe's hook

Related commands:
  gt unhook           # Same as 'gt hook clear'
  gt unsling          # Same as 'gt hook clear'`,
	Args: cobra.MaximumNArgs(2),
	RunE: runHookClear,
}

var (
	hookSubject string
	hookMessage string
	hookDryRun  bool
	hookForce   bool
	hookClear   bool
)

func init() {
	// Flags for attaching work (gt hook <bead-id>)
	hookCmd.Flags().StringVarP(&hookSubject, "subject", "s", "", "Subject for handoff mail (optional)")
	hookCmd.Flags().StringVarP(&hookMessage, "message", "m", "", "Message for handoff mail (optional)")
	hookCmd.Flags().BoolVarP(&hookDryRun, "dry-run", "n", false, "Show what would be done")
	hookCmd.Flags().BoolVarP(&hookForce, "force", "f", false, "Replace existing incomplete hooked bead")
	hookCmd.Flags().BoolVar(&hookClear, "clear", false, "Clear your hook (alias for 'gt unhook')")

	// --json flag for status output (used when no args, i.e., gt hook --json)
	hookCmd.Flags().BoolVar(&moleculeJSON, "json", false, "Output as JSON (for status)")
	hookStatusCmd.Flags().BoolVar(&moleculeJSON, "json", false, "Output as JSON")
	hookShowCmd.Flags().BoolVar(&moleculeJSON, "json", false, "Output as JSON")

	// Flags for attach subcommand
	hookAttachCmd.Flags().BoolVarP(&hookForce, "force", "f", false, "Replace existing incomplete hooked bead")

	// Flags for detach subcommand (mirror unsling flags)
	hookDetachCmd.Flags().BoolVarP(&hookForce, "force", "f", false, "Detach even if work is incomplete")

	// Flags for clear subcommand (mirror unsling flags)
	hookClearCmd.Flags().BoolVarP(&hookDryRun, "dry-run", "n", false, "Show what would be done")
	hookClearCmd.Flags().BoolVarP(&hookForce, "force", "f", false, "Clear even if work is incomplete")

	hookCmd.AddCommand(hookStatusCmd)
	hookCmd.AddCommand(hookShowCmd)
	hookCmd.AddCommand(hookAttachCmd)
	hookCmd.AddCommand(hookDetachCmd)
	hookCmd.AddCommand(hookClearCmd)

	rootCmd.AddCommand(hookCmd)
}

// runHookOrStatus dispatches to status, clear, or hook based on args/flags
func runHookOrStatus(cmd *cobra.Command, args []string) error {
	// --clear flag is alias for 'gt unhook'
	if hookClear {
		return runUnslingWith(cmd, args, hookDryRun, hookForce)
	}
	if len(args) == 0 {
		// No args - show status
		return runMoleculeStatus(cmd, args)
	}
	// Has arg - attach work
	return runHook(cmd, args)
}

// runHookClear handles 'gt hook clear' - delegates to runUnsling
func runHookClear(cmd *cobra.Command, args []string) error {
	return runUnslingWith(cmd, args, hookDryRun, hookForce)
}

func runHook(_ *cobra.Command, args []string) error {
	beadID := args[0]
	if err := ensureCurrentHookWorktreeIntegrity(); err != nil {
		return err
	}

	// Reject non-bead-shaped first args before passing to bd show, which would
	// emit a confusing "bead 'set' not found" error. cobra has already failed to
	// match against a registered subcommand, so anything reaching here that
	// doesn't look like a bead ID is almost certainly a typo'd subcommand.
	// See GH#3701.
	if !isBeadID(beadID) {
		return fmt.Errorf("%q is not a bead ID. See 'gt hook --help' for available subcommands and usage", beadID)
	}

	// Parse optional target agent
	var targetAgent string
	if len(args) > 1 {
		targetAgent = args[1]
	}

	// Polecats cannot hook - they use gt done for lifecycle.
	// Check GT_ROLE first: coordinators (mayor, witness, etc.) may have a stale
	// GT_POLECAT in their environment from spawning polecats. Only block if the
	// parsed role is actually polecat (handles compound forms like
	// "gastown/polecats/Toast"). If GT_ROLE is unset, fall back to GT_POLECAT.
	if role := os.Getenv("GT_ROLE"); role != "" {
		parsedRole, _, _ := parseRoleString(role)
		if parsedRole == RolePolecat {
			return fmt.Errorf("polecats cannot hook work (use gt done for handoff)")
		}
	} else if polecatName := os.Getenv("GT_POLECAT"); polecatName != "" {
		return fmt.Errorf("polecats cannot hook work (use gt done for handoff)")
	}

	// Verify the bead exists
	if err := verifyBeadExists(beadID); err != nil {
		return err
	}

	// Determine agent identity (target or self)
	var agentID string
	var err error
	if targetAgent != "" {
		agentID, _, _, err = resolveTargetAgent(targetAgent)
		if err != nil {
			return fmt.Errorf("resolving target agent: %w", err)
		}
	} else {
		agentID, _, _, err = resolveSelfTarget()
		if err != nil {
			return fmt.Errorf("detecting agent identity: %w", err)
		}
	}

	// Find town root - needed for bd routing and agent bead updates
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}
	townBeadsDir := filepath.Join(townRoot, ".beads")

	// Resolve the beads directory for the target agent.
	// For remote targets, resolve from the agent bead's prefix to find the
	// correct database. For self, use the local beads directory.
	var workDir string
	if targetAgent != "" {
		agentBeadID := agentIDToBeadID(agentID, townRoot)
		if agentBeadID == "" {
			return fmt.Errorf("could not convert agent ID %s to bead ID", agentID)
		}
		rigName := strings.Split(agentID, "/")[0]
		var fallbackPath string
		if rigName == "mayor" || rigName == "deacon" {
			fallbackPath = townRoot
		} else {
			fallbackPath = filepath.Join(townRoot, rigName)
		}
		workDir = beads.ResolveHookDir(townRoot, agentBeadID, fallbackPath)
	} else {
		workDir, err = findLocalBeadsDir()
		if err != nil {
			return fmt.Errorf("not in a beads workspace: %w", err)
		}
	}

	b := beads.New(workDir)

	// Check for existing hooked bead for this agent
	existingPinned, err := b.List(beads.ListOptions{
		Status:   beads.StatusHooked,
		Assignee: agentID,
		Priority: -1,
	})
	if err != nil {
		return fmt.Errorf("checking existing hooked beads: %w", err)
	}

	// If there's an existing hooked bead, check if we can auto-replace
	if len(existingPinned) > 0 {
		existing := existingPinned[0]

		// Skip if it's the same bead we're trying to pin
		if existing.ID == beadID {
			fmt.Printf("%s Already hooked: %s\n", style.Bold.Render("✓"), beadID)
			return nil
		}

		// Check if existing bead is complete
		isComplete, hasAttachment := checkPinnedBeadComplete(b, existing)

		if isComplete {
			// Auto-replace completed bead
			fmt.Printf("%s Replacing completed bead %s...\n", style.Dim.Render("ℹ"), existing.ID)
			if !hookDryRun {
				if hasAttachment {
					if err := closeCompletedHookedMolecule(workDir, existing.ID); err != nil {
						return fmt.Errorf("closing completed bead %s: %w", existing.ID, err)
					}
				} else {
					// Naked bead - just unpin, don't close (might have value)
					status := "open"
					if err := b.Update(existing.ID, beads.UpdateOptions{Status: &status}); err != nil {
						return fmt.Errorf("unpinning bead %s: %w", existing.ID, err)
					}
				}
			}
		} else if hookForce {
			// Force replace incomplete bead
			fmt.Printf("%s Force-replacing incomplete bead %s...\n", style.Dim.Render("⚠"), existing.ID)
			if !hookDryRun {
				// Unpin by setting status back to open
				status := "open"
				if err := b.Update(existing.ID, beads.UpdateOptions{Status: &status}); err != nil {
					return fmt.Errorf("unpinning bead %s: %w", existing.ID, err)
				}
			}
		} else {
			// Existing incomplete bead blocks new hook
			return fmt.Errorf("existing hooked bead %s is incomplete (%s)\n  Use --force to replace, or complete the existing work first",
				existing.ID, existing.Title)
		}
	}

	if targetAgent != "" {
		fmt.Printf("%s Hooking %s for %s...\n", style.Bold.Render("🪝"), beadID, agentID)
	} else {
		fmt.Printf("%s Hooking %s...\n", style.Bold.Render("🪝"), beadID)
	}

	if hookDryRun {
		fmt.Printf("Would run: bd update %s --status=hooked --assignee=%s\n", beadID, agentID)
		if hookSubject != "" {
			fmt.Printf("  subject (for handoff mail): %s\n", hookSubject)
		}
		if hookMessage != "" {
			fmt.Printf("  context (for handoff mail): %s\n", hookMessage)
		}
		return nil
	}

	// Hook the bead using bd update with retry logic (discovery-based approach).
	// Run from town root so bd can find routes.jsonl for prefix-based routing.
	// This is essential for hooking convoys (hq-* prefix) stored in town beads.
	// Dolt can fail with concurrency errors (HTTP 400) when multiple agents write
	// simultaneously. We retry with exponential backoff, matching sling.go behavior.
	const hookMaxRetries = 5
	const hookBaseBackoff = 500 * time.Millisecond
	const hookBackoffMax = 10 * time.Second
	var lastHookErr error
	for attempt := 1; attempt <= hookMaxRetries; attempt++ {
		if err := BdCmd("update", beadID, "--status=hooked", "--assignee="+agentID).
			Dir(resolveBeadDir(beadID)).
			StripBeadsDir().
			WithAutoCommit().
			Run(); err != nil {
			lastHookErr = err
			if attempt < hookMaxRetries {
				backoff := slingBackoff(attempt, hookBaseBackoff, hookBackoffMax)
				fmt.Printf("%s Hook attempt %d failed, retrying in %v...\n", style.Warning.Render("⚠"), attempt, backoff)
				time.Sleep(backoff)
				continue
			}
			return fmt.Errorf("hooking bead after %d attempts: %w", hookMaxRetries, lastHookErr)
		}
		break
	}

	// Emit a propulsion signal if the target is the mayor.
	// This allows the ACP propeller to react to hook changes event-driven.
	if agentID == "mayor/" {
		if townRoot, err := workspace.FindFromCwd(); err == nil && townRoot != "" {
			session := "hq-mayor"
			message := fmt.Sprintf("Hook updated: attached bead %s", beadID)
			_ = nudge.Enqueue(townRoot, session, nudge.QueuedNudge{
				Sender:   "hook",
				Message:  message,
				Priority: nudge.PriorityNormal,
			})
		}
	}

	if targetAgent != "" {
		fmt.Printf("%s Work attached to %s's hook\n", style.Bold.Render("✓"), agentID)
	} else {
		fmt.Printf("%s Work attached to hook (hooked bead)\n", style.Bold.Render("✓"))
	}

	// Update agent bead's hook_bead field (matches gt sling behavior)
	// This ensures gt hook / gt mol status can find hooked work via the agent bead
	updateAgentHookBead(agentID, beadID, workDir, townBeadsDir)

	if targetAgent != "" {
		fmt.Printf("  Use 'gt hook show %s' to verify\n", targetAgent)
	} else {
		fmt.Printf("  Use 'gt handoff' to restart with this work\n")
		fmt.Printf("  Use 'gt hook' to see hook status\n")
	}

	// Log hook event to activity feed (non-fatal)
	if err := events.LogFeed(events.TypeHook, agentID, events.HookPayload(beadID)); err != nil {
		fmt.Fprintf(os.Stderr, "%s Warning: failed to log hook event: %v\n", style.Dim.Render("⚠"), err)
	}

	return nil
}

func closeCompletedHookedMolecule(workDir, beadID string) error {
	closeArgs := []string{"close", beadID, "--force", "--reason=Auto-replaced by gt hook (molecule complete)"}
	if sessionID := runtime.SessionIDFromEnv(); sessionID != "" {
		closeArgs = append(closeArgs, "--session="+sessionID)
	}
	return BdCmd(closeArgs...).Dir(workDir).WithAutoCommit().Run()
}

// checkPinnedBeadComplete checks if a pinned bead's attached molecule is 100% complete.
// Returns (isComplete, hasAttachment):
// - isComplete=true if no molecule attached OR all molecule steps are closed
// - hasAttachment=true if there's an attached molecule
func checkPinnedBeadComplete(b *beads.Beads, issue *beads.Issue) (isComplete bool, hasAttachment bool) {
	// Check for attached molecule
	attachment := beads.ParseAttachmentFields(issue)
	if attachment == nil || attachment.AttachedMolecule == "" {
		// No molecule attached - consider complete (naked bead)
		return true, false
	}

	// Get progress of attached molecule
	progress, err := getMoleculeProgressInfo(b, attachment.AttachedMolecule)
	if err != nil {
		// Can't determine progress - be conservative, treat as incomplete
		return false, true
	}

	if progress == nil {
		// No steps found - might be a simple issue, treat as complete
		return true, true
	}

	return progress.Complete, true
}

// runHookShow displays another agent's hook in compact one-line format.
func runHookShow(cmd *cobra.Command, args []string) error {
	if err := ensureCurrentHookWorktreeIntegrity(); err != nil {
		return err
	}

	var target string
	if len(args) > 0 {
		target = normalizeHookShowTarget(args[0])
	} else {
		// Auto-detect current agent from context
		agentID, _, _, err := resolveSelfTarget()
		if err != nil {
			return fmt.Errorf("auto-detecting agent (use explicit argument): %w", err)
		}
		target = agentID
	}

	// Find beads directory.
	// For remote rig-level targets (e.g. "myndy_monorepo/refinery"), resolve the
	// rig's actual beads dir using the same rig-aware routing as runHook (attach).
	// Without this, gt hook show always queries whatever DB is local (typically HQ),
	// missing wisps stored in the target rig's database.
	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}
	if len(args) > 0 && !isTownLevelRole(target) {
		townRoot, townErr := workspace.FindFromCwd()
		if townErr == nil && townRoot != "" {
			rigName := strings.Split(target, "/")[0]
			if rigName != "" && rigName != "mayor" && rigName != "deacon" {
				// Agent beads can be stale or missing during recovery. The source
				// work assignment is authoritative, so query the target rig DB directly.
				workDir = filepath.Join(townRoot, rigName, "mayor", "rig")
			}
		}
	}

	b := beads.New(workDir)
	// Query for hooked beads assigned to the target
	hookedBeads, err := b.List(beads.ListOptions{
		Status:   beads.StatusHooked,
		Assignee: target,
		Priority: -1,
	})
	if err != nil {
		return fmt.Errorf("listing hooked beads: %w", err)
	}
	if len(hookedBeads) == 0 {
		inProgressBeads, err := b.List(beads.ListOptions{
			Status:   "in_progress",
			Assignee: target,
			Priority: -1,
		})
		if err != nil {
			return fmt.Errorf("listing in-progress beads: %w", err)
		}
		hookedBeads = inProgressBeads
	}

	// If nothing found in local beads, also check town beads for hooked convoys.
	// Convoys (hq-cv-*) are stored in town beads (~/gt/.beads) and any agent
	// can hook them for convoy-driver mode.
	if len(hookedBeads) == 0 {
		townRoot, err := findTownRoot()
		if err == nil && townRoot != "" {
			// Check town beads for hooked items
			townBeadsDir := filepath.Join(townRoot, ".beads")
			if _, err := os.Stat(townBeadsDir); err == nil {
				townBeads := beads.New(townBeadsDir)
				townHooked, err := townBeads.List(beads.ListOptions{
					Status:   beads.StatusHooked,
					Assignee: target,
					Priority: -1,
				})
				if err == nil && len(townHooked) > 0 {
					hookedBeads = townHooked
				} else if err == nil {
					townInProgress, err := townBeads.List(beads.ListOptions{
						Status:   "in_progress",
						Assignee: target,
						Priority: -1,
					})
					if err == nil && len(townInProgress) > 0 {
						hookedBeads = townInProgress
					}
				}
			}

			// If still nothing found and town-level role, scan all rigs
			if len(hookedBeads) == 0 && isTownLevelRole(target) {
				hookedBeads = scanAllRigsForHookedBeads(townRoot, target)
			}
		}
	}

	// JSON output
	if moleculeJSON {
		type compactInfo struct {
			Agent  string `json:"agent"`
			BeadID string `json:"bead_id,omitempty"`
			Title  string `json:"title,omitempty"`
			Status string `json:"status"`
		}
		info := compactInfo{Agent: target}
		if len(hookedBeads) > 0 {
			info.BeadID = hookedBeads[0].ID
			info.Title = hookedBeads[0].Title
			info.Status = hookedBeads[0].Status
		} else {
			info.Status = "empty"
		}
		enc := json.NewEncoder(os.Stdout)
		return enc.Encode(info)
	}

	// Compact one-line output
	if len(hookedBeads) == 0 {
		fmt.Printf("%s: (empty)\n", target)
		return nil
	}

	bead := hookedBeads[0]
	fmt.Printf("%s: %s '%s' [%s]\n", target, bead.ID, bead.Title, bead.Status)
	return nil
}

func ensureCurrentHookWorktreeIntegrity() error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}
	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return nil
	}
	roleCtx := detectRole(cwd, townRoot)
	return ensureRoleWorktreeIntegrity(cwd, townRoot, roleCtx.Role)
}

// normalizeHookShowTarget resolves target aliases/shorthand to canonical agent IDs.
// Examples:
//   - "rig/polecat" -> "rig/polecats/polecat"
//   - "mayor" -> "mayor"
//
// If resolution fails, it returns the original target unchanged.
func normalizeHookShowTarget(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return target
	}

	// Use the same role/path resolver as dispatching commands, then convert
	// the resulting tmux session back to a canonical assignee address.
	// This keeps "hook show" target parsing aligned with sling/hook behavior.
	if sessionName, err := resolveRoleToSession(target); err == nil && sessionName != "" {
		if addr, ok := sessionNameToCanonicalAddress(sessionName, target); ok {
			return addr
		}
	}

	// Fallback for explicit/canonical addresses when resolver couldn't help.
	if identity, err := session.ParseAddress(target); err == nil {
		return identity.Address()
	}

	// Direct shorthand expansion: rig/name → rig/polecats/name or rig/crew/name.
	// This handles the case where the session name roundtrip fails due to
	// uninitialized prefix registry. See GH#2371.
	parts := strings.Split(target, "/")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		name := parts[1]
		// Check for known roles — don't expand those
		switch strings.ToLower(name) {
		case "witness", "refinery", "mayor", "deacon":
			// Already a valid canonical address
		default:
			// Check if it's a crew member by looking for the directory
			townRoot := detectTownRootFromCwd()
			if townRoot != "" {
				crewPath := filepath.Join(townRoot, parts[0], "crew", name)
				if info, statErr := os.Stat(crewPath); statErr == nil && info.IsDir() {
					return parts[0] + "/crew/" + name
				}
			}
			// Default to polecat
			return parts[0] + "/polecats/" + strings.ToLower(name)
		}
	}

	return target
}

// sessionNameToCanonicalAddress maps a tmux session name to a canonical agent
// assignee address (e.g., "gastown/polecats/toast").
//
// targetHint is the original user input and is used to seed a temporary
// prefix→rig mapping for deterministic parsing in tests or minimal
// environments where the global session registry is not initialized.
func sessionNameToCanonicalAddress(sessionName, targetHint string) (string, bool) {
	if identity, err := session.ParseSessionName(sessionName); err == nil {
		return canonicalAssigneeAddress(identity), true
	}

	registry := session.NewPrefixRegistry()
	for rig, prefix := range session.DefaultRegistry().AllRigs() {
		registry.Register(prefix, rig)
	}
	parts := strings.Split(strings.TrimSpace(targetHint), "/")
	if len(parts) >= 2 && parts[0] != "" {
		rig := parts[0]
		registry.Register(session.PrefixFor(rig), rig)
	}

	identity, err := session.ParseSessionNameWithRegistry(sessionName, registry)
	if err != nil {
		return "", false
	}
	return canonicalAssigneeAddress(identity), true
}

// findTownRoot finds the Gas Town root directory.
func findTownRoot() (string, error) {
	return workspace.FindFromCwd()
}
