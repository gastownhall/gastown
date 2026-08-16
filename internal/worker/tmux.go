package worker

import (
	"time"

	"github.com/steveyegge/gastown/internal/tmux"
)

// TmuxAdapter wraps the existing tmux session so the fallback path stays
// inside Worker. Inference of ready/idle stays in this adapter.
type TmuxAdapter struct {
	T *tmux.Tmux
}

func NewTmuxAdapter() *TmuxAdapter {
	return &TmuxAdapter{T: tmux.NewTmux()}
}

func (a *TmuxAdapter) HasSession(session string) (bool, error) {
	return a.T.HasSession(session)
}

func (a *TmuxAdapter) NudgeSession(session, message string) error {
	return a.T.NudgeSession(session, message)
}

func (a *TmuxAdapter) WaitForRuntimeReady(session string, timeout time.Duration) error {
	return a.T.WaitForRuntimeReady(session, nil, timeout)
}

func (a *TmuxAdapter) WaitForIdle(session string, timeout time.Duration) error {
	return a.T.WaitForIdle(session, timeout)
}

func (a *TmuxAdapter) IsAgentAlive(session string) bool {
	return a.T.IsAgentAlive(session)
}

func (a *TmuxAdapter) KillSessionWithProcesses(session string) error {
	return a.T.KillSessionWithProcesses(session)
}

func (a *TmuxAdapter) CheckSessionHealth(session string, stale time.Duration) string {
	return a.T.CheckSessionHealth(session, stale).String()
}
