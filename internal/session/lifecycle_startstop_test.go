package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/nudge"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/jonbaldie/gastown/internal/worker"
)

type mockTmux struct {
	hasSession    bool
	hasSessionErr error
	agentAlive    bool
	killErr       error
	newSessionErr error
	waitErr       error
	readyErr      error
	blockedErr    error
	health        tmux.ZombieStatus

	killCalls  []string
	waitCalls  int
	readyCalls int
	enterCalls int
}

func (m *mockTmux) HasSession(string) (bool, error) { return m.hasSession, m.hasSessionErr }
func (m *mockTmux) IsAgentAlive(string) bool        { return m.agentAlive }
func (m *mockTmux) KillSessionWithProcesses(name string) error {
	m.killCalls = append(m.killCalls, name)
	return m.killErr
}
func (m *mockTmux) NewSessionWithCommandAndEnv(_, _, _ string, _ map[string]string) error {
	m.hasSession = true
	return m.newSessionErr
}
func (m *mockTmux) SetRemainOnExit(string, bool) error { return nil }
func (m *mockTmux) SetEnvironment(_, _, _ string) error {
	return nil
}
func (m *mockTmux) GetPaneID(string) (string, error)  { return "%0", nil }
func (m *mockTmux) GetPanePID(string) (string, error) { return "1", nil }
func (m *mockTmux) ConfigureGasTownSession(string, *tmux.Theme, string, string, string) error {
	return nil
}
func (m *mockTmux) WaitForCommand(string, []string, time.Duration) error {
	m.waitCalls++
	return m.waitErr
}
func (m *mockTmux) SetAutoRespawnHook(string) error   { return nil }
func (m *mockTmux) AcceptStartupDialogs(string) error { return nil }
func (m *mockTmux) CheckStartupBlocked(string) error  { return m.blockedErr }
func (m *mockTmux) WaitForRuntimeReady(string, *config.RuntimeConfig, time.Duration) error {
	m.readyCalls++
	return m.readyErr
}
func (m *mockTmux) CheckSessionHealth(string, time.Duration) tmux.ZombieStatus {
	return m.health
}
func (m *mockTmux) SendKeysRaw(_, keys string) error {
	if keys == "C-c" {
		m.enterCalls++
	}
	return nil
}

type lifecycleWorkerTmux struct{}

func (*lifecycleWorkerTmux) HasSession(string) (bool, error)                 { return true, nil }
func (*lifecycleWorkerTmux) NudgeSession(string, string) error               { return nil }
func (*lifecycleWorkerTmux) WaitForRuntimeReady(string, time.Duration) error { return nil }
func (*lifecycleWorkerTmux) WaitForIdle(string, time.Duration) error         { return nil }
func (*lifecycleWorkerTmux) IsAgentAlive(string) bool                        { return true }
func (*lifecycleWorkerTmux) KillSessionWithProcesses(string) error           { return nil }
func (*lifecycleWorkerTmux) CheckSessionHealth(string, time.Duration) string {
	return tmux.SessionHealthy.String()
}

func TestStopSession_StopsNudgePoller(t *testing.T) {
	townRoot := t.TempDir()
	sessionID := "gt-test-crew"
	pidDir := filepath.Join(townRoot, constants.DirRuntime, "nudge_poller")
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(pidDir, sessionID+".pid")
	if err := os.WriteFile(pidPath, []byte("999999\n"), 0644); err != nil {
		t.Fatal(err)
	}

	mock := &mockTmux{hasSession: true}
	if err := StopSession(mock, townRoot, sessionID, false); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("poller pid file still present: %v", err)
	}
	if len(mock.killCalls) != 1 {
		t.Fatalf("kill calls = %d, want 1", len(mock.killCalls))
	}
}

func TestStopSession_MarksWorkerRunStopped(t *testing.T) {
	townRoot := t.TempDir()
	w, err := worker.Listen(townRoot, &lifecycleWorkerTmux{})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	run, err := w.StartRun(ctx, worker.StartSpec{SessionID: "hq-mayor", Role: "mayor"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	mock := &mockTmux{hasSession: true}
	if err := StopSession(mock, townRoot, "hq-mayor", false); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	stopped, err := worker.ReadRun(townRoot, run.RunID)
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if stopped.State != worker.StateStopped || stopped.StoppedAt.IsZero() {
		t.Fatalf("run = %+v, want stopped", stopped)
	}
}

func TestStopSession_GracefulSendsInterrupt(t *testing.T) {
	mock := &mockTmux{hasSession: true}
	if err := StopSession(mock, t.TempDir(), "hq-mayor", true); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if mock.enterCalls != 1 {
		t.Fatalf("interrupt sends = %d, want 1", mock.enterCalls)
	}
}

func TestStopSession_MissingSessionStillStopsPoller(t *testing.T) {
	townRoot := t.TempDir()
	sessionID := "gt-missing"
	if err := nudge.StopPoller(townRoot, sessionID); err != nil {
		t.Fatal(err)
	}
	pidDir := filepath.Join(townRoot, constants.DirRuntime, "nudge_poller")
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(pidDir, sessionID+".pid")
	if err := os.WriteFile(pidPath, []byte("999999\n"), 0644); err != nil {
		t.Fatal(err)
	}

	mock := &mockTmux{hasSession: false}
	err := StopSession(mock, townRoot, sessionID, false)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatal("poller pid file should be removed even when the session is gone")
	}
}

func TestStartSession_BootDoesNotWait(t *testing.T) {
	dir := t.TempDir()
	mock := &mockTmux{
		waitErr:  errors.New("agent missing"),
		readyErr: errors.New("not ready"),
	}
	_, err := StartSession(mock, "boot", Work{
		SessionID: "hq-boot",
		WorkDir:   dir,
		Command:   "true",
	})
	if err != nil {
		t.Fatalf("boot start: %v", err)
	}
	if mock.waitCalls != 0 {
		t.Fatalf("boot waited for agent %d times", mock.waitCalls)
	}
	if mock.readyCalls != 0 {
		t.Fatalf("boot waited for ready %d times", mock.readyCalls)
	}
}

func TestStartSession_RefineryDoesNotWaitForAgent(t *testing.T) {
	dir := t.TempDir()
	mock := &mockTmux{waitErr: errors.New("agent missing")}
	_, err := StartSession(mock, "refinery", Work{
		SessionID: "gt-rig-refinery",
		WorkDir:   dir,
		Command:   "true",
	})
	if err != nil {
		t.Fatalf("refinery start: %v", err)
	}
	if mock.waitCalls != 0 {
		t.Fatalf("refinery waited for agent %d times", mock.waitCalls)
	}
}

func TestStartSession_RefineryReadyWaitIsFatal(t *testing.T) {
	dir := t.TempDir()
	mock := &mockTmux{readyErr: errors.New("not ready")}
	_, err := StartSession(mock, "refinery", Work{
		SessionID: "gt-rig-refinery",
		WorkDir:   dir,
		Command:   "true",
	})
	if err == nil {
		t.Fatal("expected fatal ready wait")
	}
	if len(mock.killCalls) == 0 {
		t.Fatal("expected the half-started session to be killed")
	}
}

func TestStartSession_MayorAgentWaitIsFatal(t *testing.T) {
	dir := t.TempDir()
	mock := &mockTmux{waitErr: errors.New("timeout")}
	_, err := StartSession(mock, "mayor", Work{
		SessionID: "hq-mayor",
		WorkDir:   dir,
		Command:   "true",
	})
	if err == nil {
		t.Fatal("expected fatal agent wait")
	}
	if len(mock.killCalls) == 0 {
		t.Fatal("expected the half-started session to be killed")
	}
}

func TestKillExistingSession_StopsNudgePoller(t *testing.T) {
	townRoot := t.TempDir()
	sessionID := "gt-test-zombie"
	pidDir := filepath.Join(townRoot, constants.DirRuntime, "nudge_poller")
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(pidDir, sessionID+".pid")
	if err := os.WriteFile(pidPath, []byte("999999\n"), 0644); err != nil {
		t.Fatal(err)
	}

	mock := &mockTmux{hasSession: true, agentAlive: false}
	killed, err := KillExistingSession(mock, townRoot, sessionID, true)
	if err != nil {
		t.Fatalf("KillExistingSession: %v", err)
	}
	if !killed {
		t.Fatal("expected zombie session to be killed")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("poller pid file still present after start-time kill: %v", err)
	}
}

func TestKillExistingSession_AliveAgentUntouched(t *testing.T) {
	mock := &mockTmux{hasSession: true, agentAlive: true}
	killed, err := KillExistingSession(mock, t.TempDir(), "gt-live", true)
	if !errors.Is(err, ErrSessionAlive) {
		t.Fatalf("err = %v, want ErrSessionAlive", err)
	}
	if killed {
		t.Fatal("must not kill a live agent")
	}
	if len(mock.killCalls) != 0 {
		t.Fatalf("kill calls = %d, want 0", len(mock.killCalls))
	}
}
