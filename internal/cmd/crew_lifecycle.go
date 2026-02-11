package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/crew"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/runtime"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/townlog"
	"github.com/steveyegge/gastown/internal/workspace"
)

func runCrewRemove(cmd *cobra.Command, args []string) error {
	var lastErr error

	// --purge implies --force
	forceRemove := crewForce || crewPurge

	for _, arg := range args {
		name := arg
		rigOverride := crewRig

		// Parse rig/name format (e.g., "beads/emma" -> rig=beads, name=emma)
		if rig, crewName, ok := parseRigSlashName(name); ok {
			if rigOverride == "" {
				rigOverride = rig
			}
			name = crewName
		}

		crewMgr, r, err := getCrewManagerForMember(rigOverride, name)
		if err != nil {
			fmt.Printf("Error removing %s: %v\n", arg, err)
			lastErr = err
			continue
		}

		// Check for running session (unless forced)
		if !forceRemove {
			t := tmux.NewTmux()
			sessionID := crewSessionName(r.Name, name)
			hasSession, _ := t.HasSession(sessionID)
			if hasSession {
				fmt.Printf("Error removing %s: session '%s' is running (use --force to kill and remove)\n", arg, sessionID)
				lastErr = fmt.Errorf("session running")
				continue
			}
		}

		// Kill session if it exists (with proper process cleanup to avoid orphans)
		t := tmux.NewTmux()
		sessionID := crewSessionName(r.Name, name)
		if hasSession, _ := t.HasSession(sessionID); hasSession {
			if err := t.KillSessionWithProcesses(sessionID); err != nil {
				fmt.Printf("Error killing session for %s: %v\n", arg, err)
				lastErr = err
				continue
			}
			fmt.Printf("Killed session %s\n", sessionID)
		}

		// Determine workspace path
		crewPath := filepath.Join(r.Path, "crew", name)

		// Check if this is a worktree (has .git file) vs regular clone (has .git directory)
		isWorktree := false
		gitPath := filepath.Join(crewPath, ".git")
		if info, err := os.Stat(gitPath); err == nil && !info.IsDir() {
			isWorktree = true
		}

		// Remove the workspace
		if isWorktree {
			// For worktrees, use git worktree remove
			mayorRigPath := constants.RigMayorPath(r.Path)
			removeArgs := []string{"worktree", "remove", crewPath}
			if forceRemove {
				removeArgs = []string{"worktree", "remove", "--force", crewPath}
			}
			removeCmd := exec.Command("git", removeArgs...)
			removeCmd.Dir = mayorRigPath
			if output, err := removeCmd.CombinedOutput(); err != nil {
				fmt.Printf("Error removing worktree %s: %v\n%s", arg, err, string(output))
				lastErr = err
				continue
			}
			fmt.Printf("%s Removed crew worktree: %s/%s\n",
				style.Bold.Render("✓"), r.Name, name)
		} else {
			// For regular clones, use the crew manager
			if err := crewMgr.Remove(name, forceRemove); err != nil {
				if err == crew.ErrCrewNotFound {
					fmt.Printf("Error removing %s: crew workspace not found\n", arg)
				} else if err == crew.ErrHasChanges {
					fmt.Printf("Error removing %s: uncommitted changes (use --force)\n", arg)
				} else {
					fmt.Printf("Error removing %s: %v\n", arg, err)
				}
				lastErr = err
				continue
			}
			fmt.Printf("%s Removed crew workspace: %s/%s\n",
				style.Bold.Render("✓"), r.Name, name)
		}

		// Handle agent bead
		townRoot, _ := workspace.Find(r.Path)
		if townRoot == "" {
			townRoot = r.Path
		}
		prefix := beads.GetPrefixForRig(townRoot, r.Name)
		agentBeadID := beads.CrewBeadIDWithPrefix(prefix, r.Name, name)

		if crewPurge {
			// --purge: DELETE the agent bead entirely (obliterate)
			deleteArgs := []string{"delete", agentBeadID, "--force"}
			deleteCmd := exec.Command("bd", deleteArgs...)
			deleteCmd.Dir = r.Path
			if output, err := deleteCmd.CombinedOutput(); err != nil {
				// Non-fatal: bead might not exist
				if !strings.Contains(string(output), "no issue found") &&
					!strings.Contains(string(output), "not found") {
					style.PrintWarning("could not delete agent bead %s: %v", agentBeadID, err)
				}
			} else {
				fmt.Printf("Deleted agent bead: %s\n", agentBeadID)
			}

			// Unassign any beads assigned to this crew member
			agentAddr := fmt.Sprintf("%s/crew/%s", r.Name, name)
			unassignArgs := []string{"list", "--assignee=" + agentAddr, "--format=id"}
			unassignCmd := exec.Command("bd", unassignArgs...)
			unassignCmd.Dir = r.Path
			if output, err := unassignCmd.CombinedOutput(); err == nil {
				ids := strings.Fields(strings.TrimSpace(string(output)))
				for _, id := range ids {
					if id == "" {
						continue
					}
					updateCmd := exec.Command("bd", "update", id, "--unassign")
					updateCmd.Dir = r.Path
					if _, err := updateCmd.CombinedOutput(); err == nil {
						fmt.Printf("Unassigned: %s\n", id)
					}
				}
			}

			// Clear mail directory if it exists
			mailDir := filepath.Join(crewPath, "mail")
			if _, err := os.Stat(mailDir); err == nil {
				// Mail dir was removed with the workspace, so nothing to do
				// But if we want to be extra thorough, we could look in town beads
			}
		} else {
			// Default: CLOSE the agent bead (preserves CV history)
			closeArgs := []string{"close", agentBeadID, "--reason=Crew workspace removed"}
			if sessionID := runtime.SessionIDFromEnv(); sessionID != "" {
				closeArgs = append(closeArgs, "--session="+sessionID)
			}
			closeCmd := exec.Command("bd", closeArgs...)
			closeCmd.Dir = r.Path
			if output, err := closeCmd.CombinedOutput(); err != nil {
				// Non-fatal: bead might not exist or already be closed
				if !strings.Contains(string(output), "no issue found") &&
					!strings.Contains(string(output), "already closed") {
					style.PrintWarning("could not close agent bead %s: %v", agentBeadID, err)
				}
			} else {
				fmt.Printf("Closed agent bead: %s\n", agentBeadID)
			}
		}
	}

	return lastErr
}

func runCrewRefresh(cmd *cobra.Command, args []string) error {
	name := args[0]
	// Parse rig/name format (e.g., "beads/emma" -> rig=beads, name=emma)
	if rig, crewName, ok := parseRigSlashName(name); ok {
		if crewRig == "" {
			crewRig = rig
		}
		name = crewName
	}

	crewMgr, r, err := getCrewManagerForMember(crewRig, name)
	if err != nil {
		return err
	}

	// Get the crew worker (must exist for refresh)
	worker, err := crewMgr.Get(name)
	if err != nil {
		if err == crew.ErrCrewNotFound {
			return fmt.Errorf("crew workspace '%s' not found", name)
		}
		return fmt.Errorf("getting crew worker: %w", err)
	}

	// Create handoff message
	handoffMsg := crewMessage
	if handoffMsg == "" {
		handoffMsg = fmt.Sprintf("Context refresh for %s. Check mail and beads for current work state.", name)
	}

	// Send handoff mail to self
	mailDir := filepath.Join(worker.ClonePath, "mail")
	if _, err := os.Stat(mailDir); os.IsNotExist(err) {
		if err := os.MkdirAll(mailDir, 0755); err != nil {
			return fmt.Errorf("creating mail dir: %w", err)
		}
	}

	// Create and send mail
	mailbox := mail.NewMailbox(mailDir)
	msg := &mail.Message{
		From:    fmt.Sprintf("%s/%s", r.Name, name),
		To:      fmt.Sprintf("%s/%s", r.Name, name),
		Subject: "🤝 HANDOFF: Context Refresh",
		Body:    handoffMsg,
	}
	if err := mailbox.Append(msg); err != nil {
		return fmt.Errorf("sending handoff mail: %w", err)
	}
	fmt.Printf("Sent handoff mail to %s/%s\n", r.Name, name)

	// Use manager's Start() with refresh options
	err = crewMgr.Start(name, crew.StartOptions{
		KillExisting:  true,      // Kill old session if running
		Topic:         "refresh", // Startup nudge topic
		Interactive:   true,      // No --dangerously-skip-permissions
		AgentOverride: crewAgentOverride,
	})
	if err != nil {
		return fmt.Errorf("starting crew session: %w", err)
	}

	fmt.Printf("%s Refreshed crew workspace: %s/%s\n",
		style.Bold.Render("✓"), r.Name, name)
	fmt.Printf("Attach with: %s\n", style.Dim.Render(fmt.Sprintf("gt crew at %s", name)))

	return nil
}

// runCrewStart starts crew workers in a rig.
// If first arg is a valid rig name, it's used as the rig; otherwise rig is inferred from cwd.
// Remaining args (or all args if rig is inferred) are crew member names.
// Defaults to all crew members if no names specified.
func runCrewStart(cmd *cobra.Command, args []string) error {
	var rigName string
	var crewNames []string

	// --rig flag takes priority (matches crew stop behavior)
	if crewRig != "" {
		rigName = crewRig
		crewNames = args
	} else if len(args) == 0 {
		// No args - infer rig from cwd
		rigName = "" // getCrewManager will infer from cwd
	} else {
		// Check if first arg is a valid rig name
		if _, _, err := getRig(args[0]); err == nil {
			// First arg is a rig name
			rigName = args[0]
			crewNames = args[1:]
		} else {
			// First arg is not a rig - infer rig from cwd and treat all args as crew names
			rigName = "" // getCrewManager will infer from cwd
			crewNames = args
		}
	}

	// Get the rig manager and rig (infers from cwd if rigName is empty)
	crewMgr, r, err := getCrewManager(rigName)
	if err != nil {
		return err
	}
	// Update rigName in case it was inferred
	rigName = r.Name

	// If --all flag OR no crew names specified, get all crew members
	if crewAll || len(crewNames) == 0 {
		workers, err := crewMgr.List()
		if err != nil {
			return fmt.Errorf("listing crew: %w", err)
		}
		if len(workers) == 0 {
			fmt.Printf("No crew members in rig %s\n", rigName)
			return nil
		}
		for _, w := range workers {
			crewNames = append(crewNames, w.Name)
		}
	}

	// Resolve account config once for all crew members
	townRoot, _ := workspace.Find(r.Path)
	if townRoot == "" {
		townRoot = filepath.Dir(r.Path)
	}
	accountsPath := constants.MayorAccountsPath(townRoot)
	claudeConfigDir, _, _ := config.ResolveAccountConfigDir(accountsPath, crewAccount)

	// Validate: --resume with a specific session ID only makes sense for a single
	// crew member. Resuming N members with the same session ID is always a mistake.
	if crewResume != "" && crewResume != "last" && len(crewNames) > 1 {
		return fmt.Errorf("--resume with a specific session ID can only target a single crew member, got %d", len(crewNames))
	}

	// Guard against pflag NoOptDefVal parsing ambiguity: `--resume ace` (space-separated)
	// treats "ace" as the session ID, not a crew name. If the value matches a known crew
	// member, it's almost certainly a misparse. Use --resume=<id> for explicit session IDs.
	if crewResume != "" && crewResume != "last" {
		workers, listErr := crewMgr.List()
		if listErr == nil {
			for _, w := range workers {
				if w.Name == crewResume {
					return fmt.Errorf("%q looks like a crew member name, not a session ID; use --resume=%s if you meant a session ID, or use --resume (no value) to auto-resume the most recent session", crewResume, crewResume)
				}
			}
		}
	}

	// Warn when --resume last targets multiple crew members, since agents that
	// have never had a session may fail or behave unexpectedly.
	if crewResume == "last" && len(crewNames) > 1 {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: --resume will auto-resume the most recent session for all %d crew members\n", len(crewNames))
	}

	// Build start options (shared across all crew members)
	opts := crew.StartOptions{
		Account:         crewAccount,
		ClaudeConfigDir: claudeConfigDir,
		AgentOverride:   crewAgentOverride,
		ResumeSessionID: crewResume,
		KillExisting:    crewResume != "", // Resume needs to kill existing session first
	}

	// Start each crew member in parallel
	type result struct {
		name    string
		err     error
		skipped bool // true if session was already running
	}
	results := make(chan result, len(crewNames))
	var wg sync.WaitGroup

	fmt.Printf("Starting %d crew member(s) in %s...\n", len(crewNames), rigName)

	for _, name := range crewNames {
		wg.Add(1)
		go func(crewName string) {
			defer wg.Done()
			err := crewMgr.Start(crewName, opts)
			skipped := errors.Is(err, crew.ErrSessionRunning)
			if skipped {
				err = nil // Not an error, just already running
			}
			results <- result{name: crewName, err: err, skipped: skipped}
		}(name)
	}

	// Wait for all goroutines to complete
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	var lastErr error
	startedCount := 0
	skippedCount := 0
	for res := range results {
		if res.err != nil {
			fmt.Printf("  %s %s/%s: %v\n", style.ErrorPrefix, rigName, res.name, res.err)
			lastErr = res.err
		} else if res.skipped {
			fmt.Printf("  %s %s/%s: already running\n", style.Dim.Render("○"), rigName, res.name)
			skippedCount++
		} else {
			fmt.Printf("  %s %s/%s: started\n", style.SuccessPrefix, rigName, res.name)
			startedCount++

			// Ensure agent bead exists (idempotent — only creates if missing)
			prefix := beads.GetPrefixForRig(townRoot, rigName)
			crewID := beads.CrewBeadIDWithPrefix(prefix, rigName, res.name)
			bd := beads.New(beads.ResolveBeadsDir(r.Path))
			if _, err := bd.Show(crewID); err != nil {
				fields := &beads.AgentFields{
					RoleType:   "crew",
					Rig:        rigName,
					AgentState: "working",
				}
				desc := fmt.Sprintf("Crew worker %s in %s - human-managed persistent workspace.", res.name, rigName)
				if _, err := bd.CreateAgentBead(crewID, desc, fields); err != nil {
					style.PrintWarning("could not create agent bead for %s: %v", res.name, err)
				} else {
					fmt.Printf("    Agent bead created: %s\n", crewID)
				}
			}
		}
	}

	// Summary
	fmt.Println()
	if startedCount > 0 || skippedCount > 0 {
		fmt.Printf("%s Started %d, skipped %d (already running) in %s\n",
			style.Bold.Render("✓"), startedCount, skippedCount, r.Name)
	}

	return lastErr
}

func runCrewRestart(cmd *cobra.Command, args []string) error {
	// Handle --all flag
	if crewAll {
		return runCrewRestartAll()
	}

	var lastErr error

	for _, arg := range args {
		name := arg
		rigOverride := crewRig

		// Parse rig/name format (e.g., "beads/emma" -> rig=beads, name=emma)
		if rig, crewName, ok := parseRigSlashName(name); ok {
			if rigOverride == "" {
				rigOverride = rig
			}
			name = crewName
		}

		crewMgr, r, err := getCrewManagerForMember(rigOverride, name)
		if err != nil {
			fmt.Printf("Error restarting %s: %v\n", arg, err)
			lastErr = err
			continue
		}

		// Use manager's Start() with restart options
		// Start() will create workspace if needed (idempotent)
		err = crewMgr.Start(name, crew.StartOptions{
			KillExisting:  true,      // Kill old session if running
			Topic:         "restart", // Startup nudge topic
			AgentOverride: crewAgentOverride,
		})
		if err != nil {
			fmt.Printf("Error restarting %s: %v\n", arg, err)
			lastErr = err
			continue
		}

		fmt.Printf("%s Restarted crew workspace: %s/%s\n",
			style.Bold.Render("✓"), r.Name, name)
		fmt.Printf("Attach with: %s\n", style.Dim.Render(fmt.Sprintf("gt crew at %s", name)))
	}

	return lastErr
}

// runCrewRestartAll restarts all running crew sessions.
// If crewRig is set, only restarts crew in that rig.
func runCrewRestartAll() error {
	// Get all agent sessions (including polecats to find crew)
	agents, err := getAgentSessions(true)
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}

	// Filter to crew agents only
	var targets []*AgentSession
	for _, agent := range agents {
		if agent.Type != AgentCrew {
			continue
		}
		// Filter by rig if specified
		if crewRig != "" && agent.Rig != crewRig {
			continue
		}
		targets = append(targets, agent)
	}

	if len(targets) == 0 {
		fmt.Println("No running crew sessions to restart.")
		if crewRig != "" {
			fmt.Printf("  (filtered by rig: %s)\n", crewRig)
		}
		return nil
	}

	// Dry run - just show what would be restarted
	if crewDryRun {
		fmt.Printf("Would restart %d crew session(s):\n\n", len(targets))
		for _, agent := range targets {
			fmt.Printf("  %s %s/crew/%s\n", AgentTypeIcons[AgentCrew], agent.Rig, agent.AgentName)
		}
		return nil
	}

	fmt.Printf("Restarting %d crew session(s)...\n\n", len(targets))

	var succeeded, failed int
	var failures []string

	for _, agent := range targets {
		agentName := fmt.Sprintf("%s/crew/%s", agent.Rig, agent.AgentName)

		// Use crewRig temporarily to get the right crew manager
		savedRig := crewRig
		crewRig = agent.Rig

		crewMgr, _, err := getCrewManager(crewRig)
		if err != nil {
			failed++
			failures = append(failures, fmt.Sprintf("%s: %v", agentName, err))
			fmt.Printf("  %s %s\n", style.ErrorPrefix, agentName)
			crewRig = savedRig
			continue
		}

		// Use manager's Start() with restart options
		err = crewMgr.Start(agent.AgentName, crew.StartOptions{
			KillExisting:  true,      // Kill old session if running
			Topic:         "restart", // Startup nudge topic
			AgentOverride: crewAgentOverride,
		})
		if err != nil {
			failed++
			failures = append(failures, fmt.Sprintf("%s: %v", agentName, err))
			fmt.Printf("  %s %s\n", style.ErrorPrefix, agentName)
		} else {
			succeeded++
			fmt.Printf("  %s %s\n", style.SuccessPrefix, agentName)
		}

		crewRig = savedRig

		// Small delay between restarts to avoid overwhelming the system
		time.Sleep(constants.ShutdownNotifyDelay)
	}

	fmt.Println()
	if failed > 0 {
		fmt.Printf("%s Restart complete: %d succeeded, %d failed\n",
			style.WarningPrefix, succeeded, failed)
		for _, f := range failures {
			fmt.Printf("  %s\n", style.Dim.Render(f))
		}
		return fmt.Errorf("%d restart(s) failed", failed)
	}

	fmt.Printf("%s Restart complete: %d crew session(s) restarted\n", style.SuccessPrefix, succeeded)
	return nil
}

// runCrewStop stops one or more crew workers.
// Supports: "name", "rig/name" formats, "rig" (to stop all in rig), or --all.
func runCrewStop(cmd *cobra.Command, args []string) error {
	// Handle --all flag
	if crewAll {
		return runCrewStopAll()
	}

	// Handle 0 args: default to all in inferred rig
	if len(args) == 0 {
		return runCrewStopAll()
	}

	// Handle 1 arg without "/": check if it's a rig name
	// If so, stop all crew in that rig
	if len(args) == 1 && !strings.Contains(args[0], "/") {
		// Try to interpret as rig name
		if _, _, err := getRig(args[0]); err == nil {
			// It's a valid rig name - stop all crew in that rig
			crewRig = args[0]
			return runCrewStopAll()
		}
		// Not a rig name - fall through to treat as crew name
	}

	var lastErr error
	t := tmux.NewTmux()

	for _, arg := range args {
		name := arg
		rigOverride := crewRig

		// Parse rig/name format (e.g., "beads/emma" -> rig=beads, name=emma)
		if rig, crewName, ok := parseRigSlashName(name); ok {
			if rigOverride == "" {
				rigOverride = rig
			}
			name = crewName
		}

		_, r, err := getCrewManagerForMember(rigOverride, name)
		if err != nil {
			fmt.Printf("Error stopping %s: %v\n", arg, err)
			lastErr = err
			continue
		}

		sessionID := crewSessionName(r.Name, name)

		// Check if session exists
		hasSession, err := t.HasSession(sessionID)
		if err != nil {
			fmt.Printf("Error checking session %s: %v\n", sessionID, err)
			lastErr = err
			continue
		}
		if !hasSession {
			fmt.Printf("No session found for %s/%s\n", r.Name, name)
			continue
		}

		// Dry run - just show what would be stopped
		if crewDryRun {
			fmt.Printf("Would stop %s/%s (session: %s)\n", r.Name, name, sessionID)
			continue
		}

		// Capture output before stopping (best effort)
		var output string
		if !crewForce {
			output, _ = t.CapturePane(sessionID, 50)
		}

		// Kill the session (with proper process cleanup to avoid orphans)
		if err := t.KillSessionWithProcesses(sessionID); err != nil {
			fmt.Printf("  %s [%s] %s: %s\n",
				style.ErrorPrefix,
				r.Name, name,
				style.Dim.Render(err.Error()))
			lastErr = err
			continue
		}

		fmt.Printf("  %s [%s] %s: stopped\n",
			style.SuccessPrefix,
			r.Name, name)

		// Log kill event to town log
		townRoot, _ := workspace.Find(r.Path)
		if townRoot != "" {
			agent := fmt.Sprintf("%s/crew/%s", r.Name, name)
			logger := townlog.NewLogger(townRoot)
			_ = logger.Log(townlog.EventKill, agent, "gt crew stop")
		}

		// Log captured output (truncated)
		if len(output) > 200 {
			output = output[len(output)-200:]
		}
		if output != "" {
			fmt.Printf("      %s\n", style.Dim.Render("(output captured)"))
		}
	}

	return lastErr
}

// runCrewStopAll stops all running crew sessions.
// If crewRig is set, only stops crew in that rig.
func runCrewStopAll() error {
	// Get all agent sessions (including polecats to find crew)
	agents, err := getAgentSessions(true)
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}

	// Filter to crew agents only
	var targets []*AgentSession
	for _, agent := range agents {
		if agent.Type != AgentCrew {
			continue
		}
		// Filter by rig if specified
		if crewRig != "" && agent.Rig != crewRig {
			continue
		}
		targets = append(targets, agent)
	}

	if len(targets) == 0 {
		fmt.Println("No running crew sessions to stop.")
		if crewRig != "" {
			fmt.Printf("  (filtered by rig: %s)\n", crewRig)
		}
		return nil
	}

	// Dry run - just show what would be stopped
	if crewDryRun {
		fmt.Printf("Would stop %d crew session(s):\n\n", len(targets))
		for _, agent := range targets {
			fmt.Printf("  %s %s/crew/%s\n", AgentTypeIcons[AgentCrew], agent.Rig, agent.AgentName)
		}
		return nil
	}

	fmt.Printf("%s Stopping %d crew session(s)...\n\n",
		style.Bold.Render("🛑"), len(targets))

	t := tmux.NewTmux()
	var succeeded, failed int
	var failures []string

	for _, agent := range targets {
		agentName := fmt.Sprintf("%s/crew/%s", agent.Rig, agent.AgentName)
		sessionID := agent.Name // agent.Name IS the tmux session name

		// Capture output before stopping (best effort)
		var output string
		if !crewForce {
			output, _ = t.CapturePane(sessionID, 50)
		}

		// Kill the session (with proper process cleanup to avoid orphans)
		if err := t.KillSessionWithProcesses(sessionID); err != nil {
			failed++
			failures = append(failures, fmt.Sprintf("%s: %v", agentName, err))
			fmt.Printf("  %s %s\n", style.ErrorPrefix, agentName)
			continue
		}

		succeeded++
		fmt.Printf("  %s %s\n", style.SuccessPrefix, agentName)

		// Log kill event to town log
		townRoot, _ := workspace.FindFromCwd()
		if townRoot != "" {
			logger := townlog.NewLogger(townRoot)
			_ = logger.Log(townlog.EventKill, agentName, "gt crew stop --all")
		}

		// Log captured output (truncated)
		if len(output) > 200 {
			output = output[len(output)-200:]
		}
		if output != "" {
			fmt.Printf("      %s\n", style.Dim.Render("(output captured)"))
		}
	}

	fmt.Println()
	if failed > 0 {
		fmt.Printf("%s Stop complete: %d succeeded, %d failed\n",
			style.WarningPrefix, succeeded, failed)
		for _, f := range failures {
			fmt.Printf("  %s\n", style.Dim.Render(f))
		}
		return fmt.Errorf("%d stop(s) failed", failed)
	}

	fmt.Printf("%s Stop complete: %d crew session(s) stopped\n", style.SuccessPrefix, succeeded)
	return nil
}
