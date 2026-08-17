package deacon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/nudge"
	"github.com/jonbaldie/gastown/internal/session"
	"github.com/jonbaldie/gastown/internal/tmux"
)

// Common errors
var (
	ErrNotRunning     = errors.New("deacon not running")
	ErrAlreadyRunning = errors.New("deacon already running")
)

// tmuxOps abstracts tmux operations for testing.
type tmuxOps interface {
	session.TmuxOps
	GetSessionInfo(name string) (*tmux.SessionInfo, error)
}

// Manager handles deacon lifecycle operations.
type Manager struct {
	townRoot    string
	tmux        tmuxOps
	startPoller func(townRoot, session string) (int, error)
	stopPoller  func(townRoot, session string) error
}

// NewManager creates a new deacon manager for a town.
func NewManager(townRoot string) *Manager {
	return &Manager{
		townRoot:    townRoot,
		tmux:        tmux.NewTmux(),
		startPoller: nudge.StartPoller,
		stopPoller:  nudge.StopPoller,
	}
}

// SessionName returns the tmux session name for the deacon.
// This is a package-level function for convenience.
func SessionName() string {
	return session.DeaconSessionName()
}

// SessionName returns the tmux session name for the deacon.
func (m *Manager) SessionName() string {
	return SessionName()
}

// deaconDir returns the working directory for the deacon.
func (m *Manager) deaconDir() string {
	return filepath.Join(m.townRoot, "deacon")
}

func (m *Manager) startNudgePoller(sessionID string) {
	if m.startPoller == nil {
		return
	}
	if _, pollerErr := m.startPoller(m.townRoot, sessionID); pollerErr != nil {
		fmt.Printf("warning: could not start nudge poller for %s: %v\n", sessionID, pollerErr)
	}
}

func (m *Manager) stopNudgePoller(sessionID string) {
	if m.stopPoller == nil {
		return
	}
	if pollerErr := m.stopPoller(m.townRoot, sessionID); pollerErr != nil {
		fmt.Printf("warning: could not stop nudge poller for %s: %v\n", sessionID, pollerErr)
	}
}

// Start starts the deacon session.
// agentOverride allows specifying an alternate agent alias (e.g., for testing).
// Restarts are handled by daemon via ensureDeaconRunning on each heartbeat.
func (m *Manager) Start(agentOverride string) error {
	return m.start(agentOverride, false)
}

// StartImmediate starts the Deacon session without waiting for the runtime prompt.
func (m *Manager) StartImmediate(agentOverride string) error {
	return m.start(agentOverride, true)
}

func (m *Manager) start(agentOverride string, skipReady bool) error {
	t := m.tmux
	sessionID := m.SessionName()

	running, err := t.HasSession(sessionID)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if running {
		if t.IsAgentAlive(sessionID) {
			m.startNudgePoller(sessionID)
			return ErrAlreadyRunning
		}

		if err := session.StopSession(t, m.townRoot, sessionID, false); err != nil && !errors.Is(err, session.ErrNotFound) {
			return fmt.Errorf("killing zombie session: %w", err)
		}
	}

	deaconDir := m.deaconDir()
	if err := os.MkdirAll(deaconDir, 0755); err != nil {
		return fmt.Errorf("creating deacon directory: %w", err)
	}

	theme := tmux.ResolveSessionTheme(m.townRoot, "", "deacon", "")
	if _, err := session.StartSession(t, "deacon", session.Work{
		SessionID: sessionID,
		WorkDir:   deaconDir,
		TownRoot:  m.townRoot,
		Beacon: session.BeaconConfig{
			Recipient: "deacon",
			Sender:    "daemon",
			Topic:     "patrol",
		},
		Instructions:  "I am Deacon. Start patrol: run gt deacon heartbeat, then check gt hook. If no hook, run gt sling mol-deacon-patrol deacon, then execute the hook it creates.",
		AgentOverride: agentOverride,
		Theme:         theme,
		AgentName:     "Deacon",
		SkipReady:     skipReady,
	}); err != nil {
		return err
	}

	m.startNudgePoller(sessionID)
	if !skipReady {
		time.Sleep(constants.ShutdownNotifyDelay)
	}
	return nil
}

// Stop stops the deacon session.
func (m *Manager) Stop() error {
	err := session.StopSession(m.tmux, m.townRoot, m.SessionName(), true)
	if errors.Is(err, session.ErrNotFound) {
		return ErrNotRunning
	}
	return err
}

// IsRunning checks if the deacon session is active.
func (m *Manager) IsRunning() (bool, error) {
	return m.tmux.HasSession(m.SessionName())
}

// Status returns information about the deacon session.
func (m *Manager) Status() (*tmux.SessionInfo, error) {
	t := m.tmux
	sessionID := m.SessionName()

	running, err := t.HasSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return nil, ErrNotRunning
	}

	return t.GetSessionInfo(sessionID)
}
