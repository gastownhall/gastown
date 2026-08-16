package session

import (
	"time"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/tmux"
)

// TmuxOps is the tmux seam used by StartSession and StopSession.
// Production code passes *tmux.Tmux. Tests pass a stand-in.
type TmuxOps interface {
	HasSession(name string) (bool, error)
	IsAgentAlive(session string) bool
	KillSessionWithProcesses(name string) error
	NewSessionWithCommandAndEnv(name, workDir, command string, env map[string]string) error
	SetRemainOnExit(pane string, on bool) error
	SetEnvironment(session, key, value string) error
	GetPaneID(session string) (string, error)
	GetPanePID(session string) (string, error)
	ConfigureGasTownSession(session string, theme *tmux.Theme, rig, worker, role string) error
	WaitForCommand(session string, excludeCommands []string, timeout time.Duration) error
	SetAutoRespawnHook(session string) error
	AcceptStartupDialogs(session string) error
	CheckStartupBlocked(session string) error
	WaitForRuntimeReady(session string, rc *config.RuntimeConfig, timeout time.Duration) error
	CheckSessionHealth(session string, stale time.Duration) tmux.ZombieStatus
	SendKeysRaw(session, keys string) error
}
