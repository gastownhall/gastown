package doctor

import (
	"fmt"
	"testing"

	"github.com/jonbaldie/gastown/internal/tmux"
)

// mockGlobalEnvAccessor implements GlobalEnvAccessor for unit tests.
type mockGlobalEnvAccessor struct {
	env map[string]string
	err error // returned by GetGlobalEnvironment when non-nil
}

func (m *mockGlobalEnvAccessor) GetGlobalEnvironment(key string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	val, ok := m.env[key]
	if !ok {
		// Mimic tmux behavior: variable not found returns a non-sentinel error.
		return "", fmt.Errorf("unknown variable: %s", key)
	}
	return val, nil
}

func (m *mockGlobalEnvAccessor) SetGlobalEnvironment(key, value string) error {
	if m.env == nil {
		m.env = make(map[string]string)
	}
	m.env[key] = value
	return nil
}

func TestTmuxGlobalEnvCheck_Metadata(t *testing.T) {
	check := NewTmuxGlobalEnvCheck()

	if check.Name() != "tmux-global-env" {
		t.Errorf("expected name 'tmux-global-env', got %q", check.Name())
	}
	if !check.CanFix() {
		t.Error("expected CanFix to return true")
	}
	if check.Category() != CategoryInfrastructure {
		t.Errorf("expected category %q, got %q", CategoryInfrastructure, check.Category())
	}
}

func TestTmuxGlobalEnvCheck_Missing(t *testing.T) {
	// GT_TOWN_ROOT not set — should warn, fix should set it, re-run should pass.
	mock := &mockGlobalEnvAccessor{env: map[string]string{}}
	check := NewTmuxGlobalEnvCheckWithAccessor(mock)
	ctx := &CheckContext{TownRoot: "/home/user/gt"}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning when GT_TOWN_ROOT missing, got %v: %s", result.Status, result.Message)
	}

	// Fix should set the value.
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() failed: %v", err)
	}

	// Re-run should pass.
	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK after fix, got %v: %s", result.Status, result.Message)
	}
}

func TestTmuxGlobalEnvCheck_WrongValue(t *testing.T) {
	// GT_TOWN_ROOT set to wrong path — should warn, fix should correct it.
	mock := &mockGlobalEnvAccessor{env: map[string]string{
		"GT_TOWN_ROOT": "/old/path",
	}}
	check := NewTmuxGlobalEnvCheckWithAccessor(mock)
	ctx := &CheckContext{TownRoot: "/home/user/gt"}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning when GT_TOWN_ROOT wrong, got %v: %s", result.Status, result.Message)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() failed: %v", err)
	}

	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK after fix, got %v: %s", result.Status, result.Message)
	}
}

func TestTmuxGlobalEnvCheck_Correct(t *testing.T) {
	// GT_TOWN_ROOT already correct — should pass.
	mock := &mockGlobalEnvAccessor{env: map[string]string{
		"GT_TOWN_ROOT": "/home/user/gt",
	}}
	check := NewTmuxGlobalEnvCheckWithAccessor(mock)
	ctx := &CheckContext{TownRoot: "/home/user/gt"}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when GT_TOWN_ROOT correct, got %v: %s", result.Status, result.Message)
	}
}

func TestTmuxGlobalEnvCheck_NoTmuxServer(t *testing.T) {
	// No tmux server — should be OK (nothing to check).
	mock := &mockGlobalEnvAccessor{err: tmux.ErrNoServer}
	check := NewTmuxGlobalEnvCheckWithAccessor(mock)
	ctx := &CheckContext{TownRoot: "/home/user/gt"}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when no tmux server, got %v: %s", result.Status, result.Message)
	}
}
