package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/crew"
	"github.com/steveyegge/gastown/internal/runtime"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// crewAtRetried tracks if we've already retried after stale session cleanup
var crewAtRetried bool

func runCrewAt(cmd *cobra.Command, args []string) error {
	var name string

	// Debug mode: --debug flag or GT_DEBUG env var
	debug := crewDebug || os.Getenv("GT_DEBUG") != ""
	if debug {
		cwd, _ := os.Getwd()
		fmt.Printf("[DEBUG] runCrewAt: args=%v, crewRig=%q, cwd=%q\n", args, crewRig, cwd)
	}

	// Determine crew name: from arg, or auto-detect from cwd
	if len(args) > 0 {
		name = args[0]
		// Parse rig/name format (e.g., "beads/emma" -> rig=beads, name=emma)
		if rig, crewName, ok := parseRigSlashName(name); ok {
			if crewRig == "" {
				crewRig = rig
			}
			name = crewName
		}
	} else {
		// Try to detect from current directory
		detected, err := detectCrewFromCwd()
		if err != nil {
			// Try to show available crew members if we can detect the rig
			hint := "\n\nUsage: gt crew at <name>"
			if crewRig != "" {
				if mgr, _, mgrErr := getCrewManager(crewRig); mgrErr == nil {
					if members, listErr := mgr.List(); listErr == nil && len(members) > 0 {
						hint = fmt.Sprintf("\n\nAvailable crew in %s:", crewRig)
						for _, m := range members {
							hint += fmt.Sprintf("\n  %s", m.Name)
						}
					}
				}
			}
			return fmt.Errorf("could not detect crew workspace from current directory: %w%s", err, hint)
		}
		name = detected.crewName
		if crewRig == "" {
			crewRig = detected.rigName
		}
		fmt.Printf("Detected crew workspace: %s/%s\n", detected.rigName, name)
	}

	if debug {
		fmt.Printf("[DEBUG] after detection: name=%q, crewRig=%q\n", name, crewRig)
	}

	crewMgr, r, err := getCrewManagerForMember(crewRig, name)
	if err != nil {
		return err
	}

	// Get the crew worker
	worker, err := crewMgr.Get(name)
	if err != nil {
		if err == crew.ErrCrewNotFound {
			return fmt.Errorf("crew workspace '%s' not found", name)
		}
		return fmt.Errorf("getting crew worker: %w", err)
	}

	// Reset to default branch if --reset flag is passed; otherwise warn if off-branch
	if crewReset {
		if err := ensureDefaultBranch(worker.ClonePath, fmt.Sprintf("Crew workspace %s/%s", r.Name, name), r.Path); err != nil {
			return fmt.Errorf("resetting to default branch: %w", err)
		}
	} else {
		warnIfNotDefaultBranch(worker.ClonePath, fmt.Sprintf("Crew workspace %s/%s", r.Name, name), r.Path)
	}

	// If --no-tmux, just print the path
	if crewNoTmux {
		fmt.Println(worker.ClonePath)
		return nil
	}

	// Resolve account for runtime config
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}
	accountsPath := constants.MayorAccountsPath(townRoot)
	claudeConfigDir, accountHandle, err := config.ResolveAccountConfigDir(accountsPath, crewAccount)
	if err != nil {
		return fmt.Errorf("resolving account: %w", err)
	}
	if accountHandle != "" {
		fmt.Printf("Using account: %s\n", accountHandle)
	}

	var runtimeConfig *config.RuntimeConfig
	if crewAgentOverride != "" {
		rc, _, resolveErr := config.ResolveAgentConfigWithOverride(townRoot, r.Path, crewAgentOverride)
		if resolveErr != nil {
			style.PrintWarning("could not resolve agent override %q: %v, falling back to default", crewAgentOverride, resolveErr)
			runtimeConfig = config.ResolveWorkerAgentConfig(name, townRoot, r.Path)
		} else {
			runtimeConfig = rc
		}
	} else {
		runtimeConfig = config.ResolveWorkerAgentConfig(name, townRoot, r.Path)
	}
	if runtimeConfig == nil {
		runtimeConfig = config.DefaultRuntimeConfig()
	}
	crewSettingsDir := config.RoleSettingsDir("crew", r.Path)
	if err := runtime.EnsureSettingsForRole(crewSettingsDir, worker.ClonePath, "crew", runtimeConfig); err != nil {
		// Non-fatal but log warning - missing settings can cause agents to start without hooks
		style.PrintWarning("could not ensure settings for %s: %v", name, err)
	}

	// Check if session exists
	t := tmux.NewTmux()
	sessionID := crewSessionName(r.Name, name)
	if debug {
		fmt.Printf("[DEBUG] sessionID=%q (r.Name=%q, name=%q)\n", sessionID, r.Name, name)
	}
	hasSession, err := t.HasSession(sessionID)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if debug {
		fmt.Printf("[DEBUG] hasSession=%v\n", hasSession)
	}

	// Before creating a new session, check if there's already a runtime session
	// running in this crew's directory (might have been started manually or via
	// a different mechanism)
	if !hasSession && runtimeConfig.Tmux != nil {
		existingSessions, err := t.FindSessionByWorkDir(worker.ClonePath, runtimeConfig.Tmux.ProcessNames)
		if err == nil && len(existingSessions) > 0 {
			// Found an existing session with runtime running in this directory
			existingSession := existingSessions[0]
			fmt.Printf("%s Found existing runtime session '%s' in crew directory\n",
				style.Warning.Render("⚠"),
				existingSession)
			fmt.Printf("  Attaching to existing session instead of creating a new one\n")

			// If inside tmux (but different session), inform user
			if tmux.IsInsideTmux() {
				fmt.Printf("Use C-b s to switch to '%s'\n", existingSession)
				return nil
			}

			// Outside tmux: attach unless --detached flag is set
			if crewDetached {
				fmt.Printf("Existing session: '%s'. Run 'tmux attach -t %s' to attach.\n",
					existingSession, existingSession)
				return nil
			}

			// Attach to existing session
			return attachToTmuxSession(existingSession)
		}
	}

	if !hasSession {
		// Create new session
		if err := t.NewSession(sessionID, worker.ClonePath); err != nil {
			return fmt.Errorf("creating session: %w", err)
		}

		// Set environment (non-fatal: session works without these)
		// Use centralized AgentEnv for consistency across all role startup paths
		envVars := config.AgentEnv(config.AgentEnvConfig{
			Role:             "crew",
			Rig:              r.Name,
			AgentName:        name,
			TownRoot:         townRoot,
			RuntimeConfigDir: claudeConfigDir,
			Agent:            crewAgentOverride,
			Topic:            "start",
			SessionName:      sessionID,
		})
		// Merge liveness-critical env vars (GT_AGENT, GT_PROCESS_NAMES) so that
		// IsAgentAlive can detect non-Claude runtimes. Without this, attach
		// misclassifies live sessions as dead and recreates them.
		envVars = session.MergeRuntimeLivenessEnv(envVars, runtimeConfig)
		for k, v := range envVars {
			_ = t.SetEnvironment(sessionID, k, v)
		}

		// Apply rig-based theming (non-fatal: theming failure doesn't affect operation)
		// Note: ConfigureGasTownSession includes cycle bindings
		theme := tmux.ResolveSessionTheme(townRoot, r.Name, "crew", name)
		_ = t.ConfigureGasTownSession(sessionID, theme, r.Name, name, "crew")

		// Wait for shell to be ready after session creation
		if err := t.WaitForShellReady(sessionID, constants.ShellReadyTimeout); err != nil {
			return fmt.Errorf("waiting for shell: %w", err)
		}

		// Get pane ID for respawn
		paneID, err := t.GetPaneID(sessionID)
		if err != nil {
			return fmt.Errorf("getting pane ID: %w", err)
		}

		// Build startup beacon for predecessor discovery via /resume
		// Use FormatStartupBeacon instead of bare "gt prime" which confuses agents
		// The SessionStart hook handles context injection (gt prime --hook)
		address := session.BeaconRecipient("crew", name, r.Name)
		beacon := session.FormatStartupBeacon(session.BeaconConfig{
			Recipient: address,
			Sender:    "human",
			Topic:     "start",
		})

		// Use respawn-pane to replace shell with runtime directly
		// This gives cleaner lifecycle: runtime exits → session ends (no intermediate shell)
		// Export GT_ROLE and BD_ACTOR since tmux SetEnvironment only affects new panes
		startupCmd, err := config.BuildStartupCommandFromConfig(config.AgentEnvConfig{
			Role:        "crew",
			Rig:         r.Name,
			AgentName:   name,
			TownRoot:    townRoot,
			Prompt:      beacon,
			Topic:       "start",
			SessionName: sessionID,
		}, r.Path, beacon, crewAgentOverride)
		if err != nil {
			return fmt.Errorf("building startup command: %w", err)
		}
		// Prepend config dir env if available
		if runtimeConfig.Session != nil && runtimeConfig.Session.ConfigDirEnv != "" && claudeConfigDir != "" {
			startupCmd = config.PrependEnv(startupCmd, map[string]string{runtimeConfig.Session.ConfigDirEnv: claudeConfigDir})
		}
		// Credential preflight, on the spawn path only. A dead OAuth token does
		// not make Claude Code exit — it drops into the interactive onboarding
		// flow and parks there, passing every liveness check while never
		// reaching a prompt (hq-ac0). Attaching to an already-healthy session is
		// deliberately not gated: that is how an operator re-authenticates.
		if warning, err := config.PreflightAgentAuth(runtimeConfig, claudeConfigDir); err != nil {
			return fmt.Errorf("credential preflight for %s: %w", name, err)
		} else if warning != "" {
			style.PrintWarning("%s", warning)
		}
		// Note: Don't call KillPaneProcesses here - this is a NEW session with just
		// a fresh shell. Killing it would destroy the pane before we can respawn.
		// KillPaneProcesses is only needed when restarting in an EXISTING session
		// where Claude/Node processes might be running and ignoring SIGHUP.
		if err := t.RespawnPane(paneID, startupCmd); err != nil {
			return fmt.Errorf("starting runtime: %w", err)
		}

		fmt.Printf("%s Created session for %s/%s\n",
			style.Bold.Render("✓"), r.Name, name)
	} else {
		// Session exists - check if agent is still alive
		// Uses descendant process check instead of pane command check,
		// since crew members launch via bash -c wrappers that cause
		// false-negative detection with IsAgentRunning (see #1315, #1330).
		if !t.IsAgentAlive(sessionID) {
			// Runtime has exited, restart it using respawn-pane
			fmt.Printf("Runtime exited, restarting...\n")

			// Refresh liveness env vars before restart so the next IsAgentAlive
			// check can detect non-Claude runtimes.
			restartEnv := config.AgentEnv(config.AgentEnvConfig{
				Role:             "crew",
				Rig:              r.Name,
				AgentName:        name,
				TownRoot:         townRoot,
				RuntimeConfigDir: claudeConfigDir,
				Agent:            crewAgentOverride,
				Topic:            "restart",
				SessionName:      sessionID,
			})
			restartEnv = session.MergeRuntimeLivenessEnv(restartEnv, runtimeConfig)
			for k, v := range restartEnv {
				_ = t.SetEnvironment(sessionID, k, v)
			}

			// Get pane ID for respawn
			paneID, err := t.GetPaneID(sessionID)
			if err != nil {
				return fmt.Errorf("getting pane ID: %w", err)
			}

			// Build startup beacon for predecessor discovery via /resume
			// Use FormatStartupBeacon instead of bare "gt prime" which confuses agents
			address := session.BeaconRecipient("crew", name, r.Name)
			beacon := session.FormatStartupBeacon(session.BeaconConfig{
				Recipient: address,
				Sender:    "human",
				Topic:     "restart",
			})

			// Use respawn-pane to replace shell with runtime directly
			// Export GT_ROLE and BD_ACTOR since tmux SetEnvironment only affects new panes
			startupCmd, err := config.BuildStartupCommandFromConfig(config.AgentEnvConfig{
				Role:        "crew",
				Rig:         r.Name,
				AgentName:   name,
				TownRoot:    townRoot,
				Prompt:      beacon,
				Topic:       "restart",
				SessionName: sessionID,
			}, r.Path, beacon, crewAgentOverride)
			if err != nil {
				return fmt.Errorf("building startup command: %w", err)
			}
			// Prepend config dir env if available
			if runtimeConfig.Session != nil && runtimeConfig.Session.ConfigDirEnv != "" && claudeConfigDir != "" {
				startupCmd = config.PrependEnv(startupCmd, map[string]string{runtimeConfig.Session.ConfigDirEnv: claudeConfigDir})
			}
			// Credential preflight before respawning: restarting into a dead
			// token just replaces a dead agent with a parked one (hq-ac0).
			if warning, err := config.PreflightAgentAuth(runtimeConfig, claudeConfigDir); err != nil {
				return fmt.Errorf("credential preflight for %s: %w", name, err)
			} else if warning != "" {
				style.PrintWarning("%s", warning)
			}
			// Kill all processes in the pane before respawning to prevent orphan leaks
			// RespawnPane's -k flag only sends SIGHUP which Claude/Node may ignore
			if err := t.KillPaneProcesses(paneID); err != nil {
				// Non-fatal but log the warning
				style.PrintWarning("could not kill pane processes: %v", err)
			}
			if err := t.RespawnPane(paneID, startupCmd); err != nil {
				// If pane is stale (session exists but pane doesn't), recreate the session
				if strings.Contains(err.Error(), "can't find pane") {
					if crewAtRetried {
						return fmt.Errorf("stale session persists after cleanup: %w", err)
					}
					fmt.Printf("Stale session detected, recreating...\n")
					if killErr := t.KillSession(sessionID); killErr != nil && killErr != tmux.ErrSessionNotFound {
						return fmt.Errorf("failed to kill stale session: %w", killErr)
					}
					crewAtRetried = true
					defer func() { crewAtRetried = false }()
					return runCrewAt(cmd, args) // Retry with fresh session
				}
				return fmt.Errorf("restarting runtime: %w", err)
			}
		}
	}

	// Check if we're already in the target session
	if isInTmuxSession(sessionID) {
		// Check if agent is already alive - don't restart if so
		// Uses descendant process check (see #1315, #1330).
		if t.IsAgentAlive(sessionID) {
			// Agent is already running, nothing to do
			fmt.Printf("Already in %s session with agent running.\n", name)
			return nil
		}

		// Agent not alive — resolve config to start it
		agentCfg, _, err := config.ResolveAgentConfigWithOverride(townRoot, r.Path, crewAgentOverride)
		if err != nil {
			return fmt.Errorf("resolving agent: %w", err)
		}

		// We're in the session at a shell prompt - start the agent
		// Build startup beacon for predecessor discovery via /resume
		address := session.BeaconRecipient("crew", name, r.Name)
		beacon := session.FormatStartupBeacon(session.BeaconConfig{
			Recipient: address,
			Sender:    "human",
			Topic:     "start",
		})
		fmt.Printf("Starting %s in current session...\n", agentCfg.Command)
		return execAgent(agentCfg, beacon)
	}

	// If inside tmux (but different session), don't switch - just inform user
	insideTmux := tmux.IsInsideTmux()
	if debug {
		fmt.Printf("[DEBUG] tmux.IsInsideTmux()=%v\n", insideTmux)
	}
	if insideTmux {
		fmt.Printf("Session %s ready. Use C-b s to switch.\n", sessionID)
		return nil
	}

	// Outside tmux: attach unless --detached flag is set
	if crewDetached {
		fmt.Printf("Started %s/%s. Run 'gt crew at %s' to attach.\n", r.Name, name, name)
		return nil
	}

	// Attach to session - show which session we're attaching to
	fmt.Printf("Attaching to %s...\n", sessionID)
	if debug {
		fmt.Printf("[DEBUG] calling attachToTmuxSession(%q)\n", sessionID)
	}
	return attachToTmuxSession(sessionID)
}
