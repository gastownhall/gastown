package doctor

import (
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/hooks"
)

func TestHooksBaseCheck_Missing(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	check := NewHooksBaseCheck()
	ctx := &CheckContext{TownRoot: tmpDir}
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning when hooks-base.json is missing, got %v: %s", result.Status, result.Message)
	}
}

func TestHooksBaseCheck_Present(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Create a base config
	if err := hooks.SaveBase(hooks.DefaultBase()); err != nil {
		t.Fatalf("SaveBase: %v", err)
	}

	check := NewHooksBaseCheck()
	ctx := &CheckContext{TownRoot: tmpDir}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when hooks-base.json exists, got %v: %s", result.Status, result.Message)
	}
}

func TestHooksBaseCheck_Fix(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	check := NewHooksBaseCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Should warn before fix
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning before fix, got %v", result.Status)
	}

	// Fix should create the file
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix: %v", err)
	}

	// Verify file was created
	if _, err := os.Stat(hooks.BasePath()); err != nil {
		t.Errorf("hooks-base.json not created after Fix: %v", err)
	}

	// Should now pass
	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK after fix, got %v: %s", result.Status, result.Message)
	}
}

func TestHooksBaseCheck_FixIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Pre-create the base config
	if err := hooks.SaveBase(hooks.DefaultBase()); err != nil {
		t.Fatalf("SaveBase: %v", err)
	}

	check := NewHooksBaseCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Fix on already-present file should be a no-op
	if err := check.Fix(ctx); err != nil {
		t.Errorf("Fix on existing base should not error: %v", err)
	}
}
