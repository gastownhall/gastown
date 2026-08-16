package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/deps"
)

func TestDoltBinaryCheck_Metadata(t *testing.T) {
	check := NewDoltBinaryCheck()

	if check.Name() != "dolt-binary" {
		t.Errorf("Name() = %q, want %q", check.Name(), "dolt-binary")
	}
	if check.Description() != "Check that dolt is installed and meets minimum version" {
		t.Errorf("Description() = %q", check.Description())
	}
	if check.Category() != CategoryInfrastructure {
		t.Errorf("Category() = %q, want %q", check.Category(), CategoryInfrastructure)
	}
	if check.CanFix() {
		t.Error("CanFix() should return false (user must install dolt manually)")
	}
}

// writeFakeDolt creates a platform-appropriate fake "dolt" executable in dir.
func writeFakeDolt(t *testing.T, dir string, script string, batScript string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "dolt.bat")
		if err := os.WriteFile(path, []byte(batScript), 0755); err != nil {
			t.Fatal(err)
		}
	} else {
		path := filepath.Join(dir, "dolt")
		if err := os.WriteFile(path, []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDoltBinaryCheck_DoltInstalled(t *testing.T) {
	// Skip if dolt is not actually installed in the test environment
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed, skipping installed-path test")
	}

	check := NewDoltBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	// Non-hermetic: the installed dolt may or may not meet MinDoltVersion.
	switch result.Status {
	case StatusOK:
		if !strings.Contains(result.Message, "dolt") {
			t.Errorf("expected version string in message, got %q", result.Message)
		}
	case StatusError:
		if !strings.Contains(result.Message, "too old") {
			t.Errorf("expected 'too old' in error message, got %q", result.Message)
		}
	default:
		t.Errorf("unexpected status %v when dolt is installed: %s", result.Status, result.Message)
	}
}

func TestDoltBinaryCheck_HermeticSuccess(t *testing.T) {
	fakeDir := t.TempDir()
	// Use deps.MinDoltVersion so this test stays in sync when the minimum is bumped.
	writeFakeDolt(t, fakeDir,
		fmt.Sprintf("#!/bin/sh\necho 'dolt version %s'\n", deps.MinDoltVersion),
		fmt.Sprintf("@echo off\r\necho dolt version %s\r\n", deps.MinDoltVersion),
	)

	t.Setenv("PATH", fakeDir)

	check := NewDoltBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK with fake dolt at min version, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, deps.MinDoltVersion) {
		t.Errorf("expected version in message, got %q", result.Message)
	}
}

func TestDoltBinaryCheck_DoltNotInPath(t *testing.T) {
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	check := NewDoltBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Errorf("expected StatusError when dolt is not in PATH, got %v: %s", result.Status, result.Message)
	}
	if result.Message != "dolt not found in PATH" {
		t.Errorf("unexpected message: %q", result.Message)
	}
	if result.FixHint == "" {
		t.Error("expected a fix hint with install instructions")
	}
	if !strings.Contains(result.FixHint, "dolthub/dolt") {
		t.Errorf("fix hint should reference dolthub/dolt, got %q", result.FixHint)
	}
}

func TestDoltBinaryCheck_DoltTooOld(t *testing.T) {
	fakeDir := t.TempDir()
	// Use a structurally safe low version to ensure test intent is clear
	// regardless of future MinDoltVersion changes.
	writeFakeDolt(t, fakeDir,
		"#!/bin/sh\necho 'dolt version 0.0.1'\n",
		"@echo off\r\necho dolt version 0.0.1\r\n",
	)

	t.Setenv("PATH", fakeDir)

	check := NewDoltBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Errorf("expected StatusError for too-old dolt, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "too old") {
		t.Errorf("expected 'too old' in message, got %q", result.Message)
	}
	if result.FixHint == "" {
		t.Error("expected a fix hint with upgrade instructions")
	}
}

func TestDoltBinaryCheck_DoltVersionFails(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeDolt(t, fakeDir,
		"#!/bin/sh\nexit 1\n",
		"@echo off\r\nexit /b 1\r\n",
	)

	t.Setenv("PATH", fakeDir)

	check := NewDoltBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	// When dolt version fails to execute, deps.CheckDolt returns DoltExecFailed → StatusError
	if result.Status != StatusError {
		t.Errorf("expected StatusError when dolt version fails, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "failed") {
		t.Errorf("expected 'failed' in message, got %q", result.Message)
	}
}

func TestDoltBinaryCheck_DoltVersionUnparseable(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeDolt(t, fakeDir,
		"#!/bin/sh\necho 'some garbage output'\n",
		"@echo off\r\necho some garbage output\r\n",
	)

	t.Setenv("PATH", fakeDir)

	check := NewDoltBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning when dolt version unparseable, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "could not be parsed") {
		t.Errorf("expected parse failure detail in message, got %q", result.Message)
	}
}
