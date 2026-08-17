// Package session provides Worker session start and stop.
package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/git"
	"github.com/jonbaldie/gastown/internal/nudge"
	"github.com/jonbaldie/gastown/internal/runtime"
	"github.com/jonbaldie/gastown/internal/skills"
	"github.com/jonbaldie/gastown/internal/telemetry"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/jonbaldie/gastown/internal/worker"
)

// Work is the caller-supplied identity for a Worker session.
// Start policy (waits, bypass, respawn) is not part of Work; StartSession
// reads it from the role table.
type Work struct {
	// SessionID is the tmux session name (e.g., "gt-wyvern-Toast", "hq-mayor").
	SessionID string

	// WorkDir is the working directory for the session.
	WorkDir string

	// TownRoot is the root of the Gas Town workspace (e.g., ~/gt).
	TownRoot string

	// RigPath is the rig directory path for config resolution.
	// Empty for town-level agents (mayor, deacon, boot).
	RigPath string

	// RigName is the rig name for environment variables and theming.
	// Empty for town-level agents.
	RigName string

	// AgentName is the specific agent name within a rig.
	// Used for polecats, crew, and dogs. Empty for singletons.
	AgentName string

	// Command is a pre-built startup command. If non-empty, skips command building.
	// If empty, the command is built from Beacon + config.BuildAgentStartupCommand.
	Command string

	// Beacon configures the startup beacon message for session identification.
	// Ignored if Command is non-empty.
	Beacon BeaconConfig

	// Instructions are appended after the beacon in the startup prompt.
	// Used by roles like Boot and Deacon that need explicit instructions.
	// Ignored if Command is non-empty.
	Instructions string

	// AgentOverride optionally specifies a different agent alias (e.g., "opencode").
	AgentOverride string

	// RuntimeConfigDir overrides the config directory for the runtime.
	RuntimeConfigDir string

	// ExtraEnv adds additional environment variables beyond the standard AgentEnv set.
	// These are set in the tmux session environment after the standard vars.
	ExtraEnv map[string]string

	// Theme is the tmux theme to apply. Nil means no theme is applied.
	Theme *tmux.Theme

	// Interactive marks an attended crew start. The role table then skips
	// unattended waits and dialog dismissal.
	Interactive bool

	// SkipReady treats session creation as the ready state. Used by gt now so
	// attach does not wait for a runtime prompt or the Cursor ready delay.
	SkipReady bool
}

// StartResult contains the results of session startup.
type StartResult struct {
	// RuntimeConfig is the resolved runtime config for the role.
	// Callers may need this for role-specific post-startup steps
	// (e.g., handling fallback nudges, legacy fallback).
	RuntimeConfig *config.RuntimeConfig

	// RunID is the GASTA run identifier (GT_RUN) generated for this session.
	// All telemetry events emitted within the session carry this ID, enabling
	// waterfall correlation across prompts, BD calls, mail operations, and
	// agent conversation events.
	RunID string
}

// StartSession creates a tmux session following the standard Gas Town lifecycle.
// The caller supplies the Role and the Work. Start policy is read from the
// role table inside this module.
func StartSession(t TmuxOps, role string, work Work) (_ *StartResult, retErr error) {
	runID := uuid.New().String()
	ctx := telemetry.WithRunID(context.Background(), runID)

	defer func() { telemetry.RecordSessionStart(ctx, work.SessionID, role, retErr) }()
	if work.SessionID == "" {
		return nil, fmt.Errorf("SessionID is required")
	}
	if work.WorkDir == "" {
		return nil, fmt.Errorf("WorkDir is required")
	}
	if role == "" {
		return nil, fmt.Errorf("Role is required")
	}

	policy := policyFor(role, work)

	runtimeConfig, err := resolveRuntimeConfig(role, work)
	if err != nil {
		return nil, err
	}

	settingsDir := config.RoleSettingsDir(role, work.RigPath)
	if settingsDir == "" {
		settingsDir = work.WorkDir
	}
	// SkipReady skips skill trees and slash commands (deferred by gt now) and
	// the Cursor ready delay. Hooks still install: Pi exits if gastown-hooks.js
	// is missing.
	if work.SkipReady {
		if err := runtime.EnsureHooksForRole(settingsDir, work.WorkDir, role, runtimeConfig); err != nil {
			return nil, fmt.Errorf("ensuring runtime hooks: %w", err)
		}
	} else if err := runtime.EnsureSettingsForRole(settingsDir, work.WorkDir, role, runtimeConfig); err != nil {
		return nil, fmt.Errorf("ensuring runtime settings: %w", err)
	}
	if work.RuntimeConfigDir != "" && !work.SkipReady {
		if err := skills.ProvisionUserDir(work.RuntimeConfigDir); err != nil {
			return nil, fmt.Errorf("ensuring account skills: %w", err)
		}
	}

	command := work.Command
	if command == "" {
		prompt := buildPrompt(work)
		var err error
		command, err = buildCommand(role, work, prompt)
		if err != nil {
			return nil, fmt.Errorf("building startup command: %w", err)
		}
	}

	if runtimeConfig.Session != nil && runtimeConfig.Session.ConfigDirEnv != "" && work.RuntimeConfigDir != "" {
		command = config.PrependEnv(command, map[string]string{
			runtimeConfig.Session.ConfigDirEnv: work.RuntimeConfigDir,
		})
	}

	envVars := config.AgentEnv(config.AgentEnvConfig{
		Role:             role,
		Rig:              work.RigName,
		AgentName:        work.AgentName,
		TownRoot:         work.TownRoot,
		RuntimeConfigDir: work.RuntimeConfigDir,
		Agent:            work.AgentOverride,
		SessionName:      work.SessionID,
	})
	envVars = MergeRuntimeLivenessEnv(envVars, runtimeConfig)
	envVars["GT_RUN"] = runID
	for k, v := range work.ExtraEnv {
		envVars[k] = v
	}

	if err := startSessionCommand(t, work, command, envVars); err != nil {
		return nil, fmt.Errorf("creating session: %w", err)
	}

	if policy.RemainOnExit {
		_ = t.SetRemainOnExit(work.SessionID, true)
	}

	if work.Theme != nil {
		_ = t.ConfigureGasTownSession(work.SessionID, work.Theme, work.RigName, work.AgentName, role)
	}

	if policy.WaitForAgent {
		if err := t.WaitForCommand(work.SessionID, constants.SupportedShells, constants.ClaudeStartTimeout); err != nil {
			if policy.WaitFatal {
				_ = t.KillSessionWithProcesses(work.SessionID)
				return nil, fmt.Errorf("waiting for %s to start: %w", role, err)
			}
		}
	}

	if policy.AutoRespawn {
		if err := t.SetAutoRespawnHook(work.SessionID); err != nil {
			fmt.Printf("warning: failed to set auto-respawn hook for %s: %v\n", role, err)
		}
	}

	if policy.AcceptBypass {
		_ = t.AcceptStartupDialogs(work.SessionID)
		if err := t.CheckStartupBlocked(work.SessionID); err != nil {
			_ = t.KillSessionWithProcesses(work.SessionID)
			return nil, fmt.Errorf("startup blocked: %w", err)
		}
	}

	if work.TownRoot != "" {
		if w, werr := worker.Open(work.TownRoot); werr == nil {
			agentType := runtimeConfig.ResolvedAgent
			if _, err := w.StartRun(ctx, worker.StartSpec{
				RunID:     runID,
				SessionID: work.SessionID,
				BeadID:    work.Beacon.MolID,
				Role:      role,
				Rig:       work.RigName,
				AgentName: work.AgentName,
				AgentType: agentType,
			}); err != nil && !errors.Is(err, worker.ErrLiveRun) {
				fmt.Fprintf(os.Stderr, "Warning: worker start run for %s: %v\n", work.SessionID, err)
			} else {
				_ = w.PushIdentity(ctx, worker.Identity{
					RunID:     runID,
					Role:      role,
					Rig:       work.RigName,
					AgentName: work.AgentName,
					SessionID: work.SessionID,
					Env:       envVars,
				})
				sections := []worker.ContextSection{{Type: worker.SectionRole, Content: role}}
				if work.Beacon.MolID != "" {
					sections = append(sections, worker.ContextSection{Type: worker.SectionWork, Content: work.Beacon.MolID})
				}
				if work.Instructions != "" {
					sections = append(sections, worker.ContextSection{Type: worker.SectionDirective, Content: work.Instructions})
				}
				_ = w.PushContext(ctx, worker.ContextPush{
					RunID:    runID,
					Sections: sections,
					Mode:     worker.ContextFull,
				})
			}
			if policy.ReadyDelay || policy.ReadyFatal {
				waitCtx, cancel := context.WithTimeout(ctx, constants.ClaudeStartTimeout)
				err := w.WaitReady(waitCtx, runID)
				cancel()
				if err != nil {
					if policy.ReadyFatal {
						_ = t.KillSessionWithProcesses(work.SessionID)
						return nil, fmt.Errorf("waiting for %s to become ready: %w", role, err)
					}
					fmt.Fprintf(os.Stderr, "Warning: agent readiness detection timed out for %s: %v\n", work.SessionID, err)
				}
			}
		} else if policy.ReadyDelay || policy.ReadyFatal {
			if err := t.WaitForRuntimeReady(work.SessionID, runtimeConfig, constants.ClaudeStartTimeout); err != nil {
				if policy.ReadyFatal {
					_ = t.KillSessionWithProcesses(work.SessionID)
					return nil, fmt.Errorf("waiting for %s to become ready: %w", role, err)
				}
				fmt.Fprintf(os.Stderr, "Warning: agent readiness detection timed out for %s: %v\n", work.SessionID, err)
			}
		}
	} else if policy.ReadyDelay || policy.ReadyFatal {
		if err := t.WaitForRuntimeReady(work.SessionID, runtimeConfig, constants.ClaudeStartTimeout); err != nil {
			if policy.ReadyFatal {
				_ = t.KillSessionWithProcesses(work.SessionID)
				return nil, fmt.Errorf("waiting for %s to become ready: %w", role, err)
			}
			fmt.Fprintf(os.Stderr, "Warning: agent readiness detection timed out for %s: %v\n", work.SessionID, err)
		}
	}

	if policy.VerifySurvived {
		running, err := t.HasSession(work.SessionID)
		if err != nil {
			_ = t.KillSessionWithProcesses(work.SessionID)
			return nil, fmt.Errorf("verifying session: %w", err)
		}
		if !running {
			return nil, fmt.Errorf("session %s died during startup (agent command may have failed)", work.SessionID)
		}
		if err := t.CheckStartupBlocked(work.SessionID); err != nil {
			_ = t.KillSessionWithProcesses(work.SessionID)
			return nil, fmt.Errorf("startup blocked: %w", err)
		}
		if status := t.CheckSessionHealth(work.SessionID, 0); status != tmux.SessionHealthy {
			_ = t.KillSessionWithProcesses(work.SessionID)
			return nil, fmt.Errorf("session %s unhealthy during startup: %s", work.SessionID, status)
		}
	}

	if paneID, err := t.GetPaneID(work.SessionID); err == nil {
		_ = t.SetEnvironment(work.SessionID, "GT_PANE_ID", paneID)
	}

	if policy.TrackPID && work.TownRoot != "" {
		_ = TrackSessionPID(work.TownRoot, work.SessionID, t)
	}

	if os.Getenv("GT_LOG_AGENT_OUTPUT") == "true" && os.Getenv("GT_OTEL_LOGS_URL") != "" {
		if err := ActivateAgentLogging(work.SessionID, work.WorkDir, runID); err != nil {
			fmt.Fprintf(os.Stderr, "warning: agent log watcher setup failed for %s: %v\n", work.SessionID, err)
		}
	}

	RecordAgentInstantiateFromDir(ctx, runID, runtimeConfig.ResolvedAgent,
		role, work.AgentName, work.SessionID, work.RigName, work.TownRoot, work.Beacon.MolID, work.WorkDir)

	return &StartResult{RuntimeConfig: runtimeConfig, RunID: runID}, nil
}

type sessionNoWait interface {
	NewSessionWithCommandAndEnvNoWait(name, workDir, command string, env map[string]string) error
}

func startSessionCommand(tm TmuxOps, work Work, command string, envVars map[string]string) error {
	if work.SkipReady {
		if n, ok := tm.(sessionNoWait); ok {
			return n.NewSessionWithCommandAndEnvNoWait(work.SessionID, work.WorkDir, command, envVars)
		}
	}
	return tm.NewSessionWithCommandAndEnv(work.SessionID, work.WorkDir, command, envVars)
}

// RecordAgentInstantiateFromDir resolves the git branch/commit from workDir and
// emits the agent.instantiate root telemetry event. resolvedAgent defaults to
// "claudecode" when empty. Use this instead of calling telemetry.RecordAgentInstantiate
// directly to avoid duplicating the agentType/git-lookup boilerplate.
func RecordAgentInstantiateFromDir(ctx context.Context, runID, resolvedAgent, role, agentName, sessionID, rigName, townRoot, issueID, workDir string) {
	agentType := resolvedAgent
	if agentType == "" {
		agentType = "claudecode"
	}
	branch, commit := "", ""
	if g := git.NewGit(workDir); g != nil {
		if b, err := g.CurrentBranch(); err == nil {
			branch = b
		}
		if c, err := g.Rev("HEAD"); err == nil {
			commit = c
		}
	}
	telemetry.RecordAgentInstantiate(ctx, telemetry.AgentInstantiateInfo{
		RunID:     runID,
		AgentType: agentType,
		Role:      role,
		AgentName: agentName,
		SessionID: sessionID,
		RigName:   rigName,
		TownRoot:  townRoot,
		IssueID:   issueID,
		GitBranch: branch,
		GitCommit: commit,
	})
}

// ErrNotFound is returned by StopSession when the tmux session is already gone.
var ErrNotFound = errors.New("session not found")

// StopSession is the single Worker stop path. It stops the nudge poller,
// marks the Worker run stopped, stops the agent-log watcher, and kills the
// tmux session. Role managers call this instead of writing their own stop body.
func StopSession(t TmuxOps, townRoot, sessionID string, graceful bool) error {
	if townRoot != "" {
		if pollerErr := nudge.StopPoller(townRoot, sessionID); pollerErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not stop nudge poller for %s: %v\n", sessionID, pollerErr)
		}
	}

	running, err := t.HasSession(sessionID)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}

	var stopErr error
	if running {
		if graceful {
			_ = t.SendKeysRaw(sessionID, "C-c")
			WaitForSessionExit(t, sessionID, constants.GracefulShutdownTimeout)
		}

		DeactivateAgentLogging(sessionID)

		if err := t.KillSessionWithProcesses(sessionID); err != nil {
			stopErr = fmt.Errorf("killing session: %w", err)
		}
	}

	if townRoot != "" {
		if err := worker.MarkSessionStopped(townRoot, sessionID); err != nil && stopErr == nil {
			stopErr = fmt.Errorf("recording stopped worker run: %w", err)
		}
	}

	if stopErr != nil {
		return stopErr
	}
	if !running {
		return fmt.Errorf("%w: %s", ErrNotFound, sessionID)
	}
	return nil
}

func mapKeysSorted(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// MergeRuntimeLivenessEnv ensures liveness-critical env vars are present in the
// tmux session environment table, even when agent resolution came from
// workspace/default settings rather than an explicit --agent override.
//
// Call this after config.AgentEnv() to add GT_AGENT and GT_PROCESS_NAMES
// before writing env vars to the tmux session via SetEnvironment.
func MergeRuntimeLivenessEnv(envVars map[string]string, runtimeConfig *config.RuntimeConfig) map[string]string {
	if envVars == nil {
		envVars = make(map[string]string)
	}
	if runtimeConfig == nil {
		return envVars
	}

	if _, hasGTAgent := envVars["GT_AGENT"]; !hasGTAgent && runtimeConfig.ResolvedAgent != "" {
		envVars["GT_AGENT"] = runtimeConfig.ResolvedAgent
	}

	if _, hasProcessNames := envVars["GT_PROCESS_NAMES"]; !hasProcessNames {
		agentForLookup := runtimeConfig.ResolvedAgent
		commandForLookup := runtimeConfig.Command
		argsForLookup := runtimeConfig.Args
		if existing, ok := envVars["GT_AGENT"]; ok && existing != "" {
			agentForLookup = existing
			// When GT_AGENT was set by AgentOverride (differs from the
			// workspace-resolved agent), the runtimeConfig.Command/Args
			// belong to the workspace agent, not the override. Pass empty
			// command so ResolveProcessNames uses the preset's own command.
			if existing != runtimeConfig.ResolvedAgent {
				commandForLookup = ""
				argsForLookup = nil
			}
		}
		processNames := config.ResolveProcessNames(agentForLookup, commandForLookup, argsForLookup...)
		if len(processNames) > 0 {
			envVars["GT_PROCESS_NAMES"] = strings.Join(processNames, ",")
		}
	}

	return envVars
}

// ErrSessionAlive is returned by KillExistingSession when checkAlive is true
// and the tmux session still has a live agent.
var ErrSessionAlive = errors.New("session already running")

// KillExistingSession kills an existing session if one is found.
// Returns true if a session was killed.
//
// If checkAlive is true, only kills zombie sessions (tmux alive but agent dead).
// If the session exists and the agent is alive, returns ErrSessionAlive.
// If checkAlive is false, kills any existing session unconditionally.
// The kill goes through StopSession so the nudge poller, worker run, and
// log watcher are torn down before the next start.
func KillExistingSession(t TmuxOps, townRoot, sessionID string, checkAlive bool) (bool, error) {
	running, err := t.HasSession(sessionID)
	if err != nil {
		return false, fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return false, nil
	}

	if checkAlive && t.IsAgentAlive(sessionID) {
		return false, fmt.Errorf("%w: %s", ErrSessionAlive, sessionID)
	}

	if err := StopSession(t, townRoot, sessionID, false); err != nil && !errors.Is(err, ErrNotFound) {
		return false, err
	}
	return true, nil
}

// resolveRuntimeConfig chooses the agent config for this session.
// An explicit AgentOverride wins. Crew members without an override use
// per-worker resolution. Every other role uses the role default.
func resolveRuntimeConfig(role string, work Work) (*config.RuntimeConfig, error) {
	if work.AgentOverride != "" {
		rc, _, err := config.ResolveAgentConfigWithOverride(work.TownRoot, work.RigPath, work.AgentOverride)
		if err != nil {
			return nil, fmt.Errorf("resolving agent config for %s: %w", work.AgentOverride, err)
		}
		return rc, nil
	}
	if role == constants.RoleCrew && work.AgentName != "" {
		return config.ResolveWorkerAgentConfig(work.AgentName, work.TownRoot, work.RigPath), nil
	}
	return config.ResolveRoleAgentConfig(role, work.TownRoot, work.RigPath), nil
}

// buildPrompt creates the startup prompt from beacon + instructions.
func buildPrompt(work Work) string {
	if work.Instructions != "" {
		return BuildStartupPrompt(work.Beacon, work.Instructions)
	}
	return FormatStartupBeacon(work.Beacon)
}

// buildCommand creates the startup command using the config package.
func buildCommand(role string, work Work, prompt string) (string, error) {
	return config.BuildStartupCommandFromConfig(config.AgentEnvConfig{
		Role:             role,
		Rig:              work.RigName,
		AgentName:        work.AgentName,
		TownRoot:         work.TownRoot,
		RuntimeConfigDir: work.RuntimeConfigDir,
		Agent:            work.AgentOverride,
		Prompt:           prompt,
		Issue:            work.Beacon.MolID,
		Topic:            work.Beacon.Topic,
		SessionName:      work.SessionID,
	}, work.RigPath, prompt, work.AgentOverride)
}

// ShutdownDelay is the standard delay after session creation.
// Some roles use this instead of the runtime's ready delay.
func ShutdownDelay() time.Duration {
	return constants.ShutdownNotifyDelay
}
