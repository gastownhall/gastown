package dog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeGuardianFile(t *testing.T, townRoot, contents string) {
	t.Helper()
	path := GuardianFile(townRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir guardian dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatalf("write guardian file: %v", err)
	}
}

func TestLoadGuardianState_MissingFileAllowsDispatch(t *testing.T) {
	state, err := LoadGuardianState(t.TempDir())
	if err != nil {
		t.Fatalf("LoadGuardianState() error = %v", err)
	}
	if !state.DispatchAllowed || !state.ActivationAllowed {
		t.Fatalf("missing guardian file should allow both flags, got %+v", state)
	}
}

func TestRequireDispatchAllowed_RedDenies(t *testing.T) {
	townRoot := t.TempDir()
	writeGuardianFile(t, townRoot, `{"dispatch_allowed":false,"activation_allowed":false,"reason":"integrity red"}`)

	err := RequireDispatchAllowed(townRoot)
	if !errors.Is(err, ErrGuardianDenied) {
		t.Fatalf("RequireDispatchAllowed() error = %v, want ErrGuardianDenied", err)
	}
}

func TestRequireDispatchAllowed_GreenAllows(t *testing.T) {
	townRoot := t.TempDir()
	writeGuardianFile(t, townRoot, `{"dispatch_allowed":true,"activation_allowed":true}`)

	if err := RequireDispatchAllowed(townRoot); err != nil {
		t.Fatalf("RequireDispatchAllowed() green error = %v", err)
	}
	if err := RequireActivationAllowed(townRoot); err != nil {
		t.Fatalf("RequireActivationAllowed() green error = %v", err)
	}
}

func TestRequireActivationAllowed_UnavailableFailsClosed(t *testing.T) {
	townRoot := t.TempDir()
	writeGuardianFile(t, townRoot, `{not json`)

	err := RequireActivationAllowed(townRoot)
	if !errors.Is(err, ErrGuardianDenied) {
		t.Fatalf("corrupt guardian must fail closed, got %v", err)
	}
}

func TestRequireDispatchAllowed_ActivationDeniedStillBlocksInline(t *testing.T) {
	townRoot := t.TempDir()
	writeGuardianFile(t, townRoot, `{"dispatch_allowed":true,"activation_allowed":false,"reason":"activation red"}`)

	if err := RequireDispatchAllowed(townRoot); err != nil {
		t.Fatalf("dispatch should remain allowed, got %v", err)
	}
	err := RequireActivationAllowed(townRoot)
	if !errors.Is(err, ErrGuardianDenied) {
		t.Fatalf("RequireActivationAllowed() error = %v, want ErrGuardianDenied", err)
	}
}
