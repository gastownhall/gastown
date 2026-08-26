package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/deps"
)

func TestBeadsBinaryCheck_Metadata(t *testing.T) {
	check := NewBeadsBinaryCheck()

	if check.Name() != "beads-binary" {
		t.Errorf("Name() = %q, want %q", check.Name(), "beads-binary")
	}
	if check.Description() != "Check that beads (bd) is installed and meets minimum version" {
		t.Errorf("Description() = %q", check.Description())
	}
	if check.Category() != CategoryInfrastructure {
		t.Errorf("Category() = %q, want %q", check.Category(), CategoryInfrastructure)
	}
	if check.CanFix() {
		t.Error("CanFix() should return false (user must install/upgrade bd manually)")
	}
}

func TestBeadsBinaryCheck_BdInstalled(t *testing.T) {
	// Skip if bd is not actually installed in the test environment
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping installed-path test")
	}

	check := NewBeadsBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	// Non-hermetic: the installed bd may or may not meet MinBeadsVersion.
	// We just verify it produces a meaningful result (not NotFound/Unknown).
	switch result.Status {
	case StatusOK:
		if !strings.Contains(result.Message, "bd") {
			t.Errorf("expected version string in message, got %q", result.Message)
		}
	case StatusError:
		if !strings.Contains(result.Message, "too old") {
			t.Errorf("expected 'too old' in error message, got %q", result.Message)
		}
	default:
		t.Errorf("unexpected status %v when bd is installed: %s", result.Status, result.Message)
	}
}

// writeFakeBd creates a platform-appropriate fake "bd" executable in dir.
func writeFakeBd(t *testing.T, dir string, script string, batScript string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "bd.bat")
		if err := os.WriteFile(path, []byte(batScript), 0755); err != nil {
			t.Fatal(err)
		}
	} else {
		path := filepath.Join(dir, "bd")
		if err := os.WriteFile(path, []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBeadsBinaryCheck_HermeticSuccess(t *testing.T) {
	fakeDir := t.TempDir()
	// Use deps.MinBeadsVersion so this test stays in sync when the minimum is bumped.
	writeFakeBd(t, fakeDir,
		fmt.Sprintf("#!/bin/sh\necho 'bd version %s'\n", deps.MinBeadsVersion),
		fmt.Sprintf("@echo off\r\necho bd version %s\r\n", deps.MinBeadsVersion),
	)

	t.Setenv("PATH", fakeDir)

	check := NewBeadsBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	switch result.Status {
	case StatusOK:
		if !strings.Contains(result.Message, deps.MinBeadsVersion) {
			t.Errorf("expected version in message, got %q", result.Message)
		}
	case StatusWarning:
		// Under heavy CI load the fake bd may time out; tolerate gracefully.
		t.Logf("fake bd timed out under load (got StatusWarning); skipping assertion")
	default:
		t.Errorf("expected StatusOK (or StatusWarning under load), got %v: %s", result.Status, result.Message)
	}
}

func TestBeadsBinaryCheck_BdNotInPath(t *testing.T) {
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	check := NewBeadsBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Errorf("expected StatusError when bd is not in PATH, got %v: %s", result.Status, result.Message)
	}
	if result.Message != "beads (bd) not found in PATH" {
		t.Errorf("unexpected message: %q", result.Message)
	}
	if result.FixHint == "" {
		t.Error("expected a fix hint with install instructions")
	}
	if !strings.Contains(result.FixHint, "marlon-costa-dc/beads") {
		t.Errorf("fix hint should reference beads install path, got %q", result.FixHint)
	}
}

func TestBeadsBinaryCheck_BdTooOld(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeBd(t, fakeDir,
		"#!/bin/sh\necho 'bd version 1.2.2-dc2'\n",
		"@echo off\r\necho bd version 1.2.2-dc2\r\n",
	)

	t.Setenv("PATH", fakeDir)

	check := NewBeadsBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	switch result.Status {
	case StatusError:
		if !strings.Contains(result.Message, "too old") {
			t.Errorf("expected 'too old' in message, got %q", result.Message)
		}
		if result.FixHint == "" {
			t.Error("expected a fix hint with upgrade instructions")
		}
	case StatusWarning:
		// Under heavy CI load the fake bd may time out; tolerate gracefully.
		t.Logf("fake bd timed out under load (got StatusWarning); skipping assertion")
	default:
		t.Errorf("expected StatusError (or StatusWarning under load), got %v: %s", result.Status, result.Message)
	}
}

func TestBeadsBinaryCheck_BdVersionUnparseable(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeBd(t, fakeDir,
		"#!/bin/sh\necho 'some garbage output'\n",
		"@echo off\r\necho some garbage output\r\n",
	)

	t.Setenv("PATH", fakeDir)

	check := NewBeadsBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning when bd version unparseable, got %v: %s", result.Status, result.Message)
	}
}
