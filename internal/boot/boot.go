// Package boot manages the Boot watchdog - the daemon's entry point for Deacon triage.
// Boot is a dog that runs fresh on each daemon tick, deciding whether to wake/nudge/interrupt
// the Deacon or let it continue. This centralizes the "when to wake" decision in an agent.
package boot

import (
	"github.com/steveyegge/gastown/internal/cli"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/runtime"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/util"
)

// MarkerFileName is the lock file for Boot startup coordination.
const MarkerFileName = ".boot-running"

// StatusFileName stores Boot's last execution status.
const StatusFileName = ".boot-status.json"

// Status represents Boot's execution status.
type Status struct {
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
	LastAction  string    `json:"last_action,omitempty"` // start/wake/nudge/nothing
	Target      string    `json:"target,omitempty"`      // deacon, witness, etc.
	Error       string    `json:"error,omitempty"`
}

// Boot manages the Boot watchdog lifecycle.
type Boot struct {
	townRoot   string
	bootDir    string // ~/gt/deacon/dogs/boot/
	deaconDir  string // ~/gt/deacon/
	tmux       *tmux.Tmux
	degraded   bool
	lockHandle *flock.Flock // held during triage execution
}

// New creates a new Boot manager.
func New(townRoot string) *Boot {
	return &Boot{
		townRoot:  townRoot,
		bootDir:   filepath.Join(townRoot, "deacon", "dogs", "boot"),
		deaconDir: filepath.Join(townRoot, "deacon"),
		tmux:      tmux.NewTmux(),
		degraded:  os.Getenv("GT_DEGRADED") == "true",
	}
}

// EnsureDir ensures the Boot directory exists.
func (b *Boot) EnsureDir() error {
	return os.MkdirAll(b.bootDir, 0755)
}

// markerPath returns the path to the marker file.
func (b *Boot) markerPath() string {
	return filepath.Join(b.bootDir, MarkerFileName)
}

// statusPath returns the path to the status file.
func (b *Boot) statusPath() string {
	return filepath.Join(b.bootDir, StatusFileName)
}

// IsRunning checks if Boot is currently running.
// Queries tmux directly for observable reality (ZFC principle).
func (b *Boot) IsRunning() bool {
	return b.IsSessionAlive()
}

// IsSessionAlive checks if the Boot tmux session exists.
func (b *Boot) IsSessionAlive() bool {
	has, err := b.tmux.HasSession(session.BootSessionName())
	return err == nil && has
}

// AcquireLock acquires an exclusive flock on the marker file.
// Returns error if another triage is already running.
// Uses flock instead of session existence check because triage runs inside
// the Boot session - checking session existence would always fail.
func (b *Boot) AcquireLock() error {
	if err := b.EnsureDir(); err != nil {
		return fmt.Errorf("ensuring boot dir: %w", err)
	}

	// Use flock for actual mutual exclusion
	b.lockHandle = flock.New(b.markerPath())
	locked, err := b.lockHandle.TryLock()
	if err != nil {
		return fmt.Errorf("acquiring lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("boot triage is already running (lock held)")
	}

	return nil
}

// ReleaseLock releases the flock and removes the marker file.
func (b *Boot) ReleaseLock() error {
	if b.lockHandle != nil {
		if err := b.lockHandle.Unlock(); err != nil {
			return fmt.Errorf("releasing lock: %w", err)
		}
		b.lockHandle = nil
	}
	// Remove marker file (ignore error if already gone)
	_ = os.Remove(b.markerPath())
	return nil
}

// SaveStatus saves Boot's execution status.
func (b *Boot) SaveStatus(status *Status) error {
	if err := b.EnsureDir(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(b.statusPath(), data, 0644) //nolint:gosec // G306: boot status is non-sensitive operational data
}

// LoadStatus loads Boot's last execution status.
func (b *Boot) LoadStatus() (*Status, error) {
	data, err := os.ReadFile(b.statusPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &Status{}, nil
		}
		return nil, err
	}

	var status Status
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, err
	}

	return &status, nil
}

// Spawn starts Boot in a fresh tmux session.
// Boot runs the mol-boot-triage molecule and exits when done.
// In degraded mode (no tmux), it runs in a subprocess.
// The agentOverride parameter allows specifying an agent alias to use instead of the town default.
// Boot is ephemeral - each spawn kills any existing session and starts fresh.
func (b *Boot) Spawn(agentOverride string) error {
	// No IsRunning() guard here - Boot is ephemeral by design.
	// spawnTmux() kills any existing session before spawning fresh.

	// Check for degraded mode
	if b.degraded {
		return b.spawnDegraded()
	}

	return b.spawnTmux(agentOverride)
}

// spawnTmux spawns Boot in a tmux session.
func (b *Boot) spawnTmux(agentOverride string) error {
	// Kill any stale session first (Boot is ephemeral).
	if b.IsSessionAlive() {
		_ = b.tmux.KillSessionWithProcesses(session.BootSessionName())
	}

	// Ensure boot directory exists (it should have CLAUDE.md with Boot context)
	if err := b.EnsureDir(); err != nil {
		return fmt.Errorf("ensuring boot dir: %w", err)
	}

	// Use unified session lifecycle for config → settings → command → create → env.
	bootSessionID := session.BootSessionName()
	result, err := session.StartSession(b.tmux, session.SessionConfig{
		SessionID: bootSessionID,
		WorkDir:   b.bootDir,
		Role:      "boot",
		TownRoot:  b.townRoot,
		Beacon: session.BeaconConfig{
			Recipient: "boot",
			Sender:    "daemon",
			Topic:     "triage",
		},
		Instructions:  "Run `" + cli.Name() + " boot triage` now.",
		AgentOverride: agentOverride,
	})
	if err != nil {
		return err
	}

	// Startup fallback for promptless runtimes (prompt_mode: none).
	// BuildCommandWithPrompt drops the prompt text for these runtimes, so boot
	// never receives its triage instruction via the CLI argument.
	_ = runtime.RunStartupFallback(b.tmux, bootSessionID, "boot", result.RuntimeConfig)
	bootPrompt := session.BuildStartupPrompt(session.BeaconConfig{
		Recipient: "boot",
		Sender:    "daemon",
		Topic:     "triage",
	}, "Run `"+cli.Name()+" boot triage` now.")
	_ = runtime.DeliverStartupPromptFallback(b.tmux, bootSessionID, bootPrompt, result.RuntimeConfig, constants.ClaudeStartTimeout)

	return nil
}

// spawnDegraded spawns Boot in degraded mode (no tmux).
// Boot runs to completion and exits without handoff.
func (b *Boot) spawnDegraded() error {
	// In degraded mode, we run gt boot triage directly
	// This performs the triage logic without a full Claude session
	cmd := exec.Command("gt", "boot", "triage", "--degraded")
	cmd.Dir = b.deaconDir
	util.SetDetachedProcessGroup(cmd)

	// Use centralized AgentEnv for consistency with tmux mode
	envVars := config.AgentEnv(config.AgentEnvConfig{
		Role:     "boot",
		TownRoot: b.townRoot,
	})
	cmd.Env = config.EnvForExecCommand(envVars)
	cmd.Env = append(cmd.Env, "GT_DEGRADED=true")

	// Run async - don't wait for completion
	return cmd.Start()
}

// IsDegraded returns whether Boot is in degraded mode.
func (b *Boot) IsDegraded() bool {
	return b.degraded
}

// Dir returns Boot's working directory.
func (b *Boot) Dir() string {
	return b.bootDir
}

// DeaconDir returns the Deacon's directory.
func (b *Boot) DeaconDir() string {
	return b.deaconDir
}

// Tmux returns the tmux manager.
func (b *Boot) Tmux() *tmux.Tmux {
	return b.tmux
}
