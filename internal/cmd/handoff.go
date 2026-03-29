package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/ui"
	"github.com/steveyegge/gastown/internal/workspace"
)

var handoffCmd = &cobra.Command{
	Use:         "handoff [bead-or-role]",
	GroupID:     GroupWork,
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Hand off to a fresh session, work continues from hook",
	Long: `End watch. Hand off to a fresh agent session.

This is the canonical way to end any agent session. It handles all roles:

  - Mayor, Crew, Witness, Refinery, Deacon: Respawns with fresh Claude instance
  - Polecats: Calls 'gt done --status DEFERRED' (Witness handles lifecycle)

When run without arguments, hands off the current session.
When given a bead ID (gt-xxx, hq-xxx), hooks that work first, then restarts.
When given a role name, hands off that role's session (and switches to it).

Examples:
  gt handoff                          # Hand off current session
  gt handoff gt-abc                   # Hook bead, then restart
  gt handoff gt-abc -s "Fix it"       # Hook with context, then restart
  gt handoff -s "Context" -m "Notes"  # Hand off with custom message
  gt handoff -c                       # Collect state into handoff message
  gt handoff crew                     # Hand off crew session
  gt handoff mayor                    # Hand off mayor session

The --collect (-c) flag gathers current state (hooked work, inbox, ready beads,
in-progress items) and includes it in the handoff mail. This provides context
for the next session without manual summarization.

The --cycle flag triggers automatic session cycling (used by PreCompact hooks).
Unlike --auto (state only) or normal handoff (polecat→gt-done redirect), --cycle
always does a full respawn regardless of role. This enables crew workers and
polecats to get a fresh context window when the current one fills up.

Any molecule on the hook will be auto-continued by the new session.
The SessionStart hook runs 'gt prime' to restore context.`,
	RunE: runHandoff,
}

var (
	handoffWatch      bool
	handoffDryRun     bool
	handoffSubject    string
	handoffMessage    string
	handoffCollect    bool
	handoffStdin      bool
	handoffAuto       bool
	handoffCycle      bool
	handoffReason     string
	handoffNoGitCheck bool
	handoffForce      bool
)

func init() {
	handoffCmd.Flags().BoolVarP(&handoffWatch, "watch", "w", true, "Switch to new session (for remote handoff)")
	handoffCmd.Flags().BoolVarP(&handoffDryRun, "dry-run", "n", false, "Show what would be done without executing")
	handoffCmd.Flags().StringVarP(&handoffSubject, "subject", "s", "", "Subject for handoff mail (optional)")
	handoffCmd.Flags().StringVarP(&handoffMessage, "message", "m", "", "Message body for handoff mail (optional)")
	handoffCmd.Flags().BoolVarP(&handoffCollect, "collect", "c", false, "Auto-collect state (status, inbox, beads) into handoff message")
	handoffCmd.Flags().BoolVar(&handoffStdin, "stdin", false, "Read message body from stdin (avoids shell quoting issues)")
	handoffCmd.Flags().BoolVar(&handoffAuto, "auto", false, "Save state only, no session cycling (for PreCompact hooks)")
	handoffCmd.Flags().BoolVar(&handoffCycle, "cycle", false, "Auto-cycle session (for PreCompact hooks that want full session replacement)")
	handoffCmd.Flags().StringVar(&handoffReason, "reason", "", "Reason for handoff (e.g., 'compaction', 'idle')")
	handoffCmd.Flags().BoolVar(&handoffNoGitCheck, "no-git-check", false, "Skip git workspace cleanliness check")
	handoffCmd.Flags().BoolVarP(&handoffForce, "force", "f", false, "Override session protection")
	rootCmd.AddCommand(handoffCmd)
}

func runHandoff(cmd *cobra.Command, args []string) error {
	// Handle --stdin: read message body from stdin (avoids shell quoting issues)
	if handoffStdin {
		if handoffMessage != "" {
			return fmt.Errorf("cannot use --stdin with --message/-m")
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		handoffMessage = strings.TrimRight(string(data), "\n")
	}

	// --auto mode: save state only, no session cycling.
	// Used by PreCompact hook to preserve state before compaction.
	// Note: auto-mode exits here, before the git-status warning check below.
	// This is intentional — auto-handoffs are triggered by hooks and should not
	// spam warnings. The --no-git-check flag has no effect in auto mode.
	if handoffAuto {
		return runHandoffAuto()
	}

	// --cycle mode: full session cycling, triggered by PreCompact hook.
	// Unlike --auto (state only), this replaces the current session with a fresh one.
	// Unlike normal handoff, this skips the polecat→gt-done redirect because
	// cycling preserves work state (the hook stays attached).
	//
	// Flow: collect state → send handoff mail → respawn pane (fresh Claude instance)
	// The successor session picks up hooked work via SessionStart hook (gt prime --hook).
	if handoffCycle {
		return runHandoffCycle()
	}

	// Check if we're a polecat - polecats use gt done instead.
	// Check GT_ROLE first: coordinators (mayor, witness, etc.) may have a stale
	// GT_POLECAT in their environment from spawning polecats. Only block if the
	// parsed role is actually polecat (handles compound forms like
	// "gastown/polecats/Toast"). If GT_ROLE is unset, fall back to GT_POLECAT.
	isPolecat := false
	polecatName := ""
	if role := os.Getenv("GT_ROLE"); role != "" {
		parsedRole, _, name := parseRoleString(role)
		if parsedRole == RolePolecat {
			isPolecat = true
			polecatName = name
			// Bare "polecat" role yields empty name; fall back to GT_POLECAT.
			if polecatName == "" {
				polecatName = os.Getenv("GT_POLECAT")
			}
		}
	} else if name := os.Getenv("GT_POLECAT"); name != "" {
		isPolecat = true
		polecatName = name
	}
	if isPolecat {
		fmt.Printf("%s Polecat detected (%s) - using gt done for handoff\n",
			style.Bold.Render("🐾"), polecatName)
		// Polecats don't respawn themselves - Witness handles lifecycle
		// Call gt done with DEFERRED status to preserve work state
		doneCmd := exec.Command("gt", "done", "--status", "DEFERRED")
		doneCmd.Stdout = os.Stdout
		doneCmd.Stderr = os.Stderr
		return doneCmd.Run()
	}

	// Enforce minimum handoff cooldown to prevent tight restart loops (gt-058d).
	// When a patrol agent (e.g., witness) completes quickly on idle rigs,
	// it can hand off immediately and the daemon respawns, creating a crash loop.
	enforceHandoffCooldown()

	// If --collect flag is set, auto-collect state into the message
	if handoffCollect {
		collected := collectHandoffState()
		if handoffMessage == "" {
			handoffMessage = collected
		} else {
			handoffMessage = handoffMessage + "\n\n---\n" + collected
		}
		if handoffSubject == "" {
			handoffSubject = "Session handoff with context"
		}
	}

	// Use a socket-aware Tmux for pane operations. The calling process may be
	// on a different tmux server than the town socket (e.g., default socket).
	// For self-handoff, pane operations (clear-history, respawn-pane) must target
	// the caller's own server. SocketFromEnv() reads $TMUX to find the right one.
	callerSocket := tmux.SocketFromEnv()
	t := tmux.NewTmuxWithSocket(callerSocket)
	// Town-socket Tmux for session-level queries (getSessionPane, etc.)
	townTmux := tmux.NewTmux()
	_ = townTmux // used later for remote handoff

	// Verify we're in tmux
	if !tmux.IsInsideTmux() {
		return fmt.Errorf("not running in tmux - cannot hand off")
	}

	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return fmt.Errorf("TMUX_PANE not set - cannot hand off")
	}

	// Get current session name from GT_ROLE (preferred) or tmux display-message.
	currentSession, err := getCurrentTmuxSession()
	if err != nil {
		return fmt.Errorf("getting session name: %w", err)
	}

	// Warn if workspace has uncommitted or unpushed work (wa-7967c).
	// Note: this checks the caller's cwd, not the target session's workdir.
	// For remote handoff (gt handoff <role>), the warning reflects the caller's
	// workspace state. Checking the target session's workdir would require tmux
	// pane introspection and is deferred to a future enhancement.
	if !handoffNoGitCheck {
		warnHandoffGitStatus()
	}

	// Determine target session and check for bead hook
	targetSession := currentSession
	if len(args) > 0 {
		arg := args[0]

		// Check if arg is a bead ID (gt-xxx, hq-xxx, bd-xxx, etc.)
		if looksLikeBeadID(arg) {
			// Hook the bead first
			if err := hookBeadForHandoff(arg); err != nil {
				return fmt.Errorf("hooking bead: %w", err)
			}
			// Update subject if not set
			if handoffSubject == "" {
				handoffSubject = fmt.Sprintf("🪝 HOOKED: %s", arg)
			}
		} else {
			// User specified a role to hand off
			targetSession, err = resolveRoleToSession(arg)
			if err != nil {
				return fmt.Errorf("resolving role: %w", err)
			}
		}
	}

	// Build the restart command
	restartCmd, err := buildRestartCommand(targetSession)
	if err != nil {
		return err
	}

	// If handing off a different session, we need to find its pane and respawn there.
	// Remote sessions live on the town socket, so use townTmux for their operations.
	if targetSession != currentSession {
		// Update tmux session env before respawn (not during dry-run — see below)
		updateSessionEnvForHandoff(townTmux, targetSession, "")
		return handoffRemoteSession(townTmux, targetSession, restartCmd)
	}

	// Check session protection before self-handoff
	if protected, reason := checkSessionProtected(t, currentSession); protected {
		if !handoffForce {
			return fmt.Errorf("session is protected (%s) - use --force or gt unprotect first", reason)
		}
		style.PrintWarning("overriding session protection (%s)", reason)
		_ = t.ClearSessionProtected(currentSession)
	}

	// Close any in-progress molecule steps before cycling (gt-e26g).
	// Without this, patrol agents that handoff mid-cycle leak orphaned wisps.
	cleanupMoleculeOnHandoff()

	// Handing off ourselves - print feedback then respawn
	fmt.Printf("%s Handing off %s...\n", style.Bold.Render("🤝"), currentSession)

	// Log handoff event (both townlog and events feed)
	if townRoot, err := workspace.FindFromCwd(); err == nil && townRoot != "" {
		agent := sessionToGTRole(currentSession)
		if agent == "" {
			agent = currentSession
		}
		_ = LogHandoff(townRoot, agent, handoffSubject)
		// Also log to activity feed
		_ = events.LogFeed(events.TypeHandoff, agent, events.HandoffPayload(handoffSubject, true))
	}

	// Dry run mode - show what would happen (BEFORE any side effects)
	if handoffDryRun {
		if handoffSubject != "" || handoffMessage != "" {
			fmt.Printf("Would send handoff mail: subject=%q (auto-hooked)\n", handoffSubject)
		}
		fmt.Printf("Would execute: tmux clear-history -t %s\n", pane)
		fmt.Printf("Would execute: tmux respawn-pane -k -t %s %s\n", pane, restartCmd)
		return nil
	}

	// Update tmux session environment for liveness detection.
	// IsAgentAlive reads GT_PROCESS_NAMES via tmux show-environment (session env),
	// not from shell exports. The restart command sets shell exports for the child
	// process, but we must also update the session env so liveness checks work.
	// Placed after the dry-run guard to avoid mutating session state during dry-run.
	updateSessionEnvForHandoff(t, currentSession, "")

	// Send handoff mail to self (defaults applied inside sendHandoffMail).
	// The mail is auto-hooked so the next session picks it up.
	beadID, err := sendHandoffMail(handoffSubject, handoffMessage)
	if err != nil {
		style.PrintWarning("could not send handoff mail: %v", err)
		// Continue anyway - the respawn is more important
	} else {
		fmt.Printf("%s Sent handoff mail %s (auto-hooked)\n", style.Bold.Render("📬"), beadID)
	}

	// NOTE: reportAgentState("stopped") removed (gt-zecmc)
	// Agent liveness is observable from tmux - no need to record it in bead.
	// "Discover, don't track" principle: reality is truth, state is derived.

	// Clear scrollback history before respawn (resets copy-mode from [0/N] to [0/0])
	if err := t.ClearHistory(pane); err != nil {
		// Non-fatal - continue with respawn even if clear fails
		style.PrintWarning("could not clear history: %v", err)
	}

	// Write handoff marker for successor detection (prevents handoff loop bug).
	// The marker is cleared by gt prime after it outputs the warning.
	// This tells the new session "you're post-handoff, don't re-run /handoff"
	if cwd, err := os.Getwd(); err == nil {
		runtimeDir := filepath.Join(cwd, constants.DirRuntime)
		_ = os.MkdirAll(runtimeDir, 0755)
		markerPath := filepath.Join(runtimeDir, constants.FileHandoffMarker)
		_ = os.WriteFile(markerPath, []byte(currentSession), 0644)
	}

	// Record handoff time for cooldown enforcement (gt-058d).
	recordHandoffTime()

	// Set remain-on-exit so the pane survives process death during handoff.
	// Without this, killing processes causes tmux to destroy the pane before
	// we can respawn it. This is essential for tmux session reuse.
	if err := t.SetRemainOnExit(pane, true); err != nil {
		style.PrintWarning("could not set remain-on-exit: %v", err)
	}

	// NOTE: For self-handoff, we do NOT call KillPaneProcesses here.
	// That would kill the gt handoff process itself before it can call RespawnPane,
	// leaving the pane dead with no respawn. RespawnPane's -k flag handles killing
	// atomically - tmux kills the old process and spawns the new one together.
	// See: https://github.com/steveyegge/gastown/issues/859 (pane is dead bug)
	//
	// For orphan prevention, we rely on respawn-pane -k which sends SIGHUP/SIGTERM.
	// If orphans still occur, the solution is to adjust the restart command to
	// kill orphans at startup, not to kill ourselves before respawning.

	// Check if pane's working directory exists (may have been deleted)
	paneWorkDir, _ := t.GetPaneWorkDir(currentSession)
	if paneWorkDir != "" {
		if _, err := os.Stat(paneWorkDir); err != nil {
			if townRoot := detectTownRootFromCwd(); townRoot != "" {
				style.PrintWarning("pane working directory deleted, using town root")
				return t.RespawnPaneWithWorkDir(pane, townRoot, restartCmd)
			}
		}
	}

	// Use respawn-pane -k to atomically kill current process and start new one
	// Note: respawn-pane automatically resets remain-on-exit to off
	return t.RespawnPane(pane, restartCmd)
}

// runHandoffAuto saves state without cycling the session.
// Used by the PreCompact hook to preserve context before compaction.
// No tmux required — just collects state, sends handoff mail, and writes marker.
func runHandoffAuto() error {
	// Build subject
	subject := handoffSubject
	if subject == "" {
		reason := handoffReason
		if reason == "" {
			reason = "auto"
		}
		subject = fmt.Sprintf("🤝 HANDOFF: %s", reason)
	}

	// Auto-collect state if no explicit message
	message := handoffMessage
	if message == "" {
		message = collectHandoffState()
	}

	if handoffDryRun {
		fmt.Printf("[auto-handoff] Would send mail: subject=%q\n", subject)
		fmt.Printf("[auto-handoff] Would write handoff marker\n")
		return nil
	}

	// Close any in-progress molecule steps before state save (gt-e26g).
	cleanupMoleculeOnHandoff()

	// Send handoff mail to self
	beadID, err := sendHandoffMail(subject, message)
	if err != nil {
		// Non-fatal — log and continue
		fmt.Fprintf(os.Stderr, "auto-handoff: could not send mail: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "auto-handoff: saved state to %s\n", beadID)
	}

	// Write handoff marker so post-compact prime knows it's post-handoff
	if cwd, err := os.Getwd(); err == nil {
		runtimeDir := filepath.Join(cwd, constants.DirRuntime)
		_ = os.MkdirAll(runtimeDir, 0755)
		markerPath := filepath.Join(runtimeDir, constants.FileHandoffMarker)
		sessionName := "auto-handoff"
		if tmux.IsInsideTmux() {
			if name, err := getCurrentTmuxSession(); err == nil {
				sessionName = name
			}
		}
		_ = os.WriteFile(markerPath, []byte(sessionName), 0644)
	}

	// Log handoff event
	if townRoot, err := workspace.FindFromCwd(); err == nil && townRoot != "" {
		agent := detectSender()
		if agent == "" || agent == "overseer" {
			agent = "unknown"
		}
		_ = events.LogFeed(events.TypeHandoff, agent, events.HandoffPayload(subject, false))
	}

	return nil
}

// runHandoffCycle performs a full session cycle — save state AND respawn.
// This is the PreCompact-triggered session succession mechanism (gt-op78).
//
// Unlike --auto (state only) or normal handoff (polecat→gt-done redirect),
// --cycle always does a full respawn regardless of role. This enables
// crew workers (and polecats) to get a fresh context window when the
// current one fills up.
//
// The flow:
//  1. Auto-collect state (inbox, ready beads, hooked work)
//  2. Send handoff mail to self (auto-hooked for successor)
//  3. Write handoff marker (prevents handoff loop)
//  4. Respawn the tmux pane with a fresh Claude instance
//
// The successor session starts via SessionStart hook (gt prime --hook),
// finds the hooked work, and continues from where we left off.
func runHandoffCycle() error {
	// Build subject
	subject := handoffSubject
	if subject == "" {
		reason := handoffReason
		if reason == "" {
			reason = "context-cycle"
		}
		subject = fmt.Sprintf("🤝 HANDOFF: %s", reason)
	}

	// Auto-collect state if no explicit message
	message := handoffMessage
	if message == "" {
		message = collectHandoffState()
	}

	// Must be in tmux to respawn
	if !tmux.IsInsideTmux() {
		// Fall back to auto mode (save state only) if not in tmux
		fmt.Fprintf(os.Stderr, "handoff --cycle: not in tmux, falling back to state-save only\n")
		handoffMessage = message
		handoffSubject = subject
		return runHandoffAuto()
	}

	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		fmt.Fprintf(os.Stderr, "handoff --cycle: TMUX_PANE not set, falling back to state-save only\n")
		handoffMessage = message
		handoffSubject = subject
		return runHandoffAuto()
	}

	currentSession, err := getCurrentTmuxSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "handoff --cycle: could not get session: %v, falling back to state-save only\n", err)
		handoffMessage = message
		handoffSubject = subject
		return runHandoffAuto()
	}

	// Use the caller's socket for pane operations (same as runHandoff).
	callerSocket := tmux.SocketFromEnv()
	t := tmux.NewTmuxWithSocket(callerSocket)

	// Check session protection before cycling
	if protected, reason := checkSessionProtected(t, currentSession); protected {
		fmt.Fprintf(os.Stderr, "handoff --cycle: session protected (%s), falling back to state-save only\n", reason)
		handoffMessage = message
		handoffSubject = subject
		return runHandoffAuto()
	}

	if handoffDryRun {
		fmt.Printf("[cycle] Would send handoff mail: subject=%q\n", subject)
		fmt.Printf("[cycle] Would write handoff marker\n")
		fmt.Printf("[cycle] Would execute: tmux clear-history -t %s\n", pane)
		fmt.Printf("[cycle] Would execute: tmux respawn-pane -k -t %s <restart-cmd>\n", pane)
		return nil
	}

	// Close any in-progress molecule steps before cycling (gt-e26g).
	cleanupMoleculeOnHandoff()

	// Send handoff mail to self (auto-hooked for successor)
	beadID, err := sendHandoffMail(subject, message)
	if err != nil {
		fmt.Fprintf(os.Stderr, "handoff --cycle: could not send mail: %v\n", err)
		// Continue — respawn is more important than mail
	} else {
		fmt.Fprintf(os.Stderr, "handoff --cycle: saved state to %s\n", beadID)
	}

	// Write handoff marker so post-cycle prime knows it's post-handoff.
	// Format: "session_id\nreason" — the reason enables isCompactResume()
	// to detect compaction-triggered cycles and use a lighter continuation
	// directive instead of full re-initialization. (GH#1965)
	if cwd, err := os.Getwd(); err == nil {
		runtimeDir := filepath.Join(cwd, constants.DirRuntime)
		_ = os.MkdirAll(runtimeDir, 0755)
		markerPath := filepath.Join(runtimeDir, constants.FileHandoffMarker)
		markerContent := currentSession
		if handoffReason != "" {
			markerContent += "\n" + handoffReason
		}
		_ = os.WriteFile(markerPath, []byte(markerContent), 0644)
	}

	// Record handoff time for cooldown enforcement (gt-058d).
	recordHandoffTime()

	// Log cycle event
	if townRoot, err := workspace.FindFromCwd(); err == nil && townRoot != "" {
		agent := sessionToGTRole(currentSession)
		if agent == "" {
			agent = currentSession
		}
		_ = LogHandoff(townRoot, agent, subject)
		_ = events.LogFeed(events.TypeHandoff, agent, events.HandoffPayload(subject, true))
	}

	// Build restart command with --continue so the new session resumes
	// the previous conversation (preserves context across compaction cycles).
	restartCmd, err := buildRestartCommandWithOpts(currentSession, buildRestartCommandOpts{
		ContinueSession: true,
		ContinuePrompt:  "Context compacted. Continue your previous task.",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "handoff --cycle: could not build restart command: %v\n", err)
		return err
	}

	fmt.Fprintf(os.Stderr, "handoff --cycle: cycling session %s\n", currentSession)

	// Set remain-on-exit so the pane survives process death during handoff
	if err := t.SetRemainOnExit(pane, true); err != nil {
		style.PrintWarning("could not set remain-on-exit: %v", err)
	}

	// Clear scrollback history before respawn
	if err := t.ClearHistory(pane); err != nil {
		style.PrintWarning("could not clear history: %v", err)
	}

	// Check if pane's working directory exists (may have been deleted)
	paneWorkDir, _ := t.GetPaneWorkDir(currentSession)
	if paneWorkDir != "" {
		if _, err := os.Stat(paneWorkDir); err != nil {
			if townRoot := detectTownRootFromCwd(); townRoot != "" {
				return t.RespawnPaneWithWorkDir(pane, townRoot, restartCmd)
			}
		}
	}

	// Respawn pane — this atomically kills current process and starts fresh
	return t.RespawnPane(pane, restartCmd)
}

// getCurrentTmuxSession returns the current tmux session name.
func getCurrentTmuxSession() (string, error) {
	// Prefer GT_ROLE for session resolution. BuildCommand uses -L <town-socket>,
	// but the calling process may live on the default socket (e.g., Claude Code
	// spawned by tmux on the default server). In that case, display-message on
	// the town socket returns an arbitrary session (often hq-boot) instead of
	// the caller's actual session.
	if role := os.Getenv("GT_ROLE"); role != "" {
		resolved, err := resolveRoleToSession(role)
		if err == nil && resolved != "" {
			return resolved, nil
		}
		// Fall through to tmux detection if role resolution fails
	}

	// Use TMUX_PANE for targeted display-message to avoid returning an
	// arbitrary session when multiple sessions share the town socket.
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return "", fmt.Errorf("TMUX_PANE not set")
	}
	out, err := tmux.BuildCommand("display-message", "-t", pane, "-p", "#{session_name}").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// resolveRoleToSession converts a role name or path to a tmux session name.
// Accepts:
//   - Role shortcuts: "crew", "witness", "refinery", "mayor", "deacon"
//   - Full paths: "<rig>/crew/<name>", "<rig>/witness", "<rig>/refinery"
//   - Direct session names (passed through)
//
// For role shortcuts that need context (crew, witness, refinery), it auto-detects from environment.
func resolveRoleToSession(role string) (string, error) {
	// First, check if it's a path format (contains /)
	if strings.Contains(role, "/") {
		return resolvePathToSession(role)
	}

	switch strings.ToLower(role) {
	case constants.RoleMayor, "may":
		return getMayorSessionName(), nil

	case constants.RoleDeacon, "dea":
		return getDeaconSessionName(), nil

	case constants.RoleCrew:
		// Try to get rig and crew name from environment or cwd
		rig := os.Getenv("GT_RIG")
		crewName := os.Getenv("GT_CREW")
		if rig == "" || crewName == "" {
			// Try to detect from cwd
			detected, err := detectCrewFromCwd()
			if err == nil {
				rig = detected.rigName
				crewName = detected.crewName
			}
		}
		if rig == "" || crewName == "" {
			return "", fmt.Errorf("cannot determine crew identity - run from crew directory or specify GT_RIG/GT_CREW")
		}
		return session.CrewSessionName(session.PrefixFor(rig), crewName), nil

	case constants.RoleWitness, "wit":
		rig := os.Getenv("GT_RIG")
		if rig == "" {
			return "", fmt.Errorf("cannot determine rig - set GT_RIG or run from rig context")
		}
		return session.WitnessSessionName(session.PrefixFor(rig)), nil

	case constants.RoleRefinery, "ref":
		rig := os.Getenv("GT_RIG")
		if rig == "" {
			return "", fmt.Errorf("cannot determine rig - set GT_RIG or run from rig context")
		}
		return session.RefinerySessionName(session.PrefixFor(rig)), nil

	default:
		// Assume it's a direct session name (e.g., gt-gastown-crew-max)
		return role, nil
	}
}

// resolvePathToSession converts a path like "<rig>/crew/<name>" to a session name.
// Supported formats:
//   - <rig>/crew/<name> -> gt-<rig>-crew-<name>
//   - <rig>/witness -> gt-<rig>-witness
//   - <rig>/refinery -> gt-<rig>-refinery
//   - <rig>/polecats/<name> -> gt-<rig>-<name> (explicit polecat)
//   - <rig>/<name> -> gt-<rig>-<name> (polecat shorthand, if name isn't a known role)
func resolvePathToSession(path string) (string, error) {
	parts := strings.Split(path, "/")

	// Handle <rig>/crew/<name> format
	if len(parts) == 3 && parts[1] == constants.RoleCrew {
		rig := parts[0]
		name := parts[2]
		return session.CrewSessionName(session.PrefixFor(rig), name), nil
	}

	// Handle <rig>/polecats/<name> format (explicit polecat path)
	if len(parts) == 3 && parts[1] == "polecats" {
		rig := parts[0]
		name := strings.ToLower(parts[2]) // normalize polecat name
		return session.PolecatSessionName(session.PrefixFor(rig), name), nil
	}

	// Handle <rig>/<role-or-polecat> format
	if len(parts) == 2 {
		rig := parts[0]
		second := parts[1]
		secondLower := strings.ToLower(second)

		// Check for known roles first
		switch secondLower {
		case constants.RoleWitness:
			return session.WitnessSessionName(session.PrefixFor(rig)), nil
		case constants.RoleRefinery:
			return session.RefinerySessionName(session.PrefixFor(rig)), nil
		case constants.RoleCrew:
			// Just "<rig>/crew" without a name - need more info
			return "", fmt.Errorf("crew path requires name: %s/crew/<name>", rig)
		case "polecats":
			// Just "<rig>/polecats" without a name - need more info
			return "", fmt.Errorf("polecats path requires name: %s/polecats/<name>", rig)
		default:
			// Not a known role - check if it's a crew member before assuming polecat.
			// Crew members exist at <townRoot>/<rig>/crew/<name>.
			// This fixes: gt sling gt-375 gastown/max failing because max is crew, not polecat.
			townRoot := detectTownRootFromCwd()
			if townRoot != "" {
				crewPath := filepath.Join(townRoot, rig, "crew", second)
				if info, err := os.Stat(crewPath); err == nil && info.IsDir() {
					return session.CrewSessionName(session.PrefixFor(rig), second), nil
				}
			}
			// Not a crew member - treat as polecat name (e.g., gastown/nux)
			return session.PolecatSessionName(session.PrefixFor(rig), secondLower), nil
		}
	}

	return "", fmt.Errorf("cannot parse path '%s' - expected <rig>/<polecat>, <rig>/crew/<name>, <rig>/witness, or <rig>/refinery", path)
}

// claudeEnvVars lists the Claude-related environment variables to propagate
// during handoff. These vars aren't inherited by tmux respawn-pane's fresh shell.
var claudeEnvVars = []string{
	// Claude API and config
	"ANTHROPIC_API_KEY",
	"CLAUDE_CODE_USE_BEDROCK",
	// AWS vars for Bedrock
	"AWS_PROFILE",
	"AWS_REGION",
	// OTEL telemetry — propagate so Claude keeps sending metrics after handoff
	// (tmux respawn-pane starts a fresh shell that doesn't inherit these)
	"CLAUDE_CODE_ENABLE_TELEMETRY",
	"OTEL_METRICS_EXPORTER",
	"OTEL_METRIC_EXPORT_INTERVAL",
	"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
	"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
	"OTEL_LOGS_EXPORTER",
	"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
	"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL",
	"OTEL_LOG_TOOL_DETAILS",
	"OTEL_LOG_TOOL_CONTENT",
	"OTEL_LOG_USER_PROMPTS",
	"OTEL_RESOURCE_ATTRIBUTES",
	// bd telemetry — so `bd` calls inside Claude emit to VictoriaMetrics/Logs
	"BD_OTEL_METRICS_URL",
	"BD_OTEL_LOGS_URL",
	// GT telemetry source vars — needed to recompute derived vars after handoff
	"GT_OTEL_METRICS_URL",
	"GT_OTEL_LOGS_URL",
}

// buildRestartCommand creates the command to run when respawning a session's pane.
// This needs to be the actual command to execute (e.g., claude), not a session attach command.
// The command includes a cd to the correct working directory for the role.
//
// buildRestartCommandOpts controls restart command generation.
type buildRestartCommandOpts struct {
	// ContinueSession adds --continue and omits the beacon prompt,
	// so the agent resumes its previous conversation silently.
	ContinueSession bool
	// ContinuePrompt overrides the default continuation prompt when
	// ContinueSession is true. If empty, falls back to a generic
	// continuation message.
	ContinuePrompt string
}

func buildRestartCommand(sessionName string) (string, error) {
	return buildRestartCommandWithOpts(sessionName, buildRestartCommandOpts{})
}

func buildRestartCommandWithOpts(sessionName string, opts buildRestartCommandOpts) (string, error) {
	// Detect town root from current directory
	townRoot := detectTownRootFromCwd()
	if townRoot == "" {
		return "", fmt.Errorf("cannot detect town root - run from within a Gas Town workspace")
	}

	// Determine the working directory for this session type
	workDir, err := sessionWorkDir(sessionName, townRoot)
	if err != nil {
		return "", err
	}

	// Parse the session name to get the identity (used for GT_ROLE and beacon)
	identity, err := session.ParseSessionName(sessionName)
	if err != nil {
		return "", fmt.Errorf("cannot parse session name %q: %w", sessionName, err)
	}
	gtRole := identity.GTRole()
	simpleRole := config.ExtractSimpleRole(gtRole)

	// Derive rigPath from session identity for --settings flag resolution
	rigPath := ""
	if identity.Rig != "" {
		rigPath = filepath.Join(townRoot, identity.Rig)
	}

	// Build startup beacon for predecessor discovery via /resume.
	// When ContinueSession is set, use a continuation prompt instead of
	// the full handoff beacon — the agent resumes its previous context.
	beacon := ""
	if opts.ContinueSession {
		if opts.ContinuePrompt != "" {
			beacon = opts.ContinuePrompt
		} else {
			beacon = "Your account was rotated to avoid a rate limit. Continue your previous task."
		}
	} else {
		beacon = session.FormatStartupBeacon(session.BeaconConfig{
			Recipient: identity.BeaconAddress(),
			Sender:    "self",
			Topic:     "handoff",
		})
	}

	// For respawn-pane, we:
	// 1. cd to the right directory (role's canonical home)
	// 2. export GT_ROLE and BD_ACTOR so role detection works correctly
	// 3. export Claude-related env vars (not inherited by fresh shell)
	// 4. run claude with the startup beacon (triggers immediate context loading)
	// Use exec to ensure clean process replacement.
	//
	// Agent resolution priority:
	// 1. role_agents config (authoritative — config changes take effect on next handoff)
	// 2. GT_AGENT env var (only if role has no config mapping — manual override case)
	// 3. default_agent fallback
	//
	// Previously GT_AGENT took unconditional priority, which meant config changes
	// to role_agents were invisible until sessions were manually restarted.
	currentAgent, agentInEnv := os.LookupEnv("GT_AGENT")
	if !agentInEnv {
		// GT_AGENT not in process env at all — try tmux session environment
		// as fallback, since exec env vars may not propagate through all runtimes.
		t := tmux.NewTmux()
		if val, err := t.GetEnvironment(sessionName, "GT_AGENT"); err == nil && val != "" {
			currentAgent = val
		}
	}
	var runtimeCmd string
	if simpleRole != "" {
		// Check if role_agents has a mapping for this role — if so, use it
		// (config is authoritative over stale GT_AGENT env vars).
		configAgent, isRoleSpecific := config.ResolveRoleAgentName(simpleRole, townRoot, rigPath)
		if isRoleSpecific {
			// Config has an explicit mapping — use it even if GT_AGENT differs.
			// This ensures config changes (e.g., crew→opus) take effect on handoff.
			runtimeCmd = config.ResolveRoleAgentConfig(simpleRole, townRoot, rigPath).BuildCommandWithPrompt(beacon)
			_ = configAgent // used by ResolveRoleAgentConfig internally
		} else if currentAgent != "" {
			// No role-specific config but GT_AGENT is set (manual override) — preserve it.
			var err error
			runtimeCmd, err = config.GetRuntimeCommandWithPromptAndAgentOverride(rigPath, beacon, currentAgent)
			if err != nil {
				return "", fmt.Errorf("resolving agent config: %w", err)
			}
		} else {
			runtimeCmd = config.ResolveRoleAgentConfig(simpleRole, townRoot, rigPath).BuildCommandWithPrompt(beacon)
		}
	} else if currentAgent != "" {
		var err error
		runtimeCmd, err = config.GetRuntimeCommandWithPromptAndAgentOverride(rigPath, beacon, currentAgent)
		if err != nil {
			return "", fmt.Errorf("resolving agent config: %w", err)
		}
	} else {
		runtimeCmd = config.GetRuntimeCommandWithPrompt(rigPath, beacon)
	}

	// Add --continue flag to resume the most recent session.
	// Note: runtimeCmd starts with the command name (e.g., "claude --settings ..."),
	// not "exec claude" — the "exec" prefix is added later in the Sprintf.
	if opts.ContinueSession {
		runtimeCmd = strings.Replace(runtimeCmd, "claude ", "claude --continue ", 1)
	}

	// Build environment exports - role vars first, then Claude vars
	var exports []string
	var agentEnv map[string]string // agent config Env (rc.toml [agents.X.env])
	if gtRole != "" {
		// Resolve agent config for environment variables (NODE_OPTIONS, etc.).
		// When role_agents is authoritative, use role config; otherwise use GT_AGENT override.
		var runtimeConfig *config.RuntimeConfig
		if simpleRole != "" {
			if _, isRoleSpecific := config.ResolveRoleAgentName(simpleRole, townRoot, rigPath); isRoleSpecific {
				runtimeConfig = config.ResolveRoleAgentConfig(simpleRole, townRoot, rigPath)
			} else if currentAgent != "" {
				rc, _, err := config.ResolveAgentConfigWithOverride(townRoot, rigPath, currentAgent)
				if err == nil {
					runtimeConfig = rc
				} else {
					runtimeConfig = config.ResolveRoleAgentConfig(simpleRole, townRoot, rigPath)
				}
			} else {
				runtimeConfig = config.ResolveRoleAgentConfig(simpleRole, townRoot, rigPath)
			}
		} else if currentAgent != "" {
			rc, _, err := config.ResolveAgentConfigWithOverride(townRoot, rigPath, currentAgent)
			if err == nil {
				runtimeConfig = rc
			} else {
				runtimeConfig = config.ResolveAgentConfig(townRoot, rigPath)
			}
		} else {
			runtimeConfig = config.ResolveAgentConfig(townRoot, rigPath)
		}
		agentEnv = runtimeConfig.Env
		exports = append(exports, "GT_ROLE="+gtRole)
		exports = append(exports, "BD_ACTOR="+gtRole)
		exports = append(exports, "GIT_AUTHOR_NAME="+gtRole)
		if runtimeConfig.Session != nil && runtimeConfig.Session.SessionIDEnv != "" {
			exports = append(exports, "GT_SESSION_ID_ENV="+runtimeConfig.Session.SessionIDEnv)
		}
	}

	// Propagate GT_ROOT so subsequent handoffs can use it as fallback
	// when cwd-based detection fails (broken state recovery)
	exports = append(exports, "GT_ROOT="+townRoot)

	// Preserve GT_AGENT across handoff so agent override persists.
	// When role_agents config is authoritative (isRoleSpecific), export the
	// config-resolved agent name so GT_AGENT stays in sync with the actual model.
	if simpleRole != "" {
		if configAgent, isRoleSpecific := config.ResolveRoleAgentName(simpleRole, townRoot, rigPath); isRoleSpecific {
			exports = append(exports, "GT_AGENT="+configAgent)
		} else if currentAgent != "" {
			exports = append(exports, "GT_AGENT="+currentAgent)
		}
	} else if currentAgent != "" {
		exports = append(exports, "GT_AGENT="+currentAgent)
	}

	// Preserve GT_PROCESS_NAMES across handoff for accurate liveness detection.
	// Without this, custom agents that shadow built-in presets (e.g., custom
	// "codex" running "opencode") would revert to GT_AGENT-based lookup after
	// handoff, causing false liveness failures.
	if processNames := os.Getenv("GT_PROCESS_NAMES"); processNames != "" {
		// Preserve existing process names from environment
		exports = append(exports, "GT_PROCESS_NAMES="+processNames)
	} else if currentAgent != "" {
		// First boot or missing GT_PROCESS_NAMES — compute from agent config
		resolved := config.ResolveProcessNames(currentAgent, "")
		exports = append(exports, "GT_PROCESS_NAMES="+strings.Join(resolved, ","))
	}

	// Add Claude-related env vars from current environment
	for _, name := range claudeEnvVars {
		if val := os.Getenv(name); val != "" {
			// Shell-escape the value in case it contains special chars
			exports = append(exports, fmt.Sprintf("%s=%q", name, val))
		}
	}

	// Merge all agent preset env vars from config.json [agents.X.env] so the
	// new session inherits the same custom configuration (e.g. ANTHROPIC_BASE_URL,
	// CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_CUSTOM_HEADERS for proxied Claude).
	// This mirrors the first-spawn path in config/loader.go where preset.Env is
	// merged into RuntimeConfig.Env. Existing keys (GT_ROLE, BD_ACTOR, etc.) take
	// precedence over agent-defined keys.
	exportedKeys := make(map[string]bool)
	for _, e := range exports {
		if idx := strings.Index(e, "="); idx > 0 {
			exportedKeys[e[:idx]] = true
		}
	}
	for k, v := range agentEnv {
		if k == "NODE_OPTIONS" {
			continue // handled separately below
		}
		if !exportedKeys[k] {
			exports = append(exports, fmt.Sprintf("%s=%q", k, v))
		}
	}

	// Clear NODE_OPTIONS to prevent debugger flags (e.g., --inspect from VSCode)
	// from being inherited through tmux into Claude's Node.js runtime.
	// When the agent's runtime config explicitly sets NODE_OPTIONS (e.g., for
	// memory tuning via --max-old-space-size in rc.toml [agents.X.env]), export
	// that value so it survives handoff. Otherwise clear it.
	// Note: agentEnv is intentionally nil when gtRole is empty (non-role handoffs),
	// which causes the nil map lookup to return ("", false) — clearing NODE_OPTIONS.
	if val, hasNodeOpts := agentEnv["NODE_OPTIONS"]; hasNodeOpts {
		exports = append(exports, fmt.Sprintf("NODE_OPTIONS=%q", val))
	} else {
		exports = append(exports, "NODE_OPTIONS=")
	}

	if len(exports) > 0 {
		return fmt.Sprintf("cd %s && export %s && exec %s", workDir, strings.Join(exports, " "), runtimeCmd), nil
	}
	return fmt.Sprintf("cd %s && exec %s", workDir, runtimeCmd), nil
}

// updateSessionEnvForHandoff updates the tmux session environment with the
// agent name and process names for liveness detection. IsAgentAlive reads
// GT_PROCESS_NAMES from the tmux session env (via tmux show-environment), not
// from shell exports in the pane. Without this, post-handoff liveness checks
// would use stale values from the previous agent.
func updateSessionEnvForHandoff(t *tmux.Tmux, sessionName, agentOverride string) {
	// Resolve current agent using the same priority as buildRestartCommandWithAgent
	var currentAgent string
	if agentOverride != "" {
		currentAgent = agentOverride
	} else {
		currentAgent = os.Getenv("GT_AGENT")
		if currentAgent == "" {
			if val, err := t.GetEnvironment(sessionName, "GT_AGENT"); err == nil && val != "" {
				currentAgent = val
			}
		}
	}

	if currentAgent == "" {
		return
	}

	// Update GT_AGENT in session env
	_ = t.SetEnvironment(sessionName, "GT_AGENT", currentAgent)

	// Resolve and update GT_PROCESS_NAMES in session env
	// When switching agents, recompute from config. When preserving, use env value.
	var processNames string
	if agentOverride != "" {
		// Agent is changing — resolve config to get the command for process name resolution
		townRoot := detectTownRootFromCwd()
		if townRoot != "" {
			identity, err := session.ParseSessionName(sessionName)
			rigPath := ""
			if err == nil && identity.Rig != "" {
				rigPath = filepath.Join(townRoot, identity.Rig)
			}
			rc, _, err := config.ResolveAgentConfigWithOverride(townRoot, rigPath, currentAgent)
			if err == nil {
				resolved := config.ResolveProcessNames(currentAgent, rc.Command)
				processNames = strings.Join(resolved, ",")
			}
		}
	}
	if processNames == "" {
		// Preserve existing value or compute from current agent
		if pn := os.Getenv("GT_PROCESS_NAMES"); pn != "" {
			processNames = pn
		} else {
			resolved := config.ResolveProcessNames(currentAgent, "")
			processNames = strings.Join(resolved, ",")
		}
	}

	_ = t.SetEnvironment(sessionName, "GT_PROCESS_NAMES", processNames)
}

// sessionWorkDir returns the correct working directory for a session.
// This is the canonical home for each role type.
func sessionWorkDir(sessionName, townRoot string) (string, error) {
	// Get session names for comparison
	mayorSession := getMayorSessionName()
	deaconSession := getDeaconSessionName()

	bootSession := session.BootSessionName()

	switch {
	case sessionName == mayorSession:
		// Mayor runs from ~/gt/mayor/, not town root.
		// Tools use workspace.FindFromCwd() which walks UP to find town root.
		return townRoot + "/mayor", nil

	case sessionName == bootSession:
		// Boot watchdog runs from ~/gt/deacon/dogs/boot/, not ~/gt/deacon/.
		// Boot is ephemeral (fresh each daemon tick) with its own CLAUDE.md.
		return townRoot + "/deacon/dogs/boot", nil

	case sessionName == deaconSession:
		return townRoot + "/deacon", nil

	case strings.Contains(sessionName, "-crew-"):
		// gt-<rig>-crew-<name> -> <townRoot>/<rig>/crew/<name>
		rig, name, _, ok := parseCrewSessionName(sessionName)
		if !ok {
			return "", fmt.Errorf("cannot parse crew session name: %s", sessionName)
		}
		return fmt.Sprintf("%s/%s/crew/%s", townRoot, rig, name), nil

	default:
		// Parse session name to determine role and resolve paths
		identity, err := session.ParseSessionName(sessionName)
		if err != nil {
			return "", fmt.Errorf("unknown session type: %s (%w)", sessionName, err)
		}
		switch identity.Role {
		case session.RoleMayor:
			return townRoot + "/mayor", nil
		case session.RoleDeacon:
			return townRoot + "/deacon", nil
		case session.RoleOverseer:
			return townRoot + "/deacon", nil
		case session.RoleWitness:
			return fmt.Sprintf("%s/%s/witness", townRoot, identity.Rig), nil
		case session.RoleRefinery:
			return fmt.Sprintf("%s/%s/refinery/rig", townRoot, identity.Rig), nil
		case session.RolePolecat:
			return fmt.Sprintf("%s/%s/polecats/%s", townRoot, identity.Rig, identity.Name), nil
		default:
			return "", fmt.Errorf("unknown session type: %s (role %s, try specifying role explicitly)", sessionName, identity.Role)
		}
	}
}

// sessionToGTRole converts a session name to a GT_ROLE value.
// Uses session.ParseSessionName for consistent parsing across the codebase.
func sessionToGTRole(sessionName string) string {
	identity, err := session.ParseSessionName(sessionName)
	if err != nil {
		return ""
	}
	return identity.GTRole()
}

// detectTownRootFromCwd walks up from the current directory to find the town root.
// Falls back to GT_TOWN_ROOT or GT_ROOT env vars if cwd detection fails (broken state recovery).
func detectTownRootFromCwd() string {
	// Use workspace.FindFromCwd which handles both primary (mayor/town.json)
	// and secondary (mayor/ directory) markers
	townRoot, err := workspace.FindFromCwd()
	if err == nil && townRoot != "" {
		return townRoot
	}

	// Fallback: try environment variables for town root
	// GT_TOWN_ROOT is set by shell integration, GT_ROOT is set by session manager
	// This enables handoff to work even when cwd detection fails due to
	// detached HEAD, wrong branch, deleted worktree, etc.
	for _, envName := range []string{"GT_TOWN_ROOT", "GT_ROOT"} {
		if envRoot := os.Getenv(envName); envRoot != "" {
			// Verify it's actually a workspace
			if _, statErr := os.Stat(filepath.Join(envRoot, workspace.PrimaryMarker)); statErr == nil {
				return envRoot
			}
			// Try secondary marker too
			if info, statErr := os.Stat(filepath.Join(envRoot, workspace.SecondaryMarker)); statErr == nil && info.IsDir() {
				return envRoot
			}
		}
	}

	// Final fallback: read GT_TOWN_ROOT from tmux global environment.
	// This handles the run-shell case where CWD is $HOME and process env
	// vars aren't set — the daemon sets GT_TOWN_ROOT in tmux global env.
	if socket := tmux.SocketFromEnv(); socket != "" {
		t := tmux.NewTmuxWithSocket(socket)
		if envRoot, err := t.GetGlobalEnvironment("GT_TOWN_ROOT"); err == nil && envRoot != "" {
			if _, statErr := os.Stat(filepath.Join(envRoot, workspace.PrimaryMarker)); statErr == nil {
				return envRoot
			}
			if info, statErr := os.Stat(filepath.Join(envRoot, workspace.SecondaryMarker)); statErr == nil && info.IsDir() {
				return envRoot
			}
		}
	}

	return ""
}

// checkSessionProtected checks if a session has active protection.
// Returns (true, reason) if protected, (false, "") otherwise.
func checkSessionProtected(t *tmux.Tmux, session string) (bool, string) {
	return t.IsSessionProtected(session, constants.MaxSessionProtectionDuration)
}

// handoffRemoteSession respawns a different session and optionally switches to it.
func handoffRemoteSession(t *tmux.Tmux, targetSession, restartCmd string) error {
	// Check if target session exists
	exists, err := t.HasSession(targetSession)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if !exists {
		return fmt.Errorf("session '%s' not found - is the agent running?", targetSession)
	}

	// Check session protection before respawning
	if !handoffForce {
		if protected, reason := checkSessionProtected(t, targetSession); protected {
			return fmt.Errorf("session '%s' is protected (%s) - use --force to override", targetSession, reason)
		}
	}

	// Get the pane ID for the target session
	targetPane, err := getSessionPane(targetSession)
	if err != nil {
		return fmt.Errorf("getting target pane: %w", err)
	}

	fmt.Printf("%s Handing off %s...\n", style.Bold.Render("🤝"), targetSession)

	// Dry run mode
	if handoffDryRun {
		fmt.Printf("Would execute: tmux clear-history -t %s\n", targetPane)
		fmt.Printf("Would execute: tmux respawn-pane -k -t %s %s\n", targetPane, restartCmd)
		if handoffWatch {
			fmt.Printf("Would execute: tmux switch-client -t %s\n", targetSession)
		}
		return nil
	}

	// Set remain-on-exit so the pane survives process death during handoff.
	// Without this, killing processes causes tmux to destroy the pane before
	// we can respawn it. This is essential for tmux session reuse.
	if err := t.SetRemainOnExit(targetPane, true); err != nil {
		style.PrintWarning("could not set remain-on-exit: %v", err)
	}

	// Kill all processes in the pane before respawning to prevent orphan leaks
	// RespawnPane's -k flag only sends SIGHUP which Claude/Node may ignore
	if err := t.KillPaneProcesses(targetPane); err != nil {
		// Non-fatal but log the warning
		style.PrintWarning("could not kill pane processes: %v", err)
	}

	// Clear scrollback history before respawn (resets copy-mode from [0/N] to [0/0])
	if err := t.ClearHistory(targetPane); err != nil {
		// Non-fatal - continue with respawn even if clear fails
		style.PrintWarning("could not clear history: %v", err)
	}

	// Respawn the remote session's pane, handling deleted working directories
	respawnErr := func() error {
		paneWorkDir, _ := t.GetPaneWorkDir(targetSession)
		if paneWorkDir != "" {
			if _, statErr := os.Stat(paneWorkDir); statErr != nil {
				if townRoot := detectTownRootFromCwd(); townRoot != "" {
					style.PrintWarning("pane working directory deleted, using town root")
					return t.RespawnPaneWithWorkDir(targetPane, townRoot, restartCmd)
				}
			}
		}
		return t.RespawnPane(targetPane, restartCmd)
	}()
	if respawnErr != nil {
		return fmt.Errorf("respawning pane: %w", respawnErr)
	}

	// If --watch, switch to that session
	if handoffWatch {
		fmt.Printf("Switching to %s...\n", targetSession)
		// Use tmux switch-client to move our view to the target session
		if err := tmux.BuildCommand("switch-client", "-t", targetSession).Run(); err != nil {
			// Non-fatal - they can manually switch
			fmt.Printf("Note: Could not auto-switch (use: tmux switch-client -t %s)\n", targetSession)
		}
	}

	return nil
}

// getSessionPane returns the pane identifier for a session's main pane.
func getSessionPane(sessionName string) (string, error) {
	// Get the pane ID for the first pane in the session
	out, err := tmux.BuildCommand("list-panes", "-t", sessionName, "-F", "#{pane_id}").Output()
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return "", fmt.Errorf("no panes found in session")
	}
	return lines[0], nil
}

// sendHandoffMail sends a handoff mail to self and auto-hooks it.
// Returns the created bead ID and any error.
func sendHandoffMail(subject, message string) (string, error) {
	// Build subject with handoff prefix if not already present
	if subject == "" {
		subject = "🤝 HANDOFF: Session cycling"
	} else if !strings.Contains(subject, "HANDOFF") {
		subject = "🤝 HANDOFF: " + subject
	}

	// Default message if not provided
	if message == "" {
		message = "Context cycling. Check bd ready for pending work."
	}

	// Detect agent identity for self-mail
	agentID, _, _, err := resolveSelfTarget()
	if err != nil {
		return "", fmt.Errorf("detecting agent identity: %w", err)
	}

	// Normalize identity to match mailbox query format
	agentID = mail.AddressToIdentity(agentID)

	// Detect town root for beads location
	townRoot := detectTownRootFromCwd()
	if townRoot == "" {
		return "", fmt.Errorf("cannot detect town root")
	}

	// Build labels for mail metadata (matches mail router format)
	labels := fmt.Sprintf("from:%s", agentID)

	// Close stale hooked mail beads from previous sessions before creating a new one.
	// Without this, each handoff cycle accumulates beads in status=hooked. (GH#3859)
	townB := beads.New(filepath.Join(townRoot, ".beads"))
	if n, closeErr := townB.CloseStaleHookedMailBeads(agentID); closeErr != nil {
		style.PrintWarning("couldn't close previous hooked mail bead(s): %v", closeErr)
	} else if n > 0 {
		fmt.Printf("%s Closed %d stale hooked mail bead(s)\n", style.Dim.Render("🧹"), n)
	}

	// Create mail bead directly using bd create with --silent to get the ID
	// Mail goes to town-level beads (hq- prefix)
	// Flags go first, then -- to end flag parsing, then the positional subject.
	// This prevents subjects like "--help" from being parsed as flags.
	args := []string{
		"create",
		"--assignee", agentID,
		"-d", message,
		"--priority", "1", // high — handoffs should float above normal mail
		"--labels", labels + ",gt:message",
		"--actor", agentID,
		// NOT ephemeral: handoff mail must be in issues table so gt hook can find it.
		// Ephemeral wisps are invisible to hook queries and may be reaped before successor reads.
		"--silent", // Output only the bead ID
		"--", subject,
	}

	cmd := BdCmd(args...).
		WithAutoCommit().
		Dir(townRoot).
		Build()
	cmd.Env = append(cmd.Env, "BEADS_DIR="+filepath.Join(townRoot, ".beads"))

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("creating handoff mail: %s", errMsg)
		}
		return "", fmt.Errorf("creating handoff mail: %w", err)
	}

	beadID := strings.TrimSpace(stdout.String())
	if beadID == "" {
		return "", fmt.Errorf("bd create did not return bead ID")
	}

	// Auto-hook the created mail bead
	hookCmd := BdCmd("update", beadID, "--status=hooked", "--assignee="+agentID).
		WithAutoCommit().
		Dir(townRoot).
		Build()
	hookCmd.Env = append(hookCmd.Env, "BEADS_DIR="+filepath.Join(townRoot, ".beads"))
	hookCmd.Stderr = os.Stderr

	if err := hookCmd.Run(); err != nil {
		// Non-fatal: mail was created, just couldn't hook
		style.PrintWarning("created mail %s but failed to auto-hook: %v", beadID, err)
		return beadID, nil
	}

	return beadID, nil
}

// warnHandoffGitStatus checks the current workspace for uncommitted or unpushed
// work and prints a warning if found. Non-blocking — handoff continues regardless.
// Skips .beads/ changes since those are managed by Dolt and not a concern.
func warnHandoffGitStatus() {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	g := git.NewGit(cwd)
	if !g.IsRepo() {
		return
	}
	status, err := g.CheckUncommittedWork()
	if err != nil || status.CleanExcludingBeads() {
		return
	}
	fmt.Fprintf(os.Stderr, "%s workspace has uncommitted work: %s\n", ui.IconWarn, status.String())
	if len(status.ModifiedFiles) > 0 {
		fmt.Fprintf(os.Stderr, "%s   modified: %s\n", ui.IconWarn, strings.Join(status.ModifiedFiles, ", "))
	}
	if len(status.UntrackedFiles) > 0 {
		fmt.Fprintf(os.Stderr, "%s   untracked: %s\n", ui.IconWarn, strings.Join(status.UntrackedFiles, ", "))
	}
	if status.UnpushedCommits > 0 {
		fmt.Fprintf(os.Stderr, "%s   %d unpushed commit(s) — run 'git push' before handoff\n", ui.IconWarn, status.UnpushedCommits)
	}
	fmt.Fprintln(os.Stderr, "  (use --no-git-check to suppress this warning)")
}

// looksLikeBeadID checks if a string looks like a bead ID.
// Bead IDs have format: prefix-xxxx where prefix is 1-5 lowercase letters and xxxx is alphanumeric.
// Examples: "gt-abc123", "bd-ka761", "hq-cv-abc", "beads-xyz", "ap-qtsup.16"
func looksLikeBeadID(s string) bool {
	// Find the first hyphen
	idx := strings.Index(s, "-")
	if idx < 1 || idx > 5 {
		// No hyphen, or prefix is empty/too long
		return false
	}

	// Check prefix is all lowercase letters
	prefix := s[:idx]
	for _, c := range prefix {
		if c < 'a' || c > 'z' {
			return false
		}
	}

	// Check there's something after the hyphen
	rest := s[idx+1:]
	if len(rest) == 0 {
		return false
	}

	// Check rest starts with alphanumeric and contains only alphanumeric, dots, hyphens
	first := rest[0]
	if !((first >= 'a' && first <= 'z') || (first >= '0' && first <= '9')) {
		return false
	}

	return true
}

// hookBeadForHandoff attaches a bead to the current agent's hook.
func hookBeadForHandoff(beadID string) error {
	// Verify the bead exists first
	verifyCmd := exec.Command("bd", "show", beadID, "--json")
	if err := verifyCmd.Run(); err != nil {
		return fmt.Errorf("bead '%s' not found", beadID)
	}

	// Determine agent identity
	agentID, _, _, err := resolveSelfTarget()
	if err != nil {
		return fmt.Errorf("detecting agent identity: %w", err)
	}

	fmt.Printf("%s Hooking %s...\n", style.Bold.Render("🪝"), beadID)

	if handoffDryRun {
		fmt.Printf("Would run: bd update %s --status=pinned --assignee=%s\n", beadID, agentID)
		return nil
	}

	// Pin the bead using bd update (discovery-based approach)
	pinCmd := exec.Command("bd", "update", beadID, "--status=pinned", "--assignee="+agentID)
	pinCmd.Stderr = os.Stderr
	if err := pinCmd.Run(); err != nil {
		return fmt.Errorf("pinning bead: %w", err)
	}

	fmt.Printf("%s Work attached to hook (pinned bead)\n", style.Bold.Render("✓"))
	return nil
}

// collectHandoffState gathers current state for handoff context.
// Collects: git workspace state (deterministic), inbox summary, ready beads, hooked work.
// Git state is always collected first via Go library calls (no shelling out) to ensure
// the handoff always contains useful context even when external commands fail. (GH#1996)
func collectHandoffState() string {
	var parts []string

	// Deterministic git state — always collected via Go library, never empty. (GH#1996)
	if gitState := collectGitState(); gitState != "" {
		parts = append(parts, gitState)
	}

	// Get hooked work
	hookOutput, err := exec.Command("gt", "hook").Output()
	if err == nil {
		hookStr := strings.TrimSpace(string(hookOutput))
		if hookStr != "" && !strings.Contains(hookStr, "Nothing on hook") {
			parts = append(parts, "## Hooked Work\n"+hookStr)
		}
	}

	// Get inbox summary (first few messages)
	inboxOutput, err := exec.Command("gt", "mail", "inbox").Output()
	if err == nil {
		inboxStr := strings.TrimSpace(string(inboxOutput))
		if inboxStr != "" && !strings.Contains(inboxStr, "Inbox empty") {
			// Limit to first 10 lines for brevity
			lines := strings.Split(inboxStr, "\n")
			if len(lines) > 10 {
				lines = append(lines[:10], "... (more messages)")
			}
			parts = append(parts, "## Inbox\n"+strings.Join(lines, "\n"))
		}
	}

	// Get ready beads
	readyOutput, err := exec.Command("bd", "ready").Output()
	if err == nil {
		readyStr := strings.TrimSpace(string(readyOutput))
		if readyStr != "" && !strings.Contains(readyStr, "No issues ready") {
			// Limit to first 10 lines
			lines := strings.Split(readyStr, "\n")
			if len(lines) > 10 {
				lines = append(lines[:10], "... (more issues)")
			}
			parts = append(parts, "## Ready Work\n"+strings.Join(lines, "\n"))
		}
	}

	// Get in-progress beads
	inProgressOutput, err := exec.Command("bd", "list", "--status=in_progress").Output()
	if err == nil {
		ipStr := strings.TrimSpace(string(inProgressOutput))
		if ipStr != "" && !strings.Contains(ipStr, "No issues") {
			lines := strings.Split(ipStr, "\n")
			if len(lines) > 5 {
				lines = append(lines[:5], "... (more)")
			}
			parts = append(parts, "## In Progress\n"+strings.Join(lines, "\n"))
		}
	}

	if len(parts) == 0 {
		return "No active state to report."
	}

	return strings.Join(parts, "\n\n")
}

// collectGitState captures deterministic workspace state using the Go git library.
// This uses only the git.Git wrapper (no shelling out to gt/bd), so it works
// reliably even when PATH is broken or external commands are unavailable.
// Returns empty string if git state cannot be read (e.g., not in a git repo). (GH#1996)
func collectGitState() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	g := git.NewGit(cwd)
	if !g.IsRepo() {
		return ""
	}

	var lines []string

	// Branch
	if branch, err := g.CurrentBranch(); err == nil && branch != "" {
		lines = append(lines, "Branch: "+branch)
	}

	// Uncommitted work summary (skip section on error, don't bail entirely)
	if work, err := g.CheckUncommittedWork(); err == nil {
		if work.HasUncommittedChanges {
			if len(work.ModifiedFiles) > 0 {
				files := work.ModifiedFiles
				if len(files) > 10 {
					files = append(files[:10], fmt.Sprintf("... (+%d more)", len(work.ModifiedFiles)-10))
				}
				lines = append(lines, "Modified: "+strings.Join(files, ", "))
			}
			if len(work.UntrackedFiles) > 0 {
				files := work.UntrackedFiles
				if len(files) > 5 {
					files = append(files[:5], fmt.Sprintf("... (+%d more)", len(work.UntrackedFiles)-5))
				}
				lines = append(lines, "Untracked: "+strings.Join(files, ", "))
			}
		}
		if work.StashCount > 0 {
			lines = append(lines, fmt.Sprintf("Stashes: %d", work.StashCount))
		}
		if work.UnpushedCommits > 0 {
			lines = append(lines, fmt.Sprintf("Unpushed commits: %d", work.UnpushedCommits))
		}
	}

	// Recent commits (last 5) for context on what was being worked on.
	if logStr, err := g.RecentCommits(5); err == nil && logStr != "" {
		lines = append(lines, "Recent commits:\n"+logStr)
	}

	if len(lines) == 0 {
		return ""
	}

	return "## Workspace State\n" + strings.Join(lines, "\n")
}

// cleanupMoleculeOnHandoff closes any in-progress molecule steps before session
// handoff, preventing orphaned wisps from accumulating. (gt-e26g)
//
// Without this, patrol agents (witness, refinery, deacon) that handoff mid-cycle
// leave unfinished molecule steps open forever. The next session pours a new
// molecule, so the old steps are never completed.
//
// All errors are non-fatal — handoff must succeed even if cleanup fails.
func cleanupMoleculeOnHandoff() {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return
	}

	// Detect agent identity
	roleInfo, err := GetRoleWithContext(cwd, townRoot)
	if err != nil {
		return
	}
	roleCtx := RoleContext{
		Role:     roleInfo.Role,
		Rig:      roleInfo.Rig,
		Polecat:  roleInfo.Polecat,
		TownRoot: townRoot,
		WorkDir:  cwd,
	}
	agentID := buildAgentIdentity(roleCtx)
	if agentID == "" {
		return
	}

	workDir, err := findLocalBeadsDir()
	if err != nil {
		return
	}

	b := beads.New(workDir)

	// Extract the role name for FindHandoffBead
	parts := strings.Split(agentID, "/")
	role := parts[len(parts)-1]

	handoffBead, err := b.FindHandoffBead(role)
	if err != nil || handoffBead == nil {
		return
	}

	// Check for attached molecule on the handoff bead
	attachment := beads.ParseAttachmentFields(handoffBead)
	if attachment == nil || attachment.AttachedMolecule == "" {
		return
	}

	molID := attachment.AttachedMolecule

	// Close descendant steps (the leaked wisps)
	if n := closeDescendants(b, molID); n > 0 {
		fmt.Fprintf(os.Stderr, "handoff: closed %d molecule step(s) for %s\n", n, molID)
	}

	// Detach molecule with audit trail
	if _, err := b.DetachMoleculeWithAudit(handoffBead.ID, beads.DetachOptions{
		Operation: "squash",
		Reason:    "handoff: session cycling",
	}); err != nil {
		fmt.Fprintf(os.Stderr, "handoff: warning: detach molecule audit failed: %v\n", err)
	}

	// Close all descendant wisps first, then the molecule root.
	// Without this, handoff leaks orphan wisps into the DB.
	// Best-effort in handoff path — log but proceed.
	if _, err := forceCloseDescendants(b, molID); err != nil {
		style.PrintWarning("handoff: could not close descendants of %s: %v", molID, err)
	}

	// Force-close the molecule root wisp
	if err := b.ForceCloseWithReason("handoff", molID); err != nil {
		fmt.Fprintf(os.Stderr, "handoff: warning: couldn't close molecule %s: %v\n", molID, err)
	}
}

// enforceHandoffCooldown sleeps if the last handoff was too recent.
// This prevents tight restart loops when patrol agents (e.g., witness)
// complete quickly on idle rigs and immediately hand off. (gt-058d)
//
// The cooldown is based on the modification time of the last_handoff_ts
// file in the .runtime directory. If the file exists and was written
// less than MinHandoffCooldown ago, the function sleeps for the remaining
// time. This ensures at least MinHandoffCooldown passes between handoffs.
//
// Crew and mayor roles are exempt — they hand off on human request,
// not on patrol loops, so the cooldown just gets in the way.
func enforceHandoffCooldown() {
	if role := os.Getenv("GT_ROLE"); role != "" {
		parsed, _, _ := parseRoleString(role)
		switch parsed {
		case RoleMayor, RoleCrew:
			return
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return
	}

	tsPath := filepath.Join(cwd, constants.DirRuntime, constants.FileLastHandoffTS)
	info, err := os.Stat(tsPath)
	if err != nil {
		return // No previous handoff recorded — first handoff, no cooldown
	}

	age := time.Since(info.ModTime())
	if age >= constants.MinHandoffCooldown {
		return // Enough time has passed
	}

	remaining := constants.MinHandoffCooldown - age
	fmt.Printf("%s Handoff cooldown: waiting %v (last handoff %v ago, min %v)\n",
		style.Dim.Render("⏳"), remaining.Round(time.Second),
		age.Round(time.Second), constants.MinHandoffCooldown)
	time.Sleep(remaining)
}

// recordHandoffTime writes the current timestamp to the handoff cooldown file.
// Called before respawning to establish the baseline for the next cooldown check.
func recordHandoffTime() {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}

	runtimeDir := filepath.Join(cwd, constants.DirRuntime)
	_ = os.MkdirAll(runtimeDir, 0755)
	tsPath := filepath.Join(runtimeDir, constants.FileLastHandoffTS)
	_ = os.WriteFile(tsPath, []byte(fmt.Sprintf("%d", time.Now().Unix())), 0644)
}
