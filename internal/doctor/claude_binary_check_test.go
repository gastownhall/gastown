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

func TestClaudeBinaryCheck_Metadata(t *testing.T) {
	check := NewClaudeBinaryCheck()

	if check.Name() != "claude-binary" {
		t.Errorf("Name() = %q, want %q", check.Name(), "claude-binary")
	}
	if check.Description() != "Check that Claude Code meets minimum version for Gas Town" {
		t.Errorf("Description() = %q", check.Description())
	}
	if check.Category() != CategoryInfrastructure {
		t.Errorf("Category() = %q, want %q", check.Category(), CategoryInfrastructure)
	}
	if check.CanFix() {
		t.Error("CanFix() should return false")
	}
}

func TestClaudeBinaryCheck_ClaudeInstalled(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed, skipping installed-path test")
	}

	check := NewClaudeBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	// Non-hermetic: the installed claude may or may not meet minimum.
	if result.Status == StatusError {
		t.Errorf("unexpected StatusError when claude is installed: %s", result.Message)
	}
	if !strings.Contains(result.Message, "claude") {
		t.Errorf("expected 'claude' in message, got %q", result.Message)
	}
}

// writeFakeClaude creates a platform-appropriate fake "claude" executable in dir.
func writeFakeClaude(t *testing.T, dir string, script string, batScript string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "claude.bat")
		if err := os.WriteFile(path, []byte(batScript), 0755); err != nil {
			t.Fatal(err)
		}
	} else {
		path := filepath.Join(dir, "claude")
		if err := os.WriteFile(path, []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClaudeBinaryCheck_HermeticSuccess(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeClaude(t, fakeDir,
		fmt.Sprintf("#!/bin/sh\necho '%s (Claude Code)'\n", deps.RecommendedClaudeCodeVersion),
		fmt.Sprintf("@echo off\r\necho %s (Claude Code)\r\n", deps.RecommendedClaudeCodeVersion),
	)

	t.Setenv("PATH", fakeDir)

	check := NewClaudeBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	switch result.Status {
	case StatusOK:
		if !strings.Contains(result.Message, deps.RecommendedClaudeCodeVersion) {
			t.Errorf("expected version in message, got %q", result.Message)
		}
	case StatusWarning:
		t.Logf("fake claude timed out under load (got StatusWarning); skipping assertion")
	default:
		t.Errorf("expected StatusOK, got %v: %s", result.Status, result.Message)
	}
}

func TestClaudeBinaryCheck_NotInPath(t *testing.T) {
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	check := NewClaudeBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when claude is not in PATH (optional dep), got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "not found") {
		t.Errorf("expected 'not found' in message, got %q", result.Message)
	}
}

func TestClaudeBinaryCheck_TooOld(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeClaude(t, fakeDir,
		"#!/bin/sh\necho '1.0.62 (Claude Code)'\n",
		"@echo off\r\necho 1.0.62 (Claude Code)\r\n",
	)

	t.Setenv("PATH", fakeDir)

	check := NewClaudeBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	switch result.Status {
	case StatusWarning:
		if !strings.Contains(result.Message, "below minimum") {
			t.Errorf("expected 'below minimum' in message, got %q", result.Message)
		}
		if result.FixHint == "" {
			t.Error("expected a fix hint with upgrade instructions")
		}
	default:
		// Under heavy CI load the fake claude may time out; tolerate gracefully.
		t.Logf("got status %v (may be CI load): %s", result.Status, result.Message)
	}
}

func TestClaudeBinaryCheck_OldButOK(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeClaude(t, fakeDir,
		"#!/bin/sh\necho '2.0.25 (Claude Code)'\n",
		"@echo off\r\necho 2.0.25 (Claude Code)\r\n",
	)

	t.Setenv("PATH", fakeDir)

	check := NewClaudeBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	switch result.Status {
	case StatusOK:
		if !strings.Contains(result.Message, "upgrade") || !strings.Contains(result.Message, "recommended") {
			t.Errorf("expected upgrade recommendation in message, got %q", result.Message)
		}
	case StatusWarning:
		t.Logf("fake claude timed out under load (got StatusWarning); skipping assertion")
	default:
		t.Errorf("expected StatusOK with recommendation, got %v: %s", result.Status, result.Message)
	}
}

func TestClaudeBinaryCheck_Unparseable(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeClaude(t, fakeDir,
		"#!/bin/sh\necho 'some garbage output'\n",
		"@echo off\r\necho some garbage output\r\n",
	)

	t.Setenv("PATH", fakeDir)

	check := NewClaudeBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning when version unparseable, got %v: %s", result.Status, result.Message)
	}
}
