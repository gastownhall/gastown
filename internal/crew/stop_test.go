package crew

import (
	"errors"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/rig"
	"github.com/jonbaldie/gastown/internal/tmux"
)

type mockTmux struct {
	hasSession bool
	killCalls  []string
}

func (m *mockTmux) HasSession(string) (bool, error) { return m.hasSession, nil }
func (m *mockTmux) IsAgentAlive(string) bool        { return true }
func (m *mockTmux) KillSessionWithProcesses(name string) error {
	m.killCalls = append(m.killCalls, name)
	return nil
}
func (m *mockTmux) NewSessionWithCommandAndEnv(string, string, string, map[string]string) error {
	return nil
}
func (m *mockTmux) SetRemainOnExit(string, bool) error { return nil }
func (m *mockTmux) SetEnvironment(string, string, string) error {
	return nil
}
func (m *mockTmux) GetPaneID(string) (string, error)  { return "%0", nil }
func (m *mockTmux) GetPanePID(string) (string, error) { return "1", nil }
func (m *mockTmux) ConfigureGasTownSession(string, *tmux.Theme, string, string, string) error {
	return nil
}
func (m *mockTmux) WaitForCommand(string, []string, time.Duration) error { return nil }
func (m *mockTmux) SetAutoRespawnHook(string) error                      { return nil }
func (m *mockTmux) AcceptStartupDialogs(string) error                    { return nil }
func (m *mockTmux) CheckStartupBlocked(string) error                     { return nil }
func (m *mockTmux) WaitForRuntimeReady(string, *config.RuntimeConfig, time.Duration) error {
	return nil
}
func (m *mockTmux) CheckSessionHealth(string, time.Duration) tmux.ZombieStatus {
	return tmux.SessionHealthy
}
func (m *mockTmux) SendKeysRaw(string, string) error  { return nil }
func (m *mockTmux) SetCrewCycleBindings(string) error { return nil }

func TestStop_KillsSession(t *testing.T) {
	mock := &mockTmux{hasSession: true}
	m := &Manager{
		rig:  &rig.Rig{Name: "wyvern", Path: t.TempDir()},
		tmux: mock,
	}
	if err := m.Stop("Toast"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(mock.killCalls) != 1 {
		t.Fatalf("kill calls = %d, want 1", len(mock.killCalls))
	}
}

func TestStop_MissingSession(t *testing.T) {
	mock := &mockTmux{hasSession: false}
	m := &Manager{
		rig:  &rig.Rig{Name: "wyvern", Path: t.TempDir()},
		tmux: mock,
	}
	err := m.Stop("Toast")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
}
