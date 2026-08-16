// Package dog provides dog session management for Deacon's helper workers.
package dog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/cli"
	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/session"
	"github.com/jonbaldie/gastown/internal/tmux"
)

// Session errors
var (
	ErrSessionRunning  = errors.New("session already running")
	ErrSessionNotFound = errors.New("session not found")
)

// SessionManager handles dog session lifecycle.
type SessionManager struct {
	tmux     *tmux.Tmux
	mgr      *Manager
	townRoot string
}

// NewSessionManager creates a new dog session manager.
// The Manager parameter is used to sync persistent dog state (idle/working)
// when sessions start and stop.
func NewSessionManager(t *tmux.Tmux, townRoot string, mgr *Manager) *SessionManager {
	return &SessionManager{
		tmux:     t,
		mgr:      mgr,
		townRoot: townRoot,
	}
}

// SessionStartOptions configures dog session startup.
type SessionStartOptions struct {
	// WorkDesc is the work description (formula or bead ID) for the startup prompt.
	WorkDesc string

	// AgentOverride specifies an alternate agent (e.g., "gemini", "claude-haiku").
	AgentOverride string
}

// SessionInfo contains information about a running dog session.
type SessionInfo struct {
	// DogName is the dog name.
	DogName string `json:"dog_name"`

	// SessionID is the tmux session identifier.
	SessionID string `json:"session_id"`

	// Running indicates if the session is currently active.
	Running bool `json:"running"`

	// Attached indicates if someone is attached to the session.
	Attached bool `json:"attached,omitempty"`

	// Created is when the session was created.
	Created time.Time `json:"created,omitempty"`
}

// SessionName generates the tmux session name for a dog.
// Pattern: hq-dog-{name}
// Dogs are town-level (managed by deacon), so they use the hq- prefix.
// We use "hq-dog-" instead of "hq-deacon-" to avoid tmux prefix-matching
// collisions with the "hq-deacon" session.
func (m *SessionManager) SessionName(dogName string) string {
	return fmt.Sprintf("hq-dog-%s", dogName)
}

// kennelPath returns the path to the dog's kennel directory.
func (m *SessionManager) kennelPath(dogName string) string {
	return filepath.Join(m.townRoot, "deacon", "dogs", dogName)
}

// Start creates and starts a new session for a dog.
// Dogs run agent sessions that check mail for work and execute formulas.
func (m *SessionManager) Start(dogName string, opts SessionStartOptions) error {
	kennelDir := m.kennelPath(dogName)
	if _, err := os.Stat(kennelDir); os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrDogNotFound, dogName)
	}

	sessionID := m.SessionName(dogName)

	// Kill any existing zombie session (tmux alive but agent dead).
	_, err := session.KillExistingSession(m.tmux, sessionID, true)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrSessionRunning, sessionID)
	}

	// Build instructions for the dog.
	// For plugin work, explicitly direct the dog to read mail for the full
	// plugin instructions rather than trying to locate the plugin locally.
	// This prevents dogs from scanning their worktree's plugins/ directory
	// and escalating "plugin not found" when the plugin is town-level.
	workInfo := ""
	if opts.WorkDesc != "" {
		if strings.HasPrefix(opts.WorkDesc, "plugin:") {
			pluginName := strings.TrimPrefix(opts.WorkDesc, "plugin:")
			workInfo = fmt.Sprintf(" Plugin %s dispatched — full instructions are in your mail. Do NOT look for the plugin locally; read mail instead.", pluginName)
		} else {
			workInfo = fmt.Sprintf(" Work assigned: %s.", opts.WorkDesc)
		}
	}
	instructions := fmt.Sprintf("I am Dog %s.%s IMPORTANT: If your hook is empty and you have no mail, WAIT — the dispatcher is still setting up your assignment. Do NOT search for work, scan directories, or take autonomous action. Check hook (`"+cli.Name()+" hook`) and mail (`"+cli.Name()+" mail inbox`). If neither has work, wait 10 seconds and re-check. Execute only assigned work. When done, run `"+cli.Name()+" dog done` — this clears your work and auto-terminates the session.", dogName, workInfo)

	// Use unified session lifecycle.
	theme := tmux.DogTheme()
	_, err = session.StartSession(m.tmux, session.SessionConfig{
		SessionID: sessionID,
		WorkDir:   kennelDir,
		Role:      "dog",
		TownRoot:  m.townRoot,
		AgentName: dogName,
		Beacon: session.BeaconConfig{
			Recipient: session.BeaconRecipient("dog", dogName, ""),
			Sender:    "deacon",
			Topic:     "assigned",
		},
		Instructions:   instructions,
		AgentOverride:  opts.AgentOverride,
		Theme:          &theme,
		WaitForAgent:   true,
		WaitFatal:      true,
		AcceptBypass:   true,
		ReadyDelay:     true,
		VerifySurvived: true,
		TrackPID:       true,
	})
	if err != nil {
		return err
	}

	// Update persistent state to working
	if m.mgr != nil {
		if err := m.mgr.SetState(dogName, StateWorking); err != nil {
			// Log but don't fail - session is running, state sync is best-effort
			fmt.Fprintf(os.Stderr, "warning: failed to set dog %s state to working: %v\n", dogName, err)
		}
	}

	return nil
}

// Stop terminates a dog session.
func (m *SessionManager) Stop(dogName string, force bool) error {
	sessionID := m.SessionName(dogName)

	running, err := m.tmux.HasSession(sessionID)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return ErrSessionNotFound
	}

	// Try graceful shutdown first
	if !force {
		_ = m.tmux.SendKeysRaw(sessionID, "C-c")
		session.WaitForSessionExit(m.tmux, sessionID, constants.GracefulShutdownTimeout)
	}

	if err := m.tmux.KillSessionWithProcesses(sessionID); err != nil {
		return fmt.Errorf("killing session: %w", err)
	}

	// Update persistent state to idle so dog is available for reassignment
	if m.mgr != nil {
		if err := m.mgr.SetState(dogName, StateIdle); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to set dog %s state to idle: %v\n", dogName, err)
		}
	}

	return nil
}

// IsRunning checks if a dog session is active.
func (m *SessionManager) IsRunning(dogName string) (bool, error) {
	sessionID := m.SessionName(dogName)
	return m.tmux.HasSession(sessionID)
}

// Status returns detailed status for a dog session.
func (m *SessionManager) Status(dogName string) (*SessionInfo, error) {
	sessionID := m.SessionName(dogName)

	running, err := m.tmux.HasSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("checking session: %w", err)
	}

	info := &SessionInfo{
		DogName:   dogName,
		SessionID: sessionID,
		Running:   running,
	}

	if !running {
		return info, nil
	}

	tmuxInfo, err := m.tmux.GetSessionInfo(sessionID)
	if err != nil {
		return info, nil
	}

	info.Attached = tmuxInfo.Attached

	return info, nil
}

// GetPane returns the pane ID for a dog session.
func (m *SessionManager) GetPane(dogName string) (string, error) {
	sessionID := m.SessionName(dogName)

	running, err := m.tmux.HasSession(sessionID)
	if err != nil {
		return "", fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return "", ErrSessionNotFound
	}

	// Get pane ID from session
	pane, err := m.tmux.GetPaneID(sessionID)
	if err != nil {
		return "", fmt.Errorf("getting pane: %w", err)
	}

	return pane, nil
}

// EnsureRunning ensures a dog session is running, starting it if needed.
// Returns the pane ID.
func (m *SessionManager) EnsureRunning(dogName string, opts SessionStartOptions) (string, error) {
	running, err := m.IsRunning(dogName)
	if err != nil {
		return "", err
	}

	if !running {
		if err := m.Start(dogName, opts); err != nil {
			return "", err
		}
	}

	return m.GetPane(dogName)
}
